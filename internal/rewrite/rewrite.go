// Package rewrite maps source-filesystem paths (as seen by Sonarr/Radarr)
// to target-filesystem paths (as seen by Plex/Jellyfin), and derives the
// directory that should actually be scanned.
package rewrite

import (
	"fmt"
	"path"
	"strings"
)

// Mapping is a single prefix-replacement rule.
type Mapping struct {
	From string
	To   string
}

// Apply rewrites p by replacing the first matching prefix. Mappings are
// tried in order; the first match wins.
//
// No mappings configured at all is treated as "this instance's paths are
// identical on both sides" and p is returned unchanged - useful when
// Sonarr/Radarr and Plex/Jellyfin are mounted at the exact same absolute
// path. But if mappings ARE configured and none of them match, that's a
// real misconfiguration: an error is returned rather than silently passing
// the path through unchanged.
func Apply(mappings []Mapping, p string) (string, error) {
	if len(mappings) == 0 {
		return p, nil
	}
	for _, m := range mappings {
		if strings.HasPrefix(p, m.From) {
			return m.To + strings.TrimPrefix(p, m.From), nil
		}
	}
	return "", fmt.Errorf("no path mapping matched %q", p)
}

// ScanPath returns the directory that should be targeted for a scan given
// the rewritten path to a media file. Plex/Jellyfin are asked to refresh
// the containing directory rather than the individual file or the whole
// library.
func ScanPath(rewrittenFilePath string) string {
	dir := path.Dir(rewrittenFilePath)
	if dir == "." || dir == "/" {
		return rewrittenFilePath
	}
	return dir
}
