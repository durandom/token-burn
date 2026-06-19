package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/daemon"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/otel"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/provider/claude"
	"github.com/durandom/token-burn/internal/provider/codex"
	"github.com/durandom/token-burn/internal/provider/copilot"
	"github.com/durandom/token-burn/internal/service"
	"github.com/durandom/token-burn/internal/store"
	tokenburntui "github.com/durandom/token-burn/internal/tui"
	"github.com/durandom/token-burn/internal/upgrade"
	"github.com/spf13/cobra"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func Execute(build BuildInfo) int {
	cmd := NewRootCommand(build)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func NewRootCommand(build BuildInfo) *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:           "token-burn",
		Short:         "Monitor live AI coding subscription quota usage",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "config file path")
	root.AddCommand(newVersionCommand(build))
	root.AddCommand(newOnceCommand(&configPath))
	root.AddCommand(newStatusCommand(&configPath))
	root.AddCommand(newHistoryCommand(&configPath))
	root.AddCommand(newForecastCommand(&configPath))
	root.AddCommand(newDaemonCommand(&configPath))
	root.AddCommand(newInstallCommand(&configPath))
	root.AddCommand(newUninstallCommand())
	root.AddCommand(newServiceStatusCommand())
	root.AddCommand(newOTelTestCommand(&configPath))
	root.AddCommand(newTUICommand(&configPath))
	root.AddCommand(newUpgradeCommand(build))

	return root
}

func newTUICommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the live quota dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			_, err = tea.NewProgram(tokenburntui.NewModel(cfg), tea.WithAltScreen()).Run()
			return err
		},
	}
}

func newUpgradeCommand(build BuildInfo) *cobra.Command {
	var repo string
	var version string
	var binaryPath string
	var force bool

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade token-burn from GitHub Releases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			result, err := upgrade.Run(ctx, upgrade.Options{
				Repo:       repo,
				Version:    version,
				Current:    build.Version,
				BinaryPath: binaryPath,
				Force:      force,
			})
			if err != nil {
				return err
			}
			if !result.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "token-burn is already up to date (%s)\n", result.From)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "upgraded token-burn %s -> %s at %s\n", result.From, result.To, result.BinaryPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", upgrade.DefaultRepo, "GitHub repository")
	cmd.Flags().StringVar(&version, "version", "latest", "release version or latest")
	cmd.Flags().StringVar(&binaryPath, "binary", "", "binary path to replace")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if the target version matches")
	return cmd
}

func newOTelTestCommand(configPath *string) *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "otel-test",
		Short: "Emit a synthetic OpenTelemetry metric",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			exporter, err := otel.NewOTLP(ctx, otel.Config{
				Endpoint:       cfg.OTel.Endpoint,
				ExportInterval: cfg.OTel.ExportInterval,
				ServiceVersion: "dev",
			})
			if err != nil {
				return err
			}
			defer exporter.Shutdown(context.Background())
			remaining := 99.0
			otel.EmitSnapshot(ctx, exporter, usageprovider.Snapshot{
				Provider:  "test",
				AccountID: "test",
				PlanType:  "test",
				Source:    "otel_test",
				Windows: []usageprovider.Window{{
					Name:             "test",
					UsedPercent:      1,
					RemainingPercent: &remaining,
				}},
			}, time.Now())
			if err := exporter.ForceFlush(ctx); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "emitted otel test metric")
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "OTel test timeout")
	return cmd
}

func newInstallCommand(configPath *string) *cobra.Command {
	var binaryPath string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install token-burn as a user service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := service.DefaultSpec(binaryPath, *configPath)
			if err != nil {
				return err
			}
			if err := service.Install(cmd.Context(), spec); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", spec.Label)
			return nil
		},
	}
	cmd.Flags().StringVar(&binaryPath, "binary", "", "binary path to run from the service")
	return cmd
}

func newUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the token-burn user service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := service.Uninstall(cmd.Context(), service.DefaultLabel); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "uninstalled %s\n", service.DefaultLabel)
			return nil
		},
	}
}

func newServiceStatusCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "service-status",
		Short: "Print token-burn user service status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := service.ServiceStatus(cmd.Context(), service.DefaultLabel)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "platform: %s\ninstalled: %t\nloaded: %t\npath: %s\n", status.Platform, status.Installed, status.Loaded, status.Path)
			if status.Message != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "message: %s\n", status.Message)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func newDaemonCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the polling daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			return daemon.Run(cmd.Context(), daemon.Options{Config: cfg})
		},
	}
}

func newVersionCommand(build BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printVersion(cmd.OutOrStdout(), build)
			return nil
		},
	}
}

func printVersion(w io.Writer, build BuildInfo) {
	if build.Version == "" {
		build.Version = "dev"
	}
	if build.Commit == "" {
		build.Commit = "none"
	}
	if build.Date == "" {
		build.Date = "unknown"
	}

	fmt.Fprintf(w, "token-burn %s\ncommit: %s\nbuilt: %s\n", build.Version, build.Commit, build.Date)
}

func newOnceCommand(configPath *string) *cobra.Command {
	var jsonOut bool
	var writeStore bool
	var rawJSON bool

	cmd := &cobra.Command{
		Use:   "once",
		Short: "Fetch current live usage once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.HTTPTimeout)
			defer cancel()

			result := runOnce(ctx, cfg)
			if writeStore {
				db, err := store.Open(cmd.Context(), cfg.DatabasePath)
				if err != nil {
					return err
				}
				defer db.Close()
				for _, snap := range result.Snapshots {
					if err := db.InsertSnapshot(cmd.Context(), snap, store.InsertOptions{StoreRawJSON: rawJSON}); err != nil {
						return err
					}
				}
			}

			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			printOnceText(cmd.OutOrStdout(), result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	cmd.Flags().BoolVar(&writeStore, "store", false, "write successful samples to the local database")
	cmd.Flags().BoolVar(&rawJSON, "raw-json", false, "store redacted raw provider JSON")
	return cmd
}

func newStatusCommand(configPath *string) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print latest stored usage status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			db, err := store.Open(cmd.Context(), cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer db.Close()

			var samples []store.Sample
			for _, acct := range cfg.Accounts {
				latest, err := db.LatestSamples(cmd.Context(), acct.Provider, acct.ID)
				if err != nil {
					return err
				}
				samples = append(samples, latest...)
			}
			if samples == nil {
				samples = []store.Sample{}
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), samples)
			}
			printSamplesText(cmd.OutOrStdout(), samples)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func newHistoryCommand(configPath *string) *cobra.Command {
	var providerName string
	var accountID string
	var windowName string
	var sinceRaw string
	var jsonOut bool
	var limit int

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Print stored usage history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			since, err := parseSince(sinceRaw, time.Now())
			if err != nil {
				return err
			}
			db, err := store.Open(cmd.Context(), cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer db.Close()

			samples, err := db.History(cmd.Context(), store.HistoryFilter{
				Provider:   providerName,
				AccountID:  accountID,
				WindowName: windowName,
				Since:      since,
				Limit:      limit,
			})
			if err != nil {
				return err
			}
			if samples == nil {
				samples = []store.Sample{}
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), samples)
			}
			printSamplesText(cmd.OutOrStdout(), samples)
			return nil
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "provider filter")
	cmd.Flags().StringVar(&accountID, "account", "", "account id filter")
	cmd.Flags().StringVar(&windowName, "window", "", "window filter")
	cmd.Flags().StringVar(&sinceRaw, "since", "24h", "history lookback duration")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum rows")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func newForecastCommand(configPath *string) *cobra.Command {
	var providerName string
	var accountID string
	var windowName string
	var sinceRaw string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "forecast",
		Short: "Forecast exhaustion from stored usage history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			now := time.Now()
			since, err := parseSince(sinceRaw, now)
			if err != nil {
				return err
			}
			db, err := store.Open(cmd.Context(), cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer db.Close()

			samples, err := db.History(cmd.Context(), store.HistoryFilter{
				Provider:   providerName,
				AccountID:  accountID,
				WindowName: windowName,
				Since:      since,
			})
			if err != nil {
				return err
			}
			results := forecastSamples(samples, now)
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), results)
			}
			printForecastText(cmd.OutOrStdout(), results)
			return nil
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "provider filter")
	cmd.Flags().StringVar(&accountID, "account", "", "account id filter")
	cmd.Flags().StringVar(&windowName, "window", "", "window filter")
	cmd.Flags().StringVar(&sinceRaw, "since", "7d", "forecast lookback duration")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

type onceResult struct {
	Snapshots []usageprovider.Snapshot `json:"snapshots"`
	Errors    []commandError           `json:"errors,omitempty"`
}

type commandError struct {
	Provider   string `json:"provider"`
	AccountID  string `json:"account_id"`
	Code       string `json:"code,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Message    string `json:"message"`
}

type forecastOutput struct {
	Provider                 string     `json:"provider"`
	AccountID                string     `json:"account_id"`
	WindowName               string     `json:"window_name"`
	ResetAt                  *time.Time `json:"reset_at,omitempty"`
	ComputedAt               time.Time  `json:"computed_at"`
	SampleCount              int        `json:"sample_count"`
	BurnRatePercentPerHour   *float64   `json:"burn_rate_percent_per_hour,omitempty"`
	ProjectedResetPercent    *float64   `json:"projected_reset_percent,omitempty"`
	Estimated90At            *time.Time `json:"estimated_90_at,omitempty"`
	Estimated100At           *time.Time `json:"estimated_100_at,omitempty"`
	Confidence               float64    `json:"confidence"`
	InsufficientDataReason   string     `json:"insufficient_data_reason,omitempty"`
	StableWindowObservedFrom *time.Time `json:"stable_window_observed_from,omitempty"`
}

func runOnce(ctx context.Context, cfg config.Config) onceResult {
	result := onceResult{
		Snapshots: []usageprovider.Snapshot{},
		Errors:    []commandError{},
	}
	for _, acct := range cfg.Accounts {
		client, ok := providerFor(acct.Provider)
		if !ok {
			result.Errors = append(result.Errors, commandError{
				Provider:  acct.Provider,
				AccountID: acct.ID,
				Message:   "unsupported provider",
			})
			continue
		}
		snap, err := client.Fetch(ctx, usageprovider.Account{
			Provider:          acct.Provider,
			ID:                acct.ID,
			ProviderAccountID: acct.ProviderAccountID,
			AuthFile:          acct.AuthFile,
			CredentialsFile:   acct.CredentialsFile,
		})
		if err != nil {
			result.Errors = append(result.Errors, commandErrorFromError(acct.Provider, acct.ID, err))
			continue
		}
		result.Snapshots = append(result.Snapshots, snap)
	}
	return result
}

func providerFor(name string) (usageprovider.Provider, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex":
		return codex.New(), true
	case "claude", "claude_code":
		return claude.New(), true
	case "copilot", "github_copilot":
		return copilot.New(), true
	default:
		return nil, false
	}
}

func commandErrorFromError(providerName, accountID string, err error) commandError {
	out := commandError{
		Provider:  providerName,
		AccountID: accountID,
		Message:   err.Error(),
	}
	var perr *usageprovider.Error
	if strings.TrimSpace(out.Message) == "" {
		out.Message = "unknown error"
	}
	if ok := errors.As(err, &perr); ok {
		out.Code = string(perr.Code)
		out.HTTPStatus = perr.HTTPStatus
	}
	return out
}

func forecastSamples(samples []store.Sample, computedAt time.Time) []forecastOutput {
	grouped := map[string][]forecast.Observation{}
	meta := map[string]store.Sample{}
	for _, sample := range samples {
		key := sample.Provider + "\x00" + sample.AccountID + "\x00" + sample.WindowName
		grouped[key] = append(grouped[key], forecast.Observation{
			ObservedAt:  sample.ObservedAt,
			UsedPercent: sample.UsedPercent,
			ResetAt:     sample.ResetAt,
		})
		meta[key] = sample
	}

	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := []forecastOutput{}
	for _, key := range keys {
		result := forecast.Calculate(grouped[key], computedAt)
		sample := meta[key]
		out = append(out, forecastOutput{
			Provider:                 sample.Provider,
			AccountID:                sample.AccountID,
			WindowName:               sample.WindowName,
			ResetAt:                  sample.ResetAt,
			ComputedAt:               result.ComputedAt,
			SampleCount:              result.SampleCount,
			BurnRatePercentPerHour:   result.BurnRatePercentPerHour,
			ProjectedResetPercent:    result.ProjectedResetPercent,
			Estimated90At:            result.Estimated90At,
			Estimated100At:           result.Estimated100At,
			Confidence:               result.Confidence,
			InsufficientDataReason:   result.InsufficientDataReason,
			StableWindowObservedFrom: result.StableResetWindowStartedAt,
		})
	}
	return out
}

func parseSince(raw string, now time.Time) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	d, err := parseLookbackDuration(raw)
	if err != nil {
		return nil, fmt.Errorf("parse --since: %w", err)
	}
	t := now.UTC().Add(-d)
	return &t, nil
}

func parseLookbackDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func printOnceText(w io.Writer, result onceResult) {
	for _, snap := range result.Snapshots {
		fmt.Fprintf(w, "%s/%s %s\n", snap.Provider, snap.AccountID, snap.PlanType)
		for _, win := range snap.Windows {
			fmt.Fprintf(w, "  %s: %.1f%%", win.Name, win.UsedPercent)
			if win.ResetAt != nil {
				fmt.Fprintf(w, " reset %s", win.ResetAt.Local().Format(time.RFC3339))
			}
			fmt.Fprintln(w)
		}
	}
	for _, err := range result.Errors {
		fmt.Fprintf(w, "error %s/%s: %s\n", err.Provider, err.AccountID, err.Message)
	}
}

func printSamplesText(w io.Writer, samples []store.Sample) {
	for _, sample := range samples {
		fmt.Fprintf(w, "%s %s/%s %s %.1f%%", sample.ObservedAt.Local().Format(time.RFC3339), sample.Provider, sample.AccountID, sample.WindowName, sample.UsedPercent)
		if sample.ResetAt != nil {
			fmt.Fprintf(w, " reset %s", sample.ResetAt.Local().Format(time.RFC3339))
		}
		fmt.Fprintln(w)
	}
}

func printForecastText(w io.Writer, results []forecastOutput) {
	for _, result := range results {
		fmt.Fprintf(w, "%s/%s %s", result.Provider, result.AccountID, result.WindowName)
		if result.BurnRatePercentPerHour != nil {
			fmt.Fprintf(w, " burn %.2f%%/h", *result.BurnRatePercentPerHour)
		}
		if result.ProjectedResetPercent != nil {
			fmt.Fprintf(w, " reset %.1f%%", *result.ProjectedResetPercent)
		}
		if result.Estimated100At != nil {
			fmt.Fprintf(w, " 100%% at %s", result.Estimated100At.Local().Format(time.RFC3339))
		}
		if result.InsufficientDataReason != "" {
			fmt.Fprintf(w, " (%s)", result.InsufficientDataReason)
		}
		fmt.Fprintln(w)
	}
}
