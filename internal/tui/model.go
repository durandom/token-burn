package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/store"
)

const refreshInterval = 60 * time.Second

type Model struct {
	cfg        config.Config
	theme      Theme
	styles     styles
	width      int
	height     int
	staleAfter time.Duration
	lastPoll   time.Time
	lastGood   time.Time
	loading    bool
	samples    []store.Sample
	forecasts  []forecastRow
	statuses   map[string]accountPollStatus
	errors     []string
}

type tickMsg struct{}
type refreshMsg struct {
	samples   []store.Sample
	forecasts []forecastRow
	statuses  map[string]accountPollStatus
	errors    []string
	lastPoll  time.Time
	lastGood  time.Time
}

type accountPollStatus struct {
	run            store.PollRun
	hasRun         bool
	latestSuccess  time.Time
	latestSampleAt time.Time
}

type forecastRow struct {
	Provider string
	Account  string
	Window   string
	Result   forecast.Result
}

func NewModel(cfg config.Config) Model {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = config.DefaultPollInterval
	}
	theme := DefaultTheme()
	return Model{
		cfg:        cfg,
		theme:      theme,
		styles:     newStyles(theme),
		staleAfter: staleSampleThreshold(cfg.PollInterval),
		loading:    true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tickAfter(refreshInterval))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			m.loading = true
			return m, m.refresh()
		}
	case tickMsg:
		m.loading = true
		return m, tea.Batch(m.refresh(), tickAfter(refreshInterval))
	case refreshMsg:
		m.loading = false
		m.samples = msg.samples
		m.forecasts = msg.forecasts
		m.statuses = msg.statuses
		m.errors = msg.errors
		m.lastPoll = msg.lastPoll
		m.lastGood = msg.lastGood
		return m, nil
	}
	return m, nil
}

func (m Model) View() string {
	var b strings.Builder
	st := m.styles

	title := "token-burn"
	if m.loading {
		title += "  refreshing..."
	}
	b.WriteString(st.title.Render(title))
	b.WriteString("  ")
	if !m.lastPoll.IsZero() {
		b.WriteString(st.subtle.Render("last poll " + m.lastPoll.Local().Format("15:04:05")))
	} else {
		b.WriteString(st.subtle.Render("waiting for first poll"))
	}
	if !m.lastGood.IsZero() {
		b.WriteString(st.subtle.Render(" · last success " + m.lastGood.Local().Format("15:04:05")))
	} else {
		b.WriteString(st.subtle.Render(" · no successful refresh yet"))
	}
	b.WriteString("\n")
	b.WriteString(st.subtle.Render("q quit  r refresh  auto-refresh 60s"))
	b.WriteString("\n\n")

	if len(m.errors) > 0 {
		b.WriteString(st.panelBad.Render(st.heading.Render("Errors") + "\n" + strings.Join(m.errors, "\n")))
		b.WriteString("\n\n")
	}

	if len(m.samples) == 0 {
		b.WriteString(st.panel.Render(st.subtle.Render("No samples yet.")))
		return b.String()
	}

	b.WriteString(m.renderUsage())
	if m.width > 0 {
		return lipgloss.NewStyle().MaxWidth(max(40, m.width)).Render(b.String())
	}
	return b.String()
}

func (m Model) renderUsage() string {
	grouped := map[string][]store.Sample{}
	for _, sample := range m.samples {
		key := sample.Provider + "/" + sample.AccountID
		grouped[key] = append(grouped[key], sample)
	}
	forecasts := map[string]forecastRow{}
	for _, row := range m.forecasts {
		forecasts[forecastKey(row.Provider, row.Account, row.Window)] = row
	}
	keys := sortedKeys(grouped)

	var blocks []string
	for _, key := range keys {
		rows := grouped[key]
		sort.Slice(rows, func(i, j int) bool { return rows[i].WindowName < rows[j].WindowName })
		var b strings.Builder
		now := time.Now()
		status := m.statuses[key]
		b.WriteString(renderAccountHeader(m.styles, key, rows, status, now))
		b.WriteString("\n")
		for _, row := range rows {
			forecastRow, ok := forecasts[forecastKey(row.Provider, row.AccountID, row.WindowName)]
			if !ok {
				b.WriteString(renderUsageLine(m.styles, row, nil, now, m.staleAfter))
			} else {
				b.WriteString(renderUsageLine(m.styles, row, &forecastRow, now, m.staleAfter))
			}
			b.WriteString("\n")
		}
		blocks = append(blocks, accountPanelStyle(m.styles, rows, forecasts).Render(strings.TrimRight(b.String(), "\n")))
	}
	return lipgloss.JoinVertical(lipgloss.Left, blocks...)
}

func accountHeader(key string, rows []store.Sample) string {
	for _, row := range rows {
		if strings.TrimSpace(row.PlanType) != "" {
			return key + "  " + displayPlanType(row.PlanType)
		}
	}
	return key
}

func renderAccountHeader(st styles, key string, rows []store.Sample, status accountPollStatus, now time.Time) string {
	header := accountHeader(key, rows)
	health := renderAccountHealth(st, status, now)
	if header == key {
		return st.provider.Render(key) + health
	}
	plan := strings.TrimPrefix(header, key)
	return st.provider.Render(key) + st.subtle.Render(plan) + health
}

func displayPlanType(plan string) string {
	plan = strings.TrimSpace(plan)
	if plan == "" {
		return ""
	}
	return strings.ReplaceAll(plan, "_", " ")
}

func accountPanelStyle(st styles, rows []store.Sample, forecasts map[string]forecastRow) lipgloss.Style {
	level := 0
	for _, row := range rows {
		level = max(level, riskLevel(row.UsedPercent))
		if forecastRow, ok := forecasts[forecastKey(row.Provider, row.AccountID, row.WindowName)]; ok {
			if projected := forecastRow.Result.ProjectedResetPercent; projected != nil {
				level = max(level, riskLevel(*projected))
			}
		}
	}
	switch level {
	case 2:
		return st.panelBad
	case 1:
		return st.panelWarn
	default:
		return st.panelGood
	}
}

func riskLevel(percent float64) int {
	switch {
	case percent >= 100:
		return 2
	case percent >= 70:
		return 1
	default:
		return 0
	}
}

func renderAccountHealth(st styles, status accountPollStatus, now time.Time) string {
	if currentPollFailure(status) {
		return st.bad.Render("  " + pollFailureSummary(status, now))
	}
	if !status.latestSuccess.IsZero() {
		return st.good.Render("  ok " + formatRelativeTime(status.latestSuccess, now))
	}
	if !status.latestSampleAt.IsZero() {
		return st.warn.Render("  sample " + formatRelativeTime(status.latestSampleAt, now))
	}
	return st.subtle.Render("  no poll yet")
}

func pollFailureSummary(status accountPollStatus, now time.Time) string {
	parts := []string{pollFailureReason(status.run)}
	if !status.latestSuccess.IsZero() {
		parts = append(parts, "last success "+formatRelativeTime(status.latestSuccess, now))
	} else if !status.latestSampleAt.IsZero() {
		parts = append(parts, "sample "+formatRelativeTime(status.latestSampleAt, now))
	} else {
		parts = append(parts, "no successful refresh")
	}
	if action := pollFailureAction(status.run); action != "" {
		parts = append(parts, action)
	}
	return strings.Join(parts, " · ")
}

func pollFailureReason(run store.PollRun) string {
	code := strings.TrimSpace(run.ErrorCode)
	message := strings.ToLower(run.ErrorMessage)
	switch code {
	case "auth_expired":
		return "auth expired"
	case "auth_missing":
		return "auth missing"
	case "rate_limited":
		return "rate limited"
	case "transient_http_failure":
		if strings.Contains(message, "no such host") || strings.Contains(message, "bad file descriptor") || strings.Contains(message, "dial tcp") {
			return "network error"
		}
		return "http error"
	case "invalid_response":
		return "invalid response"
	}
	if code != "" {
		return strings.ReplaceAll(code, "_", " ")
	}
	return "poll failed"
}

func pollFailureAction(run store.PollRun) string {
	provider := strings.ToLower(strings.TrimSpace(run.Provider))
	code := strings.TrimSpace(run.ErrorCode)
	switch {
	case provider == "antigravity" && code == "auth_expired":
		return "run: agy models"
	case provider == "copilot" && code == "auth_missing":
		return "check: gh auth status"
	}
	return ""
}

func (m Model) refresh() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTPTimeout)
		defer cancel()
		db, err := store.Open(ctx, cfg.DatabasePath)
		if err != nil {
			return refreshMsg{errors: []string{err.Error()}, lastPoll: time.Now()}
		}
		defer db.Close()

		now := time.Now()
		var errors []string
		pollStatus := map[string]accountPollStatus{}

		var samples []store.Sample
		for _, acct := range cfg.Accounts {
			status, err := latestPollStatus(ctx, db, acct.Provider, acct.ID, now)
			if err != nil {
				errors = append(errors, err.Error())
			} else {
				pollStatus[accountKey(acct.Provider, acct.ID)] = status
			}

			latest, err := db.LatestSamples(ctx, acct.Provider, acct.ID)
			if err != nil {
				errors = append(errors, err.Error())
				continue
			}
			samples = append(samples, latest...)
			if sampleAt := latestSampleTime(latest); !sampleAt.IsZero() {
				key := accountKey(acct.Provider, acct.ID)
				status := pollStatus[key]
				status.latestSampleAt = sampleAt
				if status.latestSuccess.IsZero() {
					status.latestSuccess = sampleAt
				}
				pollStatus[key] = status
			}
		}
		forecasts := buildForecastRows(ctx, db, samples, now)
		return refreshMsg{
			samples:   samples,
			forecasts: forecasts,
			statuses:  pollStatus,
			errors:    errors,
			lastPoll:  latestPollOrSampleTime(pollStatus, samples),
			lastGood:  latestSuccessTime(pollStatus, samples),
		}
	}
}

func latestPollStatus(ctx context.Context, db *store.Store, providerName, accountID string, now time.Time) (accountPollStatus, error) {
	since := now.Add(-7 * 24 * time.Hour)
	runs, err := db.PollRuns(ctx, store.PollRunFilter{
		Provider:  providerName,
		AccountID: accountID,
		Since:     &since,
	})
	if err != nil {
		return accountPollStatus{}, err
	}
	if len(runs) == 0 {
		return accountPollStatus{}, nil
	}
	status := accountPollStatus{run: runs[len(runs)-1], hasRun: true}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Status == "success" {
			status.latestSuccess = runs[i].StartedAt
			break
		}
	}
	return status, nil
}

func pollStatusErrors(statuses map[string]accountPollStatus) []string {
	var keys []string
	for key := range statuses {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var errors []string
	for _, key := range keys {
		status := statuses[key]
		if !currentPollFailure(status) {
			continue
		}
		message := strings.TrimSpace(status.run.ErrorMessage)
		if message == "" {
			message = strings.TrimSpace(status.run.ErrorCode)
		}
		if message == "" {
			message = "poll failed"
		}
		errors = append(errors, fmt.Sprintf("%s latest poll failed: %s", key, message))
	}
	return errors
}

func latestPollOrSampleTime(statuses map[string]accountPollStatus, samples []store.Sample) time.Time {
	latest := latestSampleTime(samples)
	for _, status := range statuses {
		if status.hasRun && status.run.StartedAt.After(latest) {
			latest = status.run.StartedAt
		}
	}
	return latest
}

func formatProjectedPercent(percent float64) string {
	if percent > 999 {
		return ">999%"
	}
	return fmt.Sprintf("%.0f%%", percent)
}

func latestSuccessTime(statuses map[string]accountPollStatus, samples []store.Sample) time.Time {
	latest := latestSampleTime(samples)
	for _, status := range statuses {
		if status.latestSuccess.After(latest) {
			latest = status.latestSuccess
		}
	}
	return latest
}

func latestKnownSuccess(status accountPollStatus) time.Time {
	if status.latestSuccess.After(status.latestSampleAt) {
		return status.latestSuccess
	}
	return status.latestSampleAt
}

func currentPollFailure(status accountPollStatus) bool {
	if !status.hasRun || status.run.Status != "error" {
		return false
	}
	success := latestKnownSuccess(status)
	return success.IsZero() || status.run.StartedAt.After(success)
}

func latestSampleTime(samples []store.Sample) time.Time {
	var latest time.Time
	for _, sample := range samples {
		if sample.ObservedAt.After(latest) {
			latest = sample.ObservedAt
		}
	}
	return latest
}

func buildForecastRows(ctx context.Context, db *store.Store, samples []store.Sample, now time.Time) []forecastRow {
	var out []forecastRow
	since := now.Add(-7 * 24 * time.Hour)
	for _, sample := range samples {
		history, err := db.History(ctx, store.HistoryFilter{
			Provider:   sample.Provider,
			AccountID:  sample.AccountID,
			WindowName: sample.WindowName,
			Since:      &since,
		})
		if err != nil {
			continue
		}
		observations := make([]forecast.Observation, 0, len(history))
		for _, item := range history {
			observations = append(observations, forecast.Observation{
				ObservedAt:  item.ObservedAt,
				UsedPercent: item.UsedPercent,
				ResetAt:     item.ResetAt,
			})
		}
		out = append(out, forecastRow{
			Provider: sample.Provider,
			Account:  sample.AccountID,
			Window:   sample.WindowName,
			Result:   forecast.Calculate(observations, now),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a := out[i].Provider + out[i].Account + out[i].Window
		b := out[j].Provider + out[j].Account + out[j].Window
		return a < b
	})
	return out
}

func tickAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func renderUsageLine(st styles, sample store.Sample, forecastRow *forecastRow, now time.Time, staleAfter time.Duration) string {
	color := st.good
	switch {
	case sample.UsedPercent >= 90:
		color = st.bad
	case sample.UsedPercent >= 70:
		color = st.warn
	}
	reset := ""
	if sample.ResetAt != nil {
		reset = "resets " + formatRelativeTime(*sample.ResetAt, now)
	}
	stale := staleSampleLabel(sample, now, staleAfter)
	expired := resetExpiredLabel(sample, now)
	detail := renderDetail(st, reset, forecastRow, sample, now, stale != "" || expired != "")
	if stale := staleSampleLabel(sample, now, staleAfter); stale != "" {
		if detail == "" {
			detail = st.warn.Render(stale)
		} else {
			detail = st.warn.Render(stale) + st.subtle.Render(" · ") + detail
		}
	}
	if expired != "" {
		if detail == "" {
			detail = st.warn.Render(expired)
		} else {
			detail = detail + st.subtle.Render(" · ") + st.warn.Render(expired)
		}
	}
	name := fmt.Sprintf("%-28s", truncateCell(displayWindowName(sample.WindowName), 28))
	return fmt.Sprintf("  %s %s %s\n  %-28s %s",
		st.heading.Render(name),
		renderBar(st, sample.UsedPercent, projectedResetPercent(forecastRow), color),
		color.Render(fmt.Sprintf("%5.1f%%", sample.UsedPercent)),
		"",
		detail,
	)
}

func staleSampleLabel(sample store.Sample, now time.Time, staleAfter time.Duration) string {
	if sample.ObservedAt.IsZero() {
		return ""
	}
	if now.Sub(sample.ObservedAt) <= staleAfter {
		return ""
	}
	return "stale " + formatRelativeTime(sample.ObservedAt, now)
}

func resetExpiredLabel(sample store.Sample, now time.Time) string {
	if sample.ResetAt == nil || sample.ResetAt.After(now) {
		return ""
	}
	return "reset expired"
}

func staleSampleThreshold(pollInterval time.Duration) time.Duration {
	if pollInterval <= 0 {
		pollInterval = config.DefaultPollInterval
	}
	threshold := 3 * pollInterval
	if threshold < 15*time.Minute {
		return 15 * time.Minute
	}
	return threshold
}

func renderDetail(st styles, reset string, forecastRow *forecastRow, sample store.Sample, now time.Time, suppressForecast bool) string {
	var parts []string
	if reset != "" && !suppressForecast {
		parts = append(parts, st.subtle.Render(reset))
	}
	if suppressForecast {
		return strings.Join(parts, st.subtle.Render(" · "))
	}
	if forecastRow == nil {
		parts = append(parts, st.subtle.Render("need history"))
		return strings.Join(parts, st.subtle.Render(" · "))
	}
	parts = append(parts, renderInlineForecast(st, *forecastRow, sample, now)...)
	return strings.Join(parts, st.subtle.Render(" · "))
}

func accountKey(providerName, accountID string) string {
	return providerName + "/" + accountID
}

func renderBar(st styles, percent float64, projected *float64, color lipgloss.Style) string {
	const width = 24
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int((percent/100)*width + 0.5)
	if filled > width {
		filled = width
	}

	projectedFilled := filled
	forecastStyle := st.subtle
	if projected != nil {
		value := *projected
		if value < percent {
			value = percent
		}
		if value > 100 {
			value = 100
		}
		projectedFilled = int((value/100)*width + 0.5)
		if projectedFilled > width {
			projectedFilled = width
		}
		switch {
		case value >= 100:
			forecastStyle = st.bad
		case value >= 70:
			forecastStyle = st.warn
		default:
			forecastStyle = st.good
		}
	}

	var b strings.Builder
	b.WriteString("[")
	if filled > 0 {
		b.WriteString(color.Render(strings.Repeat("█", filled)))
	}
	if projectedFilled > filled {
		b.WriteString(forecastStyle.Render(strings.Repeat("▒", projectedFilled-filled)))
	}
	if projectedFilled < width {
		b.WriteString(st.barBg.Render(strings.Repeat("─", width-projectedFilled)))
	}
	b.WriteString("]")
	return b.String()
}

func projectedResetPercent(row *forecastRow) *float64 {
	if row == nil {
		return nil
	}
	return row.Result.ProjectedResetPercent
}

func renderInlineForecast(st styles, row forecastRow, sample store.Sample, now time.Time) []string {
	if row.Result.BurnRatePercentPerHour == nil {
		message := ""
		switch row.Result.InsufficientDataReason {
		case "":
			message = "need history"
		case "one_sample":
			message = "need another sample"
		case "no_samples":
			message = "no history"
		case "flat_usage":
			message = "flat usage"
		default:
			message = row.Result.InsufficientDataReason
		}
		return []string{st.subtle.Render(message)}
	}
	rateStyle := forecastValueStyle(st, sample.UsedPercent)
	if row.Result.ProjectedResetPercent != nil {
		rateStyle = forecastValueStyle(st, *row.Result.ProjectedResetPercent)
	}
	parts := []string{rateStyle.Render(fmt.Sprintf("%.1f%%/h", *row.Result.BurnRatePercentPerHour))}
	if row.Result.ProjectedResetPercent != nil {
		projected := *row.Result.ProjectedResetPercent
		parts = append(parts, forecastValueStyle(st, projected).Render("reset ~"+formatProjectedPercent(projected)))
	}
	if row.Result.Estimated100At != nil {
		if sample.ResetAt != nil && row.Result.Estimated100At.After(*sample.ResetAt) {
			parts = append(parts, st.good.Render("reset first"))
		} else {
			parts = append(parts, st.bad.Render("100% "+formatRelativeTime(*row.Result.Estimated100At, now)))
		}
	} else if row.Result.Estimated90At != nil {
		if sample.ResetAt != nil && row.Result.Estimated90At.After(*sample.ResetAt) {
			parts = append(parts, st.good.Render("reset first"))
		} else {
			parts = append(parts, st.warn.Render("90% "+formatRelativeTime(*row.Result.Estimated90At, now)))
		}
	}
	return parts
}

func forecastValueStyle(st styles, percent float64) lipgloss.Style {
	switch {
	case percent >= 100:
		return st.bad
	case percent >= 70:
		return st.warn
	default:
		return st.good
	}
}

func displayWindowName(name string) string {
	if strings.HasPrefix(name, "additional_") {
		switch {
		case strings.HasSuffix(name, "_primary"):
			return "additional primary"
		case strings.HasSuffix(name, "_secondary"):
			return "additional secondary"
		}
	}
	name = strings.TrimPrefix(name, "additional_codex_")
	name = strings.ReplaceAll(name, "_", " ")
	return name
}

func formatRelativeTime(target, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	d := target.Sub(now)
	prefix := "in "
	if d < 0 {
		d = -d
		prefix = ""
	}
	if d < time.Minute {
		if prefix == "" {
			return "now"
		}
		return "in <1m"
	}

	rounded := d.Round(time.Minute)
	days := int(rounded / (24 * time.Hour))
	rounded -= time.Duration(days) * 24 * time.Hour
	hours := int(rounded / time.Hour)
	rounded -= time.Duration(hours) * time.Hour
	minutes := int(rounded / time.Minute)

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 && len(parts) < 2 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if days == 0 && minutes > 0 && len(parts) < 2 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if len(parts) == 0 {
		parts = append(parts, "<1m")
	}
	value := strings.Join(parts, " ")
	if prefix == "" {
		return value + " ago"
	}
	return prefix + value
}

func truncateCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func forecastKey(provider, account, window string) string {
	return provider + "\x00" + account + "\x00" + window
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
