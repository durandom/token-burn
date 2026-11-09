package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/otelread"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/service"
	"github.com/durandom/token-burn/internal/store"
)

func TestLatestStatusSamplesFallsBackToOpenObserve(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "token_burn_usage_used_percent") {
			_, _ = w.Write([]byte(`{"hits":[{"_timestamp":1787572800000000,"provider":"claude","account_id":"claude-default","window":"seven_day","plan_type":"max","source":"anthropic","value":61}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"hits":[]}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "empty.db")
	cfg.OTel.Read.Mode = "auto"
	cfg.OTel.Read.Endpoint = server.URL
	cfg.OTel.Read.Organization = "default"
	cfg.OTel.Read.Lookback = time.Hour
	remote, err := latestStatusSnapshot(context.Background(), cfg, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("latestStatusSamples() error = %v", err)
	}
	samples := remote.Samples
	if len(samples) != 1 || samples[0].Provider != "claude" || samples[0].UsedPercent != 61 {
		t.Fatalf("samples = %#v", samples)
	}
}

func TestSnapshotsFromSamplesGroupsAccounts(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	snapshots := snapshotsFromSamples(otelread.Snapshot{Samples: []store.Sample{
		{ObservedAt: now, Provider: "claude", AccountID: "claude-default", WindowName: "five_hour", UsedPercent: 10},
		{ObservedAt: now, Provider: "claude", AccountID: "claude-default", WindowName: "seven_day", UsedPercent: 20},
	}})
	if len(snapshots) != 1 || len(snapshots[0].Windows) != 2 {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestProviderForXAI(t *testing.T) {
	provider, ok := providerFor("grok")
	if !ok || provider.ID() != "xai" {
		t.Fatalf("providerFor(grok) = %#v, %t", provider, ok)
	}
}

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand(BuildInfo{
		Version: "v0.1.0",
		Commit:  "abc123",
		Date:    "2026-06-19T12:00:00Z",
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	want := "token-burn v0.1.0\ncommit: abc123\nbuilt: 2026-06-19T12:00:00Z\n"
	if got := out.String(); got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestUpgradeCommandIsRegistered(t *testing.T) {
	cmd := NewRootCommand(BuildInfo{Version: "v0.1.0"})
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "upgrade" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("upgrade command is not registered")
	}
}

func TestInstallSpecUsesConfiguredDatabasePath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	databasePath := filepath.Join(dir, "custom", "token-burn.db")
	data := []byte(`
poll_interval = "5m"
database_path = "` + filepath.ToSlash(databasePath) + `"

[[accounts]]
provider = "codex"
id = "codex-default"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	spec, err := installSpec("/tmp/token-burn", configPath)
	if err != nil {
		t.Fatalf("installSpec() error = %v", err)
	}
	if spec.DatabasePath != databasePath {
		t.Fatalf("DatabasePath = %q, want %q", spec.DatabasePath, databasePath)
	}
}

func TestParseLookbackDuration(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "24h", want: 24 * time.Hour},
		{raw: "7d", want: 7 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseLookbackDuration(tt.raw)
			if err != nil {
				t.Fatalf("parseLookbackDuration() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("duration = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPrintVerboseSnapshotReportsResetWait(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Minute)
	windowSeconds := 18000
	remaining := 62.5

	var out bytes.Buffer
	printVerboseSnapshot(&out, usageprovider.Snapshot{
		Provider:  "claude",
		AccountID: "claude-default",
		Windows: []usageprovider.Window{{
			Name:             "five_hour",
			UsedPercent:      37.5,
			RemainingPercent: &remaining,
			WindowSeconds:    &windowSeconds,
			ResetAt:          &reset,
			LimitReached:     false,
		}},
	}, now)

	got := out.String()
	for _, want := range []string{"window five_hour", "used=37.5%", "remaining=62.5%", "window=5h0m0s", "in=1h30m0s", "limit_reached=false"} {
		if !strings.Contains(got, want) {
			t.Fatalf("verbose snapshot output %q missing %q", got, want)
		}
	}
}

func TestPrintVerboseErrorFlagsRateLimit(t *testing.T) {
	var out bytes.Buffer
	printVerboseError(&out, commandError{
		Provider:   "claude",
		AccountID:  "claude-default",
		Code:       string(usageprovider.ErrRateLimited),
		HTTPStatus: 429,
	})

	got := out.String()
	for _, want := range []string{"code=rate_limited", "http=429", "Retry-After"} {
		if !strings.Contains(got, want) {
			t.Fatalf("verbose error output %q missing %q", got, want)
		}
	}
}

func TestInstallSpecOmitsServiceEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	data := []byte(`
[service.env]
TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID = "107100-test.apps.googleusercontent.com"

[[accounts]]
provider = "codex"
id = "codex-default"
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	spec, err := installSpec("/tmp/token-burn", configPath)
	if err != nil {
		t.Fatalf("installSpec() error = %v", err)
	}
	for name, render := range map[string]func(service.Spec) ([]byte, error){
		"launchd": service.LaunchAgentPlist, "systemd": service.SystemdUnit,
	} {
		t.Run(name, func(t *testing.T) {
			unit, err := render(spec)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID", "107100-test.apps.googleusercontent.com"} {
				if strings.Contains(string(unit), secret) {
					t.Fatal("service unit contains config environment")
				}
			}
		})
	}
}

func TestOpenObserveCommandsIncludeFreshCredits(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", stale), func(t *testing.T) {
			observed := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
			creditTime := observed
			if stale {
				creditTime = observed.Add(-3 * time.Minute)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var hits []map[string]any
				if strings.Contains(string(body), `FROM \"token_burn_usage_used_percent\"`) {
					for _, window := range []string{"five_hour", "seven_day"} {
						for _, account := range []string{"a", "b"} {
							hits = append(hits, map[string]any{"_timestamp": observed.UnixMicro(), "provider": "codex", "account_id": account, "window": window, "value": 40})
						}
					}
				}
				for name, value := range map[string]float64{
					"token_burn_overage_credits_balance":     12.5,
					"token_burn_overage_credits_has_credits": 1,
					"token_burn_overage_credits_unlimited":   0,
					"token_burn_overage_limit_reached":       1,
				} {
					if strings.Contains(string(body), `FROM \"`+name+`\"`) {
						hits = append(hits, map[string]any{"_timestamp": creditTime.UnixMicro(), "provider": "codex", "account_id": "a", "value": value})
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "config.toml")
			data := fmt.Sprintf("database_path = %q\n[otel.read]\nmode = \"openobserve\"\nendpoint = %q\norganization = \"default\"\nlookback = \"1h\"\n", filepath.Join(t.TempDir(), "missing", "db"), server.URL)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			for _, command := range []string{"status", "forecast", "once"} {
				t.Run(command, func(t *testing.T) {
					cmd := NewRootCommand(BuildInfo{})
					var out bytes.Buffer
					cmd.SetOut(&out)
					cmd.SetArgs([]string{"--config", path, command, "--json"})
					if err := cmd.Execute(); err != nil {
						t.Fatal(err)
					}
					var rows []map[string]json.RawMessage
					key, accountKey := "overage_credits", "AccountID"
					if command == "once" {
						var result struct {
							Snapshots []map[string]json.RawMessage `json:"snapshots"`
						}
						if err := json.Unmarshal(out.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						rows = result.Snapshots
						key = "OverageCredits"
					} else {
						if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
							t.Fatal(err)
						}
						if command == "forecast" {
							accountKey = "account_id"
						}
					}
					wantRows := 4
					if command == "once" {
						wantRows = 2
					}
					if len(rows) != wantRows {
						t.Fatalf("rows = %s", out.String())
					}
					for _, row := range rows {
						var account string
						_ = json.Unmarshal(row[accountKey], &account)
						raw := row[key]
						present := len(raw) != 0 && string(raw) != "null"
						if present != (!stale && account == "a") {
							t.Fatalf("credits presence: %s", out.String())
						}
						if !present {
							continue
						}
						var credits usageprovider.OverageCredits
						if err := json.Unmarshal(raw, &credits); err != nil {
							t.Fatal(err)
						}
						var fields map[string]any
						_ = json.Unmarshal(raw, &fields)
						if len(fields) != 5 || credits.Balance != "12.5" || !credits.HasCredits || credits.Unlimited || !credits.OverageLimitReached || credits.ObservedAt == nil || !credits.ObservedAt.Equal(creditTime) {
							t.Fatalf("credits = %s", raw)
						}
					}
				})
			}
		})
	}
}
