package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/store"
)

func TestViewRendersSamples(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour)
	remaining := 88.0
	model := NewModel(testConfig(t))
	model.samples = []store.Sample{{
		Provider:         "copilot",
		AccountID:        "copilot-default",
		WindowName:       "five_hour",
		PlanType:         "individual_max",
		UsedPercent:      12,
		RemainingPercent: &remaining,
		ResetAt:          &reset,
	}}
	burn := 10.0
	estimated100 := reset
	projectedReset := 50.0
	model.forecasts = []forecastRow{{
		Provider: "copilot",
		Account:  "copilot-default",
		Window:   "five_hour",
		Result: forecast.Result{
			SampleCount:            2,
			BurnRatePercentPerHour: &burn,
			ProjectedResetPercent:  &projectedReset,
			Estimated100At:         &estimated100,
		},
	}}
	model.lastPoll = time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)

	view := model.View()
	for _, want := range []string{"token-burn", "copilot/copilot-default  individual max", "five hour", "12.0%", "[", "█", "▒", "10.0%/h", "reset ~50%", "100% in"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestAccountHeaderOmitsEmptyPlan(t *testing.T) {
	got := accountHeader("claude/claude-default", []store.Sample{{PlanType: ""}})
	if got != "claude/claude-default" {
		t.Fatalf("accountHeader() = %q, want provider/account only", got)
	}
}

func TestBuildForecastRows(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/token-burn.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	for i, used := range []float64{10, 20} {
		if err := db.InsertSnapshot(ctx, provider.Snapshot{
			Provider:   "codex",
			AccountID:  "codex-default",
			Source:     "test",
			ObservedAt: t0.Add(time.Duration(i) * time.Hour),
			Windows:    []provider.Window{{Name: "five_hour", UsedPercent: used}},
		}, store.InsertOptions{}); err != nil {
			t.Fatalf("InsertSnapshot() error = %v", err)
		}
	}

	rows := buildForecastRows(ctx, db, []store.Sample{{
		Provider:   "codex",
		AccountID:  "codex-default",
		WindowName: "five_hour",
	}}, t0.Add(time.Hour))
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0].Result.BurnRatePercentPerHour == nil || *rows[0].Result.BurnRatePercentPerHour != 10 {
		t.Fatalf("forecast = %#v, want 10%%/h", rows[0].Result)
	}
}

func TestLatestSampleTime(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	got := latestSampleTime([]store.Sample{
		{ObservedAt: t0},
		{ObservedAt: t0.Add(2 * time.Minute)},
		{ObservedAt: t0.Add(time.Minute)},
	})
	want := t0.Add(2 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("latestSampleTime() = %v, want %v", got, want)
	}
}

func TestLatestPollOrSampleTimeUsesPollErrors(t *testing.T) {
	sampleAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	pollAt := sampleAt.Add(time.Hour)
	got := latestPollOrSampleTime(map[string]accountPollStatus{
		"antigravity/antigravity-default": {
			hasRun: true,
			run: store.PollRun{
				StartedAt: pollAt,
				Provider:  "antigravity",
				AccountID: "antigravity-default",
				Status:    "error",
			},
		},
	}, []store.Sample{{ObservedAt: sampleAt}})
	if !got.Equal(pollAt) {
		t.Fatalf("latestPollOrSampleTime() = %v, want %v", got, pollAt)
	}
}

func TestPollStatusErrorsReportsLatestProviderFailure(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	got := pollStatusErrors(map[string]accountPollStatus{
		"antigravity/antigravity-default": {
			hasRun:         true,
			latestSampleAt: t0.Add(-time.Minute),
			run: store.PollRun{
				StartedAt:    t0,
				Provider:     "antigravity",
				AccountID:    "antigravity-default",
				Status:       "error",
				ErrorCode:    "auth_expired",
				ErrorMessage: "antigravity: auth_expired",
			},
		},
		"codex/codex-default": {
			hasRun: true,
			run: store.PollRun{
				Provider:  "codex",
				AccountID: "codex-default",
				Status:    "success",
			},
		},
	})
	if len(got) != 1 || !strings.Contains(got[0], "antigravity/antigravity-default latest poll failed: antigravity: auth_expired") {
		t.Fatalf("pollStatusErrors() = %#v", got)
	}
}

func TestPollStatusErrorsIgnoresOlderFailureThanSample(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	got := pollStatusErrors(map[string]accountPollStatus{
		"antigravity/antigravity-default": {
			hasRun:         true,
			latestSampleAt: t0.Add(time.Minute),
			run: store.PollRun{
				StartedAt:    t0,
				Provider:     "antigravity",
				AccountID:    "antigravity-default",
				Status:       "error",
				ErrorMessage: "antigravity: auth_expired",
			},
		},
	})
	if len(got) != 0 {
		t.Fatalf("pollStatusErrors() = %#v, want no current errors", got)
	}
}

func TestThemeIsAvailable(t *testing.T) {
	theme := DefaultTheme()
	if theme.Name != "Bluloco Dark" || theme.Accent != "#3476ff" {
		t.Fatalf("theme = %#v, want Bluloco Dark default", theme)
	}
}

func TestBuiltInThemesIncludeBlulocoDarkAndLight(t *testing.T) {
	themes := BuiltInThemes()
	found := map[string]bool{}
	for _, theme := range themes {
		found[theme.Name] = true
	}
	for _, name := range []string{"Bluloco Dark", "Bluloco Light"} {
		if !found[name] {
			t.Fatalf("built-in themes = %#v, missing %s", themes, name)
		}
	}
}

func TestRenderUsageLineIncludesInlineForecastReason(t *testing.T) {
	model := NewModel(testConfig(t))
	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "claude",
		AccountID:   "claude-default",
		WindowName:  "five_hour",
		UsedPercent: 12,
	}, &forecastRow{
		Provider: "claude",
		Account:  "claude-default",
		Window:   "five_hour",
		Result: forecast.Result{
			SampleCount:            1,
			InsufficientDataReason: "one_sample",
		},
	}, time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC))
	for _, want := range []string{"five hour", "12.0%", "need another sample", "█"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
}

func TestRenderUsageLineTruncatesLongAdditionalNames(t *testing.T) {
	model := NewModel(testConfig(t))
	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "codex",
		AccountID:   "codex-default",
		WindowName:  "additional_codex_bengalfox_secondary",
		UsedPercent: 0,
	}, nil, time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC))
	if strings.Contains(line, "additional_codex") {
		t.Fatalf("line should hide internal prefix:\n%s", line)
	}
	if strings.Contains(line, "bengalfox") {
		t.Fatalf("line should hide internal feature name:\n%s", line)
	}
	if !strings.Contains(line, "additional secondary") {
		t.Fatalf("line missing readable feature name:\n%s", line)
	}
	if !strings.Contains(line, "[") || !strings.Contains(line, "]") {
		t.Fatalf("line missing bar delimiters:\n%s", line)
	}
}

func TestRenderUsageLineShowsRelativeResetAndResetFirst(t *testing.T) {
	model := NewModel(testConfig(t))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Minute)
	estimated100 := now.Add(3 * time.Hour)
	burn := 20.0
	projectedReset := 80.0

	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "codex",
		AccountID:   "codex-default",
		WindowName:  "five_hour",
		UsedPercent: 50,
		ResetAt:     &reset,
	}, &forecastRow{
		Provider: "codex",
		Account:  "codex-default",
		Window:   "five_hour",
		Result: forecast.Result{
			SampleCount:            2,
			BurnRatePercentPerHour: &burn,
			ProjectedResetPercent:  &projectedReset,
			Estimated100At:         &estimated100,
		},
	}, now)

	for _, want := range []string{"resets in 1h 30m", "20.0%/h", "reset ~80%", "reset first", "▒"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
}

func TestRenderUsageLineShowsStaleSample(t *testing.T) {
	model := NewModel(testConfig(t))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "antigravity",
		AccountID:   "antigravity-default",
		WindowName:  "gemini",
		ObservedAt:  now.Add(-3 * time.Hour),
		UsedPercent: 40.5,
	}, nil, now)
	if !strings.Contains(line, "stale 3h ago") {
		t.Fatalf("line missing stale marker:\n%s", line)
	}
}

func TestRenderUsageLineShowsProjectedResetOvershoot(t *testing.T) {
	model := NewModel(testConfig(t))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	burn := 45.0
	projectedReset := 127.0
	estimated100 := now.Add(time.Hour)

	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "copilot",
		AccountID:   "copilot-default",
		WindowName:  "ai_credits",
		UsedPercent: 37.2,
		ResetAt:     &reset,
	}, &forecastRow{
		Provider: "copilot",
		Account:  "copilot-default",
		Window:   "ai_credits",
		Result: forecast.Result{
			SampleCount:            2,
			BurnRatePercentPerHour: &burn,
			ProjectedResetPercent:  &projectedReset,
			Estimated100At:         &estimated100,
		},
	}, now)

	for _, want := range []string{"ai credits", "37.2%", "45.0%/h", "reset ~127%", "100% in 1h"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		target time.Time
		want   string
	}{
		{name: "minutes", target: now.Add(45 * time.Minute), want: "in 45m"},
		{name: "hours", target: now.Add(2*time.Hour + 10*time.Minute), want: "in 2h 10m"},
		{name: "days", target: now.Add(6*24*time.Hour + 3*time.Hour), want: "in 6d 3h"},
		{name: "past", target: now.Add(-10 * time.Minute), want: "10m ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatRelativeTime(tt.target, now); got != tt.want {
				t.Fatalf("formatRelativeTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		DatabasePath: t.TempDir() + "/token-burn.db",
		HTTPTimeout:  15 * time.Second,
		Accounts:     []config.Account{{Provider: "codex", ID: "codex-default"}},
	}
}
