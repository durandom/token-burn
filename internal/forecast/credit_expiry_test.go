package forecast

import (
	"github.com/durandom/token-burn/internal/provider"
	"testing"
	"time"
)

func TestConfiguredCreditExpiryCapsWithoutConsumptionOrder(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	next := now.Add(time.Hour)
	grants := []provider.ConfiguredCreditGrant{
		{Label: "later", Amount: 1000, ExpiresAt: now.Add(2 * time.Hour)},
		{Label: "expired", Amount: 500, ExpiresAt: now},
		{Label: "first", Amount: 100, ExpiresAt: next},
		{Label: "same expiry", Amount: 200, ExpiresAt: next},
	}
	for _, tc := range []struct {
		name     string
		live     *provider.OverageCredits
		expiring float64
		known    bool
	}{
		{"below group total", &provider.OverageCredits{Balance: "150.0000000000"}, 150, true},
		{"above group total", &provider.OverageCredits{Balance: "700"}, 300, true},
		{"zero", &provider.OverageCredits{Balance: "0"}, 0, true},
		{"negative balance", &provider.OverageCredits{Balance: "-10"}, 0, true},
		{"missing balance", nil, 0, false},
		{"unlimited without balance", &provider.OverageCredits{Unlimited: true}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := CreditExpiry(grants, tc.live, now)
			if c == nil || c.Source != "configured" || len(c.Grants) != 3 || !c.NextExpiry.Equal(next) {
				t.Fatalf("expiry %+v", c)
			}
			if tc.known {
				if c.AmountExpiring == nil || *c.AmountExpiring != tc.expiring {
					t.Fatal("next expiry cap")
				}
			} else if c.AmountExpiring != nil {
				t.Fatal("unknown balance must not imply an amount")
			}
			if c.Grants[0].ConfiguredAmount != 100 || c.Grants[2].ConfiguredAmount != 1000 {
				t.Fatal("configured grant modified")
			}
			if tc.name == "below group total" && (*c.Grants[0].Amount != 100 || *c.Grants[1].Amount != 150 || *c.Grants[2].Amount != 150) {
				t.Fatal("caps allocated a consumption order")
			}
		})
	}
	if CreditExpiry(nil, nil, now) != nil || CreditExpiry(grants, nil, now.Add(3*time.Hour)) != nil {
		t.Fatal("expired grants retained")
	}
}
