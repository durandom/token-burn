package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"github.com/durandom/token-burn/internal/otelread"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/provider/antigravity"
	"github.com/durandom/token-burn/internal/provider/claude"
	"github.com/durandom/token-burn/internal/provider/codex"
	"github.com/durandom/token-burn/internal/provider/copilot"
	"github.com/durandom/token-burn/internal/provider/xai"
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
	var verbose bool

	root := &cobra.Command{
		Use:           "token-burn",
		Short:         "Monitor live AI coding subscription quota usage",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "config file path")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "print diagnostic detail (timeout, rate-limit and reset timing)")
	root.AddCommand(newVersionCommand(build))
	root.AddCommand(newOnceCommand(&configPath, &verbose))
	root.AddCommand(newStatusCommand(&configPath))
	root.AddCommand(newHistoryCommand(&configPath))
	root.AddCommand(newForecastCommand(&configPath))
	root.AddCommand(newDaemonCommand(&configPath, &verbose))
	root.AddCommand(newInstallCommand(&configPath))
	root.AddCommand(newUninstallCommand())
	root.AddCommand(newServiceStatusCommand())
	root.AddCommand(newOTelTestCommand(&configPath))
	root.AddCommand(newOTelBackfillCommand(&configPath, build))
	root.AddCommand(newTUICommand(&configPath))
	root.AddCommand(newUpgradeCommand(build))

	return root
}

func newOTelBackfillCommand(configPath *string, build BuildInfo) *cobra.Command {
	var providerName string
	var accountID string
	var windowName string
	var fromRaw string
	var toRaw string
	var endpoint string
	var afterID int64
	var limit int
	var batchSize int
	var send bool

	cmd := &cobra.Command{
		Use:   "otel-backfill",
		Short: "Backfill stored usage samples with their original timestamps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if batchSize <= 0 {
				return errors.New("--batch-size must be positive")
			}
			if limit < 0 || afterID < 0 {
				return errors.New("--limit and --after-id must not be negative")
			}
			from, err := parseOptionalRFC3339("--from", fromRaw)
			if err != nil {
				return err
			}
			to, err := parseOptionalRFC3339("--to", toRaw)
			if err != nil {
				return err
			}
			if from != nil && to != nil && from.After(*to) {
				return errors.New("--from must not be after --to")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if cfg.OTel.Protocol != "http/protobuf" {
				return fmt.Errorf("otel-backfill requires otel.protocol = %q", "http/protobuf")
			}
			if endpoint == "" {
				endpoint = cfg.OTel.Endpoint
			}
			db, err := store.Open(cmd.Context(), cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer db.Close()

			var exporter *otel.HistoricalExporter
			if send {
				exporter, err = otel.NewHistoricalExporter(endpoint, build.Version, &http.Client{Timeout: cfg.HTTPTimeout})
				if err != nil {
					return err
				}
			}
			filter := store.HistoryFilter{
				Provider: providerName, AccountID: accountID, WindowName: windowName,
				Since: from, Until: to,
			}
			cursor := afterID
			totalSamples := 0
			totalPoints := 0
			firstID := int64(0)
			for {
				queryLimit := batchSize
				if limit > 0 && limit-totalSamples < queryLimit {
					queryLimit = limit - totalSamples
				}
				if queryLimit == 0 {
					break
				}
				samples, err := db.HistoryBatch(cmd.Context(), filter, cursor, queryLimit)
				if err != nil {
					return err
				}
				if len(samples) == 0 {
					break
				}
				if firstID == 0 {
					firstID = samples[0].ID
				}
				points := otel.HistoricalPointCount(samples)
				if send {
					points, err = exporter.Export(cmd.Context(), samples)
					if err != nil {
						return fmt.Errorf("export batch after id %d: %w", cursor, err)
					}
				}
				cursor = samples[len(samples)-1].ID
				totalSamples += len(samples)
				totalPoints += points
				if send {
					fmt.Fprintf(cmd.OutOrStdout(), "exported samples=%d points=%d last_id=%d\n", totalSamples, totalPoints, cursor)
				}
			}
			if !send {
				fmt.Fprintf(cmd.OutOrStdout(), "dry run: samples=%d points=%d first_id=%d last_id=%d; add --send to export\n", totalSamples, totalPoints, firstID, cursor)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "backfill complete: samples=%d points=%d first_id=%d last_id=%d\n", totalSamples, totalPoints, firstID, cursor)
			return nil
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "provider filter")
	cmd.Flags().StringVar(&accountID, "account", "", "account id filter")
	cmd.Flags().StringVar(&windowName, "window", "", "window filter")
	cmd.Flags().StringVar(&fromRaw, "from", "", "inclusive RFC3339 observed_at lower bound")
	cmd.Flags().StringVar(&toRaw, "to", "", "inclusive RFC3339 observed_at upper bound")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "OTLP HTTP endpoint override")
	cmd.Flags().Int64Var(&afterID, "after-id", 0, "resume after this SQLite sample id")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum samples (0 means all matching samples)")
	cmd.Flags().IntVar(&batchSize, "batch-size", 1000, "samples per OTLP request")
	cmd.Flags().BoolVar(&send, "send", false, "send data; without this flag only count matching samples")
	return cmd
}

func parseOptionalRFC3339(flagName, raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", flagName, err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func newTUICommand(configPath *string) *cobra.Command {
	var layoutName string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the live quota dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			layout, err := tokenburntui.ParseLayoutMode(layoutName)
			if err != nil {
				return err
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			_, err = tea.NewProgram(tokenburntui.NewModelWithLayout(cfg, layout), tea.WithAltScreen()).Run()
			return err
		},
	}
	cmd.Flags().StringVar(&layoutName, "layout", string(tokenburntui.LayoutAuto), "layout mode: auto, normal, compact, or ultra")
	return cmd
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
			spec, err := installSpec(binaryPath, *configPath)
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

func installSpec(binaryPath, configPath string) (service.Spec, error) {
	spec, err := service.DefaultSpec(binaryPath, configPath)
	if err != nil {
		return service.Spec{}, err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return service.Spec{}, err
	}
	spec.DatabasePath = cfg.DatabasePath
	return spec, nil
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

func newDaemonCommand(configPath *string, verbose *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the polling daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			opts := daemon.Options{Config: cfg}
			if verbose != nil && *verbose {
				opts.Verbose = cmd.OutOrStdout()
			}
			return daemon.Run(cmd.Context(), opts)
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

func newOnceCommand(configPath *string, verbose *bool) *cobra.Command {
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

			var diag io.Writer
			if verbose != nil && *verbose {
				// Diagnostics go to stdout as requested, but in --json mode
				// route to stderr so machine-readable output stays clean.
				diag = cmd.OutOrStdout()
				if jsonOut {
					diag = cmd.ErrOrStderr()
				}
			}
			var result onceResult
			if cfg.OTel.Read.Mode == "openobserve" {
				samples, err := latestStatusSamples(ctx, cfg, time.Now())
				if err != nil {
					return err
				}
				result = onceResult{Snapshots: snapshotsFromSamples(samples), Errors: []commandError{}}
			} else {
				result = runOnce(ctx, cfg, diag)
			}
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
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.HTTPTimeout)
			defer cancel()
			samples, err := latestStatusSamples(ctx, cfg, time.Now())
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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}

func latestStatusSamples(ctx context.Context, cfg config.Config, now time.Time) ([]store.Sample, error) {
	if cfg.OTel.Read.Mode == "openobserve" {
		remote, err := otelread.NewClient(cfg.OTel.Read).Fetch(ctx, now)
		return remote.Samples, err
	}
	db, localErr := store.Open(ctx, cfg.DatabasePath)
	var samples []store.Sample
	if localErr == nil {
		defer db.Close()
		for _, acct := range cfg.Accounts {
			latest, err := db.LatestSamples(ctx, acct.Provider, acct.ID)
			if err != nil {
				localErr = err
				break
			}
			samples = append(samples, latest...)
		}
	}
	latest := time.Time{}
	for _, sample := range samples {
		if sample.ObservedAt.After(latest) {
			latest = sample.ObservedAt
		}
	}
	staleAfter := 3 * cfg.PollInterval
	if staleAfter < 15*time.Minute {
		staleAfter = 15 * time.Minute
	}
	localFresh := !latest.IsZero() && now.Sub(latest) <= staleAfter
	if cfg.OTel.Read.Mode != "auto" || localFresh {
		return samples, localErr
	}
	remote, remoteErr := otelread.NewClient(cfg.OTel.Read).Fetch(ctx, now)
	if remoteErr != nil && localErr != nil {
		return nil, fmt.Errorf("local status: %v; OpenObserve fallback: %w", localErr, remoteErr)
	}
	return remote.Samples, remoteErr
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
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.HTTPTimeout)
			defer cancel()
			filter := store.HistoryFilter{
				Provider:   providerName,
				AccountID:  accountID,
				WindowName: windowName,
				Since:      since,
				Limit:      limit,
			}
			samples, err := historySamples(ctx, cfg, filter, time.Now())
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
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.HTTPTimeout)
			defer cancel()
			filter := store.HistoryFilter{
				Provider:   providerName,
				AccountID:  accountID,
				WindowName: windowName,
				Since:      since,
			}
			samples, err := historySamples(ctx, cfg, filter, now)
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

func historySamples(ctx context.Context, cfg config.Config, filter store.HistoryFilter, now time.Time) ([]store.Sample, error) {
	if cfg.OTel.Read.Mode == "openobserve" {
		start := now.Add(-cfg.OTel.Read.Lookback)
		if filter.Since != nil {
			start = *filter.Since
		}
		end := now
		if filter.Until != nil {
			end = *filter.Until
		}
		return otelread.NewClient(cfg.OTel.Read).FetchHistory(ctx, start, end, filter)
	}
	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.History(ctx, filter)
}

func snapshotsFromSamples(samples []store.Sample) []usageprovider.Snapshot {
	byAccount := map[string]*usageprovider.Snapshot{}
	keys := []string{}
	for _, sample := range samples {
		key := sample.Provider + "\x00" + sample.AccountID
		snapshot := byAccount[key]
		if snapshot == nil {
			keys = append(keys, key)
			snapshot = &usageprovider.Snapshot{Provider: sample.Provider, AccountID: sample.AccountID, PlanType: sample.PlanType, Source: sample.Source, ObservedAt: sample.ObservedAt}
			byAccount[key] = snapshot
		}
		if sample.ObservedAt.After(snapshot.ObservedAt) {
			snapshot.ObservedAt = sample.ObservedAt
		}
		snapshot.Windows = append(snapshot.Windows, usageprovider.Window{Name: sample.WindowName, UsedPercent: sample.UsedPercent, RemainingPercent: sample.RemainingPercent, ResetAt: sample.ResetAt, WindowSeconds: sample.WindowSeconds, LimitReached: sample.LimitReached})
	}
	sort.Strings(keys)
	out := make([]usageprovider.Snapshot, 0, len(keys))
	for _, key := range keys {
		out = append(out, *byAccount[key])
	}
	return out
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

func runOnce(ctx context.Context, cfg config.Config, diag io.Writer) onceResult {
	result := onceResult{
		Snapshots: []usageprovider.Snapshot{},
		Errors:    []commandError{},
	}
	if diag != nil {
		fmt.Fprintf(diag, "verbose: http_timeout=%s accounts=%d\n", cfg.HTTPTimeout, len(cfg.Accounts))
	}
	for _, acct := range cfg.Accounts {
		client, ok := providerFor(acct.Provider)
		if !ok {
			if diag != nil {
				fmt.Fprintf(diag, "verbose: %s/%s unsupported provider\n", acct.Provider, acct.ID)
			}
			result.Errors = append(result.Errors, commandError{
				Provider:  acct.Provider,
				AccountID: acct.ID,
				Message:   "unsupported provider",
			})
			continue
		}
		started := time.Now()
		snap, err := client.Fetch(ctx, usageprovider.Account{
			Provider:          acct.Provider,
			ID:                acct.ID,
			ProviderAccountID: acct.ProviderAccountID,
			AuthFile:          acct.AuthFile,
			CredentialsFile:   acct.CredentialsFile,
		})
		elapsed := time.Since(started)
		if diag != nil {
			fmt.Fprintf(diag, "verbose: %s/%s fetch elapsed=%s\n", acct.Provider, acct.ID, elapsed.Round(time.Millisecond))
		}
		if err != nil {
			cmdErr := commandErrorFromError(acct.Provider, acct.ID, err)
			if diag != nil {
				printVerboseError(diag, cmdErr)
			}
			result.Errors = append(result.Errors, cmdErr)
			continue
		}
		if diag != nil {
			printVerboseSnapshot(diag, snap, time.Now())
		}
		result.Snapshots = append(result.Snapshots, snap)
	}
	return result
}

func printVerboseError(w io.Writer, cmdErr commandError) {
	fmt.Fprintf(w, "verbose:   error code=%s http=%d\n", orNone(cmdErr.Code), cmdErr.HTTPStatus)
	if cmdErr.Code == string(usageprovider.ErrRateLimited) {
		fmt.Fprintln(w, "verbose:   rate limited; no server Retry-After captured. Backoff/cooldown is applied by the daemon, not by once.")
	}
}

func printVerboseSnapshot(w io.Writer, snap usageprovider.Snapshot, now time.Time) {
	for _, win := range snap.Windows {
		fmt.Fprintf(w, "verbose:   window %s: used=%.1f%%", win.Name, win.UsedPercent)
		if win.RemainingPercent != nil {
			fmt.Fprintf(w, " remaining=%.1f%%", *win.RemainingPercent)
		}
		if win.WindowSeconds != nil {
			fmt.Fprintf(w, " window=%s", (time.Duration(*win.WindowSeconds) * time.Second))
		}
		if win.ResetAt != nil {
			fmt.Fprintf(w, " reset=%s in=%s", win.ResetAt.Local().Format(time.RFC3339), win.ResetAt.Sub(now).Round(time.Second))
		}
		fmt.Fprintf(w, " limit_reached=%t\n", win.LimitReached)
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

func providerFor(name string) (usageprovider.Provider, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex":
		return codex.New(), true
	case "claude", "claude_code":
		return claude.New(), true
	case "copilot", "github_copilot":
		return copilot.New(), true
	case "antigravity", "agy":
		return antigravity.New(), true
	case "xai", "grok":
		return xai.New(), true
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
