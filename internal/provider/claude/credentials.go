package claude

import (
	"encoding/json"
	"strings"
	"time"
)

// credentialSourceKind identifies where a credential was read from, which also
// decides where a rotated credential may be written back to.
type credentialSourceKind int

const (
	sourceNone credentialSourceKind = iota
	// sourceEnv is CLAUDE_CODE_OAUTH_TOKEN. It carries no refresh token and is
	// never written back.
	sourceEnv
	sourceFile
	sourceKeychain
)

type credentialSource struct {
	kind credentialSourceKind
	path string
}

// credential is the Claude Code OAuth login as token-burn understands it.
//
// blob keeps every top-level key of the container the credential was decoded
// from, so a write-back can replace claudeAiOauth without discarding sibling
// keys. That matters on macOS: Claude Code stores MCP server logins under
// mcpOAuth in the same Keychain item, and dropping them would silently sign the
// user out of every OAuth-authenticated MCP server.
type credential struct {
	Access    string
	Refresh   string
	ExpiresAt int64 // epoch milliseconds; 0 when unknown
	Scopes    []string

	source credentialSource
	blob   map[string]json.RawMessage
}

// claudeOAuth mirrors the claudeAiOauth object Claude Code writes. Fields are
// matched by name, never by a recursive search: the container holds unrelated
// OAuth logins whose access tokens would otherwise be indistinguishable from
// the Claude one.
type claudeOAuth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes,omitempty"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`

	// rest preserves any field Claude Code writes that token-burn does not
	// model, so a rotation write-back is lossless within claudeAiOauth too.
	rest map[string]json.RawMessage
}

const claudeOAuthKey = "claudeAiOauth"

// oauthKnownFields are the claudeAiOauth keys claudeOAuth models directly.
// Anything else is carried through rest.
var oauthKnownFields = map[string]struct{}{
	"accessToken":      {},
	"refreshToken":     {},
	"expiresAt":        {},
	"scopes":           {},
	"subscriptionType": {},
	"rateLimitTier":    {},
}

func (c credential) hasAccess() bool { return strings.TrimSpace(c.Access) != "" }

func (c credential) canRefresh() bool {
	return strings.TrimSpace(c.Refresh) != "" && c.source.kind != sourceEnv && c.source.kind != sourceNone
}

// needsRefresh reports whether the access token is expired or close enough to
// expiry that a poll would race the boundary.
func (c credential) needsRefresh(now time.Time, skew time.Duration) bool {
	if c.ExpiresAt <= 0 {
		return false
	}
	return c.ExpiresAt <= now.Add(skew).UnixMilli()
}

// credentialFromJSON decodes a Claude credential container.
//
// The canonical shape nests the login under claudeAiOauth. A flat container
// holding only an access token is also accepted, because `claude setup-token`
// style credentials and hand-written files use that form. Both lookups are
// key-exact and top-level only.
func credentialFromJSON(data []byte) (credential, bool, error) {
	var blob map[string]json.RawMessage
	if err := json.Unmarshal(data, &blob); err != nil {
		return credential{}, false, err
	}

	if raw, ok := blob[claudeOAuthKey]; ok {
		oauth, err := decodeOAuth(raw)
		if err != nil {
			return credential{}, false, err
		}
		if strings.TrimSpace(oauth.AccessToken) == "" {
			return credential{}, false, nil
		}
		return credential{
			Access:    strings.TrimSpace(oauth.AccessToken),
			Refresh:   strings.TrimSpace(oauth.RefreshToken),
			ExpiresAt: oauth.ExpiresAt,
			Scopes:    oauth.Scopes,
			blob:      blob,
		}, true, nil
	}

	for _, key := range []string{"accessToken", "access_token", "oauthAccessToken", "oauth_access_token"} {
		raw, ok := blob[key]
		if !ok {
			continue
		}
		var token string
		if err := json.Unmarshal(raw, &token); err != nil {
			continue
		}
		if strings.TrimSpace(token) == "" {
			continue
		}
		return credential{Access: strings.TrimSpace(token), blob: blob}, true, nil
	}

	return credential{}, false, nil
}

// credentialFromSecret decodes a Keychain secret, which is either the JSON
// container or a bare access token.
func credentialFromSecret(secret string) (credential, bool, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return credential{}, false, nil
	}
	if strings.HasPrefix(secret, "{") || strings.HasPrefix(secret, "[") {
		return credentialFromJSON([]byte(secret))
	}
	return credential{Access: secret}, true, nil
}

func decodeOAuth(raw json.RawMessage) (claudeOAuth, error) {
	var oauth claudeOAuth
	if err := json.Unmarshal(raw, &oauth); err != nil {
		return claudeOAuth{}, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return claudeOAuth{}, err
	}
	for key, value := range all {
		if _, known := oauthKnownFields[key]; known {
			continue
		}
		if oauth.rest == nil {
			oauth.rest = map[string]json.RawMessage{}
		}
		oauth.rest[key] = value
	}
	return oauth, nil
}

// encode renders the credential back into its original container, replacing
// only the fields a rotation changes and preserving every sibling key.
func (c credential) encode() ([]byte, error) {
	blob := map[string]json.RawMessage{}
	for key, value := range c.blob {
		blob[key] = value
	}

	var oauth claudeOAuth
	if raw, ok := blob[claudeOAuthKey]; ok {
		decoded, err := decodeOAuth(raw)
		if err != nil {
			return nil, err
		}
		oauth = decoded
	}
	oauth.AccessToken = c.Access
	oauth.RefreshToken = c.Refresh
	oauth.ExpiresAt = c.ExpiresAt
	if len(c.Scopes) > 0 {
		oauth.Scopes = c.Scopes
	}

	merged, err := encodeOAuth(oauth)
	if err != nil {
		return nil, err
	}
	blob[claudeOAuthKey] = merged

	// A flat container that has now grown a claudeAiOauth object must not keep
	// a stale top-level token alongside it.
	for _, key := range []string{"accessToken", "access_token", "oauthAccessToken", "oauth_access_token"} {
		delete(blob, key)
	}

	return json.Marshal(blob)
}

func encodeOAuth(oauth claudeOAuth) (json.RawMessage, error) {
	known, err := json.Marshal(struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken,omitempty"`
		ExpiresAt        int64    `json:"expiresAt,omitempty"`
		Scopes           []string `json:"scopes,omitempty"`
		SubscriptionType string   `json:"subscriptionType,omitempty"`
		RateLimitTier    string   `json:"rateLimitTier,omitempty"`
	}{
		AccessToken:      oauth.AccessToken,
		RefreshToken:     oauth.RefreshToken,
		ExpiresAt:        oauth.ExpiresAt,
		Scopes:           oauth.Scopes,
		SubscriptionType: oauth.SubscriptionType,
		RateLimitTier:    oauth.RateLimitTier,
	})
	if err != nil {
		return nil, err
	}
	if len(oauth.rest) == 0 {
		return known, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return nil, err
	}
	for key, value := range oauth.rest {
		fields[key] = value
	}
	return json.Marshal(fields)
}
