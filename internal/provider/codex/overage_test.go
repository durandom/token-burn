package codex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/durandom/token-burn/internal/provider"
)

func TestFetchTypedOverageCredits(t *testing.T) {
	for _, tc := range []struct {
		name, payload, balance     string
		unlimited, reached, absent bool
	}{
		{name: "balance and estimates", payload: `{"credits":{"has_credits":true,"unlimited":false,"overage_limit_reached":true,"balance":"62750.0000000000","approx_local_messages":[10,20],"approx_cloud_messages":[3,7]}}`, balance: "62750.0000000000", reached: true},
		{name: "zero", payload: `{"credits":{"has_credits":false,"balance":"0.0000000000"}}`, balance: "0.0000000000"},
		{name: "unlimited", payload: `{"credits":{"has_credits":true,"unlimited":true,"balance":null}}`, unlimited: true},
		{name: "nested fallback", payload: `{"rate_limit_status":{"credits":{"has_credits":true,"balance":"42.125"}}}`, balance: "42.125"},
		{name: "missing", payload: `{}`, absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/wham/usage" {
					t.Error("overage credits must use only existing usage GET")
				}
				fmt.Fprint(w, tc.payload)
			}))
			defer server.Close()
			snap, err := (&Provider{BaseURL: server.URL}).Fetch(context.Background(), provider.Account{ID: "local", AuthFile: writeAuth(t, `{"tokens":{"access_token":"fake-token"}}`)})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || snap.Resets != nil {
				t.Fatal("overage credits must not create resets or additional calls")
			}
			if tc.absent {
				if snap.OverageCredits != nil {
					t.Fatal("missing balance fabricated")
				}
				return
			}
			c := snap.OverageCredits
			if c == nil || c.Balance != tc.balance || c.Unlimited != tc.unlimited || c.OverageLimitReached != tc.reached {
				t.Fatalf("credits %+v", c)
			}
			if tc.name == "balance and estimates" && (len(c.ApproxLocalMessages) != 2 || c.ApproxLocalMessages[1] != 20 || c.ApproxCloudMessages[0] != 3) {
				t.Fatal("message range lost")
			}
		})
	}
}
