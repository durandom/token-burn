package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func TestFetchMapsUsageBuckets(t *testing.T) {
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"claude-token","refreshToken":"refresh"}}`)
	observedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)

	var gotAuth string
	var gotBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			t.Fatalf("path = %q, want /usage", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"five_hour": {
				"utilization": 37.0,
				"resets_at": "2026-06-19T12:00:00.000000+00:00"
			},
			"seven_day": {
				"utilization": 26.0,
				"resets_at": "2026-06-25T12:00:00Z"
			},
			"seven_day_opus": null,
			"seven_day_sonnet": {
				"utilization": 1.0,
				"resets_at": "2026-06-26T12:00:00Z"
			},
			"seven_day_oauth_apps": {
				"utilization": 3.0,
				"resets_at": "2026-06-26T12:00:00Z"
			},
			"extra_usage": {
				"is_enabled": false,
				"monthly_limit": null,
				"used_credits": null,
				"utilization": 100.0
			}
		}`))
	}))
	defer server.Close()

	snap, err := (&Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return observedAt },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{
		ID:              "claude-default",
		CredentialsFile: credPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if gotAuth != "Bearer claude-token" {
		t.Fatalf("Authorization header = %q, want bearer access token", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q, want oauth beta", gotBeta)
	}
	if snap.Provider != "claude" || snap.AccountID != "claude-default" || snap.Source != "anthropic_oauth_usage" {
		t.Fatalf("snapshot metadata = %#v", snap)
	}

	byName := windowsByName(snap.Windows)
	if len(byName) != 4 {
		t.Fatalf("windows = %#v, want 4 non-null utilization buckets", snap.Windows)
	}
	if got := byName["five_hour"].UsedPercent; got != 37 {
		t.Fatalf("five_hour used = %v, want 37", got)
	}
	if byName["five_hour"].ResetAt == nil || byName["five_hour"].ResetAt.Format(time.RFC3339) != "2026-06-19T12:00:00Z" {
		t.Fatalf("five_hour reset = %v, want 2026-06-19T12:00:00Z", byName["five_hour"].ResetAt)
	}
	if got := byName["seven_day_oauth_apps"].UsedPercent; got != 3 {
		t.Fatalf("seven_day_oauth_apps used = %v, want 3", got)
	}
	if _, ok := byName["extra_usage"]; ok {
		t.Fatalf("disabled extra_usage should be skipped")
	}
}

func TestFetchMapsEnabledExtraUsage(t *testing.T) {
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"claude-token"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"extra_usage": {
				"is_enabled": true,
				"monthly_limit": 100,
				"used_credits": 25,
				"utilization": 25.0
			}
		}`))
	}))
	defer server.Close()

	snap, err := (&Provider{
		BaseURL: server.URL,
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{
		ID:              "claude-default",
		CredentialsFile: credPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	byName := windowsByName(snap.Windows)
	if got := byName["extra_usage"].UsedPercent; got != 25 {
		t.Fatalf("extra_usage used = %v, want 25", got)
	}
}

func TestFetchMapsScopedWeeklyLimits(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     map[string]float64
		wantSkip []string
	}{
		{
			name: "model scoped weekly limit becomes extra window",
			body: `{
				"five_hour": {"utilization": 2.0, "resets_at": "2026-07-03T17:00:00Z"},
				"seven_day": {"utilization": 11.0, "resets_at": "2026-07-08T20:00:00Z"},
				"seven_day_opus": null,
				"limits": [
					{"kind": "session", "group": "session", "percent": 2, "resets_at": "2026-07-03T17:00:00Z"},
					{"kind": "weekly_all", "group": "weekly", "percent": 11, "resets_at": "2026-07-08T20:00:00Z"},
					{"kind": "weekly_scoped", "group": "weekly", "percent": 7, "resets_at": "2026-07-08T20:00:00Z",
					 "scope": {"model": {"id": null, "display_name": "Fable"}}}
				]
			}`,
			want: map[string]float64{
				"five_hour":       2,
				"seven_day":       11,
				"seven_day_fable": 7,
			},
		},
		{
			name: "scoped limit without display name falls back to model id",
			body: `{
				"limits": [
					{"kind": "weekly_scoped", "group": "weekly", "percent": 4, "resets_at": "2026-07-08T20:00:00Z",
					 "scope": {"model": {"id": "claude-fable-5", "display_name": null}}}
				]
			}`,
			want: map[string]float64{"seven_day_claude_fable_5": 4},
		},
		{
			name: "scoped limit without percent is skipped",
			body: `{
				"limits": [
					{"kind": "weekly_scoped", "group": "weekly", "resets_at": "2026-07-08T20:00:00Z",
					 "scope": {"model": {"display_name": "Fable"}}}
				]
			}`,
			wantSkip: []string{"seven_day_fable"},
		},
		{
			name: "duplicate scoped names keep first entry",
			body: `{
				"limits": [
					{"kind": "weekly_scoped", "group": "weekly", "percent": 7, "resets_at": "2026-07-08T20:00:00Z",
					 "scope": {"model": {"display_name": "Fable"}}},
					{"kind": "weekly_scoped", "group": "weekly", "percent": 9, "resets_at": "2026-07-08T20:00:00Z",
					 "scope": {"model": {"display_name": "Fable"}}}
				]
			}`,
			want: map[string]float64{"seven_day_fable": 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"claude-token"}}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			snap, err := (&Provider{
				BaseURL: server.URL,
				Now:     func() time.Time { return time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC) },
				HomeDir: func() (string, error) {
					return t.TempDir(), nil
				},
				Env: func(string) string { return "" },
			}).Fetch(context.Background(), usageprovider.Account{
				ID:              "claude-default",
				CredentialsFile: credPath,
			})
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}

			byName := windowsByName(snap.Windows)
			if len(byName) != len(tt.want) {
				t.Fatalf("windows = %#v, want %d windows %v", snap.Windows, len(tt.want), tt.want)
			}
			for name, used := range tt.want {
				win, ok := byName[name]
				if !ok {
					t.Fatalf("window %q missing, got %#v", name, snap.Windows)
				}
				if win.UsedPercent != used {
					t.Fatalf("%s used = %v, want %v", name, win.UsedPercent, used)
				}
			}
			for _, name := range tt.wantSkip {
				if _, ok := byName[name]; ok {
					t.Fatalf("window %q should be skipped, got %#v", name, snap.Windows)
				}
			}
		})
	}
}

func TestFetchScopedLimitResetTime(t *testing.T) {
	credPath := writeCredentials(t, `{"claudeAiOauth":{"accessToken":"claude-token"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"limits": [
				{"kind": "weekly_scoped", "group": "weekly", "percent": 7,
				 "resets_at": "2026-07-08T19:59:59.801499+00:00",
				 "scope": {"model": {"display_name": "Fable"}}}
			]
		}`))
	}))
	defer server.Close()

	snap, err := (&Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC) },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{
		ID:              "claude-default",
		CredentialsFile: credPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	byName := windowsByName(snap.Windows)
	win, ok := byName["seven_day_fable"]
	if !ok {
		t.Fatalf("seven_day_fable missing, got %#v", snap.Windows)
	}
	if win.ResetAt == nil || !win.ResetAt.Equal(time.Date(2026, 7, 8, 19, 59, 59, 801499000, time.UTC)) {
		t.Fatalf("seven_day_fable reset = %v, want 2026-07-08T19:59:59.801499Z", win.ResetAt)
	}
}

func TestFetchUsesEnvironmentTokenBeforeFile(t *testing.T) {
	credPath := writeCredentials(t, `{"access_token":"file-token"}`)

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1,"resets_at":"2026-06-19T12:00:00Z"}}`))
	}))
	defer server.Close()

	_, err := (&Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(key string) string {
			if key == "CLAUDE_CODE_OAUTH_TOKEN" {
				return "env-token"
			}
			return ""
		},
	}).Fetch(context.Background(), usageprovider.Account{
		ID:              "claude-default",
		CredentialsFile: credPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotAuth != "Bearer env-token" {
		t.Fatalf("Authorization header = %q, want env token", gotAuth)
	}
}

func TestFetchAuthErrorsAreTyped(t *testing.T) {
	credPath := writeCredentials(t, `{"oauth_access_token":"claude-token"}`)
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", status)
			}))
			defer server.Close()

			_, err := (&Provider{
				BaseURL: server.URL,
				Now:     func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
				HomeDir: func() (string, error) {
					return t.TempDir(), nil
				},
				Env: func(string) string { return "" },
			}).Fetch(context.Background(), usageprovider.Account{
				ID:              "claude-default",
				CredentialsFile: credPath,
			})
			var perr *usageprovider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("error = %T, want *provider.Error", err)
			}
			if perr.Code != usageprovider.ErrAuthExpired || perr.HTTPStatus != status {
				t.Fatalf("provider error = %#v, want auth expired HTTP %d", perr, status)
			}
			if strings.Contains(err.Error(), "claude-token") {
				t.Fatalf("error leaks token: %v", err)
			}
		})
	}
}

func TestFetchMissingCredentialsIsTyped(t *testing.T) {
	_, err := (&Provider{
		BaseURL: "http://127.0.0.1",
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env:           func(string) string { return "" },
		KeychainToken: func() (string, error) { return "", nil },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error = %T, want *provider.Error", err)
	}
	if perr.Code != usageprovider.ErrAuthMissing {
		t.Fatalf("error code = %s, want auth_missing", perr.Code)
	}
}

func TestFetchUsesKeychainFallback(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1,"resets_at":"2026-06-19T12:00:00Z"}}`))
	}))
	defer server.Close()

	_, err := (&Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env:           func(string) string { return "" },
		KeychainToken: func() (string, error) { return "keychain-token", nil },
	}).Fetch(context.Background(), usageprovider.Account{ID: "claude-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotAuth != "Bearer keychain-token" {
		t.Fatalf("Authorization header = %q, want keychain token", gotAuth)
	}
}

func TestCredentialFromJSONIgnoresRefreshToken(t *testing.T) {
	cred, ok, err := credentialFromJSON([]byte(`{"claudeAiOauth":{"refreshToken":"refresh-only"}}`))
	if err != nil {
		t.Fatalf("credentialFromJSON() error = %v", err)
	}
	if ok || cred.Access != "" {
		t.Fatalf("credential = %+v ok = %t, want no usable access token", cred, ok)
	}
}

// TestCredentialFromJSONIgnoresForeignOAuthTokens pins the fix for the bug that
// made Claude polling fail roughly nine times out of ten: Claude Code stores MCP
// server logins in the same container, and a recursive "find any accessToken"
// search picked one of those at random because Go randomizes map iteration.
func TestCredentialFromJSONIgnoresForeignOAuthTokens(t *testing.T) {
	blob := []byte(`{
		"mcpOAuth": {
			"pulumi|abc": {"accessToken": "mcp-pulumi-token", "refreshToken": "mcp-refresh"},
			"workos|def": {"accessToken": "mcp-workos-token"},
			"cloudflare|ghi": {"accessToken": "mcp-cloudflare-token"}
		},
		"claudeAiOauth": {
			"accessToken": "sk-ant-oat-real",
			"refreshToken": "sk-ant-ort-real",
			"expiresAt": 1787165038813
		}
	}`)

	// Repeat: a single pass could pass by luck under randomized map iteration.
	for i := 0; i < 200; i++ {
		cred, ok, err := credentialFromJSON(blob)
		if err != nil || !ok {
			t.Fatalf("credentialFromJSON() ok = %t, error = %v", ok, err)
		}
		if cred.Access != "sk-ant-oat-real" {
			t.Fatalf("iteration %d: access token = %q, want the claudeAiOauth token", i, cred.Access)
		}
		if cred.Refresh != "sk-ant-ort-real" {
			t.Fatalf("iteration %d: refresh token = %q, want the claudeAiOauth token", i, cred.Refresh)
		}
	}
}

func TestCredentialFromSecret(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "plain", secret: "plain-token", want: "plain-token"},
		{name: "json", secret: `{"claudeAiOauth":{"accessToken":"json-token","refreshToken":"refresh"}}`, want: "json-token"},
		{name: "empty", secret: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, _, err := credentialFromSecret(tt.secret)
			if err != nil {
				t.Fatalf("credentialFromSecret() error = %v", err)
			}
			if cred.Access != tt.want {
				t.Fatalf("token = %q, want %q", cred.Access, tt.want)
			}
		})
	}
}

// TestEncodePreservesForeignKeys guards the write-back path: rotating the Claude
// token must not disturb the MCP server logins stored beside it, or the user is
// silently signed out of every OAuth-authenticated MCP server.
func TestEncodePreservesForeignKeys(t *testing.T) {
	cred, ok, err := credentialFromJSON([]byte(`{
		"mcpOAuth": {"pulumi|abc": {"accessToken": "mcp-pulumi-token"}},
		"claudeAiOauth": {"accessToken": "old", "refreshToken": "old-refresh", "subscriptionType": "max", "someFutureField": 7}
	}`))
	if err != nil || !ok {
		t.Fatalf("credentialFromJSON() ok = %t, error = %v", ok, err)
	}

	cred.Access = "new"
	cred.Refresh = "new-refresh"
	cred.ExpiresAt = 1234
	encoded, err := cred.encode()
	if err != nil {
		t.Fatalf("encode() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal encoded: %v", err)
	}
	mcp, _ := got["mcpOAuth"].(map[string]any)
	pulumi, _ := mcp["pulumi|abc"].(map[string]any)
	if pulumi["accessToken"] != "mcp-pulumi-token" {
		t.Fatalf("mcpOAuth was not preserved: %s", encoded)
	}
	oauth, _ := got["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "new" || oauth["refreshToken"] != "new-refresh" {
		t.Fatalf("rotation not applied: %s", encoded)
	}
	if oauth["subscriptionType"] != "max" {
		t.Fatalf("modelled sibling field lost: %s", encoded)
	}
	if oauth["someFutureField"] != float64(7) {
		t.Fatalf("unmodelled sibling field lost: %s", encoded)
	}
}

func writeCredentials(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	return path
}

func windowsByName(windows []usageprovider.Window) map[string]usageprovider.Window {
	out := make(map[string]usageprovider.Window, len(windows))
	for _, win := range windows {
		out[win.Name] = win
	}
	return out
}
