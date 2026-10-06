package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

func decodeCompletion(t *testing.T, resp *http.Response) openai.CompletionResponse {
	t.Helper()
	defer resp.Body.Close()
	var out openai.CompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompletionsStringPrompt(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": "once upon a time",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	out := decodeCompletion(t, resp)
	if out.Object != openai.ObjectTextCompletion || len(out.Choices) != 1 {
		t.Fatalf("response = %+v", out)
	}
	if out.Choices[0].Text != "chunk1 chunk2 " {
		t.Fatalf("text = %q", out.Choices[0].Text)
	}
	if out.Choices[0].FinishReason != "stop" || out.Choices[0].Index != 0 {
		t.Fatalf("choice = %+v", out.Choices[0])
	}
	if out.ACP == nil || out.ACP.Agent != "fake" {
		t.Fatalf("acp meta = %+v", out.ACP)
	}
}

func TestCompletionsArrayPromptGivesOneChoicePerPrompt(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": []string{"first", "second", "third"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	out := decodeCompletion(t, resp)
	if len(out.Choices) != 3 {
		t.Fatalf("got %d choices, want 3", len(out.Choices))
	}
	for i, choice := range out.Choices {
		if choice.Index != i {
			t.Fatalf("choice %d has index %d", i, choice.Index)
		}
		if choice.Text == "" {
			t.Fatalf("choice %d is empty", i)
		}
	}
}

func TestCompletionsEchoPrependsThePrompt(t *testing.T) {
	srv := newTestServer(t, nil, "")

	out := decodeCompletion(t, postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": "PREFIX",
		"echo":   true,
	}))

	if !strings.HasPrefix(out.Choices[0].Text, "PREFIX") {
		t.Fatalf("echo was not honoured: %q", out.Choices[0].Text)
	}
}

func TestCompletionsStopTruncates(t *testing.T) {
	srv := newTestServer(t, nil, "")

	out := decodeCompletion(t, postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": "go",
		"stop":   "chunk2",
	}))

	if out.Choices[0].Text != "chunk1 " {
		t.Fatalf("text = %q, want %q", out.Choices[0].Text, "chunk1 ")
	}
}

func TestCompletionsMaxTokensReportsLength(t *testing.T) {
	srv := newTestServer(t, nil, "")

	out := decodeCompletion(t, postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":      "fake",
		"prompt":     "go",
		"max_tokens": 2,
	}))

	if len(out.Choices[0].Text) > 8 {
		t.Fatalf("text = %q is longer than the cap allows", out.Choices[0].Text)
	}
	if out.Choices[0].FinishReason != openai.FinishLength {
		t.Fatalf("finish reason = %q, want %q", out.Choices[0].FinishReason, openai.FinishLength)
	}
}

func TestCompletionsSuffixIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": "go",
		"suffix": " and then",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Param != "suffix" || failure.Error.Code != openai.CodeUnsupportedParameter {
		t.Fatalf("error = %+v", failure.Error)
	}
}

func TestCompletionsStreamingArrayPromptIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": []string{"a", "b"},
		"stream": true,
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCompletionsStreaming(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_CHUNKS": "2"}, "")

	resp := postPath(t, srv, "", "/v1/completions", map[string]any{
		"model":  "fake",
		"prompt": "go",
		"stream": true,
	})
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	var text strings.Builder
	var finish string
	for _, raw := range readSSE(t, resp.Body) {
		if raw == "[DONE]" {
			continue
		}
		var chunk openai.CompletionChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", raw, err)
		}
		if chunk.Object != openai.ObjectTextCompletion {
			t.Fatalf("object = %q", chunk.Object)
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Text)
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
	}

	if text.String() != "chunk1 chunk2 " {
		t.Fatalf("streamed text = %q", text.String())
	}
	if finish != "stop" {
		t.Fatalf("finish reason = %q", finish)
	}
}

func TestCompletionsMissingPromptIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := postPath(t, srv, "", "/v1/completions", map[string]any{"model": "fake"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
