package config

import (
	"errors"
	"strings"
	"testing"
)

func TestApplyServiceEnv(t *testing.T) {
	for _, tt := range []struct {
		name     string
		existing map[string]string
		fail     bool
		wantSet  bool
	}{
		{name: "unset", wantSet: true},
		{name: "process wins", existing: map[string]string{"TOKEN_BURN_TEST": "process"}},
		{name: "empty process wins", existing: map[string]string{"TOKEN_BURN_TEST": ""}},
		{name: "setter fails", fail: true, wantSet: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			err := ApplyServiceEnv(Config{Service: ServiceConfig{Env: map[string]string{"TOKEN_BURN_TEST": "secret"}}},
				func(key string) (string, bool) { v, ok := tt.existing[key]; return v, ok },
				func(key, value string) error {
					called = true
					if key != "TOKEN_BURN_TEST" || value != "secret" {
						t.Fatal("incorrect assignment")
					}
					if tt.fail {
						return errors.New("secret")
					}
					return nil
				})
			if called != tt.wantSet || (err != nil) != tt.fail {
				t.Fatalf("called=%t err=%v", called, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaks value")
			}
		})
	}
}
