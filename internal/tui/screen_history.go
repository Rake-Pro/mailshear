package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// The history screen lists previous apply runs and reads one back out of its
// audit log, which is the record of what a run actually did. Undo from here
// is the same operation the results screen offers, pointed at an older run.

// runsLoadedMsg carries the run list in from the backend.
type runsLoadedMsg struct {
	runs []RunInfo
	err  error
}

// runDetailMsg carries one run's audit summary in.
type runDetailMsg struct {
	detail RunDetail
	err    error
}

type historyModel struct {
	styles styles

	runs   []RunInfo
	cursor int
	offset int
	viewH  int

	// detail is the run being read; showing reports that the detail panel
	// rather than the list is on screen.
	detail  RunDetail
	showing bool

	loading bool
	err     string
	note    string

	confirmUndo bool
	undoing     bool
}

func newHistoryModel(s styles) historyModel {
	return historyModel{styles: s, loading: true}
}

func (m historyModel) current() (RunInfo, bool) {
	if m.cursor < 0 || m.cursor >= len(m.runs) {
		return RunInfo{}, false
	}
	return m.runs[m.cursor], true
}

func (m historyModel) moveBy(n int) historyModel {
	m.cursor += n
	if m.cursor > len(m.runs)-1 {
		m.cursor = len(m.runs) - 1
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

func (m historyModel) view(width, height int) []string {
	s := m.styles

	panelH := height - 2
	if panelH < 3 {
		panelH = 3
	}
	m.viewH = panelH - 2

	title := "runs"
	var body []string
	switch {
	case m.loading:
		body = []string{s.subtle.Render("reading the run history...")}
	case m.err != "":
		body = []string{s.err.Render(truncate(cell(m.err), width-4))}
	case m.showing:
		title = "run " + cell(m.detail.ID)
		body = m.detailLines(width)
	case len(m.runs) == 0:
		body = []string{s.subtle.Render("no runs recorded for this account yet")}
	default:
		body = m.listLines(width)
	}
	if m.note != "" {
		body = append(body, "", s.ok.Render(truncate(cell(m.note), width-4)))
	}

	box := s.box
	if m.confirmUndo {
		box = s.boxWarn
	}
	return strings.Split(s.panel(box, title, body, width, panelH), "\n")
}

func (m historyModel) listLines(width int) []string {
	s := m.styles

	idW, startedW := 20, 20
	sumW := width - 4 - idW - startedW - 2
	if sumW < 12 {
		sumW = 12
	}
	out := []string{s.header.Render(pad("RUN", idW) + " " + pad("STARTED", startedW) + " " + pad("SUMMARY", sumW))}

	for i := m.offset; i < len(m.runs) && len(out) <= m.viewH; i++ {
		r := m.runs[i]
		started := "-"
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.Local().Format("2006-01-02 15:04")
		}
		summary := cell(r.Summary)
		if summary == "" {
			summary = "did not finish"
		}
		line := pad(cell(r.ID), idW) + " " + pad(started, startedW) + " " + pad(summary, sumW)
		if i == m.cursor {
			out = append(out, s.selected.Render(pad(line, width-4)))
			continue
		}
		out = append(out, line)
	}
	return out
}

func (m historyModel) detailLines(width int) []string {
	s := m.styles
	d := m.detail
	if d.Err != "" {
		return []string{
			s.err.Render(truncate(cell(d.Err), width-4)),
			s.subtle.Render("the audit log is what undo reads; without it a run cannot be undone"),
		}
	}

	started := "-"
	if !d.StartedAt.IsZero() {
		started = d.StartedAt.Local().Format("2006-01-02 15:04")
	}
	out := []string{
		fmt.Sprintf("started %s", started),
		fmt.Sprintf("moved %s message(s) to Trash", s.ok.Render(itoa(d.Moved))),
	}
	if len(d.Unsub) > 0 {
		parts := make([]string, 0, len(d.Unsub))
		for _, k := range unsubStatusOrder(d.Unsub) {
			parts = append(parts, s.unsubStatus(cell(k))+" "+itoa(d.Unsub[k]))
		}
		out = append(out, "unsubscribe: "+strings.Join(parts, "  "))
	}
	if len(d.Skipped) > 0 {
		parts := make([]string, 0, len(d.Skipped))
		for _, k := range sortedCountKeys(d.Skipped) {
			parts = append(parts, fmt.Sprintf("%s %d", cell(k), d.Skipped[k]))
		}
		out = append(out, s.subtle.Render("skipped: "+strings.Join(parts, ", ")))
	}
	if d.Summary != "" {
		out = append(out, s.subtle.Render(cell(d.Summary)))
	}
	if len(d.Manual) == 0 {
		out = append(out, s.subtle.Render("no senders were left needing a browser"))
	} else {
		out = append(out, "", s.header.Render(fmt.Sprintf("manual follow-up (%d)", len(d.Manual))))
		for _, link := range d.Manual {
			out = append(out, pad(cell(link.Display), 24)+" "+
				pad(s.unsubStatus(cell(link.Status)), 9)+" "+s.info.Render(cell(link.URL)))
		}
	}
	out = append(out, "", s.subtle.Render("audit "+cell(d.AuditPath)))
	return out
}

// unsubStatusOrder lists the statuses present, worst last, the way the
// results screen reads: ok, probable, manual, failed, then anything else.
func unsubStatusOrder(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for _, st := range []unsub.Status{unsub.StatusOK, unsub.StatusProbable, unsub.StatusManual, unsub.StatusFailed} {
		if m[string(st)] > 0 {
			out = append(out, string(st))
		}
	}
	for _, k := range sortedCountKeys(m) {
		if statusRank(k) == 3 {
			out = append(out, k)
		}
	}
	return out
}

func sortedCountKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m historyModel) help() string {
	switch {
	case m.confirmUndo:
		return "undo this run and move its mail back out of Trash? [y/n]"
	case m.undoing:
		return "undoing..."
	case m.showing:
		return "u undo this run  esc back to the list  q quit"
	}
	return "enter open  u undo  esc back  q quit"
}
