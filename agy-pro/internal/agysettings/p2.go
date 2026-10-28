package agysettings

import (
	"fmt"
	"regexp"
	"strings"

	"agypro/internal/providers"
)

// ModelProviderGemini is the modelProvider value P2 sessions need. It is
// mandatory for custom models: env vars alone do not enable them
// (custommodels-probe-report.md §4 variant A2).
const ModelProviderGemini = "gemini"

// staticGeminiIDs are the 11 static Gemini defaults agy lists in gemini mode.
// A customModels entry that resolves to one of them is ambiguous: agy would
// answer the static default, not the external entry, and the user could not
// tell which one ran. Proven live in surface-verification-report.md §6 Q-E.
var staticGeminiIDs = []string{
	"gemini-3.8-flash-high",
	"gemini-3.8-flash-medium",
	"gemini-3.8-flash-low",
	"gemini-3.7-flash-high",
	"gemini-3.7-flash-medium",
	"gemini-3.7-flash-low",
	"gemini-3.6-flash-high",
	"gemini-3.6-flash-medium",
	"gemini-3.6-flash-low",
	"gemini-3.1-pro-high",
	"gemini-3.1-pro-low",
}

// customModelKeyRegex is agy's v1.3.5 model-id scheme regex, verbatim
// (binary-surface-report.md §1.2). A key that does not match is a bare
// modelName, which agy rejects with "model name %q does not look like a custom
// model URL". Group 1 is the model name of the "/models/<name>" form, group 2
// the trailing-segment form; exactly one is non-empty for a valid key.
var customModelKeyRegex = regexp.MustCompile(`^(?:gemini-api:|blade:|evergreen:|beyond-api:)(?:.*?/models/(.+)|(?:.*/)?([^/]+))$`)

// StaticGeminiIDs returns the 11 static Gemini default ids. The mesh and the
// launcher share this deny-list through this accessor rather than a second copy.
func StaticGeminiIDs() []string { return append([]string(nil), staticGeminiIDs...) }

// ReservedModelNames returns every name a P2 entry may not resolve to: the 11
// static Gemini defaults plus the Google AI-Studio provider preset names. The
// preset half is derived from the provider catalog (canonical name plus
// aliases) so it tracks the catalog instead of drifting from it.
func ReservedModelNames() []string {
	out := append([]string(nil), staticGeminiIDs...)
	out = append(out, aiStudioPresetNames()...)
	return out
}

// aiStudioPresetNames returns the canonical name and aliases of the Google
// AI-Studio provider preset ("google", "gemini", "google ai studio", "aistudio").
func aiStudioPresetNames() []string {
	for _, hint := range providers.KnownProviders() {
		if hint.KeyEnv != providers.GoogleAIStudioKeyEnv {
			continue
		}
		out := []string{hint.Name}
		out = append(out, hint.Aliases...)
		return out
	}
	return nil
}

// ExternalEntry is one external model to register for a P2 session. URL is the
// customModels map key and the --model selector; the other fields become the
// CustomModelConfig value.
type ExternalEntry struct {
	URL           string
	ModelName     string
	DisplayName   string
	ContextWindow int
	PlanTier      string
	ModelFeatures *ModelFeatures
}

// config builds the CustomModelConfig for the entry.
func (e ExternalEntry) config() CustomModelConfig {
	return CustomModelConfig{
		ModelName:     strings.TrimSpace(e.ModelName),
		DisplayName:   e.DisplayName,
		ContextWindow: e.ContextWindow,
		PlanTier:      strings.TrimSpace(e.PlanTier),
		ModelFeatures: e.ModelFeatures,
	}
}

// BuildP2Settings composes the settings document for a P2 session:
// modelProvider:"gemini" plus the managed customModels map keyed by each
// entry's URL form.
//
// Every entry is validated before anything is returned: the key must look like
// a custom model URL (a bare modelName is rejected by agy), modelName is
// required, and neither the key, the name agy derives from it, nor modelName
// may resolve to a static Gemini default or to an AI-Studio preset name. A
// collision is a hard error naming the collision, never a silent overwrite of a
// static id.
//
// The returned map carries only ledger keys, so it can be handed straight to
// File.Set.
func BuildP2Settings(externals []ExternalEntry) (map[string]any, error) {
	if err := assertLedgerComplete(); err != nil {
		return nil, err
	}

	models := make(map[string]CustomModelConfig, len(externals))
	for i, entry := range externals {
		key := strings.TrimSpace(entry.URL)
		slug, err := customModelSlug(key)
		if err != nil {
			return nil, fmt.Errorf("agysettings: external #%d (%s): %w", i+1, maskOperand(entry.URL), err)
		}
		if err := checkReservedName(key, "customModels key"); err != nil {
			return nil, fmt.Errorf("agysettings: external #%d (%s): %w", i+1, maskOperand(entry.URL), err)
		}
		if err := checkReservedName(slug, "model name derived from the key"); err != nil {
			return nil, fmt.Errorf("agysettings: external #%d (%s): %w", i+1, maskOperand(entry.URL), err)
		}

		cfg := entry.config()
		if err := cfg.validate(); err != nil {
			return nil, fmt.Errorf("agysettings: external #%d (%s): %w", i+1, maskOperand(entry.URL), err)
		}
		if err := checkReservedName(cfg.ModelName, "modelName"); err != nil {
			return nil, fmt.Errorf("agysettings: external #%d (%s): %w", i+1, maskOperand(entry.URL), err)
		}
		if _, dup := models[key]; dup {
			return nil, fmt.Errorf("agysettings: external #%d: duplicate customModels key %s", i+1, maskOperand(key))
		}
		models[key] = cfg
	}

	doc := map[string]any{
		KeyModelProvider: ModelProviderGemini,
		KeyCustomModels:  models,
	}
	for key := range doc {
		if !isManagedKey(key) {
			return nil, notManagedError(key)
		}
	}
	return doc, nil
}

// customModelSlug validates a customModels map key and returns the model name
// agy derives from it (the "/models/<name>" group or the trailing segment).
func customModelSlug(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("customModels key must not be empty")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "", fmt.Errorf("customModels key %s must not contain whitespace", maskOperand(key))
	}
	m := customModelKeyRegex.FindStringSubmatch(key)
	if m == nil {
		return "", fmt.Errorf("customModels key %s does not look like a custom model URL; expected gemini-api://<base_url>/models/<model_name>, blade:<base_url>/models/<model_name>, evergreen:<base_url>/models/<model_name>, or beyond-api://<base_url>/<model_name>", maskOperand(key))
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}

// checkReservedName refuses a name that collides with a static Gemini default
// or an AI-Studio preset name. Matching is case-insensitive: a near-miss
// spelling is still a collision worth refusing rather than guessing at.
func checkReservedName(name, form string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil
	}
	for _, reserved := range ReservedModelNames() {
		if strings.EqualFold(trimmed, strings.TrimSpace(reserved)) {
			return fmt.Errorf("%s %s collides with the reserved name %q; refusing to emit an entry agy would resolve to a static default or to the AI-Studio preset", form, maskOperand(name), reserved)
		}
	}
	return nil
}

// validate checks the entry against agy's documented per-entry validation.
func (c CustomModelConfig) validate() error {
	if strings.TrimSpace(c.ModelName) == "" {
		return fmt.Errorf("modelName is required (agy: \"customModels[%%s]: modelName is required\")")
	}
	if c.ContextWindow < 0 {
		return fmt.Errorf("context_window must not be negative, got %d", c.ContextWindow)
	}
	return nil
}

// validateCustomModels checks every entry of a managed customModels map: the
// key form, the key and derived slug against the reserved names, and the entry
// itself. Errors are reported in deterministic key order.
func validateCustomModels(models map[string]CustomModelConfig) error {
	for _, key := range sortedCustomModelKeys(models) {
		slug, err := customModelSlug(key)
		if err != nil {
			return fmt.Errorf("agysettings: customModels[%s]: %w", maskOperand(key), err)
		}
		if err := checkReservedName(key, "customModels key"); err != nil {
			return fmt.Errorf("agysettings: customModels[%s]: %w", maskOperand(key), err)
		}
		if err := checkReservedName(slug, "model name derived from the key"); err != nil {
			return fmt.Errorf("agysettings: customModels[%s]: %w", maskOperand(key), err)
		}
		if err := models[key].validate(); err != nil {
			return fmt.Errorf("agysettings: customModels[%s]: %w", maskOperand(key), err)
		}
		if err := checkReservedName(models[key].ModelName, "modelName"); err != nil {
			return fmt.Errorf("agysettings: customModels[%s]: %w", maskOperand(key), err)
		}
	}
	return nil
}
