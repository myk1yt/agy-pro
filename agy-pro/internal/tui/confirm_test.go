package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestConfirmYesNoEsc(t *testing.T) {
	var answers []bool
	c := NewConfirm("Delete provider?", []string{"provider: acme", "registry entry only; key values untouched"}, func(ok bool) { answers = append(answers, ok) })

	if c.Key(KeyYes, nil) {
		t.Fatal("gate stayed open after y")
	}
	if len(answers) != 1 || !answers[0] || !c.Approved() || !c.Done() {
		t.Fatalf("y: answers %v approved %v done %v", answers, c.Approved(), c.Done())
	}

	var declines []bool
	c = NewConfirm("Delete provider?", nil, func(ok bool) { declines = append(declines, ok) })
	for _, k := range []Key{Digit(3), RuneKey('x'), KeyUp, KeyEnter, KeySpace} {
		if !c.Key(k, nil) {
			t.Fatalf("gate swallowed by %q", k)
		}
	}
	if len(declines) != 0 {
		t.Fatalf("stray keys answered the gate: %v", declines)
	}
	if c.Key(KeyEsc, nil) {
		t.Fatal("Esc left the gate open")
	}
	if len(declines) != 1 || declines[0] {
		t.Fatalf("Esc: declines %v, want [false]", declines)
	}
	if c.Approved() {
		t.Fatal("Esc approved the gate")
	}

	c = NewConfirm("Delete provider?", nil, func(bool) {})
	c.Key(KeyNo, nil)
	if c.Approved() {
		t.Fatal("n approved the gate")
	}
}

func TestChoiceDualNavigation(t *testing.T) {
	var digitPicks, arrowPicks []string
	dch := NewChoice("Mode", []string{"full", "clean"}, func(i int, v string) {
		if i >= 0 {
			digitPicks = append(digitPicks, v)
		}
	})
	if dch.Key(Digit(2), nil) {
		t.Fatal("digit activation left the choice open")
	}

	ach := NewChoice("Mode", []string{"full", "clean"}, func(i int, v string) {
		if i >= 0 {
			arrowPicks = append(arrowPicks, v)
		}
	})
	ach.Key(KeyDown, nil)
	if ach.Key(KeyEnter, nil) {
		t.Fatal("Enter activation left the choice open")
	}

	if fmt.Sprint(digitPicks) != "[clean]" || fmt.Sprint(arrowPicks) != "[clean]" {
		t.Fatalf("digit %v, arrows %v; want both [clean]", digitPicks, arrowPicks)
	}

	// Esc cancels with index -1.
	cancelled := false
	cch := NewChoice("Mode", []string{"full", "clean"}, func(i int, v string) {
		cancelled = i == -1
	})
	if cch.Key(KeyEsc, nil) {
		t.Fatal("Esc left the choice open")
	}
	if !cancelled {
		t.Fatal("Esc cancel did not report index -1")
	}
}

func TestApprovalRendersExactSummary(t *testing.T) {
	summary := []string{
		"providers.json (0600, atomic)",
		"+ provider acme",
		"+   model acme-chat (context 1M, maxOut 8K)",
		"models before: 0 · after: 1",
	}
	var approved bool
	ap := NewApproval("Approve registry write", summary, func(ok bool) { approved = ok })

	body := ap.Body(80)
	if fmt.Sprint(body) != fmt.Sprint(summary) {
		t.Fatalf("summary mutated: %v", body)
	}
	// Only y/n/Esc live inside.
	for _, k := range []Key{Digit(1), KeyEnter, KeySpace, KeyUp, RuneKey('x')} {
		if !ap.Key(k, nil) {
			t.Fatalf("approval closed by %q", k)
		}
	}
	if approved {
		t.Fatal("stray keys approved the write")
	}
	if ap.Key(KeyYes, nil) {
		t.Fatal("y left the approval open")
	}
	if !approved {
		t.Fatal("y did not approve the write")
	}
}

// TestConfirmCaseInsensitive pins the round-5 fix: Caps Lock sends uppercase
// Y/N as the rune carrier, and both gates must answer them exactly like the
// y/n enum values. Digits, Enter, and Space stay swallowed.
func TestConfirmCaseInsensitive(t *testing.T) {
	// Uppercase rune carrier approves the Confirm gate.
	var answers []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	if !c.Key(RuneKey('x'), nil) {
		t.Fatal("stray rune closed the gate")
	}
	if c.Key(RuneKey('Y'), nil) {
		t.Fatal("uppercase Y left the gate open")
	}
	if len(answers) != 1 || !answers[0] || !c.Approved() {
		t.Fatalf("uppercase Y: answers %v approved %v", answers, c.Approved())
	}

	// Uppercase rune carrier declines the Confirm gate.
	c = NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	if c.Key(RuneKey('N'), nil) {
		t.Fatal("uppercase N left the gate open")
	}
	if len(answers) != 2 || answers[1] || c.Approved() {
		t.Fatalf("uppercase N: answers %v approved %v", answers, c.Approved())
	}

	// Lowercase rune carrier answers the Approval modal; uppercase declines.
	var approved bool
	ap := NewApproval("Approve registry write", nil, func(ok bool) { approved = ok })
	if ap.Key(RuneKey('y'), nil) || !approved {
		t.Fatalf("lowercase rune carrier: approved %v", approved)
	}
	ap = NewApproval("Approve registry write", nil, func(ok bool) { approved = ok })
	if ap.Key(RuneKey('N'), nil) || approved || !ap.Done() {
		t.Fatalf("uppercase N on approval: approved %v done %v", approved, ap.Done())
	}

	// Negative pins still hold: digit, Enter, Space never answer a gate.
	for _, k := range []Key{Digit(7), KeyEnter, KeySpace} {
		g := NewConfirm("Delete provider?", nil, func(bool) {})
		if !g.Key(k, nil) || g.Done() {
			t.Fatalf("gate answered by %q", k)
		}
	}
}

// TestModalPrecedence drives a full App: while a modal is open, keys are
// consumed by the modal before the screen, Esc cancels the modal first,
// and the Esc precedence chain (modal > message > one screen > exit) holds.
func TestModalPrecedence(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	var screenKeys []Key
	stub := &stubScreen{title: "Hub", mode: ModeNav, record: &screenKeys}
	app.Push(stub)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav, record: &screenKeys})
	app.SetMessage("saved")

	if app.Mode() != ModeNav {
		t.Fatalf("mode = %v, want ModeNav", app.Mode())
	}

	app.ShowModal(NewConfirm("Delete provider?", []string{"provider: acme"}, nil))
	if app.Mode() != ModeConfirm {
		t.Fatalf("mode with modal = %v, want ModeConfirm", app.Mode())
	}

	// Modal consumes keys first: nothing reaches the screen.
	for _, k := range []Key{Digit(1), KeyUp, KeyEnter, RuneKey('x')} {
		if !app.Step(k) {
			t.Fatalf("app exited while modal consumed %q", k)
		}
	}
	if len(screenKeys) != 0 {
		t.Fatalf("keys leaked past the modal: %v", screenKeys)
	}
	if app.Depth() != 2 || app.Message() != "saved" {
		t.Fatalf("modal key changed screen state: depth %d message %q", app.Depth(), app.Message())
	}

	// Esc cancels the modal; message and stack survive.
	if !app.Step(KeyEsc) {
		t.Fatal("Esc at a live modal exited the app")
	}
	if app.Modal() != nil {
		t.Fatal("modal still open after Esc")
	}
	if app.Depth() != 2 || app.Message() != "saved" {
		t.Fatalf("Esc modal changed state: depth %d message %q", app.Depth(), app.Message())
	}

	// Esc precedence: message first, then one screen, then exit at root.
	if !app.Step(KeyEsc) {
		t.Fatal("Esc on a message exited the app")
	}
	if app.Message() != "" {
		t.Fatalf("message survived Esc: %q", app.Message())
	}
	if !app.Step(KeyEsc) {
		t.Fatal("Esc on a two-screen stack exited the app")
	}
	if app.Depth() != 1 {
		t.Fatalf("depth after Esc = %d, want 1", app.Depth())
	}
	// Root exit ceremony, user decision 8B (tui.go Step): the first Esc at
	// the root asks and the second exits. The pin moved with that decision;
	// the modal-precedence assertions above are unchanged.
	if !app.Step(KeyEsc) {
		t.Fatal("first Esc at the root exited without asking")
	}
	if app.Message() == "" || app.Depth() != 1 {
		t.Fatalf("first Esc at the root did not arm the exit confirmation: depth %d message %q", app.Depth(), app.Message())
	}
	if app.Step(KeyEsc) {
		t.Fatal("second Esc at the root did not exit")
	}
}

func TestCtrlCHandling(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.ShowModal(NewConfirm("Delete provider?", nil, nil))
	app.SetMessage("busy")

	// Ctrl+C exits cleanly from every mode, even inside a modal.
	if app.Step(KeyCtrlC) {
		t.Fatal("Ctrl+C kept the app running")
	}

	// Ctrl+L clears the message and counts a redraw.
	app = NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.SetMessage("busy")
	for i := 1; i <= 3; i++ {
		if !app.Step(KeyCtrlL) {
			t.Fatalf("Ctrl+L #%d exited the app", i)
		}
		if app.Message() != "" {
			t.Fatalf("Ctrl+L #%d left the message %q", i, app.Message())
		}
	}
	if app.Redraws() != 3 {
		t.Fatalf("redraws = %d, want 3", app.Redraws())
	}
}

func TestModeScopedDispatch(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	edit := &stubScreen{title: "Prompt editor", mode: ModeEdit, record: &[]Key{}}
	app.Push(edit)
	if app.Mode() != ModeEdit {
		t.Fatalf("mode = %v, want ModeEdit", app.Mode())
	}
	// Keys reach an EDIT screen unfiltered: it owns text semantics.
	app.Step(Digit(3))
	app.Step(KeyYes)
	if fmt.Sprint(*edit.record) != "[3 y]" {
		t.Fatalf("EDIT screen received %v, want [3 y]", *edit.record)
	}
}

type stubScreen struct {
	title  string
	mode   Mode
	record *[]Key
	body   []string

	quitAfter int
	seen      int
}

func (s *stubScreen) Title() string { return s.title }
func (s *stubScreen) Mode() Mode    { return s.mode }
func (s *stubScreen) Draw(a *App) []string {
	if s.body != nil {
		return s.body
	}
	return nil
}
func (s *stubScreen) HandleKey(k Key, a *App) {
	if s.record != nil {
		*s.record = append(*s.record, k)
	}
	s.seen++
	if s.quitAfter > 0 && s.seen >= s.quitAfter {
		a.Exit()
	}
}

// fakeGateClock is a manually advanced clock for the arm-delay pins, so no
// test sleeps. armWith installs it on a gate's arm and re-stamps the open
// time (the constructor already stamped it with the package seam).
type fakeGateClock struct{ t time.Time }

func newFakeGateClock() *fakeGateClock {
	return &fakeGateClock{t: time.Unix(1700000000, 0)}
}

func (f *fakeGateClock) now() time.Time { return f.t }

func (f *fakeGateClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func armWith(a *gateArm, clk *fakeGateClock) {
	a.now = clk.now
	a.openedAt = clk.now()
}

// paceConsoleKey simulates the raw console reader delivering the key that the
// next Key call routes: the gate must already exist, because the constructor
// stamps the console serial the routing compares against. This is the pacing
// seam the arm pins drive; a line-mode or scripted delivery never calls it,
// which is why the rest of the suite takes the immediate path.
//
// The old TestMain stepped the package gate clock once per read so every
// scripted answer looked post-window. The pacing seam replaces that: scripted
// and line-mode sources are unpaced, so the window never applies to them and
// the package clock is the real clock for the whole suite. The arm pins below
// install a fake clock per gate and advance it explicitly, so no test sleeps
// and no pin depends on wall-clock timing.
func paceConsoleKey() { markConsoleKeyRead() }

// TestGateArmWindowBounds keeps the delay inside the 20-30 ms band the audit
// prescribed: short enough that a real answer is never delayed, long enough to
// swallow the tail of a paste burst.
func TestGateArmWindowBounds(t *testing.T) {
	if gateArmWindow < 20*time.Millisecond || gateArmWindow > 30*time.Millisecond {
		t.Fatalf("arm window = %v, want 20-30ms", gateArmWindow)
	}
}

// TestConfirmArmDelayDropsFastAnswer pins F-1 for the danger gate against the
// fail-closed shape: a y delivered inside the arm window is dropped and
// recorded, and no later key commits it. Only a fresh answer key closes the
// gate, exactly once.
//
// Moved pin: this was TestConfirmArmDelayBuffersFastAnswer, which asserted the
// old fail-open rule "the first key after the window re-evaluates the buffered
// y". A leaked paste remainder could ride an innocent key press into an
// approval that way, so the expectation is now the opposite: the dropped y is
// never replayed, and the key that answers after the window is a deliberate
// one. The four-behavior F-1 coverage (dropped inside, immediate after, Esc
// immediate, decline path) still lives in the pins around this one.
func TestConfirmArmDelayDropsFastAnswer(t *testing.T) {
	clk := newFakeGateClock()
	var answers []bool
	c := NewConfirm("Delete provider?", []string{"provider: acme"}, func(ok bool) { answers = append(answers, ok) })
	armWith(&c.arm, clk)
	paceConsoleKey()

	if !c.Key(KeyYes, nil) {
		t.Fatal("fast y closed the gate")
	}
	if c.Done() || len(answers) != 0 || !c.arm.suppressed {
		t.Fatalf("fast y: done %v answers %v suppressed %v", c.Done(), answers, c.arm.suppressed)
	}

	// A key inside the window is swallowed and clears the record.
	clk.advance(gateArmWindow - time.Millisecond)
	if !c.Key(RuneKey('x'), nil) || c.Done() || c.arm.suppressed {
		t.Fatalf("a key inside the window resolved the gate: done %v suppressed %v", c.Done(), c.arm.suppressed)
	}
	if !c.Key(KeyYes, nil) || c.Done() || !c.arm.suppressed {
		t.Fatalf("a second fast y answered: done %v suppressed %v", c.Done(), c.arm.suppressed)
	}

	// The clock alone never answers, and the first key after the window does
	// not commit the dropped y: that flush was the fail-open defect.
	clk.advance(2 * time.Millisecond)
	if c.Done() || len(answers) != 0 {
		t.Fatalf("the clock alone answered the gate: done %v answers %v", c.Done(), answers)
	}
	if !c.Key(Digit(3), nil) || c.Done() || len(answers) != 0 {
		t.Fatalf("a digit after the window committed the dropped y: done %v answers %v", c.Done(), answers)
	}

	// A fresh deliberate y answers, once.
	if c.Key(KeyYes, nil) {
		t.Fatal("the fresh y left the gate open")
	}
	if !c.Approved() || len(answers) != 1 || !answers[0] {
		t.Fatalf("fresh y: approved %v answers %v", c.Approved(), answers)
	}
}

// TestConfirmArmDelayAfterWindowAnswersImmediately pins the other half of F-1:
// past the window a console answer is immediate, so a real user sees no
// latency.
func TestConfirmArmDelayAfterWindowAnswersImmediately(t *testing.T) {
	clk := newFakeGateClock()
	var answers []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	armWith(&c.arm, clk)
	paceConsoleKey()
	clk.advance(gateArmWindow)

	if c.Key(KeyYes, nil) {
		t.Fatal("y after the window left the gate open")
	}
	if !c.Approved() || len(answers) != 1 || !answers[0] || c.arm.suppressed {
		t.Fatalf("y after the window: approved %v answers %v suppressed %v", c.Approved(), answers, c.arm.suppressed)
	}
}

// TestConfirmArmDelayEscIsImmediate pins that Esc never waits on the arm
// window, before or after it elapsed, and that a dropped y can never approve
// once Esc declined.
func TestConfirmArmDelayEscIsImmediate(t *testing.T) {
	clk := newFakeGateClock()
	var answers []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	armWith(&c.arm, clk)
	paceConsoleKey()

	if !c.Key(KeyYes, nil) {
		t.Fatal("fast y closed the gate")
	}
	clk.advance(2 * time.Millisecond)
	if c.Key(KeyEsc, nil) {
		t.Fatal("Esc waited on the arm window")
	}
	if c.Approved() || len(answers) != 1 || answers[0] || c.arm.suppressed {
		t.Fatalf("Esc after a dropped y: approved %v answers %v suppressed %v", c.Approved(), answers, c.arm.suppressed)
	}
	c.Key(KeyYes, nil)
	if len(answers) != 1 {
		t.Fatalf("Esc answer replayed: %v", answers)
	}

	// Past the window Esc declines immediately instead of committing the
	// dropped y (the old fail-open flush did the opposite).
	clk2 := newFakeGateClock()
	var late []bool
	c2 := NewConfirm("Delete provider?", nil, func(ok bool) { late = append(late, ok) })
	armWith(&c2.arm, clk2)
	paceConsoleKey()
	c2.Key(KeyYes, nil)
	clk2.advance(gateArmWindow)
	if c2.Key(KeyEsc, nil) || c2.Approved() {
		t.Fatalf("Esc after the window approved: answers %v", late)
	}
	if len(late) != 1 || late[0] {
		t.Fatalf("Esc after the window: %v", late)
	}
}

// TestConfirmArmDelayDeclinePath pins that the decline path keeps its direction
// and gains the fail-closed trigger: n past the window declines immediately,
// and a fast n is dropped, so it can decline only when the operator presses it
// again.
//
// Moved pin: the old fast-N half asserted that the first key after the window
// re-evaluated the n. That was the same fail-open flush, so the pin now asserts
// the dropped n is not replayed and a fresh n declines once.
func TestConfirmArmDelayDeclinePath(t *testing.T) {
	clk := newFakeGateClock()
	var answers []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	armWith(&c.arm, clk)
	paceConsoleKey()
	clk.advance(gateArmWindow)
	if c.Key(KeyNo, nil) {
		t.Fatal("n after the window left the gate open")
	}
	if c.Approved() || len(answers) != 1 || answers[0] {
		t.Fatalf("n after the window: approved %v answers %v", c.Approved(), answers)
	}

	clk2 := newFakeGateClock()
	var fast []bool
	c2 := NewConfirm("Delete provider?", nil, func(ok bool) { fast = append(fast, ok) })
	armWith(&c2.arm, clk2)
	paceConsoleKey()
	if !c2.Key(RuneKey('N'), nil) || c2.Done() || !c2.arm.suppressed {
		t.Fatalf("fast N answered: done %v suppressed %v", c2.Done(), c2.arm.suppressed)
	}
	clk2.advance(gateArmWindow)
	if !c2.Key(RuneKey('x'), nil) || c2.Done() {
		t.Fatalf("a stray key after the window committed the dropped N: done %v", c2.Done())
	}
	if c2.Key(RuneKey('N'), nil) {
		t.Fatal("the fresh N left the gate open")
	}
	if c2.Approved() || len(fast) != 1 || fast[0] {
		t.Fatalf("fresh N: approved %v answers %v", c2.Approved(), fast)
	}
}

// TestConfirmArmDelayFreshAnswerSupersedes pins the fail-closed edge: a dropped
// y never overrides the next deliberate answer.
func TestConfirmArmDelayFreshAnswerSupersedes(t *testing.T) {
	clk := newFakeGateClock()
	var answers []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	armWith(&c.arm, clk)
	paceConsoleKey()
	c.Key(KeyYes, nil)
	clk.advance(gateArmWindow)
	if c.Key(KeyNo, nil) {
		t.Fatal("n after the window left the gate open")
	}
	if c.Approved() || len(answers) != 1 || answers[0] {
		t.Fatalf("superseding n: approved %v answers %v", c.Approved(), answers)
	}
}

// TestApprovalArmDelay pins F-1 on the M4 gate, the only path to a
// providers.json write: a console-paced y inside the window is dropped, a
// stray key after the window never commits it, and a fresh y approves exactly
// once. The byte-stable mutation summary survives the window untouched.
//
// Moved pin: the middle step used to assert that the first key after the
// window re-evaluated the dropped y. That was the fail-open flush on the
// registry-write gate, so the stray now has to leave the modal open and only a
// fresh y may approve.
func TestApprovalArmDelay(t *testing.T) {
	summary := []string{
		"providers.json (0600, atomic)",
		"+ provider acme",
		"+   model acme-chat (context 1M, maxOut 8K)",
		"models before: 0 · after: 1",
	}
	clk := newFakeGateClock()
	var answers []bool
	ap := NewApproval("Approve registry write", summary, func(ok bool) { answers = append(answers, ok) })
	armWith(&ap.arm, clk)
	paceConsoleKey()

	if !ap.Key(RuneKey('y'), nil) {
		t.Fatal("fast y closed the approval")
	}
	if ap.Done() || len(answers) != 0 || !ap.arm.suppressed {
		t.Fatalf("fast y answered the approval: done %v answers %v suppressed %v", ap.Done(), answers, ap.arm.suppressed)
	}
	clk.advance(gateArmWindow)
	if !ap.Key(RuneKey('x'), nil) || ap.Done() || ap.Approved() || len(answers) != 0 {
		t.Fatalf("a stray key after the window approved the write: done %v approved %v answers %v", ap.Done(), ap.Approved(), answers)
	}
	if ap.Key(RuneKey('y'), nil) {
		t.Fatal("the fresh y left the approval open")
	}
	if !ap.Approved() || len(answers) != 1 || !answers[0] {
		t.Fatalf("approval y: approved %v answers %v", ap.Approved(), answers)
	}
	if fmt.Sprint(ap.Body(80)) != fmt.Sprint(summary) {
		t.Fatalf("summary mutated while suppressed: %v", ap.Body(80))
	}

	// Past the window the approval answers immediately.
	clk2 := newFakeGateClock()
	var late []bool
	ap2 := NewApproval("Approve registry write", summary, func(ok bool) { late = append(late, ok) })
	armWith(&ap2.arm, clk2)
	paceConsoleKey()
	clk2.advance(gateArmWindow)
	if ap2.Key(KeyYes, nil) || !ap2.Approved() || len(late) != 1 {
		t.Fatalf("approval y after the window: approved %v answers %v", ap2.Approved(), late)
	}
}

// TestGateStrayKeysNeverAnswer pins F-3's first half in both arm states: digits
// and Enter (and every other non-answer key) never answer either gate, with no
// answer pending and with an answer dropped inside the window, and whether the
// window is still open or already elapsed. A stray also clears the dropped
// record, so the old fail-open flush has nothing left to commit.
func TestGateStrayKeysNeverAnswer(t *testing.T) {
	strays := []Key{
		Digit(0), Digit(1), Digit(5), Digit(9), KeyEnter, KeySpace,
		KeyUp, KeyDown, KeyLeft, KeyRight, KeyTab, KeyBackspace,
		KeyDelete, KeyCtrlS, KeyHelp, RuneKey('x'),
	}
	// check drives one gate through one state: optionally drop a fast y, then
	// deliver every stray key at the elapsed offset.
	check := func(t *testing.T, label string, elapsed time.Duration, pending bool, clk *fakeGateClock, arm *gateArm, key func(Key) bool, done func() bool) {
		t.Helper()
		if pending {
			if !key(KeyYes) || done() || !arm.suppressed {
				t.Fatalf("%s: fast y not dropped: done %v suppressed %v", label, done(), arm.suppressed)
			}
		}
		clk.advance(elapsed)
		for _, k := range strays {
			if !key(k) || done() {
				t.Fatalf("%s: answered by %q (pending %v, elapsed %v)", label, k, pending, elapsed)
			}
			if arm.suppressed {
				t.Fatalf("%s: %q left a dropped answer behind (pending %v, elapsed %v)", label, k, pending, elapsed)
			}
		}
	}

	for _, elapsed := range []time.Duration{0, gateArmWindow} {
		for _, pending := range []bool{false, true} {
			clk := newFakeGateClock()
			var confirmAnswers []bool
			c := NewConfirm("Delete provider?", nil, func(ok bool) { confirmAnswers = append(confirmAnswers, ok) })
			armWith(&c.arm, clk)
			paceConsoleKey()
			check(t, "confirm", elapsed, pending, clk, &c.arm, func(k Key) bool { return c.Key(k, nil) }, c.Done)
			if len(confirmAnswers) != 0 || c.Approved() || c.Done() {
				t.Fatalf("confirm: callback fired on strays (pending %v, elapsed %v): %v", pending, elapsed, confirmAnswers)
			}
			// The gate is still live after the strays: a real decline lands once.
			if c.Key(KeyEsc, nil) || len(confirmAnswers) != 1 || confirmAnswers[0] || c.Approved() {
				t.Fatalf("confirm: Esc after strays: approved %v answers %v", c.Approved(), confirmAnswers)
			}

			clk2 := newFakeGateClock()
			var approvalAnswers []bool
			ap := NewApproval("Approve registry write", nil, func(ok bool) { approvalAnswers = append(approvalAnswers, ok) })
			armWith(&ap.arm, clk2)
			paceConsoleKey()
			check(t, "approval", elapsed, pending, clk2, &ap.arm, func(k Key) bool { return ap.Key(k, nil) }, ap.Done)
			if len(approvalAnswers) != 0 || ap.Approved() || ap.Done() {
				t.Fatalf("approval: callback fired on strays (pending %v, elapsed %v): %v", pending, elapsed, approvalAnswers)
			}
			if ap.Key(KeyEsc, nil) || len(approvalAnswers) != 1 || approvalAnswers[0] || ap.Approved() {
				t.Fatalf("approval: Esc after strays: approved %v answers %v", ap.Approved(), approvalAnswers)
			}
		}
	}
}

// TestGateBurstAfterWindowAnswersOnce pins F-3's second half: a burst of y
// delivered after the window answers exactly once, a dropped key followed by a
// fresh y and a replay cannot double-fire the callback, and the done-guard
// swallows every key after the gate closed.
func TestGateBurstAfterWindowAnswersOnce(t *testing.T) {
	clk := newFakeGateClock()
	var answers []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { answers = append(answers, ok) })
	armWith(&c.arm, clk)
	paceConsoleKey()
	clk.advance(gateArmWindow)
	for i := 0; i < 4; i++ {
		c.Key(KeyYes, nil)
	}
	if !c.Done() || len(answers) != 1 || !answers[0] {
		t.Fatalf("burst after the window: done %v answers %v", c.Done(), answers)
	}
	if c.Key(KeyYes, nil) || c.Key(KeyNo, nil) || len(answers) != 1 {
		t.Fatalf("a key after the gate closed answered again: %v", answers)
	}

	clk2 := newFakeGateClock()
	var mixed []bool
	c2 := NewConfirm("Delete provider?", nil, func(ok bool) { mixed = append(mixed, ok) })
	armWith(&c2.arm, clk2)
	paceConsoleKey()
	c2.Key(KeyYes, nil)
	clk2.advance(gateArmWindow)
	c2.Key(KeyYes, nil)
	c2.Key(KeyYes, nil)
	if !c2.Done() || len(mixed) != 1 || !mixed[0] {
		t.Fatalf("dropped plus replay: done %v answers %v", c2.Done(), mixed)
	}
}

// TestLineModeWordAnswersUnchanged pins that the line-mode word vocabulary
// (y/yes/n/no) still answers both gates exactly once, on the real clock and
// without the arm window: a typed word is a deliberate answer, so the
// console-paste delay must not apply to it. The mapping itself is pinned in
// linemode_test.go; this test drives the real reader into the real gate.
func TestLineModeWordAnswersUnchanged(t *testing.T) {
	for _, tc := range []struct {
		line     string
		wantKey  Key
		approved bool
	}{
		{"y\n", KeyYes, true},
		{"yes\n", KeyYes, true},
		{"n\n", KeyNo, false},
		{"no\n", KeyNo, false},
	} {
		var answers []bool
		ap := NewApproval("Approve registry write", nil, func(ok bool) { answers = append(answers, ok) })
		k, ok := NewLineKeys(strings.NewReader(tc.line)).ReadKey()
		if !ok || k != tc.wantKey {
			t.Fatalf("%q mapped to %q (ok %v), want %q", tc.line, k, ok, tc.wantKey)
		}
		if ap.Key(k, nil) {
			t.Fatalf("%q left the approval open", tc.line)
		}
		if !ap.Done() || ap.Approved() != tc.approved || len(answers) != 1 || answers[0] != tc.approved {
			t.Fatalf("%q: approved %v answers %v", tc.line, ap.Approved(), answers)
		}
	}
}

// TestLineModeGateAnswersOnTheRealClock is the real-clock degrade pin: a gate
// driven by a real NewLineKeys reader answers y/yes immediately and declines
// n/no/esc immediately, with no fake per-gate clock and no package clock
// stepping. The gate is constructed microseconds before the key is routed, so
// on any wall clock the delivery is inside the console arm window; being
// unpaced is what makes the answer immediate.
func TestLineModeGateAnswersOnTheRealClock(t *testing.T) {
	if d := time.Since(gateClock()); d.Abs() > time.Minute {
		t.Fatalf("the package gate clock is not the real clock: off by %v", d)
	}
	for _, tc := range []struct {
		line     string
		wantKey  Key
		approved bool
	}{
		{"y\n", KeyYes, true},
		{"yes\n", KeyYes, true},
		{"n\n", KeyNo, false},
		{"no\n", KeyNo, false},
		{"esc\n", KeyEsc, false},
	} {
		var answers []bool
		ap := NewApproval("Approve registry write", nil, func(ok bool) { answers = append(answers, ok) })
		if ap.arm.now != nil {
			t.Fatal("the real-clock pin must not install a per-gate clock")
		}
		k, ok := NewLineKeys(strings.NewReader(tc.line)).ReadKey()
		if !ok || k != tc.wantKey {
			t.Fatalf("%q mapped to %q (ok %v), want %q", tc.line, k, ok, tc.wantKey)
		}
		if ap.Key(k, nil) {
			t.Fatalf("%q left the approval open", tc.line)
		}
		if !ap.Done() || ap.Approved() != tc.approved || len(answers) != 1 || answers[0] != tc.approved {
			t.Fatalf("%q: approved %v answers %v", tc.line, ap.Approved(), answers)
		}
		if elapsed := time.Since(ap.arm.openedAt); elapsed < 0 || elapsed > time.Second {
			t.Fatalf("%q: the gate open stamp is not a real-clock instant: %v", tc.line, elapsed)
		}
	}

	// The M3 gate answers on the same clock, and Enter still never answers.
	var confirmed []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { confirmed = append(confirmed, ok) })
	if k, ok := NewLineKeys(strings.NewReader("y\n")).ReadKey(); !ok || k != KeyYes {
		t.Fatalf("y mapped to %q,%v", k, ok)
	} else if c.Key(k, nil) || !c.Approved() || len(confirmed) != 1 || !confirmed[0] {
		t.Fatalf("confirm y on the real clock: approved %v answers %v", c.Approved(), confirmed)
	}

	var open []bool
	g := NewConfirm("Delete provider?", nil, func(ok bool) { open = append(open, ok) })
	if k, ok := NewLineKeys(strings.NewReader("enter\n")).ReadKey(); !ok || k != KeyEnter {
		t.Fatalf("enter mapped to %q,%v", k, ok)
	} else if !g.Key(k, nil) || g.Done() || len(open) != 0 {
		t.Fatalf("Enter answered a line-mode gate: done %v answers %v", g.Done(), open)
	}
}

// TestGateArmWindowAppliesToConsolePacingOnly pins the pacing seam: the arm
// window is a console-paste mitigation only. A gate whose source never marks
// itself answers at zero elapsed time, strictly inside the window, so a leak in
// the seam shows up as a dropped answer; the same gate constructed the same way
// drops the answer once the console reader marks the delivery.
func TestGateArmWindowAppliesToConsolePacingOnly(t *testing.T) {
	// Unpaced: zero elapsed time, immediate answer.
	clk := newFakeGateClock()
	var plain []bool
	c := NewConfirm("Delete provider?", nil, func(ok bool) { plain = append(plain, ok) })
	armWith(&c.arm, clk)
	if c.Key(KeyYes, nil) || !c.Approved() || len(plain) != 1 || !plain[0] || c.arm.suppressed {
		t.Fatalf("unpaced gate: approved %v answers %v suppressed %v", c.Approved(), plain, c.arm.suppressed)
	}

	// Console-paced: the same zero elapsed time is inside the window.
	clk2 := newFakeGateClock()
	var paced []bool
	c2 := NewConfirm("Delete provider?", nil, func(ok bool) { paced = append(paced, ok) })
	armWith(&c2.arm, clk2)
	paceConsoleKey()
	if !c2.Key(KeyYes, nil) || c2.Done() || len(paced) != 0 || !c2.arm.suppressed {
		t.Fatalf("console-paced gate: done %v answers %v suppressed %v", c2.Done(), paced, c2.arm.suppressed)
	}
}

// TestApprovalInnocentKeysNeverCommitDroppedApprove pins the confirmed defect
// closed on the write gate itself: a paste remainder leaks a y into a fresh M4
// modal, and the operator's next keys (Enter to inspect the summary, Down to
// scroll, a digit) must not approve the providers.json write. Only the
// operator's own n denies it, exactly once.
func TestApprovalInnocentKeysNeverCommitDroppedApprove(t *testing.T) {
	var answers []bool
	clk := newFakeGateClock()
	ap := NewApproval("Approve registry write", []string{"+ provider acme"}, func(ok bool) { answers = append(answers, ok) })
	armWith(&ap.arm, clk)
	paceConsoleKey()

	if !ap.Key(RuneKey('y'), nil) || ap.Done() || !ap.arm.suppressed {
		t.Fatal("the leaked y was not dropped")
	}
	clk.advance(gateArmWindow)
	for _, k := range []Key{KeyEnter, KeyDown, Digit(2), KeySpace} {
		if !ap.Key(k, nil) || ap.Done() || ap.Approved() || len(answers) != 0 {
			t.Fatalf("%q committed the dropped approve: done %v approved %v answers %v", k, ap.Done(), ap.Approved(), answers)
		}
	}
	if ap.Key(KeyNo, nil) || ap.Approved() || len(answers) != 1 || answers[0] {
		t.Fatalf("the deliberate n did not deny: approved %v answers %v", ap.Approved(), answers)
	}
}

// TestGateFootersStateOtherKeysIgnored pins F-2: both legends state that every
// non-listed key is ignored, and the composed frame carries the clause. The
// legends stay English consts by chrome decision 6B, so no catalog key is
// added (the i18n parity tests are unaffected).
func TestGateFootersStateOtherKeysIgnored(t *testing.T) {
	const clause = "other keys ignored"
	c := NewConfirm("Delete provider?", nil, nil)
	ap := NewApproval("Approve registry write", nil, nil)
	if got, want := c.Footer(), "y confirm · n/Esc cancel · other keys ignored"; got != want {
		t.Fatalf("confirm footer = %q, want %q", got, want)
	}
	if got, want := ap.Footer(), "y approve write · n/Esc cancel · other keys ignored"; got != want {
		t.Fatalf("approval footer = %q, want %q", got, want)
	}

	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav})
	app.ShowModal(c)
	if frame := app.Frame().String(); !strings.Contains(frame, clause) {
		t.Fatalf("confirm gate frame lacks the clause:\n%s", frame)
	}
	app.ShowModal(ap)
	if frame := app.Frame().String(); !strings.Contains(frame, clause) {
		t.Fatalf("approval frame lacks the clause:\n%s", frame)
	}
}
