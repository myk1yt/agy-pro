package providers

import (
	"fmt"
	"strings"
)

// Target pairs a Provider with one of its Models.
type Target struct {
	Provider Provider `json:"provider"`
	Model    Model    `json:"model"`
}

// FullName returns "provider/model" identifier.
func (t Target) FullName() string {
	return t.Provider.Name + "/" + t.Model.ID
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

// FindTarget looks for a target matching spec ("provider/model" or "model").
func (r *Registry) FindTarget(spec string) (Target, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Target{}, fmt.Errorf("empty model specification")
	}

	var provFilter, modelFilter string
	if parts := strings.SplitN(spec, "/", 2); len(parts) == 2 {
		provFilter = parts[0]
		modelFilter = parts[1]
	} else {
		modelFilter = spec
	}

	for _, p := range r.Providers {
		if provFilter != "" && !strings.EqualFold(p.Name, provFilter) {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, modelFilter) && m.IsEnabled() {
				return Target{Provider: p, Model: m}, nil
			}
		}
	}
	return Target{}, fmt.Errorf("model %q not found in registry", spec)
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
