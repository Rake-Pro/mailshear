package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "mailshear.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenTwiceIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailshear.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	var rows int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if rows != len(migrations) {
		t.Fatalf("expected %d migration rows after reopen, got %d", len(migrations), rows)
	}
}

func TestFileMode(t *testing.T) {
	_, path := openTest(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected mode 0600, got %o", got)
	}
}

func TestAccountUpsertAndGet(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "personal", "imap.gmail.com", "you@gmail.com")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if a.ID == 0 {
		t.Fatalf("expected nonzero id")
	}

	a2, err := s.UpsertAccount(ctx, "personal", "imap.gmail.com", "you2@gmail.com")
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if a2.ID != a.ID {
		t.Fatalf("expected same id on upsert, got %d vs %d", a2.ID, a.ID)
	}
	if a2.Username != "you2@gmail.com" {
		t.Fatalf("expected updated username, got %s", a2.Username)
	}

	got, err := s.GetAccount(ctx, "personal")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != a2 {
		t.Fatalf("get mismatch: %+v vs %+v", got, a2)
	}

	_, err = s.GetAccount(ctx, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
}

func TestFolderCursorAndReset(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	_, err = s.GetFolder(ctx, a.ID, "INBOX")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unseen folder, got %v", err)
	}

	if err := s.SetFolderCursor(ctx, a.ID, "INBOX", 100, 50); err != nil {
		t.Fatalf("set cursor: %v", err)
	}
	f, err := s.GetFolder(ctx, a.ID, "INBOX")
	if err != nil {
		t.Fatalf("get folder: %v", err)
	}
	if f.UIDValidity != 100 || f.LastUID != 50 {
		t.Fatalf("unexpected folder state: %+v", f)
	}

	if err := s.InsertMessages(ctx, []Message{
		{AccountID: a.ID, Folder: "INBOX", UID: 1, SenderKey: "addr:x@example.com", InternalDate: time.Now()},
	}); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	if err := s.ResetFolder(ctx, a.ID, "INBOX", 200); err != nil {
		t.Fatalf("reset folder: %v", err)
	}
	f, err = s.GetFolder(ctx, a.ID, "INBOX")
	if err != nil {
		t.Fatalf("get folder after reset: %v", err)
	}
	if f.UIDValidity != 200 || f.LastUID != 0 {
		t.Fatalf("unexpected folder state after reset: %+v", f)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE account_id = ? AND folder = ?`, a.ID, "INBOX").Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected messages dropped after reset, got %d", count)
	}
}

func TestInsertMessagesAndSenderGroups(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	senderKey := "list:foo.example.com"

	msgs := []Message{
		// oldest has_unsub message: mailto method
		{
			AccountID: a.ID, Folder: "INBOX", UID: 1, SenderKey: senderKey, DomainKey: "example.com",
			FromDisplay: "Foo Old", FromAddress: "old@example.com", ListID: "foo.example.com",
			HasUnsub: true, Method: "mailto", UnsubURIs: []string{"mailto:unsub@example.com"},
			Flags: []string{"\\Seen"}, InternalDate: base, Size: 100, Subject: "Oldest subject",
			GmLabels: []string{"INBOX"},
		},
		// middle has_unsub message: http method, flagged
		{
			AccountID: a.ID, Folder: "INBOX", UID: 2, SenderKey: senderKey, DomainKey: "example.com",
			FromDisplay: "Foo Mid", FromAddress: "mid@example.com", ListID: "foo.example.com",
			HasUnsub: true, Method: "http", UnsubURIs: []string{"https://example.com/unsub"},
			Flags: []string{"\\Flagged"}, InternalDate: base.Add(time.Hour), Size: 200, Subject: "Middle subject",
			GmLabels: []string{"INBOX", "Promotions"},
		},
		// newest has_unsub message: oneclick method, replied
		{
			AccountID: a.ID, Folder: "Archive", UID: 3, SenderKey: senderKey, DomainKey: "example.com",
			FromDisplay: "Foo New", FromAddress: "new@example.com", ListID: "foo.example.com",
			HasUnsub: true, Method: "oneclick", UnsubURIs: []string{"https://example.com/oneclick"}, OneClick: true,
			Flags: []string{"\\Answered"}, InternalDate: base.Add(2 * time.Hour), Size: 300, Subject: "Newest subject",
			GmLabels: []string{"Archive"},
		},
		// mixed message, no unsub header, same sender
		{
			AccountID: a.ID, Folder: "INBOX", UID: 4, SenderKey: senderKey, DomainKey: "example.com",
			FromAddress: "new@example.com", HasUnsub: false,
			Flags: []string{}, InternalDate: base.Add(3 * time.Hour), Size: 50,
		},
	}

	if err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	groups, err := s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 sender group, got %d", len(groups))
	}
	g := groups[0]

	if g.Count != 3 {
		t.Fatalf("expected Count 3, got %d", g.Count)
	}
	if g.MixedCount != 1 {
		t.Fatalf("expected MixedCount 1, got %d", g.MixedCount)
	}
	if g.FlaggedCount != 1 {
		t.Fatalf("expected FlaggedCount 1, got %d", g.FlaggedCount)
	}
	if g.RepliedCount != 1 {
		t.Fatalf("expected RepliedCount 1, got %d", g.RepliedCount)
	}
	if g.Method != "oneclick" {
		t.Fatalf("expected Method oneclick (precedence), got %s", g.Method)
	}
	if len(g.LatestURIs) != 1 || g.LatestURIs[0] != "https://example.com/oneclick" {
		t.Fatalf("expected LatestURIs from newest message, got %v", g.LatestURIs)
	}
	if !g.LatestOneClick {
		t.Fatalf("expected LatestOneClick true")
	}
	if g.Display != "Foo New" {
		t.Fatalf("expected Display from newest message, got %s", g.Display)
	}
	if g.Address != "new@example.com" {
		t.Fatalf("expected Address from newest message, got %s", g.Address)
	}
	if len(g.RecentSubjects) != 3 {
		t.Fatalf("expected 3 recent subjects, got %d: %v", len(g.RecentSubjects), g.RecentSubjects)
	}
	if g.RecentSubjects[0] != "Newest subject" {
		t.Fatalf("expected newest subject first, got %v", g.RecentSubjects)
	}
	wantFolders := map[string]bool{"INBOX": true, "Archive": true}
	if len(g.Folders) != len(wantFolders) {
		t.Fatalf("expected folders %v, got %v", wantFolders, g.Folders)
	}
	for _, f := range g.Folders {
		if !wantFolders[f] {
			t.Fatalf("unexpected folder %s in %v", f, g.Folders)
		}
	}
	if g.TotalSize != 600 {
		t.Fatalf("expected TotalSize 600 (has_unsub only), got %d", g.TotalSize)
	}
	if g.Protected {
		t.Fatalf("expected not protected by default")
	}

	// SetProtected and re-check
	if err := s.SetProtected(ctx, a.ID, senderKey, true); err != nil {
		t.Fatalf("set protected: %v", err)
	}
	groups, err = s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups after protect: %v", err)
	}
	if !groups[0].Protected {
		t.Fatalf("expected protected true after SetProtected")
	}

	// Set folder cursors as a real scan would, so purge's cursor reset has
	// something to reset.
	if err := s.SetFolderCursor(ctx, a.ID, "INBOX", 100, 4); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}
	if err := s.SetFolderCursor(ctx, a.ID, "Archive", 200, 3); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}

	// PurgeMessages keeps senders
	if err := s.PurgeMessages(ctx, a.ID); err != nil {
		t.Fatalf("purge messages: %v", err)
	}
	var msgCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE account_id = ?`, a.ID).Scan(&msgCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 0 {
		t.Fatalf("expected 0 messages after purge, got %d", msgCount)
	}
	var senderCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM senders WHERE account_id = ?`, a.ID).Scan(&senderCount); err != nil {
		t.Fatalf("count senders: %v", err)
	}
	if senderCount != 1 {
		t.Fatalf("expected senders to survive purge, got %d", senderCount)
	}

	// Folder cursors reset to 0 (forcing a full rescan) but the folder rows
	// and their UIDVALIDITY survive, so a UIDVALIDITY change is not implied.
	for _, tc := range []struct {
		folder      string
		uidValidity uint32
	}{
		{"INBOX", 100},
		{"Archive", 200},
	} {
		f, err := s.GetFolder(ctx, a.ID, tc.folder)
		if err != nil {
			t.Fatalf("get folder %s after purge: %v", tc.folder, err)
		}
		if f.LastUID != 0 {
			t.Fatalf("expected folder %s cursor reset to 0 after purge, got %d", tc.folder, f.LastUID)
		}
		if f.UIDValidity != tc.uidValidity {
			t.Fatalf("expected folder %s UIDVALIDITY %d preserved after purge, got %d", tc.folder, tc.uidValidity, f.UIDValidity)
		}
	}

	groupsAfterPurge, err := s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups after purge: %v", err)
	}
	if len(groupsAfterPurge) != 0 {
		t.Fatalf("expected no groups after purge (no has_unsub messages left), got %d", len(groupsAfterPurge))
	}
}

func TestTouchSenders(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := early.Add(24 * time.Hour)

	err = s.TouchSenders(ctx, a.ID, map[string]Sender{
		"addr:x@example.com": {
			Display: "X", DomainKey: "example.com", ListID: "",
			FirstSeen: later, LastSeen: later,
		},
	})
	if err != nil {
		t.Fatalf("touch senders: %v", err)
	}

	// mark protected, then touch again with an earlier/later window; protected must survive.
	if err := s.SetProtected(ctx, a.ID, "addr:x@example.com", true); err != nil {
		t.Fatalf("set protected: %v", err)
	}

	err = s.TouchSenders(ctx, a.ID, map[string]Sender{
		"addr:x@example.com": {
			Display: "X Updated", DomainKey: "example.com", ListID: "",
			FirstSeen: early, LastSeen: later.Add(time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("touch senders again: %v", err)
	}

	var display, firstSeen, lastSeen string
	var protected int
	err = s.db.QueryRow(`SELECT display, first_seen, last_seen, protected FROM senders WHERE account_id = ? AND sender_key = ?`,
		a.ID, "addr:x@example.com").Scan(&display, &firstSeen, &lastSeen, &protected)
	if err != nil {
		t.Fatalf("query sender: %v", err)
	}
	if display != "X Updated" {
		t.Fatalf("expected updated display, got %s", display)
	}
	if firstSeen != timeToText(early) {
		t.Fatalf("expected first_seen to take min, got %s", firstSeen)
	}
	if lastSeen != timeToText(later.Add(time.Hour)) {
		t.Fatalf("expected last_seen to take max, got %s", lastSeen)
	}
	if protected != 1 {
		t.Fatalf("expected protected to survive TouchSenders, got %d", protected)
	}
}

func TestPurgeLeavesNoReadableTraces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mailshear.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	const subjectMarker = "MARKERSUBJECTZZZ"
	const addressMarker = "markeraddresszzz@example.com"
	msgs := make([]Message, 0, 200)
	for i := 1; i <= 200; i++ {
		msgs = append(msgs, Message{
			AccountID: a.ID, Folder: "INBOX", UID: uint32(i),
			SenderKey: "addr:" + addressMarker, DomainKey: "example.com",
			FromDisplay: "Marker", FromAddress: addressMarker,
			HasUnsub: true, Method: "http", UnsubURIs: []string{"https://example.com/u"},
			InternalDate: time.Now(), Size: 10, Subject: subjectMarker,
		})
	}
	if err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	if err := s.PurgeMessages(ctx, a.ID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		name := path + suffix
		b, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, marker := range []string{subjectMarker, addressMarker} {
			if bytes.Contains(b, []byte(marker)) {
				t.Errorf("%s still contains %q after purge", filepath.Base(name), marker)
			}
		}
	}
}

func TestSenderGroupsIgnoresUndatedMessages(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	dated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	senderKey := "addr:x@example.com"
	msgs := []Message{
		{
			AccountID: a.ID, Folder: "INBOX", UID: 1, SenderKey: senderKey,
			HasUnsub: true, Method: "http", InternalDate: dated,
		},
		{
			AccountID: a.ID, Folder: "INBOX", UID: 2, SenderKey: senderKey,
			HasUnsub: true, Method: "http",
		},
	}
	if err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	groups, err := s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if !groups[0].FirstSeen.Equal(dated) {
		t.Fatalf("FirstSeen = %v, want %v (undated message must not win MIN)", groups[0].FirstSeen, dated)
	}
	if !groups[0].LastSeen.Equal(dated) {
		t.Fatalf("LastSeen = %v, want %v", groups[0].LastSeen, dated)
	}
}

func TestTouchSendersKeepsKnownTimes(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	key := "addr:x@example.com"
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// An undated sighting first, then a dated one.
	if err := s.TouchSenders(ctx, a.ID, map[string]Sender{key: {Display: "X"}}); err != nil {
		t.Fatalf("touch senders (undated): %v", err)
	}
	if err := s.TouchSenders(ctx, a.ID, map[string]Sender{
		key: {Display: "X", FirstSeen: when, LastSeen: when},
	}); err != nil {
		t.Fatalf("touch senders (dated): %v", err)
	}

	var firstSeen, lastSeen string
	err = s.db.QueryRow(`SELECT COALESCE(first_seen, ''), COALESCE(last_seen, '') FROM senders WHERE account_id = ? AND sender_key = ?`,
		a.ID, key).Scan(&firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("query sender: %v", err)
	}
	if firstSeen != timeToText(when) || lastSeen != timeToText(when) {
		t.Fatalf("first_seen=%q last_seen=%q, want %v", firstSeen, lastSeen, timeToText(when))
	}

	// An undated sighting afterwards must not blank what is already known.
	if err := s.TouchSenders(ctx, a.ID, map[string]Sender{key: {Display: "X"}}); err != nil {
		t.Fatalf("touch senders (undated again): %v", err)
	}
	err = s.db.QueryRow(`SELECT COALESCE(first_seen, ''), COALESCE(last_seen, '') FROM senders WHERE account_id = ? AND sender_key = ?`,
		a.ID, key).Scan(&firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("query sender: %v", err)
	}
	if firstSeen != timeToText(when) || lastSeen != timeToText(when) {
		t.Fatalf("undated touch blanked times: first_seen=%q last_seen=%q", firstSeen, lastSeen)
	}
}

func TestMethodRankNoneBeatsUnknown(t *testing.T) {
	if methodRank("none") <= methodRank("") {
		t.Fatal("methodRank(none) must outrank the empty method")
	}
	order := []string{"", "none", "mailto", "http", "oneclick"}
	for i := 1; i < len(order); i++ {
		if methodRank(order[i]) <= methodRank(order[i-1]) {
			t.Fatalf("methodRank(%q) must outrank methodRank(%q)", order[i], order[i-1])
		}
	}
}

func TestSenderGroupMethodNoneWhenAllNone(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := s.InsertMessages(ctx, []Message{{
		AccountID: a.ID, Folder: "INBOX", UID: 1, SenderKey: "addr:x@example.com",
		HasUnsub: true, Method: "none", InternalDate: time.Now(),
	}}); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	groups, err := s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Method != "none" {
		t.Fatalf("Method = %q, want none", groups[0].Method)
	}
}
