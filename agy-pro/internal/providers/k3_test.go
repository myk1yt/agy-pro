package providers

import "testing"

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
