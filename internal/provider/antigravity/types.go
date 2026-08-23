package antigravity

type tokenCandidate struct {
	AccessToken   string
	RefreshToken  string
	ExpirySeconds int64
	Source        string
}

type fetchModelsResponse struct {
	Models map[string]modelInfo `json:"models"`
}

type modelInfo struct {
	DisplayName string     `json:"displayName"`
	Label       string     `json:"label"`
	Model       string     `json:"model"`
	IsInternal  bool       `json:"isInternal"`
	QuotaInfo   *quotaInfo `json:"quotaInfo"`
}

type quotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

// codeAssistInfo is the parsed shape of /v1internal:loadCodeAssist, used to
// resolve the Cloud project ID that /v1internal:retrieveUserQuotaSummary
// requires and to surface the account's plan/tier name.
type codeAssistInfo struct {
	CloudaicompanionProject string `json:"cloudaicompanionProject"`
	ProjectID               string `json:"projectId"`
	Project                 string `json:"project"`
	CurrentTier             struct {
		ID string `json:"id"`
	} `json:"currentTier"`
	PaidTier struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"paidTier"`
}

// quotaSummaryResponse is the parsed shape of
// /v1internal:retrieveUserQuotaSummary. It reports one group per model tier
// (Gemini, Claude and GPT) with real five-hour and weekly buckets, unlike
// fetchAvailableModels which only exposes a coarse, unreliable per-model
// fraction and no weekly data at all.
type quotaSummaryResponse struct {
	Groups []quotaSummaryGroup `json:"groups"`
}

type quotaSummaryGroup struct {
	DisplayName string               `json:"displayName"`
	Description string               `json:"description"`
	Buckets     []quotaSummaryBucket `json:"buckets"`
}

type quotaSummaryBucket struct {
	BucketID          string  `json:"bucketId"`
	DisplayName       string  `json:"displayName"`
	Window            string  `json:"window"`
	ResetTime         string  `json:"resetTime"`
	Description       string  `json:"description"`
	RemainingFraction float64 `json:"remainingFraction"`
}

type modelQuota struct {
	Label             string
	ModelID           string
	Pool              string
	RemainingFraction float64
	ResetTime         string
}

type cachedToken struct {
	AccessToken   string `json:"access_token"`
	ExpirySeconds int64  `json:"expiry_seconds"`
}

type refreshResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type cliTokenFile struct {
	Token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
	} `json:"token"`
}
