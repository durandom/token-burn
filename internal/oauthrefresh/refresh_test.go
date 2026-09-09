package oauthrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func TestRefreshPersistsRotatedTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "client" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Errorf("form = %#v", r.Form)
		}
		if r.Header.Get("User-Agent") != "token-burn" {
			t.Errorf("user-agent = %q", r.Header.Get("User-Agent"))
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	token, err := Refresh(context.Background(), Config{
		Provider:   "codex",
		TokenURL:   server.URL,
		ClientID:   "client",
		HTTPClient: server.Client(),
	}, "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "new-access" || token.RefreshToken != "new-refresh" || !token.HasExpiresIn || token.ExpiresIn != 3600 {
		t.Fatalf("token = %#v", token)
	}
}

func TestRefreshClassification(t *testing.T) {
	secret := "sk-secret-refresh"
	tests := []struct {
		name   string
		status int
		body   string
		code   usageprovider.ErrorCode
		want   []string
		leak   string
	}{
		{
			name:   "invalid_state",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_state","error_description":"state sk-secret-refresh"}`,
			code:   usageprovider.ErrAuthExpired,
			want:   []string{"invalid_state", "/login openai-codex"},
			leak:   secret,
		},
		{
			name:   "invalid_grant",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_grant"}`,
			code:   usageprovider.ErrAuthExpired,
			want:   []string{"invalid_grant", "/login openai-codex"},
		},
		{
			name:   "unknown 400 is transient",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_client"}`,
			code:   usageprovider.ErrTransientHTTPFailure,
			want:   []string{"invalid_client"},
		},
		{
			name:   "401 surfaces oauth error",
			status: http.StatusUnauthorized,
			body:   `{"error":"invalid_grant"}`,
			code:   usageprovider.ErrAuthExpired,
			want:   []string{"invalid_grant", "/login openai-codex"},
		},
		{
			name:   "429",
			status: http.StatusTooManyRequests,
			body:   `{"error":"slow_down"}`,
			code:   usageprovider.ErrRateLimited,
		},
		{
			name:   "nested anthropic invalid_grant",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_grant","message":"bad refresh"}}`,
			code:   usageprovider.ErrAuthExpired,
			want:   []string{"invalid_grant"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			_, err := Refresh(context.Background(), Config{
				Provider:    "codex",
				TokenURL:    server.URL,
				ClientID:    "client",
				ReloginHint: "run /login openai-codex",
				HTTPClient:  server.Client(),
			}, secret)
			var perr *usageprovider.Error
			if !errors.As(err, &perr) || perr.Code != tt.code {
				t.Fatalf("error = %v, want %s", err, tt.code)
			}
			got := err.Error()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("error %q missing %q", got, want)
				}
			}
			if tt.leak != "" && strings.Contains(got, tt.leak) {
				t.Fatalf("error leaked token: %v", err)
			}
		})
	}
}

func TestRefreshOmitsExpiresInWhenMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"new-access"}`)
	}))
	defer server.Close()
	token, err := Refresh(context.Background(), Config{TokenURL: server.URL, HTTPClient: server.Client()}, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	if token.HasExpiresIn || token.AccessToken != "new-access" {
		t.Fatalf("token = %#v", token)
	}
}

func TestRefreshRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{`)
	}))
	defer server.Close()
	_, err := Refresh(context.Background(), Config{Provider: "xai", TokenURL: server.URL, HTTPClient: server.Client()}, "refresh")
	var perr *usageprovider.Error
	if !errors.As(err, &perr) || perr.Code != usageprovider.ErrInvalidResponse {
		t.Fatalf("error = %v", err)
	}
}

func TestParseOAuthErrorShapes(t *testing.T) {
	kind, desc := parseOAuthError([]byte(`{"error":"invalid_state","error_description":"nope"}`))
	if kind != "invalid_state" || desc != "nope" {
		t.Fatalf("rfc = %q %q", kind, desc)
	}
	kind, desc = parseOAuthError([]byte(`{"error":{"type":"invalid_grant","message":"bad"}}`))
	if kind != "invalid_grant" || desc != "bad" {
		t.Fatalf("nested = %q %q", kind, desc)
	}
}

func TestRefreshSendsClientSecretWhenSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_secret") != "secret" {
			t.Errorf("form = %#v", r.Form)
		}
		fmt.Fprint(w, `{"access_token":"a"}`)
	}))
	defer server.Close()
	if _, err := Refresh(context.Background(), Config{
		TokenURL:     server.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		HTTPClient:   server.Client(),
	}, "refresh"); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshJSONRoundTripDoesNotKeepRawBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"access_token":"a","refresh_token":"b","expires_in":1}`)
	}))
	defer server.Close()
	token, err := Refresh(context.Background(), Config{TokenURL: server.URL, HTTPClient: server.Client()}, "r")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "error") {
		t.Fatalf("unexpected encoding %s", encoded)
	}
}
