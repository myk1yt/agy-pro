package tui

import (
	"fmt"

	"agypro/internal/cellwidth"
)

// maxFieldRunes caps one form field value at 4096 runes so a large paste
// cannot grow a field without bound. The cap is enforced only on the
// rune-insertion path in HandleKey; seeded values (NewForm fields, Restore)
// are trusted program state and stay untouched, so editing a long existing
// preset value still works.
const maxFieldRunes = 4096

// formLimitMsg is the message-line notice when a rune insertion hits the
// field cap. The form consumes this const directly (wiring the catalog key
// into the component is out of scope for this mission), so the const stays
// as the English fallback only.
// catalog-resolved, fallback kept: form.limit.reached
const formLimitMsg = "Field limit reached (4096 characters); further input was dropped."

// Field is one form field. Validate is optional; when set, its error text
// becomes the form message line and blocks Enter-advance. The Value holds
// the live (edited) text; RTL input rides through as opaque runes.
type Field struct {
	Label    string
	Value    string
	Validate func(s string) error
}

// Form is the field-grid component (model add/edit form, preset editor):
// Tab or Down advances to the next field, Up moves back one field, the
// focused field accepts text through EditRune (so digits, y/n, and space
// are text here even though they select and confirm in NAV/CONFIRM), and
// Enter validates the current field before advancing. On the last field a
// valid Enter submits the form. Edits are tracked for the dirty
// discard-confirm gate.
//
// Every field carries a rune-index caret (the editor's model at one-line
// scale): Left/Right step one rune, Home/End jump to the value ends, Delete
// removes the rune at the caret, Backspace the rune before it, and printable
// runes insert at the caret instead of appending. Movement keys never set
// the dirty latch, and the caret never wraps across fields. Each caret
// starts at the end of its seeded value (so typing on first focus appends,
// the pre-caret behavior) and is remembered per field across focus changes.
// The Form does not paint the caret; FieldCaret/FieldCaretCell expose it so
// a hosting screen can (the prompt editor's Caret/CaretCell split).
type Form struct {
	fields    []Field
	carets    []int // per-field rune-index caret, parallel to fields
	cursor    int
	message   string
	dirty     bool
	submitted bool
}

// NewForm builds a form over the given fields (at least one). Every caret
// starts at the end of its seeded value.
func NewForm(fields []Field) *Form {
	if len(fields) == 0 {
		fields = []Field{{Label: ""}}
	}
	carets := make([]int, len(fields))
	for i := range fields {
		carets[i] = len([]rune(fields[i].Value))
	}
	return &Form{fields: fields, carets: carets}
}

// Len reports the field count.
func (f *Form) Len() int { return len(f.fields) }

// Cursor returns the focused field index.
func (f *Form) Cursor() int { return f.cursor }

// Field returns the field at index i.
func (f *Form) Field(i int) Field { return f.fields[i] }

// Value returns the live value of field i.
func (f *Form) Value(i int) string { return f.fields[i].Value }

// FieldCaret returns the rune-index caret of field i: 0 is the value start,
// the value's rune length is the end. CJK text counts one index per rune,
// matching the editor's caret model. Like Value, it indexes the field slice
// directly.
func (f *Form) FieldCaret(i int) int { return f.carets[i] }

// FieldCaretCell returns the display-cell offset of field i's caret within
// the field value (CJK runes count two cells). A hosting screen that paints
// the caret adds this to the cell width of its own prefix (cursor marker,
// padded label, separator) to place the glyph inside the rendered line, the
// same way the prompt editor's CaretCell is used.
func (f *Form) FieldCaretCell(i int) int {
	rs := []rune(f.fields[i].Value)
	c := f.carets[i]
	if c > len(rs) {
		c = len(rs)
	}
	if c < 0 {
		c = 0
	}
	return cellwidth.String(string(rs[:c]))
}

// SetCursor focuses field i, clamped to the field range. The caret is not
// reset: each field keeps its own caret position across focus changes.
func (f *Form) SetCursor(i int) {
	if i < 0 {
		i = 0
	}
	if i > len(f.fields)-1 {
		i = len(f.fields) - 1
	}
	f.cursor = i
}

// Message returns the last validation error or limit notice (the message
// line).
func (f *Form) Message() string { return f.message }

// Dirty reports whether any field was edited since construction or the
// last MarkClean/Restore.
func (f *Form) Dirty() bool { return f.dirty }

// MarkClean clears the dirty latch (the caller persisted the values).
func (f *Form) MarkClean() { f.dirty = false }

// Submitted reports whether a valid Enter on the last field submitted the
// form.
func (f *Form) Submitted() bool { return f.submitted }

// Snapshot copies the live values (the discard-confirm restore point). The
// caret is not part of the snapshot: it is view state, not a value.
func (f *Form) Snapshot() []string {
	vals := make([]string, len(f.fields))
	for i := range f.fields {
		vals[i] = f.fields[i].Value
	}
	return vals
}

// Restore puts back a Snapshot and clears the dirty latch: the pending
// edits are discarded, so the form is clean again. Every caret is clamped
// into its restored value, so a shortened value cannot leave the caret past
// the new end.
func (f *Form) Restore(vals []string) {
	for i := range f.fields {
		if i < len(vals) {
			f.fields[i].Value = vals[i]
		}
	}
	f.clampCarets()
	f.dirty = false
	f.message = ""
}

// clampCarets pulls every caret inside its live value. Restore is the one
// path that rewrites a value after construction.
func (f *Form) clampCarets() {
	for i := range f.fields {
		c := f.carets[i]
		if n := f.valueLen(i); c > n {
			c = n
		}
		if c < 0 {
			c = 0
		}
		f.carets[i] = c
	}
}

// HandleKey runs one key in EDIT scope. Ctrl+C/Ctrl+L/Esc and modal keys
// are consumed by the App dispatcher before this is called. Left/Right/Home/
// End move the caret only (they never set the dirty latch), Up/Down keep
// their field-navigation meaning, and the caret never crosses a field
// boundary.
func (f *Form) HandleKey(k Key) {
	switch k {
	case KeyTab, KeyDown:
		f.advance(1)
		return
	case KeyUp:
		f.advance(-1)
		return
	case KeyLeft:
		if f.carets[f.cursor] > 0 {
			f.carets[f.cursor]--
		}
		return
	case KeyRight:
		if f.carets[f.cursor] < f.valueLen(f.cursor) {
			f.carets[f.cursor]++
		}
		return
	case KeyHome:
		f.carets[f.cursor] = 0
		return
	case KeyEnd:
		f.carets[f.cursor] = f.valueLen(f.cursor)
		return
	case KeyDelete:
		f.deleteAtCaret()
		return
	case KeyBackspace:
		f.backspaceAtCaret()
		return
	case KeyEnter:
		if err := f.validate(f.cursor); err != nil {
			f.message = err.Error()
			return
		}
		f.message = ""
		if f.cursor == len(f.fields)-1 {
			f.submitted = true
			return
		}
		f.advance(1)
		return
	}
	if r, ok := EditRune(k); ok {
		f.insertAtCaret(r)
	}
}

func (f *Form) advance(delta int) {
	f.cursor += delta
	if f.cursor < 0 {
		f.cursor = 0
	}
	if f.cursor > len(f.fields)-1 {
		f.cursor = len(f.fields) - 1
	}
}

// valueLen returns the rune length of field i's live value.
func (f *Form) valueLen(i int) int { return len([]rune(f.fields[i].Value)) }

// insertAtCaret inserts r at the focused field's caret and steps the caret
// past it, so a rune burst lands in logical order. The field cap is enforced
// on this path only (seeded values stay trusted program state).
func (f *Form) insertAtCaret(r rune) {
	rs := []rune(f.fields[f.cursor].Value)
	if len(rs) >= maxFieldRunes {
		f.message = formLimitMsg
		return
	}
	c := f.carets[f.cursor]
	if c > len(rs) {
		c = len(rs)
	}
	if c < 0 {
		c = 0
	}
	rs = append(rs, 0)
	copy(rs[c+1:], rs[c:])
	rs[c] = r
	f.fields[f.cursor].Value = string(rs)
	f.carets[f.cursor] = c + 1
	f.dirty = true
}

// backspaceAtCaret removes the rune before the caret. At the value start it
// is a no-op (the form never joins fields), so a clean form stays clean.
func (f *Form) backspaceAtCaret() {
	rs := []rune(f.fields[f.cursor].Value)
	c := f.carets[f.cursor]
	if c > len(rs) {
		c = len(rs)
	}
	if c < 0 {
		c = 0
	}
	f.carets[f.cursor] = c
	if c == 0 {
		return
	}
	rs = append(rs[:c-1], rs[c:]...)
	f.fields[f.cursor].Value = string(rs)
	f.carets[f.cursor] = c - 1
	f.dirty = true
}

// deleteAtCaret removes the rune at the caret (forward delete). At the value
// end it is a no-op and leaves the dirty latch alone.
func (f *Form) deleteAtCaret() {
	rs := []rune(f.fields[f.cursor].Value)
	c := f.carets[f.cursor]
	if c > len(rs) {
		c = len(rs)
	}
	if c < 0 {
		c = 0
	}
	f.carets[f.cursor] = c
	if c == len(rs) {
		return
	}
	rs = append(rs[:c], rs[c+1:]...)
	f.fields[f.cursor].Value = string(rs)
	f.dirty = true
}

func (f *Form) validate(i int) error {
	if v := f.fields[i].Validate; v != nil {
		return v(f.fields[i].Value)
	}
	return nil
}

// Render returns the field grid lines: cursor marker, label column padded
// through cellwidth, and the live values. The message line is not part of
// the grid; the screen renders it between the grid and the footer. The caret
// is not painted here (a hosting screen paints it from FieldCaretCell), so
// the line shape, the line count, and the width contract stay unchanged.
func (f *Form) Render(w int) []string {
	labelW := 0
	for _, fld := range f.fields {
		if cw := cellwidth.String(fld.Label); cw > labelW {
			labelW = cw
		}
	}
	out := make([]string, 0, len(f.fields))
	for i, fld := range f.fields {
		marker := "  "
		if i == f.cursor {
			marker = "▶ "
		}
		line := fmt.Sprintf("%s%s : %s", marker, cellwidth.PadRight(fld.Label, labelW), fld.Value)
		out = append(out, cellwidth.Truncate(line, w))
	}
	return out
}
