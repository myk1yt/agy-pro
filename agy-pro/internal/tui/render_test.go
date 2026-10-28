package tui

import (
	"bytes"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
)

// countWriter counts Write calls so tests can pin the single-flush rule
// (no escape sequence split across writes).
type countWriter struct {
	n   int
	buf bytes.Buffer
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.n++
	return c.buf.Write(p)
}

func TestPainterPlainEmitsZeroEscapeBytes(t *testing.T) {
	// Pipe policy: a plain cap (non-console stdout) must produce zero ESC
	// bytes even when styles are passed, so nothing ANSI ever lands in a
	// pipe or file.
	f := Frame{Width: 20, Height: 4, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{
		"Title", "body", "warn line", "footer",
	}}
	styles := []Role{RoleTitle, RoleNone, RoleWarn, RoleNone}
	for _, tier := range []Tier{TierPlain} {
		var out bytes.Buffer
		p := NewPainter(&out, Cap{Tier: tier, Palette: DefaultPalette()})
		if err := p.Paint(f, styles); err != nil {
			t.Fatalf("Paint: %v", err)
		}
		if bytes.ContainsRune(out.Bytes(), 0x1b) {
			t.Fatalf("tier %v: output contains ESC bytes:\n%q", tier, out.String())
		}
		want := strings.Join(f.Lines, "\n")
		if out.String() != want {
			t.Errorf("tier %v: plain output = %q; want %q", tier, out.String(), want)
		}
	}
}

func TestPainterTieredGolden(t *testing.T) {
	f := Frame{Width: 20, Height: 3, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"Title", "body"}}
	styles := []Role{RoleTitle, RoleNone}
	pal := DefaultPalette()

	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: TierTruecolor, Palette: pal})
	if err := p.Paint(f, styles); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	want := "\x1b[H" +
		"\x1b[2K\x1b[38;2;78;156;255mTitle\x1b[0m" +
		"\n\x1b[2Kbody"
	if out.String() != want {
		t.Errorf("truecolor frame =\n%q\nwant\n%q", out.String(), want)
	}

	out.Reset()
	p = NewPainter(&out, Cap{Tier: Tier256, Palette: pal})
	if err := p.Paint(f, styles); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if !strings.Contains(out.String(), "\x1b[38;5;111mTitle\x1b[0m") {
		t.Errorf("256 frame missing the 256-column title color:\n%q", out.String())
	}

	out.Reset()
	p = NewPainter(&out, Cap{Tier: Tier16, Palette: pal})
	if err := p.Paint(f, styles); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if !strings.Contains(out.String(), "\x1b[94mTitle\x1b[0m") {
		t.Errorf("16 frame missing the 16-column title color:\n%1q", out.String())
	}
}

func TestPainterSingleFlush(t *testing.T) {
	f := Frame{Width: 10, Height: 3, CaretLine: -1, CaretCell: -1, Lines: []string{"a", "b", "c"}}
	cw := &countWriter{}
	p := NewPainter(cw, Cap{Tier: TierTruecolor, Palette: DefaultPalette()})
	if err := p.Paint(f, []Role{RoleTitle, RoleNone, RoleWarn}); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if cw.n != 1 {
		t.Errorf("frame written in %d calls; want 1 (atomic, no split escape)", cw.n)
	}
}

func TestPainterSuppressBannerFlagIsPartOfCap(t *testing.T) {
	// The plain non-console cap carries SuppressBanner so the banner unit
	// can stay silent in pipes. The painter itself ignores the flag.
	cap := Cap{Tier: TierPlain, Palette: DefaultPalette(), SuppressBanner: true}
	if !cap.SuppressBanner {
		t.Error("SuppressBanner must survive in the Cap")
	}
}

func TestMirrorLineBoxCorners(t *testing.T) {
	got := mirrorLine("┌────┐", 10)
	if want := "    ┌────┐"; got != want {
		t.Errorf("mirrorLine(box) = %q; want %q", got, want)
	}
	// An asymmetric box row mirrors its shape, not just its corners.
	got = mirrorLine("┌──┐ ", 6)
	if want := "  ┌──┐"; got != want {
		t.Errorf("mirrorLine(asymmetric box) = %q; want %q", got, want)
	}
}

func TestMirrorFrameGeometry(t *testing.T) {
	// Fixed frame: box top, two list rows (cursor on the first), footer.
	lines := []string{
		"┌──────────────────┐",
		"▶ [1] Alpha",
		"  [2] Beta",
		"move · select · quit",
	}
	mirrored := mirrorLines(lines, 20)
	want := []string{
		"┌──────────────────┐", // symmetric box row, right-aligned
		"         Alpha [1] ◀", // gutter moved right, cursor marker right
		"            Beta [2]", // numbering on the right
		"quit · select · move", // footer segments reversed
	}
	for i := range want {
		if mirrored[i] != want[i] {
			t.Errorf("line %d = %q; want %q", i, mirrored[i], want[i])
		}
	}
}

func TestMirrorPlainTextRightAligns(t *testing.T) {
	got := mirrorLine("hello", 10)
	if want := "     hello"; got != want {
		t.Errorf("mirrorLine(text) = %q; want %q", got, want)
	}
}

func TestPainterMirrorsOnlyWhenDirRTL(t *testing.T) {
	f := Frame{Width: 10, Height: 1, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"hello"}}
	var ltr bytes.Buffer
	if err := NewPainter(&ltr, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if ltr.String() != "hello" {
		t.Errorf("LTR frame = %q; want untouched %q", ltr.String(), "hello")
	}

	f.Dir = DirRTL
	var rtl bytes.Buffer
	if err := NewPainter(&rtl, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if rtl.String() != "     hello" {
		t.Errorf("RTL frame = %q; want right-aligned", rtl.String())
	}
}

func TestPainterCapAccessor(t *testing.T) {
	cap := Cap{Tier: Tier256, Palette: DefaultPalette(), Width: 90, Height: 30}
	p := NewPainter(&bytes.Buffer{}, cap)
	got := p.Cap()
	if got.Tier != cap.Tier || got.Width != cap.Width || got.Height != cap.Height ||
		got.SuppressBanner != cap.SuppressBanner {
		t.Error("Cap() must round-trip the capability object fields")
	}
}

// TestPainterStripsContentBorneControlChars pins the F3 fix: a frame line
// carrying an escape-bearing model id (crafted providers.json content)
// renders with every control character removed, on every tier.
func TestPainterStripsContentBorneControlChars(t *testing.T) {
	f := Frame{Width: 40, Height: 2, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{
		"ok\x1b]0;pwned\x07\x1b[2J",
		"clean",
	}}
	for _, tier := range []Tier{TierPlain, Tier16, Tier256, TierTruecolor} {
		var out bytes.Buffer
		p := NewPainter(&out, Cap{Tier: tier, Palette: DefaultPalette()})
		if err := p.Paint(f, nil); err != nil {
			t.Fatalf("Paint tier %v: %v", tier, err)
		}
		body := out.String()
		for _, banned := range []string{"\x1b]0;pwned", "\x07", "\x1b[2J"} {
			if strings.Contains(body, banned) {
				t.Errorf("tier %v: output still contains %q:\n%q", tier, banned, body)
			}
		}
		if !strings.Contains(body, "ok") || !strings.Contains(body, "clean") {
			t.Errorf("tier %v: printable content lost:\n%q", tier, body)
		}
	}
}

// TestPainterStripKeepsTabAndUTF8 pins that legitimate content survives
// the strip: tab stays, and multi-byte UTF-8 (CJK, Arabic) is untouched.
func TestPainterStripKeepsTabAndUTF8(t *testing.T) {
	f := Frame{Width: 40, Height: 2, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{
		"col1\tcol2",
		"日本語 العربية",
	}}
	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()})
	if err := p.Paint(f, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if want := "col1\tcol2\n日本語 العربية"; out.String() != want {
		t.Errorf("stripped frame = %q; want %q", out.String(), want)
	}
}

// TestPainterPlainTierZeroEscapeBytesWithDirtyContent pins the C3 pipe
// policy for content-borne escapes: even a frame whose lines carry ESC
// bytes paints to a plain-tier buffer with zero ESC bytes anywhere.
func TestPainterPlainTierZeroEscapeBytesWithDirtyContent(t *testing.T) {
	f := Frame{Width: 40, Height: 3, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{
		"Title\x1b[31m",
		"b\x1body",
		"footer",
	}}
	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()})
	if err := p.Paint(f, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if bytes.ContainsRune(out.Bytes(), 0x1b) {
		t.Fatalf("plain output contains ESC bytes:\n%q", out.String())
	}
	if want := "Title[31m\nbody\nfooter"; out.String() != want {
		t.Errorf("plain stripped output = %q; want %q", out.String(), want)
	}
}

// TestPainterShrinkErasesGhostRows pins the F-09 fix: a tall frame followed
// by a short frame emits one erase sequence per dropped row on console
// tiers, so no stale lines survive below the new frame end.
func TestPainterShrinkErasesGhostRows(t *testing.T) {
	pal := DefaultPalette()
	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: Tier256, Palette: pal})

	tall := Frame{Width: 20, Height: 6, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"t1", "t2", "t3", "t4"}}
	short := Frame{Width: 20, Height: 6, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"s1", "s2"}}

	if err := p.Paint(tall, nil); err != nil {
		t.Fatalf("Paint tall: %v", err)
	}
	first := out.String()
	// First paint has no previous frame: no shrink erase allowed.
	if strings.Contains(first, "\x1b[2K\n") || strings.HasSuffix(first, "\n\x1b[2K") {
		t.Errorf("first paint must not carry shrink erase bytes:\n%q", first)
	}

	out.Reset()
	if err := p.Paint(short, nil); err != nil {
		t.Fatalf("Paint short: %v", err)
	}
	second := out.String()
	// Two rows were dropped (t3, t4): the paint carries 4 erases total
	// (2 body rows + 2 shrink rows) and must end with the two consecutive
	// dropped-row erases, which no inter-line erase can produce.
	if got := strings.Count(second, "\x1b[2K"); got != 4 {
		t.Errorf("erase count = %d, want 4 (2 body + 2 shrink):\n%q", got, second)
	}
	if !strings.HasSuffix(second, "\n\x1b[2K\n\x1b[2K") {
		t.Errorf("shrink paint must end with the dropped-row erases:\n%q", second)
	}

	// Same-height repaint: no erase bytes, byte-identical to a fresh paint.
	out.Reset()
	if err := p.Paint(short, nil); err != nil {
		t.Fatalf("Paint same: %v", err)
	}
	fresh := &countWriter{}
	p2 := NewPainter(fresh, Cap{Tier: Tier256, Palette: pal})
	if err := p2.Paint(short, nil); err != nil {
		t.Fatalf("Paint fresh: %v", err)
	}
	if out.String() != fresh.buf.String() {
		t.Errorf("same-height repaint =\n%q\nwant (fresh paint)\n%q", out.String(), fresh.buf.String())
	}
}

// TestPainterShrinkPlainTierStaysTextOnly pins that the shrink erase never
// reaches non-console output: two plain-tier paints of shrinking frames are
// byte-identical to the bare joined lines, zero escape bytes.
func TestPainterShrinkPlainTierStaysTextOnly(t *testing.T) {
	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()})
	tall := Frame{Width: 20, Height: 6, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"t1", "t2", "t3", "t4"}}
	short := Frame{Width: 20, Height: 6, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"s1", "s2"}}
	if err := p.Paint(tall, nil); err != nil {
		t.Fatalf("Paint tall: %v", err)
	}
	if err := p.Paint(short, nil); err != nil {
		t.Fatalf("Paint short: %v", err)
	}
	// Each plain paint is a bare dump with no trailing newline (App.Run adds
	// the frame separator), so two paints concatenate exactly.
	want := "t1\nt2\nt3\nt4s1\ns2"
	if out.String() != want {
		t.Errorf("plain shrink output = %q; want %q", out.String(), want)
	}
	if bytes.ContainsRune(out.Bytes(), 0x1b) {
		t.Errorf("plain shrink output contains ESC bytes:\n%q", out.String())
	}
}

// TestStripControlUnit pins the strip semantics directly: C0 and C1 controls
// except tab drop (ESC and DEL included); the Cf format runes (bidi
// overrides, zero-width space, BOM) drop with them; tab, printable ASCII,
// CJK, Arabic (including its Mn combining marks), and box glyphs survive.
func TestStripControlUnit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ok\x1b]0;pwned\x07\x1b[2J", "ok]0;pwned[2J"},
		{"a\x00b\x0cc\x7fd", "abcd"},
		{"col1\tcol2", "col1\tcol2"},
		{"日本語", "日本語"},
		{"العربية", "العربية"},
		{"مُحمد", "مُحمد"}, // Arabic with U+064F DAMMA (Mn): legitimate, survives
		{"┌──┐", "┌──┐"},
		{"plain", "plain"},
		// Cf class (round-6 widening): bidi overrides and zero-width runes
		// are dropped from rendered lines.
		{"a\u202eb", "ab"},       // RLO
		{"a\u202ab", "ab"},       // LRE
		{"a\u2066b\u2069", "ab"}, // LRI + PDI
		{"x\u200by", "xy"},       // zero-width space
		{"\ufeffbom", "bom"},     // BOM
	}
	for _, c := range cases {
		if got := stripControl(c.in); got != c.want {
			t.Errorf("stripControl(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestRuneSurvivesStripParity pins the one strip predicate against the strip it
// decides for, rune by rune, and against the category rule itself. stripControl
// applies runeSurvivesStrip and the caret re-measure in tui.go counts painted
// cells with it, so a category added on one side only would shift the caret
// instead of failing a test; a category changed on both sides at once would keep
// the two in agreement while breaking the contract, which is why every corpus
// entry also carries the verdict the strip is supposed to reach. The corpus
// separates every class the decision has to tell apart: Cc controls (tab
// survives, CR and the SOH/STX bytes a prompt file really carries do not), Cf
// format runes (bidi override, zero-width space, zero-width joiner, BOM), the
// legitimate combining marks (Mn and Me, which are not Cf and must survive),
// CJK, Arabic, a box glyph, and plain ASCII.
func TestRuneSurvivesStripParity(t *testing.T) {
	corpus := []struct {
		name     string
		r        rune
		survives bool
	}{
		{"NUL (Cc)", '\x00', false},
		{"SOH (Cc)", '\x01', false},
		{"STX (Cc)", '\x02', false},
		{"BEL (Cc)", '\x07', false},
		{"TAB (Cc, survives)", '\t', true},
		{"CR (Cc)", '\r', false},
		{"ESC (Cc)", '\x1b', false},
		{"DEL (Cc)", '\x7f', false},
		{"NEL (C1 control)", '\u0085', false},
		{"RLO (Cf)", '\u202e', false},
		{"LRE (Cf)", '\u202a', false},
		{"LRI (Cf)", '\u2066', false},
		{"ZWSP (Cf)", '\u200b', false},
		{"ZWJ (Cf)", '\u200d', false},
		{"BOM (Cf)", '\ufeff', false},
		{"Arabic damma (Mn)", '\u064f', true},
		{"combining grave (Mn)", '\u0301', true},
		{"enclosing circle (Me)", '\u20dd', true},
		{"ASCII letter", 'a', true},
		{"CJK", '日', true},
		{"Arabic letter", 'م', true},
		{"box glyph", '┌', true},
		{"emoji", '\U0001F642', true},
	}
	for _, c := range corpus {
		if got := runeSurvivesStrip(c.r); got != c.survives {
			t.Errorf("%s: runeSurvivesStrip(%U) = %v, want %v", c.name, c.r, got, c.survives)
		}
		embedded, want := "a"+string(c.r)+"b", "ab"
		if c.survives {
			want = embedded
		}
		if got := stripControl(embedded); got != want {
			t.Errorf("%s: stripControl(%q) = %q, want %q (runeSurvivesStrip = %v)",
				c.name, embedded, got, want, c.survives)
		}
		// The single-rune form takes the other branch of the strip (the
		// ContainsFunc early return), so it is checked as well.
		singleWant := ""
		if c.survives {
			singleWant = string(c.r)
		}
		if got := stripControl(string(c.r)); got != singleWant {
			t.Errorf("%s: stripControl(%q) = %q, want %q", c.name, string(c.r), got, singleWant)
		}
	}
}

// TestPainterStripsBidiOverridePins the round-6 Cf widening at the final
// sink: a disk-borne RTL override cannot reach the terminal (plain tier, so
// the output is pure text), while the Arabic letters and their combining
// mark survive intact.
func TestPainterStripsBidiOverridePins(t *testing.T) {
	f := Frame{Width: 40, Height: 1, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{
		"مد\u202eيرة المت\u200bجر",
	}}
	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()})
	if err := p.Paint(f, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	body := out.String()
	for _, banned := range []string{"\u202e", "\u200b"} {
		if strings.Contains(body, banned) {
			t.Fatalf("plain output still contains %U:\n%q", []rune(banned)[0], body)
		}
	}
	if want := "مديرة المتجر"; body != want {
		t.Errorf("stripped line = %q; want %q", body, want)
	}
}

// csiSeqs returns every CSI sequence in s (ESC [ params intermediates
// final), so a pin can inventory the escape bytes a frame emits instead of
// trusting that no new sequence class appeared.
func csiSeqs(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			out = append(out, s[i:j+1])
			i = j
		}
	}
	return out
}

// visibleText removes every CSI sequence from a painted line so a pin can
// measure the cells the line really occupies. The package's stripSGR
// (banner_test.go) only understands SGR finals; a painted frame also carries
// the erase-line sequence, which has no trailing "m", so this walk consumes
// any CSI final byte and cannot run past a sequence.
func visibleText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// caretFrame is the shared fixture of the caret pins: one styled title line
// and one unstyled body line, with the caret at (0, 1) unless a case moves
// it. The width leaves room so the caret is never dropped by the width
// fail-safe.
func caretFrame() Frame {
	return Frame{
		Width: 20, Height: 2, Dir: DirLTR,
		CaretLine: 0, CaretCell: 1,
		Lines: []string{"hello", "body"},
	}
}

// TestPainterCaretInvertsRuneOnColorTiers pins the color-tier glyph byte for
// byte on all three emission columns: the role SGR still wraps the whole
// line, and the reverse-video pair (SGR 7 / SGR 27) sits INSIDE it, so 27
// cancels only the attribute it introduced and the role foreground color
// stays active for the rest of the line until the existing role reset.
func TestPainterCaretInvertsRuneOnColorTiers(t *testing.T) {
	f := caretFrame()
	styles := []Role{RoleTitle, RoleNone}
	pal := DefaultPalette()
	cases := []struct {
		tier Tier
		fg   string
	}{
		{TierTruecolor, "\x1b[38;2;78;156;255m"},
		{Tier256, "\x1b[38;5;111m"},
		{Tier16, "\x1b[94m"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		p := NewPainter(&out, Cap{Tier: c.tier, Palette: pal})
		if err := p.Paint(f, styles); err != nil {
			t.Fatalf("tier %v: Paint: %v", c.tier, err)
		}
		want := "\x1b[H" +
			"\x1b[2K" + c.fg + "h\x1b[7me\x1b[27mllo" + "\x1b[0m" +
			"\n\x1b[2Kbody"
		if got := out.String(); got != want {
			t.Errorf("tier %v caret frame =\n%q\nwant\n%q", c.tier, got, want)
		}
		if bytes.Count(out.Bytes(), []byte("\x1b[7m")) != 1 {
			t.Errorf("tier %v: caret SGR count = %d, want exactly 1:\n%q",
				c.tier, bytes.Count(out.Bytes(), []byte("\x1b[7m")), out.String())
		}
	}

	// Control: the same frame with no live caret emits no caret SGR at all,
	// so the pair is written only for a live caret.
	f.CaretLine, f.CaretCell = -1, -1
	for _, tier := range []Tier{Tier16, Tier256, TierTruecolor} {
		var out bytes.Buffer
		if err := NewPainter(&out, Cap{Tier: tier, Palette: pal}).Paint(f, styles); err != nil {
			t.Fatalf("tier %v: Paint: %v", tier, err)
		}
		if bytes.Contains(out.Bytes(), []byte("\x1b[7m")) {
			t.Errorf("tier %v: no-caret frame carries the caret SGR:\n%q", tier, out.String())
		}
	}
}

// TestPainterCaretEndOfLineInvertsSpace pins the insertion point at the end
// of the text: there is no rune to invert, so the caret is one inverted
// space, on the color tiers and as the plain marker.
func TestPainterCaretEndOfLineInvertsSpace(t *testing.T) {
	f := Frame{Width: 10, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 3, Lines: []string{"abc"}}

	var out bytes.Buffer
	if err := NewPainter(&out, Cap{Tier: Tier16, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if want := "\x1b[H\x1b[2Kabc\x1b[7m \x1b[27m"; out.String() != want {
		t.Errorf("end-of-line caret = %q; want %q", out.String(), want)
	}

	out.Reset()
	if err := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
		t.Fatalf("Paint plain: %v", err)
	}
	if want := "abc|"; out.String() != want {
		t.Errorf("plain end-of-line caret = %q; want %q", out.String(), want)
	}
	if bytes.ContainsRune(out.Bytes(), 0x1b) {
		t.Errorf("plain end-of-line caret emitted ESC bytes:\n%q", out.String())
	}
}

// TestPainterCaretFullWidthEndOfLineIsDropped pins the width fail-safe: a
// line that already fills the frame has no cell for the end-of-line caret,
// so the frame renders exactly as it would without one instead of wrapping
// one cell (a wrapped line costs a row the per-line erase cannot reach).
func TestPainterCaretFullWidthEndOfLineIsDropped(t *testing.T) {
	with := Frame{Width: 3, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 3, Lines: []string{"abc"}}
	without := Frame{Width: 3, Height: 1, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"abc"}}
	for _, tier := range []Tier{TierPlain, Tier16, Tier256, TierTruecolor} {
		var a, b bytes.Buffer
		if err := NewPainter(&a, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(with, nil); err != nil {
			t.Fatalf("tier %v: Paint with: %v", tier, err)
		}
		if err := NewPainter(&b, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(without, nil); err != nil {
			t.Fatalf("tier %v: Paint without: %v", tier, err)
		}
		if a.String() != b.String() {
			t.Errorf("tier %v: full-width end-of-line caret changed the frame:\nwith    %q\nwithout %q",
				tier, a.String(), b.String())
		}
	}
}

// TestPainterPlainCaretMarkerTruncatesTail pins the plain tier glyph: the
// marker is INSERTED at the caret cell, so the rune that was there shifts
// right, and an over-wide result loses exactly one cell from its tail. The
// output carries zero ESC bytes, caret included.
func TestPainterPlainCaretMarkerTruncatesTail(t *testing.T) {
	// A full-width line: inserting the marker overflows by one cell, so the
	// tail drops exactly one cell.
	full := Frame{Width: 6, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 2, Lines: []string{"abcdef"}}
	var out bytes.Buffer
	if err := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(full, nil); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if want := "ab|cde"; out.String() != want {
		t.Errorf("plain caret on a full line = %q; want %q (marker inserted, tail cut by one cell)", out.String(), want)
	}
	if got := cellwidth.String(visibleText(out.String())); got != 6 {
		t.Errorf("plain caret line occupies %d cells, want 6 (frame width)", got)
	}
	if bytes.ContainsRune(out.Bytes(), 0x1b) {
		t.Errorf("plain caret frame emitted ESC bytes:\n%q", out.String())
	}

	// A line with room: nothing is truncated.
	roomy := Frame{Width: 20, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 2, Lines: []string{"abcdef"}}
	out.Reset()
	if err := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(roomy, nil); err != nil {
		t.Fatalf("Paint roomy: %v", err)
	}
	if want := "ab|cdef"; out.String() != want {
		t.Errorf("plain caret with room = %q; want %q", out.String(), want)
	}

	// An empty line gets the marker alone: the most common prompt-editor
	// case, a caret in an empty buffer.
	empty := Frame{Width: 20, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 0, Lines: []string{""}}
	out.Reset()
	if err := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(empty, nil); err != nil {
		t.Fatalf("Paint empty: %v", err)
	}
	if want := "|"; out.String() != want {
		t.Errorf("plain caret on an empty line = %q; want %q", out.String(), want)
	}
}

// TestPainterCaretSnapsToWideRune pins the CJK rule: the caret cell is a
// display-cell offset, so a cell inside a two-cell rune belongs to that rune
// (the caret never splits a glyph), and the plain marker is inserted before
// the whole wide rune.
func TestPainterCaretSnapsToWideRune(t *testing.T) {
	for _, cell := range []int{2, 3} {
		f := Frame{Width: 20, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: cell, Lines: []string{"日本語"}}
		var out bytes.Buffer
		if err := NewPainter(&out, Cap{Tier: Tier16, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
			t.Fatalf("cell %d: Paint: %v", cell, err)
		}
		if want := "\x1b[H\x1b[2K日\x1b[7m本\x1b[27m語"; out.String() != want {
			t.Errorf("cell %d: wide-rune caret = %q; want %q (snap to the rune start)", cell, out.String(), want)
		}

		out.Reset()
		if err := NewPainter(&out, Cap{Tier: TierPlain, Palette: DefaultPalette()}).Paint(f, nil); err != nil {
			t.Fatalf("cell %d: Paint plain: %v", cell, err)
		}
		if want := "日|本語"; out.String() != want {
			t.Errorf("cell %d: plain wide-rune caret = %q; want %q", cell, out.String(), want)
		}
	}
}

// TestPainterCaretClipsNothingAtCellPastEnd pins the clipped-line rule: a
// caret cell past the visible end means the width clamp cut the text under
// the caret, and the painter draws no caret rather than relocating it to the
// line end, where it would point at the wrong rune.
func TestPainterCaretClipsNothingAtCellPastEnd(t *testing.T) {
	with := Frame{Width: 10, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 7, Lines: []string{"abc"}}
	without := Frame{Width: 10, Height: 1, Dir: DirLTR, CaretLine: -1, CaretCell: -1, Lines: []string{"abc"}}
	for _, tier := range []Tier{TierPlain, Tier16, Tier256, TierTruecolor} {
		var a, b bytes.Buffer
		if err := NewPainter(&a, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(with, nil); err != nil {
			t.Fatalf("tier %v: Paint with: %v", tier, err)
		}
		if err := NewPainter(&b, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(without, nil); err != nil {
			t.Fatalf("tier %v: Paint without: %v", tier, err)
		}
		if a.String() != b.String() {
			t.Errorf("tier %v: caret past the visible end changed the frame:\nwith    %q\nwithout %q",
				tier, a.String(), b.String())
		}
		if tier == TierPlain && !strings.Contains(a.String(), "abc") {
			t.Errorf("plain tier lost the line: %q", a.String())
		}
	}
}

// TestPainterCaretWritesNoCursorPositioning pins the 3B promise that the
// caret costs no cursor-sequence risk: the only CSI sequence that positions
// the cursor anywhere in a caret frame is the frame's pre-existing cursor
// home, and the caret contributes SGR 7 / 27 alone.
func TestPainterCaretWritesNoCursorPositioning(t *testing.T) {
	f := caretFrame()
	f.CaretCell = 4 // end of "hello": the inverted-space path
	var out bytes.Buffer
	if err := NewPainter(&out, Cap{Tier: TierTruecolor, Palette: DefaultPalette()}).Paint(f, []Role{RoleTitle, RoleNone}); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	var positional []string
	for _, seq := range csiSeqs(out.String()) {
		switch seq[len(seq)-1] {
		case 'H', 'f':
			positional = append(positional, seq)
		}
	}
	if len(positional) != 1 || positional[0] != "\x1b[H" {
		t.Fatalf("cursor positioning sequences = %q; want exactly [\"\\x1b[H\"] (the frame's cursor home)", positional)
	}
	seqs := strings.Join(csiSeqs(out.String()), " ")
	for _, want := range []string{"\x1b[7m", "\x1b[27m"} {
		if !strings.Contains(seqs, want) {
			t.Errorf("caret frame is missing %q: %q", want, seqs)
		}
	}
	for _, banned := range []string{"\x1b[K"} {
		if strings.Contains(out.String(), banned) {
			t.Errorf("caret frame emitted %q", banned)
		}
	}
}

// TestPainterCaretRTLScopedOut pins the documented RTL behavior: under
// DirRTL the painter renders no in-line caret, so a caret frame is
// byte-identical to the same frame without one on every tier. The reason is
// in Frame.caretPoint: mirrorLine reverses rune order for box rows, moves the
// cursor marker, swaps the gutter of numbered rows, and reorders "·"
// segments, so one logical cell offset has no single visual cell, and the
// editor promises no bidi cursor either. Nothing is left misplaced because
// no hardware cursor is involved.
//
// The marker-leak guard is two independent assertions, one per frame: the
// byte-identity assertion above covers the pair, and each of these covers a
// single output on its own, so neither can mask the other (finding D).
func TestPainterCaretRTLScopedOut(t *testing.T) {
	with := Frame{Width: 20, Height: 2, Dir: DirRTL, CaretLine: 0, CaretCell: 1,
		Lines: []string{"hello", "body"}}
	without := with
	without.CaretLine, without.CaretCell = -1, -1
	styles := []Role{RoleTitle, RoleNone}
	for _, tier := range []Tier{TierPlain, Tier16, Tier256, TierTruecolor} {
		var a, b bytes.Buffer
		if err := NewPainter(&a, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(with, styles); err != nil {
			t.Fatalf("tier %v: Paint with: %v", tier, err)
		}
		if err := NewPainter(&b, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(without, styles); err != nil {
			t.Fatalf("tier %v: Paint without: %v", tier, err)
		}
		if a.String() != b.String() {
			t.Errorf("tier %v: RTL caret changed the frame:\nwith    %q\nwithout %q", tier, a.String(), b.String())
		}
		if strings.Contains(a.String(), caretMarker) {
			t.Errorf("tier %v: caret marker leaked into the RTL frame with a caret: %q", tier, a.String())
		}
		if strings.Contains(b.String(), caretMarker) {
			t.Errorf("tier %v: caret marker appeared in the caret-less RTL frame: %q", tier, b.String())
		}
	}
}

// TestPainterCaretGuardsTrustedPrefixAndRange pins the two structural
// guards: a caret pointing into the trusted banner prefix is refused (that
// block is our own art and must stay byte-identical), and a caret line
// outside the painted frame is refused instead of panicking.
func TestPainterCaretGuardsTrustedPrefixAndRange(t *testing.T) {
	banner := "\x1b[36mBANNER\x1b[0m"
	pal := DefaultPalette()
	for _, tier := range []Tier{Tier16, TierTruecolor} {
		f := Frame{Width: 20, Height: 3, Dir: DirLTR, Trusted: 1, CaretLine: 0, CaretCell: 2,
			Lines: []string{banner, "body", "tail"}}
		var out bytes.Buffer
		if err := NewPainter(&out, Cap{Tier: tier, Palette: pal}).Paint(f, nil); err != nil {
			t.Fatalf("tier %v: Paint: %v", tier, err)
		}
		if !strings.Contains(out.String(), banner) {
			t.Errorf("tier %v: trusted banner bytes changed: %q", tier, out.String())
		}
		if bytes.Contains(out.Bytes(), []byte("\x1b[7m")) {
			t.Errorf("tier %v: caret drawn into the trusted prefix: %q", tier, out.String())
		}
	}

	// Out-of-range caret line: the frame paints unchanged, no panic.
	flt := Frame{Width: 20, Height: 2, Dir: DirLTR, CaretLine: 9, CaretCell: 2,
		Lines: []string{"one", "two"}}
	var out bytes.Buffer
	if err := NewPainter(&out, Cap{Tier: Tier16, Palette: pal}).Paint(flt, nil); err != nil {
		t.Fatalf("Paint out-of-range: %v", err)
	}
	if want := "\x1b[H\x1b[2Kone\n\x1b[2Ktwo"; out.String() != want {
		t.Errorf("out-of-range caret frame = %q; want %q", out.String(), want)
	}
}

// TestPainterCaretTrustedPrefixNormalized pins finding E: the trusted-prefix
// guard normalizes Frame.Trusted exactly as the paint strip does (both read
// Frame.trustedPrefix), so a hand-built frame with a malformed prefix cannot
// make the caret gate and the painter disagree about which lines are banner
// art. The guard does not weaken: no malformed value moves the caret onto a
// line the painter keeps as trusted banner, and a caret strictly inside the
// normalized prefix is still refused (the shape
// TestPainterCaretGuardsTrustedPrefixAndRange pins for a valid prefix).
func TestPainterCaretTrustedPrefixNormalized(t *testing.T) {
	pal := DefaultPalette()
	lines := []string{"banner", "body", "tail"}
	frame := func(trusted, caretLine int) Frame {
		return Frame{Width: 20, Height: len(lines), Dir: DirLTR, Trusted: trusted,
			CaretLine: caretLine, CaretCell: 0, Lines: append([]string(nil), lines...)}
	}

	// The shared normalization: both malformed directions clamp into the
	// frame's own line range, and a valid prefix passes through.
	for _, tc := range []struct{ trusted, want int }{
		{-9, 0}, {-1, 0}, {0, 0}, {1, 1}, {len(lines), len(lines)},
		{len(lines) + 1, len(lines)}, {99, len(lines)},
	} {
		if got := frame(tc.trusted, 0).trustedPrefix(); got != tc.want {
			t.Errorf("Trusted=%d: trustedPrefix = %d, want %d", tc.trusted, got, tc.want)
		}
	}

	// Both gates read that one answer. wantDraw is the documented rule (drawn
	// iff the line exists and sits at or after the normalized prefix), derived
	// here rather than taken from caretPoint.
	for _, tc := range []struct {
		trusted  int
		caret    int
		wantDraw bool
	}{
		{-1, 0, true},  // clamp 0: line 0 is untrusted, so the caret may sit there
		{-1, 1, true},  // and so is every later line
		{0, 2, true},   // no trusted lines at all
		{1, 0, false},  // strictly inside the prefix
		{2, 1, false},  // strictly inside the prefix
		{1, 1, true},   // at the prefix edge
		{2, 2, true},   // at the prefix edge
		{99, 0, false}, // clamp 3: the prefix covers every line
		{99, 2, false},
		{len(lines) + 5, 1, false},
	} {
		f := frame(tc.trusted, tc.caret)
		l, c := f.caretPoint(len(f.Lines))
		if drawn := l >= 0 && c >= 0; drawn != tc.wantDraw {
			t.Errorf("Trusted=%d caret line %d: caretPoint = (%d,%d), want drawn=%v",
				tc.trusted, tc.caret, l, c, tc.wantDraw)
		}
		var painted, plainOut bytes.Buffer
		if err := NewPainter(&painted, Cap{Tier: Tier256, Palette: pal}).Paint(f, nil); err != nil {
			t.Fatalf("Trusted=%d caret line %d: Paint: %v", tc.trusted, tc.caret, err)
		}
		if got := strings.Contains(painted.String(), caretSGRIn); got != tc.wantDraw {
			t.Errorf("Trusted=%d caret line %d: painted caret SGR present=%v, want %v: %q",
				tc.trusted, tc.caret, got, tc.wantDraw, painted.String())
		}
		if err := NewPainter(&plainOut, plainTestCap()).Paint(f, nil); err != nil {
			t.Fatalf("Trusted=%d caret line %d: plain Paint: %v", tc.trusted, tc.caret, err)
		}
		if got := strings.Contains(plainOut.String(), caretMarker); got != tc.wantDraw {
			t.Errorf("Trusted=%d caret line %d: plain caret marker present=%v, want %v: %q",
				tc.trusted, tc.caret, got, tc.wantDraw, plainOut.String())
		}
	}

	// A malformed value is indistinguishable from its normalized form at both
	// gates, painter output included: the raw field cannot bias the guard even
	// when the strip already keeps every line raw.
	for _, tc := range []struct{ malformed, normalized int }{
		{-1, 0}, {-7, 0}, {len(lines) + 1, len(lines)}, {99, len(lines)},
	} {
		var malformedOut, normalizedOut bytes.Buffer
		if err := NewPainter(&malformedOut, Cap{Tier: Tier256, Palette: pal}).Paint(frame(tc.malformed, 0), nil); err != nil {
			t.Fatalf("Trusted=%d: Paint: %v", tc.malformed, err)
		}
		if err := NewPainter(&normalizedOut, Cap{Tier: Tier256, Palette: pal}).Paint(frame(tc.normalized, 0), nil); err != nil {
			t.Fatalf("Trusted=%d: Paint: %v", tc.normalized, err)
		}
		if malformedOut.String() != normalizedOut.String() {
			t.Errorf("Trusted=%d differs from the normalized Trusted=%d:\nmalformed  %q\nnormalized %q",
				tc.malformed, tc.normalized, malformedOut.String(), normalizedOut.String())
		}
	}

	// The gate decides on the clamped prefix, not on the raw field. Paint always
	// passes n = len(Lines), where the raw and the clamped answers coincide on
	// every real caret line, so this case reports one line past the frame to
	// isolate the rule: a caret line at the clamped prefix edge must be allowed
	// even when the raw prefix would swallow it, while the same caret with the
	// frame's real line count is still refused as out of range.
	edge := frame(len(lines)+6, len(lines))
	if l, c := edge.caretPoint(len(lines) + 1); l != len(lines) || c != 0 {
		t.Fatalf("Trusted=%d: caretPoint(%d) = (%d,%d), want (%d,0): the gate must decide against the clamped prefix %d, not the raw field",
			len(lines)+6, len(lines)+1, l, c, len(lines), len(lines))
	}
	if l, c := edge.caretPoint(len(lines)); l != -1 || c != -1 {
		t.Fatalf("Trusted=%d: caretPoint(%d) = (%d,%d), want (-1,-1): a caret past the painted lines stays refused",
			len(lines)+6, len(lines), l, c)
	}
}

// TestPainterCaretKeepsFrameWidth sweeps caret shapes across every tier and
// pins the width contract the caret must not break: no painted line occupies
// more cells than the frame is wide, no line is added or dropped, and the
// plain tier stays at zero ESC bytes with a caret live.
func TestPainterCaretKeepsFrameWidth(t *testing.T) {
	fixtures := []struct {
		name string
		f    Frame
	}{
		{"mid", Frame{Width: 12, Height: 3, Dir: DirLTR, CaretLine: 1, CaretCell: 1,
			Lines: []string{"title", "abcdef", "tail"}}},
		{"end", Frame{Width: 12, Height: 3, Dir: DirLTR, CaretLine: 1, CaretCell: 6,
			Lines: []string{"title", "abcdef", "tail"}}},
		{"full", Frame{Width: 6, Height: 2, Dir: DirLTR, CaretLine: 0, CaretCell: 6,
			Lines: []string{"abcdef", "tail"}}},
		{"full-mid", Frame{Width: 6, Height: 2, Dir: DirLTR, CaretLine: 0, CaretCell: 3,
			Lines: []string{"abcdef", "tail"}}},
		{"cjk", Frame{Width: 8, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 3,
			Lines: []string{"日本語"}}},
		{"empty", Frame{Width: 5, Height: 1, Dir: DirLTR, CaretLine: 0, CaretCell: 0,
			Lines: []string{""}}},
	}
	for _, fx := range fixtures {
		for _, tier := range []Tier{TierPlain, Tier16, Tier256, TierTruecolor} {
			var out bytes.Buffer
			if err := NewPainter(&out, Cap{Tier: tier, Palette: DefaultPalette()}).Paint(fx.f, nil); err != nil {
				t.Fatalf("%s/%v: Paint: %v", fx.name, tier, err)
			}
			if tier == TierPlain && bytes.ContainsRune(out.Bytes(), 0x1b) {
				t.Fatalf("%s: plain caret frame emitted ESC bytes:\n%q", fx.name, out.String())
			}
			lines := strings.Split(out.String(), "\n")
			if len(lines) != len(fx.f.Lines) {
				t.Fatalf("%s/%v: painted %d lines, want %d (no line added or dropped)",
					fx.name, tier, len(lines), len(fx.f.Lines))
			}
			total := 0
			for i, line := range lines {
				cells := cellwidth.String(visibleText(line))
				total += cells
				if cells > fx.f.Width {
					t.Errorf("%s/%v: line %d occupies %d cells, frame width is %d:\n%q",
						fx.name, tier, i, cells, fx.f.Width, out.String())
				}
			}
			// Guard against a vacuous measurement: when the fixture carries
			// text, the stripper must still see it. (An all-empty fixture is
			// allowed to measure zero cells only through the caret glyph,
			// which markAtCell and invertAtCell are pinned on directly.)
			content := false
			for _, l := range fx.f.Lines {
				if l != "" {
					content = true
				}
			}
			if content && total == 0 {
				t.Fatalf("%s/%v: measured 0 visible cells, the stripper ate the content:\n%q",
					fx.name, tier, out.String())
			}
		}
	}
}
