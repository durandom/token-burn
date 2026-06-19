package otel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/durandom/token-burn/internal/forecast"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	MetricUsageUsedPercent              = "token_burn_usage_used_percent"
	MetricUsageRemainingPercent         = "token_burn_usage_remaining_percent"
	MetricUsageResetUnixSeconds         = "token_burn_usage_reset_unix_seconds"
	MetricUsageSecondsToReset           = "token_burn_usage_seconds_to_reset"
	MetricUsageWindowSeconds            = "token_burn_usage_window_seconds"
	MetricForecastBurnRatePercentPerHr  = "token_burn_forecast_burn_rate_percent_per_hour"
	MetricForecastProjectedResetPercent = "token_burn_forecast_projected_reset_percent"
	MetricForecastEstimated90Unix       = "token_burn_forecast_estimated_90_unix_seconds"
	MetricForecastEstimated100Unix      = "token_burn_forecast_estimated_100_unix_seconds"
	MetricForecastConfidence            = "token_burn_forecast_confidence"
	MetricPollRunsTotal                 = "token_burn_poll_runs_total"
	MetricPollErrorsTotal               = "token_burn_poll_errors_total"
)

type Config struct {
	Endpoint       string
	ExportInterval time.Duration
	ServiceVersion string
}

type Recorder interface {
	RecordGauge(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue)
	AddCounter(ctx context.Context, name string, value int64, attrs ...attribute.KeyValue)
}

type Exporter struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter
	mu       sync.Mutex
	gauges   map[string]metric.Float64Gauge
	counters map[string]metric.Int64Counter
}

type ForecastMetric struct {
	Provider  string
	AccountID string
	Window    string
	PlanType  string
	Source    string
	Result    forecast.Result
}

func NewOTLP(ctx context.Context, cfg Config) (*Exporter, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:4318"
	}
	if cfg.ExportInterval <= 0 {
		cfg.ExportInterval = 60 * time.Second
	}
	if cfg.ServiceVersion == "" {
		cfg.ServiceVersion = "dev"
	}

	exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.ExportInterval))
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "token-burn"),
			attribute.String("service.version", cfg.ServiceVersion),
			attribute.String("deployment.environment", "local"),
		)),
	)

	return &Exporter{
		provider: mp,
		meter:    mp.Meter("github.com/durandom/token-burn"),
		gauges:   map[string]metric.Float64Gauge{},
		counters: map[string]metric.Int64Counter{},
	}, nil
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	if e == nil || e.provider == nil {
		return nil
	}
	return e.provider.Shutdown(ctx)
}

func (e *Exporter) ForceFlush(ctx context.Context) error {
	if e == nil || e.provider == nil {
		return nil
	}
	return e.provider.ForceFlush(ctx)
}

func (e *Exporter) RecordGauge(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue) {
	gauge, err := e.float64Gauge(name)
	if err != nil {
		return
	}
	gauge.Record(ctx, value, metric.WithAttributes(attrs...))
}

func (e *Exporter) AddCounter(ctx context.Context, name string, value int64, attrs ...attribute.KeyValue) {
	counter, err := e.int64Counter(name)
	if err != nil {
		return
	}
	counter.Add(ctx, value, metric.WithAttributes(attrs...))
}

func EmitSnapshot(ctx context.Context, recorder Recorder, snap usageprovider.Snapshot, now time.Time) {
	for _, win := range snap.Windows {
		attrs := usageAttrs(snap, win)
		recorder.RecordGauge(ctx, MetricUsageUsedPercent, win.UsedPercent, attrs...)
		if win.RemainingPercent != nil {
			recorder.RecordGauge(ctx, MetricUsageRemainingPercent, *win.RemainingPercent, attrs...)
		}
		if win.ResetAt != nil {
			recorder.RecordGauge(ctx, MetricUsageResetUnixSeconds, float64(win.ResetAt.Unix()), attrs...)
			recorder.RecordGauge(ctx, MetricUsageSecondsToReset, win.ResetAt.Sub(now.UTC()).Seconds(), attrs...)
		}
		if win.WindowSeconds != nil {
			recorder.RecordGauge(ctx, MetricUsageWindowSeconds, float64(*win.WindowSeconds), attrs...)
		}
	}
	recorder.AddCounter(ctx, MetricPollRunsTotal, 1, attribute.String("provider", snap.Provider), attribute.String("account_id", snap.AccountID))
}

func EmitPollError(ctx context.Context, recorder Recorder, providerName, accountID, code string) {
	recorder.AddCounter(
		ctx,
		MetricPollErrorsTotal,
		1,
		attribute.String("provider", providerName),
		attribute.String("account_id", accountID),
		attribute.String("error_code", code),
	)
}

func EmitForecast(ctx context.Context, recorder Recorder, metric ForecastMetric) {
	attrs := []attribute.KeyValue{
		attribute.String("provider", metric.Provider),
		attribute.String("account_id", metric.AccountID),
		attribute.String("window", metric.Window),
		attribute.String("plan_type", valueOrUnknown(metric.PlanType)),
		attribute.String("source", valueOrUnknown(metric.Source)),
	}
	if metric.Result.BurnRatePercentPerHour != nil {
		recorder.RecordGauge(ctx, MetricForecastBurnRatePercentPerHr, *metric.Result.BurnRatePercentPerHour, attrs...)
	}
	if metric.Result.ProjectedResetPercent != nil {
		recorder.RecordGauge(ctx, MetricForecastProjectedResetPercent, *metric.Result.ProjectedResetPercent, attrs...)
	}
	if metric.Result.Estimated90At != nil {
		recorder.RecordGauge(ctx, MetricForecastEstimated90Unix, float64(metric.Result.Estimated90At.Unix()), attrs...)
	}
	if metric.Result.Estimated100At != nil {
		recorder.RecordGauge(ctx, MetricForecastEstimated100Unix, float64(metric.Result.Estimated100At.Unix()), attrs...)
	}
	recorder.RecordGauge(ctx, MetricForecastConfidence, metric.Result.Confidence, attrs...)
}

func usageAttrs(snap usageprovider.Snapshot, win usageprovider.Window) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("provider", snap.Provider),
		attribute.String("account_id", snap.AccountID),
		attribute.String("window", win.Name),
		attribute.String("plan_type", valueOrUnknown(snap.PlanType)),
		attribute.String("source", valueOrUnknown(snap.Source)),
	}
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func (e *Exporter) float64Gauge(name string) (metric.Float64Gauge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if gauge, ok := e.gauges[name]; ok {
		return gauge, nil
	}
	gauge, err := e.meter.Float64Gauge(name)
	if err != nil {
		return nil, err
	}
	e.gauges[name] = gauge
	return gauge, nil
}

func (e *Exporter) int64Counter(name string) (metric.Int64Counter, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if counter, ok := e.counters[name]; ok {
		return counter, nil
	}
	counter, err := e.meter.Int64Counter(name)
	if err != nil {
		return nil, err
	}
	e.counters[name] = counter
	return counter, nil
}
