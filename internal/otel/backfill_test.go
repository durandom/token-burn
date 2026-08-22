package otel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/store"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

func TestHistoricalExporterPreservesTimestampAndAttributes(t *testing.T) {
	requests := make(chan *collectormetricspb.ExportMetricsServiceRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/metrics" {
			t.Errorf("path = %q, want /v1/metrics", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-protobuf" {
			t.Errorf("content type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var request collectormetricspb.ExportMetricsServiceRequest
		if err := proto.Unmarshal(body, &request); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}
		requests <- &request
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	observed := time.Date(2026, 6, 19, 7, 32, 6, 123456789, time.UTC)
	reset := observed.Add(5 * time.Hour)
	remaining := 75.0
	windowSeconds := 18000
	exporter, err := NewHistoricalExporter(server.URL, "v0.test", server.Client())
	if err != nil {
		t.Fatalf("NewHistoricalExporter() error = %v", err)
	}
	points, err := exporter.Export(context.Background(), []store.Sample{{
		ID: 42, ObservedAt: observed, Provider: "codex", AccountID: "codex-default",
		PlanType: "pro", WindowName: "five_hour", UsedPercent: 25,
		RemainingPercent: &remaining, ResetAt: &reset, WindowSeconds: &windowSeconds,
		Source: "live_usage_api",
	}})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if points != 5 {
		t.Fatalf("points = %d, want 5", points)
	}

	request := <-requests
	rm := request.ResourceMetrics[0]
	if got := protoAttrs(rm.Resource.Attributes)["service.version"]; got != "v0.test" {
		t.Fatalf("service.version = %q", got)
	}
	metrics := rm.ScopeMetrics[0].Metrics
	if len(metrics) != 5 {
		t.Fatalf("metrics = %d, want 5", len(metrics))
	}
	for _, metric := range metrics {
		point := metric.GetGauge().DataPoints[0]
		if point.TimeUnixNano != uint64(observed.UnixNano()) {
			t.Fatalf("%s timestamp = %d, want %d", metric.Name, point.TimeUnixNano, observed.UnixNano())
		}
		attrs := protoAttrs(point.Attributes)
		if attrs["provider"] != "codex" || attrs["window"] != "five_hour" {
			t.Fatalf("%s attrs = %#v", metric.Name, attrs)
		}
	}
}

func TestHistoricalExporterRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer server.Close()
	exporter, err := NewHistoricalExporter(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = exporter.Export(context.Background(), []store.Sample{{
		ID: 1, ObservedAt: time.Now(), Provider: "codex", AccountID: "a", WindowName: "w", Source: "s",
	}})
	if err == nil {
		t.Fatal("Export() error = nil, want HTTP error")
	}
}

func TestHistoricalExporterRejectsPartialSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, err := proto.Marshal(&collectormetricspb.ExportMetricsServiceResponse{
			PartialSuccess: &collectormetricspb.ExportMetricsPartialSuccess{
				RejectedDataPoints: 1,
				ErrorMessage:       "timestamp outside retention",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	exporter, err := NewHistoricalExporter(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = exporter.Export(context.Background(), []store.Sample{{
		ID: 1, ObservedAt: time.Now(), Provider: "codex", AccountID: "a", WindowName: "w", Source: "s",
	}})
	if err == nil {
		t.Fatal("Export() error = nil, want partial-success rejection")
	}
}

func protoAttrs(attrs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[attr.Key] = attr.Value.GetStringValue()
	}
	return out
}
