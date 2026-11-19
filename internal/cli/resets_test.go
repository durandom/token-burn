package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestCapacityCommandsReadSelectedMode(t *testing.T) {
	for _, mode := range []string{"sqlite", "openobserve"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "usage.db")
			observed := time.Now().UTC().Truncate(time.Second).Add(-time.Second)
			creditObserved := observed.Add(-time.Minute)
			db, err := store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			// A newer usage-only observation must not redate capacity.
			for _, snap := range []provider.Snapshot{
				{Provider: "codex", AccountID: "a", ObservedAt: creditObserved, OverageCredits: &provider.OverageCredits{HasCredits: true, Balance: "42.000"}},
				{Provider: "codex", AccountID: "a", ObservedAt: observed, Resets: &provider.ResetSummary{AvailableCount: 2}, Windows: []provider.Window{{Name: "five_hour", UsedPercent: 10}}, ResetDetails: &provider.ResetDetails{Credits: []provider.ResetCredit{{ID: "c", Status: "available", ResetsLeft: 2, ExpiresAt: &provider.Timestamp{Time: observed.Add(time.Hour)}}}}},
				{Provider: "codex", AccountID: "a", ObservedAt: observed.Add(time.Millisecond), ResetDetailsAttemptedAt: &observed},
			} {
				if err := db.InsertSnapshot(context.Background(), snap, store.InsertOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			// The last Codex omission intentionally clears credits; restore a later
			// independently dated credit observation without reset data.
			db, err = store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			creditObserved = observed.Add(2 * time.Millisecond)
			if err := db.InsertSnapshot(context.Background(), provider.Snapshot{Provider: "codex", AccountID: "a", ObservedAt: creditObserved, OverageCredits: &provider.OverageCredits{HasCredits: true, Balance: "42.000"}}, store.InsertOptions{}); err != nil {
				t.Fatal(err)
			}
			db.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Query struct {
						SQL string `json:"sql"`
					} `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				hits := []map[string]any{}
				for name, value := range map[string]float64{"token_burn_usage_used_percent": 30, "token_burn_resets_available": 3, "token_burn_resets_seconds_to_next_expiry": 3600, "token_burn_resets_last_used_unix_seconds": float64(observed.Add(-time.Hour).Unix()), "token_burn_overage_credits_has_credits": 1, "token_burn_overage_credits_unlimited": 0, "token_burn_overage_limit_reached": 0, "token_burn_overage_credits_balance": 12.5} {
					if !strings.Contains(req.Query.SQL, `FROM "`+name+`"`) {
						continue
					}
					at := observed
					if strings.Contains(name, "overage") {
						at = creditObserved
					}
					hits = append(hits, map[string]any{"_timestamp": at.UnixMicro(), "provider": "codex", "account_id": "a", "window": "five_hour", "value": value})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits})
			}))
			defer server.Close()
			if mode == "openobserve" {
				dbPath = dir
			} // Opening a directory as SQLite fails.
			cfgPath := filepath.Join(dir, "config.toml")
			cfg := fmt.Sprintf("database_path=%q\n[[accounts]]\nprovider=\"codex\"\nid=\"a\"\n[otel.read]\nmode=%q\nendpoint=%q\norganization=\"default\"\n", dbPath, mode, server.URL)
			if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"status", "forecast", "resets"} {
				t.Run(command, func(t *testing.T) {
					cmd := NewRootCommand(BuildInfo{})
					var out bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetArgs([]string{"--config", cfgPath, command, "--json"})
					if err := cmd.Execute(); err != nil {
						t.Fatal(err)
					}
					var rows []struct {
						ResetCapacity *struct {
							OwnedResets int        `json:"owned_resets"`
							NextExpiry  time.Time  `json:"next_expiry"`
							LastUsed    *time.Time `json:"last_used"`
							ObservedAt  time.Time  `json:"observed_at"`
						} `json:"reset_capacity"`
						Credits *provider.OverageCredits `json:"overage_credits"`
					}
					if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
						t.Fatal(err)
					}
					wantCount, wantBalance := 2, "42.000"
					if mode == "openobserve" {
						wantCount, wantBalance = 3, "12.5"
					}
					if len(rows) != 1 || rows[0].ResetCapacity == nil || rows[0].Credits == nil {
						t.Fatalf("output=%s", out.String())
					}
					c := rows[0].ResetCapacity
					if c.OwnedResets != wantCount || !c.ObservedAt.Equal(observed) || !c.NextExpiry.Equal(observed.Add(time.Hour)) || rows[0].Credits.Balance != wantBalance || rows[0].Credits.ObservedAt == nil || !rows[0].Credits.ObservedAt.Equal(creditObserved) {
						t.Fatalf("capacity output=%s", out.String())
					}
					if mode == "openobserve" && (c.LastUsed == nil || !c.LastUsed.Equal(observed.Add(-time.Hour))) {
						t.Fatalf("last use=%s", out.String())
					}
				})
			}
		})
	}
}

func TestOpenObserveCommandsWithMissingAccountStreams(t *testing.T) {
	for _, tc := range []struct {
		name      string
		wantError string
	}{
		{name: "missing streams"},
		{name: "other server error", wantError: "Search stream not found: unrelated_stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := time.Now().Add(-time.Second)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Query struct {
						SQL string `json:"sql"`
					} `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, from, _ := strings.Cut(req.Query.SQL, `FROM "`)
				metric, _, _ := strings.Cut(from, `"`)
				if strings.HasPrefix(metric, "token_burn_resets_") || strings.HasPrefix(metric, "token_burn_overage_") {
					message := "Search stream not found: " + metric
					if metric == "token_burn_overage_credits_unlimited" && tc.wantError != "" {
						message = tc.wantError
					}
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": message})
					return
				}
				var hits []map[string]any
				if metric == "token_burn_usage_used_percent" {
					hits = append(hits, map[string]any{"_timestamp": observed.UnixMicro(), "provider": "codex", "account_id": "a", "window": "five_hour", "value": 30})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits})
			}))
			defer server.Close()

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.toml")
			cfg := fmt.Sprintf("database_path=%q\n[[accounts]]\nprovider=\"codex\"\nid=\"a\"\n[otel.read]\nmode=\"openobserve\"\nendpoint=%q\norganization=\"default\"\n", dir, server.URL)
			if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"status", "forecast", "resets"} {
				t.Run(command, func(t *testing.T) {
					cmd := NewRootCommand(BuildInfo{})
					var out bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetErr(&out)
					cmd.SetArgs([]string{"--config", cfgPath, command, "--json"})
					err := cmd.Execute()
					if tc.wantError != "" {
						want := "query OpenObserve metric token_burn_overage_credits_unlimited: " + tc.wantError
						if err == nil || err.Error() != want {
							t.Fatalf("error = %v, want %q", err, want)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					var rows []map[string]json.RawMessage
					if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
						t.Fatal(err)
					}
					if len(rows) != 1 {
						t.Fatalf("expected one row, got %s", out.String())
					}
					row := rows[0]
					if row["reset_capacity"] != nil || row["overage_credits"] != nil {
						t.Fatalf("unexpected capacity data in %s", out.String())
					}
					switch command {
					case "status":
						if string(row["WindowName"]) != `"five_hour"` || string(row["UsedPercent"]) != "30" {
							t.Fatalf("unexpected usage row %s", out.String())
						}
					case "forecast":
						if string(row["window_name"]) != `"five_hour"` {
							t.Fatalf("unexpected forecast row %s", out.String())
						}
					case "resets":
						if string(row["account_id"]) != `"a"` {
							t.Fatalf("unexpected resets row %s", out.String())
						}
					}
				})
			}
		})
	}
}
