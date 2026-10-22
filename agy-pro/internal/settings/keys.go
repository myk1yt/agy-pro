package settings

// Key is one navigation/selection key for the settings TUI (contract §2.9
// F-TUI keybindings: up/down/pgup/pgdn/left/right/tab/esc/y-n/enter/digits).
// The panel only navigates and selects, so there is no text-editing key here:
// input fields collect a whole line through LineReader, and the raw console
// reader drops the keys this set does not claim (SESSION6 P6-2 deleted the
// backspace entry, which no Step arm ever consumed).
type Key string

const (
	KeyUp    Key = "up"
	KeyDown  Key = "down"
	KeyPgUp  Key = "pgup"
	KeyPgDn  Key = "pgdn"
	KeyLeft  Key = "left"
	KeyRight Key = "right"
	KeyTab   Key = "tab"
	KeyEsc   Key = "esc"
	KeyEnter Key = "enter"
	KeyYes   Key = "y"
	KeyNo    Key = "n"
	KeyCtrlC Key = "ctrl-c"
	KeyCtrlL Key = "ctrl-l"
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
