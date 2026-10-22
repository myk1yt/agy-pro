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

// L1 + M1 sink pin: Entry.Family is a registry slot with no path-readability
// requirement, so the --json mask loop routes it through the registry-slot
// display authority (providers.MaskRegistryValue, the same one
// MaskFamilyName uses) instead of plain MaskOperand. A location-shaped
// key-shaped family ("./<key>") is exempt at plain MaskOperand (spawn
// errors must name the binary) and would ride raw into the dump without the
// loop change. The second row pins the M1 class (batch M) end-to-end at the
// sink: a marker-decorated JWT core is caught only by MaskRegistryValue's
// isJWTShapedOperand branch (no uppercase letter, so seedShaped cannot see
// it), so without that branch the JWT plant serializes raw. The seam drives
// the real inventory.Load so the registry -> entry -> sink chain runs.
// Without the loop change (or without the JWT branch) the raw plant reaches
// the dump and this test fails.
func TestRunModelsCmdJSONMasksLocationShapedKeyShapedFamily(t *testing.T) {
	for _, tc := range []struct{ name, familyPlant, wantFamily string }{
		{"relative-marker decorated secret", "./wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY", "wJalr****"},
		{"relative-marker decorated JWT", "./abcdefghij.klmnopqrst.uvwxyz012345", "abcde****"},
		// Batch O: the doubly-decorated sub-shape (a secret behind a
		// mid-string scheme that itself carries a marker) pinned at the
		// sink too — registrySegmentLooksSecret classifies the stripped
		// segment, and without it the plant serializes raw.
		{"mid-scheme marker-decorated secret", "note https://./wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY", "./wJa****"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &providers.Registry{Providers: []providers.Provider{{
				Name:    "nw",
				BaseURL: "https://api.example/v1",
				Models: []providers.Model{
					{ID: "glm-5.3-flash-flex", Family: tc.familyPlant},
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
			if strings.Contains(got, tc.familyPlant) {
				t.Errorf("models --json leaks the plant raw:\n%s", got)
			}
			var entries []inventory.Entry
			if err := json.Unmarshal([]byte(got), &entries); err != nil {
				t.Fatalf("json decode: %v\n%s", err, got)
			}
			if len(entries) != 2 {
				t.Fatalf("json entries = %d, want 2", len(entries))
			}
			// The registry-slot masker renders the plant masked (the exact
			// rendering belongs to providers.MaskRegistryValue; the sink pin
			// requires the deterministic first-5+**** form, so a rendering
			// drift at the sink fails here too).
			if entries[0].Family != tc.wantFamily {
				t.Errorf("family = %q, want %q", entries[0].Family, tc.wantFamily)
			}
			// The benign family keeps its case and renders byte-identical.
			if entries[1].Family != "Qwen" {
				t.Errorf("benign family = %q, want the case-preserved %q", entries[1].Family, "Qwen")
			}
		})
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

// TestRunModelsCmdJSONMasksLookAlikeModelID pins the newly converted inventory
// sink: a look-alike-decorated model id (U+2025 TWO DOT LEADER + "/" + a
// lowercase 20-rune core) sits in a record and every id-derived field of the
// --json dump must mask it. The id is free text at load (only the control-rune
// rule constrains it), so this is a real exposure: before the policy round the
// id, the seat spec, and the composed display name rode MaskOperand, whose
// location exemption and pre-policy classifier left the core readable.
func TestRunModelsCmdJSONMasksLookAlikeModelID(t *testing.T) {
	const core = "ab3def7gh9jk1mnpqr5t"
	const plant = "\u2025/" + core
	const masked = "ab3de****"

	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.example/v1",
		Models: []providers.Model{
			{ID: plant, Family: "glm"},
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
	if strings.Contains(got, core) || strings.Contains(got, plant) {
		t.Errorf("models --json leaks the look-alike model id raw:\n%s", got)
	}
	var entries []inventory.Entry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("json decode: %v\n%s", err, got)
	}
	if len(entries) != 2 {
		t.Fatalf("json entries = %d, want 2", len(entries))
	}
	if entries[0].ModelID != masked {
		t.Errorf("ModelID = %q, want %q", entries[0].ModelID, masked)
	}
	// The composed display name is a label ("<id> (<provider>)"); the display
	// arm masks the credential span, so the label cannot re-expose the core.
	if entries[0].DisplayName != masked {
		t.Errorf("DisplayName = %q, want the masked slot %q", entries[0].DisplayName, masked)
	}
	if strings.Contains(entries[0].Spec, core) {
		t.Errorf("Spec = %q leaks the core", entries[0].Spec)
	}
	// The benign row renders byte-identically (the sink masks nothing benign).
	if entries[1].ModelID != "qwen3.8-flash" || entries[1].Family != "Qwen" {
		t.Errorf("benign row moved: %+v", entries[1])
	}
}

// D7 machine half: `models --json` is the twin of the human listing, so the
// `excluded` verdict must be present on EVERY row kind, not only on the rows the
// listing marks. Entry.Excluded carries no `omitempty`, and this pin checks the
// KEY's presence as well as the value: native excluded / native clean /
// provider excluded + key missing / provider clean. The seam drives the real
// inventory.Load so the registry -> entry -> dump chain runs.
func TestRunModelsCmdJSONEmitsExcludedOnEveryRowKind(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "nw", APIKeyEnv: "NW_KEY_NOT_SET_IN_TESTS", Models: []providers.Model{
			{ID: "kimi-k3"}, {ID: "glm-5.3-flash-flex"},
		}},
		{Name: "google", Models: []providers.Model{{ID: "gemini-2.5-flash"}}},
	}}
	native := []inventory.NativeModel{
		{ID: "kimi-k3", DisplayName: "Kimi K3"},
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
	}
	old := loadModelsInventory
	loadModelsInventory = func(ctx context.Context) (*inventory.Inventory, error) {
		return inventory.Load(ctx, reg, staticFetcher(native))
	}
	t.Cleanup(func() { loadModelsInventory = old })

	var out, errw strings.Builder
	if code := runModelsCmdTo([]string{"--json"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	// Decode into raw objects, not []Entry: a struct decode cannot tell an
	// absent `excluded` key from `false` (omitempty / tag drift would pass).
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil {
		t.Fatalf("json decode: %v\n%s", err, out.String())
	}
	want := []struct {
		spec     string
		excluded bool
		note     string
	}{
		{"native:kimi-k3", true, "native row, built-in default policy"},
		{"native:gemini-3.8-flash-high", false, "native clean row"},
		{"nw/kimi-k3", true, "provider row, excluded AND key missing"},
		{"nw/glm-5.3-flash-flex", false, "provider clean row (key missing only)"},
		{"google/gemini-2.5-flash", false, "provider clean row"},
	}
	if len(rows) != len(want) {
		t.Fatalf("json rows = %d, want %d:\n%s", len(rows), len(want), out.String())
	}
	excludedCount := 0
	for i, tc := range want {
		var spec string
		if err := json.Unmarshal(rows[i]["Spec"], &spec); err != nil || spec != tc.spec {
			t.Fatalf("row %d spec = %q (err %v), want %q (row order drift)", i, spec, err, tc.spec)
		}
		raw, ok := rows[i]["excluded"]
		if !ok {
			t.Fatalf("%s (%s): the dump omits the `excluded` key: %s", tc.spec, tc.note, rows[i])
		}
		var got bool
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: excluded = %s is not a JSON bool: %v", tc.spec, raw, err)
		}
		if got != tc.excluded {
			t.Errorf("%s (%s): excluded = %v, want %v", tc.spec, tc.note, got, tc.excluded)
		}
		if got {
			excludedCount++
		}
	}
	// Non-vacuity: the dump carries both verdicts.
	if excludedCount != 2 {
		t.Errorf("excluded rows = %d, want 2 (native kimi-k3 + nw/kimi-k3)", excludedCount)
	}
}

// D7 end-to-end through the command: the `--all` listing marks the excluded row
// and leaves the clean row byte-identical, driven by the same Entry.Excluded flag
// the --json dump carries.
func TestRunModelsCmdAllMarksExcludedRows(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "nw", Models: []providers.Model{{ID: "kimi-k3"}, {ID: "kimi-k2"}},
	}}}
	old := loadModelsInventory
	loadModelsInventory = func(ctx context.Context) (*inventory.Inventory, error) {
		return inventory.Load(ctx, reg, nil)
	}
	t.Cleanup(func() { loadModelsInventory = old })

	var out, errw strings.Builder
	if code := runModelsCmdTo([]string{"--all"}, &out, &errw); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errw.String())
	}
	got := out.String()
	t.Logf("--all output:\n%s", got)
	marker := providers.ExcludedMarker()
	if n := strings.Count(got, marker); n != 1 {
		t.Fatalf("marker count = %d, want 1 (only kimi-k3 is excluded):\n%s", n, got)
	}
	var marked, clean string
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(line, "kimi-k3"):
			marked = line
		case strings.Contains(line, "kimi-k2"):
			clean = line
		}
	}
	if !strings.HasSuffix(marked, "      "+marker) {
		t.Errorf("kimi-k3 row not marked: %q", marked)
	}
	if clean == "" || strings.Contains(clean, marker) {
		t.Errorf("clean row = %q, want the row rendered without the marker", clean)
	}
}
