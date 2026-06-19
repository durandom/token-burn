package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/provider"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type InsertOptions struct {
	StoreRawJSON bool
}

type HistoryFilter struct {
	Provider   string
	AccountID  string
	WindowName string
	Since      *time.Time
	Limit      int
}

type PollRun struct {
	ID           int64
	StartedAt    time.Time
	FinishedAt   *time.Time
	Provider     string
	AccountID    string
	Status       string
	HTTPStatus   *int
	ErrorCode    string
	ErrorMessage string
	LatencyMS    *int
}

type PollRunFilter struct {
	Provider  string
	AccountID string
	Since     *time.Time
	Limit     int
}

type Sample struct {
	ID               int64
	ObservedAt       time.Time
	Provider         string
	AccountID        string
	PlanType         string
	WindowName       string
	UsedPercent      float64
	RemainingPercent *float64
	ResetAt          *time.Time
	WindowSeconds    *int
	LimitReached     bool
	Source           string
	RawJSON          *string
	CreatedAt        time.Time
}

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE IF NOT EXISTS poll_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  status TEXT NOT NULL,
  http_status INTEGER,
  error_code TEXT,
  error_message TEXT,
  latency_ms INTEGER
);

CREATE TABLE IF NOT EXISTS live_usage_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  observed_at TEXT NOT NULL,
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  plan_type TEXT,
  window_name TEXT NOT NULL,
  used_percent REAL NOT NULL,
  remaining_percent REAL,
  reset_at TEXT,
  window_seconds INTEGER,
  limit_reached INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL,
  raw_json TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE(provider, account_id, window_name, observed_at)
);

CREATE INDEX IF NOT EXISTS idx_live_usage_latest
  ON live_usage_samples(provider, account_id, window_name, observed_at DESC);

CREATE INDEX IF NOT EXISTS idx_live_usage_reset
  ON live_usage_samples(provider, account_id, window_name, reset_at, observed_at);

CREATE TABLE IF NOT EXISTS forecasts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  computed_at TEXT NOT NULL,
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  window_name TEXT NOT NULL,
  reset_at TEXT,
  sample_count INTEGER NOT NULL,
  burn_rate_percent_per_hour REAL,
  projected_reset_percent REAL,
  estimated_90_at TEXT,
  estimated_100_at TEXT,
  confidence REAL,
  method TEXT NOT NULL,
  UNIQUE(provider, account_id, window_name, reset_at, computed_at)
);

CREATE INDEX IF NOT EXISTS idx_forecasts_latest
  ON forecasts(provider, account_id, window_name, computed_at DESC);
`,
	},
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, fmt.Errorf("create store directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}

	store := &Store{db: db}
	if err := store.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()

	for _, m := range migrations {
		var exists int
		err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", m.version, formatTime(time.Now())); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func (s *Store) InsertSnapshot(ctx context.Context, snap provider.Snapshot, opts InsertOptions) error {
	if snap.Provider == "" {
		return errors.New("snapshot provider is required")
	}
	if snap.AccountID == "" {
		return errors.New("snapshot account id is required")
	}
	if snap.ObservedAt.IsZero() {
		snap.ObservedAt = time.Now()
	}
	if snap.Source == "" {
		snap.Source = "unknown"
	}

	var rawJSON sql.NullString
	if opts.StoreRawJSON && len(snap.Raw) > 0 {
		raw, err := marshalRedactedRaw(snap.Raw)
		if err != nil {
			return err
		}
		rawJSON = sql.NullString{String: raw, Valid: true}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin insert snapshot: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO live_usage_samples (
  observed_at,
  provider,
  account_id,
  plan_type,
  window_name,
  used_percent,
  remaining_percent,
  reset_at,
  window_seconds,
  limit_reached,
  source,
  raw_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`)
	if err != nil {
		return fmt.Errorf("prepare insert sample: %w", err)
	}
	defer stmt.Close()

	for _, win := range snap.Windows {
		if win.Name == "" {
			return errors.New("window name is required")
		}
		if _, err := stmt.ExecContext(
			ctx,
			formatTime(snap.ObservedAt),
			snap.Provider,
			snap.AccountID,
			nullString(snap.PlanType),
			provider.NormalizeWindowName(win.Name),
			provider.ClampPercent(win.UsedPercent),
			nullFloat64(win.RemainingPercent),
			nullTime(win.ResetAt),
			nullInt(win.WindowSeconds),
			boolInt(win.LimitReached),
			snap.Source,
			rawJSON,
		); err != nil {
			return fmt.Errorf("insert sample %s: %w", win.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit insert snapshot: %w", err)
	}
	return nil
}

func (s *Store) RecordPollRun(ctx context.Context, run PollRun) error {
	if run.Provider == "" {
		return errors.New("poll run provider is required")
	}
	if run.AccountID == "" {
		return errors.New("poll run account id is required")
	}
	if run.Status == "" {
		return errors.New("poll run status is required")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO poll_runs (
  started_at,
  finished_at,
  provider,
  account_id,
  status,
  http_status,
  error_code,
  error_message,
  latency_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
`,
		formatTime(run.StartedAt),
		nullTime(run.FinishedAt),
		run.Provider,
		run.AccountID,
		run.Status,
		nullInt(run.HTTPStatus),
		nullString(run.ErrorCode),
		nullString(run.ErrorMessage),
		nullInt(run.LatencyMS),
	)
	if err != nil {
		return fmt.Errorf("record poll run: %w", err)
	}
	return nil
}

func (s *Store) PollRuns(ctx context.Context, filter PollRunFilter) ([]PollRun, error) {
	var args []any
	var where []string

	if filter.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, filter.AccountID)
	}
	if filter.Since != nil {
		where = append(where, "started_at >= ?")
		args = append(args, formatTime(*filter.Since))
	}

	query := `
SELECT
  id,
  started_at,
  finished_at,
  provider,
  account_id,
  status,
  http_status,
  error_code,
  error_message,
  latency_ms
FROM poll_runs`
	if len(where) > 0 {
		query += "\nWHERE " + strings.Join(where, " AND ")
	}
	query += "\nORDER BY started_at ASC, id ASC"
	if filter.Limit > 0 {
		query += "\nLIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query poll runs: %w", err)
	}
	defer rows.Close()

	var out []PollRun
	for rows.Next() {
		run, err := scanPollRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate poll runs: %w", err)
	}
	return out, nil
}

func (s *Store) LatestSamples(ctx context.Context, providerName, accountID string) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  s.id,
  s.observed_at,
  s.provider,
  s.account_id,
  s.plan_type,
  s.window_name,
  s.used_percent,
  s.remaining_percent,
  s.reset_at,
  s.window_seconds,
  s.limit_reached,
  s.source,
  s.raw_json,
  s.created_at
FROM live_usage_samples s
JOIN (
  SELECT window_name, MAX(observed_at) AS observed_at
  FROM live_usage_samples
  WHERE provider = ? AND account_id = ?
  GROUP BY window_name
) latest
  ON latest.window_name = s.window_name
 AND latest.observed_at = s.observed_at
WHERE s.provider = ? AND s.account_id = ?
ORDER BY s.window_name;
`, providerName, accountID, providerName, accountID)
	if err != nil {
		return nil, fmt.Errorf("query latest samples: %w", err)
	}
	defer rows.Close()

	return scanSamples(rows)
}

func (s *Store) History(ctx context.Context, filter HistoryFilter) ([]Sample, error) {
	var args []any
	var where []string

	if filter.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.AccountID != "" {
		where = append(where, "account_id = ?")
		args = append(args, filter.AccountID)
	}
	if filter.WindowName != "" {
		where = append(where, "window_name = ?")
		args = append(args, provider.NormalizeWindowName(filter.WindowName))
	}
	if filter.Since != nil {
		where = append(where, "observed_at >= ?")
		args = append(args, formatTime(*filter.Since))
	}

	query := `
SELECT
  id,
  observed_at,
  provider,
  account_id,
  plan_type,
  window_name,
  used_percent,
  remaining_percent,
  reset_at,
  window_seconds,
  limit_reached,
  source,
  raw_json,
  created_at
FROM live_usage_samples`
	if len(where) > 0 {
		query += "\nWHERE " + strings.Join(where, " AND ")
	}
	query += "\nORDER BY observed_at ASC, provider ASC, account_id ASC, window_name ASC"
	if filter.Limit > 0 {
		query += "\nLIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	return scanSamples(rows)
}

func (s *Store) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	}
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure sqlite %q: %w", pragma, err)
		}
	}
	return nil
}

func scanSamples(rows *sql.Rows) ([]Sample, error) {
	var out []Sample
	for rows.Next() {
		sample, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate samples: %w", err)
	}
	return out, nil
}

func scanSample(rows *sql.Rows) (Sample, error) {
	var sample Sample
	var observedAt string
	var planType sql.NullString
	var remaining sql.NullFloat64
	var resetAt sql.NullString
	var windowSeconds sql.NullInt64
	var limitReached int
	var rawJSON sql.NullString
	var createdAt string

	if err := rows.Scan(
		&sample.ID,
		&observedAt,
		&sample.Provider,
		&sample.AccountID,
		&planType,
		&sample.WindowName,
		&sample.UsedPercent,
		&remaining,
		&resetAt,
		&windowSeconds,
		&limitReached,
		&sample.Source,
		&rawJSON,
		&createdAt,
	); err != nil {
		return Sample{}, fmt.Errorf("scan sample: %w", err)
	}

	parsedObservedAt, err := parseTime(observedAt)
	if err != nil {
		return Sample{}, fmt.Errorf("parse observed_at: %w", err)
	}
	sample.ObservedAt = parsedObservedAt

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return Sample{}, fmt.Errorf("parse created_at: %w", err)
	}
	sample.CreatedAt = parsedCreatedAt

	if planType.Valid {
		sample.PlanType = planType.String
	}
	if remaining.Valid {
		v := remaining.Float64
		sample.RemainingPercent = &v
	}
	if resetAt.Valid {
		t, err := parseTime(resetAt.String)
		if err != nil {
			return Sample{}, fmt.Errorf("parse reset_at: %w", err)
		}
		sample.ResetAt = &t
	}
	if windowSeconds.Valid {
		v := int(windowSeconds.Int64)
		sample.WindowSeconds = &v
	}
	sample.LimitReached = limitReached != 0
	if rawJSON.Valid {
		sample.RawJSON = &rawJSON.String
	}

	return sample, nil
}

func scanPollRun(rows *sql.Rows) (PollRun, error) {
	var run PollRun
	var startedAt string
	var finishedAt sql.NullString
	var httpStatus sql.NullInt64
	var errorCode sql.NullString
	var errorMessage sql.NullString
	var latencyMS sql.NullInt64

	if err := rows.Scan(
		&run.ID,
		&startedAt,
		&finishedAt,
		&run.Provider,
		&run.AccountID,
		&run.Status,
		&httpStatus,
		&errorCode,
		&errorMessage,
		&latencyMS,
	); err != nil {
		return PollRun{}, fmt.Errorf("scan poll run: %w", err)
	}

	parsedStartedAt, err := parseTime(startedAt)
	if err != nil {
		return PollRun{}, fmt.Errorf("parse poll started_at: %w", err)
	}
	run.StartedAt = parsedStartedAt

	if finishedAt.Valid {
		parsedFinishedAt, err := parseTime(finishedAt.String)
		if err != nil {
			return PollRun{}, fmt.Errorf("parse poll finished_at: %w", err)
		}
		run.FinishedAt = &parsedFinishedAt
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int64)
		run.HTTPStatus = &v
	}
	if errorCode.Valid {
		run.ErrorCode = errorCode.String
	}
	if errorMessage.Valid {
		run.ErrorMessage = errorMessage.String
	}
	if latencyMS.Valid {
		v := int(latencyMS.Int64)
		run.LatencyMS = &v
	}

	return run, nil
}

func marshalRedactedRaw(raw map[string]any) (string, error) {
	redacted := redactValue(raw)
	data, err := json.Marshal(redacted)
	if err != nil {
		return "", fmt.Errorf("marshal redacted raw json: %w", err)
	}
	return string(data), nil
}

func redactValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, inner := range v {
			if isSecretKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactValue(inner)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = redactValue(inner)
		}
		return out
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range []string{"access_token", "refresh_token", "id_token", "authorization", "cookie", "session"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullFloat64(value *float64) sql.NullFloat64 {
	if value == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *value, Valid: true}
}

func nullInt(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}

func nullTime(value *time.Time) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*value), Valid: true}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
