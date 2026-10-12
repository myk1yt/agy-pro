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
