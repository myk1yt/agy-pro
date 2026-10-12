//go:build windows

package settings

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Raw console key input through kernel32 only (stdlib syscall, no third-party
// packages) — a U6-local copy of the internal/launcher console_windows.go
// poller pattern (contract §2.9 F-TUI) with the key mapper EXTENDED beyond the
// launcher's 1/2/3+Enter set to arrow/page/esc/tab virtual keys. The
// peek-then-consume ordering is preserved: the poller reports sightings and
// records are consumed only once the caller accepts them.

var (
	settingsKernel32                     = syscall.NewLazyDLL("kernel32.dll")
	settingsProcPeekConsoleInputW        = settingsKernel32.NewProc("PeekConsoleInputW")
	settingsProcReadConsoleInputW        = settingsKernel32.NewProc("ReadConsoleInputW")
	settingsProcGetNumberOfConsoleInputE = settingsKernel32.NewProc("GetNumberOfConsoleInputEvents")
)

// inputRecord mirrors Win32 INPUT_RECORD for key events (20 bytes).
type inputRecord struct {
	eventType uint16
	_         uint16
	keyDown   int32
	repeat    uint16
	virtualK  uint16
	scan      uint16
	char      uint16
	state     uint32
}

const (
	keyEventRecordType = 0x0001

	vkBack    = 0x08
	vkTab     = 0x09
	vkReturn  = 0x0d
	vkEscape  = 0x1b
	vkPrior   = 0x21 // PgUp
	vkNext    = 0x22 // PgDn
	vkLeft    = 0x25
	vkUp      = 0x26
	vkRight   = 0x27
	vkDown    = 0x28
	vkShift   = 0x10
	shiftMask = 0x0010
)

// consoleBuffer abstracts the Win32 console input buffer so navigation can be
// race-tested without a live console (contract §2.9 consoleBuffer seam).
type consoleBuffer interface {
	// peek returns up to n pending input records without consuming them.
	peek(n int) ([]inputRecord, bool)
	// consume removes up to n records from the front of the buffer.
	consume(n int) (int, bool)
}

type win32ConsoleBuffer struct{ h uintptr }

func (w win32ConsoleBuffer) peek(n int) ([]inputRecord, bool) {
	if n <= 0 {
		return nil, true
	}
	buf := make([]inputRecord, n)
	var got uint32
	r, _, _ := settingsProcPeekConsoleInputW.Call(
		w.h,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&got)),
	)
	if r == 0 {
		return nil, false
	}
	return buf[:got], true
}

func (w win32ConsoleBuffer) consume(n int) (int, bool) {
	total := 0
	for total < n {
		tmp := make([]inputRecord, n-total)
		var got uint32
		r, _, _ := settingsProcReadConsoleInputW.Call(
			w.h,
			uintptr(unsafe.Pointer(&tmp[0])),
			uintptr(len(tmp)),
			uintptr(unsafe.Pointer(&got)),
		)
		if r == 0 || got == 0 {
			return total, false
		}
		total += int(got)
	}
	return total, true
}

// mapConsoleKey translates one key event to a Key. Extended beyond the
// launcher's digit+Enter set (contract §2.9 F-TUI keybindings).
func mapConsoleKey(char, vk uint16, state uint32) (Key, bool) {
	shift := state&shiftMask != 0
	switch vk {
	case vkUp:
		if shift {
			return KeyPgUp, true
		}
		return KeyUp, true
	case vkDown:
		if shift {
			return KeyPgDn, true
		}
		return KeyDown, true
	case vkLeft:
		return KeyLeft, true
	case vkRight:
		return KeyRight, true
	case vkPrior:
		return KeyPgUp, true
	case vkNext:
		return KeyPgDn, true
	case vkEscape:
		return KeyEsc, true
	case vkTab:
		return KeyTab, true
	case vkReturn:
		return KeyEnter, true
	case vkBack:
		return KeyBackspace, true
	}
	switch char {
	case 0x03:
		return KeyCtrlC, true
	case 0x0c:
		return KeyCtrlL, true
	}
	if char >= '0' && char <= '9' {
		return Key(rune(char)), true
	}
	if char == 'y' || char == 'Y' {
		return KeyYes, true
	}
	if char == 'n' || char == 'N' {
		return KeyNo, true
	}
	return "", false
}

// readKeyFrom runs the peek/decide/consume protocol against any consoleBuffer,
// blocking until one mapped key arrives. Consumption happens only after the
// sighting is accepted (same ordering guarantee as the launcher poller).
func readKeyFrom(in consoleBuffer) (Key, bool) {
	for {
		buf, ok := in.peek(32)
		if !ok {
			return "", false
		}
		if len(buf) == 0 {
			time.Sleep(15 * time.Millisecond)
			continue
		}
		for i := 0; i < len(buf); i++ {
			rec := buf[i]
			if rec.eventType != keyEventRecordType || rec.keyDown == 0 {
				continue
			}
			key, ok := mapConsoleKey(rec.char, rec.virtualK, rec.state)
			if !ok {
				// Unmapped key: drop it with the noise before it and keep polling.
				_, _ = in.consume(i + 1)
				break
			}
			remaining := i + 1
			for remaining > 0 {
				n, ok := in.consume(remaining)
				if !ok || n == 0 {
					break
				}
				remaining -= n
			}
			return key, true
		}
	}
}

// readConsoleKey blocks for one key from a real Windows console handle.
func readConsoleKey(consoleIn *os.File) (Key, bool) {
	if consoleIn == nil {
		return "", false
	}
	h := consoleIn.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return "", false
	}
	var pending uint32
	r, _, _ := settingsProcGetNumberOfConsoleInputE.Call(h, uintptr(unsafe.Pointer(&pending)))
	if r == 0 {
		return "", false
	}
	return readKeyFrom(win32ConsoleBuffer{h: h})
}
