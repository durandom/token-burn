package xai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/piauth"
	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id                      = "xai"
	source                  = "xai_grok_cli_billing"
	defaultBaseURL          = "https://cli-chat-proxy.grok.com/v1"
	defaultOAuthURL         = "https://auth.x.ai/oauth2/token"
	xaiOAuthClientID        = "b1a00492-073a-47ea-816f-4c329264a828"
	clientVersion           = "1.4.0"
	maxResponseBytes        = 64 << 10
	refreshSkew             = 5 * time.Minute
	defaultTokenLifetime    = time.Hour
	maxTokenLifetimeSeconds = int64(30 * 24 * time.Hour / time.Second)
	maxUsageWindowDuration  = 366 * 24 * time.Hour
)

type Provider struct {
	HTTPClient *http.Client
	BaseURL    string
	OAuthURL   string
	Now        func() time.Time
	HomeDir    func() (string, error)
	Env        func(string) string
}

func New() *Provider { return &Provider{} }

func (p *Provider) ID() string { return id }

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	cred, path, err := p.readCredential(ctx, acct)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	if cred.Expires > 0 && cred.Expires <= observedAt.UnixMilli() {
		cred, err = p.refreshCredential(ctx, path, cred.Access, false, observedAt)
		if err != nil {
			return usageprovider.Snapshot{}, err
		}
	}

	payload, err := p.fetchUsage(ctx, cred.Access)
	if isAuthExpired(err) {
		cred, err = p.refreshCredential(ctx, path, cred.Access, true, observedAt)
		if err != nil {
			return usageprovider.Snapshot{}, err
		}
		payload, err = p.fetchUsage(ctx, cred.Access)
	}
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	return mapBilling(payload, acct, observedAt)
}

func (p *Provider) fetchUsage(ctx context.Context, access string) (billingResponse, error) {
	headers := map[string]string{
		"Authorization":         "Bearer " + access,
		"X-XAI-Token-Auth":      "xai-grok-cli",
		"x-grok-client-version": clientVersion,
		"x-grok-client-mode":    "headless",
	}
	var user userResponse
	if err := p.getJSON(ctx, "/user", headers, &user); err != nil {
		return billingResponse{}, err
	}
	if !validUserID(user.UserID) {
		return billingResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      errors.New("xai account identity could not be verified"),
		}
	}
	headers["x-userid"] = user.UserID
	var billing billingResponse
	if err := p.getJSON(ctx, "/billing?format=credits", headers, &billing); err != nil {
		return billingResponse{}, err
	}
	return billing, nil
}

func (p *Provider) getJSON(ctx context.Context, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+endpoint, nil)
	if err != nil {
		return fmt.Errorf("xai create usage request: %w", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: safeTransportError(err)}
	}
	defer resp.Body.Close()
	if err := classifyHTTPStatus(resp.StatusCode); err != nil {
		return err
	}
	body, err := readBounded(resp.Body)
	if err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("malformed JSON")}
	}
	return nil
}

func classifyHTTPStatus(status int) error {
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: status}
	case status == http.StatusTooManyRequests:
		return &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: id, HTTPStatus: status}
	default:
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, HTTPStatus: status}
	}
}

func readBounded(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("read response")
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("response exceeds 64 KiB limit")
	}
	return body, nil
}

func safeTransportError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("request failed")
}

func validUserID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func mapBilling(payload billingResponse, acct usageprovider.Account, observedAt time.Time) (usageprovider.Snapshot, error) {
	if payload.Config == nil {
		return usageprovider.Snapshot{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("billing response missing config")}
	}
	cfg := payload.Config
	var used *float64
	if cfg.CreditUsagePercent != nil && validPercent(*cfg.CreditUsagePercent) {
		used = cfg.CreditUsagePercent
	} else if cents(cfg.Used) != nil && cents(cfg.MonthlyLimit) != nil && *cents(cfg.MonthlyLimit) > 0 {
		value := float64(*cents(cfg.Used)) / float64(*cents(cfg.MonthlyLimit)) * 100
		used = &value
	}
	if used == nil {
		return usageprovider.Snapshot{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("billing response missing usable quota")}
	}

	legacyMonthly := cfg.CreditUsagePercent == nil && cents(cfg.Used) != nil && cents(cfg.MonthlyLimit) != nil && *cents(cfg.MonthlyLimit) > 0 && parseTime(cfg.BillingPeriodEnd) != nil
	name := "quota"
	if legacyMonthly {
		name = "monthly"
	}
	startValue := cfg.BillingPeriodStart
	endValue := cfg.BillingPeriodEnd
	periodType := ""
	if cfg.CurrentPeriod != nil {
		periodType = cfg.CurrentPeriod.Type
		startValue = firstNonEmpty(cfg.CurrentPeriod.Start, startValue)
		endValue = firstNonEmpty(cfg.CurrentPeriod.End, endValue)
		switch strings.ToUpper(strings.TrimSpace(periodType)) {
		case "WEEKLY", "USAGE_PERIOD_TYPE_WEEKLY":
			name = "weekly"
		case "MONTHLY", "USAGE_PERIOD_TYPE_MONTHLY":
			name = "monthly"
		default:
			if !legacyMonthly {
				name = "quota"
			}
		}
	}
	start := parseTime(startValue)
	resetAt := parseTime(endValue)
	var windowSeconds *int
	if start != nil && resetAt != nil && resetAt.After(*start) {
		duration := resetAt.Sub(*start)
		if duration <= maxUsageWindowDuration {
			seconds := int(duration / time.Second)
			windowSeconds = &seconds
		}
	}
	win, ok := usageprovider.NewWindow(name, usageprovider.WindowOptions{
		UsedPercent:   used,
		ResetAt:       resetAt,
		WindowSeconds: windowSeconds,
	})
	if !ok {
		return usageprovider.Snapshot{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("billing response has invalid quota")}
	}

	raw := map[string]any{}
	if periodType != "" {
		raw["period_type"] = periodType
	}
	addBool(raw, "is_unified_billing_user", cfg.IsUnifiedBillingUser)
	addBool(raw, "on_demand_enabled", payload.OnDemandEnabled)
	addCents(raw, "monthly_limit_cents", cfg.MonthlyLimit)
	addCents(raw, "used_cents", cfg.Used)
	addCents(raw, "on_demand_cap_cents", cfg.OnDemandCap)
	addCents(raw, "on_demand_used_cents", cfg.OnDemandUsed)
	addCents(raw, "prepaid_balance_cents", cfg.PrepaidBalance)

	accountID := firstNonEmpty(acct.ID, acct.Alias, acct.ProviderAccountID, "xai-default")
	return usageprovider.Snapshot{
		Provider:   id,
		AccountID:  accountID,
		PlanType:   strings.TrimSpace(payload.SubscriptionTier),
		Source:     source,
		ObservedAt: observedAt.UTC(),
		Windows:    []usageprovider.Window{win},
		Raw:        raw,
	}, nil
}

func validPercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func cents(value *moneyValue) *int64 {
	if value == nil || value.Val == nil || *value.Val < 0 {
		return nil
	}
	return value.Val
}

func addCents(raw map[string]any, key string, value *moneyValue) {
	if amount := cents(value); amount != nil {
		raw[key] = *amount
	}
}

func addBool(raw map[string]any, key string, value *bool) {
	if value != nil {
		raw[key] = *value
	}
}

func parseTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	utc := parsed.UTC()
	return &utc
}

func (p *Provider) readCredential(ctx context.Context, acct usageprovider.Account) (oauthCredential, string, error) {
	path := p.authPath(acct)
	if path == "" {
		return oauthCredential{}, "", &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Pi auth path could not be resolved")}
	}
	store, err := piauth.New(path)
	if err != nil {
		return oauthCredential{}, path, authReadError(err)
	}
	credential, err := store.Read(ctx, id)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, piauth.ErrCredentialNotFound) {
		return oauthCredential{}, store.Path, &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Pi xAI OAuth credentials not found; run /login xai in Pi")}
	}
	if err != nil {
		return oauthCredential{}, store.Path, authReadError(err)
	}
	if err := validateCredential(credential); err != nil {
		return oauthCredential{}, store.Path, err
	}
	return credential, store.Path, nil
}

func validateCredential(credential oauthCredential) error {
	if credential.Type != "oauth" {
		return &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Pi xAI subscription OAuth credentials required; run /login xai and choose Use a subscription")}
	}
	if strings.TrimSpace(credential.Access) == "" {
		return &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Pi xAI OAuth access token is missing; run /login xai")}
	}
	return nil
}

func authReadError(err error) error {
	var providerErr *usageprovider.Error
	if errors.As(err, &providerErr) {
		return err
	}
	if errors.Is(err, piauth.ErrLockUnavailable) || errors.Is(err, piauth.ErrLockCompromised) {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: err}
	}
	return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
}

func (p *Provider) refreshCredential(ctx context.Context, path, rejectedAccess string, force bool, now time.Time) (oauthCredential, error) {
	store, err := piauth.New(path)
	if err != nil {
		return oauthCredential{}, authReadError(err)
	}
	result, err := store.Modify(ctx, id, func(lockCtx context.Context, current piauth.OAuthCredential) (*piauth.OAuthCredential, error) {
		if err := validateCredential(current); err != nil {
			return nil, err
		}
		if current.Access != rejectedAccess {
			return nil, nil
		}
		if !force && (current.Expires == 0 || current.Expires > now.UnixMilli()) {
			return nil, nil
		}
		if strings.TrimSpace(current.Refresh) == "" {
			return nil, &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, Err: errors.New("Pi xAI OAuth refresh token is missing; run /login xai")}
		}
		refreshed, err := p.requestRefresh(lockCtx, current.Refresh, now)
		if err != nil {
			return nil, err
		}
		if refreshed.Refresh == "" {
			refreshed.Refresh = current.Refresh
		}
		return &refreshed, nil
	})
	if err != nil {
		return oauthCredential{}, authReadError(err)
	}
	return result, nil
}

func (p *Provider) requestRefresh(ctx context.Context, refreshToken string, now time.Time) (oauthCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", xaiOAuthClientID)
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.oauthURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return oauthCredential{}, fmt.Errorf("xai create OAuth refresh request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: safeTransportError(err)}
	}
	defer resp.Body.Close()
	body, readErr := readBounded(resp.Body)
	if readErr != nil {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: readErr}
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, HTTPStatus: resp.StatusCode}
	}
	var payload refreshResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("xai OAuth refresh returned malformed JSON")}
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("xai OAuth refresh response missing access token")}
	}
	lifetime := defaultTokenLifetime
	if payload.ExpiresIn != nil {
		seconds := *payload.ExpiresIn
		if seconds <= int64(refreshSkew/time.Second) || seconds > maxTokenLifetimeSeconds {
			return oauthCredential{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("xai OAuth refresh response has invalid expiry")}
		}
		lifetime = time.Duration(seconds) * time.Second
	}
	return oauthCredential{
		Type:    "oauth",
		Access:  strings.TrimSpace(payload.AccessToken),
		Refresh: strings.TrimSpace(payload.RefreshToken),
		Expires: now.Add(lifetime - refreshSkew).UnixMilli(),
	}, nil
}

func isAuthExpired(err error) bool {
	var providerErr *usageprovider.Error
	return errors.As(err, &providerErr) && providerErr.Code == usageprovider.ErrAuthExpired
}

func (p *Provider) authPath(acct usageprovider.Account) string {
	return piauth.ResolvePath(acct.AuthFile, p.env, p.homeDir)
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
	return defaultBaseURL
}

func (p *Provider) oauthURL() string {
	if p.OAuthURL != "" {
		return p.OAuthURL
	}
	return defaultOAuthURL
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
