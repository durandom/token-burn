package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id     = "copilot"
	source = "github_copilot"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type Provider struct {
	Runner CommandRunner
	Now    func() time.Time
}

type execRunner struct{}

func New() *Provider {
	return &Provider{}
}

func (p *Provider) ID() string {
	return id
}

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()

	user, err := p.fetchUser(ctx)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	snap := mapUserResponse(user, acct, observedAt)

	if user.TokenBasedBilling && user.Login != "" {
		billing, err := p.fetchAICreditUsage(ctx, user.Login, observedAt)
		if err == nil {
			addAICreditWindow(&snap, user, billing, observedAt)
		} else {
			snap.Raw["ai_credit_usage_error"] = err.Error()
		}
	}

	return snap, nil
}

func (p *Provider) fetchUser(ctx context.Context) (userResponse, error) {
	out, err := p.runner().Run(ctx, "gh", "api", "-H", "Cache-Control: no-cache", "-H", "Pragma: no-cache", "/copilot_internal/user")
	if err != nil {
		return userResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthMissing,
			Provider: id,
			Err:      fmt.Errorf("run gh api /copilot_internal/user: %w", err),
		}
	}
	var user userResponse
	if err := json.Unmarshal(out, &user); err != nil {
		return userResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      err,
		}
	}
	if strings.TrimSpace(user.Login) == "" {
		return userResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      errors.New("copilot user response has no login"),
		}
	}
	return user, nil
}

func (p *Provider) fetchAICreditUsage(ctx context.Context, login string, observedAt time.Time) (billingUsageResponse, error) {
	endpoint := fmt.Sprintf("/users/%s/settings/billing/ai_credit/usage?year=%d&month=%d", login, observedAt.UTC().Year(), int(observedAt.UTC().Month()))
	out, err := p.runner().Run(ctx, "gh", "api", "-H", "Cache-Control: no-cache", "-H", "Pragma: no-cache", endpoint)
	if err != nil {
		return billingUsageResponse{}, fmt.Errorf("run gh api %s: %w", endpoint, err)
	}
	var usage billingUsageResponse
	if err := json.Unmarshal(out, &usage); err != nil {
		return billingUsageResponse{}, fmt.Errorf("parse ai credit usage response: %w", err)
	}
	return usage, nil
}

func mapUserResponse(user userResponse, acct usageprovider.Account, observedAt time.Time) usageprovider.Snapshot {
	plan := firstNonEmpty(user.CopilotPlan, user.AccessTypeSKU)
	snap := usageprovider.Snapshot{
		Provider:   id,
		AccountID:  firstNonEmpty(acct.ID, acct.Alias, acct.ProviderAccountID, user.Login, "default"),
		PlanType:   plan,
		Source:     source,
		ObservedAt: observedAt.UTC(),
		Raw: map[string]any{
			"github_login":         user.Login,
			"access_type_sku":      user.AccessTypeSKU,
			"copilot_plan":         user.CopilotPlan,
			"token_based_billing":  user.TokenBasedBilling,
			"quota_reset_date":     user.QuotaResetDate,
			"quota_reset_date_utc": user.QuotaResetDateUTC,
		},
	}

	resetAt := parseReset(user.QuotaResetDateUTC, user.QuotaResetDate, observedAt)
	for _, name := range stableSnapshotNames(user.QuotaSnapshots) {
		quota := user.QuotaSnapshots[name]
		addQuotaRaw(&snap, name, quota)
		win, ok := windowFromQuota(name, quota, resetAt)
		if !ok {
			continue
		}
		snap.Windows = append(snap.Windows, win)
	}
	return snap
}

func addQuotaRaw(snap *usageprovider.Snapshot, name string, quota quotaSnapshot) {
	prefix := "quota_" + usageprovider.NormalizeWindowName(name) + "_"
	if quota.Entitlement != nil {
		snap.Raw[prefix+"entitlement"] = *quota.Entitlement
	}
	if quota.QuotaRemaining != nil {
		snap.Raw[prefix+"remaining"] = *quota.QuotaRemaining
	} else if quota.Remaining != nil {
		snap.Raw[prefix+"remaining"] = *quota.Remaining
	}
	if quota.PercentRemaining != nil {
		snap.Raw[prefix+"percent_remaining"] = *quota.PercentRemaining
	}
	if quota.Unlimited != nil {
		snap.Raw[prefix+"unlimited"] = *quota.Unlimited
	}
	if quota.TokenBasedBilling {
		snap.Raw[prefix+"token_based_billing"] = quota.TokenBasedBilling
	}
}

func windowFromQuota(name string, quota quotaSnapshot, resetAt *time.Time) (usageprovider.Window, bool) {
	if quota.HasQuota != nil && !*quota.HasQuota {
		return usageprovider.Window{}, false
	}

	remainingPercent := quota.PercentRemaining
	var usedPercent *float64
	if remainingPercent != nil {
		used := 100 - *remainingPercent
		usedPercent = &used
	} else if quota.Entitlement != nil && *quota.Entitlement > 0 {
		remaining := firstFloatPtr(quota.QuotaRemaining, quota.Remaining)
		if remaining != nil {
			used := ((*quota.Entitlement - *remaining) / *quota.Entitlement) * 100
			usedPercent = &used
		}
	}
	if usedPercent == nil && quota.Unlimited != nil && *quota.Unlimited {
		zero := 0.0
		usedPercent = &zero
	}

	return usageprovider.NewWindow(name, usageprovider.WindowOptions{
		UsedPercent:  usedPercent,
		ResetAt:      resetAt,
		LimitReached: quota.OverageCount != nil && *quota.OverageCount > 0,
	})
}

func addAICreditWindow(snap *usageprovider.Snapshot, user userResponse, usage billingUsageResponse, observedAt time.Time) {
	usedCredits := 0.0
	grossAmount := 0.0
	netAmount := 0.0
	models := map[string]bool{}
	for _, item := range usage.UsageItems {
		if !strings.EqualFold(item.UnitType, "ai-credits") {
			continue
		}
		usedCredits += item.GrossQuantity
		grossAmount += item.GrossAmount
		netAmount += item.NetAmount
		if strings.TrimSpace(item.Model) != "" {
			models[item.Model] = true
		}
	}
	snap.Raw["ai_credit_usage_items"] = len(usage.UsageItems)
	snap.Raw["ai_credit_gross_amount_usd"] = grossAmount
	snap.Raw["ai_credit_net_amount_usd"] = netAmount
	snap.Raw["ai_credit_models"] = strings.Join(sortedKeys(models), ",")

	limit := aiCreditAllowance(user)
	if limit <= 0 {
		snap.Raw["ai_credit_used"] = usedCredits
		return
	}
	usedPercent := (usedCredits / limit) * 100
	remainingPercent := 100 - usedPercent
	resetAt := parseReset(user.QuotaResetDateUTC, user.QuotaResetDate, observedAt)
	if resetAt == nil {
		resetAt = firstOfNextMonth(observedAt)
	}
	win, ok := usageprovider.NewWindow("ai_credits", usageprovider.WindowOptions{
		UsedPercent:      &usedPercent,
		RemainingPercent: &remainingPercent,
		ResetAt:          resetAt,
	})
	if ok {
		snap.Windows = append(snap.Windows, win)
	}
}

func aiCreditAllowance(user userResponse) float64 {
	value := strings.ToLower(firstNonEmpty(user.CopilotPlan, user.AccessTypeSKU))
	switch {
	case strings.Contains(value, "max"):
		return 20000
	case strings.Contains(value, "pro_plus"), strings.Contains(value, "pro+"):
		return 7000
	case strings.Contains(value, "pro"):
		return 1500
	case strings.Contains(value, "enterprise"):
		return 3900
	case strings.Contains(value, "business"):
		return 1900
	default:
		return 0
	}
}

func parseReset(values ...any) *time.Time {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				utc := t.UTC()
				return &utc
			}
			if t, err := time.Parse("2006-01-02", v); err == nil {
				utc := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
				return &utc
			}
		case time.Time:
			utc := v.UTC()
			return &utc
		}
	}
	return nil
}

func firstOfNextMonth(t time.Time) *time.Time {
	utc := t.UTC()
	next := time.Date(utc.Year(), utc.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return &next
}

func stableSnapshotNames(in map[string]quotaSnapshot) []string {
	preferred := []string{"ai_credits", "premium_interactions", "chat", "completions"}
	seen := map[string]bool{}
	out := []string{}
	for _, name := range preferred {
		if _, ok := in[name]; ok {
			out = append(out, name)
			seen[name] = true
		}
	}
	var extra []string
	for name := range in {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func sortedKeys(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func firstFloatPtr(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (p *Provider) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return execRunner{}
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, resolveExecutable(name), args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return out, err
	}
	return out, nil
}

func resolveExecutable(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if name != "gh" {
		return name
	}
	for _, candidate := range []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/usr/bin/gh"} {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return name
}
