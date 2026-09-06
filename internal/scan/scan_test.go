package scan

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/keep"
	"github.com/Rake-Pro/mailshear/internal/store"
)

func hdr(uid uint32, fields map[string]string) imapx.Header {
	h := imapx.Header{
		UID:          uid,
		InternalDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Size:         1234,
		Fields:       map[string][]string{},
	}
	for k, v := range fields {
		h.Fields[k] = []string{v}
	}
	return h
}

// buildMessageT is buildMessage with the default keep matcher, so the
// existing cases read the same as before the keep column was added.
func buildMessageT(accountID int64, folder string, h imapx.Header) (store.Message, bool) {
	return buildMessage(accountID, folder, h, keep.Matcher{})
}

func TestBuildMessageBasics(t *testing.T) {
	h := hdr(7, map[string]string{
		"From":                  "Alice Example <Alice@Example.COM>",
		"Subject":               "Hello",
		"Message-Id":            "  <abc@example.com> ",
		"List-Unsubscribe":      "<https://example.com/u/1>, <mailto:leave@example.com>",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	})
	m, ok := buildMessage(1, "INBOX", h, keep.Matcher{})
	if !ok {
		t.Fatal("expected message to be built")
	}
	if m.MessageID != "abc@example.com" {
		t.Errorf("MessageID = %q, want angle brackets trimmed", m.MessageID)
	}
	if m.FromAddress != "alice@example.com" {
		t.Errorf("FromAddress = %q", m.FromAddress)
	}
	if m.SenderKey != "addr:alice@example.com" {
		t.Errorf("SenderKey = %q", m.SenderKey)
	}
	if m.DomainKey != "example.com" {
		t.Errorf("DomainKey = %q", m.DomainKey)
	}
	if !m.HasUnsub || m.Method != "oneclick" || !m.OneClick {
		t.Errorf("HasUnsub=%v Method=%q OneClick=%v", m.HasUnsub, m.Method, m.OneClick)
	}
	if len(m.UnsubURIs) != 2 {
		t.Errorf("UnsubURIs = %v", m.UnsubURIs)
	}
	if m.Subject != "Hello" || m.FromDisplay != "Alice Example" {
		t.Errorf("Subject=%q FromDisplay=%q", m.Subject, m.FromDisplay)
	}
}

func TestBuildMessageNoUnsubStoresNoSubject(t *testing.T) {
	m, ok := buildMessageT(1, "INBOX", hdr(8, map[string]string{
		"From":    "Alice <alice@example.com>",
		"Subject": "Receipt",
	}))
	if !ok {
		t.Fatal("expected message to be built")
	}
	if m.HasUnsub {
		t.Error("HasUnsub should be false")
	}
	if m.Subject != "" || m.Method != "" || m.FromDisplay != "" || len(m.UnsubURIs) != 0 {
		t.Errorf("non-bulk message stored extra data: %+v", m)
	}
}

func TestBuildMessageUnparseableUnsubIsStillBulk(t *testing.T) {
	m, ok := buildMessageT(1, "INBOX", hdr(9, map[string]string{
		"From":             "alice@example.com",
		"List-Unsubscribe": "unsubscribe by replying",
	}))
	if !ok {
		t.Fatal("expected message to be built")
	}
	if !m.HasUnsub {
		t.Error("present but unparseable List-Unsubscribe should still count as bulk")
	}
	if m.Method != "none" {
		t.Errorf("Method = %q, want none", m.Method)
	}
	if len(m.UnsubURIs) != 0 {
		t.Errorf("UnsubURIs = %v, want empty", m.UnsubURIs)
	}
}

func TestBuildMessageListIDBeatsFrom(t *testing.T) {
	m, ok := buildMessageT(1, "INBOX", hdr(10, map[string]string{
		"From":             "News <bounce-123@mail.example.net>",
		"List-Id":          "Weekly News <news.example.net>",
		"List-Unsubscribe": "<https://example.net/u>",
	}))
	if !ok {
		t.Fatal("expected message to be built")
	}
	if m.ListID != "news.example.net" {
		t.Errorf("ListID = %q", m.ListID)
	}
	if m.SenderKey != "list:news.example.net" {
		t.Errorf("SenderKey = %q, want list keying", m.SenderKey)
	}
	if m.DomainKey != "example.net" {
		t.Errorf("DomainKey = %q", m.DomainKey)
	}
}

func TestBuildMessageSenderFallbackAndSkip(t *testing.T) {
	m, ok := buildMessageT(1, "INBOX", hdr(11, map[string]string{
		"From":   "",
		"Sender": "Bot <bot@example.org>",
	}))
	if !ok {
		t.Fatal("expected Sender fallback to build a message")
	}
	if m.FromAddress != "bot@example.org" {
		t.Errorf("FromAddress = %q", m.FromAddress)
	}

	if _, ok := buildMessageT(1, "INBOX", hdr(12, map[string]string{"Subject": "orphan"})); ok {
		t.Error("message with no address and no List-Id should be skipped")
	}
}

func TestBuildMessageSubjectTruncated(t *testing.T) {
	long := strings.Repeat("e", 250)
	m, ok := buildMessageT(1, "INBOX", hdr(13, map[string]string{
		"From":             "alice@example.com",
		"Subject":          long,
		"List-Unsubscribe": "<https://example.com/u>",
	}))
	if !ok {
		t.Fatal("expected message to be built")
	}
	if got := len([]rune(m.Subject)); got != subjectRunes {
		t.Errorf("subject runes = %d, want %d", got, subjectRunes)
	}
}

func TestBuildMessageDecodesEncodedWords(t *testing.T) {
	m, _ := buildMessageT(1, "INBOX", hdr(14, map[string]string{
		"From":             "=?utf-8?q?Caf=C3=A9?= <hi@example.com>",
		"Subject":          "=?utf-8?q?Caf=C3=A9_news?=",
		"List-Unsubscribe": "<https://example.com/u>",
	}))
	if m.FromDisplay != "Café" {
		t.Errorf("FromDisplay = %q", m.FromDisplay)
	}
	if m.Subject != "Café news" {
		t.Errorf("Subject = %q", m.Subject)
	}
}

// TestBuildMessageSetsKeep: every message gets a transactional category from
// its decoded subject, bulk or not, because the subject of a non-bulk message
// is dropped and the category is all that survives.
func TestBuildMessageSetsKeep(t *testing.T) {
	cases := []struct {
		name    string
		fields  map[string]string
		keep    string
		subject string
	}{
		{
			name: "bulk receipt",
			fields: map[string]string{
				"From":             "Pay <service@pay.example>",
				"Subject":          "Your receipt from Acme",
				"List-Unsubscribe": "<https://pay.example/u/1>",
			},
			keep:    "receipt",
			subject: "Your receipt from Acme",
		},
		{
			name: "non-bulk receipt keeps the category but not the subject",
			fields: map[string]string{
				"From":    "Pay <service@pay.example>",
				"Subject": "Order confirmation #4471",
			},
			keep: "order",
		},
		{
			name: "encoded security subject",
			fields: map[string]string{
				"From":    "Pay <service@pay.example>",
				"Subject": "=?utf-8?q?Your_verification_code?=",
			},
			keep: "security",
		},
		{
			name: "plain marketing",
			fields: map[string]string{
				"From":             "Shop <mail@shop.example>",
				"Subject":          "Half price today",
				"List-Unsubscribe": "<https://shop.example/u/1>",
			},
			keep:    "",
			subject: "Half price today",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := buildMessageT(1, "INBOX", hdr(1, tc.fields))
			if !ok {
				t.Fatal("buildMessage refused the header")
			}
			if m.Keep != tc.keep {
				t.Errorf("Keep = %q, want %q", m.Keep, tc.keep)
			}
			if m.Subject != tc.subject {
				t.Errorf("Subject = %q, want %q", m.Subject, tc.subject)
			}
		})
	}
}

// TestBuildMessageKeepMatcherIsConfigurable covers the two config levers:
// the extra keep_subjects patterns and the off switch.
func TestBuildMessageKeepMatcherIsConfigurable(t *testing.T) {
	fields := map[string]string{
		"From":    "Werk <rechnung@werk.example>",
		"Subject": "Ihre Rechnung fuer September",
	}
	extra, err := keep.New(true, []string{"Rechnung"})
	if err != nil {
		t.Fatalf("keep.New: %v", err)
	}
	m, _ := buildMessage(1, "INBOX", hdr(1, fields), extra)
	if m.Keep != "custom" {
		t.Errorf("Keep = %q, want custom", m.Keep)
	}

	off, err := keep.New(false, nil)
	if err != nil {
		t.Fatalf("keep.New: %v", err)
	}
	m, _ = buildMessage(1, "INBOX", hdr(1, map[string]string{
		"From":    "Pay <service@pay.example>",
		"Subject": "Your receipt from Acme",
	}), off)
	if m.Keep != "" {
		t.Errorf("Keep = %q with keep_transactional off, want empty", m.Keep)
	}
}

func TestMixedSenderGroups(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "mailshear.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	acct, err := st.UpsertAccount(ctx, "test", "imap.example.com", "user@example.com")
	if err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	bulk, _ := buildMessageT(acct.ID, "INBOX", hdr(1, map[string]string{
		"From":             "Shop <mail@shop.example>",
		"Subject":          "Sale",
		"List-Unsubscribe": "<https://shop.example/u/1>",
	}))
	receipt, _ := buildMessageT(acct.ID, "INBOX", hdr(2, map[string]string{
		"From":    "Shop <mail@shop.example>",
		"Subject": "Your order",
	}))
	if err := st.InsertMessages(ctx, []store.Message{bulk, receipt}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	seen := map[string]store.Sender{}
	noteSender(seen, bulk)
	noteSender(seen, receipt)
	if err := st.TouchSenders(ctx, acct.ID, seen); err != nil {
		t.Fatalf("touch senders: %v", err)
	}

	groups, err := st.SenderGroups(ctx, acct.ID)
	if err != nil {
		t.Fatalf("sender groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Count != 1 {
		t.Errorf("Count = %d, want 1", g.Count)
	}
	if g.MixedCount != 1 {
		t.Errorf("MixedCount = %d, want 1", g.MixedCount)
	}
	if g.SenderKey != "addr:mail@shop.example" {
		t.Errorf("SenderKey = %q", g.SenderKey)
	}
	if g.Method != "http" {
		t.Errorf("Method = %q", g.Method)
	}
}

func TestChunkHi(t *testing.T) {
	cases := []struct {
		lo, maxUID uint32
		size       int
		want       uint32
	}{
		{1, 1000, 500, 500},
		{501, 1000, 500, 1000},
		{1, 10, 500, 10},
		{1, 4294967295, 500, 500},
		{4294967000, 4294967295, 500, 4294967295},
		{1, 1000, 0, 500},
	}
	for _, c := range cases {
		if got := chunkHi(c.lo, c.maxUID, c.size); got != c.want {
			t.Errorf("chunkHi(%d,%d,%d) = %d, want %d", c.lo, c.maxUID, c.size, got, c.want)
		}
	}
}

func TestNeedsReset(t *testing.T) {
	cases := []struct {
		stored, current uint32
		want            bool
	}{
		{0, 12, false},
		{12, 12, false},
		{11, 12, true},
	}
	for _, c := range cases {
		if got := needsReset(c.stored, c.current); got != c.want {
			t.Errorf("needsReset(%d,%d) = %v, want %v", c.stored, c.current, got, c.want)
		}
	}
}

func TestWellKnownSkip(t *testing.T) {
	for _, name := range []string{"[Gmail]/Trash", "[Gmail]/Spam", "[Gmail]/Drafts", "[Gmail]/Sent Mail", "Trash", "Junk", "Spam", "Drafts", "Sent"} {
		if !wellKnownSkip[strings.ToLower(name)] {
			t.Errorf("%q should be skipped", name)
		}
	}
	for _, name := range []string{"INBOX", "[Gmail]/All Mail"} {
		if wellKnownSkip[strings.ToLower(name)] {
			t.Errorf("%q should not be skipped", name)
		}
	}
}

func TestOpenRange(t *testing.T) {
	cases := []struct {
		uidNext uint32
		want    bool
	}{
		{0, true},
		{1, false},
		{2, false},
		{4294967295, false},
	}
	for _, c := range cases {
		if got := openRange(c.uidNext); got != c.want {
			t.Errorf("openRange(%d) = %v, want %v", c.uidNext, got, c.want)
		}
	}
}

func TestBuildMessageRepeatedHeaders(t *testing.T) {
	h := hdr(20, map[string]string{"From": "alice@example.com"})
	h.Fields["List-Unsubscribe"] = []string{"<https://example.com/u/1>", "<mailto:leave@example.com>"}
	h.Fields["List-Unsubscribe-Post"] = []string{"", "List-Unsubscribe=One-Click"}
	h.Fields["List-Id"] = []string{"First <first.example.com>", "Second <second.example.com>"}

	m, ok := buildMessage(1, "INBOX", h, keep.Matcher{})
	if !ok {
		t.Fatal("expected message to be built")
	}
	want := []string{"https://example.com/u/1", "mailto:leave@example.com"}
	if !reflect.DeepEqual(m.UnsubURIs, want) {
		t.Errorf("UnsubURIs = %v, want %v", m.UnsubURIs, want)
	}
	if !m.OneClick || m.Method != "oneclick" {
		t.Errorf("OneClick=%v Method=%q, want one-click from the repeated post header", m.OneClick, m.Method)
	}
	if m.ListID != "first.example.com" {
		t.Errorf("ListID = %q, want the first value", m.ListID)
	}
}
