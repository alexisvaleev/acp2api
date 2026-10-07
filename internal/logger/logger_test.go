package logger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func render(t *testing.T, level slog.Level, color bool, log func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	log(slog.New(newHandler(&buf, level, color)))
	return buf.String()
}

// blankModule is the module column of a record that carries no module: the
// "[module]" field stays blank so the message still starts at the same offset.
var blankModule = strings.Repeat(" ", moduleField)

func TestHandlerFormatsLevelMessageAndAttrs(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.Info("agent ready", "agent", "devin", "pid", 91240, "images", true)
	})
	want := "INFO " + blankModule + " | agent ready | agent=devin pid=91240 images=true\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerLiftsModuleIntoBrackets(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.With("module", "session").Info("agent ready", "agent", "devin")
	})
	want := "INFO [session] | agent ready | agent=devin\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerWithoutAttrs(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.Info("listening")
	})
	want := "INFO " + blankModule + " | listening\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerLevelNames(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "DEBU [session] | message\n"},
		{slog.LevelInfo, "INFO [session] | message\n"},
		{slog.LevelWarn, "WARN [session] | message\n"},
		{slog.LevelError, "ERRO [session] | message\n"},
	}
	for _, tc := range cases {
		got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
			l.With("module", "session").Log(context.Background(), tc.level, "message")
		})
		if got != tc.want {
			t.Errorf("level %s: got %q, want %q", tc.level, got, tc.want)
		}
	}
}

func TestLevelNamesShareOneWidth(t *testing.T) {
	h := newHandler(io.Discard, slog.LevelDebug, false)
	levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
	for _, level := range levels {
		name, _ := h.formatLevel(level)
		if len(name) != levelWidth {
			t.Errorf("level %s renders as %q, want %d characters", level, name, levelWidth)
		}
	}
}

// The message column is what the reader scans down, so it must not move with
// the level or the subsystem.
func TestHandlerAlignsMessageColumn(t *testing.T) {
	levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
	modules := []string{"", "acp", "agent", "session", "acp2api"}
	column := -1
	for _, level := range levels {
		for _, module := range modules {
			got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
				l.With("module", module).Log(context.Background(), level, "message")
			})
			at := strings.Index(got, "message")
			if at < 0 {
				t.Fatalf("level %s module %q: no message in %q", level, module, got)
			}
			if column < 0 {
				column = at
			}
			if at != column {
				t.Errorf("level %s module %q: message starts at %d, want %d (%q)",
					level, module, at, column, got)
			}
		}
	}
}

func TestHandlerLongModuleOverflowsTheColumn(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.With("module", "tool_calling:external").Debug("tool_call")
	})
	want := "DEBU [tool_calling:external] | tool_call\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerShortModulePadsTheColumn(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.With("module", "agent").Info("credential resolved")
	})
	want := "INFO [agent]   | credential resolved\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerDropsRecordsBelowLevel(t *testing.T) {
	got := render(t, slog.LevelInfo, false, func(l *slog.Logger) {
		l.With("module", "session").Debug("hidden")
		l.With("module", "session").Info("shown")
	})
	if want := "INFO [session] | shown\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerColorsLevelOnlyWhenEnabled(t *testing.T) {
	plain := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.With("module", "session").Warn("message")
	})
	if want := "WARN [session] | message\n"; plain != want {
		t.Fatalf("plain: got %q, want %q", plain, want)
	}

	colored := render(t, slog.LevelDebug, true, func(l *slog.Logger) {
		l.With("module", "session").Warn("message")
	})
	if want := "\033[33mWARN\033[0m [session] | message\n"; colored != want {
		t.Fatalf("colored: got %q, want %q", colored, want)
	}
}

func TestHandlerWithAttrsPersistAndErrorRenders(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.With("module", "session").With("agent", "devin").Info("failed", "error", errors.New("boom"))
	})
	want := "INFO [session] | failed | agent=devin error=boom\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerGroupPrefixesKeys(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.WithGroup("acp").Info("message", "method", "initialize")
	})
	want := "INFO " + blankModule + " | message | acp.method=initialize\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
