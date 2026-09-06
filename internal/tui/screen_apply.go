package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"

	"github.com/Rake-Pro/mailshear/internal/apply"
)

type applyModel struct {
	styles  styles
	spinner spinner.Model
	runID   string
	trash   string

	started time.Time
	// results are the unsubscribe outcomes as they stream in.
	results []apply.Event
	// folder is the folder the delete phase is working through.
	folder string
	moved  int
	// total is the message count Prepare promised, used for the bar.
	total   int
	skipped []string

	done bool
	err  error
	// cancelled records a ctrl+c: the run stopped between batches and the
	// audit log is consistent.
	cancelled bool
}

// applyEventMsg is one progress record forwarded out of the apply goroutine,
// with the channel to keep reading from.
type applyEventMsg struct {
	e  apply.Event
	ch chan apply.Event
}

// applyDoneMsg ends the run. It carries every event the run emitted, so the
// results screen does not depend on the display stream having been drained
// first: those messages are for the live view, this one is the record.
type applyDoneMsg struct {
	sum    apply.Summary
	events []apply.Event
	err    error
}

// streamClosedMsg says the event channel drained; no more applyEventMsg will
// arrive.
type streamClosedMsg struct{}

func newApplyModel(s styles, pv apply.Preview, opts apply.ExecOptions) applyModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = s.accent

	total := pv.Messages
	if opts.NoDelete {
		total = 0
	}
	return applyModel{
		styles:  s,
		spinner: sp,
		runID:   pv.RunID,
		trash:   pv.Trash,
		started: time.Now(),
		total:   total,
	}
}

func (m applyModel) apply(e apply.Event) applyModel {
	switch e.Phase {
	case apply.PhaseUnsubscribe:
		m.results = append(m.results, e)
	case apply.PhaseDelete:
		if e.SkipReason != "" {
			m.skipped = append(m.skipped, cell(e.Folder)+": "+cell(e.SkipReason))
			return m
		}
		m.folder = cell(e.Folder)
		m.moved = e.TotalMoved
	case apply.PhaseDone:
		m.moved = e.TotalMoved
	}
	return m
}

func (m applyModel) view(width, height int) []string {
	s := m.styles

	// Two stacked panels, each carrying its own border pair.
	deleteH := 4
	unsubH := height - deleteH - 4
	if unsubH < 3 {
		unsubH = 3
	}

	out := strings.Split(s.panel(s.box, m.unsubTitle(), m.unsubLines(width, unsubH-1), width, unsubH), "\n")
	return append(out, strings.Split(s.panel(s.box, m.deleteTitle(), m.deleteLines(width), width, deleteH), "\n")...)
}

func (m applyModel) unsubTitle() string {
	return fmt.Sprintf("unsubscribe (%d)", len(m.results))
}

func (m applyModel) unsubLines(width, height int) []string {
	s := m.styles
	if len(m.results) == 0 {
		if m.done {
			return []string{s.dim.Render("nothing to unsubscribe")}
		}
		return []string{m.spinner.View() + " sending unsubscribe requests..."}
	}

	const methodW, statusW, detailW = 9, 9, 26
	nameW := width - 4 - methodW - statusW - detailW - 3
	if nameW < 12 {
		nameW = 12
	}
	// Only the last screenful is kept: the full record is in the audit log.
	start := 0
	if len(m.results) > height {
		start = len(m.results) - height
	}
	out := make([]string, 0, height)
	for _, r := range m.results[start:] {
		detail := cell(r.Err)
		if r.HTTPStatus != 0 {
			detail = strings.TrimSpace(fmt.Sprintf("http %d %s", r.HTTPStatus, detail))
		}
		out = append(out, pad(cell(r.Display), nameW)+" "+
			pad(s.method(cell(r.Method)), methodW)+" "+
			pad(s.unsubStatus(cell(r.Status)), statusW)+" "+
			pad(detail, detailW))
	}
	return out
}

func (m applyModel) deleteTitle() string {
	if m.trash == "" {
		return "delete"
	}
	return "delete -> " + cell(m.trash)
}

func (m applyModel) deleteLines(width int) []string {
	s := m.styles
	// Panel borders and padding take four cells, the counters twenty.
	barW := width - 24
	if barW < 10 {
		barW = 10
	}

	var head string
	switch {
	case m.total > 0:
		frac := float64(m.moved) / float64(m.total)
		head = s.bar(barW, frac) + fmt.Sprintf("  %d/%d", m.moved, m.total)
	case m.moved > 0:
		head = fmt.Sprintf("moved %d", m.moved)
	default:
		head = s.dim.Render("nothing to move")
	}

	second := s.subtle.Render("elapsed " + elapsed(time.Since(m.started)))
	if m.folder != "" {
		second = s.subtle.Render("folder "+cell(m.folder)) + "   " + second
	}
	if n := len(m.skipped); n > 0 {
		second += s.warn.Render(fmt.Sprintf("   %d folder(s) skipped", n))
	}
	return []string{head, second}
}

func (m applyModel) help() string {
	return "ctrl+c stop between batches (the audit log stays consistent)"
}
