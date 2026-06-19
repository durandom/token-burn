package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/otel"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/store"
	"go.opentelemetry.io/otel/attribute"
)

type fakeProvider struct {
	id   string
	snap usageprovider.Snapshot
	err  error
}

func (f fakeProvider) ID() string { return f.id }

func (f fakeProvider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	if f.err != nil {
		return usageprovider.Snapshot{}, f.err
	}
	return f.snap, nil
}

type fakeRecorder struct {
	gauges   []string
	counters []string
}

func (f *fakeRecorder) RecordGauge(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue) {
	f.gauges = append(f.gauges, name)
}

func (f *fakeRecorder) AddCounter(ctx context.Context, name string, value int64, attrs ...attribute.KeyValue) {
	f.counters = append(f.counters, name)
}

func TestPollOnceStoresSnapshotAndEmitsMetrics(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/token-burn.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	recorder := &fakeRecorder{}
	result, err := PollOnce(ctx, db, Options{
		Config: config.Config{
			Accounts: []config.Account{{Provider: "codex", ID: "codex-default"}},
		},
		Providers: map[string]usageprovider.Provider{
			"codex": fakeProvider{
				id: "codex",
				snap: usageprovider.Snapshot{
					Provider:   "codex",
					AccountID:  "codex-default",
					Source:     "wham_usage",
					ObservedAt: now,
					Windows:    []usageprovider.Window{{Name: "five_hour", UsedPercent: 12}},
				},
			},
		},
		Recorder: recorder,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(result.Snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(result.Snapshots))
	}
	samples, err := db.History(ctx, store.HistoryFilter{Provider: "codex", AccountID: "codex-default"})
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(samples) != 1 || samples[0].UsedPercent != 12 {
		t.Fatalf("samples = %#v, want one stored sample", samples)
	}
	runs, err := db.PollRuns(ctx, store.PollRunFilter{Provider: "codex", AccountID: "codex-default"})
	if err != nil {
		t.Fatalf("PollRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("poll runs = %#v, want one success", runs)
	}
	if !contains(recorder.gauges, otel.MetricUsageUsedPercent) {
		t.Fatalf("gauges = %v, want usage used metric", recorder.gauges)
	}
	if !contains(recorder.counters, otel.MetricPollRunsTotal) {
		t.Fatalf("counters = %v, want poll run counter", recorder.counters)
	}
}

func TestPollOnceRecordsProviderErrors(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/token-burn.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	recorder := &fakeRecorder{}
	result, err := PollOnce(ctx, db, Options{
		Config: config.Config{
			Accounts: []config.Account{{Provider: "codex", ID: "codex-default"}},
		},
		Providers: map[string]usageprovider.Provider{
			"codex": fakeProvider{
				id: "codex",
				err: &usageprovider.Error{
					Code:     usageprovider.ErrAuthExpired,
					Provider: "codex",
				},
			},
		},
		Recorder: recorder,
	})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("error count = %d, want 1", len(result.Errors))
	}
	if result.Errors[0].Code != string(usageprovider.ErrAuthExpired) {
		t.Fatalf("error code = %q, want auth_expired", result.Errors[0].Code)
	}
	runs, err := db.PollRuns(ctx, store.PollRunFilter{Provider: "codex", AccountID: "codex-default"})
	if err != nil {
		t.Fatalf("PollRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "error" || runs[0].ErrorCode != "auth_expired" {
		t.Fatalf("poll runs = %#v, want one auth error", runs)
	}
	if !contains(recorder.counters, otel.MetricPollErrorsTotal) {
		t.Fatalf("counters = %v, want poll error counter", recorder.counters)
	}
}

func TestPollOnceRedactsSecretErrorMessages(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/token-burn.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	_, err = PollOnce(ctx, db, Options{
		Config: config.Config{
			Accounts: []config.Account{{Provider: "codex", ID: "codex-default"}},
		},
		Providers: map[string]usageprovider.Provider{
			"codex": fakeProvider{
				id:  "codex",
				err: &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: "codex", Err: errString("Bearer secret-token failed")},
			},
		},
	})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}

	runs, err := db.PollRuns(ctx, store.PollRunFilter{Provider: "codex", AccountID: "codex-default"})
	if err != nil {
		t.Fatalf("PollRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("poll run count = %d, want 1", len(runs))
	}
	if runs[0].ErrorMessage != "[REDACTED]" {
		t.Fatalf("ErrorMessage = %q, want redacted", runs[0].ErrorMessage)
	}
}

func TestPollOnceUnsupportedProvider(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/token-burn.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	result, err := PollOnce(ctx, db, Options{
		Config: config.Config{
			Accounts: []config.Account{{Provider: "unknown", ID: "default"}},
		},
	})
	if err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("error count = %d, want 1", len(result.Errors))
	}
	if result.Errors[0].Err == nil {
		t.Fatal("error should be set")
	}
}

func TestBackoffNextDelay(t *testing.T) {
	backoff := Backoff{Base: time.Minute, Max: 5 * time.Minute}

	if got := backoff.NextDelay(false); got != time.Minute {
		t.Fatalf("success delay = %s, want 1m", got)
	}
	if got := backoff.NextDelay(true); got != time.Minute {
		t.Fatalf("first failure delay = %s, want 1m", got)
	}
	if got := backoff.NextDelay(true); got != 2*time.Minute {
		t.Fatalf("second failure delay = %s, want 2m", got)
	}
	if got := backoff.NextDelay(true); got != 4*time.Minute {
		t.Fatalf("third failure delay = %s, want 4m", got)
	}
	if got := backoff.NextDelay(true); got != 5*time.Minute {
		t.Fatalf("capped failure delay = %s, want 5m", got)
	}
	if got := backoff.NextDelay(false); got != time.Minute {
		t.Fatalf("reset success delay = %s, want 1m", got)
	}
}

func TestBackoffDefaults(t *testing.T) {
	var backoff Backoff
	if got := backoff.NextDelay(true); got != config.DefaultPollInterval {
		t.Fatalf("default delay = %s, want %s", got, config.DefaultPollInterval)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type errString string

func (e errString) Error() string {
	return string(e)
}
