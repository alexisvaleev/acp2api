// Package logger renders the process log stream. It is an slog handler that
// prints one line per record as
//
//	<LEVEL> [<module>] | <message> | key=value key=value
//
// where <module> is lifted from a "module" attribute and omitted when absent,
// and the level name is colored when debug is on. The level and the module are
// rendered at a fixed width, so the message column does not move from line to
// line and the log reads as a list rather than a ragged edge.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// moduleKey is the attribute lifted out of a record and shown in brackets, so a
// subsystem reads as "[session]" instead of prefixing every message with it.
const moduleKey = "module"

// levelWidth is the width of every level name, so the columns to its right line
// up. It is the width of INFO, which forces ERROR and DEBUG to be truncated to
// ERRO and DEBU.
const levelWidth = 4

// moduleField is the width of the "[module]" column. It fits every subsystem
// the gateway logs under — [acp], [agent], [session], [acp2api], [handler] —
// with the longest of them filling it exactly. A longer one, [tool_calling:*],
// overflows the column instead of stretching every other line.
const moduleField = 9

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

	// The line is assembled first and written once, so a failed write is
	// reported instead of swallowed — a piecemeal handler cannot tell which of
	// its writes failed and ends up discarding every error to keep the rest.
	var b strings.Builder
	if h.color {
		b.WriteString(color)
		b.WriteString(name)
		b.WriteString("\033[0m")
	} else {
		b.WriteString(name)
	}
	b.WriteString(" ")
	b.WriteString(moduleColumn(module))
	b.WriteString(" | ")
	b.WriteString(r.Message)
	for i, a := range rest {
		if i == 0 {
			b.WriteString(" | ")
		} else {
			b.WriteString(" ")
		}
		b.WriteString(h.key(a.Key))
		b.WriteString("=")
		b.WriteString(a.Value.Resolve().String())
	}
	b.WriteString("\n")

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
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

// moduleColumn renders the bracketed subsystem padded to moduleField. A record
// without a module leaves the column blank, so its message still starts where
// the messages of its neighbours do; a module wider than the column is printed
// whole and overflows.
func moduleColumn(module string) string {
	if module == "" {
		return strings.Repeat(" ", moduleField)
	}
	bracketed := "[" + module + "]"
	if pad := moduleField - len(bracketed); pad > 0 {
		return bracketed + strings.Repeat(" ", pad)
	}
	return bracketed
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
		return "DEBU", "\033[36m" // cyan
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
