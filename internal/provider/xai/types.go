package xai

import "github.com/durandom/token-burn/internal/piauth"

type oauthCredential = piauth.OAuthCredential

type userResponse struct {
	UserID string `json:"userId"`
}

type billingResponse struct {
	SubscriptionTier string         `json:"subscriptionTier"`
	OnDemandEnabled  *bool          `json:"onDemandEnabled"`
	Config           *billingConfig `json:"config"`
}

type billingConfig struct {
	CreditUsagePercent   *float64     `json:"creditUsagePercent"`
	CurrentPeriod        *usagePeriod `json:"currentPeriod"`
	MonthlyLimit         *moneyValue  `json:"monthlyLimit"`
	Used                 *moneyValue  `json:"used"`
	BillingPeriodStart   string       `json:"billingPeriodStart"`
	BillingPeriodEnd     string       `json:"billingPeriodEnd"`
	OnDemandCap          *moneyValue  `json:"onDemandCap"`
	OnDemandUsed         *moneyValue  `json:"onDemandUsed"`
	PrepaidBalance       *moneyValue  `json:"prepaidBalance"`
	IsUnifiedBillingUser *bool        `json:"isUnifiedBillingUser"`
}

type usagePeriod struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type moneyValue struct {
	Val *int64 `json:"val"`
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    *int64 `json:"expires_in"`
}
