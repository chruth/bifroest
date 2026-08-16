// Package jellyfin implements the Jellyfin target: library discovery and
// targeted refresh via the media-updated notification API.
//
// API behavior:
//
//   - GET /Library/VirtualFolders (X-Emby-Token header)
//     returns the configured libraries, each with a Locations array of
//     filesystem paths. Used here purely to determine which library a
//     rewritten path belongs to, for logging/validation.
//   - POST /Library/Media/Updated (X-Emby-Token header)
//     body: {"Updates":[{"Path":"<path>","UpdateType":"Modified"}]}
//     This is the mechanism Jellyfin itself exposes for external file
//     watchers to report filesystem changes without a full library scan;
//     Jellyfin resolves the path to the owning library internally.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Library struct {
	ItemID         string
	Name           string
	CollectionType string
	Locations      []string
}

type virtualFolder struct {
	Name           string   `json:"Name"`
	ItemID         string   `json:"ItemId"`
	CollectionType string   `json:"CollectionType"`
	Locations      []string `json:"Locations"`
}

type mediaUpdate struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"`
}

type mediaUpdatedRequest struct {
	Updates []mediaUpdate `json:"Updates"`
}

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	log        *slog.Logger

	mu        sync.RWMutex
	libraries []Library
}

func New(baseURL, apiKey string, log *slog.Logger) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		log:        log,
	}
}

func (c *Client) Name() string { return "jellyfin" }

// RefreshLibraries fetches the current set of libraries and caches them in
// memory.
func (c *Client) RefreshLibraries(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/Library/VirtualFolders", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin: list libraries: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jellyfin: list libraries: unexpected status %d", resp.StatusCode)
	}

	var folders []virtualFolder
	if err := json.NewDecoder(resp.Body).Decode(&folders); err != nil {
		return fmt.Errorf("jellyfin: decode libraries: %w", err)
	}

	libraries := make([]Library, 0, len(folders))
	for _, f := range folders {
		libraries = append(libraries, Library{
			ItemID:         f.ItemID,
			Name:           f.Name,
			CollectionType: f.CollectionType,
			Locations:      f.Locations,
		})
	}

	c.mu.Lock()
	c.libraries = libraries
	c.mu.Unlock()

	c.log.Info("jellyfin libraries discovered", "count", len(libraries))
	return nil
}

// Scan notifies Jellyfin that path changed on disk, triggering a targeted
// refresh of the owning library.
func (c *Client) Scan(ctx context.Context, path string) error {
	if _, ok := c.matchLibrary(path); !ok {
		if err := c.RefreshLibraries(ctx); err != nil {
			return fmt.Errorf("jellyfin: no library matched %q and refresh failed: %w", path, err)
		}
		if _, ok := c.matchLibrary(path); !ok {
			return fmt.Errorf("jellyfin: no library matches path %q", path)
		}
	}

	// "Modified" matches the convention used by both dan-online/autopulse
	// and Cloudbox/autoscan's dedicated Jellyfin targets (their Emby
	// target, a distinct and older codebase, uses "Created" instead).
	body, err := json.Marshal(mediaUpdatedRequest{Updates: []mediaUpdate{{Path: path, UpdateType: "Modified"}}})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/Library/Media/Updated", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin: media updated request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("jellyfin: media updated request: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) matchLibrary(path string) (Library, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var best Library
	bestLen := -1
	for _, lib := range c.libraries {
		for _, loc := range lib.Locations {
			if pathHasPrefix(path, loc) && len(loc) > bestLen {
				best = lib
				bestLen = len(loc)
			}
		}
	}
	return best, bestLen >= 0
}

func pathHasPrefix(path, prefix string) bool {
	prefix = strings.TrimRight(prefix, "/")
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}
