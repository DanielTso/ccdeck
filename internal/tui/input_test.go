package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func typed(t *testing.T, in textInput, keys ...string) textInput {
	t.Helper()
	for _, k := range keys {
		in, _ = in.Update(inputKey(k))
	}
	return in
}

// inputKey builds a key press the way Bubble Tea reports it: named keys
// by code, modifiers as Mod, printable keys with their text.
func inputKey(name string) tea.KeyPressMsg {
	named := map[string]rune{
		"backspace": tea.KeyBackspace, "delete": tea.KeyDelete, "left": tea.KeyLeft, "right": tea.KeyRight,
		"home": tea.KeyHome, "end": tea.KeyEnd, "space": tea.KeySpace,
	}
	var mod tea.KeyMod
	for {
		switch {
		case strings.HasPrefix(name, "ctrl+"):
			mod, name = mod|tea.ModCtrl, name[len("ctrl+"):]
			continue
		case strings.HasPrefix(name, "alt+"):
			mod, name = mod|tea.ModAlt, name[len("alt+"):]
			continue
		}
		break
	}
	if code, ok := named[name]; ok {
		k := tea.KeyPressMsg{Code: code, Mod: mod}
		if code == tea.KeySpace && mod == 0 {
			k.Text = " "
		}
		return k
	}
	r := []rune(name)[0]
	if mod != 0 {
		return tea.KeyPressMsg{Code: r, Mod: mod}
	}
	return tea.KeyPressMsg{Code: r, Text: name}
}

func TestTextInputEditing(t *testing.T) {
	in := newTextInput()
	if in2, _ := in.Update(keyPress("a")); in2.Value() != "" {
		t.Error("an unfocused input took a key")
	}
	in.Focus()
	tests := []struct {
		keys []string
		want string
		pos  int
	}{
		{[]string{"h", "e", "l", "l", "o"}, "hello", 5},
		{[]string{"h", "i", "space", "y", "o", "u"}, "hi you", 6},
		{[]string{"a", "b", "c", "backspace"}, "ab", 2},
		{[]string{"a", "b", "c", "left", "left", "x"}, "axbc", 2},
		{[]string{"a", "b", "c", "home", "delete"}, "bc", 0},
		{[]string{"a", "b", "home", "end", "c"}, "abc", 3},
		{[]string{"o", "n", "e", "space", "t", "w", "o", "ctrl+w"}, "one ", 4},
		{[]string{"o", "n", "e", "space", "t", "w", "o", "alt+backspace", "alt+backspace"}, "", 0},
		{[]string{"o", "n", "e", "space", "t", "w", "o", "alt+left", "ctrl+k"}, "one ", 4},
		{[]string{"o", "n", "e", "space", "t", "w", "o", "alt+left", "ctrl+u"}, "two", 0},
		{[]string{"o", "n", "e", "space", "t", "w", "o", "home", "alt+right", "alt+d"}, "one", 3},
		{[]string{"a", "left", "left", "backspace", "right", "right", "delete"}, "a", 1},
		{[]string{"a", "ctrl+x", "b"}, "ab", 2}, // unbound control keys insert nothing
	}
	for _, tt := range tests {
		got := typed(t, in, tt.keys...)
		if got.Value() != tt.want || got.pos != tt.pos {
			t.Errorf("%v: value %q at %d, want %q at %d", tt.keys, got.Value(), got.pos, tt.want, tt.pos)
		}
	}
}

func TestTextInputPaste(t *testing.T) {
	in := newTextInput()
	in.Focus()
	in = typed(t, in, "a", "b", "left")
	in, _ = in.Update(tea.PasteMsg{Content: "x\ny\tz\x1b[31m!\x07"})
	if in.Value() != "ax y z!b" || in.pos != 7 {
		t.Errorf("paste: value %q at %d", in.Value(), in.pos)
	}
	in.Reset()
	if in.Value() != "" || in.pos != 0 {
		t.Errorf("Reset left %q at %d", in.Value(), in.pos)
	}
}

func TestTextInputView(t *testing.T) {
	in := newTextInput()
	in.Prompt, in.Placeholder = "/ ", "filter sessions"
	if got := ansi.Strip(in.View()); got != "/ filter sessions" {
		t.Errorf("blurred placeholder: %q", got)
	}
	in.SetWidth(6)
	if got := ansi.Strip(in.View()); got != "/ filte…" {
		t.Errorf("narrow placeholder: %q", got)
	}
	in.Focus()
	in = typed(t, in, "a", "b", "c")
	if got := ansi.Strip(in.View()); got != "/ abc " {
		t.Errorf("view = %q, want the text and a cursor cell", got)
	}
	// Longer than the width: it scrolls so the cursor shows, and never
	// takes more than the width.
	in = typed(t, in, "d", "e", "f", "g", "h", "i")
	if got := ansi.Strip(in.View()); got != "/ efghi " {
		t.Errorf("scrolled view = %q", got)
	}
	in = typed(t, in, "home")
	if got := ansi.Strip(in.View()); got != "/ abcdef" {
		t.Errorf("view at home = %q", got)
	}
	for range 20 {
		in = typed(t, in, "right")
		if w := ansi.StringWidth(in.View()) - 2; w > 6 {
			t.Fatalf("view %q is %d cells wide, want at most 6", ansi.Strip(in.View()), w)
		}
	}
	// Wide characters count by cells.
	wide := newTextInput()
	wide.SetWidth(5)
	wide.Focus()
	wide, _ = wide.Update(tea.PasteMsg{Content: "認証フロー"})
	if w := ansi.StringWidth(wide.View()); w > 5 {
		t.Errorf("wide view %q is %d cells", ansi.Strip(wide.View()), w)
	}
}
