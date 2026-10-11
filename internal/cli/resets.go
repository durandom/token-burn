package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/store"
	"github.com/spf13/cobra"
)

func newResetsCommand(configPath *string) *cobra.Command {
	var name, account string
	var jsonOut bool
	cmd := &cobra.Command{Use: "resets", Short: "List stored reset credits, grant/use events, and plan-limit history", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig(*configPath)
		if err != nil {
			return err
		}
		db, err := store.Open(cmd.Context(), cfg.DatabasePath)
		if err != nil {
			return err
		}
		defer db.Close()
		out := []resetOutput{}
		for _, a := range cfg.Accounts {
			if (name != "" && name != a.Provider) || (account != "" && account != a.ID) {
				continue
			}
			h, err := db.ResetHistory(cmd.Context(), a.Provider, a.ID)
			if err != nil {
				return err
			}
			live, err := db.LatestOverageCredits(cmd.Context(), a.Provider, a.ID)
			if err != nil {
				return err
			}
			out = append(out, resetOutput{ResetHistory: h, OverageCredits: live, ConfiguredCreditCapacity: forecast.CreditExpiry(cfg.CreditGrants[a.Provider], live, time.Now())})
		}
		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), out)
		}
		for _, h := range out {
			c := forecast.Capacity(h.Summary, h.Credits, h.Events, time.Now())
			owned := "unknown"
			if c.OwnedResets != nil {
				owned = fmt.Sprint(*c.OwnedResets)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s/%s resets owned: %s; next expiry: %s; last used: %s\n", h.Provider, h.AccountID, owned, localResetTime(c.NextExpiry), localResetTime(c.LastUsed))
			if h.OverageCredits == nil && h.ConfiguredCreditCapacity != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "  live overage credits: unknown")
			}
			if h.OverageCredits != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  live overage credits: %s\n", overageBalanceText(h.OverageCredits))
			}
			if c := h.ConfiguredCreditCapacity; c != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  credit expiry (configured): %s amount expiring=%s\n", localResetTime(&c.NextExpiry), creditAmountText(c.AmountExpiring))
				for _, g := range c.Grants {
					fmt.Fprintf(cmd.OutOrStdout(), "  configured grant %q amount=%s expires=%s\n", g.Label, creditAmountText(g.Amount), localResetTime(&g.ExpiresAt))
				}
			}
			for _, err := range h.Errors {
				fmt.Fprintf(cmd.OutOrStdout(), "  detail warning: %s\n", err)
			}
			for _, c := range h.Credits {
				fmt.Fprintf(cmd.OutOrStdout(), "  credit %s %s left=%d granted=%s expires=%s redeemed=%s\n", c.ID, c.Status, c.ResetsLeft, localResetTime(c.GrantedAt.UTCValue()), localResetTime(c.ExpiresAt.UTCValue()), localResetTime(c.RedeemedAt.UTCValue()))
			}
			for _, e := range h.Events {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s %s credit=%s count=%d source=%s\n", localResetTime(&e.OccurredAt.Time), e.Kind, e.CreditID, e.Count, e.Source)
			}
			for _, p := range h.PlanLimitHistory.Periods {
				fmt.Fprintf(cmd.OutOrStdout(), "  plan period %s %s–%s %.2f%% complete=%t\n", p.ID, localResetTime(&p.StartsAt.Time), localResetTime(&p.EndsAt.Time), p.UsedBasisPoints/100, p.AccountingComplete)
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&name, "provider", "", "provider filter")
	cmd.Flags().StringVar(&account, "account", "", "account id filter")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON, including breakdowns and coverage metadata")
	return cmd
}
func localResetTime(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return t.Local().Format(time.RFC3339)
}

func attachStoredCapacity(ctx context.Context, cfg config.Config, results []forecastOutput, now time.Time) error {
	if cfg.OTel.Read.Mode == "openobserve" {
		for i := range results {
			r := &results[i]
			r.ConfiguredCreditCapacity = forecast.CreditExpiry(cfg.CreditGrants[r.Provider], nil, now)
		}
		return nil
	}
	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	for i := range results {
		r := &results[i]
		h, err := db.ResetHistory(ctx, r.Provider, r.AccountID)
		if err != nil {
			return err
		}
		r.OverageCredits, err = db.LatestOverageCredits(ctx, r.Provider, r.AccountID)
		if err != nil {
			return err
		}
		r.ConfiguredCreditCapacity = forecast.CreditExpiry(cfg.CreditGrants[r.Provider], r.OverageCredits, now)
		if h.Summary != nil {
			c := forecast.Capacity(h.Summary, h.Credits, h.Events, now)
			r.ResetCapacity = &c
		}
	}
	return nil
}

func overageBalanceText(c *provider.OverageCredits) string {
	if c.Unlimited {
		return "unlimited"
	}
	if c.Balance == "" {
		return "unknown"
	}
	return c.Balance
}

type resetOutput struct {
	store.ResetHistory
	OverageCredits           *provider.OverageCredits           `json:"overage_credits,omitempty"`
	ConfiguredCreditCapacity *forecast.ConfiguredCreditCapacity `json:"configured_credit_capacity,omitempty"`
}

func creditAmountText(amount *float64) string {
	if amount == nil {
		return "unknown"
	}
	return fmt.Sprintf("%g", *amount)
}
