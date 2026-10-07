package handler_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

// completionOf decodes a chat completion into its text and ACP extension.
func completionOf(t *testing.T, resp *http.Response) (string, openai.ACPMeta) {
	t.Helper()
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out openai.ChatCompletionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(out.Choices) == 0 {
		t.Fatalf("no choices in %s", raw)
	}
	content := ""
	if out.Choices[0].Message.Content != nil {
		content = *out.Choices[0].Message.Content
	}
	meta := openai.ACPMeta{}
	if out.ACP != nil {
		meta = *out.ACP
	}
	return content, meta
}

// turn builds a chat request: the earlier exchanges, then one user message.
func turn(conversationID, user string, history ...map[string]string) map[string]any {
	messages := append(append([]map[string]string{}, history...),
		map[string]string{"role": "user", "content": user})
	body := map[string]any{"model": "fake", "messages": messages}
	if conversationID != "" {
		body["conversation_id"] = conversationID
	}
	return body
}

// history is the earlier part of a transcript, for a second turn.
func history(user, assistant string) []map[string]string {
	return []map[string]string{
		{"role": "user", "content": user},
		{"role": "assistant", "content": assistant},
	}
}

// echoOptions is a server whose agent echoes back the prompt it received, which
// is how a test sees what the gateway actually sent.
func echoOptions(header string) testOptions {
	return testOptions{
		env:                map[string]string{"FAKE_AGENT_ECHO": "1"},
		conversationHeader: header,
	}
}

// TestConversationAfterRestartReplaysTheTranscript covers the case that used to
// answer blind: a client keeps sending its conversation key, but the gateway has
// no session for it — after a restart, or after the idle reaper took it.
//
// The agent holds no history of its own in that situation, so the transcript is
// the only history it will ever see. Sending just the newest turn makes it
// answer a question nobody asked, with no sign that anything was wrong.
func TestConversationAfterRestartReplaysTheTranscript(t *testing.T) {
	opts := echoOptions("")
	opts.workspace = t.TempDir()

	first := newTestServerWithOptions(t, opts)
	if got, _ := completionOf(t, post(t, first, "", turn("c1", "AAA"))); got != "AAA" {
		t.Fatalf("first turn: %q, want %q", got, "AAA")
	}

	// A restart: the new manager knows nothing about conversation c1.
	second := newTestServerWithOptions(t, opts)
	got, _ := completionOf(t, post(t, second, "", turn("c1", "BBB", history("AAA", "AAA")...)))
	if !strings.Contains(got, "AAA") {
		t.Fatalf("the agent never saw the earlier turn: %q", got)
	}
	if !strings.Contains(got, "BBB") {
		t.Fatalf("the newest turn is missing: %q", got)
	}
}

// TestContinuingConversationSendsOnlyTheNewestTurn is the control: replaying the
// transcript into a session that already holds it would duplicate the history.
func TestContinuingConversationSendsOnlyTheNewestTurn(t *testing.T) {
	srv := newTestServerWithOptions(t, echoOptions(""))

	completionOf(t, post(t, srv, "", turn("c1", "AAA")))
	got, _ := completionOf(t, post(t, srv, "", turn("c1", "BBB", history("AAA", "AAA")...)))
	if got != "BBB" {
		t.Fatalf("a continuing conversation sent %q, want only the newest turn", got)
	}
}

// TestConversationHeaderKeepsTheSession covers the client that cannot put an
// extension field in the body but can send a header per chat.
func TestConversationHeaderKeepsTheSession(t *testing.T) {
	srv := newTestServerWithOptions(t, echoOptions("X-Chat-Id"))
	headers := map[string]string{"X-Chat-Id": "chat-1"}

	_, first := completionOf(t, postWith(t, srv, "", headers, turn("", "AAA")))
	got, second := completionOf(t, postWith(t, srv, "", headers, turn("", "BBB", history("AAA", "AAA")...)))

	if first.SessionID == "" {
		t.Fatal("the first turn opened no session")
	}
	if first.SessionID != second.SessionID {
		t.Fatalf("sessions %q and %q differ, want one session per chat", first.SessionID, second.SessionID)
	}
	// Same session means the agent already has the history.
	if got != "BBB" {
		t.Fatalf("the header-keyed turn sent %q, want only the newest turn", got)
	}
}

// TestExplicitConversationIDBeatsTheHeader: the most explicit key wins, so a
// client that sets both is not silently keyed by the header.
func TestExplicitConversationIDBeatsTheHeader(t *testing.T) {
	srv := newTestServerWithOptions(t, echoOptions("X-Chat-Id"))
	headers := map[string]string{"X-Chat-Id": "from-header"}

	_, fromBody := completionOf(t, postWith(t, srv, "", headers, turn("body-one", "AAA")))
	_, otherBody := completionOf(t, postWith(t, srv, "", headers, turn("body-two", "BBB")))
	if fromBody.SessionID == otherBody.SessionID {
		t.Fatal("the body's conversation_id should key the session, not the header")
	}

	_, again := completionOf(t, postWith(t, srv, "", nil, turn("body-one", "CCC")))
	if again.SessionID != fromBody.SessionID {
		t.Fatal("the body's conversation_id should key the session on its own")
	}
}

// TestHeaderBeatsTheUserFallback: `user` is the last resort, for a client that
// cannot set a custom field at all.
func TestHeaderBeatsTheUserFallback(t *testing.T) {
	srv := newTestServerWithOptions(t, echoOptions("X-Chat-Id"))

	withUser := func(content string) map[string]any {
		body := turn("", content)
		body["user"] = "u1"
		return body
	}

	_, first := completionOf(t, post(t, srv, "", withUser("AAA")))
	_, second := completionOf(t, post(t, srv, "", withUser("BBB")))
	if first.SessionID != second.SessionID {
		t.Fatal("user should key the session when nothing else does")
	}

	_, withHeader := completionOf(t, postWith(t, srv, "", map[string]string{"X-Chat-Id": "chat-1"}, withUser("CCC")))
	if withHeader.SessionID == first.SessionID {
		t.Fatal("the header should win over the user field")
	}
}

// TestNoConversationKeyIsEphemeral: with nothing to key on, each request gets
// its own session, which is the OpenAI shape.
func TestNoConversationKeyIsEphemeral(t *testing.T) {
	srv := newTestServerWithOptions(t, echoOptions("X-Chat-Id"))

	_, first := completionOf(t, post(t, srv, "", turn("", "AAA")))
	_, second := completionOf(t, post(t, srv, "", turn("", "BBB")))
	if first.SessionID == second.SessionID {
		t.Fatal("without a key each request should get its own session")
	}
}

// TestUnconfiguredHeaderIsIgnored: only the named header keys a session. A
// deployment that names none reads none.
func TestUnconfiguredHeaderIsIgnored(t *testing.T) {
	srv := newTestServerWithOptions(t, echoOptions("X-Chat-Id"))
	headers := map[string]string{"X-Other-Id": "chat-1"}

	_, first := completionOf(t, postWith(t, srv, "", headers, turn("", "AAA")))
	_, second := completionOf(t, postWith(t, srv, "", headers, turn("", "BBB")))
	if first.SessionID == second.SessionID {
		t.Fatal("a header the deployment did not name must not key a session")
	}

	disabled := newTestServerWithOptions(t, echoOptions(""))
	_, one := completionOf(t, postWith(t, disabled, "", map[string]string{"X-Chat-Id": "chat-1"}, turn("", "AAA")))
	_, two := completionOf(t, postWith(t, disabled, "", map[string]string{"X-Chat-Id": "chat-1"}, turn("", "BBB")))
	if one.SessionID == two.SessionID {
		t.Fatal("naming no header must disable the mechanism entirely")
	}
}
