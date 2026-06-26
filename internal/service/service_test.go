package service

import (
	"strings"
	"testing"
)

func TestLaunchAgentPlist(t *testing.T) {
	plist, err := LaunchAgentPlist(Spec{
		Label:      "dev.durandom.token-burn",
		BinaryPath: "/usr/local/bin/token-burn",
		ConfigPath: "/Users/test/.config/token-burn/config.toml",
		LogPath:    "/Users/test/.local/state/token-burn/token-burn.log",
	})
	if err != nil {
		t.Fatalf("LaunchAgentPlist() error = %v", err)
	}
	text := string(plist)
	for _, want := range []string{
		"<string>dev.durandom.token-burn</string>",
		"<string>/usr/local/bin/token-burn</string>",
		"<string>daemon</string>",
		"<string>--config</string>",
		"<string>/Users/test/.config/token-burn/config.toml</string>",
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"<key>HOME</key>",
		"<key>XDG_CONFIG_HOME</key>",
		"<key>XDG_STATE_HOME</key>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q:\n%s", want, text)
		}
	}
}

func TestLaunchAgentPlistEscapesXML(t *testing.T) {
	plist, err := LaunchAgentPlist(Spec{
		Label:      "dev.durandom.token-burn",
		BinaryPath: "/tmp/token-&-burn",
		LogPath:    "/tmp/token-burn.log",
	})
	if err != nil {
		t.Fatalf("LaunchAgentPlist() error = %v", err)
	}
	if !strings.Contains(string(plist), "/tmp/token-&amp;-burn") {
		t.Fatalf("plist did not escape XML:\n%s", string(plist))
	}
}

func TestLaunchAgentPlistRequiresBinaryPath(t *testing.T) {
	if _, err := LaunchAgentPlist(Spec{}); err == nil {
		t.Fatal("LaunchAgentPlist() error = nil, want error")
	}
}
