// Package scan performs incremental IMAP header scans into the local store.
package scan

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/rs/zerolog/log"

	"github.com/Rake-Pro/mailshear/internal/headers"
	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/keep"
	"github.com/Rake-Pro/mailshear/internal/store"
)

const (
	defaultChunkSize = 500
	gmailAllMail     = "[Gmail]/All Mail"
)

// Progress is one scan checkpoint. Done and Total are per folder; Fetched and
// Bulk are running totals across every folder scanned so far. Total is 0 when
// the server did not report a usable UIDNEXT.
type Progress struct {
	Folder  string
	Done    int
	Total   int
	Fetched int
	Bulk    int
}

type Options struct {
	ChunkSize int
	Prefilter string
	// Keep classifies each message's subject so apply can refuse to delete
	// receipts and security mail. The zero value is the built-in list.
	Keep keep.Matcher
	// AllMailFallback lets Run look the Gmail all-mail folder up by
	// special-use attribute when the default name does not select. Set it
	// only when the folder list was defaulted rather than configured, since
	// a localized Gmail UI renames that folder.
	AllMailFallback bool
	// OnProgress is called from Run's goroutine after every chunk. Run holds
	// no locks while calling it, so it is safe to forward the value to
	// another goroutine (a Bubble Tea program, say); it must not call back
	// into scan.
	OnProgress func(Progress)
}

type Summary struct {
	Folders int
	Fetched int
	Bulk    int
	Resets  int
}

// wellKnownSkip are folders never worth scanning even when the server does not
// advertise special-use attributes for them.
var wellKnownSkip = map[string]bool{
	"[gmail]/trash":     true,
	"[gmail]/spam":      true,
	"[gmail]/drafts":    true,
	"[gmail]/sent mail": true,
	"trash":             true,
	"junk":              true,
	"spam":              true,
	"drafts":            true,
	"sent":              true,
}

var specialUseSkip = []imap.MailboxAttr{
	imap.MailboxAttrDrafts,
	imap.MailboxAttrSent,
	imap.MailboxAttrJunk,
	imap.MailboxAttrTrash,
}

func Run(ctx context.Context, st *store.Store, cl *imapx.Client, acct store.Account, folders []string, opts Options) (Summary, error) {
	var sum Summary
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = defaultChunkSize
	}

	skip := map[string]bool{}
	for _, attr := range specialUseSkip {
		name, err := cl.FindSpecialUse(ctx, attr)
		if err != nil {
			log.Warn().Err(err).Str("attr", string(attr)).Msg("special-use lookup failed")
			continue
		}
		if name != "" {
			skip[name] = true
		}
	}

	prefilterWarned := false
	selectable, unselectable := 0, 0
	var selectErr error
	for _, folder := range folders {
		if skip[folder] || wellKnownSkip[strings.ToLower(folder)] {
			log.Info().Str("folder", folder).Msg("skipping folder (drafts/sent/junk/trash)")
			continue
		}
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		name, info, err := selectFolder(ctx, cl, folder, opts)
		if err != nil {
			log.Warn().Err(err).Str("folder", folder).Msg("skipping folder that cannot be selected")
			unselectable++
			selectErr = err
			continue
		}
		selectable++
		if err := runFolder(ctx, st, cl, acct, name, info, opts, &sum, &prefilterWarned); err != nil {
			return sum, err
		}
	}
	if selectable == 0 && unselectable > 0 {
		return sum, selectErr
	}
	return sum, nil
}

// selectFolder examines folder. A defaulted Gmail all-mail folder that does not
// select is retried once under the name the server reports for the special-use
// \All attribute, which a localized Gmail UI changes.
func selectFolder(ctx context.Context, cl *imapx.Client, folder string, opts Options) (string, imapx.MailboxInfo, error) {
	info, err := cl.SelectReadOnly(ctx, folder)
	if err == nil || !opts.AllMailFallback || folder != gmailAllMail {
		return folder, info, err
	}

	alt, findErr := cl.FindSpecialUse(ctx, imap.MailboxAttrAll)
	if findErr != nil || alt == "" || alt == folder {
		return folder, imapx.MailboxInfo{}, err
	}
	altInfo, altErr := cl.SelectReadOnly(ctx, alt)
	if altErr != nil {
		return folder, imapx.MailboxInfo{}, err
	}
	log.Info().Str("folder", alt).Msg("using the server's all-mail folder")
	return alt, altInfo, nil
}

func runFolder(ctx context.Context, st *store.Store, cl *imapx.Client, acct store.Account, folder string, info imapx.MailboxInfo, opts Options, sum *Summary, prefilterWarned *bool) error {
	sum.Folders++

	lastUID := uint32(0)
	f, err := st.GetFolder(ctx, acct.ID, folder)
	switch {
	case err == nil:
		if needsReset(f.UIDValidity, info.UIDValidity) {
			log.Warn().Str("folder", folder).
				Uint32("was", f.UIDValidity).Uint32("now", info.UIDValidity).
				Msg("uidvalidity changed, rescanning folder from zero")
			if err := st.ResetFolder(ctx, acct.ID, folder, info.UIDValidity); err != nil {
				return err
			}
			sum.Resets++
		} else {
			lastUID = f.LastUID
		}
	case errors.Is(err, store.ErrNotFound):
	default:
		return err
	}

	if openRange(info.UIDNext) {
		return runOpenRange(ctx, st, cl, acct, folder, info, lastUID, opts, sum, prefilterWarned)
	}

	if info.UIDNext-1 <= lastUID {
		log.Debug().Str("folder", folder).Msg("nothing new")
		return nil
	}
	maxUID := info.UIDNext - 1
	total := int(maxUID - lastUID)

	allowed, useAllowed := prefilterUIDs(ctx, cl, opts.Prefilter, lastUID+1, maxUID, prefilterWarned)

	seen := map[string]store.Sender{}
	done := 0
	for lo := lastUID + 1; ; {
		if err := ctx.Err(); err != nil {
			touch(ctx, st, acct.ID, seen)
			return err
		}
		hi := chunkHi(lo, maxUID, opts.ChunkSize)

		var msgs []store.Message
		err := cl.FetchHeaders(ctx, lo, hi, func(h imapx.Header) error {
			if useAllowed && !allowed[h.UID] {
				return nil
			}
			if m, ok := collect(acct.ID, folder, h, opts.Keep, sum, seen); ok {
				msgs = append(msgs, m)
			}
			return nil
		})
		if err != nil {
			touch(ctx, st, acct.ID, seen)
			return err
		}
		if err := st.InsertMessages(ctx, msgs); err != nil {
			return err
		}
		if err := st.SetFolderCursor(ctx, acct.ID, folder, info.UIDValidity, hi); err != nil {
			return err
		}

		done += len(msgs)
		if opts.OnProgress != nil {
			opts.OnProgress(Progress{
				Folder: folder, Done: done, Total: total,
				Fetched: sum.Fetched, Bulk: sum.Bulk,
			})
		}
		if hi >= maxUID {
			break
		}
		lo = hi + 1
	}

	return touch(ctx, st, acct.ID, seen)
}

// runOpenRange sweeps a folder whose server did not report a usable UIDNEXT.
// The whole remaining range is fetched in one open-ended command and the cursor
// advances to the highest UID the server actually returned.
func runOpenRange(ctx context.Context, st *store.Store, cl *imapx.Client, acct store.Account, folder string, info imapx.MailboxInfo, lastUID uint32, opts Options, sum *Summary, prefilterWarned *bool) error {
	allowed, useAllowed := prefilterUIDs(ctx, cl, opts.Prefilter, lastUID+1, 0, prefilterWarned)

	seen := map[string]store.Sender{}
	var msgs []store.Message
	maxSeen := lastUID
	err := cl.FetchHeaders(ctx, lastUID+1, 0, func(h imapx.Header) error {
		if h.UID > maxSeen {
			maxSeen = h.UID
		}
		if useAllowed && !allowed[h.UID] {
			return nil
		}
		if m, ok := collect(acct.ID, folder, h, opts.Keep, sum, seen); ok {
			msgs = append(msgs, m)
		}
		return nil
	})
	if err != nil {
		touch(ctx, st, acct.ID, seen)
		return err
	}
	if err := st.InsertMessages(ctx, msgs); err != nil {
		return err
	}
	if maxSeen > lastUID {
		if err := st.SetFolderCursor(ctx, acct.ID, folder, info.UIDValidity, maxSeen); err != nil {
			return err
		}
	}
	if opts.OnProgress != nil {
		// The folder size is unknown without UIDNEXT.
		opts.OnProgress(Progress{
			Folder: folder, Done: len(msgs), Total: 0,
			Fetched: sum.Fetched, Bulk: sum.Bulk,
		})
	}
	return touch(ctx, st, acct.ID, seen)
}

// openRange reports whether the folder must be swept with an open-ended UID
// range because the server did not report a usable UIDNEXT.
func openRange(uidNext uint32) bool {
	return uidNext == 0
}

// collect turns one fetched header into a message to store. A header that
// cannot be converted is counted and skipped, never treated as an error.
func collect(accountID int64, folder string, h imapx.Header, km keep.Matcher, sum *Summary, seen map[string]store.Sender) (store.Message, bool) {
	sum.Fetched++
	m, ok := buildMessage(accountID, folder, h, km)
	if !ok {
		return store.Message{}, false
	}
	if m.HasUnsub {
		sum.Bulk++
	}
	noteSender(seen, m)
	return m, true
}

// needsReset reports whether a stored UIDVALIDITY invalidates the cursor. A
// stored zero means the folder was recorded without one, so it is trusted.
func needsReset(stored, current uint32) bool {
	return stored != 0 && stored != current
}

func chunkHi(lo, maxUID uint32, size int) uint32 {
	if size <= 0 {
		size = defaultChunkSize
	}
	if uint64(lo)+uint64(size)-1 < uint64(maxUID) {
		return lo + uint32(size) - 1
	}
	return maxUID
}

func touch(ctx context.Context, st *store.Store, accountID int64, seen map[string]store.Sender) error {
	if len(seen) == 0 {
		return nil
	}
	// Use a detached context so an interrupted scan still records what it saw.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return st.TouchSenders(tctx, accountID, seen)
}

func prefilterUIDs(ctx context.Context, cl *imapx.Client, query string, lo, hi uint32, warned *bool) (map[uint32]bool, bool) {
	if query == "" || !cl.Gmail {
		return nil, false
	}
	uids, err := cl.SearchGmailRaw(ctx, query, lo, hi)
	if err != nil {
		if !*warned {
			*warned = true
			log.Warn().Err(err).Msg("gmail prefilter unavailable, falling back to a full header sweep")
		}
		return nil, false
	}
	set := make(map[uint32]bool, len(uids))
	for _, u := range uids {
		set[u] = true
	}
	return set, true
}

func noteSender(seen map[string]store.Sender, m store.Message) {
	s, ok := seen[m.SenderKey]
	if !ok {
		s = store.Sender{
			SenderKey: m.SenderKey,
			DomainKey: m.DomainKey,
			ListID:    m.ListID,
			FirstSeen: m.InternalDate,
			LastSeen:  m.InternalDate,
		}
	}
	if display := displayFor(m); display != "" {
		s.Display = display
	}
	if s.DomainKey == "" {
		s.DomainKey = m.DomainKey
	}
	if s.ListID == "" {
		s.ListID = m.ListID
	}
	if !m.InternalDate.IsZero() {
		if s.FirstSeen.IsZero() || m.InternalDate.Before(s.FirstSeen) {
			s.FirstSeen = m.InternalDate
		}
		if m.InternalDate.After(s.LastSeen) {
			s.LastSeen = m.InternalDate
		}
	}
	seen[m.SenderKey] = s
}

func displayFor(m store.Message) string {
	if m.FromDisplay != "" {
		return m.FromDisplay
	}
	if m.FromAddress != "" {
		return m.FromAddress
	}
	return m.ListID
}

const subjectRunes = 200

func field(h imapx.Header, name string) string {
	v := h.Fields[name]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// fieldAll joins a header that appeared more than once, so classification sees
// every URI a sender split across repeated headers.
func fieldAll(h imapx.Header, name string) string {
	return strings.Join(h.Fields[name], ", ")
}

func buildMessage(accountID int64, folder string, h imapx.Header, km keep.Matcher) (store.Message, bool) {
	display, addr := headers.ParseFrom(field(h, "From"))
	if addr == "" {
		display, addr = headers.ParseFrom(field(h, "Sender"))
	}
	listID := headers.ParseListID(field(h, "List-Id"))
	if addr == "" && listID == "" {
		return store.Message{}, false
	}

	rawUnsub := fieldAll(h, "List-Unsubscribe")
	u := headers.Classify(rawUnsub, fieldAll(h, "List-Unsubscribe-Post"))
	// A present but unparseable header still marks the message as bulk.
	hasUnsub := len(u.URIs) > 0 || strings.TrimSpace(rawUnsub) != ""
	if hasUnsub && len(u.URIs) == 0 {
		// Visible with -v so unparseable headers can be collected for the parser corpus.
		raw := rawUnsub
		if len(raw) > 300 {
			raw = raw[:300] + "..."
		}
		log.Debug().Uint32("uid", h.UID).Str("list_unsubscribe", raw).Msg("unparseable List-Unsubscribe")
	}

	m := store.Message{
		AccountID:    accountID,
		Folder:       folder,
		UID:          h.UID,
		GmMsgID:      h.GmMsgID,
		MessageID:    headers.TrimAngles(field(h, "Message-Id")),
		SenderKey:    headers.SenderKey(listID, addr),
		DomainKey:    headers.DomainKey(addr),
		FromAddress:  addr,
		ListID:       listID,
		HasUnsub:     hasUnsub,
		Flags:        h.Flags,
		InternalDate: h.InternalDate,
		Size:         h.Size,
		GmLabels:     h.GmLabels,
	}
	// Classify before the subject is dropped: a non-bulk row keeps no
	// subject, but its transactional category is what marks a mixed sender
	// like PayPal as one whose mail must not be deleted.
	subject := headers.DecodeSubject(field(h, "Subject"))
	if ok, cat := km.Match(subject); ok {
		m.Keep = cat
	}
	if hasUnsub {
		m.Method = string(u.Method)
		m.UnsubURIs = u.URIs
		m.OneClick = u.OneClick
		m.FromDisplay = display
		m.Subject = truncateRunes(subject, subjectRunes)
	}
	return m, true
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
