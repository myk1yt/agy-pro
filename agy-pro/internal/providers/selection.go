package providers

import (
	"fmt"
	"regexp"
	"strings"
)

var modelDisplayRe = regexp.MustCompile(`^(.+?)\s*\(([^)]+)\)$`)

// DisplayName renders the "model (provider)" alias that identifies a model on
// every external-model surface: the bridge /models listing, the
// AGY_LLM_GATEWAY_MODELS list behind the native /model picker, and the wizard
// summary. It is the exact form ParseModelSpec parses back.
func DisplayName(modelID, providerName string) string {
	return fmt.Sprintf("%s (%s)", strings.TrimSpace(modelID), strings.TrimSpace(providerName))
}

// ParseModelSpec parses a model specification string into its base model ID
// and provider name (if specified).
// Supported formats:
//   - "model (provider)" -> ("model", "provider")
//   - "provider/model"   -> ("model", "provider")
//   - "model"            -> ("model", "")
func ParseModelSpec(spec string) (baseModel, providerName string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", ""
	}

	// 1. Check for "model (provider)" format
	if m := modelDisplayRe.FindStringSubmatch(spec); len(m) == 3 {
		base := strings.TrimSpace(m[1])
		prov := strings.TrimSpace(m[2])
		if base != "" && prov != "" {
			return base, prov
		}
	}

	// 2. Check for "provider/model" format
	if parts := strings.SplitN(spec, "/", 2); len(parts) == 2 {
		prov := strings.TrimSpace(parts[0])
		base := strings.TrimSpace(parts[1])
		if prov != "" && base != "" {
			return base, prov
		}
	}

	// 3. Fallback: raw model name
	return spec, ""
}

// Target pairs a Provider with one of its Models.
type Target struct {
	Provider Provider `json:"provider"`
	Model    Model    `json:"model"`
}

// FullName returns "provider/model" identifier.
func (t Target) FullName() string {
	return t.Provider.Name + "/" + t.Model.ID
}

// DisplayName returns the "model (provider)" alias for this target.
func (t Target) DisplayName() string {
	return DisplayName(t.Model.ID, t.Provider.Name)
}

// AllCandidates returns all enabled model targets across all providers.
// If onlyKeyAvailable is true, providers whose APIKeyEnv is unset are skipped.
func (r *Registry) AllCandidates(onlyKeyAvailable bool) []Target {
	var out []Target
	if r == nil {
		return out
	}
	for _, p := range r.Providers {
		if onlyKeyAvailable && !p.KeyAvailable() {
			continue
		}
		for _, m := range p.Models {
			if m.IsEnabled() {
				out = append(out, Target{
					Provider: p,
					Model:    m,
				})
			}
		}
	}
	return out
}

// FindTarget looks for a target matching spec ("model (provider)", "provider/model", or "model").
func (r *Registry) FindTarget(spec string) (Target, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Target{}, fmt.Errorf("empty model specification")
	}

	baseModel, provFilter := ParseModelSpec(spec)

	// First pass: match provider and model, preferring enabled models
	for _, p := range r.Providers {
		if provFilter != "" && !strings.EqualFold(p.Name, provFilter) {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, baseModel) && m.IsEnabled() {
				return Target{Provider: p, Model: m}, nil
			}
		}
	}

	// Second pass: if provFilter was given, check disabled models under that provider
	if provFilter != "" {
		for _, p := range r.Providers {
			if strings.EqualFold(p.Name, provFilter) {
				for _, m := range p.Models {
					if strings.EqualFold(m.ID, baseModel) {
						return Target{Provider: p, Model: m}, nil
					}
				}
			}
		}
	}

	// Third pass: if provFilter was given but not found under that provider,
	// fallback to any provider matching baseModel
	if provFilter != "" {
		for _, p := range r.Providers {
			for _, m := range p.Models {
				if strings.EqualFold(m.ID, baseModel) && m.IsEnabled() {
					return Target{Provider: p, Model: m}, nil
				}
			}
		}
	}

	return Target{}, fmt.Errorf("model %q not found in registry", MaskOperand(spec))
}

// FindAlternatesForModel finds other providers offering the exact same modelID.
// It excludes excludeProvider and requires KeyAvailable() == true.
func (r *Registry) FindAlternatesForModel(modelID, excludeProvider string) []Target {
	var out []Target
	if r == nil {
		return out
	}
	for _, p := range r.Providers {
		if strings.EqualFold(p.Name, excludeProvider) || !p.KeyAvailable() {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, modelID) && m.IsEnabled() {
				out = append(out, Target{Provider: p, Model: m})
			}
		}
	}
	return out
}

// FindAlternatesExcludingFamilies finds targets whose family is not in excludeFamilies.
// It requires KeyAvailable() == true.
func (r *Registry) FindAlternatesExcludingFamilies(excludeFamilies []string) []Target {
	var out []Target
	if r == nil {
		return out
	}
	exSet := make(map[string]bool, len(excludeFamilies))
	for _, f := range excludeFamilies {
		exSet[strings.ToLower(strings.TrimSpace(f))] = true
	}

	for _, p := range r.Providers {
		if !p.KeyAvailable() {
			continue
		}
		for _, m := range p.Models {
			if !m.IsEnabled() {
				continue
			}
			fam := m.FamilyName()
			if !exSet[fam] {
				out = append(out, Target{Provider: p, Model: m})
			}
		}
	}
	return out
}
