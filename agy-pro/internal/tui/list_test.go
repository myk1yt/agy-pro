package tui

import (
	"fmt"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/providers"
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
	if got, want := long.FooterHint(), "digits 1-9 select+activate · rows past 9: 0 then digits · arrows + Enter"; got != want {
		t.Fatalf("long footer = %q, want %q", got, want)
	}
}

// TestListMultiDigitCompose pins the 0-prefix path that reaches rows past
// the single-digit range: 0 arms, digits extend, Enter (or an unextendable
// number) commits with the same select+activate semantics as a bare digit.
func TestListMultiDigitCompose(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(14))

	if !l.HandleKey(Key("0")) {
		t.Fatal("0 on a 14-row list not consumed (compose arm)")
	}
	if l.HandleKey(Digit(1)) != true {
		t.Fatal("digit inside compose not consumed")
	}
	if l.HandleKey(Digit(3)) != true {
		t.Fatal("digit inside compose not consumed")
	}
	if fmt.Sprint(picked) != "[12]" || l.Cursor() != 12 {
		t.Fatalf("0,1,3 picked %v cursor %d; want [12], 12", picked, l.Cursor())
	}
}

// TestListMultiDigitEnterCommit pins that a pending prefix commits on
// Enter, so row 1 on a long list is still one key once the user arms 0.
func TestListMultiDigitEnterCommit(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(14))

	l.HandleKey(Key("0"))
	l.HandleKey(Digit(1))
	l.HandleKey(KeyEnter)
	if fmt.Sprint(picked) != "[0]" || l.Cursor() != 0 {
		t.Fatalf("0,1,Enter picked %v cursor %d; want [0], 0", picked, l.Cursor())
	}
}

// TestListMultiDigitImmediateCommit pins that the compose commits as soon
// as the number cannot extend into a valid row: 0,9 on a 14-row list picks
// row 9 without waiting for Enter.
func TestListMultiDigitImmediateCommit(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(14))

	l.HandleKey(Key("0"))
	l.HandleKey(Digit(9))
	if fmt.Sprint(picked) != "[8]" || l.Cursor() != 8 {
		t.Fatalf("0,9 picked %v cursor %d; want [8], 8", picked, l.Cursor())
	}
	// A digit after the commit is an ordinary instant pick again.
	l.HandleKey(Digit(2))
	if fmt.Sprint(picked) != "[8 1]" || l.Cursor() != 1 {
		t.Fatalf("post-compose digit picked %v cursor %d; want [8 1], 1", picked, l.Cursor())
	}
}

// TestListMultiDigitOutOfRangeSwallowed pins that a number past the row
// count cancels the compose and activates nothing, and that the list is
// back in ordinary digit mode afterwards.
func TestListMultiDigitOutOfRangeSwallowed(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(14))

	l.HandleKey(Key("0"))
	l.HandleKey(Digit(1)) // pending 1: 10-14 still reachable
	l.HandleKey(Digit(5)) // 15 > 14: swallowed, compose cancelled
	if len(picked) != 0 {
		t.Fatalf("0,1,5 picked %v, want none", picked)
	}
	l.HandleKey(Digit(3))
	if fmt.Sprint(picked) != "[2]" {
		t.Fatalf("digit after cancelled compose picked %v, want [2]", picked)
	}
}

// TestListMultiDigitCancelledByNavigation pins that a non-digit key cancels
// the compose and then acts normally, without activating the pending row.
func TestListMultiDigitCancelledByNavigation(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(14))

	l.HandleKey(Key("0"))
	l.HandleKey(Digit(1)) // pending 1, could extend to 10-14
	l.HandleKey(KeyDown)  // cancels compose, moves the cursor
	if len(picked) != 0 || l.Cursor() != 1 {
		t.Fatalf("Down inside compose picked %v cursor %d; want none, 1", picked, l.Cursor())
	}
}

// TestListZeroFallsThroughOnShortLists pins that 0 keeps its old
// fall-through on lists with no rows past 9.
func TestListZeroFallsThroughOnShortLists(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(5))
	if l.HandleKey(Key("0")) {
		t.Fatal("0 on a 5-row list must stay unconsumed (fall-through)")
	}
	if len(picked) != 0 {
		t.Fatalf("picked %v, want none", picked)
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

// longListFooter is the paged legend every >9-row list shows when no compose
// is armed. It doubles as the "compose finished" evidence: a stale prefix
// would keep the live-prefix form on the footer line.
const longListFooter = "digits 1-9 select+activate · rows past 9: 0 then digits · arrows + Enter"

// TestListComposeCommitIsTheSameDoorAsEnter is the FIX-CODE-C5 verification
// pin. The reviewed concern was that the 0-prefix compose extends the
// Danger-row instant-arm reach past single digits. Reading the activation
// paths shows the list owns exactly ONE door: every input language moves the
// cursor and calls pick, and the danger gate lives in the screen's pick
// handler (settingsScreen.activate for the settings rows,
// ProvidersScreen.confirmDelete for the delete verb), so a composed row meets
// the same handler as arrow+Enter. This test holds that down by driving the
// three reachable ways to absolute row 10 and asserting they are
// indistinguishable from the list's side: one pick call, same index, same
// cursor, no leftover compose state.
func TestListComposeCommitIsTheSameDoorAsEnter(t *testing.T) {
	arrowKeys := make([]Key, 0, 10)
	for i := 0; i < 9; i++ {
		arrowKeys = append(arrowKeys, KeyDown)
	}
	arrowKeys = append(arrowKeys, KeyEnter)

	cases := []struct {
		name string
		rows int
		keys []Key
	}{
		// 12 rows: 10 is past the extendable range, so the compose commits on
		// the digit that cannot extend (0,1,0 -> 10).
		{"arrow+Enter", 12, arrowKeys},
		{"compose digits reaching row 10", 12, []Key{Key("0"), Digit(1), Key("0")}},
		// 105 rows: 10 can still extend, so Enter commits the pending prefix.
		{"compose Enter commit at row 10", 105, []Key{Key("0"), Digit(1), Key("0"), KeyEnter}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var picked []int
			l := newStepList(func(i int) { picked = append(picked, i) })
			l.SetRows(rows(tc.rows))
			for _, k := range tc.keys {
				if !l.HandleKey(k) {
					t.Fatalf("key %q not consumed", k)
				}
			}
			if fmt.Sprint(picked) != "[9]" {
				t.Fatalf("picked %v, want exactly [9] (row 10, one activation)", picked)
			}
			if l.Cursor() != 9 {
				t.Fatalf("cursor = %d, want 9", l.Cursor())
			}
			if l.compose || l.pending != 0 {
				t.Fatalf("compose state left behind: compose=%v pending=%d", l.compose, l.pending)
			}
			if got := l.FooterHint(); got != longListFooter {
				t.Fatalf("footer after activation = %q, want the resting legend", got)
			}
		})
	}
}

// fiveProviderRegistry builds a registry whose row set on the providers screen
// is 10 rows (5 provider rows + Add + Test all + Test + Edit + Delete), which
// puts the danger Delete verb at row 10: the first row only the 0-prefix
// compose can reach.
func fiveProviderRegistry() *providers.Registry {
	reg := &providers.Registry{}
	for i := 1; i <= 5; i++ {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name:      fmt.Sprintf("provider-%d", i),
			BaseURL:   "https://example.com",
			APIKeyEnv: fmt.Sprintf("PROVIDER_%d_KEY", i),
			Models:    []providers.Model{{ID: "model-1"}},
		})
	}
	return reg
}

// TestListComposedDangerDeleteStopsAtTheGate is the end-to-end half of the
// same pin: the providers screen's Delete verb is its danger row, and with
// five providers the row set is 10 rows, so Delete sits at row 10, the first
// row only the compose can reach. Composing it must open the M4 approval gate
// with nothing written, exactly as a digit on a lower danger row does; the
// registry write may only happen after the answer.
func TestListComposedDangerDeleteStopsAtTheGate(t *testing.T) {
	providersFixture(t, fiveProviderRegistry())
	saves := 0
	var opened []string
	app := newProvidersApp(providersRecordingCore(&saves), &opened)

	if got := app.Top().(*ProvidersScreen).list.Len(); got != 10 {
		t.Fatalf("providers row set = %d rows, want 10 (5 providers + Add + 4 verbs)", got)
	}
	for _, k := range []Key{Key("0"), Digit(1), Key("0")} {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("composed row 10 (Delete) activated with no confirmation gate")
	}
	if _, ok := m.(*Approval); !ok {
		t.Fatalf("gate = %T, want the M4 approval modal", m)
	}
	if saves != 0 {
		t.Fatalf("the composed Delete wrote %d time(s) before the answer", saves)
	}
	if len(opened) != 0 {
		t.Fatalf("the composed row opened a provider screen: %v", opened)
	}
	app.Step(KeyYes)
	if saves != 1 {
		t.Fatalf("approved composed Delete wrote %d time(s), want 1", saves)
	}
}

// TestListComposePrefixIsVisibleOnARealScreen is the frame-level half of the
// F-09 feedback fix: mid-compose on the providers screen the pending prefix is
// on the rendered frame, not only in the model.
func TestListComposePrefixIsVisibleOnARealScreen(t *testing.T) {
	providersFixture(t, fiveProviderRegistry())
	saves := 0
	var opened []string
	app := newProvidersApp(providersRecordingCore(&saves), &opened)

	app.Step(Key("0"))
	app.Step(Digit(1)) // pending row 1, still extendable
	joined := strings.Join(app.Frame().Lines, "\n")
	if !strings.Contains(joined, "composing 0·1_") {
		t.Fatalf("a live compose is invisible on the frame:\n%s", joined)
	}
	if strings.Contains(joined, longListFooter) {
		t.Fatalf("the resting legend is still drawn while composing:\n%s", joined)
	}
}

// TestListEnterOnBareComposeActivatesCursorRow pins the F-09 half of the
// review: Enter on a 0 with no digit typed yet has no row number to commit,
// so it cancels the prefix and keeps Enter's ordinary meaning (activate the
// cursor row) instead of swallowing the keystroke. Recovering from an
// accidental 0 therefore costs nothing.
func TestListEnterOnBareComposeActivatesCursorRow(t *testing.T) {
	var picked []int
	l := newStepList(func(i int) { picked = append(picked, i) })
	l.SetRows(rows(14))

	l.HandleKey(KeyDown)
	l.HandleKey(KeyDown) // cursor on row 3
	if !l.HandleKey(Key("0")) {
		t.Fatal("0 on a 14-row list not consumed (compose arm)")
	}
	if !l.HandleKey(KeyEnter) {
		t.Fatal("Enter on a bare compose not consumed")
	}
	if fmt.Sprint(picked) != "[2]" || l.Cursor() != 2 {
		t.Fatalf("0,Enter picked %v cursor %d; want [2], 2 (Enter activates the cursor row)", picked, l.Cursor())
	}
	if l.compose || l.pending != 0 {
		t.Fatalf("compose state left behind: compose=%v pending=%d", l.compose, l.pending)
	}
	// The prefix is gone: the next digit is an ordinary instant pick again.
	l.HandleKey(Digit(3))
	if fmt.Sprint(picked) != "[2 2]" {
		t.Fatalf("digit after the cancelled prefix picked %v, want [2 2]", picked)
	}
}

// TestListComposeFooterShowsThePendingPrefix pins the visible-feedback fix:
// the armed prefix and every digit typed after it are on the footer line the
// list already draws, so the compose state is never invisible and costs no
// extra frame row (the row window does not reflow while composing).
func TestListComposeFooterShowsThePendingPrefix(t *testing.T) {
	l := newStepList(nil)
	l.SetRows(rows(14))
	resting := l.FooterHint()
	restingRows := len(l.Render(80))

	if !l.HandleKey(Key("0")) {
		t.Fatal("0 on a 14-row list not consumed")
	}
	if got, want := l.FooterHint(), "composing 0·_ · digits extend · Enter activates the cursor row"; got != want {
		t.Fatalf("armed footer = %q, want %q", got, want)
	}
	if got := len(l.Render(80)); got != restingRows {
		t.Fatalf("compose reflowed the window: %d rows, want %d", got, restingRows)
	}

	l.HandleKey(Digit(1))
	if got, want := l.FooterHint(), "composing 0·1_ · Enter activates row 1 · other keys cancel"; got != want {
		t.Fatalf("pending footer = %q, want %q", got, want)
	}
	// The live prefix reuses the footer line, so it must still fit the standard
	// 80-cell frame the screens are pinned at.
	if w := cellwidth.String(l.FooterHint()); w > 80 {
		t.Fatalf("pending footer = %d cells, want <= 80", w)
	}
	// The hint names the row Enter would activate, so it cannot drift from the
	// commit path.
	var picked []int
	l.onPick = func(i int) { picked = append(picked, i) }
	l.HandleKey(KeyEnter)
	if fmt.Sprint(picked) != "[0]" {
		t.Fatalf("the hinted row was not the activated row: picked %v, want [0]", picked)
	}
	if got := l.FooterHint(); got != resting {
		t.Fatalf("footer after the commit = %q, want the resting legend %q", got, resting)
	}

	// Any other key cancels the prefix, and the footer says so again.
	l.HandleKey(Key("0"))
	l.HandleKey(KeyDown)
	if got := l.FooterHint(); got != longListFooter {
		t.Fatalf("footer after a cancelling key = %q, want %q", got, longListFooter)
	}
}
