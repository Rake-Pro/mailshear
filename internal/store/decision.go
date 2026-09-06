package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Decision is one sender's review outcome, as produced by the TUI.
type Decision struct {
	SenderKey     string
	Unsubscribe   bool
	DeleteMatched bool
	DeleteAll     bool
	// IncludeKept waives the transactional-mail rule for this sender, so
	// receipts and security mail from it are deleted along with the rest.
	IncludeKept bool
}

// String renders the flags as the comma-joined set stored in senders.decision.
func (d Decision) String() string {
	var parts []string
	if d.Unsubscribe {
		parts = append(parts, "unsubscribe")
	}
	if d.DeleteMatched {
		parts = append(parts, "delete_matched")
	}
	if d.DeleteAll {
		parts = append(parts, "delete_all")
	}
	if d.IncludeKept {
		parts = append(parts, "include_kept")
	}
	return strings.Join(parts, ",")
}

// RecordDecisions stores the review outcome for each sender, stamping the run
// id and the highest UID seen for that sender so a later scan can tell whether
// the sender kept sending after the decision.
func (s *Store) RecordDecisions(ctx context.Context, accountID int64, runID string, decs []Decision) error {
	if len(decs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: record decisions: %w", err)
	}
	defer tx.Rollback()

	maxUID, err := tx.PrepareContext(ctx,
		`SELECT COALESCE(MAX(uid), 0) FROM messages WHERE account_id = ? AND sender_key = ?`)
	if err != nil {
		return fmt.Errorf("store: record decisions: prepare max uid: %w", err)
	}
	defer maxUID.Close()

	upsert, err := tx.PrepareContext(ctx, `
		INSERT INTO senders (account_id, sender_key, first_seen, last_seen,
			decision, decided_at, unsub_run_id, last_uid_at_decision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, sender_key) DO UPDATE SET
			decision = excluded.decision,
			decided_at = excluded.decided_at,
			unsub_run_id = excluded.unsub_run_id,
			last_uid_at_decision = excluded.last_uid_at_decision
	`)
	if err != nil {
		return fmt.Errorf("store: record decisions: prepare upsert: %w", err)
	}
	defer upsert.Close()

	now := timeToText(time.Now())
	for _, d := range decs {
		var uid int64
		if err := maxUID.QueryRowContext(ctx, accountID, d.SenderKey).Scan(&uid); err != nil {
			return fmt.Errorf("store: record decisions: max uid for %s: %w", d.SenderKey, err)
		}
		_, err := upsert.ExecContext(ctx, accountID, d.SenderKey, now, now,
			d.String(), now, runID, uid)
		if err != nil {
			return fmt.Errorf("store: record decision for %s: %w", d.SenderKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: record decisions: %w", err)
	}
	return nil
}

// ListProtected returns the sender keys marked protected for the account.
func (s *Store) ListProtected(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sender_key FROM senders WHERE account_id = ? AND protected = 1 ORDER BY sender_key`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: list protected: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("store: list protected: scan: %w", err)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
