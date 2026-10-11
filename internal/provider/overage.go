package provider

import (
	"math"
	"strconv"
	"time"
)

// OverageCredits are spendable usage credits, independent of quota resets.
// Keep Balance as the provider's decimal string; its unit and expiry are not
// inferred from local token logs or pricing tables.
type OverageCredits struct {
	HasCredits          bool   `json:"has_credits"`
	Unlimited           bool   `json:"unlimited"`
	OverageLimitReached bool   `json:"overage_limit_reached"`
	Balance             string `json:"balance,omitempty"`
	ApproxLocalMessages []int  `json:"approx_local_messages,omitempty"`
	ApproxCloudMessages []int  `json:"approx_cloud_messages,omitempty"`
}

// NumericBalance is only for telemetry. Storage retains the exact decimal.
func (c *OverageCredits) NumericBalance() (float64, bool) {
	if c == nil || c.Balance == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(c.Balance, 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ConfiguredCreditGrant is user-supplied expiry metadata, never API data.
type ConfiguredCreditGrant struct {
	Label     string    `json:"label"`
	Amount    float64   `json:"amount"`
	ExpiresAt time.Time `json:"expires_at"`
}
