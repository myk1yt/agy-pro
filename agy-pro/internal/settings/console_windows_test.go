//go:build windows

package settings

import "testing"

// fakeConsoleBuffer is the race-testable consoleBuffer seam (contract §2.9,
// pattern console_windows.go:45-51 in internal/launcher).
type fakeConsoleBuffer struct {
	records []inputRecord
	consumed int
}

func (f *fakeConsoleBuffer) peek(n int) ([]inputRecord, bool) {
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
	if n > len(f.records) {
		n = len(f.records)
	}
	f.records = f.records[n:]
	f.consumed += n
	return n, true
}

func keyRec(char, vk uint16, state uint32) inputRecord {
	return inputRecord{eventType: keyEventRecordType, keyDown: 1, char: char, virtualK: vk, state: state}
}

func TestMapConsoleKey_ExtendedSet(t *testing.T) {
	cases := []struct {
		char, vk uint16
		state    uint32
		want     Key
	}{
		{0, vkUp, 0, KeyUp},
		{0, vkDown, 0, KeyDown},
		{0, vkUp, shiftMask, KeyPgUp},
		{0, vkDown, shiftMask, KeyPgDn},
		{0, vkPrior, 0, KeyPgUp},
		{0, vkNext, 0, KeyPgDn},
		{0, vkLeft, 0, KeyLeft},
		{0, vkRight, 0, KeyRight},
		{0, vkEscape, 0, KeyEsc},
		{0, vkTab, 0, KeyTab},
		{0, vkReturn, 0, KeyEnter},
		{'3', 0x33, 0, Key('3')},
		{'y', 0x59, 0, KeyYes},
		{'n', 0x4e, 0, KeyNo},
		{0x03, 0, 0, KeyCtrlC},
		{0x0c, 0, 0, KeyCtrlL},
	}
	for _, c := range cases {
		got, ok := mapConsoleKey(c.char, c.vk, c.state)
		if !ok || got != c.want {
			t.Errorf("mapConsoleKey(%#x,%#x,%#x) = (%q,%v); want %q", c.char, c.vk, c.state, got, ok, c.want)
		}
	}
	if _, ok := mapConsoleKey(0, 0, 0); ok {
		t.Error("unmapped event must not produce a key")
	}
}

func TestReadKeyFrom_PeekThenConsume(t *testing.T) {
	// Noise (focus/unknown) then an arrow key: the poller must consume the
	// noise together with the sighting and return the mapped key.
	buf := &fakeConsoleBuffer{records: []inputRecord{
		{eventType: 0x0010}, // focus event noise
		keyRec(0, vkDown, 0),
		keyRec(0, vkReturn, 0),
	}}
	k, ok := readKeyFrom(buf)
	if !ok || k != KeyDown {
		t.Fatalf("readKeyFrom = (%q,%v); want down", k, ok)
	}
	if buf.consumed != 2 {
		t.Errorf("consumed %d records; want 2 (noise + key)", buf.consumed)
	}
	k, ok = readKeyFrom(buf)
	if !ok || k != KeyEnter {
		t.Fatalf("second readKeyFrom = (%q,%v); want enter", k, ok)
	}
}

func TestReadKeyFrom_SkipsUnmapped(t *testing.T) {
	buf := &fakeConsoleBuffer{records: []inputRecord{
		keyRec(0, 0x70, 0), // F1 — not mapped in the settings set
		keyRec('5', 0x35, 0),
	}}
	k, ok := readKeyFrom(buf)
	if !ok || k != Key('5') {
		t.Fatalf("readKeyFrom = (%q,%v); want digit 5", k, ok)
	}
}

func TestDigitKeys(t *testing.T) {
	if Digit(1) != Key('1') || Digit(9) != Key('9') {
		t.Errorf("Digit mapping broken: %q %q", Digit(1), Digit(9))
	}
	if Digit(10) != "" || Digit(-1) != "" {
		t.Error("Digit out of range must be empty")
	}
	if idx, ok := digitIndex(Key('4')); !ok || idx != 3 {
		t.Errorf("digitIndex('4') = (%d,%v); want (3,true)", idx, ok)
	}
}
