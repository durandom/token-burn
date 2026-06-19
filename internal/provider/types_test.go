package provider

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestClampPercent(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{name: "negative", in: -1, want: 0},
		{name: "zero", in: 0, want: 0},
		{name: "middle", in: 42.5, want: 42.5},
		{name: "over", in: 101, want: 100},
		{name: "nan", in: math.NaN(), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampPercent(tt.in); got != tt.want {
				t.Fatalf("ClampPercent(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewWindowNormalizesFromUsedPercent(t *testing.T) {
	used := 125.0
	seconds := 18000
	reset := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	win, ok := NewWindow(" Five Hour ", WindowOptions{
		UsedPercent:   &used,
		ResetAt:       &reset,
		WindowSeconds: &seconds,
	})
	if !ok {
		t.Fatal("NewWindow() ok = false, want true")
	}
	if win.Name != "five_hour" {
		t.Fatalf("Name = %q, want five_hour", win.Name)
	}
	if win.UsedPercent != 100 {
		t.Fatalf("UsedPercent = %v, want 100", win.UsedPercent)
	}
	if win.RemainingPercent == nil || *win.RemainingPercent != 0 {
		t.Fatalf("RemainingPercent = %v, want 0", win.RemainingPercent)
	}
	if !win.LimitReached {
		t.Fatal("LimitReached = false, want true")
	}
}

func TestNewWindowNormalizesFromRemainingPercent(t *testing.T) {
	remaining := 75.0

	win, ok := NewWindow("seven/day", WindowOptions{RemainingPercent: &remaining})
	if !ok {
		t.Fatal("NewWindow() ok = false, want true")
	}
	if win.Name != "seven_day" {
		t.Fatalf("Name = %q, want seven_day", win.Name)
	}
	if win.UsedPercent != 25 {
		t.Fatalf("UsedPercent = %v, want 25", win.UsedPercent)
	}
}

func TestNewWindowRequiresPercent(t *testing.T) {
	if _, ok := NewWindow("five_hour", WindowOptions{}); ok {
		t.Fatal("NewWindow() ok = true, want false")
	}
}

func TestParseReset(t *testing.T) {
	observedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	want := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		value any
	}{
		{name: "rfc3339", value: "2026-06-19T12:00:00Z"},
		{name: "unix int", value: int(want.Unix())},
		{name: "unix int64", value: want.Unix()},
		{name: "unix float", value: float64(want.Unix())},
		{name: "json number", value: json.Number("1781870400")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReset(tt.value, observedAt)
			if err != nil {
				t.Fatalf("ParseReset() error = %v", err)
			}
			if got == nil || !got.Equal(want) {
				t.Fatalf("ParseReset() = %v, want %v", got, want)
			}
		})
	}
}

func TestParseResetAfterSeconds(t *testing.T) {
	observedAt := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	got := ParseResetAfterSeconds(300, observedAt)
	want := observedAt.Add(5 * time.Minute)

	if got == nil || !got.Equal(want) {
		t.Fatalf("ParseResetAfterSeconds() = %v, want %v", got, want)
	}
}
