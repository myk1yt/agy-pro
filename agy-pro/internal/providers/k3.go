package providers

// The k3 pool-exclusion rule, framed as the BUILT-IN DEFAULT ENTRY of the
// user-owned exclusion list (D1 = `defaults.excludeModels` in providers.json;
// D2 = one rule for every exclusion; plan §4.6, contract §2.5b, F-C;
// verify-report-f01.md §6).
//
// There is no separate stricter constant policy. An ExclusionPolicy whose User
// list is nil (no excludeModels block in the registry) answers purely from
// these two predicates, and a non-nil user list REPLACES this entry. The
// predicate is dual on purpose: `InferFamily(id) == "k3"` is unsatisfiable for
// every input (kimi-k3 infers "kimi", k3-agent-swarm infers "k"), so a
// family-only declaration (Family: "k3" on a benign id) and a token-bearing id
// are BOTH part of the single rule.
//
// Both predicates are byte-behaviour-identical to the pre-unification forms:
// inventory's id-only delegation is retired (inventory.Entry.Excluded now
// carries the one verdict), so these functions are the only remaining mouth of
// the built-in rule.

import "strings"

// IsK3Family is the built-in default exclusion entry: explicit family string
// "k3" (case-insensitive, via Model.FamilyName, which honors an explicit
// Family field) OR an id-token match (HasK3Token).
func IsK3Family(m Model) bool {
	if strings.EqualFold(strings.TrimSpace(m.FamilyName()), "k3") {
		return true
	}
	return HasK3Token(m.ID)
}

// HasK3Token tokenizes the lowercase id on every non-[a-z0-9] rune and matches
// the EXACT token "k3". Boundary-safe: kimi-k3, kimi-k3-flex, k3-agent-swarm
// and vendor-prefixed forms match; k3000, kimi-k2 and glm-k3x do not.
// Equivalent regexp: (^|[^a-z0-9])k3([^a-z0-9]|$).
func HasK3Token(id string) bool {
	for _, tok := range strings.FieldsFunc(strings.ToLower(id), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if tok == "k3" {
			return true
		}
	}
	return false
}
