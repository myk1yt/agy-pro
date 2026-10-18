// Core model-level verbs (plan-v2 phase P6): thin orchestration over the
// providers in-place model ops (P5) through the LoadReg/SaveReg seams. Each
// verb loads the registry, delegates to the single-mutation op (which mutates
// in place, never touches UpsertProvider, and masks every echoed operand via
// MaskOperand), then saves the registry through SaveReg (whole-file 0600
// atomic). Human approval (the M4 modal) is enforced by the TUI layer, NEVER
// inside Core; these verbs are pure non-interactive state transitions.

package settings

import (
	"agypro/internal/providers"
)

// ModelAdd appends m to the named provider's model list (in place).
// The provider resolves case-insensitively; a duplicate model ID is rejected
// before any file write, and the whole registry is saved on success only.
func (c *Core) ModelAdd(providerName string, m providers.Model) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.AddModel(reg, providerName, m); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ModelDelete removes the single model with the exact ID from the named
// provider (in place). Key material and provider scalars are untouched.
func (c *Core) ModelDelete(providerName, modelID string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.DeleteModel(reg, providerName, modelID); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ModelUpdate replaces the named model in place. An ID rename inside the
// update is allowed only when the new ID is unique within the provider.
func (c *Core) ModelUpdate(providerName, modelID string, m providers.Model) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.UpdateModel(reg, providerName, modelID, m); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ModelSetEnabled sets or clears the model's enabled flag in place.
// A false disable round-trips through the file (explicit off is persisted).
func (c *Core) ModelSetEnabled(providerName, modelID string, enabled bool) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.SetModelEnabled(reg, providerName, modelID, enabled); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ModelSetTools replaces the model's AllowedTools list in place. It does not
// change SupportsTools; callers set Capabilities via ModelUpdate.
func (c *Core) ModelSetTools(providerName, modelID string, tools []string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.SetModelTools(reg, providerName, modelID, tools); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ModelRename renames a model ID in place. The new ID must be unique within
// the provider; validation failure restores the old ID and nothing is saved.
func (c *Core) ModelRename(providerName, modelID, newID string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.RenameModel(reg, providerName, modelID, newID); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}
