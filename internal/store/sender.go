package store

import (
	"context"
	"fmt"
	"time"
)

type Sender struct {
	AccountID         int64
	SenderKey         string
	Display           string
	DomainKey         string
	ListID            string
	Protected         bool
	FirstSeen         time.Time
	LastSeen          time.Time
	Decision          string
	DecidedAt         time.Time
	UnsubStatus       string
	UnsubAt           time.Time
	UnsubRunID        string
	LastUIDAtDecision uint32
}

func (s *Store) TouchSenders(ctx context.Context, accountID int64, seen map[string]Sender) error {
	if len(seen) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: touch senders: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO senders (account_id, sender_key, display, domain_key, list_id, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, sender_key) DO UPDATE SET
			display = excluded.display,
			domain_key = excluded.domain_key,
			list_id = excluded.list_id,
			first_seen = CASE
				WHEN excluded.first_seen IS NULL THEN senders.first_seen
				WHEN senders.first_seen IS NULL THEN excluded.first_seen
				ELSE MIN(senders.first_seen, excluded.first_seen) END,
			last_seen = CASE
				WHEN excluded.last_seen IS NULL THEN senders.last_seen
				WHEN senders.last_seen IS NULL THEN excluded.last_seen
				ELSE MAX(senders.last_seen, excluded.last_seen) END
	`)
	if err != nil {
		return fmt.Errorf("store: touch senders: prepare: %w", err)
	}
	defer stmt.Close()

	for key, sen := range seen {
		_, err := stmt.ExecContext(ctx, accountID, key, sen.Display, sen.DomainKey, sen.ListID,
			timeToText(sen.FirstSeen), timeToText(sen.LastSeen))
		if err != nil {
			return fmt.Errorf("store: touch sender: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) SetProtected(ctx context.Context, accountID int64, senderKey string, protected bool) error {
	now := timeToText(time.Now())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO senders (account_id, sender_key, protected, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, sender_key) DO UPDATE SET protected = excluded.protected
	`, accountID, senderKey, boolToInt(protected), now, now)
	if err != nil {
		return fmt.Errorf("store: set protected: %w", err)
	}
	return nil
}

func (s *Store) PurgeMessages(ctx context.Context, accountID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: purge messages: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE account_id = ?`, accountID); err != nil {
		return fmt.Errorf("store: purge messages: delete messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE folders SET last_uid = 0 WHERE account_id = ?`, accountID); err != nil {
		return fmt.Errorf("store: purge messages: reset folder cursors: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: purge messages: %w", err)
	}

	// Rewrite the file so the deleted subjects and addresses are not left
	// behind in free pages, then fold the rewrite out of the WAL.
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("store: purge messages: vacuum: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("store: purge messages: wal checkpoint: %w", err)
	}
	return nil
}

// MessageCount reports how many cached messages the account has.
func (s *Store) MessageCount(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE account_id = ?`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count messages: %w", err)
	}
	return n, nil
}

// SetUnsubResult records the outcome of an unsubscribe attempt for a sender.
func (s *Store) SetUnsubResult(ctx context.Context, accountID int64, senderKey, status, runID string) error {
	now := timeToText(time.Now())
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO senders (account_id, sender_key, unsub_status, unsub_at, unsub_run_id, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, sender_key) DO UPDATE SET
			unsub_status = excluded.unsub_status,
			unsub_at = excluded.unsub_at,
			unsub_run_id = excluded.unsub_run_id
	`, accountID, senderKey, status, now, runID, now, now)
	if err != nil {
		return fmt.Errorf("store: set unsub result for %s: %w", senderKey, err)
	}
	return nil
}
