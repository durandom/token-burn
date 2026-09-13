package zai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/piauth"
	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	id               = "zai"
	cnID             = "zai-coding-cn"
	source           = "zai_monitor_quota"
	defaultBaseURL   = "https://api.z.ai"
	cnBaseURL        = "https://open.bigmodel.cn"
	defaultAuthID    = "zai"
	cnAuthID         = "zai-coding-cn"
	quotaPath        = "/api/monitor/usage/quota/limit"
	maxResponseBytes = 64 << 10
	// Z.AI resets buckets relative to subscription start, not wall clock,
	// so nextResetTime is authoritative and window seconds are the
	// semantic unit/number length. Cap stored durations defensively.
	maxUsageWindowDuration = 366 * 24 * time.Hour
)

// Provider reads the GLM Coding Plan quota from Z.AI's undocumented monitor
// endpoint using the API key Pi stores in auth.json.
type Provider struct {
	HTTPClient *http.Client
	BaseURL    string
	// AuthID selects which auth.json entry to read ("zai" or
	// "zai-coding-cn"). It doubles as the provider id reported in
	// snapshots.
	AuthID  string
	Now     func() time.Time
	HomeDir func() (string, error)
	Env     func(string) string
}

// New returns a provider for the global region (api.z.ai).
func New() *Provider { return &Provider{} }

// NewCN returns a provider for the China region (open.bigmodel.cn). The
// regional key Pi stores under "zai-coding-cn" is not interchangeable with
// the global one.
func NewCN() *Provider {
	return &Provider{BaseURL: cnBaseURL, AuthID: cnAuthID}
}

func (p *Provider) ID() string { return p.authID() }

func (p *Provider) Fetch(ctx context.Context, acct usageprovider.Account) (usageprovider.Snapshot, error) {
	observedAt := p.now()
	key, err := p.readAPIKey(ctx, acct)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	payload, err := p.fetchQuota(ctx, key)
	if err != nil {
		return usageprovider.Snapshot{}, err
	}
	return mapQuota(p.ID(), payload, acct, observedAt)
}

func (p *Provider) fetchQuota(ctx context.Context, key string) (envelope, error) {
	var payload envelope
	if err := p.getJSON(ctx, quotaPath, map[string]string{
		// Raw key without a Bearer prefix, matching the official
		// dashboard XHR. The endpoint also accepts Bearer.
		"Authorization": key,
	}, &payload); err != nil {
		return envelope{}, err
	}
	return payload, nil
}

func (p *Provider) getJSON(ctx context.Context, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+endpoint, nil)
	if err != nil {
		return fmt.Errorf("zai create usage request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: p.ID(), Err: safeTransportError(err)}
	}
	defer resp.Body.Close()
	if err := classifyHTTPStatus(p.ID(), resp.StatusCode); err != nil {
		return err
	}
	body, err := readBounded(resp.Body)
	if err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: p.ID(), Err: err}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: p.ID(), Err: errors.New("malformed JSON")}
	}
	return nil
}

func classifyHTTPStatus(providerID string, status int) error {
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &usageprovider.Error{Code: usageprovider.ErrAuthExpired, Provider: providerID, HTTPStatus: status}
	case status == http.StatusTooManyRequests:
		return &usageprovider.Error{Code: usageprovider.ErrRateLimited, Provider: providerID, HTTPStatus: status}
	default:
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: providerID, HTTPStatus: status}
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

// mapQuota turns the monitor envelope into snapshot windows: the 5-hour
// and weekly token/credit buckets, plus the monthly MCP TIME_LIMIT bucket
// when present. Per-bucket diagnostics are recorded in Raw.
func mapQuota(providerID string, payload envelope, acct usageprovider.Account, observedAt time.Time) (usageprovider.Snapshot, error) {
	if !payload.Success {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: providerID,
			Err:      errors.New("no active Z.AI coding plan quota (subscription inactive or free)"),
		}
	}

	seen := map[string]bool{}
	var windows []usageprovider.Window
	raw := map[string]any{}
	if strings.TrimSpace(payload.Data.Level) != "" {
		raw["level"] = payload.Data.Level
	}

	for _, item := range payload.Data.Limits {
		var name string
		if item.Type == limitTypeTime {
			// The monthly MCP-tool bucket arrives without a unit/number
			// pair; its identity comes from the type alone.
			name = mcpMonthlyWindow
		} else {
			name, _ = bucketName(item.Unit, item.Number)
		}
		if name == "" || seen[name] {
			continue
		}
		resetAt := parseMillis(item.NextResetTime)
		used, windowSeconds := usedPercent(item, resetAt, observedAt)
		if used == nil {
			continue
		}
		win, ok := usageprovider.NewWindow(name, usageprovider.WindowOptions{
			UsedPercent:   used,
			ResetAt:       resetAt,
			WindowSeconds: windowSeconds,
		})
		if !ok {
			continue
		}
		seen[name] = true
		windows = append(windows, win)
		addLimitRaw(raw, name, item)
	}
	if len(windows) == 0 {
		return usageprovider.Snapshot{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: providerID,
			Err:      errors.New("quota response has no recognizable usage buckets"),
		}
	}

	accountID := firstNonEmpty(acct.ID, acct.Alias, acct.ProviderAccountID, "zai-default")
	return usageprovider.Snapshot{
		Provider:   providerID,
		AccountID:  accountID,
		PlanType:   strings.TrimSpace(payload.Data.Level),
		Source:     source,
		ObservedAt: observedAt.UTC(),
		Windows:    windows,
		Raw:        raw,
	}, nil
}

// bucketName maps the (unit, number) pair to a window name. unit=3 is
// hours and unit=6 is weeks in the observed payloads; anything else is
// unknown and ignored.
func bucketName(unit, number int) (string, bool) {
	switch {
	case unit == unitHour && number == 5:
		return fiveHourWindow, true
	case unit == unitWeek && number == 1:
		return weeklyWindow, true
	default:
		return "", false
	}
}

func windowSeconds(unit, number int) *int {
	var seconds int
	switch {
	case unit == unitHour:
		seconds = number * 3600
	case unit == unitWeek:
		seconds = number * 7 * 24 * 3600
	default:
		return nil
	}
	if seconds <= 0 {
		return nil
	}
	return &seconds
}

// usedPercent resolves the used share for a bucket. Credit buckets carry
// an integer percentage that the API rounds up, so when a credit cap and
// a consumed value are both present the exact ratio is preferred.
func usedPercent(item limit, resetAt *time.Time, observedAt time.Time) (*float64, *int) {
	used := exactCreditPercent(item)
	if used == nil && item.Percentage != nil && validPercent(*item.Percentage) {
		value := *item.Percentage
		used = &value
	}
	if used == nil {
		return nil, nil
	}
	seconds := windowSeconds(item.Unit, item.Number)
	if seconds == nil && resetAt != nil {
		if duration := resetAt.Sub(observedAt); duration > 0 && duration <= maxUsageWindowDuration {
			value := int(duration / time.Second)
			seconds = &value
		}
	}
	return used, seconds
}

func exactCreditPercent(item limit) *float64 {
	if item.Type != limitTypeCredit && item.Type != limitTypeTokens {
		return nil
	}
	if item.Usage == nil || item.CurrentValue == nil {
		return nil
	}
	cap, used := *item.Usage, *item.CurrentValue
	if cap <= 0 || used < 0 {
		return nil
	}
	value := float64(used) / float64(cap) * 100
	if !validPercent(value) {
		return nil
	}
	return &value
}

func validPercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func addLimitRaw(raw map[string]any, name string, item limit) {
	entry := map[string]any{"type": item.Type, "unit": item.Unit, "number": item.Number}
	if item.Total != nil {
		entry["total"] = *item.Total
	}
	if item.Usage != nil {
		entry["credit_cap"] = *item.Usage
	}
	if item.CurrentValue != nil {
		entry["credit_used"] = *item.CurrentValue
	}
	if item.Remaining != nil {
		entry["credit_remaining"] = *item.Remaining
	}
	if item.Percentage != nil && validPercent(*item.Percentage) {
		entry["reported_percentage"] = *item.Percentage
	}
	if reset := parseMillis(item.NextResetTime); reset != nil {
		entry["reset_ms"] = reset.UnixMilli()
	}
	raw[name] = entry
}

func parseMillis(value *int64) *time.Time {
	if value == nil || *value <= 0 {
		return nil
	}
	t := time.UnixMilli(*value).UTC()
	return &t
}

func (p *Provider) readAPIKey(ctx context.Context, acct usageprovider.Account) (string, error) {
	path := piauth.ResolvePath(acct.AuthFile, p.env, p.homeDir)
	if path == "" {
		return "", &usageprovider.Error{Code: usageprovider.ErrAuthMissing, Provider: p.ID(), Err: errors.New("Pi auth path could not be resolved")}
	}
	store, err := piauth.New(path)
	if err != nil {
		return "", authReadError(p.ID(), err)
	}
	credential, err := store.Read(ctx, p.authID())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, piauth.ErrCredentialNotFound) {
			return "", &usageprovider.Error{
				Code:     usageprovider.ErrAuthMissing,
				Provider: p.ID(),
				Err:      fmt.Errorf("Pi %s API key not found; run /login %s in Pi", p.authID(), p.authID()),
			}
		}
		return "", authReadError(p.ID(), err)
	}
	if err := validateCredential(p.authID(), credential); err != nil {
		return "", err
	}
	return credential.Key, nil
}

func validateCredential(authID string, credential credential) error {
	if credential.Type != "api_key" {
		return &usageprovider.Error{
			Code:     usageprovider.ErrAuthMissing,
			Provider: authID,
			Err:      fmt.Errorf("Pi %s credentials must be an API key (type \"api_key\"); run /login %s in Pi", authID, authID),
		}
	}
	if strings.TrimSpace(credential.Key) == "" {
		return &usageprovider.Error{
			Code:     usageprovider.ErrAuthMissing,
			Provider: authID,
			Err:      fmt.Errorf("Pi %s API key is missing; run /login %s in Pi", authID, authID),
		}
	}
	return nil
}

func authReadError(providerID string, err error) error {
	var providerErr *usageprovider.Error
	if errors.As(err, &providerErr) {
		return err
	}
	if errors.Is(err, piauth.ErrLockUnavailable) || errors.Is(err, piauth.ErrLockCompromised) {
		return &usageprovider.Error{Code: usageprovider.ErrTransientHTTPFailure, Provider: providerID, Err: err}
	}
	return &usageprovider.Error{Code: usageprovider.ErrInvalidResponse, Provider: providerID, Err: err}
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

func (p *Provider) authID() string {
	if id := strings.TrimSpace(p.AuthID); id != "" {
		return id
	}
	return defaultAuthID
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
