package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newAccount() Account {
	return Account{Name: "personal", Host: "imap.gmail.com", Port: 993, Username: "me@gmail.com", Auth: "file"}
}

// A config file that exists but cannot be understood is the owner's, and it
// may hold things this program knows nothing about. Overwriting it to make
// room for one account would throw all of that away, so it is reported
// instead, by name.
func TestUpsertAccountRefusesToOverwriteAnUnreadableFile(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unparseable", "accounts: [\n", "does not parse as YAML"},
		{"no accounts key", "protect:\n  domains: []\n", "no accounts: list"},
		{"accounts is not a list", "accounts: nope\n", "accounts: is not a list"},
		{"top level is not a mapping", "- one\n- two\n", "the top level is not a mapping"},
		{"empty", "   \n", "is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := UpsertAccount(p, newAccount(), ExampleProtect())
			if err == nil {
				t.Fatal("UpsertAccount overwrote a file it could not read")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), p) {
				t.Fatalf("error = %v, want it to name %s", err, p)
			}
			got, readErr := os.ReadFile(p)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != tc.body {
				t.Fatalf("the file was rewritten:\n%s", got)
			}
		})
	}
}

// A missing file is the one case that still gets a fresh config written.
func TestUpsertAccountWritesOnlyWhenTheFileIsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := UpsertAccount(p, newAccount(), ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// A fresh install protects nothing until the owner says what to protect. The
// examples are in the file as comments, which is where an example belongs.
func TestWriteInitialWritesAnEmptyProtectListWithCommentedExamples(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteInitial(p, newAccount(), ExampleProtect()); err != nil {
		t.Fatalf("WriteInitial: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := len(cfg.Protect.Domains) + len(cfg.Protect.Addresses) + len(cfg.Protect.ListIDs); n != 0 {
		t.Fatalf("a fresh config protects %d sender(s): %+v", n, cfg.Protect)
	}
	if !cfg.Protect.KeepTransactional {
		t.Fatal("keep_transactional is off on a fresh config")
	}
	if cfg.Protect.Matches("chase.com", "", "") {
		t.Fatal("a shipped example is live in the protect list")
	}

	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`# senders never touched, e.g. ["chase.com", "irs.gov"]`,
		`# e.g. ["alerts@mybank.example"]`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("the rendered config is missing the comment %q:\n%s", want, body)
		}
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("a fresh config warns: %v", cfg.Warnings)
	}
}

// An older install has the examples in its live protect list; the owner is
// told so rather than left to assume the list is theirs.
func TestLoadWarnsAboutTheShippedExampleProtectList(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: personal
    host: imap.gmail.com
    username: me@gmail.com
protect:
  domains: ["chase.com", "fidelity.com", "irs.gov"]
  addresses: ["alerts@mybank.example"]
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "shipped example entries") {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}
	if !strings.Contains(cfg.Warnings[0], p) {
		t.Fatalf("warning = %q, want it to name the config file", cfg.Warnings[0])
	}
}

// Microsoft withdrew app-password IMAP, so a password account on Outlook
// cannot connect. It is a warning, not an error: the file is still valid and
// the owner may be mid-migration.
func TestLoadWarnsAboutAMicrosoftAppPassword(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: work
    host: outlook.office365.com
    username: me@example.com
    auth: file
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load rejected a Microsoft password account: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "app passwords") {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}

	oauthed := writeTemp(t, `
accounts:
  - name: work
    host: outlook.office365.com
    username: me@example.com
    auth: oauth
    oauth:
      provider: microsoft
      client_id: cid
`)
	cfg, err = Load(oauthed)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("an OAuth Microsoft account warned: %v", cfg.Warnings)
	}
}

func TestAccountNameValidation(t *testing.T) {
	good := []string{"personal", "work-2", "a", "a.b_c-D9", strings.Repeat("a", 64)}
	for _, name := range good {
		if !ValidAccountName(name) {
			t.Errorf("ValidAccountName(%q) = false, want true", name)
		}
	}
	bad := []string{"", ".hidden", "../escape", "with space", "slash/es", "back\\slash",
		"unicode-\u00e9", "quote\"", strings.Repeat("a", 65)}
	for _, name := range bad {
		if ValidAccountName(name) {
			t.Errorf("ValidAccountName(%q) = true, want false", name)
		}
	}
}

func TestLoadRejectsABadAccountName(t *testing.T) {
	p := writeTemp(t, `
accounts:
  - name: "../escape"
    host: imap.gmail.com
    username: me@gmail.com
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("Load accepted an account name with a path separator in it")
	}
	if !strings.Contains(err.Error(), "may only use letters") {
		t.Fatalf("error = %v, want the naming rule", err)
	}
}

// An edit rewrites the values in place, so a comment the owner wrote next to
// one of them is still there afterwards.
func TestUpsertAccountKeepsCommentsInsideTheAccountBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := `# mailshear configuration
accounts:
  # the mailbox I actually read
  - name: personal
    host: imap.gmail.com
    port: 993
    username: me@gmail.com
    auth: file
    folders: ["[Gmail]/All Mail"]   # everything, not just the inbox
    smtp:
      host: smtp.gmail.com          # for mailto unsubscribes
      port: 587

protect:
  domains: []
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	edited := newAccount()
	edited.Folders = []string{"INBOX"}
	edited.SMTP = &SMTP{Host: "smtp.gmail.com", Port: 465}
	if err := UpsertAccount(p, edited, ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# mailshear configuration",
		"# the mailbox I actually read",
		"everything, not just the inbox",
		"for mailto unsubscribes",
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("comment %q was lost:\n%s", want, got)
		}
	}

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.Accounts[0]
	if len(a.Folders) != 1 || a.Folders[0] != "INBOX" {
		t.Fatalf("folders = %v, want the edited value", a.Folders)
	}
	if a.SMTP == nil || a.SMTP.Port != 465 {
		t.Fatalf("smtp = %+v, want the edited port", a.SMTP)
	}
}

// Switching an account back to a password drops the oauth: block rather than
// leaving a stale one behind.
func TestUpsertAccountDropsTheOAuthBlockWhenItIsNoLongerUsed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	oauthed := newAccount()
	oauthed.Auth = "oauth"
	oauthed.OAuth = &OAuth{Provider: "google", ClientID: "cid", ClientSecret: "sec"}
	if err := UpsertAccount(p, oauthed, ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	if err := UpsertAccount(p, newAccount(), ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "client_id: cid") {
		t.Fatalf("the oauth block outlived the switch to a password:\n%s", body)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Accounts[0].Auth != "file" || cfg.Accounts[0].OAuth != nil {
		t.Fatalf("account = %+v", cfg.Accounts[0])
	}
}
