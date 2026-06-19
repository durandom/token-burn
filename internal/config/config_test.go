package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
poll_interval = "2m"
http_timeout = "5s"
database_path = "` + filepath.ToSlash(filepath.Join(dir, "token-burn.db")) + `"

[otel]
enabled = true
endpoint = "http://127.0.0.1:4318"
protocol = "http/protobuf"
export_interval = "30s"

[[accounts]]
provider = "codex"
id = "codex-default"
auth_file = "/tmp/codex-auth.json"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.PollInterval != 2*time.Minute {
		t.Fatalf("PollInterval = %s, want 2m", cfg.PollInterval)
	}
	if cfg.HTTPTimeout != 5*time.Second {
		t.Fatalf("HTTPTimeout = %s, want 5s", cfg.HTTPTimeout)
	}
	if !cfg.OTel.Enabled {
		t.Fatal("OTel.Enabled = false, want true")
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Provider != "codex" {
		t.Fatalf("Accounts = %#v, want one codex account", cfg.Accounts)
	}
}

func TestLoadMissingConfigReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	path := filepath.Join(dir, "config", "token-burn", "config.toml")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Fatalf("PollInterval = %s, want %s", cfg.PollInterval, DefaultPollInterval)
	}
	if cfg.DatabasePath != DefaultDatabasePath() {
		t.Fatalf("DatabasePath = %q, want %q", cfg.DatabasePath, DefaultDatabasePath())
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("default account count = %d, want 2", len(cfg.Accounts))
	}
	if cfg.Accounts[0].Provider != "codex" || cfg.Accounts[1].Provider != "claude" {
		t.Fatalf("default accounts = %#v, want codex and claude", cfg.Accounts)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
}

func TestDefaultPathsUseXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))

	if got, want := DefaultPath(), filepath.Join(dir, "config", "token-burn", "config.toml"); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
	if got, want := DefaultDatabasePath(), filepath.Join(dir, "state", "token-burn", "token-burn.db"); got != want {
		t.Fatalf("DefaultDatabasePath() = %q, want %q", got, want)
	}
	if got, want := DefaultLogPath(), filepath.Join(dir, "state", "token-burn", "token-burn.log"); got != want {
		t.Fatalf("DefaultLogPath() = %q, want %q", got, want)
	}
}
