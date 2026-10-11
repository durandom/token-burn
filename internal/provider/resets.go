package provider

import (
	"encoding/json"
	"time"
)

const ResetDetailsInterval = 15 * time.Minute

// Timestamp accepts the ISO and Unix timestamps used by live usage APIs.
type Timestamp struct{ time.Time }

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	parsed, err := ParseReset(v, time.Time{})
	if err != nil {
		return err
	}
	if parsed != nil {
		t.Time = *parsed
	}
	return nil
}
func (t Timestamp) MarshalJSON() ([]byte, error) { return json.Marshal(t.Time.UTC()) }
func (t *Timestamp) UTCValue() *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

type ResetCredit struct {
	ID               string         `json:"id"`
	ResetType        string         `json:"reset_type,omitempty"`
	Status           string         `json:"status"`
	Supported        bool           `json:"is_supported_by_plan"`
	GrantedAt        *Timestamp     `json:"granted_at,omitempty"`
	ExpiresAt        *Timestamp     `json:"expires_at,omitempty"`
	RedeemStartedAt  *Timestamp     `json:"redeem_started_at,omitempty"`
	RedeemedAt       *Timestamp     `json:"redeemed_at,omitempty"`
	Title            string         `json:"title,omitempty"`
	Description      string         `json:"description,omitempty"`
	ResetsTotal      int            `json:"resets_total"`
	ResetsLeft       int            `json:"resets_left"`
	Paused           bool           `json:"paused"`
	UsableNow        bool           `json:"usable_now"`
	UseRequiresLimit bool           `json:"use_requires_limit"`
	Clears           []any          `json:"clears,omitempty"`
	PercentUsed      map[string]any `json:"percent_used,omitempty"`
	Blocking         []any          `json:"blocking,omitempty"`
	Arm              any            `json:"arm,omitempty"`
}
type ResetEvent struct {
	ID         string    `json:"id"`
	CreditID   string    `json:"credit_id"`
	Kind       string    `json:"kind"`
	OccurredAt Timestamp `json:"occurred_at"`
	Source     string    `json:"source"`
	Count      int       `json:"count"`
}
type ResetSummary struct {
	AvailableCount           int `json:"available_count"`
	ApplicableAvailableCount int `json:"applicable_available_count"`
}
type ResetDetails struct {
	CreditsComplete bool           `json:"credits_complete"`
	Credits         []ResetCredit  `json:"credits"`
	Events          []ResetEvent   `json:"events"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}
type PlanLimitPeriod struct {
	ID                 string          `json:"id"`
	WindowMinutes      int             `json:"window_minutes"`
	PlanType           string          `json:"plan_type"`
	StartsAt           Timestamp       `json:"starts_at"`
	EndsAt             Timestamp       `json:"ends_at"`
	AccountingComplete bool            `json:"accounting_complete"`
	UsedBasisPoints    int             `json:"used_basis_points"`
	Breakdowns         []PlanBreakdown `json:"breakdowns"`
}
type PlanBreakdown struct {
	Dimension string             `json:"dimension"`
	Rows      []PlanBreakdownRow `json:"rows"`
}
type PlanBreakdownRow struct {
	Key         string `json:"key"`
	BasisPoints int    `json:"basis_points"`
}
type PlanLimitHistory struct {
	Periods                  []PlanLimitPeriod `json:"periods"`
	DataAsOf                 *Timestamp        `json:"data_as_of"`
	CoverageStart            *Timestamp        `json:"coverage_start"`
	CoverageComplete         bool              `json:"coverage_complete"`
	Approximate              bool              `json:"approximate"`
	BoundaryToleranceSeconds int               `json:"boundary_tolerance_seconds"`
}
