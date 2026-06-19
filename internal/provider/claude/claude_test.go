package claude

import (
	"context"
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
				"utilization": null
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
		t.Fatalf("extra_usage with null utilization should be skipped")
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

func TestAccessTokenFromJSONDoesNotUseRefreshToken(t *testing.T) {
	token, err := accessTokenFromJSON([]byte(`{"refresh_token":"refresh-only"}`))
	if err != nil {
		t.Fatalf("accessTokenFromJSON() error = %v", err)
	}
	if token != "" {
		t.Fatalf("token = %q, want empty", token)
	}
}

func TestAccessTokenFromSecret(t *testing.T) {
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
			got, err := accessTokenFromSecret(tt.secret)
			if err != nil {
				t.Fatalf("accessTokenFromSecret() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("token = %q, want %q", got, tt.want)
			}
		})
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
