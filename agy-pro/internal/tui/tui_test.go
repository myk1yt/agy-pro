package tui

import (
	"fmt"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
)

// TestFrameGolden pins the frame snapshot layout with a mixed ASCII+CJK
// fixture: title, blank, body, blank, message, blank, footer. CJK text is
// kept verbatim (the painter, not the snapshot, pads and mirrors).
func TestFrameGolden(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{
		title: "Hub",
		mode:  ModeNav,
		body:  []string{"설정 Providers", "언어 Language"},
	})
	app.SetMessage("한 line saved")

	f := app.Frame()
	if f.Width != 80 || f.Height != 20 || f.Dir != DirLTR {
		t.Fatalf("frame geometry = %dx%d dir %v, want 80x20 LTR", f.Width, f.Height, f.Dir)
	}

	want := strings.Join([]string{
		"Hub",
		"",
		"설정 Providers",
		"언어 Language",
		"",
		"한 line saved",
		"",
		baseFooter,
	}, "\n")
	if got := f.String(); got != want {
		t.Fatalf("frame mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestFrameDirRidesAlong pins that DirRTL reaches the frame while the
// lines stay in logical order (mirroring is the P4 painter's job).
func TestFrameDirRidesAlong(t *testing.T) {
	app := NewApp(80, 20, DirRTL)
	app.Push(&stubScreen{title: "اللغة", mode: ModeNav, body: []string{"العربية"}})
	f := app.Frame()
	if f.Dir != DirRTL {
		t.Fatalf("frame dir = %v, want DirRTL", f.Dir)
	}
	if f.Lines[0] != "اللغة" {
		t.Fatalf("RTL title mutated: %q", f.Lines[0])
	}
}

func TestFrameModalBlock(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav})
	app.ShowModal(NewConfirm("Delete provider?", []string{"provider: acme"}, nil))
	f := app.Frame()
	want := strings.Join([]string{
		"Providers",
		"",
		"Delete provider?",
		"provider: acme",
		"y confirm · n/Esc cancel · other keys ignored",
		"",
		baseFooter,
	}, "\n")
	if got := f.String(); got != want {
		t.Fatalf("modal frame mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestFrameHeightClamp pins the height rule of the composer: the body is the
// first block to give up rows, so the chrome (title, modal, message, footer)
// survives (UX audit C1-F6). Phase 3 F-1 replaced the last-resort tail cut
// with the priority ladder, whose later stages drop the message and the title
// before the footer; the two-row case below pins that half.
func TestFrameHeightClamp(t *testing.T) {
	body := []string{"1", "2", "3", "4", "5"}

	// Four rows cannot hold title + separator + body + separator + footer, so
	// the body is dropped whole and the three chrome lines stay.
	app := NewApp(80, 4, DirLTR)
	app.Push(&stubScreen{title: "Tall", mode: ModeNav, body: body})
	f := app.Frame()
	if len(f.Lines) != 3 {
		t.Fatalf("frame lines = %d, want 3 (title, separator, footer):\n%s", len(f.Lines), f.String())
	}
	if f.Lines[0] != "Tall" || f.Lines[len(f.Lines)-1] != baseFooter {
		t.Fatalf("chrome did not survive the body drop:\n%s", f.String())
	}
	for _, dropped := range body {
		if strings.Contains(f.String(), dropped) {
			t.Fatalf("body line %q survived a frame that cannot hold it:\n%s", dropped, f.String())
		}
	}

	// Below the chrome's own height the ladder drops the screen title before
	// the footer. Phase 3 F-1 moved this expectation: the old last-resort tail
	// cut kept the title and ate the tail (footer first), while the new ladder
	// gives up the title and keeps the legend, so a two-row frame ends up with
	// the footer alone instead of title + separator.
	short := NewApp(80, 2, DirLTR)
	short.Push(&stubScreen{title: "Tall", mode: ModeNav, body: body})
	sf := short.Frame()
	if got := len(sf.Lines); got > 2 {
		t.Fatalf("clamped frame lines = %d, want at most 2", got)
	}
	if got := sf.Lines[len(sf.Lines)-1]; got != baseFooter {
		t.Fatalf("two-row frame footer = %q, want the base legend", got)
	}
	if strings.Contains(sf.String(), "Tall") {
		t.Fatalf("the title survived a frame too short to hold it and the footer:\n%s", sf.String())
	}
}

// TestCaptureRun drives a full headless run through the scripted seam:
// one frame per step, clean exit on Ctrl+C.
func TestCaptureRun(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	var got []Key
	app.Push(&stubScreen{title: "Hub", mode: ModeNav, record: &got})

	keys := &ScriptedKeys{Keys: []Key{KeyUp, KeyCtrlC}}
	out := app.CaptureRun(keys)
	if strings.Count(out, "Hub") != 2 {
		t.Fatalf("captured frames = %d, want 2 (initial + one step)", strings.Count(out, "Hub"))
	}
	if len(got) != 1 || got[0] != KeyUp {
		t.Fatalf("screen keys = %v, want [up]", got)
	}
	if !strings.HasSuffix(out, baseFooter+"\n") {
		t.Fatalf("capture does not end with the footer frame")
	}
}

func TestAppStackBasics(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	if app.Top() != nil || app.Depth() != 0 {
		t.Fatal("empty stack must have no top and depth 0")
	}
	app.Push(&stubScreen{title: "A", mode: ModeNav})
	app.Push(&stubScreen{title: "B", mode: ModeNav})
	if app.Top().Title() != "B" || app.Depth() != 2 {
		t.Fatalf("top/depth = %s/%d, want B/2", app.Top().Title(), app.Depth())
	}
	if !app.Pop() || app.Top().Title() != "A" {
		t.Fatalf("pop did not return to A")
	}
	if app.Pop() {
		t.Fatal("pop at the root must fail (Esc at the root exits instead)")
	}
	if app.Top() == nil || app.Depth() != 1 {
		t.Fatal("the root must survive a failed pop")
	}
}

func TestAppExitByScreen(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	quitter := &stubScreen{title: "Q", mode: ModeNav, quitAfter: 1}
	app.Push(quitter)
	if app.Step(KeyUp) {
		t.Fatal("screen Exit() did not stop the loop")
	}
}

// TestFrameStripsControlCharacters pins the F3 fix at the composition choke
// point: an escape-bearing model id in the title, body, modal, and message
// never reaches the frame lines, while tab and multi-byte UTF-8 survive.
func TestFrameStripsControlCharacters(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{
		title: "Models\x1b]0;pwned\x07",
		mode:  ModeNav,
		body:  []string{"ok\x1b[2J\x00model", "col1\tcol2", "日本語 العربية"},
	})
	app.SetMessage("saved\x1b[31m!")
	app.ShowModal(NewConfirm("Delete?", []string{"m\x1b[2K1"}, nil))

	f := app.Frame()
	joined := strings.Join(f.Lines, "\n")
	if strings.ContainsRune(joined, 0x1b) || strings.ContainsRune(joined, 0x07) || strings.ContainsRune(joined, 0x00) {
		t.Fatalf("frame contains control characters:\n%q", joined)
	}
	for _, want := range []string{"Models]0;pwned", "ok[2Jmodel", "col1\tcol2", "日本語 العربية", "saved[31m!", "m[2K1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("frame missing stripped content %q:\n%q", want, joined)
		}
	}
}

// TestFrameStripKeepsCellwidthAlignment pins that stripping is pure
// deletion: the surviving runes keep exactly the cell width they would have
// if the escape bytes had never been present, so pad/truncate layout math
// on the stripped line stays consistent.
func TestFrameStripKeepsCellwidthAlignment(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{
		title: "T",
		mode:  ModeNav,
		body:  []string{"Width: \x1b[31m日本語\x1b[0m end"},
	})
	f := app.Frame()
	got := f.Lines[2] // title, blank, first body line
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("body line still carries ESC: %q", got)
	}
	want := "Width: [31m日本語[0m end"
	if got != want {
		t.Errorf("stripped line = %q; want %q", got, want)
	}
	if w := cellwidth.String(got); w != cellwidth.String(want) {
		t.Errorf("stripped line cellwidth = %d, want %d", w, cellwidth.String(want))
	}
}

// TestMessageClearedOnScreenChange pins the F-1 fix (UX audit C1-F13): the
// message line belongs to the screen that produced it, so a Push or a Pop
// drops it, while the Esc-clears-message-first precedence on the current
// screen is unchanged.
func TestMessageClearedOnScreenChange(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.SetMessage("provider saved")
	if !strings.Contains(app.Frame().String(), "provider saved") {
		t.Fatalf("message did not render on the screen that set it:\n%s", app.Frame().String())
	}

	app.Push(&stubScreen{title: "Providers", mode: ModeNav})
	if got := app.Message(); got != "" {
		t.Fatalf("message survived a Push: %q", got)
	}
	if frame := app.Frame().String(); strings.Contains(frame, "provider saved") {
		t.Fatalf("stale message rendered on the screen above it:\n%s", frame)
	}

	app.SetMessage("delete canceled")
	if !app.Pop() {
		t.Fatal("Pop at depth 2 reported false")
	}
	if got := app.Message(); got != "" {
		t.Fatalf("message survived a Pop: %q", got)
	}

	// Esc semantics on the current screen are unchanged: the first Esc
	// cancels the message, the second pops.
	app.Push(&stubScreen{title: "Models", mode: ModeNav})
	app.SetMessage("model deleted")
	if !app.Step(KeyEsc) {
		t.Fatal("Esc on a message exited the app")
	}
	if app.Depth() != 2 || app.Message() != "" {
		t.Fatalf("first Esc: depth %d message %q, want depth 2 and no message", app.Depth(), app.Message())
	}
	if !app.Step(KeyEsc) {
		t.Fatal("the second Esc exited a two-screen stack")
	}
	if app.Depth() != 1 {
		t.Fatalf("the second Esc left depth %d, want 1", app.Depth())
	}
}

// TestFooterFitsFrameWidth pins the F-2 fix (UX audit C1-F16): the legend is
// fitted to the frame width like every other block, so it never wraps on an
// 80-column console, and it always names the three global keys.
func TestFooterFitsFrameWidth(t *testing.T) {
	if w := cellwidth.String(baseFooter); w > 80 {
		t.Fatalf("baseFooter = %d cells, want at most 80 (an 80-column console must not wrap it)", w)
	}
	for _, want := range []string{"Esc", "Ctrl+L", "Ctrl+C"} {
		if !strings.Contains(baseFooter, want) {
			t.Errorf("baseFooter lost the %q reminder", want)
		}
	}

	newFrame := func(w int) Frame {
		app := NewApp(w, 20, DirLTR)
		app.Push(&stubScreen{title: "Hub", mode: ModeNav, body: []string{"row"}})
		return app.Frame()
	}

	// The 80-column frame carries the full legend, not a wrap of it.
	f := newFrame(80)
	last := f.Lines[len(f.Lines)-1]
	if last != baseFooter {
		t.Fatalf("80-column footer = %q, want the full legend", last)
	}
	if got := cellwidth.String(last); got > 80 {
		t.Fatalf("80-column footer = %d cells, want at most 80", got)
	}

	// Wide frames keep the full legend; narrow ones step down to the compact
	// legend instead of losing the Ctrl+C reminder to a mid-legend cut.
	wide := newFrame(100)
	if got := wide.Lines[len(wide.Lines)-1]; got != baseFooter {
		t.Fatalf("100-column footer = %q, want the full legend", got)
	}
	narrow := newFrame(40)
	narrowLast := narrow.Lines[len(narrow.Lines)-1]
	if got := cellwidth.String(narrowLast); got > 40 {
		t.Fatalf("40-column footer = %d cells, want at most 40: %q", got, narrowLast)
	}
	for _, want := range []string{"Esc", "Ctrl+L", "Ctrl+C"} {
		if !strings.Contains(narrowLast, want) {
			t.Errorf("40-column footer lost the %q reminder: %q", want, narrowLast)
		}
	}
	for i, line := range narrow.Lines {
		if got := cellwidth.String(line); got > 40 {
			t.Errorf("40-column frame line %d = %d cells, want at most 40: %q", i, got, line)
		}
	}
}

// TestRootEscExitConfirmation pins the F-3 ceremony (user decision 8B): the
// first Esc at the root asks, the second exits, and any key in between
// disarms it. The hub Quit row keeps exiting on one key.
func TestRootEscExitConfirmation(t *testing.T) {
	want := missingOr(Bundle(), "app.exit_confirm", exitConfirmMsg)

	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	if !app.Step(KeyEsc) {
		t.Fatal("the first root Esc exited the app; it must arm the confirmation instead")
	}
	if app.Message() != want {
		t.Fatalf("armed message = %q, want %q", app.Message(), want)
	}
	if !strings.Contains(app.Frame().String(), want) {
		t.Fatalf("confirmation did not render:\n%s", app.Frame().String())
	}
	if app.Step(KeyEsc) {
		t.Fatal("the second root Esc did not exit")
	}

	// Any other key cancels the pending exit: the app keeps running, the
	// message clears, and a later Esc asks again instead of exiting.
	app = NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.Step(KeyEsc)
	if !app.Step(KeyUp) {
		t.Fatal("a key between the two Escs exited the app")
	}
	if app.Message() != "" {
		t.Fatalf("the cancelling key left the message %q", app.Message())
	}
	if !app.Step(KeyEsc) {
		t.Fatal("Esc after a cancel exited instead of asking again")
	}
	if app.Message() != want {
		t.Fatalf("re-armed message = %q, want %q", app.Message(), want)
	}
	if app.Step(KeyEsc) {
		t.Fatal("the second Esc of the re-armed pair did not exit")
	}

	// A live modal still owns Esc: cancelling a gate at the root must not arm
	// the exit ceremony.
	app = NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.ShowModal(NewConfirm("Delete provider?", nil, nil))
	if !app.Step(KeyEsc) {
		t.Fatal("Esc on a live gate exited the app")
	}
	if app.Modal() != nil || app.Message() != "" {
		t.Fatalf("Esc on a gate: modal %v message %q", app.Modal(), app.Message())
	}

	// Ctrl+C keeps its unconditional exit, armed or not.
	app = NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.Step(KeyEsc)
	if app.Step(KeyCtrlC) {
		t.Fatal("Ctrl+C kept the app running after the confirmation armed")
	}

	// The hub Quit row (a screen calling Exit) still exits on one key, even
	// with the confirmation armed.
	app = NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav, quitAfter: 1})
	app.Step(KeyEsc)
	if app.Step(KeyUp) {
		t.Fatal("the Quit row path did not exit after the confirmation armed")
	}
}

// TestResizeUpdatesFrameGeometry pins the F-4 foundation (UX audit C2-F4,
// C1-F30): Resize moves the geometry the next composed frame uses, is safe
// before the first paint, and asks for a full repaint.
func TestResizeUpdatesFrameGeometry(t *testing.T) {
	body := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		body = append(body, fmt.Sprintf("row %d", i))
	}

	app := NewApp(120, 40, DirLTR) // geometry in place before the first paint
	app.Push(&stubScreen{title: strings.Repeat("T", 120), mode: ModeNav, body: body})
	app.Resize(80, 24)
	f := app.Frame()
	if f.Width != 80 || f.Height != 24 {
		t.Fatalf("frame geometry after Resize = %dx%d, want 80x24", f.Width, f.Height)
	}
	if got := len(f.Lines); got > 24 {
		t.Fatalf("frame lines = %d, want at most the new height 24", got)
	}
	if got := cellwidth.String(f.Lines[0]); got > 80 {
		t.Fatalf("title = %d cells, want at most the new width 80", got)
	}
	if got := app.Redraws(); got != 1 {
		t.Fatalf("redraws after Resize = %d, want 1", got)
	}

	// A size the host cannot report is ignored instead of producing a
	// degenerate frame.
	app.Resize(0, 0)
	app.Resize(-4, 10)
	if app.Width() != 80 || app.Height() != 24 {
		t.Fatalf("invalid Resize changed geometry to %dx%d", app.Width(), app.Height())
	}
}

// fullPageBody is the body of a screen whose list fills any plausible frame,
// the shape that used to push a live gate off-frame.
func fullPageBody(n int) []string {
	body := make([]string, 0, n)
	for i := 0; i < n; i++ {
		body = append(body, fmt.Sprintf("row %d", i))
	}
	return body
}

// TestFrameKeepsModalOnScreen pins the F-5 primitive (UX audit C1-F6, the
// M3/M4 blocker): at 80x24 a full-page body no longer pushes a live modal off
// the frame, and when nothing is left for the body the modal replaces it.
func TestFrameKeepsModalOnScreen(t *testing.T) {
	body := fullPageBody(40)
	gate := NewConfirm("Delete provider?", []string{"provider: acme"}, nil)

	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav, body: body})
	app.ShowModal(gate)
	app.SetMessage("registry warning")
	frame := app.Frame()
	if got := len(frame.Lines); got > 24 {
		t.Fatalf("frame lines = %d, want at most the height 24:\n%s", got, frame.String())
	}
	joined := frame.String()
	for _, want := range []string{gate.Title(), gate.Footer(), "provider: acme", "registry warning", baseFooter, "row 0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("frame lost %q:\n%s", want, joined)
		}
	}
	// The body is the block that gave up the rows.
	if strings.Contains(joined, "row 39") {
		t.Errorf("the last body row survived a frame that needed its rows:\n%s", joined)
	}

	// Small console: the chrome (title, modal, footer) is what must fit, and
	// the body is dropped entirely rather than pushing the modal off-frame.
	tiny := NewApp(80, 8, DirLTR)
	tiny.Push(&stubScreen{title: "Providers", mode: ModeNav, body: body})
	tinyGate := NewConfirm("Delete provider?", []string{"provider: acme"}, nil)
	tiny.ShowModal(tinyGate)
	tinyFrame := tiny.Frame()
	if got := len(tinyFrame.Lines); got > 8 {
		t.Fatalf("8-row frame lines = %d, want at most 8:\n%s", got, tinyFrame.String())
	}
	for _, want := range []string{tinyGate.Title(), tinyGate.Footer(), baseFooter} {
		if !strings.Contains(tinyFrame.String(), want) {
			t.Errorf("8-row frame lost %q:\n%s", want, tinyFrame.String())
		}
	}
	if strings.Contains(tinyFrame.String(), "row 0") {
		t.Errorf("the body should have been dropped at 8 rows:\n%s", tinyFrame.String())
	}
}

// TestComposeFrameBlockKinds pins the shared composer primitive the console
// painter consumes: one tag per line, aligned by index, with the banner as a
// trusted prefix and a separator tag between sections.
func TestComposeFrameBlockKinds(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav, body: []string{"row a", "row b"}})
	app.SetMessage("saved")
	banner := []string{"banner one", "banner two"}

	c := app.ComposeFrame(banner)
	if len(c.Kinds) != len(c.Frame.Lines) {
		t.Fatalf("kinds = %d, lines = %d; they must align by index", len(c.Kinds), len(c.Frame.Lines))
	}
	if c.Frame.Trusted != len(banner) {
		t.Fatalf("Trusted = %d, want the banner length %d", c.Frame.Trusted, len(banner))
	}
	want := []BlockKind{
		BlockBanner, BlockBanner,
		BlockSeparator, BlockTitle,
		BlockSeparator, BlockBody, BlockBody,
		BlockSeparator, BlockMessage,
		BlockSeparator, BlockFooter,
	}
	if fmt.Sprint(c.Kinds) != fmt.Sprint(want) {
		t.Fatalf("kinds = %v, want %v", c.Kinds, want)
	}

	// App.Frame is the same layout without the trusted prefix.
	plain := app.Frame()
	if plain.Trusted != 0 {
		t.Fatalf("App.Frame Trusted = %d, want 0", plain.Trusted)
	}
	if !strings.HasSuffix(plain.String(), baseFooter) {
		t.Fatalf("the footer is not the last block:\n%s", plain.String())
	}
}

// gateSummaryLines returns n distinct approval summary lines, the shape of the
// providers.json diff the M4 gate renders.
func gateSummaryLines(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("summary line %02d", i))
	}
	return out
}

// gateFixture builds the audit repro shape: a screen that fills the page, a
// live 20-line approval gate (title + 18 summary lines + legend), and, when msg
// is not empty, a message line above the footer.
func gateFixture(w, h, bodyRows int, msg string) (*App, *Approval, []string) {
	app := NewApp(w, h, DirLTR)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav, body: fullPageBody(bodyRows)})
	summary := gateSummaryLines(18)
	m := NewApproval("Delete provider", summary, func(bool) {})
	app.ShowModal(m)
	if msg != "" {
		app.SetMessage(msg)
	}
	return app, m, summary
}

// assertGateChrome fails unless the frame carries the two lines the priority
// cut may never remove while a gate is live: the gate legend (the only text
// documenting y / n / Esc) and the shared base footer.
func assertGateChrome(t *testing.T, f Frame, m Modal) {
	t.Helper()
	joined := f.String()
	if !strings.Contains(joined, m.Footer()) {
		t.Fatalf("frame lost the gate legend %q:\n%s", m.Footer(), joined)
	}
	if !strings.Contains(joined, baseFooter) {
		t.Fatalf("frame lost the base footer legend:\n%s", joined)
	}
}

// TestPriorityCutKeepsGateChromeAt80x24 pins F-1 (audit F6, BLOCKER): at the
// standard 80x24 geometry with a full-page list and a live 20-line gate, the
// frame is at most 24 lines and still carries the gate title, every summary
// line, the gate legend, and the shared base footer. The body is what gives up
// the rows, so the gate can never be answered blind.
func TestPriorityCutKeepsGateChromeAt80x24(t *testing.T) {
	app, m, summary := gateFixture(80, 24, 40, "")
	f := app.Frame()
	if got := len(f.Lines); got > 24 {
		t.Fatalf("frame = %d lines, want at most 24:\n%s", got, f.String())
	}
	joined := f.String()
	if !strings.Contains(joined, m.Title()) {
		t.Fatalf("gate title is off-frame:\n%s", joined)
	}
	for _, line := range summary {
		if !strings.Contains(joined, line) {
			t.Fatalf("summary line %q is off-frame:\n%s", line, joined)
		}
	}
	assertGateChrome(t, f, m)
	if strings.Contains(joined, "row 0") {
		t.Fatalf("a body row survived although the gate needed its rows:\n%s", joined)
	}
}

// TestPriorityCutTruncatesModalBodyNotChrome pins the modal-body stage: once
// the screen title is gone, the gate body loses its bottom lines while its
// title and legend stay, so the operator keeps the y / n / Esc documentation
// for a gate that still answers every key.
func TestPriorityCutTruncatesModalBodyNotChrome(t *testing.T) {
	const h = 20
	app, m, summary := gateFixture(80, h, 0, "")
	f := app.Frame()
	if got := len(f.Lines); got != h {
		t.Fatalf("frame = %d lines, want exactly %d:\n%s", got, h, f.String())
	}
	joined := f.String()
	if !strings.Contains(joined, m.Title()) {
		t.Fatalf("gate title is off-frame:\n%s", joined)
	}
	assertGateChrome(t, f, m)
	// 24 lines must fit 20: the screen title (two rows with its separator) and
	// the last two summary lines are what go.
	for _, line := range summary[:16] {
		if !strings.Contains(joined, line) {
			t.Fatalf("summary line %q is off-frame:\n%s", line, joined)
		}
	}
	for _, line := range summary[16:] {
		if strings.Contains(joined, line) {
			t.Fatalf("summary line %q survived although the frame is over height:\n%s", line, joined)
		}
	}
	if strings.Contains(joined, "Providers") {
		t.Fatalf("the screen title survived the modal body:\n%s", joined)
	}
}

// TestPriorityCutAt80x16KeepsLegendAndFooter pins the small-console half of
// F-1: at 80x16 the gate legend and the base footer are still on frame.
func TestPriorityCutAt80x16KeepsLegendAndFooter(t *testing.T) {
	const h = 16
	app, m, summary := gateFixture(80, h, 30, "")
	f := app.Frame()
	if got := len(f.Lines); got > h {
		t.Fatalf("frame = %d lines, want at most %d:\n%s", got, h, f.String())
	}
	assertGateChrome(t, f, m)
	if !strings.Contains(f.String(), m.Title()) {
		t.Fatalf("gate title is off-frame:\n%s", f.String())
	}
	if strings.Contains(f.String(), summary[len(summary)-1]) {
		t.Fatalf("the last summary line survived a frame that cannot hold it:\n%s", f.String())
	}
}

// TestFrameCutLadderOrder pins the cut order itself (phase 3 F-1/F-3): body
// first, then the message, then the screen title, then the modal body from its
// bottom, then the modal title, and finally the blank separator. Every row
// shrinks the same fixture and names exactly what must be gone and what must
// survive. The gate legend and the base footer survive every row, because a
// live gate still answers every key.
func TestFrameCutLadderOrder(t *testing.T) {
	cases := []struct {
		h      int
		want   []string
		absent []string
	}{
		{h: 26, want: []string{"Providers", "Delete provider", "summary line 01", "summary line 18", "registry warning"}},
		{h: 24, want: []string{"Providers", "Delete provider", "summary line 01", "summary line 18"}, absent: []string{"registry warning"}},
		{h: 22, want: []string{"Delete provider", "summary line 01", "summary line 18"}, absent: []string{"registry warning", "Providers"}},
		{h: 21, want: []string{"Delete provider", "summary line 01", "summary line 17"}, absent: []string{"summary line 18", "Providers"}},
		{h: 6, want: []string{"Delete provider", "summary line 01", "summary line 02"}, absent: []string{"summary line 03", "Providers"}},
		{h: 4, want: []string{"Delete provider"}, absent: []string{"summary line 01", "Providers"}},
		{h: 3, absent: []string{"Delete provider"}},
		{h: 2},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("h=%d", tc.h), func(t *testing.T) {
			app, m, _ := gateFixture(80, tc.h, 30, "registry warning")
			f := app.Frame()
			if got := len(f.Lines); got != tc.h {
				t.Fatalf("frame = %d lines, want exactly %d:\n%s", got, tc.h, f.String())
			}
			assertGateChrome(t, f, m)
			joined := f.String()
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("frame lost %q:\n%s", want, joined)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(joined, absent) {
					t.Fatalf("frame kept %q although the ladder should have dropped it:\n%s", absent, joined)
				}
			}
		})
	}

	// One row is the only geometry the ladder cannot satisfy: the gate legend
	// and the footer need two rows between them. The hard cap keeps the head,
	// so the legend (the keys the live gate still answers) outlives the footer.
	app, m, _ := gateFixture(80, 1, 0, "")
	f := app.Frame()
	if len(f.Lines) != 1 || f.Lines[0] != m.Footer() {
		t.Fatalf("one-row frame = %q, want the gate legend alone", f.String())
	}
}

// TestAppFrameClampsEveryBlock pins F-2 on the shared path: the width clamp
// used to live only in the console painter adapter, so App.Frame (and the
// line-mode loop behind it) composed a caller-owned modal line and any
// overshooting screen line unclamped. The clamp now sits at the composition
// choke point, so no block on any path exceeds the frame width, while
// Modal.Body keeps returning the raw byte-stable slice.
func TestAppFrameClampsEveryBlock(t *testing.T) {
	const w, h = 80, 30
	long := strings.Repeat("w", 100)
	app := NewApp(w, h, DirLTR)
	app.Push(&stubScreen{title: long, mode: ModeNav, body: []string{long, "short row"}})
	app.SetMessage(long)
	m := NewApproval(long, []string{long}, func(bool) {})
	app.ShowModal(m)

	f := app.Frame()
	for i, line := range f.Lines {
		if cw := cellwidth.String(line); cw > w {
			t.Fatalf("App.Frame line %d = %d cells, want at most %d: %q", i, cw, w, line)
		}
	}
	if got := m.Body(w)[0]; got != long {
		t.Fatalf("Approval.Body no longer returns the raw slice:\n got %q\nwant %q", got, long)
	}
	if got := m.Title(); got != long {
		t.Fatalf("Approval.Title changed: %q", got)
	}
	joined := f.String()
	if !strings.Contains(joined, "short row") {
		t.Fatalf("short body line changed:\n%s", joined)
	}
	if !strings.Contains(joined, cellwidth.Truncate(long, w)) {
		t.Fatalf("over-wide blocks were not clamped to the frame width:\n%s", joined)
	}
}

// TestClampKeepsRTLContentVisible pins the RTL half of F-2:
// cellwidth.Truncate refuses every mid-string cut of a line carrying a
// Hebrew/Arabic rune and returns "", so a blanket clamp used to BLANK an
// over-wide approval summary line in an ar session while the gate still
// answered y: the consent content disappeared from the frame. The clamp now
// falls back to a visible cut that fits.
func TestClampKeepsRTLContentVisible(t *testing.T) {
	const w = 80
	if got := cellwidth.String(clampMarker); got != 1 {
		t.Fatalf("clampMarker = %d cells, want exactly 1: a one-cell frame must hold it", got)
	}
	arabic := "إزالة المزود: " + strings.Repeat("سجل المزود المحذوف مع النماذج ", 6)
	if cellwidth.String(arabic) <= w {
		t.Fatalf("fixture is not over-wide: %d cells", cellwidth.String(arabic))
	}
	if got := cellwidth.Truncate(arabic, w); got != "" {
		t.Fatalf("fixture does not exercise the RTL refusal: %q", got)
	}

	app := NewApp(w, 24, DirRTL)
	app.Push(&stubScreen{title: "المزودون", mode: ModeNav})
	m := NewApproval("حذف المزود", []string{arabic}, func(bool) {})
	app.ShowModal(m)
	f := app.Frame()
	if strings.ContainsRune(f.String(), 0x1b) {
		t.Fatalf("frame carries escape bytes:\n%q", f.String())
	}
	var line string
	for _, l := range f.Lines {
		if strings.HasSuffix(l, clampMarker) {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("over-wide RTL line was blanked instead of cut:\n%s", f.String())
	}
	if cw := cellwidth.String(line); cw > w {
		t.Fatalf("clamped RTL line = %d cells, want at most %d: %q", cw, w, line)
	}
	prefix := strings.TrimSuffix(line, clampMarker)
	if prefix == "" {
		t.Fatalf("clamped RTL line carries only the marker: %q", line)
	}
	if !strings.HasPrefix(arabic, prefix) {
		t.Fatalf("clamped RTL line is not a prefix of the source: %q", line)
	}
	if got := m.Body(w)[0]; got != arabic {
		t.Fatalf("Approval.Body no longer returns the raw slice:\n got %q\nwant %q", got, arabic)
	}

	// A one-cell frame cannot hold a prefix and the marker, and a wide first
	// rune cannot be cut by the ordinary truncator either: both still show the
	// marker instead of an empty line.
	if got := clampLine(arabic, 1); got != clampMarker {
		t.Fatalf("one-cell RTL clamp = %q, want the visible marker", got)
	}
	if got := clampLine("日本語", 1); got != clampMarker {
		t.Fatalf("one-cell wide-rune clamp = %q, want the visible marker", got)
	}
}

// TestResizeTinyViewportKeepsGateChrome pins the F-4 x F-1 interaction: the
// resize hook makes a tiny viewport reachable while a gate is live, and even
// there the gate legend and the footer legend stay on frame.
func TestResizeTinyViewportKeepsGateChrome(t *testing.T) {
	app, m, _ := gateFixture(120, 40, 30, "")
	for _, geom := range []struct{ w, h int }{{80, 10}, {80, 6}, {60, 8}} {
		app.Resize(geom.w, geom.h)
		f := app.Frame()
		if got := len(f.Lines); got > geom.h {
			t.Fatalf("%dx%d frame = %d lines, want at most %d:\n%s", geom.w, geom.h, got, geom.h, f.String())
		}
		joined := f.String()
		wantLegend := cellwidth.Truncate(m.Footer(), geom.w)
		if !strings.Contains(joined, wantLegend) {
			t.Fatalf("%dx%d frame lost the gate legend %q:\n%s", geom.w, geom.h, wantLegend, joined)
		}
		if !strings.Contains(joined, "Ctrl+C exit") {
			t.Fatalf("%dx%d frame lost the footer legend:\n%s", geom.w, geom.h, joined)
		}
	}
}

// caretTestScreen is a Screen that also implements CaretSource. The composer
// translation pins drive it with an explicit caret, so they pin the contract
// itself instead of borrowing a real editor's internal state.
type caretTestScreen struct {
	title string
	body  []string
	line  int
	cell  int
	live  bool
}

func (s *caretTestScreen) Title() string           { return s.title }
func (s *caretTestScreen) Mode() Mode              { return ModeNav }
func (s *caretTestScreen) Draw(a *App) []string    { return s.body }
func (s *caretTestScreen) HandleKey(k Key, a *App) {}
func (s *caretTestScreen) Caret() (int, int, bool) { return s.line, s.cell, s.live }

// TestComposeFrameCaretTranslatesBodyLine pins the translation the sibling
// screen codes against: the screen reports a line index inside its own Draw
// output, and the frame carries that line's absolute index (title, separator,
// then the body block) plus the cell verbatim.
func TestComposeFrameCaretTranslatesBodyLine(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	s := &caretTestScreen{title: "Editor", body: []string{"first", "second"}, line: 1, cell: 3, live: true}
	app.Push(s)
	c := app.ComposeFrame(nil)

	if c.Frame.CaretLine != 3 {
		t.Fatalf("CaretLine = %d, want 3 (title 0, separator 1, body starts 2, body line 1):\n%s",
			c.Frame.CaretLine, c.Frame.String())
	}
	if c.Frame.CaretCell != 3 {
		t.Fatalf("CaretCell = %d, want the screen's own 3", c.Frame.CaretCell)
	}
	if got := c.Frame.Lines[c.Frame.CaretLine]; got != "second" {
		t.Fatalf("caret line = %q, want %q", got, "second")
	}
	if got := c.Kinds[c.Frame.CaretLine]; got != BlockBody {
		t.Fatalf("caret line kind = %v, want BlockBody", got)
	}

	// The first body line translates to the body block's own start.
	s.line, s.cell = 0, 0
	if c := app.ComposeFrame(nil); c.Frame.CaretLine != 2 || c.Frame.CaretCell != 0 {
		t.Fatalf("caret on the first body line = (%d,%d), want (2,0)", c.Frame.CaretLine, c.Frame.CaretCell)
	}
}

// TestComposeFrameCaretShiftsPastBanner pins that the trusted banner prefix
// shifts the translation: the caret still lands on its own body line, and it
// never lands inside the banner block.
func TestComposeFrameCaretShiftsPastBanner(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&caretTestScreen{title: "Editor", body: []string{"first", "second"}, line: 1, cell: 2, live: true})
	banner := []string{"banner-1", "banner-2"}
	c := app.ComposeFrame(banner)
	if c.Frame.Trusted != 2 {
		t.Fatalf("Trusted = %d, want 2", c.Frame.Trusted)
	}
	if c.Frame.CaretLine != 6 {
		t.Fatalf("CaretLine = %d, want 6 (banner 0-1, separator 2, title 3, separator 4, body starts 5):\n%s",
			c.Frame.CaretLine, c.Frame.String())
	}
	if got := c.Frame.Lines[c.Frame.CaretLine]; got != "second" {
		t.Fatalf("caret line = %q, want %q", got, "second")
	}
	if c.Frame.CaretLine < c.Frame.Trusted {
		t.Fatalf("caret landed in the trusted banner prefix (line %d, trusted %d)",
			c.Frame.CaretLine, c.Frame.Trusted)
	}
}

// TestComposeFrameCaretCutLineIsDropped pins the first fail-safe: the body
// shrink stage removes rows from the body's tail, so a caret on a removed
// line has no frame line to land on and the composer reports -1 instead of a
// nearby line.
func TestComposeFrameCaretCutLineIsDropped(t *testing.T) {
	body := []string{"b1", "b2", "b3", "b4", "b5"}
	// title + separator + 5 body rows + separator + footer = 9 lines; a
	// seven-row frame keeps title, separator, three body rows and the footer,
	// so body lines 3 and 4 are gone.
	for _, horiz := range []struct {
		line int
		want int
	}{
		{0, 2}, {2, 4}, {3, -1}, {4, -1},
	} {
		app := NewApp(80, 7, DirLTR)
		app.Push(&caretTestScreen{title: "Editor", body: body, line: horiz.line, cell: 0, live: true})
		c := app.ComposeFrame(nil)
		if c.Frame.CaretLine != horiz.want {
			t.Errorf("caret on body line %d = %d, want %d:\n%s", horiz.line, c.Frame.CaretLine, horiz.want, c.Frame.String())
			continue
		}
		if horiz.want >= 0 && c.Frame.Lines[c.Frame.CaretLine] != body[horiz.line] {
			t.Errorf("caret on body line %d landed on %q, want %q",
				horiz.line, c.Frame.Lines[c.Frame.CaretLine], body[horiz.line])
		}
	}
}

// TestComposeFrameCaretBodyDropped pins the second fail-safe: when the body
// reservation gives every body row away (a frame too short for the body
// block), the body origin is gone and the caret is -1 rather than a line
// index from a block that is no longer on the frame.
func TestComposeFrameCaretBodyDropped(t *testing.T) {
	app := NewApp(80, 4, DirLTR)
	app.Push(&caretTestScreen{title: "Editor", body: []string{"b1", "b2", "b3", "b4", "b5"}, line: 0, cell: 0, live: true})
	c := app.ComposeFrame(nil)
	if got := c.Frame.String(); strings.Contains(got, "b1") {
		t.Fatalf("body survived a frame that cannot hold it:\n%s", got)
	}
	if c.Frame.CaretLine != -1 || c.Frame.CaretCell != -1 {
		t.Fatalf("caret = (%d,%d) on a frame with no body block; want (-1,-1)",
			c.Frame.CaretLine, c.Frame.CaretCell)
	}
}

// TestComposeFrameCaretWithoutSourceIsMinusOne pins the default every
// existing frame pin relied on: a screen that does not implement
// CaretSource, or one that reports no caret, leaves both fields at -1. The
// existing frame goldens (hub and modal fixtures reproduced here) stay
// byte-identical because -1 changes no line.
func TestComposeFrameCaretWithoutSourceIsMinusOne(t *testing.T) {
	hub := NewApp(80, 20, DirLTR)
	hub.Push(&stubScreen{title: "Hub", mode: ModeNav, body: []string{"설정 Providers", "언어 Language"}})
	hub.SetMessage("한 line saved")

	modal := NewApp(80, 20, DirLTR)
	modal.Push(&stubScreen{title: "Providers", mode: ModeNav})
	modal.ShowModal(NewConfirm("Delete provider?", []string{"provider: acme"}, nil))

	for name, app := range map[string]*App{"hub": hub, "modal": modal} {
		f := app.Frame()
		if f.CaretLine != -1 || f.CaretCell != -1 {
			t.Errorf("%s: caret = (%d,%d), want (-1,-1) for a screen without CaretSource",
				name, f.CaretLine, f.CaretCell)
		}
	}

	silent := NewApp(80, 20, DirLTR)
	silent.Push(&caretTestScreen{title: "T", body: []string{"x"}, line: 0, cell: 0, live: false})
	if f := silent.Frame(); f.CaretLine != -1 || f.CaretCell != -1 {
		t.Errorf("CaretSource reporting ok=false gave caret (%d,%d), want (-1,-1)", f.CaretLine, f.CaretCell)
	}
}

// TestComposeFrameCaretRejectsBadSource pins the remaining fail-safe arms: a
// screen whose caret points past its own Draw output, or that reports a
// negative position, gets no caret at all.
func TestComposeFrameCaretRejectsBadSource(t *testing.T) {
	cases := []struct {
		name string
		s    *caretTestScreen
	}{
		{"line-past-draw", &caretTestScreen{title: "T", body: []string{"a", "b"}, line: 2, cell: 0, live: true}},
		{"line-negative", &caretTestScreen{title: "T", body: []string{"a", "b"}, line: -1, cell: 0, live: true}},
		{"cell-negative", &caretTestScreen{title: "T", body: []string{"a", "b"}, line: 0, cell: -1, live: true}},
		{"body-empty", &caretTestScreen{title: "T", body: nil, line: 0, cell: 0, live: true}},
	}
	for _, c := range cases {
		app := NewApp(80, 20, DirLTR)
		app.Push(c.s)
		f := app.Frame()
		if f.CaretLine != -1 || f.CaretCell != -1 {
			t.Errorf("%s: caret = (%d,%d), want (-1,-1)", c.name, f.CaretLine, f.CaretCell)
		}
	}
}

// caretBodyApp composes the fixture of the re-measure pins: one screen whose
// single body line is the raw string it would draw, with the caret reported on
// that raw line at cell (the way a real editor reports it, over its own buffer
// line and before any strip).
func caretBodyApp(raw string, cell int) *App {
	app := NewApp(80, 20, DirLTR)
	app.Push(&caretTestScreen{title: "Editor", body: []string{raw}, line: 0, cell: cell, live: true})
	return app
}

// paintCaretFrame paints one app's composed frame on the color tier of a real
// console and returns the bytes, so a pin can assert where the reverse-video
// pair actually sits instead of trusting the frame fields alone.
func paintCaretFrame(t *testing.T, app *App) string {
	t.Helper()
	pf := composeFrame(app, nil)
	var out strings.Builder
	if err := NewPainter(&out, Cap{Tier: Tier16, Palette: DefaultPalette()}).Paint(pf.frame, pf.styles); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	return out.String()
}

// caretRuneAt returns the rune whose display-cell span contains cell: the same
// walk caretByteOffset does, kept independent here so a pin fails on a painter
// that marked the wrong rune instead of agreeing with the code under test.
func caretRuneAt(t *testing.T, line string, cell int) string {
	t.Helper()
	pos := 0
	for _, r := range line {
		w := cellwidth.Rune(r)
		if cell < pos+w {
			return string(r)
		}
		pos += w
	}
	t.Fatalf("caret cell %d is past the end of line %q", cell, line)
	return ""
}

// assertCaretLands pins the whole chain one re-measure case needs: the caret
// line survived the composition, the line the frame paints is exactly the
// stripped raw line, the frame carries wantCell, that cell covers wantRune, and
// the cell never passes the painted line's own width. The last check is the
// fail-safe property: a cell past the visible end is a caret the painter drops
// (invertAtCell returns the line untouched), so a re-measure that overshot
// would silently delete the glyph instead of misplacing it.
func assertCaretLands(t *testing.T, c Composed, raw string, wantCell int, wantRune string) {
	t.Helper()
	if c.Frame.CaretLine < 0 || c.Frame.CaretCell < 0 {
		t.Fatalf("caret dropped for raw line %q: (%d,%d)\n%s",
			raw, c.Frame.CaretLine, c.Frame.CaretCell, c.Frame.String())
	}
	painted := c.Frame.Lines[c.Frame.CaretLine]
	if want := stripControl(raw); painted != want {
		t.Fatalf("caret line for raw %q = %q, want the stripped raw line %q", raw, painted, want)
	}
	if c.Frame.CaretCell != wantCell {
		t.Fatalf("CaretCell for raw %q = %d, want %d (painted line %q):\n%s",
			raw, c.Frame.CaretCell, wantCell, painted, c.Frame.String())
	}
	if total := cellwidth.String(painted); c.Frame.CaretCell > total {
		t.Fatalf("CaretCell %d is past the painted line %q (%d cells): the painter would draw no glyph",
			c.Frame.CaretCell, painted, total)
	}
	if got := caretRuneAt(t, painted, c.Frame.CaretCell); got != wantRune {
		t.Fatalf("caret for raw %q covers %q at painted cell %d, want %q (painted %q)",
			raw, got, c.Frame.CaretCell, wantRune, painted)
	}
}

// TestComposeFrameCaretRemeasuredOverStrippedCc pins the composer-side fix for
// the confirmed caret drift (phase-5 review finding A). The screen measures its
// caret over the raw Draw line it owns, but compose strips Cc controls from
// that line before the painter places a glyph: "a\x02bc" with the caret before
// "c" reports raw cell 3, and cell 3 of the painted line "abc" is the
// end-of-line insertion cell, so the operator would see the caret after "c"
// while typing lands before it. The frame must carry cell 2 and the painter
// must invert "c", not append an inverted space.
func TestComposeFrameCaretRemeasuredOverStrippedCc(t *testing.T) {
	raw, cell := "a\x02bc", 3
	app := caretBodyApp(raw, cell)
	assertCaretLands(t, app.ComposeFrame(nil), raw, 2, "c")

	// The glyph step, both ways: the drift is only observable on a real paint,
	// so the pin names the exact reverse-video placement it accepts and the one
	// it refuses (the end-of-line space the raw cell produced).
	out := paintCaretFrame(t, app)
	if !strings.Contains(out, "ab"+caretSGRIn+"c"+caretSGROut) {
		t.Fatalf("painted frame does not invert \"c\" on the stripped line:\n%q", out)
	}
	if strings.Contains(out, "abc"+caretSGRIn+" "+caretSGROut) {
		t.Fatalf("painted frame drew the caret after \"c\" instead of on it:\n%q", out)
	}
}

// TestComposeFrameCaretShiftEqualsStrippedCellCount pins the arithmetic of the
// re-measure: every stripped rune before the caret costs exactly the cells the
// raw measurement counted for it, so two Cc runes shift the cell by two, and a
// caret reported on a stripped rune itself resolves to the cell of the next
// painted rune (the stripped rune is not on the frame, so the caret belongs
// where typing would land). A Cc rune after the caret changes nothing.
func TestComposeFrameCaretShiftEqualsStrippedCellCount(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		cell  int
		want  int
		shift int
		rune  string
	}{
		{"one Cc before the caret", "a\x02bc", 3, 2, 1, "c"},
		{"two Cc runes before the caret", "\x01\x02abc", 3, 1, 2, "b"},
		{"two separated Cc runes before the caret", "a\x02b\x03c", 4, 2, 2, "c"},
		{"caret on a stripped rune", "a\x02bc", 1, 1, 0, "b"},
		{"Cc rune after the caret only", "\x02a\x03", 1, 0, 1, "a"},
	}
	for _, tc := range cases {
		c := caretBodyApp(tc.raw, tc.cell).ComposeFrame(nil)
		assertCaretLands(t, c, tc.raw, tc.want, tc.rune)
		if got := tc.cell - c.Frame.CaretCell; got != tc.shift {
			t.Errorf("%s: shift for raw %q cell %d = %d, want %d (painted cell %d)",
				tc.name, tc.raw, tc.cell, got, tc.shift, c.Frame.CaretCell)
		}
	}
}

// TestComposeFrameCaretRemeasuredOverCrlfLine pins the production shape that
// reaches this path: a CRLF prompt file seeds \r into the editor buffer, so the
// line the screen draws carries a Cc rune the composer strips. "ab\rcd" with
// the caret after "c" reports raw cell 4, which is past the four-cell painted
// line "abcd", where the painter draws nothing at all; the re-measured cell 3
// puts the caret on "d", the rune a typed character would take the place of.
func TestComposeFrameCaretRemeasuredOverCrlfLine(t *testing.T) {
	raw, cell := "ab\rcd", 4
	app := caretBodyApp(raw, cell)
	c := app.ComposeFrame(nil)
	assertCaretLands(t, c, raw, 3, "d")

	// The fixture only means something if the CR really is there and really is
	// gone from the painted line.
	painted := c.Frame.Lines[c.Frame.CaretLine]
	if !strings.Contains(raw, "\r") || strings.Contains(painted, "\r") {
		t.Fatalf("fixture lost its CRLF shape: raw %q, painted %q", raw, painted)
	}
	if out := paintCaretFrame(t, app); !strings.Contains(out, "abc"+caretSGRIn+"d"+caretSGROut) {
		t.Fatalf("painted frame does not invert \"d\" after the stripped CR:\n%q", out)
	}
}

// TestComposeFrameCaretAtEndBehindStrippedControl pins the end-of-line arm: the
// caret at the raw end of "abc\r" is cell 4, which is exactly the raw line's own
// cell count (the trailing CR measures one cell in cellwidth), and it is one
// past the three-cell painted line. Re-measured to 3 it is the painted line's
// end, so the insertion cell is drawn exactly where typing lands. The boundary
// is the point of the fixture: the helper maps a cell AT the raw end to the
// painted end, while a cell PAST the raw end passes through unchanged so the
// painter can drop it (the sibling pin covers that half).
func TestComposeFrameCaretAtEndBehindStrippedControl(t *testing.T) {
	raw, cell := "abc\r", 4
	if rawCells := cellwidth.String(raw); rawCells != cell {
		t.Fatalf("fixture assumption broken: raw %q measures %d cells, want the reported cell %d (the raw end)",
			raw, rawCells, cell)
	}
	app := caretBodyApp(raw, cell)
	c := app.ComposeFrame(nil)
	if c.Frame.CaretLine < 0 {
		t.Fatalf("caret dropped for raw line %q:\n%s", raw, c.Frame.String())
	}
	painted := c.Frame.Lines[c.Frame.CaretLine]
	if painted != "abc" {
		t.Fatalf("painted line = %q, want %q", painted, "abc")
	}
	if want := cellwidth.String(painted); c.Frame.CaretCell != want {
		t.Fatalf("CaretCell = %d, want the painted line's end %d", c.Frame.CaretCell, want)
	}
	if out := paintCaretFrame(t, app); !strings.Contains(out, "abc"+caretSGRIn+" "+caretSGROut) {
		t.Fatalf("painted frame drew no insertion cell at the painted line's end:\n%q", out)
	}
}

// TestComposeFrameCaretPastRawEndPassesThrough pins the fail-safe half of the
// re-measure contract: a reported cell PAST the raw line's own cell count is not
// relocated. "abc\r" is four raw cells and three painted ones, so cell 99 is
// past both. The helper must return it unchanged, the frame must carry it, and
// the painter must then draw nothing on either tier, which is the documented
// "a caret cell past the visible end draws nothing" rule (render.go). Mapping it
// to 3 instead would put a caret on the painted line's end that the screen never
// reported, which is the "caret on the wrong rune" failure this feature exists
// to avoid.
//
// No production screen reaches this shape today: Editor.CaretCell is a bounded
// prefix sum over the buffer line, so it cannot exceed the line. The pin exists
// so the helper cannot start relocating an overshooting screen later, and so the
// doc comment's invariant is tested instead of asserted.
func TestComposeFrameCaretPastRawEndPassesThrough(t *testing.T) {
	raw, cell := "abc\r", 99
	if rawCells := cellwidth.String(raw); cell <= rawCells {
		t.Fatalf("fixture assumption broken: cell %d is not past the raw line %q (%d cells)", cell, raw, rawCells)
	}
	if got := caretCellAfterStrip(raw, cell); got != cell {
		t.Fatalf("caretCellAfterStrip(%q, %d) = %d, want the cell unchanged: a cell past the raw end is the painter's to drop, not the re-measure's to relocate",
			raw, cell, got)
	}
	app := caretBodyApp(raw, cell)
	c := app.ComposeFrame(nil)
	if c.Frame.CaretLine < 0 {
		t.Fatalf("caret dropped for raw line %q:\n%s", raw, c.Frame.String())
	}
	painted := c.Frame.Lines[c.Frame.CaretLine]
	if painted != "abc" {
		t.Fatalf("painted caret line = %q, want %q", painted, "abc")
	}
	if c.Frame.CaretCell != cell {
		t.Fatalf("CaretCell = %d, want %d: the frame carries what the screen reported past its own line",
			c.Frame.CaretCell, cell)
	}

	out := paintCaretFrame(t, app)
	if strings.Contains(out, caretSGRIn) || strings.Contains(out, caretSGROut) {
		t.Fatalf("color tier drew a caret for a cell past the painted line's end:\n%q", out)
	}
	if !strings.Contains(out, "abc") {
		t.Fatalf("color tier lost the stripped line:\n%q", out)
	}

	var plain strings.Builder
	if err := NewPainter(&plain, plainTestCap()).Paint(c.Frame, nil); err != nil {
		t.Fatalf("plain paint: %v", err)
	}
	if got := strings.Split(plain.String(), "\n")[c.Frame.CaretLine]; got != "abc" {
		t.Fatalf("plain tier caret row = %q, want the untouched frame line %q", got, "abc")
	}
}

// TestComposeFrameCaretCfRuneIsSkippedNotSubtracted pins the one arithmetic
// trap in the re-measure: cellwidth already measures a Cf format rune zero
// cells wide, so the strip removes a rune that never cost a cell. Counting the
// surviving cells keeps the caret on "c"; subtracting one cell per stripped
// rune would move it left onto "b". U+202E RIGHT-TO-LEFT OVERRIDE is also the
// rune class a disk-borne prompt file uses to spoof a line, so it has to be
// stripped and it has to leave the cell alone.
func TestComposeFrameCaretCfRuneIsSkippedNotSubtracted(t *testing.T) {
	if got := cellwidth.Rune('\u202e'); got != 0 {
		t.Fatalf("fixture assumption broken: cellwidth.Rune(U+202E) = %d, want 0", got)
	}
	raw, cell := "a\u202ebc", 2
	c := caretBodyApp(raw, cell).ComposeFrame(nil)
	assertCaretLands(t, c, raw, cell, "c")
}

// TestComposeFrameCaretCleanLineKeepsItsCell pins the no-op arm: a line with
// nothing to strip re-measures to the identical cell, so every frame without
// control runes keeps its byte-for-byte caret and the pre-existing caret pins
// stay green. The table walks whole lines, including the CJK double-width spans
// and a tab, which survives the strip and stays its own cell.
func TestComposeFrameCaretCleanLineKeepsItsCell(t *testing.T) {
	cases := []struct {
		line string
		cell int
	}{
		{"hello", 0}, {"hello", 3}, {"hello", 5},
		{"日本語", 0}, {"日本語", 1}, {"日本語", 4}, {"日本語", 6},
		{"a\tb", 1}, {"a\tb", 2},
		{"", 0},
	}
	for _, tc := range cases {
		c := caretBodyApp(tc.line, tc.cell).ComposeFrame(nil)
		if c.Frame.CaretLine < 0 || c.Frame.CaretCell != tc.cell {
			t.Errorf("%q cell %d: frame caret = (%d,%d), want the screen's own cell %d",
				tc.line, tc.cell, c.Frame.CaretLine, c.Frame.CaretCell, tc.cell)
		}
		if got := caretCellAfterStrip(tc.line, tc.cell); got != tc.cell {
			t.Errorf("caretCellAfterStrip(%q, %d) = %d, want the identical cell", tc.line, tc.cell, got)
		}
	}
}

// TestCaretCellAfterStrip pins the helper's own contract at the edges the
// composition pins cannot reach: a line made only of stripped runes, the raw
// end of a control-bearing line (which maps to the painted line's end), a cell
// past that raw end on the same line (which passes through unchanged, so the
// painter's cut-away fail-safe stays the last word), and the clean-line early
// return that keeps a clean caret byte-identical.
func TestCaretCellAfterStrip(t *testing.T) {
	cases := []struct {
		name string
		line string
		cell int
		want int
	}{
		{"clean line keeps the cell", "abc", 1, 1},
		{"clean line, past the end stays verbatim", "abc", 99, 99},
		{"clean empty line", "", 0, 0},
		{"leading Cc", "\x02abc", 2, 1},
		{"trailing Cc at the raw end", "abc\r", 4, 3},
		{"one cell past the raw end behind a stripped Cc", "abc\r", 5, 5},
		{"far past the raw end behind a stripped Cc", "abc\r", 99, 99},
		{"only stripped runes", "\x02\x03", 2, 0},
		{"tab survives the strip and keeps its cell", "a\tbc", 3, 3},
		{"Cf rune is zero width and skipped", "a\u202ebc", 2, 2},
	}
	for _, tc := range cases {
		if got := caretCellAfterStrip(tc.line, tc.cell); got != tc.want {
			t.Errorf("%s: caretCellAfterStrip(%q, %d) = %d, want %d",
				tc.name, tc.line, tc.cell, got, tc.want)
		}
	}
}

// editorCaretScreen is a Screen backed by the real Editor component: Draw
// returns the editor's viewport rows and Caret reports the editor's own row and
// cell, so a pin can drive the production chain (buffer, caret measurement,
// composition, paint) instead of a hand-set cell.
type editorCaretScreen struct {
	title string
	ed    *Editor
}

func (s *editorCaretScreen) Title() string           { return s.title }
func (s *editorCaretScreen) Mode() Mode              { return ModeEdit }
func (s *editorCaretScreen) HandleKey(k Key, a *App) { s.ed.HandleKey(k) }

func (s *editorCaretScreen) Draw(a *App) []string {
	rows, _ := s.ed.View()
	return rows
}

func (s *editorCaretScreen) Caret() (int, int, bool) {
	_, caretRow := s.ed.View()
	return caretRow, s.ed.CaretCell(), true
}

// TestComposeFrameCaretRealEditorCrlfSeed drives the shape the review named as
// reachable in production without any stub: NewEditor splits the loaded text on
// "\n" and keeps the "\r" (editor.go), so a CRLF prompt file seeds a Cc rune
// into the live buffer, and the caret at the end of that line is measured one
// cell past the painted line. The pin walks real keys (KeyDown, KeyEnd) and
// asserts the composed cell and the painted insertion cell, so a fix that only
// worked on hand-built screens cannot pass it.
func TestComposeFrameCaretRealEditorCrlfSeed(t *testing.T) {
	s := &editorCaretScreen{title: "Editor", ed: NewEditor("first\r\nsecond\r\n")}
	app := NewApp(80, 20, DirLTR)
	app.Push(s)
	app.Step(KeyDown)
	app.Step(KeyEnd)

	row, cell := s.ed.Caret()
	raw := s.ed.lines[row]
	if raw != "second\r" || cell != 7 {
		t.Fatalf("editor fixture = line %q cell %d, want %q cell 7", raw, cell, "second\r")
	}

	c := app.ComposeFrame(nil)
	if c.Frame.CaretLine != 3 {
		t.Fatalf("CaretLine = %d, want 3 (title 0, separator 1, body starts 2):\n%s",
			c.Frame.CaretLine, c.Frame.String())
	}
	painted := c.Frame.Lines[c.Frame.CaretLine]
	if painted != "second" {
		t.Fatalf("painted caret line = %q, want %q", painted, "second")
	}
	if want := cellwidth.String(painted); c.Frame.CaretCell != want {
		t.Fatalf("CaretCell = %d, want the painted line's end %d (the editor reported %d)",
			c.Frame.CaretCell, want, cell)
	}
	if out := paintCaretFrame(t, app); !strings.Contains(out, "second"+caretSGRIn+" "+caretSGROut) {
		t.Fatalf("painted frame lost the insertion cell at the painted line's end:\n%q", out)
	}
}
