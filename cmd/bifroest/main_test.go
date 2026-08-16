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

func TestEnsureWritableFileRejectsExistingDirectory(t *testing.T) {
	// Exactly the mistake that prompted this check: pointing log.file at
	// a directory (e.g. a volume mount) instead of a file inside it.
	dir := t.TempDir()
	err := ensureWritableFile(dir)
	if err == nil {
		t.Fatal("expected an error when path is an existing directory")
	}
}

func TestEnsureWritableFileCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "bifroest.log")
	if err := ensureWritableFile(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist at %s: %v", path, err)
	}
}

func TestNewLoggerRejectsDirectoryLogFile(t *testing.T) {
	dir := t.TempDir()
	_, _, err := newLogger(&config.Config{Log: config.LogConfig{Level: "info", Path: dir}})
	if err == nil {
		t.Fatal("expected newLogger to fail when log.file is a directory")
	}
}

func TestNewLoggerNoFileConfiguredSucceeds(t *testing.T) {
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
