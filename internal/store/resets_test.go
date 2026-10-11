package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

func resetTime(t time.Time) *provider.Timestamp { return &provider.Timestamp{Time: t} }

func TestResetPersistenceAndInferredUses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.FixedZone("local", 7200))
	snap := provider.Snapshot{Provider: "claude", AccountID: "local", ObservedAt: now, Resets: &provider.ResetSummary{AvailableCount: 3}, ResetDetails: &provider.ResetDetails{CreditsComplete: true, Credits: []provider.ResetCredit{{ID: "fake-grant", Status: "available", ResetsTotal: 4, ResetsLeft: 3, GrantedAt: resetTime(now.Add(-time.Hour)), ExpiresAt: resetTime(now.Add(time.Hour))}}, Metadata: map[string]any{"access_token": "fake-secret", "eligible": true}}}
	if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range []struct {
		name          string
		minutes, left int
	}{{"drop by two", 1, 1}, {"repeated poll", 1, 1}, {"unchanged", 2, 1}, {"increase", 3, 2}, {"drop again", 4, 1}, {"old sample", 0, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			snap.ObservedAt = now.Add(time.Duration(tc.minutes) * time.Minute)
			snap.ResetDetails.Credits[0].ResetsLeft = tc.left
			snap.Resets.AvailableCount = tc.left
			if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	h, err := db.ResetHistory(ctx, "claude", "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Credits) != 1 || h.Credits[0].ResetsLeft != 1 || h.Summary.AvailableCount != 1 || len(h.Events) != 3 {
		t.Fatalf("history %+v", h)
	}
	if h.Events[1].Source != "inferred" || h.Events[1].Count != 2 || !h.Events[1].OccurredAt.Equal(now.Add(time.Minute)) {
		t.Fatal("inferred use mismatch")
	}
	if h.Credits[0].RedeemedAt != nil {
		t.Fatal("Claude has no API redemption timestamp")
	}
	if h.Metadata["access_token"] == "fake-secret" {
		t.Fatal("secret stored")
	}
	var stored string
	if err := db.db.QueryRowContext(ctx, `SELECT occurred_at FROM reset_events WHERE kind='used' ORDER BY occurred_at LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "2026-10-11T10:01:00Z" {
		t.Fatalf("UTC storage %s", stored)
	}
	// A missing grant is not a use, and history remains inspectable.
	snap.ObservedAt = now.Add(5 * time.Minute)
	snap.ResetDetails.Credits = nil
	if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	h, err = db.ResetHistory(ctx, "claude", "local")
	if err != nil {
		t.Fatal(err)
	}
	if h.Credits[0].Status != "unavailable" || len(h.Events) != 3 {
		t.Fatal("disappearance inferred a use")
	}
	other, err := db.ResetHistory(ctx, "claude", "other")
	if err != nil || len(other.Credits) != 0 {
		t.Fatal("account isolation")
	}
}

func TestResetUpsertsPlanHistoryAndCadence(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t, ctx)
	defer db.Close()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	snap := provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now, ResetDetailsAttemptedAt: &now, Resets: &provider.ResetSummary{AvailableCount: 1}, ResetDetails: &provider.ResetDetails{Credits: []provider.ResetCredit{{ID: "fake-credit", Status: "available", ResetsLeft: 1, ExpiresAt: resetTime(now.Add(time.Hour))}}, Events: []provider.ResetEvent{{ID: "fake-credit:granted", CreditID: "fake-credit", Kind: "granted", OccurredAt: provider.Timestamp{Time: now}, Source: "api", Count: 1}}}, PlanLimitHistory: &provider.PlanLimitHistory{Approximate: true, Periods: []provider.PlanLimitPeriod{{ID: "fake-period", StartsAt: provider.Timestamp{Time: now.Add(-7 * 24 * time.Hour)}, EndsAt: provider.Timestamp{Time: now}, UsedBasisPoints: 2000}}}}
	for _, used := range []int{2000, 3500} {
		snap.PlanLimitHistory.Periods[0].UsedBasisPoints = used
		if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := db.ResetHistory(ctx, "codex", "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Events) != 1 || len(h.PlanLimitHistory.Periods) != 1 || h.PlanLimitHistory.Periods[0].UsedBasisPoints != 3500 || !h.PlanLimitHistory.Approximate {
		t.Fatal("upsert or coverage metadata")
	}
	after, err := db.ResetDetailsAfter(ctx, "codex", "local")
	if err != nil || after == nil || !after.Equal(now.Add(15*time.Minute)) {
		t.Fatal("persisted cadence")
	}
	snap = provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now.Add(time.Minute), Resets: &provider.ResetSummary{AvailableCount: 0}}
	if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnrichResetSnapshot(ctx, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Resets.AvailableCount != 0 || len(snap.ResetDetails.Credits) != 1 {
		t.Fatal("cached enrichment")
	}
}

func TestInvalidResetSnapshotRollsBackUsage(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t, ctx)
	defer db.Close()
	snap := provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: time.Now(), Windows: []provider.Window{{Name: "five_hour", UsedPercent: 10}}, ResetDetails: &provider.ResetDetails{Events: []provider.ResetEvent{{ID: "fake-event", Kind: "used", Source: "inferred"}}}}
	if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err == nil {
		t.Fatal("invalid event accepted")
	}
	samples, err := db.LatestSamples(ctx, "codex", "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatal("partial snapshot committed")
	}
}
