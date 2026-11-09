# OpenTelemetry Plan

## Export Mode

Export metrics via OTLP to an OpenTelemetry Collector.

Default endpoint:

```text
http://localhost:4318
```

Prefer HTTP/protobuf for easy local collector setup. gRPC can be added later.

## Resource Attributes

Set bounded resource attributes:

```text
service.name = token-burn
service.version = <build version>
deployment.environment = local
```

Avoid account email as an attribute. Use account aliases or account IDs only.

## Metric Names

Gauges:

```text
token_burn_usage_used_percent
token_burn_usage_remaining_percent
token_burn_usage_reset_unix_seconds
token_burn_usage_seconds_to_reset
token_burn_usage_window_seconds
token_burn_resets_available
token_burn_resets_seconds_to_next_expiry
token_burn_forecast_burn_rate_percent_per_hour
token_burn_forecast_projected_reset_percent
token_burn_forecast_estimated_90_unix_seconds
token_burn_forecast_estimated_100_unix_seconds
token_burn_forecast_confidence
```

`token_burn_forecast_projected_reset_percent` is the projected usage percent at
the current reset time if the observed burn rate continues. It is not capped at
`100`; values above `100` represent projected overshoot before reset.

Counters:

```text
token_burn_poll_runs_total
token_burn_poll_errors_total
```

Histograms:

```text
token_burn_poll_latency_ms
```

## Metric Attributes

Allowed attributes:

```text
provider      # codex, claude, copilot, antigravity, xai, zai
account_id    # configured alias/id, not email
window        # five_hour, seven_day, ai_credits, gemini, weekly, quota, etc.
plan_type     # plus, pro, max, team, supergrok, unknown
source        # wham_usage, anthropic_oauth_usage, github_copilot,
              # google_cloud_code_fetch_available_models, xai_grok_cli_billing,
              # zai_monitor_quota
```

Avoid high-cardinality attributes:

- raw reset timestamp as string
- email
- full organization ID unless explicitly configured
- error body
- raw endpoint URL with query strings

## Collector Sketch

Example local collector config:

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 127.0.0.1:4318

processors:
  batch:

exporters:
  debug:
    verbosity: basic

service:
  pipelines:
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [debug]
```

## CLI Validation

`token-burn otel-test` should emit a synthetic metric with:

```text
provider = test
account_id = test
window = test
```

## Dashboards

An OpenObserve dashboard template is included at
`contrib/openobserve/token-burn.dashboard.json`. See
[DASHBOARDS.md](DASHBOARDS.md) for import notes and dashboard scope.

## Reading Metrics Back

OTLP is an ingestion protocol and does not provide a read API. `token-burn`
supports reading its exported metrics through OpenObserve's Search API for the
`tui`, `status`, `once`, `history`, and `forecast` commands. The read endpoint
is configured separately from the OTLP export endpoint under `[otel.read]`.
Basic Auth credentials can be stored directly as `username` and `password` in
the mode-0600 config file, or read from the configured environment-variable
names. Direct values take precedence.

In `auto` mode, fresh local SQLite samples win. OpenObserve is queried when the
local database cannot be opened, is empty, or its latest samples are stale. Use
`openobserve` to force remote reads, or `sqlite` to disable them.


## Reset capacity gauges

- `token_burn_resets_available`: owned banked resets (sum of `resets_left` for
  available Claude grants; Codex's live available count). Zero is exported when
  the API reports zero; unknown/unsupported counts produce no sample.
- `token_burn_resets_seconds_to_next_expiry`: seconds until the earliest future
  expiry among owned available credits. Emitted only when an expiry is known and
  the owned count is nonzero. Expired or used credits do not supply an expiry.

Both gauges carry only `provider` and configured `account_id`. They never carry
email, credit/grant IDs, event IDs, plan-period IDs, or a per-window attribute.
The daemon enriches usage snapshots from SQLite during slow-detail poll gaps,
so the available gauge refreshes with usage and expiry seconds decrease between
detail polls. Consumers should treat a missing expiry sample as unknown rather
than carry it forward indefinitely. Historical reset events and plan breakdowns
stay in SQLite and are available through `resets --json`.

## Overage-credit gauges

All four gauges use only `provider` and configured `account_id` attributes,
without account email or a per-window attribute.

- `token_burn_overage_credits_balance`: the numeric value of the Codex API's
  decimal credit balance, in provider credit units (OTel unit `{credit}`).
  Zero balances are exported;
  missing, non-finite, or invalid values are omitted. Negative balances reported
  by the provider are retained. OTel's float
  conversion may round; SQLite retains the original decimal string.
- `token_burn_overage_credits_has_credits`: `1` when the API's `has_credits`
  flag is true, otherwise `0` (unit `1`, dimensionless).
- `token_burn_overage_credits_unlimited`: `1` when the API's `unlimited`
  flag is true, otherwise `0` (unit `1`, dimensionless).
- `token_burn_overage_limit_reached`: `1` when the API reports the overage limit
  reached, otherwise `0` when a credit object is present.
  Unit `1` (dimensionless).

The three flags are emitted whenever `Snapshot.OverageCredits` is non-nil,
including when all flags are false or the balance is unavailable. No overage
gauges are recorded for a nil credit object. These flags are independent:
readers must preserve the reported values rather than infer them from balance.
They are emitted from the existing usage poll; overage credits never contribute
to reset-count metrics. An unlimited account without a numeric balance emits
no balance sample.
No API expiry metric is emitted because no expiry source has been verified.

## Configured credit expiry

`token_burn_credit_seconds_to_next_expiry` reports seconds until the earliest
unexpired manually configured credit grant. `token_burn_credit_amount_expiring`
reports the sum configured for that expiry capped at the nonnegative live balance.
Zero is valid; the amount gauge is omitted when no numeric balance is known.
Expired grants produce no expiry sample.

Both gauges carry `provider`, configured `account_id`, and the fixed
`source=configured`. Labels and individual grant dates are never attributes.
These metrics describe configured deadlines and caps, not verified API grant
balances or an inferred consumption order.

### Reading overage credits from OpenObserve

With `otel.read.mode = "openobserve"`, `status --json` and `forecast --json`
include an account-level `overage_credits` object on each usage-window row:

```json
{
  "overage_credits": {
    "has_credits": true,
    "unlimited": false,
    "overage_limit_reached": false,
    "balance": "62750",
    "observed_at": "2026-11-09T12:00:00Z"
  }
}
```

Existing status row fields retain their names (`Provider`, `AccountID`,
`WindowName`, `ObservedAt`, etc.); forecast rows use `provider`, `account_id`,
`window_name`, etc. `once --json` carries the same credit object in each
snapshot's existing `OverageCredits` field.

Credit gauges are joined by `(provider, account_id)`, independently of windows.
The reader requires all three boolean gauges within a two-second timestamp
span (allowing SDK collection skew), inside `otel.read.lookback`, no more than two minutes before the read time,
and not in the future. Flags must be `0` or `1`; zero is a valid observation.
`observed_at` is the oldest of those flag timestamps in UTC. An absent stream, incomplete
flag cohort, or stale cohort leaves credits nil (`overage_credits` is omitted
from status/forecast JSON). Usage and forecasts remain available.

Balance is optional and is included only within two seconds of every flag
and no more than two minutes old;
an older balance is never carried into a newer unlimited or balance-less
observation. It is a decimal string reconstructed from the numeric gauge, so
original decimal precision and trailing zeros cannot be recovered. A zero
balance is `"0"`; negative balances are retained. The flags are never inferred
from balance. Historical usage reads do not attach credit history; forecasts
attach the latest fresh credit observation using the configured read lookback,
independently of their `--since` history range.
