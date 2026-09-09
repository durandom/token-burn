package oauthrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	defaultMaxBodyBytes = 1 << 20
	maxErrorDetailChars = 256
)

// Config describes a refresh_token exchange against an OAuth token endpoint.
type Config struct {
	Provider     string
	TokenURL     string
	ClientID     string
	ClientSecret string
	ReloginHint  string
	HTTPClient   *http.Client
	MaxBodyBytes int64
	UserAgent    string
}

// Token is the usable subset of a token-endpoint success body.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	HasExpiresIn bool
}

// Refresh exchanges refreshToken for a new access token.
func Refresh(ctx context.Context, cfg Config, refreshToken string) (Token, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return Token{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthExpired,
			Provider: cfg.Provider,
			Err:      errors.New(reloginMessage(cfg, "OAuth refresh token is missing")),
		}
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cfg.ClientID},
		"refresh_token": {refreshToken},
	}
	if strings.TrimSpace(cfg.ClientSecret) != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("create OAuth refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if ua := strings.TrimSpace(cfg.UserAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	} else {
		req.Header.Set("User-Agent", "token-burn")
	}

	resp, err := httpClient(cfg).Do(req)
	if err != nil {
		return Token{}, &usageprovider.Error{
			Code:     usageprovider.ErrTransientHTTPFailure,
			Provider: cfg.Provider,
			Err:      errors.New("OAuth refresh request failed"),
		}
	}
	defer resp.Body.Close()

	limit := cfg.MaxBodyBytes
	if limit <= 0 {
		limit = defaultMaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return Token{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: cfg.Provider,
			Err:      errors.New("invalid OAuth refresh response"),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Token{}, classify(cfg, resp.StatusCode, body, refreshToken)
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    *int64 `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Token{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: cfg.Provider,
			Err:      errors.New("OAuth refresh returned malformed JSON"),
		}
	}
	access := strings.TrimSpace(payload.AccessToken)
	if access == "" {
		return Token{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: cfg.Provider,
			Err:      errors.New("OAuth refresh response missing access token"),
		}
	}
	token := Token{
		AccessToken:  access,
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
	}
	if payload.ExpiresIn != nil {
		token.HasExpiresIn = true
		token.ExpiresIn = *payload.ExpiresIn
	}
	return token, nil
}

func httpClient(cfg Config) *http.Client {
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func classify(cfg Config, status int, body []byte, refreshToken string) error {
	kind, desc := parseOAuthError(body)
	kind = redactToken(kind, refreshToken)
	desc = redactToken(desc, refreshToken)
	switch status {
	case http.StatusTooManyRequests:
		return &usageprovider.Error{
			Code:       usageprovider.ErrRateLimited,
			Provider:   cfg.Provider,
			HTTPStatus: status,
		}
	case http.StatusBadRequest:
		if deadGrant(kind, desc) {
			return &usageprovider.Error{
				Code:       usageprovider.ErrAuthExpired,
				Provider:   cfg.Provider,
				HTTPStatus: status,
				Err:        errors.New(reloginMessage(cfg, deadGrantMessage(kind))),
			}
		}
		return &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   cfg.Provider,
			HTTPStatus: status,
			Err:        fmt.Errorf("OAuth refresh rejected the request: %s", errorDetail(kind, desc)),
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		detail := deadGrantMessage(kind)
		if kind == "" && desc == "" {
			detail = "OAuth refresh rejected"
		}
		return &usageprovider.Error{
			Code:       usageprovider.ErrAuthExpired,
			Provider:   cfg.Provider,
			HTTPStatus: status,
			Err:        errors.New(reloginMessage(cfg, detail)),
		}
	default:
		return &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   cfg.Provider,
			HTTPStatus: status,
		}
	}
}

func parseOAuthError(body []byte) (kind, description string) {
	var payload struct {
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Type             string          `json:"type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", ""
	}
	description = strings.TrimSpace(payload.ErrorDescription)
	if len(payload.Error) == 0 {
		return strings.TrimSpace(payload.Type), description
	}
	var plain string
	if err := json.Unmarshal(payload.Error, &plain); err == nil {
		return strings.TrimSpace(plain), description
	}
	var nested struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload.Error, &nested); err != nil {
		return "", description
	}
	if description == "" {
		description = strings.TrimSpace(nested.Message)
	}
	return strings.TrimSpace(nested.Type), description
}

func deadGrant(kind, description string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "invalid_grant", "invalid_state":
		return true
	}
	return strings.Contains(strings.ToLower(description), "refresh token")
}

func deadGrantMessage(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "OAuth refresh token is no longer valid"
	}
	return "OAuth refresh token is no longer valid (" + kind + ")"
}

func reloginMessage(cfg Config, detail string) string {
	hint := strings.TrimSpace(cfg.ReloginHint)
	if hint == "" {
		return detail
	}
	return detail + "; " + hint
}

func errorDetail(kind, description string) string {
	detail := strings.TrimSpace(kind + " " + description)
	if detail == "" {
		detail = "no error detail"
	}
	return truncate(detail, maxErrorDetailChars)
}

func redactToken(text, token string) string {
	if strings.TrimSpace(token) == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "<redacted>")
}

func truncate(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max]
}
