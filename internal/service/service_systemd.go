package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/durandom/token-burn/internal/config"
)

// systemdUnitName returns the systemd user unit file name. The launchd label
// (reverse-DNS with dots) is threaded through the cross-platform API for
// compatibility, but Linux uses a single stable unit name per user; the label
// is intentionally not encoded into the file name.
func systemdUnitName(_ string) string {
	return "token-burn.service"
}

// SystemdUnitPath returns the path of the user unit file for label.
func SystemdUnitPath(label string) (string, error) {
	dir, err := systemdUserUnitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, systemdUnitName(label)), nil
}

func systemdUserUnitDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// SystemdUnit renders the [Unit]/[Service]/[Install] file for spec. It is pure
// (no I/O) so it can be unit-tested on any platform.
func SystemdUnit(spec Spec) ([]byte, error) {
	if spec.BinaryPath == "" {
		return nil, errors.New("binary path is required")
	}

	execStart := escapeExec(spec.BinaryPath) + " daemon"
	if spec.ConfigPath != "" {
		execStart += " --config " + escapeExec(spec.ConfigPath)
	}

	var buf bytes.Buffer
	buf.WriteString("[Unit]\n")
	buf.WriteString("Description=token-burn live AI subscription quota polling daemon\n")
	buf.WriteString("Documentation=https://github.com/durandom/token-burn\n")
	buf.WriteString("After=network-online.target\n")
	buf.WriteString("Wants=network-online.target\n\n")

	buf.WriteString("[Service]\n")
	buf.WriteString("Type=simple\n")
	fmt.Fprintf(&buf, "ExecStart=%s\n", execStart)
	buf.WriteString("Restart=on-failure\n")
	buf.WriteString("RestartSec=30\n")
	fmt.Fprintf(&buf, "Environment=PATH=%s\n", launchAgentPathEnv(spec.BinaryPath))
	for _, item := range systemdHomeEnvironment() {
		fmt.Fprintf(&buf, "Environment=%s=%s\n", item.key, item.value)
	}
	// Hardening: the daemon only needs to read provider credentials under
	// $HOME and write the state database.
	buf.WriteString("NoNewPrivileges=true\n")
	buf.WriteString("ProtectSystem=strict\n")
	buf.WriteString("ProtectHome=read-only\n")
	if dirs := stateWriteDirs(spec); len(dirs) > 0 {
		fmt.Fprintf(&buf, "ReadWritePaths=%s\n", strings.Join(dirs, " "))
	}
	buf.WriteString("PrivateTmp=true\n")
	buf.WriteString("ProtectKernelTunables=true\n")
	buf.WriteString("ProtectControlGroups=true\n")
	buf.WriteString("RestrictNamespaces=true\n\n")

	buf.WriteString("[Install]\n")
	buf.WriteString("WantedBy=default.target\n")
	return buf.Bytes(), nil
}

// stateWriteDirs returns the directories the daemon must be allowed to write
// under ProtectSystem=strict + ProtectHome=read-only: the log directory and the
// database directory. They are usually identical, but the database location is
// independently configurable, so both are whitelisted (deduplicated).
func stateWriteDirs(spec Spec) []string {
	logPath := spec.LogPath
	if logPath == "" {
		logPath = config.DefaultLogPath()
	}
	dbPath := spec.DatabasePath
	if dbPath == "" {
		dbPath = config.DefaultDatabasePath()
	}

	seen := map[string]bool{}
	var dirs []string
	for _, p := range []string{logPath, dbPath} {
		if p == "" {
			continue
		}
		dir := filepath.Dir(p)
		if dir == "" || dir == "." || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

func systemdHomeEnvironment() []launchAgentEnvVar {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return []launchAgentEnvVar{
			{key: "HOME", value: home},
			{key: "XDG_CONFIG_HOME", value: filepath.Join(home, ".config")},
			{key: "XDG_STATE_HOME", value: filepath.Join(home, ".local", "state")},
		}
	}
	return nil
}

func installSystemdUnit(ctx context.Context, spec Spec) error {
	if spec.Label == "" {
		spec.Label = DefaultLabel
	}
	path, err := SystemdUnitPath(spec.Label)
	if err != nil {
		return err
	}
	unit, err := SystemdUnit(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create systemd user unit directory: %w", err)
	}
	for _, dir := range stateWriteDirs(spec) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create state directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, unit, 0o600); err != nil {
		return fmt.Errorf("write systemd user unit: %w", err)
	}

	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl(ctx, "enable", "--now", systemdUnitName(spec.Label)); err != nil {
		return err
	}
	// Best effort: keep the daemon running across logout. Enabling linger can
	// require polkit authorization, so a failure here is a warning, not fatal.
	if err := runLoginctl(ctx, "enable-linger"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enable linger (daemon will stop at logout): %v\n", err)
	}
	return nil
}

func uninstallSystemdUnit(ctx context.Context, label string) error {
	if label == "" {
		label = DefaultLabel
	}
	path, err := SystemdUnitPath(label)
	if err != nil {
		return err
	}
	// Ignore errors: the unit may already be stopped or absent.
	_ = runSystemctl(ctx, "disable", "--now", systemdUnitName(label))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove systemd user unit: %w", err)
	}
	_ = runSystemctl(ctx, "daemon-reload")
	return nil
}

func systemdUnitStatus(ctx context.Context, label string) (Status, error) {
	if label == "" {
		label = DefaultLabel
	}
	path, err := SystemdUnitPath(label)
	if err != nil {
		return Status{}, err
	}
	status := Status{Platform: "linux", Path: path}
	if _, err := os.Stat(path); err == nil {
		status.Installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return status, err
	}

	// is-active exits non-zero when the unit is not running; treat that as
	// "not loaded" rather than an error.
	out, err := systemctlOutput(ctx, "is-active", systemdUnitName(label))
	active := strings.TrimSpace(out)
	status.Loaded = err == nil && active == "active"
	if active != "" {
		status.Message = active
	} else if err != nil {
		status.Message = err.Error()
	}
	return status, nil
}

func runSystemctl(ctx context.Context, args ...string) error {
	full := append([]string{"--user"}, args...)
	cmd := exec.CommandContext(ctx, "systemctl", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(full, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func systemctlOutput(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--user"}, args...)
	cmd := exec.CommandContext(ctx, "systemctl", full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runLoginctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "loginctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("loginctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// escapeExec quotes a path for a systemd ExecStart line if it contains spaces.
func escapeExec(value string) string {
	if strings.ContainsAny(value, " \t") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}
