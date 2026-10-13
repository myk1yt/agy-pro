//go:build windows

package providers

import (
	"os"
	"syscall"
	"unsafe"
)

// Raw-console secret input (U8, contract §2.11). Echo is disabled at the
// console via kernel32 SetConsoleMode and re-rendered locally through
// secretEcho, so the user sees the first 5 runes and "*" for everything after
// while typing. The raw value is returned to the caller and never written to
// any output stream. kernel32 via stdlib syscall only — no third-party deps.

var (
	kernel32Secret      = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode  = kernel32Secret.NewProc("GetConsoleMode")
	procSetConsoleMode  = kernel32Secret.NewProc("SetConsoleMode")
	procReadConsoleInW  = kernel32Secret.NewProc("ReadConsoleInputW")
	secretInputRecType  = uint16(0x0001) // KEY_EVENT
	enableEchoInput     = uint32(0x0004)
	enableLineInput     = uint32(0x0002)
	enableQuickEdit     = uint32(0x0040)
	enableExtendedFlags = uint32(0x0080)
)

// secretInputRecord mirrors Win32 INPUT_RECORD for key events (20 bytes).
type secretInputRecord struct {
	eventType uint16
	_         uint16
	keyDown   int32
	repeat    uint16
	virtualK  uint16
	scan      uint16
	char      uint16
	state     uint32
}

// readConsoleSecret reads one secret line from a Windows console with masked
// echo. Returns errNotConsole when f is not a usable console, in which case the
// caller falls back to an ordinary line read (pipes and scripted tests have no
// console echo to mask). Returns errSecretInterrupted on Ctrl+C (B6).
func readConsoleSecret(f *os.File, echo *secretEcho) (string, error) {
	if f == nil || echo == nil {
		return "", errNotConsole
	}
	h := f.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return "", errNotConsole
	}
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return "", errNotConsole
	}
	// Echo and line input off so every keystroke is visible to us and not
	// auto-echoed by the console. Keep the original mode for restore.
	newMode := mode &^ enableEchoInput &^ enableLineInput
	// Quick-edit would let a mouse selection freeze the read; drop it while
	// secret input is active and restore afterwards.
	newMode |= enableExtendedFlags
	newMode &^= enableQuickEdit
	if r, _, _ := procSetConsoleMode.Call(h, uintptr(newMode)); r == 0 {
		return "", errNotConsole
	}
	defer procSetConsoleMode.Call(h, uintptr(mode))

	var typed []rune
	for {
		var rec secretInputRecord
		var got uint32
		r, _, _ := procReadConsoleInW.Call(
			h,
			uintptr(unsafe.Pointer(&rec)),
			1,
			uintptr(unsafe.Pointer(&got)),
		)
		if r == 0 || got == 0 {
			break
		}
		if rec.eventType != secretInputRecType || rec.keyDown == 0 {
			continue
		}
		switch rec.char {
		case 0x0d, 0x0a: // Enter
			echo.newline()
			return string(typed), nil
		case 0x08: // Backspace
			if n := len(typed); n > 0 {
				typed = typed[:n-1]
				echo.backspace()
			}
		case 0x03: // Ctrl+C: abort secret entry (B6) — never fall through to
			// the empty-value default path that silently keeps the old key.
			echo.newline()
			return "", errSecretInterrupted
		case 0x1b, 0x09: // Esc/Tab are not part of a key; ignore
			continue
		default:
			if rec.char < 0x20 {
				continue
			}
			typed = append(typed, rune(rec.char))
			echo.add(rune(rec.char))
		}
	}
	echo.newline()
	return string(typed), nil
}
