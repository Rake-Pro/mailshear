package tui

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/creds"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// The accounts, history and maintenance screens sit outside the scan-apply
// pipeline, so their backing operations live here rather than in backend.go.
// None of them opens an IMAP connection: everything is the config file, the
// credential store, the local database and the audit logs.

// openStore opens the database without connecting to a mail server, so the
// accounts screen can describe every account before one is chosen.
func (b *LiveBackend) openStore() (*store.Store, error) {
	if b.st != nil {
		return b.st, nil
	}
	path := b.dbPath()
	st, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	b.st = st
	return st, nil
}

func (b *LiveBackend) dbPath() string { return filepath.Join(b.dataDir, "mailshear.db") }

// storeAccount finds the account's database row, creating it when a scan
// never has. It is what lets protect and purge work before the first scan.
func (b *LiveBackend) storeAccount(ctx context.Context, a config.Account) (store.Account, error) {
	st, err := b.openStore()
	if err != nil {
		return store.Account{}, err
	}
	return st.UpsertAccount(ctx, a.Name, a.Host, a.Username)
}

func (b *LiveBackend) ListAccounts(ctx context.Context) ([]AccountStatus, error) {
	if b.cfg == nil {
		return nil, nil
	}
	st, err := b.openStore()
	if err != nil {
		return nil, err
	}

	out := make([]AccountStatus, 0, len(b.cfg.Accounts))
	for _, a := range b.cfg.Accounts {
		row := AccountStatus{
			Account:  a,
			Provider: shortProviderLabel(a.Provider()),
			Auth:     "app password",
		}
		if a.Auth == "oauth" {
			row.Auth = "OAuth"
		}
		auth := b.AuthStatus(a)
		row.Credential = credentialLabel(a, auth)
		row.Err = auth.Err

		sacct, err := st.GetAccount(ctx, a.Name)
		if errors.Is(err, store.ErrNotFound) {
			out = append(out, row)
			continue
		}
		if err != nil {
			row.Err = err.Error()
			out = append(out, row)
			continue
		}
		stats, err := st.Stats(ctx, sacct.ID)
		if err != nil {
			row.Err = err.Error()
			out = append(out, row)
			continue
		}
		row.LastScan = stats.LastScan
		row.Messages = stats.Messages
		row.StillSending = stats.StillSending
		out = append(out, row)
	}
	return out, nil
}

// shortProviderLabel is the provider as a table cell: the picker's full label
// is a sentence, which a column is not.
func shortProviderLabel(p config.Provider) string {
	switch p {
	case config.ProviderGmail:
		return "Gmail"
	case config.ProviderFastmail:
		return "Fastmail"
	case config.ProviderOutlook:
		return "Outlook"
	case config.ProviderICloud:
		return "iCloud"
	case config.ProviderProton:
		return "Proton"
	default:
		return "IMAP"
	}
}

// credentialLabel says what is on disk for an account in one phrase.
func credentialLabel(a config.Account, st AuthStatus) string {
	if a.Auth == "oauth" {
		switch {
		case !st.HasToken:
			return "missing"
		case st.Expiry.IsZero():
			return "token stored"
		default:
			return st.Expiry.Local().Format("2006-01-02 15:04")
		}
	}
	if strings.HasPrefix(a.Auth, "env:") || strings.HasPrefix(a.Auth, "file:") {
		if st.HasPassword {
			return a.Auth
		}
		return "missing (" + a.Auth + ")"
	}
	if st.HasPassword {
		return "password stored"
	}
	return "missing"
}

func (b *LiveBackend) AddAccount(a config.Account, password string) (config.Account, error) {
	if b.cfg != nil {
		for _, have := range b.cfg.Accounts {
			if have.Name == a.Name {
				return config.Account{}, fmt.Errorf("an account named %q is already configured", a.Name)
			}
		}
	}
	return b.SaveSetup(a, password)
}

func (b *LiveBackend) RemoveAccount(ctx context.Context, a config.Account, purgeData bool) error {
	if err := config.RemoveAccount(b.configPath, a.Name); err != nil {
		return err
	}
	// A missing credential is not a failure here: the account is going away
	// either way, and half-removing it would be worse.
	if err := creds.Delete(b.dataDir, a); err != nil && !errors.Is(err, creds.ErrNoPassword) {
		return err
	}
	if err := creds.DeleteToken(b.dataDir, a); err != nil && !errors.Is(err, creds.ErrNoToken) {
		return err
	}

	if purgeData {
		st, err := b.openStore()
		if err != nil {
			return err
		}
		sacct, err := st.GetAccount(ctx, a.Name)
		if err == nil {
			if err := st.DeleteAccount(ctx, sacct.ID); err != nil {
				return err
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}

	cfg, err := config.Load(b.configPath)
	if err != nil {
		// The account is gone from the file; an unreadable rest of the file
		// is a separate problem and the caller shows it.
		return err
	}
	b.cfg = cfg
	return nil
}

func (b *LiveBackend) Runs(ctx context.Context, a config.Account) ([]RunInfo, error) {
	st, err := b.openStore()
	if err != nil {
		return nil, err
	}
	sacct, err := st.GetAccount(ctx, a.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runs, err := st.ListRuns(ctx, sacct.ID)
	if err != nil {
		return nil, err
	}
	out := make([]RunInfo, 0, len(runs))
	for _, r := range runs {
		out = append(out, RunInfo{ID: r.ID, StartedAt: r.StartedAt, Summary: r.Summary})
	}
	return out, nil
}

// RunDetail replays a run's audit log. The log is the record of what
// happened, so the counts here are what the run actually did rather than what
// it set out to do.
func (b *LiveBackend) RunDetail(ctx context.Context, runID string) (RunDetail, error) {
	if !apply.ValidRunID(runID) {
		return RunDetail{}, fmt.Errorf("%q is not a run id", runID)
	}
	d := RunDetail{
		RunInfo:   RunInfo{ID: runID},
		AuditPath: apply.AuditPath(b.dataDir, runID),
		Skipped:   map[string]int{},
		Unsub:     map[string]int{},
	}
	if st, err := b.openStore(); err == nil {
		if r, err := st.GetRun(ctx, runID); err == nil {
			d.StartedAt, d.Summary = r.StartedAt, r.Summary
		}
	}

	lines, err := apply.ReadLines(d.AuditPath)
	if err != nil {
		d.Err = err.Error()
		return d, nil
	}
	for _, l := range lines {
		switch l.Action {
		case "move":
			if l.Status == "done" {
				d.Moved += len(l.UIDs)
			}
		case "skip":
			d.Skipped[nonEmpty(l.Status, "skipped")]++
		case "unsubscribe":
			d.Unsub[l.Status]++
			switch unsub.Status(l.Status) {
			case unsub.StatusManual, unsub.StatusProbable, unsub.StatusFailed:
				url := l.FinalURL
				if url == "" {
					url = l.URI
				}
				d.Manual = append(d.Manual, ManualLink{
					Display: nonEmpty(l.From, l.SenderKey), Status: l.Status, URL: url,
				})
			}
		case "error":
			d.Skipped["error"]++
		}
	}
	return d, nil
}

func (b *LiveBackend) Purge(ctx context.Context, a config.Account) error {
	sacct, err := b.storeAccount(ctx, a)
	if err != nil {
		return err
	}
	return b.st.PurgeMessages(ctx, sacct.ID)
}

func (b *LiveBackend) ResetFolders(ctx context.Context, a config.Account) error {
	sacct, err := b.storeAccount(ctx, a)
	if err != nil {
		return err
	}
	for _, folder := range a.EffectiveFolders() {
		// UIDVALIDITY 0 makes the next scan treat the folder as new.
		if err := b.st.ResetFolder(ctx, sacct.ID, folder, 0); err != nil {
			return err
		}
	}
	return nil
}

// Export writes the sender groups on screen to a CSV and a JSON file under
// <data-dir>/exports. Both are the user's own data leaving the database on
// their own instruction; nothing is sent anywhere.
func (b *LiveBackend) Export(_ context.Context, a config.Account, groups []store.SenderGroup) (string, string, error) {
	dir := filepath.Join(b.dataDir, "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("creating %s: %w", dir, err)
	}
	base := filepath.Join(dir, safeFileName(a.Name)+"-"+time.Now().Format("20060102-150405"))
	csvPath, jsonPath := base+".csv", base+".json"

	if err := writeFile(csvPath, func(f *os.File) error { return writeGroupsCSV(f, groups) }); err != nil {
		return "", "", err
	}
	if err := writeFile(jsonPath, func(f *os.File) error { return writeGroupsJSON(f, groups) }); err != nil {
		return "", "", err
	}
	return csvPath, jsonPath, nil
}

// safeFileName keeps an account name from reaching outside the export
// directory or naming something unexpected.
func safeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}

func writeFile(path string, write func(*os.File) error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := write(f); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
}

// csvCell defuses a cell a spreadsheet would otherwise read as a formula.
// Display names, subjects and URIs come from mail, so a sender can choose to
// start one with =, +, - or @; Excel and LibreOffice both evaluate that on
// open. A leading apostrophe makes the cell text, which is what it is.
func csvCell(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@':
		return "'" + v
	}
	return v
}

func csvRow(values ...string) []string {
	for i, v := range values {
		values[i] = csvCell(v)
	}
	return values
}

func writeGroupsCSV(f *os.File, groups []store.SenderGroup) error {
	cw := csv.NewWriter(f)
	if err := cw.Write([]string{
		"count", "mixed", "kept", "flagged", "replied", "method", "unsub_status", "still_sending",
		"first_seen", "last_seen", "size", "display", "address", "list_id", "domain",
		"sender_key", "decision", "latest_uri",
	}); err != nil {
		return err
	}
	for _, g := range groups {
		latest := ""
		if len(g.LatestURIs) > 0 {
			latest = g.LatestURIs[0]
		}
		if err := cw.Write(csvRow(
			strconv.Itoa(g.Count), strconv.Itoa(g.MixedCount), strconv.Itoa(g.KeepCount),
			strconv.Itoa(g.FlaggedCount), strconv.Itoa(g.RepliedCount),
			g.Method, g.UnsubStatus, strconv.FormatBool(g.StillSending),
			dayString(g.FirstSeen), dayString(g.LastSeen), strconv.FormatInt(g.TotalSize, 10),
			g.Display, g.Address, g.ListID, g.DomainKey, g.SenderKey, g.Decision, latest,
		)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeGroupsJSON(f *os.File, groups []store.SenderGroup) error {
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(groups)
}

func (b *LiveBackend) ListProtected(ctx context.Context, a config.Account) ([]ProtectedEntry, error) {
	sacct, err := b.storeAccount(ctx, a)
	if err != nil {
		return nil, err
	}
	keys, err := b.st.ListProtected(ctx, sacct.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ProtectedEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, ProtectedEntry{Key: k, Source: "store"})
	}
	if b.cfg != nil {
		for _, d := range b.cfg.Protect.Domains {
			out = append(out, ProtectedEntry{Key: d, Source: "config domain"})
		}
		for _, addr := range b.cfg.Protect.Addresses {
			out = append(out, ProtectedEntry{Key: addr, Source: "config address"})
		}
		for _, l := range b.cfg.Protect.ListIDs {
			out = append(out, ProtectedEntry{Key: l, Source: "config list-id"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (b *LiveBackend) Unprotect(ctx context.Context, a config.Account, key string) error {
	sacct, err := b.storeAccount(ctx, a)
	if err != nil {
		return err
	}
	return b.st.SetProtected(ctx, sacct.ID, key, false)
}

func (b *LiveBackend) Paths() PathInfo {
	p := PathInfo{
		Version:    b.Version,
		ConfigPath: b.configPath,
		DataDir:    b.dataDir,
		DBPath:     b.dbPath(),
	}
	if fi, err := os.Stat(p.DBPath); err == nil {
		p.DBSize = fi.Size()
	}
	return p
}
