package openaiup

import "unicode/utf8"

// SplitPieces splits s into pieces of at most size bytes, cutting only at UTF-8
// rune boundaries: byte-level slicing would split multi-byte runes
// (Korean/CJK/emoji) and json.Marshal would then coerce each half to U+FFFD in
// the emitted deltas. A rune larger than the piece size still gets a whole-rune
// piece instead of a corrupted fragment.
func SplitPieces(s string, size int) []string {
	if s == "" {
		return nil
	}
	if size <= 0 || len(s) <= size {
		return []string{s}
	}
	var out []string
	for len(s) > size {
		cut := size
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// Defensive: a rune larger than the piece size still gets a
			// whole-rune piece instead of a corrupted fragment.
			_, n := utf8.DecodeRuneInString(s)
			if n <= 0 {
				n = 1
			}
			out = append(out, s[:n])
			s = s[n:]
			continue
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
