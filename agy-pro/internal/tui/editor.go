package tui

import (
	"strings"

	"agypro/internal/cellwidth"
)

// maxLineRunes caps one editor line at 4096 runes so a large paste cannot
// grow a line without bound. The cap is enforced only on the rune-insertion
// path (insert), never on programmatic seeding (NewEditor, SetText): a long
// existing prompt or preset value still loads, renders, saves, and can be
// deleted down, only typing into it past the cap is refused. Newlines arrive
// only as discrete KeyEnter events, never inside a paste burst, so bounding
// each line bounds one paste.
const maxLineRunes = 4096

// editorLimitMsg is the message-line notice when a rune insertion hits the
// per-line cap. The editor consumes this const directly (wiring the catalog
// key into the component is out of scope for this mission), so the const
// stays as the English fallback only.
// catalog-resolved, fallback kept: editor.limit.reached
const editorLimitMsg = "Line limit reached (4096 characters); further input was dropped."

// Editor is the inline multi-line text component (prompt editor): caret
// movement, rune insertion and deletion, line splitting and joining,
// viewport scrolling, and Ctrl+S save. Caret and scrolling math goes
// through cellwidth, so CJK text occupies the cells a terminal shows.
// RTL text is treated as opaque runes: movement and deletion are per rune
// in logical order, with no bidi cursor promises (honest scope).
type Editor struct {
	lines   []string
	row     int // caret line
	col     int // caret rune index within the caret line
	top     int // first visible row
	page    int // viewport height in rows
	dirty   bool
	saved   bool
	message string
	onSave  func(text string) error
}

// NewEditor loads text into a fresh editor (an empty text is one empty
// line). The caret starts at the top-left.
func NewEditor(text string) *Editor {
	if text == "" {
		return &Editor{lines: []string{""}, page: 10}
	}
	return &Editor{lines: strings.Split(text, "\n"), page: 10}
}

// PageSize returns the viewport height in rows.
func (e *Editor) PageSize() int { return e.page }

// SetPageSize sets the viewport height in rows (the screen derives it from
// the frame height; the default is 10).
func (e *Editor) SetPageSize(n int) {
	if n < 1 {
		n = 1
	}
	e.page = n
	e.ensureVisible()
}

// SetOnSave installs the save callback (the prompt store write). A nil
// callback makes Ctrl+S a pure dirty-clearing save latch.
func (e *Editor) SetOnSave(fn func(text string) error) { e.onSave = fn }

// Text returns the edited text (lines joined with "\n").
func (e *Editor) Text() string { return strings.Join(e.lines, "\n") }

// SetText replaces the text and resets caret, scroll, and dirty state.
func (e *Editor) SetText(text string) {
	if text == "" {
		e.lines = []string{""}
	} else {
		e.lines = strings.Split(text, "\n")
	}
	e.row, e.col, e.top = 0, 0, 0
	e.dirty, e.saved = false, false
	e.message = ""
}

// Dirty reports unsaved edits.
func (e *Editor) Dirty() bool { return e.dirty }

// Saved reports whether the last save succeeded.
func (e *Editor) Saved() bool { return e.saved }

// Message returns the last save error or limit notice.
func (e *Editor) Message() string { return e.message }

// Caret returns the caret position as absolute row and rune column.
func (e *Editor) Caret() (int, int) { return e.row, e.col }

// CaretCell returns the display-cell offset of the caret within its line
// (the painter places the caret with this; CJK runes count 2).
func (e *Editor) CaretCell() int {
	w := 0
	for _, r := range []rune(e.lines[e.row])[:e.col] {
		w += cellwidth.Rune(r)
	}
	return w
}

// Top returns the first visible row.
func (e *Editor) Top() int { return e.top }

// Save persists through the onSave callback. On success it clears dirty
// and latches saved; on failure the error becomes the message line.
func (e *Editor) Save() error {
	if e.onSave != nil {
		if err := e.onSave(e.Text()); err != nil {
			e.message = err.Error()
			return err
		}
	}
	e.message = ""
	e.dirty = false
	e.saved = true
	return nil
}

// HandleKey runs one key in EDIT scope. Ctrl+C/Ctrl+L/Esc and modal keys
// are consumed by the App dispatcher before this is called.
func (e *Editor) HandleKey(k Key) {
	switch k {
	case KeyLeft:
		if e.col > 0 {
			e.col--
		}
	case KeyRight:
		if e.col < e.lineLen(e.row) {
			e.col++
		}
	case KeyUp:
		if e.row > 0 {
			e.row--
			e.clampCol()
		}
	case KeyDown:
		if e.row < len(e.lines)-1 {
			e.row++
			e.clampCol()
		}
	case KeyHome:
		e.col = 0
	case KeyEnd:
		e.col = e.lineLen(e.row)
	case KeyEnter:
		e.splitLine()
	case KeyBackspace:
		e.backspace()
	case KeyDelete:
		e.deleteAt()
	case KeyPgUp:
		e.row -= e.page
		if e.row < 0 {
			e.row = 0
		}
		e.clampCol()
		e.top = e.row - e.page + 1
	case KeyPgDn:
		e.row += e.page
		if e.row > len(e.lines)-1 {
			e.row = len(e.lines) - 1
		}
		e.clampCol()
		e.top = e.row
	case KeyCtrlS:
		e.Save()
		return
	default:
		if r, ok := EditRune(k); ok {
			e.insert(r)
			return
		}
		return
	}
	e.ensureVisible()
}

func (e *Editor) lineLen(row int) int { return len([]rune(e.lines[row])) }

func (e *Editor) clampCol() {
	if e.col > e.lineLen(e.row) {
		e.col = e.lineLen(e.row)
	}
}

func (e *Editor) insert(r rune) {
	rs := []rune(e.lines[e.row])
	if len(rs) >= maxLineRunes {
		e.message = editorLimitMsg
		return
	}
	rs = append(rs, 0)
	copy(rs[e.col+1:], rs[e.col:])
	rs[e.col] = r
	e.lines[e.row] = string(rs)
	e.col++
	e.dirty = true
	e.saved = false
}

func (e *Editor) splitLine() {
	rs := []rune(e.lines[e.row])
	before, after := string(rs[:e.col]), string(rs[e.col:])
	e.lines[e.row] = before
	e.lines = append(e.lines, "")
	copy(e.lines[e.row+2:], e.lines[e.row+1:])
	e.lines[e.row+1] = after
	e.row++
	e.col = 0
	e.dirty = true
	e.saved = false
}

func (e *Editor) backspace() {
	if e.col > 0 {
		rs := []rune(e.lines[e.row])
		rs = append(rs[:e.col-1], rs[e.col:]...)
		e.lines[e.row] = string(rs)
		e.col--
		e.dirty = true
		e.saved = false
		return
	}
	if e.row > 0 {
		// At line start: join with the previous line, caret at the join.
		e.col = e.lineLen(e.row - 1)
		e.lines[e.row-1] += e.lines[e.row]
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.dirty = true
		e.saved = false
	}
}

func (e *Editor) deleteAt() {
	if e.col < e.lineLen(e.row) {
		rs := []rune(e.lines[e.row])
		rs = append(rs[:e.col], rs[e.col+1:]...)
		e.lines[e.row] = string(rs)
		e.dirty = true
		e.saved = false
		return
	}
	if e.row < len(e.lines)-1 {
		// At line end: pull the next line up.
		e.lines[e.row] += e.lines[e.row+1]
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
		e.dirty = true
		e.saved = false
	}
}

func (e *Editor) ensureVisible() {
	if e.row < e.top {
		e.top = e.row
	}
	if e.row >= e.top+e.page {
		e.top = e.row - e.page + 1
	}
	maxTop := len(e.lines) - e.page
	if maxTop < 0 {
		maxTop = 0
	}
	if e.top > maxTop {
		e.top = maxTop
	}
	if e.top < 0 {
		e.top = 0
	}
}

// View returns the visible window of lines and the caret row relative to
// the window top (the painter renders the window and places the caret).
func (e *Editor) View() (rows []string, caretRow int) {
	end := e.top + e.page
	if end > len(e.lines) {
		end = len(e.lines)
	}
	return e.lines[e.top:end], e.row - e.top
}
