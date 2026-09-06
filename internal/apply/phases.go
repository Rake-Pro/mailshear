package apply

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Rake-Pro/mailshear/internal/headers"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/plan"
	"github.com/Rake-Pro/mailshear/internal/store"
	"github.com/Rake-Pro/mailshear/internal/text"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

// deleteTarget is one sender's delete scope, resolved against the local store.
type deleteTarget struct {
	entry plan.Entry
	msgs  []store.Message
}

func (r *runner) deleteTargets(ctx context.Context, entries []plan.Entry) ([]deleteTarget, error) {
	var out []deleteTarget
	for _, e := range entries {
		if !e.DeleteMatched && !e.DeleteAll {
			continue
		}
		msgs, err := r.d.Store.MessagesForSender(ctx, r.d.Account.ID, e.SenderKey, !e.DeleteAll)
		if err != nil {
			return nil, err
		}
		out = append(out, deleteTarget{entry: e, msgs: msgs})
	}
	return out, nil
}

// preview works out what the run would do, without changing anything.
func (r *runner) preview(ctx context.Context, entries []plan.Entry) (Preview, error) {
	pv := Preview{
		RunID:          r.runID,
		Account:        r.d.Cfg.Name,
		Trash:          r.trash,
		AuditPath:      AuditPath(r.d.DataDir, r.runID),
		UnsubByMethod:  map[string]int{},
		Refused:        r.refused,
		DroppedSenders: r.sum.SkippedSenders,
		DroppedReasons: r.sum.SkippedReasons,
	}

	targets, err := r.deleteTargets(ctx, entries)
	if err != nil {
		return Preview{}, err
	}
	scope := make(map[string]deleteTarget, len(targets))
	for _, t := range targets {
		scope[t.entry.SenderKey] = t
	}

	folders := map[string]bool{}    // folders the run would actually read
	allFolders := map[string]bool{} // every folder the plan names, filtered or not

	for _, e := range entries {
		row := PreviewRow{
			SenderKey:   e.SenderKey,
			Display:     displayOf(e),
			Address:     e.Address,
			Method:      e.Method,
			Unsubscribe: e.Unsubscribe,
		}
		if row.Method == "" {
			row.Method = string(headers.MethodNone)
		}
		if e.Unsubscribe {
			pv.UnsubCount++
			pv.UnsubByMethod[row.Method]++
		}
		switch {
		case e.DeleteAll:
			row.DeleteScope = "all"
			pv.DeleteAllSenders++
		case e.DeleteMatched:
			row.DeleteScope = "matched"
		}
		row.IncludeKept = e.IncludeKept
		if row.DeleteScope != "" {
			pv.DeleteSenders++
		}
		// The count is an upper bound: it applies every pre-filter that does
		// not need the server (protected senders, never-touch folders, the
		// has_unsub scope MessagesForSender already honored, and the flags
		// recorded at scan time). Flags fetched at move time can only shrink
		// it further.
		for _, m := range scope[e.SenderKey].msgs {
			allFolders[m.Folder] = true
			if r.preFiltered(m) {
				continue
			}
			if r.keptAtScan(m, e.IncludeKept) {
				row.Kept++
				continue
			}
			row.Messages++
			row.Bytes += m.Size
			folders[m.Folder] = true
		}
		pv.Messages += row.Messages
		pv.Bytes += row.Bytes
		pv.Kept += row.Kept
		pv.Rows = append(pv.Rows, row)
	}

	pv.Folders = sortedKeys(folders)
	pv.TrashNote = r.destinationNote(allFolders)
	return pv, nil
}

// PreflightText renders the confirmation summary the command line shows
// before anything happens.
func PreflightText(pv Preview, noDelete, noUnsubscribe bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Account %s, run %s\n", pv.Account, pv.RunID)

	switch {
	case noUnsubscribe:
		fmt.Fprintln(&b, "Unsubscribe: skipped (--no-unsubscribe)")
	case pv.UnsubCount == 0:
		fmt.Fprintln(&b, "Unsubscribe: nothing selected")
	default:
		fmt.Fprintf(&b, "Unsubscribe: %d sender(s) [%s]\n", pv.UnsubCount, text.JoinCounts(pv.UnsubByMethod))
	}

	switch {
	case noDelete:
		fmt.Fprintln(&b, "Delete: skipped (--no-delete)")
	case pv.DeleteSenders == 0:
		fmt.Fprintln(&b, "Delete: nothing selected")
	default:
		fmt.Fprintf(&b, "Delete: %d sender(s), up to %d message(s), %s\n", pv.DeleteSenders, pv.Messages, text.HumanBytes(pv.Bytes))
		fmt.Fprintln(&b, "  flagged, replied, and changed messages are skipped at move time")
		if pv.Kept > 0 {
			fmt.Fprintf(&b, "  kept: %d message(s) look transactional (receipts, orders, security) and are excluded\n", pv.Kept)
		}
		fmt.Fprintf(&b, "  scope: %d sender(s) matched-only, %d sender(s) everything from the sender\n",
			pv.DeleteSenders-pv.DeleteAllSenders, pv.DeleteAllSenders)
		fmt.Fprintf(&b, "  folders: %s\n", strings.Join(pv.Folders, ", "))
		if pv.TrashNote != "" {
			fmt.Fprintf(&b, "  %s\n", pv.TrashNote)
		} else {
			fmt.Fprintf(&b, "  destination: %s (moved, never expunged)\n", pv.Trash)
		}
	}

	if pv.DroppedSenders > 0 {
		fmt.Fprintf(&b, "Dropped before the run: %s\n", text.JoinCounts(pv.DroppedReasons))
	}
	fmt.Fprintf(&b, "Audit log: %s", pv.AuditPath)
	return b.String()
}

// keptAtScan reports whether the transactional rule already excluded this
// message from the count, using the category scan recorded. The live subject
// is checked again at move time, so this only ever under-reports.
func (r *runner) keptAtScan(m store.Message, includeKept bool) bool {
	return r.d.Keep.Enabled() && !includeKept && m.Keep != ""
}

// preFiltered reports whether a message is already excluded by a rule that
// needs no server round trip, so the confirmation count does not promise
// moves that will not happen.
func (r *runner) preFiltered(m store.Message) bool {
	if neverSource[strings.ToLower(m.Folder)] {
		return true
	}
	if r.trash != "" && strings.EqualFold(m.Folder, r.trash) {
		return true
	}
	for _, f := range m.Flags {
		switch strings.ToLower(f) {
		case "\\flagged", "\\answered":
			return true
		}
	}
	return false
}

// destinationNote returns the line to print instead of a plain destination
// when the resolved Trash is not a safe place to move mail into: unresolved,
// a spam/drafts/sent mailbox, or a folder this run also reads from.
func (r *runner) destinationNote(sourceFolders map[string]bool) string {
	if r.trashErr != nil {
		return fmt.Sprintf("destination: UNAVAILABLE (%v); deletion will be skipped", r.trashErr)
	}
	if neverDest[strings.ToLower(r.trash)] {
		return fmt.Sprintf("destination: %s is a spam/drafts/sent mailbox, not a trash one; deletion will be skipped", r.trash)
	}
	for f := range sourceFolders {
		if strings.EqualFold(f, r.trash) {
			return fmt.Sprintf("destination: %s is also a source folder in this run; those messages will be skipped", r.trash)
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// unsubscribe runs the executor over the selected senders. The URIs come from
// the newest stored message that carried them, because unsubscribe tokens
// expire; the plan's copy is the fallback.
func (r *runner) unsubscribe(ctx context.Context, entries []plan.Entry) {
	if r.d.Unsub == nil {
		return
	}
	var targets []unsub.Target
	for _, e := range entries {
		if !e.Unsubscribe {
			continue
		}
		t := unsub.Target{
			SenderKey: e.SenderKey,
			Display:   e.Display,
			URIs:      e.URIs,
			OneClick:  e.OneClick,
			Method:    headers.Method(e.Method),
		}
		msgs, err := r.d.Store.MessagesForSender(ctx, r.d.Account.ID, e.SenderKey, true)
		if err == nil {
			for _, m := range msgs {
				if len(m.UnsubURIs) > 0 {
					t.URIs = m.UnsubURIs
					t.OneClick = m.OneClick
					t.Method = headers.Method(m.Method)
					break
				}
			}
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return
	}

	// onResult runs on the executor's worker goroutines, so it only forwards
	// the result; the summary is accumulated below, in order.
	results := r.d.Unsub.Run(ctx, targets, func(res unsub.Result) {
		r.emit(Event{
			Phase:      PhaseUnsubscribe,
			SenderKey:  res.SenderKey,
			Display:    res.Display,
			Method:     string(res.Method),
			Status:     string(res.Status),
			HTTPStatus: res.HTTPStatus,
			URI:        res.URI,
			FinalURL:   res.FinalURL,
			Err:        res.Err,
		})
	})
	for _, res := range results {
		r.sum.Unsub[res.Status]++
		if res.Status == unsub.StatusManual {
			r.sum.Manual = append(r.sum.Manual, res)
		}
		r.au.Write(Line{
			Action:     actionUnsubscribe,
			SenderKey:  res.SenderKey,
			Method:     string(res.Method),
			URI:        res.URI,
			FinalURL:   res.FinalURL,
			Status:     string(res.Status),
			HTTPStatus: res.HTTPStatus,
			Error:      res.Err,
		})
		if err := r.d.Store.SetUnsubResult(ctx, r.d.Account.ID, res.SenderKey, string(res.Status), r.runID); err != nil {
			fmt.Fprintf(r.out, "warning: recording the unsubscribe result for %s failed: %v\n", res.SenderKey, err)
		}
	}
}

// deleteAll moves every eligible message to Trash, folder by folder.
func (r *runner) deleteAll(ctx context.Context, entries []plan.Entry) error {
	targets, err := r.deleteTargets(ctx, entries)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	if r.trashErr != nil {
		for _, t := range targets {
			for _, m := range t.msgs {
				r.note(reasonNoTrash)
				r.au.Write(Line{
					Action: actionSkip, SenderKey: t.entry.SenderKey, Folder: m.Folder,
					UID: m.UID, MessageID: m.MessageID, Error: reasonNoTrash,
				})
			}
		}
		fmt.Fprintf(r.out, "deletion skipped: %v\n", r.trashErr)
		return nil
	}

	r.noSource = r.protectedFolders(ctx)

	// includeKept is the per-sender waiver of the transactional rule.
	includeKept := map[string]bool{}
	for _, t := range targets {
		if t.entry.IncludeKept {
			includeKept[t.entry.SenderKey] = true
		}
	}

	// Group by folder across senders so each folder is selected once.
	type folderWork struct {
		msgs   []store.Message
		sender map[uint32]string
	}
	work := map[string]*folderWork{}
	for _, t := range targets {
		for _, m := range t.msgs {
			w := work[m.Folder]
			if w == nil {
				w = &folderWork{sender: map[uint32]string{}}
				work[m.Folder] = w
			}
			w.msgs = append(w.msgs, m)
			w.sender[m.UID] = t.entry.SenderKey
		}
	}

	for _, folder := range sortedKeys(mapKeysOf(work)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		w := work[folder]
		if err := r.deleteFolder(ctx, folder, w.msgs, w.sender, includeKept); err != nil {
			return err
		}
	}
	return nil
}

func mapKeysOf[T any](m map[string]T) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// protectedFolders is the set of folder names a delete never reads from: the
// well-known names, whatever the server marks \Trash, \Junk, \Drafts or
// \Sent, and the resolved Trash destination.
func (r *runner) protectedFolders(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for k := range neverSource {
		out[k] = true
	}
	out[strings.ToLower(r.trash)] = true
	for _, attr := range protectedSpecialUse {
		name, err := r.d.Client.FindSpecialUse(ctx, attr)
		if err != nil {
			fmt.Fprintf(r.out, "warning: looking up the %s mailbox failed: %v\n", attr, err)
			continue
		}
		if name != "" {
			out[strings.ToLower(name)] = true
		}
	}
	return out
}

func (r *runner) skipFolder(folder, reason string, msgs []store.Message, sender map[uint32]string) {
	r.sum.FoldersSkipped = append(r.sum.FoldersSkipped, folder+": "+reason)
	r.emit(Event{Phase: PhaseDelete, Folder: folder, SkipReason: reason, TotalMoved: r.sum.Moved})
	for _, m := range msgs {
		r.note(reason)
		r.au.Write(Line{
			Action: actionSkip, SenderKey: sender[m.UID], Folder: folder,
			UID: m.UID, MessageID: m.MessageID, Error: reason,
		})
	}
	fmt.Fprintf(r.out, "skipping folder %s: %s\n", folder, reason)
}

func (r *runner) deleteFolder(ctx context.Context, folder string, msgs []store.Message, sender map[uint32]string, includeKept map[string]bool) error {
	if r.noSource[strings.ToLower(folder)] {
		r.skipFolder(folder, reasonProtectedFolder, msgs, sender)
		return nil
	}

	stored, err := r.d.Store.GetFolder(ctx, r.d.Account.ID, folder)
	if err != nil {
		r.skipFolder(folder, reasonNotScanned, msgs, sender)
		return nil
	}
	info, err := r.d.Client.Select(ctx, folder)
	if err != nil {
		r.skipFolder(folder, "cannot select: "+err.Error(), msgs, sender)
		return nil
	}
	if stored.UIDValidity == 0 || info.UIDValidity != stored.UIDValidity {
		r.skipFolder(folder, reasonUIDValidity, msgs, sender)
		return nil
	}

	uids := make([]uint32, 0, len(msgs))
	byUID := make(map[uint32]store.Message, len(msgs))
	for _, m := range msgs {
		if _, dup := byUID[m.UID]; dup {
			continue
		}
		byUID[m.UID] = m
		uids = append(uids, m.UID)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })

	flags, err := r.d.Client.FetchFlags(ctx, uids)
	if err != nil {
		return err
	}
	ids, err := r.d.Client.FetchIdentity(ctx, uids)
	if err != nil {
		return err
	}

	eligible := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		m := byUID[uid]
		if reason, ok := r.ineligible(m, flags, ids, uid, includeKept[sender[uid]]); ok {
			r.note(reason)
			r.au.Write(Line{
				Action: actionSkip, SenderKey: sender[uid], Folder: folder,
				UID: uid, MessageID: m.MessageID, GmMsgID: m.GmMsgID, Error: reason,
			})
			continue
		}
		eligible = append(eligible, uid)
	}
	if len(eligible) == 0 {
		return nil
	}
	if err := r.au.Err(); err != nil {
		return err
	}

	for start := 0; start < len(eligible); start += moveBatch {
		end := start + moveBatch
		if end > len(eligible) {
			end = len(eligible)
		}
		batch := eligible[start:end]

		// Write ahead: the intent to move every UID in this batch is on disk
		// and fsynced before the MOVE goes out, so a crash or a full disk can
		// leave a message undo cannot find, but never a message undo does not
		// know about.
		for _, uid := range batch {
			m := byUID[uid]
			r.au.Write(Line{
				Action: actionMove, Status: statusPending, SenderKey: sender[uid], Folder: folder,
				UID: uid, MessageID: m.MessageID, GmMsgID: m.GmMsgID,
				From: folder, To: r.trash,
			})
		}
		if err := r.au.Sync(); err != nil {
			return r.auditStopped(err)
		}

		if err := r.d.Client.MoveUIDs(ctx, batch, r.trash); err != nil {
			r.au.Write(Line{
				Action: actionError, Folder: folder, From: folder, To: r.trash,
				Error: err.Error(),
			})
			return err
		}
		r.sum.Moved += len(batch)
		r.emit(Event{Phase: PhaseDelete, Folder: folder, Moved: len(batch), TotalMoved: r.sum.Moved})

		r.au.Write(Line{
			Action: actionMove, Status: statusDone, Folder: folder,
			UIDs: append([]uint32(nil), batch...), From: folder, To: r.trash,
		})
		if err := r.au.Sync(); err != nil {
			return r.auditStopped(err)
		}

		if err := r.d.Store.DeleteMessageRows(ctx, r.d.Account.ID, folder, batch); err != nil {
			return err
		}
	}
	return nil
}

// auditStopped annotates an audit failure with the damage done so far.
func (r *runner) auditStopped(err error) error {
	return fmt.Errorf("%w; stopping the run, %d message(s) already moved to %s", err, r.sum.Moved, r.trash)
}

// ineligible applies the per-message safety rules: flagged and answered
// messages are never moved, transactional mail is never moved, and the
// message living at the recorded UID must still be the one that was scanned.
//
// Flags come from two places and both count. A UID missing from the
// apply-time FLAGS map is unknown, not clean, so it is skipped; and a message
// the scan recorded as flagged or answered stays protected even if the live
// fetch disagrees.
//
// The keep rule works the same way: the live subject decides, and the
// category the scan recorded still counts if the live subject somehow does
// not match. Only an explicit include_kept on the plan entry waives it.
func (r *runner) ineligible(m store.Message, flags map[uint32][]string, ids map[uint32]imapx.Identity, uid uint32, includeKept bool) (string, bool) {
	if reason, ok := flagged(m.Flags); ok {
		return reason, true
	}
	live, ok := flags[uid]
	if !ok {
		return reasonFlagsUnavailable, true
	}
	if reason, ok := flagged(live); ok {
		return reason, true
	}
	id, ok := ids[uid]
	if !ok {
		return reasonGone, true
	}
	if m.MessageID == "" {
		return reasonNoMessageID, true
	}
	if id.MessageID != m.MessageID {
		return reasonIdentityMismatch, true
	}
	if r.d.Keep.Enabled() && !includeKept {
		if hit, category := r.d.Keep.Match(id.Subject); hit {
			return keptReason(category), true
		}
		if m.Keep != "" {
			return keptReason(m.Keep), true
		}
	}
	return "", false
}

func flagged(flags []string) (string, bool) {
	for _, f := range flags {
		switch strings.ToLower(f) {
		case "\\flagged":
			return reasonFlagged, true
		case "\\answered":
			return reasonAnswered, true
		}
	}
	return "", false
}
