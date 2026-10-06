package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FinishLength is the finish_reason for output cut off by a token cap.
const FinishLength = "length"

// TextLimit applies `stop` and `max_tokens` to an answer.
//
// The agent owns its own generation, so these cannot be handed to it; the
// gateway enforces them on the way out. While streaming, up to
// len(longest stop) - 1 characters are held back, because a stop sequence can
// straddle a delta boundary and truncating late would leak it to the caller.
type TextLimit struct {
	stops      []string
	maxStopLen int
	maxChars   int
	held       strings.Builder
	emitted    int
	stopped    bool
	capped     bool
}

// NewTextLimit builds a limiter from the request's stop sequences and token cap.
// A nil or zero-valued request yields a limiter that passes everything through.
func NewTextLimit(stop json.RawMessage, maxTokens *int) (*TextLimit, error) {
	limit := &TextLimit{}

	stops, err := parseStop(stop)
	if err != nil {
		return nil, err
	}
	limit.stops = stops
	for _, s := range stops {
		if len(s) > limit.maxStopLen {
			limit.maxStopLen = len(s)
		}
	}

	if maxTokens != nil {
		if *maxTokens < 0 {
			return nil, fmt.Errorf("max_tokens must not be negative")
		}
		// EstimateTokens is roughly four bytes per token, so the cap converts
		// back the same way.
		limit.maxChars = *maxTokens * 4
	}
	return limit, nil
}

// parseStop decodes the OpenAI stop field: a string or an array of strings.
func parseStop(stop json.RawMessage) ([]string, error) {
	if len(stop) == 0 {
		return nil, nil
	}
	var single string
	if err := json.Unmarshal(stop, &single); err == nil {
		if single == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(stop, &many); err != nil {
		return nil, fmt.Errorf("stop must be a string or an array of strings: %w", err)
	}
	out := make([]string, 0, len(many))
	for _, s := range many {
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// Active reports whether the limiter can change anything. An inactive limiter
// is skipped entirely so the common path stays a straight copy.
func (l *TextLimit) Active() bool {
	return l != nil && (len(l.stops) > 0 || l.maxChars > 0)
}

// Push adds a delta and returns the text that may be emitted, plus whether the
// answer is finished. The second result is true once a stop sequence was found
// or the token cap was reached.
func (l *TextLimit) Push(text string) (string, bool) {
	if !l.Active() {
		return text, false
	}
	if l.stopped || l.capped {
		return "", true
	}

	l.held.WriteString(text)
	buf := l.held.String()

	if idx, found := l.earliestStop(buf); found {
		l.stopped = true
		l.held.Reset()
		l.emitted += idx
		return buf[:idx], true
	}

	if l.maxChars > 0 && l.emitted+len(buf) >= l.maxChars {
		room := l.maxChars - l.emitted
		if room < 0 {
			room = 0
		}
		l.capped = true
		l.held.Reset()
		l.emitted += room
		return buf[:room], true
	}

	// Hold back enough to notice a stop sequence that straddles this boundary.
	// With no stop sequences there is nothing to straddle, so nothing is held.
	keep := 0
	if l.maxStopLen > 0 {
		keep = l.maxStopLen - 1
	}
	if len(buf) <= keep {
		return "", false
	}
	out := buf[:len(buf)-keep]
	l.held.Reset()
	l.held.WriteString(buf[len(buf)-keep:])
	l.emitted += len(out)
	return out, false
}

// Finish returns whatever is still held back after the turn ended.
func (l *TextLimit) Finish() string {
	if !l.Active() || l.stopped || l.capped {
		return ""
	}
	out := l.held.String()
	l.held.Reset()
	l.emitted += len(out)
	return out
}

// Stopped reports whether a stop sequence ended the answer.
func (l *TextLimit) Stopped() bool { return l != nil && l.stopped }

// Capped reports whether the token cap ended the answer.
func (l *TextLimit) Capped() bool { return l != nil && l.capped }

// earliestStop returns the first index at which any stop sequence begins.
func (l *TextLimit) earliestStop(s string) (int, bool) {
	best := -1
	for _, stop := range l.stops {
		if idx := strings.Index(s, stop); idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best, best >= 0
}
