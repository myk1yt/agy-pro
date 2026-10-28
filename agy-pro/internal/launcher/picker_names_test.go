package launcher

import (
	"strings"
	"testing"

	"agypro/internal/swarm"
)

// --- F04: the picker accepts the same §2.7 grammar as the swarm seat prompt ---

// withStubPickerPresets points pickerSavedPresets at a fixture so the picker
// resolves names without reading the live ~/.agy-pro/swarm-state.json.
func withStubPickerPresets(t *testing.T, presets []swarm.SavedPreset) func() {
	t.Helper()
	old := pickerSavedPresets
	pickerSavedPresets = func() []swarm.SavedPreset { return presets }
	return func() { pickerSavedPresets = old }
}

// TestPickerResolvesSavedPresetName: a saved seat preset name answers the model
// prompt with the first seat of its stored Numbers, exactly like a p1/p2/p3
// answer already did, and the refusal wording on the grammar hint is the same
// as the swarm seat prompt's.
func TestPickerResolvesSavedPresetName(t *testing.T) {
	inv := pickerFixture()
	defer withStubPickerPresets(t, []swarm.SavedPreset{{Name: "deep", Numbers: "2,5"}})()

	var out strings.Builder
	pick := resolvePickerLine("deep", inv, &out)
	if !pick.Picked {
		t.Fatalf("a saved preset name must pick; output:\n%s", out.String())
	}
	if pick.Entry.Number != 2 {
		t.Errorf("deep picked seat %d, want seat 2 (its first stored number)", pick.Entry.Number)
	}
	if out.String() != "" {
		t.Errorf("a successful pick must stay silent: %q", out.String())
	}

	// Case-insensitive, matching the store's lookup rule.
	out.Reset()
	pick = resolvePickerLine("DEEP", inv, &out)
	if !pick.Picked || pick.Entry.Number != 2 {
		t.Errorf("DEEP picked %+v, want seat 2; output:\n%s", pick.Entry, out.String())
	}
}

// TestPickerUnknownNameIsNamedRefusal: an unknown name-shaped answer is refused
// with the swarm sentence (named reason plus the shared grammar hint) and the
// usual bridge fallback line, so the two surfaces cannot drift.
func TestPickerUnknownNameIsNamedRefusal(t *testing.T) {
	inv := pickerFixture()
	defer withStubPickerPresets(t, nil)()

	var out strings.Builder
	pick := resolvePickerLine("nosuch", inv, &out)
	if pick.Picked {
		t.Fatalf("an unknown name must not pick, got %+v", pick.Entry)
	}
	msg := out.String()
	for _, want := range []string{"unknown saved preset", "saved preset name", "continuing with External Models (bridge)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q:\n%s", want, msg)
		}
	}
}

// TestPickerGrammarHintMatchesSwarmWording pins the drift guard: the picker's
// "invalid selection" sentence renders the swarm hint verbatim, so the accepted
// grammar list is one string, not three copies.
func TestPickerGrammarHintMatchesSwarmWording(t *testing.T) {
	inv := pickerFixture()
	defer withStubPickerPresets(t, nil)()

	var out strings.Builder
	pick := resolvePickerLine("1.11.12.14", inv, &out)
	if pick.Picked {
		t.Fatalf("a dot-typed selection must not pick, got %+v", pick.Entry)
	}
	if want := swarm.SelectionGrammarHint(len(inv.Entries)); !strings.Contains(out.String(), want) {
		t.Errorf("picker hint drifted from the swarm hint\n got:\n%s\nwant substring: %q", out.String(), want)
	}
}
