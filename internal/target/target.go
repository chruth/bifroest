// Package target defines the abstraction that Plex and Jellyfin clients
// implement.
package target

import "context"

// Target triggers a targeted library scan/refresh for a path on some
// external media server.
type Target interface {
	// Name identifies the target for logging and job records, e.g. "plex".
	Name() string
	// Scan requests a targeted refresh of the directory at path.
	Scan(ctx context.Context, path string) error
}
