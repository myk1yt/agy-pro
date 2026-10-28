package swarm

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// r3 masking wave (code lane), swarm side: the command echo (FormatCommand), the
// builder preview + copy paths, the saved-preset listing and the run footer all
// render seat/model tokens and registry slots through the registry-slot
// authority, because MaskOperand is the identity on the underscore-head
// credential class (`sk_live_...`).

// --- Item 2: FormatCommand ---------------------------------------------------

func TestFormatCommandMasksUnderscoreHeadSeatTokens(t *testing.T) {
	native, provider := lineupFixture(t)
	_ = native
	planted := inventory.Entry{Number: 2, Kind: "provider", ModelID: "nw/" + testfixtures.SKLive, Spec: "nw/" + testfixtures.SKLive}
	tmpl := Templates()[0]

	args := BuildFanoutArgs(tmpl, []inventory.Entry{provider, planted}, ".", "p", 300)
	line := FormatCommand(args)
	if strings.Contains(line, testfixtures.SKLive) {
		t.Errorf("command echo leaks the underscore-head seat token raw:\n%s", line)
	}
	if !strings.Contains(line, "--seats ") || !strings.Contains(line, "nw/sk****") {
		t.Errorf("command echo must carry the masked seat value:\n%s", line)
	}
	// The benign sibling token in the same value stays readable.
	if !strings.Contains(line, "prov/m1") {
		t.Errorf("benign seat token must stay readable:\n%s", line)
	}
	// Other arguments still take the operand masker: a benign prompt is unchanged
	// (a whitespace-free value is not quoted).
	if !strings.Contains(line, "--prompt p") {
		t.Errorf("benign prompt slot drifted:\n%s", line)
	}
}

// Byte identity for benign vectors: the pinned golden command line is unchanged
// (the same assertion shape the pre-existing tests use).
func TestFormatCommandBenignVectorByteStable(t *testing.T) {
	tmpl := Templates()[0]
	args := BuildFanoutArgs(tmpl, []inventory.Entry{
		{Number: 1, Kind: "native", ModelID: "gemini-a", Spec: "native:gemini-a"},
		{Number: 2, Kind: "provider", ModelID: "m1", Spec: "prov/m1"},
	}, "/ws", "Fix the bugs", 90)
	want := `agy pro fanout --task review --seats native:gemini-a,prov/m1 --root /ws --timeout 90 --tools read,search --prompt "Fix the bugs" --json`
	if got := FormatCommand(args); got != want {
		t.Errorf("benign command echo drifted\n got: %q\nwant: %q", got, want)
	}
}

// --- Item 2: the builder preview and the `c` copy line -----------------------

// A registry-planted model id picked in the builder must not reach the preview or
// the copy line raw (the surfaces an operator copies into a shell).
func TestBuilderPreviewAndCopyMaskUnderscoreHeadSeatToken(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: testfixtures.SKLive}}}})

	for _, tc := range []struct {
		name  string
		input string
	}{
		// template 1, then the numbered seat selection: the native row first, the
		// planted provider row next (inventory order).
		{name: "preview line", input: "1\n2\nq\n"},
		{name: "copy line", input: "1\n2\nc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			b := &Builder{In: strings.NewReader(tc.input), Out: &out, ErrW: &errOut, Inv: inv}
			_ = b.Run() // exit code depends on the flow; the surfaces are the assertion
			got := out.String()
			if strings.Contains(got, testfixtures.SKLive) {
				t.Errorf("builder surface leaks the underscore-head token raw:\n%s", got)
			}
			if !strings.Contains(got, "nw/sk****") {
				t.Errorf("builder surface must carry the masked seat token:\n%s", got)
			}
		})
	}
}

// --- Item 4 (LOW): the saved-preset listing --------------------------------

func TestBuilderSavedPresetNameUsesSlotAuthority(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})
	saved := []SavedPreset{{Name: "sk_live_0123456789abcdef", Numbers: "1"}}
	var out, errOut bytes.Buffer
	// "1\nq\n": template pick, then a seat-step line that is refused, so the run
	// ends at the prompt with the listing already rendered.
	b := &Builder{In: strings.NewReader("1\nq\n"), Out: &out, ErrW: &errOut, Inv: inv, Store: fakeStore(saved)}
	if code := b.Run(); code != 0 && code != 2 {
		t.Fatalf("builder exit = %d; stderr:\n%s", code, errOut.String())
	}
	got := out.String()
	if strings.Contains(got, "sk_live_0123456789abcdef") {
		t.Errorf("saved-preset listing leaks the underscore-head name raw:\n%s", got)
	}
	if !strings.Contains(got, "sk_li****") {
		t.Errorf("saved-preset name must render through the slot authority:\n%s", got)
	}
}

// fakeStore is the minimal Store the builder reads saved presets from.
type fakeStore []SavedPreset

func (f fakeStore) LoadPresets() ([]SavedPreset, error) { return f, nil }
func (f fakeStore) SavePresets([]SavedPreset) error     { return nil }

// --- Item 4 (LOW): the footer's rung render ---------------------------------

func TestRenderRunFooterStripsControlsFromRung(t *testing.T) {
	summary := fanout.SummaryReport{
		NRequested:       1,
		Degraded:         true,
		SeatLineup:       &fanout.SeatLineupReport{Preset: "nightly", Rung: "default_preset\x1b[2J", Seats: 1, Standing: true},
		SkippedSeats:     0,
		SkippedSeatSpecs: nil,
		Seats: []fanout.SeatResult{{
			SeatID: 1, Kind: "provider",
			FinalTarget: providers.Target{Model: providers.Model{ID: "m1"}},
			Status:      "success",
		}},
	}
	var buf bytes.Buffer
	RenderSummary(&buf, summary, false)
	for _, r := range buf.String() {
		if r == 0x1b {
			t.Fatalf("footer kept a control rune from the rung: %q", buf.String())
		}
	}
	if !strings.Contains(buf.String(), "lineup from preset \"nightly\" (default_preset") {
		t.Errorf("footer lost the rung: %q", buf.String())
	}
}

// --- Item 5 comment contract (compile-time only) ---------------------------
//
// The doc comments on SeatLineupNotice / seatSkipped / skippedSeatWarning now
// name the registry-slot authority the code actually calls. There is no runtime
// assertion for a comment; the P3 finding is closed by inspection and the
// grep-based check recorded in the report.

var _ = context.Background
