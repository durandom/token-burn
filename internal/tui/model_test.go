package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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
	model.lastGood = model.lastPoll

	view := model.View()
	for _, want := range []string{"token-burn", "daemon poll 5m", "last success", "copilot/copilot-default  individual max", "five hour", "12.0%", "[", "█", "▒", "10.0%/h", "reset ~50%", "100% in"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestParseLayoutMode(t *testing.T) {
	for _, value := range []string{"auto", "normal", "compact", "ultra", " COMPACT "} {
		if _, err := ParseLayoutMode(value); err != nil {
			t.Fatalf("ParseLayoutMode(%q) error = %v", value, err)
		}
	}
	if _, err := ParseLayoutMode("wide"); err == nil {
		t.Fatal("ParseLayoutMode(wide) error = nil")
	}
}

func TestForcedLayoutModes(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour)
	sample := store.Sample{Provider: "codex", AccountID: "default", WindowName: "five_hour", UsedPercent: 20, ResetAt: &reset}
	tests := []struct {
		name       string
		mode       LayoutMode
		height     int
		wantInline bool
		wantBar    bool
	}{
		{name: "normal stays two lines", mode: LayoutNormal, height: 7, wantBar: true},
		{name: "compact stays one line", mode: LayoutCompact, height: 80, wantInline: true, wantBar: true},
		{name: "ultra omits bar", mode: LayoutUltra, height: 80, wantInline: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := NewModelWithLayout(testConfig(t), tt.mode)
			model.width = 110
			model.height = tt.height
			model.samples = []store.Sample{sample}
			view := model.View()
			for _, line := range strings.Split(view, "\n") {
				if !strings.Contains(line, "five hour") {
					continue
				}
				if got := strings.Contains(line, "resets"); got != tt.wantInline {
					t.Fatalf("inline detail = %t, want %t:\n%s", got, tt.wantInline, view)
				}
				if got := strings.Contains(line, "["); got != tt.wantBar {
					t.Fatalf("bar = %t, want %t:\n%s", got, tt.wantBar, view)
				}
				return
			}
			t.Fatalf("missing usage row:\n%s", view)
		})
	}
}

func TestResponsivePanelsUseTerminalWidth(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 96
	model.height = 80
	model.samples = []store.Sample{
		{Provider: "a", AccountID: "short", WindowName: "weekly", UsedPercent: 10},
		{Provider: "provider-with-long-name", AccountID: "long-account", WindowName: "premium_interactions", UsedPercent: 75},
	}

	view := model.View()
	var borders int
	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, "╭") && !strings.Contains(line, "╰") {
			continue
		}
		borders++
		if got := lipgloss.Width(line); got != model.width {
			t.Fatalf("panel border width = %d, want %d:\n%s", got, model.width, view)
		}
	}
	if borders != 4 {
		t.Fatalf("panel border count = %d, want 4:\n%s", borders, view)
	}
}

func TestViewSwitchesToCompactRowsWhenHeightIsConstrained(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour)
	model := NewModel(testConfig(t))
	model.width = 110
	model.height = 7
	model.samples = []store.Sample{{
		Provider: "codex", AccountID: "default", WindowName: "five_hour",
		UsedPercent: 20, ResetAt: &reset,
	}}

	view := model.View()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "five hour") {
			if !strings.Contains(line, "resets") {
				t.Fatalf("compact row did not keep detail inline:\n%s", view)
			}
			return
		}
	}
	t.Fatalf("missing usage row:\n%s", view)
}

func TestNarrowTerminalUsesUltraCompactRows(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 30
	model.height = 40
	model.samples = []store.Sample{{
		Provider: "copilot", AccountID: "default", WindowName: "premium_interactions", UsedPercent: 75,
	}}
	view := model.View()
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > model.width {
			t.Fatalf("line width = %d, terminal width = %d:\n%s", got, model.width, view)
		}
		if strings.Contains(line, "premium") && strings.Contains(line, "[") {
			t.Fatalf("ultra-compact row should omit bar:\n%s", view)
		}
	}
}

func TestNarrowEmptyStateRespectsTerminalWidth(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 30
	view := model.View()
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > model.width {
			t.Fatalf("line width = %d, terminal width = %d:\n%s", got, model.width, view)
		}
	}
}

func TestResponsiveLayoutExpandsBarsOnWideTerminals(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 80
	narrow := model.usageLayout(false)
	model.width = 160
	wide := model.usageLayout(false)
	if narrow.barWidth >= wide.barWidth {
		t.Fatalf("bar widths narrow=%d wide=%d, want wide larger", narrow.barWidth, wide.barWidth)
	}
	if wide.barWidth > 36 || narrow.barWidth < 12 {
		t.Fatalf("bar widths outside bounds: narrow=%d wide=%d", narrow.barWidth, wide.barWidth)
	}
}

func TestTooShortTerminalShowsResizeHint(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 30
	model.height = 5
	model.samples = []store.Sample{
		{Provider: "codex", AccountID: "default", WindowName: "primary", UsedPercent: 25},
		{Provider: "xai", AccountID: "default", WindowName: "weekly", UsedPercent: 25},
	}
	if view := model.View(); !strings.Contains(view, "resize: need") {
		t.Fatalf("missing resize hint:\n%s", view)
	}
}

func TestTooShortEmptyStateShowsResizeHint(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 30
	model.height = 3
	if view := model.View(); !strings.Contains(view, "resize: need") {
		t.Fatalf("missing empty-state resize hint:\n%s", view)
	}
}

func TestTypicalTerminalFitsWithoutScrolling(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 100
	model.height = 30
	for providerName, windows := range map[string][]string{
		"antigravity": {"claude_and_gpt", "gemini"},
		"claude":      {"five_hour", "seven_day", "seven_day_fable"},
		"codex":       {"additional_primary", "primary", "code_review_primary", "code_review_secondary"},
		"copilot":     {"ai_credits", "chat", "completions", "premium_interactions"},
		"xai":         {"weekly"},
	} {
		for _, window := range windows {
			model.samples = append(model.samples, store.Sample{
				Provider: providerName, AccountID: providerName + "-default",
				WindowName: window, UsedPercent: 25,
			})
		}
	}
	view := model.View()
	if got := lipgloss.Height(view); got > model.height {
		t.Fatalf("view height = %d, terminal height = %d:\n%s", got, model.height, view)
	}
}

func TestAutoLayoutKeepsFullBordersWhenCompactRowsFit(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 127
	model.height = 32
	for providerName, windows := range map[string][]string{
		"antigravity": {"claude_and_gpt", "gemini"},
		"claude":      {"five_hour", "seven_day", "seven_day_fable"},
		"codex":       {"additional_primary", "primary"},
		"copilot":     {"chat", "completions"},
		"xai":         {"weekly"},
	} {
		for _, window := range windows {
			model.samples = append(model.samples, store.Sample{
				Provider: providerName, AccountID: providerName + "-default",
				WindowName: window, UsedPercent: 25,
			})
		}
	}
	view := model.View()
	if got := lipgloss.Height(view); got > model.height {
		t.Fatalf("view height = %d, terminal height = %d:\n%s", got, model.height, view)
	}
	var fullBorders int
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "╭") || strings.Contains(line, "╰") {
			fullBorders++
		}
		if strings.Contains(line, "├") {
			t.Fatalf("expected full borders (room to spare), found a merged divider:\n%s", view)
		}
	}
	if want := 2 * 5; fullBorders != want {
		t.Fatalf("full border lines = %d, want %d (5 panels x top+bottom):\n%s", fullBorders, want, view)
	}
}

func TestCompactUsageIsShorterThanStandardUsage(t *testing.T) {
	model := NewModel(testConfig(t))
	model.width = 100
	model.samples = []store.Sample{
		{Provider: "copilot", AccountID: "default", WindowName: "chat", UsedPercent: 0},
		{Provider: "copilot", AccountID: "default", WindowName: "premium_interactions", UsedPercent: 75},
	}
	standard := model.renderUsage(model.usageLayout(false))
	compact := model.renderUsage(model.usageLayout(true))
	if lipgloss.Height(compact) >= lipgloss.Height(standard) {
		t.Fatalf("compact height=%d standard height=%d", lipgloss.Height(compact), lipgloss.Height(standard))
	}
}

func TestViewRendersGlobalErrors(t *testing.T) {
	model := NewModel(testConfig(t))
	model.errors = []string{"open sqlite store: permission denied"}
	view := model.View()
	for _, want := range []string{"Errors", "open sqlite store: permission denied"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestDaemonPollLabelUsesOnlyLiveState(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	model := NewModel(testConfig(t)) // config default is 5m, timeout is 15s
	grace := model.cfg.HTTPTimeout + refreshInterval
	liveNextPollAt := now.Add(-grace + time.Second)
	staleNextPollAt := now.Add(-grace)
	futureNextPollAt := now.Add(24 * time.Hour)

	tests := []struct {
		name  string
		state *store.DaemonState
		want  string
	}{
		{
			name: "no daemon state",
			want: "daemon poll 5m (config)",
		},
		{
			name:  "state without update timestamp",
			state: &store.DaemonState{PollInterval: 14 * time.Minute},
			want:  "daemon poll 5m (config)",
		},
		{
			name: "live scheduled state",
			state: &store.DaemonState{
				UpdatedAt:    now.Add(-time.Minute),
				PollInterval: 14 * time.Minute,
				NextPollAt:   &liveNextPollAt,
			},
			want: "daemon poll 14m",
		},
		{
			name: "expired scheduled state",
			state: &store.DaemonState{
				UpdatedAt:    now.Add(-time.Minute),
				PollInterval: 14 * time.Minute,
				NextPollAt:   &staleNextPollAt,
			},
			want: "daemon poll 5m (config)",
		},
		{
			name: "live state derived from updated time",
			state: &store.DaemonState{
				UpdatedAt:    now.Add(-14*time.Minute - grace + time.Second),
				PollInterval: 14 * time.Minute,
			},
			want: "daemon poll 14m",
		},
		{
			name: "expired state derived from updated time",
			state: &store.DaemonState{
				UpdatedAt:    now.Add(-14*time.Minute - grace),
				PollInterval: 14 * time.Minute,
			},
			want: "daemon poll 5m (config)",
		},
		{
			name: "future schedule cannot extend expired update",
			state: &store.DaemonState{
				UpdatedAt:    now.Add(-14*time.Minute - grace),
				PollInterval: 14 * time.Minute,
				NextPollAt:   &futureNextPollAt,
			},
			want: "daemon poll 5m (config)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model.daemonState = tt.state
			if got := model.daemonPollLabel(now); got != tt.want {
				t.Fatalf("daemonPollLabel() = %q, want %q", got, tt.want)
			}
		})
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

func TestLatestSuccessTimeUsesSamplesAndSuccessfulPolls(t *testing.T) {
	sampleAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	successAt := sampleAt.Add(time.Hour)
	got := latestSuccessTime(map[string]accountPollStatus{
		"codex/codex-default": {latestSuccess: successAt},
	}, []store.Sample{{ObservedAt: sampleAt}})
	if !got.Equal(successAt) {
		t.Fatalf("latestSuccessTime() = %v, want %v", got, successAt)
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

func TestRenderAccountHealth(t *testing.T) {
	model := NewModel(testConfig(t))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	line := renderAccountHealth(model.styles, accountPollStatus{
		hasRun:        true,
		latestSuccess: now.Add(-2 * time.Hour),
		run: store.PollRun{
			StartedAt:    now.Add(-5 * time.Minute),
			Provider:     "antigravity",
			Status:       "error",
			ErrorCode:    "auth_expired",
			ErrorMessage: "antigravity: auth_expired",
		},
	}, now)
	for _, want := range []string{"auth expired", "last success 2h ago", "run: agy models"} {
		if !strings.Contains(line, want) {
			t.Fatalf("health missing %q: %q", want, line)
		}
	}
	line = renderAccountHealth(model.styles, accountPollStatus{latestSuccess: now.Add(-2 * time.Minute)}, now)
	if !strings.Contains(line, "ok 2m ago") {
		t.Fatalf("health missing ok status: %q", line)
	}
	line = renderAccountHealth(model.styles, accountPollStatus{
		hasRun:        true,
		latestSuccess: now.Add(-2 * time.Minute),
		recentErrors:  1,
		run: store.PollRun{
			StartedAt:    now.Add(-time.Minute),
			Provider:     "claude",
			Status:       "error",
			ErrorCode:    "rate_limited",
			ErrorMessage: "claude: rate_limited: HTTP 429",
		},
	}, now)
	for _, want := range []string{"ok 2m ago", "latest refresh rate limited"} {
		if !strings.Contains(line, want) {
			t.Fatalf("soft rate-limit health missing %q: %q", want, line)
		}
	}
	if strings.Contains(line, "last success") {
		t.Fatalf("soft rate-limit health should not look like a hard failure: %q", line)
	}
	line = renderAccountHealth(model.styles, accountPollStatus{
		hasRun:        true,
		latestSuccess: now.Add(-20 * time.Minute),
		recentErrors:  3,
		run: store.PollRun{
			StartedAt: now.Add(-time.Minute),
			Provider:  "claude",
			Status:    "error",
			ErrorCode: "rate_limited",
		},
	}, now)
	for _, want := range []string{"ok 20m ago", "latest refresh rate limited"} {
		if !strings.Contains(line, want) {
			t.Fatalf("soft repeated rate-limit health missing %q: %q", want, line)
		}
	}
	line = renderAccountHealth(model.styles, accountPollStatus{
		hasRun:        true,
		latestSuccess: now.Add(-2 * time.Hour),
		recentErrors:  3,
		run: store.PollRun{
			StartedAt: now.Add(-time.Minute),
			Provider:  "claude",
			Status:    "error",
			ErrorCode: "rate_limited",
		},
	}, now)
	for _, want := range []string{"rate limited", "last success 2h ago"} {
		if !strings.Contains(line, want) {
			t.Fatalf("hard rate-limit health missing %q: %q", want, line)
		}
	}
	line = renderAccountHealth(model.styles, accountPollStatus{
		hasRun:         true,
		latestSampleAt: now,
		run: store.PollRun{
			StartedAt: now.Add(-5 * time.Minute),
			Status:    "error",
		},
	}, now)
	if strings.Contains(line, "poll failed") {
		t.Fatalf("health should ignore older failure: %q", line)
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
	}, time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC), model.staleAfter)
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
	}, nil, time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC), model.staleAfter)
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
	}, now, model.staleAfter)

	for _, want := range []string{"resets in 1h 30m", "20.0%/h", "reset ~80%", "reset first", "▒"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
}

func TestRenderUsageLineShowsStaleSample(t *testing.T) {
	model := NewModel(testConfig(t))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	burn := 5.0
	projected := 42.0
	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "antigravity",
		AccountID:   "antigravity-default",
		WindowName:  "gemini",
		ObservedAt:  now.Add(-3 * time.Hour),
		UsedPercent: 40.5,
		ResetAt:     &reset,
	}, &forecastRow{
		Provider: "antigravity",
		Account:  "antigravity-default",
		Window:   "gemini",
		Result: forecast.Result{
			SampleCount:            3,
			BurnRatePercentPerHour: &burn,
			ProjectedResetPercent:  &projected,
		},
	}, now, model.staleAfter)
	for _, want := range []string{"stale 3h ago", "reset expired"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
	for _, unwanted := range []string{"5.0%/h", "reset ~42%", "resets 1h ago"} {
		if strings.Contains(line, unwanted) {
			t.Fatalf("stale line should hide %q:\n%s", unwanted, line)
		}
	}
}

func TestRenderUsageLineDoesNotShowNormalPollDelayAsStale(t *testing.T) {
	model := NewModel(testConfig(t))
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	line := renderUsageLine(model.styles, store.Sample{
		Provider:    "claude",
		AccountID:   "claude-default",
		WindowName:  "five_hour",
		ObservedAt:  now.Add(-6 * time.Minute),
		UsedPercent: 6,
	}, nil, now, model.staleAfter)
	if strings.Contains(line, "stale") {
		t.Fatalf("line should not mark normal poll delay stale:\n%s", line)
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
	}, now, model.staleAfter)

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
