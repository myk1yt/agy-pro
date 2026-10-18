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

// providersFixture writes reg to a temp AGY_PRO_HOME and points the process
// at it. The live providers.json is never touched: EnvProviders is forced
// empty so DefaultPath resolves inside the temp home.
func providersFixture(t *testing.T, reg *providers.Registry) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv(providers.EnvProviders, "")
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(home, "providers.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
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
	}))
	return app
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
		"Test: gemini",
		"Delete: anthropic",
		"digits 1-9 select+activate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("frame missing %q\n%s", want, out)
		}
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
	// Row 4 is "Test: gemini" (1 gemini, 2 anthropic, 3 Test all, 4 Test: gemini).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(4), KeyLeft}})

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
	out2 := app2.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(4), KeyLeft}})
	if !strings.Contains(out2, "gemini: connection OK") {
		t.Fatalf("no success verdict on the message line\n%s", out2)
	}
}

func TestProvidersDeleteApprovalAndConfirm(t *testing.T) {
	path := providersFixture(t, twoProviderRegistry())
	core := providersTestCore(providers.ProbeValid, "dest example.com: credential accepted")
	var opened []string
	app := newProvidersApp(core, &opened)

	// Row 6 is "Delete: gemini". y approves the write; KeyLeft forces one
	// more frame so the post-delete list and message are captured.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(6), KeyYes, KeyLeft}})

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

			out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(6), tc.key, KeyLeft}})

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
	awsKey := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
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
		digit    Key // delete row: providers(2) + test-all(1) + test rows(2) -> delete rows are 6 and 7
		keep     string
		mustHide []string
	}{
		{"short password", Digit(6), "gw.example", []string{"user:hunter2sekret", "hunter2sekret"}},
		{"aws-shaped userinfo", Digit(7), "gateway.example", []string{awsKey}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opened []string
			app := newProvidersApp(core, &opened)
			// Digit opens the modal; Esc cancels so nothing is written.
			out := app.CaptureRun(&ScriptedKeys{Keys: []Key{tc.digit, KeyEsc, KeyLeft}})

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

// TestProvidersRowIDsSurviveSeparatorNames pins the collision finding: a
// provider name that IS a fixed row ID or embeds the old separator must not
// shadow another row or mis-route an activation.
//
// Round-6 seat1-R8 update: the closed provider-name charset is enforced at
// load (Registry.Validate calls ValidateProviderName), so a name embedding
// the old ":" row separator ("delete:x") can no longer load from disk; the
// collision class is now closed at the registry boundary, which the last
// sub-test pins. The routing sub-tests keep their coverage with
// charset-legal names ("acme-corp", "x"): a delete/test row must still name
// exactly its own provider.
func TestProvidersRowIDsSurviveSeparatorNames(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "acme-corp", BaseURL: "https://a.example/v1", Models: []providers.Model{{ID: "m-a"}}},
		{Name: "x", BaseURL: "https://b.example/v1", Models: []providers.Model{{ID: "m-b"}}},
	}}
	providersFixture(t, reg)
	core := providersTestCore(providers.ProbeValid, "ok")

	// Row order: 1 provider acme-corp, 2 provider x, 3 Test all, 4 test
	// acme-corp, 5 test x, 6 delete acme-corp, 7 delete x.
	t.Run("delete row for x targets x", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(7), KeyEsc, KeyLeft}})
		if !strings.Contains(out, "Removes the registry entry for x only") {
			t.Fatalf("delete approval named the wrong target\n%s", out)
		}
		if strings.Contains(out, "Removes the registry entry for acme-corp only") {
			t.Fatalf("delete approval shadowed by the acme-corp provider\n%s", out)
		}
	})
	t.Run("delete row for acme-corp targets acme-corp", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(6), KeyEsc, KeyLeft}})
		if !strings.Contains(out, "Removes the registry entry for acme-corp only") {
			t.Fatalf("delete approval named the wrong target\n%s", out)
		}
	})
	t.Run("test row for acme-corp probes acme-corp", func(t *testing.T) {
		var opened []string
		app := newProvidersApp(core, &opened)
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(4), KeyLeft}})
		if !strings.Contains(out, "acme-corp: connection OK") {
			t.Fatalf("test verdict named the wrong provider\n%s", out)
		}
		if len(opened) != 0 {
			t.Fatalf("test row opened models: %v", opened)
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
	// "Test all" row (row 1 is the provider, row 2 is the action).
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
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(2), KeyLeft}})
		if !strings.Contains(out, "1/1 providers OK") {
			t.Fatalf("Test all did not run\n%s", out)
		}
		if len(opened) != 0 {
			t.Fatalf("Test all opened models: %v", opened)
		}
	})
}
