package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/store"
	"github.com/spf13/cobra"
)

func newResetsCommand(configPath *string) *cobra.Command {
	var name, account string
	var jsonOut bool
	cmd := &cobra.Command{Use: "resets", Short: "List stored reset credits, grant/use events, and plan-limit history", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		db, err := store.Open(cmd.Context(), cfg.DatabasePath)
		if err != nil {
			return err
		}
		defer db.Close()
		out := []store.ResetHistory{}
		for _, a := range cfg.Accounts {
			if (name != "" && name != a.Provider) || (account != "" && account != a.ID) {
				continue
			}
			h, err := db.ResetHistory(cmd.Context(), a.Provider, a.ID)
			if err != nil {
				return err
			}
			out = append(out, h)
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
				fmt.Fprintf(cmd.OutOrStdout(), "  plan period %s %s–%s %.2f%% complete=%t\n", p.ID, localResetTime(&p.StartsAt.Time), localResetTime(&p.EndsAt.Time), float64(p.UsedBasisPoints)/100, p.AccountingComplete)
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

func attachResetCapacity(ctx context.Context, cfg config.Config, results []forecastOutput, now time.Time) error {
	if cfg.OTel.Read.Mode == "openobserve" {
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
		if h.Summary != nil {
			c := forecast.Capacity(h.Summary, h.Credits, h.Events, now)
			r.ResetCapacity = &c
		}
	}
	return nil
}
