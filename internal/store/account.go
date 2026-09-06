package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Account struct {
	ID       int64
	Name     string
	Host     string
	Username string
	// LastScan is when a scan last finished for this account. Zero means it
	// has never been scanned.
	LastScan time.Time
}

func (s *Store) UpsertAccount(ctx context.Context, name, host, username string) (Account, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO accounts (name, host, username) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET host = excluded.host, username = excluded.username
	`, name, host, username)
	if err != nil {
		return Account{}, fmt.Errorf("store: upsert account %s: %w", name, err)
	}
	return s.GetAccount(ctx, name)
}

func (s *Store) GetAccount(ctx context.Context, name string) (Account, error) {
	var a Account
	var lastScan string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, host, username, last_scan_at FROM accounts WHERE name = ?`, name).
		Scan(&a.ID, &a.Name, &a.Host, &a.Username, &lastScan)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, fmt.Errorf("store: account %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: get account %s: %w", name, err)
	}
	a.LastScan = textToTime(lastScan)
	return a, nil
}

func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, host, username, last_scan_at FROM accounts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var a Account
		var lastScan string
		if err := rows.Scan(&a.ID, &a.Name, &a.Host, &a.Username, &lastScan); err != nil {
			return nil, fmt.Errorf("store: scan account: %w", err)
		}
		a.LastScan = textToTime(lastScan)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetLastScan records that a scan finished for the account just now.
func (s *Store) SetLastScan(ctx context.Context, accountID int64, t time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET last_scan_at = ? WHERE id = ?`, t.UTC().Format(time.RFC3339), accountID)
	if err != nil {
		return fmt.Errorf("store: set last scan: %w", err)
	}
	return nil
}

// AccountStats is what the accounts screen shows about one mailbox without
// connecting to it.
type AccountStats struct {
	Messages int
	// StillSending counts senders unsubscribed in an earlier run that have
	// sent bulk mail since.
	StillSending int
	LastScan     time.Time
}

// stillSendingQuery counts senders whose newest bulk message is later than the
// unsubscribe that was supposed to stop them. Timestamps are stored as UTC
// RFC 3339, so comparing them as text is the same as comparing the times.
const stillSendingQuery = `
SELECT COUNT(*) FROM (
	SELECT sn.sender_key
	FROM senders sn
	JOIN messages m ON m.account_id = sn.account_id AND m.sender_key = sn.sender_key AND m.has_unsub = 1
	WHERE sn.account_id = ? AND sn.unsub_status IN ('ok', 'probable')
	GROUP BY sn.sender_key
	HAVING MAX(m.internal_date) > COALESCE(NULLIF(sn.unsub_at, ''), sn.decided_at, '')
)
`

// Stats summarises the stored state for one account.
func (s *Store) Stats(ctx context.Context, accountID int64) (AccountStats, error) {
	var st AccountStats
	var lastScan string
	err := s.db.QueryRowContext(ctx,
		`SELECT last_scan_at FROM accounts WHERE id = ?`, accountID).Scan(&lastScan)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountStats{}, fmt.Errorf("store: account %d: %w", accountID, ErrNotFound)
	}
	if err != nil {
		return AccountStats{}, fmt.Errorf("store: stats: %w", err)
	}
	st.LastScan = textToTime(lastScan)

	if st.Messages, err = s.MessageCount(ctx, accountID); err != nil {
		return AccountStats{}, err
	}
	if err := s.db.QueryRowContext(ctx, stillSendingQuery, accountID).Scan(&st.StillSending); err != nil {
		return AccountStats{}, fmt.Errorf("store: stats: still sending: %w", err)
	}
	return st, nil
}

// DeleteAccount drops every row this account owns: messages, senders, folder
// cursors, run records and the account itself. It is the "purge" half of
// removing an account and never touches the mail server.
func (s *Store) DeleteAccount(ctx context.Context, accountID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DELETE FROM messages WHERE account_id = ?`,
		`DELETE FROM senders WHERE account_id = ?`,
		`DELETE FROM folders WHERE account_id = ?`,
		`DELETE FROM runs WHERE account_id = ?`,
		`DELETE FROM accounts WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, accountID); err != nil {
			return fmt.Errorf("store: delete account: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete account: %w", err)
	}
	// Rewrite the file so the deleted subjects and addresses are not left
	// behind in free pages.
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("store: delete account: vacuum: %w", err)
	}
	return nil
}
