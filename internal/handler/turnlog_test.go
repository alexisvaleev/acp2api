package handler_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// logCapture returns a buffer and a debug-level logger writing to it, so a test
// can assert on the tool-call records the server emits.
func logCapture() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestLogsExternalMCPToolCall(t *testing.T) {
	buf, log := logCapture()
	srv := newTestServerWithOptions(t, testOptions{
		logger: log,
		env: map[string]string{
			"FAKE_AGENT_TOOL_NAME":  "mcp__github__create_issue",
			"FAKE_AGENT_TOOL_TITLE": "Create issue",
			"FAKE_AGENT_TOOL_KIND":  "other",
		},
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, "tool_calling:external") {
		t.Fatalf("an MCP tool call was not logged as external:\n%s", out)
	}
	if !strings.Contains(out, "mcp__github__create_issue") {
		t.Fatalf("the log does not name the tool:\n%s", out)
	}
	if strings.Contains(out, "tool_calling:internal") {
		t.Fatalf("an MCP tool call was misclassified as internal:\n%s", out)
	}
}

func TestLogsInternalAgentToolCall(t *testing.T) {
	buf, log := logCapture()
	srv := newTestServerWithOptions(t, testOptions{
		logger: log,
		env: map[string]string{
			"FAKE_AGENT_TOOL_NAME":  "exec",
			"FAKE_AGENT_TOOL_TITLE": "Run the tests",
			"FAKE_AGENT_TOOL_KIND":  "execute",
		},
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, "tool_calling:internal") {
		t.Fatalf("an agent tool call was not logged as internal:\n%s", out)
	}
	if strings.Contains(out, "tool_calling:external") {
		t.Fatalf("an agent tool call was misclassified as external:\n%s", out)
	}
}

func TestLogsExternalMCPToolCallByTitleWhenNameIsAbsent(t *testing.T) {
	buf, log := logCapture()
	srv := newTestServerWithOptions(t, testOptions{
		logger: log,
		env: map[string]string{
			// No FAKE_AGENT_TOOL_NAME: only the title identifies the tool.
			"FAKE_AGENT_TOOL_TITLE": "Calling mcp__github__create_issue",
		},
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, "tool_calling:external") {
		t.Fatalf("an MCP tool call with no name was not classified by its title:\n%s", out)
	}
}

// TestToolCallLogNamesTheConversationAndSession: a tool-call record has to say
// which chat it belongs to — the caller's conversation key, and the ACP
// session id once the manager reports it — or interleaved sessions cannot be
// told apart.
func TestToolCallLogNamesTheConversationAndSession(t *testing.T) {
	buf, log := logCapture()
	srv := newTestServerWithOptions(t, testOptions{
		logger: log,
		env: map[string]string{
			"FAKE_AGENT_TOOL_NAME":  "exec",
			"FAKE_AGENT_TOOL_TITLE": "Run the tests",
		},
	})

	resp := post(t, srv, "", map[string]any{
		"model":           "fake",
		"conversation_id": "chat-9",
		"messages":        []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	var body struct {
		ACP struct {
			SessionID string `json:"session_id"`
		} `json:"acp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "conversation=chat-9") {
		t.Fatalf("the conversation key is missing from the tool-call log:\n%s", out)
	}
	if body.ACP.SessionID == "" {
		t.Fatal("the response carries no acp.session_id to correlate against")
	}
	if !strings.Contains(out, "session="+body.ACP.SessionID) {
		t.Fatalf("the session id %q is missing from the tool-call log:\n%s", body.ACP.SessionID, out)
	}
}

// TestTurnReplyIsLogged: the agent's reply is logged on the turn it answered —
// conversation key, session id and stop reason on the same record.
func TestTurnReplyIsLogged(t *testing.T) {
	buf, log := logCapture()
	srv := newTestServerWithOptions(t, testOptions{
		logger: log,
		env:    map[string]string{"FAKE_AGENT_REPLY": "the answer is 42"},
	})

	resp := post(t, srv, "", map[string]any{
		"model":           "fake",
		"conversation_id": "chat-9",
		"messages":        []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	var body struct {
		ACP struct {
			SessionID string `json:"session_id"`
		} `json:"acp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var line string
	for _, candidate := range strings.Split(buf.String(), "\n") {
		if strings.Contains(candidate, "msg=reply") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("the agent's reply was not logged:\n%s", buf.String())
	}
	for _, want := range []string{"conversation=chat-9", "session=" + body.ACP.SessionID,
		"stop=end_turn", "the answer is 42"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the reply line is missing %q:\n%s", want, line)
		}
	}
}

func TestLogsCallerToolCallFromREST(t *testing.T) {
	buf, log := logCapture()
	srv := newTestServerWithOptions(t, testOptions{
		logger: log,
		env:    envelopeEnv(nil),
	})

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "weather in Paris?"}},
		"tools":    weatherTool(),
	})
	defer resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, "tool_calling:from rest") {
		t.Fatalf("a caller tool call was not logged as from rest:\n%s", out)
	}
	if !strings.Contains(out, "get_weather") {
		t.Fatalf("the log does not name the caller tool:\n%s", out)
	}
}
