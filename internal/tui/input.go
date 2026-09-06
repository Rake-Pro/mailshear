package tui

import (
	"strings"
	"unicode"
)

// field is a one-line text input. bubbles/textinput would do this, but it
// pulls a clipboard dependency into the build for one binding, so the setup
// form uses these instead: printable runes, backspace, ctrl+u to clear, and
// whole blocks of text from a bracketed paste.
type field struct {
	label string
	value string
	// mask hides the value behind asterisks (the app password).
	mask bool
	// hint is shown in place of an empty value.
	hint string
	// edited records that the user typed here, so detection stops
	// overwriting it.
	edited bool
	// stripSpaces drops spaces as well as control characters, for a Gmail app
	// password: creds.Normalize removes them before the password is used, so
	// keeping them would only make the mask lie about the length.
	stripSpaces bool
}

// insert appends text at the cursor, which is always the end of the value.
// text is one keystroke, a burst of runes from a terminal without bracketed
// paste, or a whole pasted block; control characters are dropped rather than
// stored, so a trailing newline in a paste does not become part of the value.
func (f *field) insert(text string) {
	clean := sanitizeInput(text, f.stripSpaces)
	if clean == "" {
		return
	}
	f.value += clean
	f.edited = true
}

// sanitizeInput keeps the printable runes of typed or pasted text.
func sanitizeInput(text string, stripSpaces bool) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case unicode.IsControl(r):
		case stripSpaces && unicode.IsSpace(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (f *field) backspace() {
	r := []rune(f.value)
	if len(r) == 0 {
		return
	}
	f.value = string(r[:len(r)-1])
	f.edited = true
}

func (f *field) clear() {
	f.value = ""
	f.edited = true
}

// set replaces the value without marking the field as user-edited, for
// values filled in by provider detection.
func (f *field) set(v string) { f.value = v }

// render draws the value, masked when it is a password, with a block cursor
// when focused and the hint when it is empty and unfocused.
func (f field) render(s styles, focused bool, width int) string {
	shown := f.value
	if f.mask {
		shown = strings.Repeat("*", len([]rune(f.value)))
	}
	if shown == "" && !focused {
		return s.dim.Render(pad(f.hint, width))
	}
	if focused {
		return s.prompt.Render(pad(shown+"_", width))
	}
	return pad(shown, width)
}
