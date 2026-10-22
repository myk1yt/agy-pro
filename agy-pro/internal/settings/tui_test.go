package settings

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/swarm"
)

func newTestTUI(t *testing.T) (*TUI, *fakeDeps, *bytes.Buffer) {
	t.Helper()
	isolatePanelModeDirs(t)
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
	for _, want := range []string{"Launch & Profile", "Models & Providers", "Bridge Tools", "Swarm", "Diagnostics", "Install & Update", "●"} {
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
	// P1-4: the read-only legend rides the footer and every read-only row
	// carries the ● ro dot, both before any press.
	if !strings.Contains(s.Footer, ReadOnlyLegend) {
		t.Errorf("footer missing the read-only legend %q:\n%s", ReadOnlyLegend, s.Footer)
	}
	if !strings.Contains(s.Right, "● "+ReadOnlyMarker) {
		t.Errorf("right pane missing the read-only dot on read-only rows:\n%s", s.Right)
	}
	// Numbered selection language on rows: the row index renders inside
	// brackets, right-aligned ("[ 1]" … "[10]"). P1-6 removed the number the
	// section title used to carry, so this pin is on the row index itself
	// (it was passing on the title's "[1]" before, not on a row).
	if !strings.Contains(s.Right, "[ 1]") || !strings.Contains(s.Right, "[10]") {
		t.Errorf("rows not numbered:\n%s", s.Right)
	}
}

// TestTUILegacyPanelDeprecationNotice (P1-7=(a)): the legacy panel names itself
// deprecated AND hands the reader the command that opens the maintained surface,
// on the frame every entry path renders. The notice is honest about the panel
// still working, so the pin asserts all three claims rather than the word
// "deprecated" alone: without the notice every string below is absent and the
// test fails (it is not vacuous against the pre-change frame).
func TestTUILegacyPanelDeprecationNotice(t *testing.T) {
	tui, _, out := newTestTUI(t)
	s := tui.Frame()
	for _, want := range []string{
		"DEPRECATED",
		"still works exactly as today",
		"deprecated",
		"new setting work lands in the setup hub",
	} {
		if !strings.Contains(s.Notice, want) {
			t.Errorf("frame notice missing %q:\n%s", want, s.Notice)
		}
	}
	// (b) routing: the notice carries the exact hub command, on the notice line
	// itself, not a pointer the reader has to resolve elsewhere.
	if !strings.Contains(s.Notice, "agy pro setup") {
		t.Errorf("frame notice does not name the hub command:\n%s", s.Notice)
	}
	if !strings.Contains(s.Notice, "Open the hub: "+legacyHubCommand) {
		t.Errorf("frame notice missing the hub routing line %q:\n%s", "Open the hub: "+legacyHubCommand, s.Notice)
	}

	// The notice is RENDERED, not just captured in the frame: a line-mode
	// reader (and the interactive frame) sees it above the section list.
	tui.Render(false)
	rendered := out.String()
	if !strings.Contains(rendered, LegacyPanelNotice) {
		t.Errorf("Render output missing the deprecation notice:\n%s", rendered)
	}
	if i, j := strings.Index(rendered, "agy-pro Settings - full-screen panel"), strings.Index(rendered, "Open the hub: "+legacyHubCommand); i < 0 || j < 0 || j < i {
		t.Errorf("the notice does not ride directly under the banner (banner=%d, notice=%d):\n%s", i, j, rendered)
	}

	// (c) behaviour is unchanged: the notice is presentation only. A press still
	// moves the cursor and the deep-link still selects its section.
	tui.Step(KeyDown)
	if tui.row != 1 {
		t.Errorf("the notice changed row navigation: row = %d; want 1", tui.row)
	}
	tui.SelectSection(4)
	if s := tui.Frame(); s.Notice != LegacyPanelNotice {
		t.Errorf("deep-linked frame lost the notice: %q", s.Notice)
	}
}

// legacyHubCommand is the hub command the notice routes to, spelled exactly as
// cmd/agy/main.go dispatches it (`case "setup":` under `agy pro <sub>`).
const legacyHubCommand = "agy pro setup"

// TestLegacyPanelNoticeRoutesToARealCommand: the notice names a command, so the
// command has to exist. This pins the wiring the notice advertises against the
// CLI dispatch itself, so renaming or dropping `agy pro setup` fails here
// instead of leaving the deprecated panel pointing at a dead surface.
func TestLegacyPanelNoticeRoutesToARealCommand(t *testing.T) {
	if !strings.Contains(LegacyPanelNotice, legacyHubCommand) {
		t.Fatalf("notice does not carry %q: %q", legacyHubCommand, LegacyPanelNotice)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agy", "main.go"))
	if err != nil {
		t.Fatalf("read cmd/agy/main.go: %v", err)
	}
	main := string(src)
	if !strings.Contains(main, `case "setup":`) {
		t.Error("`agy pro setup` is no longer dispatched by cmd/agy/main.go; the notice routes to a dead command")
	}
	if !strings.Contains(main, "runSetup(subArgs)") {
		t.Error("cmd/agy/main.go no longer routes setup to runSetup; the notice routes to a dead command")
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
//
// UPDATED by P5-1 U5b: the restore door now presents the deployment plan it
// authorizes (W15), and with no resolvable backup source it refuses before the
// gate instead of opening a body-less confirmation (see
// TestU5bModeRestoreGateRefusesWithoutASource). The pin seeds the one backup the
// plan needs, so the danger-gate property it was written for is asserted
// unchanged: activation gates, n cancels, y runs the op once.
func TestTUIDangerGate(t *testing.T) {
	tui, f, _ := newTestTUI(t)
	seedPanelBackup(t, map[string]string{"rules/AGENTS.md": "restored"})

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
//
// UPDATED by P5-1 U5b: the state.json scalar rows are gated writes now (F3(a)),
// so a numbered choice arms the exact-diff gate and the value lands on y. The
// property this pin was written for (numbered selection drives Choices) is kept
// and now asserted together with the gate in between.
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
	if tui.choices != nil {
		t.Error("choice mode not closed after selection")
	}
	if tui.pending == nil {
		t.Fatalf("the scalar choice did not arm the exact-diff gate: %q", tui.message)
	}
	if f.state.DefaultProfile != mode.ProfilePrompt {
		t.Errorf("the choice wrote before consent: %q", f.state.DefaultProfile)
	}
	if !strings.Contains(tui.message, "confirm to proceed") {
		t.Errorf("no consent message after choice: %q", tui.message)
	}
	if !strings.Contains(tui.Frame().Right, "default_profile: prompt -> native") {
		t.Errorf("gate body missing the exact diff:\n%s", tui.Frame().Right)
	}
	tui.Step(KeyYes)
	if f.state.DefaultProfile != mode.ProfileNative {
		t.Errorf("digit choice did not set the value after consent: %q", f.state.DefaultProfile)
	}
	if !strings.Contains(tui.message, "[SUCCESS]") {
		t.Errorf("no success message after the confirmed choice: %q", tui.message)
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
	for _, want := range []string{"agy-pro Settings", "Launch & Profile", "⎿", "↑↓ move"} {
		if !strings.Contains(captured, want) {
			t.Errorf("captured run missing %q", want)
		}
	}
	if strings.Contains(captured, "\x1b[") {
		t.Error("headless capture must not claim ANSI (non-TTY)")
	}
}

// TestTUIPromptOps: rename flows through the line prompts and the Core seam.
// UPDATED by P5-1 U5a: the write is gated (F3(a)), so the flow is prompts ->
// exact-diff gate -> y -> the same Core verb it always called.
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
	if tui.pending == nil {
		t.Fatal("rename must ask the exact-diff gate (F3(a))")
	}
	if len(f.reg.Providers) != 1 || f.reg.Providers[0].Name != "google" {
		t.Fatalf("rename applied before consent: %+v", f.reg.Providers)
	}
	tui.Step(KeyYes)
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

// newSwarmFileTUI builds the legacy panel over a real swarm-state.json at path,
// so the corrupt-file gate probes the same file-backed store the production
// Core wires (newTestTUI keeps the in-memory fake store, which has no file to
// overwrite and therefore no gate).
func newSwarmFileTUI(t *testing.T, path string) (*TUI, *swarm.FileStore, *bytes.Buffer) {
	t.Helper()
	isolatePanelModeDirs(t)
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	fs := &swarm.FileStore{Path: path}
	warn := &bytes.Buffer{}
	fs.WarnOut = warn
	deps := f.deps()
	deps.SwarmStore = fs
	c := NewCore(deps)
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	tui := NewTUI(c, view, &ScriptedKeys{}, &ScriptedLines{}, &bytes.Buffer{})
	return tui, fs, warn
}

// selectItemRow points the panel at the section holding itemID and returns its
// row index (the digit-jump/Enter activation surface).
func selectItemRow(t *testing.T, tui *TUI, itemID string) {
	t.Helper()
	for si, sec := range tui.view.Sections {
		for ri, it := range sec.Items {
			if it.ID == itemID {
				tui.section = si
				tui.row = ri
				return
			}
		}
	}
	t.Fatalf("item %q missing from the panel view", itemID)
}

// TestSwarmCorruptGateWordingMatchesCatalog pins the parity choice recorded in
// swarmCorruptGateBody's comment: the legacy panel copies the full-screen gate's
// sentence instead of inventing one, and the copy must stay byte-equal to the en
// catalog entry the hub surfaces resolve.
func TestSwarmCorruptGateWordingMatchesCatalog(t *testing.T) {
	b := i18n.Load("en")
	if got := b.T("presets.corrupt.body"); got != swarmCorruptGateBody {
		t.Fatalf("en presets.corrupt.body = %q, want the panel const %q", got, swarmCorruptGateBody)
	}
}

// TestTUISwarmSavePresetGatesCorruptStore pins the M-2 parity fix: the legacy
// panel used to append a preset over an unreadable swarm-state.json with no
// acknowledgment (the stderr warning was visible in line mode, the overwrite was
// not acknowledged). The write now refuses until the panel's own y/n gate is
// answered, naming the file, while the warning line still reaches the console
// sink.
func TestTUISwarmSavePresetGatesCorruptStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	const corrupt = "{not json"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	tui, fs, warn := newSwarmFileTUI(t, path)
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}

	tui.Step(KeyEnter)
	if tui.pending == nil || tui.pending.item.ID != "swarm.save_preset" {
		t.Fatalf("the corrupt store did not arm the acknowledgment gate: %+v", tui.pending)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != corrupt {
		t.Fatalf("the gate wrote the corrupt file before it was answered: %q (%v)", got, err)
	}
	right := tui.Frame().Right
	for _, want := range []string{"Confirm ", "[y/n]", "swarm-state.json is corrupt and was ignored", "Saving now overwrites it"} {
		if !strings.Contains(right, want) {
			t.Errorf("gate frame missing %q:\n%s", want, right)
		}
	}
	if !strings.Contains(warn.String(), "is corrupt; ignoring it") {
		t.Errorf("the store's stderr warning line is gone: %q", warn.String())
	}

	// n cancels: nothing is written and the file keeps its bytes.
	tui.Step(KeyNo)
	if tui.pending != nil {
		t.Fatal("n left the gate armed")
	}
	if tui.message != "cancelled" {
		t.Errorf("declined gate message = %q; want cancelled", tui.message)
	}
	if got, _ := os.ReadFile(path); string(got) != corrupt {
		t.Fatalf("a declined gate wrote the file: %q", got)
	}

	// y acknowledges the overwrite and performs exactly the one write.
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
	tui.Step(KeyEnter)
	if tui.pending == nil {
		t.Fatal("the second attempt skipped the corrupt gate")
	}
	tui.Step(KeyYes)
	presets, err := fs.LoadPresets()
	if err != nil {
		t.Fatalf("the approved gate left the file unreadable: %v", err)
	}
	if len(presets) != 1 || presets[0].Name != "deep-review" || presets[0].Numbers != "2,5" {
		t.Fatalf("on-disk presets = %+v; want deep-review=2,5", presets)
	}
	if !strings.HasPrefix(tui.message, "[SUCCESS]") {
		t.Errorf("approved gate message = %q; want the success stamp", tui.message)
	}
}

// TestTUISwarmSavePresetHealthyStoreConsentsAppend pins the W24 half of P5-1
// U5b: a healthy store (or none yet) used to append straight through with no
// prompt at all. The append is now presented as an exact diff and waits for y;
// the corrupt branch above keeps its overwrite acknowledgment on the same gate,
// so one answer can no longer cover only half of what the save does.
func TestTUISwarmSavePresetHealthyStoreConsentsAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	fs := &swarm.FileStore{Path: path}
	if err := fs.SavePresets([]swarm.SavedPreset{{Name: "existing", Numbers: "1"}}); err != nil {
		t.Fatal(err)
	}
	tui, _, _ := newSwarmFileTUI(t, path)
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}

	tui.Step(KeyEnter)
	if tui.pending == nil {
		t.Fatal("a healthy store appended without a consent gate")
	}
	if tui.pending.notice != "" {
		t.Errorf("a healthy store carried the corrupt-overwrite notice: %q", tui.pending.notice)
	}
	right := tui.Frame().Right
	for _, want := range []string{
		"Appends preset deep-review to swarm-state.json.",
		"presets before: 1, after: 2",
		"existing presets (kept byte-identical):",
		"  preset existing",
		"+ preset deep-review = 2,5",
		"swarm-state.json will be written (0600, atomic)",
	} {
		if !strings.Contains(right, want) {
			t.Errorf("append gate body missing %q:\n%s", want, right)
		}
	}
	if presets, err := fs.LoadPresets(); err != nil || len(presets) != 1 {
		t.Fatalf("the gate wrote before it was answered: %+v (%v)", presets, err)
	}

	// n cancels: the file keeps its one preset.
	tui.Step(KeyNo)
	if presets, err := fs.LoadPresets(); err != nil || len(presets) != 1 || presets[0].Name != "existing" {
		t.Fatalf("a declined append changed the file: %+v (%v)", presets, err)
	}

	// y appends the presented entry.
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
	tui.Step(KeyEnter)
	tui.Step(KeyYes)
	presets, err := fs.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if len(presets) != 2 || presets[1].Name != "deep-review" || presets[1].Numbers != "2,5" {
		t.Fatalf("on-disk presets = %+v; want existing + deep-review", presets)
	}
	if !strings.HasPrefix(tui.message, "[SUCCESS]") {
		t.Errorf("approved append message = %q; want the success stamp", tui.message)
	}
}

// panelRow returns the frame line of the right pane that renders itemID's row.
func panelRow(t *testing.T, tui *TUI, itemID string) string {
	t.Helper()
	label := ""
	for _, sec := range tui.view.Sections {
		for _, it := range sec.Items {
			if it.ID == itemID {
				label = it.Label
			}
		}
	}
	if label == "" {
		t.Fatalf("item %q missing from the panel view", itemID)
	}
	for _, ln := range strings.Split(tui.Frame().Right, "\n") {
		if strings.Contains(ln, label) {
			return ln
		}
	}
	t.Fatalf("panel renders no row for %q:\n%s", itemID, tui.Frame().Right)
	return ""
}

// TestLegacyPanelMarksHubOnlyExclusionRowReadOnly is S7-C6 on the deprecated
// line-mode panel: the panel has no activation arm for the model exclusion row
// (its only write surface is the hub), so the row must carry the shared
// read-only dot instead of the bare ● of an editable row, and pressing it must
// answer with the hub route rather than leaving the operator at a dead end.
func TestLegacyPanelMarksHubOnlyExclusionRowReadOnly(t *testing.T) {
	tui, f, _ := newTestTUI(t)
	selectItemRow(t, tui, "swarm.exclude_models")

	row := panelRow(t, tui, "swarm.exclude_models")
	if !strings.HasSuffix(strings.TrimRight(row, " "), "● "+ReadOnlyMarker) {
		t.Fatalf("panel row does not end with the shared read-only dot: %q", row)
	}
	// Non-vacuity: an editable row on the same frame carries the bare dot, so
	// the marker above is not printed for every row.
	if ctrl := panelRow(t, tui, "native_seat_concurrency"); strings.Contains(ctrl, ReadOnlyMarker) {
		t.Fatalf("an editable row also carries the read-only dot: %q", ctrl)
	}

	// Pressing the row is read-only in fact: no write arm, no confirm gate, and
	// the message names the one surface that can edit it.
	before := f.saved
	tui.Step(KeyEnter)
	if f.saved != before {
		t.Fatal("the panel activation wrote to a Core seam")
	}
	if tui.pending != nil || tui.choices != nil {
		t.Fatalf("the row armed a write surface: pending %v, choices %v", tui.pending, tui.choices)
	}
	if !strings.HasPrefix(tui.message, "read-only: ") {
		t.Fatalf("panel message = %q; want the read-only answer", tui.message)
	}
	if !strings.Contains(tui.message, "edit in the hub: agy pro setup") {
		t.Fatalf("panel message = %q; want the hub route", tui.message)
	}
	if strings.Contains(tui.message, "[SUCCESS]") {
		t.Fatalf("panel message claims a write: %q", tui.message)
	}

	// The detail pane carries the same route before any press.
	if !strings.Contains(tui.Frame().Right, "edit in the hub: agy pro setup") {
		t.Fatalf("detail pane lost the hub route:\n%s", tui.Frame().Right)
	}
}
