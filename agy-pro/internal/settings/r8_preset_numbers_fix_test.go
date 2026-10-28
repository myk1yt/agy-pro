package settings

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// r8 P1-B pins (panel + file-seeded render half).
//
// The r7 F-01 wave grammar-gated the hub append door and masked the panel's
// diff body, but the legacy panel's save door (TUI.savePreset) still armed the
// consent for ANY non-empty numbers answer, so a misdirected paste reached
// Core.SwarmSavePresets. The gate now sits at the top of savePreset, with the
// hub's own catalog sentence copied verbatim (the panel has no bundle).

// TestSwarmNumbersInvalidWordingMatchesCatalog pins the parity choice recorded
// in swarmPresetNumbersInvalidBody's comment: the panel copies the hub's
// refusal sentence instead of inventing one, byte-equal to the en catalog
// entry, so a wording change on one surface cannot leave the other behind.
func TestSwarmNumbersInvalidWordingMatchesCatalog(t *testing.T) {
	b := i18n.Load("en")
	if got := b.T("settings.msg.preset_numbers_invalid"); got != swarmPresetNumbersInvalidBody {
		t.Fatalf("en settings.msg.preset_numbers_invalid = %q, want the panel const %q", got, swarmPresetNumbersInvalidBody)
	}
}

// TestTUISwarmSavePresetRefusesInvalidNumbers pins the panel write door's
// grammar gate: a planted numbers answer refuses BEFORE the consent arms, the
// refusal names the rule (never the value), and the store file keeps its bytes.
func TestTUISwarmSavePresetRefusesInvalidNumbers(t *testing.T) {
	const plant = testfixtures.SKLive
	const tail = "0123456789abcdef"
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	fs := &swarm.FileStore{Path: path}
	if err := fs.SavePresets([]swarm.SavedPreset{{Name: "existing", Numbers: "1"}}); err != nil {
		t.Fatal(err)
	}
	tui, fs, _ := newSwarmFileTUI(t, path)
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"r8good", plant}}

	tui.Step(KeyEnter)
	if tui.pending != nil {
		t.Fatal("the grammar refusal must not arm the consent gate")
	}
	msg := tui.message
	if !strings.Contains(msg, "numbers must be a numbered selection") {
		t.Fatalf("refusal message = %q; want the catalog sentence", msg)
	}
	if !strings.Contains(msg, "token 1") {
		t.Errorf("refusal must name the offending token position: %q", msg)
	}
	if strings.Contains(msg, plant) || strings.Contains(msg, tail) {
		t.Errorf("refusal echoes the plant: %q", msg)
	}
	if presets, err := fs.LoadPresets(); err != nil || len(presets) != 1 || presets[0].Name != "existing" {
		t.Fatalf("a refused save wrote the store: %+v (%v)", presets, err)
	}

	// A grammar-invalid NON-credential answer is refused the same way.
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"r8good", "enter"}}
	tui.Step(KeyEnter)
	if tui.pending != nil {
		t.Fatal("the grammar refusal must not arm the consent gate")
	}
	if msg := tui.message; !strings.Contains(msg, "numbers must be a numbered selection") {
		t.Fatalf("grammar refusal message = %q; want the catalog sentence", msg)
	}
	if presets, err := fs.LoadPresets(); err != nil || len(presets) != 1 {
		t.Fatalf("a refused save wrote the store: %+v (%v)", presets, err)
	}
}

// TestTUISwarmPresetsVerbMasksLegacySkLive pins the legacy panel's own render
// sink (the `swarm.presets` verb's ⎿ result lines, internal/settings/tui.go):
// a record that reached the in-memory list with a credential in Numbers (a
// store written before the grammar gate existed) renders masked, per token
// inside a mixed list, and benign "1,3" stays byte-identical.
func TestTUISwarmPresetsVerbMasksLegacySkLive(t *testing.T) {
	const plant = testfixtures.SKLive
	const tail = "0123456789abcdef"
	isolatePanelModeDirs(t)
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	deps := f.deps()
	deps.SwarmStore = presetSinkStore{presets: []swarm.SavedPreset{
		{Name: "legacy", Numbers: plant},
		{Name: "split", Numbers: "1," + plant},
		{Name: "quick", Numbers: "1,3"},
	}}
	c := NewCore(deps)
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	tui := NewTUI(c, view, &ScriptedKeys{}, &ScriptedLines{}, &bytes.Buffer{})
	selectItemRow(t, tui, "swarm.presets")
	tui.Step(KeyEnter)
	joined := strings.Join(tui.result, "\n")
	for _, want := range []string{"legacy = sk_li****", "split = 1,sk_li****", "quick = 1,3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel result missing %q\n%s", want, joined)
		}
	}
	for _, raw := range []string{plant, tail} {
		if strings.Contains(joined, raw) {
			t.Errorf("panel result echoes %q raw\n%s", raw, joined)
		}
	}
}

// TestSeededStateFileRendersMaskedEverywhere pins the display sinks end to end
// from a HAND-SEEDED swarm-state.json: the numbers plant is dropped by the
// load door (defense-in-depth, kept) with a masked warning that never echoes
// the value, a credential-shaped NAME carrying benign "1,3" survives and
// renders masked-name + byte-identical numbers, and no render (show,
// show --json, the legacy panel line) carries a raw plant byte.
func TestSeededStateFileRendersMaskedEverywhere(t *testing.T) {
	const plant = testfixtures.SKLive
	const tail = "0123456789abcdef"
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	raw := `{"version":1,"presets":[` +
		`{"name":"p9","numbers":"` + plant + `"},` +
		`{"name":"` + plant + `","numbers":"1,3"}` +
		`]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	isolatePanelModeDirs(t)
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	fs := &swarm.FileStore{Path: path}
	warn := &bytes.Buffer{}
	fs.WarnOut = warn
	deps := f.deps()
	deps.SwarmStore = fs
	c := NewCore(deps)

	// show (text snapshot) and show --json both serialize the same view value;
	// the first LoadPresets (inside the first View) is what drops the numbers
	// plant, so the warning buffer is read after this loop.
	for _, asJSON := range []bool{false, true} {
		var out bytes.Buffer
		if err := WriteSnapshot(&out, c, asJSON); err != nil {
			t.Fatalf("WriteSnapshot(json=%v): %v", asJSON, err)
		}
		snapshot := out.String()
		if strings.Contains(snapshot, plant) || strings.Contains(snapshot, tail) {
			t.Errorf("show (json=%v) echoes the plant raw:\n%s", asJSON, snapshot)
		}
		if !strings.Contains(snapshot, "sk_li****=1,3") {
			t.Errorf("show (json=%v) must render the surviving name masked with the benign numbers byte-identical:\n%s", asJSON, snapshot)
		}
		if !strings.Contains(snapshot, "p1/p2/p3") {
			t.Errorf("show (json=%v) lost the defaults note that stands in for the dropped record:\n%s", asJSON, snapshot)
		}
	}

	// The load door dropped the numbers plant with a masked warning: it names
	// the preset and the rule, never the stored value.
	w := warn.String()
	if !strings.Contains(w, `"p9"`) {
		t.Errorf("the drop warning must name the dropped preset: %q", w)
	}
	if strings.Contains(w, plant) || strings.Contains(w, tail) {
		t.Errorf("the drop warning echoes the stored value: %q", w)
	}

	// The legacy panel line renders the same view over the same seeded file.
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	tui := NewTUI(c, view, &ScriptedKeys{}, &ScriptedLines{}, &bytes.Buffer{})
	selectItemRow(t, tui, "swarm.presets")
	tui.Step(KeyEnter)
	joined := strings.Join(append([]string{tui.Frame().Right}, tui.result...), "\n")
	if strings.Contains(joined, plant) || strings.Contains(joined, tail) {
		t.Errorf("the legacy panel echoes the plant raw:\n%s", joined)
	}
	if !strings.Contains(joined, "sk_li****=1,3") {
		t.Errorf("the legacy panel must render the surviving record masked with benign numbers byte-identical:\n%s", joined)
	}
}
