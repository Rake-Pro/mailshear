package undo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/store"
)

const (
	testUser  = "user"
	testPass  = "pass"
	senderKey = "addr:news@example.com"
	runID     = "20260906-130000-bb22"
)

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}

func raw(id string) string {
	return "From: News <news@example.com>\r\n" +
		"Subject: subject " + id + "\r\n" +
		"Message-Id: <" + id + ">\r\n" +
		"List-Unsubscribe: <https://example.com/u>\r\n" +
		"\r\n" + "body " + id + "\r\n"
}

func startServer(t *testing.T, msgs []string) string {
	t.Helper()

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPass)
	for _, name := range []string{"INBOX", "Trash"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Logger:       nopLogger{},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
	})
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	addr := ln.Addr().String()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	for _, m := range msgs {
		cmd := c.Append("INBOX", int64(len(m)), nil)
		if _, err := io.WriteString(cmd, m); err != nil {
			t.Fatalf("append write: %v", err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatalf("append close: %v", err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return addr
}

func dialTest(t *testing.T, addr string) *imapx.Client {
	t.Helper()
	c, err := imapx.DialInsecureForTest(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func folderUIDs(t *testing.T, addr, mailbox string) []uint32 {
	t.Helper()
	c, err := imapx.DialInsecureForTest(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.SelectReadOnly(context.Background(), mailbox); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var uids []uint32
	if err := c.FetchHeaders(context.Background(), 1, 0, func(h imapx.Header) error {
		uids = append(uids, h.UID)
		return nil
	}); err != nil {
		t.Fatalf("fetch %s: %v", mailbox, err)
	}
	return uids
}

// applyRun deletes both fixture messages and returns the data directory.
func applyRun(t *testing.T, addr string, cl *imapx.Client, st *store.Store) (store.Account, string) {
	t.Helper()
	ctx := context.Background()

	acct, err := st.UpsertAccount(ctx, "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := st.InsertMessages(ctx, []store.Message{
		{AccountID: acct.ID, Folder: "INBOX", UID: 1, MessageID: "m1@example.com", SenderKey: senderKey,
			FromAddress: "news@example.com", DomainKey: "example.com", HasUnsub: true,
			Method: "oneclick", InternalDate: base, Size: 10},
		{AccountID: acct.ID, Folder: "INBOX", UID: 2, MessageID: "m2@example.com", SenderKey: senderKey,
			FromAddress: "news@example.com", DomainKey: "example.com", HasUnsub: true,
			Method: "oneclick", InternalDate: base.Add(time.Hour), Size: 10},
	}); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	info, err := cl.SelectReadOnly(ctx, "INBOX")
	if err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	if err := st.SetFolderCursor(ctx, acct.ID, "INBOX", info.UIDValidity, 2); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}

	dataDir := t.TempDir()
	p := &plan.Plan{
		RunID: runID, Account: "personal",
		Snapshot:  plan.Snapshot{MessageCount: 2},
		Decisions: []plan.Entry{{SenderKey: senderKey, Display: "News", DeleteMatched: true}},
	}
	sum, err := apply.Run(ctx, apply.Deps{
		Store: st, Client: cl, Account: acct, DataDir: dataDir,
		Cfg: config.Account{Name: "personal", Host: "localhost", Username: testUser},
	}, p, apply.Options{Yes: true})
	if err != nil {
		t.Fatalf("apply.Run: %v", err)
	}
	if sum.Moved != 2 {
		t.Fatalf("apply moved %d, want 2", sum.Moved)
	}
	return acct, dataDir
}

func TestUndoRoundTrip(t *testing.T) {
	addr := startServer(t, []string{raw("m1@example.com"), raw("m2@example.com")})
	cl := dialTest(t, addr)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	acct, dataDir := applyRun(t, addr, cl, st)
	ctx := context.Background()

	if got := folderUIDs(t, addr, "INBOX"); len(got) != 0 {
		t.Fatalf("INBOX = %v after apply, want empty", got)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 2 {
		t.Fatalf("Trash = %v after apply, want 2", got)
	}

	var out bytes.Buffer
	moved, missing, err := Run(ctx, st, cl, acct, dataDir, runID, true, nil, &out)
	if err != nil {
		t.Fatalf("undo.Run: %v", err)
	}
	if moved != 2 || missing != 0 {
		t.Fatalf("undo moved=%d missing=%d, want 2 and 0", moved, missing)
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 2 {
		t.Fatalf("INBOX = %v after undo, want 2 messages back", got)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v after undo, want empty", got)
	}

	lines, err := apply.ReadLines(apply.AuditPath(dataDir, runID))
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	undos := 0
	for _, l := range lines {
		if l.Action == "undo" {
			undos++
			if l.From != "Trash" || l.To != "INBOX" {
				t.Errorf("undo line from=%q to=%q", l.From, l.To)
			}
		}
	}
	if undos != 2 {
		t.Fatalf("audit log has %d undo lines, want 2", undos)
	}

	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Summary != "undone: restored 2, missing 0" {
		t.Fatalf("run summary = %q", run.Summary)
	}

	// A second undo finds nothing left in Trash and says so instead of moving
	// anything.
	moved, missing, err = Run(ctx, st, cl, acct, dataDir, runID, true, nil, &out)
	if err != nil {
		t.Fatalf("second undo.Run: %v", err)
	}
	if moved != 0 || missing != 2 {
		t.Fatalf("second undo moved=%d missing=%d, want 0 and 2", moved, missing)
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 2 {
		t.Fatalf("INBOX = %v after the second undo", got)
	}
}

func TestUndoConfirmFalse(t *testing.T) {
	addr := startServer(t, []string{raw("m1@example.com"), raw("m2@example.com")})
	cl := dialTest(t, addr)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	acct, dataDir := applyRun(t, addr, cl, st)

	var shown string
	_, _, err = Run(context.Background(), st, cl, acct, dataDir, runID, false,
		func(s string) (bool, error) { shown = s; return false, nil }, io.Discard)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("undo.Run err = %v, want ErrAborted", err)
	}
	if shown == "" {
		t.Fatal("no confirmation text shown")
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 2 {
		t.Fatalf("Trash = %v, want the messages left where they were", got)
	}
}

func TestUndoUnknownRun(t *testing.T) {
	addr := startServer(t, nil)
	cl := dialTest(t, addr)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	acct, err := st.UpsertAccount(context.Background(), "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if _, _, err := Run(context.Background(), st, cl, acct, dir, "nope", true, nil, io.Discard); err == nil {
		t.Fatal("undo of an unknown run: want error")
	}
}

// appendTo puts a raw message into mailbox over a fresh connection.
func appendTo(t *testing.T, addr, mailbox, msg string) {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	cmd := c.Append(mailbox, int64(len(msg)), nil)
	if _, err := io.WriteString(cmd, msg); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestUndoLeavesPreexistingTrashCopy: the user trashed their own copy of m1
// before the run. Undo restores only the one copy the run moved and leaves
// the other where the user put it.
func TestUndoLeavesPreexistingTrashCopy(t *testing.T) {
	addr := startServer(t, []string{raw("m1@example.com"), raw("m2@example.com")})
	cl := dialTest(t, addr)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	appendTo(t, addr, "Trash", raw("m1@example.com"))
	if got := folderUIDs(t, addr, "Trash"); len(got) != 1 {
		t.Fatalf("Trash = %v before the run, want the user's own copy", got)
	}

	acct, dataDir := applyRun(t, addr, cl, st)
	if got := folderUIDs(t, addr, "Trash"); len(got) != 3 {
		t.Fatalf("Trash = %v after apply, want 3 (1 pre-existing + 2 moved)", got)
	}

	var out bytes.Buffer
	moved, missing, err := Run(context.Background(), st, cl, acct, dataDir, runID, true, nil, &out)
	if err != nil {
		t.Fatalf("undo.Run: %v", err)
	}
	if moved != 2 {
		t.Fatalf("undo restored %d, want the 2 the run moved", moved)
	}
	if missing != 0 {
		t.Fatalf("missing = %d, want 0", missing)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 1 {
		t.Fatalf("Trash = %v after undo, want the user's own copy left behind", got)
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 2 {
		t.Fatalf("INBOX = %v after undo, want the 2 restored messages", got)
	}
	if !strings.Contains(out.String(), "ambiguous, left in Trash") {
		t.Fatalf("no ambiguity reported in %q", out.String())
	}
}

// TestUndoRefusesToRestoreIntoTrash: an audit log claiming a message came
// from Trash must not send it back there.
func TestUndoRefusesToRestoreIntoProtectedFolder(t *testing.T) {
	addr := startServer(t, []string{raw("m1@example.com")})
	cl := dialTest(t, addr)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	acct, err := st.UpsertAccount(context.Background(), "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	dataDir := t.TempDir()
	au, err := apply.OpenAudit(dataDir, runID, "personal")
	if err != nil {
		t.Fatalf("OpenAudit: %v", err)
	}
	au.Write(apply.Line{
		Action: "move", MessageID: "m1@example.com",
		From: "Drafts", To: "Trash", Folder: "Drafts",
	})
	if err := au.Close(); err != nil {
		t.Fatalf("close audit: %v", err)
	}

	var out bytes.Buffer
	moved, missing, err := Run(context.Background(), st, cl, acct, dataDir, runID, true, nil, &out)
	if err != nil {
		t.Fatalf("undo.Run: %v", err)
	}
	if moved != 0 || missing != 1 {
		t.Fatalf("moved=%d missing=%d, want 0 and 1", moved, missing)
	}
	if !strings.Contains(out.String(), "never moves mail into") {
		t.Fatalf("no refusal reported in %q", out.String())
	}
}

func TestUndoRejectsMalformedRunID(t *testing.T) {
	addr := startServer(t, nil)
	cl := dialTest(t, addr)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	acct, err := st.UpsertAccount(context.Background(), "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	_, _, err = Run(context.Background(), st, cl, acct, dir, "../../outside", true, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is not a run id") {
		t.Fatalf("err = %v, want a run-id rejection", err)
	}
}
