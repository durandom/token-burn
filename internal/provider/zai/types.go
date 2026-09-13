package zai

import "github.com/durandom/token-burn/internal/piauth"

type credential = piauth.OAuthCredential

// envelope is the monitor API wrapper. A success flag of false means the
// account has no active coding package, so there is no quota to report.
type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Success bool   `json:"success"`
	Data    data   `json:"data"`
}

type data struct {
	Level  string  `json:"level"`
	Limits []limit `json:"limits"`
}

// limit entries arrive in at least two shapes. Older deployments report
// token buckets as type "TOKENS_LIMIT" with a percentage and total; newer
// deployments report credit buckets as type "CREDIT_LIMIT" with a credit
// cap (usage), consumed credits (currentValue), and remaining credits.
// Both carry a unit/number pair identifying the window length and a
// millisecond nextResetTime. The wire contract is undocumented and
// community reverse-engineered, so every field is optional and unknown
// shapes are tolerated.
type limit struct {
	Type          string   `json:"type"`
	Unit          int      `json:"unit"`
	Number        int      `json:"number"`
	Percentage    *float64 `json:"percentage"`
	Total         *int64   `json:"total"`
	Usage         *int64   `json:"usage"`
	CurrentValue  *int64   `json:"currentValue"`
	Remaining     *int64   `json:"remaining"`
	NextResetTime *int64   `json:"nextResetTime"`
}

const (
	limitTypeTokens  = "TOKENS_LIMIT"
	limitTypeCredit  = "CREDIT_LIMIT"
	limitTypeTime    = "TIME_LIMIT"
	unitHour         = 3
	unitWeek         = 6
	fiveHourWindow   = "5h"
	weeklyWindow     = "weekly"
	mcpMonthlyWindow = "mcp_monthly"
)
