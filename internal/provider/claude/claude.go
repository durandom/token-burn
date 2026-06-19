package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id             = "claude"
	defaultBaseURL = "https://api.anthropic.com/api/oauth"
	source         = "anthropic_oauth_usage"
)

type Provider struct {
	HTTPClient    *http.Client
	BaseURL       string
	Now           func() time.Time
	HomeDir       func() (string, error)
	Env           func(string) string
	KeychainToken func() (string, error)
}

func New() *Provider {
	return &Provider{}
}

func (p *Provider) ID() string {
	return id
}

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	token, err := p.readAccessToken(acct)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+"/usage", nil)
	if err != nil {
		return usageprovider.Snapshot{}, fmt.Errorf("claude create usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "token-burn")

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
		return usageprovider.Snapshot{}, fmt.Errorf("claude read usage response: %w", err)
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

	var payload usageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      err,
		}
	}

	return mapUsageResponse(payload, acct, observedAt), nil
}

func (p *Provider) readAccessToken(acct usageprovider.Account) (string, error) {
	if token := strings.TrimSpace(p.env("CLAUDE_CODE_OAUTH_TOKEN")); token != "" {
		return token, nil
	}
	for _, path := range p.credentialCandidates(acct) {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("claude read credentials file %s: %w", path, err)
		}
		token, err := accessTokenFromJSON(data)
		if err != nil {
			return "", &usageprovider.Error{
				Code:     usageprovider.ErrInvalidResponse,
				Provider: id,
				Err:      fmt.Errorf("parse claude credentials file %s: %w", path, err),
			}
		}
		if token != "" {
			return token, nil
		}
	}
	token, err := p.keychainAccessToken()
	if err != nil {
		return "", err
	}
	if token != "" {
		return token, nil
	}
	return "", &usageprovider.Error{
		Code:     usageprovider.ErrAuthMissing,
		Provider: id,
		Err:      errors.New("claude oauth credentials not found; run claude login"),
	}
}

func (p *Provider) credentialCandidates(acct usageprovider.Account) []string {
	var paths []string
	if acct.CredentialsFile != "" {
		paths = append(paths, expandHome(acct.CredentialsFile, p.homeDir))
	}
	if home, err := p.homeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".claude", ".credentials.json"))
	}
	return dedupe(paths)
}

func mapUsageResponse(payload usageResponse, acct usageprovider.Account, observedAt time.Time) usageprovider.Snapshot {
	snap := usageprovider.Snapshot{
		Provider:   id,
		AccountID:  firstNonEmpty(acct.ID, acct.Alias, acct.ProviderAccountID, "default"),
		Source:     source,
		ObservedAt: observedAt.UTC(),
		Raw:        map[string]any{},
	}

	addBucket(&snap, "five_hour", payload.FiveHour)
	addBucket(&snap, "seven_day", payload.SevenDay)
	addBucket(&snap, "seven_day_sonnet", payload.SevenDaySonnet)
	addBucket(&snap, "seven_day_opus", payload.SevenDayOpus)
	addBucket(&snap, "seven_day_cowork", payload.SevenDayCowork)
	addBucket(&snap, "seven_day_oauth_apps", payload.SevenDayOAuthApps)
	addBucket(&snap, "extra_usage", payload.ExtraUsage)

	return snap
}

func addBucket(snap *usageprovider.Snapshot, name string, bucket *usageBucket) {
	if bucket == nil || bucket.Utilization == nil {
		return
	}
	var resetAt *time.Time
	if strings.TrimSpace(bucket.ResetsAt) != "" {
		parsed, err := time.Parse(time.RFC3339, bucket.ResetsAt)
		if err == nil {
			t := parsed.UTC()
			resetAt = &t
		}
	}
	win, ok := usageprovider.NewWindow(name, usageprovider.WindowOptions{
		UsedPercent: bucket.Utilization,
		ResetAt:     resetAt,
	})
	if !ok {
		return
	}
	snap.Windows = append(snap.Windows, win)
}

func accessTokenFromJSON(data []byte) (string, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return "", err
	}
	return findAccessToken(root), nil
}

func accessTokenFromSecret(secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", nil
	}
	if strings.HasPrefix(secret, "{") || strings.HasPrefix(secret, "[") {
		return accessTokenFromJSON([]byte(secret))
	}
	return secret, nil
}

func findAccessToken(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for key, inner := range v {
			if isAccessTokenKey(key) {
				if token, ok := inner.(string); ok {
					return strings.TrimSpace(token)
				}
			}
		}
		for _, inner := range v {
			if token := findAccessToken(inner); token != "" {
				return token
			}
		}
	case []any:
		for _, inner := range v {
			if token := findAccessToken(inner); token != "" {
				return token
			}
		}
	}
	return ""
}

func isAccessTokenKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	return key == "accesstoken" || key == "oauthaccesstoken"
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

func (p *Provider) keychainAccessToken() (string, error) {
	if p.KeychainToken != nil {
		return p.KeychainToken()
	}
	secret, err := readKeychainSecret()
	if err != nil {
		return "", err
	}
	return accessTokenFromSecret(secret)
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

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
