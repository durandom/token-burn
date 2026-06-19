//go:build darwin

package claude

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func readKeychainSecret() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-w", "-s", "Claude Code-credentials").Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 44 {
			return "", nil
		}
	}
	return "", &usageprovider.Error{
		Code:     usageprovider.ErrAuthMissing,
		Provider: id,
		Err:      fmt.Errorf("read Claude Code credentials from macOS Keychain: %w", err),
	}
}
