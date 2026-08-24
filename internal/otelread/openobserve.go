package otelread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/otel"
	"github.com/durandom/token-burn/internal/store"
)

type Forecast struct {
	Provider string
	Account  string
	Window   string
	Result   forecast.Result
}

type Snapshot struct {
	Samples   []store.Sample
	Forecasts []Forecast
}

type Client struct {
	Config     config.OTelReadConfig
	HTTPClient *http.Client
	Username   string
	Password   string
}

func NewClient(cfg config.OTelReadConfig) Client {
	return Client{Config: cfg, Username: os.Getenv(cfg.UsernameEnv), Password: os.Getenv(cfg.PasswordEnv)}
}

type metricPoint struct {
	Timestamp int64
	Provider  string
	Account   string
	Window    string
	PlanType  string
	Source    string
	Value     float64
}

type searchHit struct {
	Timestamp json.Number `json:"_timestamp"`
	Provider  string      `json:"provider"`
	Account   string      `json:"account_id"`
	Window    string      `json:"window"`
	PlanType  string      `json:"plan_type"`
	Source    string      `json:"source"`
	Value     json.Number `json:"value"`
}

type searchResponse struct {
	Hits    []searchHit `json:"hits"`
	Code    int         `json:"code"`
	Message string      `json:"message"`
}

var metricNames = []string{
	otel.MetricUsageUsedPercent,
	otel.MetricUsageRemainingPercent,
	otel.MetricUsageResetUnixSeconds,
	otel.MetricUsageWindowSeconds,
	otel.MetricForecastBurnRatePercentPerHr,
	otel.MetricForecastProjectedResetPercent,
	otel.MetricForecastEstimated90Unix,
	otel.MetricForecastEstimated100Unix,
	otel.MetricForecastConfidence,
}

func (c Client) Fetch(ctx context.Context, now time.Time) (Snapshot, error) {
	if strings.TrimSpace(c.Config.Endpoint) == "" {
		return Snapshot{}, errors.New("otel.read.endpoint is required")
	}
	if strings.TrimSpace(c.Config.Organization) == "" {
		return Snapshot{}, errors.New("otel.read.organization is required")
	}
	if c.Config.Lookback <= 0 {
		c.Config.Lookback = 24 * time.Hour
	}
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}

	type result struct {
		name   string
		points []metricPoint
		err    error
	}
	results := make(chan result, len(metricNames))
	var wg sync.WaitGroup
	for _, name := range metricNames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			points, err := c.queryMetric(ctx, name, now.Add(-c.Config.Lookback), now)
			results <- result{name: name, points: points, err: err}
		}()
	}
	wg.Wait()
	close(results)

	byMetric := map[string][]metricPoint{}
	for result := range results {
		if result.err != nil {
			return Snapshot{}, result.err
		}
		byMetric[result.name] = latestPoints(result.points)
	}
	return assemble(byMetric), nil
}

func (c Client) queryMetric(ctx context.Context, metric string, start, end time.Time) ([]metricPoint, error) {
	base, err := url.Parse(strings.TrimRight(c.Config.Endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse otel.read.endpoint: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/" + url.PathEscape(c.Config.Organization) + "/_search"
	base.RawQuery = "type=metrics"
	body := map[string]any{"query": map[string]any{
		"sql":        fmt.Sprintf(`SELECT _timestamp, provider, account_id, window, plan_type, source, value FROM %q WHERE provider <> 'test' ORDER BY _timestamp DESC`, metric),
		"start_time": start.UTC().UnixMicro(), "end_time": end.UTC().UnixMicro(), "from": 0, "size": 10000,
	}}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Username != "" || c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query OpenObserve metric %s: %w", metric, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read OpenObserve response: %w", err)
	}
	var decoded searchResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode OpenObserve response for %s: %w", metric, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || decoded.Code != 0 {
		if decoded.Code == 20003 {
			return nil, nil
		}
		message := strings.TrimSpace(decoded.Message)
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("query OpenObserve metric %s: %s", metric, message)
	}
	points := make([]metricPoint, 0, len(decoded.Hits))
	for _, hit := range decoded.Hits {
		timestamp, err := hit.Timestamp.Int64()
		if err != nil {
			continue
		}
		value, err := hit.Value.Float64()
		if err != nil || hit.Provider == "" || hit.Account == "" || hit.Window == "" {
			continue
		}
		points = append(points, metricPoint{Timestamp: timestamp, Provider: hit.Provider, Account: hit.Account, Window: hit.Window, PlanType: unknownToEmpty(hit.PlanType), Source: unknownToEmpty(hit.Source), Value: value})
	}
	return points, nil
}

func latestPoints(points []metricPoint) []metricPoint {
	latest := map[string]metricPoint{}
	for _, point := range points {
		key := point.Provider + "\x00" + point.Account + "\x00" + point.Window
		if current, ok := latest[key]; !ok || point.Timestamp > current.Timestamp {
			latest[key] = point
		}
	}
	out := make([]metricPoint, 0, len(latest))
	for _, point := range latest {
		out = append(out, point)
	}
	return out
}

func assemble(metrics map[string][]metricPoint) Snapshot {
	type values struct {
		used                                                   metricPoint
		remaining, reset, window                               *metricPoint
		burn, projected, estimated90, estimated100, confidence *metricPoint
	}
	rows := map[string]*values{}
	for name, points := range metrics {
		for _, point := range points {
			key := point.Provider + "\x00" + point.Account + "\x00" + point.Window
			row := rows[key]
			if row == nil {
				row = &values{}
				rows[key] = row
			}
			value := point
			switch name {
			case otel.MetricUsageUsedPercent:
				row.used = point
			case otel.MetricUsageRemainingPercent:
				row.remaining = &value
			case otel.MetricUsageResetUnixSeconds:
				row.reset = &value
			case otel.MetricUsageWindowSeconds:
				row.window = &value
			case otel.MetricForecastBurnRatePercentPerHr:
				row.burn = &value
			case otel.MetricForecastProjectedResetPercent:
				row.projected = &value
			case otel.MetricForecastEstimated90Unix:
				row.estimated90 = &value
			case otel.MetricForecastEstimated100Unix:
				row.estimated100 = &value
			case otel.MetricForecastConfidence:
				row.confidence = &value
			}
		}
	}
	var out Snapshot
	for _, row := range rows {
		if row.used.Provider == "" {
			continue
		}
		observed := time.UnixMicro(row.used.Timestamp).UTC()
		sample := store.Sample{ObservedAt: observed, Provider: row.used.Provider, AccountID: row.used.Account, PlanType: row.used.PlanType, WindowName: row.used.Window, UsedPercent: row.used.Value, Source: row.used.Source, LimitReached: row.used.Value >= 100}
		if fresh(row.used, row.remaining) {
			value := row.remaining.Value
			sample.RemainingPercent = &value
		}
		if fresh(row.used, row.reset) {
			value := time.Unix(int64(row.reset.Value), 0).UTC()
			sample.ResetAt = &value
		}
		if fresh(row.used, row.window) {
			value := int(row.window.Value)
			sample.WindowSeconds = &value
		}
		out.Samples = append(out.Samples, sample)
		result := forecast.Result{ComputedAt: observed, SampleCount: 2, Method: forecast.MethodLinearRegression}
		if fresh(row.used, row.burn) {
			value := row.burn.Value
			result.BurnRatePercentPerHour = &value
		}
		if fresh(row.used, row.projected) {
			value := row.projected.Value
			result.ProjectedResetPercent = &value
		}
		if fresh(row.used, row.confidence) {
			result.Confidence = row.confidence.Value
		}
		if fresh(row.used, row.estimated90) {
			value := time.Unix(int64(row.estimated90.Value), 0).UTC()
			result.Estimated90At = &value
		}
		if fresh(row.used, row.estimated100) {
			value := time.Unix(int64(row.estimated100.Value), 0).UTC()
			result.Estimated100At = &value
		}
		out.Forecasts = append(out.Forecasts, Forecast{Provider: sample.Provider, Account: sample.AccountID, Window: sample.WindowName, Result: result})
	}
	sort.Slice(out.Samples, func(i, j int) bool {
		return out.Samples[i].Provider+out.Samples[i].AccountID+out.Samples[i].WindowName < out.Samples[j].Provider+out.Samples[j].AccountID+out.Samples[j].WindowName
	})
	sort.Slice(out.Forecasts, func(i, j int) bool {
		return out.Forecasts[i].Provider+out.Forecasts[i].Account+out.Forecasts[i].Window < out.Forecasts[j].Provider+out.Forecasts[j].Account+out.Forecasts[j].Window
	})
	return out
}

func fresh(used metricPoint, value *metricPoint) bool {
	if value == nil {
		return false
	}
	delta := time.Duration(used.Timestamp-value.Timestamp) * time.Microsecond
	if delta < 0 {
		delta = -delta
	}
	return delta <= 2*time.Minute
}

func unknownToEmpty(value string) string {
	if value == "unknown" {
		return ""
	}
	return value
}
