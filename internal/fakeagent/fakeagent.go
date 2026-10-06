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
//	FAKE_AGENT_STOP_REASON      stop reason returned by session/prompt
//	FAKE_AGENT_FAIL_INIT=1      answer initialize with a JSON-RPC error
//	FAKE_AGENT_READ_PATH=/x     request fs/read_text_file during the turn
//	FAKE_AGENT_WRITE_PATH=/x    request fs/write_text_file during the turn
//	FAKE_AGENT_REQUEST_PERM=1   request session/request_permission during the turn
//	FAKE_AGENT_ENVELOPE=name    reply with a tool-call envelope for that function
//	FAKE_AGENT_ENVELOPE_PREFIX  prose to emit before the envelope
//	FAKE_AGENT_ENVELOPE_PARTS   how many deltas to split the envelope into
//	FAKE_AGENT_ENVELOPE_ONCE=1  emit the envelope only on the first turn
//	FAKE_AGENT_ECHO=1           reply with the prompt it received
//	FAKE_AGENT_REPLY=text       reply with this text
//	FAKE_AGENT_REPLY_AFTER=text reply with this text from the second turn on
//	FAKE_AGENT_IMAGES=1         advertise image prompt support
//	FAKE_AGENT_REQUIRE_AUTH=1   advertise an auth method and refuse session/new
//	                            until authenticate is called
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
	defer a.out.Flush()

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
			MethodID string `json:"methodId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.MethodID == "" {
			a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{"code": -32602, "message": "methodId is required"}})
			return
		}
		a.mu.Lock()
		a.authenticated = true
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
				"availableModes": []any{map[string]any{"id": "build", "name": "Build"}, map[string]any{"id": "plan", "name": "Plan"}},
			},
			"configOptions": []any{map[string]any{
				"id":           "model",
				"category":     "model",
				"currentValue": "fake-model-1",
				"options": []any{
					map[string]any{"value": "fake-model-1", "name": "Fake Model 1"},
					map[string]any{"value": "fake-model-2", "name": "Fake Model 2"},
				},
			}},
		})
	case "session/prompt":
		a.turn(msg)
	default:
		a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(msg.ID), "error": map[string]any{"code": -32601, "message": "unknown method " + msg.Method}})
	}
}
