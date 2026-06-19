package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/durandom/token-burn/internal/config"
)

const DefaultLabel = "dev.durandom.token-burn"

type Spec struct {
	Label      string
	BinaryPath string
	ConfigPath string
	LogPath    string
}

type Status struct {
	Platform  string
	Installed bool
	Loaded    bool
	Path      string
	Message   string
}

func DefaultSpec(binaryPath, configPath string) (Spec, error) {
	var err error
	if binaryPath == "" {
		binaryPath, err = os.Executable()
		if err != nil {
			return Spec{}, fmt.Errorf("resolve executable path: %w", err)
		}
	}
	return Spec{
		Label:      DefaultLabel,
		BinaryPath: binaryPath,
		ConfigPath: configPath,
		LogPath:    config.DefaultLogPath(),
	}, nil
}

func Install(ctx context.Context, spec Spec) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchAgent(ctx, spec)
	default:
		return fmt.Errorf("service install is not implemented for %s yet", runtime.GOOS)
	}
}

func Uninstall(ctx context.Context, label string) error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchAgent(ctx, label)
	default:
		return fmt.Errorf("service uninstall is not implemented for %s yet", runtime.GOOS)
	}
}

func ServiceStatus(ctx context.Context, label string) (Status, error) {
	switch runtime.GOOS {
	case "darwin":
		return launchAgentStatus(ctx, label)
	default:
		return Status{Platform: runtime.GOOS, Message: "service status is not implemented for this platform yet"}, nil
	}
}

func LaunchAgentPath(label string) (string, error) {
	if label == "" {
		label = DefaultLabel
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func LaunchAgentPlist(spec Spec) ([]byte, error) {
	if spec.Label == "" {
		spec.Label = DefaultLabel
	}
	if spec.BinaryPath == "" {
		return nil, errors.New("binary path is required")
	}
	if spec.LogPath == "" {
		spec.LogPath = config.DefaultLogPath()
	}

	args := []string{spec.BinaryPath, "daemon"}
	if spec.ConfigPath != "" {
		args = append(args, "--config", spec.ConfigPath)
	}

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0">` + "\n")
	buf.WriteString("<dict>\n")
	writeKeyString(&buf, "Label", spec.Label)
	buf.WriteString("  <key>ProgramArguments</key>\n")
	buf.WriteString("  <array>\n")
	for _, arg := range args {
		buf.WriteString("    <string>")
		buf.WriteString(escapeXML(arg))
		buf.WriteString("</string>\n")
	}
	buf.WriteString("  </array>\n")
	writeKeyBool(&buf, "RunAtLoad", true)
	writeKeyBool(&buf, "KeepAlive", true)
	writeKeyString(&buf, "StandardOutPath", spec.LogPath)
	writeKeyString(&buf, "StandardErrorPath", spec.LogPath)
	buf.WriteString("</dict>\n")
	buf.WriteString("</plist>\n")
	return buf.Bytes(), nil
}

func installLaunchAgent(ctx context.Context, spec Spec) error {
	if spec.Label == "" {
		spec.Label = DefaultLabel
	}
	path, err := LaunchAgentPath(spec.Label)
	if err != nil {
		return err
	}
	plist, err := LaunchAgentPlist(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	if err := os.WriteFile(path, plist, 0600); err != nil {
		return fmt.Errorf("write LaunchAgent plist: %w", err)
	}

	_ = runLaunchctl(ctx, "bootout", launchDomain(), path)
	if err := runLaunchctl(ctx, "bootstrap", launchDomain(), path); err != nil {
		return err
	}
	return runLaunchctl(ctx, "enable", launchDomain()+"/"+spec.Label)
}

func uninstallLaunchAgent(ctx context.Context, label string) error {
	if label == "" {
		label = DefaultLabel
	}
	path, err := LaunchAgentPath(label)
	if err != nil {
		return err
	}
	_ = runLaunchctl(ctx, "bootout", launchDomain(), path)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove LaunchAgent plist: %w", err)
	}
	return nil
}

func launchAgentStatus(ctx context.Context, label string) (Status, error) {
	if label == "" {
		label = DefaultLabel
	}
	path, err := LaunchAgentPath(label)
	if err != nil {
		return Status{}, err
	}
	status := Status{Platform: "darwin", Path: path}
	if _, err := os.Stat(path); err == nil {
		status.Installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return status, err
	}

	err = runLaunchctl(ctx, "print", launchDomain()+"/"+label)
	status.Loaded = err == nil
	if err != nil {
		status.Message = err.Error()
	}
	return status, nil
}

func launchDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func runLaunchctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeKeyString(buf *bytes.Buffer, key, value string) {
	buf.WriteString("  <key>")
	buf.WriteString(escapeXML(key))
	buf.WriteString("</key>\n")
	buf.WriteString("  <string>")
	buf.WriteString(escapeXML(value))
	buf.WriteString("</string>\n")
}

func writeKeyBool(buf *bytes.Buffer, key string, value bool) {
	buf.WriteString("  <key>")
	buf.WriteString(escapeXML(key))
	buf.WriteString("</key>\n")
	if value {
		buf.WriteString("  <true/>\n")
		return
	}
	buf.WriteString("  <false/>\n")
}

func escapeXML(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "'", "&apos;")
	return value
}
