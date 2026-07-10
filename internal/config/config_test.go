package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes s to <dir>/name and returns the full path.
func writeConfig(t *testing.T, dir, name, s string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoadDefaultsWhenMissing(t *testing.T) {
	// Empty path + no discoverable file => defaults, no error.
	t.Chdir(t.TempDir())
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.StoreDir != DefaultStoreDir {
		t.Errorf("StoreDir = %q, want %q", cfg.StoreDir, DefaultStoreDir)
	}
	if cfg.AuthEnabled() {
		t.Error("AuthEnabled() = true, want false for zero config")
	}
}

func TestLoadFileValues(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, "looper.json", `{
		"port": 8080,
		"db": "postgres://localhost/looper",
		"store_dir": "/data/looper",
		"auth": {"username": "admin", "password": "secret", "ingest_token": "tok"},
		"model_costs": {"anthropic/claude-x": {"input": 3, "output": 15, "cached": 0.3, "cache_write": 3.75}}
	}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 || cfg.DB != "postgres://localhost/looper" || cfg.StoreDir != "/data/looper" {
		t.Errorf("unexpected top-level fields: %+v", cfg)
	}
	if !cfg.AuthEnabled() {
		t.Fatal("AuthEnabled() = false, want true")
	}
	if cfg.Auth.Username != "admin" || cfg.Auth.Password != "secret" || cfg.Auth.IngestToken != "tok" {
		t.Errorf("unexpected auth: %+v", cfg.Auth)
	}
	mc, ok := cfg.ModelCosts["anthropic/claude-x"]
	if !ok {
		t.Fatal("model_costs missing key")
	}
	if mc.InputCostPer1MTokens != 3 || mc.OutputCostPer1MTokens != 15 ||
		mc.CachedCostPer1MTokens != 0.3 || mc.CacheWriteCostPer1MTokens != 3.75 {
		t.Errorf("unexpected cost config: %+v", mc)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, "looper.json", `{"port": 8080, "db": "file-db", "store_dir": "file-dir"}`)

	t.Setenv("LOOPER_PORT", "9999")
	t.Setenv("LOOPER_DB", "env-db")
	t.Setenv("LOOPER_STORE_DIR", "env-dir")
	t.Setenv("LOOPER_AUTH_PASSWORD", "env-pass")
	t.Setenv("LOOPER_AUTH_USERNAME", "env-user")
	t.Setenv("LOOPER_SESSION_SECRET", "env-secret")
	t.Setenv("LOOPER_INGEST_TOKEN", "env-tok")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port = %d, want 9999 (env wins)", cfg.Port)
	}
	if cfg.DB != "env-db" || cfg.StoreDir != "env-dir" {
		t.Errorf("env did not override db/store_dir: %+v", cfg)
	}
	if !cfg.AuthEnabled() {
		t.Fatal("env password should enable auth")
	}
	if cfg.Auth.Username != "env-user" || cfg.Auth.Password != "env-pass" ||
		cfg.Auth.SessionSecret != "env-secret" || cfg.Auth.IngestToken != "env-tok" {
		t.Errorf("unexpected auth after env: %+v", cfg.Auth)
	}
}

func TestEnvEnablesAuthWithoutFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("LOOPER_AUTH_PASSWORD", "only-env")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AuthEnabled() {
		t.Fatal("auth should be enabled from env password alone")
	}
	if cfg.Auth.Password != "only-env" {
		t.Errorf("password = %q", cfg.Auth.Password)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, "looper.json", `{"port": 8080, "prot": 9090}`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestExplicitMissingFileIsError(t *testing.T) {
	// Caller named a path that doesn't exist => error (surfaces bad --config).
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := Load(missing); err == nil {
		t.Fatal("expected error for explicit missing file")
	}
}

func TestDiscoverLooperJSONInCwd(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "looper.json", `{"port": 7070}`)
	t.Chdir(dir)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 7070 {
		t.Errorf("Port = %d, want 7070 from discovered looper.json", cfg.Port)
	}
}

func TestLooperConfigEnvPath(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, "custom.json", `{"port": 6060}`)
	// Ensure no ./looper.json shadows the env path.
	t.Chdir(t.TempDir())
	t.Setenv("LOOPER_CONFIG", p)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 6060 {
		t.Errorf("Port = %d, want 6060 from $LOOPER_CONFIG", cfg.Port)
	}
}
