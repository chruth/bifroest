package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestEnabledRespectsLevel(t *testing.T) {
	h := New(&bytes.Buffer{}, slog.LevelWarn)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected Info to be disabled when minimum level is Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("expected Error to be enabled when minimum level is Warn")
	}
}

func TestHandleWritesTimeLevelMessageAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := New(&buf, slog.LevelInfo)

	logger := slog.New(h)
	logger.Info("scan queued", "target", "plex", "path", "/media/tv/Show")

	out := stripANSI(buf.String())
	if !strings.Contains(out, "INFO") {
		t.Errorf("expected level INFO in output, got: %q", out)
	}
	if !strings.Contains(out, "scan queued") {
		t.Errorf("expected message in output, got: %q", out)
	}
	if !strings.Contains(out, "target=plex") {
		t.Errorf("expected target=plex in output, got: %q", out)
	}
	if !strings.Contains(out, "path=/media/tv/Show") {
		t.Errorf("expected path attr in output, got: %q", out)
	}
}

func TestColorsPresentByDefault(t *testing.T) {
	var buf bytes.Buffer
	h := New(&buf, slog.LevelInfo)
	slog.New(h).Info("hello")

	if !strings.Contains(buf.String(), "\x1b[") {
		t.Error("expected ANSI escape codes in output by default")
	}
}

func TestNoColorEnvDisablesColors(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	h := New(&buf, slog.LevelInfo)
	slog.New(h).Info("hello")

	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("expected no ANSI escape codes with NO_COLOR set, got: %q", buf.String())
	}
}

func TestLevelColorsDiffer(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, colorBlue},
		{slog.LevelInfo, colorGreen},
		{slog.LevelWarn, colorYellow},
		{slog.LevelError, colorRed},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		h := New(&buf, slog.LevelDebug)
		logger := slog.New(h)
		logger.Log(context.Background(), tc.level, "msg")
		if !strings.Contains(buf.String(), tc.want) {
			t.Errorf("level %s: expected color %q in output, got: %q", tc.level, tc.want, buf.String())
		}
	}
}

func TestErrorAttrIsAlwaysRed(t *testing.T) {
	var buf bytes.Buffer
	h := New(&buf, slog.LevelInfo)
	// A retryable failure logged at Warn, not Error - the error value
	// itself should still stand out.
	slog.New(h).Warn("scan failed, will retry", "error", "connection refused")

	if !strings.Contains(buf.String(), colorRed+`"connection refused"`+colorReset) {
		t.Errorf("expected error value highlighted in red, got: %q", buf.String())
	}
}

func TestValuesWithSpacesAreQuoted(t *testing.T) {
	var buf bytes.Buffer
	h := New(&buf, slog.LevelInfo)
	slog.New(h).Info("received webhook", "path", "/media/tv/Breaking Bad/Season 05")

	out := stripANSI(buf.String())
	if !strings.Contains(out, `path="/media/tv/Breaking Bad/Season 05"`) {
		t.Errorf("expected quoted path with spaces, got: %q", out)
	}
}

func TestWithAttrsAccumulates(t *testing.T) {
	var buf bytes.Buffer
	h := New(&buf, slog.LevelInfo)
	logger := slog.New(h).With("component", "queue").With("worker", 1)
	logger.Info("scan completed", "job_id", 42)

	out := stripANSI(buf.String())
	for _, want := range []string{"component=queue", "worker=1", "job_id=42"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %q", want, out)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
