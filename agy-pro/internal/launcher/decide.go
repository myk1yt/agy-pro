package launcher

import (
	"strings"

	"agypro/internal/mode"
)

// Route is the terminal routing decision for one `agy` invocation.
type Route int

const (
	// RoutePassthrough keeps the historical behavior: no launcher, and the
	// existing ShouldActivateBridge rules decide native vs bridge (explicit
	// --bridge/-b/env flags or a --model targeting a registered model).
	RoutePassthrough Route = iota
	// RouteNative runs the real binary with no launcher and no gateway env.
	RouteNative
	// RouteBridge runs the real binary behind the local gateway, no launcher.
	RouteBridge
	// RouteMenu shows the interactive fast launcher.
	RouteMenu
	// RouteFanout runs the multi-model swarm. Decide never returns it;
	// only ResolveMenuChoice does.
	RouteFanout
	// RouteHybridP1 runs the native-coexistence P1 front door (picker
	// coexistence): a per-session /v1internal mesh behind CLOUD_CODE_URL
	// (hybrid.LaunchP1). Decide returns it only for a PLAIN invocation whose
	// effective hybrid profile is p1.
	RouteHybridP1
	// RouteHybridP2 runs the P2 (subagent) front door: a per-session
	// /v1beta mesh behind GOOGLE_GEMINI_BASE_URL plus a per-session
	// --app_data_dir holding the gemini-mode settings document
	// (hybrid.LaunchP2). Decide returns it only for a PLAIN invocation whose
	// effective hybrid profile is p2; the passthrough path shows the combined
	// picker for a bare interactive session and hands everything else straight
	// to the front door.
	RouteHybridP2
)

// String renders the route name for diagnostics and tests.
func (r Route) String() string {
	switch r {
	case RoutePassthrough:
		return "passthrough"
	case RouteNative:
		return "native"
	case RouteBridge:
		return "bridge"
	case RouteMenu:
		return "menu"
	case RouteFanout:
		return "fanout"
	case RouteHybridP1:
		return "hybrid-p1"
	case RouteHybridP2:
		return "hybrid-p2"
	}
	return "unknown"
}

// EnvIntegrationPreset is the environment override for the integrations posture
// the decision resolves. It takes the same three words the persisted key
// carries (native|tools|custom) and takes precedence over them.
const EnvIntegrationPreset = "AGY_PRO_INTEGRATION_PRESET"

// Decision is the full verdict for one `agy` invocation: the Route the launcher
// acts on, plus the integrations posture the same call resolves but must not act
// on yet (P5-4: there is no second binary, so the preset never selects a binary;
// it is a posture signal for the T8 surfaces and the P6 docs).
//
// FOUR different persisted values are called "native" in this tree. They sit on
// separate axes (ratified decision 6: orthogonal, documented) and none of them
// implies another:
//
//   - mode.State.DefaultProfile ("native"|"prompt"|"gateway") is the LAUNCH
//     PROFILE: which door a plain invocation takes (RouteNative, RouteBridge, or
//     the menu). It is the routing axis proper, and none of the four ever selects
//     a different binary (ratified: there is no second binary).
//   - mode.State.HybridProfile ("p1"|"p2"|"none") is the NATIVE-COEXISTENCE
//     PROFILE: whether a per-session mesh serves native picks, i.e. whether the
//     session reaches RouteHybridP1/RouteHybridP2. The word "native" appears
//     only in the axis name; "none" here means no hybrid mesh.
//   - mode.State.CurrentMode ("full"|"clean"|"restored") is the DEPLOYED PROFILE
//     MODE: which agents/references/profile.json/config.json tree `mode` last
//     laid down. It never routes an invocation.
//   - mode.State.IntegrationPreset ("native"|"tools"|"custom") is the
//     INTEGRATIONS POSTURE: what internal/integrations last deployed or
//     retracted. "native" here means DEPLOY NOTHING, not "run the native
//     binary": that is RouteNative and DefaultProfile's word, and the two are
//     independent (a native preset behind a gateway launch profile is legal).
//
// Decision.NativePreset carries the fourth meaning only. A consumer asking "is
// the tools deployment off" reads NativePreset; a consumer asking "which door
// does this invocation take" reads Route.
type Decision struct {
	// Route is the routing verdict. The integrations posture never changes it.
	Route Route
	// IntegrationPreset is the resolved posture, one of
	// mode.IntegrationPresetNative/Tools/Custom, or "" when nothing claims it (no
	// env override and no stored key: the legacy no-opinion posture every build
	// before P5-3 had).
	IntegrationPreset string
	// NativePreset is true only for an explicit "native" posture. It is false for
	// "tools", for "custom" AND for the absent no-opinion posture, so a caller
	// that must tell absent from tools has to read IntegrationPreset.
	NativePreset bool
}

// Decide maps (args, env, stdin TTY-ness, registered model count, default
// profile, effective hybrid profile, integrations preset) to the routing for a
// non-`pro` agy invocation. It is pure: the caller supplies the environment
// lookup and TTY flag so tests can drive every branch without touching the real
// console or ~/.agy-pro.
//
// The integrations preset is resolved with the precedence EnvIntegrationPreset
// override > persisted key > absent and is carried on the returned Decision
// only: Decide does not gate the Route on it (P5-4; there is no second binary).
// Rule 11 states that resolution; rules 1-10 are the routing.
//
// Rules, in order (precedence is explicit and load-bearing):
//  1. `pro` subcommand: passthrough (never hybrid-routed).
//  2. Hybrid profile (native coexistence, design §2): an active profile is a
//     SESSION-level choice that must cover EVERY invocation shape (the TUI, a
//     -p/--print one-shot, a --model run, the `models` subcommand, a pipe/CI
//     run). It sits immediately after the pro check and BEFORE the
//     modelCount / bridge / model-flag / prompt-flag / menu rules, so a hybrid
//     session reaches its front door for any invocation. The single exception
//     is an explicit per-invocation bridge request (AGY_PRO_BRIDGE=1 /
//     AGY_BRIDGE=1 / --bridge / -b): that keeps its meaning and falls through
//     to the explicit-bridge rule below. hybridProfile is "p1"/"p2"/"" (the
//     effective profile from hybrid.ResolveProfile; "" or "none" = no profile).
//  3. No registered models: nothing to route, go native (zero delay).
//  4. Explicit bridge request (--bridge/-b/AGY_PRO_BRIDGE=1/AGY_BRIDGE=1) or
//     a model flag (--model/-m): defer to the existing conditional-bridge
//     rules so native Gemini requests keep working unchanged.
//  5. Default profile gateway: force the bridge for plain invocations.
//  6. Default profile native: run straight through.
//  7. One-shot prompt flags (-p/--prompt/--print) or AGY_LAUNCHER=0: no menu.
//  8. Any argument at all (agents, --version, --help, ...): this is not a bare
//     interactive session, so the menu is skipped and the native default runs.
//  9. stdin is not a TTY (pipe/CI): no menu, zero delay.
//  10. Bare `agy` on a TTY: the fast launcher is opt-IN, not opt-out. The menu
//     shows only when the user sets AGY_LAUNCHER=1; every other bare launch
//     falls through to RouteNative.
//  11. Integrations posture (carried on the Decision, never routed on): an
//     EnvIntegrationPreset value naming native/tools/custom wins over the
//     persisted integration_preset key, which wins over absent. An override that
//     is set but names no preset is ignored and the persisted key decides - the
//     same tolerance hybrid.ResolveProfile holds for AGY_PRO_HYBRID, so a typo
//     cannot silently drop a posture the user chose in the hub. Absent stays
//     distinct from "native": absent is no opinion, not "deploy nothing".
func Decide(args []string, lookupEnv func(string) string, stdinIsTTY bool, modelCount int, profile, hybridProfile, integrationPreset string) Decision {
	preset := resolveIntegrationPreset(lookupEnv, integrationPreset)
	return Decision{
		Route:             decideRoute(args, lookupEnv, stdinIsTTY, modelCount, profile, hybridProfile),
		IntegrationPreset: preset,
		NativePreset:      preset == mode.IntegrationPresetNative,
	}
}

// DecideRoute is Decide without the integrations posture: the routing alone, for
// callers that only route. It is exactly Decide(...).Route when no posture is in
// play.
func DecideRoute(args []string, lookupEnv func(string) string, stdinIsTTY bool, modelCount int, profile, hybridProfile string) Route {
	return decideRoute(args, lookupEnv, stdinIsTTY, modelCount, profile, hybridProfile)
}

// resolveIntegrationPreset applies the posture precedence: a recognized
// EnvIntegrationPreset override wins, then the persisted key, then "" (the legacy
// no-opinion posture). Unrecognized values on either side are inert
// (mode.NormalizeIntegrationPreset drops them) and are never reinterpreted, so a
// hand-edited state file or a typo'd override cannot invent a posture.
func resolveIntegrationPreset(lookupEnv func(string) string, persisted string) string {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(EnvIntegrationPreset)); raw != "" {
			if p := mode.NormalizeIntegrationPreset(raw); p != "" {
				return p
			}
		}
	}
	return mode.NormalizeIntegrationPreset(persisted)
}

// decideRoute is Decide's routing body: rules 1-10, and nothing about the
// integrations posture.
func decideRoute(args []string, lookupEnv func(string) string, stdinIsTTY bool, modelCount int, profile, hybridProfile string) Route {
	if lookupEnv == nil {
		lookupEnv = func(string) string { return "" }
	}
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "pro") {
		return RoutePassthrough
	}
	// Hybrid profile: a session-level choice that covers every invocation
	// shape, evaluated before the modelCount/bridge/model-flag/prompt/menu
	// rules. An explicit per-invocation bridge request is the only carve-out
	// and keeps its meaning via the explicit-bridge rule below.
	switch strings.ToLower(strings.TrimSpace(hybridProfile)) {
	case "p1":
		if !explicitBridgeRequested(args, lookupEnv) {
			return RouteHybridP1
		}
	case "p2":
		if !explicitBridgeRequested(args, lookupEnv) {
			return RouteHybridP2
		}
	}
	if modelCount <= 0 {
		return RouteNative
	}
	if explicitBridgeRequested(args, lookupEnv) || hasModelFlag(args) {
		return RoutePassthrough
	}
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "gateway":
		return RouteBridge
	case "native":
		return RouteNative
	}
	if hasPromptFlag(args) || launcherDisabled(lookupEnv) || !stdinIsTTY || len(args) > 0 {
		return RouteNative
	}
	if launcherEnabled(lookupEnv) {
		return RouteMenu
	}
	return RouteNative
}

// ResolveMenuChoice maps launcher input to a route. Empty input (timeout or
// Enter) and "1" pick the native default; "2" forces the bridge; "3" runs the
// swarm. Any other input also falls back to native so a stray keypress can
// never leave the user stranded.
func ResolveMenuChoice(choice string) Route {
	switch strings.TrimSpace(choice) {
	case "2":
		return RouteBridge
	case "3":
		return RouteFanout
	default:
		return RouteNative
	}
}

// explicitBridgeRequested reports whether the invocation asks for the local
// bridge through a flag or environment variable.
func explicitBridgeRequested(args []string, lookupEnv func(string) string) bool {
	if lookupEnv("AGY_PRO_BRIDGE") == "1" || lookupEnv("AGY_BRIDGE") == "1" {
		return true
	}
	for _, a := range args {
		if a == "--bridge" || a == "-b" {
			return true
		}
	}
	return false
}

// launcherDisabled reports whether the fast launcher is switched off via
// AGY_LAUNCHER=0. Still honored, but redundant since the menu became opt-in:
// anything that is not "1" already routes native.
func launcherDisabled(lookupEnv func(string) string) bool {
	return lookupEnv("AGY_LAUNCHER") == "0"
}

// launcherEnabled reports whether the fast launcher was explicitly opted in
// via AGY_LAUNCHER=1. Only then does a bare `agy` on a TTY show the menu.
func launcherEnabled(lookupEnv func(string) string) bool {
	return lookupEnv("AGY_LAUNCHER") == "1"
}

// hasModelFlag reports whether the invocation targets a specific model
// (--model X, --model=X, -m X, -m=X).
func hasModelFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--model" || a == "-m" {
			return true
		}
		if strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "-m=") {
			return true
		}
	}
	return false
}

// hasPromptFlag reports whether the invocation is a one-shot prompt run
// (--print / --prompt, with -p as the alias of --print), which must never
// block on an interactive menu. Both the space form (`--print x`) and the
// equals form (`--print=x`) count.
func hasPromptFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-p" || a == "--print" || a == "--prompt" {
			return true
		}
		if strings.HasPrefix(a, "-p=") || strings.HasPrefix(a, "--print=") || strings.HasPrefix(a, "--prompt=") {
			return true
		}
	}
	return false
}
