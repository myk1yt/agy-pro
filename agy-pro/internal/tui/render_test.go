package tui

import (
	"bytes"
	"strings"
	"testing"
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
	f := Frame{Width: 20, Height: 4, Dir: DirLTR, Lines: []string{
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
	f := Frame{Width: 20, Height: 3, Dir: DirLTR, Lines: []string{"Title", "body"}}
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
	f := Frame{Width: 10, Height: 3, Lines: []string{"a", "b", "c"}}
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
	f := Frame{Width: 10, Height: 1, Dir: DirLTR, Lines: []string{"hello"}}
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
	f := Frame{Width: 40, Height: 2, Dir: DirLTR, Lines: []string{
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
	f := Frame{Width: 40, Height: 2, Dir: DirLTR, Lines: []string{
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
	f := Frame{Width: 40, Height: 3, Dir: DirLTR, Lines: []string{
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

	tall := Frame{Width: 20, Height: 6, Dir: DirLTR, Lines: []string{"t1", "t2", "t3", "t4"}}
	short := Frame{Width: 20, Height: 6, Dir: DirLTR, Lines: []string{"s1", "s2"}}

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
	tall := Frame{Width: 20, Height: 6, Dir: DirLTR, Lines: []string{"t1", "t2", "t3", "t4"}}
	short := Frame{Width: 20, Height: 6, Dir: DirLTR, Lines: []string{"s1", "s2"}}
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

// TestPainterStripsBidiOverridePins the round-6 Cf widening at the final
// sink: a disk-borne RTL override cannot reach the terminal (plain tier, so
// the output is pure text), while the Arabic letters and their combining
// mark survive intact.
func TestPainterStripsBidiOverridePins(t *testing.T) {
	f := Frame{Width: 40, Height: 1, Dir: DirLTR, Lines: []string{
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
