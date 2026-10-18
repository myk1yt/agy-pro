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
type Form struct {
	fields    []Field
	cursor    int
	message   string
	dirty     bool
	submitted bool
}

// NewForm builds a form over the given fields (at least one).
func NewForm(fields []Field) *Form {
	if len(fields) == 0 {
		fields = []Field{{Label: ""}}
	}
	return &Form{fields: fields}
}

// Len reports the field count.
func (f *Form) Len() int { return len(f.fields) }

// Cursor returns the focused field index.
func (f *Form) Cursor() int { return f.cursor }

// Field returns the field at index i.
func (f *Form) Field(i int) Field { return f.fields[i] }

// Value returns the live value of field i.
func (f *Form) Value(i int) string { return f.fields[i].Value }

// SetCursor focuses field i, clamped to the field range.
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

// Snapshot copies the live values (the discard-confirm restore point).
func (f *Form) Snapshot() []string {
	vals := make([]string, len(f.fields))
	for i := range f.fields {
		vals[i] = f.fields[i].Value
	}
	return vals
}

// Restore puts back a Snapshot and clears the dirty latch: the pending
// edits are discarded, so the form is clean again.
func (f *Form) Restore(vals []string) {
	for i := range f.fields {
		if i < len(vals) {
			f.fields[i].Value = vals[i]
		}
	}
	f.dirty = false
	f.message = ""
}

// HandleKey runs one key in EDIT scope. Ctrl+C/Ctrl+L/Esc and modal keys
// are consumed by the App dispatcher before this is called.
func (f *Form) HandleKey(k Key) {
	switch k {
	case KeyTab, KeyDown:
		f.advance(1)
		return
	case KeyUp:
		f.advance(-1)
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
	case KeyBackspace:
		if v := f.fields[f.cursor].Value; v != "" {
			r := []rune(v)
			f.fields[f.cursor].Value = string(r[:len(r)-1])
			f.dirty = true
		}
		return
	}
	if r, ok := EditRune(k); ok {
		if len([]rune(f.fields[f.cursor].Value)) >= maxFieldRunes {
			f.message = formLimitMsg
			return
		}
		f.fields[f.cursor].Value += string(r)
		f.dirty = true
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

func (f *Form) validate(i int) error {
	if v := f.fields[i].Validate; v != nil {
		return v(f.fields[i].Value)
	}
	return nil
}

// Render returns the field grid lines: cursor marker, label column padded
// through cellwidth, and the live values. The message line is not part of
// the grid; the screen renders it between the grid and the footer.
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
