package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

// fallbackNow is the shared time base for fallback tests.
var fallbackNow = time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)

// writeHomeCredentials plants a Claude credentials file inside a fake home
// directory, as Claude Code would.
func writeHomeCredentials(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create .claude dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write home credentials: %v", err)
	}
	return path
}

func expiredCredJSON(t *testing.T, access, refresh string) string {
	t.Helper()
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","expiresAt":` +
		strconv.FormatInt(fallbackNow.Add(-time.Hour).UnixMilli(), 10) + `}}`
}

func validCredJSON(t *testing.T, access string) string {
	t.Helper()
	return `{"claudeAiOauth":{"accessToken":"` + access + `","expiresAt":` +
		strconv.FormatInt(fallbackNow.Add(2*time.Hour).UnixMilli(), 10) + `}}`
}

func usageAccepting(t *testing.T, token string, calls *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1,"resets_at":"2026-06-19T12:00:00Z"}}`))
	}))
}

func usageRejecting(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Header.Get("Authorization"))
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
}

func refreshRejectingGrant(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
}

// TestFetchFallsThroughStaleFileToKeychain is the stale-file shadowing bug: an
// expired credentials file whose refresh token is already consumed must not
// block the healthy keychain login Claude Code keeps rotating.
func TestFetchFallsThroughStaleFileToKeychain(t *testing.T) {
	home := t.TempDir()
	stalePath := writeHomeCredentials(t, home, expiredCredJSON(t, "stale-access", "dead-refresh"))
	before, _ := os.ReadFile(stalePath)

	var usageCalls []string
	usage := usageAccepting(t, "kc-fresh", &usageCalls)
	defer usage.Close()
	refreshCalls := 0
	refresh := refreshRejectingGrant(t, &refreshCalls)
	defer refresh.Close()

	snap, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return fallbackNow },
		HomeDir:    func() (string, error) { return home, nil },
		Env:        func(string) string { return "" },
		KeychainToken: func() (string, error) {
			return validCredJSON(t, "kc-fresh"), nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(snap.Windows) == 0 {
		t.Fatal("snapshot has no windows")
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1 (only the stale file refresh)", refreshCalls)
	}
	// The stale login is expired, so its refresh happens before any usage
	// call: the only usage request belongs to the keychain login.
	if len(usageCalls) != 1 || usageCalls[0] != "Bearer kc-fresh" {
		t.Fatalf("usage calls = %v, want the keychain token to win", usageCalls)
	}
	after, _ := os.ReadFile(stalePath)
	if string(before) != string(after) {
		t.Fatal("stale credentials file was modified; fall-through must leave it alone")
	}
}

// TestFetchFreshFileNeverReadsKeychain keeps the happy path hermetic and cheap:
// a working file login must not trigger a keychain read at all.
func TestFetchFreshFileNeverReadsKeychain(t *testing.T) {
	home := t.TempDir()
	writeHomeCredentials(t, home, validCredJSON(t, "file-fresh"))

	keychainReads := 0
	var usageCalls []string
	usage := usageAccepting(t, "file-fresh", &usageCalls)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL: usage.URL,
		Now:     func() time.Time { return fallbackNow },
		HomeDir: func() (string, error) { return home, nil },
		Env:     func(string) string { return "" },
		KeychainToken: func() (string, error) {
			keychainReads++
			return validCredJSON(t, "kc"), nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if keychainReads != 0 {
		t.Fatalf("keychain reads = %d, want 0 while the file login works", keychainReads)
	}
}

// TestFetchTriesFreshestFileFirst proves the expiry ordering: the home file is
// discovered after the configured credentials file, but its newer expiry must
// put it ahead of a staler configured login.
func TestFetchTriesFreshestFileFirst(t *testing.T) {
	home := t.TempDir()
	writeHomeCredentials(t, home, validCredJSON(t, "home-fresh"))
	configured := writeCredentials(t, expiredCredJSON(t, "configured-stale", "dead-refresh"))

	var usageCalls []string
	usage := usageAccepting(t, "home-fresh", &usageCalls)
	defer usage.Close()
	refreshCalls := 0
	refresh := refreshRejectingGrant(t, &refreshCalls)
	defer refresh.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return fallbackNow },
		HomeDir:    func() (string, error) { return home, nil },
		Env:        func(string) string { return "" },
		KeychainToken: func() (string, error) {
			t.Error("keychain read during file-only ordering test")
			return "", errors.New("no keychain")
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: configured})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usageCalls) != 1 || usageCalls[0] != "Bearer home-fresh" {
		t.Fatalf("usage calls = %v, want exactly the freshest file login", usageCalls)
	}
	if refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0 (freshest candidate wins outright)", refreshCalls)
	}
}

// TestFetchFallsThroughUnrefreshableLoginToKeychain covers a login without
// a refresh token (flat access-token shape): a 401 must advance to the
// keychain instead of ending the poll.
func TestFetchFallsThroughUnrefreshableLoginToKeychain(t *testing.T) {
	home := t.TempDir()
	writeHomeCredentials(t, home, `{"access_token":"flat-dead"}`)

	var usageCalls []string
	usage := usageAccepting(t, "kc-after-flat", &usageCalls)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL: usage.URL,
		Now:     func() time.Time { return fallbackNow },
		HomeDir: func() (string, error) { return home, nil },
		Env:     func(string) string { return "" },
		KeychainToken: func() (string, error) {
			return validCredJSON(t, "kc-after-flat"), nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(usageCalls) != 2 || usageCalls[0] != "Bearer flat-dead" || usageCalls[1] != "Bearer kc-after-flat" {
		t.Fatalf("usage calls = %v, want the rejected flat login then the keychain login", usageCalls)
	}
}

// TestFetchAllSourcesDeadNamesEverySource: when everything is expired, the
// error must name every source that was tried without leaking token material.
func TestFetchAllSourcesDeadNamesEverySource(t *testing.T) {
	home := t.TempDir()
	stalePath := writeHomeCredentials(t, home, expiredCredJSON(t, "stale", "dead-refresh"))

	var usageCalls []string
	usage := usageRejecting(t, &usageCalls)
	defer usage.Close()
	refreshCalls := 0
	refresh := refreshRejectingGrant(t, &refreshCalls)
	defer refresh.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return fallbackNow },
		HomeDir:    func() (string, error) { return home, nil },
		Env:        func(string) string { return "" },
		KeychainToken: func() (string, error) {
			return "kc-also-dead", nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthExpired {
		t.Fatalf("error = %v, want auth_expired", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "file "+stalePath) {
		t.Fatalf("error does not name the credentials file: %v", msg)
	}
	if !strings.Contains(msg, "macOS keychain") {
		t.Fatalf("error does not name the keychain source: %v", msg)
	}
	if strings.Contains(msg, "dead-refresh") || strings.Contains(msg, "kc-also-dead") {
		t.Fatalf("error leaks token material: %v", msg)
	}
}

// TestFetchEnvTokenFailureDoesNotFallThrough pins the override semantics:
// CLAUDE_CODE_OAUTH_TOKEN is explicit, so when it is dead the poll fails
// instead of silently switching to the regular sources.
func TestFetchEnvTokenFailureDoesNotFallThrough(t *testing.T) {
	home := t.TempDir()
	writeHomeCredentials(t, home, validCredJSON(t, "file-fresh"))

	var usageCalls []string
	usage := usageRejecting(t, &usageCalls)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL: usage.URL,
		Now:     func() time.Time { return fallbackNow },
		HomeDir: func() (string, error) { return home, nil },
		Env: func(key string) string {
			if key == "CLAUDE_CODE_OAUTH_TOKEN" {
				return "env-dead"
			}
			return ""
		},
		KeychainToken: func() (string, error) {
			t.Error("keychain read despite env override")
			return "", errors.New("no keychain")
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthExpired {
		t.Fatalf("error = %v, want auth_expired", err)
	}
	if len(usageCalls) != 1 || usageCalls[0] != "Bearer env-dead" {
		t.Fatalf("usage calls = %v, want exactly the env token attempt", usageCalls)
	}
}

// TestFetchRotatesKeychainLoginBackToKeychain: when the keychain is the
// winning source, a due refresh must be persisted into the keychain item.
func TestFetchRotatesKeychainLoginBackToKeychain(t *testing.T) {
	home := t.TempDir() // no credentials file at all

	var usageCalls []string
	usage := usageAccepting(t, "kc-rotated", &usageCalls)
	defer usage.Close()
	refresh := refreshServer(t, "kc-refresh", "kc-rotated", "kc-refresh-2", new(int))
	defer refresh.Close()

	writes := 0
	var written string
	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return fallbackNow },
		HomeDir:    func() (string, error) { return home, nil },
		Env:        func(string) string { return "" },
		KeychainToken: func() (string, error) {
			return expiredCredJSON(t, "kc-old", "kc-refresh"), nil
		},
		KeychainAccount: func() (string, error) { return "Claude Code-credentials", nil },
		KeychainWrite: func(account, secret string) error {
			writes++
			written = secret
			return nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if writes != 1 {
		t.Fatalf("keychain writes = %d, want 1", writes)
	}
	var blob map[string]any
	if err := json.Unmarshal([]byte(written), &blob); err != nil {
		t.Fatalf("written keychain secret is not JSON: %v", err)
	}
	oauth, _ := blob["claudeAiOauth"].(map[string]any)
	if oauth == nil || oauth["accessToken"] != "kc-rotated" || oauth["refreshToken"] != "kc-refresh-2" {
		t.Fatalf("written keychain secret = %v, want rotated tokens", blob)
	}
}

// TestFetchSkipsUnparsableFileAndKeychain: broken sources are skipped, and only
// when nothing remains readable does the poll report auth_missing.
func TestFetchSkipsUnparsableFileAndKeychain(t *testing.T) {
	home := t.TempDir()
	writeHomeCredentials(t, home, validCredJSON(t, "home-ok"))
	broken := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"ok","expiresAt":`) // truncated JSON

	// Broken configured file plus a working home file: the home file wins.
	var usageCalls []string
	usage := usageAccepting(t, "home-ok", &usageCalls)
	defer usage.Close()
	_, err := (&Provider{
		BaseURL: usage.URL,
		Now:     func() time.Time { return fallbackNow },
		HomeDir: func() (string, error) { return home, nil },
		Env:     func(string) string { return "" },
		KeychainToken: func() (string, error) {
			t.Error("keychain read; home file should have won")
			return "", errors.New("no keychain")
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: broken})
	if err != nil {
		t.Fatalf("Fetch() error = %v, want broken configured file to be skipped", err)
	}
	if len(usageCalls) != 1 || usageCalls[0] != "Bearer home-ok" {
		t.Fatalf("usage calls = %v", usageCalls)
	}

	// Nothing readable at all: auth_missing, not a parse error.
	_, err = (&Provider{
		BaseURL: "http://127.0.0.1",
		Now:     func() time.Time { return fallbackNow },
		HomeDir: func() (string, error) { return t.TempDir(), nil },
		Env:     func(string) string { return "" },
		KeychainToken: func() (string, error) {
			return "{not json", nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthMissing {
		t.Fatalf("error = %v, want auth_missing", err)
	}
}
