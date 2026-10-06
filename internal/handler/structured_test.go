package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

func jsonFormatBody(extra map[string]any) map[string]any {
	body := map[string]any{
		"model":           "fake",
		"messages":        []map[string]string{{"role": "user", "content": "give me JSON"}},
		"response_format": map[string]any{"type": "json_object"},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestStructuredOutputReturnsCanonicalJSON(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_REPLY": "```json\n{\"answer\":42}\n```",
	}, "")

	resp := post(t, srv, "", jsonFormatBody(nil))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}

	content := completion.Choices[0].Message.ContentString()
	if content != `{"answer":42}` {
		t.Fatalf("content = %q, want the canonical JSON", content)
	}
	if completion.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish reason = %q", completion.Choices[0].FinishReason)
	}
}

func TestStructuredOutputRetriesOnce(t *testing.T) {
	// The first reply is prose, the retry is valid JSON.
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_REPLY":       "I am afraid I cannot do that.",
		"FAKE_AGENT_REPLY_AFTER": `{"answer":42}`,
	}, "")

	resp := post(t, srv, "", jsonFormatBody(nil))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	if got := completion.Choices[0].Message.ContentString(); got != `{"answer":42}` {
		t.Fatalf("content = %q, want the retry's JSON", got)
	}
}

func TestStructuredOutputFailsAfterOneRetry(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_REPLY": "still not JSON, sorry",
	}, "")

	resp := post(t, srv, "", jsonFormatBody(nil))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "invalid_response_format" || failure.Error.Param != "response_format" {
		t.Fatalf("error = %+v", failure.Error)
	}
	if !strings.Contains(failure.Error.Message, "one retry") {
		t.Fatalf("error message = %q", failure.Error.Message)
	}
}

func TestStructuredOutputEnforcesTheSchema(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		// Missing the required "city" property.
		"FAKE_AGENT_REPLY": `{"temp":18}`,
	}, "")

	body := jsonFormatBody(map[string]any{
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "weather",
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"city"},
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
				},
			},
		},
	})

	resp := post(t, srv, "", body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStructuredOutputStreamingIsBufferedAndValidated(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"FAKE_AGENT_REPLY": "not json at all",
	}, "")

	resp := post(t, srv, "", jsonFormatBody(map[string]any{"stream": true}))
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	var sawError bool
	var sawContent bool
	for _, raw := range readSSE(t, resp.Body) {
		if raw == "[DONE]" {
			continue
		}
		if strings.Contains(raw, "invalid_response_format") {
			sawError = true
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				sawContent = true
			}
		}
	}

	if sawContent {
		t.Fatal("invalid JSON must not reach the caller as content")
	}
	if !sawError {
		t.Fatal("expected an in-band error event")
	}
}

func TestInvalidResponseFormatIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", jsonFormatBody(map[string]any{
		"response_format": map[string]any{"type": "yaml"},
	}))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Param != "response_format" {
		t.Fatalf("error = %+v", failure.Error)
	}
}
