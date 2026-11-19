package tui

import (
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
)

// keyMap holds every binding. Views match keys against it, the footer and
// the help overlay are generated from it, and Options.Keys overrides it.
type keyMap struct {
	// Global.
	Quit, Help, Theme, Back     key.Binding
	Sessions, Live, Usage, Open key.Binding
	Up, Down, PageUp, PageDown  key.Binding
	Top, Bottom                 key.Binding

	// Sessions view.
	Resume, Fork, New  key.Binding
	Filter, Search     key.Binding
	Sort, Group, Scope key.Binding

	// Live view.
	NextPane, PrevPane, Zoom key.Binding
	MorePanes, FewerPanes    key.Binding

	// Transcript view.
	NextFold, PrevFold, Expand, ExpandAll key.Binding
	Find, NextMatch, PrevMatch            key.Binding
}

// keyGroup is a titled set of actions, as the help overlay shows them.
type keyGroup struct {
	title   string
	actions []string
}

// keyGroups orders the actions for help and decides which actions must not
// share a key: actions in the same group, or in a group and one of the
// groups every view uses ("global" and "move").
var keyGroups = []keyGroup{
	{"global", []string{"sessions", "live", "usage", "open", "help", "theme", "back", "quit"}},
	{"move", []string{"up", "down", "page_up", "page_down", "top", "bottom"}},
	{"sessions", []string{"resume", "fork", "new", "filter", "search", "sort", "group", "scope"}},
	{"live", []string{"next_pane", "prev_pane", "zoom", "more_panes", "fewer_panes", "resume"}},
	{"usage", []string{"resume"}},
	{"transcript", []string{"next_fold", "prev_fold", "expand", "expand_all", "find", "next_match", "prev_match"}},
}

// sharedGroups are the groups whose keys work in every view.
var sharedGroups = []string{"global", "move"}

func defaultKeyMap() keyMap {
	b := func(desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp("", desc))
	}
	return keyMap{
		Quit:     b("quit", "q", "ctrl+c"),
		Help:     b("help", "?"),
		Theme:    b("theme", "T"),
		Back:     b("back", "esc"),
		Sessions: b("sessions", "1"),
		Live:     b("live", "2"),
		Usage:    b("usage", "3"),
		Open:     b("transcript", "o"),

		Up:       b("up", "up", "k"),
		Down:     b("down", "down", "j"),
		PageUp:   b("page up", "pgup", "ctrl+u"),
		PageDown: b("page down", "pgdown", "ctrl+d"),
		Top:      b("top", "home"),
		Bottom:   b("bottom", "end"),

		Resume: b("resume", "enter"),
		Fork:   b("fork", "f"),
		New:    b("new", "n"),
		Filter: b("filter", "/"),
		Search: b("search", "p"),
		Sort:   b("sort", "s"),
		Group:  b("group", "g"),
		Scope:  b("scope", "a"),

		NextPane:   b("next pane", "tab"),
		PrevPane:   b("previous pane", "shift+tab"),
		Zoom:       b("zoom", "z"),
		MorePanes:  b("more panes", "+"),
		FewerPanes: b("fewer panes", "-"),

		NextFold:  b("next tool", "tab"),
		PrevFold:  b("previous tool", "shift+tab"),
		Expand:    b("expand", "enter", "space"),
		ExpandAll: b("expand all", "e"),
		Find:      b("find", "/"),
		NextMatch: b("next match", "n"),
		PrevMatch: b("previous match", "N"),
	}
}

// longHelp is the help overlay's wording where the footer's single word
// is not enough.
var longHelp = map[string]string{
	"theme":  "next theme",
	"back":   "back, clear filter",
	"open":   "open transcript",
	"resume": "resume, or focus if live",
	"fork":   "fork into a new session",
	"new":    "new session in project",
	"filter": "fuzzy filter",
	"search": "search past prompts",
	"sort":   "sort: recent, project, context, cost",
	"group":  "group by project",
	"scope":  "scope: all, live, ended",
	"zoom":   "zoom pane",

	"next_fold":  "next tool call",
	"prev_fold":  "previous tool call",
	"expand":     "expand or fold tool call",
	"expand_all": "expand or fold all",
	"find":       "find in transcript",
}

// actions maps action names, as written in a config file, to bindings.
func (k *keyMap) actions() map[string]*key.Binding {
	return map[string]*key.Binding{
		"quit": &k.Quit, "help": &k.Help, "theme": &k.Theme, "back": &k.Back,
		"sessions": &k.Sessions, "live": &k.Live, "usage": &k.Usage, "open": &k.Open,
		"up": &k.Up, "down": &k.Down, "page_up": &k.PageUp, "page_down": &k.PageDown,
		"top": &k.Top, "bottom": &k.Bottom,
		"resume": &k.Resume, "fork": &k.Fork, "new": &k.New,
		"filter": &k.Filter, "search": &k.Search,
		"sort": &k.Sort, "group": &k.Group, "scope": &k.Scope,
		"next_pane": &k.NextPane, "prev_pane": &k.PrevPane, "zoom": &k.Zoom,
		"more_panes": &k.MorePanes, "fewer_panes": &k.FewerPanes,
		"next_fold": &k.NextFold, "prev_fold": &k.PrevFold, "expand": &k.Expand,
		"expand_all": &k.ExpandAll, "find": &k.Find,
		"next_match": &k.NextMatch, "prev_match": &k.PrevMatch,
	}
}

// Actions lists the action names Options.Keys may override.
func Actions() []string {
	var k keyMap
	return slices.Sorted(maps.Keys(k.actions()))
}

// newKeyMap applies overrides to the defaults and rejects unknown actions,
// empty key lists and keys bound twice where both bindings are active.
func newKeyMap(overrides map[string][]string) (keyMap, error) {
	k := defaultKeyMap()
	acts := k.actions()
	for _, name := range slices.Sorted(maps.Keys(overrides)) {
		b, ok := acts[name]
		if !ok {
			return keyMap{}, optionErr("keys."+name, "unknown key action %q (have %s)", name, strings.Join(Actions(), ", "))
		}
		keys := overrides[name]
		if len(keys) == 0 || slices.Contains(keys, "") {
			return keyMap{}, optionErr("keys."+name, "key action %q: no key given", name)
		}
		b.SetKeys(keys...)
	}
	return k, k.checkConflicts(overrides)
}

// checkConflicts rejects a key bound to two actions that are active at the
// same time. The error names the overridden action, which is where the
// conflict was introduced.
func (k *keyMap) checkConflicts(overrides map[string][]string) error {
	acts := k.actions()
	var shared []string
	for _, g := range keyGroups {
		if slices.Contains(sharedGroups, g.title) {
			shared = append(shared, g.actions...)
		}
	}
	for _, g := range keyGroups {
		scope := g.actions
		if !slices.Contains(sharedGroups, g.title) {
			scope = append(slices.Clone(shared), g.actions...)
		} else if g.title != "global" {
			scope = append(slices.Clone(keyGroups[0].actions), g.actions...)
		}
		owner := map[string]string{}
		for _, name := range scope {
			for _, kk := range acts[name].Keys() {
				if prev, dup := owner[kk]; dup && prev != name {
					blame := name
					if _, ok := overrides[name]; !ok {
						blame = prev
					}
					return optionErr("keys."+blame, "key %q is bound to both %q and %q", kk, prev, name)
				}
				owner[kk] = name
			}
		}
	}
	return nil
}

// keyLabel is how a binding's keys are shown: the first n, prettified.
func keyLabel(b key.Binding, n int) string {
	keys := b.Keys()
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = prettyKey(k)
	}
	return strings.Join(out, "/")
}

func prettyKey(k string) string {
	switch k {
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case "pgup":
		return "PgUp"
	case "pgdown":
		return "PgDn"
	}
	return k
}
