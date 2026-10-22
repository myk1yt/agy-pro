package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
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

// RemoteModel is one entry of a provider's GET /models data array. ID is the
// model id. ContextLength and MaxOutputTokens are the per-model ceilings the
// endpoint advertises under any of the known alias names, or 0 when the field
// is absent or carries an unusable value: 0 is never a real token ceiling, so
// it reads unambiguously as "not advertised". Callers that persist a ceiling
// map 0 to their own "unknown" representation (the model form maps it to the
// server-maximum sentinel), so a provider whose /models omits ceilings keeps
// behaving exactly as it did before this type existed.
type RemoteModel struct {
	ID              string
	ContextLength   int
	MaxOutputTokens int
}

// Failure sentinels for FetchRemoteModels. Every silent failure of the old
// ids-only contract is now one distinguishable error, so a caller can surface
// WHICH step failed instead of guessing from an empty list. Match with
// errors.Is; the wrapped detail is sanitized (host and/or status code only,
// never the URL path or key material).
var (
	ErrModelsNoBaseURL = errors.New("models fetch: empty base URL")
	ErrModelsRequest   = errors.New("models fetch: request could not be built")
	ErrModelsConnect   = errors.New("models fetch: endpoint unreachable")
	ErrModelsStatus    = errors.New("models fetch: unexpected HTTP status")
	ErrModelsDecode    = errors.New("models fetch: response could not be decoded")
)

// maxSaneTokens is the sanity ceiling for an advertised token limit. The
// largest value observed live (T-B probe, 2026-10-22) is 1,048,576 (Gemini 1M
// context on both nw and Charm); 10,000,000 keeps ~10x headroom above that
// while rejecting absurd or misparsed values (a 64-bit overflow, a stringly
// "999999999999", a metadata blob misread as a count). Non-positive values are
// rejected separately: 0 doubles as "not advertised" in RemoteModel, and a
// negative ceiling is a broken payload, not a limit.
const maxSaneTokens = 10_000_000

// contextCeilingAliases and outputCeilingAliases are the field names live
// /models payloads use for the two token ceilings, most-preferred first: the
// first alias that parses to a sane value wins. Names are NOT standardized
// across providers (the T-B probe verified both shapes below):
//
//	context_window:      Charm (live-verified), several aggregators
//	max_model_len:       vLLM (nw, live-verified)
//	context_length:      OpenRouter convention
//	context_size:        generic gateway naming
//	max_context_tokens:  gateway-style variant of context_window
//	max_input_tokens:    OpenAI-style naming used by aggregators
//	max_output_tokens:   Charm (live-verified), OpenAI convention
//	max_tokens:          legacy OpenAI
//	max_completion_tokens: Anthropic / OpenAI responses naming
var contextCeilingAliases = []string{
	"context_window",
	"max_model_len",
	"context_length",
	"context_size",
	"max_context_tokens",
	"max_input_tokens",
}

var outputCeilingAliases = []string{
	"max_output_tokens",
	"max_tokens",
	"max_completion_tokens",
}

// saneTokenCount coerces one decoded JSON value into a usable token count.
// Live payloads carry ceilings as bare numbers; a quoted-number string is
// accepted too (some gateways serialize every scalar as a string). Bools,
// nulls, junk strings, fractions and out-of-range values are rejected.
func saneTokenCount(v any) (int, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f <= 0 || f > maxSaneTokens || f != float64(int64(f)) {
		return 0, false
	}
	return int(f), true
}

// firstCeiling returns the first alias that yields a sane count, else 0.
func firstCeiling(m map[string]any, aliases []string) int {
	for _, name := range aliases {
		if v, ok := m[name]; ok {
			if n, sane := saneTokenCount(v); sane {
				return n
			}
		}
	}
	return 0
}

// modelsFetchHost extracts the host for sanitized error detail. The full URL
// is never echoed: it can carry the /v1 prefix the user typed and, in a
// hand-edited record, anything else.
func modelsFetchHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "provider"
	}
	return u.Host
}

// stripModelIDControls deletes every control (Cc) and format (Cf) rune from a
// remote model id at the decode boundary, before the id can reach any caller.
// The id is REMOTE RESPONSE MATERIAL: a hostile or broken endpoint can answer
// `{"id":"\x1b[2JFAKE-MODEL"}`, TrimSpace keeps the ESC (it is not a space),
// and every downstream sink that does not route through a masking authority
// would paint the rune sequence into the terminal frame. Stripping here rather
// than at each sink closes the class once for both consumers (the TUI picker
// and the non-console wizard), including the persisted form: nothing this
// function touches can be written into providers.json either.
//
// The predicate is the full Cc plus Cf class: unicode.IsControl for Cc (the C0
// controls 0x00..0x1f, DEL 0x7f, and the C1 controls 0x80..0x9f) and
// unicode.Cf for the format runes (the bidi embeddings and overrides
// U+202A..U+202E, the isolates U+2066..U+2069, ZERO WIDTH SPACE/JOINER
// U+200B..U+200D, SOFT HYPHEN U+00AD, BOM U+FEFF). The earlier C0+DEL-only
// predicate was too narrow for this boundary: the final painter sink
// (internal/tui/render.go, runeSurvivesStrip) already drops Cc plus Cf, so a
// frame never carried one, but the model-id EDIT BUFFER did. A pasted or
// endpoint-planted RIGHT-TO-LEFT OVERRIDE survived the decode, sat in the
// editor, and could reorder the row it was typed into. Widening this predicate
// closes that edit-buffer residual only; no other strip site changes.
//
// Zl/Zp (U+2028 LINE SEPARATOR, U+2029 PARAGRAPH SEPARATOR) are NOT Cf and
// survive here: they end a terminal line rather than format one, and they are
// the separator class other call sites already reason about explicitly. The
// caller's surrounding TrimSpace still trims them at the edges, exactly as it
// trims every other space-class rune, so an interior separator is the only
// position that can reach a sink. A benign id is byte-identical.
func stripModelIDControls(id string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, id)
}

// FetchRemoteModels queries <baseURL>/models to discover available models.
// If the endpoint is unreachable, answers non-200, or returns an unreadable
// body, it returns one of the ErrModels* sentinels with sanitized detail
// (the old ids-only contract swallowed every one of those into a bare nil).
// Each returned RemoteModel carries whatever ceilings the payload advertised
// under a known alias, 0 for "not advertised".
func FetchRemoteModels(baseURL, apiKey string) ([]RemoteModel, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, ErrModelsNoBaseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w (base URL rejected)", ErrModelsRequest)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrModelsConnect, modelsFetchHost(baseURL))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (%d from %s)", ErrModelsStatus, resp.StatusCode, modelsFetchHost(baseURL))
	}

	// The decode contract stays ids-first: only the data array is read (a
	// top-level models array stays ignored, as before). Each entry is decoded
	// into a generic map so the alias lookup can see every ceiling name the
	// payload happens to carry without pinning one provider's schema. The id is
	// stripped of control runes BEFORE the trim, so a control-padded id
	// ("\x1b abc") cannot keep its outer whitespace: the strip runs first, the
	// trim then sees the real edges.
	var res struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrModelsDecode, modelsFetchHost(baseURL))
	}

	var models []RemoteModel
	for _, m := range res.Data {
		id, _ := m["id"].(string)
		id = strings.TrimSpace(stripModelIDControls(id))
		if id == "" {
			continue
		}
		models = append(models, RemoteModel{
			ID:              id,
			ContextLength:   firstCeiling(m, contextCeilingAliases),
			MaxOutputTokens: firstCeiling(m, outputCeilingAliases),
		})
	}
	return models, nil
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
