package claudeplatform

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const keychainService = "token-burn.claude-platform"

// NormalizeSession accepts only the sessionKey value, never an entire Cookie
// header. This keeps unrelated browser credentials out of the daemon.
func NormalizeSession(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n;\"\\") || strings.HasPrefix(value, "sk-ant-api") || strings.HasPrefix(value, "sk-ant-admin") {
		return "", errors.New("provide only the Console sessionKey cookie value")
	}
	c := http.Cookie{Name: "sessionKey", Value: value}
	if c.Valid() != nil {
		return "", errors.New("invalid Console sessionKey cookie value")
	}
	return value, nil
}

func credentialPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func ReadSession(ctx context.Context, account, file string) (string, error) {
	var data []byte
	if file != "" {
		f, err := os.Open(credentialPath(file))
		if err != nil {
			return "", errors.New("Console session file unavailable")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 2048 {
			return "", errors.New("Console session file must be a private regular file (0600), at most 2048 bytes")
		}
		data = make([]byte, info.Size())
		if _, err := f.ReadAt(data, 0); err != nil {
			return "", errors.New("cannot read Console session file")
		}
	} else {
		if runtime.GOOS != "darwin" {
			return "", errors.New("configure credentials_file for the Console session on this platform")
		}
		var err error
		data, err = exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-s", keychainService, "-a", account).Output()
		if err != nil {
			return "", errors.New("Console session unavailable; import it with token-burn auth claude-platform")
		}
	}
	return NormalizeSession(string(data))
}

// ImportSession sends the secret through stdin, never command arguments.
// A file is an explicit alternative for hosts without a macOS Keychain.
func ImportSession(ctx context.Context, account, file, value string) error {
	session, err := NormalizeSession(value)
	if err != nil {
		return err
	}
	if file != "" {
		path := credentialPath(file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return errors.New("cannot create session directory")
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".console-session-*")
		if err != nil {
			return errors.New("cannot create private session file")
		}
		defer os.Remove(f.Name())
		if _, err = f.WriteString(session); err != nil {
			f.Close()
			return errors.New("cannot write session file")
		}
		if err = f.Close(); err != nil {
			return errors.New("cannot close session file")
		}
		if err = os.Rename(f.Name(), path); err != nil {
			return errors.New("cannot replace session file")
		}
		return nil
	}
	if runtime.GOOS != "darwin" {
		return errors.New("configure credentials_file before importing a Console session on this platform")
	}
	// security's interactive parser has a short line limit; NormalizeSession
	// bounds the value and disallows command delimiters/quoting characters.
	if strings.ContainsAny(account, "\r\n\"\\") {
		return errors.New("invalid keychain account name")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -a \"" + account + "\" -s " + keychainService + " -T /usr/bin/security -w \"" + session + "\"\n")
	if err := cmd.Run(); err != nil {
		return errors.New("cannot import Console session into Keychain")
	}
	// Read back: security -i can exit successfully after a command failure.
	got, err := ReadSession(ctx, account, "")
	if err != nil || got != session {
		return errors.New("Keychain session verification failed")
	}
	return nil
}
