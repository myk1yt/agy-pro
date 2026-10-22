package providers

import (
	"strings"
	"testing"
)

func TestHasK3Token(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		// positives (verify-report-f01.md §6 table)
		{"kimi-k3", true},
		{"kimi-k3-flex", true},
		{"k3-agent-swarm", true},
		{"NW/kimi-k3-flex", true},
		{"kimi-code/k3-agent-swarm", true},
		{"Charm/kimi-k3", true},
		{"k3", true},
		{"K3-Agent-Swarm", true}, // case-insensitive
		{"KIMI-K3", true},
		// boundary-safe negatives
		{"k3000", false},
		{"kimi-k2", false},
		{"glm-k3x", false},
		{"glm-5.3-flash", false},
		{"gemini-2.5-flash", false},
		{"gpt-4o", false},
		{"", false},
	}
	for _, c := range cases {
		if got := HasK3Token(c.id); got != c.want {
			t.Errorf("HasK3Token(%q) = %v; want %v", c.id, got, c.want)
		}
	}
}

func TestIsK3Family(t *testing.T) {
	cases := []struct {
		name  string
		model Model
		want  bool
	}{
		{"explicit family k3 lowercase", Model{ID: "anything", Family: "k3"}, true},
		{"explicit family K3 uppercase", Model{ID: "anything", Family: "K3"}, true},
		{"explicit family k3 padded", Model{ID: "x", Family: "  k3  "}, true},
		{"id token, no family", Model{ID: "kimi-k3"}, true},
		{"id token flex, no family", Model{ID: "kimi-k3-flex"}, true},
		{"swarm id, no family", Model{ID: "k3-agent-swarm"}, true},
		{"family gemini but id token k3", Model{ID: "kimi-k3", Family: "gemini"}, true},
		{"k3000 boundary, no family", Model{ID: "k3000"}, false},
		{"kimi-k2 negative", Model{ID: "kimi-k2"}, false},
		{"glm-k3x negative", Model{ID: "glm-k3x"}, false},
		{"plain gemini", Model{ID: "gemini-2.5-flash", Family: "gemini"}, false},
		{"family kimi non-k3 id", Model{ID: "kimi-latest", Family: "kimi"}, false},
	}
	for _, c := range cases {
		if got := IsK3Family(c.model); got != c.want {
			t.Errorf("%s: IsK3Family(%+v) = %v; want %v", c.name, c.model, got, c.want)
		}
	}
}

// The k3 rule is the BUILT-IN DEFAULT ENTRY of the user-owned exclusion list
// (D1/D2), so it must be reachable through the authority and answer exactly what
// IsK3Family answers. The row that matters is the last family-only case: a
// benign id carrying an explicit family "k3" is EXCLUDED here, while the
// retired inventory.IsK3Excluded (id token only) allowed it - that disagreement
// is the defect this unification closes (inventory.Entry.Excluded now carries
// this one verdict for both the fanout and the swarm side).
func TestBuiltinDefaultEntryThroughAuthority(t *testing.T) {
	// familyOperand mirrors inventory.familyForEntry: the explicit registry
	// family case-preserved, else FamilyName().
	familyOperand := func(m Model) string {
		if f := strings.TrimSpace(m.Family); f != "" {
			return f
		}
		return m.FamilyName()
	}
	cases := []struct {
		name  string
		model Model
		want  bool
	}{
		{"family k3 with benign id", Model{ID: "kimi-latest", Family: "k3"}, true},
		{"family K3 uppercase, benign id", Model{ID: "kimi-latest", Family: "K3"}, true},
		{"family k3 padded, benign id", Model{ID: "kimi-latest", Family: "  k3  "}, true},
		{"explicit family k3, unrelated id", Model{ID: "anything", Family: "k3"}, true},
		{"id token, no family", Model{ID: "kimi-k3"}, true},
		{"id token flex, no family", Model{ID: "kimi-k3-flex"}, true},
		{"swarm id, no family", Model{ID: "k3-agent-swarm"}, true},
		{"family gemini but id token k3", Model{ID: "kimi-k3", Family: "gemini"}, true},
		{"k3000 boundary", Model{ID: "k3000"}, false},
		{"kimi-k2 negative", Model{ID: "kimi-k2"}, false},
		{"glm-k3x negative", Model{ID: "glm-k3x"}, false},
		{"plain gemini", Model{ID: "gemini-2.5-flash", Family: "gemini"}, false},
		{"family kimi non-k3 id", Model{ID: "kimi-latest", Family: "kimi"}, false},
	}
	policy := ExclusionPolicyFromDefaults(Defaults{})
	if policy.UserOwned() {
		t.Fatal("premise broken: an empty defaults block must leave the pool to the built-in default entry")
	}
	for _, c := range cases {
		got := policy.Excludes("nw", c.model.ID, familyOperand(c.model))
		if got != c.want {
			t.Errorf("%s: ExclusionPolicy.Excludes = %v; want %v", c.name, got, c.want)
		}
		if byPredicate := IsK3Family(c.model); got != byPredicate {
			t.Errorf("%s: authority (%v) and IsK3Family (%v) disagree", c.name, got, byPredicate)
		}
	}

	// A user-owned list replaces the entry outright: the k3 rule stays the
	// DEFAULT, not a separate stricter constant path, so kimi-k3 stops being
	// excluded once the user owns a list that does not name it.
	user := ExclusionPolicyFromDefaults(Defaults{ExcludeModels: []string{"nw/glm-5.3-flash"}})
	if !user.UserOwned() {
		t.Fatal("premise broken: a non-nil excludeModels block must be user-owned")
	}
	if user.Excludes("nw", "kimi-k3", "kimi") {
		t.Error("a user-owned list must replace the built-in k3 default entry")
	}
	if !user.Excludes("nw", "glm-5.3-flash", "glm") {
		t.Error("the user-owned list must answer for the entry it names")
	}
}
