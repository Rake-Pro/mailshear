package tui

import (
	"fmt"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/text"
)

// The maintenance menu is where the housekeeping that used to be separate
// commands lives: dropping the cached mail data, starting a scan over,
// exporting the sender list, editing the protected senders, and saying which
// files this build is using.

// protectedLoadedMsg carries the protected sender list in from the backend.
type protectedLoadedMsg struct {
	entries []ProtectedEntry
	err     error
}

// maintenanceDoneMsg is the result of one maintenance action. rescan asks the
// flow to offer a scan, which is what a purge or a cursor reset leads to.
type maintenanceDoneMsg struct {
	notice string
	err    error
	rescan bool
}

// Menu actions, in the order they appear.
const (
	actPurge = iota
	actRescan
	actExport
	actProtected
	actConfigFile
	actPaths
	numMenuActions
)

var menuLabels = [numMenuActions]struct{ label, detail string }{
	{"purge cached messages", "drop stored subjects and addresses; sender decisions are kept"},
	{"rescan from scratch", "clear the folder cursors and read every message again"},
	{"export senders", "write the current sender list to CSV and JSON"},
	{"protected senders", "senders that are never unsubscribed or deleted"},
	{"open config file", "hand the config path to the system opener"},
	{"version and paths", "which build this is and where it keeps its files"},
}

// Sub-panels the menu can open in place of the action list.
const (
	panelProtected = "protected"
	panelPaths     = "paths"
)

type menuModel struct {
	styles styles

	cursor int
	// panel is the open sub-panel, one of the panel* constants; empty is the
	// action list.
	panel string
	// confirm is the action index awaiting a y/n answer, or -1.
	confirm int
	// rescanPrompt offers a scan after a purge or a cursor reset.
	rescanPrompt bool
	busy         bool

	notice string
	err    string

	protected []ProtectedEntry
	pCursor   int
	pOffset   int
	pViewH    int

	paths PathInfo
}

func newMenuModel(s styles) menuModel {
	return menuModel{styles: s, confirm: -1}
}

func (m menuModel) moveBy(n int) menuModel {
	if m.panel == panelProtected {
		m.pCursor += n
		if m.pCursor > len(m.protected)-1 {
			m.pCursor = len(m.protected) - 1
		}
		if m.pCursor < 0 {
			m.pCursor = 0
		}
		if m.pCursor < m.pOffset {
			m.pOffset = m.pCursor
		}
		if m.pViewH > 0 && m.pCursor >= m.pOffset+m.pViewH {
			m.pOffset = m.pCursor - m.pViewH + 1
		}
		if m.pOffset < 0 {
			m.pOffset = 0
		}
		return m
	}
	m.cursor = (m.cursor + n + numMenuActions) % numMenuActions
	return m
}

func (m menuModel) currentProtected() (ProtectedEntry, bool) {
	if m.pCursor < 0 || m.pCursor >= len(m.protected) {
		return ProtectedEntry{}, false
	}
	return m.protected[m.pCursor], true
}

func (m menuModel) view(width, height int) []string {
	s := m.styles

	panelH := height - 2
	if panelH < 3 {
		panelH = 3
	}
	m.pViewH = panelH - 2

	title := "maintenance"
	var body []string
	switch m.panel {
	case panelProtected:
		title = "protected senders"
		body = m.protectedLines(width)
	case panelPaths:
		title = "version and paths"
		body = m.pathLines()
	default:
		body = m.actionLines()
	}

	switch {
	case m.err != "":
		body = append(body, "", s.err.Render(truncate(cell(m.err), width-4)))
	case m.busy:
		body = append(body, "", s.subtle.Render("working..."))
	case m.notice != "":
		body = append(body, "", s.ok.Render(truncate(cell(m.notice), width-4)))
	}

	box := s.box
	if m.confirm >= 0 || m.rescanPrompt {
		box = s.boxWarn
	}
	return strings.Split(s.panel(box, title, body, width, panelH), "\n")
}

func (m menuModel) actionLines() []string {
	s := m.styles
	out := make([]string, 0, numMenuActions)
	for i, a := range menuLabels {
		line := "  " + pad(a.label, 24) + s.subtle.Render(a.detail)
		if i == m.cursor {
			line = s.accent.Render("> ") + s.bold.Render(pad(a.label, 24)) + s.subtle.Render(a.detail)
		}
		out = append(out, line)
	}
	return out
}

func (m menuModel) protectedLines(width int) []string {
	s := m.styles
	if len(m.protected) == 0 {
		return []string{s.subtle.Render("no protected senders; press p on a review row to add one")}
	}

	keyW := width - 4 - 16 - 1
	if keyW < 16 {
		keyW = 16
	}
	out := []string{s.header.Render(pad("ENTRY", keyW) + " " + pad("SOURCE", 16))}
	for i := m.pOffset; i < len(m.protected) && len(out) <= m.pViewH; i++ {
		e := m.protected[i]
		line := pad(cell(e.Key), keyW) + " " + pad(cell(e.Source), 16)
		switch {
		case i == m.pCursor:
			out = append(out, s.selected.Render(pad(line, width-4)))
		case e.Source == "store":
			out = append(out, line)
		default:
			out = append(out, s.dim.Render(line))
		}
	}
	return out
}

func (m menuModel) pathLines() []string {
	s := m.styles
	return []string{
		"version   " + nonEmpty(cell(m.paths.Version), "dev"),
		"config    " + cell(m.paths.ConfigPath),
		"data      " + cell(m.paths.DataDir),
		"database  " + cell(m.paths.DBPath) + s.subtle.Render("  "+text.HumanBytes(m.paths.DBSize)),
	}
}

func (m menuModel) help() string {
	if m.rescanPrompt {
		return "scan the mailbox now? [y/n]"
	}
	if m.confirm >= 0 {
		switch m.confirm {
		case actPurge:
			return "drop every cached message for this account? decisions are kept [y/n]"
		case actRescan:
			return "clear the folder cursors and read every message again? [y/n]"
		}
		return fmt.Sprintf("%s? [y/n]", menuLabels[m.confirm].label)
	}
	switch m.panel {
	case panelProtected:
		if e, ok := m.currentProtected(); ok && e.Source != "store" {
			return "config entry: edit the protect: list in config.yaml to change it  esc back"
		}
		return "x unprotect the selected sender  esc back  q quit"
	case panelPaths:
		return "esc back  q quit"
	}
	return "up/down move  enter run  esc back  q quit"
}
