package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDesignExample(t *testing.T) {
	path := writeTemp(t, Example())
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(cfg.Accounts))
	}
	a := cfg.Accounts[0]
	if a.Name != "personal" || a.Host != "imap.gmail.com" || a.Port != 993 {
		t.Errorf("unexpected account: %+v", a)
	}
	if a.Username != "you@gmail.com" || a.Auth != "file" {
		t.Errorf("unexpected account: %+v", a)
	}
	if len(a.Folders) != 1 || a.Folders[0] != "[Gmail]/All Mail" {
		t.Errorf("unexpected folders: %v", a.Folders)
	}
	if a.SMTP == nil || a.SMTP.Host != "smtp.gmail.com" || a.SMTP.Port != 587 {
		t.Errorf("unexpected smtp: %+v", a.SMTP)
	}
	// The shipped example protects nothing: the entries are comments, so the
	// owner's own list is the only one that ever takes effect.
	if len(cfg.Protect.Domains) != 0 || len(cfg.Protect.Addresses) != 0 || len(cfg.Protect.ListIDs) != 0 {
		t.Errorf("the example config ships a non-empty protect list: %+v", cfg.Protect)
	}
	if !strings.Contains(Example(), `# senders never touched, e.g. ["chase.com", "irs.gov"]`) {
		t.Error("the example lost the commented protect examples")
	}
	if cfg.Unsubscribe.HTTPGet != true || cfg.Unsubscribe.FollowRedirects != 5 ||
		cfg.Unsubscribe.TimeoutSeconds != 15 || cfg.Unsubscribe.PerHostRPS != 1 {
		t.Errorf("unexpected unsubscribe opts: %+v", cfg.Unsubscribe)
	}
	if cfg.Scan.GmailPrefilter != "" {
		t.Errorf("unexpected scan prefilter: %q", cfg.Scan.GmailPrefilter)
	}
}

func TestDefaultsApplied(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: minimal
    host: imap.example.com
    username: me@example.com
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.Accounts[0]
	if a.Port != 993 {
		t.Errorf("expected default port 993, got %d", a.Port)
	}
	if a.Auth != "file" {
		t.Errorf("expected default auth file, got %q", a.Auth)
	}
	if !cfg.Unsubscribe.HTTPGet {
		t.Errorf("expected default http_get true")
	}
	if cfg.Unsubscribe.FollowRedirects != 5 {
		t.Errorf("expected default follow_redirects 5, got %d", cfg.Unsubscribe.FollowRedirects)
	}
	if cfg.Unsubscribe.TimeoutSeconds != 15 {
		t.Errorf("expected default timeout_seconds 15, got %d", cfg.Unsubscribe.TimeoutSeconds)
	}
	if cfg.Unsubscribe.PerHostRPS != 1 {
		t.Errorf("expected default per_host_rps 1, got %v", cfg.Unsubscribe.PerHostRPS)
	}
	if !cfg.Protect.KeepTransactional {
		t.Errorf("expected keep_transactional to default to true")
	}
}

func TestKeepTransactionalCanBeTurnedOff(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: minimal
    host: imap.example.com
    username: me@example.com

protect:
  keep_transactional: false
  keep_subjects: ["Rechnung", "/renewal (notice|reminder)/"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Protect.KeepTransactional {
		t.Errorf("expected explicit keep_transactional: false to be kept")
	}
	if len(cfg.Protect.KeepSubjects) != 2 {
		t.Errorf("keep_subjects = %v", cfg.Protect.KeepSubjects)
	}
}

func TestExplicitHTTPGetFalseKept(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: minimal
    host: imap.example.com
    username: me@example.com

unsubscribe:
  http_get: false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Unsubscribe.HTTPGet {
		t.Errorf("expected explicit http_get: false to be kept")
	}
}

func TestKeyringAuthAcceptedAsDeprecatedAlias(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: minimal
    host: imap.example.com
    username: me@example.com
    auth: keyring
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Accounts[0].Auth != "keyring" {
		t.Errorf("expected auth to remain %q, got %q", "keyring", cfg.Accounts[0].Auth)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: minimal
    host: imap.example.com
    username: me@example.com

protect:
  domian: ["typo.example.com"]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestDuplicateNamesRejected(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: dup
    host: imap.example.com
    username: me@example.com
  - name: dup
    host: imap.other.com
    username: me@other.com
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for duplicate account name, got nil")
	}
}

func TestBadAuthRejected(t *testing.T) {
	cases := []string{
		`auth: bogus`,
		`auth: "env:"`,
		`auth: "file:"`,
	}
	for _, auth := range cases {
		path := writeTemp(t, `
accounts:
  - name: acct
    host: imap.example.com
    username: me@example.com
    `+auth+"\n")
		if _, err := Load(path); err == nil {
			t.Errorf("expected error for %s, got nil", auth)
		}
	}
}

func TestProviderTable(t *testing.T) {
	cases := []struct {
		host string
		port int
		want Provider
	}{
		{"imap.gmail.com", 993, ProviderGmail},
		{"someuser.googlemail.com", 993, ProviderGmail},
		{"imap.fastmail.com", 993, ProviderFastmail},
		{"outlook.office365.com", 993, ProviderOutlook},
		{"imap-mail.outlook.com", 993, ProviderOutlook},
		{"imap.mail.me.com", 993, ProviderICloud},
		{"127.0.0.1", 993, ProviderProton},
		{"localhost", 993, ProviderProton},
		{"imap.example.com", 1143, ProviderProton},
		{"imap.example.com", 993, ProviderGeneric},
	}
	for _, c := range cases {
		a := Account{Host: c.host, Port: c.port}
		if got := a.Provider(); got != c.want {
			t.Errorf("Provider(%s:%d) = %s, want %s", c.host, c.port, got, c.want)
		}
	}
}

func TestEffectiveFolders(t *testing.T) {
	cases := []struct {
		name string
		acct Account
		want []string
	}{
		{"explicit wins", Account{Host: "imap.gmail.com", Folders: []string{"Custom"}}, []string{"Custom"}},
		{"gmail default", Account{Host: "imap.gmail.com"}, []string{"[Gmail]/All Mail"}},
		{"generic default", Account{Host: "imap.example.com"}, []string{"INBOX"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.acct.EffectiveFolders()
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestSMTPDefault(t *testing.T) {
	cases := []struct {
		host     string
		wantHost string
		wantPort int
	}{
		{"imap.gmail.com", "smtp.gmail.com", 587},
		{"imap.fastmail.com", "smtp.fastmail.com", 587},
		{"outlook.office365.com", "smtp.office365.com", 587},
		{"imap.mail.me.com", "smtp.mail.me.com", 587},
		{"127.0.0.1", "", 0},
		{"imap.example.com", "", 0},
	}
	for _, c := range cases {
		a := Account{Host: c.host, Port: 993}
		gotHost, gotPort := a.SMTPDefault()
		if gotHost != c.wantHost || gotPort != c.wantPort {
			t.Errorf("SMTPDefault(%s) = %s:%d, want %s:%d", c.host, gotHost, gotPort, c.wantHost, c.wantPort)
		}
	}
}

func TestDetectFromDomain(t *testing.T) {
	cases := []struct {
		domain     string
		wantHost   string
		wantPort   int
		wantProton bool
	}{
		{"gmail.com", "imap.gmail.com", 993, false},
		{"googlemail.com", "imap.gmail.com", 993, false},
		{"fastmail.com", "imap.fastmail.com", 993, false},
		{"fastmail.fm", "imap.fastmail.com", 993, false},
		{"messagingengine.com", "imap.fastmail.com", 993, false},
		{"outlook.com", "outlook.office365.com", 993, false},
		{"hotmail.com", "outlook.office365.com", 993, false},
		{"live.com", "outlook.office365.com", 993, false},
		{"msn.com", "outlook.office365.com", 993, false},
		{"icloud.com", "imap.mail.me.com", 993, false},
		{"me.com", "imap.mail.me.com", 993, false},
		{"mac.com", "imap.mail.me.com", 993, false},
		{"proton.me", "", 0, true},
		{"protonmail.com", "", 0, true},
		{"pm.me", "", 0, true},
		{"example.org", "imap.example.org", 993, false},
	}
	for _, c := range cases {
		host, port, proton := DetectFromDomain(c.domain)
		if host != c.wantHost || port != c.wantPort || proton != c.wantProton {
			t.Errorf("DetectFromDomain(%s) = %s:%d proton=%v, want %s:%d proton=%v",
				c.domain, host, port, proton, c.wantHost, c.wantPort, c.wantProton)
		}
	}
}

func TestAccountLookup(t *testing.T) {
	empty := &Config{}
	if _, err := empty.Account(""); err == nil {
		t.Error("expected error looking up account in empty config")
	}

	one := &Config{Accounts: []Account{{Name: "solo"}}}
	a, err := one.Account("")
	if err != nil {
		t.Fatalf("Account(\"\"): %v", err)
	}
	if a.Name != "solo" {
		t.Errorf("expected solo, got %q", a.Name)
	}

	two := &Config{Accounts: []Account{{Name: "a"}, {Name: "b"}}}
	if _, err := two.Account(""); err == nil {
		t.Error("expected error for ambiguous Account(\"\") with two accounts")
	}
	a, err = two.Account("b")
	if err != nil {
		t.Fatalf("Account(\"b\"): %v", err)
	}
	if a.Name != "b" {
		t.Errorf("expected b, got %q", a.Name)
	}
	if _, err := two.Account("missing"); err == nil {
		t.Error("expected error for missing account name")
	}
}

func TestWriteInitialRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")

	a, proton := Account{Name: "personal", Auth: "file"}.Detect("me@gmail.com")
	if proton {
		t.Fatal("gmail detected as a Proton address")
	}
	a.SMTP = &SMTP{Host: "smtp.gmail.com", Port: 587}
	if err := WriteInitial(p, a, ExampleProtect()); err != nil {
		t.Fatalf("WriteInitial: %v", err)
	}

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Accounts[0]
	if got.Host != "imap.gmail.com" || got.Port != 993 || got.Username != "me@gmail.com" {
		t.Fatalf("account = %+v", got)
	}
	if len(got.Folders) != 1 || got.Folders[0] != "[Gmail]/All Mail" {
		t.Fatalf("folders = %v", got.Folders)
	}
	if got.SMTP == nil || got.SMTP.Host != "smtp.gmail.com" {
		t.Fatalf("smtp = %+v", got.SMTP)
	}
	if len(cfg.Protect.Domains) != 0 || len(cfg.Protect.Addresses) != 0 {
		t.Fatalf("a fresh config wrote protect entries: %+v", cfg.Protect)
	}
	if !cfg.Protect.KeepTransactional {
		t.Fatal("keep_transactional did not survive the round trip")
	}
}

func TestDetectProton(t *testing.T) {
	if _, proton := (Account{}).Detect("me@proton.me"); !proton {
		t.Fatal("proton.me not detected as Proton")
	}
}

func TestDefaultsTable(t *testing.T) {
	cases := []struct {
		p        Provider
		host     string
		port     int
		folders  []string
		smtpHost string
		smtpPort int
	}{
		{ProviderGmail, "imap.gmail.com", 993, []string{"[Gmail]/All Mail"}, "smtp.gmail.com", 587},
		{ProviderFastmail, "imap.fastmail.com", 993, []string{"INBOX"}, "smtp.fastmail.com", 587},
		{ProviderOutlook, "outlook.office365.com", 993, []string{"INBOX"}, "smtp.office365.com", 587},
		{ProviderICloud, "imap.mail.me.com", 993, []string{"INBOX"}, "smtp.mail.me.com", 587},
		{ProviderProton, "", 0, []string{"INBOX"}, "", 0},
		{ProviderGeneric, "", 0, []string{"INBOX"}, "", 0},
	}
	for _, c := range cases {
		host, port, folders, smtpHost, smtpPort := Defaults(c.p)
		if host != c.host || port != c.port || smtpHost != c.smtpHost || smtpPort != c.smtpPort {
			t.Errorf("Defaults(%s) = %s:%d smtp %s:%d, want %s:%d smtp %s:%d",
				c.p, host, port, smtpHost, smtpPort, c.host, c.port, c.smtpHost, c.smtpPort)
		}
		if len(folders) != len(c.folders) || folders[0] != c.folders[0] {
			t.Errorf("Defaults(%s) folders = %v, want %v", c.p, folders, c.folders)
		}
	}

	// Every caller gets its own slice, so one editing it cannot reach another.
	_, _, f1, _, _ := Defaults(ProviderGmail)
	_, _, f2, _, _ := Defaults(ProviderGmail)
	f1[0] = "changed"
	if f2[0] != "[Gmail]/All Mail" {
		t.Fatalf("Defaults handed out a shared folder slice: %v", f2)
	}
}

func TestProviderForMX(t *testing.T) {
	cases := []struct {
		host string
		want Provider
	}{
		{"aspmx.l.google.com.", ProviderGmail},
		{"ALT1.ASPMX.L.GOOGLE.COM", ProviderGmail},
		{"gmr-smtp-in.l.googlemail.com.", ProviderGmail},
		{"contoso.mail.protection.outlook.com.", ProviderOutlook},
		{"outlook.com", ProviderOutlook},
		{"mx.hotmail.com", ProviderOutlook},
		{"in1-smtp.messagingengine.com.", ProviderFastmail},
		{"in2-smtp.fastmail.com", ProviderFastmail},
		{"mx01.mail.icloud.com.", ProviderICloud},
		{"mx1.mail.me.com", ProviderICloud},
		{"mail.protonmail.ch.", ProviderProton},
		{"mailsec.proton.me", ProviderProton},
		{"mx.acme.com", ProviderGeneric},
		{"mx.notgoogle.com", ProviderGeneric},
		{"", ProviderGeneric},
		{".", ProviderGeneric},
	}
	for _, c := range cases {
		if got := providerForMX(c.host); got != c.want {
			t.Errorf("providerForMX(%q) = %s, want %s", c.host, got, c.want)
		}
	}
}

func TestOAuthAccountValidates(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: personal
    host: imap.gmail.com
    username: me@gmail.com
    auth: oauth
    oauth:
      client_id: "abc.apps.googleusercontent.com"
      client_secret: "shh"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := cfg.Accounts[0].OAuthSpec()
	if err != nil {
		t.Fatalf("OAuthSpec: %v", err)
	}
	if spec.Name != "google" || spec.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestOAuthAccountRejectsIncompleteBlocks(t *testing.T) {
	cases := []struct{ name, body string }{
		{"no block", `
accounts:
  - name: a
    host: imap.gmail.com
    username: me@gmail.com
    auth: oauth
`},
		{"no client id", `
accounts:
  - name: a
    host: imap.gmail.com
    username: me@gmail.com
    auth: oauth
    oauth:
      provider: google
`},
		{"unknown provider", `
accounts:
  - name: a
    host: imap.gmail.com
    username: me@gmail.com
    auth: oauth
    oauth:
      provider: yahoo
      client_id: "x"
`},
		{"unresolvable provider", `
accounts:
  - name: a
    host: imap.example.com
    username: me@example.com
    auth: oauth
    oauth:
      client_id: "x"
`},
		{"custom without endpoints", `
accounts:
  - name: a
    host: imap.example.com
    username: me@example.com
    auth: oauth
    oauth:
      provider: custom
      client_id: "x"
`},
	}
	for _, tc := range cases {
		if _, err := Load(writeTemp(t, tc.body)); err == nil {
			t.Errorf("%s: expected an error, got nil", tc.name)
		}
	}
}

func TestOAuthCustomProvider(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: a
    host: imap.example.com
    username: me@example.com
    auth: oauth
    oauth:
      provider: custom
      client_id: "x"
      auth_url: "https://id.example.com/authorize"
      token_url: "https://id.example.com/token"
      scopes: ["mail"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := cfg.Accounts[0].OAuthSpec()
	if err != nil {
		t.Fatalf("OAuthSpec: %v", err)
	}
	if spec.AuthURL != "https://id.example.com/authorize" || len(spec.Scopes) != 1 {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestWriteInitialRendersOAuthBlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")

	a := Account{
		Name: "personal", Host: "outlook.office365.com", Port: 993,
		Username: "me@outlook.com", Auth: "oauth",
		OAuth: &OAuth{ClientID: "entra-app-id"},
	}
	if err := WriteInitial(p, a, ExampleProtect()); err != nil {
		t.Fatalf("WriteInitial: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Accounts[0]
	if got.Auth != "oauth" || got.OAuth == nil {
		t.Fatalf("account = %+v", got)
	}
	if got.OAuth.ClientID != "entra-app-id" || got.OAuth.Provider != "microsoft" || got.OAuth.Tenant != "common" {
		t.Fatalf("oauth = %+v", got.OAuth)
	}
}

func TestWriteInitialLeavesOAuthCommentedForPasswordAccounts(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")

	a, _ := Account{Name: "personal", Auth: "file"}.Detect("me@gmail.com")
	if err := WriteInitial(p, a, ExampleProtect()); err != nil {
		t.Fatalf("WriteInitial: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "# oauth:") {
		t.Fatal("the commented oauth template is missing")
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Accounts[0].OAuth != nil {
		t.Fatal("the commented block was parsed as a real oauth block")
	}
}
