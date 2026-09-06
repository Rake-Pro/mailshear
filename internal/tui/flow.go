package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/scan"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// Backend is everything the flow needs from the rest of the program. It is an
// interface so the screens can be driven in tests without a mail server: the
// real one lives in backend.go and is the only thing that talks to IMAP, the
// database or the audit log.
type Backend interface {
	// HasCredentials reports whether a password is already stored for the
	// account, so the flow knows whether to open on the setup screen.
	HasCredentials(a config.Account) bool
	// SaveSetup writes the config file and stores the password. It returns
	// the account as it was saved. An account on auth: oauth has no password
	// to store; OAuthLogin is what gives it a credential.
	SaveSetup(a config.Account, password string) (config.Account, error)
	// OAuthLogin runs the browser sign-in and stores the token. onURL is
	// called with the authorization URL before the browser opens, so the
	// screen can show it for copying.
	OAuthLogin(ctx context.Context, a config.Account, onURL func(string)) error
	// AuthStatus describes the credential stored for an account, for the
	// account screen.
	AuthStatus(a config.Account) AuthStatus
	// SignOut deletes the stored OAuth token. It does not revoke access at
	// the provider, which is done from the provider's own account page.
	SignOut(a config.Account) error
	// Connect dials the server and opens the local database.
	Connect(ctx context.Context, a config.Account) error
	// Scan performs the incremental header scan. It changes nothing on the
	// server. onProgress may be called from Scan's own goroutine.
	Scan(ctx context.Context, onProgress func(scan.Progress)) (scan.Summary, error)
	// Load reads the sender groups and the protected key set.
	Load(ctx context.Context) ([]store.SenderGroup, map[string]bool, error)
	// Protect marks sender keys protected in the database.
	Protect(ctx context.Context, keys []string) error
	// WritePlan records the decisions and writes the plan file, returning the
	// plan and the path it was written to.
	WritePlan(ctx context.Context, groups []store.SenderGroup, decisions []store.Decision) (*plan.Plan, string, error)
	// Prepare validates the plan and previews the work. It changes nothing.
	Prepare(ctx context.Context, p *plan.Plan) (apply.Preview, error)
	// Execute runs the plan Prepare last previewed.
	Execute(ctx context.Context, opts apply.ExecOptions) (apply.Summary, error)
	// Undo moves a run's deletions back out of Trash.
	Undo(ctx context.Context, runID string) (moved, missing int, err error)

	// ListAccounts describes every configured account for the accounts
	// screen: how it authenticates, what credential is stored, and what the
	// database holds for it. It connects to nothing.
	ListAccounts(ctx context.Context) ([]AccountStatus, error)
	// AddAccount writes a new account into the config file, keeping the ones
	// already there, and stores its password. The name must be unused.
	AddAccount(a config.Account, password string) (config.Account, error)
	// RemoveAccount drops the account from the config and deletes its stored
	// credentials. Mail is never touched; the database rows are kept unless
	// purgeData is set.
	RemoveAccount(ctx context.Context, a config.Account, purgeData bool) error
	// Runs lists previous apply runs for the account, newest first.
	Runs(ctx context.Context, a config.Account) ([]RunInfo, error)
	// RunDetail reads one run's audit log back: what moved, what was skipped
	// and why, and which senders were left needing a browser.
	RunDetail(ctx context.Context, runID string) (RunDetail, error)
	// Purge drops the cached message rows for the account, keeping the
	// sender decisions. Nothing on the server changes.
	Purge(ctx context.Context, a config.Account) error
	// ResetFolders clears the folder cursors so the next scan starts over.
	ResetFolders(ctx context.Context, a config.Account) error
	// Export writes the current sender groups to a CSV and a JSON file and
	// returns both paths.
	Export(ctx context.Context, a config.Account, groups []store.SenderGroup) (csvPath, jsonPath string, err error)
	// ListProtected lists the protected senders: the database rows the TUI
	// can remove, and the config entries it cannot.
	ListProtected(ctx context.Context, a config.Account) ([]ProtectedEntry, error)
	// Unprotect clears one protected sender in the database.
	Unprotect(ctx context.Context, a config.Account, key string) error
	// Paths reports where this build keeps its config and data.
	Paths() PathInfo
}

// AccountStatus is one row of the accounts screen.
type AccountStatus struct {
	Account config.Account
	// Provider and Auth are display labels: "Gmail / Google Workspace" and
	// "app password" or "OAuth".
	Provider string
	Auth     string
	// Credential says what is stored: "password stored", "expires ...", or
	// "missing".
	Credential string
	LastScan   time.Time
	Messages   int
	// StillSending counts senders that kept mailing after an unsubscribe.
	StillSending int
	// Err explains a row that could not be read; empty when nothing is wrong.
	Err string
}

// RunInfo is one row of the history screen.
type RunInfo struct {
	ID        string
	StartedAt time.Time
	Summary   string
}

// ManualLink is one sender a run could not finish on its own.
type ManualLink struct {
	Display string
	Status  string
	URL     string
}

// RunDetail is a previous run read back out of its audit log.
type RunDetail struct {
	RunInfo
	AuditPath string
	// Moved is how many messages the run confirmed into Trash.
	Moved int
	// Skipped and Unsub are counts by reason and by unsubscribe status.
	Skipped map[string]int
	Unsub   map[string]int
	Manual  []ManualLink
	// Err explains an audit log that could not be read.
	Err string
}

// ProtectedEntry is one protected sender and where the protection came from.
type ProtectedEntry struct {
	Key string
	// Source is "store" for a database row, or "config domain", "config
	// address" or "config list-id" for a config entry, which is read-only.
	Source string
}

// PathInfo is what the "version and paths" panel shows.
type PathInfo struct {
	Version    string
	ConfigPath string
	DataDir    string
	DBPath     string
	DBSize     int64
}

// AuthStatus is what the account screen says about a stored credential.
type AuthStatus struct {
	HasPassword bool
	// Provider names the OAuth provider, empty for a password account.
	Provider string
	HasToken bool
	Expiry   time.Time
	// Err explains a credential that cannot be read or a config block that
	// does not resolve; empty when there is nothing wrong.
	Err string
}

// FlowDeps is what RunFlow needs to start.
type FlowDeps struct {
	// Cfg is the loaded configuration, or nil when there is none yet.
	Cfg *config.Config
	// ConfigPath and DataDir are shown on the setup screen so the user can
	// see where their answers will land.
	ConfigPath string
	DataDir    string
	// Account names the account to work on; empty means the only one, or
	// the accounts screen when there is more than one.
	AccountName string
	Backend     Backend
	// Version is the build version, shown in the header and on the
	// maintenance screen's paths panel.
	Version string
	// OpenURL hands an unsubscribe link to the system browser. May be nil.
	OpenURL func(string) error
	// NoDelete and NoUnsubscribe pre-set the confirm screen's toggles, so
	// a caller can open a run that only unsubscribes.
	NoDelete      bool
	NoUnsubscribe bool
}

// FlowResult is what the whole session ended up doing.
type FlowResult struct {
	// Applied reports that a plan was executed.
	Applied bool
	RunID   string
	// Manual counts senders that still need a browser after the run.
	Manual int
	// PlanPath is the plan file the review wrote, if any.
	PlanPath string
}

type screen int

const (
	screenSetup screen = iota
	screenScan
	screenReview
	screenConfirm
	screenApply
	screenResults
	// The screens below sit beside the pipeline rather than in it, so they
	// are left out of the step trail.
	screenAccounts
	screenHistory
	screenMenu
)

func (s screen) String() string {
	switch s {
	case screenSetup:
		return "setup"
	case screenScan:
		return "scan"
	case screenReview:
		return "review"
	case screenConfirm:
		return "confirm"
	case screenApply:
		return "apply"
	case screenAccounts:
		return "accounts"
	case screenHistory:
		return "history"
	case screenMenu:
		return "maintenance"
	default:
		return "results"
	}
}

type flowModel struct {
	deps   FlowDeps
	styles styles

	screen screen
	width  int
	height int

	acct config.Account

	setup    setupModel
	scan     scanModel
	review   model
	confirm  confirmModel
	apply    applyModel
	results  resultsModel
	accounts accountsModel
	history  historyModel
	menu     menuModel

	// back is where esc returns from a screen beside the pipeline. It is
	// screenReview once there is a review to go back to.
	back screen

	groups   []store.SenderGroup
	planPath string

	// ctx is the session context; opCancel cancels whatever long-running
	// command is in flight, so ctrl+c stops the work before it quits.
	ctx      context.Context
	opCancel context.CancelFunc
	// interrupted records that ctrl+c already cancelled once: the next press
	// quits.
	interrupted bool

	notice string
	result FlowResult
}

// RunFlow drives the whole session: setup, scan, review, confirm, apply,
// results. It restores the terminal on every exit path, including a panic
// raised outside Bubble Tea's own recovery.
func RunFlow(ctx context.Context, deps FlowDeps) (FlowResult, error) {
	p := tea.NewProgram(newFlowModel(ctx, deps))

	defer func() {
		if r := recover(); r != nil {
			p.Kill()
			panic(r)
		}
	}()

	final, runErr := p.Run()
	if runErr != nil {
		return FlowResult{}, fmt.Errorf("tui: %w", runErr)
	}
	fm, ok := final.(flowModel)
	if !ok {
		return FlowResult{}, fmt.Errorf("tui: unexpected final model %T", final)
	}
	return fm.result, nil
}

func newFlowModel(ctx context.Context, deps FlowDeps) flowModel {
	st := newStyles()
	m := flowModel{
		deps:   deps,
		styles: st,
		ctx:    ctx,
		width:  minWidth,
		height: 24,
	}

	// Any configured account and nothing naming which: the accounts screen is
	// the landing screen, so adding or switching mailboxes is always in view.
	// Only a fresh install (no usable account) goes straight to setup.
	if deps.AccountName == "" && deps.Cfg != nil && len(deps.Cfg.Accounts) > 0 && !needsSetup(deps) {
		m.acct = deps.Cfg.Accounts[0]
		m.screen = screenAccounts
		m.back = screenAccounts
		m.accounts = newAccountsModel(st, deps.ConfigPath)
		m.accounts.notice = configNotice(deps.Cfg)
		return m
	}

	acct, needSetup := resolveAccount(deps)
	m.acct = acct
	m.back = screenAccounts
	if needSetup {
		m.screen = screenSetup
		m.setup = newSetupModel(st, acct, deps.ConfigPath)
		return m
	}
	m.screen = screenScan
	m.scan = newScanModel(st, acct)
	return m
}

// resolveAccount picks the account to work on and reports whether the setup
// screen has to run first: no config, no such account, the placeholder
// username from the shipped example, or no stored password.
func resolveAccount(deps FlowDeps) (config.Account, bool) {
	blank := config.Account{Name: "personal", Auth: "file"}
	if deps.Cfg == nil {
		return blank, true
	}
	acct, err := deps.Cfg.Account(deps.AccountName)
	if err != nil {
		return blank, true
	}
	if acct.Username == "" || acct.Username == placeholderUsername {
		return acct, true
	}
	if deps.Backend != nil && !deps.Backend.HasCredentials(acct) {
		return acct, true
	}
	return acct, false
}

// needsSetup reports whether no account is usable yet (fresh install), in
// which case the setup form is the landing screen instead of the accounts list.
func needsSetup(deps FlowDeps) bool {
	if deps.Cfg == nil {
		return true
	}
	for _, a := range deps.Cfg.Accounts {
		if a.Username == "" || a.Username == placeholderUsername {
			continue
		}
		if deps.Backend != nil && !deps.Backend.HasCredentials(a) {
			continue
		}
		return false
	}
	return true
}

// placeholderUsername is the address in the shipped example config. An
// account still carrying it has never been set up.
const placeholderUsername = "you@gmail.com"

// configNotice is the load-time config warnings as the one line the accounts
// screen has room for: an auth method the provider has withdrawn, a protect
// list that is still the shipped example. It is a notice rather than an
// error, and the next keypress clears it.
func configNotice(cfg *config.Config) string {
	if cfg == nil || len(cfg.Warnings) == 0 {
		return ""
	}
	return strings.Join(cfg.Warnings, "; ")
}

func (m flowModel) Init() tea.Cmd {
	switch m.screen {
	case screenSetup:
		return nil
	case screenScan:
		return tea.Batch(m.scan.spinner.Tick, func() tea.Msg { return beginScanMsg{} })
	case screenAccounts:
		return m.loadAccounts()
	}
	return nil
}

// elapsed formats a duration as mm:ss, which is all a scan or an apply needs.
func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}
