package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestFanOutWritesToAllHandlers(t *testing.T) {
	var console, file bytes.Buffer
	h := NewFanOut(New(&console, slog.LevelInfo), NewPlain(&file, slog.LevelInfo))

	slog.New(h).Info("scan queued", "target", "plex")

	if !strings.Contains(console.String(), "scan queued") {
		t.Errorf("expected message in console output, got: %q", console.String())
	}
	if !strings.Contains(file.String(), "scan queued") {
		t.Errorf("expected message in file output, got: %q", file.String())
	}
}

func TestFanOutOnlyConsoleIsColored(t *testing.T) {
	var console, file bytes.Buffer
	h := NewFanOut(New(&console, slog.LevelInfo), NewPlain(&file, slog.LevelInfo))

	slog.New(h).Info("scan queued")

	if !strings.Contains(console.String(), "\x1b[") {
		t.Error("expected console output to be colored")
	}
	if strings.Contains(file.String(), "\x1b[") {
		t.Errorf("expected file output to be plain, got: %q", file.String())
	}
}

func TestFanOutRespectsPerHandlerLevel(t *testing.T) {
	var console, file bytes.Buffer
	// Console gets everything; the file only gets Warn and above.
	h := NewFanOut(New(&console, slog.LevelDebug), NewPlain(&file, slog.LevelWarn))

	logger := slog.New(h)
	logger.Info("routine event")
	logger.Warn("something worth keeping")

	if !strings.Contains(console.String(), "routine event") {
		t.Error("expected console to log the Info line")
	}
	if strings.Contains(file.String(), "routine event") {
		t.Errorf("expected file to skip the Info line, got: %q", file.String())
	}
	if !strings.Contains(file.String(), "something worth keeping") {
		t.Error("expected file to log the Warn line")
	}
}

func TestFanOutWithAttrsAppliesToAllHandlers(t *testing.T) {
	var console, file bytes.Buffer
	h := NewFanOut(New(&console, slog.LevelInfo), NewPlain(&file, slog.LevelInfo))

	slog.New(h).With("component", "queue").Info("scan completed")

	for name, buf := range map[string]*bytes.Buffer{"console": &console, "file": &file} {
		if !strings.Contains(stripANSI(buf.String()), "component=queue") {
			t.Errorf("%s: expected component=queue, got: %q", name, buf.String())
		}
	}
}

func TestFanOutEnabledIfAnyHandlerEnabled(t *testing.T) {
	h := NewFanOut(New(&bytes.Buffer{}, slog.LevelDebug), NewPlain(&bytes.Buffer{}, slog.LevelError))

	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected Enabled(Debug) to be true when at least one handler accepts it")
	}
}
