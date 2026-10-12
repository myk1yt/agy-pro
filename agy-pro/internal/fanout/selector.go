package fanout

import (
	"fmt"
	"sort"
	"strings"

	"agypro/internal/providers"
)

// SelectTargets resolves N distinct targets (provider + model) from the registry,
// enforcing family diversity and consulting state history for rotation.
func SelectTargets(reg *providers.Registry, state *FanoutState, n int, explicitModels []string) ([]providers.Target, error) {
	if reg == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	if n <= 0 {
		n = 3
	}

	// 1. If user provided explicit models, resolve them directly
	if len(explicitModels) > 0 {
		var selected []providers.Target
		for _, spec := range explicitModels {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			target, err := reg.FindTarget(spec)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve model %q: %w", spec, err)
			}
			selected = append(selected, target)
		}
		if len(selected) > 0 {
			return selected, nil
		}
	}

	// 2. Discover all enabled candidates with available API keys
	allCandidates := reg.AllCandidates(true)
	if len(allCandidates) == 0 {
		// If no provider has keys set, try all candidates to provide actionable error
		all := reg.AllCandidates(false)
		if len(all) == 0 {
			return nil, fmt.Errorf("no enabled models found in registry")
		}
		unset := reg.UnsetEnvNames()
		return nil, fmt.Errorf("no models available with valid API keys (unset env vars: %s)", strings.Join(unset, ", "))
	}

	// 3. Group candidates by family
	byFamily := map[string][]providers.Target{}
	for _, cand := range allCandidates {
		fam := cand.Model.FamilyName()
		byFamily[fam] = append(byFamily[fam], cand)
	}

	var families []string
	for f := range byFamily {
		families = append(families, f)
	}
	sort.Strings(families)

	// 4. Rotation preference: prioritize families NOT in state.LastUsedFamilies
	lastUsedSet := map[string]bool{}
	if state != nil {
		for _, f := range state.LastUsedFamilies {
			lastUsedSet[f] = true
		}
	}

	var freshFamilies, recentFamilies []string
	for _, f := range families {
		if lastUsedSet[f] {
			recentFamilies = append(recentFamilies, f)
		} else {
			freshFamilies = append(freshFamilies, f)
		}
	}

	// Order candidate families: fresh families first, then recently used
	orderedFamilies := append(freshFamilies, recentFamilies...)

	// 5. Pick N targets with distinct families
	var selected []providers.Target
	usedFamilies := map[string]bool{}

	for _, fam := range orderedFamilies {
		if len(selected) >= n {
			break
		}
		cands := byFamily[fam]
		if len(cands) == 0 {
			continue
		}
		// Pick preferred model in family (tag "flagship" if present)
		best := pickBestModelInFamily(cands)
		selected = append(selected, best)
		usedFamilies[fam] = true
	}

	// If fewer families available than N, fill remaining slots from remaining models
	if len(selected) < n {
		for _, cand := range allCandidates {
			if len(selected) >= n {
				break
			}
			alreadyPicked := false
			for _, s := range selected {
				if s.Provider.Name == cand.Provider.Name && s.Model.ID == cand.Model.ID {
					alreadyPicked = true
					break
				}
			}
			if !alreadyPicked {
				selected = append(selected, cand)
			}
		}
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("unable to select any models for fanout")
	}

	return selected, nil
}

func pickBestModelInFamily(cands []providers.Target) providers.Target {
	for _, c := range cands {
		if c.Model.HasTag("flagship") {
			return c
		}
	}
	for _, c := range cands {
		if c.Model.HasTag("tools") {
			return c
		}
	}
	return cands[0]
}
