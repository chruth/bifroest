package webhook

import "testing"

const sonarrDownloadPayload = `{
  "series": {"id": 1, "title": "Breaking Bad", "path": "/tv/Breaking Bad"},
  "episodes": [{"seasonNumber": 5, "episodeNumber": 1}],
  "episodeFile": {
    "id": 123,
    "relativePath": "Season 05/S05E01.mkv",
    "path": "/tv/Breaking Bad/Season 05/S05E01.mkv",
    "quality": "HDTV-1080p"
  },
  "isUpgrade": false,
  "downloadClient": "SABnzbd",
  "downloadId": "abc123",
  "eventType": "Download"
}`

const sonarrUpgradePayload = `{
  "series": {"id": 1, "title": "Breaking Bad", "path": "/tv/Breaking Bad"},
  "episodes": [{"seasonNumber": 5, "episodeNumber": 1}],
  "episodeFile": {
    "id": 124,
    "relativePath": "Season 05/S05E01.mkv",
    "path": "/tv/Breaking Bad/Season 05/S05E01.mkv",
    "quality": "Bluray-1080p"
  },
  "isUpgrade": true,
  "eventType": "Download"
}`

const sonarrRenamePayload = `{
  "series": {"id": 1, "title": "Breaking Bad", "path": "/tv/Breaking Bad"},
  "renamedEpisodeFiles": [
    {
      "relativePath": "Season 05/S05E01.mkv",
      "path": "/tv/Breaking Bad/Season 05/S05E01.mkv",
      "previousRelativePath": "Season 05/breaking.bad.s05e01.mkv",
      "previousPath": "/tv/Breaking Bad/Season 05/breaking.bad.s05e01.mkv"
    }
  ],
  "eventType": "Rename"
}`

const sonarrDeletePayload = `{
  "series": {"id": 1, "title": "Breaking Bad", "path": "/tv/Breaking Bad"},
  "episodes": [{"seasonNumber": 5, "episodeNumber": 1}],
  "episodeFile": {
    "relativePath": "Season 05/S05E01.mkv",
    "path": "/tv/Breaking Bad/Season 05/S05E01.mkv"
  },
  "deleteReason": "Manual",
  "eventType": "EpisodeFileDelete"
}`

const sonarrGrabPayload = `{
  "series": {"id": 1, "title": "Breaking Bad"},
  "episodes": [{"seasonNumber": 5, "episodeNumber": 1}],
  "eventType": "Grab"
}`

const sonarrBatchImportPayload = `{
  "series": {"id": 1, "title": "Westworld", "path": "/tv/Westworld"},
  "episodeFiles": [
    {"relativePath": "Season 01/Westworld.S01E01.mkv", "path": "/tv/Westworld/Season 01/Westworld.S01E01.mkv"},
    {"relativePath": "Season 01/Westworld.S01E02.mkv", "path": "/tv/Westworld/Season 01/Westworld.S01E02.mkv"}
  ],
  "eventType": "Download"
}`

const sonarrSeriesDeletePayload = `{
  "series": {"id": 1, "title": "Westworld", "path": "/tv/Westworld"},
  "eventType": "SeriesDelete"
}`

func TestParseSonarrDownload(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrDownloadPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Source != "sonarr" || ev.Instance != "main" || ev.MediaType != "episode" {
		t.Errorf("unexpected event fields: %+v", ev)
	}
	if ev.Path != "/tv/Breaking Bad/Season 05/S05E01.mkv" {
		t.Errorf("unexpected path: %s", ev.Path)
	}
	if ev.IsUpgrade {
		t.Errorf("expected IsUpgrade=false")
	}
}

func TestParseSonarrUpgrade(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrUpgradePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || !events[0].IsUpgrade {
		t.Fatalf("expected a single upgrade event, got %+v", events)
	}
}

func TestParseSonarrRename(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrRenamePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both the new and previous locations must be scanned, so a rename
	// that moves an episode to a different season folder clears the old
	// folder's stale entry too.
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (new path + previous path)", len(events))
	}
	if events[0].Path != "/tv/Breaking Bad/Season 05/S05E01.mkv" {
		t.Errorf("expected renamed (new) path, got %s", events[0].Path)
	}
	if events[1].Path != "/tv/Breaking Bad/Season 05/breaking.bad.s05e01.mkv" {
		t.Errorf("expected previous path, got %s", events[1].Path)
	}
	for _, ev := range events {
		if ev.IsDir {
			t.Errorf("rename events should be file paths, not directories: %+v", ev)
		}
	}
}

func TestParseSonarrBatchImport(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrBatchImportPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Path != "/tv/Westworld/Season 01/Westworld.S01E01.mkv" {
		t.Errorf("unexpected path: %s", events[0].Path)
	}
	if events[1].Path != "/tv/Westworld/Season 01/Westworld.S01E02.mkv" {
		t.Errorf("unexpected path: %s", events[1].Path)
	}
}

func TestParseSonarrSeriesDelete(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrSeriesDeletePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Path != "/tv/Westworld" {
		t.Errorf("unexpected path: %s", events[0].Path)
	}
	if !events[0].IsDir {
		t.Error("expected SeriesDelete event to be marked IsDir")
	}
}

func TestParseSonarrDelete(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrDeletePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Path != "/tv/Breaking Bad/Season 05/S05E01.mkv" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestParseSonarrGrabIgnored(t *testing.T) {
	events, err := ParseSonarr("main", []byte(sonarrGrabPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected Grab event to produce no jobs, got %+v", events)
	}
}

func TestParseSonarrMalformed(t *testing.T) {
	_, err := ParseSonarr("main", []byte("{not json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
