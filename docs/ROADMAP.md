# Roadmap

`token-burn` is already usable as a local daemon, CLI, TUI, SQLite store, and
OpenTelemetry exporter. The roadmap below tracks likely next work without
pretending that provider internals are stable.

## Near Term

- Retention cleanup for old samples and poll errors.
- Homebrew formula/tap, if installing via GitHub Releases is not enough.
- Better terminal layout behavior for very narrow terminals.
- More fixtures for provider response shape drift.

## Discovered endpoints to investigate (TODO only)

The 2026-10-11 discovery found these additional read endpoints. Their payloads,
auth requirements, polling cost, and usefulness still need evaluation before
implementation:

- `wham/usage/credit-usage-events`
- `wham/usage/daily-token-usage-breakdown`
- `wham/usage/chatpass/apps`
- `wham/analytics/daily-*`
- `pageConfigs/usage_limits`

## Medium Term

- Windows viability check.
- Optional config for account aliases.
- Additional provider window shapes when they appear.
- Forecast persistence if external consumers need stored forecast history.
- Collector examples for Prometheus-compatible backends.

## Explicit Non-Goals

- Inferring subscription quota from local transcripts, token logs, or session
  files by default.
- Managing separate provider authentication.
- Exporting account email as an OpenTelemetry attribute by default.
- Running a local web server unless a concrete integration needs it.
