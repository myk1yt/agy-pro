package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/testfixtures"
)

// newPromptEditorApp pushes the prompt editor over a stub root so save
// and reset pops have somewhere to land.
func newPromptEditorApp(slot string) *App {
	app := NewApp(110, 40, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptEditorScreen(slot))
	return app
}

// TestPromptEditorSeedsCompiledDefaultLabeled pins the no-override view:
// the buffer carries the compiled default text, the label marks it as a
// read-only reference, the mode is ModeEdit, and the seat slot renders
// the anti-nesting guardrail.
func TestPromptEditorSeedsCompiledDefaultLabeled(t *testing.T) {
	promptTestHome(t)
	app := newPromptEditorApp("review")
	out := app.Frame().String()
	if !strings.Contains(out, "You are a Staff Code Reviewer") {
		t.Fatalf("editor frame missing the compiled default seed text; frame:\n%s", out)
	}
	if !strings.Contains(out, "read-only reference") {
		t.Fatalf("editor frame missing the compiled-default label; frame:\n%s", out)
	}
	if !strings.Contains(out, "compiled default") {
		t.Fatalf("editor frame missing the source badge; frame:\n%s", out)
	}
	if top := app.Top(); top == nil || top.Mode() != ModeEdit {
		t.Fatalf("editor mode = %v, want ModeEdit while editing", top.Mode())
	}

	seatApp := newPromptEditorApp("seat")
	seatOut := seatApp.Frame().String()
	if !strings.Contains(seatOut, "anti-nesting control text") {
		t.Fatalf("seat editor frame missing the guardrail line; frame:\n%s", seatOut)
	}
}

// TestPromptEditorSaveWritesAndFlipsBadge drives the full save flow:
// type new text, Ctrl+S, the confirm shows the path and byte count, y
// writes through the store, the screen pops back, and the badge flips to
// file.
func TestPromptEditorSaveWritesAndFlipsBadge(t *testing.T) {
	promptTestHome(t)
	const text = "My custom review prompt."
	app := newPromptEditorApp("review")
	keys := append(typeRunes(text), KeyCtrlS)
	for _, k := range keys {
		app.Step(k)
	}
	// The save gate is live: path and byte count rendered, content not.
	// Typing lands at the top-left caret, so the buffer is the typed text
	// prepended to the compiled default.
	wantText := text + fanout.GetTaskPreset("review").SystemPrompt
	w := app.Width()
	out := app.Frame().String()
	if !strings.Contains(out, "Save prompt?") {
		t.Fatalf("save gate not open after Ctrl+S; frame:\n%s", out)
	}
	// Moved pin: the width clamp moved to the shared composition path
	// (framePlan.compose -> clampLines, phase-3 fix-2). The gate body is one
	// line carrying the target path and the byte count; the temp path alone
	// fills the 110-cell frame edge, so the frame keeps the clamped form of
	// that line and the byte-count clause the old pin read off the frame now
	// falls past the cut. The clause stays pinned on the gate body (the raw,
	// byte-stable slice Confirm.Body keeps) and the frame is pinned to that
	// line's clamped form; the old expectation asserted content only the
	// unclamped test path ever composed.
	modal := app.Modal()
	if modal == nil {
		t.Fatalf("no modal live at the save gate")
	}
	body := modal.Body(w)
	if len(body) == 0 || body[0] == "" {
		t.Fatalf("save gate body is empty")
	}
	gateLine := body[0]
	if want := "(" + strconv.Itoa(len(wantText)) + " bytes)"; !strings.Contains(gateLine, want) {
		t.Fatalf("save gate missing byte count %q; body:\n%s", want, gateLine)
	}
	if want := filepath.Join(mustPromptsDir(t), "review.txt"); !strings.Contains(gateLine, want) {
		t.Fatalf("save gate missing the target path %q; body:\n%s", want, gateLine)
	}
	if clamped := cellwidth.Truncate(gateLine, w); !frameHasLine(out, clamped) {
		t.Fatalf("save gate frame missing the clamped line %q; frame:\n%s", clamped, out)
	}
	for i, line := range strings.Split(out, "\n") {
		if cw := cellwidth.String(line); cw > w {
			t.Fatalf("save gate frame line %d = %d cells, want at most %d: %q", i, cw, w, line)
		}
	}
	app.Step(KeyYes)
	if got := fanout.TaskPromptSource("review"); got != fanout.PromptSourceFile {
		t.Fatalf("review source after save = %q, want %q", got, fanout.PromptSourceFile)
	}
	saved, err := fanout.ResolveTaskPreset("review")
	if err != nil {
		t.Fatalf("resolve saved review prompt: %v", err)
	}
	if saved != wantText {
		t.Fatalf("saved prompt = %q, want %q", saved, wantText)
	}
	if top := app.Top(); top == nil || top.Title() != "root" {
		t.Fatalf("top after save = %v, want root screen", top.Title())
	}
	if msg := app.Message(); !strings.Contains(msg, "Prompt saved:") {
		t.Fatalf("message after save = %q, want the saved notice", msg)
	}
}

// TestPromptEditorResetDeletesBehindGate seeds a file override, declines
// the gate once (file stays), then approves it: the file is deleted, the
// compiled constant is back in force, and the reset notice names the
// path.
func TestPromptEditorResetDeletesBehindGate(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("security", "override text"); err != nil {
		t.Fatalf("seed security override: %v", err)
	}
	app := newPromptEditorApp("security")
	for _, k := range []Key{KeyTab, KeyDown, KeyEnter} {
		app.Step(k)
	}
	if top := app.Top(); top.Mode() != ModeNav {
		t.Fatalf("mode on action rows = %v, want ModeNav", top.Mode())
	}
	out := app.Frame().String()
	if !strings.Contains(out, "Restore security task prompt to the compiled default text?") {
		t.Fatalf("reset gate missing; frame:\n%s", out)
	}
	app.Step(KeyNo)
	if _, err := os.Stat(filepath.Join(mustPromptsDir(t), "security.txt")); err != nil {
		t.Fatalf("gate decline must keep the file: %v", err)
	}
	if msg := app.Message(); !strings.Contains(msg, "Cancelled. Nothing was written.") {
		t.Fatalf("message after declined reset = %q", msg)
	}
	for _, k := range []Key{KeyEnter, KeyYes} {
		app.Step(k)
	}
	if _, err := os.Stat(filepath.Join(mustPromptsDir(t), "security.txt")); !os.IsNotExist(err) {
		t.Fatalf("approved reset must delete the file, stat err = %v", err)
	}
	if got := fanout.TaskPromptSource("security"); got != fanout.PromptSourceCompiled {
		t.Fatalf("security source after reset = %q, want %q", got, fanout.PromptSourceCompiled)
	}
	if msg := app.Message(); !strings.Contains(msg, "Override file deleted:") {
		t.Fatalf("message after reset = %q, want the reset notice", msg)
	}
}

// TestPromptEditorResetWithoutFile pins the honest no-op: with no slot
// file the reset row answers with a message instead of a gate.
func TestPromptEditorResetWithoutFile(t *testing.T) {
	promptTestHome(t)
	app := newPromptEditorApp("research")
	for _, k := range []Key{KeyTab, KeyDown, KeyEnter} {
		app.Step(k)
	}
	if app.Modal() != nil {
		t.Fatalf("reset without a file must not open a gate")
	}
	if msg := app.Message(); !strings.Contains(msg, "No override file exists") {
		t.Fatalf("message = %q, want the no-override notice", msg)
	}
}

// TestPromptEditorCredentialGate pins the credential-shaped flow: the
// buffer triggers a warning on the message line and a y/n gate before
// the save confirm; declining writes nothing and keeps the buffer, while
// approving both gates saves the file.
func TestPromptEditorCredentialGate(t *testing.T) {
	promptTestHome(t)
	const secret = "sk-abcdef0123456789"
	app := newPromptEditorApp("ideas")
	for _, k := range append(typeRunes(secret), KeyCtrlS) {
		app.Step(k)
	}
	if msg := app.Message(); !strings.Contains(msg, "looks like secret or key material") {
		t.Fatalf("message line = %q, want the credential warning", msg)
	}
	out := app.Frame().String()
	if !strings.Contains(out, "Save secret-looking text?") {
		t.Fatalf("credential gate not open; frame:\n%s", out)
	}
	// The gate (and the warning) must never echo the buffer. The editor
	// viewport itself shows what the user typed, so scope the assertion
	// to the modal body.
	if app.Modal() == nil {
		t.Fatalf("no modal live at the credential gate")
	}
	if body := strings.Join(app.Modal().Body(app.Width()), "\n"); strings.Contains(body, secret) {
		t.Fatalf("credential gate echoes the buffer content; body:\n%s", body)
	}
	app.Step(KeyNo)
	if _, err := os.Stat(filepath.Join(mustPromptsDir(t), "ideas.txt")); !os.IsNotExist(err) {
		t.Fatalf("declined credential save must not write, stat err = %v", err)
	}
	if !strings.Contains(app.Frame().String(), secret) {
		t.Fatalf("declining the gate must keep the buffer; frame:\n%s", app.Frame().String())
	}
	for _, k := range []Key{KeyCtrlS, KeyYes, KeyYes} {
		app.Step(k)
	}
	if got := fanout.TaskPromptSource("ideas"); got != fanout.PromptSourceFile {
		t.Fatalf("ideas source after approved save = %q, want %q", got, fanout.PromptSourceFile)
	}
}

// TestPromptEditorDirtyEscAsksDiscard pins the Esc-on-dirty guard: the
// App precedence would pop silently, so the guard intercepts Esc and
// asks; declining preserves the buffer and re-arms, approving pops.
func TestPromptEditorDirtyEscAsksDiscard(t *testing.T) {
	promptTestHome(t)
	app := newPromptEditorApp("review")
	for _, k := range typeRunes("x") {
		app.Step(k)
	}
	app.Step(KeyEsc)
	out := app.Frame().String()
	if !strings.Contains(out, "Discard unsaved edits?") {
		t.Fatalf("dirty Esc must open the discard gate; frame:\n%s", out)
	}
	app.Step(KeyNo)
	if !strings.Contains(app.Frame().String(), "xYou are a Staff Code Reviewer") {
		t.Fatalf("declined discard must keep the buffer; frame:\n%s", app.Frame().String())
	}
	app.Step(KeyEsc)
	app.Step(KeyYes)
	if top := app.Top(); top == nil || top.Title() != "root" {
		t.Fatalf("top after approved discard = %v, want root screen", top.Title())
	}
	if got := fanout.TaskPromptSource("review"); got != fanout.PromptSourceCompiled {
		t.Fatalf("review source after discard = %q, want %q", got, fanout.PromptSourceCompiled)
	}
}

// TestPromptEditorSaveConfirmNotesSeatEnv pins the honest env note: with
// AGY_PRO_SEAT_PREAMBLE set, the seat save gate warns that the env rung
// still wins over the file being written.
func TestPromptEditorSaveConfirmNotesSeatEnv(t *testing.T) {
	promptTestHome(t)
	t.Setenv(fanout.EnvSeatPreamble, "inline preamble override")
	app := newPromptEditorApp("seat")
	for _, k := range append(typeRunes("edited preamble"), KeyCtrlS) {
		app.Step(k)
	}
	out := app.Frame().String()
	if !strings.Contains(out, "AGY_PRO_SEAT_PREAMBLE is set") {
		t.Fatalf("seat save gate missing the env note; frame:\n%s", out)
	}
}

// TestPromptTextLooksSecret pins the conservative credential shape:
// natural prompt sentences, URLs, and paths never match; key prefixes
// and opaque mixed tokens do. The token floor is the keyaudit AKIA-class
// 16 runes (aligned with the model form classifier), so a 16-23 rune
// credential triggers the gate while a 39-char identifier-shaped string
// with separators does not.
func TestPromptTextLooksSecret(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain sentence", "You are a Staff Code Reviewer conducting a rigorous review.", false},
		{"long prose with dashes", "Double-blind, cross-verified - cite exact files and line numbers.", false},
		{"url line", "See https://example.com/docs/reference-guide for details.", false},
		{"windows path line", `Read C:\Users\tester\project\prompts\review.txt first.`, false},
		{"short token", "sk-abc", true},
		{"key prefix", "sk-abcdef0123456789", true},
		{"bearer prefix", "Bearer abcdef0123456789", true},
		{"opaque mixed token", "AbCdEf0123456789GhIjKl9876543210", true},
		{"token inside prose", "Use key AbCdEf0123456789GhIjKl9876543210 for the API.", false},
		{"akia class 20 rune", testfixtures.AKIAExample, true},
		{"16 rune opaque token", "ABCD1234EFGH5678", true},
		{"23 rune opaque token", "AbCdEf0123456789GhIjKl9", true},
		{"15 rune run stays readable", "AbCdEf012345678", false},
		{"39 char identifier shaped", "meta-llama/Llama-3.3-70B-Instruct-Turbo", false},
		{"long prose with punctuation", "Summarize the findings - files, line numbers, and severity - in plain prose, please.", false},
	}
	for _, tc := range cases {
		if got := promptTextLooksSecret(tc.text); got != tc.want {
			t.Errorf("%s: promptTextLooksSecret(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

// mustPromptsDir resolves the prompt store directory the tests just
// wrote into.
func mustPromptsDir(t *testing.T) string {
	t.Helper()
	dir, err := fanout.PromptsDir()
	if err != nil {
		t.Fatalf("resolve prompts dir: %v", err)
	}
	return dir
}

// promptEditorTop returns the prompt editor that owns the app's top slot.
func promptEditorTop(t *testing.T, app *App) *promptEditorScreen {
	t.Helper()
	s, ok := app.Top().(*promptEditorScreen)
	if !ok {
		t.Fatalf("top screen = %T, want *promptEditorScreen", app.Top())
	}
	return s
}

// promptCaretHeaderCount independently derives how many lines Draw renders
// above the editor window from the screen state and the documented layout
// (status always, the resolve error when one is recorded, the
// compiled-default note only with no override file, the seat guardrail only
// on the seat slot). The Caret pins compare the reported body line against
// this derivation instead of re-running the production helper.
func promptCaretHeaderCount(s *promptEditorScreen) int {
	n := 1 // the status line
	if s.loadErr != "" {
		n++
	}
	if !s.hasFile && s.source == fanout.PromptSourceCompiled {
		n++
	}
	if s.slot == "seat" {
		n++
	}
	return n
}

// promptCaretLineText returns the text of the caret's own line through the
// public editor text, so a body-line pin compares against the buffer rather
// than against the screen's own window slice.
func promptCaretLineText(t *testing.T, s *promptEditorScreen) string {
	t.Helper()
	row, _ := s.ed.Caret()
	return strings.Split(s.ed.Text(), "\n")[row]
}

// promptSeedSlot writes one slot override through the same store rung the
// screen reads: the seat preamble has its own file and save verb, and
// SaveTaskPrompt maps the seat name onto the review slot.
func promptSeedSlot(t *testing.T, slot, text string) {
	t.Helper()
	var err error
	if slot == "seat" {
		_, err = fanout.SaveSeatPrompt(text)
	} else {
		_, err = fanout.SaveTaskPrompt(slot, text)
	}
	if err != nil {
		t.Fatalf("seed %s override: %v", slot, err)
	}
}

// TestPromptEditorCaretBodyLineTracksLayout pins the body-line computation
// against real Draw output at several geometries and header shapes: the
// status line only (a file override exists), status plus the
// compiled-default note (no override), status plus note plus the seat
// guardrail, and a narrow frame. In every case the reported body line must
// be exactly one past the header block (the blank separator) plus the
// window row Draw renders, and the Draw line there must be the caret's own
// line.
func TestPromptEditorCaretBodyLineTracksLayout(t *testing.T) {
	cases := []struct {
		name       string
		slot       string
		seed       bool
		width      int
		height     int
		wantHeader int
	}{
		{"review default note 80x24", "review", false, 80, 24, 2},
		{"review default note 110x40", "review", false, 110, 40, 2},
		{"seat note plus guardrail", "seat", false, 80, 24, 3},
		{"seat file override guardrail only", "seat", true, 80, 24, 2},
		{"review file override", "review", true, 80, 24, 1},
		{"review file override narrow", "review", true, 52, 16, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			promptTestHome(t)
			if tc.seed {
				promptSeedSlot(t, tc.slot, "seeded alpha\nseeded beta")
			}
			app := NewApp(tc.width, tc.height, DirLTR)
			app.Push(&stubScreen{title: "root", mode: ModeNav})
			app.Push(NewPromptEditorScreen(tc.slot))
			body := app.Top().Draw(app)
			s := promptEditorTop(t, app)

			if got := promptCaretHeaderCount(s); got != tc.wantHeader {
				t.Fatalf("independent header count = %d, want %d", got, tc.wantHeader)
			}
			_, caretRow := s.ed.View()
			line, cell, ok := s.Caret()
			if !ok {
				t.Fatalf("editor has focus, Caret() reported none")
			}
			wantLine := tc.wantHeader + 1 + caretRow
			if line != wantLine {
				t.Fatalf("Caret bodyLine = %d, want %d (header %d + blank + window row %d)",
					line, wantLine, tc.wantHeader, caretRow)
			}
			if line-1 < 0 || body[line-1] != "" {
				t.Fatalf("Draw line %d (above the caret row) is not the blank separator: %q", line-1, body[line-1])
			}
			wantText := cellwidth.Truncate(promptCaretLineText(t, s), tc.width)
			if body[line] != wantText {
				t.Fatalf("Draw line %d = %q, want the caret line %q", line, body[line], wantText)
			}
			if want := s.ed.CaretCell(); cell != want {
				t.Fatalf("Caret cell = %d, want Editor.CaretCell() = %d", cell, want)
			}
		})
	}
}

// TestPromptEditorCaretCJKCell pins the display-cell offset the contract
// asks for: a CJK rune occupies two cells, so a caret behind one reports
// cell 2, and the row it points at is the CJK line of the buffer.
func TestPromptEditorCaretCJKCell(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "가나다\nplain"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := newPromptEditorApp("review")
	s := promptEditorTop(t, app)
	app.Frame()

	app.Step(KeyEnd)
	line, cell, ok := s.Caret()
	if !ok {
		t.Fatalf("editor has focus, Caret() reported none")
	}
	if cell != 6 {
		t.Fatalf("caret cell behind three CJK runes = %d, want 6", cell)
	}
	body := s.Draw(app)
	if body[line] != "가나다" {
		t.Fatalf("Draw line %d = %q, want the CJK caret line", line, body[line])
	}
}

// TestPromptEditorCaretKeyDispatch drives every editor key through the
// screen's real dispatch path (App.Step, the dirty guard included once the
// buffer is dirty) and pins the caret after each one: Left, Right, Home,
// End, Down, Delete, Backspace, and rune insertion all reach the editor and
// move the caret the way the editor defines.
func TestPromptEditorCaretKeyDispatch(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "abc\ndef"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := newPromptEditorApp("review")
	s := promptEditorTop(t, app)
	app.Frame()

	cases := []struct {
		name string
		key  Key
		row  int
		col  int
		cell int
		line string // caret line after the key; "" leaves it unchecked
	}{
		{"right", KeyRight, 0, 1, 1, ""},
		{"right again", KeyRight, 0, 2, 2, ""},
		{"left", KeyLeft, 0, 1, 1, ""},
		{"end", KeyEnd, 0, 3, 3, "abc"},
		{"home", KeyHome, 0, 0, 0, ""},
		{"down", KeyDown, 1, 0, 0, "def"},
		{"end on second line", KeyEnd, 1, 3, 3, "def"},
		{"rune insert", RuneKey('Z'), 1, 4, 4, "defZ"},
		{"backspace", KeyBackspace, 1, 3, 3, "def"},
		{"delete at line end", KeyDelete, 1, 3, 3, "def"},
		{"home again", KeyHome, 1, 0, 0, "def"},
		{"delete", KeyDelete, 1, 0, 0, "ef"},
	}
	for _, tc := range cases {
		app.Step(tc.key)
		if app.Modal() != nil && app.Modal() != s.guard {
			t.Fatalf("%s: unexpected gate %v on the frame", tc.name, app.Modal())
		}
		row, col := s.ed.Caret()
		if row != tc.row || col != tc.col {
			t.Fatalf("%s: editor caret = (%d,%d), want (%d,%d)", tc.name, row, col, tc.row, tc.col)
		}
		line, cell, ok := s.Caret()
		if !ok {
			t.Fatalf("%s: Caret() reported none while the editor had focus", tc.name)
		}
		if cell != tc.cell {
			t.Fatalf("%s: Caret cell = %d, want %d", tc.name, cell, tc.cell)
		}
		body := s.Draw(app)
		if tc.line != "" && body[line] != tc.line {
			t.Fatalf("%s: Draw line %d = %q, want the caret line %q", tc.name, line, body[line], tc.line)
		}
	}
	if got := s.ed.Text(); got != "abc\nef" {
		t.Fatalf("buffer after the key sequence = %q, want %q", got, "abc\nef")
	}
	if !s.ed.Dirty() {
		t.Fatalf("buffer must be dirty after rune insertion, Backspace, and Delete")
	}
}

// TestPromptEditorCaretTracksViewportScroll pins the caret against the
// scrolled window: the body line follows the window row as Down and PgDn
// scroll the buffer, the Draw line at that index is the caret's own line,
// and a stale window whose top sits past the caret row reports no caret.
func TestPromptEditorCaretTracksViewportScroll(t *testing.T) {
	promptTestHome(t)
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	if _, err := fanout.SaveTaskPrompt("review", strings.TrimSuffix(b.String(), "\n")); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptEditorScreen("review"))
	s := promptEditorTop(t, app)
	s.Draw(app)

	if page := s.ed.PageSize(); page != pageSizeFor(24, 9) {
		t.Fatalf("editor page = %d, want %d", page, pageSizeFor(24, 9))
	}
	for i := 0; i < 20; i++ {
		app.Step(KeyDown)
	}
	if row, _ := s.ed.Caret(); row != 20 {
		t.Fatalf("caret row after 20 Down = %d, want 20", row)
	}
	if top := s.ed.Top(); top != 6 {
		t.Fatalf("window top after 20 Down = %d, want 6", top)
	}
	body := s.Draw(app)
	line, cell, ok := s.Caret()
	if !ok {
		t.Fatalf("caret scrolled into the window must report a caret")
	}
	if cell != 0 {
		t.Fatalf("caret cell on an untouched line = %d, want 0", cell)
	}
	// The body line uses the WINDOW row (14), never the absolute editor row
	// (20): the caret's frame position is relative to the viewport.
	_, caretRow := s.ed.View()
	if wantLine := promptCaretHeaderCount(s) + 1 + caretRow; line != wantLine {
		t.Fatalf("scrolled Caret bodyLine = %d, want %d (header %d + blank + window row %d)",
			line, wantLine, promptCaretHeaderCount(s), caretRow)
	}
	if body[line] != "line 21" {
		t.Fatalf("Draw line %d = %q, want the caret line %q", line, body[line], "line 21")
	}

	// PgDn moves a whole page and scrolls the window with it.
	app.Step(KeyPgDn)
	body = s.Draw(app)
	line, _, ok = s.Caret()
	if !ok {
		t.Fatalf("caret after PgDn must report a caret")
	}
	want := cellwidth.Truncate(promptCaretLineText(t, s), 80)
	if body[line] != want {
		t.Fatalf("after PgDn: Draw line %d = %q, want the caret line %q", line, body[line], want)
	}
	if body[line] != "line 36" {
		t.Fatalf("after PgDn: Draw line %d = %q, want %q", line, body[line], "line 36")
	}

	// A stale window that opens below the caret row has no caret to place:
	// the key surface cannot produce this (ensureVisible keeps the caret in
	// the window), so the pin forces the window directly.
	s.ed.top = 30
	s.ed.row = 5
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("a caret row outside the window must report no caret")
	}
}

// TestPromptEditorCaretHiddenWithoutFocus pins every state in which the
// editor is not the live editing surface: the action rows own the keys, a
// save or reset or credential gate is live, the discard gate is live, a
// resolve error left the buffer untrustworthy, the screen is covered by
// another screen, and a screen that was never drawn has no app at all.
func TestPromptEditorCaretHiddenWithoutFocus(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "gate text"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := newPromptEditorApp("review")
	s := promptEditorTop(t, app)
	app.Frame()
	if _, _, ok := s.Caret(); !ok {
		t.Fatalf("editor focus must report a caret")
	}

	// Action rows are ModeNav: arrows and digits navigate, no text caret.
	app.Step(KeyTab)
	if s.Mode() != ModeNav {
		t.Fatalf("mode on the action rows = %v, want ModeNav", s.Mode())
	}
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("action rows focused: caret must not show")
	}
	app.Step(KeyLeft)
	if _, _, ok := s.Caret(); !ok {
		t.Fatalf("back on the text: caret must show again")
	}

	// Save gate.
	app.Step(KeyCtrlS)
	if app.Modal() == nil || app.Modal() == s.guard {
		t.Fatalf("save gate not live; modal = %v", app.Modal())
	}
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("save gate live: caret must not show")
	}
	app.Step(KeyNo)
	if _, _, ok := s.Caret(); !ok {
		t.Fatalf("after the save gate closed: caret must show again")
	}

	// Discard gate over a dirty buffer.
	app.Step(RuneKey('x'))
	if app.Modal() != s.guard {
		t.Fatalf("dirty guard not armed; modal = %v", app.Modal())
	}
	app.Step(KeyEsc)
	if app.Modal() == nil || app.Modal() == s.guard {
		t.Fatalf("discard gate not live; modal = %v", app.Modal())
	}
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("discard gate live: caret must not show")
	}
	app.Step(KeyNo)
	if app.Modal() != s.guard {
		t.Fatalf("declining discard must re-arm the guard; modal = %v", app.Modal())
	}
	if _, _, ok := s.Caret(); !ok {
		t.Fatalf("guard re-armed: caret must show again")
	}

	// Credential gate: a secret-shaped buffer gates the save first.
	app.Step(KeyHome)
	for _, k := range typeRunes("sk-abcdef0123456789") {
		app.Step(k)
	}
	app.Step(KeyCtrlS)
	if app.Modal() == nil || app.Modal() == s.guard {
		t.Fatalf("credential gate not live; modal = %v", app.Modal())
	}
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("credential gate live: caret must not show")
	}
	app.Step(KeyNo)
	if app.Modal() != s.guard {
		t.Fatalf("declining the credential gate must re-arm the guard; modal = %v", app.Modal())
	}

	// Reset gate from the action rows.
	app.Step(KeyTab)
	app.Step(KeyDown)
	app.Step(KeyEnter)
	if app.Modal() == nil || app.Modal() == s.guard {
		t.Fatalf("reset gate not live; modal = %v", app.Modal())
	}
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("reset gate live: caret must not show")
	}
	app.Step(KeyNo)
	app.Step(KeyLeft)
	if _, _, ok := s.Caret(); !ok {
		t.Fatalf("after the reset gate closed and the text refocused: caret must show again")
	}

	// A resolve error leaves the buffer untrustworthy; the state is forced
	// here because NewPromptEditorScreen only reaches it when the store
	// itself is unreadable.
	s.loadErr = "prompt store unreadable"
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("load-error state: caret must not show")
	}
	s.loadErr = ""

	// A covered screen owns no frame.
	app.Push(&stubScreen{title: "other", mode: ModeNav})
	if _, _, ok := s.Caret(); ok {
		t.Fatalf("covered screen: caret must not show")
	}

	// A screen that was never attached to an app has no frame either.
	orphan := NewPromptEditorScreen("review").(*promptEditorScreen)
	if _, _, ok := orphan.Caret(); ok {
		t.Fatalf("screen without an app: Caret must report none")
	}
}

// TestPromptEditorCaretSurvivesDirtyGuard pins the documented exception:
// the dirty guard is a transparent modal that forwards every key to the
// editor, so the caret stays on the frame while it is armed (it is armed
// from the first keystroke on), and a key the guard forwards still moves
// it.
func TestPromptEditorCaretSurvivesDirtyGuard(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "abc"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := newPromptEditorApp("review")
	s := promptEditorTop(t, app)
	app.Frame()

	app.Step(RuneKey('x'))
	if app.Modal() != s.guard {
		t.Fatalf("dirty guard not armed; modal = %v", app.Modal())
	}
	line, cell, ok := s.Caret()
	if !ok {
		t.Fatalf("dirty guard armed: caret must still show")
	}
	if cell != 1 {
		t.Fatalf("caret cell after one rune = %d, want 1", cell)
	}
	body := s.Draw(app)
	if body[line] != "xabc" {
		t.Fatalf("Draw line %d = %q, want the edited caret line %q", line, body[line], "xabc")
	}
	app.Step(KeyEnd)
	if _, cell, ok := s.Caret(); !ok || cell != 4 {
		t.Fatalf("End through the guard: caret = (ok %v, cell %d), want (true, 4)", ok, cell)
	}
}

// The screen is a CaretSource: the frame composition queries the top screen
// through this interface, and a signature drift would silently drop the
// caret from every frame instead of failing the build.
var _ CaretSource = (*promptEditorScreen)(nil)

// promptCaretFrameStart independently derives the frame line index where the
// body block begins for the composition path the frame pins use: the title
// line plus one blank separator, and, when a banner block is passed, the
// banner lines plus their separator.
func promptCaretFrameStart(bannerLines int) int {
	if bannerLines > 0 {
		return bannerLines + 1 + 1 + 1
	}
	return 1 + 1
}

// promptCaretCellRune returns the rune the caret cell covers in line: the
// rune whose display-cell span contains cell (CJK spans two cells). It is an
// independent walk over cellwidth, not the painter's splitter, so a painter
// that marked the wrong rune would fail the pin.
func promptCaretCellRune(t *testing.T, line string, cell int) string {
	t.Helper()
	pos := 0
	for _, r := range line {
		w := cellwidth.Rune(r)
		if cell < pos+w {
			return string(r)
		}
		pos += w
	}
	t.Fatalf("caret cell %d is past the end of line %q", cell, line)
	return ""
}

// TestPromptEditorCaretFrameComposes pins the composed frame fields through
// App.Frame / App.ComposeFrame (the same path the frame tests use): the
// caret line is the screen's body line translated by the origin of the body
// block, the line it points at is the screen's own Draw line, and a banner
// block shifts that origin by exactly its height. A geometry whose body is
// cut before the caret row reports -1 rather than a guessed line.
func TestPromptEditorCaretFrameComposes(t *testing.T) {
	cases := []struct {
		name   string
		slot   string
		seed   bool
		width  int
		height int
		cut    bool
	}{
		{"review default 80x24", "review", false, 80, 24, false},
		{"review file 110x40", "review", true, 110, 40, false},
		{"seat default 60x20", "seat", false, 60, 20, false},
		{"review default 80x7 body cut", "review", false, 80, 7, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			promptTestHome(t)
			if tc.seed {
				if _, err := fanout.SaveTaskPrompt(tc.slot, "abc\ndef"); err != nil {
					t.Fatalf("seed %s override: %v", tc.slot, err)
				}
			}
			app := NewApp(tc.width, tc.height, DirLTR)
			app.Push(&stubScreen{title: "root", mode: ModeNav})
			app.Push(NewPromptEditorScreen(tc.slot))
			s := promptEditorTop(t, app)

			app.Step(KeyRight) // focus stays on the text; caret moves one cell
			bodyLine, cell, ok := s.Caret()
			if !ok {
				t.Fatalf("editor has focus, Caret() reported none")
			}
			if cell != 1 {
				t.Fatalf("caret cell after one Right = %d, want 1", cell)
			}
			body := s.Draw(app)

			f := app.Frame()
			if tc.cut {
				if f.CaretLine != -1 || f.CaretCell != -1 {
					t.Fatalf("cut body: frame caret = (%d,%d), want (-1,-1)", f.CaretLine, f.CaretCell)
				}
			} else {
				want := promptCaretFrameStart(0) + bodyLine
				if f.CaretLine != want {
					t.Fatalf("frame CaretLine = %d, want %d (origin + body line %d)", f.CaretLine, want, bodyLine)
				}
				if f.CaretCell != cell {
					t.Fatalf("frame CaretCell = %d, want %d", f.CaretCell, cell)
				}
				if f.Lines[f.CaretLine] != body[bodyLine] {
					t.Fatalf("frame line %d = %q, want the Draw body line %d = %q",
						f.CaretLine, f.Lines[f.CaretLine], bodyLine, body[bodyLine])
				}
			}

			// A banner block is part of the frame origin: the caret must move
			// down by exactly the banner height plus its separator.
			cf := app.ComposeFrame([]string{"banner art"})
			if tc.cut {
				if cf.Frame.CaretLine != -1 || cf.Frame.CaretCell != -1 {
					t.Fatalf("cut body with banner: frame caret = (%d,%d), want (-1,-1)",
						cf.Frame.CaretLine, cf.Frame.CaretCell)
				}
				return
			}
			want := promptCaretFrameStart(1) + bodyLine
			if cf.Frame.CaretLine != want {
				t.Fatalf("banner frame CaretLine = %d, want %d (banner origin + body line %d)",
					cf.Frame.CaretLine, want, bodyLine)
			}
			if cf.Frame.CaretCell != cell {
				t.Fatalf("banner frame CaretCell = %d, want %d", cf.Frame.CaretCell, cell)
			}
			if cf.Frame.Lines[cf.Frame.CaretLine] != body[bodyLine] {
				t.Fatalf("banner frame line %d = %q, want the Draw body line %q",
					cf.Frame.CaretLine, cf.Frame.Lines[cf.Frame.CaretLine], body[bodyLine])
			}
		})
	}
}

// TestPromptEditorCaretGateDropsFrameCaret pins the modal rule at frame
// level: while a save gate is live the composed frame carries no caret at
// all (-1, -1), and once the gate closes the caret returns on the same
// line.
func TestPromptEditorCaretGateDropsFrameCaret(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "abc"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptEditorScreen("review"))
	promptEditorTop(t, app) // the screen under test owns the top slot
	app.Step(KeyRight)

	before := app.Frame()
	if before.CaretLine < 0 {
		t.Fatalf("frame before the gate has no caret")
	}
	app.Step(KeyCtrlS)
	if app.Modal() == nil {
		t.Fatalf("save gate not live")
	}
	gated := app.Frame()
	if gated.CaretLine != -1 || gated.CaretCell != -1 {
		t.Fatalf("save gate live: frame caret = (%d,%d), want (-1,-1)", gated.CaretLine, gated.CaretCell)
	}
	app.Step(KeyNo)
	after := app.Frame()
	if after.CaretLine != before.CaretLine || after.CaretCell != before.CaretCell {
		t.Fatalf("caret after the gate closed = (%d,%d), want (%d,%d)",
			after.CaretLine, after.CaretCell, before.CaretLine, before.CaretCell)
	}
}

// TestPromptEditorCaretPainterGlyph pins the painted output at the caret
// row: a color tier wraps exactly the rune the caret cell covers in the
// reverse-video pair (one rune only, no other line carries it), and the
// plain tier emits zero escape bytes with the printable marker sitting at
// the same display cell.
func TestPromptEditorCaretPainterGlyph(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "abc"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptEditorScreen("review"))
	promptEditorTop(t, app)
	app.Step(KeyRight)
	f := app.Frame()
	if f.CaretLine < 0 {
		t.Fatalf("frame has no caret")
	}
	if f.CaretCell != 1 {
		t.Fatalf("frame CaretCell = %d, want 1", f.CaretCell)
	}
	caretText := f.Lines[f.CaretLine]
	if caretText != "abc" {
		t.Fatalf("caret frame line = %q, want the seeded line %q", caretText, "abc")
	}
	if got := promptCaretCellRune(t, caretText, f.CaretCell); got != "b" {
		t.Fatalf("run rune under the caret cell = %q, want %q", got, "b")
	}

	var console bytes.Buffer
	if err := NewPainter(&console, Cap{Tier: Tier256, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
		t.Fatalf("console paint: %v", err)
	}
	if n := strings.Count(console.String(), caretSGRIn); n != 1 {
		t.Fatalf("console output carries %d caret SGR opens, want exactly 1; output:\n%q", n, console.String())
	}
	if n := strings.Count(console.String(), caretSGROut); n != 1 {
		t.Fatalf("console output carries %d caret SGR closes, want exactly 1; output:\n%q", n, console.String())
	}
	consoleLines := strings.Split(console.String(), "\n")
	if len(consoleLines) != len(f.Lines) {
		t.Fatalf("console painted %d lines, want %d", len(consoleLines), len(f.Lines))
	}
	row := consoleLines[f.CaretLine]
	open := strings.Index(row, caretSGRIn)
	closeAt := strings.Index(row, caretSGROut)
	if open < 0 || closeAt < open {
		t.Fatalf("caret SGR pair missing from the caret line: %q", row)
	}
	if inner := row[open+len(caretSGRIn) : closeAt]; inner != "b" {
		t.Fatalf("caret SGR wraps %q, want exactly the caret rune %q", inner, "b")
	}
	for i, line := range consoleLines {
		if i != f.CaretLine && (strings.Contains(line, caretSGRIn) || strings.Contains(line, caretSGROut)) {
			t.Fatalf("line %d carries caret SGR outside the caret row: %q", i, line)
		}
	}

	plainRow := promptPlainCaretRow(t, f)
	promptPlainCaretSplice(t, plainRow, caretText, f.CaretCell)

	// A caret line whose own text already contains the marker character is the
	// case a text search cannot report: the marker is in-band on this tier, so
	// the painted row carries two "|" and only the cell says which one the
	// painter inserted (finding C). The color tier stays exact on the same
	// content, because its caret is an SGR pair rather than a cell.
	if _, err := fanout.SaveTaskPrompt("review", "a|bc"); err != nil {
		t.Fatalf("seed pipe-bearing review override: %v", err)
	}
	pipeApp := NewApp(80, 24, DirLTR)
	pipeApp.Push(&stubScreen{title: "root", mode: ModeNav})
	pipeApp.Push(NewPromptEditorScreen("review"))
	promptEditorTop(t, pipeApp)
	for i := 0; i < 3; i++ {
		pipeApp.Step(KeyRight)
	}
	pf := pipeApp.Frame()
	if pf.CaretCell != 3 {
		t.Fatalf("pipe frame CaretCell = %d, want 3", pf.CaretCell)
	}
	pipeText := pf.Lines[pf.CaretLine]
	if pipeText != "a|bc" {
		t.Fatalf("pipe caret frame line = %q, want %q", pipeText, "a|bc")
	}
	if got := promptCaretCellRune(t, pipeText, pf.CaretCell); got != "c" {
		t.Fatalf("pipe rune under the caret cell = %q, want %q", got, "c")
	}
	pipeRow := promptPlainCaretRow(t, pf)
	promptPlainCaretSplice(t, pipeRow, pipeText, pf.CaretCell)

	var pipeConsole bytes.Buffer
	if err := NewPainter(&pipeConsole, Cap{Tier: Tier256, Palette: DefaultPalette()}).Paint(pf, nil); err != nil {
		t.Fatalf("pipe console paint: %v", err)
	}
	pipeConsoleLines := strings.Split(pipeConsole.String(), "\n")
	if len(pipeConsoleLines) != len(pf.Lines) {
		t.Fatalf("pipe console painted %d lines, want %d", len(pipeConsoleLines), len(pf.Lines))
	}
	pipeColorRow := pipeConsoleLines[pf.CaretLine]
	pipeOpen := strings.Index(pipeColorRow, caretSGRIn)
	pipeClose := strings.Index(pipeColorRow, caretSGROut)
	if pipeOpen < 0 || pipeClose < pipeOpen {
		t.Fatalf("pipe caret SGR pair missing from the caret line: %q", pipeColorRow)
	}
	if inner := pipeColorRow[pipeOpen+len(caretSGRIn) : pipeClose]; inner != "c" {
		t.Fatalf("pipe caret SGR wraps %q, want exactly the caret rune %q", inner, "c")
	}
}

// promptPlainCaretRow paints f on the plain tier and returns the painted caret
// row after asserting the invariants every plain frame owes: zero escape bytes,
// no line added or dropped, and every non-caret line byte-identical to the
// frame.
func promptPlainCaretRow(t *testing.T, f Frame) string {
	t.Helper()
	var plain bytes.Buffer
	if err := NewPainter(&plain, plainTestCap()).Paint(f, nil); err != nil {
		t.Fatalf("plain paint: %v", err)
	}
	if bytes.ContainsRune(plain.Bytes(), 0x1b) {
		t.Fatalf("plain tier emitted an escape byte: %q", plain.String())
	}
	lines := strings.Split(plain.String(), "\n")
	if len(lines) != len(f.Lines) {
		t.Fatalf("plain painted %d lines, want %d", len(lines), len(f.Lines))
	}
	for i, line := range lines {
		if i != f.CaretLine && line != f.Lines[i] {
			t.Fatalf("plain line %d = %q, want the frame line %q", i, line, f.Lines[i])
		}
	}
	return lines[f.CaretLine]
}

// promptPlainCaretSplice pins the plain-tier marker without searching the row
// for its text: the marker is one cell wide and is inserted at the caret cell,
// so the row must carry the marker's bytes at the byte offset of that cell,
// occupy exactly one cell more than the frame line (these fixtures are narrower
// than the frame, so no tail clamp applies), and reproduce the frame line once
// those bytes are removed. The offset is resolved by promptCellByteOffset, an
// independent walk of the row, so a regression in the mapper the painter splices
// with cannot be read back through the same function. A row whose own text
// contains a "|" therefore cannot mis-report the marker's position the way a
// strings.Index lookup would, which reports the content pipe instead of the
// inserted one (finding C).
func promptPlainCaretSplice(t *testing.T, row, wantLine string, cell int) {
	t.Helper()
	off := promptCellByteOffset(t, row, cell)
	if !strings.HasPrefix(row[off:], caretMarker) {
		t.Fatalf("cell %d of the plain row %q does not start the caret marker", cell, row)
	}
	if got, want := cellwidth.String(row), cellwidth.String(wantLine)+1; got != want {
		t.Fatalf("plain row occupies %d cells, want %d (the frame line plus one marker cell)", got, want)
	}
	if got := row[:off] + row[off+len(caretMarker):]; got != wantLine {
		t.Fatalf("removing the plain marker yields %q, want the frame line %q", got, wantLine)
	}
}

// promptCellByteOffset resolves the byte offset of the rune that covers display
// cell cell in a painted row by walking the row's own cell spans. It repeats the
// mapping on purpose instead of calling caretByteOffset: a pin that asked the
// production mapper where the marker should be would follow a broken mapper and
// could never report the regression. A cell at the row's cell count is the
// end-of-line insertion point; a cell past it is a fixture error, because the
// painter draws no marker there at all.
func promptCellByteOffset(t *testing.T, row string, cell int) int {
	t.Helper()
	pos := 0
	for i, r := range row {
		w := cellwidth.Rune(r)
		if w == 0 {
			continue
		}
		if cell < pos+w {
			return i
		}
		pos += w
	}
	if cell == pos {
		return len(row)
	}
	t.Fatalf("cell %d is past the end of the painted row %q (%d cells)", cell, row, pos)
	return 0
}

// TestPromptPlainCaretSpliceIsMapperIndependent pins the locator itself on
// hand-built frames, one of which carries the marker character in its content.
// The frame is painted by the real painter, so the marker lands wherever the
// production mapper puts it, while the splice helper resolves the cell with its
// own walk: a mapper regression fails here instead of being agreed with. The
// pipe cases are the ones a text search cannot read (fixB item C), kept next to
// the independent walk so the two properties are pinned together.
func TestPromptPlainCaretSpliceIsMapperIndependent(t *testing.T) {
	cases := []struct {
		name string
		line string
		cell int
	}{
		{"plain ascii", "abc", 1},
		{"literal pipe in the content", "a|bc", 3},
		{"literal pipe under the caret", "a|bc", 2},
		{"end-of-line caret", "abc", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Frame{Width: 40, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: tc.cell, Lines: []string{tc.line}}
			row := promptPlainCaretRow(t, f)
			promptPlainCaretSplice(t, row, tc.line, tc.cell)
		})
	}
}

// TestPromptEditorCaretPainterCJKCell pins the double-width rule on the
// paint path: a caret behind one CJK rune covers two cells, and the SGR
// pair must wrap that whole rune instead of splitting it.
func TestPromptEditorCaretPainterCJKCell(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "가나다"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptEditorScreen("review"))
	promptEditorTop(t, app)
	app.Step(KeyRight)
	f := app.Frame()
	if f.CaretCell != 2 {
		t.Fatalf("frame CaretCell = %d, want 2", f.CaretCell)
	}
	caretText := f.Lines[f.CaretLine]
	if caretText != "가나다" {
		t.Fatalf("caret frame line = %q, want %q", caretText, "가나다")
	}
	if got := promptCaretCellRune(t, caretText, f.CaretCell); got != "나" {
		t.Fatalf("run rune under the caret cell = %q, want %q", got, "나")
	}
	var console bytes.Buffer
	if err := NewPainter(&console, Cap{Tier: Tier256, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
		t.Fatalf("console paint: %v", err)
	}
	row := strings.Split(console.String(), "\n")[f.CaretLine]
	if !strings.Contains(row, caretSGRIn+"나"+caretSGROut) {
		t.Fatalf("caret SGR does not wrap the whole CJK rune: %q", row)
	}
	if cellwidth.String("나") != 2 {
		t.Fatalf("CJK rune width = %d, want 2", cellwidth.String("나"))
	}
}
