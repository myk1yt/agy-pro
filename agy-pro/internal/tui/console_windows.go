//go:build windows

package tui

import (
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Raw console access for the setup TUI through kernel32 only (stdlib
// syscall, no third-party packages). This file consolidates the proven
// patterns from internal/settings/console_windows.go and
// internal/launcher/console_windows.go: the peek-then-consume ordering
// (records are consumed only once a sighting is accepted), typematic repeat
// expansion (one record with repeat=N becomes N keys), surrogate-pair
// folding for non-BMP text, and WINDOW_BUFFER_SIZE_EVENT latching. It adds
// the extended key map (Home/End/Delete/Space/Ctrl+S plus the KeyRune
// carrier for printable text), the console mode takeover on the input
// handle, the console size query, the VT output enable that termcap.go's
// detection ladder probes, and the platform's default capability probes.

var (
	tuiKernel32                          = syscall.NewLazyDLL("kernel32.dll")
	tuiProcPeekConsoleInputW             = tuiKernel32.NewProc("PeekConsoleInputW")
	tuiProcReadConsoleInputW             = tuiKernel32.NewProc("ReadConsoleInputW")
	tuiProcGetNumberOfConsoleInputEvents = tuiKernel32.NewProc("GetNumberOfConsoleInputEvents")
	tuiProcGetConsoleMode                = tuiKernel32.NewProc("GetConsoleMode")
	tuiProcSetConsoleMode                = tuiKernel32.NewProc("SetConsoleMode")
	tuiProcGetConsoleScreenBufferInfo    = tuiKernel32.NewProc("GetConsoleScreenBufferInfo")
	tuiProcSetConsoleCtrlHandler         = tuiKernel32.NewProc("SetConsoleCtrlHandler")
)

const (
	rawConsoleKeysSupported = true // compile-time degrade signal for callers

	keyEventRecordType        = 0x0001 // KEY_EVENT
	windowBufferSizeEventType = 0x0004 // WINDOW_BUFFER_SIZE_EVENT
	enableVirtualTerminalProc = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING (output)
	enableEchoInput           = 0x0004 // ENABLE_ECHO_INPUT (input)
	enableLineInput           = 0x0002 // ENABLE_LINE_INPUT
	enableQuickEdit           = 0x0040 // ENABLE_QUICK_EDIT_MODE
	enableExtendedFlags       = 0x0080 // ENABLE_EXTENDED_FLAGS
)

// Virtual-key codes covered by the key map beyond the control characters.
const (
	vkBack   = 0x08
	vkTab    = 0x09
	vkReturn = 0x0d
	vkEscape = 0x1b
	vkPrior  = 0x21 // PgUp
	vkNext   = 0x22 // PgDn
	vkEnd    = 0x23
	vkHome   = 0x24
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28
	vkDelete = 0x2e

	shiftMask = 0x0010 // SHIFT_PRESSED
)

// inputRecord mirrors the Win32 INPUT_RECORD layout for key events
// (20 bytes: WORD eventType, WORD pad, KEY_EVENT_RECORD). The pad plus the
// keyDown int32 share the offset where WINDOW_BUFFER_SIZE_EVENT stores its
// dwSize COORD, so windowSize reinterprets the same memory.
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

// windowSize reads the dwSize COORD of a WINDOW_BUFFER_SIZE_EVENT record.
func (r inputRecord) windowSize() (int, int) {
	sz := (*winSize)(unsafe.Pointer(&r.keyDown))
	return int(sz.x), int(sz.y)
}

type winSize struct{ x, y int16 }

// consoleBuffer abstracts the Win32 console input buffer so the
// peek-then-consume protocol can be driven without a live console.
type consoleBuffer interface {
	// peek returns up to n pending input records without consuming them.
	peek(n int) ([]inputRecord, bool)
	// consume removes up to n records from the front of the buffer and
	// returns how many were actually removed.
	consume(n int) (int, bool)
}

// win32ConsoleBuffer is the real console input buffer behind a handle.
type win32ConsoleBuffer struct{ h uintptr }

func (w win32ConsoleBuffer) peek(n int) ([]inputRecord, bool) {
	if n <= 0 {
		return nil, true
	}
	buf := make([]inputRecord, n)
	var got uint32
	r, _, _ := tuiProcPeekConsoleInputW.Call(
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
		r, _, _ := tuiProcReadConsoleInputW.Call(
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

// mapConsoleKey translates one key event to a Key, covering the full P3
// enum: arrows and paging (Shift+Up/Down page, as the settings panel maps
// them), Home/End/Delete, Esc/Tab/Enter/Backspace, Ctrl+C/Ctrl+L/Ctrl+S,
// Space, digits, lowercase y/n (the confirm pair), and every other
// printable rune (0x21-0x7E plus wide BMP characters from the UTF-16 char
// field) as the KeyRune carrier. Uppercase Y/N are never confirm keys, so
// they ride the rune carrier and keep their case in EDIT-mode text.
// Surrogate halves never reach this function; the reader folds pairs first
// (foldKey).
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
	case vkHome:
		return KeyHome, true
	case vkEnd:
		return KeyEnd, true
	case vkDelete:
		return KeyDelete, true
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
	case 0x13:
		return KeyCtrlS, true
	case 0x20:
		return KeySpace, true
	}
	if char >= '0' && char <= '9' {
		return Key(rune(char)), true
	}
	if char == 'y' {
		return KeyYes, true
	}
	if char == 'n' {
		return KeyNo, true
	}
	if char >= 0x21 && char != 0x7f {
		return RuneKey(rune(char)), true
	}
	return "", false
}

// ConsoleKeys is the raw-console KeySource (P4). OpenConsoleKeys takes the
// console input mode over (echo off, line input off, quick-edit off) and
// Close restores it. ReadKey runs the peek-then-consume protocol: records
// are only consumed once a mapped key is accepted, so nothing the caller
// has not seen is lost.
type ConsoleKeys struct {
	in       consoleBuffer
	h        uintptr
	origMode uint32
	closed   bool

	pendingKey Key // typematic repeat expansion
	pendingN   int
	holdHi     uint16 // held high surrogate awaiting its low half

	onResize func() // latched resize notification
	resized  bool
	lastW    int
	lastH    int
}

// OpenConsoleKeys verifies the handle is a console, takes the input mode
// over, and returns the raw key source. ErrNotConsole means the caller must
// degrade to the line-mode reader.
func OpenConsoleKeys(in *os.File) (*ConsoleKeys, error) {
	if in == nil {
		return nil, ErrNotConsole
	}
	h := in.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return nil, ErrNotConsole
	}
	var mode uint32
	if r, _, _ := tuiProcGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return nil, ErrNotConsole
	}
	// Echo and line input off so every keystroke reaches us; quick-edit off
	// so a mouse selection cannot freeze the read. Extended flags stay on.
	newMode := mode &^ enableEchoInput &^ enableLineInput
	newMode |= enableExtendedFlags
	newMode &^= enableQuickEdit
	if r, _, _ := tuiProcSetConsoleMode.Call(h, uintptr(newMode)); r == 0 {
		return nil, ErrNotConsole
	}
	// Ctrl+C exits through the console control handler, not through a
	// buffered key record, so register a handler that restores the saved
	// mode before the process dies (see consoleCtrlHandler).
	registerConsoleCtrlHandler(h, mode)
	return &ConsoleKeys{in: win32ConsoleBuffer{h: h}, h: h, origMode: mode}, nil
}

// Close restores the console input mode and detaches the console control
// handler. Safe to call more than once.
func (c *ConsoleKeys) Close() {
	if c.closed {
		return
	}
	c.closed = true
	if c.h != 0 {
		restoreConsoleInputMode(c.h)
		unregisterConsoleCtrlHandler(c.h)
	}
}

// Console control handler for the Ctrl+C exit path. OpenConsoleKeys leaves
// ENABLE_PROCESSED_INPUT as found, so Ctrl+C/Ctrl+Break reach the process as
// console control events and the default handler terminates it without
// running the app's deferred ck.Close / cap.Restore. Without a handler the
// user's console would keep raw input (echo and line input off) after the
// exit. The handler below restores the saved input mode first and then
// returns false so the default handler still runs and the advertised
// Ctrl+C exit actually exits.
const (
	ctrlCEvent     = 0
	ctrlBreakEvent = 1
	ctrlCloseEvent = 2
)

// consoleCtrlState is package level (not per ConsoleKeys) because the
// handler callback receives no instance pointer. One console per process is
// the supported shape, so a single slot is enough.
var consoleCtrlState struct {
	mu       sync.Mutex
	callback uintptr // syscall.NewCallback handle; 0 = not registered
	h        uintptr // console input handle the saved mode belongs to
	origMode uint32
	restored bool // once-latch: first restore applies, later ones no-op
}

// consoleCtrlHandler is the SetConsoleCtrlHandler callback. The restore is
// idempotent, so the handler thread and the normal Close path can race
// safely. A full end-to-end check (press Ctrl+C inside the setup TUI on a
// real console, confirm the process exits and shell echo still works) is a
// live-console verification and cannot run headlessly.
func consoleCtrlHandler(evt uint32) uintptr {
	switch evt {
	case ctrlCEvent, ctrlBreakEvent, ctrlCloseEvent:
		consoleCtrlState.mu.Lock()
		h := consoleCtrlState.h
		consoleCtrlState.mu.Unlock()
		restoreConsoleInputMode(h)
	}
	// false: the default handler proceeds, so Ctrl+C still exits the app.
	return 0
}

// registerConsoleCtrlHandler installs consoleCtrlHandler and records the
// handle and original mode for the restore path. Returns false when the OS
// refused the registration; the app still runs, only without
// restore-on-signal. The saved mode is recorded even then, so the normal
// Close path keeps restoring.
func registerConsoleCtrlHandler(h uintptr, origMode uint32) bool {
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	consoleCtrlState.h = h
	consoleCtrlState.origMode = origMode
	consoleCtrlState.restored = false
	if consoleCtrlState.callback != 0 {
		return true // already registered: single slot, just refresh state
	}
	cb := syscall.NewCallback(consoleCtrlHandler)
	r, _, _ := tuiProcSetConsoleCtrlHandler.Call(cb, 1)
	if r == 0 {
		return false
	}
	consoleCtrlState.callback = cb
	return true
}

// unregisterConsoleCtrlHandler detaches the callback when it belongs to h.
func unregisterConsoleCtrlHandler(h uintptr) {
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	if consoleCtrlState.callback == 0 || consoleCtrlState.h != h {
		return
	}
	_, _, _ = tuiProcSetConsoleCtrlHandler.Call(consoleCtrlState.callback, 0)
	consoleCtrlState.callback = 0
	consoleCtrlState.h = 0
}

// restoreConsoleInputMode puts back the saved console input mode exactly
// once. Safe from any goroutine: the handler thread (console control
// events arrive on a dedicated thread) and the normal Close path can both
// call it; the once-latch makes the second call a no-op. A handle mismatch
// (restore requested for a console that was never registered) is a no-op.
func restoreConsoleInputMode(h uintptr) {
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	if consoleCtrlState.restored || h == 0 || h != consoleCtrlState.h {
		return
	}
	consoleCtrlState.restored = true
	_, _, _ = tuiProcSetConsoleMode.Call(h, uintptr(consoleCtrlState.origMode))
}

// SetResizeHook installs the callback fired (synchronously, from ReadKey)
// every time a WINDOW_BUFFER_SIZE_EVENT record is latched.
func (c *ConsoleKeys) SetResizeHook(f func()) { c.onResize = f }

// Resized reports whether a resize record has been latched since open.
func (c *ConsoleKeys) Resized() bool { return c.resized }

// LatchedSize returns the console size carried by the most recent resize
// record. ok is false until the first resize arrives.
func (c *ConsoleKeys) LatchedSize() (int, int, bool) {
	if !c.resized {
		return 0, 0, false
	}
	return c.lastW, c.lastH, true
}

// ReadKey returns the next key, blocking until one arrives or the buffer
// fails. The console delivers a held key as ONE record with repeat=N; the
// count expands into N consecutive ReadKey results so a held arrow pages and
// held text repeats exactly as typed. Non-key records (focus, mouse) and
// key-up records are noise: they ride in the buffer and are consumed
// together with the next accepted key. Resize records are latched, consumed,
// and reported through the resize hook, then reading continues.
func (c *ConsoleKeys) ReadKey() (Key, bool) {
	if c.closed {
		return "", false
	}
	if c.pendingN > 0 {
		c.pendingN--
		return c.pendingKey, true
	}
	for {
		buf, ok := c.in.peek(32)
		if !ok {
			return "", false
		}
		if len(buf) > 0 {
			consumed, key, ok := c.scan(buf)
			if !ok {
				return "", false
			}
			if !c.consumeThrough(consumed) {
				return "", false
			}
			if key != "" {
				return key, true
			}
			// scan consumed accepted noise (unmapped keys, resize events,
			// held surrogates) and wants a fresh peek; a full window of
			// pure noise asks us to wait for more records.
			if consumed == 0 {
				time.Sleep(15 * time.Millisecond)
			}
			continue
		}
		// Empty buffer: nothing to decide yet. A partial window of pure
		// noise (shorter than the peek size) can never hide a key behind
		// it, so drain it and keep waiting instead of re-scanning the same
		// records forever.
		time.Sleep(15 * time.Millisecond)
	}
}

// scan inspects one peeked window and returns how many leading records the
// caller must consume, plus the accepted key ("" when the window held only
// noise). It never consumes itself; ReadKey applies the count.
func (c *ConsoleKeys) scan(buf []inputRecord) (consume int, key Key, ok bool) {
	for i := 0; i < len(buf); i++ {
		rec := buf[i]
		switch rec.eventType {
		case windowBufferSizeEventType:
			w, h := rec.windowSize()
			c.resized = true
			c.lastW, c.lastH = w, h
			if c.onResize != nil {
				c.onResize()
			}
			return i + 1, "", true
		case keyEventRecordType:
			if rec.keyDown == 0 {
				continue // key-up record: noise
			}
			k, mapped := c.foldKey(rec)
			if !mapped {
				return i + 1, "", true // unmapped key: drop with its noise
			}
			if rep := int(rec.repeat); rep > 1 {
				c.pendingKey, c.pendingN = k, rep-1
			}
			return i + 1, k, true
		}
		// Other event types (focus, mouse, menu) are noise.
	}
	// No key in this window: if the window is full there may be more
	// records behind it, so keep waiting; otherwise the window is pure
	// noise and can be drained.
	if len(buf) < 32 {
		return len(buf), "", true
	}
	return 0, "", true
}

// foldKey folds UTF-16 surrogate pairs before mapping: a high surrogate is
// held until its low half arrives (mapped false, consumed as noise); a lone
// low surrogate degrades to U+FFFD so text is never silently dropped.
func (c *ConsoleKeys) foldKey(rec inputRecord) (Key, bool) {
	ch := rec.char
	if ch >= 0xd800 && ch <= 0xdbff {
		c.holdHi = ch
		return "", false
	}
	if ch >= 0xdc00 && ch <= 0xdfff {
		if c.holdHi != 0 {
			r := rune(c.holdHi&0x3ff)<<10 | rune(ch&0x3ff)
			c.holdHi = 0
			return RuneKey(r + 0x10000), true
		}
		return RuneKey(0xfffd), true
	}
	c.holdHi = 0
	return mapConsoleKey(ch, rec.virtualK, rec.state)
}

// consumeThrough consumes exactly n leading records, tolerating short reads.
func (c *ConsoleKeys) consumeThrough(n int) bool {
	for n > 0 {
		got, ok := c.in.consume(n)
		if !ok || got == 0 {
			return false
		}
		n -= got
	}
	return true
}

// consoleScreenBufferInfo mirrors the Win32 CONSOLE_SCREEN_BUFFER_INFO
// header (22 bytes; the trailing fields are not needed for the window box).
type consoleScreenBufferInfo struct {
	sizeX, sizeY                         int16
	cursorX, cursorY                     int16
	attributes                           uint16
	winLeft, winTop, winRight, winBottom int16
	maxWinX, maxWinY                     int16
}

// ConsoleSize queries the console window size in cells through
// GetConsoleScreenBufferInfo. ok is false for pipes and non-console handles.
func ConsoleSize(out *os.File) (int, int, bool) {
	if out == nil {
		return 0, 0, false
	}
	h := out.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return 0, 0, false
	}
	var info consoleScreenBufferInfo
	r, _, _ := tuiProcGetConsoleScreenBufferInfo.Call(h, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, 0, false
	}
	w := int(info.winRight - info.winLeft + 1)
	hgt := int(info.winBottom - info.winTop + 1)
	if w <= 0 || hgt <= 0 {
		return 0, 0, false
	}
	return w, hgt, true
}

// isConsoleHandle reports whether GetConsoleMode succeeds on the handle.
func isConsoleHandle(f *os.File) bool {
	if f == nil {
		return false
	}
	h := f.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return false
	}
	var mode uint32
	r, _, _ := tuiProcGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

// enableVTOutput opts the console output handle into VT processing and
// returns the original mode. A down-level host fails with
// ERROR_INVALID_PARAMETER; that error propagates so the ladder picks
// TierPlain.
func enableVTOutput(out *os.File) (uint32, error) {
	if out == nil {
		return 0, ErrNotConsole
	}
	h := out.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return 0, ErrNotConsole
	}
	var orig uint32
	if r, _, _ := tuiProcGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&orig))); r == 0 {
		return 0, ErrNotConsole
	}
	r, _, err := tuiProcSetConsoleMode.Call(h, uintptr(orig|enableVirtualTerminalProc))
	if r == 0 {
		if err == nil {
			return 0, ErrNotConsole
		}
		return 0, err
	}
	return orig, nil
}

// setConsoleMode restores a remembered console mode (Cap.Restore path).
func setConsoleMode(f *os.File, mode uint32) {
	if f == nil {
		return
	}
	h := f.Fd()
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return
	}
	_, _, _ = tuiProcSetConsoleMode.Call(h, uintptr(mode))
}

// defaultProbes is the Windows probe set behind Detect (termcap.go).
var defaultProbes = probes{
	isConsole: func() bool { return isConsoleHandle(os.Stdout) },
	enableVT:  func() (uint32, error) { return enableVTOutput(os.Stdout) },
	setMode:   func(m uint32) { setConsoleMode(os.Stdout, m) },
	size:      func() (int, int, bool) { return ConsoleSize(os.Stdout) },
	getenv:    os.Getenv,
}
