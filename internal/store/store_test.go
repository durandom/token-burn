package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

func TestOpenMigratesSchema(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	for _, version := range []int{1, 2} {
		var count int
		err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count)
		if err != nil {
			t.Fatalf("query schema_migrations version %d: %v", version, err)
		}
		if count != 1 {
			t.Fatalf("migration %d count = %d, want 1", version, count)
		}
	}
}

func TestInsertSnapshotAndHistory(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	berlin := time.FixedZone("Europe/Berlin", 2*60*60)
	observedAt := time.Date(2026, 6, 19, 12, 0, 0, 0, berlin)
	resetAt := observedAt.Add(2 * time.Hour)
	remaining := 88.0
	windowSeconds := 18000

	snap := provider.Snapshot{
		Provider:   "codex",
		AccountID:  "codex-default",
		PlanType:   "plus",
		Source:     "wham_usage",
		ObservedAt: observedAt,
		Windows: []provider.Window{
			{
				Name:             "Five Hour",
				UsedPercent:      12,
				RemainingPercent: &remaining,
				ResetAt:          &resetAt,
				WindowSeconds:    &windowSeconds,
			},
			{
				Name:        "seven_day",
				UsedPercent: 3,
			},
		},
		Raw: map[string]any{"access_token": "secret"},
	}

	if err := store.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}
	if err := store.InsertSnapshot(ctx, snap, InsertOptions{}); err != nil {
		t.Fatalf("second InsertSnapshot() error = %v", err)
	}

	history, err := store.History(ctx, HistoryFilter{
		Provider:  "codex",
		AccountID: "codex-default",
	})
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}

	first := history[0]
	if first.ObservedAt.Location() != time.UTC {
		t.Fatalf("ObservedAt location = %v, want UTC", first.ObservedAt.Location())
	}
	if !first.ObservedAt.Equal(observedAt.UTC()) {
		t.Fatalf("ObservedAt = %s, want %s", first.ObservedAt, observedAt.UTC())
	}
	if first.WindowName != "five_hour" {
		t.Fatalf("WindowName = %q, want five_hour", first.WindowName)
	}
	if first.RawJSON != nil {
		t.Fatalf("RawJSON = %q, want nil by default", *first.RawJSON)
	}
	if first.ResetAt == nil || !first.ResetAt.Equal(resetAt.UTC()) {
		t.Fatalf("ResetAt = %v, want %v", first.ResetAt, resetAt.UTC())
	}
	if first.WindowSeconds == nil || *first.WindowSeconds != windowSeconds {
		t.Fatalf("WindowSeconds = %v, want %d", first.WindowSeconds, windowSeconds)
	}
}

func TestLatestSamplesReturnsLatestPerWindow(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	insertSnapshot(t, ctx, store, provider.Snapshot{
		Provider:   "claude",
		AccountID:  "claude-default",
		Source:     "anthropic_oauth_usage",
		ObservedAt: t0,
		Windows: []provider.Window{
			{Name: "five_hour", UsedPercent: 20},
			{Name: "seven_day", UsedPercent: 4},
		},
	})
	insertSnapshot(t, ctx, store, provider.Snapshot{
		Provider:   "claude",
		AccountID:  "claude-default",
		Source:     "anthropic_oauth_usage",
		ObservedAt: t1,
		Windows: []provider.Window{
			{Name: "five_hour", UsedPercent: 25},
		},
	})

	latest, err := store.LatestSamples(ctx, "claude", "claude-default")
	if err != nil {
		t.Fatalf("LatestSamples() error = %v", err)
	}
	if len(latest) != 1 {
		t.Fatalf("latest length = %d, want 1", len(latest))
	}

	byWindow := map[string]Sample{}
	for _, sample := range latest {
		byWindow[sample.WindowName] = sample
	}
	if got := byWindow["five_hour"].UsedPercent; got != 25 {
		t.Fatalf("latest five_hour used = %v, want 25", got)
	}
	if !byWindow["five_hour"].ObservedAt.Equal(t1) {
		t.Fatalf("latest five_hour observed_at = %s, want %s", byWindow["five_hour"].ObservedAt, t1)
	}
	if _, ok := byWindow["seven_day"]; ok {
		t.Fatal("latest samples should not include windows omitted from newest snapshot")
	}
}

func TestHistoryFilters(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	insertSnapshot(t, ctx, store, provider.Snapshot{
		Provider:   "codex",
		AccountID:  "codex-default",
		Source:     "wham_usage",
		ObservedAt: t0,
		Windows:    []provider.Window{{Name: "five_hour", UsedPercent: 10}},
	})
	insertSnapshot(t, ctx, store, provider.Snapshot{
		Provider:   "codex",
		AccountID:  "codex-default",
		Source:     "wham_usage",
		ObservedAt: t1,
		Windows:    []provider.Window{{Name: "five_hour", UsedPercent: 15}},
	})

	history, err := store.History(ctx, HistoryFilter{
		Provider:   "codex",
		AccountID:  "codex-default",
		WindowName: "five/hour",
		Since:      &t1,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].UsedPercent != 15 {
		t.Fatalf("UsedPercent = %v, want 15", history[0].UsedPercent)
	}
}

func TestInsertSnapshotStoresRedactedRawJSONWhenEnabled(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	insertSnapshotWithOptions(t, ctx, store, provider.Snapshot{
		Provider:   "codex",
		AccountID:  "codex-default",
		Source:     "wham_usage",
		ObservedAt: time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC),
		Windows:    []provider.Window{{Name: "five_hour", UsedPercent: 10}},
		Raw: map[string]any{
			"access_token": "secret-access",
			"ok":           "visible",
			"nested": map[string]any{
				"refresh_token": "secret-refresh",
				"count":         float64(1),
			},
		},
	}, InsertOptions{StoreRawJSON: true})

	history, err := store.History(ctx, HistoryFilter{Provider: "codex"})
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].RawJSON == nil {
		t.Fatal("RawJSON = nil, want redacted JSON")
	}
	raw := *history[0].RawJSON
	for _, secret := range []string{"secret-access", "secret-refresh"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("RawJSON contains secret %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("RawJSON = %s, want redaction marker", raw)
	}
	if !strings.Contains(raw, "visible") {
		t.Fatalf("RawJSON = %s, want non-secret value", raw)
	}
}

func TestRecordPollRunAndQuery(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	startedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(150 * time.Millisecond)
	httpStatus := 401
	latencyMS := 150

	if err := store.RecordPollRun(ctx, PollRun{
		StartedAt:    startedAt,
		FinishedAt:   &finishedAt,
		Provider:     "codex",
		AccountID:    "codex-default",
		Status:       "error",
		HTTPStatus:   &httpStatus,
		ErrorCode:    "auth_expired",
		ErrorMessage: "codex: auth expired",
		LatencyMS:    &latencyMS,
	}); err != nil {
		t.Fatalf("RecordPollRun() error = %v", err)
	}

	runs, err := store.PollRuns(ctx, PollRunFilter{Provider: "codex", AccountID: "codex-default"})
	if err != nil {
		t.Fatalf("PollRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("poll run count = %d, want 1", len(runs))
	}
	got := runs[0]
	if got.Status != "error" || got.ErrorCode != "auth_expired" || got.ErrorMessage != "codex: auth expired" {
		t.Fatalf("poll run = %#v, want error metadata", got)
	}
	if got.HTTPStatus == nil || *got.HTTPStatus != 401 {
		t.Fatalf("HTTPStatus = %v, want 401", got.HTTPStatus)
	}
	if got.LatencyMS == nil || *got.LatencyMS != 150 {
		t.Fatalf("LatencyMS = %v, want 150", got.LatencyMS)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finishedAt) {
		t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, finishedAt)
	}
}

func TestPollRunsFiltersSinceAndLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	for i, ts := range []time.Time{t0, t0.Add(time.Hour)} {
		if err := store.RecordPollRun(ctx, PollRun{
			StartedAt: ts,
			Provider:  "claude",
			AccountID: "claude-default",
			Status:    "success",
		}); err != nil {
			t.Fatalf("RecordPollRun(%d) error = %v", i, err)
		}
	}

	since := t0.Add(30 * time.Minute)
	runs, err := store.PollRuns(ctx, PollRunFilter{Provider: "claude", Since: &since, Limit: 1})
	if err != nil {
		t.Fatalf("PollRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("poll run count = %d, want 1", len(runs))
	}
	if !runs[0].StartedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("StartedAt = %v, want second run", runs[0].StartedAt)
	}
}

func TestDaemonStateRoundTripAndUpsert(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	if _, ok, err := store.LatestDaemonState(ctx); err != nil {
		t.Fatalf("LatestDaemonState() error = %v", err)
	} else if ok {
		t.Fatal("LatestDaemonState() ok = true, want false before any upsert")
	}

	updatedAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	nextPollAt := updatedAt.Add(7 * time.Minute)
	if err := store.UpsertDaemonState(ctx, DaemonState{
		UpdatedAt:    updatedAt,
		PollInterval: 7 * time.Minute,
		NextPollAt:   &nextPollAt,
	}); err != nil {
		t.Fatalf("UpsertDaemonState() error = %v", err)
	}

	got, ok, err := store.LatestDaemonState(ctx)
	if err != nil || !ok {
		t.Fatalf("LatestDaemonState() = ok %t err %v", ok, err)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, updatedAt)
	}
	if got.PollInterval != 7*time.Minute {
		t.Fatalf("PollInterval = %s, want 7m", got.PollInterval)
	}
	if got.NextPollAt == nil || !got.NextPollAt.Equal(nextPollAt) {
		t.Fatalf("NextPollAt = %v, want %v", got.NextPollAt, nextPollAt)
	}

	// A second upsert must overwrite, not append (single-row table).
	if err := store.UpsertDaemonState(ctx, DaemonState{
		UpdatedAt:    updatedAt.Add(time.Minute),
		PollInterval: 14 * time.Minute,
	}); err != nil {
		t.Fatalf("UpsertDaemonState() second error = %v", err)
	}
	got, _, err = store.LatestDaemonState(ctx)
	if err != nil {
		t.Fatalf("LatestDaemonState() error = %v", err)
	}
	if got.PollInterval != 14*time.Minute {
		t.Fatalf("PollInterval after upsert = %s, want 14m", got.PollInterval)
	}
	if wantUpdatedAt := updatedAt.Add(time.Minute); !got.UpdatedAt.Equal(wantUpdatedAt) {
		t.Fatalf("UpdatedAt after upsert = %v, want %v", got.UpdatedAt, wantUpdatedAt)
	}
	if got.NextPollAt != nil {
		t.Fatalf("NextPollAt = %v, want nil after upsert without it", got.NextPollAt)
	}
}

func TestHistoryBatchFiltersPagesAndOmitsRawJSON(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	defer store.Close()

	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		insertSnapshotWithOptions(t, ctx, store, provider.Snapshot{
			Provider: "codex", AccountID: "codex-default", Source: "live",
			ObservedAt: t0.Add(time.Duration(i) * time.Minute),
			Windows:    []provider.Window{{Name: "five_hour", UsedPercent: float64(i)}},
			Raw:        map[string]any{"access_token": "must-not-be-read"},
		}, InsertOptions{StoreRawJSON: true})
	}
	from := t0.Add(time.Minute)
	batch, err := store.HistoryBatch(ctx, HistoryFilter{Provider: "codex", Since: &from}, 0, 1)
	if err != nil {
		t.Fatalf("HistoryBatch() error = %v", err)
	}
	if len(batch) != 1 || !batch[0].ObservedAt.Equal(from) {
		t.Fatalf("first batch = %#v", batch)
	}
	if batch[0].RawJSON != nil {
		t.Fatalf("RawJSON = %q, want nil", *batch[0].RawJSON)
	}
	second, err := store.HistoryBatch(ctx, HistoryFilter{Provider: "codex", Since: &from}, batch[0].ID, 10)
	if err != nil {
		t.Fatalf("second HistoryBatch() error = %v", err)
	}
	if len(second) != 1 || !second[0].ObservedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("second batch = %#v", second)
	}
}

func openTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()

	store, err := Open(ctx, t.TempDir()+"/token-burn.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

func insertSnapshot(t *testing.T, ctx context.Context, store *Store, snap provider.Snapshot) {
	t.Helper()
	insertSnapshotWithOptions(t, ctx, store, snap, InsertOptions{})
}

func insertSnapshotWithOptions(t *testing.T, ctx context.Context, store *Store, snap provider.Snapshot, opts InsertOptions) {
	t.Helper()
	if err := store.InsertSnapshot(ctx, snap, opts); err != nil {
		t.Fatalf("InsertSnapshot() error = %v", err)
	}
}
