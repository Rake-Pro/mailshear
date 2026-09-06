// Package config loads and validates the mailshear YAML configuration file.
package config

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Rake-Pro/mailshear/internal/oauth"
)

type Config struct {
	Accounts    []Account       `yaml:"accounts"`
	Protect     Protect         `yaml:"protect"`
	Unsubscribe UnsubscribeOpts `yaml:"unsubscribe"`
	Scan        ScanOpts        `yaml:"scan"`

	// Warnings are the things worth telling the owner about a config that is
	// nonetheless valid and loadable: an auth method the provider has since
	// withdrawn, a protect list that is still the shipped example. The UI
	// shows them; nothing here stops a run.
	Warnings []string `yaml:"-"`
}

type Account struct {
	Name     string   `yaml:"name"`
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port"`
	Username string   `yaml:"username"`
	Auth     string   `yaml:"auth"`
	Folders  []string `yaml:"folders"`
	Trash    string   `yaml:"trash"`
	SMTP     *SMTP    `yaml:"smtp"`
	OAuth    *OAuth   `yaml:"oauth"`
}

// OAuth is the per-account OAuth 2.0 client, used when auth is "oauth". The
// client id (and, on Google, the secret) are the user's own: mailshear ships no
// client credentials, so nothing here identifies this tool to the provider.
type OAuth struct {
	// Provider is google, microsoft or custom. Empty falls back to the
	// provider the IMAP host implies.
	Provider     string `yaml:"provider"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// Tenant is the Entra tenant, microsoft only; empty means "common".
	Tenant string `yaml:"tenant"`
	// AuthURL, TokenURL and Scopes are required for provider: custom and
	// ignored otherwise.
	AuthURL  string   `yaml:"auth_url"`
	TokenURL string   `yaml:"token_url"`
	Scopes   []string `yaml:"scopes"`
}

type SMTP struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type Protect struct {
	Domains   []string `yaml:"domains"`
	Addresses []string `yaml:"addresses"`
	ListIDs   []string `yaml:"list_ids"`

	// KeepTransactional refuses to delete receipts, bills, invoices,
	// statements, order/payment/shipping confirmations and security mail
	// whatever the sender. It defaults to true; an omitted key is on.
	KeepTransactional bool `yaml:"keep_transactional"`
	// KeepSubjects adds to the built-in phrase list. Each entry is a
	// case-insensitive substring, or a /regexp/ between slashes.
	KeepSubjects []string `yaml:"keep_subjects"`
}

// Matches reports whether the protect list covers a sender, keyed by its
// registrable domain, From address and List-Id. Comparison is
// case-insensitive; an empty field never matches.
func (p Protect) Matches(domainKey, address, listID string) bool {
	return matchAny(p.Domains, domainKey) ||
		matchAny(p.Addresses, address) ||
		matchAny(p.ListIDs, listID)
}

func matchAny(list []string, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return false
	}
	for _, entry := range list {
		if strings.ToLower(strings.TrimSpace(entry)) == v {
			return true
		}
	}
	return false
}

type UnsubscribeOpts struct {
	HTTPGet         bool    `yaml:"http_get"`
	FollowRedirects int     `yaml:"follow_redirects"`
	TimeoutSeconds  int     `yaml:"timeout_seconds"`
	PerHostRPS      float64 `yaml:"per_host_rps"`
	// AllowPrivateHosts lets unsubscribe requests reach loopback, link-local
	// and RFC 1918 / ULA addresses. Off by default: List-Unsubscribe URLs
	// come from the sender, so following one inward would let bulk mail
	// reach services on the user's own network.
	AllowPrivateHosts bool `yaml:"allow_private_hosts"`
}

type ScanOpts struct {
	GmailPrefilter string `yaml:"gmail_prefilter"`
}

type Provider string

const (
	ProviderGmail    Provider = "gmail"
	ProviderFastmail Provider = "fastmail"
	ProviderOutlook  Provider = "outlook"
	ProviderICloud   Provider = "icloud"
	ProviderProton   Provider = "proton"
	ProviderGeneric  Provider = "generic"
)

func (a Account) Provider() Provider {
	h := strings.ToLower(a.Host)
	switch {
	case h == "imap.gmail.com" || strings.HasSuffix(h, ".googlemail.com") || h == "googlemail.com":
		return ProviderGmail
	case h == "imap.fastmail.com":
		return ProviderFastmail
	case h == "outlook.office365.com" || h == "imap-mail.outlook.com":
		return ProviderOutlook
	case h == "imap.mail.me.com":
		return ProviderICloud
	case h == "127.0.0.1" || h == "localhost" || a.Port == 1143:
		return ProviderProton
	default:
		return ProviderGeneric
	}
}

// Defaults is the single source of the per-provider settings: the IMAP host
// and port, the folders to scan, and the SMTP host and port for mailto
// unsubscribes. Proton (behind Bridge, on a host and port only the user
// knows) and generic IMAP return empty hosts and INBOX.
func Defaults(p Provider) (host string, port int, folders []string, smtpHost string, smtpPort int) {
	switch p {
	case ProviderGmail:
		return "imap.gmail.com", 993, []string{"[Gmail]/All Mail"}, "smtp.gmail.com", 587
	case ProviderFastmail:
		return "imap.fastmail.com", 993, []string{"INBOX"}, "smtp.fastmail.com", 587
	case ProviderOutlook:
		return "outlook.office365.com", 993, []string{"INBOX"}, "smtp.office365.com", 587
	case ProviderICloud:
		return "imap.mail.me.com", 993, []string{"INBOX"}, "smtp.mail.me.com", 587
	default:
		return "", 0, []string{"INBOX"}, "", 0
	}
}

// SMTPDefault returns the provider's well-known SMTP host and port, for
// accounts that did not configure smtp: explicitly. Providers with no known
// default (Proton, generic IMAP) return an empty host.
func (a Account) SMTPDefault() (string, int) {
	_, _, _, host, port := Defaults(a.Provider())
	return host, port
}

// providerForDomain maps a well-known mail domain to its provider. The bool
// reports a match; anything else is generic IMAP as far as the domain alone
// can tell, and DetectByMX is the next thing to ask.
func providerForDomain(domain string) (Provider, bool) {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "gmail.com", "googlemail.com":
		return ProviderGmail, true
	case "fastmail.com", "fastmail.fm", "messagingengine.com":
		return ProviderFastmail, true
	case "outlook.com", "hotmail.com", "live.com", "msn.com":
		return ProviderOutlook, true
	case "icloud.com", "me.com", "mac.com":
		return ProviderICloud, true
	case "proton.me", "protonmail.com", "pm.me":
		return ProviderProton, true
	default:
		return ProviderGeneric, false
	}
}

// DetectFromDomain guesses the IMAP host and port for an email address's
// domain, for use by the setup screen before an account exists to call Provider
// on. isProton is true when the domain belongs to Proton Mail, which needs
// Proton Bridge and whose host/port cannot be guessed: this build only
// speaks implicit TLS, and Bridge's default is STARTTLS, so the caller must
// ask the user for both explicitly.
func DetectFromDomain(domain string) (host string, port int, isProton bool) {
	p, known := providerForDomain(domain)
	if p == ProviderProton {
		return "", 0, true
	}
	if !known {
		return "imap." + domain, 993, false
	}
	host, port, _, _, _ = Defaults(p)
	return host, port, false
}

// DetectByMX classifies a domain by the mail exchangers it publishes, which is
// how a custom domain hosted on Google Workspace or Microsoft 365 is
// recognised: the domain itself says nothing. It reports false when the lookup
// fails, times out after three seconds, or matches no provider.
func DetectByMX(ctx context.Context, domain string) (Provider, bool) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" {
		return ProviderGeneric, false
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	records, err := net.DefaultResolver.LookupMX(ctx, domain)
	if err != nil || len(records) == 0 {
		return ProviderGeneric, false
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Pref < records[j].Pref })
	for _, mx := range records {
		if p := providerForMX(mx.Host); p != ProviderGeneric {
			return p, true
		}
	}
	return ProviderGeneric, false
}

// providerForMX classifies one MX hostname. It is pure, so the table can be
// tested without a resolver.
func providerForMX(host string) Provider {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	switch {
	case underDomain(h, "google.com"), underDomain(h, "googlemail.com"):
		return ProviderGmail
	case underDomain(h, "protection.outlook.com"), underDomain(h, "outlook.com"), underDomain(h, "hotmail.com"):
		return ProviderOutlook
	case underDomain(h, "messagingengine.com"), underDomain(h, "fastmail.com"):
		return ProviderFastmail
	case underDomain(h, "icloud.com"), underDomain(h, "me.com"):
		return ProviderICloud
	case underDomain(h, "protonmail.ch"), underDomain(h, "proton.me"):
		return ProviderProton
	default:
		return ProviderGeneric
	}
}

// underDomain reports whether host is the domain itself or a name beneath it,
// so "acme.com" does not count as being under "me.com".
func underDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// FoldersExplicit reports whether the config named the folders to scan, as
// opposed to EffectiveFolders defaulting them from the provider.
func (a Account) FoldersExplicit() bool {
	return len(a.Folders) > 0
}

func (a Account) EffectiveFolders() []string {
	if len(a.Folders) > 0 {
		return a.Folders
	}
	_, _, folders, _, _ := Defaults(a.Provider())
	return folders
}

func DefaultPath() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "mailshear", "config.yaml")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "mailshear", "config.yaml")
}

func DataDir() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "mailshear")
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "mailshear")
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "mailshear")
		}
		dir, _ := os.UserCacheDir()
		return filepath.Join(dir, "mailshear")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", "mailshear")
	}
}

// rawProtect mirrors Protect but keeps KeepTransactional as a pointer so Load
// can tell an explicit "keep_transactional: false" apart from an unset key,
// which defaults to true.
type rawProtect struct {
	Domains           []string `yaml:"domains"`
	Addresses         []string `yaml:"addresses"`
	ListIDs           []string `yaml:"list_ids"`
	KeepTransactional *bool    `yaml:"keep_transactional"`
	KeepSubjects      []string `yaml:"keep_subjects"`
}

// rawUnsubscribeOpts mirrors UnsubscribeOpts but keeps HTTPGet as a pointer
// so Load can tell an explicit "http_get: false" apart from an unset key.
type rawUnsubscribeOpts struct {
	HTTPGet           *bool   `yaml:"http_get"`
	FollowRedirects   int     `yaml:"follow_redirects"`
	TimeoutSeconds    int     `yaml:"timeout_seconds"`
	PerHostRPS        float64 `yaml:"per_host_rps"`
	AllowPrivateHosts bool    `yaml:"allow_private_hosts"`
}

type rawConfig struct {
	Accounts    []Account          `yaml:"accounts"`
	Protect     rawProtect         `yaml:"protect"`
	Unsubscribe rawUnsubscribeOpts `yaml:"unsubscribe"`
	Scan        ScanOpts           `yaml:"scan"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s; run mailshear and its setup screen will create it", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // UTF-8 BOM from Windows PowerShell redirects
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("config file %s is empty; delete it and run mailshear, whose setup screen writes a new one", path)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	cfg := Config{
		Accounts: raw.Accounts,
		Protect: Protect{
			Domains:           raw.Protect.Domains,
			Addresses:         raw.Protect.Addresses,
			ListIDs:           raw.Protect.ListIDs,
			KeepTransactional: raw.Protect.KeepTransactional == nil || *raw.Protect.KeepTransactional,
			KeepSubjects:      raw.Protect.KeepSubjects,
		},
		Scan: raw.Scan,
		Unsubscribe: UnsubscribeOpts{
			FollowRedirects:   raw.Unsubscribe.FollowRedirects,
			TimeoutSeconds:    raw.Unsubscribe.TimeoutSeconds,
			PerHostRPS:        raw.Unsubscribe.PerHostRPS,
			AllowPrivateHosts: raw.Unsubscribe.AllowPrivateHosts,
		},
	}
	if raw.Unsubscribe.HTTPGet != nil {
		cfg.Unsubscribe.HTTPGet = *raw.Unsubscribe.HTTPGet
	}

	applyDefaults(&cfg, raw.Unsubscribe.HTTPGet)

	if err := validate(&cfg); err != nil {
		return nil, err
	}
	cfg.Warnings = warningsFor(&cfg, path)

	return &cfg, nil
}

// AccountNameRule is the message every account-name rejection uses, in the
// config loader and in the setup form alike.
const AccountNameRule = "an account name may only use letters, digits, dot, underscore and hyphen, " +
	"at most 64 characters, and may not start with a dot"

var accountNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidAccountName reports whether name is one mailshear will accept. The name
// reaches the credential store, the export file names and the database, so it
// is kept to a set that means the same thing everywhere: no path separators,
// no leading dot, nothing that needs quoting.
func ValidAccountName(name string) bool {
	return accountNameRe.MatchString(name) && !strings.HasPrefix(name, ".")
}

// warningsFor collects the non-fatal remarks about a loaded config.
func warningsFor(cfg *Config, path string) []string {
	var out []string
	for _, a := range cfg.Accounts {
		// Microsoft withdrew app-password IMAP from Outlook.com and Exchange
		// Online, so a password account there cannot connect any more. This
		// is a warning rather than an error: the config is still well formed,
		// and saying so at load time beats an opaque AUTHENTICATE failure.
		if a.Provider() == ProviderOutlook && a.Auth != "oauth" {
			out = append(out, fmt.Sprintf(
				"account %q: Microsoft no longer accepts app passwords for IMAP on Outlook.com or Microsoft 365; "+
					"switch it to auth: oauth (see docs/oauth.md)", a.Name))
		}
	}
	if shipsExampleProtect(cfg.Protect) {
		out = append(out, fmt.Sprintf(
			"protect: still lists the shipped example entries (%s); edit %s so it names the senders you actually care about",
			strings.Join(append(append([]string{}, ExampleProtectDomains...), ExampleProtectAddresses...), ", "), path))
	}
	return out
}

// shipsExampleProtect reports a protect list that is still the example an
// older version wrote on a fresh install: the entries are there and nothing
// of the owner's is.
func shipsExampleProtect(p Protect) bool {
	if len(p.ListIDs) > 0 {
		return false
	}
	return sameList(p.Domains, ExampleProtectDomains) && sameList(p.Addresses, ExampleProtectAddresses)
}

func sameList(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !strings.EqualFold(strings.TrimSpace(got[i]), want[i]) {
			return false
		}
	}
	return true
}

func applyDefaults(cfg *Config, httpGetSet *bool) {
	for i := range cfg.Accounts {
		if cfg.Accounts[i].Port == 0 {
			cfg.Accounts[i].Port = 993
		}
		if cfg.Accounts[i].Auth == "" {
			cfg.Accounts[i].Auth = "file"
		}
	}
	if httpGetSet == nil {
		cfg.Unsubscribe.HTTPGet = true
	}
	if cfg.Unsubscribe.FollowRedirects == 0 {
		cfg.Unsubscribe.FollowRedirects = 5
	}
	if cfg.Unsubscribe.TimeoutSeconds == 0 {
		cfg.Unsubscribe.TimeoutSeconds = 15
	}
	if cfg.Unsubscribe.PerHostRPS == 0 {
		cfg.Unsubscribe.PerHostRPS = 1
	}
}

func validate(cfg *Config) error {
	seen := make(map[string]bool, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		if a.Name == "" {
			return fmt.Errorf("account: name is required")
		}
		if !ValidAccountName(a.Name) {
			return fmt.Errorf("account %q: %s", a.Name, AccountNameRule)
		}
		if seen[a.Name] {
			return fmt.Errorf("account %q: duplicate account name", a.Name)
		}
		seen[a.Name] = true
		if a.Host == "" {
			return fmt.Errorf("account %q: host is required", a.Name)
		}
		if a.Username == "" {
			return fmt.Errorf("account %q: username is required", a.Name)
		}
		if err := validateAuth(a.Auth); err != nil {
			return fmt.Errorf("account %q: %w", a.Name, err)
		}
		if a.Auth == "oauth" {
			if _, err := a.OAuthSpec(); err != nil {
				return fmt.Errorf("account %q: %w", a.Name, err)
			}
		}
	}
	return nil
}

func validateAuth(spec string) error {
	switch {
	case spec == "file":
		return nil
	case spec == "oauth":
		return nil
	case spec == "keyring":
		// Deprecated alias for "file", kept so old configs still load.
		return nil
	case strings.HasPrefix(spec, "env:"):
		if strings.TrimPrefix(spec, "env:") == "" {
			return fmt.Errorf("auth: env: spec requires a variable name, e.g. env:UNSUB_PASSWORD")
		}
		return nil
	case strings.HasPrefix(spec, "file:"):
		if strings.TrimPrefix(spec, "file:") == "" {
			return fmt.Errorf("auth: file: spec requires a path, e.g. file:/path/to/secret")
		}
		return nil
	default:
		return fmt.Errorf("auth: invalid spec %q; must be \"file\", \"oauth\", \"env:NAME\", or \"file:PATH\"", spec)
	}
}

// OAuthSpec resolves the account's oauth: block into the provider endpoints
// the flow needs. It is the validation for auth: oauth as well, so a config
// that would fail at sign-in time fails at load time instead.
func (a Account) OAuthSpec() (oauth.ProviderSpec, error) {
	o := a.OAuth
	if o == nil {
		return oauth.ProviderSpec{}, fmt.Errorf("auth: oauth needs an oauth: block with client_id; see docs/oauth.md")
	}
	if strings.TrimSpace(o.ClientID) == "" {
		return oauth.ProviderSpec{}, fmt.Errorf("auth: oauth needs oauth.client_id; see docs/oauth.md")
	}

	name := strings.ToLower(strings.TrimSpace(o.Provider))
	if name == "" {
		// An unnamed provider is the one the IMAP host already implies, so
		// a Gmail or Outlook account needs nothing but a client id.
		switch a.Provider() {
		case ProviderGmail:
			name = "google"
		case ProviderOutlook:
			name = "microsoft"
		}
	}

	switch name {
	case "google":
		return oauth.Google(), nil
	case "microsoft":
		return oauth.Microsoft(o.Tenant), nil
	case "custom":
		switch {
		case strings.TrimSpace(o.AuthURL) == "":
			return oauth.ProviderSpec{}, fmt.Errorf("auth: oauth provider custom needs oauth.auth_url")
		case strings.TrimSpace(o.TokenURL) == "":
			return oauth.ProviderSpec{}, fmt.Errorf("auth: oauth provider custom needs oauth.token_url")
		case len(o.Scopes) == 0:
			return oauth.ProviderSpec{}, fmt.Errorf("auth: oauth provider custom needs oauth.scopes")
		}
		return oauth.ProviderSpec{
			Name:     "custom",
			AuthURL:  o.AuthURL,
			TokenURL: o.TokenURL,
			Scopes:   o.Scopes,
		}, nil
	case "":
		return oauth.ProviderSpec{}, fmt.Errorf("auth: oauth needs oauth.provider (google, microsoft or custom) for host %s", a.Host)
	default:
		return oauth.ProviderSpec{}, fmt.Errorf("auth: unknown oauth.provider %q; must be google, microsoft or custom", o.Provider)
	}
}

// SupportsOAuth reports whether mailshear knows the OAuth endpoints for a
// provider, which is what decides whether the setup screen offers the choice.
func SupportsOAuth(p Provider) bool {
	return p == ProviderGmail || p == ProviderOutlook
}

// OAuthProviderFor names the oauth: provider for a mail provider, or "" when
// there is no known one.
func OAuthProviderFor(p Provider) string {
	switch p {
	case ProviderGmail:
		return "google"
	case ProviderOutlook:
		return "microsoft"
	default:
		return ""
	}
}

func (c *Config) Account(name string) (Account, error) {
	if name == "" {
		switch len(c.Accounts) {
		case 0:
			return Account{}, fmt.Errorf("no accounts configured")
		case 1:
			return c.Accounts[0], nil
		default:
			return Account{}, fmt.Errorf("multiple accounts configured; specify --account NAME")
		}
	}
	for _, a := range c.Accounts {
		if a.Name == name {
			return a, nil
		}
	}
	return Account{}, fmt.Errorf("account %q not found", name)
}

func Example() string {
	return `# mailshear configuration. See docs for the full reference.

accounts:
  - name: personal
    host: imap.gmail.com
    port: 993
    username: you@gmail.com
    auth: file                      # file | oauth | env:UNSUB_PASSWORD | file:/path
    folders: ["[Gmail]/All Mail"]   # default chosen by provider detection
    trash: ""                       # override; empty = discover via special-use
    smtp:                           # optional, enables mailto unsubscribes
      host: smtp.gmail.com
      port: 587
` + commentedOAuthBlock + `
protect:
  domains: []                # senders never touched, e.g. ["chase.com", "irs.gov"]
  addresses: []              # e.g. ["alerts@mybank.example"]
  list_ids: []               # e.g. ["announce.example.com"]
  keep_transactional: true   # never delete receipts, bills, orders, security mail
  keep_subjects: []          # extra phrases: "Rechnung" or "/renewal (notice|reminder)/"

unsubscribe:
  http_get: true          # attempt plain https links, not just one-click
  follow_redirects: 5
  timeout_seconds: 15
  per_host_rps: 1
  allow_private_hosts: false   # true lets unsubscribe links reach your own network

scan:
  gmail_prefilter: ""     # optional X-GM-RAW query, e.g. "category:promotions"
`
}
