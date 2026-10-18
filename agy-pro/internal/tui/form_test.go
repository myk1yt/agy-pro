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
