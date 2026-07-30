package piauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrCredentialNotFound = errors.New("Pi credential not found")

const maxAuthFileBytes = 1 << 20

type OAuthCredential struct {
	Type          string `json:"type"`
	Access        string `json:"access"`
	Refresh       string `json:"refresh"`
	Expires       int64  `json:"expires"`
	AccountID     string `json:"accountId,omitempty"`
	EnterpriseURL string `json:"enterpriseUrl,omitempty"`
}

type Store struct {
	Path string
}

func New(path string) (*Store, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	return &Store{Path: canonical}, nil
}

func CanonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Pi auth path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, lstatErr := os.Lstat(absolute); errors.Is(lstatErr, os.ErrNotExist) {
			return absolute, nil
		}
	}
	return "", fmt.Errorf("resolve Pi auth symlink: %w", err)
}

func ResolvePath(configured string, env func(string) string, homeDir func() (string, error)) string {
	if env == nil {
		env = os.Getenv
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if strings.TrimSpace(configured) != "" {
		return expandHome(configured, homeDir)
	}
	if dir := strings.TrimSpace(env("PI_CODING_AGENT_DIR")); dir != "" {
		return filepath.Join(expandHome(dir, homeDir), "auth.json")
	}
	if home, err := homeDir(); err == nil && home != "" {
		return filepath.Join(home, ".pi", "agent", "auth.json")
	}
	return ""
}

func (s *Store) Read(ctx context.Context, provider string) (OAuthCredential, error) {
	var credential OAuthCredential
	err := withAuthLock(ctx, s.Path, func(_ context.Context, _ *authFileLock) error {
		var readErr error
		credential, _, readErr = readAuthFile(s.Path, provider)
		return readErr
	})
	return credential, err
}

// Modify serializes a provider credential update with Pi. Returning nil from fn
// leaves the file unchanged and returns the current credential.
func (s *Store) Modify(ctx context.Context, provider string, fn func(context.Context, OAuthCredential) (*OAuthCredential, error)) (OAuthCredential, error) {
	var result OAuthCredential
	err := withAuthLock(ctx, s.Path, func(lockCtx context.Context, lock *authFileLock) error {
		current, root, err := readAuthFile(s.Path, provider)
		if err != nil {
			return err
		}
		next, err := fn(lockCtx, current)
		if err != nil {
			return err
		}
		if next == nil {
			result = current
			return nil
		}
		if err := persistCredential(s.Path, provider, root, *next, lock); err != nil {
			return err
		}
		result = *next
		return nil
	})
	return result, err
}

func readAuthFile(path, provider string) (OAuthCredential, map[string]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return OAuthCredential{}, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAuthFileBytes+1))
	if err != nil {
		return OAuthCredential{}, nil, fmt.Errorf("read Pi auth file: %w", err)
	}
	if len(data) > maxAuthFileBytes {
		return OAuthCredential{}, nil, errors.New("Pi auth file exceeds 1 MiB limit")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return OAuthCredential{}, nil, fmt.Errorf("parse Pi auth file: %w", err)
	}
	raw, ok := root[provider]
	if !ok {
		return OAuthCredential{}, root, ErrCredentialNotFound
	}
	var credential OAuthCredential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return OAuthCredential{}, root, fmt.Errorf("parse Pi %s credential: %w", provider, err)
	}
	return credential, root, nil
}

func persistCredential(path, provider string, root map[string]json.RawMessage, credential OAuthCredential, lock *authFileLock) error {
	var fields map[string]json.RawMessage
	if raw := root[provider]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("parse current Pi %s credential", provider)
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	setJSON := func(key string, value any) error {
		data, err := json.Marshal(value)
		if err == nil {
			fields[key] = data
		}
		return err
	}
	for key, value := range map[string]any{
		"type": credential.Type, "access": credential.Access,
		"refresh": credential.Refresh, "expires": credential.Expires,
	} {
		if err := setJSON(key, value); err != nil {
			return err
		}
	}
	if credential.AccountID != "" {
		if err := setJSON("accountId", credential.AccountID); err != nil {
			return err
		}
	}
	if credential.EnterpriseURL != "" {
		if err := setJSON("enterpriseUrl", credential.EnterpriseURL); err != nil {
			return err
		}
	}
	encodedCredential, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	root[provider] = encodedCredential
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(filepath.Dir(path), ".auth.json-*")
	if err != nil {
		return fmt.Errorf("create Pi auth temp file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("chmod Pi auth temp file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write Pi auth temp file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync Pi auth temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close Pi auth temp file: %w", err)
	}
	if err := lock.verifyOwned(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace Pi auth file: %w", err)
	}
	return os.Chmod(path, 0600)
}

func expandHome(path string, homeDir func() (string, error)) string {
	if homeDir == nil {
		return path
	}
	if path == "~" {
		if home, err := homeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := homeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
