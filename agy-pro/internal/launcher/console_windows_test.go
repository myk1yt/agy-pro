//go:build windows

package launcher

import (
	"sync"
	"testing"
	"time"
)

// fakeConsole is a scripted console input buffer: it serves a fixed record
// list and counts every consume call, so the decision-before-consume ordering
// (F-9) can be asserted without a live console on the user's screen.
type fakeConsole struct {
	mu           sync.Mutex
	records      []inputRecord
	consumed     int
	consumeCalls int
	// beforePeek runs before each peek answer; used to shape the race.
	beforePeek func()
}

func (f *fakeConsole) peek(n int) ([]inputRecord, bool) {
	f.mu.Lock()
	hook := f.beforePeek
	recs := f.records
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if n > len(recs) {
		n = len(recs)
	}
	out := make([]inputRecord, n)
	copy(out, recs[:n])
	return out, true
}

func (f *fakeConsole) consume(n int) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumeCalls++
	if n > len(f.records) {
		n = len(f.records)
	}
	f.records = f.records[n:]
	f.consumed += n
	return n, true
}

func (f *fakeConsole) stats() (consumed, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consumed, f.consumeCalls
}

func keyRec(char, vk uint16) inputRecord {
	return inputRecord{eventType: keyEventRecordType, keyDown: 1, char: char, virtualK: vk}
}

func noiseRec() inputRecord {
	return inputRecord{eventType: 0x0004} // focus/mouse noise, not a key event
}

// The chosen key (plus the noise queued before it) is consumed only after the
// sighting beat the deadline, and the trailing Enter is drained afterwards.
func TestReadConsoleChoiceFrom_ConsumesAfterDecision(t *testing.T) {
	fake := &fakeConsole{records: []inputRecord{
		noiseRec(),
		keyRec('2', 0x32),
		keyRec('\r', vkReturn),
	}}
	choice, status := readConsoleChoiceFrom(fake, time.Second)
	if status != consoleChoice || choice != "2" {
		t.Fatalf("got (%q, %v); want (\"2\", consoleChoice)", choice, status)
	}
	consumed, calls := fake.stats()
	if calls == 0 {
		t.Fatal("accepted sighting must consume the matched keystroke")
	}
	if consumed != 3 {
		t.Errorf("consumed %d records; want 3 (noise + choice + trailing enter)", consumed)
	}
	if len(fake.records) != 0 {
		t.Errorf("records left = %v; want none", fake.records)
	}
}

// F-9 regression: when the timeout wins the race against a keystroke that
// lands on the same tick, that keystroke must stay in the buffer for the real
// agy child. The old poller consumed the record inside the goroutine and lost
// it to the child.
func TestReadConsoleChoiceFrom_TimeoutNeverConsumesPendingKey(t *testing.T) {
	fake := &fakeConsole{records: []inputRecord{keyRec('2', 0x32)}}
	var once sync.Once
	fake.beforePeek = func() {
		once.Do(func() {
			// Deliver the sighting only after the deadline has passed.
			time.Sleep(60 * time.Millisecond)
		})
	}

	choice, status := readConsoleChoiceFrom(fake, 25*time.Millisecond)
	if status != consoleTimeout {
		t.Fatalf("status = %v; want consoleTimeout", status)
	}
	if choice != "" {
		t.Fatalf("choice = %q; want empty on timeout", choice)
	}
	consumed, calls := fake.stats()
	if calls != 0 || consumed != 0 {
		t.Fatalf("timeout path consumed %d records in %d calls; want none (child input loss)", consumed, calls)
	}
	if len(fake.records) != 1 {
		t.Fatalf("pending key was lost: %v", fake.records)
	}
}

// Same-tick race invariant, hammered: a run may end in either outcome
// (choice or timeout), but never in "timeout AND consumed".
func TestReadConsoleChoiceFrom_SameTickRaceInvariant(t *testing.T) {
	for i := 0; i < 10; i++ {
		fake := &fakeConsole{records: []inputRecord{keyRec('1', 0x31)}}
		_, status := readConsoleChoiceFrom(fake, 15*time.Millisecond)
		consumed, calls := fake.stats()
		if status == consoleTimeout && (calls != 0 || consumed != 0) {
			t.Fatalf("iteration %d: timeout won but %d records were consumed in %d calls", i, consumed, calls)
		}
		if status == consoleChoice && consumed == 0 {
			t.Fatalf("iteration %d: choice won but nothing was consumed", i)
		}
	}
}

// Keys that are not launcher input are never consumed: they stay queued for
// the child (or the next reader).
func TestReadConsoleChoiceFrom_NonChoiceKeysUntouched(t *testing.T) {
	fake := &fakeConsole{records: []inputRecord{keyRec('x', 0x58), keyRec(' ', 0x20)}}
	_, status := readConsoleChoiceFrom(fake, 40*time.Millisecond)
	if status != consoleTimeout {
		t.Fatalf("status = %v; want consoleTimeout for non-choice keys", status)
	}
	if consumed, calls := fake.stats(); consumed != 0 || calls != 0 {
		t.Fatalf("non-choice keys consumed: %d records in %d calls", consumed, calls)
	}
}

func TestDrainTrailingEnterFrom(t *testing.T) {
	fake := &fakeConsole{records: []inputRecord{
		keyRec('\r', vkReturn),
		keyRec('\n', vkReturn),
		keyRec('a', 0x41),
	}}
	start := time.Now()
	drainTrailingEnterFrom(fake, 150*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("drain blocked too long: %v", elapsed)
	}
	if len(fake.records) != 1 || fake.records[0].char != 'a' {
		t.Fatalf("drain must stop at the first non-Enter key; left %v", fake.records)
	}

	// Empty buffer: the drain waits out its settle window and consumes nothing.
	empty := &fakeConsole{}
	start = time.Now()
	drainTrailingEnterFrom(empty, 50*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("drain returned before the settle window: %v", elapsed)
	}
	if consumed, _ := empty.stats(); consumed != 0 {
		t.Fatalf("empty buffer consumed %d records", consumed)
	}
}

// readConsoleLineFrom accumulates multi-character selection input (digits,
// separators, presets), honors Backspace, and consumes the line's records only
// after Enter decides the race. A timeout consumes nothing.
func TestReadConsoleLineFrom_MultiDigitAccumulateAndConsume(t *testing.T) {
	recs := []inputRecord{
		keyRec('1', '1'),
		keyRec('1', '1'),
		keyRec('.', 0xbe),
		keyRec('1', '1'),
		keyRec('4', '4'),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice {
		t.Fatalf("status = %v, want consoleChoice", status)
	}
	if line != "11.14" {
		t.Errorf("line = %q, want 11.14", line)
	}
	if consumed, _ := fc.stats(); consumed != len(recs) {
		t.Errorf("consumed %d records, want all %d", consumed, len(recs))
	}
}

func TestReadConsoleLineFrom_BackspaceEdits(t *testing.T) {
	recs := []inputRecord{
		keyRec('9', '9'),
		keyRec(0, 0x08), // VK_BACK
		keyRec('3', '3'),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice || line != "3" {
		t.Fatalf("line/status = %q/%v, want 3/consoleChoice", line, status)
	}
}

func TestReadConsoleLineFrom_TimeoutConsumesNothing(t *testing.T) {
	recs := []inputRecord{keyRec('7', '7')} // no Enter yet
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 120*time.Millisecond)
	if status != consoleTimeout || line != "" {
		t.Fatalf("line/status = %q/%v, want empty/consoleTimeout", line, status)
	}
	if consumed, _ := fc.stats(); consumed != 0 {
		t.Errorf("timeout consumed %d records; the child must still see them", consumed)
	}
}

// Fail-without-fix pin (gate round 1, F-01): a human types across many 15ms
// ticks while the poller never consumes, so each still-queued record is
// re returned by every later peek. The reader must fold each record exactly
// once (watermark), or "11" arrives as "1111111111" and the picker degrades
// to the bridge fallback. The one-batch pins above cannot catch this because
// they serve the whole line before the first tick.
func TestReadConsoleLineFrom_SlowTypingFoldsEachRecordOnce(t *testing.T) {
	fc := &fakeConsole{}
	queue := []inputRecord{keyRec('1', '1'), keyRec('1', '1'), keyRec('\r', 0x0d)}
	calls := 0
	fc.beforePeek = func() {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		if len(queue) > 0 && calls%3 == 0 { // one key every ~3 ticks
			fc.records = append(fc.records, queue[0])
			queue = queue[1:]
		}
		calls++
	}
	line, status := readConsoleLineFrom(fc, 3*time.Second)
	if status != consoleChoice {
		t.Fatalf("status = %v, want consoleChoice", status)
	}
	if line != "11" {
		t.Fatalf("line = %q, want 11 (each record folded exactly once across ticks)", line)
	}
}

// Backspace across ticks edits the accumulated line, not a per-tick snapshot.
func TestReadConsoleLineFrom_SlowTypingWithBackspace(t *testing.T) {
	fc := &fakeConsole{}
	queue := []inputRecord{keyRec('9', '9'), keyRec('9', '9'), keyRec(0, 0x08), keyRec('3', '3'), keyRec('\r', 0x0d)}
	calls := 0
	fc.beforePeek = func() {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		if len(queue) > 0 && calls%2 == 0 {
			fc.records = append(fc.records, queue[0])
			queue = queue[1:]
		}
		calls++
	}
	line, status := readConsoleLineFrom(fc, 3*time.Second)
	if status != consoleChoice || line != "93" {
		t.Fatalf("line/status = %q/%v, want 93/consoleChoice", line, status)
	}
}

// A long selection (key-down plus key-up records) must stay inside the peek
// window so the Enter is sighted instead of timing out (gate round 1, F-02).
func TestReadConsoleLineFrom_LongLineWithinWindow(t *testing.T) {
	var recs []inputRecord
	for _, c := range "1,3-5,8,11-14" {
		recs = append(recs, keyRec(uint16(c), uint16(c)))
		recs = append(recs, inputRecord{eventType: keyEventRecordType, keyDown: 0, char: uint16(c), virtualK: uint16(c)})
	}
	recs = append(recs, keyRec('\r', 0x0d))
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice {
		t.Fatalf("status = %v, want consoleChoice (Enter must be inside the peek window)", status)
	}
	if line != "1,3-5,8,11-14" {
		t.Errorf("line = %q, want 1,3-5,8,11-14", line)
	}
}

// Fail-without-fix pin (gate round 3, F-R2-03): the console reader accumulates
// every printable key, so a line the selection grammar rejects ("1/2") arrives
// whole and fails with the grammar message instead of silently dropping the
// stray character and picking seat 12.
func TestReadConsoleLineFrom_StrayCharsStayInLine(t *testing.T) {
	recs := []inputRecord{
		keyRec('1', '1'),
		keyRec('/', 0xBF),
		keyRec('2', '2'),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice || line != "1/2" {
		t.Fatalf("line/status = %q/%v, want 1/2/consoleChoice", line, status)
	}
}

// Non-ASCII printable keys (Korean/CJK prompt text) must stay in the line: the
// reader is now also the [3] free-text swarm prompt path, and dropping them
// silently cancels a Korean prompt as "empty".
func TestReadConsoleLineFrom_NonASCIIKeysStayInLine(t *testing.T) {
	recs := []inputRecord{
		keyRec(0xD55C, 0xBD),
		keyRec(0xAC70, 0xBC),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice {
		t.Fatalf("status = %v, want consoleChoice", status)
	}
	if line != "\uD55C\uAC70" {
		t.Errorf("line = %q, want the Korean text intact", line)
	}
}

// Fail-without-fix pin (gate round 3, F-R2-02): the fixture must exceed the
// OLD 32-record peek window so a peek(1024)->peek(32) regression fails here.
// 300 keys with key-ups is 601 records; the Enter at index 600 is invisible
// under a 32-record window, so the pin only passes while the window is wide.
func TestReadConsoleLineFrom_LineBeyondOldWindow(t *testing.T) {
	var recs []inputRecord
	for i := 0; i < 300; i++ {
		c := uint16('0' + i%10)
		recs = append(recs, keyRec(c, c), inputRecord{eventType: keyEventRecordType, keyDown: 0, char: c, virtualK: c})
	}
	recs = append(recs, keyRec('\r', 0x0d))
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 3*time.Second)
	if status != consoleChoice {
		t.Fatalf("status = %v, want consoleChoice (Enter must be inside the peek window)", status)
	}
	if len(line) != 300 {
		t.Errorf("line length = %d, want 300", len(line))
	}
}

// Fail-without-fix pin (gate round 4, F-R4-03): a held key queues ONE record
// with repeat=N. Ignoring repeat made a held "1" (for seat "11") silently
// pick seat 1, because the record folded as a single character.
func TestReadConsoleLineFrom_HeldKeyRepeats(t *testing.T) {
	recs := []inputRecord{
		{eventType: keyEventRecordType, keyDown: 1, repeat: 3, char: '1', virtualK: '1'},
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice || line != "111" {
		t.Fatalf("line/status = %q/%v, want 111/consoleChoice (repeat=3)", line, status)
	}
}

// Non-BMP text survives: a high surrogate followed by its low surrogate folds
// into ONE rune instead of two U+FFFD replacements.
func TestReadConsoleLineFrom_SurrogatePairFolds(t *testing.T) {
	recs := []inputRecord{
		keyRec(0xD83D, 0xBA),
		keyRec(0xDE00, 0xBC),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice {
		t.Fatalf("status = %v, want consoleChoice", status)
	}
	if line != "\U0001F600" {
		t.Errorf("line = %q, want the emoji intact", line)
	}
}

// Control keys are editing keys, not text: Tab must not enter the line.
func TestReadConsoleLineFrom_ControlKeysStayOut(t *testing.T) {
	recs := []inputRecord{
		keyRec('1', '1'),
		keyRec(0x09, 0x09),
		keyRec('3', '3'),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice || line != "13" {
		t.Fatalf("line/status = %q/%v, want 13/consoleChoice (Tab excluded)", line, status)
	}
}

// A held Backspace erases repeat characters (the mirror of the text-key
// typematic fold); a single Backspace still erases exactly one.
func TestReadConsoleLineFrom_HeldBackspaceRepeats(t *testing.T) {
	recs := []inputRecord{
		keyRec('9', '9'),
		keyRec('9', '9'),
		keyRec('9', '9'),
		{eventType: keyEventRecordType, keyDown: 1, repeat: 2, virtualK: 0x08},
		keyRec('3', '3'),
		keyRec('\r', 0x0d),
	}
	fc := &fakeConsole{records: recs}
	line, status := readConsoleLineFrom(fc, 2*time.Second)
	if status != consoleChoice || line != "93" {
		t.Fatalf("line/status = %q/%v, want 93/consoleChoice (held Backspace erased 2)", line, status)
	}
}
