package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/creds"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/scan"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// fakeBackend stands in for IMAP, the database and the audit log. Commands
// run on their own goroutines, so every field is behind the mutex.
type fakeBackend struct {
	mu sync.Mutex

	hasCreds bool
	groups   []store.SenderGroup

	savedAcct   config.Account
	savedPW     string
	oauthLogins []config.Account
	oauthErr    error
	oauthBlock  bool
	authStatus  AuthStatus
	signedOut   int
	signOutErr  error

	connected []config.Account
	protected []string
	planned   []store.Decision

	execOpts apply.ExecOptions
	executed int
	undone   []string

	events []apply.Event
	sum    apply.Summary
	scanN  int

	accounts      []AccountStatus
	accountsErr   error
	added         []config.Account
	removed       []string
	purged        bool
	runs          []RunInfo
	protectedList []ProtectedEntry
	unprotected   []string

	purgedMessages int
	resetFolders   int
	exported       int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		hasCreds: true,
		groups:   testGroups(),
		accounts: []AccountStatus{
			{
				Account:  config.Account{Name: "personal", Host: "imap.gmail.com", Port: 993, Username: "me@gmail.com", Auth: "file"},
				Provider: "Gmail / Google Workspace", Auth: "app password",
				Credential: "password stored", Messages: 4200, StillSending: 2,
			},
			{
				Account:  config.Account{Name: "work", Host: "outlook.office365.com", Port: 993, Username: "me@work.example", Auth: "oauth"},
				Provider: "Outlook / Microsoft 365", Auth: "OAuth",
				Credential: "missing",
			},
		},
		runs: []RunInfo{
			{ID: "20260906-120000-aa11", Summary: "moved 100 message(s)"},
			{ID: "20260901-090000-bb22", Summary: "moved 12 message(s)"},
		},
		protectedList: []ProtectedEntry{
			{Key: "addr:alerts@bank.example", Source: "store"},
			{Key: "bank.example", Source: "config domain"},
		},
		events: []apply.Event{
			{Phase: apply.PhaseUnsubscribe, SenderKey: "addr:news@example.com", Display: "Example News",
				Method: "oneclick", Status: string(unsub.StatusOK), HTTPStatus: 200,
				URI: "https://example.com/u/news"},
			{Phase: apply.PhaseUnsubscribe, SenderKey: "addr:deals@example.com", Display: "Example Deals",
				Method: "http", Status: string(unsub.StatusProbable), HTTPStatus: 200,
				URI: "https://example.com/u/deals", FinalURL: "https://example.com/confirm?t=1"},
			{Phase: apply.PhaseUnsubscribe, SenderKey: "addr:spam@spam.example", Display: "Persistent",
				Method: "none", Status: string(unsub.StatusManual), Err: "no unsubscribe uri"},
			{Phase: apply.PhaseDelete, Folder: "INBOX", Moved: 100, TotalMoved: 100},
			{Phase: apply.PhaseDone, TotalMoved: 100},
		},
		sum: apply.Summary{
			RunID: "20260906-120000-aa11",
			Unsub: map[unsub.Status]int{
				unsub.StatusOK: 1, unsub.StatusProbable: 1, unsub.StatusManual: 1,
			},
			Moved:          100,
			SkippedReasons: map[string]int{"flagged": 1},
			AuditPath:      "/tmp/audit.jsonl",
		},
	}
}

func (f *fakeBackend) HasCredentials(a config.Account) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	// An account the list reports as having no credential has none, whatever
	// the default is: that is what sends the accounts screen to the form.
	for _, row := range f.accounts {
		if row.Account.Name == a.Name && row.Credential == "missing" {
			return false
		}
	}
	return f.hasCreds
}

func (f *fakeBackend) SaveSetup(a config.Account, password string) (config.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.savedAcct, f.savedPW, f.hasCreds = a, password, true
	return a, nil
}

func (f *fakeBackend) OAuthLogin(ctx context.Context, a config.Account, onURL func(string)) error {
	f.mu.Lock()
	f.oauthLogins = append(f.oauthLogins, a)
	err := f.oauthErr
	block := f.oauthBlock
	f.mu.Unlock()

	if onURL != nil {
		onURL("https://auth.example.com/authorize?client_id=cid&state=st")
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeBackend) AuthStatus(config.Account) AuthStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authStatus
}

func (f *fakeBackend) SignOut(config.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signedOut++
	f.authStatus = AuthStatus{}
	return f.signOutErr
}

func (f *fakeBackend) signOutCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signedOut
}

func (f *fakeBackend) oauthLoginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.oauthLogins)
}

func (f *fakeBackend) Connect(_ context.Context, a config.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = append(f.connected, a)
	return nil
}

func (f *fakeBackend) Scan(_ context.Context, onProgress func(scan.Progress)) (scan.Summary, error) {
	f.mu.Lock()
	f.scanN++
	f.mu.Unlock()
	onProgress(scan.Progress{Folder: "INBOX", Done: 50, Total: 100, Fetched: 50, Bulk: 10})
	onProgress(scan.Progress{Folder: "INBOX", Done: 100, Total: 100, Fetched: 100, Bulk: 20})
	return scan.Summary{Folders: 1, Fetched: 100, Bulk: 20}, nil
}

func (f *fakeBackend) Load(context.Context) ([]store.SenderGroup, map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groups, map[string]bool{"addr:alerts@bank.example": true}, nil
}

func (f *fakeBackend) Protect(_ context.Context, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.protected = append(f.protected, keys...)
	return nil
}

func (f *fakeBackend) WritePlan(_ context.Context, _ []store.SenderGroup, decisions []store.Decision) (*plan.Plan, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planned = decisions
	return &plan.Plan{RunID: "20260906-120000-aa11", Account: "personal"},
		"/tmp/plans/20260906-120000-aa11.yaml", nil
}

func (f *fakeBackend) Prepare(context.Context, *plan.Plan) (apply.Preview, error) {
	return apply.Preview{
		RunID:   "20260906-120000-aa11",
		Account: "personal",
		Rows: []apply.PreviewRow{
			{SenderKey: "addr:news@example.com", Display: "Example News", Method: "oneclick",
				Unsubscribe: true, DeleteScope: "matched", Messages: 100, Bytes: 5_000_000},
			{SenderKey: "addr:deals@example.com", Display: "Example Deals", Method: "http",
				Unsubscribe: true, DeleteScope: "all", Messages: 50, Bytes: 1_000_000},
		},
		Refused:       []apply.RefusedRow{{SenderKey: "addr:alerts@bank.example", Display: "Bank Alerts", Reason: "protected"}},
		Folders:       []string{"INBOX"},
		Trash:         "[Gmail]/Trash",
		AuditPath:     "/tmp/audit.jsonl",
		Warnings:      []string{"database changed since the plan was written"},
		UnsubCount:    2,
		UnsubByMethod: map[string]int{"oneclick": 1, "http": 1},
		DeleteSenders: 2, DeleteAllSenders: 1,
		Messages: 150, Bytes: 6_000_000,
	}, nil
}

func (f *fakeBackend) Execute(_ context.Context, opts apply.ExecOptions) (apply.Summary, error) {
	f.mu.Lock()
	f.executed++
	f.execOpts = opts
	events := f.events
	sum := f.sum
	f.mu.Unlock()

	for _, e := range events {
		if opts.OnEvent != nil {
			opts.OnEvent(e)
		}
	}
	return sum, nil
}

func (f *fakeBackend) Undo(_ context.Context, runID string) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undone = append(f.undone, runID)
	return 100, 0, nil
}

func (f *fakeBackend) ListAccounts(context.Context) ([]AccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.accountsErr != nil {
		return nil, f.accountsErr
	}
	return append([]AccountStatus(nil), f.accounts...), nil
}

func (f *fakeBackend) AddAccount(a config.Account, password string) (config.Account, error) {
	f.mu.Lock()
	f.added = append(f.added, a)
	f.mu.Unlock()
	return f.SaveSetup(a, password)
}

func (f *fakeBackend) RemoveAccount(_ context.Context, a config.Account, purge bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, a.Name)
	f.purged = purge
	for i, row := range f.accounts {
		if row.Account.Name == a.Name {
			f.accounts = append(f.accounts[:i:i], f.accounts[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeBackend) Runs(context.Context, config.Account) ([]RunInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RunInfo(nil), f.runs...), nil
}

func (f *fakeBackend) RunDetail(_ context.Context, runID string) (RunDetail, error) {
	return RunDetail{
		RunInfo:   RunInfo{ID: runID},
		AuditPath: "/tmp/audit/" + runID + ".jsonl",
		Moved:     100,
		Skipped:   map[string]int{"flagged": 1},
		Unsub:     map[string]int{"ok": 2, "manual": 1},
		Manual:    []ManualLink{{Display: "Persistent", Status: "manual"}},
	}, nil
}

func (f *fakeBackend) Purge(context.Context, config.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgedMessages++
	return nil
}

func (f *fakeBackend) ResetFolders(context.Context, config.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetFolders++
	return nil
}

func (f *fakeBackend) Export(_ context.Context, _ config.Account, groups []store.SenderGroup) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exported = len(groups)
	return "/tmp/exports/personal.csv", "/tmp/exports/personal.json", nil
}

func (f *fakeBackend) ListProtected(context.Context, config.Account) ([]ProtectedEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProtectedEntry(nil), f.protectedList...), nil
}

func (f *fakeBackend) Unprotect(_ context.Context, _ config.Account, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unprotected = append(f.unprotected, key)
	for i, e := range f.protectedList {
		if e.Key == key {
			f.protectedList = append(f.protectedList[:i:i], f.protectedList[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeBackend) Paths() PathInfo {
	return PathInfo{
		Version: "v9.9.9", ConfigPath: "/tmp/config.yaml",
		DataDir: "/tmp/data", DBPath: "/tmp/data/mailshear.db", DBSize: 2048,
	}
}

func (f *fakeBackend) connectedAccounts() []config.Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]config.Account(nil), f.connected...)
}

func (f *fakeBackend) removedAccounts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *fakeBackend) purgedData() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.purged
}

func (f *fakeBackend) undoneRuns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.undone...)
}

func (f *fakeBackend) purgeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.purgedMessages
}

func (f *fakeBackend) exportedGroups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exported
}

func (f *fakeBackend) unprotectedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unprotected...)
}

func (f *fakeBackend) execCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.executed
}

func (f *fakeBackend) options() apply.ExecOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execOpts
}

// harness runs the flow the way Bubble Tea does: commands on their own
// goroutines, messages fed back into Update in arrival order.
type harness struct {
	t    *testing.T
	m    tea.Model
	msgs chan tea.Msg
	quit bool
}

func newHarness(t *testing.T, deps FlowDeps) *harness {
	t.Helper()
	h := &harness{t: t, msgs: make(chan tea.Msg, 64)}
	h.m = newFlowModel(context.Background(), deps)
	h.exec(h.m.Init())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	return h
}

func (h *harness) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			h.msgs <- msg
		}
	}()
}

// settle processes queued messages until nothing has arrived for a moment.
func (h *harness) settle() {
	h.t.Helper()
	for {
		select {
		case msg := <-h.msgs:
			switch mm := msg.(type) {
			case tea.BatchMsg:
				for _, c := range mm {
					h.exec(c)
				}
				continue
			case spinner.TickMsg:
				continue // the spinner would tick forever
			case tea.QuitMsg:
				h.quit = true
				continue
			}
			var cmd tea.Cmd
			h.m, cmd = h.m.Update(msg)
			h.exec(cmd)
		case <-time.After(80 * time.Millisecond):
			return
		}
	}
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	var cmd tea.Cmd
	h.m, cmd = h.m.Update(msg)
	h.exec(cmd)
	h.settle()
}

func (h *harness) key(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		h.send(keyMsg(k))
	}
}

func (h *harness) flow() flowModel {
	h.t.Helper()
	fm, ok := h.m.(flowModel)
	if !ok {
		h.t.Fatalf("model is %T", h.m)
	}
	return fm
}

func testFlowDeps(b Backend) FlowDeps {
	return FlowDeps{
		Cfg: &config.Config{Accounts: []config.Account{{
			Name: "personal", Host: "imap.gmail.com", Port: 993,
			Username: "me@gmail.com", Auth: "file",
		}}},
		ConfigPath:  "/tmp/config.yaml",
		DataDir:     "/tmp/data",
		AccountName: "personal",
		Backend:     b,
	}
}

func TestFlowRunsSetupScanReviewConfirmApplyResults(t *testing.T) {
	fake := newFakeBackend()
	fake.hasCreds = false // force the setup screen

	deps := testFlowDeps(fake)
	h := newHarness(t, deps)
	if got := h.flow().screen; got != screenSetup {
		t.Fatalf("opened on %v, want setup", got)
	}

	// Setup: the email is already filled in from the config, so the provider
	// is detected from the domain; tab past the auth-method picker (Gmail
	// offers OAuth, and App password is its default), type a password and
	// submit.
	h.key("tab", "tab", "tab", "tab")
	if got := h.flow().setup.resolved; got != config.ProviderGmail {
		t.Fatalf("provider resolved to %q, want gmail", got)
	}
	for _, r := range "hunter2" {
		h.key(string(r))
	}
	h.key("enter")
	h.settle()

	fake.mu.Lock()
	savedPW, savedHost := fake.savedPW, fake.savedAcct.Host
	fake.mu.Unlock()
	if savedPW != "hunter2" {
		t.Fatalf("password saved as %q", savedPW)
	}
	if savedHost != "imap.gmail.com" {
		t.Fatalf("host detected as %q, want imap.gmail.com", savedHost)
	}

	// Setup hands over to the scan, which hands over to review.
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("after setup and scan, screen = %v, want review", got)
	}
	if n := len(h.flow().review.groups); n != len(fake.groups) {
		t.Fatalf("review loaded %d groups, want %d", n, len(fake.groups))
	}

	// Review: mark a sender, then w to move on.
	h.key("end", "space", "w")
	h.settle()
	if got := h.flow().screen; got != screenConfirm {
		t.Fatalf("after w, screen = %v, want confirm", got)
	}
	fake.mu.Lock()
	planned := len(fake.planned)
	fake.mu.Unlock()
	if planned == 0 {
		t.Fatal("no decisions reached WritePlan")
	}
	if h.flow().planPath == "" {
		t.Fatal("plan path not recorded")
	}

	// Confirm: apply.
	h.key("enter")
	h.settle()
	if n := fake.execCount(); n != 1 {
		t.Fatalf("Execute called %d times, want 1", n)
	}
	if got := h.flow().screen; got != screenResults {
		t.Fatalf("after apply, screen = %v, want results", got)
	}

	fm := h.flow()
	if fm.result.RunID != "20260906-120000-aa11" || !fm.result.Applied {
		t.Fatalf("result = %+v", fm.result)
	}
	// probable and manual both need a person; ok does not.
	if fm.result.Manual != 2 {
		t.Fatalf("Manual = %d, want 2", fm.result.Manual)
	}
	if len(fm.results.manual) != 2 {
		t.Fatalf("manual list = %d rows", len(fm.results.manual))
	}

	// Undo asks first, then runs.
	h.key("u")
	if !h.flow().results.confirmUndo {
		t.Fatal("u did not raise the undo confirmation")
	}
	h.key("y")
	h.settle()
	fake.mu.Lock()
	undone := append([]string(nil), fake.undone...)
	fake.mu.Unlock()
	if len(undone) != 1 || undone[0] != "20260906-120000-aa11" {
		t.Fatalf("undone = %v", undone)
	}
	if !strings.Contains(h.flow().results.undoNote, "restored 100") {
		t.Fatalf("undo note = %q", h.flow().results.undoNote)
	}
}

func TestConfirmBackOutNeverExecutes(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, testFlowDeps(fake))
	h.settle()

	h.key("end", "space", "w")
	h.settle()
	if got := h.flow().screen; got != screenConfirm {
		t.Fatalf("screen = %v, want confirm", got)
	}

	h.key("esc")
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("esc from confirm went to %v, want review", got)
	}
	// The selection survives the round trip.
	if len(h.flow().review.sel) == 0 {
		t.Fatal("backing out of confirm dropped the selections")
	}
	h.key("q", "y")
	h.settle()

	if n := fake.execCount(); n != 0 {
		t.Fatalf("Execute ran %d times after backing out", n)
	}
}

func TestConfirmTogglesBecomeExecOptions(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, testFlowDeps(fake))
	h.settle()
	h.key("end", "space", "w")
	h.settle()

	h.key("d", "u")
	cm := h.flow().confirm
	if !cm.noDelete || !cm.noUnsubscribe {
		t.Fatalf("toggles = delete %v unsubscribe %v", cm.noDelete, cm.noUnsubscribe)
	}
	if _, live := cm.actionText(cm.preview.Rows[0]); live {
		t.Fatal("a row is still live with both phases toggled off")
	}

	h.key("enter")
	h.settle()
	opts := fake.options()
	if !opts.NoDelete || !opts.NoUnsubscribe {
		t.Fatalf("ExecOptions = %+v, want both set", opts)
	}
}

func TestFlowPrefersConfiguredToggles(t *testing.T) {
	fake := newFakeBackend()
	deps := testFlowDeps(fake)
	deps.NoDelete = true
	h := newHarness(t, deps)
	h.settle()
	h.key("end", "space", "w")
	h.settle()

	if !h.flow().confirm.noDelete {
		t.Fatal("--no-delete did not reach the confirm screen")
	}
}

func TestManualListPicksTheFollowUps(t *testing.T) {
	events := []apply.Event{
		{Display: "Done", Method: "oneclick", Status: string(unsub.StatusOK), URI: "https://a.example/u"},
		{Display: "Maybe", Method: "http", Status: string(unsub.StatusProbable),
			URI: "https://b.example/u", FinalURL: "https://b.example/confirm"},
		{Display: "Byhand", Method: "none", Status: string(unsub.StatusManual)},
		{Display: "Broke", Method: "http", Status: string(unsub.StatusFailed), URI: "https://c.example/u", Err: "timeout"},
	}
	got := manualFrom(events)
	if len(got) != 3 {
		t.Fatalf("manualFrom returned %d rows, want 3 (ok is not a follow-up)", len(got))
	}
	// Failures first, then manual, then probable.
	want := []string{"Broke", "Byhand", "Maybe"}
	for i, w := range want {
		if got[i].display != w {
			t.Fatalf("row %d = %q, want %q", i, got[i].display, w)
		}
	}
	if got[2].url != "https://b.example/confirm" {
		t.Fatalf("probable row url = %q, want the final URL", got[2].url)
	}
	if len(got[2].uris) != 2 {
		t.Fatalf("probable row uris = %v, want both the header URI and the final URL", got[2].uris)
	}
}

func TestFlowViewRendersEveryScreenAtEverySize(t *testing.T) {
	for _, size := range []struct{ w, h int }{{70, 16}, {200, 50}, {40, 10}} {
		fake := newFakeBackend()
		fake.hasCreds = false
		h := newHarness(t, testFlowDeps(fake))
		h.send(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		checkView(t, h.m, size.w, size.h, 0) // setup

		h.key("f2")
		checkView(t, h.m, size.w, size.h, 1) // setup, advanced fields

		h.key("enter")
		checkView(t, h.m, size.w, size.h, 2) // setup, rejected: no password

		h.key("f2", "tab", "tab", "tab")
		for _, r := range "hunter2" {
			h.key(string(r))
		}
		h.key("enter")
		h.settle()
		checkView(t, h.m, size.w, size.h, 3) // review, via setup and scan

		h.key("end", "space", "w")
		h.settle()
		checkView(t, h.m, size.w, size.h, 4) // confirm

		h.key("enter")
		h.settle()
		checkView(t, h.m, size.w, size.h, 5) // results

		h.key("enter")
		checkView(t, h.m, size.w, size.h, 6) // results, detail
	}
}

func checkView(t *testing.T, m tea.Model, w, hgt, step int) {
	t.Helper()
	out := m.View()
	if out.Content == "" {
		t.Fatalf("%dx%d step %d: empty view", w, hgt, step)
	}
	if !out.AltScreen {
		t.Fatalf("%dx%d step %d: view should use the alt screen", w, hgt, step)
	}
	if w < minWidth || hgt < minHeight {
		if !strings.Contains(out.Content, "terminal too small") {
			t.Fatalf("%dx%d step %d: expected the too-small guard", w, hgt, step)
		}
		return
	}
	if n := len(strings.Split(out.Content, "\n")); n != hgt {
		t.Fatalf("%dx%d step %d: rendered %d lines", w, hgt, step, n)
	}
}

func TestScreenViewsRenderAtTheFloorAndWide(t *testing.T) {
	st := newStylesFor(true)
	fake := newFakeBackend()
	pv, _ := fake.Prepare(context.Background(), nil)

	scanning := newScanModel(st, config.Account{Name: "personal", Host: "imap.gmail.com"})
	scanning.connected = true
	scanning.progress = scan.Progress{Folder: "[Gmail]/All Mail", Done: 4200, Total: 9000, Fetched: 4200, Bulk: 980}

	failed := newScanModel(st, config.Account{Name: "personal", Host: "imap.gmail.com"})
	failed.err = errAuth{}

	applying := newApplyModel(st, pv, apply.ExecOptions{})
	for _, e := range fake.events[:4] {
		applying = applying.apply(e)
	}

	for _, size := range []struct{ w, h int }{{70, 16}, {200, 50}} {
		body := size.h - flowChrome

		views := map[string][]string{
			"setup":       newSetupModel(st, config.Account{Name: "personal"}, "/tmp/config.yaml").view(size.w, body),
			"scan":        newScanModel(st, config.Account{Name: "personal", Host: "imap.gmail.com"}).view(size.w, body),
			"scanning":    scanning.view(size.w, body),
			"scan error":  failed.view(size.w, body),
			"confirm":     newConfirmModel(st, pv, false, false).view(size.w, body),
			"confirm off": newConfirmModel(st, pv, true, true).view(size.w, body),
			"apply":       applying.view(size.w, body),
			"results":     newResultsModel(st, fake.sum, "[Gmail]/Trash", fake.events).view(size.w, body),
		}
		for name, lines := range views {
			if len(lines) == 0 {
				t.Fatalf("%s at %dx%d rendered nothing", name, size.w, size.h)
			}
			// More lines than the body means something wrapped inside a
			// bordered panel and pushed the layout down.
			if len(lines) > body {
				t.Fatalf("%s at %dx%d rendered %d lines, body is %d", name, size.w, size.h, len(lines), body)
			}
			for i, l := range lines {
				if got := lineWidth(l); got > size.w {
					t.Fatalf("%s at %dx%d: line %d is %d cells wide", name, size.w, size.h, i, got)
				}
			}
		}
	}
}

func TestScanErrorPanelExplainsGmailLogins(t *testing.T) {
	st := newStylesFor(true)
	sm := newScanModel(st, config.Account{Name: "personal", Host: "imap.gmail.com"})
	sm.err = errAuth{}
	body := strings.Join(sm.errorView(100, 20), "\n")
	if !strings.Contains(body, "app password") {
		t.Fatalf("gmail login hint missing:\n%s", body)
	}
	if !strings.Contains(sm.help(), "retry") {
		t.Fatalf("error footer = %q", sm.help())
	}
}

type errAuth struct{}

func (errAuth) Error() string { return "login failed: invalid credentials" }

// lineWidth measures a rendered line in display cells, ignoring escape codes.
func lineWidth(s string) int { return lipgloss.Width(s) }

// setupAt returns a setup model with an address already typed and the focus
// on the given field.
func setupAt(email string, focus int) setupModel {
	m := newSetupModel(newStylesFor(true), config.Account{Name: "personal"}, "/tmp/config.yaml")
	m.focus = fEmail
	for _, r := range email {
		m, _ = m.update(keyMsg(string(r)))
	}
	m.focus = focus
	return m
}

func TestSetupPickerOverridesDetectionForACustomDomain(t *testing.T) {
	// A custom domain the table cannot place leaves the provider unresolved
	// rather than quietly guessing a generic host.
	m := setupAt("alex@example.com", fEmail)
	if m.resolved != "" {
		t.Fatalf("custom domain resolved to %q without a lookup", m.resolved)
	}

	// Picking Gmail fills the whole provider block in from config.Defaults.
	m.focus = fProvider
	m, _ = m.update(keyMsg("right"))
	if m.resolved != config.ProviderGmail {
		t.Fatalf("one right press resolved to %q, want gmail", m.resolved)
	}
	if got := m.fields[fHost].value; got != "imap.gmail.com" {
		t.Fatalf("host = %q", got)
	}
	if got := m.fields[fFolders].value; got != "[Gmail]/All Mail" {
		t.Fatalf("folders = %q", got)
	}
	if got := m.fields[fSMTPHost].value; got != "smtp.gmail.com" {
		t.Fatalf("smtp host = %q", got)
	}
	if !strings.HasPrefix(m.note, "selected Gmail / Google Workspace: imap.gmail.com:993, folders") {
		t.Fatalf("note = %q", m.note)
	}

	// space cycles as well, and left goes back to auto-detect.
	m, _ = m.update(keyMsg("space"))
	if m.resolved != config.ProviderFastmail {
		t.Fatalf("space resolved to %q, want fastmail", m.resolved)
	}
	m, _ = m.update(keyMsg("left"))
	m, _ = m.update(keyMsg("left"))
	if m.choice != autoChoice {
		t.Fatalf("choice = %d, want auto-detect", m.choice)
	}
}

func TestSetupMXLookupResolvesAndSubmits(t *testing.T) {
	m := setupAt("alex@example.com", fEmail)

	// Leaving the address field starts the lookup, which never blocks Update.
	m, cmd := m.update(keyMsg("tab"))
	if cmd == nil {
		t.Fatal("tabbing off the address started no MX lookup")
	}
	if !m.looking || !strings.Contains(m.note, "looking up MX for example.com") {
		t.Fatalf("looking = %v, note = %q", m.looking, m.note)
	}

	m, _ = m.mxResult(mxResultMsg{seq: m.mxSeq, domain: "example.com", p: config.ProviderGmail, ok: true})
	want := "detected Gmail / Google Workspace via MX: imap.gmail.com:993, folders [Gmail]/All Mail"
	if m.note != want {
		t.Fatalf("note = %q, want %q", m.note, want)
	}
	if m.resolved != config.ProviderGmail || m.looking {
		t.Fatalf("resolved = %q, looking = %v", m.resolved, m.looking)
	}

	// A stale answer from a superseded lookup is ignored.
	m, _ = m.mxResult(mxResultMsg{seq: m.mxSeq - 1, domain: "old.example", p: config.ProviderICloud, ok: true})
	if m.resolved != config.ProviderGmail {
		t.Fatalf("a stale MX answer overwrote the provider: %q", m.resolved)
	}
}

func TestSetupMXMissBecomesGenericWithTheAdvancedFields(t *testing.T) {
	m := setupAt("alex@example.com", fEmail)
	m, _ = m.update(keyMsg("tab"))
	m, _ = m.mxResult(mxResultMsg{seq: m.mxSeq, domain: "example.com", ok: false})

	if m.resolved != config.ProviderGeneric {
		t.Fatalf("resolved = %q, want generic", m.resolved)
	}
	if got := m.fields[fHost].value; got != "imap.example.com" {
		t.Fatalf("host = %q", got)
	}
	if !m.advanced {
		t.Fatal("generic IMAP did not reveal the advanced fields")
	}
	if !strings.Contains(m.note, "no provider matched the MX records for example.com") {
		t.Fatalf("note = %q", m.note)
	}
}

func TestSetupEnterWaitsForTheLookupThenSaves(t *testing.T) {
	m := setupAt("alex@example.com", fEmail)
	m.fields[fPassword].value = "hunter2"

	// Enter on an unresolved provider starts the lookup instead of failing.
	m, cmd := m.update(keyMsg("enter"))
	if cmd == nil || !m.submitAfterMX {
		t.Fatalf("enter did not start the lookup: cmd %v, pending %v", cmd != nil, m.submitAfterMX)
	}

	m, cmd = m.mxResult(mxResultMsg{seq: m.mxSeq, domain: "example.com", p: config.ProviderGmail, ok: true})
	if cmd == nil {
		t.Fatal("the resolved lookup did not submit the form")
	}
	sub, ok := cmd().(setupSubmitMsg)
	if !ok {
		t.Fatalf("command produced %T, want setupSubmitMsg", cmd())
	}
	if sub.acct.Host != "imap.gmail.com" || sub.password != "hunter2" {
		t.Fatalf("submitted %+v with password %q", sub.acct, sub.password)
	}
}

func TestSetupProtonShowsTheBridgeCaveat(t *testing.T) {
	m := setupAt("me@proton.me", fEmail)
	if m.resolved != config.ProviderProton || !m.proton {
		t.Fatalf("resolved = %q, proton = %v", m.resolved, m.proton)
	}
	if !strings.Contains(m.note, "Proton Bridge") {
		t.Fatalf("note = %q", m.note)
	}
	// Proton has no OAuth path here, so the form is the password fields plus
	// every advanced one.
	want := []int{fName, fEmail, fProvider, fPassword, fHost, fPort, fFolders, fSMTPHost, fSMTPPort}
	if got := m.visible(); len(got) != len(want) {
		t.Fatalf("proton shows %v, want %v", got, want)
	}
	// No host to guess, so the form refuses to save until one is typed.
	if _, _, err := m.account(); err == nil {
		t.Fatal("proton saved without a host")
	}
}

func TestSetupPasteFillsThePasswordField(t *testing.T) {
	m := setupAt("alex@example.com", fPassword)
	m.focus = fPassword

	m, _ = m.paste("wxyz-abcd-efgh-ijkl")
	if got := m.fields[fPassword].value; got != "wxyz-abcd-efgh-ijkl" {
		t.Fatalf("password = %q", got)
	}
	shown := m.fields[fPassword].render(m.styles, false, 40)
	if n := strings.Count(shown, "*"); n != 19 {
		t.Fatalf("mask shows %d asterisks, want 19", n)
	}
}

func TestSetupPasteDropsNewlinesAndGmailSpaces(t *testing.T) {
	m := setupAt("me@gmail.com", fPassword)
	m.focus = fPassword
	if m.resolved != config.ProviderGmail {
		t.Fatalf("resolved = %q, want gmail", m.resolved)
	}

	// A password manager hands over a trailing newline; Gmail shows its app
	// passwords in groups of four, and creds.Normalize drops the spaces.
	m, _ = m.paste("wxyz abcd efgh ijkl\r\n")
	if got := m.fields[fPassword].value; got != "wxyzabcdefghijkl" {
		t.Fatalf("password = %q", got)
	}
	if got := creds.Normalize(config.Account{Host: "imap.gmail.com"}, "wxyz abcd efgh ijkl"); got != m.fields[fPassword].value {
		t.Fatalf("field holds %q, creds.Normalize would store %q", m.fields[fPassword].value, got)
	}
}

func TestSetupAcceptsAMultiRuneKeyPress(t *testing.T) {
	m := setupAt("alex@example.com", fPassword)
	m.focus = fPassword

	// Terminals without bracketed paste deliver a paste as one burst.
	m, _ = m.update(tea.KeyPressMsg{Code: 'w', Text: "wxyz-abcd\nefgh"})
	if got := m.fields[fPassword].value; got != "wxyz-abcdefgh" {
		t.Fatalf("password = %q", got)
	}
}

func TestFlowRoutesPasteToTheSetupForm(t *testing.T) {
	fake := newFakeBackend()
	fake.hasCreds = false
	h := newHarness(t, testFlowDeps(fake))
	h.key("tab", "tab", "tab", "tab")
	h.send(tea.PasteMsg{Content: "hunter2\n"})

	if got := h.flow().setup.fields[fPassword].value; got != "hunter2" {
		t.Fatalf("pasted password = %q", got)
	}
}

func TestReviewFilterTakesAPaste(t *testing.T) {
	m := start(t, Options{})
	m = press(t, m, "/")
	tm, _ := m.Update(tea.PasteMsg{Content: "linked\nin"})
	m = tm.(model)
	if m.filterDraft != "linkedin" {
		t.Fatalf("filter draft = %q", m.filterDraft)
	}
}

func TestSetupOffersOAuthOnlyWhereItIsKnown(t *testing.T) {
	cases := []struct {
		email string
		want  bool
	}{
		{"me@gmail.com", true},
		{"me@outlook.com", true},
		{"me@fastmail.com", false},
		{"me@icloud.com", false},
		{"me@proton.me", false},
	}
	for _, tc := range cases {
		m := setupAt(tc.email, fEmail)
		if got := m.oauthOffered(); got != tc.want {
			t.Errorf("%s: oauthOffered = %v, want %v (resolved %q)", tc.email, got, tc.want, m.resolved)
		}
		if has := contains(m.visible(), fAuthMethod); has != tc.want {
			t.Errorf("%s: auth-method field shown = %v, want %v", tc.email, has, tc.want)
		}
	}
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestSetupOAuthRevealsTheClientFields(t *testing.T) {
	// Google offers a client secret; a Microsoft public client must not send
	// one, so the field is not offered.
	google := setupAt("me@gmail.com", fEmail)
	google.authChoice = authOAuth
	if !contains(google.visible(), fClientID) || !contains(google.visible(), fClientSecret) {
		t.Fatalf("google oauth fields = %v", google.visible())
	}
	if contains(google.visible(), fPassword) {
		t.Fatal("the app password field is still shown under OAuth")
	}

	ms := setupAt("me@outlook.com", fEmail)
	ms.authChoice = authOAuth
	if !contains(ms.visible(), fClientID) {
		t.Fatalf("microsoft oauth fields = %v", ms.visible())
	}
	if contains(ms.visible(), fClientSecret) {
		t.Fatal("a client secret field was offered for a Microsoft public client")
	}
}

func TestSetupOAuthAccountNeedsAClientID(t *testing.T) {
	m := setupAt("me@gmail.com", fEmail)
	m.authChoice = authOAuth

	if _, _, err := m.account(); err == nil {
		t.Fatal("the form saved an OAuth account with no client id")
	}

	m.fields[fClientID].set("abc.apps.googleusercontent.com")
	m.fields[fClientSecret].set("shh")
	acct, password, err := m.account()
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if acct.Auth != "oauth" || password != "" {
		t.Fatalf("account auth = %q, password = %q", acct.Auth, password)
	}
	if acct.OAuth == nil || acct.OAuth.Provider != "google" ||
		acct.OAuth.ClientID != "abc.apps.googleusercontent.com" || acct.OAuth.ClientSecret != "shh" {
		t.Fatalf("oauth block = %+v", acct.OAuth)
	}
	if _, err := acct.OAuthSpec(); err != nil {
		t.Fatalf("the saved account does not resolve a provider spec: %v", err)
	}
}

func TestSetupAuthMethodPickerCyclesAndClampsFocus(t *testing.T) {
	m := setupAt("me@gmail.com", fEmail)
	// Move onto the auth-method picker and switch it to OAuth.
	m.focus = 3
	m, _ = m.update(keyMsg("right"))
	if !m.useOAuth() {
		t.Fatal("right on the auth-method picker did not select OAuth")
	}
	m, _ = m.update(keyMsg("left"))
	if m.useOAuth() {
		t.Fatal("left did not go back to the app password")
	}

	// Sitting on the last field and switching to OAuth (which drops the
	// password field and adds two) must leave the focus on a real field.
	m.focus = len(m.visible()) - 1
	m.authChoice = authOAuth
	m.clampFocus()
	if m.focus >= len(m.visible()) {
		t.Fatalf("focus %d is off the end of %d visible fields", m.focus, len(m.visible()))
	}
}

// oauthAccount is the account oauthFlowDeps configures.
func oauthAccount() config.Account {
	return config.Account{
		Name: "personal", Host: "imap.gmail.com", Port: 993,
		Username: "me@gmail.com", Auth: "oauth",
		OAuth: &config.OAuth{Provider: "google", ClientID: "cid"},
	}
}

// oauthFlowDeps is testFlowDeps with the account already on auth: oauth. The
// backend's account list has to agree with it: the accounts screen is what
// hands the form its account.
func oauthFlowDeps(b Backend) FlowDeps {
	deps := testFlowDeps(b)
	deps.Cfg = &config.Config{Accounts: []config.Account{oauthAccount()}}
	if f, ok := b.(*fakeBackend); ok {
		f.accounts = []AccountStatus{{
			Account: oauthAccount(), Provider: "Gmail / Google Workspace",
			Auth: "OAuth", Credential: "token stored",
		}}
	}
	return deps
}

func TestFlowRunsTheBrowserSignInAfterSavingAnOAuthAccount(t *testing.T) {
	fake := newFakeBackend()
	fake.hasCreds = false

	h := newHarness(t, oauthFlowDeps(fake))
	if got := h.flow().screen; got != screenSetup {
		t.Fatalf("opened on %v, want setup", got)
	}
	if !h.flow().setup.useOAuth() {
		t.Fatal("an auth: oauth account did not open on the OAuth method")
	}

	h.key("enter")
	h.settle()

	if n := fake.oauthLoginCount(); n != 1 {
		t.Fatalf("OAuthLogin called %d times, want 1", n)
	}
	fake.mu.Lock()
	savedAuth := fake.savedAcct.Auth
	savedPW := fake.savedPW
	fake.mu.Unlock()
	if savedAuth != "oauth" {
		t.Fatalf("saved auth = %q", savedAuth)
	}
	if savedPW != "" {
		t.Fatal("a password was stored for an OAuth account")
	}

	// The sign-in succeeded, so the flow moved straight on to the scan and
	// then to review.
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("after the sign-in, screen = %v, want review", got)
	}
}

func TestFlowShowsTheAuthorizationURLAndCanCancelIt(t *testing.T) {
	fake := newFakeBackend()
	fake.hasCreds = false
	fake.oauthBlock = true // the sign-in waits for the context

	h := newHarness(t, oauthFlowDeps(fake))
	h.key("enter")
	h.settle()

	fm := h.flow()
	if !fm.setup.signingIn {
		t.Fatal("the setup screen is not waiting on the browser")
	}
	if fm.setup.authURL == "" {
		t.Fatal("the authorization URL was not shown")
	}
	view := strings.Join(fm.setup.view(100, 20), "\n")
	if !strings.Contains(view, "waiting for the browser sign-in") {
		t.Fatalf("view = %q", view)
	}
	if !strings.Contains(view, "auth.example.com") {
		t.Fatalf("the URL is not on screen: %q", view)
	}

	h.key("c")
	h.settle()

	fm = h.flow()
	if fm.setup.signingIn {
		t.Fatal("c did not stop the sign-in")
	}
	if fm.screen != screenSetup {
		t.Fatalf("screen = %v, want to stay on setup after a cancel", fm.screen)
	}
	if !strings.Contains(fm.setup.err, "cancelled") {
		t.Fatalf("setup error = %q", fm.setup.err)
	}
	if n := fake.execCount(); n != 0 {
		t.Fatalf("Execute ran %d times", n)
	}
}

func TestFlowKeepsSetupOpenWhenTheSignInFails(t *testing.T) {
	fake := newFakeBackend()
	fake.hasCreds = false
	fake.oauthErr = errors.New("oauth: invalid_client: bad client id")

	h := newHarness(t, oauthFlowDeps(fake))
	h.key("enter")
	h.settle()

	fm := h.flow()
	if fm.screen != screenSetup {
		t.Fatalf("screen = %v, want setup", fm.screen)
	}
	if !strings.Contains(fm.setup.err, "invalid_client") {
		t.Fatalf("setup error = %q", fm.setup.err)
	}
	if fm.setup.signingIn {
		t.Fatal("still waiting on the browser after a failure")
	}
}

func TestSetupSignInViewNeverShowsATruncatedURL(t *testing.T) {
	m := setupAt("me@gmail.com", fEmail)
	m.signingIn = true
	m.authURL = "https://accounts.google.com/o/oauth2/v2/auth?client_id=" + strings.Repeat("a", 120) + "&state=xyz"

	joined := strings.Join(m.view(60, 24), "")
	for _, part := range wrapAt(m.authURL, 56) {
		if !strings.Contains(joined, part) {
			t.Fatalf("wrapped URL segment %q is missing from the view", part)
		}
	}
}

func TestReviewOpensTheAccountScreenAndComesBack(t *testing.T) {
	fake := newFakeBackend()
	fake.authStatus = AuthStatus{
		Provider: "google",
		HasToken: true,
		Expiry:   time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	h := newHarness(t, oauthFlowDeps(fake))
	h.settle()
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("screen = %v, want review", got)
	}

	h.key(",")
	h.settle()
	if got := h.flow().screen; got != screenAccounts {
		t.Fatalf("\",\" went to %v, want the accounts screen", got)
	}
	h.key("e")
	h.settle()

	fm := h.flow()
	if fm.screen != screenSetup {
		t.Fatalf("\"e\" went to %v, want the account form", fm.screen)
	}
	if fm.setup.mode != setupEdit {
		t.Fatalf("form mode = %v, want edit", fm.setup.mode)
	}
	line := fm.setup.statusLine()
	if !strings.Contains(line, "google") || !strings.Contains(line, "2030-01-01") {
		t.Fatalf("status line = %q, want the provider and the expiry", line)
	}
	if !strings.Contains(fm.setup.help(), "f3 sign out") {
		t.Fatalf("help = %q, want the sign-out key", fm.setup.help())
	}

	// esc returns to the accounts screen, and again to review.
	h.key("esc")
	h.settle()
	if got := h.flow().screen; got != screenAccounts {
		t.Fatalf("esc from the account form went to %v, want the accounts screen", got)
	}
	h.key("esc")
	h.settle()
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("esc from the accounts screen went to %v, want review", got)
	}
	if h.quit {
		t.Fatal("esc from the account screens quit the program")
	}
}

func TestAccountScreenSignsOut(t *testing.T) {
	fake := newFakeBackend()
	fake.authStatus = AuthStatus{Provider: "google", HasToken: true}

	h := newHarness(t, oauthFlowDeps(fake))
	h.settle()
	h.key(",")
	h.settle()
	h.key("e")
	h.settle()

	h.key("f3")
	h.settle()

	if n := fake.signOutCount(); n != 1 {
		t.Fatalf("SignOut called %d times, want 1", n)
	}
	fm := h.flow()
	if fm.setup.status.HasToken {
		t.Fatal("the account screen still reports a stored token")
	}
	if !strings.Contains(fm.notice, "signed out") {
		t.Fatalf("notice = %q", fm.notice)
	}
	if got := fm.setup.statusLine(); !strings.Contains(got, "no token yet") {
		t.Fatalf("status line = %q", got)
	}
}

func TestAccountKeyDoesNothingInStandaloneReview(t *testing.T) {
	m := start(t, Options{})
	m = press(t, m, ",")
	if m.quitting {
		t.Fatal("\",\" quit the standalone review")
	}
}
