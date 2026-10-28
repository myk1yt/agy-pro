package settings

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/providers"
)

func newTestTUI(t *testing.T) (*TUI, *fakeDeps, *bytes.Buffer) {
	t.Helper()
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	f.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "google", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", APIKeyEnv: "GEMINI_API_KEY"},
	}}
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	out := &bytes.Buffer{}
	keys := &ScriptedKeys{}
	tui := NewTUI(c, view, keys, &ScriptedLines{}, out)
	return tui, f, out
}

// TestTUIFrameLayout (D3 headless capture): banner / left list / right pane /
// footer are all present, with the evidence-backed glyphs only.
func TestTUIFrameLayout(t *testing.T) {
	tui, _, _ := newTestTUI(t)
	s := tui.Frame()
	if !strings.Contains(s.Banner, "===") {
		t.Errorf("banner missing launcher-style === rule:\n%s", s.Banner)
	}
	if !strings.Contains(s.Banner, "agy-pro Settings") {
		t.Errorf("banner missing title:\n%s", s.Banner)
	}
	for _, want := range []string{"[1] Launch & Profile", "[2] Models & Providers", "[3] Bridge Tools", "[4] Swarm", "[5] Diagnostics", "[6] Install & Update", "●"} {
		if !strings.Contains(s.Left, want) {
			t.Errorf("left list missing %q:\n%s", want, s.Left)
		}
	}
	if !strings.Contains(s.Right, "⎿") {
		t.Errorf("right pane missing result glyph:\n%s", s.Right)
	}
	if !strings.Contains(s.Right, "value   :") || !strings.Contains(s.Right, "default :") || !strings.Contains(s.Right, "source  :") {
		t.Errorf("right pane missing detail fields:\n%s", s.Right)
	}
	if !strings.Contains(s.Footer, "↑↓") || !strings.Contains(s.Footer, "PgUp/PgDn") || !strings.Contains(s.Footer, "Esc") || !strings.Contains(s.Footer, "Ctrl+C") {
		t.Errorf("footer missing keybinding legend:\n%s", s.Footer)
	}
	// Numbered selection language on rows.
	if !strings.Contains(s.Right, "[1]") {
		t.Errorf("rows not numbered:\n%s", s.Right)
	}
}

// TestTUINavigation drives the keybinding seam: ↑↓ move, ←→ section switch,
// Esc back, digit jump (contract §2.9 keybindings).
func TestTUINavigation(t *testing.T) {
	tui, _, _ := newTestTUI(t)

	if tui.row != 0 || tui.section != 0 {
		t.Fatalf("initial cursor = (%d,%d)", tui.section, tui.row)
	}
	tui.Step(KeyDown)
	tui.Step(KeyDown)
	if tui.row != 2 {
		t.Errorf("after 2x down row = %d; want 2", tui.row)
	}
	tui.Step(KeyUp)
	if tui.row != 1 {
		t.Errorf("after up row = %d; want 1", tui.row)
	}
	for i := 0; i < 20; i++ {
		tui.Step(KeyDown) // walk into the clamp at the section's last row
	}
	last := tui.row
	tui.Step(KeyDown)
	if tui.row != last {
		t.Errorf("row walked past the section: %d -> %d", last, tui.row)
	}
	if last != len(tui.view.Sections[0].Items)-1 {
		t.Errorf("clamp row = %d; want %d", last, len(tui.view.Sections[0].Items)-1)
	}

	tui.Step(KeyRight)
	if tui.section != 1 || tui.row != 0 {
		t.Errorf("after right cursor = (%d,%d); want (1,0)", tui.section, tui.row)
	}
	tui.Step(KeyLeft)
	if tui.section != 0 {
		t.Errorf("after left section = %d; want 0", tui.section)
	}

	// PgDn/PgUp page by 10.
	tui.Step(KeyPgDn)
	if tui.row == 0 {
		t.Error("PgDn did not move the cursor")
	}
	tui.Step(KeyPgUp)
	if tui.row != 0 {
		t.Errorf("PgUp back to top = %d", tui.row)
	}

	// Digit jump selects a numbered row (numbered selection language).
	tui.Step(Key('3'))
	if tui.row != 2 {
		t.Errorf("digit jump to [3] left row = %d; want 2", tui.row)
	}

	// Esc backs out of a message, then exits at the top level.
	tui.message = "something"
	if !tui.Step(KeyEsc) {
		t.Error("Esc with a message must not exit")
	}
	if tui.message != "" {
		t.Errorf("Esc did not clear the message: %q", tui.message)
	}
	if tui.Step(KeyEsc) {
		t.Error("Esc at the top level must exit")
	}

	// Ctrl+C exits.
	tui2, _, _ := newTestTUI(t)
	if tui2.Step(KeyCtrlC) {
		t.Error("Ctrl+C must exit")
	}
}

// TestTUIDangerGate: destructive ops require a y/n confirmation (contract
// §2.9 Danger items; research §3.1 confirmations).
func TestTUIDangerGate(t *testing.T) {
	tui, f, _ := newTestTUI(t)

	// Jump to P1's mode.restore item (danger) by item id.
	var idx int
	for i, it := range tui.view.Sections[0].Items {
		if it.ID == "mode.restore" {
			idx = i
		}
	}
	tui.row = idx
	tui.Step(KeyEnter)
	if tui.pending == nil {
		t.Fatal("danger item activated without a confirmation gate")
	}
	// 'n' cancels: the restore op must not run.
	tui.Step(KeyNo)
	if tui.pending != nil {
		t.Error("n did not clear the pending confirmation")
	}
	if f.restored != 0 {
		t.Error("cancelled danger op still ran the restore")
	}

	// 'y' runs the op through the seam.
	tui.row = idx
	tui.Step(KeyEnter)
	if tui.pending == nil {
		t.Fatal("second activation lost the gate")
	}
	tui.Step(KeyYes)
	if tui.pending != nil {
		t.Error("y did not resolve the confirmation")
	}
	if f.restored != 1 {
		t.Errorf("confirmed restore ran %d times; want 1", f.restored)
	}
}

// TestTUIChoiceSelection: Choices use numbered selection (digits / Enter).
func TestTUIChoiceSelection(t *testing.T) {
	tui, f, _ := newTestTUI(t)

	// default_profile is row 0 of P1 and carries Choices.
	tui.row = 0
	tui.Step(KeyEnter)
	if tui.choices == nil {
		t.Fatal("choice-bearing item did not enter choice mode")
	}
	s := tui.Frame()
	if !strings.Contains(s.Right, "Choices (numbered selection)") {
		t.Errorf("choice list not rendered:\n%s", s.Right)
	}
	tui.Step(Key('2')) // second choice = native
	if f.state.DefaultProfile != mode.ProfileNative {
		t.Errorf("digit choice did not set the value: %q", f.state.DefaultProfile)
	}
	if tui.choices != nil {
		t.Error("choice mode not closed after selection")
	}
	if !strings.Contains(tui.message, "[SUCCESS]") {
		t.Errorf("no success message after choice: %q", tui.message)
	}
}

// TestTUICaptureHeadless is the D3 headless capture: a full scripted run
// produces coherent frames without a live console. Glyph parity with the
// native overlay stays UNVERIFIED (research §3.3) and is not claimed here.
func TestTUICaptureHeadless(t *testing.T) {
	tui, _, out := newTestTUI(t)
	tui.keys = &ScriptedKeys{Keys: []Key{KeyDown, KeyDown, KeyRight, KeyLeft, KeyCtrlC}}
	captured := captureRun(tui)
	if out.Len() == 0 && captured == "" {
		t.Fatal("no frames captured")
	}
	for _, want := range []string{"agy-pro Settings", "[1] Launch & Profile", "⎿", "↑↓ move"} {
		if !strings.Contains(captured, want) {
			t.Errorf("captured run missing %q", want)
		}
	}
	if strings.Contains(captured, "\x1b[") {
		t.Error("headless capture must not claim ANSI (non-TTY)")
	}
}

// TestTUIPromptOps: rename flows through the line prompt and the Core seam.
func TestTUIPromptOps(t *testing.T) {
	tui, f, _ := newTestTUI(t)
	// Navigate to P2 (Models & Providers) and find providers.rename.
	tui.section = 1
	var idx int
	found := false
	for i, it := range tui.view.Sections[1].Items {
		if it.ID == "providers.rename" {
			idx = i
			found = true
		}
	}
	if !found {
		t.Fatal("providers.rename missing from P2")
	}
	tui.lines = &ScriptedLines{Lines: []string{"google", "gemini"}}
	tui.row = idx
	tui.Step(KeyEnter)
	if tui.pending != nil {
		t.Fatal("rename should not ask a danger gate")
	}
	if len(f.reg.Providers) != 1 || f.reg.Providers[0].Name != "gemini" {
		t.Errorf("rename did not apply: %+v", f.reg.Providers)
	}
	if !strings.Contains(tui.message, "[SUCCESS]") {
		t.Errorf("rename message = %q", tui.message)
	}
}

// TestLineModeKeys: the non-console degrade maps text lines to the same Key
// set (contract §2.9 line-mode fallback).
func TestLineModeKeys(t *testing.T) {
	script := strings.NewReader("up\ndown\nleft\nright\npgup\npgdn\nesc\nenter\ny\nn\n3\nquit\n")
	lk := &lineModeKeys{r: bufio.NewReader(script)}
	want := []Key{KeyUp, KeyDown, KeyLeft, KeyRight, KeyPgUp, KeyPgDn, KeyEsc, KeyEnter, KeyYes, KeyNo, Key('3'), KeyCtrlC}
	for i, w := range want {
		k, ok := lk.ReadKey()
		if !ok || k != w {
			t.Fatalf("key %d = (%q,%v); want %q", i, k, ok, w)
		}
	}
	if _, ok := lk.ReadKey(); ok {
		t.Error("exhausted script must report no key")
	}
}

// TestRunTUILineModeFallback: a non-console RunTUI drives the same panel loop
// through line-mode keys and exits cleanly.
func TestRunTUILineModeFallback(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	out := &bytes.Buffer{}
	code := RunTUI(c, strings.NewReader("down\nquit\n"), out, "")
	if code != 0 {
		t.Fatalf("RunTUI line-mode exit = %d", code)
	}
	got := out.String()
	for _, want := range []string{"Line-mode fallback", "agy-pro Settings", "↑↓ move"} {
		if !strings.Contains(got, want) {
			t.Errorf("line-mode output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Error("line-mode must not emit ANSI (C3)")
	}
}
