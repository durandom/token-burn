package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/store"
)

func TestResetsCommandReadsStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.db")
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf("database_path = %q\n[[accounts]]\nprovider = \"codex\"\nid = \"local\"\n", dbPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snap := provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now, Resets: &provider.ResetSummary{AvailableCount: 1}, ResetDetails: &provider.ResetDetails{Credits: []provider.ResetCredit{{ID: "fake-credit", Status: "available", ResetsLeft: 1, ExpiresAt: &provider.Timestamp{Time: now.Add(time.Hour)}}}, Events: []provider.ResetEvent{{ID: "fake-credit:granted", CreditID: "fake-credit", Kind: "granted", Source: "api", Count: 1, OccurredAt: provider.Timestamp{Time: now}}}}}
	if err := db.InsertSnapshot(context.Background(), snap, store.InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{{"text", nil, "resets owned: 1"}, {"json", []string{"--json"}, `"fake-credit"`}, {"filter", []string{"--provider", "claude", "--json"}, "[]"}} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCommand(BuildInfo{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append([]string{"--config", cfgPath, "resets"}, tc.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output %s", out.String())
			}
		})
	}
}

func TestForecastIncludesResetCapacity(t *testing.T) {
	cfg := config.Default()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "usage.db")
	now := time.Now().UTC()
	db, err := store.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	snap := provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now, Resets: &provider.ResetSummary{AvailableCount: 2}, ResetDetails: &provider.ResetDetails{Credits: []provider.ResetCredit{{ID: "fake-credit", Status: "available", ResetsLeft: 1, ExpiresAt: &provider.Timestamp{Time: now.Add(time.Hour)}}}}}
	if err := db.InsertSnapshot(context.Background(), snap, store.InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	results := []forecastOutput{{Provider: "codex", AccountID: "local"}}
	if err := attachStoredCapacity(context.Background(), cfg, results, now); err != nil {
		t.Fatal(err)
	}
	c := results[0].ResetCapacity
	if c == nil || *c.OwnedResets != 2 || !c.NextExpiry.Equal(now.Add(time.Hour)) {
		t.Fatal("forecast capacity missing")
	}
	var out bytes.Buffer
	printForecastText(&out, results)
	if !strings.Contains(out.String(), "resets owned 2") {
		t.Fatal("forecast text missing resets")
	}
}

func TestForecastOverageCapacitySeparateFromResets(t *testing.T) {
	cfg := config.Default()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "usage.db")
	now := time.Now().UTC()
	db, err := store.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSnapshot(context.Background(), provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: now, OverageCredits: &provider.OverageCredits{Balance: "62750.0000000000", OverageLimitReached: true}}, store.InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	results := []forecastOutput{{Provider: "codex", AccountID: "local"}}
	if err := attachStoredCapacity(context.Background(), cfg, results, now); err != nil {
		t.Fatal(err)
	}
	if results[0].ResetCapacity != nil || results[0].OverageCredits == nil || results[0].OverageCredits.Balance != "62750.0000000000" {
		t.Fatal("overage confused with resets")
	}
	var out bytes.Buffer
	printForecastText(&out, results)
	if !strings.Contains(out.String(), "overage credits 62750.0000000000 (overage limit reached)") {
		t.Fatal("capacity text missing balance")
	}
}

func TestResetsCLIConfiguredGrantsBesideLiveBalance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.db")
	cfgPath := filepath.Join(dir, "config.toml")
	text := fmt.Sprintf("database_path=%q\n[credit_grants]\ncodex=[{label=\"Pro promo\",amount=62500,expires_at=\"2099-12-31\"},{label=\"expired promo\",amount=500,expires_at=\"2020-01-01\"}]\n[[accounts]]\nprovider=\"codex\"\nid=\"local\"\n", dbPath)
	if err := os.WriteFile(cfgPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSnapshot(context.Background(), provider.Snapshot{Provider: "codex", AccountID: "local", ObservedAt: time.Now(), OverageCredits: &provider.OverageCredits{Balance: "400.0000000000"}}, store.InsertOptions{}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, tc := range []struct {
		name  string
		args  []string
		wants []string
	}{{"text", nil, []string{"live overage credits: 400.0000000000", "credit expiry (configured)", "amount expiring=400", "Pro promo"}}, {"json", []string{"--json"}, []string{`"source": "configured"`, `"configured_amount": 62500`, `"amount_expiring": 400`, `"overage_credits"`}}} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCommand(BuildInfo{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append([]string{"--config", cfgPath, "resets"}, tc.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %s in %s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "expired promo") {
				t.Fatal("expired grant shown")
			}
		})
	}
}
