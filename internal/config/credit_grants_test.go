package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfiguredCreditGrantsLoadAndValidate(t *testing.T) {
	for _, tc := range []struct {
		name, amount, expiry, want string
		invalid                    bool
	}{
		{"RFC3339 offset", "62500", "2026-12-31T23:59:59+02:00", "2026-12-31T21:59:59Z", false},
		{"date only", "62500", "2026-12-31", "2026-12-31T23:59:59Z", false},
		{"fractional amount", "12.5", "2026-12-31T23:59:59Z", "2026-12-31T23:59:59Z", false},
		{"zero", "0", "2026-12-31", "", true},
		{"negative", "-10", "2026-12-31", "", true},
		{"NaN", "nan", "2026-12-31", "", true},
		{"infinite", "inf", "2026-12-31", "", true},
		{"bad date", "10", "2026-02-30", "", true},
		{"missing date", "10", "", "", true},
		{"local datetime", "10", "2026-12-31T23:59:59", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			text := fmt.Sprintf("[credit_grants]\ncodex = [{label=\"Pro promo\",amount=%s,expires_at=%q}]\n", tc.amount, tc.expiry)
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid credit grant accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			grants := cfg.CreditGrants["codex"]
			if len(grants) != 1 || grants[0].Label != "Pro promo" || grants[0].ExpiresAt.Location() != time.UTC || grants[0].ExpiresAt.Format(time.RFC3339) != tc.want {
				t.Fatalf("grants %+v", grants)
			}
		})
	}
}

func TestDefaultCreditGrantsNotEnabled(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CreditGrants) != 0 {
		t.Fatal("example promo enabled by default")
	}
}
