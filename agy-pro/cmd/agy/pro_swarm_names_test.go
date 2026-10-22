package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// --- F04 saved-preset NAME selectability on the --seats flag path ---

// withStubSwarmPresets points loadSwarmPresets at a fixture so the flag path
// resolves names without reading the live ~/.agy-pro/swarm-state.json.
func withStubSwarmPresets(t *testing.T, presets []swarm.SavedPreset) func() {
	t.Helper()
	old := loadSwarmPresets
	loadSwarmPresets = func() []swarm.SavedPreset { return presets }
	return func() { loadSwarmPresets = old }
}

// TestResolveFlagSeatsResolvesSavedPresetName: `--seats <saved name>` is
// selection input (F04) and seats exactly the preset's stored Numbers.
func TestResolveFlagSeatsResolvesSavedPresetName(t *testing.T) {
	inv := swarmFixtureInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()
	defer withStubSwarmPresets(t, []swarm.SavedPreset{{Name: "deep", Numbers: "1,2"}})()

	var errOut bytes.Buffer
	entries, code := resolveFlagSeats("deep", &errOut)
	if code >= 0 {
		t.Fatalf("resolveFlagSeats(deep) exit = %d, want success; stderr:\n%s", code, errOut.String())
	}
	if len(entries) != 2 {
		t.Fatalf("deep seated %d entries, want 2 (its numbers 1,2): %+v", len(entries), entries)
	}
	if entries[0].Number != 1 || entries[1].Number != 2 {
		t.Errorf("deep seated %+v, want seats 1 and 2", entries)
	}
}

// TestResolveFlagSeatsUnknownNameStaysSeatSpec is the precedence pin: a token
// that names no saved preset keeps the pre-F04 reading, so the mode switch
// cannot hijack the documented seat-spec path (`--seats prov/model`,
// `native:id`, and a bare token that ParseSeatSpecs accepts). The name-shaped
// refusal fires on the surfaces that are already known to be selections: the
// interactive builder's seat step (TestBuilderSeatStepResolvesSavedPresetName)
// and the launcher picker (TestPickerResolvesSavedPresetName).
func TestResolveFlagSeatsUnknownNameStaysSeatSpec(t *testing.T) {
	inv := swarmFixtureInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()
	defer withStubSwarmPresets(t, []swarm.SavedPreset{{Name: "deep", Numbers: "1,2"}})()
	// The spec path reads the exclusion policy through the seam; pin the
	// built-in default so the verdict never depends on the live registry.
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()

	var errOut bytes.Buffer
	entries, code := resolveFlagSeats("unknownmodel", &errOut)
	if code >= 0 {
		t.Fatalf("a bare unknown token must stay a seat spec; exit = %d, stderr:\n%s", code, errOut.String())
	}
	if len(entries) != 1 || entries[0].ModelID != "unknownmodel" {
		t.Errorf("bare token resolved to %+v, want one provider seat spec 'unknownmodel'", entries)
	}

	// Spec spellings are untouched even when the line also holds a name-shaped
	// token (pre-F04 mixed-line precedence, unchanged on purpose).
	errOut = bytes.Buffer{}
	entries, code = resolveFlagSeats("native:gemini-3.8-flash-high,unknownmodel", &errOut)
	if code >= 0 {
		t.Fatalf("a native:/bare spec line must stay a spec line; exit = %d, stderr:\n%s", code, errOut.String())
	}
	if len(entries) != 2 {
		t.Errorf("spec line seated %d entries, want 2: %+v", len(entries), entries)
	}
}

// TestResolveFlagSeatsSavedNameErrorsAreMasked: a key-shaped stored Numbers that
// the expansion surfaces (an out-of-range seat token) must not reach stderr raw,
// and a benign one must stay readable.
func TestResolveFlagSeatsSavedNameErrorsAreMasked(t *testing.T) {
	inv := swarmFixtureInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()

	// Numbers far outside this inventory: the error quotes the number token
	// from the state file, which is the value SelectionSeeds has to cover.
	// The key-shaped plant comes from the shared fixture contract, so no
	// contiguous provider-key literal lives in the tracked tree.
	plant := testfixtures.AWSSecretNoSlash
	defer withStubSwarmPresets(t, []swarm.SavedPreset{{Name: "planted", Numbers: plant}})()

	var errOut bytes.Buffer
	_, code := resolveFlagSeats("planted", &errOut)
	if code != 2 {
		t.Fatalf("out-of-range stored numbers exit = %d, want 2; stderr:\n%s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), plant) {
		t.Errorf("stored Numbers echoed raw:\n%s", errOut.String())
	}
}

// withStubSwarmPolicy points loadSwarmPolicy at a fixture so the explicit
// seat-spec path's exclusion verdict never depends on the live
// ~/.agy-pro/providers.json. The stub reports a HEALTHY read, so no
// exclusion-unread notice is involved (FIX-G F1's notices are pinned against
// the real seam in pro_swarm_test.go).
func withStubSwarmPolicy(t *testing.T, policy providers.ExclusionPolicy) func() {
	t.Helper()
	old := loadSwarmPolicy
	loadSwarmPolicy = func() (providers.ExclusionPolicy, bool) { return policy, false }
	return func() { loadSwarmPolicy = old }
}

// excludedSwarmInventory is the D2 fixture on the CLI path: 1 eligible native
// row plus a provider group holding one excluded row (the built-in default
// entry: the k3 id token) and one eligible row.
func excludedSwarmInventory(t *testing.T) (*inventory.Inventory, inventory.Entry) {
	t.Helper()
	reg := &providers.Registry{Providers: []providers.Provider{{Name: "nw", Models: []providers.Model{
		{ID: "kimi-k3"},
		{ID: "glm-5.3-flash-flex"},
	}}}}
	inv, err := inventory.Load(context.Background(), reg, staticFetcher{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
	})
	if err != nil {
		t.Fatalf("inventory.Load: %v", err)
	}
	for _, e := range inv.Entries {
		if e.Excluded {
			return inv, e
		}
	}
	t.Fatal("fixture must contain an excluded row")
	return nil, inventory.Entry{}
}

// sharedSwarmPickWarning is the sentence D2 = b prints for an explicit pick of
// an excluded row, composed from the marker authority.
func sharedSwarmPickWarning(seat int, spec string) string {
	return "warning: seat " + strconv.Itoa(seat) + " (" + spec + ") is " + providers.ExcludedMarker() +
		"; explicit pick honored (the exclusion list covers auto-seating and failover only)"
}

// TestResolveFlagSeatsHonorsExcludedSpecWithWarning pins the spec half of
// D2 = b on the --seats flag path: the excluded spec is HONORED (it stays in
// the returned entries, so it reaches the fanout argv) and warned about with
// the shared sentence on the ERROR writer (S7-C10: stdout on this path is the
// dry-run command echo, so a warning there would interleave it). The sibling
// eligible spec stays silent, which is the non-vacuity control.
func TestResolveFlagSeatsHonorsExcludedSpecWithWarning(t *testing.T) {
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	// An empty saved-preset store keeps "nw/kimi-k3" on the seat-spec path.
	defer withStubSwarmPresets(t, nil)()

	var errOut bytes.Buffer
	entries, code := resolveFlagSeats("native:gemini-3.8-flash-high,nw/kimi-k3", &errOut)
	if code >= 0 {
		t.Fatalf("explicit spec line exit = %d, want success; stderr:\n%s", code, errOut.String())
	}
	if len(entries) != 2 {
		t.Fatalf("spec line seated %d entries, want 2 (nothing is rejected): %+v", len(entries), entries)
	}
	if entries[0].Excluded {
		t.Errorf("%s must stay eligible: %+v", entries[0].Spec, entries[0])
	}
	if !entries[1].Excluded {
		t.Errorf("%s must carry the exclusion verdict: %+v", entries[1].Spec, entries[1])
	}
	if want := sharedSwarmPickWarning(2, "nw/kimi-k3"); !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr = %q; want the shared sentence %q", errOut.String(), want)
	}
	if got := strings.Count(errOut.String(), "warning:"); got != 1 {
		t.Errorf("exactly one warning expected (the native spec is eligible); got %d: %q", got, errOut.String())
	}
}

// TestResolveFlagSeatsEmptyUserListExcludesNothing pins D1 = a on the spec path:
// a user-owned EMPTY exclusion list excludes nothing, so the same spec line
// warns about nothing (neither writer carries a warning).
func TestResolveFlagSeatsEmptyUserListExcludesNothing(t *testing.T) {
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{User: []string{}})()
	defer withStubSwarmPresets(t, nil)()

	var errOut bytes.Buffer
	entries, code := resolveFlagSeats("nw/kimi-k3", &errOut)
	if code >= 0 {
		t.Fatalf("exit = %d, want success; stderr:\n%s", code, errOut.String())
	}
	if len(entries) != 1 || entries[0].Excluded {
		t.Fatalf("an empty user list excludes nothing: %+v", entries)
	}
	if errOut.Len() != 0 {
		t.Errorf("no warning expected under an empty user list: %q", errOut.String())
	}
}

// TestRunSwarmCmdHonorsExcludedNumberedPick is the end-to-end proof of the
// deliberate behavior change: `--seats <n>` naming an excluded row used to
// exit 2. It now exits 0, warns with the shared sentence on STDERR, and the row
// reaches the dry-run fanout vector. S7-C10: stdout carries the command echo
// and nothing else, so a caller can relay it verbatim.
func TestRunSwarmCmdHonorsExcludedNumberedPick(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	inv, excluded := excludedSwarmInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()
	defer withStubSwarmPresets(t, nil)()
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", strconv.Itoa(excluded.Number),
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (an explicit pick of an excluded row is honored); stderr:\n%s", code, errBuf.String())
	}
	if ran {
		t.Error("--dry-run must not execute the swarm")
	}
	got := outBuf.String()
	if !strings.Contains(got, "--seats "+excluded.Spec) {
		t.Errorf("the honored excluded seat must reach the fanout vector:\n%s", got)
	}
	if want := sharedSwarmPickWarning(excluded.Number, excluded.Spec); !strings.Contains(errBuf.String(), want) {
		t.Errorf("stderr must carry the shared warning %q:\n%s", want, errBuf.String())
	}
	if strings.Contains(got, "warning:") {
		t.Errorf("stdout must stay the machine-relayed command echo (no warning):\n%s", got)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "agy pro fanout") {
		t.Errorf("the fanout command must be the first thing on stdout:\n%s", got)
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("stdout must be exactly the one-line command echo, got %d lines:\n%s", n, got)
	}
}

// TestRunSwarmCmdHonorsExcludedSpec is the same end-to-end proof on the
// explicit seat-spec spelling, where the policy comes from loadSwarmPolicy
// (the seam) instead of the loaded inventory. The warning rides stderr here
// too (S7-C10).
func TestRunSwarmCmdHonorsExcludedSpec(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer withStubSwarmPresets(t, nil)()
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", "nw/kimi-k3",
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errBuf.String())
	}
	got := outBuf.String()
	if !strings.Contains(got, "--seats nw/kimi-k3") {
		t.Errorf("the honored excluded spec must reach the fanout vector:\n%s", got)
	}
	if want := sharedSwarmPickWarning(1, "nw/kimi-k3"); !strings.Contains(errBuf.String(), want) {
		t.Errorf("stderr must carry the shared warning %q:\n%s", want, errBuf.String())
	}
	if strings.Contains(got, "warning:") {
		t.Errorf("stdout must stay the machine-relayed command echo (no warning):\n%s", got)
	}
}

// TestRunSwarmCmdPresetSkipWarningRidesStderr covers the OTHER swarm warning
// that used to reach stdout: the auto-pick skip ("skipped seat N (...)"). The
// preset arm seats nothing excluded, so the dry-run --seats value must not name
// the row, while stderr carries the skip and the degrade note (S7-C10).
func TestRunSwarmCmdPresetSkipWarningRidesStderr(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	inv, excluded := excludedSwarmInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()
	defer withStubSwarmPresets(t, nil)()
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", "p2",
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a short preset degrades, it never errors); stderr:\n%s", code, errBuf.String())
	}
	if ran {
		t.Error("--dry-run must not execute the swarm")
	}
	// Non-vacuity: this fixture's p2 expansion would auto-seat the excluded row
	// if the exclusion list were ignored, so the skip warning must fire.
	skip := "skipped seat " + strconv.Itoa(excluded.Number) + " (" + excluded.Spec + "): " + providers.ExcludedMarker()
	if !strings.Contains(errBuf.String(), skip) {
		t.Errorf("stderr must name the skipped row %q:\n%s", skip, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "degraded") {
		t.Errorf("stderr must carry the degrade note for the short preset:\n%s", errBuf.String())
	}
	got := outBuf.String()
	if strings.Contains(got, "skipped seat") || strings.Contains(got, "warning:") {
		t.Errorf("stdout must not carry a warning of either arm:\n%s", got)
	}
	if strings.Contains(got, excluded.Spec) {
		t.Errorf("an auto-picked preset must never seat the excluded row %q:\n%s", excluded.Spec, got)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "agy pro fanout") {
		t.Errorf("the fanout command must be the first thing on stdout:\n%s", got)
	}
}

// TestRunSwarmCmdDryRunEchoIsExactlyTheBuiltVector pins the contract's 7-flag
// dry-run vector byte for byte while an exclusion warning fires: stdout must be
// exactly FormatCommand(RunSpec.Args()) and nothing else, because the command
// echo is the value callers relay (S7-C10 moved the warnings off it).
func TestRunSwarmCmdDryRunEchoIsExactlyTheBuiltVector(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	inv, excluded := excludedSwarmInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()
	defer withStubSwarmPresets(t, nil)()
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", strconv.Itoa(excluded.Number),
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errBuf.String())
	}

	tmpl, ok := swarm.FindTemplate("code-review")
	if !ok {
		t.Fatal("template code-review must exist")
	}
	// The same resolution the command performed, with the warnings discarded.
	seats, err := swarm.ResolveSeatsWith(strconv.Itoa(excluded.Number), inv, nil, io.Discard)
	if err != nil {
		t.Fatalf("ResolveSeatsWith(%d): %v", excluded.Number, err)
	}
	want := swarm.FormatCommand(swarm.RunSpec{Template: tmpl, Seats: seats, Root: ".", Prompt: "check it"}.Args())
	if got := strings.TrimSpace(outBuf.String()); got != want {
		t.Errorf("dry-run echo = %q; want exactly the built vector %q", got, want)
	}
	// Non-vacuity on the flag set: all 7 documented flags are in the echo, in
	// the builder's order.
	prev := -1
	for _, f := range []string{"--task", "--seats", "--root", "--timeout", "--tools", "--prompt", "--json"} {
		i := strings.Index(outBuf.String(), f)
		if i <= prev {
			t.Errorf("flag %q missing or out of order in the dry-run echo:\n%s", f, outBuf.String())
		}
		prev = i
	}
}

// TestProSwarmUsageDocumentsExclusionPolicy replaces the retired rejection
// wording: the flag surface must state that the exclusion list governs
// auto-seating and that an explicit pick is honored, never refused.
func TestProSwarmUsageDocumentsExclusionPolicy(t *testing.T) {
	for _, want := range []string{"defaults.excludeModels", "never auto-seated", "honors it and prints a warning", "nothing is\nrefused"} {
		if !strings.Contains(proSwarmUsage, want) {
			t.Errorf("usage text must document %q:\n%s", want, proSwarmUsage)
		}
	}
	if strings.Contains(proSwarmUsage, "rejected") {
		t.Errorf("usage text must not describe a rejection (D2 = b rejects nothing):\n%s", proSwarmUsage)
	}
}

// TestProSwarmUsageNamesSavedPresets pins the flag surface documentation: the
// usage text and the --seats help must mention the saved-preset name, or the
// grammar is undiscoverable from `agy pro swarm --help`.
func TestProSwarmUsageNamesSavedPresets(t *testing.T) {
	if !strings.Contains(proSwarmUsage, "saved seat preset") {
		t.Errorf("usage text does not mention saved seat preset names:\n%s", proSwarmUsage)
	}
}

// TestRunSwarmCmdDryRunResolvesSavedPresetName is the end-to-end proof on the
// real CLI entry point: `agy pro swarm --dry-run --seats <saved name>` reaches
// the zero-prompt path, resolves the name against the loaded store, and prints
// the fanout command carrying the preset's seat SPECS (the contract's 7-flag
// vector is untouched: --seats value is still the comma join of Spec).
func TestRunSwarmCmdDryRunResolvesSavedPresetName(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	inv := swarmFixtureInventory(t)
	defer withStubSwarmInventory(t, inv, nil)()
	defer withTTY(t, true)()
	// The store the CLI reads is the real FileStore under AGY_PRO_HOME.
	st := &swarm.FileStore{Path: filepath.Join(os.Getenv("AGY_PRO_HOME"), "swarm-state.json")}
	if err := st.SavePresets([]swarm.SavedPreset{{Name: "deep", Numbers: "1,3"}}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", "deep",
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errBuf.String())
	}
	if ran {
		t.Error("--dry-run must not execute the swarm")
	}
	wantSeats := inv.Entries[0].Spec + "," + inv.Entries[2].Spec
	got := outBuf.String()
	if !strings.Contains(got, "--seats "+wantSeats) {
		t.Errorf("dry-run must carry the preset's seat specs %q:\n%s", wantSeats, got)
	}
	if strings.Contains(got, "--seats deep") {
		t.Errorf("the saved name must resolve to seat specs, not reach the fanout argv:\n%s", got)
	}
}
