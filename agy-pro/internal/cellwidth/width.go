// Package cellwidth computes terminal display-cell widths for UTF-8 text.
//
// Width semantics (tables generated from Unicode 15.1.0, see tables.go):
//   - EastAsianWidth W or F            -> 2 cells
//   - General_Category Mn, Me, or Cf   -> 0 cells (combining marks, RLM/LRM,
//     zero-width space, and other format controls)
//   - everything else, including EastAsianWidth A (Ambiguous) -> 1 cell
//
// Callers doing layout must use this package instead of len() or rune count.
package cellwidth

import "unicode"

// Rune returns the display cell width of r.
func Rune(r rune) int {
	if inRanges(zeroRanges[:], r) {
		return 0
	}
	if inRanges(wideRanges[:], r) {
		return 2
	}
	return 1
}

// String returns the total display cell width of s.
func String(s string) int {
	w := 0
	for _, r := range s {
		w += Rune(r)
	}
	return w
}

// Truncate returns the longest prefix of s that fits in cells display
// columns. A base rune and its following Unicode marks form one cluster, as do
// a regional-indicator pair, an emoji base and its skin-tone modifiers, and a
// ZWJ-joined sequence. The cut therefore never leaves a base without its
// marks, half a flag, an orphan modifier, or a broken ZWJ chain. Dangling
// trailing format controls are dropped. The same cluster-boundary rule applies
// to RTL text, so an over-wide Arabic or Hebrew line keeps a readable
// logical-order prefix instead of disappearing.
//
// When no complete cluster fits a positive width, Truncate returns the
// one-cell fallback marker "…" rather than blanking non-empty content. A
// non-positive width still returns the empty string.
func Truncate(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	if String(s) <= cells {
		return s
	}
	runes := []rune(s)
	w, n := 0, 0
	hasBase := false
	for i := 0; i < len(runes); {
		var clusterWidth int
		i, clusterWidth = nextCluster(runes, i)
		if w+clusterWidth > cells {
			break
		}
		w += clusterWidth
		n = i
		hasBase = hasBase || clusterWidth > 0
	}
	for n > 0 && unicode.Is(unicode.Cf, runes[n-1]) {
		n--
	}
	if n == 0 || !hasBase {
		return "…"
	}
	return string(runes[:n])
}

func nextCluster(runes []rune, start int) (end, width int) {
	i := start + 1
	width = Rune(runes[start])
	if isRegionalIndicator(runes[start]) && i < len(runes) && isRegionalIndicator(runes[i]) {
		width += Rune(runes[i])
		i++
	}
	for i < len(runes) {
		if runes[i] == '\u200d' {
			if i+1 >= len(runes) || isRegionalIndicator(runes[i+1]) {
				break
			}
			width += Rune(runes[i+1])
			i += 2
			continue
		}
		if !clusterExtension(runes[i]) && !isEmojiModifier(runes[i]) {
			break
		}
		width += Rune(runes[i])
		i++
	}
	return i, width
}

func clusterExtension(r rune) bool {
	return Rune(r) == 0 || unicode.Is(unicode.M, r)
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1f1e6 && r <= 0x1f1ff
}

func isEmojiModifier(r rune) bool {
	return r >= 0x1f3fb && r <= 0x1f3ff
}

// PadRight appends spaces so the result occupies exactly w cells. Strings
// already w cells or wider are returned unchanged.
func PadRight(s string, w int) string {
	if n := String(s); n < w {
		return s + spaces(w-n)
	}
	return s
}

// PadLeft prepends spaces so the result occupies exactly w cells. Strings
// already w cells or wider are returned unchanged.
func PadLeft(s string, w int) string {
	if n := String(s); n < w {
		return spaces(w-n) + s
	}
	return s
}

// Center pads s with spaces on both sides so the result occupies exactly
// w cells; when the padding is odd, the extra space goes on the right.
// Strings already w cells or wider are returned unchanged.
func Center(s string, w int) string {
	n := String(s)
	if n >= w {
		return s
	}
	total := w - n
	left := total / 2
	return spaces(left) + s + spaces(total-left)
}

func spaces(n int) string {
	const unit = "                                " // 32 spaces
	b := make([]byte, 0, n)
	for n > 0 {
		m := n
		if m > len(unit) {
			m = len(unit)
		}
		b = append(b, unit[:m]...)
		n -= m
	}
	return string(b)
}

// inRanges reports whether r falls inside any [lo, hi] span of the sorted,
// non-overlapping table t, using binary search.
func inRanges(t [][2]rune, r rune) bool {
	lo, hi := 0, len(t)-1
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		switch {
		case r < t[mid][0]:
			hi = mid - 1
		case r > t[mid][1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}
