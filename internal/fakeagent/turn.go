package fakeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

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

	for _, piece := range a.thoughtPieces() {
		a.notify("session/update", map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"content":       map[string]any{"type": "text", "text": piece},
			},
		})
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

// thoughtPieces returns the reasoning deltas for one turn, emitted before the
// answer as agent_thought_chunk updates.
func (a *agent) thoughtPieces() []string {
	thought := os.Getenv("FAKE_AGENT_THOUGHT")
	if thought == "" {
		return nil
	}
	return splitEvery(thought, envInt("FAKE_AGENT_CHUNKS", 2))
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
