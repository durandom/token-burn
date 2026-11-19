package claudeplatform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

const creditFixture = `{"amount":21776,"currency":"USD","balance":{"credits":{"amount_minor":21776,"exponent":2}},"tranches":[{"id":"purchase","program_id":"api_credits","remaining_amount_minor_units":1775,"granted_amount_minor_units":2500,"currency":"USD","expires_at":"2027-03-07T00:00:00Z"}],"promo_tranches":[{"id":"promo","program_id":"subscription_api_credit","remaining_amount_minor_units":20000,"granted_amount_minor_units":20000,"currency":"USD","expires_at":"2026-11-13T00:00:00Z","scope":{"kind":"scoped","applies_to":[{"product":"api"}]}}]}`

func testProvider(server *httptest.Server) *Provider {
	return &Provider{BaseURL: server.URL, HTTPClient: server.Client(), ReadSession: func(context.Context, string, string) (string, error) { return "synthetic-session", nil }}
}

func TestFetchConsoleCredits(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		c, err := r.Cookie("sessionKey")
		if err != nil || c.Value != "synthetic-session" || r.Method != http.MethodGet {
			t.Error("invalid request credentials or method")
		}
		if r.UserAgent() == "" {
			t.Error("missing Console user agent")
		}
		if r.URL.Path == "/api/organizations" {
			fmt.Fprint(w, `[{"uuid":"chat","capabilities":["chat"]},{"uuid":"api-org","capabilities":["api"]}]`)
			return
		}
		if r.URL.Path != "/api/organizations/api-org/prepaid/credits" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, creditFixture)
	}))
	defer server.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	p := testProvider(server)
	p.Now = func() time.Time { return now }
	snap, err := p.Fetch(context.Background(), provider.Account{ID: "console"})
	if err != nil {
		t.Fatal(err)
	}
	c := snap.CreditBalance
	if snap.Provider != ID || snap.ObservedAt != now || len(snap.Windows) != 0 || c.Balance.String() != "$217.76" || len(c.Grants) != 2 {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
	if c.NextExpiry(now).Format("2006-01-02") != "2026-11-13" || c.Grants[1].Scope.AppliesTo[0].Product != "api" {
		t.Fatal("grant metadata lost")
	}
	if len(paths) != 2 {
		t.Fatalf("requests=%v", paths)
	}
}

func TestConsoleFailuresAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   provider.ErrorCode
	}{
		{"unauthorized", 401, `synthetic-session`, provider.ErrAuthExpired},
		{"forbidden", 403, `synthetic-session`, provider.ErrAuthExpired},
		{"rate", 429, `synthetic-session`, provider.ErrRateLimited},
		{"outage", 500, `synthetic-session`, provider.ErrTransientHTTPFailure},
		{"malformed", 200, `{"amount":"synthetic-session"}`, provider.ErrInvalidResponse},
		{"missing", 200, `{}`, provider.ErrInvalidResponse},
		{"fractional", 200, `{"amount":217.76,"currency":"USD"}`, provider.ErrInvalidResponse},
		{"negative", 200, `{"amount":-1,"currency":"USD"}`, provider.ErrInvalidResponse},
		{"bad exponent", 200, `{"currency":"USD","balance":{"credits":{"amount_minor":10,"exponent":8}}}`, provider.ErrInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			_, err := testProvider(server).Fetch(context.Background(), provider.Account{ID: "test", ProviderAccountID: "org"})
			var e *provider.Error
			if !errors.As(err, &e) || e.Code != tc.code {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-session") {
				t.Fatal("secret in error")
			}
		})
	}
}

func TestNoRedirectLeaksCookie(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	_, err := testProvider(source).Fetch(context.Background(), provider.Account{ID: "test", ProviderAccountID: "org"})
	if err == nil || called {
		t.Fatal("redirect followed")
	}
}

func TestSessionReaderErrorIsSanitized(t *testing.T) {
	p := &Provider{ReadSession: func(context.Context, string, string) (string, error) {
		return "", errors.New("sessionKey=synthetic-session")
	}}
	_, err := p.Fetch(context.Background(), provider.Account{ID: "console"})
	var e *provider.Error
	if !errors.As(err, &e) || e.Code != provider.ErrAuthMissing || strings.Contains(err.Error(), "synthetic-session") {
		t.Fatal("expected sanitized authentication error")
	}
}

func TestMoneyExponentAndUnknownExpiry(t *testing.T) {
	// Exercise non-cent units and malformed optional expiry without corrupting balance.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"amount":12345,"currency":"USD","balance":{"credits":{"amount_minor":12345,"exponent":3}},"tranches":[{"remaining_amount_minor_units":5,"expires_at":"unknown"}]}`)
	}))
	defer server.Close()
	snap, err := testProvider(server).Fetch(context.Background(), provider.Account{ID: "test", ProviderAccountID: "org"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.CreditBalance.Balance.String() != "$12.345" || snap.CreditBalance.Grants[0].ExpiresAt != nil {
		t.Fatal("invalid monetary normalization")
	}
}

func TestMultipleOrganizationsNeedSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"uuid":"one","capabilities":["api"]},{"uuid":"two","capabilities":["api"]}]`)
	}))
	defer server.Close()
	_, err := testProvider(server).Fetch(context.Background(), provider.Account{ID: "test"})
	if err == nil || !strings.Contains(err.Error(), "provider_account_id") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestPrivateSessionFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session")
	ctx := context.Background()
	if err := ImportSession(ctx, "test", path, "synthetic-session\n"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSession(ctx, "test", path)
	if err != nil || got != "synthetic-session" {
		t.Fatalf("read: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSession(ctx, "test", path); err == nil {
		t.Fatal("accepted world-readable secret")
	}
	for _, value := range []string{"", "sk-ant-api03-fake", "abc\nnext-command", "abc;other-cookie", strings.Repeat("a", 2049)} {
		if _, err := NormalizeSession(value); err == nil {
			t.Fatal("accepted invalid session")
		}
	}
}
