package xai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/piauth"
	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func TestFetchUsesPiOAuthAndMapsWeeklyUsage(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{
		"xai": map[string]any{"type": "oauth", "access": "secret-access", "refresh": "secret-refresh", "expires": now.Add(time.Hour).UnixMilli()},
	})
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if got := r.Header.Get("Authorization"); got != "Bearer secret-access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-XAI-Token-Auth"); got != "xai-grok-cli" {
			t.Errorf("X-XAI-Token-Auth = %q", got)
		}
		if got := r.Header.Get("x-grok-client-version"); got != clientVersion {
			t.Errorf("x-grok-client-version = %q", got)
		}
		if got := r.Header.Get("x-grok-client-mode"); got != "headless" {
			t.Errorf("x-grok-client-mode = %q", got)
		}
		switch r.URL.Path {
		case "/v1/user":
			if got := r.Header.Get("x-userid"); got != "" {
				t.Errorf("user x-userid = %q", got)
			}
			fmt.Fprint(w, `{"userId":"transient-user"}`)
		case "/v1/billing":
			if got := r.Header.Get("x-userid"); got != "transient-user" {
				t.Errorf("billing x-userid = %q", got)
			}
			fmt.Fprint(w, `{
				"subscriptionTier":"supergrok",
				"onDemandEnabled":true,
				"config":{
					"creditUsagePercent":25,
					"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-07-29T13:00:00Z","end":"2026-08-05T13:00:00Z"},
					"isUnifiedBillingUser":true,
					"onDemandCap":{"val":1000},"onDemandUsed":{"val":250},"prepaidBalance":{"val":500}
				}
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := &Provider{HTTPClient: server.Client(), BaseURL: server.URL + "/v1", Now: func() time.Time { return now }}
	snap, err := p.Fetch(context.Background(), usageprovider.Account{ID: "xai-work", AuthFile: authPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if want := []string{"/v1/user", "/v1/billing?format=credits"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	if snap.Provider != "xai" || snap.AccountID != "xai-work" || snap.PlanType != "supergrok" || snap.Source != source {
		t.Fatalf("snapshot metadata = %#v", snap)
	}
	if len(snap.Windows) != 1 {
		t.Fatalf("windows = %#v", snap.Windows)
	}
	win := snap.Windows[0]
	if win.Name != "weekly" || win.UsedPercent != 25 || win.WindowSeconds == nil || *win.WindowSeconds != 7*24*60*60 {
		t.Fatalf("window = %#v", win)
	}
	if win.ResetAt == nil || !win.ResetAt.Equal(time.Date(2026, 8, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("reset = %v", win.ResetAt)
	}
	rawJSON, _ := json.Marshal(snap.Raw)
	for _, secret := range []string{"secret-access", "secret-refresh", "transient-user"} {
		if strings.Contains(string(rawJSON), secret) {
			t.Fatalf("raw metadata leaked %q: %s", secret, rawJSON)
		}
	}
	if snap.Raw["on_demand_cap_cents"] != int64(1000) || snap.Raw["is_unified_billing_user"] != true {
		t.Fatalf("raw = %#v", snap.Raw)
	}
}

func TestFetchMapsLegacyMonthlyUsage(t *testing.T) {
	payload := billingResponse{Config: &billingConfig{
		MonthlyLimit:       money(2000),
		Used:               money(500),
		BillingPeriodStart: "2026-07-01T00:00:00Z",
		BillingPeriodEnd:   "2026-08-01T00:00:00Z",
	}}
	snap, err := mapBilling(payload, usageprovider.Account{}, time.Now())
	if err != nil {
		t.Fatalf("mapBilling() error = %v", err)
	}
	if got := snap.Windows[0]; got.Name != "monthly" || got.UsedPercent != 25 || got.WindowSeconds == nil || *got.WindowSeconds != 31*24*60*60 {
		t.Fatalf("window = %#v", got)
	}
}

func TestModernPeriodNamesAreNeutralUnlessVerified(t *testing.T) {
	tests := []struct {
		name       string
		periodType string
		want       string
	}{
		{name: "absent", want: "quota"},
		{name: "unknown", periodType: "USAGE_PERIOD_TYPE_ROLLING", want: "quota"},
		{name: "biweekly near match", periodType: "USAGE_PERIOD_TYPE_BIWEEKLY", want: "quota"},
		{name: "bimonthly near match", periodType: "USAGE_PERIOD_TYPE_BIMONTHLY", want: "quota"},
		{name: "explicit monthly", periodType: "USAGE_PERIOD_TYPE_MONTHLY", want: "monthly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used := 12.5
			cfg := &billingConfig{CreditUsagePercent: &used}
			if tt.periodType != "" {
				cfg.CurrentPeriod = &usagePeriod{Type: tt.periodType}
			}
			snap, err := mapBilling(billingResponse{Config: cfg}, usageprovider.Account{}, time.Now())
			if err != nil {
				t.Fatalf("mapBilling() error = %v", err)
			}
			if got := snap.Windows[0].Name; got != tt.want {
				t.Fatalf("window name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyMonthlyRequiresBillingPeriodEnd(t *testing.T) {
	payload := billingResponse{Config: &billingConfig{
		MonthlyLimit: money(2000),
		Used:         money(500),
	}}
	snap, err := mapBilling(payload, usageprovider.Account{}, time.Now())
	if err != nil {
		t.Fatalf("mapBilling() error = %v", err)
	}
	if got := snap.Windows[0].Name; got != "quota" {
		t.Fatalf("window name = %q, want quota", got)
	}
}

func TestWindowDurationIsBoundedBeforeIntConversion(t *testing.T) {
	used := 10.0
	payload := billingResponse{Config: &billingConfig{
		CreditUsagePercent: &used,
		CurrentPeriod: &usagePeriod{
			Type:  "WEEKLY",
			Start: "2000-01-01T00:00:00Z",
			End:   "2200-01-01T00:00:00Z",
		},
	}}
	snap, err := mapBilling(payload, usageprovider.Account{}, time.Now())
	if err != nil {
		t.Fatalf("mapBilling() error = %v", err)
	}
	if got := snap.Windows[0].WindowSeconds; got != nil {
		t.Fatalf("unreasonable window duration converted to int: %d", *got)
	}
}

func TestAuthPathPrecedenceAndEnvironment(t *testing.T) {
	home := t.TempDir()
	p := &Provider{HomeDir: func() (string, error) { return home, nil }, Env: func(key string) string {
		if key == "PI_CODING_AGENT_DIR" {
			return "~/custom-pi"
		}
		return ""
	}}
	if got, want := p.authPath(usageprovider.Account{AuthFile: "~/explicit.json"}), filepath.Join(home, "explicit.json"); got != want {
		t.Fatalf("configured auth path = %q, want %q", got, want)
	}
	if got, want := p.authPath(usageprovider.Account{}), filepath.Join(home, "custom-pi", "auth.json"); got != want {
		t.Fatalf("environment auth path = %q, want %q", got, want)
	}
	p.Env = func(string) string { return "" }
	if got, want := p.authPath(usageprovider.Account{}), filepath.Join(home, ".pi", "agent", "auth.json"); got != want {
		t.Fatalf("default auth path = %q, want %q", got, want)
	}
}

func TestAuthSymlinkCanonicalizesBeforeLockAndRefresh(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	target := writeAuth(t, map[string]any{"xai": map[string]any{
		"type": "oauth", "access": "old-access", "refresh": "old-refresh", "expires": now.Add(-time.Minute).UnixMilli(),
	}})
	link := filepath.Join(t.TempDir(), "auth.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	p := &Provider{}
	cred, canonical, err := p.readCredential(context.Background(), usageprovider.Account{AuthFile: link})
	if err != nil {
		t.Fatalf("readCredential() error = %v", err)
	}
	wantCanonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != wantCanonical || cred.Access != "old-access" {
		t.Fatalf("canonical path = %q, credential = %#v, want %q", canonical, cred, wantCanonical)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"new-access","expires_in":3600}`)
	}))
	defer server.Close()
	p.HTTPClient = server.Client()
	p.OAuthURL = server.URL
	if _, err := p.refreshCredential(context.Background(), canonical, "old-access", false, now); err != nil {
		t.Fatalf("refreshCredential() error = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("auth symlink replaced: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(wantCanonical + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical target lock remains: %v", err)
	}
	root := readAuthMap(t, target)
	if got := root["xai"].(map[string]any)["access"]; got != "new-access" {
		t.Fatalf("target access = %#v", got)
	}
}

func TestReadCredentialRejectsMalformedAndWrongTypes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		code    usageprovider.ErrorCode
	}{
		{name: "malformed", content: `{`, code: usageprovider.ErrInvalidResponse},
		{name: "missing xai", content: `{"openai":{"type":"oauth"}}`, code: usageprovider.ErrAuthMissing},
		{name: "api key", content: `{"xai":{"type":"api_key","key":"secret"}}`, code: usageprovider.ErrAuthMissing},
		{name: "missing access", content: `{"xai":{"type":"oauth","refresh":"secret"}}`, code: usageprovider.ErrAuthMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := (&Provider{}).readCredential(context.Background(), usageprovider.Account{AuthFile: path})
			var providerErr *usageprovider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != tt.code {
				t.Fatalf("error = %v, want %s", err, tt.code)
			}
			if strings.Contains(fmt.Sprint(err), "secret") {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestProactiveRefreshPersistsAndPreservesAuthFields(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{
		"github-copilot": map[string]any{"type": "oauth", "access": "other-secret", "unknown": true},
		"xai":            map[string]any{"type": "oauth", "access": "old-access", "refresh": "old-refresh", "expires": now.Add(-time.Minute).UnixMilli(), "custom": "keep"},
	})
	var refreshCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			refreshCalls++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != xaiOAuthClientID || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("grant_type") != "refresh_token" {
				t.Errorf("refresh form = %#v", r.Form)
			}
			fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
		case "/v1/user":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Errorf("usage auth = %q", r.Header.Get("Authorization"))
			}
			fmt.Fprint(w, `{"userId":"user"}`)
		case "/v1/billing":
			fmt.Fprint(w, `{"config":{"creditUsagePercent":10,"currentPeriod":{"type":"WEEKLY","end":"2026-08-01T00:00:00Z"}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := &Provider{HTTPClient: server.Client(), BaseURL: server.URL + "/v1", OAuthURL: server.URL + "/oauth", Now: func() time.Time { return now }}
	if _, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath}); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d", refreshCalls)
	}
	root := readAuthMap(t, authPath)
	xai := root["xai"].(map[string]any)
	if xai["access"] != "new-access" || xai["refresh"] != "new-refresh" || xai["custom"] != "keep" {
		t.Fatalf("persisted xai = %#v", xai)
	}
	wantExpires := now.Add(55 * time.Minute).UnixMilli()
	if got := int64(xai["expires"].(float64)); got != wantExpires {
		t.Fatalf("expires = %d, want %d", got, wantExpires)
	}
	other := root["github-copilot"].(map[string]any)
	if other["access"] != "other-secret" || other["unknown"] != true {
		t.Fatalf("unrelated credential changed: %#v", other)
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("auth mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRefreshDoesNotWriteOrDeleteSuccessorWhenLockIsReplaced(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{"xai": map[string]any{
		"type": "oauth", "access": "old-access", "refresh": "old-refresh", "expires": now.Add(-time.Minute).UnixMilli(),
	}})
	lockPath := authPath + ".lock"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		replaceLockWithSuccessor(t, lockPath)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"must-not-persist","expires_in":3600}`)),
		}, nil
	})}

	p := &Provider{HTTPClient: client, OAuthURL: "https://oauth.invalid/token"}
	_, err := p.refreshCredential(context.Background(), authPath, "old-access", false, now)
	assertCompromisedRefresh(t, err)
	root := readAuthMap(t, authPath)
	if got := root["xai"].(map[string]any)["access"]; got != "old-access" {
		t.Fatalf("credential was written after compromise: %#v", got)
	}
	if info, statErr := os.Stat(lockPath); statErr != nil || !info.IsDir() {
		t.Fatalf("successor lock was deleted: info=%v err=%v", info, statErr)
	}
}

func TestHeartbeatCompromiseCancelsInFlightRefresh(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{"xai": map[string]any{
		"type": "oauth", "access": "old-access", "refresh": "old-refresh", "expires": now.Add(-time.Minute).UnixMilli(),
	}})
	lockPath := authPath + ".lock"
	refreshCanceled := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		replaceLockWithSuccessor(t, lockPath)
		<-r.Context().Done()
		close(refreshCanceled)
		return nil, r.Context().Err()
	})}

	p := &Provider{HTTPClient: client, OAuthURL: "https://oauth.invalid/token"}
	_, err := p.refreshCredential(context.Background(), authPath, "old-access", false, now)
	assertCompromisedRefresh(t, err)
	select {
	case <-refreshCanceled:
	default:
		t.Fatal("heartbeat compromise did not cancel in-flight refresh")
	}
}

func replaceLockWithSuccessor(t *testing.T, lockPath string) {
	t.Helper()
	if err := os.Remove(lockPath); err != nil {
		t.Errorf("remove owned lock: %v", err)
	}
	if err := os.Mkdir(lockPath, 0700); err != nil {
		t.Errorf("create successor lock: %v", err)
	}
}

func assertCompromisedRefresh(t *testing.T, err error) {
	t.Helper()
	var providerErr *usageprovider.Error
	if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrTransientHTTPFailure || !errors.Is(err, piauth.ErrLockCompromised) {
		t.Fatalf("refresh error = %v, want typed transient compromise", err)
	}
}

func TestRefreshDoubleCheckUsesCredentialRotatedByAnotherProcess(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{"xai": map[string]any{
		"type": "oauth", "access": "already-rotated", "refresh": "rotated-refresh", "expires": now.Add(time.Hour).UnixMilli(),
	}})
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	p := &Provider{HTTPClient: server.Client(), OAuthURL: server.URL}
	cred, err := p.refreshCredential(context.Background(), authPath, "rejected-old-access", true, now)
	if err != nil {
		t.Fatalf("refreshCredential() error = %v", err)
	}
	if cred.Access != "already-rotated" || called {
		t.Fatalf("credential = %#v, refresh called = %t", cred, called)
	}
}

func TestRefreshPreservesOmittedRefreshToken(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{"xai": map[string]any{
		"type": "oauth", "access": "old-access", "refresh": "keep-refresh", "expires": now.Add(-time.Minute).UnixMilli(),
	}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new-access","expires_in":3600}`)
	}))
	defer server.Close()
	p := &Provider{HTTPClient: server.Client(), OAuthURL: server.URL, Now: func() time.Time { return now }}
	cred, err := p.refreshCredential(context.Background(), authPath, "old-access", false, now)
	if err != nil {
		t.Fatalf("refreshCredential() error = %v", err)
	}
	if cred.Refresh != "keep-refresh" {
		t.Fatalf("refresh = %q", cred.Refresh)
	}
	root := readAuthMap(t, authPath)
	if got := root["xai"].(map[string]any)["refresh"]; got != "keep-refresh" {
		t.Fatalf("persisted refresh = %#v", got)
	}
}

func TestRefreshRejectsUnsafeExpiresInBeforeDurationConversion(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	for _, expiresIn := range []int64{int64(refreshSkew / time.Second), math.MaxInt64} {
		t.Run(fmt.Sprint(expiresIn), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"access_token":"new-access","expires_in":%d}`, expiresIn)
			}))
			defer server.Close()
			p := &Provider{HTTPClient: server.Client(), OAuthURL: server.URL}
			_, err := p.requestRefresh(context.Background(), "refresh", now)
			var providerErr *usageprovider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrInvalidResponse {
				t.Fatalf("requestRefresh() error = %v, want invalid response", err)
			}
		})
	}
}

func TestReactiveRefreshOnce(t *testing.T) {
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authPath := writeAuth(t, map[string]any{"xai": map[string]any{
		"type": "oauth", "access": "rejected", "refresh": "refresh", "expires": now.Add(time.Hour).UnixMilli(),
	}})
	var refreshes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth" {
			refreshes++
			fmt.Fprint(w, `{"access_token":"accepted","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") == "Bearer rejected" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/user" {
			fmt.Fprint(w, `{"userId":"user"}`)
			return
		}
		fmt.Fprint(w, `{"config":{"creditUsagePercent":20,"currentPeriod":{"type":"WEEKLY"}}}`)
	}))
	defer server.Close()
	p := &Provider{HTTPClient: server.Client(), BaseURL: server.URL + "/v1", OAuthURL: server.URL + "/oauth", Now: func() time.Time { return now }}
	if _, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath}); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d", refreshes)
	}
}

func TestHTTPClassificationAndBoundedResponse(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want usageprovider.ErrorCode
	}{
		{name: "unauthorized", code: 401, want: usageprovider.ErrAuthExpired},
		{name: "forbidden", code: 403, want: usageprovider.ErrAuthExpired},
		{name: "rate limited", code: 429, want: usageprovider.ErrRateLimited},
		{name: "server", code: 500, body: `{"error":"upstream-secret"}`, want: usageprovider.ErrTransientHTTPFailure},
		{name: "malformed", code: 200, body: `{`, want: usageprovider.ErrInvalidResponse},
		{name: "oversize", code: 200, body: strings.Repeat("x", maxResponseBytes+1), want: usageprovider.ErrInvalidResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			p := &Provider{HTTPClient: server.Client(), BaseURL: server.URL}
			err := p.getJSON(context.Background(), "/user", map[string]string{}, &userResponse{})
			var providerErr *usageprovider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != tt.want {
				t.Fatalf("error = %v, want %s", err, tt.want)
			}
			if strings.Contains(fmt.Sprint(err), "upstream-secret") {
				t.Fatalf("error leaked response body: %v", err)
			}
		})
	}
}

func TestRedirectIsRejected(t *testing.T) {
	var followed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			followed = true
			fmt.Fprint(w, `{}`)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()
	p := &Provider{HTTPClient: server.Client(), BaseURL: server.URL}
	err := p.getJSON(context.Background(), "/user", map[string]string{}, &userResponse{})
	var providerErr *usageprovider.Error
	if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrTransientHTTPFailure || followed {
		t.Fatalf("error = %v, followed = %t", err, followed)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func money(value int64) *moneyValue { return &moneyValue{Val: &value} }

func writeAuth(t *testing.T, value map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readAuthMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return root
}
