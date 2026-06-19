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

Projected percent at reset may exceed `100` when current burn would overshoot a
quota before reset. Dashboard panels should not clamp this metric unless they
explicitly want a visual progress bar.

The `token-burn otel-test` command emits a synthetic sample with
`provider = test`. The dashboard intentionally filters those samples out with
`provider <> 'test'`.

## Importing

Import `contrib/openobserve/token-burn.dashboard.json` through OpenObserve's
dashboard import flow, or use your own OpenObserve automation to create a
dashboard from the JSON template.

The template is intentionally OpenObserve-specific. Other backends should use
the metric names in [OTEL.md](OTEL.md) as the stable contract.
