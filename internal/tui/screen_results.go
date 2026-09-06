package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/text"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// manualItem is one sender the run could not finish on its own: no method at
// all, or an HTTP link that probably landed on a page needing another click.
type manualItem struct {
	display string
	method  string
	status  string
	// url is the best link to hand the browser: the page the request ended
	// on, falling back to the URI from the header.
	url  string
	uris []string
	err  string
}

type resultsModel struct {
	styles styles
	sum    apply.Summary
	trash  string

	manual []manualItem
	cursor int
	offset int
	viewH  int

	detail bool

	// undoing and undone drive the undo confirmation and its outcome.
	confirmUndo bool
	undoing     bool
	undoNote    string
}

// undoDoneMsg reports the outcome of undoing this run.
type undoDoneMsg struct {
	moved   int
	missing int
	err     error
}

// manualFrom picks the senders that still need a person out of the streamed
// unsubscribe results: manual outright, probable (a GET that may or may not
// have finished the job), and outright failures.
func manualFrom(events []apply.Event) []manualItem {
	out := make([]manualItem, 0, len(events))
	for _, e := range events {
		switch unsub.Status(e.Status) {
		case unsub.StatusManual, unsub.StatusProbable, unsub.StatusFailed:
		default:
			continue
		}
		url := e.FinalURL
		if url == "" {
			url = e.URI
		}
		var uris []string
		if e.URI != "" {
			uris = append(uris, e.URI)
		}
		if e.FinalURL != "" && e.FinalURL != e.URI {
			uris = append(uris, e.FinalURL)
		}
		out = append(out, manualItem{
			display: cell(e.Display), method: cell(e.Method), status: cell(e.Status),
			url: cell(url), uris: cells(uris), err: cell(e.Err),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return statusRank(out[i].status) < statusRank(out[j].status)
	})
	return out
}

// statusRank sorts the follow-up list by how much attention each row needs.
func statusRank(s string) int {
	switch unsub.Status(s) {
	case unsub.StatusFailed:
		return 0
	case unsub.StatusManual:
		return 1
	case unsub.StatusProbable:
		return 2
	default:
		return 3
	}
}

func newResultsModel(s styles, sum apply.Summary, trash string, events []apply.Event) resultsModel {
	return resultsModel{styles: s, sum: sum, trash: trash, manual: manualFrom(events)}
}

func (m resultsModel) current() (manualItem, bool) {
	if m.cursor < 0 || m.cursor >= len(m.manual) {
		return manualItem{}, false
	}
	return m.manual[m.cursor], true
}

func (m resultsModel) moveBy(n int) resultsModel {
	m.cursor += n
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor > len(m.manual)-1 {
		m.cursor = len(m.manual) - 1
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

func (m resultsModel) summaryLines(width int) []string {
	s := m.styles
	sum := m.sum

	var parts []string
	for _, st := range []unsub.Status{unsub.StatusOK, unsub.StatusProbable, unsub.StatusManual, unsub.StatusFailed} {
		if n := sum.Unsub[st]; n > 0 {
			parts = append(parts, s.unsubStatus(string(st))+" "+itoa(n))
		}
	}
	unsubLine := "unsubscribe: " + s.dim.Render("nothing attempted")
	if len(parts) > 0 {
		unsubLine = "unsubscribe: " + strings.Join(parts, "  ")
	}

	dest := cell(m.trash)
	if dest == "" {
		dest = "Trash"
	}
	out := []string{
		unsubLine,
		fmt.Sprintf("moved %s message(s) to %s", s.ok.Render(itoa(sum.Moved)), dest),
	}
	if sum.SkippedSenders > 0 || sum.SkippedMessages > 0 {
		out = append(out, s.subtle.Render(fmt.Sprintf("skipped %d sender(s), %d message(s): %s",
			sum.SkippedSenders, sum.SkippedMessages, cell(text.JoinCounts(sum.SkippedReasons)))))
	}
	for _, f := range sum.FoldersSkipped {
		out = append(out, s.warn.Render("folder skipped: "+cell(f)))
	}
	if sum.AuditPath != "" {
		out = append(out, s.subtle.Render(truncate("audit "+cell(sum.AuditPath), width-4)))
	}
	if m.undoNote != "" {
		out = append(out, s.ok.Render(m.undoNote))
	}
	return out
}

func (m resultsModel) view(width, height int) []string {
	s := m.styles

	summary := m.summaryLines(width)
	sumH := len(summary) + 1 // the panel title takes one inner row
	if sumH > height-6 {
		sumH = height - 6
	}
	if sumH < 2 {
		sumH = 2
	}
	out := strings.Split(s.panel(s.box, "run "+cell(m.sum.RunID), summary, width, sumH), "\n")

	listH := height - len(out) - 2
	if listH < 3 {
		listH = 3
	}
	// The panel title takes one inner row and the column header another.
	m.viewH = listH - 2

	if m.detail {
		return append(out, strings.Split(s.panel(s.box, "detail", m.detailLines(), width, listH), "\n")...)
	}
	return append(out, strings.Split(
		s.panel(s.box, fmt.Sprintf("manual follow-up (%d)", len(m.manual)), m.manualLines(width), width, listH), "\n")...)
}

func (m resultsModel) manualLines(width int) []string {
	s := m.styles
	if len(m.manual) == 0 {
		return []string{s.ok.Render("nothing left to do by hand")}
	}

	nameW, statusW := 24, 9
	urlW := width - 4 - nameW - statusW - 2
	if urlW < 12 {
		urlW = 12
		nameW = 16
	}

	head := s.header.Render(pad("SENDER", nameW) + " " + pad("STATUS", statusW) + " " + pad("LINK", urlW))
	out := []string{head}
	for i := m.offset; i < len(m.manual) && len(out) <= m.viewH; i++ {
		it := m.manual[i]
		link := it.url
		if link == "" {
			link = it.err
		}
		plain := pad(it.display, nameW) + " " + pad(it.status, statusW) + " " + pad(link, urlW)
		if i == m.cursor {
			out = append(out, s.selected.Render(pad(plain, width-4)))
			continue
		}
		out = append(out, pad(it.display, nameW)+" "+
			pad(s.unsubStatus(it.status), statusW)+" "+
			s.info.Render(pad(link, urlW)))
	}
	return out
}

func (m resultsModel) detailLines() []string {
	it, ok := m.current()
	if !ok {
		return []string{m.styles.dim.Render("no sender selected")}
	}
	out := []string{
		it.display,
		fmt.Sprintf("method %s   status %s", it.method, it.status),
	}
	if it.err != "" {
		out = append(out, "error "+it.err)
	}
	if len(it.uris) == 0 {
		out = append(out, m.styles.dim.Render("no unsubscribe URI was recorded"))
	}
	for _, u := range it.uris {
		out = append(out, m.styles.info.Render(u))
	}
	return out
}

func (m resultsModel) help() string {
	switch {
	case m.confirmUndo:
		return "undo this run and move the mail back out of Trash? [y/n]"
	case m.undoing:
		return "undoing..."
	}
	return "o open  c copy  enter detail  u undo  h history  r review again  q quit"
}
