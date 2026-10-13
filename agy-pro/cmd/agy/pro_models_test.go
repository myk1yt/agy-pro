package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// stubInventory returns a combined inventory shaped like B1-1: 18 native rows
// (the real `agy models` catalog size) + external rows across two providers.
func stubInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{{
			ID: "gemini-2.5-flash",
			Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsVision: true,
			},
		}}},
		{Name: "nw", APIKeyEnv: "NW_KEY_NOT_SET_IN_TESTS", Models: []providers.Model{{
			ID: "glm-5.3-flash-flex",
			Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true,
			},
		}}},
	}}
	var native []inventory.NativeModel
	for i := 0; i < 18; i++ {
		native = append(native, inventory.NativeModel{
			ID:          "native-model-" + string(rune('a'+i)),
			DisplayName: "Native Model " + string(rune('A'+i)),
		})
	}
	inv, err := inventory.Load(context.Background(), reg, staticFetcher(native))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

type staticFetcher []inventory.NativeModel

func (s staticFetcher) Fetch(context.Context) ([]inventory.NativeModel, error) {
	return s, nil
}

// withStubInventory points loadModelsInventory at the fixture and returns the
// restore func.
func withStubInventory(t *testing.T, inv *inventory.Inventory, err error) func() {
	t.Helper()
	old := loadModelsInventory
	loadModelsInventory = func(context.Context) (*inventory.Inventory, error) {
		return inv, err
	}
	return func() { loadModelsInventory = old }
}

func TestRunModelsCmdAllRendersCombinedInventory(t *testing.T) {
	defer withStubInventory(t, stubInventory(t), nil)()
	var out, errw strings.Builder
	code := runModelsCmdTo([]string{"--all"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	got := out.String()
	t.Logf("--all output:\n%s", got)

	// B1-1: >=18 native rows + all registered externals, group-tagged.
	nativeRows := strings.Count(got, "[native]")
	if nativeRows != 18 {
		t.Errorf("[native] row tags = %d, want 18", nativeRows)
	}
	if !strings.Contains(got, "[google]") || !strings.Contains(got, "[nw]") {
		t.Errorf("missing provider group tags:\n%s", got)
	}
	// Capability columns per contract.
	if !strings.Contains(got, "tools, vision") {
		t.Errorf("missing capability column:\n%s", got)
	}
	// (key missing) marker for the unset NW key.
	if !strings.Contains(got, "(key missing)") {
		t.Errorf("missing key-missing marker:\n%s", got)
	}
	// Dedupe: 18 + 2 rows total.
	if strings.Count(got, "[") < 20 {
		t.Errorf("row count suspiciously low:\n%s", got)
	}
}

func TestRunModelsCmdJSONDumpsEntries(t *testing.T) {
	defer withStubInventory(t, stubInventory(t), nil)()
	var out, errw strings.Builder
	code := runModelsCmdTo([]string{"--json"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	var entries []inventory.Entry
	if err := json.Unmarshal([]byte(out.String()), &entries); err != nil {
		t.Fatalf("json decode: %v\n%s", err, out.String())
	}
	if len(entries) != 20 {
		t.Fatalf("json entries = %d, want 20 (18 native + 2 provider)", len(entries))
	}
	if entries[0].Kind != "native" || entries[0].Number != 1 {
		t.Errorf("first entry = %+v", entries[0])
	}
	if entries[19].Spec != "nw/glm-5.3-flash-flex" {
		t.Errorf("last entry = %+v", entries[19])
	}
}

// C2 end-to-end: `models --json` must mask a key-shaped family. The combined
// inventory carries the explicit registry family CASE-PRESERVED (phase 1), and
// the decorated display class requires an uppercase letter, so the pre-fix
// pipeline (FamilyName() folded before the mask at the JSON sink) let a
// lowercase-shaped family pasted into the registry family slot ride raw into
// the dump. The seam drives the real inventory.Load so the registry -> entry
// -> mask chain runs, not just the render step. The plant is
// separator-decorated (hyphens): its folded form is neither a contiguous
// base64 run nor identifier-class, so only the preserved case can fire the
// decorated class.
func TestRunModelsCmdJSONMasksKeyShapedFamily(t *testing.T) {
	const familyPlant = "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	const familyPlantMask = "wJalr****"
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.example/v1",
		Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", Family: familyPlant},
			{ID: "qwen3.8-flash", Family: "Qwen"},
		},
	}}}
	old := loadModelsInventory
	loadModelsInventory = func(ctx context.Context) (*inventory.Inventory, error) {
		return inventory.Load(ctx, reg, nil)
	}
	t.Cleanup(func() { loadModelsInventory = old })

	var out, errw strings.Builder
	code := runModelsCmdTo([]string{"--json"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	got := out.String()
	if strings.Contains(got, familyPlant) {
		t.Errorf("models --json leaks the key-shaped family raw:\n%s", got)
	}
	var entries []inventory.Entry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("json decode: %v\n%s", err, got)
	}
	if len(entries) != 2 {
		t.Fatalf("json entries = %d, want 2", len(entries))
	}
	if entries[0].Family != familyPlantMask {
		t.Errorf("key-shaped family = %q, want %q", entries[0].Family, familyPlantMask)
	}
	// The case-preserved family keeps its case; the pre-fix folded rendering
	// would have printed "qwen".
	if entries[1].Family != "Qwen" {
		t.Errorf("benign family = %q, want the case-preserved %q", entries[1].Family, "Qwen")
	}
}

// L1 sink pin: Entry.Family is a registry slot with no path-readability
// requirement, so the --json mask loop routes it through the registry-slot
// display authority (providers.MaskRegistryValue, the same one
// MaskFamilyName uses) instead of plain MaskOperand. A location-shaped
// key-shaped family ("./<key>") is exempt at plain MaskOperand (spawn
// errors must name the binary) and would ride raw into the dump without the
// loop change. The seam drives the real inventory.Load so the registry ->
// entry -> sink chain runs. Without the loop change the raw plant reaches
// the dump and this test fails.
func TestRunModelsCmdJSONMasksLocationShapedKeyShapedFamily(t *testing.T) {
	const familyPlant = "./wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.example/v1",
		Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", Family: familyPlant},
			{ID: "qwen3.8-flash", Family: "Qwen"},
		},
	}}}
	old := loadModelsInventory
	loadModelsInventory = func(ctx context.Context) (*inventory.Inventory, error) {
		return inventory.Load(ctx, reg, nil)
	}
	t.Cleanup(func() { loadModelsInventory = old })

	var out, errw strings.Builder
	code := runModelsCmdTo([]string{"--json"}, &out, &errw)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	got := out.String()
	if strings.Contains(got, familyPlant) {
		t.Errorf("models --json leaks the location-shaped key-shaped family raw:\n%s", got)
	}
	var entries []inventory.Entry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("json decode: %v\n%s", err, got)
	}
	if len(entries) != 2 {
		t.Fatalf("json entries = %d, want 2", len(entries))
	}
	// The registry-slot masker renders the location-shaped plant masked (the
	// exact rendering belongs to providers.MaskRegistryValue; the sink pin
	// only requires that the raw plant never serializes).
	if !strings.Contains(entries[0].Family, "****") {
		t.Errorf("location-shaped key-shaped family = %q, want a masked rendering", entries[0].Family)
	}
	// The benign family keeps its case and renders byte-identical.
	if entries[1].Family != "Qwen" {
		t.Errorf("benign family = %q, want the case-preserved %q", entries[1].Family, "Qwen")
	}
}

// K1 sink pin: Entry.Capabilities carries the registry-controlled
// reasoningEffort (Capabilities.Summary composes thinking(<effort>)) and
// nothing on the add-json / hand-edit write path validates that field. The
// composition mask inside Summary covers a key-shaped effort (plainly
// key-shaped and location-shaped alike, K1), and the --json mask loop
// covers the field again as defense in depth: the composed summary is
// itself secret-shaped, so the dump renders "tools****". The seam drives
// the real inventory.Load so the registry -> entry -> sink chain runs.
// Without the loop change the dump carries the unmasked composed summary
// "tools, thinking(wJalr****)" and this test fails.
func TestRunModelsCmdJSONMasksKeyShapedReasoningEffort(t *testing.T) {
	const plainPlant = "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	const locationPlant = "./" + plainPlant
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.example/v1",
		Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true, ReasoningEffort: locationPlant,
			}},
			{ID: "deepseek-v4.1-flash-flex", Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true, ReasoningEffort: plainPlant,
			}},
			{ID: "qwen3.8-flash", Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true, ReasoningEffort: "high",
			}},
		},
	}}}
	old := loadModelsInventory
	loadModelsInventory = func(ctx context.Context) (*inventory.Inventory, error) {
		return inventory.Load(ctx, reg, nil)
	}
	t.Cleanup(func() { loadModelsInventory = old })

	var out, errw strings.Builder
	if code := runModelsCmdTo([]string{"--json"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	got := out.String()
	if strings.Contains(got, plainPlant) {
		t.Errorf("models --json leaks the key-shaped reasoningEffort raw:\n%s", got)
	}
	var entries []inventory.Entry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("json decode: %v\n%s", err, got)
	}
	if len(entries) != 3 {
		t.Fatalf("json entries = %d, want 3", len(entries))
	}
	// The sink renders the composed summary through MaskOperand (first-5+****
	// of the whole summary); without the sink both plants would serialize
	// with the effort readable ("tools, thinking(wJalr****)" at best).
	for i, want := range []string{"tools****", "tools****", "tools, thinking(high)"} {
		if entries[i].Capabilities != want {
			t.Errorf("entry %d capabilities = %q, want %q", i, entries[i].Capabilities, want)
		}
	}
}

func TestRunModelsCmdUnknownFlagUsageExit2(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"extra-positional"}, {"--all", "trailing"}} {
		var out, errw strings.Builder
		code := runModelsCmdTo(args, &out, &errw)
		if code != 2 {
			t.Errorf("runModelsCmdTo(%v) exit = %d, want 2", args, code)
		}
		if !strings.Contains(errw.String(), proModelsUsage) {
			t.Errorf("runModelsCmdTo(%v) missing usage:\n%s", args, errw.String())
		}
	}
}

func TestRunModelsCmdNoArgsIsLegacyOutput(t *testing.T) {
	// The no-flag path must be the legacy runModels() rendering, verbatim.
	// Compare a direct runModels() capture with the command's capture.
	legacy := captureStdout(t, runModels)
	viaCmd := captureStdout(t, func() {
		if code := runModelsCmd(nil); code != 0 {
			t.Errorf("runModelsCmd(nil) exit = %d, want 0", code)
		}
	})
	if viaCmd != legacy {
		t.Errorf("legacy output drifted.\n--- runModels() ---\n%s\n--- runModelsCmd() ---\n%s", legacy, viaCmd)
	}
}

func TestRunModelsCmdInventoryFailureExits1(t *testing.T) {
	defer withStubInventory(t, nil, errors.New("no catalog"))()
	var out, errw strings.Builder
	code := runModelsCmdTo([]string{"--all"}, &out, &errw)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errw.String(), "no catalog") {
		t.Errorf("stderr = %q, want the inventory error", errw.String())
	}
}

// U7 machine-output guard: `agy pro models` output (human and JSON) must
// never carry the community banner - it lives only in the interactive
// launcher (contract §2.10).
func TestRunModelsCmdMachineOutputHasNoCommunityBanner(t *testing.T) {
	defer withStubInventory(t, stubInventory(t), nil)()
	for _, args := range [][]string{{"--all"}, {"--json"}} {
		var out, errw strings.Builder
		if code := runModelsCmdTo(args, &out, &errw); code != 0 {
			t.Fatalf("runModelsCmdTo(%v) exit = %d", args, code)
		}
		for _, s := range []string{out.String(), errw.String()} {
			if strings.Contains(s, "reddit.com") {
				t.Errorf("runModelsCmdTo(%v) leaked the community banner:\n%s", args, s)
			}
		}
	}
}
