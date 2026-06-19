package otel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenObserveDashboardReferencesKnownMetrics(t *testing.T) {
	path := filepath.Join("..", "..", "contrib", "openobserve", "token-burn.dashboard.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}

	var dashboard struct {
		Title string `json:"title"`
		Tabs  []struct {
			Panels []struct {
				Title   string `json:"title"`
				Queries []struct {
					Query  string `json:"query"`
					Fields struct {
						Stream string `json:"stream"`
					} `json:"fields"`
				} `json:"queries"`
			} `json:"panels"`
		} `json:"tabs"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatalf("parse dashboard: %v", err)
	}
	if dashboard.Title != "Token Burn" {
		t.Fatalf("dashboard title = %q, want Token Burn", dashboard.Title)
	}

	known := map[string]bool{
		MetricUsageUsedPercent:              true,
		MetricUsageRemainingPercent:         true,
		MetricUsageResetUnixSeconds:         true,
		MetricUsageSecondsToReset:           true,
		MetricUsageWindowSeconds:            true,
		MetricForecastBurnRatePercentPerHr:  true,
		MetricForecastProjectedResetPercent: true,
		MetricForecastEstimated90Unix:       true,
		MetricForecastEstimated100Unix:      true,
		MetricForecastConfidence:            true,
		MetricPollRunsTotal:                 true,
		MetricPollErrorsTotal:               true,
	}

	checked := 0
	for _, tab := range dashboard.Tabs {
		for _, panel := range tab.Panels {
			for _, query := range panel.Queries {
				stream := query.Fields.Stream
				if stream == "" {
					t.Fatalf("panel %q has a query without fields.stream", panel.Title)
				}
				if !known[stream] {
					t.Fatalf("panel %q references unknown metric stream %q", panel.Title, stream)
				}
				if !strings.Contains(query.Query, `"`+stream+`"`) {
					t.Fatalf("panel %q query does not reference fields.stream %q", panel.Title, stream)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("dashboard has no metric queries")
	}
}
