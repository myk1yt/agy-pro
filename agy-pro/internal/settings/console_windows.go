//go:build windows

package settings

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Raw console key input through kernel32 only (stdlib syscall, no third-party
// packages). A U6-local copy of the internal/launcher console_windows.go
// poller pattern (contract §2.9 F-TUI) with the key mapper EXTENDED beyond the
// launcher's 1/2/3+Enter set to arrow/page/esc/tab virtual keys. The
// peek-then-consume ordering is preserved: the poller reports sightings and
// records are consumed only once the caller accepts them (readKeyFrom also
// drains a full window of pure noise, waits out a partial one with idleBackoff,
// and fails the read when the buffer cannot consume).

// rawConsoleKeysSupported reports that this build owns a raw-console key
// reader (B7): an interactive TTY takes the raw-console path here.
const rawConsoleKeysSupported = true

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

// Peek and wait policy for readKeyFrom: peekWindow is how many input records
// one peek inspects, and idleBackoff is the wait between peeks when the buffer
// holds nothing usable yet (an empty buffer, or a partial window of pure
// noise). idleBackoff is the same 15 ms backoff the internal/tui reader uses
// on its no-key path; without it the reader busy-spins and pegs a core while
// the panel idles.
const (
	peekWindow  = 32
	idleBackoff = 15 * time.Millisecond
)

// readKeyFrom runs the peek/decide/consume protocol against any consoleBuffer,
// blocking until one mapped key arrives. Consumption happens only after the
// sighting is accepted (same ordering guarantee as the launcher poller), with
// three carve-outs.
//
// A full window of pure noise is drained, bounded to one peek window per
// iteration: the console input queue is FIFO and its head advances only on
// consumption, so waiting would keep every later key hidden behind that
// window forever. Records are atomic and FIFO, so draining noise can never
// drop a key.
//
// A partial window of pure noise hides no key, so it stays queued (the noise
// still rides with the next accepted sighting) and the wait uses idleBackoff
// instead of a tight re-peek loop that pegs a core while the panel idles.
//
// A consume that cannot move records fails the read: re-peeking the same
// records would spin forever, and reporting a sighting whose record is still
// queued would deliver the same key twice.
func readKeyFrom(in consoleBuffer) (Key, bool) {
	for {
		buf, ok := in.peek(peekWindow)
		if !ok {
			return "", false
		}
		if len(buf) == 0 {
			time.Sleep(idleBackoff)
			continue
		}
		pureNoise := true
		for i := 0; i < len(buf); i++ {
			rec := buf[i]
			if rec.eventType == keyEventRecordType && rec.keyDown != 0 {
				pureNoise = false
				break
			}
		}
		if pureNoise {
			if len(buf) < peekWindow {
				// Partial window of pure noise (mouse/focus/menu events,
				// key-ups). It cannot hide a key and must stay queued so the
				// noise still rides with the next accepted sighting, so wait
				// idleBackoff instead of re-peeking the same records in a
				// tight loop that pegs a core while the panel idles.
				time.Sleep(idleBackoff)
				continue
			}
			// A full window with no key-down record at all is the same noise,
			// but it hides every key queued behind it, so the FIFO head only
			// advances if the window is drained. Bounded to one peek window
			// per iteration so key latency stays low.
			remaining := len(buf)
			for remaining > 0 {
				n, ok := in.consume(remaining)
				if !ok || n == 0 {
					return "", false
				}
				remaining -= n
			}
			continue
		}
		for i := 0; i < len(buf); i++ {
			rec := buf[i]
			if rec.eventType != keyEventRecordType || rec.keyDown == 0 {
				continue
			}
			key, ok := mapConsoleKey(rec.char, rec.virtualK, rec.state)
			if !ok {
				// Unmapped key: drop it with the noise before it and keep
				// polling. A consume that moves nothing must fail the read;
				// ignoring it would re-peek the same records forever.
				n, ok := in.consume(i + 1)
				if !ok || n == 0 {
					return "", false
				}
				break
			}
			remaining := i + 1
			for remaining > 0 {
				n, ok := in.consume(remaining)
				if !ok || n == 0 {
					// A sighting may be reported only once its record left
					// the queue: a partial consume leaves it queued and the
					// next read would deliver the same key twice.
					return "", false
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
