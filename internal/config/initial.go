package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Detect fills Host, Port and Username in from an email address, using the
// provider defaults DetectFromDomain knows. isProton reports a Proton Mail
// address: those need Proton Bridge and the host and port cannot be guessed
// (this build speaks implicit TLS only, Bridge defaults to STARTTLS), so the
// caller has to ask for both.
func (a Account) Detect(email string) (out Account, isProton bool) {
	out = a
	out.Username = email

	domain := ""
	if at := strings.LastIndex(email, "@"); at >= 0 {
		domain = email[at+1:]
	}
	host, port, proton := DetectFromDomain(domain)
	if proton {
		return out, true
	}
	out.Host = host
	out.Port = port
	return out, false
}

// ExampleProtect is the starting protect list a fresh config ships with: an
// empty one. The domains and addresses that used to be here are examples, not
// a curated list, and a shipped example in a live protect list is worse than
// nothing: it reads as protection the user has not actually set up, and it
// protects a bank they may not bank with while their own is unlisted. The
// examples are now comments in the rendered file, where they explain the
// shape without pretending to be a policy.
func ExampleProtect() Protect {
	return Protect{KeepTransactional: true}
}

// ExampleProtectDomains and ExampleProtectAddresses are the entries older
// versions wrote into a fresh config. Load reports a config that still
// carries them, so the owner is told to replace them with their own.
var (
	ExampleProtectDomains   = []string{"chase.com", "fidelity.com", "irs.gov"}
	ExampleProtectAddresses = []string{"alerts@mybank.example"}
)

// WriteInitial writes a starting config.yaml for one account at path, with
// the same comments as Example() so the file reads the same however it was
// created. The directory is created at mode 0700 and the file at 0600; an
// existing file is replaced.
func WriteInitial(path string, a Account, protect Protect) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(RenderInitial(a, protect)), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// RenderInitial builds the document WriteInitial stores.
func RenderInitial(a Account, protect Protect) string {
	if a.Port == 0 {
		a.Port = 993
	}
	if a.Auth == "" {
		a.Auth = "file"
	}

	var b strings.Builder
	b.WriteString("# mailshear configuration. See docs for the full reference.\n\n")
	b.WriteString("accounts:\n")
	fmt.Fprintf(&b, "  - name: %s\n", strconv.Quote(a.Name))
	fmt.Fprintf(&b, "    host: %s\n", strconv.Quote(a.Host))
	fmt.Fprintf(&b, "    port: %d\n", a.Port)
	fmt.Fprintf(&b, "    username: %s\n", strconv.Quote(a.Username))
	fmt.Fprintf(&b, "    auth: %-26s# file | oauth | env:UNSUB_PASSWORD | file:/path\n", a.Auth)
	fmt.Fprintf(&b, "    folders: %s   # default chosen by provider detection\n", yamlStringList(a.EffectiveFolders()))
	fmt.Fprintf(&b, "    trash: %-25s# override; empty = discover via special-use\n", strconv.Quote(a.Trash))
	if a.SMTP != nil && a.SMTP.Host != "" {
		port := a.SMTP.Port
		if port == 0 {
			port = 587
		}
		b.WriteString("    smtp:                           # optional, enables mailto unsubscribes\n")
		fmt.Fprintf(&b, "      host: %s\n", strconv.Quote(a.SMTP.Host))
		fmt.Fprintf(&b, "      port: %d\n", port)
	}
	b.WriteString(renderOAuth(a))

	b.WriteString("\nprotect:\n")
	fmt.Fprintf(&b, "  domains: %-24s# senders never touched, e.g. [\"chase.com\", \"irs.gov\"]\n", yamlStringList(protect.Domains))
	fmt.Fprintf(&b, "  addresses: %-22s# e.g. [\"alerts@mybank.example\"]\n", yamlStringList(protect.Addresses))
	fmt.Fprintf(&b, "  list_ids: %-23s# e.g. [\"announce.example.com\"]\n", yamlStringList(protect.ListIDs))
	fmt.Fprintf(&b, "  keep_transactional: %-6v# never delete receipts, bills, orders, security mail\n", protect.KeepTransactional)
	fmt.Fprintf(&b, "  keep_subjects: %s   # extra phrases: \"Rechnung\" or \"/renewal (notice|reminder)/\"\n", yamlStringList(protect.KeepSubjects))

	b.WriteString("\nunsubscribe:\n")
	b.WriteString("  http_get: true          # attempt plain https links, not just one-click\n")
	b.WriteString("  follow_redirects: 5\n")
	b.WriteString("  timeout_seconds: 15\n")
	b.WriteString("  per_host_rps: 1\n")
	b.WriteString("  allow_private_hosts: false   # true lets unsubscribe links reach your own network\n")

	b.WriteString("\nscan:\n")
	b.WriteString("  gmail_prefilter: \"\"     # optional X-GM-RAW query, e.g. \"category:promotions\"\n")
	return b.String()
}

// commentedOAuthBlock is the oauth: block as it appears in a config that does
// not use it: present so the keys are discoverable, commented so YAML never
// sees an empty client id.
const commentedOAuthBlock = `    # oauth:                        # with auth: oauth, see docs/oauth.md
    #   provider: google            # google | microsoft | custom
    #   client_id: ""
    #   client_secret: ""           # Google desktop clients issue one; empty for Microsoft
    #   tenant: common              # microsoft only
    #   auth_url: ""                # custom only
    #   token_url: ""
    #   scopes: []
`

// renderOAuth writes the account's oauth: block, or the commented template
// when the account does not use OAuth. The client secret is written as given:
// it is not a password, and the file is already mode 0600.
func renderOAuth(a Account) string {
	if a.Auth != "oauth" || a.OAuth == nil {
		return commentedOAuthBlock
	}
	o := *a.OAuth
	if o.Provider == "" {
		o.Provider = OAuthProviderFor(a.Provider())
	}

	var b strings.Builder
	b.WriteString("    oauth:                          # see docs/oauth.md\n")
	fmt.Fprintf(&b, "      provider: %-21s# google | microsoft | custom\n", o.Provider)
	fmt.Fprintf(&b, "      client_id: %s\n", strconv.Quote(o.ClientID))
	fmt.Fprintf(&b, "      client_secret: %s\n", strconv.Quote(o.ClientSecret))
	if o.Provider == "microsoft" || o.Tenant != "" {
		fmt.Fprintf(&b, "      tenant: %s\n", strconv.Quote(nonBlank(o.Tenant, "common")))
	}
	if o.Provider == "custom" {
		fmt.Fprintf(&b, "      auth_url: %s\n", strconv.Quote(o.AuthURL))
		fmt.Fprintf(&b, "      token_url: %s\n", strconv.Quote(o.TokenURL))
		fmt.Fprintf(&b, "      scopes: %s\n", yamlStringList(o.Scopes))
	}
	return b.String()
}

func nonBlank(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func yamlStringList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = strconv.Quote(it)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
