package launcher

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agypro/internal/bridge"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

func TestResolveRealAgyWithEnvOverride(t *testing.T) {
	tempDir := t.TempDir()
	fakeRealAgy := filepath.Join(tempDir, "real-agy")
	if runtime.GOOS == "windows" {
		fakeRealAgy += ".exe"
	}
	if err := os.WriteFile(fakeRealAgy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake real agy: %v", err)
	}

	// 1. Env override succeeds
	resolved, err := resolveRealAgy("", fakeRealAgy, "", "")
	if err != nil {
		t.Fatalf("expected resolution via env, got error: %v", err)
	}
	if resolved != fakeRealAgy {
		t.Fatalf("expected %q, got %q", fakeRealAgy, resolved)
	}

	// 2. Env override non-existent fails
	_, err = resolveRealAgy("", filepath.Join(tempDir, "non-existent"), "", "")
	if !errors.Is(err, ErrRealAgyNotFound) {
		t.Fatalf("expected ErrRealAgyNotFound, got: %v", err)
	}
}

func TestResolveRealAgyExcludesSelfDirAndSameFile(t *testing.T) {
	tempDir := t.TempDir()
	selfDir := filepath.Join(tempDir, "shim-bin")
	otherDir := filepath.Join(tempDir, "real-bin")

	if err := os.MkdirAll(selfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}

	binName := "agy"
	if runtime.GOOS == "windows" {
		binName = "agy.exe"
	}

	selfBin := filepath.Join(selfDir, binName)
	realBin := filepath.Join(otherDir, binName)

	if err := os.WriteFile(selfBin, []byte("shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realBin, []byte("real"), 0o755); err != nil {
		t.Fatal(err)
	}

	// PATH has selfDir first, then otherDir
	pathEnv := stringsJoinPaths(selfDir, otherDir)

	resolved, err := resolveRealAgy(selfBin, "", pathEnv, "")
	if err != nil {
		t.Fatalf("resolveRealAgy failed: %v", err)
	}
	if resolved != realBin {
		t.Fatalf("expected to resolve %q (excluding selfDir), but got %q", realBin, resolved)
	}
}

func TestResolveRealAgyFallback(t *testing.T) {
	tempDir := t.TempDir()
	fallbackBin := filepath.Join(tempDir, "fallback-agy")
	if runtime.GOOS == "windows" {
		fallbackBin += ".exe"
	}
	if err := os.WriteFile(fallbackBin, []byte("fallback"), 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveRealAgy("", "", "", fallbackBin)
	if err != nil {
		t.Fatalf("resolveRealAgy fallback failed: %v", err)
	}
	if resolved != fallbackBin {
		t.Fatalf("expected fallback %q, got %q", fallbackBin, resolved)
	}
}

func TestExecutePassthroughRecursionGuard(t *testing.T) {
	t.Setenv(paths.EnvAgyProActive, "1")

	_, err := ExecutePassthrough("dummy", []string{})
	if !errors.Is(err, ErrRecursionDetected) {
		t.Fatalf("expected ErrRecursionDetected, got: %v", err)
	}
}

func stringsJoinPaths(paths ...string) string {
	return filepath.Join(paths[0]) + string(filepath.ListSeparator) + filepath.Join(paths[1])
}

func TestBuildGatewayEnv(t *testing.T) {
	// 1. Nil or empty registry
	if env := BuildGatewayEnv(nil, "http://127.0.0.1:12345", "k"); env != nil {
		t.Fatalf("expected nil for nil registry, got %v", env)
	}
	if env := BuildGatewayEnv(&providers.Registry{}, "http://127.0.0.1:12345", "k"); env != nil {
		t.Fatalf("expected nil for empty registry, got %v", env)
	}
	if env := BuildGatewayEnv(&providers.Registry{Providers: []providers.Provider{{Models: []providers.Model{{ID: "m1"}}}}}, "", "k"); env != nil {
		t.Fatalf("expected nil for empty bridgeURL, got %v", env)
	}
	if env := BuildGatewayEnv(&providers.Registry{Providers: []providers.Provider{{Models: []providers.Model{{ID: "m1"}}}}}, "http://127.0.0.1:12345", ""); env != nil {
		t.Fatalf("expected nil for empty session key, got %v", env)
	}

	// 2. Registry with configured provider and models
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   "https://api.purged.invalid/v1",
				APIKeyEnv: "TEST_GATEWAY_KEY",
				Models: []providers.Model{
					{ID: "deepseek-v4.1-flash-flex"},
					{ID: "glm-5.3-flash-flex"},
				},
			},
		},
	}

	bridgeURL := "http://127.0.0.1:54321"
	sessionKey := "cafebabe-deadbeef-session-key"
	envVars := BuildGatewayEnv(reg, bridgeURL, sessionKey)
	if len(envVars) != 4 {
		t.Fatalf("expected 4 gateway env vars, got %d: %v", len(envVars), envVars)
	}

	envMap := make(map[string]string)
	for _, ev := range envVars {
		parts := strings.SplitN(ev, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	expectedModels := "deepseek-v4.1-flash-flex (NW),deepseek-v4.1-flash-flex,glm-5.3-flash-flex (NW),glm-5.3-flash-flex"
	if envMap["AGY_LLM_GATEWAY_MODELS"] != expectedModels {
		t.Errorf("AGY_LLM_GATEWAY_MODELS = %q; want %q", envMap["AGY_LLM_GATEWAY_MODELS"], expectedModels)
	}
	if envMap["AGY_LLM_GATEWAY_URL"] != "http://127.0.0.1:54321" {
		t.Errorf("AGY_LLM_GATEWAY_URL = %q; want 'http://127.0.0.1:54321'", envMap["AGY_LLM_GATEWAY_URL"])
	}
	if envMap["AGY_LLM_GATEWAY_WIRE_PROTOCOL"] != "openai" {
		t.Errorf("AGY_LLM_GATEWAY_WIRE_PROTOCOL = %q; want 'openai'", envMap["AGY_LLM_GATEWAY_WIRE_PROTOCOL"])
	}
	if envMap["AGY_LLM_GATEWAY_API_KEY"] != sessionKey {
		t.Errorf("AGY_LLM_GATEWAY_API_KEY = %q; want %q", envMap["AGY_LLM_GATEWAY_API_KEY"], sessionKey)
	}
}

// S-3: the session-scoped gateway env is the only thing the launcher injects
// for the real agy child. Provider API keys resolve in-process (the bridge
// calls Provider.KeyValue per request) and must never ride along - the old
// launcher copied every registry key into the child environment, handing them
// to anything the child later spawned.
func TestGatewayEnvNeverCarriesProviderKeys(t *testing.T) {
	t.Setenv("W6_S3_PARENT_KEY", "sk-parent-only-value")
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   "https://api.purged.invalid/v1",
				APIKeyEnv: "W6_S3_PARENT_KEY",
				Models:    []providers.Model{{ID: "m1"}},
			},
			{
				Name:      "Charm",
				BaseURL:   "https://purged.invalid/v1",
				APIKeyEnv: "W6_S3_STORE_ONLY_KEY",
				Models:    []providers.Model{{ID: "m2"}},
			},
		},
	}
	envVars := BuildGatewayEnv(reg, "http://127.0.0.1:9", "session-key")
	if len(envVars) == 0 {
		t.Fatal("expected gateway env entries")
	}
	for _, ev := range envVars {
		name := ev
		if i := strings.IndexByte(ev, '='); i >= 0 {
			name = ev[:i]
		}
		if !strings.HasPrefix(name, "AGY_LLM_GATEWAY_") {
			t.Errorf("non-gateway entry injected into the child env: %q", name)
		}
		if strings.Contains(ev, "W6_S3_PARENT_KEY") || strings.Contains(ev, "sk-parent-only-value") ||
			strings.Contains(ev, "W6_S3_STORE_ONLY_KEY") {
			t.Errorf("provider key material leaked into the gateway env: %q", ev)
		}
	}
}

func TestCollectModelIDs(t *testing.T) {
	trueVal := true
	falseVal := false
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name: "p1",
				Models: []providers.Model{
					{ID: "model-a", Enabled: &trueVal},
					{ID: "model-b", Enabled: &falseVal}, // disabled
				},
			},
			{
				Name: "p2",
				Models: []providers.Model{
					{ID: "model-a", Enabled: &trueVal}, // duplicate ID across different providers
					{ID: "model-c"},
				},
			},
		},
	}

	ids := CollectModelIDs(reg)
	// Should include both display names and bare IDs
	foundDisplayP1 := false
	foundDisplayP2 := false
	foundBareA := false
	foundBareC := false
	for _, id := range ids {
		if id == "model-a (p1)" {
			foundDisplayP1 = true
		}
		if id == "model-a (p2)" {
			foundDisplayP2 = true
		}
		if id == "model-a" {
			foundBareA = true
		}
		if id == "model-c" {
			foundBareC = true
		}
	}
	if !foundDisplayP1 || !foundDisplayP2 || !foundBareA || !foundBareC {
		t.Fatalf("unexpected model IDs: %v", ids)
	}
}

func TestShouldActivateBridge(t *testing.T) {
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name: "NW",
				Models: []providers.Model{
					{ID: "glm-5.3-flash-flex"},
				},
			},
		},
	}

	// 1. Default invocation with no arguments -> native mode (Gemini preserved)
	act, clean := ShouldActivateBridge([]string{}, reg)
	if act {
		t.Errorf("expected activate=false for empty args, got true")
	}
	if len(clean) != 0 {
		t.Errorf("expected empty clean args, got %v", clean)
	}

	// 2. Regular passthrough arguments -> native mode
	act, _ = ShouldActivateBridge([]string{"-p", "hello", "--dangerously-skip-permissions"}, reg)
	if act {
		t.Errorf("expected activate=false for standard flags, got true")
	}

	// 3. Native Gemini model explicitly requested -> native mode
	act, _ = ShouldActivateBridge([]string{"--model", "gemini-3.8-flash-high", "-p", "hello"}, reg)
	if act {
		t.Errorf("expected activate=false for native gemini model, got true")
	}

	// 4. External model requested via --model -> bridge activated
	act, clean = ShouldActivateBridge([]string{"--model", "glm-5.3-flash-flex", "-p", "hello"}, reg)
	if !act {
		t.Errorf("expected activate=true for external model, got false")
	}
	if len(clean) != 4 || clean[0] != "--model" || clean[1] != "glm-5.3-flash-flex" {
		t.Errorf("unexpected clean args: %v", clean)
	}

	// 5. External model requested via display format "model (provider)" -> bridge activated
	act, _ = ShouldActivateBridge([]string{"--model", "glm-5.3-flash-flex (NW)"}, reg)
	if !act {
		t.Errorf("expected activate=true for display format model, got false")
	}

	// 6. External model requested via -m short flag -> normalized to --model and bridge activated
	act, clean = ShouldActivateBridge([]string{"-m", "glm-5.3-flash-flex", "-p", "test"}, reg)
	if !act {
		t.Errorf("expected activate=true for -m external model, got false")
	}
	if len(clean) != 4 || clean[0] != "--model" || clean[1] != "glm-5.3-flash-flex" {
		t.Errorf("expected clean args with normalized --model, got %v", clean)
	}

	// 7. Explicit --bridge flag -> stripped and bridge activated
	act, clean = ShouldActivateBridge([]string{"--bridge", "models"}, reg)
	if !act {
		t.Errorf("expected activate=true for --bridge flag, got false")
	}
	if len(clean) != 1 || clean[0] != "models" {
		t.Errorf("expected clean args ['models'], got %v", clean)
	}

	// 8. Explicit -b short flag -> stripped and bridge activated
	act, clean = ShouldActivateBridge([]string{"-b"}, reg)
	if !act {
		t.Errorf("expected activate=true for -b flag, got false")
	}
	if len(clean) != 0 {
		t.Errorf("expected empty clean args, got %v", clean)
	}

	// 9. Environment variable AGY_PRO_BRIDGE=1 -> bridge activated
	t.Setenv("AGY_PRO_BRIDGE", "1")
	act, _ = ShouldActivateBridge([]string{}, reg)
	if !act {
		t.Errorf("expected activate=true when AGY_PRO_BRIDGE=1, got false")
	}
}

func TestSetEnvKV(t *testing.T) {
	initial := []string{"FOO=bar", "BAZ=qux"}
	// Update FOO
	updated := setEnvKV(initial, "FOO=updated")
	if len(updated) != 2 {
		t.Fatalf("expected len 2, got %d", len(updated))
	}
	if updated[0] != "BAZ=qux" || updated[1] != "FOO=updated" {
		t.Errorf("unexpected updated env: %v", updated)
	}

	// Add new KEY
	added := setEnvKV(updated, "NEW=val")
	if len(added) != 3 {
		t.Fatalf("expected len 3, got %d", len(added))
	}
}

func TestExecutePassthrough_StartsBridgeAndInjectsEnv(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		gwURL := os.Getenv("AGY_LLM_GATEWAY_URL")
		if gwURL == "" {
			fmt.Fprintln(os.Stderr, "missing AGY_LLM_GATEWAY_URL")
			os.Exit(2)
		}
		if os.Getenv("AGY_LLM_GATEWAY_WIRE_PROTOCOL") != "openai" {
			fmt.Fprintln(os.Stderr, "invalid AGY_LLM_GATEWAY_WIRE_PROTOCOL")
			os.Exit(3)
		}
		apiKey := os.Getenv("AGY_LLM_GATEWAY_API_KEY")
		if len(apiKey) != 64 {
			fmt.Fprintf(os.Stderr, "invalid AGY_LLM_GATEWAY_API_KEY length: %d\n", len(apiKey))
			os.Exit(4)
		}
		for _, c := range apiKey {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				fmt.Fprintf(os.Stderr, "AGY_LLM_GATEWAY_API_KEY is not lowercase hex\n")
				os.Exit(4)
			}
		}
		models := os.Getenv("AGY_LLM_GATEWAY_MODELS")
		if !strings.Contains(models, "mock-e2e-model (test-prov)") {
			fmt.Fprintf(os.Stderr, "invalid AGY_LLM_GATEWAY_MODELS: %s\n", models)
			os.Exit(5)
		}

		// R1-3/S-3: the shim pins AGY_PRO_WORKSPACE on its own process for the
		// bridge tool loop; the child agy must never receive it.
		if ws := os.Getenv(workspaceEnv); ws != "" {
			fmt.Fprintf(os.Stderr, "child agy received the shim workspace root: %q\n", ws)
			os.Exit(12)
		}

		client := &http.Client{Timeout: 2 * time.Second}

		// Unauthenticated /models must be rejected (bearer enforcement).
		resp, err := client.Get(gwURL + "/models")
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to query bridge /models: %v\n", err)
			os.Exit(6)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			fmt.Fprintf(os.Stderr, "expected 401 for unauthenticated /models, got %d\n", resp.StatusCode)
			os.Exit(7)
		}

		// Authenticated with the injected session key must succeed.
		req, err := http.NewRequest(http.MethodGet, gwURL+"/models", nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(8)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp2, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to query bridge /models with session key: %v\n", err)
			os.Exit(9)
		}
		if resp2.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "expected 200 with session key, got %d\n", resp2.StatusCode)
			os.Exit(9)
		}
		resp2.Body.Close()

		// Wrong key must be rejected too.
		req3, _ := http.NewRequest(http.MethodGet, gwURL+"/models", nil)
		req3.Header.Set("Authorization", "Bearer "+"0000000000000000000000000000000000000000000000000000000000000000")
		resp3, err := client.Do(req3)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(10)
		}
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusUnauthorized {
			fmt.Fprintf(os.Stderr, "expected 401 for wrong session key, got %d\n", resp3.StatusCode)
			os.Exit(11)
		}

		os.Exit(0)
	}

	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	provJSON := `{"providers":[{"name":"test-prov","baseURL":"http://127.0.0.1:1","models":[{"id":"mock-e2e-model"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	t.Setenv(paths.EnvAgyProHome, filepath.Join(tempDir, "home"))
	t.Setenv(workspaceEnv, "")
	t.Setenv(paths.EnvAgyProActive, "")
	t.Setenv("AGY_PRO_BRIDGE", "1")
	t.Setenv("AGY_LAUNCHER", "0")
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	code, err := ExecutePassthrough(selfExe, []string{"-test.run=TestExecutePassthrough_StartsBridgeAndInjectsEnv"})
	if err != nil {
		t.Fatalf("ExecutePassthrough failed: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

// F-11: a failing bridge start must warn on stderr and record the fallback on
// the route log instead of silently booting native.
func TestStartBridgeOrFallbackWarnsAndLogs(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv(paths.EnvAgyProHome, home)

	old := startBridgeFunc
	t.Cleanup(func() { startBridgeFunc = old })
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		return "", nil, errors.New("bind refused")
	}

	var errw strings.Builder
	gatewayEnv, stop := startBridgeOrFallback(testRegistryWithModels(), "session-key", bridge.BridgeToolsConfig{}, &errw)
	if gatewayEnv != nil || stop != nil {
		t.Fatalf("failed bridge must not inject gateway env or a stop func: env=%v", gatewayEnv)
	}
	if !strings.Contains(errw.String(), "bridge failed: bind refused - falling back to native") {
		t.Errorf("stderr warning missing: %q", errw.String())
	}

	logPath := filepath.Join(home, "route.log")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("route log not written: %v", err)
	}
	if !strings.Contains(string(raw), "bridge-fallback: bind refused") {
		t.Errorf("route log missing fallback entry:\n%s", raw)
	}

	// Success path: env is injected, no warning, no fallback entry.
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		return "http://127.0.0.1:9", func() {}, nil
	}
	var errw2 strings.Builder
	gatewayEnv, stop = startBridgeOrFallback(testRegistryWithModels(), "session-key", bridge.BridgeToolsConfig{}, &errw2)
	if stop == nil {
		t.Fatal("successful bridge start must return a stop func")
	}
	if len(gatewayEnv) != 4 || !strings.HasPrefix(gatewayEnv[0], "AGY_LLM_GATEWAY_URL=http://127.0.0.1:9") {
		t.Errorf("gateway env = %v", gatewayEnv)
	}
	if errw2.Len() != 0 {
		t.Errorf("unexpected warning on success: %q", errw2.String())
	}
	raw, _ = os.ReadFile(logPath)
	if strings.Count(string(raw), "bridge-fallback") != 1 {
		t.Errorf("success must not add a fallback entry:\n%s", raw)
	}
}

// F-11: the no-stop-func case is a bridge failure too, never a silent boot.
func TestStartBridgeOrFallbackTreatsNilStopAsFailure(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	old := startBridgeFunc
	t.Cleanup(func() { startBridgeFunc = old })
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		return "http://127.0.0.1:9", nil, nil
	}
	var errw strings.Builder
	gatewayEnv, stop := startBridgeOrFallback(testRegistryWithModels(), "session-key", bridge.BridgeToolsConfig{}, &errw)
	if gatewayEnv != nil || stop != nil {
		t.Fatalf("nil stop func must not inject gateway env: %v", gatewayEnv)
	}
	if !strings.Contains(errw.String(), "bridge failed: bridge returned no stop function - falling back to native") {
		t.Errorf("stderr warning missing: %q", errw.String())
	}
}

// R1-3: the shim pins AGY_PRO_WORKSPACE on its OWN process env right before the
// bridge starts, so the tool loop resolves paths against the shim cwd. The
// value never rides into the child env (BuildGatewayEnv stays 4 entries).
func TestEnsureBridgeWorkspaceSetsAndPreserves(t *testing.T) {
	// 1. Unset: the shim cwd is captured.
	t.Setenv(workspaceEnv, "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ensureBridgeWorkspace()
	if got := os.Getenv(workspaceEnv); got != wd {
		t.Fatalf("%s = %q; want shim cwd %q", workspaceEnv, got, wd)
	}

	// 2. Already set: an existing value is authoritative (env over cwd).
	t.Setenv(workspaceEnv, filepath.Join(t.TempDir(), "orchestrator-workspace"))
	want := os.Getenv(workspaceEnv)
	ensureBridgeWorkspace()
	if got := os.Getenv(workspaceEnv); got != want {
		t.Fatalf("%s was overwritten: got %q, want %q", workspaceEnv, got, want)
	}

	// 3. The gateway env handed to the child stays exactly the 4 documented
	// entries: no workspace, no tools.
	envVars := BuildGatewayEnv(testRegistryWithModels(), "http://127.0.0.1:9", "session-key")
	if len(envVars) != 4 {
		t.Fatalf("expected 4 gateway env vars, got %d: %v", len(envVars), envVars)
	}
	for _, ev := range envVars {
		if strings.HasPrefix(ev, workspaceEnv+"=") || strings.HasPrefix(ev, bridgeToolsEnv+"=") {
			t.Errorf("loop-only env leaked into the child env: %q", ev)
		}
	}
}

// startBridgeOrFallback is the only path to StartBridge, so it must pin the
// workspace before the bridge can serve a request, and it must hand the
// resolved allow-list to the bridge unchanged.
func TestStartBridgeOrFallbackPinsWorkspaceAndPassesTools(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	t.Setenv(workspaceEnv, "")

	old := startBridgeFunc
	t.Cleanup(func() { startBridgeFunc = old })

	var gotTools bridge.BridgeToolsConfig
	var workspaceAtStart string
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		gotTools = toolsCfg
		workspaceAtStart = os.Getenv(workspaceEnv)
		return "http://127.0.0.1:9", func() {}, nil
	}

	want := bridge.BridgeToolsConfig{Enabled: true, Tools: []string{"read", "search", "write"}}
	var errw strings.Builder
	gatewayEnv, stop := startBridgeOrFallback(testRegistryWithModels(), "session-key", want, &errw)
	if stop == nil || len(gatewayEnv) != 4 {
		t.Fatalf("bridge success must inject the 4 gateway entries: env=%v stop==nil=%v", gatewayEnv, stop == nil)
	}
	wd, _ := os.Getwd()
	if workspaceAtStart != wd {
		t.Errorf("%s at StartBridge = %q; want shim cwd %q", workspaceEnv, workspaceAtStart, wd)
	}
	if gotTools.Enabled != want.Enabled || strings.Join(gotTools.Tools, ",") != strings.Join(want.Tools, ",") {
		t.Errorf("tools config passed to StartBridge = %+v; want %+v", gotTools, want)
	}
}

// R1-6 precedence: AGY_PRO_BRIDGE_TOOLS env > stored bridge_tools > default
// read,search. Invalid or "none" values disable the loop instead of widening it.
func TestResolveBridgeToolsPrecedence(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv(paths.EnvAgyProHome, home)
	t.Setenv(bridgeToolsEnv, "")

	// 1. Default when neither env nor state carries a value.
	got := ResolveBridgeTools()
	if !got.Enabled || strings.Join(got.Tools, ",") != mode.DefaultBridgeTools {
		t.Fatalf("default resolve = %+v; want read,search enabled", got)
	}

	// 2. Stored config wins over the default.
	st, err := mode.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	st.BridgeTools = "read,exec"
	if err := mode.SaveState(st); err != nil {
		t.Fatal(err)
	}
	got = ResolveBridgeTools()
	if !got.Enabled || strings.Join(got.Tools, ",") != "read,exec" {
		t.Fatalf("stored-config resolve = %+v; want read,exec", got)
	}

	// 3. Env wins over stored config.
	t.Setenv(bridgeToolsEnv, "write,read")
	got = ResolveBridgeTools()
	if !got.Enabled || strings.Join(got.Tools, ",") != "read,write" {
		t.Fatalf("env resolve = %+v; want canonical read,write", got)
	}

	// 4. "none" disables the loop.
	t.Setenv(bridgeToolsEnv, "none")
	if got = ResolveBridgeTools(); got.Enabled || len(got.Tools) != 0 {
		t.Fatalf("none resolve = %+v; want loop disabled", got)
	}

	// 5. Unknown tokens fail closed (loop off), never widen the allow-list.
	t.Setenv(bridgeToolsEnv, "read,bogus")
	if got = ResolveBridgeTools(); got.Enabled || len(got.Tools) != 0 {
		t.Fatalf("invalid resolve = %+v; want loop disabled", got)
	}
}
