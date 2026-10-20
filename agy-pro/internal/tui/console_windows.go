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
//
// Real-console defect fixes on top of that foundation (follow-up research B
// and C2): the noise drain that keeps the FIFO head moving, the mouse-input
// and VT-input normalization in the takeover, the window-input flag the
// resize latch needs, the output-mode restore on the Ctrl+C path, and the
// paste interior-newline debounce.

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
	enableProcessedInput      = 0x0001 // ENABLE_PROCESSED_INPUT (deliberately left as found)
	enableWindowInput         = 0x0008 // ENABLE_WINDOW_INPUT
	enableMouseInput          = 0x0010 // ENABLE_MOUSE_INPUT
	enableQuickEdit           = 0x0040 // ENABLE_QUICK_EDIT_MODE
	enableExtendedFlags       = 0x0080 // ENABLE_EXTENDED_FLAGS
	enableVirtualTerminalIn   = 0x0200 // ENABLE_VIRTUAL_TERMINAL_INPUT
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

	// Paste burst detector state. pendingSeenAt is when the batch of records
	// currently sitting in the input buffer first became OBSERVABLE to the
	// reader (a peek returned records after the buffer had been empty), and
	// batchStamped reports whether the current batch carries that stamp. The
	// reference is the batch arrival, never the per-record consumption time:
	// a reader catching up on a backlog measures its own loop turnaround
	// against a consumption stamp and would drop a genuine Enter. now is the
	// clock seam; tests replace it so no test sleeps (nil means time.Now).
	now           func() time.Time
	pendingSeenAt time.Time
	batchStamped  bool
}

// clock reads the injectable time source of the paste burst detector.
func (c *ConsoleKeys) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// pasteBurstWindow is the age under which a pending batch still counts as a
// paste burst. A host injects a clipboard paste as one KEY_EVENT record per
// character with no perceptible gap, while no human presses two keys 15 ms
// apart, so an Enter whose queued batch is this young is burst traffic and not
// a submit.
const pasteBurstWindow = 15 * time.Millisecond

// keyDownQueuedBehind reports whether another key press (a KEY_EVENT record
// with keyDown set) is already queued behind index i. Key-up records do not
// count: the console queues one after every press, so counting them would drop
// every genuine Enter, whose own key-up always rides directly behind it.
func keyDownQueuedBehind(buf []inputRecord, i int) bool {
	for j := i + 1; j < len(buf); j++ {
		if buf[j].eventType == keyEventRecordType && buf[j].keyDown != 0 {
			return true
		}
	}
	return false
}

// pasteTrailingEnter reports whether the Enter record at index i of the peeked
// window is an interior newline of a multi-line paste and must be dropped
// instead of delivered. All three conditions must hold: another key press is
// already queued behind it, the batch holding it is younger than
// pasteBurstWindow, and the record is not a held key (repeat > 1), so the
// typematic expansion stays exact.
//
// A genuine Enter press fails at least one condition and is delivered with no
// added delay: its own key-up is the only record behind it, or the batch it
// belongs to arrived earlier than the window (a human pause, or a backlog the
// reader is still catching up on). The paste's FINAL newline is delivered too,
// because nothing is queued behind it: the rule covers interior newlines only,
// so a paste submits once at its end instead of leaking its middle lines into
// the surface that opens next.
func (c *ConsoleKeys) pasteTrailingEnter(buf []inputRecord, i, repeat int) bool {
	if repeat > 1 || !c.batchStamped || !keyDownQueuedBehind(buf, i) {
		return false
	}
	return c.clock().Sub(c.pendingSeenAt) <= pasteBurstWindow
}

// consoleTakeoverMode computes the input mode OpenConsoleKeys installs. It is
// a pure function of the mode found on the handle, so every flag decision can
// be pinned headlessly (console_windows_test.go); OpenConsoleKeys applies the
// result with SetConsoleMode and remembers the ORIGINAL mode for restore, so
// nothing decided here leaks past the process.
//
// Decisions, cross-checked against the Microsoft SetConsoleMode and Console
// Input Buffer documentation cited in the follow-up research:
//   - ENABLE_ECHO_INPUT and ENABLE_LINE_INPUT off: every keystroke must reach
//     ReadConsoleInputW instead of being echoed or line-buffered.
//   - ENABLE_EXTENDED_FLAGS on: required to change quick-edit at all.
//   - ENABLE_QUICK_EDIT_MODE off: a mouse selection must not freeze the read.
//   - ENABLE_MOUSE_INPUT off: the host then DISCARDS pointer records instead
//     of queueing them. Left on, every pointer move over the window queues a
//     MOUSE_EVENT record that this reader treats as noise, and 32 of them at
//     the buffer head used to livelock ReadKey (see scan's drain rule).
//   - ENABLE_VIRTUAL_TERMINAL_INPUT off: the host then delivers arrows as VK
//     KEY_EVENT records (VK_UP and friends), the shape mapConsoleKey is proven
//     for. With the flag inherited on, the host hands the reader VT byte
//     sequences (ESC [ A) and nothing in this repo reassembles them, so
//     arrows would either vanish or corrupt an EDIT field with a literal
//     "[A". Clearing is deterministic and the original mode comes back on
//     exit, so an inherited VT input mode costs nothing to normalize away.
//   - ENABLE_WINDOW_INPUT on: Microsoft places buffer-resizing events in the
//     input buffer only when this mode is set, so the resize latch in scan can
//     receive WINDOW_BUFFER_SIZE_EVENT at all.
//   - ENABLE_PROCESSED_INPUT left as found: it is what turns Ctrl+C into a
//     console control event, and that handler is the only rescue path when
//     the reader is wedged. The handler restores the console (input and
//     output modes) and then lets the default handler exit.
func consoleTakeoverMode(mode uint32) uint32 {
	newMode := mode &^ enableEchoInput &^ enableLineInput
	newMode |= enableExtendedFlags
	newMode &^= enableQuickEdit
	newMode &^= enableMouseInput
	newMode &^= enableVirtualTerminalIn
	newMode |= enableWindowInput
	return newMode
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
	// so a mouse selection cannot freeze the read; mouse input off so the
	// host discards pointer records instead of queueing noise; VT input off
	// so arrows always arrive as VK key events; window input on so resize
	// records can arrive at all. Extended flags stay on and processed input
	// stays as found (see consoleTakeoverMode for the per-flag reasoning).
	newMode := consoleTakeoverMode(mode)
	if r, _, _ := tuiProcSetConsoleMode.Call(h, uintptr(newMode)); r == 0 {
		return nil, ErrNotConsole
	}
	// Ctrl+C exits through the console control handler, not through a
	// buffered key record, so register a handler that restores the saved
	// console state (input mode, plus the output mode when the capability
	// layer enabled VT) before the process dies (see consoleCtrlHandler).
	// The mode handed over here is the PRE-takeover mode, so the restore puts
	// the console back exactly as it was found.
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
// exit. The handler below restores the saved input mode AND the saved output
// mode first and then returns false so the default handler still runs and the
// advertised Ctrl+C exit actually exits.
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

	outH        uintptr // console output handle whose VT mode was enabled
	outMode     uint32  // its pre-detection mode
	outRestored bool    // once-latch for the output restore
}

// SetConsoleOutputRestore records the console OUTPUT handle and its
// pre-detection mode so the console control handler can put them back. The
// capability layer enables ENABLE_VIRTUAL_TERMINAL_PROCESSING on the output
// handle during Detect, and its Cap.Restore runs only on the normal return
// path: Ctrl+C exits through the Go runtime's default handler, which does not
// run defers, so without this record the VT flag would leak onto the user's
// screen buffer after a Ctrl+C exit.
//
// The record also ARMS the console control handler when nothing has armed it
// yet. That covers the "console stdout, piped stdin" shape, where
// OpenConsoleKeys never runs and so never registers anything: the handler is
// installed without touching the input slot, which keeps
// restoreConsoleInputMode a no-op through its zero-handle check, and only the
// output mode is restored. When OpenConsoleKeys already registered the handler,
// nothing is re-armed.
//
// enableVTOutput calls this itself, so the production path needs no caller
// wiring; app.go may also call it directly once it owns the output handle.
// Repeated calls with the same handle and mode change nothing, so they never
// re-arm the once-latch; a different pair replaces the record and re-arms it.
// A zero handle (VT output never enabled: a pipe, or a down-level host) is
// ignored, so the restore stays a no-op there.
func SetConsoleOutputRestore(h uintptr, mode uint32) {
	if h == 0 || h == uintptr(syscall.InvalidHandle) {
		return
	}
	// The whole state transition is decided under one lock hold: the record,
	// the latch reset, and whether the handler still needs installing. The
	// install itself runs outside the lock (it takes the same mutex) but only
	// ever writes consoleCtrlState.callback, so a caller that races
	// OpenConsoleKeys cannot have its input slot wiped by this path, and a
	// refused registration leaves the recorded state intact.
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.outH == h && consoleCtrlState.outMode == mode {
		consoleCtrlState.mu.Unlock()
		return // same pair already recorded: nothing to re-arm
	}
	consoleCtrlState.outH = h
	consoleCtrlState.outMode = mode
	consoleCtrlState.outRestored = false
	install := consoleCtrlState.callback == 0
	consoleCtrlState.mu.Unlock()
	if install {
		installConsoleCtrlHandler()
	}
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
		restoreConsoleOutputMode()
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

// installConsoleCtrlHandler registers consoleCtrlHandler without recording any
// console state. It is the arm-only path SetConsoleOutputRestore uses, both for
// the shape where no input console was opened and for the refused-registration
// shape where an input slot is already recorded but no handler is installed, so
// a refused registration (or a caller that races OpenConsoleKeys) cannot wipe a
// slot another goroutine just filled: only consoleCtrlState.callback is ever
// written here. Idempotent, and safe to call with the mutex released by the
// caller.
func installConsoleCtrlHandler() bool {
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	if consoleCtrlState.callback != 0 {
		return true // already registered: single slot, nothing to install
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

// restoreConsoleOutputMode puts back the saved console output mode exactly
// once. Only the console control handler reaches this function: Cap.Restore
// restores the output mode on its own normal-exit path and never touches this
// latch, which is why the two restores keep separate latches and either one
// can happen without the other. A zero handle (VT output was never enabled) is
// a no-op, so the plain and down-level paths are untouched.
func restoreConsoleOutputMode() {
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	if consoleCtrlState.outRestored || consoleCtrlState.outH == 0 {
		return
	}
	consoleCtrlState.outRestored = true
	_, _, _ = tuiProcSetConsoleMode.Call(consoleCtrlState.outH, uintptr(consoleCtrlState.outMode))
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
// and reported through the resize hook, then reading continues. An interior
// newline of a multi-line paste is consumed without being delivered, so the
// paste cannot submit the surface it lands on and leak its remaining lines
// into the next one.
func (c *ConsoleKeys) ReadKey() (Key, bool) {
	if c.closed {
		return "", false
	}
	if c.pendingN > 0 {
		c.pendingN--
		return consoleDelivered(c.pendingKey, true)
	}
	for {
		buf, ok := c.in.peek(32)
		if !ok {
			return "", false
		}
		if len(buf) > 0 {
			if !c.batchStamped {
				// A new batch: these records just became observable, which is
				// the reference the paste burst detector measures against.
				// Stamping here rather than at consumption is what keeps a
				// reader that is catching up on a backlog from measuring its
				// own loop turnaround.
				c.batchStamped = true
				c.pendingSeenAt = c.clock()
			}
			consumed, key, ok := c.scan(buf)
			if !ok {
				return "", false
			}
			if !c.consumeThrough(consumed) {
				return "", false
			}
			if key != "" {
				return consoleDelivered(key, true)
			}
			// scan consumed accepted noise (unmapped keys, resize events,
			// held surrogates, a dropped paste newline) and wants a fresh
			// peek. Consumption moved the FIFO head, so the next peek sees
			// records the previous one could not: no wait is needed here,
			// which is the same rule the partial-window drain always used.
			continue
		}
		// Empty buffer: this batch is fully drained, so the next non-empty
		// peek starts a new one and re-stamps the burst reference. Wait for
		// the host to queue something instead of burning a syscall per spin.
		c.batchStamped = false
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
			// The payload is the SCREEN BUFFER size, not the viewport, so a
			// consumer must re-query ConsoleSize before trusting the numbers
			// (WINDOW_BUFFER_SIZE_RECORD.dwSize). The latch keeps the raw
			// pair for diagnostics only.
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
			if k == KeyEnter && c.pasteTrailingEnter(buf, i, int(rec.repeat)) {
				// Interior newline of a multi-line paste: consumed, never
				// delivered, so the paste cannot submit the current form and
				// leak its remaining lines into the approval modal.
				return i + 1, "", true
			}
			if rep := int(rec.repeat); rep > 1 {
				c.pendingKey, c.pendingN = k, rep-1
			}
			return i + 1, k, true
		}
		// Other event types (focus, mouse, menu) are noise.
	}
	// No accepted key in this window: drain all of it. Records are atomic and
	// the queue is FIFO, so consuming the head can never hide a key behind it,
	// and the head only moves when something consumes. Returning 0 here used
	// to livelock the reader: a full 32-record window of pure noise (pointer
	// moves, focus and menu events) was peeked forever, and every key queued
	// behind that noise, arrows and digits alike, was unreachable. The drain
	// is bounded by the peek size, so one ReadKey iteration never consumes
	// more than a single window.
	return len(buf), "", true
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

// StdinIsConsole reports whether the process standard input is a real Windows
// console. It is the stdin half of the setup entry gate (cmd/agy/main.go):
// ConsoleKeys is the TUI key source, and it reads the console input buffer
// with ReadConsoleInputW, so a stdin that is not a console can never deliver a
// typed keystroke. A full-screen surface behind such a stdin would accept
// nothing the user types, so the entry gate opens the TUI only when this probe
// and the stdout console probe both answer true.
//
// The probe reuses the reader's idiom instead of inventing a second one:
// os.Stdin is opened on the inherited STD_INPUT_HANDLE (the same handle
// OpenConsoleKeys takes over), the handle comes from the *os.File, the zero and
// INVALID_HANDLE_VALUE values are rejected first, and GetConsoleMode is the
// console test.
//
// Failure mapping on Windows, all FALSE with ERROR_INVALID_HANDLE from
// GetConsoleMode:
//   - PIPED stdin (`producer | agy pro setup`): an anonymous pipe handle.
//   - redirected stdin (`agy pro setup < input.txt`): a file handle.
//   - `< NUL`: the NUL device handle.
//   - detached or closed stdin: an invalid handle, rejected before the call.
//
// Only a console input buffer answers TRUE.
func StdinIsConsole() bool { return isConsoleHandle(os.Stdin) }

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
	// Record the pre-detection mode so the console control handler can put it
	// back on the Ctrl+C path, which never reaches Cap.Restore.
	SetConsoleOutputRestore(h, orig)
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
