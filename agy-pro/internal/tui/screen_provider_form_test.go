package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/testfixtures"
)

// providerFormFixtureReg is the registry the provider-form pins run against: one
// provider with a two-model list (so an edit can prove the model list survives
// byte-identical) and a populated defaults block (so the read-only block has
// rows). The defaults block is the rung the phase-2 fanout ladder consumes; the
// form must display it and never write it.
func providerFormFixtureReg() *providers.Registry {
	return &providers.Registry{
		Providers: []providers.Provider{{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{
				{ID: "acme-fast", Family: "fast"},
				{ID: "acme-slow", Family: "slow"},
			},
		}},
		Defaults: providers.Defaults{
			N:               3,
			TimeoutSeconds:  120,
			MaxTurns:        10,
			Tools:           "read,search",
			TokenBudget:     90000,
			ExcludeFamilies: []string{"embed"},
		},
	}
}

// newProviderFormTestApp seeds reg into a temp providers.json (AGY_PRO_HOME and
// AGY_PRO_PROVIDERS both point there, so the live registry is never touched) and
// returns an app whose stack is [stub root "Providers", the form screen]. A nil
// reg leaves the file absent, which is the pre-setup add case.
func newProviderFormTestApp(t *testing.T, reg *providers.Registry, modeName, providerName string) (*App, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv(providers.EnvHome, tmp)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	if reg != nil {
		reg.Path = path
		data, err := json.MarshalIndent(reg, "", "  ")
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("seed providers.json: %v", err)
		}
	}
	core := settings.NewCore(settings.Deps{LoadReg: providers.Load})
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav})
	app.Push(NewProviderFormScreen(core, modeName, providerName))
	return app, path
}

// providerFormScreenOf returns the form screen on top of the stack.
func providerFormScreenOf(t *testing.T, app *App) *providerFormScreen {
	t.Helper()
	s, ok := app.Top().(*providerFormScreen)
	if !ok {
		t.Fatalf("top screen = %T, want *providerFormScreen", app.Top())
	}
	return s
}

// providerFormDisk reads the provider registry straight from the temp file.
func providerFormDisk(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	return data
}

// providerFormType is typeRunes under a form-local name (the helper is shared
// with the model-form pins).
func providerFormType(s string) []Key { return typeRunes(s) }

// providerFormClear returns one backspace per rune of s (clearing a seeded
// field without touching the rest of the grid).
func providerFormClear(s string) []Key {
	keys := make([]Key, 0, len([]rune(s)))
	for range []rune(s) {
		keys = append(keys, KeyBackspace)
	}
	return keys
}

// providerFormAddKeys types a full add-mode record and walks to Save: name, base
// URL, env NAME, then Tab out of the last field into the defaults block, Tab on
// to the Save row, Enter to open the M4 approval.
func providerFormAddKeys(name, baseURL, env string) []Key {
	keys := providerFormType(name)
	keys = append(keys, KeyTab)
	keys = append(keys, providerFormType(baseURL)...)
	keys = append(keys, KeyTab)
	keys = append(keys, providerFormType(env)...)
	keys = append(keys, KeyTab, KeyTab, KeyEnter)
	return keys
}

// TestProviderFormAddEndToEnd drives the full add flow: field entry, the M4
// approval diff with the exact record fields, the read-only defaults block, y
// writing through Core.ProviderAdd, the sibling provider byte-identical, the
// success message, and the pop back to the caller.
func TestProviderFormAddEndToEnd(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	siblingBefore, _ := json.Marshal(providerFormFixtureReg().Providers[0])

	out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("acme-new", "https://api.acme-new.test/v1", "ACME_NEW_API_KEY")})
	for _, want := range []string{
		"Add Provider",
		"Provider name",
		"Base URL",
		"API key env-var NAME",
		// The defaults block is displayed read-only, from the live registry.
		"providers.json defaults (read-only",
		"timeoutSeconds",
		"120",
		"excludeFamilies",
		"embed",
		// The M4 approval with the exact record fields.
		"Approve Registry Write",
		"Add provider acme-new to the registry.",
		"Models before: 0, after: 0",
		"providers.json will be written (0600, atomic) with this exact diff:",
		"+ name: acme-new",
		"+ baseURL: https://api.acme-new.test/v1",
		"+ apiKeyEnv: ACME_NEW_API_KEY",
		"y approve write · n/Esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("add approval missing %q; frames:\n%s", want, out)
		}
	}
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want *Approval (the M4 gate)", app.Modal())
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if len(reg.Providers) != 2 {
		t.Fatalf("providers on disk = %d, want 2", len(reg.Providers))
	}
	p, ok := reg.Provider("acme-new")
	if !ok {
		t.Fatalf("added provider not on disk: %+v", reg.Providers)
	}
	if p.BaseURL != "https://api.acme-new.test/v1" || p.APIKeyEnv != "ACME_NEW_API_KEY" {
		t.Fatalf("added provider = %+v", p)
	}
	// Core.ProviderAdd accepts an empty Models list: the new record carries no
	// models and the sibling keeps its own.
	if len(p.Models) != 0 {
		t.Fatalf("added provider models = %v, want none", p.Models)
	}
	sibling := reg.Providers[0]
	after, _ := json.Marshal(sibling)
	if string(after) != string(siblingBefore) {
		t.Errorf("sibling provider changed:\nbefore: %s\nafter:  %s", siblingBefore, after)
	}
	// The defaults block was never written by the form.
	if reg.Defaults.N != 3 || reg.Defaults.TimeoutSeconds != 120 || reg.Defaults.Tools != "read,search" {
		t.Errorf("defaults block moved: %+v", reg.Defaults)
	}
	if !strings.Contains(app.Message(), "Provider acme-new added to providers.json.") {
		t.Errorf("success message missing; message = %q", app.Message())
	}
	if app.Depth() != 1 || app.Top().Title() != "Providers" {
		t.Errorf("stack = depth %d top %q, want 1/Providers", app.Depth(), app.Top().Title())
	}
}

// TestProviderFormAddWithoutDefaults pins the pre-setup add: no registry file at
// all, so the form starts empty, the defaults block renders its honest empty row,
// and the first provider is created through the same approved path.
func TestProviderFormAddWithoutDefaults(t *testing.T) {
	app, path := newProviderFormTestApp(t, nil, "add", "")
	out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("first", "http://127.0.0.1:8080/v1", "")})
	if !strings.Contains(out, "no defaults block in providers.json") {
		t.Fatalf("empty defaults row missing; frames:\n%s", out)
	}
	// The apiKeyEnv field is optional (the twin treats it as optional), so the
	// diff states the empty slot instead of omitting the field.
	if !strings.Contains(out, "+ apiKeyEnv: (empty)") {
		t.Fatalf("empty env diff line missing; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	p, ok := reg.Provider("first")
	if !ok || p.BaseURL != "http://127.0.0.1:8080/v1" || p.APIKeyEnv != "" {
		t.Fatalf("added provider = %+v (ok %v)", p, ok)
	}
}

// TestProviderFormEditPrefillAndFixedName pins the edit-mode shape: the name is
// not an editable grid row (it renders as a fixed line), base URL and env NAME
// are prefilled from the live record, and typing can never reach the name.
func TestProviderFormEditPrefillAndFixedName(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "acme")
	scr := providerFormScreenOf(t, app)

	if scr.form.Len() != 2 {
		t.Fatalf("editable fields = %d, want 2 (baseURL, apiKeyEnv)", scr.form.Len())
	}
	for _, id := range scr.gridOf {
		if id == pfName {
			t.Fatalf("gridOf = %v, want no name row in edit mode", scr.gridOf)
		}
	}
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{
		"Edit Provider",
		"Provider (fixed) : acme",
		"https://acme.example/v1",
		"ACME_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit prefill missing %q; frames:\n%s", want, out)
		}
	}
	// Runes landed in the first editable field (baseURL), never in a name row.
	app.CaptureRun(&ScriptedKeys{Keys: providerFormType("X")})
	if got := scr.form.Value(0); !strings.HasSuffix(got, "X") {
		t.Fatalf("first field = %q, want the base URL with the typed X", got)
	}
	if scr.trimName() != "acme" {
		t.Fatalf("name = %q, want the fixed record name", scr.trimName())
	}
}

// TestProviderFormEditWritesOnlyEndpointFields pins the Core.ProviderEdit
// contract through the form: only baseURL and apiKeyEnv move, the model list and
// the defaults block stay byte-identical, and the M4 diff shows exactly the two
// changed fields.
func TestProviderFormEditWritesOnlyEndpointFields(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "acme")
	scr := providerFormScreenOf(t, app)

	keys := providerFormClear(scr.form.Value(0))
	keys = append(keys, providerFormType("https://api.acme.example/v2")...)
	keys = append(keys, KeyTab)
	keys = append(keys, providerFormClear(scr.form.Value(1))...)
	keys = append(keys, providerFormType("ACME_ALT_KEY")...)
	keys = append(keys, KeyTab, KeyTab, KeyEnter)

	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	// The approval body is asserted verbatim (Body is the byte-stable summary the
	// operator approves); the frame assertions below prove it also renders.
	appr, ok := app.Modal().(*Approval)
	if !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}
	summary := strings.Join(appr.Body(80), "\n")
	for _, want := range []string{
		"Edits provider acme in place: base URL and key env NAME only. Its model list and every key value are untouched.",
		"Models before: 2, after: 2",
		"  baseURL: https://acme.example/v1 -> https://api.acme.example/v2",
		"  apiKeyEnv: ACME_KEY -> ACME_ALT_KEY",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("edit approval summary missing %q; summary:\n%s", want, summary)
		}
	}
	for _, want := range []string{
		"Models before: 2, after: 2",
		"  baseURL: https://acme.example/v1 -> https://api.acme.example/v2",
		"  apiKeyEnv: ACME_KEY -> ACME_ALT_KEY",
		"y approve write · n/Esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit approval missing %q; frames:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(no field changes)") {
		t.Fatalf("edit approval rendered the no-changes marker; frames:\n%s", out)
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(reg.Providers))
	}
	p := reg.Providers[0]
	if p.Name != "acme" || p.BaseURL != "https://api.acme.example/v2" || p.APIKeyEnv != "ACME_ALT_KEY" {
		t.Fatalf("edited provider = %+v", p)
	}
	if len(p.Models) != 2 || p.Models[0].ID != "acme-fast" || p.Models[1].ID != "acme-slow" {
		t.Fatalf("model list = %+v, want both models untouched", p.Models)
	}
	if reg.Defaults.N != 3 || reg.Defaults.TokenBudget != 90000 {
		t.Errorf("defaults block moved: %+v", reg.Defaults)
	}
	if !strings.Contains(app.Message(), "Provider acme updated in providers.json.") {
		t.Errorf("success message missing; message = %q", app.Message())
	}
	if app.Depth() != 1 {
		t.Errorf("depth = %d, want 1 (popped back to the providers list)", app.Depth())
	}
}

// TestProviderFormEditUnchangedSignsNoChanges pins the sibling's no-change shape:
// an untouched edit still opens the approval and renders the no-changes marker,
// and the approved write is a no-op (Core.ProviderEdit writes nothing). FIX-E: the
// success sentence must not claim a write on that path, so it is the dedicated
// unchanged sentence, and the write-bearing sentence of the changed path
// (TestProviderFormEditWritesOnlyEndpointFields) must not appear.
func TestProviderFormEditUnchangedSignsNoChanges(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "acme")
	before := providerFormDisk(t, path)

	// Cursor sits on the first editable field: Tab to the env field, Tab out of
	// the grid, Tab off the read-only defaults block, Enter for the approval.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyTab, KeyTab, KeyEnter}})
	if !strings.Contains(out, "(no field changes)") {
		t.Fatalf("no-changes marker missing; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatalf("an unchanged edit rewrote providers.json:\nbefore: %s\nafter:  %s", before, got)
	}
	msg := app.Message()
	if want := "Provider acme: providers.json already held these endpoint values, so nothing was written."; !strings.Contains(msg, want) {
		t.Fatalf("no-op message = %q, want %q", msg, want)
	}
	if strings.Contains(msg, "updated in providers.json") {
		t.Fatalf("the no-op message claims a write that did not happen: %q", msg)
	}
}

// TestProviderFormValidationRefusals pins every validation refusal the twin
// enforces: each one names the field/reason on the message line, opens no
// approval, and leaves providers.json byte-identical.
func TestProviderFormValidationRefusals(t *testing.T) {
	// wants are substrings that fit the 80-column frame (the drawn message line
	// is truncated there); full is asserted against the untruncated message line
	// the form composes.
	cases := []struct {
		name  string
		keys  func(scr *providerFormScreen) []Key
		wants []string
		full  string
	}{
		{
			name: "empty name",
			keys: func(*providerFormScreen) []Key { return []Key{KeyEnter} },
			wants: []string{
				"Provider name is required.",
			},
			full: "Provider name is required.",
		},
		{
			name: "name charset",
			keys: func(*providerFormScreen) []Key {
				return append(providerFormType("bad name"), KeyEnter)
			},
			wants: []string{
				"Provider name must be 1-64 characters",
			},
			full: "no spaces or shell metacharacters",
		},
		{
			name: "duplicate name",
			keys: func(*providerFormScreen) []Key {
				return append(providerFormType("acme"), KeyEnter)
			},
			wants: []string{
				"A provider named acme already exists.",
			},
			full: "would replace the entry and drop its models",
		},
		{
			name: "empty base url",
			keys: func(*providerFormScreen) []Key {
				keys := append(providerFormType("acme-new"), KeyTab)
				return append(keys, KeyEnter)
			},
			wants: []string{
				"Base URL is required.",
			},
			full: "Base URL is required.",
		},
		{
			name: "non-loopback http",
			keys: func(*providerFormScreen) []Key {
				keys := append(providerFormType("acme-new"), KeyTab)
				keys = append(keys, providerFormType("http://api.acme.example/v1")...)
				return append(keys, KeyEnter)
			},
			// The exact wording lives in providers.validation.base_url (a locale
			// wave rewords it to match the enforced rule, FIX-H), so the frame
			// assertion keeps only the stable prefix and the message-line assertion
			// the clause both wordings carry.
			wants: []string{
				"Base URL must",
			},
			full: "http:// is accepted only for loopback hosts",
		},
		{
			name: "unparseable base url",
			keys: func(*providerFormScreen) []Key {
				keys := append(providerFormType("acme-new"), KeyTab)
				keys = append(keys, providerFormType("api.acme.example/v1")...)
				return append(keys, KeyEnter)
			},
			wants: []string{
				"Base URL must",
			},
			full: "http:// is accepted only for loopback hosts",
		},
		{
			name: "env name shape",
			keys: func(*providerFormScreen) []Key {
				keys := append(providerFormType("acme-new"), KeyTab)
				keys = append(keys, providerFormType("https://api.acme.example/v1")...)
				keys = append(keys, KeyTab)
				keys = append(keys, providerFormType("NOT A NAME")...)
				return append(keys, KeyEnter)
			},
			wants: []string{
				"API key env-var NAME must name an environment variable",
			},
			full: "or use a known provider key variable such as GEMINI_API_KEY",
		},
		{
			name: "pasted key value",
			keys: func(*providerFormScreen) []Key {
				keys := append(providerFormType("acme-new"), KeyTab)
				keys = append(keys, providerFormType("https://api.acme.example/v1")...)
				keys = append(keys, KeyTab)
				keys = append(keys, providerFormType(providerFormPastedKeyValue)...)
				return append(keys, KeyEnter)
			},
			wants: []string{
				"The API key env field holds an environment-variable NAME",
			},
			full: "looks like key material and was refused (value hidden)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr := providerFormScreenOf(t, app)
			before := providerFormDisk(t, path)

			out := app.CaptureRun(&ScriptedKeys{Keys: tc.keys(scr)})
			for _, want := range tc.wants {
				if !strings.Contains(out, want) {
					t.Fatalf("refusal missing %q; frames:\n%s", want, out)
				}
			}
			if msg := scr.messageLine(); !strings.Contains(msg, tc.full) {
				t.Fatalf("message line = %q, want %q", msg, tc.full)
			}
			// The dirty-form guard is a modal too; what must NOT be live is the
			// M4 approval.
			if _, isApproval := app.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (a refused form opens none)", app.Modal())
			}
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatal("providers.json changed after a validation refusal")
			}
			if strings.Contains(out, "y approve write") {
				t.Fatalf("approval modal opened for an invalid form; frames:\n%s", out)
			}
		})
	}
}

// TestProviderFormLoopbackHTTPAccepted pins the positive half of the transport
// rule: the same http:// scheme is accepted for a loopback host (the twin's
// isLoopbackHost rule), so the form does not over-tighten the twin.
func TestProviderFormLoopbackHTTPAccepted(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("local", "http://localhost:11434/v1", "LOCAL_KEY")})
	if !strings.Contains(out, "y approve write") {
		t.Fatalf("loopback http:// endpoint was refused; frames:\n%s", out)
	}
	if !strings.Contains(out, "+ baseURL: http://localhost:11434/v1") {
		t.Fatalf("approval diff missing the loopback base URL; frames:\n%s", out)
	}
}

// TestProviderFormEditUnknownProvider pins the honest not-found state: the
// missing record renders its error line, the form still renders, and Save opens
// no approval and writes nothing.
func TestProviderFormEditUnknownProvider(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "ghost")
	before := providerFormDisk(t, path)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyTab, KeyTab, KeyEnter}})
	if !strings.Contains(out, "Provider ghost is not in providers.json") {
		t.Fatalf("not-found line missing; frames:\n%s", out)
	}
	if _, isApproval := app.Modal().(*Approval); isApproval {
		t.Fatalf("modal = %T, want no approval (a not-found form opens none)", app.Modal())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed from a not-found form")
	}
}

// TestProviderFormEditAmbiguousTarget pins the wrong-record guard: two records
// that match under case folding are not prefilled by a guess (the Core verb
// refuses them), and no save is possible.
func TestProviderFormEditAmbiguousTarget(t *testing.T) {
	reg := providerFormFixtureReg()
	reg.Providers = append(reg.Providers, providers.Provider{Name: "ACME", BaseURL: "https://other.example/v1"})
	app, path := newProviderFormTestApp(t, reg, "edit", "acme")
	before := providerFormDisk(t, path)

	// The exact-case match wins outright, so "acme" still resolves.
	scr := providerFormScreenOf(t, app)
	if !scr.origFound || scr.orig.Name != "acme" {
		t.Fatalf("exact-case target = %+v (found %v)", scr.orig, scr.origFound)
	}

	app2, path2 := newProviderFormTestApp(t, reg, "edit", "AcMe")
	before2 := providerFormDisk(t, path2)
	out := app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyTab, KeyTab, KeyEnter}})
	if !strings.Contains(out, "More than one provider matches AcMe") {
		t.Fatalf("ambiguity line missing; frames:\n%s", out)
	}
	if _, isApproval := app2.Modal().(*Approval); isApproval {
		t.Fatalf("modal = %T, want no approval (an ambiguous target opens none)", app2.Modal())
	}
	if got := providerFormDisk(t, path2); string(got) != string(before2) {
		t.Fatal("providers.json changed from an ambiguous-target form")
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("the first registry changed while rendering the second form")
	}
}

// TestProviderFormAddApprovalDecline pins the decline path: n writes nothing,
// returns to the form with the values preserved, and reports that nothing was
// written.
func TestProviderFormAddApprovalDecline(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	before := providerFormDisk(t, path)

	keys := providerFormAddKeys("acme-new", "https://api.acme-new.test/v1", "ACME_NEW_API_KEY")
	keys = append(keys, KeyNo)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(out, "Cancelled. Nothing was written.") {
		t.Fatalf("decline message missing; frames:\n%s", out)
	}
	if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
		t.Fatalf("app message = %q, want the decline notice", app.Message())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed after a declined approval")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (back on the form)", app.Depth())
	}
	scr := providerFormScreenOf(t, app)
	if scr.trimName() != "acme-new" || scr.trimAPIKeyEnv() != "ACME_NEW_API_KEY" {
		t.Fatalf("declined form lost its values: name %q env %q", scr.trimName(), scr.trimAPIKeyEnv())
	}
}

// TestProviderFormApprovalSwallowsNonAnswerKeys pins that the M4 answer
// semantics ride the shared Approval component: a non-answer key (Enter, a
// digit) leaves the modal live and writes nothing, and only y writes.
func TestProviderFormApprovalSwallowsNonAnswerKeys(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	before := providerFormDisk(t, path)

	app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("acme-new", "https://api.acme-new.test/v1", "ACME_NEW_API_KEY")})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter, Digit(2), KeySpace}})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want the approval still live after non-answer keys", app.Modal())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed on a non-answer key")
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if got := providerFormDisk(t, path); string(got) == string(before) {
		t.Fatal("providers.json unchanged after y (the approved write did not run)")
	}
}

// TestProviderFormSecretValueNeverRenders pins the masking discipline: a key
// VALUE pasted into the apiKeyEnv field is refused by name, and the value never
// appears raw in a frame, in the message line, or in a diff line.
func TestProviderFormSecretValueNeverRenders(t *testing.T) {
	const secret = providerFormSecretValue
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	before := providerFormDisk(t, path)

	keys := append(providerFormType("acme-new"), KeyTab)
	keys = append(keys, providerFormType("https://api.acme.example/v1")...)
	keys = append(keys, KeyTab)
	keys = append(keys, providerFormType(secret)...)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if strings.Contains(out, secret) {
		t.Fatalf("a frame rendered the pasted key value raw:\n%s", out)
	}
	if !strings.Contains(out, "sk-li****") {
		t.Fatalf("the apiKeyEnv grid row was not masked; frames:\n%s", out)
	}
	// The field itself still holds the raw value so the user can edit it; only
	// the frame is masked. The message line refuses it without echoing.
	scr := providerFormScreenOf(t, app)
	if scr.trimAPIKeyEnv() != secret {
		t.Fatalf("form value = %q, want the typed value (editing stays raw)", scr.trimAPIKeyEnv())
	}
	if strings.Contains(scr.messageLine(), secret) {
		t.Fatalf("message line echoed the key value: %q", scr.messageLine())
	}

	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
	if !strings.Contains(out, "The API key env field holds an environment-variable NAME") {
		t.Fatalf("secret refusal missing; frames:\n%s", out)
	}
	if msg := scr.messageLine(); !strings.Contains(msg, "looks like key material and was refused (value hidden)") {
		t.Fatalf("secret refusal message = %q", msg)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("a frame rendered the pasted key value raw:\n%s", out)
	}
	if _, isApproval := app.Modal().(*Approval); isApproval {
		t.Fatalf("modal = %T, want no approval (the secret refusal blocks it)", app.Modal())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed after the secret refusal")
	}
}

// TestProviderFormEditMasksRegistryKeyValue pins the display rule for a
// credential-shaped value already on disk: the prefilled grid row and every diff
// line mask it, and an untouched env signs no diff line at all.
func TestProviderFormEditMasksRegistryKeyValue(t *testing.T) {
	const planted = testfixtures.AKIAExample
	reg := providerFormFixtureReg()
	reg.Providers[0].APIKeyEnv = planted
	app, path := newProviderFormTestApp(t, reg, "edit", "acme")
	before := providerFormDisk(t, path)

	scr := providerFormScreenOf(t, app)
	keys := providerFormClear(scr.form.Value(0))
	keys = append(keys, providerFormType("https://api.acme.example/v2")...)
	keys = append(keys, KeyTab, KeyTab, KeyTab, KeyEnter)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if strings.Contains(out, planted) {
		t.Fatalf("a frame rendered the on-disk key value raw:\n%s", out)
	}
	if !strings.Contains(out, "AKIAI****") {
		t.Fatalf("the prefilled env row was not masked; frames:\n%s", out)
	}
	if strings.Contains(out, "apiKeyEnv:") {
		t.Fatalf("an untouched env signed a diff line; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed after a declined edit")
	}
}

// TestProviderFormEditStaleBaseRefuses pins the front-end half of the M4
// contract (Core.ProviderEdit documents it): an external change to the same
// record while the approval is open is refused instead of silently reverted.
func TestProviderFormEditStaleBaseRefuses(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "acme")
	scr := providerFormScreenOf(t, app)

	keys := providerFormClear(scr.form.Value(0))
	keys = append(keys, providerFormType("https://api.acme.example/v2")...)
	keys = append(keys, KeyTab, KeyTab, KeyTab, KeyEnter)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}

	// Another writer moves the same record while the approval is open.
	external := providerFormFixtureReg()
	external.Providers[0].BaseURL = "https://external.example/v9"
	external.Path = path
	data, err := json.MarshalIndent(external, "", "  ")
	if err != nil {
		t.Fatalf("marshal external registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write external registry: %v", err)
	}
	stale := providerFormDisk(t, path)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if !strings.Contains(out, "The provider changed on disk since this form opened.") {
		t.Fatalf("stale-base refusal missing; frames:\n%s", out)
	}
	if got := providerFormDisk(t, path); string(got) != string(stale) {
		t.Fatal("providers.json was overwritten from a stale approval")
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (the form stays open)", app.Depth())
	}
}

// TestProviderFormAddDuplicateAppearsWhileApprovalOpen pins the add-mode twin of
// the stale gate: a provider created behind the form's back turns the approved
// ProviderAdd into a replace (UpsertProvider semantics), so the commit refuses.
func TestProviderFormAddDuplicateAppearsWhileApprovalOpen(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("racing", "https://api.racing.example/v1", "RACING_KEY")})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}

	external := providerFormFixtureReg()
	external.Providers = append(external.Providers, providers.Provider{
		Name: "racing", BaseURL: "https://external.example/v1", APIKeyEnv: "OTHER_KEY",
		Models: []providers.Model{{ID: "keep-me"}},
	})
	external.Path = path
	data, err := json.MarshalIndent(external, "", "  ")
	if err != nil {
		t.Fatalf("marshal external registry: %v", err)
	}
	if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write external registry: %v", err)
	}
	externalBytes := providerFormDisk(t, path)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if !strings.Contains(out, "A provider named racing already exists.") {
		t.Fatalf("duplicate-at-commit refusal missing; frames:\n%s", out)
	}
	if got := providerFormDisk(t, path); string(got) != string(externalBytes) {
		t.Fatal("the approved add replaced the provider that appeared while the approval was open")
	}
}

// TestProviderFormDiscardGate pins the dirty-form Esc gate: Esc on a dirty form
// opens the discard confirm instead of popping; y drops the edits and pops with
// the cancelled message, and n returns to the form with its values.
func TestProviderFormDiscardGate(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	before := providerFormDisk(t, path)

	app.CaptureRun(&ScriptedKeys{Keys: providerFormType("half-typed")})
	if _, ok := app.Modal().(*providerFormGuard); !ok {
		t.Fatalf("modal = %T, want *providerFormGuard (dirty form)", app.Modal())
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	if _, ok := app.Modal().(*Confirm); !ok {
		t.Fatalf("modal = %T, want *Confirm (the discard gate)", app.Modal())
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if !strings.Contains(out, "Discard unsaved edits?") {
		t.Fatalf("discard confirm missing; frames:\n%s", out)
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (n keeps the form)", app.Depth())
	}
	if got := providerFormScreenOf(t, app).trimName(); got != "half-typed" {
		t.Fatalf("name = %q, want the edits preserved after n", got)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc, KeyYes}})
	if app.Depth() != 1 {
		t.Fatalf("depth = %d, want 1 (y drops the edits and pops)", app.Depth())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed after discarding a dirty form")
	}
	if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
		t.Fatalf("discard message = %q", app.Message())
	}
}

// TestProviderFormDefaultsWindowShrinksWithFrame pins the frame-safety rule: the
// read-only defaults window is sized through pageSizeFor against the real chrome,
// so a short frame pages the block instead of letting the composer cut the Save
// row off the tail.
func TestProviderFormDefaultsWindowShrinksWithFrame(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	// 80x24: the whole block fits.
	frame := app.Frame()
	if got := scr.defList.PageSize(); got < scr.defList.Len() {
		t.Fatalf("defaults window = %d rows at 80x24, want the whole block (%d)", got, scr.defList.Len())
	}
	// 80x12: the window shrinks, and the Save row still renders.
	app.Resize(80, 12)
	frame = app.Frame()
	if got := scr.defList.PageSize(); got != 1 {
		t.Fatalf("defaults window = %d rows at 80x12, want 1", got)
	}
	out := strings.Join(frame.Lines, "\n")
	if !strings.Contains(out, "Save") {
		t.Fatalf("Save row was cut off a short frame; frame:\n%s", out)
	}
	if !strings.Contains(out, "providers.json defaults") {
		t.Fatalf("defaults block title was cut off a short frame; frame:\n%s", out)
	}
}

// TestProviderFormFrameFitsEightyColumns pins the width rule: with a long typed
// value and a live message, no composed line exceeds the 80-column frame.
func TestProviderFormFrameFitsEightyColumns(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	app.CaptureRun(&ScriptedKeys{Keys: providerFormType("a-very-long-provider-name-that-keeps-going-and-going")})
	app.SetMessage("a live message that must stay inside the frame and must not push any line past the right edge")

	frame := app.Frame()
	for i, line := range frame.Lines {
		if cw := cellwidth.String(line); cw > app.Width() {
			t.Fatalf("line %d is %d cells wide, frame is %d: %q", i, cw, app.Width(), line)
		}
	}
	out := strings.Join(frame.Lines, "\n")
	if !strings.Contains(out, "providers.json defaults") {
		t.Fatalf("defaults block missing at 80 columns; frame:\n%s", out)
	}
	if !strings.Contains(out, "Enter opens the approval diff") {
		t.Fatalf("hint missing at 80 columns; frame:\n%s", out)
	}
}

// TestProviderFormDefaultsBlockReadOnly pins that the defaults zone cannot write:
// digits and paging keys only move its window, and no key opens an approval.
func TestProviderFormDefaultsBlockReadOnly(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	before := providerFormDisk(t, path)

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyTab, KeyTab}}) // grid -> defaults
	scr := providerFormScreenOf(t, app)
	if scr.zone != providerFormZoneDefaults {
		t.Fatalf("zone = %d, want the defaults zone", scr.zone)
	}
	offset := scr.defList.Cursor()
	app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(3), KeyPgDn, KeyEnd}})
	if app.Modal() != nil {
		t.Fatalf("modal = %T, want none (the block is read-only)", app.Modal())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("providers.json changed from the read-only defaults block")
	}
	if scr.defList.Cursor() == offset && scr.defList.Len() > 1 {
		t.Fatalf("the read-only block did not move its window on digit/paging keys")
	}
}

// TestProviderFormPlainTierEmitsNoEscapes pins the plain-tier rule: the composed
// frame of this screen paints as bare text with zero ESC bytes.
func TestProviderFormPlainTierEmitsNoEscapes(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "acme")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyTab}})
	app.SetMessage("live message")

	var buf bytes.Buffer
	if err := NewPainter(&buf, plainTestCap()).Paint(app.Frame(), nil); err != nil {
		t.Fatalf("paint: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("plain tier emitted escape bytes:\n%q", buf.String())
	}
	if !strings.Contains(buf.String(), "Edit Provider") {
		t.Fatalf("plain frame missing the screen title:\n%s", buf.String())
	}
}

// providerFormSecretValue is a synthetic key VALUE. It is shaped like live key
// material (a listed credential head plus an opaque alnum run) so the shared
// classifier reads it as a secret; it is not a real credential. The head is
// built by concatenation so the tracked source carries no contiguous
// provider-key spelling; the runtime value is byte-identical to the literal it
// replaced.
const providerFormSecretValue = "sk-" + "live-" + "0123456789abcdefXYZ"

// providerFormPastedKeyValue is the shorter synthetic key VALUE pasted into the
// apiKeyEnv field by the refusal pins. Same concatenation discipline.
const providerFormPastedKeyValue = "sk-" + "live-" + "0123456789abcdef"

// TestProviderFormSecretShapedFieldsRefused is the FIX-A pin: a key VALUE pasted
// into the name or the base URL field is refused at validation time, before the
// M4 gate opens, with a named value-free message, and nothing is written. Each
// subtest asserts the raw value appears in NO frame, in NO message, and in NO
// on-disk byte: refusing to save is worthless if the refusal prints the key.
func TestProviderFormSecretShapedFieldsRefused(t *testing.T) {
	cases := []struct {
		name    string
		keys    []Key
		field   string
		raw     string
		masked  string
		message string
	}{
		{
			// The charset validator admits every bare alnum key ID, so without the
			// secrets guard this value would be a legal provider name and would be
			// persisted verbatim while the grid rendered "sk-li****".
			name:    "name field",
			keys:    append(providerFormType(providerFormSecretValue), KeyEnter),
			field:   "name",
			raw:     providerFormSecretValue,
			masked:  "sk-li****",
			message: "looks like secret/key material and cannot be saved (value hidden)",
		},
		{
			// The mission's own shape: a key inside a URL. The transport rule accepts
			// it (valid scheme, valid host), and the grid plus the M4 diff mask it,
			// so only the secrets guard stands between the paste and providers.json.
			// The base-URL display authority keeps the location readable and redacts
			// the credential run, so the expected row is its own output.
			name: "base url field",
			keys: func() []Key {
				keys := append(providerFormType("acme-new"), KeyTab)
				keys = append(keys, providerFormType("https://gw.example/v1/"+providerFormSecretValue)...)
				return append(keys, KeyEnter)
			}(),
			field:   "baseURL",
			raw:     providerFormSecretValue,
			masked:  providers.MaskBaseURL("https://gw.example/v1/" + providerFormSecretValue),
			message: "looks like secret/key material and cannot be saved (value hidden)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr := providerFormScreenOf(t, app)
			before := providerFormDisk(t, path)

			out := app.CaptureRun(&ScriptedKeys{Keys: tc.keys})

			// The value is editable (the user may repair it) and masked on screen.
			if !strings.Contains(out, tc.masked) {
				t.Fatalf("the %s row was not masked as %q; frames:\n%s", tc.field, tc.masked, out)
			}
			if strings.Contains(out, tc.raw) {
				t.Fatalf("a frame rendered the pasted key value raw:\n%s", out)
			}
			// The refusal is named, and it names nothing else.
			msg := scr.messageLine()
			if !strings.Contains(msg, tc.message) {
				t.Fatalf("message line = %q, want the value-free secret refusal", msg)
			}
			if strings.Contains(msg, tc.raw) || strings.Contains(msg, "sk-li") {
				t.Fatalf("the refusal echoes the refused value: %q", msg)
			}
			// Validation-time refusal: the M4 gate never opened.
			if _, isApproval := app.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (the secret refusal precedes the gate)", app.Modal())
			}
			if strings.Contains(out, "y approve write") {
				t.Fatalf("the approval modal opened over a credential-shaped field; frames:\n%s", out)
			}
			// No write, and no raw value anywhere in the on-disk bytes.
			got := providerFormDisk(t, path)
			if string(got) != string(before) {
				t.Fatalf("providers.json changed after the secret refusal:\nbefore: %s\nafter:  %s", before, got)
			}
			if strings.Contains(string(got), tc.raw) {
				t.Fatalf("the refused key value reached providers.json: %s", got)
			}
			if app.Depth() != 2 {
				t.Fatalf("depth = %d, want 2 (the form stays open on the refusal)", app.Depth())
			}
		})
	}
}

// TestProviderFormBaseURLRefusalTextMatchesRule is the FIX-H pin. The enforced
// rule is the registry's load-time one, unchanged: https:// is accepted (with or
// without a host, matching the CLI twin), http:// only for a loopback host, and
// every other scheme is refused. The refusal TEXT used to claim a host was
// required, which the rule never enforced. This pins the corrected fallback const
// exactly (the locale wave mirrors it) and re-asserts the behaviour, so a reword
// can never tighten the rule.
func TestProviderFormBaseURLRefusalTextMatchesRule(t *testing.T) {
	const want = "Base URL must use https://; http:// is accepted only for loopback hosts (localhost, 127.0.0.1, ::1)."
	if missingProviderFormBaseURLInvalid != want {
		t.Fatalf("fallback const = %q, want %q", missingProviderFormBaseURLInvalid, want)
	}
	if strings.Contains(missingProviderFormBaseURLInvalid, "with a host") {
		t.Fatalf("the refusal text still overclaims a host requirement: %q", missingProviderFormBaseURLInvalid)
	}

	// A hostless https:// stays accepted: the twin and the parity pin require it.
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("hostless", "https://", "HOSTLESS_KEY")})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("hostless https:// was refused (the rule got tighter); frames:\n%s", out)
	}

	// A non-https scheme is still refused, and the message is the catalog/const
	// entry for the base-URL rule (the wiring this pin exists to keep).
	app, _ = newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	out = app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("bad-scheme", "ftp://acme.example/v1", "BAD_KEY")})
	if _, isApproval := app.Modal().(*Approval); isApproval {
		t.Fatalf("a non-https base URL opened an approval; frames:\n%s", out)
	}
	scr := providerFormScreenOf(t, app)
	if got, wantMsg := scr.messageLine(), missingOr(scr.b, "providers.validation.base_url", missingProviderFormBaseURLInvalid); got != wantMsg {
		t.Fatalf("refusal message = %q, want the base_url rule text %q", got, wantMsg)
	}
}

// providerFormDupRegistry is a registry Registry.Validate permits: its duplicate
// check is exact-case, so "acme" and "ACME" coexist and a case-insensitive
// resolver sees two matches for one name.
func providerFormDupRegistry(firstBase, secondBase string) *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{Name: "acme", BaseURL: firstBase, APIKeyEnv: "FIRST_KEY", Models: []providers.Model{{ID: "m-first"}}},
		{Name: "ACME", BaseURL: secondBase, APIKeyEnv: "SECOND_KEY", Models: []providers.Model{{ID: "m-second"}}},
	}}
}

// TestProviderFormCaseDuplicateEditTargetAgreesWithVerb is the FIX-B pin: the
// stale-base gate resolves its target with the SAME rule the verb uses
// (settings.ResolveProviderEditTarget), so on a case-duplicate registry the guard
// proceeds exactly when the verb proceeds and refuses exactly when it refuses.
// Before the fix the guard took the first case-insensitive match.
func TestProviderFormCaseDuplicateEditTargetAgreesWithVerb(t *testing.T) {
	t.Run("guard proceeds where the verb proceeds", func(t *testing.T) {
		// The form edits the exact-case target "ACME" (index 1). A first-match
		// resolver compares against "acme" (index 0), whose endpoint differs, and
		// falsely refuses a legal edit as stale.
		app, path := newProviderFormTestApp(t, providerFormDupRegistry("https://first.example/v1", "https://second.example/v1"), "edit", "ACME")
		scr := providerFormScreenOf(t, app)
		keys := providerFormClear(scr.form.Value(0))
		keys = append(keys, providerFormType("https://second-moved.example/v1")...)
		keys = append(keys, KeyTab, KeyTab, KeyTab, KeyEnter)
		out := app.CaptureRun(&ScriptedKeys{Keys: keys})
		if _, ok := app.Modal().(*Approval); !ok {
			t.Fatalf("modal = %T, want *Approval; frames:\n%s", app.Modal(), out)
		}

		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
		if strings.Contains(app.Message(), "changed on disk") {
			t.Fatalf("the guard false-refused a legal edit of the exact-case target: %q", app.Message())
		}
		reg, err := providers.LoadFrom(path)
		if err != nil {
			t.Fatalf("reload registry: %v", err)
		}
		if got := reg.Providers[0]; got.Name != "acme" || got.BaseURL != "https://first.example/v1" {
			t.Fatalf("the case duplicate was edited instead: %+v", got)
		}
		if got := reg.Providers[1]; got.Name != "ACME" || got.BaseURL != "https://second-moved.example/v1" {
			t.Fatalf("exact-case target not edited: %+v", got)
		}
	})

	t.Run("guard refuses a real drift of the approved target", func(t *testing.T) {
		// Both duplicates carry the snapshot's endpoint fields, base URL AND key
		// env NAME (the bypass precondition: a case-insensitive first match agrees
		// with the snapshot on every field the guard compares), while the real
		// target moves on disk. A first-match resolver then sees "no drift" and the
		// approved write silently reverts the concurrent change. The shared
		// resolver reads the exact-case record and refuses.
		const mirror = "https://mirror.example/v1"
		const mirrorEnv = "MIRROR_KEY"
		dup := func(secondBase string) *providers.Registry {
			return &providers.Registry{Providers: []providers.Provider{
				{Name: "acme", BaseURL: mirror, APIKeyEnv: mirrorEnv, Models: []providers.Model{{ID: "m-first"}}},
				{Name: "ACME", BaseURL: secondBase, APIKeyEnv: mirrorEnv, Models: []providers.Model{{ID: "m-second"}}},
			}}
		}
		app, path := newProviderFormTestApp(t, dup(mirror), "edit", "ACME")
		scr := providerFormScreenOf(t, app)
		keys := providerFormClear(scr.form.Value(0))
		keys = append(keys, providerFormType("https://edited.example/v2")...)
		keys = append(keys, KeyTab, KeyTab, KeyTab, KeyEnter)
		app.CaptureRun(&ScriptedKeys{Keys: keys})
		if _, ok := app.Modal().(*Approval); !ok {
			t.Fatalf("modal = %T, want *Approval (cursor=%d)", app.Modal(), scr.form.Cursor())
		}

		external := dup("https://drifted.example/v9")
		external.Path = path
		data, err := json.MarshalIndent(external, "", "  ")
		if err != nil {
			t.Fatalf("marshal external registry: %v", err)
		}
		if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("write external registry: %v", err)
		}
		stale := providerFormDisk(t, path)

		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
		if !strings.Contains(out, "The provider changed on disk since this form opened.") {
			t.Fatalf("stale-base refusal missing; frames:\n%s", out)
		}
		if got := providerFormDisk(t, path); string(got) != string(stale) {
			t.Fatal("the approved edit reverted the concurrent change to the real target")
		}
	})

	t.Run("guard fails closed where the verb refuses an ambiguity", func(t *testing.T) {
		// The form opened on the single record "Acme"; on disk it is now the
		// case-duplicate pair, so no exact match exists and the name is ambiguous.
		// The verb answers ErrProviderEditAmbiguous, so the guard must refuse.
		single := &providers.Registry{Providers: []providers.Provider{
			{Name: "Acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY", Models: []providers.Model{{ID: "m1"}}},
		}}
		app, path := newProviderFormTestApp(t, single, "edit", "Acme")
		scr := providerFormScreenOf(t, app)
		keys := providerFormClear(scr.form.Value(0))
		keys = append(keys, providerFormType("https://edited.example/v2")...)
		keys = append(keys, KeyTab, KeyTab, KeyTab, KeyEnter)
		app.CaptureRun(&ScriptedKeys{Keys: keys})
		if _, ok := app.Modal().(*Approval); !ok {
			t.Fatalf("modal = %T, want *Approval", app.Modal())
		}

		external := providerFormDupRegistry("https://a.example/v1", "https://b.example/v1")
		external.Path = path
		data, err := json.MarshalIndent(external, "", "  ")
		if err != nil {
			t.Fatalf("marshal external registry: %v", err)
		}
		if err := providers.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("write external registry: %v", err)
		}
		ambiguous := providerFormDisk(t, path)

		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
		if !strings.Contains(out, "The provider changed on disk since this form opened.") {
			t.Fatalf("the guard did not fail closed on an ambiguous target; frames:\n%s", out)
		}
		if got := providerFormDisk(t, path); string(got) != string(ambiguous) {
			t.Fatal("providers.json changed although the target no longer resolved")
		}
		// The verb's verdict for the same operand, from the same seams: the guard
		// refusal above is the verb's own refusal class, not a stricter one.
		if err := scr.core.ProviderEdit("Acme", "https://edited.example/v2", ""); !errors.Is(err, settings.ErrProviderEditAmbiguous) {
			t.Fatalf("verb verdict = %v, want ErrProviderEditAmbiguous (guard and verb must agree)", err)
		}
	})
}

// TestProviderFormAddFailsClosedOnDiskCheckError is the FIX-F pin: when the
// duplicate-name re-read fails, the approved add is refused with a named message
// and nothing is written. Reporting "no duplicate" on a load error widened the
// window in which an approved add silently REPLACES the provider that appeared on
// disk (UpsertProvider semantics).
func TestProviderFormAddFailsClosedOnDiskCheckError(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("racing", "https://api.racing.example/v1", "RACING_KEY")})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}

	// The file becomes unreadable while the approval is open, so the duplicate
	// check cannot run.
	if err := providers.WriteFileAtomic(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("corrupt providers.json: %v", err)
	}
	broken := providerFormDisk(t, path)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if !strings.Contains(out, "providers.json could not be re-read") {
		t.Fatalf("fail-closed refusal missing; frames:\n%s", out)
	}
	// FIX-7A: this sentence is shown by BOTH commit gates (this duplicate-name
	// re-check and the edit-mode stale comparison), so the English fallback was
	// reworded to the door-neutral "the on-disk check". The rendered sentence is
	// catalog-owned and the twelve locales are updated by the translation wave,
	// so the frame assertion pins the stable class prefix and the fallback const
	// is pinned separately below.
	if msg := app.Message(); !strings.Contains(msg, "providers.json could not be re-read") {
		t.Fatalf("refusal message = %q, want the named fail-closed sentence", msg)
	}
	if !strings.Contains(missingProviderFormDiskCheckFailed, "so the on-disk check did not run") ||
		strings.Contains(missingProviderFormDiskCheckFailed, "duplicate-name check") {
		t.Fatalf("the English fallback lost its door-neutral wording: %q", missingProviderFormDiskCheckFailed)
	}
	if got := app.Message(); strings.Contains(got, "Success") {
		t.Fatalf("the failed duplicate check reported success: %q", got)
	}
	if got := providerFormDisk(t, path); string(got) != string(broken) {
		t.Fatalf("providers.json changed although the duplicate check could not run:\nbefore: %s\nafter:  %s", broken, got)
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (the form stays open on the refusal)", app.Depth())
	}
}

// TestProviderFormEditFailsClosedOnDiskCheckError is the FIX-I pin: when the
// edit-mode re-read fails, the stale comparison cannot run, so the approved edit
// is refused with the add-mode gate's named message (FIX-F) and nothing is
// written. The pre-fix answer was (false, nil), which skipped the comparison
// entirely, so the approved write landed with no stale check at all and could
// revert a concurrent change inside every transient I/O or corrupt-file window.
func TestProviderFormEditFailsClosedOnDiskCheckError(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", "acme")
	scr := providerFormScreenOf(t, app)

	keys := providerFormClear(scr.form.Value(0))
	keys = append(keys, providerFormType("https://api.acme.example/v2")...)
	keys = append(keys, KeyTab, KeyTab, KeyTab, KeyEnter)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}

	// The registry becomes unreadable while the approval is open, so the gate
	// cannot compare the approved values against the record.
	if err := providers.WriteFileAtomic(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("corrupt providers.json: %v", err)
	}
	broken := providerFormDisk(t, path)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	if !strings.Contains(out, "providers.json could not be re-read") {
		t.Fatalf("fail-closed refusal missing; frames:\n%s", out)
	}
	msg := app.Message()
	// FIX-7A: the shared sentence now says "the on-disk check" (both gates show
	// it), and the rendered text is catalog-owned until the translation wave
	// lands, so the assertion pins the stable class prefix plus the fallback
	// const rather than the add-mode wording the const used to bake.
	if !strings.Contains(msg, "providers.json could not be re-read") {
		t.Fatalf("refusal message = %q, want the named fail-closed sentence", msg)
	}
	if !strings.Contains(missingProviderFormDiskCheckFailed, "so the on-disk check did not run") ||
		strings.Contains(missingProviderFormDiskCheckFailed, "duplicate-name check") {
		t.Fatalf("the English fallback lost its door-neutral wording: %q", missingProviderFormDiskCheckFailed)
	}
	if strings.Contains(msg, "Success") {
		t.Fatalf("the failed stale comparison reported success: %q", msg)
	}
	// The refusal names the load-error class, not drift: the record did not move,
	// it became unreadable.
	if strings.Contains(msg, "changed on disk since this form opened") {
		t.Fatalf("refusal message = %q, want the load-error class rather than the drift class", msg)
	}
	// The verb never ran. On the same unreadable state it reports its own load
	// error text, so a message line that does not carry that text cannot be the
	// verb's verdict.
	verbErr := scr.core.ProviderEdit("acme", "https://api.acme.example/v2", "")
	if verbErr == nil {
		t.Fatal("the verb unexpectedly accepted the corrupt registry")
	}
	if strings.Contains(msg, verbErr.Error()) {
		t.Fatalf("the message line carries the verb's load error %q, so the verb ran: %q", verbErr.Error(), msg)
	}
	if got := providerFormDisk(t, path); string(got) != string(broken) {
		t.Fatalf("providers.json changed although the stale check could not run:\nbefore: %s\nafter:  %s", broken, got)
	}
	if app.Depth() != 2 {
		t.Fatalf("depth = %d, want 2 (the form stays open on the refusal)", app.Depth())
	}
}

// providerFormSecretRefusalText is the value-free sentence the form uses for
// every credential-shaped field refusal.
const providerFormSecretRefusalText = "looks like secret/key material and cannot be saved (value hidden)"

// providerFormUnderscoreNameProbes is the FIX-K target class: key shapes whose
// head is absent from the shared prefix list (slotSecretPrefixes) and whose "_"
// breaks the opaque-run test, so both pre-union arms (providers.SecretShaped,
// providers.MaskOperand) returned them unchanged and the M4 "y" persisted them
// RAW. Synthetic values only.
var providerFormUnderscoreNameProbes = []string{
	testfixtures.SKLive,
	testfixtures.TokSample,
	testfixtures.PKLive,
	testfixtures.KeyHex,
	testfixtures.AKIAUnderscore,
}

// TestProviderFormUnderscoreNameProbesRefused is the FIX-K pin: each probe is
// refused at validation time with the value-free sentence, the grid row is
// masked, no M4 gate opens, and neither a stray "y" nor the field content can
// reach a temp-home providers.json.
func TestProviderFormUnderscoreNameProbesRefused(t *testing.T) {
	for _, probe := range providerFormUnderscoreNameProbes {
		t.Run(probe, func(t *testing.T) {
			// The hole is restated from the pre-union arms, so this pin proves the
			// gap instead of assuming it: both old arms are the identity and the
			// charset admits the value.
			if providers.SecretShaped(probe) || providers.MaskOperand(probe) != probe {
				t.Fatalf("%q is already refused by the pre-fix arms; it no longer exercises the gap", probe)
			}
			if providers.MaskSlot(probe) == probe {
				t.Fatalf("%q is identity under the slot authority; the union cannot catch it", probe)
			}
			if err := providers.ValidateProviderName(probe); err != nil {
				t.Fatalf("%q no longer passes the name charset, so the refusal would be the charset's: %v", probe, err)
			}

			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr := providerFormScreenOf(t, app)
			before := providerFormDisk(t, path)

			out := app.CaptureRun(&ScriptedKeys{Keys: append(providerFormType(probe), KeyEnter)})

			// The row the form refuses is the row the frame masks.
			masked := providers.MaskSlot(probe)
			if !strings.Contains(out, masked) {
				t.Fatalf("the name row was not masked as %q; frames:\n%s", masked, out)
			}
			if strings.Contains(out, probe) {
				t.Fatalf("a frame rendered the pasted key value raw:\n%s", out)
			}
			msg := scr.messageLine()
			if !strings.Contains(msg, providerFormSecretRefusalText) {
				t.Fatalf("message line = %q, want the value-free secret refusal", msg)
			}
			if strings.Contains(msg, probe) || strings.Contains(msg, probe[:5]) {
				t.Fatalf("the refusal echoes the refused value: %q", msg)
			}
			// Validation-time refusal: the M4 gate never opened.
			if _, isApproval := app.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (the secret refusal precedes the gate)", app.Modal())
			}
			if strings.Contains(out, "y approve write") {
				t.Fatalf("the approval modal opened over a credential-shaped name; frames:\n%s", out)
			}

			// Even a stray approval key cannot write: there is no gate to answer.
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

			got := providerFormDisk(t, path)
			if string(got) != string(before) {
				t.Fatalf("providers.json changed after the secret refusal:\nbefore: %s\nafter:  %s", before, got)
			}
			if strings.Contains(string(got), probe) {
				t.Fatalf("the refused key value reached providers.json: %s", got)
			}
			if app.Depth() != 2 {
				t.Fatalf("depth = %d, want 2 (the form stays open on the refusal)", app.Depth())
			}
		})
	}
}

// TestProviderFormNameArmAddsOnlyUnderscoreProbes pins the no-over-refusal
// property of the FIX-K union: the accept/refuse set after the fix is EXACTLY
// the pre-fix set plus the five underscore probes. "before" is recomputed from
// the pre-union expression on every row, so the table is a live before/after
// comparison rather than a restatement.
func TestProviderFormNameArmAddsOnlyUnderscoreProbes(t *testing.T) {
	cases := []struct {
		name   string
		before bool // providers.SecretShaped(v) || providers.MaskOperand(v) != v
		after  bool
	}{
		// FIX-K target class: accepted (and persisted raw) before the union.
		{testfixtures.SKLive, false, true},
		{testfixtures.TokSample, false, true},
		{testfixtures.PKLive, false, true},
		{testfixtures.KeyHex, false, true},
		{testfixtures.AKIAUnderscore, false, true},
		// Pre-existing refusals (16-rune run, prefix heads, JWT display arm):
		// unchanged by the union.
		{"openai2024backup", true, true},
		{"npm_registry", true, true},
		{"qwen2point5turbo", true, true},
		{"eu-west1.fastproxy.example", true, true},
		{"provider0123456789", true, true},
		{testfixtures.SKProj, true, true},
		// Legal records: unchanged accept verdicts.
		{"openai", false, false},
		{"gemini", false, false},
		{"deepseek", false, false},
		{"anthropic", false, false},
		{"mistral", false, false},
		{"groq", false, false},
		{"openrouter", false, false},
		{"together", false, false},
		{"fireworks", false, false},
		{"moonshot", false, false},
		{"zhipu", false, false},
		{"ollama", false, false},
		{"lmstudio", false, false},
		{"vllm", false, false},
		{"litellm", false, false},
		{"acme-new", false, false},
		{"acme2", false, false},
		{"my-provider", false, false},
		{"my_provider", false, false},
		{"internal-gateway", false, false},
		{"eu-east-prod", false, false},
		{"beta-fast", false, false},
		{"fast-tier-2", false, false},
		{"azure-eastus", false, false},
		{"bedrock-us-east-1", false, false},
		{"vertex-gemini", false, false},
		{"qwen-local", false, false},
		{"llama-local", false, false},
		{"gateway-eu", false, false},
		{"team-alpha-gateway", false, false},
		{"backup-2024", false, false},
		{"sandbox", false, false},
	}
	probe := map[string]bool{}
	for _, p := range providerFormUnderscoreNameProbes {
		probe[p] = true
	}

	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	for _, tc := range cases {
		before := providers.SecretShaped(tc.name) || providers.MaskOperand(tc.name) != tc.name
		after := fieldCarriesCredential(pfName, tc.name)
		if before != tc.before || after != tc.after {
			t.Fatalf("%q: before/after = %v/%v, want %v/%v", tc.name, before, after, tc.before, tc.after)
		}
		// The union's whole delta: no row outside the five probes may flip.
		if after != (before || probe[tc.name]) {
			t.Fatalf("%q flipped outside the FIX-K class (before=%v, after=%v)", tc.name, before, after)
		}
		if err := providers.ValidateProviderName(tc.name); err != nil {
			t.Fatalf("%q is no longer a legal provider name: %v", tc.name, err)
		}
		// The form door agrees with the arm.
		err := scr.validateName(tc.name)
		switch {
		case after && err == nil:
			t.Fatalf("%q passed validateName although the arm refuses it", tc.name)
		case after && !strings.Contains(err.Error(), providerFormSecretRefusalText):
			t.Fatalf("%q refusal = %q, want the value-free sentence", tc.name, err)
		case !after && err != nil:
			t.Fatalf("%q was refused by validateName although the arm accepts it: %v", tc.name, err)
		}
	}
}

// providerFormCredentialPathURLs is the FIX-L target class: credential material
// in a URL PATH. The base-URL arm's masker (providers.MaskBaseURL) covers
// userinfo, query and fragment only, and providers.SecretShaped classifies a
// value whole, so a path component was invisible to every arm and persisted
// raw. Synthetic values only.
var providerFormCredentialPathURLs = []struct {
	url   string
	token string // the credential-bearing part that must never be echoed
}{
	{
		url:   "https://gw.example/v1/" + testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		token: testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	},
	{
		url:   "https://gw.example/v1/0123456789abcdef",
		token: "0123456789abcdef",
	},
	{
		url:   "https://gw.example/v1/" + testfixtures.NvidiaKey,
		token: testfixtures.NvidiaKey,
	},
	{
		// The FIX-K underscore class parked in a path rather than a name.
		url:   "https://gw.example/v1/" + testfixtures.SKLive,
		token: testfixtures.SKLive,
	},
	{
		// The same slot in its non-hierarchical spelling: the transport rule
		// accepts a hostless "https:", and url.Parse reports the key as Opaque
		// with an EMPTY Path, so the path loop alone would miss it.
		url:   "https:" + testfixtures.SKLive,
		token: testfixtures.SKLive,
	},
}

// TestProviderFormBaseURLPathSecretRefused is the FIX-L pin: a URL whose path
// carries a credential-shaped segment is refused at validation time with the
// existing value-free sentence, the M4 gate never opens, and nothing is
// written.
func TestProviderFormBaseURLPathSecretRefused(t *testing.T) {
	for _, tc := range providerFormCredentialPathURLs {
		t.Run(tc.token, func(t *testing.T) {
			// The gap, restated from the pre-fix arms: the shared URL masker and the
			// whole-value classifier both read the URL as benign.
			if providers.SecretShaped(tc.url) || providers.MaskBaseURL(tc.url) != tc.url {
				t.Fatalf("%q is already refused by the pre-fix arms; it no longer exercises the path gap", tc.url)
			}
			if !providers.BaseURLCarriesCredential(tc.url) {
				t.Fatalf("%q does not read as a credential path; the fixture no longer exercises FIX-L", tc.url)
			}

			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr := providerFormScreenOf(t, app)
			before := providerFormDisk(t, path)

			keys := append(providerFormType("acme-new"), KeyTab)
			keys = append(keys, providerFormType(tc.url)...)
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})

			msg := scr.messageLine()
			if !strings.Contains(msg, providerFormSecretRefusalText) {
				t.Fatalf("message line = %q, want the value-free secret refusal", msg)
			}
			if strings.Contains(msg, tc.token) {
				t.Fatalf("the refusal echoes the refused value: %q", msg)
			}
			if _, isApproval := app.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (the path refusal precedes the gate)", app.Modal())
			}
			if strings.Contains(out, "y approve write") {
				t.Fatalf("the approval modal opened over a credential-bearing URL; frames:\n%s", out)
			}

			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})

			got := providerFormDisk(t, path)
			if string(got) != string(before) {
				t.Fatalf("providers.json changed after the path refusal:\nbefore: %s\nafter:  %s", before, got)
			}
			if strings.Contains(string(got), tc.token) {
				t.Fatalf("the refused URL reached providers.json: %s", got)
			}
			if app.Depth() != 2 {
				t.Fatalf("depth = %d, want 2 (the form stays open on the refusal)", app.Depth())
			}
		})
	}
}

// TestProviderFormBaseURLPathArmAddsOnlyCredentialPaths pins the FIX-L
// no-over-refusal property: the base-URL arm's accept/refuse set after the fix
// is the pre-fix set plus the credential-path URLs, and every benign path keeps
// its accept verdict. "before" is recomputed from the pre-fix expression, so the
// query refusals are shown to be unchanged. The one row the FIX-8h alignment
// flips is the benign userinfo URL: the pre-fix form arm compared
// providers.MaskBaseURL(v) against v, and that masker drops ANY userinfo for
// display, so the form refused "https://user:pw@gw.example/v1" while the wizard
// and the CLI accepted it. The shared URL authority classifies the userinfo
// parts instead, so the row is accepted here exactly as at the other two doors.
func TestProviderFormBaseURLPathArmAddsOnlyCredentialPaths(t *testing.T) {
	cases := []struct {
		url    string
		before bool // providers.SecretShaped(v) || providers.MaskBaseURL(v) != v
		after  bool
	}{
		// Benign endpoints: ordinary path components stay accepted.
		{"https://gw.example/v1", false, false},
		{"https://gw.example/v1/chat/completions", false, false},
		{"https://gw.example/api/v2", false, false},
		{"https://gw.example/v1/beta-fast", false, false},
		{"https://", false, false},
		{"https:", false, false},
		{"https:gw.example", false, false},
		{"http://127.0.0.1:8080/v1", false, false},
		{"https://api.openai.com/v1", false, false},
		{"https://openrouter.ai/api/v1", false, false},
		{"https://generativelanguage.googleapis.com/v1beta", false, false},
		// FIX-L target class: accepted (and persisted raw) before the path arm.
		{providerFormCredentialPathURLs[0].url, false, true},
		{providerFormCredentialPathURLs[1].url, false, true},
		{providerFormCredentialPathURLs[2].url, false, true},
		{providerFormCredentialPathURLs[3].url, false, true},
		{providerFormCredentialPathURLs[4].url, false, true},
		// FIX-8h alignment: benign userinfo is accepted (the pre-fix arm refused
		// it only because the display masker drops userinfo).
		{"https://user:pw@gw.example/v1", true, false},
		// Pre-existing query and head refusals: unchanged.
		{"https://gw.example/v1?api_key=" + providerFormSecretValue, true, true},
		{"https://gw.example/v1/" + providerFormSecretValue, true, true},
	}
	credPath := map[string]bool{}
	for _, tc := range providerFormCredentialPathURLs {
		credPath[tc.url] = true
	}
	userinfoAligned := map[string]bool{"https://user:pw@gw.example/v1": true}

	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	for _, tc := range cases {
		before := providers.SecretShaped(tc.url) || providers.MaskBaseURL(tc.url) != tc.url
		after := fieldCarriesCredential(pfBaseURL, tc.url)
		if before != tc.before || after != tc.after {
			t.Fatalf("%q: before/after = %v/%v, want %v/%v", tc.url, before, after, tc.before, tc.after)
		}
		want := before || credPath[tc.url]
		if userinfoAligned[tc.url] {
			want = false
		}
		if after != want {
			t.Fatalf("%q flipped outside the documented classes (before=%v, after=%v)", tc.url, before, after)
		}
		err := scr.validateBaseURL(tc.url)
		switch {
		case after && err == nil:
			t.Fatalf("%q passed validateBaseURL although the arm refuses it", tc.url)
		case after && !strings.Contains(err.Error(), providerFormSecretRefusalText):
			t.Fatalf("%q refusal = %q, want the value-free sentence", tc.url, err)
		case !after && err != nil:
			t.Fatalf("%q was refused by validateBaseURL although the arm accepts it: %v", tc.url, err)
		}
	}
}

// TestProviderFormBaseURLPathBenignAccepted is the accept-side end-to-end pin:
// every ordinary endpoint still opens the M4 approval, and declining it leaves
// providers.json untouched.
func TestProviderFormBaseURLPathBenignAccepted(t *testing.T) {
	for _, raw := range []string{
		"https://gw.example/v1",
		"https://gw.example/v1/chat/completions",
		"https://gw.example/api/v2",
		"https://gw.example/v1/beta-fast",
		"https://",
	} {
		t.Run(raw, func(t *testing.T) {
			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			before := providerFormDisk(t, path)
			out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("acme-new", raw, "ACME_KEY")})
			if _, ok := app.Modal().(*Approval); !ok {
				t.Fatalf("%q was refused by the form; frames:\n%s", raw, out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatalf("providers.json changed after a declined approval:\nbefore: %s\nafter:  %s", before, got)
			}
		})
	}
}

// providerFormCredentialSlotURLs is the FIX-7A target class: credential material
// in the URL slots the FIX-L path arm does not reach. The phase-4 review swarm
// confirmed the shared maskers read each of these as benign, because the value's
// head is absent from the prefix list and its "_" breaks the opaque-run test.
// preFix records which arm refused the row BEFORE this unit: false means the row
// was accepted and persisted raw (the confirmed gap), true means the display
// masker already refused it and the per-part classifier now states the same rule
// on the ingest side. Both sides are recomputed on every run, so the table stays
// a live before/after comparison. Synthetic values only.
var providerFormCredentialSlotURLs = []struct {
	name   string
	url    string
	token  string
	preFix bool
}{
	{"query value", "https://gw.example/v1?api_key=" + testfixtures.SKLive, testfixtures.SKLive, false},
	{"query key", "https://gw.example/v1?" + testfixtures.SKLive + "=1", testfixtures.SKLive, false},
	{"fragment", "https://host/v1#" + testfixtures.SKLive, testfixtures.SKLive, false},
	{"userinfo username", "https://" + testfixtures.SKLive + "@host/v1", testfixtures.SKLive, true},
	{"userinfo password", "https://user:" + testfixtures.SKLive + "@host/v1", testfixtures.SKLive, true},
}

// TestProviderFormBaseURLCredentialSlotsRefused is the FIX-7A pin for the three
// slots the FIX-L arm left open: the query (both sides of a pair), the fragment,
// and the userinfo. Each row proves its own premise against the pre-fix arms,
// then requires the per-part classifier, the field arm, and the field validator
// to refuse it, and drives the form end to end so the M4 gate never opens and
// nothing is written.
func TestProviderFormBaseURLCredentialSlotsRefused(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	for _, tc := range providerFormCredentialSlotURLs {
		t.Run(tc.name, func(t *testing.T) {
			// The path is "/v1" on every row, so the pre-fix verdict is decided
			// by the two masker arms; the path arm is the identity here.
			preFix := providers.SecretShaped(tc.url) || providers.MaskBaseURL(tc.url) != tc.url
			if preFix != tc.preFix {
				t.Fatalf("%q: pre-fix arm verdict = %v, want %v (the fixture no longer exercises the slot it names)",
					tc.url, preFix, tc.preFix)
			}
			if !providers.BaseURLCarriesCredential(tc.url) {
				t.Fatalf("%q does not read as credential-bearing; the shared URL authority is missing the slot", tc.url)
			}
			if !fieldCarriesCredential(pfBaseURL, tc.url) {
				t.Fatalf("%q passes the base-URL arm", tc.url)
			}
			err := scr.validateBaseURL(tc.url)
			if err == nil || !strings.Contains(err.Error(), providerFormSecretRefusalText) {
				t.Fatalf("%q validation = %v, want the value-free secret refusal", tc.url, err)
			}
			if err != nil && strings.Contains(err.Error(), tc.token) {
				t.Fatalf("%q refusal echoes the refused value: %q", tc.url, err)
			}

			app2, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr2 := providerFormScreenOf(t, app2)
			before := providerFormDisk(t, path)
			keys := append(providerFormType("acme-new"), KeyTab)
			keys = append(keys, providerFormType(tc.url)...)
			keys = append(keys, KeyEnter)
			app2.CaptureRun(&ScriptedKeys{Keys: keys})
			if msg := scr2.messageLine(); !strings.Contains(msg, providerFormSecretRefusalText) {
				t.Fatalf("message line = %q, want the value-free secret refusal", msg)
			}
			if _, isApproval := app2.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (the slot refusal precedes the gate)", app2.Modal())
			}
			app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatalf("%q reached providers.json:\nbefore: %s\nafter:  %s", tc.url, before, got)
			}
			if strings.Contains(string(providerFormDisk(t, path)), tc.token) {
				t.Fatalf("the refused credential reached providers.json")
			}
		})
	}
}

// providerFormBaseURLIdentifierURLs is the FIX-7A over-refusal guard: the
// identifier exemption must accept the gateway shapes that carry an id in the
// URL, and the credential shapes around them must stay refused.
const providerFormIdentifierHex32 = "0123456789abcdef0123456789abcdef"

var providerFormCredentialPathJWT = "https://gw.example/v1/" + testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"

// TestProviderFormBaseURLIdentifierExemption pins the FIX-7A identifier rule: a
// URL part whose whole shape is an identifier (all-lowercase hex at the
// identifier floor, or the UUID form) is not credential material, so the
// Cloudflare AI gateway endpoint with a 32-hex account id is accepted again.
// The neighbouring credential shapes stay refused, including the bare 16-rune
// mixed run the FIX-L pin requires, so the exemption cannot be widened by
// accident.
func TestProviderFormBaseURLIdentifierExemption(t *testing.T) {
	cloudflare := "https://gateway.ai.cloudflare.com/v1/" + providerFormIdentifierHex32 + "/anthropic"
	uuidURL := "https://gw.example/v1/550e8400-e29b-41a7-a716-446655440000/chat"

	cases := []struct {
		name   string
		url    string
		refuse bool
	}{
		{"cloudflare gateway account id", cloudflare, false},
		{"uuid resource id", uuidURL, false},
		{"uuid resource id uppercase", "https://gw.example/v1/550E8400-E29B-41A7-A716-446655440000/chat", false},
		{"ordinary version path", "https://gw.example/v1", false},
		{"ordinary chat path", "https://gw.example/v1/chat/completions", false},
		{"ordinary api path", "https://gw.example/api/v2", false},
		{"ordinary query", "https://gw.example/v1?api-version=2024-02-01", false},
		{"ordinary query text", "https://gw.example/v1?q=hello", false},
		{"ordinary fragment", "https://gw.example/v1#section-2", false},
		{"jwt in path", providerFormCredentialPathJWT, true},
		{"16-rune mixed run in path", "https://gw.example/v1/0123456789abcdef", true},
		{"24-rune mixed base64 run in path", "https://gw.example/v1/ab3def7gh9jk1mnpqr5txz", true},
		{"uppercase 32-rune hex run in path", "https://gw.example/v1/0123456789ABCDEF0123456789ABCDEF", true},
		{"nvapi head in path", "https://gw.example/v1/" + testfixtures.NvidiaKey, true},
		{"underscore head in path", "https://gw.example/v1/" + testfixtures.SKLive, true},
		{"opaque spelling", "https:" + testfixtures.SKLive, true},
	}

	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	// Premise for the exemption case: the pre-fix path rule (the slot masker read
	// per segment) refuses the account id, and the display masker never masked the
	// URL, so only the identifier rule can be what flips the verdict.
	if providers.MaskSlot(providerFormIdentifierHex32) == providerFormIdentifierHex32 {
		t.Fatalf("premise broken: the slot masker is the identity on the 32-hex account id")
	}
	if providers.MaskBaseURL(cloudflare) != cloudflare || maskBaseURL(cloudflare) != cloudflare {
		t.Fatalf("the display maskers changed for the gateway URL; FIX-7A keeps them untouched")
	}
	if providers.MaskSlot("550e8400-e29b-41a7-a716-446655440000") != "550e8400-e29b-41a7-a716-446655440000" {
		t.Fatalf("premise broken: the UUID form is no longer identity under the slot masker")
	}
	if providers.MaskSlot("0123456789abcdef") == "0123456789abcdef" {
		t.Fatalf("premise broken: the 16-rune mixed run is no longer refused by the slot masker")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providers.BaseURLCarriesCredential(tc.url); got != tc.refuse {
				t.Fatalf("BaseURLCarriesCredential(%q) = %v, want %v", tc.url, got, tc.refuse)
			}
			if got := fieldCarriesCredential(pfBaseURL, tc.url); got != tc.refuse {
				t.Fatalf("fieldCarriesCredential(%q) = %v, want %v", tc.url, got, tc.refuse)
			}
			err := scr.validateBaseURL(tc.url)
			switch {
			case tc.refuse && err == nil:
				t.Fatalf("%q passed validateBaseURL although the arm refuses it", tc.url)
			case tc.refuse && !strings.Contains(err.Error(), providerFormSecretRefusalText):
				t.Fatalf("%q refusal = %q, want the value-free sentence", tc.url, err)
			case !tc.refuse && err != nil:
				t.Fatalf("%q was refused although the identifier rule accepts it: %v", tc.url, err)
			}
		})
	}

	// End to end on the two accepted identifier shapes: the M4 gate opens, and a
	// declined approval writes nothing.
	for _, raw := range []string{cloudflare, uuidURL} {
		t.Run("accepted/"+raw, func(t *testing.T) {
			app2, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			before := providerFormDisk(t, path)
			out := app2.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("acme-new", raw, "ACME_KEY")})
			if _, ok := app2.Modal().(*Approval); !ok {
				t.Fatalf("%q was refused by the form; frames:\n%s", raw, out)
			}
			app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatalf("providers.json changed after a declined approval:\nbefore: %s\nafter:  %s", before, got)
			}
		})
	}
}

// TestProviderFormNameArmIsTheSharedAuthority is the FIX-7A drift pin for the
// name arm: the form's predicate must be the shared authority the sibling doors
// call, not a local union that can drift from it.
func TestProviderFormNameArmIsTheSharedAuthority(t *testing.T) {
	values := append([]string{}, providerFormUnderscoreNameProbes...)
	values = append(values,
		"openai", "gemini", "deepseek", "anthropic", "key_backup", "sk_staging",
		"openai2024backup", "npm_registry", "qwen2point5turbo",
		"eu-west1.fastproxy.example", "acme-new", "my_provider",
		"internal-gateway", "sandbox")
	for _, v := range values {
		if got, want := fieldCarriesCredential(pfName, v), providers.NameCarriesCredential(v); got != want {
			t.Fatalf("fieldCarriesCredential(pfName, %q) = %v, want the shared authority's %v", v, got, want)
		}
	}
}

// TestProviderFormEditFixedNameLineMasked is the FIX-7A display pin: a
// hand-planted key-shaped name on disk rendered RAW in the edit-mode fixed line
// (the bare operand masker is the identity on the underscore-head class) while
// the add-mode name row masked it. Both the fixed line and the M4 approval
// summary now route the on-disk name through the registry-slot authority.
func TestProviderFormEditFixedNameLineMasked(t *testing.T) {
	const planted = testfixtures.SKLive
	if providers.MaskOperand(planted) != planted {
		t.Fatalf("premise broken: the bare operand masker already masks the planted name")
	}
	want := providers.MaskSlot(planted)
	if want == planted {
		t.Fatalf("premise broken: the slot authority is the identity on the planted name")
	}
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      planted,
		BaseURL:   "https://acme.example/v1",
		APIKeyEnv: "ACME_KEY",
	}}}
	app, _ := newProviderFormTestApp(t, reg, "edit", planted)
	scr := providerFormScreenOf(t, app)
	if !scr.origFound {
		t.Fatalf("the planted record did not resolve, so the pin would test the not-found state")
	}

	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, "Provider (fixed) : "+want) {
		t.Fatalf("the fixed-name line did not render %q; frames:\n%s", want, out)
	}
	if strings.Contains(out, planted) {
		t.Fatalf("a frame rendered the on-disk key-shaped name raw:\n%s", out)
	}

	// The M4 approval frame signs the same record name: walk the grid to Save and
	// open the gate without changing a field.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyEnter, KeyTab, KeyEnter}})
	appr, ok := app.Modal().(*Approval)
	if !ok {
		t.Fatalf("modal = %T, want *Approval", app.Modal())
	}
	summary := strings.Join(appr.Body(80), "\n")
	if !strings.Contains(summary, "Edits provider "+want+" in place") {
		t.Fatalf("the approval summary did not mask the record name; summary:\n%s", summary)
	}
	if strings.Contains(summary, planted) {
		t.Fatalf("the approval summary signed the key-shaped record name raw:\n%s", summary)
	}
}

// TestProviderFormMissingTargetEchoMasked pins the second on-demand name echo: a
// key-shaped operand that resolves to no record is rendered through the
// registry-slot authority in the not-found line, never raw.
func TestProviderFormMissingTargetEchoMasked(t *testing.T) {
	const planted = testfixtures.SKLive
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "edit", planted)
	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, providers.MaskSlot(planted)) {
		t.Fatalf("the not-found line did not render the masked operand; frames:\n%s", out)
	}
	if strings.Contains(out, planted) {
		t.Fatalf("the not-found line echoed the key-shaped operand raw:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// FIX-8: the shared URL authority across the three real ingest doors.
// ---------------------------------------------------------------------------

// providerFormURLDoorCorpus is the cross-door corpus (58 synthetic URLs; no live
// providers.json or state.json is touched). Every URL slot appears on both
// sides: the benign shapes all three doors must keep accepting (ordinary
// gateways, the Cloudflare 32-hex account-id path, the two long gateway
// hostnames the reviewers probe-verified, a benign userinfo pair) and one
// credential shape per slot (host head, host decoration, host JWT, userinfo
// username and password, path segment, Opaque spelling, query key, query value,
// fragment). secret is the credential-bearing part a refusal must never echo;
// it is empty on accept rows and on a refusal row that carries no credential
// (the FIX-8h fail-closed row for a merely malformed URL).
var providerFormURLDoorCorpus = []struct {
	name   string
	url    string
	refuse bool
	secret string
}{
	// Accept side.
	{"ordinary openai", "https://api.openai.com/v1", false, ""},
	{"ordinary openrouter", "https://openrouter.ai/api/v1", false, ""},
	{"ordinary google", "https://generativelanguage.googleapis.com/v1beta", false, ""},
	{"plain gateway", "https://gw.example/v1", false, ""},
	{"chat path", "https://gw.example/v1/chat/completions", false, ""},
	{"scheme only", "https://", false, ""},
	{"scheme only hostless", "https:", false, ""},
	{"hostless gateway", "https:gw.example", false, ""},
	{"loopback", "http://127.0.0.1:8080/v1", false, ""},
	{"benign userinfo", "https://user:pw@gw.example/v1", false, ""},
	{"benign userinfo username", "https://user@api.example.com/v1", false, ""},
	{"cloudflare account id", "https://gateway.ai.cloudflare.com/v1/0123456789abcdef0123456789abcdef/anthropic", false, ""},
	{"long gateway host a", "https://mycompany2024gateway.example.com/v1", false, ""},
	{"long gateway host b", "https://acmeprod2024gateway.example.com/v1", false, ""},
	{"benign query", "https://gw.example/v1?api-version=2024-02-01", false, ""},
	{"benign fragment", "https://gw.example/v1#section-2", false, ""},
	{"benign hyphen path", "https://gw.example/v1/beta-fast", false, ""},
	{"uuid host label", "https://550e8400-e29b-41a7-a716-446655440000.example.com/v1", false, ""},
	{"compound host label", "https://aci-gw-2024-production.us-east-1.example.com/v1", false, ""},
	{"single class long label", "https://verylonggatewaylabelname.example.com/v1", false, ""},
	// Refuse side: the host slot (FIX-8f).
	{"host underscore head", "https://" + testfixtures.SKLive + "/v1", true, testfixtures.SKLive},
	{"host tok head", "https://" + testfixtures.TokSample + "/v1", true, testfixtures.TokSample},
	{"host pk head", "https://" + testfixtures.PKLive + "/v1", true, testfixtures.PKLive},
	{"host key head", "https://" + testfixtures.KeyHex + "/v1", true, testfixtures.KeyHex},
	{"host ghp head", "https://" + testfixtures.GHPShort + ".example.com/v1", true, testfixtures.GHPShort},
	{"host aws key id", "https://" + testfixtures.AKIAExample + "/v1", true, testfixtures.AKIAExample},
	{"host jwt", "https://" + testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U/v1", true, testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
	{"host decorated", "https://1-" + testfixtures.ASIADocExample + ".example.com/v1", true, testfixtures.ASIADocExample},
	// The documented bare-run trade: the host label is one uninterrupted run, so
	// the whole-value arms decide it (and refuse it). The row is here so the host
	// rule can never turn it into an accepted value.
	{"bare run host", "https://0123456789abcdef/v1", true, "0123456789abcdef"},
	// Refuse side: userinfo, path, Opaque, query, fragment.
	{"userinfo username", "https://" + testfixtures.SKLive + "@host/v1", true, testfixtures.SKLive},
	{"userinfo password", "https://user:" + testfixtures.SKLive + "@host/v1", true, testfixtures.SKLive},
	{"path underscore head", "https://host/v1/" + testfixtures.SKLive, true, testfixtures.SKLive},
	{"path 16 run", "https://host/v1/0123456789abcdef", true, "0123456789abcdef"},
	{"path jwt", "https://host/v1/" + testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", true, testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
	{"opaque path", "https:" + testfixtures.SKLive, true, testfixtures.SKLive},
	{"query value", "https://host/v1?api_key=" + testfixtures.SKLive, true, testfixtures.SKLive},
	{"query key", "https://host/v1?" + testfixtures.SKLive + "=1", true, testfixtures.SKLive},
	{"fragment", "https://host/v1#" + testfixtures.SKLive, true, testfixtures.SKLive},
	{"path sk head", "https://gw.example/v1/" + providerFormSecretValue, true, providerFormSecretValue},
	{"path nvapi head", "https://gw.example/v1/" + testfixtures.NvidiaKey, true, testfixtures.NvidiaKey},
	// FIX-8h H-1: the malformed query pair url.Query() drops. The decoded view
	// never sees it, so all three doors accepted it before the raw-pair read.
	{"query malformed value", "https://gw.example/v1?api_key=" + testfixtures.SKLive + "%2", true, testfixtures.SKLive},
	{"query malformed key", "https://gw.example/v1?" + testfixtures.SKLive + "=1%2", true, testfixtures.SKLive},
	{"query malformed second pair", "https://gw.example/v1?api-version=2024-02-01&api_key=" + testfixtures.SKLive + "%2", true, testfixtures.SKLive},
	// FIX-8h H-1 accept side: odd-but-legal escapes keep their verdict.
	{"query odd escape pct25", "https://gw.example/v1?q=100%25", false, ""},
	{"query odd escape bare pct", "https://gw.example/v1?q=100%", false, ""},
	{"query odd escape comma", "https://gw.example/v1?filter=a%2Cb", false, ""},
	{"query malformed benign", "https://gw.example/v1?api-version=2024-02-01%2", false, ""},
	// FIX-8h H-3: an invisible rune inside the host label.
	{"host zwsp", "https://" + testfixtures.SKLiveShort + "\u200bcdef/v1", true, testfixtures.SKLive},
	{"host zwj", "https://" + testfixtures.SKLiveShort + "\u200dcdef/v1", true, testfixtures.SKLive},
	{"host bidi override", "https://" + testfixtures.SKLiveShort + "\u202ecdef/v1", true, testfixtures.SKLive},
	{"host bom", "https://" + testfixtures.SKLiveShort + "\ufeffcdef/v1", true, testfixtures.SKLive},
	{"host combining mark", "https://" + testfixtures.SKLiveShort + "\u0301cdef/v1", true, testfixtures.SKLive},
	{"host benign zwsp", "https://mycompany2024\u200bgateway.example.com/v1", false, ""},
	// FIX-8h H-2: a value url.Parse cannot read. The authority fails closed, so
	// the wizard and the add-json parser refuse where only the transport rule
	// used to (and that rule echoed the operand).
	{"unparseable path escape", "https://gw.example/v1/" + testfixtures.SKLive + "%2", true, testfixtures.SKLive},
	{"unparseable path escape benign", "https://gw.example/%zz", true, ""},
	{"unparseable port", "https://gw.example:" + testfixtures.SKLive + "/v1", true, testfixtures.SKLive},
	{"unparseable fragment escape", "https://gw.example/v1#" + testfixtures.SKLive + "%2", true, testfixtures.SKLive},
	{"unparseable userinfo escape", "https://" + testfixtures.SKLive + "%2@gw.example/v1", true, testfixtures.SKLive},
}

// providerFormDoorSpec renders the add-json wire shape for one base URL.
func providerFormDoorSpec(rawURL string) []byte {
	b, _ := json.Marshal(settings.ProviderSpecJSON{
		Name: "acme", BaseURL: rawURL, APIKeyEnv: "ACME_API_KEY",
	})
	return b
}

// TestProviderFormURLDoorAgreement asserts door agreement over the corpus: the
// TUI provider form's field validator, the add-json parser, and the shared URL
// authority return the same credential verdict on every row. The wizard's URL
// guard is pinned against the same authority in internal/providers
// (TestWizardBaseURLDoorMatchesSharedAuthority) and its refusals are driven end
// to end below, so all three ingest doors are held to one rule.
func TestProviderFormURLDoorAgreement(t *testing.T) {
	app, _ := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	for _, tc := range providerFormURLDoorCorpus {
		t.Run(tc.name, func(t *testing.T) {
			formRefuse := false
			if err := scr.validateBaseURL(tc.url); err != nil {
				formRefuse = strings.Contains(err.Error(), providerFormSecretRefusalText)
				if !formRefuse {
					t.Fatalf("%q was refused for a non-credential reason: %v", tc.url, err)
				}
			} else if tc.refuse {
				t.Fatalf("%q passed the form door although the corpus refuses it", tc.url)
			}

			_, specErr := settings.ParseProviderSpecJSON(providerFormDoorSpec(tc.url))
			addJSONRefuse := errors.Is(specErr, settings.ErrProviderSpecBaseURLCarriesCredential)
			if specErr != nil && !addJSONRefuse {
				t.Fatalf("%q was refused by add-json for a non-credential reason: %v", tc.url, specErr)
			}

			shared := providers.BaseURLCarriesCredential(tc.url)
			if formRefuse != tc.refuse || addJSONRefuse != tc.refuse || shared != tc.refuse {
				t.Fatalf("%q: form=%v addJSON=%v shared=%v; want %v on all three",
					tc.url, formRefuse, addJSONRefuse, shared, tc.refuse)
			}
		})
	}
}

// TestProviderFormURLDoorAgreementWizardRefusals drives the REAL wizard over
// every refuse row: the wizard stops at its base-URL prompt (before the key
// prompt, before the live probe, before any write), the refusal is value-free,
// and the temp AGY_PRO_HOME keeps no providers.json. Accept rows are not driven
// here because a full wizard run for an accepted external host resolves that
// host, which is the very leak this unit closes; their wizard-side verdict is
// pinned on the guard in internal/providers.
func TestProviderFormURLDoorAgreementWizardRefusals(t *testing.T) {
	for _, tc := range providerFormURLDoorCorpus {
		if !tc.refuse {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(providers.EnvHome, home)
			path := filepath.Join(home, "providers.json")
			t.Setenv(providers.EnvProviders, path)
			t.Setenv("ACME_API_KEY", "")

			out := &bytes.Buffer{}
			err := providers.InteractiveSetupWizard(strings.NewReader("acme\n"+tc.url+"\n"), out)
			if err == nil || !strings.Contains(err.Error(), "Base URL looks like secret material") {
				t.Fatalf("the wizard accepted %q: err=%v\ntranscript:\n%s", tc.url, err, out.String())
			}
			if !strings.Contains(err.Error(), "value hidden") {
				t.Fatalf("the wizard refusal is not the value-free sentence: %q", err)
			}
			if tc.secret != "" && (strings.Contains(err.Error(), tc.secret) || strings.Contains(out.String(), tc.secret)) {
				t.Fatalf("the wizard refusal echoed the credential from %q", tc.url)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a refused URL wrote a registry")
			}
		})
	}
}

// TestProviderFormBaseURLHostSlotRefused is the FIX-8f end-to-end pin on the TUI
// door: a key pasted as the host label opens no M4 gate and writes nothing, and
// the refusal never echoes the value. The host row was the one the phase-4
// review swarm confirmed was accepted, persisted raw, and resolved as a DNS name
// by the live probe.
func TestProviderFormBaseURLHostSlotRefused(t *testing.T) {
	for _, tc := range providerFormURLDoorCorpus {
		if !tc.refuse || !strings.HasPrefix(tc.name, "host ") {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr := providerFormScreenOf(t, app)
			before := providerFormDisk(t, path)

			keys := append(providerFormType("acme-new"), KeyTab)
			keys = append(keys, providerFormType(tc.url)...)
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})

			msg := scr.messageLine()
			if !strings.Contains(msg, providerFormSecretRefusalText) {
				t.Fatalf("message line = %q, want the value-free secret refusal", msg)
			}
			if strings.Contains(msg, tc.secret) {
				t.Fatalf("the refusal echoes the refused value: %q", msg)
			}
			if _, isApproval := app.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (the host refusal precedes the gate)", app.Modal())
			}
			if strings.Contains(out, "y approve write") {
				t.Fatalf("the approval modal opened over a credential-bearing host; frames:\n%s", out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatalf("providers.json changed after the host refusal:\nbefore: %s\nafter:  %s", before, got)
			}
		})
	}
}

// TestProviderFormBaseURLFix8hRowsRefused is the FIX-8h end-to-end pin on the TUI
// door for the three shapes the fix8 review found open: the malformed query
// pair url.Query() drops (H-1), the invisible rune inside the host label (H-3),
// and the unparseable value the authority used to defer to a transport rule that
// echoed the operand (H-2). Every row refuses at the field arm, the message line
// is the value-free secret sentence, no credential fragment reaches the line or
// the frames, no approval opens, and nothing is written. The H-2 rows are also
// the pin for the commit-path sink (screen_provider_form.go:1084/:1121): that
// sink renders the error through MaskOperand, and the error body is value-free
// now, so the fragment cannot survive either.
func TestProviderFormBaseURLFix8hRowsRefused(t *testing.T) {
	for _, tc := range providerFormURLDoorCorpus {
		if !tc.refuse || !(strings.HasPrefix(tc.name, "query malformed") || strings.HasPrefix(tc.name, "host zw") ||
			strings.HasPrefix(tc.name, "host bidi") || strings.HasPrefix(tc.name, "host bom") ||
			strings.HasPrefix(tc.name, "host combining") || strings.HasPrefix(tc.name, "unparseable")) {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			scr := providerFormScreenOf(t, app)
			before := providerFormDisk(t, path)

			keys := append(providerFormType("acme-new"), KeyTab)
			keys = append(keys, providerFormType(tc.url)...)
			keys = append(keys, KeyEnter)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})

			msg := scr.messageLine()
			if !strings.Contains(msg, providerFormSecretRefusalText) {
				t.Fatalf("message line = %q, want the value-free secret refusal", msg)
			}
			if tc.secret != "" && strings.Contains(msg, tc.secret) {
				t.Fatalf("the refusal echoes the refused value: %q", msg)
			}
			if _, isApproval := app.Modal().(*Approval); isApproval {
				t.Fatalf("modal = %T, want no approval (the refusal precedes the gate)", app.Modal())
			}
			if strings.Contains(out, "y approve write") {
				t.Fatalf("the approval modal opened over a refused URL; frames:\n%s", out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatalf("providers.json changed after the refusal:\nbefore: %s\nafter:  %s", before, got)
			}
		})
	}
}

// TestProviderFormBaseURLHostGatewayAccepted is the FIX-8f accept side: the long
// gateway hostnames the reviewers probe-verified (and the Cloudflare account-id
// endpoint) still open the M4 approval, and a declined approval writes nothing,
// so the targeted host rule did not turn every long host label into a refusal.
func TestProviderFormBaseURLHostGatewayAccepted(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"long gateway a", "https://mycompany2024gateway.example.com/v1"},
		{"long gateway b", "https://acmeprod2024gateway.example.com/v1"},
		{"cloudflare account id", "https://gateway.ai.cloudflare.com/v1/0123456789abcdef0123456789abcdef/anthropic"},
		{"compound host", "https://aci-gw-2024-production.us-east-1.example.com/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
			before := providerFormDisk(t, path)
			out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("acme-new", tc.url, "ACME_KEY")})
			if _, ok := app.Modal().(*Approval); !ok {
				t.Fatalf("%q was refused by the form; frames:\n%s", tc.url, out)
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
			if got := providerFormDisk(t, path); string(got) != string(before) {
				t.Fatalf("providers.json changed after a declined approval:\nbefore: %s\nafter:  %s", before, got)
			}
		})
	}
}

// TestProviderFormBenignUserinfoAccepted is the FIX-8h alignment pin on the TUI
// door: a benign "user:pw@host" base URL is accepted (the pre-fix arm refused
// every userinfo URL because providers.MaskBaseURL drops userinfo for display),
// the M4 approval opens with the userinfo-free display rendering the other doors
// already used, and the credential-shaped userinfo spellings stay refused. The
// wizard and the add-json parser agree on all three rows: the wizard's benign
// userinfo acceptance is pinned end to end in internal/providers
// (TestInteractiveSetupWizard_BaseURLGuard) and the parser's verdict is pinned in
// internal/settings (TestParseProviderSpecJSON_BenignBaseURLsUnchanged).
func TestProviderFormBenignUserinfoAccepted(t *testing.T) {
	const benign = "https://user:pw@gw.example/v1"
	if providers.BaseURLCarriesCredential(benign) {
		t.Fatalf("premise: the shared authority refuses the benign userinfo URL")
	}
	if _, err := settings.ParseProviderSpecJSON(providerFormDoorSpec(benign)); err != nil {
		t.Fatalf("premise: the add-json door refuses the benign userinfo URL: %v", err)
	}

	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	before := providerFormDisk(t, path)
	out := app.CaptureRun(&ScriptedKeys{Keys: providerFormAddKeys("acme-new", benign, "ACME_KEY")})
	if _, ok := app.Modal().(*Approval); !ok {
		t.Fatalf("the form refused a benign userinfo URL; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatalf("providers.json changed after a declined approval:\nbefore: %s\nafter:  %s", before, got)
	}

	// The credential-shaped userinfo spellings stay refused at every door.
	for _, raw := range []string{
		"https://" + testfixtures.SKLive + "@gw.example/v1",
		"https://user:" + testfixtures.SKLive + "@gw.example/v1",
	} {
		if !providers.BaseURLCarriesCredential(raw) {
			t.Fatalf("the shared authority accepts %q", raw)
		}
		if _, err := settings.ParseProviderSpecJSON(providerFormDoorSpec(raw)); !errors.Is(err, settings.ErrProviderSpecBaseURLCarriesCredential) {
			t.Fatalf("the add-json door verdict for %q = %v; want the credential refusal", raw, err)
		}
	}
}

// TestProviderFormDefaultsExcludeModelsRow pins the P4-5 read-only row in the
// defaults block: excludeModels renders beside excludeFamilies with the hub
// route in its note, the block stays read-only (Enter moves on to Save and never
// opens a gate), and nothing is written.
func TestProviderFormDefaultsExcludeModelsRow(t *testing.T) {
	app, path := newProviderFormTestApp(t, providerFormFixtureReg(), "add", "")
	scr := providerFormScreenOf(t, app)

	last := scr.defList.Row(scr.defList.Len() - 1)
	if last.Label != "excludeModels" {
		t.Fatalf("last defaults row = %q; want excludeModels", last.Label)
	}
	if !strings.Contains(last.Note, "(absent: the built-in k3 default applies)") {
		t.Fatalf("absent-key note = %q; want the built-in default named", last.Note)
	}
	if !strings.Contains(last.Note, "edit in the hub: agy pro setup") {
		t.Fatalf("note = %q; want the hub route", last.Note)
	}
	if prev := scr.defList.Row(scr.defList.Len() - 2); prev.Label != "excludeFamilies" {
		t.Fatalf("row before excludeModels = %q; want excludeFamilies (the fields stay adjacent)", prev.Label)
	}

	before := providerFormDisk(t, path)
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab, KeyTab, KeyTab}}) // grid -> defaults
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnd, KeyEnter, KeyEnter}})
	if app.Modal() != nil {
		t.Fatalf("the read-only defaults block opened %T", app.Modal())
	}
	if got := providerFormDisk(t, path); string(got) != string(before) {
		t.Fatal("the read-only excludeModels row wrote providers.json")
	}
	out := app.Frame().String()
	for _, want := range []string{"excludeModels", "edit in the hub: agy pro setup"} {
		if !strings.Contains(out, want) {
			t.Fatalf("frame missing %q:\n%s", want, out)
		}
	}
}

// TestProviderFormDefaultsExcludeModelsShapes pins the two stored shapes the
// block must not collapse: a user-owned empty list and a populated list.
func TestProviderFormDefaultsExcludeModelsShapes(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    string
	}{
		{"owned empty list", []string{}, "(empty list: nothing is excluded)"},
		{"populated list", []string{"nw", "glm-5"}, "nw, glm-5"},
	}
	got := map[string]bool{}
	for _, tc := range cases {
		reg := providerFormFixtureReg()
		reg.Defaults.ExcludeModels = tc.entries
		app, _ := newProviderFormTestApp(t, reg, "add", "")
		scr := providerFormScreenOf(t, app)
		note := scr.defList.Row(scr.defList.Len() - 1).Note
		if !strings.Contains(note, tc.want) {
			t.Errorf("%s: note = %q; want %q", tc.name, note, tc.want)
		}
		if !strings.Contains(note, "edit in the hub: agy pro setup") {
			t.Errorf("%s: note = %q; want the hub route", tc.name, note)
		}
		got[tc.want] = true
	}
	if len(got) != 2 {
		t.Fatalf("the two stored shapes rendered %d distinct notes; the row collapses them", len(got))
	}
}
