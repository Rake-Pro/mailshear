package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"
)

// v1DDL is the schema as it shipped before the keep column, written out here
// so the migration is exercised against a real v1 database rather than
// against whatever the current code would create.
var v1DDL = []string{
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
	`CREATE TABLE schema_migrations (version INTEGER NOT NULL)`,
	`INSERT INTO schema_migrations (version) VALUES (1)`,
}

// v1Fixture writes a database at schema version 1 with the given subjects and
// returns its path. Subjects are keyed by UID.
func v1Fixture(t *testing.T, subjects map[int]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailshear.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v1 database: %v", err)
	}
	defer db.Close()

	for _, stmt := range v1DDL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("v1 ddl: %v\n%s", err, stmt)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO accounts (id, name, host, username) VALUES (1, 'personal', 'localhost', 'user')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	for uid, subject := range subjects {
		hasUnsub := 1
		if subject == "" {
			hasUnsub = 0
		}
		_, err := db.Exec(`
			INSERT INTO messages (account_id, folder, uid, message_id, sender_key, domain_key,
				has_unsub, subject, size)
			VALUES (1, 'INBOX', ?, ?, 'addr:svc@pay.example', 'pay.example', ?, ?, 100)`,
			uid, "m"+strconv.Itoa(uid)+"@pay.example", hasUnsub, subject)
		if err != nil {
			t.Fatalf("insert message %d: %v", uid, err)
		}
	}
	return path
}

func TestMigrationV2BackfillsKeep(t *testing.T) {
	subjects := map[int]string{
		1: "Your receipt from Acme",
		2: "Weekly digest",
		3: "Order confirmation #4471",
		4: "", // no subject stored: nothing to classify
		5: "=?utf-8?q?Your_verification_code?=",
		6: "Newsletter receipts up 20%",
	}
	path := v1Fixture(t, subjects)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open (which migrates): %v", err)
	}
	defer st.Close()

	var version int
	if err := st.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}

	want := map[int]string{
		1: "receipt",
		2: "",
		3: "order",
		4: "",
		5: "security",
		6: "",
	}
	for uid, category := range want {
		var got string
		err := st.db.QueryRow(
			`SELECT keep FROM messages WHERE account_id = 1 AND folder = 'INBOX' AND uid = ?`, uid).Scan(&got)
		if err != nil {
			t.Fatalf("read keep for uid %d: %v", uid, err)
		}
		if got != category {
			t.Errorf("uid %d keep = %q, want %q (subject %q)", uid, got, category, subjects[uid])
		}
	}

	// Reopening is a no-op: the migration does not run twice.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	var n int
	if err := st2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != len(migrations) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(migrations))
	}
}

// TestMigrationV2BackfillsPastOneBatch walks more rows than the batch size, so
// the LIMIT/OFFSET loop is exercised rather than just its first pass.
func TestMigrationV2BackfillsPastOneBatch(t *testing.T) {
	subjects := map[int]string{}
	const n = backfillKeepBatch + 37
	for uid := 1; uid <= n; uid++ {
		if uid%2 == 0 {
			subjects[uid] = "Your receipt " + strconv.Itoa(uid)
		} else {
			subjects[uid] = "Deals of the week " + strconv.Itoa(uid)
		}
	}
	st, err := Open(v1Fixture(t, subjects))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var kept int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE keep = 'receipt'`).Scan(&kept); err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := n / 2; kept != want {
		t.Fatalf("backfilled %d receipts, want %d", kept, want)
	}
}

// TestSenderGroupsKeepCounters checks the two counters the review screen
// partitions on: bulk mail that matched, and every category seen.
func TestSenderGroupsKeepCounters(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "mailshear.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	acct, err := st.UpsertAccount(ctx, "personal", "localhost", "user")
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	msgs := []Message{
		{UID: 1, HasUnsub: true, Subject: "Deals", Keep: ""},
		{UID: 2, HasUnsub: true, Subject: "Your receipt", Keep: "receipt"},
		{UID: 3, HasUnsub: false, Subject: "Security alert", Keep: "security"},
		{UID: 4, HasUnsub: false, Subject: "", Keep: ""},
	}
	for i := range msgs {
		msgs[i].AccountID = acct.ID
		msgs[i].Folder = "INBOX"
		msgs[i].SenderKey = "addr:svc@pay.example"
		msgs[i].DomainKey = "pay.example"
		msgs[i].FromAddress = "svc@pay.example"
	}
	if err := st.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	groups, err := st.SenderGroups(ctx, acct.ID)
	if err != nil {
		t.Fatalf("SenderGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	g := groups[0]
	if g.Count != 2 || g.MixedCount != 2 {
		t.Fatalf("counts = %d bulk, %d mixed", g.Count, g.MixedCount)
	}
	if g.KeepCount != 1 {
		t.Fatalf("KeepCount = %d, want 1 (bulk mail only)", g.KeepCount)
	}
	if len(g.KeepCategories) != 2 {
		t.Fatalf("KeepCategories = %v, want both the bulk and the non-bulk category", g.KeepCategories)
	}
}
