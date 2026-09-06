package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Rake-Pro/mailshear/internal/store"
)

var (
	now    = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	longGo = now.Add(-90 * 24 * time.Hour)
)

func testGroups() []store.SenderGroup {
	return []store.SenderGroup{
		{
			SenderKey: "addr:news@example.com", DomainKey: "example.com",
			Display: "Example News", Address: "news@example.com",
			Count: 100, MixedCount: 4, TotalSize: 5_000_000,
			FirstSeen: longGo, LastSeen: now.Add(-time.Hour),
			Method: "oneclick", LatestURIs: []string{"https://example.com/u/news"},
			LatestOneClick: true, RecentSubjects: []string{"Weekly digest"},
			Folders: []string{"INBOX"},
		},
		{
			SenderKey: "addr:deals@example.com", DomainKey: "example.com",
			Display: "Example Deals", Address: "deals@example.com",
			Count: 50, FlaggedCount: 1, TotalSize: 1_000_000,
			FirstSeen: longGo, LastSeen: now.Add(-2 * time.Hour),
			Method: "http", LatestURIs: []string{"https://example.com/u/deals"},
			RecentSubjects: []string{"Half price today"},
		},
		{
			SenderKey: "addr:alerts@bank.example", DomainKey: "bank.example",
			Display: "Bank Alerts", Address: "alerts@bank.example",
			Count: 10, TotalSize: 20_000,
			FirstSeen: longGo, LastSeen: now.Add(-3 * time.Hour),
			Method: "none",
		},
		{
			SenderKey: "addr:old@old.example", DomainKey: "old.example",
			Display: "Old Sender", Address: "old@old.example",
			Count: 5, TotalSize: 10_000,
			FirstSeen: longGo, LastSeen: now.Add(-40 * 24 * time.Hour),
			Method: "http", Decision: "unsubscribe",
			DecidedAt: now.Add(-30 * 24 * time.Hour), UnsubStatus: "ok",
		},
		{
			SenderKey: "addr:spam@spam.example", DomainKey: "spam.example",
			Display: "Persistent", Address: "spam@spam.example",
			Count: 20, TotalSize: 40_000,
			FirstSeen: longGo, LastSeen: now.Add(-30 * time.Minute),
			Method: "http", Decision: "unsubscribe",
			DecidedAt: now.Add(-30 * 24 * time.Hour), UnsubStatus: "ok",
			StillSending: true,
		},
		// Three List-Ids and subdomains, one brand: the shape the first live
		// run turned up for LinkedIn.
		{
			SenderKey: "list:jobs.linkedin.com", DomainKey: "linkedin.com",
			Display: "LinkedIn", Address: "jobs@e.linkedin.com", ListID: "jobs.linkedin.com",
			Count: 30, TotalSize: 300_000,
			FirstSeen: longGo, LastSeen: now.Add(-4 * time.Hour),
			Method: "oneclick", LatestURIs: []string{"https://linkedin.com/u/jobs"},
			LatestOneClick: true, RecentSubjects: []string{"Jobs you may like"},
		},
		{
			SenderKey: "list:news.linkedin.com", DomainKey: "linkedin.com",
			Display: "LinkedIn", Address: "news@e.linkedin.com", ListID: "news.linkedin.com",
			Count: 20, TotalSize: 200_000,
			FirstSeen: longGo, LastSeen: now.Add(-5 * time.Hour),
			Method: "oneclick", LatestURIs: []string{"https://linkedin.com/u/news"},
			LatestOneClick: true,
		},
		{
			SenderKey: "addr:invites@linkedin.com", DomainKey: "linkedin.com",
			Display: "LinkedIn Invitations", Address: "invites@linkedin.com",
			Count: 5, TotalSize: 50_000,
			FirstSeen: longGo, LastSeen: now.Add(-6 * time.Hour),
			Method: "http",
		},
		// The PayPal shape: marketing with List-Unsubscribe, receipts without.
		{
			SenderKey: "addr:service@pay.example", DomainKey: "pay.example",
			Display: "PayPal", Address: "service@pay.example",
			Count: 40, MixedCount: 12, KeepCount: 9,
			KeepCategories: []string{"receipt", "security"},
			TotalSize:      400_000,
			FirstSeen:      longGo, LastSeen: now.Add(-7 * time.Hour),
			Method: "http", LatestURIs: []string{"https://pay.example/u"},
			RecentSubjects: []string{"Your receipt from Acme"},
		},
	}
}

func testOptions() Options {
	return Options{
		Account:       "personal",
		ProtectedKeys: map[string]bool{"addr:alerts@bank.example": true},
	}
}

func senderOptions() Options {
	o := testOptions()
	o.SenderMode = true
	return o
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "f2":
		return tea.KeyPressMsg{Code: tea.KeyF2}
	case "f3":
		return tea.KeyPressMsg{Code: tea.KeyF3}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic("unsupported test key: " + s)
	}
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func start(t *testing.T, opts Options) model {
	t.Helper()
	var m tea.Model = newModel(testGroups(), opts)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m.(model)
}

func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	var tm tea.Model = m
	for _, k := range keys {
		tm, _ = tm.Update(keyMsg(k))
	}
	return tm.(model)
}

// rowKeys renders the row list the way the screen orders it: section headers
// as <Section>, member rows indented with a dot.
func rowKeys(m model) []string {
	out := []string{}
	for _, it := range m.items {
		switch {
		case it.marker:
			out = append(out, "<"+it.section.String()+">")
		case it.child:
			out = append(out, "."+it.key)
		default:
			out = append(out, it.key)
		}
	}
	return out
}

func rowsEqual(t *testing.T, m model, want ...string) {
	t.Helper()
	got := rowKeys(m)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// moveToKey walks the cursor onto the row with the given key.
func moveToKey(t *testing.T, m model, key string) model {
	t.Helper()
	for i, it := range m.items {
		if !it.marker && it.key == key {
			m.moveTo(i)
			return m
		}
	}
	t.Fatalf("no row %q in %v", key, rowKeys(m))
	return m
}

func cursorKey(t *testing.T, m model) string {
	t.Helper()
	it, ok := m.current()
	if !ok {
		t.Fatalf("no selectable row under the cursor (rows %v, cursor %d)", rowKeys(m), m.cursor)
	}
	return it.key
}

func TestDefaultGroupingIsBrandAndRowsAreSectioned(t *testing.T) {
	m := start(t, testOptions())
	if !m.domainMode {
		t.Fatal("review should open grouped by domain (brand)")
	}
	rowsEqual(t, m,
		"<Protected>", "bank.example",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com")
	if m.hidden != 1 {
		t.Fatalf("expected 1 hidden decided row, got %d", m.hidden)
	}
	if k := cursorKey(t, m); k != "bank.example" {
		t.Fatalf("cursor started on %s, want the first row of the first section", k)
	}

	// The brand row names the sender, not the domain.
	m = moveToKey(t, m, "linkedin.com")
	it, _ := m.current()
	if it.brand != "LinkedIn" || it.senders != 3 {
		t.Fatalf("brand row = %q with %d senders", it.brand, it.senders)
	}
	if cell := m.rows[m.cursor][2]; !strings.Contains(cell, "LinkedIn") ||
		!strings.Contains(cell, "linkedin.com") || !strings.Contains(cell, "(3 senders)") {
		t.Fatalf("brand cell = %q", cell)
	}

	// g falls back to one row per sender key.
	m = press(t, m, "g")
	if m.domainMode {
		t.Fatal("g did not switch to sender grouping")
	}
	if got := strings.Join(rowKeys(m), ","); !strings.Contains(got, "list:jobs.linkedin.com") {
		t.Fatalf("sender rows = %v", rowKeys(m))
	}
}

func TestExpandAndCollapseDomainRow(t *testing.T) {
	m := start(t, testOptions())
	m = moveToKey(t, m, "linkedin.com")
	m = press(t, m, "enter")
	rowsEqual(t, m,
		"<Protected>", "bank.example",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com",
		".list:jobs.linkedin.com", ".list:news.linkedin.com", ".addr:invites@linkedin.com")
	if k := cursorKey(t, m); k != "linkedin.com" {
		t.Fatalf("expanding moved the cursor to %s", k)
	}

	m = press(t, m, "enter")
	rowsEqual(t, m,
		"<Protected>", "bank.example",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com")

	// The detail pane still works for a domain row and names its members.
	m = press(t, m, "enter") // expand
	m = press(t, m, "down")  // first member
	m = press(t, m, "enter") // a member row has nothing to expand: detail
	if !m.detail {
		t.Fatal("enter on a member row did not open the detail pane")
	}
	m = moveToKey(t, m, "linkedin.com")
	body := strings.Join(m.detailLines(), "\n")
	if !strings.Contains(body, "LinkedIn (30)") || !strings.Contains(body, "LinkedIn Invitations (5)") {
		t.Fatalf("domain detail pane has no member breakdown:\n%s", body)
	}
}

func TestSectionHeaderCollapses(t *testing.T) {
	m := start(t, testOptions())
	m.moveTo(0) // the Protected header
	m = press(t, m, "enter")
	rowsEqual(t, m,
		"<Protected>",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com")
	if m.cursor != 0 {
		t.Fatalf("cursor left the header it collapsed: %d", m.cursor)
	}
	if cell := m.rows[0][2]; !strings.HasPrefix(cell, "> ") {
		t.Fatalf("collapsed header cell = %q", cell)
	}
	m = press(t, m, "enter")
	rowsEqual(t, m,
		"<Protected>", "bank.example",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com")
}

func TestDomainDecisionsReachMembersAndPartialMarker(t *testing.T) {
	m := start(t, testOptions())
	m = moveToKey(t, m, "linkedin.com")
	m = press(t, m, "space", "d")
	for _, k := range []string{"list:jobs.linkedin.com", "list:news.linkedin.com", "addr:invites@linkedin.com"} {
		s := m.sel[k]
		if !s.unsubscribe || !s.deleteMatched {
			t.Fatalf("brand decision did not reach member %s: %+v", k, s)
		}
	}
	if row := m.rows[m.cursor]; row[0] != "[x]" || row[1] != "[d ]" {
		t.Fatalf("brand row checkboxes = %v", row)
	}

	// Clear the row, then decide on one member only: the parent goes partial.
	m = press(t, m, "space", "d", "enter", "down", "space")
	if k := cursorKey(t, m); k != "list:jobs.linkedin.com" {
		t.Fatalf("cursor on %s, want the first member", k)
	}
	if !m.sel["list:jobs.linkedin.com"].unsubscribe {
		t.Fatal("space on a member row did nothing")
	}
	if s := m.sel["list:news.linkedin.com"]; s.any() {
		t.Fatalf("a member decision leaked to a sibling: %+v", s)
	}
	m = moveToKey(t, m, "linkedin.com")
	if row := m.rows[m.cursor]; row[0] != "[~]" {
		t.Fatalf("parent row should be partial, got %v", row)
	}
}

func TestBulkKeysAreSectionScoped(t *testing.T) {
	m := start(t, testOptions())
	m = moveToKey(t, m, "example.com")
	m = press(t, m, "a")
	for _, k := range []string{"addr:news@example.com", "list:jobs.linkedin.com"} {
		if !m.sel[k].unsubscribe {
			t.Fatalf("a did not reach %s in the Bulk section", k)
		}
	}
	for _, k := range []string{"addr:service@pay.example", "addr:spam@spam.example", "addr:alerts@bank.example"} {
		if m.sel[k].any() {
			t.Fatalf("a reached %s outside the Bulk section", k)
		}
	}
	if !strings.Contains(m.notice, "Bulk") {
		t.Fatalf("notice should name the section, got %q", m.notice)
	}

	// A on the keep section touches only that section.
	m = moveToKey(t, m, "pay.example")
	m = press(t, m, "A")
	if !m.sel["addr:service@pay.example"].deleteMatched {
		t.Fatal("A did not reach the keep section row")
	}
	if m.sel["addr:news@example.com"].deleteMatched {
		t.Fatal("A on the keep section reached the bulk section")
	}

	// x clears only the section under the cursor.
	m = press(t, m, "x")
	if m.sel["addr:service@pay.example"].any() {
		t.Fatal("x did not clear the keep section")
	}
	if !m.sel["addr:news@example.com"].unsubscribe {
		t.Fatal("x cleared a section it was not in")
	}
}

func TestIncludeKeptEscalation(t *testing.T) {
	m := start(t, testOptions())
	m = moveToKey(t, m, "pay.example")

	// K alone means nothing: it changes what a delete covers.
	m = press(t, m, "K")
	if m.confirming {
		t.Fatal("K without a delete opened the confirmation")
	}
	if !strings.Contains(m.notice, "d or D first") {
		t.Fatalf("notice = %q", m.notice)
	}

	m = press(t, m, "d", "K")
	if !m.confirming || m.pendingField != fieldIncludeKept {
		t.Fatalf("K did not open the include-kept prompt (%v, %q)", m.confirming, m.pendingField)
	}
	if m.sel["addr:service@pay.example"].includeKept {
		t.Fatal("include-kept applied before confirmation")
	}
	m = press(t, m, "n")
	if m.confirming || m.sel["addr:service@pay.example"].includeKept {
		t.Fatal("n should cancel the escalation")
	}

	m = press(t, m, "K", "y")
	if !m.sel["addr:service@pay.example"].includeKept || !m.includeKeptAcked {
		t.Fatal("y should apply include-kept and remember the acknowledgement")
	}
	if row := m.rows[m.cursor][1]; row != "[d!]" {
		t.Fatalf("delete cell = %q, want [d!]", row)
	}

	// Escalating the delete keeps the bang.
	m = press(t, m, "D", "y")
	if row := m.rows[m.cursor][1]; row != "[D!]" {
		t.Fatalf("delete cell = %q, want [D!]", row)
	}

	// A second K toggles straight off, no prompt.
	m = press(t, m, "K")
	if m.confirming {
		t.Fatal("include-kept prompted twice in one session")
	}
	if m.sel["addr:service@pay.example"].includeKept {
		t.Fatal("K did not toggle include-kept back off")
	}
	if row := m.rows[m.cursor][1]; row != "[D ]" {
		t.Fatalf("delete cell = %q, want [D ]", row)
	}
}

func TestIncludeKeptRefusedOnProtectedRow(t *testing.T) {
	m := start(t, testOptions())
	m = moveToKey(t, m, "bank.example")
	m = press(t, m, "K")
	if m.confirming {
		t.Fatal("K opened a prompt on a protected row")
	}
	if m.sel["addr:alerts@bank.example"].any() {
		t.Fatal("K selected a protected row")
	}
	if !strings.Contains(m.notice, "protected") {
		t.Fatalf("notice = %q", m.notice)
	}
	if row := m.rows[m.cursor]; row[0] != "[-]" || row[1] != "[- ]" {
		t.Fatalf("protected row should render as locked, got %v", row)
	}
}

func TestToggleUnsubscribeAndDelete(t *testing.T) {
	m := start(t, senderOptions())
	m = moveToKey(t, m, "addr:news@example.com")

	m = press(t, m, "space")
	if !m.sel["addr:news@example.com"].unsubscribe {
		t.Fatal("space did not set unsubscribe")
	}
	m = press(t, m, "d")
	if !m.sel["addr:news@example.com"].deleteMatched {
		t.Fatal("d did not set delete matched")
	}
	if m.rows[m.cursor][0] != "[x]" || m.rows[m.cursor][1] != "[d ]" {
		t.Fatalf("row checkboxes not rendered: %v", m.rows[m.cursor])
	}

	m = press(t, m, "space", "d")
	if s := m.sel["addr:news@example.com"]; s.any() {
		t.Fatalf("toggling twice should clear the selection, got %+v", s)
	}
}

func TestDeleteAllPromptsOncePerSession(t *testing.T) {
	m := start(t, senderOptions())
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "D")
	if !m.confirming || m.pendingField != fieldDeleteAll {
		t.Fatal("expected a confirmation prompt on the first delete-all")
	}
	if m.sel["addr:news@example.com"].deleteAll {
		t.Fatal("delete-all applied before confirmation")
	}
	m = press(t, m, "n")
	if m.confirming || m.sel["addr:news@example.com"].deleteAll {
		t.Fatal("n should cancel the escalation")
	}

	m = press(t, m, "D", "y")
	if !m.sel["addr:news@example.com"].deleteAll || !m.deleteAllAcked {
		t.Fatal("y should apply delete-all and remember the acknowledgement")
	}
	m = moveToKey(t, m, "addr:deals@example.com")
	m = press(t, m, "D")
	if m.confirming {
		t.Fatal("second delete-all should not prompt again")
	}
	if !m.sel["addr:deals@example.com"].deleteAll {
		t.Fatal("second delete-all did not apply")
	}
}

func TestProtectedRowRefusesEverySelectionKey(t *testing.T) {
	m := start(t, testOptions())
	m = moveToKey(t, m, "bank.example")

	for _, k := range []string{"space", "d", "D"} {
		m = press(t, m, k)
		if s := m.sel["addr:alerts@bank.example"]; s.any() {
			t.Fatalf("%s was accepted on a protected row: %+v", k, s)
		}
		if m.notice == "" {
			t.Fatalf("%s on a protected row produced no notice", k)
		}
		if m.confirming {
			t.Fatalf("%s on a protected row opened the delete-all prompt", k)
		}
	}

	// Bulk selection skips protected rows too.
	m = press(t, m, "a", "A")
	if s := m.sel["addr:alerts@bank.example"]; s.any() {
		t.Fatalf("bulk selection touched a protected row: %+v", s)
	}
}

func TestProtectClearsSelectionsAndReports(t *testing.T) {
	m := start(t, senderOptions())
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "space", "d")
	if !m.sel["addr:news@example.com"].any() {
		t.Fatal("setup: expected selections")
	}

	m = press(t, m, "p")
	if m.sel["addr:news@example.com"].any() {
		t.Fatal("protect should clear the row's selections")
	}
	if len(m.newlyProtected) != 1 || m.newlyProtected[0] != "addr:news@example.com" {
		t.Fatalf("newlyProtected = %v", m.newlyProtected)
	}
	if !m.protected["addr:news@example.com"] {
		t.Fatal("row not marked protected")
	}

	// The row moved into the protected section; find it again.
	m = moveToKey(t, m, "addr:news@example.com")
	before := len(m.newlyProtected)
	m = press(t, m, "p")
	if len(m.newlyProtected) != before {
		t.Fatalf("protecting twice should be a no-op, got %v", m.newlyProtected)
	}
	if m.notice != "already protected" {
		t.Fatalf("notice = %q", m.notice)
	}

	m = press(t, m, "q")
	res := m.result()
	if res.Written {
		t.Fatal("q should not write")
	}
	if len(res.NewlyProtected) != 1 {
		t.Fatalf("newly protected must survive a quit, got %v", res.NewlyProtected)
	}
}

func TestFilterNarrowsRowsAndMatchesMembers(t *testing.T) {
	m := start(t, testOptions())
	m = press(t, m, "/", "d", "e", "a", "l", "s", "enter")
	if m.filtering {
		t.Fatal("enter should close the filter")
	}
	rowsEqual(t, m, "<Bulk>", "example.com")

	// A filter that only matches one member still keeps the brand row.
	m = press(t, m, "/", "backspace", "backspace", "backspace", "backspace", "backspace",
		"i", "n", "v", "i", "t", "e", "s", "enter")
	rowsEqual(t, m, "<Bulk>", "linkedin.com")

	m = press(t, m, "/", "ctrl+u", "enter")
	if len(rowKeys(m)) != 9 {
		t.Fatalf("clearing the filter should restore every row, got %v", rowKeys(m))
	}

	m = press(t, m, "/", "b", "a", "n", "k", "esc")
	if m.filterText != "" {
		t.Fatalf("esc should abandon the draft, filter = %q", m.filterText)
	}
	if len(rowKeys(m)) != 9 {
		t.Fatalf("esc should restore every row, got %v", rowKeys(m))
	}
}

func TestSortAppliesWithinSections(t *testing.T) {
	m := start(t, testOptions())
	if m.sorting != sortCount {
		t.Fatalf("default sort = %v", m.sorting)
	}
	want := []sortMode{sortLastSeen, sortSize, sortSender, sortCount}
	for _, w := range want {
		m = press(t, m, "s")
		if m.sorting != w {
			t.Fatalf("after s: sorting = %v, want %v", m.sorting, w)
		}
	}

	// Sender order: the sections stay put, the rows inside them reorder.
	m = press(t, m, "s", "s", "s") // last seen, size, sender
	rowsEqual(t, m,
		"<Protected>", "bank.example",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com")

	m = press(t, m, "s", "s") // count, last seen
	rowsEqual(t, m,
		"<Protected>", "bank.example",
		"<Keep: receipts and security>", "pay.example",
		"<Still sending>", "spam.example",
		"<Bulk>", "example.com", "linkedin.com")
}

func TestShowDecidedToggle(t *testing.T) {
	m := start(t, testOptions())
	if len(rowKeys(m)) != 9 {
		t.Fatalf("setup rows = %v", rowKeys(m))
	}
	m = press(t, m, "h")
	if !m.showDecided {
		t.Fatal("h did not toggle decided rows on")
	}
	if got := strings.Join(rowKeys(m), ","); !strings.Contains(got, "old.example") {
		t.Fatalf("h should reveal the decided row, got %v", rowKeys(m))
	}
	if m.hidden != 0 {
		t.Fatalf("hidden = %d with decided rows shown", m.hidden)
	}

	m = press(t, m, "h")
	if len(rowKeys(m)) != 9 {
		t.Fatalf("h should hide the decided row again, got %v", rowKeys(m))
	}

	opts := testOptions()
	opts.ShowDecided = true
	m2 := start(t, opts)
	if len(rowKeys(m2)) != 10 {
		t.Fatalf("ShowDecided option ignored, rows = %v", rowKeys(m2))
	}
}

func TestWriteReturnsDecisions(t *testing.T) {
	m := start(t, senderOptions())
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "space")
	m = moveToKey(t, m, "addr:deals@example.com")
	m = press(t, m, "d")
	m = moveToKey(t, m, "addr:service@pay.example")
	m = press(t, m, "d", "K", "y")
	m = press(t, m, "w")

	if !m.quitting {
		t.Fatal("w should quit")
	}
	res := m.result()
	if !res.Written {
		t.Fatal("w should mark the result written")
	}

	want := map[string]store.Decision{
		"addr:news@example.com":    {SenderKey: "addr:news@example.com", Unsubscribe: true},
		"addr:deals@example.com":   {SenderKey: "addr:deals@example.com", DeleteMatched: true},
		"addr:service@pay.example": {SenderKey: "addr:service@pay.example", DeleteMatched: true, IncludeKept: true},
	}
	if len(res.Decisions) != len(want) {
		t.Fatalf("decisions = %+v", res.Decisions)
	}
	for _, d := range res.Decisions {
		w, ok := want[d.SenderKey]
		if !ok || d != w {
			t.Fatalf("decision %+v, want %+v", d, w)
		}
	}
}

func TestQuitWritesNothing(t *testing.T) {
	m := start(t, senderOptions())
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "space", "q")
	res := m.result()
	if res.Written {
		t.Fatal("q should not write a plan")
	}
	if len(res.Decisions) != 0 {
		t.Fatalf("q should return no decisions, got %+v", res.Decisions)
	}
}

func TestProtectedByConfig(t *testing.T) {
	opts := senderOptions()
	opts.ProtectedByConfig = func(g store.SenderGroup) bool { return g.DomainKey == "example.com" }
	m := start(t, opts)
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "space")
	if m.sel["addr:news@example.com"].any() {
		t.Fatal("config-protected row accepted a selection")
	}
}

func TestDetailPaneAndOpenURL(t *testing.T) {
	opened := ""
	opts := senderOptions()
	opts.OpenURL = func(u string) error { opened = u; return nil }

	m := start(t, opts)
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "enter")
	if !m.detail {
		t.Fatal("enter did not open the detail pane")
	}
	body := m.detailTitle() + "\n" + strings.Join(m.detailLines(), "\n")
	for _, want := range []string{"Example News", "messages 100", "oneclick", "https://example.com/u/news", "INBOX"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail pane missing %q:\n%s", want, body)
		}
	}
	m = press(t, m, "esc")
	if m.detail {
		t.Fatal("esc did not close the detail pane")
	}

	var tm tea.Model = m
	tm, cmd := tm.Update(keyMsg("o"))
	if cmd == nil {
		t.Fatal("o produced no command")
	}
	msg := cmd()
	if opened != "https://example.com/u/news" {
		t.Fatalf("opened %q", opened)
	}
	tm, _ = tm.Update(msg)
	if !strings.Contains(tm.(model).notice, "opened") {
		t.Fatalf("notice = %q", tm.(model).notice)
	}
}

func TestStatusLineTotalsExcludeKept(t *testing.T) {
	m := start(t, senderOptions())
	m = moveToKey(t, m, "addr:news@example.com")
	m = press(t, m, "space", "d")
	line := m.statusLine()
	if !strings.Contains(line, "unsubscribe 1") || !strings.Contains(line, "delete 1") {
		t.Fatalf("status = %q", line)
	}
	if !strings.Contains(line, "100 msgs") {
		t.Fatalf("status should count matched messages only: %q", line)
	}
	if !strings.Contains(line, "kept 0") {
		t.Fatalf("status should carry a kept count: %q", line)
	}

	m = press(t, m, "D", "y")
	line = m.statusLine()
	if !strings.Contains(line, "104 msgs") {
		t.Fatalf("delete-all should include the mixed messages: %q", line)
	}
	if !strings.Contains(line, "unknown for mixed") {
		t.Fatalf("delete-all should flag the unknown mixed bytes: %q", line)
	}

	// PayPal: 40 bulk messages, 9 of them receipts, so 31 would move.
	m = moveToKey(t, m, "addr:service@pay.example")
	m = press(t, m, "d")
	line = m.statusLine()
	if !strings.Contains(line, "135 msgs") {
		t.Fatalf("kept messages should be excluded from the count: %q", line)
	}
	if !strings.Contains(line, "kept 9") {
		t.Fatalf("kept total = %q", line)
	}

	// K puts them back in.
	m = press(t, m, "K", "y")
	line = m.statusLine()
	if !strings.Contains(line, "144 msgs") || !strings.Contains(line, "kept 0") {
		t.Fatalf("include-kept should restore the kept messages: %q", line)
	}
}

func TestViewRendersAtEveryStateAndSize(t *testing.T) {
	sizes := []struct{ w, h int }{{70, 16}, {200, 50}, {40, 8}}
	for _, size := range sizes {
		var tm tea.Model = newModel(testGroups(), testOptions())
		tm, _ = tm.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		for _, keys := range [][]string{
			{},
			{"end", "enter"},      // expand the brand row
			{"down", "down", "d"}, // decide on a member
			{"home", "enter"},     // collapse a section
			{"g"},
			{"/", "e"},
			{"D"},
			{"d", "K"},
			{"h", "s", "s"},
			{"end"},
			{"?"},
			{"?", "space", "q"},
		} {
			for _, k := range keys {
				tm, _ = tm.Update(keyMsg(k))
			}
			out := tm.(model).View()
			if out.Content == "" {
				t.Fatalf("%dx%d: empty view", size.w, size.h)
			}
			if !out.AltScreen {
				t.Fatalf("%dx%d: view should use the alt screen", size.w, size.h)
			}
			if size.w < minWidth || size.h < minHeight {
				if !strings.Contains(out.Content, "terminal too small") {
					t.Fatalf("%dx%d: expected the too-small guard", size.w, size.h)
				}
				continue
			}
			lines := strings.Split(out.Content, "\n")
			if len(lines) != size.h {
				t.Fatalf("%dx%d: rendered %d lines", size.w, size.h, len(lines))
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > size.w {
					t.Fatalf("%dx%d: line %d is %d cells wide", size.w, size.h, i, w)
				}
			}
		}
	}
}

// TestSectionsShowInTheRenderedTable checks the headers actually reach the
// screen, since they are the shape of the whole review now.
func TestSectionsShowInTheRenderedTable(t *testing.T) {
	m := start(t, testOptions())
	out := m.View().Content
	for _, want := range []string{"Protected (1)", "Keep: receipts and security (1)", "Still sending (1)", "Bulk (2)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered table has no %q section header:\n%s", want, out)
		}
	}
}
