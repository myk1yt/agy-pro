package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	// 7. Context Length: (accept default 1048576)
	// 8. Max Output: (accept default 131072)
	// 9. Supports Tools: Y (tags automatically derived)
	// 10. Allowed tools: (accept default)
	// 11. Supports Thinking: N
	// 12. Supports Vision: N
	// 13. More models: N
	input := strings.Join([]string{
		"mockprov",
		server.URL + "/v1",
		"MOCK_PROV_API_KEY",
		"",
		"mock-model-1",
		"",
		"",
		"",
		"Y",
		"",
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
	if p.Models[0].ContextLength != 1048576 {
		t.Errorf("contextLength = %d; want 1048576", p.Models[0].ContextLength)
	}
	if p.Models[0].MaxOutputTokens != 131072 {
		t.Errorf("maxOutputTokens = %d; want 131072", p.Models[0].MaxOutputTokens)
	}
	if !p.Models[0].HasTag("general") || !p.Models[0].HasTag("tools") {
		t.Errorf("expected tags [general, tools], got %v", p.Models[0].Tags)
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
	if p.Models[0].ContextLength != 1048576 {
		t.Errorf("contextLength = %d; want 1048576", p.Models[0].ContextLength)
	}
	if p.Models[0].MaxOutputTokens != -1 {
		t.Errorf("maxOutputTokens = %d; want -1", p.Models[0].MaxOutputTokens)
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

func TestDeriveTags(t *testing.T) {
	// Base capability: only "general"
	t1 := DeriveTags(Capabilities{})
	if len(t1) != 1 || t1[0] != "general" {
		t.Errorf("expected [general], got %v", t1)
	}

	// Tools only
	t2 := DeriveTags(Capabilities{SupportsTools: true})
	if len(t2) != 2 || t2[0] != "general" || t2[1] != "tools" {
		t.Errorf("expected [general, tools], got %v", t2)
	}

	// Reasoning only
	t3 := DeriveTags(Capabilities{SupportsThinking: true})
	if len(t3) != 2 || t3[0] != "general" || t3[1] != "reasoning" {
		t.Errorf("expected [general, reasoning], got %v", t3)
	}

	// Vision only
	t4 := DeriveTags(Capabilities{SupportsVision: true})
	if len(t4) != 2 || t4[0] != "general" || t4[1] != "vision" {
		t.Errorf("expected [general, vision], got %v", t4)
	}

	// All capabilities
	t5 := DeriveTags(Capabilities{
		SupportsTools:    true,
		SupportsThinking: true,
		SupportsVision:   true,
	})
	expected := []string{"general", "tools", "reasoning", "vision"}
	if len(t5) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, t5)
	}
	for i, exp := range expected {
		if t5[i] != exp {
			t.Errorf("tag[%d] = %q; want %q", i, t5[i], exp)
		}
	}
}

func TestInteractiveSetupWizard_ServerMaximumTokens(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate user entering -1 for context length and -1 for max output tokens
	input := strings.Join([]string{
		"servermaxprov",
		server.URL + "/v1",
		"SERVER_MAX_KEY",
		"",
		"custom-server-max-model",
		"openai", // family
		"-1",     // context length: -1 for server maximum
		"-1",     // max output tokens: -1 for server maximum
		"Y",      // supports tools: Y
		"",       // allowed tools default
		"y",      // reasoning: y
		"y",      // vision: y
		"N",      // add another: N
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("InteractiveSetupWizard returned error: %v\nOutput: %s", err, out.String())
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load saved providers: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(reg.Providers))
	}
	p := reg.Providers[0]
	if len(p.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(p.Models))
	}
	m := p.Models[0]
	if m.ContextLength != -1 {
		t.Errorf("contextLength = %d; want -1", m.ContextLength)
	}
	if m.MaxOutputTokens != -1 {
		t.Errorf("maxOutputTokens = %d; want -1", m.MaxOutputTokens)
	}
	for _, tag := range []string{"general", "tools", "reasoning", "vision"} {
		if !m.HasTag(tag) {
			t.Errorf("missing expected tag %q in %v", tag, m.Tags)
		}
	}
}

func TestIsTimeoutErr(t *testing.T) {
	if isTimeoutErr(nil) {
		t.Error("expected false for nil")
	}
	if !isTimeoutErr(context.DeadlineExceeded) {
		t.Error("expected true for context.DeadlineExceeded")
	}
	if !isTimeoutErr(fmt.Errorf("network error: %w", context.DeadlineExceeded)) {
		t.Error("expected true for wrapped context.DeadlineExceeded")
	}
	if !isTimeoutErr(errors.New("Client.Timeout exceeded while awaiting headers")) {
		t.Error("expected true for timeout string")
	}
	if !isTimeoutErr(errors.New("connection timed out")) {
		t.Error("expected true for timed out string")
	}
	if isTimeoutErr(errors.New("HTTP 401 unauthorized")) {
		t.Error("expected false for 401 error")
	}
}

func TestProbeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	lat, err := ProbeEndpoint(server.URL, "test-key", "test-model")
	if err != nil {
		t.Fatalf("ProbeEndpoint failed: %v", err)
	}
	if lat < 0 {
		t.Errorf("expected positive latency, got %d", lat)
	}

	_, err = ProbeEndpoint(server.URL, "wrong-key", "test-model")
	if err == nil {
		t.Fatal("expected error for wrong key, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %v", err)
	}

	if _, err := ProbeEndpoint("", "k", "m"); err == nil {
		t.Error("expected error for empty baseURL")
	}
	if _, err := ProbeEndpoint("http://a", "k", ""); err == nil {
		t.Error("expected error for empty modelID")
	}
}

func TestInteractiveSetupWizard_ProbeFailure_SaveAnyway(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream gateway error", http.StatusBadGateway)
	}))
	defer server.Close()

	input := strings.Join([]string{
		"failed-save-prov",
		server.URL + "/v1",
		"MOCK_KEY",
		"",
		"custom-model",
		"openai",
		"1048576",
		"131072",
		"n",
		"n",
		"n",
		"1", // choice 1: Save model configuration anyway [Recommended]
		"N", // add another: N
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	outStr := out.String()
	if !strings.Contains(outStr, "Save model configuration anyway [Recommended]") {
		t.Errorf("expected options list in output, got: %s", outStr)
	}
	if !strings.Contains(outStr, `Saved model "custom-model" anyway.`) {
		t.Errorf("expected save confirmation in output, got: %s", outStr)
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	if len(reg.Providers) != 1 || len(reg.Providers[0].Models) != 1 {
		t.Fatalf("expected 1 provider with 1 model, got %+v", reg.Providers)
	}
	if reg.Providers[0].Models[0].ID != "custom-model" {
		t.Errorf("model ID = %q; want custom-model", reg.Providers[0].Models[0].ID)
	}
}

func TestInteractiveSetupWizard_ProbeFailure_RetryImmediately(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	var probeCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[]}`))
			return
		}
		count := atomic.AddInt32(&probeCount, 1)
		if count == 1 {
			http.Error(w, "server cold start", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	input := strings.Join([]string{
		"NW",
		server.URL + "/v1",
		"MOCK_KEY",
		"1", // select preset 1
		"2", // choice 2: Retry test immediately
		"N", // add another: N
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	outStr := out.String()
	if !strings.Contains(outStr, "Retry test immediately") {
		t.Errorf("expected Retry test immediately in output, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Connection succeeded!") {
		t.Errorf("expected Connection succeeded! in output, got: %s", outStr)
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	if len(reg.Providers) != 1 || len(reg.Providers[0].Models) != 1 {
		t.Fatalf("expected 1 provider with 1 model, got %+v", reg.Providers)
	}
}

func TestInteractiveSetupWizard_ProbeFailure_EditAndRetry(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer corrected-key" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	input := strings.Join([]string{
		"NW",
		server.URL + "/v1",
		"wrong-key",
		"1", // select preset 1
		"3", // choice 3: Edit Base URL or API Key
		"",  // Base URL: hit enter to keep current
		"corrected-key", // API Key: updated key
		"N", // add another: N
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	outStr := out.String()
	if !strings.Contains(outStr, "Connection succeeded!") {
		t.Errorf("expected successful retry after key edit in output, got: %s", outStr)
	}
}

func TestInteractiveSetupWizard_ProbeFailure_TimeoutNotice(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[]}`))
			return
		}
		http.Error(w, "gateway timeout while queuing", http.StatusGatewayTimeout)
	}))
	defer server.Close()

	input := strings.Join([]string{
		"NW",
		server.URL + "/v1",
		"mock-key",
		"1", // select preset 1
		"1", // choice 1: save anyway
		"N", // add another: N
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	outStr := out.String()
	expectedNotice := "[Notice] Gateway response timed out. The model may be cold-starting or queueing. You can retry immediately or save anyway."
	if !strings.Contains(outStr, expectedNotice) {
		t.Errorf("expected timeout notice in output, got: %s", outStr)
	}
}

func TestInteractiveSetupWizard_MultipleModels_PreservesCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	var probeHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[]}`))
			return
		}
		probeHeaders = append(probeHeaders, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	input := strings.Join([]string{
		"NW",
		server.URL + "/v1",
		"mock-nw-key-999",
		"1", // model 1: nw-flash
		"y", // add another model: yes
		"2", // model 2: deepseek-v4-flash (NO URL or Key prompted!)
		"N", // add another model: no
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
	if len(p.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(p.Models))
	}
	if p.Models[0].ID != "nw-flash" || p.Models[1].ID != "deepseek-v4-flash" {
		t.Errorf("unexpected models: %+v", p.Models)
	}

	if len(probeHeaders) != 2 {
		t.Fatalf("expected 2 probe requests, got %d", len(probeHeaders))
	}
	for i, h := range probeHeaders {
		if h != "Bearer mock-nw-key-999" {
			t.Errorf("probe request %d had header %q; want Bearer mock-nw-key-999", i, h)
		}
	}
}


