package imapx

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	testUser = "user"
	testPass = "pass"
)

var testMessages = []string{
	"From: Alice <alice@example.com>\r\n" +
		"Subject: One\r\n" +
		"Message-Id: <1@example.com>\r\n" +
		"List-Unsubscribe: <https://example.com/u/1>\r\n" +
		"\r\n" +
		"body one\r\n",

	"From: Bob <bob@example.net>\r\n" +
		"Subject: Two\r\n" +
		"Message-Id: <2@example.net>\r\n" +
		"List-Id: Bob List <bob.example.net>\r\n" +
		"List-Unsubscribe: <https://example.net/u/2>,\r\n" +
		" <mailto:leave@example.net>\r\n" +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
		"\r\n" +
		"body two\r\n",

	"From: Carol <carol@example.org>\r\n" +
		"Subject: Three\r\n" +
		"Message-Id: <3@example.org>\r\n" +
		"\r\n" +
		"body three\r\n",
}

func startServer(t *testing.T) string {
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
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIMAP4rev2: {},
		},
	})

	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	appendMessages(t, ln.Addr().String())
	return ln.Addr().String()
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}

func appendMessages(t *testing.T, addr string) {
	t.Helper()

	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	for i, raw := range testMessages {
		var opts *imap.AppendOptions
		if i == 1 {
			opts = &imap.AppendOptions{Flags: []imap.Flag{imap.FlagFlagged}}
		}
		cmd := c.Append("INBOX", int64(len(raw)), opts)
		if _, err := io.WriteString(cmd, raw); err != nil {
			t.Fatalf("append write: %v", err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatalf("append close: %v", err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	addr := startServer(t)
	c, err := dialInsecure(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dialInsecure: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestDialCaps(t *testing.T) {
	c := newTestClient(t)
	if c.Gmail {
		t.Error("Gmail = true, want false")
	}
	if !c.HasMove {
		t.Error("HasMove = false, want true")
	}
	if !c.HasUIDPlus {
		t.Error("HasUIDPlus = false, want true")
	}
	if !c.Caps.Has(imap.CapIMAP4rev2) {
		t.Error("Caps missing IMAP4rev2")
	}
}

func TestSelectReadOnly(t *testing.T) {
	c := newTestClient(t)
	mbox, err := c.SelectReadOnly(context.Background(), "INBOX")
	if err != nil {
		t.Fatalf("SelectReadOnly: %v", err)
	}
	if mbox.Name != "INBOX" {
		t.Errorf("Name = %q, want INBOX", mbox.Name)
	}
	if mbox.UIDValidity == 0 {
		t.Error("UIDValidity = 0")
	}
	if mbox.UIDNext != 4 {
		t.Errorf("UIDNext = %d, want 4", mbox.UIDNext)
	}
}

func TestSelectReadOnlyMissing(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.SelectReadOnly(context.Background(), "Nope"); err == nil {
		t.Fatal("SelectReadOnly on missing mailbox: want error")
	}
}

func TestFetchHeaders(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if _, err := c.SelectReadOnly(ctx, "INBOX"); err != nil {
		t.Fatalf("SelectReadOnly: %v", err)
	}

	var got []Header
	if err := c.FetchHeaders(ctx, 1, 0, func(h Header) error {
		got = append(got, h)
		return nil
	}); err != nil {
		t.Fatalf("FetchHeaders: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d headers, want 3", len(got))
	}

	for i, h := range got {
		if h.UID != uint32(i+1) {
			t.Errorf("msg %d: UID = %d, want %d", i, h.UID, i+1)
		}
		if want := int64(len(testMessages[i])); h.Size != want {
			t.Errorf("msg %d: Size = %d, want %d", i, h.Size, want)
		}
		if h.InternalDate.IsZero() {
			t.Errorf("msg %d: InternalDate is zero", i)
		}
		if h.GmMsgID != 0 || h.GmLabels != nil {
			t.Errorf("msg %d: Gmail fields set on non-Gmail server", i)
		}
	}

	if v := got[0].Fields["List-Unsubscribe"]; len(v) != 1 || v[0] != "<https://example.com/u/1>" {
		t.Errorf("msg 0 List-Unsubscribe = %q", v)
	}
	if v := got[0].Fields["From"]; len(v) != 1 || v[0] != "Alice <alice@example.com>" {
		t.Errorf("msg 0 From = %q", v)
	}
	if v := got[0].Fields["Message-Id"]; len(v) != 1 || v[0] != "<1@example.com>" {
		t.Errorf("msg 0 Message-Id = %q", v)
	}

	wantFolded := "<https://example.net/u/2>, <mailto:leave@example.net>"
	if v := got[1].Fields["List-Unsubscribe"]; len(v) != 1 || v[0] != wantFolded {
		t.Errorf("msg 1 List-Unsubscribe = %q, want %q", v, wantFolded)
	}
	if strings.ContainsAny(strings.Join(got[1].Fields["List-Unsubscribe"], ""), "\r\n") {
		t.Error("msg 1 List-Unsubscribe still folded")
	}
	if v := got[1].Fields["List-Id"]; len(v) != 1 || v[0] != "Bob List <bob.example.net>" {
		t.Errorf("msg 1 List-Id = %q", v)
	}
	if v := got[1].Fields["List-Unsubscribe-Post"]; len(v) != 1 || v[0] != "List-Unsubscribe=One-Click" {
		t.Errorf("msg 1 List-Unsubscribe-Post = %q", v)
	}
	if !hasFlag(got[1].Flags, imap.FlagFlagged) {
		t.Errorf("msg 1 Flags = %q, want \\Flagged", got[1].Flags)
	}
	if _, ok := got[2].Fields["List-Unsubscribe"]; ok {
		t.Error("msg 2 has List-Unsubscribe")
	}
	if _, ok := got[2].Fields["Content-Type"]; ok {
		t.Error("msg 2 returned a field outside the requested set")
	}

	// BODY.PEEK must not mark messages read.
	for _, h := range got {
		if hasFlag(h.Flags, imap.FlagSeen) {
			t.Fatalf("uid %d already \\Seen during fetch", h.UID)
		}
	}
	var after []Header
	if err := c.FetchHeaders(ctx, 1, 3, func(h Header) error {
		after = append(after, h)
		return nil
	}); err != nil {
		t.Fatalf("FetchHeaders re-fetch: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("re-fetch got %d headers, want 3", len(after))
	}
	for _, h := range after {
		if hasFlag(h.Flags, imap.FlagSeen) {
			t.Errorf("uid %d got \\Seen after fetch", h.UID)
		}
	}
}

func TestFetchHeadersEmptyRange(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if _, err := c.SelectReadOnly(ctx, "INBOX"); err != nil {
		t.Fatalf("SelectReadOnly: %v", err)
	}
	n := 0
	if err := c.FetchHeaders(ctx, 100, 200, func(Header) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("FetchHeaders: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d headers, want 0", n)
	}
}

func TestFetchHeadersCallbackError(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if _, err := c.SelectReadOnly(ctx, "INBOX"); err != nil {
		t.Fatalf("SelectReadOnly: %v", err)
	}
	want := io.ErrUnexpectedEOF
	err := c.FetchHeaders(ctx, 1, 0, func(Header) error { return want })
	if err != want {
		t.Fatalf("FetchHeaders err = %v, want %v", err, want)
	}
}

func TestFindSpecialUse(t *testing.T) {
	// imapmemserver has no exported way to set mailbox special-use attributes
	// (Mailbox.specialUse is unexported and User.Create ignores
	// CreateOptions.SpecialUse), so only the "not found" path is testable here.
	c := newTestClient(t)
	name, err := c.FindSpecialUse(context.Background(), imap.MailboxAttrTrash)
	if err != nil {
		t.Fatalf("FindSpecialUse: %v", err)
	}
	if name != "" {
		t.Errorf("FindSpecialUse = %q, want empty", name)
	}
}

func TestSearchGmailRawUnsupported(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.SearchGmailRaw(context.Background(), "category:promotions", 1, 0); err == nil {
		t.Fatal("SearchGmailRaw: want error")
	}
}

func TestLoginError(t *testing.T) {
	base := &imap.Error{
		Type: imap.StatusResponseTypeNo,
		Code: imap.ResponseCodeAlert,
		Text: "Application-specific password required: https://support.google.com/accounts/answer/185833",
	}
	err := loginError("imap.gmail.com", base)
	if !strings.Contains(err.Error(), gmailLoginHint) {
		t.Errorf("gmail host: missing hint in %q", err)
	}
	err = loginError("imap.fastmail.com", base)
	if strings.Contains(err.Error(), gmailLoginHint) {
		t.Errorf("non-gmail host: unexpected hint in %q", err)
	}
	err = loginError("imap.gmail.com", &imap.Error{Type: imap.StatusResponseTypeNo, Text: "server busy"})
	if strings.Contains(err.Error(), gmailLoginHint) {
		t.Errorf("unrelated failure: unexpected hint in %q", err)
	}
}

func TestIsGoogleHost(t *testing.T) {
	yes := []string{"imap.gmail.com", "gmail.com", "imap.googlemail.com", "imap.google.com", "IMAP.GMAIL.COM."}
	no := []string{"imap.notgmail.com", "gmail.com.evil.net", "imap.fastmail.com", ""}
	for _, h := range yes {
		if !isGoogleHost(h) {
			t.Errorf("isGoogleHost(%q) = false", h)
		}
	}
	for _, h := range no {
		if isGoogleHost(h) {
			t.Errorf("isGoogleHost(%q) = true", h)
		}
	}
}

func hasFlag(flags []string, f imap.Flag) bool {
	for _, v := range flags {
		if strings.EqualFold(v, string(f)) {
			return true
		}
	}
	return false
}

// The memserver only ever returns the header fields the fetch asked for, so a
// header block that fails to parse cannot be produced through it. Test the
// per-message conversion FetchHeaders skips on instead.
func TestHeaderFromBuffer(t *testing.T) {
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader}

	buf := &imapclient.FetchMessageBuffer{
		UID:        7,
		RFC822Size: 42,
		Flags:      []imap.Flag{imap.FlagSeen},
		BodySection: []imapclient.FetchBodySectionBuffer{{
			Section: section,
			Bytes:   []byte("From: Alice <alice@example.com>\r\nSubject: One\r\n\r\n"),
		}},
	}
	h, err := headerFromBuffer(buf)
	if err != nil {
		t.Fatalf("headerFromBuffer: %v", err)
	}
	if h.UID != 7 || h.Size != 42 {
		t.Errorf("UID=%d Size=%d", h.UID, h.Size)
	}
	if v := h.Fields["From"]; len(v) != 1 || v[0] != "Alice <alice@example.com>" {
		t.Errorf("From = %q", v)
	}

	bad := &imapclient.FetchMessageBuffer{
		UID: 8,
		BodySection: []imapclient.FetchBodySectionBuffer{{
			Section: section,
			Bytes:   []byte("From: Alice <alice@example.com>\r\nthis line has no colon\r\n\r\n"),
		}},
	}
	got, err := headerFromBuffer(bad)
	if err == nil {
		t.Fatalf("headerFromBuffer on a malformed block: want error, got %+v", got)
	}
	if got.UID != 8 {
		t.Errorf("UID = %d, want 8 so the caller can log it", got.UID)
	}
	if strings.Contains(err.Error(), "alice@example.com") {
		t.Error("parse error quotes header content")
	}
}

// newTestClientAddr is newTestClient plus the server address, for tests that
// need a second connection to change server state behind the client's back.
func newTestClientAddr(t *testing.T) (*Client, string) {
	t.Helper()
	addr := startServer(t)
	c, err := dialInsecure(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dialInsecure: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, addr
}

func mailboxUIDs(t *testing.T, addr, mailbox string) []uint32 {
	t.Helper()
	c, err := dialInsecure(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dialInsecure: %v", err)
	}
	defer c.Close()
	if _, err := c.SelectReadOnly(context.Background(), mailbox); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var uids []uint32
	if err := c.FetchHeaders(context.Background(), 1, 0, func(h Header) error {
		uids = append(uids, h.UID)
		return nil
	}); err != nil {
		t.Fatalf("fetch %s: %v", mailbox, err)
	}
	return uids
}

func TestSelectReadWrite(t *testing.T) {
	c := newTestClient(t)
	mbox, err := c.Select(context.Background(), "INBOX")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if mbox.Name != "INBOX" || mbox.UIDValidity == 0 || mbox.UIDNext != 4 {
		t.Fatalf("Select = %+v", mbox)
	}
	if _, err := c.Select(context.Background(), "Nope"); err == nil {
		t.Fatal("Select on a missing mailbox: want error")
	}
}

func TestListMailboxes(t *testing.T) {
	c := newTestClient(t)
	names, err := c.ListMailboxes(context.Background())
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["INBOX"] || !got["Trash"] {
		t.Fatalf("ListMailboxes = %v, want INBOX and Trash", names)
	}
}

func TestFetchFlagsAndMessageIDs(t *testing.T) {
	c, addr := newTestClientAddr(t)
	ctx := context.Background()
	if _, err := c.Select(ctx, "INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}

	flags, err := c.FetchFlags(ctx, []uint32{1, 2, 3})
	if err != nil {
		t.Fatalf("FetchFlags: %v", err)
	}
	if len(flags) != 3 {
		t.Fatalf("FetchFlags returned %d entries, want 3", len(flags))
	}
	if !hasFlag(flags[2], imap.FlagFlagged) {
		t.Errorf("uid 2 flags = %v, want \\Flagged", flags[2])
	}
	if hasFlag(flags[1], imap.FlagAnswered) {
		t.Errorf("uid 1 flags = %v, want no \\Answered yet", flags[1])
	}

	// A flag set through the server must show up on the next fetch.
	other, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial second client: %v", err)
	}
	defer other.Close()
	if err := other.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login second client: %v", err)
	}
	if _, err := other.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("select on second client: %v", err)
	}
	var one imap.UIDSet
	one.AddNum(imap.UID(1))
	if err := other.Store(one, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagAnswered},
	}, nil).Close(); err != nil {
		t.Fatalf("store \\Answered: %v", err)
	}

	flags, err = c.FetchFlags(ctx, []uint32{1})
	if err != nil {
		t.Fatalf("FetchFlags after store: %v", err)
	}
	if !hasFlag(flags[1], imap.FlagAnswered) {
		t.Errorf("uid 1 flags = %v, want \\Answered", flags[1])
	}

	ids, err := c.FetchIdentity(ctx, []uint32{1, 2, 3})
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	want := map[uint32]Identity{
		1: {MessageID: "1@example.com", Subject: "One"},
		2: {MessageID: "2@example.net", Subject: "Two"},
		3: {MessageID: "3@example.org", Subject: "Three"},
	}
	for uid, id := range want {
		if ids[uid] != id {
			t.Errorf("uid %d identity = %+v, want %+v", uid, ids[uid], id)
		}
	}

	// The identity fetch must not mark anything read.
	flags, err = c.FetchFlags(ctx, []uint32{1, 2, 3})
	if err != nil {
		t.Fatalf("FetchFlags after message-id fetch: %v", err)
	}
	for uid, f := range flags {
		if hasFlag(f, imap.FlagSeen) {
			t.Errorf("uid %d got \\Seen from FetchIdentity", uid)
		}
	}
}

func TestMoveUIDs(t *testing.T) {
	c, addr := newTestClientAddr(t)
	ctx := context.Background()
	if !c.HasMove {
		t.Skip("test server does not advertise MOVE")
	}
	if _, err := c.Select(ctx, "INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := c.MoveUIDs(ctx, []uint32{1, 2, 3}, "Trash"); err != nil {
		t.Fatalf("MoveUIDs: %v", err)
	}
	if got := mailboxUIDs(t, addr, "INBOX"); len(got) != 0 {
		t.Fatalf("INBOX still holds %v", got)
	}
	if got := mailboxUIDs(t, addr, "Trash"); len(got) != 3 {
		t.Fatalf("Trash holds %v, want 3 messages", got)
	}

	// UIDs change on the move, so undo has to find messages by Message-ID.
	if _, err := c.Select(ctx, "Trash"); err != nil {
		t.Fatalf("Select Trash: %v", err)
	}
	uids, err := c.SearchMessageID(ctx, "2@example.net")
	if err != nil {
		t.Fatalf("SearchMessageID: %v", err)
	}
	if len(uids) != 1 {
		t.Fatalf("SearchMessageID = %v, want exactly one hit", uids)
	}
	ids, err := c.FetchIdentity(ctx, uids)
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if ids[uids[0]].MessageID != "2@example.net" {
		t.Fatalf("uid %d in Trash has Message-ID %q", uids[0], ids[uids[0]].MessageID)
	}
	if got, err := c.SearchMessageID(ctx, "<nosuch@example.com>"); err != nil || len(got) != 0 {
		t.Fatalf("SearchMessageID for a missing id = %v (%v)", got, err)
	}
	if _, err := c.SearchMessageID(ctx, "  "); err == nil {
		t.Fatal("SearchMessageID with an empty id: want error")
	}
}

func TestMoveUIDsCopyFallback(t *testing.T) {
	c, addr := newTestClientAddr(t)
	ctx := context.Background()
	if !c.HasUIDPlus {
		t.Skip("test server does not advertise UIDPLUS")
	}
	// Force the COPY + \Deleted + UID EXPUNGE path.
	c.HasMove = false

	if _, err := c.Select(ctx, "INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := c.MoveUIDs(ctx, []uint32{1, 3}, "Trash"); err != nil {
		t.Fatalf("MoveUIDs: %v", err)
	}
	if got := mailboxUIDs(t, addr, "INBOX"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("INBOX = %v, want only uid 2 left", got)
	}
	if got := mailboxUIDs(t, addr, "Trash"); len(got) != 2 {
		t.Fatalf("Trash = %v, want 2 messages", got)
	}
}

func TestMoveUIDsRefusesWithoutMoveOrUIDPlus(t *testing.T) {
	c, addr := newTestClientAddr(t)
	ctx := context.Background()
	c.HasMove = false
	c.HasUIDPlus = false

	if _, err := c.Select(ctx, "INBOX"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	err := c.MoveUIDs(ctx, []uint32{1}, "Trash")
	if err == nil {
		t.Fatal("MoveUIDs: want a refusal")
	}
	if !strings.Contains(err.Error(), "refusing to expunge") {
		t.Fatalf("MoveUIDs err = %v", err)
	}
	if got := mailboxUIDs(t, addr, "INBOX"); len(got) != 3 {
		t.Fatalf("INBOX = %v, want all 3 messages untouched", got)
	}
	if got := mailboxUIDs(t, addr, "Trash"); len(got) != 0 {
		t.Fatalf("Trash = %v, want empty", got)
	}

	// An empty UID list is a no-op even on a server that cannot move.
	if err := c.MoveUIDs(ctx, nil, "Trash"); err != nil {
		t.Fatalf("MoveUIDs(nil) = %v, want nil", err)
	}
}

func TestChunkUIDs(t *testing.T) {
	if got := chunkUIDs(nil, 10); got != nil {
		t.Fatalf("chunkUIDs(nil) = %v", got)
	}
	got := chunkUIDs([]uint32{1, 2, 3, 4, 5}, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Fatalf("chunkUIDs = %v", got)
	}
	if len(chunkUIDs([]uint32{1, 2}, 0)) != 2 {
		t.Fatal("chunkUIDs with a non-positive size must not loop forever")
	}
}

func TestDialXOAUTH2RefusesServerWithoutTheMechanism(t *testing.T) {
	addr := startServer(t)
	_, err := dialInsecureXOAUTH2(addr, testUser, "at-1")
	if err == nil {
		t.Fatal("dialInsecureXOAUTH2 succeeded against a server with no AUTH=XOAUTH2")
	}
	if !strings.Contains(err.Error(), "does not advertise AUTH=XOAUTH2") {
		t.Fatalf("error = %v, want the missing-mechanism message", err)
	}
	if !strings.Contains(err.Error(), "auth: file") {
		t.Fatalf("error = %v, want it to point at the app-password path", err)
	}
}

func TestXOAUTH2ErrorAddsTheGmailHint(t *testing.T) {
	err := xoauth2Error("imap.gmail.com", errors.New("NO invalid credentials"), "")
	if !strings.Contains(err.Error(), "sign in again from the accounts screen") {
		t.Fatalf("error = %v, want the re-login hint", err)
	}
	if !strings.Contains(err.Error(), "7 days") {
		t.Fatalf("error = %v, want the testing-mode note", err)
	}

	other := xoauth2Error("outlook.office365.com", errors.New("NO nope"), `{"status":"401"}`)
	if !strings.Contains(other.Error(), `server said {"status":"401"}`) {
		t.Fatalf("error = %v, want the server challenge", other)
	}
	if strings.Contains(other.Error(), "sign in again from the accounts screen") {
		t.Fatalf("error = %v, want no Gmail hint on a non-Google host", other)
	}
	if !strings.Contains(other.Error(), "xoauth2 login failed") {
		t.Fatalf("error = %v", other)
	}
}
