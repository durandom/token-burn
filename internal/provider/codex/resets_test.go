package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

func TestResetDetailsReadOnlyPaginationAndCadence(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || strings.Contains(r.URL.Path, "consume") {
			t.Error("reset discovery must only GET")
		}
		if r.Header.Get("Authorization") != "Bearer fake-token" || r.Header.Get("ChatGPT-Account-Id") != "fake-account" {
			t.Error("missing usage credentials")
		}
		counts[r.URL.Path]++
		switch r.URL.Path {
		case "/wham/usage":
			fmt.Fprint(w, `{"rate_limit_reset_credits":{"available_count":1,"applicable_available_count":1}}`)
		case "/wham/rate-limit-reset-credits":
			fmt.Fprint(w, `{"credits":[{"id":"fake-credit","reset_type":"weekly","is_supported_by_plan":true,"status":"available","granted_at":1791626400,"expires_at":"2026-10-20T12:00:00+02:00","redeem_started_at":null,"redeemed_at":null,"title":"Weekly reset","description":"A reset"}],"available_count":1,"total_earned_count":2,"history_enabled":true,"immediate_reset_purchase_eligible":false}`)
		case "/wham/rate-limit-reset-credits/history":
			if r.URL.Query().Get("cursor") == "next/value" {
				fmt.Fprint(w, `{"events":[{"id":"fake-old:used","kind":"used","occurred_at":"2026-10-10T09:00:00Z"}],"next_cursor":null}`)
			} else {
				fmt.Fprint(w, `{"events":[{"id":"fake-credit:granted","kind":"granted","occurred_at":"2026-10-10T10:00:00Z"}],"window_start":"2026-09-11T00:00:00Z","as_of":"2026-10-11T10:00:00Z","next_cursor":"next/value"}`)
			}
		case "/wham/usage/plan_limit_history":
			if r.URL.Query().Get("days") != "7" {
				t.Error("plan history days")
			}
			fmt.Fprint(w, `{"periods":[{"id":"fake-period","window_minutes":10080,"plan_type":"plus","starts_at":"2026-10-01T10:00:00Z","ends_at":"2026-10-08T10:00:00Z","accounting_complete":true,"used_basis_points":3456.760161,"breakdowns":[{"dimension":"model","rows":[{"key":"fake-model","basis_points":3456.760161}]}]}],"data_as_of":"2026-10-11T10:00:00Z","coverage_start":"2026-10-01T10:00:00Z","coverage_complete":true,"approximate":true,"boundary_tolerance_seconds":60}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := &Provider{BaseURL: server.URL, Now: func() time.Time { return now }}
	acct := provider.Account{ID: "local", AuthFile: writeAuth(t, `{"tokens":{"access_token":"fake-token","account_id":"fake-account"}}`)}
	snap, err := p.Fetch(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Resets.AvailableCount != 1 || snap.ResetDetails == nil || len(snap.ResetDetails.Credits) != 1 || len(snap.ResetDetails.Events) != 2 {
		t.Fatalf("reset details: %+v", snap)
	}
	c := snap.ResetDetails.Credits[0]
	if c.ResetsLeft != 1 || c.ExpiresAt.Hour() != 10 || c.ExpiresAt.Location() != time.UTC {
		t.Fatalf("normalized credit %+v", c)
	}
	if snap.ResetDetails.Events[1].CreditID != "fake-old" || snap.ResetDetails.Events[1].Source != "api" {
		t.Fatal("history mapping")
	}
	if snap.PlanLimitHistory.Periods[0].Breakdowns[0].Rows[0].BasisPoints != 3456.760161 || !snap.PlanLimitHistory.Approximate {
		t.Fatal("plan history mapping")
	}
	now = now.Add(time.Minute)
	snap, err = p.Fetch(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ResetDetailsAttemptedAt != nil || counts["/wham/rate-limit-reset-credits"] != 1 {
		t.Fatal("details polled too soon")
	}
	now = now.Add(14 * time.Minute)
	if _, err = p.Fetch(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	if counts["/wham/rate-limit-reset-credits"] != 2 {
		t.Fatal("details not refreshed after cadence")
	}
	// A new provider instance honors the persisted attempt deadline too.
	after := now.Add(15 * time.Minute)
	acct.ResetDetailsAfter = &after
	p = &Provider{BaseURL: server.URL, Now: func() time.Time { return now.Add(time.Minute) }}
	if _, err = p.Fetch(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	if counts["/wham/rate-limit-reset-credits"] != 2 {
		t.Fatal("stored cadence ignored")
	}
}

func TestResetDetailFailuresPreserveUsage(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("non-GET")
				}
				if r.URL.Path == "/wham/usage" {
					fmt.Fprint(w, `{"rate_limit_reset_credits":{"available_count":2},"rate_limit":{"primary_window":{"used_percent":12}}}`)
				} else {
					w.WriteHeader(status)
				}
			}))
			defer server.Close()
			p := &Provider{BaseURL: server.URL}
			snap, err := p.Fetch(context.Background(), provider.Account{ID: "local", AuthFile: writeAuth(t, `{"tokens":{"access_token":"fake-token"}}`)})
			if err != nil {
				t.Fatal(err)
			}
			if len(snap.Windows) != 1 || snap.Resets.AvailableCount != 2 || len(snap.ResetDetailsErrors) == 0 || snap.ResetDetailsAttemptedAt == nil {
				t.Fatal("optional detail failure lost usage or retry deadline")
			}
		})
	}
}
