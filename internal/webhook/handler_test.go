package webhook

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chruth/bifroest/internal/config"
	"github.com/chruth/bifroest/internal/queue"
)

func testConfig() *config.Config {
	return &config.Config{
		Queue: config.QueueConfig{Delay: 0},
		Sources: config.SourcesConfig{
			Sonarr: map[string]config.SourceInstance{
				"main":  {Token: "main-token", PathMaps: []config.PathMapping{{From: "/tv/", To: "/media/tv/"}}},
				"anime": {Token: "anime-token", PathMaps: []config.PathMapping{{From: "/anime/", To: "/media/anime/"}}},
				// No path_maps: Sonarr and Plex/Jellyfin see this
				// instance's files at the exact same path.
				"samepath": {Token: "samepath-token"},
			},
			Radarr: map[string]config.SourceInstance{
				"main": {Token: "radarr-token", PathMaps: []config.PathMapping{{From: "/movies/", To: "/media/movies/"}}},
			},
		},
	}
}

func newTestHandler(t *testing.T) (http.Handler, *queue.Queue) {
	t.Helper()
	h, q, _ := newTestHandlerWithLog(t)
	return h, q
}

// newTestHandlerWithLog is like newTestHandler but also returns the log
// buffer, for tests that need to inspect what got logged.
func newTestHandlerWithLog(t *testing.T) (http.Handler, *queue.Queue, *bytes.Buffer) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := queue.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	q := queue.New(db)
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	h := New(testConfig(), q, []string{"plex", "jellyfin"}, log)
	return h, q, &logBuf
}

func doWebhook(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookUnknownSource(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doWebhook(h, "POST", "/webhook/plex/main", "main-token", sonarrDownloadPayload)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404", rec.Code)
	}
}

func TestWebhookUnknownInstance(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doWebhook(h, "POST", "/webhook/sonarr/nope", "main-token", sonarrDownloadPayload)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404", rec.Code)
	}
}

func TestWebhookMissingAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doWebhook(h, "POST", "/webhook/sonarr/main", "", sonarrDownloadPayload)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", rec.Code)
	}
}

func TestWebhookInvalidAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doWebhook(h, "POST", "/webhook/sonarr/main", "wrong-token", sonarrDownloadPayload)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", rec.Code)
	}
}

func TestWebhookMalformedJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := doWebhook(h, "POST", "/webhook/sonarr/main", "main-token", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
}

func TestWebhookMultipleInstancesUseDistinctAuthAndMapping(t *testing.T) {
	h, _ := newTestHandler(t)

	// main's token must not authenticate anime's endpoint.
	rec := doWebhook(h, "POST", "/webhook/sonarr/anime", "main-token", sonarrDownloadPayload)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cross-instance token should be rejected, got status %d", rec.Code)
	}

	rec = doWebhook(h, "POST", "/webhook/sonarr/main", "main-token", sonarrDownloadPayload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want 202: %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookAcceptedAndEnqueues(t *testing.T) {
	h, q := newTestHandler(t)

	rec := doWebhook(h, "POST", "/webhook/sonarr/main", "main-token", sonarrDownloadPayload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want 202: %s", rec.Code, rec.Body.String())
	}

	job, err := q.ClaimNext(t.Context())
	if err != nil {
		t.Fatalf("claim next: %v", err)
	}
	if job == nil {
		t.Fatal("expected a job to have been enqueued")
	}
	if job.ScanPath != "/media/tv/Breaking Bad/Season 05" {
		t.Errorf("unexpected scan path: %s", job.ScanPath)
	}
}

func TestWebhookDoesNotLogIsUpgrade(t *testing.T) {
	h, _, logBuf := newTestHandlerWithLog(t)

	// is_upgrade was noise: it's a plain bool Sonarr/Radarr only populate
	// for Download events, so it sat at "false" for everything else,
	// misleadingly implying bifroest had determined those weren't
	// upgrades when the concept doesn't apply there at all. Not worth
	// logging even for Download - dropped entirely.
	rec := doWebhook(h, "POST", "/webhook/sonarr/main", "main-token", sonarrDownloadPayload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(logBuf.String(), "is_upgrade") {
		t.Errorf("expected no is_upgrade in the log, got: %s", logBuf.String())
	}
}

func TestWebhookNoPathMapsPassesPathThrough(t *testing.T) {
	h, q := newTestHandler(t)

	body := `{
		"series": {"path": "/media/tv/Breaking Bad"},
		"episodeFile": {"relativePath": "Season 05/S05E01.mkv", "path": "/media/tv/Breaking Bad/Season 05/S05E01.mkv"},
		"eventType": "Download"
	}`
	rec := doWebhook(h, "POST", "/webhook/sonarr/samepath", "samepath-token", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want 202: %s", rec.Code, rec.Body.String())
	}

	job, err := q.ClaimNext(t.Context())
	if err != nil {
		t.Fatalf("claim next: %v", err)
	}
	if job == nil {
		t.Fatal("expected a job to have been enqueued")
	}
	if job.ScanPath != "/media/tv/Breaking Bad/Season 05" {
		t.Errorf("unexpected scan path: %s", job.ScanPath)
	}
}

func TestWebhookUnmappablePathRecordsFailureNotScan(t *testing.T) {
	h, q := newTestHandler(t)

	body := `{"movie":{"id":1,"title":"X"},"movieFile":{"relativePath":"x.mkv","path":"/unmapped/x.mkv"},"eventType":"Download"}`
	rec := doWebhook(h, "POST", "/webhook/radarr/main", "radarr-token", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want 202: %s", rec.Code, rec.Body.String())
	}

	job, err := q.ClaimNext(t.Context())
	if err != nil {
		t.Fatalf("claim next: %v", err)
	}
	if job != nil {
		t.Fatalf("expected no claimable job for an unmapped path, got %+v", job)
	}
}
