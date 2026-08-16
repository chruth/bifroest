package webhook

import (
	"encoding/json"
	"fmt"

	"github.com/chruth/bifroest/internal/model"
)

// Sonarr webhook payload shapes. These mirror
// NzbDrone.Core.Notifications.Webhook.Webhook* in the Sonarr source
// (develop branch), serialized with camelCase property names and
// PascalCase enum values, e.g. {"eventType":"Download",...}. Cross-checked
// against two independent, production Sonarr/Radarr-to-Plex/Jellyfin
// bridges (dan-online/autopulse and Cloudbox/autoscan) to confirm field
// names and which paths actually need scanning.
//
// Only the fields this application actually needs are captured; Sonarr
// sends considerably more (images, quality info, custom formats, etc).
type sonarrPayload struct {
	EventType   string             `json:"eventType"`
	Series      *sonarrSeries      `json:"series"`
	EpisodeFile *sonarrEpisodeFile `json:"episodeFile"`
	// EpisodeFiles (plural) is used instead of EpisodeFile for Sonarr's
	// batch "On Import Complete" event, which shares eventType "Download"
	// with the single-file "On File Import"/"On Upgrade" event.
	EpisodeFiles        []sonarrEpisodeFile        `json:"episodeFiles"`
	IsUpgrade           bool                       `json:"isUpgrade"`
	RenamedEpisodeFiles []sonarrRenamedEpisodeFile `json:"renamedEpisodeFiles"`
}

type sonarrSeries struct {
	Path string `json:"path"`
}

type sonarrEpisodeFile struct {
	RelativePath string `json:"relativePath"`
	Path         string `json:"path"`
}

type sonarrRenamedEpisodeFile struct {
	sonarrEpisodeFile
	PreviousRelativePath string `json:"previousRelativePath"`
	PreviousPath         string `json:"previousPath"`
}

// ParseSonarr normalizes a Sonarr webhook body into zero or more
// MediaEvents. Event types unrelated to filesystem changes (Grab, SeriesAdd,
// Health, Test, ...) yield no events and no error.
func ParseSonarr(instance string, body []byte) ([]model.MediaEvent, error) {
	var p sonarrPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("malformed sonarr payload: %w", err)
	}

	switch p.EventType {
	case "Download":
		var events []model.MediaEvent
		if p.EpisodeFile != nil && p.EpisodeFile.Path != "" {
			events = append(events, sonarrFileEvent(instance, p.EventType, p.EpisodeFile.Path, p.IsUpgrade))
		}
		for _, f := range p.EpisodeFiles {
			if f.Path == "" {
				continue
			}
			events = append(events, sonarrFileEvent(instance, p.EventType, f.Path, p.IsUpgrade))
		}
		if len(events) == 0 {
			return nil, fmt.Errorf("sonarr %s event missing episodeFile(s).path", p.EventType)
		}
		return events, nil

	case "Rename":
		var events []model.MediaEvent
		for _, f := range p.RenamedEpisodeFiles {
			// The file's new location.
			if f.Path != "" {
				events = append(events, sonarrFileEvent(instance, p.EventType, f.Path, false))
			}
			// The file's old location: Plex/Jellyfin need to notice it's
			// gone too, e.g. when a rename moves an episode to a
			// different season folder.
			if f.PreviousPath != "" {
				events = append(events, sonarrFileEvent(instance, p.EventType, f.PreviousPath, false))
			}
		}
		return events, nil

	case "EpisodeFileDelete":
		if p.EpisodeFile == nil || p.EpisodeFile.Path == "" {
			return nil, fmt.Errorf("sonarr %s event missing episodeFile.path", p.EventType)
		}
		return []model.MediaEvent{sonarrFileEvent(instance, p.EventType, p.EpisodeFile.Path, false)}, nil

	case "SeriesDelete":
		// The whole series folder (and everything under it) is gone;
		// scan the folder itself rather than any one file within it.
		if p.Series == nil || p.Series.Path == "" {
			return nil, fmt.Errorf("sonarr %s event missing series.path", p.EventType)
		}
		return []model.MediaEvent{{
			Source:    "sonarr",
			Instance:  instance,
			EventType: p.EventType,
			MediaType: "episode",
			Path:      p.Series.Path,
			IsDir:     true,
		}}, nil

	default:
		return nil, nil
	}
}

func sonarrFileEvent(instance, eventType, path string, isUpgrade bool) model.MediaEvent {
	return model.MediaEvent{
		Source:    "sonarr",
		Instance:  instance,
		EventType: eventType,
		MediaType: "episode",
		IsUpgrade: isUpgrade,
		Path:      path,
	}
}
