// Package claudeplatform reads Console prepaid balances independently of
// Claude subscription limits. The Console API is undocumented and GET-only.
package claudeplatform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

const ID = "claude-platform"
const PollInterval = 15 * time.Minute

type Provider struct {
	BaseURL     string
	HTTPClient  *http.Client
	Now         func() time.Time
	ReadSession func(context.Context, string, string) (string, error)
}

func New() *Provider           { return &Provider{} }
func (p *Provider) ID() string { return ID }

func failure(code provider.ErrorCode, status int, msg string) error {
	return &provider.Error{Provider: ID, Code: code, HTTPStatus: status, Err: errors.New(msg)}
}

func (p *Provider) get(ctx context.Context, session, path string, out any) error {
	base := p.BaseURL
	if base == "" {
		base = "https://platform.claude.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return failure(provider.ErrInvalidResponse, 0, "cannot construct Console request")
	}
	req.AddCookie(&http.Cookie{Name: "sessionKey", Value: session})
	req.Header.Set("Accept", "application/json")
	// Console rejects requests without a browser-style User-Agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; token-burn)")
	client := http.Client{Timeout: 15 * time.Second}
	if p.HTTPClient != nil {
		client = *p.HTTPClient
	}
	// Never forward the manually supplied Cookie header to a redirect target.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return failure(provider.ErrTransientHTTPFailure, 0, "Console request failed")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case 401, 403:
		return failure(provider.ErrAuthExpired, resp.StatusCode, "Console login required; reimport sessionKey (billing access is required)")
	case 429:
		return failure(provider.ErrRateLimited, 429, "Console rate limited")
	default:
		return failure(provider.ErrTransientHTTPFailure, resp.StatusCode, "Console request unsuccessful")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return failure(provider.ErrInvalidResponse, 0, "invalid Console response size")
	}
	// Never include server bodies or decoder errors: either can echo secrets.
	if json.Unmarshal(body, out) != nil {
		return failure(provider.ErrInvalidResponse, 0, "invalid Console JSON")
	}
	return nil
}

type organization struct {
	ID           string   `json:"uuid"`
	Capabilities []string `json:"capabilities"`
}

func (p *Provider) Fetch(ctx context.Context, acct provider.Account) (provider.Snapshot, error) {
	read := p.ReadSession
	if read == nil {
		read = ReadSession
	}
	session, err := read(ctx, acct.ID, acct.CredentialsFile)
	if err != nil {
		if p.ReadSession != nil {
			return provider.Snapshot{}, failure(provider.ErrAuthMissing, 0, "Console session unavailable")
		}
		return provider.Snapshot{}, failure(provider.ErrAuthMissing, 0, err.Error())
	}
	session, err = NormalizeSession(session)
	if err != nil {
		return provider.Snapshot{}, failure(provider.ErrAuthMissing, 0, "invalid Console session; reimport sessionKey")
	}
	orgID := acct.ProviderAccountID
	if orgID == "" {
		var orgs []organization
		if err := p.get(ctx, session, "/api/organizations", &orgs); err != nil {
			return provider.Snapshot{}, err
		}
		for _, org := range orgs {
			for _, cap := range org.Capabilities {
				if cap == "api" {
					if orgID != "" {
						return provider.Snapshot{}, failure(provider.ErrUnsupportedAccountShape, 0, "multiple API organizations; configure provider_account_id")
					}
					orgID = org.ID
					break
				}
			}
		}
		if orgID == "" {
			return provider.Snapshot{}, failure(provider.ErrUnsupportedAccountShape, 0, "no Console API organization found")
		}
	}
	if strings.ContainsAny(orgID, "/\\?#%") || strings.TrimSpace(orgID) != orgID {
		return provider.Snapshot{}, failure(provider.ErrUnsupportedAccountShape, 0, "invalid organization ID")
	}
	var raw creditsResponse
	if err := p.get(ctx, session, "/api/organizations/"+url.PathEscape(orgID)+"/prepaid/credits", &raw); err != nil {
		return provider.Snapshot{}, err
	}
	credits, err := raw.normalize(orgID)
	if err != nil {
		return provider.Snapshot{}, failure(provider.ErrInvalidResponse, 0, err.Error())
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	return provider.Snapshot{Provider: ID, AccountID: acct.ID, Source: "claude_console_prepaid", ObservedAt: now, CreditBalance: credits}, nil
}

type creditsResponse struct {
	Amount   *int64 `json:"amount"`
	Currency string `json:"currency"`
	Balance  *struct {
		Credits *struct {
			Amount   *int64 `json:"amount_minor"`
			Exponent *int   `json:"exponent"`
		} `json:"credits"`
	} `json:"balance"`
	Tranches []tranche `json:"tranches"`
	Promos   []tranche `json:"promo_tranches"`
}
type tranche struct {
	ID        string                `json:"id"`
	Program   string                `json:"program_id"`
	Name      string                `json:"name"`
	Remaining *int64                `json:"remaining_amount_minor_units"`
	Granted   *int64                `json:"granted_amount_minor_units"`
	Currency  string                `json:"currency"`
	ExpiresAt string                `json:"expires_at"`
	Scope     *provider.CreditScope `json:"scope"`
}

func (r creditsResponse) normalize(org string) (*provider.CreditBalance, error) {
	exponent := 2
	amount := r.Amount
	if r.Balance != nil && r.Balance.Credits != nil {
		amount = r.Balance.Credits.Amount
		if r.Balance.Credits.Exponent != nil {
			exponent = *r.Balance.Credits.Exponent
		}
	}
	if amount == nil || *amount < 0 || exponent < 0 || exponent > 4 || len(r.Currency) != 3 || strings.ToUpper(r.Currency) != r.Currency {
		return nil, errors.New("invalid Console credit balance or currency")
	}
	makeMoney := func(n int64, currency string) provider.Money {
		return provider.Money{AmountMinor: n, Currency: currency, Exponent: exponent}
	}
	out := &provider.CreditBalance{OrganizationID: org, Balance: makeMoney(*amount, r.Currency), Grants: []provider.CreditGrant{}}
	for _, t := range append(r.Tranches, r.Promos...) {
		if t.Remaining == nil || *t.Remaining < 0 || (t.Granted != nil && *t.Granted < 0) {
			return nil, errors.New("invalid Console credit grant amount")
		}
		currency := t.Currency
		if currency == "" {
			currency = r.Currency
		}
		if currency != r.Currency {
			return nil, errors.New("inconsistent Console grant currency")
		}
		g := provider.CreditGrant{ID: t.ID, Program: t.Program, Name: t.Name, Scope: t.Scope, Remaining: makeMoney(*t.Remaining, currency)}
		if t.Granted != nil {
			m := makeMoney(*t.Granted, currency)
			g.Granted = &m
		}
		if at, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil {
			at = at.UTC()
			g.ExpiresAt = &at
		}
		out.Grants = append(out.Grants, g)
	}
	return out, nil
}
