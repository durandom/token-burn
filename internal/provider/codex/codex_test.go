package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func TestFetchMapsUsageWindows(t *testing.T) {
	authPath := writeAuth(t, `{"account_id":"acct_header","tokens":{"access_token":"tok_test"}}`)
	observedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)

	var gotAuth string
	var gotAccountID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wham/usage" {
			t.Fatalf("path = %q, want /wham/usage", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("ChatGPT-Account-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"plan_type": "plus",
			"account_id": "provider-account",
			"rate_limit": {
				"primary_window": {
					"used_percent": 12,
					"limit_window_seconds": 18000,
					"reset_at": 1781870400
				},
				"secondary_window": {
					"remaining_percent": 90,
					"limit_window_seconds": 604800,
					"resets_at": 1782302400
				}
			},
			"code_review_rate_limit": {
				"primary_window": {
					"used_percent": 25,
					"window_minutes": 60,
					"reset_after_seconds": 300
				}
			},
			"additional_rate_limits": [{
				"metered_feature": "deep research",
				"rate_limit": {
					"primary_window": {
						"used_percent": 45,
						"limit_window_seconds": 18000
					}
				}
			}],
			"credits": {
				"has_credits": true
			}
		}`))
	}))
	defer server.Close()

	provider := &Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return observedAt },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}

	snap, err := provider.Fetch(context.Background(), usageprovider.Account{
		ID:       "codex-default",
		AuthFile: authPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if gotAuth != "Bearer tok_test" {
		t.Fatalf("Authorization header = %q, want bearer token", gotAuth)
	}
	if gotAccountID != "acct_header" {
		t.Fatalf("ChatGPT-Account-Id = %q, want acct_header", gotAccountID)
	}
	if snap.Provider != "codex" || snap.AccountID != "codex-default" || snap.PlanType != "plus" || snap.Source != "wham_usage" {
		t.Fatalf("snapshot metadata = %#v", snap)
	}

	byName := windowsByName(snap.Windows)
	wantNames := []string{"additional_deep_research_primary", "code_review_primary", "five_hour", "seven_day"}
	if got := sortedKeys(byName); !equalStrings(got, wantNames) {
		t.Fatalf("windows = %v, want %v", got, wantNames)
	}

	if got := byName["five_hour"].UsedPercent; got != 12 {
		t.Fatalf("five_hour used = %v, want 12", got)
	}
	if byName["five_hour"].WindowSeconds == nil || *byName["five_hour"].WindowSeconds != 18000 {
		t.Fatalf("five_hour window seconds = %v, want 18000", byName["five_hour"].WindowSeconds)
	}
	if byName["five_hour"].ResetAt == nil || byName["five_hour"].ResetAt.Unix() != 1781870400 {
		t.Fatalf("five_hour reset = %v, want unix 1781870400", byName["five_hour"].ResetAt)
	}
	if got := byName["seven_day"].UsedPercent; got != 10 {
		t.Fatalf("seven_day used = %v, want 10 from remaining_percent", got)
	}
	if byName["code_review_primary"].ResetAt == nil || !byName["code_review_primary"].ResetAt.Equal(observedAt.Add(5*time.Minute)) {
		t.Fatalf("code_review reset = %v, want observed+5m", byName["code_review_primary"].ResetAt)
	}
}

func TestFetchUsesRateLimitStatusFallback(t *testing.T) {
	authPath := writeAuth(t, `{"tokens":{"access_token":"tok_test"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"rate_limit_status": {
				"plan_type": "team",
				"rate_limit": {
					"primary": {
						"used_percent": 33,
						"window_minutes": 300
					}
				}
			}
		}`))
	}))
	defer server.Close()

	snap, err := (&Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{
		ID:       "codex-default",
		AuthFile: authPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if snap.PlanType != "team" {
		t.Fatalf("PlanType = %q, want team", snap.PlanType)
	}
	byName := windowsByName(snap.Windows)
	if got := byName["primary"].UsedPercent; got != 33 {
		t.Fatalf("primary used = %v, want 33", got)
	}
	if byName["primary"].WindowSeconds == nil || *byName["primary"].WindowSeconds != 18000 {
		t.Fatalf("primary window seconds = %v, want 18000 from window_minutes", byName["primary"].WindowSeconds)
	}
}

func TestFetchHandlesMissingSecondaryWindow(t *testing.T) {
	authPath := writeAuth(t, `{"tokens":{"access_token":"tok_test"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"rate_limit": {
				"primary_window": {
					"used_percent": 20,
					"limit_window_seconds": 18000
				}
			}
		}`))
	}))
	defer server.Close()

	snap, err := (&Provider{
		BaseURL: server.URL,
		Now:     func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{
		ID:       "codex-default",
		AuthFile: authPath,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Name != "five_hour" {
		t.Fatalf("windows = %#v, want only five_hour", snap.Windows)
	}
}

func TestFetchAuthErrorsAreTyped(t *testing.T) {
	authPath := writeAuth(t, `{"tokens":{"access_token":"tok_test"}}`)
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
				ID:       "codex-default",
				AuthFile: authPath,
			})
			var perr *usageprovider.Error
			if !errors.As(err, &perr) {
				t.Fatalf("error = %T, want *provider.Error", err)
			}
			if perr.Code != usageprovider.ErrAuthExpired || perr.HTTPStatus != status {
				t.Fatalf("provider error = %#v, want auth expired HTTP %d", perr, status)
			}
			if strings.Contains(err.Error(), "tok_test") {
				t.Fatalf("error leaks token: %v", err)
			}
		})
	}
}

func TestFetchMissingAuthIsTyped(t *testing.T) {
	_, err := (&Provider{
		BaseURL: "http://127.0.0.1",
		HomeDir: func() (string, error) {
			return t.TempDir(), nil
		},
		Env: func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{ID: "codex-default"})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error = %T, want *provider.Error", err)
	}
	if perr.Code != usageprovider.ErrAuthMissing {
		t.Fatalf("error code = %s, want auth_missing", perr.Code)
	}
}

func TestAuthLookupUsesConfiguredPathBeforeDefaults(t *testing.T) {
	dir := t.TempDir()
	configured := filepath.Join(dir, "configured.json")
	codexHome := filepath.Join(dir, "codex-home")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(configured, []byte(`{"tokens":{"access_token":"configured"}}`), 0600); err != nil {
		t.Fatalf("write configured auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"tokens":{"access_token":"default"}}`), 0600); err != nil {
		t.Fatalf("write default auth: %v", err)
	}

	provider := &Provider{
		HomeDir: func() (string, error) { return dir, nil },
		Env: func(key string) string {
			if key == "CODEX_HOME" {
				return codexHome
			}
			return ""
		},
	}
	auth, path, err := provider.readAuth(usageprovider.Account{AuthFile: configured})
	if err != nil {
		t.Fatalf("readAuth() error = %v", err)
	}
	if path != configured {
		t.Fatalf("auth path = %q, want configured path", path)
	}
	if auth.Tokens.AccessToken != "configured" {
		t.Fatalf("access token = %q, want configured", auth.Tokens.AccessToken)
	}
}

func writeAuth(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	var valid any
	if err := json.Unmarshal([]byte(content), &valid); err != nil {
		t.Fatalf("invalid test auth json: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write auth: %v", err)
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

func sortedKeys[V any](values map[string]V) []string {
	var out []string
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
