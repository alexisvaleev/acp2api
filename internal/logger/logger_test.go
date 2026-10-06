package logger

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
)

func render(t *testing.T, level slog.Level, color bool, log func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	log(slog.New(newHandler(&buf, level, color)))
	return buf.String()
}

func TestHandlerFormatsLevelMessageAndAttrs(t *testing.T) {
	got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.Info("agent ready", "agent", "devin", "pid", 91240, "images", true)
	})
	want := "INFO | agent ready | agent=devin pid=91240 images=true\n"
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
	want := "INFO | listening\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerLevelNames(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "DEBUG | message\n"},
		{slog.LevelInfo, "INFO | message\n"},
		{slog.LevelWarn, "WARN | message\n"},
		{slog.LevelError, "ERRO | message\n"},
	}
	for _, tc := range cases {
		got := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
			l.Log(nil, tc.level, "message")
		})
		if got != tc.want {
			t.Errorf("level %s: got %q, want %q", tc.level, got, tc.want)
		}
	}
}

func TestHandlerDropsRecordsBelowLevel(t *testing.T) {
	got := render(t, slog.LevelInfo, false, func(l *slog.Logger) {
		l.Debug("hidden")
		l.Info("shown")
	})
	if want := "INFO | shown\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHandlerColorsLevelOnlyWhenEnabled(t *testing.T) {
	plain := render(t, slog.LevelDebug, false, func(l *slog.Logger) {
		l.Warn("message")
	})
	if want := "WARN | message\n"; plain != want {
		t.Fatalf("plain: got %q, want %q", plain, want)
	}

	colored := render(t, slog.LevelDebug, true, func(l *slog.Logger) {
		l.Warn("message")
	})
	if want := "\033[33mWARN\033[0m | message\n"; colored != want {
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
	want := "INFO | message | acp.method=initialize\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
