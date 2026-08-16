package mount

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnchorExistsMeansAvailable(t *testing.T) {
	anchor := filepath.Join(t.TempDir(), "anchor.bin")
	if err := os.WriteFile(anchor, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(anchor, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !m.Available() {
		t.Error("expected mount to be available when anchor exists")
	}
}

func TestAnchorMissingMeansUnavailable(t *testing.T) {
	anchor := filepath.Join(t.TempDir(), "does-not-exist.bin")

	m := New(anchor, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if m.Available() {
		t.Error("expected mount to be unavailable when anchor is missing")
	}
}

func TestCheckDetectsTransition(t *testing.T) {
	dir := t.TempDir()
	anchor := filepath.Join(dir, "anchor.bin")

	m := New(anchor, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if m.Available() {
		t.Fatal("expected initially unavailable")
	}

	if err := os.WriteFile(anchor, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.check()
	if !m.Available() {
		t.Error("expected available after anchor appears")
	}

	if err := os.Remove(anchor); err != nil {
		t.Fatal(err)
	}
	m.check()
	if m.Available() {
		t.Error("expected unavailable after anchor removed")
	}
}
