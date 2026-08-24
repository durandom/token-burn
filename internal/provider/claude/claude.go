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
	HTTPClient      *http.Client
	BaseURL         string
	RefreshURL      string
	Now             func() time.Time
	HomeDir         func() (string, error)
	Env             func(string) string
	KeychainToken   func() (string, error)
	KeychainAccount func() (string, error)
	KeychainWrite   func(account, secret string) error
}

func New() *Provider {
	return &Provider{}
}

func (p *Provider) ID() string {
	return id
}

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	cred, err := p.resolveCredential(acct)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}

	// Refresh ahead of expiry when the stored login says it is due. Claude Code
	// normally keeps its own token fresh, so this only matters while it sits
	// idle - which is exactly when an unattended monitor would otherwise go
	// dark.
	attemptedRefresh := false
	if cred.canRefresh() && cred.needsRefresh(observedAt, refreshSkew) {
		attemptedRefresh = true
		rotated, refreshErr := p.refreshCredential(ctx, cred, observedAt)
		switch {
		case refreshErr == nil:
			cred = rotated
		case cred.isExpired(observedAt):
			// The token is already dead, so the failed refresh is the real
			// cause. Falling through would produce a 401 and report
			// auth_expired for what may well be a rate limit or an outage,
			// which sends the user to re-login for no reason and denies the
			// daemon the backoff signal it needs.
			return usageprovider.Snapshot{}, refreshErr
		}
		// Otherwise the token is inside the refresh skew but still valid, so
		// the current one is good for this poll.
	}

	payload, err := p.fetchUsage(ctx, cred.Access)
	if err != nil && isAuthExpired(err) && cred.canRefresh() && !attemptedRefresh {
		rotated, refreshErr := p.refreshCredential(ctx, cred, observedAt)
		if refreshErr == nil {
			cred = rotated
			payload, err = p.fetchUsage(ctx, cred.Access)
		}
	}
	if err != nil {
		return usageprovider.Snapshot{}, err
	}

	return mapUsageResponse(payload, acct, observedAt), nil
}

func (p *Provider) fetchUsage(ctx context.Context, token string) (usageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+"/usage", nil)
	if err != nil {
		return usageResponse{}, fmt.Errorf("claude create usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "token-burn")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return usageResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrTransientHTTPFailure,
			Provider: id,
			Err:      err,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usageResponse{}, fmt.Errorf("claude read usage response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return usageResponse{}, &usageprovider.Error{
			Code:       usageprovider.ErrAuthExpired,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return usageResponse{}, &usageprovider.Error{
			Code:       usageprovider.ErrRateLimited,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return usageResponse{}, &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("unexpected status: %s", truncate(string(body), 256)),
		}
	}

	var payload usageResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return usageResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      err,
		}
	}
	return payload, nil
}

func isAuthExpired(err error) bool {
	var perr *usageprovider.Error
	return errors.As(err, &perr) && perr.Code == usageprovider.ErrAuthExpired
}

// resolveCredential returns the first usable Claude login, preferring an
// explicit environment token, then the credentials file, then the macOS
// Keychain.
func (p *Provider) resolveCredential(acct usageprovider.Account) (credential, error) {
	if token := strings.TrimSpace(p.env("CLAUDE_CODE_OAUTH_TOKEN")); token != "" {
		return credential{Access: token, source: credentialSource{kind: sourceEnv}}, nil
	}
	for _, path := range p.credentialCandidates(acct) {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return credential{}, fmt.Errorf("claude read credentials file %s: %w", path, err)
		}
		cred, ok, err := credentialFromJSON(data)
		if err != nil {
			return credential{}, &usageprovider.Error{
				Code:     usageprovider.ErrInvalidResponse,
				Provider: id,
				Err:      fmt.Errorf("parse claude credentials file %s: %w", path, err),
			}
		}
		if ok {
			cred.source = credentialSource{kind: sourceFile, path: path}
			return cred, nil
		}
	}

	secret, err := p.keychainSecret()
	if err != nil {
		return credential{}, err
	}
	cred, ok, err := credentialFromSecret(secret)
	if err != nil {
		return credential{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      errors.New("parse claude credentials from macOS Keychain"),
		}
	}
	if ok {
		cred.source = credentialSource{kind: sourceKeychain}
		return cred, nil
	}

	return credential{}, &usageprovider.Error{
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
	addScopedLimits(&snap, payload.Limits)

	return snap
}

// addScopedLimits maps model-scoped weekly limits from the limits array. The
// session and weekly_all entries duplicate the legacy five_hour/seven_day
// buckets, so only weekly_scoped entries produce additional windows.
func addScopedLimits(snap *usageprovider.Snapshot, limits []usageLimit) {
	seen := map[string]struct{}{}
	for _, win := range snap.Windows {
		seen[win.Name] = struct{}{}
	}
	for _, limit := range limits {
		if limit.Kind != "weekly_scoped" || limit.Percent == nil {
			continue
		}
		name := "seven_day_" + scopeSlug(limit.Scope)
		if _, ok := seen[name]; ok {
			continue
		}
		var resetAt *time.Time
		if strings.TrimSpace(limit.ResetsAt) != "" {
			if parsed, err := time.Parse(time.RFC3339, limit.ResetsAt); err == nil {
				t := parsed.UTC()
				resetAt = &t
			}
		}
		win, ok := usageprovider.NewWindow(name, usageprovider.WindowOptions{
			UsedPercent:   limit.Percent,
			ResetAt:       resetAt,
			WindowSeconds: windowSecondsForName(name),
		})
		if !ok {
			continue
		}
		seen[name] = struct{}{}
		snap.Windows = append(snap.Windows, win)
	}
}

func scopeSlug(scope *limitScope) string {
	label := ""
	if scope != nil && scope.Model != nil {
		label = firstNonEmpty(scope.Model.DisplayName, scope.Model.ID)
	}
	if label == "" {
		return "scoped"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore && b.Len() > 0:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

func addBucket(snap *usageprovider.Snapshot, name string, bucket *usageBucket) {
	if bucket == nil || bucket.Utilization == nil {
		return
	}
	if name == "extra_usage" && !bucket.extraUsageEnabled() {
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
		UsedPercent:   bucket.Utilization,
		ResetAt:       resetAt,
		WindowSeconds: windowSecondsForName(name),
	})
	if !ok {
		return
	}
	snap.Windows = append(snap.Windows, win)
}

func windowSecondsForName(name string) *int {
	seconds := 0
	switch {
	case name == "five_hour":
		seconds = 5 * 60 * 60
	case name == "seven_day" || strings.HasPrefix(name, "seven_day_"):
		seconds = 7 * 24 * 60 * 60
	default:
		return nil
	}
	return &seconds
}

func (b *usageBucket) extraUsageEnabled() bool {
	if b == nil {
		return false
	}
	if b.IsEnabled != nil {
		return *b.IsEnabled
	}
	return b.MonthlyLimit != nil || b.UsedCredits != nil
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

func (p *Provider) refreshURL() string {
	if p.RefreshURL != "" {
		return p.RefreshURL
	}
	return defaultRefreshURL
}

func (p *Provider) keychainSecret() (string, error) {
	if p.KeychainToken != nil {
		return p.KeychainToken()
	}
	return readKeychainSecret()
}

func (p *Provider) keychainAccount() (string, error) {
	if p.KeychainAccount != nil {
		return p.KeychainAccount()
	}
	return keychainAccount()
}

func (p *Provider) keychainWrite(account, secret string) error {
	if p.KeychainWrite != nil {
		return p.KeychainWrite(account, secret)
	}
	return writeKeychainSecret(account, secret)
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
