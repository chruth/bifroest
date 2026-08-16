package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chruth/bifroest/internal/model"
	"github.com/chruth/bifroest/internal/target"
)

// WorkerPool processes jobs from the queue, respecting mount availability
// and per-target retry/backoff.
type WorkerPool struct {
	queue       *Queue
	targets     map[string]target.Target
	mount       interface{ Available() bool }
	scanTimeout time.Duration
	log         *slog.Logger
}

func NewWorkerPool(q *Queue, targets map[string]target.Target, mount interface{ Available() bool }, log *slog.Logger) *WorkerPool {
	return &WorkerPool{
		queue:       q,
		targets:     targets,
		mount:       mount,
		scanTimeout: 30 * time.Second,
		log:         log,
	}
}

// Run starts n worker goroutines and blocks until ctx is cancelled and they
// have all exited.
func (p *WorkerPool) Run(ctx context.Context, n int, pollInterval time.Duration) {
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		id := i
		go func() {
			p.loop(ctx, id, pollInterval)
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

func (p *WorkerPool) loop(ctx context.Context, id int, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx, id)
		}
	}
}

func (p *WorkerPool) tick(ctx context.Context, workerID int) {
	if !p.mount.Available() {
		if err := p.queue.ParkEligibleForMount(ctx); err != nil {
			p.log.Error("failed parking jobs for mount outage", "error", err)
		}
		return
	}

	if err := p.queue.ReleaseWaitingForMount(ctx); err != nil {
		p.log.Error("failed releasing jobs waiting for mount", "error", err)
		return
	}

	job, err := p.queue.ClaimNext(ctx)
	if err != nil {
		p.log.Error("failed claiming job", "error", err)
		return
	}
	if job == nil {
		return
	}

	p.process(ctx, workerID, job)
}

func (p *WorkerPool) process(ctx context.Context, workerID int, job *model.Job) {
	log := p.log.With("worker", workerID, "target", job.Target, "path", job.ScanPath, "job_id", job.ID)

	t, ok := p.targets[job.Target]
	if !ok {
		// The target was removed from configuration since this job was
		// created. Only a config change (and restart) can fix that, so
		// this is the one case that gives up rather than retrying forever.
		err := fmt.Errorf("target %q is not configured", job.Target)
		log.Error("scan failed permanently: target no longer configured", "error", err)
		if mErr := p.queue.MarkFailed(ctx, job.ID, err); mErr != nil {
			log.Error("failed marking job failed", "error", mErr)
		}
		return
	}

	// Deliberately not derived from ctx: once shutdown begins we stop
	// claiming new jobs, but a scan already in flight should be allowed to
	// finish rather than being aborted mid-request.
	scanCtx, cancel := context.WithTimeout(context.Background(), p.scanTimeout)
	defer cancel()

	err := t.Scan(scanCtx, job.ScanPath)
	if err == nil {
		log.Info("scan completed")
		if err := p.queue.MarkCompleted(ctx, job.ID); err != nil {
			log.Error("failed marking job completed", "error", err)
		}
		return
	}

	// Retried indefinitely with capped backoff - see MarkRetry.
	attempts := job.Attempts + 1
	delay := Backoff(attempts)
	log.Warn("scan failed, will retry", "attempt", attempts, "next_attempt_in", delay, "error", err)
	if mErr := p.queue.MarkRetry(ctx, job.ID, attempts, delay, err); mErr != nil {
		log.Error("failed recording scan failure", "error", mErr)
	}
}
