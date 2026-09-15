package config

import (
	"os"
	"path/filepath"
	"strings"
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

[otel.read]
mode = "auto"
endpoint = "https://observe.example.test"
organization = "token-burn"
username = "reader@example.test"
password = "config-secret"
username_env = "TEST_O2_USER"
password_env = "TEST_O2_PASSWORD"
lookback = "12h"

[tui]
theme = "light"

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
	if cfg.OTel.Read.Mode != "auto" || cfg.OTel.Read.Endpoint != "https://observe.example.test" || cfg.OTel.Read.Organization != "token-burn" || cfg.OTel.Read.Lookback != 12*time.Hour {
		t.Fatalf("OTel.Read = %#v", cfg.OTel.Read)
	}
	if cfg.TUI.Theme != "light" {
		t.Fatalf("TUI.Theme = %q, want light", cfg.TUI.Theme)
	}
	if cfg.OTel.Read.Username != "reader@example.test" || cfg.OTel.Read.Password != "config-secret" {
		t.Fatalf("OTel.Read credentials were not loaded")
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
	if len(cfg.Accounts) != 6 {
		t.Fatalf("default account count = %d, want 6", len(cfg.Accounts))
	}
	if cfg.Accounts[0].Provider != "codex" || cfg.Accounts[1].Provider != "claude" || cfg.Accounts[2].Provider != "copilot" || cfg.Accounts[3].Provider != "antigravity" || cfg.Accounts[4].Provider != "xai" || cfg.Accounts[5].Provider != "zai" {
		t.Fatalf("default accounts = %#v, want codex, claude, copilot, antigravity, xai, and zai", cfg.Accounts)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default config was not written: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read default config: %v", err)
	}
	if !strings.Contains(string(data), "provider = \"xai\"") {
		t.Fatalf("default config missing xai account: %s", data)
	}
	if !strings.Contains(string(data), "provider = \"zai\"") {
		t.Fatalf("default config missing zai account: %s", data)
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

func TestLoadServiceEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
[service.env]
TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID = "107100-test.apps.googleusercontent.com"
TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET = "GOCSPX-test"

[[accounts]]
provider = "codex"
id = "codex-default"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]string{
		"TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID":     "107100-test.apps.googleusercontent.com",
		"TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET": "GOCSPX-test",
	}
	if len(cfg.Service.Env) != len(want) {
		t.Fatalf("Service.Env = %#v, want %#v", cfg.Service.Env, want)
	}
	for key, value := range want {
		if cfg.Service.Env[key] != value {
			t.Fatalf("Service.Env[%q] = %q, want %q", key, cfg.Service.Env[key], value)
		}
	}
}

func TestLoadServiceEnvRejectsInvalidKeys(t *testing.T) {
	for name, key := range map[string]string{
		"lowercase":     "token_burn_secret",
		"leading-digit": "9TOKEN",
		"shell-meta":    "PATH${HOME}",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			data := []byte("[service.env]\n" + key + ` = "value"` + "\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("Load() with key %q succeeded, want error", key)
			}
		})
	}
}

func TestLoadServiceEnvRejectsEmptyValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[service.env]\nTOKEN_BURN_X = \"\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() with empty value succeeded, want error")
	}
}

func TestLoadWithoutServiceEnvSectionKeepsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("poll_interval = \"5m\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Service.Env != nil && len(cfg.Service.Env) != 0 {
		t.Fatalf("Service.Env = %#v, want empty", cfg.Service.Env)
	}
}
