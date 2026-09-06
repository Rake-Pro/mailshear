package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Chrome outside the body on every screen but review, which renders its own
// full frame: one header row, one notice row, one help row.
const flowChrome = 3

func (m flowModel) bodyHeight() int {
	h := m.height - flowChrome
	if h < 3 {
		h = 3
	}
	return h
}

func (m flowModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "mailshear"
	// Bubble Tea v2 turns bracketed paste on unless a view opts out; the setup
	// form and the review filter both read the tea.PasteMsg it produces.
	v.DisableBracketedPasteMode = false
	return v
}

func (m flowModel) render() string {
	if m.width < minWidth || m.height < minHeight {
		return tooSmall(m.styles, m.width, m.height)
	}
	if m.screen == screenReview {
		return m.review.render()
	}

	lines := []string{m.header()}
	lines = append(lines, m.body()...)
	lines = append(lines, m.styles.notice.Render(truncate(cell(m.notice), m.width)))
	lines = append(lines, m.styles.help.Render(truncate(m.helpLine(), m.width)))
	return frame(lines, m.width, m.height)
}

// header is the persistent context line: the app, the step, and the account.
func (m flowModel) header() string {
	s := m.styles
	left := s.title.Render("mailshear") + s.subtle.Render(" "+m.version()) + s.subtle.Render("  "+m.screen.String())

	right := s.chip.Render(cell(m.acct.Name))
	if m.acct.Username != "" && m.acct.Username != placeholderUsername {
		right = s.chip.Render(cell(m.acct.Name)) + s.subtle.Render("  "+cell(m.acct.Username))
	}

	steps := s.dim.Render(m.stepTrail())
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(steps) - lipgloss.Width(right) - 2
	if gap < 1 {
		return truncate(left+"  "+right, m.width)
	}
	return left + "  " + steps + strings.Repeat(" ", gap) + right
}

// version is the build tag for the header, or nothing when this build has no
// version stamped into it.
func (m flowModel) version() string {
	if m.deps.Version == "" || m.deps.Version == "dev" {
		return ""
	}
	return m.deps.Version
}

// stepTrail shows where in the pipeline this screen sits, with the current
// step picked out, so the flow reads as one thing rather than five screens.
func (m flowModel) stepTrail() string {
	switch m.screen {
	case screenAccounts, screenHistory, screenMenu:
		// These sit beside the pipeline; the header already names them.
		return ""
	}
	order := []screen{screenSetup, screenScan, screenReview, screenConfirm, screenApply, screenResults}
	parts := make([]string, 0, len(order))
	for _, sc := range order {
		if sc == screenSetup && m.screen != screenSetup {
			continue // setup only shows while it is the current step
		}
		if sc == m.screen {
			parts = append(parts, m.styles.accent.Render(sc.String()))
			continue
		}
		parts = append(parts, sc.String())
	}
	return strings.Join(parts, " > ")
}

func (m flowModel) body() []string {
	h := m.bodyHeight()
	var lines []string
	switch m.screen {
	case screenSetup:
		lines = m.setup.view(m.width, h)
	case screenScan:
		lines = m.scan.view(m.width, h)
	case screenConfirm:
		lines = m.confirm.view(m.width, h)
	case screenApply:
		lines = m.apply.view(m.width, h)
	case screenResults:
		lines = m.results.view(m.width, h)
	case screenAccounts:
		lines = m.accounts.view(m.width, h)
	case screenHistory:
		lines = m.history.view(m.width, h)
	case screenMenu:
		lines = m.menu.view(m.width, h)
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

func (m flowModel) helpLine() string {
	switch m.screen {
	case screenSetup:
		return m.setup.help()
	case screenScan:
		return m.scan.help()
	case screenConfirm:
		return m.confirm.help()
	case screenApply:
		return m.apply.help()
	case screenResults:
		return m.results.help()
	case screenAccounts:
		return m.accounts.help()
	case screenHistory:
		return m.history.help()
	case screenMenu:
		return m.menu.help()
	}
	return ""
}
