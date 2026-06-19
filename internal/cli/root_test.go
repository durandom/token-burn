package cli

import (
	"bytes"
	"testing"
	"time"
)

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand(BuildInfo{
		Version: "v0.1.0",
		Commit:  "abc123",
		Date:    "2026-06-19T12:00:00Z",
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	want := "token-burn v0.1.0\ncommit: abc123\nbuilt: 2026-06-19T12:00:00Z\n"
	if got := out.String(); got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestParseLookbackDuration(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{raw: "24h", want: 24 * time.Hour},
		{raw: "7d", want: 7 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseLookbackDuration(tt.raw)
			if err != nil {
				t.Fatalf("parseLookbackDuration() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("duration = %s, want %s", got, tt.want)
			}
		})
	}
}
