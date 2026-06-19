package antigravity

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	oauthTokenKey      = "antigravityUnifiedStateSync.oauthToken"
	oauthTokenSentinel = "oauthTokenInfoSentinelKey"
	keychainService    = "gemini"
	keychainAccount    = "antigravity"
	cacheFileName      = "antigravity-auth.json"
)

type keychainReader func() (string, error)

func (p *Provider) tokenCandidates() ([]tokenCandidate, error) {
	var candidates []tokenCandidate
	if cached, err := p.cachedAccessToken(); err == nil && cached.AccessToken != "" {
		cached.Source = "token_burn_cache"
		candidates = append(candidates, cached)
	} else if err != nil {
		return nil, err
	}
	for _, path := range p.stateDBCandidates() {
		token, err := readTokenFromStateDB(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if token.AccessToken != "" {
			token.Source = "state_db"
			candidates = append(candidates, token)
		}
	}
	if secret, err := p.keychainSecret(); err == nil && strings.TrimSpace(secret) != "" {
		if token := tokenFromKeychainSecret(secret); token.AccessToken != "" || token.RefreshToken != "" {
			token.Source = "keychain"
			candidates = append(candidates, token)
		}
	} else if err != nil {
		return nil, err
	}
	return dedupeTokens(candidates), nil
}

func (p *Provider) stateDBCandidates() []string {
	if p.StateDBPaths != nil {
		return p.StateDBPaths
	}
	home, err := p.homeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, "Library", "Application Support", "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb"),
	}
}

func readTokenFromStateDB(path string) (tokenCandidate, error) {
	if _, err := os.Stat(path); err != nil {
		return tokenCandidate{}, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("open antigravity state db %s: %w", path, err)
	}
	defer db.Close()

	var encoded string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key = ? LIMIT 1", oauthTokenKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return tokenCandidate{}, nil
	}
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("read antigravity oauth state from %s: %w", path, err)
	}
	return decodeStateDBToken(encoded)
}

func decodeStateDBToken(encoded string) (tokenCandidate, error) {
	outer, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("decode antigravity oauth outer envelope: %w", err)
	}
	outerFields, err := readProtoFields(outer)
	if err != nil {
		return tokenCandidate{}, err
	}
	wrapper := outerFields[1].data
	if len(wrapper) == 0 {
		return tokenCandidate{}, nil
	}
	wrapperFields, err := readProtoFields(wrapper)
	if err != nil {
		return tokenCandidate{}, err
	}
	if string(wrapperFields[1].data) != oauthTokenSentinel {
		return tokenCandidate{}, nil
	}
	payload := wrapperFields[2].data
	if len(payload) == 0 {
		return tokenCandidate{}, nil
	}
	payloadFields, err := readProtoFields(payload)
	if err != nil {
		return tokenCandidate{}, err
	}
	innerText := strings.TrimSpace(string(payloadFields[1].data))
	if innerText == "" {
		return tokenCandidate{}, nil
	}
	inner, err := base64.StdEncoding.DecodeString(innerText)
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("decode antigravity oauth inner token: %w", err)
	}
	innerFields, err := readProtoFields(inner)
	if err != nil {
		return tokenCandidate{}, err
	}
	token := tokenCandidate{
		AccessToken:  strings.TrimSpace(string(innerFields[1].data)),
		RefreshToken: strings.TrimSpace(string(innerFields[3].data)),
	}
	if tsBytes := innerFields[4].data; len(tsBytes) > 0 {
		tsFields, err := readProtoFields(tsBytes)
		if err == nil {
			token.ExpirySeconds = int64(tsFields[1].varint)
		}
	}
	return token, nil
}

func (p *Provider) cachedAccessToken() (tokenCandidate, error) {
	path := p.tokenCachePath()
	if path == "" {
		return tokenCandidate{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return tokenCandidate{}, nil
	}
	if err != nil {
		return tokenCandidate{}, fmt.Errorf("read antigravity token cache %s: %w", path, err)
	}
	var cached cachedToken
	if err := json.Unmarshal(data, &cached); err != nil {
		return tokenCandidate{}, nil
	}
	return tokenCandidate{
		AccessToken:   strings.TrimSpace(cached.AccessToken),
		ExpirySeconds: cached.ExpirySeconds,
	}, nil
}

func (p *Provider) cacheAccessToken(token tokenCandidate) {
	path := p.tokenCachePath()
	if path == "" || strings.TrimSpace(token.AccessToken) == "" || token.ExpirySeconds <= 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	data, err := json.Marshal(cachedToken{AccessToken: token.AccessToken, ExpirySeconds: token.ExpirySeconds})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

func (p *Provider) tokenCachePath() string {
	if p.TokenCachePath != "" {
		return p.TokenCachePath
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := p.homeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "token-burn", cacheFileName)
}

type protoField struct {
	wireType int
	varint   uint64
	data     []byte
}

func readProtoFields(data []byte) (map[int]protoField, error) {
	fields := map[int]protoField{}
	for pos := 0; pos < len(data); {
		tag, next, ok := readVarint(data, pos)
		if !ok {
			return fields, fmt.Errorf("parse protobuf tag at byte %d", pos)
		}
		pos = next
		fieldNum := int(tag / 8)
		wireType := int(tag % 8)
		field := protoField{wireType: wireType}
		switch wireType {
		case 0:
			v, next, ok := readVarint(data, pos)
			if !ok {
				return fields, fmt.Errorf("parse protobuf varint field %d", fieldNum)
			}
			field.varint = v
			pos = next
		case 1:
			if pos+8 > len(data) {
				return fields, fmt.Errorf("parse protobuf fixed64 field %d", fieldNum)
			}
			pos += 8
		case 2:
			n, next, ok := readVarint(data, pos)
			if !ok {
				return fields, fmt.Errorf("parse protobuf length field %d", fieldNum)
			}
			pos = next
			if n > uint64(len(data)-pos) {
				return fields, fmt.Errorf("parse protobuf field %d length %d exceeds input", fieldNum, n)
			}
			field.data = append([]byte(nil), data[pos:pos+int(n)]...)
			pos += int(n)
		case 5:
			if pos+4 > len(data) {
				return fields, fmt.Errorf("parse protobuf fixed32 field %d", fieldNum)
			}
			pos += 4
		default:
			return fields, fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
		fields[fieldNum] = field
	}
	return fields, nil
}

func readVarint(data []byte, pos int) (uint64, int, bool) {
	var value uint64
	var shift uint
	for pos < len(data) && shift < 64 {
		b := data[pos]
		pos++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, pos, true
		}
		shift += 7
	}
	return 0, pos, false
}

func accessTokenFromKeychainSecret(secret string) string {
	return tokenFromKeychainSecret(secret).AccessToken
}

func tokenFromKeychainSecret(secret string) tokenCandidate {
	text := strings.TrimSpace(secret)
	if strings.HasPrefix(text, "go-keyring-base64:") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(text, "go-keyring-base64:")))
		if err == nil {
			text = strings.TrimSpace(string(decoded))
		}
	}
	if text == "" {
		return tokenCandidate{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		if s, ok := parsed.(string); ok {
			return tokenCandidate{AccessToken: strings.TrimSpace(s)}
		}
		return tokenCandidate{
			AccessToken:  findTokenValue(parsed, isAccessTokenKey),
			RefreshToken: findTokenValue(parsed, isRefreshTokenKey),
		}
	}
	return tokenCandidate{AccessToken: strings.TrimPrefix(text, "Bearer ")}
}

func findAccessToken(value any) string {
	return findTokenValue(value, isAccessTokenKey)
}

func findTokenValue(value any, matches func(string) bool) string {
	switch v := value.(type) {
	case map[string]any:
		for key, inner := range v {
			if matches(key) {
				if token, ok := inner.(string); ok {
					return strings.TrimSpace(token)
				}
			}
		}
		for _, inner := range v {
			if token := findTokenValue(inner, matches); token != "" {
				return token
			}
		}
	case []any:
		for _, inner := range v {
			if token := findTokenValue(inner, matches); token != "" {
				return token
			}
		}
	}
	return ""
}

func isAccessTokenKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	return normalized == "accesstoken" || normalized == "token" || normalized == "bearertoken"
}

func isRefreshTokenKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	return normalized == "refreshtoken"
}

func (p *Provider) keychainSecret() (string, error) {
	if p.KeychainSecret != nil {
		return p.KeychainSecret()
	}
	if runtime.GOOS != "darwin" {
		return "", nil
	}
	out, err := exec.Command("security", "find-generic-password", "-w", "-s", keychainService, "-a", keychainAccount).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return "", nil
		}
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func dedupeTokens(in []tokenCandidate) []tokenCandidate {
	seen := map[string]bool{}
	var out []tokenCandidate
	for _, token := range in {
		token.AccessToken = strings.TrimSpace(token.AccessToken)
		token.RefreshToken = strings.TrimSpace(token.RefreshToken)
		key := token.AccessToken
		if key == "" {
			if token.RefreshToken == "" {
				continue
			}
			key = "refresh:" + token.RefreshToken
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, token)
	}
	return out
}

func expiryFromNow(now time.Time, expiresIn int64) int64 {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return now.UTC().Add(time.Duration(expiresIn) * time.Second).Unix()
}
