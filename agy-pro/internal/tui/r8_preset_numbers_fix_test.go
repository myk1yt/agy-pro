package tui

import (
	"strings"
	"testing"

	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// r8 P1-B pins (hub half).
//
// The r7 F-01 wave grammar-gated the hub APPEND door and masked the display
// operands, but two seams stayed open:
//
//   - the hub EDIT door (settingsPresetsScreen.editArmed) armed the M4 consent
//     for ANY non-empty answer, so a misdirected paste rewrote swarm-state.json
//     with numbers the §2.7 selection expander would reject;
//   - the management-screen row composer (settingsPresetsScreen.rebuild) rendered
//     Numbers through providers.MaskOperand, which is the IDENTITY on the
//     underscore-head credential class: a legacy record (hand-edited store, or a
//     writer predating the grammar) rode the listing raw. The ghp_-class plant
//     the r7 pin used masked under MaskOperand, which is why that pin stayed
//     green while sk_live leaked.
//
// These tests plant testfixtures.SKLive through both seams.

// TestSettingsSwarmEditRefusesInvalidNumbers pins the edit door's grammar gate:
// a planted answer is refused by the rule, the M4 gate never arms, the refusal
// echoes neither the plant nor its tail, and the store keeps its bytes.
func TestSettingsSwarmEditRefusesInvalidNumbers(t *testing.T) {
	SetLanguage("en")
	const plant = testfixtures.SKLive
	const tail = "0123456789abcdef"
	h := newSettingsTestCore(t)
	h.presets = []swarm.SavedPreset{{Name: "quick", Numbers: "1,3"}}
	app := newSettingsApp(h)
	submitSwarmEdit(t, app, 1, "1,3", plant)

	if app.Modal() != nil {
		t.Fatal("the edit write gate must not open for numbers the grammar refuses")
	}
	msg := app.Message()
	if !strings.Contains(msg, "numbered selection") {
		t.Fatalf("refusal message = %q; want the numbers-grammar sentence", msg)
	}
	if strings.Contains(msg, plant) || strings.Contains(msg, tail) {
		t.Errorf("refusal echoes the plant: %q", msg)
	}
	if len(h.presets) != 1 || h.presets[0].Numbers != "1,3" {
		t.Fatalf("a refused edit wrote the store: %+v", h.presets)
	}

	// A grammar-invalid NON-credential answer is refused the same way (the gate
	// is the grammar, not the secret shape). Fresh app: the first refusal left
	// the management screen on top, and the row-opening helper starts from the
	// settings screen.
	h = newSettingsTestCore(t)
	h.presets = []swarm.SavedPreset{{Name: "quick", Numbers: "1,3"}}
	app = newSettingsApp(h)
	submitSwarmEdit(t, app, 1, "1,3", "enter")
	if app.Modal() != nil {
		t.Fatal("the edit write gate must not open for grammar-invalid numbers")
	}
	if msg := app.Message(); !strings.Contains(msg, "numbered selection") {
		t.Fatalf("grammar refusal message = %q; want the numbers-grammar sentence", msg)
	}
	if len(h.presets) != 1 || h.presets[0].Numbers != "1,3" {
		t.Fatalf("a refused edit wrote the store: %+v", h.presets)
	}
}

// TestSettingsPresetHubRowMasksSkLiveNumbers pins the display arm for the row
// composer: a LEGACY record carrying the underscore-head plant renders masked,
// per token inside a mixed list, and benign "1,3" stays byte-identical.
func TestSettingsPresetHubRowMasksSkLiveNumbers(t *testing.T) {
	SetLanguage("en")
	const plant = testfixtures.SKLive
	const tail = "0123456789abcdef"
	h := newSettingsTestCore(t)
	h.presets = []swarm.SavedPreset{
		{Name: "legacy", Numbers: plant},
		{Name: "split", Numbers: "1," + plant},
		{Name: "quick", Numbers: "1,3"},
	}
	app := newSettingsApp(h)
	openSettingsRow(t, app, "swarm.presets")
	if _, ok := app.Top().(*settingsPresetsScreen); !ok {
		t.Fatalf("preset listing did not push the management screen: %T", app.Top())
	}
	frame := app.Frame().String()
	for _, want := range []string{
		"legacy = sk_li****",
		"split = 1,sk_li****",
		"quick = 1,3",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("preset row missing %q\n%s", want, frame)
		}
	}
	for _, raw := range []string{plant, tail} {
		if strings.Contains(frame, raw) {
			t.Errorf("preset row echoes %q raw\n%s", raw, frame)
		}
	}
}
