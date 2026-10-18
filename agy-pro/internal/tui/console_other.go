//go:build !windows

package tui

import "os"

// Non-Windows surface of the console layer: no raw keys, unknown size,
// plain capability. The exported shape matches console_windows.go so the
// package builds and tests run under every GOOS; callers degrade to the
// P3 line-mode reader (plan v1 entry contract, line-mode degrade).

const rawConsoleKeysSupported = false // compile-time degrade signal

// ConsoleKeys is the inert non-Windows key source. OpenConsoleKeys always
// reports ErrNotConsole so the caller takes the line-mode path.
type ConsoleKeys struct{}

// OpenConsoleKeys reports ErrNotConsole: raw console input is Windows-only.
func OpenConsoleKeys(*os.File) (*ConsoleKeys, error) { return nil, ErrNotConsole }

// ReadKey reports end of input.
func (c *ConsoleKeys) ReadKey() (Key, bool) { return "", false }

// Close is a no-op.
func (c *ConsoleKeys) Close() {}

// SetResizeHook is a no-op (resize records never arrive without a console).
func (c *ConsoleKeys) SetResizeHook(func()) {}

// Resized reports false.
func (c *ConsoleKeys) Resized() bool { return false }

// LatchedSize reports unknown size.
func (c *ConsoleKeys) LatchedSize() (int, int, bool) { return 0, 0, false }

// ConsoleSize reports unknown size on non-Windows hosts.
func ConsoleSize(*os.File) (int, int, bool) { return 0, 0, false }

// defaultProbes makes Detect degrade to TierPlain everywhere on this
// platform: not a console, no VT, no size.
var defaultProbes = probes{
	isConsole: func() bool { return false },
	enableVT:  func() (uint32, error) { return 0, ErrNotConsole },
	setMode:   func(uint32) {},
	size:      func() (int, int, bool) { return 0, 0, false },
	getenv:    os.Getenv,
}
