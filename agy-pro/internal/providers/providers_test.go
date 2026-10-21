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
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"agypro/internal/testfixtures"
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
	input := "Bearer super_secret_token_123 in " + testfixtures.SKProjNumeric
	out := redact(input)

	if strings.Contains(out, "super_secret_token_123") {
		t.Errorf("failed to redact secret: %s", out)
	}
	if strings.Contains(out, testfixtures.SKProjNumeric) {
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
	// 13. Supports Video: N
	// 14. More models: N
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
		testfixtures.NvidiaMock,
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

	models, err := FetchRemoteModels(server.URL, "test-synthetic-key-0123456789abcdef")
	if err != nil {
		t.Fatalf("FetchRemoteModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "remote-model-1" || models[1].ID != "remote-model-2" {
		t.Fatalf("unexpected fetched models: %+v", models)
	}
	// Ids-only payloads advertise no ceilings: both slots stay 0 ("not
	// advertised"), the pre-P2-7 behaviour for every field downstream.
	if models[0].ContextLength != 0 || models[0].MaxOutputTokens != 0 {
		t.Fatalf("ids-only payload carried ceilings: %+v", models[0])
	}

	// Unreachable server surfaces ErrModelsConnect (P2-6: the five silent
	// failures are now distinguishable errors).
	empty, err := FetchRemoteModels("http://127.0.0.1:59999", "key")
	if !errors.Is(err, ErrModelsConnect) {
		t.Fatalf("unreachable server err = %v, want ErrModelsConnect", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty slice for unreachable server, got %+v", empty)
	}
}

// TestFetchRemoteModelsCeilings pins the P2-7 alias decode against the two
// real payload shapes the T-B live probe recorded (Charm-style and nw-style),
// their union, and payloads advertising nothing at all. Fixture values are the
// sanitized samples from survey-tb-models-live.md (ids + numbers only).
func TestFetchRemoteModelsCeilings(t *testing.T) {
	charmStyle := `{"data":[
		{"id":"charm-small","context_window":256000,"max_output_tokens":16000},
		{"id":"charm-mid","context_window":262144,"max_output_tokens":26214},
		{"id":"charm-big","context_window":1048576,"max_output_tokens":384000}
	]}`
	nwStyle := `{"data":[
		{"id":"nw-ctx-256k","max_model_len":262128},
		{"id":"nw-ctx-1m","max_model_len":1048560}
	]}`
	unionStyle := `{"data":[
		{"id":"union-a","context_window":1000000,"max_output_tokens":262144},
		{"id":"union-b","context_length":128000,"max_tokens":4096},
		{"id":"union-c","context_size":65536,"max_completion_tokens":8192},
		{"id":"union-d","max_context_tokens":32768},
		{"id":"union-e","max_input_tokens":16384}
	]}`
	junkShape := `{"data":[
		{"id":"junk-neg","context_window":-5,"max_output_tokens":-1},
		{"id":"junk-zero","context_window":0,"max_output_tokens":0},
		{"id":"junk-absurd","context_window":999999999999},
		{"id":"junk-string","context_window":"262144","max_output_tokens":"8192"},
		{"id":"junk-badstring","context_window":"unlimited","max_output_tokens":"4o96"},
		{"id":"junk-bool","context_window":true,"max_output_tokens":8192},
		{"id":"junk-fraction","context_window":128000.5,"max_output_tokens":4096},
		{"id":"","context_window":262144},
		{"id":"junk-multi","max_model_len":999999999999,"context_window":524288}
	]}`

	cases := []struct {
		name   string
		body   string
		ids    []string
		ctx    []int
		maxOut []int
	}{
		{
			name:   "charm style (context_window + max_output_tokens)",
			body:   charmStyle,
			ids:    []string{"charm-small", "charm-mid", "charm-big"},
			ctx:    []int{256000, 262144, 1048576},
			maxOut: []int{16000, 26214, 384000},
		},
		{
			name:   "nw style (max_model_len only, no max-output field)",
			body:   nwStyle,
			ids:    []string{"nw-ctx-256k", "nw-ctx-1m"},
			ctx:    []int{262128, 1048560},
			maxOut: []int{0, 0},
		},
		{
			name:   "union of alias families",
			body:   unionStyle,
			ids:    []string{"union-a", "union-b", "union-c", "union-d", "union-e"},
			ctx:    []int{1000000, 128000, 65536, 32768, 16384},
			maxOut: []int{262144, 4096, 8192, 0, 0},
		},
		{
			name:   "neither shape (ids only)",
			body:   `{"data":[{"id":"plain-1"},{"id":"plain-2"}]}`,
			ids:    []string{"plain-1", "plain-2"},
			ctx:    []int{0, 0},
			maxOut: []int{0, 0},
		},
		{
			name: "junk and sanity floor",
			body: junkShape,
			ids:  []string{"junk-neg", "junk-zero", "junk-absurd", "junk-string", "junk-badstring", "junk-bool", "junk-fraction", "junk-multi"},
			// -5/-1/0/999999999999 rejected; quoted numbers accepted; bools and
			// fractions rejected (context side; the sibling numeric ceiling
			// still parses); an empty id drops the whole row; the absurd
			// max_model_len is skipped and the sane context_window wins.
			ctx:    []int{0, 0, 0, 262144, 0, 0, 0, 524288},
			maxOut: []int{0, 0, 0, 8192, 0, 8192, 4096, 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.body))
			}))
			defer server.Close()

			models, err := FetchRemoteModels(server.URL, "")
			if err != nil {
				t.Fatalf("FetchRemoteModels: %v", err)
			}
			if len(models) != len(tc.ids) {
				t.Fatalf("got %d models %+v, want %d", len(models), models, len(tc.ids))
			}
			for i := range models {
				if models[i].ID != tc.ids[i] || models[i].ContextLength != tc.ctx[i] || models[i].MaxOutputTokens != tc.maxOut[i] {
					t.Fatalf("model %d = %+v, want id=%s ctx=%d maxOut=%d",
						i, models[i], tc.ids[i], tc.ctx[i], tc.maxOut[i])
				}
			}
		})
	}
}

// TestFetchRemoteModelsStripsControlRunes pins the decode-boundary rule
// (FIX-SEC-F1b, review MED-2): a remote id carrying C0 control runes or DEL is
// stripped before it leaves FetchRemoteModels, so no caller (TUI picker, wizard,
// the persisted registry) can paint a terminal escape sequence the endpoint
// planted in its /models answer. The strip runs BEFORE the trim, so a
// control-padded id keeps neither the rune nor its surrounding whitespace; an
// id that is nothing but control runes drops out of the list entirely, and a
// benign id stays byte-identical.
func TestFetchRemoteModelsStripsControlRunes(t *testing.T) {
	body := `{"data":[
		{"id":"\u001b[2J\u001b[1;1HFAKE-MODEL"},
		{"id":"\u007fdel-head"},
		{"id":" \u001btrim-me\u001b "},
		{"id":"\u001b\u0001\u007f"},
		{"id":"plain-model"}
	]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer server.Close()

	models, err := FetchRemoteModels(server.URL, "")
	if err != nil {
		t.Fatalf("FetchRemoteModels: %v", err)
	}
	want := []string{"[2J[1;1HFAKE-MODEL", "del-head", "trim-me", "plain-model"}
	if len(models) != len(want) {
		t.Fatalf("got %d models %+v, want %d (the all-control id drops)", len(models), models, len(want))
	}
	for i, id := range want {
		if models[i].ID != id {
			t.Fatalf("model %d id = %q, want %q", i, models[i].ID, id)
		}
		for _, r := range models[i].ID {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("model %d id %q still carries control rune %U", i, models[i].ID, r)
			}
		}
	}
}

// TestFetchRemoteModelsFailureKinds pins the five P2-6 failure surfaces: each
// silent nil of the old contract is now one sentinel, with sanitized detail
// (status code / host, never the URL or key material).
func TestFetchRemoteModelsFailureKinds(t *testing.T) {
	if _, err := FetchRemoteModels("   ", "key"); !errors.Is(err, ErrModelsNoBaseURL) {
		t.Fatalf("empty baseURL err = %v, want ErrModelsNoBaseURL", err)
	}

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer statusServer.Close()
	_, err := FetchRemoteModels(statusServer.URL, "key")
	if !errors.Is(err, ErrModelsStatus) {
		t.Fatalf("non-200 err = %v, want ErrModelsStatus", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("status detail missing code: %v", err)
	}

	decodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{not json`))
	}))
	defer decodeServer.Close()
	if _, err := FetchRemoteModels(decodeServer.URL, "key"); !errors.Is(err, ErrModelsDecode) {
		t.Fatalf("bad JSON err = %v, want ErrModelsDecode", err)
	}

	// A valid payload with an empty data array is NOT an error: the endpoint
	// answered fine and simply lists nothing (e.g. a keyless gateway).
	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer emptyServer.Close()
	models, err := FetchRemoteModels(emptyServer.URL, "key")
	if err != nil || len(models) != 0 {
		t.Fatalf("empty data: models=%+v err=%v, want empty nil", models, err)
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
	// 3. API Key: test-synthetic-key-0123456789abcdef
	// 4. Select preset 1: deepseek-chat
	// 5. Add another model: N
	input := strings.Join([]string{
		"deepseek",
		server.URL + "/v1",
		"test-synthetic-key-0123456789abcdef",
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

// TestInteractiveSetupWizard_RefusesKeyShapedFamily pins the wizard door's
// secret guard (K-2) on the family slot. The wizard persists straight into
// providers.json, so a key-shaped value pasted at the Model Family prompt is
// refused non-echoing and nothing is written, exactly as the TUI model form and
// the CLI model verbs refuse it. Before the guard the value was persisted and
// only the summary line masked it: the C2 display rule itself is pinned
// directly by TestMaskFamilyName, and this pin is the door that keeps the value
// from reaching the file at all.
func TestInteractiveSetupWizard_RefusesKeyShapedFamily(t *testing.T) {
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

	keyFamily := testfixtures.AWSSecretExample
	// Unknown provider (no presets): name, URL, key, then the custom row. The
	// family is the planted value, so the wizard refuses before the probe and
	// before the save.
	input := strings.Join([]string{
		"mycustomvendor",
		server.URL + "/v1",
		"mock-dummy-key-789",
		"custom-model-x",
		keyFamily,
	}, "\n") + "\n"

	out := &bytes.Buffer{}
	err := InteractiveSetupWizard(strings.NewReader(input), out)
	if err == nil {
		t.Fatalf("wizard accepted a key-shaped family: %s", out.String())
	}
	if !strings.Contains(err.Error(), "model family") || !strings.Contains(err.Error(), "value hidden") {
		t.Errorf("refusal = %q; want the non-echoing family refusal", err)
	}
	if strings.Contains(err.Error(), keyFamily) || strings.Contains(out.String(), keyFamily) {
		t.Errorf("refusal echoes the key-shaped family raw: err=%q output=%s", err, out.String())
	}
	if _, statErr := os.Stat(provPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a refused family must write nothing; stat error = %v", statErr)
	}

	// The benign half of the pre-guard pin: an explicit benign family still
	// renders readable in the summary and is persisted verbatim.
	out.Reset()
	benign := strings.Join([]string{
		"mycustomvendor",
		server.URL + "/v1",
		"mock-dummy-key-789",
		"custom-model-x",
		"gemini",
		"", "", "N", "N", "N",
		"N",
	}, "\n") + "\n"
	if err := InteractiveSetupWizard(strings.NewReader(benign), out); err != nil {
		t.Fatalf("benign family run returned error: %v\nOutput: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "[family: gemini") {
		t.Errorf("benign family must stay readable in the summary:\n%s", out.String())
	}
	if reg, loadErr := LoadFrom(provPath); loadErr != nil {
		t.Fatalf("benign run did not save: %v", loadErr)
	} else if got := reg.Providers[0].Models[0].Family; got != "gemini" {
		t.Errorf("registry family = %q; want gemini", got)
	}
}

// TestInteractiveSetupWizard_RefusesDecoratedModelFields pins the wizard door
// (K-2) against the DECORATED plant class the shared policy closes, on every
// free-text model field the wizard collects: the Model ID, the Model Family,
// and the allowed-tools list. Each refusal is non-echoing and writes nothing,
// and a benign row still persists verbatim, so the guard cannot become an
// over-refusal of ordinary model names.
func TestInteractiveSetupWizard_RefusesDecoratedModelFields(t *testing.T) {
	const plant = "./" + "\u2025/" + slotTestOpaque // refused by SecretShaped, readable to every pre-policy masker
	const core = slotTestOpaque
	if !SecretShaped(plant) {
		t.Fatalf("premise: the decorated plant %q must be classified as credential material", plant)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	cases := []struct {
		name      string
		lines     []string
		wantField string
	}{
		{
			name:      "decorated model ID",
			lines:     []string{plant},
			wantField: "model ID",
		},
		{
			name:      "decorated model family",
			lines:     []string{"acme-fast", plant},
			wantField: "model family",
		},
		{
			name:      "decorated tool name",
			lines:     []string{"acme-fast", "", "", "", "Y", plant + ",read_file"},
			wantField: "model tool name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provPath := filepath.Join(t.TempDir(), "providers.json")
			t.Setenv(EnvProviders, provPath)
			t.Setenv("MYCUSTOMVENDOR_API_KEY", "")

			input := strings.Join(append([]string{"mycustomvendor", server.URL + "/v1", "mock-dummy-key-789"}, tc.lines...), "\n") + "\n"
			out := &bytes.Buffer{}
			err := InteractiveSetupWizard(strings.NewReader(input), out)
			if err == nil {
				t.Fatalf("wizard accepted a decorated %s: %s", tc.wantField, out.String())
			}
			if !strings.Contains(err.Error(), tc.wantField) || !strings.Contains(err.Error(), "value hidden") {
				t.Errorf("refusal = %q; want the non-echoing %s refusal", err, tc.wantField)
			}
			if strings.Contains(err.Error(), core) || strings.Contains(out.String(), core) {
				t.Errorf("refusal echoes the credential core: err=%q output=%s", err, out.String())
			}
			if _, statErr := os.Stat(provPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("a refused value must write nothing; stat error = %v", statErr)
			}
		})
	}

	// Benign ids pass and persist verbatim, tools included.
	t.Run("benign row persists", func(t *testing.T) {
		provPath := filepath.Join(t.TempDir(), "providers.json")
		t.Setenv(EnvProviders, provPath)
		t.Setenv("MYCUSTOMVENDOR_API_KEY", "")

		input := strings.Join([]string{
			"mycustomvendor",
			server.URL + "/v1",
			"mock-dummy-key-789",
			"acme-fast",
			"acme",
			"", "", "Y", "read_file,list_dir",
			"N", "N", "N",
		}, "\n") + "\n"
		out := &bytes.Buffer{}
		if err := InteractiveSetupWizard(strings.NewReader(input), out); err != nil {
			t.Fatalf("benign run returned error: %v\nOutput: %s", err, out.String())
		}
		reg, err := LoadFrom(provPath)
		if err != nil {
			t.Fatalf("benign run did not save: %v", err)
		}
		if len(reg.Providers) != 1 || len(reg.Providers[0].Models) != 1 {
			t.Fatalf("unexpected saved registry: %+v", reg.Providers)
		}
		m := reg.Providers[0].Models[0]
		if m.ID != "acme-fast" || m.Family != "acme" {
			t.Errorf("saved model = %+v; want acme-fast/acme verbatim", m)
		}
		if got := strings.Join(m.Capabilities.AllowedTools, ","); got != "read_file,list_dir" {
			t.Errorf("saved tool grants = %q; want read_file,list_dir", got)
		}
	})
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
		"y",       // video: y
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
		"n", // video: n
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
		{Capabilities{SupportsThinking: true, ReasoningEffort: testfixtures.AWSSecretExample}, "thinking(wJalr****)"},
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
		// Round-5 P1 (batch N): a non-leading scheme is not stripped for
		// rendering (the benign sentence containing a URL stays intact), but
		// the tail behind the last mid-string "://" is classified for the
		// mask decision, so the composed summary masks instead of riding raw.
		{Capabilities{SupportsThinking: true, ReasoningEffort: "note https://" + testfixtures.AWSSecretExample}, "thinking(wJalr****)"},
		// Batch-N addendum: the mirror head check — a secret-shaped segment
		// before the first "://" with a benign tail masks at the composition.
		{Capabilities{SupportsThinking: true, ReasoningEffort: testfixtures.AWSSecretExample + "://x"}, "thinking(wJalr****)"},
		// Batch O (round-6 M-1): the marker-decorated segment sub-shape masks
		// at the composition (stripped-segment classification).
		{Capabilities{SupportsThinking: true, ReasoningEffort: "note https://./" + testfixtures.AWSSecretExample}, "thinking(./wJa****)"},
		// Batch-N LOW: a secret-shaped scheme-only value masks at the
		// composition; a benign one ("https://") renders unchanged.
		{Capabilities{SupportsThinking: true, ReasoningEffort: "abcdefghijklmnop://"}, "thinking(abcde****)"},
		{Capabilities{SupportsThinking: true, ReasoningEffort: "https://"}, "thinking(https://)"},
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
	key := testfixtures.AWSSecretExample
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

// wizardBaseURLTranscript drives the wizard to a completed save against a live
// loopback endpoint and returns the transcript, the registry it wrote, the Base
// URL it was given, and that URL without userinfo. userinfo is spliced into the
// URL authority when non-empty, so the shape under test still reaches a
// successful probe instead of the probe-failure menu.
func wizardBaseURLTranscript(t *testing.T, userinfo string) (transcript string, reg *Registry, rawURL, endpoint string) {
	t.Helper()
	provPath := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	t.Cleanup(server.Close)

	authority := strings.TrimPrefix(server.URL, "http://")
	endpoint = "http://" + authority + "/v1"
	rawURL = endpoint
	if userinfo != "" {
		rawURL = "http://" + userinfo + "@" + authority + "/v1"
	}

	// Catalog name with presets, so the flow stays short: provider name, base
	// URL, blank key, preset 1, no second model.
	input := strings.Join([]string{"deepseek", rawURL, "", "1", "N"}, "\n") + "\n"
	var out bytes.Buffer
	if err := InteractiveSetupWizard(strings.NewReader(input), &out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}
	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load saved registry: %v", err)
	}
	if len(reg.Providers) != 1 {
		t.Fatalf("expected 1 saved provider, got %d", len(reg.Providers))
	}
	return out.String(), reg, rawURL, endpoint
}

// TestInteractiveSetupWizard_BaseURLSummaryBenignByteIdentical pins the
// no-change half of FIX-G: for a URL with no credential shape the completion
// summary line is byte-identical to the pre-fix expectation (the raw value).
// Reason it is unchanged: the summary now routes through MaskBaseURL, which is
// the identity on such a URL. MaskOperand leaves a plain URL readable, url.Parse
// finds no userinfo, no query and no fragment to strip or mask, and the "@" arm
// is never reached, so the helper returns its input unchanged.
func TestInteractiveSetupWizard_BaseURLSummaryBenignByteIdentical(t *testing.T) {
	transcript, reg, rawURL, _ := wizardBaseURLTranscript(t, "")

	if want := "Base URL        : " + rawURL + "\n"; !strings.Contains(transcript, want) {
		t.Errorf("summary line is not the pre-fix byte-identical rendering (want %q):\n%s", want, transcript)
	}
	if got := MaskBaseURL(rawURL); got != rawURL {
		t.Fatalf("MaskBaseURL(%q) = %q; the benign identity this pin relies on moved", rawURL, got)
	}
	// Masking is display-only: the registry keeps the value the user typed.
	if reg.Providers[0].BaseURL != rawURL {
		t.Errorf("saved baseURL = %q; want the typed value %q (masking must be display-only)", reg.Providers[0].BaseURL, rawURL)
	}
}

// TestInteractiveSetupWizard_BaseURLSummaryMasksUserinfo pins FIX-G: a
// credential-shaped userinfo URL (validateBaseURL accepts it: https plus host,
// or http on a loopback host) must not echo raw in the completion summary. The
// summary renders through MaskBaseURL, the same layered URL mask the TUI twin
// uses (tui maskBaseURL, internal/tui/screen_providers.go:418, kept in parity
// with providers.MaskBaseURL by its cross-package pin in
// internal/tui/screen_providers_test.go), so the wizard and the TUI render the
// same input the same way.
func TestInteractiveSetupWizard_BaseURLSummaryMasksUserinfo(t *testing.T) {
	const secret = "hunter2sekret"
	transcript, reg, rawURL, endpoint := wizardBaseURLTranscript(t, "user:"+secret)

	if strings.Contains(transcript, secret) || strings.Contains(transcript, rawURL) {
		t.Errorf("wizard transcript echoes the userinfo credential raw:\n%s", transcript)
	}
	if want := "Base URL        : " + endpoint + "\n"; !strings.Contains(transcript, want) {
		t.Errorf("summary missing the masked rendering %q:\n%s", want, transcript)
	}
	// The rendering is exactly the shared authority's: userinfo dropped, host
	// and path readable.
	if got := MaskBaseURL(rawURL); got != endpoint {
		t.Fatalf("MaskBaseURL(%q) = %q; want the userinfo-free endpoint %q", rawURL, got, endpoint)
	}
	// Masking is display-only: the registry keeps the value the user typed, so
	// the gateway still authenticates against that endpoint.
	if reg.Providers[0].BaseURL != rawURL {
		t.Errorf("saved baseURL = %q; want the typed value (masking must be display-only)", reg.Providers[0].BaseURL)
	}
}

// wizardEditRun is the outcome of one probe-failure edit-branch run.
type wizardEditRun struct {
	transcript string
	reg        *Registry
	rawURL     string // the Base URL the wizard was given, userinfo included
	endpoint   string // rawURL without userinfo: what the masker renders
	typedURL   string // the value typed at the edit prompt ("" when the default was accepted)
}

// wizardEditPromptRun drives the wizard through a probe failure into the
// "3) Edit Base URL" branch and returns the transcript plus the registry it
// wrote. userinfo is spliced into the Base URL authority when non-empty, so the
// credential shape under test reaches the edit prompt's default. A non-empty
// typedPath is typed at that prompt as server URL + typedPath (a path the test
// server accepts); an empty typedPath accepts the default with an empty line.
func wizardEditPromptRun(t *testing.T, userinfo, typedPath string) wizardEditRun {
	t.Helper()
	provPath := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ok/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	authority := strings.TrimPrefix(server.URL, "http://")
	rawURL := "http://" + authority + "/v1"
	if userinfo != "" {
		rawURL = "http://" + userinfo + "@" + authority + "/v1"
	}
	run := wizardEditRun{
		rawURL:   rawURL,
		endpoint: "http://" + authority + "/v1",
		typedURL: typedPath,
	}
	if typedPath != "" {
		run.typedURL = server.URL + typedPath
	}

	steps := []string{
		"nw",           // catalog name with presets, so the flow stays short
		rawURL,         // Base URL prompt: the shape under test
		"sk-test-0000", // synthetic key, so the probe carries a bearer
		"1",            // preset 1
		"3",            // choice 3: Edit Base URL or API Key
		run.typedURL,   // Base URL at the edit prompt ("" accepts the default)
		"",             // API Key: keep current
	}
	if typedPath == "" {
		steps = append(steps, "1") // the probe failed again: save anyway
	}
	steps = append(steps, "N") // no second model

	var out bytes.Buffer
	if err := InteractiveSetupWizard(strings.NewReader(strings.Join(steps, "\n")+"\n"), &out); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}
	run.transcript = out.String()
	saved, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("failed to load saved registry: %v", err)
	}
	if len(saved.Providers) != 1 {
		t.Fatalf("expected 1 saved provider, got %d", len(saved.Providers))
	}
	run.reg = saved
	return run
}

// TestInteractiveSetupWizard_EditPromptBenignDefaultByteIdentical pins the
// no-change half of FIX-J: for a URL with no credential shape the edit prompt's
// bracketed default is byte-identical to the pre-fix rendering ("Base URL
// [<current URL>]: "). Reason it is unchanged: the default now routes through
// MaskBaseURL, which is the identity on such a URL. The prompt line builds
// exactly the pre-fix format, so the rendering the operator sees did not move.
func TestInteractiveSetupWizard_EditPromptBenignDefaultByteIdentical(t *testing.T) {
	run := wizardEditPromptRun(t, "", "")

	if want := "Base URL [" + run.rawURL + "]: "; !strings.Contains(run.transcript, want) {
		t.Errorf("edit prompt is not the pre-fix byte-identical rendering (want %q):\n%s", want, run.transcript)
	}
	if got := MaskBaseURL(run.rawURL); got != run.rawURL {
		t.Fatalf("MaskBaseURL(%q) = %q; the benign identity this pin relies on moved", run.rawURL, got)
	}
	// Accepting the default writes the value the wizard was given.
	if run.reg.Providers[0].BaseURL != run.rawURL {
		t.Errorf("saved baseURL = %q; want the current value %q", run.reg.Providers[0].BaseURL, run.rawURL)
	}
}

// TestInteractiveSetupWizard_EditPromptMasksUserinfoDefault pins FIX-J: the edit
// prompt's bracketed default is the CURRENT base URL, and validateBaseURL
// accepts a userinfo URL, so the raw value used to print at this prompt in
// captured or piped runs. The default now renders through MaskBaseURL, the same
// authority the completion summary uses (FIX-G, wizard.go summary block), while
// the accepted default still returns the RAW value: the wizard writes what the
// prompt returns, so masking the return value would persist the mask.
func TestInteractiveSetupWizard_EditPromptMasksUserinfoDefault(t *testing.T) {
	const secret = "hunter2sekret"
	run := wizardEditPromptRun(t, "user:"+secret, "")

	if strings.Contains(run.transcript, secret) || strings.Contains(run.transcript, run.rawURL) {
		t.Errorf("wizard transcript echoes the userinfo credential raw:\n%s", run.transcript)
	}
	if want := "Base URL [" + run.endpoint + "]: "; !strings.Contains(run.transcript, want) {
		t.Errorf("edit prompt missing the masked default %q:\n%s", want, run.transcript)
	}
	// The rendering is exactly the shared authority's.
	if got := MaskBaseURL(run.rawURL); got != run.endpoint {
		t.Fatalf("MaskBaseURL(%q) = %q; want the userinfo-free endpoint %q", run.rawURL, got, run.endpoint)
	}
	// Display-only masking: the accepted default returns the raw current value, so
	// the gateway still authenticates against that endpoint.
	if run.reg.Providers[0].BaseURL != run.rawURL {
		t.Errorf("saved baseURL = %q; want the raw current value %q (the masked default must be display-only)", run.reg.Providers[0].BaseURL, run.rawURL)
	}
}

// TestInteractiveSetupWizard_EditPromptTypedURLWins pins the other unchanged
// half of FIX-J: a value typed at the edit prompt still replaces the base URL,
// and the default it was offered still renders through the masker.
func TestInteractiveSetupWizard_EditPromptTypedURLWins(t *testing.T) {
	run := wizardEditPromptRun(t, "", "/ok")

	if !strings.Contains(run.transcript, "Connection succeeded!") {
		t.Errorf("the typed base URL did not reach a successful probe:\n%s", run.transcript)
	}
	if run.reg.Providers[0].BaseURL != run.typedURL {
		t.Errorf("saved baseURL = %q; want the typed value %q", run.reg.Providers[0].BaseURL, run.typedURL)
	}
	// The offered default is still the pre-edit current URL, rendered masked.
	if want := "Base URL [" + run.endpoint + "]: "; !strings.Contains(run.transcript, want) {
		t.Errorf("edit prompt missing the pre-edit default %q:\n%s", want, run.transcript)
	}
}

// ---------------------------------------------------------------------------
// FIX-7B: the setup wizard's provider-name and base-URL doors. The wizard is a
// write path straight into providers.json with no approval gate, so both prompts
// classify their input with the registry-slot authority before anything is
// probed or written.
// ---------------------------------------------------------------------------

// wizardGuardScript drives the wizard past the name and base-URL prompts and
// through one custom model, then discards it ("4") so the run ends with the
// no-models abort and writes nothing. It fits both the name table and the URL
// table: an accepted value reaches the later prompts, a refused one aborts at
// the prompt under test.
func wizardGuardScript(name, baseURL string) string {
	return strings.Join([]string{
		name, baseURL, "ACMEGUARD_KEY", "", "acme-model",
		"", "", "", "Y", "", "N", "N", "N", "4", "",
	}, "\n") + "\n"
}

// TestInteractiveSetupWizard_NameGuard drives the REAL name prompt with 34 names
// (18 legal, 16 credential-shaped) and pins the door on both sides: a shape that
// the charset accepts but the registry-slot authority refuses aborts before the
// base-URL prompt, value-free, with nothing written; a legal name still reaches
// the next prompt, so the guard cannot become a blanket refusal.
func TestInteractiveSetupWizard_NameGuard(t *testing.T) {
	benignURL := closedLoopbackBaseURL(t) + "/v1"
	for _, tc := range []struct {
		name   string
		refuse bool
	}{
		// Legal names: the registry-slot authority is the identity on every one.
		{name: "acme"},
		{name: "acmeguard"},
		{name: "a"},
		{name: "zz9"},
		{name: "prov1"},
		{name: "my-provider"},
		{name: "my_provider"},
		{name: "provider-name-with-hyphens"},
		{name: "key_backup"},
		{name: "tokenstore"},
		{name: "sklearn-gateway"},
		{name: "qwen-gateway"},
		{name: "cedar.2024"},
		{name: "my.provider.v2"},
		{name: "acct_123"},
		{name: "us-east-1-gw"},
		{name: "provider-2024"},
		{name: "provname24"},
		// Credential shapes: the charset accepts them (letters, digits,
		// underscore, dot, hyphen) and MaskOperand is the identity on the
		// underscore family, so only the slot authority refuses them.
		{name: testfixtures.SKLive, refuse: true},
		{name: testfixtures.SKLiveLong, refuse: true},
		{name: testfixtures.TokSample, refuse: true},
		{name: testfixtures.PKLiveHex, refuse: true},
		{name: testfixtures.KeyHex, refuse: true},
		{name: testfixtures.AKIAUnderscore, refuse: true},
		{name: testfixtures.AKIAExample, refuse: true},
		{name: testfixtures.SKProj, refuse: true},
		{name: testfixtures.GroqKeyLong, refuse: true},
		{name: testfixtures.NvidiaKey, refuse: true},
		{name: testfixtures.GHPNumericLong, refuse: true},
		{name: testfixtures.SlackBotDashed, refuse: true},
		{name: testfixtures.JWTHeader, refuse: true},
		{name: "0123456789abcdef", refuse: true},
		{name: "0123456789abcdef0123456789abcdef", refuse: true},
		{name: "a1b2c3d4e5f6g7h8i9j0k1l2", refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(defaultEnvFor(tc.name), "")
			run := runWizardScript(t, wizardGuardScript(tc.name, benignURL))

			if !tc.refuse {
				if run.err == nil || !strings.Contains(run.err.Error(), "no models configured") {
					t.Fatalf("legal name %q was not accepted at the name prompt: err=%v\ntranscript:\n%s", tc.name, run.err, run.transcript)
				}
				if !strings.Contains(run.transcript, "Enter API Key for") {
					t.Fatalf("legal name %q did not reach the key prompt:\n%s", tc.name, run.transcript)
				}
				return
			}
			if run.err == nil {
				t.Fatalf("credential-shaped name %q was accepted:\n%s", tc.name, run.transcript)
			}
			if !strings.Contains(run.err.Error(), "provider name") || !strings.Contains(run.err.Error(), "value hidden") {
				t.Fatalf("refusal for %q = %q; want the value-free provider-name refusal", tc.name, run.err)
			}
			assertNoLeak(t, run.err.Error(), tc.name)
			assertNoLeak(t, run.transcript, tc.name)
			// Fail closed at the name prompt: the key prompt was never shown.
			if strings.Contains(run.transcript, "Enter API Key for") {
				t.Fatalf("the name refusal landed after the URL prompt for %q:\n%s", tc.name, run.transcript)
			}
			if run.wrote {
				t.Fatalf("a refused name wrote a registry")
			}
		})
	}
}

// hostDoorProbeJWT is the synthetic JWT this file pastes into the HOST slot
// (fixture only: the standard eyJ header, so the target machine's classifiers
// read it as the JWT class).
const hostDoorProbeJWT = testfixtures.JWTHeader + "." + testfixtures.JWTPayload + ".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"

// TestInteractiveSetupWizard_BaseURLGuard drives the REAL base-URL prompt with
// 39 URLs (16 legal gateway shapes, 23 credential-bearing ones) and pins the
// door: a credential in the host, the path, the query, the fragment, or the
// userinfo is refused value-free before the key prompt and before any write,
// while ordinary gateway URLs (including the Cloudflare 32-hex account-id path
// and the identifier shapes) stay accepted. The accept side of the two long
// gateway hostnames is pinned on the guard
// (TestWizardBaseURLDoorMatchesSharedAuthority) and end to end on the TUI form
// and the add-json parser, because a full wizard run for an accepted external
// host would resolve it (the very leak this unit closes).
func TestInteractiveSetupWizard_BaseURLGuard(t *testing.T) {
	base := closedLoopbackBaseURL(t)
	authority := strings.TrimPrefix(base, "http://")
	const (
		underscore = testfixtures.SKLive
		tokHead    = testfixtures.TokSample
		pkHead     = testfixtures.PKLiveHex
		keyHead    = testfixtures.KeyHex
		akiaHead   = testfixtures.AKIAUnderscore
	)
	for _, tc := range []struct {
		name    string
		url     string
		refuse  bool
		secrets []string
	}{
		// Ordinary gateway shapes: every URL part is identity under the slot rule.
		{name: "root", url: base},
		{name: "v1", url: base + "/v1"},
		{name: "chat completions", url: base + "/v1/chat/completions"},
		{name: "benign query", url: base + "/v1?api-version=2024-08-01"},
		{name: "benign query pair", url: base + "/v1/chat/completions?alt=sse&api-version=2024-08-01"},
		{name: "cloudflare account id", url: base + "/v1/0123456789abcdef0123456789abcdef/anthropic"},
		{name: "32 hex account id", url: base + "/v1/0123456789abcdef0123456789abcdef"},
		{name: "24 hex object id", url: base + "/v1/507f1f77bcf86cd799439011/users"},
		{name: "40 hex sha1 id", url: base + "/v1/0123456789abcdef0123456789abcdef012345678"},
		{name: "uuid id", url: base + "/v1/123e4567-e89b-12d3-a456-426614174000/chat"},
		{name: "azure deployment", url: base + "/openai/deployments/gpt-4o?api-version=2024-02-01"},
		{name: "model path", url: base + "/v1/model-2024"},
		{name: "benign userinfo", url: "http://user:hunter2sekret@" + authority + "/v1"},
		{name: "benign fragment", url: base + "/v1#section"},
		{name: "v1beta", url: base + "/v1beta/models"},
		{name: "gateway name", url: base + "/v1/gw-name"},
		// Credential-bearing shapes.
		{name: "path jwt", url: base + "/v1/" + probeTestJWT, refuse: true, secrets: []string{probeTestJWT}},
		{name: "path underscore head", url: base + "/v1/" + underscore, refuse: true, secrets: []string{underscore}},
		{name: "path long underscore head", url: base + "/v1/" + underscore + underscore, refuse: true, secrets: []string{underscore}},
		{name: "path tok head", url: base + "/v1/" + tokHead, refuse: true, secrets: []string{tokHead}},
		{name: "path pk head", url: base + "/v1/" + pkHead, refuse: true, secrets: []string{pkHead}},
		{name: "path key head", url: base + "/v1/" + keyHead, refuse: true, secrets: []string{keyHead}},
		{name: "path akia head", url: base + "/v1/" + akiaHead, refuse: true, secrets: []string{akiaHead}},
		{name: "path 16 run", url: base + "/v1/0123456789abcdef", refuse: true, secrets: []string{"0123456789abcdef"}},
		{name: "path mixed case run", url: base + "/v1/AbCdEf0123456789", refuse: true, secrets: []string{"AbCdEf0123456789"}},
		{name: "path percent encoded", url: base + "/v1/sk%5Flive%5F0123456789abcdef", refuse: true, secrets: []string{underscore}},
		{name: "query value", url: base + "/v1?api_key=" + underscore, refuse: true, secrets: []string{underscore}},
		{name: "query key", url: base + "/v1?" + underscore + "=1", refuse: true, secrets: []string{underscore}},
		{name: "fragment", url: base + "/v1#" + underscore, refuse: true, secrets: []string{underscore}},
		{name: "userinfo username", url: "http://" + underscore + "@" + authority + "/v1", refuse: true, secrets: []string{underscore}},
		{name: "userinfo password", url: "http://user:" + underscore + "@" + authority + "/v1", refuse: true, secrets: []string{underscore}},
		{name: "whole value key paste", url: "sk-ant-0123456789abcdef", refuse: true, secrets: []string{"sk-ant-0123456789abcdef"}},
		{name: "hostless opaque", url: "https:" + underscore, refuse: true, secrets: []string{underscore}},
		// FIX-8f: the HOST slot. The key is the DNS name itself, so the live probe
		// resolved it (leaking it to the resolver). Each row is refused at the URL
		// prompt before the key prompt, so no request is composed at all.
		{name: "host underscore head", url: "https://" + underscore + "/v1", refuse: true, secrets: []string{underscore}},
		{name: "host tok head", url: "https://" + tokHead + "/v1", refuse: true, secrets: []string{tokHead}},
		{name: "host ghp head", url: "https://" + testfixtures.GHPShort + ".example.com/v1", refuse: true, secrets: []string{testfixtures.GHPShort}},
		{name: "host aws key id", url: "https://" + testfixtures.AKIAExample + "/v1", refuse: true, secrets: []string{testfixtures.AKIAExample}},
		{name: "host jwt", url: "https://" + hostDoorProbeJWT + "/v1", refuse: true, secrets: []string{hostDoorProbeJWT}},
		{name: "host decorated", url: "https://1-" + testfixtures.ASIADocExample + ".example.com/v1", refuse: true, secrets: []string{testfixtures.ASIADocExample}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ACMEGUARD_API_KEY", "")
			run := runWizardScript(t, wizardGuardScript("acmeguard", tc.url))

			if !tc.refuse {
				if run.err == nil || !strings.Contains(run.err.Error(), "no models configured") {
					t.Fatalf("gateway URL %q was not accepted: err=%v\ntranscript:\n%s", tc.url, run.err, run.transcript)
				}
				if !strings.Contains(run.transcript, "Enter API Key for") {
					t.Fatalf("gateway URL %q did not reach the key prompt:\n%s", tc.url, run.transcript)
				}
				return
			}
			if run.err == nil {
				t.Fatalf("credential-bearing URL %q was accepted:\n%s", tc.url, run.transcript)
			}
			if !strings.Contains(run.err.Error(), "Base URL looks like secret material") ||
				!strings.Contains(run.err.Error(), "value hidden") {
				t.Fatalf("refusal for %q = %q; want the value-free Base URL refusal", tc.url, run.err)
			}
			for _, secret := range tc.secrets {
				assertNoLeak(t, run.err.Error(), secret)
				assertNoLeak(t, run.transcript, secret)
			}
			// Fail closed at the URL prompt: the key prompt was never shown, so no
			// key was ever collected for the refused endpoint.
			if strings.Contains(run.transcript, "Enter API Key for") {
				t.Fatalf("the URL refusal landed after the key prompt for %q:\n%s", tc.url, run.transcript)
			}
			if run.wrote {
				t.Fatalf("a refused URL wrote a registry")
			}
		})
	}
}

// TestInteractiveSetupWizard_EditPromptRefusesTypedCredentialURL pins the FIX-7B
// edit-branch guard: the edit prompt validated nothing, so a credential-bearing
// replacement typed there reached the probe and the write. A typed replacement
// now passes the same guard as the step-2 prompt; the accepted default keeps its
// masked display and raw return (the FIX-J pins).
func TestInteractiveSetupWizard_EditPromptRefusesTypedCredentialURL(t *testing.T) {
	const secret = testfixtures.SKLive
	provPath := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(EnvProviders, provPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[]}`))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	// The typed replacement is the only line that differs from a passing edit run;
	// the wizard refuses it before the API Key prompt and before the second probe.
	steps := []string{
		"nw", // catalog name with presets, so the flow stays short
		server.URL + "/v1",
		"sk-test-0000", // synthetic key, so the probe carries a bearer
		"1",            // preset 1
		"3",            // choice 3: Edit Base URL or API Key
		server.URL + "/v1/" + secret,
		"", // API Key: keep current
	}
	out := &bytes.Buffer{}
	err := InteractiveSetupWizard(strings.NewReader(strings.Join(steps, "\n")+"\n"), out)
	if err == nil {
		t.Fatalf("the wizard accepted a credential-bearing edit replacement:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "Base URL looks like secret material") ||
		!strings.Contains(err.Error(), "value hidden") {
		t.Fatalf("refusal = %q; want the value-free Base URL refusal", err)
	}
	assertNoLeak(t, err.Error(), secret)
	assertNoLeak(t, out.String(), secret)
	if _, statErr := os.Stat(provPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused edit replacement must write nothing; stat error = %v", statErr)
	}
}

// TestInteractiveSetupWizard_RefusesUnderscoreModelFields is the FIX-8e door
// pin: the wizard's model-field guard classified with SecretShaped alone, so a
// key-shaped model ID of the underscore class passed the Model ID, family, tags,
// and tools prompts and persisted raw into models[].id while the TUI model form,
// the CLI model verbs, and add-json all refused it (fix7-C's report listed the
// leftover; no unit landed it). Every row is refused value-free, nothing is
// written, and the pre-write sweep reads the same authority, so a preset row or
// an endpoint-discovered id cannot slip in either.
func TestInteractiveSetupWizard_RefusesUnderscoreModelFields(t *testing.T) {
	probes := []struct{ name, value string }{
		{"sk underscore head", testfixtures.SKLive},
		{"tok underscore head", testfixtures.TokSample},
		{"pk underscore head", testfixtures.PKLiveHex},
		{"key underscore head", testfixtures.KeyHex},
		{"AKIA underscore head", testfixtures.AKIAUnderscore},
	}
	for _, probe := range probes {
		// Premise: the pre-fix classifier (SecretShaped alone) is the identity on
		// every row, so each one exercises exactly the authority switch.
		if SecretShaped(probe.value) {
			t.Fatalf("premise: %q is already refused by SecretShaped alone", probe.value)
		}
		if !NameCarriesCredential(probe.value) {
			t.Fatalf("premise: the shared authority does not refuse %q", probe.value)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			provPath := filepath.Join(t.TempDir(), "providers.json")
			t.Setenv(EnvProviders, provPath)
			t.Setenv("MYCUSTOMVENDOR_API_KEY", "")

			input := strings.Join([]string{
				"mycustomvendor", server.URL + "/v1", "mock-dummy-key-789", probe.value,
			}, "\n") + "\n"
			out := &bytes.Buffer{}
			err := InteractiveSetupWizard(strings.NewReader(input), out)
			if err == nil {
				t.Fatalf("the wizard accepted %q as a model ID: %s", probe.value, out.String())
			}
			if !strings.Contains(err.Error(), "model ID") || !strings.Contains(err.Error(), "value hidden") {
				t.Fatalf("refusal = %q; want the non-echoing model ID refusal", err)
			}
			if strings.Contains(err.Error(), probe.value) || strings.Contains(out.String(), probe.value) {
				t.Fatalf("the refusal echoes the refused value: err=%q output=%s", err, out.String())
			}
			if _, statErr := os.Stat(provPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a refused model field must write nothing; stat error = %v", statErr)
			}
		})
	}

	// The pre-write sweep (the path a preset row or a discovered id takes) reads
	// the same authority on every free-text field the wizard collects.
	const probe = testfixtures.TokSample
	for _, field := range []struct {
		name  string
		model Model
	}{
		{"model ID", Model{ID: probe}},
		{"model family", Model{ID: "acme-fast", Family: probe}},
		{"model tag", Model{ID: "acme-fast", Tags: []string{probe}}},
		{"model tool name", Model{ID: "acme-fast", Capabilities: Capabilities{AllowedTools: []string{probe}}}},
	} {
		err := wizardModelFieldsRefused([]Model{field.model})
		if err == nil {
			t.Fatalf("wizardModelFieldsRefused accepted a key-shaped %s", field.name)
		}
		if !strings.Contains(err.Error(), field.name) || !strings.Contains(err.Error(), "value hidden") {
			t.Fatalf("%s refusal = %q; want the named, value-free sentence", field.name, err)
		}
		if strings.Contains(err.Error(), probe) {
			t.Fatalf("%s refusal echoes the value: %q", field.name, err)
		}
	}
}

// TestWizardModelFieldsAcceptRealisticIDs is the accept side of FIX-8e: every
// realistic model ID shape the TUI model form and the CLI model verbs accept
// (Ollama tags, quant suffixes, Bedrock ARNs, HF and MLX paths, dated and
// family-qualified ids) is unchanged under the shared authority, so the switch
// cannot become an over-refusal of ordinary model names. The location-shaped-ID
// verdict (a model ID that is a path or a URL) is a HELD product decision, pinned
// on the CLI and TUI sides; this table deliberately contains no such row and does
// not re-open it.
func TestWizardModelFieldsAcceptRealisticIDs(t *testing.T) {
	for _, id := range []string{
		"llama3.2:latest",
		"qwen2.5-coder:32b-instruct-q4_K_M",
		"deepseek-r1:8b-q4_K_M",
		"mistralai/Mixtral-8x22B-Instruct-v0.1",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo",
		"mlx-community/Qwen2.5-72B-Instruct-4bit",
		"us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-5-sonnet-20241022-v2:0",
		"accounts/fireworks/models/llama-v3p1-70b-instruct",
		"gpt-4o-2024-08-06",
		"claude-sonnet-5-5-high",
		"gemini-3.8-flash",
		"acme-fast",
	} {
		if NameCarriesCredential(id) {
			t.Errorf("NameCarriesCredential(%q) = true; want false (realistic model ID)", id)
		}
		if err := wizardFieldRefused("model ID", id); err != nil {
			t.Errorf("wizardFieldRefused(model ID, %q) = %v; want accepted", id, err)
		}
	}
}

// TestMaskProbeTranscriptMasksCredentialHost is the FIX-8f defense-in-depth pin
// on the probe-failure transcript: the seed builder classified the path, the
// query, the fragment, and the userinfo, but not the HOST, so a key pasted as
// the DNS name printed raw inside the wrapped *url.Error sentence (the same
// value the resolver saw). The host labels are now seeded through the shared host
// rule, and a benign gateway host still yields no seed at all, so its
// transcript stays byte-identical.
func TestMaskProbeTranscriptMasksCredentialHost(t *testing.T) {
	const secret = testfixtures.SKLive
	for _, tc := range []struct {
		name string
		base string
		host string
	}{
		{"bare host", "https://" + secret + "/v1", secret},
		{"host with port", "https://" + secret + ":8443/v1", secret + ":8443"},
		{"host behind a benign label", "https://" + secret + ".example.com/v1", secret + ".example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentence := `network error contacting ` + tc.host + `: Post "` + tc.base + `/chat/completions": dial tcp: lookup ` + tc.host + `: no such host`
			got := maskProbeTranscript(sentence, tc.base)
			if strings.Contains(got, secret) {
				t.Fatalf("the host credential survived masking: %q", got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("the host credential was not redacted: %q", got)
			}
			if !strings.Contains(got, "network error contacting") {
				t.Fatalf("the sentence wording was lost: %q", got)
			}
		})
	}

	// Benign long gateway hostnames: the host rule is the identity on both, so the
	// seed set stays empty and the transcript keeps its exact bytes.
	for _, base := range []string{
		"https://gw.example/v1",
		"https://mycompany2024gateway.example.com/v1",
		"https://acmeprod2024gateway.example.com/v1",
	} {
		if seeds := probeCredentialPathSeeds(base); len(seeds) != 0 {
			t.Fatalf("probeCredentialPathSeeds(%q) = %q; want no seeds on a benign gateway host", base, seeds)
		}
		sentence := `network error contacting gw.example: Post "` + base + `/chat/completions": dial tcp 10.0.0.1:443: connect: connection refused`
		if got := maskProbeTranscript(sentence, base); got != sentence {
			t.Fatalf("maskProbeTranscript changed a benign sentence:\n got %q\nwant %q", got, sentence)
		}
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): Registry.Validate quotes the duplicated
// provider NAME and the duplicated model ID. Both are registry slots, so they
// render through MaskSlot (the registry-slot authority) rather than the
// operand masker, whose underscore-head arm is the identity. The benign
// sentences the pre-fix suite pins stay byte-identical.
// ---------------------------------------------------------------------------

func TestValidateEchoesRegistrySlotsThroughSlotAuthority(t *testing.T) {
	const (
		plant     = testfixtures.SKLive
		plantMask = "sk_li****"
	)

	// Duplicate provider NAME.
	reg := &Registry{Providers: []Provider{
		{Name: plant, BaseURL: "https://a.example/v1"},
		{Name: plant, BaseURL: "https://b.example/v1"},
	}}
	err := reg.Validate()
	if err == nil {
		t.Fatal("Validate must reject a duplicated provider name")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("Validate error echoes the duplicated name raw: %v", err)
	}
	if want := `provider "` + plantMask + `" is defined twice`; err.Error() != want {
		t.Errorf("Validate error drifted\n got: %q\nwant: %q", err.Error(), want)
	}

	// Duplicate model ID inside one provider.
	reg = &Registry{Providers: []Provider{{
		Name:    "acme",
		BaseURL: "https://acme.example/v1",
		Models:  []Model{{ID: plant}, {ID: plant}},
	}}}
	err = reg.Validate()
	if err == nil {
		t.Fatal("Validate must reject a duplicated model id")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("Validate error echoes the duplicated model id raw: %v", err)
	}
	if want := `provider "acme": model "` + plantMask + `" is listed twice`; err.Error() != want {
		t.Errorf("Validate error drifted\n got: %q\nwant: %q", err.Error(), want)
	}

	// Benign corpus: byte-identical sentences.
	for _, tc := range []struct {
		name string
		reg  *Registry
		want string
	}{
		{"duplicate provider", &Registry{Providers: []Provider{
			{Name: "acme", BaseURL: "https://a.example/v1"},
			{Name: "acme", BaseURL: "https://b.example/v1"},
		}}, `provider "acme" is defined twice`},
		{"duplicate model", &Registry{Providers: []Provider{{
			Name: "acme", BaseURL: "https://acme.example/v1",
			Models: []Model{{ID: "acme-fast"}, {ID: "acme-fast"}},
		}}}, `provider "acme": model "acme-fast" is listed twice`},
		{"duplicate vendor model", &Registry{Providers: []Provider{{
			Name: "nw", BaseURL: "https://api.example/v1",
			Models: []Model{{ID: "meta-llama/Llama-3-70B"}, {ID: "meta-llama/Llama-3-70B"}},
		}}}, `provider "nw": model "meta-llama/Llama-3-70B" is listed twice`},
	} {
		err := tc.reg.Validate()
		if err == nil {
			t.Fatalf("%s: expected a validation error", tc.name)
		}
		if err.Error() != tc.want {
			t.Errorf("%s: error drifted\n got: %q\nwant: %q", tc.name, err.Error(), tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// fix8-3 continuation: the wizard's registry-borne model-ID sinks.
//
// A model ID is a registry name (providers.json models[].id) and the wizard
// carries it in its own state (typed prompt, preset catalog, endpoint
// discovery). Eight ID sinks rendered through MaskOperand, whose underscore-head
// class (the planted underscore-head key shape) is the identity, so a hand-edited or
// discovered credential-shaped ID printed raw. All of them now route through
// wizardModelLabel (MaskSlot), the same registry-slot authority wizardProvLabel
// uses, plus the two preset-list renders (the row and the synthesized
// description).
// ---------------------------------------------------------------------------

// wizardModelIDBenignCorpus is the benign side: the model IDs the existing
// wizard pins use plus ordinary catalog and path-shaped names that are NOT
// location-shaped (a leading "./", a drive prefix, or a URL scheme would be
// rewritten by the registry-slot authority, and those are refused at ingest by
// the held location-shaped-ID decision).
var wizardModelIDBenignCorpus = []string{
	"acme-fast",
	"acme",
	"acme-slow",
	"custom-model-x",
	"gpt-4o",
	"deepseek-chat",
	"meta-llama/Llama-3.3-70B-Instruct-Turbo",
	"qwen2.5-coder:32b-instruct-q4_K_M",
	"llama3.2:latest",
	"gemini-3.8-flash",
}

// TestWizardModelLabelMasksRegistryBorneID pins the renderer: the underscore
// class (and the rest of the shared authority's refuse set) renders first-5 +
// ****, and every benign ID is byte-identical.
func TestWizardModelLabelMasksRegistryBorneID(t *testing.T) {
	for _, probe := range []string{
		testfixtures.SKLive,
		testfixtures.TokSample,
		testfixtures.PKLive,
		testfixtures.KeyHex,
		testfixtures.AKIAUnderscore,
	} {
		// Premise: the pre-fix renderer (MaskOperand) is the identity on the
		// underscore class, so each probe exercises exactly the authority switch.
		if MaskOperand(probe) != probe {
			t.Fatalf("premise: MaskOperand already masks %q", probe)
		}
		got := wizardModelLabel(probe)
		if got != MaskSlot(probe) {
			t.Fatalf("wizardModelLabel(%q) = %q; want the slot authority's %q", probe, got, MaskSlot(probe))
		}
		if got == probe || strings.Contains(got, probe) {
			t.Fatalf("wizardModelLabel(%q) = %q; the ID rendered raw", probe, got)
		}
	}
	for _, id := range wizardModelIDBenignCorpus {
		if got := wizardModelLabel(id); got != id {
			t.Fatalf("wizardModelLabel(%q) = %q; a benign ID must render byte-identically", id, got)
		}
	}
}

// TestWizardModelIDSitesRouteThroughTheSlotAuthority is the call-site pin: it
// fails if any of the eight ID sinks (or the two preset-list renders) is
// reverted to MaskOperand or deleted. The counts are exact on purpose, so a new
// sink must be added here too.
func TestWizardModelIDSitesRouteThroughTheSlotAuthority(t *testing.T) {
	src, err := os.ReadFile("wizard.go")
	if err != nil {
		t.Fatalf("read wizard.go: %v", err)
	}
	code := string(src)

	// No model-ID operand may render through the operand masker: it is the
	// identity on the underscore class this unit closes.
	operandID := regexp.MustCompile(`MaskOperand\(\s*[A-Za-z0-9_\[\]]*\.ID\s*\)`)
	if hits := operandID.FindAllString(code, -1); len(hits) != 0 {
		t.Fatalf("a model-ID sink still renders through MaskOperand: %q", hits)
	}

	// The eight ID sinks, by operand group.
	for _, want := range []struct {
		operand string
		count   int
	}{
		{"wizardModelLabel(selectedModel.ID)", 5}, // duplicate, probe, saved, discard x2
		{"wizardModelLabel(m.ID)", 1},             // summary row
		{"wizardModelLabel(models[0].ID)", 2},     // native agy -m, fanout --models
		{"wizardModelLabel(p.ID)", 1},             // preset list row
		{"wizardModelLabel(rm.ID)", 1},            // synthesized discovered description
	} {
		if got := strings.Count(code, want.operand); got != want.count {
			t.Fatalf("%s appears %d times; want %d", want.operand, got, want.count)
		}
	}
	if got := strings.Count(code, "wizardModelLabel("); got != 11 { // 10 call sites + the definition
		t.Fatalf("wizardModelLabel appears %d times; want 11 (10 call sites plus the definition)", got)
	}
}

// wizardDiscoveryServer serves a /models list (the discovery path) and refuses
// every completion request, so a selected model always reaches the probe-failure
// options with no network dependency.
func wizardDiscoveryServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i, id := range ids {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`{"id":"` + id + `"}`)
			}
			b.WriteString(`]}`)
			w.Write([]byte(b.String()))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wizardDiscoveryScript builds one scripted wizard run that discovers the
// endpoint's model list and then follows the given answer lines.
func wizardDiscoveryScript(baseURL string, lines ...string) string {
	return strings.Join(append([]string{"mycustomvendor", baseURL + "/v1", "mock-dummy-key-789"}, lines...), "\n") + "\n"
}

// TestInteractiveSetupWizard_TranscriptMasksSavedCredentialModelID drives the
// REAL wizard through the discovery path (the one path that can carry a
// credential-shaped ID past the prompts, since the prompt guard refuses a typed
// one) and pins the duplicate, live-probe, and save-anyway sinks: every render
// is masked, the transcript carries no raw value, and the pre-write sweep still
// refuses the record so nothing is written.
func TestInteractiveSetupWizard_TranscriptMasksSavedCredentialModelID(t *testing.T) {
	const plant = testfixtures.SKLive
	masked := MaskSlot(plant)
	if masked == plant {
		t.Fatalf("premise: MaskSlot is the identity on %q", plant)
	}
	srv := wizardDiscoveryServer(t, plant, "acme-fast")

	provPath := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(EnvProviders, provPath)
	t.Setenv("MYCUSTOMVENDOR_API_KEY", "")

	// select the plant, save it anyway after the failed probe, add another
	// model, select the plant again (duplicate notice), then leave the model
	// loop through the custom row with an empty ID.
	script := wizardDiscoveryScript(srv.URL, "1", "1", "y", "1", "c", "")
	out := &bytes.Buffer{}
	err := InteractiveSetupWizard(strings.NewReader(script), out)
	if err == nil {
		t.Fatalf("the wizard persisted a credential-shaped model ID:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "model ID") || !strings.Contains(err.Error(), "value hidden") {
		t.Fatalf("error = %q; want the non-echoing model ID refusal", err)
	}

	transcript := out.String()
	for _, want := range []string{
		`Model "` + masked + `" is already configured for mycustomvendor.`,
		`with model "` + masked + `"...`,
		`Saved model "` + masked + `" anyway.`,
		`1) ` + masked + ` (` + masked + ` (auto-discovered))`,
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript is missing the masked render %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, plant) || strings.Contains(err.Error(), plant) {
		t.Fatalf("the transcript echoed the credential-shaped model ID raw:\n%s", transcript)
	}
	if _, statErr := os.Stat(provPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused model ID must write nothing; stat error = %v", statErr)
	}
}

// TestInteractiveSetupWizard_TranscriptMasksDiscardedCredentialModelID pins the
// two discard sinks (the "4"/"d" case and the default branch) and the benign
// side of the same transcript: the plant is discarded, a benign discovered model
// is saved, the run reaches the summary, and the summary renders the benign ID
// byte-identically while no raw plant appears anywhere.
func TestInteractiveSetupWizard_TranscriptMasksDiscardedCredentialModelID(t *testing.T) {
	const plant = testfixtures.SKLive
	masked := MaskSlot(plant)
	if masked == plant {
		t.Fatalf("premise: MaskSlot is the identity on %q", plant)
	}
	for _, discardChoice := range []string{"4", "x"} {
		t.Run("discard "+discardChoice, func(t *testing.T) {
			srv := wizardDiscoveryServer(t, plant, "acme-fast")
			provPath := filepath.Join(t.TempDir(), "providers.json")
			t.Setenv(EnvProviders, provPath)
			t.Setenv("MYCUSTOMVENDOR_API_KEY", "")

			// select the plant, discard it after the failed probe, then select
			// the benign row, save it anyway, and decline another model.
			script := wizardDiscoveryScript(srv.URL, "1", discardChoice, "y", "2", "1", "n")
			out := &bytes.Buffer{}
			if err := InteractiveSetupWizard(strings.NewReader(script), out); err != nil {
				t.Fatalf("the benign run failed: %v\n%s", err, out.String())
			}

			transcript := out.String()
			if !strings.Contains(transcript, `Discarded model "`+masked+`".`) {
				t.Fatalf("transcript is missing the masked discard render:\n%s", transcript)
			}
			if strings.Contains(transcript, plant) {
				t.Fatalf("the transcript echoed the credential-shaped model ID raw:\n%s", transcript)
			}
			// The benign side of the same sinks is byte-identical: the saved
			// notice, the summary row, and the two ready-to-use commands.
			for _, want := range []string{
				`Saved model "acme-fast" anyway.`,
				"1. acme-fast",
				`agy -m "acme-fast (mycustomvendor)"`,
				`--models "mycustomvendor/acme-fast"`,
			} {
				if !strings.Contains(transcript, want) {
					t.Fatalf("transcript is missing the benign render %q:\n%s", want, transcript)
				}
			}
			reg, err := LoadFrom(provPath)
			if err != nil {
				t.Fatalf("the benign run did not save: %v", err)
			}
			if len(reg.Providers) != 1 || len(reg.Providers[0].Models) != 1 {
				t.Fatalf("unexpected saved registry: %+v", reg.Providers)
			}
			if got := reg.Providers[0].Models[0].ID; got != "acme-fast" {
				t.Fatalf("saved model ID = %q; want acme-fast", got)
			}
		})
	}
}

// TestWizardCapabilityDefaultsYTaken pins U3 (P2-4) on the wizard surface:
// accepting the defaults at the thinking, vision and video prompts saves all
// three flags true, and explicit "n" answers remain the manual override. Form
// parity lives in TestModelFormCapabilityDefaultsAddMode.
func TestWizardCapabilityDefaultsYTaken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer server.Close()

	run := func(name, modelID string, answers []string) Model {
		t.Helper()
		provPath := filepath.Join(t.TempDir(), "providers.json")
		t.Setenv(EnvProviders, provPath)
		lines := []string{
			name, server.URL + "/v1", name + "_API_KEY", "", modelID,
			"", // family
			"", // context length
			"", // max output tokens
		}
		lines = append(lines, answers...)
		lines = append(lines, "N") // add another: N
		out := &bytes.Buffer{}
		if err := InteractiveSetupWizard(strings.NewReader(strings.Join(lines, "\n")+"\n"), out); err != nil {
			t.Fatalf("InteractiveSetupWizard returned error: %v\nOutput: %s", err, out.String())
		}
		reg, err := LoadFrom(provPath)
		if err != nil {
			t.Fatalf("failed to load saved providers: %v", err)
		}
		return reg.Providers[0].Models[0]
	}

	// Empty answers accept the bracketed defaults at every capability prompt.
	m := run("defprov", "custom-def-model", []string{"n", "", "", "", ""})
	// tools n, thinking "" -> Y, effort "" -> default, vision "" -> Y, video "" -> Y
	if !m.Capabilities.SupportsThinking {
		t.Error("SupportsThinking = false; want true (Y default)")
	}
	if m.Capabilities.ReasoningEffort != "default" {
		t.Errorf("reasoningEffort = %q; want \"default\"", m.Capabilities.ReasoningEffort)
	}
	if !m.Capabilities.SupportsVision {
		t.Error("SupportsVision = false; want true (Y default)")
	}
	if !m.Capabilities.SupportsVideo {
		t.Error("SupportsVideo = false; want true (Y default)")
	}
	if !m.HasTag("reasoning") || !m.HasTag("vision") {
		t.Errorf("expected reasoning+vision tags, got %v", m.Tags)
	}

	// Explicit "n" at every capability prompt overrides the Y default.
	m = run("novprov", "custom-nov-model", []string{"n", "n", "n", "n"})
	if m.Capabilities.SupportsThinking || m.Capabilities.SupportsVision || m.Capabilities.SupportsVideo {
		t.Errorf("manual n override lost: %+v", m.Capabilities)
	}
}
