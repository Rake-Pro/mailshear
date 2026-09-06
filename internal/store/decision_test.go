package store

import (
	"context"
	"testing"
	"time"
)

func seedDecisionMessages(t *testing.T, s *Store, accountID int64, base time.Time) {
	t.Helper()
	msgs := []Message{
		{
			AccountID: accountID, Folder: "INBOX", UID: 7, SenderKey: "addr:news@example.com",
			DomainKey: "example.com", FromDisplay: "News", FromAddress: "news@example.com",
			HasUnsub: true, Method: "oneclick", UnsubURIs: []string{"https://example.com/u"},
			OneClick: true, InternalDate: base, Size: 100, Subject: "Issue 1",
		},
		{
			AccountID: accountID, Folder: "INBOX", UID: 11, SenderKey: "addr:news@example.com",
			DomainKey: "example.com", FromDisplay: "News", FromAddress: "news@example.com",
			HasUnsub: true, Method: "oneclick", UnsubURIs: []string{"https://example.com/u2"},
			OneClick: true, InternalDate: base.Add(time.Hour), Size: 120, Subject: "Issue 2",
		},
		{
			AccountID: accountID, Folder: "INBOX", UID: 3, SenderKey: "addr:deals@example.net",
			DomainKey: "example.net", FromDisplay: "Deals", FromAddress: "deals@example.net",
			HasUnsub: true, Method: "http", UnsubURIs: []string{"https://example.net/u"},
			InternalDate: base, Size: 90, Subject: "Deal",
		},
	}
	if err := s.InsertMessages(context.Background(), msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
}

func TestRecordDecisions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedDecisionMessages(t, s, a.ID, base)

	decs := []Decision{
		{SenderKey: "addr:news@example.com", Unsubscribe: true, DeleteMatched: true},
		{SenderKey: "addr:deals@example.net", DeleteAll: true},
		{SenderKey: "addr:ghost@example.org"},
	}
	if err := s.RecordDecisions(ctx, a.ID, "20260906-153012-ab12", decs); err != nil {
		t.Fatalf("record decisions: %v", err)
	}

	type row struct {
		decision   string
		runID      string
		lastUID    int64
		decidedSet bool
	}
	got := map[string]row{}
	rows, err := s.db.QueryContext(ctx,
		`SELECT sender_key, decision, unsub_run_id, last_uid_at_decision, decided_at IS NOT NULL
		 FROM senders WHERE account_id = ?`, a.ID)
	if err != nil {
		t.Fatalf("query senders: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var r row
		if err := rows.Scan(&key, &r.decision, &r.runID, &r.lastUID, &r.decidedSet); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[key] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 sender rows (upsert for the missing one), got %d", len(got))
	}
	if r := got["addr:news@example.com"]; r.decision != "unsubscribe,delete_matched" || r.lastUID != 11 {
		t.Fatalf("news: got decision %q lastUID %d", r.decision, r.lastUID)
	}
	if r := got["addr:deals@example.net"]; r.decision != "delete_all" || r.lastUID != 3 {
		t.Fatalf("deals: got decision %q lastUID %d", r.decision, r.lastUID)
	}
	if r := got["addr:ghost@example.org"]; r.decision != "" || r.lastUID != 0 {
		t.Fatalf("ghost: got decision %q lastUID %d", r.decision, r.lastUID)
	}
	for key, r := range got {
		if !r.decidedSet {
			t.Fatalf("%s: decided_at not set", key)
		}
		if r.runID != "20260906-153012-ab12" {
			t.Fatalf("%s: got run id %q", key, r.runID)
		}
	}
}

func TestRecordDecisionsPreservesProtected(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	seedDecisionMessages(t, s, a.ID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	if err := s.SetProtected(ctx, a.ID, "addr:deals@example.net", true); err != nil {
		t.Fatalf("set protected: %v", err)
	}
	if err := s.RecordDecisions(ctx, a.ID, "run1",
		[]Decision{{SenderKey: "addr:deals@example.net", Unsubscribe: true}}); err != nil {
		t.Fatalf("record decisions: %v", err)
	}

	keys, err := s.ListProtected(ctx, a.ID)
	if err != nil {
		t.Fatalf("list protected: %v", err)
	}
	if len(keys) != 1 || keys[0] != "addr:deals@example.net" {
		t.Fatalf("expected the protected flag to survive, got %v", keys)
	}
}

func TestListProtected(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if got, err := s.ListProtected(ctx, a.ID); err != nil || len(got) != 0 {
		t.Fatalf("expected no protected senders, got %v (%v)", got, err)
	}

	for _, key := range []string{"addr:b@example.com", "addr:a@example.com"} {
		if err := s.SetProtected(ctx, a.ID, key, true); err != nil {
			t.Fatalf("set protected %s: %v", key, err)
		}
	}
	if err := s.SetProtected(ctx, a.ID, "addr:b@example.com", false); err != nil {
		t.Fatalf("unprotect: %v", err)
	}

	got, err := s.ListProtected(ctx, a.ID)
	if err != nil {
		t.Fatalf("list protected: %v", err)
	}
	if len(got) != 1 || got[0] != "addr:a@example.com" {
		t.Fatalf("expected only a@, got %v", got)
	}
}

func TestSenderGroupDecisionFields(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	base := time.Now().UTC().Add(-48 * time.Hour)
	seedDecisionMessages(t, s, a.ID, base)

	if err := s.RecordDecisions(ctx, a.ID, "run1", []Decision{
		{SenderKey: "addr:news@example.com", Unsubscribe: true},
		{SenderKey: "addr:deals@example.net", DeleteMatched: true},
	}); err != nil {
		t.Fatalf("record decisions: %v", err)
	}
	// StillSending only applies once an unsubscribe attempt actually
	// recorded ok or probable; delete-only senders never get one.
	if err := s.SetUnsubResult(ctx, a.ID, "addr:news@example.com", "ok", "run1"); err != nil {
		t.Fatalf("set unsub result: %v", err)
	}

	groups, err := s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups: %v", err)
	}
	byKey := map[string]SenderGroup{}
	for _, g := range groups {
		byKey[g.SenderKey] = g
	}

	news := byKey["addr:news@example.com"]
	if news.Decision != "unsubscribe" {
		t.Fatalf("news decision: %q", news.Decision)
	}
	if news.DecidedAt.IsZero() {
		t.Fatalf("news DecidedAt not populated")
	}
	if news.LastUIDAtDecision != 11 {
		t.Fatalf("news LastUIDAtDecision: %d", news.LastUIDAtDecision)
	}
	// Decided now, last message two days ago: not still sending.
	if news.StillSending {
		t.Fatalf("news should not be still sending")
	}

	deals := byKey["addr:deals@example.net"]
	if deals.StillSending {
		t.Fatalf("delete-only decisions are never still sending")
	}

	// Newer mail than the decision flips the flag.
	if err := s.InsertMessages(ctx, []Message{{
		AccountID: a.ID, Folder: "INBOX", UID: 40, SenderKey: "addr:news@example.com",
		DomainKey: "example.com", FromAddress: "news@example.com", HasUnsub: true,
		Method: "oneclick", InternalDate: time.Now().UTC().Add(time.Hour), Size: 10,
	}}); err != nil {
		t.Fatalf("insert later message: %v", err)
	}
	groups, err = s.SenderGroups(ctx, a.ID)
	if err != nil {
		t.Fatalf("sender groups: %v", err)
	}
	for _, g := range groups {
		if g.SenderKey == "addr:news@example.com" {
			if !g.StillSending {
				t.Fatalf("expected news to be still sending")
			}
			if g.StillSendingCount != 1 {
				t.Fatalf("expected StillSendingCount 1 (only the post-unsub message), got %d", g.StillSendingCount)
			}
		}
	}
}
