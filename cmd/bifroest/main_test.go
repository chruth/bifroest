package main

import (
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
