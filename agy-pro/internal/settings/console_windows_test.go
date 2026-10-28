//go:build windows

package settings

import (
	"sync"
	"testing"
	"time"
)

// fakeConsoleBuffer is the race-testable consoleBuffer seam (contract §2.9,
// pattern console_windows.go:45-51 in internal/launcher). It is mutex-guarded
// so a pin can observe the peek rate and stop or break the buffer while a
// reader goroutine is blocked inside readKeyFrom.
type fakeConsoleBuffer struct {
	mu          sync.Mutex
	records     []inputRecord
	consumed    int
	peeks       int
	failPeek    bool
	failConsume bool
}

func (f *fakeConsoleBuffer) peek(n int) ([]inputRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peeks++
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
	if f.failConsume {
		return 0, false
	}
	if n > len(f.records) {
		n = len(f.records)
	}
	f.records = f.records[n:]
	f.consumed += n
	return n, true
}

// consumedCount reports how many records have left the buffer.
func (f *fakeConsoleBuffer) consumedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.consumed
}

// peekCount reports how many peek calls the buffer has seen, which is how a
// backoff pin tells a paced wait from a busy-spin.
func (f *fakeConsoleBuffer) peekCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peeks
}

// stop makes every later peek fail so a reader blocked in readKeyFrom returns
// and a pin can finish after observing a wait.
func (f *fakeConsoleBuffer) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPeek = true
}

func keyRec(char, vk uint16, state uint32) inputRecord {
	return inputRecord{eventType: keyEventRecordType, keyDown: 1, char: char, virtualK: vk, state: state}
}

// noiseRecs builds n pure-noise records (mouse/focus/menu class): records this
// reader never accepts and must either drain (full window) or wait on
// (partial window).
func noiseRecs(n int) []inputRecord {
	recs := make([]inputRecord, n)
	for i := range recs {
		recs[i] = inputRecord{eventType: 0x0010} // focus event noise
	}
	return recs
}

// readKeyWithin runs readKeyFrom under a deadline so a livelock or busy-spin
// regression fails the pin fast instead of hanging the package for the whole
// go test timeout. On timeout it stops the fake buffer first, so the reader
// goroutine unwinds instead of pegging a core for the rest of the run.
func readKeyWithin(t *testing.T, buf *fakeConsoleBuffer, d time.Duration) (Key, bool) {
	t.Helper()
	type outcome struct {
		key Key
		ok  bool
	}
	done := make(chan outcome, 1)
	go func() {
		k, ok := readKeyFrom(buf)
		done <- outcome{k, ok}
	}()
	select {
	case o := <-done:
		return o.key, o.ok
	case <-time.After(d):
		buf.stop()
		t.Fatalf("readKeyFrom blocked longer than %v (livelock or busy-spin regression)", d)
		return "", false
	}
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
	k, ok := readKeyWithin(t, buf, 2*time.Second)
	if !ok || k != KeyDown {
		t.Fatalf("readKeyFrom = (%q,%v); want down", k, ok)
	}
	if got := buf.consumedCount(); got != 2 {
		t.Errorf("consumed %d records; want 2 (noise + key)", got)
	}
	k, ok = readKeyWithin(t, buf, 2*time.Second)
	if !ok || k != KeyEnter {
		t.Fatalf("second readKeyFrom = (%q,%v); want enter", k, ok)
	}
}

func TestReadKeyFrom_SkipsUnmapped(t *testing.T) {
	buf := &fakeConsoleBuffer{records: []inputRecord{
		keyRec(0, 0x70, 0), // F1, not mapped in the settings set
		keyRec('5', 0x35, 0),
	}}
	k, ok := readKeyWithin(t, buf, 2*time.Second)
	if !ok || k != Key('5') {
		t.Fatalf("readKeyFrom = (%q,%v); want digit 5", k, ok)
	}
}

func TestReadKeyFrom_FullNoiseWindowDrains(t *testing.T) {
	// A full peek window of pure noise (mouse/focus/menu records, key-ups)
	// must be consumed so the FIFO head advances; the key queued behind it
	// then becomes visible. Records are atomic and FIFO, so draining noise
	// can never lose a key.
	recs := noiseRecs(32)
	recs = append(recs, keyRec(0, vkReturn, 0))
	buf := &fakeConsoleBuffer{records: recs}
	k, ok := readKeyWithin(t, buf, 2*time.Second)
	if !ok || k != KeyEnter {
		t.Fatalf("readKeyFrom = (%q,%v); want enter", k, ok)
	}
	if got := buf.consumedCount(); got != 33 {
		t.Errorf("consumed %d records; want 33 (32 noise + key)", got)
	}
}

func TestReadKeyFrom_PartialNoiseWindowBackoff(t *testing.T) {
	// A partial pure-noise window hides no key, so the records stay queued
	// (they ride with the next accepted sighting, peek-then-consume) while
	// the reader waits idleBackoff between peeks. Without the backoff the
	// reader re-peeks the same records in a tight loop and pegs a core while
	// the panel idles (B1).
	buf := &fakeConsoleBuffer{records: noiseRecs(3)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = readKeyFrom(buf) // returns as soon as stop() fails the peeks
	}()
	start := time.Now()
	time.Sleep(100 * time.Millisecond)
	elapsed := time.Since(start)
	peeks := buf.peekCount()
	buf.stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readKeyFrom did not return after peek failure (busy-spin or hang regression)")
	}
	// A paced waiter peeks about once per idleBackoff; the bound allows 2x
	// that rate plus slack for scheduler jitter. The pre-fix busy-spin peeked
	// hundreds of thousands of times in the same window.
	maxPeeks := 2*int(elapsed/idleBackoff) + 4
	if peeks > maxPeeks {
		t.Errorf("peeked %d times in %v; want <= %d (backoff missing, busy-spin)", peeks, elapsed, maxPeeks)
	}
	if got := buf.consumedCount(); got != 0 {
		t.Errorf("consumed %d records while waiting; want 0 (peek-then-consume contract)", got)
	}
}

func TestReadKeyFrom_ConsumeFailureFailsRead(t *testing.T) {
	// A consume that cannot move records must fail the read at every consume
	// site: re-peeking the same records would spin (unmapped drop, noise
	// drain) and reporting a sighting whose record is still queued would
	// deliver the same key twice (accepted key).
	cases := []struct {
		name string
		recs []inputRecord
	}{
		{"unmapped drop", []inputRecord{keyRec(0, 0x70, 0)}},
		{"accepted sighting", []inputRecord{keyRec(0, vkDown, 0)}},
		{"full noise window", noiseRecs(32)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf := &fakeConsoleBuffer{records: c.recs, failConsume: true}
			k, ok := readKeyWithin(t, buf, 2*time.Second)
			if ok || k != "" {
				t.Fatalf("readKeyFrom = (%q,%v); want (\"\",false) when consume fails", k, ok)
			}
		})
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
