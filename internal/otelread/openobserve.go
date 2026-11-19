package otelread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/otel"
	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/store"
)

type Forecast struct {
	Provider string
	Account  string
	Window   string
	Result   forecast.Result
}

type Account struct {
	Provider string
	ID       string
}

type Snapshot struct {
	Resets    map[Account]*forecast.ResetCapacity
	Credits   map[Account]*provider.OverageCredits
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
	username, password := cfg.Username, cfg.Password
	if username == "" {
		username = os.Getenv(cfg.UsernameEnv)
	}
	if password == "" {
		password = os.Getenv(cfg.PasswordEnv)
	}
	return Client{Config: cfg, Username: username, Password: password}
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

var creditMetricNames = []string{
	otel.MetricOverageCreditsBalance,
	otel.MetricOverageCreditsHasCredits,
	otel.MetricOverageCreditsUnlimited,
	otel.MetricOverageLimitReached,
}

var resetMetricNames = []string{otel.MetricResetsAvailable, otel.MetricResetsSecondsToNextExpiry, otel.MetricResetsLastUsedUnixSeconds}

var metricNames = append(append([]string{
	otel.MetricUsageUsedPercent,
	otel.MetricUsageRemainingPercent,
	otel.MetricUsageResetUnixSeconds,
	otel.MetricUsageWindowSeconds,
	otel.MetricForecastBurnRatePercentPerHr,
	otel.MetricForecastProjectedResetPercent,
	otel.MetricForecastEstimated90Unix,
	otel.MetricForecastEstimated100Unix,
	otel.MetricForecastConfidence,
}, creditMetricNames...), resetMetricNames...)

func (c Client) Fetch(ctx context.Context, now time.Time) (Snapshot, error) {
	lookback := c.Config.Lookback
	if lookback <= 0 {
		lookback = 24 * time.Hour
	}
	return c.fetch(ctx, now.Add(-lookback), now, true)
}

// FetchHistory returns usage samples from OpenObserve rather than only the
// latest sample per provider/account/window.
func (c Client) FetchHistory(ctx context.Context, start, end time.Time, filter store.HistoryFilter) ([]store.Sample, error) {
	snapshot, err := c.fetch(ctx, start, end, false)
	if err != nil {
		return nil, err
	}
	samples := make([]store.Sample, 0, len(snapshot.Samples))
	for _, sample := range snapshot.Samples {
		if filter.Provider != "" && sample.Provider != filter.Provider {
			continue
		}
		if filter.AccountID != "" && sample.AccountID != filter.AccountID {
			continue
		}
		if filter.WindowName != "" && sample.WindowName != filter.WindowName {
			continue
		}
		samples = append(samples, sample)
		if filter.Limit > 0 && len(samples) >= filter.Limit {
			break
		}
	}
	return samples, nil
}

func (c Client) fetch(ctx context.Context, start, now time.Time, latestOnly bool) (Snapshot, error) {
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
		if !latestOnly && isAccountMetric(name) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			points, err := c.queryMetric(ctx, name, start, now)
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
		if latestOnly {
			result.points = latestPoints(result.points)
		}
		byMetric[result.name] = result.points
	}
	out := assemble(byMetric, latestOnly)
	if latestOnly {
		out.Credits = assembleCredits(byMetric, now)
		out.Resets = assembleResets(byMetric, now)
	}
	return out, nil
}

func (c Client) queryMetric(ctx context.Context, metric string, start, end time.Time) ([]metricPoint, error) {
	base, err := url.Parse(strings.TrimRight(c.Config.Endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse otel.read.endpoint: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/" + url.PathEscape(c.Config.Organization) + "/_search"
	base.RawQuery = "type=metrics"
	columns := "_timestamp, provider, account_id, window, plan_type, source, value"
	accountMetric := isAccountMetric(metric)
	if accountMetric {
		columns = "_timestamp, provider, account_id, value"
	}
	body := map[string]any{"query": map[string]any{
		"sql":        fmt.Sprintf(`SELECT %s FROM %q WHERE provider <> 'test' ORDER BY _timestamp DESC`, columns, metric),
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
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || hit.Provider == "" || hit.Account == "" || (!accountMetric && hit.Window == "") || timestamp < start.UnixMicro() || timestamp > end.UnixMicro() {
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

func assemble(metrics map[string][]metricPoint, latestOnly bool) Snapshot {
	type values struct {
		used                                                   metricPoint
		remaining, reset, window                               *metricPoint
		burn, projected, estimated90, estimated100, confidence *metricPoint
	}
	rows := map[string]*values{}
	for name, points := range metrics {
		if isAccountMetric(name) {
			continue
		}
		for _, point := range points {
			key := point.Provider + "\x00" + point.Account + "\x00" + point.Window
			if !latestOnly {
				key += fmt.Sprintf("\x00%d", point.Timestamp)
			}
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
		if !out.Samples[i].ObservedAt.Equal(out.Samples[j].ObservedAt) {
			return out.Samples[i].ObservedAt.Before(out.Samples[j].ObservedAt)
		}
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

func isAccountMetric(name string) bool {
	for _, metric := range creditMetricNames {
		if name == metric {
			return true
		}
	}
	for _, metric := range resetMetricNames {
		if name == metric {
			return true
		}
	}
	return false
}

// Credits are account-level gauges. Require a complete flag cohort from one
// export (allowing collection skew), and never carry an omitted balance
// forward from an older export.
func assembleCredits(metrics map[string][]metricPoint, now time.Time) map[Account]*provider.OverageCredits {
	byAccount := map[Account]map[string]metricPoint{}
	for _, name := range creditMetricNames {
		for _, point := range metrics[name] {
			account := Account{Provider: point.Provider, ID: point.Account}
			if byAccount[account] == nil {
				byAccount[account] = map[string]metricPoint{}
			}
			current, ok := byAccount[account][name]
			if !ok || point.Timestamp > current.Timestamp {
				byAccount[account][name] = point
			}
		}
	}
	out := map[Account]*provider.OverageCredits{}
	for account, points := range byAccount {
		has, ok := points[otel.MetricOverageCreditsHasCredits]
		unlimited, uok := points[otel.MetricOverageCreditsUnlimited]
		reached, rok := points[otel.MetricOverageLimitReached]
		// The SDK collects instruments separately, so their timestamps can differ.
		oldest := min(has.Timestamp, unlimited.Timestamp, reached.Timestamp)
		newest := max(has.Timestamp, unlimited.Timestamp, reached.Timestamp)
		observed := time.UnixMicro(oldest).UTC()
		if !ok || !uok || !rok || newest-oldest > int64(2*time.Second/time.Microsecond) || now.Sub(observed) > 2*time.Minute || time.UnixMicro(newest).After(now) {
			continue
		}
		if (has.Value != 0 && has.Value != 1) || (unlimited.Value != 0 && unlimited.Value != 1) || (reached.Value != 0 && reached.Value != 1) {
			continue
		}
		credits := &provider.OverageCredits{HasCredits: has.Value == 1, Unlimited: unlimited.Value == 1, OverageLimitReached: reached.Value == 1, ObservedAt: &observed}
		if balance, ok := points[otel.MetricOverageCreditsBalance]; ok && balance.Timestamp >= newest-int64(2*time.Second/time.Microsecond) && balance.Timestamp <= oldest+int64(2*time.Second/time.Microsecond) && now.Sub(time.UnixMicro(balance.Timestamp)) <= 2*time.Minute {
			credits.Balance = strconv.FormatFloat(balance.Value, 'f', -1, 64)
		}
		out[account] = credits
	}
	return out
}

// Reset gauges are joined independently of usage and credit observations. The
// count anchors the cohort; optional gauges from older exports stay unknown.
func assembleResets(metrics map[string][]metricPoint, now time.Time) map[Account]*forecast.ResetCapacity {
	out := map[Account]*forecast.ResetCapacity{}
	optional := map[Account]map[string]metricPoint{}
	for _, name := range resetMetricNames[1:] {
		for _, p := range metrics[name] {
			a := Account{Provider: p.Provider, ID: p.Account}
			if optional[a] == nil {
				optional[a] = map[string]metricPoint{}
			}
			if old, ok := optional[a][name]; !ok || p.Timestamp > old.Timestamp {
				optional[a][name] = p
			}
		}
	}
	for _, p := range metrics[otel.MetricResetsAvailable] {
		observed := time.UnixMicro(p.Timestamp).UTC()
		if observed.After(now) || now.Sub(observed) > 2*time.Minute || p.Value < 0 || p.Value != math.Trunc(p.Value) || p.Value >= float64(math.MaxInt) {
			continue
		}
		a := Account{Provider: p.Provider, ID: p.Account}
		count := int(p.Value)
		c := &forecast.ResetCapacity{OwnedResets: &count, ObservedAt: &observed}
		for name, v := range optional[a] {
			at := time.UnixMicro(v.Timestamp).UTC()
			skew := at.Sub(observed)
			if skew < -2*time.Second || skew > 2*time.Second || at.After(now) || now.Sub(at) > 2*time.Minute {
				continue
			}
			switch name {
			case otel.MetricResetsSecondsToNextExpiry:
				if count > 0 && v.Value > 0 && v.Value < float64(math.MaxInt64)/float64(time.Second) {
					expiry := at.Add(time.Duration(v.Value * float64(time.Second)))
					c.NextExpiry = &expiry
				}
			case otel.MetricResetsLastUsedUnixSeconds:
				if v.Value > 0 && v.Value <= float64(now.Unix()) {
					used := time.Unix(int64(v.Value), 0).UTC()
					c.LastUsed = &used
				}
			}
		}
		out[a] = c
	}
	return out
}
