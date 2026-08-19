//go:build !darwin

package claude

import "errors"

func readKeychainSecret() (string, error) {
	return "", nil
}

func keychainAccount() (string, error) {
	return "", errors.New("macOS Keychain is not available on this platform")
}

func writeKeychainSecret(string, string) error {
	return errors.New("macOS Keychain is not available on this platform")
}
