//go:build darwin

package claude

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const keychainService = "Claude Code-credentials"

// keychainItemMissing is the `security` exit code for "item not found".
const keychainItemMissing = 44

func readKeychainSecret() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-w", "-s", keychainService).Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == keychainItemMissing {
			return "", nil
		}
	}
	return "", &usageprovider.Error{
		Code:     usageprovider.ErrAuthMissing,
		Provider: id,
		Err:      fmt.Errorf("read Claude Code credentials from macOS Keychain: %w", err),
	}
}

// keychainAccount reads the account attribute of the existing item so a
// write-back updates that item instead of creating a second one. Two items
// sharing a service name would make every later read pick between them
// arbitrarily.
func keychainAccount() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read Claude Code Keychain item attributes: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		const prefix = `"acct"<blob>="`
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.TrimSuffix(strings.TrimPrefix(line, prefix), `"`), nil
	}
	return "", errors.New("Claude Code Keychain item has no account attribute")
}

// writeKeychainSecret updates the existing item in place and verifies the
// result by reading it back.
//
// The secret goes through the -w argument rather than stdin. `security` reads a
// stdin-prompted password through a terminal buffer that silently truncates at
// 128 bytes, which would replace a multi-kilobyte credential store with a
// fragment - destroying the Claude login and every MCP server login stored
// beside it. Passing the value in argv makes it briefly visible in ps, but any
// local process that can read this process's argv runs as the same user and can
// already read the item straight out of the Keychain, so it grants no access an
// attacker would not already have.
//
// The read-back is not belt-and-braces: a truncating write is exactly the
// failure mode that would otherwise corrupt the store without any error.
func writeKeychainSecret(account, secret string) error {
	if err := runKeychainWrite(account, secret); err != nil {
		return err
	}
	stored, err := readKeychainSecret()
	if err != nil {
		return fmt.Errorf("verify Claude Code credentials after Keychain write: %w", err)
	}
	if stored == strings.TrimSpace(secret) {
		return nil
	}
	return fmt.Errorf("Claude Code Keychain write did not round-trip (wrote %d bytes, read back %d)", len(secret), len(stored))
}

func runKeychainWrite(account, secret string) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-a", account, "-s", keychainService, "-w", secret)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update Claude Code credentials in macOS Keychain: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
