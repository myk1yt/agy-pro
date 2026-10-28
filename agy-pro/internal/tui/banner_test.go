// banner_test.go: golden byte tests and structural pins for the banner.
//
// The goldens below are hand-copied from the mission design documents
// (truecolor from the design's literal expansion, 256 from the static
// mapping table, 16 from the hand-authored bands, plain from the ASCII
// figure), NOT generated from the implementation, so a table typo fails
// here.
package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
)

const testTagline = "Gemini workspace, configured once"

func sp(n int) string { return strings.Repeat(" ", n) }

// stripSGR removes CSI SGR sequences so visible cell width can be
// measured on colored lines (cellwidth would count the ASCII digits of
// the escape parameters).
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// needleTruecolorGolden is the full 11-line block at field width 48,
// copied from the design document's truecolor expansion.
func needleTruecolorGolden() []string {
	g := sparkGradient
	line := func(l, runLen int) string {
		left := sp((fieldW - runLen) / 2)
		right := sp(fieldW - (fieldW-runLen)/2 - runLen)
		fg, bg := g[2*l+1], g[2*l]
		return left + "\x1b[38;2;" + itoa(fg.r) + ";" + itoa(fg.g) + ";" + itoa(fg.b) +
			";48;2;" + itoa(bg.r) + ";" + itoa(bg.g) + ";" + itoa(bg.b) + "m" +
			strings.Repeat("▄", runLen) + "\x1b[0m" + right
	}
	wordmark := sp(17) + "\x1b[1;38;2;78;156;255mA\x1b[0m" +
		" \x1b[1;38;2;123;121;251mG\x1b[0m" +
		" \x1b[1;38;2;168;85;247mY\x1b[0m" +
		"   \x1b[1;38;2;207;88;199mP\x1b[0m" +
		" \x1b[1;38;2;226;90;174mR\x1b[0m" +
		" \x1b[1;38;2;255;92;138mO\x1b[0m" + sp(18)
	tag := sp(7) + "\x1b[2;38;2;154;160;166m" + testTagline + "\x1b[0m" + sp(8)
	return []string{
		line(0, 1), line(1, 1), line(2, 3), line(3, 7), line(4, 15),
		line(5, 7), line(6, 3), line(7, 1), line(8, 1),
		wordmark, tag,
	}
}

func TestBannerTruecolorGolden(t *testing.T) {
	got := Render(TierTruecolor, 60, testTagline)
	want := needleTruecolorGolden()
	if len(got) != len(want) {
		t.Fatalf("line count: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestBanner256Golden(t *testing.T) {
	got := Render(Tier256, 60, testTagline)
	if len(got) != 11 {
		t.Fatalf("line count: got %d want 11", len(got))
	}
	// Star lines: fg = spark256[2L+1], bg = spark256[2L], one SGR per line.
	runs := []int{1, 1, 3, 7, 15, 7, 3, 1, 1}
	for l, runLen := range runs {
		fg, bg := spark256[2*l+1], spark256[2*l]
		want := sp((fieldW-runLen)/2) +
			"\x1b[38;5;" + itoa(fg) + ";48;5;" + itoa(bg) + "m" +
			strings.Repeat("▄", runLen) + "\x1b[0m" +
			sp(fieldW-(fieldW-runLen)/2-runLen)
		if got[l] != want {
			t.Errorf("star line %d:\n got %q\nwant %q", l, got[l], want)
		}
	}
	// The static mapping table is pinned row by row (architect table).
	static := [18]int{75, 69, 69, 69, 105, 99, 99, 135, 135, 135, 134, 170, 170, 169, 169, 205, 204, 204}
	if spark256 != static {
		t.Errorf("spark256 drifted: got %v want %v", spark256, static)
	}
	// Wordmark letters: 75, 105, 135, 170, 169, 204.
	wantWord := sp(17) + "\x1b[1;38;5;75mA\x1b[0m" +
		" \x1b[1;38;5;105mG\x1b[0m" +
		" \x1b[1;38;5;135mY\x1b[0m" +
		"   \x1b[1;38;5;170mP\x1b[0m" +
		" \x1b[1;38;5;169mR\x1b[0m" +
		" \x1b[1;38;5;204mO\x1b[0m" + sp(18)
	if got[9] != wantWord {
		t.Errorf("wordmark:\n got %q\nwant %q", got[9], wantWord)
	}
	wantTag := sp(7) + "\x1b[2;38;5;247m" + testTagline + "\x1b[0m" + sp(8)
	if got[10] != wantTag {
		t.Errorf("tagline:\n got %q\nwant %q", got[10], wantTag)
	}
}

func TestBanner16Golden(t *testing.T) {
	got := Render(Tier16, 60, testTagline)
	if len(got) != 11 {
		t.Fatalf("line count: got %d want 11", len(got))
	}
	// Full cells U+2588, fg only, fg = spark16[2L+1]; silhouette bands.
	runs := []int{1, 1, 3, 7, 15, 7, 3, 1, 1}
	for l, runLen := range runs {
		fg := spark16[2*l+1]
		want := sp((fieldW-runLen)/2) + "\x1b[" + itoa(fg) + "m" +
			strings.Repeat("█", runLen) + "\x1b[0m" +
			sp(fieldW-(fieldW-runLen)/2-runLen)
		if got[l] != want {
			t.Errorf("star line %d:\n got %q\nwant %q", l, got[l], want)
		}
	}
	static := [18]int{94, 94, 94, 94, 95, 95, 95, 95, 95, 35, 35, 35, 35, 35, 91, 91, 91, 91}
	if spark16 != static {
		t.Errorf("spark16 drifted: got %v want %v", spark16, static)
	}
	wantWord := sp(17) + "\x1b[94mA\x1b[0m \x1b[95mG\x1b[0m \x1b[95mY\x1b[0m" +
		"   \x1b[35mP\x1b[0m \x1b[91mR\x1b[0m \x1b[91mO\x1b[0m" + sp(18)
	if got[9] != wantWord {
		t.Errorf("wordmark:\n got %q\nwant %q", got[9], wantWord)
	}
	wantTag := sp(7) + "\x1b[2;90m" + testTagline + "\x1b[0m" + sp(8)
	if got[10] != wantTag {
		t.Errorf("tagline:\n got %q\nwant %q", got[10], wantTag)
	}
}

func TestBannerPlainGolden(t *testing.T) {
	got := Render(TierPlain, 60, testTagline)
	want := []string{
		cellwidth.Center("        *", fieldW),
		cellwidth.Center("       /|\\", fieldW),
		cellwidth.Center("      / | \\", fieldW),
		cellwidth.Center("  *---< + >---*", fieldW),
		cellwidth.Center("      \\ | /", fieldW),
		cellwidth.Center("       \\|/", fieldW),
		cellwidth.Center("        *", fieldW),
		cellwidth.Center("A G Y   P R O", fieldW),
		cellwidth.Center(testTagline, fieldW),
	}
	if len(got) != len(want) {
		t.Fatalf("line count: got %d want %d\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	// Structural pin: zero escape bytes in plain-tier output.
	for _, line := range got {
		if bytes.Contains([]byte(line), []byte{0x1b}) {
			t.Errorf("plain tier contains ESC: %q", line)
		}
	}
}

func TestBannerSizingBoundaries(t *testing.T) {
	// cols >= 52: full 11-line block.
	full := Render(TierTruecolor, 52, testTagline)
	if len(full) != 11 {
		t.Fatalf("cols=52: got %d lines want 11", len(full))
	}
	// 32..51: spark only, 9 lines, field padding dropped, total width = cols.
	for _, cols := range []int{32, 40, 51} {
		lines := Render(TierTruecolor, cols, testTagline)
		if len(lines) != 9 {
			t.Fatalf("cols=%d: got %d lines want 9", cols, len(lines))
		}
		for _, line := range lines {
			if w := cellwidth.String(stripSGR(line)); w != cols {
				t.Errorf("cols=%d: line width %d", cols, w)
			}
		}
	}
	// < 32: ASCII figure, no escapes.
	lines := Render(TierTruecolor, 31, testTagline)
	if len(lines) != 9 {
		t.Fatalf("cols=31: got %d lines want 9 (ASCII figure)", len(lines))
	}
	for _, line := range lines {
		if bytes.Contains([]byte(line), []byte{0x1b}) {
			t.Errorf("cols=31 contains ESC: %q", line)
		}
	}
}

func TestBannerCJKTaglineCentering(t *testing.T) {
	cjk := "한국어 태그라인"
	// Plain tier: the tagline line is the last line, centered via cellwidth.
	got := Render(TierPlain, 40, cjk)
	last := got[len(got)-1]
	if want := cellwidth.Center(cjk, 40); last != want {
		t.Errorf("plain CJK tagline:\n got %q\nwant %q", last, want)
	}
	// Truecolor tier: 16 left + dim SGR run + 17 right (tagline is 15
	// cells wide: 7 CJK runes plus one ASCII space) in field 48.
	got = Render(TierTruecolor, 60, cjk)
	last = got[len(got)-1]
	want := sp(16) + "\x1b[2;38;2;154;160;166m" + cjk + "\x1b[0m" + sp(17)
	if last != want {
		t.Errorf("truecolor CJK tagline:\n got %q\nwant %q", last, want)
	}
	// A CJK tagline must not be centered by rune count (that would pad
	// 8 runes as if they were 8 cells, 8 short on the left).
	left := len(last) - len(strings.TrimLeft(last, " "))
	if left != 16 {
		t.Errorf("CJK tagline left pad %d want 16 (rune-count centering would give 8)", left)
	}
}

func TestBannerNoUpperHalfBlockInSource(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(src, []byte("\u2580")) { // U+2580 UPPER HALF BLOCK
			t.Errorf("%s contains U+2580 (banned: legacy CJK console fonts render it hollow)", e.Name())
		}
	}
}

func TestBannerCompactAndWordmark(t *testing.T) {
	c := RenderCompact(TierTruecolor, 30, testTagline)
	if len(c) != 3 {
		t.Fatalf("compact: got %d lines want 3", len(c))
	}
	if !strings.Contains(c[2], "\x1b[38;2;255;92;138m") {
		t.Errorf("compact rule does not reach R17: %q", c[2])
	}
	w := RenderWordmark(Tier256, 20)
	if len(w) != 1 || !strings.Contains(w[0], "38;5;75mA") {
		t.Errorf("wordmark line: %q", w)
	}
	// Compact at a large console caps the field at 48.
	c = RenderCompact(Tier16, 120, "")
	for _, line := range c {
		if got := cellwidth.String(stripSGR(line)); got != 48 {
			t.Errorf("compact line width %d want 48: %q", got, line)
		}
	}
}

func TestBannerWingVariant(t *testing.T) {
	defer func() { SparkVariant = 0 }()
	SparkVariant = 1
	got := Render(TierTruecolor, 60, testTagline)
	if len(got) != 10 { // 8 star lines + wordmark + tagline
		t.Fatalf("wing: got %d lines want 10", len(got))
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "\x1b[38;2;224;236;255") && !strings.Contains(joined, ";48;2;224;236;255") {
		t.Error("wing core color missing")
	}
	// Half-empty cells render with SGR 39/49 (terminal default), never U+2580.
	if strings.Contains(joined, "\u2580") {
		t.Error("wing contains U+2580")
	}
	// 256 tier uses the wing mapping (core 255, blue 111, ...).
	got256 := Render(Tier256, 60, "")
	j256 := strings.Join(got256, "\n")
	if !strings.Contains(j256, "38;5;255") && !strings.Contains(j256, "48;5;255") {
		t.Error("wing 256 core mapping missing")
	}
}

// TestBannerWingNarrowGeometryNoPanic is the regression pin for the latent
// banner.Render panic code seat 3 flagged: the wing box is 36 cells wide, so
// at cols 32..35 (the 32..51 spark-only ladder step) the centering computed
// a negative left pad and strings.Repeat panicked. The fix substitutes the
// ASCII figure whenever the active geometry is wider than the frame
// (truncating through an escape sequence would corrupt the SGR), so this
// test also pins the chosen variant per width band.
func TestBannerWingNarrowGeometryNoPanic(t *testing.T) {
	defer func() { SparkVariant = 0 }()
	SparkVariant = 1
	for cols := 32; cols <= 51; cols++ {
		lines := Render(TierTruecolor, cols, testTagline) // must not panic
		joined := strings.Join(lines, "\n")
		switch {
		case cols <= 35:
			// 36-cell wing does not fit: the ASCII figure substitutes,
			// byte-identical to the documented fallback and zero escapes.
			want := asciiFigure(cols, testTagline)
			if strings.Join(lines, "\n") != strings.Join(want, "\n") {
				t.Errorf("cols=%d: got %q, want ASCII figure %q", cols, lines, want)
			}
			for _, l := range lines {
				if strings.Contains(l, "\x1b") {
					t.Errorf("cols=%d: ASCII fallback carries escapes: %q", cols, l)
				}
			}
		default:
			// Spark fits: 8 art lines, gradient escapes intact, exact
			// frame width after SGR removal.
			if len(lines) != 8 {
				t.Errorf("cols=%d: got %d lines want 8", cols, len(lines))
			}
			if !strings.Contains(joined, "\x1b[38;2;") {
				t.Errorf("cols=%d: wing spark lost its truecolor escapes:\n%q", cols, joined)
			}
			for _, l := range lines {
				if w := cellwidth.String(stripSGR(l)); w != cols {
					t.Errorf("cols=%d: line width %d want %d: %q", cols, w, cols, l)
				}
			}
		}
	}
}
