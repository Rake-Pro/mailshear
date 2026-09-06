package tui

import (
	"strings"
	"testing"

	"github.com/Rake-Pro/mailshear/internal/config"
)

// multiAccountDeps configures two mailboxes and names neither, which is what
// puts the accounts screen first.
func multiAccountDeps(f *fakeBackend) FlowDeps {
	deps := testFlowDeps(f)
	deps.AccountName = ""
	deps.Cfg = &config.Config{Accounts: []config.Account{
		f.accounts[0].Account, f.accounts[1].Account,
	}}
	return deps
}

func TestTwoAccountsOpenOnTheSwitcher(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, multiAccountDeps(fake))
	h.settle()

	fm := h.flow()
	if fm.screen != screenAccounts {
		t.Fatalf("opened on %v, want the accounts screen", fm.screen)
	}
	if len(fm.accounts.rows) != 2 {
		t.Fatalf("accounts screen has %d rows, want 2", len(fm.accounts.rows))
	}

	// Enter takes the highlighted account through to the scan.
	h.key("down", "enter")
	h.settle()
	fm = h.flow()
	if fm.acct.Name != "work" {
		t.Fatalf("selected account = %q, want work", fm.acct.Name)
	}
	// The second account has no credential, so the form opens rather than a
	// scan that could only fail.
	if fm.screen != screenSetup || fm.setup.mode != setupEdit {
		t.Fatalf("screen = %v mode = %v, want the account form", fm.screen, fm.setup.mode)
	}
}

func TestAccountsScreenSelectsAnAccountAndScans(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, multiAccountDeps(fake))
	h.settle()

	h.key("enter")
	h.settle()

	fm := h.flow()
	if fm.acct.Name != "personal" {
		t.Fatalf("selected account = %q, want personal", fm.acct.Name)
	}
	if fm.screen != screenReview {
		t.Fatalf("screen = %v, want review after the scan", fm.screen)
	}
	if got := fake.connectedAccounts(); len(got) != 1 || got[0].Name != "personal" {
		t.Fatalf("connected to %v, want personal only", got)
	}
}

func TestAddAccountFormRefusesADuplicateName(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, multiAccountDeps(fake))
	h.settle()

	h.key("n")
	h.settle()
	fm := h.flow()
	if fm.screen != screenSetup || fm.setup.mode != setupAdd {
		t.Fatalf("n opened %v/%v, want the add form", fm.screen, fm.setup.mode)
	}
	if fm.setup.fields[fName].value != "" {
		t.Fatalf("add form opened with the name %q, want it empty", fm.setup.fields[fName].value)
	}

	setup := fm.setup
	setup.fields[fName].value = "personal"
	setup.fields[fEmail].value = "other@gmail.com"
	setup.resolve()
	if _, _, err := setup.account(); err == nil {
		t.Fatal("the add form accepted a name that is already configured")
	} else if !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("error = %v, want it to name the clash", err)
	}
}

func TestAccountsScreenSignsOutAfterConfirming(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, multiAccountDeps(fake))
	h.settle()

	h.key("x")
	if got := h.flow().accounts.confirm; got != confirmSignOut {
		t.Fatalf("confirm = %q, want the sign-out question", got)
	}
	h.key("n")
	if fake.signOutCount() != 0 {
		t.Fatal("answering n still signed out")
	}

	h.key("x", "y")
	h.settle()
	if fake.signOutCount() != 1 {
		t.Fatalf("SignOut called %d times, want 1", fake.signOutCount())
	}
}

func TestAccountsScreenRemovesAnAccountAndOnlyPurgesWhenAsked(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, multiAccountDeps(fake))
	h.settle()

	// r asks twice: the config entry, then the stored data.
	h.key("r")
	if got := h.flow().accounts.confirm; got != confirmRemove {
		t.Fatalf("confirm = %q, want the removal question", got)
	}
	h.key("y")
	if got := h.flow().accounts.confirm; got != confirmPurge {
		t.Fatalf("confirm = %q, want the purge question", got)
	}
	h.key("n")
	h.settle()

	if got := fake.removedAccounts(); len(got) != 1 || got[0] != "personal" {
		t.Fatalf("removed %v, want personal", got)
	}
	if fake.purgedData() {
		t.Fatal("answering n to the purge question still dropped the account's data")
	}
	if n := len(h.flow().accounts.rows); n != 1 {
		t.Fatalf("the list still has %d rows, want 1", n)
	}
}

func TestReviewOpensTheRunHistoryAndUndoesARun(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, testFlowDeps(fake))
	h.settle()
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("screen = %v, want review", got)
	}

	h.key("H")
	h.settle()
	fm := h.flow()
	if fm.screen != screenHistory {
		t.Fatalf("H went to %v, want history", fm.screen)
	}
	if len(fm.history.runs) != 2 {
		t.Fatalf("history has %d runs, want 2", len(fm.history.runs))
	}

	h.key("enter")
	h.settle()
	fm = h.flow()
	if !fm.history.showing || fm.history.detail.Moved != 100 {
		t.Fatalf("detail = %+v, want the audit summary", fm.history.detail)
	}

	h.key("u", "y")
	h.settle()
	if got := fake.undoneRuns(); len(got) != 1 || got[0] != "20260906-120000-aa11" {
		t.Fatalf("undone %v, want the selected run", got)
	}
	if note := h.flow().history.note; !strings.Contains(note, "restored") {
		t.Fatalf("note = %q, want the undo result", note)
	}

	// esc leaves the detail, then the screen.
	h.key("esc")
	if h.flow().history.showing {
		t.Fatal("esc did not leave the run detail")
	}
	h.key("esc")
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("esc from the history went to %v, want review", got)
	}
}

func TestMaintenanceMenuExportsAndPurges(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, testFlowDeps(fake))
	h.settle()

	h.key("m")
	h.settle()
	if got := h.flow().screen; got != screenMenu {
		t.Fatalf("m went to %v, want the maintenance menu", got)
	}

	// Export needs no confirmation and reports both paths.
	h.flow()
	hm := h.flow()
	hm.menu.cursor = actExport
	h.m = hm
	h.key("enter")
	h.settle()
	if notice := h.flow().menu.notice; !strings.Contains(notice, ".csv") || !strings.Contains(notice, ".json") {
		t.Fatalf("notice = %q, want both export paths", notice)
	}
	if fake.exportedGroups() == 0 {
		t.Fatal("Export was called with no sender groups")
	}

	// Purge asks first, then offers a rescan.
	hm = h.flow()
	hm.menu.cursor = actPurge
	h.m = hm
	h.key("enter")
	if got := h.flow().menu.confirm; got != actPurge {
		t.Fatalf("confirm = %d, want the purge question", got)
	}
	h.key("y")
	h.settle()
	if fake.purgeCount() != 1 {
		t.Fatalf("Purge called %d times, want 1", fake.purgeCount())
	}
	if !h.flow().menu.rescanPrompt {
		t.Fatal("the purge did not offer a rescan")
	}
	h.key("y")
	h.settle()
	if got := h.flow().screen; got != screenReview {
		t.Fatalf("answering the rescan prompt went to %v, want a scan through to review", got)
	}
}

func TestProtectedPanelRefusesConfigEntries(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, testFlowDeps(fake))
	h.settle()

	h.key("m")
	h.settle()
	hm := h.flow()
	hm.menu.cursor = actProtected
	h.m = hm
	h.key("enter")
	h.settle()

	fm := h.flow()
	if fm.menu.panel != panelProtected || len(fm.menu.protected) != 2 {
		t.Fatalf("panel = %q with %d entries", fm.menu.panel, len(fm.menu.protected))
	}

	// The config entry is read-only and says so.
	hm = h.flow()
	hm.menu.pCursor = 1
	h.m = hm
	h.key("x")
	h.settle()
	if got := h.flow().menu.notice; !strings.Contains(got, "protect:") {
		t.Fatalf("notice = %q, want the config hint", got)
	}
	if n := len(fake.unprotectedKeys()); n != 0 {
		t.Fatalf("Unprotect was called %d times for a config entry", n)
	}

	// The store entry goes.
	hm = h.flow()
	hm.menu.pCursor = 0
	h.m = hm
	h.key("x")
	h.settle()
	if got := fake.unprotectedKeys(); len(got) != 1 || got[0] != "addr:alerts@bank.example" {
		t.Fatalf("unprotected %v, want the store entry", got)
	}
	if n := len(h.flow().menu.protected); n != 1 {
		t.Fatalf("the list still has %d entries, want 1", n)
	}
}

func TestVersionAndPathsPanel(t *testing.T) {
	fake := newFakeBackend()
	h := newHarness(t, testFlowDeps(fake))
	h.settle()

	h.key("m")
	h.settle()
	hm := h.flow()
	hm.menu.cursor = actPaths
	h.m = hm
	h.key("enter")
	h.settle()

	joined := strings.Join(h.flow().menu.view(100, 20), "\n")
	for _, want := range []string{"v9.9.9", "/tmp/config.yaml", "/tmp/data/mailshear.db", "2.0 KB"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("paths panel is missing %q:\n%s", want, joined)
		}
	}
}

func TestSingleConfiguredAccountLandsOnAccountsScreen(t *testing.T) {
	fake := newFakeBackend()
	deps := testFlowDeps(fake)
	deps.AccountName = "" // nothing named on the command line, one account configured
	h := newHarness(t, deps)
	if got := h.flow().screen; got != screenAccounts {
		t.Fatalf("opened on %v, want accounts", got)
	}
}

func TestFreshInstallLandsOnSetup(t *testing.T) {
	fake := newFakeBackend()
	fake.hasCreds = false
	deps := testFlowDeps(fake)
	deps.AccountName = ""
	h := newHarness(t, deps)
	if got := h.flow().screen; got != screenSetup {
		t.Fatalf("opened on %v, want setup", got)
	}
}
