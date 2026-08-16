package jellyfin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

const virtualFoldersJSON = `[
  {"Name": "TV Shows", "ItemId": "1", "CollectionType": "tvshows", "Locations": ["/media/tv"]},
  {"Name": "Movies", "ItemId": "2", "CollectionType": "movies", "Locations": ["/media/movies"]}
]`

func newTestClient(t *testing.T, updateHandler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Library/VirtualFolders", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, virtualFoldersJSON)
	})
	mux.HandleFunc("/Library/Media/Updated", func(w http.ResponseWriter, r *http.Request) {
		if updateHandler != nil {
			updateHandler(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return New(srv.URL, "test-key", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestJellyfinRefreshLibrariesAndMatch(t *testing.T) {
	c := newTestClient(t, nil)
	if err := c.RefreshLibraries(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	lib, ok := c.matchLibrary("/media/tv/Breaking Bad/Season 05")
	if !ok || lib.Name != "TV Shows" {
		t.Errorf("expected TV Shows match, got %+v ok=%v", lib, ok)
	}

	lib, ok = c.matchLibrary("/media/movies/Inception (2010)")
	if !ok || lib.Name != "Movies" {
		t.Errorf("expected Movies match, got %+v ok=%v", lib, ok)
	}
}

func TestJellyfinScanSendsMediaUpdate(t *testing.T) {
	var body mediaUpdatedRequest
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.RefreshLibraries(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	path := "/media/tv/Breaking Bad/Season 05"
	if err := c.Scan(context.Background(), path); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if len(body.Updates) != 1 || body.Updates[0].Path != path {
		t.Errorf("unexpected update body: %+v", body)
	}
}

func TestJellyfinScanUnmatchedPathErrors(t *testing.T) {
	c := newTestClient(t, nil)
	if err := c.RefreshLibraries(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if err := c.Scan(context.Background(), "/media/music/unknown.flac"); err == nil {
		t.Fatal("expected an error for a path with no matching library")
	}
}
