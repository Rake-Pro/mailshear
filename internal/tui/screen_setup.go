package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/Rake-Pro/mailshear/internal/config"
)

// Field indices in setupModel.fields. Which of them are on screen is
// visible()'s answer: the provider decides whether the auth method can be
// chosen at all, the auth method decides between the password and the OAuth
// client fields, and the rest appear under the advanced toggle.
const (
	fName = iota
	fEmail
	fProvider
	fAuthMethod
	fPassword
	fClientID
	fClientSecret
	fHost
	fPort
	fFolders
	fSMTPHost
	fSMTPPort
	fieldCount
)

// Stops on the auth-method picker.
const (
	authPassword = iota
	authOAuth
)

var authChoiceLabels = [...]string{"App password", "OAuth sign-in"}

// providerChoice is one stop on the provider picker. The first entry, with no
// provider of its own, is auto-detect.
type providerChoice struct {
	label string
	p     config.Provider
}

// autoChoice is the index of auto-detect in providerChoices.
const autoChoice = 0

var providerChoices = []providerChoice{
	{label: "Auto-detect"},
	{label: "Gmail / Google Workspace", p: config.ProviderGmail},
	{label: "Fastmail", p: config.ProviderFastmail},
	{label: "Outlook / Microsoft 365", p: config.ProviderOutlook},
	{label: "iCloud", p: config.ProviderICloud},
	{label: "Proton Bridge", p: config.ProviderProton},
	{label: "Other IMAP", p: config.ProviderGeneric},
}

// providerLabel is the picker's name for a resolved provider.
func providerLabel(p config.Provider) string {
	for _, c := range providerChoices[autoChoice+1:] {
		if c.p == p {
			return c.label
		}
	}
	return string(p)
}

type setupModel struct {
	styles     styles
	configPath string
	spinner    spinner.Model

	fields [fieldCount]field
	focus  int

	// authChoice indexes authChoiceLabels. It only means anything when the
	// resolved provider has OAuth endpoints mailshear knows.
	authChoice int

	// signingIn is a browser sign-in in flight, with authURL the address to
	// show so it can be opened or copied by hand.
	signingIn bool
	authURL   string

	// mode says which of the three forms this is: the first-run setup, an
	// edit of an existing account, or a new one being added.
	mode setupMode
	// existing holds the account names already in the config, so add mode
	// can refuse a duplicate before it reaches the file.
	existing []string
	status   AuthStatus

	advanced bool
	// proton records that the account needs Proton Bridge, whose host and
	// port cannot be guessed; the form then insists on the advanced fields.
	proton bool

	// choice indexes providerChoices.
	choice int
	// resolved is the provider actually in effect. It is empty while
	// auto-detect has yet to settle on one, which is what blocks the save.
	resolved config.Provider
	// looking reports an MX lookup in flight. mxSeq discards the answers of
	// lookups that have been superseded.
	looking bool
	mxSeq   int
	// submitAfterMX saves as soon as the lookup lands, so enter pressed on a
	// finished form does not have to be pressed again.
	submitAfterMX bool

	// note explains the provider in effect.
	note string
	err  string
	// saving blocks a second Enter while the write is in flight.
	saving bool
}

// setupMode is which of the three jobs the form is doing.
type setupMode int

const (
	// setupFirstRun is the form on a fresh install: esc quits, and saving
	// goes straight on to the scan.
	setupFirstRun setupMode = iota
	// setupEdit is an existing account opened from the accounts screen.
	setupEdit
	// setupAdd is a new account being appended to the config.
	setupAdd
)

// setupSubmitMsg carries a validated form to the flow, which saves it. An
// OAuth account carries no password; the browser sign-in follows the save.
type setupSubmitMsg struct {
	acct     config.Account
	password string
	// add asks for the account to be appended rather than replaced, which
	// fails if the name is taken.
	add bool
}

// setupSavedMsg is the result of saving the form.
type setupSavedMsg struct {
	acct config.Account
	err  error
}

// mxResultMsg is the answer to one MX lookup started by the setup screen.
type mxResultMsg struct {
	seq    int
	domain string
	p      config.Provider
	ok     bool
}

func newSetupModel(s styles, a config.Account, configPath string) setupModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = s.accent

	m := setupModel{styles: s, configPath: configPath, spinner: sp}
	m.fields[fName] = field{label: "Account name", value: nonEmpty(a.Name, "personal")}
	m.fields[fEmail] = field{label: "Email address", hint: "you@example.com"}
	if a.Username != placeholderUsername {
		m.fields[fEmail].value = a.Username
	}
	m.fields[fProvider] = field{label: "Provider"}
	m.fields[fAuthMethod] = field{label: "Auth method"}
	m.fields[fPassword] = field{label: "App password", mask: true, hint: "provider app password, not your login"}
	m.fields[fClientID] = field{label: "Client ID", hint: "from your own OAuth client, see docs/oauth.md"}
	m.fields[fClientSecret] = field{label: "Client secret", mask: true, hint: "Google desktop clients issue one"}
	m.fields[fHost] = field{label: "IMAP host", hint: "imap.example.com"}
	m.fields[fPort] = field{label: "IMAP port", value: "993"}
	m.fields[fFolders] = field{label: "Folders", hint: "INBOX"}
	m.fields[fSMTPHost] = field{label: "SMTP host", hint: "blank disables mailto unsubscribes"}
	m.fields[fSMTPPort] = field{label: "SMTP port", value: "587"}

	if a.Host != "" {
		m.fields[fHost].set(a.Host)
	}
	if a.Port != 0 {
		m.fields[fPort].set(strconv.Itoa(a.Port))
	}
	if len(a.Folders) > 0 {
		m.fields[fFolders].set(strings.Join(a.Folders, ", "))
	}
	if a.SMTP != nil {
		m.fields[fSMTPHost].set(a.SMTP.Host)
		if a.SMTP.Port != 0 {
			m.fields[fSMTPPort].set(strconv.Itoa(a.SMTP.Port))
		}
	}
	if a.Auth == "oauth" {
		m.authChoice = authOAuth
		if a.OAuth != nil {
			m.fields[fClientID].set(a.OAuth.ClientID)
			m.fields[fClientSecret].set(a.OAuth.ClientSecret)
		}
	}
	m.resolve()
	return m
}

func nonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// oauthOffered reports whether the resolved provider has OAuth endpoints
// mailshear knows, which is what puts the auth-method picker on screen.
func (m setupModel) oauthOffered() bool {
	return config.SupportsOAuth(m.resolved)
}

// useOAuth reports that the form is describing an OAuth account.
func (m setupModel) useOAuth() bool {
	return m.oauthOffered() && (m.oauthOnly() || m.authChoice == authOAuth)
}

// microsoftNoAppPasswords is why the auth-method picker is fixed on OAuth for
// Outlook and Microsoft 365.
const microsoftNoAppPasswords = "Microsoft no longer accepts app passwords for IMAP; OAuth is the only way in"

// oauthOnly reports a provider that will not take a password at all, so the
// app-password stop on the picker is not offered rather than offered and then
// rejected by the server.
func (m setupModel) oauthOnly() bool {
	return m.resolved == config.ProviderOutlook
}

// visible lists the field indices currently on screen, in order.
func (m setupModel) visible() []int {
	out := []int{fName, fEmail, fProvider}
	if m.oauthOffered() {
		out = append(out, fAuthMethod)
	}
	if m.useOAuth() {
		out = append(out, fClientID)
		// Only Google issues a secret for a desktop client; a Microsoft
		// public client is rejected if it sends one.
		if m.resolved == config.ProviderGmail {
			out = append(out, fClientSecret)
		}
	} else {
		out = append(out, fPassword)
	}
	if m.advanced || m.proton {
		out = append(out, fHost, fPort, fFolders, fSMTPHost, fSMTPPort)
	}
	return out
}

// clampFocus keeps the cursor on a field that is still on screen after the
// visible set changed.
func (m *setupModel) clampFocus() {
	if n := len(m.visible()); m.focus >= n {
		m.focus = n - 1
	}
	if m.focus < 0 {
		m.focus = 0
	}
}

// emailDomain is the part of the address after the last @, lowercased.
func (m setupModel) emailDomain() string {
	email := strings.TrimSpace(m.fields[fEmail].value)
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// resolve recomputes the provider from the picker and the address. It never
// touches the network: auto-detect that the domain table cannot answer leaves
// the provider unresolved for lookupMX to settle.
func (m *setupModel) resolve() {
	if m.choice != autoChoice {
		c := providerChoices[m.choice]
		m.adopt(c.p, "selected "+c.label)
		return
	}

	email := strings.TrimSpace(m.fields[fEmail].value)
	if email == "" {
		m.resolved = ""
		m.proton = false
		m.note = ""
		return
	}
	acct, isProton := config.Account{}.Detect(email)
	p := acct.Provider()
	if isProton {
		p = config.ProviderProton
	}
	if p == config.ProviderGeneric {
		// Nothing in the domain table knows this one; the MX records decide.
		m.resolved = ""
		m.proton = false
		m.note = ""
		return
	}
	m.adopt(p, "detected "+providerLabel(p))
}

// adopt takes a provider on, filling every field the user has not edited from
// config.Defaults and writing the result line under the given prefix.
func (m *setupModel) adopt(p config.Provider, prefix string) {
	m.resolved = p
	m.proton = p == config.ProviderProton
	m.fields[fPassword].stripSpaces = p == config.ProviderGmail

	if m.proton {
		m.note = "Proton needs Proton Bridge running locally, and this build speaks implicit TLS only: " +
			"enter the host and port Bridge listens on."
		return
	}

	host, port, folders, smtpHost, smtpPort := config.Defaults(p)
	if p == config.ProviderGeneric {
		// Nothing well known: guess the conventional host and show the
		// advanced fields, since the guess is the user's to correct.
		if d := m.emailDomain(); d != "" {
			host = "imap." + d
		}
		port = 993
		m.advanced = true
	}

	if !m.fields[fHost].edited {
		m.fields[fHost].set(host)
	}
	if !m.fields[fPort].edited && port != 0 {
		m.fields[fPort].set(strconv.Itoa(port))
	}
	if !m.fields[fFolders].edited {
		m.fields[fFolders].set(strings.Join(folders, ", "))
	}
	if !m.fields[fSMTPHost].edited {
		m.fields[fSMTPHost].set(smtpHost)
		if smtpPort != 0 {
			m.fields[fSMTPPort].set(strconv.Itoa(smtpPort))
		}
	}
	m.note = fmt.Sprintf("%s: %s:%d, folders %s", prefix, host, port, strings.Join(folders, ", "))
	if p == config.ProviderOutlook {
		m.authChoice = authOAuth
		m.note += ". " + microsoftNoAppPasswords
	}
}

// lookupMX starts an MX lookup for the address's domain when auto-detect is on
// and the domain table did not already answer. The resolver call runs as a
// command, so Update never waits on it.
func (m *setupModel) lookupMX() tea.Cmd {
	if m.choice != autoChoice || m.resolved != "" || m.looking {
		return nil
	}
	domain := m.emailDomain()
	if !strings.Contains(domain, ".") {
		return nil
	}

	m.mxSeq++
	m.looking = true
	m.note = "looking up MX for " + domain + "..."

	seq := m.mxSeq
	return func() tea.Msg {
		p, ok := config.DetectByMX(context.Background(), domain)
		return mxResultMsg{seq: seq, domain: domain, p: p, ok: ok}
	}
}

// mxResult takes the answer to a lookup, unless it has been superseded or the
// user picked a provider by hand while it ran.
func (m setupModel) mxResult(msg mxResultMsg) (setupModel, tea.Cmd) {
	if !m.looking || msg.seq != m.mxSeq {
		return m, nil
	}
	m.looking = false
	if m.choice != autoChoice {
		return m, nil
	}

	if msg.ok {
		m.adopt(msg.p, "detected "+providerLabel(msg.p)+" via MX")
	} else {
		m.adopt(config.ProviderGeneric, "no provider matched the MX records for "+msg.domain)
	}
	if m.submitAfterMX {
		m.submitAfterMX = false
		return m.submit()
	}
	return m, nil
}

func (m setupModel) update(msg tea.KeyPressMsg) (setupModel, tea.Cmd) {
	if m.saving || m.signingIn {
		return m, nil
	}
	m.clampFocus()
	vis := m.visible()
	cur := vis[m.focus]

	switch msg.String() {
	case "tab", "down":
		return m.moveFocus(1)
	case "shift+tab", "up":
		return m.moveFocus(-1)
	case "f2":
		m.advanced = !m.advanced
		m.clampFocus()
		return m, nil
	case "f3":
		if m.mode == setupFirstRun || !m.status.HasToken {
			return m, nil
		}
		return m, func() tea.Msg { return signOutMsg{} }
	case "enter":
		return m.submit()
	}

	if cur == fProvider || cur == fAuthMethod {
		delta := 0
		switch msg.String() {
		case "left":
			delta = -1
		case "right", "space", " ":
			delta = 1
		}
		if delta == 0 {
			return m, nil
		}
		if cur == fAuthMethod {
			return m.cycleAuth(delta)
		}
		return m.cycle(delta)
	}

	switch msg.String() {
	case "backspace":
		m.fields[cur].backspace()
	case "ctrl+u":
		m.fields[cur].clear()
	default:
		m.fields[cur].insert(msg.Text)
	}
	if cur == fEmail {
		m.resolve()
		m.clampFocus()
	}
	m.err = ""
	return m, nil
}

// paste drops a whole pasted block into the focused field. The picker takes
// no text.
func (m setupModel) paste(text string) (setupModel, tea.Cmd) {
	if m.saving || m.signingIn {
		return m, nil
	}
	m.clampFocus()
	cur := m.visible()[m.focus]
	if cur == fProvider || cur == fAuthMethod {
		return m, nil
	}
	m.fields[cur].insert(text)
	if cur == fEmail {
		m.resolve()
		m.clampFocus()
	}
	m.err = ""
	return m, nil
}

// moveFocus steps between fields. Leaving the address is the moment to ask
// the resolver about a domain the table did not know.
func (m setupModel) moveFocus(delta int) (setupModel, tea.Cmd) {
	m.clampFocus()
	vis := m.visible()
	prev := vis[m.focus]
	m.focus = (m.focus + delta + len(vis)) % len(vis)
	if prev == fEmail {
		return m, m.lookupMX()
	}
	return m, nil
}

// cycle moves the provider picker one stop.
func (m setupModel) cycle(delta int) (setupModel, tea.Cmd) {
	m.choice = (m.choice + delta + len(providerChoices)) % len(providerChoices)
	m.err = ""
	m.resolve()
	m.clampFocus()
	if m.choice == autoChoice {
		return m, m.lookupMX()
	}
	return m, nil
}

// cycleAuth moves the auth-method picker one stop, unless the provider takes
// nothing but OAuth, in which case there is only one stop.
func (m setupModel) cycleAuth(delta int) (setupModel, tea.Cmd) {
	if m.oauthOnly() {
		m.note = microsoftNoAppPasswords
		return m, nil
	}
	m.authChoice = (m.authChoice + delta + len(authChoiceLabels)) % len(authChoiceLabels)
	m.err = ""
	m.clampFocus()
	return m, nil
}

func (m setupModel) submit() (setupModel, tea.Cmd) {
	if m.looking {
		m.submitAfterMX = true
		m.err = ""
		return m, nil
	}
	if cmd := m.lookupMX(); cmd != nil {
		m.submitAfterMX = true
		m.err = ""
		return m, cmd
	}

	acct, password, err := m.account()
	if err != nil {
		m.err = err.Error()
		if m.proton {
			m.advanced = true
		}
		return m, nil
	}
	m.err = ""
	m.saving = true
	add := m.mode == setupAdd
	return m, func() tea.Msg { return setupSubmitMsg{acct: acct, password: password, add: add} }
}

// account validates the form into an account plus the password to store.
func (m setupModel) account() (config.Account, string, error) {
	get := func(i int) string { return strings.TrimSpace(m.fields[i].value) }

	name := get(fName)
	if name == "" {
		return config.Account{}, "", fmt.Errorf("account name is required")
	}
	if !config.ValidAccountName(name) {
		return config.Account{}, "", fmt.Errorf("%s", config.AccountNameRule)
	}
	if m.mode == setupAdd {
		for _, have := range m.existing {
			if have == name {
				return config.Account{}, "", fmt.Errorf("an account named %q is already configured", name)
			}
		}
	}
	email := get(fEmail)
	if email == "" || !strings.Contains(email, "@") {
		return config.Account{}, "", fmt.Errorf("an email address is required")
	}
	if m.resolved == "" {
		return config.Account{}, "", fmt.Errorf("no provider yet; pick one with left/right on the provider field")
	}

	authSpec := "file"
	password := ""
	var oauthCfg *config.OAuth
	if m.useOAuth() {
		clientID := get(fClientID)
		if clientID == "" {
			return config.Account{}, "", fmt.Errorf("OAuth sign-in needs a client ID; create one first, see docs/oauth.md")
		}
		authSpec = "oauth"
		oauthCfg = &config.OAuth{
			Provider: config.OAuthProviderFor(m.resolved),
			ClientID: clientID,
		}
		if m.resolved == config.ProviderGmail {
			oauthCfg.ClientSecret = strings.TrimSpace(m.fields[fClientSecret].value)
		}
	} else {
		password = m.fields[fPassword].value
		if strings.TrimSpace(password) == "" {
			return config.Account{}, "", fmt.Errorf("an app password is required")
		}
	}
	host := get(fHost)
	if host == "" {
		return config.Account{}, "", fmt.Errorf("an IMAP host is required; press f2 for the advanced fields")
	}
	port, err := strconv.Atoi(get(fPort))
	if err != nil || port <= 0 {
		return config.Account{}, "", fmt.Errorf("invalid IMAP port %q", get(fPort))
	}

	acct := config.Account{
		Name:     name,
		Host:     host,
		Port:     port,
		Username: email,
		Auth:     authSpec,
		Folders:  splitList(get(fFolders)),
		OAuth:    oauthCfg,
	}
	if smtpHost := get(fSMTPHost); smtpHost != "" {
		smtpPort, err := strconv.Atoi(get(fSMTPPort))
		if err != nil || smtpPort <= 0 {
			return config.Account{}, "", fmt.Errorf("invalid SMTP port %q", get(fSMTPPort))
		}
		acct.SMTP = &config.SMTP{Host: smtpHost, Port: smtpPort}
	}
	return acct, password, nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// view renders the form into width columns and exactly height rows.
func (m setupModel) view(width, height int) []string {
	if m.signingIn {
		return m.signInView(width, height)
	}

	s := m.styles
	vis := m.visible()

	const labelW = 15
	inputW := width - 4 - labelW - 1
	if inputW < 8 {
		inputW = 8
	}

	body := make([]string, 0, len(vis)+4)
	for i, idx := range vis {
		f := m.fields[idx]
		label := pad(f.label, labelW)
		if i == m.focus {
			label = s.accent.Render(label)
		} else {
			label = s.subtle.Render(label)
		}
		value := f.render(s, i == m.focus, inputW)
		switch idx {
		case fProvider:
			value = m.renderChoice(s, providerChoices[m.choice].label, i == m.focus, inputW)
		case fAuthMethod:
			if m.oauthOnly() {
				value = pad(s.dim.Render(authChoiceLabels[authOAuth]+"   app passwords not accepted by Microsoft"), inputW)
				break
			}
			value = m.renderChoice(s, authChoiceLabels[m.authChoice], i == m.focus, inputW)
		}
		body = append(body, label+" "+value)
	}
	if m.note != "" {
		body = append(body, "", s.subtle.Render(cell(m.note)))
	}

	panelH := height - 3 // the note line below the panel plus its two borders
	if panelH < 3 {
		panelH = 3
	}
	out := strings.Split(s.panel(s.boxFocus, m.title(), body, width, panelH), "\n")

	switch {
	case m.err != "":
		out = append(out, s.err.Render(truncate(cell(m.err), width)))
	case m.saving:
		out = append(out, s.subtle.Render("saving..."))
	case m.mode != setupFirstRun:
		out = append(out, s.subtle.Render(truncate(m.statusLine(), width)))
	default:
		out = append(out, s.subtle.Render(truncate("config "+cell(m.configPath), width)))
	}
	return out
}

// title names the form for what it is doing.
func (m setupModel) title() string {
	switch m.mode {
	case setupAdd:
		return "add an account"
	case setupEdit:
		return "account"
	default:
		return "set up your mailbox"
	}
}

// statusLine describes the credential this account already has, so the
// account screen answers "am I signed in, with what, until when" without a
// separate command.
func (m setupModel) statusLine() string {
	st := m.status
	switch {
	case st.Err != "":
		return "stored: " + st.Err
	case st.HasToken:
		provider := nonEmpty(st.Provider, "oauth")
		if st.Expiry.IsZero() {
			return "stored: " + provider + " token, no expiry recorded"
		}
		return "stored: " + provider + " token, access token expires " +
			st.Expiry.Local().Format("2006-01-02 15:04") + " (refreshed automatically)"
	case m.useOAuth():
		return "stored: no token yet; enter saves and signs in"
	case st.HasPassword:
		return "stored: app password"
	default:
		return "stored: nothing yet"
	}
}

// renderChoice draws one picker: the current stop between arrows while it has
// the focus, since left and right are what move it.
func (m setupModel) renderChoice(s styles, label string, focused bool, width int) string {
	if focused {
		return s.prompt.Render(pad("< "+label+" >", width))
	}
	return pad(label, width)
}

// signInView replaces the form while the browser sign-in runs. The URL is
// shown in full, wrapped, so it can be copied when the browser did not open.
func (m setupModel) signInView(width, height int) []string {
	s := m.styles

	body := []string{
		m.spinner.View() + " waiting for the browser sign-in...",
		"",
		s.subtle.Render("A browser tab should have opened. If it did not, open this URL:"),
	}
	if m.authURL == "" {
		body = append(body, s.dim.Render("preparing the sign-in URL..."))
	} else {
		body = append(body, wrapAt(m.authURL, width-4)...)
	}
	body = append(body, "", s.subtle.Render("The token is stored locally; mailshear never sees your password."))

	panelH := height - 3
	if panelH < 3 {
		panelH = 3
	}
	out := strings.Split(s.panel(s.boxFocus, "sign in", body, width, panelH), "\n")
	if m.err != "" {
		return append(out, s.err.Render(truncate(cell(m.err), width)))
	}
	return append(out, s.subtle.Render("c cancels and returns to the form"))
}

// wrapAt breaks a long single-token string (an authorization URL) into lines
// of at most width columns, so nothing is truncated away from a copy.
func wrapAt(str string, width int) []string {
	if width < 8 {
		width = 8
	}
	r := []rune(str)
	var out []string
	for len(r) > width {
		out = append(out, string(r[:width]))
		r = r[width:]
	}
	return append(out, string(r))
}

func (m setupModel) help() string {
	if m.signingIn {
		return "c cancel the sign-in  ctrl+c quit"
	}
	adv := "f2 advanced"
	if m.advanced {
		adv = "f2 basic"
	}
	pickers := "left/right provider"
	if m.oauthOffered() {
		pickers = "left/right pickers"
	}
	tail := "esc quit"
	save := "enter save and scan"
	if m.mode != setupFirstRun {
		tail = "esc back"
		save = "enter save"
		if m.status.HasToken {
			tail = "f3 sign out  " + tail
		}
	}
	return "tab move  " + pickers + "  " + save + "  " + adv + "  ctrl+u clear  " + tail
}
