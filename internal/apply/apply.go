// Package apply executes a reviewed plan: unsubscribes, then moves the
// selected messages to Trash, recording every action in a JSONL audit log.
//
// The safety invariants from docs/design.md section 5 live here. Deletion is
// always a MOVE to Trash: nothing in this package expunges, empties Trash, or
// selects Spam, Drafts or Sent as a source.
package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/keep"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// ErrAborted is returned when the confirmation prompt was declined. Nothing
// was changed on the server, in the database, or on disk.
var ErrAborted = errors.New("aborted at the confirmation prompt")

type Options struct {
	NoDelete      bool
	NoUnsubscribe bool
	Yes           bool
	Confirm       func(summary string) (bool, error)
	Out           io.Writer
}

type Deps struct {
	Store   *store.Store
	Client  *imapx.Client
	Account store.Account
	Cfg     config.Account
	Protect config.Protect
	// Keep is the transactional-mail rule. The zero value is on with the
	// built-in phrase list, so a caller that forgets to wire the config
	// still refuses to delete receipts.
	Keep    keep.Matcher
	Unsub   *unsub.Executor
	DataDir string
}

// ExecOptions configures one Execute call. OnEvent, when set, is called as
// the run progresses; it may be called from several goroutines during the
// unsubscribe phase, so it must be safe for concurrent use and must not
// block for long.
type ExecOptions struct {
	NoDelete      bool
	NoUnsubscribe bool
	OnEvent       func(Event)
}

// Phase names the stage of the run an Event came from.
type Phase string

const (
	PhaseUnsubscribe Phase = "unsubscribe"
	PhaseDelete      Phase = "delete"
	PhaseDone        Phase = "done"
)

// Event is one progress record from Execute.
type Event struct {
	Phase Phase

	// Unsubscribe phase.
	SenderKey  string
	Display    string
	Method     string
	Status     string
	HTTPStatus int
	URI        string
	FinalURL   string
	Err        string

	// Delete phase.
	Folder     string
	Moved      int // messages moved by this batch
	TotalMoved int // messages moved by the run so far
	SkipReason string
}

// PreviewRow is one sender the run would act on.
type PreviewRow struct {
	SenderKey string
	Display   string
	Address   string
	Method    string
	// Unsubscribe reports that an unsubscribe would be attempted.
	Unsubscribe bool
	// DeleteScope is "", "matched" or "all".
	DeleteScope string
	// IncludeKept reports that this sender waived the transactional rule.
	IncludeKept bool
	// Messages is the upper bound on messages moved, with every pre-filter
	// that needs no server round trip already applied.
	Messages int
	Bytes    int64
	// Kept is how many of the sender's messages the transactional rule
	// excluded from Messages. It is a lower bound: the live subject is
	// checked again at move time and can only exclude more.
	Kept int
}

// RefusedRow is a plan entry dropped before the run, with the reason.
type RefusedRow struct {
	SenderKey string
	Display   string
	Reason    string
}

// Preview is everything Prepare learned without changing anything: what the
// run would do, where it would move mail, and what it refused.
type Preview struct {
	RunID   string
	Account string
	Rows    []PreviewRow
	Refused []RefusedRow

	// Folders the delete phase would read from.
	Folders []string
	// Trash is the resolved destination; empty when it could not be resolved.
	Trash string
	// TrashNote explains why Trash is unusable or risky. When it is set the
	// delete phase will skip the messages it describes.
	TrashNote string
	AuditPath string
	// Warnings are non-fatal problems worth showing before applying.
	Warnings []string

	UnsubCount       int
	UnsubByMethod    map[string]int
	DeleteSenders    int
	DeleteAllSenders int
	Messages         int
	Bytes            int64
	// Kept is the total the transactional rule excluded from Messages.
	Kept int

	DroppedSenders int
	DroppedReasons map[string]int
}

// Prepared is a validated plan that has touched nothing yet. Execute is the
// only thing that acts on the server, the database or the audit log.
type Prepared struct {
	Preview Preview

	r       *runner
	entries []plan.Entry
}

type Summary struct {
	RunID string
	Unsub map[unsub.Status]int
	Moved int
	// SkippedSenders counts plan entries dropped before the run (protected,
	// own address); SkippedMessages counts individual messages not moved.
	SkippedSenders  int
	SkippedMessages int
	SkippedReasons  map[string]int
	Manual          []unsub.Result
	FoldersSkipped  []string
	AuditPath       string
}

// moveBatch is the number of UIDs moved per audit checkpoint. imapx batches
// the IMAP commands itself; batching here bounds how much progress a mid-run
// failure can leave unrecorded.
const moveBatch = 200

// Skip reasons, also written to the audit log's error field.
const (
	reasonProtected        = "protected"
	reasonProtectedAll     = "protected (delete_all refused)"
	reasonFlagged          = "flagged"
	reasonAnswered         = "answered"
	reasonIdentityMismatch = "identity mismatch"
	reasonGone             = "gone"
	reasonNoMessageID      = "no stored message-id"
	reasonUIDValidity      = "uidvalidity changed"
	reasonNotScanned       = "folder not in the local scan state"
	reasonProtectedFolder  = "protected folder (trash/spam/drafts/sent)"
	reasonNoTrash          = "no trash folder"
	reasonFlagsUnavailable = "flags unavailable"
	reasonOwnAddress       = "own address (delete_all refused)"
	// reasonKept is prefixed onto the matched category, so the audit log
	// says which rule saved the message: "kept: receipt".
	reasonKept = "kept"
)

// keptReason names the skip for a message the transactional rule protects.
func keptReason(category string) string {
	if category == "" {
		return reasonKept
	}
	return reasonKept + ": " + category
}

// neverSource are folders a delete never reads from, whatever the local state
// claims. Names are compared lowercased.
var neverSource = map[string]bool{
	"trash":             true,
	"spam":              true,
	"junk":              true,
	"drafts":            true,
	"sent":              true,
	"[gmail]/trash":     true,
	"[gmail]/spam":      true,
	"[gmail]/drafts":    true,
	"[gmail]/sent mail": true,
}

// neverDest are folders a delete never moves messages INTO, whatever the
// config or the server says the trash mailbox is. Trash itself is absent: it
// is the one legitimate destination.
var neverDest = map[string]bool{
	"spam":              true,
	"junk":              true,
	"drafts":            true,
	"sent":              true,
	"[gmail]/spam":      true,
	"[gmail]/drafts":    true,
	"[gmail]/sent mail": true,
}

// NeverTouch reports whether name is a well-known mailbox that mailshear never
// reads from and never restores into: Trash, Spam/Junk, Drafts or Sent.
// internal/undo shares this set so an undo cannot push mail into them.
func NeverTouch(name string) bool { return neverSource[strings.ToLower(name)] }

// ProtectedSpecialUse returns the RFC 6154 attributes marking those same
// mailboxes, for callers that want to resolve them against a live server.
func ProtectedSpecialUse() []imap.MailboxAttr {
	return append([]imap.MailboxAttr(nil), protectedSpecialUse...)
}

var protectedSpecialUse = []imap.MailboxAttr{
	imap.MailboxAttrTrash,
	imap.MailboxAttrJunk,
	imap.MailboxAttrDrafts,
	imap.MailboxAttrSent,
}

type runner struct {
	d     Deps
	exec  ExecOptions
	p     *plan.Plan
	out   io.Writer
	runID string
	trash string
	// trashErr is why deletion cannot run at all, if it cannot.
	trashErr error
	au       *Auditor
	sum      Summary
	// pending holds skips decided before the audit log exists.
	pending []Line
	// refused lists the plan entries dropped before the run, for the preview.
	refused []RefusedRow
	// noSource is neverSource plus whatever the server marks special-use.
	noSource map[string]bool
}

// Prepare validates p and works out what applying it would do. It reads the
// database and the server (mailbox lookups only) and changes nothing: no run
// row, no audit log, no message moved. Execute is the only side effect.
func Prepare(ctx context.Context, d Deps, p *plan.Plan) (*Prepared, error) {
	r := &runner{
		d: d, p: p, out: io.Discard,
		runID: p.RunID,
		sum: Summary{
			Unsub:          map[unsub.Status]int{},
			SkippedReasons: map[string]int{},
		},
	}
	if r.runID == "" {
		r.runID = plan.NewRunID()
	}
	r.sum.RunID = r.runID

	warnings, err := r.validate(ctx)
	if err != nil {
		return nil, err
	}

	entries, err := r.filterProtected(ctx)
	if err != nil {
		return nil, err
	}

	if anyDelete(entries) {
		r.trash, r.trashErr = r.resolveTrash(ctx)
	}

	pv, err := r.preview(ctx, entries)
	if err != nil {
		return nil, err
	}
	pv.Warnings = warnings
	return &Prepared{Preview: pv, r: r, entries: entries}, nil
}

// Execute performs the prepared run: unsubscribes, then moves the selected
// messages to Trash, recording everything in the audit log.
func (pd *Prepared) Execute(ctx context.Context, opts ExecOptions) (Summary, error) {
	r := pd.r
	r.exec = opts

	if err := r.openRun(ctx); err != nil {
		return r.sum, err
	}
	defer r.au.Close()
	r.sum.AuditPath = r.au.Path()
	for _, l := range r.pending {
		r.au.Write(l)
	}
	if err := r.au.Err(); err != nil {
		return r.sum, err
	}

	if !opts.NoUnsubscribe {
		r.unsubscribe(ctx, pd.entries)
	}
	if !opts.NoDelete {
		if err := r.deleteAll(ctx, pd.entries); err != nil {
			r.finish(ctx)
			return r.sum, err
		}
	}
	r.finish(ctx)
	r.emit(Event{Phase: PhaseDone, TotalMoved: r.sum.Moved})
	return r.sum, r.au.Err()
}

// emit hands one progress record to the caller's callback, if any.
func (r *runner) emit(e Event) {
	if r.exec.OnEvent != nil {
		r.exec.OnEvent(e)
	}
}

// Run is the command-line path: prepare, show the summary, ask, execute. It
// returns ErrAborted, having changed nothing, when the prompt is declined.
func Run(ctx context.Context, d Deps, p *plan.Plan, o Options) (Summary, error) {
	out := o.Out
	if out == nil {
		out = io.Discard
	}

	pd, err := Prepare(ctx, d, p)
	if err != nil {
		return Summary{}, err
	}
	pd.r.out = out
	for _, w := range pd.Preview.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}

	summaryText := PreflightText(pd.Preview, o.NoDelete, o.NoUnsubscribe)
	if !o.Yes {
		if o.Confirm == nil {
			return pd.r.sum, errors.New("apply: confirmation required but no prompt available; pass --yes")
		}
		ok, err := o.Confirm(summaryText)
		if err != nil {
			return pd.r.sum, err
		}
		if !ok {
			return Summary{RunID: pd.Preview.RunID}, ErrAborted
		}
	} else {
		fmt.Fprintln(out, summaryText)
	}

	return pd.Execute(ctx, ExecOptions{
		NoDelete:      o.NoDelete,
		NoUnsubscribe: o.NoUnsubscribe,
	})
}

// validate checks the plan against the account and the local database. It
// returns warnings rather than printing them, so both the CLI and the TUI can
// render them where they belong.
func (r *runner) validate(ctx context.Context) ([]string, error) {
	if r.p.Account != r.d.Cfg.Name {
		return nil, fmt.Errorf("apply: plan is for account %q but %q is selected", r.p.Account, r.d.Cfg.Name)
	}
	count, err := r.d.Store.MessageCount(ctx, r.d.Account.ID)
	if err != nil {
		return nil, err
	}
	if r.p.Snapshot.MessageCount != 0 && count != r.p.Snapshot.MessageCount {
		return []string{fmt.Sprintf(
			"database changed since the plan was written; re-run review if unsure (plan %d messages, now %d)",
			r.p.Snapshot.MessageCount, count)}, nil
	}
	return nil, nil
}

// filterProtected drops entries for protected senders. Protection comes from
// the database flag and from the config protect list; delete_all on a
// protected sender is called out separately because it is the escalation the
// protected list exists to block.
func (r *runner) filterProtected(ctx context.Context) ([]plan.Entry, error) {
	// Both reads decide what is protected. A failure here would silently
	// unprotect senders, so it aborts the run instead: nothing has happened
	// yet, and refusing to apply is always recoverable.
	keys, err := r.d.Store.ListProtected(ctx, r.d.Account.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot load protected senders: %w; refusing to apply", err)
	}
	protectedKeys := make(map[string]bool, len(keys))
	for _, k := range keys {
		protectedKeys[k] = true
	}

	gs, err := r.d.Store.SenderGroups(ctx, r.d.Account.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot load protected senders: %w; refusing to apply", err)
	}
	groups := make(map[string]store.SenderGroup, len(gs))
	for _, g := range gs {
		groups[g.SenderKey] = g
	}

	out := make([]plan.Entry, 0, len(r.p.Decisions))
	for _, e := range r.p.Decisions {
		if !e.Unsubscribe && !e.DeleteMatched && !e.DeleteAll {
			continue
		}
		g := groups[e.SenderKey]
		address := g.Address
		if address == "" {
			address = e.Address
		}
		protected := protectedKeys[e.SenderKey] ||
			r.d.Protect.Matches(g.DomainKey, address, g.ListID)
		if protected {
			reason := reasonProtected
			if e.DeleteAll {
				reason = reasonProtectedAll
			}
			r.noteSender(reason)
			r.refused = append(r.refused, RefusedRow{
				SenderKey: e.SenderKey, Display: displayOf(e), Reason: reason,
			})
			r.pending = append(r.pending, Line{
				Action: actionSkip, SenderKey: e.SenderKey, Error: reason,
			})
			continue
		}
		// On Gmail, All Mail includes sent messages, so "everything from
		// this sender" on your own address would sweep up your own mail.
		if e.DeleteAll && r.d.Cfg.Username != "" && strings.EqualFold(address, r.d.Cfg.Username) {
			r.noteSender(reasonOwnAddress)
			r.refused = append(r.refused, RefusedRow{
				SenderKey: e.SenderKey, Display: displayOf(e), Reason: reasonOwnAddress,
			})
			r.pending = append(r.pending, Line{
				Action: actionSkip, SenderKey: e.SenderKey, Error: reasonOwnAddress,
			})
			e.DeleteAll = false
			if !e.Unsubscribe && !e.DeleteMatched {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// displayOf is the human label for a plan entry, falling back through the
// address to the sender key.
func displayOf(e plan.Entry) string {
	if e.Display != "" {
		return e.Display
	}
	if e.Address != "" {
		return e.Address
	}
	return e.SenderKey
}

func anyDelete(entries []plan.Entry) bool {
	for _, e := range entries {
		if e.DeleteMatched || e.DeleteAll {
			return true
		}
	}
	return false
}

// resolveTrash finds the destination for deletions: the config override, the
// RFC 6154 \Trash special-use mailbox, the Gmail name, or a mailbox literally
// called Trash. Anything else is an error and deletion does not run.
func (r *runner) resolveTrash(ctx context.Context) (string, error) {
	if r.d.Cfg.Trash != "" {
		return r.d.Cfg.Trash, nil
	}
	if name, err := r.d.Client.FindSpecialUse(ctx, imap.MailboxAttrTrash); err == nil && name != "" {
		return name, nil
	}
	if r.d.Client.Gmail {
		return "[Gmail]/Trash", nil
	}
	names, err := r.d.Client.ListMailboxes(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if n == "Trash" {
			return "Trash", nil
		}
	}
	return "", errors.New("no Trash folder found; set trash: in the account config")
}

// note records one skipped message; noteSender records one dropped plan entry.
func (r *runner) note(reason string) {
	r.sum.SkippedMessages++
	r.sum.SkippedReasons[reason]++
}

func (r *runner) noteSender(reason string) {
	r.sum.SkippedSenders++
	r.sum.SkippedReasons[reason]++
}

func (r *runner) openRun(ctx context.Context) error {
	run := store.Run{
		ID:        r.runID,
		AccountID: r.d.Account.ID,
		StartedAt: time.Now(),
		PlanPath:  plan.Path(r.d.DataDir, r.runID),
	}
	if err := r.d.Store.InsertRun(ctx, run); err != nil {
		// A re-apply of the same plan reuses the run row rather than failing.
		if _, getErr := r.d.Store.GetRun(ctx, r.runID); getErr != nil {
			return err
		}
	}
	au, err := OpenAudit(r.d.DataDir, r.runID, r.d.Cfg.Name)
	if err != nil {
		return err
	}
	r.au = au
	return nil
}

func (r *runner) finish(ctx context.Context) {
	text := fmt.Sprintf("moved %d, skipped %d sender(s) and %d message(s), unsubscribed %d",
		r.sum.Moved, r.sum.SkippedSenders, r.sum.SkippedMessages,
		r.sum.Unsub[unsub.StatusOK]+r.sum.Unsub[unsub.StatusProbable])
	if err := r.d.Store.FinishRun(ctx, r.runID, text); err != nil {
		fmt.Fprintf(r.out, "warning: recording the run summary failed: %v\n", err)
	}
}
