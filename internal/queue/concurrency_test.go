package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentClaimNeverDoublesClaims exercises the real safety property
// the spec asks for: many goroutines racing to claim jobs must never end up
// processing the same job twice.
func TestConcurrentClaimNeverDoublesClaims(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	const jobCount = 50
	for i := 0; i < jobCount; i++ {
		if err := q.EnqueueScan(ctx, EnqueueParams{
			Source: "sonarr", Instance: "main", EventType: "Download", MediaType: "episode",
			Path: fmt.Sprintf("/tv/Show/E%d.mkv", i), Target: "plex",
			ScanPath: fmt.Sprintf("/media/tv/Show/E%d", i), Delay: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu     sync.Mutex
		seen   = map[int64]int{}
		wg     sync.WaitGroup
		claims int
	)

	const workers = 10
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				job, err := q.ClaimNext(ctx)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if job == nil {
					return
				}
				mu.Lock()
				seen[job.ID]++
				claims++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if claims != jobCount {
		t.Fatalf("got %d total claims, want %d", claims, jobCount)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("job %d was claimed %d times, want 1", id, count)
		}
	}
}
