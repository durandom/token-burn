# Roadmap

`token-burn` is already usable as a local daemon, CLI, TUI, SQLite store, and
OpenTelemetry exporter. The roadmap below tracks likely next work without
pretending that provider internals are stable.

## Near Term

- Linux systemd user service install/uninstall/status.
- Retention cleanup for old samples and poll errors.
- Release builds for macOS and Linux.
- Better terminal layout behavior for very narrow terminals.
- More fixtures for provider response shape drift.

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
