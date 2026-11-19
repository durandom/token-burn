package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

func TestCreditBalanceSurvivesReopenAndOutOfOrderSamples(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "credits.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	wholeSecond := time.Now().UTC().Truncate(time.Second)
	now := wholeSecond.Add(time.Nanosecond)
	expiry := now.Add(time.Hour)
	snap := provider.Snapshot{Provider: "claude-platform", AccountID: "org", ObservedAt: now, CreditBalance: &provider.CreditBalance{OrganizationID: "remote-org", Balance: provider.Money{AmountMinor: 21776, Currency: "USD", Exponent: 2}, Grants: []provider.CreditGrant{{Remaining: provider.Money{AmountMinor: 20000, Currency: "USD", Exponent: 2}, ExpiresAt: &expiry}}}}
	if err := db.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	snap.ObservedAt = wholeSecond
	snap.CreditBalance.Balance.AmountMinor = 999
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
	got, err := db.LatestCreditBalance(ctx, "claude-platform", "org")
	if err != nil {
		t.Fatal(err)
	}
	if got.Credits.Balance.AmountMinor != 21776 || !got.ObservedAt.Equal(now) || !got.Credits.Grants[0].ExpiresAt.Equal(expiry) {
		t.Fatalf("lost snapshot: %#v", got)
	}
	missing, err := db.LatestCreditBalance(ctx, "claude", "org")
	if err != nil || missing != nil {
		t.Fatal("balance leaked between providers")
	}
}

func TestMigrateConsoleWIPDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "console-wip.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	observed := "2026-10-21T12:00:00Z"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := legacy.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	for _, m := range migrations {
		if m.version >= 5 {
			break
		}
		exec(m.sql)
		exec(`INSERT INTO schema_migrations VALUES (?, ?)`, m.version, observed)
	}
	exec(creditBalanceSchema)
	exec(`INSERT INTO schema_migrations VALUES (5, ?)`, observed)
	exec(`INSERT INTO credit_balance_snapshots VALUES ('claude-platform','console',?, '{"balance":{"amount_minor":123,"currency":"USD","exponent":2}}')`, observed)
	exec(`INSERT INTO reset_state(provider,account_id,summary,observed_at) VALUES ('codex','a','{"available_count":2}',?)`, observed)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	balance, err := upgraded.LatestCreditBalance(ctx, "claude-platform", "console")
	if err != nil || balance == nil || balance.Credits.Balance.AmountMinor != 123 {
		t.Fatalf("balance after upgrade = %#v, error = %v", balance, err)
	}
	history, err := upgraded.ResetHistory(ctx, "codex", "a")
	if err != nil || history.ObservedAt == nil || formatTime(*history.ObservedAt) != observed {
		t.Fatalf("reset history after upgrade = %#v, error = %v", history, err)
	}
}
