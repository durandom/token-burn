package codex

type authFile struct {
	AccountID string     `json:"account_id,omitempty"`
	Tokens    authTokens `json:"tokens"`
}

type authTokens struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id,omitempty"`
}

type usagePayload struct {
	UserID               string                 `json:"user_id,omitempty"`
	AccountID            string                 `json:"account_id,omitempty"`
	Email                string                 `json:"email,omitempty"`
	PlanType             string                 `json:"plan_type,omitempty"`
	RateLimit            *usageLimitDetails     `json:"rate_limit,omitempty"`
	CodeReviewRateLimit  *usageLimitDetails     `json:"code_review_rate_limit,omitempty"`
	AdditionalRateLimits []usageAdditionalLimit `json:"additional_rate_limits,omitempty"`
	Credits              any                    `json:"credits,omitempty"`
	RateLimitStatus      *usageRateLimitStatus  `json:"rate_limit_status,omitempty"`
}

type usageRateLimitStatus struct {
	PlanType             string                 `json:"plan_type,omitempty"`
	RateLimit            *usageLimitDetails     `json:"rate_limit,omitempty"`
	CodeReviewRateLimit  *usageLimitDetails     `json:"code_review_rate_limit,omitempty"`
	AdditionalRateLimits []usageAdditionalLimit `json:"additional_rate_limits,omitempty"`
	Credits              any                    `json:"credits,omitempty"`
}

type usageLimitDetails struct {
	Allowed         bool             `json:"allowed"`
	LimitReached    bool             `json:"limit_reached"`
	PrimaryWindow   *usageWindowInfo `json:"primary_window,omitempty"`
	SecondaryWindow *usageWindowInfo `json:"secondary_window,omitempty"`
	Primary         *usageWindowInfo `json:"primary,omitempty"`
	Secondary       *usageWindowInfo `json:"secondary,omitempty"`
}

type usageWindowInfo struct {
	UsedPercent        *float64 `json:"used_percent,omitempty"`
	RemainingPercent   *float64 `json:"remaining_percent,omitempty"`
	LimitWindowSeconds int      `json:"limit_window_seconds,omitempty"`
	WindowMinutes      int      `json:"window_minutes,omitempty"`
	ResetAt            int64    `json:"reset_at,omitempty"`
	ResetsAt           int64    `json:"resets_at,omitempty"`
	ResetAfterSeconds  int      `json:"reset_after_seconds,omitempty"`
}

type usageAdditionalLimit struct {
	LimitName      string             `json:"limit_name,omitempty"`
	MeteredFeature string             `json:"metered_feature,omitempty"`
	RateLimit      *usageLimitDetails `json:"rate_limit,omitempty"`
}
