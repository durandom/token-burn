package claude

import "github.com/durandom/token-burn/internal/provider"

type usageResponse struct {
	CedarEmber        *resetProgram  `json:"cedar_ember"`
	JuniperTide       map[string]any `json:"juniper_tide"`
	FiveHour          *usageBucket   `json:"five_hour"`
	SevenDay          *usageBucket   `json:"seven_day"`
	SevenDaySonnet    *usageBucket   `json:"seven_day_sonnet"`
	SevenDayOpus      *usageBucket   `json:"seven_day_opus"`
	SevenDayCowork    *usageBucket   `json:"seven_day_cowork"`
	SevenDayOAuthApps *usageBucket   `json:"seven_day_oauth_apps"`
	ExtraUsage        *usageBucket   `json:"extra_usage"`
	Limits            []usageLimit   `json:"limits"`
}

type usageLimit struct {
	Kind     string      `json:"kind"`
	Percent  *float64    `json:"percent"`
	ResetsAt string      `json:"resets_at"`
	Scope    *limitScope `json:"scope"`
}

type limitScope struct {
	Model *limitModel `json:"model"`
}

type limitModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type usageBucket struct {
	Utilization  *float64 `json:"utilization"`
	ResetsAt     string   `json:"resets_at"`
	IsEnabled    *bool    `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
}

type resetProgram struct {
	Eligible         bool                `json:"eligible"`
	IneligibleReason string              `json:"ineligible_reason"`
	AtLimit          bool                `json:"at_limit"`
	Exhausted        []any               `json:"exhausted"`
	Grants           []resetGrant        `json:"grants"`
	NextGrantID      any                 `json:"next_grant_id"`
	WeeklyResetsAt   *provider.Timestamp `json:"weekly_resets_at"`
	CooldownUntil    *provider.Timestamp `json:"cooldown_until"`
}
type resetGrant struct {
	provider.ResetCredit
	Label    string              `json:"label"`
	StartsAt *provider.Timestamp `json:"starts_at"`
	EndsAt   *provider.Timestamp `json:"ends_at"`
}
