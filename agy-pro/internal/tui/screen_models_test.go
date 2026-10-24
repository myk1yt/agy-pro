package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/testfixtures"
)

// modelsFixtureReg is the standard two-model provider every test here reads:
// acme-fast (family, context, one granted tool, enabled) and acme-slow
// (family, maxOut, explicitly disabled). All tests run against a temp
// AGY_PRO_HOME + AGY_PRO_PROVIDERS; the live registry is never touched.
func modelsFixtureReg() *providers.Registry {
	disabled := false
	return &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{
				{
					ID:            "acme-fast",
					Family:        "fast",
					ContextLength: 128000,
					Capabilities:  providers.Capabilities{SupportsTools: true, AllowedTools: []string{"read_file"}},
				},
				{
					ID:              "acme-slow",
					Family:          "slow",
					MaxOutputTokens: 4096,
					Enabled:         &disabled,
				},
			},
		},
	}}
}

// newModelsTestApp seeds the fixture into a temp providers.json and returns
// an app whose top screen is the models screen for "acme", plus the core
// and the on-disk path for byte-level assertions. SaveReg is left nil so the
// Core uses the real saveRegistryFile seam (0600 atomic, reg.Path).
func newModelsTestApp(t *testing.T, openForm func(mode string, modelID string, a *App)) (*App, *settings.Core, string) {
	t.Helper()
	return newModelsTestAppReg(t, modelsFixtureReg(), openForm)
}

// newModelsTestAppReg is newModelsTestApp over an arbitrary registry, for
// tests that need a non-standard fixture (unlisted tool grants,
// credential-shaped fields). The provider under test must be named "acme".
func newModelsTestAppReg(t *testing.T, reg *providers.Registry, openForm func(mode string, modelID string, a *App)) (*App, *settings.Core, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv(providers.EnvHome, tmp)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	reg.Path = path
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("seed providers.json: %v", err)
	}
	core := settings.NewCore(settings.Deps{LoadReg: providers.Load})
	app := NewApp(100, 24, DirLTR)
	app.Push(NewModelsScreen(core, "acme", openForm))
	return app, core, path
}

// reloadModels reads the on-disk registry and returns the acme models.
func reloadModels(t *testing.T, path string) []providers.Model {
	t.Helper()
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	p, ok := reg.Provider("acme")
	if !ok {
		t.Fatal("provider acme missing after reload")
	}
	return p.Models
}

// formCall is one recorded openForm invocation.
type formCall struct {
	mode    string
	modelID string
}

// runModelsKeys drives one capture run with a form spy and returns the
// captured frames plus the form calls the spy saw.
func runModelsKeys(t *testing.T, keys []Key) (string, []formCall) {
	t.Helper()
	var calls []formCall
	app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
		calls = append(calls, formCall{mode: mode, modelID: modelID})
	})
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	return out, calls
}

// TestModelsRowsRender pins the row inventory: one row per model with the
// family/context/maxOut/enabled/tool-count note, then the fixed action rows.
func TestModelsRowsRender(t *testing.T) {
	out, _ := runModelsKeys(t, nil)
	for _, want := range []string{
		"Models: acme",
		"acme-fast", "acme-slow",
		"fast", "slow",
		"Context 128000", "Max Out 4096",
		"Enabled", "Disabled",
		"tools 1", "tools 0",
		"Add model", "Edit", "Delete", "Enable / disable",
		missingModelsActionTools, "Back",
		"No models for this provider.",
	} {
		if want == "No models for this provider." {
			if strings.Contains(out, want) {
				t.Fatalf("empty-state line rendered for a provider with models; frames:\n%s", out)
			}
			continue
		}
		if !strings.Contains(out, want) {
			t.Fatalf("frames missing %q; frames:\n%s", want, out)
		}
	}
}

// TestModelsTitleAndMode pins the surface contract: NAV mode and the
// catalog title with the provider slot filled.
func TestModelsTitleAndMode(t *testing.T) {
	app, _, _ := newModelsTestApp(t, nil)
	scr := app.Top()
	if scr.Mode() != ModeNav {
		t.Fatalf("models mode = %v, want ModeNav", scr.Mode())
	}
	if got, want := scr.Title(), "Models: acme"; got != want {
		t.Fatalf("models title = %q, want %q", got, want)
	}
}

// TestModelsDualNavModelRows pins the dual-navigation structural guarantee
// on the model rows: digit 2 and arrows+Enter both open the edit form for
// the same model.
func TestModelsDualNavModelRows(t *testing.T) {
	_, digitCalls := runModelsKeys(t, []Key{Digit(2)})
	_, arrowCalls := runModelsKeys(t, []Key{KeyDown, KeyEnter})
	if len(digitCalls) != 1 || len(arrowCalls) != 1 {
		t.Fatalf("digit calls %v, arrow calls %v; want one form call each", digitCalls, arrowCalls)
	}
	if digitCalls[0] != arrowCalls[0] {
		t.Fatalf("digit opened %+v, arrows opened %+v; want the same target", digitCalls[0], arrowCalls[0])
	}
	if digitCalls[0].mode != "edit" || digitCalls[0].modelID != "acme-slow" {
		t.Fatalf("model row opened %+v, want edit/acme-slow", digitCalls[0])
	}
}

// TestModelsDualNavActionRows pins the same guarantee on the action rows:
// digit 3 and arrows+Enter both activate "Add model", and the injected form
// factory receives mode "add" with an empty model ID.
func TestModelsDualNavActionRows(t *testing.T) {
	_, digitCalls := runModelsKeys(t, []Key{Digit(3)})
	_, arrowCalls := runModelsKeys(t, []Key{KeyDown, KeyDown, KeyEnter})
	if len(digitCalls) != 1 || len(arrowCalls) != 1 {
		t.Fatalf("digit calls %v, arrow calls %v; want one form call each", digitCalls, arrowCalls)
	}
	if digitCalls[0] != arrowCalls[0] {
		t.Fatalf("digit opened %+v, arrows opened %+v; want the same target", digitCalls[0], arrowCalls[0])
	}
	if digitCalls[0].mode != "add" || digitCalls[0].modelID != "" {
		t.Fatalf("add row opened %+v, want add/empty", digitCalls[0])
	}
}

// TestModelsEditActionRow pins that the Edit action opens the form in edit
// mode for the model under the cursor. The selection gesture (KeyUp rests
// the cursor on model row 1) is required first: the sticky selection starts
// at -1 since user decision 2A, so firing Edit on entry would refuse.
func TestModelsEditActionRow(t *testing.T) {
	_, calls := runModelsKeys(t, []Key{KeyUp, Digit(4)})
	if len(calls) != 1 || calls[0].mode != "edit" || calls[0].modelID != "acme-fast" {
		t.Fatalf("edit action opened %v, want edit/acme-fast", calls)
	}
}

// TestModelsDeleteApprovalSummary pins the M4 gate content: the exact
// mutation summary (masked provider + model id from the catalog sentence,
// model count N -> N-1) renders in the approval modal, without the
// last-model warning for a two-model provider.
func TestModelsDeleteApprovalSummary(t *testing.T) {
	// KeyUp first: the sticky selection starts at -1 (user decision 2A), so
	// the Delete row refuses until the cursor rested on a model row.
	out, _ := runModelsKeys(t, []Key{KeyUp, Digit(5)})
	for _, want := range []string{
		"Delete Model?",
		"Removes the registry entry for acme-fast in acme. Key values are untouched.",
		"Model count: 2 -> 1",
		"y approve write · n/Esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("approval modal missing %q; frames:\n%s", want, out)
		}
	}
	if strings.Contains(out, missingModelsDeleteLastWarning) {
		t.Fatalf("last-model warning shown for a two-model provider; frames:\n%s", out)
	}
}

// TestModelsDeleteConfirm pins the happy path: y runs the delete, the file
// on disk loses the model, the sibling model is byte-identical after a full
// save/reload cycle, and the success message carries the masked operands.
func TestModelsDeleteConfirm(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	before, _ := json.Marshal(reloadModels(t, path)[1])

	// KeyUp first: select acme-fast (selection starts at -1, decision 2A).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(5), KeyYes}})

	models := reloadModels(t, path)
	if len(models) != 1 || models[0].ID != "acme-slow" {
		t.Fatalf("models on disk = %+v, want only acme-slow", models)
	}
	after, _ := json.Marshal(models[0])
	if string(after) != string(before) {
		t.Errorf("sibling model changed:\nbefore: %s\nafter:  %s", before, after)
	}
	if !strings.Contains(out, "Deleted model acme-fast from acme.") {
		t.Errorf("success message missing; frames:\n%s", out)
	}
	// The re-rendered list must show the surviving model and no orphan row.
	if !strings.Contains(out, "acme-slow") || strings.Contains(out, "[1] acme-fast") {
		t.Errorf("post-delete frames:\n%s", out)
	}
}

// TestModelsDeleteCancel pins that n closes the gate with the
// nothing-written message and leaves the on-disk file byte-identical.
func TestModelsDeleteCancel(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	// KeyUp first: select acme-fast (selection starts at -1, decision 2A).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(5), KeyNo}})

	if got, err := os.ReadFile(path); err != nil || string(got) != string(diskBefore) {
		t.Fatalf("on-disk JSON changed after cancel (err %v)", err)
	}
	if models := reloadModels(t, path); len(models) != 2 {
		t.Fatalf("models on disk = %d, want 2 after cancel", len(models))
	}
	if !strings.Contains(out, missingModelsCancelled) {
		t.Errorf("cancel message missing; frames:\n%s", out)
	}
}

// TestModelsDeleteLastModelWarning pins that deleting the provider's last
// model surfaces the warning line in the approval summary.
func TestModelsDeleteLastModelWarning(t *testing.T) {
	// Select acme-fast first (KeyUp rests the cursor on model row 1; the
	// sticky selection starts at -1 since user decision 2A), delete it, then
	// the provider has one model left. Approve, re-aim at the survivor, open
	// the gate again.
	out, _ := runModelsKeys(t, []Key{
		KeyUp, Digit(5), KeyYes, // delete acme-fast (selection sticks on the model rows)
		KeyHome, Digit(4), // selection follows the cursor onto acme-slow; Delete is now row 4
	})
	for _, want := range []string{
		missingModelsDeleteLastWarning,
		"Model count: 1 -> 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("last-model delete missing %q; frames:\n%s", want, out)
		}
	}
}

// TestModelsToggleEnabled pins the M4-gated toggle: the flip is proposed in
// the approval modal (masked operands, enabled field before/after), y runs
// Core.ModelSetEnabled so the file gains the explicit true, and the message
// carries the masked operands.
func TestModelsToggleEnabled(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)

	// Cursor on acme-slow (row 2), currently disabled.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(6), KeyYes}})

	models := reloadModels(t, path)
	if models[1].Enabled == nil || !*models[1].Enabled {
		t.Fatalf("enabled flag on disk = %+v, want explicit true", models[1].Enabled)
	}
	if !strings.Contains(out, "Model acme-slow in acme: enabled set to Enabled.") {
		t.Errorf("toggle message missing; frames:\n%s", out)
	}
}

// TestModelsToggleApprovalGate pins that the toggle writes nothing until the
// M4 modal is answered y: opening the gate alone leaves the on-disk file
// byte-identical, and the approval body shows the exact field before/after.
func TestModelsToggleApprovalGate(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(6)}})

	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed before the approval modal was answered")
	}
	for _, want := range []string{
		missingModelsToggleTitle,
		"Sets enabled for model acme-slow in acme.",
		"Enabled: Disabled -> Enabled",
		"y approve write · n/Esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("toggle approval missing %q; frames:\n%s", want, out)
		}
	}

	// n answers the gate with the nothing-written message and no byte change.
	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(6), KeyNo}})
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after toggle cancel")
	}
	if !strings.Contains(out, missingModelsCancelled) {
		t.Errorf("cancel message missing; frames:\n%s", out)
	}
}

// TestModelsToggleActionNeedsModel pins that the toggle action on a
// provider with no models reports the select-a-model message and writes
// nothing.
func TestModelsToggleActionNeedsModel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(providers.EnvHome, tmp)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "acme",
		BaseURL: "https://acme.example/v1",
		Models:  []providers.Model{},
	}}}
	reg.Path = path
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("seed providers.json: %v", err)
	}
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	core := settings.NewCore(settings.Deps{LoadReg: providers.Load})
	app := NewApp(100, 24, DirLTR)
	app.Push(NewModelsScreen(core, "acme", nil))
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(5)}}) // Delete action row.

	if !strings.Contains(out, "No models for this provider.") {
		t.Fatalf("empty-state line missing; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after no-model guard")
	}
	if app.Message() != missingModelsSelectModel {
		t.Fatalf("message = %q, want %q", app.Message(), missingModelsSelectModel)
	}
}

// TestModelsAddOpensForm pins that the Add row calls the injected form
// factory with mode "add" and an empty model ID.
func TestModelsAddOpensForm(t *testing.T) {
	_, calls := runModelsKeys(t, []Key{Digit(3)})
	if len(calls) != 1 || calls[0].mode != "add" || calls[0].modelID != "" {
		t.Fatalf("add row calls = %v, want one add/empty call", calls)
	}
}

// TestModelsToolsOverlay pins the M2 multi-select behind the M4 gate: the
// overlay renders the known tool list with the current grant checked, Space
// toggles, Enter proposes the new set in the approval modal (full before/
// after grant diff), and only y persists through Core.ModelSetTools in
// known-tool order. The overlay never routes through UpsertProvider
// (SupportsTools stays untouched).
func TestModelsToolsOverlay(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)

	// Cursor on acme-fast (row 1, selected via KeyUp since the sticky
	// selection starts at -1, decision 2A); open tools (row 7). Toggle
	// list_dir on (row 2 of the overlay), then save, then approve.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeyDown, KeySpace, KeyEnter, KeyYes}})

	for _, want := range []string{
		missingModelsToolsTitle, "[x] read_file", "[ ] grep", missingModelsToolsHint,
		missingModelsToolsApprovalTitle,
		"Updates tool grants for model acme-fast in acme.",
		"tools: read_file -> read_file, list_dir",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tools overlay missing %q; frames:\n%s", want, out)
		}
	}
	models := reloadModels(t, path)
	tools := models[0].Capabilities.AllowedTools
	if len(tools) != 2 || tools[0] != "read_file" || tools[1] != "list_dir" {
		t.Fatalf("AllowedTools on disk = %v, want [read_file list_dir]", tools)
	}
	if !models[0].Capabilities.SupportsTools {
		t.Fatal("SetModelTools must not flip SupportsTools")
	}
	if !strings.Contains(out, "Tool grants for acme-fast updated: 2 tools.") {
		t.Errorf("tools-saved message missing; frames:\n%s", out)
	}
}

// TestModelsToolsApprovalGate pins that Enter on the overlay writes nothing
// by itself: without a y the on-disk file is byte-identical.
func TestModelsToolsApprovalGate(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeyDown, KeySpace, KeyEnter}})

	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed before the tools approval modal was answered")
	}
	if !strings.Contains(out, missingModelsToolsApprovalTitle) {
		t.Errorf("tools approval modal missing; frames:\n%s", out)
	}
}

// TestModelsToolsUnionPreservesUnlisted pins the union semantics: grants
// outside the known tool list (replace_in_file, read_url_content, search_web)
// are not togglable in the overlay but pass through every save untouched,
// and the M4 approval diff shows the FULL before/after lists so the
// passthrough is visible, never hidden.
func TestModelsToolsUnionPreservesUnlisted(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities.AllowedTools = []string{
		"read_file", "replace_in_file", "read_url_content", "search_web",
	}
	app, _, path := newModelsTestAppReg(t, reg, nil)

	// Open tools (row 7), grant list_dir (overlay row 2), save, approve.
	// KeyUp first selects acme-fast (selection starts at -1, decision 2A).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeyDown, KeySpace, KeyEnter, KeyYes}})

	// Moved pin: the width clamp moved to the shared composition path
	// (phase-3 fix-2), so the approval diff reaches App.Frame already cut at
	// the frame width. The old expectation asserted the pre-clamp content (the
	// 133-cell diff at width 100), reachable only on the unclamped test path;
	// the byte-exact expectation is now the clamped form of the full frame
	// line (two-space indent included, as the catalog sentence renders it), and
	// the raw line must be absent.
	full := "  tools: read_file, replace_in_file, read_url_content, search_web -> read_file, list_dir, replace_in_file, read_url_content, search_web"
	clamped := cellwidth.Truncate(full, app.Width())
	if clamped == full || !strings.Contains(clamped, "->") {
		t.Fatalf("clamped diff = %q; want the width-%d cut that keeps the arrow", clamped, app.Width())
	}
	if !strings.Contains(out, clamped) {
		t.Fatalf("approval diff missing %q; frames:\n%s", clamped, out)
	}
	if strings.Contains(out, full) {
		t.Fatalf("unclamped approval diff reached the frame: %q", full)
	}
	// No composed line may exceed the frame width.
	for _, line := range app.Frame().Lines {
		if cells := cellwidth.String(line); cells > app.Width() {
			t.Fatalf("composed line = %d cells, want <= %d: %q", cells, app.Width(), line)
		}
	}
	tools := reloadModels(t, path)[0].Capabilities.AllowedTools
	want := []string{"read_file", "list_dir", "replace_in_file", "read_url_content", "search_web"}
	if strings.Join(tools, ",") != strings.Join(want, ",") {
		t.Fatalf("AllowedTools on disk = %v, want %v (unlisted grants must survive)", tools, want)
	}
}

// TestModelsToolsApprovalMasksEachGrant pins the round-5 security fix: the
// tools approval diff renders every grant through the per-item masker
// (maskJoined), so a credential-shaped hand-edited grant planted in the
// registry prints masked in both the before and the after column and the raw
// value never appears in any frame. The write itself still persists the
// grant verbatim (union semantics are unchanged; the fix is display-only).
func TestModelsToolsApprovalMasksEachGrant(t *testing.T) {
	const planted = "Ab3dEf7Gh9jK1mNpQr5t" // 20 runes, opaque, mixed letter/digit
	const masked = "Ab3dE****"
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities.AllowedTools = []string{"read_file", planted}
	app, _, path := newModelsTestAppReg(t, reg, nil)

	// Open tools (row 7), save untouched (Enter), approve (y). KeyUp first
	// selects acme-fast (selection starts at -1, decision 2A).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeyEnter, KeyYes}})

	if strings.Contains(out, planted) {
		t.Fatalf("raw credential-shaped grant rendered; frames:\n%s", out)
	}
	for _, want := range []string{
		masked,
		"tools: read_file, " + masked + " -> read_file, " + masked,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("approval diff missing %q; frames:\n%s", want, out)
		}
	}
	tools := reloadModels(t, path)[0].Capabilities.AllowedTools
	if strings.Join(tools, ",") != "read_file,"+planted {
		t.Fatalf("AllowedTools on disk = %v; want the grant preserved verbatim", tools)
	}
}

// TestModelsToolsMinOne pins the empty-grant guard: with no unlisted grants
// to preserve, unchecking the last known tool is refused with the
// allow-every-tool explanation, and no approval can be opened on an empty
// set (an empty AllowedTools means allow-EVERY-tool at runtime).
func TestModelsToolsMinOne(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	// Open tools (row 7); the overlay cursor starts on read_file, the only
	// grant. Space tries to uncheck it and must be refused. KeyUp first
	// selects acme-fast (selection starts at -1, decision 2A).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeySpace, KeyEnter, KeyNo}})

	if !strings.Contains(out, missingModelsToolsMinOne) {
		t.Fatalf("min-one refusal missing; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after refused empty-grant save")
	}
	tools := reloadModels(t, path)[0].Capabilities.AllowedTools
	if len(tools) != 1 || tools[0] != "read_file" {
		t.Fatalf("AllowedTools on disk = %v, want [read_file]", tools)
	}
}

// TestModelsToolsOverlayCancel pins that Esc closes the overlay with the
// nothing-written message and leaves the file byte-identical.
func TestModelsToolsOverlayCancel(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeySpace, KeyEsc}})

	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after tools overlay cancel")
	}
	if !strings.Contains(out, missingModelsCancelled) {
		t.Errorf("cancel message missing; frames:\n%s", out)
	}
}

// TestModelsEscPopsToProviders pins that Esc falls through to the App
// precedence: with the models screen pushed over a root screen, Esc pops it
// back to the root.
func TestModelsEscPopsToProviders(t *testing.T) {
	app, _, _ := newModelsTestApp(t, nil)
	root := &stubScreen{title: "Providers", mode: ModeNav}
	app2 := NewApp(100, 24, DirLTR)
	app2.Push(root)
	app2.Push(app.Top())
	if app2.Depth() != 2 {
		t.Fatalf("depth = %d, want 2", app2.Depth())
	}
	if !app2.Step(KeyEsc) {
		t.Fatal("Esc at depth 2 should keep the app running")
	}
	if app2.Top() != root || app2.Depth() != 1 {
		t.Fatal("Esc did not pop back to the providers screen")
	}
}

// TestModelsBackRowPops pins that the Back action row pops the screen.
func TestModelsBackRowPops(t *testing.T) {
	app, _, _ := newModelsTestApp(t, nil)
	root := &stubScreen{title: "Providers", mode: ModeNav}
	app2 := NewApp(100, 24, DirLTR)
	app2.Push(root)
	app2.Push(app.Top())
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(8)}})
	if app2.Top() != root || app2.Depth() != 1 {
		t.Fatal("Back row did not pop back to the providers screen")
	}
}

// TestModelsReloadOnExternalChange pins the per-frame signature-check reload
// (the prompts-screen pattern): a providers.json mutated behind the
// screen's back (the path the model form returns through) shows up on the
// next frame, with no stale row state.
func TestModelsReloadOnExternalChange(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)

	first := app.CaptureRun(&ScriptedKeys{Keys: nil})
	if !strings.Contains(first, "Context 128000 · Enabled · tools 1") {
		t.Fatalf("pre-mutation frame missing the enabled acme-fast row; frames:\n%s", first)
	}

	// Mutate the registry behind the screen's back: flip acme-fast to
	// explicitly disabled, exactly what the model form's commit would do.
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	disabled := false
	reg.Providers[0].Models[0].Enabled = &disabled
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("rewrite providers.json: %v", err)
	}

	second := app.CaptureRun(&ScriptedKeys{Keys: nil})
	if !strings.Contains(second, "Context 128000 · Disabled · tools 1") {
		t.Fatalf("post-mutation frame did not reload acme-fast; frames:\n%s", second)
	}
	if strings.Contains(second, "Context 128000 · Enabled · tools 1") {
		t.Fatalf("stale enabled row survived the mutation; frames:\n%s", second)
	}
}

// TestModelsMasksSecretShapedFields pins the masking discipline on the rows:
// a credential-shaped model id renders masked (first-5 + ****), never raw,
// in the row label; a credential-shaped explicit family renders masked
// through the registry-slot masker. Benign ids and families stay readable.
func TestModelsMasksSecretShapedFields(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].ID = testfixtures.AKIAExample
	reg.Providers[0].Models[0].Family = testfixtures.AKIAExample
	app, _, _ := newModelsTestAppReg(t, reg, nil)

	out := app.CaptureRun(&ScriptedKeys{Keys: nil})

	if strings.Contains(out, testfixtures.AKIAExample) {
		t.Fatalf("raw credential-shaped id/family rendered; frames:\n%s", out)
	}
	if !strings.Contains(out, "AKIAI****") {
		t.Fatalf("masked form missing; frames:\n%s", out)
	}
	// The sibling model's benign id and family stay readable.
	if !strings.Contains(out, "acme-slow") || !strings.Contains(out, "slow") {
		t.Fatalf("benign sibling row lost; frames:\n%s", out)
	}
}

// TestModelsVerbErrorTailMasked pins the round-6 security fix on the three
// Core failure tails (delete / toggle / tools): a Core op error that embeds
// a credential-shaped operand (for example via a redirected home path) must
// render masked in the message line, matching the sibling screens. The save
// failure also leaves the on-disk file byte-identical.
func TestModelsVerbErrorTailMasked(t *testing.T) {
	const planted = testfixtures.AKIAExample
	newFailingApp := func(t *testing.T) (*App, string) {
		t.Helper()
		tmp := t.TempDir()
		t.Setenv(providers.EnvHome, tmp)
		path := filepath.Join(tmp, "providers.json")
		t.Setenv(providers.EnvProviders, path)
		reg := modelsFixtureReg()
		reg.Path = path
		data, err := json.MarshalIndent(reg, "", "  ")
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("seed providers.json: %v", err)
		}
		// The Core save seam fails with a composite error carrying the
		// credential-shaped operand, exactly the redirected-path shape the
		// review flagged.
		core := settings.NewCore(settings.Deps{
			LoadReg: providers.Load,
			SaveReg: func(*providers.Registry) error {
				return errors.New("write " + planted + "/providers.json: access denied")
			},
		})
		app := NewApp(100, 24, DirLTR)
		app.Push(NewModelsScreen(core, "acme", nil))
		return app, path
	}

	cases := []struct {
		name string
		keys []Key
	}{
		// KeyUp selects acme-fast first on the delete/tools paths (the
		// sticky selection starts at -1, decision 2A); the toggle path
		// selects acme-slow with KeyDown.
		{"delete", []Key{KeyUp, Digit(5), KeyYes}},
		{"toggle", []Key{KeyDown, Digit(6), KeyYes}},
		{"tools", []Key{KeyUp, Digit(7), KeyEnter, KeyYes}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, path := newFailingApp(t)
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json: %v", err)
			}
			out := app.CaptureRun(&ScriptedKeys{Keys: tc.keys})
			if strings.Contains(out, planted) {
				t.Fatalf("raw credential-shaped operand echoed in the error tail; frames:\n%s", out)
			}
			if !strings.Contains(out, planted[:5]+"[REDACTED]") && !strings.Contains(out, "[REDACTED]") {
				t.Fatalf("masked operand missing from the error tail; frames:\n%s", out)
			}
			if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
				t.Fatal("failed Core op mutated the on-disk registry")
			}
		})
	}
}

// TestModelsCapabilitiesNote pins defect-A companion item 1: a model with
// thinking + effort + vision + video renders all four flags in the row
// note, with the effort in parentheses after the thinking label (the CLI
// twin's Capabilities.Summary shape).
func TestModelsCapabilitiesNote(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  "high",
		SupportsVision:   true,
		SupportsVideo:    true,
	}
	app, _, _ := newModelsTestAppReg(t, reg, nil)

	out := app.CaptureRun(&ScriptedKeys{Keys: nil})

	for _, want := range []string{"thinking(high)", "vision", "video"} {
		if !strings.Contains(out, want) {
			t.Fatalf("capability note missing %q; frames:\n%s", want, out)
		}
	}
	// The unselected sibling row still renders (it carries no flags).
	if !strings.Contains(out, "acme-slow") {
		t.Fatalf("frames lost the sibling row; frames:\n%s", out)
	}
}

// TestModelsCapabilitiesExternalRefresh pins defect-A companion item 2: an
// external change to SupportsThinking / ReasoningEffort / SupportsVision /
// SupportsVideo moves modelsSig, so the row note refreshes on the next
// frame without any key press.
func TestModelsCapabilitiesExternalRefresh(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsThinking: true,
		ReasoningEffort:  "high",
		SupportsVision:   true,
		SupportsVideo:    true,
	}
	app, _, path := newModelsTestAppReg(t, reg, nil)

	first := app.CaptureRun(&ScriptedKeys{Keys: nil})
	for _, want := range []string{"thinking(high)", "vision", "video"} {
		if !strings.Contains(first, want) {
			t.Fatalf("pre-mutation frame missing %q; frames:\n%s", want, first)
		}
	}

	// Mutate the registry behind the screen's back: flip vision and video
	// off and lower the effort, exactly what a hand edit would do.
	reg2, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	caps := &reg2.Providers[0].Models[0].Capabilities
	caps.SupportsVision = false
	caps.SupportsVideo = false
	caps.ReasoningEffort = "low"
	data, err := json.MarshalIndent(reg2, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("rewrite providers.json: %v", err)
	}

	second := app.CaptureRun(&ScriptedKeys{Keys: nil})
	for _, want := range []string{"thinking(low)"} {
		if !strings.Contains(second, want) {
			t.Fatalf("post-mutation frame missing %q; frames:\n%s", want, second)
		}
	}
	for _, stale := range []string{"thinking(high)", "vision", "video"} {
		if strings.Contains(second, stale) {
			t.Fatalf("stale capability text %q survived the mutation; frames:\n%s", stale, second)
		}
	}
}

// TestModelsActionRefusesWithoutSelection pins user decision 2A: entering
// the screen and firing a Delete-row digit with no selection refuses with
// the select-a-model message, opens no gate, and writes nothing.
func TestModelsActionRefusesWithoutSelection(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(5)}}) // Delete action row.

	if strings.Contains(out, "Delete Model?") {
		t.Fatalf("delete gate opened without a selection; frames:\n%s", out)
	}
	if got := app.Message(); got != missingModelsSelectModel {
		t.Fatalf("message = %q, want %q", got, missingModelsSelectModel)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after the refused no-selection delete")
	}
	if strings.Contains(out, "●") {
		t.Fatalf("selection marker rendered while nothing is selected; frames:\n%s", out)
	}
}

// TestModelsCursorIdentityAfterDelete pins audit F9: after a delete the
// cursor is restored by row ID onto the Delete action row it was on, so the
// next Enter reopens the gate for the surviving model (the sticky selection
// clamps onto it), never for a different verb or model than the user saw.
// The surviving row also carries the visible selection marker.
func TestModelsCursorIdentityAfterDelete(t *testing.T) {
	app, _, path := newModelsTestApp(t, nil)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(5), KeyYes, KeyEnter}})

	models := reloadModels(t, path)
	if len(models) != 1 || models[0].ID != "acme-slow" {
		t.Fatalf("models on disk = %+v, want only acme-slow", models)
	}
	// The stray Enter after the delete must reopen the delete gate for the
	// survivor, not act on whatever row happened to land at the old index.
	if !strings.Contains(out, "Removes the registry entry for acme-slow in acme.") {
		t.Fatalf("post-delete Enter did not reopen the gate for acme-slow; frames:\n%s", out)
	}
	if !strings.Contains(out, "● acme-slow") {
		t.Fatalf("surviving row carries no selection marker; frames:\n%s", out)
	}
}

// TestModelsSelectionMarker pins the visible selection marker: resting the
// cursor on a model row marks exactly that row, and only that row.
func TestModelsSelectionMarker(t *testing.T) {
	out, _ := runModelsKeys(t, []Key{KeyDown})
	if !strings.Contains(out, "● acme-slow") {
		t.Fatalf("selected row missing the marker; frames:\n%s", out)
	}
	if strings.Contains(out, "● acme-fast") {
		t.Fatalf("unselected row carries the marker; frames:\n%s", out)
	}
}

// TestModelsPageSizeGrowsWithHeight pins audit F8: the list window derives
// from the frame height (pageSizeFor, same formula as the sibling list
// screens) instead of staying at the 10-row default, so a provider with
// more than 10 models shows them all on a tall console.
func TestModelsPageSizeGrowsWithHeight(t *testing.T) {
	reg := modelsFixtureReg()
	models := make([]providers.Model, 0, 15)
	for i := 1; i <= 15; i++ {
		models = append(models, providers.Model{ID: "acme-m" + strconv.Itoa(i)})
	}
	reg.Providers[0].Models = models

	tmp := t.TempDir()
	t.Setenv(providers.EnvHome, tmp)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	reg.Path = path
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("seed providers.json: %v", err)
	}
	core := settings.NewCore(settings.Deps{LoadReg: providers.Load})
	app := NewApp(100, 40, DirLTR)
	scr := NewModelsScreen(core, "acme", nil)
	app.Push(scr)

	out := app.CaptureRun(&ScriptedKeys{Keys: nil})

	ms, ok := scr.(*modelsScreen)
	if !ok {
		t.Fatalf("screen type = %T, want *modelsScreen", scr)
	}
	if got, want := ms.list.PageSize(), 40-6; got != want {
		t.Fatalf("page size = %d, want %d (height - 6 chrome)", got, want)
	}
	if !strings.Contains(out, "[11]") {
		t.Fatalf("row 11 not visible on a 40-row console; frames:\n%s", out)
	}
}

// TestModelsSelectionMoveKeepsWindow pins review finding F-S1: moving the
// sticky selection (the marker follow) must not reset the stepList scroll
// window. On a 26-row list (20 models + 6 action rows) with page size 14,
// sixteen KeyDowns slide the window to offset 3; one KeyUp then moves the
// selection and the offset must stay 3 (the pre-fix rebuild() reset the
// window through SetRows and the offset jumped).
func TestModelsSelectionMoveKeepsWindow(t *testing.T) {
	reg := modelsFixtureReg()
	models := make([]providers.Model, 0, 20)
	for i := 1; i <= 20; i++ {
		models = append(models, providers.Model{ID: "acme-m" + strconv.Itoa(i)})
	}
	reg.Providers[0].Models = models

	tmp := t.TempDir()
	t.Setenv(providers.EnvHome, tmp)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	reg.Path = path
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("seed providers.json: %v", err)
	}
	core := settings.NewCore(settings.Deps{LoadReg: providers.Load})
	app := NewApp(100, 20, DirLTR)
	scr := NewModelsScreen(core, "acme", nil)
	app.Push(scr)
	ms, ok := scr.(*modelsScreen)
	if !ok {
		t.Fatalf("screen type = %T, want *modelsScreen", scr)
	}

	downs := make([]Key, 0, 16)
	for i := 0; i < 16; i++ {
		downs = append(downs, KeyDown)
	}
	app.CaptureRun(&ScriptedKeys{Keys: downs})
	if got, want := ms.list.PageSize(), 14; got != want {
		t.Fatalf("page size = %d, want %d", got, want)
	}
	if c := ms.list.Cursor(); c != 16 {
		t.Fatalf("cursor = %d, want 16 after sixteen KeyDown", c)
	}
	if got, want := ms.list.Offset(), 3; got != want {
		t.Fatalf("offset = %d, want %d before the selection-move check", got, want)
	}

	// One KeyUp moves the sticky selection onto acme-m16 (model index 15):
	// the window must not jump, the cursor row must stay visible, and the
	// marker must follow onto the new selection.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp}})
	if got, want := ms.list.Offset(), 3; got != want {
		t.Fatalf("window jumped on selection move: offset = %d, want %d", got, want)
	}
	if c := ms.list.Cursor(); c != 15 {
		t.Fatalf("cursor = %d, want 15 after KeyUp", c)
	}
	if c, o, p := ms.list.Cursor(), ms.list.Offset(), ms.list.PageSize(); c < o || c >= o+p {
		t.Fatalf("cursor %d outside window [%d,%d) after selection move", c, o, o+p)
	}
	if !strings.Contains(out, "● acme-m16") {
		t.Fatalf("marker did not follow the selection; frames:\n%s", out)
	}
	// CaptureRun emits one frame per key, so the stale-marked earlier frame
	// is still in the output; only the LAST frame must carry no stale
	// marker.
	last := out
	if i := strings.LastIndex(out, "Models: acme"); i >= 0 {
		last = out[i:]
	}
	if strings.Contains(last, "● acme-m17") {
		t.Fatalf("stale marker on the previous selection in the final frame; frames:\n%s", out)
	}

	// Repeated moves around the same spot keep the window pinned.
	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, KeyUp, KeyDown, KeyUp}})
	if got, want := ms.list.Offset(), 3; got != want {
		t.Fatalf("window jumped on repeated selection moves: offset = %d, want %d", got, want)
	}
}

// --- Masking convergence round (H-3, H-6) ---------------------------------

// TestModelsDecoratedRegistrySlotMasked pins H-3: the row label and the
// delete/toggle/tools approval sentences render a disk-borne model id through
// the slot masker. A hand-edited marker-decorated id rode
// providers.MaskOperand's location exemption raw in the row and in every
// sentence, because on this screen no validator ever sees the id. The
// look-alike plant is identity under providers.MaskOperand on every platform,
// so that case discriminates regardless of GOOS; the display masking never
// rewrites the on-disk id.
func TestModelsDecoratedRegistrySlotMasked(t *testing.T) {
	plants := []struct{ name, id string }{
		{"drive decorated id", `C:\` + testfixtures.AWSSecretExample},
		{"look-alike decorated id", "\u2025/ab3def7gh9jk1mnpqr5t"},
	}
	for _, tc := range plants {
		t.Run(tc.name, func(t *testing.T) {
			want := maskSlotOperand(tc.id)
			if want == tc.id {
				t.Fatalf("test premise broken: the id plant is not masked (%q)", want)
			}
			reg := modelsFixtureReg()
			reg.Providers[0].Models[0].ID = tc.id
			app, _, path := newModelsTestAppReg(t, reg, nil)

			// Row label, plus the benign sibling row and its note.
			out := app.CaptureRun(&ScriptedKeys{Keys: nil})
			if strings.Contains(out, tc.id) {
				t.Fatalf("row label echoes the raw decorated id; frames:\n%s", out)
			}
			if !strings.Contains(out, want) {
				t.Fatalf("row label missing the masked id %q; frames:\n%s", want, out)
			}
			if !strings.Contains(out, "acme-slow") || !strings.Contains(out, "Max Out 4096") {
				t.Fatalf("benign sibling row lost its label or note; frames:\n%s", out)
			}

			// Delete approval sentence (KeyNo closes the gate again, so the next
			// key reaches the screen and not the live modal).
			out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(5), KeyNo}})
			if strings.Contains(out, tc.id) {
				t.Fatalf("delete approval echoes the raw decorated id; frames:\n%s", out)
			}
			if wantLine := "Removes the registry entry for " + want + " in acme. Key values are untouched."; !strings.Contains(out, wantLine) {
				t.Fatalf("delete approval missing the masked id sentence %q; frames:\n%s", wantLine, out)
			}

			// Toggle approval sentence.
			out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(6), KeyNo}})
			if strings.Contains(out, tc.id) {
				t.Fatalf("toggle approval echoes the raw decorated id; frames:\n%s", out)
			}
			if wantLine := "Sets enabled for model " + want + " in acme."; !strings.Contains(out, wantLine) {
				t.Fatalf("toggle approval missing the masked id sentence %q; frames:\n%s", wantLine, out)
			}

			// Tools approval sentence.
			out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyUp, Digit(7), KeyEnter, KeyNo}})
			if strings.Contains(out, tc.id) {
				t.Fatalf("tools approval echoes the raw decorated id; frames:\n%s", out)
			}
			if wantLine := "Updates tool grants for model " + want + " in acme."; !strings.Contains(out, wantLine) {
				t.Fatalf("tools approval missing the masked id sentence %q; frames:\n%s", wantLine, out)
			}

			if got := reloadModels(t, path)[0].ID; got != tc.id {
				t.Fatalf("on-disk id = %q, want the planted raw id %q (masking is display-only)", got, tc.id)
			}
		})
	}
}

// TestModelsCapabilitiesSoloSigMutation pins every modelsSig entry on its own: an
// external mutation of one signed field must refresh the row note without a key
// press. The earlier refresh pin mutated vision, video, and effort together, so
// deleting any single signature entry still left the suite green; each subtest
// here mutates exactly one field, so a missing entry leaves the stale note and
// fails.
func TestModelsCapabilitiesSoloSigMutation(t *testing.T) {
	base := func() *providers.Registry {
		reg := modelsFixtureReg()
		reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
			SupportsTools:    true,
			AllowedTools:     []string{"read_file"},
			SupportsThinking: true,
			ReasoningEffort:  "high",
			SupportsVision:   true,
			SupportsVideo:    true,
		}
		return reg
	}
	cases := []struct {
		field  string
		mutate func(*providers.Capabilities)
		before string
		after  string
	}{
		{"tools", func(c *providers.Capabilities) { c.AllowedTools = []string{"read_file", "grep"} }, "tools 1", "tools 2"},
		{"thinking", func(c *providers.Capabilities) { c.SupportsThinking = false }, "thinking(high)", ""},
		{"vision", func(c *providers.Capabilities) { c.SupportsVision = false }, "vision", ""},
		{"video", func(c *providers.Capabilities) { c.SupportsVideo = false }, "video", ""},
		{"effort", func(c *providers.Capabilities) { c.ReasoningEffort = "low" }, "thinking(high)", "thinking(low)"},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			app, _, path := newModelsTestAppReg(t, base(), nil)
			first := app.CaptureRun(&ScriptedKeys{Keys: nil})
			if !strings.Contains(first, tc.before) {
				t.Fatalf("base frame missing the pinned note text %q; frames:\n%s", tc.before, first)
			}

			reg, err := providers.LoadFrom(path)
			if err != nil {
				t.Fatalf("reload registry: %v", err)
			}
			tc.mutate(&reg.Providers[0].Models[0].Capabilities)
			data, err := json.MarshalIndent(reg, "", "  ")
			if err != nil {
				t.Fatalf("marshal mutated registry: %v", err)
			}
			if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
				t.Fatalf("rewrite providers.json: %v", err)
			}

			second := app.CaptureRun(&ScriptedKeys{Keys: nil})
			if tc.after != "" && !strings.Contains(second, tc.after) {
				t.Fatalf("row note did not refresh to %q; frames:\n%s", tc.after, second)
			}
			if strings.Contains(second, tc.before) {
				t.Fatalf("stale note text %q survived the single-field mutation; frames:\n%s", tc.before, second)
			}
		})
	}
}

// TestModelsMessageRoles pins user decision D7-7B at the models screen: the
// delete success opens with the localized common.success label and the Core
// write failure with common.error, so the two compose DIFFERENT palette roles
// (RoleSuccess vs RoleDanger) instead of the neutral amber every message used
// to get. Both frames go through the same composition path the app-level pin
// uses, and the plain tier still emits zero escape bytes for them.
func TestModelsMessageRoles(t *testing.T) {
	newRolesApp := func(t *testing.T, save func(*providers.Registry) error) *App {
		t.Helper()
		tmp := t.TempDir()
		t.Setenv(providers.EnvHome, tmp)
		path := filepath.Join(tmp, "providers.json")
		t.Setenv(providers.EnvProviders, path)
		reg := modelsFixtureReg()
		reg.Path = path
		data, err := json.MarshalIndent(reg, "", "  ")
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("seed providers.json: %v", err)
		}
		deps := settings.Deps{LoadReg: providers.Load}
		if save != nil {
			deps.SaveReg = save
		}
		app := NewApp(100, 24, DirLTR)
		app.Push(NewModelsScreen(settings.NewCore(deps), "acme", nil))
		return app
	}
	// KeyUp first selects acme-fast (the sticky selection starts at -1,
	// decision 2A); the Delete row is 5 and y approves the write.
	deleteKeys := []Key{KeyUp, Digit(5), KeyYes}

	success := newRolesApp(t, nil)
	success.CaptureRun(&ScriptedKeys{Keys: deleteKeys})
	successMsg := success.Message()
	successLabel := Bundle().T("common.success")
	if !strings.HasPrefix(successMsg, successLabel+": ") {
		t.Fatalf("delete success message = %q, want the %q label prefix", successMsg, successLabel)
	}
	successRole := roleOfLine(t, composeFrame(success, nil), successMsg)
	if successRole != RoleSuccess {
		t.Fatalf("success message role = %v, want RoleSuccess", successRole)
	}

	failure := newRolesApp(t, func(*providers.Registry) error {
		return errors.New("registry write refused")
	})
	failure.CaptureRun(&ScriptedKeys{Keys: deleteKeys})
	failureMsg := failure.Message()
	errorLabel := Bundle().T("common.error")
	if !strings.HasPrefix(failureMsg, errorLabel+": ") {
		t.Fatalf("write failure message = %q, want the %q label prefix", failureMsg, errorLabel)
	}
	failureRole := roleOfLine(t, composeFrame(failure, nil), failureMsg)
	if failureRole != RoleDanger {
		t.Fatalf("write failure role = %v, want RoleDanger", failureRole)
	}
	if successRole == failureRole {
		t.Fatalf("success and write failure both paint %v; want different roles", successRole)
	}

	// Plain tier: the same frames resolve to no bytes at all.
	frames := []struct {
		name string
		app  *App
	}{{"success", success}, {"failure", failure}}
	for _, f := range frames {
		var out bytes.Buffer
		p := NewPainter(&out, plainTestCap())
		if code := paintLoop(f.app, &ScriptedKeys{}, p, nil); code != 0 {
			t.Fatalf("%s frame paintLoop exit = %d, want 0", f.name, code)
		}
		if strings.Contains(out.String(), "\x1b") {
			t.Fatalf("%s plain-tier paint emitted escape bytes:\n%q", f.name, out.String())
		}
	}
}

// modelsPinMessage is the confirmation line Space writes on this screen,
// resolved the way the screen resolves it (catalog entry, then the shared
// const) with the model id through the row-label slot masker.
func modelsPinMessage(id string) string {
	return fillHubSlots(missingOr(Bundle(), "list.msg.pinned", listMsgPinned),
		map[string]string{"name": maskSlotOperand(id)})
}

// TestModelsSpacePinSurvivesArrowTransitToTheStickyVerbs is the flow that was
// impossible before the Space pin: the action rows sit below the model block, so
// arrow travel from model 1 down to Edit/Delete passes model 2, and each rest
// re-stole the sticky selection, leaving the verb on the LAST model. Space pins
// model 1, the transit cannot overwrite the pin, and the verb acts on it.
func TestModelsSpacePinSurvivesArrowTransitToTheStickyVerbs(t *testing.T) {
	cases := []struct {
		name string
		down int // Downs from the opening cursor row (model 1) to the verb row
	}{
		// Rows: 1 acme-fast, 2 acme-slow, 3 Add, 4 Edit, 5 Delete, 6 Toggle, 7 Tools, 8 Back.
		{"Edit", 3},
		{"Delete", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []formCall
			app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
				calls = append(calls, formCall{mode: mode, modelID: modelID})
			})
			scr := app.Top().(*modelsScreen)

			app.Step(KeySpace) // pin acme-fast (the opening cursor row)
			if got, want := app.Message(), modelsPinMessage("acme-fast"); got != want {
				t.Fatalf("pin confirmation = %q, want %q", got, want)
			}
			if app.Modal() != nil || len(calls) != 0 {
				t.Fatalf("Space activated something: modal=%v form calls=%v", app.Modal() != nil, calls)
			}
			for i := 0; i < tc.down; i++ {
				app.Step(KeyDown)
				if lines := app.Frame().Lines; !strings.Contains(strings.Join(lines, "\n"), "● acme-fast") {
					t.Fatalf("the pin was stolen after %d Down(s):\n%s", i+1, strings.Join(lines, "\n"))
				}
			}
			app.Step(KeyEnter)
			if tc.name == "Edit" {
				if len(calls) != 1 || calls[0] != (formCall{mode: "edit", modelID: "acme-fast"}) {
					t.Fatalf("Edit after the transit asked for %v, want [edit acme-fast] (the pinned row)", calls)
				}
				return
			}
			m := app.Modal()
			if m == nil {
				t.Fatal("Delete after the transit opened no approval gate")
			}
			if body := strings.Join(m.Body(160), "\n"); !strings.Contains(body, "acme-fast") {
				t.Fatalf("the gate summarizes no acme-fast; body:\n%s", body)
			}
			if scr.locked {
				t.Fatal("the pin survived the verb that consumed it")
			}
		})
	}
}

// TestModelsSpaceOnAnActionRowIsANoOp pins the boundary: the action rows below
// the model block are not selectable, so Space there changes nothing, and a
// Space on an action row AFTER a pin leaves the pin standing.
func TestModelsSpaceOnAnActionRowIsANoOp(t *testing.T) {
	var calls []formCall
	app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
		calls = append(calls, formCall{mode: mode, modelID: modelID})
	})
	scr := app.Top().(*modelsScreen)

	app.Step(KeySpace) // pin acme-fast
	pinned := app.Message()
	app.Step(KeyDown) // cursor on acme-slow; the pin holds
	if scr.sel != 0 {
		t.Fatalf("the pin was overwritten by the rest: sel=%d, want 0", scr.sel)
	}
	for i := 0; i < 3; i++ {
		app.Step(KeyDown) // row 5 = Delete
	}
	app.Step(KeySpace)
	if got := app.Message(); got != pinned {
		t.Fatalf("Space on the Delete row rewrote the message to %q, want the pin line %q", got, pinned)
	}
	if !scr.locked || scr.sel != 0 {
		t.Fatalf("Space on the Delete row dropped the pin: sel=%d locked=%v", scr.sel, scr.locked)
	}
	if app.Modal() != nil || len(calls) != 0 {
		t.Fatalf("Space on the Delete row activated something: modal=%v calls=%v", app.Modal() != nil, calls)
	}
}

// TestModelsSpaceDoesNotShadowTheToolsOverlay pins the interaction the shared
// list-level Space case must not break: the tools overlay owns Space
// (toggle-the-cursor-tool), and it is a live modal, so the key reaches the
// overlay's own Key handler and never the list under it.
func TestModelsSpaceDoesNotShadowTheToolsOverlay(t *testing.T) {
	app, _, _ := newModelsTestApp(t, nil)

	// Pin a model first, then open the tools overlay through the Tools row
	// (row 7) so the list-level Space case is registered and armed.
	app.Step(KeySpace)
	for i := 0; i < 6; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	ov, ok := app.Modal().(*toolsOverlay)
	if !ok {
		t.Fatalf("the Tools row opened %T, want the tools overlay", app.Modal())
	}
	// acme-fast grants read_file only; the overlay refuses to clear the last
	// grant, so toggle the SECOND row (list_dir, ungranted) to see a flip.
	app.Step(KeyDown)
	before := ov.list.Row(ov.list.Cursor()).Label
	app.Step(KeySpace)
	after := ov.list.Row(ov.list.Cursor()).Label
	if before == after {
		t.Fatalf("Space did not toggle the tool in the overlay (label stayed %q)", before)
	}
	if app.Modal() == nil {
		t.Fatal("Space closed the overlay instead of toggling a tool")
	}
	if got, want := app.Message(), modelsPinMessage("acme-fast"); got != want {
		t.Fatalf("Space inside the overlay rewrote the message to %q, want the pin line %q", got, want)
	}
}

// modelsWriteReg rewrites the temp providers.json in place: the external
// mid-list mutation reloadIfChanged has to catch (fix round 1, F-01).
func modelsWriteReg(t *testing.T, path string, reg *providers.Registry) {
	t.Helper()
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("rewrite registry: %v", err)
	}
}

// TestModelsSpacePinDropsWhenItsTargetVanishes is the F-01 regression pin. The
// model selection used to be index-anchored, so an EXTERNAL mid-list deletion of
// the pinned model (picked up by reloadIfChanged) clamped the index onto the
// following model while the lock stayed on: the pin silently re-targeted a model
// the user never chose, and Delete would have opened its gate. The pin is now
// re-anchored by model ID, so a vanished target drops the selection AND the lock,
// which leaves the verbs refusing instead of guessing.
func TestModelsSpacePinDropsWhenItsTargetVanishes(t *testing.T) {
	var calls []formCall
	app, _, path := newModelsTestApp(t, func(mode string, modelID string, a *App) {
		calls = append(calls, formCall{mode: mode, modelID: modelID})
	})
	scr := app.Top().(*modelsScreen)

	// Rows: 1 acme-fast, 2 acme-slow, 3 Add, 4 Edit, 5 Delete, 6 Toggle, 7 Tools, 8 Back.
	app.Step(KeySpace) // pin acme-fast, a MID-LIST row (acme-slow follows it)
	if !scr.locked || scr.selID != "acme-fast" || scr.sel != 0 {
		t.Fatalf("the pin did not take: sel=%d selID=%q locked=%v", scr.sel, scr.selID, scr.locked)
	}

	// External mid-list deletion: acme-fast is gone, so acme-slow slides into index
	// 0 and an index clamp would point the verbs at it.
	reg := modelsFixtureReg()
	reg.Providers[0].Models = reg.Providers[0].Models[1:]
	modelsWriteReg(t, path, reg)
	app.Frame() // reloadIfChanged picks the new registry up, as a live screen does

	if scr.locked || scr.sel != -1 || scr.selID != "" {
		t.Fatalf("the pin outlived its target: sel=%d selID=%q locked=%v", scr.sel, scr.selID, scr.locked)
	}
	if body := strings.Join(app.Frame().Lines, "\n"); strings.Contains(body, "●") {
		t.Fatalf("a vanished pin still marks a row:\n%s", body)
	}

	// The Delete verb (row 4 on the shrunk list: 1 acme-slow, 2 Add, 3 Edit, 4
	// Delete) must refuse instead of opening a gate for the clamped neighbour.
	app.Step(Digit(4))
	if got, want := app.Message(), Bundle().T("models.msg.select_model"); got != want {
		t.Fatalf("Delete after the pinned model vanished = %q, want the refusal %q", got, want)
	}
	if app.Modal() != nil {
		t.Fatal("Delete opened an approval gate for a model nobody selected")
	}
	if len(calls) != 0 {
		t.Fatalf("the vanished pin opened a form: %v", calls)
	}
}

// TestModelsSpaceRePinMovesThePin pins the re-pin transition on this screen:
// another Space on another entity row moves the pin, the message names the new
// target, and the vertices below act on it.
func TestModelsSpaceRePinMovesThePin(t *testing.T) {
	var calls []formCall
	app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
		calls = append(calls, formCall{mode: mode, modelID: modelID})
	})
	scr := app.Top().(*modelsScreen)

	app.Step(KeySpace) // pin acme-fast
	app.Step(KeyDown)  // rest on acme-slow: the pin holds
	if scr.selID != "acme-fast" {
		t.Fatalf("the rest stole the pin: selID=%q", scr.selID)
	}
	app.Step(KeySpace) // re-pin onto acme-slow
	if got, want := app.Message(), modelsPinMessage("acme-slow"); got != want {
		t.Fatalf("re-pin confirmation = %q, want %q", got, want)
	}
	if !scr.locked || scr.selID != "acme-slow" || scr.sel != 1 {
		t.Fatalf("the re-pin did not move: sel=%d selID=%q locked=%v", scr.sel, scr.selID, scr.locked)
	}
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● acme-slow") {
		t.Fatalf("the marker did not follow the re-pin:\n%s", body)
	}

	// Two Downs reach the Edit row (rows: 1 acme-fast, 2 acme-slow, 3 Add, 4 Edit).
	app.Step(KeyDown)
	app.Step(KeyDown)
	app.Step(KeyEnter)
	if len(calls) != 1 || calls[0] != (formCall{mode: "edit", modelID: "acme-slow"}) {
		t.Fatalf("Edit after the re-pin asked for %v, want [edit acme-slow]", calls)
	}
}

// TestModelsSpacePinUnlocksWhenAModelRowIsActivated pins the navigation
// transition on this screen: Enter on a model row opens its form and consumes the
// pin, because a model row activation addresses the CURSOR row, never the pin.
func TestModelsSpacePinUnlocksWhenAModelRowIsActivated(t *testing.T) {
	var calls []formCall
	app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
		calls = append(calls, formCall{mode: mode, modelID: modelID})
	})
	scr := app.Top().(*modelsScreen)

	app.Step(KeySpace) // pin acme-fast
	app.Step(KeyDown)  // rest on acme-slow; the pin holds
	if !scr.locked || scr.selID != "acme-fast" {
		t.Fatalf("the pin did not survive the rest: selID=%q locked=%v", scr.selID, scr.locked)
	}
	app.Step(KeyEnter) // activates the CURSOR row (acme-slow) and clears the pin
	if len(calls) != 1 || calls[0] != (formCall{mode: "edit", modelID: "acme-slow"}) {
		t.Fatalf("model row activation asked for %v, want [edit acme-slow]", calls)
	}
	if scr.locked {
		t.Fatal("the pin survived the navigation into the edit form")
	}
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● acme-slow") {
		t.Fatalf("the marker did not adopt the activated row:\n%s", body)
	}
}

// TestModelsNoSpaceTransitTargetsTheLastRow is the no-Space negative control
// (fix round 1, F-02): the exact key sequence the pin tests use, minus Space,
// still lands the verb on the LAST model row. It keeps the pin tests honest (they
// prove the gesture, not the fixture) and documents the defect the gesture fixes.
func TestModelsNoSpaceTransitTargetsTheLastRow(t *testing.T) {
	var calls []formCall
	app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
		calls = append(calls, formCall{mode: mode, modelID: modelID})
	})
	scr := app.Top().(*modelsScreen)

	for i := 0; i < 3; i++ {
		app.Step(KeyDown) // acme-slow, Add, Edit: every rest re-steals the selection
	}
	app.Step(KeyEnter)
	if len(calls) != 1 || calls[0].modelID != "acme-slow" {
		t.Fatalf("the no-Space transit asked for %v, want the LAST model row (acme-slow)", calls)
	}
	if scr.locked {
		t.Fatal("the no-Space transit locked something; Space is the only pin")
	}
}

// threeModelRegistry is the fixture the T-01 move tests read: three models so the
// pinned row is genuinely MID-LIST (a neighbour on each side), which is the shape
// an index anchor breaks on. The ids are deliberately distinct from the two-model
// fixture so a stale reference cannot pass by matching the wrong record.
func threeModelRegistry() *providers.Registry {
	reg := modelsFixtureReg()
	reg.Providers[0].Models = []providers.Model{
		{ID: "acme-one", Family: "fast"},
		{ID: "acme-two", Family: "mid"},
		{ID: "acme-three", Family: "slow"},
	}
	return reg
}

// TestModelsSpacePinnedTargetSurvivesAMovedRow is the T-01 regression pin: the
// other half of the F-01 identity re-anchor. A pinned model that MOVES through an
// external mutation (a reorder, or a deletion of an entry BEFORE it) must keep its
// target: the selection re-anchors onto the same model ID at its NEW index, the
// lock stays on, the marker follows the row, and the verb below fires on it. The
// reference structure is the providers screen's
// TestProvidersStickyTargetHeldByProviderName ("reordered registry keeps the
// marked provider"); before the re-anchor, the clamp left the pin on the index,
// which after the move names a different model than the user pinned.
func TestModelsSpacePinnedTargetSurvivesAMovedRow(t *testing.T) {
	cases := []struct {
		name string
		// mutate applies the external registry change to a fresh fixture.
		mutate func(reg *providers.Registry)
		// wantIdx is where the pinned model (acme-two) sits after the reload.
		wantIdx int
		// editRow is the digit that reaches the Edit row on the mutated list.
		editRow Key
	}{
		{
			// [one, two, three] -> [three, one, two]: acme-two moves 1 -> 2.
			name: "reordered registry keeps the pinned model",
			mutate: func(reg *providers.Registry) {
				m := reg.Providers[0].Models
				reg.Providers[0].Models = []providers.Model{m[2], m[0], m[1]}
			},
			wantIdx: 2,
			editRow: Digit(5), // rows: 1 three, 2 one, 3 two, 4 Add, 5 Edit
		},
		{
			// [one, two, three] -> [two, three]: the row BEFORE the pin is gone, so
			// acme-two moves 1 -> 0.
			name: "deletion before the pinned model shifts its index",
			mutate: func(reg *providers.Registry) {
				reg.Providers[0].Models = reg.Providers[0].Models[1:]
			},
			wantIdx: 0,
			editRow: Digit(4), // rows: 1 two, 2 three, 3 Add, 4 Edit
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []formCall
			app, _, path := newModelsTestAppReg(t, threeModelRegistry(), func(mode string, modelID string, a *App) {
				calls = append(calls, formCall{mode: mode, modelID: modelID})
			})
			scr := app.Top().(*modelsScreen)

			// Rows: 1 acme-one, 2 acme-two, 3 acme-three, 4 Add, 5 Edit, ...
			app.Step(KeyDown)  // rest on acme-two: the MID entity row
			app.Step(KeySpace) // pin it
			if !scr.locked || scr.selID != "acme-two" || scr.sel != 1 {
				t.Fatalf("the pin did not take: sel=%d selID=%q locked=%v", scr.sel, scr.selID, scr.locked)
			}

			mutated := threeModelRegistry()
			tc.mutate(mutated)
			modelsWriteReg(t, path, mutated)
			app.Frame() // reloadIfChanged re-reads the registry, as a live screen does

			if !scr.locked {
				t.Fatal("the pin dropped although the pinned model still exists; a move must keep it")
			}
			if scr.selID != "acme-two" || scr.sel != tc.wantIdx {
				t.Fatalf("the pin re-anchored to sel=%d selID=%q, want index %d of acme-two", scr.sel, scr.selID, tc.wantIdx)
			}
			body := strings.Join(app.Frame().Lines, "\n")
			if !strings.Contains(body, "● acme-two") {
				t.Fatalf("the marker did not follow the moved pin:\n%s", body)
			}
			if strings.Contains(body, "● acme-one") || strings.Contains(body, "● acme-three") {
				t.Fatalf("the marker also sits on a neighbour:\n%s", body)
			}

			// The verb below the entity block must act on the moved pin, not on
			// whatever now occupies the old index.
			app.Step(tc.editRow)
			if len(calls) != 1 || calls[0] != (formCall{mode: "edit", modelID: "acme-two"}) {
				t.Fatalf("Edit after the move asked for %v, want [edit acme-two]", calls)
			}
			if scr.locked {
				t.Fatal("the pin survived the verb that consumed it")
			}
		})
	}
}

// TestModelsSpacePinUnlocksWhenAVerbConsumesIt is the T-02 close-out: the models
// screen's verb-consumption unlock (screen_models.go activate's `s.locked = false`
// arms) asserted on its own, the way the presets screen already pins it
// (TestPresetsSpacePinUnlocksAfterTheVerb). The transit test asserts it only in its
// Delete arm, and the Edit arm returns before reaching it, so this test fires both
// an Edit (opens the form) and a Tools (opens the overlay and keeps the screen).
func TestModelsSpacePinUnlocksWhenAVerbConsumesIt(t *testing.T) {
	t.Run("Edit opens the form and drops the pin", func(t *testing.T) {
		var calls []formCall
		app, _, _ := newModelsTestApp(t, func(mode string, modelID string, a *App) {
			calls = append(calls, formCall{mode: mode, modelID: modelID})
		})
		scr := app.Top().(*modelsScreen)

		// Rows: 1 acme-fast, 2 acme-slow, 3 Add, 4 Edit, ...
		app.Step(KeySpace) // pin acme-fast
		for i := 0; i < 3; i++ {
			app.Step(KeyDown) // acme-slow, Add, Edit: the pin holds through the transit
		}
		app.Step(KeyEnter) // the Edit verb consumes the pin
		if len(calls) != 1 || calls[0] != (formCall{mode: "edit", modelID: "acme-fast"}) {
			t.Fatalf("Edit asked for %v, want [edit acme-fast]", calls)
		}
		if scr.locked {
			t.Fatal("the pin survived the verb that consumed it")
		}

		// The follow-on-rest rule is live again: the marker follows the cursor.
		app.Step(KeyHome)
		app.Step(KeyDown)
		if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● acme-slow") {
			t.Fatalf("the marker did not follow the cursor after the verb:\n%s", body)
		}
	})

	t.Run("Tools keeps the screen and drops the pin", func(t *testing.T) {
		app, _, _ := newModelsTestApp(t, nil)
		scr := app.Top().(*modelsScreen)

		// Rows: 1 acme-fast, 2 acme-slow, 3 Add, 4 Edit, 5 Delete, 6 Toggle,
		// 7 Tools, 8 Back.
		app.Step(KeySpace) // pin acme-fast
		for i := 0; i < 6; i++ {
			app.Step(KeyDown)
		}
		app.Step(KeyEnter) // the Tools verb consumes the pin and opens its overlay
		if _, ok := app.Modal().(*toolsOverlay); !ok {
			t.Fatalf("the Tools row opened %T, want the tools overlay", app.Modal())
		}
		if scr.locked {
			t.Fatal("the pin survived the Tools verb, so the follow-on-rest rule stays suspended")
		}
		app.Step(KeyEsc) // close the overlay: the screen stays, the pin is already spent
		if app.Modal() != nil {
			t.Fatal("Esc did not close the tools overlay")
		}
		if scr.locked {
			t.Fatal("the pin came back after the overlay closed")
		}
	})
}
