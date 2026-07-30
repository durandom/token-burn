package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

type fakeRunner struct {
	responses map[string][]byte
	errs      map[string]error
	calls     []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return f.responses[key], nil
}

func TestFetchMapsCopilotQuotaAndAICredits(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	runner := &fakeRunner{responses: map[string][]byte{}, errs: map[string]error{}}
	runner.responses["gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user"] = []byte(`{
		"login": "durandom",
		"access_type_sku": "max_monthly_subscriber_quota",
		"copilot_plan": "individual_max",
		"quota_reset_date_utc": "2026-07-01T00:00:00.000Z",
		"token_based_billing": true,
		"quota_snapshots": {
			"premium_interactions": {
				"has_quota": true,
				"entitlement": 20000,
				"remaining": 15000,
				"percent_remaining": 75,
				"unlimited": false
			},
			"chat": {
				"has_quota": true,
				"unlimited": true
			}
		}
	}`)
	runner.responses["gh api -H Cache-Control: no-cache -H Pragma: no-cache /users/durandom/settings/billing/ai_credit/usage?year=2026&month=6"] = []byte(`{
		"timePeriod": {"year": 2026, "month": 6},
		"user": "durandom",
		"usageItems": [
			{"product": "Copilot AI Credits", "sku": "AI Credit", "model": "GPT-5", "unitType": "ai-credits", "grossQuantity": 2000, "grossAmount": 20, "discountQuantity": 2000, "discountAmount": 20, "netQuantity": 0, "netAmount": 0},
			{"product": "Copilot AI Credits", "sku": "AI Credit", "model": "Claude Sonnet", "unitType": "ai-credits", "grossQuantity": 1000, "grossAmount": 10, "discountQuantity": 1000, "discountAmount": 10, "netQuantity": 0, "netAmount": 0}
		]
	}`)

	snap, err := (&Provider{Runner: runner, Now: func() time.Time { return now }}).Fetch(context.Background(), usageprovider.Account{ID: "copilot-default"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if snap.Provider != "copilot" || snap.AccountID != "copilot-default" || snap.PlanType != "individual_max" {
		t.Fatalf("snapshot metadata = %#v", snap)
	}
	assertWindow(t, snap, "premium_interactions", 25, 75, "2026-07-01T00:00:00Z")
	assertWindow(t, snap, "chat", 0, 100, "2026-07-01T00:00:00Z")
	assertWindow(t, snap, "ai_credits", 15, 85, "2026-07-01T00:00:00Z")
	if got := snap.Raw["ai_credit_gross_amount_usd"]; got != 30.0 {
		t.Fatalf("ai_credit_gross_amount_usd = %#v, want 30", got)
	}
	if got := snap.Raw["ai_credit_net_amount_usd"]; got != 0.0 {
		t.Fatalf("ai_credit_net_amount_usd = %#v, want 0", got)
	}
	if got := snap.Raw["quota_premium_interactions_entitlement"]; got != 20000.0 {
		t.Fatalf("quota_premium_interactions_entitlement = %#v, want 20000", got)
	}
	if got := snap.Raw["quota_premium_interactions_remaining"]; got != 15000.0 {
		t.Fatalf("quota_premium_interactions_remaining = %#v, want 15000", got)
	}
	if got := snap.Raw["quota_premium_interactions_unlimited"]; got != false {
		t.Fatalf("quota_premium_interactions_unlimited = %#v, want false", got)
	}
	if got := snap.Raw["quota_chat_unlimited"]; got != true {
		t.Fatalf("quota_chat_unlimited = %#v, want true", got)
	}
}

func TestFetchKeepsQuotaWhenBillingUsageFails(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	runner := &fakeRunner{
		responses: map[string][]byte{
			"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": []byte(`{
				"login": "durandom",
				"copilot_plan": "individual_pro",
				"quota_reset_date": "2026-07-01",
				"token_based_billing": true,
				"quota_snapshots": {
					"premium_interactions": {"has_quota": true, "percent_remaining": 90}
				}
			}`),
		},
		errs: map[string]error{
			"gh api -H Cache-Control: no-cache -H Pragma: no-cache /users/durandom/settings/billing/ai_credit/usage?year=2026&month=6": errors.New("forbidden"),
		},
	}

	snap, err := (&Provider{Runner: runner, Now: func() time.Time { return now }}).Fetch(context.Background(), usageprovider.Account{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	assertWindow(t, snap, "premium_interactions", 10, 90, "2026-07-01T00:00:00Z")
	if _, ok := snap.Raw["ai_credit_usage_error"]; !ok {
		t.Fatal("missing ai_credit_usage_error")
	}
}

func TestFetchMapsGHFailureToAuthMissing(t *testing.T) {
	runner := &fakeRunner{
		responses: map[string][]byte{},
		errs: map[string]error{
			"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": errors.New("not logged in"),
		},
	}
	_, err := (&Provider{
		Runner:  runner,
		HomeDir: func() (string, error) { return t.TempDir(), nil },
		Env:     func(string) string { return "" },
	}).Fetch(context.Background(), usageprovider.Account{})
	if err == nil {
		t.Fatal("Fetch() error = nil")
	}
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthMissing {
		t.Fatalf("error = %#v, want auth missing provider error", err)
	}
}

func TestGHPreferredOverConfiguredPiAuth(t *testing.T) {
	authPath := writePiAuth(t, map[string]any{"github-copilot": map[string]any{"type": "oauth", "access": "proxy", "refresh": "github-secret"}})
	runner := &fakeRunner{responses: map[string][]byte{
		"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": []byte(`{"login":"gh-user","quota_snapshots":{"chat":{"unlimited":true}}}`),
	}, errs: map[string]error{}}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	p := &Provider{Runner: runner, HTTPClient: server.Client(), BaseURL: server.URL}
	if _, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("Pi HTTP fallback called while gh succeeded")
	}
}

func TestPiFallbackAfterInvalidGHResponse(t *testing.T) {
	authPath := writePiAuth(t, map[string]any{"github-copilot": map[string]any{"type": "oauth", "access": "proxy", "refresh": "github-token"}})
	runner := &fakeRunner{responses: map[string][]byte{
		"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": []byte(`{`),
	}, errs: map[string]error{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"login":"pi-user","quota_snapshots":{"chat":{"unlimited":true}}}`)
	}))
	defer server.Close()
	p := &Provider{Runner: runner, HTTPClient: server.Client(), BaseURL: server.URL}
	snap, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Raw["auth_source"] != "pi" {
		t.Fatalf("raw = %#v", snap.Raw)
	}
}

func TestPiFallbackUsesGitHubTokenForQuotaAndBilling(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	authPath := writePiAuth(t, map[string]any{"github-copilot": map[string]any{"type": "oauth", "access": "short-lived-proxy", "refresh": "github-oauth-token", "expires": 1}})
	runner := &fakeRunner{responses: map[string][]byte{}, errs: map[string]error{
		"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": errors.New("not logged in"),
	}}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer github-oauth-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if strings.Contains(r.Header.Get("Authorization"), "short-lived-proxy") {
			t.Fatal("used Copilot proxy token")
		}
		switch r.URL.Path {
		case "/copilot_internal/user":
			fmt.Fprint(w, `{"login":"pi-user","copilot_plan":"individual_pro","quota_reset_date":"2026-07-01","token_based_billing":true,"quota_snapshots":{"premium_interactions":{"percent_remaining":80}}}`)
		default:
			fmt.Fprint(w, `{"usageItems":[{"unitType":"ai-credits","grossQuantity":150}]}`)
		}
	}))
	defer server.Close()
	p := &Provider{Runner: runner, HTTPClient: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}
	snap, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if snap.Raw["auth_source"] != "pi" {
		t.Fatalf("raw = %#v", snap.Raw)
	}
	assertWindow(t, snap, "premium_interactions", 20, 80, "2026-07-01T00:00:00Z")
	assertWindow(t, snap, "ai_credits", 10, 90, "2026-07-01T00:00:00Z")
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestBillingEndpointEscapesLogin(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	got := billingEndpoint("user/name?admin=true", now)
	want := "/users/user%2Fname%3Fadmin=true/settings/billing/ai_credit/usage?year=2026&month=6"
	if got != want {
		t.Fatalf("billing endpoint = %q, want %q", got, want)
	}
}

func TestPiFallbackRejectsEnterpriseCredentialWithoutSendingToken(t *testing.T) {
	authPath := writePiAuth(t, map[string]any{"github-copilot": map[string]any{
		"type": "oauth", "access": "proxy", "refresh": "enterprise-secret", "enterpriseUrl": "company.ghe.com",
	}})
	runner := &fakeRunner{responses: map[string][]byte{}, errs: map[string]error{
		"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": errors.New("not logged in"),
	}}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	_, err := (&Provider{Runner: runner, HTTPClient: server.Client(), BaseURL: server.URL}).Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrUnsupportedAccountShape {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("enterprise token was sent to public GitHub fallback")
	}
	if strings.Contains(fmt.Sprint(err), "enterprise-secret") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestPiFallbackRejectsWrongCredentialWithoutSecretLeak(t *testing.T) {
	authPath := writePiAuth(t, map[string]any{"github-copilot": map[string]any{"type": "api_key", "key": "must-not-leak"}})
	runner := &fakeRunner{responses: map[string][]byte{}, errs: map[string]error{
		"gh api -H Cache-Control: no-cache -H Pragma: no-cache /copilot_internal/user": errors.New("not logged in"),
	}}
	_, err := (&Provider{Runner: runner}).Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthMissing {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "must-not-leak") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestPiGitHubHTTPClassificationBoundsAndRedirect(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		want    usageprovider.ErrorCode
	}{
		{name: "unauthorized", status: 401, want: usageprovider.ErrAuthExpired},
		{name: "forbidden permissions", status: 403, want: usageprovider.ErrAuthMissing},
		{name: "forbidden rate limit", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0"}, want: usageprovider.ErrRateLimited},
		{name: "rate", status: 429, want: usageprovider.ErrRateLimited},
		{name: "server", status: 500, body: `{"error":"must-not-leak"}`, want: usageprovider.ErrTransientHTTPFailure},
		{name: "oversize", status: 200, body: strings.Repeat("x", maxResponseBytes+1), want: usageprovider.ErrInvalidResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range tt.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			err := (&Provider{HTTPClient: server.Client(), BaseURL: server.URL}).getJSON(context.Background(), "secret", "/x", &map[string]any{})
			var perr *usageprovider.Error
			if !errors.As(err, &perr) || perr.Code != tt.want {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(fmt.Sprint(err), "must-not-leak") || strings.Contains(fmt.Sprint(err), "secret") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	followed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			followed = true
			fmt.Fprint(w, `{}`)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()
	err := (&Provider{HTTPClient: server.Client(), BaseURL: server.URL}).getJSON(context.Background(), "secret", "/x", &map[string]any{})
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrTransientHTTPFailure || followed {
		t.Fatalf("redirect error = %v followed=%t", err, followed)
	}
}

func writePiAuth(t *testing.T, value map[string]any) string {
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

func assertWindow(t *testing.T, snap usageprovider.Snapshot, name string, used, remaining float64, reset string) {
	t.Helper()
	for _, win := range snap.Windows {
		if win.Name != name {
			continue
		}
		if win.UsedPercent != used {
			t.Fatalf("%s used = %v, want %v", name, win.UsedPercent, used)
		}
		if win.RemainingPercent == nil || *win.RemainingPercent != remaining {
			t.Fatalf("%s remaining = %v, want %v", name, win.RemainingPercent, remaining)
		}
		if win.ResetAt == nil || win.ResetAt.Format(time.RFC3339) != reset {
			t.Fatalf("%s reset = %v, want %s", name, win.ResetAt, reset)
		}
		return
	}
	t.Fatalf("missing window %q in %#v", name, snap.Windows)
}
