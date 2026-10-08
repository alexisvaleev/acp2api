package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

// effortEnv advertises a family with two levels and makes the agent report the
// model it settled on, so the tests observe the selection across the process
// boundary rather than through an internal.
func effortEnv() map[string]string {
	return map[string]string{
		"FAKE_AGENT_MODELS":     "base-low,base-high",
		"FAKE_AGENT_ECHO_STATE": "1",
	}
}

// chatText runs a chat completion and returns the assistant message.
func chatText(t *testing.T, srv *httptest.Server, body map[string]any) (string, *http.Response) {
	t.Helper()
	resp := post(t, srv, "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure openai.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		return "", resp
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) == 0 {
		t.Fatal("no choices in the response")
	}
	if content := completion.Choices[0].Message.Content; content != nil {
		return *content, resp
	}
	return "", resp
}

func TestChatReasoningEffortSelectsVariant(t *testing.T) {
	srv := newTestServer(t, effortEnv(), "")

	text, _ := chatText(t, srv, map[string]any{
		"model":            "fake/base",
		"messages":         []map[string]string{{"role": "user", "content": "hi"}},
		"reasoning_effort": "low",
	})
	if !strings.Contains(text, "model=base-low") {
		t.Fatalf("reply = %q, want the base-low variant selected", text)
	}
}

// TestChatReasoningObjectIsHonoured is the regression guard for the chat
// surface: the reasoning object used to be policed as supported while the chat
// struct had no field for it, so the effort was dropped without an error, an
// ignored_params entry, or a header.
func TestChatReasoningObjectIsHonoured(t *testing.T) {
	srv := newTestServer(t, effortEnv(), "")

	text, resp := chatText(t, srv, map[string]any{
		"model":     "fake/base",
		"messages":  []map[string]string{{"role": "user", "content": "hi"}},
		"reasoning": map[string]any{"effort": "low"},
	})
	if !strings.Contains(text, "model=base-low") {
		t.Fatalf("reply = %q, want reasoning.effort to select base-low", text)
	}
	if got := resp.Header.Get("X-Acp2api-Ignored-Params"); got != "" {
		t.Fatalf("header = %q, the effort was honoured, not ignored", got)
	}
}

// TestChatReasoningUnsupportedKeysAreRefused covers the same guard from the
// other side: a reasoning key with no ACP equivalent must fail the request
// rather than vanish.
func TestChatReasoningUnsupportedKeysAreRefused(t *testing.T) {
	srv := newTestServer(t, effortEnv(), "")

	resp := post(t, srv, "", map[string]any{
		"model":     "fake/base",
		"messages":  []map[string]string{{"role": "user", "content": "hi"}},
		"reasoning": map[string]any{"effort": "low", "summary": "auto"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != openai.CodeUnsupportedParameter {
		t.Fatalf("error code = %q, want %q", failure.Error.Code, openai.CodeUnsupportedParameter)
	}
	if failure.Error.Param != "reasoning" {
		t.Fatalf("error param = %q, want reasoning", failure.Error.Param)
	}
}

// TestUnknownEffortIsRefusedBeforeTheAgentStarts pins the cheap check: the
// catalog vocabulary is fixed, so a level outside it fails with 400 instead of
// being discovered after a cold start.
func TestUnknownEffortIsRefusedBeforeTheAgentStarts(t *testing.T) {
	srv := newTestServer(t, effortEnv(), "")

	resp := post(t, srv, "", map[string]any{
		"model":            "fake/base",
		"messages":         []map[string]string{{"role": "user", "content": "hi"}},
		"reasoning_effort": "bogus",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "invalid_reasoning_effort" {
		t.Fatalf("error code = %q, want invalid_reasoning_effort", failure.Error.Code)
	}
	if failure.Error.Param != "reasoning_effort" {
		t.Fatalf("error param = %q, want reasoning_effort", failure.Error.Param)
	}
	// The message has to be actionable: it names the levels that exist.
	if !strings.Contains(failure.Error.Message, "high") {
		t.Fatalf("error message = %q, want the accepted levels listed", failure.Error.Message)
	}
}

func TestResponsesEffortSelectsVariant(t *testing.T) {
	srv := newTestServer(t, effortEnv(), "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":     "fake/base",
		"input":     "hi",
		"reasoning": map[string]any{"effort": "low"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeResponse(t, resp)
	if !strings.Contains(outText(t, out), "model=base-low") {
		t.Fatalf("response = %+v, want the base-low variant selected", out)
	}
}

func TestResponsesUnsupportedReasoningKeyIsRefused(t *testing.T) {
	srv := newTestServer(t, effortEnv(), "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":     "fake/base",
		"input":     "hi",
		"reasoning": map[string]any{"summary": "auto"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != openai.CodeUnsupportedParameter {
		t.Fatalf("error code = %q, want %q", failure.Error.Code, openai.CodeUnsupportedParameter)
	}
}

// outText flattens the text an output item carries.
func outText(t *testing.T, out openai.Response) string {
	t.Helper()
	var text strings.Builder
	for _, item := range out.Output {
		for _, part := range item.Content {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}
