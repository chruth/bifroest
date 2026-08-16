// Package model holds the small set of types shared across the application.
package model

import "time"

// MediaEvent is the normalized representation of a Sonarr/Radarr webhook
// event once it has been parsed into something the rest of the application
// understands.
type MediaEvent struct {
	Source    string // "sonarr" or "radarr"
	Instance  string
	EventType string // raw upstream event type, kept for logging
	MediaType string // "episode" or "movie"
	IsUpgrade bool
	Path      string // absolute source-filesystem path
	// IsDir indicates Path already names the directory that should be
	// scanned (e.g. a series/movie folder from a delete event), as opposed
	// to a file path whose containing directory must be derived.
	IsDir bool
}

// JobStatus is the state of a scan job in the queue.
type JobStatus string

const (
	StatusPending         JobStatus = "pending"
	StatusWaitingForMount JobStatus = "waiting_for_mount"
	StatusProcessing      JobStatus = "processing"
	StatusRetry           JobStatus = "retry"
	StatusCompleted       JobStatus = "completed"
	StatusFailed          JobStatus = "failed"
)

// Job is a persisted scan job. One job represents "send a targeted scan for
// this path to this target".
type Job struct {
	ID            int64
	Source        string
	Instance      string
	EventType     string
	MediaType     string
	Path          string // original source-filesystem path (for logging/debugging)
	Target        string // "plex" or "jellyfin"
	ScanPath      string // rewritten, target-filesystem path to scan
	Status        JobStatus
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastError     string
}
