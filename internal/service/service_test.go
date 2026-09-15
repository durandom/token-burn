package service

import (
	"os"
	"path/filepath"
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

func TestAbsolutePathCanonicalizesRelativePathsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	// Binaries often live behind a symlinked parent, e.g. a "bin" dir
	// under a symlinked home. The symlink must resolve to the real
	// target so the service unit stays valid.
	if err := os.Symlink(filepath.Join(dir, "bin"), filepath.Join(dir, "link-to-bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "bin", "token-burn")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := AbsolutePath(filepath.Join(dir, "link-to-bin", "token-burn"))
	if err != nil {
		t.Fatalf("AbsolutePath() error = %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("AbsolutePath() = %q, want absolute", resolved)
	}
	// macOS prefixes such as /var/folders are themselves symlinks, so
	// compare against the fully resolved target instead of the literal
	// path this test constructed.
	want, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("AbsolutePath() = %q, want symlink-resolved %q", resolved, want)
	}

	missing, err := AbsolutePath(filepath.Join(dir, "missing", "token-burn"))
	if err != nil {
		t.Fatalf("AbsolutePath(missing) error = %v", err)
	}
	if !filepath.IsAbs(missing) {
		t.Fatalf("AbsolutePath(missing) = %q, want absolute", missing)
	}
}

func TestLaunchAgentPlistExtraEnvSorted(t *testing.T) {
	plist, err := LaunchAgentPlist(Spec{
		Label:      "dev.durandom.token-burn",
		BinaryPath: "/usr/local/bin/token-burn",
		ExtraEnv: map[string]string{
			"TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET": "GOCSPX-secret",
			"TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID":     "107100-test.apps.googleusercontent.com",
		},
	})
	if err != nil {
		t.Fatalf("LaunchAgentPlist() error = %v", err)
	}
	text := string(plist)
	idIdx := strings.Index(text, "<key>TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID</key>")
	secretIdx := strings.Index(text, "<key>TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET</key>")
	if idIdx < 0 || secretIdx < 0 {
		t.Fatalf("plist missing extra env keys:\n%s", text)
	}
	if idIdx > secretIdx {
		t.Fatalf("extra env keys not sorted: id at %d after secret at %d", idIdx, secretIdx)
	}
	if !strings.Contains(text, "<string>GOCSPX-secret</string>") {
		t.Fatalf("plist missing extra env value:\n%s", text)
	}
	// Extra env must come after the built-in HOME/XDG entries so the
	// built-ins stay easy to find in diffs.
	homeIdx := strings.Index(text, "<key>XDG_STATE_HOME</key>")
	if homeIdx > idIdx || homeIdx > secretIdx {
		t.Fatalf("extra env keys must follow the built-in environment:\n%s", text)
	}
}

func TestLaunchAgentPlistRejectsReservedExtraEnv(t *testing.T) {
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		if _, err := LaunchAgentPlist(Spec{BinaryPath: "/usr/local/bin/token-burn", ExtraEnv: map[string]string{key: "x"}}); err == nil {
			t.Fatalf("LaunchAgentPlist() with reserved key %q succeeded, want error", key)
		}
	}
}

func TestLaunchAgentPlistRejectsEmptyExtraEnvValue(t *testing.T) {
	if _, err := LaunchAgentPlist(Spec{BinaryPath: "/usr/local/bin/token-burn", ExtraEnv: map[string]string{"TOKEN_BURN_X": " "}}); err == nil {
		t.Fatal("LaunchAgentPlist() with blank value succeeded, want error")
	}
}
