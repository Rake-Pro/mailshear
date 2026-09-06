package store

import (
	"context"
	"fmt"
	"github.com/Rake-Pro/mailshear/internal/headers"
	"time"
)

type SenderGroup struct {
	SenderKey  string
	DomainKey  string
	Display    string
	Address    string
	ListID     string
	Count      int
	MixedCount int
	// KeepCount is how many of the sender's bulk (has_unsub) messages carry
	// a transactional subject, so a delete would refuse to move them.
	KeepCount int
	// KeepCategories lists the distinct transactional categories seen across
	// every message from the sender, header or not. A mixed sender whose
	// receipts arrive without List-Unsubscribe shows up here and nowhere
	// else, which is what puts PayPal and Chase in the keep section.
	KeepCategories []string
	FlaggedCount   int
	RepliedCount   int
	TotalSize      int64
	FirstSeen      time.Time
	LastSeen       time.Time
	Method         string
	LatestURIs     []string
	LatestOneClick bool
	RecentSubjects []string
	Folders        []string
	Labels         []string
	Protected      bool
	Decision       string
	DecidedAt      time.Time
	UnsubStatus    string
	UnsubAt        time.Time

	// LastUIDAtDecision is the highest UID this sender had when the decision
	// was recorded.
	LastUIDAtDecision uint32

	// StillSending reports that an unsubscribe was recorded as ok or probable
	// and the sender has sent mail (with List-Unsubscribe) since. The
	// reference point is UnsubAt, falling back to DecidedAt when the sender
	// has no recorded unsub attempt timestamp.
	StillSending bool

	// StillSendingCount is the number of has_unsub messages with an
	// internal_date after the same reference point used for StillSending.
	StillSendingCount int
}

func methodRank(m string) int {
	switch m {
	case "oneclick":
		return 4
	case "http":
		return 3
	case "mailto":
		return 2
	case "none":
		return 1
	default:
		return 0
	}
}

const senderGroupAggQuery = `
WITH agg AS (
	SELECT sender_key,
		SUM(CASE WHEN has_unsub = 1 THEN 1 ELSE 0 END) AS cnt,
		SUM(CASE WHEN has_unsub = 0 THEN 1 ELSE 0 END) AS mixed_cnt,
		SUM(CASE WHEN has_unsub = 1 AND keep != '' THEN 1 ELSE 0 END) AS keep_cnt,
		SUM(CASE WHEN has_unsub = 1 AND flags LIKE '%\Flagged%' THEN 1 ELSE 0 END) AS flagged_cnt,
		SUM(CASE WHEN has_unsub = 1 AND flags LIKE '%\Answered%' THEN 1 ELSE 0 END) AS replied_cnt,
		SUM(CASE WHEN has_unsub = 1 THEN size ELSE 0 END) AS total_size,
		COALESCE(MIN(internal_date), '') AS first_seen,
		COALESCE(MAX(internal_date), '') AS last_seen
	FROM messages
	WHERE account_id = ?
	GROUP BY sender_key
	HAVING cnt > 0
)
SELECT agg.sender_key, agg.cnt, agg.mixed_cnt, agg.keep_cnt, agg.flagged_cnt, agg.replied_cnt, agg.total_size,
	agg.first_seen, agg.last_seen,
	COALESCE(sn.protected, 0), COALESCE(sn.decision, ''), COALESCE(sn.decided_at, ''),
	COALESCE(sn.unsub_status, ''), COALESCE(sn.unsub_at, ''), COALESCE(sn.last_uid_at_decision, 0)
FROM agg
LEFT JOIN senders sn ON sn.account_id = ? AND sn.sender_key = agg.sender_key
ORDER BY agg.cnt DESC, agg.last_seen DESC
`

const senderGroupLatestQuery = `
SELECT sender_key, domain_key, from_display, from_address, list_id, method, unsub_uris, one_click, folder, gm_labels, subject, COALESCE(internal_date, '')
FROM messages
WHERE account_id = ? AND has_unsub = 1
ORDER BY sender_key, internal_date DESC
`

// senderKeepQuery lists the transactional categories seen per sender across
// every message, bulk or not.
const senderKeepQuery = `
SELECT sender_key, keep, COUNT(*)
FROM messages
WHERE account_id = ? AND keep != ''
GROUP BY sender_key, keep
ORDER BY sender_key, COUNT(*) DESC, keep
`

func (s *Store) SenderGroups(ctx context.Context, accountID int64) ([]SenderGroup, error) {
	rows, err := s.db.QueryContext(ctx, senderGroupAggQuery, accountID, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: sender groups: %w", err)
	}

	groups := map[string]*SenderGroup{}
	var order []string
	for rows.Next() {
		var key, firstSeen, lastSeen, decision, decidedAt, unsubStatus, unsubAt string
		var cnt, mixedCnt, keepCnt, flaggedCnt, repliedCnt, totalSize, protected, lastUID int64
		if err := rows.Scan(&key, &cnt, &mixedCnt, &keepCnt, &flaggedCnt, &repliedCnt, &totalSize,
			&firstSeen, &lastSeen, &protected, &decision, &decidedAt,
			&unsubStatus, &unsubAt, &lastUID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: sender groups: scan: %w", err)
		}
		groups[key] = &SenderGroup{
			SenderKey:    key,
			Count:        int(cnt),
			MixedCount:   int(mixedCnt),
			KeepCount:    int(keepCnt),
			FlaggedCount: int(flaggedCnt),
			RepliedCount: int(repliedCnt),
			TotalSize:    totalSize,
			FirstSeen:    textToTime(firstSeen),
			LastSeen:     textToTime(lastSeen),
			Protected:    protected != 0,
			Decision:     decision,
			DecidedAt:    textToTime(decidedAt),
			UnsubStatus:  unsubStatus,
			UnsubAt:      textToTime(unsubAt),

			LastUIDAtDecision: uint32(lastUID),
		}
		g := groups[key]
		switch g.UnsubStatus {
		case "ok", "probable":
			ref := g.UnsubAt
			if ref.IsZero() {
				ref = g.DecidedAt
			}
			g.StillSending = g.LastSeen.After(ref)
		}
		order = append(order, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: sender groups: %w", err)
	}
	rows.Close()

	if len(groups) == 0 {
		return nil, nil
	}

	keepRows, err := s.db.QueryContext(ctx, senderKeepQuery, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: sender groups: keep: %w", err)
	}
	for keepRows.Next() {
		var senderKey, category string
		var n int64
		if err := keepRows.Scan(&senderKey, &category, &n); err != nil {
			keepRows.Close()
			return nil, fmt.Errorf("store: sender groups: scan keep: %w", err)
		}
		if g, ok := groups[senderKey]; ok {
			g.KeepCategories = append(g.KeepCategories, headers.Clean(category))
		}
	}
	if err := keepRows.Err(); err != nil {
		keepRows.Close()
		return nil, fmt.Errorf("store: sender groups: keep: %w", err)
	}
	keepRows.Close()

	rows2, err := s.db.QueryContext(ctx, senderGroupLatestQuery, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: sender groups: latest: %w", err)
	}
	defer rows2.Close()

	haveLatest := map[string]bool{}
	folderSeen := map[string]map[string]bool{}
	labelSeen := map[string]map[string]bool{}

	for rows2.Next() {
		var senderKey, domainKey, fromDisplay, fromAddress, listID, method, unsubURIs, folder, gmLabels, subject, internalDate string
		var oneClick int64
		if err := rows2.Scan(&senderKey, &domainKey, &fromDisplay, &fromAddress, &listID, &method,
			&unsubURIs, &oneClick, &folder, &gmLabels, &subject, &internalDate); err != nil {
			return nil, fmt.Errorf("store: sender groups: scan latest: %w", err)
		}

		g, ok := groups[senderKey]
		if !ok {
			continue
		}

		if g.StillSending {
			ref := g.UnsubAt
			if ref.IsZero() {
				ref = g.DecidedAt
			}
			if textToTime(internalDate).After(ref) {
				g.StillSendingCount++
			}
		}

		if !haveLatest[senderKey] {
			display := fromDisplay
			if display == "" {
				display = fromAddress
			}
			g.Display = headers.DecodeSubject(display) // rows scanned before lenient decoding existed
			g.Address = headers.Clean(fromAddress)
			g.ListID = headers.Clean(listID)
			g.DomainKey = headers.Clean(domainKey)
			g.LatestURIs = cleanURIs(unmarshalStrings(unsubURIs))
			g.LatestOneClick = oneClick != 0
			haveLatest[senderKey] = true
		}

		if methodRank(method) > methodRank(g.Method) {
			g.Method = method
		}

		if folder != "" {
			if folderSeen[senderKey] == nil {
				folderSeen[senderKey] = map[string]bool{}
			}
			if !folderSeen[senderKey][folder] {
				folderSeen[senderKey][folder] = true
				g.Folders = append(g.Folders, headers.Clean(folder))
			}
		}

		for _, label := range unmarshalStrings(gmLabels) {
			if labelSeen[senderKey] == nil {
				labelSeen[senderKey] = map[string]bool{}
			}
			if !labelSeen[senderKey][label] {
				labelSeen[senderKey][label] = true
				g.Labels = append(g.Labels, headers.Clean(label))
			}
		}

		if subject != "" && len(g.RecentSubjects) < 5 {
			g.RecentSubjects = append(g.RecentSubjects, headers.DecodeSubject(subject))
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, fmt.Errorf("store: sender groups: latest: %w", err)
	}

	out := make([]SenderGroup, 0, len(order))
	for _, key := range order {
		out = append(out, *groups[key])
	}
	return out, nil
}

// cleanURIs drops the stored unsubscribe links that carry a control
// character and cleans the rest. Rows scanned before the header parser
// rejected them are covered this way without a rescan.
func cleanURIs(uris []string) []string {
	out := make([]string, 0, len(uris))
	for _, u := range uris {
		c := headers.Clean(u)
		if c == "" || c != u {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
