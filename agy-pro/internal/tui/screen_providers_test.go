package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/testfixtures"
)

// providersFixture writes reg to a temp AGY_PRO_HOME and points the process
// at it. The live providers.json is never touched: EnvProviders is forced
// empty so DefaultPath resolves inside the temp home.
func providersFixture(t *testing.T, reg *providers.Registry) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv(providers.EnvProviders, "")
	path := filepath.Join(home, "providers.json")
	providersWrite(t, path, reg)
	return path
}

// providersWrite overwrites the temp-home providers.json in place. The delete
// re-verify pins call it between the modal opening and the y answer, which is
// the mid-modal change the answer path has to catch.
func providersWrite(t *testing.T, path string, reg *providers.Registry) {
	t.Helper()
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func twoProviderRegistry() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{
			Name: "gemini", BaseURL: "https://example.com", APIKeyEnv: "GEMINI_API_KEY",
			Models: []providers.Model{{ID: "gemini-2.5"}, {ID: "gemini-2.0"}},
		},
		{
			Name: "anthropic", BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY",
			Models: []providers.Model{{ID: "claude-sonnet"}},
		},
	}}
}

// providersTestCore builds a Core over the temp-home registry with a fake
// audit probe: no network, no live files, verdict controlled by the test.
func providersTestCore(status providers.KeyProbeStatus, detail string) *settings.Core {
	return settings.NewCore(settings.Deps{
		AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
			return []providers.KeyAudit{{
				Provider: reg.Providers[0].Name,
				Env:      reg.Providers[0].APIKeyEnv,
				Probe:    providers.KeyProbeResult{Status: status, Detail: detail},
			}}
		},
	})
}

func newProvidersApp(core *settings.Core, opened *[]string) *App {
	app := NewApp(160, 30, DirLTR)
	app.Push(NewProvidersScreen(core, func(name string, a *App) {
		*opened = append(*opened, name)
	}, func(string, string, *App) {}))
	return app
}

// newProvidersFormApp builds the app with a recording provider-form callback
// that pushes the REAL form screen (NewProviderFormScreen), so the Add/Edit
// wiring pins run against the sibling unit's constructor contract rather than
// a spy. mode and name record what this screen asked for; pushed is the screen
// the callback put on the stack.
func newProvidersFormApp(core *settings.Core, mode, name *string, pushed *Screen) *App {
	app := NewApp(160, 30, DirLTR)
	app.Push(NewProvidersScreen(core, func(string, *App) {}, func(m, n string, a *App) {
		*mode, *name = m, n
		s := NewProviderFormScreen(core, m, n)
		*pushed = s
		a.Push(s)
	}))
	return app
}

// selectProviderKeys returns the keystrokes that leave the sticky selection on
// provider index i without activating anything: the opening cursor sits on
// provider 1, and any key that reaches the screen re-asserts the selection of
// the row the cursor rests on, so a lone KeyUp selects provider 1 while one
// KeyDown per index reaches a deeper provider.
func selectProviderKeys(i int) []Key {
	keys := make([]Key, 0, i+1)
	for n := 0; n < i; n++ {
		keys = append(keys, KeyDown)
	}
	if i == 0 {
		keys = append(keys, KeyUp)
	}
	return keys
}

func TestProvidersRowsRenderWithCounts(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	t.Setenv("GEMINI_API_KEY", "g-test-key-value") // resolved; the VALUE must never render
	core := providersTestCore(providers.ProbeSkipped, "no API key resolved; nothing to probe")
	var opened []string
	app := newProvidersApp(core, &opened)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlC}})

	for _, want := range []string{
		"Providers",
		"gemini",
		"anthropic",
		"2 models",
		"1 model",
		"GEMINI_API_KEY (OK)",
		"ANTHROPIC_API_KEY (unset)",
		providersActionTestAll, // "Test all"
		providersFormAdd,       // "Add provider"
		// The inverted layout (user ask U2): providers 1..2, then Add, Test all,
		// and the three sticky-target verbs, one row each. The verb rows carry no
		// provider name, so the row set no longer grows with the registry.
		"[ 5] Test",
		"[ 6] Edit",
		"[ 7] Delete",
		"digits 1-9 select+activate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("frame missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Test: ") || strings.Contains(out, "Edit: ") || strings.Contains(out, "Delete: ") {
		t.Fatalf("a per-provider verb row survived the inversion\n%s", out)
	}
	if strings.Contains(out, "● ") {
		t.Fatalf("the opening frame marks a provider before the user selected one\n%s", out)
	}
	if strings.Contains(out, "g-test-key-value") {
		t.Fatal("key VALUE leaked into the frame")
	}
	if len(opened) != 0 {
		t.Fatalf("render-only run opened models: %v", opened)
	}
}

func TestProvidersDualNavigationOpensSameProvider(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeValid, "dest example.com: credential accepted")

	cases := []struct {
		name string
		keys []Key
		want string
	}{
		{"digit 1", []Key{Digit(1)}, "gemini"},
		{"digit 2", []Key{Digit(2)}, "anthropic"},
		{"arrow+enter", []Key{KeyDown, KeyEnter}, "anthropic"},
		{"home+enter", []Key{KeyHome, KeyEnter}, "gemini"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opened []string
			app := newProvidersApp(core, &opened)
			app.CaptureRun(&ScriptedKeys{Keys: tc.keys})
			if strings.Join(opened, ",") != tc.want {
				t.Fatalf("opened %v, want [%s]", opened, tc.want)
			}
		})
	}
}

func TestProvidersTestActionRendersMaskedResult(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	rawToken := "sk-abcdefghijklmnop1234"
	core := providersTestCore(providers.ProbeInvalid, "upstream rejected token "+rawToken)

	var opened []string
	app := newProvidersApp(core, &opened)
	// Rows: 1 gemini, 2 anthropic, 3 Add, 4 Test all, 5 Test, 6 Edit, 7 Delete.
	// The Test row acts on the sticky-selected provider, so the run rests the
	// cursor on provider 1 before firing the verb.
	out := app.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(0), Digit(5), KeyLeft)})

	if !strings.Contains(out, "test failed") {
		t.Fatalf("no failure verdict on the message line\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("key-shaped operand was not masked\n%s", out)
	}
	if strings.Contains(out, rawToken) {
		t.Fatal("raw key-shaped token leaked into the frame")
	}
	if !strings.Contains(out, "gemini") {
		t.Fatal("verdict lost the provider name")
	}

	// Success path: the OK verdict renders on the message line.
	okCore := providersTestCore(providers.ProbeValid, "dest example.com: credential accepted")
	var opened2 []string
	app2 := newProvidersApp(okCore, &opened2)
	out2 := app2.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(0), Digit(5), KeyLeft)})
	if !strings.Contains(out2, "gemini: connection OK") {
		t.Fatalf("no success verdict on the message line\n%s", out2)
	}
}

func TestProvidersDeleteApprovalAndConfirm(t *testing.T) {
	path := providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeValid, "dest example.com: credential accepted")
	var opened []string
	app := newProvidersApp(core, &opened)

	// Rows: 1 gemini, 2 anthropic, 3 Add, 4 Test all, 5 Test, 6 Edit, 7 Delete.
	// The Delete row acts on the sticky-selected provider, so the run rests the
	// cursor on provider 1 first. y approves the write; KeyLeft forces one more
	// frame so the post-delete list and message are captured.
	out := app.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(0), Digit(7), KeyYes, KeyLeft)})

	for _, want := range []string{
		"Delete Provider?",
		"Removes the registry entry for gemini only",
		"Models before: 2, after: 0",
		"providers.json will be written (0600, atomic)",
		"- provider gemini (https://example.com, 2 models)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("approval summary missing %q\n%s", want, out)
		}
	}
	if app.Message() != "Provider gemini deleted." {
		t.Fatalf("post-delete message = %q", app.Message())
	}
	if len(opened) != 0 {
		t.Fatalf("delete run opened models: %v", opened)
	}

	// Sibling state: anthropic and its model survive on disk; gemini is gone.
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload temp registry: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "anthropic" {
		t.Fatalf("sibling state wrong: %+v", reg.Providers)
	}
	if len(reg.Providers[0].Models) != 1 || reg.Providers[0].Models[0].ID != "claude-sonnet" {
		t.Fatalf("sibling models wrong: %+v", reg.Providers[0].Models)
	}
}

func TestProvidersDeleteCancelWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  Key
	}{
		{"n", KeyNo},
		{"esc", KeyEsc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := providersFixture(t, twoProviderRegistry())
			core := providersTestCore(providers.ProbeValid, "ok")
			var opened []string
			app := newProvidersApp(core, &opened)

			out := app.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(0), Digit(7), tc.key, KeyLeft)})

			if app.Message() != providersDeleteCanceled {
				t.Fatalf("cancel message = %q", app.Message())
			}
			if !strings.Contains(out, "gemini") || !strings.Contains(out, "anthropic") {
				t.Fatalf("cancel run lost provider rows\n%s", out)
			}
			reg, err := providers.LoadFrom(path)
			if err != nil {
				t.Fatalf("reload temp registry: %v", err)
			}
			if len(reg.Providers) != 2 {
				t.Fatalf("cancel wrote to providers.json: %+v", reg.Providers)
			}
		})
	}
}

// TestProvidersDeleteApprovalMasksBaseURLUserinfo pins the M4 delete-approval
// diff against the SH-3 finding: a BaseURL carrying credential material in
// the userinfo slot must never render the credential, no matter its shape.
func TestProvidersDeleteApprovalMasksBaseURLUserinfo(t *testing.T) {
	awsKey := testfixtures.AWSSecretExample
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name: "leaky", BaseURL: "https://user:hunter2sekret@" + "gw.example/v1", APIKeyEnv: "LEAKY_API_KEY",
			Models: []providers.Model{{ID: "m1"}},
		},
		{
			Name: "awsish", BaseURL: "https://" + awsKey + "@gateway.example/v1", APIKeyEnv: "AWSISH_API_KEY",
			Models: []providers.Model{{ID: "m2"}},
		},
	}}
	providersFixture(t, reg)
	core := providersTestCore(providers.ProbeValid, "ok")

	cases := []struct {
		name     string
		keys     []Key // selection, then Delete (row 7 of 7: 2 providers + Add + Test all + 3 verbs)
		keep     string
		mustHide []string
	}{
		{"short password", append(selectProviderKeys(0), Digit(7)), "gw.example", []string{"user:hunter2sekret", "hunter2sekret"}},
		// Provider 2 is one arrow away; the verb row is the same fixed Delete row,
		// so the target is the marker, not the row position.
		{"aws-shaped userinfo", append(selectProviderKeys(1), Digit(7)), "gateway.example", []string{awsKey}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opened []string
			app := newProvidersApp(core, &opened)
			// The rows open the modal; Esc cancels so nothing is written.
			keys := append(append([]Key{}, tc.keys...), KeyEsc, KeyLeft)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})

			if !strings.Contains(out, tc.keep) {
				t.Fatalf("approval diff lost the endpoint host %q\n%s", tc.keep, out)
			}
			for _, secret := range tc.mustHide {
				if strings.Contains(out, secret) {
					t.Fatalf("credential material %q rendered in the approval frame\n%s", secret, out)
				}
			}
			if len(opened) != 0 {
				t.Fatalf("delete approval opened models: %v", opened)
			}
		})
	}
}

// TestMaskBaseURLQueryAndFragmentMasking pins the round-2 re-review R3/R5
// fix: the screen-local maskBaseURL (and the shared providers.MaskBaseURL)
// must mask a credential planted in the query or fragment slot of a
// well-formed BaseURL, not only the userinfo slot, while benign queries
// ("?q={query}") stay byte-identical. The providers.MaskBaseURL assertion on
// every case is the parity pin across the package boundary: the two maskers
// must agree on every input.
func TestMaskBaseURLQueryAndFragmentMasking(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"benign host", "https://example.com", "https://example.com"},
		{"benign query byte-identical", "https://example.com/v1?q={query}", "https://example.com/v1?q={query}"},
		{"benign multi-param query byte-identical", "https://example.com/v1?api-version=2&region=us-east-1", "https://example.com/v1?api-version=2&region=us-east-1"},
		{"userinfo dropped, host kept", "https://user:hunter2sekret@gw.example/v1", "https://gw.example/v1"},
		{"query api key masked, endpoint kept", "https://example.com/v1?api_key=ABCDEFGHIJKLMNOPQRST", "https://example.com/v1?api_key=ABCDE%2A%2A%2A%2A"},
		{"sk-shaped query value masked", "https://gw.example/v1?key=sk-abcdefghijklmnop1234", "https://gw.example/v1?key=[REDACTED]"},
		{"fragment credential masked", "https://example.com/v1#ABCDEFGHIJKLMNOPQRST", "https://example.com/v1#ABCDE****"},
		{"userinfo and query secret both handled", "https://user:hunter2sekret@gw.example/v1?api_key=ABCDEFGHIJKLMNOPQRST", "https://gw.example/v1?api_key=ABCDE%2A%2A%2A%2A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskBaseURL(tc.in); got != tc.want {
				t.Errorf("maskBaseURL(%q) = %q; want %q", tc.in, got, tc.want)
			}
			if got := providers.MaskBaseURL(tc.in); got != tc.want {
				t.Errorf("providers.MaskBaseURL(%q) = %q; want %q (tui/providers masker drift)", tc.in, got, tc.want)
			}
		})
	}
	// The planted credential never survives at any slot.
	for _, in := range []string{
		"https://example.com/v1?api_key=ABCDEFGHIJKLMNOPQRST",
		"https://gw.example/v1?key=sk-abcdefghijklmnop1234",
		"https://example.com/v1#ABCDEFGHIJKLMNOPQRST",
	} {
		if got := maskBaseURL(in); strings.Contains(got, "ABCDEFGHIJKLMNOPQRST") || strings.Contains(got, "sk-abcdefghijklmnop1234") {
			t.Errorf("maskBaseURL(%q) = %q; credential material survived", in, got)
		}
	}
}

// TestMaskBaseURLLastResortUsesSlotMasker pins the screen-local masker's last
// resort onto the shared slot masker. The arm runs when a value carries "@" but
// url.Parse cannot isolate a host slot, and it used to end on the bare
// providers.MaskRegistryValue: the authority rewrites a location-shaped value to
// its marker-stripped core, so a look-alike-decorated credential inside that
// rewrite came back readable. providers.MaskSlot classifies the rewrite and
// masks it; a benign parameter keeps the endpoint readable and no mask fires.
func TestMaskBaseURLLastResortUsesSlotMasker(t *testing.T) {
	const core = "ab3def7gh9jk1mnpqr5t"
	plant := "\u2025/" + core
	if got := providers.MaskOperand(plant); got != plant {
		t.Fatalf("premise broken: MaskOperand is not identity on the plant (%q)", got)
	}

	// The "@" sits in the query, so the userinfo cut cannot apply and the last
	// resort arm runs.
	in := "https://example.com/v1?q=" + plant + "@x"
	if got := providers.MaskRegistryValue(in); !strings.Contains(got, core) {
		t.Fatalf("premise broken: the bare authority already clears the plant (%q)", got)
	}
	got := maskBaseURL(in)
	if strings.Contains(got, core) || strings.Contains(got, plant) {
		t.Errorf("maskBaseURL(%q) = %q leaks the credential core", in, got)
	}
	if got != providers.MaskSlot(in) {
		t.Errorf("maskBaseURL(%q) = %q; want the shared slot masker's %q", in, got, providers.MaskSlot(in))
	}

	// A benign parameter reaches the same arm and stays readable (no mask, host
	// intact), and the two maskers still agree byte for byte.
	benign := "https://example.com/v1?q=acme@x"
	gotBenign := maskBaseURL(benign)
	if strings.Contains(gotBenign, "****") || !strings.Contains(gotBenign, "example.com/v1") {
		t.Errorf("maskBaseURL(%q) = %q; want the URL readable and unmasked", benign, gotBenign)
	}
	if gotBenign != providers.MaskBaseURL(benign) {
		t.Errorf("maskBaseURL(%q) = %q; want providers.MaskBaseURL parity (%q)", benign, gotBenign, providers.MaskBaseURL(benign))
	}
}

// TestProvidersRowIDsSurviveSeparatorNames pins the collision finding: a
// provider name that IS a fixed row ID or embeds the old separator must not
// shadow another row or mis-route an activation.
//
// Round-6 seat1-R8 update: the closed provider-name charset is enforced at
// load (Registry.Validate calls ValidateProviderName), so a name embedding
// the old ":" row separator ("delete:x") can no longer load from disk; the
// collision class is now closed at the registry boundary, which the last
// sub-test pins. The routing sub-tests keep their coverage with
// charset-legal names ("acme-corp", "x"): a delete/test verb must still hit
// exactly the sticky-selected provider, never the row position.
func TestProvidersRowIDsSurviveSeparatorNames(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "acme-corp", BaseURL: "https://a.example/v1", Models: []providers.Model{{ID: "m-a"}}},
		{Name: "x", BaseURL: "https://b.example/v1", Models: []providers.Model{{ID: "m-b"}}},
	}}
	providersFixture(t, reg)
	core := providersTestCore(providers.ProbeValid, "ok")

	// Row order: 1 provider acme-corp, 2 provider x, 3 Add, 4 Test all, 5 Test,
	// 6 Edit, 7 Delete. The three verbs act on the sticky selection, so each
	// sub-test selects its provider and then fires the same fixed row.
	t.Run("delete verb for x targets x", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(1), Digit(7), KeyEsc, KeyLeft)})
		if !strings.Contains(out, "Removes the registry entry for x only") {
			t.Fatalf("delete approval named the wrong target\n%s", out)
		}
		if strings.Contains(out, "Removes the registry entry for acme-corp only") {
			t.Fatalf("delete approval shadowed by the acme-corp provider\n%s", out)
		}
	})
	t.Run("delete verb for acme-corp targets acme-corp", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(0), Digit(7), KeyEsc, KeyLeft)})
		if !strings.Contains(out, "Removes the registry entry for acme-corp only") {
			t.Fatalf("delete approval named the wrong target\n%s", out)
		}
	})
	t.Run("test verb for acme-corp probes acme-corp", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: append(selectProviderKeys(0), Digit(5), KeyLeft)})
		if !strings.Contains(out, "acme-corp: connection OK") {
			t.Fatalf("test verdict named the wrong provider\n%s", out)
		}
		if len(opened) != 0 {
			t.Fatalf("test verb opened models: %v", opened)
		}
	})
	t.Run("separator name fails the load closed", func(t *testing.T) {
		bad := &providers.Registry{Providers: []providers.Provider{
			{Name: "delete:x", BaseURL: "https://a.example/v1", Models: []providers.Model{{ID: "m-a"}}},
		}}
		providersFixture(t, bad)
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
		if !strings.Contains(out, "Load failed") {
			t.Fatalf("separator-carrying registry must fail the load, got:\n%s", out)
		}
		if len(opened) != 0 {
			t.Fatalf("load-failed screen opened models: %v", opened)
		}
	})

	// A provider literally named "test-all" must not shadow the fixed
	// "Test all" row (row 1 is the provider, row 3 is the action, after the
	// Add row at 2).
	solo := &providers.Registry{Providers: []providers.Provider{
		{Name: "test-all", BaseURL: "https://solo.example/v1", Models: []providers.Model{{ID: "m-s"}}},
	}}
	providersFixture(t, solo)
	t.Run("provider named test-all opens models", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(1), KeyLeft}})
		if strings.Join(opened, ",") != "test-all" {
			t.Fatalf("provider row mis-routed: opened %v", opened)
		}
	})
	t.Run("fixed test-all row still probes", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(3), KeyLeft}})
		if !strings.Contains(out, "1/1 providers OK") {
			t.Fatalf("Test all did not run\n%s", out)
		}
		if len(opened) != 0 {
			t.Fatalf("Test all opened models: %v", opened)
		}
	})
}

// TestProvidersRowInventory pins the row order, IDs, and labels the screen
// composes after the inversion (user ask U2): the entity block first (one row
// per provider, so digit 1..N keep opening each provider's models screen), then
// the registry-level verbs ("Add provider", "Test all"), then the three fixed
// sticky-target verbs Test, Edit, and Delete. IDs follow the NUL-terminated
// kind-prefix scheme and every action row carries an EMPTY operand, so no
// provider name can shadow a verb row and the row count no longer grows with
// the registry. The sticky selection starts at -1, so no row carries the "●"
// marker here (the marker pin lives in TestProvidersStickySelectionMarksTarget).
func TestProvidersRowInventory(t *testing.T) {
	cases := []struct {
		name string
		reg  *providers.Registry
		want []Row
	}{
		{
			name: "two providers",
			reg:  twoProviderRegistry(),
			want: []Row{
				{ID: "provider\x00gemini", Label: "gemini"},
				{ID: "provider\x00anthropic", Label: "anthropic"},
				{ID: providersRowIDAdd, Label: providersFormAdd},
				{ID: providersRowIDTestAll, Label: providersActionTestAll},
				{ID: providersRowIDTest, Label: "Test"},
				{ID: providersRowIDEdit, Label: "Edit"},
				{ID: providersRowIDDelete, Label: "Delete"},
			},
		},
		{
			name: "empty registry offers the add path only",
			reg:  &providers.Registry{},
			want: []Row{{ID: providersRowIDAdd, Label: providersFormAdd}},
		},
		{
			name: "one provider",
			reg: &providers.Registry{Providers: []providers.Provider{{
				Name: "solo", BaseURL: "https://solo.example/v1", Models: []providers.Model{{ID: "m"}},
			}}},
			want: []Row{
				{ID: "provider\x00solo", Label: "solo"},
				{ID: providersRowIDAdd, Label: providersFormAdd},
				{ID: providersRowIDTestAll, Label: providersActionTestAll},
				{ID: providersRowIDTest, Label: "Test"},
				{ID: providersRowIDEdit, Label: "Edit"},
				{ID: providersRowIDDelete, Label: "Delete"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providersFixture(t, tc.reg)
			scr := NewProvidersScreen(providersTestCore(providers.ProbeSkipped, "no key"), func(string, *App) {}, func(string, string, *App) {}).(*ProvidersScreen)
			if got := scr.list.Len(); got != len(tc.want) {
				t.Fatalf("row count = %d, want %d", got, len(tc.want))
			}
			for i, want := range tc.want {
				got := scr.list.Row(i)
				if got.ID != want.ID {
					t.Errorf("row %d ID = %q, want %q", i+1, got.ID, want.ID)
				}
				if got.Label != want.Label {
					t.Errorf("row %d label = %q, want %q", i+1, got.Label, want.Label)
				}
			}
		})
	}
}

// TestProvidersAddRowOpensProviderForm pins the in-TUI add path (user decision
// 1A): activating the Add row pushes the REAL provider form screen
// (NewProviderFormScreen, the sibling unit's constructor) in "add" mode with an
// empty provider name. The callback here is the shell's wiring shape, so the
// pin covers the mode/name contract this screen owns and the push itself.
func TestProvidersAddRowOpensProviderForm(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeSkipped, "no key")
	var modeArg, nameArg string
	var pushed Screen
	app := newProvidersFormApp(core, &modeArg, &nameArg, &pushed)

	// Rows: 1 gemini, 2 anthropic, 3 Add.
	app.Step(Digit(3))

	if modeArg != "add" || nameArg != "" {
		t.Fatalf("add row asked for mode %q name %q, want \"add\" and an empty name", modeArg, nameArg)
	}
	if pushed == nil {
		t.Fatal("the add row pushed no screen")
	}
	top := app.Top()
	if _, ok := top.(*ProvidersScreen); ok {
		t.Fatalf("the add row left the providers list on top; depth %d", app.Depth())
	}
	if top == nil || top.Title() != pushed.Title() {
		t.Fatalf("the pushed provider form is not the top screen; top = %T", top)
	}
	lines := app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return l == pushed.Title() }) {
		t.Fatalf("the pushed form's title %q is not on the frame:\n%s", pushed.Title(), strings.Join(lines, "\n"))
	}
}

// TestProvidersEditActionOpensProviderForm pins the sticky-target Edit row: it
// opens the provider form in "edit" mode naming EXACTLY the selected provider,
// by the raw registry name (the form addresses the record by name, so a masked
// operand would not resolve). With two providers the Edit row is row 6 and each
// selection is asserted, so the verb cannot silently hit provider[0] or the row
// position instead of the marked target.
func TestProvidersEditActionOpensProviderForm(t *testing.T) {
	cases := []struct {
		name string
		keys []Key
		want string
	}{
		{"selecting gemini edits gemini", append(selectProviderKeys(0), Digit(6)), "gemini"},
		{"selecting anthropic edits anthropic", append(selectProviderKeys(1), Digit(6)), "anthropic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providersFixture(t, twoProviderRegistry())
			core := providersTestCore(providers.ProbeSkipped, "no key")
			var modeArg, nameArg string
			var pushed Screen
			app := newProvidersFormApp(core, &modeArg, &nameArg, &pushed)

			for _, k := range tc.keys {
				app.Step(k)
			}

			if modeArg != "edit" || nameArg != tc.want {
				t.Fatalf("edit verb asked for mode %q name %q, want \"edit\" and %q", modeArg, nameArg, tc.want)
			}
			top := app.Top()
			if _, ok := top.(*ProvidersScreen); ok {
				t.Fatalf("the edit verb left the providers list on top; depth %d", app.Depth())
			}
			if pushed == nil || top == nil || top.Title() != pushed.Title() {
				t.Fatalf("the pushed provider form is not the top screen; top = %T", top)
			}
		})
	}
}

// TestProvidersStickySelectionMarksTarget pins the visible target of the
// sticky-target verbs (user ask U2): the selection follows the cursor while it
// rests on a provider row, the selected row carries the "●" marker while its
// siblings do not, and the selection survives the move onto an action row, so
// the object a verb will act on is never invisible (the models screen's
// audit-F9 marker rule).
func TestProvidersStickySelectionMarksTarget(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeValid, "dest example.com: credential accepted")
	var opened []string
	app := newProvidersApp(core, &opened)

	lines := app.Frame().Lines
	if frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● ") }) {
		t.Fatalf("the opening frame marks a provider before any key:\n%s", strings.Join(lines, "\n"))
	}

	app.Step(KeyDown) // rest the cursor on provider 2
	lines = app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
		t.Fatalf("provider 2 is not marked after the cursor moved onto it:\n%s", strings.Join(lines, "\n"))
	}
	if frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● gemini") }) {
		t.Fatalf("provider 1 stayed marked after the selection moved:\n%s", strings.Join(lines, "\n"))
	}

	// The verb acts on the marked provider even though the cursor has left the
	// provider block for the verb row.
	app.Step(Digit(5))
	if got := app.Message(); !strings.Contains(got, "anthropic") {
		t.Fatalf("the test verdict = %q, want the marked provider anthropic", got)
	}
	lines = app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
		t.Fatalf("the marker vanished when the cursor moved to the verb row:\n%s", strings.Join(lines, "\n"))
	}

	// Walking the cursor back to provider 1 moves the marker back with it.
	for i := 0; i < 4; i++ {
		app.Step(KeyUp)
	}
	lines = app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● gemini") }) {
		t.Fatalf("provider 1 is not marked after the cursor returned to it:\n%s", strings.Join(lines, "\n"))
	}
}

// TestProvidersActionWithoutSelectionRefuses pins the refusal half of the
// sticky-target contract (user ask U2): entering the screen leaves no provider
// selected (the models screen's sel = -1 rule, user decision 2A), so firing Test,
// Edit, or Delete before resting the cursor on a provider row refuses with the
// named message instead of silently targeting provider[0]. The refusal is inert:
// Delete opens no approval gate, Edit pushes no form, and Test never reaches
// Core.ProviderTest, so no probe or registry write can happen on a guess. The
// positive control re-fires the same verb row after marking a provider.
func TestProvidersActionWithoutSelectionRefuses(t *testing.T) {
	refusal := Bundle().T("providers.msg.select_provider")
	if refusal != providersMsgSelectProvider {
		t.Fatalf("precondition: the en catalog entry = %q, want the fallback const %q", refusal, providersMsgSelectProvider)
	}
	cases := []struct {
		name string
		row  Key
	}{
		{"Test", Digit(5)},
		{"Edit", Digit(6)},
		{"Delete", Digit(7)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			providersFixture(t, twoProviderRegistry())
			probes := 0
			core := settings.NewCore(settings.Deps{
				AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
					probes++
					return []providers.KeyAudit{{
						Provider: reg.Providers[0].Name,
						Env:      reg.Providers[0].APIKeyEnv,
						Probe:    providers.KeyProbeResult{Status: providers.ProbeValid},
					}}
				},
			})
			var forms, opened []string
			app := NewApp(160, 30, DirLTR)
			app.Push(NewProvidersScreen(core, func(name string, a *App) {
				opened = append(opened, name)
			}, func(modeName, providerName string, a *App) {
				forms = append(forms, modeName+":"+providerName)
			}))

			app.Step(tc.row)

			if got := app.Message(); got != refusal {
				t.Fatalf("%s with no selection = %q, want the refusal %q", tc.name, got, refusal)
			}
			if app.Modal() != nil {
				t.Fatalf("%s with no selection opened a modal", tc.name)
			}
			if len(forms) != 0 {
				t.Fatalf("%s with no selection pushed the provider form: %v", tc.name, forms)
			}
			if probes != 0 {
				t.Fatalf("%s with no selection reached the probe: %d call(s)", tc.name, probes)
			}
			if len(opened) != 0 {
				t.Fatalf("%s with no selection opened models: %v", tc.name, opened)
			}
			if lines := app.Frame().Lines; !frameLineMatches(lines, func(l string) bool { return l == refusal }) {
				t.Fatalf("the refusal line is not on the frame:\n%s", strings.Join(lines, "\n"))
			}

			// Positive control: the same verb row acts once a provider is marked.
			// KeyHome is used instead of selectProviderKeys because the refusal
			// left the cursor on the verb row, not on the provider block.
			for _, k := range []Key{KeyHome, tc.row} {
				app.Step(k)
			}
			switch tc.name {
			case "Test":
				if probes != 1 {
					t.Fatalf("the marked target did not reach the probe: %d call(s)", probes)
				}
				if got := app.Message(); !strings.Contains(got, "gemini") {
					t.Fatalf("the marked target's verdict = %q, want gemini", got)
				}
			case "Edit":
				if len(forms) != 1 || forms[0] != "edit:gemini" {
					t.Fatalf("the marked target asked the form for %v, want [edit:gemini]", forms)
				}
			case "Delete":
				if app.Modal() == nil {
					t.Fatal("the marked target did not open the approval gate")
				}
			}
		})
	}
}

// TestProvidersEmptyStateNamesInTUIAddPath pins the honest empty state. The
// catalog notice alone ("No providers registered yet.") named no way forward on
// this screen, so the composed line now points at the Add row, the Add row
// itself renders under it (the pointer and its target are never separated), and
// digit 1 reaches it in add mode.
func TestProvidersEmptyStateNamesInTUIAddPath(t *testing.T) {
	providersFixture(t, &providers.Registry{})
	core := providersTestCore(providers.ProbeSkipped, "no key")
	var modeArg, nameArg string
	var pushed Screen
	app := newProvidersFormApp(core, &modeArg, &nameArg, &pushed)

	lines := app.Frame().Lines
	joined := strings.Join(lines, "\n")
	if !frameLineMatches(lines, func(l string) bool { return l == providersEmptyLine(Bundle()) }) {
		t.Fatalf("the composed empty line is not on the frame:\n%s", joined)
	}
	if !strings.Contains(joined, providersFormAdd) {
		t.Fatalf("the empty state does not name the in-TUI add path %q:\n%s", providersFormAdd, joined)
	}
	if !strings.Contains(joined, Bundle().T("providers.empty")) {
		t.Fatalf("the empty state lost the catalog's empty notice:\n%s", joined)
	}

	// The Add row is the only row, so digit 1 reaches it.
	app.Step(Digit(1))
	if modeArg != "add" || nameArg != "" {
		t.Fatalf("the empty state's add row asked for mode %q name %q", modeArg, nameArg)
	}
	if pushed == nil {
		t.Fatal("the empty state's add row pushed no screen")
	}
}

// TestProvidersEmptyLineResolvesEmptyHintThroughCatalog pins the empty-hint
// lookup. The sentence resolves providers.form.empty_hint by KEY, so a
// non-English catalog renders its own translation and the English const is
// only the fallback. The pre-fix call handed the fallback TEXT to Tf, which is
// a literal-name lookup, so the catalog entry was unreachable and ko/ja/de all
// printed the English sentence while the catalogs carried translations. The en
// case must stay byte-identical to the const composition (no visible change
// for English), and every probed locale must both differ from it and name the
// row by its own translated add label.
func TestProvidersEmptyLineResolvesEmptyHintThroughCatalog(t *testing.T) {
	en := i18n.Load("en")
	const englishHint = `Use the "Add provider" row below to register one.`
	if got := fillHubSlots(providersFormEmptyHint, map[string]string{"action": providersFormAdd}); got != englishHint {
		t.Fatalf("the fallback const composition = %q, want %q", got, englishHint)
	}
	englishLine := en.T("providers.empty") + " " + englishHint
	if got := providersEmptyLine(en); got != englishLine {
		t.Fatalf("en empty line = %q, want the fallback composition %q", got, englishLine)
	}

	for _, lang := range []string{"ko", "ja", "de"} {
		t.Run(lang, func(t *testing.T) {
			b := i18n.Load(lang)
			action := b.T("providers.form.add")
			if action == providersFormAdd {
				t.Fatalf("%s: precondition: the add row label is still the English const", lang)
			}
			want := b.T("providers.empty") + " " +
				fillHubSlots(b.T("providers.form.empty_hint"), map[string]string{"action": action})
			got := providersEmptyLine(b)
			if got != want {
				t.Fatalf("%s empty line = %q, want the translated composition %q", lang, got, want)
			}
			if got == englishLine {
				t.Fatalf("%s empty line rendered the English fallback: %q", lang, got)
			}
			if !strings.Contains(got, action) {
				t.Fatalf("%s empty line does not name the translated add row %q: %q", lang, action, got)
			}
			for _, leak := range []string{"row below to register one", "{{action}}", "providers.form.empty_hint"} {
				if strings.Contains(got, leak) {
					t.Fatalf("%s empty line leaks %q: %q", lang, leak, got)
				}
			}
		})
	}

	// The screen binds the active bundle at construction, so the Draw path must
	// render the same translated line on a live frame.
	t.Run("draw", func(t *testing.T) {
		providersFixture(t, &providers.Registry{})
		SetLanguage("ko")
		defer SetLanguage("en")
		ko := i18n.Load("ko")
		want := providersEmptyLine(ko)
		if want == englishLine {
			t.Fatalf("precondition: the ko empty line is still the English fallback: %q", want)
		}
		app := NewApp(200, 24, DirLTR)
		app.Push(NewProvidersScreen(providersTestCore(providers.ProbeSkipped, "no key"),
			func(string, *App) {}, func(string, string, *App) {}))
		lines := app.Frame().Lines
		if !frameLineMatches(lines, func(l string) bool { return l == want }) {
			t.Fatalf("the ko empty line is not on the frame:\n%s", strings.Join(lines, "\n"))
		}
		if frameLineMatches(lines, func(l string) bool { return l == englishLine }) {
			t.Fatalf("the frame still carries the English empty hint:\n%s", strings.Join(lines, "\n"))
		}
	})
}

// TestProvidersRowsRefreshAfterAFormWrite pins the Draw-time registry refresh.
// The provider form pushes ABOVE this screen, and App.Pop does not re-run the
// constructor, so without a re-read the list would keep painting the snapshot
// it was built from and an approved add would stay invisible until the operator
// re-entered the screen. The pin mutates providers.json behind the screen (the
// observable effect of an approved form write) and asserts the next frame, then
// that an unreadable registry fails closed (no rows, so no verb can write) and
// a repaired one clears the error line again.
func TestProvidersRowsRefreshAfterAFormWrite(t *testing.T) {
	path := providersFixture(t, &providers.Registry{})
	app := NewApp(80, 24, DirLTR)
	app.Push(NewProvidersScreen(providersTestCore(providers.ProbeSkipped, "no key"), func(string, *App) {}, func(string, string, *App) {}))

	out := app.Frame().String()
	if !strings.Contains(out, providersFormAdd) || strings.Contains(out, "gemini") {
		t.Fatalf("precondition: the empty frame does not show the add-only state:\n%s", out)
	}

	providersWrite(t, path, twoProviderRegistry())
	out = app.Frame().String()
	for _, want := range []string{"gemini", "anthropic", "[ 5] Test", "[ 6] Edit", "[ 7] Delete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the frame did not pick up the registry change (%q missing):\n%s", want, out)
		}
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt providers.json: %v", err)
	}
	lines := app.Frame().Lines
	out = strings.Join(lines, "\n")
	if !strings.Contains(out, "Load failed") {
		t.Fatalf("the corrupt registry did not surface its error line:\n%s", out)
	}
	if frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[ 1]") }) {
		t.Fatalf("a row stayed live while the registry could not be read:\n%s", out)
	}

	providersWrite(t, path, twoProviderRegistry())
	out = app.Frame().String()
	if strings.Contains(out, "Load failed") || !strings.Contains(out, "gemini") {
		t.Fatalf("the repaired registry did not clear the error line:\n%s", out)
	}
}

// manyProviderRegistry builds n single-model providers, so the composed row
// set (n provider rows plus the five fixed action rows, so n+5 rows) is long
// enough to page on an 80-wide frame. Names past the spelled list are numbered
// ("p08", "p09", ...), so n can exceed seven after the row-set inversion made
// the fixed block count constant (user ask U2).
func manyProviderRegistry(n int) *providers.Registry {
	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"}
	reg := &providers.Registry{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("p%02d", i+1)
		if i < len(names) {
			name = names[i]
		}
		reg.Providers = append(reg.Providers, providers.Provider{
			Name:    name,
			BaseURL: "https://" + name + ".example.com",
			Models:  []providers.Model{{ID: name + "-1"}},
		})
	}
	return reg
}

// TestProvidersWindowKeepsChromeAt80x24 pins the F7 fix on the audit's
// original repro screen: the list window is derived from the frame height
// through pageSizeFor, so a full-page list at 80x24 keeps the list hint and
// the shared base footer instead of pushing them off the frame. The window's
// last row stays reachable through End. The fixture carries 20 providers, so
// the row set (20 + 5 fixed rows = 25) still overflows the 18-row window after
// the inversion made the fixed block constant.
func TestProvidersWindowKeepsChromeAt80x24(t *testing.T) {
	providersFixture(t, manyProviderRegistry(20))
	scr := NewProvidersScreen(providersTestCore(providers.ProbeSkipped, "no key"), func(string, *App) {}, func(string, string, *App) {}).(*ProvidersScreen)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)

	// 24 rows minus the body shape's chrome (title, both section separators,
	// base footer, the blank and the list hint) leaves 18 list rows.
	lines := app.Frame().Lines
	if got := scr.list.PageSize(); got != 18 {
		t.Fatalf("list page size = %d, want 18", got)
	}
	if scr.list.Len() <= scr.list.PageSize() {
		t.Fatalf("fixture does not overflow the window: %d rows, page %d", scr.list.Len(), scr.list.PageSize())
	}
	if scr.list.Len() != 25 {
		t.Fatalf("row inventory = %d rows, want 25 (20 providers + Add + Test all + Test + Edit + Delete)", scr.list.Len())
	}
	if len(lines) != 24 {
		t.Fatalf("frame = %d lines, want 24:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != baseFooter {
		t.Fatalf("last frame line = %q, want the base footer", lines[len(lines)-1])
	}
	if !frameLineMatches(lines, func(l string) bool { return l == scr.list.FooterHint() }) {
		t.Fatalf("list hint missing from the frame:\n%s", strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[18]") }) {
		t.Fatalf("the window's last row [18] is not on the frame:\n%s", strings.Join(lines, "\n"))
	}

	// End pages the tail into the window; the chrome is unchanged.
	app.Step(KeyEnd)
	lines = app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[25]") }) {
		t.Fatalf("the row set's last row [25] is unreachable:\n%s", strings.Join(lines, "\n"))
	}
	if len(lines) != 24 || lines[len(lines)-1] != baseFooter {
		t.Fatalf("paged frame = %d lines, last %q; want 24 and the base footer", len(lines), lines[len(lines)-1])
	}
}

// TestProvidersNoticeLinesClampToWidth pins the width cap on the two one-off
// lines this screen renders in place of the list: an over-wide load error and
// an over-wide empty-state line both compose to at most the frame width.
func TestProvidersNoticeLinesClampToWidth(t *testing.T) {
	t.Run("load error", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(providers.EnvHome, home)
		// A long file name makes the load error wider than an 80-cell frame.
		path := filepath.Join(home, strings.Repeat("x", 40)+".json")
		t.Setenv(providers.EnvProviders, path)
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("seed corrupt providers.json: %v", err)
		}
		_, loadErr := providers.Load()
		if loadErr == nil {
			t.Fatal("precondition: the corrupt file must fail the load")
		}
		want := Bundle().Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(loadErr.Error())})
		if cellwidth.String(want) <= 80 {
			t.Fatalf("precondition: the error line is only %d cells, the pin needs more than 80", cellwidth.String(want))
		}
		app := NewApp(80, 24, DirLTR)
		app.Push(NewProvidersScreen(nil, func(string, *App) {}, func(string, string, *App) {}))
		lines := app.Frame().Lines
		clamped := cellwidth.Truncate(want, 80)
		if !frameLineMatches(lines, func(l string) bool { return l == clamped }) {
			t.Fatalf("the clamped load-error line is not on the frame:\n%s", strings.Join(lines, "\n"))
		}
		for _, l := range lines {
			if cw := cellwidth.String(l); cw > 80 {
				t.Fatalf("frame line %q is %d cells, over the 80-cell frame", l, cw)
			}
		}
	})

	t.Run("empty registry", func(t *testing.T) {
		providersFixture(t, &providers.Registry{})
		// The empty line now carries the pointer at the in-TUI add path, so the
		// composition (not the bare catalog notice) is what must be clamped.
		empty := providersEmptyLine(Bundle())
		if cellwidth.String(empty) <= 20 {
			t.Fatalf("precondition: the empty line is only %d cells, the pin needs more than 20", cellwidth.String(empty))
		}
		app := NewApp(20, 24, DirLTR)
		app.Push(NewProvidersScreen(nil, func(string, *App) {}, func(string, string, *App) {}))
		lines := app.Frame().Lines
		clamped := cellwidth.Truncate(empty, 20)
		if !frameLineMatches(lines, func(l string) bool { return l == clamped }) {
			t.Fatalf("the clamped empty-state line is not on the frame:\n%s", strings.Join(lines, "\n"))
		}
		if cellwidth.String(clamped) > 20 {
			t.Fatalf("empty-state line = %d cells, over the 20-cell frame", cellwidth.String(clamped))
		}
		for _, l := range lines {
			if cw := cellwidth.String(l); cw > 20 {
				t.Fatalf("frame line %q is %d cells, over the 20-cell frame", l, cw)
			}
		}
	})
}

// TestProvidersWindowReservesMessageChromeAt80x24 pins the message-row
// accounting fix: the phase-3 window sized the list at pageSizeFor(height, 6)
// and left the live app-message block uncovered, so a message could push the
// list hint or the base footer off this screen's frame. The window now gives
// back the message's two rows (its separator plus the line) exactly like the
// presets screen, so the message frame keeps the hint, the message and the
// base footer inside 24 lines, while the message-free frame stays byte-stable
// at the phase-3 size (24 - 6 = 18 rows).
func TestProvidersWindowReservesMessageChromeAt80x24(t *testing.T) {
	providersFixture(t, manyProviderRegistry(20))
	scr := NewProvidersScreen(providersTestCore(providers.ProbeSkipped, "no key"), func(string, *App) {}, func(string, string, *App) {}).(*ProvidersScreen)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)

	// No message: the window is unchanged (chrome 6, 18 rows).
	lines := app.Frame().Lines
	if got := scr.list.PageSize(); got != 18 {
		t.Fatalf("message-free list page size = %d, want 18", got)
	}
	if len(lines) != 24 {
		t.Fatalf("message-free frame = %d lines, want 24:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != baseFooter {
		t.Fatalf("message-free last frame line = %q, want the base footer", lines[len(lines)-1])
	}

	// A live message: its two rows come out of the window (18 - 2 = 16).
	app.SetMessage("7/7 providers OK")
	lines = app.Frame().Lines
	if got := scr.list.PageSize(); got != 16 {
		t.Fatalf("message list page size = %d, want 16", got)
	}
	if len(lines) != 24 {
		t.Fatalf("message frame = %d lines, want 24:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != baseFooter {
		t.Fatalf("message frame lost the base footer (last line %q):\n%s", lines[len(lines)-1], strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return l == scr.list.FooterHint() }) {
		t.Fatalf("message frame lost the list hint:\n%s", strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return l == "7/7 providers OK" }) {
		t.Fatalf("message frame lost the message line:\n%s", strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[16]") }) {
		t.Fatalf("the window's last row [16] is not on the message frame:\n%s", strings.Join(lines, "\n"))
	}

	// The tail stays reachable with the message live, and the chrome holds.
	app.Step(KeyEnd)
	lines = app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[25]") }) {
		t.Fatalf("the row set's last row [25] is unreachable with a live message:\n%s", strings.Join(lines, "\n"))
	}
	if len(lines) != 24 {
		t.Fatalf("paged message frame = %d lines, want 24:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != baseFooter {
		t.Fatalf("paged message frame lost the base footer (last line %q)", lines[len(lines)-1])
	}
}

// providersRecordingCore is providersTestCore with a recording registry write:
// saveCalls counts the writes, so the refusal pins assert that no write
// happened at all, not merely that the file bytes still match.
func providersRecordingCore(saveCalls *int) *settings.Core {
	return settings.NewCore(settings.Deps{
		SaveReg: func(*providers.Registry) error {
			*saveCalls++
			return nil
		},
		AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
			return []providers.KeyAudit{{
				Provider: reg.Providers[0].Name,
				Env:      reg.Providers[0].APIKeyEnv,
				Probe:    providers.KeyProbeResult{Status: providers.ProbeSkipped, Detail: "no key"},
			}}
		},
	})
}

// anthropicOnlyRegistry is the two-provider fixture with gemini removed: the
// mid-modal removal the delete re-verify has to catch.
func anthropicOnlyRegistry() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{
			Name: "anthropic", BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY",
			Models: []providers.Model{{ID: "claude-sonnet"}},
		},
	}}
}

// TestProvidersDeleteReverifiesApprovedIdentity pins the shown-vs-written fix
// on the M4 delete answer path. The modal summarizes the screen's s.reg
// snapshot while Core.ProviderDelete re-loads providers.json and removes the
// FIRST case-insensitive name match, so a mid-modal change could delete a
// different entry than the operator approved. The approve path now re-reads
// the registry, refuses on any mismatch (fail-closed, no Core call, no write)
// with a named message, and writes only when the approved record is still the
// entry the verb would remove.
func TestProvidersDeleteReverifiesApprovedIdentity(t *testing.T) {
	cases := []struct {
		name string
		// mutate changes providers.json between the modal opening and the y.
		mutate func(t *testing.T, path string)
		// wantSaves is the registry write count the fake saver records.
		wantSaves int
		// wantMessage is the exact message line; wantContains covers the case
		// whose text carries the masked load detail (a temp path).
		wantMessage  string
		wantContains string
		// wantRowsDrop pins the post-refusal refresh: the list is rebuilt from
		// the file the answer path refused on, so the stale delete row is gone.
		wantRowsDrop bool
	}{
		{
			name:        "unchanged registry writes",
			mutate:      func(t *testing.T, path string) { providersWrite(t, path, twoProviderRegistry()) },
			wantSaves:   1,
			wantMessage: "Provider gemini deleted.",
		},
		{
			name:         "target gone refuses",
			mutate:       func(t *testing.T, path string) { providersWrite(t, path, anthropicOnlyRegistry()) },
			wantSaves:    0,
			wantMessage:  "Refused to delete gemini: the approved entry is no longer in providers.json. Nothing was written; reopen the delete to review the current entry.",
			wantRowsDrop: true,
		},
		{
			name: "record body changed refuses",
			mutate: func(t *testing.T, path string) {
				reg := twoProviderRegistry()
				reg.Providers[0].BaseURL = "https://gw.example/v1"
				providersWrite(t, path, reg)
			},
			wantSaves:   0,
			wantMessage: "Refused to delete gemini: the approved entry changed while the approval was open. Nothing was written; reopen the delete to review the current entry.",
		},
		{
			name: "model list changed refuses",
			mutate: func(t *testing.T, path string) {
				reg := twoProviderRegistry()
				reg.Providers[0].Models = reg.Providers[0].Models[:1]
				providersWrite(t, path, reg)
			},
			wantSaves:   0,
			wantMessage: "Refused to delete gemini: the approved entry changed while the approval was open. Nothing was written; reopen the delete to review the current entry.",
		},
		{
			name: "case duplicate sorts first refuses",
			mutate: func(t *testing.T, path string) {
				reg := twoProviderRegistry()
				dup := reg.Providers[0]
				dup.Name = "Gemini"
				reg.Providers = append([]providers.Provider{dup}, reg.Providers...)
				providersWrite(t, path, reg)
			},
			wantSaves:   0,
			wantMessage: "Refused to delete gemini: a differently-cased entry now matches this name first. Nothing was written; reopen the delete to review the current entry.",
		},
		{
			name: "unreadable registry refuses",
			mutate: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
					t.Fatalf("corrupt providers.json: %v", err)
				}
			},
			wantSaves:    0,
			wantContains: providersDeleteReasonUnreadable,
			wantRowsDrop: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := providersFixture(t, twoProviderRegistry())
			saves := 0
			var opened []string
			app := newProvidersApp(providersRecordingCore(&saves), &opened)

			// The Delete verb (row 7) opens the modal over the s.reg snapshot of
			// the sticky-selected provider (provider 1 = gemini).
			for _, k := range append(selectProviderKeys(0), Digit(7)) {
				app.Step(k)
			}
			if app.Modal() == nil {
				t.Fatal("precondition: the delete verb must open the M4 approval modal")
			}
			tc.mutate(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read the mutated registry: %v", err)
			}

			app.Step(KeyYes)
			if app.Modal() != nil {
				t.Fatal("the gate stayed open after the answer")
			}
			if saves != tc.wantSaves {
				t.Fatalf("registry writes = %d, want %d (message %q)", saves, tc.wantSaves, app.Message())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json after the answer: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("providers.json changed under the recording saver\nbefore: %s\nafter: %s", before, after)
			}
			if tc.wantMessage != "" && app.Message() != tc.wantMessage {
				t.Fatalf("message = %q, want %q", app.Message(), tc.wantMessage)
			}
			if tc.wantContains != "" && !strings.Contains(app.Message(), tc.wantContains) {
				t.Fatalf("message = %q, want it to carry %q", app.Message(), tc.wantContains)
			}
			if len(opened) != 0 {
				t.Fatalf("delete answer opened models: %v", opened)
			}
			if tc.wantRowsDrop {
				out := strings.Join(app.Frame().Lines, "\n")
				if frameLineMatches(app.Frame().Lines, func(l string) bool { return strings.Contains(l, "] gemini") }) {
					t.Fatalf("the refusal kept the stale provider row on the screen:\n%s", out)
				}
			}
		})
	}
}

// TestProvidersTestAllSummaryResolvesThroughCatalog pins the testAll summary
// lookup. The message resolves providers.test.all_summary by KEY through
// missingOr and fills the two count slots with fillHubSlots, so a non-English
// catalog renders its own summary. The pre-fix call handed the fallback TEXT to
// Tf, which is a literal-name lookup, so the catalog entry was unreachable in
// every locale. With en the message stays byte-identical to the const
// composition; ko/ja/de must both differ from it and still carry the count
// slots.
func TestProvidersTestAllSummaryResolvesThroughCatalog(t *testing.T) {
	// gemini probes valid and anthropic does not, so the two count slots carry
	// distinct values (1/2) and a lost slot cannot hide behind equal numbers.
	ok, total := 1, 2
	slots := map[string]string{"ok": strconv.Itoa(ok), "total": strconv.Itoa(total)}
	counts := slots["ok"] + "/" + slots["total"]
	englishMsg := fillHubSlots(providersTestAllSummary, slots)
	if englishMsg != "1/2 providers OK" {
		t.Fatalf("the fallback const composition = %q, want %q", englishMsg, "1/2 providers OK")
	}

	for _, lang := range []string{"en", "ko", "ja", "de"} {
		t.Run(lang, func(t *testing.T) {
			providersFixture(t, twoProviderRegistry())
			SetLanguage(lang)
			defer SetLanguage("en")
			b := i18n.Load(lang)

			// The first provider of the registry handed to AuditKeys is the one
			// being probed, so this fake passes gemini and fails anthropic.
			core := settings.NewCore(settings.Deps{
				AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
					status := providers.ProbeInvalid
					if len(reg.Providers) == 1 && reg.Providers[0].Name == "gemini" {
						status = providers.ProbeValid
					}
					return []providers.KeyAudit{{
						Provider: reg.Providers[0].Name,
						Env:      reg.Providers[0].APIKeyEnv,
						Probe:    providers.KeyProbeResult{Status: status, Detail: "dest example.com: credential rejected"},
					}}
				},
			})
			var opened []string
			app := newProvidersApp(core, &opened)
			// Row 4 is "Test all" (1 gemini, 2 anthropic, 3 Add, 4 Test all).
			app.Step(Digit(4))

			want := fillHubSlots(b.T("providers.test.all_summary"), slots)
			got := app.Message()
			if got != want {
				t.Fatalf("%s test-all message = %q, want the catalog composition %q", lang, got, want)
			}
			if !strings.Contains(got, counts) {
				t.Fatalf("%s test-all message lost the count slots %q: %q", lang, counts, got)
			}
			if lang == "en" {
				if got != englishMsg {
					t.Fatalf("en test-all message = %q, want the fallback composition %q", got, englishMsg)
				}
			} else if got == englishMsg {
				t.Fatalf("%s test-all message rendered the English fallback: %q", lang, got)
			}

			lines := app.Frame().Lines
			if !frameLineMatches(lines, func(l string) bool { return l == want }) {
				t.Fatalf("%s test-all summary is not on the frame:\n%s", lang, strings.Join(lines, "\n"))
			}
		})
	}
}

// TestProvidersDeleteMessagesResolveThroughCatalog pins both delete message
// lines. providers.delete.done is a slot template filled through fillHubSlots
// after a missingOr key lookup, and providers.delete.canceled is a slot-free
// sentence that used to render the raw const and never touched the bundle. Each
// non-English locale must print its own catalog entry (differing from the
// English const composition and keeping the provider name slot), and en must
// stay byte-identical to the const.
func TestProvidersDeleteMessagesResolveThroughCatalog(t *testing.T) {
	const name = "gemini"
	nameSlots := map[string]string{"name": name}
	englishDone := fillHubSlots(providersDeleteDone, nameSlots)
	if englishDone != "Provider gemini deleted." {
		t.Fatalf("the fallback const composition = %q, want %q", englishDone, "Provider gemini deleted.")
	}

	for _, lang := range []string{"en", "ko", "ja", "de"} {
		t.Run(lang, func(t *testing.T) {
			b := i18n.Load(lang)

			t.Run("done", func(t *testing.T) {
				providersFixture(t, twoProviderRegistry())
				SetLanguage(lang)
				defer SetLanguage("en")
				var opened []string
				app := newProvidersApp(providersTestCore(providers.ProbeValid, "ok"), &opened)
				for _, k := range append(selectProviderKeys(0), Digit(7), KeyYes) {
					app.Step(k)
				}

				want := fillHubSlots(b.T("providers.delete.done"), nameSlots)
				got := app.Message()
				if got != want {
					t.Fatalf("%s delete-done message = %q, want the catalog composition %q", lang, got, want)
				}
				if !strings.Contains(got, name) {
					t.Fatalf("%s delete-done message lost the provider slot: %q", lang, got)
				}
				if lang == "en" {
					if got != englishDone {
						t.Fatalf("en delete-done message = %q, want the fallback composition %q", got, englishDone)
					}
				} else if got == englishDone {
					t.Fatalf("%s delete-done message rendered the English fallback: %q", lang, got)
				}
			})

			t.Run("canceled", func(t *testing.T) {
				providersFixture(t, twoProviderRegistry())
				SetLanguage(lang)
				defer SetLanguage("en")
				var opened []string
				app := newProvidersApp(providersTestCore(providers.ProbeValid, "ok"), &opened)
				for _, k := range append(selectProviderKeys(0), Digit(7), KeyNo) {
					app.Step(k)
				}

				want := missingOr(b, "providers.delete.canceled", providersDeleteCanceled)
				got := app.Message()
				if got != want {
					t.Fatalf("%s delete-canceled message = %q, want the catalog composition %q", lang, got, want)
				}
				if lang == "en" {
					if got != providersDeleteCanceled {
						t.Fatalf("en delete-canceled message = %q, want the const %q", got, providersDeleteCanceled)
					}
				} else if got == providersDeleteCanceled {
					t.Fatalf("%s delete-canceled message rendered the raw English const: %q", lang, got)
				}
			})
		})
	}
}

// TestProvidersFallbackConstsResolveInAllLocales gates the const-block header
// on screen_providers.go. Every const that names a catalog key must resolve
// through that KEY in all twelve locale files: no locale may fall through to
// the key ID (the pre-fix symptom, since a fallback sentence used as a key finds
// nothing) and no locale may lean on the en fallback, and every slot the screen
// fills must survive in the localized template. The const text itself must stay
// byte-identical to the en catalog value, which is what "fallback kept" means.
func TestProvidersFallbackConstsResolveInAllLocales(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		text  string
		slots []string
	}{
		{"providersActionTestAll", "providers.action.test_all", providersActionTestAll, nil},
		{"providersTestAllSummary", "providers.test.all_summary", providersTestAllSummary, []string{"{{ok}}", "{{total}}"}},
		{"providersDeleteDone", "providers.delete.done", providersDeleteDone, []string{"{{name}}"}},
		{"providersDeleteCanceled", "providers.delete.canceled", providersDeleteCanceled, nil},
		{"providersFormAdd", "providers.form.add", providersFormAdd, nil},
		{"providersFormEdit", "providers.form.edit", providersFormEdit, nil},
		{"providersFormEmptyHint", "providers.form.empty_hint", providersFormEmptyHint, []string{"{{action}}"}},
		{"providersMsgSelectProvider", "providers.msg.select_provider", providersMsgSelectProvider, nil},
	}
	langs := i18n.Languages()
	if len(langs) != 12 {
		t.Fatalf("locale registry = %d codes, want 12", len(langs))
	}

	en := i18n.Load("en")
	for _, c := range cases {
		if got := en.T(c.key); got != c.text {
			t.Fatalf("%s: en catalog = %q, want the const text %q (the const is the English fallback)", c.key, got, c.text)
		}
	}
	for _, c := range cases {
		for _, lm := range langs {
			b := i18n.Load(lm.Code)
			if b.Lang != lm.Code {
				t.Fatalf("%s: Load(%q).Lang = %q", c.name, lm.Code, b.Lang)
			}
			got := b.T(c.key)
			if got == c.key {
				t.Fatalf("%s/%s: T returned the key ID, so the lookup fell through every catalog", c.name, lm.Code)
			}
			if lm.Code != "en" && got == c.text {
				t.Fatalf("%s/%s: resolved the English text, so the locale file carries no entry of its own", c.name, lm.Code)
			}
			for _, slot := range c.slots {
				if !strings.Contains(got, slot) {
					t.Fatalf("%s/%s: template %q lost the %s slot", c.name, lm.Code, got, slot)
				}
			}
		}
	}
}

// nameSinkPlant is the credential-class plant for the fix7-D name-sink pins: an
// underscore-headed key shape that a hand edit (or a registry written before
// the ingest doors closed) can leave in the NAME slot. providers.MaskOperand is
// identity on it (the "_" breaks its contiguous-run test and its head is not in
// the shared prefix list), while the registry-slot display authority
// providers.MaskSlot masks it first-5 + ****.
const (
	nameSinkPlant = testfixtures.SKLive
	nameSinkMask  = "sk_li****"
)

// nameSinkCorpus is the identity corpus: the required names (openai, gemini,
// deepseek, nw, charm) plus the names the pre-existing pins on this screen
// use (anthropic, acme-corp, test-all, solo, google, leaky, awsish, and the
// separator-bearing my_provider / my-provider / cedar.2024 / x). Every name
// here renders byte-identically under MaskOperand and MaskSlot, so the
// authority swap tightened nothing for a benign name.
func nameSinkCorpus() []string {
	return []string{
		"openai", "gemini", "deepseek", "nw", "charm",
		"anthropic", "acme-corp", "test-all", "solo", "google",
		"leaky", "awsish", "my_provider", "my-provider", "cedar.2024", "x",
	}
}

// nameSinkRegistry seeds the identity corpus plus the plant as single-model
// providers with neutral endpoints (the name must be the only registry value
// the row renderers can echo).
func nameSinkRegistry() *providers.Registry {
	reg := &providers.Registry{}
	for _, n := range append(nameSinkCorpus(), nameSinkPlant) {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name:    n,
			BaseURL: "https://gw.example/v1",
			Models:  []providers.Model{{ID: n + "-1"}},
		})
	}
	return reg
}

// TestProvidersNameSinksUseSlotAuthority (fix7-D) pins every provider-NAME sink
// on the providers screen against the slot authority: the entity row and the
// delete-refusal line (the answer-path composer). Identity half: the benign
// corpus renders unchanged. Credential half: the underscore-headed plant renders
// masked, never raw. The inversion (user ask U2) removed the per-provider verb
// rows, so the pin also asserts the other half: the three fixed verbs carry no
// registry name at all, which is what keeps a name out of the frame there.
func TestProvidersNameSinksUseSlotAuthority(t *testing.T) {
	providersFixture(t, nameSinkRegistry())
	core := providersTestCore(providers.ProbeSkipped, "no API key resolved; nothing to probe")
	scr := NewProvidersScreen(core, func(string, *App) {}, func(string, string, *App) {}).(*ProvidersScreen)

	rows := make(map[string]Row, scr.list.Len())
	for i := 0; i < scr.list.Len(); i++ {
		r := scr.list.Row(i)
		rows[r.ID] = r
	}

	cases := make([]struct{ name, rendered string }, 0, len(nameSinkCorpus())+1)
	for _, n := range nameSinkCorpus() {
		cases = append(cases, struct{ name, rendered string }{n, n})
	}
	cases = append(cases, struct{ name, rendered string }{nameSinkPlant, nameSinkMask})

	for _, c := range cases {
		entity, ok := rows[providerRowPrefix+c.name]
		if !ok {
			t.Fatalf("entity row for %q missing (%d rows)", c.name, len(rows))
		}
		if entity.Label != c.rendered {
			t.Errorf("entity row label = %q; want %q", entity.Label, c.rendered)
		}
		// deleteRefusal is the answer-path refusal composer: the name slot must
		// carry the same rendering.
		line := scr.deleteRefusal(c.name, providersDeleteReasonGone)
		if !strings.Contains(line, "delete "+c.rendered+": ") {
			t.Errorf("deleteRefusal(%q) = %q; want the %q name slot", c.name, line, c.rendered)
		}
		if c.name == nameSinkPlant && strings.Contains(line, nameSinkPlant) {
			t.Errorf("deleteRefusal rendered the plant raw: %q", line)
		}
	}

	// The sticky-target verbs are name-free rows: their labels are exactly the
	// catalog verb, so no registry name can reach the frame through them.
	for id, want := range map[string]string{
		providersRowIDTest:   scr.bundle.T("providers.action.test"),
		providersRowIDEdit:   missingOr(scr.bundle, "providers.form.edit", providersFormEdit),
		providersRowIDDelete: scr.bundle.T("providers.action.delete"),
	} {
		r, ok := rows[id]
		if !ok {
			t.Fatalf("verb row %q missing (%d rows)", id, len(rows))
		}
		if r.Label != want {
			t.Errorf("verb row %q label = %q; want the bare verb %q", id, r.Label, want)
		}
	}
}

// TestProvidersNameMessageSinksUseSlotAuthority (fix7-D) pins the two sinks
// that compose the name into a message or a modal body: the test-probe verdict
// line and the delete approval summary, both reached through the sticky-target
// verbs. Rows: 1 <plant>, 2 gemini, 3 Add, 4 Test all, 5 Test, 6 Edit, 7 Delete.
// The probe is made to succeed so no error detail (an error-string sink, outside
// this unit's name scope) joins the frame.
func TestProvidersNameMessageSinksUseSlotAuthority(t *testing.T) {
	providersFixture(t, &providers.Registry{Providers: []providers.Provider{
		{Name: nameSinkPlant, BaseURL: "https://gw.example/v1", Models: []providers.Model{{ID: "p-1"}}},
		{Name: "gemini", BaseURL: "https://example.com", APIKeyEnv: "GEMINI_API_KEY",
			Models: []providers.Model{{ID: "g-1"}, {ID: "g-2"}}},
	}})
	core := providersTestCore(providers.ProbeValid, "dest example.com: credential accepted")
	scr := NewProvidersScreen(core, func(string, *App) {}, func(string, string, *App) {}).(*ProvidersScreen)
	app := NewApp(160, 30, DirLTR)
	app.Push(scr)

	keys := append(selectProviderKeys(0), Digit(5), KeyLeft, Digit(7), KeyEsc, KeyLeft)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	if strings.Contains(out, nameSinkPlant) {
		t.Fatalf("the plant rendered raw in a providers frame\n%s", out)
	}
	for _, want := range []string{
		// The sticky-selection marker on the entity row is the target display.
		"● " + nameSinkMask,
		nameSinkMask + ": connection OK",
		"provider " + nameSinkMask + " (https://gw.example/v1, 1 model)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("name sink missing %q\n%s", want, out)
		}
	}
}

// TestProvidersStickyTargetHeldByProviderName (W1): the sticky verb target is
// held by provider NAME, not by a row index. An index survives a reload only by
// luck: the registry is rewritten whole by this screen's own delete, by the form
// above it, and by other sessions, so a re-clamped index can name a DIFFERENT
// provider than the one the user marked. Two paths are pinned here.
//
//  1. delete-marked-provider-then-next-verb: after the marked provider is
//     deleted the selection is gone, so the next verb must REFUSE (pre-fix it
//     clamped onto the neighbouring provider and silently probed it).
//  2. reordered registry: an external rewrite that moves the marked provider to
//     another position must not hand the verb to the record that inherited its
//     old index; the verb still acts on the marked NAME.
func TestProvidersStickyTargetHeldByProviderName(t *testing.T) {
	refusal := Bundle().T("providers.msg.select_provider")
	if refusal != providersMsgSelectProvider {
		t.Fatalf("precondition: the en catalog entry = %q, want the fallback const %q", refusal, providersMsgSelectProvider)
	}

	// probedName collects the provider each Core.ProviderTest actually probes
	// (Core hands the audit a single-provider registry, so entry 0 is the one
	// the verb targeted).
	newProbeCore := func(probed *[]string) *settings.Core {
		return settings.NewCore(settings.Deps{
			AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
				if len(reg.Providers) == 0 {
					return nil
				}
				*probed = append(*probed, reg.Providers[0].Name)
				return []providers.KeyAudit{{
					Provider: reg.Providers[0].Name,
					Env:      reg.Providers[0].APIKeyEnv,
					Probe:    providers.KeyProbeResult{Status: providers.ProbeValid},
				}}
			},
		})
	}

	t.Run("deleted target clears the selection", func(t *testing.T) {
		path := providersFixture(t, twoProviderRegistry())
		var probed, opened []string
		app := newProvidersApp(newProbeCore(&probed), &opened)

		// Rows: 1 gemini, 2 anthropic, 3 Add, 4 Test all, 5 Test, 6 Edit, 7 Delete.
		app.Step(KeyDown) // rest the cursor on provider 2: the marked target is anthropic
		app.Step(Digit(7))
		if app.Modal() == nil {
			t.Fatal("the delete verb did not open the approval gate on the marked provider")
		}
		app.Step(KeyYes)
		if got := app.Message(); got != "Provider anthropic deleted." {
			t.Fatalf("post-delete message = %q, want the anthropic delete stamp", got)
		}

		// anthropic is gone, so gemini moved from index 1 to index 0 and the
		// verbs moved up one row with it (1 gemini, 2 Add, 3 Test all, 4 Test).
		// Firing Test now must refuse: gemini is a provider the user never marked.
		app.Step(Digit(4))
		if got := app.Message(); got != refusal {
			t.Fatalf("verb after the marked provider was deleted = %q, want the refusal %q", got, refusal)
		}
		if len(probed) != 0 {
			t.Fatalf("the verb probed %v after its marked target was deleted; want no probe", probed)
		}
		if lines := app.Frame().Lines; frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● ") }) {
			t.Fatalf("a provider row is still marked after the marked provider was deleted:\n%s", strings.Join(lines, "\n"))
		}

		reg, err := providers.LoadFrom(path)
		if err != nil {
			t.Fatalf("reload temp registry: %v", err)
		}
		if len(reg.Providers) != 1 || reg.Providers[0].Name != "gemini" {
			t.Fatalf("registry after the approved delete = %+v, want gemini only", reg.Providers)
		}
	})

	t.Run("reordered registry keeps the marked provider", func(t *testing.T) {
		path := providersFixture(t, twoProviderRegistry())
		var probed, opened []string
		app := newProvidersApp(newProbeCore(&probed), &opened)

		app.Step(KeyDown) // mark anthropic (index 1)
		reversed := twoProviderRegistry()
		reversed.Providers[0], reversed.Providers[1] = reversed.Providers[1], reversed.Providers[0]
		providersWrite(t, path, reversed)
		app.Frame() // the frame re-reads the registry (reloadIfChanged), as a live screen does

		if lines := app.Frame().Lines; !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
			t.Fatalf("the marker did not follow anthropic to its new position:\n%s", strings.Join(lines, "\n"))
		}

		app.Step(Digit(5))
		if len(probed) != 1 || probed[0] != "anthropic" {
			t.Fatalf("the verb probed %v, want [anthropic] (the marked name, not the index)", probed)
		}
		if got := app.Message(); !strings.Contains(got, "anthropic") {
			t.Fatalf("the verdict = %q, want the marked provider anthropic", got)
		}
	})
}

// providersPinMessage is the confirmation line Space writes, resolved the same
// way the screen resolves it (catalog entry for the active language, the shared
// const as the fallback), with the provider name through the row-label masker.
func providersPinMessage(name string) string {
	return fillHubSlots(missingOr(Bundle(), "list.msg.pinned", listMsgPinned),
		map[string]string{"name": providers.MaskSlot(name)})
}

// TestProvidersSpacePinSurvivesArrowTransitToTheVerbRow is the flow that was
// impossible before the Space pin (user ask): with the follow-on-rest rule
// alone, arrow travel from provider 1 down to the Edit row passes provider 2,
// and each rest re-stole the sticky target, so Edit always opened the LAST
// provider. Space pins provider 1 explicitly, the transit cannot overwrite the
// pin, and Enter on the Edit row opens the form for the pinned provider.
func TestProvidersSpacePinSurvivesArrowTransitToTheVerbRow(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeSkipped, "no key")
	var modeArg, nameArg string
	var pushed Screen
	app := newProvidersFormApp(core, &modeArg, &nameArg, &pushed)

	// Rows: 1 gemini, 2 anthropic, 3 Add, 4 Test all, 5 Test, 6 Edit, 7 Delete.
	app.Step(KeySpace) // pin gemini (the opening cursor row) without activating it
	if got, want := app.Message(), providersPinMessage("gemini"); got != want {
		t.Fatalf("pin confirmation = %q, want %q", got, want)
	}
	if app.Depth() != 1 {
		t.Fatalf("Space activated a screen (depth %d); it must only pin", app.Depth())
	}

	// Arrow-only transit to the Edit row: anthropic, Add, Test all, Test, Edit.
	for i := 0; i < 5; i++ {
		app.Step(KeyDown)
		lines := app.Frame().Lines
		if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● gemini") }) {
			t.Fatalf("the pin was stolen after %d Down(s); marker is not on gemini:\n%s", i+1, strings.Join(lines, "\n"))
		}
	}
	app.Step(KeyEnter)
	if modeArg != "edit" || nameArg != "gemini" {
		t.Fatalf("Edit after the transit asked for mode %q name %q, want \"edit\" and gemini (the pinned row)", modeArg, nameArg)
	}
	if pushed == nil {
		t.Fatal("the Edit verb pushed no form")
	}
}

// TestProvidersSpacePinReachesTheDeleteGateForThePinnedRow pins the same
// transit against the danger row: the approval gate opened from the bottom of
// the list summarizes the PINNED provider, and answering n writes nothing, so
// the pin cannot widen the delete blast radius.
func TestProvidersSpacePinReachesTheDeleteGateForThePinnedRow(t *testing.T) {
	path := providersFixture(t, twoProviderRegistry())
	saves := 0
	var opened []string
	app := newProvidersApp(providersRecordingCore(&saves), &opened)

	app.Step(KeySpace) // pin gemini
	for i := 0; i < 6; i++ {
		app.Step(KeyDown) // transit to the Delete row (row 7)
	}
	app.Step(KeyEnter)
	m := app.Modal()
	if m == nil {
		t.Fatal("the Delete verb opened no gate after a pin")
	}
	if _, ok := m.(*Approval); !ok {
		t.Fatalf("gate = %T, want the M4 approval modal", m)
	}
	if body := strings.Join(m.Body(160), "\n"); !strings.Contains(body, "gemini") {
		t.Fatalf("the gate summarizes no gemini; body:\n%s", body)
	}
	app.Step(KeyNo)
	if saves != 0 {
		t.Fatalf("the declined gate wrote %d time(s)", saves)
	}
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload temp registry: %v", err)
	}
	if len(reg.Providers) != 2 {
		t.Fatalf("registry after the declined gate = %+v, want both providers", reg.Providers)
	}
}

// TestProvidersSpaceOnAVerbRowIsANoOp pins the boundary: the verb rows below the
// provider block are not selectable, so a Space there changes nothing (no pin,
// no message) and a later verb still refuses, while a Space on a verb row AFTER
// a pin leaves the pin standing.
func TestProvidersSpaceOnAVerbRowIsANoOp(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	app := newProvidersApp(providersTestCore(providers.ProbeSkipped, "no key"), new([]string))
	scr := app.Top().(*ProvidersScreen)

	app.Step(KeyDown)
	app.Step(KeyDown) // row 3 = Add; the rest already selected anthropic (follow-on-rest)
	beforeSel, beforeName, beforeMsg := scr.sel, scr.selName, app.Message()
	app.Step(KeySpace)
	if got := app.Message(); got != beforeMsg {
		t.Fatalf("Space on the Add row wrote a message: %q", got)
	}
	if scr.sel != beforeSel || scr.selName != beforeName || scr.locked {
		t.Fatalf("Space on the Add row changed the selection: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	// Nothing was pinned either: the refusal proof uses a fresh screen whose
	// cursor never rested on a provider row.
	fresh := newProvidersApp(providersTestCore(providers.ProbeSkipped, "no key"), new([]string))
	fresh.Step(KeyEnd)
	fresh.Step(KeySpace) // Delete row: no pin, no message
	if got := fresh.Message(); got != "" {
		t.Fatalf("Space on the Delete row wrote a message: %q", got)
	}
	fresh.Step(Digit(5))
	if got, want := fresh.Message(), Bundle().T("providers.msg.select_provider"); got != want {
		t.Fatalf("verb after the no-op Space = %q, want the refusal %q", got, want)
	}

	// Re-pin half: a pin, a transit onto a verb row, and a Space there.
	app.Step(KeyHome) // cursor back to gemini
	app.Step(KeySpace)
	pinned := app.Message()
	if pinned != providersPinMessage("gemini") {
		t.Fatalf("re-pin confirmation = %q, want %q", pinned, providersPinMessage("gemini"))
	}
	for i := 0; i < 4; i++ {
		app.Step(KeyDown) // row 5 = Test
	}
	app.Step(KeySpace)
	if got := app.Message(); got != pinned {
		t.Fatalf("Space on the Test row rewrote the message to %q, want the pin line %q", got, pinned)
	}
	if !scr.locked || scr.selName != "gemini" {
		t.Fatalf("Space on the Test row dropped the pin: selName=%q locked=%v", scr.selName, scr.locked)
	}
}

// TestProvidersSpacePinUnlocksAfterTheVerb pins the first unlock transition: a
// sticky verb spends the pin, so the follow-on-rest rule resumes immediately and
// the marker follows the cursor again.
func TestProvidersSpacePinUnlocksAfterTheVerb(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	app := newProvidersApp(providersTestCore(providers.ProbeValid, "dest example.com: credential accepted"), new([]string))
	scr := app.Top().(*ProvidersScreen)

	app.Step(KeySpace) // pin gemini
	for i := 0; i < 4; i++ {
		app.Step(KeyDown) // row 5 = Test
	}
	app.Step(KeyEnter) // the verb consumes the pin
	if scr.locked {
		t.Fatal("the pin survived the verb that consumed it")
	}
	if got := app.Message(); !strings.Contains(got, "gemini") {
		t.Fatalf("the verb's verdict = %q, want the pinned gemini", got)
	}

	app.Step(KeyHome) // cursor back to gemini
	app.Step(KeyDown) // rest on anthropic: the cursor reassert is live again
	lines := app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
		t.Fatalf("the marker did not follow the cursor after the verb:\n%s", strings.Join(lines, "\n"))
	}
	if frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● gemini") }) {
		t.Fatalf("gemini is still marked after the cursor moved to anthropic:\n%s", strings.Join(lines, "\n"))
	}
}

// TestProvidersSpacePinUnlocksWhenAnEntityRowIsActivated pins the navigation
// transition: Enter on a provider row opens its models screen, so the pin is
// spent (a provider row activation addresses the CURSOR row, never the pinned
// one) and the marker adopts the row the cursor activated.
func TestProvidersSpacePinUnlocksWhenAnEntityRowIsActivated(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	var opened []string
	app := newProvidersApp(providersTestCore(providers.ProbeSkipped, "no key"), &opened)
	scr := app.Top().(*ProvidersScreen)

	app.Step(KeySpace) // pin gemini
	app.Step(KeyDown)  // rest on anthropic; the pin holds
	if !scr.locked || scr.selName != "gemini" {
		t.Fatalf("the pin did not survive the rest: selName=%q locked=%v", scr.selName, scr.locked)
	}
	app.Step(KeyEnter) // activates the CURSOR row (anthropic) and clears the pin
	if got, want := strings.Join(opened, ","), "anthropic"; got != want {
		t.Fatalf("provider row activation opened %q, want %q", got, want)
	}
	if scr.locked {
		t.Fatal("the pin survived the navigation into the models screen")
	}
	lines := app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
		t.Fatalf("the marker did not adopt the activated row:\n%s", strings.Join(lines, "\n"))
	}
}

// TestProvidersSpacePinDropsWhenItsTargetVanishes pins the invariant the pin
// rests on: locked means "the selection owns the cursor-reassert", so a locked
// screen with no selection would freeze the marker forever. An external delete
// of the pinned provider clears the identity (reload's re-anchor) and must take
// the lock with it.
func TestProvidersSpacePinDropsWhenItsTargetVanishes(t *testing.T) {
	path := providersFixture(t, twoProviderRegistry())
	app := newProvidersApp(providersTestCore(providers.ProbeSkipped, "no key"), new([]string))
	scr := app.Top().(*ProvidersScreen)

	app.Step(KeySpace) // pin gemini
	if !scr.locked || scr.selName != "gemini" {
		t.Fatalf("the pin did not take: selName=%q locked=%v", scr.selName, scr.locked)
	}

	providersWrite(t, path, anthropicOnlyRegistry()) // gemini is deleted behind the screen
	app.Frame()                                      // reloadIfChanged picks the new registry up
	if scr.locked || scr.selName != "" || scr.sel != -1 {
		t.Fatalf("the pin outlived its target: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}

	// The follow-on-rest rule is live again: the only provider row gets marked.
	app.Step(KeyUp) // cursor stays on the row; the rest re-asserts the selection
	if lines := app.Frame().Lines; !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
		t.Fatalf("the marker did not resume following the cursor:\n%s", strings.Join(lines, "\n"))
	}
}

// TestProvidersSpacePinEndToEndThroughTheShell drives the flow the user asked
// for through the SHIPPED shell (assembleSetupApp + the line-mode reader, the
// same wiring RunSetup installs on a non-console host): hub row 1 opens the
// providers screen, Space pins gemini, five Downs walk the cursor down to the
// Edit row past anthropic, and Enter opens the provider form for the PINNED
// provider. Before the pin, every one of those rests re-stole the selection,
// so the form always came up for anthropic (the last provider row). The form
// carries the pinned provider's endpoint, and the other endpoint is absent, so
// the assertion names the target rather than the gesture.
func TestProvidersSpacePinEndToEndThroughTheShell(t *testing.T) {
	appTestHome(t, "en") // temp AGY_PRO_HOME, state.json language en, providers.json seeded
	cap := plainTestCap()
	var out, errb bytes.Buffer
	script := strings.NewReader("1\nspace\ndown\ndown\ndown\ndown\ndown\nenter\n")
	if code := runSetup(script, &out, &errb, cap); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0; stderr=%s", code, errb.String())
	}
	frames := out.String()
	if !strings.Contains(frames, providersPinMessage("gemini")) {
		t.Fatalf("the shell never rendered the pin confirmation:\n%s", frames)
	}
	if !strings.Contains(frames, Bundle().T("providers.form.title.edit")) {
		t.Fatalf("the Edit verb opened no provider form:\n%s", frames)
	}
	if !strings.Contains(frames, "https://example.com") {
		t.Fatalf("the form does not carry the PINNED provider's endpoint:\n%s", frames)
	}
	if strings.Contains(frames, "api.anthropic.com") {
		t.Fatalf("the form carries the LAST provider's endpoint, so the transit stole the pin:\n%s", frames)
	}

	// Negative control: the same transit WITHOUT the Space gesture reproduces
	// the reported defect, so the assertion above is sensitive to the fix and
	// not to the fixture.
	appTestHome(t, "en")
	var control, controlErr bytes.Buffer
	noPin := strings.NewReader("1\ndown\ndown\ndown\ndown\ndown\nenter\n")
	if code := runSetup(noPin, &control, &controlErr, plainTestCap()); code != 0 {
		t.Fatalf("control runSetup exit = %d, want 0; stderr=%s", code, controlErr.String())
	}
	if !strings.Contains(control.String(), "api.anthropic.com") {
		t.Fatalf("the control (no pin) did not reproduce the last-row target:\n%s", control.String())
	}
}

// TestProvidersSpaceRePinMovesThePin closes the re-pin row of the lifecycle
// matrix for this screen (fix round 1, F-02): another Space on another provider
// row moves the pin, the confirmation names the new target, and the verb below
// acts on it rather than on the first pin.
func TestProvidersSpaceRePinMovesThePin(t *testing.T) {
	providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeSkipped, "no key")
	var modeArg, nameArg string
	var pushed Screen
	app := newProvidersFormApp(core, &modeArg, &nameArg, &pushed)
	scr := app.Top().(*ProvidersScreen)

	// Rows: 1 gemini, 2 anthropic, 3 Add, 4 Test all, 5 Test, 6 Edit, 7 Delete.
	app.Step(KeySpace) // pin gemini
	app.Step(KeyDown)  // rest on anthropic: the pin holds
	if !scr.locked || scr.selName != "gemini" {
		t.Fatalf("the rest stole the pin: selName=%q locked=%v", scr.selName, scr.locked)
	}
	app.Step(KeySpace) // re-pin onto anthropic
	if got, want := app.Message(), providersPinMessage("anthropic"); got != want {
		t.Fatalf("re-pin confirmation = %q, want %q", got, want)
	}
	if !scr.locked || scr.selName != "anthropic" {
		t.Fatalf("the re-pin did not move: selName=%q locked=%v", scr.selName, scr.locked)
	}
	if lines := app.Frame().Lines; !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "● anthropic") }) {
		t.Fatalf("the marker did not follow the re-pin:\n%s", strings.Join(lines, "\n"))
	}

	for i := 0; i < 4; i++ {
		app.Step(KeyDown) // rows 3..6: Add, Test all, Test, Edit
	}
	app.Step(KeyEnter)
	if modeArg != "edit" || nameArg != "anthropic" {
		t.Fatalf("Edit after the re-pin asked for mode %q name %q, want \"edit\" and anthropic", modeArg, nameArg)
	}
}
