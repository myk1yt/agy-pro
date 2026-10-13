package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ModelPreset represents a recommended model configuration for a provider.
type ModelPreset struct {
	ID              string       `json:"id"`
	Family          string       `json:"family,omitempty"`
	Description     string       `json:"description,omitempty"`
	Recommended     bool         `json:"recommended,omitempty"`
	ContextLength   int          `json:"contextLength,omitempty"`
	MaxOutputTokens int          `json:"maxOutputTokens,omitempty"`
	Tags            []string     `json:"tags,omitempty"`
	Capabilities    Capabilities `json:"capabilities,omitempty"`
}

// GoogleAIStudioBaseURL is the Google AI Studio OpenAI-compatibility endpoint.
// The gateway bridge appends "/chat/completions" and "/models" to it.
const GoogleAIStudioBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"

// GoogleAIStudioKeyEnv is the environment variable holding a Google AI Studio
// (Gemini API) key.
const GoogleAIStudioKeyEnv = "GEMINI_API_KEY"

// ProviderHint describes a well-known OpenAI-compatible provider: the canonical
// name stored in providers.json, its OpenAI-compatible base URL, the API key
// variable, and the names accepted as aliases at the setup prompt. It is the
// single source of truth for the setup wizard, the model presets, and any
// interactive provider menu built on top of this package.
type ProviderHint struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	BaseURL string   `json:"baseURL"`
	KeyEnv  string   `json:"keyEnv"`
	Aliases []string `json:"aliases,omitempty"`
	KeyURL  string   `json:"keyURL,omitempty"`
}

var knownProviders = []ProviderHint{
	{Name: "deepseek", Label: "deepseek", BaseURL: "https://api.deepseek.com/v1", KeyEnv: "DEEPSEEK_API_KEY", KeyURL: "https://platform.deepseek.com/api_keys"},
	{Name: "openrouter", Label: "openrouter", BaseURL: "https://openrouter.ai/api/v1", KeyEnv: "OPENROUTER_API_KEY", KeyURL: "https://openrouter.ai/keys"},
	{Name: "groq", Label: "groq", BaseURL: "https://api.groq.com/openai/v1", KeyEnv: "GROQ_API_KEY", KeyURL: "https://console.groq.com/keys"},
	{Name: "openai", Label: "openai", BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY", KeyURL: "https://platform.openai.com/api-keys"},
	{Name: "bigmodel", Label: "bigmodel (Zhipu GLM)", BaseURL: "https://open.bigmodel.cn/api/paas/v4", KeyEnv: "BIGMODEL_API_KEY", Aliases: []string{"glm", "zhipu"}},
	{Name: "google", Label: "google (Google AI Studio)", BaseURL: GoogleAIStudioBaseURL, KeyEnv: GoogleAIStudioKeyEnv, Aliases: []string{"gemini", "google ai studio", "aistudio"}, KeyURL: "https://aistudio.google.com/apikey"},
	{Name: "nw", Label: "nw (NeuralWatt)", BaseURL: "https://api.neuralwatt.com/v1", KeyEnv: "NW_API_KEY", Aliases: []string{"neuralwatt"}},
	{Name: "together", Label: "together", BaseURL: "https://api.together.xyz/v1", KeyEnv: "TOGETHER_API_KEY"},
	{Name: "mistral", Label: "mistral", BaseURL: "https://api.mistral.ai/v1", KeyEnv: "MISTRAL_API_KEY", KeyURL: "https://console.mistral.ai/api-keys"},
}

// KnownProviders returns the well-known provider catalog in menu order. The
// returned slice and its Alias lists are copies, so callers may modify them.
func KnownProviders() []ProviderHint {
	out := make([]ProviderHint, len(knownProviders))
	copy(out, knownProviders)
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
	}
	return out
}

// providerAliasKey folds a provider name for alias matching: lowercase, with
// spaces, underscores, hyphens, and dots removed.
func providerAliasKey(name string) string {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "\ufeff")
	return strings.NewReplacer(" ", "", "_", "", "-", "", ".", "").Replace(norm)
}

// CanonicalProviderName resolves a provider name typed at the prompt to the
// canonical catalog name ("gemini", "google ai studio" -> "google"). Names that
// are not in the catalog are returned lowercased and trimmed.
func CanonicalProviderName(name string) string {
	key := providerAliasKey(name)
	for _, h := range knownProviders {
		if key == providerAliasKey(h.Name) {
			return h.Name
		}
		for _, alias := range h.Aliases {
			if key == providerAliasKey(alias) {
				return h.Name
			}
		}
	}
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "\ufeff")
}

// LookupProviderHint returns the catalog entry for a provider name or alias.
func LookupProviderHint(name string) (ProviderHint, bool) {
	canonical := CanonicalProviderName(name)
	for _, h := range knownProviders {
		if h.Name == canonical {
			return h, true
		}
	}
	return ProviderHint{}, false
}

// GetModelPresets returns curated popular models for well-known providers.
func GetModelPresets(providerName string) []ModelPreset {
	switch CanonicalProviderName(providerName) {
	case "deepseek":
		return []ModelPreset{
			{
				ID:              "deepseek-chat",
				Family:          "deepseek",
				Description:     "DeepSeek-V3 (128k context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-reasoner",
				Family:          "deepseek",
				Description:     "DeepSeek-R1 (128k context, server max output, reasoning/thinking)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning", "coding", "math"},
				Capabilities: Capabilities{
					SupportsThinking: true,
					ReasoningEffort:  "high",
				},
			},
		}

	case "openrouter":
		return []ModelPreset{
			{
				ID:              "anthropic/claude-3.7-sonnet",
				Family:          "claude",
				Description:     "Claude 3.7 Sonnet (200k context, server max output, hybrid thinking & tools)",
				Recommended:     true,
				ContextLength:   200000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools", "coding", "reasoning"},
				Capabilities: Capabilities{
					SupportsTools:    true,
					SupportsThinking: true,
					ReasoningEffort:  "high",
					AllowedTools:     []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek/deepseek-r1",
				Family:          "deepseek",
				Description:     "DeepSeek R1 (128k context, server max output, reasoning/thinking)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning", "coding"},
				Capabilities: Capabilities{
					SupportsThinking: true,
					ReasoningEffort:  "high",
				},
			},
			{
				ID:              "meta-llama/llama-3.3-70b-instruct",
				Family:          "llama",
				Description:     "Llama 3.3 70B Instruct (128k context, server max output, tools enabled)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"tools", "open-weights"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "openai/gpt-4o",
				Family:          "gpt",
				Description:     "GPT-4o (128k context, server max output, multimodal & tools)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "vision", "tools"},
				Capabilities: Capabilities{
					SupportsTools:  true,
					SupportsVision: true,
					AllowedTools:   []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	case "groq":
		return []ModelPreset{
			{
				ID:              "llama-3.3-70b-versatile",
				Family:          "llama",
				Description:     "Llama 3.3 70B Versatile (Ultra-fast, 128k context, server max output, tools)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-r1-distill-llama-70b",
				Family:          "deepseek",
				Description:     "DeepSeek R1 Distill Llama 70B (Fast reasoning, 128k context, server max output)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "reasoning"},
				Capabilities: Capabilities{
					SupportsThinking: true,
					ReasoningEffort:  "high",
				},
			},
		}

	case "openai":
		return []ModelPreset{
			{
				ID:              "gpt-4o",
				Family:          "gpt",
				Description:     "GPT-4o (128k context, server max output, flagship multimodal & tools)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "vision", "tools"},
				Capabilities: Capabilities{
					SupportsTools:  true,
					SupportsVision: true,
					AllowedTools:   []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "gpt-4o-mini",
				Family:          "gpt",
				Description:     "GPT-4o Mini (128k context, server max output, lightweight, tools enabled)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "o3-mini",
				Family:          "o-series",
				Description:     "o3-mini (200k context, server max output, reasoning/STEM)",
				Recommended:     false,
				ContextLength:   200000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning", "coding"},
				Capabilities: Capabilities{
					SupportsThinking: true,
					ReasoningEffort:  "high",
				},
			},
		}

	case "nw":
		return []ModelPreset{
			{
				ID:              "nw-flash",
				Family:          "nw",
				Description:     "NW Flash (High-throughput, 1M context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-v4-flash",
				Family:          "deepseek",
				Description:     "DeepSeek V4 Flash (1M context, server max output, fast inference)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "glm-5.3-flash",
				Family:          "glm",
				Description:     "GLM 5.3 Flash (1M context, server max output, tools enabled)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "qwen-3.8-27b",
				Family:          "qwen",
				Description:     "Qwen 3.8 27B (1M context, server max output, tools enabled)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	case "together":
		return []ModelPreset{
			{
				ID:              "meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
				Family:          "llama",
				Description:     "Llama 3.1 70B Turbo (128k context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-ai/DeepSeek-R1",
				Family:          "deepseek",
				Description:     "DeepSeek R1 (128k context, server max output, reasoning enabled)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning"},
				Capabilities: Capabilities{
					SupportsThinking: true,
					ReasoningEffort:  "high",
				},
			},
		}

	case "mistral":
		return []ModelPreset{
			{
				ID:              "mistral-large-latest",
				Family:          "mistral",
				Description:     "Mistral Large Latest (128k context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "codestral-latest",
				Family:          "mistral",
				Description:     "Codestral Latest (256k context, server max output, specialized coding)",
				Recommended:     false,
				ContextLength:   256000,
				MaxOutputTokens: -1,
				Tags:            []string{"coding", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}
	// bigmodel.cn (Zhipu GLM): specs verified against docs.bigmodel.cn
	// (model-overview table, 2026-01): GLM-4.6 = 200K context, advanced
	// coding/reasoning/tool-calling; GLM-4.5-Flash = free tier, 128K context,
	// thinking mode + tool calling. GLM's hybrid thinking is toggled per call
	// (no effort dial), so ReasoningEffort stays unset like the google presets.
	case "bigmodel":
		return []ModelPreset{
			{
				ID:              "glm-4.6",
				Family:          "glm",
				Description:     "GLM-4.6 (200k context, advanced coding, reasoning & tool calling)",
				Recommended:     true,
				ContextLength:   200000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools", "coding", "reasoning"},
				Capabilities: Capabilities{
					SupportsTools:    true,
					SupportsThinking: true,
					AllowedTools:     []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "glm-4.5-flash",
				Family:          "glm",
				Description:     "GLM-4.5-Flash (free tier, 128k context, thinking & tool calling)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"free", "fast", "tools", "reasoning"},
				Capabilities: Capabilities{
					SupportsTools:    true,
					SupportsThinking: true,
					AllowedTools:     []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	// Gemini 2.5 models think by default, so ReasoningEffort is left unset: the
	// gateway then omits reasoning_effort and Google applies its own budget.
	case "google":
		return []ModelPreset{
			{
				ID:              "gemini-2.5-flash",
				Family:          "gemini",
				Description:     "Gemini 2.5 Flash (1M context, Google AI Studio OpenAI endpoint, vision & thinking)",
				Recommended:     true,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools", "vision", "reasoning"},
				Capabilities: Capabilities{
					SupportsTools:    true,
					SupportsVision:   true,
					SupportsThinking: true,
					AllowedTools:     []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "gemini-2.5-pro",
				Family:          "gemini",
				Description:     "Gemini 2.5 Pro (1M context, Google AI Studio OpenAI endpoint, vision & thinking)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools", "vision", "reasoning"},
				Capabilities: Capabilities{
					SupportsTools:    true,
					SupportsVision:   true,
					SupportsThinking: true,
					AllowedTools:     []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	default:
		return nil
	}
}

// FetchRemoteModels queries <baseURL>/models to discover available models.
// If the endpoint is unreachable or does not support /models, returns an empty list.
func FetchRemoteModels(baseURL, apiKey string) []string {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var res struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil
	}

	var models []string
	for _, m := range res.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	return models
}

// credentialShapeRe matches well-known credential formats that are short
// enough to slip past the length heuristic yet are never environment-variable
// names (B4): AWS access key IDs (AKIA/ASIA + 16), GitHub tokens, Slack
// tokens, Google API/OAuth tokens, GitLab/NPM/Docker tokens, and JWTs. These
// closed shapes catch the AKIA-style apiKeyEnv bypass without flagging real
// env names such as MY_CUSTOM_VAR.
var credentialShapeRe = regexp.MustCompile(
	`^(?:AKIA|ASIA)[0-9A-Z]{16}$` +
		`|^gh[pousr]_[A-Za-z0-9]{16,}$` +
		`|^github_pat_[A-Za-z0-9_]{20,}$` +
		`|^xox[baprs]-[A-Za-z0-9-]{10,}$` +
		`|^AIza[0-9A-Za-z_-]{35}$` +
		`|^ya29\.[0-9A-Za-z_-]{20,}$` +
		`|^sk-ant-[A-Za-z0-9_-]{10,}$` +
		`|^glpat-[A-Za-z0-9_-]{16,}$` +
		`|^npm_[A-Za-z0-9]{30,}$` +
		`|^dckr_pat-[A-Za-z0-9_-]{20,}$` +
		`|^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.`)

// isLikelyAPIKey returns true if the input resembles an API key/secret token
// rather than an environment variable name.
func isLikelyAPIKey(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return false
	}
	if credentialShapeRe.MatchString(val) {
		return true
	}
	lower := strings.ToLower(val)
	for _, p := range []string{"sk-", "key-", "gsk_", "nvapi-", "live_", "secret_"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	// Environment variable names never have hyphens or dots, and are usually uppercase.
	if strings.Contains(val, "-") || strings.Contains(val, ".") {
		return true
	}
	// Very long strings (>= 24 chars) are almost always keys.
	if len(val) >= 24 {
		return true
	}
	return false
}

// persistWindowsUserEnv sets a persistent user environment variable on
// Windows through the advapi32 registry API (win32reg_windows.go). It used to
// launch powershell.exe with the name and value base64-encoded on the argv,
// which exposed every saved key to process-list and command-line telemetry
// (security finding S-2); the registry API passes both as UTF-16 pointers.
func persistWindowsUserEnv(key, value string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if store := defaultEnvStore; store != nil {
		return store.Set(EnvRootUser, key, value)
	}
	return (&windowsEnvStore{}).Set(EnvRootUser, key, value)
}
