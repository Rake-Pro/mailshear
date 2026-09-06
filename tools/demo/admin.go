package main

import (
	"context"
	"time"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/tui"
)

// The accounts, history and maintenance screens answer from the same made-up
// mailbox as the rest of the demo: no config file is read or written, no
// database is opened, and nothing is exported anywhere.

// demoAccounts is the two-mailbox setup the accounts screen shows: one on an
// app password, one signed in with OAuth.
func demoAccounts() []config.Account {
	return []config.Account{
		{
			Name: "personal", Host: "imap.gmail.com", Port: 993,
			Username: "casey@example.com", Auth: "file",
			Folders: []string{"[Gmail]/All Mail"},
			SMTP:    &config.SMTP{Host: "smtp.gmail.com", Port: 587},
		},
		{
			Name: "work", Host: "outlook.office365.com", Port: 993,
			Username: "casey@work.example", Auth: "oauth",
			Folders: []string{"INBOX", "Archive"},
			OAuth:   &config.OAuth{Provider: "microsoft", ClientID: "demo-client-id"},
		},
	}
}

func (b *demoBackend) ListAccounts(ctx context.Context) ([]tui.AccountStatus, error) {
	if !b.pause(ctx, 200) {
		return nil, ctx.Err()
	}
	accts := demoAccounts()
	now := time.Now()
	return []tui.AccountStatus{
		{
			Account: accts[0], Provider: "Gmail", Auth: "app password",
			Credential: "password stored", LastScan: now.Add(-2 * time.Hour),
			Messages: 48212, StillSending: 3,
		},
		{
			Account: accts[1], Provider: "Outlook", Auth: "OAuth",
			Credential: now.Add(50 * time.Minute).Format("2006-01-02 15:04"),
			LastScan:   now.Add(-6 * 24 * time.Hour), Messages: 12894, StillSending: 1,
		},
	}, nil
}

func (b *demoBackend) AddAccount(a config.Account, password string) (config.Account, error) {
	return b.SaveSetup(a, password)
}

func (b *demoBackend) RemoveAccount(ctx context.Context, _ config.Account, _ bool) error {
	if !b.pause(ctx, 300) {
		return ctx.Err()
	}
	return nil
}

func (b *demoBackend) Runs(ctx context.Context, _ config.Account) ([]tui.RunInfo, error) {
	if !b.pause(ctx, 200) {
		return nil, ctx.Err()
	}
	now := time.Now()
	return []tui.RunInfo{
		{ID: demoRunID, StartedAt: now.Add(-40 * time.Minute),
			Summary: "unsubscribed 9, moved 1204 message(s) to Trash"},
		{ID: "20260830-091455-31ab", StartedAt: now.Add(-7 * 24 * time.Hour),
			Summary: "unsubscribed 4, moved 318 message(s) to Trash"},
		{ID: "20260812-201002-c07e", StartedAt: now.Add(-25 * 24 * time.Hour),
			Summary: "unsubscribed 12, moved 2967 message(s) to Trash"},
	}, nil
}

func (b *demoBackend) RunDetail(ctx context.Context, runID string) (tui.RunDetail, error) {
	if !b.pause(ctx, 250) {
		return tui.RunDetail{}, ctx.Err()
	}
	return tui.RunDetail{
		RunInfo: tui.RunInfo{
			ID: runID, StartedAt: time.Now().Add(-40 * time.Minute),
			Summary: "unsubscribed 9, moved 1204 message(s) to Trash",
		},
		AuditPath: demoAuditPath,
		Moved:     1204,
		Skipped:   map[string]int{"kept: receipt": 18, "flagged": 2, "identity mismatch": 1},
		Unsub:     map[string]int{"ok": 6, "probable": 2, "manual": 1},
		Manual: []tui.ManualLink{
			{Display: "Persistent Deals", Status: "manual", URL: ""},
			{Display: "Weekly Roundup", Status: "probable", URL: "https://news.example/u/9f3c?confirm=1"},
		},
	}, nil
}

func (b *demoBackend) Purge(ctx context.Context, _ config.Account) error {
	if !b.pause(ctx, 600) {
		return ctx.Err()
	}
	return nil
}

func (b *demoBackend) ResetFolders(ctx context.Context, _ config.Account) error {
	if !b.pause(ctx, 400) {
		return ctx.Err()
	}
	return nil
}

func (b *demoBackend) Export(ctx context.Context, _ config.Account, _ []store.SenderGroup) (string, string, error) {
	if !b.pause(ctx, 500) {
		return "", "", ctx.Err()
	}
	base := demoExportDir + "/personal-20260906-142430"
	return base + ".csv", base + ".json", nil
}

func (b *demoBackend) ListProtected(ctx context.Context, _ config.Account) ([]tui.ProtectedEntry, error) {
	if !b.pause(ctx, 250) {
		return nil, ctx.Err()
	}
	return []tui.ProtectedEntry{
		{Key: "addr:alerts@bank.example", Source: "store"},
		{Key: "bank.example", Source: "config domain"},
		{Key: "list:statements.utility.example", Source: "store"},
	}, nil
}

func (b *demoBackend) Unprotect(ctx context.Context, _ config.Account, _ string) error {
	if !b.pause(ctx, 200) {
		return ctx.Err()
	}
	return nil
}

func (b *demoBackend) Paths() tui.PathInfo {
	return tui.PathInfo{
		Version:    demoVersion,
		ConfigPath: demoConfigPath,
		DataDir:    demoDataDir,
		DBPath:     demoDataDir + "/mailshear.db",
		DBSize:     41_582_592,
	}
}
