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

// S-6: bearer keys transit the baseURL, so http:// is only acceptable for
// loopback endpoints; everything else must be https://.
func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		url  string
		ok   bool
		desc string
	}{
		{"https://api.openai.com/v1", true, "https anywhere"},
		{"https://10.0.0.5:8443/v1", true, "https on a private host"},
		{"http://127.0.0.1:8080/v1", true, "http loopback ipv4"},
		{"http://localhost:11434/v1", true, "http localhost"},
		{"http://[::1]:8080/v1", true, "http loopback ipv6"},
		{"http://127.0.0.53/v1", true, "http loopback range"},
		{"http://example.com/v1", false, "http cleartext to a remote host"},
		{"http://10.0.0.5:8000/v1", false, "http cleartext on a private host"},
		{"ftp://foo", false, "unsupported scheme"},
		{"ws://127.0.0.1/v1", false, "unsupported scheme even on loopback"},
		{"", false, "empty"},
	}
	for _, tc := range cases {
		err := validateBaseURL(tc.url)
		if tc.ok && err != nil {
			t.Errorf("validateBaseURL(%q) [%s] = %v; want nil", tc.url, tc.desc, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("validateBaseURL(%q) [%s] = nil; want error", tc.url, tc.desc)
		}
	}

	reg := Registry{Providers: []Provider{{Name: "p1", BaseURL: "http://collect.attacker.tld/v1", Models: []Model{{ID: "m1"}}}}}
	if err := reg.Validate(); err == nil {
		t.Fatal("Validate must reject a cleartext non-loopback baseURL")
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
	cases := []string{"deepseek", "openrouter", "groq", "openai", "bigmodel", "glm", "zhipu", "nw", "neuralwatt", "together", "mistral", "google", "gemini", "google ai studio"}
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

// I12: an unregistered provider gets no invented Base URL default - the wizard
// prints an example format and requires explicit input, then saves exactly
// what the user typed.
func TestInteractiveSetupWizard_UnknownProviderNoDefaultURL(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate an unknown provider: the Base URL must be typed by hand, then
	// the custom-model prompts accept defaults (empty lines), then N to stop.
	input := strings.Join([]string{
		"mycustomvendor",
		server.URL + "/v1",
		"mock-dummy-key-789",
		"custom-model-x",
		"", "", "", "", "", "", "", "",
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	if !strings.Contains(out.String(), `No built-in default Base URL for "mycustomvendor"`) {
		t.Errorf("missing no-default notice:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Example: https://api.openai.com/v1") {
		t.Errorf("missing example format hint:\n%s", out.String())
	}
	if strings.Contains(out.String(), "OpenAI-compatible Base URL [https://api.openai.com/v1]:") {
		t.Errorf("unknown provider must not get an OpenAI default prompt:\n%s", out.String())
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(reg.Providers))
	}
	p := reg.Providers[0]
	if p.BaseURL != server.URL+"/v1" {
		t.Errorf("baseURL = %q; want the explicitly typed %q", p.BaseURL, server.URL+"/v1")
	}
	if p.APIKeyEnv != "MYCUSTOMVENDOR_API_KEY" {
		t.Errorf("apiKeyEnv = %q; want MYCUSTOMVENDOR_API_KEY", p.APIKeyEnv)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "custom-model-x" {
		t.Fatalf("unexpected models: %+v", p.Models)
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

// TestInteractiveSetupWizard_FamilyDisplayMasksKeyShapedFamily is the C2
// display fix at the wizard summary: the family column routes through
// MaskFamilyName, so a key-shaped explicit family renders first-5+****
// (the folded FamilyName() rode it raw) while a benign family stays
// readable. The registry itself keeps the typed value: the fix is
// display-only.
func TestInteractiveSetupWizard_FamilyDisplayMasksKeyShapedFamily(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)
	t.Setenv("MYCUSTOMVENDOR_API_KEY", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	keyFamily := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// Unknown provider (no presets): name, URL, key, then two custom models.
	// The first declares a key-shaped family, the second a benign one.
	input := strings.Join([]string{
		"mycustomvendor",
		server.URL + "/v1",
		"mock-dummy-key-789",
		"custom-model-x",
		keyFamily,
		"", "", "N", "N", "N", // ctx, maxout, tools, thinking, vision
		"y",
		"custom-model-2",
		"gemini",
		"", "", "N", "N", "N",
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}
	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	if !strings.Contains(out.String(), "[family: wJalr****") {
		t.Errorf("key-shaped family must render first-5+**** in the summary:\n%s", out.String())
	}
	if strings.Contains(out.String(), "wJalrXUtnFEMI") {
		t.Errorf("summary leaks the key-shaped family raw:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "[family: gemini") {
		t.Errorf("benign family must stay readable in the summary:\n%s", out.String())
	}

	// The registry keeps the typed value verbatim (display-only fix).
	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load saved providers: %v", err)
	}
	if len(reg.Providers) != 1 || len(reg.Providers[0].Models) != 2 {
		t.Fatalf("unexpected saved registry: %+v", reg.Providers)
	}
	if reg.Providers[0].Models[0].Family != keyFamily {
		t.Errorf("registry family = %q; want the typed value %q (display-only fix)", reg.Providers[0].Models[0].Family, keyFamily)
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
		"openai",  // family
		"-1",      // context length: -1 for server maximum
		"-1",      // max output tokens: -1 for server maximum
		"Y",       // supports tools: Y
		"",        // allowed tools default
		"y",       // reasoning: y
		"default", // reasoning effort
		"y",       // vision: y
		"N",       // add another: N
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
		"1",             // select preset 1
		"3",             // choice 3: Edit Base URL or API Key
		"",              // Base URL: hit enter to keep current
		"corrected-key", // API Key: updated key
		"N",             // add another: N
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

func TestInteractiveSetupWizard_ReasoningEffortDefault(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate user pressing Enter on reasoning prompt (defaults to Y) and Enter on effort (defaults to default)
	input := strings.Join([]string{
		"thinkprov",
		server.URL + "/v1",
		"THINK_KEY",
		"",
		"custom-think-model",
		"thinkfam",
		"128000",
		"4096",
		"Y", // tools: Y
		"",  // allowed tools default
		"",  // reasoning: Enter -> defaults to Y!
		"",  // reasoning effort: Enter -> defaults to "default"!
		"N", // vision: N
		"N", // add another: N
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
	m := reg.Providers[0].Models[0]
	if !m.Capabilities.SupportsThinking {
		t.Errorf("expected SupportsThinking to be true by default")
	}
	if m.Capabilities.ReasoningEffort != "default" {
		t.Errorf("reasoningEffort = %q; want 'default'", m.Capabilities.ReasoningEffort)
	}
	if !m.HasTag("reasoning") {
		t.Errorf("expected model to have tag 'reasoning', got %v", m.Tags)
	}
}

func TestInteractiveSetupWizard_ReasoningEffortExplicitHigh(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Explicitly choose "high" for reasoning effort
	input := strings.Join([]string{
		"highprov",
		server.URL + "/v1",
		"HIGH_KEY",
		"",
		"custom-high-model",
		"highfam",
		"128000",
		"4096",
		"Y",    // tools: Y
		"",     // allowed tools default
		"y",    // reasoning: y
		"high", // reasoning effort: high
		"N",    // vision: N
		"N",    // add another: N
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
	m := reg.Providers[0].Models[0]
	if !m.Capabilities.SupportsThinking {
		t.Errorf("expected SupportsThinking to be true")
	}
	if m.Capabilities.ReasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q; want 'high'", m.Capabilities.ReasoningEffort)
	}
}

func TestKnownProviderCatalog(t *testing.T) {
	hints := KnownProviders()
	if len(hints) == 0 {
		t.Fatal("known provider catalog is empty")
	}
	for _, h := range hints {
		if h.Name == "" || h.Label == "" {
			t.Errorf("catalog entry %+v is missing name or label", h)
		}
		if strings.Contains(h.Name, "/") {
			t.Errorf("provider name %q must not contain '/'", h.Name)
		}
		if !strings.HasPrefix(h.BaseURL, "https://") {
			t.Errorf("provider %q baseURL %q must be https", h.Name, h.BaseURL)
		}
		if !envNameRe.MatchString(h.KeyEnv) {
			t.Errorf("provider %q keyEnv %q is not a valid environment variable name", h.Name, h.KeyEnv)
		}
	}

	google, ok := LookupProviderHint("google")
	if !ok {
		t.Fatal("expected a catalog entry for google")
	}
	if google.BaseURL != GoogleAIStudioBaseURL {
		t.Errorf("google baseURL = %q; want %q", google.BaseURL, GoogleAIStudioBaseURL)
	}
	if google.KeyEnv != GoogleAIStudioKeyEnv {
		t.Errorf("google keyEnv = %q; want %q", google.KeyEnv, GoogleAIStudioKeyEnv)
	}
	if GoogleAIStudioKeyEnv != "GEMINI_API_KEY" {
		t.Errorf("GoogleAIStudioKeyEnv = %q; want GEMINI_API_KEY", GoogleAIStudioKeyEnv)
	}

	// Aliases resolve to the canonical name.
	for _, alias := range []string{"gemini", "Gemini", "google ai studio", "google-ai-studio", "AIStudio"} {
		hint, ok := LookupProviderHint(alias)
		if !ok || hint.Name != "google" {
			t.Errorf("LookupProviderHint(%q) = %+v, %v; want canonical google", alias, hint, ok)
		}
	}
	// bigmodel (Zhipu GLM): catalog entry, key env, and aliases.
	bigmodel, ok := LookupProviderHint("bigmodel")
	if !ok {
		t.Fatal("expected a catalog entry for bigmodel")
	}
	if bigmodel.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Errorf("bigmodel baseURL = %q", bigmodel.BaseURL)
	}
	if bigmodel.KeyEnv != "BIGMODEL_API_KEY" {
		t.Errorf("bigmodel keyEnv = %q; want BIGMODEL_API_KEY", bigmodel.KeyEnv)
	}
	for _, alias := range []string{"glm", "GLM", "zhipu", "Zhipu"} {
		hint, ok := LookupProviderHint(alias)
		if !ok || hint.Name != "bigmodel" {
			t.Errorf("LookupProviderHint(%q) = %+v, %v; want canonical bigmodel", alias, hint, ok)
		}
	}
	if got := CanonicalProviderName("NeuralWatt"); got != "nw" {
		t.Errorf("CanonicalProviderName(NeuralWatt) = %q; want nw", got)
	}
	if got := CanonicalProviderName("MyCustomVendor"); got != "mycustomvendor" {
		t.Errorf("CanonicalProviderName(MyCustomVendor) = %q; want mycustomvendor", got)
	}
	if _, ok := LookupProviderHint("mycustomvendor"); ok {
		t.Error("unknown provider must not resolve to a catalog entry")
	}
}

func TestDefaultBaseURLAndEnvFor(t *testing.T) {
	baseCases := map[string]string{
		"google":           GoogleAIStudioBaseURL,
		"gemini":           GoogleAIStudioBaseURL,
		"Google AI Studio": GoogleAIStudioBaseURL,
		"openai":           "https://api.openai.com/v1",
		"deepseek":         "https://api.deepseek.com/v1",
		"openrouter":       "https://openrouter.ai/api/v1",
		"groq":             "https://api.groq.com/openai/v1",
		"bigmodel":         "https://open.bigmodel.cn/api/paas/v4",
		"glm":              "https://open.bigmodel.cn/api/paas/v4",
		"NW":               "https://api.neuralwatt.com/v1",
		"neuralwatt":       "https://api.neuralwatt.com/v1",
		"together":         "https://api.together.xyz/v1",
		"mistral":          "https://api.mistral.ai/v1",
		// Unregistered providers get NO invented default (the wizard shows an
		// example and requires explicit input instead of silently targeting
		// api.openai.com).
		"unknown-custom":    "",
		"my local gateway ": "",
	}
	for name, want := range baseCases {
		if got := defaultBaseURLFor(name); got != want {
			t.Errorf("defaultBaseURLFor(%q) = %q; want %q", name, got, want)
		}
	}

	envCases := map[string]string{
		"google":           "GEMINI_API_KEY",
		"gemini":           "GEMINI_API_KEY",
		"google ai studio": "GEMINI_API_KEY",
		"openai":           "OPENAI_API_KEY",
		"nw":               "NW_API_KEY",
		"neuralwatt":       "NW_API_KEY",
		"my-prov":          "MY_PROV_API_KEY",
		"My Provider":      "MY_PROVIDER_API_KEY",
	}
	for name, want := range envCases {
		got := defaultEnvFor(name)
		if got != want {
			t.Errorf("defaultEnvFor(%q) = %q; want %q", name, got, want)
		}
		if !envNameRe.MatchString(got) {
			t.Errorf("defaultEnvFor(%q) = %q is not a valid environment variable name", name, got)
		}
	}
}

func TestGoogleAIStudioPresets(t *testing.T) {
	presets := GetModelPresets("google")
	if len(presets) != 2 {
		t.Fatalf("expected 2 Google presets, got %d", len(presets))
	}

	// "gemini" is an alias for the same preset set.
	aliasPresets := GetModelPresets("gemini")
	if len(aliasPresets) != len(presets) {
		t.Fatalf("alias preset count = %d; want %d", len(aliasPresets), len(presets))
	}

	byID := map[string]ModelPreset{}
	for _, p := range presets {
		byID[p.ID] = p
		if !p.Recommended && p.ID == "gemini-2.5-flash" {
			t.Error("gemini-2.5-flash should be the recommended preset")
		}
		if !p.Capabilities.SupportsVision {
			t.Errorf("preset %q must declare SupportsVision", p.ID)
		}
		if !p.Capabilities.SupportsThinking {
			t.Errorf("preset %q must declare SupportsThinking", p.ID)
		}
		if !p.Capabilities.SupportsTools {
			t.Errorf("preset %q must declare SupportsTools", p.ID)
		}
		// Effort is intentionally unset: the gateway then omits reasoning_effort
		// and Google applies its own thinking budget.
		if p.Capabilities.ReasoningEffort != "" {
			t.Errorf("preset %q ReasoningEffort = %q; want empty (provider default)", p.ID, p.Capabilities.ReasoningEffort)
		}
		if !presetHasTag(p, "vision") || !presetHasTag(p, "reasoning") {
			t.Errorf("preset %q tags = %v; want vision and reasoning", p.ID, p.Tags)
		}
	}

	flash, ok := byID["gemini-2.5-flash"]
	if !ok {
		t.Fatal("missing gemini-2.5-flash preset")
	}
	if flash.Family != "gemini" || flash.ContextLength != 1048576 || flash.MaxOutputTokens != -1 {
		t.Errorf("unexpected gemini-2.5-flash preset: %+v", flash)
	}
	pro, ok := byID["gemini-2.5-pro"]
	if !ok {
		t.Fatal("missing gemini-2.5-pro preset")
	}
	if pro.Family != "gemini" || pro.ContextLength != 1048576 {
		t.Errorf("unexpected gemini-2.5-pro preset: %+v", pro)
	}

	// The AI Studio endpoint must resolve to the OpenAI-compatible paths the
	// bridge and the wizard probe append.
	prov := Provider{Name: "google", BaseURL: GoogleAIStudioBaseURL, APIKeyEnv: GoogleAIStudioKeyEnv}
	if got, want := prov.Endpoint(), "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"; got != want {
		t.Errorf("AI Studio endpoint = %q; want %q", got, want)
	}
}

// I12: bigmodel (Zhipu GLM) presets. Specs come from docs.bigmodel.cn
// (model overview): GLM-4.6 = 200K context, coding/reasoning/tools;
// GLM-4.5-Flash = free tier, 128K context, thinking + tools. Neither is a
// vision model, and GLM thinking has no effort dial (ReasoningEffort unset).
func TestBigModelPresets(t *testing.T) {
	presets := GetModelPresets("bigmodel")
	if len(presets) != 2 {
		t.Fatalf("expected 2 bigmodel presets, got %d", len(presets))
	}

	// "glm" / "zhipu" resolve to the same preset set.
	if alias := GetModelPresets("glm"); len(alias) != len(presets) {
		t.Fatalf("alias preset count = %d; want %d", len(alias), len(presets))
	}

	byID := map[string]ModelPreset{}
	for _, p := range presets {
		byID[p.ID] = p
		if !p.Capabilities.SupportsTools || !p.Capabilities.SupportsThinking {
			t.Errorf("preset %q must declare tools and thinking, got %+v", p.ID, p.Capabilities)
		}
		if p.Capabilities.SupportsVision {
			t.Errorf("preset %q must not claim vision (GLM text models)", p.ID)
		}
		if p.Capabilities.ReasoningEffort != "" {
			t.Errorf("preset %q ReasoningEffort = %q; want empty (GLM thinking is on/off, no dial)", p.ID, p.Capabilities.ReasoningEffort)
		}
		if !presetHasTag(p, "tools") {
			t.Errorf("preset %q tags = %v; want the tools tag", p.ID, p.Tags)
		}
	}

	glm46, ok := byID["glm-4.6"]
	if !ok {
		t.Fatal("missing glm-4.6 preset")
	}
	if !glm46.Recommended {
		t.Error("glm-4.6 should be the recommended bigmodel preset")
	}
	if glm46.Family != "glm" || glm46.ContextLength != 200000 || glm46.MaxOutputTokens != -1 {
		t.Errorf("unexpected glm-4.6 preset: %+v", glm46)
	}

	flash, ok := byID["glm-4.5-flash"]
	if !ok {
		t.Fatal("missing glm-4.5-flash preset")
	}
	if flash.Recommended {
		t.Error("glm-4.5-flash is the free tier; glm-4.6 should carry the recommendation")
	}
	if flash.Family != "glm" || flash.ContextLength != 128000 {
		t.Errorf("unexpected glm-4.5-flash preset: %+v", flash)
	}
	if !presetHasTag(flash, "free") {
		t.Errorf("glm-4.5-flash tags = %v; want the free tag", flash.Tags)
	}
}

// presetHasTag mirrors Model.HasTag for ModelPreset.
func presetHasTag(p ModelPreset, tag string) bool {
	for _, t := range p.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

func TestPresetsCapabilityTagConsistency(t *testing.T) {
	for _, hint := range KnownProviders() {
		presets := GetModelPresets(hint.Name)
		if len(presets) == 0 {
			t.Errorf("catalog provider %q has no model presets", hint.Name)
			continue
		}
		for _, p := range presets {
			m := Model{ID: p.ID, Tags: p.Tags, Capabilities: p.Capabilities}
			if missing := m.MissingCapabilityTags(); len(missing) > 0 {
				t.Errorf("preset %q (%s) declares capabilities but is missing tags %v", p.ID, hint.Name, missing)
			}
		}
	}

	// The wizard writes these presets into the registry, so the saved models must
	// keep the same guarantee.
	for _, p := range GetModelPresets("openrouter") {
		m := Model{ID: p.ID, Tags: p.Tags, Capabilities: p.Capabilities}
		if missing := m.MissingCapabilityTags(); len(missing) > 0 {
			t.Errorf("openrouter preset %q is missing tags %v", p.ID, missing)
		}
	}

	// DeriveTags and MissingCapabilityTags agree on the tag vocabulary.
	if got := DeriveTags(Capabilities{SupportsTools: true, SupportsVision: true, SupportsThinking: true}); len(got) != 4 {
		t.Errorf("DeriveTags = %v; want 4 tags", got)
	}
	m := Model{ID: "x", Tags: []string{"general", "tools"}, Capabilities: Capabilities{SupportsTools: true, SupportsVision: true}}
	if missing := m.MissingCapabilityTags(); len(missing) != 1 || missing[0] != "vision" {
		t.Errorf("MissingCapabilityTags = %v; want [vision]", missing)
	}
}

func TestCapabilitySummary(t *testing.T) {
	cases := []struct {
		caps Capabilities
		want string
	}{
		{Capabilities{}, "none"},
		{Capabilities{SupportsTools: true}, "tools"},
		{Capabilities{SupportsTools: true, SupportsVision: true}, "tools, vision"},
		{Capabilities{SupportsThinking: true}, "thinking(default)"},
		{Capabilities{SupportsThinking: true, ReasoningEffort: "high"}, "thinking(high)"},
		{Capabilities{SupportsTools: true, SupportsVision: true, SupportsThinking: true, ReasoningEffort: "high"}, "tools, vision, thinking(high)"},
		{Capabilities{SupportsVideo: true, SupportsThinking: true, ReasoningEffort: "low"}, "video, thinking(low)"},
		// K1: a key-shaped reasoningEffort (nothing on the add-json /
		// hand-edit write path validates the field) renders masked at the
		// composition; the API path keeps the raw field.
		{Capabilities{SupportsThinking: true, ReasoningEffort: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, "thinking(wJalr****)"},
		// L1/F-1: a URL-shaped key effort masks at the composition (a scheme
		// is location-shaped on every platform), so the composed summary never
		// itself becomes location-shaped and rides raw downstream.
		{Capabilities{SupportsThinking: true, ReasoningEffort: "https://wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"}, "thinking(wJalr****)"},
		// M1: a PADDED key effort is trimmed before the masker, so the trailing
		// newline no longer defeats the classifier and rides raw.
		{Capabilities{SupportsThinking: true, ReasoningEffort: "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY\n"}, "thinking(wJalr****)"},
		// Round-4 P1 (batch M): a marker-decorated JWT effort masks at the
		// composition. The MaskRegistryValue mask branch dropped
		// isJWTShapedOperand, so the stripped JWT core rode raw here (and at
		// every other Summary sink: the banner, the wizard list).
		{Capabilities{SupportsThinking: true, ReasoningEffort: "./abcDef.ghiJkl.mnopqr"}, "thinking(abcDe****)"},
		{Capabilities{SupportsThinking: true, ReasoningEffort: "../aaaabcdef.gggghijk.iiiijjjj"}, "thinking(aaaab****)"},
	}
	for _, tc := range cases {
		if got := tc.caps.Summary(); got != tc.want {
			t.Errorf("Capabilities%+v.Summary() = %q; want %q", tc.caps, got, tc.want)
		}
	}
	if got := (Model{Capabilities: Capabilities{SupportsVision: true}}).CapabilitySummary(); got != "vision" {
		t.Errorf("Model.CapabilitySummary() = %q; want vision", got)
	}
}

// TestCapabilitySummary_MasksKeyShapedReasoningEffort pins the K1 composition
// fix end to end: the registry-persisted reasoningEffort is free-form text, so
// a misdirected key paste in that slot renders first-5+**** in the composed
// summary (and therefore at every display sink: the wizard model list at
// wizard.go:447, the status banner, RenderNumbered's provider row). The raw
// getter stays raw: the API path (fanout mapReasoningEffort, bridge
// NormalizeReasoningEffort) reads Capabilities.ReasoningEffort directly and
// never sees the composed text.
func TestCapabilitySummary_MasksKeyShapedReasoningEffort(t *testing.T) {
	key := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	caps := Capabilities{SupportsTools: true, SupportsVision: true, SupportsThinking: true, ReasoningEffort: key}
	want := "tools, vision, thinking(wJalr****)"
	if got := caps.Summary(); got != want {
		t.Errorf("Summary() = %q; want %q", got, want)
	}
	assertNoLeak(t, caps.Summary(), key)
	// The wizard model list renders exactly this expression (wizard.go:447).
	if got := (Model{Capabilities: caps}).CapabilitySummary(); got != want {
		t.Errorf("Model.CapabilitySummary() = %q; want %q", got, want)
	}
	// The API path contract: the raw getter is untouched by the display mask.
	if got := caps.EffectiveReasoningEffort(); got != key {
		t.Errorf("EffectiveReasoningEffort() = %q; want the raw %q (API path keeps the raw value)", got, key)
	}
	// Benign efforts render byte-identical through the mask.
	for _, effort := range []string{"default", "none", "low", "medium", "high", "xhigh", "max"} {
		benign := Capabilities{SupportsThinking: true, ReasoningEffort: effort}
		if got := benign.Summary(); got != "thinking("+effort+")" {
			t.Errorf("Summary(benign effort %q) = %q; want thinking(%s)", effort, got, effort)
		}
	}
}

// K1b: a LOCATION-shaped key-shaped effort ("./<key>") is not a legitimate
// effort and the slot has no path-readability requirement, so it masks at the
// composition even inside a >=3-part composite (where the composite itself
// defeats the <=1-space display class and the sink mask cannot help). Before
// the location fallback the plant rode raw at every display sink.
func TestCapabilitySummary_MasksLocationShapedReasoningEffort(t *testing.T) {
	key := "./wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	caps := Capabilities{SupportsTools: true, SupportsVision: true, SupportsThinking: true, ReasoningEffort: key}
	want := "tools, vision, thinking(wJalr****)"
	if got := caps.Summary(); got != want {
		t.Errorf("Summary() = %q; want %q", got, want)
	}
	assertNoLeak(t, caps.Summary(), key)
	if got := (Model{Capabilities: caps}).CapabilitySummary(); got != want {
		t.Errorf("Model.CapabilitySummary() = %q; want %q", got, want)
	}
	if got := caps.EffectiveReasoningEffort(); got != key {
		t.Errorf("EffectiveReasoningEffort() = %q; want the raw %q (API path keeps the raw value)", got, key)
	}
	if got := (Capabilities{SupportsThinking: true, ReasoningEffort: "./gemini"}).Summary(); got != "thinking(gemini)" {
		t.Errorf("Summary(location-shaped benign effort) = %q; want thinking(gemini)", got)
	}
	// L1/F-1: a Windows-drive- or abs-path-shaped key effort masks at the
	// composition too (these rode raw on Windows before batch L). The exact
	// form is platform-dependent, so they pin the no-leak mask.
	for _, effort := range []string{`C:\wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY`, "/wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"} {
		got := (Capabilities{SupportsThinking: true, ReasoningEffort: effort}).Summary()
		if strings.Contains(got, "wJalrXUtnFEMI") {
			t.Errorf("Summary(effort %q) = %q; leaks the key core", effort, got)
		}
	}
}

func TestDisplayNameRoundTrip(t *testing.T) {
	alias := DisplayName("gemini-2.5-flash", "google")
	if alias != "gemini-2.5-flash (google)" {
		t.Errorf("DisplayName = %q; want \"gemini-2.5-flash (google)\"", alias)
	}
	base, prov := ParseModelSpec(alias)
	if base != "gemini-2.5-flash" || prov != "google" {
		t.Errorf("ParseModelSpec(%q) = (%q, %q); want (gemini-2.5-flash, google)", alias, base, prov)
	}

	tgt := Target{Provider: Provider{Name: "google"}, Model: Model{ID: "gemini-2.5-pro"}}
	if tgt.DisplayName() != "gemini-2.5-pro (google)" {
		t.Errorf("Target.DisplayName() = %q", tgt.DisplayName())
	}
	if tgt.FullName() != "google/gemini-2.5-pro" {
		t.Errorf("Target.FullName() = %q", tgt.FullName())
	}

	// Names with spaces must still round-trip.
	spaced := DisplayName("gemini-2.5-flash", "Google AI Studio")
	base, prov = ParseModelSpec(spaced)
	if base != "gemini-2.5-flash" || prov != "Google AI Studio" {
		t.Errorf("ParseModelSpec(%q) = (%q, %q)", spaced, base, prov)
	}
}

func TestInteractiveSetupWizard_GoogleAIStudioPreset(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)
	t.Setenv(GoogleAIStudioKeyEnv, "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	// Simulate the AI Studio path via the "gemini" alias:
	// 1. Provider name: gemini
	// 2. Base URL: mock server (the AI Studio default is asserted separately)
	// 3. API Key: mock-google-key-123456
	// 4. Select preset 1: gemini-2.5-flash
	// 5. Add another model: N
	input := strings.Join([]string{
		"gemini",
		server.URL + "/v1beta/openai/",
		"mock-google-key-123456",
		"1",
		"N",
	}, "\n") + "\n"

	in := strings.NewReader(input)
	out := &bytes.Buffer{}

	if err := InteractiveSetupWizard(in, out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}

	outStr := out.String()
	for _, want := range []string{
		"Known providers:",
		"google (Google AI Studio)",
		"Aliases: glm, zhipu -> bigmodel; gemini, google ai studio, aistudio -> google; neuralwatt -> nw",
		"Get a key at: https://aistudio.google.com/apikey",
		"gemini-2.5-flash (gemini)",
		"capabilities: tools, vision, thinking(default)",
		`agy -m "gemini-2.5-flash (gemini)"`,
		`agy pro fanout --models "gemini/gemini-2.5-flash" --prompt`,
		"agy pro models",
	} {
		if !strings.Contains(outStr, want) {
			t.Errorf("wizard output missing %q\nOutput: %s", want, outStr)
		}
	}
	// Stale guidance: `agy pro fanout` has no -m flag and no positional prompt.
	if strings.Contains(outStr, "fanout -m") {
		t.Errorf("wizard still prints the removed `agy pro fanout -m` form:\n%s", outStr)
	}

	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(reg.Providers))
	}
	p := reg.Providers[0]
	if p.Name != "gemini" {
		t.Errorf("provider name = %q; want the name the user typed (gemini)", p.Name)
	}
	if p.APIKeyEnv != "GEMINI_API_KEY" {
		t.Errorf("apiKeyEnv = %q; want GEMINI_API_KEY", p.APIKeyEnv)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "gemini-2.5-flash" {
		t.Fatalf("unexpected models: %+v", p.Models)
	}
	m := p.Models[0]
	if !m.Capabilities.SupportsVision || !m.Capabilities.SupportsThinking || !m.Capabilities.SupportsTools {
		t.Errorf("gemini-2.5-flash capabilities = %+v; want tools, vision, thinking", m.Capabilities)
	}
	if missing := m.MissingCapabilityTags(); len(missing) > 0 {
		t.Errorf("saved model is missing capability tags %v", missing)
	}

	// The gateway resolves both alias forms, and the display alias round-trips.
	for _, spec := range []string{"gemini-2.5-flash (gemini)", "gemini/gemini-2.5-flash", "gemini-2.5-flash"} {
		tgt, err := reg.FindTarget(spec)
		if err != nil {
			t.Errorf("FindTarget(%q) failed: %v", spec, err)
			continue
		}
		if tgt.DisplayName() != "gemini-2.5-flash (gemini)" {
			t.Errorf("FindTarget(%q).DisplayName() = %q", spec, tgt.DisplayName())
		}
		if got, want := tgt.Provider.Endpoint(), server.URL+"/v1beta/openai/chat/completions"; got != want {
			t.Errorf("gateway endpoint = %q; want %q", got, want)
		}
	}
}
