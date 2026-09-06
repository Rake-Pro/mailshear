// Package tui is the whole user interface: accounts, setup, scan, review,
// confirm, apply, results, run history and maintenance, driven by RunFlow.
// The screens hold no state outside the model and reach the mailbox, the
// database and the audit log only through the Backend interface; LiveBackend
// is the one implementation that touches any of them.
package tui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/help"

	"github.com/Rake-Pro/mailshear/internal/store"
)

// Options configures one review session.
type Options struct {
	// Account is the account name, shown in the title bar.
	Account string
	// ProtectedKeys marks sender keys protected in the database.
	ProtectedKeys map[string]bool
	// ProtectedByConfig reports whether the config's protect list covers a
	// group. May be nil.
	ProtectedByConfig func(g store.SenderGroup) bool
	// OpenURL opens an unsubscribe URI in the system browser. May be nil.
	OpenURL func(string) error
	// ShowDecided starts the screen with previously decided rows visible.
	ShowDecided bool
	// Embedded reports that the review screen is one step of the full flow
	// rather than the whole program, so leaving it must not quit Bubble Tea.
	Embedded bool
	// SenderMode starts the screen grouped by sender rather than by domain.
	SenderMode bool
	// Version is the build version, shown in the top bar.
	Version string
}

// Result is what the review session decided.
type Result struct {
	// Written reports that the user asked for a plan (w) rather than quitting.
	Written bool
	// Decisions holds one entry per sender with at least one selection. It is
	// empty unless Written.
	Decisions []store.Decision
	// NewlyProtected lists sender keys protected during this session. It is
	// returned whether or not a plan was written.
	NewlyProtected []string
}

type selection struct {
	unsubscribe   bool
	deleteMatched bool
	deleteAll     bool
	// includeKept waives the transactional-mail rule for this sender. It is
	// not a selection on its own: without a delete it means nothing, so any
	// does not count it and the entry is dropped.
	includeKept bool
}

func (s selection) any() bool {
	return s.unsubscribe || s.deleteMatched || s.deleteAll
}

func (s selection) deletes() bool {
	return s.deleteMatched || s.deleteAll
}

// tri is the state of one checkbox across the members of a row: none of them,
// some of them, or all of them.
type tri int

const (
	triNone tri = iota
	triSome
	triAll
)

// sectionID partitions the table. The order here is the order on screen.
type sectionID int

const (
	sectionProtected sectionID = iota
	sectionKeep
	sectionStillSending
	sectionBulk
	numSections
)

func (s sectionID) String() string {
	switch s {
	case sectionProtected:
		return "Protected"
	case sectionKeep:
		return "Keep: receipts and security"
	case sectionStillSending:
		return "Still sending"
	default:
		return "Bulk"
	}
}

// item is one displayed row: a section header, a sender group, a domain that
// merges several sender groups, or one member of an expanded domain.
type item struct {
	// marker marks a section header line. It carries no decision and no
	// counters except headerCount.
	marker      bool
	headerCount int
	// section is the partition this row belongs to. Header rows carry the
	// section they introduce; member rows carry their parent's.
	section sectionID
	// child marks a member row shown under an expanded domain row.
	child bool

	key     string
	members []string
	// children are the member rows of a domain row, newest-heaviest first.
	// Only domain rows have them.
	children []item

	display string
	// brand is the label for a domain row: the display name most of its
	// members use.
	brand string
	// senders is how many sender keys a domain row merges.
	senders int

	address string
	listID  string
	domain  string

	count     int
	mixed     int
	keepCount int
	// keepCategories are the transactional categories seen anywhere in this
	// row's mail, header or not.
	keepCategories []string
	flagged        int
	replied        int
	size           int64
	firstSeen      time.Time
	lastSeen       time.Time

	method   string
	uris     []string
	oneClick bool

	protected         bool
	decided           bool
	decision          string
	decidedAt         time.Time
	unsubStatus       string
	unsubAt           time.Time
	stillSending      bool
	stillSendingCount int

	subjects []string
	folders  []string
	labels   []string
}

// expandable reports whether Enter on this row opens a member breakdown.
func (it item) expandable() bool {
	return len(it.children) > 1
}

// keepish reports whether the row's mail looks transactional enough to belong
// in the keep section: anything at all matched, or the sender also sends mail
// without List-Unsubscribe and a fifth of its bulk mail matched. That second
// shape is PayPal and Chase, whose receipts arrive beside their marketing.
func (it item) keepish() bool {
	if len(it.keepCategories) > 0 {
		return true
	}
	return it.mixed > 0 && it.count > 0 && it.keepCount*5 >= it.count
}

// sectionOf places a row. Protection wins over everything, then the keep
// rule, then the still-sending escalation.
func sectionOf(it item) sectionID {
	switch {
	case it.protected:
		return sectionProtected
	case it.keepish():
		return sectionKeep
	case it.stillSending:
		return sectionStillSending
	default:
		return sectionBulk
	}
}

// headerLabel is the text of a section header row.
func headerLabel(s sectionID, n int) string {
	return fmt.Sprintf("%s (%d)", s, n)
}

type sortMode int

const (
	sortCount sortMode = iota
	sortLastSeen
	sortSize
	sortSender
)

func (s sortMode) String() string {
	switch s {
	case sortLastSeen:
		return "last seen"
	case sortSize:
		return "size"
	case sortSender:
		return "sender"
	default:
		return "count"
	}
}

const (
	minWidth  = 70
	minHeight = 16
	detailRow = 8
)

type model struct {
	opts   Options
	groups []store.SenderGroup
	byKey  map[string]store.SenderGroup

	sel            map[string]selection
	protected      map[string]bool
	newlyProtected []string

	items  []item
	hidden int
	// rows holds the plain cell text for items, in the same order. The view
	// colors them per column at render time; the selected row is rendered
	// plain under the selection background instead, so no style is nested
	// inside another.
	rows [][]string

	cursor int
	offset int
	// viewH is how many table rows fit on screen, borders and chrome already
	// subtracted.
	viewH int

	help    help.Model
	keys    keyMap
	styles  styles
	width   int
	height  int
	ready   bool
	sorting sortMode

	domainMode  bool
	showDecided bool
	filtering   bool
	filterText  string
	filterDraft string
	filterSaved string
	detail      bool
	notice      string

	// expanded holds the domain rows showing their members.
	expanded map[string]bool
	// collapsed holds the sections whose rows are hidden.
	collapsed map[sectionID]bool

	deleteAllAcked   bool
	includeKeptAcked bool
	confirming       bool
	// pendingField is what the open confirmation would set: "delete_all" or
	// "include_kept".
	pendingField string
	pendingKeys  []string
	pendingValue bool

	written  bool
	quitting bool
	// confirmQuit is the "quit without applying?" guard, raised only when
	// there are selections to lose.
	confirmQuit bool
}
