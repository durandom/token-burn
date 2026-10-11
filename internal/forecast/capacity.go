package forecast

import (
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

type ResetCapacity struct {
	OwnedResets *int       `json:"owned_resets"`
	NextExpiry  *time.Time `json:"next_expiry"`
	LastUsed    *time.Time `json:"last_used"`
}

// Reset capacity is separate from burn-rate prediction: owning a reset does
// not imply that the user will redeem it, or that every window can be reset.
func Capacity(summary *provider.ResetSummary, credits []provider.ResetCredit, events []provider.ResetEvent, now time.Time) ResetCapacity {
	out := ResetCapacity{}
	if summary != nil {
		v := summary.AvailableCount
		out.OwnedResets = &v
	}
	for _, c := range credits {
		if c.RedeemedAt != nil && (out.LastUsed == nil || c.RedeemedAt.After(*out.LastUsed)) {
			out.LastUsed = c.RedeemedAt.UTCValue()
		}
		if c.Status != "available" || c.ResetsLeft <= 0 {
			continue
		}
		expiry := c.ExpiresAt.UTCValue()
		if expiry != nil && expiry.After(now) && (out.NextExpiry == nil || expiry.Before(*out.NextExpiry)) {
			out.NextExpiry = expiry
		}
	}
	for _, e := range events {
		if e.Kind == "used" && (out.LastUsed == nil || e.OccurredAt.After(*out.LastUsed)) {
			v := e.OccurredAt.Time
			out.LastUsed = &v
		}
	}
	if out.OwnedResets != nil && *out.OwnedResets == 0 {
		out.NextExpiry = nil
	}
	return out
}
