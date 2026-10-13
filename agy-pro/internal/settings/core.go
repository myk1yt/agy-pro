// Package settings implements the G4-b settings surface (contract §2.9):
// a presentation-agnostic Core shared by the M1 plugin's non-interactive verbs
// and the M2 full-screen TUI (`agy pro settings`). No front-end reads
// state.json or providers.json directly; state writes go through
// mode.SaveState, provider writes through providers.WriteFileAtomic, and key
// VALUES never enter the registry (APIKeyEnv carries an env-var NAME only).
package settings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/inventory"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/swarm"
)

// Deps are the injectable seams behind Core (contract §2.9). Nil fields are
// filled with the real implementations by NewCore; tests inject fakes so no
// live file, registry, or network is touched.
type Deps struct {
	LoadState    func() (*mode.State, error)
	SaveState    func(*mode.State) error
	LoadReg      func() (*providers.Registry, error)
	SaveReg      func(*providers.Registry) error
	AuditKeys    func(*providers.Registry, providers.AuditOptions) []providers.KeyAudit
	FixKeys      func(target string) error
	RollbackKeys func(backupDir string) error
	RunWizard    func(in io.Reader, out io.Writer) error
	Doctor       func(out io.Writer, strict bool) error
	Inventory    func(ctx context.Context) (*inventory.Inventory, error)
	SwarmStore   swarm.Store
	// VersionFunc (additive to the frozen Deps set) reports the shim version
	// for the maintenance panel; the CLI wires it to main.Version.
	VersionFunc func() string
	// SwitchMode / RestoreMode (additive) wrap the existing mode.Switch /
	// mode.Restore ops so tests never touch the live profile directory.
	SwitchMode  func(target string, in io.Reader, out io.Writer) error
	RestoreMode func(out io.Writer) error
}

// ProviderSpec is the non-interactive provider add payload (F-PROV).
// APIKeyEnv must be an environment-variable NAME — a key VALUE is rejected.
type ProviderSpec struct {
	Name, BaseURL string
	APIKeyEnv     string
	Models        []providers.Model
}

// Core is the settings verb surface (contract §2.9). Every method is
// idempotent where the underlying op is, non-interactive, and safe to drive
// from the M1 plugin body or the M2 TUI.
type Core struct {
	deps Deps
}

// NewCore builds a Core over deps, filling nil seams with real implementations.
func NewCore(deps Deps) *Core {
	if deps.LoadState == nil {
		deps.LoadState = mode.LoadState
	}
	if deps.SaveState == nil {
		deps.SaveState = mode.SaveState
	}
	if deps.LoadReg == nil {
		deps.LoadReg = providers.Load
	}
	if deps.SaveReg == nil {
		deps.SaveReg = func(reg *providers.Registry) error { return saveRegistryFile(reg) }
	}
	if deps.AuditKeys == nil {
		deps.AuditKeys = func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
			return providers.AuditProviderKeys(reg, opts)
		}
	}
	if deps.FixKeys == nil {
		deps.FixKeys = defaultFixKeys
	}
	if deps.RollbackKeys == nil {
		deps.RollbackKeys = defaultRollbackKeys
	}
	if deps.RunWizard == nil {
		deps.RunWizard = providers.InteractiveSetupWizard
	}
	if deps.Doctor == nil {
		deps.Doctor = defaultDoctor
	}
	if deps.Inventory == nil {
		deps.Inventory = defaultInventory
	}
	if deps.SwarmStore == nil {
		if st, err := swarm.NewFileStore(); err == nil {
			deps.SwarmStore = st
		}
	}
	if deps.SwitchMode == nil {
		deps.SwitchMode = mode.Switch
	}
	if deps.RestoreMode == nil {
		deps.RestoreMode = mode.Restore
	}
	return &Core{deps: deps}
}

// Set writes one settings key. Supported keys (contract §2.9):
// default-profile | bridge-tools | native-seat-concurrency.
func (c *Core) Set(key, value string) error {
	st, err := c.deps.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "default-profile", "default_profile":
		profile := strings.ToLower(strings.TrimSpace(value))
		if !mode.ValidProfile(profile) {
			// B2/U8: operands may be misdirected key pastes; mask before echo.
			return fmt.Errorf("invalid default profile %q (use prompt, native, or gateway)", providers.MaskOperand(value))
		}
		st.DefaultProfile = mode.NormalizeProfile(profile)
	case "bridge-tools", "bridge_tools":
		tools, err := ParseBridgeToolsSpec(value)
		if err != nil {
			return err
		}
		st.BridgeTools = tools
	case "native-seat-concurrency", "native_seat_concurrency":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("native-seat-concurrency must be an integer (got %q)", providers.MaskOperand(value))
		}
		if n < 1 || n > 8 {
			return fmt.Errorf("native-seat-concurrency must be 1..8 (got %d)", n)
		}
		st.NativeSeatConcurrency = n
	default:
		return fmt.Errorf("unknown settings key %q (want default-profile, bridge-tools, or native-seat-concurrency)", providers.MaskOperand(key))
	}
	if err := c.deps.SaveState(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// ParseBridgeToolsSpec validates a bridge-tools value exactly like
// `agy pro config bridge-tools` (main.go parseBridgeToolsSpec): the closed
// token set, or "none".
func ParseBridgeToolsSpec(spec string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(spec))
	if trimmed == "" {
		return "", errors.New("empty bridge-tools value (want read,search,write,exec or none)")
	}
	if trimmed == mode.BridgeToolsNone {
		return mode.BridgeToolsNone, nil
	}
	return agenttools.ParseToolTokens(trimmed)
}

// ModeSwitch switches the profile mode (full|clean) via the existing atomic
// switch op (mode.Switch with an explicit target = non-interactive path).
func (c *Core) ModeSwitch(target string) error {
	return c.deps.SwitchMode(target, strings.NewReader(""), io.Discard)
}

// ModeRestore restores the latest profile backup (mode.Restore op).
func (c *Core) ModeRestore() error {
	return c.deps.RestoreMode(io.Discard)
}

// ModeStatus renders the read-only profile mode status.
func (c *Core) ModeStatus(out io.Writer) error {
	return mode.Status(out)
}

// ModeBackups lists timestamped profile backups (numbered-render source).
func (c *Core) ModeBackups() ([]string, error) {
	return mode.ListBackups()
}

// ProviderAdd upserts a provider non-interactively (F-PROV). APIKeyEnv must be
// an environment-variable NAME; a key VALUE is rejected and never echoed.
func (c *Core) ProviderAdd(spec ProviderSpec) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	p := providers.Provider{
		Name:      spec.Name,
		BaseURL:   spec.BaseURL,
		APIKeyEnv: spec.APIKeyEnv,
		Models:    spec.Models,
	}
	if err := providers.UpsertProvider(reg, p); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ProviderTest probes a provider's key/endpoint via the audit path.
func (c *Core) ProviderTest(name string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	var found *providers.Provider
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			found = &reg.Providers[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("provider %q not found", providers.MaskOperand(name))
	}
	audits := c.deps.AuditKeys(&providers.Registry{Providers: []providers.Provider{*found}}, providers.AuditOptions{})
	if len(audits) == 0 {
		return fmt.Errorf("provider %q has no API key env to probe", providers.MaskOperand(name))
	}
	a := audits[0]
	switch a.Probe.Status {
	case providers.ProbeValid:
		return nil
	case providers.ProbeSkipped:
		return fmt.Errorf("provider %q: %s", providers.MaskOperand(name), a.Probe.Detail)
	default:
		return fmt.Errorf("provider %q: key probe status %s (%s)", providers.MaskOperand(name), a.Probe.Status, a.Probe.Detail)
	}
}

// ProviderRename renames a registry entry (F-PROV). Key material untouched.
func (c *Core) ProviderRename(oldName, newName string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.RenameProvider(reg, oldName, newName); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// ProviderDelete removes a registry entry ONLY (F-PROV). It never touches env
// or registry key values and never deletes files.
func (c *Core) ProviderDelete(name string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if err := providers.DeleteProvider(reg, name); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
}

// RunWizard runs the interactive secret-entry wizard (the only path that
// accepts raw key material — masked on echo per §2.11).
func (c *Core) RunWizard(in io.Reader, out io.Writer) error {
	return c.deps.RunWizard(in, out)
}

// DoctorDiagnose runs the doctor report. strict mirrors `agy pro doctor
// --strict`: structural failures return an error.
func (c *Core) DoctorDiagnose(out io.Writer, strict bool) error {
	return c.deps.Doctor(out, strict)
}

// DoctorFixKeys runs BOM-variant key-name repair for target ("all" or a
// provider/env list).
func (c *Core) DoctorFixKeys(target string) error {
	return c.deps.FixKeys(target)
}

// DoctorRollbackKeys restores registry key names from a backup dir/file.
func (c *Core) DoctorRollbackKeys(backupDir string) error {
	return c.deps.RollbackKeys(backupDir)
}

// LoadInventory exposes the combined model inventory to front-ends.
func (c *Core) LoadInventory(ctx context.Context) (*inventory.Inventory, error) {
	return c.deps.Inventory(ctx)
}

// SwarmPresets lists user-saved numbered seat presets (swarm.Store).
func (c *Core) SwarmPresets() ([]swarm.SavedPreset, error) {
	if c.deps.SwarmStore == nil {
		return nil, nil
	}
	return c.deps.SwarmStore.LoadPresets()
}

// SwarmSavePresets persists numbered seat presets (swarm.Store).
func (c *Core) SwarmSavePresets(presets []swarm.SavedPreset) error {
	if c.deps.SwarmStore == nil {
		return errors.New("swarm store not available")
	}
	return c.deps.SwarmStore.SavePresets(presets)
}

func (c *Core) loadRegistry() (*providers.Registry, error) {
	reg, err := c.deps.LoadReg()
	if err != nil {
		// A missing registry is a normal pre-setup state for write verbs that
		// can create the first entry (F-REG spirit); anything else is hard.
		if errors.Is(err, providers.ErrNotConfigured) || errors.Is(err, os.ErrNotExist) {
			return &providers.Registry{}, nil
		}
		return nil, err
	}
	if reg == nil {
		return &providers.Registry{}, nil
	}
	return reg, nil
}
