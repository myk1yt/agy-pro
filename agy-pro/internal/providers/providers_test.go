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
