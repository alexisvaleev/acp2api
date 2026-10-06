package openai

import (
	"strings"
	"testing"
)

const (
	testEnvelope = `{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}`
	fencedBody   = "```json\n" + testEnvelope + "\n```"
)

// splitEvery splits a string into n parts, to prove the stream survives deltas
// that land mid-JSON.
func splitEvery(s string, n int) []string {
	if n < 2 {
		return []string{s}
	}
	size := (len(s) + n - 1) / n
	var parts []string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		parts = append(parts, s[i:end])
	}
	return parts
}

func TestParseToolCalls(t *testing.T) {
	t.Run("object form", func(t *testing.T) {
		calls := ParseToolCalls(testEnvelope)
		if len(calls) != 1 {
			t.Fatalf("got %d calls", len(calls))
		}
		if calls[0].Function.Name != "get_weather" {
			t.Fatalf("name = %q", calls[0].Function.Name)
		}
		if calls[0].Type != "function" {
			t.Fatalf("type = %q", calls[0].Type)
		}
		if !strings.Contains(calls[0].Function.Arguments, "Paris") {
			t.Fatalf("arguments = %q", calls[0].Function.Arguments)
		}
	})

	t.Run("markdown fenced", func(t *testing.T) {
		if calls := ParseToolCalls(fencedBody); len(calls) != 1 {
			t.Fatalf("got %d calls", len(calls))
		}
	})

	t.Run("wrapped in prose", func(t *testing.T) {
		if calls := ParseToolCalls("Sure, let me check.\n" + testEnvelope + "\nDone."); len(calls) != 1 {
			t.Fatalf("got %d calls", len(calls))
		}
	})

	t.Run("bare array", func(t *testing.T) {
		bare := `[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]`
		if calls := ParseToolCalls(bare); len(calls) != 1 {
			t.Fatalf("got %d calls", len(calls))
		}
	})

	t.Run("fills in missing fields", func(t *testing.T) {
		calls := ParseToolCalls(`{"tool_calls":[{"function":{"name":"f"}}]}`)
		if len(calls) != 1 {
			t.Fatalf("got %d calls", len(calls))
		}
		if calls[0].ID == "" || calls[0].Type != "function" || calls[0].Function.Arguments != "{}" {
			t.Fatalf("call = %+v", calls[0])
		}
	})

	t.Run("drops calls with no name", func(t *testing.T) {
		if calls := ParseToolCalls(`{"tool_calls":[{"function":{"arguments":"{}"}}]}`); calls != nil {
			t.Fatalf("got %+v, want nil", calls)
		}
	})

	t.Run("plain prose is not a call", func(t *testing.T) {
		for _, text := range []string{"", "hello", `{"answer": 42}`, "{}", "not json at all"} {
			if calls := ParseToolCalls(text); calls != nil {
				t.Fatalf("ParseToolCalls(%q) = %+v, want nil", text, calls)
			}
		}
	})
}

func TestClassifyEnvelope(t *testing.T) {
	cases := map[string]EnvelopeState{
		"":                         EnvelopeNo,
		"hello":                    EnvelopeNo,
		`{"answer": 42}`:           EnvelopeNo,
		`{"tool_calls":[`:          EnvelopeMaybe,
		`{`:                        EnvelopeMaybe,
		`[`:                        EnvelopeMaybe,
		`{"other": 1`:              EnvelopeMaybe,
		testEnvelope:               EnvelopeYes,
		`{"tool_calls":[{"id":"1"`: EnvelopeMaybe,
	}
	for input, want := range cases {
		if got := ClassifyEnvelope(input); got != want {
			t.Fatalf("ClassifyEnvelope(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestToolStreamPassesPlainTextThrough(t *testing.T) {
	s := NewToolStream(true)
	if got := s.Push("hello "); got != "hello " {
		t.Fatalf("Push = %q", got)
	}
	if got := s.Push("world"); got != "world" {
		t.Fatalf("Push = %q", got)
	}
	rest, calls := s.Finish()
	if rest != "" || calls != nil {
		t.Fatalf("Finish = (%q, %+v)", rest, calls)
	}
}

func TestToolStreamIsInertWhenInactive(t *testing.T) {
	s := NewToolStream(false)
	if got := s.Push(testEnvelope); got != testEnvelope {
		t.Fatalf("an inactive stream must pass text through, got %q", got)
	}
	if rest, calls := s.Finish(); rest != "" || calls != nil {
		t.Fatalf("Finish = (%q, %+v)", rest, calls)
	}
}

func TestToolStreamHoldsAnEnvelopeSplitAcrossDeltas(t *testing.T) {
	s := NewToolStream(true)

	var streamed strings.Builder
	for _, part := range splitEvery(testEnvelope, 5) {
		streamed.WriteString(s.Push(part))
	}
	if streamed.String() != "" {
		t.Fatalf("the envelope leaked to the client as prose: %q", streamed.String())
	}

	rest, calls := s.Finish()
	if rest != "" {
		t.Fatalf("Finish released %q, want the calls", rest)
	}
	if len(calls) != 1 || calls[0].Function.Name != "get_weather" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestToolStreamReleasesJSONThatIsNotAnEnvelope(t *testing.T) {
	s := NewToolStream(true)

	if got := s.Push(`{"answer": 42}`); got != `{"answer": 42}` {
		t.Fatalf("a JSON answer must be released, got %q", got)
	}
	rest, calls := s.Finish()
	if rest != "" || calls != nil {
		t.Fatalf("Finish = (%q, %+v)", rest, calls)
	}
}

func TestToolStreamReleasesAsSoonAsItCannotBeAnEnvelope(t *testing.T) {
	s := NewToolStream(true)

	if got := s.Push(`{"ans`); got != "" {
		t.Fatalf("an open object must be held, got %q", got)
	}
	if got := s.Push(`wer":42}`); got != `{"answer":42}` {
		t.Fatalf("the released object = %q", got)
	}
}

func TestToolStreamStreamsProseBeforeAnEnvelope(t *testing.T) {
	s := NewToolStream(true)

	var streamed strings.Builder
	streamed.WriteString(s.Push("Let me check that. "))
	streamed.WriteString(s.Push(testEnvelope))

	if streamed.String() != "Let me check that. " {
		t.Fatalf("streamed = %q, want only the prose", streamed.String())
	}
	_, calls := s.Finish()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
}

// A brace in ordinary prose must not permanently disable detection: the
// envelope that follows still has to be recognised.
func TestToolStreamSurvivesABraceInProse(t *testing.T) {
	s := NewToolStream(true)

	var streamed strings.Builder
	streamed.WriteString(s.Push("Use {name} as a placeholder. "))
	streamed.WriteString(s.Push(testEnvelope))

	if streamed.String() != "Use {name} as a placeholder. " {
		t.Fatalf("streamed = %q", streamed.String())
	}
	_, calls := s.Finish()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestToolStreamReleasesAnUnfinishedObjectAtFinish(t *testing.T) {
	s := NewToolStream(true)

	if got := s.Push(`{"truncated`); got != "" {
		t.Fatalf("an open object must be held, got %q", got)
	}
	rest, calls := s.Finish()
	if calls != nil {
		t.Fatalf("calls = %+v, want nil", calls)
	}
	if rest != `{"truncated` {
		t.Fatalf("Finish must release the held text, got %q", rest)
	}
}
