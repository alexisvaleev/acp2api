package session_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/session"
)

// captureLog redirects the process logger into a buffer for the test's
// duration, so log lines can be asserted on.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestOnSessionReceivesTheSessionID: the hook exists so a caller can tag its
// own log lines with the session its turn is running on, which means it has to
// fire before the first update arrives.
func TestOnSessionReceivesTheSessionID(t *testing.T) {
	m, _ := newManager(t, fakeRegistry(), map[string]string{"FAKE_AGENT_CHUNKS": "1"})

	var hooked string
	var updatesBeforeHook int
	res, err := m.Prompt(context.Background(), session.Request{
		Model:          "fake",
		ConversationID: "chat-1",
		Prompt:         "hi",
		OnSession:      func(id string) { hooked = id },
	}, func(acp.SessionUpdate) error {
		if hooked == "" {
			updatesBeforeHook++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if hooked == "" {
		t.Fatal("OnSession was never called")
	}
	if hooked != res.SessionID {
		t.Fatalf("OnSession got %q, the result reports %q", hooked, res.SessionID)
	}
	if updatesBeforeHook > 0 {
		t.Fatalf("OnSession fired after %d updates; it must fire before the first", updatesBeforeHook)
	}
}

// TestSessionOpenedIsLogged: the line tying a caller's conversation key to the
// agent's session id is what lets a log be attributed to one chat.
func TestSessionOpenedIsLogged(t *testing.T) {
	buf := captureLog(t)
	m, _ := newManager(t, fakeRegistry(), nil)

	res, err := m.Prompt(context.Background(), session.Request{
		Model:          "fake",
		ConversationID: "chat-7",
		Prompt:         "hi",
	}, noop)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "session opened") {
		t.Fatalf("opening a session was not logged:\n%s", out)
	}
	if !strings.Contains(out, "conversation=chat-7") {
		t.Fatalf("the conversation key is missing from the log:\n%s", out)
	}
	if !strings.Contains(out, "session="+res.SessionID) {
		t.Fatalf("the session id %q is missing from the log:\n%s", res.SessionID, out)
	}
}

// lineWith returns the first log line carrying the marker, for assertions that
// must hold on one record rather than anywhere in the output.
func lineWith(out, marker string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return ""
}

// TestTurnPromptIsLogged: what the agent is asked is logged against the session
// it was sent on, and a replayed transcript is marked as such — otherwise a
// fresh session looks identical to an ordinary turn.
func TestTurnPromptIsLogged(t *testing.T) {
	buf := captureLog(t)
	m, _ := newManager(t, fakeRegistry(), nil)

	res, err := m.Prompt(context.Background(), session.Request{
		Model:          "fake",
		ConversationID: "chat-8",
		Prompt:         "newest question",
		Replay:         "the whole transcript",
	}, noop)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	line := lineWith(buf.String(), "msg=prompt")
	if line == "" {
		t.Fatalf("the prompt was not logged:\n%s", buf.String())
	}
	for _, want := range []string{"session=" + res.SessionID, "conversation=chat-8",
		"replayed=true", "the whole transcript"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the prompt line is missing %q:\n%s", want, line)
		}
	}

	// A session that already holds the history must not be marked replayed,
	// and only the newest turn is sent.
	buf.Reset()
	if _, err := m.Prompt(context.Background(), session.Request{
		Model:          "fake",
		ConversationID: "chat-8",
		Prompt:         "newest question",
		Replay:         "the whole transcript",
	}, noop); err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	line = lineWith(buf.String(), "msg=prompt")
	if !strings.Contains(line, "newest question") {
		t.Fatalf("the newest turn is missing from the prompt line:\n%s", line)
	}
	if strings.Contains(line, "replayed") {
		t.Fatalf("an existing session must not be marked replayed:\n%s", line)
	}
}
