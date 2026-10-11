package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/durandom/token-burn/internal/provider"
)

const resetSchema = `
CREATE TABLE reset_credits (
 provider TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
 status TEXT NOT NULL, granted_at TEXT, expires_at TEXT, redeemed_at TEXT,
 observed_at TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(provider,account_id,id));
CREATE TABLE reset_events (
 provider TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
 credit_id TEXT NOT NULL, kind TEXT NOT NULL, occurred_at TEXT NOT NULL,
 source TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(provider,account_id,id));
CREATE TABLE plan_limit_periods (
 provider TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
 starts_at TEXT NOT NULL, ends_at TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(provider,account_id,id));
CREATE TABLE reset_state (
 provider TEXT NOT NULL, account_id TEXT NOT NULL,
 summary TEXT, metadata TEXT, plan_metadata TEXT, attempted_at TEXT, detail_errors TEXT,
 observed_at TEXT NOT NULL,
 PRIMARY KEY(provider,account_id));
`

func safeJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var raw map[string]any
	if err = json.Unmarshal(b, &raw); err != nil {
		return "", err
	}
	return marshalRedactedRaw(raw)
}
func insertResets(ctx context.Context, tx *sql.Tx, snap provider.Snapshot) error {
	if snap.Resets == nil && snap.ResetDetails == nil && snap.PlanLimitHistory == nil && snap.ResetDetailsAttemptedAt == nil {
		return nil
	}
	var previousObservation string
	var previousMetadata sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT observed_at,metadata FROM reset_state WHERE provider=? AND account_id=?`, snap.Provider, snap.AccountID).Scan(&previousObservation, &previousMetadata)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previousObservation != "" {
		previous, err := parseTime(previousObservation)
		if err != nil {
			return err
		}
		if snap.ObservedAt.Before(previous) {
			return nil
		}
	}
	var summary, metadata, plans any
	if snap.Resets != nil {
		b, e := safeJSON(snap.Resets)
		if e != nil {
			return e
		}
		summary = b
	}
	if snap.ResetDetails != nil && snap.ResetDetails.Metadata != nil {
		merged := map[string]any{}
		if previousMetadata.Valid {
			if err := json.Unmarshal([]byte(previousMetadata.String), &merged); err != nil {
				return err
			}
		}
		for k, v := range snap.ResetDetails.Metadata {
			merged[k] = v
		}
		b, e := safeJSON(merged)
		if e != nil {
			return e
		}
		metadata = b
	}
	if snap.PlanLimitHistory != nil {
		p := *snap.PlanLimitHistory
		p.Periods = nil
		b, e := safeJSON(p)
		if e != nil {
			return e
		}
		plans = b
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reset_state(provider,account_id,summary,metadata,plan_metadata,attempted_at,detail_errors,observed_at) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(provider,account_id) DO UPDATE SET observed_at=excluded.observed_at,detail_errors=COALESCE(excluded.detail_errors,detail_errors),summary=COALESCE(excluded.summary,summary),metadata=COALESCE(excluded.metadata,metadata),plan_metadata=COALESCE(excluded.plan_metadata,plan_metadata),attempted_at=COALESCE(excluded.attempted_at,attempted_at)`, snap.Provider, snap.AccountID, summary, metadata, plans, nullTime(snap.ResetDetailsAttemptedAt), resetErrors(snap), formatTime(snap.ObservedAt))
	if err != nil {
		return err
	}
	if snap.ResetDetails != nil {
		events := append([]provider.ResetEvent(nil), snap.ResetDetails.Events...)
		for _, c := range snap.ResetDetails.Credits {
			if c.ID == "" {
				return errors.New("reset credit id is required")
			}
			var previous string
			var observed string
			err := tx.QueryRowContext(ctx, `SELECT data,observed_at FROM reset_credits WHERE provider=? AND account_id=? AND id=?`, snap.Provider, snap.AccountID, c.ID).Scan(&previous, &observed)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if snap.Provider == "claude" {
				if previous == "" && c.GrantedAt != nil {
					events = append(events, provider.ResetEvent{ID: c.ID + ":granted", CreditID: c.ID, Kind: "granted", OccurredAt: *c.GrantedAt, Source: "api", Count: c.ResetsTotal})
				} else if previous != "" {
					var old provider.ResetCredit
					if err := json.Unmarshal([]byte(previous), &old); err != nil {
						return err
					}
					before, err := parseTime(observed)
					if err != nil {
						return err
					}
					if snap.ObservedAt.After(before) && old.Status != "unavailable" && old.ResetsLeft > c.ResetsLeft {
						events = append(events, provider.ResetEvent{ID: c.ID + ":used:" + formatTime(snap.ObservedAt), CreditID: c.ID, Kind: "used", OccurredAt: provider.Timestamp{Time: snap.ObservedAt.UTC()}, Source: "inferred", Count: old.ResetsLeft - c.ResetsLeft})
						// An observed timestamp is not a provider redemption timestamp.
					}
				}
			}
			b, err := safeJSON(c)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO reset_credits VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(provider,account_id,id) DO UPDATE SET status=excluded.status,granted_at=excluded.granted_at,expires_at=excluded.expires_at,redeemed_at=excluded.redeemed_at,observed_at=excluded.observed_at,data=excluded.data WHERE excluded.observed_at>=reset_credits.observed_at`, snap.Provider, snap.AccountID, c.ID, c.Status, nullTime(c.GrantedAt.UTCValue()), nullTime(c.ExpiresAt.UTCValue()), nullTime(c.RedeemedAt.UTCValue()), formatTime(snap.ObservedAt), b)
			if err != nil {
				return err
			}
		}
		if snap.ResetDetails.CreditsComplete {
			if err := reconcileMissingCredits(ctx, tx, snap); err != nil {
				return err
			}
		}
		for _, e := range events {
			if e.ID == "" || e.OccurredAt.IsZero() || (e.Kind != "granted" && e.Kind != "used") || (e.Source != "api" && e.Source != "inferred") {
				return errors.New("invalid reset event")
			}
			b, err := safeJSON(e)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO reset_events VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(provider,account_id,id) DO UPDATE SET credit_id=excluded.credit_id,kind=excluded.kind,occurred_at=excluded.occurred_at,source=excluded.source,data=excluded.data`, snap.Provider, snap.AccountID, e.ID, e.CreditID, e.Kind, formatTime(e.OccurredAt.Time), e.Source, b)
			if err != nil {
				return err
			}
		}
	}
	if snap.PlanLimitHistory != nil {
		for _, p := range snap.PlanLimitHistory.Periods {
			if p.ID == "" {
				return errors.New("plan period id is required")
			}
			b, err := safeJSON(p)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO plan_limit_periods VALUES(?,?,?,?,?,?) ON CONFLICT(provider,account_id,id) DO UPDATE SET starts_at=excluded.starts_at,ends_at=excluded.ends_at,data=excluded.data`, snap.Provider, snap.AccountID, p.ID, formatTime(p.StartsAt.Time), formatTime(p.EndsAt.Time), b)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

type ResetHistory struct {
	Provider         string                    `json:"provider"`
	AccountID        string                    `json:"account_id"`
	Summary          *provider.ResetSummary    `json:"summary"`
	Credits          []provider.ResetCredit    `json:"credits"`
	Events           []provider.ResetEvent     `json:"events"`
	PlanLimitHistory provider.PlanLimitHistory `json:"plan_limit_history"`
	Metadata         map[string]any            `json:"metadata"`
	Errors           []string                  `json:"errors"`
	AttemptedAt      *time.Time                `json:"attempted_at"`
}

func (s *Store) ResetHistory(ctx context.Context, name, account string) (ResetHistory, error) {
	out := ResetHistory{Provider: name, AccountID: account, Credits: []provider.ResetCredit{}, Events: []provider.ResetEvent{}}
	var summary, metadata, plans, attempted, detailErrors sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT summary,metadata,plan_metadata,attempted_at,detail_errors FROM reset_state WHERE provider=? AND account_id=?`, name, account).Scan(&summary, &metadata, &plans, &attempted, &detailErrors)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	for _, v := range []struct {
		value  sql.NullString
		target any
	}{{summary, &out.Summary}, {metadata, &out.Metadata}, {plans, &out.PlanLimitHistory}} {
		if v.value.Valid {
			if err := json.Unmarshal([]byte(v.value.String), v.target); err != nil {
				return out, err
			}
		}
	}
	if detailErrors.Valid {
		if err := json.Unmarshal([]byte(detailErrors.String), &out.Errors); err != nil {
			return out, err
		}
	}
	if attempted.Valid {
		t, err := parseTime(attempted.String)
		if err != nil {
			return out, err
		}
		out.AttemptedAt = &t
	}
	for _, q := range []struct {
		table, order string
		add          func(string) error
	}{
		{"reset_credits", "id", func(b string) error {
			var c provider.ResetCredit
			if err := json.Unmarshal([]byte(b), &c); err != nil {
				return err
			}
			out.Credits = append(out.Credits, c)
			return nil
		}},
		{"reset_events", "occurred_at,id", func(b string) error {
			var e provider.ResetEvent
			if err := json.Unmarshal([]byte(b), &e); err != nil {
				return err
			}
			out.Events = append(out.Events, e)
			return nil
		}},
		{"plan_limit_periods", "starts_at,id", func(b string) error {
			var p provider.PlanLimitPeriod
			if err := json.Unmarshal([]byte(b), &p); err != nil {
				return err
			}
			out.PlanLimitHistory.Periods = append(out.PlanLimitHistory.Periods, p)
			return nil
		}},
	} {
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf("SELECT data FROM %s WHERE provider=? AND account_id=? ORDER BY %s", q.table, q.order), name, account)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var b string
			if err = rows.Scan(&b); err == nil {
				err = q.add(b)
			}
			if err != nil {
				rows.Close()
				return out, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
func (s *Store) ResetDetailsAfter(ctx context.Context, name, account string) (*time.Time, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT attempted_at FROM reset_state WHERE provider=? AND account_id=?`, name, account).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	t, err := parseTime(raw.String)
	if err != nil {
		return nil, err
	}
	t = t.Add(provider.ResetDetailsInterval)
	return &t, nil
}

// EnrichResetSnapshot attaches persisted details when slow polling is skipped.
// Call after InsertSnapshot so cached Claude grants never create inferred uses.
func (s *Store) EnrichResetSnapshot(ctx context.Context, snap *provider.Snapshot) error {
	h, err := s.ResetHistory(ctx, snap.Provider, snap.AccountID)
	if err != nil {
		return err
	}
	if h.Summary != nil {
		snap.Resets = h.Summary
	}
	snap.ResetDetailsErrors = h.Errors
	if len(h.Credits) > 0 || len(h.Events) > 0 || h.Metadata != nil {
		snap.ResetDetails = &provider.ResetDetails{Credits: h.Credits, Events: h.Events, Metadata: h.Metadata}
	}
	return nil
}

func resetErrors(snap provider.Snapshot) any {
	if snap.ResetDetailsAttemptedAt == nil {
		return nil
	}
	b, _ := json.Marshal(snap.ResetDetailsErrors)
	return string(b)
}

// Absence from an authoritative list changes availability, never infers a use.
func reconcileMissingCredits(ctx context.Context, tx *sql.Tx, snap provider.Snapshot) error {
	seen := map[string]bool{}
	for _, c := range snap.ResetDetails.Credits {
		seen[c.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,data FROM reset_credits WHERE provider=? AND account_id=? AND status='available' AND observed_at<=?`, snap.Provider, snap.AccountID, formatTime(snap.ObservedAt))
	if err != nil {
		return err
	}
	var missing []provider.ResetCredit
	for rows.Next() {
		var id, b string
		if err := rows.Scan(&id, &b); err != nil {
			rows.Close()
			return err
		}
		if !seen[id] {
			var c provider.ResetCredit
			if err := json.Unmarshal([]byte(b), &c); err != nil {
				rows.Close()
				return err
			}
			missing = append(missing, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range missing {
		c.Status = "unavailable"
		b, err := safeJSON(c)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE reset_credits SET status=?,data=?,observed_at=? WHERE provider=? AND account_id=? AND id=?`, c.Status, b, formatTime(snap.ObservedAt), snap.Provider, snap.AccountID, c.ID)
		if err != nil {
			return err
		}
	}
	return nil
}
