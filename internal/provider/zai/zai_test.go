package zai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	usageprovider "github.com/durandom/token-burn/internal/provider"
)

func writeAuth(t *testing.T, value map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const legacyTokensEnvelope = `{
	"code": 200,
	"msg": "Operation successful",
	"success": true,
	"data": {
		"level": "pro",
		"limits": [
			{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":18.5,"total":6000000,"nextResetTime":%[1]d},
			{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":47.2,"total":80000000,"nextResetTime":%[2]d}
		]
	}
}`

const creditLimitsEnvelope = `{
	"code": 200,
	"msg": "Operation successful",
	"success": true,
	"data": {
		"level": "lite",
		"limits": [
			{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":500,"remaining":1500,"percentage":25,"nextResetTime":%[1]d},
			{"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":10000,"currentValue":300,"remaining":9700,"percentage":3,"nextResetTime":%[2]d},
			{"type":"TIME_LIMIT","percentage":4,"currentValue":12,"usage":300,"nextResetTime":%[3]d}
		]
	}
}`

// fixtureTimes anchors all fixtures to one instant so reset times always
// land in the future relative to the fixed Now the tests inject.
var fixtureTimes = struct {
	base     time.Time
	fiveHour int64
	weekly   int64
	monthly  int64
}{
	base:     time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
	fiveHour: time.Date(2026, 9, 16, 14, 0, 0, 0, time.UTC).UnixMilli(),
	weekly:   time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC).UnixMilli(),
	monthly:  time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli(),
}

func tokensFixture() string {
	return fmt.Sprintf(legacyTokensEnvelope, fixtureTimes.fiveHour, fixtureTimes.weekly)
}

func creditsFixture() string {
	return fmt.Sprintf(creditLimitsEnvelope, fixtureTimes.fiveHour, fixtureTimes.weekly, fixtureTimes.monthly)
}

func newTestProvider(t *testing.T, body string, status int, authPath string) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != quotaPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, quotaPath)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return &Provider{HTTPClient: server.Client(), BaseURL: server.URL, Now: func() time.Time { return fixtureTimes.base }}
}

func TestFetchReadsPiAPIKeyAndMapsTokenBuckets(t *testing.T) {
	now := fixtureTimes.base
	authPath := writeAuth(t, map[string]any{
		"zai": map[string]any{"type": "api_key", "key": "secret-zai-key"},
	})
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		fmt.Fprint(w, tokensFixture())
	}))
	defer server.Close()

	p := &Provider{HTTPClient: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}
	snap, err := p.Fetch(context.Background(), usageprovider.Account{ID: "zai-work", AuthFile: authPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotAuth != "secret-zai-key" {
		t.Fatalf("Authorization = %q, want raw API key without Bearer prefix", gotAuth)
	}
	if snap.Provider != "zai" || snap.AccountID != "zai-work" || snap.PlanType != "pro" || snap.Source != source {
		t.Fatalf("snapshot metadata = %#v", snap)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("windows = %#v", snap.Windows)
	}
	fiveHour, weekly := snap.Windows[0], snap.Windows[1]
	if fiveHour.Name != "5h" || fiveHour.UsedPercent != 18.5 {
		t.Fatalf("5h window = %#v", fiveHour)
	}
	if fiveHour.WindowSeconds == nil || *fiveHour.WindowSeconds != 5*3600 {
		t.Fatalf("5h window seconds = %v", fiveHour.WindowSeconds)
	}
	if !fiveHour.ResetAt.Equal(time.UnixMilli(fixtureTimes.fiveHour)) {
		t.Fatalf("5h reset = %v", fiveHour.ResetAt)
	}
	if weekly.Name != "weekly" || weekly.UsedPercent != 47.2 {
		t.Fatalf("weekly window = %#v", weekly)
	}
	if weekly.WindowSeconds == nil || *weekly.WindowSeconds != 7*24*3600 {
		t.Fatalf("weekly window seconds = %v", weekly.WindowSeconds)
	}

	rawJSON, _ := json.Marshal(snap.Raw)
	if strings.Contains(string(rawJSON), "secret-zai-key") {
		t.Fatalf("raw metadata leaked the API key: %s", rawJSON)
	}
	if snap.Raw["level"] != "pro" {
		t.Fatalf("raw = %#v", snap.Raw)
	}
}

func TestFetchPrefersExactCreditRatioOverRoundedPercentage(t *testing.T) {
	now := fixtureTimes.base
	authPath := writeAuth(t, map[string]any{
		"zai": map[string]any{"type": "api_key", "key": "secret-zai-key"},
	})
	p := newTestProvider(t, creditsFixture(), http.StatusOK, authPath)
	p.Now = func() time.Time { return now }

	snap, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if snap.AccountID != "zai-default" {
		t.Fatalf("account id = %q, want zai-default", snap.AccountID)
	}
	fiveHour, weekly := snap.Windows[0], snap.Windows[1]
	// 500/2000 = 25%; the API-reported percentage is already exact here.
	if fiveHour.UsedPercent != 25 {
		t.Fatalf("5h used = %v, want 25", fiveHour.UsedPercent)
	}
	// 300/10000 = 3%; same value, but derived from the credit ratio.
	if weekly.UsedPercent != 3 {
		t.Fatalf("weekly used = %v, want 3", weekly.UsedPercent)
	}
	if len(snap.Windows) != 3 {
		t.Fatalf("windows = %#v, want mcp_monthly included", snap.Windows)
	}
	mcp := snap.Windows[2]
	if mcp.Name != "mcp_monthly" || mcp.UsedPercent != 4 {
		t.Fatalf("mcp window = %#v", mcp)
	}
	if got := snap.Raw["5h"].(map[string]any)["credit_cap"]; got != int64(2000) {
		t.Fatalf("raw credit cap = %#v", snap.Raw["5h"])
	}
	if got := snap.Raw["weekly"].(map[string]any)["credit_used"]; got != int64(300) {
		t.Fatalf("raw credit used = %#v", snap.Raw["weekly"])
	}
}

func TestCreditPercentageFallsBackToReportedValueWithoutRatio(t *testing.T) {
	item := limit{Type: limitTypeCredit, Unit: unitHour, Number: 5, Percentage: floatPtr(25), NextResetTime: int64Ptr(1789339283092)}
	used, seconds := usedPercent(item, parseMillis(item.NextResetTime), time.UnixMilli(1789337283092))
	if used == nil || *used != 25 {
		t.Fatalf("used = %#v, want 25", used)
	}
	if seconds == nil || *seconds != 5*3600 {
		t.Fatalf("seconds = %#v, want %d", seconds, 5*3600)
	}
}

func TestReportedValuesPassThroughEvenNearRollover(t *testing.T) {
	// The window may roll over server-side between polls; token-burn
	// reports what the API returned rather than inventing a zero.
	reset := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	item := limit{Type: limitTypeCredit, Unit: unitHour, Number: 5, Percentage: floatPtr(90)}
	used, seconds := usedPercent(item, &reset, observed)
	if used == nil || *used != 90 {
		t.Fatalf("used = %#v, want 90", used)
	}
	if seconds == nil || *seconds != 5*3600 {
		t.Fatalf("seconds = %#v, want %d", seconds, 5*3600)
	}
}

func TestUnknownBucketShapesAreIgnoredAndReported(t *testing.T) {
	body := `{
		"success": true,
		"data": {
			"level": "lite",
			"limits": [
				{"type":"TOKENS_LIMIT","unit":2,"number":10,"percentage":50,"nextResetTime":1789339283092},
				{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":10,"percentage":1,"nextResetTime":1789339283092}
			]
		}
	}`
	snap, err := mapQuota(id, parseEnvelope(t, body), usageprovider.Account{}, time.UnixMilli(1789337283092))
	if err != nil {
		t.Fatalf("mapQuota() error = %v", err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Name != "5h" {
		t.Fatalf("windows = %#v, want only the 5h bucket", snap.Windows)
	}
	if snap.Windows[0].UsedPercent != 0.5 {
		t.Fatalf("used = %v, want 0.5 (10/2000)", snap.Windows[0].UsedPercent)
	}
	if _, ok := snap.Raw["5h"]; !ok {
		t.Fatalf("raw = %#v, want 5h entry", snap.Raw)
	}
}

func TestMapQuotaRejectsEnvelopeWithoutUsableBuckets(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"success false", `{"success":false,"data":{"level":"lite","limits":[]}}`},
		{"empty limits", `{"success":true,"data":{"level":"lite","limits":[]}}`},
		{"only unknown shapes", `{"success":true,"data":{"limits":[{"type":"MYSTERY","percentage":5}]}}`},
		{"missing data", `{"success":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mapQuota(id, parseEnvelope(t, tt.body), usageprovider.Account{}, time.Now())
			var providerErr *usageprovider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrInvalidResponse {
				t.Fatalf("mapQuota() error = %v, want %s", err, usageprovider.ErrInvalidResponse)
			}
		})
	}
}

func TestHTTPClassification(t *testing.T) {
	tests := []struct {
		status int
		want   usageprovider.ErrorCode
	}{
		{http.StatusUnauthorized, usageprovider.ErrAuthExpired},
		{http.StatusForbidden, usageprovider.ErrAuthExpired},
		{http.StatusTooManyRequests, usageprovider.ErrRateLimited},
		{http.StatusInternalServerError, usageprovider.ErrTransientHTTPFailure},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("HTTP %d", tt.status), func(t *testing.T) {
			authPath := writeAuth(t, map[string]any{
				"zai": map[string]any{"type": "api_key", "key": "secret-zai-key"},
			})
			p := newTestProvider(t, `{"error":"nope"}`, tt.status, authPath)
			_, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
			var providerErr *usageprovider.Error
			if !errors.As(err, &providerErr) {
				t.Fatalf("Fetch() error = %v, want provider error", err)
			}
			if providerErr.Code != tt.want || providerErr.HTTPStatus != tt.status {
				t.Fatalf("error = %#v, want code %s with status %d", providerErr, tt.want, tt.status)
			}
		})
	}
}

func TestFetchRejectsOversizedResponses(t *testing.T) {
	authPath := writeAuth(t, map[string]any{
		"zai": map[string]any{"type": "api_key", "key": "secret-zai-key"},
	})
	big := `{"success":true,"data":{"limits":["` + strings.Repeat("x", maxResponseBytes) + `"]}}`
	p := newTestProvider(t, big, http.StatusOK, authPath)
	_, err := p.Fetch(context.Background(), usageprovider.Account{AuthFile: authPath})
	var providerErr *usageprovider.Error
	if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrInvalidResponse {
		t.Fatalf("Fetch() error = %v, want %s", err, usageprovider.ErrInvalidResponse)
	}
}

func TestReadAPIKeyRejectsMissingAndWrongShapedCredentials(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
	}{
		{"oauth credential instead of api key", map[string]any{"type": "oauth", "access": "secret-access"}},
		{"api key entry without key", map[string]any{"type": "api_key"}},
		{"blank key", map[string]any{"type": "api_key", "key": "  "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authPath := writeAuth(t, map[string]any{"zai": tt.entry})
			p := &Provider{}
			_, err := p.readAPIKey(context.Background(), usageprovider.Account{AuthFile: authPath})
			var providerErr *usageprovider.Error
			if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrAuthMissing {
				t.Fatalf("readAPIKey() error = %v, want %s", err, usageprovider.ErrAuthMissing)
			}
		})
	}

	t.Run("missing credential entry", func(t *testing.T) {
		authPath := writeAuth(t, map[string]any{})
		p := &Provider{}
		_, err := p.readAPIKey(context.Background(), usageprovider.Account{AuthFile: authPath})
		var providerErr *usageprovider.Error
		if !errors.As(err, &providerErr) || providerErr.Code != usageprovider.ErrAuthMissing {
			t.Fatalf("readAPIKey() error = %v, want %s", err, usageprovider.ErrAuthMissing)
		}
		if !strings.Contains(err.Error(), "/login zai") {
			t.Fatalf("error = %v, want relogin hint", err)
		}
	})
}

func TestCNProviderTargetsBigmodelHostAndCNCredential(t *testing.T) {
	authPath := writeAuth(t, map[string]any{
		"zai-coding-cn": map[string]any{"type": "api_key", "key": "secret-cn-key"},
		"zai":           map[string]any{"type": "api_key", "key": "secret-global-key"},
	})
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, tokensFixture())
	}))
	defer server.Close()

	p := NewCN()
	p.HTTPClient = server.Client()
	p.BaseURL = server.URL
	p.Now = func() time.Time { return fixtureTimes.base }
	if p.ID() != cnID {
		t.Fatalf("ID() = %q, want %q", p.ID(), cnID)
	}
	snap, err := p.Fetch(context.Background(), usageprovider.Account{ID: "zai-cn", AuthFile: authPath})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotAuth != "secret-cn-key" {
		t.Fatalf("Authorization = %q, want the CN key", gotAuth)
	}
	if snap.Provider != cnID || snap.AccountID != "zai-cn" {
		t.Fatalf("snapshot metadata = %#v", snap)
	}
}

func parseEnvelope(t *testing.T, body string) envelope {
	t.Helper()
	var payload envelope
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	return payload
}

func floatPtr(v float64) *float64 { return &v }

func int64Ptr(v int64) *int64 { return &v }
