package tui

import (
	"fmt"
	"testing"
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
	if app.Step(KeyEsc) {
		t.Fatal("Esc at the root must exit")
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
