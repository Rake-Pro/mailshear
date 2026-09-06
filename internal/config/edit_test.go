package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const twoAccountConfig = `# my mailshear config
accounts:
  - name: personal          # the one I use daily
    host: imap.gmail.com
    port: 993
    username: me@gmail.com
    auth: file
    folders: ["[Gmail]/All Mail"]
  - name: work
    host: outlook.office365.com
    port: 993
    username: me@work.example
    auth: oauth
    oauth:
      provider: microsoft
      client_id: "cid"

protect:
  domains: ["bank.example"]   # never touch these
  keep_transactional: true
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestUpsertAccountReplacesOneAndKeepsTheRest(t *testing.T) {
	p := writeConfig(t, twoAccountConfig)

	a := Account{
		Name: "personal", Host: "imap.gmail.com", Port: 993,
		Username: "new@gmail.com", Auth: "file", Folders: []string{"INBOX"},
	}
	if err := UpsertAccount(p, a, ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("%d accounts after the edit, want 2", len(cfg.Accounts))
	}
	got, err := cfg.Account("personal")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if got.Username != "new@gmail.com" {
		t.Fatalf("username = %q, want the new one", got.Username)
	}
	work, err := cfg.Account("work")
	if err != nil {
		t.Fatalf("the other account is gone: %v", err)
	}
	if work.OAuth == nil || work.OAuth.ClientID != "cid" {
		t.Fatalf("work account lost its oauth block: %+v", work.OAuth)
	}

	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, want := range []string{"# my mailshear config", "# never touch these"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("comment %q was lost:\n%s", want, body)
		}
	}
}

func TestUpsertAccountAppendsANewOne(t *testing.T) {
	p := writeConfig(t, twoAccountConfig)

	a := Account{
		Name: "side", Host: "imap.fastmail.com", Port: 993,
		Username: "me@fastmail.com", Auth: "file",
		SMTP: &SMTP{Host: "smtp.fastmail.com", Port: 587},
	}
	if err := UpsertAccount(p, a, ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 3 {
		t.Fatalf("%d accounts, want 3", len(cfg.Accounts))
	}
	got, err := cfg.Account("side")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if got.SMTP == nil || got.SMTP.Host != "smtp.fastmail.com" {
		t.Fatalf("smtp = %+v, want the fastmail host", got.SMTP)
	}
}

// A password account must not have an empty oauth block written under it: an
// empty client id fails validation on the way back in.
func TestUpsertAccountWritesNoEmptyOAuthBlock(t *testing.T) {
	p := writeConfig(t, twoAccountConfig)
	a := Account{Name: "plain", Host: "imap.example.com", Port: 993, Username: "me@example.com", Auth: "file"}
	if err := UpsertAccount(p, a, ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("Load after adding a password account: %v", err)
	}
	body, _ := os.ReadFile(p)
	if strings.Contains(string(body), "smtp: null") || strings.Contains(string(body), "oauth: null") {
		t.Fatalf("null blocks written:\n%s", body)
	}
}

// With no file at all the first account writes the shipped starting config.
func TestUpsertAccountWritesAFreshConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	a := Account{Name: "personal", Host: "imap.gmail.com", Port: 993, Username: "me@gmail.com", Auth: "file"}
	if err := UpsertAccount(p, a, ExampleProtect()); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Name != "personal" {
		t.Fatalf("accounts = %+v", cfg.Accounts)
	}
}

func TestRemoveAccountLeavesTheRestAlone(t *testing.T) {
	p := writeConfig(t, twoAccountConfig)
	if err := RemoveAccount(p, "personal"); err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Name != "work" {
		t.Fatalf("accounts = %+v, want work only", cfg.Accounts)
	}
	if len(cfg.Protect.Domains) != 1 || cfg.Protect.Domains[0] != "bank.example" {
		t.Fatalf("protect list changed: %+v", cfg.Protect)
	}

	if err := RemoveAccount(p, "nobody"); err == nil {
		t.Fatal("removing an account that is not there succeeded")
	}
}
