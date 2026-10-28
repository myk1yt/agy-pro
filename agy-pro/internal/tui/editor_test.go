package tui

import (
	"errors"
	"strings"
	"testing"
)

func TestEditorCJKCaretMath(t *testing.T) {
	e := NewEditor("한글ab")
	// 한 and 글 are wide (2 cells each); a and b are 1 cell each.
	if got := e.CaretCell(); got != 0 {
		t.Fatalf("initial caret cell = %d, want 0", got)
	}
	e.HandleKey(KeyRight)
	if got := e.CaretCell(); got != 2 {
		t.Fatalf("caret cell after 1 Right = %d, want 2", got)
	}
	e.HandleKey(KeyRight)
	if got := e.CaretCell(); got != 4 {
		t.Fatalf("caret cell after 2 Right = %d, want 4", got)
	}
	e.HandleKey(KeyLeft)
	if got := e.CaretCell(); got != 2 {
		t.Fatalf("caret cell after Left = %d, want 2", got)
	}
	e.HandleKey(KeyEnd)
	if got := e.CaretCell(); got != 6 {
		t.Fatalf("caret cell at End = %d, want 6", got)
	}
	e.HandleKey(KeyHome)
	if got := e.CaretCell(); got != 0 {
		t.Fatalf("caret cell at Home = %d, want 0", got)
	}
	if got := e.Text(); got != "한글ab" {
		t.Fatalf("text changed by movement: %q", got)
	}
}

func TestEditorInsertAndDelete(t *testing.T) {
	e := NewEditor("ac")
	e.HandleKey(KeyEnd)  // caret starts at the top-left; move to the end
	e.HandleKey(KeyLeft) // caret between a and c (col 1)
	e.HandleKey(RuneKey('b'))
	if got := e.Text(); got != "abc" {
		t.Fatalf("insert = %q, want %q", got, "abc")
	}
	e.HandleKey(KeyBackspace)
	if got := e.Text(); got != "ac" {
		t.Fatalf("backspace = %q, want %q", got, "ac")
	}
	e.HandleKey(KeyRight) // caret at end
	e.HandleKey(RuneKey('!'))
	if got := e.Text(); got != "ac!" {
		t.Fatalf("append = %q, want %q", got, "ac!")
	}
	e.HandleKey(KeyLeft) // caret before '!'
	e.HandleKey(KeyDelete)
	if got := e.Text(); got != "ac" {
		t.Fatalf("delete = %q, want %q", got, "ac")
	}
	e.HandleKey(KeyEnd)
	e.HandleKey(RuneKey('한'))
	e.HandleKey(KeyBackspace)
	if got := e.Text(); got != "ac" {
		t.Fatalf("wide backspace = %q, want %q", got, "ac")
	}
}

func TestEditorLineSplitAndJoin(t *testing.T) {
	e := NewEditor("helloworld")
	e.HandleKey(KeyHome)
	for i := 0; i < 5; i++ {
		e.HandleKey(KeyRight)
	}
	e.HandleKey(KeyEnter) // split after "hello"
	if got := e.Text(); got != "hello\nworld" {
		t.Fatalf("split = %q, want %q", got, "hello\nworld")
	}
	if r, c := e.Caret(); r != 1 || c != 0 {
		t.Fatalf("caret after split = %d,%d; want 1,0", r, c)
	}
	e.HandleKey(KeyBackspace) // join back
	if got := e.Text(); got != "helloworld" {
		t.Fatalf("join = %q, want %q", got, "helloworld")
	}
	if r, c := e.Caret(); r != 0 || c != 5 {
		t.Fatalf("caret after join = %d,%d; want 0,5", r, c)
	}

	// Delete at line end pulls the next line up.
	e.HandleKey(KeyEnd)
	e.HandleKey(KeyEnter)
	e.HandleKey(KeyUp)
	e.HandleKey(KeyEnd)
	e.HandleKey(KeyDelete)
	if got := e.Text(); got != "helloworld" {
		t.Fatalf("delete-join = %q, want %q", got, "helloworld")
	}
}

func TestEditorVerticalMovementClampsColumn(t *testing.T) {
	e := NewEditor("long line\nxy")
	e.HandleKey(KeyEnd) // col 9 on line 0
	e.HandleKey(KeyDown)
	if r, c := e.Caret(); r != 1 || c != 2 {
		t.Fatalf("caret = %d,%d; want 1,2 (clamped to the short line)", r, c)
	}
	e.HandleKey(KeyUp)
	if r, c := e.Caret(); r != 0 || c != 2 {
		t.Fatalf("caret = %d,%d; want 0,2 (column restored)", r, c)
	}
	e.HandleKey(KeyDown) // at last line: stays
	if r, _ := e.Caret(); r != 1 {
		t.Fatalf("Down at last line moved to row %d", r)
	}
}

func TestEditorScrollPaging(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString("\n")
	}
	e := NewEditor(strings.TrimRight(b.String(), "\n"))
	e.SetPageSize(10)

	rows, caretRow := e.View()
	if len(rows) != 10 || caretRow != 0 || e.Top() != 0 {
		t.Fatalf("initial view = %d rows caret %d top %d", len(rows), caretRow, e.Top())
	}
	e.HandleKey(KeyPgDn)
	if r, _ := e.Caret(); r != 10 {
		t.Fatalf("caret after PgDn = %d, want 10", r)
	}
	if e.Top() != 10 {
		t.Fatalf("top after PgDn = %d, want 10", e.Top())
	}
	e.HandleKey(KeyPgDn)
	if r, _ := e.Caret(); r != 20 || e.Top() != 20 {
		t.Fatalf("second PgDn caret/top = %d/%d, want 20/20", r, e.Top())
	}
	e.HandleKey(KeyPgDn) // clamps at the last line
	if r, _ := e.Caret(); r != 29 {
		t.Fatalf("PgDn clamp = %d, want 29", r)
	}
	e.HandleKey(KeyHome) // line start: row stays, column goes home
	if r, c := e.Caret(); r != 29 || c != 0 {
		t.Fatalf("Home caret = %d,%d; want 29,0 (Home is line start, not document start)", r, c)
	}
	for i := 0; i < 3; i++ {
		e.HandleKey(KeyPgUp)
	}
	if r, _ := e.Caret(); r != 0 || e.Top() != 0 {
		t.Fatalf("PgUp back to top: caret %d top %d, want 0/0", r, e.Top())
	}
}

func TestEditorSave(t *testing.T) {
	var saved string
	e := NewEditor("draft")
	e.SetOnSave(func(text string) error {
		saved = text
		return nil
	})
	e.HandleKey(KeyEnd)
	e.HandleKey(RuneKey('!'))
	if e.Saved() {
		t.Fatal("saved latch set before Ctrl+S")
	}
	e.HandleKey(KeyCtrlS)
	if got := saved; got != "draft!" {
		t.Fatalf("saved text = %q, want %q", got, "draft!")
	}
	if !e.Saved() || e.Dirty() {
		t.Fatalf("after save: saved %v dirty %v, want true/false", e.Saved(), e.Dirty())
	}
	e.HandleKey(KeyEnd)
	e.HandleKey(RuneKey('?'))
	if !e.Dirty() || e.Saved() {
		t.Fatalf("edit after save: saved %v dirty %v, want false/true", e.Saved(), e.Dirty())
	}

	// A failing save keeps the error on the message line and stays dirty.
	e.SetOnSave(func(text string) error { return errors.New("disk full") })
	e.HandleKey(KeyCtrlS)
	if got := e.Message(); got != "disk full" {
		t.Fatalf("save error message = %q, want %q", got, "disk full")
	}
	if !e.Dirty() || e.Saved() {
		t.Fatalf("failed save: saved %v dirty %v, want false/true", e.Saved(), e.Dirty())
	}

	// New text resets everything (reset-to-default behind the y/n gate).
	e.SetText("default")
	if e.Dirty() || e.Saved() || e.Message() != "" {
		t.Fatalf("SetText left state: dirty %v saved %v msg %q", e.Dirty(), e.Saved(), e.Message())
	}
	if got := e.Text(); got != "default" {
		t.Fatalf("SetText = %q, want %q", got, "default")
	}
}

func TestEditorEmptyTextIsOneLine(t *testing.T) {
	e := NewEditor("")
	e.HandleKey(RuneKey('x'))
	if got := e.Text(); got != "x" {
		t.Fatalf("typed into empty editor = %q, want %q", got, "x")
	}
	e2 := NewEditor("")
	rows, caretRow := e2.View()
	if len(rows) != 1 || caretRow != 0 {
		t.Fatalf("empty editor view = %d rows caret %d, want 1/0", len(rows), caretRow)
	}
}

// TestEditorLineCap pins the rune cap: typing past maxLineRunes stops with
// the limit notice and changes nothing, while a programmatically seeded long
// line still renders, accepts deletions, and saves.
func TestEditorLineCap(t *testing.T) {
	// Typing to the cap and beyond: the line stops growing and the message
	// line reports the limit.
	e := NewEditor("")
	for i := 0; i < maxLineRunes+8; i++ {
		e.HandleKey(RuneKey('a'))
	}
	if got := len([]rune(e.Text())); got != maxLineRunes {
		t.Fatalf("typed length = %d, want the cap %d", got, maxLineRunes)
	}
	if e.Message() != editorLimitMsg {
		t.Fatalf("cap message = %q, want %q", e.Message(), editorLimitMsg)
	}
	if !e.Dirty() {
		t.Fatal("editor not dirty after capped typing")
	}

	// A pre-seeded long line bypasses the cap by design (seeding is program
	// state, not rune insertion): it renders, saves, and can be edited down.
	long := strings.Repeat("x", maxLineRunes+100)
	e2 := NewEditor(long)
	rows, caretRow := e2.View()
	if len(rows) != 1 || rows[0] != long || caretRow != 0 {
		t.Fatalf("seeded long line did not render: %d rows", len(rows))
	}
	e2.HandleKey(KeyEnd)
	e2.HandleKey(RuneKey('y')) // refused: still over the per-line cap
	if got := e2.Text(); got != long {
		t.Fatalf("insert into seeded long line changed it: %d runes", len([]rune(got)))
	}
	if e2.Message() != editorLimitMsg {
		t.Fatalf("cap message = %q, want %q", e2.Message(), editorLimitMsg)
	}
	e2.HandleKey(KeyBackspace) // deletion is never capped
	if got := len([]rune(e2.Text())); got != maxLineRunes+99 {
		t.Fatalf("backspace on seeded long line = %d runes, want %d", got, maxLineRunes+99)
	}
	var saved string
	e2.SetOnSave(func(text string) error { saved = text; return nil })
	e2.HandleKey(KeyCtrlS)
	if saved != e2.Text() || !e2.Saved() {
		t.Fatalf("seeded long line did not save: saved %v latch %v", saved != e2.Text(), e2.Saved())
	}
}
