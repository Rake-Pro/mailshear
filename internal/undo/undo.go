// Package undo reverses the deletions of a previous apply run by moving the
// messages recorded in its audit log back out of Trash.
package undo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/apply"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/store"
)

// ErrAborted is returned when the confirmation prompt was declined.
var ErrAborted = errors.New("aborted at the confirmation prompt")

// gmailLabelNotice explains the one thing undo cannot restore on Gmail.
const gmailLabelNotice = "note: Gmail labels are not restored; the IMAP library has no X-GM-LABELS support yet, so messages come back to All Mail without their old labels."

// Run moves the messages the run moved to Trash back to the folders they came
// from. UIDs change on a move, so each message is located in Trash by its
// Message-ID rather than by the UID the audit log recorded.
func Run(ctx context.Context, st *store.Store, cl *imapx.Client, acct store.Account, dataDir, runID string, yes bool, confirm func(string) (bool, error), out io.Writer) (moved, missing int, err error) {
	if out == nil {
		out = io.Discard
	}
	if !apply.ValidRunID(runID) {
		return 0, 0, fmt.Errorf("undo: %q is not a run id (expected YYYYMMDD-HHMMSS-xxxx)", runID)
	}
	path := apply.AuditPath(dataDir, runID)
	lines, err := apply.ReadLines(path)
	if err != nil {
		return 0, 0, err
	}

	// A move line is written per UID before the move and confirmed per batch
	// after it, so one line here means one message this run may have moved.
	// Counting them per Message-ID is what bounds how many copies undo is
	// allowed to pull back out of Trash.
	//
	// The confirmation is one "done" line per MOVE, listing the UIDs the
	// server accepted. A pending line whose UID is in none of them may never
	// have moved at all, so it is checked against the source folder before
	// anything is pulled back out of Trash.
	confirmed := map[string]map[uint32]bool{} // source folder -> uid
	for _, l := range lines {
		if l.Action != "move" || l.Status != "done" {
			continue
		}
		if l.Account != "" && l.Account != acct.Name {
			continue
		}
		src := l.From
		if src == "" {
			src = l.Folder
		}
		if src == "" {
			continue
		}
		if confirmed[src] == nil {
			confirmed[src] = map[uint32]bool{}
		}
		for _, u := range l.UIDs {
			confirmed[src][u] = true
		}
	}

	type want struct {
		source string
		count  int
		// unconfirmed are the UIDs this run recorded an intent to move and
		// never confirmed. never is how many of them turned out to still be
		// in the source folder, and so were never moved.
		unconfirmed []uint32
		never       int
	}
	order := map[string][]string{}          // dest -> message ids, audit order
	wants := map[string]map[string]*want{}  // dest -> message id -> want
	sources := map[string]map[string]bool{} // dest -> source folders
	for _, l := range lines {
		if l.Action != "move" || l.MessageID == "" {
			continue
		}
		if l.Account != "" && l.Account != acct.Name {
			continue
		}
		src, dst := l.From, l.To
		if src == "" {
			src = l.Folder
		}
		if src == "" || dst == "" {
			continue
		}
		if wants[dst] == nil {
			wants[dst] = map[string]*want{}
			sources[dst] = map[string]bool{}
		}
		w := wants[dst][l.MessageID]
		if w == nil {
			w = &want{source: src}
			wants[dst][l.MessageID] = w
			order[dst] = append(order[dst], l.MessageID)
		}
		w.count++
		if l.UID != 0 && !confirmed[src][l.UID] {
			w.unconfirmed = append(w.unconfirmed, l.UID)
		}
		sources[dst][src] = true
	}
	if len(order) == 0 {
		return 0, 0, fmt.Errorf("run %s moved nothing; there is nothing to undo (audit log %s)", runID, path)
	}

	total := 0
	var summary strings.Builder
	fmt.Fprintf(&summary, "Undo run %s (account %s)\n", runID, acct.Name)
	for _, dst := range sortedKeys(order) {
		n := 0
		for _, id := range order[dst] {
			n += wants[dst][id].count
		}
		total += n
		fmt.Fprintf(&summary, "  %d message(s) from %s back to %s\n",
			n, dst, strings.Join(sortedKeys(sources[dst]), ", "))
	}
	fmt.Fprintf(&summary, "Audit log: %s", path)

	if !yes {
		if confirm == nil {
			return 0, 0, errors.New("undo: confirmation required but no prompt available; pass --yes")
		}
		ok, cerr := confirm(summary.String())
		if cerr != nil {
			return 0, 0, cerr
		}
		if !ok {
			return 0, 0, ErrAborted
		}
	} else {
		fmt.Fprintln(out, summary.String())
	}

	if cl.Gmail {
		fmt.Fprintln(out, gmailLabelNotice)
	}

	au, err := apply.OpenAudit(dataDir, runID, acct.Name)
	if err != nil {
		return 0, 0, err
	}
	defer au.Close()

	blocked := blockedDestinations(ctx, cl, out)

	// Check the unconfirmed intents against the folders they were written
	// for, before the Trash side is touched: the run may have stopped between
	// the write-ahead record and the MOVE, and a message still sitting where
	// it started must not be "restored" from a copy that is not there.
	type pending struct {
		id string
		w  *want
	}
	bySource := map[string][]pending{}
	for _, dst := range sortedKeys(order) {
		for _, id := range order[dst] {
			w := wants[dst][id]
			if len(w.unconfirmed) == 0 || apply.NeverTouch(w.source) || blocked[strings.ToLower(w.source)] {
				continue
			}
			bySource[w.source] = append(bySource[w.source], pending{id: id, w: w})
		}
	}
	for _, src := range sortedKeys(bySource) {
		if err := ctx.Err(); err != nil {
			return moved, missing, err
		}
		if _, err := cl.Select(ctx, src); err != nil {
			// The source folder is gone or unreadable: nothing can be
			// confirmed either way, so the run is treated as having moved
			// what it said it would, which is what undo did before.
			fmt.Fprintf(out, "warning: cannot check %s for messages that never moved: %v\n", src, err)
			continue
		}
		for _, p := range bySource[src] {
			uids, err := cl.SearchMessageID(ctx, p.id)
			if err != nil {
				return moved, missing, err
			}
			present := map[uint32]bool{}
			for _, u := range uids {
				present[u] = true
			}
			for _, u := range p.w.unconfirmed {
				if present[u] {
					p.w.never++
				}
			}
			if p.w.never == 0 {
				continue
			}
			p.w.count -= p.w.never
			if p.w.count < 0 {
				p.w.count = 0
			}
			au.Write(apply.Line{
				Action: "undo", MessageID: p.id, From: src, To: src,
				Error: "never moved, left in place",
			})
			fmt.Fprintf(out, "%s: %d copy/copies were never moved, left in place in %s\n", p.id, p.w.never, src)
		}
	}

	for _, dst := range sortedKeys(order) {
		if err := ctx.Err(); err != nil {
			return moved, missing, err
		}
		if _, err := cl.Select(ctx, dst); err != nil {
			return moved, missing, err
		}

		// Locate everything first: searching after a move would race the
		// UIDs that the move itself changes.
		found := map[string][]uint32{}
		for _, id := range order[dst] {
			w := wants[dst][id]
			if apply.NeverTouch(w.source) || blocked[strings.ToLower(w.source)] {
				missing++
				au.Write(apply.Line{
					Action: "undo", Folder: dst, MessageID: id, From: dst, To: w.source,
					Error: "refusing to restore into " + w.source,
				})
				fmt.Fprintf(out, "not restored: %s came from %s, which undo never moves mail into\n", id, w.source)
				continue
			}
			if w.count == 0 {
				// Every copy this run recorded turned out to be still in the
				// source folder; there is nothing in Trash to bring back.
				continue
			}
			uids, err := cl.SearchMessageID(ctx, id)
			if err != nil {
				return moved, missing, err
			}
			if len(uids) == 0 {
				missing++
				au.Write(apply.Line{
					Action: "undo", Folder: dst, MessageID: id,
					From: dst, To: w.source, Error: "not found in " + dst,
				})
				continue
			}
			uids = dedupe(uids)
			// More copies in Trash than this run put there means the user
			// already had one. Restore only as many as the run moved, newest
			// UIDs first (the run's copies arrived last), and leave the rest.
			if len(uids) > w.count {
				surplus := len(uids) - w.count
				uids = uids[len(uids)-w.count:]
				au.Write(apply.Line{
					Action: "undo", Folder: dst, MessageID: id, From: dst, To: w.source,
					Error: fmt.Sprintf("ambiguous, left in Trash (%d extra copy/copies)", surplus),
				})
				fmt.Fprintf(out, "%s: %d extra copy/copies in %s are ambiguous, left in Trash\n", id, surplus, dst)
			}
			found[w.source] = append(found[w.source], uids...)
		}

		for _, src := range sortedKeys(found) {
			uids := dedupe(found[src])
			if err := cl.MoveUIDs(ctx, uids, src); err != nil {
				au.Write(apply.Line{Action: "error", Folder: dst, From: dst, To: src, Error: err.Error()})
				return moved, missing, err
			}
			for _, uid := range uids {
				moved++
				au.Write(apply.Line{
					Action: "undo", Folder: dst, UID: uid, From: dst, To: src,
				})
			}
		}
	}

	if err := st.FinishRun(ctx, runID, fmt.Sprintf("undone: restored %d, missing %d", moved, missing)); err != nil {
		fmt.Fprintf(out, "warning: recording the undo summary failed: %v\n", err)
	}
	return moved, missing, au.Err()
}

// blockedDestinations is the set of mailbox names undo refuses to restore
// into, resolved against the server: whatever it marks \Trash, \Junk,
// \Drafts or \Sent. apply.NeverTouch covers the well-known names.
func blockedDestinations(ctx context.Context, cl *imapx.Client, out io.Writer) map[string]bool {
	blocked := map[string]bool{}
	for _, attr := range apply.ProtectedSpecialUse() {
		name, err := cl.FindSpecialUse(ctx, attr)
		if err != nil {
			fmt.Fprintf(out, "warning: looking up the %s mailbox failed: %v\n", attr, err)
			continue
		}
		if name != "" {
			blocked[strings.ToLower(name)] = true
		}
	}
	return blocked
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(uids []uint32) []uint32 {
	seen := map[uint32]bool{}
	out := make([]uint32, 0, len(uids))
	for _, u := range uids {
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
