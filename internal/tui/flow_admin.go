package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/Rake-Pro/mailshear/internal/config"
)

// Transitions, commands and key handling for the three screens beside the
// pipeline: accounts, history and maintenance. Everything here goes through
// the Backend, so none of it knows about IMAP or SQLite.

// openAccounts shows the account switcher.
func (m flowModel) openAccounts() (tea.Model, tea.Cmd) {
	m.back = m.returnTarget()
	m.accounts = newAccountsModel(m.styles, m.deps.ConfigPath)
	m.accounts.notice = configNotice(m.deps.Cfg)
	m.accounts.canReturn = m.canReturn()
	m.screen = screenAccounts
	m.notice = ""
	return m, m.loadAccounts()
}

func (m flowModel) openHistory() (tea.Model, tea.Cmd) {
	m.back = m.returnTarget()
	m.history = newHistoryModel(m.styles)
	m.screen = screenHistory
	m.notice = ""
	return m, m.loadRuns()
}

func (m flowModel) openMenu() (tea.Model, tea.Cmd) {
	m.back = m.returnTarget()
	m.menu = newMenuModel(m.styles)
	m.screen = screenMenu
	m.notice = ""
	return m, nil
}

// returnTarget is the pipeline screen a side trip should come back to. The
// side screens themselves keep whatever the last one recorded, so accounts to
// form and back lands where the trip started.
func (m flowModel) returnTarget() screen {
	switch m.screen {
	case screenReview, screenResults:
		return m.screen
	}
	return m.back
}

// canReturn reports that there is somewhere to go back to, which is what puts
// esc in the footer instead of leaving only q.
func (m flowModel) canReturn() bool {
	switch m.back {
	case screenReview:
		return len(m.groups) > 0
	case screenResults:
		return m.result.Applied
	}
	return false
}

// returnBack leaves a screen beside the pipeline. From the very first screen
// there is nothing behind it but the exit.
func (m flowModel) returnBack() (tea.Model, tea.Cmd) {
	if !m.canReturn() {
		return m, tea.Quit
	}
	m.screen = m.back
	return m, nil
}

func (m *flowModel) loadAccounts() tea.Cmd {
	ctx, b := m.ctx, m.deps.Backend
	return func() tea.Msg {
		rows, err := b.ListAccounts(ctx)
		return accountsLoadedMsg{rows: rows, err: err}
	}
}

func (m *flowModel) loadRuns() tea.Cmd {
	ctx, b, acct := m.ctx, m.deps.Backend, m.acct
	return func() tea.Msg {
		runs, err := b.Runs(ctx, acct)
		return runsLoadedMsg{runs: runs, err: err}
	}
}

func (m *flowModel) loadRunDetail(runID string) tea.Cmd {
	ctx, b := m.ctx, m.deps.Backend
	return func() tea.Msg {
		d, err := b.RunDetail(ctx, runID)
		return runDetailMsg{detail: d, err: err}
	}
}

func (m *flowModel) loadProtected() tea.Cmd {
	ctx, b, acct := m.ctx, m.deps.Backend, m.acct
	return func() tea.Msg {
		entries, err := b.ListProtected(ctx, acct)
		return protectedLoadedMsg{entries: entries, err: err}
	}
}

func (m flowModel) onAccountsLoaded(msg accountsLoadedMsg) (tea.Model, tea.Cmd) {
	m.accounts.loading = false
	if msg.err != nil {
		m.accounts.err = msg.err.Error()
		return m, nil
	}
	m.accounts.err = ""
	m.accounts.rows = msg.rows
	m.accounts = m.accounts.moveBy(0)
	// Keep the cursor on the account the session is working with.
	for i, r := range msg.rows {
		if r.Account.Name == m.acct.Name {
			m.accounts.cursor = i
			break
		}
	}
	return m, nil
}

func (m flowModel) onAccountsChanged(msg accountsChangedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.accounts.err = msg.err.Error()
		m.accounts.loading = false
		return m, nil
	}
	m.accounts.err = ""
	m.accounts.notice = msg.notice
	m.accounts.loading = true
	return m, m.loadAccounts()
}

// useAccount is enter on the accounts screen: work on this mailbox. Without a
// stored credential there is nothing to connect with, so the form opens
// instead of a scan that would only fail.
func (m flowModel) useAccount(row AccountStatus) (tea.Model, tea.Cmd) {
	m.acct = row.Account
	m.groups = nil
	if m.deps.Backend != nil && !m.deps.Backend.HasCredentials(row.Account) {
		return m.openSetup(setupEdit, "this account has no stored credential yet")
	}
	m.screen = screenScan
	m.scan = newScanModel(m.styles, m.acct)
	m.interrupted = false
	return m, tea.Batch(m.scan.spinner.Tick, m.startScan())
}

// openSetup shows the form over the current account (edit) or an empty one
// (add). The first-run form is opened by newFlowModel instead.
func (m flowModel) openSetup(mode setupMode, notice string) (tea.Model, tea.Cmd) {
	acct := m.acct
	if mode == setupAdd {
		acct = config.Account{Name: "", Auth: "file"}
	}
	m.setup = newSetupModel(m.styles, acct, m.deps.ConfigPath)
	m.setup.mode = mode
	m.setup.existing = m.accountNames()
	if mode == setupAdd {
		m.setup.fields[fName].value = ""
	}
	if mode == setupEdit && m.deps.Backend != nil {
		m.setup.status = m.deps.Backend.AuthStatus(acct)
	}
	m.screen = screenSetup
	m.notice = notice
	return m, nil
}

// accountNames lists the account names already configured, so the add form
// can refuse a duplicate before it reaches the config file.
func (m flowModel) accountNames() []string {
	if len(m.accounts.rows) > 0 {
		out := make([]string, 0, len(m.accounts.rows))
		for _, r := range m.accounts.rows {
			out = append(out, r.Account.Name)
		}
		return out
	}
	if m.deps.Cfg == nil {
		return nil
	}
	out := make([]string, 0, len(m.deps.Cfg.Accounts))
	for _, a := range m.deps.Cfg.Accounts {
		out = append(out, a.Name)
	}
	return out
}

func (m flowModel) accountsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.accounts.confirm != "" {
		return m.accountsConfirmKey(msg)
	}
	m.accounts.notice = ""

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		return m.returnBack()
	case "up", "k":
		m.accounts = m.accounts.moveBy(-1)
	case "down", "j":
		m.accounts = m.accounts.moveBy(1)
	case "enter":
		if row, ok := m.accounts.current(); ok {
			return m.useAccount(row)
		}
	case "n":
		return m.openSetup(setupAdd, "")
	case "e":
		if row, ok := m.accounts.current(); ok {
			m.acct = row.Account
			return m.openSetup(setupEdit, "")
		}
	case "x":
		if _, ok := m.accounts.current(); ok {
			m.accounts.confirm = confirmSignOut
		}
	case "r":
		if _, ok := m.accounts.current(); ok {
			m.accounts.confirm = confirmRemove
		}
	}
	return m, nil
}

// accountsConfirmKey answers the open y/n question. Removing an account asks
// twice: once for the config entry and its credential, once more before any
// stored mail data is dropped.
func (m flowModel) accountsConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	row, ok := m.accounts.current()
	if !ok {
		m.accounts.confirm = ""
		return m, nil
	}
	yes := msg.String() == "y" || msg.String() == "Y"
	no := msg.String() == "n" || msg.String() == "N" || msg.String() == "esc" || msg.String() == "q"

	switch m.accounts.confirm {
	case confirmSignOut:
		if yes {
			m.accounts.confirm = ""
			m.accounts.loading = true
			return m, m.signOutAccount(row.Account)
		}
		if no {
			m.accounts.confirm = ""
			m.accounts.notice = "sign-out cancelled"
		}
	case confirmRemove:
		if yes {
			m.accounts.confirm = confirmPurge
		}
		if no {
			m.accounts.confirm = ""
			m.accounts.notice = "the account was left alone"
		}
	case confirmPurge:
		if yes || no {
			m.accounts.confirm = ""
			m.accounts.loading = true
			return m, m.removeAccount(row.Account, yes)
		}
	}
	return m, nil
}

func (m *flowModel) signOutAccount(a config.Account) tea.Cmd {
	b := m.deps.Backend
	return func() tea.Msg {
		if err := b.SignOut(a); err != nil {
			return accountsChangedMsg{err: err}
		}
		return accountsChangedMsg{
			notice: "signed out " + a.Name + "; revoke access at the provider to stop it working elsewhere",
		}
	}
}

func (m *flowModel) removeAccount(a config.Account, purge bool) tea.Cmd {
	ctx, b := m.ctx, m.deps.Backend
	return func() tea.Msg {
		if err := b.RemoveAccount(ctx, a, purge); err != nil {
			return accountsChangedMsg{err: err}
		}
		notice := "removed " + a.Name + " from the config; its scanned data was kept"
		if purge {
			notice = "removed " + a.Name + " and dropped its scanned data; no mail was touched"
		}
		return accountsChangedMsg{notice: notice}
	}
}

func (m flowModel) onRunsLoaded(msg runsLoadedMsg) (tea.Model, tea.Cmd) {
	m.history.loading = false
	if msg.err != nil {
		m.history.err = msg.err.Error()
		return m, nil
	}
	m.history.err = ""
	m.history.runs = msg.runs
	m.history = m.history.moveBy(0)
	return m, nil
}

func (m flowModel) onRunDetail(msg runDetailMsg) (tea.Model, tea.Cmd) {
	m.history.loading = false
	if msg.err != nil {
		m.history.err = msg.err.Error()
		return m, nil
	}
	m.history.detail = msg.detail
	m.history.showing = true
	return m, nil
}

func (m flowModel) historyKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.history.confirmUndo {
		switch msg.String() {
		case "y", "Y":
			run, ok := m.history.current()
			if !ok {
				m.history.confirmUndo = false
				return m, nil
			}
			m.history.confirmUndo = false
			m.history.undoing = true
			m.interrupted = false
			return m, m.startUndo(run.ID)
		case "n", "N", "esc", "q":
			m.history.confirmUndo = false
		}
		return m, nil
	}
	if m.history.undoing || m.history.loading {
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.history.showing {
			m.history.showing = false
			m.history.note = ""
			return m, nil
		}
		return m.returnBack()
	case "up", "k":
		m.history = m.history.moveBy(-1)
	case "down", "j":
		m.history = m.history.moveBy(1)
	case "enter":
		if run, ok := m.history.current(); ok {
			m.history.loading = true
			return m, m.loadRunDetail(run.ID)
		}
	case "u":
		if _, ok := m.history.current(); ok {
			m.history.confirmUndo = true
		}
	}
	return m, nil
}

func (m flowModel) menuKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.menu.rescanPrompt {
		switch msg.String() {
		case "y", "Y":
			m.menu.rescanPrompt = false
			m.screen = screenScan
			m.scan = newScanModel(m.styles, m.acct)
			m.interrupted = false
			return m, tea.Batch(m.scan.spinner.Tick, m.startScan())
		case "n", "N", "esc", "q":
			m.menu.rescanPrompt = false
		}
		return m, nil
	}
	if m.menu.confirm >= 0 {
		switch msg.String() {
		case "y", "Y":
			action := m.menu.confirm
			m.menu.confirm = -1
			m.menu.busy = true
			return m, m.runMaintenance(action)
		case "n", "N", "esc", "q":
			m.menu.confirm = -1
			m.menu.notice = "cancelled"
		}
		return m, nil
	}
	if m.menu.busy {
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		if m.menu.panel != "" {
			m.menu.panel = ""
			m.menu.notice = ""
			return m, nil
		}
		return m.returnBack()
	case "up", "k":
		m.menu = m.menu.moveBy(-1)
	case "down", "j":
		m.menu = m.menu.moveBy(1)
	case "x":
		if m.menu.panel != panelProtected {
			return m, nil
		}
		e, ok := m.menu.currentProtected()
		if !ok {
			return m, nil
		}
		if e.Source != "store" {
			m.menu.notice = "that one comes from the protect: list in " + m.deps.ConfigPath
			return m, nil
		}
		m.menu.busy = true
		return m, m.unprotect(e.Key)
	case "enter":
		if m.menu.panel != "" {
			return m, nil
		}
		return m.startMenuAction(m.menu.cursor)
	}
	return m, nil
}

// startMenuAction opens a panel, asks for confirmation, or runs the action
// outright when it changes nothing.
func (m flowModel) startMenuAction(action int) (tea.Model, tea.Cmd) {
	m.menu.notice = ""
	m.menu.err = ""
	switch action {
	case actPurge, actRescan:
		m.menu.confirm = action
		return m, nil
	case actProtected:
		m.menu.panel = panelProtected
		m.menu.busy = true
		return m, m.loadProtected()
	case actPaths:
		m.menu.panel = panelPaths
		if m.deps.Backend != nil {
			m.menu.paths = m.deps.Backend.Paths()
		}
		if m.menu.paths.Version == "" {
			m.menu.paths.Version = m.deps.Version
		}
		return m, nil
	default:
		m.menu.busy = true
		return m, m.runMaintenance(action)
	}
}

func (m *flowModel) runMaintenance(action int) tea.Cmd {
	ctx, b, acct := m.ctx, m.deps.Backend, m.acct
	groups := m.groups
	openURL := m.deps.OpenURL
	configPath := m.deps.ConfigPath

	switch action {
	case actPurge:
		return func() tea.Msg {
			if err := b.Purge(ctx, acct); err != nil {
				return maintenanceDoneMsg{err: err}
			}
			return maintenanceDoneMsg{
				notice: "cached messages dropped; the sender decisions are still there",
				rescan: true,
			}
		}
	case actRescan:
		return func() tea.Msg {
			if err := b.ResetFolders(ctx, acct); err != nil {
				return maintenanceDoneMsg{err: err}
			}
			return maintenanceDoneMsg{notice: "folder cursors cleared", rescan: true}
		}
	case actExport:
		return func() tea.Msg {
			if len(groups) == 0 {
				return maintenanceDoneMsg{err: fmt.Errorf("nothing to export yet; scan first")}
			}
			csvPath, jsonPath, err := b.Export(ctx, acct, groups)
			if err != nil {
				return maintenanceDoneMsg{err: err}
			}
			return maintenanceDoneMsg{notice: "wrote " + csvPath + " and " + jsonPath}
		}
	case actConfigFile:
		return func() tea.Msg {
			if openURL == nil {
				return maintenanceDoneMsg{notice: configPath}
			}
			if err := openURL(configPath); err != nil {
				return maintenanceDoneMsg{notice: "could not open it here: " + configPath}
			}
			return maintenanceDoneMsg{notice: "opened " + configPath}
		}
	}
	return nil
}

func (m *flowModel) unprotect(key string) tea.Cmd {
	ctx, b, acct := m.ctx, m.deps.Backend, m.acct
	return func() tea.Msg {
		if err := b.Unprotect(ctx, acct, key); err != nil {
			return protectedLoadedMsg{err: err}
		}
		entries, err := b.ListProtected(ctx, acct)
		return protectedLoadedMsg{entries: entries, err: err}
	}
}

func (m flowModel) onProtectedLoaded(msg protectedLoadedMsg) (tea.Model, tea.Cmd) {
	m.menu.busy = false
	if msg.err != nil {
		m.menu.err = msg.err.Error()
		return m, nil
	}
	m.menu.err = ""
	m.menu.protected = msg.entries
	m.menu = m.menu.moveBy(0)
	return m, nil
}

func (m flowModel) onMaintenanceDone(msg maintenanceDoneMsg) (tea.Model, tea.Cmd) {
	m.menu.busy = false
	if msg.err != nil {
		m.menu.err = msg.err.Error()
		return m, nil
	}
	m.menu.err = ""
	m.menu.notice = msg.notice
	m.menu.rescanPrompt = msg.rescan
	return m, nil
}
