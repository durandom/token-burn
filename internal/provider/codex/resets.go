package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

// Reset discovery is GET-only. Never add consume/redeem requests here.
func (p *Provider) getResetJSON(ctx context.Context, credential resolvedCredential, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.baseURL(), "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Access)
	req.Header.Set("ChatGPT-Account-Id", credential.AccountID)
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("reset detail request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reset detail HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxResponseBytes {
		return fmt.Errorf("reset detail response exceeds size limit")
	}
	return json.Unmarshal(b, out)
}
func (p *Provider) fetchResetDetails(ctx context.Context, credential resolvedCredential, acct provider.Account, snap *provider.Snapshot) {
	if acct.ResetDetailsAfter != nil && snap.ObservedAt.Before(*acct.ResetDetailsAfter) {
		return
	}
	p.resetMu.Lock()
	key := acct.ID + "\x00" + credential.AccountID
	if snap.ObservedAt.Before(p.resetNext[key]) {
		p.resetMu.Unlock()
		return
	}
	if p.resetNext == nil {
		p.resetNext = map[string]time.Time{}
	}
	p.resetNext[key] = snap.ObservedAt.Add(provider.ResetDetailsInterval)
	p.resetMu.Unlock()
	now := snap.ObservedAt
	snap.ResetDetailsAttemptedAt = &now
	var list struct {
		Credits                        []provider.ResetCredit `json:"credits"`
		AvailableCount                 int                    `json:"available_count"`
		TotalEarnedCount               int                    `json:"total_earned_count"`
		ImmediateResetPurchaseEligible bool                   `json:"immediate_reset_purchase_eligible"`
		HistoryEnabled                 bool                   `json:"history_enabled"`
	}
	if err := p.getResetJSON(ctx, credential, "/wham/rate-limit-reset-credits", &list); err != nil {
		snap.ResetDetailsErrors = append(snap.ResetDetailsErrors, "credits: "+err.Error())
	} else if list.Credits == nil {
		snap.ResetDetailsErrors = append(snap.ResetDetailsErrors, "credits: missing credit list")
	} else {
		for i := range list.Credits {
			c := &list.Credits[i]
			c.ResetsTotal = 1
			if c.Status == "available" {
				c.ResetsLeft = 1
			}
		}
		snap.ResetDetails = &provider.ResetDetails{Credits: list.Credits, CreditsComplete: true, Metadata: map[string]any{"available_count": list.AvailableCount, "total_earned_count": list.TotalEarnedCount, "immediate_reset_purchase_eligible": list.ImmediateResetPurchaseEligible, "history_enabled": list.HistoryEnabled}}
	}
	var events []provider.ResetEvent
	cursor := ""
	seen := map[string]bool{}
	complete := false
	for page := 0; page < 100; page++ {
		var history struct {
			Events      []provider.ResetEvent `json:"events"`
			NextCursor  *string               `json:"next_cursor"`
			WindowStart *provider.Timestamp   `json:"window_start"`
			AsOf        *provider.Timestamp   `json:"as_of"`
		}
		path := "/wham/rate-limit-reset-credits/history"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		if err := p.getResetJSON(ctx, credential, path, &history); err != nil {
			snap.ResetDetailsErrors = append(snap.ResetDetailsErrors, "history: "+err.Error())
			break
		}
		for _, e := range history.Events {
			if e.Kind != "granted" && e.Kind != "used" {
				continue
			}
			e.CreditID = strings.TrimSuffix(e.ID, ":"+e.Kind)
			e.Source = "api"
			e.Count = 1
			events = append(events, e)
		}
		if snap.ResetDetails == nil {
			snap.ResetDetails = &provider.ResetDetails{Metadata: map[string]any{}}
		}
		if snap.ResetDetails.Metadata == nil {
			snap.ResetDetails.Metadata = map[string]any{}
		}
		snap.ResetDetails.Metadata["window_start"] = history.WindowStart
		snap.ResetDetails.Metadata["as_of"] = history.AsOf
		if history.NextCursor == nil || *history.NextCursor == "" {
			complete = true
			break
		}
		cursor = *history.NextCursor
		if seen[cursor] {
			break
		}
		seen[cursor] = true
	}
	if !complete {
		snap.ResetDetailsErrors = append(snap.ResetDetailsErrors, "history pagination incomplete")
	}
	if len(events) > 0 {
		snap.ResetDetails.Events = events
	}
	var plans provider.PlanLimitHistory
	if err := p.getResetJSON(ctx, credential, "/wham/usage/plan_limit_history?days=7", &plans); err != nil {
		snap.ResetDetailsErrors = append(snap.ResetDetailsErrors, "plan history: "+err.Error())
	} else {
		snap.PlanLimitHistory = &plans
	}
}
