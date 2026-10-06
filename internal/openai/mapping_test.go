package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/quonaro/acp2api/internal/acp"
)

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMessageText(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		m := Message{Role: "user", Content: rawJSON(t, "hello")}
		if got := m.Text(); got != "hello" {
			t.Fatalf("Text() = %q", got)
		}
	})

	t.Run("parts", func(t *testing.T) {
		m := Message{Role: "user", Content: rawJSON(t, []map[string]string{
			{"type": "text", "text": "one "},
			{"type": "text", "text": "two"},
			{"type": "image_url"},
		})}
		if got := m.Text(); got != "one two[image]" {
			t.Fatalf("Text() = %q", got)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := (Message{}).Text(); got != "" {
			t.Fatalf("Text() = %q", got)
		}
	})
}

func TestBuildPromptPersistentSendsOnlyTheNewestTurn(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: rawJSON(t, "be brief")},
		{Role: "user", Content: rawJSON(t, "first")},
		{Role: "assistant", Content: rawJSON(t, "answer")},
		{Role: "user", Content: rawJSON(t, "second")},
	}
	got, err := BuildPrompt(messages, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "second" {
		t.Fatalf("prompt = %q, want only the newest user turn", got)
	}
}

func TestBuildPromptEphemeralFlattensTranscript(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: rawJSON(t, "be brief")},
		{Role: "user", Content: rawJSON(t, "first")},
		{Role: "assistant", Content: rawJSON(t, "answer")},
		{Role: "user", Content: rawJSON(t, "second")},
	}
	got, err := BuildPrompt(messages, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## system", "be brief", "## user", "first", "## assistant", "answer", "second"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q is missing %q", got, want)
		}
	}
}

func TestBuildPromptLoneUserMessageHasNoHeader(t *testing.T) {
	got, err := BuildPrompt([]Message{{Role: "user", Content: rawJSON(t, "just this")}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "just this" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestBuildPromptRejectsEmptyRequests(t *testing.T) {
	if _, err := BuildPrompt(nil, false); err == nil {
		t.Fatal("expected an empty request to fail")
	}
	if _, err := BuildPrompt([]Message{{Role: "assistant", Content: rawJSON(t, "hi")}}, true); err == nil {
		t.Fatal("expected a persistent prompt with no user turn to fail")
	}
}

func TestFromUpdate(t *testing.T) {
	t.Run("message chunk is text", func(t *testing.T) {
		text, step := FromUpdate(acp.SessionUpdate{
			SessionUpdate: acp.UpdateAgentMessageChunk,
			Content:       &acp.ContentBlock{Type: "text", Text: "hi"},
		})
		if text != "hi" || step != nil {
			t.Fatalf("text=%q step=%+v", text, step)
		}
	})

	t.Run("thought is a step, not content", func(t *testing.T) {
		text, step := FromUpdate(acp.SessionUpdate{
			SessionUpdate: acp.UpdateAgentThoughtChunk,
			Content:       &acp.ContentBlock{Type: "text", Text: "hmm"},
		})
		if text != "" {
			t.Fatalf("text = %q, want empty", text)
		}
		if step == nil || step.Type != StepThought || step.Text != "hmm" {
			t.Fatalf("step = %+v", step)
		}
	})

	t.Run("tool call", func(t *testing.T) {
		text, step := FromUpdate(acp.SessionUpdate{
			SessionUpdate: acp.UpdateToolCall,
			ToolCallID:    "tc1",
			Title:         "Run tests",
			Kind:          "execute",
			Status:        "in_progress",
		})
		if text != "" {
			t.Fatalf("text = %q", text)
		}
		if step == nil || step.Type != StepToolCall || step.ToolCallID != "tc1" || step.Kind != "execute" {
			t.Fatalf("step = %+v", step)
		}
	})

	t.Run("plan", func(t *testing.T) {
		_, step := FromUpdate(acp.SessionUpdate{
			SessionUpdate: acp.UpdatePlan,
			Entries:       []acp.PlanEntry{{Content: "step one"}, {Content: "step two"}},
		})
		if step == nil || step.Text != "step one; step two" {
			t.Fatalf("step = %+v", step)
		}
	})

	t.Run("unrelated update is ignored", func(t *testing.T) {
		text, step := FromUpdate(acp.SessionUpdate{SessionUpdate: acp.UpdateAvailableCommands})
		if text != "" || step != nil {
			t.Fatalf("text=%q step=%+v", text, step)
		}
	})
}

func TestFinishReason(t *testing.T) {
	cases := map[string]string{
		acp.StopEndTurn:   "stop",
		acp.StopMaxTokens: "length",
		acp.StopRefusal:   "content_filter",
		acp.StopCancelled: "stop",
		"":                "stop",
	}
	for stop, want := range cases {
		if got := FinishReason(stop); got != want {
			t.Fatalf("FinishReason(%q) = %q, want %q", stop, got, want)
		}
	}
}

func TestStepLogIsBounded(t *testing.T) {
	var log StepLog
	for i := 0; i < MaxSteps+10; i++ {
		log.Add(&Step{Type: StepToolCall})
	}
	if got := len(log.Steps()); got != MaxSteps {
		t.Fatalf("collected %d steps, want %d", got, MaxSteps)
	}

	var empty StepLog
	if empty.Steps() != nil {
		t.Fatal("an empty log must report nil steps")
	}
}
