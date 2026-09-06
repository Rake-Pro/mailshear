package store

import (
	"database/sql"
	"fmt"

	"github.com/Rake-Pro/mailshear/internal/headers"
	"github.com/Rake-Pro/mailshear/internal/keep"
)

var v1 = []string{
	`CREATE TABLE accounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		host TEXT NOT NULL,
		username TEXT NOT NULL
	)`,
	`CREATE TABLE folders (
		account_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		uidvalidity INTEGER NOT NULL DEFAULT 0,
		last_uid INTEGER NOT NULL DEFAULT 0,
		special_use TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (account_id, name)
	)`,
	`CREATE TABLE messages (
		account_id INTEGER NOT NULL,
		folder TEXT NOT NULL,
		uid INTEGER NOT NULL,
		gm_msgid INTEGER NOT NULL DEFAULT 0,
		message_id TEXT NOT NULL DEFAULT '',
		sender_key TEXT NOT NULL DEFAULT '',
		domain_key TEXT NOT NULL DEFAULT '',
		from_display TEXT NOT NULL DEFAULT '',
		from_address TEXT NOT NULL DEFAULT '',
		list_id TEXT NOT NULL DEFAULT '',
		has_unsub INTEGER NOT NULL DEFAULT 0,
		method TEXT NOT NULL DEFAULT '',
		unsub_uris TEXT NOT NULL DEFAULT '[]',
		one_click INTEGER NOT NULL DEFAULT 0,
		flags TEXT NOT NULL DEFAULT '',
		internal_date TEXT,
		size INTEGER NOT NULL DEFAULT 0,
		subject TEXT NOT NULL DEFAULT '',
		gm_labels TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (account_id, folder, uid)
	)`,
	`CREATE TABLE senders (
		account_id INTEGER NOT NULL,
		sender_key TEXT NOT NULL,
		display TEXT NOT NULL DEFAULT '',
		domain_key TEXT NOT NULL DEFAULT '',
		list_id TEXT NOT NULL DEFAULT '',
		protected INTEGER NOT NULL DEFAULT 0,
		first_seen TEXT,
		last_seen TEXT,
		decision TEXT NOT NULL DEFAULT '',
		decided_at TEXT,
		unsub_status TEXT NOT NULL DEFAULT '',
		unsub_at TEXT,
		unsub_run_id TEXT NOT NULL DEFAULT '',
		last_uid_at_decision INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (account_id, sender_key)
	)`,
	`CREATE TABLE runs (
		id TEXT PRIMARY KEY,
		account_id INTEGER NOT NULL,
		started_at TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		plan_path TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX idx_messages_sender ON messages(account_id, sender_key)`,
	`CREATE INDEX idx_messages_has_unsub ON messages(account_id, has_unsub)`,
}

// v2 records, per message, which transactional category its subject matched,
// so apply can refuse to delete receipts and security mail whatever the
// sender. Empty means "not transactional".
var v2 = []string{
	`ALTER TABLE messages ADD COLUMN keep TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX idx_messages_keep ON messages(account_id, keep)`,
}

// v3 records when each account was last scanned, so the accounts screen can
// say how fresh the stored sender list is.
var v3 = []string{
	`ALTER TABLE accounts ADD COLUMN last_scan_at TEXT NOT NULL DEFAULT ''`,
}

// migration is one schema version: statements first, then an optional Go step
// for work SQL cannot express.
type migration struct {
	stmts []string
	fn    func(*sql.Tx) error
}

var migrations = []migration{
	{stmts: v1},
	{stmts: v2, fn: backfillKeep},
	{stmts: v3},
}

// backfillKeepBatch is how many rows one backfill pass reads at a time. The
// filter is stable under the updates, so a plain LIMIT/OFFSET walk is enough.
const backfillKeepBatch = 500

// backfillKeep classifies the subjects already in the database. Rows scanned
// before v2 kept a subject only for bulk mail, so the mixed rows of a sender
// stay unclassified until the next scan; that is safe, because the live
// subject is checked again at apply time.
func backfillKeep(tx *sql.Tx) error {
	type row struct {
		accountID int64
		folder    string
		uid       int64
		category  string
	}
	for offset := 0; ; offset += backfillKeepBatch {
		rows, err := tx.Query(`
			SELECT account_id, folder, uid, subject FROM messages
			WHERE subject != ''
			ORDER BY account_id, folder, uid
			LIMIT ? OFFSET ?`, backfillKeepBatch, offset)
		if err != nil {
			return fmt.Errorf("store: backfill keep: %w", err)
		}
		var (
			hits []row
			seen int
		)
		for rows.Next() {
			var r row
			var subject string
			if err := rows.Scan(&r.accountID, &r.folder, &r.uid, &subject); err != nil {
				rows.Close()
				return fmt.Errorf("store: backfill keep: scan: %w", err)
			}
			seen++
			if ok, cat := keep.Match(headers.DecodeSubject(subject)); ok {
				r.category = cat
				hits = append(hits, r)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("store: backfill keep: %w", err)
		}
		rows.Close()

		for _, r := range hits {
			_, err := tx.Exec(
				`UPDATE messages SET keep = ? WHERE account_id = ? AND folder = ? AND uid = ?`,
				r.category, r.accountID, r.folder, r.uid)
			if err != nil {
				return fmt.Errorf("store: backfill keep: update: %w", err)
			}
		}
		if seen < backfillKeepBatch {
			return nil
		}
	}
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	for v := version + 1; v <= len(migrations); v++ {
		if err := s.applyMigration(v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(v int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migration %d: %w", v, err)
	}
	defer tx.Rollback()

	mg := migrations[v-1]
	for _, stmt := range mg.stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: apply migration %d: %w", v, err)
		}
	}
	if mg.fn != nil {
		if err := mg.fn(tx); err != nil {
			return fmt.Errorf("store: apply migration %d: %w", v, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, v); err != nil {
		return fmt.Errorf("store: record migration %d: %w", v, err)
	}
	return tx.Commit()
}
