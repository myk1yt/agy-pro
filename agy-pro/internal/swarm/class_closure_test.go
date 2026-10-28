package swarm

import (
	"bytes"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// Class-closure sweep (r4), swarm side: every seat/models-token sink in this
// package now renders through the registry-slot authority (fanout.MaskSeatToken /
// MaskSeatTokens), and one pin guards the other direction — the vector the runner
// EXECUTES must stay raw (masking is display-only).

// H-2 (#1/#2): the saved-preset name refusals quote a token.
func TestExpandSavedPresetNamesMasksUnderscoreHeadToken(t *testing.T) {
	plant := testfixtures.SKLive
	// A saved preset whose stored Numbers is empty: the refusal quotes the token.
	presets := []SavedPreset{{Name: plant, Numbers: "  "}}
	_, err := ExpandSavedPresetNames(plant, presets)
	if err == nil {
		t.Fatal("an empty-Numbers preset must be refused")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("refusal leaks the token raw: %v", err)
	}
	if !strings.Contains(err.Error(), "****") {
		t.Errorf("refusal must render the token masked: %v", err)
	}

	// Unknown saved preset: same slot, same authority.
	_, err2 := ExpandSavedPresetNames(plant, nil)
	if err2 == nil {
		t.Fatal("an unknown saved preset name must be refused")
	}
	if strings.Contains(err2.Error(), plant) {
		t.Errorf("unknown-preset refusal leaks the token raw: %v", err2)
	}
	// Benign selection syntax stays byte-identical.
	got, gerr := ExpandSavedPresetNames("1,3-5,p2", nil)
	if gerr != nil || got != "1,3-5,p2" {
		t.Errorf("benign selection drifted: %q, %v", got, gerr)
	}
}

// H-2 (#3): the nil-inventory refusal quotes the raw selection line.
func TestResolveSeatsNilInventoryMasksToken(t *testing.T) {
	plant := testfixtures.SKLive
	_, err := ResolveSeatsWith(plant, nil, nil, nil)
	if err == nil {
		t.Fatal("nil inventory must error")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("nil-inventory refusal leaks the token raw: %v", err)
	}
	if !strings.Contains(err.Error(), "****") {
		t.Errorf("nil-inventory refusal must be masked: %v", err)
	}
}

// H-2 (#4): the two warning sentence bodies.
func TestSeatWarningsMaskUnderscoreHeadSpecs(t *testing.T) {
	plant := testfixtures.SKLive
	entry := inventory.Entry{Number: 1, Kind: "provider", ModelID: plant, Spec: "nw/" + plant}

	var skipped bytes.Buffer
	warnSkippedSeat(&skipped, entry)
	if strings.Contains(skipped.String(), plant) {
		t.Errorf("skipped-seat warning leaks the token raw: %q", skipped.String())
	}
	if !strings.Contains(skipped.String(), "****") {
		t.Errorf("skipped-seat warning must be masked: %q", skipped.String())
	}

	var excluded bytes.Buffer
	WarnExcludedSeat(&excluded, 1, "nw/"+plant, plant)
	if strings.Contains(excluded.String(), plant) {
		t.Errorf("excluded-pick warning leaks the token raw: %q", excluded.String())
	}
	// Benign specs stay readable in the same sentence.
	var benign bytes.Buffer
	WarnExcludedSeat(&benign, 2, "nw/glm-5.3-flash", "glm-5.3-flash")
	if !strings.Contains(benign.String(), "nw/glm-5.3-flash") || !strings.Contains(benign.String(), "explicit pick honored") {
		t.Errorf("benign warning drifted: %q", benign.String())
	}
}

// Class closure at the door boundary: the interactive seat-step print wraps the
// selection error's quoted spans.
func TestBuilderSeatStepMasksQuotedSelectionToken(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})
	plant := testfixtures.SKLive
	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader("1\n" + plant + "\n"), Out: &out, ErrW: &errOut, Inv: inv}
	_ = b.Run()
	if strings.Contains(errOut.String(), plant) {
		t.Errorf("seat step leaks the underscore-head selection token raw:\n%s", errOut.String())
	}
}

// L4 pin (r4): masking is DISPLAY only. The vector handed to the runner still
// carries the raw token, because a masked value cannot resolve in the registry.
func TestExecutedVectorStaysRawWhileEchoMasks(t *testing.T) {
	const plant = "sk_live_0123456789abcdef"
	token := "nw/" + plant
	native := inventory.Entry{Number: 1, Kind: "native", ModelID: "gemini-a", Spec: "native:gemini-a"}
	entry := inventory.Entry{Number: 2, Kind: "provider", ModelID: plant, Spec: token}
	tmpl := Templates()[0]
	spec := RunSpec{Template: tmpl, Seats: []inventory.Entry{native, entry}, Root: ".", Prompt: "p", TimeoutSec: 300}

	// The executed vector (what RunFanout receives) is raw.
	executed := spec.Args()
	joined := strings.Join(executed, " ")
	if !strings.Contains(joined, token) {
		t.Fatalf("executed vector lost the raw token (overmasking): %q", joined)
	}
	for _, a := range executed {
		if strings.Contains(a, "****") {
			t.Fatalf("executed vector carries a MASKED argument: %q", executed)
		}
	}

	// The display rendering of the SAME vector is masked.
	echo := FormatCommand(executed)
	if strings.Contains(echo, plant) {
		t.Errorf("display echo leaks the token raw: %q", echo)
	}
	if !strings.Contains(echo, "nw/sk****") {
		t.Errorf("display echo must be masked: %q", echo)
	}
}
