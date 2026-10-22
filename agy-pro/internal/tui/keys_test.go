package tui

import "testing"

// TestKeyStrings pins the string form of every enum value: the console
// mapper (P4) and the line-mode reader both speak these values, and the
// settings-panel enum must stay compatible for any future migration.
func TestKeyStrings(t *testing.T) {
	cases := []struct {
		key  Key
		want string
	}{
		{KeyUp, "up"},
		{KeyDown, "down"},
		{KeyLeft, "left"},
		{KeyRight, "right"},
		{KeyPgUp, "pgup"},
		{KeyPgDn, "pgdn"},
		{KeyHome, "home"},
		{KeyEnd, "end"},
		{KeyTab, "tab"},
		{KeyEsc, "esc"},
		{KeyEnter, "enter"},
		{KeyBackspace, "backspace"},
		{KeyDelete, "delete"},
		{KeyYes, "y"},
		{KeyNo, "n"},
		{KeySpace, "space"},
		{KeyCtrlC, "ctrl-c"},
		{KeyCtrlL, "ctrl-l"},
		{KeyCtrlS, "ctrl-s"},
	}
	for _, c := range cases {
		if string(c.key) != c.want {
			t.Errorf("key %q: string form %q, want %q", c.want, string(c.key), c.want)
		}
	}
}

func TestDigit(t *testing.T) {
	if got := Digit(1); got != Key("1") {
		t.Errorf("Digit(1) = %q, want %q", got, Key("1"))
	}
	if got := Digit(9); got != Key("9") {
		t.Errorf("Digit(9) = %q, want %q", got, Key("9"))
	}
	if got := Digit(0); got != Key("0") {
		t.Errorf("Digit(0) = %q, want %q", got, Key("0"))
	}
	if got := Digit(10); got != "" {
		t.Errorf("Digit(10) = %q, want empty", got)
	}
	if got := Digit(-1); got != "" {
		t.Errorf("Digit(-1) = %q, want empty", got)
	}
}

func TestDigitIndex(t *testing.T) {
	for n := 1; n <= 9; n++ {
		idx, ok := digitIndex(Digit(n))
		if !ok || idx != n-1 {
			t.Errorf("digitIndex(Digit(%d)) = %d,%v; want %d,true", n, idx, ok, n-1)
		}
	}
	for _, k := range []Key{Key("0"), Key("10"), KeyUp, KeyYes, KeyEsc, ""} {
		if idx, ok := digitIndex(k); ok {
			t.Errorf("digitIndex(%q) = %d,true; want false", k, idx)
		}
	}
}

func TestRuneKeyRoundTrip(t *testing.T) {
	for _, r := range []rune{'a', 'Z', '7', ' ', '한', '文', '😀'} {
		k := RuneKey(r)
		got, ok := RuneOf(k)
		if !ok || got != r {
			t.Errorf("RuneOf(RuneKey(%q)) = %q,%v; want %q,true", r, got, ok, r)
		}
	}
	for _, k := range []Key{KeyUp, KeyEnter, Key("a"), KeyYes, KeyCtrlS} {
		if r, ok := RuneOf(k); ok {
			t.Errorf("RuneOf(%q) = %q,true; want false (fixed enum value)", k, r)
		}
	}
}

// TestEditRune pins the EDIT-mode meaning of the dual keys: digits, y/n,
// and space are text in EDIT even though they select and confirm in NAV.
func TestEditRune(t *testing.T) {
	cases := []struct {
		key  Key
		want rune
		ok   bool
	}{
		{RuneKey('x'), 'x', true},
		{RuneKey('한'), '한', true},
		{RuneKey('Y'), 'Y', true},
		{RuneKey('N'), 'N', true},
		{Digit(3), '3', true},
		{Key("0"), '0', true},
		{KeyYes, 'y', true},
		{KeyNo, 'n', true},
		{KeySpace, ' ', true},
		{KeyUp, 0, false},
		{KeyEnter, 0, false},
		{KeyEsc, 0, false},
		{KeyCtrlC, 0, false},
		{KeyCtrlL, 0, false},
		{KeyCtrlS, 0, false},
		{KeyTab, 0, false},
		{KeyBackspace, 0, false},
		{KeyDelete, 0, false},
		{KeyHome, 0, false},
		{KeyEnd, 0, false},
		{KeyPgUp, 0, false},
		{KeyPgDn, 0, false},
	}
	for _, c := range cases {
		got, ok := EditRune(c.key)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("EditRune(%q) = %q,%v; want %q,%v", c.key, got, ok, c.want, c.ok)
		}
	}
}
