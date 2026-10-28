package openaiup

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSplitPieces_RuneBoundaries is the F-01 pin moved here from the bridge:
// byte-level slicing would split multi-byte runes and json.Marshal would coerce
// each half to U+FFFD.
func TestSplitPieces_RuneBoundaries(t *testing.T) {
	cases := []struct {
		name string
		s    string
		size int
	}{
		{"hangul small budget", strings.Repeat("한", 10), 4},
		{"mixed ascii hangul", strings.Repeat("a가", 1500), 2048},
		{"emoji 4-byte runes", strings.Repeat("😀", 5), 4},
		{"empty", "", 4},
		{"shorter than budget", "abc", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pieces := SplitPieces(tc.s, tc.size)
			if strings.Join(pieces, "") != tc.s {
				t.Errorf("reconstruction lost data (input %d bytes)", len(tc.s))
			}
			for _, p := range pieces {
				if !utf8.ValidString(p) {
					t.Errorf("piece %q is not valid UTF-8", p)
				}
				if tc.size > 0 && len(p) > tc.size {
					t.Errorf("piece length %d exceeds size %d", len(p), tc.size)
				}
			}
		})
	}

	// The exact F-01 shape: 3-byte runes against a 2048-byte budget.
	s := strings.Repeat("한글", 700) // 4200 bytes
	pieces := SplitPieces(s, 2048)
	if len(pieces) < 2 {
		t.Fatalf("expected multiple pieces, got %d", len(pieces))
	}
	if strings.Join(pieces, "") != s {
		t.Error("2048-budget reconstruction lost data")
	}
	for _, p := range pieces {
		if !utf8.ValidString(p) {
			t.Errorf("2048-budget piece corrupted: %q", p)
		}
	}
}

// TestSplitPieces_RuneSafetyAtEveryByteOffset walks every budget from 1 byte to
// the full corpus length over a mixed CJK/emoji corpus. Every budget must yield
// pieces that rejoin to the original with no U+FFFD anywhere, which is what the
// bridge's synthesized SSE stream and the adapter's Gemini emitter both rely on.
func TestSplitPieces_RuneSafetyAtEveryByteOffset(t *testing.T) {
	corpus := strings.Repeat("a한글😀漢字b가나다🎉", 8)
	if !utf8.ValidString(corpus) {
		t.Fatal("corpus is not valid UTF-8")
	}
	for size := 1; size <= len(corpus); size++ {
		pieces := SplitPieces(corpus, size)
		if joined := strings.Join(pieces, ""); joined != corpus {
			t.Fatalf("size %d: rejoin lost data (%d bytes vs %d)", size, len(joined), len(corpus))
		}
		for _, p := range pieces {
			if !utf8.ValidString(p) {
				t.Fatalf("size %d: piece %q is not valid UTF-8", size, p)
			}
			if strings.ContainsRune(p, utf8.RuneError) {
				t.Fatalf("size %d: piece contains U+FFFD", size)
			}
			if len(p) > size && utf8.RuneCountInString(p) != 1 {
				t.Fatalf("size %d: piece %q exceeds the budget without being a single wide rune", size, p)
			}
		}
	}
}

// A rune wider than the whole budget still gets a whole-rune piece rather than a
// corrupted fragment.
func TestSplitPieces_RuneLargerThanBudget(t *testing.T) {
	pieces := SplitPieces("😀😀", 1)
	if len(pieces) != 2 {
		t.Fatalf("pieces = %d; want one whole rune per piece", len(pieces))
	}
	for _, p := range pieces {
		if !utf8.ValidString(p) || len(p) != 4 {
			t.Errorf("piece %q; want a whole 4-byte rune", p)
		}
	}
	if strings.Join(pieces, "") != "😀😀" {
		t.Error("rejoin lost data")
	}
}
