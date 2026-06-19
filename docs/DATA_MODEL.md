# Data Model

## Storage Decision

Use SQLite as the local durable history store.

The data is time-series shaped, but the expected volume is small: a poll every
60 seconds, a handful of accounts, and a small fixed set of quota windows. SQLite
keeps local CLI queries, forecast reads, migrations, and retention simple.

Prometheus and OpenTelemetry are export paths rather than local storage formats
for v1. The daemon exports current samples and forecasts through OTLP so an
external collector/backend can own long-term time-series retention and
dashboards.

## SQLite Schema Draft

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

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
```

`projected_reset_percent` is intentionally not bounded to `100`. A value above
`100` means the current burn rate would overshoot the quota before reset.

## Normalized Windows

Window names should be stable and low-cardinality:

- `five_hour`
- `seven_day`
- `seven_day_sonnet`
- `seven_day_opus`
- `extra_usage`
- `code_review_primary`
- `code_review_secondary`
- `additional_<sanitized_feature>_primary`
- `additional_<sanitized_feature>_secondary`
- `premium_interactions`
- `chat`
- `completions`
- `ai_credits`

Additional provider-internal feature names may be present in storage, but UI
surfaces should prefer stable human labels when the raw feature name is not
useful to the user.

## Raw JSON Policy

Default should be `raw_json = NULL` unless diagnostics are enabled.

If stored, redact:

- `access_token`
- `refresh_token`
- `id_token`
- `authorization`
- `cookie`
- `session`

## Retention

Suggested defaults:

- keep samples for 90 days
- keep poll errors for 30 days
- keep forecasts for 90 days

Retention should run once per daemon start and then daily.
