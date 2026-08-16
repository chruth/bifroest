package queue

import "time"

// defaultBackoff mirrors the schedule from the spec: attempt 1 waits 5s,
// attempt 2 waits 15s, and so on. Attempts beyond the table reuse the last
// entry.
var defaultBackoff = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
}

// Backoff returns the delay to use before the given attempt number
// (1-indexed: the delay applied after the first failed attempt).
func Backoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	if attempt > len(defaultBackoff) {
		return defaultBackoff[len(defaultBackoff)-1]
	}
	return defaultBackoff[attempt-1]
}
