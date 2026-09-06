package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// hostile is a display name carrying the sequences that matter: clear the
// screen, rename the window, and then colour whatever follows.
const hostile = "\x1b[2J\x1b]0;pwned\x07Evil\x1b[31m Sender"

// sgrOnly matches the only escape sequences a rendered view is allowed to
// carry: the SGR colour and attribute codes lipgloss emits.
var sgrOnly = regexp.MustCompile(`^\x1b\[[0-9;:]*m`)

// assertNoRawEscapes fails when the rendered output carries an escape
// sequence that is not an SGR one, or any other control character. Styles are
// allowed; anything that came out of a mail header is not.
func assertNoRawEscapes(t *testing.T, what, out string) {
	t.Helper()
	for i := 0; i < len(out); i++ {
		c := out[i]
		if c == '\x1b' {
			if loc := sgrOnly.FindStringIndex(out[i:]); loc != nil {
				i += loc[1] - 1
				continue
			}
			t.Fatalf("%s: escape sequence that is not SGR at %d: %q", what, i, out[i:min(i+24, len(out))])
		}
		if c == '\n' || c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			t.Fatalf("%s: control byte %#x at %d", what, c, i)
		}
	}
}

func hostileGroup() store.SenderGroup {
	return store.SenderGroup{
		SenderKey: "addr:e@evil.example", DomainKey: "evil.example",
		Display: hostile, Address: "e\x1b[2J@evil.example", ListID: "list\x07.evil.example",
		Count: 10, LastSeen: time.Now(), FirstSeen: time.Now().Add(-time.Hour),
		Method:         "oneclick",
		LatestURIs:     []string{"https://evil.example/\x1b[2J"},
		RecentSubjects: []string{"sub\x1b]52;c;cGFzcw==\x07ject"},
		Folders:        []string{"[Gmail]/\x1b[31mAll Mail"},
		Labels:         []string{"lab\x1bel"},
		KeepCategories: []string{"re\x07ceipt"},
		Decision:       "unsubscribe\x1b[2J",
		UnsubStatus:    "ok\x07",
	}
}

// The review screen is where mail headers are shown in bulk, so it is the
// screen a sender would aim an escape sequence at.
func TestReviewViewNeverEmitsEscapesFromMail(t *testing.T) {
	m := newModel([]store.SenderGroup{hostileGroup()}, Options{Account: "personal", Embedded: true, ShowDecided: true})
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = mm.(model)
	// The detail pane is where the URIs, subjects and folders are shown.
	m.detail = true
	m.relayout()

	out := m.View().Content
	assertNoRawEscapes(t, "review", out)
	if !strings.Contains(out, "Evil Sender") {
		t.Fatalf("the cleaned display name is missing from the view:\n%s", out)
	}
	if strings.Contains(out, "pwned") {
		t.Fatalf("the OSC payload survived as text:\n%s", out)
	}
}

// The same model rendered with plain strings is the control: whatever escapes
// the hostile render carries, the plain one carries the same ones.
func TestHostileRenderMatchesThePlainRenderEscapes(t *testing.T) {
	plain := hostileGroup()
	plain.Display = "Evil Sender"
	plain.Address = "e@evil.example"
	plain.ListID = "list.evil.example"
	plain.LatestURIs = []string{"https://evil.example/"}
	plain.RecentSubjects = []string{"subject"}
	plain.Folders = []string{"[Gmail]/All Mail"}
	plain.Labels = []string{"label"}
	plain.KeepCategories = []string{"receipt"}
	plain.Decision = "unsubscribe"
	plain.UnsubStatus = "ok"

	render := func(g store.SenderGroup) string {
		m := newModel([]store.SenderGroup{g}, Options{Account: "personal", Embedded: true, ShowDecided: true})
		mm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = mm.(model)
		m.detail = true
		m.relayout()
		return m.View().Content
	}
	if got, want := escapes(render(hostileGroup())), escapes(render(plain)); got != want {
		t.Fatalf("hostile render escapes %q, plain render %q", got, want)
	}
}

// escapes joins every escape sequence in s, so two renders can be compared on
// their styling alone.
func escapes(s string) string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' {
			continue
		}
		j := i + 1
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
		if j < len(s) {
			j++
		}
		out = append(out, s[i:j])
		i = j - 1
	}
	return strings.Join(out, " ")
}

// The other screens show the same strings back from the audit log and the
// database, so each is checked as well.
func TestSideScreensNeverEmitEscapesFromMail(t *testing.T) {
	s := newStylesFor(true)

	results := newResultsModel(s, apply.Summary{
		RunID: "20260906-101010-ab\x1b[2Jcd", Moved: 3, AuditPath: "/tmp/a\x07udit.jsonl",
		FoldersSkipped: []string{"IN\x1b]0;x\x07BOX"},
		SkippedReasons: map[string]int{"pro\x1btected": 2},
	}, "Tra\x1b[31msh", []apply.Event{{
		Phase: apply.PhaseUnsubscribe, Display: hostile, Method: "http",
		Status: "manual", URI: "https://evil.example/\x1b[2J", Err: "boom\x07",
	}})
	results.detail = true
	assertNoRawEscapes(t, "results", strings.Join(results.view(120, 30), "\n"))
	results.detail = false
	assertNoRawEscapes(t, "results list", strings.Join(results.view(120, 30), "\n"))

	confirm := newConfirmModel(s, apply.Preview{
		RunID: "20260906-101010-ab\x07cd", Account: "per\x1b[2Jsonal",
		Trash: "Tra\x1bsh", Folders: []string{"IN\x07BOX"}, AuditPath: "/tmp/a\x1b]0;x\x07udit",
		Warnings: []string{"war\x1b[31mning"},
		Rows: []apply.PreviewRow{{
			Display: hostile, Method: "one\x07click", Unsubscribe: true,
			DeleteScope: "matched", Messages: 4, Bytes: 100,
		}},
		Refused: []apply.RefusedRow{{Display: hostile, Reason: "prot\x1bected"}},
	}, false, false)
	assertNoRawEscapes(t, "confirm", strings.Join(confirm.view(120, 30), "\n"))

	accounts := newAccountsModel(s, "/tmp/con\x07fig.yaml")
	accounts.loading = false
	accounts.notice = "not\x1b[2Jice"
	accounts.rows = []AccountStatus{{
		Provider: "Gm\x07ail", Auth: "OA\x1buth", Credential: "expires\x1b[31m soon",
	}}
	assertNoRawEscapes(t, "accounts", strings.Join(accounts.view(120, 30), "\n"))

	history := newHistoryModel(s)
	history.loading = false
	history.showing = true
	history.detail = RunDetail{
		RunInfo:   RunInfo{ID: "20260906-101010-abcd", Summary: "sum\x1b[2Jmary"},
		AuditPath: "/tmp/a\x07udit", Moved: 2,
		Unsub:   map[string]int{"ok": 1},
		Skipped: map[string]int{"prot\x1bected": 1},
		Manual:  []ManualLink{{Display: hostile, Status: "man\x07ual", URL: "https://evil.example/\x1b[2J"}},
	}
	assertNoRawEscapes(t, "history", strings.Join(history.view(120, 30), "\n"))
}

// The provider writes error_description, and it lands in the setup screen's
// error line; the OAuth package cleans it as the error is built.
func TestSetupErrorPanelNeverEmitsEscapes(t *testing.T) {
	m := setupAt("me@gmail.com", fEmail)
	m.err = fmt.Sprintf("oauth: %s", hostile)
	assertNoRawEscapes(t, "setup error", strings.Join(m.view(100, 24), "\n"))

	m.err = ""
	m.signingIn = true
	m.authURL = "https://login.example/authorize?state=abc"
	assertNoRawEscapes(t, "setup sign-in", strings.Join(m.view(100, 24), "\n"))
}
