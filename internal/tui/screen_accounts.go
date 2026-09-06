package tui

import (
	"fmt"
	"strings"
	"time"
)

// The accounts screen is the way in when more than one mailbox is
// configured, and the way to everything about an account afterwards: which
// one to work on, adding another, re-authenticating, signing out, and
// removing one from the config.

// accountsLoadedMsg carries the account list in from the backend.
type accountsLoadedMsg struct {
	rows []AccountStatus
	err  error
}

// accountsChangedMsg reports a sign-out or a removal, after which the list is
// read again.
type accountsChangedMsg struct {
	notice string
	err    error
}

// Pending confirmations on the accounts screen.
const (
	confirmSignOut = "signout"
	confirmRemove  = "remove"
	confirmPurge   = "purge"
)

type accountsModel struct {
	styles     styles
	configPath string
	// canReturn marks the screen as opened from review, where esc goes back
	// rather than quitting.
	canReturn bool

	rows   []AccountStatus
	cursor int
	offset int
	viewH  int

	loading bool
	err     string
	notice  string

	// confirm is the open y/n question, one of the confirm* constants.
	confirm string
}

func newAccountsModel(s styles, configPath string) accountsModel {
	return accountsModel{styles: s, configPath: configPath, loading: true}
}

func (m accountsModel) current() (AccountStatus, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return AccountStatus{}, false
	}
	return m.rows[m.cursor], true
}

func (m accountsModel) moveBy(n int) accountsModel {
	m.cursor += n
	if m.cursor > len(m.rows)-1 {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.viewH > 0 && m.cursor >= m.offset+m.viewH {
		m.offset = m.cursor - m.viewH + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	return m
}

// acctCol is one column of the accounts table. ADDRESS absorbs whatever the
// others leave; the rest are dropped, widest and least important first, until
// the address still has room to read.
type acctCol struct {
	id    int
	title string
	width int
	right bool
}

// Column ids, in screen order.
const (
	colName = iota
	colAddress
	colProvider
	colAuth
	colCredential
	colLastScan
	colMessages
	colStillSending
)

// dropOrder is which columns give way on a narrow terminal.
var dropOrder = []int{colProvider, colAuth, colLastScan}

// minAddress is how much of an address has to survive before another column
// is dropped instead.
const minAddress = 16

func accountColumns(width int) []acctCol {
	cols := []acctCol{
		{id: colName, title: "NAME", width: 10},
		{id: colAddress, title: "ADDRESS", width: minAddress},
		{id: colProvider, title: "PROVIDER", width: 10},
		{id: colAuth, title: "AUTH", width: 12},
		{id: colCredential, title: "CREDENTIAL", width: 16},
		{id: colLastScan, title: "LAST SCAN", width: 12},
		{id: colMessages, title: "MSGS", width: 6, right: true},
		{id: colStillSending, title: "STILL", width: 5, right: true},
	}

	spare := func() int {
		used := len(cols) - 1 // one space between each pair
		for _, c := range cols {
			if c.id != colAddress {
				used += c.width
			}
		}
		return width - 4 - used
	}
	for _, drop := range dropOrder {
		if spare() >= minAddress {
			break
		}
		for i, c := range cols {
			if c.id == drop {
				cols = append(cols[:i:i], cols[i+1:]...)
				break
			}
		}
	}

	w := spare()
	if w < 10 {
		w = 10
	}
	for i := range cols {
		if cols[i].id == colAddress {
			cols[i].width = w
		}
	}
	return cols
}

func (m accountsModel) view(width, height int) []string {
	s := m.styles

	panelH := height - 2 // the two border rows
	if panelH < 3 {
		panelH = 3
	}
	// The panel title takes one inner row and the column header another.
	m.viewH = panelH - 2

	var body []string
	switch {
	case m.loading:
		body = []string{s.subtle.Render("reading the accounts...")}
	case m.err != "":
		body = []string{s.err.Render(truncate(cell(m.err), width-4))}
	case len(m.rows) == 0:
		body = []string{
			s.subtle.Render("no accounts configured yet."),
			s.subtle.Render("press n to add one; it is written to " + cell(m.configPath)),
		}
	default:
		body = m.tableLines(width)
	}
	if m.notice != "" {
		body = append(body, "", s.subtle.Render(truncate(cell(m.notice), width-4)))
	}

	box := s.box
	if m.confirm != "" {
		box = s.boxWarn
	}
	return strings.Split(s.panel(box, "accounts", body, width, panelH), "\n")
}

func (m accountsModel) tableLines(width int) []string {
	s := m.styles
	cols := accountColumns(width)

	head := make([]string, 0, len(cols))
	for _, c := range cols {
		head = append(head, alignCell(c.title, c.width, c.right))
	}
	out := []string{s.header.Render(strings.Join(head, " "))}

	for i := m.offset; i < len(m.rows) && len(out) <= m.viewH; i++ {
		parts := make([]string, 0, len(cols))
		for _, col := range cols {
			parts = append(parts, alignCell(m.cellFor(m.rows[i], col.id), col.width, col.right))
		}
		line := strings.Join(parts, " ")
		if i == m.cursor {
			out = append(out, s.selected.Render(pad(line, width-4)))
			continue
		}
		if m.rows[i].Err != "" || strings.HasPrefix(m.rows[i].Credential, "missing") {
			out = append(out, s.warn.Render(line))
			continue
		}
		out = append(out, line)
	}
	return out
}

// cellFor is one column's text for one account, cleaned: the credential
// column can carry a provider's error text, which is not this program's.
func (m accountsModel) cellFor(r AccountStatus, id int) string {
	switch id {
	case colName:
		return cell(r.Account.Name)
	case colAddress:
		return cell(r.Account.Username)
	case colProvider:
		return cell(r.Provider)
	case colAuth:
		return cell(r.Auth)
	case colCredential:
		if r.Err != "" {
			return cell(r.Err)
		}
		return cell(r.Credential)
	case colLastScan:
		return scanAge(r.LastScan)
	case colMessages:
		if r.Messages == 0 {
			return ""
		}
		return itoa(r.Messages)
	default:
		if r.StillSending == 0 {
			return ""
		}
		return itoa(r.StillSending)
	}
}

// scanAge renders when a mailbox was last scanned, close enough for a table.
func scanAge(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}

func (m accountsModel) help() string {
	switch m.confirm {
	case confirmSignOut:
		name := ""
		if r, ok := m.current(); ok {
			name = cell(r.Account.Name)
		}
		return "delete the stored credential for " + name + "? the mailbox is untouched [y/n]"
	case confirmRemove:
		name := ""
		if r, ok := m.current(); ok {
			name = cell(r.Account.Name)
		}
		return "remove " + name + " from the config and delete its credential? [y/n]"
	case confirmPurge:
		return "also delete this account's scanned data (messages, decisions, runs)? [y/n]"
	}
	tail := "q quit"
	if m.canReturn {
		tail = "esc back  " + tail
	}
	return "enter use  n add  e edit/re-auth  x sign out  r remove  " + tail
}
