package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	up          key.Binding
	down        key.Binding
	pageUp      key.Binding
	pageDown    key.Binding
	top         key.Binding
	bottom      key.Binding
	unsubscribe key.Binding
	deleteMatch key.Binding
	deleteAll   key.Binding
	includeKept key.Binding
	protect     key.Binding
	allUnsub    key.Binding
	allDelete   key.Binding
	clear       key.Binding
	filter      key.Binding
	sort        key.Binding
	group       key.Binding
	detail      key.Binding
	open        key.Binding
	showDecided key.Binding
	account     key.Binding
	history     key.Binding
	menu        key.Binding
	write       key.Binding
	help        key.Binding
	quit        key.Binding
	suspend     key.Binding
	cancel      key.Binding
}

// defaultKeys follows the review key table in docs/design.md section 6. The
// design's g/G for top and bottom would collide with g for grouping, so
// home/end take that job.
func defaultKeys() keyMap {
	return keyMap{
		up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/k", "up")),
		down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("down/j", "down")),
		pageUp:      key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		pageDown:    key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		top:         key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "top")),
		bottom:      key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "bottom")),
		unsubscribe: key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "unsubscribe")),
		deleteMatch: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete matched")),
		deleteAll:   key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete all")),
		includeKept: key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "include kept")),
		protect:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "protect")),
		allUnsub:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "unsub section")),
		allDelete:   key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "delete section")),
		clear:       key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "clear section")),
		filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		sort:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		group:       key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "group")),
		detail:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand/detail")),
		open:        key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open url")),
		showDecided: key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "show decided")),
		account:     key.NewBinding(key.WithKeys(","), key.WithHelp(",", "accounts")),
		history:     key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "history")),
		menu:        key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "maintenance")),
		write:       key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "the plan")),
		help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		suspend:     key.NewBinding(key.WithKeys("ctrl+z"), key.WithHelp("ctrl+z", "suspend")),
		cancel:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		k.unsubscribe, k.deleteMatch, k.deleteAll, k.includeKept, k.protect,
		k.filter, k.sort, k.group, k.detail, k.write, k.help, k.quit,
	}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.up, k.down, k.pageUp, k.pageDown, k.top, k.bottom},
		{k.unsubscribe, k.deleteMatch, k.deleteAll, k.includeKept, k.protect},
		{k.allUnsub, k.allDelete, k.clear, k.open, k.detail},
		{k.filter, k.sort, k.group, k.showDecided, k.write},
		{k.account, k.history, k.menu, k.help, k.quit, k.cancel},
	}
}
