package service

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	unit, err := SystemdUnit(Spec{
		Label:        "dev.durandom.token-burn",
		BinaryPath:   "/home/test/.local/bin/token-burn",
		ConfigPath:   "/home/test/.config/token-burn/config.toml",
		LogPath:      "/home/test/.local/state/token-burn/token-burn.log",
		DatabasePath: "/home/test/.local/state/token-burn/token-burn.db",
	})
	if err != nil {
		t.Fatalf("SystemdUnit() error = %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"Type=simple",
		"ExecStart=/home/test/.local/bin/token-burn daemon --config /home/test/.config/token-burn/config.toml",
		"Restart=on-failure",
		"WantedBy=default.target",
		"ProtectSystem=strict",
		"ProtectHome=read-only",
		"ReadWritePaths=/home/test/.local/state/token-burn\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit missing %q:\n%s", want, text)
		}
	}
}

func TestSystemdUnitWhitelistsSeparateDatabaseDir(t *testing.T) {
	unit, err := SystemdUnit(Spec{
		BinaryPath:   "/usr/local/bin/token-burn",
		LogPath:      "/home/test/.local/state/token-burn/token-burn.log",
		DatabasePath: "/var/lib/token-burn/token-burn.db",
	})
	if err != nil {
		t.Fatalf("SystemdUnit() error = %v", err)
	}
	want := "ReadWritePaths=/home/test/.local/state/token-burn /var/lib/token-burn\n"
	if !strings.Contains(string(unit), want) {
		t.Fatalf("unit missing %q:\n%s", want, string(unit))
	}
}

func TestSystemdUnitWithoutConfig(t *testing.T) {
	unit, err := SystemdUnit(Spec{
		BinaryPath: "/usr/local/bin/token-burn",
		LogPath:    "/tmp/token-burn.log",
	})
	if err != nil {
		t.Fatalf("SystemdUnit() error = %v", err)
	}
	text := string(unit)
	if !strings.Contains(text, "ExecStart=/usr/local/bin/token-burn daemon\n") {
		t.Fatalf("unit ExecStart should omit --config:\n%s", text)
	}
	if strings.Contains(text, "--config") {
		t.Fatalf("unit should not contain --config when ConfigPath is empty:\n%s", text)
	}
}

func TestSystemdUnitQuotesBinaryWithSpaces(t *testing.T) {
	unit, err := SystemdUnit(Spec{
		BinaryPath: "/opt/token burn/token-burn",
		LogPath:    "/tmp/token-burn.log",
	})
	if err != nil {
		t.Fatalf("SystemdUnit() error = %v", err)
	}
	if !strings.Contains(string(unit), `ExecStart="/opt/token burn/token-burn" daemon`) {
		t.Fatalf("unit did not quote binary path with spaces:\n%s", string(unit))
	}
}

func TestSystemdUnitRequiresBinaryPath(t *testing.T) {
	if _, err := SystemdUnit(Spec{}); err == nil {
		t.Fatal("SystemdUnit() error = nil, want error")
	}
}

func TestSystemdUnitName(t *testing.T) {
	if got := systemdUnitName(""); got != "token-burn.service" {
		t.Fatalf("systemdUnitName(\"\") = %q, want token-burn.service", got)
	}
	if got := systemdUnitName("dev.durandom.token-burn"); got != "token-burn.service" {
		t.Fatalf("systemdUnitName(label) = %q, want token-burn.service", got)
	}
}
