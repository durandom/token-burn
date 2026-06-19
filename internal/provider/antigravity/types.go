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
