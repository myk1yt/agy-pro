package settings

import (
	"strings"
	"testing"

	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// r7 F-01 (display arm), settings package: SavedPreset.Numbers is §2.7 selection
// text by grammar, but a LEGACY or force-written record can still carry a
// credential, so every renderer in this package routes the field through the
// seat-token authority. These pins force such a record past the grammar gate (the
// struct literal is the legacy path) and assert masked-ness plus benign
// byte-identity - masked-ness, not a mask spelling, per the lane's D-3 policy.

// TestSwarmPresetsValueMasksUnderscoreHeadNumbers pins the swarm.presets row.
func TestSwarmPresetsValueMasksUnderscoreHeadNumbers(t *testing.T) {
	const plant = testfixtures.SKLive
	view := viewOverPresets(t, []swarm.SavedPreset{
		{Name: "legacy", Numbers: plant},
		{Name: "split", Numbers: "1," + plant},
		{Name: "benign", Numbers: "1,3"},
	})
	it, ok := view.FindItem("swarm.presets")
	if !ok {
		t.Fatal("swarm.presets row missing from the view")
	}
	if strings.Contains(it.Value, plant) {
		t.Errorf("swarm.presets row echoes the numbers plant raw: %q", it.Value)
	}
	if !strings.Contains(it.Value, "legacy=sk_li****") {
		t.Errorf("numbers plant must render through the seat-token authority: %q", it.Value)
	}
	// Per token: the benign sibling in a mixed list keeps its bytes.
	if !strings.Contains(it.Value, "split=1,sk_li****") {
		t.Errorf("per-token masking drifted: %q", it.Value)
	}
	// Benign records are byte-identical.
	if !strings.Contains(it.Value, "benign=1,3") {
		t.Errorf("benign numbers drifted: %q", it.Value)
	}
}

// TestPanelPresetAddedLineMasksUnderscoreHeadNumbers pins the panel's exact-diff
// added line (the gate body an operator approves before the write).
func TestPanelPresetAddedLineMasksUnderscoreHeadNumbers(t *testing.T) {
	const plant = testfixtures.SKLive
	presets := []swarm.SavedPreset{{Name: "quick", Numbers: "1,3"}}
	// panelPresetPlanFor(...).diff() is the plan's exact diff; the added line is
	// the one that carries the new record's numbers.
	joined := strings.Join(panelPresetPlanFor(presets, "legacy", plant).diff(), "\n")
	if strings.Contains(joined, plant) {
		t.Errorf("panel diff echoes the numbers plant raw:\n%s", joined)
	}
	if !strings.Contains(joined, "legacy") || !strings.Contains(joined, "****") {
		t.Errorf("panel diff must name the preset and mask its numbers:\n%s", joined)
	}
	// Benign numbers render byte-identically.
	benign := strings.Join(panelPresetPlanFor(presets, "nightly", "2,5").diff(), "\n")
	if !strings.Contains(benign, "2,5") || strings.Contains(benign, "****") {
		t.Errorf("benign numbers drifted:\n%s", benign)
	}
}
