//go:build windows

package launcher

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Raw console access through kernel32 only (stdlib syscall, no third-party
// packages). The poller peeks the console input buffer and only reports what
// it sees; records are consumed by the caller after its select has accepted
// the sighting. That ordering is the fix for the consume-vs-timeout race: a
// timeout that wins the race never eats a keystroke the real agy child should
// receive, because nothing is consumed until the decision is final.
var (
	kernel32                          = syscall.NewLazyDLL("kernel32.dll")
	procPeekConsoleInputW             = kernel32.NewProc("PeekConsoleInputW")
	procReadConsoleInputW             = kernel32.NewProc("ReadConsoleInputW")
	procGetNumberOfConsoleInputEvents = kernel32.NewProc("GetNumberOfConsoleInputEvents")
)

// inputRecord mirrors Win32 INPUT_RECORD for key events (20 bytes:
// WORD eventType, WORD pad, KEY_EVENT_RECORD).
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
	vkReturn           = 0x0d
)

// consoleBuffer abstracts the Win32 console input buffer so the
// decision-before-consume ordering can be race-tested without a live console.
type consoleBuffer interface {
	// peek returns up to n pending input records without consuming them.
	peek(n int) ([]inputRecord, bool)
	// consume removes up to n records from the front of the buffer and
	// returns how many were actually removed.
	consume(n int) (int, bool)
}

// win32ConsoleBuffer is the real console input buffer behind a console handle.
type win32ConsoleBuffer struct {
	h uintptr
}

func (w win32ConsoleBuffer) peek(n int) ([]inputRecord, bool) {
	if n <= 0 {
		return nil, true
	}
	buf := make([]inputRecord, n)
	var got uint32
	r, _, _ := procPeekConsoleInputW.Call(
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
		r, _, _ := procReadConsoleInputW.Call(
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

// readConsoleChoice polls the Windows console input buffer for a single
// launcher key. The poller never consumes; consumption happens only after the
// deadline race is decided, so a timeout can never leave a keystroke missing
// from the buffer the real agy child inherits.
func readConsoleChoice(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
	if consoleIn == nil {
		return "", consoleUnavailable
	}
	h := consoleIn.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return "", consoleUnavailable
	}

	var pending uint32
	r, _, _ := procGetNumberOfConsoleInputEvents.Call(h, uintptr(unsafe.Pointer(&pending)))
	if r == 0 {
		return "", consoleUnavailable
	}

	return readConsoleChoiceFrom(win32ConsoleBuffer{h: h}, timeout)
}

// sighting is one launcher key found in the buffer. count is how many leading
// records must be consumed together with it (the match plus the noise before
// it), and is applied only once the caller accepts the sighting.
type sighting struct {
	choice string
	count  int
}

// readConsoleChoiceFrom runs the poll/decide/consume protocol against any
// consoleBuffer. The poller only peeks and reports; consume is called
// exclusively on the branch where the sighting beat the deadline.
func readConsoleChoiceFrom(in consoleBuffer, timeout time.Duration) (string, consoleRead) {
	ch := make(chan sighting, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}

			buf, ok := in.peek(32)
			if !ok || len(buf) == 0 {
				continue
			}

			for i := 0; i < len(buf); i++ {
				rec := buf[i]
				if rec.eventType != keyEventRecordType || rec.keyDown == 0 {
					continue
				}
				choice, ok := mapConsoleKey(rec.char, rec.virtualK)
				if !ok {
					continue
				}
				// Report without consuming: if the deadline wins the select
				// below, these records stay in the buffer for the child.
				select {
				case ch <- sighting{choice: choice, count: i + 1}:
				case <-done:
				}
				return
			}
		}
	}()

	select {
	case s := <-ch:
		// Decision won: only now is the matched keystroke consumed, so it is
		// never replayed into the real agy child process.
		remaining := s.count
		for remaining > 0 {
			n, ok := in.consume(remaining)
			if !ok || n == 0 {
				break
			}
			remaining -= n
		}
		drainTrailingEnterFrom(in, 150*time.Millisecond)
		return s.choice, consoleChoice
	case <-time.After(timeout):
		// Timeout won: the poller consumed nothing, so any keystroke that
		// arrived on this tick is still queued for the real agy child.
		return "", consoleTimeout
	}
}

// drainTrailingEnterFrom consumes Enter keystrokes that are already queued, or
// that arrive within settle (users habitually type a digit then Enter). Left
// in the buffer they would submit an empty line to the next reader (the swarm
// prompt), cancelling a deliberate [3] choice. The first real non-Enter key
// stops the drain so eagerly typed prompt text still reaches the line reader;
// non-key console noise (focus, mouse) is discarded along the way.
func drainTrailingEnterFrom(in consoleBuffer, settle time.Duration) {
	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		buf, ok := in.peek(32)
		if !ok || len(buf) == 0 {
			time.Sleep(15 * time.Millisecond)
			continue
		}
		stop := false
		for i := 0; i < len(buf); i++ {
			rec := buf[i]
			if rec.eventType == keyEventRecordType && rec.keyDown != 0 {
				isEnter := rec.char == '\r' || rec.char == '\n' || rec.virtualK == vkReturn
				if !isEnter {
					stop = true
					break
				}
			}
			if _, ok := in.consume(1); !ok {
				return
			}
		}
		if stop {
			return
		}
	}
}

// consoleLineSighting is one completed selection line from the console. count
// is how many leading records must be consumed together with it (the accepted
// keys, the terminating Enter, and any noise in front of them); it is applied
// only once the caller accepts the line.
type consoleLineSighting struct {
	line  string
	count int
}

// isTextKey reports whether a console key event carries text. The reader
// accumulates every text key - not just the selection alphabet - so the
// console path sees exactly what the relay path sees: a stray character stays
// in the line and the picker answers with the grammar instead of silently
// dropping it (and a dot-decorated secret stays one token for the seeded
// masker, U8). Non-ASCII is included: the reader is also the [3] free-text
// swarm prompt path, and dropping Korean/CJK text would cancel a Korean
// prompt as "empty". C0 controls and DEL are excluded (Tab, Esc, Ctrl keys
// are editing keys, not text).
func isTextKey(char uint16) bool {
	return char >= 0x20 && char != 0x7f
}

// appendText folds one key event's character into the line. repeat is the
// console's typematic count (a held key queues ONE record with repeat=N), so
// the character is appended repeat times; ignoring it made a held "1" for
// seat "11" silently pick seat 1. High surrogates are held until their low
// half arrives so non-BMP text (emoji, rare CJK) survives instead of
// degrading to U+FFFD.
func appendText(acc []rune, char uint16, repeat int) []rune {
	if repeat < 1 {
		repeat = 1
	}
	if char >= 0xd800 && char <= 0xdbff { // high surrogate: hold it (once)
		acc = append(acc, rune(char))
		return acc
	}
	if char >= 0xdc00 && char <= 0xdfff { // low surrogate: pair with a held high
		if n := len(acc); n > 0 && acc[n-1] >= 0xd800 && acc[n-1] <= 0xdbff {
			r := rune(acc[n-1]&0x3ff)<<10 | rune(char&0x3ff)
			acc[n-1] = r + 0x10000
			return acc
		}
		char = 0xfffd // lone low surrogate
	}
	for i := 0; i < repeat; i++ {
		acc = append(acc, rune(char))
	}
	return acc
}

// readConsoleLine polls the console input buffer for one full selection line
// (multi-digit capable) and reports it when Enter arrives. Backspace edits the
// line; every other key is ignored but still consumed with the line. The
// poll/decide/consume protocol matches readConsoleChoiceFrom: nothing is
// consumed until the line is complete, so a deadline can never eat a keystroke
// the real agy child should receive.
func readConsoleLine(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
	if consoleIn == nil {
		return "", consoleUnavailable
	}
	h := consoleIn.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return "", consoleUnavailable
	}

	var pending uint32
	r, _, _ := procGetNumberOfConsoleInputEvents.Call(h, uintptr(unsafe.Pointer(&pending)))
	if r == 0 {
		return "", consoleUnavailable
	}

	return readConsoleLineFrom(win32ConsoleBuffer{h: h}, timeout)
}

// readConsoleLineFrom runs the poll/decide/consume line protocol against any
// consoleBuffer so tests can drive it without a live console.
//
// The poller never consumes, so a record that is already queued keeps being
// returned by every later peek. scanned is the watermark of records already
// folded into acc: without it a character typed across several 15ms ticks is
// appended once per tick ("1" typed over 100ms would arrive as "1111111").
// Nothing is consumed until Enter decides the race, and a timeout consumes
// nothing at all, so the child never loses or sees a duplicated keystroke.
func readConsoleLineFrom(in consoleBuffer, timeout time.Duration) (string, consoleRead) {
	ch := make(chan consoleLineSighting, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		var acc []rune
		scanned := 0 // records before this index are already in acc
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}

			// A wide window: with key-up records riding along, a long
			// selection ("1,3-5,8,11-14") can exceed 32 queued records, and
			// an Enter beyond the window would never be sighted.
			buf, ok := in.peek(1024)
			if !ok || len(buf) == 0 {
				continue
			}
			if len(buf) < scanned {
				// The buffer shrank under us (should not happen while
				// nothing is consumed); rescan from the front.
				scanned = 0
				acc = nil
			}

			for i := scanned; i < len(buf); i++ {
				rec := buf[i]
				if rec.eventType != keyEventRecordType || rec.keyDown == 0 {
					continue
				}
				if rec.char == '\r' || rec.char == '\n' || rec.virtualK == vkReturn {
					select {
					case ch <- consoleLineSighting{line: string(acc), count: i + 1}:
					case <-done:
					}
					return
				}
				if rec.virtualK == 0x08 { // VK_BACK edits the line (repeat-aware)
					back := int(rec.repeat)
					if back < 1 {
						back = 1
					}
					for i := 0; i < back && len(acc) > 0; i++ {
						acc = acc[:len(acc)-1]
					}
					continue
				}
				if isTextKey(rec.char) {
					acc = appendText(acc, rec.char, int(rec.repeat))
				}
			}
			scanned = len(buf)
		}
	}()

	select {
	case s := <-ch:
		// Decision won: only now are the line's records consumed, so they are
		// never replayed into the real agy child process.
		remaining := s.count
		for remaining > 0 {
			n, ok := in.consume(remaining)
			if !ok || n == 0 {
				break
			}
			remaining -= n
		}
		drainTrailingEnterFrom(in, 150*time.Millisecond)
		return s.line, consoleChoice
	case <-time.After(timeout):
		// Timeout won: nothing was consumed, so every keystroke typed so far
		// stays queued for the real agy child.
		return "", consoleTimeout
	}
}
