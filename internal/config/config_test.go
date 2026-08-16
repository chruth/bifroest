package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const minimalConfig = `
mount:
  anchor: "/media/anchor.bin"

sources:
  sonarr:
    main:
      token: "sonarr-secret"
      path_maps:
        - from: "/tv/"
          to: "/media/tv/"

targets:
  plex:
    enabled: true
    url: "http://plex:32400"
    token: "plex-secret"

database:
  path: "/data/app.db"
`

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sources.Sonarr["main"].Token != "sonarr-secret" {
		t.Errorf("unexpected token: %s", cfg.Sources.Sonarr["main"].Token)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default server port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Address() != ":8080" {
		t.Errorf("expected derived address :8080, got %s", cfg.Server.Address())
	}
	if len(cfg.Sources.Sonarr["main"].PathMaps) != 1 || cfg.Sources.Sonarr["main"].PathMaps[0].From != "/tv/" {
		t.Errorf("unexpected path maps: %+v", cfg.Sources.Sonarr["main"].PathMaps)
	}
}

func TestLoadMissingPathMapsIsOptional(t *testing.T) {
	// No path_maps means this instance's paths are identical on both
	// sides (see rewrite.Apply) - not a config error.
	cfg := `
mount:
  anchor: "/media/anchor.bin"
sources:
  sonarr:
    main:
      token: "x"
targets:
  plex:
    enabled: true
    url: "http://plex:32400"
    token: "x"
database:
  path: "/data/app.db"
`
	loaded, err := Load(writeConfig(t, cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded.Sources.Sonarr["main"].PathMaps) != 0 {
		t.Errorf("expected no path maps, got %+v", loaded.Sources.Sonarr["main"].PathMaps)
	}
}

func TestLoadIncompletePathMapEntryFails(t *testing.T) {
	bad := `
mount:
  anchor: "/media/anchor.bin"
sources:
  sonarr:
    main:
      token: "x"
      path_maps:
        - from: "/tv/"
targets:
  plex:
    enabled: true
    url: "http://plex:32400"
    token: "x"
database:
  path: "/data/app.db"
`
	if _, err := Load(writeConfig(t, bad)); err == nil {
		t.Fatal("expected error for a path_maps entry missing 'to'")
	}
}

func TestLoadNoTargetsEnabledFails(t *testing.T) {
	bad := `
mount:
  anchor: "/media/anchor.bin"
sources:
  sonarr:
    main:
      token: "x"
      path_maps:
        - from: "/tv/"
          to: "/media/tv/"
database:
  path: "/data/app.db"
`
	if _, err := Load(writeConfig(t, bad)); err == nil {
		t.Fatal("expected error when no targets are enabled")
	}
}

func TestLoadZeroPollIntervalFails(t *testing.T) {
	// queue.poll_interval feeds directly into time.NewTicker, which panics
	// on a non-positive duration - this must be caught at config load time,
	// not at the first worker tick.
	bad := minimalConfig + "\nqueue:\n  poll_interval: 0s\n"
	if _, err := Load(writeConfig(t, bad)); err == nil {
		t.Fatal("expected error for queue.poll_interval: 0s")
	}
}

func TestLoadZeroCheckIntervalFails(t *testing.T) {
	// Same hazard as poll_interval, via the mount monitor's ticker.
	bad := `
mount:
  anchor: "/media/anchor.bin"
  check_interval: 0s
sources:
  sonarr:
    main:
      token: "x"
      path_maps:
        - from: "/tv/"
          to: "/media/tv/"
targets:
  plex:
    enabled: true
    url: "http://plex:32400"
    token: "x"
database:
  path: "/data/app.db"
`
	if _, err := Load(writeConfig(t, bad)); err == nil {
		t.Fatal("expected error for mount.check_interval: 0s")
	}
}

func TestLoadInvalidPortFails(t *testing.T) {
	bad := `
server:
  port: 0
mount:
  anchor: "/media/anchor.bin"
sources:
  sonarr:
    main:
      token: "x"
      path_maps:
        - from: "/tv/"
          to: "/media/tv/"
targets:
  plex:
    enabled: true
    url: "http://plex:32400"
    token: "x"
database:
  path: "/data/app.db"
`
	if _, err := Load(writeConfig(t, bad)); err == nil {
		t.Fatal("expected error for server.port: 0")
	}
}

func TestEnvOverrideFillsBlankToken(t *testing.T) {
	cfg := `
mount:
  anchor: "/media/anchor.bin"
sources:
  sonarr:
    main:
      token: ""
      path_maps:
        - from: "/tv/"
          to: "/media/tv/"
targets:
  plex:
    enabled: true
    url: "http://plex:32400"
    token: ""
database:
  path: "/data/app.db"
`
	t.Setenv("BIFROEST_SOURCES_SONARR_MAIN_TOKEN", "from-env")
	t.Setenv("BIFROEST_PLEX_TOKEN", "plex-from-env")

	loaded, err := Load(writeConfig(t, cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.Sources.Sonarr["main"].Token != "from-env" {
		t.Errorf("got token %q, want from-env", loaded.Sources.Sonarr["main"].Token)
	}
	if loaded.Targets.Plex.Token != "plex-from-env" {
		t.Errorf("got plex token %q, want plex-from-env", loaded.Targets.Plex.Token)
	}
}

func TestEnvOverrideBeatsNonBlankYAMLValue(t *testing.T) {
	// Env vars are the final override layer: unlike the old scheme (env
	// only filled in blanks), a set env var wins even if the config file
	// already has a value, for every field - not just secrets.
	t.Setenv("BIFROEST_PLEX_TOKEN", "from-env")

	loaded, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.Targets.Plex.Token != "from-env" {
		t.Errorf("got token %q, want from-env to override the file's plex-secret", loaded.Targets.Plex.Token)
	}
}

func TestEnvOverrideCoversScalarFieldsThroughoutTheTree(t *testing.T) {
	t.Setenv("BIFROEST_SERVER_PORT", "9999")
	t.Setenv("BIFROEST_MOUNT_CHECK_INTERVAL", "30s")
	t.Setenv("BIFROEST_QUEUE_WORKERS", "7")
	t.Setenv("BIFROEST_QUEUE_DELAY", "1m")
	t.Setenv("BIFROEST_PLEX_ENABLED", "false")
	t.Setenv("BIFROEST_JELLYFIN_ENABLED", "true")
	t.Setenv("BIFROEST_JELLYFIN_URL", "http://jellyfin:8096")
	t.Setenv("BIFROEST_JELLYFIN_TOKEN", "jf-secret")
	t.Setenv("BIFROEST_DATABASE_PATH", "/tmp/other.db")
	t.Setenv("BIFROEST_LOG_LEVEL", "debug")

	loaded, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.Server.Port != 9999 {
		t.Errorf("got port %d, want 9999", loaded.Server.Port)
	}
	if loaded.Mount.CheckInterval != 30*time.Second {
		t.Errorf("got check_interval %s, want 30s", loaded.Mount.CheckInterval)
	}
	if loaded.Queue.Workers != 7 {
		t.Errorf("got workers %d, want 7", loaded.Queue.Workers)
	}
	if loaded.Queue.Delay != time.Minute {
		t.Errorf("got delay %s, want 1m", loaded.Queue.Delay)
	}
	if loaded.Targets.Plex.Enabled {
		t.Error("expected plex.enabled to be overridden to false")
	}
	if !loaded.Targets.Jellyfin.Enabled || loaded.Targets.Jellyfin.Token != "jf-secret" {
		t.Errorf("unexpected jellyfin config: %+v", loaded.Targets.Jellyfin)
	}
	if loaded.Database.Path != "/tmp/other.db" {
		t.Errorf("got database.path %q, want /tmp/other.db", loaded.Database.Path)
	}
	if loaded.Log.Level != "debug" {
		t.Errorf("got log.level %q, want debug", loaded.Log.Level)
	}
}

func TestEnvOverrideInvalidValueFails(t *testing.T) {
	t.Setenv("BIFROEST_QUEUE_WORKERS", "not-a-number")
	if _, err := Load(writeConfig(t, minimalConfig)); err == nil {
		t.Fatal("expected error for a non-numeric BIFROEST_QUEUE_WORKERS")
	}
}

func TestEnvOverrideCanInventNewSourceInstance(t *testing.T) {
	// A _TOKEN env var for an instance name not already in the file
	// creates that instance (with no path_maps, i.e. identity-mapped
	// paths) rather than being silently dropped - this is what makes a
	// config-file-less setup possible for instances that don't need a
	// prefix rewrite.
	t.Setenv("BIFROEST_SOURCES_SONARR_ANIME_TOKEN", "anime-secret")

	loaded, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	anime, ok := loaded.Sources.Sonarr["anime"]
	if !ok {
		t.Fatal("expected an 'anime' instance to be created from the env var alone")
	}
	if anime.Token != "anime-secret" {
		t.Errorf("got token %q, want anime-secret", anime.Token)
	}
	if len(anime.PathMaps) != 0 {
		t.Errorf("expected no path maps for an env-only instance, got %+v", anime.PathMaps)
	}
	// The file's own "main" instance must be untouched.
	if loaded.Sources.Sonarr["main"].Token != "sonarr-secret" {
		t.Errorf("existing instance was disturbed: %+v", loaded.Sources.Sonarr["main"])
	}
}

func TestEnvOverrideDoesNotClobberExistingInstance(t *testing.T) {
	// Discovery must never overwrite an instance the file already defines
	// (which could otherwise wipe out its path_maps).
	t.Setenv("BIFROEST_SOURCES_SONARR_MAIN_TOKEN", "from-env")

	loaded, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	main := loaded.Sources.Sonarr["main"]
	if main.Token != "from-env" {
		t.Errorf("got token %q, want from-env", main.Token)
	}
	if len(main.PathMaps) != 1 || main.PathMaps[0].From != "/tv/" {
		t.Errorf("expected the file's path_maps to survive, got %+v", main.PathMaps)
	}
}

func TestLoadWithoutConfigFileFromEnvAlone(t *testing.T) {
	t.Setenv("BIFROEST_MOUNT_ANCHOR", "/media/anchor.bin")
	t.Setenv("BIFROEST_SOURCES_SONARR_MAIN_TOKEN", "sonarr-secret")
	t.Setenv("BIFROEST_PLEX_ENABLED", "true")
	t.Setenv("BIFROEST_PLEX_URL", "http://plex:32400")
	t.Setenv("BIFROEST_PLEX_TOKEN", "plex-secret")

	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	loaded, err := Load(missing)
	if err != nil {
		t.Fatalf("unexpected error running without a config file: %v", err)
	}
	if loaded.Sources.Sonarr["main"].Token != "sonarr-secret" {
		t.Errorf("got token %q, want sonarr-secret", loaded.Sources.Sonarr["main"].Token)
	}
	if !loaded.Targets.Plex.Enabled || loaded.Targets.Plex.URL != "http://plex:32400" {
		t.Errorf("unexpected plex config: %+v", loaded.Targets.Plex)
	}
	// Defaults still apply for anything not set via env.
	if loaded.Server.Port != 8080 {
		t.Errorf("got port %d, want default 8080", loaded.Server.Port)
	}
}

func TestLoadOtherFileReadErrorStillFails(t *testing.T) {
	// A missing file is tolerated, but a directory where a file was
	// expected is a genuine error and must not be silently swallowed the
	// same way.
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("expected an error when the config path is a directory")
	}
}
