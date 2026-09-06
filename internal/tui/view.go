package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Rake-Pro/mailshear/internal/text"
)

func itoa(n int) string { return strconv.Itoa(n) }

func dayString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// reserved rows around the table: the top bar, the column header, the two
// status rows, and the help footer. The status rows are never given up, so
// the pending counts cannot scroll off the bottom of the screen.
const reviewChrome = 1 + 1 + 2 + 1

// relayout recomputes the table window for the current terminal size.
func (m *model) relayout() {
	reserved := reviewChrome
	if m.filtering {
		reserved++
	}
	if m.detail {
		reserved += detailRow + 2 // panel content plus its two border rows
	}
	h := m.height - reserved
	if h < 3 {
		h = 3
	}
	m.viewH = h
	m.help.SetWidth(m.width)
	m.clampOffset()
}

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "mailshear"
	// Bracketed paste is on unless a view opts out; the filter reads the
	// tea.PasteMsg it produces.
	v.DisableBracketedPasteMode = false
	return v
}

func (m model) render() string {
	if m.width < minWidth || m.height < minHeight {
		return tooSmall(m.styles, m.width, m.height)
	}

	lines := []string{m.topBar()}
	if m.help.ShowAll {
		lines = append(lines, m.helpOverlay()...)
	} else {
		lines = append(lines, m.tableLines()...)
	}
	if m.filtering {
		lines = append(lines, m.styles.prompt.Render("filter: "+m.filterDraft+"_"))
	}
	if m.detail {
		lines = append(lines, strings.Split(m.detailPanel(), "\n")...)
	}
	lines = append(lines, m.styles.status.Render(m.statusLine()))
	lines = append(lines, m.noticeLine())
	lines = append(lines, m.styles.help.Render(m.help.View(m.keys)))

	return frame(lines, m.width, m.height)
}

// frame truncates every line to the terminal width and pads or cuts the whole
// screen to exactly height rows.
func frame(lines []string, width, height int) string {
	for i := range lines {
		lines[i] = truncate(lines[i], width)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func tooSmall(s styles, width, height int) string {
	msg := s.err.Render(fmt.Sprintf("terminal too small: need at least %dx%d, have %dx%d",
		minWidth, minHeight, width, height))
	if width < 1 || height < 1 {
		return msg
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, msg)
}

// tableLines renders the column header plus the visible window of rows. Only
// the rows on screen are rendered, so the list size does not affect the frame
// cost.
func (m model) tableLines() []string {
	cols := columns(m.width)
	out := make([]string, 0, m.viewH+1)

	head := make([]string, 0, len(cols))
	for _, c := range cols {
		head = append(head, alignCell(c.title, c.width, c.right))
	}
	out = append(out, m.styles.header.Render(strings.Join(head, " ")))

	if len(m.items) == 0 {
		out = append(out, m.styles.subtle.Render("no senders match; press / to change the filter, h to show decided rows"))
	}

	for i := m.offset; i < len(m.items) && len(out) <= m.viewH; i++ {
		out = append(out, m.renderRow(i, cols))
	}
	for len(out) < m.viewH+1 {
		out = append(out, "")
	}
	return out[:m.viewH+1]
}

// renderRow lays one row out. The row under the cursor is drawn plain under
// the selection background: nesting a foreground colour inside a background
// leaves the highlight broken at the first reset, so the selection replaces
// the per-column colours rather than fighting them.
func (m model) renderRow(i int, cols []colSpec) string {
	cells := m.rows[i]
	it := m.items[i]
	selected := i == m.cursor

	// A section header is a full-width band, not a table row: it introduces
	// the rows under it rather than lining up with them.
	if it.marker {
		line := truncate(cells[2], m.width)
		if selected {
			return m.styles.selected.Render(pad(line, m.width))
		}
		return m.styles.panelTitle.Render(line)
	}

	parts := make([]string, 0, len(cols))
	for c, col := range cols {
		text := alignCell(cells[c], col.width, col.right)
		if selected {
			parts = append(parts, text)
			continue
		}
		parts = append(parts, m.colorCell(c, cells[c], it, col))
	}
	line := strings.Join(parts, " ")

	switch {
	case selected:
		return m.styles.selected.Render(pad(line, m.width))
	case it.protected || it.decided:
		return m.styles.dim.Render(line)
	}
	return line
}

// colorCell paints one cell: the checkboxes, the method, and the flag glyphs
// each carry their own meaning, so each gets its own colour.
func (m model) colorCell(idx int, text string, it item, col colSpec) string {
	switch idx {
	case 0: // unsubscribe checkbox
		if text == "[x]" {
			return m.styles.ok.Render(text)
		}
		return m.styles.dim.Render(text)
	case 1: // delete checkbox
		switch {
		case strings.HasSuffix(strings.TrimSpace(text), "!]"):
			// The include-kept escalation: this delete covers receipts.
			return m.styles.err.Render(text)
		case strings.HasPrefix(text, "[D"):
			return m.styles.err.Render(text)
		case strings.HasPrefix(text, "[d"), text == "[~ ]":
			return m.styles.warn.Render(text)
		}
		return m.styles.dim.Render(text)
	case 2: // sender
		return alignCell(m.senderStyled(it), col.width, col.right)
	case 4: // last seen
		return m.styles.subtle.Render(alignCell(text, col.width, col.right))
	case 5: // method
		return alignCell(m.styles.method(text), col.width, col.right)
	case 6: // flags
		return m.flagCells(it)
	}
	return alignCell(text, col.width, col.right)
}

// senderStyled paints the sender column: on a domain row the brand reads as
// the name and the domain and sender count behind it are muted.
func (m model) senderStyled(it item) string {
	if it.brand == "" || it.senders <= 1 {
		return it.senderCell()
	}
	prefix := "  "
	if it.expandable() {
		prefix = "> "
	}
	return prefix + it.brand +
		m.styles.subtle.Render("  "+it.domain+fmt.Sprintf("  (%d senders)", it.senders))
}

// flagCells colours each flag glyph on its own: M mixed sender, F flagged
// mail present, R replied to, U already unsubscribed, S still sending.
func (m model) flagCells(it item) string {
	glyphs := flagString(it)
	styles := []lipgloss.Style{
		m.styles.warn, // M
		m.styles.info, // F
		m.styles.info, // R
		m.styles.ok,   // U
		m.styles.err,  // S
	}
	var b strings.Builder
	for i, r := range glyphs {
		if r == ' ' {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(styles[i].Render(string(r)))
	}
	return b.String()
}

func alignCell(text string, width int, right bool) string {
	text = truncate(text, width)
	n := width - lipgloss.Width(text)
	if n <= 0 {
		return text
	}
	if right {
		return strings.Repeat(" ", n) + text
	}
	return text + strings.Repeat(" ", n)
}

// topBar is the persistent context line: what this is, whose mailbox, how
// many rows, and the active sort, grouping and filter as chips.
func (m model) topBar() string {
	rows := 0
	for _, it := range m.items {
		if !it.marker {
			rows++
		}
	}
	group := "sender"
	if m.domainMode {
		group = "brand"
	}

	left := m.styles.title.Render("mailshear review") + m.styles.subtle.Render("  "+cell(m.opts.Account))
	if v := m.opts.Version; v != "" && v != "dev" {
		left += m.styles.dim.Render("  " + v)
	}
	chips := []string{
		fmt.Sprintf("%d rows", rows),
		"sort " + m.sorting.String(),
		"group " + group,
	}
	if m.hidden > 0 {
		chips = append(chips, fmt.Sprintf("%d decided hidden", m.hidden))
	}
	if m.filterText != "" {
		chips = append(chips, "filter "+strconv.Quote(m.filterText))
	}
	right := m.styles.chip.Render(strings.Join(chips, m.styles.subtle.Render(" | ")))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncate(left+"  "+right, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m model) helpOverlay() []string {
	out := strings.Split(m.help.View(m.keys), "\n")
	head := []string{m.styles.panelTitle.Render("keys"), ""}
	out = append(head, out...)
	for len(out) < m.viewH+1 {
		out = append(out, "")
	}
	return out[:m.viewH+1]
}

// totals sums what the current selections would do. delete_all supersedes
// delete_matched on the same sender, so it is counted once.
// totals sums what the current selections would do. kept is what the
// transactional rule holds back, subtracted from msgs unless the row waived
// it; it counts the bulk messages only, so for a delete-all row it is a lower
// bound.
type reviewTotals struct {
	unsub  int
	del    int
	msgs   int
	kept   int
	manual int
	bytes  int64
	// mixedUnknown reports that a delete-all row has messages whose size was
	// never recorded.
	mixedUnknown bool
}

func (m model) totals() reviewTotals {
	var t reviewTotals
	for _, g := range m.groups {
		s, ok := m.sel[g.SenderKey]
		if !ok || !s.any() {
			continue
		}
		if s.unsubscribe {
			t.unsub++
			switch g.Method {
			case "oneclick", "http":
			default:
				t.manual++
			}
		}
		if !s.deletes() {
			continue
		}
		t.del++
		t.bytes += g.TotalSize
		n := g.Count
		if s.deleteAll {
			n += g.MixedCount
			if g.MixedCount > 0 {
				t.mixedUnknown = true
			}
		}
		if !s.includeKept && g.KeepCount > 0 {
			n -= g.KeepCount
			t.kept += g.KeepCount
		}
		if n < 0 {
			n = 0
		}
		t.msgs += n
	}
	return t
}

func (m model) statusLine() string {
	t := m.totals()
	size := text.HumanBytes(t.bytes)
	if t.mixedUnknown {
		size += " + unknown for mixed"
	}
	return fmt.Sprintf("unsubscribe %d | delete %d | to trash %d msgs, %s | kept %d | manual %d",
		t.unsub, t.del, t.msgs, size, t.kept, t.manual)
}

func (m model) noticeLine() string {
	switch {
	case m.confirming && m.pendingField == fieldIncludeKept:
		return m.styles.err.Render(
			"delete this sender's receipts, bills and security mail too? they are kept by default [y/n]")
	case m.confirming:
		return m.styles.err.Render(
			"delete ALL mail from this sender, header or not? this is not undone by unsubscribe [y/n]")
	case m.confirmQuit:
		return m.styles.err.Render("quit without applying? your selections are lost [y/n]")
	case m.notice != "":
		return m.styles.notice.Render(cell(m.notice))
	}
	return ""
}

func (m model) detailPanel() string {
	return m.styles.panel(m.styles.box, m.detailTitle(), m.detailLines(), m.width, detailRow)
}

func (m model) detailTitle() string {
	it, ok := m.current()
	if !ok {
		if i := m.cursor; i >= 0 && i < len(m.items) && m.items[i].marker {
			return m.items[i].display
		}
		return "detail"
	}
	if it.brand != "" && it.senders > 1 {
		return fmt.Sprintf("%s  %s  (%d senders)", it.brand, it.domain, it.senders)
	}
	who := it.display
	if it.address != "" && it.address != it.display {
		who += " <" + it.address + ">"
	}
	if it.listID != "" {
		who += " list-id " + it.listID
	}
	return who
}

func (m model) detailLines() []string {
	it, ok := m.current()
	if !ok {
		if i := m.cursor; i >= 0 && i < len(m.items) && m.items[i].marker {
			return []string{sectionBlurb(m.items[i].section)}
		}
		return []string{"no row selected"}
	}
	lines := []string{
		fmt.Sprintf("messages %d  mixed %d  kept %d  flagged %d  replied %d  size %s",
			it.count, it.mixed, it.keepCount, it.flagged, it.replied, text.HumanBytes(it.size)),
		fmt.Sprintf("method %s  one-click %v  first %s  last %s",
			it.method, it.oneClick, dayString(it.firstSeen), dayString(it.lastSeen)),
		"keep " + joinOr(it.keepCategories, "nothing transactional"),
		"uris " + joinOr(it.uris, "none"),
		"folders " + joinOr(it.folders, "none"),
		"subjects " + joinOr(it.subjects, "none"),
		decisionHistory(it),
	}
	if len(it.children) > 1 {
		lines = append(lines[:1], append([]string{m.memberBreakdown(it)}, lines[1:]...)...)
	}
	return lines
}

// memberBreakdown is the one-line "what is actually behind this brand row"
// summary: each member sender and its message count.
func (m model) memberBreakdown(it item) string {
	parts := make([]string, 0, len(it.children))
	for _, c := range it.children {
		parts = append(parts, fmt.Sprintf("%s (%d)", c.display, c.count))
	}
	return "senders " + strings.Join(parts, ", ")
}

func sectionBlurb(s sectionID) string {
	switch s {
	case sectionProtected:
		return "protected senders: nothing here is ever unsubscribed or deleted"
	case sectionKeep:
		return "these senders mix receipts, bills or security mail into their bulk mail; that mail is never deleted unless you press K"
	case sectionStillSending:
		return "unsubscribed before, still sending: escalate with D or a provider-side filter"
	default:
		return "ordinary bulk senders"
	}
}

func decisionHistory(it item) string {
	if it.decision == "" && it.unsubStatus == "" {
		return "no previous decision"
	}
	parts := []string{}
	if it.decision != "" {
		s := "decided " + it.decision
		if !it.decidedAt.IsZero() {
			s += " on " + dayString(it.decidedAt)
		}
		parts = append(parts, s)
	}
	if it.unsubStatus != "" {
		s := "unsubscribe " + it.unsubStatus
		if !it.unsubAt.IsZero() {
			s += " on " + dayString(it.unsubAt)
		}
		parts = append(parts, s)
	}
	if it.stillSending {
		parts = append(parts, fmt.Sprintf("still sending (%d)", it.stillSendingCount))
	}
	return strings.Join(parts, "; ")
}

func joinOr(v []string, empty string) string {
	if len(v) == 0 {
		return empty
	}
	return strings.Join(v, ", ")
}
