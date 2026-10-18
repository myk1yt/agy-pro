package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/settings"
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
	app.Push(NewModelFormScreen(core, "acme", modeName, modelID))
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
	keys = append(keys, typeRunes("swift")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("beta,fast")...)
	keys = append(keys, KeyTab) // enabled keeps the "y" default
	keys = append(keys, KeyTab) // context
	keys = append(keys, typeRunes("200000")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("8192")...)
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

	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
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

	keys := append(typeRunes("acme-new"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter, KeyNo)
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

	keys := append(typeRunes("acme-new"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter, KeyNo, KeyEsc)
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
	// second pops the clean form.
	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter, KeyNo, KeyEsc, KeyEsc}
	app.CaptureRun(&ScriptedKeys{Keys: keys})
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
		KeyDown, KeyDown, KeyDown, KeyDown,
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
func TestModelFormToolsFromForm(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-tool"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab)
	keys = append(keys, KeyEnter, KeySpace, KeyEnter) // open overlay, toggle read_file, save
	keys = append(keys, KeyTab, KeyEnter)             // save row, approval
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	for _, want := range []string{
		missingModelsToolsTitle,
		"+ tools: read_file",
		"Model count: 2 -> 3",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("tools flow missing %q; frames:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "[x] read_file") {
		t.Fatalf("overlay toggle missing; frames:\n%s", out)
	}

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	models := reloadModels(t, path)
	m := models[2]
	if m.ID != "acme-tool" {
		t.Fatalf("new model id = %q, want acme-tool", m.ID)
	}
	if len(m.Capabilities.AllowedTools) != 1 || m.Capabilities.AllowedTools[0] != "read_file" {
		t.Fatalf("AllowedTools = %v, want [read_file]", m.Capabilities.AllowedTools)
	}
	if !m.Capabilities.SupportsTools {
		t.Fatal("SupportsTools = false, want true with tools granted")
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

	if !strings.Contains(out, "Context length must be a positive number.") {
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
func TestLooksSecretValueModelIDShape(t *testing.T) {
	accept := []string{
		"anthropic/claude-3.5-sonnet",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo", // 39 runes
		"Qwen/Qwen2.5-72B-Instruct-Turbo",
		"meta-llama/llama-3.3-70b-instruct",
		"claude-3-5-sonnet-20241022",
		"acme-fast",
		"abcdefghijklmnopqrstuvwxyz", // 26 runes but a single-class word
		"1234567890123456",           // 16 runes but digits only
		"gpt-4.1-mini",
	}
	for _, id := range accept {
		if looksSecretValue(id) {
			t.Errorf("looksSecretValue(%q) = true, want false (legitimate model id/tag)", id)
		}
	}
	reject := []string{
		"AKIAIOSFODNN7EXAMPLE",                     // 20-rune AWS access-key id shape
		"AbCdEfGh0123456jKlMnoP",                   // 22-rune opaque mixed token
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", // 40-rune AWS secret, "/" runes (round-6 F-01)
		"Ab3dEf7Gh9jK1mNp+/Qr=",                    // 21-rune base64 token, "+/=" runes (round-6 F-01)
		"sk-abc",                                   // known prefix, any length
		"gsk_abcdefghijklm",                        // 17-rune prefix token
		"dckr_pat-abcdef0123456789",                // docker pat head
		"bearer abc",                               // bearer head
		"eyJhbGciOiJIUzI1NiIsInR5cCI6kpXVCJ9",      // JWT head
		"ghp_abcdefghij0123456789",                 // GitHub token head
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

	keys := append(typeRunes(longID), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyEnter)
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
	seed := "AKIAIOSFODNN7EXAMPLE"

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
// outside knownToolList (replace_in_file, read_url_content) survive the
// edit seed, the overlay persist, and the commit, and the approval diff's
// before column shows the FULL original list, so a removal can never hide.
func TestModelFormToolsUnionPassthrough(t *testing.T) {
	reg := modelsFixtureReg()
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{
		SupportsTools: true,
		AllowedTools:  []string{"replace_in_file", "read_file", "read_url_content"},
	}
	app, path := newModelFormTestAppReg(t, reg, "edit", "acme-fast")

	// Caps row, open the picker, additionally grant write_file, save.
	keys := []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
	keys = append(keys, KeyDown, KeyDown, KeyDown, KeyDown, KeySpace, KeyEnter) // write_file row, toggle, save
	keys = append(keys, KeyTab, KeyEnter)                                       // save row, approval
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	for _, want := range []string{
		"read_file, replace_in_file, read_url_content", // full before column
		"  tools: read_file, replace_in_file, read_url_content -> read_file, write_file, replace_in_file, read_url_content",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("union diff missing %q; frames:\n%s", want, out)
		}
	}

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[0]
	wantTools := []string{"read_file", "write_file", "replace_in_file", "read_url_content"}
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
// with SupportsTools set as allow-EVERY-tool), in both directions: an empty
// overlay save on a fresh add, and unchecking the last grant in edit mode.
func TestModelFormToolsEmptyRefused(t *testing.T) {
	// Add mode: open the picker, immediately Enter with nothing checked.
	app, path := newModelFormTestApp(t, "add", "")
	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab)
	keys = append(keys, KeyEnter, KeyEnter) // open picker, empty save
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, missingModelsToolsMinOne) {
		t.Fatalf("empty-selection refusal missing; frames:\n%s", out)
	}
	scr := app.Top().(*modelFormScreen)
	if len(scr.tools) != 0 {
		t.Fatalf("tools = %v, want unchanged empty after refusal", scr.tools)
	}
	// Saving with no tools at all stays safe: SupportsTools false means the
	// engine permits nothing.
	keys = []Key{KeyTab, KeyEnter}
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})
	if strings.Contains(out, "+ tools:") {
		t.Fatalf("approval shows a tools line for an untooled model; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	m := reloadModels(t, path)[2]
	if m.ID != "acme-x" {
		t.Fatalf("model id = %q, want acme-x", m.ID)
	}
	if m.Capabilities.SupportsTools || len(m.Capabilities.AllowedTools) != 0 {
		t.Fatalf("capabilities = %+v, want SupportsTools false with no grants", m.Capabilities)
	}
	if m.Capabilities.ToolPermitted("run_command") {
		t.Fatal("ToolPermitted(run_command) = true on a model with SupportsTools false")
	}

	// Edit mode: uncheck the last grant, Enter; the refusal keeps the
	// previous selection and nothing is written.
	app2, path2 := newModelFormTestApp(t, "edit", "acme-fast")
	diskBefore, _ := os.ReadFile(path2)
	keys = []Key{KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter}
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
