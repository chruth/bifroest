// Package queue implements a small SQLite-backed persistent job queue for
// scan jobs, including claiming, retry/backoff, deduplication, and
// mount-aware waiting.
package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chruth/bifroest/internal/model"
)

type Queue struct {
	db *sql.DB
}

func New(db *sql.DB) *Queue {
	return &Queue{db: db}
}

// EnqueueParams describes a scan that should happen for one target.
type EnqueueParams struct {
	Source    string
	Instance  string
	EventType string
	MediaType string
	Path      string
	Target    string
	ScanPath  string
	Delay     time.Duration
}

// EnqueueScan inserts a new job, or, if a pending/waiting/retry job already
// exists for the same target and scan path, merges into it instead of
// creating a duplicate. This is the deduplication behavior described in the
// spec: repeated Download/Upgrade/Rename events for the same file within a
// short window should not produce multiple scans.
func (q *Queue) EnqueueScan(ctx context.Context, p EnqueueParams) error {
	now := time.Now().UTC()
	nextAttempt := now.Add(p.Delay)

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM jobs
		WHERE target = ? AND scan_path = ? AND status IN ('pending', 'waiting_for_mount', 'retry')
		LIMIT 1`, p.Target, p.ScanPath).Scan(&existingID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO jobs (source, instance, event_type, media_type, path, target, scan_path, status, attempts, next_attempt_at, created_at, updated_at, last_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?, '')`,
			p.Source, p.Instance, p.EventType, p.MediaType, p.Path, p.Target, p.ScanPath, nextAttempt, now, now)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// Merge: refresh the pending job's event metadata and give it a
		// fresh delay window rather than scanning multiple times.
		_, err = tx.ExecContext(ctx, `
			UPDATE jobs SET event_type = ?, media_type = ?, path = ?, status = 'pending', next_attempt_at = ?, updated_at = ?
			WHERE id = ?`,
			p.EventType, p.MediaType, p.Path, nextAttempt, now, existingID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ReleaseWaitingForMount moves every job parked for a mount outage back to
// pending, eligible for immediate processing.
func (q *Queue) ReleaseWaitingForMount(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'pending', updated_at = ?
		WHERE status = 'waiting_for_mount'`, time.Now().UTC())
	return err
}

// ParkEligibleForMount moves jobs that are due for an attempt into
// waiting_for_mount. Doing this explicitly (rather than just leaving them
// pending) makes the mount outage visible in job state and guarantees mount
// downtime never consumes a normal retry attempt.
func (q *Queue) ParkEligibleForMount(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'waiting_for_mount', updated_at = ?
		WHERE status IN ('pending', 'retry') AND next_attempt_at <= ?`,
		time.Now().UTC(), time.Now().UTC())
	return err
}

// RecoverStuckJobs resets any job left in 'processing' by a previous,
// presumably crashed, run back to pending so it gets retried.
func (q *Queue) RecoverStuckJobs(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'pending', updated_at = ?
		WHERE status = 'processing'`, time.Now().UTC())
	return err
}

// ClaimNext atomically claims the next eligible job, if any, marking it
// processing. BEGIN IMMEDIATE takes SQLite's write lock up front so
// concurrent workers can never claim the same row.
func (q *Queue) ClaimNext(ctx context.Context) (*model.Job, error) {
	conn, err := q.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	now := time.Now().UTC()
	row := conn.QueryRowContext(ctx, `
		SELECT id, source, instance, event_type, media_type, path, target, scan_path, status, attempts, next_attempt_at, created_at, updated_at, last_error
		FROM jobs
		WHERE status IN ('pending', 'retry') AND next_attempt_at <= ?
		ORDER BY next_attempt_at ASC
		LIMIT 1`, now)

	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		if _, cErr := conn.ExecContext(ctx, "COMMIT"); cErr != nil {
			return nil, cErr
		}
		committed = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if _, err := conn.ExecContext(ctx, `UPDATE jobs SET status = 'processing', updated_at = ? WHERE id = ?`, now, job.ID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	committed = true

	job.Status = model.StatusProcessing
	return job, nil
}

func scanJob(row *sql.Row) (*model.Job, error) {
	var j model.Job
	var status string
	err := row.Scan(&j.ID, &j.Source, &j.Instance, &j.EventType, &j.MediaType, &j.Path, &j.Target, &j.ScanPath,
		&status, &j.Attempts, &j.NextAttemptAt, &j.CreatedAt, &j.UpdatedAt, &j.LastError)
	if err != nil {
		return nil, err
	}
	j.Status = model.JobStatus(status)
	return &j, nil
}

// MarkCompleted marks a job successfully finished. Completed jobs are never
// retried.
func (q *Queue) MarkCompleted(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `UPDATE jobs SET status = 'completed', updated_at = ?, last_error = '' WHERE id = ?`,
		time.Now().UTC(), id)
	return err
}

// MarkRetry records a failed scan attempt and schedules the next one after
// delay. Ordinary target failures (Plex/Jellyfin unreachable, HTTP errors,
// timeouts) are retried indefinitely rather than eventually being marked
// failed: the job is already durable in SQLite specifically so a long
// outage doesn't lose it, and there's no UI or API to requeue a failed job
// short of editing the database directly, so giving up permanently would
// mean silently stranding it. Backoff (see Backoff) naturally caps at its
// longest step, so this settles into a slow, low-noise retry cadence
// rather than hammering a dead target.
func (q *Queue) MarkRetry(ctx context.Context, id int64, attempts int, delay time.Duration, cause error) error {
	now := time.Now().UTC()
	errText := ""
	if cause != nil {
		errText = cause.Error()
	}

	_, err := q.db.ExecContext(ctx, `UPDATE jobs SET status = 'retry', attempts = ?, next_attempt_at = ?, updated_at = ?, last_error = ? WHERE id = ?`,
		attempts, now.Add(delay), now, errText, id)
	return err
}

// MarkFailed marks a job permanently failed. Reserved for conditions
// retrying can never fix on its own — currently just a job whose target has
// been removed from configuration since the job was created.
func (q *Queue) MarkFailed(ctx context.Context, id int64, cause error) error {
	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	_, err := q.db.ExecContext(ctx, `UPDATE jobs SET status = 'failed', updated_at = ?, last_error = ? WHERE id = ?`,
		time.Now().UTC(), errText, id)
	return err
}

// InsertFailed persists a job that failed before it could even be
// scheduled, e.g. because the source path did not match any configured
// path mapping. This keeps a record of the event for observability without
// pretending a scan was ever attempted.
func (q *Queue) InsertFailed(ctx context.Context, source, instance, eventType, mediaType, path, target string, cause error) error {
	now := time.Now().UTC()
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO jobs (source, instance, event_type, media_type, path, target, scan_path, status, attempts, next_attempt_at, created_at, updated_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, '', 'failed', 0, ?, ?, ?, ?)`,
		source, instance, eventType, mediaType, path, target, now, now, now, fmt.Sprintf("path mapping failed: %v", cause))
	return err
}
