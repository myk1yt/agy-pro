package providers

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	// Valid
	reg := Registry{
		Providers: []Provider{
			{
				Name:      "test-prov",
				BaseURL:   "https://api.example.com/v1",
				APIKeyEnv: "TEST_API_KEY",
				Models: []Model{
					{ID: "m1", Family: "test", ContextLength: 32000, MaxOutputTokens: 2048},
				},
			},
		},
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("unexpected validate error: %v", err)
	}

	// Missing name
	bad := reg
	bad.Providers = []Provider{{BaseURL: "https://foo"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for missing name")
	}

	// Duplicate provider name
	bad = reg
	bad.Providers = append(bad.Providers, reg.Providers[0])
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for duplicate provider name")
	}

	// Bad baseURL
	bad = reg
	bad.Providers = []Provider{{Name: "p1", BaseURL: "ftp://foo"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for non http/https baseURL")
	}

	// Bad env var name
	bad = reg
	bad.Providers = []Provider{{Name: "p1", BaseURL: "https://foo", APIKeyEnv: "123-bad"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for invalid env var name")
	}

	// Duplicate model
	bad = reg
	bad.Providers = []Provider{
		{
			Name:    "p1",
			BaseURL: "https://foo",
			Models: []Model{
				{ID: "m1"},
				{ID: "m1"},
			},
		},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected error for duplicate model id")
	}
}

func TestLoadFrom_NotExist(t *testing.T) {
	_, err := LoadFrom(filepath.Join(t.TempDir(), "nonexistent.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestUnsetEnvVars(t *testing.T) {
	envKey := "TEST_UNSET_VAR_PROVIDERS"
	_ = os.Unsetenv(envKey)

	reg := Registry{
		Providers: []Provider{
			{
				Name:      "p1",
				BaseURL:   "https://p1.com",
				APIKeyEnv: envKey,
				Models:    []Model{{ID: "m1"}},
			},
			{
				Name:    "p2",
				BaseURL: "https://p2.com",
				Models:  []Model{{ID: "m2"}},
			},
		},
	}

	unset := reg.UnsetEnvVars()
	if len(unset) != 1 || unset[0].Provider != "p1" || unset[0].Env != envKey {
		t.Fatalf("unexpected unset vars: %+v", unset)
	}

	names := reg.UnsetEnvNames()
	if len(names) != 1 || names[0] != envKey {
		t.Fatalf("unexpected unset names: %+v", names)
	}

	// Now set the env var
	t.Setenv(envKey, "secret-value")
	unset = reg.UnsetEnvVars()
	if len(unset) != 0 {
		t.Fatalf("expected 0 unset vars after setting env, got %d", len(unset))
	}
}

func TestInferFamily(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"gpt-4o", "gpt"},
		{"claude-3-5-sonnet", "claude"},
		{"deepseek-chat", "deepseek"},
		{"openai/gpt-4o", "gpt"},
		{"anthropic/claude-3.5-sonnet", "claude"},
		{"meta-llama/llama-3.3-70b", "llama"},
		{"qwen2.5-coder", "qwen"},
		{"123model", "123model"},
	}

	for _, c := range cases {
		got := InferFamily(c.input)
		if got != c.expected {
			t.Errorf("InferFamily(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestRedactor(t *testing.T) {
	redact := NewRedactor("super_secret_token_123")
	input := "Bearer super_secret_token_123 in sk-proj-123456789012345678"
	out := redact(input)

	if strings.Contains(out, "super_secret_token_123") {
		t.Errorf("failed to redact secret: %s", out)
	}
	if strings.Contains(out, "sk-proj-123456789012345678") {
		t.Errorf("failed to redact sk- key: %s", out)
	}
}

func TestSelectionAndAlternates(t *testing.T) {
	envKey1 := "TEST_PROV_SELECTION_K1"
	envKey2 := "TEST_PROV_SELECTION_K2"
	t.Setenv(envKey1, "key1")
	t.Setenv(envKey2, "key2")

	reg := &Registry{
		Providers: []Provider{
			{
				Name:      "p1",
				BaseURL:   "https://p1.com",
				APIKeyEnv: envKey1,
				Models: []Model{
					{ID: "shared-model", Family: "shared"},
					{ID: "p1-only", Family: "p1fam"},
				},
			},
			{
				Name:      "p2",
				BaseURL:   "https://p2.com",
				APIKeyEnv: envKey2,
				Models: []Model{
					{ID: "shared-model", Family: "shared"},
					{ID: "p2-only", Family: "p2fam"},
				},
			},
		},
	}

	// AllCandidates
	cands := reg.AllCandidates(true)
	if len(cands) != 4 {
		t.Fatalf("expected 4 candidates, got %d", len(cands))
	}

	// FindTarget
	t1, err := reg.FindTarget("p1/shared-model")
	if err != nil || t1.Provider.Name != "p1" {
		t.Fatalf("FindTarget failed: %v", err)
	}

	// FindAlternatesForModel
	alts := reg.FindAlternatesForModel("shared-model", "p1")
	if len(alts) != 1 || alts[0].Provider.Name != "p2" {
		t.Fatalf("expected alternate in p2, got %+v", alts)
	}

	// FindAlternatesExcludingFamilies
	nonShared := reg.FindAlternatesExcludingFamilies([]string{"shared", "p1fam"})
	if len(nonShared) != 1 || nonShared[0].Model.FamilyName() != "p2fam" {
		t.Fatalf("expected p2fam model, got %+v", nonShared)
	}
}

func TestInteractiveSetupWizard(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate user input
	// 1. Provider name: mockprov
	// 2. Base URL: server.URL + "/v1"
	// 3. API key env: MOCK_PROV_API_KEY
	// 4. API key value: (leave blank)
	// 5. Model ID: mock-model-1
	// 6. Model Family: (accept inferred)
	// 7. Context Length: (accept default)
	// 8. Max Output: (accept default)
	// 9. Tags: (accept default)
	// 10. Supports Tools: Y
	// 11. Allowed tools: (accept default)
	// 12. Supports Vision: N
	// 13. Supports Video: N
	// 14. Supports Thinking: N
	// 15. More models: N
	input := strings.Join([]string{
		"mockprov",
		server.URL + "/v1",
		"MOCK_PROV_API_KEY",
		"",
		"mock-model-1",
		"",
		"",
		"",
		"",
		"Y",
		"",
		"N",
		"N",
		"N",
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("InteractiveSetupWizard returned error: %v\nOutput: %s", err, out.String())
	}

	// Verify saved file
	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load saved providers: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(reg.Providers))
	}
	p := reg.Providers[0]
	if p.Name != "mockprov" || len(p.Models) != 1 || p.Models[0].ID != "mock-model-1" {
		t.Fatalf("unexpected provider saved: %+v", p)
	}
}

func TestGetModelPresets(t *testing.T) {
	cases := []string{"deepseek", "openrouter", "groq", "openai", "nw", "neuralwatt", "together", "mistral"}
	for _, name := range cases {
		presets := GetModelPresets(name)
		if len(presets) == 0 {
			t.Errorf("expected presets for provider %q, got none", name)
		}
		foundRec := false
		for _, p := range presets {
			if p.ID == "" {
				t.Errorf("preset in %q has empty ID", name)
			}
			if p.Recommended {
				foundRec = true
			}
			if p.ContextLength <= 0 {
				t.Errorf("preset %q in %q has invalid ContextLength: %d", p.ID, name, p.ContextLength)
			}
		}
		if !foundRec {
			t.Errorf("expected at least one recommended preset for %q", name)
		}
	}

	// Unknown provider should return nil
	if presets := GetModelPresets("unknown-custom-prov"); presets != nil {
		t.Errorf("expected nil presets for unknown provider, got %+v", presets)
	}
}

func TestIsLikelyAPIKey(t *testing.T) {
	keys := []string{
		"sk-dummy-mock-token-1234567890abcdef",
		"sk-proj-mock-key-1234567890",
		"gsk_mock_secret_key_12345678",
		"nvapi-mock-token-abc-xyz-12345",
		"a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		"this-is-a-long-token-with-hyphens-and-lowercase",
	}
	for _, k := range keys {
		if !isLikelyAPIKey(k) {
			t.Errorf("isLikelyAPIKey(%q) = false; want true", k)
		}
	}

	envVars := []string{
		"OPENAI_API_KEY",
		"DEEPSEEK_API_KEY",
		"NW_API_KEY",
		"MY_CUSTOM_VAR",
	}
	for _, v := range envVars {
		if isLikelyAPIKey(v) {
			t.Errorf("isLikelyAPIKey(%q) = true; want false", v)
		}
	}
}

func TestFetchRemoteModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"remote-model-1"},{"id":"remote-model-2"}]}`))
	}))
	defer server.Close()

	models := FetchRemoteModels(server.URL, "mock-test-key-123")
	if len(models) != 2 || models[0] != "remote-model-1" || models[1] != "remote-model-2" {
		t.Fatalf("unexpected fetched models: %+v", models)
	}

	// Unreachable server should return nil
	empty := FetchRemoteModels("http://127.0.0.1:59999", "key")
	if len(empty) != 0 {
		t.Fatalf("expected empty slice for unreachable server, got %+v", empty)
	}
}

func TestInteractiveSetupWizard_DeepSeekPreset(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate selecting DeepSeek:
	// 1. Provider name: deepseek
	// 2. Base URL: server.URL + "/v1"
	// 3. API Key: mock-test-key-123
	// 4. Select preset 1: deepseek-chat
	// 5. Add another model: N
	input := strings.Join([]string{
		"deepseek",
		server.URL + "/v1",
		"mock-test-key-123",
		"1",
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(reg.Providers))
	}
	p := reg.Providers[0]
	if p.Name != "deepseek" {
		t.Errorf("provider name = %q; want deepseek", p.Name)
	}
	if p.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Errorf("apiKeyEnv = %q; want DEEPSEEK_API_KEY", p.APIKeyEnv)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "deepseek-chat" {
		t.Fatalf("unexpected models: %+v", p.Models)
	}
	if !p.Models[0].Capabilities.SupportsTools {
		t.Errorf("expected deepseek-chat to have tools enabled")
	}
}

func TestInteractiveSetupWizard_NWPreset(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate NW setup:
	// 1. Provider name: NW
	// 2. Base URL: server.URL + "/v1"
	// 3. API Key: mock-dummy-key-456
	// 4. Select preset 1: nw-flash
	// 5. Add another model: N
	input := strings.Join([]string{
		"NW",
		server.URL + "/v1",
		"mock-dummy-key-456",
		"1",
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	p := reg.Providers[0]
	if p.Name != "NW" {
		t.Errorf("provider name = %q; want NW", p.Name)
	}
	if p.APIKeyEnv != "NW_API_KEY" {
		t.Errorf("apiKeyEnv = %q; want NW_API_KEY", p.APIKeyEnv)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "nw-flash" {
		t.Fatalf("unexpected models: %+v", p.Models)
	}
	if p.Models[0].ContextLength != 128000 {
		t.Errorf("contextLength = %d; want 128000", p.Models[0].ContextLength)
	}
}

func TestInteractiveSetupWizard_PastedRawKeyAbsorption(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate pasting a raw API key directly
	rawKey := "sk-mock-raw-key-with-dashes-and-digits-1234567890"
	input := strings.Join([]string{
		"NW",
		server.URL + "/v1",
		rawKey,
		"2", // select deepseek-v4-flash
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error with pasted raw key: %v\nOutput: %s", err, out.String())
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	p := reg.Providers[0]
	// Invariant: Registry MUST NOT contain raw key, must store env var name
	if p.APIKeyEnv != "NW_API_KEY" {
		t.Errorf("apiKeyEnv = %q; want NW_API_KEY", p.APIKeyEnv)
	}
	// Invariant: Process env must have the key set
	if os.Getenv("NW_API_KEY") != rawKey {
		t.Errorf("os.Getenv(NW_API_KEY) was not set to raw key")
	}
	if len(p.Models) != 1 || p.Models[0].ID != "deepseek-v4-flash" {
		t.Fatalf("unexpected models: %+v", p.Models)
	}
}

