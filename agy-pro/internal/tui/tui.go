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
// in ModeConfirm and become text in ModeEdit. Space follows the same rule:
// on a list that registered a select callback it pins the verb target in
// ModeNav (stepList.SetOnSelect) and inserts a space through EditRune in
// ModeEdit on every surface. Ctrl+C exits and Ctrl+L redraws in every mode.
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
	// the terminal (see stripUntrustedLines). App.Frame (no banner) leaves
	// this zero; ComposeFrame sets it to the banner prefix it was given.
	Trusted int
	// CaretLine and CaretCell locate a live text caret inside Lines: the
	// frame line index and the display-cell offset within that line
	// (cellwidth cells, CJK counts 2). Both are -1 when no caret is live:
	// no top screen implements CaretSource, the screen reports no caret, or
	// the caret line did not survive the body cut or the height cap, in
	// which case the composer refuses to guess a position (see
	// framePlan.caret). The painter draws the glyph from these two fields
	// alone; nothing here moves the hardware cursor.
	//
	// The -1 sentinel exists because (0,0) is a real position (the first
	// cell of the first line), so the zero value of a hand-built Frame reads
	// as a live caret at the frame origin. Every frame the composer produces
	// gets the right value without caller help; code that builds a Frame
	// literal and wants no caret states -1 for both fields, as the painter
	// fixtures do.
	CaretLine int
	CaretCell int
	Lines     []string
}

// String joins the frame lines into one renderable snapshot.
func (f Frame) String() string { return strings.Join(f.Lines, "\n") }

// BlockKind tags one composed frame section. A painter adapter (the console
// composer in app.go) maps kinds onto palette roles, so it never has to
// re-derive the layout and cannot drift from it silently.
type BlockKind int

const (
	// BlockSeparator is the blank line between two sections.
	BlockSeparator BlockKind = iota
	// BlockBanner is the trusted banner art prefix (console first frame).
	BlockBanner
	// BlockTitle is the top screen's title line.
	BlockTitle
	// BlockBody is the top screen's body block. It is the first block the
	// composer gives up rows from (the cut ladder on ComposeFrame), never the
	// gate legend or the base footer.
	BlockBody
	// BlockModal is a live modal: title, body, and footer lines.
	BlockModal
	// BlockMessage is the message line.
	BlockMessage
	// BlockFooter is the keybinding legend.
	BlockFooter
)

// block is one composed frame section plus its tag.
type block struct {
	kind  BlockKind
	lines []string
	// bare attaches the section to the previous one without a blank
	// separator. The parts of one modal (title, body, legend) are a single
	// visual section, so a cut inside them must never leave a floating blank
	// line behind; the separator-collapse stage sets it on every section.
	bare bool
}

// Composed is one composed frame plus the section tag of every line, aligned
// by index with Frame.Lines. It is the shared primitive for painter
// adapters: the frame layout (including the body reservation that keeps a
// live modal on screen) is computed once, in one place.
type Composed struct {
	Frame Frame
	Kinds []BlockKind
}

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

// CaretSource is implemented by screens that have a visible text caret.
// bodyLine is the index within the screen's own Draw output; cell is the
// display-cell offset within that line (cellwidth cells, CJK counts 2).
//
// The screen owns the mapping from its components to its own Draw lines (an
// editor viewport that starts under a status block reports the offset the
// status block adds); the composer only translates bodyLine into a frame
// line. ok == false means the surface has no caret right now, which is the
// same as not implementing the interface.
type CaretSource interface {
	Caret() (bodyLine, cell int, ok bool)
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
	// exitPending is the armed root exit confirmation: the first Esc at the
	// root sets the message and arms it, the next Esc exits, and any other
	// key disarms it. It never outlives a screen change.
	exitPending bool
	redraws     int
	quit        bool
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

// Push adds a screen on top of the stack and drops the transient feedback of
// the screen below it. A message belongs to the screen that produced it (UX
// audit C1-F13): it used to follow the user into unrelated surfaces, where it
// read as feedback about the new screen. A screen that wants to greet the
// screen below sets its message after popping, which every caller already
// does.
func (a *App) Push(s Screen) {
	a.stack = append(a.stack, s)
	a.clearTransient()
}

// Pop removes the top screen. The root screen is never popped (Esc at the
// root arms the exit confirmation instead); it reports false when nothing was
// removed. A successful pop drops the transient feedback of the screen it
// removed, exactly like Push.
func (a *App) Pop() bool {
	if len(a.stack) <= 1 {
		return false
	}
	a.stack = a.stack[:len(a.stack)-1]
	a.clearTransient()
	return true
}

// clearTransient drops the state that belongs to one screen only: the message
// line a screen set, and an armed root exit confirmation. A screen change
// invalidates both.
func (a *App) clearTransient() {
	a.message = ""
	a.exitPending = false
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

// Redraws counts full-repaint requests since construction: Ctrl+L presses
// and Resize calls (the redraw request the P4 painter consumes).
func (a *App) Redraws() int { return a.redraws }

// Exit requests a clean exit at the next Step boundary (screens call this
// to quit without the user pressing Ctrl+C).
func (a *App) Exit() { a.quit = true }

// Resize updates the frame geometry after the console changed size and asks
// for one full repaint on the next frame: the redraw counter advances and the
// next composed frame uses the new width and height. The painter already
// erases the rows a shorter frame leaves behind (its ghost-row erase), so a
// shrinking frame leaves no ghost lines. Non-positive sizes are ignored,
// because a resize notification can arrive before the host reports a usable
// size; the method is safe to call before the first paint, and it never
// touches the screen stack, the message, or a live modal.
func (a *App) Resize(w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	a.width = w
	a.height = h
	a.redraws++
}

// Step applies one key through the mode-scoped dispatcher and reports
// whether the app should keep running. Precedence, outermost first:
// Ctrl+C exits cleanly in every mode; Ctrl+L clears the message and counts
// a redraw; a live modal consumes everything else (CONFIRM scope, Esc
// cancels inside the modal); Esc then cancels the message line, then pops
// one screen, and at the root arms the exit confirmation (the next Esc
// exits, any other key disarms); every other key reaches the top screen in
// its own mode scope.
func (a *App) Step(k Key) bool {
	switch k {
	case KeyCtrlC:
		a.quit = true
		return false
	case KeyCtrlL:
		a.message = ""
		a.disarmExit()
		a.redraws++
		return true
	}

	if a.modal != nil {
		a.disarmExit()
		m := a.modal
		if !m.Key(k, a) && a.modal == m {
			a.modal = nil
		}
		return !a.quit
	}

	if k == KeyEsc {
		if a.exitPending {
			a.quit = true
			return false
		}
		if a.message != "" {
			a.message = ""
			return true
		}
		if a.Pop() {
			return true
		}
		// Root exit ceremony (user decision 8B): the root Esc used to drop
		// the session instantly while the hub also carries a Quit row, so the
		// two exit ceremonies disagreed. The first Esc now asks, and only a
		// second Esc with no key in between exits.
		a.message = exitPrompt()
		a.exitPending = true
		return true
	}

	a.disarmExit()
	if top := a.Top(); top != nil {
		top.HandleKey(k, a)
	}
	return !a.quit
}

// exitPrompt is the root exit confirmation text in the active language: the
// catalog key when the active catalog carries it, the English const
// otherwise.
func exitPrompt() string { return missingOr(Bundle(), "app.exit_confirm", exitConfirmMsg) }

// disarmExit cancels an armed root exit and removes the prompt that armed it,
// while any other message keeps its place: a "saved" or "cancelled" line has
// to survive the keys that follow it. Every key except the confirming Esc
// disarms, so a stray key between the two Esc presses cannot leave a prompt
// promising an exit that will not happen.
func (a *App) disarmExit() {
	if !a.exitPending {
		return
	}
	if a.message == exitPrompt() {
		a.message = ""
	}
	a.exitPending = false
}

// exitConfirmMsg is the confirm prompt of the root exit ceremony (user
// decision 8B): the first Esc at the root arms it, the second Esc exits.
// The catalog does not carry the key yet, so this English const is the
// documented fallback; missingOr picks the translation up the moment a
// catalog wave adds it, with no code change here.
// MISSING-KEY: app.exit_confirm
const exitConfirmMsg = "Press Esc again to exit"

// baseFooter is the shared keybinding legend. Screen-specific hints (for
// example the stepList footer) are rendered by the screen inside its body.
// The legend is 77 cells so it fits the 80-column frame instead of wrapping
// in it (UX audit C1-F16): the previous 105-cell line was the only block
// appended without a cellwidth pass. Wrapping costs a physical row the
// painter cannot erase, because it erases one logical row per line.
// Home/End still jump; the legend only stopped advertising them to stay
// inside 80 columns.
const baseFooter = "↑↓←→ move · PgUp/PgDn · Enter/digits · Esc back · Ctrl+L redraw · Ctrl+C exit"

// compactFooter is the narrow-frame legend. When a frame cannot hold
// baseFooter, the three global keys stay readable instead of losing their
// tail to a mid-legend cut; Ctrl+C exit is the hint a user must never lose,
// because it is the only exit that works in every mode and inside a gate.
const compactFooter = "Esc back · Ctrl+L redraw · Ctrl+C exit"

// footerLines fits the keybinding legend to the frame width: the full
// legend when it fits, the compact one when it does not, and a hard cut of
// the compact one only below its own width. A zero-width frame carries no
// footer at all.
func footerLines(w int) []string {
	switch {
	case w <= 0:
		return nil
	case cellwidth.String(baseFooter) <= w:
		return []string{baseFooter}
	case cellwidth.String(compactFooter) <= w:
		return []string{compactFooter}
	default:
		return []string{cellwidth.Truncate(compactFooter, w)}
	}
}

// Frame renders the current state as one snapshot.
func (a *App) Frame() Frame { return a.ComposeFrame(nil).Frame }

// ComposeFrame composes the current state as one frame snapshot, with an
// optional trusted banner block first, and returns the per-line section tags
// alongside it. Sections (banner, title bar, body, modal, message, footer)
// are separated by one blank line; empty sections are skipped.
//
// Caret (user decision 3B): when the top screen implements CaretSource and
// reports a live caret, the frame carries its absolute line and cell
// (Frame.CaretLine/CaretCell) and the painter draws the in-line glyph from
// them. The translation is recomputed with the lines on every attempt, and
// it fails safe to -1/-1 whenever the caret line did not survive the cut
// ladder (framePlan.caret).
//
// Body reservation (UX audit C1-F6, blocker class): the frame used to append
// the modal after a full-page body and then cut the tail at the frame
// height, so at 80x24 a live M3/M4 gate was entirely off-frame while it
// swallowed every key, and a blind y could write. The body is now the first
// block the composer gives up rows from: when the assembled frame is taller
// than the height, the body loses exactly the rows the frame is over by, and
// once nothing is left for it the body is dropped, so the modal REPLACES the
// body instead of falling off the frame. A frame that fits is composed
// exactly as before.
//
// Cut ladder (phase 3, F-1/F-3): the last-resort tail cut used to remove the
// base footer first, then the message, then the gate legend - the only line
// that documents y / n / Esc - while the gate still answered every key, the
// blind-approval class this phase exists to close. That cut is gone. The
// remaining stages run in the order of what the operator can afford to lose
// (framePlan.cut): body, message, screen title, modal body from its bottom,
// and only in an impossible geometry the modal title. The gate legend and the
// base footer are not a stage at all, so no geometry the app can reach
// removes them; the hard cap after the last stage only fires when even their
// two rows do not fit.
func (a *App) ComposeFrame(banner []string) Composed {
	var body []string
	if top := a.Top(); top != nil {
		body = top.Draw(a)
	}
	plan := a.newFramePlan(banner, body)
	height := a.height
	if height < 0 {
		height = 0
	}
	for {
		blocks := plan.compose(a.width)
		lines, kinds, bodyStart := joinBlocks(blocks)
		// The body start is recomputed with the lines on every attempt: the
		// cut stages drop whole blocks and the sealed stage drops separators,
		// so a body index from an earlier attempt would point at the wrong
		// line. It is the only origin the caret translation needs.
		if len(lines) <= height {
			return a.composed(plan, lines, kinds, banner, bodyStart)
		}
		if !plan.cut(len(lines) - height) {
			// Nothing is left to give: the frame is shorter than the two rows
			// the gate legend and the base footer need. Capping from the tail
			// keeps the head, so the gate legend (the line that documents the
			// keys a live gate still answers) outlives the footer.
			return a.composed(plan, lines[:height], kinds[:height], banner, bodyStart)
		}
	}
}

// composed assembles the snapshot, clamping the trusted prefix to the lines
// that actually survived the height budget and translating the top screen's
// caret into frame coordinates.
func (a *App) composed(plan framePlan, lines []string, kinds []BlockKind, banner []string, bodyStart int) Composed {
	trusted := 0
	if len(banner) > 0 {
		trusted = len(banner)
		if trusted > len(lines) {
			trusted = len(lines)
		}
	}
	caretLine, caretCell := plan.caret(a.Top(), bodyStart, len(lines))
	return Composed{
		Frame: Frame{
			Width:     a.width,
			Height:    a.height,
			Dir:       a.dir,
			Trusted:   trusted,
			CaretLine: caretLine,
			CaretCell: caretCell,
			Lines:     lines,
		},
		Kinds: kinds,
	}
}

// caret translates the top screen's own caret into frame coordinates and
// reports -1, -1 whenever the translation is impossible. The fail-safe rule
// is the whole point of this function: a missing caret is always better than
// a caret drawn on the wrong rune, because the operator would then type into
// a position the frame does not show. So every uncertain case drops the
// caret instead of guessing:
//
//   - no top screen, or one that does not implement CaretSource: no caret;
//   - bodyStart < 0: the body block is gone (nothing was drawn, or the
//     body reservation gave every row to a live modal), so a body line
//     index has no frame line to land on;
//   - the screen reports ok == false, a negative index, or a bodyLine past
//     its own Draw output (more than len(body) lines survived the cut): the
//     caret line was removed by the body shrink stage;
//   - the translated line fell off the height cap: the caret is on a line
//     the operator cannot see.
//
// The cell is not the screen's own: a screen measures it over the raw line it
// drew, while compose strips that line (Cc controls and Cf format runes) before
// the painter places a glyph, so the reported cell is re-measured against the
// line that will actually be painted (caretCellAfterStrip). The fail-safe arms
// above stay exactly as they were: a caret that cannot be placed is dropped,
// never relocated to a nearby cell.
func (p framePlan) caret(top Screen, bodyStart, lineCount int) (line, cell int) {
	if bodyStart < 0 {
		return -1, -1
	}
	cs, ok := top.(CaretSource)
	if !ok {
		return -1, -1
	}
	bodyLine, caretCell, ok := cs.Caret()
	if !ok || bodyLine < 0 || caretCell < 0 || bodyLine >= len(p.body) {
		return -1, -1
	}
	if frameLine := bodyStart + bodyLine; frameLine < lineCount {
		return frameLine, caretCellAfterStrip(p.body[bodyLine], caretCell)
	}
	return -1, -1
}

// caretCellAfterStrip re-measures a caret cell against the line the painter
// will actually paint. A screen measures its caret over its own raw Draw line,
// but every composed body line passes through stripFrameLines (compose, the
// single strip choke point), which drops Cc controls and Cf format runes; when
// such a rune sits before the caret, the raw measurement counted a cell the
// frame does not show, so the glyph landed one cell to the right per stripped
// rune, or on the end-of-line insertion cell when the caret was at the end.
// That is the misleading caret this feature exists to avoid: the operator would
// type into a position the frame does not show.
//
// The walk adds the display cells of the runes the strip keeps and skips the
// ones it removes, which is what keeps a Cf rune correct: cellwidth already
// measures U+202E and its neighbours zero cells wide, so subtracting a stripped
// rune instead of skipping it would shift the caret one cell to the left. A
// caret whose byte offset lands on a stripped rune resolves to the cell of the
// next surviving rune, because the stripped rune is not on the frame and the
// caret belongs where typing would land. A line with nothing to strip
// re-measures to the identical cell, so clean frames are untouched.
//
// The result is therefore a prefix sum of surviving rune widths, bounded by the
// raw line's own cell count: a reported cell at that count is the raw end, and
// it maps to the stripped line's end (the insertion cell a typed rune would
// take). A reported cell past the raw end is not modeled here at all. It passes
// through unchanged, so the painter's own fail-safe ("a caret cell past the
// visible end draws nothing", render.go) stays the last word and a screen that
// overshoots its own line cannot have the caret relocated to a nearby cell.
// The width clamp is not modeled here either: a line the clamp cut away keeps
// the raw measurement, and the painter drops the glyph.
func caretCellAfterStrip(line string, cell int) int {
	if !strings.ContainsFunc(line, strippedRune) {
		return cell
	}
	if cell > cellwidth.String(line) {
		return cell
	}
	off := caretByteOffset(line, cell)
	n := 0
	for i, r := range line {
		if i >= off {
			break
		}
		if strippedRune(r) {
			continue
		}
		n += cellwidth.Rune(r)
	}
	return n
}

// strippedRune reports whether compose removes r from a frame line. The
// decision itself lives in runeSurvivesStrip (render.go), the predicate
// stripControl applies, so the caret walk and the strip cannot disagree about
// what survives when a category is added.
func strippedRune(r rune) bool {
	return !runeSurvivesStrip(r)
}

// framePlan is one composition attempt: the frame sections after the cut
// stages applied so far. A section is a slice rather than a string because a
// dropped section must stay distinguishable from an empty one (a screen may
// carry an empty title line, and that line is still chrome).
type framePlan struct {
	banner      []string
	title       []string
	body        []string
	modalTitle  []string
	modalBody   []string
	modalLegend []string
	message     []string
	footer      []string
	// sealed joins every section without a blank separator. It is the last
	// stage before the hard cap: when only the gate legend and the footer are
	// left and their two rows plus a separator no longer fit, the separator is
	// what goes, not the legend or the footer.
	sealed bool
}

// newFramePlan resolves the current app state into the sections the cut
// stages work on. The modal is split into its three parts (title, body,
// legend) because the cut truncates the body from its bottom while the title
// and the legend stay: the legend is the only line that documents y / n / Esc,
// and the gate answers every key for as long as it is on the frame.
//
// Modal.Body keeps its byte-stable contract: the plan holds the caller's own
// slice and only ever subslices it, so the summary the operator approved stays
// untouched and the width clamp applies to the frame copy.
func (a *App) newFramePlan(banner, body []string) framePlan {
	p := framePlan{banner: banner, body: body, footer: footerLines(a.width)}
	if top := a.Top(); top != nil {
		p.title = []string{top.Title()}
	}
	if a.modal != nil {
		m := a.modal
		p.modalTitle = []string{m.Title()}
		p.modalBody = m.Body(a.width)
		p.modalLegend = []string{m.Footer()}
	}
	if a.message != "" {
		p.message = []string{a.message}
	}
	return p
}

// cut applies one cut stage and reports whether it removed anything. The
// stages are ordered by what the operator can afford to lose, never by what is
// cheapest to remove (phase 3 F-1/F-3):
//
//  1. the screen body, from its tail;
//  2. the message line;
//  3. the screen title bar;
//  4. the modal body, from its bottom (the modal title and legend stay);
//  5. the modal title, only once the modal body is gone;
//  6. the blank separator between the surviving sections.
//
// The gate legend and the base footer are deliberately absent: a live gate
// answers every key, so a frame that hides the legend while the gate still
// answers approves blind. Only the hard cap in ComposeFrame can still remove
// them, and only when the two rows they need do not fit the frame.
func (p *framePlan) cut(over int) bool {
	switch {
	case len(p.body) > 0:
		drop := over
		if drop > len(p.body) {
			drop = len(p.body)
		}
		p.body = p.body[:len(p.body)-drop]
		return true
	case len(p.message) > 0:
		p.message = nil
		return true
	case len(p.title) > 0:
		p.title = nil
		return true
	case len(p.modalBody) > 0:
		drop := over
		if drop > len(p.modalBody) {
			drop = len(p.modalBody)
		}
		p.modalBody = p.modalBody[:len(p.modalBody)-drop]
		return true
	case len(p.modalTitle) > 0:
		p.modalTitle = nil
		return true
	case !p.sealed:
		p.sealed = true
		return true
	}
	return false
}

// compose renders the plan as tagged sections. The banner block is trusted
// content (our own art, whose SGR sequences are the rendering) and passes
// through verbatim; every other block is stripped and width-clamped here, at
// the single composition choke point every path shares (App.Frame, the console
// painter, the line-mode loop), so no screen (body, title, modal, message,
// footer) can smuggle an escape sequence from disk-borne registry or preset
// content into the operator's terminal, and no caller-owned line can wrap on a
// real console: a wrapped line costs a physical row the painter cannot erase.
func (p framePlan) compose(w int) []block {
	var blocks []block
	if len(p.banner) > 0 {
		blocks = append(blocks, block{kind: BlockBanner, lines: p.banner})
	}
	if len(p.title) > 0 {
		blocks = append(blocks, block{kind: BlockTitle, lines: p.title})
	}
	if len(p.body) > 0 {
		blocks = append(blocks, block{kind: BlockBody, lines: p.body})
	}
	// The modal is one visual section: its parts carry no separator between
	// them, so truncating the body cannot leave a floating blank line.
	if len(p.modalTitle) > 0 {
		blocks = append(blocks, block{kind: BlockModal, lines: p.modalTitle})
	}
	if len(p.modalBody) > 0 {
		blocks = append(blocks, block{kind: BlockModal, lines: p.modalBody, bare: true})
	}
	if len(p.modalLegend) > 0 {
		blocks = append(blocks, block{kind: BlockModal, lines: p.modalLegend, bare: true})
	}
	if len(p.message) > 0 {
		blocks = append(blocks, block{kind: BlockMessage, lines: p.message})
	}
	if len(p.footer) > 0 {
		blocks = append(blocks, block{kind: BlockFooter, lines: p.footer})
	}
	if p.sealed {
		for i := range blocks {
			blocks[i].bare = true
		}
	}
	for i := range blocks {
		if blocks[i].kind == BlockBanner {
			continue
		}
		blocks[i].lines = clampLines(stripFrameLines(blocks[i].lines), w)
	}
	return blocks
}

// clampLines fits every line of one section to the frame width, copying the
// slice only when a line actually changes.
func clampLines(lines []string, w int) []string {
	var out []string
	for i, line := range lines {
		clamped := clampLine(line, w)
		if clamped == line && out == nil {
			continue
		}
		if out == nil {
			out = make([]string, len(lines))
			copy(out[:i], lines[:i])
		}
		out[i] = clamped
	}
	if out == nil {
		return lines
	}
	return out
}

// clampLine fits one composed line into w cells. A line that already fits is
// returned unchanged, so a width that needs no work stays byte-identical.
func clampLine(line string, w int) string {
	if w <= 0 || line == "" {
		return line
	}
	if cellwidth.String(line) <= w {
		return line
	}
	if cut := cellwidth.Truncate(line, w); cut != "" {
		return cut
	}
	return visibleCut(line, w)
}

// clampMarker ends a line whose ordinary truncation refused to cut it. It is
// one cell, so it always fits a frame that has a cell to spare.
const clampMarker = "…"

// visibleCut is the non-blank fallback for an over-wide line whose truncation
// came back empty. cellwidth.Truncate refuses every mid-string cut of a line
// carrying a rune in the RTL script range U+0590..U+08FF (it will not hand
// back half-cut logical-order RTL text) and it also returns "" when the first
// rune is wider than the whole budget. Accepting either as the clamped line
// would BLANK it: in an ar session the over-wide approval diff line, the
// consent content the operator is approving, would vanish from the frame while
// the gate still answered y. The fallback keeps the rune-bounded prefix that
// fits plus the visible marker, so a clamped line always carries something and
// the operator can see that content was withheld. A one-cell frame cannot hold
// both, so it gets the marker alone.
func visibleCut(line string, w int) string {
	budget := w - cellwidth.String(clampMarker)
	if budget <= 0 {
		return clampMarker
	}
	if prefix := prefixCut(line, budget); prefix != "" {
		return prefix + clampMarker
	}
	return clampMarker
}

// prefixCut returns the longest rune-bounded prefix of s that fits in cells
// display columns, dropping the zero-width marks the cut would orphan. It is
// the cut cellwidth.Truncate refuses to make; visibleCut is its only caller.
func prefixCut(s string, cells int) string {
	runes := []rune(s)
	width, n := 0, 0
	for _, r := range runes {
		rw := cellwidth.Rune(r)
		if width+rw > cells {
			break
		}
		width += rw
		n++
	}
	for n > 0 && cellwidth.Rune(runes[n-1]) == 0 {
		n--
	}
	return string(runes[:n])
}

// joinBlocks flattens the sections into frame lines, inserting one blank
// separator between two adjacent non-empty sections (unless the section is
// bare: the parts of one modal and the sealed skeleton are one visual run),
// and returns the section tag of every line (Kinds stays aligned with the
// returned lines) plus the index of the first BlockBody line, or -1 when no
// body section was composed.
//
// bodyStart is measured in the same walk that emits the lines, so it cannot
// drift from the separator rule: it is the origin the caret translation adds
// the screen's own body line index to.
func joinBlocks(blocks []block) (lines []string, kinds []BlockKind, bodyStart int) {
	n := 0
	for _, b := range blocks {
		if len(b.lines) > 0 {
			n += len(b.lines) + 1
		}
	}
	lines = make([]string, 0, n)
	kinds = make([]BlockKind, 0, n)
	bodyStart = -1
	for _, b := range blocks {
		if len(b.lines) == 0 {
			continue
		}
		if len(lines) > 0 && !b.bare {
			lines = append(lines, "")
			kinds = append(kinds, BlockSeparator)
		}
		if b.kind == BlockBody && bodyStart < 0 {
			bodyStart = len(lines)
		}
		lines = append(lines, b.lines...)
		for range b.lines {
			kinds = append(kinds, b.kind)
		}
	}
	return lines, kinds, bodyStart
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
