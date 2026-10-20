package tui

import (
	"errors"
	"strings"
	"testing"
)

func nameForm() *Form {
	return NewForm([]Field{
		{Label: "Name", Validate: func(s string) error {
			if s == "" {
				return errors.New("name required")
			}
			return nil
		}},
		{Label: "Family"},
	})
}

func typeText(f *Form, s string) {
	for _, r := range s {
		f.HandleKey(RuneKey(r))
	}
}

func TestFormAdvanceAndBackfield(t *testing.T) {
	f := nameForm()
	f.HandleKey(KeyTab)
	if f.Cursor() != 1 {
		t.Fatalf("Tab cursor = %d, want 1", f.Cursor())
	}
	f.HandleKey(KeyDown) // already last: clamps
	if f.Cursor() != 1 {
		t.Fatalf("Down at last field cursor = %d, want 1", f.Cursor())
	}
	f.HandleKey(KeyUp) // backfield
	if f.Cursor() != 0 {
		t.Fatalf("Up cursor = %d, want 0", f.Cursor())
	}
	f.SetCursor(99) // clamp
	if f.Cursor() != 1 {
		t.Fatalf("SetCursor clamp = %d, want 1", f.Cursor())
	}
}

func TestFormTypingIsTextInEdit(t *testing.T) {
	f := nameForm()
	// Digits and y/n are text here even though they select/confirm in NAV.
	f.HandleKey(Digit(3))
	f.HandleKey(KeyYes)
	f.HandleKey(KeyNo)
	f.HandleKey(KeySpace)
	if got := f.Value(0); got != "3yn " {
		t.Fatalf("value = %q, want %q", got, "3yn ")
	}
	if !f.Dirty() {
		t.Fatal("form not dirty after edits")
	}
	f.HandleKey(KeyBackspace)
	if got := f.Value(0); got != "3yn" {
		t.Fatalf("value after Backspace = %q, want %q", got, "3yn")
	}
}

func TestFormValidationBlocksAdvance(t *testing.T) {
	f := nameForm()
	typeText(f, "web")
	for i := 0; i < 3; i++ {
		f.HandleKey(KeyBackspace)
	}
	if f.Value(0) != "" {
		t.Fatalf("value after wiping = %q, want empty", f.Value(0))
	}
	f.HandleKey(KeyEnter) // invalid: empty name
	if got := f.Message(); got != "name required" {
		t.Fatalf("message = %q, want %q", got, "name required")
	}
	if f.Cursor() != 0 {
		t.Fatalf("cursor moved past an invalid field: %d", f.Cursor())
	}
	if f.Submitted() {
		t.Fatal("form submitted with an invalid field")
	}
	typeText(f, "web")
	f.HandleKey(KeyEnter) // valid: advance to field 2
	if f.Cursor() != 1 || f.Message() != "" {
		t.Fatalf("advance: cursor %d message %q, want 1 and empty", f.Cursor(), f.Message())
	}
	f.HandleKey(KeyEnter) // valid last field: submit
	if !f.Submitted() {
		t.Fatal("Enter on the last valid field did not submit")
	}
	if got := f.Value(0); got != "web" {
		t.Fatalf("field value = %q, want %q", got, "web")
	}
}

func TestFormDirtySnapshotRestore(t *testing.T) {
	f := nameForm()
	typeText(f, "web")
	snap := f.Snapshot()
	if snap[0] != "web" {
		t.Fatalf("snapshot = %v, want [web]", snap)
	}
	typeText(f, "-v2")
	if got := f.Value(0); got != "web-v2" {
		t.Fatalf("edited value = %q, want %q", got, "web-v2")
	}
	f.Restore(snap)
	if got := f.Value(0); got != "web" {
		t.Fatalf("restored value = %q, want %q", got, "web")
	}
	if f.Dirty() {
		t.Fatal("form still dirty after Restore (discard-confirm needs clean)")
	}
	f.MarkClean()
	typeText(f, "x")
	if !f.Dirty() {
		t.Fatal("form not dirty after edits past MarkClean")
	}
}

// TestFormFieldCap pins the rune cap: typing past maxFieldRunes stops with
// the limit notice and changes nothing, while a seeded long value still
// renders, and deletion plus snapshot/restore keep working on it.
func TestFormFieldCap(t *testing.T) {
	f := NewForm([]Field{{Label: "Name"}})
	for i := 0; i < maxFieldRunes+8; i++ {
		f.HandleKey(RuneKey('x'))
	}
	if got := len([]rune(f.Value(0))); got != maxFieldRunes {
		t.Fatalf("typed length = %d, want the cap %d", got, maxFieldRunes)
	}
	if f.Message() != formLimitMsg {
		t.Fatalf("cap message = %q, want %q", f.Message(), formLimitMsg)
	}
	if !f.Dirty() {
		t.Fatal("form not dirty after capped typing")
	}

	// A pre-seeded long value bypasses the cap by design (seeding is program
	// state, not rune insertion): it renders, refuses further runes, and
	// still edits down.
	long := strings.Repeat("y", maxFieldRunes+50)
	f2 := NewForm([]Field{{Label: "Name", Value: long}})
	rows := f2.Render(len(long) + 20)
	if !strings.Contains(rows[0], long) {
		t.Fatalf("seeded long value did not render: %q...", rows[0][:40])
	}
	f2.HandleKey(RuneKey('z'))
	if got := f2.Value(0); got != long {
		t.Fatalf("insert into seeded long value changed it: %d runes", len([]rune(got)))
	}
	if f2.Message() != formLimitMsg {
		t.Fatalf("cap message = %q, want %q", f2.Message(), formLimitMsg)
	}
	f2.HandleKey(KeyBackspace)
	if got := len([]rune(f2.Value(0))); got != maxFieldRunes+49 {
		t.Fatalf("backspace on seeded long value = %d runes, want %d", got, maxFieldRunes+49)
	}
	snap := f2.Snapshot()
	if snap[0] != f2.Value(0) {
		t.Fatalf("snapshot drifted from the live value")
	}
	f2.Restore(snap)
	if f2.Dirty() {
		t.Fatal("form still dirty after Restore")
	}
}

func TestFormRender(t *testing.T) {
	// The CJK label is 6 cells (한국어), "Name" 4: the grid aligns by cells.
	f := NewForm([]Field{{Label: "한국어", Value: "kim"}, {Label: "Name", Value: "lee"}})
	got := f.Render(60)
	want0 := "▶ 한국어 : kim"
	want1 := "  Name   : lee"
	if got[0] != want0 {
		t.Fatalf("row 0 = %q, want %q", got[0], want0)
	}
	if got[1] != want1 {
		t.Fatalf("row 1 = %q, want %q", got[1], want1)
	}
}

// moveCaret puts the focused field's caret at rune index n by homing and
// stepping right; the values in these pins stay short enough for that to be
// the exact index, and a larger n clamps at the value end.
func moveCaret(f *Form, n int) {
	f.HandleKey(KeyHome)
	for i := 0; i < n; i++ {
		f.HandleKey(KeyRight)
	}
}

// TestFormCaretStartsAtValueEnd pins the construction rule: every field
// carries a caret, seeded values start at their end (so first-focus typing
// appends, the pre-caret behavior), and empty values start at 0.
func TestFormCaretStartsAtValueEnd(t *testing.T) {
	f := NewForm([]Field{
		{Label: "Name", Value: "web"},
		{Label: "Family", Value: "한국어"},
		{Label: "Tags"},
	})
	for i, want := range []int{3, 3, 0} {
		if got := f.FieldCaret(i); got != want {
			t.Fatalf("field %d caret = %d, want %d", i, got, want)
		}
	}
	if got := f.FieldCaretCell(0); got != 3 {
		t.Fatalf("latin caret cell = %d, want 3", got)
	}
	if got := f.FieldCaretCell(1); got != 6 {
		t.Fatalf("CJK caret cell = %d, want 6", got)
	}
	if got := f.FieldCaretCell(2); got != 0 {
		t.Fatalf("empty caret cell = %d, want 0", got)
	}
}

// TestFormCaretLeftRight pins one-rune movement with clamping at both ends
// and no wrap across fields.
func TestFormCaretLeftRight(t *testing.T) {
	f := NewForm([]Field{{Label: "A", Value: "abc"}, {Label: "B", Value: "de"}})
	steps := []struct {
		key  Key
		want int
	}{
		{KeyLeft, 2},
		{KeyLeft, 1},
		{KeyLeft, 0},
		{KeyLeft, 0},
		{KeyRight, 1},
		{KeyRight, 2},
		{KeyRight, 3},
		{KeyRight, 3},
	}
	for _, s := range steps {
		f.HandleKey(s.key)
		if got := f.FieldCaret(0); got != s.want {
			t.Fatalf("after %q caret = %d, want %d", s.key, got, s.want)
		}
		if f.Cursor() != 0 {
			t.Fatalf("after %q cursor = %d, want 0 (movement never wraps fields)", s.key, f.Cursor())
		}
	}
}

// TestFormCaretHomeEnd pins Home/End: they jump to the value start and end,
// and the end index counts runes, not bytes or cells (CJK 3 runes = 6 cells).
func TestFormCaretHomeEnd(t *testing.T) {
	f := NewForm([]Field{{Label: "Name", Value: "한국어"}})
	f.HandleKey(KeyHome)
	if got := f.FieldCaret(0); got != 0 {
		t.Fatalf("Home caret = %d, want 0", got)
	}
	if got := f.FieldCaretCell(0); got != 0 {
		t.Fatalf("Home caret cell = %d, want 0", got)
	}
	f.HandleKey(KeyEnd)
	if got := f.FieldCaret(0); got != 3 {
		t.Fatalf("End caret = %d, want 3 runes", got)
	}
	if got := f.FieldCaretCell(0); got != 6 {
		t.Fatalf("End caret cell = %d, want 6 cells", got)
	}
	f.HandleKey(KeyHome)
	f.HandleKey(KeyRight)
	if got := f.FieldCaretCell(0); got != 2 {
		t.Fatalf("one-rune Right across CJK = %d cells, want 2", got)
	}
}

// TestFormInsertAtCaret pins insert-at-caret: a rune typed with the caret
// inside the value lands at the caret, not at the end, and the caret
// advances past it. Typing with the caret at the end still appends, which is
// the pre-caret behavior, so no existing append pin had to move.
func TestFormInsertAtCaret(t *testing.T) {
	f := NewForm([]Field{{Label: "Name", Value: "web"}})
	f.HandleKey(KeyHome)
	f.HandleKey(KeyRight) // caret 1: between w and e
	typeText(f, "X")
	if got := f.Value(0); got != "wXeb" {
		t.Fatalf("insert at caret = %q, want %q", got, "wXeb")
	}
	if got := f.FieldCaret(0); got != 2 {
		t.Fatalf("caret after insert = %d, want 2", got)
	}
	typeText(f, "12") // a burst keeps inserting at the advancing caret
	if got := f.Value(0); got != "wX12eb" {
		t.Fatalf("burst insert = %q, want %q", got, "wX12eb")
	}
	if got := f.FieldCaret(0); got != 4 {
		t.Fatalf("caret after burst = %d, want 4", got)
	}
	f.HandleKey(KeyEnd)
	typeText(f, "!")
	if got := f.Value(0); got != "wX12eb!" {
		t.Fatalf("append at end caret = %q, want %q", got, "wX12eb!")
	}
	if !f.Dirty() {
		t.Fatal("form not dirty after insert-at-caret")
	}
}

// TestFormDeleteBackspaceAtCaret pins forward delete and caret-aware
// backspace at the start, middle, and end of single- and multi-byte values.
// A deletion that removes nothing leaves the value, the caret, and the dirty
// latch alone.
func TestFormDeleteBackspaceAtCaret(t *testing.T) {
	tests := []struct {
		name      string
		key       Key
		value     string
		caret     int
		want      string
		wantCaret int
		wantDirty bool
	}{
		{"delete at start", KeyDelete, "abc", 0, "bc", 0, true},
		{"delete in middle", KeyDelete, "abc", 1, "ac", 1, true},
		{"delete at end is a no-op", KeyDelete, "abc", 3, "abc", 3, false},
		{"delete in empty value is a no-op", KeyDelete, "", 0, "", 0, false},
		{"delete CJK in middle", KeyDelete, "한국어", 1, "한어", 1, true},
		{"backspace at start is a no-op", KeyBackspace, "abc", 0, "abc", 0, false},
		{"backspace in middle", KeyBackspace, "abc", 1, "bc", 0, true},
		{"backspace at end", KeyBackspace, "abc", 3, "ab", 2, true},
		{"backspace in empty value is a no-op", KeyBackspace, "", 0, "", 0, false},
		{"backspace CJK at end", KeyBackspace, "한국어", 3, "한국", 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := NewForm([]Field{{Label: "Name", Value: tc.value}})
			moveCaret(f, tc.caret)
			f.HandleKey(tc.key)
			if got := f.Value(0); got != tc.want {
				t.Fatalf("value = %q, want %q", got, tc.want)
			}
			if got := f.FieldCaret(0); got != tc.wantCaret {
				t.Fatalf("caret = %d, want %d", got, tc.wantCaret)
			}
			if got := f.Dirty(); got != tc.wantDirty {
				t.Fatalf("dirty = %v, want %v", got, tc.wantDirty)
			}
		})
	}
}

// TestFormInsertDeleteMultiByte pins CJK editing at the caret: one rune
// index per CJK rune (two cells), so insert, forward delete, and backspace
// each move a whole glyph and the caret cell math follows.
func TestFormInsertDeleteMultiByte(t *testing.T) {
	f := NewForm([]Field{{Label: "Name", Value: "ab"}})
	f.HandleKey(KeyHome)
	typeText(f, "한")
	if got := f.Value(0); got != "한ab" {
		t.Fatalf("CJK insert = %q, want %q", got, "한ab")
	}
	if got := f.FieldCaret(0); got != 1 {
		t.Fatalf("CJK insert caret = %d, want 1", got)
	}
	if got := f.FieldCaretCell(0); got != 2 {
		t.Fatalf("CJK insert caret cell = %d, want 2", got)
	}
	f.HandleKey(KeyDelete) // removes the rune at the caret: 'a'
	if got := f.Value(0); got != "한b" {
		t.Fatalf("delete next to CJK = %q, want %q", got, "한b")
	}
	if got := f.FieldCaret(0); got != 1 {
		t.Fatalf("delete caret = %d, want 1", got)
	}
	f.HandleKey(KeyEnd)
	f.HandleKey(KeyBackspace) // removes 'b'
	if got := f.Value(0); got != "한" {
		t.Fatalf("backspace = %q, want %q", got, "한")
	}
	f.HandleKey(KeyBackspace) // removes the CJK rune in one step
	if got := f.Value(0); got != "" {
		t.Fatalf("CJK backspace = %q, want empty", got)
	}
	if got := f.FieldCaret(0); got != 0 {
		t.Fatalf("CJK backspace caret = %d, want 0", got)
	}
}

// TestFormFieldSwitchKeepsCaret pins the field-switch rule: the caret is
// remembered per field across focus changes, a field visited for the first
// time keeps the end-of-value caret it was built with, and focus moves
// (Tab/Up/Down/SetCursor) never reset a caret.
func TestFormFieldSwitchKeepsCaret(t *testing.T) {
	f := NewForm([]Field{{Label: "A", Value: "abc"}, {Label: "B", Value: "seed"}})
	f.HandleKey(KeyHome) // A caret 0
	f.HandleKey(KeyTab)  // first visit to B: end of value
	if got := f.FieldCaret(1); got != 4 {
		t.Fatalf("first visit caret = %d, want 4 (value end)", got)
	}
	typeText(f, "!")
	if got := f.Value(1); got != "seed!" {
		t.Fatalf("first visit typing = %q, want %q", got, "seed!")
	}
	f.HandleKey(KeyHome) // B caret 0
	f.HandleKey(KeyUp)   // back to A: remembered caret 0
	if f.Cursor() != 0 {
		t.Fatalf("Up cursor = %d, want 0", f.Cursor())
	}
	if got := f.FieldCaret(0); got != 0 {
		t.Fatalf("remembered caret = %d, want 0", got)
	}
	typeText(f, "Z")
	if got := f.Value(0); got != "Zabc" {
		t.Fatalf("typing at remembered caret = %q, want %q", got, "Zabc")
	}
	f.HandleKey(KeyDown) // B: remembered caret 0
	typeText(f, "Q")
	if got := f.Value(1); got != "Qseed!" {
		t.Fatalf("typing at remembered caret = %q, want %q", got, "Qseed!")
	}
	f.SetCursor(0)
	if got := f.FieldCaret(0); got != 1 {
		t.Fatalf("SetCursor reset the caret: %d, want 1", got)
	}
}

// TestFormCaretClampsOnRestore pins the clamp on the one path that rewrites
// values after construction: Restore pulls a caret past the new value end
// back to the end, leaves a caret that still fits where it was, and keeps
// the dirty latch clean. Snapshot stays caret-free: view state is not a
// value.
func TestFormCaretClampsOnRestore(t *testing.T) {
	f := NewForm([]Field{{Label: "A", Value: "web"}})
	snap := f.Snapshot()
	typeText(f, "-v2") // caret 6
	if got := f.FieldCaret(0); got != 6 {
		t.Fatalf("caret before restore = %d, want 6", got)
	}
	f.Restore(snap)
	if got := f.FieldCaret(0); got != 3 {
		t.Fatalf("caret after restore = %d, want the restored end 3", got)
	}
	if f.Dirty() {
		t.Fatal("form dirty after Restore")
	}
	f.Restore([]string{"longer"}) // caret 3 still fits: kept
	if got := f.FieldCaret(0); got != 3 {
		t.Fatalf("caret after longer restore = %d, want 3 (still fits)", got)
	}
	f.HandleKey(KeyEnd) // caret 6
	f.Restore([]string{"x"})
	if got := f.FieldCaret(0); got != 1 {
		t.Fatalf("caret after shorter restore = %d, want the new end 1", got)
	}

	// A value list shorter than the field grid leaves the remaining values
	// alone but still clamps every caret.
	g := NewForm([]Field{{Label: "A", Value: "abc"}, {Label: "B", Value: "defgh"}})
	g.HandleKey(KeyTab) // B caret 5
	g.Restore([]string{"z"})
	if got := g.Value(1); got != "defgh" {
		t.Fatalf("unrestored field value = %q, want %q", got, "defgh")
	}
	if got := g.FieldCaret(0); got != 1 {
		t.Fatalf("restored caret = %d, want 1", got)
	}
	if got := g.FieldCaret(1); got != 5 {
		t.Fatalf("unrestored caret = %d, want 5", got)
	}
}

// TestFormCaretMovementIsViewState pins that movement keys change neither
// the values, the dirty latch, the message line, nor the rendered grid: the
// Form does not paint the caret this round (a hosting screen will, from
// FieldCaretCell), so the rendered line shape and count stay unchanged.
func TestFormCaretMovementIsViewState(t *testing.T) {
	f := NewForm([]Field{{Label: "한국어", Value: "kim"}, {Label: "Name", Value: "lee"}})
	before := strings.Join(f.Render(60), "\n")
	steps := []struct {
		key  Key
		want int
	}{
		{KeyLeft, 2},
		{KeyLeft, 1},
		{KeyLeft, 0},
		{KeyLeft, 0},
		{KeyRight, 1},
		{KeyEnd, 3},
		{KeyHome, 0},
	}
	for _, s := range steps {
		f.HandleKey(s.key)
		if got := f.FieldCaret(0); got != s.want {
			t.Fatalf("after %q caret = %d, want %d", s.key, got, s.want)
		}
		if got := strings.Join(f.Render(60), "\n"); got != before {
			t.Fatalf("%q changed the rendered grid:\n%s\nwant:\n%s", s.key, got, before)
		}
		if got := f.Value(0); got != "kim" {
			t.Fatalf("%q changed the value to %q", s.key, got)
		}
		if got := f.Message(); got != "" {
			t.Fatalf("%q set the message to %q", s.key, got)
		}
		if f.Dirty() {
			t.Fatalf("%q set the dirty latch", s.key)
		}
	}
	f.HandleKey(KeyEnd) // caret 3: forward delete has nothing to remove
	f.HandleKey(KeyDelete)
	if got := strings.Join(f.Render(60), "\n"); got != before {
		t.Fatal("no-op forward delete changed the rendered grid")
	}
	if got := f.Value(0); got != "kim" {
		t.Fatalf("no-op forward delete changed the value to %q", got)
	}
	if f.Dirty() {
		t.Fatal("no-op forward delete set the dirty latch")
	}
}
