package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/scan"
)

type scanModel struct {
	styles  styles
	spinner spinner.Model
	acct    config.Account

	started  time.Time
	progress scan.Progress
	// connected flips once the first progress arrives, which is the first
	// proof the login worked.
	connected bool
	sum       scan.Summary
	done      bool
	err       error
	// cancelled records a ctrl+c during the scan: the cursor is saved, so
	// this is a stop rather than a failure.
	cancelled bool
}

// scanProgressMsg is one checkpoint forwarded out of the scan goroutine. It
// carries the channel it came from so the next read re-arms itself without
// the model having to remember it.
type scanProgressMsg struct {
	p  scan.Progress
	ch chan scan.Progress
}

// scanDoneMsg ends the scan, successfully or not.
type scanDoneMsg struct {
	sum scan.Summary
	err error
}

func newScanModel(s styles, a config.Account) scanModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = s.accent
	return scanModel{styles: s, spinner: sp, acct: a, started: time.Now()}
}

// hint turns a login failure into something the user can act on. Gmail and
// Workspace reject plain passwords outright, and the server's message says
// only "invalid credentials".
func (m scanModel) hint() []string {
	if m.err == nil {
		return nil
	}
	text := strings.ToLower(m.err.Error())
	if !strings.Contains(text, "auth") && !strings.Contains(text, "credential") &&
		!strings.Contains(text, "login") && !strings.Contains(text, "password") {
		return nil
	}
	switch m.acct.Provider() {
	case config.ProviderGmail:
		return []string{
			"Gmail rejects account passwords over IMAP. Turn on 2-Step Verification, then",
			"create an app password at https://myaccount.google.com/apppasswords and use",
			"that here. Workspace accounts also need IMAP enabled by the administrator.",
		}
	default:
		return []string{
			"Most providers need an app-specific password rather than the one you log in",
			"with, and some need IMAP switched on in their web settings first.",
		}
	}
}

func (m scanModel) view(width, height int) []string {
	s := m.styles

	if m.err != nil {
		return m.errorView(width, height)
	}

	var lines []string
	if m.connected {
		lines = append(lines, fmt.Sprintf("folder %s", cell(m.progress.Folder)))
	} else {
		lines = append(lines, m.spinner.View()+" connecting to "+cell(m.acct.Host))
	}

	// Panel borders and padding take four cells, the percentage six.
	barW := width - 12
	if barW < 10 {
		barW = 10
	}
	switch {
	case m.progress.Total > 0:
		frac := float64(m.progress.Done) / float64(m.progress.Total)
		lines = append(lines, s.bar(barW, frac)+fmt.Sprintf("  %3.0f%%", frac*100))
		lines = append(lines, fmt.Sprintf("this folder %d/%d", m.progress.Done, m.progress.Total))
	case m.connected:
		lines = append(lines, s.dim.Render(strings.Repeat("-", barW))+"  size unknown")
		lines = append(lines, fmt.Sprintf("this folder %d", m.progress.Done))
	default:
		lines = append(lines, s.dim.Render(strings.Repeat("-", barW)))
		lines = append(lines, "")
	}

	lines = append(lines,
		"",
		fmt.Sprintf("fetched %d   bulk %d   folders %d   elapsed %s",
			m.progress.Fetched, m.progress.Bulk, m.sum.Folders, elapsed(time.Since(m.started))),
	)
	lines = append(lines, s.subtle.Render("the scan reads headers only; nothing on the server changes"))

	return strings.Split(s.panel(s.box, "scanning "+cell(m.acct.Name), lines, width, height-2), "\n")
}

func (m scanModel) errorView(width, height int) []string {
	s := m.styles
	lines := []string{s.err.Render(truncate(cell(m.err.Error()), width-4))}
	if hint := m.hint(); len(hint) > 0 {
		lines = append(lines, "")
		for _, l := range hint {
			lines = append(lines, s.subtle.Render(l))
		}
	}
	lines = append(lines, "", s.subtle.Render("r retry   s back to setup   q quit"))
	return strings.Split(s.panel(s.boxErr, "cannot scan", lines, width, height-2), "\n")
}

func (m scanModel) help() string {
	if m.err != nil {
		return "r retry  s setup  q quit"
	}
	return "ctrl+c cancel (the scan cursor is saved)  q quit"
}
