package apply

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/keep"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

const (
	newsKey      = "addr:news@example.com"
	protectedKey = "addr:alerts@bank.example"
)

func msg(from, id string, unsubHeader bool) string {
	s := "From: " + from + "\r\n" +
		"Subject: subject " + id + "\r\n" +
		"Message-Id: <" + id + ">\r\n"
	if unsubHeader {
		s += "List-Unsubscribe: <https://example.com/u/" + id + ">\r\n" +
			"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"
	}
	return s + "\r\n" + "body " + id + "\r\n"
}

// fixtureMessages is the mailbox every delete test runs against. UIDs are the
// 1-based index into this slice.
func fixtureMessages() []testMsg {
	return []testMsg{
		{raw: msg("News <news@example.com>", "m1@example.com", true)},
		{raw: msg("News <news@example.com>", "m2@example.com", true)},
		{raw: msg("News <news@example.com>", "m3@example.com", false)},
		{raw: msg("News <news@example.com>", "m4@example.com", true), flags: []imap.Flag{imap.FlagFlagged}},
		{raw: msg("News <news@example.com>", "m5@example.com", true), flags: []imap.Flag{imap.FlagAnswered}},
		{raw: msg("News <news@example.com>", "m6@example.com", true)},
		{raw: msg("Bank <alerts@bank.example>", "m7@bank.example", true)},
	}
}

// seedStore mirrors fixtureMessages into the local database. UID 6's stored
// Message-ID is deliberately wrong so the identity check has something to
// catch.
func seedStore(t *testing.T, st *store.Store, cl *imapx.Client) store.Account {
	t.Helper()
	ctx := context.Background()

	acct, err := st.UpsertAccount(ctx, "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	rows := []store.Message{
		{UID: 1, MessageID: "m1@example.com", SenderKey: newsKey, HasUnsub: true},
		{UID: 2, MessageID: "m2@example.com", SenderKey: newsKey, HasUnsub: true},
		{UID: 3, MessageID: "m3@example.com", SenderKey: newsKey},
		{UID: 4, MessageID: "m4@example.com", SenderKey: newsKey, HasUnsub: true},
		{UID: 5, MessageID: "m5@example.com", SenderKey: newsKey, HasUnsub: true},
		{UID: 6, MessageID: "altered@example.com", SenderKey: newsKey, HasUnsub: true},
		{UID: 7, MessageID: "m7@bank.example", SenderKey: protectedKey, HasUnsub: true},
	}
	msgs := make([]store.Message, 0, len(rows))
	for i, m := range rows {
		m.AccountID = acct.ID
		m.Folder = "INBOX"
		m.InternalDate = ts(i)
		m.Size = 100
		if m.SenderKey == newsKey {
			m.FromDisplay, m.FromAddress, m.DomainKey = "News", "news@example.com", "example.com"
		} else {
			m.FromDisplay, m.FromAddress, m.DomainKey = "Bank", "alerts@bank.example", "bank.example"
		}
		if m.HasUnsub {
			m.Method = "oneclick"
			m.OneClick = true
			m.UnsubURIs = []string{"https://example.com/u/" + m.MessageID}
		}
		msgs = append(msgs, m)
	}
	if err := st.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	uidValidity := mustSelectUIDValidity(t, cl, "INBOX")
	if err := st.SetFolderCursor(ctx, acct.ID, "INBOX", uidValidity, 7); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}
	return acct
}

func deletePlan() *plan.Plan {
	return &plan.Plan{
		RunID:    "20260906-120000-aa11",
		Account:  "personal",
		Snapshot: plan.Snapshot{MessageCount: 7},
		Decisions: []plan.Entry{
			{SenderKey: newsKey, Display: "News", Address: "news@example.com",
				DeleteMatched: true, Method: "oneclick", OneClick: true},
			{SenderKey: protectedKey, Display: "Bank", Address: "alerts@bank.example",
				DeleteAll: true, Method: "oneclick"},
		},
	}
}

func testDeps(st *store.Store, cl *imapx.Client, acct store.Account, dataDir string) Deps {
	return Deps{
		Store:   st,
		Client:  cl,
		Account: acct,
		Cfg:     config.Account{Name: "personal", Host: "localhost", Username: testUser},
		Protect: config.Protect{Addresses: []string{"alerts@bank.example"}},
		DataDir: dataDir,
	}
}

func auditReasons(t *testing.T, path string) (map[string]int, map[uint32]bool) {
	t.Helper()
	lines, err := ReadLines(path)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	reasons := map[string]int{}
	moved := map[uint32]bool{}
	for _, l := range lines {
		switch l.Action {
		case actionSkip:
			reasons[l.Error]++
		case actionMove:
			if l.Status == statusDone {
				continue // per-batch confirmation, no single UID
			}
			moved[l.UID] = true
			if l.From != "INBOX" || l.To != "Trash" {
				t.Errorf("move line from=%q to=%q, want INBOX -> Trash", l.From, l.To)
			}
			if l.RunID == "" || l.Account != "personal" || l.TS.IsZero() {
				t.Errorf("move line missing run context: %+v", l)
			}
		}
	}
	return reasons, moved
}

func TestApplyDeleteSafetyFilters(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	var out bytes.Buffer
	sum, err := Run(ctx, testDeps(st, cl, acct, dataDir), deletePlan(), Options{Yes: true, Out: &out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if sum.Moved != 2 {
		t.Fatalf("Moved = %d, want 2 (uids 1 and 2)", sum.Moved)
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 5 {
		t.Fatalf("INBOX = %v, want 5 messages left", got)
	}
	trash := folderMessageIDs(t, addr, "Trash")
	if len(trash) != 2 || !trash["m1@example.com"] || !trash["m2@example.com"] {
		t.Fatalf("Trash = %v, want m1 and m2 only", trash)
	}

	reasons, moved := auditReasons(t, sum.AuditPath)
	if !moved[1] || !moved[2] || len(moved) != 2 {
		t.Fatalf("audited moves = %v, want uids 1 and 2", moved)
	}
	for _, want := range []string{reasonFlagged, reasonAnswered, reasonIdentityMismatch, reasonProtectedAll} {
		if reasons[want] == 0 {
			t.Errorf("audit log has no %q skip; got %v", want, reasons)
		}
	}
	if sum.SkippedReasons[reasonProtectedAll] != 1 {
		t.Errorf("SkippedReasons = %v", sum.SkippedReasons)
	}

	// Store rows go away only for the messages that actually moved.
	left, err := st.MessagesForSender(ctx, acct.ID, newsKey, false)
	if err != nil {
		t.Fatalf("MessagesForSender: %v", err)
	}
	leftUIDs := map[uint32]bool{}
	for _, m := range left {
		leftUIDs[m.UID] = true
	}
	if len(leftUIDs) != 4 || leftUIDs[1] || leftUIDs[2] {
		t.Fatalf("remaining store rows = %v, want uids 3,4,5,6", leftUIDs)
	}
	if rows, err := st.MessagesForSender(ctx, acct.ID, protectedKey, false); err != nil || len(rows) != 1 {
		t.Fatalf("protected sender rows = %v (%v), want 1 untouched", rows, err)
	}

	run, err := st.GetRun(ctx, "20260906-120000-aa11")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.FinishedAt.IsZero() || !strings.Contains(run.Summary, "moved 2") {
		t.Fatalf("run row = %+v", run)
	}
}

func TestApplyConfirmFalseChangesNothing(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	var shown string
	sum, err := Run(ctx, testDeps(st, cl, acct, dataDir), deletePlan(), Options{
		Confirm: func(s string) (bool, error) { shown = s; return false, nil },
	})
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("Run err = %v, want ErrAborted", err)
	}
	if sum.Moved != 0 {
		t.Fatalf("Moved = %d after abort", sum.Moved)
	}
	if !strings.Contains(shown, "Trash") || !strings.Contains(shown, "INBOX") {
		t.Fatalf("confirmation text did not describe the work: %q", shown)
	}

	if got := folderUIDs(t, addr, "INBOX"); len(got) != 7 {
		t.Fatalf("INBOX = %v, want all 7 messages", got)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v, want empty", got)
	}
	if _, err := os.Stat(AuditPath(dataDir, "20260906-120000-aa11")); !os.IsNotExist(err) {
		t.Fatalf("audit log exists after an aborted run (%v)", err)
	}
	if _, err := st.GetRun(ctx, "20260906-120000-aa11"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run row written after an aborted run: %v", err)
	}
	if n, err := st.MessageCount(ctx, acct.ID); err != nil || n != 7 {
		t.Fatalf("MessageCount = %d (%v), want 7", n, err)
	}
}

func TestApplyRejectsPlanForAnotherAccount(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)

	p := deletePlan()
	p.Account = "work"
	_, err := Run(context.Background(), testDeps(st, cl, acct, dataDir), p, Options{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "plan is for account") {
		t.Fatalf("Run err = %v, want an account mismatch", err)
	}
}

func TestApplyWarnsWhenDatabaseMoved(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)

	p := deletePlan()
	p.Snapshot.MessageCount = 99
	var out bytes.Buffer
	if _, err := Run(context.Background(), testDeps(st, cl, acct, dataDir), p,
		Options{Yes: true, NoDelete: true, Out: &out}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "database changed since the plan was written") {
		t.Fatalf("no snapshot warning in %q", out.String())
	}
}

func TestApplyUIDValidityChangeSkipsFolder(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	if err := st.SetFolderCursor(ctx, acct.ID, "INBOX", 999999, 7); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}
	sum, err := Run(ctx, testDeps(st, cl, acct, dataDir), deletePlan(), Options{Yes: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Moved != 0 {
		t.Fatalf("Moved = %d, want 0 when UIDVALIDITY changed", sum.Moved)
	}
	if len(sum.FoldersSkipped) != 1 || !strings.Contains(sum.FoldersSkipped[0], reasonUIDValidity) {
		t.Fatalf("FoldersSkipped = %v", sum.FoldersSkipped)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v, want empty", got)
	}
	reasons, _ := auditReasons(t, sum.AuditPath)
	if reasons[reasonUIDValidity] == 0 {
		t.Fatalf("audit log has no uidvalidity skip: %v", reasons)
	}
}

func TestApplyNeverReadsFromTrash(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	// A stray row claiming the sender's mail lives in Trash must not be acted on.
	if err := st.InsertMessages(ctx, []store.Message{{
		AccountID: acct.ID, Folder: "Trash", UID: 1, MessageID: "m1@example.com",
		SenderKey: newsKey, HasUnsub: true, InternalDate: ts(9), Size: 10,
	}}); err != nil {
		t.Fatalf("insert trash row: %v", err)
	}

	sum, err := Run(ctx, testDeps(st, cl, acct, dataDir), deletePlan(), Options{Yes: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	reasons, _ := auditReasons(t, sum.AuditPath)
	if reasons[reasonProtectedFolder] == 0 {
		t.Fatalf("no protected-folder skip recorded: %v", reasons)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 2 {
		t.Fatalf("Trash = %v, want only the two moved messages", got)
	}
}

func TestApplyUnsubscribeOneClick(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	var gotBody, gotType, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMethod = req.Method
		gotType = req.Header.Get("Content-Type")
		buf := make([]byte, 256)
		n, _ := req.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Point the newest stored message at the test server: apply prefers the
	// store's freshest URI over the plan's copy.
	if err := st.InsertMessages(ctx, []store.Message{{
		AccountID: acct.ID, Folder: "INBOX", UID: 2, MessageID: "m2@example.com",
		SenderKey: newsKey, FromAddress: "news@example.com", DomainKey: "example.com",
		HasUnsub: true, Method: "oneclick", OneClick: true,
		UnsubURIs: []string{srv.URL + "/u"}, InternalDate: ts(99), Size: 100,
	}}); err != nil {
		t.Fatalf("update newest message: %v", err)
	}

	p := deletePlan()
	p.Decisions[0].Unsubscribe = true
	p.Decisions[0].DeleteMatched = false
	p.Decisions[0].URIs = []string{"https://stale.example/never"}
	p.Decisions[1].DeleteAll = false
	p.Decisions[1].Unsubscribe = true

	d := testDeps(st, cl, acct, dataDir)
	d.Unsub = unsub.New(unsub.Options{
		HTTPGet:           true,
		PerHostRPS:        1000,
		MaxInFlight:       4,
		Client:            srv.Client(),
		AllowPrivateHosts: true,
	})

	sum, err := Run(ctx, d, p, Options{Yes: true, NoDelete: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotMethod != http.MethodPost || gotType != "application/x-www-form-urlencoded" {
		t.Fatalf("one-click request: method=%q type=%q", gotMethod, gotType)
	}
	if gotBody != "List-Unsubscribe=One-Click" {
		t.Fatalf("one-click body = %q", gotBody)
	}
	if sum.Unsub[unsub.StatusOK] != 1 {
		t.Fatalf("Unsub = %v, want one ok (the protected sender is dropped)", sum.Unsub)
	}

	lines, err := ReadLines(sum.AuditPath)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	found := false
	for _, l := range lines {
		if l.Action == actionUnsubscribe && l.SenderKey == newsKey {
			found = true
			if l.Status != string(unsub.StatusOK) || l.HTTPStatus != http.StatusOK || l.Method != "oneclick" {
				t.Fatalf("unsubscribe audit line = %+v", l)
			}
		}
	}
	if !found {
		t.Fatal("no unsubscribe audit line for the news sender")
	}

	var status, runID string
	groups, err := st.SenderGroups(ctx, acct.ID)
	if err != nil {
		t.Fatalf("SenderGroups: %v", err)
	}
	for _, g := range groups {
		if g.SenderKey == newsKey {
			status = g.UnsubStatus
		}
	}
	if status != string(unsub.StatusOK) {
		t.Fatalf("stored unsub status = %q, want ok", status)
	}
	run, err := st.GetRun(ctx, p.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	runID = run.ID
	if runID != p.RunID {
		t.Fatalf("run id = %q", runID)
	}
}

// TestApplyProtectedListReadFailureAborts covers the fail-closed rule: if the
// protected list cannot be read, the run must not guess. Nothing moves and no
// audit log is created.
func TestApplyProtectedListReadFailureAborts(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	if err := st.SetProtected(ctx, acct.ID, protectedKey, true); err != nil {
		t.Fatalf("SetProtected: %v", err)
	}

	// Break the senders table behind the store's back, so ListProtected and
	// SenderGroups both fail the way a corrupt database would.
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "mailshear.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE senders RENAME TO senders_broken`); err != nil {
		db.Close()
		t.Fatalf("rename senders: %v", err)
	}
	db.Close()

	d := testDeps(st, cl, acct, dataDir)
	d.Protect = config.Protect{} // protection would come from the database only

	sum, err := Run(ctx, d, deletePlan(), Options{Yes: true, Out: io.Discard})
	if err == nil {
		t.Fatal("Run succeeded with an unreadable protected list; want an error")
	}
	if !strings.Contains(err.Error(), "cannot load protected senders") ||
		!strings.Contains(err.Error(), "refusing to apply") {
		t.Fatalf("Run err = %v, want the fail-closed message", err)
	}
	if sum.Moved != 0 {
		t.Fatalf("Moved = %d, want 0", sum.Moved)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v, want empty", got)
	}
	if got := folderUIDs(t, addr, "INBOX"); len(got) != 7 {
		t.Fatalf("INBOX = %v, want all 7 messages", got)
	}
	if _, statErr := os.Stat(AuditPath(dataDir, "20260906-120000-aa11")); !os.IsNotExist(statErr) {
		t.Fatalf("audit log written despite the abort (%v)", statErr)
	}
}

// TestApplyAuditFailureStopsBeforeAnyMove covers the write-ahead rule: the
// intent lines must be on disk before the first MOVE, so an unwritable audit
// log stops the run with nothing moved rather than moving mail unrecorded.
func TestApplyAuditFailureStopsBeforeAnyMove(t *testing.T) {
	const n = 250 // more than moveBatch, so a per-batch-only check would leak
	msgs := make([]testMsg, 0, n)
	for i := 1; i <= n; i++ {
		msgs = append(msgs, testMsg{raw: msg("News <news@example.com>", fmt.Sprintf("c%d@example.com", i), true)})
	}
	addr := startServer(t, msgs)
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	ctx := context.Background()

	acct, err := st.UpsertAccount(ctx, "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	rows := make([]store.Message, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, store.Message{
			AccountID: acct.ID, Folder: "INBOX", UID: uint32(i),
			MessageID: fmt.Sprintf("c%d@example.com", i), SenderKey: newsKey,
			HasUnsub: true, InternalDate: ts(i), Size: 10,
		})
	}
	if err := st.InsertMessages(ctx, rows); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	uv := mustSelectUIDValidity(t, cl, "INBOX")
	if err := st.SetFolderCursor(ctx, acct.ID, "INBOX", uv, n); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}

	au, err := OpenAudit(dataDir, "20260906-160000-ee55", "personal")
	if err != nil {
		t.Fatalf("OpenAudit: %v", err)
	}
	// Close the file with no write yet attempted, so Err() is still nil when
	// the run starts: only the write-ahead line can catch this.
	if err := au.f.Close(); err != nil {
		t.Fatalf("close audit file: %v", err)
	}

	r := &runner{
		d: testDeps(st, cl, acct, dataDir), out: io.Discard,
		runID: "20260906-160000-ee55", trash: "Trash", au: au,
		sum:      Summary{Unsub: map[unsub.Status]int{}, SkippedReasons: map[string]int{}},
		noSource: map[string]bool{"trash": true},
	}
	all, err := st.MessagesForSender(ctx, acct.ID, newsKey, true)
	if err != nil {
		t.Fatalf("MessagesForSender: %v", err)
	}
	sender := map[uint32]string{}
	for _, m := range all {
		sender[m.UID] = newsKey
	}

	err = r.deleteFolder(ctx, "INBOX", all, sender, nil)
	if err == nil {
		t.Fatal("deleteFolder succeeded with an unwritable audit log; want an error")
	}
	if !strings.Contains(err.Error(), "audit log") {
		t.Fatalf("err = %v, want it to name the audit log", err)
	}
	if r.sum.Moved != 0 {
		t.Fatalf("Moved = %d, want 0", r.sum.Moved)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v, want empty", got)
	}
}

// TestApplyAuditRecordsIntentThenConfirmation checks the two-phase move
// record: one pending line per UID and one done line per batch.
func TestApplyAuditRecordsIntentThenConfirmation(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)

	sum, err := Run(context.Background(), testDeps(st, cl, acct, dataDir), deletePlan(), Options{Yes: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines, err := ReadLines(sum.AuditPath)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	var pending []uint32
	var done [][]uint32
	for _, l := range lines {
		if l.Action != actionMove {
			continue
		}
		switch l.Status {
		case statusPending:
			pending = append(pending, l.UID)
		case statusDone:
			done = append(done, l.UIDs)
		default:
			t.Errorf("move line with status %q: %+v", l.Status, l)
		}
	}
	if len(pending) != 2 || pending[0] != 1 || pending[1] != 2 {
		t.Fatalf("pending move lines = %v, want uids 1 and 2", pending)
	}
	if len(done) != 1 || len(done[0]) != 2 {
		t.Fatalf("confirmation lines = %v, want one batch of 2", done)
	}
}

func TestIneligibleFlagsUnavailableAndScanTimeFlags(t *testing.T) {
	ids := map[uint32]imapx.Identity{9: {MessageID: "x@y", Subject: "Weekly digest"}}
	r := &runner{}

	// The apply-time FETCH omitted uid 9 entirely: unknown, not clean.
	clean := store.Message{UID: 9, MessageID: "x@y"}
	if reason, skip := r.ineligible(clean, map[uint32][]string{}, ids, 9, false); !skip || reason != reasonFlagsUnavailable {
		t.Fatalf("missing flags entry = (%q, %v), want (%q, true)", reason, skip, reasonFlagsUnavailable)
	}

	// The server says the message is clean, but the scan saw \Flagged.
	scanned := store.Message{UID: 9, MessageID: "x@y", Flags: []string{"\\Flagged"}}
	if reason, skip := r.ineligible(scanned, map[uint32][]string{9: {}}, ids, 9, false); !skip || reason != reasonFlagged {
		t.Fatalf("scan-time flag = (%q, %v), want (%q, true)", reason, skip, reasonFlagged)
	}
	answered := store.Message{UID: 9, MessageID: "x@y", Flags: []string{"\\Answered"}}
	if reason, skip := r.ineligible(answered, map[uint32][]string{9: {}}, ids, 9, false); !skip || reason != reasonAnswered {
		t.Fatalf("scan-time answered = (%q, %v), want (%q, true)", reason, skip, reasonAnswered)
	}

	// Both agree it is clean.
	if reason, skip := r.ineligible(clean, map[uint32][]string{9: {}}, ids, 9, false); skip {
		t.Fatalf("clean message = (%q, %v), want eligible", reason, skip)
	}
}

// TestIneligibleKeepsTransactionalMail covers the keep rule: the live subject
// decides, the stored category is the fallback, and include_kept waives both.
func TestIneligibleKeepsTransactionalMail(t *testing.T) {
	r := &runner{}
	flags := map[uint32][]string{9: {}}
	m := store.Message{UID: 9, MessageID: "x@y"}

	live := map[uint32]imapx.Identity{9: {MessageID: "x@y", Subject: "Your receipt from Acme"}}
	if reason, skip := r.ineligible(m, flags, live, 9, false); !skip || reason != "kept: receipt" {
		t.Fatalf("live receipt = (%q, %v), want (\"kept: receipt\", true)", reason, skip)
	}
	if reason, skip := r.ineligible(m, flags, live, 9, true); skip {
		t.Fatalf("include_kept should waive the rule, got (%q, %v)", reason, skip)
	}

	// The live subject says nothing, but the scan classified it.
	plain := map[uint32]imapx.Identity{9: {MessageID: "x@y", Subject: "Weekly digest"}}
	stored := store.Message{UID: 9, MessageID: "x@y", Keep: "security"}
	if reason, skip := r.ineligible(stored, flags, plain, 9, false); !skip || reason != "kept: security" {
		t.Fatalf("stored category = (%q, %v), want (\"kept: security\", true)", reason, skip)
	}

	// With the rule off nothing is kept.
	off, err := keep.New(false, nil)
	if err != nil {
		t.Fatalf("keep.New: %v", err)
	}
	rOff := &runner{d: Deps{Keep: off}}
	if reason, skip := rOff.ineligible(stored, flags, live, 9, false); skip {
		t.Fatalf("keep_transactional off = (%q, %v), want eligible", reason, skip)
	}
}

func TestValidRunID(t *testing.T) {
	good := []string{"20260906-120000-aa11", "20260101-000000-0000", "20991231-235959-ffff"}
	for _, s := range good {
		if !ValidRunID(s) {
			t.Errorf("ValidRunID(%q) = false, want true", s)
		}
	}
	bad := []string{
		"../../outside",
		"../../../etc/passwd",
		"20260906-120000-aa11/../../x",
		"20260906-120000-AA11",
		"20260906-120000-aa1",
		"20260906-120000",
		"",
		".",
		"nope",
	}
	for _, s := range bad {
		if ValidRunID(s) {
			t.Errorf("ValidRunID(%q) = true, want false", s)
		}
	}
	// The traversal string must also not escape the audit directory once the
	// run id is rejected; this documents why the check exists.
	p := filepath.Clean(AuditPath("/data", "../../outside"))
	if strings.HasPrefix(p, filepath.Join("/data", "audit")) {
		t.Fatalf("AuditPath cleaned to %s, which no longer needs the run-id check", p)
	}
}

// TestApplyRefusesDeleteAllOnOwnAddress: on Gmail, All Mail includes sent
// mail, so delete_all on the account's own address is refused.
func TestApplyRefusesDeleteAllOnOwnAddress(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)

	d := testDeps(st, cl, acct, dataDir)
	d.Cfg.Username = "news@example.com" // the plan's delete_matched sender
	d.Protect = config.Protect{}

	p := deletePlan()
	p.Decisions[0].DeleteMatched = false
	p.Decisions[0].DeleteAll = true
	p.Decisions = p.Decisions[:1]

	sum, err := Run(context.Background(), d, p, Options{Yes: true, Out: io.Discard})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Moved != 0 {
		t.Fatalf("Moved = %d, want 0 for the account's own address", sum.Moved)
	}
	if sum.SkippedReasons[reasonOwnAddress] != 1 {
		t.Fatalf("SkippedReasons = %v, want one %q", sum.SkippedReasons, reasonOwnAddress)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v, want empty", got)
	}
}

// TestPrepareHasNoSideEffects locks in the split: Prepare may read the
// database and the server, but must not open a run, write an audit log, or
// move anything.
func TestPrepareHasNoSideEffects(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	pd, err := Prepare(ctx, testDeps(st, cl, acct, dataDir), deletePlan())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if pd.Preview.RunID != "20260906-120000-aa11" {
		t.Fatalf("RunID = %q", pd.Preview.RunID)
	}
	if len(pd.Preview.Rows) == 0 {
		t.Fatal("Preview has no rows")
	}
	if pd.Preview.Trash != "Trash" {
		t.Fatalf("Trash = %q, want Trash", pd.Preview.Trash)
	}
	if pd.Preview.Messages == 0 {
		t.Fatal("Preview.Messages = 0, want the pre-filtered upper bound")
	}

	if _, err := os.Stat(AuditPath(dataDir, "20260906-120000-aa11")); !os.IsNotExist(err) {
		t.Fatalf("audit log exists after Prepare (%v)", err)
	}
	if _, err := st.GetRun(ctx, "20260906-120000-aa11"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run row written by Prepare: %v", err)
	}
	if got := folderUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v after Prepare, want empty", got)
	}
	if n, err := st.MessageCount(ctx, acct.ID); err != nil || n != 7 {
		t.Fatalf("MessageCount = %d (%v), want 7", n, err)
	}
}

// TestExecuteStreamsEvents checks that ExecOptions.OnEvent sees the delete
// batches and the terminating done event.
func TestExecuteStreamsEvents(t *testing.T) {
	addr := startServer(t, fixtureMessages())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedStore(t, st, cl)
	ctx := context.Background()

	pd, err := Prepare(ctx, testDeps(st, cl, acct, dataDir), deletePlan())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var events []Event
	sum, err := pd.Execute(ctx, ExecOptions{
		NoUnsubscribe: true,
		OnEvent:       func(e Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if last := events[len(events)-1]; last.Phase != PhaseDone {
		t.Fatalf("last event phase = %q, want done", last.Phase)
	}
	moved := 0
	for _, e := range events {
		if e.Phase == PhaseDelete {
			moved += e.Moved
		}
	}
	if moved != sum.Moved {
		t.Fatalf("events reported %d moved, summary says %d", moved, sum.Moved)
	}
}
