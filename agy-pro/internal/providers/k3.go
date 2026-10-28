package providers

// k3-family exclusion predicate (contract §2.5b, F-C; verify-report-f01.md §6).
// Single source of truth for the subagent/swarm AUTO-seating pool policy:
// `InferFamily(id) == "k3"` is unsatisfiable for every input (kimi-k3 infers
// "kimi", k3-agent-swarm infers "k"), so exclusion must key on the explicit
// family string OR a boundary-safe id token match.

import "strings"

// IsK3Family reports the dual predicate: explicit family string "k3"
// (case-insensitive, via Model.FamilyName, which honors an explicit Family
// field) OR an id-token match (HasK3Token).
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
