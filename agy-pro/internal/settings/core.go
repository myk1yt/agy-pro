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
	"agypro/internal/integrations"
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

// Set writes one settings key. Supported keys (contract §2.9, plus the
// appearance keys from plan §4/§5, plus the P5-3 integrations intent keys):
// default-profile | bridge-tools | native-seat-concurrency | language |
// launcher_menu | integration-preset | integration-statusline |
// integration-hook | integration-rules | integration-plugins |
// integration-skills. Every key accepts the hyphen and the underscore spelling.
func (c *Core) Set(key, value string) error {
	st, err := c.deps.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	normKey := strings.ToLower(strings.TrimSpace(key))
	switch normKey {
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
	case "language":
		lang := strings.TrimSpace(value)
		if !mode.ValidLanguage(lang) {
			// B2/U8: operands may be misdirected key pastes; mask before echo.
			return fmt.Errorf("invalid language %q (want one of: en, zh-Hans, zh-Hant, ko, ja, ru, hi, de, fr, it, vi, ar)", providers.MaskOperand(value))
		}
		st.Language = mode.NormalizeLanguage(lang)
	case "launcher_menu", "launcher-menu":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "on", "1", "true":
			st.LauncherMenu = mode.LauncherMenuOn
		case "off", "0", "false":
			st.LauncherMenu = mode.LauncherMenuOff
		default:
			// B2/U8: operands may be misdirected key pastes; mask before echo.
			return fmt.Errorf("invalid launcher_menu %q (use on, off, 1, or 0)", providers.MaskOperand(value))
		}
	case "integration-preset", "integration_preset":
		// P5-3 T6 (pick 3, INTENT half): the composed preset is validated
		// through the shared internal/integrations vocabulary, so the store
		// never keeps a second copy of the three preset words.
		preset, err := integrations.ParsePreset(value)
		if err != nil {
			// B2/U8: operands may be misdirected key pastes; mask before echo.
			return fmt.Errorf("invalid integration-preset %q (use native, tools, or custom)", providers.MaskOperand(value))
		}
		st.IntegrationPreset = string(preset)
	default:
		// P5-3 T6: the five per-feature toggles are one family. The key table
		// below is closed and names the state.json row each key writes; the
		// value is canonicalized by mode.NormalizeIntegrationToggle, the same
		// helper the readers use, so the door and the readers cannot disagree.
		set, ok := integrationToggleTarget(normKey)
		if !ok {
			return fmt.Errorf("unknown settings key %q (want default-profile, bridge-tools, native-seat-concurrency, language, launcher_menu, integration-preset, integration-statusline, integration-hook, integration-rules, integration-plugins, or integration-skills)", providers.MaskOperand(key))
		}
		toggle, err := integrationToggleValue(normKey, value)
		if err != nil {
			return err
		}
		set(st, toggle)
	}
	if err := c.deps.SaveState(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// integrationToggleTarget maps one closed Core.Set per-feature key onto the
// state.json row the write sets. The accepted key set is deliberately closed
// (the canonical hyphen and underscore spellings only): deriving it from the
// key suffix would silently accept every alias the feature vocabulary carries.
// TestCoreSetIntegrationFeatureToggles pins each key's suffix to
// integrations.ParseFeature, so the two vocabularies stay one vocabulary.
func integrationToggleTarget(key string) (func(*mode.State, string), bool) {
	switch key {
	case "integration-statusline", "integration_statusline":
		return func(st *mode.State, v string) { st.IntegrationStatusline = v }, true
	case "integration-hook", "integration_hook":
		return func(st *mode.State, v string) { st.IntegrationHook = v }, true
	case "integration-rules", "integration_rules":
		return func(st *mode.State, v string) { st.IntegrationRules = v }, true
	case "integration-plugins", "integration_plugins":
		return func(st *mode.State, v string) { st.IntegrationPlugins = v }, true
	case "integration-skills", "integration_skills":
		return func(st *mode.State, v string) { st.IntegrationSkills = v }, true
	}
	return nil, false
}

// integrationToggleValue canonicalizes one per-feature on/off operand. The
// stored words are mode.IntegrationToggleOn/Off; an operand outside the
// on/off/1/0/true/false set is refused here (the readers stay tolerant, the SET
// door does not), and the operand is masked before it is echoed.
func integrationToggleValue(key, value string) (string, error) {
	toggle := mode.NormalizeIntegrationToggle(value)
	if toggle == "" {
		// B2/U8: operands may be misdirected key pastes; mask before echo.
		return "", fmt.Errorf("invalid %s %q (use on, off, 1, or 0)", key, providers.MaskOperand(value))
	}
	return toggle, nil
}

// ParseBridgeToolsSpec validates a bridge-tools value exactly like
// `agy pro config bridge-tools` (main.go parseBridgeToolsSpec): the closed
// token set, or "none".
func ParseBridgeToolsSpec(spec string) (string, error) {
	raw := strings.TrimSpace(spec)
	if strings.ToLower(raw) == mode.BridgeToolsNone {
		return mode.BridgeToolsNone, nil
	}
	if raw == "" {
		return "", errors.New("empty bridge-tools value (want read,search,write,exec or none)")
	}
	// I1: the parser echoes the token; pass the ORIGINAL-case operand so the
	// error quotes it verbatim and the seeded redactor can match (pre-folding
	// made the original-case seed inert against the case-folded echo).
	toks, err := agenttools.ParseToolTokens(raw)
	if err != nil {
		return "", errors.New(providers.MaskSentence(err.Error(), providers.FlagSeedOperands(raw)...))
	}
	return toks, nil
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
	// Every NAME operand below routes through MaskSlot, the registry-slot
	// display authority: MaskOperand is the identity on the underscore-head key
	// class, so a hand-planted "sk_live_..." name rode raw inside the failure
	// detail. Benign names render byte-identically (MaskSlot's first stage IS
	// MaskOperand); the probe detail stays on its existing unmasked form.
	if found == nil {
		return fmt.Errorf("provider %q not found", providers.MaskSlot(name))
	}
	audits := c.deps.AuditKeys(&providers.Registry{Providers: []providers.Provider{*found}}, providers.AuditOptions{})
	if len(audits) == 0 {
		return fmt.Errorf("provider %q has no API key env to probe", providers.MaskSlot(name))
	}
	a := audits[0]
	switch a.Probe.Status {
	case providers.ProbeValid:
		return nil
	case providers.ProbeSkipped:
		return fmt.Errorf("provider %q: %s", providers.MaskSlot(name), a.Probe.Detail)
	default:
		return fmt.Errorf("provider %q: key probe status %s (%s)", providers.MaskSlot(name), a.Probe.Status, a.Probe.Detail)
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

// ProviderEdit error classes. Each verb outcome is a named error so a front-end
// (the M2 TUI provider form, the M1 plugin) can branch on the reason without
// parsing text; the wrapped detail carries the (masked) sentence.
var (
	// ErrProviderEditNotFound reports that no provider carries the requested name.
	ErrProviderEditNotFound = errors.New("provider edit: provider not found")
	// ErrProviderEditAmbiguous reports that the requested name matches more than
	// one provider under case folding, so the edit target is not determinable.
	ErrProviderEditAmbiguous = errors.New("provider edit: provider name is ambiguous")
	// ErrProviderEditBaseURL reports a refused base URL (empty, malformed, or a
	// non-loopback http:// endpoint).
	ErrProviderEditBaseURL = errors.New("provider edit: baseURL refused")
	// ErrProviderEditAPIKeyEnv reports a refused apiKeyEnv operand: not a
	// plausible environment-variable NAME, or credential material.
	ErrProviderEditAPIKeyEnv = errors.New("provider edit: apiKeyEnv refused")
)

// ProviderEdit rewrites an EXISTING provider's two endpoint fields in place:
// BaseURL and APIKeyEnv. Every other field of the record is left byte-identical,
// above all Models. That is the whole reason this verb exists instead of a
// second ProviderAdd: UpsertProvider REPLACES a same-named provider with the
// record handed to it (registry_ops.go:118-124), so a caller that passes only
// the two edited fields silently drops that provider's models. ProviderEdit
// never calls UpsertProvider and never builds a replacement provider record; it
// mutates reg.Providers[idx] field by field and saves through the same seam as
// ProviderAdd/ProviderRename/ProviderDelete (deps.SaveReg -> saveRegistryFile ->
// providers.WriteFileAtomic: whole file, 0600, temp file + rename).
//
// APIKeyEnv carries an environment-variable NAME. A key VALUE pasted into the
// slot is refused by the same guard the add-json twin and UpsertProvider use
// (providers.ValidateAPIKeyEnvName: charset, then the name must prove itself via
// the process environment, the known env-name catalog, or the short UPPER_SNAKE
// rule, then credential shapes are rejected). No operand is ever echoed: every
// sentence renders through providers.MaskSentence, and the guard's own messages
// are value-free by construction. Nothing is resolved, logged, or persisted but
// the NAME. An empty apiKeyEnv is accepted and clears the slot (a provider that
// needs no key), matching the twin's optional field. Both operands are trimmed
// first because the add-json twin trims them (spec.go:33-43); nothing else is
// normalized: a trailing slash, a path prefix, or an explicit port survives
// verbatim.
//
// Validation mirrors the CLI twin (cmd/agy/pro_settings.go add-json ->
// settings.ParseProviderSpecJSON -> providers.UpsertProvider): the base-URL
// transport rules and the apiKeyEnv control/charset rules are the registry's own
// load-time validators, applied here by validating the mutated registry
// (Registry.Validate -> validateBaseURL / envNameRe; providers.go:296-331,
// 388-396), and the env-name proof rule is providers.ValidateAPIKeyEnvName. No
// rule is added, relaxed, or re-ordered relative to the twin. The mutation is
// applied in two steps so a refusal can name the offending field: baseURL is
// set and validated first, then apiKeyEnv. Classification relies on the target
// record having loaded clean (providers.Load -> LoadFrom -> Validate), so a
// first-step failure cannot be an apiKeyEnv fault.
//
// The guard applies to the RESULTING record, not only to the changed field, so
// an edit cannot launder an env name the add-json twin would refuse (a legacy
// registry entry whose apiKeyEnv only satisfies the load-time charset); the fix
// is to correct that field in the same edit.
//
// Idempotence: editing a provider to the values it already holds performs NO
// write at all (SaveReg is not called), so the file's bytes and mtime are
// untouched. Every other outcome either writes exactly once or returns before
// SaveReg, so a refused or unknown-target edit leaves the on-disk bytes
// identical. On a validation failure the two fields are restored in memory
// before returning, so the caller's registry copy is not left half-edited.
//
// Concurrency window: the registry is re-LOADED here (a stale in-memory copy is
// never trusted, same as the sibling verbs), then mutated, validated, and
// written as a whole file. A write to providers.json that lands between this
// load and the rename is overwritten by this one: last writer wins, and there is
// no lock. Agreement with an EARLIER read (the TUI form's prefill) is the
// caller's concern, not this verb's; the approval diff a front-end shows must be
// re-checked against the state it described if the user can pause there.
func (c *Core) ProviderEdit(name, baseURL, apiKeyEnv string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	idx, err := ResolveProviderEditTarget(reg, name)
	if err != nil {
		return err
	}
	baseURL = strings.TrimSpace(baseURL)
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if baseURL == "" {
		return fmt.Errorf("%w: baseURL is required", ErrProviderEditBaseURL)
	}
	if apiKeyEnv != "" {
		if err := providers.ValidateAPIKeyEnvName(apiKeyEnv); err != nil {
			return fmt.Errorf("%w: %s", ErrProviderEditAPIKeyEnv, providers.MaskSentence(err.Error(), apiKeyEnv))
		}
	}
	p := &reg.Providers[idx]
	if p.BaseURL == baseURL && p.APIKeyEnv == apiKeyEnv {
		return nil
	}
	oldBaseURL, oldAPIKeyEnv := p.BaseURL, p.APIKeyEnv
	p.BaseURL = baseURL
	if err := reg.Validate(); err != nil {
		p.BaseURL, p.APIKeyEnv = oldBaseURL, oldAPIKeyEnv
		return fmt.Errorf("%w: %s", ErrProviderEditBaseURL, providers.MaskSentence(err.Error(), baseURL, apiKeyEnv))
	}
	p.APIKeyEnv = apiKeyEnv
	if err := reg.Validate(); err != nil {
		p.BaseURL, p.APIKeyEnv = oldBaseURL, oldAPIKeyEnv
		return fmt.Errorf("%w: %s", ErrProviderEditAPIKeyEnv, providers.MaskSentence(err.Error(), baseURL, apiKeyEnv))
	}
	return c.deps.SaveReg(reg)
}

// ResolveProviderEditTarget locates the provider an edit applies to and reports
// its index in reg.Providers.
//
// ProviderDelete and RenameProvider both take the FIRST case-insensitive match
// (registry_ops.go:152-161 and 187-192). Registry.Validate's duplicate check is
// exact-case (providers.go:360-368), so one file may legally hold "acme" and
// "ACME" side by side; under a first-match rule an edit of "acme" rewrites
// whichever of the two sits earlier in the array, i.e. a live provider's
// endpoint and key variable. An edit moves credential routing, so this verb
// refuses to guess: an exact-case match wins outright (the registry cannot hold
// two of those), and a case-folded match is accepted only when it is the single
// one. Anything else fails with ErrProviderEditAmbiguous before any mutation.
//
// Exported because the identity rule must be ONE rule. Core.ProviderEdit is not
// the only resolver on the path to a write: the TUI provider form resolves the
// target twice more (to prefill, and to re-check the form-open snapshot against
// the on-disk record before the approved write). A front-end that resolved with
// a different rule (a first-case-insensitive match) could refuse a legal edit or,
// when the case duplicate mirrors the snapshot's endpoint fields, pass its
// stale-base gate while the verb writes the OTHER record's values over the real
// target. Every caller now resolves through this function, so the verb and the
// gates in front of it cannot disagree about which record a name addresses.
func ResolveProviderEditTarget(reg *providers.Registry, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return -1, fmt.Errorf("%w: a provider name is required", ErrProviderEditNotFound)
	}
	for i := range reg.Providers {
		if reg.Providers[i].Name == name {
			return i, nil
		}
	}
	idx := -1
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			if idx >= 0 {
				return -1, fmt.Errorf("%w: %s matches more than one provider under case folding (use the exact spelling)", ErrProviderEditAmbiguous, providers.MaskSlot(name))
			}
			idx = i
		}
	}
	if idx < 0 {
		return -1, fmt.Errorf("%w: %s", ErrProviderEditNotFound, providers.MaskSlot(name))
	}
	return idx, nil
}

// ExcludeModelsBuiltinRule is the one spelling every surface gives the built-in
// default policy: the built-in rule is a PREDICATE (family "k3" or a
// boundary-safe "k3" id token, providers.IsK3Family), and "family:k3" is that
// predicate written in the exclusion grammar's own family form, so it is the
// exact reproduction of the rule as a list entry. The settings row shows it when
// defaults.excludeModels is absent, the editor seeds it, and saving the seeded
// list unchanged therefore keeps the same pool.
//
// The bare spelling "k3" is NOT that reproduction: a user-list entry that names
// no family matches only a provider or a model literally named k3, which drops
// the family-declared and id-token rows from the exclusion (the first-save
// policy-weakening trap).
const ExcludeModelsBuiltinRule = "family:k3"

// ExcludeModelsSeed is the editor's starting list: the stored entries when the
// user owns the list, and the built-in default rule in its exact list spelling
// (ExcludeModelsBuiltinRule) when the key is absent, so a first save writes an
// explicit list that keeps the policy instead of starting empty or weakening it.
// The returned slice is always a copy, so an editor can mutate its draft freely.
func ExcludeModelsSeed(d providers.Defaults) []string {
	if d.ExcludeModels == nil {
		return []string{ExcludeModelsBuiltinRule}
	}
	return append([]string{}, d.ExcludeModels...)
}

// Exclusion-list write door (D1=(a): the list is providers.json
// defaults.excludeModels). Errors are named classes so a front-end branches on
// the reason without parsing text.
var (
	// ErrExcludeModelsInvalid reports a list or an entry the shared authority
	// (providers.ValidateExcludeModels) refuses: an entry empty after the trim, a
	// Cc or Cf rune, interior whitespace, a family: entry with no name, or a
	// non-nil list whose every entry is blank. One sentinel for the whole class,
	// so a front-end that branches on it cannot mis-classify the family and
	// all-blank refusals as some other failure. The wrapped detail names the
	// field class only and never the offending entry (a misdirected paste may be
	// a key).
	ErrExcludeModelsInvalid = errors.New("exclusion list: entry refused")
	// ErrExcludeModelsStale reports that defaults.excludeModels no longer
	// matches the at-open snapshot the approval diff was signed against.
	ErrExcludeModelsStale = errors.New("exclusion list: changed on disk since it was read")
)

// ExcludeModelsBase is the at-open snapshot of defaults.excludeModels: the base
// a write must still find on disk. Owned carries the nil-vs-empty distinction
// (false = the key was absent, so the built-in default policy applies; true
// with no Entries = the user owned an empty list), because the two mean
// different things and a write that cannot tell them apart would silently
// resurrect the built-in default.
type ExcludeModelsBase struct {
	Owned   bool
	Entries []string
}

// ExcludeModelsBaseFromDefaults snapshots a loaded defaults block. The entries
// are copied, so later mutation of the caller's registry cannot move the base.
func ExcludeModelsBaseFromDefaults(d providers.Defaults) ExcludeModelsBase {
	base := ExcludeModelsBase{Owned: d.ExcludeModels != nil}
	if base.Owned {
		base.Entries = append([]string(nil), d.ExcludeModels...)
	}
	return base
}

// Matches reports whether d still carries the snapshot's shape and entries, in
// order. A nil base (the zero value: the key was absent) matches only an
// absent key.
func (b ExcludeModelsBase) Matches(d providers.Defaults) bool {
	if (d.ExcludeModels != nil) != b.Owned {
		return false
	}
	if len(d.ExcludeModels) != len(b.Entries) {
		return false
	}
	for i := range b.Entries {
		if d.ExcludeModels[i] != b.Entries[i] {
			return false
		}
	}
	return true
}

// ParseExcludeModels validates and normalises a proposed list through the ONE
// exclusion-entry authority both doors share: providers.ValidateExcludeModels,
// which is the write-door half of the same code the registry loader runs
// (providers.NormalizeExcludeModels, called by Registry.Validate). Delegating
// instead of re-implementing is the point: a front-end and the next load can no
// longer disagree about which entries are legal, which is what let `family:`
// (no name), a Cf-decorated entry, or an all-blank list through this door while
// the loader refused them (S7-C2, S7-C3, S7-C9).
//
// Per entry, in the authority's order: a Cc or Cf rune anywhere in the raw entry
// is refused (U+0085 NEL is both a control and a space, so a trim-first order
// would erase it instead of refusing it), then the entry is trimmed, an empty
// result is refused, an interior whitespace rune is refused (an entry names one
// provider, one model id, provider/model, or family:<name>, all single tokens, so
// interior space can never match), and a family: entry with no name after the
// colon is refused. A non-nil list whose EVERY entry is blank is refused as a
// list, and a nil or explicit-empty list passes through with its shape intact
// (nil stays nil, `[]` stays a non-nil empty list), because the caller's
// ownership decision rides on that distinction.
//
// Nothing else is normalised: entries stay case-sensitive and keep their order.
// Every refusal wraps ErrExcludeModelsInvalid and names the field class only,
// never the offending entry (a misdirected paste may be a key).
func ParseExcludeModels(entries []string) ([]string, error) {
	out, err := providers.ValidateExcludeModels(entries)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExcludeModelsInvalid, err)
	}
	return out, nil
}

// sameExcludeEntries compares two entry lists in order. Order is part of the
// stored shape, so a reorder counts as a change.
func sameExcludeEntries(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SetExcludeModels replaces providers.json defaults.excludeModels with entries.
// It is the ONE write door for the list: the hub editor opens the M4 approval
// modal over the exact before/after diff and calls this only after y, and no
// front-end writes the field directly.
//
// Ownership: this method always writes the USER-OWNED shape. An empty (or nil)
// entries slice persists as an explicit empty list ([]), never as the absent
// key, because the caller has decided to own the list; the built-in k3 default
// therefore does NOT come back on the next load. That is the whole point of the
// D1=(a) nil-vs-empty contract and the reason Defaults.ExcludeModels carries no
// omitempty.
//
// Stale base (the model form's write-time re-verify, F-M3/CWE-367): the
// registry is loaded FRESH here and compared against base, the snapshot the
// approval diff was composed from. A list that moved while the operator was
// reading the diff fails with ErrExcludeModelsStale before any write, so an
// external edit is refused instead of silently reverted. A matched base that
// already holds exactly the requested entries performs NO write at all
// (SaveReg is not called, so the file's bytes and mtime are untouched), which
// keeps an idempotent re-approval from rewriting the file for nothing.
//
// Every other field of the registry is untouched: the mutation is one field of
// the defaults block, validated through Registry.Validate (the loader's own
// authority) and written whole-file 0600 atomic through deps.SaveReg.
func (c *Core) SetExcludeModels(entries []string, base ExcludeModelsBase) error {
	next, err := ParseExcludeModels(entries)
	if err != nil {
		return err
	}
	reg, err := c.loadRegistry()
	if err != nil {
		return err
	}
	if !base.Matches(reg.Defaults) {
		return fmt.Errorf("%w: providers.json defaults.excludeModels no longer holds the list this write was approved against", ErrExcludeModelsStale)
	}
	if base.Owned && sameExcludeEntries(base.Entries, next) {
		// The approved list is exactly what is already stored: no write at all,
		// so the file's bytes and mtime stay untouched (the ProviderEdit
		// idempotence rule).
		return nil
	}
	// A non-nil empty slice is the user-owned empty list; append into a fresh
	// zero-length slice so len(next)==0 still serialises as [].
	owned := make([]string, 0, len(next))
	owned = append(owned, next...)
	reg.Defaults.ExcludeModels = owned
	if err := reg.Validate(); err != nil {
		return err
	}
	return c.deps.SaveReg(reg)
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

// SwarmStateStore returns the on-disk swarm store behind the Core, or nil when
// the injected store is not the file-backed one (tests, embedders). It is the
// seam a front-end with its own notice surface uses to redirect the store's
// corrupt-file warning: NewCore's default store warns on os.Stderr, which the
// full-screen TUI cannot show (security review F-3), and the warning has to be
// counted by the caller's latch before the save that replaces the file. The
// accessor exposes the store, not a second copy of it, so the redirect lands on
// the instance the Core verbs actually read and write.
func (c *Core) SwarmStateStore() *swarm.FileStore {
	fs, _ := c.deps.SwarmStore.(*swarm.FileStore)
	return fs
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
