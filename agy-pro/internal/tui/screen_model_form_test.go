package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/testfixtures"
)

// newModelFormTestApp seeds the shared two-model fixture (modelsFixtureReg
// from screen_models_test.go) into a temp providers.json and returns an app
// whose stack is [stub root "Models", form screen]. The live registry is
// never touched; every assertion reloads from the temp path.
func newModelFormTestApp(t *testing.T, modeName, modelID string) (*App, string) {
	t.Helper()
	return newModelFormTestAppReg(t, modelsFixtureReg(), modeName, modelID)
}

// newModelFormTestAppReg is newModelFormTestApp over a caller-supplied
// registry, so a test can vary model capabilities (for example grants
// outside knownToolList) without touching the shared fixture.
func newModelFormTestAppReg(t *testing.T, reg *providers.Registry, modeName, modelID string) (*App, string) {
	t.Helper()
	return newModelFormTestAppNamed(t, reg, "acme", modeName, modelID)
}

// newModelFormTestAppNamed is the full helper: it seeds reg into a temp
// providers.json and builds an app whose stack is [stub root, form screen] for
// the named provider. A caller-supplied provider name lets a test plant a
// hand-edited registry slot (a decorated provider row) that no validator ever
// sees. The live registry is never touched.
func newModelFormTestAppNamed(t *testing.T, reg *providers.Registry, providerName, modeName, modelID string) (*App, string) {
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
	app := NewApp(100, 30, DirLTR)
	app.Push(&stubScreen{title: "Models", mode: ModeNav})
	app.Push(NewModelFormScreen(core, providerName, modeName, modelID))
	return app, path
}

// typeRunes converts text into the rune-carrier keys EDIT mode consumes.
func typeRunes(s string) []Key {
	keys := make([]Key, 0, len(s))
	for _, r := range s {
		keys = append(keys, RuneKey(r))
	}
	return keys
}

// TestModelFormAddEndToEnd drives the full add flow: field entry through
// the grid, the M4 approval summary (masked operands, count, field diff),
// y writing through Core.ModelAdd, sibling models byte-identical, the
// provider env NAME untouched, the success message, and the pop back to the
// caller screen.
func TestModelFormAddEndToEnd(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := []Key{}
	keys = append(keys, typeRunes("acme-new")...)
	keys = append(keys, KeyTab)
	// P2-5: the ID commit auto-fills the family ("acme"); the typed family
	// replaces the prefill, the same keystrokes a user seeing it types.
	for i := 0; i < len("acme"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("swift")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("beta,fast")...)
	keys = append(keys, KeyTab) // enabled keeps the "y" default
	keys = append(keys, KeyTab) // context
	keys = append(keys, typeRunes("200000")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("8192")...)
	keys = append(keys, KeyTab) // thinking (default off)
	keys = append(keys, KeyTab) // reasoning effort (default empty)
	keys = append(keys, KeyTab) // vision (default off)
	keys = append(keys, KeyTab) // video (default off)
	keys = append(keys, KeyTab) // capabilities row
	keys = append(keys, KeyTab) // save row
	keys = append(keys, KeyEnter)

	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	for _, want := range []string{
		"Add Model",
		"Approve Registry Write",
		"Adds model acme-new to provider acme. Key values are untouched.",
		"Model count: 2 -> 3",
		"+ id: acme-new",
		"+ family: swift",
		"+ tags: beta, fast",
		"+ contextLength: 200000",
		"+ maxOutputTokens: 8192",
		"y approve write · n/Esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("add approval missing %q; frames:\n%s", want, out)
		}
	}

	before, _ := json.Marshal(reloadModels(t, path)[0])
	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	if len(models) != 3 {
		t.Fatalf("models on disk = %d, want 3; frames:\n%s", len(models), out)
	}
	m := models[2]
	if m.ID != "acme-new" || m.Family != "swift" || m.ContextLength != 200000 || m.MaxOutputTokens != 8192 {
		t.Fatalf("new model = %+v", m)
	}
	if len(m.Tags) != 2 || m.Tags[0] != "beta" || m.Tags[1] != "fast" {
		t.Fatalf("tags = %v, want [beta fast]", m.Tags)
	}
	if m.Enabled != nil {
		t.Fatalf("enabled = %+v, want nil (default)", m.Enabled)
	}
	after, _ := json.Marshal(models[0])
	if string(after) != string(before) {
		t.Errorf("sibling model changed:\nbefore: %s\nafter:  %s", before, after)
	}
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	p, ok := reg.Provider("acme")
	if !ok || p.APIKeyEnv != "ACME_KEY" {
		t.Fatalf("provider env name = %+v (ok %v), want ACME_KEY untouched", p, ok)
	}
	if !strings.Contains(app.Message(), "Model acme-new added to acme.") {
		t.Errorf("success message missing; message = %q", app.Message())
	}
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Errorf("stack = depth %d top %q, want 1/Models", app.Depth(), app.Top().Title())
	}
}

// TestModelFormAddEmptyIDValidation pins that Enter on the Save row with an
// empty id shows the catalog error, opens no approval, and writes nothing.
func TestModelFormAddEmptyIDValidation(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Model ID must not be empty.") {
		t.Fatalf("empty-id error missing; frames:\n%s", out)
	}
	if strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal opened for an invalid form; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after invalid save")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (still on the form)", app.Depth())
	}
}

// TestModelFormAddDuplicateID pins the live duplicate check: typing an
// existing id shows "already present" on the message line before Enter, and
// Enter blocks the advance. The file stays untouched.
func TestModelFormAddDuplicateID(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-fast"), KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "A model with ID acme-fast already exists in this provider.") {
		t.Fatalf("live duplicate message missing; frames:\n%s", out)
	}
	scr, ok := app.Top().(*modelFormScreen)
	if !ok {
		t.Fatalf("top screen = %T, want *modelFormScreen", app.Top())
	}
	if scr.form.Cursor() != fModelID {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fModelID)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after duplicate-id validation")
	}
}

// TestModelFormSecretGuard pins that a secret-looking free-text value is
// rejected with a masked message. The message line never echoes the value
// (the form field itself shows what the user typed, as any text field does).
func TestModelFormSecretGuard(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("ok-model"), KeyTab)
	keys = append(keys, typeRunes("sk-abcdef0123456789")...)
	keys = append(keys, KeyEnter) // validate family: secret guard blocks
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "(value hidden)") {
		t.Fatalf("masked secret error missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fFamily {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fFamily)
	}
	if strings.Contains(scr.messageLine(), "sk-abcdef0123456789") {
		t.Fatalf("message line echoes the secret-looking value: %q", scr.messageLine())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after secret guard")
	}
}

// TestModelFormAddApprovalCancel pins that n closes the approval with the
// nothing-written message, leaves the file byte-identical, and returns to
// the form with the field values preserved.
func TestModelFormAddApprovalCancel(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-new"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter, KeyNo)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Model count: 2 -> 3") {
		t.Fatalf("approval never rendered; frames:\n%s", out)
	}
	if !strings.Contains(out, missingModelsCancelled) {
		t.Fatalf("cancel message missing; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after approval cancel")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (back on the form)", app.Depth())
	}
	if !strings.Contains(out, "acme-new") {
		t.Fatalf("field values not preserved after cancel; frames:\n%s", out)
	}
}

// TestModelFormDeclinedApprovalEscAsksDiscard pins the round-5 review fix:
// declining the M4 approval must not clear the dirty state, so an immediate
// Esc afterwards still opens the discard-confirm (the dirty Esc gate), not a
// silent pop. Fills a field, opens the approval, answers n, presses Esc.
func TestModelFormDeclinedApprovalEscAsksDiscard(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-new"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter, KeyNo, KeyEsc)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, missingModelFormDiscardTitle) {
		t.Fatalf("discard-confirm missing after declined approval + Esc; frames:\n%s", out)
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (Esc on a dirty form must not pop)", app.Depth())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after declined approval + Esc")
	}

	// The discard-confirm is live: y discards and pops, file untouched.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Fatalf("stack = depth %d top %q, want 1/Models after discard", app.Depth(), app.Top().Title())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after discard")
	}
}

// TestModelFormCleanApprovalDeclineEscPops pins the counterpart: a clean
// form (edit mode, no edits) declining the approval stays clean, so plain
// Esc pops silently through the App precedence with nothing written.
func TestModelFormCleanApprovalDeclineEscPops(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	// Down to the save row, open the approval on the pristine edit seed,
	// decline, then two Escs: the first clears the cancel message, the
	// second pops the clean form. The pristine edit must also show the
	// no-changes marker: the four capability fields seed without diffs.
	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyNo, KeyEsc, KeyEsc}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, missingModelFormDiffNone) {
		t.Fatalf("pristine edit approval missing the no-changes marker; frames:\n%s", out)
	}
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Fatalf("clean decline + Esc should pop silently: depth %d top %q", app.Depth(), app.Top().Title())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after clean decline + Esc")
	}
}

// TestModelFormEditPrefill pins that edit mode pre-fills every field from
// the existing model and does not flag the model's own id as a duplicate.
func TestModelFormEditPrefill(t *testing.T) {
	app, _ := newModelFormTestApp(t, "edit", "acme-fast")
	out := app.CaptureRun(&ScriptedKeys{})

	for _, want := range []string{
		"Edit Model",
		"acme-fast",
		"fast",
		"128000",
		"read_file",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit prefill missing %q; frames:\n%s", want, out)
		}
	}
	if strings.Contains(out, "already exists in this provider") {
		t.Fatalf("own id flagged as duplicate; frames:\n%s", out)
	}
}

// TestModelFormEditUpdate pins the edit write: the enabled flip persists
// (explicit false -> default true), the approval diff shows the change, the
// sibling model is byte-identical, and the caller screen returns.
func TestModelFormEditUpdate(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-slow")
	before, _ := json.Marshal(reloadModels(t, path)[0])

	// Cursor to the enabled field (index 3), replace "n" with "y", then to
	// the save row and through the approval.
	keys := []Key{
		KeyDown, KeyDown, KeyDown,
		KeyBackspace, KeyYes,
		KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown,
		KeyEnter,
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	for _, want := range []string{
		"Updates model acme-slow in provider acme. Key values are untouched.",
		"Model count: 2 -> 2",
		"  enabled: false -> true",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit approval missing %q; frames:\n%s", want, out)
		}
	}

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	if models[1].ID != "acme-slow" {
		t.Fatalf("model id = %q, want acme-slow", models[1].ID)
	}
	if models[1].Enabled != nil {
		t.Fatalf("enabled = %+v, want nil (default true)", models[1].Enabled)
	}
	after, _ := json.Marshal(models[0])
	if string(after) != string(before) {
		t.Errorf("sibling model changed:\nbefore: %s\nafter:  %s", before, after)
	}
	if !strings.Contains(app.Message(), "Model acme-slow updated in acme.") {
		t.Errorf("success message missing; message = %q", app.Message())
	}
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Errorf("stack = depth %d top %q, want 1/Models", app.Depth(), app.Top().Title())
	}
}

// TestModelFormDirtyDiscard pins the dirty Esc gate: a dirty form opens the
// discard-confirm (the guard runs before the App Esc precedence), n returns
// to the form, and y restores the construction values and pops back, with
// the file untouched throughout.
func TestModelFormDirtyDiscard(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := []Key{RuneKey('x'), KeyEsc, KeyNo, KeyEsc, KeyYes}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, missingModelFormDiscardTitle) {
		t.Fatalf("discard-confirm missing; frames:\n%s", out)
	}
	if !strings.Contains(out, missingModelFormDiscardBody) {
		t.Fatalf("discard body missing; frames:\n%s", out)
	}
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Fatalf("stack = depth %d top %q, want 1/Models after discard", app.Depth(), app.Top().Title())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after discard")
	}
}

// TestModelFormCleanEscPops pins that a clean form never arms the guard:
// Esc falls through the plain App precedence and pops to the caller.
func TestModelFormCleanEscPops(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	if !app.Step(KeyEsc) {
		t.Fatal("Esc at depth 2 should keep the app running")
	}
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Fatalf("stack = depth %d top %q, want 1/Models", app.Depth(), app.Top().Title())
	}
}

// TestModelFormModeScope pins the mode contract: ModeEdit on a text field,
// ModeNav on the Save row.
func TestModelFormModeScope(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)
	if scr.Mode() != ModeEdit {
		t.Fatalf("mode on a field = %v, want ModeEdit", scr.Mode())
	}
	for i := 0; i < fCount; i++ {
		app.Step(KeyDown)
	}
	if scr.Mode() != ModeNav {
		t.Fatalf("mode on the save row = %v, want ModeNav", scr.Mode())
	}
}

// TestModelFormToolsFromForm pins the capabilities row: Enter opens the
// shared tool-picker overlay, Space toggles, Enter persists the selection
// into the form, and the approval diff carries the tools line; y writes
// AllowedTools with SupportsTools set.
//
// Moved pin (add-path default change): the add form now seeds the shared
// grants (every known tool but run_command), so the single Space here UNCHECKS
// read_file instead of checking it. The round trip this test proves (overlay
// toggle -> form state -> approval line -> disk) is unchanged; only the
// direction of the toggle and the resulting list moved.
func TestModelFormToolsFromForm(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-tool"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab)
	keys = append(keys, KeyEnter, KeySpace, KeyEnter) // open overlay, untoggle read_file, save
	keys = append(keys, KeyTab, KeyEnter)             // save row, approval
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	for _, want := range []string{
		missingModelsToolsTitle,
		"+ tools: list_dir", // the seeded 8 minus the row Space unchecked
		"Model count: 2 -> 3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tools flow missing %q; frames:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "[ ] read_file") {
		t.Fatalf("overlay untoggle missing; frames:\n%s", out)
	}

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	m := models[2]
	if m.ID != "acme-tool" {
		t.Fatalf("new model id = %q, want acme-tool", m.ID)
	}
	wantTools := []string{"list_dir", "grep", "glob", "write_file", "replace_in_file", "search_web", "read_url_content"}
	if strings.Join(m.Capabilities.AllowedTools, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("AllowedTools = %v, want %v", m.Capabilities.AllowedTools, wantTools)
	}
	if !m.Capabilities.SupportsTools {
		t.Fatal("SupportsTools = false, want true with tools granted")
	}
}

// TestModelFormAddSeedsDefaultToolGrants pins the owner-directed add default
// end to end at the form level: a brand-new model opens with exactly
// providers.DefaultAllowedTools checked (the runtime universe minus
// run_command), the picker paints those rows checked and run_command unchecked,
// and the seed is also the form's BASE, so a pristine form is not dirty (the
// Esc discard gate must not fire on a form the user never touched).
func TestModelFormAddSeedsDefaultToolGrants(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	want := providers.DefaultAllowedTools
	if len(want) == 0 {
		t.Fatal("premise: the add default is empty")
	}
	if got := strings.Join(scr.tools, ","); got != strings.Join(want, ",") {
		t.Fatalf("add form seeded tools = %q; want %q", got, strings.Join(want, ","))
	}
	if got := strings.Join(scr.baseTools, ","); got != strings.Join(want, ",") {
		t.Fatalf("add form baseTools = %q; want the seed %q (a pristine form must not read as dirty)", got, strings.Join(want, ","))
	}
	if scr.dirty() {
		t.Fatal("pristine add form reports dirty; the seed leaked past baseTools")
	}
	for _, name := range scr.tools {
		if name == "run_command" {
			t.Fatal("add form seeded run_command; exec must stay an explicit pick")
		}
	}
	// What the seed means at runtime: the record the form would save permits
	// eight of the nine tools, and run_command is the one it refuses.
	caps := providers.Capabilities{SupportsTools: true, AllowedTools: want}
	for _, name := range knownToolList {
		if got, expected := caps.ToolPermitted(name), name != "run_command"; got != expected {
			t.Fatalf("ToolPermitted(%q) = %v on the seeded capabilities; want %v", name, got, expected)
		}
	}
	for _, name := range []string{"custom_tool", "exec"} {
		if caps.ToolPermitted(name) {
			t.Fatalf("ToolPermitted(%q) = true; the seed must grant the named tools only", name)
		}
	}

	// The picker paints the seed: eight checked rows, run_command unchecked.
	// No known tool name is a prefix of another, so the bare row markers are
	// unambiguous (the last row carries no padding).
	keys := []Key{}
	for i := 0; i < fCaps; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, missingModelsToolsTitle) {
		t.Fatalf("tool picker did not open from the capabilities row; frames:\n%s", out)
	}
	for _, name := range want {
		if !strings.Contains(out, "[x] "+name) {
			t.Fatalf("picker row %q is not checked; frames:\n%s", name, out)
		}
		if strings.Contains(out, "[ ] "+name) {
			t.Fatalf("picker paints %q as unchecked and checked; frames:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "[ ] run_command") {
		t.Fatalf("picker row run_command is not unchecked; frames:\n%s", out)
	}
}

// TestModelFormContextInvalid pins the integer validator: a non-numeric
// context length blocks the advance with the catalog message and writes
// nothing.
func TestModelFormContextInvalid(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab) // to context
	keys = append(keys, typeRunes("abc")...)
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Context length must be a positive number, or -1 for server maximum.") {
		t.Fatalf("context error missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fContext {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fContext)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after invalid context")
	}
}

// TestLooksSecretValueModelIDShape pins the guard in both directions: the
// project's own example model IDs (27-39 runes, separator-carrying) must be
// accepted, while a 20-rune AKIA-class key and any 16+ rune mixed opaque
// token must be rejected. The old blanket len >= 24 rule failed the first
// direction and missed the second.
//
// FIX-7 moved the guard to the shared NAME authority, so two entries that this
// pin used to accept are now refused: a 26-rune single-class word and a 16-digit
// serial are render-rewritten by the registry-slot authority that paints this
// screen's rows ("abcde****", "12345****"), and a form that accepts a value its
// own frames mask as a key is the masked-display/raw-persist divergence FIX-7
// closes. Both moved entries are synthetic shape probes, not model IDs; every
// realistic ID (separator-carrying) keeps its verdict, which
// TestModelFormIDAuthorityAddsOnlyCredentialClass pins as a live table.
func TestLooksSecretValueModelIDShape(t *testing.T) {
	accept := []string{
		"anthropic/claude-3.5-sonnet",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo", // 39 runes
		"Qwen/Qwen2.5-72B-Instruct-Turbo",
		"meta-llama/llama-3.3-70b-instruct",
		"claude-3-5-sonnet-20241022",
		"acme-fast",
		"gpt-4.1-mini",
	}
	for _, id := range accept {
		if looksSecretValue(id) {
			t.Errorf("looksSecretValue(%q) = true, want false (legitimate model id/tag)", id)
		}
	}
	reject := []string{
		testfixtures.AKIAExample,      // 20-rune AWS access-key id shape
		"AbCdEfGh0123456jKlMnoP",      // 22-rune opaque mixed token
		testfixtures.AWSSecretExample, // 40-rune AWS secret, "/" runes (round-6 F-01)
		"Ab3dEf7Gh9jK1mNp+/Qr=",       // 21-rune base64 token, "+/=" runes (round-6 F-01)
		"sk-abc",                      // known prefix, any length
		testfixtures.GroqKeyShort,     // 17-rune prefix token
		"dckr_pat-abcdef0123456789",   // docker pat head
		"bearer abc",                  // bearer head
		testfixtures.JWTFullBadSep,    // JWT head
		testfixtures.GHPAlphabet,      // GitHub token head
		"abcdefghijklmnopqrstuvwxyz",  // 26 runes, single-class word: slot authority masks it (FIX-7)
		"1234567890123456",            // 16 runes, digits only: slot authority masks it (FIX-7)
	}
	for _, v := range reject {
		if !looksSecretValue(v) {
			t.Errorf("looksSecretValue(%q) = false, want true (credential-shaped)", v)
		}
	}
}

// TestModelFormAddLongModelID pins that the headline model-add capability
// accepts the project's own 39-rune example ID end to end: no secret-guard
// block, the approval diff names the id, and the write lands on disk.
func TestModelFormAddLongModelID(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	longID := "meta-llama/Llama-3.3-70B-Instruct-Turbo"

	keys := append(typeRunes(longID), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if strings.Contains(out, "looks like secret/key material") {
		t.Fatalf("39-rune example model id rejected by the secret guard; frames:\n%s", out)
	}
	if !strings.Contains(out, "+ id: "+longID) {
		t.Fatalf("approval missing the long id; frames:\n%s", out)
	}
	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	models := reloadModels(t, path)
	if len(models) != 3 || models[2].ID != longID {
		t.Fatalf("models on disk = %d, want 3 with id %q; frames:\n%s", len(models), longID, out)
	}
}

// TestModelFormSecretGuardOpaqueToken pins the narrowed guard on a seeded
// credential-shaped input: a 20-rune AKIA-class value is rejected at the id
// field, the value never appears on the message line or in the save error,
// the approval modal never opens, and nothing is written.
func TestModelFormSecretGuardOpaqueToken(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	seed := testfixtures.AKIAExample

	keys := append(typeRunes(seed), KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "(value hidden)") {
		t.Fatalf("masked secret error missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fModelID {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fModelID)
	}
	if msg := scr.messageLine(); strings.Contains(msg, seed) {
		t.Fatalf("message line echoes the credential-shaped value: %q", msg)
	}
	// Now drive the save row: validation must block the approval modal.
	for i := 0; i < fCount; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	if strings.Contains(app.CaptureRun(&ScriptedKeys{}), "y approve write") {
		t.Fatal("approval modal opened for a credential-shaped id")
	}
	if msg := scr.messageLine(); msg != "" && strings.Contains(msg, seed) {
		t.Fatalf("save error echoes the credential-shaped value: %q", msg)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after credential-shaped input")
	}
}

// TestModelFormToolsUnionPassthrough pins the union semantics: grants
// outside knownToolList (custom names a hand-edited registry can carry)
// survive the edit seed, the overlay persist, and the commit, and the
// approval diff's before column shows the FULL original list, so a removal
// can never hide.
func TestModelFormToolsUnionPassthrough(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools: true,
		AllowedTools:  []string{"custom_tool_alpha", "read_file", "third_party_tool"},
	}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	// Caps row, open the picker, additionally grant write_file, save.
	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeySpace, KeyEnter) // write_file row, toggle, save
	keys = append(keys, KeyTab, KeyEnter)                                       // save row, approval
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	// Moved pin: the width clamp moved to the shared composition path
	// (phase-3 fix-2), so the approval diff reaches App.Frame already cut at
	// the frame width. The old expectation asserted the pre-clamp line (113
	// cells at width 100), content only the unclamped test path ever held; the
	// byte-exact expectation is now the clamped form, and the raw line must be
	// absent. The before column stays a full-list assertion (it fits).
	//
	// Moved again (F-B): the before column is the RAW on-disk grant list, in file
	// order, because that is the operand the save starts from. This fixture is
	// deliberately non-canonical (custom_tool_alpha first), so the column now
	// prints the file order instead of the normalized one, and the after column
	// shows the canonical write the toggle produces.
	full := "  tools: custom_tool_alpha, read_file, third_party_tool -> read_file, write_file, custom_tool_alpha, third_party_tool"
	clamped := cellwidth.Truncate(full, app.Width())
	if clamped == full || !strings.Contains(clamped, "->") {
		t.Fatalf("clamped diff = %q; want the width-%d cut that keeps the arrow", clamped, app.Width())
	}
	for _, want := range []string{
		"custom_tool_alpha, read_file, third_party_tool", // full before column
		clamped,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("union diff missing %q; frames:\n%s", want, out)
		}
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

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	wantTools := []string{"read_file", "write_file", "custom_tool_alpha", "third_party_tool"}
	if len(m.Capabilities.AllowedTools) != len(wantTools) {
		t.Fatalf("AllowedTools = %v, want %v; frames:\n%s", m.Capabilities.AllowedTools, wantTools, out)
	}
	for i, w := range wantTools {
		if m.Capabilities.AllowedTools[i] != w {
			t.Fatalf("AllowedTools = %v, want %v", m.Capabilities.AllowedTools, wantTools)
		}
	}
	if !m.Capabilities.SupportsTools {
		t.Fatal("SupportsTools = false, want true")
	}
}

// TestModelFormToolsEmptyRefused pins the empty-selection contract: the
// picker refuses to persist zero grants (the engine reads AllowedTools empty
// with SupportsTools set as allow-EVERY-tool), in both directions: an all-rows
// overlay save on a fresh add, and unchecking the last grant in edit mode.
//
// Moved pin (add-path default change): a fresh add form no longer starts
// empty, so reaching the refusal takes one uncheck per seeded row. The add
// half also proves the seed itself is what lands on disk when the user leaves
// the picker alone, run_command excluded.
func TestModelFormToolsEmptyRefused(t *testing.T) {
	// Add mode: open the picker and uncheck every seeded row. The overlay rows
	// follow knownToolList, and run_command is already unchecked there (the
	// default excludes it), so it is stepped over rather than toggled.
	app, path := newModelFormTestApp(t, "add", "")
	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab)
	keys = append(keys, KeyEnter) // open picker
	for i, name := range knownToolList {
		if i > 0 {
			keys = append(keys, KeyDown)
		}
		if name == "run_command" {
			continue
		}
		keys = append(keys, KeySpace)
	}
	keys = append(keys, KeyEnter) // save with nothing checked
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, missingModelsToolsMinOne) {
		t.Fatalf("empty-selection refusal missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if got, want := strings.Join(scr.tools, ","), strings.Join(providers.DefaultAllowedTools, ","); got != want {
		t.Fatalf("tools = %q, want %q kept after refusal", got, want)
	}
	// The untouched seed is a complete selection: saving carries it to disk,
	// with exec still out.
	keys = []Key{KeyTab, KeyEnter}
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "+ tools: read_file") {
		t.Fatalf("approval missing the seeded tools line; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.ID != "acme-x" {
		t.Fatalf("model id = %q, want acme-x", m.ID)
	}
	if !m.Capabilities.SupportsTools {
		t.Fatalf("capabilities = %+v, want SupportsTools true with the seeded grants", m.Capabilities)
	}
	if got, want := strings.Join(m.Capabilities.AllowedTools, ","), strings.Join(providers.DefaultAllowedTools, ","); got != want {
		t.Fatalf("AllowedTools on disk = %q, want %q", got, want)
	}
	if m.Capabilities.ToolPermitted("run_command") {
		t.Fatal("ToolPermitted(run_command) = true on a model seeded with the default grants")
	}

	// Edit mode: uncheck the last grant, Enter; the refusal keeps the
	// previous selection and nothing is written.
	app2, path2 := newModelFormTestApp(t, "edit", "acme-fast")
	diskBefore, _ := os.ReadFile(path2)
	keys = []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
	keys = append(keys, KeySpace, KeyEnter) // uncheck read_file, save
	out = app2.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, missingModelsToolsMinOne) {
		t.Fatalf("edit-mode empty refusal missing; frames:\n%s", out)
	}
	scr2 := app2.Top().(*modelFormScreen)
	if len(scr2.tools) != 1 || scr2.tools[0] != "read_file" {
		t.Fatalf("tools = %v, want [read_file] preserved after refusal", scr2.tools)
	}
	if got, _ := os.ReadFile(path2); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after refused empty selection")
	}
}

// TestModelFormEditKeepsExistingToolGrants pins the other half of the
// add-path default: EDIT keeps the record's own grants byte-identically. The
// record that matters most is the one with an empty AllowedTools list, which
// the engine reads as allow-every-tool (providers.Capabilities.ToolPermitted):
// seeding the add default there would silently narrow a model that could use
// the whole universe down to eight tools, and the approval would present it as
// an untouched save. The single-grant row pins the same rule from the other
// side, so a future edit seed cannot pass by matching the default by luck.
func TestModelFormEditKeepsExistingToolGrants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []string
	}{
		{name: "empty list stays empty", tools: nil},
		{name: "single grant stays single", tools: []string{"read_file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := modelsFixtureReg()
			reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
				SupportsTools: true,
				AllowedTools:  tc.tools,
			}
			app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json: %v", err)
			}

			scr := app.Top().(*modelFormScreen)
			if got, want := strings.Join(scr.tools, ","), strings.Join(tc.tools, ","); got != want {
				t.Fatalf("edit form seeded tools = %q; want the record's own %q (the add default must not reach the edit path)", got, want)
			}

			keys := []Key{}
			for i := 0; i < fCount; i++ {
				keys = append(keys, KeyDown)
			}
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})
			if strings.Contains(out, "  tools:") {
				t.Fatalf("untouched edit rendered a tools change line; frames:\n%s", out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

			if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
				t.Fatalf("on-disk JSON changed after an untouched edit save:\nbefore: %s\nafter:  %s", diskBefore, got)
			}
			m := reloadModels(t, path)[0]
			if got, want := strings.Join(m.Capabilities.AllowedTools, ","), strings.Join(tc.tools, ","); got != want {
				t.Fatalf("AllowedTools on disk = %q, want %q", got, want)
			}
			if !m.Capabilities.SupportsTools {
				t.Fatal("SupportsTools flipped off on an untouched edit save")
			}
		})
	}
}

// TestModelFormEditPreservesRawGrantBytes pins F-B: an EDIT that leaves the
// grants alone writes back the grant list the record already carries, byte for
// byte, even when that list is non-canonical (out of the picker's order, with a
// repeat). Seeding the editable selection through normalizeTools is fine for the
// GUI, but committing that selection rewrote hand-authored registries: the
// untouched save re-sorted ["grep","read_file","grep"] to ["read_file","grep"]
// and the approval showed no tools line at all, so the rewrite was invisible.
// The second half pins the other side of the same contract: once the overlay
// actually changes the selection, the committed list IS the canonical one the
// picker showed, and the approval's before column carries the raw file order so
// the reordering + deduplication is on the consent surface.
func TestModelFormEditPreservesRawGrantBytes(t *testing.T) {
	raw := []string{"grep", "read_file", "grep"}
	newRawFixture := func(t *testing.T) (*App, string) {
		t.Helper()
		reg := modelsFixtureReg()
		reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
			SupportsTools: true,
			AllowedTools:  append([]string(nil), raw...),
		}
		return newModelFormTestAppReg(t, reg, "edit", "acme-fast")
	}

	// Untouched save: the form shows the canonical view, the write is the raw list.
	app, path := newRawFixture(t)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	scr := app.Top().(*modelFormScreen)
	if got := strings.Join(scr.tools, ","); got != "read_file,grep" {
		t.Fatalf("picker selection = %q; want the canonical view read_file,grep", got)
	}
	if got := strings.Join(scr.origTools, ","); got != "grep,read_file,grep" {
		t.Fatalf("raw snapshot = %q; want the file order grep,read_file,grep", got)
	}

	keys := []Key{}
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if strings.Contains(out, "  tools:") {
		t.Fatalf("untouched edit rendered a tools change line; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatalf("on-disk JSON changed after an untouched non-canonical edit save:\nbefore: %s\nafter:  %s", diskBefore, got)
	}
	if got := strings.Join(reloadModels(t, path)[0].Capabilities.AllowedTools, ","); got != "grep,read_file,grep" {
		t.Fatalf("AllowedTools on disk = %q; want the raw list byte for byte", got)
	}

	// Toggle then save: canonical order on disk, raw order in the before column.
	app2, path2 := newRawFixture(t)
	keys = []Key{}
	for i := 0; i < fCaps; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)                                               // open the picker
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeySpace, KeyEnter) // grant write_file, persist
	keys = append(keys, KeyTab, KeyEnter)                                       // save row, approval
	out = app2.CaptureRun(&ScriptedKeys{Keys: keys})

	wantLine := "  tools: grep, read_file, grep -> read_file, grep, write_file"
	if !strings.Contains(out, wantLine) {
		t.Fatalf("approval diff missing the raw-before/canonical-after line %q; frames:\n%s", wantLine, out)
	}
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if got := strings.Join(reloadModels(t, path2)[0].Capabilities.AllowedTools, ","); got != "read_file,grep,write_file" {
		t.Fatalf("AllowedTools on disk = %q; want the canonical list read_file,grep,write_file", got)
	}
}

// TestModelFormEditStaleBaseRawGrantDrift pins G-1: the stale-base guard must
// compare the grants RAW to RAW. With the form opened on the non-canonical
// ["grep","read_file","grep"], an external writer that lands the deduplicated,
// reordered ["read_file","grep"] is a real base change — the untouched commit
// would put the stale raw bytes back and silently revert it. A normalized
// comparison calls those two lists equal, returns "nothing changed", and waves
// the revert through; raw-vs-raw refuses it with the stale-base sentence and
// leaves the external bytes in place.
func TestModelFormEditStaleBaseRawGrantDrift(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools: true,
		AllowedTools:  []string{"grep", "read_file", "grep"},
	}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	// The external writer lands the normalized-equivalent list.
	disk, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	disk.Providers[0].Models[0].Capabilities.AllowedTools = []string{"read_file", "grep"}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write mutated registry: %v", err)
	}
	mutated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mutated providers.json: %v", err)
	}

	keys := make([]Key, 0, fCount+2)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter, KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	// Bytes first: the contract is that the external list survives untouched.
	if got, _ := os.ReadFile(path); string(got) != string(mutated) {
		t.Fatalf("on-disk JSON changed despite the raw grant drift:\nbefore: %s\nafter:  %s", mutated, got)
	}
	if got := strings.Join(reloadModels(t, path)[0].Capabilities.AllowedTools, ","); got != "read_file,grep" {
		t.Fatalf("externally written grants = %q; want read_file,grep (the stale raw must not come back)", got)
	}
	if msg := app.Message(); !strings.Contains(msg, missingModelFormStaleBase) {
		t.Fatalf("raw grant drift not refused as a stale base; message = %q\nframes:\n%s", msg, out)
	}
	if !strings.Contains(out, "The model changed on disk since this form opened.") {
		t.Fatalf("stale-base refusal did not reach the frame; frames:\n%s", out)
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (form stays open after the refusal)", app.Depth())
	}
}

// TestModelFormAddCapabilityFlags pins the phase-2 capability parity
// fields in add mode under the P2-4 defaults: thinking and vision are seeded
// "y" (left untouched), effort=high is typed, and video is overridden to "n";
// all four flags land on disk and the M4 approval diff shows the signed lines.
func TestModelFormAddCapabilityFlags(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-caps"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to thinking
	keys = append(keys, KeyTab)                                                            // thinking seeded "y", untouched
	keys = append(keys, typeRunes("high")...)
	keys = append(keys, KeyTab) // vision seeded "y", untouched
	keys = append(keys, KeyTab) // to video
	keys = append(keys, KeyBackspace)
	keys = append(keys, typeRunes("n")...)
	keys = append(keys, KeyTab, KeyTab, KeyEnter) // caps row, save row, approval
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	for _, want := range []string{
		"+ thinking: true",
		"+ reasoningEffort: high",
		"+ vision: true",
		"y approve write",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("capability approval missing %q; frames:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+ video:") {
		t.Fatalf("video=n (the default) must not appear in the add diff; frames:\n%s", out)
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.ID != "acme-caps" {
		t.Fatalf("new model id = %q, want acme-caps", m.ID)
	}
	if !m.Capabilities.SupportsThinking {
		t.Fatal("SupportsThinking = false, want true")
	}
	if m.Capabilities.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", m.Capabilities.ReasoningEffort)
	}
	if !m.Capabilities.SupportsVision {
		t.Fatal("SupportsVision = false, want true")
	}
	if m.Capabilities.SupportsVideo {
		t.Fatal("SupportsVideo = true, want false for video=n")
	}
}

// TestModelFormEditEffortChangedDiff pins the edit-mode effort round trip:
// the seeded low changes to an uppercase HIGH entry, the approval diff shows
// the normalized change line, and the persisted value is lowercased.
func TestModelFormEditEffortChangedDiff(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  "low",
	}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	// Down to the effort field (index 7), replace "low" with "HIGH".
	keys := []Key{
		KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown,
		KeyBackspace, KeyBackspace, KeyBackspace,
	}
	keys = append(keys, typeRunes("HIGH")...)
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter) // save row, approval
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	for _, want := range []string{
		"Updates model acme-fast in provider acme. Key values are untouched.",
		"  reasoningEffort: low -> high",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("effort edit approval missing %q; frames:\n%s", want, out)
		}
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	if m.Capabilities.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort on disk = %q, want high", m.Capabilities.ReasoningEffort)
	}
	if !m.Capabilities.SupportsThinking {
		t.Fatal("SupportsThinking = false, want true (untouched seed preserved)")
	}
	if !m.Capabilities.SupportsTools || len(m.Capabilities.AllowedTools) != 1 || m.Capabilities.AllowedTools[0] != "read_file" {
		t.Fatalf("tool grants changed: %+v", m.Capabilities)
	}
}

// TestModelFormEffortInvalid pins the closed-set effort validator: an
// out-of-set value ("extreme") blocks the field advance and the save-row
// validation, moves the cursor to the effort field (the repair site), opens
// no approval, and writes nothing.
func TestModelFormEffortInvalid(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to effort
	keys = append(keys, typeRunes("extreme")...)
	keys = append(keys, KeyEnter)                                 // field advance: blocked
	keys = append(keys, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter) // save row: blocked
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Reasoning effort must be one of") {
		t.Fatalf("effort validation message missing; frames:\n%s", out)
	}
	if strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal opened for an invalid effort; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fEffort {
		t.Fatalf("cursor = %d, want %d (focus on the repair site)", scr.form.Cursor(), fEffort)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after invalid effort")
	}
}

// TestModelFormEffortSecretGuard pins the two independent halves of the effort
// secret guard (G-M6): the live check on the focused field (messageLine's
// fEffort case) refuses while the value is only typed, and the validator
// (validateEffort) refuses on Enter with the same non-echoing message instead
// of falling through to the closed-set message. Each half is asserted with its
// own observable, so removing either one fails this pin on its own.
func TestModelFormEffortSecretGuard(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	seed := testfixtures.AKIAExample

	// Half 1: type the value and stop. No validator has run, so the only thing
	// that can raise the masked warning is the live check.
	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to effort
	keys = append(keys, typeRunes(seed)...)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fEffort {
		t.Fatalf("cursor = %d, want %d (still on the effort field)", scr.form.Cursor(), fEffort)
	}
	if scr.form.Message() != "" {
		t.Fatalf("validator message present before Enter: %q", scr.form.Message())
	}
	if scr.errMsg != "" {
		t.Fatalf("save error present before Enter: %q", scr.errMsg)
	}
	if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
		t.Fatalf("live secret check missing on the effort field; messageLine = %q", msg)
	}
	if strings.Contains(scr.messageLine(), seed) {
		t.Fatalf("message line echoes the credential-shaped value: %q", scr.messageLine())
	}
	if !strings.Contains(out, "(value hidden)") {
		t.Fatalf("live masked warning missing from the frame; frames:\n%s", out)
	}

	// Half 2: Enter must run the validator's secret guard, not the closed-set
	// check, and must block the advance.
	app.Step(KeyEnter)
	if scr.form.Cursor() != fEffort {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fEffort)
	}
	if msg := scr.form.Message(); !strings.Contains(msg, "(value hidden)") {
		t.Fatalf("validator secret guard did not fire; form message = %q", msg)
	}
	if strings.Contains(scr.form.Message(), "Reasoning effort must be one of") {
		t.Fatalf("validator fell through to the closed-set message: %q", scr.form.Message())
	}

	// The save row refuses too, with no approval and nothing written.
	keys = []Key{KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter}
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})
	if strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal opened for a credential-shaped effort; frames:\n%s", out)
	}
	if scr.form.Cursor() != fEffort {
		t.Fatalf("cursor = %d, want %d (focus on the repair site)", scr.form.Cursor(), fEffort)
	}
	if strings.Contains(scr.messageLine(), seed) {
		t.Fatalf("save error echoes the credential-shaped value: %q", scr.messageLine())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after secret-shaped effort")
	}
}

// TestModelFormEditCapabilityPrefill pins that edit mode seeds all four
// capability fields from the original record (booleans as "y", effort
// lowercased), the seed counts as clean, and an immediate buildModel keeps
// the original values (no invisible pass-through, no phantom diff).
func TestModelFormEditCapabilityPrefill(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  "medium",
		SupportsVision:   true,
		SupportsVideo:    true,
	}
	app, _ := newModelFormTestAppReg(t, reg, "edit", "acme-fast")
	scr := app.Top().(*modelFormScreen)

	if got := scr.form.Value(fThinking); got != "y" {
		t.Fatalf("thinking seed = %q, want y", got)
	}
	if got := scr.form.Value(fEffort); got != "medium" {
		t.Fatalf("effort seed = %q, want medium", got)
	}
	if got := scr.form.Value(fVision); got != "y" {
		t.Fatalf("vision seed = %q, want y", got)
	}
	if got := scr.form.Value(fVideo); got != "y" {
		t.Fatalf("video seed = %q, want y", got)
	}
	if scr.dirty() {
		t.Fatal("prefilled capability fields flagged the form dirty")
	}
	m, _ := scr.buildModel()
	if !m.Capabilities.SupportsThinking || m.Capabilities.ReasoningEffort != "medium" ||
		!m.Capabilities.SupportsVision || !m.Capabilities.SupportsVideo {
		t.Fatalf("buildModel caps = %+v, want the original four flags", m.Capabilities)
	}
	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, "medium") {
		t.Fatalf("edit frame missing the seeded effort; frames:\n%s", out)
	}
}

// TestModelFormYesNoInvalid pins the negative side of the y/n validator
// (F-M6a): an out-of-set capability flag value ("maybe") is refused with
// the yes/no message, the advance and the save-row validation both block,
// the cursor lands on the failing field, and nothing is written.
func TestModelFormYesNoInvalid(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to thinking
	keys = append(keys, typeRunes("maybe")...)
	keys = append(keys, KeyEnter)                                         // field advance: blocked
	keys = append(keys, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter) // save row: blocked
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Value must be y or n.") {
		t.Fatalf("y/n validation message missing; frames:\n%s", out)
	}
	if strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal opened for an invalid flag value; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fThinking {
		t.Fatalf("cursor = %d, want %d (focus on the repair site)", scr.form.Cursor(), fThinking)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after invalid flag value")
	}
}

// TestModelFormValidEffortValuesParity pins the form's closed effort set to
// the canonical gateway set (F-M6b): drift between the two would let the
// form accept a value the gateway silently normalizes away.
func TestModelFormValidEffortValuesParity(t *testing.T) {
	if len(validEffortValues) != len(openaiup.ValidReasoningEfforts) {
		t.Fatalf("validEffortValues has %d entries, want %d (openaiup.ValidReasoningEfforts is canonical)",
			len(validEffortValues), len(openaiup.ValidReasoningEfforts))
	}
	for k := range openaiup.ValidReasoningEfforts {
		if !validEffortValues[k] {
			t.Errorf("validEffortValues missing %q (openaiup.ValidReasoningEffort is canonical)", k)
		}
	}
}

// TestModelFormEffortPlantMasksRegistryValue pins F-M1: a marker-decorated
// credential planted in the on-disk reasoningEffort slot (bypassing every
// validator, which is why the diff site matters) renders masked through
// providers.MaskRegistryValue in the M4 approval, never raw. The form grid
// shows the seeded field value like any text field, so the raw-absence
// assertions run on the approval portion of the transcript only.
func TestModelFormEffortPlantMasksRegistryValue(t *testing.T) {
	plant := "./" + testfixtures.AKIAExample
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  plant,
	}
	app, _ := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	// Down to the effort field (index 7), clear the seeded value so the
	// before operand must render, then to the save row and the approval.
	keys := []Key{
		KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown,
	}
	for i := 0; i < len(normEffort(plant)); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	idx := strings.Index(out, "Approve Registry Write")
	if idx < 0 {
		t.Fatalf("approval modal never opened; frames:\n%s", out)
	}
	approval := out[idx:]
	// G-M4: the diff masks the RAW operand (the normalized form is only the
	// comparison key), so the expected before column is the raw mask. The
	// pre-G-M4 code masked the normalized value, which lowercased the plant
	// first and defeated the case-sensitive secret classes.
	want := providers.MaskRegistryValue(stripDiffControls(plant))
	if want == plant {
		t.Fatalf("test premise broken: MaskRegistryValue(%q) returned the raw plant", plant)
	}
	if !strings.Contains(approval, "reasoningEffort: "+want+" ->") {
		t.Fatalf("approval missing the masked effort operand %q; approval:\n%s", want, approval)
	}
	for _, raw := range []string{plant, normEffort(plant), testfixtures.AKIAExample} {
		if strings.Contains(approval, raw) {
			t.Fatalf("approval echoes the raw planted value %q; approval:\n%s", raw, approval)
		}
	}
}

// TestModelFormEditEffortNormalizedNoPhantom pins F-M2: a pristine edit of
// a model whose on-disk reasoningEffort is not lowercase/trimmed shows
// "(no field changes)" instead of a phantom diff line, and the approved
// no-op save persists the raw value byte-unchanged.
func TestModelFormEditEffortNormalizedNoPhantom(t *testing.T) {
	for _, planted := range []string{"HIGH", "  high  "} {
		reg := modelsFixtureReg()
		reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
			SupportsTools:    true,
			AllowedTools:     []string{"read_file"},
			SupportsThinking: true,
			ReasoningEffort:  planted,
		}
		app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

		keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
		out := app.CaptureRun(&ScriptedKeys{Keys: keys})

		if !strings.Contains(out, missingModelFormDiffNone) {
			t.Fatalf("planted effort %q: pristine edit missing the no-changes marker; frames:\n%s", planted, out)
		}
		if strings.Contains(out, "reasoningEffort:") {
			t.Fatalf("planted effort %q: phantom effort diff line; frames:\n%s", planted, out)
		}

		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
		m := reloadModels(t, path)[0]
		if m.Capabilities.ReasoningEffort != planted {
			t.Fatalf("planted effort %q: persisted value = %q, want the raw bytes unchanged", planted, m.Capabilities.ReasoningEffort)
		}
	}
}

// TestModelFormEditExternalChangeRefused pins F-M3 (CWE-367): an external
// edit to the same model between form open and commit is detected at
// commit time, the approved write is refused with the non-echoing
// stale-base message, and the on-disk registry keeps the external edit
// byte-for-byte (no silent revert).
func TestModelFormEditExternalChangeRefused(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")

	// External mutation while the form is open: another tool changes the
	// model's family on disk.
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	reg.Providers[0].Models[0].Family = "tampered"
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write mutated registry: %v", err)
	}
	diskAfterMutation, _ := os.ReadFile(path)

	// Edit the context length on the form, walk to the save row, open the
	// approval, approve: the commit must refuse.
	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown}
	keys = append(keys, typeRunes("999")...)
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "The model changed on disk since this form opened.") {
		t.Fatalf("stale-base refusal missing; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskAfterMutation) {
		t.Fatal("on-disk JSON changed despite the stale-base refusal")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (form stays open after the refusal)", app.Depth())
	}
	reg2, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload after refusal: %v", err)
	}
	if reg2.Providers[0].Models[0].Family != "tampered" {
		t.Fatalf("external edit was reverted: family = %q", reg2.Providers[0].Models[0].Family)
	}
}

// TestModelFormDiffSlotOperandsMasked pins G-M1: every registry-slot operand of
// the M4 approval diff (id, family, tags, tools, reasoning effort) renders
// through the registry-slot display authority plus the control-rune strip, so a
// marker-decorated credential planted on disk cannot ride MaskOperand's
// location exemption into the consent frame. The plants bypass every VALIDATION
// rule that would refuse them at ingest (a hand-edited registry), which is why
// the diff site matters; raw absence is asserted on the approval section only,
// because the form grid legitimately shows the seeded field value while it is
// being edited.
//
// Policy round: the on-disk plant is a look-alike-decorated family
// (U+2025 TWO DOT LEADER inside the marker), not the previously used
// Cf-decorated one. The registry load rule was widened from Cc to Cc+Cf, so a
// Cf-decorated model field can no longer be loaded at all (that refusal is
// pinned by TestValidateRejectsInvisibleFormatRunes and by the load-refusal
// assert at the end of this test); the Cf display arm is still pinned, at the
// masker seam, where it remains reachable (a spec-borne or form-borne value).
func TestModelFormDiffSlotOperandsMasked(t *testing.T) {
	// The plant is the mission's example shape for this class: U+2025 TWO DOT
	// LEADER, a slash, and a lowercase 20-rune core. The pre-policy authority
	// masks neither (its ASCII marker set cannot see U+2025, and a lowercase
	// core carries no uppercase letter for the decorated class), so the plant
	// discriminates the shared policy from the bare authority.
	const famCore = "ab3def7gh9jk1mnpqr5t"
	famPlant := "\u2025/" + famCore
	tagPlant := "./" + testfixtures.AKIAExample
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Family = famPlant
	reg.Providers[0].Models[0].Tags = []string{tagPlant, "fast"}
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  "low",
	}
	app, _ := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	// The expected render is the single display authority's (maskSlotOperand),
	// not the bare authority's: the look-alike plant is exactly the class the
	// bare authority misses, so the second premise check is the discriminating
	// one (it fails if the sink falls back to the bare authority).
	wantFam := maskSlotOperand(famPlant)
	if wantFam == famPlant {
		t.Fatalf("test premise broken: the family plant is not masked (%q)", wantFam)
	}
	if bare := providers.MaskRegistryValue(stripDiffControls(famPlant)); bare == wantFam {
		t.Fatalf("test premise broken: the bare authority already masks the plant (%q), so the entry no longer isolates the shared policy", bare)
	}
	wantTag := maskSlotOperand(tagPlant)
	if wantTag == tagPlant {
		t.Fatalf("test premise broken: the tag plant is not masked (%q)", wantTag)
	}

	// Replace the family and the tags with benign values, so both diff lines
	// must render their planted before operands (a seeded plant would also be
	// refused by the G-M8 ingest guard at save time).
	keys := []Key{KeyDown}
	for i := 0; i < len([]rune(famPlant)); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("swift")...)
	keys = append(keys, KeyDown)
	for i := 0; i < len([]rune(tagPlant+", fast")); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("beta")...)
	for i := 0; i < 9; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	idx := strings.Index(out, "Approve Registry Write")
	if idx < 0 {
		t.Fatalf("approval modal never opened; frames:\n%s", out)
	}
	approval := out[idx:]
	for _, want := range []string{
		"family: " + wantFam + " -> swift",
		"tags: " + wantTag + ", fast -> beta",
	} {
		if !strings.Contains(approval, want) {
			t.Fatalf("approval missing the masked slot operand %q; approval:\n%s", want, approval)
		}
	}
	for _, raw := range []string{
		famPlant,
		stripDiffControls(famPlant),
		famCore,
		tagPlant,
		testfixtures.AKIAExample,
	} {
		if strings.Contains(approval, raw) {
			t.Fatalf("approval echoes the raw planted value %q; approval:\n%s", raw, approval)
		}
	}

	// The control strip belongs to the diff builder, not only to the painter
	// (which drops Cf/Cc runes from untrusted frame lines at render time), so
	// assert it on the builder output directly.
	scr := app.Top().(*modelFormScreen)
	m, tools := scr.buildModel()
	for _, line := range scr.changedDiffLines(m, tools) {
		if strings.ContainsFunc(line, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			t.Fatalf("diff line carries a control or format rune: %q", line)
		}
	}

	// The Cf display arm, pinned at the masker seam: a right-to-left override
	// inside the location marker must not survive into the masked operand
	// ("\u202ewJal****" would be the leak). This shape can no longer reach a
	// display sink through the registry (the load rule refuses Cc+Cf), but the
	// same value can arrive spec- or form-borne, so the masker must keep
	// stripping it.
	cfPlant := "./\u202e" + testfixtures.AWSSecretExample
	cfMasked := maskSlotOperand(cfPlant)
	if cfMasked == cfPlant {
		t.Fatalf("the Cf-decorated plant is not masked (%q)", cfMasked)
	}
	if strings.ContainsFunc(cfMasked, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		t.Fatalf("the masked Cf-decorated plant still carries the format rune: %q", cfMasked)
	}
	if strings.Contains(cfMasked, "K7MDENG") {
		t.Fatalf("the masked Cf-decorated plant leaks the credential core: %q", cfMasked)
	}

	// The load rule of the same class (the widening that forced the on-disk
	// plant change): a Cf-decorated family can no longer be loaded, so nothing
	// persists it at rest.
	reg.Providers[0].Models[0].Family = cfPlant
	cfData, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal Cf-decorated fixture: %v", err)
	}
	cfPath := filepath.Join(t.TempDir(), "providers.json")
	if err := providers.WriteFileAtomic(cfPath, append(cfData, '\n'), 0o600); err != nil {
		t.Fatalf("write Cf-decorated fixture: %v", err)
	}
	if _, err := providers.LoadFrom(cfPath); err == nil {
		t.Fatal("a Cf-decorated model family loaded; the Cc+Cf load rule is not in force")
	}
}

// TestModelFormEditSupportsToolsFlipRefused pins G-M2: supportsTools is a
// signed field, so an external flip of that one flag between form open and
// commit is a stale base. Without the sign the approved write passed the gate
// and silently reverted the external change.
func TestModelFormEditSupportsToolsFlipRefused(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")

	// The fixture's acme-fast carries SupportsTools true; flip it off on disk
	// while the form is open, leaving every other signed field untouched.
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	reg.Providers[0].Models[0].Capabilities.SupportsTools = false
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write mutated registry: %v", err)
	}
	diskAfterMutation, _ := os.ReadFile(path)

	// Edit the context, walk to the save row, approve: the commit must refuse.
	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown}
	keys = append(keys, typeRunes("999")...)
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "The model changed on disk since this form opened.") {
		t.Fatalf("supportsTools-only drift not refused; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskAfterMutation) {
		t.Fatal("on-disk JSON changed despite the stale-base refusal")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (form stays open after the refusal)", app.Depth())
	}
	reg2, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload after refusal: %v", err)
	}
	if reg2.Providers[0].Models[0].Capabilities.SupportsTools {
		t.Fatal("the external supportsTools flip was reverted by the approved write")
	}
	if len(reg2.Providers[0].Models[0].Capabilities.AllowedTools) != 1 {
		t.Fatalf("tool grants changed: %v", reg2.Providers[0].Models[0].Capabilities.AllowedTools)
	}
}

// TestModelFormSupportsToolsDiffLine pins G-M3: buildModel OR-derives
// SupportsTools from the grant list, so a record with grants and the flag off
// saved tool use under a diff that named no field. Both sides now carry the
// line (add when the flag is set, change when it flips), mirroring the CLI
// twin, and the write lands.
func TestModelFormSupportsToolsDiffLine(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools: false,
		AllowedTools:  []string{"read_file"},
	}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	keys := make([]Key, 0, fCount+1)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "  supportsTools: false -> true") {
		t.Fatalf("the OR-derived supportsTools flip is missing from the diff; frames:\n%s", out)
	}
	if strings.Contains(out, missingModelFormDiffNone) {
		t.Fatalf("the flip was hidden under the no-changes marker; frames:\n%s", out)
	}

	// The add side names the flag too, matching the CLI twin's line.
	scr := app.Top().(*modelFormScreen)
	addFrame := strings.Join(scr.addedDiffLines(providers.Model{
		ID:           "acme-tool",
		Capabilities: providers.Capabilities{SupportsTools: true, AllowedTools: []string{"read_file"}},
	}, []string{"read_file"}), "\n")
	if !strings.Contains(addFrame, "+ supportsTools: true") {
		t.Fatalf("add-mode diff missing the supportsTools line; lines:\n%s", addFrame)
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	if !m.Capabilities.SupportsTools {
		t.Fatal("SupportsTools = false on disk, want the OR-derived true")
	}
	if len(m.Capabilities.AllowedTools) != 1 || m.Capabilities.AllowedTools[0] != "read_file" {
		t.Fatalf("AllowedTools = %v, want [read_file]", m.Capabilities.AllowedTools)
	}
}

// TestModelFormEffortPlantRawMask pins G-M4: the edit diff masks the RAW
// operand, not the normalized one. Normalizing first lowercases the value and
// defeats the case-sensitive secret classes, so a hand-planted
// "1-ASIA...EXAMPLE" rendered raw (lowercased) in the before column.
func TestModelFormEffortPlantRawMask(t *testing.T) {
	plant := "1-" + testfixtures.ASIADocExample
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  plant,
	}
	app, _ := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	want := providers.MaskRegistryValue(plant)
	if want == plant {
		t.Fatalf("test premise broken: MaskRegistryValue(%q) returned the raw plant", plant)
	}

	// Clear the seeded (normalized) value and retype a valid effort, so the
	// before column must render the on-disk bytes.
	keys := make([]Key, 0, fCount+8)
	for i := 0; i < fEffort; i++ {
		keys = append(keys, KeyDown)
	}
	for i := 0; i < len([]rune(normEffort(plant))); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("high")...)
	for i := fEffort; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	idx := strings.Index(out, "Approve Registry Write")
	if idx < 0 {
		t.Fatalf("approval modal never opened; frames:\n%s", out)
	}
	approval := out[idx:]
	if !strings.Contains(approval, "reasoningEffort: "+want+" -> high") {
		t.Fatalf("approval missing the raw-masked effort operand %q; approval:\n%s", want, approval)
	}
	if strings.Contains(approval, plant) {
		t.Fatalf("approval echoes the raw plant; approval:\n%s", approval)
	}
	// The pre-G-M4 rendering was the lowercased plant: assert that shape is gone
	// too, not just the original spelling.
	if lower := strings.ToLower(plant); strings.Contains(strings.ToLower(approval), lower) {
		t.Fatalf("approval echoes the lowercased plant %q; approval:\n%s", lower, approval)
	}
}

// TestModelFormEditEffortCaseOnlyRetype pins G-M5: a retype that differs from
// the on-disk value only by case is a real byte change, so the approval must
// name it. The untouched pristine case keeps the no-changes marker (F-M2).
func TestModelFormEditEffortCaseOnlyRetype(t *testing.T) {
	upperReg := func() *providers.Registry {
		reg := modelsFixtureReg()
		reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
			SupportsTools:    true,
			AllowedTools:     []string{"read_file"},
			SupportsThinking: true,
			ReasoningEffort:  "HIGH",
		}
		return reg
	}

	// Pristine: the untouched seed is normalized-equal to the on-disk value, so
	// no effort line may appear.
	clean, _ := newModelFormTestAppReg(t, upperReg(), "edit", "acme-fast")
	keys := make([]Key, 0, fCount+1)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := clean.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, missingModelFormDiffNone) {
		t.Fatalf("pristine uppercase effort lost the no-changes marker; frames:\n%s", out)
	}
	if strings.Contains(out, "reasoningEffort:") {
		t.Fatalf("pristine uppercase effort produced a phantom diff line; frames:\n%s", out)
	}

	// Retyped: the same normalized value with different raw bytes.
	app, path := newModelFormTestAppReg(t, upperReg(), "edit", "acme-fast")
	keys = keys[:0]
	for i := 0; i < fEffort; i++ {
		keys = append(keys, KeyDown)
	}
	for i := 0; i < len([]rune("HIGH")); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("high")...)
	for i := fEffort; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "  reasoningEffort: HIGH -> high") {
		t.Fatalf("case-only retype missing from the approval; frames:\n%s", out)
	}
	if strings.Contains(out, missingModelFormDiffNone) {
		t.Fatalf("the approval claimed no field changes for a case-only retype; frames:\n%s", out)
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if got := reloadModels(t, path)[0].Capabilities.ReasoningEffort; got != "high" {
		t.Fatalf("persisted effort = %q, want the retyped bytes %q", got, "high")
	}
}

// TestModelFormEditLoadErrorSurfacesError pins the baseChangedOnDisk load-error
// branch (G-M7): a registry that cannot be read at commit time is not a stale
// base, so the approved write proceeds to Core, which surfaces its own load
// error. A false refusal would mislabel the failure; nothing may be written.
func TestModelFormEditLoadErrorSurfacesError(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")

	// Corrupt the registry after the form opened, so both the base check and
	// Core's own load fail.
	corrupt := []byte("{ this is not a registry\n")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatalf("corrupt providers.json: %v", err)
	}

	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown}
	keys = append(keys, typeRunes("999")...)
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if strings.Contains(out, "The model changed on disk since this form opened.") {
		t.Fatalf("a load error was reported as a stale base; frames:\n%s", out)
	}
	msg := app.Message()
	if !strings.HasPrefix(msg, "Error: ") {
		t.Fatalf("core error message missing the error label; message = %q", msg)
	}
	if !strings.Contains(msg, "invalid JSON") {
		t.Fatalf("core's own load error not surfaced; message = %q", msg)
	}
	if got, _ := os.ReadFile(path); string(got) != string(corrupt) {
		t.Fatal("the unreadable registry was rewritten")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (form stays open after the error)", app.Depth())
	}
}

// TestModelFormDecoratedSecretRefused pins G-M8: one leading location marker
// ("./", ".\", a URL scheme) used to defeat the free-text shape test, so a
// decorated credential entered the registry through the family and tags fields.
// The marker-stripped core is classified too, the refusal keeps the existing
// non-echoing message, and legitimate values keep passing.
func TestModelFormDecoratedSecretRefused(t *testing.T) {
	famPlant := "./" + testfixtures.AWSSecretExample
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-x"), KeyTab)
	keys = append(keys, typeRunes(famPlant)...)
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fFamily {
		t.Fatalf("cursor = %d, want %d (advance blocked at the family field); frames:\n%s", scr.form.Cursor(), fFamily, out)
	}
	if msg := scr.form.Message(); !strings.Contains(msg, "(value hidden)") {
		t.Fatalf("decorated family not refused by the validator; form message = %q", msg)
	}
	if strings.Contains(scr.messageLine(), famPlant) {
		t.Fatalf("message line echoes the decorated plant: %q", scr.messageLine())
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after the decorated family")
	}

	// The tags field classifies each comma-separated item the same way.
	app2, path2 := newModelFormTestApp(t, "add", "")
	diskBefore2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	keys = append(typeRunes("acme-x"), KeyTab, KeyTab)
	keys = append(keys, typeRunes("./"+testfixtures.AKIAExample)...)
	keys = append(keys, KeyEnter)
	out = app2.CaptureRun(&ScriptedKeys{Keys: keys})

	scr2 := app2.Top().(*modelFormScreen)
	if scr2.form.Cursor() != fTags {
		t.Fatalf("cursor = %d, want %d (advance blocked at the tags field); frames:\n%s", scr2.form.Cursor(), fTags, out)
	}
	if msg := scr2.form.Message(); !strings.Contains(msg, "(value hidden)") {
		t.Fatalf("decorated tag not refused by the validator; form message = %q", msg)
	}
	if got, _ := os.ReadFile(path2); string(got) != string(diskBefore2) {
		t.Fatal("on-disk JSON changed after the decorated tag")
	}

	// The guard in both directions, across the marker forms.
	for _, decorated := range []string{
		"./" + testfixtures.AWSSecretExample,
		`.\` + testfixtures.AWSSecretExample,
		"https://" + testfixtures.AWSSecretExample,
		"./" + testfixtures.AKIAExample,
		"file:///./" + testfixtures.AKIAExample,
	} {
		if !looksSecretValue(decorated) {
			t.Errorf("looksSecretValue(%q) = false, want true (decorated credential)", decorated)
		}
	}
	for _, benign := range []string{
		"anthropic/claude-3.5-sonnet",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo",
		"beta, fast",
	} {
		if looksSecretValue(benign) {
			t.Errorf("looksSecretValue(%q) = true, want false (legitimate model field value)", benign)
		}
	}
	// FIX-7: a location-shaped value is refused by the shared name authority
	// even when its core is benign, because the registry-slot authority this
	// screen paints through does not return it byte-identical (it strips the
	// location marker: "./models/gemini-pro" renders "models/gemini-pro"). The
	// form therefore refuses it instead of accepting a value whose frames show
	// different bytes. These three moved out of the benign list above, where
	// the SecretShaped-only body accepted them.
	for _, location := range []string{
		"./models/gemini-pro",
		"https://example.com/v1",
		`.\models\gemini`,
	} {
		if !looksSecretValue(location) {
			t.Errorf("looksSecretValue(%q) = false, want true (location re-rendered by the slot authority)", location)
		}
		if maskSlotOperand(location) == location {
			t.Errorf("%q is byte-identical under the slot authority, so the refusal above would over-refuse", location)
		}
	}
}

// --- Masking convergence round (H-1 .. H-5) -------------------------------

// TestLooksSecretValueFullMarkerSet pins H-1's ingest side, now expressed by the
// shared policy: the strip carries every leading decoration class the category
// policy defines (the Windows drive prefix bare and nested, the relative-path
// markers, a URL scheme) plus the invisible classes (Cc/Cf/Mn/Me) and the
// non-ASCII separator look-alikes. Each refused entry isolates one class, so
// dropping that class from the policy fails the entry; the benign list protects
// the accept policy (a hyphenated id, a slash-separated id, a comma tag list,
// and a marker-decorated value the slot authority leaves byte-identical).
//
// FIX-7 moved the five location entries out of the benign list: the lead-in
// markers below are still stripped for CLASSIFICATION, and since the shared name
// authority refuses any value the registry-slot display authority re-renders,
// a benign path or URL is refused at a model field rather than accepted with a
// frame that shows its stripped core. The move is pinned explicitly (locations
// loop) so the verdict change is visible in this file, not implied.
func TestLooksSecretValueFullMarkerSet(t *testing.T) {
	const awsSecret = testfixtures.AWSSecretExample
	const opaque = "ab3def7gh9jk1mnpqr5t" // 20 runes, lowercase, mixed classes
	refused := []struct{ name, value string }{
		{"bare drive backslash", `C:\` + awsSecret},
		{"bare drive slash", `c:/` + awsSecret},
		{"bare drive opaque run", `D:\` + opaque},
		{"relative then drive", `./C:\` + awsSecret},
		{"stacked drive", `C:\C:\` + awsSecret},
		{"leading separator then drive", `\C:\` + awsSecret},
		{"bidi override", "\u202e" + testfixtures.AKIAExample},
		{"bidi isolate with zero width space", "\u2067akia\u200bosfodnn7example\u2069"},
		{"byte order mark", "\uFEFF" + testfixtures.AKIAExample},
		{"two dot leader", "\u2025/" + opaque},
		{"horizontal ellipsis", "\u2026/" + opaque},
		{"fullwidth solidus", "\uFF0F" + testfixtures.AKIAExample},
		{"fullwidth reverse solidus", "\uFF3C" + awsSecret},
		{"fullwidth full stop then solidus", "\uFF0E\uFF0F" + awsSecret},
		{"fraction slash", "\u2044" + opaque},
		{"division slash", "\u2215" + awsSecret},
		{"small reverse solidus", "\uFE68" + awsSecret},
	}
	for _, tc := range refused {
		if !looksSecretValue(tc.value) {
			t.Errorf("%s: looksSecretValue(%q) = false, want true (decorated credential)", tc.name, tc.value)
		}
	}
	benign := []string{
		"anthropic/claude-3.5-sonnet",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo",
		"gemini-3-pro-high",
		"read_file",
		"high",
		"beta, fast",
		"\u2025models/gemini-pro",
	}
	for _, v := range benign {
		if looksSecretValue(v) {
			t.Errorf("looksSecretValue(%q) = true, want false (legitimate model field value)", v)
		}
	}
	// The moved location class (FIX-7): benign cores, but the slot authority
	// does not return the typed bytes, so the form refuses them.
	locations := []string{
		`C:\models\gemini-pro`,
		`C:\models\anthropic\claude-3.5-sonnet`,
		`c:/models/gemini-pro`,
		`.\models\gemini`,
		"./models/gemini-pro",
		"https://example.com/v1",
	}
	for _, v := range locations {
		if !looksSecretValue(v) {
			t.Errorf("looksSecretValue(%q) = false, want true (location re-rendered by the slot authority)", v)
		}
		if maskSlotOperand(v) == v {
			t.Errorf("%q is byte-identical under the slot authority; the refusal would over-refuse", v)
		}
	}
}

// TestModelFormDecoratedSecretMarkerClassesRefused pins the field-level half of
// H-1: a drive-decorated plant (bare and nested), a Cf-decorated plant, and a
// unicode-look-alike-decorated plant are refused at the family field with the
// existing non-echoing secret message and nothing written. Before the fix each
// of these entered the registry through the TUI door while the CLI twin refused
// the same value.
func TestModelFormDecoratedSecretMarkerClassesRefused(t *testing.T) {
	const awsSecret = testfixtures.AWSSecretExample
	plants := []struct{ name, value string }{
		{"bare drive", `C:\` + awsSecret},
		{"nested drive", `./C:\` + awsSecret},
		{"cf decorated", "\u202e" + "akiaosfodnn7example"},
		{"look-alike decorated", "\u2025/ab3def7gh9jk1mnpqr5t"},
	}
	for _, tc := range plants {
		t.Run(tc.name, func(t *testing.T) {
			app, path := newModelFormTestApp(t, "add", "")
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json: %v", err)
			}
			keys := append(typeRunes("acme-x"), KeyTab)
			keys = append(keys, typeRunes(tc.value)...)
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})

			scr := app.Top().(*modelFormScreen)
			if scr.form.Cursor() != fFamily {
				t.Fatalf("cursor = %d, want %d (advance blocked at the family field); frames:\n%s",
					scr.form.Cursor(), fFamily, out)
			}
			if msg := scr.form.Message(); !strings.Contains(msg, "(value hidden)") {
				t.Fatalf("decorated family not refused by the validator; form message = %q", msg)
			}
			if strings.Contains(scr.messageLine(), tc.value) {
				t.Fatalf("message line echoes the decorated plant: %q", scr.messageLine())
			}
			if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
				t.Fatal("on-disk JSON changed after the decorated family")
			}
		})
	}
}

// TestModelFormBenignMarkerValuePersisted pins the accept side of H-1: a value
// carrying a decoration marker but no secret-shaped core passes every validator
// and the registry stores the bytes exactly as typed (the marker strip is a
// classification helper and never rewrites the value).
//
// FIX-7 narrowed which marker values pass: a location marker (a drive prefix or
// a relative path, whose rendering the slot authority rewrites) is now refused,
// so the benign representative here is the non-ASCII look-alike marker, which
// the slot authority leaves byte-identical and which therefore survives the
// form, the approval diff, and the write unchanged.
func TestModelFormBenignMarkerValuePersisted(t *testing.T) {
	const family = "\u2025models/gemini-pro"
	if looksSecretValue(family) {
		t.Fatal("test premise broken: the benign marker value reads as a secret")
	}
	if maskSlotOperand(family) != family {
		t.Fatalf("test premise broken: the slot authority re-renders the benign value (%q)", maskSlotOperand(family))
	}
	app, path := newModelFormTestApp(t, "add", "")
	keys := append(typeRunes("acme-path"), KeyTab)
	// P2-5: the ID commit auto-fills the family ("acme"); the typed marker
	// value replaces it, the same keystrokes a user seeing the prefill types.
	for i := 0; i < len("acme"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes(family)...)
	for i := 0; i <= fCount; i++ {
		keys = append(keys, KeyTab)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "Approve Registry Write") {
		t.Fatalf("approval modal never opened for the benign marker value; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	models := reloadModels(t, path)
	if len(models) != 3 {
		t.Fatalf("models on disk = %d, want 3", len(models))
	}
	if got := models[2].Family; got != family {
		t.Fatalf("persisted family = %q, want the typed bytes %q", got, family)
	}
}

// TestModelFormApprovalHeaderSlotMasked pins H-2: the consent header names two
// registry slots (the model id and the provider name) and both render through
// the slot masker exactly like the field diff below them. Before the fix the
// header composed providers.MaskOperand, whose location exemption printed a
// drive-decorated credential raw on Windows while the field line masked the
// same value.
//
// Subtests 1 and 2 plant the provider slot. The registry row is absent by
// design (the caller holds a name the file no longer carries), which is the one
// slot on this screen no validator ever sees; the form still composes the
// header, which is the sink under test. The look-alike plant is identity under
// providers.MaskOperand on every platform, so subtest 2 discriminates
// regardless of GOOS. Subtest 3 is the id slot: after H-1 the ingest guard
// refuses a decorated credential at the field, so the reachable id-slot case is
// a benign path, whose header operand is the authority's stripped core rather
// than the raw location (MaskOperand returned the raw value on every platform).
func TestModelFormApprovalHeaderSlotMasked(t *testing.T) {
	reachSave := func(app *App, prefix []Key) string {
		keys := append([]Key(nil), prefix...)
		for i := 0; i <= fCount; i++ {
			keys = append(keys, KeyTab)
		}
		keys = append(keys, KeyEnter)
		return app.CaptureRun(&ScriptedKeys{Keys: keys})
	}

	t.Run("drive decorated provider", func(t *testing.T) {
		const plant = `C:\` + testfixtures.AWSSecretExample
		want := maskSlotOperand(plant)
		if want == plant {
			t.Fatalf("test premise broken: the provider plant is not masked (%q)", want)
		}
		app, _ := newModelFormTestAppNamed(t, modelsFixtureReg(), plant, "add", "")
		out := reachSave(app, typeRunes("acme-x"))
		idx := strings.Index(out, "Approve Registry Write")
		if idx < 0 {
			t.Fatalf("approval modal never opened; frames:\n%s", out)
		}
		approval := out[idx:]
		wantLine := "Adds model acme-x to provider " + want + ". Key values are untouched."
		if !strings.Contains(approval, wantLine) {
			t.Fatalf("header missing the slot-masked provider %q; approval:\n%s", wantLine, approval)
		}
		if strings.Contains(approval, plant) {
			t.Fatalf("header echoes the raw planted provider; approval:\n%s", approval)
		}
	})

	t.Run("look-alike decorated provider", func(t *testing.T) {
		const plant = "\u2025/ab3def7gh9jk1mnpqr5t"
		want := maskSlotOperand(plant)
		if want == plant {
			t.Fatalf("test premise broken: the provider plant is not masked (%q)", want)
		}
		app, _ := newModelFormTestAppNamed(t, modelsFixtureReg(), plant, "add", "")
		out := reachSave(app, typeRunes("acme-x"))
		idx := strings.Index(out, "Approve Registry Write")
		if idx < 0 {
			t.Fatalf("approval modal never opened; frames:\n%s", out)
		}
		approval := out[idx:]
		wantLine := "Adds model acme-x to provider " + want + ". Key values are untouched."
		if !strings.Contains(approval, wantLine) {
			t.Fatalf("header missing the slot-masked provider %q; approval:\n%s", wantLine, approval)
		}
		if strings.Contains(approval, plant) {
			t.Fatalf("header echoes the raw planted provider; approval:\n%s", approval)
		}
	})

	// FIX-7 replaced this subtest's premise. It used to plant a benign path in
	// the id slot and assert the header showed the slot authority's stripped core
	// ("gemini-pro") rather than the typed bytes. Since the shared name authority
	// now refuses any id whose rendering differs from the typed bytes, that id
	// never reaches the header: the field validator stops it first, so the id slot
	// cannot diverge from its field verdict at all. The pin keeps both halves of
	// that statement: the path is refused at the field with no gate, and an
	// accepted id reaches the header byte-identically.
	t.Run("id slot cannot diverge from the field verdict", func(t *testing.T) {
		const id = `C:\models\gemini-pro`
		if maskSlotOperand(id) == id {
			t.Fatalf("test premise broken: the id operand is not re-rendered (%q)", id)
		}
		app, path := newModelFormTestApp(t, "add", "")
		diskBefore, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read providers.json: %v", err)
		}
		out := reachSave(app, typeRunes(id))
		if strings.Contains(out, "Approve Registry Write") {
			t.Fatalf("approval modal opened for an id whose rendering differs from its bytes; frames:\n%s", out)
		}
		scr := app.Top().(*modelFormScreen)
		if scr.form.Cursor() != fModelID {
			t.Fatalf("cursor = %d, want %d (advance blocked at the id field)", scr.form.Cursor(), fModelID)
		}
		// The save-row refusal is the screen's own message (errMsg), which the
		// message line composes ahead of the form's field message.
		if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
			t.Fatalf("path id not refused by the save validation; message line = %q", msg)
		}
		if strings.Contains(scr.messageLine(), id) {
			t.Fatalf("message line echoes the refused location: %q", scr.messageLine())
		}
		if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
			t.Fatal("on-disk JSON changed after the refused location")
		}

		// Accept side: an id the authority leaves byte-identical reaches the
		// header unchanged (no rewrite to hide behind).
		const benign = "acme-x"
		app2, _ := newModelFormTestApp(t, "add", "")
		out2 := reachSave(app2, typeRunes(benign))
		idx := strings.Index(out2, "Approve Registry Write")
		if idx < 0 {
			t.Fatalf("approval modal never opened for a benign id; frames:\n%s", out2)
		}
		wantLine := "Adds model " + benign + " to provider acme. Key values are untouched."
		if !strings.Contains(out2[idx:], wantLine) {
			t.Fatalf("header missing the benign id %q; approval:\n%s", wantLine, out2[idx:])
		}
	})
}

// TestModelFormDiscardClearsEffortLatch pins H-4: the effort latch belongs to
// the screen, not the Form, so Restore alone left it set; a save of the restored
// (pristine-looking) form then persisted the normalized bytes over the raw
// original. The latch is cleared with the restore, so the restored form saves as
// a true no-op again.
//
// The App pops the form on a confirmed discard, so the save path is driven on
// the SAME restored instance (re-pushed onto the stack): that instance is the
// one the latch belongs to, which is exactly the state the fix cleans.
func TestModelFormDiscardClearsEffortLatch(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools:    true,
		AllowedTools:     []string{"read_file"},
		SupportsThinking: true,
		ReasoningEffort:  "HIGH",
	}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")
	scr := app.Top().(*modelFormScreen)

	// Touch the effort field: clear "HIGH" and type a different valid value, so
	// the form is dirty and the discard gate arms (the normalized-equal retype
	// leaves the form clean, which is the separately recorded residual).
	keys := []Key{}
	for i := 0; i < fEffort; i++ {
		keys = append(keys, KeyDown)
	}
	for i := 0; i < len([]rune("HIGH")); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("low")...)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !scr.effortTouched {
		t.Fatal("test premise broken: the effort edit did not latch")
	}

	// Esc asks to discard; y restores the construction values (the normalized
	// "high" seed) and pops.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	if scr.effortTouched {
		t.Fatal("the effort latch survived the discard restore")
	}
	if built, _ := scr.buildModel(); built.Capabilities.ReasoningEffort != "HIGH" {
		t.Fatalf("restored form builds effort %q, want the raw original %q",
			built.Capabilities.ReasoningEffort, "HIGH")
	}

	// Save the restored instance: a true no-op, no effort line, raw bytes kept.
	app.Push(scr)
	for i := 0; i <= fCount; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("no approval after the restored save; message = %q", app.Message())
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if !strings.Contains(out, missingModelFormDiffNone) {
		t.Fatalf("restored save lost the no-changes marker; frames:\n%s", out)
	}
	if strings.Contains(out, "reasoningEffort:") {
		t.Fatalf("restored save emitted an effort line; frames:\n%s", out)
	}
	if got := reloadModels(t, path)[0].Capabilities.ReasoningEffort; got != "HIGH" {
		t.Fatalf("persisted effort = %q, want the raw original %q", got, "HIGH")
	}
}

// TestModelFormEffortDeleteOnlyTrimLatches pins finding B: the Form's forward
// delete is a write key (form.go deleteAtCaret), so a delete-only trim of the
// effort field must latch effortTouched exactly like a backspace. Without the
// latch buildModel keeps the raw original bytes, so the edit the user sees on
// screen is silently not persisted. The trim is normalized-equal to the seed,
// which means the normEffort inequality guard cannot carry it; the latch is the
// only thing that can. The fixture therefore carries the padded on-disk seed
// ("HIGH ", the shape TestModelFormEditEffortNormalizedNoPhantom already
// plants) whose padding the screen normalizes away at construction.
//
// Form.Restore pads the live field without going through a write key: it is the
// value setter that does not latch dirty and it leaves the caret clamped in
// place. The forward delete is therefore provably the only write key in the
// sequence. The second sub-test pins the other direction: a forward delete at
// the value end writes nothing, so it must not latch and must keep the raw
// bytes.
func TestModelFormEffortDeleteOnlyTrimLatches(t *testing.T) {
	paddedSeed := func() *providers.Registry {
		reg := modelsFixtureReg()
		reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
			SupportsThinking: true,
			ReasoningEffort:  "HIGH ",
		}
		return reg
	}
	keysToEffort := func() []Key {
		keys := make([]Key, 0, fEffort+1)
		for i := 0; i < fEffort; i++ {
			keys = append(keys, KeyDown)
		}
		return keys
	}

	t.Run("trim latches and persists the normalized text", func(t *testing.T) {
		app, _ := newModelFormTestAppReg(t, paddedSeed(), "edit", "acme-fast")
		scr := app.Top().(*modelFormScreen)
		if got := scr.form.Value(fEffort); got != "high" {
			t.Fatalf("test premise broken: effort field = %q, want the normalized seed %q", got, "high")
		}
		vals := scr.form.Snapshot()
		vals[fEffort] = "high "
		scr.form.Restore(vals)
		if got := scr.form.FieldCaret(fEffort); got != len([]rune("high")) {
			t.Fatalf("test premise broken: effort caret = %d, want %d", got, len([]rune("high")))
		}

		app.CaptureRun(&ScriptedKeys{Keys: append(keysToEffort(), KeyDelete)})

		if got := scr.form.Value(fEffort); got != "high" {
			t.Fatalf("delete-only trim left the field at %q, want %q", got, "high")
		}
		if !scr.effortTouched {
			t.Fatal("delete-only trim did not latch effortTouched")
		}
		built, _ := scr.buildModel()
		if got := built.Capabilities.ReasoningEffort; got != "high" {
			t.Fatalf("buildModel persists effort %q, want the normalized text %q (raw seed was %q)", got, "high", "HIGH ")
		}
	})

	t.Run("delete at the value end does not latch", func(t *testing.T) {
		app, _ := newModelFormTestAppReg(t, paddedSeed(), "edit", "acme-fast")
		scr := app.Top().(*modelFormScreen)
		before := scr.form.Value(fEffort)

		app.CaptureRun(&ScriptedKeys{Keys: append(keysToEffort(), KeyDelete)})

		if got := scr.form.Value(fEffort); got != before {
			t.Fatalf("no-op delete changed the field from %q to %q", before, got)
		}
		if scr.effortTouched {
			t.Fatal("a no-op forward delete at the value end latched effortTouched")
		}
		built, _ := scr.buildModel()
		if got := built.Capabilities.ReasoningEffort; got != "HIGH " {
			t.Fatalf("no-op delete rewrote the raw original: buildModel effort = %q, want %q", got, "HIGH ")
		}
	})
}

// TestModelFormEditStaleBaseProviderVanished pins the fail-closed provider-nil
// branch of baseChangedOnDisk: a provider that disappeared between form open and
// commit is a stale base, so the write is refused with the stale-base sentence
// and nothing is written. Replacing the branch with a silent proceed turns the
// refusal into Core's own "provider not found" error, which the sentence
// assertion catches.
func TestModelFormEditStaleBaseProviderVanished(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")

	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	reg.Providers[0].Name = "acme-renamed"
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write mutated registry: %v", err)
	}
	mutated, _ := os.ReadFile(path)

	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown}
	keys = append(keys, typeRunes("999")...)
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	// The message line is truncated to the frame width, so the frame carries the
	// first sentence and the app message carries the whole refusal.
	if !strings.Contains(out, "The model changed on disk since this form opened.") {
		t.Fatalf("vanished provider not refused as a stale base; frames:\n%s", out)
	}
	if msg := app.Message(); !strings.Contains(msg, missingModelFormStaleBase) {
		t.Fatalf("refusal message is not the stale-base sentence; message = %q", msg)
	}
	if got, _ := os.ReadFile(path); string(got) != string(mutated) {
		t.Fatal("on-disk JSON changed despite the stale-base refusal")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (form stays open after the refusal)", app.Depth())
	}
	reg2, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload after refusal: %v", err)
	}
	if _, ok := reg2.Provider("acme-renamed"); !ok {
		t.Fatal("the external provider rename did not survive the refusal")
	}
}

// TestModelFormEditStaleBaseModelVanished pins the fail-closed model-missing
// branch of baseChangedOnDisk: the edited model vanished from the provider
// between form open and commit, so the write is refused with the stale-base
// sentence and nothing is written. Replacing the branch with a silent proceed
// turns the refusal into Core's own "model not found" error, which the sentence
// assertion catches.
func TestModelFormEditStaleBaseModelVanished(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")

	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	reg.Providers[0].Models = reg.Providers[0].Models[1:]
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write mutated registry: %v", err)
	}
	mutated, _ := os.ReadFile(path)

	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown}
	keys = append(keys, typeRunes("999")...)
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	// The message line is truncated to the frame width, so the frame carries the
	// first sentence and the app message carries the whole refusal.
	if !strings.Contains(out, "The model changed on disk since this form opened.") {
		t.Fatalf("vanished model not refused as a stale base; frames:\n%s", out)
	}
	if msg := app.Message(); !strings.Contains(msg, missingModelFormStaleBase) {
		t.Fatalf("refusal message is not the stale-base sentence; message = %q", msg)
	}
	if got, _ := os.ReadFile(path); string(got) != string(mutated) {
		t.Fatal("on-disk JSON changed despite the stale-base refusal")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (form stays open after the refusal)", app.Depth())
	}
	if models := reloadModels(t, path); len(models) != 1 || models[0].ID != "acme-slow" {
		t.Fatalf("models on disk = %+v, want only the externally surviving acme-slow", models)
	}
}

// TestModelFormMessageRoles pins user decision D7-7B at the model form: the
// commit success opens with the localized common.success label and a refused
// Core write with common.error, so the two compose DIFFERENT palette roles
// (RoleSuccess vs RoleDanger) instead of the neutral amber every message used
// to get. Both frames go through the same composition path the app-level pin
// uses, and the plain tier still emits zero escape bytes for them.
func TestModelFormMessageRoles(t *testing.T) {
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
		app := NewApp(100, 30, DirLTR)
		app.Push(&stubScreen{title: "Models", mode: ModeNav})
		app.Push(NewModelFormScreen(settings.NewCore(deps), "acme", modelFormModeAdd, ""))
		return app
	}
	// The add flow from TestModelFormAddEndToEnd: fill the grid, walk to the
	// save row, open the M4 gate.
	keys := []Key{}
	keys = append(keys, typeRunes("acme-new")...)
	keys = append(keys, KeyTab)
	// P2-5: the ID commit auto-fills the family ("acme"); the typed family
	// replaces the prefill, the same keystrokes a user seeing it types.
	for i := 0; i < len("acme"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("swift")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("beta,fast")...)
	keys = append(keys, KeyTab) // enabled keeps the "y" default
	keys = append(keys, KeyTab) // context
	keys = append(keys, typeRunes("200000")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("8192")...)
	keys = append(keys, KeyTab) // thinking (default off)
	keys = append(keys, KeyTab) // reasoning effort (default empty)
	keys = append(keys, KeyTab) // vision (default off)
	keys = append(keys, KeyTab) // video (default off)
	keys = append(keys, KeyTab) // capabilities row
	keys = append(keys, KeyTab) // save row
	keys = append(keys, KeyEnter)

	success := newRolesApp(t, nil)
	success.CaptureRun(&ScriptedKeys{Keys: keys})
	success.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	successMsg := success.Message()
	successLabel := Bundle().T("common.success")
	if !strings.HasPrefix(successMsg, successLabel+": ") {
		t.Fatalf("commit success message = %q, want the %q label prefix", successMsg, successLabel)
	}
	successRole := roleOfLine(t, composeFrame(success, nil), successMsg)
	if successRole != RoleSuccess {
		t.Fatalf("success message role = %v, want RoleSuccess", successRole)
	}

	failure := newRolesApp(t, func(*providers.Registry) error {
		return errors.New("registry write refused")
	})
	failure.CaptureRun(&ScriptedKeys{Keys: keys})
	failure.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
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

// --- FIX-7: the model-ID door joins the shared name authority --------------

// nameAuthorityUnderscoreProbes is the FIX-7 target class on the model side:
// key shapes whose head is absent from the shared prefix list and whose "_"
// breaks the opaque-run test, so providers.SecretShaped alone (the pre-fix body
// of looksSecretValue) is false for all five while the registry-slot display
// authority paints this screen's rows through providers.MaskSlot and masks
// them. Synthetic values only; kept local so this file states its own premise
// instead of borrowing the provider form's fixture.
var nameAuthorityUnderscoreProbes = []string{
	testfixtures.SKLive,
	testfixtures.TokSample,
	testfixtures.PKLive,
	testfixtures.KeyHex,
	testfixtures.AKIAUnderscore,
}

// TestModelFormUnderscoreIDProbesRefused is the FIX-7 pin on the model-ID field:
// each underscore probe is refused at the id field with the existing value-free
// sentence, the refusal reaches neither the message line nor the frame, no M4
// gate opens (so a stray "y" has nothing to approve), and the temp-home
// providers.json stays byte-identical. Every probe also restates the pre-fix
// hole, so this pin fails if the hole closes for another reason (a probe that
// SecretShaped already catches would stop exercising it).
func TestModelFormUnderscoreIDProbesRefused(t *testing.T) {
	for _, probe := range nameAuthorityUnderscoreProbes {
		t.Run(probe, func(t *testing.T) {
			if providers.SecretShaped(probe) {
				t.Fatalf("%q is caught by the pre-fix arm; it no longer exercises the gap", probe)
			}
			if !looksSecretValue(probe) {
				t.Fatalf("%q is identity under the shared name authority; the form cannot refuse it", probe)
			}
			// The row/diff rendering and the validator are one rule: the value the
			// form refuses is the value the frames mask, so the operator never sees
			// a masked row over an accepted field.
			if maskSlotOperand(probe) == probe {
				t.Fatalf("%q is byte-identical under the slot authority; the refusal would be invisible", probe)
			}

			app, path := newModelFormTestApp(t, "add", "")
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json: %v", err)
			}

			out := app.CaptureRun(&ScriptedKeys{Keys: append(typeRunes(probe), KeyEnter)})
			if !strings.Contains(out, "(value hidden)") {
				t.Fatalf("masked secret error missing; frames:\n%s", out)
			}
			scr := app.Top().(*modelFormScreen)
			if scr.form.Cursor() != fModelID {
				t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fModelID)
			}
			if msg := scr.messageLine(); strings.Contains(msg, probe) {
				t.Fatalf("message line echoes the credential-shaped id: %q", msg)
			}

			// Drive to the Save row: validation must keep the M4 gate shut.
			for i := 0; i < fCount; i++ {
				app.Step(KeyDown)
			}
			app.Step(KeyEnter)
			if strings.Contains(app.CaptureRun(&ScriptedKeys{}), "y approve write") {
				t.Fatal("approval modal opened for a credential-shaped id")
			}
			if msg := scr.messageLine(); msg != "" && strings.Contains(msg, probe) {
				t.Fatalf("save error echoes the credential-shaped id: %q", msg)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json after the refusal: %v", err)
			}
			if string(got) != string(diskBefore) {
				t.Fatalf("providers.json changed on a refused id:\nbefore: %s\nafter:  %s", diskBefore, got)
			}
			if len(reloadModels(t, path)) != 2 {
				t.Fatalf("model count moved on a refused id")
			}
		})
	}
}

// TestModelFormIDAuthorityAddsOnlyCredentialClass is the model-side before/after
// table, written the way the provider form's name-arm pin is: the "before"
// column is recomputed from the pre-fix classifier (providers.SecretShaped) on
// every row, so the delta is measured rather than restated.
//
// The delta is the five underscore probes PLUS the two classes the earlier DoD
// wording did not name, both refused for the same reason (the slot authority
// that paints this screen does not return them byte-identical): a single-class
// 16+ rune value and a location (path or URL). The pin asserts the moved set
// EXACTLY, so a future widening fails here instead of slipping in, and it
// asserts the display-agreement invariant on every row: the form's verdict
// equals "the slot authority would re-render this value".
func TestModelFormIDAuthorityAddsOnlyCredentialClass(t *testing.T) {
	cases := []struct {
		id     string
		before bool // providers.SecretShaped(id), the pre-fix looksSecretValue body
		after  bool
	}{
		// Realistic model IDs: the accept set the phase-2 work protected. Every
		// verdict is unchanged by FIX-7.
		{"anthropic/claude-3.5-sonnet", false, false},
		{"meta-llama/Llama-3.3-70B-Instruct-Turbo", false, false},
		{"Qwen/Qwen2.5-72B-Instruct-Turbo", false, false},
		{"meta-llama/llama-3.3-70b-instruct", false, false},
		{"claude-3-5-sonnet-20241022", false, false},
		{"acme-fast", false, false},
		{"gpt-4.1-mini", false, false},
		{"deepseek-v4-flash", false, false},
		{"qwen2.5-72b-instruct", false, false},
		{"glm-5.3-flash", false, false},
		{"gemini-3-pro-high", false, false},
		{"gemini-3.8-flash-high", false, false},
		{"claude-opus-5-5-high", false, false},
		{"claude-sonnet-5-5-high", false, false},
		{"kimi-k2-thinking", false, false},
		{"gpt-5-codex", false, false},
		{"o4-mini", false, false},
		{"mistral-large-latest", false, false},
		{"llama-4-scout-17b-16e-instruct", false, false},
		{"gemma-3-27b-it", false, false},
		{"phi-4-reasoning", false, false},
		{"command-r-plus-08-2025", false, false},
		{"nvidia/llama-3.3-nemotron-super-49b-v1", false, false},
		{"x-ai/grok-4-fast", false, false},
		{"moonshotai/kimi-k2-0905-preview", false, false},
		{"zhipuai/glm-5.3", false, false},
		{"qwen3-coder-480b-a35b", false, false},
		{"deepseek-r1-distill-qwen-32b", false, false},
		{"internlm3-8b-instruct", false, false},
		{"acme-large", false, false},
		{"us.anthropic.claude-3-5-sonnet-20241022-v2:0", false, false},
		{"models/gemini-2.5-pro", false, false},
		{"text-embedding-3-large", false, false},
		{"whisper-1", false, false},
		{"tts-1", false, false},
		{"gpt-4o-audio-preview", false, false},
		// FIX-7 target class: accepted before, refused now.
		{testfixtures.SKLive, false, true},
		{testfixtures.TokSample, false, true},
		{testfixtures.PKLive, false, true},
		{testfixtures.KeyHex, false, true},
		{testfixtures.AKIAUnderscore, false, true},
		// Single-class 16+ runes: accepted before, refused now (the slot
		// authority masks a bare alpha word and a bare digit serial alike).
		{"abcdefghijklmnopqrstuvwxyz", false, true},
		{"1234567890123456", false, true},
		// Locations: accepted before, refused now (the slot authority strips the
		// marker, so the frame would show different bytes than the record).
		{`C:\models\gemini-pro`, false, true},
		{"./models/gemini-pro", false, true},
		{"https://example.com/v1", false, true},
		// Pre-existing refusals: unchanged.
		{testfixtures.AKIAExample, true, true},
		{"AbCdEfGh0123456jKlMnoP", true, true},
		{testfixtures.AWSSecretExample, true, true},
		{"Ab3dEf7Gh9jK1mNp+/Qr=", true, true},
		{"sk-abc", true, true},
		{testfixtures.GroqKeyShort, true, true},
		{"dckr_pat-abcdef0123456789", true, true},
		{"bearer abc", true, true},
		{testfixtures.JWTFullBadSep, true, true},
		{testfixtures.GHPAlphabet, true, true},
	}

	moved := map[string]bool{}
	for _, tc := range cases {
		if got := providers.SecretShaped(tc.id); got != tc.before {
			t.Errorf("%q: pre-fix SecretShaped = %v, table says %v", tc.id, got, tc.before)
		}
		if got := looksSecretValue(tc.id); got != tc.after {
			t.Errorf("%q: form verdict = %v, table says %v", tc.id, got, tc.after)
		}
		// The one rule: refused exactly when the display authority re-renders it.
		if got := maskSlotOperand(tc.id) != tc.id; got != tc.after {
			t.Errorf("%q: slot authority re-renders = %v, form verdict = %v (display and validation disagree)", tc.id, got, tc.after)
		}
		if !tc.before && tc.after {
			moved[tc.id] = true
		}
	}

	wantMoved := []string{
		testfixtures.SKLive, testfixtures.TokSample, testfixtures.PKLive,
		testfixtures.KeyHex, testfixtures.AKIAUnderscore,
		"abcdefghijklmnopqrstuvwxyz", "1234567890123456",
		`C:\models\gemini-pro`, "./models/gemini-pro", "https://example.com/v1",
	}
	if len(moved) != len(wantMoved) {
		t.Fatalf("moved verdicts = %d, want exactly %d (%v)", len(moved), len(wantMoved), moved)
	}
	for _, id := range wantMoved {
		if !moved[id] {
			t.Errorf("%q should have moved from accept to refuse", id)
		}
	}
}

// TestModelFormNameDoorParityWithAddJSONTwin is the twin-parity pin: one corpus
// of charset-legal provider names is driven through the model form's classifier,
// the provider form's name arm, and the CLI add-json parser, and all three must
// return the same accept/refuse verdict for every entry. The three bodies are
// the shared authority today; the pin exists to fail the moment a door grows,
// loses, or reorders an arm of its own (the drift class every earlier round
// produced), and it also pins the CLI refusal to the named error and a
// value-free sentence.
func TestModelFormNameDoorParityWithAddJSONTwin(t *testing.T) {
	corpus := []string{
		// Legal names: accepted by all three doors.
		"openai", "gemini", "deepseek", "anthropic", "mistral", "groq", "openrouter",
		"together", "fireworks", "moonshot", "zhipu", "ollama", "lmstudio", "vllm",
		"litellm", "perplexity", "xai", "cerebras", "hyperbolic", "sambanova",
		"acme", "acme-fast", "acme_internal", "key_backup", "aws_prod", "gateway-eu",
		"provider-01", "my-gateway.example", "openai-eu", "bedrock-us-east1",
		// Credential-shaped names: refused by all three doors.
		testfixtures.SKLive, testfixtures.TokSample, testfixtures.PKLive,
		testfixtures.KeyHex, testfixtures.AKIAUnderscore, testfixtures.SKProj,
		"openai2024backup", "npm_registry", "qwen2point5turbo", "eu-west1.fastproxy.example",
		"provider0123456789", testfixtures.AKIAExample, testfixtures.GHPAlphabet,
	}

	accepted, refused := 0, 0
	for _, name := range corpus {
		name := name
		t.Run(name, func(t *testing.T) {
			modelDoor := looksSecretValue(name)
			providerDoor := fieldCarriesCredential(pfName, name)

			specJSON, err := json.Marshal(settings.ProviderSpecJSON{
				Name: name, BaseURL: "https://gw.example/v1", APIKeyEnv: "GW_KEY",
			})
			if err != nil {
				t.Fatalf("marshal spec: %v", err)
			}
			_, specErr := settings.ParseProviderSpecJSON(specJSON)
			cliDoor := errors.Is(specErr, settings.ErrProviderSpecNameCarriesCredential)

			if modelDoor != providerDoor || providerDoor != cliDoor {
				t.Fatalf("door verdicts disagree: model form = %v, provider form = %v, add-json = %v (err %v)",
					modelDoor, providerDoor, cliDoor, specErr)
			}
			if cliDoor {
				if specErr == nil || strings.Contains(specErr.Error(), name) {
					t.Fatalf("the CLI refusal must name no operand: %v", specErr)
				}
				// The refusal must come from the name gate, not from another arm
				// that happens to reject the same value.
				if !errors.Is(specErr, settings.ErrProviderSpecNameCarriesCredential) {
					t.Fatalf("the refusal is not the named gate error: %v", specErr)
				}
			} else if specErr != nil {
				t.Fatalf("a benign name was refused by a door: %v", specErr)
			}
			if cliDoor {
				refused++
			} else {
				accepted++
			}
		})
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("the corpus must exercise both verdicts (accepted %d, refused %d)", accepted, refused)
	}
}

// ---------------------------------------------------------------------------
// FIX-8a — tags validation reads the storage splitter.
// ---------------------------------------------------------------------------

// TestModelFormTagsValidationMatchesStorage is the direct pin on the
// validation/storage asymmetry: the form's validator and buildModel's
// splitTags must read one splitter, so a tag string validates exactly when
// every tag it STORES clears the ingest guard.
//
// The pre-fix body split on "," without trimming, and the shared name authority
// refuses any value the registry-slot display authority re-renders (MaskSlot
// trims, so it re-renders a padded tag: MaskSlot(" fast") == "fast"). Edit mode
// prefills the field with strings.Join(tags, ", "), so the second and later tags
// of every multi-tag record arrived padded and were refused as secret/key
// material; a typed "beta, fast" was refused the same way. Each row below is
// checked against the STORED tags rather than against the raw text, so the pin
// measures the asymmetry instead of restating the fix.
func TestModelFormTagsValidationMatchesStorage(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	// The two repro shapes from the review, named explicitly: the wizard-default
	// 2+ tag prefill and a natural spaced add.
	for _, repro := range []string{"flagship, tools", "beta, fast"} {
		if err := scr.validateTags(repro); err != nil {
			t.Fatalf("FIX-8a: validateTags(%q) = %v; a multi-tag list must be accepted", repro, err)
		}
	}

	cases := []struct {
		value  string
		stored []string
	}{
		{"beta, fast", []string{"beta", "fast"}},
		{"beta,fast", []string{"beta", "fast"}},
		{"  flagship ,  tools  ", []string{"flagship", "tools"}},
		{"flagship, tools, coding, reasoning", []string{"flagship", "tools", "coding", "reasoning"}},
		{"beta, fast ", []string{"beta", "fast"}},
		// Empty and whitespace-only parts are dropped by the storage splitter,
		// so they neither count as tags nor draw a verdict (rules unchanged).
		{"beta,,fast,", []string{"beta", "fast"}},
		{", beta", []string{"beta"}},
		{"", nil},
		{"   ", nil},
		// The credential side: one probe alone and one probe beside a benign
		// tag both refuse, so trimming cannot turn into a bypass.
		{testfixtures.SKLive, []string{testfixtures.SKLive}},
		{"beta, " + testfixtures.SKLive, []string{"beta", testfixtures.SKLive}},
		{"beta,  " + testfixtures.TokSample, []string{"beta", testfixtures.TokSample}},
		{"./" + testfixtures.AKIAExample, []string{"./" + testfixtures.AKIAExample}},
		{"beta, ./" + testfixtures.AKIAExample, []string{"beta", "./" + testfixtures.AKIAExample}},
	}
	for _, tc := range cases {
		stored := splitTags(tc.value)
		if !equalKeyList(stored, tc.stored) {
			t.Errorf("splitTags(%q) = %#v; want %#v (storage splitter drifted)", tc.value, stored, tc.stored)
			continue
		}
		// The invariant: the validator's verdict equals the verdict of the
		// guard over every stored tag.
		want := true
		for _, tag := range stored {
			if looksSecretValue(tag) {
				want = false
				break
			}
		}
		err := scr.validateTags(tc.value)
		if got := err == nil; got != want {
			t.Errorf("validateTags(%q) accepted = %v; want %v (storage would store %#v)", tc.value, got, want, stored)
		}
		if err != nil {
			if msg := err.Error(); !strings.Contains(msg, "(value hidden)") {
				t.Errorf("validateTags(%q) message = %q; want the value-free secret sentence", tc.value, msg)
			}
			for _, tag := range stored {
				if strings.Contains(err.Error(), tag) {
					t.Errorf("validateTags(%q) echoes the refused tag %q", tc.value, tag)
				}
			}
		}
	}

	// The live check on the focused field (messageLine) is the same rule: while
	// a multi-tag list is being typed it must not raise the masked warning, and
	// a probe inside the list must raise it.
	scr.form.SetCursor(fTags)
	setTags := func(v string) {
		vals := scr.form.Snapshot()
		vals[fTags] = v
		scr.form.Restore(vals)
		scr.form.SetCursor(fTags)
	}
	for _, benign := range []string{"beta, fast", "beta, ", "flagship, tools"} {
		setTags(benign)
		if msg := scr.messageLine(); msg != "" {
			t.Errorf("live message for %q = %q; a benign tag list must not warn", benign, msg)
		}
	}
	setTags("beta, " + testfixtures.SKLive)
	if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
		t.Errorf("live message for a probe tag = %q; want the masked warning", msg)
	}
}

// TestModelFormEditTwoTagSaveAccepted pins the end-to-end repro: editing a
// model that carries the wizard-default two tags and saving it unchanged must
// reach the approval modal and write. The pre-fix validator refused the
// prefill ("flagship, tools" -> " tools"), so the M4 gate never opened.
func TestModelFormEditTwoTagSaveAccepted(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Tags = []string{"flagship", "tools"}
	reg.Providers[0].Models[0].Capabilities.AllowedTools = []string{"read_file"}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	scr := app.Top().(*modelFormScreen)
	if got := scr.form.Value(fTags); got != "flagship, tools" {
		t.Fatalf("tags prefill = %q; want the joined form that carries the pad", got)
	}

	keys := []Key{}
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal never opened for a 2-tag edit; frames:\n%s", out)
	}
	if strings.Contains(out, "(value hidden)") {
		t.Fatalf("a 2-tag edit raised the secret refusal; frames:\n%s", out)
	}

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	models := reloadModels(t, path)
	if len(models) != 2 {
		t.Fatalf("model count = %d; want 2", len(models))
	}
	if got := models[0].Tags; !equalKeyList(got, []string{"flagship", "tools"}) {
		t.Fatalf("tags on disk = %#v; want %#v (round trip of the 2-tag record)", got, []string{"flagship", "tools"})
	}
	if !strings.Contains(app.Message(), "updated") {
		t.Errorf("success message = %q; want the update sentence", app.Message())
	}
}

// TestModelFormAddSpacedTagListAccepted pins the second repro: a natural
// "beta, fast" entry is accepted end to end, stores the two trimmed tags, and
// renders the diff line from them.
func TestModelFormAddSpacedTagListAccepted(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-new"), KeyTab)
	keys = append(keys, typeRunes("swift")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("beta, fast")...)
	for i := 0; i < 9; i++ {
		keys = append(keys, KeyTab) // enabled .. caps, then the save row
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal never opened for a spaced tag list; frames:\n%s", out)
	}
	if !strings.Contains(out, "+ tags: beta, fast") {
		t.Fatalf("added-diff tag line missing; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	if len(models) != 3 {
		t.Fatalf("models on disk = %d; want 3", len(models))
	}
	if got := models[2].Tags; !equalKeyList(got, []string{"beta", "fast"}) {
		t.Fatalf("tags on disk = %#v; want the two trimmed tags", got)
	}
}

// TestModelFormTagProbesRefused pins the refusal side of the trim: each
// underscore-head probe is still refused as a tag, alone and beside a benign
// tag, with the value-free sentence, no raw echo in the message line, the
// cursor held on the field, no approval, and no write.
func TestModelFormTagProbesRefused(t *testing.T) {
	for _, probe := range nameAuthorityUnderscoreProbes {
		t.Run(probe, func(t *testing.T) {
			// Premise: the probe is benign to the bare shape test and refused
			// only by the shared name authority, so trimming cannot be what
			// catches it.
			if providers.SecretShaped(probe) {
				t.Fatalf("%q is caught by the pre-fix arm; it no longer exercises the class", probe)
			}
			if !looksSecretValue(probe) {
				t.Fatalf("%q is identity under the shared name authority", probe)
			}
			for _, list := range []string{probe, "beta, " + probe, "beta," + probe} {
				app, path := newModelFormTestApp(t, "add", "")
				diskBefore, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read providers.json: %v", err)
				}

				keys := append(typeRunes("acme-x"), KeyTab, KeyTab)
				keys = append(keys, typeRunes(list)...)
				keys = append(keys, KeyEnter)
				app.CaptureRun(&ScriptedKeys{Keys: keys})

				scr := app.Top().(*modelFormScreen)
				if scr.form.Cursor() != fTags {
					t.Fatalf("tags %q: cursor = %d; want %d (advance blocked)", list, scr.form.Cursor(), fTags)
				}
				if msg := scr.form.Message(); !strings.Contains(msg, "(value hidden)") {
					t.Fatalf("tags %q not refused by the validator; message = %q", list, msg)
				}
				if msg := scr.messageLine(); strings.Contains(msg, probe) {
					t.Fatalf("tags %q: message line echoes the probe: %q", list, msg)
				}

				// Drive to the save row: the validator keeps the M4 gate shut.
				for i := 0; i < fCount; i++ {
					app.Step(KeyDown)
				}
				app.Step(KeyEnter)
				if strings.Contains(app.CaptureRun(&ScriptedKeys{}), "y approve write") {
					t.Fatalf("tags %q: approval modal opened", list)
				}
				if got, err := os.ReadFile(path); err != nil || string(got) != string(diskBefore) {
					t.Fatalf("tags %q: providers.json changed on a refused tag", list)
				}
			}
		})
	}
}

// TestModelFormPaddedValuesAccepted pins the adjacent-pattern closure of the
// FIX-8a class in the two remaining free-text sites of this form: the family
// validator and the live checks on family and effort read the value the way
// buildModel stores it (family trimmed, effort normalized), so padding alone
// never draws the secret refusal while a padded credential is still refused.
func TestModelFormPaddedValuesAccepted(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	for _, v := range []string{"swift", " swift", "swift ", "  swift  ", "glm-4.6"} {
		if err := scr.validateFamily(v); err != nil {
			t.Fatalf("validateFamily(%q) = %v; padding alone must not refuse a benign family", v, err)
		}
	}
	for _, v := range []string{" ./" + testfixtures.AKIAExample, "swift,./" + testfixtures.AKIAExample} {
		if err := scr.validateFamily(v); err == nil {
			t.Fatalf("validateFamily(%q) accepted a decorated credential; trimming must not become a bypass", v)
		}
	}
	for _, v := range []string{testfixtures.SKLive, " " + testfixtures.SKLive + " "} {
		if err := scr.validateFamily(v); err == nil {
			t.Fatalf("validateFamily(%q) accepted the underscore-head class", v)
		}
	}

	// The live checks follow the same reads: a padded benign value raises no
	// warning, a padded credential still does.
	setField := func(i int, v string) {
		vals := scr.form.Snapshot()
		vals[i] = v
		scr.form.Restore(vals)
		scr.form.SetCursor(i)
	}
	for _, v := range []string{" swift", "  swift  "} {
		setField(fFamily, v)
		if msg := scr.messageLine(); msg != "" {
			t.Errorf("live message for family %q = %q; padding must not warn", v, msg)
		}
	}
	setField(fFamily, " ./"+testfixtures.AKIAExample)
	if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
		t.Errorf("live message for a padded decorated family = %q; want the masked warning", msg)
	}
	for _, v := range []string{" low", "  HIGH  ", "medium"} {
		setField(fEffort, v)
		if msg := scr.messageLine(); msg != "" {
			t.Errorf("live message for effort %q = %q; padding and case must not warn", v, msg)
		}
	}
	setField(fEffort, " "+testfixtures.AKIAExample)
	if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
		t.Errorf("live message for a padded credential effort = %q; want the masked warning", msg)
	}
}

// TestModelFormAddPaddedFamilyAccepted drives the padded-family case end to
// end: the field advances, the approval opens, and the stored bytes are the
// trimmed family (the same bytes buildModel already produced). Since P2-5
// the commit of the ID auto-fills the family, so the typed text replaces
// the inferred value (the same keystrokes a user seeing the prefill types).
func TestModelFormAddPaddedFamilyAccepted(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	app.CaptureRun(&ScriptedKeys{Keys: append(typeRunes("acme-new"), KeyTab)})
	if got := app.Top().(*modelFormScreen).form.Value(fFamily); got != "acme" {
		t.Fatalf("family after the ID commit = %q, want the inferred acme", got)
	}
	keys := []Key{}
	for i := 0; i < len("acme"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("  swift  ")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("beta")...)
	for i := 0; i < 9; i++ {
		keys = append(keys, KeyTab)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if strings.Contains(out, "(value hidden)") {
		t.Fatalf("a padded benign family raised the secret refusal; frames:\n%s", out)
	}
	if !strings.Contains(out, "y approve write") {
		t.Fatalf("approval modal never opened for a padded family; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	if len(models) != 3 {
		t.Fatalf("models on disk = %d; want 3", len(models))
	}
	if got := models[2].Family; got != "swift" {
		t.Fatalf("family on disk = %q; want the trimmed %q", got, "swift")
	}
}

// wizardPresetModel is the wizard's preset-selection conversion
// (internal/providers/wizard.go:264-277) reproduced against the compiled
// preset table: a preset row becomes the registry record with
// ContextLength/MaxOutputTokens carried through verbatim. That conversion is
// how the -1 "server maximum" sentinel reaches providers.json in the first
// place, so seeding it here IS the wizard path for this package (promptCustom-
// Model is unexported in internal/providers).
func wizardPresetModel(t *testing.T, providerName string, idx int) providers.Model {
	t.Helper()
	presets := providers.GetModelPresets(providerName)
	if len(presets) <= idx {
		t.Fatalf("provider %q has %d presets, need index %d", providerName, len(presets), idx)
	}
	p := presets[idx]
	caps := p.Capabilities
	if caps.SupportsThinking && caps.ReasoningEffort == "" {
		caps.ReasoningEffort = "default"
	}
	return providers.Model{
		ID:              p.ID,
		Family:          p.Family,
		Tags:            p.Tags,
		ContextLength:   p.ContextLength,
		MaxOutputTokens: p.MaxOutputTokens,
		Capabilities:    caps,
	}
}

// serverMaxFixtureReg is a two-model registry built the two ways a
// "server maximum" limit reaches disk: acme-preset comes from the compiled
// preset table (MaxOutputTokens -1), acme-max is the wizard's custom prompt
// answered with -1 at both limit questions (wizard.go:687-697).
func serverMaxFixtureReg(t *testing.T) *providers.Registry {
	t.Helper()
	preset := wizardPresetModel(t, "deepseek", 0)
	if preset.MaxOutputTokens != -1 {
		t.Fatalf("preset fixture MaxOutputTokens = %d, want -1 (fixture contract)", preset.MaxOutputTokens)
	}
	return &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{
				{ID: preset.ID, Family: preset.Family, ContextLength: preset.ContextLength,
					MaxOutputTokens: preset.MaxOutputTokens, Capabilities: preset.Capabilities},
				{ID: "acme-max", Family: "fast", ContextLength: -1, MaxOutputTokens: -1,
					Capabilities: providers.Capabilities{SupportsTools: true, AllowedTools: []string{"read_file"}}},
			},
		},
	}}
}

// TestServerMaxTokenInputPinsWireValue pins the one sentinel spelling against
// the wire value the other doors already speak: the compiled presets carry it,
// the catalog reads it as "server maximum", and the form's parser must map the
// typed text back to exactly that integer. A second spelling (or a parser that
// still folded it away) fails here rather than silently in a saved registry.
func TestServerMaxTokenInputPinsWireValue(t *testing.T) {
	if ServerMaxTokenInput != "-1" {
		t.Fatalf("ServerMaxTokenInput = %q, want the pinned spelling %q", ServerMaxTokenInput, "-1")
	}
	preset := wizardPresetModel(t, "deepseek", 0)
	if got := parseTokenLimit(ServerMaxTokenInput); got != preset.MaxOutputTokens {
		t.Fatalf("parseTokenLimit(%q) = %d, want the preset wire value %d", ServerMaxTokenInput, got, preset.MaxOutputTokens)
	}
	if got := parseTokenLimit(ServerMaxTokenInput); got != -1 {
		t.Fatalf("parseTokenLimit(%q) = %d, want -1", ServerMaxTokenInput, got)
	}
	// Unset stays 0 and a positive limit round-trips unchanged: the sentinel
	// widened the parser by exactly one spelling.
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"200000", 200000},
		{" 8192 ", 8192},
		{"-1", -1},
		{" -1 ", -1},
		{"-01", 0}, // not the canonical spelling, and not a positive limit
		{"-5", 0},  // unreachable defensive path: the validator refuses it first
	} {
		if got := parseTokenLimit(tc.in); got != tc.want {
			t.Fatalf("parseTokenLimit(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestModelFormServerMaxEditRoundTrip is the F2 regression: open a
// wizard-created model whose limits are the server-maximum sentinel, save it
// untouched, and assert the sentinel survives byte-for-byte. Before P2-1 the
// field seeded blank and the save wrote 0, which the catalog reads as "no limit
// declared" and replaced with its 131072/16384 fallbacks.
func TestModelFormServerMaxEditRoundTrip(t *testing.T) {
	app, path := newModelFormTestAppReg(t, serverMaxFixtureReg(t), "edit", "acme-max")
	out := app.CaptureRun(&ScriptedKeys{})

	scr := app.Top().(*modelFormScreen)
	if got := scr.form.Value(fContext); got != ServerMaxTokenInput {
		t.Fatalf("context field seeded %q, want the sentinel %q; frames:\n%s", got, ServerMaxTokenInput, out)
	}
	if got := scr.form.Value(fMaxOut); got != ServerMaxTokenInput {
		t.Fatalf("max output field seeded %q, want the sentinel %q; frames:\n%s", got, ServerMaxTokenInput, out)
	}
	if n := strings.Count(out, ServerMaxTokenInput); n < 2 {
		t.Fatalf("seeded sentinel rendered %d times, want both limit rows; frames:\n%s", n, out)
	}
	if scr.dirty() {
		t.Fatal("form is dirty on open: the sentinel seed does not round-trip through buildModel")
	}

	// Walk to the Save row and open the approval without editing anything.
	keys := make([]Key, 0, fCount+1)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Updates model acme-max in provider acme.") {
		t.Fatalf("approval never opened; frames:\n%s", out)
	}
	if !strings.Contains(out, "(no field changes)") {
		t.Fatalf("pristine save raised a phantom diff; frames:\n%s", out)
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	var got *providers.Model
	for i := range models {
		if models[i].ID == "acme-max" {
			got = &models[i]
		}
	}
	if got == nil {
		t.Fatal("acme-max missing after save")
	}
	if got.ContextLength != -1 || got.MaxOutputTokens != -1 {
		t.Fatalf("persisted limits = context %d / maxOut %d, want -1 / -1 (sentinel destroyed)",
			got.ContextLength, got.MaxOutputTokens)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	for _, want := range []string{`"contextLength": -1`, `"maxOutputTokens": -1`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("on-disk JSON missing %s:\n%s", want, raw)
		}
	}
	if !strings.Contains(app.Message(), "Model acme-max updated in acme.") {
		t.Errorf("success message missing; message = %q", app.Message())
	}
}

// TestModelFormServerMaxPresetSeed pins the preset-built sibling: a record
// whose MaxOutputTokens came from the compiled preset table (-1) displays the
// sentinel and keeps it through a save that only touches another field.
func TestModelFormServerMaxPresetSeed(t *testing.T) {
	reg := serverMaxFixtureReg(t)
	presetID := reg.Providers[0].Models[0].ID
	app, path := newModelFormTestAppReg(t, reg, "edit", presetID)
	out := app.CaptureRun(&ScriptedKeys{})

	scr := app.Top().(*modelFormScreen)
	if got := scr.form.Value(fMaxOut); got != ServerMaxTokenInput {
		t.Fatalf("preset max output seeded %q, want %q; frames:\n%s", got, ServerMaxTokenInput, out)
	}
	if got := scr.form.Value(fContext); got != "128000" {
		t.Fatalf("preset context seeded %q, want 128000", got)
	}

	// Retag the model, then save: an unrelated edit must not disturb the limit.
	keys := []Key{KeyDown, KeyDown, RuneKey('x')}
	for i := 0; i < fCount-2; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})
	if strings.Contains(out, "maxOutputTokens") {
		t.Fatalf("phantom maxOutputTokens diff on a tag-only edit; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	if models[0].ID != presetID {
		t.Fatalf("first model id = %q, want %q", models[0].ID, presetID)
	}
	if models[0].MaxOutputTokens != -1 {
		t.Fatalf("preset maxOutputTokens = %d after save, want -1", models[0].MaxOutputTokens)
	}
	if models[0].ContextLength != 128000 {
		t.Fatalf("preset contextLength = %d after save, want 128000", models[0].ContextLength)
	}
}

// TestModelFormServerMaxAddTyped drives the add flow with the sentinel typed
// into both limit fields: the typed spelling is accepted, signs the approval
// diff, and is persisted as the -1 wire value.
func TestModelFormServerMaxAddTyped(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := typeRunes("acme-typed")
	keys = append(keys, KeyTab) // family
	keys = append(keys, KeyTab) // tags
	keys = append(keys, KeyTab) // enabled (seeded "y", left untouched)
	keys = append(keys, KeyTab) // context
	keys = append(keys, typeRunes(ServerMaxTokenInput)...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes(ServerMaxTokenInput)...)
	for i := 0; i < 6; i++ {
		keys = append(keys, KeyTab)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	for _, want := range []string{
		"+ contextLength: -1",
		"+ maxOutputTokens: -1",
		"Model count: 2 -> 3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("add approval missing %q; frames:\n%s", want, out)
		}
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	m := models[2]
	if m.ID != "acme-typed" {
		t.Fatalf("new model id = %q, want acme-typed", m.ID)
	}
	if m.ContextLength != -1 || m.MaxOutputTokens != -1 {
		t.Fatalf("typed sentinel persisted as context %d / maxOut %d, want -1 / -1",
			m.ContextLength, m.MaxOutputTokens)
	}
}

// TestModelFormServerMaxOtherNegativesRefused pins the width of the widening:
// only the canonical sentinel joined the accepted set, so a negative limit that
// is not -1 is still refused by the existing catalog message and writes nothing.
func TestModelFormServerMaxOtherNegativesRefused(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	keys := append(typeRunes("acme-neg"), KeyTab, KeyTab, KeyTab, KeyTab) // to context
	keys = append(keys, typeRunes("-5")...)
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Context length must be a positive number, or -1 for server maximum.") {
		t.Fatalf("non-sentinel negative accepted; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fContext {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fContext)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after a refused negative limit")
	}

	// Same for a second sentinel-shaped spelling that is not canonical: the
	// comparison is exact, so "-01" parses to -1 but is not the accepted
	// spelling, and Atoi's negative value keeps it out of the accepted set.
	keys = []Key{KeyBackspace, KeyBackspace, RuneKey('-'), RuneKey('0'), RuneKey('1'), KeyEnter}
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "Context length must be a positive number, or -1 for server maximum.") {
		t.Fatalf("non-canonical spelling -01 accepted; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after a refused non-canonical sentinel")
	}
}

// limitPickerTestRows pins the P2-2 picker contract from a single place: the
// label column is locale-neutral literals plus the server-maximum label, the
// value column is exactly what typing the choice would produce, and the
// sentinel row writes the ONE P2-1 spelling (never its own wire value).
func limitPickerTestRows(t *testing.T) {
	t.Helper()
	want := []limitPickerOption{
		{"128k", "131072"},
		{"256k", "262144"},
		{"512k", "524288"},
		{"1M", "1048576"},
		{limitPickerServerMaxLabel, ServerMaxTokenInput},
	}
	if len(limitPickerOptions) != len(want) {
		t.Fatalf("limitPickerOptions = %d rows, want %d", len(limitPickerOptions), len(want))
	}
	for i, w := range want {
		if limitPickerOptions[i] != w {
			t.Fatalf("limitPickerOptions[%d] = %+v, want %+v", i, limitPickerOptions[i], w)
		}
	}
	if limitPickerOptions[len(want)-1].value != "-1" {
		t.Fatalf("server-max row value = %q, want the P2-1 spelling %q",
			limitPickerOptions[len(want)-1].value, "-1")
	}
	for _, o := range limitPickerOptions[:len(want)-1] {
		n, err := strconv.Atoi(o.value)
		if err != nil || n <= 0 {
			t.Fatalf("preset row %q writes non-positive value %q", o.label, o.value)
		}
		if err := validateTokenLimit(o.value, "msg"); err != nil {
			t.Fatalf("preset row %q value %q fails the field validator: %v", o.label, o.value, err)
		}
	}
}

// driveLimitPicker navigates from the form open state to one limit field,
// opens the picker on Enter, and returns the screen for further assertions.
func driveLimitPicker(t *testing.T, app *App, downs int) *modelFormScreen {
	t.Helper()
	for i := 0; i < downs; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("modal after Enter on limit field = %T, want *Choice; frames:\n%s",
			app.Modal(), app.CaptureRun(&ScriptedKeys{}))
	}
	return app.Top().(*modelFormScreen)
}

// TestModelFormLimitPickerContext pins the P2-2 dispatch for the context
// field: Enter opens the shared Choice overlay, arrow-select lands the chosen
// row's numeric bytes in the SAME free-text field, the field stays focused
// and editable (typing still appends), and the value flows to disk on save.
func TestModelFormLimitPickerContext(t *testing.T) {
	limitPickerTestRows(t)
	app, path := newModelFormTestApp(t, "add", "")
	// Type the id first (add mode requires one), then walk down to the field.
	app.CaptureRun(&ScriptedKeys{Keys: typeRunes("acme-pick")})
	scr := driveLimitPicker(t, app, fContext) // cursor starts on fModelID

	ch := app.Modal().(*Choice)
	if title := ch.Title(); title != "Context length" {
		t.Fatalf("picker title = %q, want the reused field label %q", title, "Context length")
	}
	body := strings.Join(ch.Body(80), "\n")
	for _, o := range limitPickerOptions {
		if !strings.Contains(body, o.label) {
			t.Fatalf("picker body missing row label %q; body:\n%s", o.label, body)
		}
	}

	// Arrow down to "256k", Enter writes back. The overlay closes; on a dirty
	// form the Esc-guard re-arms behind it (same contract as the tools
	// overlay), so the assertion is "no Choice", not "no modal".
	app.Step(KeyDown)
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("picker still open after Enter")
	}
	if got := scr.form.Value(fContext); got != "262144" {
		t.Fatalf("context field = %q, want 262144 (picker write-back)", got)
	}
	if scr.form.Cursor() != fContext {
		t.Fatalf("cursor = %d, want %d (field stays focused)", scr.form.Cursor(), fContext)
	}
	if !scr.dirty() {
		t.Fatal("picker write-back left the form clean")
	}

	// The field stays the authority: typing still edits it.
	app.Step(RuneKey('7'))
	if got := scr.form.Value(fContext); got != "2621447" {
		t.Fatalf("field not editable after picker; value = %q, want 2621447", got)
	}
	app.Step(KeyBackspace)

	// Save end to end: the picked value is what the approval signs and disk
	// receives.
	keys := make([]Key, 0, fCount+2)
	for i := fContext; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "+ contextLength: 262144") {
		t.Fatalf("add approval missing the picked limit; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.ContextLength != 262144 {
		t.Fatalf("contextLength on disk = %d, want 262144", m.ContextLength)
	}
}

// TestModelFormLimitPickerMaxOut pins the same dispatch for the max output
// field and the server-maximum entry: the last row writes the ONE P2-1
// spelling into the field, save persists the -1 wire value.
func TestModelFormLimitPickerMaxOut(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	app.CaptureRun(&ScriptedKeys{Keys: typeRunes("acme-maxout")})
	scr := driveLimitPicker(t, app, fMaxOut)

	ch := app.Modal().(*Choice)
	if title := ch.Title(); title != "Max output tokens" {
		t.Fatalf("picker title = %q, want the reused field label %q", title, "Max output tokens")
	}
	for i := 0; i < len(limitPickerOptions)-1; i++ {
		app.Step(KeyDown)
	}
	if label := ch.list.Row(ch.Cursor()).Label; label != limitPickerServerMaxLabel {
		t.Fatalf("last row label = %q, want %q", label, limitPickerServerMaxLabel)
	}
	app.Step(KeyEnter)
	if got := scr.form.Value(fMaxOut); got != ServerMaxTokenInput {
		t.Fatalf("max out field = %q, want the sentinel spelling %q", got, ServerMaxTokenInput)
	}
	if err := validateTokenLimit(scr.form.Value(fMaxOut), "msg"); err != nil {
		t.Fatalf("picker-written sentinel fails the field validator: %v", err)
	}

	keys := make([]Key, 0, fCount+2)
	for i := fMaxOut; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "+ maxOutputTokens: -1") {
		t.Fatalf("add approval missing the sentinel; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.MaxOutputTokens != -1 {
		t.Fatalf("maxOutputTokens on disk = %d, want -1", m.MaxOutputTokens)
	}
}

// TestModelFormLimitPickerSnapshotRestore pins that a picker-written value is
// ordinary field state: it appears in Snapshot, dirty tracking sees it, and
// Restore (the discard-confirm path) returns the field to its construction
// value with the form clean again.
func TestModelFormLimitPickerSnapshotRestore(t *testing.T) {
	reg := serverMaxFixtureReg(t)
	presetID := reg.Providers[0].Models[0].ID // contextLength 128000, maxOut -1
	app, _ := newModelFormTestAppReg(t, reg, "edit", presetID)
	scr := driveLimitPicker(t, app, fContext) // seeds "128000"

	app.Step(KeyDown) // "256k"
	app.Step(KeyDown) // "512k"
	app.Step(KeyEnter)
	if got := scr.form.Value(fContext); got != "524288" {
		t.Fatalf("context field = %q, want 524288", got)
	}
	if got := scr.form.Snapshot()[fContext]; got != "524288" {
		t.Fatalf("Snapshot[fContext] = %q, want the picker value 524288", got)
	}
	if !scr.dirty() {
		t.Fatal("form clean after a picker write on a seeded record")
	}

	scr.form.Restore(scr.baseVals)
	if got := scr.form.Value(fContext); got != "128000" {
		t.Fatalf("restored context field = %q, want the seeded 128000", got)
	}
	if got := scr.form.Value(fMaxOut); got != ServerMaxTokenInput {
		t.Fatalf("restore disturbed the sibling limit: %q", got)
	}
	if scr.dirty() {
		t.Fatal("form still dirty after Restore")
	}
}

// TestModelFormLimitPickerEscKeepsTypedValue pins the cancel arm: Esc closes
// the overlay, leaves the free-text field byte-identical (even when it holds
// typed junk the picker opened over... it cannot: the picker only opens over
// an accepted value, so here a typed valid value), and the form keeps its
// dirty state.
func TestModelFormLimitPickerEscKeepsValue(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	keys := append(typeRunes("acme-esc"), KeyTab, KeyTab, KeyTab, KeyTab) // to context
	keys = append(keys, typeRunes("999")...)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	scr := app.Top().(*modelFormScreen)

	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("picker did not open over a valid typed value; modal = %T", app.Modal())
	}
	app.Step(KeyDown)
	app.Step(KeyEsc)
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("overlay still open after Esc")
	}
	if got := scr.form.Value(fContext); got != "999" {
		t.Fatalf("Esc changed the field: %q, want 999", got)
	}
	if !scr.dirty() {
		t.Fatal("Esc cancelled the overlay but cleared the typed dirty state")
	}
}

// TestModelFormLimitPickerRefusedValueBlocksOpen pins the validate-then-open
// arm: Enter over a value the field refuses keeps the plain Enter behaviour
// (the catalog refusal message, no advance, no overlay), so the picker never
// opens on top of text the save would reject.
func TestModelFormLimitPickerRefusedValueBlocksOpen(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	keys := append(typeRunes("acme-blk"), KeyTab, KeyTab, KeyTab, KeyTab) // to context
	keys = append(keys, typeRunes("12x")...)
	out := app.CaptureRun(&ScriptedKeys{Keys: append(keys, KeyEnter)})
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("picker opened over a refused value")
	}
	if !strings.Contains(out, "Context length must be a positive number, or -1 for server maximum.") {
		t.Fatalf("refusal message missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fContext {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fContext)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after a refused limit value")
	}
}

// effortPickerTestRows pins the P2-3 picker contract from a single place: the
// row set is EXACTLY the validEffortValues closed set (no drift in either
// direction), every row passes the field validator, and the rows are plain
// locale-neutral literals (label == value; no per-value catalog labels exist,
// so nothing can be reused and nothing new is needed).
func effortPickerTestRows(t *testing.T) {
	t.Helper()
	if len(effortPickerOptions) != len(validEffortValues) {
		t.Fatalf("effortPickerOptions = %d rows, want %d (the validEffortValues set size)",
			len(effortPickerOptions), len(validEffortValues))
	}
	seen := map[string]bool{}
	for _, v := range effortPickerOptions {
		if seen[v] {
			t.Fatalf("effortPickerOptions has duplicate row %q", v)
		}
		seen[v] = true
		if !validEffortValues[v] {
			t.Fatalf("picker row %q is not in validEffortValues", v)
		}
		// The validator's acceptance arm for a literal is normEffort + the set
		// lookup (the secret guard is irrelevant for plain words).
		if e := normEffort(v); e == "" || !validEffortValues[e] {
			t.Fatalf("picker row %q fails the field validator's acceptance rule", v)
		}
	}
	for v := range validEffortValues {
		if !seen[v] {
			t.Fatalf("validEffortValues entry %q missing from effortPickerOptions", v)
		}
	}
}

// TestModelFormEffortPickerRoundTrip drives every picker row end to end: open
// the picker on the effort field, arrow to the row, Enter writes the literal
// back into the SAME free-text field, the field stays focused and editable,
// and save persists the picked value to disk. One subtest per row so a row
// regression names itself.
func TestModelFormEffortPickerRoundTrip(t *testing.T) {
	effortPickerTestRows(t)
	for idx, want := range effortPickerOptions {
		t.Run(want, func(t *testing.T) {
			app, path := newModelFormTestApp(t, "add", "")
			app.CaptureRun(&ScriptedKeys{Keys: typeRunes("acme-effort-" + want)})
			for i := 0; i < fEffort; i++ {
				app.Step(KeyDown)
			}
			app.Step(KeyEnter)
			ch, ok := app.Modal().(*Choice)
			if !ok {
				t.Fatalf("modal after Enter on effort field = %T, want *Choice; frames:\n%s",
					app.Modal(), app.CaptureRun(&ScriptedKeys{}))
			}
			if title := ch.Title(); title != "Reasoning effort" {
				t.Fatalf("picker title = %q, want the reused field label %q", title, "Reasoning effort")
			}
			body := strings.Join(ch.Body(80), "\n")
			for _, o := range effortPickerOptions {
				if !strings.Contains(body, o) {
					t.Fatalf("picker body missing row %q; body:\n%s", o, body)
				}
			}
			for i := 0; i < idx; i++ {
				app.Step(KeyDown)
			}
			app.Step(KeyEnter)
			if _, ok := app.Modal().(*Choice); ok {
				t.Fatal("picker still open after Enter")
			}
			scr := app.Top().(*modelFormScreen)
			if got := scr.form.Value(fEffort); got != want {
				t.Fatalf("effort field = %q, want %q (picker write-back)", got, want)
			}
			if scr.form.Cursor() != fEffort {
				t.Fatalf("cursor = %d, want %d (field stays focused)", scr.form.Cursor(), fEffort)
			}
			if !scr.dirty() {
				t.Fatal("picker write-back left the form clean")
			}

			// The field stays the authority: typing still edits it.
			app.Step(RuneKey('x'))
			if got := scr.form.Value(fEffort); got != want+"x" {
				t.Fatalf("field not editable after picker; value = %q, want %q", got, want+"x")
			}
			app.Step(KeyBackspace)
			if got := scr.form.Value(fEffort); got != want {
				t.Fatalf("field value = %q after the probe edit, want %q", got, want)
			}

			// Save end to end: the picked value is what the approval signs and
			// disk receives.
			keys := make([]Key, 0, fCount+2)
			for i := fEffort; i < fCount; i++ {
				keys = append(keys, KeyDown)
			}
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})
			if !strings.Contains(out, "+ reasoningEffort: "+want) {
				t.Fatalf("add approval missing the picked effort; frames:\n%s", out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
			m := reloadModels(t, path)[2]
			if m.Capabilities.ReasoningEffort != want {
				t.Fatalf("reasoningEffort on disk = %q, want %q", m.Capabilities.ReasoningEffort, want)
			}
		})
	}
}

// TestModelFormEffortPickerEditModeChange pins the edit-mode arm of a picker
// write: the form field is rebuilt by setFieldValue, which never runs the
// effortTouched latch (a key-path-only bit), yet a pick that differs from the
// on-disk value must still flow to disk through the normalized value
// comparison in buildModel.
func TestModelFormEffortPickerEditModeChange(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{
				{ID: "acme-e1", Family: "fast", ContextLength: 1000, MaxOutputTokens: 500,
					Capabilities: providers.Capabilities{SupportsThinking: true, ReasoningEffort: "high"}},
			},
		},
	}}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-e1")
	scr := app.Top().(*modelFormScreen)
	if got := scr.form.Value(fEffort); got != "high" {
		t.Fatalf("seeded effort field = %q, want high", got)
	}
	for i := 0; i < fEffort; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("picker did not open on the effort field; modal = %T", app.Modal())
	}
	// Arrow to "xhigh" (index 5).
	for i := 0; i < 5; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	if got := scr.form.Value(fEffort); got != "xhigh" {
		t.Fatalf("effort field = %q, want xhigh (picker write-back)", got)
	}
	if scr.effortTouched {
		t.Fatal("picker write-back set effortTouched; the latch is key-path-only")
	}

	keys := make([]Key, 0, fCount+2)
	for i := fEffort; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "  reasoningEffort: high -> xhigh") {
		t.Fatalf("edit approval missing the picked effort change; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	if m.Capabilities.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoningEffort on disk = %q, want xhigh", m.Capabilities.ReasoningEffort)
	}
}

// TestModelFormEffortPickerEscKeepsValue pins the cancel arm: Esc closes the
// overlay, leaves the free-text field byte-identical, and keeps the dirty
// state.
func TestModelFormEffortPickerEscKeepsValue(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	keys := append(typeRunes("acme-effesc"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to effort
	keys = append(keys, typeRunes("medium")...)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	scr := app.Top().(*modelFormScreen)

	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("picker did not open over a valid typed value; modal = %T", app.Modal())
	}
	app.Step(KeyDown)
	app.Step(KeyEsc)
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("overlay still open after Esc")
	}
	if got := scr.form.Value(fEffort); got != "medium" {
		t.Fatalf("Esc changed the field: %q, want medium", got)
	}
	if !scr.dirty() {
		t.Fatal("Esc cancelled the overlay but cleared the typed dirty state")
	}
}

// TestModelFormEffortPickerRefusedValueBlocksOpen pins the validate-then-open
// arm: Enter over a value the field refuses keeps the plain Enter behaviour
// (the catalog refusal message, no advance, no overlay), so the picker never
// opens on top of text the save would reject.
func TestModelFormEffortPickerRefusedValueBlocksOpen(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	keys := append(typeRunes("acme-effblk"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to effort
	keys = append(keys, typeRunes("bogus")...)
	out := app.CaptureRun(&ScriptedKeys{Keys: append(keys, KeyEnter)})
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("picker opened over a refused value")
	}
	if !strings.Contains(out, "Reasoning effort must be one of:") {
		t.Fatalf("refusal message missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fEffort {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fEffort)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after a refused effort value")
	}
}

// TestModelFormCapabilityDefaultsAddMode pins U3 (P2-4): in add mode the
// thinking, vision and video flags seed "y" and persist true on save, and an
// explicit "n" typed over the seed stays the manual override. Wizard parity
// lives in TestWizardCapabilityDefaultsYTaken.
func TestModelFormCapabilityDefaultsAddMode(t *testing.T) {
	cases := []struct {
		name    string
		field   int
		enabled func(providers.Capabilities) bool
	}{
		{"thinking", fThinking, func(c providers.Capabilities) bool { return c.SupportsThinking }},
		{"vision", fVision, func(c providers.Capabilities) bool { return c.SupportsVision }},
		{"video", fVideo, func(c providers.Capabilities) bool { return c.SupportsVideo }},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/default_y_saved", func(t *testing.T) {
			app, path := newModelFormTestApp(t, "add", "")
			out := app.CaptureRun(&ScriptedKeys{})

			scr := app.Top().(*modelFormScreen)
			if got := scr.form.Value(tc.field); got != "y" {
				t.Fatalf("add-mode %s seed = %q, want \"y\"; frames:\n%s", tc.name, got, out)
			}
			if scr.dirty() {
				t.Fatal("form is dirty on open: the Y seed must be the pristine state")
			}

			// Walk to the Save row, open the approval, confirm.
			keys := typeRunes("acme-def-" + tc.name)
			for i := 0; i < fCount; i++ {
				keys = append(keys, KeyTab)
			}
			keys = append(keys, KeyEnter)
			out = app.CaptureRun(&ScriptedKeys{Keys: keys})
			if !strings.Contains(out, "Adds model acme-def-"+tc.name+" to provider acme.") {
				t.Fatalf("approval never opened; frames:\n%s", out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

			m := reloadModels(t, path)[2]
			if m.ID != "acme-def-"+tc.name {
				t.Fatalf("new model id = %q", m.ID)
			}
			if !tc.enabled(m.Capabilities) {
				t.Fatalf("saved %s = false, want true (Y default did not persist)", tc.name)
			}
		})

		t.Run(tc.name+"/manual_n_override", func(t *testing.T) {
			app, path := newModelFormTestApp(t, "add", "")

			// Type the ID, tab to the flag field, replace the seeded "y" with "n".
			keys := append(typeRunes("acme-nov-"+tc.name), KeyTab)
			for i := 1; i < tc.field; i++ {
				keys = append(keys, KeyTab)
			}
			keys = append(keys, KeyBackspace)
			keys = append(keys, typeRunes("n")...)
			for i := tc.field; i < fCount; i++ {
				keys = append(keys, KeyTab)
			}
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})
			if !strings.Contains(out, "Adds model acme-nov-"+tc.name+" to provider acme.") {
				t.Fatalf("approval never opened; frames:\n%s", out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

			m := reloadModels(t, path)[2]
			if tc.enabled(m.Capabilities) {
				t.Fatalf("saved %s = true, want false (manual n override lost)", tc.name)
			}
		})
	}
}

// TestModelFormLimitPickerServerMaxLabelLocalized pins the P2-4 wiring of the
// P1-3 residual: the server-maximum row label resolves through the catalog key
// models.form.limit_server_max (present in all 12 locales since P1-3), not the
// English const. Under ko the overlay must show the Korean catalog value and
// never the English fallback; under en the catalog value and the const agree.
func TestModelFormLimitPickerServerMaxLabelLocalized(t *testing.T) {
	koLabel := i18n.Load("ko").T("models.form.limit_server_max")
	if koLabel == limitPickerServerMaxLabel {
		t.Fatalf("ko catalog value equals the English const %q; the localization pin is vacuous", koLabel)
	}
	if enLabel := i18n.Load("en").T("models.form.limit_server_max"); enLabel != limitPickerServerMaxLabel {
		t.Fatalf("en catalog value = %q, want the const %q (missing-key fallback contract)", enLabel, limitPickerServerMaxLabel)
	}

	SetLanguage("ko")
	defer SetLanguage("en")
	app, _ := newModelFormTestApp(t, "add", "")
	// Bind the ko bundle directly: the constructor reads the language from
	// persisted state, which the fixture home does not carry, and the wiring
	// under test is openLimitPicker's missingOr resolution over s.b.
	scr := &modelFormScreen{b: i18n.Load("ko"), app: app}
	scr.openLimitPicker(fContext)

	body := strings.Join(app.Modal().(*Choice).Body(80), "\n")
	if !strings.Contains(body, koLabel) {
		t.Fatalf("picker body missing the ko catalog label %q; body:\n%s", koLabel, body)
	}
	if strings.Contains(body, limitPickerServerMaxLabel) {
		t.Fatalf("picker body still shows the English const %q under ko; body:\n%s", limitPickerServerMaxLabel, body)
	}
}

// --- P2-6/P2-7: the model-ID picker (server model list download) -----------

// modelsListStub serves one fixed /models body (or one fixed non-200 status)
// for the model-ID picker tests. The ids and numbers are the sanitized
// samples recorded by the T-B live probe (survey-tb-models-live.md).
func modelsListStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// modelIDPickerApp seeds a one-provider registry whose base URL points at the
// stub server (loopback http is allowed) and opens the add-mode form.
func modelIDPickerApp(t *testing.T, baseURL string) (*App, string) {
	t.Helper()
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "acme",
		BaseURL: baseURL,
		Models:  nil,
	}}}
	return newModelFormTestAppReg(t, reg, "add", "")
}

// driveModelIDPicker opens the picker from the form-open state (the cursor
// starts on fModelID) and returns the screen.
func driveModelIDPicker(t *testing.T, app *App) *modelFormScreen {
	t.Helper()
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("modal after Enter on model ID field = %T, want *Choice; frames:\n%s",
			app.Modal(), app.CaptureRun(&ScriptedKeys{}))
	}
	return app.Top().(*modelFormScreen)
}

// savePickedModel drives from the model ID field to the save row, confirms the
// approval, and returns the first (only) model on disk.
func savePickedModel(t *testing.T, app *App, path string) providers.Model {
	t.Helper()
	keys := make([]Key, 0, fCount+2)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	models := reloadModels(t, path)
	if len(models) != 1 {
		t.Fatalf("models on disk = %d, want exactly the picked one", len(models))
	}
	return models[0]
}

const charmListFixture = `{"data":[
	{"id":"charm-small","context_window":256000,"max_output_tokens":16000},
	{"id":"charm-mid","context_window":262144,"max_output_tokens":26214},
	{"id":"charm-big","context_window":1048576,"max_output_tokens":384000}
]}`

const nwListFixture = `{"data":[
	{"id":"nw-ctx-256k","max_model_len":262128},
	{"id":"nw-ctx-1m","max_model_len":1048560}
]}`

// TestModelFormModelIDPickerCharmSeedsCeilings pins the full P2-7 happy path
// on the Charm-shaped payload (context_window + max_output_tokens, the T-B
// verified shape): the pick writes the id, seeds BOTH limit fields from the
// advertised ceilings, and the values flow to disk through the approval.
func TestModelFormModelIDPickerCharmSeedsCeilings(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, charmListFixture)
	app, path := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	ch := app.Modal().(*Choice)
	if title := ch.Title(); title != "Model ID" {
		t.Fatalf("picker title = %q, want the reused field label %q", title, "Model ID")
	}
	body := strings.Join(ch.Body(80), "\n")
	for _, id := range []string{"charm-small", "charm-mid", "charm-big"} {
		if !strings.Contains(body, id) {
			t.Fatalf("picker body missing %q; body:\n%s", id, body)
		}
	}

	app.Step(KeyDown) // charm-mid
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("picker still open after Enter")
	}
	if got := scr.form.Value(fModelID); got != "charm-mid" {
		t.Fatalf("model ID field = %q, want charm-mid", got)
	}
	if got := scr.form.Value(fContext); got != "262144" {
		t.Fatalf("context field = %q, want 262144 (advertised ceiling)", got)
	}
	if got := scr.form.Value(fMaxOut); got != "26214" {
		t.Fatalf("max out field = %q, want 26214 (advertised ceiling)", got)
	}
	if err := validateTokenLimit(scr.form.Value(fContext), "msg"); err != nil {
		t.Fatalf("seeded context fails the validator: %v", err)
	}
	if err := validateTokenLimit(scr.form.Value(fMaxOut), "msg"); err != nil {
		t.Fatalf("seeded max out fails the validator: %v", err)
	}
	if !scr.dirty() {
		t.Fatal("picker seed left the form clean")
	}

	keys := make([]Key, 0, fCount+2)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	for _, want := range []string{"+ contextLength: 262144", "+ maxOutputTokens: 26214"} {
		if !strings.Contains(out, want) {
			t.Fatalf("add approval missing %q; frames:\n%s", want, out)
		}
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	if m.ID != "charm-mid" || m.ContextLength != 262144 || m.MaxOutputTokens != 26214 {
		t.Fatalf("on-disk model = %+v, want charm-mid with 262144/26214", m)
	}
}

// TestModelFormModelIDPickerNWPartialCeilings pins the T-B caveat for the
// nw-shaped payload (max_model_len only): the context field seeds from the
// vLLM-named ceiling and the max-output field falls back to the
// server-maximum sentinel, so the sentinel finally has its data source on
// one of the two slots.
func TestModelFormModelIDPickerNWPartialCeilings(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, nwListFixture)
	app, path := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	app.Step(KeyEnter) // first row: nw-ctx-256k
	if got := scr.form.Value(fModelID); got != "nw-ctx-256k" {
		t.Fatalf("model ID field = %q, want nw-ctx-256k", got)
	}
	if got := scr.form.Value(fContext); got != "262128" {
		t.Fatalf("context field = %q, want 262128 (max_model_len alias)", got)
	}
	if got := scr.form.Value(fMaxOut); got != ServerMaxTokenInput {
		t.Fatalf("max out field = %q, want the sentinel %q (no advertised ceiling)", got, ServerMaxTokenInput)
	}

	keys := make([]Key, 0, fCount+2)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "+ maxOutputTokens: -1") {
		t.Fatalf("add approval missing the sentinel max-out; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	if m.ContextLength != 262128 || m.MaxOutputTokens != -1 {
		t.Fatalf("on-disk limits = %d/%d, want 262128/-1", m.ContextLength, m.MaxOutputTokens)
	}
}

// TestModelFormModelIDPickerNoCeilings pins the no-hard-dependency half: an
// ids-only payload (the pre-P2-7 contract) seeds BOTH fields with the
// sentinel and changes nothing else about the form's behaviour.
func TestModelFormModelIDPickerNoCeilings(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, `{"data":[{"id":"plain-1"},{"id":"plain-2"}]}`)
	app, path := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	app.Step(KeyDown)
	app.Step(KeyEnter) // plain-2
	if got := scr.form.Value(fModelID); got != "plain-2" {
		t.Fatalf("model ID field = %q, want plain-2", got)
	}
	if got := scr.form.Value(fContext); got != ServerMaxTokenInput {
		t.Fatalf("context field = %q, want the sentinel %q (no ceilings advertised)", got, ServerMaxTokenInput)
	}
	if got := scr.form.Value(fMaxOut); got != ServerMaxTokenInput {
		t.Fatalf("max out field = %q, want the sentinel %q (no ceilings advertised)", got, ServerMaxTokenInput)
	}

	m := savePickedModel(t, app, path)
	if m.ContextLength != -1 || m.MaxOutputTokens != -1 {
		t.Fatalf("on-disk limits = %d/%d, want -1/-1 (sentinel wire values)", m.ContextLength, m.MaxOutputTokens)
	}
}

// TestModelFormModelIDPickerMasksRemoteIDLabels pins the display rule of the
// picker (FIX-SEC-F1, review F-1) together with the r3 M-1 pick-door refusal:
// the option LABELS route through the registry-slot authority
// providers.MaskSlot, so a credential-shaped id served by the provider endpoint
// cannot ride raw into the overlay frame, while a benign id stays
// byte-identical (the picker keeps its readability). Picking the
// credential-shaped row is refused AT THE PICK by the save door's own rule: the
// field keeps its previous value (the raw bytes never enter the edit buffer, so
// Form.Render has nothing hostile to paint), no ceiling or family seeding runs,
// the refusal names itself on the message line, and nothing reaches
// providers.json. The benign row still picks, so the refusal costs the picker
// nothing.
func TestModelFormModelIDPickerMasksRemoteIDLabels(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK,
		`{"data":[{"id":"`+testfixtures.SKLive+`"},{"id":"acme-fast"}]}`)
	app, path := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	ch := app.Modal().(*Choice)
	rendered := strings.Join(ch.Body(80), "\n")
	if strings.Contains(rendered, testfixtures.SKLive) {
		t.Fatalf("picker body rendered the credential-shaped remote id raw; body:\n%s", rendered)
	}
	masked := providers.MaskSlot(testfixtures.SKLive)
	if masked == testfixtures.SKLive {
		t.Fatalf("fixture premise broken: MaskSlot(%q) is the identity", testfixtures.SKLive)
	}
	if !strings.Contains(rendered, masked) {
		t.Fatalf("picker body missing the masked label %q; body:\n%s", masked, rendered)
	}
	if !strings.Contains(rendered, "acme-fast") {
		t.Fatalf("benign id is no longer byte-identical in the picker; body:\n%s", rendered)
	}
	if !looksSecretValue(testfixtures.SKLive) {
		t.Fatalf("fixture premise broken: the pick door does not refuse %q", testfixtures.SKLive)
	}

	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	// Row 0 is the credential-shaped id: the pick is refused at the door, so the
	// raw bytes never reach the field and no frame can paint them (M-1).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatalf("picker still open after a refused pick; frames:\n%s", out)
	}
	if strings.Contains(out, testfixtures.SKLive) {
		t.Fatalf("frames painted the raw credential-shaped id after the pick; frames:\n%s", out)
	}
	if got := scr.form.Value(fModelID); got != "" {
		t.Fatalf("refused pick wrote the model ID field: %q, want the previous (empty) value", got)
	}
	if got := scr.form.Value(fFamily); got != "" {
		t.Fatalf("refused pick seeded the family field: %q, want it untouched", got)
	}
	if got := scr.form.Value(fContext); got != "" {
		t.Fatalf("refused pick seeded the context field: %q, want it untouched", got)
	}
	if got := scr.form.Value(fMaxOut); got != "" {
		t.Fatalf("refused pick seeded the max-out field: %q, want it untouched", got)
	}
	if scr.dirty() {
		t.Fatal("refused pick left the form dirty: nothing was written")
	}
	msg := scr.messageLine()
	if msg == "" || strings.Contains(msg, testfixtures.SKLive) {
		t.Fatalf("refusal message = %q, want the named secret refusal with no echoed value", msg)
	}
	if want := scr.b.T("models.validation.secret"); msg != want {
		t.Fatalf("refusal message = %q, want the save door's own sentence %q", msg, want)
	}
	if !strings.Contains(out, "looks like secret/key material") {
		t.Fatalf("frames missing the refusal sentence; frames:\n%s", out)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after a refused picked id")
	}

	// The benign row still picks through the same door: the refusal is a rule,
	// not a broken picker.
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("picker did not reopen after the refusal; modal = %T", app.Modal())
	}
	app.Step(KeyDown)
	app.Step(KeyEnter)
	if got := scr.form.Value(fModelID); got != "acme-fast" {
		t.Fatalf("benign picked id = %q, want acme-fast", got)
	}
}

// TestModelFormModelIDPickerFailureModes pins the P2-6 failure surfacing:
// every silent nil of the old contract is now one in-screen message, the
// form is byte-untouched, and no overlay opens. The 3s deadline lives inside
// providers; these stubs answer (or refuse) immediately.
func TestModelFormModelIDPickerFailureModes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "non-200 surfaces the status",
			status: http.StatusForbidden,
			want:   "status 403",
		},
		{
			name:   "unreadable body surfaces the decode failure",
			status: http.StatusOK,
			body:   `{not json`,
			want:   missingModelFormFetchDecode,
		},
		{
			name:   "empty list surfaces its own message",
			status: http.StatusOK,
			body:   `{"data":[]}`,
			want:   missingModelFormFetchEmpty,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := modelsListStub(t, tc.status, tc.body)
			app, path := modelIDPickerApp(t, srv.URL)
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json: %v", err)
			}

			out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
			if app.Modal() != nil {
				t.Fatalf("modal after a failed fetch = %T, want none; frames:\n%s", app.Modal(), out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("frames missing %q; frames:\n%s", tc.want, out)
			}
			if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
				t.Fatal("on-disk JSON changed after a failed fetch")
			}
		})
	}

	t.Run("unreachable endpoint surfaces the connect failure", func(t *testing.T) {
		app, _ := modelIDPickerApp(t, "http://127.0.0.1:59999")
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
		if app.Modal() != nil {
			t.Fatalf("modal after a failed fetch = %T, want none; frames:\n%s", app.Modal(), out)
		}
		// The connect message carries the sanitized host operand.
		if !strings.Contains(out, "could not be reached") || !strings.Contains(out, "127.0.0.1:59999") {
			t.Fatalf("frames missing the connect sentence with host; frames:\n%s", out)
		}
	})
	// Note: the missing/blank base URL sentinel (ErrModelsNoBaseURL) has no
	// TUI-level case here, because the registry loader itself refuses such a
	// record before the form opens (loadErr arm). It is pinned at the
	// providers level in TestFetchRemoteModelsFailureKinds.
}

// TestModelFormModelIDPickerConnectHostMasked pins the FIX-SEC-F2 rule
// (review F-2 / MED-1): the fetch-failure sentence interpolates the provider
// host through fillHubSlots, which substitutes verbatim, so the connect arm
// masks the detail with providers.MaskOperand before the slot fill. A
// credential-shaped HOST is reachable from a hand-edited providers.json
// (load-time validation enforces scheme/host transport only, so a token-shaped
// label passes), and an unreachable endpoint then echoes it on every failed
// fetch. A benign host stays byte-identical, which is why the frame pin in
// TestModelFormModelIDPickerFailureModes keeps its raw loopback assertion.
func TestModelFormModelIDPickerConnectHostMasked(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	shapedHost := testfixtures.SKProj + ".example.com"
	want := providers.MaskOperand(shapedHost)
	if want == shapedHost {
		t.Fatalf("fixture premise broken: MaskOperand(%q) is the identity", shapedHost)
	}
	msg := scr.modelsFetchMessage(fmt.Errorf("%w (%s)", providers.ErrModelsConnect, shapedHost))
	if strings.Contains(msg, shapedHost) {
		t.Fatalf("connect message echoed the credential-shaped host raw: %q", msg)
	}
	if !strings.Contains(msg, want) {
		t.Fatalf("connect message = %q, want the masked host %q", msg, want)
	}
	if !strings.Contains(msg, "could not be reached") {
		t.Fatalf("connect message = %q, want the catalog connect sentence", msg)
	}

	// Benign hosts are identity under MaskOperand: the pinned raw form stays.
	benign := scr.modelsFetchMessage(fmt.Errorf("%w (%s)", providers.ErrModelsConnect, "127.0.0.1:59999"))
	if !strings.Contains(benign, "127.0.0.1:59999") {
		t.Fatalf("benign host no longer renders raw: %q", benign)
	}
}

// TestModelFormModelIDPickerEscKeepsValues pins the cancel arm: Esc closes
// the overlay, leaves every field byte-identical, and keeps the form clean.
func TestModelFormModelIDPickerEscKeepsValues(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, charmListFixture)
	app, _ := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	app.Step(KeyEsc)
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("overlay still open after Esc")
	}
	if got := scr.form.Value(fModelID); got != "" {
		t.Fatalf("Esc changed the model ID field: %q", got)
	}
	if got := scr.form.Value(fContext); got != "" {
		t.Fatalf("Esc changed the context field: %q", got)
	}
	if scr.dirty() {
		t.Fatal("Esc cancelled the overlay but left the form dirty")
	}
}

// TestModelFormModelIDPickerRefusedValueBlocksOpen pins the validate-then-open
// arm shared with the limit pickers: Enter over a value the ID field refuses
// keeps the plain Enter behaviour (no fetch, no overlay), so no network call
// is ever triggered on top of text the save would reject.
func TestModelFormModelIDPickerRefusedValueBlocksOpen(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, charmListFixture)
	app, path := modelIDPickerApp(t, srv.URL)
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: append(typeRunes(testfixtures.SKLive), KeyEnter)})
	// A dirty form arms the Esc guard behind any modal interaction; the pin is
	// that NO Choice (picker) opened and the plain Enter refusal blocked the
	// advance, not that the modal slot is empty.
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatalf("picker opened over a refused ID value; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if scr.form.Cursor() != fModelID {
		t.Fatalf("cursor = %d, want %d (advance blocked)", scr.form.Cursor(), fModelID)
	}
	if got := scr.form.Value(fModelID); got != testfixtures.SKLive {
		t.Fatalf("refused ID value changed: %q", got)
	}
	if got, _ := os.ReadFile(path); string(got) != string(diskBefore) {
		t.Fatal("on-disk JSON changed after a refused ID value")
	}
}

// TestLimitSeedFor pins the seeding rule from both sides: a present ceiling
// becomes the decimal field value, an absent one (0 = "not advertised", any
// non-positive likewise) becomes the sentinel spelling, and every possible
// output passes the field validator by construction.
func TestLimitSeedFor(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, ServerMaxTokenInput},
		{-1, ServerMaxTokenInput},
		{16000, "16000"},
		{262128, "262128"},
		{1048576, "1048576"},
	} {
		if got := limitSeedFor(tc.in); got != tc.want {
			t.Fatalf("limitSeedFor(%d) = %q, want %q", tc.in, got, tc.want)
		}
		if err := validateTokenLimit(limitSeedFor(tc.in), "msg"); err != nil {
			t.Fatalf("limitSeedFor(%d) fails the validator: %v", tc.in, err)
		}
	}
}

// --- P2-5: the family field auto-fills from the model ID --------------------

// walkToSaveRow appends the Tab keys that move the cursor from field `from`
// off the grid onto the Save row, then the Enter that opens the approval.
func walkToSaveRow(from int) []Key {
	keys := make([]Key, 0, fCount-from+1)
	for i := from; i < fCount; i++ {
		keys = append(keys, KeyTab)
	}
	return append(keys, KeyEnter)
}

// TestModelFormFamilyAutofillOnAdvance pins the core P2-5 behaviour: typing a
// model ID and committing it with Tab infers the family through
// providers.InferFamily (lowercased leading letters after the vendor
// prefix), the field stays manually editable afterwards, and the inferred
// value flows through the approval to disk.
func TestModelFormFamilyAutofillOnAdvance(t *testing.T) {
	if got := providers.InferFamily("acme/Qwen3-8B"); got != "qwen" {
		t.Fatalf("fixture premise broken: InferFamily(acme/Qwen3-8B) = %q, want qwen", got)
	}
	app, path := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	app.CaptureRun(&ScriptedKeys{Keys: append(typeRunes("acme/Qwen3-8B"), KeyTab)})
	if scr.form.Cursor() != fFamily {
		t.Fatalf("cursor = %d, want %d after Tab off the ID field", scr.form.Cursor(), fFamily)
	}
	if got := scr.form.Value(fFamily); got != "qwen" {
		t.Fatalf("family field = %q, want the inferred %q", got, "qwen")
	}
	if scr.familyTouched {
		t.Fatal("the auto-fill set familyTouched: the latch must follow hand edits only")
	}
	if !scr.dirty() {
		t.Fatal("auto-filled family left the form clean")
	}

	// The field stays the authority: typing still edits it.
	app.Step(RuneKey('2'))
	if got := scr.form.Value(fFamily); got != "qwen2" {
		t.Fatalf("family not editable after auto-fill: %q, want qwen2", got)
	}
	app.Step(KeyBackspace)

	// Save end to end: the inferred value is what the approval signs and disk
	// receives.
	out := app.CaptureRun(&ScriptedKeys{Keys: walkToSaveRow(fFamily)})
	if !strings.Contains(out, "+ family: qwen") {
		t.Fatalf("add approval missing the inferred family; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.Family != "qwen" {
		t.Fatalf("family on disk = %q, want qwen", m.Family)
	}
}

// TestModelFormFamilyAutofillReinfersOnIdChange pins that an untouched family
// follows the ID: editing the committed ID and advancing again re-infers the
// family from the new ID (the auto-fill is a mirror of the ID, not a
// one-time prefill).
func TestModelFormFamilyAutofillReinfersOnIdChange(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	keys := append(typeRunes("qwen3-8b"), KeyTab) // family -> "qwen"
	keys = append(keys, KeyUp)                    // back to the ID field
	for i := 0; i < len("qwen3-8b"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("mistral-large")...)
	keys = append(keys, KeyTab)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if got := scr.form.Value(fModelID); got != "mistral-large" {
		t.Fatalf("model ID field = %q, want mistral-large", got)
	}
	if got := scr.form.Value(fFamily); got != "mistral" {
		t.Fatalf("family field = %q, want the re-inferred mistral", got)
	}
}

// TestModelFormFamilyManualValueSurvives pins the precedence rule: a
// hand-edited family (even one that started as an auto-fill) is never
// overwritten by inference. The latch follows the text, so appending to the
// auto-filled value counts as a hand set.
func TestModelFormFamilyManualValueSurvives(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	keys := append(typeRunes("qwen3-8b"), KeyTab) // auto-fill: family "qwen"
	keys = append(keys, typeRunes("2")...)        // hand edit: family "qwen2"
	keys = append(keys, KeyUp)                    // back to the ID field
	for i := 0; i < len("qwen3-8b"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("mistral-large")...)
	keys = append(keys, KeyTab) // would re-infer "mistral" if the latch failed
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !scr.familyTouched {
		t.Fatal("familyTouched not set by a real hand edit")
	}
	if got := scr.form.Value(fFamily); got != "qwen2" {
		t.Fatalf("hand-set family overwritten by inference: %q, want qwen2", got)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: walkToSaveRow(fFamily)})
	if !strings.Contains(out, "+ family: qwen2") {
		t.Fatalf("add approval missing the hand-set family; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.Family != "qwen2" || m.ID != "mistral-large" {
		t.Fatalf("on-disk model = id %q family %q, want mistral-large/qwen2", m.ID, m.Family)
	}
}

// TestModelFormFamilyClearedStaysCleared pins that clearing the auto-filled
// family is itself a hand edit: the emptied field survives the save, and a
// later ID change does not resurrect an inferred family behind the user's
// back.
func TestModelFormFamilyClearedStaysCleared(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	keys := append(typeRunes("qwen3"), KeyTab) // auto-fill: family "qwen"
	for i := 0; i < len("qwen"); i++ {
		keys = append(keys, KeyBackspace) // clear the field: a hand edit
	}
	keys = append(keys, KeyUp) // back to the ID field
	keys = append(keys, typeRunes("-v2")...)
	keys = append(keys, KeyTab) // commit again; must NOT refill the family
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if got := scr.form.Value(fFamily); got != "" {
		t.Fatalf("cleared family resurrected by the ID commit: %q, want empty", got)
	}
	if got := scr.form.Value(fModelID); got != "qwen3-v2" {
		t.Fatalf("model ID field = %q, want qwen3-v2", got)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: walkToSaveRow(fFamily)})
	if strings.Contains(out, "+ family:") {
		t.Fatalf("add approval signed a family the user cleared; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.Family != "" {
		t.Fatalf("family on disk = %q, want empty (the user's clear is the authority)", m.Family)
	}
}

// TestModelFormFamilyEditModeKeepsStored pins that edit mode never
// auto-fills: the stored family seeds the field, advancing off the ID field
// (edited or not) leaves it byte-identical, and the form stays clean.
func TestModelFormFamilyEditModeKeepsStored(t *testing.T) {
	app, path := newModelFormTestApp(t, "edit", "acme-fast")
	scr := app.Top().(*modelFormScreen)
	if got := scr.form.Value(fFamily); got != "fast" {
		t.Fatalf("edit seed family = %q, want the stored fast", got)
	}

	// Edit the ID, then commit it: the stored family must not be re-inferred
	// from the new ID.
	app.CaptureRun(&ScriptedKeys{Keys: append(typeRunes("-v2"), KeyTab)})
	if got := scr.form.Value(fModelID); got != "acme-fast-v2" {
		t.Fatalf("model ID field = %q, want acme-fast-v2", got)
	}
	if got := scr.form.Value(fFamily); got != "fast" {
		t.Fatalf("edit mode overwrote the stored family: %q, want fast", got)
	}

	// Save: disk keeps the stored family under the renamed ID.
	out := app.CaptureRun(&ScriptedKeys{Keys: walkToSaveRow(fFamily)})
	if !strings.Contains(out, "family: fast -> fast") && strings.Contains(out, "family:") {
		t.Fatalf("approval signed an unexpected family change; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	found := false
	for _, m := range reloadModels(t, path) {
		if m.ID == "acme-fast-v2" {
			found = true
			if m.Family != "fast" {
				t.Fatalf("family on disk = %q, want the stored fast", m.Family)
			}
		}
	}
	if !found {
		t.Fatal("renamed model missing on disk")
	}
}

// TestModelFormFamilyDiscardClearsLatch pins the H-4 sweep for the new
// latch: the discard-confirm restores the construction values AND clears
// familyTouched before the form pops. The confirmed discard closes the form
// (the standing TestModelFormDirtyDiscard contract: y pops back to the
// caller), so there is no in-form auto-fill after it; the pin is that the
// restored screen object carries no stale latch.
func TestModelFormFamilyDiscardClearsLatch(t *testing.T) {
	app, _ := newModelFormTestApp(t, "add", "")
	scr := app.Top().(*modelFormScreen)

	// Type an ID, commit (family auto-fills), then hand-clear the family:
	// dirty, latch set. Esc opens the discard-confirm (through the formGuard),
	// y restores the construction state and pops the form.
	keys := append(typeRunes("qwen3"), KeyTab)
	for i := 0; i < len("qwen"); i++ {
		keys = append(keys, KeyBackspace)
	}
	app.CaptureRun(&ScriptedKeys{Keys: append(keys, KeyEsc, KeyYes)})
	if app.Depth() != 1 || app.Top().Title() != "Models" {
		t.Fatalf("stack = depth %d top %q, want 1/Models after the confirmed discard",
			app.Depth(), app.Top().Title())
	}
	if scr.familyTouched {
		t.Fatal("familyTouched survived the discard: the H-4 sweep is missing")
	}
	if got := scr.form.Value(fFamily); got != "" {
		t.Fatalf("family after restore = %q, want the construction value empty", got)
	}
	if scr.effortTouched {
		t.Fatal("effortTouched survived the discard")
	}
}

// TestModelFormFamilyPickerPickSeedsFamily pins the P2-7 seeding path: a pick
// from the model-ID fetch picker seeds the family from the picked id under
// the same gate as the typed-ID commit, and the value flows to disk.
func TestModelFormFamilyPickerPickSeedsFamily(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, charmListFixture)
	app, path := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	app.Step(KeyDown) // charm-mid
	app.Step(KeyEnter)
	if got := scr.form.Value(fModelID); got != "charm-mid" {
		t.Fatalf("model ID field = %q, want charm-mid", got)
	}
	if got := scr.form.Value(fFamily); got != providers.InferFamily("charm-mid") {
		t.Fatalf("family field = %q, want the inferred %q", got, providers.InferFamily("charm-mid"))
	}
	if scr.familyTouched {
		t.Fatal("the picker seed set familyTouched")
	}

	m := savePickedModel(t, app, path)
	if m.Family != "charm" {
		t.Fatalf("family on disk = %q, want charm", m.Family)
	}
}

// TestModelFormFamilyPickerPickKeepsManualFamily pins the picker-side
// precedence: a hand-set family survives a pick that rewrites the ID.
func TestModelFormFamilyPickerPickKeepsManualFamily(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, charmListFixture)
	app, path := modelIDPickerApp(t, srv.URL)
	scr := driveModelIDPicker(t, app)

	// Set the family by hand BEFORE picking. The overlay from
	// driveModelIDPicker is open; Esc cancels it (nothing is written), then
	// the form accepts keys again: type an ID, commit (the family
	// auto-fills), replace the auto-fill by hand, and clear the ID so the
	// picker becomes the id source again.
	app.Step(KeyEsc)
	if _, ok := app.Modal().(*Choice); ok {
		t.Fatal("stale overlay still open")
	}
	keys := append(typeRunes("tmp"), KeyTab)
	for i := 0; i < len("tmp"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typeRunes("custom")...)
	keys = append(keys, KeyUp)
	for i := 0; i < len("tmp"); i++ {
		keys = append(keys, KeyBackspace)
	}
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if got := scr.form.Value(fModelID); got != "" {
		t.Fatalf("model ID field = %q, want empty before the fresh pick", got)
	}
	if got := scr.form.Value(fFamily); got != "custom" {
		t.Fatalf("family field = %q, want the hand-set custom", got)
	}
	if !scr.familyTouched {
		t.Fatal("familyTouched not set by the hand edit")
	}

	// Reopen the picker (empty ID) and pick charm-mid: the ID changes, the
	// hand-set family must not.
	app.Step(KeyEnter)
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("picker did not reopen; frames:\n%s", app.CaptureRun(&ScriptedKeys{}))
	}
	app.Step(KeyDown)
	app.Step(KeyEnter)
	if got := scr.form.Value(fModelID); got != "charm-mid" {
		t.Fatalf("model ID field = %q, want charm-mid", got)
	}
	if got := scr.form.Value(fFamily); got != "custom" {
		t.Fatalf("picker pick overwrote the hand-set family: %q, want custom", got)
	}

	m := savePickedModel(t, app, path)
	if m.Family != "custom" {
		t.Fatalf("family on disk = %q, want custom", m.Family)
	}
}

// TestModelFormFamilyPickerPickEditModeKeepsStored pins that a pick in edit
// mode rewrites the ID and seeds the limits (P2-7) but never touches the
// stored family.
func TestModelFormFamilyPickerPickEditModeKeepsStored(t *testing.T) {
	srv := modelsListStub(t, http.StatusOK, charmListFixture)
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "acme",
		BaseURL: srv.URL,
		Models:  []providers.Model{{ID: "old-model", Family: "keptfam"}},
	}}}
	app, path := newModelFormTestAppReg(t, reg, "edit", "old-model")
	scr := app.Top().(*modelFormScreen)

	app.Step(KeyEnter) // seeded ID is valid -> picker opens
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("picker did not open in edit mode; frames:\n%s", app.CaptureRun(&ScriptedKeys{}))
	}
	app.Step(KeyDown) // charm-mid
	app.Step(KeyEnter)
	if got := scr.form.Value(fModelID); got != "charm-mid" {
		t.Fatalf("model ID field = %q, want charm-mid", got)
	}
	if got := scr.form.Value(fFamily); got != "keptfam" {
		t.Fatalf("edit-mode pick overwrote the stored family: %q, want keptfam", got)
	}

	// Save and confirm disk: renamed ID, stored family, seeded limits.
	keys := make([]Key, 0, fCount+2)
	for i := 0; i < fCount; i++ {
		keys = append(keys, KeyDown)
	}
	keys = append(keys, KeyEnter)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	models := reloadModels(t, path)
	if len(models) != 1 {
		t.Fatalf("models on disk = %d, want exactly the picked one", len(models))
	}
	m := models[0]
	if m.ID != "charm-mid" || m.Family != "keptfam" {
		t.Fatalf("on-disk model = id %q family %q, want charm-mid/keptfam", m.ID, m.Family)
	}
	if m.ContextLength != 262144 || m.MaxOutputTokens != 26214 {
		t.Fatalf("on-disk limits = %d/%d, want the seeded 262144/26214", m.ContextLength, m.MaxOutputTokens)
	}
}
