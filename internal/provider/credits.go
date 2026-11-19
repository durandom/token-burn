package provider

import (
	"fmt"
	"strings"
	"time"
)

// Money retains provider monetary units exactly. Exponent is the number of
// decimal places, not an inferred currency conversion.
type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Exponent    int    `json:"exponent"`
}

func (m Money) String() string {
	// Format integer units without converting through floating point.
	s := fmt.Sprint(m.AmountMinor)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	if m.Exponent > 0 && m.Exponent <= 4 {
		if len(s) <= m.Exponent {
			s = strings.Repeat("0", m.Exponent-len(s)+1) + s
		}
		s = s[:len(s)-m.Exponent] + "." + s[len(s)-m.Exponent:]
	}
	if m.Currency == "USD" {
		return sign + "$" + s
	}
	return sign + s + " " + m.Currency
}

type CreditGrant struct {
	ID        string       `json:"id,omitempty"`
	Program   string       `json:"program,omitempty"`
	Name      string       `json:"name,omitempty"`
	Remaining Money        `json:"remaining"`
	Granted   *Money       `json:"granted,omitempty"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
	Scope     *CreditScope `json:"scope,omitempty"`
}

type CreditScope struct {
	Kind      string              `json:"kind"`
	AppliesTo []CreditApplication `json:"applies_to"`
}

type CreditApplication struct {
	Product      string  `json:"product"`
	ModelFamily  *string `json:"model_family,omitempty"`
	ModelVersion *string `json:"model_version,omitempty"`
}

type CreditBalance struct {
	OrganizationID string        `json:"organization_id"`
	Balance        Money         `json:"balance"`
	Grants         []CreditGrant `json:"grants"`
}

// NextExpiry excludes expired and exhausted grants; an unknown expiry stays
// unknown and never becomes a fabricated reset time.
func (c *CreditBalance) NextExpiry(now time.Time) *time.Time {
	var next *time.Time
	for _, g := range c.Grants {
		if g.Remaining.AmountMinor > 0 && g.ExpiresAt != nil && g.ExpiresAt.After(now) && (next == nil || g.ExpiresAt.Before(*next)) {
			next = g.ExpiresAt
		}
	}
	return next
}
