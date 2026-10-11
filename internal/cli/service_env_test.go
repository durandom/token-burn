package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/durandom/token-burn/internal/config"
)

func TestWarnServiceEnvPermissions(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode os.FileMode
		env  map[string]string
		want bool
	}{
		{"private", 0o600, map[string]string{"TOKEN_BURN_TEST": "secret"}, false},
		{"group readable", 0o640, map[string]string{"TOKEN_BURN_TEST": "secret"}, true},
		{"world readable", 0o604, map[string]string{"TOKEN_BURN_TEST": "secret"}, true},
		{"empty section", 0o644, map[string]string{}, true},
		{"no section", 0o644, nil, false},
		{"writable only", 0o622, map[string]string{"TOKEN_BURN_TEST": "secret"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			warnServiceEnvPermissions(&out, path, config.Config{Service: config.ServiceConfig{Env: tt.env}})
			if (out.Len() > 0) != tt.want {
				t.Fatalf("warning=%q", out.String())
			}
			if tt.want {
				want := fmt.Sprintf("warning: config %q has mode %04o; [service.env] secrets are group/world readable\n", path, tt.mode)
				if out.String() != want {
					t.Fatalf("warning = %q, want %q", out.String(), want)
				}
			}
			for _, secret := range []string{"TOKEN_BURN_TEST", "secret\""} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("warning leaked environment")
				}
			}
		})
	}
}

func TestRuntimeCommandsApplyServiceEnv(t *testing.T) {
	for _, name := range []string{"daemon", "once", "status", "history", "forecast", "resets", "otel-test", "otel-backfill"} {
		t.Run(name, func(t *testing.T) {
			const key = "TOKEN_BURN_RUNTIME_TEST"
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			data := fmt.Sprintf("database_path = %q\n[service.env]\n%s = \"runtime-value\"\n[[accounts]]\nprovider = \"codex\"\nid = \"test\"\nauth_file = %q\n", dir, key, filepath.Join(dir, "missing-auth.json"))
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := NewRootCommand(BuildInfo{})
			cmd.SetArgs([]string{"--config", path, name})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cmd.SetContext(ctx)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			_ = cmd.Execute()
			if os.Getenv(key) != "runtime-value" {
				t.Fatal("command did not apply config environment")
			}
		})
	}
}
