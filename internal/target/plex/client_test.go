package plex

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

const sectionsJSON = `{
  "MediaContainer": {
    "size": 2,
    "Directory": [
      {"key": "1", "type": "show", "title": "TV Shows", "Location": [{"id": 1, "path": "/media/tv"}]},
      {"key": "2", "type": "movie", "title": "Movies", "Location": [{"id": 2, "path": "/media/movies"}]}
    ]
  }
}`

func newTestClient(t *testing.T, refreshHandler func(w http.ResponseWriter, r *http.Request)) (*Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, sectionsJSON)
	})
	mux.HandleFunc("/library/sections/", func(w http.ResponseWriter, r *http.Request) {
		if refreshHandler != nil {
			refreshHandler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "test-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	return c, srv
}

func TestRefreshLibrariesAndMatch(t *testing.T) {
	c, _ := newTestClient(t, nil)
	if err := c.RefreshLibraries(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	lib, ok := c.matchLibrary("/media/tv/Breaking Bad/Season 05")
	if !ok {
		t.Fatal("expected a matching library")
	}
	if lib.Title != "TV Shows" {
		t.Errorf("got library %q, want TV Shows", lib.Title)
	}

	lib, ok = c.matchLibrary("/media/movies/Inception (2010)")
	if !ok || lib.Title != "Movies" {
		t.Errorf("expected Movies library, got %+v ok=%v", lib, ok)
	}

	if _, ok := c.matchLibrary("/media/music/Some Album"); ok {
		t.Error("expected no match for unrelated path")
	}
}

func TestScanSendsPathToCorrectLibrary(t *testing.T) {
	var gotPath string
	var gotSection string

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// path form: /library/sections/{key}/refresh
		gotSection = r.URL.Path
		gotPath = r.URL.Query().Get("path")
		if r.Header.Get("X-Plex-Token") != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	if err := c.RefreshLibraries(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	path := "/media/tv/Breaking Bad/Season 05"
	if err := c.Scan(context.Background(), path); err != nil {
		t.Fatalf("scan: %v", err)
	}

	decodedPath, err := url.QueryUnescape(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	if decodedPath != path {
		t.Errorf("got scanned path %q, want %q", decodedPath, path)
	}
	if gotSection != "/library/sections/1/refresh" {
		t.Errorf("got section path %q, want /library/sections/1/refresh (TV Shows)", gotSection)
	}
}

func TestScanUnmatchedPathErrors(t *testing.T) {
	c, _ := newTestClient(t, nil)
	if err := c.RefreshLibraries(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	err := c.Scan(context.Background(), "/media/music/unknown.flac")
	if err == nil {
		t.Fatal("expected an error for a path with no matching library")
	}
}
