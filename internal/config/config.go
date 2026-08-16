// Package config loads and validates the application's YAML configuration.
package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Mount  MountConfig  `yaml:"mount"`
	Queue  QueueConfig  `yaml:"queue"`
	// env:"-" on Sources and Targets: both are groupings in the YAML
	// schema, but the env var names skip straight to
	// BIFROEST_SONARR_*/BIFROEST_PLEX_* rather than
	// BIFROEST_SOURCES_SONARR_*/BIFROEST_TARGETS_PLEX_* - neither word
	// carries information worth an extra path segment, and it just makes
	// the names longer to type.
	Sources  SourcesConfig  `yaml:"sources" env:"-"`
	Targets  TargetsConfig  `yaml:"targets" env:"-"`
	Database DatabaseConfig `yaml:"database"`
	Log      LogConfig      `yaml:"log"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

// Address returns the net/http listen address for this config. Binding is
// always to all interfaces (":<port>") — bifroest is Docker-first, and
// interface restriction belongs at the Docker port-publishing layer (e.g.
// "127.0.0.1:8080:8080" in docker-compose.yml), not duplicated here.
func (s ServerConfig) Address() string {
	return fmt.Sprintf(":%d", s.Port)
}

type MountConfig struct {
	Anchor        string        `yaml:"anchor"`
	CheckInterval time.Duration `yaml:"check_interval"`
}

type QueueConfig struct {
	Workers      int           `yaml:"workers"`
	Delay        time.Duration `yaml:"delay"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

type SourcesConfig struct {
	Sonarr map[string]SourceInstance `yaml:"sonarr"`
	Radarr map[string]SourceInstance `yaml:"radarr"`
}

type SourceInstance struct {
	Token    string        `yaml:"token"`
	PathMaps []PathMapping `yaml:"path_maps"`
}

type PathMapping struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type TargetsConfig struct {
	Plex     PlexConfig     `yaml:"plex"`
	Jellyfin JellyfinConfig `yaml:"jellyfin"`
}

type PlexConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	Token   string `yaml:"token"`
}

type JellyfinConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	Token   string `yaml:"token"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type LogConfig struct {
	Level string `yaml:"level"`
	// Dir, if set, additionally writes logs to a file inside this
	// directory (rotated - see cmd/bifroest). The directory is created if
	// it doesn't exist; bifroest picks the filename itself. Empty (the
	// default) means stdout only.
	Dir string `yaml:"dir"`
}

func defaults() Config {
	return Config{
		Server: ServerConfig{Port: 8080},
		Mount:  MountConfig{Anchor: "/media/anchor.bin", CheckInterval: 5 * time.Second},
		Queue: QueueConfig{
			Workers:      2,
			Delay:        5 * time.Second,
			PollInterval: 5 * time.Second,
		},
		Database: DatabaseConfig{Path: "/data/app.db"},
		Log:      LogConfig{Level: "info"},
	}
}

// Load builds a Config from defaults, an optional YAML file at path, and
// environment variables, in that order of increasing precedence, and
// validates the result.
//
// The config file is optional: bifroest can run entirely from environment
// variables (see applyEnvOverrides and discoverSourceInstances). A missing
// file at path is not an error; any other read failure (permissions, a
// directory in the way, ...) is.
func Load(path string) (*Config, error) {
	cfg := defaults()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse config file: %w", err)
		}
	case os.IsNotExist(err):
		// No config file: proceed on defaults + environment variables
		// alone. This is a supported mode, not an error.
	default:
		return nil, fmt.Errorf("read config file: %w", err)
	}

	discoverSourceInstances(&cfg)

	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, fmt.Errorf("apply environment overrides: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// envPrefix namespaces every environment variable bifroest reads, so it
// never collides with an unrelated variable in a shared environment.
const envPrefix = "BIFROEST"

// tokenSuffix is the env var suffix discoverSourceInstances looks for.
// SourceInstance currently has exactly one overridable scalar field
// (Token; PathMaps is YAML-only), so this is the only shape a
// config-file-less instance definition can take.
const tokenSuffix = "_TOKEN"

// discoverSourceInstances lets a Sonarr/Radarr instance be defined purely
// from an environment variable, with no config file at all: for any
// BIFROEST_SONARR_<NAME>_TOKEN or BIFROEST_RADARR_<NAME>_TOKEN variable
// whose <NAME> isn't already a key in the corresponding map (from the
// config file, if any), an empty SourceInstance is inserted under that
// (lower-cased) name before applyEnvOverrides runs, so the normal env
// override logic then fills in its Token.
//
// Such an instance has no path_maps, which per rewrite.Apply means its
// source and target paths are treated as identical - the one thing this
// can't express is a genuine prefix rewrite, which still requires a config
// file. Existing config-file instances are never touched here.
func discoverSourceInstances(cfg *Config) {
	discoverInstances(&cfg.Sources.Sonarr, "SONARR")
	discoverInstances(&cfg.Sources.Radarr, "RADARR")
}

func discoverInstances(m *map[string]SourceInstance, source string) {
	prefix := envPrefix + "_" + source + "_"
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, tokenSuffix) {
			continue
		}
		instance := strings.TrimSuffix(strings.TrimPrefix(name, prefix), tokenSuffix)
		if instance == "" {
			continue
		}
		key := strings.ToLower(instance)
		if *m == nil {
			*m = make(map[string]SourceInstance)
		}
		if _, exists := (*m)[key]; !exists {
			(*m)[key] = SourceInstance{}
		}
	}
}

// durationType lets applyEnvToLeaf special-case time.Duration fields,
// which reflect.Kind reports as Int64 like any other integer.
var durationType = reflect.TypeOf(time.Duration(0))

// applyEnvOverrides overlays environment variables onto cfg. Every scalar
// field (string, bool, int, time.Duration) anywhere in the config tree can
// be set this way: the env var name is built by walking the field's yaml
// tag path, upper-casing each segment, and joining with underscores,
// prefixed with BIFROEST_ (a field tagged env:"-", currently Sources and
// Targets, is traversed but contributes no segment of its own). A
// Sonarr/Radarr instance name (a map key) is inserted into the path
// unchanged, upper-cased. For example:
//
//	server.port                    -> BIFROEST_SERVER_PORT
//	mount.check_interval           -> BIFROEST_MOUNT_CHECK_INTERVAL
//	targets.plex.token             -> BIFROEST_PLEX_TOKEN
//	targets.jellyfin.token         -> BIFROEST_JELLYFIN_TOKEN
//	sources.sonarr.main.token      -> BIFROEST_SONARR_MAIN_TOKEN
//
// A set (non-empty) env var always overrides the config file's value for
// that field; an unset or empty one leaves the file's value untouched.
//
// path_maps has no sensible single-env-var representation (it's a list of
// from/to pairs) and stays YAML-only - see discoverSourceInstances for how
// a Sonarr/Radarr instance can still be defined without one.
func applyEnvOverrides(cfg *Config) error {
	return applyEnvToValue(reflect.ValueOf(cfg).Elem(), []string{envPrefix})
}

func applyEnvToValue(v reflect.Value, path []string) error {
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
			if tag == "" || tag == "-" {
				continue
			}
			fieldPath := path
			if field.Tag.Get("env") != "-" {
				fieldPath = append(path, tag)
			}
			if err := applyEnvToValue(v.Field(i), fieldPath); err != nil {
				return err
			}
		}
		return nil

	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		// Map values aren't addressable, so mutate a scratch copy and
		// write it back.
		for _, key := range v.MapKeys() {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(v.MapIndex(key))
			if err := applyEnvToValue(elem, append(path, key.String())); err != nil {
				return err
			}
			v.SetMapIndex(key, elem)
		}
		return nil

	case reflect.Slice:
		// No sensible single-env-var representation for a list of
		// structs (e.g. path_maps); left YAML-only.
		return nil

	default:
		return applyEnvToLeaf(v, path)
	}
}

func applyEnvToLeaf(v reflect.Value, path []string) error {
	name := envVarName(path)
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return nil
	}

	switch {
	case v.Type() == durationType:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q: %w", name, raw, err)
		}
		v.SetInt(int64(d))
	case v.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid bool %q: %w", name, raw, err)
		}
		v.SetBool(b)
	case v.Kind() == reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q: %w", name, raw, err)
		}
		v.SetInt(int64(n))
	case v.Kind() == reflect.String:
		v.SetString(raw)
	}
	return nil
}

func envVarName(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = strings.ToUpper(strings.ReplaceAll(p, "-", "_"))
	}
	return strings.Join(parts, "_")
}

func (c *Config) validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	if c.Mount.Anchor == "" {
		return fmt.Errorf("mount.anchor is required")
	}
	if c.Mount.CheckInterval <= 0 {
		return fmt.Errorf("mount.check_interval must be positive")
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	if c.Queue.Workers < 1 {
		return fmt.Errorf("queue.workers must be at least 1")
	}
	if c.Queue.PollInterval <= 0 {
		return fmt.Errorf("queue.poll_interval must be positive")
	}
	if len(c.Sources.Sonarr) == 0 && len(c.Sources.Radarr) == 0 {
		return fmt.Errorf("at least one sonarr or radarr source instance must be configured")
	}
	if !c.Targets.Plex.Enabled && !c.Targets.Jellyfin.Enabled {
		return fmt.Errorf("at least one target (plex or jellyfin) must be enabled")
	}

	for source, instances := range map[string]map[string]SourceInstance{
		"sonarr": c.Sources.Sonarr,
		"radarr": c.Sources.Radarr,
	} {
		for name, inst := range instances {
			if inst.Token == "" {
				envVar := envVarName([]string{envPrefix, source, name, "token"})
				return fmt.Errorf("%s.%s: token is required (set it in the config file or via %s)", source, name, envVar)
			}
			// path_maps is optional: no entries means this instance's
			// paths are identical on both sides (see rewrite.Apply), so
			// there's nothing to require here. But any entries that ARE
			// given must be well-formed.
			for i, m := range inst.PathMaps {
				if m.From == "" || m.To == "" {
					return fmt.Errorf("%s.%s: path_maps[%d]: both from and to are required", source, name, i)
				}
			}
		}
	}

	if c.Targets.Plex.Enabled {
		if c.Targets.Plex.URL == "" {
			return fmt.Errorf("targets.plex.url is required when plex is enabled (set it in the config file or via BIFROEST_PLEX_URL)")
		}
		if c.Targets.Plex.Token == "" {
			return fmt.Errorf("targets.plex.token is required when plex is enabled (set it in the config file or via BIFROEST_PLEX_TOKEN)")
		}
	}
	if c.Targets.Jellyfin.Enabled {
		if c.Targets.Jellyfin.URL == "" {
			return fmt.Errorf("targets.jellyfin.url is required when jellyfin is enabled (set it in the config file or via BIFROEST_JELLYFIN_URL)")
		}
		if c.Targets.Jellyfin.Token == "" {
			return fmt.Errorf("targets.jellyfin.token is required when jellyfin is enabled (set it in the config file or via BIFROEST_JELLYFIN_TOKEN)")
		}
	}

	return nil
}
