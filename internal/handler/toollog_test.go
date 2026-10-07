package handler_test

import (
	"bytes"
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
