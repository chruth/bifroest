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
	"sync"
	"syscall"

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
	log = slog.New(logging.New(os.Stdout, parseLevel(cfg.Log.Level)))
	slog.SetDefault(log)

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
