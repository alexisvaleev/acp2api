package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func limitWith(t *testing.T, stop string, maxTokens *int) *TextLimit {
	t.Helper()
	var raw json.RawMessage
	if stop != "" {
		raw = json.RawMessage(stop)
	}
	limit, err := NewTextLimit(raw, maxTokens)
	if err != nil {
		t.Fatal(err)
	}
	return limit
}

// drain pushes every piece and returns the emitted text.
func drain(limit *TextLimit, pieces []string) string {
	var out strings.Builder
	for _, piece := range pieces {
		emitted, _ := limit.Push(piece)
		out.WriteString(emitted)
	}
	out.WriteString(limit.Finish())
	return out.String()
}

func TestTextLimitInactivePassesEverythingThrough(t *testing.T) {
	limit := limitWith(t, "", nil)
	if limit.Active() {
		t.Fatal("a limiter with no stop and no cap must be inactive")
	}
	if got := drain(limit, []string{"hello ", "world"}); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestTextLimitTruncatesAtStop(t *testing.T) {
	limit := limitWith(t, `"STOP"`, nil)

	got := drain(limit, []string{"keep this ", "STOP", " and drop this"})
	if got != "keep this " {
		t.Fatalf("got %q", got)
	}
	if !limit.Stopped() {
		t.Fatal("the limiter should report that it stopped")
	}
}

func TestTextLimitDoesNotLeakAStopSequenceSplitAcrossDeltas(t *testing.T) {
	limit := limitWith(t, `"END"`, nil)

	var out strings.Builder
	for _, piece := range []string{"answer", "E", "N", "D", "more"} {
		emitted, _ := limit.Push(piece)
		out.WriteString(emitted)
	}
	out.WriteString(limit.Finish())

	if strings.Contains(out.String(), "END") {
		t.Fatalf("the stop sequence leaked: %q", out.String())
	}
	if out.String() != "answer" {
		t.Fatalf("got %q, want %q", out.String(), "answer")
	}
}

func TestTextLimitAcceptsAnArrayOfStops(t *testing.T) {
	limit := limitWith(t, `["X","YY"]`, nil)

	if got := drain(limit, []string{"abcYYdef"}); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestTextLimitHonoursTheTokenCap(t *testing.T) {
	max := 3 // roughly twelve bytes
	limit := limitWith(t, "", &max)

	got := drain(limit, []string{strings.Repeat("a", 40)})
	if len(got) > 12 {
		t.Fatalf("emitted %d bytes, want at most 12", len(got))
	}
	if !limit.Capped() {
		t.Fatal("the limiter should report that it was capped")
	}
	if limit.Stopped() {
		t.Fatal("a cap is not a stop")
	}
}

func TestTextLimitCapWinsOverStop(t *testing.T) {
	max := 2 // eight bytes
	limit := limitWith(t, `"never"`, &max)

	got := drain(limit, []string{strings.Repeat("b", 40)})
	if len(got) > 8 {
		t.Fatalf("emitted %d bytes, want at most 8", len(got))
	}
	if !limit.Capped() {
		t.Fatal("expected the cap to trigger")
	}
}

func TestNewTextLimitRejectsBadInput(t *testing.T) {
	if _, err := NewTextLimit(json.RawMessage(`{"not":"a stop"}`), nil); err == nil {
		t.Fatal("expected a non-string stop to be rejected")
	}
	negative := -1
	if _, err := NewTextLimit(nil, &negative); err == nil {
		t.Fatal("expected a negative max_tokens to be rejected")
	}
}
