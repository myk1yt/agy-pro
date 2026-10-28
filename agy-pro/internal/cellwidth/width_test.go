package cellwidth

import (
	"testing"
	"unicode/utf8"
)

func TestRuneWidths(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'Z', 1},
		{'1', 1},
		{' ', 1},
		{'世', 2}, // CJK Unified Ideographs
		{'界', 2},
		{'한', 2},      // Hangul syllable
		{'ｱ', 1},      // halfwidth katakana, H -> 1
		{'Ａ', 2},      // fullwidth A, F
		{'ア', 2},      // katakana, W
		{'·', 1},      // middle dot, Ambiguous -> narrow
		{'°', 1},      // degree sign, Ambiguous -> narrow
		{'─', 1},      // box drawing, Ambiguous -> narrow
		{'\u0301', 0}, // combining acute, Mn
		{'\u200D', 0}, // zero width joiner, Cf
		{'\u200B', 0}, // zero width space, Cf
		{'\u200E', 0}, // left-to-right mark, Cf
		{'\u200F', 0}, // right-to-left mark, Cf
		{'\u00AD', 0}, // soft hyphen, Cf
		{'\u0488', 0}, // combining cyrillic hundred thousands, Me
		{'\u20DD', 0}, // enclosing circle, Me
	}
	for _, c := range cases {
		if got := Rune(c.r); got != c.want {
			t.Errorf("Rune(%q U+%04X) = %d, want %d", c.r, c.r, got, c.want)
		}
	}
}

func TestStringGolden(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"hello", 5},
		{"Ko-fi", 5},
		{"한국어", 6},
		{"你好", 4},
		{"こんにちは", 10},
		{"Tiếng Việt", 10}, // NFC precomposed
		{"مرحبا", 5},
		{"हिन्दी", 5},   // ह+ि(Mc)=2, न+्(Mn)=1, द+ी(Mc)=2
		{"कु", 1},       // base 1 + u-matra (Mn) 0
		{"e\u0301", 1},  // base + combining acute (Mn) 0
		{"a\u200Fb", 2}, // RLM is zero width
		{"a\u200Bb", 2}, // zero width space
		{"\u200F", 0},   // RLM alone
	}
	for _, c := range cases {
		if got := String(c.s); got != c.want {
			t.Errorf("String(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestTruncateASCII(t *testing.T) {
	cases := []struct {
		s     string
		cells int
		want  string
	}{
		{"hello", 0, ""},
		{"hello", -3, ""},
		{"hello", 1, "h"},
		{"hello", 3, "hel"},
		{"hello", 5, "hello"},
		{"hello", 10, "hello"},
	}
	for _, c := range cases {
		if got := Truncate(c.s, c.cells); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.s, c.cells, got, c.want)
		}
	}
}

func TestTruncateCJK(t *testing.T) {
	cases := []struct {
		s     string
		cells int
		want  string
	}{
		{"你好", 1, "…"}, // no source rune fits: visible fallback, not a blank line
		{"你好", 2, "你"},
		{"你好", 3, "你"}, // a wide rune must not be split across the boundary
		{"你好", 4, "你好"},
		{"こんにちは", 7, "こんに"},
	}
	for _, c := range cases {
		if got := Truncate(c.s, c.cells); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.s, c.cells, got, c.want)
		}
	}
}

func TestTruncateNeverOrphansCombiningMark(t *testing.T) {
	cases := []struct {
		s     string
		cells int
		want  string
	}{
		{"e\u0301x", 1, "e\u0301"}, // combining acute stays with its base
		{"e\u0301x", 2, "e\u0301x"},
		{"e\u0301\u0302x", 1, "e\u0301\u0302"}, // stacked marks stay together
		{"café", 4, "café"},                  // precomposed é stays intact
		{"हिन्दी", 1, "…"},                  // the base + spacing mark cluster cannot fit
		{"हिन्दी", 2, "हि"},
	}
	for _, c := range cases {
		if got := Truncate(c.s, c.cells); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.s, c.cells, got, c.want)
		}
	}
}

func TestTruncateKeepsEmojiSequencesWhole(t *testing.T) {
	cases := []struct {
		name string
		s    string
	}{
		{name: "regional-indicator flag pair", s: "🇰🇷"},
		{name: "skin-tone modifier", s: "👋🏽"},
		{name: "ZWJ family", s: "👨‍👩‍👧‍👦"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fullWidth := String(tc.s)
			for cells := 1; cells <= fullWidth; cells++ {
				got := Truncate(tc.s, cells)
				if !utf8.ValidString(got) {
					t.Fatalf("Truncate(%q, %d) returned invalid UTF-8: %q", tc.s, cells, got)
				}
				want := "…"
				if cells == fullWidth {
					want = tc.s
				}
				if got != want {
					t.Errorf("Truncate(%q, %d) = %q, want the whole sequence or the fallback %q", tc.s, cells, got, want)
				}
			}
		})
	}
}

func TestTruncateCutsRTLAtClusterBoundary(t *testing.T) {
	// The old empty-return expectations were the blank-row bug's fossil. RTL
	// text now uses the same prefix and combining-mark boundary as LTR text.
	cases := []struct {
		s     string
		cells int
		want  string
	}{
		{"مرحبا", 5, "مرحبا"}, // fits: whole string
		{"مرحبا", 3, "مرح"},
		{"مرحبا", 0, ""},
		{"مَرحبا", 1, "مَ"}, // the Arabic vowel mark stays with its base
		{"abcمرحبا", 2, "ab"},
		{"abcمرحبا", 8, "abcمرحبا"},
		{"עברית", 4, "עברי"},
		{"עברית", 5, "עברית"},
	}
	for _, c := range cases {
		if got := Truncate(c.s, c.cells); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.s, c.cells, got, c.want)
		}
	}
}

func TestTruncateNeverBlanksNonEmptyOverwideInput(t *testing.T) {
	for _, s := range []string{"hello", "مرحبا", "עברית", "你好", "हिन्दी", "e\u0301x", "مَرحبا"} {
		for cells := 1; cells < String(s); cells++ {
			got := Truncate(s, cells)
			if got == "" {
				t.Errorf("Truncate(%q, %d) returned an empty line", s, cells)
			}
			if width := String(got); width > cells {
				t.Errorf("Truncate(%q, %d) = %q occupying %d cells", s, cells, got, width)
			}
		}
	}
}

func TestPadRight(t *testing.T) {
	cases := []struct {
		s    string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abc", 3, "abc"},
		{"hello", 3, "hello"}, // wider than w: unchanged
		{"你好", 5, "你好 "},
		{"", 2, "  "},
	}
	for _, c := range cases {
		if got := PadRight(c.s, c.w); got != c.want {
			t.Errorf("PadRight(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

func TestPadLeft(t *testing.T) {
	cases := []struct {
		s    string
		w    int
		want string
	}{
		{"abc", 5, "  abc"},
		{"abc", 3, "abc"},
		{"你好", 6, "  你好"},
		{"", 2, "  "},
	}
	for _, c := range cases {
		if got := PadLeft(c.s, c.w); got != c.want {
			t.Errorf("PadLeft(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

func TestCenter(t *testing.T) {
	cases := []struct {
		s    string
		w    int
		want string
	}{
		{"ab", 6, "  ab  "},
		{"ab", 5, " ab  "}, // odd padding: extra space on the right
		{"ab", 2, "ab"},
		{"abc", 2, "abc"}, // wider than w: unchanged
		{"你", 4, " 你 "},
		{"ab你", 8, "  ab你  "}, // mixed ASCII+CJK: width 4, pad 4
		{"", 2, "  "},
	}
	for _, c := range cases {
		if got := Center(c.s, c.w); got != c.want {
			t.Errorf("Center(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

func TestPaddedWidthIsW(t *testing.T) {
	for _, s := range []string{"", "a", "한국어", "مرحبا", "e\u0301", "Tiếng Việt"} {
		for _, w := range []int{String(s), String(s) + 3} {
			if got := String(PadRight(s, w)); got != w {
				t.Errorf("String(PadRight(%q, %d)) = %d", s, w, got)
			}
			if got := String(PadLeft(s, w)); got != w {
				t.Errorf("String(PadLeft(%q, %d)) = %d", s, w, got)
			}
			if got := String(Center(s, w)); got != w {
				t.Errorf("String(Center(%q, %d)) = %d", s, w, got)
			}
		}
	}
}
