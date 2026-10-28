package settings

// Key is one navigation/selection key for the settings TUI (contract §2.9
// F-TUI keybindings: up/down/pgup/pgdn/left/right/tab/esc/y-n/enter/digits).
type Key string

const (
	KeyUp        Key = "up"
	KeyDown      Key = "down"
	KeyPgUp      Key = "pgup"
	KeyPgDn      Key = "pgdn"
	KeyLeft      Key = "left"
	KeyRight     Key = "right"
	KeyTab       Key = "tab"
	KeyEsc       Key = "esc"
	KeyEnter     Key = "enter"
	KeyBackspace Key = "backspace"
	KeyYes       Key = "y"
	KeyNo        Key = "n"
	KeyCtrlC     Key = "ctrl-c"
	KeyCtrlL     Key = "ctrl-l"
)

// Digit returns the digit key for a 1-based choice number, or "" when n is
// outside 0..9 (numbered selection is the single CHOICES language, §2.7).
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
