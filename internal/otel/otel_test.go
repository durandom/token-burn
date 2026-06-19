package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/forecast"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"go.opentelemetry.io/otel/attribute"
)

type record struct {
	name  string
	value float64
	attrs map[string]string
}

type count struct {
	name  string
	value int64
	attrs map[string]string
}

type fakeRecorder struct {
	gauges   []record
	counters []count
}

func (f *fakeRecorder) RecordGauge(ctx context.Context, name string, value float64, attrs ...attribute.KeyValue) {
	f.gauges = append(f.gauges, record{name: name, value: value, attrs: attrsMap(attrs)})
}

func (f *fakeRecorder) AddCounter(ctx context.Context, name string, value int64, attrs ...attribute.KeyValue) {
	f.counters = append(f.counters, count{name: name, value: value, attrs: attrsMap(attrs)})
}

func TestEmitSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	remaining := 88.0
	windowSeconds := 18000
	recorder := &fakeRecorder{}

	EmitSnapshot(context.Background(), recorder, usageprovider.Snapshot{
		Provider:  "codex",
		AccountID: "codex-default",
		PlanType:  "plus",
		Source:    "wham_usage",
		Windows: []usageprovider.Window{{
			Name:             "five_hour",
			UsedPercent:      12,
			RemainingPercent: &remaining,
			ResetAt:          &reset,
			WindowSeconds:    &windowSeconds,
		}},
	}, now)

	assertGauge(t, recorder, MetricUsageUsedPercent, 12, map[string]string{
		"provider":   "codex",
		"account_id": "codex-default",
		"window":     "five_hour",
		"plan_type":  "plus",
		"source":     "wham_usage",
	})
	assertGauge(t, recorder, MetricUsageRemainingPercent, 88, nil)
	assertGauge(t, recorder, MetricUsageResetUnixSeconds, float64(reset.Unix()), nil)
	assertGauge(t, recorder, MetricUsageSecondsToReset, 7200, nil)
	assertGauge(t, recorder, MetricUsageWindowSeconds, 18000, nil)
	if len(recorder.counters) != 1 || recorder.counters[0].name != MetricPollRunsTotal {
		t.Fatalf("counters = %#v, want poll run counter", recorder.counters)
	}
}

func TestEmitForecast(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	burn := 10.0
	projectedReset := 60.0
	estimated90 := now.Add(8 * time.Hour)
	estimated100 := now.Add(9 * time.Hour)
	recorder := &fakeRecorder{}

	EmitForecast(context.Background(), recorder, ForecastMetric{
		Provider:  "claude",
		AccountID: "claude-default",
		Window:    "five_hour",
		Source:    "anthropic_oauth_usage",
		Result: forecast.Result{
			ComputedAt:             now,
			SampleCount:            3,
			BurnRatePercentPerHour: &burn,
			ProjectedResetPercent:  &projectedReset,
			Estimated90At:          &estimated90,
			Estimated100At:         &estimated100,
			Confidence:             0.8,
		},
	})

	assertGauge(t, recorder, MetricForecastBurnRatePercentPerHr, 10, map[string]string{
		"provider":   "claude",
		"account_id": "claude-default",
		"window":     "five_hour",
		"plan_type":  "unknown",
		"source":     "anthropic_oauth_usage",
	})
	assertGauge(t, recorder, MetricForecastProjectedResetPercent, 60, nil)
	assertGauge(t, recorder, MetricForecastEstimated90Unix, float64(estimated90.Unix()), nil)
	assertGauge(t, recorder, MetricForecastEstimated100Unix, float64(estimated100.Unix()), nil)
	assertGauge(t, recorder, MetricForecastConfidence, 0.8, nil)
}

func TestEmitPollError(t *testing.T) {
	recorder := &fakeRecorder{}
	EmitPollError(context.Background(), recorder, "codex", "codex-default", "auth_expired")

	if len(recorder.counters) != 1 {
		t.Fatalf("counter count = %d, want 1", len(recorder.counters))
	}
	got := recorder.counters[0]
	if got.name != MetricPollErrorsTotal || got.value != 1 {
		t.Fatalf("counter = %#v, want poll error count", got)
	}
	if got.attrs["error_code"] != "auth_expired" {
		t.Fatalf("error_code attr = %q, want auth_expired", got.attrs["error_code"])
	}
}

func TestOTLPExporterPostsMetrics(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exporter, err := NewOTLP(ctx, Config{
		Endpoint:       server.URL,
		ExportInterval: time.Hour,
		ServiceVersion: "test",
	})
	if err != nil {
		t.Fatalf("NewOTLP() error = %v", err)
	}
	defer exporter.Shutdown(context.Background())

	remaining := 99.0
	EmitSnapshot(ctx, exporter, usageprovider.Snapshot{
		Provider:  "test",
		AccountID: "test",
		PlanType:  "test",
		Source:    "otel_test",
		Windows: []usageprovider.Window{{
			Name:             "test",
			UsedPercent:      1,
			RemainingPercent: &remaining,
		}},
	}, time.Now())

	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}

	select {
	case path := <-requests:
		if path == "" {
			t.Fatal("OTLP request path is empty")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for OTLP request")
	}
}

func attrsMap(attrs []attribute.KeyValue) map[string]string {
	out := map[string]string{}
	for _, attr := range attrs {
		out[string(attr.Key)] = attr.Value.AsString()
	}
	return out
}

func assertGauge(t *testing.T, recorder *fakeRecorder, name string, value float64, attrs map[string]string) {
	t.Helper()
	for _, gauge := range recorder.gauges {
		if gauge.name != name || gauge.value != value {
			continue
		}
		for key, want := range attrs {
			if got := gauge.attrs[key]; got != want {
				t.Fatalf("%s attr %s = %q, want %q", name, key, got, want)
			}
		}
		return
	}
	t.Fatalf("missing gauge %s=%v in %#v", name, value, recorder.gauges)
}
