package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// textInput is a one-line text field: a prompt, the text with a cursor,
// and a placeholder while it is empty. It stands in for bubbles'
// textinput, whose clipboard package searches PATH for clipboard tools
// when any ccdeck process starts. On WSL, where PATH holds the Windows
// directories, that search alone took half a second, in the dashboard and
// in the status-line helper alike. Paste arrives as the terminal's
// bracketed paste (tea.PasteMsg) instead.
type textInput struct {
	Prompt      string
	Placeholder string

	value   []rune
	pos     int // cursor, in runes
	offset  int // first rune shown, when the text is wider than width
	width   int // cells for the text; 0 means unlimited
	focused bool
	st      textInputStyles
}

// textInputStyles are the styles of a textInput.
type textInputStyles struct {
	Prompt, Text, Placeholder, Cursor lipgloss.Style
}

func newTextInput() textInput { return textInput{} }

// Focus shows the cursor and lets Update edit the text.
func (t *textInput) Focus() tea.Cmd {
	t.focused = true
	return nil
}

// Blur hides the cursor; Update then ignores keys.
func (t *textInput) Blur() { t.focused = false }

// Reset empties the text.
func (t *textInput) Reset() { t.value, t.pos, t.offset = nil, 0, 0 }

// Value is the text.
func (t *textInput) Value() string { return string(t.value) }

// SetWidth sets how many cells the text gets, the prompt not included.
func (t *textInput) SetWidth(w int) { t.width = max(w, 0) }

// SetStyles sets the styles.
func (t *textInput) SetStyles(s textInputStyles) { t.st = s }

// Update edits the text on a key press or a paste while focused.
func (t textInput) Update(msg tea.Msg) (textInput, tea.Cmd) {
	if !t.focused {
		return t, nil
	}
	switch msg := msg.(type) {
	case tea.PasteMsg:
		t.insert(msg.Content)
	case tea.KeyPressMsg:
		t.key(msg)
	}
	return t, nil
}

func (t *textInput) key(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "backspace", "ctrl+h":
		if t.pos > 0 {
			t.cut(t.pos-1, t.pos)
		}
	case "delete", "ctrl+d":
		if t.pos < len(t.value) {
			t.cut(t.pos, t.pos+1)
		}
	case "left", "ctrl+b":
		t.pos = max(t.pos-1, 0)
	case "right", "ctrl+f":
		t.pos = min(t.pos+1, len(t.value))
	case "home", "ctrl+a":
		t.pos = 0
	case "end", "ctrl+e":
		t.pos = len(t.value)
	case "alt+left", "ctrl+left", "alt+b":
		t.pos = t.wordStart()
	case "alt+right", "ctrl+right", "alt+f":
		t.pos = t.wordEnd()
	case "ctrl+w", "alt+backspace":
		t.cut(t.wordStart(), t.pos)
	case "alt+delete", "alt+d":
		t.cut(t.pos, t.wordEnd())
	case "ctrl+u":
		t.cut(0, t.pos)
	case "ctrl+k":
		t.cut(t.pos, len(t.value))
	default:
		if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) == 0 {
			t.insert(msg.Text)
		}
	}
}

// insert puts s at the cursor as one line: line breaks and tabs become
// spaces, other control characters and escape sequences are dropped.
func (t *textInput) insert(s string) {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	r := []rune(sanitize(s))
	if len(r) == 0 {
		return
	}
	t.value = append(t.value[:t.pos], append(r, t.value[t.pos:]...)...)
	t.pos += len(r)
}

// cut removes runes [from, to).
func (t *textInput) cut(from, to int) {
	if from >= to {
		return
	}
	t.value = append(t.value[:from], t.value[to:]...)
	t.pos = from
}

// wordStart is the start of the word before the cursor; wordEnd the end of
// the word after it.
func (t *textInput) wordStart() int {
	i := t.pos
	for i > 0 && t.value[i-1] == ' ' {
		i--
	}
	for i > 0 && t.value[i-1] != ' ' {
		i--
	}
	return i
}

func (t *textInput) wordEnd() int {
	i := t.pos
	for i < len(t.value) && t.value[i] == ' ' {
		i++
	}
	for i < len(t.value) && t.value[i] != ' ' {
		i++
	}
	return i
}

// View is the prompt and the text, scrolled so the cursor shows, or the
// placeholder while the text is empty.
func (t *textInput) View() string {
	prompt := t.st.Prompt.Render(t.Prompt)
	if len(t.value) == 0 {
		ph := []rune(t.Placeholder)
		if t.width > 0 {
			ph = []rune(truncate(t.Placeholder, t.width))
		}
		if !t.focused {
			return prompt + t.st.Placeholder.Render(string(ph))
		}
		if len(ph) == 0 {
			return prompt + t.st.Cursor.Render(" ")
		}
		return prompt + t.st.Cursor.Render(string(ph[0])) + t.st.Placeholder.Render(string(ph[1:]))
	}
	t.scroll()
	end := len(t.value)
	if t.width > 0 {
		// scroll keeps the text before the cursor, and the cursor's cell,
		// within width; the rest shows as far as it fits.
		end = t.offset
		for w := 0; end < len(t.value); end++ {
			rw := width(string(t.value[end]))
			if w+rw > t.width && end > t.pos {
				break
			}
			w += rw
		}
	}
	shown := t.value[t.offset:end]
	if !t.focused {
		return prompt + t.st.Text.Render(string(shown))
	}
	at := t.pos - t.offset
	before := t.st.Text.Render(string(shown[:at]))
	if at >= len(shown) {
		return prompt + before + t.st.Cursor.Render(" ")
	}
	return prompt + before + t.st.Cursor.Render(string(shown[at])) + t.st.Text.Render(string(shown[at+1:]))
}

// scroll moves offset so that the cursor, and the cell it needs, fit in
// width.
func (t *textInput) scroll() {
	t.offset = min(t.offset, t.pos)
	if t.width <= 0 {
		t.offset = 0
		return
	}
	for t.offset < t.pos && width(string(t.value[t.offset:t.pos]))+1 > t.width {
		t.offset++
	}
}
