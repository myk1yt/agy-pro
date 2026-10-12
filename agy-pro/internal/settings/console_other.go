//go:build !windows

package settings

import "os"

// readConsoleKey has no raw-console implementation outside Windows. The TUI
// degrades to the line-mode fallback (same Core verbs, `--show` snapshot) on
// non-Windows consoles; glyph parity there stays UNVERIFIED (D3).
func readConsoleKey(consoleIn *os.File) (Key, bool) {
	return "", false
}
