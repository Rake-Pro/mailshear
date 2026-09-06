package tui

import (
	"fmt"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/text"
)

type confirmModel struct {
	styles  styles
	preview apply.Preview
	// noDelete and noUnsubscribe are the toggles that become ExecOptions.
	noDelete      bool
	noUnsubscribe bool

	cursor int
	offset int
	viewH  int
}

func newConfirmModel(s styles, pv apply.Preview, noDelete, noUnsubscribe bool) confirmModel {
	return confirmModel{styles: s, preview: pv, noDelete: noDelete, noUnsubscribe: noUnsubscribe}
}

// execOptions is what the toggles mean to apply.
func (m confirmModel) execOptions() apply.ExecOptions {
	return apply.ExecOptions{NoDelete: m.noDelete, NoUnsubscribe: m.noUnsubscribe}
}

// actionText names what would happen to one sender, honouring the toggles so
// the table always shows the run that is actually queued up.
func (m confirmModel) actionText(r apply.PreviewRow) (string, bool) {
	var parts []string
	if r.Unsubscribe && !m.noUnsubscribe {
		parts = append(parts, "unsub")
	}
	if r.DeleteScope != "" && !m.noDelete {
		switch r.DeleteScope {
		case "all":
			// Upper case because this is the escalation: everything from the
			// sender, header or not.
			parts = append(parts, "delete ALL")
		default:
			parts = append(parts, "delete matched")
		}
	}
	if len(parts) == 0 {
		return "nothing (toggled off)", false
	}
	return strings.Join(parts, " + "), true
}

func (m confirmModel) moveBy(n int) confirmModel {
	m.cursor += n
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor > len(m.preview.Rows)-1 {
		m.cursor = len(m.preview.Rows) - 1
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

// confirmCols lays the table out: sender takes what the fixed columns leave.
func confirmCols(width int) (sender, action, method, msgs, kept, size int) {
	action, method, msgs, kept, size = 22, 8, 7, 5, 9
	sender = width - 4 - action - method - msgs - kept - size - 5 // five single-space gaps
	if sender < 12 {
		sender = 12
	}
	return sender, action, method, msgs, kept, size
}

func (m confirmModel) view(width, height int) []string {
	s := m.styles
	pv := m.preview

	// The footer gets whatever is left over a minimum table of three inner
	// rows plus its two borders, so a short terminal loses detail rather
	// than pushing the table off the bottom.
	extra := m.footerLines(width, height-5)
	tableH := height - len(extra) - 2 // the panel's two border rows
	if tableH < 3 {
		tableH = 3
	}
	// The panel title takes one inner row and the column header another.
	m.viewH = tableH - 2

	sw, aw, mw, cw, kw, zw := confirmCols(width)
	head := strings.Join([]string{
		pad("SENDER", sw), pad("ACTION", aw), pad("METHOD", mw),
		alignCell("MSGS", cw, true), alignCell("KEPT", kw, true), alignCell("SIZE", zw, true),
	}, " ")

	lines := []string{s.header.Render(head)}
	for i := m.offset; i < len(pv.Rows) && len(lines) <= m.viewH; i++ {
		r := pv.Rows[i]
		action, live := m.actionText(r)
		msgs, kept, size := "", "", ""
		if r.DeleteScope != "" && !m.noDelete {
			msgs = itoa(r.Messages)
			size = text.HumanBytes(r.Bytes)
			switch {
			case r.IncludeKept:
				kept = "none"
			case r.Kept > 0:
				kept = itoa(r.Kept)
			}
		}
		display, method := cell(r.Display), cell(r.Method)
		plain := strings.Join([]string{
			pad(display, sw), pad(action, aw), pad(method, mw),
			alignCell(msgs, cw, true), alignCell(kept, kw, true), alignCell(size, zw, true),
		}, " ")

		switch {
		case i == m.cursor:
			lines = append(lines, s.selected.Render(pad(plain, width-4)))
		case !live:
			lines = append(lines, s.dim.Render(plain))
		default:
			keptCell := alignCell(kept, kw, true)
			if r.IncludeKept {
				keptCell = alignCell(s.err.Render(kept), kw, true)
			}
			colored := strings.Join([]string{
				pad(display, sw), pad(m.actionStyle(r, action), aw),
				alignCell(s.method(method), mw, false),
				alignCell(msgs, cw, true), keptCell, alignCell(size, zw, true),
			}, " ")
			lines = append(lines, colored)
		}
	}
	if len(pv.Rows) == 0 {
		lines = append(lines, s.dim.Render("nothing selected"))
	}

	out := strings.Split(s.panel(s.boxFocus, m.title(), lines, width, tableH), "\n")
	return append(out, extra...)
}

func (m confirmModel) title() string {
	return fmt.Sprintf("about to apply - run %s, account %s", cell(m.preview.RunID), cell(m.preview.Account))
}

func (m confirmModel) actionStyle(r apply.PreviewRow, action string) string {
	if r.DeleteScope == "all" && !m.noDelete {
		return m.styles.err.Render(action)
	}
	if r.DeleteScope != "" && !m.noDelete {
		return m.styles.warn.Render(action)
	}
	return m.styles.ok.Render(action)
}

// footerLines are the totals, the destination, the warnings and the refused
// senders: everything that must be read before pressing enter. budget caps
// how many rows they may take; past it the panels collapse to one line each
// so nothing is silently dropped.
func (m confirmModel) footerLines(width, budget int) []string {
	s := m.styles
	pv := m.preview
	var out []string

	unsub := pv.UnsubCount
	if m.noUnsubscribe {
		unsub = 0
	}
	del, msgs, bytes := pv.DeleteSenders, pv.Messages, pv.Bytes
	if m.noDelete {
		del, msgs, bytes = 0, 0, 0
	}
	kept := pv.Kept
	if m.noDelete {
		kept = 0
	}
	out = append(out, s.status.Render(truncate(fmt.Sprintf(
		"unsubscribe %d | delete %d sender(s), up to %d msgs, %s | kept %d | delete-all %d",
		unsub, del, msgs, text.HumanBytes(bytes), kept, pv.DeleteAllSenders), width)))
	if kept > 0 {
		out = append(out, s.subtle.Render(truncate(fmt.Sprintf(
			"kept: %d message(s) look transactional (receipts, orders, bookings, security) and stay put",
			kept), width)))
	}

	dest := cell(pv.Trash)
	if dest == "" {
		dest = "unresolved"
	}
	out = append(out, s.subtle.Render(truncate(fmt.Sprintf(
		"destination %s (moved, never expunged)   folders %s   audit %s",
		dest, joinOr(cells(pv.Folders), "none"), cell(pv.AuditPath)), width)))

	warnings := cells(pv.Warnings)
	if pv.TrashNote != "" {
		warnings = append(warnings, cell(pv.TrashNote))
	}
	if len(warnings) > 0 {
		panel := strings.Split(s.panel(s.boxWarn, "warnings", warnings, width, len(warnings)+1), "\n")
		if len(out)+len(panel) <= budget {
			out = append(out, panel...)
		} else {
			out = append(out, s.warn.Render(truncate(
				fmt.Sprintf("warning: %s", warnings[0]), width)))
		}
	}

	if n := len(pv.Refused); n > 0 {
		refused := make([]string, 0, n)
		for _, r := range pv.Refused {
			refused = append(refused, s.dim.Render(cell(r.Display)+" - "+cell(r.Reason)))
		}
		rows := min(len(refused), 3)
		panel := strings.Split(s.panel(s.box, fmt.Sprintf("refused (%d)", n), refused, width, rows+1), "\n")
		if len(out)+len(panel) <= budget {
			out = append(out, panel...)
		} else {
			out = append(out, s.dim.Render(truncate(
				fmt.Sprintf("refused %d sender(s): %s", n, cell(pv.Refused[0].Reason)), width)))
		}
	}
	return out
}

func (m confirmModel) help() string {
	del, unsubscribe := "on", "on"
	if m.noDelete {
		del = "off"
	}
	if m.noUnsubscribe {
		unsubscribe = "off"
	}
	return fmt.Sprintf("enter/y apply  d delete %s  u unsubscribe %s  up/down move  esc back  q quit", del, unsubscribe)
}
