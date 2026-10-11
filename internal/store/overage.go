package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

const overageSchema = `
CREATE TABLE overage_credit_snapshots (
 provider TEXT NOT NULL, account_id TEXT NOT NULL, observed_at TEXT NOT NULL,
 data TEXT,
 PRIMARY KEY(provider,account_id,observed_at));
`

func insertOverageCredits(ctx context.Context, tx *sql.Tx, snap provider.Snapshot) error {
	if snap.Provider != "codex" && snap.OverageCredits == nil {
		return nil
	}
	var data any
	if snap.OverageCredits != nil {
		b, err := safeJSON(snap.OverageCredits)
		if err != nil {
			return err
		}
		data = b
	}
	// Persist an absent Codex field too, so a later omission cannot show an old
	// balance as though it was included in the latest usage observation.
	_, err := tx.ExecContext(ctx, `INSERT INTO overage_credit_snapshots(provider,account_id,observed_at,data) VALUES(?,?,?,?) ON CONFLICT(provider,account_id,observed_at) DO UPDATE SET data=excluded.data`, snap.Provider, snap.AccountID, snap.ObservedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), data)
	return err
}

type OverageCreditSnapshot struct {
	Provider   string                   `json:"provider"`
	AccountID  string                   `json:"account_id"`
	ObservedAt time.Time                `json:"observed_at"`
	Credits    *provider.OverageCredits `json:"credits"`
}

func (s *Store) OverageCreditHistory(ctx context.Context, name, account string) ([]OverageCreditSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT observed_at,data FROM overage_credit_snapshots WHERE provider=? AND account_id=? ORDER BY observed_at`, name, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OverageCreditSnapshot{}
	for rows.Next() {
		var observed string
		var data sql.NullString
		if err := rows.Scan(&observed, &data); err != nil {
			return nil, err
		}
		at, err := parseTime(observed)
		if err != nil {
			return nil, err
		}
		item := OverageCreditSnapshot{Provider: name, AccountID: account, ObservedAt: at}
		if data.Valid {
			if err := json.Unmarshal([]byte(data.String), &item.Credits); err != nil {
				return nil, err
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) LatestOverageCredits(ctx context.Context, name, account string) (*provider.OverageCredits, error) {
	var data sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT data FROM overage_credit_snapshots WHERE provider=? AND account_id=? ORDER BY observed_at DESC LIMIT 1`, name, account).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !data.Valid {
		return nil, nil
	}
	var out provider.OverageCredits
	if err := json.Unmarshal([]byte(data.String), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
