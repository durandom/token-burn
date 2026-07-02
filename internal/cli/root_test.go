package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand(BuildInfo{
		Version: "v0.1.0",
		Commit:  "abc123",
		Date:    "2026-06-19T12:00:00Z",
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	want := "token-burn v0.1.0\ncommit: abc123\nbuilt: 2026-06-19T12:00:00Z\n"
	if got := out.String(); got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestUpgradeCommandIsRegistered(t *testing.T) {
	cmd := NewRootCommand(BuildInfo{Version: "v0.1.0"})
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "upgrade" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("upgrade command is not registered")
	}
}

func TestInstallSpecUsesConfiguredDatabasePath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	databasePath := filepath.Join(dir, "custom", "token-burn.db")
	data := []byte(`
poll_interval = "5m"
database_path = "` + filepath.ToSlash(databasePath) + `"

[[accounts]]
provider = "codex"
id = "codex-default"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	spec, err := installSpec("/tmp/token-burn", configPath)
	if err != nil {
		t.Fatalf("installSpec() error = %v", err)
	}
	if spec.DatabasePath != databasePath {
		t.Fatalf("DatabasePath = %q, want %q", spec.DatabasePath, databasePath)
	}
}

func TestParseLookbackDuration(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "24h", want: 24 * time.Hour},
		{raw: "7d", want: 7 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseLookbackDuration(tt.raw)
			if err != nil {
				t.Fatalf("parseLookbackDuration() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("duration = %s, want %s", got, tt.want)
			}
		})
	}
}
