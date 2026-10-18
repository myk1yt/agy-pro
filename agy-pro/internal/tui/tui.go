// Package tui is the platform-neutral core of the agy-pro setup TUI:
// the extended key enum (keys.go), the frame snapshot model and the
// mode-scoped dispatcher (tui.go), the shared list/form/editor/confirm
// components (list.go, form.go, editor.go, confirm.go), and the line-mode
// degrade reader (linemode.go). The Windows console input reader, the ANSI
// renderer, capability tiers, and banner art are a later unit (P4); this
// package builds under every GOOS and is fully drivable headless through
// the scripted seams (ScriptedKeys / ScriptedLines / CaptureRun).
//
// Layout contract: every cell computation goes through internal/cellwidth.
// len() and rune count are banned for layout on these surfaces.
package tui

import (
	"fmt"
	"io"
	"strings"

	"agypro/internal/cellwidth"
)

// Dir is the text direction the frame was composed for. It rides along in
// every Frame so the P4 painter can mirror layout when Dir == DirRTL
// (right-aligned columns, gutter and numbering on the right, reversed
// footer). The core composes logical-order text only and never mirrors.
type Dir int

const (
	DirLTR Dir = iota
	DirRTL
)

// Mode is the dispatcher scope of the active surface. The dual meanings of
// digits and y/n resolve through it: digits select in ModeNav (via the
// shared stepList) and become text in ModeEdit (via EditRune); y/n confirm
// in ModeConfirm and become text in ModeEdit. Ctrl+C exits and Ctrl+L
// redraws in every mode.
type Mode int

const (
	ModeNav Mode = iota
	ModeEdit
	ModeConfirm
)

// Frame is one full-screen snapshot: the logical-order text lines plus the
// geometry the painter needs. String joins the lines with "\n"; no trailing
// newline is included.
type Frame struct {
	Width  int
	Height int
	Dir    Dir
	// Trusted is the count of leading Lines that are exempt from the
	// painter's control-character strip: the console banner block, which
	// our own art generator wrote and whose embedded SGR sequences are the
	// rendering, not an injection. Every line after the trusted prefix is
	// disk-borne or user-borne content and is stripped before it can reach
	// the terminal (see stripUntrustedLines). App.Frame leaves this zero;
	// composeFrame sets it to the banner line count.
	Trusted int
	Lines   []string
}

// String joins the frame lines into one renderable snapshot.
func (f Frame) String() string { return strings.Join(f.Lines, "\n") }

// KeySource delivers one key at a time (console raw poll, line-mode reader,
// or scripted). ReadKey returns false at end of input.
type KeySource interface {
	ReadKey() (Key, bool)
}

// Screen is one TUI surface on the App stack. Draw returns body lines in
// logical order; components pad and truncate through cellwidth. HandleKey
// receives keys after the App-level precedence (Ctrl+C, Ctrl+L, modal,
// Esc) has run; screens in EDIT mode decode text through EditRune.
type Screen interface {
	Title() string
	Mode() Mode
	Draw(a *App) []string
	HandleKey(k Key, a *App)
}

// Modal is a key-consuming overlay (confirm gate, numbered choice, approval).
// While a modal is open the App routes every key to it before the screen
// (ModeConfirm), so nothing leaks past a live gate. Key returns true while
// the modal stays open and false once it closes.
type Modal interface {
	Title() string
	// Body returns the modal body lines exactly as the caller composed
	// them (the approval modal's mutation summary must survive verbatim).
	Body(w int) []string
	Footer() string
	Key(k Key, a *App) bool
}

// App is the platform-neutral TUI engine: a screen stack plus at most one
// modal plus a message line, with the mode-scoped dispatcher. It owns no
// OS handles; the console reader and the ANSI painter attach in P4.
type App struct {
	width   int
	height  int
	dir     Dir
	stack   []Screen
	modal   Modal
	message string
	redraws int
	quit    bool
}

// NewApp builds an empty app for the given geometry and text direction.
func NewApp(width, height int, dir Dir) *App {
	return &App{width: width, height: height, dir: dir}
}

// Width returns the frame width in cells.
func (a *App) Width() int { return a.width }

// Height returns the frame height in cells.
func (a *App) Height() int { return a.height }

// Dir returns the text direction the frames are composed for.
func (a *App) Dir() Dir { return a.dir }

// SetDir switches the text direction (the language screen re-renders the
// whole app in the chosen language, which may flip direction).
func (a *App) SetDir(d Dir) { a.dir = d }

// Mode reports the current dispatch scope: a live modal is ModeConfirm, an
// EDIT-mode top screen is ModeEdit, everything else is ModeNav.
func (a *App) Mode() Mode {
	if a.modal != nil {
		return ModeConfirm
	}
	if top := a.Top(); top != nil && top.Mode() == ModeEdit {
		return ModeEdit
	}
	return ModeNav
}

// Push adds a screen on top of the stack.
func (a *App) Push(s Screen) { a.stack = append(a.stack, s) }

// Pop removes the top screen. The root screen is never popped (Esc at the
// root exits instead); it reports false when nothing was removed.
func (a *App) Pop() bool {
	if len(a.stack) <= 1 {
		return false
	}
	a.stack = a.stack[:len(a.stack)-1]
	return true
}

// Top returns the active screen, or nil on an empty stack.
func (a *App) Top() Screen {
	if len(a.stack) == 0 {
		return nil
	}
	return a.stack[len(a.stack)-1]
}

// Depth reports how many screens are on the stack.
func (a *App) Depth() int { return len(a.stack) }

// ShowModal opens a modal. It replaces any live modal (screens chain
// confirms by opening the next modal from the answer callback).
func (a *App) ShowModal(m Modal) { a.modal = m }

// Modal returns the live modal, or nil.
func (a *App) Modal() Modal { return a.modal }

// SetMessage sets the message line rendered between the body and the footer.
func (a *App) SetMessage(msg string) { a.message = msg }

// Message returns the current message line.
func (a *App) Message() string { return a.message }

// Redraws counts Ctrl+L presses since construction (the redraw request the
// P4 painter consumes).
func (a *App) Redraws() int { return a.redraws }

// Exit requests a clean exit at the next Step boundary (screens call this
// to quit without the user pressing Ctrl+C).
func (a *App) Exit() { a.quit = true }

// Step applies one key through the mode-scoped dispatcher and reports
// whether the app should keep running. Precedence, outermost first:
// Ctrl+C exits cleanly in every mode; Ctrl+L clears the message and counts
// a redraw; a live modal consumes everything else (CONFIRM scope, Esc
// cancels inside the modal); Esc then cancels the message line, then pops
// one screen, and at the root exits; every other key reaches the top
// screen in its own mode scope.
func (a *App) Step(k Key) bool {
	switch k {
	case KeyCtrlC:
		a.quit = true
		return false
	case KeyCtrlL:
		a.message = ""
		a.redraws++
		return true
	}

	if a.modal != nil {
		m := a.modal
		if !m.Key(k, a) && a.modal == m {
			a.modal = nil
		}
		return !a.quit
	}

	if k == KeyEsc {
		if a.message != "" {
			a.message = ""
			return true
		}
		if a.Pop() {
			return true
		}
		a.quit = true
		return false
	}

	if top := a.Top(); top != nil {
		top.HandleKey(k, a)
	}
	return !a.quit
}

// baseFooter is the shared keybinding legend. Screen-specific hints (for
// example the stepList footer) are rendered by the screen inside its body.
const baseFooter = "↑↓←→ move · PgUp/PgDn page · Home/End jump · Enter/digits select · Esc back · Ctrl+L redraw · Ctrl+C exit"

// Frame renders the current state as one snapshot. Sections (title bar,
// body, modal, message, footer) are separated by one blank line; empty
// sections are skipped. Lines past the frame height are dropped, matching
// what the painter can show.
func (a *App) Frame() Frame {
	var blocks [][]string
	if top := a.Top(); top != nil {
		title := cellwidth.Truncate(top.Title(), a.width)
		blocks = append(blocks, []string{title}, top.Draw(a))
	}
	if a.modal != nil {
		m := a.modal
		mb := append([]string{cellwidth.Truncate(m.Title(), a.width)}, m.Body(a.width)...)
		mb = append(mb, m.Footer())
		blocks = append(blocks, mb)
	}
	if a.message != "" {
		blocks = append(blocks, []string{cellwidth.Truncate(a.message, a.width)})
	}
	blocks = append(blocks, []string{baseFooter})

	lines := make([]string, 0, a.height)
	for i, b := range blocks {
		if len(b) == 0 {
			continue
		}
		if i > 0 && len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, b...)
	}
	if len(lines) > a.height {
		lines = lines[:a.height]
	}
	// Frame boundary hygiene (security review F3): every line that reaches
	// the painter is stripped of control characters here, at the single
	// composition choke point, so no screen (body, title, modal, message,
	// footer) can smuggle an escape sequence from disk-borne registry or
	// preset content into the operator's terminal. Clean lines pass
	// through unchanged (stripFrameLines shares the slice).
	lines = stripFrameLines(lines)
	return Frame{Width: a.width, Height: a.height, Dir: a.dir, Lines: lines}
}

// Run drives the render/step loop until the app exits or the key source is
// exhausted, writing every frame through out. The v1 painter is a full
// frame dump per step (P4 swaps in the console renderer behind this same
// loop). It returns the process exit code, always 0 for a clean exit.
func (a *App) Run(keys KeySource, out io.Writer) int {
	for !a.quit {
		fmt.Fprintln(out, a.Frame().String())
		k, ok := keys.ReadKey()
		if !ok {
			break
		}
		if !a.Step(k) {
			break
		}
	}
	return 0
}

// CaptureRun is the headless harness: it drives Run over a scripted key
// source and returns every captured frame (one per line group, frames
// separated by blank-free dumps, trailing newline per frame).
func (a *App) CaptureRun(keys KeySource) string {
	var b strings.Builder
	a.Run(keys, &b)
	return b.String()
}

// ScriptedKeys feeds a fixed key sequence (headless capture tests).
type ScriptedKeys struct {
	Keys []Key
	pos  int
}

// ReadKey returns the next scripted key.
func (s *ScriptedKeys) ReadKey() (Key, bool) {
	if s.pos >= len(s.Keys) {
		return "", false
	}
	k := s.Keys[s.pos]
	s.pos++
	return k, true
}

// ScriptedLines feeds fixed text lines to EDIT-mode prompts and the
// line-mode reader (headless capture tests).
type ScriptedLines struct {
	Lines []string
	pos   int
}

// ReadLine returns the next scripted line.
func (s *ScriptedLines) ReadLine() (string, bool) {
	if s.pos >= len(s.Lines) {
		return "", false
	}
	l := s.Lines[s.pos]
	s.pos++
	return l, true
}
