package claude

type usageResponse struct {
	FiveHour          *usageBucket `json:"five_hour"`
	SevenDay          *usageBucket `json:"seven_day"`
	SevenDaySonnet    *usageBucket `json:"seven_day_sonnet"`
	SevenDayOpus      *usageBucket `json:"seven_day_opus"`
	SevenDayCowork    *usageBucket `json:"seven_day_cowork"`
	SevenDayOAuthApps *usageBucket `json:"seven_day_oauth_apps"`
	ExtraUsage        *usageBucket `json:"extra_usage"`
	Limits            []usageLimit `json:"limits"`
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
