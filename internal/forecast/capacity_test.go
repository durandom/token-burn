package forecast

import (
	"github.com/durandom/token-burn/internal/provider"
	"testing"
	"time"
)

func TestResetCapacity(t *testing.T) {
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	ts := func(t time.Time) *provider.Timestamp { return &provider.Timestamp{Time: t} }
	for _, tc := range []struct {
		name    string
		summary *provider.ResetSummary
		expiry  bool
	}{{"owned", &provider.ResetSummary{AvailableCount: 2}, true}, {"zero", &provider.ResetSummary{}, false}, {"unknown", nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := Capacity(tc.summary, []provider.ResetCredit{
				{Status: "available", ResetsLeft: 1, ExpiresAt: ts(now.Add(2 * time.Hour))},
				{Status: "available", ResetsLeft: 1, ExpiresAt: ts(now.Add(time.Hour))},
				{Status: "used", RedeemedAt: ts(now.Add(-time.Hour)), ExpiresAt: ts(now.Add(time.Minute))},
				{Status: "available", ResetsLeft: 1, ExpiresAt: ts(now.Add(-time.Hour))},
			}, []provider.ResetEvent{{Kind: "used", OccurredAt: provider.Timestamp{Time: now.Add(-30 * time.Minute)}}}, now)
			if tc.expiry {
				if c.NextExpiry == nil || !c.NextExpiry.Equal(now.Add(time.Hour)) {
					t.Fatal("earliest live owned expiry")
				}
			} else if c.NextExpiry != nil {
				t.Fatal("no expiry when none owned")
			}
			if c.LastUsed == nil || !c.LastUsed.Equal(now.Add(-30*time.Minute)) {
				t.Fatal("last use")
			}
			if (tc.summary == nil) != (c.OwnedResets == nil) {
				t.Fatal("unknown is not zero")
			}
		})
	}
}
