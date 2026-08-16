package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chruth/bifroest/internal/config"
)

func TestInstanceListEmpty(t *testing.T) {
	if got := instanceList(map[string]config.SourceInstance{}); got != "none" {
		t.Errorf("got %q, want none", got)
	}
}

func TestInstanceListSortedAndJoined(t *testing.T) {
	instances := map[string]config.SourceInstance{
		"anime": {}, "main": {}, "4k": {},
	}
	got := instanceList(instances)
	want := "4k,anime,main"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnsureWritableDirCreatesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")
	if err := ensureWritableDir(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("expected directory to exist at %s: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("expected %s to be a directory", dir)
	}
}

func TestEnsureWritableDirRejectsExistingFile(t *testing.T) {
	// The inverse of the original mistake: pointing log.dir at an actual
	// file rather than a directory to create files inside.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureWritableDir(file); err == nil {
		t.Fatal("expected an error when log.dir is an existing file")
	}
}

func TestNewLoggerRejectsFileAsLogDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := newLogger(&config.Config{Log: config.LogConfig{Level: "info", Dir: file}})
	if err == nil {
		t.Fatal("expected newLogger to fail when log.dir is a file")
	}
}

func TestNewLoggerCreatesLogFileInsideDir(t *testing.T) {
	dir := t.TempDir()
	log, closeLog, err := newLogger(&config.Config{Log: config.LogConfig{Level: "info", Dir: dir}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer closeLog()

	log.Info("hello")

	path := filepath.Join(dir, logFileName)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected log file to exist at %s: %v", path, err)
	}
}

func TestNewLoggerNoDirConfiguredSucceeds(t *testing.T) {
	log, closeLog, err := newLogger(&config.Config{Log: config.LogConfig{Level: "info"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if log == nil {
		t.Fatal("expected a non-nil logger")
	}
	if err := closeLog(); err != nil {
		t.Errorf("expected closeLog to be a no-op returning nil, got: %v", err)
	}
}
