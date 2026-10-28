//go:build windows

package tui

import (
	"os"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// fakeConsoleBuffer is the peek-then-consume seam: tests script records,
// observe exactly how many were consumed, and can append records while a
// reader goroutine is blocked (mutex-guarded).
type fakeConsoleBuffer struct {
	mu         sync.Mutex
	records    []inputRecord
	consumed   int
	failPeek   bool
	emptyPeeks int
}

func (f *fakeConsoleBuffer) peek(n int) ([]inputRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPeek {
		return nil, false
	}
	if n <= 0 {
		return nil, true
	}
	end := n
	if end > len(f.records) {
		end = len(f.records)
	}
	if end == 0 {
		f.emptyPeeks++
	}
	return f.records[:end], true
}

func (f *fakeConsoleBuffer) consume(n int) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n > len(f.records) {
		n = len(f.records)
	}
	f.records = f.records[n:]
	f.consumed += n
	return n, true
}

func (f *fakeConsoleBuffer) append(recs ...inputRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, recs...)
}

func (f *fakeConsoleBuffer) consumedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consumed
}

// emptyPeekCount reports how many peek calls returned no records. A pin polls
// it to wait deterministically for a reader to reach the empty-buffer wait,
// instead of sleeping a fixed duration that a loaded host can outrun.
func (f *fakeConsoleBuffer) emptyPeekCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.emptyPeeks
}

func keyRec(char, vk uint16, state uint32) inputRecord {
	return inputRecord{eventType: keyEventRecordType, keyDown: 1, char: char, virtualK: vk, state: state}
}

// resetConsoleCtrlStateForTest clears the single console control slot so one
// test's registration or restore cannot leak into another. The callback
// registration itself is removed first with unregisterConsoleCtrlHandler and
// the matching handle; this helper only clears the recorded state.
func resetConsoleCtrlStateForTest() {
	consoleCtrlState.mu.Lock()
	consoleCtrlState.h = 0
	consoleCtrlState.origMode = 0
	consoleCtrlState.restored = false
	consoleCtrlState.outH = 0
	consoleCtrlState.outMode = 0
	consoleCtrlState.outRestored = false
	consoleCtrlState.mu.Unlock()
}

func keyRepeatRec(char, vk uint16, state uint32, repeat uint16) inputRecord {
	rec := keyRec(char, vk, state)
	rec.repeat = repeat
	return rec
}

// keyUpRec is the key-release half of a key press: the console queues one
// after every key-down, and the reader treats it as noise.
func keyUpRec(char, vk uint16) inputRecord {
	rec := keyRec(char, vk, 0)
	rec.keyDown = 0
	return rec
}

func resizeRec(w, h int16) inputRecord {
	var rec inputRecord
	rec.eventType = windowBufferSizeEventType
	sz := (*winSize)(unsafe.Pointer(&rec.keyDown))
	sz.x, sz.y = w, h
	return rec
}

func TestMapConsoleKey_FullEnum(t *testing.T) {
	r := func(ch rune) Key { return RuneKey(ch) }
	cases := []struct {
		name     string
		char, vk uint16
		state    uint32
		want     Key
	}{
		{"up", 0, vkUp, 0, KeyUp},
		{"down", 0, vkDown, 0, KeyDown},
		{"left", 0, vkLeft, 0, KeyLeft},
		{"right", 0, vkRight, 0, KeyRight},
		{"shift+up pages", 0, vkUp, shiftMask, KeyPgUp},
		{"shift+down pages", 0, vkDown, shiftMask, KeyPgDn},
		{"pgup", 0, vkPrior, 0, KeyPgUp},
		{"pgdn", 0, vkNext, 0, KeyPgDn},
		{"home", 0, vkHome, 0, KeyHome},
		{"end", 0, vkEnd, 0, KeyEnd},
		{"delete", 0, vkDelete, 0, KeyDelete},
		{"esc", 0, vkEscape, 0, KeyEsc},
		{"tab", 0, vkTab, 0, KeyTab},
		{"enter", 0, vkReturn, 0, KeyEnter},
		{"backspace", 0, vkBack, 0, KeyBackspace},
		{"ctrl-c", 0x03, 0, 0, KeyCtrlC},
		{"ctrl-l", 0x0c, 0, 0, KeyCtrlL},
		{"ctrl-s", 0x13, 0, 0, KeyCtrlS},
		{"ctrl-a", 0x01, 0, 0, KeyCtrlA},
		{"space", 0x20, 0x20, 0, KeySpace},
		{"digit 3", '3', 0x33, 0, Key('3')},
		{"digit 0", '0', 0x30, 0, Key('0')},
		{"y", 'y', 0x59, 0, KeyYes},
		{"Y", 'Y', 0x59, 0, r('Y')},
		{"n", 'n', 0x4e, 0, KeyNo},
		{"N", 'N', 0x4e, 0, r('N')},
		{"printable a", 'a', 0x41, 0, r('a')},
		{"printable tilde", '~', 0, 0, r('~')},
		{"latin-1 e-acute", 0xe9, 0, 0, r('é')},
		{"wide hangul", 0xd55c, 0, 0, r('한')},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := mapConsoleKey(c.char, c.vk, c.state)
			if !ok || got != c.want {
				t.Errorf("mapConsoleKey(%#x,%#x,%#x) = (%q,%v); want (%q,true)", c.char, c.vk, c.state, got, ok, c.want)
			}
		})
	}
	if _, ok := mapConsoleKey(0, 0, 0); ok {
		t.Error("unmapped event must not produce a key")
	}
}

func TestConsoleKeys_PeekThenConsume(t *testing.T) {
	// Focus noise, then an arrow, then Enter: noise and key are consumed
	// together on acceptance, never before.
	buf := &fakeConsoleBuffer{records: []inputRecord{
		{eventType: 0x0010}, // focus event noise
		keyRec(0, vkDown, 0),
		keyRec(0, vkReturn, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	k, ok := ck.ReadKey()
	if !ok || k != KeyDown {
		t.Fatalf("ReadKey = (%q,%v); want down", k, ok)
	}
	if got := buf.consumedCount(); got != 2 {
		t.Errorf("consumed %d records; want 2 (noise + key)", got)
	}
	k, ok = ck.ReadKey()
	if !ok || k != KeyEnter {
		t.Fatalf("ReadKey = (%q,%v); want enter", k, ok)
	}
	if got := buf.consumedCount(); got != 3 {
		t.Errorf("consumed %d records; want 3", got)
	}
}

func TestConsoleKeys_SkipsUnmapped(t *testing.T) {
	buf := &fakeConsoleBuffer{records: []inputRecord{
		keyRec(0, 0x70, 0), // F1, not mapped
		keyRec('5', 0x35, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	k, ok := ck.ReadKey()
	if !ok || k != Key('5') {
		t.Fatalf("ReadKey = (%q,%v); want digit 5", k, ok)
	}
	if got := buf.consumedCount(); got != 2 {
		t.Errorf("consumed %d records; want 2 (unmapped + key)", got)
	}
}

func TestConsoleKeys_TypematicRepeat(t *testing.T) {
	// One record with repeat=3 becomes three KeyUp results.
	buf := &fakeConsoleBuffer{records: []inputRecord{
		keyRepeatRec(0, vkUp, 0, 3),
		keyRec('x', 0x58, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	for i := 0; i < 3; i++ {
		k, ok := ck.ReadKey()
		if !ok || k != KeyUp {
			t.Fatalf("repeat %d: ReadKey = (%q,%v); want up", i, k, ok)
		}
	}
	if got := buf.consumedCount(); got != 1 {
		t.Errorf("consumed %d records after 3 keys; want 1 (single record)", got)
	}
	k, ok := ck.ReadKey()
	if !ok || k != RuneKey('x') {
		t.Fatalf("ReadKey after repeat = (%q,%v); want rune x", k, ok)
	}
}

// TestConsoleReadKeyMarksConsolePacing pins the console half of the gate arm
// delay: every key ReadKey delivers advances the pacing serial, including the
// typematic repeats expanded from one held-key record, while a read that
// delivers no key leaves it alone. The serial is what makes a gate treat a
// console answer as paste-exposed; if the mark were dropped, the arm window
// would silently become dead code on the only source it protects.
func TestConsoleReadKeyMarksConsolePacing(t *testing.T) {
	buf := &fakeConsoleBuffer{records: []inputRecord{
		keyRepeatRec(0, vkUp, 0, 2),
		keyRec('y', 0x59, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	before := consoleKeySerial
	for i := 0; i < 3; i++ {
		if _, ok := ck.ReadKey(); !ok {
			t.Fatalf("read %d delivered no key", i)
		}
	}
	if got := consoleKeySerial - before; got != 3 {
		t.Fatalf("console serial advanced %d times for 3 keys", got)
	}

	// A read that cannot deliver a key (peek failure here, EOF or a closed
	// reader on the real handle) must not mark anything as paced.
	dead := &ConsoleKeys{in: &fakeConsoleBuffer{failPeek: true}}
	if _, ok := dead.ReadKey(); ok {
		t.Fatal("a failing peek delivered a key")
	}
	if got := consoleKeySerial - before; got != 3 {
		t.Fatalf("a read without a key advanced the serial: %d", got)
	}
}

func TestConsoleKeys_ResizeLatch(t *testing.T) {
	buf := &fakeConsoleBuffer{records: []inputRecord{
		keyRec(0, vkDown, 0),
		resizeRec(132, 43),
		keyRec(0, vkReturn, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	if ck.Resized() {
		t.Fatal("resize latched before any record was read")
	}
	if _, _, ok := ck.LatchedSize(); ok {
		t.Fatal("LatchedSize ok before any resize record")
	}
	hooks := 0
	ck.SetResizeHook(func() { hooks++ })

	k, ok := ck.ReadKey()
	if !ok || k != KeyDown {
		t.Fatalf("ReadKey = (%q,%v); want down", k, ok)
	}
	if ck.Resized() {
		t.Error("resize latched before the resize record")
	}

	k, ok = ck.ReadKey()
	if !ok || k != KeyEnter {
		t.Fatalf("ReadKey across resize = (%q,%v); want enter", k, ok)
	}
	if !ck.Resized() {
		t.Error("resize record was not latched")
	}
	if hooks != 1 {
		t.Errorf("resize hook fired %d times; want 1", hooks)
	}
	if w, h, ok := ck.LatchedSize(); !ok || w != 132 || h != 43 {
		t.Errorf("LatchedSize = (%d,%d,%v); want (132,43,true)", w, h, ok)
	}
	if got := buf.consumedCount(); got != 3 {
		t.Errorf("consumed %d records; want 3", got)
	}
}

func TestConsoleKeys_SurrogatePair(t *testing.T) {
	// U+1F600 GRINNING FACE arrives as UTF-16 halves D83D + DE00.
	buf := &fakeConsoleBuffer{records: []inputRecord{
		{eventType: keyEventRecordType, keyDown: 1, char: 0xd83d},
		{eventType: keyEventRecordType, keyDown: 1, char: 0xde00},
		keyRec('a', 0x41, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	k, ok := ck.ReadKey()
	if !ok {
		t.Fatal("ReadKey on surrogate pair returned false")
	}
	r, isRune := RuneOf(k)
	if !isRune || r != 0x1f600 {
		t.Errorf("surrogate pair = %q (rune %#x,%v); want U+1F600", k, r, isRune)
	}
	k, ok = ck.ReadKey()
	if !ok || k != RuneKey('a') {
		t.Fatalf("ReadKey after pair = (%q,%v); want rune a", k, ok)
	}
}

func TestConsoleKeys_LoneLowSurrogate(t *testing.T) {
	buf := &fakeConsoleBuffer{records: []inputRecord{
		{eventType: keyEventRecordType, keyDown: 1, char: 0xde00},
	}}
	ck := &ConsoleKeys{in: buf}
	k, ok := ck.ReadKey()
	if !ok {
		t.Fatal("ReadKey returned false")
	}
	r, isRune := RuneOf(k)
	if !isRune || r != 0xfffd {
		t.Errorf("lone low surrogate = %q; want U+FFFD carrier", k)
	}
}

func TestConsoleKeys_DrainsNoiseThenReadsLateKey(t *testing.T) {
	// A partial window (fewer records than the peek size) holding only
	// noise must be drained so a later key is not scanned behind it
	// forever.
	buf := &fakeConsoleBuffer{records: []inputRecord{
		{eventType: 0x0010},
		{eventType: 0x0002}, // mouse event noise
	}}
	ck := &ConsoleKeys{in: buf}
	result := make(chan Key, 1)
	go func() {
		k, ok := ck.ReadKey()
		if ok {
			result <- k
		}
		close(result)
	}()
	time.Sleep(60 * time.Millisecond) // let the reader drain and block
	buf.append(keyRec(0, vkEscape, 0))
	select {
	case k, ok := <-result:
		if !ok || k != KeyEsc {
			t.Fatalf("ReadKey = (%q,%v); want esc", k, ok)
		}
	case <-time.After(2 * time.Second):
		buf.stop() // unwind the reader instead of leaving it spinning
		t.Fatal("ReadKey did not return after the late key arrived")
	}
}

func TestConsoleKeys_PeekFailureStops(t *testing.T) {
	buf := &fakeConsoleBuffer{failPeek: true}
	ck := &ConsoleKeys{in: buf}
	if k, ok := ck.ReadKey(); ok || k != "" {
		t.Errorf("ReadKey on failed peek = (%q,%v); want (\"\",false)", k, ok)
	}
}

func TestConsoleKeys_KeyUpRecordsAreNoise(t *testing.T) {
	up := keyRec(0, vkDown, 0)
	up.keyDown = 0
	buf := &fakeConsoleBuffer{records: []inputRecord{
		up,
		keyRec(0, vkDown, 0),
	}}
	ck := &ConsoleKeys{in: buf}
	k, ok := ck.ReadKey()
	if !ok || k != KeyDown {
		t.Fatalf("ReadKey = (%q,%v); want down", k, ok)
	}
	if got := buf.consumedCount(); got != 2 {
		t.Errorf("consumed %d records; want 2 (key-up noise + key-down)", got)
	}
}

// TestEditRuneConsoleVocabulary pins the full console-to-EDIT chain for the
// keys that live double lives: mapConsoleKey output goes through EditRune,
// never a hand-built RuneKey. Digit 0 must survive (review finding: '0' was
// silently dropped in every EDIT field) and uppercase Y/N must keep their
// case.
func TestEditRuneConsoleVocabulary(t *testing.T) {
	cases := []struct {
		name     string
		char, vk uint16
		state    uint32
		want     rune
	}{
		{"digit 0", '0', 0x30, 0, '0'},
		{"digit 5", '5', 0x35, 0, '5'},
		{"uppercase Y", 'Y', 0x59, 0, 'Y'},
		{"uppercase N", 'N', 0x4e, 0, 'N'},
		{"lowercase y stays confirm-lowercase", 'y', 0x59, 0, 'y'},
		{"lowercase n stays confirm-lowercase", 'n', 0x4e, 0, 'n'},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, ok := mapConsoleKey(c.char, c.vk, c.state)
			if !ok {
				t.Fatalf("mapConsoleKey(%#x,%#x,%#x) not mapped", c.char, c.vk, c.state)
			}
			r, ok := EditRune(k)
			if !ok || r != c.want {
				t.Errorf("EditRune(mapConsoleKey(%#x)) = (%q,%v); want (%q,true)", c.char, r, ok, c.want)
			}
		})
	}
}

// TestFormTypesThroughConsoleVocabulary types a real value (model timeout
// 300, context length 128000, family tag YOLO) through the console key
// vocabulary into a Form, the way a live console delivers keystrokes. The
// pre-fix code dropped every 0 and downcased Y/N, so this test is the
// end-to-end guard for both defects.
func TestFormTypesThroughConsoleVocabulary(t *testing.T) {
	typeThroughConsole := func(form *Form, text string) {
		t.Helper()
		for _, ch := range text {
			k, ok := mapConsoleKey(uint16(ch), 0, 0)
			if !ok {
				t.Fatalf("mapConsoleKey(%q) not mapped", ch)
			}
			form.HandleKey(k)
		}
	}

	t.Run("zero-containing value", func(t *testing.T) {
		form := NewForm([]Field{{Label: "Timeout", Value: ""}})
		typeThroughConsole(form, "128000")
		if got := form.Value(0); got != "128000" {
			t.Errorf("typed 128000 through console vocabulary; form value = %q", got)
		}
	})

	t.Run("uppercase preserved", func(t *testing.T) {
		form := NewForm([]Field{{Label: "Tag", Value: ""}})
		typeThroughConsole(form, "YOLO")
		if got := form.Value(0); got != "YOLO" {
			t.Errorf("typed YOLO through console vocabulary; form value = %q", got)
		}
	})
}

// TestConsoleKeys_CtrlHandlerLifecycle pins the console control handler
// plumbing headlessly: registration state, idempotent restore, and
// unregistration. The handler runs on a dedicated OS thread for real
// CTRL_C_EVENT delivery; that part only a live console can prove.
func TestConsoleKeys_CtrlHandlerLifecycle(t *testing.T) {
	// Any nonzero value works: the registration API is process-wide and the
	// restore call on a fake handle fails silently inside the kernel call.
	const fakeHandle = uintptr(0x7fff0001)

	consoleCtrlState.mu.Lock()
	if consoleCtrlState.callback != 0 {
		consoleCtrlState.mu.Unlock()
		t.Fatal("test precondition: a console control handler is already registered")
	}
	consoleCtrlState.mu.Unlock()

	// The slot is process-wide, so this test must leave it exactly as it found
	// it: without the reset the once-latch and the recorded handle survive into
	// the next test that reads them (the ordering is source order today, but no
	// pin may depend on that). Installed BEFORE the registration call so the
	// refusal path, which fatals out of the test, cannot leak the slot either.
	defer func() {
		unregisterConsoleCtrlHandler(fakeHandle)
		resetConsoleCtrlStateForTest()
	}()
	if !registerConsoleCtrlHandler(fakeHandle, enableEchoInput|enableLineInput) {
		t.Fatal("SetConsoleCtrlHandler refused registration")
	}
	consoleCtrlState.mu.Lock()
	registered := consoleCtrlState.callback != 0
	consoleCtrlState.mu.Unlock()
	if !registered {
		t.Error("registration flag not set after registerConsoleCtrlHandler")
	}

	// Unknown control events must not touch the restore latch.
	if got := consoleCtrlHandler(999); got != 0 {
		t.Errorf("consoleCtrlHandler(unknown) = %d; want 0 (pass through to default handling)", got)
	}
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.restored {
		t.Error("unknown control event latched the restore flag")
	}
	consoleCtrlState.mu.Unlock()

	// CTRL_C_EVENT restores; a second restore (the Close path racing the
	// handler) is a no-op, and a mismatched handle never restores.
	if got := consoleCtrlHandler(ctrlCEvent); got != 0 {
		t.Errorf("consoleCtrlHandler(CTRL_C_EVENT) = %d; want 0 so the default handler still exits the process", got)
	}
	consoleCtrlState.mu.Lock()
	if !consoleCtrlState.restored {
		t.Error("CTRL_C_EVENT did not latch the restore flag")
	}
	consoleCtrlState.mu.Unlock()
	restoreConsoleInputMode(fakeHandle) // idempotent: must stay latched, must not panic
	consoleCtrlState.mu.Lock()
	if !consoleCtrlState.restored {
		t.Error("second restore cleared the latch")
	}
	consoleCtrlState.mu.Unlock()
	restoreConsoleInputMode(fakeHandle + 1)      // wrong handle: no-op
	unregisterConsoleCtrlHandler(fakeHandle + 1) // wrong handle: keeps registration
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.callback == 0 {
		t.Error("unregister with a mismatched handle removed the registration")
	}
	consoleCtrlState.mu.Unlock()

	unregisterConsoleCtrlHandler(fakeHandle)
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.callback != 0 {
		t.Error("callback still registered after unregister")
	}
	consoleCtrlState.mu.Unlock()
}

// TestConsoleTakeoverMode_FlagDecisions pins the mode OpenConsoleKeys
// installs, flag by flag. OpenConsoleKeys itself needs a live console handle,
// so the math is pinned through the pure consoleTakeoverMode it calls; the
// syscall path (GetConsoleMode, SetConsoleMode, restore) is live-console
// verification.
func TestConsoleTakeoverMode_FlagDecisions(t *testing.T) {
	// A stock console default: everything on except window input and VT input
	// (Microsoft: "all input modes except ENABLE_WINDOW_INPUT and
	// ENABLE_VIRTUAL_TERMINAL_INPUT are enabled by default").
	const stock = enableProcessedInput | enableEchoInput | enableLineInput |
		enableMouseInput | enableQuickEdit | enableExtendedFlags
	got := consoleTakeoverMode(stock)
	if got&enableMouseInput != 0 {
		t.Error("ENABLE_MOUSE_INPUT must be cleared so the host discards pointer records")
	}
	if got&enableVirtualTerminalIn != 0 {
		t.Error("ENABLE_VIRTUAL_TERMINAL_INPUT must be cleared so arrows arrive as VK key events")
	}
	if got&enableWindowInput == 0 {
		t.Error("ENABLE_WINDOW_INPUT must be set so resize records can arrive at all")
	}
	if got&enableEchoInput != 0 || got&enableLineInput != 0 {
		t.Error("echo and line input must stay off")
	}
	if got&enableQuickEdit != 0 {
		t.Error("quick-edit must stay off so a mouse selection cannot freeze the read")
	}
	if got&enableExtendedFlags == 0 {
		t.Error("extended flags must stay on (required to change quick-edit)")
	}
	if got&enableProcessedInput == 0 {
		t.Error("processed input must be left as found: it is the Ctrl+C rescue path")
	}

	// A VT input mode inherited from a prior process on the same console must
	// be normalized away, and flags the takeover does not decide must survive
	// untouched (ENABLE_AUTO_POSITION 0x0100 here).
	const inherited = enableProcessedInput | enableVirtualTerminalIn | 0x0100
	if g := consoleTakeoverMode(inherited); g&enableVirtualTerminalIn != 0 {
		t.Errorf("consoleTakeoverMode(%#x) = %#x; want ENABLE_VIRTUAL_TERMINAL_INPUT cleared", inherited, g)
	}
	if g := consoleTakeoverMode(inherited); g&0x0100 == 0 || g&enableProcessedInput == 0 {
		t.Errorf("consoleTakeoverMode(%#x) = %#x; want undecided flags preserved", inherited, g)
	}
}

// TestConsoleKeys_TakeoverRestoresOriginalMode pins the restore contract of
// the takeover: what is registered for the Ctrl+C restore is the PRE-takeover
// mode, so clearing ENABLE_MOUSE_INPUT (and the rest) cannot leak past the
// process. OpenConsoleKeys hands the mode it read from the handle straight to
// registerConsoleCtrlHandler; the handle check and once-latch below are the
// existing restore path, which needs no new code for this.
func TestConsoleKeys_TakeoverRestoresOriginalMode(t *testing.T) {
	const fakeHandle = uintptr(0x7fff0002)
	const orig uint32 = enableProcessedInput | enableEchoInput | enableLineInput |
		enableMouseInput | enableQuickEdit | enableVirtualTerminalIn
	if consoleTakeoverMode(orig) == orig {
		t.Fatal("precondition: the takeover must change the mode it found")
	}
	registerConsoleCtrlHandler(fakeHandle, orig)
	defer func() {
		unregisterConsoleCtrlHandler(fakeHandle)
		resetConsoleCtrlStateForTest()
	}()
	consoleCtrlState.mu.Lock()
	saved := consoleCtrlState.origMode
	consoleCtrlState.mu.Unlock()
	if saved != orig {
		t.Fatalf("registered restore mode = %#x; want the pre-takeover %#x", saved, orig)
	}
	consoleCtrlHandler(ctrlCEvent)
	consoleCtrlState.mu.Lock()
	latched := consoleCtrlState.restored
	consoleCtrlState.mu.Unlock()
	if !latched {
		t.Error("CTRL_C_EVENT did not latch the input restore")
	}
}

// TestSetConsoleOutputRestore_ArmsHandler pins the "console stdout, piped
// stdin" shape: OpenConsoleKeys never runs there, so the setter must arm the
// console control handler itself, with a zero input handle that keeps the
// input restore a clean no-op. The OS-level install count is not observable
// headlessly, so "armed exactly once" is pinned through the callback handle
// staying identical and through the once-latch not being re-armed.
func TestSetConsoleOutputRestore_ArmsHandler(t *testing.T) {
	const fakeOut = uintptr(0x7fff0005)
	const outMode = enableEchoInput | enableQuickEdit

	consoleCtrlState.mu.Lock()
	if consoleCtrlState.callback != 0 {
		consoleCtrlState.mu.Unlock()
		t.Fatal("test precondition: a console control handler is already registered")
	}
	consoleCtrlState.mu.Unlock()

	SetConsoleOutputRestore(fakeOut, outMode)
	defer func() {
		// The setter armed the handler with a zero input handle, so detaching
		// by that handle is what removes the registration.
		unregisterConsoleCtrlHandler(0)
		resetConsoleCtrlStateForTest()
	}()
	consoleCtrlState.mu.Lock()
	armed := consoleCtrlState.callback
	zeroInput := consoleCtrlState.h
	consoleCtrlState.mu.Unlock()
	if armed == 0 {
		t.Fatal("SetConsoleOutputRestore did not arm the console control handler")
	}
	if zeroInput != 0 {
		t.Errorf("armed with input handle %#x; want 0 so the input restore stays a no-op", zeroInput)
	}

	// The handler restores the output mode only: the zero input handle keeps
	// restoreConsoleInputMode a no-op, so its latch must stay clear.
	consoleCtrlHandler(ctrlCEvent)
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.restored {
		t.Error("the input restore latched although no input handle was recorded")
	}
	if !consoleCtrlState.outRestored {
		t.Error("CTRL_C_EVENT did not restore the output mode")
	}
	consoleCtrlState.mu.Unlock()

	// An identical second call must not re-arm anything: same callback handle,
	// and the output latch stays latched.
	SetConsoleOutputRestore(fakeOut, outMode)
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.callback != armed {
		t.Error("an identical second call re-armed the console control handler")
	}
	if !consoleCtrlState.outRestored {
		t.Error("an identical second call re-armed the output restore latch")
	}
	consoleCtrlState.mu.Unlock()

	// A different pair is a new record and does re-arm the latch.
	SetConsoleOutputRestore(fakeOut+1, outMode)
	consoleCtrlState.mu.Lock()
	reArmed := consoleCtrlState.outRestored
	consoleCtrlState.mu.Unlock()
	if reArmed {
		t.Error("a different output record did not re-arm the restore latch")
	}
}

// TestSetConsoleOutputRestore_PreservesRecordedInputSlot pins the C5 arm-only
// hardening. A caller can reach the setter with an input slot already recorded
// but no handler installed yet: the refused-registration shape, where
// registerConsoleCtrlHandler stores the mode and the OS then refuses the
// install. The arm must install the callback WITHOUT touching the input slot.
// The earlier arm path routed through registerConsoleCtrlHandler(0, 0), which
// overwrote h, origMode and restored, silently disarming the input restore that
// the recorded slot existed for.
func TestSetConsoleOutputRestore_PreservesRecordedInputSlot(t *testing.T) {
	const fakeIn = uintptr(0x7fff0006)
	const inMode = enableEchoInput | enableLineInput | enableProcessedInput
	const fakeOut = uintptr(0x7fff0007)
	const outMode = enableEchoInput | enableQuickEdit

	consoleCtrlState.mu.Lock()
	if consoleCtrlState.callback != 0 {
		consoleCtrlState.mu.Unlock()
		t.Fatal("test precondition: a console control handler is already registered")
	}
	consoleCtrlState.h = fakeIn
	consoleCtrlState.origMode = inMode
	consoleCtrlState.restored = false
	consoleCtrlState.mu.Unlock()
	defer func() {
		// Detach by handle 0 first: in the exact wipe regression this pin
		// detects, the slot handle is already 0, so detaching by fakeIn alone
		// would no-op and leave the process-wide callback installed for the
		// next test's precondition to fatal on. The fakeIn detach then covers
		// the healthy path, where the slot still carries it.
		unregisterConsoleCtrlHandler(0)
		unregisterConsoleCtrlHandler(fakeIn)
		resetConsoleCtrlStateForTest()
	}()

	SetConsoleOutputRestore(fakeOut, outMode)

	consoleCtrlState.mu.Lock()
	armed := consoleCtrlState.callback
	consoleCtrlState.mu.Unlock()
	if armed == 0 {
		t.Fatal("SetConsoleOutputRestore did not arm the console control handler")
	}
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.h != fakeIn {
		t.Errorf("input handle = %#x; want %#x (the arm must not overwrite the recorded input slot)", consoleCtrlState.h, fakeIn)
	}
	if consoleCtrlState.origMode != inMode {
		t.Errorf("input mode = %#x; want %#x (the arm must not overwrite the recorded input slot)", consoleCtrlState.origMode, inMode)
	}
	consoleCtrlState.mu.Unlock()

	// The input restore must still be live for the handle recorded before the
	// arm, and the output restore must fire too.
	consoleCtrlHandler(ctrlCEvent)
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	if !consoleCtrlState.restored {
		t.Error("CTRL_C_EVENT did not restore the input mode recorded before the arm")
	}
	if !consoleCtrlState.outRestored {
		t.Error("CTRL_C_EVENT did not restore the output mode")
	}
}

// TestSetConsoleOutputRestore_CtrlHandlerRestoresOutput pins the output half
// of the Ctrl+C restore when an input console is also open: the STDOUT handle
// and its pre-detection mode are recorded, the handler restores both handles
// once, and a later restore is a no-op. The kernel call on a fake handle fails
// silently, exactly as in the input lifecycle test above.
func TestSetConsoleOutputRestore_CtrlHandlerRestoresOutput(t *testing.T) {
	const fakeIn = uintptr(0x7fff0004)
	const fakeOut = uintptr(0x7fff0005)
	const inMode = enableEchoInput | enableLineInput
	const outMode = enableEchoInput | enableQuickEdit

	// A zero handle means VT output was never enabled: nothing is recorded and
	// no handler is armed.
	SetConsoleOutputRestore(0, outMode)
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.outH != 0 || consoleCtrlState.callback != 0 {
		consoleCtrlState.mu.Unlock()
		t.Fatal("a zero output handle must not record state or arm the handler")
	}
	consoleCtrlState.mu.Unlock()

	registerConsoleCtrlHandler(fakeIn, inMode)
	defer func() {
		unregisterConsoleCtrlHandler(fakeIn)
		resetConsoleCtrlStateForTest()
	}()
	SetConsoleOutputRestore(fakeOut, outMode)
	consoleCtrlState.mu.Lock()
	if consoleCtrlState.outH != fakeOut || consoleCtrlState.outMode != outMode {
		consoleCtrlState.mu.Unlock()
		t.Fatalf("recorded output restore = (%#x,%#x); want (%#x,%#x)",
			consoleCtrlState.outH, consoleCtrlState.outMode, fakeOut, outMode)
	}
	consoleCtrlState.mu.Unlock()

	consoleCtrlHandler(ctrlCEvent)
	consoleCtrlState.mu.Lock()
	if !consoleCtrlState.restored {
		t.Error("CTRL_C_EVENT did not restore the input mode")
	}
	if !consoleCtrlState.outRestored {
		t.Error("CTRL_C_EVENT did not restore the output mode")
	}
	consoleCtrlState.mu.Unlock()

	// Idempotent: a second handler call (or the Close path racing it) must
	// not clear the latches.
	consoleCtrlHandler(ctrlCEvent)
	consoleCtrlState.mu.Lock()
	if !consoleCtrlState.restored || !consoleCtrlState.outRestored {
		t.Error("second CTRL_C_EVENT cleared a restore latch")
	}
	consoleCtrlState.mu.Unlock()
}

// TestConsoleKeys_FullNoiseWindowDrains pins the livelock fix: a peek window
// that is completely full of noise (32 records, no accepted key) must still be
// consumed so the FIFO head advances and the key queued behind the flood is
// reachable. Before the fix scan returned consume=0 for a full window, so the
// same 32 records were peeked forever. The drain also stops at the first
// accepted key, so nothing behind it is thrown away.
func TestConsoleKeys_FullNoiseWindowDrains(t *testing.T) {
	recs := make([]inputRecord, 0, 34)
	for i := 0; i < 32; i++ {
		switch i % 3 {
		case 0:
			recs = append(recs, inputRecord{eventType: 0x0010}) // focus event noise
		case 1:
			recs = append(recs, inputRecord{eventType: 0x0002}) // mouse event noise
		default:
			up := keyRec(0, vkUp, 0)
			up.keyDown = 0
			recs = append(recs, up) // key-up noise
		}
	}
	recs = append(recs, keyRec(0, vkDown, 0), keyRec(0, vkReturn, 0))
	buf := &fakeConsoleBuffer{records: recs}
	ck := &ConsoleKeys{in: buf}

	// The read runs under a deadline: the pre-fix code returned consume=0 for
	// a full window, so this read never returned at all and a plain call here
	// would hang the suite instead of failing. The timeout branch stops the
	// buffer first so the reader unwinds instead of spinning for the rest of
	// the run.
	result := make(chan Key, 1)
	go func() {
		if k, ok := ck.ReadKey(); ok {
			result <- k
		}
		close(result)
	}()
	select {
	case k, ok := <-result:
		if !ok || k != KeyDown {
			t.Fatalf("ReadKey = (%q,%v); want down behind a full noise window", k, ok)
		}
	case <-time.After(2 * time.Second):
		buf.stop()
		t.Fatal("ReadKey did not return: a full noise window still blocks the reader")
	}
	if got := buf.consumedCount(); got != 33 {
		t.Errorf("consumed %d records; want 33 (32 noise + the key)", got)
	}
	// Under the same deadline: a regression in the drain-stops-at-the-key rule
	// would block here, and a bare call would hang the package to its 10 minute
	// timeout instead of failing this pin.
	k, ok := readKeyWithin(t, ck, 2*time.Second)
	if !ok || k != KeyEnter {
		t.Fatalf("ReadKey after the drain = (%q,%v); want enter", k, ok)
	}
	if got := buf.consumedCount(); got != 34 {
		t.Errorf("consumed %d records; want 34 (nothing past the accepted key)", got)
	}
}

// stop makes every later peek fail so a reader blocked in ReadKey returns and
// a pin can finish after observing a wait, instead of leaving a goroutine
// spinning on the 15 ms empty-buffer sleep for the rest of the run. Same
// contract as the settings package's fakeConsoleBuffer.stop.
func (f *fakeConsoleBuffer) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPeek = true
}

// readKeyWithin reads one key under a deadline, so a regression that swallows
// the key fails the subtest instead of hanging the suite (a dropped Enter
// leaves the reader waiting on an empty buffer forever). On timeout it stops
// the fake buffer first, so the reader goroutine unwinds instead of pegging a
// core for the rest of the run (same contract as the settings helper).
func readKeyWithin(t *testing.T, ck *ConsoleKeys, d time.Duration) (Key, bool) {
	t.Helper()
	type res struct {
		k  Key
		ok bool
	}
	done := make(chan res, 1)
	go func() {
		k, ok := ck.ReadKey()
		done <- res{k, ok}
	}()
	select {
	case r := <-done:
		return r.k, r.ok
	case <-time.After(d):
		if fb, ok := ck.in.(*fakeConsoleBuffer); ok {
			fb.stop()
		}
		t.Fatalf("ReadKey did not return within %v", d)
		return "", false
	}
}

// TestConsoleKeys_PasteEnterDebounce pins the multi-line paste mitigation
// (decision 8A). A paste arrives as one KEY_EVENT per character with CR for
// newlines; an interior newline must not submit the surface it lands on,
// because the remaining lines would leak into the next surface (the approval
// modal is a confirmation gate). The clock seam (ConsoleKeys.now) makes the
// burst-window decision deterministic: no subtest depends on wall-clock timing
// for the drop-or-deliver outcome, and the burst reference is the moment the
// pending batch became observable, never the moment a record was consumed. One
// subtest parks a reader on an empty buffer before appending its second batch;
// that wait is scheduling only (it lets the reader reach the empty peek that
// clears the stamp) and never feeds the burst decision.
func TestConsoleKeys_PasteEnterDebounce(t *testing.T) {
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	newReader := func(records []inputRecord) (*ConsoleKeys, *fakeConsoleBuffer, func(time.Duration)) {
		buf := &fakeConsoleBuffer{records: records}
		var mu sync.Mutex
		tick := base
		ck := &ConsoleKeys{in: buf, now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return tick
		}}
		return ck, buf, func(d time.Duration) {
			mu.Lock()
			tick = tick.Add(d)
			mu.Unlock()
		}
	}

	t.Run("enter with nothing pending is delivered", func(t *testing.T) {
		// The clock never advances, so the only thing that can save this
		// Enter is the empty queue behind it: a real press must not be delayed
		// or dropped.
		ck, _, _ := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec(0, vkReturn, 0),
		})
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('a') {
			t.Fatalf("ReadKey = (%q,%v); want rune a", k, ok)
		}
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
			t.Fatalf("ReadKey = (%q,%v); want enter (nothing pending behind it)", k, ok)
		}
	})

	t.Run("enter with only its own key-up behind it is delivered", func(t *testing.T) {
		// The console queues a key-up record after every press, so a genuine
		// Enter always has one record behind it. Counting that record as
		// pending would drop every real Enter: only another key PRESS counts.
		ck, _, _ := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec(0, vkReturn, 0),
			keyUpRec(0, vkReturn),
		})
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('a') {
			t.Fatalf("ReadKey = (%q,%v); want rune a", k, ok)
		}
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
			t.Fatalf("ReadKey = (%q,%v); want enter (its own key-up is not a pending key)", k, ok)
		}
	})

	t.Run("enter inside a fresh burst with pending records is dropped", func(t *testing.T) {
		// The batch is observed in this same read sequence and the clock never
		// advances, so the batch is fresh: an interior paste newline.
		ck, buf, _ := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec(0, vkReturn, 0), // interior paste newline
			keyRec('b', 0x42, 0),
			keyRec('c', 0x43, 0),
		})
		var got []Key
		for i := 0; i < 3; i++ {
			k, ok := readKeyWithin(t, ck, 2*time.Second)
			if !ok {
				t.Fatalf("read %d: ReadKey returned false; want a,b,c", i)
			}
			got = append(got, k)
		}
		if len(got) != 3 || got[0] != RuneKey('a') || got[1] != RuneKey('b') || got[2] != RuneKey('c') {
			t.Errorf("keys = %q; want a,b,c (the burst Enter dropped)", got)
		}
		if n := buf.consumedCount(); n != 4 {
			t.Errorf("consumed %d records; want 4 (the dropped Enter is still consumed)", n)
		}
	})

	t.Run("enter at the last index of a full window is delivered", func(t *testing.T) {
		// Reader level: ReadKey returns at the first accepted record, so the
		// fillers are consumed one at a time. In the delivered branch the Enter
		// is the last record of the queue, with nothing behind it; in the drop
		// branch a key press sits directly behind it, which is what makes it an
		// interior paste newline. That boundary is what this subtest pins, on
		// both sides. The true full 32-record window, where the Enter sits at
		// index 31 of a peek that is still full, cannot be reached through
		// ReadKey and is pinned directly at the scan level by the subtest below.
		full := func(enterLast bool) (recs []inputRecord, fillers int) {
			fillers = 31
			if !enterLast {
				fillers = 30
			}
			recs = make([]inputRecord, 0, 32)
			for i := 0; i < fillers; i++ {
				recs = append(recs, keyRec('x', 0x58, 0))
			}
			recs = append(recs, keyRec(0, vkReturn, 0))
			if !enterLast {
				// 'z', not 'y': lowercase y is the confirm pair, not a rune.
				recs = append(recs, keyRec('z', 0x5a, 0))
			}
			return recs, fillers
		}
		for _, enterLast := range []bool{true, false} {
			recs, fillers := full(enterLast)
			if len(recs) != 32 {
				t.Fatalf("enterLast=%v: window holds %d records; want a full 32", enterLast, len(recs))
			}
			ck, _, _ := newReader(recs)
			for i := 0; i < fillers; i++ {
				if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('x') {
					t.Fatalf("enterLast=%v filler %d: ReadKey = (%q,%v); want rune x", enterLast, i, k, ok)
				}
			}
			if enterLast {
				if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
					t.Fatalf("ReadKey = (%q,%v); want enter (last index of the window)", k, ok)
				}
				continue
			}
			// A key press sits directly behind the Enter in a fresh batch, so
			// the Enter is dropped and the queued 'z' comes out next.
			if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('z') {
				t.Fatalf("ReadKey = (%q,%v); want rune z (the fresh-burst Enter was dropped)", k, ok)
			}
		}
	})

	t.Run("enter after a human-spaced gap is delivered", func(t *testing.T) {
		// Records are pending behind the Enter, so only the timing keeps it:
		// 500 ms between keystrokes is a human pause, not a paste burst.
		ck, _, advance := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec(0, vkReturn, 0),
			keyRec('b', 0x42, 0),
		})
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('a') {
			t.Fatalf("ReadKey = (%q,%v); want rune a", k, ok)
		}
		advance(500 * time.Millisecond)
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
			t.Fatalf("ReadKey = (%q,%v); want enter after a human-spaced gap", k, ok)
		}
	})

	t.Run("backlog catch-up delivers a genuine enter", func(t *testing.T) {
		// The whole batch was queued before the reader woke up and the frame
		// then stalls with the rest of it still pending. The reader catches up
		// long after those records became observable, so the Enter's own gap is
		// loop turnaround while the batch behind it is old: a genuine Enter,
		// and it must be delivered even with records queued behind it.
		ck, _, advance := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec('b', 0x42, 0),
			keyRec(0, vkReturn, 0),
			keyRec('c', 0x43, 0),
		})
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('a') {
			t.Fatalf("ReadKey = (%q,%v); want rune a", k, ok)
		}
		advance(500 * time.Millisecond) // stalled frame, batch still queued
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('b') {
			t.Fatalf("ReadKey = (%q,%v); want rune b", k, ok)
		}
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
			t.Fatalf("ReadKey = (%q,%v); want enter (the pending batch is old, not a fresh burst)", k, ok)
		}
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('c') {
			t.Fatalf("ReadKey = (%q,%v); want rune c", k, ok)
		}
	})

	t.Run("stamp clears and re-arms across two batches", func(t *testing.T) {
		// A first burst is drained, a reader then parks on the EMPTY buffer
		// (that peek is what clears the stamp), and a second burst arrives
		// after a human pause. Its interior newline must be dropped again: the
		// reference re-armed with the new batch instead of keeping the first
		// batch's age, which would make the second Enter look human-spaced.
		ck, buf, advance := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec(0, vkReturn, 0),
			keyRec('b', 0x42, 0),
		})
		for _, want := range []Key{RuneKey('a'), RuneKey('b')} {
			if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != want {
				t.Fatalf("first batch: ReadKey = (%q,%v); want %q", k, ok, want)
			}
		}
		// Park a reader on the empty buffer so the stamp actually clears. Poll
		// for the empty peek rather than sleeping a fixed 60 ms: a loaded host
		// can delay the goroutine, and the wait is scheduling only, so it must
		// never gate the burst decision.
		emptyBefore := buf.emptyPeekCount()
		parked := make(chan Key, 1)
		go func() {
			if k, ok := ck.ReadKey(); ok {
				parked <- k
			}
		}()
		parkDeadline := time.Now().Add(2 * time.Second)
		for buf.emptyPeekCount() == emptyBefore && time.Now().Before(parkDeadline) {
			time.Sleep(time.Millisecond)
		}
		if buf.emptyPeekCount() == emptyBefore {
			buf.stop() // unwind the parked reader instead of leaving it spinning
			t.Fatal("the parked reader never observed the empty buffer")
		}
		advance(500 * time.Millisecond) // human pause between the two pastes
		buf.append(
			keyRec('c', 0x43, 0),
			keyRec(0, vkReturn, 0),
			keyRec('d', 0x44, 0),
		)
		select {
		case k, ok := <-parked:
			if !ok || k != RuneKey('c') {
				t.Fatalf("second batch first key = (%q,%v); want rune c", k, ok)
			}
		case <-time.After(2 * time.Second):
			buf.stop() // unwind the parked reader instead of leaving it spinning
			t.Fatal("the parked reader did not pick up the second batch")
		}
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('d') {
			t.Fatalf("ReadKey = (%q,%v); want rune d (the second batch's interior newline must be dropped again)", k, ok)
		}
	})

	t.Run("a focus or menu event behind the enter is not a queued key", func(t *testing.T) {
		// keyDownQueuedBehind must skip non-key events. These noise records
		// carry a non-zero payload at the keyDown offset, the way a real mouse
		// or menu record does, so a regression that drops the event-type guard
		// would count them as a queued key press and drop this genuine Enter.
		ck, _, _ := newReader([]inputRecord{
			keyRec('a', 0x41, 0),
			keyRec(0, vkReturn, 0),
			{eventType: 0x0010, keyDown: 1}, // focus event noise
			{eventType: 0x0008, keyDown: 1}, // menu event noise
			{eventType: 0x0002, keyDown: 1}, // mouse event noise
		})
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != RuneKey('a') {
			t.Fatalf("ReadKey = (%q,%v); want rune a", k, ok)
		}
		if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
			t.Fatalf("ReadKey = (%q,%v); want enter (focus, menu and mouse records are not queued keys)", k, ok)
		}
	})

	t.Run("scan evaluates an enter in the last slot of a full window", func(t *testing.T) {
		// ReadKey cannot reach this shape on its own: it returns at the first
		// accepted record, so by the time the Enter is at the head the peeked
		// window is no longer full. Drive scan directly with a full window of
		// noise and the Enter in the last slot, then with a key press in that
		// slot, to pin both sides of the boundary.
		window := func(lastIsEnter bool) []inputRecord {
			noise := 31
			if !lastIsEnter {
				noise = 30
			}
			recs := make([]inputRecord, 0, 32)
			for i := 0; i < noise; i++ {
				recs = append(recs, inputRecord{eventType: 0x0010}) // focus noise
			}
			recs = append(recs, keyRec(0, vkReturn, 0))
			if !lastIsEnter {
				recs = append(recs, keyRec('z', 0x5a, 0))
			}
			return recs
		}
		ck := &ConsoleKeys{in: &fakeConsoleBuffer{}, now: func() time.Time { return base }}
		ck.batchStamped = true
		ck.pendingSeenAt = base // fresh batch: only the pending check can save it

		full := window(true)
		if len(full) != 32 {
			t.Fatalf("window holds %d records; want a full 32", len(full))
		}
		consume, key, ok := ck.scan(full)
		if !ok || key != KeyEnter || consume != 32 {
			t.Fatalf("scan(full window, enter last) = (%d,%q,%v); want (32,enter,true)", consume, key, ok)
		}
		mixed := window(false)
		if len(mixed) != 32 {
			t.Fatalf("window holds %d records; want a full 32", len(mixed))
		}
		consume, key, ok = ck.scan(mixed)
		if !ok || key != "" || consume != 31 {
			t.Fatalf("scan(full window, enter then key) = (%d,%q,%v); want (31,\"\",true)", consume, key, ok)
		}
	})

	t.Run("held enter keeps its typematic expansion", func(t *testing.T) {
		// repeat > 1 is never debounced, so a held Enter still delivers every
		// repetition even with records queued behind it.
		ck, _, _ := newReader([]inputRecord{
			keyRepeatRec(0, vkReturn, 0, 3),
			keyRec('b', 0x42, 0),
		})
		for i := 0; i < 3; i++ {
			if k, ok := readKeyWithin(t, ck, 2*time.Second); !ok || k != KeyEnter {
				t.Fatalf("repeat %d: ReadKey = (%q,%v); want enter", i, k, ok)
			}
		}
	})
}

// TestStdinIsConsoleProbe pins the P4-5 entry-gate probe on every stdin shape a
// test process can produce without a console: a pipe, a redirected file, the
// NUL device, and no file at all. GetConsoleMode answers FALSE with
// ERROR_INVALID_HANDLE on all of them, so the probe must report false instead of
// claiming a console key source it does not have. The true branch needs a LIVE
// console attached to this process, which a headless run may not have; when
// CONIN$ opens, the probe's helper is asserted against a real console handle, and
// when it does not, the gap is reported instead of faked.
func TestStdinIsConsoleProbe(t *testing.T) {
	// No file at all: nothing to probe.
	if isConsoleHandle(nil) {
		t.Error("isConsoleHandle(nil) = true; a nil file has no handle")
	}

	// Piped stdin (`producer | agy pro setup`): an anonymous pipe handle.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	if isConsoleHandle(pr) {
		t.Error("isConsoleHandle(pipe read end) = true; a pipe is not a console")
	}

	// Redirected stdin (`agy pro setup < input.txt`): a plain file handle.
	f, err := os.CreateTemp(t.TempDir(), "stdin-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if isConsoleHandle(f) {
		t.Error("isConsoleHandle(file) = true; a redirected file is not a console")
	}

	// The NUL device (`agy pro setup < NUL`): a character device, still not a
	// console.
	nul, err := os.OpenFile("NUL", os.O_RDWR, 0)
	if err == nil {
		if isConsoleHandle(nul) {
			t.Error("isConsoleHandle(NUL) = true; the NUL device is not a console")
		}
		nul.Close()
	}

	// The exported probe targets os.Stdin whatever this environment's stdin
	// shape is (console runner: true, piped runner: false). It must answer a
	// bool without panicking.
	if got, want := StdinIsConsole(), isConsoleHandle(os.Stdin); got != want {
		t.Errorf("StdinIsConsole() = %v; want %v (the probe must target os.Stdin)", got, want)
	}

	// TRUE branch: CONIN$ opens the console input buffer independently of the
	// std handles, so a process that owns a console can assert it even with a
	// piped stdin.
	conin, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		t.Logf("no console reachable (%v): the TRUE branch of StdinIsConsole cannot be exercised in this run", err)
		return
	}
	defer conin.Close()
	if !isConsoleHandle(conin) {
		t.Error("isConsoleHandle(CONIN$) = false; a live console input buffer must answer true")
	}
}

// TestConsoleCtrlStateCleanAtEnd is the hygiene pin for the process-wide
// console control slot. The registration is process-wide, so a test that leaves
// it installed (or leaves recorded state behind) corrupts every test that runs
// after it. This is the last test of THIS file, and Go runs a package's test
// files in order with the tests inside a file in source order, so what it
// observes is the state every earlier test in this file left behind: the slot
// must be clean here.
func TestConsoleCtrlStateCleanAtEnd(t *testing.T) {
	consoleCtrlState.mu.Lock()
	defer consoleCtrlState.mu.Unlock()
	if consoleCtrlState.callback != 0 {
		t.Error("a console control handler is still registered: a test leaked the process-wide registration")
	}
	if consoleCtrlState.h != 0 || consoleCtrlState.outH != 0 {
		t.Error("the console control slot still carries a recorded handle")
	}
	if consoleCtrlState.restored || consoleCtrlState.outRestored {
		t.Error("a restore latch is still set")
	}
}
