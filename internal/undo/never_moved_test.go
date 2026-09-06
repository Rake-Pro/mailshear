package undo

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// A move is recorded before it is attempted, so a run that stopped between
// the write-ahead line and the MOVE leaves an intent for a message that never
// went anywhere. Undo must not pull a copy back out of Trash for it: the one
// in the source folder is the only one there is.
func TestUndoLeavesAMessageThatNeverMoved(t *testing.T) {
	addr := startServer(t, []string{raw("m1@example.com"), raw("m2@example.com")})
	cl := dialTest(t, addr)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	acct, err := st.UpsertAccount(ctx, "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	// m2 really moved; m1 is still in INBOX.
	if _, err := cl.Select(ctx, "INBOX"); err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	if err := cl.MoveUIDs(ctx, []uint32{2}, "Trash"); err != nil {
		t.Fatalf("move: %v", err)
	}

	// An audit log with an intent for both and a confirmation for m2 only.
	dataDir := t.TempDir()
	au, err := apply.OpenAudit(dataDir, runID, "personal")
	if err != nil {
		t.Fatalf("OpenAudit: %v", err)
	}
	au.Write(apply.Line{Action: "move", Status: "pending", Folder: "INBOX", UID: 1,
		MessageID: "m1@example.com", From: "INBOX", To: "Trash"})
	au.Write(apply.Line{Action: "move", Status: "pending", Folder: "INBOX", UID: 2,
		MessageID: "m2@example.com", From: "INBOX", To: "Trash"})
	au.Write(apply.Line{Action: "move", Status: "done", Folder: "INBOX",
		UIDs: []uint32{2}, From: "INBOX", To: "Trash"})
	if err := au.Close(); err != nil {
		t.Fatalf("close audit: %v", err)
	}

	var out bytes.Buffer
	moved, missing, err := Run(ctx, st, cl, acct, dataDir, runID, true, nil, &out)
	if err != nil {
		t.Fatalf("undo.Run: %v", err)
	}
	if moved != 1 {
		t.Fatalf("undo moved %d, want only the message that really moved", moved)
	}
	if missing != 0 {
		t.Fatalf("undo reported %d missing, want 0: a message that never moved is not missing", missing)
	}
	if !strings.Contains(out.String(), "never moved, left in place") {
		t.Fatalf("output does not report the message that never moved:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "m1@example.com") {
		t.Fatalf("output does not name the message:\n%s", out.String())
	}

	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v after undo, want empty", got)
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 2 {
		t.Fatalf("INBOX = %v after undo, want the one that stayed plus the one restored", got)
	}

	lines, err := apply.ReadLines(apply.AuditPath(dataDir, runID))
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	found := false
	for _, l := range lines {
		if l.Action == "undo" && l.MessageID == "m1@example.com" && l.Error == "never moved, left in place" {
			found = true
		}
	}
	if !found {
		t.Fatal("the audit log does not record that the message was left in place")
	}
}

// An unconfirmed intent for a message that is no longer in the source folder
// did move: it is restored as usual.
func TestUndoRestoresAnUnconfirmedMoveThatDidHappen(t *testing.T) {
	addr := startServer(t, []string{raw("m1@example.com")})
	cl := dialTest(t, addr)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	acct, err := st.UpsertAccount(ctx, "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if _, err := cl.Select(ctx, "INBOX"); err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	if err := cl.MoveUIDs(ctx, []uint32{1}, "Trash"); err != nil {
		t.Fatalf("move: %v", err)
	}

	dataDir := t.TempDir()
	au, err := apply.OpenAudit(dataDir, runID, "personal")
	if err != nil {
		t.Fatalf("OpenAudit: %v", err)
	}
	// The MOVE went out and the process died before the confirmation line.
	au.Write(apply.Line{Action: "move", Status: "pending", Folder: "INBOX", UID: 1,
		MessageID: "m1@example.com", From: "INBOX", To: "Trash"})
	if err := au.Close(); err != nil {
		t.Fatalf("close audit: %v", err)
	}

	var out bytes.Buffer
	moved, _, err := Run(ctx, st, cl, acct, dataDir, runID, true, nil, &out)
	if err != nil {
		t.Fatalf("undo.Run: %v", err)
	}
	if moved != 1 {
		t.Fatalf("undo moved %d, want 1:\n%s", moved, out.String())
	}
	if strings.Contains(out.String(), "never moved") {
		t.Fatalf("a message that did move was reported as never moved:\n%s", out.String())
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 1 {
		t.Fatalf("INBOX = %v after undo, want the restored message", got)
	}
}
