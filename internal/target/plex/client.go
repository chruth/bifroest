// Package plex implements the Plex target: library discovery and targeted
// partial-scan refresh.
//
// API behavior verified against Plex Media Server's documented URL
// commands and the Plexopedia API reference (https://www.plexopedia.com):
//
//   - GET /library/sections            (Accept: application/json, X-Plex-Token header)
//     returns {"MediaContainer":{"Directory":[{"key","type","title","Location":[{"id","path"}]}]}}
//   - GET /library/sections/{key}/refresh?path={folder}
//     triggers a partial scan of just that folder (requires PMS >= 1.20.0.3125).
//     Authentication via X-Plex-Token header.
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Library struct {
	Key       string
	Type      string
	Title     string
	Locations []string
}

type sectionsResponse struct {
	MediaContainer struct {
		Directory []struct {
			Key      string `json:"key"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			Location []struct {
				ID   int    `json:"id"`
				Path string `json:"path"`
			} `json:"Location"`
		} `json:"Directory"`
	} `json:"MediaContainer"`
}

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	log        *slog.Logger

	mu        sync.RWMutex
	libraries []Library
}

func New(baseURL, token string, log *slog.Logger) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		log:        log,
	}
}

func (c *Client) Name() string { return "plex" }

// RefreshLibraries fetches the current set of libraries and their storage
// locations from Plex and caches them in memory.
func (c *Client) RefreshLibraries(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/library/sections", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: list libraries: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("plex: list libraries: unexpected status %d", resp.StatusCode)
	}

	var parsed sectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("plex: decode libraries: %w", err)
	}

	libraries := make([]Library, 0, len(parsed.MediaContainer.Directory))
	for _, d := range parsed.MediaContainer.Directory {
		lib := Library{Key: d.Key, Type: d.Type, Title: d.Title}
		for _, loc := range d.Location {
			lib.Locations = append(lib.Locations, loc.Path)
		}
		libraries = append(libraries, lib)
	}

	c.mu.Lock()
	c.libraries = libraries
	c.mu.Unlock()

	c.log.Info("plex libraries discovered", "count", len(libraries))
	return nil
}

// Scan triggers a targeted partial scan of path's Plex library.
func (c *Client) Scan(ctx context.Context, path string) error {
	lib, ok := c.matchLibrary(path)
	if !ok {
		// The cache might be stale (e.g. Plex was down at startup, or a
		// library was added since). Try once, synchronously, before giving
		// up.
		if err := c.RefreshLibraries(ctx); err != nil {
			return fmt.Errorf("plex: no library matched %q and refresh failed: %w", path, err)
		}
		lib, ok = c.matchLibrary(path)
		if !ok {
			return fmt.Errorf("plex: no library matches path %q", path)
		}
	}

	u := fmt.Sprintf("%s/library/sections/%s/refresh?path=%s", c.baseURL, url.PathEscape(lib.Key), url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plex-Token", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("plex: refresh request: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// matchLibrary finds the library whose location has the longest matching
// prefix with path.
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

// pathHasPrefix reports whether path is located under prefix, treating
// prefix as a directory boundary (so "/media/tv" matches "/media/tv/Show"
// but not "/media/tvshows").
func pathHasPrefix(path, prefix string) bool {
	prefix = strings.TrimRight(prefix, "/")
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}
