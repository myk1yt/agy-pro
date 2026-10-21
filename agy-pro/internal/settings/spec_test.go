package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// FIX-8d / FIX-8g: the add-json parser is the ONE gate both add-json surfaces
// (the CLI verb and the TUI settings row) call, so the pins below cover both.

// specJSONWithURL renders a minimal provider spec carrying one base URL and an
// empty model list. Synthetic values only.
func specJSONWithURL(rawURL string) []byte {
	b, _ := json.Marshal(ProviderSpecJSON{Name: "acme", BaseURL: rawURL, APIKeyEnv: "ACME_API_KEY"})
	return b
}

// TestParseProviderSpecJSON_RefusesCredentialBaseURL is the FIX-8d pin. The
// add-json door gated the provider NAME with the shared authority but checked the
// base URL for non-emptiness only, while the write path enforces transport only,
// so a credential-bearing base URL that the TUI form and the wizard refuse this
// round was accepted and persisted raw here (and its path sub-case rendered raw
// in the shareable summary.json / --json surfaces). Each row is refused with the
// named sentinel, the sentence is value-free, and the temp registry keeps its
// exact bytes.
func TestParseProviderSpecJSON_RefusesCredentialBaseURL(t *testing.T) {
	const (
		underscore = testfixtures.SKLive
		tokHead    = testfixtures.TokSample
	)
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"host underscore head", "https://" + underscore + "/v1"},
		{"host tok head", "https://" + tokHead + "/v1"},
		{"host ghp head", "https://" + testfixtures.GHPShort + ".example.com/v1"},
		{"host aws key id", "https://" + testfixtures.AKIAExample + "/v1"},
		{"path underscore head", "https://host/v1/" + underscore},
		{"path 16 run", "https://host/v1/0123456789abcdef"},
		{"query value", "https://host/v1?api_key=" + underscore},
		{"query key", "https://host/v1?" + underscore + "=1"},
		{"fragment", "https://host/v1#" + underscore},
		{"userinfo username", "https://" + underscore + "@host/v1"},
		{"userinfo password", "https://user:" + underscore + "@host/v1"},
		{"opaque path", "https:" + underscore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Premise: the field arm is what refuses the row (the parser's older
			// gates accepted it).
			if !providers.BaseURLCarriesCredential(tc.url) {
				t.Fatalf("premise: the shared URL authority does not refuse %q", tc.url)
			}

			home := t.TempDir()
			t.Setenv(providers.EnvHome, home)
			path := filepath.Join(home, "providers.json")
			t.Setenv(providers.EnvProviders, path)
			seed := []byte(`{"providers":[{"name":"seed","baseURL":"https://seed.example/v1"}]}` + "\n")
			if err := os.WriteFile(path, seed, 0o600); err != nil {
				t.Fatalf("seed registry: %v", err)
			}

			_, err := ParseProviderSpecJSON(specJSONWithURL(tc.url))
			if !errors.Is(err, ErrProviderSpecBaseURLCarriesCredential) {
				t.Fatalf("ParseProviderSpecJSON(%q) err = %v; want ErrProviderSpecBaseURLCarriesCredential", tc.url, err)
			}
			if strings.Contains(err.Error(), underscore) || strings.Contains(err.Error(), tokHead) ||
				strings.Contains(err.Error(), "0123456789abcdef") || strings.Contains(err.Error(), testfixtures.AKIAExample) {
				t.Fatalf("the refusal echoes the refused value: %q", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read registry: %v", readErr)
			}
			if !bytes.Equal(got, seed) {
				t.Fatalf("the refused spec reached providers.json:\nbefore: %s\nafter:  %s", seed, got)
			}
		})
	}
}

// TestParseProviderSpecJSON_BenignBaseURLsUnchanged is the accept side of
// FIX-8d: every benign URL the other two doors accept still parses, and the
// spec it returns is byte-identical to the input values (no trimming surprise,
// no rewrite). The benign userinfo row is the FIX-8h alignment: the form used to
// refuse it, and the CLI accepted it, so the parser's verdict is now the shared
// one.
func TestParseProviderSpecJSON_BenignBaseURLsUnchanged(t *testing.T) {
	for _, rawURL := range []string{
		"https://api.openai.com/v1",
		"https://gw.example/v1/chat/completions",
		"https://user:pw@gw.example/v1",
		"https://user@api.example.com/v1",
		"https://gateway.ai.cloudflare.com/v1/0123456789abcdef0123456789abcdef/anthropic",
		"https://mycompany2024gateway.example.com/v1",
		"https://acmeprod2024gateway.example.com/v1",
		"https://gw.example/v1?api-version=2024-02-01",
		"https://gw.example/v1#section-2",
		"https:",
	} {
		t.Run(rawURL, func(t *testing.T) {
			spec, err := ParseProviderSpecJSON(specJSONWithURL(rawURL))
			if err != nil {
				t.Fatalf("ParseProviderSpecJSON(%q) = %v; want accepted", rawURL, err)
			}
			if spec.Name != "acme" || spec.BaseURL != rawURL || spec.APIKeyEnv != "ACME_API_KEY" || len(spec.Models) != 0 {
				t.Fatalf("parsed spec = %+v; want the input values verbatim", spec)
			}
			if rendered := RenderSpec(spec); !strings.Contains(rendered, rawURL) {
				t.Fatalf("RenderSpec = %q; want the base URL verbatim", rendered)
			}
		})
	}
}

// TestParseProviderSpecJSON_RefusesCredentialModels is the FIX-8g pin: the
// Models[] array was not classified at all, so a credential-shaped model
// id/family/tag/tool/effort persisted raw from this door alone, diverging from
// model-add-json, model-update, and the TUI model form (which all classify the
// same field set). Each row is refused with the named sentinel and the sentence
// never names the value.
func TestParseProviderSpecJSON_RefusesCredentialModels(t *testing.T) {
	const probe = testfixtures.TokSample
	cases := []struct {
		name  string
		model providers.Model
	}{
		{"model ID", providers.Model{ID: probe}},
		{"model family", providers.Model{ID: "acme-fast", Family: probe}},
		{"model tag", providers.Model{ID: "acme-fast", Tags: []string{"general", probe}}},
		{"model tool", providers.Model{ID: "acme-fast", Capabilities: providers.Capabilities{AllowedTools: []string{probe}}}},
		{"reasoning effort", providers.Model{ID: "acme-fast", Capabilities: providers.Capabilities{ReasoningEffort: probe}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Premise: the shared NAME authority refuses the field while the old
			// parser (which classified nothing) accepted it.
			if !providers.NameCarriesCredential(probe) {
				t.Fatalf("premise: the shared authority does not refuse %q", probe)
			}
			data, err := json.Marshal(ProviderSpecJSON{
				Name: "acme", BaseURL: "https://gw.example/v1", APIKeyEnv: "ACME_API_KEY",
				Models: []providers.Model{tc.model},
			})
			if err != nil {
				t.Fatalf("marshal spec: %v", err)
			}
			_, parseErr := ParseProviderSpecJSON(data)
			if !errors.Is(parseErr, ErrProviderSpecModelCarriesCredential) {
				t.Fatalf("ParseProviderSpecJSON(%s) err = %v; want ErrProviderSpecModelCarriesCredential", tc.name, parseErr)
			}
			if strings.Contains(parseErr.Error(), probe) {
				t.Fatalf("the refusal echoes the refused value: %q", parseErr)
			}
		})
	}
}

// TestParseProviderSpecJSON_BenignModelsUnchanged is the accept side of FIX-8g:
// a benign model array (realistic ids, families, tags, tools, and the closed-set
// effort) parses unchanged, so the gate cannot become an over-refusal of an
// ordinary provider spec.
func TestParseProviderSpecJSON_BenignModelsUnchanged(t *testing.T) {
	want := []providers.Model{
		{
			ID:     "meta-llama/Llama-3.3-70B-Instruct-Turbo",
			Family: "llama",
			Tags:   []string{"general", "tools", "reasoning"},
			Capabilities: providers.Capabilities{
				SupportsTools:   true,
				AllowedTools:    []string{"read_file", "list_dir"},
				ReasoningEffort: "high",
			},
		},
		{ID: "qwen2.5-coder:32b-instruct-q4_K_M", Family: "qwen", Tags: []string{"general"}},
		{ID: "arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-5-sonnet-20241022-v2:0", Family: "claude"},
	}
	data, err := json.Marshal(ProviderSpecJSON{
		Name: "acme", BaseURL: "https://gateway.ai.cloudflare.com/v1/0123456789abcdef0123456789abcdef/anthropic",
		APIKeyEnv: "ACME_API_KEY", Models: want,
	})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	spec, err := ParseProviderSpecJSON(data)
	if err != nil {
		t.Fatalf("ParseProviderSpecJSON = %v; want accepted", err)
	}
	if !reflect.DeepEqual(spec.Models, want) {
		t.Fatalf("parsed models = %#v; want the input records verbatim", spec.Models)
	}
	if rendered := RenderSpec(spec); !strings.Contains(rendered, "meta-llama/Llama-3.3-70B-Instruct-Turbo") ||
		!strings.Contains(rendered, "reasoningEffort") {
		t.Fatalf("RenderSpec = %q; want the model array rendered verbatim", rendered)
	}
}
