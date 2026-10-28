// Package swarm implements the interactive swarm builder (`agy pro swarm`):
// templates, numbered seat selection over the combined inventory, the
// fanout argument builder, and the ~/.agy-pro/swarm-state.json preset store
// (contract U3, docs/261012_0002_session_hybrid-swarm-model-ux/architect-report.md).
//
// Seat assignment is NUMBERED selection only in the happy path (D1). The
// non-interactive flags remain a power-user/CI fallback (C2); flags.go owns
// that flag surface, including the --tools / --task equivalents of the
// interactive builder's edits (A2).
package swarm

import "strings"

// Template is one swarm task template (contract U3):
// code-review -> {review, read,search}, security-audit -> {security, read,search
// + adversarial framing}, custom.
type Template struct {
	ID             string // stable id accepted by --template
	Name           string // display name for the numbered pick
	Task           string // fanout -task value
	Tools          string // fanout -tools value
	PromptScaffold string // default user prompt when none is supplied
}

// Templates returns the built-in templates in numbered-pick order.
func Templates() []Template {
	return []Template{
		{
			ID:             "code-review",
			Name:           "Code Review",
			Task:           "review",
			Tools:          "read,search",
			PromptScaffold: "Review the code in this workspace for bugs, correctness, and edge cases. Cite exact file:line locations.",
		},
		{
			ID:             "security-audit",
			Name:           "Security Audit",
			Task:           "security",
			Tools:          "read,search",
			PromptScaffold: "Perform an adversarial security audit of this workspace. Cite exact file:line locations.",
		},
		{
			ID:             "custom",
			Name:           "Custom",
			Task:           "custom",
			Tools:          "read,search",
			PromptScaffold: "",
		},
	}
}

// FindTemplate resolves a template by id/name (case-insensitive). Accepted
// spellings: the exact id ("code-review"), its display name ("code review"),
// and the fanout task alias ("review", "security").
func FindTemplate(s string) (Template, bool) {
	key := strings.ToLower(strings.TrimSpace(s))
	key = strings.ReplaceAll(key, "_", "-")
	key = strings.ReplaceAll(key, " ", "-")
	for _, t := range Templates() {
		if key == t.ID || key == strings.ReplaceAll(strings.ToLower(t.Name), " ", "-") {
			return t, true
		}
	}
	switch key {
	case "review":
		return Templates()[0], true
	case "security":
		return Templates()[1], true
	}
	return Template{}, false
}
