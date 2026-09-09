package oauthrefresh

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	iat := now.Add(-6 * 24 * time.Hour)
	exp := now.Add(4 * 24 * time.Hour)
	halfJWT := testJWT(t, iat, exp)
	freshJWT := testJWT(t, now.Add(-24*time.Hour), now.Add(9*24*time.Hour))

	tests := []struct {
		name    string
		access  string
		expires int64
		want    bool
	}{
		{name: "no expiry", access: "opaque", want: false},
		{name: "inside skew", expires: now.Add(4 * time.Minute).UnixMilli(), want: true},
		{name: "already expired", expires: now.Add(-time.Minute).UnixMilli(), want: true},
		{name: "opaque far from expiry", access: "opaque", expires: now.Add(4 * 24 * time.Hour).UnixMilli(), want: false},
		{name: "jwt past half ttl", access: halfJWT, expires: exp.UnixMilli(), want: true},
		{name: "jwt past half ttl without stored expires", access: halfJWT, want: true},
		{name: "jwt under half ttl", access: freshJWT, expires: now.Add(9 * 24 * time.Hour).UnixMilli(), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsRefresh(now, tt.access, tt.expires, skew); got != tt.want {
				t.Fatalf("NeedsRefresh() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	if Expired(now, "opaque", 0) {
		t.Fatal("opaque token without expires should not be expired")
	}
	if !Expired(now, "opaque", now.Add(-time.Second).UnixMilli()) {
		t.Fatal("stored expiry in the past")
	}
	token := testJWT(t, now.Add(-2*time.Hour), now.Add(-time.Minute))
	if !Expired(now, token, 0) {
		t.Fatal("jwt exp in the past")
	}
	live := testJWT(t, now.Add(-time.Hour), now.Add(time.Hour))
	if Expired(now, live, 0) {
		t.Fatal("live jwt")
	}
}

func testJWT(t *testing.T, iat, exp time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"iat": iat.Unix(), "exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}
