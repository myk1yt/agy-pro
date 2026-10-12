package launcher

import (
	"testing"

	"agypro/internal/mode"
)

func TestDecide(t *testing.T) {
	envNone := func(string) string { return "" }
	envLauncherOff := func(k string) string {
		if k == "AGY_LAUNCHER" {
			return "0"
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
		name       string
		args       []string
		env        func(string) string
		tty        bool
		modelCount int
		profile    string
		want       Route
	}{
		// 1. Registered models == 0: launcher skipped, native immediately.
		{"no models plain tty prompt", nil, envNone, true, 0, mode.ProfilePrompt, RouteNative},
		{"no models gateway", nil, envNone, true, 0, mode.ProfileGateway, RouteNative},
		{"no models with args", []string{"agents"}, envNone, true, 0, mode.ProfilePrompt, RouteNative},

		// 2. pro subcommand is never launched through the menu.
		{"pro args", []string{"pro", "status"}, envNone, true, 5, mode.ProfilePrompt, RoutePassthrough},

		// 3. Explicit bridge request defers to the historical rules.
		{"bridge flag", []string{"--bridge"}, envNone, true, 5, mode.ProfilePrompt, RoutePassthrough},
		{"bridge short flag", []string{"-b"}, envNone, true, 5, mode.ProfilePrompt, RoutePassthrough},
		{"bridge env", nil, envBridge, true, 5, mode.ProfilePrompt, RoutePassthrough},
		{"bridge alt env", nil, envBridgeAlt, true, 5, mode.ProfilePrompt, RoutePassthrough},
		{"bridge flag under gateway", []string{"--bridge"}, envNone, true, 5, mode.ProfileGateway, RoutePassthrough},

		// 4. Model targeting defers to the historical rules (native Gemini
		// requests must keep working even under the gateway profile).
		{"model flag", []string{"--model", "glm-5.3-flash-flex"}, envNone, true, 5, mode.ProfilePrompt, RoutePassthrough},
		{"model flag equals", []string{"--model=glm-5.3-flash-flex"}, envNone, true, 5, mode.ProfilePrompt, RoutePassthrough},
		{"model short", []string{"-m", "gemini-3.8-flash"}, envNone, true, 5, mode.ProfileGateway, RoutePassthrough},
		{"model short equals", []string{"-m=gemini-3.8-flash"}, envNone, true, 5, mode.ProfileNative, RoutePassthrough},

		// 5. Gateway profile forces the bridge for plain invocations.
		{"gateway plain", nil, envNone, true, 5, mode.ProfileGateway, RouteBridge},
		{"gateway with passthrough args", []string{"agents"}, envNone, true, 5, mode.ProfileGateway, RouteBridge},
		{"gateway non-tty", nil, envNone, false, 5, mode.ProfileGateway, RouteBridge},
		{"gateway launcher off", nil, envLauncherOff, true, 5, mode.ProfileGateway, RouteBridge},
		{"gateway prompt flag", []string{"-p", "hello"}, envNone, true, 5, mode.ProfileGateway, RouteBridge},

		// 6. Native profile runs straight through.
		{"native plain", nil, envNone, true, 5, mode.ProfileNative, RouteNative},
		{"native non-tty", nil, envNone, false, 5, mode.ProfileNative, RouteNative},
		{"native launcher menu would fit", nil, envNone, true, 5, mode.ProfileNative, RouteNative},

		// 7. Prompt profile: one-shot prompts never block on the menu.
		{"prompt flag", []string{"-p", "hello"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"prompt flag long", []string{"--prompt", "hello"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"prompt flag equals", []string{"--prompt=hello"}, envNone, true, 5, "", RouteNative},
		// F-4: --print is agy's canonical one-shot (-p is its alias).
		{"print flag", []string{"--print", "hello"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"print flag equals", []string{"--print=hello"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"print flag empty profile", []string{"--print", "x"}, envNone, true, 5, "", RouteNative},
		{"print flag unknown profile", []string{"--print=x"}, envNone, true, 5, "bogus", RouteNative},

		// 8. AGY_LAUNCHER=0 disables only the menu.
		{"launcher disabled", nil, envLauncherOff, true, 5, mode.ProfilePrompt, RouteNative},

		// 9. Pipes and CI are zero-delay native.
		{"non tty", nil, envNone, false, 5, mode.ProfilePrompt, RouteNative},
		{"non tty with args", []string{"agents"}, envNone, false, 5, mode.ProfilePrompt, RouteNative},

		// 10. F-10: the menu fires only for bare `agy` (zero arguments).
		// Non-plain invocations must never show the banner.
		{"menu plain", nil, envNone, true, 5, mode.ProfilePrompt, RouteMenu},
		{"menu empty profile", nil, envNone, true, 5, "", RouteMenu},
		{"menu unknown profile", nil, envNone, true, 5, "bogus", RouteMenu},
		{"menu single model", nil, envNone, true, 1, mode.ProfilePrompt, RouteMenu},
		{"non-plain subcommand agents", []string{"agents"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"non-plain version flag", []string{"--version"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"non-plain help flag", []string{"--help"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"non-plain help short", []string{"-h"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
		{"non-plain equals flag", []string{"--foo=bar"}, envNone, true, 5, mode.ProfilePrompt, RouteNative},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.args, tc.env, tc.tty, tc.modelCount, tc.profile)
			if got != tc.want {
				t.Errorf("Decide(%v, tty=%v, models=%d, profile=%q) = %s; want %s",
					tc.args, tc.tty, tc.modelCount, tc.profile, got, tc.want)
			}
		})
	}
}

func TestDecideNilLookupEnv(t *testing.T) {
	if got := Decide(nil, nil, true, 3, mode.ProfilePrompt); got != RouteMenu {
		t.Errorf("Decide with nil lookupEnv = %s; want menu", got)
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
