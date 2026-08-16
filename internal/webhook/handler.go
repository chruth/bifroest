// Package webhook receives and authenticates Sonarr/Radarr webhooks,
// normalizes them into MediaEvents, applies path mapping, and enqueues scan
// jobs. It never talks to Plex/Jellyfin directly.
package webhook

import (
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chruth/bifroest/internal/config"
	"github.com/chruth/bifroest/internal/model"
	"github.com/chruth/bifroest/internal/queue"
	"github.com/chruth/bifroest/internal/rewrite"
)

// maxBodyBytes bounds webhook request bodies. Sonarr/Radarr payloads are
// normally a few KB; this is generous headroom without being unbounded.
const maxBodyBytes = 5 << 20 // 5 MiB

// instanceAuth holds the per-instance token and path mapping needed to
// authenticate and rewrite paths for one configured Sonarr/Radarr instance.
type instanceAuth struct {
	token    string
	mappings []rewrite.Mapping
}

type handler struct {
	sonarr  map[string]instanceAuth
	radarr  map[string]instanceAuth
	queue   *queue.Queue
	targets []string
	delay   time.Duration
	log     *slog.Logger
}

// New builds the webhook HTTP handler from configuration.
func New(cfg *config.Config, q *queue.Queue, enabledTargets []string, log *slog.Logger) http.Handler {
	h := &handler{
		sonarr:  buildInstances(cfg.Sources.Sonarr),
		radarr:  buildInstances(cfg.Sources.Radarr),
		queue:   q,
		targets: enabledTargets,
		delay:   cfg.Queue.Delay,
		log:     log,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/{source}/{instance}", h.handleWebhook)
	return mux
}

func buildInstances(instances map[string]config.SourceInstance) map[string]instanceAuth {
	out := make(map[string]instanceAuth, len(instances))
	for name, inst := range instances {
		mappings := make([]rewrite.Mapping, 0, len(inst.PathMaps))
		for _, m := range inst.PathMaps {
			mappings = append(mappings, rewrite.Mapping{From: m.From, To: m.To})
		}
		out[name] = instanceAuth{token: inst.Token, mappings: mappings}
	}
	return out
}

func (h *handler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	instance := r.PathValue("instance")

	var instances map[string]instanceAuth
	var parse func(instance string, body []byte) ([]model.MediaEvent, error)

	switch source {
	case "sonarr":
		instances, parse = h.sonarr, ParseSonarr
	case "radarr":
		instances, parse = h.radarr, ParseRadarr
	default:
		http.Error(w, "unknown source", http.StatusNotFound)
		return
	}

	inst, ok := instances[instance]
	if !ok {
		http.Error(w, "unknown instance", http.StatusNotFound)
		return
	}

	token, ok := bearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "missing authentication", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(inst.token)) != 1 {
		http.Error(w, "invalid authentication", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}

	events, err := parse(instance, body)
	if err != nil {
		h.log.Warn("malformed webhook payload", "source", source, "instance", instance, "error", err)
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	for _, ev := range events {
		logAttrs := []any{
			"source", ev.Source, "instance", ev.Instance, "event", ev.EventType,
			"media_type", ev.MediaType, "path", ev.Path,
		}
		// is_upgrade only means anything for a Download event (the only
		// one Sonarr/Radarr actually populate it for); showing it as
		// "false" on e.g. a Rename or delete event would misleadingly
		// suggest bifroest determined it wasn't an upgrade, when the
		// concept just doesn't apply there.
		if ev.EventType == "Download" {
			logAttrs = append(logAttrs, "is_upgrade", ev.IsUpgrade)
		}
		h.log.Info("received webhook", logAttrs...)

		scanPath, mapErr := rewrite.Apply(inst.mappings, ev.Path)
		if mapErr != nil {
			h.log.Error("path mapping failed", "source", ev.Source, "instance", ev.Instance, "path", ev.Path, "error", mapErr)
			if err := h.queue.InsertFailed(ctx, ev.Source, ev.Instance, ev.EventType, ev.MediaType, ev.Path, "", mapErr); err != nil {
				h.log.Error("failed recording mapping failure", "error", err)
			}
			continue
		}
		targetDir := scanPath
		if !ev.IsDir {
			targetDir = rewrite.ScanPath(scanPath)
		}

		for _, tgt := range h.targets {
			if err := h.queue.EnqueueScan(ctx, queue.EnqueueParams{
				Source:    ev.Source,
				Instance:  ev.Instance,
				EventType: ev.EventType,
				MediaType: ev.MediaType,
				Path:      ev.Path,
				Target:    tgt,
				ScanPath:  targetDir,
				Delay:     h.delay,
			}); err != nil {
				h.log.Error("failed enqueueing scan job", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			h.log.Info("scan queued", "target", tgt, "path", targetDir)
		}
	}

	w.WriteHeader(http.StatusAccepted)
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	return strings.TrimPrefix(h, prefix), true
}
