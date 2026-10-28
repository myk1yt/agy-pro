package swarm

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// FL-1/FL-7: seat source priority at the door layer. Chat/operator-named seats
// outrank the fanout preset everywhere, so the vector drops --seats ONLY when the
// spec carries no seats of its own and a preset pins a lineup. These tests assert
// the argv, which is the observable the delegated run consumes.

func lineupFixture(t *testing.T) (native, provider inventory.Entry) {
	t.Helper()
	return inventory.Entry{Number: 1, Kind: "native", ModelID: "gemini-a", Spec: "native:gemini-a"},
		inventory.Entry{Number: 2, Kind: "provider", ModelID: "m1", Spec: "prov/m1"}
}

func pinnedLineup() fanout.SeatLineup {
	return fanout.SeatLineup{
		PresetName: "nightly",
		Rung:       fanout.RungDefaultPreset,
		Raw:        "native:gemini-a,prov/m1",
		Specs: []fanout.SeatSpec{
			{Kind: fanout.SeatKindNative, Raw: "native:gemini-a", ModelID: "gemini-a"},
			{Kind: fanout.SeatKindProvider, Raw: "prov/m1", ModelID: "prov/m1"},
		},
	}
}

// flagValues returns the flag names of an argv in order, so an assertion failure
// shows the whole shape.
func flagValues(args []string) []string {
	out := make([]string, 0, len(args)/2)
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			out = append(out, a)
		}
	}
	return out
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// Case 1: the door holds seats AND a pin pins a lineup. The operator's seats win
// and are emitted verbatim; the pin is not consulted for substitution.
func TestRunSpecArgsKeepsOperatorSeatsWhenPinned(t *testing.T) {
	native, provider := lineupFixture(t)
	tmpl := Templates()[0] // code-review
	spec := RunSpec{
		Template:     tmpl,
		Seats:        []inventory.Entry{native, provider},
		Root:         "/ws",
		Prompt:       "Fix the bugs",
		TimeoutSec:   90,
		PresetLineup: pinnedLineup(),
	}
	if spec.AppliesPresetLineup() {
		t.Fatal("AppliesPresetLineup = true; operator-named seats must outrank the pin (FL-1)")
	}

	got := spec.Args()
	want := BuildFanoutArgs(tmpl, spec.Seats, "/ws", "Fix the bugs", 90)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q; want the operator's own seat vector %q", got, want)
	}
	if len(got) != 13 || got[2] != "--seats" || got[3] != "native:gemini-a,prov/m1" {
		t.Errorf("args = %q; want --task/--seats/native:gemini-a,prov/m1 ...", got)
	}
	// The --prompt-file form keeps them too.
	fileSpec := spec
	fileSpec.PromptFile = "prompt.txt"
	if !hasFlag(fileSpec.Args(), "--seats") {
		t.Errorf("args = %q; the file form must keep the operator's --seats", fileSpec.Args())
	}
}

// Case 2: no seats of this spec's own + a pinned lineup => the pair is omitted so
// the fanout ladder resolves the preset, in both prompt forms.
func TestRunSpecArgsAppliesPinWithoutSeats(t *testing.T) {
	tmpl := Templates()[0]
	spec := RunSpec{
		Template:     tmpl,
		Root:         "/ws",
		Prompt:       "Fix the bugs",
		TimeoutSec:   90,
		PresetLineup: pinnedLineup(),
	}
	if !spec.AppliesPresetLineup() {
		t.Fatal("AppliesPresetLineup = false; an empty spec with a pin must apply it")
	}
	got := spec.Args()
	if hasFlag(got, "--seats") {
		t.Errorf("args = %q; a pinned lineup with no seats must omit --seats", got)
	}
	want := []string{"--task", "--root", "--timeout", "--tools", "--prompt", "--json"}
	if strings.Join(flagValues(got), ",") != strings.Join(want, ",") {
		t.Fatalf("flag order = %q; want %q", flagValues(got), want)
	}
	if len(got) != 11 {
		t.Errorf("args = %q; want 11 elements (6 flag/value pairs plus --json)", got)
	}
	// The pinned specs are not smuggled into any flag value.
	if strings.Contains(strings.Join(got, " "), "prov/m1") {
		t.Errorf("args = %q; the pin must not be re-passed as a flag value", got)
	}

	fileSpec := spec
	fileSpec.PromptFile = "prompt.txt"
	fileGot := fileSpec.Args()
	if hasFlag(fileGot, "--seats") || !hasFlag(fileGot, "--prompt-file") || hasFlag(fileGot, "--prompt") {
		t.Errorf("args = %q; want the --seats-less --prompt-file form", fileGot)
	}
}

// Case 3: no pin, no seats => unchanged (the historical empty --seats pair).
func TestRunSpecArgsKeepsSeatsWithoutPresetLineup(t *testing.T) {
	native, provider := lineupFixture(t)
	tmpl := Templates()[0]
	spec := RunSpec{
		Template:   tmpl,
		Seats:      []inventory.Entry{native, provider},
		Root:       "/ws",
		Prompt:     "Fix the bugs",
		TimeoutSec: 90,
	}
	if spec.AppliesPresetLineup() {
		t.Fatal("AppliesPresetLineup must be false without a pin")
	}
	got := spec.Args()
	want := BuildFanoutArgs(tmpl, spec.Seats, "/ws", "Fix the bugs", 90)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q; want the explicit-seat vector %q", got, want)
	}

	// And with no seats either, the pre-U2a empty pair survives (it claims the
	// flag rung, which is the documented behavior of `--seats ""`).
	empty := RunSpec{Template: tmpl, Root: ".", Prompt: "p"}
	if args := empty.Args(); !hasFlag(args, "--seats") || args[3] != "" {
		t.Errorf("args = %q; want the historical empty --seats pair", args)
	}
}

// The four builders differ ONLY in the --seats pair and the prompt form.
func TestBuildFanoutArgsPresetLineupForms(t *testing.T) {
	tmpl := Templates()[0]
	inline := BuildFanoutArgsPresetLineup(tmpl, "/ws", "p", 90)
	file := BuildFanoutArgsFilePresetLineup(tmpl, "/ws", "prompt.txt", 90)
	for _, args := range [][]string{inline, file} {
		if hasFlag(args, "--seats") {
			t.Errorf("args = %q; want no --seats", args)
		}
		if args[0] != "--task" || args[len(args)-1] != "--json" {
			t.Errorf("args = %q; want the --task ... --json frame", args)
		}
	}
	if !hasFlag(inline, "--prompt") || hasFlag(inline, "--prompt-file") {
		t.Errorf("inline args = %q; want --prompt", inline)
	}
	if !hasFlag(file, "--prompt-file") || hasFlag(file, "--prompt") {
		t.Errorf("file args = %q; want --prompt-file", file)
	}
	// Defaults are the shared ones: root ".", timeout 300s.
	bare := BuildFanoutArgsPresetLineup(tmpl, "", "", 0)
	if bare[3] != "." || bare[5] != "300" {
		t.Errorf("bare args = %q; want root \".\" and timeout 300", bare)
	}
}

// The door reads the pin through the fanout store API, so an isolated AGY_PRO_HOME
// with a default_preset lineup is what flips the vector.
func TestResolveRunSpecLineupReadsTheStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv(fanout.EnvFanoutPreset, "")
	presets := `{"presets":[{"name":"nightly","seats":"native:gemini-a,prov/m1"}],"default_preset":"nightly"}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(presets), 0o600); err != nil {
		t.Fatal(err)
	}

	lineup, warning := ResolveRunSpecLineup()
	if warning != "" {
		t.Fatalf("warning = %q; want none for a resolvable pin", warning)
	}
	if !lineup.Pinned() {
		t.Fatalf("lineup = %+v; want the preset's pin", lineup)
	}
	if lineup.PresetName != "nightly" || !lineup.Standing() {
		t.Errorf("lineup = %+v; want the standing nightly origin", lineup)
	}
	if got := lineup.MeteredSeats(); got != 1 {
		t.Errorf("MeteredSeats = %d; want 1", got)
	}

	// A spec with NO seats echoes the --seats-less vector...
	spec := RunSpec{Template: Templates()[0], Root: ".", Prompt: "p", PresetLineup: lineup}
	if hasFlag(spec.Args(), "--seats") {
		t.Errorf("args = %q; want no --seats once the store pins a lineup and no seats were passed", spec.Args())
	}
	// ...and a spec WITH seats keeps its own (FL-1).
	native, provider := lineupFixture(t)
	withSeats := RunSpec{Template: Templates()[0], Seats: []inventory.Entry{native, provider}, Root: ".", Prompt: "p", PresetLineup: lineup}
	if !hasFlag(withSeats.Args(), "--seats") {
		t.Errorf("args = %q; operator seats must survive a pinned store lineup", withSeats.Args())
	}
}

// No preset / a preset that pins no lineup keeps the door's seats and warns
// nothing.
func TestResolveRunSpecLineupWithoutPin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv(fanout.EnvFanoutPreset, "")

	lineup, warning := ResolveRunSpecLineup()
	if lineup.Pinned() || warning != "" {
		t.Errorf("lineup = %+v warning = %q; want nothing pinned and no warning", lineup, warning)
	}

	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"),
		[]byte(`{"presets":[{"name":"plain","n":3}],"default_preset":"plain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lineup, warning = ResolveRunSpecLineup()
	if lineup.Pinned() || warning != "" {
		t.Errorf("a preset without seats must not pin: lineup = %+v warning = %q", lineup, warning)
	}
}

// A lookup failure yields no lineup plus ONE warning, and the door keeps its
// seats (the delegated run reports the same misconfiguration itself).
func TestResolveRunSpecLineupFailureKeepsSeats(t *testing.T) {
	orig := PresetSeatLineup
	t.Cleanup(func() { PresetSeatLineup = orig })
	PresetSeatLineup = func() (fanout.SeatLineup, error) {
		return fanout.SeatLineup{}, errors.New(`preset "ghost" not found`)
	}

	lineup, warning := ResolveRunSpecLineup()
	if lineup.Pinned() {
		t.Errorf("lineup = %+v; want no pin on a lookup error", lineup)
	}
	if !strings.Contains(warning, "ghost") || !strings.Contains(warning, "using the requested seats") {
		t.Errorf("warning = %q; want it to name the failure and the fallback", warning)
	}
}

// FL-1: the notice must never claim the preset supplies the lineup while the
// vector actually carries --seats.
func TestSeatLineupNoticeIsOnlyForTheAppliedCase(t *testing.T) {
	native, provider := lineupFixture(t)
	spec := RunSpec{Template: Templates()[0], Seats: []inventory.Entry{native, provider}, Root: ".", Prompt: "p", PresetLineup: pinnedLineup()}
	if spec.AppliesPresetLineup() {
		t.Error("AppliesPresetLineup must be false when the spec carries seats")
	}
	if !strings.Contains(SeatLineupNotice(pinnedLineup()), "no seats were passed") {
		t.Errorf("notice = %q; want the no-seats-passed wording", SeatLineupNotice(pinnedLineup()))
	}
	if strings.Contains(SeatLineupNotice(pinnedLineup()), "passing no --seats") {
		t.Errorf("notice must not use the old false phrasing")
	}
}

// The notice names the preset and the lineup, masks a key-shaped operand and
// strips control runes (FL-4).
func TestSeatLineupNoticeMasksOperands(t *testing.T) {
	notice := SeatLineupNotice(fanout.SeatLineup{PresetName: "nightly", Raw: "native:gemini-a,nw/glm"})
	if !strings.Contains(notice, `"nightly"`) || !strings.Contains(notice, "native:gemini-a,nw/glm") {
		t.Errorf("notice = %q; want the preset and its lineup", notice)
	}
	// F5 family: the lineup is file-sourced, so a planted credential shape in a
	// hand-edited preset must not ride this echo (MaskOperand is the house
	// masker for preset VALUES; the preset NAME takes MaskSlot).
	if masked := SeatLineupNotice(fanout.SeatLineup{PresetName: "nightly", Raw: "nw/" + testfixtures.AWSSecretExample}); strings.Contains(masked, testfixtures.AWSSecretExample) {
		t.Errorf("notice = %q; a key-shaped seat token must be masked", masked)
	}
	// FL-4: a control rune must never reach the terminal through this sink.
	planted := SeatLineupNotice(fanout.SeatLineup{PresetName: "night\x1b[2Jly", Raw: "native:a\x07"})
	for _, r := range planted {
		if r == 0x1b || r == 0x07 {
			t.Fatalf("notice carries a control rune: %q", planted)
		}
	}
}

// FL-5: the console surface must not hide a degraded / shrunken run. A clean run
// prints no footer (byte-identical to the pre-FL-5 table).
func TestRenderSummarySurfacesDegradedRunState(t *testing.T) {
	clean := fanout.SummaryReport{
		NRequested: 1,
		Seats: []fanout.SeatResult{{
			SeatID: 1, Kind: "provider",
			FinalTarget: providers.Target{Model: providers.Model{ID: "m1"}},
			Status:      "success",
		}},
	}
	var buf bytes.Buffer
	RenderSummary(&buf, clean, false)
	if want := "seat | kind | model | verdict\n1 | provider | m1 | success\n"; buf.String() != want {
		t.Errorf("clean run output drifted\n got: %q\nwant: %q", buf.String(), want)
	}

	lossy := clean
	lossy.NRequested = 3
	lossy.Degraded = true
	lossy.SkippedSeats = 2
	lossy.SkippedSeatSpecs = []string{"prov/gone", "prov/also-gone"}
	lossy.SeatLineup = &fanout.SeatLineupReport{Preset: "nightly", Rung: fanout.RungDefaultPreset, Seats: 3, MeteredSeat: 2, Standing: true}
	buf.Reset()
	RenderSummary(&buf, lossy, false)
	out := buf.String()
	for _, want := range []string{
		"run status: DEGRADED",
		"seats 1 / 3 requested",
		"skipped 2 (prov/gone, prov/also-gone)",
		`lineup from preset "nightly" (default_preset, standing approval)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("degraded footer %q missing %q", out, want)
		}
	}

	// An ad-hoc (named) preset is labelled as such, never as standing approval.
	lossy.SeatLineup = &fanout.SeatLineupReport{Preset: "adhoc", Rung: fanout.RungPresetFlag, Seats: 3, MeteredSeat: 2}
	buf.Reset()
	RenderSummary(&buf, lossy, false)
	if !strings.Contains(buf.String(), "(--preset, ad-hoc)") {
		t.Errorf("named preset must read as ad-hoc: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// R2-1 (P1 security): the underscore-head credential class (sk_live_...) is
// identity under MaskOperand, so every seat/model TOKEN echo on this package's
// surfaces takes the registry-slot authority (fanout.MaskSeatTokens).
// ---------------------------------------------------------------------------

// Sink: the door's lineup notice.
func TestSeatLineupNoticeMasksUnderscoreHeadTokens(t *testing.T) {
	lineup := fanout.SeatLineup{
		PresetName: "nightly",
		Rung:       fanout.RungDefaultPreset,
		Raw:        "native:gemini-a,nw/" + testfixtures.SKLive,
		Specs: []fanout.SeatSpec{
			{Kind: fanout.SeatKindNative, Raw: "native:gemini-a", ModelID: "gemini-a"},
			{Kind: fanout.SeatKindProvider, Raw: "nw/" + testfixtures.SKLive, ModelID: "nw/" + testfixtures.SKLive},
		},
	}
	notice := SeatLineupNotice(lineup)
	if strings.Contains(notice, testfixtures.SKLive) {
		t.Errorf("notice leaks the underscore-head token raw: %q", notice)
	}
	if !strings.Contains(notice, "native:gemini-a") {
		t.Errorf("benign seat must stay readable: %q", notice)
	}
	if !strings.Contains(notice, "no seats were passed") {
		t.Errorf("notice lost its wording: %q", notice)
	}
}

// Sink: the console run-status footer (and, through it, the skipped-seat list).
func TestRenderRunFooterMasksUnderscoreHeadSkippedSpecs(t *testing.T) {
	summary := fanout.SummaryReport{
		NRequested:       2,
		Degraded:         true,
		SkippedSeats:     1,
		SkippedSeatSpecs: []string{"nw/" + testfixtures.SKLive},
		Seats: []fanout.SeatResult{{
			SeatID: 1, Kind: "provider",
			FinalTarget: providers.Target{Model: providers.Model{ID: "m1"}},
			Status:      "success",
		}},
	}
	var buf bytes.Buffer
	RenderSummary(&buf, summary, false)
	out := buf.String()
	if strings.Contains(out, testfixtures.SKLive) {
		t.Errorf("footer leaks the underscore-head token raw: %q", out)
	}
	if !strings.Contains(out, "run status: DEGRADED") || !strings.Contains(out, "skipped 1") {
		t.Errorf("footer lost its content: %q", out)
	}
}
