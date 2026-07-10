// Package config loads the optional looper.json panel configuration.
//
// Precedence, highest wins: flags (applied by the caller) > env > file >
// defaults. This package owns file+env+defaults; the flag layer is the
// serve command's job (it overwrites fields after Load returns).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/cuatroochenta-idi/looper-agent/telemetry"
)

// Defaults applied when neither file nor env set a value.
const (
	DefaultPort     = 9090
	DefaultStoreDir = ".looper"
)

// Config is the panel configuration. A missing file yields a zero Config with
// defaults applied — not an error — so local dev needs no file at all.
type Config struct {
	Port     int         `json:"port"`
	DB       string      `json:"db"`        // postgres DSN; empty = folder store
	StoreDir string      `json:"store_dir"` // default ".looper"
	Auth     *AuthConfig `json:"auth,omitempty"`

	// ModelCosts feeds telemetry.CostModel.WithCustomCosts. Keys are
	// "provider/model" or a bare model id; values reuse telemetry.CostConfig's
	// json tags (input, output, cached, cache_write).
	ModelCosts map[string]telemetry.CostConfig `json:"model_costs,omitempty"`
}

// AuthConfig enables the login page and ingest bearer protection. Auth is off
// unless Password is non-empty (see Config.AuthEnabled).
type AuthConfig struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password"`
	SessionSecret string `json:"session_secret,omitempty"`
	IngestToken   string `json:"ingest_token,omitempty"`
}

// AuthEnabled reports whether the login flow should be active. A password is
// required: an Auth block without one is treated as disabled.
func (c Config) AuthEnabled() bool {
	return c.Auth != nil && c.Auth.Password != ""
}

// Load reads JSON config from path. When path is "" it tries ./looper.json,
// then $LOOPER_CONFIG. A missing file is not an error: the zero Config with
// defaults applied is returned. Unknown JSON fields are rejected so typos
// surface loudly. Env overrides (LOOPER_*) are applied last.
func Load(path string) (Config, error) {
	cfg := Config{}

	resolved, explicit := resolvePath(path)
	if resolved != "" {
		if err := readFile(resolved, &cfg); err != nil {
			// Missing file is only tolerated when the path was auto-discovered,
			// not when the caller (or $LOOPER_CONFIG) named it explicitly.
			if errors.Is(err, fs.ErrNotExist) && !explicit {
				// fall through to defaults + env
			} else {
				return Config{}, err
			}
		}
	}

	applyEnv(&cfg)
	applyDefaults(&cfg)
	return cfg, nil
}

// resolvePath returns the config path to read and whether it was named
// explicitly (by the caller or $LOOPER_CONFIG) vs auto-discovered. Explicit
// paths make a missing file an error; auto-discovered ones do not.
func resolvePath(path string) (resolved string, explicit bool) {
	if path != "" {
		return path, true
	}
	if _, err := os.Stat("looper.json"); err == nil {
		return "looper.json", false
	}
	if env := os.Getenv("LOOPER_CONFIG"); env != "" {
		return env, true
	}
	return "", false
}

func readFile(path string, cfg *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields() // typos in looper.json become errors, not silence
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// applyEnv overlays LOOPER_* environment variables onto cfg. Only set env
// vars override; empty/unset ones leave the file value intact.
func applyEnv(cfg *Config) {
	if v, ok := os.LookupEnv("LOOPER_PORT"); ok && v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v, ok := os.LookupEnv("LOOPER_DB"); ok {
		cfg.DB = v
	}
	if v, ok := os.LookupEnv("LOOPER_STORE_DIR"); ok && v != "" {
		cfg.StoreDir = v
	}

	// Auth env vars lazily materialize the Auth block so a password supplied
	// only via env still enables auth.
	if v, ok := os.LookupEnv("LOOPER_AUTH_USERNAME"); ok {
		ensureAuth(cfg).Username = v
	}
	if v, ok := os.LookupEnv("LOOPER_AUTH_PASSWORD"); ok {
		ensureAuth(cfg).Password = v
	}
	if v, ok := os.LookupEnv("LOOPER_SESSION_SECRET"); ok {
		ensureAuth(cfg).SessionSecret = v
	}
	if v, ok := os.LookupEnv("LOOPER_INGEST_TOKEN"); ok {
		ensureAuth(cfg).IngestToken = v
	}
}

func ensureAuth(cfg *Config) *AuthConfig {
	if cfg.Auth == nil {
		cfg.Auth = &AuthConfig{}
	}
	return cfg.Auth
}

func applyDefaults(cfg *Config) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.StoreDir == "" {
		cfg.StoreDir = DefaultStoreDir
	}
}
