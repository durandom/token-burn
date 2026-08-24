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
provider      # codex, claude, copilot, antigravity, xai
account_id    # configured alias/id, not email
window        # five_hour, seven_day, ai_credits, gemini, weekly, quota, etc.
plan_type     # plus, pro, max, team, supergrok, unknown
source        # wham_usage, anthropic_oauth_usage, github_copilot,
              # google_cloud_code_fetch_available_models, xai_grok_cli_billing
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
