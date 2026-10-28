package cellwidth

import (
	"strings"
	"testing"
)

func TestP20ZWJDoesNotSplitFollowingRegionalIndicatorPair(t *testing.T) {
	const input = "x\u200d🇩🇪"
	for cells := 1; cells <= String(input); cells++ {
		got := Truncate(input, cells)
		if strings.Count(got, "🇩")+strings.Count(got, "🇪") == 1 {
			t.Fatalf("Truncate(%q, %d) = %q contains half a flag", input, cells, got)
		}
	}
	if got := Truncate(input, 2); got != "x" {
		t.Fatalf("Truncate(%q, 2) = %q, want %q", input, got, "x")
	}
	t.Logf("Truncate(%q, 2)=%q", input, Truncate(input, 2))
}
