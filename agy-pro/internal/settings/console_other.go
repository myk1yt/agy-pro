//go:build !windows

package settings

import "os"

// rawConsoleKeysSupported reports that this build has no raw-console key
// reader (B7): RunTUI must degrade to the line-mode UI even on an interactive
// TTY, because readConsoleKey below returns no keys and the TUI would render
// one frame and exit.
const rawConsoleKeysSupported = false

// readConsoleKey has no raw-console implementation outside Windows. The TUI
// degrades to the line-mode fallback (same Core verbs, `--show` snapshot) on
// non-Windows consoles; glyph parity there stays UNVERIFIED (D3).
func readConsoleKey(consoleIn *os.File) (Key, bool) {
	return "", false
}
