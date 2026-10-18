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

package tui

import (
	"io"
	"strings"
	"unicode"

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
// styles are passed. Every line past the f.Trusted prefix is passed through
// stripControl first, so content-borne control characters (a crafted model
// id or provider name from a hand-edited registry file, plan review F3) can
// never reach the terminal or a pipe; the trusted leading lines (the banner
// block, composed by our own art code) pass through verbatim so their SGR
// sequences survive to the terminal. After the last line, any rows the
// previous frame occupied beyond this frame are erased (console tiers
// only), so switching from a tall screen to a short one leaves no ghost
// lines (review F-09).
func (p *Painter) Paint(f Frame, styles []Role) error {
	lines := stripUntrustedLines(f)
	if f.Dir == DirRTL {
		lines = mirrorLines(lines, f.Width)
	}
	tier := p.cap.Tier
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

// stripControl removes control and format characters from one frame line,
// mirroring the hybrid catalog precedent (internal/hybrid/catalog.go
// stripControlChars, FIX-50/FIX-51) with two deliberate differences: tab
// survives, because tab is legitimate frame content; and the Cf category
// (bidi overrides U+202A-U+202E / U+2066-U+2069, zero-width joiners and
// spaces, BOM) is dropped with the Cc controls, because a disk-borne
// right-to-left override or zero-width rune can reorder or spoof a rendered
// line without being a Cc control. Printable runes, every multi-byte UTF-8
// rune, and the legitimate combining marks (Mn/Me, not Cf) pass through
// untouched. Newline structure is not handled here because frames are
// already split into lines before they reach the painter.
func stripControl(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) {
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
// f.Trusted leading banner lines, which our own art generator composed and
// which carry legitimate SGR sequences. The strip ran over the whole frame
// once (fix round 1) and deleted the banner's own escape bytes, leaving
// visible junk like "[38;5;75m" on every color console (re-review seat 4,
// empirical repro); the untrusted tail is disk-borne content and stays
// stripped at this final sink, so an escape can never reach the terminal
// through a screen line regardless of what the composers did.
func stripUntrustedLines(f Frame) []string {
	trusted := f.Trusted
	if trusted < 0 {
		trusted = 0
	}
	if trusted > len(f.Lines) {
		trusted = len(f.Lines)
	}
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
