package config

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

type fileCreditGrant struct {
	Label     string  `toml:"label"`
	Amount    float64 `toml:"amount"`
	ExpiresAt string  `toml:"expires_at"`
}

func parseCreditGrants(raw map[string][]fileCreditGrant) (map[string][]provider.ConfiguredCreditGrant, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := map[string][]provider.ConfiguredCreditGrant{}
	for name, grants := range raw {
		if name == "" || name != strings.ToLower(strings.TrimSpace(name)) {
			return nil, fmt.Errorf("credit_grants provider must be a lowercase identifier")
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return nil, fmt.Errorf("invalid credit_grants provider %q", name)
			}
		}
		for i, g := range grants {
			if g.Amount <= 0 || math.IsNaN(g.Amount) || math.IsInf(g.Amount, 0) {
				return nil, fmt.Errorf("credit_grants.%s[%d].amount must be positive and finite", name, i)
			}
			at, err := time.Parse(time.RFC3339, g.ExpiresAt)
			if err != nil {
				at, err = time.Parse("2006-01-02", g.ExpiresAt)
				if err == nil {
					at = at.Add(24*time.Hour - time.Second)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("credit_grants.%s[%d].expires_at must be RFC3339 or YYYY-MM-DD", name, i)
			}
			out[name] = append(out[name], provider.ConfiguredCreditGrant{Label: g.Label, Amount: g.Amount, ExpiresAt: at.UTC()})
		}
	}
	return out, nil
}
