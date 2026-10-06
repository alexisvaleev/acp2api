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
//	FAKE_AGENT_EXIT_AFTER=1     exit the process right after the first turn
package fakeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
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

	sessions int
	turns    int
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
		a.result(msg.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession":        false,
				"promptCapabilities": promptCaps,
			},
			"agentInfo":   map[string]any{"name": "fakeagent", "version": "0.0.1"},
			"authMethods": []any{},
		})
	case "session/new":
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

// turn streams a canned reply and answers the prompt request.
func (a *agent) turn(msg message) {
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	sessionID := p.SessionID
	if sessionID == "" {
		sessionID = "fake-session-1"
	}

	prompt := ""
	for _, block := range p.Prompt {
		if block.Text != "" {
			prompt += block.Text
			continue
		}
		// Non-text blocks are reported by type, which is how the tests prove an
		// image reached the agent as an image block rather than as a placeholder.
		prompt += "[" + block.Type + "]"
	}

	for _, piece := range a.turnPieces(prompt) {
		a.notify("session/update", map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": piece},
			},
		})
	}

	if path := os.Getenv("FAKE_AGENT_READ_PATH"); path != "" {
		_, _ = a.request("fs/read_text_file", map[string]any{"sessionId": sessionID, "path": path})
	}

	if path := os.Getenv("FAKE_AGENT_WRITE_PATH"); path != "" {
		_, _ = a.request("fs/write_text_file", map[string]any{
			"sessionId": sessionID,
			"path":      path,
			"content":   os.Getenv("FAKE_AGENT_WRITE_CONTENT"),
		})
	}

	if os.Getenv("FAKE_AGENT_REQUEST_PERM") == "1" {
		_, _ = a.request("session/request_permission", map[string]any{
			"sessionId": sessionID,
			"toolCall":  map[string]any{"toolCallId": "tc-1", "title": "Run a command", "kind": "execute"},
			"options": []any{
				map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
				map[string]any{"optionId": "deny", "name": "Deny", "kind": "reject_once"},
			},
		})
	}

	stop := os.Getenv("FAKE_AGENT_STOP_REASON")
	if stop == "" {
		stop = "end_turn"
	}
	a.result(msg.ID, map[string]any{"stopReason": stop})

	if os.Getenv("FAKE_AGENT_EXIT_AFTER") == "1" {
		a.out.Flush()
		os.Exit(0)
	}
}

// turnPieces returns the text deltas for one turn.
//
// In envelope mode the deltas are deliberately split mid-JSON, which is what
// exercises the gateway's stream hold-back: a naive implementation leaks half
// an envelope to the client as prose.
func (a *agent) turnPieces(prompt string) []string {
	a.mu.Lock()
	a.turns++
	turn := a.turns
	a.mu.Unlock()

	if name := os.Getenv("FAKE_AGENT_ENVELOPE"); name != "" && !(turn > 1 && os.Getenv("FAKE_AGENT_ENVELOPE_ONCE") == "1") {
		envelope := fmt.Sprintf(
			`{"tool_calls":[{"id":"call_1","type":"function","function":{"name":%q,"arguments":"{\"city\":\"Paris\"}"}}]}`,
			name,
		)
		pieces := make([]string, 0, 4)
		if prefix := os.Getenv("FAKE_AGENT_ENVELOPE_PREFIX"); prefix != "" {
			pieces = append(pieces, prefix)
		}
		return append(pieces, splitEvery(envelope, envInt("FAKE_AGENT_ENVELOPE_PARTS", 3))...)
	}

	if os.Getenv("FAKE_AGENT_ECHO") == "1" {
		return splitEvery(prompt, envInt("FAKE_AGENT_CHUNKS", 2))
	}

	// A literal reply, optionally different from the second turn on, which is
	// how the structured-output retry is exercised.
	if reply := os.Getenv("FAKE_AGENT_REPLY"); reply != "" {
		if after := os.Getenv("FAKE_AGENT_REPLY_AFTER"); after != "" && turn > 1 {
			reply = after
		}
		return splitEvery(reply, envInt("FAKE_AGENT_CHUNKS", 2))
	}

	chunks := envInt("FAKE_AGENT_CHUNKS", 2)
	pieces := make([]string, 0, chunks)
	for i := 0; i < chunks; i++ {
		pieces = append(pieces, fmt.Sprintf("chunk%d ", i+1))
	}
	return pieces
}

// splitEvery splits s into at most n roughly equal parts.
func splitEvery(s string, n int) []string {
	if n < 2 || len(s) < n {
		return []string{s}
	}
	size := (len(s) + n - 1) / n
	parts := make([]string, 0, n)
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		parts = append(parts, s[i:end])
	}
	return parts
}

/* ---- wire helpers ---- */

func (a *agent) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.out.Write(append(data, '\n'))
	_ = a.out.Flush()
}

func (a *agent) result(id json.RawMessage, result any) {
	a.write(map[string]any{"jsonrpc": "2.0", "id": rawID(id), "result": result})
}

func (a *agent) notify(method string, params any) {
	a.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// request sends a client→agent request and waits for its response.
func (a *agent) request(method string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	a.nextID++
	id := a.nextID
	ch := make(chan message, 1)
	a.pending[id] = ch
	a.mu.Unlock()

	a.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})

	msg := <-ch
	if msg.Error != nil {
		return nil, fmt.Errorf("fakeagent: %s: %s", method, msg.Error.Message)
	}
	return msg.Result, nil
}

// rawID renders an id as a JSON value, defaulting to 0 when absent.
func rawID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("0")
	}
	return id
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
