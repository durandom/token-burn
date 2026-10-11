package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

func TestOverageSnapshotsPreserveDecimalAndLatestState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("local", 7200))
	for _, tc := range []struct {
		name    string
		nanos   int
		credits *provider.OverageCredits
	}{
		{"initial", 0, &provider.OverageCredits{HasCredits: true, Balance: "62750.0000000000", ApproxLocalMessages: []int{10, 20}, ApproxCloudMessages: []int{3, 7}}},
		{"new balance", 100, &provider.OverageCredits{HasCredits: true, Balance: "62500.1250000000", OverageLimitReached: true}},
		{"same timestamp upsert", 100, &provider.OverageCredits{Balance: "0.0000000000", OverageLimitReached: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now.Add(time.Duration(tc.nanos)), OverageCredits: tc.credits}
			if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
				t.Fatal(err)
			}
			latest, err := db.LatestOverageCredits(ctx, "codex", "local")
			if err != nil {
				t.Fatal(err)
			}
			if latest == nil || latest.Balance != tc.credits.Balance {
				t.Fatalf("latest %+v", latest)
			}
		})
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := db.OverageCreditHistory(ctx, "codex", "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Credits.Balance != "62750.0000000000" || h[1].Credits.Balance != "0.0000000000" || !h[0].ObservedAt.Equal(now) || h[0].ObservedAt.Location() != time.UTC {
		t.Fatalf("history %+v", h)
	}
	if h[0].Credits.ApproxCloudMessages[1] != 7 || h[0].Credits.ApproxLocalMessages[0] != 10 {
		t.Fatal("ranges not stored")
	}
	other, err := db.LatestOverageCredits(ctx, "codex", "other")
	if err != nil || other != nil {
		t.Fatal("account isolation")
	}
	if err := db.InsertSnapshot(ctx, provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now.Add(time.Minute)}, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	latest, err := db.LatestOverageCredits(ctx, "codex", "local")
	if err != nil || latest != nil {
		t.Fatal("omitted field displayed an old balance")
	}
	h, err = db.OverageCreditHistory(ctx, "codex", "local")
	if err != nil || len(h) != 3 || h[2].Credits != nil {
		t.Fatal("missing-field observation not retained")
	}
}
