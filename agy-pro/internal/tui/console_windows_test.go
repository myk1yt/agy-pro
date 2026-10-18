//go:build windows

package tui

import (
	"sync"
	"testing"
	"time"
	"unsafe"
)

// fakeConsoleBuffer is the peek-then-consume seam: tests script records,
// observe exactly how many were consumed, and can append records while a
// reader goroutine is blocked (mutex-guarded).
type fakeConsoleBuffer struct {
	mu       sync.Mutex
	records  []inputRecord
	consumed int
	failPeek bool
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

func keyRec(char, vk uint16, state uint32) inputRecord {
	return inputRecord{eventType: keyEventRecordType, keyDown: 1, char: char, virtualK: vk, state: state}
}

func keyRepeatRec(char, vk uint16, state uint32, repeat uint16) inputRecord {
	rec := keyRec(char, vk, state)
	rec.repeat = repeat
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

	if !registerConsoleCtrlHandler(fakeHandle, enableEchoInput|enableLineInput) {
		t.Fatal("SetConsoleCtrlHandler refused registration")
	}
	if consoleCtrlState.callback == 0 {
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
