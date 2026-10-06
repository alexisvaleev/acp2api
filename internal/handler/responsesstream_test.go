package handler_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/openai"
)

func TestResponsesStreamingEventOrder(t *testing.T) {
	srv := newTestServer(t, map[string]string{"FAKE_AGENT_CHUNKS": "2"}, "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":  "fake",
		"input":  "hello",
		"stream": true,
	})
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	events, types := readNamedEvents(t, resp.Body)

	// The number of deltas depends on how the agent chunked its output, so the
	// assertion is on the shape: runs of deltas collapse to one entry.
	want := []string{
		openai.EventCreated,
		openai.EventInProgress,
		openai.EventOutputItemAdded,
		openai.EventContentPartAdded,
		openai.EventOutputTextDelta,
		openai.EventOutputTextDone,
		openai.EventContentPartDone,
		openai.EventOutputItemDone,
		openai.EventCompleted,
	}
	got := collapseRuns(types)
	if len(got) != len(want) {
		t.Fatalf("event shape = %v, want %v", got, want)
	}
	for i, expected := range want {
		if got[i] != expected {
			t.Fatalf("event %d = %q, want %q (shape %v)", i, got[i], expected, got)
		}
	}

	// Every event carries a monotonic sequence number starting at one.
	for i, event := range events {
		if event.SequenceNumber != i+1 {
			t.Fatalf("event %d has sequence %d", i, event.SequenceNumber)
		}
	}

	var text strings.Builder
	for _, event := range events {
		text.WriteString(event.Delta)
	}
	if text.String() != "chunk1 chunk2 " {
		t.Fatalf("streamed text = %q", text.String())
	}

	last := events[len(events)-1]
	if last.Response == nil || last.Response.Status != openai.StatusCompleted {
		t.Fatalf("completed event = %+v", last.Response)
	}
	if last.Response.OutputText != "chunk1 chunk2 " {
		t.Fatalf("completed output_text = %q", last.Response.OutputText)
	}
}

func TestResponsesStreamingFunctionCall(t *testing.T) {
	srv := newTestServer(t, envelopeEnv(nil), "")

	resp := postPath(t, srv, "", "/v1/responses", map[string]any{
		"model":  "fake",
		"input":  "weather?",
		"stream": true,
		"tools":  weatherTool(),
	})
	defer resp.Body.Close()

	events, types := readNamedEvents(t, resp.Body)

	var sawTextDelta bool
	var sawArgsDone bool
	for _, eventType := range types {
		switch eventType {
		case openai.EventOutputTextDelta:
			sawTextDelta = true
		case openai.EventFunctionArgsDone:
			sawArgsDone = true
		}
	}
	if sawTextDelta {
		t.Fatal("the envelope leaked into output_text deltas")
	}
	if !sawArgsDone {
		t.Fatalf("no function-call arguments event: %v", types)
	}

	last := events[len(events)-1]
	if last.Type != openai.EventCompleted {
		t.Fatalf("last event = %q", last.Type)
	}
	if len(last.Response.Output) != 1 || last.Response.Output[0].Type != openai.ItemFunctionCall {
		t.Fatalf("completed output = %+v", last.Response.Output)
	}
}

// collapseRuns merges consecutive duplicate values, so an assertion can be
// about the shape of an event stream rather than how many deltas it carried.
func collapseRuns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(out) > 0 && out[len(out)-1] == value {
			continue
		}
		out = append(out, value)
	}
	return out
}

// readNamedEvents parses an SSE stream into events and their type names.
func readNamedEvents(t *testing.T, body io.Reader) ([]openai.ResponseEvent, []string) {
	t.Helper()

	var events []openai.ResponseEvent
	var types []string

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	for _, block := range strings.Split(string(data), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var eventType, payload string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				eventType = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if payload == "" {
			continue
		}
		var event openai.ResponseEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("decode event %q: %v", payload, err)
		}
		events = append(events, event)
		types = append(types, eventType)
	}
	return events, types
}
