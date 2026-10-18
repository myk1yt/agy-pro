package providers

import (
	"fmt"
	"strings"
	"unicode"
)

// Model-level registry operations (architect-report.md §5, requirement 3).
// These mutate ONLY the target provider's Models slice in place: they never
// call UpsertProvider, never rebuild a Provider struct, and never touch key
// material (apiKeyEnv and its value belong to the environment, not the file).
// Provider names are matched case-insensitively (wizard upsert semantics);
// model IDs are matched exactly, matching the duplicate rule in
// Registry.Validate. Every echoed operand passes through MaskOperand (B2/U8),
// and errors never echo key material.

// findProviderIdx returns the index of the named provider
// (case-insensitive) or -1.
func findProviderIdx(reg *Registry, name string) int {
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return i
		}
	}
	return -1
}

// findModelIdx returns the index of the model with the exact ID in p, or -1.
func findModelIdx(p *Provider, id string) int {
	for j := range p.Models {
		if p.Models[j].ID == id {
			return j
		}
	}
	return -1
}

// modelIDTaken reports whether id is already used by a model in p other than
// the element at except (pass -1 to check all elements).
func modelIDTaken(p *Provider, id string, except int) bool {
	for j := range p.Models {
		if j != except && p.Models[j].ID == id {
			return true
		}
	}
	return false
}

// hasControlRunes reports whether s carries any control rune (C0, ESC via
// C0, DEL, C1): single-line registry data must not contain any.
func hasControlRunes(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// checkModelFields rejects control characters in the single-line text a
// model carries: id, family, tags, and tool names. IDs, families, and tags
// are single-line data, so no control rune may persist into the protected
// providers.json (the frame strip in internal/tui is only the render-time
// backstop; this is the registry boundary, and it also covers the CLI JSON
// twin path that routes through the same ops). The error names the offending
// field class through the masked provider name only and never echoes the
// value itself, so a rejected ESC byte cannot replay in the caller's
// terminal or transcript.
func checkModelFields(providerName string, m Model) error {
	kind := ""
	switch {
	case hasControlRunes(m.ID):
		kind = "model id"
	case hasControlRunes(m.Family):
		kind = "model family"
	default:
		for _, t := range m.Tags {
			if hasControlRunes(t) {
				kind = "model tag"
				break
			}
		}
		if kind == "" {
			for _, tool := range m.Capabilities.AllowedTools {
				if hasControlRunes(tool) {
					kind = "model tool name"
					break
				}
			}
		}
	}
	if kind != "" {
		return fmt.Errorf("provider %q: %s contains control characters", MaskOperand(providerName), kind)
	}
	return nil
}

// checkToolNames rejects control characters in an AllowedTools replacement
// list (see checkModelFields for the boundary rule).
func checkToolNames(providerName string, tools []string) error {
	for _, tool := range tools {
		if hasControlRunes(tool) {
			return fmt.Errorf("provider %q: model tool name contains control characters", MaskOperand(providerName))
		}
	}
	return nil
}

// AddModel appends m to the named provider's Models slice in place. The
// provider is found by case-insensitive name; a duplicate model ID within
// that provider is rejected (the same rule Registry.Validate enforces). The
// mutated registry is validated before the change is kept; on validation
// failure the saved slice copy is restored.
func AddModel(reg *Registry, providerName string, m Model) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	providerName = strings.TrimSpace(providerName)
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		return fmt.Errorf("model id is required")
	}
	if err := checkModelFields(providerName, m); err != nil {
		return err
	}
	idx := findProviderIdx(reg, providerName)
	if idx < 0 {
		return fmt.Errorf("provider %q not found", MaskOperand(providerName))
	}
	p := &reg.Providers[idx]
	if modelIDTaken(p, m.ID, -1) {
		return fmt.Errorf("provider %q: model %q already exists", MaskOperand(p.Name), MaskOperand(m.ID))
	}
	saved := make([]Model, len(p.Models))
	copy(saved, p.Models)
	p.Models = append(p.Models, m)
	if err := reg.Validate(); err != nil {
		p.Models = saved
		return err
	}
	return nil
}

// DeleteModel removes the single model with the exact ID from the named
// provider in place. Deleting the last model of a provider is allowed: the
// provider keeps an empty list, matching the DeleteProvider precedent.
func DeleteModel(reg *Registry, providerName, modelID string) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	idx := findProviderIdx(reg, providerName)
	if idx < 0 {
		return fmt.Errorf("provider %q not found", MaskOperand(providerName))
	}
	p := &reg.Providers[idx]
	mj := findModelIdx(p, modelID)
	if mj < 0 {
		return fmt.Errorf("provider %q: model %q not found", MaskOperand(p.Name), MaskOperand(modelID))
	}
	p.Models = append(p.Models[:mj], p.Models[mj+1:]...)
	return nil
}

// UpdateModel replaces the model with the exact ID in the named provider in
// place. An ID rename (m.ID different from modelID) is allowed only when the
// new ID is unique within the provider. The mutated registry is validated
// before the change is kept; on validation failure the replaced element is
// restored.
func UpdateModel(reg *Registry, providerName, modelID string, m Model) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		return fmt.Errorf("model id is required")
	}
	if err := checkModelFields(providerName, m); err != nil {
		return err
	}
	idx := findProviderIdx(reg, providerName)
	if idx < 0 {
		return fmt.Errorf("provider %q not found", MaskOperand(providerName))
	}
	p := &reg.Providers[idx]
	mj := findModelIdx(p, modelID)
	if mj < 0 {
		return fmt.Errorf("provider %q: model %q not found", MaskOperand(p.Name), MaskOperand(modelID))
	}
	if m.ID != modelID && modelIDTaken(p, m.ID, mj) {
		return fmt.Errorf("provider %q: model %q already exists", MaskOperand(p.Name), MaskOperand(m.ID))
	}
	saved := p.Models[mj]
	p.Models[mj] = m
	if err := reg.Validate(); err != nil {
		p.Models[mj] = saved
		return err
	}
	return nil
}

// SetModelEnabled sets (or clears, via nil) the model's enabled flag in
// place. A nil Enabled means enabled; pass false to disable explicitly.
func SetModelEnabled(reg *Registry, providerName, modelID string, enabled bool) error {
	return mutateModel(reg, providerName, modelID, func(m *Model) {
		m.Enabled = &enabled
	})
}

// SetModelTools replaces the model's AllowedTools list in place. It does not
// change SupportsTools; callers that want to enable tool use set
// Capabilities via UpdateModel.
func SetModelTools(reg *Registry, providerName, modelID string, tools []string) error {
	if err := checkToolNames(providerName, tools); err != nil {
		return err
	}
	return mutateModel(reg, providerName, modelID, func(m *Model) {
		m.Capabilities.AllowedTools = tools
	})
}

// mutateModel applies fn to the named model in place. The mutation cannot
// break Registry.Validate (which checks IDs, names, base URLs, and env names
// only), so no rollback is needed.
func mutateModel(reg *Registry, providerName, modelID string, fn func(*Model)) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	idx := findProviderIdx(reg, providerName)
	if idx < 0 {
		return fmt.Errorf("provider %q not found", MaskOperand(providerName))
	}
	p := &reg.Providers[idx]
	mj := findModelIdx(p, modelID)
	if mj < 0 {
		return fmt.Errorf("provider %q: model %q not found", MaskOperand(p.Name), MaskOperand(modelID))
	}
	fn(&p.Models[mj])
	return nil
}

// RenameModel renames a model ID in place. The new ID must be unique within
// the provider (an exact match against the sibling IDs, the same rule
// Registry.Validate enforces). The mutated registry is validated before the
// change is kept; on validation failure the old ID is restored.
func RenameModel(reg *Registry, providerName, modelID, newID string) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	newID = strings.TrimSpace(newID)
	if newID == "" {
		return fmt.Errorf("model id is required")
	}
	if hasControlRunes(newID) {
		return fmt.Errorf("provider %q: model id contains control characters", MaskOperand(providerName))
	}
	idx := findProviderIdx(reg, providerName)
	if idx < 0 {
		return fmt.Errorf("provider %q not found", MaskOperand(providerName))
	}
	p := &reg.Providers[idx]
	mj := findModelIdx(p, modelID)
	if mj < 0 {
		return fmt.Errorf("provider %q: model %q not found", MaskOperand(p.Name), MaskOperand(modelID))
	}
	if newID != modelID && modelIDTaken(p, newID, mj) {
		return fmt.Errorf("provider %q: model %q already exists", MaskOperand(p.Name), MaskOperand(newID))
	}
	p.Models[mj].ID = newID
	if err := reg.Validate(); err != nil {
		p.Models[mj].ID = modelID
		return err
	}
	return nil
}
