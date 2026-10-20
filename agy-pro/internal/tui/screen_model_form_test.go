package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"agypro/internal/cellwidth"
	"agypro/internal/openaiup"
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
func TestModelFormToolsFromForm(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-tool"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab)
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
		"abcdefghijklmnopqrstuvwxyz",               // 26 runes, single-class word: slot authority masks it (FIX-7)
		"1234567890123456",                         // 16 runes, digits only: slot authority masks it (FIX-7)
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
	full := "  tools: read_file, replace_in_file, read_url_content -> read_file, write_file, replace_in_file, read_url_content"
	clamped := cellwidth.Truncate(full, app.Width())
	if clamped == full || !strings.Contains(clamped, "->") {
		t.Fatalf("clamped diff = %q; want the width-%d cut that keeps the arrow", clamped, app.Width())
	}
	for _, want := range []string{
		"read_file, replace_in_file, read_url_content", // full before column
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
	keys := append(typeRunes("acme-x"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab)
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

// TestModelFormAddCapabilityFlags pins the phase-2 capability parity
// fields in add mode: thinking=y, effort=high, vision=y, video=n land on
// disk with all four flags, and the M4 approval diff shows the non-default
// lines (video=n is the default, so it gets no line).
func TestModelFormAddCapabilityFlags(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-caps"), KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab) // to thinking
	keys = append(keys, typeRunes("y")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("high")...)
	keys = append(keys, KeyTab)
	keys = append(keys, typeRunes("y")...)
	keys = append(keys, KeyTab)
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
	seed := "AKIAIOSFODNN7EXAMPLE"

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
	plant := "./AKIAIOSFODNN7EXAMPLE"
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
	for _, raw := range []string{plant, normEffort(plant), "AKIAIOSFODNN7EXAMPLE"} {
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
	tagPlant := "./AKIAIOSFODNN7EXAMPLE"
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
		"AKIAIOSFODNN7EXAMPLE",
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
	cfPlant := "./\u202e" + "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
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
// "1-ASIAIOSFODNN7EXAMPLE" rendered raw (lowercased) in the before column.
func TestModelFormEffortPlantRawMask(t *testing.T) {
	plant := "1-ASIAIOSFODNN7EXAMPLE"
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
	famPlant := "./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
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
	keys = append(keys, typeRunes("./AKIAIOSFODNN7EXAMPLE")...)
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
		"./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		`.\wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
		"https://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"./AKIAIOSFODNN7EXAMPLE",
		"file:///./AKIAIOSFODNN7EXAMPLE",
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
	const awsSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	const opaque = "ab3def7gh9jk1mnpqr5t" // 20 runes, lowercase, mixed classes
	refused := []struct{ name, value string }{
		{"bare drive backslash", `C:\` + awsSecret},
		{"bare drive slash", `c:/` + awsSecret},
		{"bare drive opaque run", `D:\` + opaque},
		{"relative then drive", `./C:\` + awsSecret},
		{"stacked drive", `C:\C:\` + awsSecret},
		{"leading separator then drive", `\C:\` + awsSecret},
		{"bidi override", "\u202eAKIAIOSFODNN7EXAMPLE"},
		{"bidi isolate with zero width space", "\u2067akia\u200bosfodnn7example\u2069"},
		{"byte order mark", "\uFEFFAKIAIOSFODNN7EXAMPLE"},
		{"two dot leader", "\u2025/" + opaque},
		{"horizontal ellipsis", "\u2026/" + opaque},
		{"fullwidth solidus", "\uFF0F" + "AKIAIOSFODNN7EXAMPLE"},
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
	const awsSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
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
		const plant = `C:\wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`
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
	"sk_live_0123456789abcdef",
	"tok_a1b2c3d4e5f6g7h8i9j0",
	"pk_live_0123456789abcdef",
	"key_0123456789abcdef",
	"AKIA_0123456789ABCDEF",
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
		{"sk_live_0123456789abcdef", false, true},
		{"tok_a1b2c3d4e5f6g7h8i9j0", false, true},
		{"pk_live_0123456789abcdef", false, true},
		{"key_0123456789abcdef", false, true},
		{"AKIA_0123456789ABCDEF", false, true},
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
		{"AKIAIOSFODNN7EXAMPLE", true, true},
		{"AbCdEfGh0123456jKlMnoP", true, true},
		{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", true, true},
		{"Ab3dEf7Gh9jK1mNp+/Qr=", true, true},
		{"sk-abc", true, true},
		{"gsk_abcdefghijklm", true, true},
		{"dckr_pat-abcdef0123456789", true, true},
		{"bearer abc", true, true},
		{"eyJhbGciOiJIUzI1NiIsInR5cCI6kpXVCJ9", true, true},
		{"ghp_abcdefghij0123456789", true, true},
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
		"sk_live_0123456789abcdef", "tok_a1b2c3d4e5f6g7h8i9j0", "pk_live_0123456789abcdef",
		"key_0123456789abcdef", "AKIA_0123456789ABCDEF",
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
		"sk_live_0123456789abcdef", "tok_a1b2c3d4e5f6g7h8i9j0", "pk_live_0123456789abcdef",
		"key_0123456789abcdef", "AKIA_0123456789ABCDEF", "sk-proj-0123456789abcdef",
		"openai2024backup", "npm_registry", "qwen2point5turbo", "eu-west1.fastproxy.example",
		"provider0123456789", "AKIAIOSFODNN7EXAMPLE", "ghp_abcdefghij0123456789",
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
		{"sk_live_0123456789abcdef", []string{"sk_live_0123456789abcdef"}},
		{"beta, sk_live_0123456789abcdef", []string{"beta", "sk_live_0123456789abcdef"}},
		{"beta,  tok_a1b2c3d4e5f6g7h8i9j0", []string{"beta", "tok_a1b2c3d4e5f6g7h8i9j0"}},
		{"./AKIAIOSFODNN7EXAMPLE", []string{"./AKIAIOSFODNN7EXAMPLE"}},
		{"beta, ./AKIAIOSFODNN7EXAMPLE", []string{"beta", "./AKIAIOSFODNN7EXAMPLE"}},
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
	setTags("beta, sk_live_0123456789abcdef")
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
	for _, v := range []string{" ./AKIAIOSFODNN7EXAMPLE", "swift,./AKIAIOSFODNN7EXAMPLE"} {
		if err := scr.validateFamily(v); err == nil {
			t.Fatalf("validateFamily(%q) accepted a decorated credential; trimming must not become a bypass", v)
		}
	}
	for _, v := range []string{"sk_live_0123456789abcdef", " sk_live_0123456789abcdef "} {
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
	setField(fFamily, " ./AKIAIOSFODNN7EXAMPLE")
	if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
		t.Errorf("live message for a padded decorated family = %q; want the masked warning", msg)
	}
	for _, v := range []string{" low", "  HIGH  ", "medium"} {
		setField(fEffort, v)
		if msg := scr.messageLine(); msg != "" {
			t.Errorf("live message for effort %q = %q; padding and case must not warn", v, msg)
		}
	}
	setField(fEffort, " AKIAIOSFODNN7EXAMPLE")
	if msg := scr.messageLine(); !strings.Contains(msg, "(value hidden)") {
		t.Errorf("live message for a padded credential effort = %q; want the masked warning", msg)
	}
}

// TestModelFormAddPaddedFamilyAccepted drives the padded-family case end to
// end: the field advances, the approval opens, and the stored bytes are the
// trimmed family (the same bytes buildModel already produced).
func TestModelFormAddPaddedFamilyAccepted(t *testing.T) {
	app, path := newModelFormTestApp(t, "add", "")

	keys := append(typeRunes("acme-new"), KeyTab)
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
