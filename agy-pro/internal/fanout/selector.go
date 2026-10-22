package fanout

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"agypro/internal/providers"
)

// ExcludeFunc is the pool-exclusion seam (F-C, contract §2.5b): it reports
// whether AUTO-seating and failover must skip one provider/model. The provider
// NAME is part of the signature because the user-owned exclusion list (D1,
// defaults.excludeModels) may name a whole provider, which a model id alone
// cannot answer.
//
// A nil seam means the built-in default policy. Every construction site goes
// through PolicyExcluder, so the package holds exactly one matching rule.
type ExcludeFunc func(providerName string, m providers.Model) bool

// PolicyExcluder adapts the single exclusion authority
// (providers.ExclusionPolicy) into the fanout seam, so auto-seating and
// failover apply exactly the rule the inventory and the swarm seat list render.
// The zero policy (no user list) reproduces the built-in k3 default.
func PolicyExcluder(p providers.ExclusionPolicy) ExcludeFunc {
	return func(providerName string, m providers.Model) bool {
		return p.Excludes(providerName, m.ID, m.FamilyName())
	}
}

// excludedPickWarning is the ONE sentence every honored-but-excluded explicit
// pick renders. The swarm subsystem prints the identical sentence, so an
// operator reads the same rule wherever the pick came from; the "exclusion
// list" wording names the rule the user owns (D1) instead of a policy they
// cannot inspect.
const excludedPickWarning = "warning: %s is excluded (exclusion list); explicit pick honored (the exclusion list covers auto-seating and failover only)"

// warnExplicitPick prints the shared exclusion warning for one explicit pick
// that the pool rule would have skipped. Nothing is rejected anywhere: the pick
// is honored, and this line is the only consequence.
//
// The target renders through the registry-slot authority (MaskSlot), exactly
// like seatSpecLabel's provider branch: both halves of "provider/model" come
// from providers.json, so a key-shaped plant in either half masks.
func warnExplicitPick(w io.Writer, exclude ExcludeFunc, t providers.Target) {
	if w == nil || exclude == nil {
		return
	}
	if !exclude(t.Provider.Name, t.Model) {
		return
	}
	fmt.Fprintf(w, excludedPickWarning+"\n", providers.MaskSlot(t.FullName()))
}

// SelectTargets resolves N distinct targets (provider + model) from the registry,
// enforcing family diversity and consulting state history for rotation.
//
// exclude (F-C, contract §2.5b) is the pool-exclusion seam: candidates for
// which it returns true never enter the family grouping nor the fill pass, so
// they are never AUTO-seated. It is NOT applied to explicit user picks
// (explicitModels) — those are honored, and warnw receives the shared warning
// sentence when the pick is one the rule would have skipped. A nil exclude
// excludes nothing (the engine substitutes the built-in default policy); a nil
// warnw discards the warning.
func SelectTargets(reg *providers.Registry, state *FanoutState, n int, explicitModels []string, exclude ExcludeFunc, warnw io.Writer) ([]providers.Target, error) {
	if reg == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	if n <= 0 {
		n = 3
	}

	// 1. If user provided explicit models, resolve them directly. Explicit
	// picks are EXEMPT from the exclusion rule (contract §2.5b / D2=b): the
	// pick is honored, and an excluded one carries the shared warning.
	if len(explicitModels) > 0 {
		var selected []providers.Target
		for _, spec := range explicitModels {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			target, err := reg.FindTarget(spec)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve model %q: %w", providers.MaskOperand(spec), err)
			}
			warnExplicitPick(warnw, exclude, target)
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
		masked := make([]string, len(unset))
		for i, name := range unset {
			masked[i] = providers.MaskOperand(name)
		}
		return nil, fmt.Errorf("no models available with valid API keys (unset env vars: %s)", strings.Join(masked, ", "))
	}

	// F-C: apply the exclusion rule to AUTO-seating candidates before
	// grouping/filling. Track whether exclusion emptied the pool so the error
	// message names the user's own list rather than a policy they cannot see.
	if exclude != nil {
		filtered := allCandidates[:0]
		for _, cand := range allCandidates {
			if !exclude(cand.Provider.Name, cand.Model) {
				filtered = append(filtered, cand)
			}
		}
		if len(filtered) == 0 && len(allCandidates) > 0 {
			return nil, fmt.Errorf("no models available for auto-seating: every candidate is excluded by the exclusion list")
		}
		allCandidates = filtered
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
