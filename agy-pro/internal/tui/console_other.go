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

// StdinIsConsole reports false on every non-Windows host, and false is the
// honest answer rather than a defensive default: the raw-console TUI path this
// probe gates is Windows-only. OpenConsoleKeys always answers ErrNotConsole
// here (rawConsoleKeysSupported is false) and Detect returns TierPlain with
// SuppressBanner set, so the setup entry already degrades to the line wizard on
// the stdout signal alone. Answering true would claim a console key source this
// platform does not have.
func StdinIsConsole() bool { return false }

// ReadKey reports end of input. No key can be delivered without a console, but
// the read still goes through the pacing funnel (consoleDelivered), so this
// surface stays wired to the arm-delay seam exactly like console_windows.go.
func (c *ConsoleKeys) ReadKey() (Key, bool) { return consoleDelivered("", false) }

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

// SetConsoleOutputRestore is a no-op: there is no console control handler to
// arm and no console mode to restore without a console. The export exists so
// platform-neutral callers keep compiling under every GOOS.
func SetConsoleOutputRestore(uintptr, uint32) {}

// defaultProbes makes Detect degrade to TierPlain everywhere on this
// platform: not a console, no VT, no size.
var defaultProbes = probes{
	isConsole: func() bool { return false },
	enableVT:  func() (uint32, error) { return 0, ErrNotConsole },
	setMode:   func(uint32) {},
	size:      func() (int, int, bool) { return 0, 0, false },
	getenv:    os.Getenv,
}
