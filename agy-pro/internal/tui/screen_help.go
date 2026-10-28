package tui

import (
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

// English fallbacks for catalog keys this screen resolves through missingOr.
// en.json now carries every key below (catalog-resolved); each const names
// its key and stays as the fallback for a catalog lacking the translation.
const (
	// catalog-resolved, fallback kept: help.keys.enter
	helpKeysEnter = "Activates the cursor row"
	// catalog-resolved, fallback kept: help.keys.rows_past_9
	helpKeysRowsPast9 = "Rows past 9: arrows + Enter reach them"
	// catalog-resolved, fallback kept: help.keys.tab
	helpKeysTab = "Switches panes (for example Status / Doctor)"
	// catalog-resolved, fallback kept: help.keys.backspace
	helpKeysBackspace = "Edit mode: deletes characters"
	// catalog-resolved, fallback kept: help.keys.page
	helpKeysPage = "Scrolls one page up or down"
	// catalog-resolved, fallback kept: help.keys.home_end
	helpKeysHomeEnd = "Jumps to the first or last row"
	// catalog-resolved, fallback kept: help.keys.yn
	helpKeysYN = "Answers yes / no confirmation prompts"
	// catalog-resolved, fallback kept: help.keys.redraw
	helpKeysRedraw = "Redraws the screen"
	// catalog-resolved, fallback kept: help.keys.space
	helpKeysSpace = "List: pins the verb target. Edit mode: inserts a space."
	// catalog-resolved, fallback kept: help.keys.save
	helpKeysSave = "Edit mode: save binding in editors"
	// catalog-resolved, fallback kept: help.keys.runes
	helpKeysRunes = "Edit mode: printable runes become text input"
	// catalog-resolved, fallback kept: help.numbered.reminder
	helpNumberedReminder = "Every list numbers its rows: digits 1-9 select and activate, arrows move the cursor, Enter activates."
	// catalog-resolved, fallback kept: help.storage.providers.purpose
	helpStorageProvidersPurpose = "provider registry; every write goes through the approval modal"
	// catalog-resolved, fallback kept: help.storage.state.purpose
	helpStorageStatePurpose = "profile mode, default profile, language, launcher toggle"
	// catalog-resolved, fallback kept: help.storage.presets.purpose
	helpStoragePresetsPurpose = "saved fanout seat presets"
	// catalog-resolved, fallback kept: help.storage.prompts.purpose
	helpStoragePromptsPurpose = "custom system prompts, one file per slot"
	// catalog-resolved, fallback kept: help.color.title
	helpColorTitle = "Color Tier"
	// catalog-resolved, fallback kept: help.color.note
	helpColorNote = "Detected once at startup: a non-console output or NO_COLOR forces plain, a VT probe gates color at all, then WT_SESSION or COLORTERM=truecolor picks truecolor and every other console gets 256 colors. Set AGY_PRO_COLOR to truecolor, 256, 16, or off to force a tier."
	// English chrome (decision 6B keeps chrome English, so this hint carries
	// no catalog key). The line names the overlay's whole contract: the six
	// scroll keys, the Enter door into the manual browser, and the two keys
	// that close the overlay. Enter left the close set when the door landed:
	// the overlay is the manual's front door (user ask: a per-feature manual
	// readable with Enter), so a stray Enter now opens the manual instead of
	// dismissing the document, and Esc/q still close.
	helpCloseHint = "Up/Down/PgUp/PgDn/Home/End scroll; Enter opens the AGY manual; Esc/q close."
)

// helpLine is one row of a help section: a left key/path column plus a
// description, or a full-width paragraph when key is "".
type helpLine struct {
	key    string
	desc   string
	nowrap bool // data lines (storage paths) truncate instead of word-wrapping
}

// helpSection is one titled block of the help overlay.
type helpSection struct {
	title string
	lines []helpLine
}

// helpScreen is S12: the scrollable read-only help overlay. It carries the
// guide section (what agy pro does plus the door into the manual browser), the
// full key map, the storage file locations, the RTL scope note, the color-tier
// note, and the numbered-selection reminder.
//
// Reading contract (decision D5-5B, amended by the manual door): Up/Down scroll
// by one line and PgUp/PgDn by one page, Home/End jump to either edge, Enter
// opens the manual browser, Esc/q close the overlay, and every other key is
// inert so a stray press cannot dismiss the document the operator is reading.
// openManual calls a.Pop() before pushing the manual, so the manual sits on the
// screen that opened help and Esc from the manual lands back there rather than
// on this overlay. The close path keeps the D5-5B shape: HandleKey calls
// a.Pop() as the primary close mechanism, which the shell satisfies because it
// pushes the overlay over the calling screen, and when help is the root screen
// (a degenerate standalone use with nothing beneath) Pop reports false and
// a.Exit() closes the app instead, so the invariant holds in every stack shape.
// Esc normally never reaches HandleKey: the App-level precedence pops the
// overlay one step earlier, so the KeyEsc case covers the direct-drive path
// only.
type helpScreen struct {
	bundle   *i18n.Bundle
	sections []helpSection
	sc       textScroller
	page     int // last window height derived in Draw
}

// NewHelpScreen builds the help overlay. Strings resolve through the catalog
// where a key exists; the key-map rows, the color-tier note, and the close
// hint are English consts (decision 6B keeps shared chrome English). Storage
// paths are resolved (read-only) at construction.
func NewHelpScreen() Screen {
	h := &helpScreen{
		bundle: Bundle(),
		page:   1,
	}
	h.sections = h.buildSections()
	return h
}

// buildSections composes the overlay content: the guide into the manual, the key
// map, storage locations, RTL scope, color tier, numbered-selection reminder.
func (h *helpScreen) buildSections() []helpSection {
	b := h.bundle
	return []helpSection{
		{
			// The guide is the overlay's first section: three plain sentences
			// naming what agy pro adds, then the door sentence naming the
			// gesture. All four resolve through the catalog, so a reader in any
			// of the twelve languages is told how to reach the manual in that
			// language.
			title: b.T("help.section.guide"),
			lines: []helpLine{
				{key: "", desc: b.T("help.guide.line.1")},
				{key: "", desc: b.T("help.guide.line.2")},
				{key: "", desc: b.T("help.guide.line.3")},
				{key: "", desc: b.T("help.guide.manual")},
			},
		},
		{
			title: b.T("help.section.keys"),
			lines: []helpLine{
				{key: "1-9", desc: b.T("help.keys.digits")},
				{key: "↑↓←→", desc: b.T("help.keys.arrows")},
				{key: "Enter", desc: helpKeysEnter},
				{key: "Esc / Ctrl+C", desc: b.T("help.keys.esc")},
				{key: "Tab", desc: helpKeysTab},
				{key: "Backspace / Delete", desc: helpKeysBackspace},
				{key: "PgUp/PgDn", desc: helpKeysPage},
				{key: "Home/End", desc: helpKeysHomeEnd},
				{key: "y/n", desc: helpKeysYN},
				{key: "Ctrl+L", desc: helpKeysRedraw},
				{key: "Space", desc: helpKeysSpace},
				{key: "Ctrl+S", desc: helpKeysSave},
				{key: "a-z 0-9 symbols", desc: helpKeysRunes},
				{key: "10+", desc: helpKeysRowsPast9},
				{key: "", desc: helpNumberedReminder},
			},
		},
		{
			title: b.T("help.section.storage"),
			lines: []helpLine{
				{key: "", nowrap: true, desc: b.Tf("help.storage.providers", map[string]string{"path": h.storagePath(paths.AgyProProvidersFile)})},
				{key: "", desc: helpStorageProvidersPurpose},
				{key: "", nowrap: true, desc: b.Tf("help.storage.state", map[string]string{"path": h.storagePath(paths.AgyProStateFile)})},
				{key: "", desc: helpStorageStatePurpose},
				{key: "", nowrap: true, desc: b.Tf("help.storage.presets", map[string]string{"path": h.storagePath(fanout.DefaultPresetsPath)})},
				{key: "", desc: helpStoragePresetsPurpose},
				{key: "", nowrap: true, desc: b.Tf("help.storage.prompts", map[string]string{"path": h.storagePath(fanout.PromptsDir)})},
				{key: "", desc: helpStoragePromptsPurpose},
			},
		},
		{
			title: b.T("help.rtl.title"),
			lines: []helpLine{{key: "", desc: b.T("help.rtl.note")}},
		},
		{
			title: helpColorTitle,
			lines: []helpLine{{key: "", desc: helpColorNote}},
		},
	}
}

// storagePath renders one storage location, keeping a resolution failure
// honest (masked, path-shaped text stays readable through MaskOperand).
func (h *helpScreen) storagePath(resolve func() (string, error)) string {
	p, err := resolve()
	if err != nil {
		return "(unavailable: " + providers.MaskOperand(err.Error()) + ")"
	}
	return p
}

// Title returns the overlay title.
func (h *helpScreen) Title() string { return h.bundle.T("help.screen.title") }

// Mode is ModeNav: the overlay only scrolls and closes, it never edits.
func (h *helpScreen) Mode() Mode { return ModeNav }

// Draw renders the composed sections through the scroll window, wrapping
// paragraphs and padding the key column through cellwidth, and pins the
// close hint under the window.
func (h *helpScreen) Draw(a *App) []string {
	w := a.Width()
	h.page = a.Height() - 6
	if h.page < 1 {
		h.page = 1
	}
	keyW := 0
	for _, sec := range h.sections {
		for _, ln := range sec.lines {
			if ln.key == "" {
				continue
			}
			if cw := cellwidth.String(ln.key); cw > keyW {
				keyW = cw
			}
		}
	}
	avail := w - keyW - 2
	if avail < 1 {
		avail = 1
	}
	var lines []string
	for i, sec := range h.sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, sec.title)
		for _, ln := range sec.lines {
			if ln.key == "" {
				if ln.nowrap {
					lines = append(lines, cellwidth.Truncate(ln.desc, w))
					continue
				}
				lines = append(lines, wrapCells(ln.desc, w)...)
				continue
			}
			lines = append(lines,
				cellwidth.PadRight(ln.key, keyW)+"  "+cellwidth.Truncate(ln.desc, avail))
		}
	}
	h.sc.replace(lines)
	out := make([]string, 0, h.page+2)
	for _, l := range h.sc.window(h.page) {
		out = append(out, cellwidth.Truncate(l, w))
	}
	return append(out, "", helpCloseHint)
}

// HandleKey implements the reading contract documented on helpScreen: Up/Down
// scroll one line, PgUp/PgDn one page, Home/End jump to either edge, Enter opens
// the manual browser, and only Esc/q close the overlay (a.Pop, with a.Exit as
// the root-screen fallback). Any other key is inert, so the arrows the footer
// advertises scroll the document instead of dismissing it.
func (h *helpScreen) HandleKey(k Key, a *App) {
	switch k {
	case KeyUp:
		h.sc.scrollLines(-1, h.page)
	case KeyDown:
		h.sc.scrollLines(1, h.page)
	case KeyPgUp:
		h.sc.scroll(-1, h.page)
	case KeyPgDn:
		h.sc.scroll(1, h.page)
	case KeyHome:
		h.sc.home()
	case KeyEnd:
		h.sc.end(h.page)
	case KeyEnter:
		h.openManual(a)
	default:
		if isHelpCloseKey(k) {
			if !a.Pop() {
				a.Exit()
			}
		}
	}
}

// openManual is the overlay's Enter door: it steps this overlay aside and pushes
// the manual browser, so the manual sits on the screen that opened help and Esc
// from the manual lands there in one step instead of on this overlay again. The
// pop reports false only when help is the root screen (the degenerate standalone
// stack), and the manual then stacks above it so Esc still has somewhere to go.
func (h *helpScreen) openManual(a *App) {
	a.Pop()
	a.Push(NewManualScreen())
}

// isHelpCloseKey reports whether k closes the overlay: Esc or q. Enter is not in
// this set any more: it opens the manual browser (openManual), which is the door
// the guide section and the close hint both name. Esc normally never reaches
// HandleKey (the App-level precedence pops it first), so this is the
// direct-drive path. The console reader delivers a printable letter as the rune
// carrier (Caps Lock included) and the scripted and line-mode seams hand over
// the plain spelling, so both forms resolve here.
func isHelpCloseKey(k Key) bool {
	if k == KeyEsc {
		return true
	}
	if k == Key("q") {
		return true
	}
	r, ok := RuneOf(k)
	return ok && (r == 'q' || r == 'Q')
}

// wrapCells word-wraps one paragraph to w display cells. A word wider than the
// frame is broken across rows through cellwidth, never dropped: a script written
// without spaces (CJK) hands the wrapper one long run per sentence, and cutting
// it once would silently lose the rest of the sentence. cellwidth.Truncate cuts
// at cluster boundaries for every script (the P9 contract; its retired RTL
// refusal used to blank these rows) and never returns an empty string for
// over-wide input at a positive width, so the break loop always makes progress;
// the head == "" arm below is the defensive guard for a non-positive width only.
func wrapCells(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			cand := word
			if line != "" {
				cand = line + " " + word
			}
			if cellwidth.String(cand) <= w {
				line = cand
				continue
			}
			if line != "" {
				out = append(out, line)
				line = ""
			}
			for cellwidth.String(word) > w {
				head := cellwidth.Truncate(word, w)
				if head == "" {
					out = append(out, "")
					word = ""
					break
				}
				out = append(out, head)
				runes, cut := []rune(word), len([]rune(head))
				if cut <= 0 || cut >= len(runes) {
					word = ""
					break
				}
				word = string(runes[cut:])
			}
			line = word
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
