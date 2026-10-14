//go:build !windows

package launcher

import (
	"os"
	"time"
)

// readConsoleChoice has no raw-console implementation outside Windows.
// The caller falls back to line input with the same deadline.
func readConsoleChoice(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
	return "", consoleUnavailable
}

// readConsoleLine has no raw-console implementation outside Windows. The
// picker falls back to the shared line source with the same deadline.
func readConsoleLine(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
	return "", consoleUnavailable
}
