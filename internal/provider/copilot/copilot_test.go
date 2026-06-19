package copilot

import (
	"context"
	"errors"
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
	_, err := (&Provider{Runner: runner}).Fetch(context.Background(), usageprovider.Account{})
	if err == nil {
		t.Fatal("Fetch() error = nil")
	}
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrAuthMissing {
		t.Fatalf("error = %#v, want auth missing provider error", err)
	}
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
