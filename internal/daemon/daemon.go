package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/durandom/token-burn/internal/config"
	"github.com/durandom/token-burn/internal/forecast"
	"github.com/durandom/token-burn/internal/otel"
	usageprovider "github.com/durandom/token-burn/internal/provider"
	"github.com/durandom/token-burn/internal/provider/antigravity"
	"github.com/durandom/token-burn/internal/provider/claude"
	"github.com/durandom/token-burn/internal/provider/codex"
	"github.com/durandom/token-burn/internal/provider/copilot"
	"github.com/durandom/token-burn/internal/store"
)

type Options struct {
	Config                 config.Config
	Providers              map[string]usageprovider.Provider
	Recorder               otel.Recorder
	Now                    func() time.Time
	CommandRunner          CommandRunner
	CredentialRefreshState *CredentialRefreshState
}

type Backoff struct {
	Base     time.Duration
	Max      time.Duration
	failures int
}

type PollResult struct {
	Snapshots []usageprovider.Snapshot
	Errors    []PollError
}

type PollError struct {
	Provider   string
	AccountID  string
	Code       string
	HTTPStatus int
	Err        error
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type execCommandRunner struct{}

type CredentialRefreshState struct {
	mu      sync.Mutex
	lastRun map[string]time.Time
}

const (
	minRateLimitCooldown = 5 * time.Minute
	maxRateLimitCooldown = time.Hour
	authRefreshCooldown  = 30 * time.Minute
	authRefreshTimeout   = 2 * time.Minute
)

func Run(ctx context.Context, opts Options) error {
	if opts.Config.PollInterval <= 0 {
		opts.Config.PollInterval = config.DefaultPollInterval
	}
	db, err := store.Open(ctx, opts.Config.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	var recorder otel.Recorder
	var exporter *otel.Exporter
	if opts.Config.OTel.Enabled {
		exporter, err = otel.NewOTLP(ctx, otel.Config{
			Endpoint:       opts.Config.OTel.Endpoint,
			ExportInterval: opts.Config.OTel.ExportInterval,
			ServiceVersion: "dev",
		})
		if err != nil {
			return err
		}
		defer exporter.Shutdown(context.Background())
		recorder = exporter
	}
	if opts.Recorder != nil {
		recorder = opts.Recorder
	}
	if opts.CredentialRefreshState == nil {
		opts.CredentialRefreshState = &CredentialRefreshState{}
	}

	backoff := Backoff{Base: opts.Config.PollInterval, Max: 15 * time.Minute}
	for {
		result, err := PollOnce(ctx, db, opts.withRecorder(recorder))
		if err != nil {
			return err
		}
		delay := backoff.NextDelay(shouldBackoff(result))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func shouldBackoff(result PollResult) bool {
	return len(result.Errors) > 0 && len(result.Snapshots) == 0
}

func (b *Backoff) NextDelay(failed bool) time.Duration {
	if b.Base <= 0 {
		b.Base = config.DefaultPollInterval
	}
	if b.Max <= 0 {
		b.Max = 15 * time.Minute
	}
	if !failed {
		b.failures = 0
		return b.Base
	}
	b.failures++
	delay := b.Base
	for i := 1; i < b.failures; i++ {
		delay *= 2
		if delay >= b.Max {
			return b.Max
		}
	}
	if delay > b.Max {
		return b.Max
	}
	return delay
}

func PollOnce(ctx context.Context, db *store.Store, opts Options) (PollResult, error) {
	var result PollResult
	now := opts.now()
	for _, acct := range opts.Config.Accounts {
		startedAt := opts.now()
		skip, err := shouldSkipRateLimitedAccount(ctx, db, acct.Provider, acct.ID, startedAt, opts.Config.PollInterval)
		if err != nil {
			return result, err
		}
		if skip {
			continue
		}
		client, ok := opts.providerFor(acct.Provider)
		if !ok {
			pollErr := PollError{
				Provider:  acct.Provider,
				AccountID: acct.ID,
				Code:      string(usageprovider.ErrUnsupportedAccountShape),
				Err:       errors.New("unsupported provider"),
			}
			result.Errors = append(result.Errors, pollErr)
			if err := recordPollError(ctx, db, pollErr, startedAt, opts.now()); err != nil {
				return result, err
			}
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
			pollErr := pollErrorFrom(acct.Provider, acct.ID, err)
			if shouldAttemptCredentialRefresh(opts, pollErr, startedAt) {
				markCredentialRefreshAttempt(opts, pollErr, startedAt)
				if refreshErr := refreshCredentials(ctx, opts, pollErr); refreshErr == nil {
					snap, err = client.Fetch(ctx, usageprovider.Account{
						Provider:          acct.Provider,
						ID:                acct.ID,
						ProviderAccountID: acct.ProviderAccountID,
						AuthFile:          acct.AuthFile,
						CredentialsFile:   acct.CredentialsFile,
					})
					if err == nil {
						if err := db.InsertSnapshot(ctx, snap, store.InsertOptions{}); err != nil {
							return result, err
						}
						if err := recordPollSuccess(ctx, db, snap.Provider, snap.AccountID, startedAt, opts.now()); err != nil {
							return result, err
						}
						result.Snapshots = append(result.Snapshots, snap)
						if opts.Recorder != nil {
							otel.EmitSnapshot(ctx, opts.Recorder, snap, now)
							emitForecasts(ctx, opts.Recorder, db, snap, now)
						}
						continue
					}
					pollErr = pollErrorFrom(acct.Provider, acct.ID, err)
				}
			}
			result.Errors = append(result.Errors, pollErr)
			if err := recordPollError(ctx, db, pollErr, startedAt, opts.now()); err != nil {
				return result, err
			}
			if opts.Recorder != nil {
				otel.EmitPollError(ctx, opts.Recorder, pollErr.Provider, pollErr.AccountID, pollErr.Code)
			}
			continue
		}
		if err := db.InsertSnapshot(ctx, snap, store.InsertOptions{}); err != nil {
			return result, err
		}
		if err := recordPollSuccess(ctx, db, snap.Provider, snap.AccountID, startedAt, opts.now()); err != nil {
			return result, err
		}
		result.Snapshots = append(result.Snapshots, snap)
		if opts.Recorder != nil {
			otel.EmitSnapshot(ctx, opts.Recorder, snap, now)
			emitForecasts(ctx, opts.Recorder, db, snap, now)
		}
	}
	return result, nil
}

func shouldAttemptCredentialRefresh(opts Options, pollErr PollError, now time.Time) bool {
	if strings.ToLower(strings.TrimSpace(pollErr.Provider)) != "antigravity" {
		return false
	}
	if pollErr.Code != string(usageprovider.ErrAuthExpired) {
		return false
	}
	if opts.CredentialRefreshState == nil {
		return false
	}
	key := credentialRefreshKey(pollErr)
	opts.CredentialRefreshState.mu.Lock()
	defer opts.CredentialRefreshState.mu.Unlock()
	lastRun := opts.CredentialRefreshState.lastRun[key]
	return lastRun.IsZero() || now.Sub(lastRun) >= authRefreshCooldown
}

func markCredentialRefreshAttempt(opts Options, pollErr PollError, now time.Time) {
	if opts.CredentialRefreshState == nil {
		return
	}
	key := credentialRefreshKey(pollErr)
	opts.CredentialRefreshState.mu.Lock()
	defer opts.CredentialRefreshState.mu.Unlock()
	if opts.CredentialRefreshState.lastRun == nil {
		opts.CredentialRefreshState.lastRun = map[string]time.Time{}
	}
	opts.CredentialRefreshState.lastRun[key] = now
}

func credentialRefreshKey(pollErr PollError) string {
	return strings.ToLower(strings.TrimSpace(pollErr.Provider)) + "/" + strings.TrimSpace(pollErr.AccountID)
}

func refreshCredentials(ctx context.Context, opts Options, pollErr PollError) error {
	switch strings.ToLower(strings.TrimSpace(pollErr.Provider)) {
	case "antigravity":
		refreshCtx, cancel := context.WithTimeout(ctx, authRefreshTimeout)
		defer cancel()
		return opts.commandRunner().Run(refreshCtx, "agy", "models")
	default:
		return nil
	}
}

func providerRateLimitBaseCooldown(pollInterval time.Duration) time.Duration {
	if pollInterval <= 0 {
		pollInterval = config.DefaultPollInterval
	}
	cooldown := 3 * pollInterval
	if cooldown < minRateLimitCooldown {
		return minRateLimitCooldown
	}
	return cooldown
}

func shouldSkipRateLimitedAccount(ctx context.Context, db *store.Store, providerName, accountID string, now time.Time, pollInterval time.Duration) (bool, error) {
	if pollInterval <= 0 {
		pollInterval = config.DefaultPollInterval
	}
	baseCooldown := providerRateLimitBaseCooldown(pollInterval)
	if baseCooldown <= 0 {
		return false, nil
	}
	since := now.Add(-maxRateLimitCooldown)
	runs, err := db.PollRuns(ctx, store.PollRunFilter{
		Provider:  providerName,
		AccountID: accountID,
		Since:     &since,
	})
	if err != nil {
		return false, err
	}
	if len(runs) == 0 {
		return false, nil
	}
	cooldown, latest := rateLimitCooldownForRuns(runs, baseCooldown, maxRateLimitCooldown)
	if cooldown <= 0 {
		return false, nil
	}
	return now.Sub(latest.StartedAt) < cooldown, nil
}

func rateLimitCooldownForRuns(runs []store.PollRun, baseCooldown, maxCooldown time.Duration) (time.Duration, store.PollRun) {
	if len(runs) == 0 || baseCooldown <= 0 {
		return 0, store.PollRun{}
	}
	latest := runs[len(runs)-1]
	if latest.Status != "error" || latest.ErrorCode != string(usageprovider.ErrRateLimited) {
		return 0, latest
	}
	consecutive := 0
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Status != "error" || runs[i].ErrorCode != string(usageprovider.ErrRateLimited) {
			break
		}
		consecutive++
	}
	cooldown := baseCooldown
	for i := 1; i < consecutive; i++ {
		cooldown *= 2
		if maxCooldown > 0 && cooldown >= maxCooldown {
			return maxCooldown, latest
		}
	}
	if maxCooldown > 0 && cooldown > maxCooldown {
		return maxCooldown, latest
	}
	return cooldown, latest
}

func recordPollSuccess(ctx context.Context, db *store.Store, providerName, accountID string, startedAt, finishedAt time.Time) error {
	latencyMS := int(finishedAt.Sub(startedAt).Milliseconds())
	return db.RecordPollRun(ctx, store.PollRun{
		StartedAt:  startedAt,
		FinishedAt: &finishedAt,
		Provider:   providerName,
		AccountID:  accountID,
		Status:     "success",
		LatencyMS:  &latencyMS,
	})
}

func recordPollError(ctx context.Context, db *store.Store, pollErr PollError, startedAt, finishedAt time.Time) error {
	latencyMS := int(finishedAt.Sub(startedAt).Milliseconds())
	return db.RecordPollRun(ctx, store.PollRun{
		StartedAt:    startedAt,
		FinishedAt:   &finishedAt,
		Provider:     pollErr.Provider,
		AccountID:    pollErr.AccountID,
		Status:       "error",
		HTTPStatus:   optionalInt(pollErr.HTTPStatus),
		ErrorCode:    pollErr.Code,
		ErrorMessage: redactErrorMessage(pollErr.Err),
		LatencyMS:    &latencyMS,
	})
}

func optionalInt(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

func redactErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, marker := range []string{"Bearer ", "access_token", "refresh_token", "id_token", "authorization", "cookie", "session"} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(marker)) {
			return "[REDACTED]"
		}
	}
	return msg
}

func (o Options) providerFor(name string) (usageprovider.Provider, bool) {
	if o.Providers != nil {
		if provider, ok := o.Providers[strings.ToLower(strings.TrimSpace(name))]; ok {
			return provider, true
		}
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex":
		return codex.New(), true
	case "claude", "claude_code":
		return claude.New(), true
	case "copilot", "github_copilot":
		return copilot.New(), true
	case "antigravity", "agy":
		return antigravity.New(), true
	default:
		return nil, false
	}
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o Options) withRecorder(recorder otel.Recorder) Options {
	o.Recorder = recorder
	return o
}

func (o Options) commandRunner() CommandRunner {
	if o.CommandRunner != nil {
		return o.CommandRunner
	}
	return execCommandRunner{}
}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, resolveExecutable(name), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	return nil
}

func resolveExecutable(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	if name != "agy" {
		return name
	}
	for _, candidate := range []string{"/opt/homebrew/bin/agy", "/usr/local/bin/agy", "/usr/bin/agy"} {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return name
}

func pollErrorFrom(providerName, accountID string, err error) PollError {
	out := PollError{
		Provider:  providerName,
		AccountID: accountID,
		Err:       err,
	}
	var perr *usageprovider.Error
	if errors.As(err, &perr) {
		out.Code = string(perr.Code)
		out.HTTPStatus = perr.HTTPStatus
	} else {
		out.Code = "unknown"
	}
	return out
}

func emitForecasts(ctx context.Context, recorder otel.Recorder, db *store.Store, snap usageprovider.Snapshot, now time.Time) {
	for _, win := range snap.Windows {
		since := now.Add(-7 * 24 * time.Hour)
		samples, err := db.History(ctx, store.HistoryFilter{
			Provider:   snap.Provider,
			AccountID:  snap.AccountID,
			WindowName: win.Name,
			Since:      &since,
		})
		if err != nil {
			continue
		}
		observations := make([]forecast.Observation, 0, len(samples))
		for _, sample := range samples {
			observations = append(observations, forecast.Observation{
				ObservedAt:  sample.ObservedAt,
				UsedPercent: sample.UsedPercent,
				ResetAt:     sample.ResetAt,
			})
		}
		otel.EmitForecast(ctx, recorder, otel.ForecastMetric{
			Provider:  snap.Provider,
			AccountID: snap.AccountID,
			Window:    win.Name,
			PlanType:  snap.PlanType,
			Source:    snap.Source,
			Result:    forecast.Calculate(observations, now),
		})
	}
}

func (e PollError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s/%s: %s", e.Provider, e.AccountID, e.Code)
	}
	return fmt.Sprintf("%s/%s: %s: %v", e.Provider, e.AccountID, e.Code, e.Err)
}
