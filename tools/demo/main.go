// Command demo drives the mailshear terminal UI against a scripted fake backend,
// so the screens can be recorded without a mailbox. It opens no network
// connection, reads no mail, and writes nothing: every path, run id and
// counter on screen is synthetic.
//
//	go run ./tools/demo -screen flow      # start at the scan, credentials present
//	go run ./tools/demo -screen setup     # start on the setup form
//	go run ./tools/demo -screen accounts  # start on the accounts switcher
//	go run ./tools/demo -screen flow -speed 2   # run the scripted delays twice as fast
//
// tools/demo/record.py drives this program inside a pty and turns the frames
// into the GIFs under docs/demo.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/scan"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/tui"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// Display-only paths. Nothing is written to them, or anywhere else.
const (
	demoConfigPath = "/home/casey/.config/mailshear/config.yaml"
	demoDataDir    = "/home/casey/.local/share/mailshear"
	demoAuditPath  = "/home/casey/.local/share/mailshear/audit.jsonl"
	demoPlanDir    = "/home/casey/.local/share/mailshear/plans"
	demoExportDir  = "/home/casey/.local/share/mailshear/exports"
	demoRunID      = "20260906-142211-9f3c"
	demoTrash      = "Trash"
	demoVersion    = "v1.0.0"
)

func main() {
	screen := flag.String("screen", "flow", "which screen to start on: setup, flow or accounts")
	speed := flag.Float64("speed", 1, "multiplier for the scripted delays; 2 runs twice as fast")
	flag.Parse()

	if *speed <= 0 {
		fmt.Fprintln(os.Stderr, "demo: -speed must be positive")
		os.Exit(2)
	}

	b := newDemoBackend(*speed)
	acct := config.Account{
		Name: "personal", Host: "imap.example.com", Port: 993,
		Username: "casey@example.com", Auth: "file",
		Folders: []string{"INBOX", "Archive"},
		SMTP:    &config.SMTP{Host: "smtp.example.com", Port: 587},
	}

	accounts := []config.Account{acct}
	name := "personal"
	switch *screen {
	case "flow":
	case "setup":
		// An account with no address has never been set up, so the flow
		// opens on the form with the fields empty.
		acct = config.Account{Name: "personal", Auth: "file"}
		accounts = []config.Account{acct}
	case "accounts":
		// Two accounts and nothing naming one: the flow opens on the
		// switcher, the way it does for a multi-mailbox install.
		accounts = demoAccounts()
		name = ""
	default:
		fmt.Fprintf(os.Stderr, "demo: unknown -screen %q, want setup, flow or accounts\n", *screen)
		os.Exit(2)
	}

	deps := tui.FlowDeps{
		Cfg: &config.Config{
			Accounts: accounts,
			Protect: config.Protect{
				Domains:           []string{"bank.example"},
				KeepTransactional: true,
			},
		},
		ConfigPath:  demoConfigPath,
		DataDir:     demoPlanDir,
		AccountName: name,
		Version:     demoVersion,
		Backend:     b,
		// The demo never reaches for a browser.
		OpenURL: func(string) error { return nil },
	}

	if _, err := tui.RunFlow(context.Background(), deps); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// demoBackend is a tui.Backend that answers from the synthetic mailbox in
// data.go, pausing where a real run would wait on a server so the progress
// bars and streaming panels animate.
type demoBackend struct {
	mu sync.Mutex

	speed  float64
	groups []store.SenderGroup
	byKey  map[string]store.SenderGroup

	hasCreds  bool
	decisions []store.Decision
	preview   apply.Preview
}

func newDemoBackend(speed float64) *demoBackend {
	groups := demoGroups(time.Now())
	byKey := make(map[string]store.SenderGroup, len(groups))
	for _, g := range groups {
		byKey[g.SenderKey] = g
	}
	return &demoBackend{speed: speed, groups: groups, byKey: byKey, hasCreds: true}
}

// pause sleeps a scripted delay, scaled by -speed, and reports whether the
// context is still live.
func (b *demoBackend) pause(ctx context.Context, ms int) bool {
	d := time.Duration(float64(ms)/b.speed) * time.Millisecond
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (b *demoBackend) HasCredentials(config.Account) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hasCreds
}

func (b *demoBackend) SaveSetup(a config.Account, _ string) (config.Account, error) {
	time.Sleep(time.Duration(400/b.speed) * time.Millisecond)
	b.mu.Lock()
	b.hasCreds = true
	b.mu.Unlock()
	return a, nil
}

// OAuthLogin fakes the browser round trip: the recording never reaches a real
// identity provider.
func (b *demoBackend) OAuthLogin(ctx context.Context, _ config.Account, onURL func(string)) error {
	if onURL != nil {
		onURL("https://accounts.example.com/o/oauth2/v2/auth?client_id=demo&response_type=code")
	}
	if !b.pause(ctx, 1200) {
		return ctx.Err()
	}
	return nil
}

func (b *demoBackend) AuthStatus(config.Account) tui.AuthStatus {
	return tui.AuthStatus{HasPassword: true}
}

func (b *demoBackend) SignOut(config.Account) error { return nil }

func (b *demoBackend) Connect(ctx context.Context, _ config.Account) error {
	if !b.pause(ctx, 900) {
		return ctx.Err()
	}
	return nil
}

// Scan walks two folders to 48,212 messages over about two seconds, which is
// long enough for the bar and the counters to read as a live scan.
func (b *demoBackend) Scan(ctx context.Context, onProgress func(scan.Progress)) (scan.Summary, error) {
	folders := []struct {
		name  string
		total int
	}{
		{"INBOX", 31500},
		{"Archive", 16712},
	}

	const stepsPerFolder = 16
	fetched := 0
	for _, f := range folders {
		for i := 1; i <= stepsPerFolder; i++ {
			done := f.total * i / stepsPerFolder
			p := scan.Progress{
				Folder:  f.name,
				Done:    done,
				Total:   f.total,
				Fetched: fetched + done,
				Bulk:    (fetched + done) * 23 / 100,
			}
			onProgress(p)
			if !b.pause(ctx, 62) {
				return scan.Summary{}, ctx.Err()
			}
		}
		fetched += f.total
	}

	return scan.Summary{Folders: len(folders), Fetched: fetched, Bulk: fetched * 23 / 100}, nil
}

func (b *demoBackend) Load(ctx context.Context) ([]store.SenderGroup, map[string]bool, error) {
	if !b.pause(ctx, 250) {
		return nil, nil, ctx.Err()
	}
	return b.groups, protectedKeys(), nil
}

func (b *demoBackend) Protect(context.Context, []string) error { return nil }

func (b *demoBackend) WritePlan(ctx context.Context, _ []store.SenderGroup, decisions []store.Decision) (*plan.Plan, string, error) {
	if !b.pause(ctx, 350) {
		return nil, "", ctx.Err()
	}
	b.mu.Lock()
	b.decisions = decisions
	b.mu.Unlock()
	return &plan.Plan{RunID: demoRunID, Account: "personal", CreatedAt: time.Now()},
		demoPlanDir + "/" + demoRunID + ".yaml", nil
}

// Prepare turns the review's decisions into the preview the confirm screen
// shows, the same join a real Prepare does against the database.
func (b *demoBackend) Prepare(ctx context.Context, _ *plan.Plan) (apply.Preview, error) {
	if !b.pause(ctx, 300) {
		return apply.Preview{}, ctx.Err()
	}

	b.mu.Lock()
	decisions := append([]store.Decision(nil), b.decisions...)
	b.mu.Unlock()

	pv := apply.Preview{
		RunID:         demoRunID,
		Account:       "personal",
		Folders:       []string{"INBOX", "Archive"},
		Trash:         demoTrash,
		AuditPath:     demoAuditPath,
		UnsubByMethod: map[string]int{},
		Warnings:      []string{"3 sender(s) last scanned 4 days ago; rescan for a fresher list"},
	}

	for _, d := range decisions {
		g, ok := b.byKey[d.SenderKey]
		if !ok {
			continue
		}
		row := apply.PreviewRow{
			SenderKey:   g.SenderKey,
			Display:     g.Display,
			Address:     g.Address,
			Method:      g.Method,
			Unsubscribe: d.Unsubscribe,
			IncludeKept: d.IncludeKept,
		}
		if d.Unsubscribe {
			pv.UnsubCount++
			pv.UnsubByMethod[g.Method]++
		}
		switch {
		case d.DeleteAll:
			row.DeleteScope = "all"
		case d.DeleteMatched:
			row.DeleteScope = "matched"
		}
		if row.DeleteScope != "" {
			msgs := g.Count
			if d.DeleteAll {
				msgs += g.MixedCount
				pv.DeleteAllSenders++
			}
			if !d.IncludeKept {
				row.Kept = g.KeepCount
				msgs -= g.KeepCount
			}
			if msgs < 0 {
				msgs = 0
			}
			row.Messages = msgs
			row.Bytes = g.TotalSize * int64(msgs) / int64(max(g.Count, 1))

			pv.DeleteSenders++
			pv.Messages += row.Messages
			pv.Bytes += row.Bytes
			pv.Kept += row.Kept
		}
		pv.Rows = append(pv.Rows, row)
	}
	sort.SliceStable(pv.Rows, func(i, j int) bool { return pv.Rows[i].Messages > pv.Rows[j].Messages })

	b.mu.Lock()
	b.preview = pv
	b.mu.Unlock()
	return pv, nil
}

// Execute streams the unsubscribe results over about three seconds and the
// delete progress over about two more, the pace a live run moves at.
func (b *demoBackend) Execute(ctx context.Context, opts apply.ExecOptions) (apply.Summary, error) {
	b.mu.Lock()
	pv := b.preview
	b.mu.Unlock()

	emit := func(e apply.Event) {
		if opts.OnEvent != nil {
			opts.OnEvent(e)
		}
	}

	counts := map[unsub.Status]int{}
	if !opts.NoUnsubscribe {
		for _, r := range pv.Rows {
			if !r.Unsubscribe {
				continue
			}
			if !b.pause(ctx, 480) {
				return apply.Summary{}, ctx.Err()
			}
			status, errText := demoStatus(r.SenderKey)
			e := apply.Event{
				Phase:     apply.PhaseUnsubscribe,
				SenderKey: r.SenderKey,
				Display:   r.Display,
				Method:    r.Method,
				Status:    status,
				Err:       errText,
			}
			if g, ok := b.byKey[r.SenderKey]; ok && len(g.LatestURIs) > 0 {
				e.URI = g.LatestURIs[0]
			}
			switch status {
			case "ok":
				e.HTTPStatus = 200
			case "probable":
				e.HTTPStatus = 200
				sep := "?"
				if strings.Contains(e.URI, "?") {
					sep = "&"
				}
				e.FinalURL = e.URI + sep + "confirm=1"
			}
			counts[unsub.Status(status)]++
			emit(e)
		}
	}

	moved := 0
	if !opts.NoDelete && pv.Messages > 0 {
		const steps = 20
		for i := 1; i <= steps; i++ {
			if !b.pause(ctx, 100) {
				return apply.Summary{}, ctx.Err()
			}
			folder := "INBOX"
			if i > steps*2/3 {
				folder = "Archive"
			}
			next := pv.Messages * i / steps
			emit(apply.Event{
				Phase: apply.PhaseDelete, Folder: folder,
				Moved: next - moved, TotalMoved: next,
			})
			moved = next
		}
		emit(apply.Event{Phase: apply.PhaseDone, TotalMoved: moved})
	}

	sum := apply.Summary{
		RunID:     demoRunID,
		Unsub:     counts,
		Moved:     moved,
		AuditPath: demoAuditPath,
	}
	if pv.Kept > 0 {
		sum.SkippedMessages = pv.Kept + 2
		sum.SkippedReasons = map[string]int{"kept: receipt": pv.Kept, "flagged": 2}
	}
	return sum, nil
}

func (b *demoBackend) Undo(ctx context.Context, _ string) (int, int, error) {
	if !b.pause(ctx, 900) {
		return 0, 0, ctx.Err()
	}
	b.mu.Lock()
	moved := b.preview.Messages
	b.mu.Unlock()
	return moved, 0, nil
}
