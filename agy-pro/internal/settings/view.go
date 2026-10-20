package settings

import (
	"fmt"
	"os"
	"strings"
	"time"

	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/realagy"
	"agypro/internal/swarm"
)

// Read model (contract §2.9): generated from the research §2 surface inventory
// (37 rows / 3 GAPs). NO front-end reads state.json / providers.json directly;
// both the M1 plugin verbs and the M2 TUI render this structure. Secrets only
// ever appear as providers.MaskSecret output (§2.11/U8).

// Item is one settings surface row.
type Item struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Value    string   `json:"value"` // current effective value; secrets ALWAYS via providers.MaskSecret
	Default  string   `json:"default,omitempty"`
	Source   string   `json:"source"`            // "env" | "state" | "registry" | "default" | "constant"
	Choices  []string `json:"choices,omitempty"` // empty = free value
	Danger   bool     `json:"danger,omitempty"`  // destructive op → y/n confirmation
	ReadOnly bool     `json:"readOnly,omitempty"`
	Note     string   `json:"note,omitempty"` // precedence/override text
	// MsgID joins the row to the i18n catalog: renderers resolve
	// view.<MsgID>.label / view.<MsgID>.note at paint time and fall back to
	// the compiled English Label/Note. View() sets it to the Item ID
	// (dynamic provider-key rows use "provider.key", whose catalog entry
	// carries the {{name}} slot). In-memory only: json:"-" keeps
	// show / show --json byte-identical.
	MsgID string `json:"-"`
}

// Section groups items into one TUI panel (P1..P6).
type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Items []Item `json:"items"`
}

// SettingsView is the full read model snapshot.
type SettingsView struct {
	Sections    []Section `json:"sections"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// FindItem returns the item with the given ID anywhere in the view.
func (v SettingsView) FindItem(id string) (Item, bool) {
	for _, s := range v.Sections {
		for _, it := range s.Items {
			if it.ID == id {
				return it, true
			}
		}
	}
	return Item{}, false
}

// SectionByID returns the section with the given ID.
func (v SettingsView) SectionByID(id string) (Section, bool) {
	for _, s := range v.Sections {
		if s.ID == id {
			return s, true
		}
	}
	return Section{}, false
}

// ItemIDs lists every surface ID in the view (D2 coverage source).
func (v SettingsView) ItemIDs() []string {
	var ids []string
	for _, s := range v.Sections {
		for _, it := range s.Items {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// Bridge budget constants (read-only display of bridge_tools.go:35-50).
const (
	budgetIterations       = 8
	budgetWallSeconds      = 240
	budgetTurnSeconds      = 60
	budgetToolSeconds      = 30
	budgetToolCallsPerTurn = 16
)

// View builds the read model for BOTH front-ends (contract §2.9).
func (c *Core) View() (SettingsView, error) {
	view := SettingsView{GeneratedAt: time.Now()}

	st, err := c.deps.LoadState()
	if err != nil {
		return SettingsView{}, fmt.Errorf("load state: %w", err)
	}
	reg, regErr := c.loadRegistry()
	if regErr != nil {
		// Blind-qa gate D: a hard registry load failure (e.g. the round-6
		// control-rune guard rejecting a disk-borne ESC byte in a provider
		// name) must surface as the same clean, masked error the model verbs
		// emit, and fail the command - never panic on the nil registry in
		// sectionProviders. loadRegistry only returns an error for a hard
		// failure (missing/unconfigured registries degrade to an empty
		// registry with a nil error), and the loader error names the field
		// class only, never the offending value.
		return SettingsView{}, fmt.Errorf("load providers registry: %w", regErr)
	}

	view.Sections = append(view.Sections, c.sectionLaunch(st))
	view.Sections = append(view.Sections, c.sectionProviders(reg, regErr))
	view.Sections = append(view.Sections, c.sectionBridge(st))
	view.Sections = append(view.Sections, c.sectionSwarm(st))
	view.Sections = append(view.Sections, sectionDiagnostics())
	view.Sections = append(view.Sections, c.sectionMaintenance())
	view.Sections = append(view.Sections, sectionEnvDocs(st))
	view.Sections = append(view.Sections, sectionAppearance(st))

	// MsgID join (plan-v2 §3 extraction point 1): every row carries its
	// catalog key stem for the renderer's view.<MsgID>.label/.note lookup.
	// Rows that already set MsgID (the dynamic provider-key rows use
	// "provider.key") keep their value.
	for si := range view.Sections {
		for ii := range view.Sections[si].Items {
			if view.Sections[si].Items[ii].MsgID == "" {
				view.Sections[si].Items[ii].MsgID = view.Sections[si].Items[ii].ID
			}
		}
	}
	return view, nil
}

// P1 Launch & Profile (research §2.1, §2.2 rows 1-11).
func (c *Core) sectionLaunch(st *mode.State) Section {
	profile := st.EffectiveDefaultProfile()

	items := []Item{
		{
			ID: "default_profile", Label: "Default Launch Profile",
			Value: profile, Default: mode.ProfilePrompt, Source: "state",
			Choices: []string{mode.ProfilePrompt, mode.ProfileNative, mode.ProfileGateway},
			Note:    "prompt = fast launcher on plain agy; native = straight to native Gemini; gateway = local LLM bridge",
		},
		{
			ID: "current_mode", Label: "Profile Mode",
			Value: st.CurrentMode, Default: mode.ModeClean, Source: "state",
			Choices: []string{mode.ModeFull, mode.ModeClean}, Danger: true,
			Note: "full = orchestrator rules + agent skills/plugins; clean = minimal baseline. Switching backs up the config dir first.",
		},
		{
			ID: "managed_items", Label: "Managed Items (profile switch set)",
			Value: strings.Join(st.ManagedItems, ", "), Default: strings.Join(mode.DefaultManagedItems, ", "),
			Source: "state", ReadOnly: true,
			Note: "moved to a timestamped backup on every mode switch; never deleted",
		},
		{
			ID: "last_backup", Label: "Last Profile Backup",
			Value: emptyDash(st.LastBackup), Source: "state", ReadOnly: true,
			Note: "restore with the mode.restore operation",
		},
		{
			ID: "updated_at", Label: "State Updated At",
			Value: formatTime(st.UpdatedAt), Source: "state", ReadOnly: true,
		},
		{
			ID: "config.show", Label: "Config Read View",
			Value:  fmt.Sprintf("profile=%s mode=%s bridge-tools=%s", profile, st.CurrentMode, st.EffectiveBridgeTools()),
			Source: "state", ReadOnly: true,
			Note: "same content as `agy pro config show`; this panel is the settings view",
		},
		{
			ID: "mode.switch", Label: "Switch Profile Mode",
			Value: "full | clean", Source: "state", Danger: true, Choices: []string{mode.ModeFull, mode.ModeClean},
			Note: "atomic switch + timestamped backup (mode.Switch)",
		},
		{ID: "mode.restore", Label: "Restore Latest Profile Backup", Value: emptyDash(st.LastBackup), Source: "state", Danger: true,
			Note: "pre-restore snapshot first (mode.Restore)"},
		{ID: "mode.backups", Label: "List Profile Backups", Value: "(numbered list)", Source: "state", ReadOnly: false,
			Note: "numbered backup listing (`agy pro mode list`)"},
		{ID: "mode.status", Label: "Profile Mode Status", Value: st.CurrentMode, Source: "state", ReadOnly: true,
			Note: "read-only status (`agy pro mode status`)"},
		{
			ID: "env.AGY_LAUNCHER", Label: "AGY_LAUNCHER=0 (skip launcher)",
			Value: envOr("AGY_LAUNCHER", "(unset)"), Default: "(unset)", Source: "env", ReadOnly: true,
			Note: "when set to 0 the fast launcher is bypassed (decide.go)",
		},
	}
	return Section{ID: "p1", Title: "[1] Launch & Profile", Items: items}
}

// P2 Models & Providers (research §2.3 rows 12-20; F-PROV GAP rows 15-17).
func (c *Core) sectionProviders(reg *providers.Registry, regErr error) Section {
	items := []Item{
		{ID: "providers.list", Label: "List Providers", Value: fmt.Sprintf("%d registered", len(reg.Providers)), Source: "registry",
			Note: "providers + model capabilities (`agy pro status`, `agy pro models`)"},
		{ID: "providers.add_interactive", Label: "Add/Update Provider (interactive wizard)", Value: "agy pro setup", Source: "registry",
			Note: "the ONLY secret-entry path: raw key input echo is masked first-5 + **** (§2.11)"},
		{ID: "providers.add_json", Label: "Add/Update Provider (non-interactive)", Value: "add-json <file|->", Source: "registry",
			Note: "APIKeyEnv = environment-variable NAME only; a key VALUE is rejected (F-PROV)"},
		{ID: "providers.test", Label: "Test Provider (live probe)", Value: "test <name>", Source: "registry",
			Note: "probe via the AuditKeys path (5s, masked)"},
		{ID: "providers.rename", Label: "Rename Provider", Value: "rename <old> <new>", Source: "registry",
			Note: "registry-entry rename only; key material untouched (F-PROV)"},
		{ID: "providers.delete", Label: "Delete Provider", Value: "delete <name>", Source: "registry", Danger: true,
			Note: "registry-entry removal ONLY - never touches env/registry key values, never deletes files (F-PROV)"},
		{ID: "models.inventory", Label: "Combined Model Inventory", Value: "(native + external)", Source: "registry", ReadOnly: true,
			Note: "run `agy pro models --all` for the numbered combined view (contract §2.5)"},
		{ID: "providers.presets", Label: "Provider Presets (catalog)", Value: presetCatalog(), Source: "constant", ReadOnly: true,
			Note: "built-in base URL + key env defaults; charm is a user-registered custom provider (F-PRESET)"},
		{ID: "providers.model_capabilities", Label: "Model Capability Fields", Value: "family, contextLength, maxOutputTokens, supportsTools, supportsThinking, supportsVision", Source: "registry", ReadOnly: true,
			Note: "defaults: context 1048576, maxOut 131072, tools read_file,list_dir,grep,glob, reasoning default"},
		{ID: "providers.key_policy", Label: "Registry Key Policy", Value: "keys live in env, never in providers.json", Source: "constant", ReadOnly: true,
			Note: "providers.json is written 0600 atomic; display values use first-5 + **** masking"},
	}
	if regErr != nil && !os.IsNotExist(regErr) {
		items = append(items, Item{ID: "providers.registry_error", Label: "Registry Load Error", Value: regErr.Error(), Source: "registry", ReadOnly: true})
	}
	for _, p := range reg.Providers {
		status := "(no key env)"
		// B4 display defense: a legacy registry can hold a credential in the
		// name or env-name slot (short env-name-shaped values were accepted as
		// apiKeyEnv before the LookupEnv-or-catalog guard). The provider NAME is
		// a registry slot, so it renders through providers.MaskSlot (the one
		// registry-slot display authority: its operand stage first, then the
		// shared policy's strip/classify, so a decorated or underscore-headed
		// key shape masks rather than ride MaskOperand's classification gaps).
		// The env-var NAME and the key value keep their own maskers. Nothing
		// renders raw.
		envName := providers.MaskOperand(p.APIKeyEnv)
		if p.APIKeyEnv != "" {
			val := p.KeyValue()
			if val == "" {
				status = fmt.Sprintf("env %s: (not set)", envName)
			} else {
				status = fmt.Sprintf("env %s: %s", envName, providers.MaskSecret(val))
			}
		}
		items = append(items, Item{
			ID:     "provider.key." + providers.MaskSlot(p.Name),
			MsgID:  "provider.key", // catalog entry view.provider.key.label carries {{name}}
			Label:  "Key status - " + providers.MaskSlot(p.Name),
			Value:  status,
			Source: "registry", ReadOnly: true,
			Note: "resolved from process env then persistent stores; values never shown beyond the mask",
		})
	}
	return Section{ID: "p2", Title: "[2] Models & Providers", Items: items}
}

// P3 Bridge Tools & Budgets (research §2.1 row 7, §2.5 budgets).
func (c *Core) sectionBridge(st *mode.State) Section {
	tools := st.EffectiveBridgeTools()
	source := "state"
	if v := strings.TrimSpace(os.Getenv("AGY_PRO_BRIDGE_TOOLS")); v != "" {
		source = "env"
		tools = v
	}
	items := []Item{
		{
			ID: "bridge_tools", Label: "Bridge Tools Allow-List",
			Value: tools, Default: mode.DefaultBridgeTools, Source: source,
			Choices: []string{"read,search", "read,search,write", "read,search,exec", "read,search,write,exec", mode.BridgeToolsNone},
			Note:    "capability tokens read,search,write,exec or none; AGY_PRO_BRIDGE_TOOLS overrides the stored value at run time",
		},
		{ID: "env.AGY_PRO_BRIDGE_TOOLS", Label: "AGY_PRO_BRIDGE_TOOLS Override", Value: envOr("AGY_PRO_BRIDGE_TOOLS", "(unset)"), Source: "env", ReadOnly: true,
			Note: "precedence: env > stored state > default read,search"},
		{ID: "budget.iterations", Label: "Budget: Iterations per Request", Value: fmt.Sprintf("%d", budgetIterations), Default: fmt.Sprintf("%d", budgetIterations), Source: "constant", ReadOnly: true,
			Note: "compile-time constant (bridge_tools.go) - not configurable"},
		{ID: "budget.wall", Label: "Budget: Wall Clock per Loop", Value: fmt.Sprintf("%ds", budgetWallSeconds), Default: fmt.Sprintf("%ds", budgetWallSeconds), Source: "constant", ReadOnly: true,
			Note: "compile-time constant - not configurable"},
		{ID: "budget.turn", Label: "Budget: Per Upstream Turn", Value: fmt.Sprintf("%ds", budgetTurnSeconds), Default: fmt.Sprintf("%ds", budgetTurnSeconds), Source: "constant", ReadOnly: true,
			Note: "compile-time constant - not configurable"},
		{ID: "budget.tool", Label: "Budget: Per Tool Execution", Value: fmt.Sprintf("%ds", budgetToolSeconds), Default: fmt.Sprintf("%ds", budgetToolSeconds), Source: "constant", ReadOnly: true,
			Note: "compile-time constant - not configurable"},
		{ID: "budget.tool_calls_per_turn", Label: "Budget: Tool Calls per Turn", Value: fmt.Sprintf("%d", budgetToolCallsPerTurn), Default: fmt.Sprintf("%d", budgetToolCallsPerTurn), Source: "constant", ReadOnly: true,
			Note: "compile-time constant - not configurable"},
		{ID: "budget.fallback", Label: "Budget: Fallback on Exhaustion", Value: "fixed apology line (never textless)", Source: "constant", ReadOnly: true,
			Note: "bridge_tools.go apology constant"},
	}
	return Section{ID: "p3", Title: "[3] Bridge Tools & Budgets", Items: items}
}

// P4 Swarm (research §2.4 rows 21-30; §2.3 concurrency; §U3 presets).
func (c *Core) sectionSwarm(st *mode.State) Section {
	items := []Item{
		{
			ID: "native_seat_concurrency", Label: "Native Seat Concurrency",
			Value: fmt.Sprintf("%d", st.EffectiveNativeSeatConcurrency()), Default: fmt.Sprintf("%d", mode.DefaultNativeSeatConcurrency),
			Source: "state", Choices: []string{"1", "2", "3", "4", "5", "6", "7", "8"},
			Note: "cap on simultaneous native (real-agy) fanout seats; clamped 1..8 (contract §2.3)",
		},
		{ID: "fanout.n", Label: "Fanout Default Seats (--n)", Value: "3", Default: "3", Source: "constant", ReadOnly: true,
			Note: "CLI flag truth (fanout/cli.go); flags remain the power-user/CI path"},
		{ID: "fanout.timeout", Label: "Fanout Wall Timeout (--timeout)", Value: fmt.Sprintf("%ds", fanout.DefaultTimeoutSec), Default: fmt.Sprintf("%ds", fanout.DefaultTimeoutSec), Source: "constant", ReadOnly: true,
			Note: "CLI flag truth (fanout/cli.go); the shared provider client's per-request timeout is derived from this budget (never shorter, 120s floor)"},
		{ID: "fanout.max_turns", Label: "Fanout Max Turns (--max-turns)", Value: "10", Default: "10", Source: "constant", ReadOnly: true,
			Note: "CLI flag truth"},
		{ID: "fanout.tools", Label: "Fanout Tools (--tools)", Value: "read,search", Default: "read,search", Source: "constant", ReadOnly: true,
			Note: "CLI flag truth; closed set read,search,write,exec"},
		{ID: "fanout.task", Label: "Fanout Task (--task)", Value: "review", Default: "review", Source: "constant", ReadOnly: true,
			Note: "review|security|plan-audit|research|ideas|custom"},
		{ID: "fanout.flags", Label: "Fanout Other Flags", Value: "--root . / --out ./reports/fanout/<ts> / --models \"\" / --json / --setup", Source: "constant", ReadOnly: true,
			Note: "CLI truths surfaced here so nothing is reachable only via flags (D2)"},
		{ID: "fanout.registry_defaults", Label: "Registry Defaults Block", Value: "consumed (read-only)", Default: "n=3 timeout=120 maxTurns=10 tools=read,search", Source: "registry", ReadOnly: true,
			Note: "D4: consumed as the rung below presets (CLI flag > preset via flag/AGY_PRO_FANOUT_PRESET/default key > providers.json defaults > engine default); read-only, written only via the M4 approval modal; drift from engine defaults is surfaced per run"},
		{ID: "fanout.consensus", Label: "Reconciliation Consensus", Value: ">= 2 models agreed = CONFIRMED", Source: "constant", ReadOnly: true,
			Note: "fanout reconcile threshold"},
		{ID: "fanout.failover", Label: "Failover Policy", Value: "3-step (same model/other provider → other family → degraded seat)", Source: "constant", ReadOnly: true,
			Note: "k3 family is excluded from AUTO-seating (pool policy)"},
		{ID: "swarm.k3_exclusion", Label: "k3 Pool Exclusion", Value: "active (k3 models never auto-seated)", Source: "constant", ReadOnly: true,
			Note: "explicit --models/--seats picks are exempt; rotation and failover skip k3 (§2.5b)"},
		{ID: "swarm.templates", Label: "Swarm Templates", Value: templateCatalog(), Source: "constant", ReadOnly: true,
			Note: "code-review / security-audit / custom (swarm.Templates)"},
	}
	presets := c.swarmPresetsValue()
	items = append(items, Item{
		ID: "swarm.presets", Label: "Saved Seat Presets (p1/p2/p3 + user)",
		Value: presets, Source: "registry",
		Note: "numbered selection text (e.g. 1,3,19,23) stored in ~/.agy-pro/swarm-state.json; defaults p1/p2/p3 resolve against the live inventory",
	})
	items = append(items, Item{
		ID: "swarm.save_preset", Label: "Save Seat Preset", Value: "save preset <name> <numbers>", Source: "registry",
		Note: "stores a numbered seat selection via swarm.Store",
	})
	return Section{ID: "p4", Title: "[4] Swarm & Fanout", Items: items}
}

// P5 Diagnostics & Keys (research §2.6 rows 31-34).
func sectionDiagnostics() Section {
	return Section{ID: "p5", Title: "[5] Diagnostics & Keys", Items: []Item{
		{ID: "doctor.diagnose", Label: "Run Doctor", Value: "diagnose [--strict]", Source: "constant",
			Note: "real agy binary, shim PATH location, state/dirs, provider-key probes (masked); --strict gates structural failures only"},
		{ID: "doctor.fix_keys", Label: "Repair Key Names (--fix-keys)", Value: "fix-keys [targets]", Source: "constant", Danger: true,
			Note: "BOM-variant key-name repair; writes a backup and prints the rollback dir"},
		{ID: "doctor.rollback_keys", Label: "Rollback Key Repair", Value: "rollback-keys <backup-dir>", Source: "constant", Danger: true,
			Note: "restores registry key names from the backup written by fix-keys"},
		{ID: "doctor.exit_semantics", Label: "Doctor Exit Semantics", Value: "--strict exit 1 on structural failure only", Source: "constant", ReadOnly: true,
			Note: "key-probe verdicts are informational (N-3) and never gate the exit code"},
	}}
}

// P6 Install & Update (research §2.6 rows 35-37).
func (c *Core) sectionMaintenance() Section {
	shimPath := "(unknown)"
	if self, err := os.Executable(); err == nil {
		shimPath = self
	}
	realAgy := "(not found)"
	if p, err := realagy.Resolve(); err == nil {
		realAgy = p
	}
	version := "(set by CLI)"
	if c.deps.VersionFunc != nil {
		version = c.deps.VersionFunc()
	}
	items := []Item{
		{ID: "version.info", Label: "Shim Version", Value: version, Source: "constant", ReadOnly: true,
			Note: "agy-pro Go shim & swarm orchestrator (`agy pro version`)"},
		{ID: "version.shim_path", Label: "Shim Path", Value: shimPath, Source: "constant", ReadOnly: true},
		{ID: "version.real_agy", Label: "Real agy Path", Value: realAgy, Source: "constant", ReadOnly: true,
			Note: "authentic Antigravity CLI resolved from AGY_PRO_REAL_AGY / PATH"},
		{ID: "update.info", Label: "Update", Value: "agy update", Source: "constant", ReadOnly: true,
			Note: "update is a real-agy subcommand (\"Update CLI\"); run it in your terminal - the shim delegates"},
		{ID: "uninstall.info", Label: "Uninstall", Value: "uninstall.cmd [-KeepState|-DryRun|-RestorePath|-Force]", Source: "constant", ReadOnly: true,
			Note: "recycle-bin only; run `agy pro mode restore` first if profile backups exist (agy-pro/README.md)"},
		{ID: "install.info", Label: "Install", Value: "install.cmd / scripts/install.ps1", Source: "constant", ReadOnly: true,
			Note: "deploys to ~/.agy-pro/bin/agy.exe, prepends PATH, gates on doctor"},
	}
	return Section{ID: "p6", Title: "[6] Install & Update", Items: items}
}

// sectionEnvDocs is the environment-variable docs panel (research §2.7).
func sectionEnvDocs(st *mode.State) Section {
	_ = st
	return Section{ID: "p7", Title: "[7] Environment Variables", Items: []Item{
		{ID: "env.AGY_PRO_HOME", Label: "AGY_PRO_HOME", Value: envOr("AGY_PRO_HOME", "(unset → ~/.agy-pro)"), Source: "env", ReadOnly: true,
			Note: "overrides the agy-pro home directory (paths.go)"},
		{ID: "env.AGY_PRO_PROVIDERS", Label: "AGY_PRO_PROVIDERS", Value: envOr("AGY_PRO_PROVIDERS", "(unset → ~/.agy-pro/providers.json)"), Source: "env", ReadOnly: true,
			Note: "overrides the providers registry path"},
		{ID: "env.AGY_PRO_REAL_AGY", Label: "AGY_PRO_REAL_AGY", Value: envOr("AGY_PRO_REAL_AGY", "(unset → PATH scan)"), Source: "env", ReadOnly: true,
			Note: "absolute path to the real agy binary"},
		{ID: "env.AGY_PRO_ACTIVE", Label: "AGY_PRO_ACTIVE", Value: envOr("AGY_PRO_ACTIVE", "(unset)"), Source: "env", ReadOnly: true,
			Note: "recursion guard: set to 1 in shim-spawned children"},
		{ID: "env.GEMINI_CONFIG_DIR", Label: "GEMINI_CONFIG_DIR", Value: envOr("GEMINI_CONFIG_DIR", "(unset → ~/.gemini/config)"), Source: "env", ReadOnly: true,
			Note: "overrides the Gemini config directory managed by profile switches"},
		{ID: "env.AGY_PRO_WORKSPACE", Label: "AGY_PRO_WORKSPACE", Value: envOr("AGY_PRO_WORKSPACE", "(unset)"), Source: "env", ReadOnly: true,
			Note: "workspace root passed to the bridge tool loop"},
		{ID: "env.AGY_PRO_BRIDGE", Label: "AGY_PRO_BRIDGE / AGY_BRIDGE (legacy)", Value: envOr("AGY_PRO_BRIDGE", "(unset)"), Source: "env", ReadOnly: true,
			Note: "legacy bridge activation flags (launcher.go)"},
	}}
}

func (c *Core) swarmPresetsValue() string {
	presets, err := c.SwarmPresets()
	if err != nil {
		return "(none: " + err.Error() + ")"
	}
	if len(presets) == 0 {
		return "(none saved; defaults p1/p2/p3 always available)"
	}
	var parts []string
	for _, p := range presets {
		parts = append(parts, fmt.Sprintf("%s=%s", p.Name, p.Numbers))
	}
	return strings.Join(parts, ", ")
}

func presetCatalog() string {
	var names []string
	for _, h := range providers.KnownProviders() {
		names = append(names, h.Name)
	}
	return strings.Join(names, ", ")
}

func templateCatalog() string {
	var names []string
	for _, t := range swarm.Templates() {
		names = append(names, t.ID)
	}
	return strings.Join(names, ", ")
}

func envOr(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "(never)"
	}
	return t.Format("2006-01-02 15:04:05 MST")
}

// languageNativeNames returns the native display name of every UI language
// in the catalog's canonical order (en first). The names are code constants
// in internal/i18n (plan §3: every language can read every selector row),
// not catalog entries, so the row resolves them without loading a bundle.
func languageNativeNames() []string {
	metas := i18n.Languages()
	names := make([]string, 0, len(metas))
	for _, m := range metas {
		names = append(names, m.Endonym)
	}
	return names
}

// P8 Appearance (plan §4/§5): the persisted UI language and the
// launcher-menu toggle. Labels/Notes stay English literals as the compiled
// default (plan §3 extraction point 1: show/show --json stay byte-identical);
// View() stamps MsgID on every row and the new TUI screen resolves
// view.<MsgID>.label/.note through the catalog at paint time.
func sectionAppearance(st *mode.State) Section {
	lang := st.EffectiveLanguage()
	langSource := "state.json"
	if strings.TrimSpace(st.Language) == "" {
		langSource = "default: English"
	}
	items := []Item{
		{
			ID: "appearance.language", Label: "Language",
			Value: i18n.Endonym(lang), Default: i18n.Endonym(mode.DefaultLanguage),
			Source: langSource, Choices: languageNativeNames(),
			Note: "UI language (closed 12-code set); written via Core.Set language",
		},
	}

	// AGY_LAUNCHER non-empty wins entirely (opt-in semantics: only "1"
	// shows, anything else hides); env unset/empty -> stored toggle
	// decides; no stored value -> off. Editing the stored toggle has no
	// effect while the env override is set.
	stored := st.EffectiveLauncherMenu()
	launcher := Item{
		ID: "appearance.launcher_menu", Label: "Launcher menu",
		Value: "hide", Default: "hide", Choices: []string{"show", "hide"},
	}
	if v := strings.TrimSpace(os.Getenv("AGY_LAUNCHER")); v != "" {
		launcher.Value = "hide"
		if v == "1" {
			launcher.Value = "show"
		}
		launcher.Source = "env AGY_LAUNCHER (override)"
		launcher.Note = "env wins over the stored toggle; editing the stored toggle has no effect until the env is cleared"
	} else if stored != "" {
		launcher.Value = "hide"
		if stored == mode.LauncherMenuOn {
			launcher.Value = "show"
		}
		launcher.Source = "state.json"
	} else {
		launcher.Source = "default (off)"
	}
	items = append(items, launcher)
	return Section{ID: "p8", Title: "[8] Appearance", Items: items}
}
