package webhook

import (
	"encoding/json"
	"fmt"

	"github.com/chruth/bifroest/internal/model"
)

// Radarr webhook payload shapes, mirroring
// NzbDrone.Core.Notifications.Webhook.Webhook* in the Radarr source
// (develop branch). Radarr's payload shape closely parallels Sonarr's,
// with movie/movieFile in place of series/episodeFile.
//
// Unlike Sonarr, Rename events here are handled via movie.folderPath
// rather than any per-file rename list: two independent, production
// Sonarr/Radarr-to-Plex/Jellyfin bridges (dan-online/autopulse and
// Cloudbox/autoscan) both do this rather than relying on
// renamedMovieFiles, and since a movie (unlike a series) normally lives
// entirely in one folder, folderPath is already the correct, precise scan
// target.
type radarrPayload struct {
	EventType string           `json:"eventType"`
	Movie     *radarrMovie     `json:"movie"`
	MovieFile *radarrMovieFile `json:"movieFile"`
	IsUpgrade bool             `json:"isUpgrade"`
}

type radarrMovie struct {
	FolderPath string `json:"folderPath"`
}

type radarrMovieFile struct {
	RelativePath string `json:"relativePath"`
	Path         string `json:"path"`
}

// ParseRadarr normalizes a Radarr webhook body into zero or more
// MediaEvents. Event types unrelated to filesystem changes (Grab,
// MovieAdded, Health, Test, ...) yield no events and no error.
func ParseRadarr(instance string, body []byte) ([]model.MediaEvent, error) {
	var p radarrPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("malformed radarr payload: %w", err)
	}

	switch p.EventType {
	case "Download":
		if p.MovieFile == nil || p.MovieFile.Path == "" {
			return nil, fmt.Errorf("radarr %s event missing movieFile.path", p.EventType)
		}
		return []model.MediaEvent{{
			Source:    "radarr",
			Instance:  instance,
			EventType: p.EventType,
			MediaType: "movie",
			IsUpgrade: p.IsUpgrade,
			Path:      p.MovieFile.Path,
		}}, nil

	case "Rename", "MovieDelete":
		// Rename: the file itself may or may not have moved folders, but
		// movie.folderPath is always the directory that needs rescanning.
		// MovieDelete: the whole movie folder (and everything under it)
		// may be gone; scan the folder itself.
		if p.Movie == nil || p.Movie.FolderPath == "" {
			return nil, fmt.Errorf("radarr %s event missing movie.folderPath", p.EventType)
		}
		return []model.MediaEvent{{
			Source:    "radarr",
			Instance:  instance,
			EventType: p.EventType,
			MediaType: "movie",
			Path:      p.Movie.FolderPath,
			IsDir:     true,
		}}, nil

	case "MovieFileDelete":
		if p.MovieFile == nil || p.MovieFile.Path == "" {
			return nil, fmt.Errorf("radarr %s event missing movieFile.path", p.EventType)
		}
		return []model.MediaEvent{{
			Source:    "radarr",
			Instance:  instance,
			EventType: p.EventType,
			MediaType: "movie",
			Path:      p.MovieFile.Path,
		}}, nil

	default:
		return nil, nil
	}
}
