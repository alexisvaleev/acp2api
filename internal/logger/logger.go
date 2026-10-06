// Package logger renders the process log stream. It is an slog handler that
// prints one line per record as
//
//	<LEVEL> [<module>] | <message> | key=value key=value
//
// where <module> is lifted from a "module" attribute and omitted when absent,
// and the four-character level name is colored when debug is on.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// moduleKey is the attribute lifted out of a record and shown in brackets, so a
// subsystem reads as "[session]" instead of prefixing every message with it.
const moduleKey = "module"

type handler struct {
	w      io.Writer
	level  slog.Level
	attrs  []slog.Attr
	groups []string
	color  bool
	mu     *sync.Mutex
}

func newHandler(w io.Writer, level slog.Level, color bool) *handler {
	return &handler{w: w, level: level, color: color, mu: &sync.Mutex{}}
}

func (h *handler) clone() *handler {
	return &handler{
		w:      h.w,
		level:  h.level,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
		color:  h.color,
		mu:     h.mu,
	}
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	name, color := h.formatLevel(r.Level)
	var module string
	rest := attrs[:0]
	for _, a := range attrs {
		if a.Key == moduleKey {
			module = a.Value.String()
			continue
		}
		rest = append(rest, a)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.color {
		fmt.Fprintf(h.w, "%s%s\033[0m", color, name)
	} else {
		fmt.Fprint(h.w, name)
	}
	if module != "" {
		fmt.Fprintf(h.w, " [%s] | %s", module, r.Message)
	} else {
		fmt.Fprintf(h.w, " | %s", r.Message)
	}
	for i, a := range rest {
		if i == 0 {
			fmt.Fprint(h.w, " | ")
		} else {
			fmt.Fprint(h.w, " ")
		}
		fmt.Fprintf(h.w, "%s=%s", h.key(a.Key), a.Value.Resolve().String())
	}
	fmt.Fprint(h.w, "\n")
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := h.clone()
	h2.attrs = append(h2.attrs, attrs...)
	return h2
}

func (h *handler) WithGroup(name string) slog.Handler {
	h2 := h.clone()
	h2.groups = append(h2.groups, name)
	return h2
}

func (h *handler) formatLevel(l slog.Level) (string, string) {
	switch {
	case l >= slog.LevelError:
		return "ERRO", "\033[31m" // red
	case l >= slog.LevelWarn:
		return "WARN", "\033[33m" // yellow
	case l >= slog.LevelInfo:
		return "INFO", "\033[32m" // green
	default:
		return "DEBUG", "\033[36m" // cyan
	}
}

func (h *handler) key(k string) string {
	if len(h.groups) == 0 {
		return k
	}
	return strings.Join(h.groups, ".") + "." + k
}

// New returns the process logger on stderr: debug level and colored level names
// when debug is set, info level and plain output otherwise.
func New(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(newHandler(os.Stderr, level, debug))
}
