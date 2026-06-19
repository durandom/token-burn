package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id             = "codex"
	defaultBaseURL = "https://chatgpt.com/backend-api"
	source         = "wham_usage"
)

type Provider struct {
	HTTPClient *http.Client
	BaseURL    string
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

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	auth, path, err := p.readAuth(acct)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	if strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthMissing,
			Provider: id,
			Err:      fmt.Errorf("codex auth file %s does not contain an access token", path),
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+"/wham/usage", nil)
	if err != nil {
		return usageprovider.Snapshot{}, fmt.Errorf("codex create usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "token-burn")

	headerAccountID := firstNonEmpty(auth.Tokens.AccountID, auth.AccountID, acct.ProviderAccountID)
	if headerAccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", headerAccountID)
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrTransientHTTPFailure,
			Provider: id,
			Err:      err,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usageprovider.Snapshot{}, fmt.Errorf("codex read usage response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:       usageprovider.ErrAuthExpired,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:       usageprovider.ErrRateLimited,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("unexpected status: %s", truncate(string(body), 256)),
		}
	}

	var payload usagePayload
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      err,
		}
	}

	snap, err := mapUsagePayload(payload, acct, observedAt)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	return snap, nil
}

func (p *Provider) readAuth(acct usageprovider.Account) (authFile, string, error) {
	for _, path := range p.authCandidates(acct) {
		if strings.TrimSpace(path) == "" {
			continue
		}
		data, err := os.ReadFile(path)
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
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (p *Provider) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return defaultBaseURL
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

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
