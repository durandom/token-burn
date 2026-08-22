package otel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/durandom/token-burn/internal/store"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// HistoricalExporter sends normalized SQLite samples as OTLP gauge points
// while preserving each sample's original observed_at timestamp.
type HistoricalExporter struct {
	endpoint       string
	serviceVersion string
	client         *http.Client
}

func HistoricalPointCount(samples []store.Sample) int {
	total := 0
	for _, sample := range samples {
		total++
		if sample.RemainingPercent != nil {
			total++
		}
		if sample.ResetAt != nil {
			total += 2
		}
		if sample.WindowSeconds != nil {
			total++
		}
	}
	return total
}

func NewHistoricalExporter(endpoint, serviceVersion string, client *http.Client) (*HistoricalExporter, error) {
	endpoint, err := metricsEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if serviceVersion == "" {
		serviceVersion = "dev"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &HistoricalExporter{endpoint: endpoint, serviceVersion: serviceVersion, client: client}, nil
}

// Export sends one bounded batch and returns the number of OTLP data points.
func (e *HistoricalExporter) Export(ctx context.Context, samples []store.Sample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}
	metrics := make(map[string][]*metricspb.NumberDataPoint)
	for _, sample := range samples {
		if sample.ObservedAt.IsZero() {
			return 0, fmt.Errorf("sample %d has no observed_at", sample.ID)
		}
		attrs := historyAttrs(sample)
		addHistoryPoint(metrics, MetricUsageUsedPercent, sample.UsedPercent, sample, attrs)
		if sample.RemainingPercent != nil {
			addHistoryPoint(metrics, MetricUsageRemainingPercent, *sample.RemainingPercent, sample, attrs)
		}
		if sample.ResetAt != nil {
			addHistoryPoint(metrics, MetricUsageResetUnixSeconds, float64(sample.ResetAt.Unix()), sample, attrs)
			addHistoryPoint(metrics, MetricUsageSecondsToReset, sample.ResetAt.Sub(sample.ObservedAt.UTC()).Seconds(), sample, attrs)
		}
		if sample.WindowSeconds != nil {
			addHistoryPoint(metrics, MetricUsageWindowSeconds, float64(*sample.WindowSeconds), sample, attrs)
		}
	}

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	otlpMetrics := make([]*metricspb.Metric, 0, len(names))
	pointCount := 0
	for _, name := range names {
		points := metrics[name]
		pointCount += len(points)
		otelMetric := &metricspb.Metric{Name: name}
		otelMetric.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: points}}
		otlpMetrics = append(otlpMetrics, otelMetric)
	}

	payload, err := proto.Marshal(&collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				stringKV("service.name", "token-burn"),
				stringKV("service.version", e.serviceVersion),
				stringKV("deployment.environment", "local"),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: "github.com/durandom/token-burn"},
				Metrics: otlpMetrics,
			}},
		}},
	})
	if err != nil {
		return 0, fmt.Errorf("marshal historical OTLP metrics: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("create historical OTLP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("send historical OTLP metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("historical OTLP endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, fmt.Errorf("read historical OTLP response: %w", err)
	}
	if len(body) > 0 {
		var result collectormetricspb.ExportMetricsServiceResponse
		if err := proto.Unmarshal(body, &result); err != nil {
			return 0, fmt.Errorf("decode historical OTLP response: %w", err)
		}
		if partial := result.PartialSuccess; partial != nil && partial.RejectedDataPoints > 0 {
			return 0, fmt.Errorf("historical OTLP endpoint rejected %d data points: %s", partial.RejectedDataPoints, partial.ErrorMessage)
		}
	}
	return pointCount, nil
}

func addHistoryPoint(metrics map[string][]*metricspb.NumberDataPoint, name string, value float64, sample store.Sample, attrs []*commonpb.KeyValue) {
	metrics[name] = append(metrics[name], &metricspb.NumberDataPoint{
		Attributes:   attrs,
		TimeUnixNano: uint64(sample.ObservedAt.UTC().UnixNano()),
		Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: value},
	})
}

func historyAttrs(sample store.Sample) []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		stringKV("provider", sample.Provider),
		stringKV("account_id", sample.AccountID),
		stringKV("window", sample.WindowName),
		stringKV("plan_type", valueOrUnknown(sample.PlanType)),
		stringKV("source", valueOrUnknown(sample.Source)),
	}
}

func stringKV(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func metricsEndpoint(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "http://localhost:4318"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid OTLP endpoint %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported OTLP endpoint scheme %q", u.Scheme)
	}
	if strings.TrimRight(u.Path, "/") != "/v1/metrics" {
		u.Path = strings.TrimRight(u.Path, "/") + "/v1/metrics"
	}
	return u.String(), nil
}
