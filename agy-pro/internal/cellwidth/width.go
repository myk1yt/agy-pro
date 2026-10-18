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

// RTLMin and RTLMax bound the rune range covering RTL scripts (Hebrew,
// Arabic, Syriac, Thaana, NKo, Samaritan, Mandaic, Arabic Extended,
// Hebrew Extended, and Arabic Presentation Forms). Truncate refuses
// mid-string cuts inside this range because naive prefix cuts leave
// logically-ordered RTL text in an unrenderable state.
const (
	RTLMin = rune(0x0590)
	RTLMax = rune(0x08FF)
)

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
// columns. It never separates a base rune from its following combining
// marks: zero-width marks orphaned by the cut are dropped together with
// their dangling position.
//
// If s contains any rune in the RTL-script range U+0590..U+08FF, a
// mid-string cut is refused: Truncate returns the whole string when it
// fits, otherwise the empty string.
func Truncate(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	if String(s) <= cells {
		return s
	}
	for _, r := range s {
		if r >= RTLMin && r <= RTLMax {
			return ""
		}
	}
	runes := []rune(s)
	w := 0
	n := 0
	for _, r := range runes {
		rw := Rune(r)
		if w+rw > cells {
			break
		}
		w += rw
		n++
	}
	// Back up over zero-width marks so no combining mark is orphaned.
	for n > 0 && Rune(runes[n-1]) == 0 {
		n--
	}
	return string(runes[:n])
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
