// Command bifroest receives Sonarr/Radarr webhooks and triggers targeted
// Plex/Jellyfin library scans, rewriting paths between the two filesystem
// views and coordinating with an rclone mount health check.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/chruth/bifroest/internal/config"
	"github.com/chruth/bifroest/internal/logging"
	"github.com/chruth/bifroest/internal/mount"
	"github.com/chruth/bifroest/internal/queue"
	"github.com/chruth/bifroest/internal/server"
	"github.com/chruth/bifroest/internal/target"
	"github.com/chruth/bifroest/internal/target/jellyfin"
	"github.com/chruth/bifroest/internal/target/plex"
	"github.com/chruth/bifroest/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to configuration file (optional; bifroest can run entirely from BIFROEST_* environment variables)")
	flag.Parse()

	// A basic logger for startup, reconfigured once the config's log level
	// is known.
	log := slog.New(logging.New(os.Stdout, slog.LevelInfo))

	// 1. Load configuration.
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 4. Initialize logging.
	var closeLog func() error
	log, closeLog, err = newLogger(cfg)
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	defer closeLog()
	slog.SetDefault(log)
	logStartupSummary(log, cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2 & 3. Open SQLite and run migrations.
	db, err := queue.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	q := queue.New(db)

	// 5. Initialize Plex/Jellyfin clients.
	targets := map[string]target.Target{}
	var enabledTargetNames []string

	if cfg.Targets.Plex.Enabled {
		c := plex.New(cfg.Targets.Plex.URL, cfg.Targets.Plex.Token, log.With("target", "plex"))
		targets[c.Name()] = c
		enabledTargetNames = append(enabledTargetNames, c.Name())
		// 6. Discover target libraries. Non-fatal: Plex may be down at
		// startup. Library discovery is also retried reactively by Scan
		// whenever a path doesn't match the cache, so no periodic
		// background refresh is needed - library layouts essentially
		// never change in practice.
		if err := c.RefreshLibraries(ctx); err != nil {
			log.Warn("initial plex library discovery failed, will retry on next scan", "error", err)
		}
	}

	if cfg.Targets.Jellyfin.Enabled {
		c := jellyfin.New(cfg.Targets.Jellyfin.URL, cfg.Targets.Jellyfin.Token, log.With("target", "jellyfin"))
		targets[c.Name()] = c
		enabledTargetNames = append(enabledTargetNames, c.Name())
		if err := c.RefreshLibraries(ctx); err != nil {
			log.Warn("initial jellyfin library discovery failed, will retry on next scan", "error", err)
		}
	}

	// 10. Resume persisted jobs: anything left "processing" from a prior
	// run is presumed interrupted, not completed.
	if err := q.RecoverStuckJobs(ctx); err != nil {
		return fmt.Errorf("recover stuck jobs: %w", err)
	}

	var wg sync.WaitGroup

	// 7. Start the mount monitor.
	mon := mount.New(cfg.Mount.Anchor, cfg.Mount.CheckInterval, log.With("component", "mount"))
	wg.Add(1)
	go func() {
		defer wg.Done()
		mon.Run(ctx)
	}()

	// 8. Start queue workers.
	pool := queue.NewWorkerPool(q, targets, mon, log.With("component", "queue"))
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Run(ctx, cfg.Queue.Workers, cfg.Queue.PollInterval)
	}()

	// 9. Start HTTP server.
	webhookHandler := webhook.New(cfg, q, enabledTargetNames, log.With("component", "webhook"))
	httpHandler := server.New(webhookHandler, mon.Available, log.With("component", "http"))

	wg.Add(1)
	var serveErr error
	go func() {
		defer wg.Done()
		serveErr = server.Serve(ctx, cfg.Server.Address(), httpHandler, log)
	}()

	<-ctx.Done()
	log.Info("shutdown signal received")
	wg.Wait()

	return serveErr
}

// newLogger builds the application logger: always to stdout (colored), and
// additionally to cfg.Log.Path (rotated, plain - no ANSI codes in a file
// meant to be read later by grep/tail/whatever) if one is configured. The
// returned close func releases the log file on shutdown; it's a no-op
// when no file is configured.
//
// Rotation uses lumberjack with fixed, sensible defaults rather than
// exposing yet more config knobs for a feature that just needs to not
// grow the file forever: 100MB per file, 3 old files kept (compressed),
// 28 days maximum age.
func newLogger(cfg *config.Config) (*slog.Logger, func() error, error) {
	level := parseLevel(cfg.Log.Level)
	console := logging.New(os.Stdout, level)

	if cfg.Log.Path == "" {
		return slog.New(console), func() error { return nil }, nil
	}

	// slog.Logger's public methods (Info/Warn/...) never surface a
	// Handler's write errors to the caller - a broken log destination
	// isn't allowed to crash the app. That's the right call at runtime,
	// but it means a bad path here (e.g. an existing directory, or a
	// parent directory bifroest can't create/write to) would otherwise
	// fail silently forever: every line to the file just vanishes with
	// nothing printed anywhere to explain why. Catch that now, once, at
	// startup, while we can still fail loudly.
	if err := ensureWritableFile(cfg.Log.Path); err != nil {
		return nil, nil, fmt.Errorf("log.path %q: %w", cfg.Log.Path, err)
	}

	rotator := &lumberjack.Logger{
		Filename:   cfg.Log.Path,
		MaxSize:    100, // megabytes
		MaxBackups: 3,
		MaxAge:     28, // days
		Compress:   true,
	}
	file := logging.NewPlain(rotator, level)
	return slog.New(logging.NewFanOut(console, file)), rotator.Close, nil
}

// ensureWritableFile creates path's parent directory if needed and confirms
// path itself can be opened for writing - notably rejecting the case where
// path is an existing directory rather than a file.
func ensureWritableFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// logStartupSummary prints the effective (non-secret) configuration once,
// right after it's loaded, so the first thing a look at the logs shows is
// "here's what's actually in effect" rather than scattered per-target
// startup noise. Split into a few short lines rather than one long one -
// easier to scan at a glance. Tokens/API keys are never included.
func logStartupSummary(log *slog.Logger, cfg *config.Config) {
	logAttrs := []any{"port", cfg.Server.Port, "log_level", cfg.Log.Level}
	if cfg.Log.Path != "" {
		logAttrs = append(logAttrs, "log_path", cfg.Log.Path)
	}
	log.Info("starting bifroest", logAttrs...)
	log.Info("mount", "anchor", cfg.Mount.Anchor)
	log.Info("database", "path", cfg.Database.Path)

	if cfg.Targets.Plex.Enabled {
		log.Info("plex", "enabled", true, "url", cfg.Targets.Plex.URL)
	} else {
		log.Info("plex", "enabled", false)
	}
	if cfg.Targets.Jellyfin.Enabled {
		log.Info("jellyfin", "enabled", true, "url", cfg.Targets.Jellyfin.URL)
	} else {
		log.Info("jellyfin", "enabled", false)
	}

	log.Info("sources",
		"sonarr", instanceList(cfg.Sources.Sonarr),
		"radarr", instanceList(cfg.Sources.Radarr),
	)
}

func instanceList(instances map[string]config.SourceInstance) string {
	if len(instances) == 0 {
		return "none"
	}
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
