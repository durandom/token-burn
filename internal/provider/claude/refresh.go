package claude

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
	"strings"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

const (
	defaultRefreshURL = "https://platform.claude.com/v1/oauth/token"
	oauthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// refreshSkew refreshes slightly before expiry so a poll never races the
	// boundary and reports a spurious auth failure.
	refreshSkew          = 5 * time.Minute
	maxTokenLifetimeSecs = 90 * 24 * 60 * 60
	maxRefreshBodyBytes  = 1 << 20
)

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// refreshCredential exchanges the refresh token for a new access token and
// persists the result back to the source it came from.
//
// Persisting is not optional. Anthropic rotates the refresh token on every
// exchange and invalidates the previous one, so a refresh that is not written
// back would leave Claude Code itself holding a dead refresh token — the
// monitor would fix its own polling by signing the user out of the tool it
// monitors. When the write-back cannot be completed the rotation is reported as
// an error rather than used in memory.
func (p *Provider) refreshCredential(ctx context.Context, current credential, now time.Time) (credential, error) {
	if !current.canRefresh() {
		return credential{}, &usageprovider.Error{
			Code:     usageprovider.ErrAuthExpired,
			Provider: id,
			Err:      errors.New("claude oauth token expired and no refresh token is stored; run claude login"),
		}
	}

	payload, err := p.requestRefresh(ctx, current.Refresh)
	if err != nil {
		return credential{}, err
	}

	rotated := current
	rotated.Access = strings.TrimSpace(payload.AccessToken)
	if refreshed := strings.TrimSpace(payload.RefreshToken); refreshed != "" {
		rotated.Refresh = refreshed
	}
	rotated.ExpiresAt = now.Add(time.Duration(payload.ExpiresIn) * time.Second).UnixMilli()
	if scopes := strings.Fields(payload.Scope); len(scopes) > 0 {
		rotated.Scopes = scopes
	}

	if err := p.persistCredential(current, rotated); err != nil {
		return credential{}, err
	}
	return rotated, nil
}

func (p *Provider) requestRefresh(ctx context.Context, refreshToken string) (refreshResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {oauthClientID},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.refreshURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return refreshResponse{}, fmt.Errorf("claude create OAuth refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "token-burn")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return refreshResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrTransientHTTPFailure,
			Provider: id,
			Err:      errors.New("claude OAuth refresh request failed"),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRefreshBodyBytes))
	if err != nil {
		return refreshResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      errors.New("claude OAuth refresh response could not be read"),
		}
	}
	switch {
	case resp.StatusCode == http.StatusBadRequest,
		resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusForbidden:
		return refreshResponse{}, &usageprovider.Error{
			Code:       usageprovider.ErrAuthExpired,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
			Err:        errors.New("claude OAuth refresh rejected; run claude login"),
		}
	case resp.StatusCode == http.StatusTooManyRequests:
		return refreshResponse{}, &usageprovider.Error{
			Code:       usageprovider.ErrRateLimited,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
		}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return refreshResponse{}, &usageprovider.Error{
			Code:       usageprovider.ErrTransientHTTPFailure,
			Provider:   id,
			HTTPStatus: resp.StatusCode,
		}
	}

	var payload refreshResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return refreshResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      errors.New("claude OAuth refresh returned malformed JSON"),
		}
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 || payload.ExpiresIn > maxTokenLifetimeSecs {
		return refreshResponse{}, &usageprovider.Error{
			Code:     usageprovider.ErrInvalidResponse,
			Provider: id,
			Err:      errors.New("claude OAuth refresh returned an unusable payload"),
		}
	}
	return payload, nil
}

// persistCredential writes a rotation back to the source it was read from.
//
// The source is re-read first and the write is skipped unless it still holds
// the access token the rotation started from. Neither the Keychain nor the
// credentials file offers a compare-and-swap, so this only narrows the window
// in which a concurrent Claude Code refresh could be overwritten; it does not
// close it. Losing that race costs one redundant refresh, not a broken login,
// because the winner's tokens stay intact.
func (p *Provider) persistCredential(previous, rotated credential) error {
	switch previous.source.kind {
	case sourceFile:
		return p.persistToFile(previous, rotated)
	case sourceKeychain:
		return p.persistToKeychain(previous, rotated)
	default:
		return &usageprovider.Error{
			Code:     usageprovider.ErrAuthExpired,
			Provider: id,
			Err:      errors.New("claude oauth token expired and the credential source is read-only"),
		}
	}
}

func (p *Provider) persistToFile(previous, rotated credential) error {
	path := previous.source.path
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("claude re-read credentials file before rotation: %w", err)
	}
	stored, ok, err := credentialFromJSON(data)
	if err != nil || !ok || stored.Access != previous.Access {
		return errCredentialsChanged
	}

	rotated.blob = stored.blob
	encoded, err := rotated.encode()
	if err != nil {
		return fmt.Errorf("claude encode rotated credentials: %w", err)
	}
	return writeFileAtomic(path, encoded)
}

func (p *Provider) persistToKeychain(previous, rotated credential) error {
	secret, err := p.keychainSecret()
	if err != nil {
		return err
	}
	stored, ok, err := credentialFromSecret(secret)
	if err != nil || !ok || stored.Access != previous.Access {
		return errCredentialsChanged
	}

	rotated.blob = stored.blob
	encoded, err := rotated.encode()
	if err != nil {
		return fmt.Errorf("claude encode rotated credentials: %w", err)
	}
	account, err := p.keychainAccount()
	if err != nil {
		return err
	}
	if err := p.keychainWrite(account, string(encoded)); err != nil {
		// A rejected write may have left a partial item behind. Put the content
		// that was just read and verified back, so a failed rotation degrades to
		// "token-burn is stale" rather than "the Keychain item is corrupt".
		if restoreErr := p.keychainWrite(account, secret); restoreErr != nil {
			return fmt.Errorf("%w (restoring the previous credentials also failed: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

var errCredentialsChanged = &usageprovider.Error{
	Code:     usageprovider.ErrTransientHTTPFailure,
	Provider: id,
	Err:      errors.New("claude credentials changed during refresh; retrying on next poll"),
}

// writeFileAtomic replaces path via a same-directory temp file so a crash
// cannot leave a partially written credential store behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("claude create temp credentials file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("claude set temp credentials permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("claude write temp credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("claude sync temp credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("claude close temp credentials: %w", err)
	}
	return os.Rename(tmpName, path)
}
