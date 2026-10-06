package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolChoice(t *testing.T) {
	t.Run("absent means auto", func(t *testing.T) {
		choice, err := ParseToolChoice(nil)
		if err != nil {
			t.Fatal(err)
		}
		if choice.Mode != ToolChoiceAuto || !choice.UsesCallerTools() {
			t.Fatalf("choice = %+v", choice)
		}
	})

	t.Run("modes", func(t *testing.T) {
		for _, mode := range []string{ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired} {
			raw, _ := json.Marshal(mode)
			choice, err := ParseToolChoice(raw)
			if err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			if choice.Mode != mode {
				t.Fatalf("mode = %q, want %q", choice.Mode, mode)
			}
		}
	})

	t.Run("named function", func(t *testing.T) {
		choice, err := ParseToolChoice(json.RawMessage(`{"type":"function","function":{"name":"get_weather"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if choice.Mode != ToolChoiceFunction || choice.Function != "get_weather" {
			t.Fatalf("choice = %+v", choice)
		}
	})

	t.Run("rejects nonsense", func(t *testing.T) {
		for _, raw := range []string{`"sometimes"`, `{"type":"function"}`, `{"type":"function","function":{}}`} {
			if _, err := ParseToolChoice(json.RawMessage(raw)); err == nil {
				t.Fatalf("ParseToolChoice(%s) succeeded, want an error", raw)
			}
		}
	})
}

func TestToolChoiceNoneForbidsCallerTools(t *testing.T) {
	if (ToolChoice{Mode: ToolChoiceNone}).UsesCallerTools() {
		t.Fatal("none must forbid caller tools")
	}
	for _, mode := range []string{ToolChoiceAuto, ToolChoiceRequired, ToolChoiceFunction} {
		if !(ToolChoice{Mode: mode}).UsesCallerTools() {
			t.Fatalf("%s must permit caller tools", mode)
		}
	}
}

func sampleTools() []Tool {
	return []Tool{{
		Type: "function",
		Function: FunctionDef{
			Name:        "get_weather",
			Description: "Look up the weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		},
	}}
}

func TestToolPreamble(t *testing.T) {
	t.Run("describes the functions", func(t *testing.T) {
		got := ToolPreamble(sampleTools(), ToolChoice{Mode: ToolChoiceAuto})
		for _, want := range []string{"get_weather", "Look up the weather", `"city"`, `"tool_calls"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("preamble is missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("is empty without tools", func(t *testing.T) {
		if got := ToolPreamble(nil, ToolChoice{Mode: ToolChoiceAuto}); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("is empty when the choice forbids tools", func(t *testing.T) {
		if got := ToolPreamble(sampleTools(), ToolChoice{Mode: ToolChoiceNone}); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("required demands a call", func(t *testing.T) {
		got := ToolPreamble(sampleTools(), ToolChoice{Mode: ToolChoiceRequired})
		if !strings.Contains(got, "must call one of the functions") {
			t.Fatalf("preamble = %s", got)
		}
	})

	t.Run("named demands that function", func(t *testing.T) {
		got := ToolPreamble(sampleTools(), ToolChoice{Mode: ToolChoiceFunction, Function: "get_weather"})
		if !strings.Contains(got, "must call the function get_weather") {
			t.Fatalf("preamble = %s", got)
		}
	})
}

func TestRenderToolResults(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: rawJSON(t, "what is the weather?")},
		{Role: "assistant", Content: rawJSON(t, "")},
		{Role: "tool", ToolCallID: "call_1", Content: rawJSON(t, "18C and sunny")},
	}

	got := RenderToolResults(messages)
	if !strings.Contains(got, "call_1") || !strings.Contains(got, "18C and sunny") {
		t.Fatalf("rendered = %q", got)
	}
	if strings.Contains(got, "what is the weather?") {
		t.Fatalf("only tool messages belong here, got %q", got)
	}
}

func TestBuildTurnGivesToolResultsPrecedence(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: rawJSON(t, "what is the weather?")},
		{Role: "tool", ToolCallID: "call_1", Content: rawJSON(t, "18C")},
	}

	got, err := BuildTurn(messages, true, nil, ToolChoice{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "18C") {
		t.Fatalf("the tool result must be the turn's input, got %q", got)
	}
	if strings.Contains(got, "what is the weather?") {
		t.Fatalf("the user turn must not be repeated on a persistent session, got %q", got)
	}
}

func TestBuildTurnPrependsThePreamble(t *testing.T) {
	messages := []Message{{Role: "user", Content: rawJSON(t, "weather?")}}

	withTools, err := BuildTurn(messages, true, sampleTools(), ToolChoice{Mode: ToolChoiceAuto})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(withTools, "[system — caller tools]") {
		t.Fatalf("preamble missing:\n%s", withTools)
	}
	if !strings.HasSuffix(withTools, "weather?") {
		t.Fatalf("the turn must follow the preamble:\n%s", withTools)
	}

	withoutTools, err := BuildTurn(messages, true, nil, ToolChoice{})
	if err != nil {
		t.Fatal(err)
	}
	if withoutTools != "weather?" {
		t.Fatalf("without tools the prompt must be untouched, got %q", withoutTools)
	}
}

func TestHasToolResults(t *testing.T) {
	with := []Message{{Role: "user", Content: rawJSON(t, "x")}, {Role: "tool", Content: rawJSON(t, "y")}}
	if !HasToolResults(with) {
		t.Fatal("expected trailing tool results")
	}
	without := []Message{{Role: "tool", Content: rawJSON(t, "y")}, {Role: "user", Content: rawJSON(t, "x")}}
	if HasToolResults(without) {
		t.Fatal("a tool result that is not trailing must not count")
	}
}
