package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/piauth"
	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id                   = "codex"
	defaultBaseURL       = "https://chatgpt.com/backend-api"
	defaultOAuthURL      = "https://auth.openai.com/oauth/token"
	openAIOAuthClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	source               = "wham_usage"
	piProviderID         = "openai-codex"
	piRefreshSkew        = 5 * time.Minute
	maxResponseBytes     = 1 << 20
	maxTokenLifetimeSecs = int64(30 * 24 * time.Hour / time.Second)
)

type Provider struct {
	HTTPClient *http.Client
	BaseURL    string
	OAuthURL   string
	Now        func() time.Time
	HomeDir    func() (string, error)
	Env        func(string) string
}

func New() *Provider {
	return &Provider{}
}

func (p *Provider) ID() string {
	return id
}

type resolvedCredential struct {
	Access    string
	AccountID string
	PiStore   *piauth.Store
}

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	credential, err := p.resolveCredential(ctx, acct, observedAt)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	payload, err := p.fetchUsage(ctx, credential)
	if isAuthExpired(err) && credential.PiStore != nil {
		credential, err = p.refreshPiCredential(ctx, credential, true, observedAt)
		if err == nil {
			payload, err = p.fetchUsage(ctx, credential)
		}
	}
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	return mapUsagePayload(payload, acct, observedAt)
}

func (p *Provider) fetchUsage(ctx context.Context, credential resolvedCredential) (usagePayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+"/wham/usage", nil)
	if err != nil {
		return usagePayload{}, fmt.Errorf("codex create usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credential.Access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "token-burn")
	if credential.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", credential.AccountID)
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return usagePayload{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return usagePayload{}, fmt.Errorf("codex read usage response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return usagePayload{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("usage response exceeds size limit")}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return usagePayload{}, &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return usagePayload{}, &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return usagePayload{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, HTTPStatus: resp.StatusCode, Err: errors.New("unexpected usage response status")}
	}
	var payload usagePayload
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return usagePayload{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
	}
	return payload, nil
}

func (p *Provider) resolveCredential(ctx context.Context, acct usageprovider.Account, now time.Time) (resolvedCredential, error) {
	if strings.TrimSpace(acct.AuthFile) != "" {
		path := expandHome(acct.AuthFile, p.homeDir)
		if native, ok, err := readNativeAuthPath(path); err != nil {
			return resolvedCredential{}, err
		} else if ok {
			return resolvedCredential{Access: native.Tokens.AccessToken, AccountID: firstNonEmpty(native.Tokens.AccountID, native.AccountID, acct.ProviderAccountID)}, nil
		}
		if credential, err := p.readPiCredential(ctx, path, now); err == nil {
			credential.AccountID = firstNonEmpty(credential.AccountID, acct.ProviderAccountID)
			return credential, nil
		} else if !isPiMissing(err) {
			return resolvedCredential{}, err
		}
	}

	nativeAcct := acct
	nativeAcct.AuthFile = ""
	if native, _, err := p.readAuth(nativeAcct); err == nil {
		return resolvedCredential{Access: native.Tokens.AccessToken, AccountID: firstNonEmpty(native.Tokens.AccountID, native.AccountID, acct.ProviderAccountID)}, nil
	} else {
		var providerErr *usageprovider.Error
		if errors.As(err, &providerErr) && providerErr.Code != usageprovider.ErrAuthMissing {
			return resolvedCredential{}, err
		}
	}
	path := piauth.ResolvePath("", p.env, p.homeDir)
	if path != "" {
		if credential, err := p.readPiCredential(ctx, path, now); err == nil {
			credential.AccountID = firstNonEmpty(credential.AccountID, acct.ProviderAccountID)
			return credential, nil
		} else if !isPiMissing(err) {
			return resolvedCredential{}, err
		}
	}
	return resolvedCredential{}, &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Codex credentials not found; run codex login or /login openai-codex in Pi")}
}

func readNativeAuthPath(path string) (authFile, bool, error) {
	data, err := readCredentialFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return authFile{}, false, nil
	}
	if err != nil {
		return authFile{}, false, fmt.Errorf("codex read auth file %s: %w", path, err)
	}
	var auth authFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return authFile{}, false, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: fmt.Errorf("parse codex auth file %s: %w", path, err)}
	}
	return auth, strings.TrimSpace(auth.Tokens.AccessToken) != "", nil
}

func readCredentialFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("credential file exceeds 1 MiB limit")
	}
	return data, nil
}

func (p *Provider) readPiCredential(ctx context.Context, path string, now time.Time) (resolvedCredential, error) {
	store, err := piauth.New(path)
	if err != nil {
		return resolvedCredential{}, piAuthError(err)
	}
	credential, err := store.Read(ctx, piProviderID)
	if err != nil {
		return resolvedCredential{}, piAuthError(err)
	}
	if credential.Type != "oauth" || strings.TrimSpace(credential.Access) == "" {
		return resolvedCredential{}, &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Pi OpenAI Codex OAuth credentials required")}
	}
	resolved := resolvedCredential{Access: credential.Access, AccountID: credential.AccountID, PiStore: store}
	if credential.Expires > 0 && credential.Expires <= now.Add(piRefreshSkew).UnixMilli() {
		return p.refreshPiCredential(ctx, resolved, false, now)
	}
	return resolved, nil
}

func (p *Provider) refreshPiCredential(ctx context.Context, rejected resolvedCredential, force bool, now time.Time) (resolvedCredential, error) {
	result, err := rejected.PiStore.Modify(ctx, piProviderID, func(lockCtx context.Context, current piauth.OAuthCredential) (*piauth.OAuthCredential, error) {
		if current.Type != "oauth" || strings.TrimSpace(current.Access) == "" || strings.TrimSpace(current.Refresh) == "" {
			return nil, &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, Err: errors.New("Pi OpenAI Codex OAuth refresh credentials are missing")}
		}
		if current.Access != rejected.Access {
			return nil, nil
		}
		if !force && (current.Expires == 0 || current.Expires > now.Add(piRefreshSkew).UnixMilli()) {
			return nil, nil
		}
		refreshed, err := p.requestPiRefresh(lockCtx, current, now)
		if err != nil {
			return nil, err
		}
		return &refreshed, nil
	})
	if err != nil {
		return resolvedCredential{}, piAuthError(err)
	}
	return resolvedCredential{Access: result.Access, AccountID: result.AccountID, PiStore: rejected.PiStore}, nil
}

func (p *Provider) requestPiRefresh(ctx context.Context, current piauth.OAuthCredential, now time.Time) (piauth.OAuthCredential, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {openAIOAuthClientID}, "refresh_token": {current.Refresh}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.oauthURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return piauth.OAuthCredential{}, fmt.Errorf("codex create OAuth refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return piauth.OAuthCredential{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: errors.New("OAuth refresh request failed")}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return piauth.OAuthCredential{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("invalid OAuth refresh response")}
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return piauth.OAuthCredential{}, &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return piauth.OAuthCredential{}, &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return piauth.OAuthCredential{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, HTTPStatus: resp.StatusCode}
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.RefreshToken) == "" || payload.ExpiresIn <= 0 || payload.ExpiresIn > maxTokenLifetimeSecs {
		return piauth.OAuthCredential{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("invalid OAuth refresh payload")}
	}
	return piauth.OAuthCredential{Type: "oauth", Access: strings.TrimSpace(payload.AccessToken), Refresh: strings.TrimSpace(payload.RefreshToken), Expires: now.Add(time.Duration(payload.ExpiresIn) * time.Second).UnixMilli(), AccountID: current.AccountID}, nil
}

func isPiMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, piauth.ErrCredentialNotFound)
}

func piAuthError(err error) error {
	var providerErr *usageprovider.Error
	if errors.As(err, &providerErr) {
		return err
	}
	if isPiMissing(err) {
		return &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id, Err: errors.New("Pi OpenAI Codex OAuth credentials not found")}
	}
	if errors.Is(err, piauth.ErrLockUnavailable) || errors.Is(err, piauth.ErrLockCompromised) {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: err}
	}
	return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
}

func isAuthExpired(err error) bool {
	var providerErr *usageprovider.Error
	return errors.As(err, &providerErr) && providerErr.Code == usageprovider.ErrAuthExpired
}

func (p *Provider) readAuth(acct usageprovider.Account) (authFile, string, error) {
	for _, path := range p.authCandidates(acct) {
		if strings.TrimSpace(path) == "" {
			continue
		}
		data, err := readCredentialFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return authFile{}, "", fmt.Errorf("codex read auth file %s: %w", path, err)
		}
		var auth authFile
		if err := json.Unmarshal(data, &auth); err != nil {
			return authFile{}, "", &usageprovider.Error{
				Code:     usageprovider.ErrInvalidResponse,
				Provider: id,
				Err:      fmt.Errorf("parse codex auth file %s: %w", path, err),
			}
		}
		if strings.TrimSpace(auth.Tokens.AccessToken) == "" {
			continue
		}
		return auth, path, nil
	}
	return authFile{}, "", &usageprovider.Error{
		Code:     usageprovider.ErrAuthMissing,
		Provider: id,
		Err:      errors.New("codex auth file not found; run codex login"),
	}
}

func (p *Provider) authCandidates(acct usageprovider.Account) []string {
	var paths []string
	if acct.AuthFile != "" {
		paths = append(paths, expandHome(acct.AuthFile, p.homeDir))
	}
	if codexHome := p.env("CODEX_HOME"); codexHome != "" {
		paths = append(paths, filepath.Join(codexHome, "auth.json"))
	}
	if home, err := p.homeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".codex", "auth.json"))
	}
	return dedupe(paths)
}

func mapUsagePayload(payload usagePayload, acct usageprovider.Account, observedAt time.Time) (usageprovider.Snapshot, error) {
	planType := payload.PlanType
	if planType == "" && payload.RateLimitStatus != nil {
		planType = payload.RateLimitStatus.PlanType
	}

	snap := usageprovider.Snapshot{
		Provider:   id,
		AccountID:  acct.ID,
		PlanType:   planType,
		Source:     source,
		ObservedAt: observedAt.UTC(),
		Raw: map[string]any{
			"account_id": payload.AccountID,
			"user_id":    payload.UserID,
			"credits":    payload.Credits,
		},
	}
	if snap.AccountID == "" {
		snap.AccountID = firstNonEmpty(acct.Alias, payload.AccountID, acct.ProviderAccountID, "default")
	}

	windows := make(map[string]usageprovider.Window)
	addLimitDetails(windows, payload.RateLimit, "primary", "secondary", observedAt)
	addLimitDetails(windows, payload.CodeReviewRateLimit, "code_review_primary", "code_review_secondary", observedAt)
	addAdditionalLimits(windows, payload.AdditionalRateLimits, observedAt)
	if payload.RateLimitStatus != nil {
		addLimitDetails(windows, payload.RateLimitStatus.RateLimit, "primary", "secondary", observedAt)
		addLimitDetails(windows, payload.RateLimitStatus.CodeReviewRateLimit, "code_review_primary", "code_review_secondary", observedAt)
		addAdditionalLimits(windows, payload.RateLimitStatus.AdditionalRateLimits, observedAt)
		if snap.PlanType == "" {
			snap.PlanType = payload.RateLimitStatus.PlanType
		}
		if payload.Credits == nil {
			snap.Raw["credits"] = payload.RateLimitStatus.Credits
		}
	}

	for _, name := range stableWindowOrder(windows) {
		snap.Windows = append(snap.Windows, windows[name])
	}
	return snap, nil
}

func addLimitDetails(out map[string]usageprovider.Window, details *usageLimitDetails, primaryName, secondaryName string, observedAt time.Time) {
	if details == nil {
		return
	}
	primary := details.PrimaryWindow
	if primary == nil {
		primary = details.Primary
	}
	secondary := details.SecondaryWindow
	if secondary == nil {
		secondary = details.Secondary
	}
	addWindow(out, primaryNameFor(primaryName, primary), primary, details.LimitReached, observedAt)
	addWindow(out, secondaryNameFor(secondaryName, secondary), secondary, details.LimitReached, observedAt)
}

func addAdditionalLimits(out map[string]usageprovider.Window, additional []usageAdditionalLimit, observedAt time.Time) {
	for _, extra := range additional {
		limitID := usageprovider.NormalizeWindowName(firstNonEmpty(extra.MeteredFeature, extra.LimitName))
		if limitID == "" || limitID == "unknown" || limitID == "codex" {
			continue
		}
		addLimitDetails(out, extra.RateLimit, "additional_"+limitID+"_primary", "additional_"+limitID+"_secondary", observedAt)
	}
}

func addWindow(out map[string]usageprovider.Window, name string, info *usageWindowInfo, limitReached bool, observedAt time.Time) {
	if info == nil {
		return
	}
	resetAt := resolveReset(info, observedAt)
	windowSeconds := resolveWindowSeconds(info)
	win, ok := usageprovider.NewWindow(name, usageprovider.WindowOptions{
		UsedPercent:      info.UsedPercent,
		RemainingPercent: info.RemainingPercent,
		ResetAt:          resetAt,
		WindowSeconds:    windowSeconds,
		LimitReached:     limitReached,
	})
	if !ok {
		return
	}
	out[win.Name] = win
}

func primaryNameFor(fallback string, info *usageWindowInfo) string {
	if fallback != "primary" {
		return fallback
	}
	if info != nil && info.LimitWindowSeconds == 18000 {
		return "five_hour"
	}
	return fallback
}

func secondaryNameFor(fallback string, info *usageWindowInfo) string {
	if fallback != "secondary" {
		return fallback
	}
	if info != nil && info.LimitWindowSeconds == 604800 {
		return "seven_day"
	}
	return fallback
}

func resolveReset(info *usageWindowInfo, observedAt time.Time) *time.Time {
	switch {
	case info.ResetAt > 0:
		t := time.Unix(info.ResetAt, 0).UTC()
		return &t
	case info.ResetsAt > 0:
		t := time.Unix(info.ResetsAt, 0).UTC()
		return &t
	case info.ResetAfterSeconds > 0:
		return usageprovider.ParseResetAfterSeconds(info.ResetAfterSeconds, observedAt)
	default:
		return nil
	}
}

func resolveWindowSeconds(info *usageWindowInfo) *int {
	switch {
	case info.LimitWindowSeconds > 0:
		v := info.LimitWindowSeconds
		return &v
	case info.WindowMinutes > 0:
		v := info.WindowMinutes * 60
		return &v
	default:
		return nil
	}
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

func expandHome(path string, homeDir func() (string, error)) string {
	if path == "~" {
		if home, err := homeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := homeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func dedupe(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stableWindowOrder(windows map[string]usageprovider.Window) []string {
	preferred := []string{
		"five_hour",
		"seven_day",
		"primary",
		"secondary",
		"code_review_primary",
		"code_review_secondary",
	}
	seen := map[string]struct{}{}
	var out []string
	for _, name := range preferred {
		if _, ok := windows[name]; ok {
			out = append(out, name)
			seen[name] = struct{}{}
		}
	}
	for name := range windows {
		if _, ok := seen[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out[len(out)-(len(windows)-len(seen)):])
	return out
}
