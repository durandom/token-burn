package forecast

import (
	"math"
	"sort"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

type CreditGrantCapacity struct {
	Label            string    `json:"label"`
	ConfiguredAmount float64   `json:"configured_amount"`
	Amount           *float64  `json:"amount"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type ConfiguredCreditCapacity struct {
	Source         string                `json:"source"`
	Grants         []CreditGrantCapacity `json:"grants"`
	NextExpiry     time.Time             `json:"next_expiry"`
	AmountExpiring *float64              `json:"amount_expiring"`
}

// CreditExpiry overlays configured dates without assigning usage to a grant.
// Each amount, and the total at the next expiry, is independently capped at
// the known live balance. These caps must not be summed as remaining grants.
func CreditExpiry(grants []provider.ConfiguredCreditGrant, live *provider.OverageCredits, now time.Time) *ConfiguredCreditCapacity {
	out := &ConfiguredCreditCapacity{Source: "configured"}
	balance, known := live.NumericBalance()
	balance = math.Max(0, balance)
	for _, g := range grants {
		if !g.ExpiresAt.After(now) {
			continue
		}
		item := CreditGrantCapacity{Label: g.Label, ConfiguredAmount: g.Amount, ExpiresAt: g.ExpiresAt.UTC()}
		if known {
			v := math.Min(g.Amount, balance)
			item.Amount = &v
		}
		out.Grants = append(out.Grants, item)
	}
	if len(out.Grants) == 0 {
		return nil
	}
	sort.SliceStable(out.Grants, func(i, j int) bool { return out.Grants[i].ExpiresAt.Before(out.Grants[j].ExpiresAt) })
	out.NextExpiry = out.Grants[0].ExpiresAt
	if known {
		sum := 0.0
		for _, g := range out.Grants {
			if g.ExpiresAt.Equal(out.NextExpiry) {
				sum += g.ConfiguredAmount
			}
		}
		v := math.Min(sum, balance)
		out.AmountExpiring = &v
	}
	return out
}
