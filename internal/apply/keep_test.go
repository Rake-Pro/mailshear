package apply

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/keep"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// keepMsg is one message with a chosen subject and a List-Unsubscribe header,
// so every row in the fixture is in the default delete scope.
func keepMsg(id, subject string) string {
	return "From: Pay <service@pay.example>\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-Id: <" + id + ">\r\n" +
		"List-Unsubscribe: <https://pay.example/u/" + id + ">\r\n" +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
		"\r\n" +
		"body " + id + "\r\n"
}

const payKey = "addr:service@pay.example"

// keepFixture is four bulk messages from one sender: two ordinary, one the
// scan already classified as a receipt, and one whose transactional subject
// only the live fetch can see.
func keepFixture() []testMsg {
	return []testMsg{
		{raw: keepMsg("k1@pay.example", "Weekly digest")},
		{raw: keepMsg("k2@pay.example", "Your receipt from Acme")},
		{raw: keepMsg("k3@pay.example", "Half price today")},
		{raw: keepMsg("k4@pay.example", "Order confirmation #99")},
	}
}

func seedKeepStore(t *testing.T, st *store.Store, cl *imapx.Client) store.Account {
	t.Helper()
	ctx := context.Background()

	acct, err := st.UpsertAccount(ctx, "personal", "localhost", testUser)
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	rows := []store.Message{
		{UID: 1, MessageID: "k1@pay.example", Subject: "Weekly digest"},
		// The scan recognised this one; the preview can act on that.
		{UID: 2, MessageID: "k2@pay.example", Subject: "Your receipt from Acme", Keep: "receipt"},
		{UID: 3, MessageID: "k3@pay.example", Subject: "Half price today"},
		// Scanned before the keep column existed: only the live subject saves it.
		{UID: 4, MessageID: "k4@pay.example", Subject: "Order confirmation #99"},
	}
	msgs := make([]store.Message, 0, len(rows))
	for i, m := range rows {
		m.AccountID = acct.ID
		m.Folder = "INBOX"
		m.SenderKey = payKey
		m.FromDisplay, m.FromAddress, m.DomainKey = "Pay", "service@pay.example", "pay.example"
		m.HasUnsub = true
		m.Method = "oneclick"
		m.OneClick = true
		m.InternalDate = ts(i)
		m.Size = 100
		msgs = append(msgs, m)
	}
	if err := st.InsertMessages(ctx, msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	uidValidity := mustSelectUIDValidity(t, cl, "INBOX")
	if err := st.SetFolderCursor(ctx, acct.ID, "INBOX", uidValidity, 4); err != nil {
		t.Fatalf("set folder cursor: %v", err)
	}
	return acct
}

func keepPlan(includeKept bool) *plan.Plan {
	return &plan.Plan{
		RunID:    "20260906-130000-bb22",
		Account:  "personal",
		Snapshot: plan.Snapshot{MessageCount: 4},
		Decisions: []plan.Entry{{
			SenderKey: payKey, Display: "Pay", Address: "service@pay.example",
			DeleteMatched: true, IncludeKept: includeKept,
			Method: "oneclick", OneClick: true,
		}},
	}
}

func TestApplyKeepsTransactionalMail(t *testing.T) {
	addr := startServer(t, keepFixture())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedKeepStore(t, st, cl)

	d := testDeps(st, cl, acct, dataDir)
	pd, err := Prepare(context.Background(), d, keepPlan(false))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The preview knows about the row the scan classified, and only that one.
	if pd.Preview.Messages != 3 || pd.Preview.Kept != 1 {
		t.Fatalf("preview = %d messages, %d kept; want 3 and 1", pd.Preview.Messages, pd.Preview.Kept)
	}
	if r := pd.Preview.Rows[0]; r.Messages != 3 || r.Kept != 1 || r.IncludeKept {
		t.Fatalf("preview row = %+v", r)
	}
	if text := PreflightText(pd.Preview, false, true); !strings.Contains(text, "kept: 1 message(s)") {
		t.Fatalf("preflight text does not mention the kept messages:\n%s", text)
	}

	sum, err := pd.Execute(context.Background(), ExecOptions{NoUnsubscribe: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sum.Moved != 2 {
		t.Fatalf("Moved = %d, want 2 (the receipt and the order confirmation stay)", sum.Moved)
	}
	if sum.SkippedReasons["kept: receipt"] != 1 {
		t.Fatalf("SkippedReasons = %v, want one \"kept: receipt\"", sum.SkippedReasons)
	}
	if sum.SkippedReasons["kept: order"] != 1 {
		t.Fatalf("SkippedReasons = %v, want one \"kept: order\" from the live subject", sum.SkippedReasons)
	}

	left := folderMessageIDs(t, addr, "INBOX")
	for _, id := range []string{"k2@pay.example", "k4@pay.example"} {
		if !left[id] {
			t.Fatalf("%s was moved out of INBOX; kept mail must stay", id)
		}
	}
	for _, id := range []string{"k1@pay.example", "k3@pay.example"} {
		if left[id] {
			t.Fatalf("%s is still in INBOX", id)
		}
	}

	reasons, _ := auditReasons(t, sum.AuditPath)
	if reasons["kept: receipt"] != 1 || reasons["kept: order"] != 1 {
		t.Fatalf("audit reasons = %v", reasons)
	}
}

func TestApplyIncludeKeptWaivesTheRule(t *testing.T) {
	addr := startServer(t, keepFixture())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedKeepStore(t, st, cl)

	d := testDeps(st, cl, acct, dataDir)
	pd, err := Prepare(context.Background(), d, keepPlan(true))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if pd.Preview.Messages != 4 || pd.Preview.Kept != 0 {
		t.Fatalf("preview = %d messages, %d kept; want 4 and 0", pd.Preview.Messages, pd.Preview.Kept)
	}
	if !pd.Preview.Rows[0].IncludeKept {
		t.Fatal("preview row does not carry the waiver")
	}

	sum, err := pd.Execute(context.Background(), ExecOptions{NoUnsubscribe: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sum.Moved != 4 {
		t.Fatalf("Moved = %d, want all 4 with include_kept set", sum.Moved)
	}
	if len(folderUIDs(t, addr, "INBOX")) != 0 {
		t.Fatalf("INBOX still holds %v", folderUIDs(t, addr, "INBOX"))
	}
}

// TestApplyKeepDisabledDeletesEverything covers keep_transactional: false.
func TestApplyKeepDisabledDeletesEverything(t *testing.T) {
	addr := startServer(t, keepFixture())
	cl := dialTest(t, addr)
	st, dataDir := openStore(t)
	acct := seedKeepStore(t, st, cl)

	d := testDeps(st, cl, acct, dataDir)
	off, err := keep.New(false, nil)
	if err != nil {
		t.Fatalf("keep.New: %v", err)
	}
	d.Keep = off

	sum, err := Run(context.Background(), d, keepPlan(false), Options{
		Yes: true, NoUnsubscribe: true, Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Moved != 4 {
		t.Fatalf("Moved = %d, want 4 with the keep rule off", sum.Moved)
	}
}
