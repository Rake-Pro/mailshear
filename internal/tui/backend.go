package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/creds"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/keep"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/scan"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/undo"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// LiveBackend is the real Backend: the one thing in this package that opens
// the database, talks to IMAP and writes the audit log. Everything above it
// is screens and layout.
type LiveBackend struct {
	cfg        *config.Config
	configPath string
	dataDir    string

	acct   config.Account
	secret unsub.Secret
	st     *store.Store
	cl     *imapx.Client
	sacct  store.Account

	prepared *apply.Prepared

	// OpenURL hands the OAuth authorization URL to the system browser. nil
	// means the user opens the URL the screen shows.
	OpenURL func(string) error
	// Version is the build version, reported by Paths.
	Version string
}

// NewBackend builds the live backend. cfg may be nil: the setup screen writes
// one and SaveSetup loads it back.
func NewBackend(cfg *config.Config, configPath, dataDir string) *LiveBackend {
	return &LiveBackend{cfg: cfg, configPath: configPath, dataDir: dataDir}
}

// Close releases the database and the IMAP connection. The caller owns the
// backend's lifetime, so this is not part of the Backend interface.
func (b *LiveBackend) Close() {
	if b.cl != nil {
		b.cl.Close()
		b.cl = nil
	}
	if b.st != nil {
		b.st.Close()
		b.st = nil
	}
}

func (b *LiveBackend) HasCredentials(a config.Account) bool {
	if a.Auth == "oauth" {
		return creds.HasToken(b.dataDir, a)
	}
	_, err := creds.Resolve(b.dataDir, a)
	return err == nil
}

// SaveSetup writes the account into the config file and stores its password.
// An account already in the file is replaced in place, so adding or editing
// one never disturbs the others or the file's comments.
func (b *LiveBackend) SaveSetup(a config.Account, password string) (config.Account, error) {
	protect := config.ExampleProtect()
	if b.cfg != nil {
		protect = b.cfg.Protect
	}
	if err := config.UpsertAccount(b.configPath, a, protect); err != nil {
		return config.Account{}, err
	}
	// An OAuth account has no password to store: the browser sign-in that
	// follows writes the token instead.
	if a.Auth != "oauth" {
		if err := creds.Set(b.dataDir, a, creds.Normalize(a, password)); err != nil {
			return config.Account{}, fmt.Errorf("storing the password for %s: %w", a.Name, err)
		}
	}

	// Read the file back so the account carries the same defaults any other
	// command would see.
	cfg, err := config.Load(b.configPath)
	if err != nil {
		return config.Account{}, err
	}
	b.cfg = cfg
	saved, err := cfg.Account(a.Name)
	if err != nil {
		return config.Account{}, err
	}
	return saved, nil
}

// OAuthLogin runs the browser sign-in for an OAuth account and stores the
// token. onURL receives the authorization URL first, so the screen can show
// it for copying when the browser does not open.
func (b *LiveBackend) OAuthLogin(ctx context.Context, a config.Account, onURL func(string)) error {
	cl, err := creds.OAuthClient(a)
	if err != nil {
		return err
	}
	cl.OpenURL = b.OpenURL
	tok, err := cl.Login(ctx, onURL)
	if err != nil {
		return err
	}
	return creds.SaveToken(b.dataDir, a, tok)
}

// AuthStatus reports what credential is on disk for an account: a password, an
// OAuth token with its expiry, or nothing yet.
func (b *LiveBackend) AuthStatus(a config.Account) AuthStatus {
	var st AuthStatus
	if _, err := creds.Resolve(b.dataDir, a); err == nil {
		st.HasPassword = true
	}
	if a.Auth != "oauth" {
		return st
	}
	spec, err := a.OAuthSpec()
	if err != nil {
		st.Err = err.Error()
		return st
	}
	st.Provider = spec.Name
	tok, err := creds.LoadToken(b.dataDir, a)
	if err != nil {
		return st
	}
	st.HasToken = true
	st.Expiry = tok.Expiry
	return st
}

// SignOut removes whatever credential the account has stored locally: the
// OAuth token, the app password, or both. Access at the provider is
// untouched: that is revoked from the provider's own account page.
func (b *LiveBackend) SignOut(a config.Account) error {
	tokErr := creds.DeleteToken(b.dataDir, a)
	if errors.Is(tokErr, creds.ErrNoToken) {
		tokErr = nil
	}
	pwErr := creds.Delete(b.dataDir, a)
	if errors.Is(pwErr, creds.ErrNoPassword) {
		pwErr = nil
	}
	if tokErr != nil {
		return tokErr
	}
	return pwErr
}

func (b *LiveBackend) Connect(ctx context.Context, a config.Account) error {
	// Switching accounts mid-session must not reuse the previous mailbox's
	// connection or its prepared plan.
	if b.cl != nil && (b.acct.Host != a.Host || b.acct.Username != a.Username) {
		b.cl.Close()
		b.cl = nil
		b.prepared = nil
	}
	b.acct = a
	sec, err := b.resolveSecret(ctx, a)
	if err != nil {
		return err
	}
	b.secret = sec

	if _, err := b.openStore(); err != nil {
		return err
	}
	if b.cl == nil {
		var cl *imapx.Client
		if sec.AccessToken != "" {
			cl, err = imapx.DialXOAUTH2(ctx, a.Host, a.Port, a.Username, sec.AccessToken)
		} else {
			cl, err = imapx.Dial(ctx, a.Host, a.Port, a.Username, sec.Password)
		}
		if err != nil {
			return err
		}
		b.cl = cl
	}
	sacct, err := b.st.UpsertAccount(ctx, a.Name, a.Host, a.Username)
	if err != nil {
		return err
	}
	b.sacct = sacct
	return nil
}

// resolveSecret loads the account's app password, or refreshes and returns an
// OAuth access token.
func (b *LiveBackend) resolveSecret(ctx context.Context, a config.Account) (unsub.Secret, error) {
	if a.Auth == "oauth" {
		token, err := creds.AccessToken(ctx, b.dataDir, a, nil)
		if err != nil {
			return unsub.Secret{}, err
		}
		return unsub.Secret{AccessToken: token}, nil
	}
	pw, err := creds.Resolve(b.dataDir, a)
	if err != nil {
		return unsub.Secret{}, err
	}
	return unsub.Secret{Password: pw}, nil
}

func (b *LiveBackend) Scan(ctx context.Context, onProgress func(scan.Progress)) (scan.Summary, error) {
	prefilter := ""
	if b.cfg != nil {
		prefilter = b.cfg.Scan.GmailPrefilter
	}
	km, err := b.keepMatcher()
	if err != nil {
		return scan.Summary{}, err
	}
	sum, err := scan.Run(ctx, b.st, b.cl, b.sacct, b.acct.EffectiveFolders(), scan.Options{
		Prefilter:       prefilter,
		Keep:            km,
		AllMailFallback: b.acct.Provider() == config.ProviderGmail && !b.acct.FoldersExplicit(),
		OnProgress:      onProgress,
	})
	if err == nil {
		// Best effort: a scan that worked is not a failure because the
		// timestamp could not be recorded.
		_ = b.st.SetLastScan(ctx, b.sacct.ID, time.Now())
	}
	return sum, err
}

func (b *LiveBackend) Load(ctx context.Context) ([]store.SenderGroup, map[string]bool, error) {
	groups, err := b.st.SenderGroups(ctx, b.sacct.ID)
	if err != nil {
		return nil, nil, err
	}
	keys, err := b.st.ListProtected(ctx, b.sacct.ID)
	if err != nil {
		return nil, nil, err
	}
	protected := make(map[string]bool, len(keys))
	for _, k := range keys {
		protected[k] = true
	}
	return groups, protected, nil
}

func (b *LiveBackend) Protect(ctx context.Context, keys []string) error {
	for _, k := range keys {
		if err := b.st.SetProtected(ctx, b.sacct.ID, k, true); err != nil {
			return err
		}
	}
	return nil
}

func (b *LiveBackend) WritePlan(ctx context.Context, groups []store.SenderGroup, decisions []store.Decision) (*plan.Plan, string, error) {
	runID := plan.NewRunID()
	if err := b.st.RecordDecisions(ctx, b.sacct.ID, runID, decisions); err != nil {
		return nil, "", err
	}
	count, err := b.st.MessageCount(ctx, b.sacct.ID)
	if err != nil {
		return nil, "", err
	}
	p := plan.FromDecisions(runID, b.acct.Name, count, groups, decisions)
	path := plan.Path(b.dataDir, runID)
	if err := plan.Write(path, p); err != nil {
		return nil, "", err
	}
	return p, path, nil
}

// keepMatcher builds the transactional-mail rule from protect:. With no
// config loaded yet the zero Matcher applies, which is the rule at its
// default: on, with the built-in phrase list.
func (b *LiveBackend) keepMatcher() (keep.Matcher, error) {
	if b.cfg == nil {
		return keep.Matcher{}, nil
	}
	return keep.New(b.cfg.Protect.KeepTransactional, b.cfg.Protect.KeepSubjects)
}

func (b *LiveBackend) Prepare(ctx context.Context, p *plan.Plan) (apply.Preview, error) {
	protect := config.Protect{}
	unsubOpts := config.UnsubscribeOpts{}
	if b.cfg != nil {
		protect = b.cfg.Protect
		unsubOpts = b.cfg.Unsubscribe
	}
	km, err := b.keepMatcher()
	if err != nil {
		return apply.Preview{}, err
	}
	pd, err := apply.Prepare(ctx, apply.Deps{
		Store:   b.st,
		Client:  b.cl,
		Account: b.sacct,
		Cfg:     b.acct,
		Protect: protect,
		Keep:    km,
		Unsub:   unsub.FromConfig(unsubOpts, b.acct, b.secret),
		DataDir: b.dataDir,
	}, p)
	if err != nil {
		return apply.Preview{}, err
	}
	b.prepared = pd
	return pd.Preview, nil
}

func (b *LiveBackend) Execute(ctx context.Context, opts apply.ExecOptions) (apply.Summary, error) {
	if b.prepared == nil {
		return apply.Summary{}, errors.New("nothing prepared to execute")
	}
	return b.prepared.Execute(ctx, opts)
}

func (b *LiveBackend) Undo(ctx context.Context, runID string) (int, int, error) {
	return undo.Run(ctx, b.st, b.cl, b.sacct, b.dataDir, runID, true, nil, io.Discard)
}
