package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

const datedResetCapacitySchema = `ALTER TABLE reset_state ADD COLUMN capacity_observed_at TEXT;
 UPDATE reset_state SET capacity_observed_at=observed_at WHERE summary IS NOT NULL;`

const creditBalanceSchema = `CREATE TABLE IF NOT EXISTS credit_balance_snapshots (
 provider TEXT NOT NULL, account_id TEXT NOT NULL, observed_at TEXT NOT NULL,
 data TEXT NOT NULL, PRIMARY KEY(provider,account_id,observed_at));`

// The pre-release Console build used version 5 for credit balances.
func ensureDatedResetCapacity(ctx context.Context, tx *sql.Tx) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pragma_table_info('reset_state') WHERE name = 'capacity_observed_at'
	)`).Scan(&exists)
	if err != nil || exists {
		return err
	}
	_, err = tx.ExecContext(ctx, datedResetCapacitySchema)
	return err
}

// Trim the UTC suffix when ordering RFC3339Nano strings so whole seconds sort
// before fractional timestamps in the same second.
// LatestPollAttempt includes failures so a broken Console session is not
// retried on every daemon tick, even after the process restarts.
func (s *Store) LatestPollAttempt(ctx context.Context, name, account string) (*time.Time, error) {
	var at string
	if err := s.db.QueryRowContext(ctx, `SELECT started_at FROM poll_runs WHERE provider=? AND account_id=? ORDER BY rtrim(started_at, 'Z') DESC LIMIT 1`, name, account).Scan(&at); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	t, err := parseTime(at)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

type CreditBalanceSnapshot struct {
	Provider   string                  `json:"provider"`
	AccountID  string                  `json:"account_id"`
	ObservedAt time.Time               `json:"observed_at"`
	Credits    *provider.CreditBalance `json:"credits"`
}

func insertCreditBalance(ctx context.Context, tx *sql.Tx, snap provider.Snapshot) error {
	if snap.CreditBalance == nil {
		return nil
	}
	data, err := safeJSON(snap.CreditBalance)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_balance_snapshots VALUES(?,?,?,?) ON CONFLICT(provider,account_id,observed_at) DO UPDATE SET data=excluded.data`, snap.Provider, snap.AccountID, formatTime(snap.ObservedAt), data)
	return err
}

func (s *Store) LatestCreditBalance(ctx context.Context, name, account string) (*CreditBalanceSnapshot, error) {
	var data, observed string
	err := s.db.QueryRowContext(ctx, `SELECT observed_at,data FROM credit_balance_snapshots WHERE provider=? AND account_id=? ORDER BY rtrim(observed_at, 'Z') DESC LIMIT 1`, name, account).Scan(&observed, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &CreditBalanceSnapshot{Provider: name, AccountID: account}
	if out.ObservedAt, err = parseTime(observed); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(data), &out.Credits); err != nil {
		return nil, err
	}
	return out, nil
}
