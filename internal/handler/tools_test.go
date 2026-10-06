package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

// weatherTool is the caller-declared function the fake agent knows how to ask for.
func weatherTool() []map[string]any {
	return []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Look up the weather",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
		},
	}}
}

func envelopeEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"FAKE_AGENT_ENVELOPE":       "get_weather",
		"FAKE_AGENT_ENVELOPE_PARTS": "4",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestToolCallNonStreaming(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(nil), "")

	resp := post(t, srv, "", map[string]any{
		"model":       "fake",
		"messages":    []map[string]string{{"role": "user", "content": "weather in Paris?"}},
		"tools":       weatherTool(),
		"tool_choice": "auto",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}

	choice := completion.Choices[0]
	if choice.FinishReason != openai.FinishToolCalls {
		t.Fatalf("finish reason = %q, want %q", choice.FinishReason, openai.FinishToolCalls)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", choice.Message.ToolCalls)
	}
	call := choice.Message.ToolCalls[0]
	if call.Function.Name != "get_weather" || call.ID == "" || call.Type != "function" {
		t.Fatalf("call = %+v", call)
	}
	if !strings.Contains(call.Function.Arguments, "Paris") {
		t.Fatalf("arguments = %q", call.Function.Arguments)
	}
	// A tool-call turn carries no prose, and the envelope must not leak into it.
	if choice.Message.Content != nil {
		t.Fatalf("content = %q, want null", choice.Message.ContentString())
	}
	if completion.ACP == nil || completion.ACP.Agent != "fake" {
		t.Fatalf("acp meta = %+v", completion.ACP)
	}
}

func TestToolCallStreamingDoesNotLeakTheEnvelope(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(nil), "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "weather?"}},
		"tools":    weatherTool(),
	})
	defer resp.Body.Close()

	var text strings.Builder
	var calls []openai.ToolCallDelta
	var finish string

	for _, raw := range readSSE(t, resp.Body) {
		if raw == "[DONE]" {
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("decode chunk %q: %v", raw, err)
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
			calls = append(calls, c.Delta.ToolCalls...)
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
		}
	}

	if text.String() != "" {
		t.Fatalf("the envelope leaked into content: %q", text.String())
	}
	if len(calls) != 1 {
		t.Fatalf("tool call deltas = %+v", calls)
	}
	if calls[0].Index != 0 || calls[0].Function.Name != "get_weather" || calls[0].ID == "" {
		t.Fatalf("delta = %+v", calls[0])
	}
	if finish != openai.FinishToolCalls {
		t.Fatalf("finish reason = %q, want %q", finish, openai.FinishToolCalls)
	}
}

func TestToolCallStreamingStreamsTheProsePrefix(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(map[string]string{
		"FAKE_AGENT_ENVELOPE_PREFIX": "Let me look that up. ",
	}), "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "weather?"}},
		"tools":    weatherTool(),
	})
	defer resp.Body.Close()

	var text strings.Builder
	var calls int
	for _, raw := range readSSE(t, resp.Body) {
		if raw == "[DONE]" {
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, c := range chunk.Choices {
			text.WriteString(c.Delta.Content)
			calls += len(c.Delta.ToolCalls)
		}
	}

	if text.String() != "Let me look that up. " {
		t.Fatalf("streamed content = %q", text.String())
	}
	if calls != 1 {
		t.Fatalf("tool calls = %d, want 1", calls)
	}
}

func TestToolChoiceNoneDisablesTheEnvelopeContract(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(nil), "")

	resp := post(t, srv, "", map[string]any{
		"model":       "fake",
		"messages":    []map[string]string{{"role": "user", "content": "weather?"}},
		"tools":       weatherTool(),
		"tool_choice": "none",
	})
	defer resp.Body.Close()

	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	choice := completion.Choices[0]
	// With the contract off, the envelope is just text: nothing is held back,
	// nothing is parsed, and no call is reported.
	if len(choice.Message.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want none", choice.Message.ToolCalls)
	}
	if !strings.Contains(choice.Message.ContentString(), "tool_calls") {
		t.Fatalf("content = %q, want the raw text", choice.Message.ContentString())
	}
	if choice.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", choice.FinishReason)
	}
}

// TestToolResultRoundTrip drives the whole loop: the agent asks for a tool, the
// caller runs it, and the result resumes the same ACP session.
func TestToolResultRoundTrip(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(map[string]string{
		"FAKE_AGENT_ENVELOPE_ONCE": "1",
		// On the second turn the agent echoes what it received, which is how the
		// test proves the tool result actually reached it.
		"FAKE_AGENT_ECHO": "1",
	}), "")

	first := post(t, srv, "", map[string]any{
		"model":           "fake",
		"conversation_id": "weather-session",
		"messages":        []map[string]string{{"role": "user", "content": "weather in Paris?"}},
		"tools":           weatherTool(),
	})
	defer first.Body.Close()

	var call openai.ChatCompletionResponse
	if err := json.NewDecoder(first.Body).Decode(&call); err != nil {
		t.Fatal(err)
	}
	if len(call.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("first turn returned no tool call: %+v", call.Choices[0].Message)
	}
	toolCall := call.Choices[0].Message.ToolCalls[0]

	// The caller executes the tool and sends the result back.
	second := post(t, srv, "", map[string]any{
		"model":           "fake",
		"conversation_id": "weather-session",
		"tools":           weatherTool(),
		"messages": []map[string]any{
			{"role": "user", "content": "weather in Paris?"},
			{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": toolCall.ID, "type": "function",
				"function": map[string]any{"name": toolCall.Function.Name, "arguments": toolCall.Function.Arguments},
			}}},
			{"role": "tool", "tool_call_id": toolCall.ID, "content": "18C and sunny"},
		},
	})
	defer second.Body.Close()

	if second.StatusCode != http.StatusOK {
		t.Fatalf("second turn status = %d", second.StatusCode)
	}
	var answer openai.ChatCompletionResponse
	if err := json.NewDecoder(second.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}

	choice := answer.Choices[0]
	if len(choice.Message.ToolCalls) != 0 {
		t.Fatalf("second turn must answer, not call again: %+v", choice.Message.ToolCalls)
	}
	if choice.Message.ContentString() == "" {
		t.Fatal("second turn returned no content")
	}
	// The agent echoed its prompt, so the result is provably what it received.
	if !strings.Contains(choice.Message.ContentString(), "18C and sunny") {
		t.Fatalf("the tool result never reached the agent: %q", choice.Message.ContentString())
	}
	if choice.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", choice.FinishReason)
	}
	// Same conversation, same ACP session: the tool loop stayed in one session.
	if answer.ACP == nil || call.ACP == nil {
		t.Fatal("expected acp meta on both turns")
	}
	if answer.ACP.SessionID != call.ACP.SessionID {
		t.Fatalf("session changed across the tool loop: %q then %q",
			call.ACP.SessionID, answer.ACP.SessionID)
	}
}

func TestRequestWithoutToolsIsUnaffected(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(nil), "")

	resp := post(t, srv, "", map[string]any{
		"model":    "fake",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()

	var completion openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	choice := completion.Choices[0]
	if len(choice.Message.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want none", choice.Message.ToolCalls)
	}
	if !strings.Contains(choice.Message.ContentString(), "tool_calls") {
		t.Fatalf("without tools the envelope is plain text, got %q", choice.Message.ContentString())
	}
}

func TestInvalidToolChoiceIsRejected(t *testing.T) {
	srv := newTestServer(t, nil, "")

	resp := post(t, srv, "", map[string]any{
		"model":       "fake",
		"messages":    []map[string]string{{"role": "user", "content": "hi"}},
		"tools":       weatherTool(),
		"tool_choice": "sometimes",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var failure openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Param != "tool_choice" {
		t.Fatalf("error = %+v", failure.Error)
	}
}
