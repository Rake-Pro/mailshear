package tui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/scan"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// Screen transitions and the commands behind them: everything that turns a
// finished piece of work into the next screen. The dispatcher and the key
// handling live in flow_update.go.

// oauthURLMsg carries the authorization URL out of the sign-in goroutine so
// the screen can show it. It carries the channel it came from, the way
// scanProgressMsg does.
type oauthURLMsg struct {
	url string
	ch  chan string
}

// oauthDoneMsg ends the browser sign-in, successfully or not.
type oauthDoneMsg struct {
	err error
}

// signOutMsg asks for the stored OAuth token to be deleted; signedOutMsg is
// the answer.
type signOutMsg struct{}

type signedOutMsg struct {
	err error
}

func (m flowModel) signOut() (tea.Model, tea.Cmd) {
	b, acct := m.deps.Backend, m.acct
	return m, func() tea.Msg { return signedOutMsg{err: b.SignOut(acct)} }
}

func (m flowModel) onSignedOut(msg signedOutMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setup.err = msg.err.Error()
		return m, nil
	}
	m.setup.err = ""
	m.setup.status = m.deps.Backend.AuthStatus(m.acct)
	m.notice = "signed out; the token was deleted. Revoke access at the provider to stop it working elsewhere"
	return m, nil
}

func (m flowModel) onSetupSaved(msg setupSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setup.saving = false
		m.setup.err = msg.err.Error()
		return m, nil
	}
	m.acct = msg.acct

	// An OAuth account has no credential yet: the browser sign-in writes one
	// before the scan can connect.
	if m.acct.Auth == "oauth" {
		m.setup.saving = false
		m.setup.signingIn = true
		m.setup.authURL = ""
		m.setup.err = ""
		m.interrupted = false
		return m, tea.Batch(m.setup.spinner.Tick, m.startOAuthLogin(m.acct))
	}
	return m.afterSetup("saved " + m.deps.ConfigPath)
}

// afterSetup is where the form goes once it has saved: on to the scan on a
// fresh install, back to the accounts screen when the form was opened from
// there to add or edit one.
func (m flowModel) afterSetup(notice string) (tea.Model, tea.Cmd) {
	if m.setup.mode == setupFirstRun {
		m.screen = screenScan
		m.scan = newScanModel(m.styles, m.acct)
		m.notice = notice
		m.interrupted = false
		return m, tea.Batch(m.scan.spinner.Tick, m.startScan())
	}
	next, cmd := m.openAccounts()
	fm := next.(flowModel)
	fm.notice = notice
	return fm, cmd
}

func (m flowModel) onOAuthDone(msg oauthDoneMsg) (tea.Model, tea.Cmd) {
	m.setup.signingIn = false
	m.setup.authURL = ""
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			m.setup.err = "sign-in cancelled; press enter to try again"
		} else {
			m.setup.err = msg.err.Error()
		}
		m.interrupted = false
		m.notice = ""
		return m, nil
	}
	return m.afterSetup("signed in; the token is stored locally")
}

func (m flowModel) onScanDone(msg scanDoneMsg) (tea.Model, tea.Cmd) {
	m.scan.sum = msg.sum
	m.scan.done = true
	if msg.err != nil && errors.Is(msg.err, context.Canceled) {
		m.scan.cancelled = true
		msg.err = nil
	}
	if msg.err != nil {
		m.scan.err = msg.err
		return m, nil
	}

	notice := fmt.Sprintf("scanned %d message(s), %d bulk", msg.sum.Fetched, msg.sum.Bulk)
	if m.scan.cancelled {
		notice = "scan stopped; the cursor is saved, rerun to continue"
	} else if msg.sum.Fetched == 0 {
		notice = "nothing new since the last scan"
	}
	return m, m.loadGroups(notice)
}

func (m flowModel) onLoaded(msg loadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.scan.err = msg.err
		m.screen = screenScan
		return m, nil
	}
	if len(msg.groups) == 0 {
		m.scan.err = errors.New("no bulk senders recorded for this account yet")
		m.screen = screenScan
		return m, nil
	}

	m.groups = msg.groups
	m.notice = msg.notice
	m.screen = screenReview

	opts := Options{
		Account:           m.acct.Name,
		ProtectedKeys:     msg.protected,
		ProtectedByConfig: m.configProtect(),
		OpenURL:           m.deps.OpenURL,
		Embedded:          true,
		Version:           m.deps.Version,
	}
	m.review = newModel(msg.groups, opts)
	m.review.notice = msg.notice
	return m.forwardReview(tea.WindowSizeMsg{Width: m.width, Height: m.height})
}

// configProtect adapts the config protect list to a sender group, or nil when
// the list is empty.
func (m flowModel) configProtect() func(store.SenderGroup) bool {
	if m.deps.Cfg == nil {
		return nil
	}
	p := m.deps.Cfg.Protect
	if len(p.Domains)+len(p.Addresses)+len(p.ListIDs) == 0 {
		return nil
	}
	return func(g store.SenderGroup) bool {
		return p.Matches(g.DomainKey, g.Address, g.ListID)
	}
}

func (m flowModel) onReviewDone(msg reviewDoneMsg) (tea.Model, tea.Cmd) {
	res := m.review.result()
	if !msg.written {
		return m, m.finishReview(res.NewlyProtected, nil)
	}
	if len(res.Decisions) == 0 {
		m.review.notice = "nothing selected; mark a sender with space or d first"
		return m, nil
	}
	m.review.notice = "writing the plan..."
	return m, m.finishReview(res.NewlyProtected, res.Decisions)
}

func (m flowModel) onPrepared(msg preparedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.review.notice = "cannot prepare: " + msg.err.Error()
		m.screen = screenReview
		return m, nil
	}
	m.planPath = msg.path
	m.result.PlanPath = msg.path
	m.result.RunID = msg.preview.RunID
	m.confirm = newConfirmModel(m.styles, msg.preview, m.deps.NoDelete, m.deps.NoUnsubscribe)
	m.screen = screenConfirm
	m.notice = "plan written: " + msg.path
	return m, nil
}

func (m flowModel) onApplyDone(msg applyDoneMsg) (tea.Model, tea.Cmd) {
	m.apply.done = true
	if len(msg.events) > 0 {
		m.apply.results = unsubscribeEvents(msg.events)
	}
	if msg.err != nil && errors.Is(msg.err, context.Canceled) {
		m.apply.cancelled = true
		msg.err = nil
	}
	m.apply.err = msg.err

	m.result.Applied = true
	m.result.RunID = msg.sum.RunID
	m.result.Manual = len(manualFrom(m.apply.results))

	m.results = newResultsModel(m.styles, msg.sum, m.confirm.preview.Trash, m.apply.results)
	m.screen = screenResults
	switch {
	case msg.err != nil:
		m.notice = "apply stopped: " + msg.err.Error()
	case m.apply.cancelled:
		m.notice = "apply stopped between batches; the audit log is complete for what ran"
	default:
		m.notice = ""
	}
	return m, nil
}

func (m flowModel) onUndoDone(msg undoDoneMsg) (tea.Model, tea.Cmd) {
	m.results.undoing = false
	m.history.undoing = false
	if msg.err != nil {
		m.notice = "undo failed: " + msg.err.Error()
		if m.screen == screenHistory {
			m.history.err = msg.err.Error()
		}
		return m, nil
	}
	note := fmt.Sprintf("undone: restored %d message(s); %d no longer in Trash", msg.moved, msg.missing)
	if m.screen == screenHistory {
		m.history.note = note
		m.notice = ""
		return m, nil
	}
	m.results.undoNote = note
	m.notice = ""
	return m, nil
}

// unsubscribeEvents keeps only the unsubscribe results out of a run's events.
func unsubscribeEvents(events []apply.Event) []apply.Event {
	out := make([]apply.Event, 0, len(events))
	for _, e := range events {
		if e.Phase == apply.PhaseUnsubscribe {
			out = append(out, e)
		}
	}
	return out
}

// newOp cancels whatever was running and returns a context for the next
// operation, so ctrl+c always has exactly one thing to stop.
func (m *flowModel) newOp() context.Context {
	if m.opCancel != nil {
		m.opCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.opCancel = cancel
	return ctx
}

func (m flowModel) saveSetup(msg setupSubmitMsg) tea.Cmd {
	b := m.deps.Backend
	return func() tea.Msg {
		if msg.add {
			acct, err := b.AddAccount(msg.acct, msg.password)
			return setupSavedMsg{acct: acct, err: err}
		}
		acct, err := b.SaveSetup(msg.acct, msg.password)
		return setupSavedMsg{acct: acct, err: err}
	}
}

// startScan connects and scans on a goroutine, forwarding progress through a
// buffered channel. Progress is dropped rather than blocking the scan when
// the UI falls behind: every value carries running totals, so a lost frame
// costs nothing but a redraw.
func (m *flowModel) startScan() tea.Cmd {
	ctx := m.newOp()
	ch := make(chan scan.Progress, 64)

	b, acct := m.deps.Backend, m.acct
	work := func() tea.Msg {
		defer close(ch)
		if err := b.Connect(ctx, acct); err != nil {
			return scanDoneMsg{err: err}
		}
		sum, err := b.Scan(ctx, func(p scan.Progress) {
			select {
			case ch <- p:
			default:
			}
		})
		return scanDoneMsg{sum: sum, err: err}
	}
	return tea.Batch(work, waitScan(ch))
}

func waitScan(ch chan scan.Progress) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return scanProgressMsg{p: p, ch: ch}
	}
}

func (m *flowModel) loadGroups(notice string) tea.Cmd {
	ctx := m.newOp()
	b := m.deps.Backend
	return func() tea.Msg {
		groups, protected, err := b.Load(ctx)
		return loadedMsg{groups: groups, protected: protected, notice: notice, err: err}
	}
}

// finishReview persists any new protections, then writes the plan and
// previews it. With no decisions it quits: the user asked to leave.
func (m *flowModel) finishReview(protect []string, decisions []store.Decision) tea.Cmd {
	ctx := m.newOp()
	b, groups := m.deps.Backend, m.groups
	return func() tea.Msg {
		if len(protect) > 0 {
			if err := b.Protect(ctx, protect); err != nil {
				return preparedMsg{err: err}
			}
		}
		if decisions == nil {
			return tea.QuitMsg{}
		}
		p, path, err := b.WritePlan(ctx, groups, decisions)
		if err != nil {
			return preparedMsg{err: err}
		}
		pv, err := b.Prepare(ctx, p)
		return preparedMsg{plan: p, path: path, preview: pv, err: err}
	}
}

// startApply executes the prepared plan, streaming events through a buffered
// channel. Events are display only (the audit log is the record), so a full
// buffer drops a frame rather than stalling the run.
func (m *flowModel) startApply(opts apply.ExecOptions) tea.Cmd {
	ctx := m.newOp()
	ch := make(chan apply.Event, 256)

	b := m.deps.Backend
	var mu sync.Mutex
	var seen []apply.Event
	opts.OnEvent = func(e apply.Event) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
		select {
		case ch <- e:
		default:
		}
	}
	work := func() tea.Msg {
		defer close(ch)
		sum, err := b.Execute(ctx, opts)
		mu.Lock()
		events := append([]apply.Event(nil), seen...)
		mu.Unlock()
		return applyDoneMsg{sum: sum, events: events, err: err}
	}
	return tea.Batch(work, waitApply(ch))
}

func waitApply(ch chan apply.Event) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return applyEventMsg{e: e, ch: ch}
	}
}

// startOAuthLogin runs the browser sign-in on a goroutine, forwarding the
// authorization URL through a one-slot channel so the screen can show it
// while the user is still in the browser.
func (m *flowModel) startOAuthLogin(a config.Account) tea.Cmd {
	ctx := m.newOp()
	ch := make(chan string, 1)

	b := m.deps.Backend
	work := func() tea.Msg {
		defer close(ch)
		err := b.OAuthLogin(ctx, a, func(u string) {
			select {
			case ch <- u:
			default:
			}
		})
		return oauthDoneMsg{err: err}
	}
	return tea.Batch(work, waitOAuthURL(ch))
}

func waitOAuthURL(ch chan string) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return oauthURLMsg{url: u, ch: ch}
	}
}

func (m *flowModel) startUndo(runID string) tea.Cmd {
	ctx := m.newOp()
	b := m.deps.Backend
	return func() tea.Msg {
		moved, missing, err := b.Undo(ctx, runID)
		return undoDoneMsg{moved: moved, missing: missing, err: err}
	}
}
