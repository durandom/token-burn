//go:build !darwin

package claude

func readKeychainSecret() (string, error) {
	return "", nil
}
