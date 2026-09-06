package tui

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// Microsoft withdrew app-password IMAP, so offering the choice and letting the
// server refuse it later is worse than not offering it: the form starts on
// OAuth, will not move off it, and says why.
func TestSetupOutlookDefaultsToOAuth(t *testing.T) {
	m := setupAt("me@outlook.com", fEmail)
	if m.resolved != config.ProviderOutlook {
		t.Fatalf("provider = %q, want outlook", m.resolved)
	}
	if !m.useOAuth() {
		t.Fatal("the form did not default to OAuth for Outlook")
	}
	if m.authChoice != authOAuth {
		t.Fatalf("auth choice = %d, want OAuth", m.authChoice)
	}

	vis := m.visible()
	for _, idx := range vis {
		if idx == fPassword {
			t.Fatal("the app password field is on screen for Outlook")
		}
	}
	if !containsField(vis, fClientID) {
		t.Fatal("the client ID field is not on screen for an OAuth-only provider")
	}
	if containsField(vis, fClientSecret) {
		t.Fatal("a Microsoft public client was offered a client secret")
	}

	// The picker will not move off OAuth.
	moved, _ := m.cycleAuth(1)
	if !moved.useOAuth() {
		t.Fatal("the auth picker moved off OAuth for Outlook")
	}
	if !strings.Contains(moved.note, "no longer accepts app passwords") {
		t.Fatalf("note = %q, want the reason", moved.note)
	}

	joined := strings.Join(m.view(100, 24), "\n")
	if !strings.Contains(joined, "app passwords not accepted by Microsoft") {
		t.Fatalf("the form does not say why the app password is not offered:\n%s", joined)
	}
	if !strings.Contains(m.note, "no longer accepts app passwords") {
		t.Fatalf("note = %q, want the full reason", m.note)
	}

	// Gmail still offers both.
	gmail := setupAt("me@gmail.com", fEmail)
	if gmail.oauthOnly() {
		t.Fatal("Gmail was treated as OAuth-only")
	}
	if gmail.useOAuth() {
		t.Fatal("Gmail defaulted to OAuth rather than the app password")
	}
	if !containsField(gmail.visible(), fPassword) {
		t.Fatal("the app password field is missing for Gmail")
	}
}

func containsField(vis []int, want int) bool {
	for _, idx := range vis {
		if idx == want {
			return true
		}
	}
	return false
}

// The account name reaches the credential store, the export file names and
// the database, so the form refuses one that would mean something different
// in any of them.
func TestSetupFormRejectsABadAccountName(t *testing.T) {
	m := setupAt("me@gmail.com", fEmail)
	m.focus = 0
	m.fields[fName].clear()
	for _, r := range "../escape" {
		m, _ = m.update(keyMsg(string(r)))
	}
	m.fields[fPassword].set("hunter2")

	if _, _, err := m.account(); err == nil {
		t.Fatal("the form accepted an account name with a path separator in it")
	} else if !strings.Contains(err.Error(), "may only use letters") {
		t.Fatalf("error = %v, want the naming rule", err)
	}

	m.fields[fName].clear()
	m.fields[fName].set("personal")
	if _, _, err := m.account(); err != nil {
		t.Fatalf("the form rejected a valid name: %v", err)
	}
}

// Display names, subjects and links come from mail, so a sender can start a
// cell with a character a spreadsheet reads as a formula and have it
// evaluated when the export is opened.
func TestExportedCSVNeutralisesFormulas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	groups := []store.SenderGroup{
		{SenderKey: "addr:a@x.example", Display: `=HYPERLINK("http://evil.example","click")`,
			Address: "a@x.example", Method: "oneclick", LatestURIs: []string{"+https://evil.example/u"}},
		{SenderKey: "addr:b@x.example", Display: "-2+3", Address: "@handle", Method: "http"},
		{SenderKey: "addr:c@x.example", Display: "Acme", Address: "c@x.example", Method: "none"},
	}
	if err := writeGroupsCSV(f, groups); err != nil {
		t.Fatalf("writeGroupsCSV: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("the export is not valid CSV: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want a header and three senders", len(rows))
	}
	for i, row := range rows[1:] {
		for j, cell := range row {
			if cell == "" {
				continue
			}
			if strings.ContainsAny(cell[:1], "=+-@") {
				t.Errorf("row %d column %d starts a formula: %q", i+1, j, cell)
			}
		}
	}
	if got := rows[1][11]; got != `'=HYPERLINK("http://evil.example","click")` {
		t.Errorf("display cell = %q, want it quoted as text", got)
	}
	if got := rows[1][17]; got != "'+https://evil.example/u" {
		t.Errorf("uri cell = %q, want it quoted as text", got)
	}
	if got := rows[2][11]; got != "'-2+3" {
		t.Errorf("display cell = %q, want it quoted as text", got)
	}
	if got := rows[2][12]; got != "'@handle" {
		t.Errorf("address cell = %q, want it quoted as text", got)
	}
	// An ordinary cell is untouched.
	if got := rows[3][11]; got != "Acme" {
		t.Errorf("plain cell = %q, want it unchanged", got)
	}
}
