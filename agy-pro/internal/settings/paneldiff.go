package settings

// P5-1 U5a: exact-diff presentation for the legacy `agy pro settings` panel's
// persisted-state writes.
//
// User decision F3(a) (decisions.md, pick record §2): every write path to
// persisted state is presented as an exact diff before it happens, and the
// panel KEEPS WORKING with gates rather than being turned read-only (P1-7=(a)
// wording: its ungated writes are fixed by write discipline, not by removal).
// The door policy ruling puts the renderers next to the store and lets each
// door pass the lines in, so this file owns the panel's renderers and
// internal/settings/tui.go owns the y/N gate that shows them.
//
// Writers covered here (research-p51-writers.md §1.1, §1.2, §1.3, §1.7):
//
//	W1  providers.add_json -> Core.ProviderAdd     (providers.json)
//	W2  providers.rename   -> Core.ProviderRename  (providers.json)
//	W3  providers.delete   -> Core.ProviderDelete  (providers.json)
//	W32 doctor.fix_keys    -> Core.DoctorFixKeys   (HKCU\Environment + process env)
//	W33 the DPAPI backup file that rides the fix-keys op (same decision)
//	W34 doctor.rollback_keys -> Core.DoctorRollbackKeys (HKCU + process env)
//	W20 the state.json scalar choice rows -> Core.Set (U5b)
//	W24 swarm.save_preset healthy-path append -> Core.SwarmSavePresets (U5b)
//	W13 current_mode / mode.switch choice rows -> Core.ModeSwitch (U5b)
//	W15 mode.restore -> Core.ModeRestore (U5b)
//
// U5a gated the first six; U5b gated the last four, which are the same four
// writers U2 gated on the CLI side (and U3 for the repair stack), so the two
// front-ends now present the same plan shape for the same write.
//
// Line shapes are MIRRORED, not copied, from the two existing twins:
//
//   - the provider doors render the en catalog strings the hub's M4 provider
//     approvals render (settings.approval.add/replace/rename, providers.delete.body,
//     modal.approval.body/counts/diff_added/diff_removed at
//     internal/tui/screen_settings.go:695-783 and screen_providers.go:667-700).
//     The panel holds them as package consts (decision 6B documented-English
//     chrome, the LegacyPanelNotice / approvalEntriesHidden idiom: this surface
//     is not in the twelve catalogs), and TestPanelDiffWordingMatchesCatalog
//     pins every const byte-equal to the en entry so a hub wording change
//     cannot leave the panel behind.
//   - the repair doors render the probe-free plan U3 composed for the CLI
//     doors (cmd/agy/pro_settings.go keyRepairPlan.diff, itself the console
//     twin of the hub's approveFixKeys/approveRollbackKeys): one line per
//     resolved target, names only, values never.
//
// Bounded body: the repair plan is capped with the hub's bounded-entry rule
// (internal/tui/screen_settings.go capApprovalEntries + the approvalEntriesHidden
// tail sentence, mirrored for a CLI console by U3's capRepairDiffEntries). The
// panel has no frame-height ladder to fight, but an unbounded list in a
// line-mode transcript is worse than a bounded one, so the same 20-entry budget
// and the same "N more entries NOT shown" tail apply.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/swarm"
	"agypro/profiles"
)

// --- display authorities -----------------------------------------------------
//
// Every masking call goes through ONE of these three, so the panel cannot
// invent a fourth way to render a registry slot, an environment NAME, or a
// URL. providers.MaskSlot is the registry-slot authority (and its first stage
// IS MaskOperand, so benign values stay byte-identical); MaskOperand is the
// environment-NAME authority; MaskBaseURL is the layered URL mask U1 chose for
// the base-URL column because the slot masker would rewrite a benign URL while
// a credential in userinfo/query must not ride raw.

func panelSlot(v string) string    { return providers.MaskSlot(v) }
func panelOperand(v string) string { return providers.MaskOperand(v) }
func panelURL(v string) string     { return providers.MaskBaseURL(v) }

// panelCountWord renders a model count in the hub's plural form
// (menu.count.models: "1 model" / "N models").
func panelCountWord(n int) string {
	if n == 1 {
		return "1 model"
	}
	return strconv.Itoa(n) + " models"
}

// panelBodyLine is the last pass before a diff line reaches the frame: control
// runes (Cc) and the format class (Cf: bidi overrides, zero-width joiners, BOM)
// are removed through the shared policy, so a byte planted in a registry slot
// or an environment NAME can never forge the consent presentation the operator
// reads. The maskers already strip their own slots; this covers the separators
// and counts this file composes around them.
func panelBodyLine(s string) string {
	return providers.StripSlotControls(strings.ReplaceAll(s, "\n", " "))
}

func panelBody(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, panelBodyLine(ln))
	}
	return out
}

// panelDiffEntryBudget / panelDiffEntriesHidden are the bounded-entry rule: the
// entries that fit render byte-identically, a longer list is cut to them plus
// ONE tail line naming exactly how many entries are not shown.
const (
	panelDiffEntryBudget   = 20
	panelDiffEntriesHidden = "  ... %d more entries NOT shown (the diff above is partial)"
)

func capPanelEntries(entries []string) []string {
	if len(entries) <= panelDiffEntryBudget {
		return entries
	}
	visible := panelDiffEntryBudget - 1
	out := make([]string, 0, visible+1)
	out = append(out, entries[:visible]...)
	out = append(out, fmt.Sprintf(panelDiffEntriesHidden, len(entries)-visible))
	return out
}

// panelRefusal builds a refusal the panel can render: the sentence is run
// through MaskSentence seeded with the caller's per-token seeds FIRST, so a
// location-shaped operand ("./backups,<secret>") cannot ride raw through the
// token class the generic patterns do not cover. The panel's prompt wrappers
// then mask it once more with their own seeds, which is idempotent; the refusal
// is value-free by construction otherwise (only masked slots are interpolated).
func panelRefusal(format string, seeds []string, args ...any) error {
	return errors.New(providers.MaskSentence(fmt.Sprintf(format, args...), seeds...))
}

// --- provider doors (W1/W2/W3) ----------------------------------------------

// Panel-side spellings of the hub catalog entries the provider approvals show.
// The {{slot}} markers are filled with fmt.Sprintf; the en catalog values are
// the source of truth and TestPanelDiffWordingMatchesCatalog pins them.
const (
	panelAddSentence      = "Add provider %s to the registry."
	panelReplaceSentence  = "Replace provider %s wholesale: the current entry and its whole model list are discarded."
	panelRenameSentence   = "Rename provider %s to %s; the model list carries over unchanged."
	panelDeleteSentence   = "Removes the registry entry for %s only. Key values and files are untouched."
	panelCountsLine       = "Models before: %d, after: %d"
	panelAtomicLine       = "providers.json will be written (0600, atomic) with this exact diff:"
	panelAddedLine        = "+ %s"
	panelRemovedLine      = "- %s"
	panelMissingDeleteFmt = "no registry entry named %s is on disk; nothing to delete"
	panelMissingRenameFmt = "no registry entry named %s is on disk; nothing to rename"
)

// panelProviderLine is one diff-context line for a registry entry, the exact
// shape both twins render (screen_settings.go providerLine / cmd/agy
// providerDiffLine): the slot-masked name, the masked base URL, the model count.
func panelProviderLine(name, baseURL string, models int) string {
	return fmt.Sprintf("provider %s (%s, %s)", panelSlot(name), panelURL(baseURL), panelCountWord(models))
}

// panelProviderBase is the signed state of one provider write. The same struct
// covers all three verbs because their difference is which slots are signed:
//
//	add    -> name/present/record (absent = a new entry; present = replace)
//	rename -> the old slot plus the new-name slot
//	delete -> the removed record
//
// record and newRecord are DEEP copies, so mutating the live registry between
// presentation and consent cannot move the base this write is checked against.
type panelProviderBase struct {
	name       string
	present    bool
	record     providers.Provider
	newName    string
	newPresent bool
	newRecord  providers.Provider
}

// clonePanelProvider copies a record (models included) so the base cannot be
// moved by an in-place mutation of the registry it was read from.
func clonePanelProvider(p providers.Provider) providers.Provider {
	out := p
	if p.Models != nil {
		out.Models = append([]providers.Model(nil), p.Models...)
	}
	return out
}

// findPanelProvider returns the FIRST case-insensitive match, the same rule
// DeleteProvider and RenameProvider apply (registry_ops.go:234-242, 270-275),
// so the record the diff describes is the record the verb addresses.
func findPanelProvider(reg *providers.Registry, name string) *providers.Provider {
	if reg == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return &reg.Providers[i]
		}
	}
	return nil
}

// samePanelProviderRecord compares two records field by field through a deep
// comparison, so a field added to providers.Provider later is signed without
// touching this gate (the rule screen_providers.go sameProviderRecord and
// cmd/agy sameProviderRecord both use).
func samePanelProviderRecord(a, b providers.Provider) bool { return reflect.DeepEqual(a, b) }

// panelProviderBaseFor composes the base an add or delete signs: the record for
// name, or the absent slot. The registry read goes through Core.loadRegistry, so
// the presentation reads exactly the seam the write reads (a missing registry
// file is the normal pre-setup state and yields an empty registry).
func (c *Core) panelProviderBaseFor(name string) (panelProviderBase, error) {
	reg, err := c.loadRegistry()
	if err != nil {
		return panelProviderBase{}, err
	}
	b := panelProviderBase{name: strings.TrimSpace(name)}
	if p := findPanelProvider(reg, b.name); p != nil {
		b.present = true
		b.record = clonePanelProvider(*p)
	}
	return b, nil
}

// panelProviderBaseForRename composes the two slots a rename signs: the old
// record and the current occupant of the new name.
func (c *Core) panelProviderBaseForRename(oldName, newName string) (panelProviderBase, error) {
	b, err := c.panelProviderBaseFor(oldName)
	if err != nil {
		return panelProviderBase{}, err
	}
	newName = strings.TrimSpace(newName)
	reg, err := c.loadRegistry()
	if err != nil {
		return panelProviderBase{}, err
	}
	b.newName = newName
	if p := findPanelProvider(reg, newName); p != nil {
		b.newPresent = true
		b.newRecord = clonePanelProvider(*p)
	}
	return b, nil
}

// panelSlotDrift compares one signed provider slot against a freshly loaded
// registry. present tells which way the slot was signed: an entry that was on
// disk must still be the same record, and an entry that was absent must still be
// absent (an entry created while the operator read the diff would otherwise be
// replaced or removed under a diff rendered before it existed).
func panelSlotDrift(reg *providers.Registry, name string, present bool, record providers.Provider, verb string) error {
	p := findPanelProvider(reg, name)
	if !present {
		if p != nil {
			return fmt.Errorf("provider %s appeared in providers.json between the presentation and the write, so the presented diff is stale; nothing written (re-run %s after reviewing the current registry)", panelSlot(name), verb)
		}
		return nil
	}
	if p == nil {
		return fmt.Errorf("provider %s disappeared from providers.json between the presentation and the write; nothing written", panelSlot(name))
	}
	if samePanelProviderRecord(*p, record) {
		return nil
	}
	return fmt.Errorf("provider %s changed on disk between the presentation and the write, so the presented diff is stale; nothing written (re-run %s after reviewing the current record)", panelSlot(name), verb)
}

// reverify is the write-time stale-base check (the per-writer signed state the
// pick record's unit chain requires: skipping it re-opens the CWE-367 class the
// model verbs closed). It runs AFTER consent and before the Core verb, and every
// doubt refuses: an unreadable or corrupt registry, a vanished record, a record
// that appeared, and a record whose body moved.
func (c *Core) panelProviderReverify(b panelProviderBase, verb string) error {
	reg, err := c.loadRegistry()
	if err != nil {
		return fmt.Errorf("providers.json could not be re-read before the write, so the presented diff could not be re-verified; nothing written")
	}
	if err := panelSlotDrift(reg, b.name, b.present, b.record, verb); err != nil {
		return err
	}
	if b.newName == "" {
		return nil
	}
	return panelSlotDrift(reg, b.newName, b.newPresent, b.newRecord, verb)
}

// panelProviderAddDiff renders the exact pre-write presentation for add-json.
// A same-named provider renders as a wholesale replace (removed line + added
// line and both model counts), because UpsertProvider replaces the record and
// its whole model list: a silent model-list wipe has to be visible before y.
func panelProviderAddDiff(b panelProviderBase, spec ProviderSpec) []string {
	after := len(spec.Models)
	if !b.present {
		return panelBody([]string{
			fmt.Sprintf(panelAddSentence, panelSlot(spec.Name)),
			fmt.Sprintf(panelCountsLine, 0, after),
			panelAtomicLine,
			fmt.Sprintf(panelAddedLine, panelProviderLine(spec.Name, spec.BaseURL, after)),
		})
	}
	before := len(b.record.Models)
	return panelBody([]string{
		fmt.Sprintf(panelReplaceSentence, panelSlot(spec.Name)),
		fmt.Sprintf(panelCountsLine, before, after),
		panelAtomicLine,
		fmt.Sprintf(panelRemovedLine, panelProviderLine(b.record.Name, b.record.BaseURL, before)),
		fmt.Sprintf(panelAddedLine, panelProviderLine(spec.Name, spec.BaseURL, after)),
	})
}

// panelProviderRenameDiff renders the exact presentation for rename: the action
// sentence, the model count (carried over unchanged), the atomic-0600 sentence,
// and the removed/added lines.
func panelProviderRenameDiff(b panelProviderBase, newName string) []string {
	n := len(b.record.Models)
	return panelBody([]string{
		fmt.Sprintf(panelRenameSentence, panelSlot(b.record.Name), panelSlot(newName)),
		fmt.Sprintf(panelCountsLine, n, n),
		panelAtomicLine,
		fmt.Sprintf(panelRemovedLine, panelProviderLine(b.record.Name, b.record.BaseURL, n)),
		fmt.Sprintf(panelAddedLine, panelProviderLine(newName, b.record.BaseURL, n)),
	})
}

// panelProviderDeleteDiff renders the exact presentation for delete: the
// removal sentence, the model count before/after (the whole model list leaves
// with the entry), the atomic-0600 sentence, and the removed line.
func panelProviderDeleteDiff(b panelProviderBase) []string {
	n := len(b.record.Models)
	return panelBody([]string{
		fmt.Sprintf(panelDeleteSentence, panelSlot(b.record.Name)),
		fmt.Sprintf(panelCountsLine, n, 0),
		panelAtomicLine,
		fmt.Sprintf(panelRemovedLine, panelProviderLine(b.record.Name, b.record.BaseURL, n)),
	})
}

// --- repair doors (W32/W33/W34) ---------------------------------------------

// panelEnvCandidates is the presentation-time read of the stored clean /
// BOM-variant entries per target: providers.ResolveEnvCandidates, the same
// authority the key audit, the wizard and U3's CLI plan use. A seam so a test
// never touches the live environment store (the panel's tests run headless).
var panelEnvCandidates = providers.ResolveEnvCandidates

// panelBackupBase names the directory the repair writes its timestamped backup
// under (W33).
func panelBackupBase() string {
	if dir, err := paths.AgyProBackups(); err == nil && dir != "" {
		return dir
	}
	return "(the agy-pro backups directory)"
}

// panelRepairEntry is one resolved repair target: the environment NAME the
// repair may write, the provider that declares it, and the probe-free verdict
// this surface can state (rewrite from a stored BOM variant, or keep). The
// probe-gated half of the decision is owned by the repair itself, so the
// approval is never broader than the operation.
type panelRepairEntry struct {
	env          string
	provider     string
	rewrite      bool
	variant      string
	variantCount int
	keepWhy      string
}

func (e panelRepairEntry) action() string {
	if e.rewrite {
		return "rewrite"
	}
	return "keep"
}

// panelVariantDisplay renders a stored BOM-variant name with the invisible
// prefix made visible ("<BOM>NAME"), the bare name masked, so a key-shaped
// variant name cannot survive the annotation.
func panelVariantDisplay(name string) string {
	return "<BOM>" + panelOperand(strings.ReplaceAll(name, providers.BOM, ""))
}

func (e panelRepairEntry) diffLine() string {
	provider := "-"
	if strings.TrimSpace(e.provider) != "" {
		provider = panelSlot(e.provider)
	}
	env := panelOperand(e.env)
	if !e.rewrite {
		return fmt.Sprintf("  %s (provider %s): keep (%s)", env, provider, e.keepWhy)
	}
	line := fmt.Sprintf("  %s (provider %s): rewrite the clean name from BOM variant %s, then delete that variant",
		env, provider, panelVariantDisplay(e.variant))
	if e.variantCount > 1 {
		line += fmt.Sprintf(" (%d BOM variants stored; one is promoted)", e.variantCount)
	}
	return line
}

// panelRollbackEntry is one recorded backup entry: the root it writes, the NAME
// it writes, and whether the value is restored or the name removed. Values never
// reach this struct because the field is not declared.
type panelRollbackEntry struct {
	root    string
	name    string
	existed bool
}

func (e panelRollbackEntry) diffLine() string {
	root := "HKCU\\Environment"
	if e.root == providers.EnvRootProcess {
		root = "the process environment"
	}
	verb := "delete "
	if e.existed {
		verb = "restore"
	}
	return fmt.Sprintf("  %s %s in %s", verb, panelOperand(e.name), root)
}

// panelRepairPlan is the presented repair (or rollback) intent plus its signed
// state. presented records whether a plan could be composed at all: when the
// registry, the target set, or the backup file could not be read at
// presentation time, there is nothing that can go stale, so the gate still runs
// the op and lets the STORE report its own (masked) sentence. Refusing before
// the gate instead would duplicate the store's rule on this surface and would
// hide the forwarded-error masking path the F6 pins cover.
type panelRepairPlan struct {
	rollback  bool
	presented bool
	target    string
	names     []string
	entries   []panelRepairEntry
	file      string
	restores  []panelRollbackEntry
	// unreadable states why no plan could be composed (empty when presented).
	unreadable string
}

// panelProviderForEnv resolves the registry provider that declares one key
// variable, the same first-match walk the repair uses.
func panelProviderForEnv(reg *providers.Registry, env string) string {
	if reg == nil {
		return ""
	}
	for i := range reg.Providers {
		if strings.TrimSpace(reg.Providers[i].APIKeyEnv) == env {
			return reg.Providers[i].Name
		}
	}
	return ""
}

// panelComposeRepairEntries reads the stored entries for every resolved target
// and states the probe-free half: a target with a BOM variant stored is a
// rewrite candidate, a target without one is kept.
func panelComposeRepairEntries(reg *providers.Registry, names []string) []panelRepairEntry {
	out := make([]panelRepairEntry, 0, len(names))
	for _, env := range names {
		env = strings.TrimSpace(env)
		if env == "" {
			continue
		}
		e := panelRepairEntry{env: env, provider: panelProviderForEnv(reg, env)}
		var exact, variants []providers.EnvCandidate
		for _, c := range panelEnvCandidates(env) {
			if c.Root != providers.EnvRootUser {
				continue
			}
			if c.Exact {
				exact = append(exact, c)
			} else {
				variants = append(variants, c)
			}
		}
		switch {
		case len(variants) > 0:
			e.rewrite = true
			e.variant = variants[0].Name
			e.variantCount = len(variants)
		case len(exact) > 0:
			e.keepWhy = "no BOM variant stored beside the clean name"
		default:
			e.keepWhy = "no entry stored under this name or a BOM variant of it"
		}
		out = append(out, e)
	}
	return out
}

// panelFixPlan composes the fix-keys plan from the resolved targets. A registry
// or target-resolution failure yields a plan that states, in the body, that the
// presented plan could not be composed and that the repair will refuse.
func (c *Core) panelFixPlan(target string) panelRepairPlan {
	p := panelRepairPlan{target: target}
	reg, err := c.loadRegistry()
	if err != nil {
		p.unreadable = "the providers registry could not be read, so the repair targets and their stored entries cannot be shown"
		return p
	}
	names, err := providers.ResolveRepairTargets(reg, target)
	if err != nil {
		p.unreadable = "the repair targets could not be resolved from providers.json, so the per-target entries cannot be shown"
		return p
	}
	p.presented = true
	p.names = names
	p.entries = panelComposeRepairEntries(reg, names)
	return p
}

// panelRollbackBackup is the on-disk key-backup layout the rollback plan reads.
// Only entry names, roots and existence flags are decoded; a value field is not
// declared, so a value can never reach the frame.
type panelRollbackBackup struct {
	Entries []struct {
		Root    string `json:"root"`
		Name    string `json:"name"`
		Existed bool   `json:"existed"`
	} `json:"entries"`
}

// panelRollbackPlan reads the backup file the rollback will apply, BEFORE any
// consent is asked, so the operator sees the entries the write restores or
// deletes. A missing or unparsable file leaves the plan unpresented (the
// rollback reports the read error after consent; nothing is written).
func panelRollbackPlan(target string) panelRepairPlan {
	p := panelRepairPlan{rollback: true, target: target}
	p.file = providers.ResolveBackupFile(target)
	raw, err := os.ReadFile(p.file)
	if err != nil {
		p.unreadable = "the key backup could not be read, so the entries it records cannot be shown"
		return p
	}
	var snap panelRollbackBackup
	if err := json.Unmarshal(raw, &snap); err != nil {
		p.unreadable = "the key backup is not a readable key-backup document, so the entries it records cannot be shown"
		return p
	}
	p.presented = true
	for _, e := range snap.Entries {
		p.restores = append(p.restores, panelRollbackEntry{root: e.Root, name: e.Name, existed: e.Existed})
	}
	return p
}

// diff renders the exact presented plan for one fix-keys or rollback op.
func (p panelRepairPlan) diff() []string {
	if p.rollback {
		lines := []string{
			"Restores the recorded key names in HKCU\\Environment and this process's environment from one key backup.",
			"Backup file: " + panelOperand(p.file),
		}
		if !p.presented {
			return panelBody(append(lines,
				"  "+p.unreadable+": the rollback refuses after consent (nothing written)."))
		}
		if len(p.restores) == 0 {
			lines = append(lines, "The backup records no entries: the rollback refuses after consent (nothing written).")
		}
		entries := make([]string, 0, len(p.restores))
		for _, e := range p.restores {
			entries = append(entries, panelBodyLine(e.diffLine()))
		}
		lines = append(lines, capPanelEntries(entries)...)
		return panelBody(append(lines, "The backup file is only read; nothing under the backup directory changes."))
	}
	lines := []string{
		"Repairs BOM-polluted provider key names in HKCU\\Environment and this process's environment.",
		"Per-target action is probe-gated: a target whose BOM-variant value does not authenticate upstream is kept, and a clean name that already validates upstream is kept.",
	}
	if !p.presented {
		return panelBody(append(lines,
			"  "+p.unreadable+": the repair refuses after consent (nothing written)."))
	}
	entries := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, panelBodyLine(e.diffLine()))
	}
	lines = append(lines, capPanelEntries(entries)...)
	return panelBody(append(lines,
		fmt.Sprintf("One DPAPI-encrypted backup of every value it touches is written first (0600, atomic) under %s; values are copied, never printed.", panelBackupBase()),
		"Writes the key NAMES above; the process environment and HKCU\\Environment are the only stores it touches.",
	))
}

// panelRepairDrift names the first difference between the presented fix-keys
// plan and a fresh one, or "" when the entry sets match.
func panelRepairDrift(presented, fresh []panelRepairEntry) string {
	if len(presented) != len(fresh) {
		return fmt.Sprintf("the target count moved from %d to %d", len(presented), len(fresh))
	}
	for i := range presented {
		a, b := presented[i], fresh[i]
		if a.env != b.env {
			return fmt.Sprintf("target %d moved from %s to %s", i+1, panelOperand(a.env), panelOperand(b.env))
		}
		if a.action() != b.action() {
			return fmt.Sprintf("%s moved from %s to %s", panelOperand(a.env), a.action(), b.action())
		}
		if a.variant != b.variant {
			return fmt.Sprintf("the stored BOM variant of %s changed", panelOperand(a.env))
		}
	}
	return ""
}

// panelRollbackDrift names the first difference between the presented rollback
// entry set and a fresh read of the same file, or "" when they match.
func panelRollbackDrift(presented, fresh []panelRollbackEntry) string {
	if len(presented) != len(fresh) {
		return fmt.Sprintf("the recorded entry count moved from %d to %d", len(presented), len(fresh))
	}
	for i := range presented {
		a, b := presented[i], fresh[i]
		if a.name != b.name || a.root != b.root {
			return fmt.Sprintf("entry %d became %s", i+1, panelOperand(b.name))
		}
		if a.existed != b.existed {
			return fmt.Sprintf("the recorded existence of %s changed", panelOperand(a.name))
		}
	}
	return ""
}

// reverify re-reads the presented state after consent and refuses the write when
// the entry set moved. An unpresented plan signs nothing, so it has nothing to
// re-verify and the op runs (the store owns the refusal sentence). Nothing has
// been written when a drift is named: no HKCU value, no process environment
// value, and no backup file.
func (c *Core) panelRepairReverify(p panelRepairPlan, verb string) error {
	if !p.presented {
		return nil
	}
	if p.rollback {
		fresh := panelRollbackPlan(p.target)
		if !fresh.presented {
			return errors.New("the key backup could not be re-read before the write, so the presented diff could not be re-verified; nothing written (HKCU untouched)")
		}
		if drift := panelRollbackDrift(p.restores, fresh.restores); drift != "" {
			return fmt.Errorf("the key backup changed on disk between the presentation and the write (%s), so the presented diff is stale; nothing written (HKCU untouched; re-run %s after reviewing the backup)", drift, verb)
		}
		return nil
	}
	// The signed state is the resolved target set plus the per-target verdict:
	// the registry can be edited under the prompt, and the Core verb re-resolves
	// its targets from the registry, so a target set that moved must refuse here
	// instead of being applied.
	reg, err := c.loadRegistry()
	if err != nil {
		return errors.New("the registry could not be re-read before the write, so the presented key-repair plan could not be re-verified; nothing written (HKCU untouched)")
	}
	names, err := providers.ResolveRepairTargets(reg, p.target)
	if err != nil {
		return errors.New("the repair targets could not be re-resolved before the write, so the presented key-repair plan could not be re-verified; nothing written (HKCU untouched)")
	}
	if strings.Join(names, "\x00") != strings.Join(p.names, "\x00") {
		return fmt.Errorf("the resolved repair target set changed on disk between the presentation and the write, so the presented diff is stale; nothing written (HKCU untouched, no backup file written; re-run %s after reviewing the registry)", verb)
	}
	if drift := panelRepairDrift(p.entries, panelComposeRepairEntries(reg, p.names)); drift != "" {
		return fmt.Errorf("the key-repair plan changed on disk between the presentation and the write (%s), so the presented diff is stale; nothing written (HKCU untouched, no backup file written; re-run %s after reviewing the keys)", drift, verb)
	}
	return nil
}

// --- P5-1 U5b: state.json scalar doors (W20) --------------------------------
//
// User decision F3(a) on the remaining panel write doors. The choice rows
// (default_profile, bridge_tools, native_seat_concurrency) used to call
// Core.Set the instant a number was pressed, so the write happened with no
// presentation at all. The panel now composes the SAME diff its CLI twin
// (`agy pro settings set <key> <value>`, U2's stateScalarPlan) prints, signs the
// one row it names, and re-reads that row immediately before the write.

// Panel-side spellings of the two state.json diff sentences. They are the bytes
// U2's CLI gate prints (cmd/agy/pro_settings.go stateWriteAtomicSentence and
// stateScalarPlan.diff), held here because that package is not importable from
// this one and this surface is outside the twelve catalogs (the
// LegacyPanelNotice / approvalEntriesHidden idiom). TestU5bStateDiffBytePins
// pins them, so a wording change on either front-end fails a test.
const (
	panelStateSetsLine   = "Sets %s in state.json. No other key changes."
	panelStateUnknownFmt = "  %s is not a row this panel writes; the store refuses unknown keys."
	panelStateAtomicLine = "state.json will be written (0600, atomic) with this exact diff:"
)

// panelStateField maps one panel choice row onto the state.json row the write
// touches and the raw stored value the write-time re-verify compares.
type panelStateField struct {
	jsonKey string
	read    func(*mode.State) string
}

// panelStateFieldFor resolves a row id to its persisted key. The spellings are
// the hyphen and underscore forms Core.Set's closed key table accepts, so the
// panel and the store name the same row.
func panelStateFieldFor(key string) (panelStateField, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "default-profile", "default_profile":
		return panelStateField{"default_profile", func(s *mode.State) string { return s.DefaultProfile }}, true
	case "bridge-tools", "bridge_tools":
		return panelStateField{"bridge_tools", func(s *mode.State) string { return s.BridgeTools }}, true
	case "native-seat-concurrency", "native_seat_concurrency":
		return panelStateField{"native_seat_concurrency", func(s *mode.State) string {
			// 0 is the zero value the store drops through omitempty, so the row is
			// absent from state.json rather than "0"; the diff says (unset).
			if s.NativeSeatConcurrency == 0 {
				return ""
			}
			return strconv.Itoa(s.NativeSeatConcurrency)
		}}, true
	}
	return panelStateField{}, false
}

// panelStateValueDisplay renders one state.json value for a diff line: masked
// through the registry-slot authority (a scalar slot can carry a misdirected key
// paste, the same class panelSlot covers for registry slots), and "(unset)" for
// the empty value the store drops. Benign values stay byte-identical.
func panelStateValueDisplay(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(unset)"
	}
	return panelSlot(v)
}

// panelStatePlan is one presented state.json scalar write: the row that changes,
// the value it held on disk when the diff was rendered, and the value the write
// will persist.
type panelStatePlan struct {
	key      string
	jsonKey  string
	knownKey bool
	read     func(*mode.State) string
	before   string
	after    string
}

// panelStatePlanFor reads state.json and captures the presented value of one row
// before anything is printed. after is the value the write will persist (see
// panelAfterStateValue).
func (c *Core) panelStatePlanFor(key, after string) (panelStatePlan, error) {
	st, err := c.deps.LoadState()
	if err != nil || st == nil {
		return panelStatePlan{}, errors.New("state.json could not be read before the write, so the presented diff could not be composed; nothing written")
	}
	f, known := panelStateFieldFor(key)
	p := panelStatePlan{key: key, jsonKey: strings.TrimSpace(key), knownKey: known, after: after}
	if known {
		p.jsonKey, p.read, p.before = f.jsonKey, f.read, f.read(st)
	}
	return p, nil
}

// diff renders the exact diff for one scalar write. An unknown row has no key to
// change, so the diff states that instead of inventing an old -> new line (the
// store owns the refusal, and the key renders masked).
func (p panelStatePlan) diff() []string {
	key := p.jsonKey
	if !p.knownKey {
		key = panelSlot(key)
	}
	lines := []string{fmt.Sprintf(panelStateSetsLine, key)}
	if p.knownKey {
		lines = append(lines, fmt.Sprintf("  %s: %s -> %s", key, panelStateValueDisplay(p.before), panelStateValueDisplay(p.after)))
	} else {
		lines = append(lines, fmt.Sprintf(panelStateUnknownFmt, key))
	}
	return panelBody(append(lines, panelStateAtomicLine))
}

// panelStateReverify re-reads state.json after consent and refuses the write when
// the presented row moved (the G-C3 / CWE-367 rule). Nothing has been written at
// this point, and the write that follows re-loads the same state through Core.Set.
func (c *Core) panelStateReverify(p panelStatePlan, verb string) error {
	st, err := c.deps.LoadState()
	if err != nil || st == nil {
		return errors.New("state.json re-read failed before the write, so the presented diff could not be re-verified; nothing written")
	}
	if !p.knownKey || p.read == nil {
		return nil
	}
	if cur := p.read(st); cur != p.before {
		return fmt.Errorf("%s in state.json changed on disk between the presentation and the write (%s -> %s), so the presented diff is stale; nothing written (re-run %s after reviewing the current state)",
			p.jsonKey, panelStateValueDisplay(p.before), panelStateValueDisplay(cur), verb)
	}
	return nil
}

// panelAfterStateValue projects the bytes Core.Set will persist, so the diff
// shows what lands in state.json instead of the raw choice. The projection
// delegates to the store's own normalizers (mode.NormalizeProfile,
// ParseBridgeToolsSpec, the 1..MaxNativeSeatConcurrency bound) and mirrors the
// closed key table of Core.Set; a value outside a closed set renders as given
// (masked) and the store refuses it after consent, so the panel never owns a
// second validation authority.
func panelAfterStateValue(key, value string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "default-profile", "default_profile":
		if mode.ValidProfile(value) {
			return mode.NormalizeProfile(value)
		}
	case "bridge-tools", "bridge_tools":
		if tools, err := ParseBridgeToolsSpec(value); err == nil {
			return tools
		}
	case "native-seat-concurrency", "native_seat_concurrency":
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 1 && n <= mode.MaxNativeSeatConcurrency {
			return strconv.Itoa(n)
		}
	}
	return value
}

// --- P5-1 U5b: swarm preset append (W24) ------------------------------------

const (
	panelPresetAppendSentence = "Appends preset %s to swarm-state.json. No existing preset changes."
	panelPresetCountsLine     = "  presets before: %d, after: %d"
	panelPresetExistingLine   = "  existing presets (kept byte-identical):"
	panelPresetExistingFmt    = "  preset %s"
	panelPresetAddedLine      = "+ preset %s = %s"
	panelPresetAtomicLine     = "swarm-state.json will be written (0600, atomic) with this exact diff:"
)

// panelPresetPlan is one presented append plus its signed state: the existing
// preset names, in the order the store returned them. The numbers of the new
// preset are part of the diff, so they are masked through panelOperand (the
// authority the panel's own `swarm.presets` row already renders them with).
type panelPresetPlan struct {
	name    string
	numbers string
	names   []string
}

// panelPresetPlanFor signs the preset list the append will extend. The caller
// passes the list it just read (the same read the write appends to), so the
// presented "before" list and the written list are one read, not two.
func panelPresetPlanFor(presets []swarm.SavedPreset, name, numbers string) panelPresetPlan {
	p := panelPresetPlan{name: name, numbers: numbers}
	for _, e := range presets {
		p.names = append(p.names, e.Name)
	}
	return p
}

// diff renders the exact append: the count line, the bounded existing list, the
// one added entry, and the atomic-0600 sentence for the file it replaces.
func (p panelPresetPlan) diff() []string {
	lines := []string{
		fmt.Sprintf(panelPresetAppendSentence, panelOperand(p.name)),
		fmt.Sprintf(panelPresetCountsLine, len(p.names), len(p.names)+1),
	}
	if len(p.names) > 0 {
		lines = append(lines, panelPresetExistingLine)
		entries := make([]string, 0, len(p.names))
		for _, n := range p.names {
			// Existing rows are listed by NAME only: their numbers are the other
			// presets' business, and the append changes none of them.
			entries = append(entries, panelBodyLine(fmt.Sprintf(panelPresetExistingFmt, panelOperand(n))))
		}
		lines = append(lines, capPanelEntries(entries)...)
	}
	return panelBody(append(lines,
		fmt.Sprintf(panelPresetAddedLine, panelOperand(p.name), panelOperand(p.numbers)),
		panelPresetAtomicLine,
	))
}

// panelPresetDrift names the first difference between the presented preset list
// and a fresh read, or "" when they match.
func panelPresetDrift(presented, fresh panelPresetPlan) string {
	if len(presented.names) != len(fresh.names) {
		return fmt.Sprintf("the preset count moved from %d to %d", len(presented.names), len(fresh.names))
	}
	for i := range presented.names {
		if presented.names[i] != fresh.names[i] {
			return fmt.Sprintf("preset %d became %s", i+1, panelOperand(fresh.names[i]))
		}
	}
	return ""
}

// panelPresetReverify re-reads swarm-state.json after consent and refuses the
// append when the preset list moved. It returns the freshly read list, so the
// save appends to the list it verified instead of a stale in-memory copy. A
// corrupt file reads as empty here, exactly as it does for the append, so the
// acknowledgment gate (and not this check) owns that decision.
func (c *Core) panelPresetReverify(p panelPresetPlan, verb string) ([]swarm.SavedPreset, error) {
	presets, err := c.SwarmPresets()
	if err != nil {
		return nil, errors.New("swarm-state.json could not be re-read before the write, so the presented diff could not be re-verified; nothing written")
	}
	if drift := panelPresetDrift(p, panelPresetPlanFor(presets, p.name, p.numbers)); drift != "" {
		return nil, fmt.Errorf("swarm-state.json changed on disk between the presentation and the write (%s), so the presented diff is stale; nothing written (re-run %s after reviewing the saved presets)", drift, verb)
	}
	return presets, nil
}

// --- P5-1 U5b: mode switch / restore (W13/W15 panel halves) ------------------
//
// The panel's two mode doors used to open a bare M3 confirmation ("Confirm
// Switch Profile Mode? [y/n]") with no body, so the operator approved a label
// instead of a deployment. They now present the same FILE PLAN U2's CLI gate
// prints: the mode transition, the config dir, the managed set with its on-disk
// state, the deployment groups (or the restore source and its groups), and the
// current_mode row the deployment writes last.

const (
	panelModeSwitchIntro        = "Switches the active profile: %s -> %s"
	panelModeRestoreIntro       = "Restores the newest profile backup over the live profile configuration."
	panelModeCurrentLine        = "Current mode: %s -> %s"
	panelModeConfigLine         = "Config dir: %s"
	panelModeBackupLine         = "Managed entries move to a timestamped backup under the agy-pro backups directory, then the new profile is deployed:"
	panelModeEntryLine          = "  %-14s %s"
	panelModeDeployIntro        = "Deploys %s from the embedded %s profile:"
	panelModeDeployGroup        = "  %s (%s)"
	panelModeUnknownDeploy      = "Deploys: the %s profile (unrecognized target: the switch refuses after consent)"
	panelModeRestoreSource      = "Restore source: %s (%s)"
	panelModeRestoreLeftInPlace = "Left in place (not a mode-managed item): %s"
	panelModeRestorePre         = "Current managed entries move to a pre-restore backup first (under %s):"
	panelModeAtomicLine         = "state.json will be written (0600, atomic) with the mode change:"
	panelModeStateLine          = "  current_mode: %s -> %s"
)

// panelConfigDir and panelListBackups are the two environment reads the file plan
// needs. They are seams (the panelEnvCandidates idiom) so the panel's tests never
// touch the live ~/.gemini/config or the live ~/.agy-pro/backups directory.
var (
	panelConfigDir   = paths.GeminiConfigDir
	panelListBackups = mode.ListBackups
)

// panelModeManagedEntry is one managed-set entry's presented on-disk state, the
// backup half of a switch and the pre-restore half of a restore.
type panelModeManagedEntry struct {
	name    string
	present bool
}

// panelModeAssetGroup is one top-level group of profile or backup files with its
// file count, so a 64-file deployment fits one body.
type panelModeAssetGroup struct {
	name  string
	files int
}

// panelModePlan is the presented FILE PLAN for one switch or restore plus the
// state it signs.
type panelModePlan struct {
	restore     bool
	targetRaw   string
	target      string
	recognized  bool
	current     string
	configDir   string
	managed     []panelModeManagedEntry
	groups      []panelModeAssetGroup
	deployCount int
	sourceDir   string
	sourcePaths []string
	sourceCount int
	// sourceSkipped names the backup's top-level entries the managed register
	// does not own; mode.Restore leaves them in place, so the body says so.
	sourceSkipped []string
}

// panelNormalizeModeTarget resolves a mode operand to the profile the switch
// deploys. recognized is false for anything outside the alias set mode.Switch
// accepts; the plan then presents the managed set only and the switch itself owns
// the refusal (no second validation authority here).
func panelNormalizeModeTarget(target string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "1", mode.ModeFull, "full-stack":
		return mode.ModeFull, true
	case "2", mode.ModeClean, "default":
		return mode.ModeClean, true
	}
	return strings.ToLower(strings.TrimSpace(target)), false
}

// panelModeTargetDisplay renders the plan's target mode for a body line.
func (p panelModePlan) targetDisplay() string {
	if p.restore || p.recognized {
		return p.target
	}
	return panelSlot(p.targetRaw)
}

// panelModeBase captures the state every mode presentation signs: the current
// mode, the config directory, and the on-disk presence of each managed entry
// (mode.DefaultManagedItems, the ONE deployment enumeration mode.Switch and
// mode.Restore walk, so the plan cannot drift from the write).
func (c *Core) panelModeBase() (panelModePlan, *mode.State, error) {
	st, err := c.deps.LoadState()
	if err != nil || st == nil {
		return panelModePlan{}, nil, errors.New("state.json could not be read before the write, so the presented file plan could not be composed; nothing written")
	}
	configDir, err := panelConfigDir()
	if err != nil || configDir == "" {
		return panelModePlan{}, nil, errors.New("the profile config directory could not be resolved before the write, so the presented file plan could not be composed; nothing written")
	}
	p := panelModePlan{current: st.CurrentMode, configDir: configDir}
	for _, item := range mode.DefaultManagedItems {
		_, statErr := os.Stat(filepath.Join(configDir, item))
		p.managed = append(p.managed, panelModeManagedEntry{name: item, present: statErr == nil})
	}
	return p, st, nil
}

// panelModeSwitchPlan captures the file plan for one switch to target.
func (c *Core) panelModeSwitchPlan(target string) (panelModePlan, error) {
	p, _, err := c.panelModeBase()
	if err != nil {
		return panelModePlan{}, err
	}
	p.targetRaw = target
	p.target, p.recognized = panelNormalizeModeTarget(target)
	if !p.recognized {
		return p, nil
	}
	groups, count, err := panelCountProfileAssets(p.target)
	if err != nil {
		return panelModePlan{}, fmt.Errorf("the %s profile assets could not be enumerated before the write, so the presented file plan could not be composed; nothing written", p.target)
	}
	p.groups, p.deployCount = groups, count
	return p, nil
}

// panelModeRestorePlan captures the file plan for one restore of the newest
// profile backup.
func (c *Core) panelModeRestorePlan() (panelModePlan, error) {
	p, st, err := c.panelModeBase()
	if err != nil {
		return panelModePlan{}, err
	}
	p.restore = true
	p.target = "restored"
	source, err := panelResolveRestoreSource(st)
	if err != nil {
		return panelModePlan{}, err
	}
	p.sourceDir = source
	groups, count, paths, skipped, err := panelCountRestoreSource(source)
	if err != nil {
		return panelModePlan{}, errors.New("the restore source could not be read before the write, so the presented file plan could not be composed; nothing written")
	}
	p.groups, p.sourceCount, p.sourcePaths, p.sourceSkipped = groups, count, paths, skipped
	return p, nil
}

// panelResolveRestoreSource resolves the backup directory a restore copies from,
// exactly the way mode.Restore does: the recorded LastBackup when it still
// exists, otherwise the newest directory through the exported ListBackups seam.
// The "no previous backups found to restore" sentence is mode.Restore's own, so
// the refusal reads the same from either door.
func panelResolveRestoreSource(st *mode.State) (string, error) {
	if st != nil {
		if dir := strings.TrimSpace(st.LastBackup); dir != "" {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				return dir, nil
			}
		}
	}
	backups, err := panelListBackups()
	if err != nil {
		return "", errors.New("the backup directories could not be listed before the write, so the presented file plan could not be composed; nothing written")
	}
	if len(backups) == 0 {
		return "", errors.New("no previous backups found to restore")
	}
	return backups[len(backups)-1], nil
}

// panelCountProfileAssets walks the embedded profile the switch will deploy
// (profiles.GetProfileFS, the same filesystem mode.Switch walks) and counts its
// files per top-level group. No deployment list is invented here: a new asset in
// the embedded profile appears in the plan automatically.
func panelCountProfileAssets(profile string) ([]panelModeAssetGroup, int, error) {
	profileFS, err := profiles.GetProfileFS(profile)
	if err != nil {
		return nil, 0, err
	}
	counts := map[string]int{}
	var order []string
	total := 0
	walkErr := fs.WalkDir(profileFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." || d.IsDir() {
			return nil
		}
		top := path
		if i := strings.IndexByte(path, '/'); i >= 0 {
			top = path[:i]
		}
		if _, seen := counts[top]; !seen {
			order = append(order, top)
		}
		counts[top]++
		total++
		return nil
	})
	if walkErr != nil {
		return nil, 0, walkErr
	}
	sort.Strings(order)
	groups := make([]panelModeAssetGroup, 0, len(order))
	for _, name := range order {
		groups = append(groups, panelModeAssetGroup{name: name, files: counts[name]})
	}
	return groups, total, nil
}

// panelCountRestoreSource lists what a restore will copy: the file count per
// top-level entry the managed register owns, plus the sorted relative file list
// (the state the plan signs for its write-time re-verify) and the names of the
// backup's other top-level entries, which the copy-back leaves where they are.
//
// The enumeration is filtered through mode.DefaultManagedItems because that is
// exactly what mode.Restore's copy-back walks (Core.ModeRestore delegates to it),
// so the approved body lists only the entries the narrowed write touches
// (F-S9A-3). The skipped names are returned so the body can name them rather than
// silently shortening its own list.
func panelCountRestoreSource(dir string) (groups []panelModeAssetGroup, total int, paths []string, skipped []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	for _, entry := range entries {
		if !panelManagedSourceEntry(entry.Name()) {
			skipped = append(skipped, entry.Name())
			continue
		}
		root := filepath.Join(dir, entry.Name())
		count := 0
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				return relErr
			}
			paths = append(paths, filepath.ToSlash(rel))
			count++
			return nil
		})
		if walkErr != nil {
			return nil, 0, nil, nil, walkErr
		}
		groups = append(groups, panelModeAssetGroup{name: entry.Name(), files: count})
		total += count
	}
	sort.Strings(paths)
	return groups, total, paths, skipped, nil
}

// panelManagedSourceEntry reports whether the managed register owns a top-level
// backup entry. The CLI mirror resolves the same question from the same register
// (mode.DefaultManagedItems), which is what keeps the two presented plans equal -
// including the case-fold half, because the copy-back's Join+Stat lookup is
// case-insensitive on Windows and a byte-exact answer would understate the
// write set there (see mode.managedItemOwner).
func panelManagedSourceEntry(name string) bool {
	for _, item := range mode.DefaultManagedItems {
		if strings.EqualFold(item, name) {
			return true
		}
	}
	return false
}

// panelFileCountWord renders a file count in the plural form the CLI plan uses
// ("1 file" / "N files").
func panelFileCountWord(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// diff renders the exact file plan for one switch or restore. The two list-shaped
// halves (the managed set, the deploy/restore groups) go through capPanelEntries,
// so a wider managed set or a deeper profile still fits one body.
func (p panelModePlan) diff() []string {
	var lines []string
	if p.restore {
		lines = append(lines,
			panelModeRestoreIntro,
			fmt.Sprintf(panelModeCurrentLine, panelStateValueDisplay(p.current), p.targetDisplay()),
			fmt.Sprintf(panelModeConfigLine, p.configDir),
			fmt.Sprintf(panelModeRestoreSource, p.sourceDir, panelFileCountWord(p.sourceCount)),
		)
		if len(p.sourceSkipped) > 0 {
			lines = append(lines, panelBodyLine(fmt.Sprintf(panelModeRestoreLeftInPlace, strings.Join(p.sourceSkipped, ", "))))
		}
		groupLines := make([]string, 0, len(p.groups))
		for _, g := range p.groups {
			groupLines = append(groupLines, panelBodyLine(fmt.Sprintf(panelModeDeployGroup, g.name, panelFileCountWord(g.files))))
		}
		lines = append(lines, capPanelEntries(groupLines)...)
		lines = append(lines, fmt.Sprintf(panelModeRestorePre, filepath.Dir(p.sourceDir)))
	} else {
		lines = append(lines,
			fmt.Sprintf(panelModeSwitchIntro, panelStateValueDisplay(p.current), p.targetDisplay()),
			fmt.Sprintf(panelModeConfigLine, p.configDir),
			panelModeBackupLine,
		)
	}
	managedLines := make([]string, 0, len(p.managed))
	for _, m := range p.managed {
		state := "absent"
		if m.present {
			state = "present (backed up)"
		}
		managedLines = append(managedLines, panelBodyLine(fmt.Sprintf(panelModeEntryLine, m.name, state)))
	}
	lines = append(lines, capPanelEntries(managedLines)...)
	if !p.restore {
		if p.recognized {
			lines = append(lines, fmt.Sprintf(panelModeDeployIntro, panelFileCountWord(p.deployCount), p.target))
			groupLines := make([]string, 0, len(p.groups))
			for _, g := range p.groups {
				groupLines = append(groupLines, panelBodyLine(fmt.Sprintf(panelModeDeployGroup, g.name, panelFileCountWord(g.files))))
			}
			lines = append(lines, capPanelEntries(groupLines)...)
		} else {
			lines = append(lines, fmt.Sprintf(panelModeUnknownDeploy, p.targetDisplay()))
		}
	}
	return panelBody(append(lines,
		panelModeAtomicLine,
		fmt.Sprintf(panelModeStateLine, panelStateValueDisplay(p.current), p.targetDisplay()),
	))
}

// panelModeReverify re-reads the presented state after consent and refuses the
// deployment when the mode, the managed set, or (for a restore) the restore
// source's signed STATE moved: the file count plus the sorted relative path
// list the panel presented. The signature covers the file SET, not the file
// BYTES, so a same-name content rewrite inside the approval window is not
// detected here; per-path sha256 signing (deploy's StaleError shape) is the
// follow-up that would close it. Nothing has been written at this point: the
// deployment has not started.
func (c *Core) panelModeReverify(p panelModePlan, verb string) error {
	st, err := c.deps.LoadState()
	if err != nil || st == nil {
		return errors.New("state.json re-read failed before the write, so the presented file plan could not be re-verified; nothing written")
	}
	if st.CurrentMode != p.current {
		return fmt.Errorf("current_mode in state.json changed on disk between the presentation and the write (%s -> %s), so the presented file plan is stale; nothing written (re-run %s after reviewing the current state)",
			panelStateValueDisplay(p.current), panelStateValueDisplay(st.CurrentMode), verb)
	}
	for _, m := range p.managed {
		_, statErr := os.Stat(filepath.Join(p.configDir, m.name))
		if now := statErr == nil; now != m.present {
			state := "absent"
			if m.present {
				state = "present"
			}
			return fmt.Errorf("the managed entry %q of the live config directory changed on disk between the presentation and the write (it was %s when the plan was presented), so the presented file plan is stale; nothing written (re-run %s after reviewing the config directory)",
				m.name, state, verb)
		}
	}
	if !p.restore {
		return nil
	}
	_, total, paths, _, err := panelCountRestoreSource(p.sourceDir)
	if err != nil {
		return fmt.Errorf("the restore source %s could not be re-read before the write, so the presented file plan could not be re-verified; nothing written", p.sourceDir)
	}
	if total != p.sourceCount || !panelSameStrings(paths, p.sourcePaths) {
		return fmt.Errorf("the restore source %s changed on disk between the presentation and the write, so the presented file plan is stale; nothing written (re-run %s after reviewing the backup)", p.sourceDir, verb)
	}
	return nil
}

// panelSameStrings reports whether two string lists are element-wise equal.
func panelSameStrings(a, b []string) bool {
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
