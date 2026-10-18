// banner.go renders the Gemini spark banner art in four color tiers
// (truecolor, 256, 16, plain) plus the ASCII fallback, and the compact
// 3-line and 1-line headers. All geometry and palette data lives in the
// tables below: retuning the art after the user's visual pass is a
// data-only edit.
//
// Technique (verified terminal research): one glyph, U+2584 LOWER HALF
// BLOCK, foreground = lower pixel row, background = upper pixel row
// (fg/bg swap). U+2580 is banned everywhere: legacy conhost CJK console
// fonts lack it and render hollow boxes. Every star cell has both halves
// filled by construction, so no partial cells exist. One text line is
// emitted per art line pair as a single string (single-flush write per
// line at the call site).
//
// Pipe policy: a plain tier coming from a non-console host suppresses the
// banner entirely (Cap.SuppressBanner in termcap.go decides that one step
// earlier). Render is only called when the capability layer has already
// decided an interactive console is present; this file never performs
// that detection itself.
package tui

import (
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
)

// rgb is one truecolor palette entry.
type rgb struct{ r, g, b int }

// sparkGradient is the 18-row vertical gradient (R0..R17), blue through
// violet to pink. Art pixel row i uses sparkGradient[i]; art line L
// covers rows 2L (upper half, background) and 2L+1 (lower half,
// foreground).
var sparkGradient = [18]rgb{
	{78, 156, 255},  // R0  #4E9CFF
	{89, 147, 254},  // R1  #5993FE
	{100, 138, 253}, // R2  #648AFD
	{112, 129, 252}, // R3  #7081FC
	{123, 121, 251}, // R4  #7B79FB
	{134, 112, 250}, // R5  #8670FA
	{146, 103, 249}, // R6  #9267F9
	{157, 94, 248},  // R7  #9D5EF8
	{168, 85, 247},  // R8  #A855F7
	{178, 86, 235},  // R9  #B256EB
	{187, 87, 223},  // R10 #BB57DF
	{197, 87, 211},  // R11 #C557D3
	{207, 88, 199},  // R12 #CF58C7
	{216, 89, 186},  // R13 #D859BA
	{226, 90, 174},  // R14 #E25AAE
	{236, 90, 162},  // R15 #EC5AA2
	{245, 91, 150},  // R16 #F55B96
	{255, 92, 138},  // R17 #FF5C8A
}

// spark256 is the precomputed nearest xterm-256 index per gradient row
// (nearest cube/grayscale by squared RGB distance, computed once at
// authoring time). Adjacent rows sometimes collapse to one index; the
// resulting banding at the blue and purple ends is accepted. The 256
// tier emits 38;5;N / 48;5;M from this table.
var spark256 = [18]int{
	75, 69, 69, 69, 105, 99, 99, 135, 135,
	135, 134, 170, 170, 169, 169, 205, 204, 204,
}

// spark16 is the hand-authored 16-color SGR code per gradient row (never
// algorithmic nearest-match, so the logo stays intentional): R0-R3
// bright blue 94, R4-R8 bright magenta 95, R9-R13 magenta 35, R14-R17
// bright red 91. The 16 tier renders FULL cells (U+2588, fg only) so
// the silhouette survives without 24-bit pairs.
var spark16 = [18]int{
	94, 94, 94, 94, 95, 95, 95, 95, 95,
	35, 35, 35, 35, 35, 91, 91, 91, 91,
}

// maskRun is one run of filled cells inside the art box, half-open
// [start, start+width).
type maskRun struct{ start, width int }

// needleMask is the v1 base geometry: the 15-column needle spark, 9 art
// lines. Each line is a single centered run (line runs L0:1, L1:1, L2:3,
// L3:7, L4:15, L5:7, L6:3, L7:1, L8:1); long thin vertical points, wide
// middle band, horizontal needle points at L4.
var needleMask = [9]maskRun{
	{7, 1}, {7, 1}, {6, 3}, {4, 7}, {0, 15}, {4, 7}, {6, 3}, {7, 1}, {7, 1},
}

// wingRows is the alternative geometry: the diagonal-wing spark as 16
// pixel rows of filled 1-indexed art columns (40-column box; art columns
// 3..38 are used). Four-pointed spark with long horizontal and vertical
// spikes and short diagonal spikes.
var wingRows = [16][]int{
	{20},                                     // row 1
	{19, 20, 21, 22},                         // row 2
	{19, 20, 21, 22},                         // row 3
	{12, 13, 18, 19, 20, 21, 22, 23, 28, 29}, // row 4
	{10, 11, 12, 13, 14, 18, 19, 20, 21, 22, 23, 26, 27, 28, 29, 30},                                                                          // row 5
	{9, 10, 11, 12, 13, 14, 15, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31},                                                   // row 6
	{15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26},                                                                                          // row 7
	{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38}, // row 8
	{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38}, // row 9
	{15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26},                                                                                          // row 10
	{9, 10, 11, 12, 13, 14, 15, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31},                                                   // row 11
	{10, 11, 12, 13, 14, 18, 19, 20, 21, 22, 23, 26, 27, 28, 29, 30},                                                                          // row 12
	{12, 13, 18, 19, 20, 21, 22, 23, 28, 29},                                                                                                  // row 13
	{19, 20, 21, 22},                                                                                                                          // row 14
	{19, 20, 21, 22},                                                                                                                          // row 15
	{21},                                                                                                                                      // row 16
}

// wingColors is the diagonal-wing palette: A hot core, B blue, C violet,
// D purple, E magenta. Row assignment: rows 1-2 B, 3-5 C, 6-8 D, 9-10 E,
// 11-12 D, 13-14 C, 15-16 B; the core A overrides rows 8-9 columns
// 19-22.
var wingColors = []rgb{
	{224, 236, 255}, // A core
	{110, 150, 255}, // B
	{120, 100, 250}, // C
	{150, 80, 240},  // D
	{210, 80, 170},  // E
}

// wingRowColor is the palette index per pixel row (1-indexed row n uses
// wingRowColor[n-1]).
var wingRowColor = [16]int{1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 3, 3, 2, 2, 1, 1}

// wingCoreColor/wingCoreRows/wingCoreCols bound the hot core override.
const (
	wingCoreColor = 0
	wingCoreRowLo = 8
	wingCoreRowHi = 9
	wingCoreColLo = 19
	wingCoreColHi = 22
)

// wing256 and wing16 are the per-palette-entry tier mappings for the
// wing variant (same authoring rules as the needle tables). wing16Bold
// marks entries that need SGR bold for contrast.
var wing256 = []int{255, 111, 105, 141, 175}
var wing16 = []int{97, 94, 35, 95, 35}
var wing16Bold = []bool{true, false, false, false, true}

// cell is one half-block cell: the palette indices of its upper and lower
// pixel rows. -1 marks an empty half (the terminal default shows
// through). A run of cells shares one SGR per tier.
type cell struct{ upper, lower int }

// sparkGeom is one complete spark geometry: the half-block cell grid and
// the per-tier palette columns.
type sparkGeom struct {
	name   string
	cells  [][]cell
	colors []rgb
	c256   []int
	c16    []int
	bold16 []bool
}

const (
	needleBoxW = 15
	wingBoxW   = 36
	fieldW     = 48
)

// SparkVariant selects the spark geometry: 0 is the v1 needle spark, 1 is
// the diagonal-wing spark. The user's visual pass picks between them; the
// switch is data-only.
var SparkVariant int

// needleGeom builds the v1 needle spark cell grid from the mask table:
// line L fills cells for rows 2L (upper) and 2L+1 (lower), both halves
// always filled by construction.
var needleGeom = sparkGeom{
	name:   "needle",
	colors: sparkGradient[:],
	c256:   spark256[:],
	c16:    spark16[:],
	cells:  buildNeedleCells(),
}

// wingGeom builds the diagonal-wing cell grid from the pixel-row table.
var wingGeom = sparkGeom{
	name:   "wing",
	colors: wingColors,
	c256:   wing256,
	c16:    wing16,
	bold16: wing16Bold,
	cells:  buildWingCells(),
}

func buildNeedleCells() [][]cell {
	lines := make([][]cell, len(needleMask))
	for l, run := range needleMask {
		line := make([]cell, needleBoxW)
		for i := range line {
			line[i] = cell{-1, -1}
		}
		for c := run.start; c < run.start+run.width; c++ {
			line[c] = cell{2 * l, 2*l + 1}
		}
		lines[l] = line
	}
	return lines
}

func buildWingCells() [][]cell {
	lines := make([][]cell, 8)
	for l := range lines {
		upper, lower := wingRows[2*l], wingRows[2*l+1]
		line := make([]cell, wingBoxW)
		for c := 0; c < wingBoxW; c++ {
			line[c] = cell{wingColorAt(2*l+1, c+3, upper), wingColorAt(2*l+2, c+3, lower)}
		}
		lines[l] = line
	}
	return lines
}

// wingColorAt returns the palette index for 1-indexed pixel (row, col),
// or -1 when that pixel is empty. The core override wins inside its box.
func wingColorAt(row, col int, filled []int) int {
	full := false
	for _, c := range filled {
		if c == col {
			full = true
			break
		}
	}
	if !full {
		return -1
	}
	if row >= wingCoreRowLo && row <= wingCoreRowHi && col >= wingCoreColLo && col <= wingCoreColHi {
		return wingCoreColor
	}
	return wingRowColor[row-1]
}

func activeGeom() *sparkGeom {
	if SparkVariant == 1 {
		return &wingGeom
	}
	return &needleGeom
}

// wordmarkStops is the per-letter gradient stop per letter of "AGY PRO":
// A G Y P R O sample R0 R4 R8 R12 R14 R17 of the gradient.
var wordmarkStops = []int{0, 4, 8, 12, 14, 17}

// wordmark16 is the hand-authored 16-color letter sequence for the
// wordmark (blue A to red O, matching the gradient bands).
var wordmark16 = []int{94, 95, 95, 35, 91, 91}

const wordmarkText = "A G Y   P R O"

// taglineColor is the dim gray tagline color per tier.
var taglineRGB = rgb{154, 160, 166}

const (
	tagline256 = 247
	tagline16  = 90
)

const (
	esc       = "\x1b["
	sgrReset  = "\x1b[0m"
	lowerHalf = "▄" // U+2584 LOWER HALF BLOCK
	fullBlock = "█" // U+2588 FULL BLOCK
)

// Render returns the banner lines for the active tier and console width.
// cols >= 52 renders the full 48-column field with wordmark and tagline;
// 32..51 renders the spark only, centered, with the field padding dropped
// first (when the active geometry is wider than the frame, as the 36-cell
// wing is at cols 32..35, the ASCII figure substitutes: truncating through
// an escape sequence would corrupt the SGR); below 32 renders the ASCII
// figure. A plain tier always renders the ASCII figure (that is the shipped
// fallback for NO_COLOR and VT-enable-failure hosts; a non-console host
// suppresses the banner entirely before this function is called). tagline
// is a parameter: the i18n lookup happens at the call site.
func Render(tier Tier, cols int, tagline string) []string {
	if tier == TierPlain || cols < 32 {
		return asciiFigure(cols, tagline)
	}
	g := activeGeom()
	boxW := len(g.cells[0])
	if cols >= 52 {
		left := (fieldW - boxW) / 2
		lines := renderSparkLines(tier, g, left, fieldW-boxW-left)
		lines = append(lines, renderWordmark(tier, fieldW))
		if tagline != "" {
			lines = append(lines, renderTagline(tier, tagline, fieldW))
		}
		return lines
	}
	if boxW > cols {
		return asciiFigure(cols, tagline)
	}
	left := (cols - boxW) / 2
	return renderSparkLines(tier, g, left, cols-boxW-left)
}

// RenderCompact returns the compact 3-line header: wordmark line,
// tagline line, gradient rule. The caller picks this from the sizing
// ladder (rows >= 22 and cols >= 30).
func RenderCompact(tier Tier, cols int, tagline string) []string {
	w := cols
	if w > fieldW {
		w = fieldW
	}
	lines := []string{renderWordmark(tier, w)}
	if tagline != "" {
		lines = append(lines, renderTagline(tier, tagline, w))
	}
	lines = append(lines, renderRule(tier, w))
	return lines
}

// RenderWordmark returns the 1-line wordmark header, the smallest sizing
// ladder step.
func RenderWordmark(tier Tier, cols int) []string {
	w := cols
	if w > fieldW {
		w = fieldW
	}
	return []string{renderWordmark(tier, w)}
}

// renderSparkLines renders every cell line of the geometry with the given
// box padding. One string per art line (single flush per line).
func renderSparkLines(tier Tier, g *sparkGeom, leftPad, rightPad int) []string {
	lines := make([]string, len(g.cells))
	for i, cells := range g.cells {
		lines[i] = strings.Repeat(" ", leftPad) + renderCellLine(tier, g, cells) + strings.Repeat(" ", rightPad)
	}
	return lines
}

// cellRun is a maximal run of cells sharing the same (upper, lower)
// palette indices.
type cellRun struct {
	c   cell
	n   int
	pos int
}

func groupRuns(cells []cell) []cellRun {
	var runs []cellRun
	for i, c := range cells {
		if len(runs) > 0 && runs[len(runs)-1].c == c {
			runs[len(runs)-1].n++
			continue
		}
		runs = append(runs, cellRun{c: c, n: 1, pos: i})
	}
	return runs
}

// renderCellLine emits one art line for the tier: truecolor and 256 use
// U+2584 with fg = lower row and bg = upper row; 16 uses full cells
// U+2588 fg only so the silhouette survives without 24-bit pairs. Every
// colored run opens and closes its SGR so empty-cell padding always sits
// outside the escape sequence.
func renderCellLine(tier Tier, g *sparkGeom, cells []cell) string {
	var b strings.Builder
	for _, run := range groupRuns(cells) {
		switch {
		case run.c.upper < 0 && run.c.lower < 0:
			b.WriteString(strings.Repeat(" ", run.n))
		case tier == TierTruecolor:
			if run.c.upper < 0 {
				b.WriteString(sgrTruecolorFg(g.colors[run.c.lower]))
				b.WriteString(";49m")
			} else if run.c.lower < 0 {
				b.WriteString(esc + "39;")
				b.WriteString(sgrTruecolorBg(g.colors[run.c.upper]))
				b.WriteString("m")
			} else {
				b.WriteString(sgrTruecolorFg(g.colors[run.c.lower]))
				b.WriteString(";")
				b.WriteString(sgrTruecolorBg(g.colors[run.c.upper]))
				b.WriteString("m")
			}
			b.WriteString(strings.Repeat(lowerHalf, run.n))
			b.WriteString(sgrReset)
		case tier == Tier256:
			if run.c.upper < 0 {
				b.WriteString(esc + "38;5;" + itoa(g.c256[run.c.lower]) + ";49m")
			} else if run.c.lower < 0 {
				b.WriteString(esc + "39;48;5;" + itoa(g.c256[run.c.upper]) + "m")
			} else {
				b.WriteString(esc + "38;5;" + itoa(g.c256[run.c.lower]) + ";48;5;" + itoa(g.c256[run.c.upper]) + "m")
			}
			b.WriteString(strings.Repeat(lowerHalf, run.n))
			b.WriteString(sgrReset)
		case tier == Tier16:
			code := g.c16[run.c.lower]
			if run.c.lower < 0 {
				code = g.c16[run.c.upper]
			}
			if len(g.bold16) > 0 && g.bold16[colorIndex(g, run.c)] {
				b.WriteString(esc + "1;" + itoa(code) + "m")
			} else {
				b.WriteString(esc + itoa(code) + "m")
			}
			b.WriteString(strings.Repeat(fullBlock, run.n))
			b.WriteString(sgrReset)
		default:
			b.WriteString(strings.Repeat(" ", run.n))
		}
	}
	return b.String()
}

// colorIndex returns the palette index a run paints with in the 16 tier.
func colorIndex(g *sparkGeom, c cell) int {
	if c.lower >= 0 {
		return c.lower
	}
	return c.upper
}

func sgrTruecolorFg(c rgb) string {
	return esc + "38;2;" + itoa(c.r) + ";" + itoa(c.g) + ";" + itoa(c.b)
}

func sgrTruecolorBg(c rgb) string {
	return "48;2;" + itoa(c.r) + ";" + itoa(c.g) + ";" + itoa(c.b)
}

// renderWordmark renders "AGY PRO" bold with per-letter gradient stops,
// centered through cellwidth so the padding is cell-accurate.
func renderWordmark(tier Tier, w int) string {
	left := (w - cellwidth.String(wordmarkText)) / 2
	if left < 0 {
		left = 0
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", left))
	letters := []rune{'A', 'G', 'Y', 'P', 'R', 'O'}
	for i, ch := range letters {
		if i > 0 {
			if i == 3 {
				b.WriteString("   ")
			} else {
				b.WriteString(" ")
			}
		}
		switch tier {
		case TierTruecolor:
			c := sparkGradient[wordmarkStops[i]]
			b.WriteString(esc + "1;38;2;" + itoa(c.r) + ";" + itoa(c.g) + ";" + itoa(c.b) + "m" + string(ch) + sgrReset)
		case Tier256:
			b.WriteString(esc + "1;38;5;" + itoa(spark256[wordmarkStops[i]]) + "m" + string(ch) + sgrReset)
		case Tier16:
			b.WriteString(esc + itoa(wordmark16[i]) + "m" + string(ch) + sgrReset)
		default:
			b.WriteRune(ch)
		}
	}
	padRight := w - left - cellwidth.String(wordmarkText)
	if padRight > 0 {
		b.WriteString(strings.Repeat(" ", padRight))
	}
	return b.String()
}

// renderTagline renders the dim tagline centered through cellwidth, so a
// CJK tagline centers on cells, not runes.
func renderTagline(tier Tier, tagline string, w int) string {
	left := (w - cellwidth.String(tagline)) / 2
	if left < 0 {
		left = 0
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", left))
	switch tier {
	case TierTruecolor:
		b.WriteString(esc + "2;38;2;" + itoa(taglineRGB.r) + ";" + itoa(taglineRGB.g) + ";" + itoa(taglineRGB.b) + "m" + tagline + sgrReset)
	case Tier256:
		b.WriteString(esc + "2;38;5;" + itoa(tagline256) + "m" + tagline + sgrReset)
	case Tier16:
		b.WriteString(esc + "2;" + itoa(tagline16) + "m" + tagline + sgrReset)
	default:
		b.WriteString(tagline)
	}
	padRight := w - left - cellwidth.String(tagline)
	if padRight > 0 {
		b.WriteString(strings.Repeat(" ", padRight))
	}
	return b.String()
}

// renderRule renders the gradient rule of the compact header: a run of
// full cells sampling the gradient R0..R17 left to right.
func renderRule(tier Tier, w int) string {
	if w <= 0 {
		return ""
	}
	if tier == TierPlain {
		return strings.Repeat("=", w)
	}
	var b strings.Builder
	prev := -1
	count := 0
	flush := func(idx, n int) {
		if n <= 0 {
			return
		}
		switch tier {
		case TierTruecolor:
			c := sparkGradient[idx]
			b.WriteString(esc + "38;2;" + itoa(c.r) + ";" + itoa(c.g) + ";" + itoa(c.b) + "m")
		case Tier256:
			b.WriteString(esc + "38;5;" + itoa(spark256[idx]) + "m")
		case Tier16:
			b.WriteString(esc + itoa(spark16[idx]) + "m")
		}
		b.WriteString(strings.Repeat(fullBlock, n))
	}
	for i := 0; i < w; i++ {
		idx := 0
		if w > 1 {
			idx = i * 17 / (w - 1)
		}
		if idx == prev {
			count++
			continue
		}
		flush(prev, count)
		prev = idx
		count = 1
	}
	flush(prev, count)
	b.WriteString(sgrReset)
	return b.String()
}

// asciiFigure is the plain fallback: 7 ASCII figure lines plus the
// wordmark and tagline lines, everything centered through cellwidth.
// Zero escape bytes by construction.
var asciiFigureLines = []string{
	"        *",
	"       /|\\",
	"      / | \\",
	"  *---< + >---*",
	"      \\ | /",
	"       \\|/",
	"        *",
}

func asciiFigure(cols int, tagline string) []string {
	w := cols
	if w > fieldW {
		w = fieldW
	}
	lines := make([]string, 0, len(asciiFigureLines)+2)
	for _, line := range asciiFigureLines {
		lines = append(lines, cellwidth.Center(line, w))
	}
	lines = append(lines, cellwidth.Center(wordmarkText, w))
	if tagline != "" {
		lines = append(lines, cellwidth.Center(tagline, w))
	}
	return lines
}

// itoa delegates to strconv.Itoa.
func itoa(n int) string { return strconv.Itoa(n) }
