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
- `gemini`
- `gemini_weekly`
- `claude_and_gpt`
- `claude_and_gpt_weekly`

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


## Reset credits and plan-limit history (migration 3)

Reset data is inserted atomically with usage snapshots, even when a snapshot has
no quota windows. All timestamp columns contain UTC RFC3339 values.

| Table | Key | Stored data |
| --- | --- | --- |
| `reset_credits` | provider, account_id, id | status, granted_at, expires_at, redeemed_at, observed_at; normalized credit JSON with scope, counts, and usability flags |
| `reset_events` | provider, account_id, id | credit_id, kind (`granted`/`used`), occurred_at, source (`api`/`inferred`); normalized JSON includes event count |
| `plan_limit_periods` | provider, account_id, id | starts_at, ends_at; normalized JSON with plan, window_minutes, accounting_complete, used_basis_points and breakdowns |
| `reset_state` | provider, account_id | latest summary/program metadata, plan coverage metadata, detail attempt time, errors and observed_at |

IDs are upsert keys within an account, never telemetry attributes. Duplicate API
events are idempotent; inferred use IDs include the grant ID and observation
time. Repeated or older snapshots cannot infer duplicate Claude uses. Missing
credits in a complete list become `unavailable`; historical credits/events remain.
A partial or failed list leaves known credits intact. Credits from older
snapshots cannot overwrite newer credit observations.

Normalized JSON and program metadata always pass through the secret redactor.
These fields are part of the data model and do not require `--raw-json`.

`token-burn resets [--provider codex|claude] [--account <id>] [--json]` reads the
local store only. Text lists credits, grant/use events, and plan periods; JSON
also includes program flags, breakdowns, coverage, polling errors, and the detail
attempt time. The TUI reads owned resets, next expiry, and last use from SQLite.
`forecast` includes reset capacity when known, without assuming redemption in
its burn-rate projection. Unknown counts stay unknown rather than becoming zero.

## Overage-credit observations (migration 4)

`overage_credit_snapshots` stores one row per `(provider, account_id, observed_at)`
with redacted normalized credit JSON. Observation times use UTC with a fixed
nine-digit fractional part, preserving subsecond ordering. Repeated timestamps
upsert the same row. Decimal balance strings and approximate message ranges are
retained without rounding; the balance is separate from reset capacity.

A Codex observation that omits `credits` stores NULL data. Latest-credit reads
then return unknown instead of carrying forward an older balance. Historical
rows remain available through the store's `OverageCreditHistory` query.
`forecast --json` exposes `overage_credits` alongside `reset_capacity`; the local
TUI shows the latest balance, unlimited state, and overage-limit warning. No
expiry field is synthesized.

Manual `credit_grants` remain in XDG config rather than SQLite API observations.
The read-only TUI, `forecast`, and `resets` overlay a
`configured_credit_capacity` object with `source=configured`, active grants,
next expiry, and the amount expiring capped at the nonnegative live balance.
The original configured amount is retained separately from each independent cap.
