package webhook

import "testing"

const radarrDownloadPayload = `{
  "movie": {"id": 1, "title": "Inception", "folderPath": "/movies/Inception (2010)"},
  "movieFile": {
    "relativePath": "Inception.mkv",
    "path": "/movies/Inception (2010)/Inception.mkv",
    "quality": "Bluray-1080p"
  },
  "isUpgrade": false,
  "eventType": "Download"
}`

const radarrRenamePayload = `{
  "movie": {"id": 1, "title": "Inception", "folderPath": "/movies/Inception (2010)"},
  "renamedMovieFiles": [
    {
      "relativePath": "Inception.mkv",
      "path": "/movies/Inception (2010)/Inception.mkv",
      "previousRelativePath": "inception.2010.mkv",
      "previousPath": "/movies/Inception (2010)/inception.2010.mkv"
    }
  ],
  "eventType": "Rename"
}`

const radarrMovieDeletePayload = `{
  "movie": {"id": 1, "title": "Inception", "folderPath": "/movies/Inception (2010)"},
  "deletedFiles": true,
  "eventType": "MovieDelete"
}`

const radarrDeletePayload = `{
  "movie": {"id": 1, "title": "Inception"},
  "movieFile": {
    "relativePath": "Inception.mkv",
    "path": "/movies/Inception (2010)/Inception.mkv"
  },
  "deleteReason": "Manual",
  "eventType": "MovieFileDelete"
}`

const radarrMovieAddedPayload = `{
  "movie": {"id": 1, "title": "Inception"},
  "eventType": "MovieAdded"
}`

func TestParseRadarrDownload(t *testing.T) {
	events, err := ParseRadarr("main", []byte(radarrDownloadPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Source != "radarr" || ev.MediaType != "movie" {
		t.Errorf("unexpected event fields: %+v", ev)
	}
	if ev.Path != "/movies/Inception (2010)/Inception.mkv" {
		t.Errorf("unexpected path: %s", ev.Path)
	}
}

func TestParseRadarrRename(t *testing.T) {
	events, err := ParseRadarr("main", []byte(radarrRenamePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Rename uses movie.folderPath directly (not movieFile paths): a movie
	// lives in a single folder, so the folder is already the correct,
	// precise scan target and doesn't depend on renamedMovieFiles being
	// present.
	if len(events) != 1 || events[0].Path != "/movies/Inception (2010)" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if !events[0].IsDir {
		t.Error("expected Rename event to be marked IsDir")
	}
}

func TestParseRadarrMovieDelete(t *testing.T) {
	events, err := ParseRadarr("main", []byte(radarrMovieDeletePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Path != "/movies/Inception (2010)" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if !events[0].IsDir {
		t.Error("expected MovieDelete event to be marked IsDir")
	}
}

func TestParseRadarrDelete(t *testing.T) {
	events, err := ParseRadarr("main", []byte(radarrDeletePayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Path != "/movies/Inception (2010)/Inception.mkv" {
		t.Fatalf("unexpected events: %+v", events)
	}
	if events[0].MediaType != "movie" {
		t.Errorf("expected media type movie, got %s", events[0].MediaType)
	}
}

func TestParseRadarrMovieAddedIgnored(t *testing.T) {
	events, err := ParseRadarr("main", []byte(radarrMovieAddedPayload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected MovieAdded event to produce no jobs, got %+v", events)
	}
}
