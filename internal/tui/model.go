package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"

	"github.com/Rake-Pro/mailshear/internal/store"
)

func newModel(groups []store.SenderGroup, opts Options) model {
	m := model{
		opts:        opts,
		groups:      groups,
		byKey:       make(map[string]store.SenderGroup, len(groups)),
		sel:         map[string]selection{},
		protected:   map[string]bool{},
		expanded:    map[string]bool{},
		collapsed:   map[sectionID]bool{},
		keys:        defaultKeys(),
		styles:      newStyles(),
		showDecided: opts.ShowDecided,
		// Brand-level rows are the default: a sender like LinkedIn arrives
		// under half a dozen List-Ids and subdomains, and one row per brand
		// is the decision the user actually wants to make.
		domainMode: !opts.SenderMode,
		width:      minWidth,
		height:     24,
	}

	for _, g := range groups {
		m.byKey[g.SenderKey] = g
		if opts.ProtectedKeys[g.SenderKey] {
			m.protected[g.SenderKey] = true
		}
		if opts.ProtectedByConfig != nil && opts.ProtectedByConfig(g) {
			m.protected[g.SenderKey] = true
		}
	}

	m.help = help.New()
	m.help.ShortSeparator = "  "
	m.help.FullSeparator = "   "
	m.help.Ellipsis = "..."

	m.rebuild()
	m.relayout()
	return m
}

// colSpec is one review table column. SENDER absorbs whatever width is left.
type colSpec struct {
	title string
	width int
	right bool
}

// fixedCols is every column but SENDER; gapCols is the single space between
// each pair of columns. The delete column is four cells wide because the
// include-kept escalation renders as [d!] and [D!].
const (
	fixedCols = 3 + 4 + 6 + 10 + 8 + 5
	gapCols   = 6
	minSender = 8
)

func columns(width int) []colSpec {
	sender := width - fixedCols - gapCols
	if sender < minSender {
		sender = minSender
	}
	return []colSpec{
		{title: "U", width: 3},
		{title: "D", width: 4},
		{title: "SENDER", width: sender},
		{title: "COUNT", width: 6, right: true},
		{title: "LAST", width: 10},
		{title: "METHOD", width: 8},
		{title: "FLAGS", width: 5},
	}
}

// baseItems turns the sender groups into display units, merging by registrable
// domain when grouping is on. A domain row keeps its members as children so
// the row can be expanded, filtered and broken down without a second pass.
func (m *model) baseItems() []item {
	if !m.domainMode {
		out := make([]item, 0, len(m.groups))
		for _, g := range m.groups {
			out = append(out, m.itemFromGroup(g))
		}
		return out
	}

	order := []string{}
	merged := map[string]*item{}
	newest := map[string]time.Time{}
	assigned := map[string]bool{}
	for _, g := range m.groups {
		domain := g.DomainKey
		if domain == "" {
			domain = g.SenderKey
		}
		it, ok := merged[domain]
		if !ok {
			cp := item{key: domain, display: cell(domain), domain: cell(domain)}
			merged[domain] = &cp
			it = &cp
			order = append(order, domain)
			newest[domain] = time.Time{}
		}
		child := m.itemFromGroup(g)
		child.child = true
		it.children = append(it.children, child)
		it.members = append(it.members, g.SenderKey)
		it.count += g.Count
		it.mixed += g.MixedCount
		it.keepCount += g.KeepCount
		it.keepCategories = appendUnique(it.keepCategories, cells(g.KeepCategories), 8)
		it.flagged += g.FlaggedCount
		it.replied += g.RepliedCount
		it.size += g.TotalSize
		if it.firstSeen.IsZero() || (!g.FirstSeen.IsZero() && g.FirstSeen.Before(it.firstSeen)) {
			it.firstSeen = g.FirstSeen
		}
		if g.LastSeen.After(it.lastSeen) {
			it.lastSeen = g.LastSeen
		}
		if m.protected[g.SenderKey] {
			it.protected = true
		}
		if g.Decision != "" {
			it.decided = true
		}
		if g.StillSending {
			it.stillSending = true
			it.stillSendingCount += g.StillSendingCount
		}
		it.subjects = appendUnique(it.subjects, cells(g.RecentSubjects), 5)
		it.folders = appendUnique(it.folders, cells(g.Folders), 8)
		it.labels = appendUnique(it.labels, cells(g.Labels), 8)

		// The most recent group in the domain supplies the actionable data:
		// unsubscribe tokens expire, so always act on the newest.
		if !assigned[domain] || g.LastSeen.After(newest[domain]) {
			assigned[domain] = true
			newest[domain] = g.LastSeen
			it.address = cell(g.Address)
			it.listID = cell(g.ListID)
			it.method = cell(g.Method)
			it.uris = cells(g.LatestURIs)
			it.oneClick = g.LatestOneClick
			it.decision = cell(g.Decision)
			it.decidedAt = g.DecidedAt
			it.unsubStatus = cell(g.UnsubStatus)
			it.unsubAt = g.UnsubAt
		}
	}

	out := make([]item, 0, len(order))
	for _, domain := range order {
		it := merged[domain]
		it.senders = len(it.members)
		it.brand = brandOf(it.children)
		sort.SliceStable(it.children, func(i, j int) bool {
			return it.children[i].count > it.children[j].count
		})
		out = append(out, *it)
	}
	return out
}

// brandOf picks the label for a domain row: the display name the most member
// senders use, with the heaviest sender breaking a tie. That turns six
// LinkedIn List-Ids into one row that says LinkedIn.
func brandOf(members []item) string {
	type tally struct {
		n     int
		count int
	}
	seen := map[string]*tally{}
	var names []string
	for _, c := range members {
		name := strings.TrimSpace(c.display)
		if name == "" {
			continue
		}
		t, ok := seen[name]
		if !ok {
			t = &tally{}
			seen[name] = t
			names = append(names, name)
		}
		t.n++
		t.count += c.count
	}
	sort.Strings(names) // ties resolve the same way on every rebuild
	best := ""
	for _, name := range names {
		if best == "" {
			best = name
			continue
		}
		t, b := seen[name], seen[best]
		if t.n > b.n || (t.n == b.n && t.count > b.count) {
			best = name
		}
	}
	return best
}

// itemFromGroup is where mail-derived text enters the review screen, so it is
// where cell() is applied: every string below is rendered somewhere, and all
// of them come from headers a sender wrote.
func (m *model) itemFromGroup(g store.SenderGroup) item {
	display := g.Display
	if display == "" {
		display = g.Address
	}
	if display == "" {
		display = g.SenderKey
	}
	return item{
		key:               g.SenderKey,
		members:           []string{g.SenderKey},
		display:           cell(display),
		address:           cell(g.Address),
		listID:            cell(g.ListID),
		domain:            cell(g.DomainKey),
		count:             g.Count,
		mixed:             g.MixedCount,
		keepCount:         g.KeepCount,
		keepCategories:    cells(g.KeepCategories),
		flagged:           g.FlaggedCount,
		replied:           g.RepliedCount,
		size:              g.TotalSize,
		firstSeen:         g.FirstSeen,
		lastSeen:          g.LastSeen,
		method:            cell(g.Method),
		uris:              cells(g.LatestURIs),
		oneClick:          g.LatestOneClick,
		protected:         m.protected[g.SenderKey],
		decided:           g.Decision != "",
		decision:          cell(g.Decision),
		decidedAt:         g.DecidedAt,
		unsubStatus:       cell(g.UnsubStatus),
		unsubAt:           g.UnsubAt,
		stillSending:      g.StillSending,
		stillSendingCount: g.StillSendingCount,
		subjects:          cells(g.RecentSubjects),
		folders:           cells(g.Folders),
		labels:            cells(g.Labels),
	}
}

func appendUnique(dst, src []string, limit int) []string {
	for _, s := range src {
		if len(dst) >= limit {
			return dst
		}
		found := false
		for _, have := range dst {
			if have == s {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, s)
		}
	}
	return dst
}

// matches tests the row and, for a domain row, its members: filtering for one
// LinkedIn subdomain must not hide the brand row that carries it.
func (it item) matches(needle string) bool {
	if needle == "" {
		return true
	}
	if it.matchesSelf(needle) {
		return true
	}
	for _, c := range it.children {
		if c.matchesSelf(needle) {
			return true
		}
	}
	return false
}

func (it item) matchesSelf(needle string) bool {
	fields := []string{it.display, it.brand, it.address, it.domain, it.listID, it.key}
	fields = append(fields, it.subjects...)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

func sortItems(items []item, mode sortMode) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch mode {
		case sortLastSeen:
			if !a.lastSeen.Equal(b.lastSeen) {
				return a.lastSeen.After(b.lastSeen)
			}
		case sortSize:
			if a.size != b.size {
				return a.size > b.size
			}
		case sortSender:
			ad, bd := strings.ToLower(a.label()), strings.ToLower(b.label())
			if ad != bd {
				return ad < bd
			}
		default:
			if a.count != b.count {
				return a.count > b.count
			}
			if !a.lastSeen.Equal(b.lastSeen) {
				return a.lastSeen.After(b.lastSeen)
			}
		}
		return a.key < b.key
	})
}

// label is the name a row sorts and reads by: the brand for a domain row,
// the display name otherwise.
func (it item) label() string {
	if it.brand != "" {
		return it.brand
	}
	return it.display
}

// rebuild recomputes the visible rows from the current grouping, filter, sort,
// hide-decided and section state, keeping the cursor where it was.
func (m *model) rebuild() {
	wantKey, wantSection := m.cursorTarget()

	needle := strings.ToLower(strings.TrimSpace(m.filterText))
	base := m.baseItems()

	var buckets [numSections][]item
	m.hidden = 0
	for _, it := range base {
		if !it.matches(needle) {
			continue
		}
		if it.decided && !it.stillSending && !m.showDecided {
			m.hidden++
			continue
		}
		s := sectionOf(it)
		it.section = s
		buckets[s] = append(buckets[s], it)
	}

	items := make([]item, 0, len(base)+int(numSections))
	for s := sectionID(0); s < numSections; s++ {
		rows := buckets[s]
		if len(rows) == 0 {
			continue
		}
		sortItems(rows, m.sorting)
		items = append(items, item{
			marker: true, section: s, headerCount: len(rows),
			display: headerLabel(s, len(rows)),
		})
		if m.collapsed[s] {
			continue
		}
		for _, it := range rows {
			items = append(items, it)
			if !it.expandable() || !m.expanded[it.key] {
				continue
			}
			for _, c := range it.children {
				c.section = s
				items = append(items, c)
			}
		}
	}
	m.items = items

	m.rows = make([][]string, 0, len(items))
	for _, it := range items {
		m.rows = append(m.rows, m.row(it))
	}

	m.restoreCursor(wantKey, wantSection)
	m.clampOffset()
}

// cursorTarget records what the cursor is on so rebuild can put it back: a
// row key, or the section of a header line.
func (m *model) cursorTarget() (key string, section sectionID) {
	i := m.cursor
	if i < 0 || i >= len(m.items) {
		return "", -1
	}
	if m.items[i].marker {
		return "", m.items[i].section
	}
	return m.items[i].key, -1
}

func (m *model) restoreCursor(key string, section sectionID) {
	if key != "" {
		for i, it := range m.items {
			if !it.marker && it.key == key {
				m.cursor = i
				return
			}
		}
	}
	if section >= 0 {
		for i, it := range m.items {
			if it.marker && it.section == section {
				m.cursor = i
				return
			}
		}
	}
	m.cursor = 0
	m.skipMarker(1)
}

// row is the plain cell text for one item. Colour is applied at render time,
// so these values stay comparable in tests and diff-able by eye.
func (m *model) row(it item) []string {
	if it.marker {
		return []string{"", "", m.markerCell(it), "", "", "", ""}
	}

	u, d := "[ ]", "[  ]"
	if it.protected {
		u, d = "[-]", "[- ]"
	} else {
		uState, dState, allState, kept := m.selState(it)
		switch uState {
		case triAll:
			u = "[x]"
		case triSome:
			u = "[~]"
		}
		bang := ""
		if kept != triNone {
			bang = "!"
		}
		switch {
		case allState == triAll:
			d = "[D" + pad2(bang) + "]"
		case dState == triAll:
			d = "[d" + pad2(bang) + "]"
		case allState == triSome || dState == triSome:
			d = "[~ ]"
		}
	}

	return []string{
		u, d,
		it.senderCell(),
		itoa(it.count),
		dayString(it.lastSeen),
		it.method,
		flagString(it),
	}
}

// pad2 keeps the delete cell four cells wide whether or not the include-kept
// bang is there, so the column never shifts under the cursor.
func pad2(s string) string {
	if s == "" {
		return " "
	}
	return s
}

// markerCell is a section header's text, with the collapse indicator.
func (m *model) markerCell(it item) string {
	arrow := "v"
	if m.collapsed[it.section] {
		arrow = ">"
	}
	return arrow + " " + it.display
}

// senderCell is the sender column: a domain row shows its brand, its domain
// and how many senders it merges; a member row is indented one level.
func (it item) senderCell() string {
	if it.child {
		return "    " + it.display
	}
	prefix := "  "
	if it.expandable() {
		prefix = "> "
	}
	if it.brand != "" && it.senders > 1 {
		return prefix + it.brand + "  " + it.domain + fmt.Sprintf("  (%d senders)", it.senders)
	}
	return prefix + it.label()
}

func flagString(it item) string {
	b := []byte("     ")
	if it.mixed > 0 {
		b[0] = 'M'
	}
	if it.flagged > 0 {
		b[1] = 'F'
	}
	if it.replied > 0 {
		b[2] = 'R'
	}
	if strings.Contains(it.decision, "unsubscribe") || it.unsubStatus != "" {
		b[3] = 'U'
	}
	if it.stillSending {
		b[4] = 'S'
	}
	return string(b)
}

// selState reduces the members' selections to one checkbox state each, so a
// domain row whose members disagree renders as partial rather than lying in
// either direction.
func (m *model) selState(it item) (unsub, del, all, kept tri) {
	if len(it.members) == 0 {
		return triNone, triNone, triNone, triNone
	}
	var nu, nd, na, nk int
	for _, k := range it.members {
		s := m.sel[k]
		if s.unsubscribe {
			nu++
		}
		if s.deleteMatched {
			nd++
		}
		if s.deleteAll {
			na++
		}
		if s.includeKept {
			nk++
		}
	}
	n := len(it.members)
	return triOf(nu, n), triOf(nd, n), triOf(na, n), triOf(nk, n)
}

func triOf(got, total int) tri {
	switch {
	case got == 0:
		return triNone
	case got == total:
		return triAll
	default:
		return triSome
	}
}

// selectionOf is the conjunction across members: what is true of the whole
// row. Toggles read it so pressing the same key twice clears a mixed row.
func (m *model) selectionOf(it item) selection {
	if len(it.members) == 0 {
		return selection{}
	}
	out := selection{unsubscribe: true, deleteMatched: true, deleteAll: true, includeKept: true}
	for _, k := range it.members {
		s := m.sel[k]
		out.unsubscribe = out.unsubscribe && s.unsubscribe
		out.deleteMatched = out.deleteMatched && s.deleteMatched
		out.deleteAll = out.deleteAll && s.deleteAll
		out.includeKept = out.includeKept && s.includeKept
	}
	return out
}

func (m *model) setSelection(it item, apply func(*selection)) {
	for _, k := range it.members {
		s := m.sel[k]
		apply(&s)
		if s.any() {
			m.sel[k] = s
		} else {
			delete(m.sel, k)
		}
	}
}

func (m *model) current() (item, bool) {
	i := m.cursor
	if i < 0 || i >= len(m.items) {
		return item{}, false
	}
	if m.items[i].marker {
		return item{}, false
	}
	return m.items[i], true
}

// currentSection is the section the cursor is in, header lines included. It
// is what a, A and x act on.
func (m *model) currentSection() (sectionID, bool) {
	i := m.cursor
	if i < 0 || i >= len(m.items) {
		return 0, false
	}
	return m.items[i].section, true
}

// skipMarker nudges the cursor off a section header, preferring the given
// direction and falling back to the other one at the ends of the list. It is
// used for the initial placement; ordinary movement may rest on a header, so
// Enter can collapse it.
func (m *model) skipMarker(dir int) {
	if len(m.items) == 0 {
		return
	}
	i := m.cursor
	if i < 0 || i >= len(m.items) || !m.items[i].marker {
		return
	}
	for j := i + dir; j >= 0 && j < len(m.items); j += dir {
		if !m.items[j].marker {
			m.moveTo(j)
			return
		}
	}
	for j := i - dir; j >= 0 && j < len(m.items); j -= dir {
		if !m.items[j].marker {
			m.moveTo(j)
			return
		}
	}
}

// moveTo puts the cursor on index i and scrolls the window to keep it visible.
func (m *model) moveTo(i int) {
	if i < 0 {
		i = 0
	}
	if i > len(m.items)-1 {
		i = len(m.items) - 1
	}
	if i < 0 {
		i = 0
	}
	m.cursor = i
	m.clampOffset()
}

// clampOffset keeps the scroll window inside the list and around the cursor.
func (m *model) clampOffset() {
	h := m.viewH
	if h < 1 {
		h = 1
	}
	maxOffset := len(m.items) - h
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *model) moveBy(n int) {
	if len(m.items) == 0 || n == 0 {
		return
	}
	target := m.cursor + n
	if target < 0 {
		target = 0
	}
	if target > len(m.items)-1 {
		target = len(m.items) - 1
	}
	m.moveTo(target)
}

func (m *model) result() Result {
	res := Result{Written: m.written, NewlyProtected: m.newlyProtected}
	if !m.written {
		return res
	}
	for _, g := range m.groups {
		s, ok := m.sel[g.SenderKey]
		if !ok || !s.any() {
			continue
		}
		res.Decisions = append(res.Decisions, store.Decision{
			SenderKey:     g.SenderKey,
			Unsubscribe:   s.unsubscribe,
			DeleteMatched: s.deleteMatched,
			DeleteAll:     s.deleteAll,
			IncludeKept:   s.includeKept && s.deletes(),
		})
	}
	return res
}
