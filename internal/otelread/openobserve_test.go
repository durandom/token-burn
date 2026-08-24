package otelread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/otel"
)

func TestQueryMetricUsesMetricsSearchAndBasicAuth(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/default/_search" || r.URL.Query().Get("type") != "metrics" {
			t.Errorf("request URL = %s, want metrics search", r.URL.String())
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "reader" || password != "secret" {
			t.Errorf("basic auth = %q/%q/%t", username, password, ok)
		}
		var request struct {
			Query struct {
				SQL       string `json:"sql"`
				StartTime int64  `json:"start_time"`
				EndTime   int64  `json:"end_time"`
			} `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Query.SQL, `"token_burn_usage_used_percent"`) {
			t.Errorf("SQL = %q", request.Query.SQL)
		}
		if request.Query.StartTime != now.Add(-time.Hour).UnixMicro() || request.Query.EndTime != now.UnixMicro() {
			t.Errorf("query range = %d..%d", request.Query.StartTime, request.Query.EndTime)
		}
		_, _ = w.Write([]byte(`{"hits":[{"_timestamp":1787572800000000,"provider":"claude","account_id":"claude-default","window":"seven_day","plan_type":"unknown","source":"anthropic_oauth_usage","value":61}]}`))
	}))
	defer server.Close()

	client := Client{Config: config.OTelReadConfig{Endpoint: server.URL, Organization: "default"}, HTTPClient: server.Client(), Username: "reader", Password: "secret"}
	points, err := client.queryMetric(context.Background(), otel.MetricUsageUsedPercent, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("queryMetric() error = %v", err)
	}
	if len(points) != 1 || points[0].Value != 61 || points[0].PlanType != "" {
		t.Fatalf("points = %#v", points)
	}
}

func TestNewClientPrefersConfiguredCredentials(t *testing.T) {
	t.Setenv("TEST_O2_USER", "environment-user")
	t.Setenv("TEST_O2_PASSWORD", "environment-password")
	client := NewClient(config.OTelReadConfig{
		Username: "configured-user", Password: "configured-password",
		UsernameEnv: "TEST_O2_USER", PasswordEnv: "TEST_O2_PASSWORD",
	})
	if client.Username != "configured-user" || client.Password != "configured-password" {
		t.Fatalf("credentials = %q/%q", client.Username, client.Password)
	}
}

func TestAssembleBuildsSamplesAndForecasts(t *testing.T) {
	keyPoint := metricPoint{Timestamp: 1787572800000000, Provider: "claude", Account: "claude-default", Window: "seven_day", PlanType: "max", Source: "anthropic", Value: 61}
	withValue := func(value float64) metricPoint { point := keyPoint; point.Value = value; return point }
	result := assemble(map[string][]metricPoint{
		otel.MetricUsageUsedPercent:              {keyPoint},
		otel.MetricUsageRemainingPercent:         {withValue(39)},
		otel.MetricUsageResetUnixSeconds:         {withValue(1787875200)},
		otel.MetricUsageWindowSeconds:            {withValue(604800)},
		otel.MetricForecastBurnRatePercentPerHr:  {withValue(0.8)},
		otel.MetricForecastProjectedResetPercent: {withValue(128)},
	}, true)
	if len(result.Samples) != 1 || len(result.Forecasts) != 1 {
		t.Fatalf("result = %#v", result)
	}
	sample := result.Samples[0]
	if sample.UsedPercent != 61 || sample.RemainingPercent == nil || *sample.RemainingPercent != 39 || sample.WindowSeconds == nil || *sample.WindowSeconds != 604800 || sample.ResetAt == nil {
		t.Fatalf("sample = %#v", sample)
	}
	forecast := result.Forecasts[0].Result
	if forecast.BurnRatePercentPerHour == nil || *forecast.BurnRatePercentPerHour != 0.8 || forecast.ProjectedResetPercent == nil || *forecast.ProjectedResetPercent != 128 {
		t.Fatalf("forecast = %#v", forecast)
	}
}

func TestAssembleDoesNotCombineStaleForecast(t *testing.T) {
	used := metricPoint{Timestamp: 1787572800000000, Provider: "claude", Account: "claude-default", Window: "seven_day", Value: 4}
	oldForecast := used
	oldForecast.Timestamp -= int64((3 * time.Minute) / time.Microsecond)
	oldForecast.Value = 99

	result := assemble(map[string][]metricPoint{
		otel.MetricUsageUsedPercent:              {used},
		otel.MetricForecastProjectedResetPercent: {oldForecast},
	}, true)
	if got := result.Forecasts[0].Result.ProjectedResetPercent; got != nil {
		t.Fatalf("ProjectedResetPercent = %v, want nil for stale metric", *got)
	}
}

func TestAssembleHistoryKeepsExportTimestampsSeparate(t *testing.T) {
	first := metricPoint{Timestamp: 1787572800000000, Provider: "claude", Account: "claude-default", Window: "seven_day", Value: 40}
	second := first
	second.Timestamp += int64(time.Minute / time.Microsecond)
	second.Value = 42
	result := assemble(map[string][]metricPoint{otel.MetricUsageUsedPercent: {second, first}}, false)
	if len(result.Samples) != 2 || result.Samples[0].UsedPercent != 40 || result.Samples[1].UsedPercent != 42 {
		t.Fatalf("history samples = %#v", result.Samples)
	}
}
