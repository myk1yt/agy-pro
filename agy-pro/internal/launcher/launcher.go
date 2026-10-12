// Package launcher locates the underlying real agy binary and executes it
// transparently, preserving stdio handles, exit codes, and preventing recursion.
package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/bridge"
	"agypro/internal/fanout"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// ErrRecursionDetected is returned when agy-pro detects an infinite call loop.
// D-1 (contract §2.2.1): the sentinels ARE realagy's errors - the single
// source of truth - so errors.Is keeps working at every call site
// (main.go:41, launcher tests) whether the error originates in realagy.Resolve
// or in the passthrough recursion guard.
var ErrRecursionDetected = realagy.ErrRecursion

// ErrRealAgyNotFound is returned when the original agy binary cannot be located.
var ErrRealAgyNotFound = realagy.ErrNotFound

// FindRealAgy locates the authentic agy binary. Delegates to realagy.Resolve
// (contract §2.2.1, D-1 single-logic guarantee): AGY_PRO_REAL_AGY > PATH scan
// (excluding the shim) > paths.DefaultRealAgyPath() > Windows
// ~/.gemini/bin/agy.exe.
func FindRealAgy() (string, error) {
	return realagy.Resolve()
}

// resolveRealAgy is the pure, injectable core behind FindRealAgy; it delegates
// to realagy.ResolveWith so the resolution logic has exactly one home (D-1).
func resolveRealAgy(selfPath, envReal, pathEnv, fallback string) (string, error) {
	return realagy.ResolveWith(selfPath, envReal, pathEnv, fallback)
}

// ExecutePassthrough runs the real agy binary with the given arguments,
// connecting standard I/O streams directly to preserve full TUI and interactive CLI behavior.
// It activates the local HTTP bridge only when an external model is targeted or explicit
// bridge mode is requested, ensuring native Gemini models are 100% preserved during normal use.
// Before that historical routing runs, Decide may divert a plain invocation to the
// interactive fast launcher (menu), force the bridge (gateway profile or menu [2]),
// or start the multi-model swarm (menu [3]).
func ExecutePassthrough(realAgyPath string, args []string) (int, error) {
	// Guard against recursive execution loop
	if os.Getenv(paths.EnvAgyProActive) == "1" {
		return 1, fmt.Errorf("%w: active guard triggered for %s", ErrRecursionDetected, realAgyPath)
	}

	reg, _ := providers.Load()
	modelCount := CountEnabledModels(reg)

	// One shared input owner for the whole interactive flow: the relay hands
	// the launcher its lines and everything else to the real agy child, so a
	// menu deadline can never lose or steal the child's first input (F-2).
	rel := newStdinRelay(os.Stdin)
	childStdin := io.Reader(os.Stdin)

	forceBridge := false
	forcedNativeID := ""
	switch Decide(args, os.Getenv, stdinIsTTY(), modelCount, loadDefaultProfile()) {
	case RouteMenu:
		names := EnabledProviderNames(reg)
		choice := ShowMenu(os.Stdout, os.Stdin, rel, modelCount, ProviderSummary(names), MenuTimeout)
		switch choice {
		case RouteBridge:
			// Plan §4.2 B1: choice [2] opens the combined model sub-menu.
			// Routing is by Entry.Kind provenance (F-BRIDGE): a native pick
			// bypasses ShouldActivateBridge entirely; an external pick keeps
			// the forceBridge path; an aborted pick keeps the legacy [2]
			// bridge behavior.
			pick := showModelPicker(os.Stdout, rel, pickerInventory(reg))
			plan := planFromPick(pick)
			forcedNativeID = plan.ForcedNativeID
			if plan.ForceBridge {
				forceBridge = true
				args = append(args, plan.ModelArgs...)
			}
		case RouteFanout:
			code, handled, err := runSwarmFromMenu(rel, os.Stdout, os.Stderr)
			if handled || err != nil {
				return code, err
			}
			// Empty prompt: fall through to the native default.
		}
		if rel.Used() {
			childStdin = rel.ChildStdin()
		}
	case RouteBridge:
		forceBridge = true
	}

	// Forced-native picker pick: pure native session, scrubbed env, no bridge
	// (contract §2.7 F-BRIDGE), regardless of registry contents.
	if forcedNativeID != "" {
		return executeForcedNative(realAgyPath, forcedNativeID, childStdin, reg)
	}

	activateBridge, cleanArgs := ShouldActivateBridge(args, reg)
	if forceBridge && modelCount > 0 {
		activateBridge = true
	}

	cmd := spawnRealAgy(realAgyPath, cleanArgs...)
	cmd.Stdin = childStdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Propagate recursion guard to child process. The snapshot is taken before
	// the bridge starts on purpose: startBridgeOrFallback pins
	// AGY_PRO_WORKSPACE on this process (R1-3), and the child must never
	// receive the shim's workspace root (S-3, four gateway entries only).
	childEnv := os.Environ()
	childEnv = append(childEnv, paths.EnvAgyProActive+"=1")

	// Inject LLM Gateway bridge if conditions are met
	if activateBridge && reg != nil {
		// Provider keys are deliberately NOT injected into the child (S-3):
		// the bridge resolves them in this process at request time
		// (bridge.go Provider.KeyValue), and the child only needs the
		// session-scoped AGY_LLM_GATEWAY_* values below. Copying every
		// registry key into the child env would hand them to anything the
		// child spawns (agent tools, MCP servers).
		modelIDs := CollectModelIDs(reg)
		if len(modelIDs) > 0 {
			sessionKey, keyErr := bridge.GenerateSessionKey()
			if keyErr != nil {
				return 1, keyErr
			}
			gatewayEnv, stopBridge := startBridgeOrFallback(reg, sessionKey, ResolveBridgeTools(), os.Stderr)
			if stopBridge != nil {
				defer stopBridge()
			}
			for _, ge := range gatewayEnv {
				childEnv = setEnvKV(childEnv, ge)
			}
		}
	}

	cmd.Env = childEnv

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("failed to run real agy at %q: %w", realAgyPath, err)
	}

	return 0, nil
}

// startBridgeFunc is the bridge starter seam; tests stub it to exercise the
// failure fallback without binding a port.
var startBridgeFunc = bridge.StartBridge

// stdinIsTTY reports whether the launcher should consider stdin interactive.
// It is a seam so route tests can drive the TTY and piped paths deterministically.
var stdinIsTTY = func() bool { return isTerminal(os.Stdin) }

// workspaceEnv is the bridge tool loop's workspace root. The shim sets it on
// its OWN process env (never on the child agy env) so the bridge resolves tool
// paths against the directory `agy` was started in (R1-3).
const workspaceEnv = "AGY_PRO_WORKSPACE"

// bridgeToolsEnv overrides the stored bridge-tools allow-list for one session
// (an already-set value passes through untouched; nothing is injected into the
// child env).
const bridgeToolsEnv = "AGY_PRO_BRIDGE_TOOLS"

// ensureBridgeWorkspace pins the tool loop's workspace root to the shim's
// current directory. Precedence is env-over-cwd: an AGY_PRO_WORKSPACE already
// present in the environment is never overwritten, so an orchestrator can point
// a session at another workspace. The value is written to this process only:
// BuildGatewayEnv deliberately keeps it out of the child env (S-3).
func ensureBridgeWorkspace() {
	if strings.TrimSpace(os.Getenv(workspaceEnv)) != "" {
		return
	}
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		return
	}
	_ = os.Setenv(workspaceEnv, wd)
}

// ResolveBridgeTools resolves the gateway tool-loop allow-list with the
// documented precedence: AGY_PRO_BRIDGE_TOOLS env > stored bridge_tools key >
// default read,search. Parsing is fail-closed: an unknown token, a blank spec,
// or "none" leaves the loop disabled (bridge.BridgeToolsConfig zero value)
// instead of silently widening the allow-list.
func ResolveBridgeTools() bridge.BridgeToolsConfig {
	spec := strings.TrimSpace(os.Getenv(bridgeToolsEnv))
	if spec == "" {
		if st, err := mode.LoadState(); err == nil && st != nil {
			spec = st.EffectiveBridgeTools()
		}
	}
	if spec == "" {
		spec = mode.DefaultBridgeTools
	}
	if strings.EqualFold(spec, mode.BridgeToolsNone) {
		return bridge.BridgeToolsConfig{}
	}
	normalized, err := agenttools.ParseToolTokens(spec)
	if err != nil {
		return bridge.BridgeToolsConfig{}
	}
	var tools []string
	for _, tok := range strings.Split(normalized, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			tools = append(tools, tok)
		}
	}
	if len(tools) == 0 {
		return bridge.BridgeToolsConfig{}
	}
	return bridge.BridgeToolsConfig{Enabled: true, Tools: tools}
}

// startBridgeOrFallback starts the local gateway bridge and returns the
// AGY_LLM_GATEWAY_* entries to inject into the child environment. When the
// bridge cannot start (or hands back no stop function) the explicit
// gateway/-b/menu[2] route must not fall back to native silently: a warning
// goes to stderr and the fallback is recorded on the route log, and the real
// agy still boots without gateway env.
func startBridgeOrFallback(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig, errw io.Writer) (gatewayEnv []string, stop func()) {
	ensureBridgeWorkspace()
	bridgeURL, stopFunc, err := startBridgeFunc(reg, sessionKey, toolsCfg)
	if err == nil && stopFunc != nil {
		return BuildGatewayEnv(reg, bridgeURL, sessionKey), stopFunc
	}
	errText := "bridge returned no stop function"
	if err != nil {
		errText = err.Error()
	}
	fmt.Fprintf(errw, "[agy-pro] bridge failed: %s - falling back to native\n", errText)
	recordRoute("bridge-fallback: " + errText)
	return nil, nil
}

// recordRoute appends one entry to <agy-pro home>/route.log so routing
// fallbacks stay diagnosable after the fact. Best-effort: a logging failure
// never affects the invocation.
func recordRoute(entry string) {
	home, err := paths.AgyProHome()
	if err != nil {
		return
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "route.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), entry)
}

// runSwarmFromMenu handles launcher choice [3]: one line of prompt input and
// the default fanout seat rotation (no -models override). handled is false
// when the prompt was left empty, which cancels back to the native default.
// The relay is the shared stdin owner so the prompt sees whatever the menu
// choice left over and the swarm (or a later native child) receives the rest.
func runSwarmFromMenu(rel *stdinRelay, out, errw io.Writer) (code int, handled bool, err error) {
	fmt.Fprint(out, "Swarm prompt: ")
	line, _ := rel.readLine(0)
	prompt := strings.TrimSpace(line)
	if prompt == "" {
		fmt.Fprintln(errw, "[agy-pro] empty swarm prompt; continuing with native Gemini")
		return 0, false, nil
	}
	return fanout.Run([]string{"-prompt", prompt}, rel.ChildStdin(), out, errw), true, nil
}

// loadDefaultProfile reads the persisted plain-`agy` launch profile. Missing
// or unreadable state falls back to the prompt profile (fast launcher).
func loadDefaultProfile() string {
	st, err := mode.LoadState()
	if err != nil || st == nil {
		return mode.ProfilePrompt
	}
	return st.EffectiveDefaultProfile()
}

// CountEnabledModels counts distinct enabled model entries across all
// providers ("model (provider)" identity).
func CountEnabledModels(reg *providers.Registry) int {
	if reg == nil {
		return 0
	}
	seen := make(map[string]bool)
	count := 0
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if !m.IsEnabled() {
				continue
			}
			dn := providers.DisplayName(m.ID, p.Name)
			if !seen[dn] {
				seen[dn] = true
				count++
			}
		}
	}
	return count
}

// EnabledProviderNames lists the provider names that contribute at least one
// enabled model, in registry order.
func EnabledProviderNames(reg *providers.Registry) []string {
	if reg == nil {
		return nil
	}
	var names []string
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if m.IsEnabled() {
				names = append(names, p.Name)
				break
			}
		}
	}
	return names
}

// ShouldActivateBridge determines whether the local HTTP bridge and AGY_LLM_GATEWAY_*
// environment variables should be activated for this invocation.
// It returns (activate bool, cleanedArgs []string).
//
// Rules:
//  1. Explicit bridge mode: enabled if AGY_PRO_BRIDGE=1 or AGY_BRIDGE=1 in environment,
//     or if CLI arguments include --bridge or -b. The --bridge and -b flags are stripped
//     before forwarding to real agy.
//  2. Targeted external model: if the CLI specifies a model via --model or -m that matches
//     an enabled model in the external providers registry, bridge is activated.
//     If -m is used, it is normalized to --model because upstream agy only supports --model.
//  3. Otherwise: returns false (100% native mode, preserving native Gemini 3.8 Flash,
//     Antigravity subscription quotas, and all 18 authentic models).
func ShouldActivateBridge(args []string, reg *providers.Registry) (bool, []string) {
	var normalizedArgs []string
	explicitBridge := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--bridge" || arg == "-b" {
			explicitBridge = true
			continue
		}
		if arg == "-m" {
			if i+1 < len(args) {
				normalizedArgs = append(normalizedArgs, "--model", args[i+1])
				i++
				continue
			}
		} else if strings.HasPrefix(arg, "-m=") {
			normalizedArgs = append(normalizedArgs, "--model="+strings.TrimPrefix(arg, "-m="))
			continue
		}
		normalizedArgs = append(normalizedArgs, arg)
	}

	if explicitBridge || os.Getenv("AGY_PRO_BRIDGE") == "1" || os.Getenv("AGY_BRIDGE") == "1" {
		if reg != nil && len(CollectModelIDs(reg)) > 0 {
			return true, normalizedArgs
		}
		return false, normalizedArgs
	}

	if reg == nil || len(reg.Providers) == 0 {
		return false, normalizedArgs
	}

	var requestedModel string
	for i := 0; i < len(normalizedArgs); i++ {
		arg := normalizedArgs[i]
		if arg == "--model" {
			if i+1 < len(normalizedArgs) {
				requestedModel = normalizedArgs[i+1]
				i++
			}
		} else if strings.HasPrefix(arg, "--model=") {
			requestedModel = strings.TrimPrefix(arg, "--model=")
		}
	}

	if requestedModel == "" {
		return false, normalizedArgs
	}

	baseModel, provFilter := providers.ParseModelSpec(requestedModel)
	for _, p := range reg.Providers {
		if provFilter != "" && !strings.EqualFold(p.Name, provFilter) {
			continue
		}
		for _, m := range p.Models {
			if !m.IsEnabled() {
				continue
			}
			displayName := providers.DisplayName(m.ID, p.Name)
			if strings.EqualFold(m.ID, baseModel) || strings.EqualFold(displayName, requestedModel) || strings.EqualFold(m.ID, requestedModel) {
				return true, normalizedArgs
			}
		}
	}

	return false, normalizedArgs
}

// BuildGatewayEnv constructs the AGY_LLM_GATEWAY_* environment variables from the registry,
// enabling the native agy CLI to surface external models in the /model picker.
// sessionKey is the per-session bridge credential; the same value is the
// server-side expectation inside StartBridge, so only this child can call in.
func BuildGatewayEnv(reg *providers.Registry, bridgeURL, sessionKey string) []string {
	if reg == nil || len(reg.Providers) == 0 || bridgeURL == "" || sessionKey == "" {
		return nil
	}

	modelIDs := CollectModelIDs(reg)
	if len(modelIDs) == 0 {
		return nil
	}

	return []string{
		"AGY_LLM_GATEWAY_URL=" + bridgeURL,
		"AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai",
		"AGY_LLM_GATEWAY_API_KEY=" + sessionKey,
		"AGY_LLM_GATEWAY_MODELS=" + strings.Join(modelIDs, ","),
	}
}

// CollectModelIDs extracts all unique enabled model IDs across all providers in the registry.
// It includes the providers.DisplayName alias "model (provider)" and the bare
// "model" ID so both the interactive TUI picker and CLI flag specifications
// (e.g. --model glm-5.3-flash-flex) are recognized by real agy.
func CollectModelIDs(reg *providers.Registry) []string {
	if reg == nil {
		return nil
	}
	var modelIDs []string
	seen := make(map[string]bool)
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if m.IsEnabled() {
				displayName := providers.DisplayName(m.ID, p.Name)
				if !seen[displayName] {
					seen[displayName] = true
					modelIDs = append(modelIDs, displayName)
				}
				if !seen[m.ID] {
					seen[m.ID] = true
					modelIDs = append(modelIDs, m.ID)
				}
			}
		}
	}
	return modelIDs
}

func setEnvKV(env []string, entry string) []string {
	parts := strings.SplitN(entry, "=", 2)
	if len(parts) != 2 {
		return append(env, entry)
	}
	key := parts[0]
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if idx := strings.Index(e, "="); idx >= 0 {
			k := e[:idx]
			if strings.EqualFold(k, key) {
				continue
			}
		}
		out = append(out, e)
	}
	return append(out, entry)
}
