package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	DefaultPollInterval = 5 * time.Minute
	DefaultHTTPTimeout  = 15 * time.Second
	appDir              = "token-burn"
	configFile          = "config.toml"
	databaseFile        = "token-burn.db"
)

type Config struct {
	PollInterval time.Duration
	HTTPTimeout  time.Duration
	DatabasePath string
	OTel         OTelConfig
	Accounts     []Account
}

type OTelConfig struct {
	Enabled        bool
	Endpoint       string
	Protocol       string
	ExportInterval time.Duration
	Read           OTelReadConfig
}

type OTelReadConfig struct {
	Mode         string
	Endpoint     string
	Organization string
	UsernameEnv  string
	PasswordEnv  string
	Lookback     time.Duration
}

type Account struct {
	Provider          string `toml:"provider"`
	ID                string `toml:"id"`
	ProviderAccountID string `toml:"provider_account_id"`
	AuthFile          string `toml:"auth_file"`
	CredentialsFile   string `toml:"credentials_file"`
}

type fileConfig struct {
	PollInterval string    `toml:"poll_interval"`
	HTTPTimeout  string    `toml:"http_timeout"`
	DatabasePath string    `toml:"database_path"`
	OTel         fileOTel  `toml:"otel"`
	Accounts     []Account `toml:"accounts"`
}

type fileOTel struct {
	Enabled        bool         `toml:"enabled"`
	Endpoint       string       `toml:"endpoint"`
	Protocol       string       `toml:"protocol"`
	ExportInterval string       `toml:"export_interval"`
	Read           fileOTelRead `toml:"read"`
}

type fileOTelRead struct {
	Mode         string `toml:"mode"`
	Endpoint     string `toml:"endpoint"`
	Organization string `toml:"organization"`
	UsernameEnv  string `toml:"username_env"`
	PasswordEnv  string `toml:"password_env"`
	Lookback     string `toml:"lookback"`
}

func Default() Config {
	return Config{
		PollInterval: DefaultPollInterval,
		HTTPTimeout:  DefaultHTTPTimeout,
		DatabasePath: DefaultDatabasePath(),
		OTel: OTelConfig{
			Endpoint:       "http://localhost:4318",
			Protocol:       "http/protobuf",
			ExportInterval: 60 * time.Second,
			Read: OTelReadConfig{
				Mode: "sqlite", Organization: "default",
				UsernameEnv: "OPENOBSERVE_USER", PasswordEnv: "OPENOBSERVE_PASSWORD",
				Lookback: 24 * time.Hour,
			},
		},
		Accounts: []Account{
			{Provider: "codex", ID: "codex-default"},
			{Provider: "claude", ID: "claude-default"},
			{Provider: "copilot", ID: "copilot-default"},
			{Provider: "antigravity", ID: "antigravity-default"},
			{Provider: "xai", ID: "xai-default"},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
	}

	expanded, err := ExpandPath(path)
	if err != nil {
		return Config{}, err
	}

	data, err := os.ReadFile(expanded)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, writeDefaultFile(expanded)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var fc fileConfig
	if err := toml.Unmarshal(data, &fc); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if fc.PollInterval != "" {
		cfg.PollInterval, err = time.ParseDuration(fc.PollInterval)
		if err != nil {
			return Config{}, fmt.Errorf("parse poll_interval: %w", err)
		}
	}
	if fc.HTTPTimeout != "" {
		cfg.HTTPTimeout, err = time.ParseDuration(fc.HTTPTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("parse http_timeout: %w", err)
		}
	}
	if fc.DatabasePath != "" {
		cfg.DatabasePath = fc.DatabasePath
	}
	cfg.DatabasePath, err = ExpandPath(cfg.DatabasePath)
	if err != nil {
		return Config{}, err
	}

	cfg.OTel.Enabled = fc.OTel.Enabled
	if fc.OTel.Endpoint != "" {
		cfg.OTel.Endpoint = fc.OTel.Endpoint
	}
	if fc.OTel.Protocol != "" {
		cfg.OTel.Protocol = fc.OTel.Protocol
	}
	if fc.OTel.ExportInterval != "" {
		cfg.OTel.ExportInterval, err = time.ParseDuration(fc.OTel.ExportInterval)
		if err != nil {
			return Config{}, fmt.Errorf("parse otel.export_interval: %w", err)
		}
	}
	if fc.OTel.Read.Mode != "" {
		cfg.OTel.Read.Mode = fc.OTel.Read.Mode
	}
	switch cfg.OTel.Read.Mode {
	case "sqlite", "auto", "openobserve":
	default:
		return Config{}, fmt.Errorf("invalid otel.read.mode %q; want sqlite, auto, or openobserve", cfg.OTel.Read.Mode)
	}
	if fc.OTel.Read.Endpoint != "" {
		cfg.OTel.Read.Endpoint = fc.OTel.Read.Endpoint
	}
	if fc.OTel.Read.Organization != "" {
		cfg.OTel.Read.Organization = fc.OTel.Read.Organization
	}
	if fc.OTel.Read.UsernameEnv != "" {
		cfg.OTel.Read.UsernameEnv = fc.OTel.Read.UsernameEnv
	}
	if fc.OTel.Read.PasswordEnv != "" {
		cfg.OTel.Read.PasswordEnv = fc.OTel.Read.PasswordEnv
	}
	if fc.OTel.Read.Lookback != "" {
		cfg.OTel.Read.Lookback, err = time.ParseDuration(fc.OTel.Read.Lookback)
		if err != nil {
			return Config{}, fmt.Errorf("parse otel.read.lookback: %w", err)
		}
		if cfg.OTel.Read.Lookback <= 0 {
			return Config{}, errors.New("otel.read.lookback must be positive")
		}
	}
	if len(fc.Accounts) > 0 {
		cfg.Accounts = fc.Accounts
	}

	return cfg, nil
}

func DefaultPath() string {
	return filepath.Join(XDGConfigHome(), appDir, configFile)
}

func DefaultDatabasePath() string {
	return filepath.Join(XDGStateHome(), appDir, databaseFile)
}

func DefaultLogPath() string {
	return filepath.Join(XDGStateHome(), appDir, "token-burn.log")
}

func XDGConfigHome() string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return filepath.Join("~", ".config")
}

func XDGStateHome() string {
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state")
	}
	return filepath.Join("~", ".local", "state")
}

func writeDefaultFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(defaultFileContents()), 0600); err != nil {
		return fmt.Errorf("write default config: %w", err)
	}
	return nil
}

func ExpandPath(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	if len(path) > 1 && path[1] != '/' {
		return "", fmt.Errorf("unsupported home path %q", path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if len(path) == 1 {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func defaultFileContents() string {
	return fmt.Sprintf(`poll_interval = "5m"
http_timeout = "15s"
database_path = %q

[otel]
enabled = false
endpoint = "http://localhost:4318"
protocol = "http/protobuf"
export_interval = "60s"

[otel.read]
mode = "sqlite"
endpoint = ""
organization = "default"
username_env = "OPENOBSERVE_USER"
password_env = "OPENOBSERVE_PASSWORD"
lookback = "24h"

[[accounts]]
provider = "codex"
id = "codex-default"

[[accounts]]
provider = "claude"
id = "claude-default"

[[accounts]]
provider = "copilot"
id = "copilot-default"

[[accounts]]
provider = "antigravity"
id = "antigravity-default"

[[accounts]]
provider = "xai"
id = "xai-default"
`, DefaultDatabasePath())
}
