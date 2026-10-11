package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

func TestFetchResetPrograms(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		wantCount    int
	}{{"eligible", "", 3}, {"wrong surface", "surface", -1}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/usage" {
					t.Error("must GET usage only; never reset_rate_limits")
				}
				if r.URL.Query().Get("at_wall") != "1" || r.URL.Query().Get("skip_spend") != "1" {
					t.Error("missing reset query")
				}
				if r.Header.Get("User-Agent") != "claude-cli/9.9.9 (external, cli)" {
					t.Error("missing configured CLI surface")
				}
				fmt.Fprintf(w, `{"five_hour":{"utilization":45},"cedar_ember":{"eligible":true,"ineligible_reason":%q,"at_limit":false,"exhausted":[],"grants":[{"id":"fake-grant","label":"Weekly reset","resets_total":4,"resets_left":3,"starts_at":"2026-10-10T12:00:00+02:00","ends_at":"2026-10-17T10:00:00Z","clears":["seven_day"],"paused":false,"usable_now":true,"use_requires_limit":true,"percent_used":{"seven_day":45},"blocking":[],"arm":"fake-arm"}],"next_grant_id":"fake-next","weekly_resets_at":"2026-10-17T10:00:00Z","cooldown_until":null},"juniper_tide":{"eligible":true,"ineligible_reason":null,"in_experiment":true,"arm":"fake-arm","available":true,"next_available_at":null,"weekly_resets_at":"2026-10-17T10:00:00Z","resets_per_week":1}}`, tc.reason)
			}))
			defer server.Close()
			p := &Provider{BaseURL: server.URL, Env: func(k string) string {
				if k == "CLAUDE_CODE_OAUTH_TOKEN" {
					return "fake-token"
				}
				return ""
			}, Now: func() time.Time { return time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC) }}
			snap, err := p.Fetch(context.Background(), provider.Account{ID: "local", ClaudeCLIVersion: "9.9.9"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantCount < 0 {
				if snap.Resets != nil || snap.ResetDetails.CreditsComplete {
					t.Fatal("surface ineligible must not clear stored grants")
				}
				return
			}
			if snap.Resets.AvailableCount != tc.wantCount || snap.Resets.ApplicableAvailableCount != tc.wantCount || len(snap.ResetDetails.Credits) != 1 {
				t.Fatal("grant count mapping")
			}
			c := snap.ResetDetails.Credits[0]
			if c.GrantedAt.Hour() != 10 || !c.UseRequiresLimit || len(c.Clears) != 1 || c.Title != "Weekly reset" {
				t.Fatalf("grant mapping %+v", c)
			}
			if snap.ResetDetails.Metadata["juniper_tide"] == nil {
				t.Fatal("weekly program missing")
			}
		})
	}
}
