package oauthrefresh

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const maxJWTPayloadBytes = 8 << 10

// NeedsRefresh reports whether an access token should be exchanged now.
//
// Tokens at or inside skew of expiry always refresh. When the access token is
// a JWT with iat and exp, refresh also starts once half of that lifetime has
// elapsed. Stored expires (unix millis) is treated as exp when present.
func NeedsRefresh(now time.Time, access string, expiresMillis int64, skew time.Duration) bool {
	now = now.UTC()
	iat, exp := lifetimeBounds(access, expiresMillis)
	if exp.IsZero() {
		return false
	}
	if !exp.After(now.Add(skew)) {
		return true
	}
	if iat.IsZero() || !exp.After(iat) {
		return false
	}
	return !now.Before(iat.Add(exp.Sub(iat) / 2))
}

// Expired reports whether the access token is past its stated expiry.
func Expired(now time.Time, access string, expiresMillis int64) bool {
	now = now.UTC()
	if expiresMillis > 0 && expiresMillis <= now.UnixMilli() {
		return true
	}
	_, exp, ok := jwtTimes(access)
	return ok && !exp.IsZero() && !exp.After(now)
}

func lifetimeBounds(access string, expiresMillis int64) (iat, exp time.Time) {
	jwtIat, jwtExp, ok := jwtTimes(access)
	if ok {
		iat, exp = jwtIat, jwtExp
	}
	if expiresMillis > 0 {
		stored := time.UnixMilli(expiresMillis).UTC()
		if exp.IsZero() || stored.Before(exp) {
			exp = stored
		}
	}
	return iat, exp
}

func jwtTimes(access string) (iat, exp time.Time, ok bool) {
	parts := strings.Split(access, ".")
	if len(parts) != 3 || parts[1] == "" {
		return time.Time{}, time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
	}
	if len(payload) == 0 || len(payload) > maxJWTPayloadBytes {
		return time.Time{}, time.Time{}, false
	}
	var claims struct {
		Iat json.Number `json:"iat"`
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, time.Time{}, false
	}
	iat, iatOK := unixClaim(claims.Iat)
	exp, expOK := unixClaim(claims.Exp)
	if !iatOK && !expOK {
		return time.Time{}, time.Time{}, false
	}
	return iat, exp, true
}

func unixClaim(value json.Number) (time.Time, bool) {
	if strings.TrimSpace(value.String()) == "" {
		return time.Time{}, false
	}
	seconds, err := value.Int64()
	if err != nil {
		f, ferr := value.Float64()
		if ferr != nil || f <= 0 {
			return time.Time{}, false
		}
		seconds = int64(f)
	}
	if seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}
