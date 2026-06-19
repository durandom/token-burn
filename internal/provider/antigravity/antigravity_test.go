package antigravity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
	_ "modernc.org/sqlite"
)

func TestFetchMapsCloudCodeModels(t *testing.T) {
	stateDB := writeStateDB(t, tokenEnvelope("agy-token", time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:fetchAvailableModels" {
			t.Fatalf("path = %q, want /v1internal:fetchAvailableModels", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-3-pro": {
					"displayName": "Gemini 3 Pro",
					"model": "gemini-3-pro",
					"quotaInfo": {
						"remainingFraction": 0.8,
						"resetTime": "2026-06-26T10:00:00Z"
					}
				},
				"gemini-3-flash": {
					"displayName": "Gemini 3 Flash",
					"model": "gemini-3-flash",
					"quotaInfo": {
						"remainingFraction": 0.9,
						"resetTime": "2026-06-26T10:00:00Z"
					}
				},
				"claude-sonnet": {
					"displayName": "Claude Sonnet 4.5",
					"model": "claude-sonnet",
					"quotaInfo": {
						"remainingFraction": 0.45,
						"resetTime": "2026-06-19T15:00:00Z"
					}
				},
				"internal": {
					"displayName": "Hidden",
					"model": "internal",
					"isInternal": true,
					"quotaInfo": {"remainingFraction": 0.1}
				}
			}
		}`))
	}))
	defer server.Close()

	snap, err := (&Provider{
		BaseURLs:       []string{server.URL},
		StateDBPaths:   []string{stateDB},
		KeychainSecret: func() (string, error) { return "", nil },
		Now:            func() time.Time { return now },
	}).Fetch(context.Background(), usageprovider.Account{ID: "antigravity-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotAuth != "Bearer agy-token" {
		t.Fatalf("Authorization = %q, want bearer token", gotAuth)
	}
	if snap.Provider != "antigravity" || snap.Source != source || snap.AccountID != "antigravity-default" {
		t.Fatalf("snapshot metadata = %#v", snap)
	}
	byName := windowsByName(snap.Windows)
	if got := byName["gemini"].UsedPercent; math.Abs(got-20) > 0.0001 {
		t.Fatalf("gemini used = %v, want 20", got)
	}
	if byName["gemini"].ResetAt == nil || byName["gemini"].ResetAt.Format(time.RFC3339) != "2026-06-26T10:00:00Z" {
		t.Fatalf("gemini reset = %v", byName["gemini"].ResetAt)
	}
	if got := byName["claude_and_gpt"].UsedPercent; math.Abs(got-55) > 0.0001 {
		t.Fatalf("claude_and_gpt used = %v, want 55", got)
	}
	if got := snap.Raw["gemini_model"]; got != "gemini-3-pro" {
		t.Fatalf("gemini_model = %#v, want gemini-3-pro", got)
	}
}

func TestFetchUsesKeychainWhenStateDBMissing(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"models":{}}`))
	}))
	defer server.Close()

	_, err := (&Provider{
		BaseURLs:       []string{server.URL},
		StateDBPaths:   []string{filepath.Join(t.TempDir(), "missing.vscdb")},
		KeychainSecret: func() (string, error) { return `{"oauth":{"access_token":"keychain-token"}}`, nil },
	}).Fetch(context.Background(), usageprovider.Account{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotAuth != "Bearer keychain-token" {
		t.Fatalf("Authorization = %q, want keychain token", gotAuth)
	}
}

func TestFetchExpiredTokenIsTyped(t *testing.T) {
	stateDB := writeStateDB(t, tokenEnvelopeWithRefresh("expired-token", "", time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)))
	_, err := (&Provider{
		BaseURLs:       []string{"http://127.0.0.1"},
		StateDBPaths:   []string{stateDB},
		KeychainSecret: func() (string, error) { return "", nil },
		Now:            func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
	}).Fetch(context.Background(), usageprovider.Account{})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthExpired {
		t.Fatalf("error = %#v, want auth_expired", err)
	}
	if strings.Contains(err.Error(), "expired-token") {
		t.Fatalf("error leaks token: %v", err)
	}
}

func TestFetchRefreshesExpiredStateToken(t *testing.T) {
	stateDB := writeStateDB(t, tokenEnvelopeWithRefresh("expired-token", "refresh-token", time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	var sawRefresh bool
	var gotModelAuth string
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRefresh = true
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("refresh_token"); got != "refresh-token" {
			t.Fatalf("refresh_token = %q, want refresh-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-token","expires_in":3600}`))
	}))
	defer oauthServer.Close()

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotModelAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"models":{"gemini-3-pro":{"displayName":"Gemini 3 Pro","model":"gemini-3-pro","quotaInfo":{"remainingFraction":0.75}}}}`))
	}))
	defer modelServer.Close()

	snap, err := (&Provider{
		BaseURLs:       []string{modelServer.URL},
		OAuthURL:       oauthServer.URL,
		OAuthClientID:  "client-id",
		OAuthSecret:    "client-secret",
		StateDBPaths:   []string{stateDB},
		TokenCachePath: cachePath,
		KeychainSecret: func() (string, error) { return "", nil },
		Now:            func() time.Time { return now },
	}).Fetch(context.Background(), usageprovider.Account{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !sawRefresh {
		t.Fatal("oauth refresh was not called")
	}
	if gotModelAuth != "Bearer fresh-token" {
		t.Fatalf("Authorization = %q, want fresh token", gotModelAuth)
	}
	if got := windowsByName(snap.Windows)["gemini"].UsedPercent; math.Abs(got-25) > 0.0001 {
		t.Fatalf("gemini used = %v, want 25", got)
	}
	if data, err := os.ReadFile(cachePath); err != nil || !strings.Contains(string(data), "fresh-token") {
		t.Fatalf("cache file = %q, err = %v; want fresh-token", data, err)
	}
}

func TestFetchHTTPAuthFailureIsTyped(t *testing.T) {
	stateDB := writeStateDB(t, tokenEnvelope("agy-token", time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := (&Provider{
		BaseURLs:       []string{server.URL},
		StateDBPaths:   []string{stateDB},
		KeychainSecret: func() (string, error) { return "", nil },
		Now:            func() time.Time { return time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC) },
	}).Fetch(context.Background(), usageprovider.Account{})

	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthExpired {
		t.Fatalf("error = %#v, want auth_expired", err)
	}
}

func TestAccessTokenFromKeychainSecret(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"access_token":"wrapped"}`))
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{name: "json", secret: `{"credentials":{"access_token":"json-token"}}`, want: "json-token"},
		{name: "base64", secret: "go-keyring-base64:" + encoded, want: "wrapped"},
		{name: "bearer", secret: "Bearer plain-token", want: "plain-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accessTokenFromKeychainSecret(tt.secret); got != tt.want {
				t.Fatalf("token = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenFromKeychainSecretExtractsRefreshToken(t *testing.T) {
	token := tokenFromKeychainSecret(`{"credentials":{"access_token":"access","refresh_token":"refresh"}}`)
	if token.AccessToken != "access" || token.RefreshToken != "refresh" {
		t.Fatalf("token = %#v, want access and refresh", token)
	}
}

func writeStateDB(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT, value TEXT)`); err != nil {
		t.Fatalf("create ItemTable: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, oauthTokenKey, value); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	return path
}

func tokenEnvelope(accessToken string, expiry time.Time) string {
	return tokenEnvelopeWithRefresh(accessToken, "refresh-token", expiry)
}

func tokenEnvelopeWithRefresh(accessToken, refreshToken string, expiry time.Time) string {
	ts := protoMessage(protoVarint(1, uint64(expiry.Unix())))
	parts := [][]byte{protoString(1, accessToken)}
	if refreshToken != "" {
		parts = append(parts, protoString(3, refreshToken))
	}
	parts = append(parts, protoBytes(4, ts))
	inner := protoMessage(parts...)
	payload := protoMessage(protoString(1, base64.StdEncoding.EncodeToString(inner)))
	wrapper := protoMessage(protoString(1, oauthTokenSentinel), protoBytes(2, payload))
	outer := protoMessage(protoBytes(1, wrapper))
	return base64.StdEncoding.EncodeToString(outer)
}

func protoMessage(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func protoString(field int, value string) []byte {
	return protoBytes(field, []byte(value))
}

func protoBytes(field int, value []byte) []byte {
	out := append(protoVarintBytes(uint64(field<<3|2)), protoVarintBytes(uint64(len(value)))...)
	return append(out, value...)
}

func protoVarint(field int, value uint64) []byte {
	return append(protoVarintBytes(uint64(field<<3)), protoVarintBytes(value)...)
}

func protoVarintBytes(value uint64) []byte {
	var out []byte
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

func windowsByName(windows []usageprovider.Window) map[string]usageprovider.Window {
	out := make(map[string]usageprovider.Window, len(windows))
	for _, win := range windows {
		out[win.Name] = win
	}
	return out
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
