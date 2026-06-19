package copilot

type userResponse struct {
	Login             string                   `json:"login"`
	AccessTypeSKU     string                   `json:"access_type_sku"`
	CopilotPlan       string                   `json:"copilot_plan"`
	QuotaResetDate    string                   `json:"quota_reset_date"`
	QuotaResetDateUTC string                   `json:"quota_reset_date_utc"`
	TokenBasedBilling bool                     `json:"token_based_billing"`
	QuotaSnapshots    map[string]quotaSnapshot `json:"quota_snapshots"`
}

type quotaSnapshot struct {
	Entitlement        *float64 `json:"entitlement"`
	HasQuota           *bool    `json:"has_quota"`
	OverageCount       *float64 `json:"overage_count"`
	OverageEntitlement *float64 `json:"overage_entitlement"`
	OveragePermitted   *bool    `json:"overage_permitted"`
	PercentRemaining   *float64 `json:"percent_remaining"`
	QuotaID            string   `json:"quota_id"`
	QuotaRemaining     *float64 `json:"quota_remaining"`
	QuotaResetAt       int64    `json:"quota_reset_at"`
	Remaining          *float64 `json:"remaining"`
	TimestampUTC       string   `json:"timestamp_utc"`
	TokenBasedBilling  bool     `json:"token_based_billing"`
	Unlimited          *bool    `json:"unlimited"`
}

type billingUsageResponse struct {
	TimePeriod map[string]any     `json:"timePeriod"`
	User       string             `json:"user"`
	UsageItems []billingUsageItem `json:"usageItems"`
}

type billingUsageItem struct {
	Product          string  `json:"product"`
	SKU              string  `json:"sku"`
	Model            string  `json:"model"`
	UnitType         string  `json:"unitType"`
	PricePerUnit     float64 `json:"pricePerUnit"`
	GrossQuantity    float64 `json:"grossQuantity"`
	GrossAmount      float64 `json:"grossAmount"`
	DiscountQuantity float64 `json:"discountQuantity"`
	DiscountAmount   float64 `json:"discountAmount"`
	NetQuantity      float64 `json:"netQuantity"`
	NetAmount        float64 `json:"netAmount"`
}
