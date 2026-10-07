package handler_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quonaro/acp2api/internal/agent"
	"github.com/quonaro/acp2api/internal/client"
	"github.com/quonaro/acp2api/internal/fakeagent"
	"github.com/quonaro/acp2api/internal/handler"
	"github.com/quonaro/acp2api/internal/openai"
	"github.com/quonaro/acp2api/internal/session"
)

func TestMain(m *testing.M) {
	if fakeagent.MaybeRun() {
		return
	}
	os.Exit(m.Run())
}

// newTestServer starts the gateway in front of the fake agent.
// testOptions describes how the fake agent is configured for one test server.
type testOptions struct {
	env map[string]string
	// token is the bearer token the test server requires, empty for none.
	token string
	// filesystem is the agent's filesystem mode: full, readonly or none.
	filesystem string
	mode       string
	// workspace, when set, is used instead of a fresh temporary directory, so a
	// test can inspect what the agent did or did not write.
	workspace string
	// logger, when set, captures the server's diagnostics for assertion.
	logger *slog.Logger
}

func newTestServer(t *testing.T, env map[string]string, token string) *httptest.Server {
	t.Helper()
	return newTestServerWithOptions(t, testOptions{env: env, token: token})
}

func newTestServerWithOptions(t *testing.T, opts testOptions) *httptest.Server {
	t.Helper()

	registry := agent.NewRegistry(agent.Agent{
		ID:         "fake",
		Name:       "Fake",
		Command:    os.Args[0],
		Filesystem: opts.filesystem,
		Mode:       opts.mode,
	})
	merged := map[string]string{"ACP2API_FAKE_AGENT": "1"}
	for k, v := range opts.env {
		merged[k] = v
	}

	workspace := opts.workspace
	if workspace == "" {
		workspace = t.TempDir()
	}

	manager, err := session.New(registry, session.Options{
		Workspace:      workspace,
		Policy:         client.AllowAll(),
		RequestTimeout: 15 * time.Second,
		Env:            merged,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	srv := httptest.NewServer(handler.New(manager, handler.Options{Token: opts.token, Logger: opts.logger}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// post sends a chat completion request and returns the response.
func post(t *testing.T, srv *httptest.Server, token string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

func TestModelsListsConfiguredAgents(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp, err := srv.Client().Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var list openai.ModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Object != openai.ObjectList || len(list.Data) != 1 || list.Data[0].ID != "fake" {
		t.Fatalf("model list = %+v", list)
	}
}

func TestChatCompletionNonStreaming(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_CHUNKS": "2"}, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(completion.Choices))
	}
	choice := completion.Choices[0]
	if got := choice.Message.ContentString(); got != "chunk1 chunk2 " {
		t.Fatalf("content = %q", got)
	}
	if choice.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", choice.FinishReason)
	}
	if completion.ACP == nil {
		t.Fatal("expected the acp extension on the response")
	}
	if completion.ACP.Agent != "fake" || completion.ACP.SessionID == "" {
		t.Fatalf("acp meta = %+v", completion.ACP)
	}
	if completion.ACP.StopReason != "end_turn" {
		t.Fatalf("acp stop reason = %q", completion.ACP.StopReason)
	}
	if completion.Usage == nil || completion.Usage.TotalTokens == 0 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func TestChatCompletionStreaming(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_CHUNKS": "3"}, "")

	resp := post(t, srv, "", map[string]any{
		"model":          "fake",
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": "hello"}},
	})
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	events := readSSE(t, resp.Body)
	if len(events) == 0 {
		t.Fatal("no server-sent events")
	}
	if events[len(events)-1] != "[DONE]" {
		t.Fatalf("last event = %q, want [DONE]", events[len(events)-1])
	}

	var text strings.Builder
	var sawFinish bool
	var sawUsage bool
	var sawACP bool

	for _, raw := range events {
		if raw == "[DONE]" {
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", raw, err)
		}
		if chunk.Object != openai.ObjectChatCompletionChunk {
			t.Fatalf("object = %q", chunk.Object)
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
			if c.FinishReason != nil {
				sawFinish = true
				if *c.FinishReason != "stop" {
					t.Fatalf("finish reason = %q", *c.FinishReason)
				}
			}
		}
		if chunk.Usage != nil {
			sawUsage = true
		}
		if chunk.ACP != nil {
			sawACP = true
		}
	}

	if text.String() != "chunk1 chunk2 chunk3 " {
		t.Fatalf("streamed text = %q", text.String())
	}
	if !sawFinish {
		t.Fatal("no chunk carried a finish reason")
	}
	if !sawUsage {
		t.Fatal("include_usage was requested but no usage chunk arrived")
	}
	if !sawACP {
		t.Fatal("no chunk carried the acp extension")
	}
}

func TestChatCompletionExposesReasoning(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_THOUGHT": "let me think",
		"FAKE_AGENT_CHUNKS":  "2",
	}, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	defer resp.Body.Close()

	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	message := completion.Choices[0].Message
	if message.ReasoningContent != "let me think" {
		t.Fatalf("reasoning_content = %q, want %q", message.ReasoningContent, "let me think")
	}
	if got := message.ContentString(); got != "chunk1 chunk2 " {
		t.Fatalf("content = %q", got)
	}
}

func TestChatCompletionStreamsReasoning(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_THOUGHT": "thinking hard",
		"FAKE_AGENT_CHUNKS":  "2",
	}, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	defer resp.Body.Close()

	events := readSSE(t, resp.Body)

	var reasoning, text strings.Builder
	for _, raw := range events {
		if raw == "[DONE]" {
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", raw, err)
		}
		for _, c := range chunk.Choices {
			reasoning.WriteString(c.Delta.ReasoningContent)
			text.WriteString(c.Delta.Content)
		}
	}

	if reasoning.String() != "thinking hard" {
		t.Fatalf("streamed reasoning = %q, want %q", reasoning.String(), "thinking hard")
	}
	if text.String() != "chunk1 chunk2 " {
		t.Fatalf("streamed text = %q", text.String())
	}
}

func TestUnknownModelIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "does-not-exist",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Type != openai.ErrTypeInvalidRequest || failure.Error.Code != "unknown_model" {
		t.Fatalf("error = %+v", failure.Error)
	}
}

func TestAuthIsRequiredWhenATokenIsConfigured(t *testing.T) {
	srv := newTestServer(t, nil, "secret-token")
	body := map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}

	t.Run("missing token", func(t *testing.T) {
		resp := post(t, srv, "", body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		resp := post(t, srv, "nope", body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("correct token", func(t *testing.T) {
		resp := post(t, srv, "secret-token", body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

func TestHealthNeedsNoToken(t *testing.T) {
	srv := newTestServer(t, nil, "secret-token")

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestUnsupportedParameterIsRejectedBeforeAnyWork(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"logprobs": true,
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
	if failure.Error.Param != "logprobs" {
		t.Fatalf("error param = %q, want logprobs", failure.Error.Param)
	}
}

func TestIgnoredParametersAreReportedInHeaderAndBody(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":       "fake",
		"messages":    []map[string]string{{"role": "user", "content": "hi"}},
		"temperature": 0.2,
		"top_p":       0.9,
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	header := resp.Header.Get("X-Acp2api-Ignored-Params")
	if header != "temperature,top_p" {
		t.Fatalf("header = %q, want temperature,top_p", header)
	}

	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if completion.ACP == nil {
		t.Fatal("expected the acp extension")
	}
	got := strings.Join(completion.ACP.IgnoredParams, ",")
	if got != header {
		t.Fatalf("body reports %q but the header says %q; they must agree", got, header)
	}
}

func TestNoIgnoredHeaderWhenNothingWasIgnored(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Acp2api-Ignored-Params"); got != "" {
		t.Fatalf("header = %q, want it absent", got)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if completion.ACP != nil && len(completion.ACP.IgnoredParams) != 0 {
		t.Fatalf("ignored params = %v, want none", completion.ACP.IgnoredParams)
	}
}

// readSSE collects the payloads of a server-sent event stream.
func readSSE(t *testing.T, body io.Reader) []string {
	t.Helper()
	var events []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		events = append(events, payload)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return events
}
