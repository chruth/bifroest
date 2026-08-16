package queue

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chruth/bifroest/internal/model"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func countJobs(t *testing.T, q *Queue, status model.JobStatus) int {
	t.Helper()
	var n int
	if err := q.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status = ?`, string(status)).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return n
}

func TestEnqueueScanInsertsPendingJob(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if n := countJobs(t, q, model.StatusPending); n != 1 {
		t.Errorf("got %d pending jobs, want 1", n)
	}
}

func TestEnqueueScanDeduplicates(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	params := EnqueueParams{
		Source: "sonarr", Instance: "main", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: time.Hour,
	}

	for _, eventType := range []string{"Download", "Rename"} {
		p := params
		p.EventType = eventType
		if err := q.EnqueueScan(ctx, p); err != nil {
			t.Fatalf("enqueue %s: %v", eventType, err)
		}
	}

	if n := countJobs(t, q, model.StatusPending); n != 1 {
		t.Errorf("got %d pending jobs after duplicate events, want 1 (deduplicated)", n)
	}
}

func TestEnqueueScanDoesNotDeduplicateCompletedJobs(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	params := EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: 0,
	}
	if err := q.EnqueueScan(ctx, params); err != nil {
		t.Fatal(err)
	}
	job, err := q.ClaimNext(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%v err=%v", job, err)
	}
	if err := q.MarkCompleted(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	// A later, legitimate scan for the same path should not be blocked
	// forever by the earlier completed job.
	if err := q.EnqueueScan(ctx, params); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, q, model.StatusPending); n != 1 {
		t.Errorf("got %d pending jobs, want 1 new job after completion", n)
	}
}

func TestClaimNextRespectsDelay(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := q.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if job != nil {
		t.Fatalf("expected no claimable job before delay elapses, got %+v", job)
	}
}

func TestClaimNextMarksProcessing(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: 0,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := q.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if job == nil {
		t.Fatal("expected a claimable job")
	}
	if job.Status != model.StatusProcessing {
		t.Errorf("got status %s, want processing", job.Status)
	}

	second, err := q.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatal("expected the already-claimed job not to be claimable again")
	}
}

// TestMarkRetryNeverGivesUp exercises the core resilience property this
// queue exists for: a target that's down for a long time (matching how
// mount outages are already handled) must never cause a job to be
// silently abandoned. There's no UI or API to requeue a failed job, so
// ordinary scan failures should keep retrying indefinitely rather than
// eventually landing in 'failed'.
func TestMarkRetryNeverGivesUp(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: 0,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := q.ClaimNext(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%v err=%v", job, err)
	}

	cause := errors.New("target unreachable")

	// Fail it many times, well past what the old max_retries=5 default
	// would have allowed.
	for attempt := 1; attempt <= 20; attempt++ {
		if err := q.MarkRetry(ctx, job.ID, attempt, time.Millisecond, cause); err != nil {
			t.Fatal(err)
		}
	}

	if n := countJobs(t, q, model.StatusRetry); n != 1 {
		t.Fatalf("got %d retry jobs after 20 failures, want 1 (still retrying, not failed)", n)
	}
	if n := countJobs(t, q, model.StatusFailed); n != 0 {
		t.Fatalf("got %d failed jobs, want 0 - target failures must never give up permanently", n)
	}
}

func TestMarkFailedIsPermanent(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: 0,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := q.ClaimNext(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%v err=%v", job, err)
	}

	if err := q.MarkFailed(ctx, job.ID, errors.New("target no longer configured")); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, q, model.StatusFailed); n != 1 {
		t.Fatalf("got %d failed jobs, want 1", n)
	}
}

func TestMountParkAndRelease(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: 0,
	}); err != nil {
		t.Fatal(err)
	}

	if err := q.ParkEligibleForMount(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, q, model.StatusWaitingForMount); n != 1 {
		t.Fatalf("got %d waiting_for_mount jobs, want 1", n)
	}

	job, err := q.ClaimNext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatal("waiting_for_mount jobs must not be claimable")
	}

	if err := q.ReleaseWaitingForMount(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, q, model.StatusPending); n != 1 {
		t.Fatalf("got %d pending jobs after release, want 1", n)
	}
}

func TestRecoverStuckJobsOnRestart(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	if err := q.EnqueueScan(ctx, EnqueueParams{
		Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
		Path: "/tv/Show/E1.mkv", Target: "plex", ScanPath: "/media/tv/Show", Delay: 0,
	}); err != nil {
		t.Fatal(err)
	}
	job, err := q.ClaimNext(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim: job=%v err=%v", job, err)
	}
	if n := countJobs(t, q, model.StatusProcessing); n != 1 {
		t.Fatalf("got %d processing jobs, want 1", n)
	}

	// Simulate a restart: RecoverStuckJobs should reset it so it gets
	// retried rather than lost forever.
	if err := q.RecoverStuckJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, q, model.StatusPending); n != 1 {
		t.Fatalf("got %d pending jobs after recovery, want 1", n)
	}

	recovered, err := q.ClaimNext(ctx)
	if err != nil || recovered == nil {
		t.Fatalf("expected recovered job to be claimable: job=%v err=%v", recovered, err)
	}
}

func TestInsertFailedForUnmappablePath(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	err := q.InsertFailed(ctx, "sonarr", "main", "Download", "episode", "/unmapped/x.mkv", "", errors.New("no mapping"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, q, model.StatusFailed); n != 1 {
		t.Fatalf("got %d failed jobs, want 1", n)
	}
}
