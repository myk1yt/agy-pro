package launcher

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/hybrid"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

func TestDecide(t *testing.T) {
	envNone := func(string) string { return "" }
	envLauncherOff := func(k string) string {
		if k == "AGY_LAUNCHER" {
			return "0"
		}
		return ""
	}
	envLauncherOn := func(k string) string {
		if k == "AGY_LAUNCHER" {
			return "1"
		}
		return ""
	}
	envBridge := func(k string) string {
		if k == "AGY_PRO_BRIDGE" {
			return "1"
		}
		return ""
	}
	envBridgeAlt := func(k string) string {
		if k == "AGY_BRIDGE" {
			return "1"
		}
		return ""
	}

	cases := []struct {
		name          string
		args          []string
		env           func(string) string
		tty           bool
		modelCount    int
		profile       string
		hybridProfile string
		want          Route
	}{
		// 1. Registered models == 0: launcher skipped, native immediately.
		{"no models plain tty prompt", nil, envNone, true, 0, mode.ProfilePrompt, "", RouteNative},
		{"no models gateway", nil, envNone, true, 0, mode.ProfileGateway, "", RouteNative},
		{"no models with args", []string{"agents"}, envNone, true, 0, mode.ProfilePrompt, "", RouteNative},

		// 2. pro subcommand is never launched through the menu.
		{"pro args", []string{"pro", "status"}, envNone, true, 5, mode.ProfilePrompt, "", RoutePassthrough},

		// 3. Explicit bridge request defers to the historical rules.
		{"bridge flag", []string{"--bridge"}, envNone, true, 5, mode.ProfilePrompt, "", RoutePassthrough},
		{"bridge short flag", []string{"-b"}, envNone, true, 5, mode.ProfilePrompt, "", RoutePassthrough},
		{"bridge env", nil, envBridge, true, 5, mode.ProfilePrompt, "", RoutePassthrough},
		{"bridge alt env", nil, envBridgeAlt, true, 5, mode.ProfilePrompt, "", RoutePassthrough},
		{"bridge flag under gateway", []string{"--bridge"}, envNone, true, 5, mode.ProfileGateway, "", RoutePassthrough},

		// 4. Model targeting defers to the historical rules (native Gemini
		// requests must keep working even under the gateway profile).
		{"model flag", []string{"--model", "glm-5.3-flash-flex"}, envNone, true, 5, mode.ProfilePrompt, "", RoutePassthrough},
		{"model flag equals", []string{"--model=glm-5.3-flash-flex"}, envNone, true, 5, mode.ProfilePrompt, "", RoutePassthrough},
		{"model short", []string{"-m", "gemini-3.8-flash"}, envNone, true, 5, mode.ProfileGateway, "", RoutePassthrough},
		{"model short equals", []string{"-m=gemini-3.8-flash"}, envNone, true, 5, mode.ProfileNative, "", RoutePassthrough},

		// 5. Gateway profile forces the bridge for plain invocations.
		{"gateway plain", nil, envNone, true, 5, mode.ProfileGateway, "", RouteBridge},
		{"gateway with passthrough args", []string{"agents"}, envNone, true, 5, mode.ProfileGateway, "", RouteBridge},
		{"gateway non-tty", nil, envNone, false, 5, mode.ProfileGateway, "", RouteBridge},
		{"gateway launcher off", nil, envLauncherOff, true, 5, mode.ProfileGateway, "", RouteBridge},
		{"gateway prompt flag", []string{"-p", "hello"}, envNone, true, 5, mode.ProfileGateway, "", RouteBridge},

		// 6. Native profile runs straight through.
		{"native plain", nil, envNone, true, 5, mode.ProfileNative, "", RouteNative},
		{"native non-tty", nil, envNone, false, 5, mode.ProfileNative, "", RouteNative},
		{"native launcher menu would fit", nil, envNone, true, 5, mode.ProfileNative, "", RouteNative},

		// 7. Prompt profile: one-shot prompts never block on the menu.
		{"prompt flag", []string{"-p", "hello"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"prompt flag long", []string{"--prompt", "hello"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"prompt flag equals", []string{"--prompt=hello"}, envNone, true, 5, "", "", RouteNative},
		// F-4: --print is agy's canonical one-shot (-p is its alias).
		{"print flag", []string{"--print", "hello"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"print flag equals", []string{"--print=hello"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"print flag empty profile", []string{"--print", "x"}, envNone, true, 5, "", "", RouteNative},
		{"print flag unknown profile", []string{"--print=x"}, envNone, true, 5, "bogus", "", RouteNative},

		// 8. AGY_LAUNCHER=0 disables only the menu (now redundant with the
		// opt-in default, still honored).
		{"launcher disabled", nil, envLauncherOff, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"launcher disabled gateway", nil, envLauncherOff, true, 5, mode.ProfileGateway, "", RouteBridge},

		// 9. Pipes and CI are zero-delay native.
		{"non tty", nil, envNone, false, 5, mode.ProfilePrompt, "", RouteNative},
		{"non tty with args", []string{"agents"}, envNone, false, 5, mode.ProfilePrompt, "", RouteNative},

		// 10. Default flip: the menu is opt-IN. A bare `agy` on a TTY goes
		// straight to native with zero delay; only AGY_LAUNCHER=1 shows it.
		{"bare tty default native", nil, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"bare tty default native empty profile", nil, envNone, true, 5, "", "", RouteNative},
		{"bare tty default native unknown profile", nil, envNone, true, 5, "bogus", "", RouteNative},
		{"bare tty default native single model", nil, envNone, true, 1, mode.ProfilePrompt, "", RouteNative},

		// 11. AGY_LAUNCHER=1 is the only way to the menu, and it still obeys
		// every earlier rule (profiles, prompt flags, args, non-TTY).
		{"menu opt in", nil, envLauncherOn, true, 5, mode.ProfilePrompt, "", RouteMenu},
		{"menu opt in empty profile", nil, envLauncherOn, true, 5, "", "", RouteMenu},
		{"menu opt in single model", nil, envLauncherOn, true, 1, mode.ProfilePrompt, "", RouteMenu},
		{"menu opt in gateway profile", nil, envLauncherOn, true, 5, mode.ProfileGateway, "", RouteBridge},
		{"menu opt in native profile", nil, envLauncherOn, true, 5, mode.ProfileNative, "", RouteNative},
		{"menu opt in prompt flag", []string{"-p", "hello"}, envLauncherOn, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"menu opt in non tty", nil, envLauncherOn, false, 5, mode.ProfilePrompt, "", RouteNative},
		{"menu opt in with args", []string{"agents"}, envLauncherOn, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"menu opt in version flag", []string{"--version"}, envLauncherOn, true, 5, mode.ProfilePrompt, "", RouteNative},

		// 12. F-10: any argument still skips the menu on the default path.
		{"non-plain subcommand agents", []string{"agents"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"non-plain version flag", []string{"--version"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"non-plain help flag", []string{"--help"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"non-plain help short", []string{"-h"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		{"non-plain equals flag", []string{"--foo=bar"}, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},

		// 13. Hybrid native-coexistence profiles (design §2). The hybrid rule
		// sits immediately after the pro check and BEFORE the
		// modelCount/bridge/model-flag/prompt-flag/menu/default-profile rules,
		// so an active profile covers EVERY invocation shape (TUI, -p one-shot,
		// --model, the models subcommand, pipe/CI, zero registered models). The
		// only carve-out is an explicit per-invocation bridge request, which
		// keeps its meaning. No-profile behavior is unchanged (RouteNative).
		{"hybrid p1 bare tty", nil, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p2 bare tty", nil, envNone, true, 5, mode.ProfilePrompt, "p2", RouteHybridP2},
		{"hybrid p1 empty default profile", nil, envNone, true, 5, "", "p1", RouteHybridP1},
		{"hybrid p1 single model", nil, envNone, true, 1, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid none is unchanged native", nil, envNone, true, 5, mode.ProfilePrompt, "none", RouteNative},
		{"hybrid empty is unchanged native", nil, envNone, true, 5, mode.ProfilePrompt, "", RouteNative},
		// The G4 / core-use-case shapes: a -p one-shot, a --model run, and the
		// models subcommand all route to the mesh under an active profile.
		{"hybrid p1 prompt flag routes to mesh", []string{"-p", "hi"}, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 print flag routes to mesh", []string{"--print", "hi"}, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 model flag routes to mesh", []string{"--model", "ext-one"}, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 model short routes to mesh", []string{"-m", "ext-one"}, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 models subcommand routes to mesh", []string{"models"}, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 any arg routes to mesh", []string{"agents"}, envNone, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 non tty routes to mesh", nil, envNone, false, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 no models routes to mesh", nil, envNone, true, 0, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p2 prompt flag routes to mesh", []string{"-p", "hi"}, envNone, true, 5, mode.ProfilePrompt, "p2", RouteHybridP2},
		{"hybrid p1 menu opt-in overridden by hybrid", nil, envLauncherOn, true, 5, mode.ProfilePrompt, "p1", RouteHybridP1},
		{"hybrid p1 gateway profile overridden by hybrid", nil, envNone, true, 5, mode.ProfileGateway, "p1", RouteHybridP1},
		{"hybrid p1 native profile overridden by hybrid", nil, envNone, true, 5, mode.ProfileNative, "p1", RouteHybridP1},
		// The single carve-out: an explicit per-invocation bridge request keeps
		// its meaning and is handled by the explicit-bridge rule (RoutePassthrough).
		{"hybrid p1 explicit --bridge wins", []string{"--bridge"}, envNone, true, 5, mode.ProfilePrompt, "p1", RoutePassthrough},
		{"hybrid p1 explicit -b wins", []string{"-b"}, envNone, true, 5, mode.ProfilePrompt, "p1", RoutePassthrough},
		{"hybrid p1 explicit AGY_PRO_BRIDGE wins", nil, envBridge, true, 5, mode.ProfilePrompt, "p1", RoutePassthrough},
		{"hybrid p1 explicit AGY_BRIDGE wins", nil, envBridgeAlt, true, 5, mode.ProfilePrompt, "p1", RoutePassthrough},
		{"hybrid p2 explicit --bridge wins", []string{"--bridge"}, envNone, true, 5, mode.ProfilePrompt, "p2", RoutePassthrough},
		// pro is still before the hybrid rule.
		{"hybrid p1 pro args passthrough", []string{"pro", "status"}, envNone, true, 5, mode.ProfilePrompt, "p1", RoutePassthrough},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideRoute(tc.args, tc.env, tc.tty, tc.modelCount, tc.profile, tc.hybridProfile)
			if got != tc.want {
				t.Errorf("DecideRoute(%v, tty=%v, models=%d, profile=%q, hybrid=%q) = %s; want %s",
					tc.args, tc.tty, tc.modelCount, tc.profile, tc.hybridProfile, got, tc.want)
			}
		})
	}
}

func TestDecideNilLookupEnv(t *testing.T) {
	// nil lookupEnv means every variable is unset, so the menu opt-in
	// (AGY_LAUNCHER=1) is absent and the bare launch goes straight native.
	if got := DecideRoute(nil, nil, true, 3, mode.ProfilePrompt, ""); got != RouteNative {
		t.Errorf("DecideRoute with nil lookupEnv = %s; want native", got)
	}
	// The posture side must tolerate the same nil seam: no env layer to read, so
	// only the injected persisted value counts.
	if got := Decide(nil, nil, true, 3, mode.ProfilePrompt, "", mode.IntegrationPresetTools); got.IntegrationPreset != mode.IntegrationPresetTools || got.NativePreset {
		t.Errorf("Decide with nil lookupEnv and a persisted tools preset = %+v; want tools, NativePreset=false", got)
	}
}

// The menu opt-in is an exact match: only AGY_LAUNCHER=1 opens it. Truthy
// variants, unset, and "0" all keep the straight-native default.
func TestLauncherOptInExactMatch(t *testing.T) {
	cases := []struct {
		value string
		want  Route
	}{
		{"1", RouteMenu},
		{"0", RouteNative},
		{"", RouteNative},
		{"true", RouteNative},
		{"yes", RouteNative},
		{" 1", RouteNative},
		{"1 ", RouteNative},
		{"2", RouteNative},
	}
	for _, tc := range cases {
		env := func(k string) string {
			if k == "AGY_LAUNCHER" {
				return tc.value
			}
			return ""
		}
		if got := DecideRoute(nil, env, true, 5, mode.ProfilePrompt, ""); got != tc.want {
			t.Errorf("AGY_LAUNCHER=%q: DecideRoute = %s; want %s", tc.value, got, tc.want)
		}
	}
}

func TestResolveMenuChoice(t *testing.T) {
	cases := []struct {
		input string
		want  Route
	}{
		{"", RouteNative},   // timeout
		{"1", RouteNative},  // explicit default
		{"\r", RouteNative}, // stray whitespace around 1
		{" 1 ", RouteNative},
		{"2", RouteBridge},
		{" 2 ", RouteBridge},
		{"3", RouteFanout},
		{"3\n", RouteFanout},
		{"9", RouteNative}, // unknown keys fall back to native
		{"x", RouteNative},
		{"\n", RouteNative}, // bare Enter
	}
	for _, tc := range cases {
		if got := ResolveMenuChoice(tc.input); got != tc.want {
			t.Errorf("ResolveMenuChoice(%q) = %s; want %s", tc.input, got, tc.want)
		}
	}
}

// TestHasPromptFlag covers the one-shot bypass set. --print is agy's canonical
// one-shot flag (F-4) and -p is its alias; every form must be recognized.
func TestHasPromptFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"agents"}, false},
		{[]string{"--version"}, false},
		{[]string{"--print"}, true},
		{[]string{"--print", "x"}, true},
		{[]string{"--print=x"}, true},
		{[]string{"--print="}, true},
		{[]string{"-p", "x"}, true},
		{[]string{"-p=x"}, true},
		{[]string{"--prompt", "x"}, true},
		{[]string{"--prompt=x"}, true},
		{[]string{"--printer"}, false},
		{[]string{"--model", "m", "--print", "x"}, true},
	}
	for _, tc := range cases {
		if got := hasPromptFlag(tc.args); got != tc.want {
			t.Errorf("hasPromptFlag(%v) = %v; want %v", tc.args, got, tc.want)
		}
	}
}

func TestMapConsoleKey(t *testing.T) {
	cases := []struct {
		char   uint16
		vk     uint16
		want   string
		wantOK bool
		desc   string
	}{
		{'1', 0x31, "1", true, "digit 1"},
		{'2', 0x32, "2", true, "digit 2"},
		{'3', 0x33, "3", true, "digit 3"},
		{'\r', 0x0d, "", true, "carriage return = default"},
		{'\n', 0x0d, "", true, "line feed = default"},
		{0, 0x0d, "", true, "VK_RETURN without char (numpad Enter)"},
		{'4', 0x34, "", false, "digit 4 is not a choice"},
		{'a', 0x41, "", false, "letter key"},
		{' ', 0x20, "", false, "space is not a choice"},
		{0, 0x1b, "", false, "escape"},
	}
	for _, tc := range cases {
		got, ok := mapConsoleKey(tc.char, tc.vk)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("mapConsoleKey(%q, vk=%#x) [%s] = (%q, %v); want (%q, %v)",
				tc.char, tc.vk, tc.desc, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestRouteString(t *testing.T) {
	cases := map[Route]string{
		RoutePassthrough: "passthrough",
		RouteNative:      "native",
		RouteBridge:      "bridge",
		RouteMenu:        "menu",
		RouteFanout:      "fanout",
		RouteHybridP1:    "hybrid-p1",
		RouteHybridP2:    "hybrid-p2",
		Route(42):        "unknown",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("Route(%d).String() = %q; want %q", r, got, want)
		}
	}
}

func TestCountEnabledModels(t *testing.T) {
	// Built through the providers registry shape the launcher consumes.
	// Verified again end-to-end in TestDecide* and the bridge tests.
	reg := testRegistryWithModels()
	if got := CountEnabledModels(reg); got != 3 {
		t.Errorf("CountEnabledModels = %d; want 3", got)
	}
	if got := CountEnabledModels(nil); got != 0 {
		t.Errorf("CountEnabledModels(nil) = %d; want 0", got)
	}
	names := EnabledProviderNames(reg)
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("EnabledProviderNames = %v; want [alpha beta]", names)
	}
}

func TestProviderSummary(t *testing.T) {
	if got := ProviderSummary([]string{"openai", "openai", "anthropic", ""}); got != "openai, anthropic" {
		t.Errorf("ProviderSummary = %q; want 'openai, anthropic'", got)
	}
	if got := ProviderSummary(nil); got != "none" {
		t.Errorf("ProviderSummary(nil) = %q; want 'none'", got)
	}
}

// ---------------------------------------------------------------------------
// Integrations posture (P5-4): carried on the verdict, never routed on
// ---------------------------------------------------------------------------

// TestDecideIntegrationPosture pins the posture precedence
// EnvIntegrationPreset override > persisted key > absent, and the two shape
// rules a consumer depends on: NativePreset is true only for an explicit
// "native", and the Route is identical to DecideRoute for the same inputs (the
// preset cannot move a launch, because there is only one binary).
func TestDecideIntegrationPosture(t *testing.T) {
	envNone := func(string) string { return "" }
	envWith := func(value string) func(string) string {
		return func(k string) string {
			if k == EnvIntegrationPreset {
				return value
			}
			return ""
		}
	}

	cases := []struct {
		name       string
		env        func(string) string
		persisted  string
		want       string
		wantNative bool
	}{
		// Absent everywhere: the legacy no-opinion posture.
		{"absent both", envNone, "", "", false},
		// Persisted key alone.
		{"persisted native", envNone, mode.IntegrationPresetNative, mode.IntegrationPresetNative, true},
		{"persisted tools", envNone, mode.IntegrationPresetTools, mode.IntegrationPresetTools, false},
		{"persisted custom", envNone, mode.IntegrationPresetCustom, mode.IntegrationPresetCustom, false},
		{"persisted padded and cased", envNone, " TOOLS ", mode.IntegrationPresetTools, false},
		// Env override wins in both directions.
		{"env beats persisted tools", envWith(mode.IntegrationPresetNative), mode.IntegrationPresetTools, mode.IntegrationPresetNative, true},
		{"env beats persisted native", envWith(mode.IntegrationPresetTools), mode.IntegrationPresetNative, mode.IntegrationPresetTools, false},
		{"env custom beats persisted", envWith(mode.IntegrationPresetCustom), mode.IntegrationPresetNative, mode.IntegrationPresetCustom, false},
		{"env padded and cased", envWith(" Native "), "", mode.IntegrationPresetNative, true},
		// An override that names no preset is inert: the persisted key decides.
		{"unrecognized env falls through", envWith("bogus"), mode.IntegrationPresetCustom, mode.IntegrationPresetCustom, false},
		{"empty env is absent", envWith(""), mode.IntegrationPresetNative, mode.IntegrationPresetNative, true},
		{"unrecognized env and absent key", envWith("on"), "", "", false},
		// An unrecognized persisted value is inert too, never reinterpreted.
		{"unrecognized persisted", envNone, "bogus", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(nil, tc.env, true, 5, mode.ProfilePrompt, "", tc.persisted)
			if got.IntegrationPreset != tc.want {
				t.Errorf("IntegrationPreset = %q; want %q", got.IntegrationPreset, tc.want)
			}
			if got.NativePreset != tc.wantNative {
				t.Errorf("NativePreset = %v; want %v", got.NativePreset, tc.wantNative)
			}
			if want := DecideRoute(nil, tc.env, true, 5, mode.ProfilePrompt, ""); got.Route != want {
				t.Errorf("the posture moved the route: Decide = %s, DecideRoute = %s; want identical",
					got.Route, want)
			}
		})
	}
}

// TestDecideRouteIsPostureBlind walks the routing shapes that decide_test pins
// and proves the new field is inert for every one of them: the same invocation
// under an absent, native, tools and custom posture, plus a hostile env
// override, must produce the route DecideRoute produces with no posture at all.
func TestDecideRouteIsPostureBlind(t *testing.T) {
	envNone := func(string) string { return "" }
	envOverride := func(k string) string {
		if k == EnvIntegrationPreset {
			return mode.IntegrationPresetNative
		}
		return ""
	}
	shapes := []struct {
		name          string
		args          []string
		env           func(string) string
		tty           bool
		modelCount    int
		profile       string
		hybridProfile string
	}{
		{"bare tty", nil, envNone, true, 5, mode.ProfilePrompt, ""},
		{"bare tty hostile preset env", nil, envOverride, true, 5, mode.ProfilePrompt, ""},
		{"gateway profile", nil, envNone, true, 5, mode.ProfileGateway, ""},
		{"native profile", nil, envNone, true, 5, mode.ProfileNative, ""},
		{"prompt flag", []string{"-p", "hi"}, envNone, true, 5, mode.ProfilePrompt, ""},
		{"model flag", []string{"--model", "ext-one"}, envNone, true, 5, mode.ProfilePrompt, ""},
		{"zero models", nil, envNone, true, 0, mode.ProfilePrompt, ""},
		{"non tty", nil, envNone, false, 5, mode.ProfilePrompt, ""},
		{"hybrid p1", nil, envNone, true, 5, mode.ProfilePrompt, "p1"},
		{"pro subcommand", []string{"pro", "status"}, envNone, true, 5, mode.ProfilePrompt, ""},
	}
	postures := []string{"", mode.IntegrationPresetNative, mode.IntegrationPresetTools, mode.IntegrationPresetCustom, "bogus"}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			want := DecideRoute(shape.args, shape.env, shape.tty, shape.modelCount, shape.profile, shape.hybridProfile)
			for _, posture := range postures {
				got := Decide(shape.args, shape.env, shape.tty, shape.modelCount, shape.profile, shape.hybridProfile, posture)
				if got.Route != want {
					t.Errorf("posture %q moved the route: got %s, want %s", posture, got.Route, want)
				}
			}
		})
	}
}

// presetStateFile returns the sandboxed state.json path, pointing AGY_PRO_HOME
// at a fresh temp dir first so the real ~/.agy-pro is never read or written.
func presetStateFile(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	return filepath.Join(home, "state.json")
}

// TestLoadIntegrationPreset covers the persisted half of the seam: a legacy
// state file with no integration_preset key reads as no opinion (not as
// "native"), an explicit preset reads back as itself, and an unrecognized or
// unreadable value is inert.
func TestLoadIntegrationPreset(t *testing.T) {
	// A pre-P5-3 state file: no integration_preset key at all.
	const legacyState = `{"current_mode":"clean","default_profile":"prompt",` +
		`"managed_items":["agents","references","profile.json","config.json"]}`

	cases := []struct {
		name    string
		content string // raw state.json contents; "" = use SaveState with `preset`
		preset  string
		want    string
	}{
		{"legacy file without the key", legacyState, "", ""},
		{"saved native", "", mode.IntegrationPresetNative, mode.IntegrationPresetNative},
		{"saved tools", "", mode.IntegrationPresetTools, mode.IntegrationPresetTools},
		{"saved custom", "", mode.IntegrationPresetCustom, mode.IntegrationPresetCustom},
		{"saved no-opinion", "", "", ""},
		{"hand-edited padded value", `{"integration_preset":" TOOLS "}`, "", mode.IntegrationPresetTools},
		{"hand-edited unknown value", `{"integration_preset":"everything"}`, "", ""},
		{"corrupt state", `{ not valid json`, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateFile := presetStateFile(t)
			if tc.content != "" {
				if err := os.WriteFile(stateFile, []byte(tc.content), 0o600); err != nil {
					t.Fatalf("write state file: %v", err)
				}
			} else if err := mode.SaveState(&mode.State{IntegrationPreset: tc.preset}); err != nil {
				t.Fatalf("SaveState(integration_preset=%q): %v", tc.preset, err)
			}

			if got := loadIntegrationPreset(); got != tc.want {
				t.Errorf("loadIntegrationPreset() = %q; want %q", got, tc.want)
			}
			// The same value through Decide: the legacy file must leave the
			// verdict at no-opinion rather than defaulting to "native".
			got := Decide(nil, func(string) string { return "" }, true, 5, mode.ProfilePrompt, "", loadIntegrationPreset())
			if got.IntegrationPreset != tc.want {
				t.Errorf("Decide(...).IntegrationPreset = %q; want %q", got.IntegrationPreset, tc.want)
			}
			if got.NativePreset != (tc.want == mode.IntegrationPresetNative) {
				t.Errorf("Decide(...).NativePreset = %v; want %v", got.NativePreset, tc.want == mode.IntegrationPresetNative)
			}
		})
	}
}

// TestLegacyStateFileKeepsNoOpinionOnResave pins the forward-compatibility half:
// loading a pre-P5-3 state file and saving it back must not invent an
// integration_preset key, so a launch cannot silently turn "no opinion" into
// "native".
func TestLegacyStateFileKeepsNoOpinionOnResave(t *testing.T) {
	const legacyState = `{"current_mode":"clean","default_profile":"prompt",` +
		`"managed_items":["agents","references","profile.json","config.json"]}`
	stateFile := presetStateFile(t)
	if err := os.WriteFile(stateFile, []byte(legacyState), 0o600); err != nil {
		t.Fatalf("write legacy state file: %v", err)
	}
	st, err := mode.LoadState()
	if err != nil || st == nil {
		t.Fatalf("LoadState on a legacy file = (%v, %v); want a state", st, err)
	}
	if got := loadIntegrationPreset(); got != "" {
		t.Errorf("loadIntegrationPreset() on a legacy file = %q; want \"\"", got)
	}
	if err := mode.SaveState(st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	saved, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read saved state file: %v", err)
	}
	if strings.Contains(string(saved), "integration_preset") {
		t.Errorf("re-saving a legacy state file invented an integration_preset key:\n%s", saved)
	}
}

// ---------------------------------------------------------------------------
// ExecutePassthrough -> Decide -> LaunchP1 wiring (real wirecheck, real mesh)
// ---------------------------------------------------------------------------

// sandboxHybridP1 points AGY_PRO_HOME, the providers file and the user home at
// fresh temp dirs and clears every override that could divert the route, so the
// real ~/.agy-pro and the real user profile are never touched. registryJSON is
// written as the sandbox providers file.
func sandboxHybridP1(t *testing.T, registryJSON string) {
	t.Helper()
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}
	p := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(p, []byte(registryJSON), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	t.Setenv(providers.EnvProviders, p)
	t.Setenv(paths.EnvAgyProActive, "")
	t.Setenv("AGY_PRO_BRIDGE", "")
	t.Setenv("AGY_BRIDGE", "")
	t.Setenv("AGY_LAUNCHER", "")
}

// buildCanaryStubAgy compiles a real "agy" stub that carries all six P1 canary
// literals (so the REAL wirecheck.Check reads GO for P1), answers `--version`
// with the pinned version, and dumps its environment to STUB_ENVFILE. It is a
// distinct file from the test binary, so realagy.Resolve accepts it via
// AGY_PRO_REAL_AGY without a recursion refusal.
func buildCanaryStubAgy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const stubSrc = `package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	canaryCloudCodeURL   = "CLOUD_CODE_URL"
	canaryOverrideLog    = "Overriding CloudCodeServerURL via CLOUD_CODE_URL environment variable"
	canaryFetchModels    = "/v1internal:fetchAvailableModels"
	canaryStreamGenerate = "/v1internal:streamGenerateContent"
	canaryLoadAssist     = "/v1internal:loadCodeAssist"
	canaryPlanGate       = "plan model not specified"
)

// emitCanaries keeps the P1 canary literals linked into the binary (a real
// wirecheck scan reads GO for P1). Guarded by an env var so normal runs never
// write the file; the literals are present in the binary regardless.
func emitCanaries(path string) {
	_ = os.WriteFile(path, []byte(strings.Join([]string{
		canaryCloudCodeURL, canaryOverrideLog, canaryFetchModels,
		canaryStreamGenerate, canaryLoadAssist, canaryPlanGate,
	}, "\n")), 0o600)
}

func main() {
	if p := os.Getenv("STUB_EMIT_CANARIES"); p != "" {
		emitCanaries(p)
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("1.3.5")
		return
	}
	if p := os.Getenv("STUB_ENVFILE"); p != "" {
		var b strings.Builder
		for _, e := range os.Environ() {
			b.WriteString(e)
			b.WriteByte('\n')
		}
		_ = os.WriteFile(p, []byte(b.String()), 0o600)
	}
}
`
	if err := os.WriteFile(src, []byte(stubSrc), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	out := filepath.Join(dir, "agy")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	build := exec.Command("go", "build", "-o", out, src)
	build.Env = append(os.Environ(), "GO111MODULE=off")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, b)
	}
	return out
}

// readEnvDump parses a KEY=VALUE dump (one per line) into a map.
func readEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env dump %s: %v", path, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '='); i >= 0 {
			out[line[:i]] = line[i+1:]
		}
	}
	return out
}

// TestExecutePassthrough_HybridP1ChildGetsCloudCodeURL drives the full chain
// end to end through the REAL wirecheck and a REAL mesh: AGY_PRO_HYBRID=p1 + a
// canary-carrying stub real agy + a temp registry with one external. The stub
// child must receive CLOUD_CODE_URL pointing at the mesh (the mesh served the
// catalog), proving ExecutePassthrough routes a hybrid session to LaunchP1.
func TestExecutePassthrough_HybridP1ChildGetsCloudCodeURL(t *testing.T) {
	sandboxHybridP1(t, `{"providers":[{"name":"stubprov","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"STUB_PROVIDER_KEY","models":[{"id":"ext-one","contextLength":300000}]}]}`)
	stub := buildCanaryStubAgy(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)
	t.Setenv(hybrid.EnvHybridProfile, "p1")
	envFile := filepath.Join(t.TempDir(), "child.env")
	t.Setenv("STUB_ENVFILE", envFile)

	code, err := ExecutePassthrough(stub, nil)
	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
	}
	childEnv := readEnvDump(t, envFile)
	if !strings.HasPrefix(childEnv["CLOUD_CODE_URL"], "http://127.0.0.1:") {
		t.Errorf("the P1 child must receive CLOUD_CODE_URL=<mesh URL>; got %q", childEnv["CLOUD_CODE_URL"])
	}
	if childEnv["AGY_PRO_ACTIVE"] != "1" {
		t.Errorf("AGY_PRO_ACTIVE = %q; want 1", childEnv["AGY_PRO_ACTIVE"])
	}
}

// TestExecutePassthrough_HybridP1ZeroExternalsFallsBack proves the fail-closed
// path: AGY_PRO_HYBRID=p1 + a canary-carrying stub (wirecheck GO) + a temp
// registry with zero externals. LaunchP1 finds nothing to serve, prints the
// native-fallback notice, and execs the stub with no CLOUD_CODE_URL.
func TestExecutePassthrough_HybridP1ZeroExternalsFallsBack(t *testing.T) {
	sandboxHybridP1(t, `{"providers":[]}`)
	stub := buildCanaryStubAgy(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)
	t.Setenv(hybrid.EnvHybridProfile, "p1")
	envFile := filepath.Join(t.TempDir(), "child.env")
	t.Setenv("STUB_ENVFILE", envFile)

	// Capture the fallback notice (ExecutePassthrough wires the mesh/fallback
	// stderr to os.Stderr).
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	code, err := ExecutePassthrough(stub, nil)
	_ = w.Close()
	os.Stderr = old
	notice, _ := io.ReadAll(r)
	_ = r.Close()

	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(string(notice), "nothing to serve") || !strings.Contains(string(notice), "falling back to native") {
		t.Errorf("the zero-externals fallback must print a notice; got %q", notice)
	}
	if _, ok := readEnvDump(t, envFile)["CLOUD_CODE_URL"]; ok {
		t.Error("the zero-externals fallback must not set CLOUD_CODE_URL")
	}
}

// FIX-53: profile p2 with a registered --model must never divert into the
// gateway bridge. This stub carries the P1 canaries only, so the REAL wirecheck
// gate reads NOGO for P2 and hybrid.LaunchP2 falls back to a TRUE native exec
// (no mesh, no GOOGLE_GEMINI_BASE_URL, no AGY_LLM_GATEWAY_*). The live P2 mesh
// path carries the same FIX-53 assertion in
// TestExecutePassthrough_HybridP2NonBareInvocationSkipsPicker.
//
// R5B-FIX-5: the invocation is non-bare (an explicit --model), so the picker
// gate is off; the seams are pinned anyway so the test cannot depend on the
// host's stdio.
func TestExecutePassthrough_HybridP2TrueNativeNoGatewayEnv(t *testing.T) {
	// A registry with a registered external, so a --model <ext> WOULD activate
	// the bridge on the historical passthrough path (the defect FIX-53 removes).
	sandboxHybridP1(t, `{"providers":[{"name":"stubprov","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"STUB_PROVIDER_KEY","models":[{"id":"ext-one","contextLength":300000}]}]}`)
	stubP2BareNonInteractive(t)
	stub := buildCanaryStubAgy(t)
	t.Setenv(hybrid.EnvHybridProfile, "p2")
	envFile := filepath.Join(t.TempDir(), "child.env")
	t.Setenv("STUB_ENVFILE", envFile)

	code, err := ExecutePassthrough(stub, []string{"--model", "ext-one"})
	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
	}
	childEnv := readEnvDump(t, envFile)
	for _, k := range []string{
		"AGY_LLM_GATEWAY_URL", "AGY_LLM_GATEWAY_WIRE_PROTOCOL",
		"AGY_LLM_GATEWAY_API_KEY", "AGY_LLM_GATEWAY_MODELS",
	} {
		if v, ok := childEnv[k]; ok {
			t.Errorf("the P2 true-native child must not carry %s (FIX-53); got %q", k, v)
		}
	}
	if childEnv["AGY_PRO_ACTIVE"] != "1" {
		t.Errorf("AGY_PRO_ACTIVE = %q; want 1", childEnv["AGY_PRO_ACTIVE"])
	}
}

// FIX-60: the P2 native fallback inside hybrid.LaunchP2 (p2NativeFallback, the
// path that replaced the launcher's own executeTrueNative in U9b) must apply the
// SAME known-preset scrub union the P1 fallback uses, so an unreadable registry
// (reg == nil) cannot leak an ambient known-preset key VALUE into the child.
// Mirrors TestLaunchP1_RegistryErrorFallbackScrubsKnownPresetKeys.
//
// R5B-FIX-5: this is a BARE invocation, so the P2 picker gate is live. The
// stdinIsTTY/pickerInventory seams are pinned here: on a developer's console the
// default picker would resolve the real agy, spawn `agy models`, and block on
// the console read for up to pickerTimeout (60s).
func TestExecutePassthrough_HybridP2NilRegistryScrubsPresetKeys(t *testing.T) {
	// An unreadable providers file -> providers.Load fails -> reg == nil.
	sandboxHybridP1(t, "{ not valid json")
	stubP2BareNonInteractive(t)
	stub := buildCanaryStubAgy(t)
	t.Setenv(hybrid.EnvHybridProfile, "p2")
	t.Setenv("OPENAI_API_KEY", "sk-planted-openai-secret")
	envFile := filepath.Join(t.TempDir(), "child.env")
	t.Setenv("STUB_ENVFILE", envFile)

	code, err := ExecutePassthrough(stub, nil)
	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
	}
	if v, ok := readEnvDump(t, envFile)["OPENAI_API_KEY"]; ok {
		t.Errorf("the P2 true-native child must scrub the known-preset key when reg=nil (FIX-60); got %q", v)
	}
}

// FIX-65: the historical passthrough native-path error must NOT echo the
// resolved real-agy binary path. An empty registry routes to RouteNative; a
// non-existent, key-shaped binary path makes the exec fail, and the error must
// carry no raw path (a class label + the masked underlying error).
func TestExecutePassthrough_MasksRealAgyPathInError(t *testing.T) {
	sandboxHybridP1(t, `{"providers":[]}`)
	t.Setenv(hybrid.EnvHybridProfile, "") // ensure the plain native path, not hybrid
	const secret = "sk-planted-realagy-path-secret-shape-0000000000"

	code, err := ExecutePassthrough(secret, nil)
	if err == nil {
		t.Fatalf("a non-existent real agy must error; got code %d", code)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the passthrough error must mask the real-agy path (FIX-65); got %v", err)
	}
}

// P5-4 end-to-end: the integrations posture on disk must not gate a launch.
// This drives the REAL production chain (ExecutePassthrough -> Decide ->
// loadIntegrationPreset -> loadDefaultProfile -> hybrid.LaunchP1) with a
// canary-carrying stub real agy and a real mesh, once per posture. Every posture
// must reach the same front door and hand the child the same CLOUD_CODE_URL: the
// preset is a posture signal, not a launch selector (ratified: no second
// binary).
func TestExecutePassthrough_IntegrationPresetDoesNotGateLaunch(t *testing.T) {
	sandboxHybridP1(t, `{"providers":[{"name":"stubprov","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"STUB_PROVIDER_KEY","models":[{"id":"ext-one","contextLength":300000}]}]}`)
	stub := buildCanaryStubAgy(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)
	t.Setenv(hybrid.EnvHybridProfile, "p1")
	envFile := filepath.Join(t.TempDir(), "child.env")
	t.Setenv("STUB_ENVFILE", envFile)

	for _, preset := range []string{"", mode.IntegrationPresetNative, mode.IntegrationPresetTools, mode.IntegrationPresetCustom} {
		t.Run("preset="+preset, func(t *testing.T) {
			if err := mode.SaveState(&mode.State{IntegrationPreset: preset}); err != nil {
				t.Fatalf("SaveState(integration_preset=%q): %v", preset, err)
			}
			if got := loadIntegrationPreset(); got != preset {
				t.Fatalf("loadIntegrationPreset() = %q; want %q", got, preset)
			}
			if err := os.Remove(envFile); err != nil && !os.IsNotExist(err) {
				t.Fatalf("reset child env dump: %v", err)
			}
			code, err := ExecutePassthrough(stub, nil)
			if err != nil || code != 0 {
				t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
			}
			childEnv := readEnvDump(t, envFile)
			if !strings.HasPrefix(childEnv["CLOUD_CODE_URL"], "http://127.0.0.1:") {
				t.Errorf("preset %q must not divert the P1 route; CLOUD_CODE_URL = %q",
					preset, childEnv["CLOUD_CODE_URL"])
			}
			if childEnv["AGY_PRO_ACTIVE"] != "1" {
				t.Errorf("preset %q: AGY_PRO_ACTIVE = %q; want 1", preset, childEnv["AGY_PRO_ACTIVE"])
			}
		})
	}
}
