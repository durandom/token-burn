package forecast

import (
	"math"
	"sort"
	"time"
)

const MethodLinearRegression = "linear_regression"

const resetWindowTolerance = 2 * time.Minute

type Observation struct {
	ObservedAt  time.Time
	UsedPercent float64
	ResetAt     *time.Time
}

type Result struct {
	ComputedAt                 time.Time
	SampleCount                int
	BurnRatePercentPerHour     *float64
	ProjectedResetPercent      *float64
	Estimated90At              *time.Time
	Estimated100At             *time.Time
	Confidence                 float64
	Method                     string
	InsufficientDataReason     string
	StableResetWindowStartedAt *time.Time
}

func Calculate(observations []Observation, computedAt time.Time) Result {
	result := Result{
		ComputedAt: computedAt.UTC(),
		Method:     MethodLinearRegression,
	}

	samples := stableSamples(observations)
	result.SampleCount = len(samples)
	if len(samples) > 0 {
		start := samples[0].ObservedAt.UTC()
		result.StableResetWindowStartedAt = &start
	}
	if len(samples) == 0 {
		result.InsufficientDataReason = "no_samples"
		return result
	}
	if len(samples) == 1 {
		result.InsufficientDataReason = "one_sample"
		return result
	}

	slope, r2 := linearRegression(samples)
	if math.IsNaN(slope) || math.IsInf(slope, 0) {
		result.InsufficientDataReason = "invalid_slope"
		return result
	}

	burn := slope
	if burn < 0 {
		burn = 0
	}
	result.BurnRatePercentPerHour = &burn
	result.Confidence = confidence(len(samples), r2)

	last := samples[len(samples)-1]
	if burn <= 0 {
		result.ProjectedResetPercent = projectedResetPercent(last, burn)
		result.InsufficientDataReason = "flat_usage"
		return result
	}

	result.ProjectedResetPercent = projectedResetPercent(last, burn)
	result.Estimated90At = estimateThreshold(last, burn, 90)
	result.Estimated100At = estimateThreshold(last, burn, 100)
	return result
}

func stableSamples(observations []Observation) []Observation {
	samples := make([]Observation, 0, len(observations))
	for _, obs := range observations {
		if obs.ObservedAt.IsZero() {
			continue
		}
		obs.ObservedAt = obs.ObservedAt.UTC()
		if obs.ResetAt != nil {
			t := obs.ResetAt.UTC()
			obs.ResetAt = &t
		}
		samples = append(samples, obs)
	}
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].ObservedAt.Before(samples[j].ObservedAt)
	})
	if len(samples) == 0 {
		return nil
	}

	latestReset := samples[len(samples)-1].ResetAt
	if latestReset != nil {
		filtered := samples[:0]
		for _, sample := range samples {
			if sample.ResetAt != nil && sameResetWindow(*sample.ResetAt, *latestReset) {
				filtered = append(filtered, sample)
			}
		}
		samples = filtered
	}

	start := 0
	for i := 1; i < len(samples); i++ {
		if samples[i].UsedPercent+1 < samples[i-1].UsedPercent {
			start = i
		}
	}
	return append([]Observation(nil), samples[start:]...)
}

func sameResetWindow(a, b time.Time) bool {
	diff := a.Sub(b)
	if diff < 0 {
		diff = -diff
	}
	return diff <= resetWindowTolerance
}

func linearRegression(samples []Observation) (float64, float64) {
	first := samples[0].ObservedAt
	var sumX, sumY float64
	for _, sample := range samples {
		x := sample.ObservedAt.Sub(first).Hours()
		sumX += x
		sumY += sample.UsedPercent
	}
	n := float64(len(samples))
	meanX := sumX / n
	meanY := sumY / n

	var ssXX, ssXY, ssTot float64
	for _, sample := range samples {
		x := sample.ObservedAt.Sub(first).Hours()
		y := sample.UsedPercent
		dx := x - meanX
		dy := y - meanY
		ssXX += dx * dx
		ssXY += dx * dy
		ssTot += dy * dy
	}
	if ssXX == 0 {
		return 0, 0
	}
	slope := ssXY / ssXX

	if ssTot == 0 {
		return slope, 1
	}
	var ssErr float64
	for _, sample := range samples {
		x := sample.ObservedAt.Sub(first).Hours()
		predicted := meanY + slope*(x-meanX)
		err := sample.UsedPercent - predicted
		ssErr += err * err
	}
	r2 := 1 - ssErr/ssTot
	if r2 < 0 {
		r2 = 0
	}
	if r2 > 1 {
		r2 = 1
	}
	return slope, r2
}

func estimateThreshold(last Observation, burnRatePercentPerHour float64, threshold float64) *time.Time {
	if last.UsedPercent >= threshold {
		t := last.ObservedAt.UTC()
		return &t
	}
	hours := (threshold - last.UsedPercent) / burnRatePercentPerHour
	if hours < 0 || math.IsInf(hours, 0) || math.IsNaN(hours) {
		return nil
	}
	t := last.ObservedAt.UTC().Add(time.Duration(hours * float64(time.Hour)))
	return &t
}

func projectedResetPercent(last Observation, burnRatePercentPerHour float64) *float64 {
	if last.ResetAt == nil {
		return nil
	}
	hoursUntilReset := last.ResetAt.Sub(last.ObservedAt).Hours()
	if hoursUntilReset < 0 || math.IsNaN(hoursUntilReset) || math.IsInf(hoursUntilReset, 0) {
		return nil
	}
	value := last.UsedPercent + burnRatePercentPerHour*hoursUntilReset
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return &value
}

func confidence(sampleCount int, r2 float64) float64 {
	if sampleCount < 2 {
		return 0
	}
	countFactor := float64(sampleCount) / 5
	if countFactor > 1 {
		countFactor = 1
	}
	value := countFactor * r2
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
