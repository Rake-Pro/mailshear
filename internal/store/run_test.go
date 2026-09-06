package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunLifecycle(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	if runs, err := s.ListRuns(ctx, a.ID); err != nil || len(runs) != 0 {
		t.Fatalf("ListRuns on an empty db = %v (%v)", runs, err)
	}
	if _, err := s.GetRun(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun missing = %v, want ErrNotFound", err)
	}
	if err := s.FinishRun(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FinishRun missing = %v, want ErrNotFound", err)
	}
	if err := s.InsertRun(ctx, Run{AccountID: a.ID}); err == nil {
		t.Fatal("InsertRun with an empty id: want error")
	}

	started := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if err := s.InsertRun(ctx, Run{
		ID: "run-a", AccountID: a.ID, StartedAt: started, PlanPath: "/plans/run-a.yaml",
	}); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if err := s.InsertRun(ctx, Run{
		ID: "run-b", AccountID: a.ID, StartedAt: started.Add(time.Hour), PlanPath: "/plans/run-b.yaml",
	}); err != nil {
		t.Fatalf("InsertRun second: %v", err)
	}

	got, err := s.GetRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.PlanPath != "/plans/run-a.yaml" || !got.StartedAt.Equal(started) {
		t.Fatalf("GetRun = %+v", got)
	}
	if !got.FinishedAt.IsZero() {
		t.Fatalf("FinishedAt = %v, want zero before FinishRun", got.FinishedAt)
	}

	if err := s.FinishRun(ctx, "run-a", "moved 3, unsubscribed 1"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	got, err = s.GetRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("GetRun after finish: %v", err)
	}
	if got.Summary != "moved 3, unsubscribed 1" || got.FinishedAt.IsZero() {
		t.Fatalf("GetRun after finish = %+v", got)
	}

	runs, err := s.ListRuns(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run-b" {
		t.Fatalf("ListRuns = %+v, want newest first", runs)
	}
}

func TestMessagesForSenderAndDeleteRows(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	key := "addr:news@example.com"

	msgs := []Message{
		{
			AccountID: a.ID, Folder: "INBOX", UID: 1, MessageID: "a@example.com", SenderKey: key,
			HasUnsub: true, Method: "http", UnsubURIs: []string{"https://example.com/old"},
			Flags: []string{"\\Seen"}, InternalDate: base, Size: 10,
		},
		{
			AccountID: a.ID, Folder: "Archive", UID: 2, MessageID: "b@example.com", SenderKey: key,
			HasUnsub: true, Method: "oneclick", UnsubURIs: []string{"https://example.com/new"},
			OneClick: true, Flags: []string{"\\Flagged", "\\Seen"}, InternalDate: base.Add(2 * time.Hour), Size: 20,
		},
		{
			AccountID: a.ID, Folder: "INBOX", UID: 3, MessageID: "c@example.com", SenderKey: key,
			HasUnsub: false, InternalDate: base.Add(time.Hour), Size: 30,
		},
		{
			AccountID: a.ID, Folder: "INBOX", UID: 9, SenderKey: "addr:other@example.net",
			HasUnsub: true, InternalDate: base, Size: 40,
		},
	}
	if err := s.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	matched, err := s.MessagesForSender(ctx, a.ID, key, true)
	if err != nil {
		t.Fatalf("MessagesForSender matched: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("matched = %d messages, want 2", len(matched))
	}
	if matched[0].UID != 2 {
		t.Fatalf("matched[0].UID = %d, want the newest (2)", matched[0].UID)
	}
	if len(matched[0].UnsubURIs) != 1 || matched[0].UnsubURIs[0] != "https://example.com/new" {
		t.Fatalf("matched[0].UnsubURIs = %v", matched[0].UnsubURIs)
	}
	if !matched[0].OneClick || matched[0].Method != "oneclick" {
		t.Fatalf("matched[0] method fields = %q %v", matched[0].Method, matched[0].OneClick)
	}
	if len(matched[0].Flags) != 2 || matched[0].Flags[0] != "\\Flagged" {
		t.Fatalf("matched[0].Flags = %v", matched[0].Flags)
	}
	if matched[0].MessageID != "b@example.com" {
		t.Fatalf("matched[0].MessageID = %q", matched[0].MessageID)
	}
	if !matched[0].InternalDate.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("matched[0].InternalDate = %v", matched[0].InternalDate)
	}

	all, err := s.MessagesForSender(ctx, a.ID, key, false)
	if err != nil {
		t.Fatalf("MessagesForSender all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all = %d messages, want 3", len(all))
	}

	if err := s.DeleteMessageRows(ctx, a.ID, "INBOX", nil); err != nil {
		t.Fatalf("DeleteMessageRows(nil): %v", err)
	}
	if err := s.DeleteMessageRows(ctx, a.ID, "INBOX", []uint32{1, 3}); err != nil {
		t.Fatalf("DeleteMessageRows: %v", err)
	}
	left, err := s.MessagesForSender(ctx, a.ID, key, false)
	if err != nil {
		t.Fatalf("MessagesForSender after delete: %v", err)
	}
	if len(left) != 1 || left[0].UID != 2 {
		t.Fatalf("left = %+v, want only the Archive row", left)
	}
	// Another sender's rows in the same folder are untouched.
	other, err := s.MessagesForSender(ctx, a.ID, "addr:other@example.net", false)
	if err != nil {
		t.Fatalf("MessagesForSender other: %v", err)
	}
	if len(other) != 1 {
		t.Fatalf("other sender lost rows: %+v", other)
	}
}

func TestSetUnsubResult(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()

	a, err := s.UpsertAccount(ctx, "acct", "host", "user")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	key := "addr:news@example.com"
	if err := s.SetProtected(ctx, a.ID, key, true); err != nil {
		t.Fatalf("set protected: %v", err)
	}
	if err := s.SetUnsubResult(ctx, a.ID, key, "ok", "run-1"); err != nil {
		t.Fatalf("SetUnsubResult: %v", err)
	}

	var status, runID, at string
	var protected int
	err = s.db.QueryRow(
		`SELECT unsub_status, unsub_run_id, COALESCE(unsub_at, ''), protected FROM senders WHERE account_id = ? AND sender_key = ?`,
		a.ID, key).Scan(&status, &runID, &at, &protected)
	if err != nil {
		t.Fatalf("query sender: %v", err)
	}
	if status != "ok" || runID != "run-1" || at == "" {
		t.Fatalf("status=%q run=%q at=%q", status, runID, at)
	}
	if protected != 1 {
		t.Fatal("SetUnsubResult cleared the protected flag")
	}

	// A sender with no row yet gets one.
	if err := s.SetUnsubResult(ctx, a.ID, "addr:new@example.org", "manual", "run-2"); err != nil {
		t.Fatalf("SetUnsubResult on a new sender: %v", err)
	}
	if err := s.db.QueryRow(
		`SELECT unsub_status FROM senders WHERE account_id = ? AND sender_key = ?`,
		a.ID, "addr:new@example.org").Scan(&status); err != nil {
		t.Fatalf("query new sender: %v", err)
	}
	if status != "manual" {
		t.Fatalf("status = %q, want manual", status)
	}
}
