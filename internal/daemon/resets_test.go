package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/otel"
	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/provider/codex"
	"github.com/durandom/token-burn/internal/store"
)

func TestPollResetCadenceAcrossProviderInstances(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("reset polling must GET")
		}
		counts[r.URL.Path]++
		switch r.URL.Path {
		case "/wham/usage":
			fmt.Fprint(w, `{"rate_limit_reset_credits":{"available_count":1},"rate_limit":{"primary_window":{"used_percent":15}}}`)
		case "/wham/rate-limit-reset-credits":
			fmt.Fprint(w, `{"credits":[{"id":"fake-credit","status":"available","expires_at":"2026-10-20T10:00:00Z"}],"available_count":1}`)
		case "/wham/rate-limit-reset-credits/history":
			fmt.Fprint(w, `{"events":[],"next_cursor":null}`)
		case "/wham/usage/plan_limit_history":
			fmt.Fprint(w, `{"periods":[],"coverage_complete":true}`)
		default:
			t.Error("unexpected endpoint")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"fake-token","account_id":"fake-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Provider: "codex", ID: "local", AuthFile: auth}}
	for _, minutes := range []int{0, 1, 14, 15} {
		now = time.Date(2026, 10, 11, 10, minutes, 0, 0, time.UTC)
		p := &codex.Provider{BaseURL: server.URL, Now: func() time.Time { return now }}
		result, err := PollOnce(ctx, db, Options{Config: cfg, Now: func() time.Time { return now }, Providers: map[string]provider.Provider{"codex": p}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) > 0 || len(result.Snapshots) != 1 || len(result.Snapshots[0].ResetDetails.Credits) != 1 {
			t.Fatalf("cached resets missing %+v", result)
		}
		// Reopening simulates a daemon restart; the slow cadence is durable.
		db.Close()
		db, err = store.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer db.Close()
	if counts["/wham/usage"] != 4 || counts["/wham/rate-limit-reset-credits"] != 2 || counts["/wham/usage/plan_limit_history"] != 2 {
		t.Fatalf("poll counts %+v", counts)
	}
}

func TestPollConfiguredCreditExpiryMetrics(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Provider: "codex", ID: "local"}}
	cfg.CreditGrants = map[string][]provider.ConfiguredCreditGrant{"codex": {{Label: "Pro promo", Amount: 62500, ExpiresAt: now.Add(time.Hour)}}}
	recorder := &fakeRecorder{}
	p := fakeProvider{id: "codex", snap: provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now, OverageCredits: &provider.OverageCredits{Balance: "100"}}}
	_, err = PollOnce(ctx, db, Options{Config: cfg, Providers: map[string]provider.Provider{"codex": p}, Recorder: recorder, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{otel.MetricCreditSecondsToNextExpiry, otel.MetricCreditAmountExpiring} {
		found := false
		for _, g := range recorder.gauges {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing daemon metric %s", want)
		}
	}
}
