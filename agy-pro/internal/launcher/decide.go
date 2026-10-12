package launcher

import "strings"

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
	}
	return "unknown"
}

// Decide maps (args, env, stdin TTY-ness, registered model count, default
// profile) to the routing for a non-`pro` agy invocation. It is pure: the
// caller supplies the environment lookup and TTY flag so tests can drive
// every branch without touching the real console or ~/.agy-pro.
//
// Rules, in order:
//  1. No registered models: nothing to route, go native (zero delay).
//  2. Explicit bridge request (--bridge/-b/AGY_PRO_BRIDGE=1/AGY_BRIDGE=1) or
//     a model flag (--model/-m): defer to the existing conditional-bridge
//     rules so native Gemini requests keep working unchanged.
//  3. Default profile gateway: force the bridge for plain invocations.
//  4. Default profile native: run straight through.
//  5. One-shot prompt flags (-p/--prompt/--print) or AGY_LAUNCHER=0: no menu.
//  6. Any argument at all (agents, --version, --help, ...): this is not a bare
//     interactive session, so the menu is skipped and the native default runs.
//  7. stdin is not a TTY (pipe/CI): no menu, zero delay.
//  8. Otherwise (bare `agy` on a TTY): show the fast launcher.
func Decide(args []string, lookupEnv func(string) string, stdinIsTTY bool, modelCount int, profile string) Route {
	if lookupEnv == nil {
		lookupEnv = func(string) string { return "" }
	}
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "pro") {
		return RoutePassthrough
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
	return RouteMenu
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
// AGY_LAUNCHER=0.
func launcherDisabled(lookupEnv func(string) string) bool {
	return lookupEnv("AGY_LAUNCHER") == "0"
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
