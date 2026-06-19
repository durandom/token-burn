package forecast

import (
	"math"
	"testing"
	"time"
)

func TestCalculateNoData(t *testing.T) {
	result := Calculate(nil, time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC))
	if result.SampleCount != 0 {
		t.Fatalf("SampleCount = %d, want 0", result.SampleCount)
	}
	if result.BurnRatePercentPerHour != nil {
		t.Fatalf("BurnRatePercentPerHour = %v, want nil", *result.BurnRatePercentPerHour)
	}
	if result.InsufficientDataReason != "no_samples" {
		t.Fatalf("reason = %q, want no_samples", result.InsufficientDataReason)
	}
}

func TestCalculateFlatUsage(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(5 * time.Hour)
	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 20, ResetAt: &reset},
		{ObservedAt: t0.Add(time.Hour), UsedPercent: 20, ResetAt: &reset},
	}, t0.Add(2*time.Hour))

	if result.BurnRatePercentPerHour == nil || *result.BurnRatePercentPerHour != 0 {
		t.Fatalf("burn = %v, want 0", result.BurnRatePercentPerHour)
	}
	if result.Estimated100At != nil {
		t.Fatalf("Estimated100At = %v, want nil", result.Estimated100At)
	}
	if result.InsufficientDataReason != "flat_usage" {
		t.Fatalf("reason = %q, want flat_usage", result.InsufficientDataReason)
	}
	if result.ProjectedResetPercent == nil || *result.ProjectedResetPercent != 20 {
		t.Fatalf("ProjectedResetPercent = %v, want 20", result.ProjectedResetPercent)
	}
}

func TestCalculateIncreasingUsage(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(5 * time.Hour)
	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 10, ResetAt: &reset},
		{ObservedAt: t0.Add(time.Hour), UsedPercent: 20, ResetAt: &reset},
		{ObservedAt: t0.Add(2 * time.Hour), UsedPercent: 30, ResetAt: &reset},
	}, t0.Add(2*time.Hour))

	if result.BurnRatePercentPerHour == nil || math.Abs(*result.BurnRatePercentPerHour-10) > 0.0001 {
		t.Fatalf("burn = %v, want 10", result.BurnRatePercentPerHour)
	}
	want90 := t0.Add(8 * time.Hour)
	if result.Estimated90At == nil || !result.Estimated90At.Equal(want90) {
		t.Fatalf("Estimated90At = %v, want %v", result.Estimated90At, want90)
	}
	want100 := t0.Add(9 * time.Hour)
	if result.Estimated100At == nil || !result.Estimated100At.Equal(want100) {
		t.Fatalf("Estimated100At = %v, want %v", result.Estimated100At, want100)
	}
	if result.Confidence <= 0 {
		t.Fatalf("Confidence = %v, want > 0", result.Confidence)
	}
	if result.ProjectedResetPercent == nil || math.Abs(*result.ProjectedResetPercent-60) > 0.0001 {
		t.Fatalf("ProjectedResetPercent = %v, want 60", result.ProjectedResetPercent)
	}
}

func TestCalculateProjectedResetCanExceedLimit(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(5 * time.Hour)
	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 20, ResetAt: &reset},
		{ObservedAt: t0.Add(time.Hour), UsedPercent: 40, ResetAt: &reset},
	}, t0.Add(time.Hour))

	if result.ProjectedResetPercent == nil || math.Abs(*result.ProjectedResetPercent-120) > 0.0001 {
		t.Fatalf("ProjectedResetPercent = %v, want 120", result.ProjectedResetPercent)
	}
	if result.Estimated100At == nil || !result.Estimated100At.Equal(t0.Add(4*time.Hour)) {
		t.Fatalf("Estimated100At = %v, want %v", result.Estimated100At, t0.Add(4*time.Hour))
	}
}

func TestCalculateUsesLatestResetWindow(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	oldReset := t0.Add(time.Hour)
	newReset := t0.Add(6 * time.Hour)

	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 90, ResetAt: &oldReset},
		{ObservedAt: t0.Add(time.Hour), UsedPercent: 5, ResetAt: &newReset},
		{ObservedAt: t0.Add(2 * time.Hour), UsedPercent: 15, ResetAt: &newReset},
	}, t0.Add(2*time.Hour))

	if result.SampleCount != 2 {
		t.Fatalf("SampleCount = %d, want 2 latest-reset samples", result.SampleCount)
	}
	if result.BurnRatePercentPerHour == nil || math.Abs(*result.BurnRatePercentPerHour-10) > 0.0001 {
		t.Fatalf("burn = %v, want 10", result.BurnRatePercentPerHour)
	}
}

func TestCalculateTreatsNearbyResetTimesAsSameWindow(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(5 * time.Hour)

	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 12, ResetAt: ptrTime(reset.Add(-500 * time.Millisecond))},
		{ObservedAt: t0.Add(time.Minute), UsedPercent: 13, ResetAt: ptrTime(reset.Add(500 * time.Millisecond))},
	}, t0.Add(time.Minute))

	if result.SampleCount != 2 {
		t.Fatalf("SampleCount = %d, want 2", result.SampleCount)
	}
	if result.InsufficientDataReason != "" {
		t.Fatalf("InsufficientDataReason = %q, want empty", result.InsufficientDataReason)
	}
}

func TestCalculateIgnoresMaterialDecreaseWithinSameReset(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(5 * time.Hour)

	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 50, ResetAt: &reset},
		{ObservedAt: t0.Add(time.Hour), UsedPercent: 55, ResetAt: &reset},
		{ObservedAt: t0.Add(2 * time.Hour), UsedPercent: 20, ResetAt: &reset},
		{ObservedAt: t0.Add(3 * time.Hour), UsedPercent: 30, ResetAt: &reset},
	}, t0.Add(3*time.Hour))

	if result.SampleCount != 2 {
		t.Fatalf("SampleCount = %d, want 2 samples after decrease", result.SampleCount)
	}
	if result.BurnRatePercentPerHour == nil || math.Abs(*result.BurnRatePercentPerHour-10) > 0.0001 {
		t.Fatalf("burn = %v, want 10", result.BurnRatePercentPerHour)
	}
}

func TestCalculateStartsNewSegmentAfterShortLargeJump(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	reset := t0.Add(12 * 24 * time.Hour)

	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 0, ResetAt: &reset},
		{ObservedAt: t0.Add(time.Minute), UsedPercent: 0, ResetAt: &reset},
		{ObservedAt: t0.Add(2 * time.Minute), UsedPercent: 37.2, ResetAt: &reset},
		{ObservedAt: t0.Add(3 * time.Minute), UsedPercent: 37.2, ResetAt: &reset},
		{ObservedAt: t0.Add(4 * time.Minute), UsedPercent: 37.2, ResetAt: &reset},
	}, t0.Add(4*time.Minute))

	if result.SampleCount != 3 {
		t.Fatalf("SampleCount = %d, want 3 samples after jump", result.SampleCount)
	}
	if result.BurnRatePercentPerHour == nil || *result.BurnRatePercentPerHour != 0 {
		t.Fatalf("burn = %v, want 0", result.BurnRatePercentPerHour)
	}
	if result.ProjectedResetPercent == nil || math.Abs(*result.ProjectedResetPercent-37.2) > 0.0001 {
		t.Fatalf("ProjectedResetPercent = %v, want 37.2", result.ProjectedResetPercent)
	}
	if result.Estimated100At != nil {
		t.Fatalf("Estimated100At = %v, want nil for flat usage after jump", result.Estimated100At)
	}
}

func TestCalculateNoisyDataConfidence(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	result := Calculate([]Observation{
		{ObservedAt: t0, UsedPercent: 10},
		{ObservedAt: t0.Add(time.Hour), UsedPercent: 19},
		{ObservedAt: t0.Add(2 * time.Hour), UsedPercent: 31},
		{ObservedAt: t0.Add(3 * time.Hour), UsedPercent: 39},
		{ObservedAt: t0.Add(4 * time.Hour), UsedPercent: 52},
	}, t0.Add(4*time.Hour))

	if result.BurnRatePercentPerHour == nil || *result.BurnRatePercentPerHour <= 0 {
		t.Fatalf("burn = %v, want positive", result.BurnRatePercentPerHour)
	}
	if result.Confidence <= 0 || result.Confidence > 1 {
		t.Fatalf("Confidence = %v, want within (0,1]", result.Confidence)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
