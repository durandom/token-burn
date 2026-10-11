package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRewritesLegacyEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name    string
		path    func(string) (string, error)
		install func(context.Context, Spec) error
	}{
		{"launchd", LaunchAgentPath, installLaunchAgent},
		{"systemd", SystemdUnitPath, installSystemdUnit},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"launchctl", "systemctl", "loginctl"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			path, err := tt.path(DefaultLabel)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("TOKEN_BURN_OLD_SECRET=legacy-secret\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			spec := Spec{BinaryPath: filepath.Join(bin, "token-burn"), LogPath: filepath.Join(dir, "state", "daemon.log"), DatabasePath: filepath.Join(dir, "state", "usage.db")}
			if err := tt.install(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, legacy := range []string{"TOKEN_BURN_OLD_SECRET", "legacy-secret"} {
				if strings.Contains(string(data), legacy) {
					t.Fatal("install preserved legacy environment")
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %04o", info.Mode().Perm())
			}
		})
	}
}
