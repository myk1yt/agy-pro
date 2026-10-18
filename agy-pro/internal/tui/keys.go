package tui

import "strings"

// Key is one input key for the setup TUI. It generalizes the settings panel
// key enum (same string values) with the Phase 3 additions from the key map:
// Home/End/Delete/Space/Ctrl+S/Help plus a printable-rune carrier (KeyRune)
// that carries one text rune and is consumed only in EDIT mode. The Windows
// console mapper that produces these values is a later unit (P4); everything
// here is platform-neutral.
type Key string

const (
	KeyUp        Key = "up"
	KeyDown      Key = "down"
	KeyLeft      Key = "left"
	KeyRight     Key = "right"
	KeyPgUp      Key = "pgup"
	KeyPgDn      Key = "pgdn"
	KeyHome      Key = "home"
	KeyEnd       Key = "end"
	KeyTab       Key = "tab"
	KeyEsc       Key = "esc"
	KeyEnter     Key = "enter"
	KeyBackspace Key = "backspace"
	KeyDelete    Key = "delete"
	KeyYes       Key = "y"
	KeyNo        Key = "n"
	KeySpace     Key = "space"
	KeyCtrlC     Key = "ctrl-c"
	KeyCtrlL     Key = "ctrl-l"
	KeyCtrlS     Key = "ctrl-s"
	KeyHelp      Key = "help"
)

// runePrefix marks Key values that carry one printable rune (the EDIT-mode
// text carrier). Values are constructed only through RuneKey and decoded
// only through RuneOf, so the prefix never collides with the fixed enum
// strings above.
const runePrefix = "rune:"

// RuneKey wraps one printable rune in a Key so the dispatcher can deliver
// text in EDIT mode without a parallel channel. RTL text rides through as
// opaque runes (no bidi cursor promises, honest scope).
func RuneKey(r rune) Key { return Key(runePrefix + string(r)) }

// RuneOf decodes a KeyRune carrier back to its rune. It reports false for
// every fixed enum value.
func RuneOf(k Key) (rune, bool) {
	s := string(k)
	if !strings.HasPrefix(s, runePrefix) {
		return 0, false
	}
	rs := []rune(s[len(runePrefix):])
	if len(rs) != 1 {
		return 0, false
	}
	return rs[0], true
}

// Digit returns the digit key for a 1-based choice number, or "" when n is
// outside 0..9 (same convention as the settings panel: numbered selection
// is the single CHOICES language).
func Digit(n int) Key {
	if n < 0 || n > 9 {
		return ""
	}
	return Key(rune('0' + n))
}

// digitIndex maps a digit key to its 0-based choice index (digits 1..9).
func digitIndex(k Key) (int, bool) {
	if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
		return int(k[0] - '1'), true
	}
	return 0, false
}

// EditRune resolves the printable rune a key carries in EDIT mode: the
// KeyRune carrier plus every key whose NAV meaning is shadowed by text
// entry (digits 1-9 and 0, lowercase y/n, space). This helper is the
// mode-scoped half of the dispatcher: in NAV a list consumes digits via
// digitIndex and y/n via the confirm gate, while in EDIT the form and the
// editor receive them as text through EditRune.
func EditRune(k Key) (rune, bool) {
	if r, ok := RuneOf(k); ok {
		return r, true
	}
	// Key("0") is the console/line-mode digit carrier for 0. digitIndex
	// intentionally excludes it (0 is not a NAV list selector), so EDIT
	// decodes it here; digits 1-9 resolve through digitIndex below.
	if k == Key("0") {
		return '0', true
	}
	if idx, ok := digitIndex(k); ok {
		return rune('1' + idx), true
	}
	switch k {
	case KeyYes:
		return 'y', true
	case KeyNo:
		return 'n', true
	case KeySpace:
		return ' ', true
	}
	return 0, false
}
