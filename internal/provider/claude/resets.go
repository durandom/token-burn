package claude

import "github.com/durandom/token-burn/internal/provider"

func mapResetProgram(snap *provider.Snapshot, payload usageResponse) {
	program := payload.CedarEmber
	if program == nil {
		if payload.JuniperTide != nil {
			snap.ResetDetails = &provider.ResetDetails{Metadata: map[string]any{"juniper_tide": payload.JuniperTide}}
		}
		return
	}
	snap.ResetDetails = &provider.ResetDetails{Metadata: map[string]any{"cedar_ember": program, "juniper_tide": payload.JuniperTide}}
	// Surface-ineligible responses have empty grants: do not interpret them as
	// disappearance or redemption of previously observed credits.
	if program.IneligibleReason == "surface" {
		return
	}
	snap.ResetDetails.CreditsComplete = true
	snap.Resets = &provider.ResetSummary{}
	for _, grant := range program.Grants {
		c := grant.ResetCredit
		c.Title = grant.Label
		c.GrantedAt = grant.StartsAt
		c.ExpiresAt = grant.EndsAt
		c.Supported = program.Eligible
		c.Status = "available"
		if c.ResetsLeft <= 0 {
			c.Status = "used"
		} else if c.ExpiresAt != nil && !c.ExpiresAt.After(snap.ObservedAt) {
			c.Status = "expired"
		}
		snap.ResetDetails.Credits = append(snap.ResetDetails.Credits, c)
		if c.Status == "available" {
			snap.Resets.AvailableCount += c.ResetsLeft
			if c.UsableNow && !c.Paused {
				snap.Resets.ApplicableAvailableCount += c.ResetsLeft
			}
		}
	}
}
