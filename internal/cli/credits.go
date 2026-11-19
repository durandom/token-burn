package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/provider/claudeplatform"
	"github.com/durandom/token-burn/internal/store"
	"github.com/spf13/cobra"
)

func newAuthCommand(configPath *string) *cobra.Command {
	root := &cobra.Command{Use: "auth", Short: "Import provider credentials"}
	var account string
	cmd := &cobra.Command{Use: "claude-platform", Short: "Import Console sessionKey from stdin into Keychain or the configured private file", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig(*configPath)
		if err != nil {
			return err
		}
		var file, selected, organization string
		found := false
		for _, a := range cfg.Accounts {
			if a.Provider != claudeplatform.ID || (account != "" && account != a.ID) {
				continue
			}
			if found {
				return errors.New("multiple Console accounts; select --account")
			}
			selected, file, organization, found = a.ID, a.CredentialsFile, a.ProviderAccountID, true
		}
		if !found {
			return errors.New("configure a claude-platform account first")
		}
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 2050))
		if err != nil || len(data) > 2049 {
			return errors.New("cannot read Console sessionKey from stdin (maximum 2048 bytes)")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), cfg.HTTPTimeout)
		defer cancel()
		p := claudeplatform.New()
		p.ReadSession = func(context.Context, string, string) (string, error) { return string(data), nil }
		snap, err := p.Fetch(ctx, provider.Account{ID: selected, ProviderAccountID: organization})
		if err != nil {
			return err
		}
		if err := claudeplatform.ImportSession(ctx, selected, file, string(data)); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Console session verified and imported: API balance %s · organization %s\n", snap.CreditBalance.Balance, snap.CreditBalance.OrganizationID)
		return nil
	}}
	cmd.Flags().StringVar(&account, "account", "", "configured Console account ID")
	root.AddCommand(cmd)
	return root
}

func newCreditsCommand(configPath *string) *cobra.Command {
	var name, account string
	var jsonOut bool
	cmd := &cobra.Command{Use: "credits", Short: "Show stored API monetary balances and credit grants", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig(*configPath)
		if err != nil {
			return err
		}
		db, err := store.Open(cmd.Context(), cfg.DatabasePath)
		if err != nil {
			return err
		}
		defer db.Close()
		out := []store.CreditBalanceSnapshot{}
		for _, a := range cfg.Accounts {
			if (name != "" && a.Provider != name) || (account != "" && a.ID != account) {
				continue
			}
			c, err := db.LatestCreditBalance(cmd.Context(), a.Provider, a.ID)
			if err != nil {
				return err
			}
			if c != nil {
				out = append(out, *c)
			}
		}
		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), out)
		}
		for _, s := range out {
			fmt.Fprintf(cmd.OutOrStdout(), "%s/%s API balance %s · observed %s\n", s.Provider, s.AccountID, s.Credits.Balance, s.ObservedAt.Local().Format(time.RFC3339))
			for _, g := range s.Credits.Grants {
				label := g.Name
				if label == "" {
					label = g.Program
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s remaining", label, g.Remaining)
				if g.Granted != nil {
					fmt.Fprintf(cmd.OutOrStdout(), " of %s", g.Granted)
				}
				fmt.Fprintf(cmd.OutOrStdout(), " · expires %s\n", localResetTime(g.ExpiresAt))
			}
		}
		if len(out) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No stored monetary credit balances.")
		}
		return nil
	}}
	cmd.Flags().StringVar(&name, "provider", "", "provider filter")
	cmd.Flags().StringVar(&account, "account", "", "account ID filter")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON including observation time and all grants")
	return cmd
}
