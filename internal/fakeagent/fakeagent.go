// Package fakeagent is a scriptable ACP agent used by tests in place of a real
// agent CLI. It is never imported by production code.
//
// A test spawns it by re-executing its own test binary with the marker
// environment variable set (see MaybeRun). Behaviour is steered through
// environment variables so one fixture covers many scenarios without new
// binaries:
//
//	ACP2API_FAKE_AGENT=1        required marker; runs the agent loop
//	FAKE_AGENT_CHUNKS=3         number of agent_message_chunk updates per turn
//	FAKE_AGENT_CHUNK_DELAY_MS=n wait this long between answer chunks, so a turn
//	                            spans time and interleaves with another session
//	FAKE_AGENT_THOUGHT=text     emit this text as agent_thought_chunk updates
//	                            before the answer, split by FAKE_AGENT_CHUNKS
//	FAKE_AGENT_STOP_REASON      stop reason returned by session/prompt
//	FAKE_AGENT_FAIL_INIT=1      answer initialize with a JSON-RPC error
//	FAKE_AGENT_READ_PATH=/x     request fs/read_text_file during the turn
//	FAKE_AGENT_WRITE_PATH=/x    request fs/write_text_file during the turn
//	FAKE_AGENT_FS_REPORT=1      append the outcome of those calls to the reply
//	FAKE_AGENT_REQUEST_PERM=1   request session/request_permission during the turn
//	FAKE_AGENT_ENVELOPE=name    reply with a tool-call envelope for that function
//	FAKE_AGENT_ENVELOPE_PREFIX  prose to emit before the envelope
//	FAKE_AGENT_ENVELOPE_PARTS   how many deltas to split the envelope into
//	FAKE_AGENT_ENVELOPE_ONCE=1  emit the envelope only on the first turn
//	FAKE_AGENT_TOOL_NAME=name   emit a tool_call with this programmatic name
//	                            (e.g. exec or mcp__github__create_issue)
//	FAKE_AGENT_TOOL_TITLE=text  title for the emitted tool_call
//	FAKE_AGENT_TOOL_KIND=kind   kind for the emitted tool_call
//	FAKE_AGENT_ECHO=1           reply with the prompt it received
//	FAKE_AGENT_ECHO_STATE=1     reply with "mode=<mode> model=<model>" as last
//	                            selected through session/set_config_option
//	FAKE_AGENT_MODELS=a,b,c     advertise these model ids instead of the
//	                            default pair, for effort-suffix variants
//	FAKE_AGENT_REPLY=text       reply with this text
//	FAKE_AGENT_REPLY_AFTER=text reply with this text from the second turn on
//	FAKE_AGENT_IMAGES=1         advertise image prompt support
//	FAKE_AGENT_REQUIRE_AUTH=1   advertise an auth method and refuse session/new
//	                            until authenticate is called
//	FAKE_AGENT_REQUIRE_API_KEY=1 refuse authenticate unless _meta.api_key is set
//	FAKE_AGENT_EXIT_AFTER=1     exit the process right after the first turn
package fakeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// fakeModels is the model catalog every fake session advertises, unless the
// test overrides it wholesale through FAKE_AGENT_MODELS.
var fakeModels = []map[string]any{
	{"value": "fake-model-1", "name": "Fake Model 1"},
	{"value": "fake-model-2", "name": "Fake Model 2"},
}

// fakeCatalog returns the advertised model list: the caller's override when
// FAKE_AGENT_MODELS carries a comma-separated id list, else the default pair.
func fakeCatalog() []map[string]any {
	list := os.Getenv("FAKE_AGENT_MODELS")
	if list == "" {
		return fakeModels
	}
	models := make([]map[string]any, 0)
	for _, id := range strings.Split(list, ",") {
		if id = strings.TrimSpace(id); id != "" {
			models = append(models, map[string]any{"value": id, "name": id})
		}
	}
	if len(models) == 0 {
		return fakeModels
	}
	return models
}

// fakeModes is the session mode list every fake session advertises.
var fakeModes = []map[string]any{
	{"id": "build", "name": "Build"},
	{"id": "plan", "name": "Plan"},
	{"id": "ask", "name": "Ask"},
}

// EnvVar is the marker environment variable that turns a process into the
// fake agent.
const EnvVar = "ACP2API_FAKE_AGENT"

// MaybeRun runs the fake agent when the marker is set. It reports whether the
// current process became the agent, so a TestMain can bail out early:
//
//	func TestMain(m *testing.M) {
//		if fakeagent.MaybeRun() {
//			return
//		}
//		os.Exit(m.Run())
//	}
func MaybeRun() bool {
	if os.Getenv(EnvVar) != "1" {
		return false
	}
	run()
	return true
}

// Env builds the child environment for spawning the fake agent from a test
// binary at path: the parent environment plus the marker and any overrides.
func Env(overrides map[string]string) []string {
	env := append(os.Environ(), EnvVar+"=1")
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type agent struct {
	mu      sync.Mutex
	out     *bufio.Writer
	nextID  int64
	pending map[int64]chan message

	sessions      int
	turns         int
	authenticated bool
	authMetaKeys  int
	mode          string
	model         string
	models        []map[string]any
}

// models returns the catalog this process advertises, resolved once at startup
// so the session/new offer and the set_config_option check cannot diverge.
func (a *agent) catalog() []map[string]any {
	if a.models == nil {
		a.models = fakeCatalog()
	}
	return a.models
}

// Model reports the model the gateway last selected.
func (a *agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// Mode reports the session mode the gateway last selected.
func (a *agent) Mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// AuthMetaKeys reports how many `_meta` entries the last authenticate carried,
// which is how a test proves an API key was actually sent.
func (a *agent) AuthMetaKeys() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authMetaKeys
}

// isAuthenticated reports whether authenticate has been called.
func (a *agent) isAuthenticated() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.authenticated
}

func run() {
	a := &agent{
		out:     bufio.NewWriter(os.Stdout),
		pending: make(map[int64]chan message),
	}
	defer func() { _ = a.out.Flush() }()

	in := bufio.NewReaderSize(os.Stdin, 64*1024)
	for {
		line, err := in.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			a.handleLine(line)
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, "fakeagent: read:", err)
			}
			return
		}
	}
}

func (a *agent) handleLine(line []byte) {
	var msg message
	if err := json.Unmarshal(line, &msg); err != nil {
		return
	}
	switch {
	case msg.Method != "" && msg.ID != nil:
		go a.handleRequest(msg)
	case msg.ID != nil:
		a.resolve(msg)
	}
}

func (a *agent) resolve(msg message) {
	var id int64
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return
	}
	a.mu.Lock()
	ch, ok := a.pending[id]
	if ok {
		delete(a.pending, id)
	}
	a.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func (a *agent) handleRequest(msg message) {
	switch msg.Method {
	case "initialize":
		if os.Getenv("FAKE_AGENT_FAIL_INIT") == "1" {
			a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{"code": -32603, "message": "init refused"}})
			return
		}
		promptCaps := map[string]any{}
		if os.Getenv("FAKE_AGENT_IMAGES") == "1" {
			promptCaps["image"] = true
		}
		authMethods := []any{}
		if os.Getenv("FAKE_AGENT_REQUIRE_AUTH") == "1" {
			authMethods = append(authMethods, map[string]any{"id": "fake-login", "name": "Log in"})
		}
		a.result(msg.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession":        false,
				"promptCapabilities": promptCaps,
			},
			"agentInfo":   map[string]any{"name": "fakeagent", "version": "0.0.1"},
			"authMethods": authMethods,
		})
	case "authenticate":
		var p struct {
			MethodID string         `json:"methodId"`
			Meta     map[string]any `json:"_meta"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.MethodID == "" {
			a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{"code": -32602, "message": "methodId is required"}})
			return
		}
		if os.Getenv("FAKE_AGENT_REQUIRE_API_KEY") == "1" {
			if key, _ := p.Meta["api_key"].(string); key == "" {
				a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{
					"code": -32000, "message": "an api_key is required in _meta",
				}})
				return
			}
		}
		a.mu.Lock()
		a.authenticated = true
		a.authMetaKeys = len(p.Meta)
		a.mu.Unlock()
		// A real agent answers with null; the gateway must tolerate that.
		a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "result": nil})
	case "session/new":
		if os.Getenv("FAKE_AGENT_REQUIRE_AUTH") == "1" && !a.isAuthenticated() {
			a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{
				"code": -32000, "message": "ACP host has not authenticated",
			}})
			return
		}
		a.mu.Lock()
		a.sessions++
		n := a.sessions
		a.mu.Unlock()
		a.result(msg.ID, map[string]any{
			"sessionId": fmt.Sprintf("fake-session-%d", n),
			"modes": map[string]any{
				"currentModeId":  "build",
				"availableModes": fakeModes,
			},
			"configOptions": []any{map[string]any{
				"id":           "model",
				"category":     "model",
				"currentValue": a.catalog()[0]["value"],
				"options":      a.catalog(),
			}},
		})
	case "session/set_mode":
		var p struct {
			SessionID string `json:"sessionId"`
			ModeID    string `json:"modeId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		known := false
		for _, mode := range fakeModes {
			if mode["id"] == p.ModeID {
				known = true
				break
			}
		}
		if !known {
			a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{
				"code": -32602, "message": "unknown mode " + p.ModeID,
			}})
			return
		}
		a.mu.Lock()
		a.mode = p.ModeID
		a.mu.Unlock()
		a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "result": nil})
	case "session/set_config_option":
		var p struct {
			ConfigID string `json:"configId"`
			Value    string `json:"value"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.ConfigID == "model" {
			known := false
			for _, model := range a.catalog() {
				if model["value"] == p.Value {
					known = true
					break
				}
			}
			if !known {
				a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{
					"code": -32602, "message": "unknown model " + p.Value,
				}})
				return
			}
			a.mu.Lock()
			a.model = p.Value
			a.mu.Unlock()
		}
		a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "result": nil})
	case "session/prompt":
		a.turn(msg)
	default:
		a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{"code": -32601, "message": "unknown method " + msg.Method}})
	}
}
