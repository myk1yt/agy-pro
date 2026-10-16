package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/bridge"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

// TestM4ChildEnvNamesHelper is a fake "real agy" child that dumps ONLY the NAMES
// of its environment variables (never the values) to AGY_PRO_STUB_ENVFILE, so
// the env-scrub tests can assert on the presence/absence of key NAMES without
// ever persisting or inspecting a key VALUE (M4F-4). A scrub regression shows
// up as a banned NAME in the dump, never as a leaked secret.
func TestM4ChildEnvNamesHelper(t *testing.T) {
	if os.Getenv("GO_M4_ENVNAMES_HELPER") != "1" {
		return
	}
	p := os.Getenv("AGY_PRO_STUB_ENVFILE")
	if p == "" {
		os.Exit(0)
	}
	var names []string
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			names = append(names, kv[:eq])
		}
	}
	_ = os.WriteFile(p, []byte(strings.Join(names, "\n")), 0o600)
	os.Exit(0)
}

// envNameSet reads a names-only dump and returns the case-folded name set.
func envNameSet(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("child env names not recorded: %v", err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if n := strings.ToLower(strings.TrimSpace(line)); n != "" {
			set[n] = true
		}
	}
	return set
}

// m4BridgeHarness drives ExecutePassthrough down the gateway/bridge path with a
// stubbed bridge start (no port) and the names-only helper child, planting the
// given ambient vars, and returns the child's case-folded env-name set.
func m4BridgeHarness(t *testing.T, plants map[string]string) map[string]bool {
	t.Helper()
	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	// The registry declares GO_M4_PLANTED_KEY by name; CHARM_API_KEY and
	// SOMEVENDOR_API_KEY are deliberately UNdeclared (charm is not a compiled
	// preset) so only the shape rule can scrub them (M4F-1).
	provJSON := `{"providers":[{"name":"m4prov","baseURL":"http://127.0.0.1:1","apiKeyEnv":"GO_M4_PLANTED_KEY","models":[{"id":"m4-model"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	t.Setenv(paths.EnvAgyProHome, filepath.Join(tempDir, "home"))
	t.Setenv(paths.EnvAgyProActive, "")

	// Planted FAKE secrets (never real values). Only NAMES are ever inspected.
	for k, v := range plants {
		t.Setenv(k, v)
	}

	t.Setenv("AGY_PRO_BRIDGE", "1")
	t.Setenv("AGY_BRIDGE", "")
	t.Setenv("AGY_LAUNCHER", "0")
	t.Setenv(workspaceEnv, "")

	oldTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = oldTTY })

	oldBridge := startBridgeFunc
	t.Cleanup(func() { startBridgeFunc = oldBridge })
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		return "http://127.0.0.1:9", func() {}, nil
	}

	envFile := filepath.Join(tempDir, "child-env-names.txt")
	t.Setenv("GO_M4_ENVNAMES_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)
	oldSpawn := spawnRealAgy
	spawnRealAgy = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=TestM4ChildEnvNamesHelper")
	}
	t.Cleanup(func() { spawnRealAgy = oldSpawn })

	code, err := ExecutePassthrough("dummy-real-agy", []string{"-p", "hi"})
	if err != nil {
		t.Fatalf("ExecutePassthrough: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d; want 0", code)
	}
	return envNameSet(t, envFile)
}

// M4 item 3 + M4F-1: the gateway/bridge child env scrubs every provider key
// VALUE by name AND by *_API_KEY shape (a declared registry key, the ambient
// GEMINI_API_KEY, an undeclared CHARM_API_KEY that is not a compiled preset, and
// a generic SOMEVENDOR_API_KEY), while the four injected AGY_LLM_GATEWAY_*
// entries and the recursion latch all land. Asserts on NAMES only; never
// inspects or persists a key VALUE.
func TestExecutePassthrough_BridgeChildScrubsProviderKeys(t *testing.T) {
	names := m4BridgeHarness(t, map[string]string{
		"GO_M4_PLANTED_KEY":  "sk-planted-m4-registry-secret",
		"GEMINI_API_KEY":     "AIzaSyPLANTED-AMBIENT-GEMINI",
		"CHARM_API_KEY":      "sk-planted-charm-undeclared-secret",
		"SOMEVENDOR_API_KEY": "sk-planted-somevendor-secret",
	})

	// The bridge path ran and scrub-then-append landed ALL FOUR gateway entries
	// plus the latch (a regression dropping any one fails here).
	for _, want := range []string{
		"agy_llm_gateway_url", "agy_llm_gateway_wire_protocol",
		"agy_llm_gateway_api_key", "agy_llm_gateway_models",
		"agy_pro_active",
	} {
		if !names[want] {
			t.Errorf("bridge child missing %s", want)
		}
	}

	// The asymmetry is closed: no provider key NAME (declared, ambient-Gemini,
	// or undeclared *_API_KEY shape) rides into the child.
	for _, banned := range []string{
		"go_m4_planted_key", "gemini_api_key",
		"charm_api_key", "somevendor_api_key",
	} {
		if names[banned] {
			t.Errorf("bridge child env leaks key name %s (a provider key value rode into the TUI child)", banned)
		}
	}
}

// M4F-1 invariant (b): the pure-native passthrough (no bridge) is UNCHANGED and
// intentionally keeps an ambient *_API_KEY, because native agy may use an
// ambient AI-Studio key and this path is deliberately not scrubbed.
func TestExecutePassthrough_PureNativeKeepsAmbientAPIKey(t *testing.T) {
	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	provJSON := `{"providers":[{"name":"m4prov","baseURL":"http://127.0.0.1:1","apiKeyEnv":"GO_M4_PLANTED_KEY","models":[{"id":"m4-model"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	t.Setenv(paths.EnvAgyProHome, filepath.Join(tempDir, "home"))
	t.Setenv(paths.EnvAgyProActive, "")
	// A unique FAKE ambient *_API_KEY that the pure-native path must keep.
	t.Setenv("PURE_NATIVE_PLANTED_API_KEY", "sk-planted-pure-native-ambient")

	// NO bridge activation: plain invocation, no external --model.
	t.Setenv("AGY_PRO_BRIDGE", "")
	t.Setenv("AGY_BRIDGE", "")
	t.Setenv("AGY_LAUNCHER", "0")
	t.Setenv(workspaceEnv, "")

	oldTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = oldTTY })

	// Detect any accidental bridge activation (it must NOT run on pure native).
	bridgeCalls := 0
	oldBridge := startBridgeFunc
	t.Cleanup(func() { startBridgeFunc = oldBridge })
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		bridgeCalls++
		return "http://127.0.0.1:9", func() {}, nil
	}

	envFile := filepath.Join(tempDir, "child-env-names.txt")
	t.Setenv("GO_M4_ENVNAMES_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)
	oldSpawn := spawnRealAgy
	spawnRealAgy = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=TestM4ChildEnvNamesHelper")
	}
	t.Cleanup(func() { spawnRealAgy = oldSpawn })

	code, err := ExecutePassthrough("dummy-real-agy", []string{"-p", "hi"})
	if err != nil {
		t.Fatalf("ExecutePassthrough: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d; want 0", code)
	}
	if bridgeCalls != 0 {
		t.Fatalf("bridge started %d times on a pure-native invocation", bridgeCalls)
	}

	names := envNameSet(t, envFile)
	// The ambient *_API_KEY survives (pure native keeps ambient credentials),
	// and the recursion latch is set, and NO gateway env is injected.
	if !names["pure_native_planted_api_key"] {
		t.Errorf("pure-native child must keep an ambient *_API_KEY (native AI-Studio key posture)")
	}
	if !names["agy_pro_active"] {
		t.Errorf("pure-native child missing the recursion latch")
	}
	if names["agy_llm_gateway_url"] {
		t.Errorf("pure-native child must not carry gateway env")
	}
}
