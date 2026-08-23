package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/piauth"
	usageprovider "github.com/durandom/token-burn/internal/provider"
)

var (
	errPiCopilotCredentialMissing     = errors.New("Pi GitHub Copilot OAuth credential is missing")
	errPiCopilotEnterpriseUnsupported = errors.New("Pi GitHub Enterprise Copilot credentials require the GitHub CLI")
)

const (
	id               = "copilot"
	source           = "github_copilot"
	piProviderID     = "github-copilot"
	defaultGitHubURL = "https://api.github.com"
	maxResponseBytes = 1 << 20
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type Provider struct {
	Runner     CommandRunner
	HTTPClient *http.Client
	BaseURL    string
	Now        func() time.Time
	HomeDir    func() (string, error)
	Env        func(string) string
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
	user, piToken, err := p.fetchUser(ctx, acct)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	snap := mapUserResponse(user, acct, observedAt)
	if piToken != "" {
		snap.Raw["auth_source"] = "pi"
	}
	if user.TokenBasedBilling && user.Login != "" {
		var billing billingUsageResponse
		if piToken != "" {
			billing, err = p.fetchAICreditUsageHTTP(ctx, piToken, user.Login, observedAt)
		} else {
			billing, err = p.fetchAICreditUsageGH(ctx, user.Login, observedAt)
		}
		if err == nil {
			addAICreditWindow(&snap, user, billing, observedAt)
		} else {
			snap.Raw["ai_credit_usage_error"] = err.Error()
		}
	}
	return snap, nil
}

func (p *Provider) fetchUser(ctx context.Context, acct usageprovider.Account) (userResponse, string, error) {
	out, ghErr := p.runner().Run(ctx, "gh", "api", "-H", "Cache-Control: no-cache", "-H", "Pragma: no-cache", "/copilot_internal/user")
	if ghErr == nil {
		if user, err := parseUser(out); err == nil {
			return user, "", nil
		} else {
			ghErr = err
		}
	}
	token, piErr := p.readPiGitHubToken(ctx, acct)
	if piErr != nil {
		var providerErr *usageprovider.Error
		if errors.As(ghErr, &providerErr) {
			return userResponse{}, "", ghErr
		}
		return userResponse{}, "", copilotPiError(piErr)
	}
	var user userResponse
	if err := p.getJSON(ctx, token, "/copilot_internal/user", &user); err != nil {
		return userResponse{}, "", err
	}
	if strings.TrimSpace(user.Login) == "" {
		return userResponse{}, "", &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("copilot user response has no login")}
	}
	return user, token, nil
}

func parseUser(out []byte) (userResponse, error) {
	var user userResponse
	if err := json.Unmarshal(out, &user); err != nil {
		return userResponse{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
	}
	if strings.TrimSpace(user.Login) == "" {
		return userResponse{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("copilot user response has no login")}
	}
	return user, nil
}

func (p *Provider) fetchAICreditUsageGH(ctx context.Context, login string, observedAt time.Time) (billingUsageResponse, error) {
	endpoint := billingEndpoint(login, observedAt)
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

func (p *Provider) fetchAICreditUsageHTTP(ctx context.Context, token, login string, observedAt time.Time) (billingUsageResponse, error) {
	var usage billingUsageResponse
	err := p.getJSON(ctx, token, billingEndpoint(login, observedAt), &usage)
	return usage, err
}

func billingEndpoint(login string, observedAt time.Time) string {
	return fmt.Sprintf("/users/%s/settings/billing/ai_credit/usage?year=%d&month=%d", url.PathEscape(login), observedAt.UTC().Year(), int(observedAt.UTC().Month()))
}

func (p *Provider) readPiGitHubToken(ctx context.Context, acct usageprovider.Account) (string, error) {
	path := piauth.ResolvePath(acct.AuthFile, p.env, p.homeDir)
	if path == "" {
		return "", errPiCopilotCredentialMissing
	}
	store, err := piauth.New(path)
	if err != nil {
		return "", err
	}
	credential, err := store.Read(ctx, piProviderID)
	if err != nil {
		return "", err
	}
	if credential.Type != "oauth" || strings.TrimSpace(credential.Refresh) == "" {
		return "", errPiCopilotCredentialMissing
	}
	if strings.TrimSpace(credential.EnterpriseURL) != "" {
		// Never send an enterprise-scoped token to public api.github.com.
		// The preferred gh path already handles enterprise host routing.
		return "", errPiCopilotEnterpriseUnsupported
	}
	return strings.TrimSpace(credential.Refresh), nil
}

func copilotPiError(err error) error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, piauth.ErrCredentialNotFound) {
		return &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("GitHub CLI unavailable and Pi Copilot credentials not found")}
	}
	if errors.Is(err, piauth.ErrLockUnavailable) || errors.Is(err, piauth.ErrLockCompromised) {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: err}
	}
	if errors.Is(err, errPiCopilotCredentialMissing) {
		return &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: err}
	}
	if errors.Is(err, errPiCopilotEnterpriseUnsupported) {
		return &usageprovider.Error{Code: usageprovider.ErrUnsupportedAccountShape, Provider: id, Err: err}
	}
	return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
}

func (p *Provider) getJSON(ctx context.Context, token, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+endpoint, nil)
	if err != nil {
		return fmt.Errorf("create GitHub request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "token-burn")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: errors.New("GitHub request failed")}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("invalid GitHub response body")}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: resp.StatusCode}
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden && githubRateLimited(resp):
		return &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: id, HTTPStatus: resp.StatusCode}
	case resp.StatusCode == http.StatusForbidden:
		return &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, HTTPStatus: resp.StatusCode, Err: errors.New("GitHub token lacks permission for Copilot usage")}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("invalid GitHub JSON")}
	}
	return nil
}

func githubRateLimited(resp *http.Response) bool {
	return strings.TrimSpace(resp.Header.Get("Retry-After")) != "" || strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0"
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
	if quota.HasQuota != nil {
		snap.Raw[prefix+"has_quota"] = *quota.HasQuota
	}
	if quota.OverageCount != nil {
		snap.Raw[prefix+"overage_count"] = *quota.OverageCount
	}
}

func windowFromQuota(name string, quota quotaSnapshot, resetAt *time.Time) (usageprovider.Window, bool) {
	// has_quota:false is GitHub's own "you are blocked, no quota available"
	// signal. It must surface as an exhausted window, not disappear —
	// dropping it here made the UI keep showing whatever capacity was last
	// reported instead of the current "no credits" reality.
	exhausted := quota.HasQuota != nil && !*quota.HasQuota

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
	if usedPercent == nil && exhausted {
		full := 100.0
		usedPercent = &full
	}
	if usedPercent == nil {
		return usageprovider.Window{}, false
	}
	if exhausted && *usedPercent < 100 {
		full := 100.0
		usedPercent = &full
	}

	return usageprovider.NewWindow(name, usageprovider.WindowOptions{
		UsedPercent:  usedPercent,
		ResetAt:      resetAt,
		LimitReached: exhausted || (quota.OverageCount != nil && *quota.OverageCount > 0),
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

	// The hardcoded allowance below is only an estimate. Cross-check it
	// against GitHub's own authoritative exhaustion signal so a stale or
	// wrong allowance can't report spare capacity GitHub has already cut off.
	quota, hasQuota := tokenBillingQuota(user)
	exhausted := hasQuota && quota.HasQuota != nil && !*quota.HasQuota
	overage := hasQuota && quota.OverageCount != nil && *quota.OverageCount > 0

	limit := aiCreditAllowance(user)
	if limit <= 0 {
		snap.Raw["ai_credit_used"] = usedCredits
		return
	}
	usedPercent := (usedCredits / limit) * 100
	if exhausted && usedPercent < 100 {
		usedPercent = 100
	}
	remainingPercent := 100 - usedPercent
	resetAt := parseReset(user.QuotaResetDateUTC, user.QuotaResetDate, observedAt)
	if resetAt == nil {
		resetAt = firstOfNextMonth(observedAt)
	}
	win, ok := usageprovider.NewWindow("ai_credits", usageprovider.WindowOptions{
		UsedPercent:      &usedPercent,
		RemainingPercent: &remainingPercent,
		ResetAt:          resetAt,
		LimitReached:     exhausted || overage,
	})
	if ok {
		snap.Windows = append(snap.Windows, win)
	}
}

// tokenBillingQuota returns the quota_snapshots entry GitHub marks as backing
// token-based billing, falling back to the conventional "premium_interactions"
// name when no entry is explicitly flagged.
func tokenBillingQuota(user userResponse) (quotaSnapshot, bool) {
	for _, quota := range user.QuotaSnapshots {
		if quota.TokenBasedBilling {
			return quota, true
		}
	}
	if quota, ok := user.QuotaSnapshots["premium_interactions"]; ok {
		return quota, true
	}
	return quotaSnapshot{}, false
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

func (p *Provider) httpClient() *http.Client {
	base := p.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 15 * time.Second}
	}
	client := *base
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func (p *Provider) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return defaultGitHubURL
}

func (p *Provider) homeDir() (string, error) {
	if p.HomeDir != nil {
		return p.HomeDir()
	}
	return os.UserHomeDir()
}

func (p *Provider) env(key string) string {
	if p.Env != nil {
		return p.Env(key)
	}
	return os.Getenv(key)
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
