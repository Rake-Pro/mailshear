package tui

import (
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// beginScanMsg starts the scan from inside Update, where the model change
// that records the operation's cancel function actually sticks.
type beginScanMsg struct{}

// loadedMsg carries the sender groups and the protected set into the review
// screen.
type loadedMsg struct {
	groups    []store.SenderGroup
	protected map[string]bool
	notice    string
	err       error
}

// preparedMsg is the plan file plus the preview of what applying it would do.
type preparedMsg struct {
	plan    *plan.Plan
	path    string
	preview apply.Preview
	err     error
}

func (m flowModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.resize()

	case tea.BackgroundColorMsg:
		// Bubble Tea reports the real background once the program owns the
		// terminal; re-derive the palette so the startup guess cannot stick.
		return m.restyle(msg.IsDark()), nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		switch m.screen {
		case screenSetup:
			m.setup.spinner, cmd = m.setup.spinner.Update(msg)
		case screenScan:
			m.scan.spinner, cmd = m.scan.spinner.Update(msg)
		case screenApply:
			m.apply.spinner, cmd = m.apply.spinner.Update(msg)
		}
		return m, cmd

	case setupSubmitMsg:
		return m, m.saveSetup(msg)
	case setupSavedMsg:
		return m.onSetupSaved(msg)
	case mxResultMsg:
		sm, cmd := m.setup.mxResult(msg)
		m.setup = sm
		return m, cmd

	case oauthURLMsg:
		m.setup.authURL = msg.url
		return m, waitOAuthURL(msg.ch)
	case oauthDoneMsg:
		return m.onOAuthDone(msg)
	case signOutMsg:
		return m.signOut()
	case signedOutMsg:
		return m.onSignedOut(msg)

	case accountMsg:
		return m.openAccounts()
	case historyMsg:
		return m.openHistory()
	case menuMsg:
		return m.openMenu()

	case accountsLoadedMsg:
		return m.onAccountsLoaded(msg)
	case accountsChangedMsg:
		return m.onAccountsChanged(msg)
	case runsLoadedMsg:
		return m.onRunsLoaded(msg)
	case runDetailMsg:
		return m.onRunDetail(msg)
	case protectedLoadedMsg:
		return m.onProtectedLoaded(msg)
	case maintenanceDoneMsg:
		return m.onMaintenanceDone(msg)

	case beginScanMsg:
		return m, m.startScan()

	case scanProgressMsg:
		m.scan.connected = true
		m.scan.progress = msg.p
		return m, waitScan(msg.ch)
	case scanDoneMsg:
		return m.onScanDone(msg)

	case loadedMsg:
		return m.onLoaded(msg)

	case reviewDoneMsg:
		return m.onReviewDone(msg)
	case preparedMsg:
		return m.onPrepared(msg)

	case applyEventMsg:
		m.apply = m.apply.apply(msg.e)
		return m, waitApply(msg.ch)
	case streamClosedMsg:
		return m, nil
	case applyDoneMsg:
		return m.onApplyDone(msg)

	case undoDoneMsg:
		return m.onUndoDone(msg)

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
		return m.handlePaste(msg)
	}

	// Anything unrecognised still belongs to the review screen while it is
	// showing, since it owns a help component of its own.
	if m.screen == screenReview {
		return m.forwardReview(msg)
	}
	return m, nil
}

// restyle rebuilds every screen's style token set for the detected
// background. The screens hold a copy so they can render without reaching
// back into the flow.
func (m flowModel) restyle(dark bool) flowModel {
	m.styles = newStylesFor(dark)
	m.setup.styles = m.styles
	m.setup.spinner.Style = m.styles.accent
	m.scan.styles = m.styles
	m.scan.spinner.Style = m.styles.accent
	m.review.styles = m.styles
	m.accounts.styles = m.styles
	m.history.styles = m.styles
	m.menu.styles = m.styles
	m.confirm.styles = m.styles
	m.apply.styles = m.styles
	m.apply.spinner.Style = m.styles.accent
	m.results.styles = m.styles
	return m
}

func (m flowModel) resize() (tea.Model, tea.Cmd) {
	if m.screen == screenReview {
		// The review screen draws the whole frame, chrome included, so it
		// gets the terminal size rather than a body slice.
		return m.forwardReview(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	}
	return m, nil
}

// forwardReview hands a message to the embedded review screen.
func (m flowModel) forwardReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	rm, cmd := m.review.Update(msg)
	m.review = rm.(model)
	return m, cmd
}

func (m flowModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+z":
		return m, tea.Suspend
	case "ctrl+c":
		return m.interrupt()
	}

	switch m.screen {
	case screenSetup:
		if m.setup.signingIn {
			if msg.String() == "c" {
				return m.interrupt()
			}
			return m, nil
		}
		if msg.String() == "esc" {
			// Opened from the accounts screen, esc goes back to it; on
			// first-run setup there is nothing behind it but the exit.
			if m.setup.mode != setupFirstRun {
				return m.openAccounts()
			}
			return m, tea.Quit
		}
		sm, cmd := m.setup.update(msg)
		m.setup = sm
		return m, cmd
	case screenScan:
		return m.scanKey(msg)
	case screenReview:
		m.notice = ""
		return m.forwardReview(msg)
	case screenConfirm:
		return m.confirmKey(msg)
	case screenApply:
		// The run advances to the results on its own; nothing else applies
		// here except the ctrl+c handled above.
		return m, nil
	case screenAccounts:
		return m.accountsKey(msg)
	case screenHistory:
		return m.historyKey(msg)
	case screenMenu:
		return m.menuKey(msg)
	default:
		return m.resultsKey(msg)
	}
}

// handlePaste routes a bracketed paste to whichever screen has a text field
// open. Everywhere else a paste means nothing and is dropped.
func (m flowModel) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenSetup:
		sm, cmd := m.setup.paste(msg.Content)
		m.setup = sm
		return m, cmd
	case screenReview:
		return m.forwardReview(msg)
	}
	return m, nil
}

// interrupt implements ctrl+c: the first press cancels whatever is running,
// the second quits. With nothing running it quits at once.
func (m flowModel) interrupt() (tea.Model, tea.Cmd) {
	busy := (m.screen == screenSetup && m.setup.signingIn) ||
		(m.screen == screenScan && !m.scan.done) ||
		(m.screen == screenApply && !m.apply.done) ||
		m.results.undoing || m.history.undoing
	if busy && !m.interrupted {
		m.interrupted = true
		m.notice = "stopping..."
		if m.opCancel != nil {
			m.opCancel()
		}
		return m, nil
	}
	return m, tea.Quit
}

func (m flowModel) scanKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.scan.err != nil {
		switch msg.String() {
		case "r":
			m.scan = newScanModel(m.styles, m.acct)
			m.interrupted = false
			return m, tea.Batch(m.scan.spinner.Tick, m.startScan())

		case "s":
			m.screen = screenSetup
			m.setup = newSetupModel(m.styles, m.acct, m.deps.ConfigPath)
			return m, nil
		case "q":
			return m, tea.Quit
		}
		return m, nil
	}
	if msg.String() == "q" {
		return m.interrupt()
	}
	return m, nil
}

func (m flowModel) confirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "y":
		opts := m.confirm.execOptions()
		m.apply = newApplyModel(m.styles, m.confirm.preview, opts)
		m.screen = screenApply
		m.interrupted = false
		return m, tea.Batch(m.apply.spinner.Tick, m.startApply(opts))
	case "d":
		m.confirm.noDelete = !m.confirm.noDelete
	case "u":
		m.confirm.noUnsubscribe = !m.confirm.noUnsubscribe
	case "up", "k":
		m.confirm = m.confirm.moveBy(-1)
	case "down", "j":
		m.confirm = m.confirm.moveBy(1)
	case "esc":
		// Backing out changes nothing: the plan file stays on disk for the
		// command line, and nothing has reached the server.
		m.screen = screenReview
		m.review.notice = "back in review; the plan is still at " + m.planPath
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

func (m flowModel) resultsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.results.confirmUndo {
		switch msg.String() {
		case "y", "Y":
			m.results.confirmUndo = false
			m.results.undoing = true
			m.interrupted = false
			return m, m.startUndo(m.result.RunID)
		case "n", "N", "esc", "q":
			m.results.confirmUndo = false
		}
		return m, nil
	}
	if m.results.undoing {
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.results = m.results.moveBy(-1)
	case "down", "j":
		m.results = m.results.moveBy(1)
	case "enter":
		m.results.detail = !m.results.detail
	case "u":
		m.results.confirmUndo = true
	case "o":
		return m.openCurrentResult()
	case "c":
		if it, ok := m.results.current(); ok && it.url != "" {
			m.notice = "copied " + it.url
			return m, tea.SetClipboard(it.url)
		}
		m.notice = "no link to copy for this sender"
	case "r":
		// Another round over the same mailbox; the scan state is current.
		return m, m.loadGroups("another round over the same scan")
	case "h":
		return m.openHistory()
	}
	return m, nil
}

func (m flowModel) openCurrentResult() (tea.Model, tea.Cmd) {
	it, ok := m.results.current()
	if !ok || it.url == "" {
		m.notice = "no link recorded for this sender"
		return m, nil
	}
	if m.deps.OpenURL == nil {
		m.notice = it.url
		return m, nil
	}
	open, uri := m.deps.OpenURL, it.url
	return m, func() tea.Msg { return openedMsg{uri: uri, err: open(uri)} }
}
