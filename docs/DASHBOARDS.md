# Dashboards

`token-burn` exports OpenTelemetry metrics. Dashboards are downstream
visualizations over those metrics, not part of the provider polling path.

## OpenObserve

An OpenObserve dashboard template is included at:

```text
contrib/openobserve/token-burn.dashboard.json
```

It expects OTLP metrics exported by the daemon and uses these streams:

```text
token_burn_usage_used_percent
token_burn_usage_seconds_to_reset
token_burn_forecast_burn_rate_percent_per_hour
token_burn_forecast_projected_reset_percent
token_burn_poll_runs_total
```

The dashboard has two tabs:

- `Overview` shows current used percent, projected percent at reset, hours to
  reset, usage time series, and a latest-window table.
- `Forecast` shows burn rate, reset horizon, and poll run freshness.

Queries group by generic `provider`, `account_id`, and `window` attributes, so
new providers such as GitHub Copilot and Google Antigravity appear without
dashboard JSON changes.

Projected percent at reset may exceed `100` when current burn would overshoot a
quota before reset. Dashboard panels should not clamp this metric unless they
explicitly want a visual progress bar.

The `token-burn otel-test` command emits a synthetic sample with
`provider = test`. The dashboard intentionally filters those samples out with
`provider <> 'test'`.

## Token Burn TUI

A second template mirrors the terminal UI's glanceable layout:

```text
contrib/openobserve/token-burn-tui.dashboard.json
```

The `Now` tab shows one block per provider/account: a stacked horizontal bar
per window (used / forecasted additional usage by reset / free, matching the
TUI bar legend) with a detail table underneath (used %, hours to reset, burn
%/h, projected % at reset — uncapped). The `History` tab keeps a used-percent
time series.

Unlike the generic template, the account blocks are fixed: each panel filters
on `provider` and `account_id` literals. To add an account, duplicate one
bar+table panel pair and adjust the two literals in both queries. Windows
appear automatically — no window names are hardcoded.

## Importing

Import `contrib/openobserve/token-burn.dashboard.json` through OpenObserve's
dashboard import flow, or use your own OpenObserve automation to create a
dashboard from the JSON template.

The template is intentionally OpenObserve-specific. Other backends should use
the metric names in [OTEL.md](OTEL.md) as the stable contract.
