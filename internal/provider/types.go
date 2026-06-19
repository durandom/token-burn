package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

type Account struct {
	Provider          string
	ID                string
	Alias             string
	ProviderAccountID string
	AuthFile          string
	CredentialsFile   string
}

type Snapshot struct {
	Provider   string
	AccountID  string
	PlanType   string
	Source     string
	ObservedAt time.Time
	Windows    []Window
	Raw        map[string]any
}

type Window struct {
	Name             string
	UsedPercent      float64
	RemainingPercent *float64
	ResetAt          *time.Time
	WindowSeconds    *int
	LimitReached     bool
}

type WindowOptions struct {
	UsedPercent      *float64
	RemainingPercent *float64
	ResetAt          *time.Time
	WindowSeconds    *int
	LimitReached     bool
}

type Provider interface {
	ID() string
	Fetch(ctx context.Context, acct Account) (Snapshot, error)
}

type ErrorCode string

const (
	ErrAuthMissing             ErrorCode = "auth_missing"
	ErrAuthExpired             ErrorCode = "auth_expired"
	ErrRateLimited             ErrorCode = "rate_limited"
	ErrTransientHTTPFailure    ErrorCode = "transient_http_failure"
	ErrInvalidResponse         ErrorCode = "invalid_response"
	ErrUnsupportedAccountShape ErrorCode = "unsupported_account_shape"
)

type Error struct {
	Code       ErrorCode
	Provider   string
	HTTPStatus int
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	msg := string(e.Code)
	if e.Provider != "" {
		msg = e.Provider + ": " + msg
	}
	if e.HTTPStatus != 0 {
		msg = fmt.Sprintf("%s: HTTP %d", msg, e.HTTPStatus)
	}
	if e.Err != nil {
		msg = msg + ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewWindow(name string, opts WindowOptions) (Window, bool) {
	used, ok := ResolveUsedPercent(opts.UsedPercent, opts.RemainingPercent)
	if !ok {
		return Window{}, false
	}

	var remaining *float64
	if opts.RemainingPercent != nil {
		v := ClampPercent(*opts.RemainingPercent)
		remaining = &v
	} else {
		v := ClampPercent(100 - used)
		remaining = &v
	}

	return Window{
		Name:             NormalizeWindowName(name),
		UsedPercent:      used,
		RemainingPercent: remaining,
		ResetAt:          opts.ResetAt,
		WindowSeconds:    opts.WindowSeconds,
		LimitReached:     opts.LimitReached || used >= 100,
	}, true
}

func ResolveUsedPercent(used, remaining *float64) (float64, bool) {
	if used != nil {
		return ClampPercent(*used), true
	}
	if remaining != nil {
		return ClampPercent(100 - *remaining), true
	}
	return 0, false
}

func ClampPercent(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

var nonWindowName = regexp.MustCompile(`[^a-z0-9]+`)

func NormalizeWindowName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = nonWindowName.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return "unknown"
	}
	return name
}

func ParseReset(value any, observedAt time.Time) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}

	var t time.Time
	switch v := value.(type) {
	case time.Time:
		t = v
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return nil, fmt.Errorf("parse reset timestamp: %w", err)
		}
		t = parsed
	case int:
		t = time.Unix(int64(v), 0)
	case int64:
		t = time.Unix(v, 0)
	case float64:
		t = time.Unix(int64(v), 0)
	case json.Number:
		seconds, err := v.Int64()
		if err != nil {
			return nil, fmt.Errorf("parse reset unix timestamp: %w", err)
		}
		t = time.Unix(seconds, 0)
	default:
		return nil, fmt.Errorf("unsupported reset timestamp type %T", value)
	}

	if observedAt.Location() == nil {
		observedAt = observedAt.UTC()
	}
	t = t.UTC()
	return &t, nil
}

func ParseResetAfterSeconds(seconds int, observedAt time.Time) *time.Time {
	if seconds <= 0 {
		return nil
	}
	t := observedAt.UTC().Add(time.Duration(seconds) * time.Second)
	return &t
}
