package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id                    = "antigravity"
	sourceQuotaSummary    = "google_cloud_code_retrieve_user_quota_summary"
	sourceAvailableModels = "google_cloud_code_fetch_available_models"
	defaultOAuthURL       = "https://oauth2.googleapis.com/token"
)

var defaultBaseURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

type Provider struct {
	HTTPClient     *http.Client
	BaseURLs       []string
	Now            func() time.Time
	HomeDir        func() (string, error)
	StateDBPaths   []string
	KeychainSecret func() (string, error)
	TokenCachePath string
	CLITokenPath   string
	OAuthURL       string
	OAuthClientID  string
	OAuthSecret    string
	Env            func(string) string
}

func New() *Provider {
	return &Provider{}
}

func (p *Provider) ID() string {
	return id
}

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	tokens, err := p.tokenCandidates()
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	if len(tokens) == 0 {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthMissing,
			Provider: id,
			Err:      errors.New("antigravity oauth token not found; start Antigravity or run agy models"),
		}
	}

	var sawAuthExpired bool
	var lastErr error
	for _, token := range tokens {
		if token.ExpirySeconds > 0 && token.ExpirySeconds <= observedAt.Unix() {
			refreshed, err := p.refreshToken(ctx, token.RefreshToken, observedAt)
			if err != nil {
				sawAuthExpired = true
				lastErr = err
				continue
			}
			token = refreshed
		}
		snap, err := p.fetchSnapshot(ctx, token.AccessToken, acct, observedAt, token.Source)
		if err == nil {
			return snap, nil
		}
		var perr *usageprovider.Error
		if errors.As(err, &perr) && perr.Code == usageprovider.ErrAuthExpired {
			refreshed, refreshErr := p.refreshToken(ctx, token.RefreshToken, observedAt)
			if refreshErr == nil {
				retrySnap, retryErr := p.fetchSnapshot(ctx, refreshed.AccessToken, acct, observedAt, refreshed.Source)
				if retryErr == nil {
					return retrySnap, nil
				}
				lastErr = retryErr
				continue
			}
			sawAuthExpired = true
			lastErr = refreshErr
			continue
		}
		lastErr = err
	}
	if sawAuthExpired {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthExpired,
			Provider: id,
			Err:      errors.New("antigravity oauth token expired; start Antigravity or run agy models to refresh vendor credentials"),
		}
	}
	if lastErr != nil {
		return usageprovider.Snapshot{}, lastErr
	}
	return usageprovider.Snapshot{}, &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: id}
}

func (p *Provider) refreshToken(ctx context.Context, refreshToken string, observedAt time.Time) (tokenCandidate, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return tokenCandidate{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthExpired,
			Provider: id,
			Err:      errors.New("antigravity access token expired and no refresh token is available"),
		}
	}
	clientID := firstNonEmpty(p.OAuthClientID, p.env("TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID"))
	clientSecret := firstNonEmpty(p.OAuthSecret, p.env("TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		return tokenCandidate{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthExpired,
			Provider: id,
			Err:      errors.New("antigravity access token expired and OAuth client credentials are not configured"),
		}
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.oauthURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("antigravity create oauth refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return tokenCandidate{}, &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("antigravity read oauth refresh response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest {
		return tokenCandidate{}, &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return tokenCandidate{}, &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("oauth refresh unexpected status: %s", truncate(string(body), 256)),
		}
	}
	var payload refreshResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return tokenCandidate{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
	}
	token := tokenCandidate{
		AccessToken:   strings.TrimSpace(payload.AccessToken),
		RefreshToken:  refreshToken,
		ExpirySeconds: expiryFromNow(observedAt, payload.ExpiresIn),
		Source:        "oauth_refresh",
	}
	if token.AccessToken == "" {
		return tokenCandidate{}, &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: errors.New("oauth refresh response missing access token")}
	}
	p.cacheAccessToken(token)
	return token, nil
}

// fetchSnapshot prefers the real per-tier five-hour/weekly windows from
// loadCodeAssist + retrieveUserQuotaSummary. That pair is what Antigravity's
// own UI reads. fetchAvailableModels is a legacy, per-model-only endpoint: it
// has no weekly data, and when a pool is currently blocked it tends to stop
// reporting real numbers for that pool's models entirely (nil quotaInfo)
// rather than reporting them as exhausted, which silently hid blocked pools
// instead of showing them. It stays as a fallback for accounts/builds where
// the quota-summary endpoint isn't available.
func (p *Provider) fetchSnapshot(ctx context.Context, token string, acct usageprovider.Account, observedAt time.Time, tokenSource string) (usageprovider.Snapshot, error) {
	info, err := p.loadCodeAssist(ctx, token)
	if isAuthOrRateLimit(err) {
		return usageprovider.Snapshot{}, err
	}
	if err == nil && strings.TrimSpace(projectID(info)) != "" {
		summary, sErr := p.retrieveUserQuotaSummary(ctx, token, projectID(info))
		if isAuthOrRateLimit(sErr) {
			return usageprovider.Snapshot{}, sErr
		}
		if sErr == nil && len(summary.Groups) > 0 {
			return mapQuotaSummary(summary, info, acct, observedAt, tokenSource), nil
		}
	}

	payload, err := p.fetchAvailableModels(ctx, token)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	snap := mapFetchModels(payload, acct, observedAt, tokenSource)
	if plan := planName(info); plan != "" {
		snap.PlanType = plan
	}
	return snap, nil
}

func isAuthOrRateLimit(err error) bool {
	var perr *usageprovider.Error
	return errors.As(err, &perr) && (perr.Code == usageprovider.ErrAuthExpired || perr.Code == usageprovider.ErrRateLimited)
}

func projectID(info codeAssistInfo) string {
	return firstNonEmpty(info.CloudaicompanionProject, info.ProjectID, info.Project)
}

func planName(info codeAssistInfo) string {
	return firstNonEmpty(info.PaidTier.Name, info.PaidTier.ID, info.CurrentTier.ID)
}

func (p *Provider) loadCodeAssist(ctx context.Context, token string) (codeAssistInfo, error) {
	body := []byte(`{"metadata":{"ideType":"ANTIGRAVITY"}}`)
	var info codeAssistInfo
	err := p.callCloudCode(ctx, token, "loadCodeAssist", body, &info)
	return info, err
}

func (p *Provider) retrieveUserQuotaSummary(ctx context.Context, token, projectID string) (quotaSummaryResponse, error) {
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return quotaSummaryResponse{}, fmt.Errorf("antigravity marshal retrieveUserQuotaSummary request: %w", err)
	}
	var summary quotaSummaryResponse
	err = p.callCloudCode(ctx, token, "retrieveUserQuotaSummary", body, &summary)
	return summary, err
}

func (p *Provider) fetchAvailableModels(ctx context.Context, token string) (fetchModelsResponse, error) {
	var payload fetchModelsResponse
	err := p.callCloudCode(ctx, token, "fetchAvailableModels", []byte(`{}`), &payload)
	return payload, err
}

// callCloudCode POSTs to a v1internal Cloud Code method against each known
// base URL in turn, decoding the first successful JSON response into out.
func (p *Provider) callCloudCode(ctx context.Context, token, method string, body []byte, out any) error {
	for _, baseURL := range p.baseURLs() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1internal:"+method, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("antigravity create %s request: %w", method, err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", "antigravity")

		resp, err := p.httpClient().Do(req)
		if err != nil {
			return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: id, Err: err}
		}
		readErr := decodeCloudCodeResponse(resp, out)
		if readErr == nil {
			return nil
		}
		var perr *usageprovider.Error
		if errors.As(readErr, &perr) {
			if perr.Code == usageprovider.ErrAuthExpired || perr.Code == usageprovider.ErrRateLimited {
				return readErr
			}
		}
	}
	return &usageprovider.Error{
		Code:     usageprovider.ErrTransientHTTPFailure,
		Provider: id,
		Err:      fmt.Errorf("all antigravity cloud code endpoints failed for %s", method),
	}
}

func decodeCloudCodeResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("antigravity read cloud code response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: id, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("unexpected status: %s", truncate(string(body), 256)),
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: id, Err: err}
	}
	return nil
}

func mapFetchModels(payload fetchModelsResponse, acct usageprovider.Account, observedAt time.Time, tokenSource string) usageprovider.Snapshot {
	snap := usageprovider.Snapshot{
		Provider:   id,
		AccountID:  firstNonEmpty(acct.ID, acct.Alias, acct.ProviderAccountID, "antigravity-default"),
		Source:     sourceAvailableModels,
		ObservedAt: observedAt.UTC(),
		Raw: map[string]any{
			"token_source": tokenSource,
			"model_count":  len(payload.Models),
		},
	}

	quotas := modelQuotas(payload)
	bestByPool := map[string]modelQuota{}
	for _, quota := range quotas {
		current, ok := bestByPool[quota.Pool]
		if !ok || quota.RemainingFraction < current.RemainingFraction {
			bestByPool[quota.Pool] = quota
		}
	}
	for _, pool := range []string{"gemini", "claude_and_gpt"} {
		quota, ok := bestByPool[pool]
		if !ok {
			continue
		}
		used := (1 - quota.RemainingFraction) * 100
		remaining := quota.RemainingFraction * 100
		resetAt := parseReset(quota.ResetTime)
		win, ok := usageprovider.NewWindow(pool, usageprovider.WindowOptions{
			UsedPercent:      &used,
			RemainingPercent: &remaining,
			ResetAt:          resetAt,
		})
		if ok {
			snap.Windows = append(snap.Windows, win)
		}
		snap.Raw[pool+"_model"] = quota.ModelID
		snap.Raw[pool+"_label"] = quota.Label
	}
	return snap
}

// mapQuotaSummary maps retrieveUserQuotaSummary's real per-tier buckets into
// windows. Each group (Gemini, Claude and GPT) carries a five-hour and a
// weekly bucket; the five-hour bucket keeps the existing "gemini" /
// "claude_and_gpt" window names for continuity with prior history, and the
// weekly bucket gets a "_weekly" suffix, matching the seven_day_sonnet /
// seven_day_opus naming convention used for Claude's per-scope windows.
func mapQuotaSummary(summary quotaSummaryResponse, info codeAssistInfo, acct usageprovider.Account, observedAt time.Time, tokenSource string) usageprovider.Snapshot {
	snap := usageprovider.Snapshot{
		Provider:   id,
		AccountID:  firstNonEmpty(acct.ID, acct.Alias, acct.ProviderAccountID, "antigravity-default"),
		PlanType:   planName(info),
		Source:     sourceQuotaSummary,
		ObservedAt: observedAt.UTC(),
		Raw: map[string]any{
			"token_source": tokenSource,
			"project_id":   projectID(info),
		},
	}

	for _, group := range summary.Groups {
		pool := quotaSummaryPool(group.DisplayName)
		for _, bucket := range group.Buckets {
			name := pool
			switch strings.ToLower(strings.TrimSpace(bucket.Window)) {
			case "5h", "five-hour", "five_hour", "":
				// keep the bare pool name
			case "weekly", "week":
				name = pool + "_weekly"
			default:
				name = pool + "_" + usageprovider.NormalizeWindowName(bucket.Window)
			}

			used := (1 - bucket.RemainingFraction) * 100
			remaining := bucket.RemainingFraction * 100
			resetAt := parseReset(bucket.ResetTime)
			win, ok := usageprovider.NewWindow(name, usageprovider.WindowOptions{
				UsedPercent:      &used,
				RemainingPercent: &remaining,
				ResetAt:          resetAt,
			})
			if !ok {
				continue
			}
			snap.Windows = append(snap.Windows, win)
			if strings.TrimSpace(bucket.Description) != "" {
				snap.Raw[name+"_status"] = bucket.Description
			}
		}
	}
	return snap
}

func quotaSummaryPool(displayName string) string {
	if strings.Contains(strings.ToLower(displayName), "gemini") {
		return "gemini"
	}
	return "claude_and_gpt"
}

func modelQuotas(payload fetchModelsResponse) []modelQuota {
	var out []modelQuota
	for key, model := range payload.Models {
		if model.IsInternal || model.QuotaInfo == nil || model.QuotaInfo.RemainingFraction == nil {
			continue
		}
		modelID := firstNonEmpty(model.Model, key)
		if blacklistedModel(modelID) {
			continue
		}
		label := firstNonEmpty(model.DisplayName, model.Label, modelID)
		if strings.TrimSpace(label) == "" {
			continue
		}
		remaining := *model.QuotaInfo.RemainingFraction
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 1 {
			remaining = 1
		}
		out = append(out, modelQuota{
			Label:             label,
			ModelID:           modelID,
			Pool:              quotaPool(label, modelID),
			RemainingFraction: remaining,
			ResetTime:         model.QuotaInfo.ResetTime,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pool != out[j].Pool {
			return out[i].Pool < out[j].Pool
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func quotaPool(label, modelID string) string {
	text := strings.ToLower(label + " " + modelID)
	if strings.Contains(text, "gemini") {
		return "gemini"
	}
	return "claude_and_gpt"
}

func blacklistedModel(modelID string) bool {
	modelID = strings.ToUpper(strings.TrimSpace(modelID))
	switch modelID {
	case "", "MODEL_CHAT_20706", "MODEL_CHAT_23310",
		"MODEL_GOOGLE_GEMINI_2_5_FLASH",
		"MODEL_GOOGLE_GEMINI_2_5_FLASH_THINKING",
		"MODEL_GOOGLE_GEMINI_2_5_FLASH_LITE",
		"MODEL_GOOGLE_GEMINI_2_5_PRO",
		"MODEL_PLACEHOLDER_M19", "MODEL_PLACEHOLDER_M9", "MODEL_PLACEHOLDER_M12":
		return true
	default:
		return false
	}
}

func parseReset(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func (p *Provider) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (p *Provider) baseURLs() []string {
	if len(p.BaseURLs) > 0 {
		return p.BaseURLs
	}
	return defaultBaseURLs
}

func (p *Provider) oauthURL() string {
	if p.OAuthURL != "" {
		return p.OAuthURL
	}
	return defaultOAuthURL
}

func (p *Provider) env(key string) string {
	if p.Env != nil {
		return p.Env(key)
	}
	return os.Getenv(key)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
