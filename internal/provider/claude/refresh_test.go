package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const usageBody = `{"five_hour":{"utilization":1,"resets_at":"2026-06-19T12:00:00Z"}}`

// usageServer accepts exactly one bearer token and returns 401 for anything
// else, so a test fails loudly if the provider keeps using a stale token.
func usageServer(t *testing.T, wantToken string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		*seen = append(*seen, token)
		if token != "Bearer "+wantToken {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(usageBody))
	}))
}

func refreshServer(t *testing.T, wantRefresh, newAccess, newRefresh string, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse refresh form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostForm.Get("refresh_token"); got != wantRefresh {
			t.Errorf("refresh_token = %q, want %q", got, wantRefresh)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  newAccess,
			"refresh_token": newRefresh,
			"expires_in":    43200,
		})
	}))
}

func readBlob(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal credentials: %v", err)
	}
	return out
}

func TestFetchRefreshesExpiredCredentialAndPersists(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	// Already expired an hour ago.
	credPath := writeCredentials(t, `{
		"mcpOAuth": {"pulumi|abc": {"accessToken": "mcp-token"}},
		"claudeAiOauth": {"accessToken": "stale", "refreshToken": "refresh-1", "expiresAt": 1781431200000}
	}`)

	var seen []string
	usage := usageServer(t, "fresh", &seen)
	defer usage.Close()
	calls := 0
	refresh := refreshServer(t, "refresh-1", "fresh", "refresh-2", &calls)
	defer refresh.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return now },
		HomeDir:    func() (string, error) { return t.TempDir(), nil },
		Env:        func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: credPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	if len(seen) != 1 || seen[0] != "Bearer fresh" {
		t.Fatalf("usage requests = %v, want a single call with the refreshed token", seen)
	}

	blob := readBlob(t, credPath)
	oauth, _ := blob["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "fresh" || oauth["refreshToken"] != "refresh-2" {
		t.Fatalf("rotation not persisted: %+v", oauth)
	}
	// A rotated refresh token that is not written back would leave Claude Code
	// holding a dead one, so expiry must move forward too.
	if want := now.Add(43200 * time.Second).UnixMilli(); int64(oauth["expiresAt"].(float64)) != want {
		t.Fatalf("expiresAt = %v, want %d", oauth["expiresAt"], want)
	}
	mcp, _ := blob["mcpOAuth"].(map[string]any)
	pulumi, _ := mcp["pulumi|abc"].(map[string]any)
	if pulumi["accessToken"] != "mcp-token" {
		t.Fatalf("mcpOAuth clobbered by rotation: %+v", blob)
	}
}

func TestFetchRefreshesAfterUnauthorized(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	// expiresAt is far in the future, so only the 401 can trigger the refresh.
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"revoked","refreshToken":"refresh-1","expiresAt":1907000000000}}`)

	var seen []string
	usage := usageServer(t, "fresh", &seen)
	defer usage.Close()
	calls := 0
	refresh := refreshServer(t, "refresh-1", "fresh", "refresh-2", &calls)
	defer refresh.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return now },
		HomeDir:    func() (string, error) { return t.TempDir(), nil },
		Env:        func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: credPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	if len(seen) != 2 || seen[0] != "Bearer revoked" || seen[1] != "Bearer fresh" {
		t.Fatalf("usage requests = %v, want a rejected call then a refreshed retry", seen)
	}
}

func TestFetchDoesNotRefreshEnvironmentToken(t *testing.T) {
	var seen []string
	usage := usageServer(t, "unreachable", &seen)
	defer usage.Close()
	calls := 0
	refresh := refreshServer(t, "", "", "", &calls)
	defer refresh.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir:    func() (string, error) { return t.TempDir(), nil },
		Env: func(key string) string {
			if key == "CLAUDE_CODE_OAUTH_TOKEN" {
				return "env-token"
			}
			return ""
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthExpired {
		t.Fatalf("error = %v, want auth_expired", err)
	}
	// An environment token carries no refresh token and is not ours to rotate.
	if calls != 0 {
		t.Fatalf("refresh calls = %d, want 0", calls)
	}
}

func TestRefreshDoesNotPersistWhenCredentialsChangedConcurrently(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"stale","refreshToken":"refresh-1","expiresAt":1781431200000}}`)

	calls := 0
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Simulate Claude Code winning the race: it rewrites the store while our
		// refresh request is in flight.
		if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"accessToken":"other","refreshToken":"refresh-9","expiresAt":1907000000000}}`), 0o600); err != nil {
			t.Errorf("rewrite credentials: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh", "refresh_token": "refresh-2", "expires_in": 43200,
		})
	}))
	defer refresh.Close()

	var seen []string
	usage := usageServer(t, "never", &seen)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return now },
		HomeDir:    func() (string, error) { return t.TempDir(), nil },
		Env:        func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: credPath})
	if err == nil {
		t.Fatal("Fetch() error = nil, want the rotation to be abandoned")
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}

	blob := readBlob(t, credPath)
	oauth, _ := blob["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "other" || oauth["refreshToken"] != "refresh-9" {
		t.Fatalf("concurrent writer was overwritten: %+v", oauth)
	}
}

func TestRefreshRejectsUnusablePayload(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "missing token", body: `{"expires_in":3600}`},
		{name: "zero lifetime", body: `{"access_token":"x","expires_in":0}`},
		{name: "absurd lifetime", body: `{"access_token":"x","expires_in":99999999}`},
		{name: "malformed", body: `not json`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer refresh.Close()

			_, err := (&Provider{RefreshURL: refresh.URL}).requestRefresh(context.Background(), "refresh-1")
			var perr *usageprovider.Error
			if !errors.As(err, &perr) || perr.Code != usageprovider.ErrInvalidResponse {
				t.Fatalf("error = %v, want invalid_response", err)
			}
		})
	}
}

// TestKeychainRotationRestoresPreviousSecretOnFailedWrite covers the failure the
// stdin-based writer actually produced: `security` truncated multi-kilobyte
// secrets at 128 bytes, replacing the whole credential store - Claude login and
// every MCP server login - with a fragment, silently.
func TestKeychainRotationRestoresPreviousSecretOnFailedWrite(t *testing.T) {
	original := `{"mcpOAuth":{"pulumi|abc":{"accessToken":"mcp-token"}},"claudeAiOauth":{"accessToken":"stale","refreshToken":"refresh-1","expiresAt":1781431200000}}`

	var writes []string
	calls := 0
	refresh := refreshServer(t, "refresh-1", "fresh", "refresh-2", &calls)
	defer refresh.Close()

	var seen []string
	usage := usageServer(t, "never", &seen)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL:         usage.URL,
		RefreshURL:      refresh.URL,
		Now:             func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir:         func() (string, error) { return t.TempDir(), nil },
		Env:             func(string) string { return "" },
		KeychainToken:   func() (string, error) { return original, nil },
		KeychainAccount: func() (string, error) { return "tester", nil },
		KeychainWrite: func(_, secret string) error {
			writes = append(writes, secret)
			if len(writes) == 1 {
				return errors.New("write did not round-trip")
			}
			return nil
		},
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})

	if err == nil {
		t.Fatal("Fetch() error = nil, want the failed rotation surfaced")
	}
	if len(writes) != 2 {
		t.Fatalf("keychain writes = %d, want the rotation plus a restore", len(writes))
	}
	if writes[1] != original {
		t.Fatalf("restore wrote %q, want the original secret", writes[1])
	}
}

// TestRefreshBadRequestClassification separates "the login is dead" from
// "token-burn sent something the endpoint rejected". Only the former should tell
// the user to re-login; reporting a client-side problem as an expired login
// sends them to re-authenticate while the real cause stays hidden.
func TestRefreshBadRequestClassification(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want usageprovider.ErrorCode
	}{
		{
			name: "rfc6749 invalid_grant",
			body: `{"error":"invalid_grant","error_description":"expired"}`,
			want: usageprovider.ErrAuthExpired,
		},
		{
			name: "nested invalid_grant",
			body: `{"type":"error","error":{"type":"invalid_grant","message":"bad refresh"}}`,
			want: usageprovider.ErrAuthExpired,
		},
		{
			name: "message names the refresh token",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Refresh token not found"}}`,
			want: usageprovider.ErrAuthExpired,
		},
		{
			// Observed live from the real endpoint with an unknown client_id.
			// This is token-burn's problem, not a dead user login.
			name: "unknown client",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Client with id 0000 not found"}}`,
			want: usageprovider.ErrTransientHTTPFailure,
		},
		{
			name: "unparseable",
			body: `<html>gateway</html>`,
			want: usageprovider.ErrTransientHTTPFailure,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, tt.body, http.StatusBadRequest)
			}))
			defer server.Close()

			_, err := (&Provider{RefreshURL: server.URL}).requestRefresh(context.Background(), "sk-ant-ort-secret")
			var perr *usageprovider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("error = %T, want *provider.Error", err)
			}
			if perr.Code != tt.want {
				t.Fatalf("code = %s, want %s (%v)", perr.Code, tt.want, err)
			}
			if strings.Contains(err.Error(), "sk-ant-ort-secret") {
				t.Fatalf("error leaks the refresh token: %v", err)
			}
		})
	}
}

// TestExpiredTokenReportsRefreshFailureCause pins the rate-limit case: the
// refresh endpoint answers 429, and the daemon must see rate_limited so it backs
// off, rather than auth_expired telling the user to re-login.
func TestExpiredTokenReportsRefreshFailureCause(t *testing.T) {
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"stale","refreshToken":"refresh-1","expiresAt":1781431200000}}`)

	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	}))
	defer refresh.Close()

	var seen []string
	usage := usageServer(t, "never", &seen)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir:    func() (string, error) { return t.TempDir(), nil },
		Env:        func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: credPath})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrRateLimited {
		t.Fatalf("error = %v, want rate_limited", err)
	}
	if len(seen) != 0 {
		t.Fatalf("usage requests = %v, want none once the token is known dead", seen)
	}
}

// TestValidTokenSurvivesFailedProactiveRefresh is the other side: inside the
// refresh skew the token still works, so a failed refresh must not break the
// poll.
func TestValidTokenSurvivesFailedProactiveRefresh(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	// Expires in two minutes: inside the five-minute skew, but still valid.
	expiresAt := now.Add(2 * time.Minute).UnixMilli()
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"current","refreshToken":"refresh-1","expiresAt":`+strconv.FormatInt(expiresAt, 10)+`}}`)

	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	}))
	defer refresh.Close()

	var seen []string
	usage := usageServer(t, "current", &seen)
	defer usage.Close()

	_, err := (&Provider{
		BaseURL:    usage.URL,
		RefreshURL: refresh.URL,
		Now:        func() time.Time { return now },
		HomeDir:    func() (string, error) { return t.TempDir(), nil },
		Env:        func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default", CredentialsFile: credPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v, want the still-valid token to be used", err)
	}
	if len(seen) != 1 || seen[0] != "Bearer current" {
		t.Fatalf("usage requests = %v, want one call with the current token", seen)
	}
}
