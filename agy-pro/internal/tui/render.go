// render.go is the painter of the setup TUI. v1 strategy: full redraw per
// frame (cursor home + per-line erase) driven by the P3 Frame, flushed as a
// single atomic write so an escape sequence is never split across writes
// (research report §8: whole sequence in one call). Escape bytes are emitted
// only when the capability object is above TierPlain; a plain cap (pipe,
// NO_COLOR, down-level console) emits the same frame as bare text with zero
// ESC bytes anywhere.
//
// RTL mirroring lives here, not in the screens (plan v2 §3): when Frame.Dir
// is DirRTL the painter mirrors each line geometrically (box corners swapped
// and rune order reversed, list gutter and numbering moved to the right,
// "·"-segmented lines such as footers reordered, everything right-aligned
// through cellwidth). Text itself stays in logical order; no bidi pass.
//
// The live text caret is drawn here too, as an in-line glyph: reverse video
// around the caret rune on the color tiers, a "|" marker on TierPlain, and
// nothing at all under RTL (the mirroring rules above make one logical cell
// offset ambiguous, so Frame.caretPoint scopes it out). No cursor-positioning
// sequence is involved beyond the frame's existing cursor home.

package tui

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"agypro/internal/cellwidth"
)

// Painter renders Frames to one output stream under one capability object.
// A Painter is built once per session from the Cap the TUI entry detected.
// lastRows tracks how many rows the previous frame occupied so a shrinking
// frame can erase the rows the taller frame left behind (no ghost lines).
type Painter struct {
	out      io.Writer
	cap      Cap
	lastRows int
}

// NewPainter binds an output stream and a capability object. out must be
// the console the Cap describes; on non-console output the caller is
// expected to run the line-mode degrade instead of a Painter at all.
func NewPainter(out io.Writer, cap Cap) *Painter {
	return &Painter{out: out, cap: cap}
}

// Cap returns the capability object the painter renders under.
func (p *Painter) Cap() Cap { return p.cap }

// Paint renders one full frame: cursor home, per-line erase, styled lines.
// styles aligns with f.Lines by index (nil or shorter means RoleNone for the
// rest). The whole frame is composed in memory and written with a single
// Write, so no escape sequence can be split across writes. For TierPlain
// the output is the bare lines joined by "\n": zero escape bytes even when
// styles are passed. Every line past the frame's trusted prefix is passed
// through stripControl first, so content-borne control characters (a crafted
// model id or provider name from a hand-edited registry file, plan review F3)
// can never reach the terminal or a pipe; the trusted leading lines (the
// banner block, composed by our own art code) pass through verbatim so their
// SGR sequences survive to the terminal. The prefix is the clamped one
// (Frame.trustedPrefix), the same normalization the caret gate applies, so a
// hand-built Frame cannot make the gate and the strip disagree about which
// lines are banner art. After the last line, any rows the previous frame
// occupied beyond this frame are erased (console tiers only), so switching
// from a tall screen to a short one leaves no ghost lines (review F-09).
//
// The live caret (Frame.CaretLine/CaretCell, user decision 3B) is rendered
// as an in-line glyph on the caret line only: reverse video around the rune
// at the caret cell on the color tiers, one printable marker cell on
// TierPlain. The glyph is applied here, after the control strip and after
// the RTL mirror, so no screen-emitted escape is involved and no SGR byte
// can reach an untrusted line's content; the frame's existing cursor home
// stays the only positioning sequence a frame emits (see caretPoint).
func (p *Painter) Paint(f Frame, styles []Role) error {
	lines := stripUntrustedLines(f)
	if f.Dir == DirRTL {
		lines = mirrorLines(lines, f.Width)
	}
	tier := p.cap.Tier
	caretLine, caretCell := f.caretPoint(len(lines))
	var b strings.Builder
	if tier != TierPlain {
		b.WriteString("\x1b[H") // cursor home
	}
	for i, line := range lines {
		var role Role
		if i < len(styles) {
			role = styles[i]
		}
		if tier != TierPlain {
			b.WriteString("\x1b[2K") // erase whole line
			if role != RoleNone {
				b.WriteString(p.cap.Palette.Role(role).FG(tier))
			}
			if i == caretLine {
				line = invertAtCell(line, caretCell, f.Width)
			}
		} else if i == caretLine {
			line = markAtCell(line, caretCell, f.Width)
		}
		b.WriteString(line)
		if tier != TierPlain && role != RoleNone {
			b.WriteString("\x1b[0m")
		}
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	if tier != TierPlain {
		// Shrink erase: walk down and clear each row the previous frame
		// painted beyond this frame's last row. The cursor lands where
		// the previous frame ended, so the screen never scrolls.
		for i := len(lines); i < p.lastRows; i++ {
			b.WriteString("\n\x1b[2K")
		}
	}
	p.lastRows = len(lines)
	_, err := io.WriteString(p.out, b.String())
	return err
}

// The caret glyph (phase 5, user decision 3B). Two spellings, one per tier
// class, both applied by the painter at paint time:
//
//   - color tiers: SGR 7 (reverse video) around the rune the caret sits on,
//     closed by SGR 27 (reverse off). 7/27 rather than a palette background
//     because they NEST: 27 cancels the one attribute it introduced and
//     leaves the enclosing role foreground color active, so the line's role
//     still colors the whole line (the reset the caller already emits after
//     the line ends the role color). Reverse video also inherits the role
//     color as the background, so the rune reads as a selected cell in its
//     own color instead of a fixed swatch that could collide with the text.
//   - TierPlain: the printable ASCII marker "|", inserted at the caret cell.
//     A plain tier must emit zero escape bytes, so the only way to show a
//     caret there is a character; "|" is the design record's own choice and
//     is one cell wide (markAtCell trims the tail so the line keeps its
//     width).
const (
	caretSGRIn  = "\x1b[7m"
	caretSGROut = "\x1b[27m"
	caretMarker = "|"
)

// trustedPrefix returns f.Trusted clamped into the frame's own line range: the
// one normalization both the caret gate (caretPoint) and the final sink
// (stripUntrustedLines) apply. A Frame can be built by hand with any Trusted
// value, so a gate that read the raw field while the strip clamped it could
// draw a caret on a line the painter keeps as trusted banner art, or refuse one
// on a line the painter strips. Clamping in one place keeps the two answers
// identical by construction.
func (f Frame) trustedPrefix() int {
	trusted := f.Trusted
	if trusted < 0 {
		trusted = 0
	}
	if trusted > len(f.Lines) {
		trusted = len(f.Lines)
	}
	return trusted
}

// caretPoint reports the in-line caret's coordinates inside the lines the
// painter is about to write, or -1, -1 when there is nothing to draw. It is
// the single gate every tier goes through, so the fail-safe rules cannot
// drift between them:
//
//   - both fields must be set (the composer's -1 convention);
//   - the frame must have a width, because a caret that cannot be proven to
//     fit is not drawn (the width contract outranks caret visibility);
//   - the caret line must not be in the trusted banner prefix: that block is
//     our own art, and a caret there would mean a screen pointed into it. The
//     prefix is normalized exactly as the strip normalizes it (trustedPrefix),
//     so a hand-built Frame cannot make the gate and the painter disagree
//     about which lines are banner art;
//   - the caret line must exist in the painted frame;
//   - the frame must be left-to-right. RTL is scoped out, deliberately:
//     mirrorLine reverses rune order for box rows, moves the cursor marker,
//     swaps the gutter of numbered rows, and reorders "·" segments, so one
//     logical cell offset has no single visual cell, and the editor above
//     promises no bidi cursor either (editor.go:30). Mirroring already
//     right-aligns the text, and with no hardware cursor to leave behind,
//     the honest result is "no in-line caret under RTL" rather than a caret
//     on the wrong rune.
func (f Frame) caretPoint(n int) (line, cell int) {
	if f.CaretLine < 0 || f.CaretCell < 0 || f.Width <= 0 {
		return -1, -1
	}
	if f.CaretLine < f.trustedPrefix() || f.CaretLine >= n {
		return -1, -1
	}
	if f.Dir != DirLTR {
		return -1, -1
	}
	return f.CaretLine, f.CaretCell
}

// caretByteOffset returns the byte offset of the rune that covers display
// cell cell: the insertion point the caret marks. A cell that lands inside a
// double-width rune resolves to that rune's first byte, so the caret never
// splits a glyph; a cell at or past the line's last cell resolves to
// len(line), which is the end-of-line insertion point. Callers bound-check
// the cell against cellwidth.String(line) first, because "at the end" is an
// insertion point the painter can draw in and "past the end" is a caret the
// width clamp cut away, which it must not relocate to the end.
func caretByteOffset(line string, cell int) int {
	pos := 0
	for i, r := range line {
		w := cellwidth.Rune(r)
		if cell < pos+w {
			return i
		}
		pos += w
	}
	return len(line)
}

// invertAtCell returns line with the rune the caret occupies wrapped in the
// reverse-video pair. An end-of-line caret has no rune to invert, so it draws
// one inverted space: that is the insertion point itself, and it is the cell
// a typed rune would occupy. A line that already fills the frame width has no
// cell to spare and gets no caret rather than one cell of wrap (a wrapped
// line costs a physical row the per-line erase cannot reach). A caret cell
// past the visible end means the width clamp cut the line under the caret, so
// there is no rune to mark and the painter draws nothing instead of moving
// the caret to the line end, where it would point at the wrong rune.
func invertAtCell(line string, cell, width int) string {
	total := cellwidth.String(line)
	if cell > total {
		return line
	}
	off := caretByteOffset(line, cell)
	if off == len(line) {
		if total >= width {
			return line
		}
		return line + caretSGRIn + " " + caretSGROut
	}
	_, size := utf8.DecodeRuneInString(line[off:])
	return line[:off] + caretSGRIn + line[off:off+size] + caretSGROut + line[off+size:]
}

// markAtCell returns line with the plain-tier caret marker inserted at the
// caret cell, before the rune that sits there. The insertion shifts that rune
// and everything after it one cell right, so the result can be one cell wider
// than the line was; clampLine fits it back to the frame width by dropping
// cells from the tail, which is the documented plain-tier truncation. The
// clamp is the same one the composer applies, so an over-wide result cannot
// reach the terminal and an RTL-script line still ends in the visible
// "content withheld" marker instead of vanishing. The clipped-line rule
// matches invertAtCell: a caret past the visible end draws no marker at all.
func markAtCell(line string, cell, width int) string {
	if cell > cellwidth.String(line) {
		return line
	}
	off := caretByteOffset(line, cell)
	return clampLine(line[:off]+caretMarker+line[off:], width)
}

// runeSurvivesStrip reports whether a frame line keeps r once the control strip
// has run: tab survives because tab is legitimate frame content, and every
// other Cc control and every Cf format rune goes. This is the single predicate
// for that decision. stripControl applies it, and the caret re-measure in
// tui.go counts painted cells with it, so a strip category added on one side
// can never leave the other side counting a rune the painter does not show
// (re-review residual of finding A: the two sites had drifted into separate
// copies of the same expression).
func runeSurvivesStrip(r rune) bool {
	return r == '\t' || (!unicode.IsControl(r) && !unicode.Is(unicode.Cf, r))
}

// stripControl removes control and format characters from one frame line,
// mirroring the hybrid catalog precedent (internal/hybrid/catalog.go
// stripControlChars, FIX-50/FIX-51) with two deliberate differences: tab
// survives, because tab is legitimate frame content; and the Cf category
// (bidi overrides U+202A-U+202E / U+2066-U+2069, zero-width joiners and
// spaces, BOM) is dropped with the Cc controls, because a disk-borne
// right-to-left override or zero-width rune can reorder or spoof a rendered
// line without being a Cc control. Printable runes, every multi-byte UTF-8
// rune, and the legitimate combining marks (Mn/Me, not Cf) pass through
// untouched. Rune survival is decided by runeSurvivesStrip alone, which the
// caret re-measure shares. Newline structure is not handled here because
// frames are already split into lines before they reach the painter.
func stripControl(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return !runeSurvivesStrip(r) }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if !runeSurvivesStrip(r) {
			return -1
		}
		return r
	}, s)
}

// stripFrameLines applies stripControl to every line, sharing the slice
// when nothing changed so clean frames pay no allocation.
func stripFrameLines(lines []string) []string {
	out := make([]string, len(lines))
	changed := false
	for i, l := range lines {
		out[i] = stripControl(l)
		if out[i] != l {
			changed = true
		}
	}
	if !changed {
		return lines
	}
	return out
}

// stripUntrustedLines applies stripControl to every line except the
// trustedPrefix leading banner lines, which our own art generator composed and
// which carry legitimate SGR sequences. The strip ran over the whole frame
// once (fix round 1) and deleted the banner's own escape bytes, leaving
// visible junk like "[38;5;75m" on every color console (re-review seat 4,
// empirical repro); the untrusted tail is disk-borne content and stays
// stripped at this final sink, so an escape can never reach the terminal
// through a screen line regardless of what the composers did. The prefix is
// read through trustedPrefix, the same normalization the caret gate applies.
func stripUntrustedLines(f Frame) []string {
	trusted := f.trustedPrefix()
	out := make([]string, len(f.Lines))
	copy(out[:trusted], f.Lines[:trusted])
	copy(out[trusted:], stripFrameLines(f.Lines[trusted:]))
	return out
}

// mirrorLines mirrors every line of a frame for DirRTL composition.
func mirrorLines(lines []string, width int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = mirrorLine(l, width)
	}
	return out
}

// boxSwap pairs each directional box glyph with its horizontal mirror.
// Symmetric glyphs (─ │ ═ ║ ┼ and spaces) need no entry.
var boxSwap = map[rune]rune{
	'┌': '┐', '┐': '┌', '└': '┘', '┘': '└',
	'┏': '┓', '┓': '┏', '┗': '┛', '┛': '┗',
	'╔': '╗', '╗': '╔', '╚': '╝', '╝': '╚',
	'├': '┤', '┤': '├', '┬': '┴', '┴': '┬',
}

// isBoxRune reports whether r participates in a pure box-drawing line.
func isBoxRune(r rune) bool {
	if r == ' ' {
		return true
	}
	if _, ok := boxSwap[r]; ok {
		return true
	}
	switch r {
	case '─', '│', '═', '║', '┼':
		return true
	}
	return false
}

// mirrorLine mirrors one frame line geometrically for DirRTL:
//   - pure box-drawing lines: corners/tees swapped and the rune order
//     reversed (the string itself is mirrored), then right-aligned;
//   - lines with a leading "▶ " cursor marker: the marker moves to the
//     right end as " ◀";
//   - numbered rows "[N] label": the gutter becomes "label [N]";
//   - "·"-segmented lines (footers, legends): segment order reversed;
//   - every other line: right-aligned through cellwidth.
//
// Text content stays in logical order; only layout is mirrored.
func mirrorLine(line string, width int) string {
	runes := []rune(line)
	boxOnly := true
	for _, r := range runes {
		if !isBoxRune(r) {
			boxOnly = false
			break
		}
	}
	if boxOnly {
		for i, r := range runes {
			if s, ok := boxSwap[r]; ok {
				runes[i] = s
			}
		}
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return cellwidth.PadLeft(string(runes), width)
	}

	marker := ""
	body := line
	if strings.HasPrefix(body, "▶ ") {
		marker = " ◀"
		body = body[len("▶ "):]
	}
	if num, rest, ok := splitNumbered(strings.TrimLeft(body, " ")); ok {
		body = rest + " [" + num + "]"
	}
	if strings.Contains(body, " · ") {
		segs := strings.Split(body, " · ")
		for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
			segs[i], segs[j] = segs[j], segs[i]
		}
		body = strings.Join(segs, " · ")
	}
	return cellwidth.PadLeft(body+marker, width)
}

// splitNumbered splits a "[N] rest" row into its number and remainder.
// Anything that is not exactly that shape reports ok == false.
func splitNumbered(s string) (num, rest string, ok bool) {
	if !strings.HasPrefix(s, "[") {
		return "", "", false
	}
	i := strings.Index(s, "] ")
	if i < 2 {
		return "", "", false
	}
	num = s[1:i]
	for _, r := range num {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return num, s[i+2:], true
}
