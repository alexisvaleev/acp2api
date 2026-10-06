package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EnvelopeState classifies a buffer against the tool-call envelope contract.
type EnvelopeState int

const (
	// EnvelopeNo means the buffer cannot become a tool-call envelope, so it is
	// safe to stream.
	EnvelopeNo EnvelopeState = iota
	// EnvelopeMaybe means the buffer could still become one and must be held
	// back until the turn ends or the question is settled.
	EnvelopeMaybe
	// EnvelopeYes means the buffer is a complete envelope.
	EnvelopeYes
)

// ClassifyEnvelope reports whether s is, could still become, or cannot be a
// tool-call envelope.
//
// The rule that makes this safe: the envelope's discriminator (`"tool_calls"`)
// appears before any closing brace, so a closing brace without the
// discriminator proves the buffer is not an envelope. That is what lets the
// gateway release a JSON-looking answer instead of swallowing it.
func ClassifyEnvelope(s string) EnvelopeState {
	t := strings.TrimSpace(s)
	if t == "" {
		return EnvelopeNo
	}
	if t[0] != '{' && t[0] != '[' {
		return EnvelopeNo
	}
	if len(ParseToolCalls(t)) > 0 {
		return EnvelopeYes
	}
	if !strings.Contains(t, `"tool_calls"`) {
		if strings.ContainsAny(t, "}]") {
			return EnvelopeNo
		}
		return EnvelopeMaybe
	}
	return EnvelopeMaybe
}

// ParseToolCalls extracts tool calls from an agent message.
//
// The agent is asked to reply with `{"tool_calls":[…]}`, but models wrap it in
// prose, fence it as markdown, and occasionally emit a bare array. All three
// are accepted. Anything else yields nil, so the caller can fall back to
// treating the text as the assistant's answer.
func ParseToolCalls(text string) []ToolCall {
	for _, candidate := range envelopeCandidates(text) {
		if calls := decodeCalls(candidate); len(calls) > 0 {
			return normalizeCalls(calls)
		}
	}
	return nil
}

// envelopeCandidates returns the substrings worth trying, most specific first.
func envelopeCandidates(text string) []string {
	t := strings.TrimSpace(stripCodeFences(text))
	if t == "" {
		return nil
	}

	out := []string{t}
	start := strings.IndexAny(t, "{[")
	if start < 0 {
		return out
	}
	if start > 0 {
		out = append(out, t[start:])
	}
	if end := strings.LastIndexAny(t, "}]"); end > start {
		out = append(out, t[start:end+1])
	}
	return out
}

// stripCodeFences removes a markdown fence wrapper, if present.
func stripCodeFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	if i := strings.Index(t, "\n"); i >= 0 {
		t = t[i+1:]
	}
	if i := strings.LastIndex(t, "```"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// decodeCalls tries the object form and then the bare array form.
func decodeCalls(s string) []ToolCall {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var envelope struct {
		ToolCalls []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(s), &envelope); err == nil && len(envelope.ToolCalls) > 0 {
		return envelope.ToolCalls
	}

	var bare []ToolCall
	if err := json.Unmarshal([]byte(s), &bare); err == nil && len(bare) > 0 {
		return bare
	}
	return nil
}

// normalizeCalls fills in what an OpenAI client requires and drops calls that
// cannot be executed because they name no function.
func normalizeCalls(calls []ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for i, call := range calls {
		if strings.TrimSpace(call.Function.Name) == "" {
			continue
		}
		if call.Type == "" {
			call.Type = "function"
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%s_%d", call.Function.Name, i+1)
		}
		if strings.TrimSpace(call.Function.Arguments) == "" {
			call.Function.Arguments = "{}"
		}
		out = append(out, call)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

/* ---- streaming hold-back ---- */

// ToolStream decides, while a turn streams, which text is the caller's answer
// and which might be a tool-call envelope.
//
// It is used only when the caller supplied tools; without them there is no
// envelope contract, so text passes through untouched.
//
// The rule: text before the first brace is never part of an envelope, so it
// streams immediately. From that brace onward the text is held only while it
// could still be an envelope, and released the moment it provably cannot be.
// Memory is therefore bounded by the size of one candidate, and the stream
// fails open — a JSON-looking answer is delayed, never swallowed.
//
// A model that appends prose *after* a complete envelope loses that prose: the
// contract asks for the JSON object and nothing else.
type ToolStream struct {
	active bool
	buf    strings.Builder
}

// NewToolStream creates a hold-back stream. A nil or inactive stream passes
// every delta straight through.
func NewToolStream(active bool) *ToolStream {
	return &ToolStream{active: active}
}

// Push adds a text delta and returns the text that is safe to stream now.
func (t *ToolStream) Push(text string) string {
	if t == nil || !t.active {
		return text
	}
	t.buf.WriteString(text)
	s := t.buf.String()

	start := strings.IndexAny(s, "{[")
	if start < 0 {
		// No brace anywhere, so nothing here can open an envelope. A brace is a
		// single byte and cannot be split across deltas, so clearing is safe.
		t.buf.Reset()
		return s
	}

	prefix, tail := s[:start], s[start:]
	if ClassifyEnvelope(tail) == EnvelopeNo {
		// Provably not an envelope: release it and keep watching for the next one.
		t.buf.Reset()
		return s
	}
	t.buf.Reset()
	t.buf.WriteString(tail)
	return prefix
}

// Finish settles the turn: it returns the text still held back and any tool
// calls the buffer encodes. At most one of the two is non-empty.
func (t *ToolStream) Finish() (string, []ToolCall) {
	if t == nil || !t.active {
		return "", nil
	}
	s := t.buf.String()
	if s == "" {
		return "", nil
	}
	if calls := ParseToolCalls(s); len(calls) > 0 {
		return "", calls
	}
	// Not an envelope after all: release what was held.
	return s, nil
}
