package tui

import (
	"fmt"
	"testing"
)

func rows(n int) []Row {
	out := make([]Row, n)
	for i := range out {
		out[i] = Row{Label: fmt.Sprintf("Row %d", i+1), ID: fmt.Sprintf("id-%d", i)}
	}
	return out
}

// TestListDualNavigation is the structural pin of the two-handled
// guarantee: a scripted digit press and a scripted arrow+Enter walk both
// activate the same row.
func TestListDualNavigation(t *testing.T) {
	var digitPicked, arrowPicked []int
	digitList := newStepList(func(i int) { digitPicked = append(digitPicked, i) })
	digitList.SetRows(rows(5))
	if !digitList.HandleKey(Digit(3)) {
		t.Fatal("digit key not consumed by the list")
	}

	arrowList := newStepList(func(i int) { arrowPicked = append(arrowPicked, i) })
	arrowList.SetRows(rows(5))
	for _, k := range []Key{KeyDown, KeyDown, KeyEnter} {
		if !arrowList.HandleKey(k) {
			t.Fatalf("key %q not consumed by the list", k)
		}
	}

	if fmt.Sprint(digitPicked) != "[2]" || fmt.Sprint(arrowPicked) != "[2]" {
		t.Fatalf("digit picked %v, arrows picked %v; want both [2]", digitPicked, arrowPicked)
	}
	if digitList.Cursor() != 2 || arrowList.Cursor() != 2 {
		t.Fatalf("digit cursor %d, arrow cursor %d; want both 2", digitList.Cursor(), arrowList.Cursor())
	}
}

// TestListTwelveRowsArrowReachable pins that a 12-row list (beyond the
// 1..9 digit range) is fully reachable by arrows, with Enter activating
// the last row.
func TestListTwelveRowsArrowReachable(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(12))
	for i := 0; i < 11; i++ {
		if !l.HandleKey(KeyDown) {
			t.Fatalf("Down %d not consumed", i+1)
		}
	}
	if l.Cursor() != 11 {
		t.Fatalf("cursor after 11 Downs = %d, want 11", l.Cursor())
	}
	l.HandleKey(KeyEnter)
	if fmt.Sprint(picked) != "[11]" {
		t.Fatalf("picked %v, want [11]", picked)
	}
	// Digit 1 on a paged list still selects the absolute row 1.
	l.HandleKey(Digit(1))
	if fmt.Sprint(picked) != "[11 0]" || l.Cursor() != 0 {
		t.Fatalf("absolute digit pick failed: picked %v cursor %d", picked, l.Cursor())
	}
}

func TestListPagingAndJumps(t *testing.T) {
	l := newStepList(nil)
	l.SetRows(rows(25))

	l.HandleKey(KeyPgDn)
	if l.Cursor() != 10 {
		t.Fatalf("PgDn cursor = %d, want 10", l.Cursor())
	}
	l.HandleKey(KeyPgDn)
	if l.Cursor() != 20 {
		t.Fatalf("second PgDn cursor = %d, want 20", l.Cursor())
	}
	l.HandleKey(KeyPgDn)
	if l.Cursor() != 24 {
		t.Fatalf("PgDn clamp = %d, want 24", l.Cursor())
	}
	l.HandleKey(KeyPgUp)
	if l.Cursor() != 14 {
		t.Fatalf("PgUp cursor = %d, want 14", l.Cursor())
	}
	l.HandleKey(KeyHome)
	if l.Cursor() != 0 || l.Offset() != 0 {
		t.Fatalf("Home cursor/offset = %d/%d, want 0/0", l.Cursor(), l.Offset())
	}
	l.HandleKey(KeyEnd)
	if l.Cursor() != 24 {
		t.Fatalf("End cursor = %d, want 24", l.Cursor())
	}
}

func TestListWindowFollowsCursor(t *testing.T) {
	l := newStepList(nil)
	l.SetRows(rows(12))
	l.SetPageSize(10)

	if got := len(l.Render(80)); got != 10 {
		t.Fatalf("first window renders %d rows, want 10", got)
	}
	l.HandleKey(KeyEnd) // cursor 11 -> window slides to rows 2..11
	if l.Offset() != 2 {
		t.Fatalf("offset after End = %d, want 2", l.Offset())
	}
	visible := l.Render(80)
	if len(visible) != 10 {
		t.Fatalf("slid window renders %d rows, want 10", len(visible))
	}
	// First visible row is absolute row 3 (numbered [ 3]); "Row 3" is
	// padded to the 6-cell label column ("Row 10" is the widest label).
	want := "  [ 3] Row 3 "
	if visible[0] != want {
		t.Fatalf("first visible row = %q, want %q", visible[0], want)
	}
	if l.VisibleEnd() != 12 {
		t.Fatalf("visible end = %d, want 12", l.VisibleEnd())
	}
}

func TestListDigitOutOfRangeIgnored(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(3))
	l.HandleKey(Digit(9)) // beyond the row count: consumed, no activation
	if len(picked) != 0 {
		t.Fatalf("picked %v, want none", picked)
	}
	if l.HandleKey(Key("x")) {
		t.Fatal("non-list key should be reported unconsumed (fall-through)")
	}
	if l.HandleKey(KeyEsc) {
		t.Fatal("Esc is not a list key; the App dispatcher owns it")
	}
}

func TestListFooterHint(t *testing.T) {
	short := newStepList(nil)
	short.SetRows(rows(3))
	if got, want := short.FooterHint(), "digits 1-9 select+activate · arrows move · Enter activates"; got != want {
		t.Fatalf("short footer = %q, want %q", got, want)
	}
	long := newStepList(nil)
	long.SetRows(rows(12))
	if got, want := long.FooterHint(), "digits 1-9 select+activate · rows past 9: arrows + Enter"; got != want {
		t.Fatalf("long footer = %q, want %q", got, want)
	}
}

func TestListRenderCJKPadding(t *testing.T) {
	l := newStepList(nil)
	l.SetRows([]Row{{Label: "언어", Note: "ko"}, {Label: "Language", Note: "en"}})
	got := l.Render(40)
	// Labels padded by display cells: "언어" (4 cells) + 6 spaces aligns
	// with "Language" (8 cells); the CJK row must not throw the note off.
	want0 := "▶ [ 1] 언어      ko"
	want1 := "  [ 2] Language  en"
	if got[0] != want0 {
		t.Fatalf("row 0 = %q, want %q", got[0], want0)
	}
	if got[1] != want1 {
		t.Fatalf("row 1 = %q, want %q", got[1], want1)
	}
}
