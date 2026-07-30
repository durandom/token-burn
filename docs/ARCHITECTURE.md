# Architecture

## Overview

`token-burn` is a local observability agent for AI subscription quota windows.
It is designed for normal logged-in developer workstations across macOS and
Linux first, with Windows kept viable where the provider credential sources
allow it.

```text
Provider credential stores
        |
        v
Provider live usage APIs
        |
        v
token-burn daemon -- periodic poll
        |
        +--> SQLite local history
        |
        +--> OpenTelemetry metrics exporter
        |
        +--> CLI/TUI read SQLite for status/history/forecast
```

## Components

### CLI and TUI

The CLI starts one-shot fetches, reads the local database, manages the daemon,
and prints JSON for scripts.

The TUI is a read-only dashboard over SQLite. It does not poll providers
directly, so refreshing the dashboard does not increase provider request volume.
The bar chart is the glanceable primary surface; detail text is the slower
digital readout.

### Daemon

The daemon owns polling cadence, retries, provider execution, persistence, and
OTel export. It should be safe to run for weeks without supervision beyond the
platform's user service manager restarting it on failure.

The service layer should stay platform-specific and thin:

- macOS: LaunchAgent.
- Linux: systemd user service.
- Windows: later user-level service support if provider authentication can be
  read without extra setup.

### Providers

Providers are narrow clients around live usage endpoints. They should return
normalized quota windows and preserve selected raw metadata for diagnostics.

Providers must not inspect local sessions, transcripts, or token logs.

Providers must not implement a separate authentication flow or store their own
provider credentials. They reuse the standard local login artifacts already
created by the vendor tool, such as Codex auth files, Claude Code OAuth
credentials, the logged-in GitHub CLI session, Antigravity OAuth state, or Pi's
provider OAuth entries. Missing credentials require login with the vendor tool.
Providers may refresh an expired access token when an existing vendor refresh
token is available; rejected refresh credentials require login again.

Providers may refresh short-lived access tokens with an existing vendor refresh
token when that is the normal vendor credential shape, but they must not run a
new login flow. Prefer a token-burn-owned access-token cache. A shared vendor
credential may be updated only when rotation must be persisted, the provider's
locking, symlink, ownership-compromise, and file-mode contract is implemented,
and unrelated credential fields are preserved. Refresh work must have a bounded
lock wait and be cancelled if lock ownership is lost. OAuth client credentials
should come from local environment or
vendor-owned state; a public client identifier may be pinned only when it is
part of the vendor tool's existing OAuth contract.

The provider-owned live endpoint is the source of truth. This is what lets
`token-burn` observe quota usage caused by the same account on other machines,
which local transcript parsing cannot do. Pi request/session token counters are
therefore deliberately excluded even when Pi supplies the OAuth credential.

### Store

SQLite is the durable source for local history and forecasts. OTel is the
external observation stream, not the only data store.

This is intentionally not an embedded Prometheus or OpenTelemetry time-series
store. The local daemon needs a small, queryable, durable history for CLI
commands and forecast calculations. SQLite is enough for the expected write
volume and keeps local inspection, retention, and migrations simple.

OpenTelemetry export remains the path for long-term time-series storage,
dashboards, and alerting. A collector can route metrics to Prometheus-compatible
backends, Grafana Cloud, VictoriaMetrics, ClickHouse, or another observability
store without changing local persistence.

### Forecast

Forecasting reads recent samples from SQLite. It is not embedded in provider
clients.

Forecast output includes:

- burn rate in percent per hour
- estimated 90% and 100% exhaustion time
- projected percent at reset
- confidence

Projected percent at reset is not capped at `100%`. Values above `100%` are
intentional and show the expected overshoot if the current burn rate continues.
The TUI bar remains visually capped, while the text and OTel metric keep the
actual projection.

Forecasting treats material usage jumps within a few minutes as a new segment.
This avoids turning provider-side batch updates or local measurement fixes into
impossible burn rates.

### OTel Export

The daemon exports current gauges and forecast gauges. Historical graphing is
expected to happen in the OpenTelemetry backend, while SQLite remains useful for
offline CLI history and debugging.

## Error Handling

Provider errors should be typed:

- auth missing
- auth expired
- rate limited by endpoint
- transient HTTP failure
- invalid response
- unsupported account shape

The daemon records failures in SQLite and logs redacted details.

## Poll Cadence

Default interval: five minutes.

The endpoints are undocumented or semi-private. Polling faster than 60 seconds
should require explicit configuration and a CLI warning.

## Config

The default config path is XDG-driven:

```text
${XDG_CONFIG_HOME:-~/.config}/token-burn/config.toml
```

The file is created automatically when it is first loaded. There is no separate
init command. The generated TOML is intentionally small and keeps provider
authentication delegated to each provider's normal local auth artifacts.

Default TOML shape:

```toml
poll_interval = "5m"
http_timeout = "15s"
database_path = "/home/alice/.local/state/token-burn/token-burn.db"

[otel]
enabled = false
endpoint = "http://localhost:4318"
protocol = "http/protobuf"
export_interval = "60s"

[[accounts]]
provider = "codex"
id = "codex-default"

[[accounts]]
provider = "claude"
id = "claude-default"

[[accounts]]
provider = "copilot"
id = "copilot-default"

[[accounts]]
provider = "antigravity"
id = "antigravity-default"

[[accounts]]
provider = "xai"
id = "xai-default"
```

## Data Flow Details

1. Daemon tick starts.
2. Each configured account is fetched with context timeout.
3. Provider returns normalized windows.
4. Store writes one row per window.
5. Forecast module computes derived values for latest windows.
6. OTel exporter updates gauges.
7. CLI/TUI read latest rows and forecast from SQLite history.

## Roadmap Notes

- Retention cleanup should run at daemon start and then daily.
- Raw JSON should remain opt-in diagnostics.
- Token refresh must remain narrowly scoped to existing vendor refresh tokens;
  shared-store updates require vendor-compatible locking and field preservation.
