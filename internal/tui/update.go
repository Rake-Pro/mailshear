package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type openedMsg struct {
	uri string
	err error
}

// reviewDoneMsg leaves the review screen: written reports that the user asked
// to go on to the plan rather than quitting.
type reviewDoneMsg struct{ written bool }

// accountMsg asks the flow for the accounts screen: switch mailbox, add one,
// re-run an OAuth sign-in, sign out, or remove one from the config.
type accountMsg struct{}

// historyMsg asks the flow for the run history, and menuMsg for the
// maintenance actions.
type historyMsg struct{}

type menuMsg struct{}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.relayout()
		return m, nil

	case openedMsg:
		if msg.err != nil {
			m.notice = "could not open " + msg.uri + ": " + msg.err.Error()
		} else {
			m.notice = "opened " + msg.uri
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		// The filter is the only text field on this screen.
		if m.filtering {
			m.filterDraft += sanitizeInput(msg.Content, false)
			m.filterText = m.filterDraft
			m.rebuild()
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		return m.handleFilterKey(msg)
	}
	if m.confirming {
		return m.handleConfirmKey(msg)
	}
	if m.confirmQuit {
		return m.handleQuitKey(msg)
	}

	m.notice = ""
	switch {
	case key.Matches(msg, m.keys.quit):
		// Only guard the exit when there is something to lose.
		if len(m.sel) > 0 {
			m.confirmQuit = true
			return m, nil
		}
		return m.leave(false)

	case key.Matches(msg, m.keys.suspend):
		return m, tea.Suspend

	case key.Matches(msg, m.keys.write):
		return m.leave(true)

	case key.Matches(msg, m.keys.account):
		// Standalone review has no other screen to hand over to.
		if m.opts.Embedded {
			return m, func() tea.Msg { return accountMsg{} }
		}

	case key.Matches(msg, m.keys.history):
		if m.opts.Embedded {
			return m, func() tea.Msg { return historyMsg{} }
		}

	case key.Matches(msg, m.keys.menu):
		if m.opts.Embedded {
			return m, func() tea.Msg { return menuMsg{} }
		}

	case key.Matches(msg, m.keys.help):
		m.help.ShowAll = !m.help.ShowAll
		m.relayout()

	case key.Matches(msg, m.keys.up):
		m.moveBy(-1)
	case key.Matches(msg, m.keys.down):
		m.moveBy(1)
	case key.Matches(msg, m.keys.pageUp):
		m.moveBy(-m.pageStep())
	case key.Matches(msg, m.keys.pageDown):
		m.moveBy(m.pageStep())
	case key.Matches(msg, m.keys.top):
		m.moveTo(0)
		m.skipMarker(1)
	case key.Matches(msg, m.keys.bottom):
		m.moveTo(len(m.items) - 1)
		m.skipMarker(-1)

	case key.Matches(msg, m.keys.unsubscribe):
		m.toggle(func(s *selection) { s.unsubscribe = !s.unsubscribe }, false)
	case key.Matches(msg, m.keys.deleteMatch):
		m.toggle(func(s *selection) { s.deleteMatched = !s.deleteMatched }, false)
	case key.Matches(msg, m.keys.deleteAll):
		return m.startDeleteAll()
	case key.Matches(msg, m.keys.includeKept):
		return m.startIncludeKept()

	case key.Matches(msg, m.keys.protect):
		m.protectCurrent()

	case key.Matches(msg, m.keys.allUnsub):
		m.bulk(func(s *selection) { s.unsubscribe = true }, "marked every row in %s for unsubscribe")
	case key.Matches(msg, m.keys.allDelete):
		m.bulk(func(s *selection) { s.deleteMatched = true }, "marked every row in %s for delete")
	case key.Matches(msg, m.keys.clear):
		m.bulk(func(s *selection) { *s = selection{} }, "cleared selections on every row in %s")

	case key.Matches(msg, m.keys.filter):
		m.filtering = true
		m.filterDraft = m.filterText
		m.filterSaved = m.filterText
		m.relayout()
		return m, nil

	case key.Matches(msg, m.keys.sort):
		m.sorting = (m.sorting + 1) % 4
		m.rebuild()
		m.notice = "sorted by " + m.sorting.String()

	case key.Matches(msg, m.keys.group):
		m.domainMode = !m.domainMode
		m.expanded = map[string]bool{}
		m.rebuild()
		if m.domainMode {
			m.notice = "grouping by domain (brand)"
		} else {
			m.notice = "grouping by sender"
		}

	case key.Matches(msg, m.keys.detail):
		return m.enter()

	case key.Matches(msg, m.keys.showDecided):
		m.showDecided = !m.showDecided
		m.rebuild()
		if m.showDecided {
			m.notice = "showing previously decided rows"
		} else {
			m.notice = "hiding previously decided rows"
		}

	case key.Matches(msg, m.keys.open):
		return m.openCurrent()

	case key.Matches(msg, m.keys.cancel):
		if m.detail {
			m.detail = false
			m.relayout()
		}
	}
	return m, nil
}

// handleFilterKey runs the one-line filter field. It is deliberately minimal:
// printable runes, backspace, ctrl+u, enter and esc.
func (m model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.filterDraft = m.filterSaved
		m.filterText = m.filterSaved
		m.rebuild()
		m.relayout()
		return m, nil
	case "enter":
		m.filtering = false
		m.filterText = m.filterDraft
		m.rebuild()
		m.relayout()
		return m, nil
	case "backspace":
		r := []rune(m.filterDraft)
		if len(r) > 0 {
			m.filterDraft = string(r[:len(r)-1])
		}
	case "ctrl+u":
		m.filterDraft = ""
	default:
		// Text is a whole burst of runes on terminals that deliver a paste
		// as one key press rather than a bracketed one.
		m.filterDraft += sanitizeInput(msg.Text, false)
	}
	m.filterText = m.filterDraft
	m.rebuild()
	return m, nil
}

// leave ends the review. Standalone (the review screen on its own) quits;
// embedded in the full flow it hands control back with a message so the flow
// can move to the confirm screen without tearing the terminal down.
func (m model) leave(written bool) (tea.Model, tea.Cmd) {
	m.written = written
	m.confirmQuit = false
	if m.opts.Embedded {
		return m, func() tea.Msg { return reviewDoneMsg{written: written} }
	}
	m.quitting = true
	return m, tea.Quit
}

func (m model) handleQuitKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "ctrl+c":
		return m.leave(false)
	case "n", "N", "esc", "q":
		m.confirmQuit = false
	}
	return m, nil
}

func (m model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		field := m.pendingField
		for _, k := range m.pendingKeys {
			s := m.sel[k]
			if field == fieldIncludeKept {
				s.includeKept = m.pendingValue
			} else {
				s.deleteAll = m.pendingValue
			}
			if s.any() {
				m.sel[k] = s
			} else {
				delete(m.sel, k)
			}
		}
		if field == fieldIncludeKept {
			m.includeKeptAcked = true
			m.notice = "include-kept enabled for this session"
		} else {
			m.deleteAllAcked = true
			m.notice = "delete-all enabled for this session"
		}
		m.confirming = false
		m.pendingKeys = nil
		m.pendingField = ""
		m.rebuild()
	case "n", "N", "esc":
		if m.pendingField == fieldIncludeKept {
			m.notice = "include-kept cancelled"
		} else {
			m.notice = "delete all cancelled"
		}
		m.confirming = false
		m.pendingKeys = nil
		m.pendingField = ""
	}
	return m, nil
}

// The two escalations that ask once per session before they take effect.
const (
	fieldDeleteAll   = "delete_all"
	fieldIncludeKept = "include_kept"
)

// startDeleteAll asks for confirmation the first time delete-all is used in a
// session; after that the key toggles directly.
func (m model) startDeleteAll() (tea.Model, tea.Cmd) {
	it, ok := m.current()
	if !ok {
		return m, nil
	}
	if it.protected {
		m.notice = "protected sender: delete all is never allowed"
		return m, nil
	}
	if m.deleteAllAcked {
		m.toggle(func(s *selection) { s.deleteAll = !s.deleteAll }, true)
		return m, nil
	}
	m.confirming = true
	m.pendingField = fieldDeleteAll
	m.pendingKeys = it.members
	m.pendingValue = !m.selectionOf(it).deleteAll
	return m, nil
}

// startIncludeKept waives the transactional-mail rule for one row. It only
// means anything alongside a delete, it is never allowed on a protected
// sender, and like delete-all it asks once per session.
func (m model) startIncludeKept() (tea.Model, tea.Cmd) {
	it, ok := m.current()
	if !ok {
		return m, nil
	}
	if it.protected {
		m.notice = "protected sender: nothing is deleted from it, kept or not"
		return m, nil
	}
	if !m.selectionAnyDelete(it) {
		m.notice = "press d or D first: include-kept only changes what a delete covers"
		return m, nil
	}
	if m.includeKeptAcked {
		m.setSelection(it, func(s *selection) { s.includeKept = !s.includeKept })
		m.rebuild()
		return m, nil
	}
	m.confirming = true
	m.pendingField = fieldIncludeKept
	m.pendingKeys = it.members
	m.pendingValue = !m.selectionOf(it).includeKept
	return m, nil
}

// selectionAnyDelete reports whether any member of the row is marked for a
// delete, which is what makes include-kept meaningful.
func (m *model) selectionAnyDelete(it item) bool {
	for _, k := range it.members {
		if m.sel[k].deletes() {
			return true
		}
	}
	return false
}

// enter is the drill-in key: it collapses a section from its header line,
// expands a domain row into its members, and otherwise toggles the detail
// pane.
func (m model) enter() (tea.Model, tea.Cmd) {
	i := m.cursor
	if i >= 0 && i < len(m.items) && m.items[i].marker {
		s := m.items[i].section
		m.collapsed[s] = !m.collapsed[s]
		m.rebuild()
		if m.collapsed[s] {
			m.notice = "collapsed " + s.String()
		} else {
			m.notice = "expanded " + s.String()
		}
		return m, nil
	}
	if it, ok := m.current(); ok && it.expandable() {
		m.expanded[it.key] = !m.expanded[it.key]
		m.rebuild()
		if m.expanded[it.key] {
			m.notice = "showing the senders behind " + it.label()
		} else {
			m.notice = "collapsed " + it.label()
		}
		return m, nil
	}
	m.detail = !m.detail
	m.relayout()
	return m, nil
}

func (m *model) toggle(apply func(*selection), isDeleteAll bool) {
	it, ok := m.current()
	if !ok {
		return
	}
	if it.protected {
		if isDeleteAll {
			m.notice = "protected sender: delete all is never allowed"
		} else {
			m.notice = "protected sender: unprotect it in the config or database first"
		}
		return
	}
	m.setSelection(it, apply)
	m.rebuild()
}

// bulk applies to the section the cursor is in, and only that one: a, A and x
// are section-scoped so "select everything" cannot reach across the keep or
// protected sections by accident. Member rows are skipped because their
// parent already covers them.
func (m *model) bulk(apply func(*selection), noticeFormat string) {
	section, ok := m.currentSection()
	if !ok {
		return
	}
	for _, it := range m.items {
		if it.marker || it.child || it.protected || it.section != section {
			continue
		}
		m.setSelection(it, apply)
	}
	m.rebuild()
	m.notice = fmt.Sprintf(noticeFormat, section.String())
}

func (m *model) protectCurrent() {
	it, ok := m.current()
	if !ok {
		return
	}
	if it.protected {
		m.notice = "already protected"
		return
	}
	for _, k := range it.members {
		m.protected[k] = true
		delete(m.sel, k)
		m.newlyProtected = append(m.newlyProtected, k)
	}
	m.rebuild()
	m.notice = "protected " + it.display + "; selections cleared"
}

func (m model) openCurrent() (tea.Model, tea.Cmd) {
	it, ok := m.current()
	if !ok {
		return m, nil
	}
	if len(it.uris) == 0 {
		m.notice = "no unsubscribe URI recorded for this sender"
		return m, nil
	}
	if m.opts.OpenURL == nil {
		m.notice = it.uris[0]
		return m, nil
	}
	uri := it.uris[0]
	open := m.opts.OpenURL
	return m, func() tea.Msg {
		return openedMsg{uri: uri, err: open(uri)}
	}
}

func (m model) pageStep() int {
	if m.viewH < 1 {
		return 1
	}
	return m.viewH
}
