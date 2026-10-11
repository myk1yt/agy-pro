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
	if env := BuildGatewayEnv(nil, "http://127.0.0.1:12345"); env != nil {
		t.Fatalf("expected nil for nil registry, got %v", env)
	}
	if env := BuildGatewayEnv(&providers.Registry{}, "http://127.0.0.1:12345"); env != nil {
		t.Fatalf("expected nil for empty registry, got %v", env)
	}
	if env := BuildGatewayEnv(&providers.Registry{Providers: []providers.Provider{{Models: []providers.Model{{ID: "m1"}}}}}, ""); env != nil {
		t.Fatalf("expected nil for empty bridgeURL, got %v", env)
	}

	// 2. Registry with configured provider and models
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   "https://api.neuralwatt.com/v1",
				APIKeyEnv: "TEST_GATEWAY_KEY",
				Models: []providers.Model{
					{ID: "deepseek-v4.1-flash-flex"},
					{ID: "glm-5.3-flash-flex"},
				},
			},
		},
	}

	bridgeURL := "http://127.0.0.1:54321"
	envVars := BuildGatewayEnv(reg, bridgeURL)
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
	if envMap["AGY_LLM_GATEWAY_API_KEY"] != "agy-pro-local-key" {
		t.Errorf("AGY_LLM_GATEWAY_API_KEY = %q; want 'agy-pro-local-key'", envMap["AGY_LLM_GATEWAY_API_KEY"])
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
		if os.Getenv("AGY_LLM_GATEWAY_API_KEY") != "agy-pro-local-key" {
			fmt.Fprintln(os.Stderr, "invalid AGY_LLM_GATEWAY_API_KEY")
			os.Exit(4)
		}
		models := os.Getenv("AGY_LLM_GATEWAY_MODELS")
		if !strings.Contains(models, "mock-e2e-model (test-prov)") {
			fmt.Fprintf(os.Stderr, "invalid AGY_LLM_GATEWAY_MODELS: %s\n", models)
			os.Exit(5)
		}

		// Verify bridge is alive and returns models
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(gwURL + "/models")
		if err != nil || resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "failed to query bridge /models: %v\n", err)
			os.Exit(6)
		}
		resp.Body.Close()

		os.Exit(0)
	}

	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	provJSON := `{"providers":[{"name":"test-prov","baseURL":"http://127.0.0.1:1","models":[{"id":"mock-e2e-model"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	t.Setenv(paths.EnvAgyProActive, "")
	t.Setenv("AGY_PRO_BRIDGE", "1")
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

