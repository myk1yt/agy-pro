package hybrid

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"agypro/internal/providers"
)

// CatalogOptions tunes BuildFetchAvailableModels. The zero value is usable:
// EnumBase defaults to 9001, GroupLabel to "External", and no enums are
// reserved.
type CatalogOptions struct {
	// ReservedEnums is the set of enum values the real agy binary already
	// maps in its static ModelInfo table (plan-model-gate-report.md §2). A
	// synthesized enum must never land on one of these, or agy would resolve
	// the external to a native id. The known values are 326 (BYOM), 330, 1050,
	// 1071-1073, 1298-1300, 1318-1320, and 1368; see DefaultReservedEnums.
	ReservedEnums map[int]bool
	// EnumBase is the first synthesized enum value. Default 9001 (values in no
	// static table resolve exactly with per-model metadata honored,
	// audit-gap-probe-report.md §3.3).
	EnumBase int
	// GroupLabel is the display_name of the single agent_model_sorts entry.
	// Default "External". The label is inert: only group MEMBERSHIP gates
	// listing and selection, not the sort or group display names.
	GroupLabel string
	// Logf receives fail-closed drop reasons (a cross-provider duplicate id, a
	// blank id). Operands are masked before they reach the sink. Nil discards
	// the reasons.
	Logf func(format string, args ...any)
}

// DefaultEnumBase is the first synthesized enum. 9001+ sit in no static table,
// so agy resolves them exactly and honors per-model metadata; a synthesized
// enum's wire label is the bare number string (harmless).
const DefaultEnumBase = 9001

// DefaultGroupLabel is the default agent_model_sorts display_name.
const DefaultGroupLabel = "External"

// defaultMaxTokens is the max_tokens served for an external whose registry
// ContextLength is below maxTokensFloor. Below ~20.5k every print run hard-fails
// ("maxMessageTokens must be positive, got 0"), and the threshold scales with
// agy's own prompt+tool payload, so a generous constant is served instead of a
// value that could drift under the floor (audit-gap-probe-report.md §3.5).
const defaultMaxTokens = 131072

// maxTokensFloor is the registry ContextLength at or above which the real value
// is served; below it the generous defaultMaxTokens is served.
const maxTokensFloor = 25000

// defaultMaxOutputTokens is served when the registry MaxOutputTokens is not
// positive. agy's own default is 16384.
const defaultMaxOutputTokens = 16384

// apiProviderGoogle is the api_provider carried by every external entry. It is
// inert on the mesh path but present in the proven payload.
const apiProviderGoogle = "API_PROVIDER_GOOGLE"

// staticGeminiIDs are the 11 static Gemini default ids agy lists in gemini mode.
// An external whose id collides with any of them is refused at sync time: agy
// would show two entries with the same id and route ambiguously.
var staticGeminiIDs = []string{
	"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low",
	"gemini-3.7-flash-high", "gemini-3.7-flash-medium", "gemini-3.7-flash-low",
	"gemini-3.6-flash-high", "gemini-3.6-flash-medium", "gemini-3.6-flash-low",
	"gemini-3.1-pro-high", "gemini-3.1-pro-low",
}

// DefaultReservedEnums returns the known static enum set from
// plan-model-gate-report.md §2: the binary's static ModelInfo table maps these
// enum values to native ids, so a synthesized enum must skip all of them.
func DefaultReservedEnums() map[int]bool {
	return map[int]bool{
		326: true, // MODEL_GOOGLE_GEMINI_INTERNAL_BYOM
		330: true, 1050: true,
		1071: true, 1072: true, 1073: true,
		1298: true, 1299: true, 1300: true,
		1318: true, 1319: true, 1320: true,
		1368: true,
	}
}

// catalogModel is one fetchAvailableModels model entry (ModelDetails).
type catalogModel struct {
	DisplayName      string `json:"display_name"`
	Model            int    `json:"model"`
	MaxTokens        int    `json:"max_tokens"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	APIProvider      string `json:"api_provider"`
	SupportsThinking bool   `json:"supports_thinking,omitempty"`
}

// catalogGroup is one agent_model_sorts group.
type catalogGroup struct {
	DisplayName string   `json:"display_name"`
	ModelIDs    []string `json:"model_ids"`
}

// catalogSort is one agent_model_sorts entry.
type catalogSort struct {
	DisplayName string         `json:"display_name"`
	Groups      []catalogGroup `json:"groups"`
}

// catalogResponse is the fetchAvailableModels payload. It carries only models
// and agent_model_sorts: the minimal shape proven sufficient for native
// print-mode generation (plan-model-gate-report.md §3.2). default_agent_model_id
// and tiered_model_ids are deliberately omitted (see BuildFetchAvailableModels).
type catalogResponse struct {
	Models          map[string]catalogModel `json:"models"`
	AgentModelSorts []catalogSort           `json:"agent_model_sorts"`
}

// BuildFetchAvailableModels composes the fetchAvailableModels payload from the
// provider registry. For each provider (registry order) and each ENABLED model
// it emits an entry keyed by the model id (the mesh routes by this slug):
//
//   - display_name = "<id> (<provider name>)" (non-empty; the picker label).
//   - model = a UNIQUE synthesized enum from EnumBase, skipping ReservedEnums
//     and any already-allocated value (deterministic, monotonic).
//   - max_tokens = ContextLength when >= 25000, else 131072.
//   - max_output_tokens = MaxOutputTokens when > 0, else 16384.
//   - api_provider = "API_PROVIDER_GOOGLE"; supports_thinking from capabilities.
//
// Every id lands in exactly one agent_model_sorts group (one per provider). A
// cross-provider duplicate id is dropped fail-closed with a logged reason: never
// a duplicate map key, never a duplicate enum. An id colliding with a static
// Gemini id or an AI-Studio preset name (providers.KnownProviders) is refused
// with a clear error naming the id.
//
// Documented decision: no tiered_model_ids and no default_agent_model_id.
// P1-lite is external-only, so a tier pick is not offered, and emitting a tier
// pointing at a native id would be a hidden native. The title/checkpoint turn is
// answered by the mesh's local housekeeping handler, and the minimal payload is
// proven sufficient for print mode.
//
// That handler is model-name independent on purpose (G10): with no
// gemini-*-flash-lite-preview entry in this external-only catalog, agy resolves
// the title model to an external session id (observed: ext-gamma-1), so
// wireadp's dispatchGenerate detects the title turn by its system-prompt
// signature before this catalog's allow-list is ever consulted. Adding a
// housekeeping entry here would not be what answers it.
//
// The output is byte-deterministic for a given registry: encoding/json sorts the
// models map keys, and agent_model_sorts, its groups, and each group's model_ids
// are all built in registry order. The bytes feed wireadp.Config.CatalogFunc.
func BuildFetchAvailableModels(reg *providers.Registry, opts CatalogOptions) ([]byte, error) {
	enumBase := opts.EnumBase
	if enumBase <= 0 {
		enumBase = DefaultEnumBase
	}
	groupLabel := opts.GroupLabel
	if strings.TrimSpace(groupLabel) == "" {
		groupLabel = DefaultGroupLabel
	}
	reserved := opts.ReservedEnums
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	models := map[string]catalogModel{}
	var groups []catalogGroup

	// A nil or empty registry yields the minimal empty catalog (matches the
	// wireadp default shape), never a null map or null sorts array.
	if reg == nil {
		return marshalCatalog(catalogResponse{Models: models, AgentModelSorts: []catalogSort{}})
	}

	denied := buildDenyList()
	seenID := map[string]bool{}
	allocated := map[int]bool{}
	nextEnum := enumBase

	for _, p := range reg.Providers {
		var ids []string
		for _, m := range p.Models {
			if !m.IsEnabled() {
				continue
			}
			// FIX-64: one normalization point for the id — trim AND strip control
			// characters — so the catalog map key, agent_model_sorts.model_ids,
			// and the resolver comparison all agree and no control char reaches
			// agy's picker (raw or JSON-escaped).
			id := stripControlChars(strings.TrimSpace(m.ID))
			if id == "" {
				logf("hybrid catalog: dropped a model from provider %q: blank id", providers.MaskOperand(p.Name))
				continue
			}
			if denied[strings.ToLower(id)] {
				return nil, fmt.Errorf("external model id %q collides with a reserved native or AI-Studio preset name; refusing to sync (rename it in providers.json)", providers.MaskOperand(id))
			}
			if seenID[id] {
				logf("hybrid catalog: dropped model %q from provider %q: id already listed by an earlier provider", providers.MaskOperand(id), providers.MaskOperand(p.Name))
				continue
			}
			seenID[id] = true

			enum := nextEnum
			for reserved[enum] || allocated[enum] {
				enum++
			}
			allocated[enum] = true
			nextEnum = enum + 1

			models[id] = catalogModel{
				DisplayName:      stripControlChars(providers.DisplayName(id, p.Name)),
				Model:            enum,
				MaxTokens:        maxTokensFor(m.ContextLength),
				MaxOutputTokens:  maxOutputTokensFor(m.MaxOutputTokens),
				APIProvider:      apiProviderGoogle,
				SupportsThinking: m.Capabilities.SupportsThinking,
			}
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			name := stripControlChars(strings.TrimSpace(p.Name))
			if name == "" {
				name = groupLabel
			}
			groups = append(groups, catalogGroup{DisplayName: name, ModelIDs: ids})
		}
	}

	sorts := []catalogSort{}
	if len(groups) > 0 {
		sorts = append(sorts, catalogSort{DisplayName: groupLabel, Groups: groups})
	}
	return marshalCatalog(catalogResponse{Models: models, AgentModelSorts: sorts})
}

// marshalCatalog serializes the payload deterministically. encoding/json sorts
// map keys, and every slice is already in registry order, so two builds over the
// same registry are byte-identical.
func marshalCatalog(resp catalogResponse) ([]byte, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize catalog: %w", err)
	}
	return data, nil
}

// stripControlChars removes control characters from s, keeping printable runes.
// Registry strings (model ids, provider names) are user-editable and flow into
// agy's picker as display_name and group labels and into logs, so control
// characters (\r, \n, \t, ...) are stripped there (FIX-51) and at the catalog
// drop-reason sink (FIX-50), matching the apology-path hygiene.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// maxTokensFor serves the registry ContextLength when it clears the floor, else
// the generous default that keeps every print run above the hard-fail threshold.
func maxTokensFor(contextLength int) int {
	if contextLength >= maxTokensFloor {
		return contextLength
	}
	return defaultMaxTokens
}

// maxOutputTokensFor serves the registry MaxOutputTokens when positive, else the
// agy default.
func maxOutputTokensFor(maxOutput int) int {
	if maxOutput > 0 {
		return maxOutput
	}
	return defaultMaxOutputTokens
}

// buildDenyList returns the lowercase set of ids that must never be served as an
// external: the 11 static Gemini ids plus every AI-Studio preset provider name
// and alias from providers.KnownProviders. Matching is case-insensitive so a
// case variant of a native or preset name is still refused.
func buildDenyList() map[string]bool {
	deny := make(map[string]bool)
	for _, id := range staticGeminiIDs {
		deny[strings.ToLower(id)] = true
	}
	for _, h := range providers.KnownProviders() {
		if n := strings.ToLower(strings.TrimSpace(h.Name)); n != "" {
			deny[n] = true
		}
		for _, a := range h.Aliases {
			if n := strings.ToLower(strings.TrimSpace(a)); n != "" {
				deny[n] = true
			}
		}
	}
	return deny
}
