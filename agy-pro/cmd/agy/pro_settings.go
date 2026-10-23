package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"agypro/internal/integrations"
	"agypro/internal/mode"
	"agypro/internal/openaiup"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/profiles"
)

// Settings surface command (contract §2.6, U6):
//
//	agy pro settings                      full-screen keybinding-driven TUI (M2)
//	agy pro settings --panel <id>         deep-link one TUI panel
//	agy pro settings show [--json]        SettingsView snapshot (non-interactive)
//	agy pro settings set <key> <value>    default-profile | bridge-tools | native-seat-concurrency |
//	                                      language | launcher_menu | integration-preset |
//	                                      integration-statusline|hook|rules|plugins|skills
//	                                      (P5-1 U2: gates like every state.json write below)
//	agy pro settings providers ...        list | add-json | add-interactive | test | rename | delete |
//	                                      model-add-json | model-update | model-delete
//	                                      (every registry write gates: add-json, rename, delete and the
//	                                      model twins print the exact masked diff and then require
//	                                      consent - console stdin prompts y/N, non-interactive stdin
//	                                      requires the explicit --yes flag - and every gated verb
//	                                      re-verifies the presented on-disk record after consent.
//	                                      add-interactive runs the setup wizard, whose composed gate
//	                                      covers providers.json AND the Windows user environment;
//	                                      it takes --yes on the same terms, P5-1 U4)
//	agy pro settings mode ...             switch | restore | list | status
//	                                      (switch and restore gate: the exact FILE PLAN is printed,
//	                                      consent is required, and the presented plan is re-verified
//	                                      against the live config directory before the write)
//	agy pro settings doctor ...           diagnose | fix-keys [--yes] | rollback-keys <dir> [--yes]
//	                                      (both write the Windows environment: fix-keys also
//	                                      writes the DPAPI backup file. The exact plan prints,
//	                                      consent is required, and the presented plan is re-verified
//	                                      before the write)
//	agy pro settings update-info | uninstall-info | version-info
//
// Phase 1: unused-but-compilable; U-wiring flips main.go's dispatch line to
// os.Exit(runSettingsCmd(subArgs, os.Stdin, os.Stdout, os.Stderr)).

const proSettingsUsage = `Usage: agy pro settings [--panel <id>] | show [--json] | set <key> <value> [--yes] |
       providers <list|add-json <file|-> [--yes]|add-interactive [--yes]|test <name>|rename <old> <new> [--yes]|
                  delete <name> [--yes]|
                  model-add-json <name> <file|-> [--yes]|model-update <name> <model-id> <file|-> [--yes]|
                  model-delete <name> <model-id> [--yes]> |
       mode <switch <full|clean> [--yes]|restore [--yes]|list|status> |
       doctor <diagnose [--strict]|fix-keys [targets] [--yes]|rollback-keys <dir> [--yes]> |
       update-info | uninstall-info | version-info
       set keys: default-profile | bridge-tools | native-seat-concurrency | language |
                 launcher_menu | integration-preset |
                 integration-statusline|hook|rules|plugins|skills`

// newSettingsCore builds the production Core. Tests replace this seam.
var newSettingsCore = func() *settings.Core {
	return settings.NewCore(settings.Deps{
		VersionFunc: func() string { return Version },
	})
}

// providerWizardRunner is the wizard sink for the add-interactive door (and the
// wizard alias). P5-1 U4 (F3(a)): the wizard writes providers.json (W12) and the
// Windows user environment (W31), so the door hands it the caller's consent
// context (providers.WizardOptions), which no settings.Core verb carries.
// Tests replace this seam.
var providerWizardRunner = providers.InteractiveSetupWizardWithGate

// runSettingsCmd executes `agy pro settings [args]` and returns the process
// exit code (0 ok, 1 runtime error, 2 usage/flag error).
func runSettingsCmd(args []string, in io.Reader, out, errw io.Writer) int {
	core := newSettingsCore()

	if len(args) == 0 {
		return settings.RunTUI(core, in, out, "")
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "--panel":
		if len(args) < 2 {
			fmt.Fprintf(errw, "[agy-pro] --panel requires a panel id or number\n%s\n", proSettingsUsage)
			return 2
		}
		return settings.RunTUI(core, in, out, args[1])
	case "show":
		asJSON := len(args) > 1 && args[1] == "--json"
		if len(args) > 1 && !asJSON {
			// C2: operands echoed in errors can be a misdirected key paste.
			fmt.Fprintf(errw, "[agy-pro] unknown show option %q\n%s\n", providers.MaskOperand(args[1]), proSettingsUsage)
			return 2
		}
		if err := settings.WriteSnapshot(out, core, asJSON); err != nil {
			fmt.Fprintf(errw, "[agy-pro] show failed: %v\n", err)
			return 1
		}
		return 0
	case "set":
		ops, yes := splitYesFlag(args[1:])
		if len(ops) < 2 {
			fmt.Fprintf(errw, "[agy-pro] set requires <key> <value>\n%s\n", proSettingsUsage)
			return 2
		}
		// P5-1 U2 (F3(a)): state.json is persisted state, so the write gates
		// like every registry write. The diff is composed from the on-disk
		// value BEFORE anything is printed, and the same key is re-read after
		// consent, so a key that moved under the prompt refuses instead of
		// being overwritten under a diff that no longer describes the file.
		plan, err := snapshotStateScalar(ops[0], afterStateValue(ops[0], ops[1]))
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		proceed, code := confirmStateWrite(in, out, errw, plan.diff(), yes)
		if !proceed {
			return code
		}
		if _, err := plan.reverify("agy pro settings set"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		// Core.Set keeps the closed key table and its value validation, so an
		// out-of-set value is refused by the store after consent (the
		// presented line carries the masked operand, never the raw bytes).
		if err := core.Set(ops[0], ops[1]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] %s = %s\n", providers.MaskOperand(ops[0]), providers.MaskOperand(ops[1]))
		return 0
	case "providers":
		return settingsProvidersCmd(core, args[1:], in, out, errw)
	case "mode":
		return settingsModeCmd(core, args[1:], in, out, errw)
	case "doctor":
		return settingsDoctorCmd(core, args[1:], in, out, errw)
	case "update-info":
		fmt.Fprintln(out, "Update: run `agy update` in your terminal (real-agy subcommand \"Update CLI\").")
		return 0
	case "uninstall-info":
		fmt.Fprintln(out, "Uninstall: uninstall.cmd [-KeepState|-DryRun|-RestorePath|-Force] (recycle-bin only).")
		fmt.Fprintln(out, "If profile backups exist, run `agy pro mode restore` first (agy-pro/README.md).")
		return 0
	case "version-info":
		fmt.Fprintf(out, "agy-pro version %s\n", Version)
		return 0
	default:
		fmt.Fprintf(errw, "[agy-pro] unknown settings subcommand %q\n%s\n", providers.MaskOperand(args[0]), proSettingsUsage)
		return 2
	}
}

func settingsProvidersCmd(core *settings.Core, args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(errw, "[agy-pro] providers requires a subcommand\n%s\n", proSettingsUsage)
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		view, err := core.View()
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] list failed: %v\n", err)
			return 1
		}
		for _, sec := range view.Sections {
			if sec.ID != "p2" {
				continue
			}
			for _, it := range sec.Items {
				if strings.HasPrefix(it.ID, "provider.key.") {
					fmt.Fprintf(out, "%s: %s\n", it.Label, it.Value)
				}
			}
		}
		return 0
	case "add-json":
		if len(args) < 2 {
			fmt.Fprintf(errw, "[agy-pro] add-json requires a file path or -\n%s\n", proSettingsUsage)
			return 2
		}
		data, err := readSettingsSpec(args[1], in)
		if err != nil {
			// C2/D1: os.ReadFile embeds the operand in its error text; the
			// redactor is seeded with the raw operand and its per-token
			// forms (the same composition as the prompt-file and rollback
			// twins), so a comma-decorated plant is masked in the composite
			// sentence and not only at the operand slot.
			fmt.Fprintf(errw, "[agy-pro] %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(args[1])...))
			return 1
		}
		spec, err := settings.ParseProviderSpecJSON(data)
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		base := snapshotRegistryProvider(spec.Name)
		proceed, code := confirmRegistryMutation(in, out, errw, providerAddDiff(base, spec), hasYesFlag(args[2:]))
		if !proceed {
			return code
		}
		// G-C3: the diff above described the on-disk registry, so that is the
		// state the write must still find. An external edit landing while the
		// prompt waited (or right before a --yes run) can turn a new-name add
		// into a wholesale replace of a record the operator never saw.
		if err := reverifyPresentedProvider(base, "add-json"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		if err := core.ProviderAdd(spec); err != nil {
			fmt.Fprintf(errw, "[agy-pro] add-json failed: %v\n", err)
			return 1
		}
		// H-C7: the provider name and the env-var NAME are registry slots, so the
		// success line renders them through the registry-slot masker (identity for
		// a benign name, first-5+**** for a key-shaped one).
		fmt.Fprintf(out, "[SUCCESS] provider %s saved (apiKeyEnv %s - env-var NAME only)\n", regOperand(spec.Name), regOperand(spec.APIKeyEnv))
		return 0
	case "add-interactive", "wizard":
		// P5-1 U4 (F3(a)): this is the third door onto the wizard, and the
		// wizard writes providers.json (W12) plus the Windows user environment
		// (W31). It therefore takes the same consent context the setup doors
		// pass: --yes is the documented non-interactive stand-in, and the
		// console fact is stdinIsTTY (the authority every other CLI gate in this
		// file uses), so a piped caller prints the diff and refuses naming --yes
		// with nothing written.
		//
		// The door calls the wizard seam directly: settings.Core carries no
		// wizard verb, since a verb taking only (in, out) has no place for that
		// consent context.
		ops, yes := splitYesFlag(args[1:])
		if len(ops) > 0 {
			fmt.Fprintf(errw, "[agy-pro] add-interactive takes no operands (got %q)\n%s\n", providers.MaskOperand(ops[0]), proSettingsUsage)
			return 2
		}
		if err := providerWizardRunner(in, out, providers.WizardOptions{Yes: yes, Console: stdinIsTTY(in)}); err != nil {
			if errors.Is(err, providers.ErrWizardWriteRefused) {
				// The gate already printed its refusal sentence naming --yes;
				// the door only carries the code (nothing was written).
				return 1
			}
			fmt.Fprintf(errw, "[agy-pro] setup wizard failed: %v\n", err)
			return 1
		}
		return 0
	case "test":
		if len(args) < 2 {
			fmt.Fprintf(errw, "[agy-pro] test requires a provider name\n%s\n", proSettingsUsage)
			return 2
		}
		if err := core.ProviderTest(args[1]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s key probe ok\n", regOperand(args[1]))
		return 0
	case "rename":
		if len(args) < 3 {
			fmt.Fprintf(errw, "[agy-pro] rename requires <old> <new>\n%s\n", proSettingsUsage)
			return 2
		}
		// FIX-7E parity: the store refuses a new name that is credential
		// material (the shared NAME authority). The CLI does not present a diff
		// for a write the store will refuse for a value-class reason, and the
		// sentence is the store's own named error, so the transcript never
		// carries the refused value.
		if providers.NameCarriesCredential(args[2]) {
			fmt.Fprintf(errw, "[agy-pro] %v\n", providers.ErrProviderNameCarriesCredential)
			return 1
		}
		base := snapshotRegistryRename(args[1], args[2])
		if !base.present {
			// Nothing to rename: the TUI twin refuses before its modal for the
			// same reason (screen_settings.go renameProvider), so no diff is
			// presented for a write that addresses no record.
			fmt.Fprintf(errw, "[agy-pro] provider %s not found in providers.json; nothing written\n", regOperand(args[1]))
			return 1
		}
		proceed, code := confirmRegistryMutation(in, out, errw, providerRenameDiff(base, args[2]), hasYesFlag(args[3:]))
		if !proceed {
			return code
		}
		// G-C3 stale base: the presented diff signs the old record and the
		// absence/presence of the new name; either moving makes the diff stale.
		if err := reverifyPresentedProvider(base, "rename"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		if err := core.ProviderRename(base.record.Name, args[2]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s renamed to %s\n", regOperand(args[1]), regOperand(args[2]))
		return 0
	case "delete":
		if len(args) < 2 {
			fmt.Fprintf(errw, "[agy-pro] delete requires a provider name\n%s\n", proSettingsUsage)
			return 2
		}
		base := snapshotRegistryProvider(args[1])
		if !base.present {
			// Mirrors the providers screen (screen_providers.go confirmDelete):
			// the not-found path refuses before the approval modal.
			fmt.Fprintf(errw, "[agy-pro] provider %s not found in providers.json; nothing written\n", regOperand(args[1]))
			return 1
		}
		proceed, code := confirmRegistryMutation(in, out, errw, providerDeleteDiff(base), hasYesFlag(args[2:]))
		if !proceed {
			return code
		}
		// G-C3 stale base: the signed state is the provider record as presented
		// (the same field-by-field comparison the providers screen re-verifies
		// with), so a record that moved under the prompt is never removed.
		if err := reverifyPresentedProvider(base, "delete"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		// Address the byte-exact name that was presented and re-verified, not
		// the raw operand: the Core verb removes the first case-insensitive
		// match, and the operator approved this record.
		if err := core.ProviderDelete(base.record.Name); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s removed from the registry (key values untouched)\n", regOperand(args[1]))
		return 0
	case "model-add-json":
		if len(args) < 3 {
			fmt.Fprintf(errw, "[agy-pro] model-add-json requires a provider name and a file path or -\n%s\n", proSettingsUsage)
			return 2
		}
		yes := hasYesFlag(args[3:])
		m, code := readModelSpec(args[1], args[2], in, errw)
		if code != 0 {
			return code
		}
		proceed, code := confirmRegistryMutation(in, out, errw, modelAddDiff(args[1], m), yes)
		if !proceed {
			return code
		}
		if err := core.ModelAdd(args[1], m); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s: model %s added\n", regOperand(args[1]), regOperand(m.ID))
		return 0
	case "model-update":
		if len(args) < 4 {
			fmt.Fprintf(errw, "[agy-pro] model-update requires a provider name, a model id, and a file path or -\n%s\n", proSettingsUsage)
			return 2
		}
		yes := hasYesFlag(args[4:])
		m, code := readModelSpec(args[1], args[3], in, errw)
		if code != 0 {
			return code
		}
		presented := snapshotRegistryModel(args[1], args[2])
		proceed, code := confirmRegistryMutation(in, out, errw, modelUpdateDiff(presented, m), yes)
		if !proceed {
			return code
		}
		// G-C3: consent was given against the diff rendered above, so the state
		// that diff described must still be the state on disk. The prompt above
		// can block indefinitely and --yes returns instantly; either way an
		// external edit can land in between, and Core.ModelUpdate replaces the
		// whole record from a fresh load with no re-verification of its own. The
		// signed state is the presented record, or its absence when the diff was
		// after-only (H-C3: a model created in the window would be replaced
		// silently). Covered for both consent paths (the prompt finishes before
		// this line, and --yes never skips it).
		if err := reverifyPresentedModel(presented); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		if err := core.ModelUpdate(args[1], args[2], m); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s: model %s updated\n", regOperand(args[1]), regOperand(m.ID))
		return 0
	case "model-delete":
		if len(args) < 3 {
			fmt.Fprintf(errw, "[agy-pro] model-delete requires a provider name and a model id\n%s\n", proSettingsUsage)
			return 2
		}
		proceed, code := confirmRegistryMutation(in, out, errw, modelDeleteDiff(args[1], args[2]), hasYesFlag(args[3:]))
		if !proceed {
			return code
		}
		if err := core.ModelDelete(args[1], args[2]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s: model %s removed\n", regOperand(args[1]), regOperand(args[2]))
		return 0
	default:
		fmt.Fprintf(errw, "[agy-pro] unknown providers subcommand %q\n%s\n", providers.MaskOperand(args[0]), proSettingsUsage)
		return 2
	}
}

// settingsModeCmd runs `agy pro settings mode ...`. switch and restore are
// persisted-state writes (state.json plus the ~/.gemini/config deployment), so
// P5-1 U2 (F3(a)) gates both: the exact FILE PLAN prints first, consent is
// required (console y/N, or --yes on a non-interactive stdin), and the plan is
// re-verified against the live config directory before the write runs.
func settingsModeCmd(core *settings.Core, args []string, in io.Reader, out, errw io.Writer) int {
	ops, yes := splitYesFlag(args)
	if len(ops) == 0 {
		fmt.Fprintf(errw, "[agy-pro] mode requires a subcommand\n%s\n", proSettingsUsage)
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(ops[0])) {
	case "switch":
		if len(ops) < 2 {
			fmt.Fprintf(errw, "[agy-pro] switch requires <full|clean>\n%s\n", proSettingsUsage)
			return 2
		}
		plan, err := snapshotModeSwitch(ops[1])
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		proceed, code := confirmModeWrite(in, out, errw, plan.diff(), yes)
		if !proceed {
			return code
		}
		if err := plan.reverify("agy pro settings mode switch"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		if err := core.ModeSwitch(ops[1]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] mode switched to %s\n", strings.ToLower(ops[1]))
		return 0
	case "restore":
		plan, err := snapshotModeRestore()
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		proceed, code := confirmModeWrite(in, out, errw, plan.diff(), yes)
		if !proceed {
			return code
		}
		if err := plan.reverify("agy pro settings mode restore"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		if err := core.ModeRestore(); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintln(out, "[SUCCESS] latest profile backup restored")
		return 0
	case "list", "backups":
		backups, err := core.ModeBackups()
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		for i, p := range backups {
			fmt.Fprintf(out, "  %d. %s\n", i+1, p)
		}
		if len(backups) == 0 {
			fmt.Fprintln(out, "  (no backups)")
		}
		return 0
	case "status":
		if err := core.ModeStatus(out); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(errw, "[agy-pro] unknown mode subcommand %q\n%s\n", providers.MaskOperand(ops[0]), proSettingsUsage)
		return 2
	}
}

// settingsDoctorCmd runs `agy pro settings doctor ...`. fix-keys and
// rollback-keys write the Windows environment (W32/W34) and, for fix-keys, the
// DPAPI backup file (W33), so P5-1 U3 (F3(a)) gates both: the exact plan prints
// first, consent is required (console y/N, or --yes on a non-interactive
// stdin), and the presented plan is re-verified against the live registry and
// environment before the write runs.
func settingsDoctorCmd(core *settings.Core, args []string, in io.Reader, out, errw io.Writer) int {
	ops, yes := splitYesFlag(args)
	if len(ops) == 0 {
		ops = []string{"diagnose"}
	}
	switch {
	case ops[0] == "diagnose":
		strict := len(ops) > 1 && ops[1] == "--strict"
		if err := core.DoctorDiagnose(out, strict); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		return 0
	case ops[0] == "fix-keys":
		target := "all"
		if len(ops) > 1 {
			target = ops[1]
		}
		reg, err := registrySnapshot()
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] Providers registry not loaded: %v\n", err)
			return 1
		}
		names, err := resolveRepairTargets(reg, target)
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] Cannot resolve repair targets: %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(target)...))
			return 1
		}
		plan := snapshotKeyRepairPlan(reg, target, names)
		proceed, code := confirmKeyRepairWrite(in, out, errw, plan.diff(), yes)
		if !proceed {
			return code
		}
		if err := plan.reverify("agy pro settings doctor fix-keys"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		report, err := core.FixKeysReport(target)
		if report != nil {
			fmt.Fprintf(out, "Repaired: %d | Skipped: %d | Failed: %d\n", report.Repaired, report.Skipped, report.Failed)
			if report.BackupPath != "" {
				fmt.Fprintf(out, "Rollback target (backup dir): %s\n", dirOf(report.BackupPath))
			}
		}
		if err != nil {
			// F7/G3: the fix-keys tail echoes the aggregate error, which embeds
			// registry-derived env names and quotes individual target tokens;
			// redact seeded with the raw target plus its token forms (same rule
			// as the `agy pro doctor` G2 tail).
			fmt.Fprintf(errw, "[agy-pro] fix-keys finished with errors: %s\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(target)...))
			return 1
		}
		return 0
	case ops[0] == "rollback-keys":
		if len(ops) < 2 {
			fmt.Fprintf(errw, "[agy-pro] rollback-keys requires a backup dir\n%s\n", proSettingsUsage)
			return 2
		}
		target := ops[1]
		plan, err := snapshotKeyRollbackPlan(target)
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %s\n", maskOperandSentence(rollbackPreConsentSentence(err), providers.FlagSeedOperands(target, providers.ResolveBackupFile(target))...))
			return 1
		}
		proceed, code := confirmKeyRepairWrite(in, out, errw, plan.diff(), yes)
		if !proceed {
			return code
		}
		if err := plan.reverify("agy pro settings doctor rollback-keys"); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		if err := core.DoctorRollbackKeys(target); err != nil {
			// E5: the forwarded rollback error re-embeds the raw backup dir /
			// resolved file from the operand; redact seeded with both so a
			// key-shaped --rollback-keys value cannot echo.
			// Batch N (M6 preemptive): the seed list also takes the per-token
			// forms (providers.FlagSeedOperands), the batch-M composition the
			// add-json, fix-keys, and prompt-file twins already use. The whole
			// operand alone is not enough: a location-shaped whole operand
			// ("./backups,<secret>") never seeds (SeedEligible's location
			// exemption), so the composite sentence fell back to MaskOperand,
			// whose generic patterns do not cover a bare secret token, and the
			// tail re-echoed it raw. FlagSeedOperands keeps the whole value AND
			// adds each comma/space/semicolon-separated token, so the token
			// seed closes the sentence; separator-free operands seed exactly
			// what they seeded before (byte-identical).
			fmt.Fprintf(errw, "[agy-pro] %s\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(target, providers.ResolveBackupFile(target))...))
			return 1
		}
		fmt.Fprintln(out, "[SUCCESS] registry key names restored from backup")
		return 0
	default:
		fmt.Fprintf(errw, "[agy-pro] unknown doctor subcommand %q\n%s\n", providers.MaskOperand(ops[0]), proSettingsUsage)
		return 2
	}
}

// rollbackPreConsentSentence renders the pre-consent failure of a rollback
// door. A read failure keeps the byte-stable "Rollback failed:" tail the door
// printed before the gate existed; a file that is not a readable key backup gets
// its own named refusal, because consent is never asked for a plan that could
// not be composed.
func rollbackPreConsentSentence(err error) string {
	if errors.Is(err, errRollbackBackupShape) {
		return fmt.Sprintf("Rollback refused before consent: %s (nothing written)", err.Error())
	}
	return fmt.Sprintf("Rollback failed: %s", err.Error())
}

// readSettingsSpec loads add-json input from a file or stdin ("-").
func readSettingsSpec(path string, in io.Reader) ([]byte, error) {
	if strings.TrimSpace(path) == "-" {
		return io.ReadAll(in)
	}
	return os.ReadFile(path)
}

// readModelSpec loads and parses one model record for the model-add-json /
// model-update twins. The file read error goes through the same composite
// masking as add-json (C2/D1: the path operand can be a misdirected key paste).
func readModelSpec(provider, path string, in io.Reader, errw io.Writer) (providers.Model, int) {
	data, err := readSettingsSpec(path, in)
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(path)...))
		return providers.Model{}, 1
	}
	m, err := parseModelSpecJSON(data)
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %v\n", err)
		return providers.Model{}, 1
	}
	return m, 0
}

// parseModelSpecJSON decodes one model record (the providers.Model wire
// shape). The ID is required; everything else is optional. Errors never echo
// the record body (B2/U8).
//
// Secret-shaped guard (the F2/F4 CLI gap the TUI form closes with
// looksSecretValue): the registry stores env-var NAMES only, so a free-text
// field carrying credential material (a misdirected paste) is refused here,
// before Core ever sees the record.
//
// The classifier is a delegation to the ONE shared NAME authority in
// internal/providers (NameCarriesCredential), the same predicate the TUI model
// form guard and every display sink call: a category-based separator class
// instead of an enumerated marker list, the invisible classes deleted first, a
// fixpoint strip of the leading decoration (relative markers, a URL scheme, a
// Windows drive prefix, bare or nested), and the shared shape test (known
// credential prefixes at any length, plus one uninterrupted opaque run of 16+
// runes carrying BOTH letters and digits) over the raw value and its stripped
// core, plus the operand and registry-slot arms that read the key classes those
// two miss. Separators and single-class runs (long words, all-letter ids,
// serials) stay readable on both doors, and a location-shaped value is refused
// on both (a held product decision, pinned on both sides). The earlier rounds
// kept a copy of the shape test here and it drifted from the TUI copy; the
// parity pin fails if a local list or a local strip reappears in this file.
func parseModelSpecJSON(data []byte) (providers.Model, error) {
	var m providers.Model
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		return providers.Model{}, fmt.Errorf("invalid model spec JSON: %w", err)
	}
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		return providers.Model{}, fmt.Errorf("model spec: id is required")
	}
	if modelSpecValueLooksSecret(m.ID) {
		return providers.Model{}, fmt.Errorf("model spec: field %q looks like secret material (value hidden)", "id")
	}
	if modelSpecValueLooksSecret(m.Family) {
		return providers.Model{}, fmt.Errorf("model spec: field %q looks like secret material (value hidden)", "family")
	}
	for i, tag := range m.Tags {
		if modelSpecValueLooksSecret(tag) {
			return providers.Model{}, fmt.Errorf("model spec: tag %d looks like secret material (value hidden)", i+1)
		}
	}
	for i, tool := range m.Capabilities.AllowedTools {
		if modelSpecValueLooksSecret(tool) {
			return providers.Model{}, fmt.Errorf("model spec: tool %d looks like secret material (value hidden)", i+1)
		}
	}
	// F-C4: strip Cc+Cf control runes from the effort operand before any
	// classification or display: the CLI printer has no painter (the TUI
	// painter strips at render), so an escape byte here would otherwise reach
	// the consent transcript and forge the visual presentation before
	// Registry.Validate refuses the write.
	m.Capabilities.ReasoningEffort = stripDiffControls(m.Capabilities.ReasoningEffort)
	if modelSpecValueLooksSecret(m.Capabilities.ReasoningEffort) {
		return providers.Model{}, fmt.Errorf("model spec: field %q looks like secret material (value hidden)", "reasoningEffort")
	}
	// F-C3: the closed effort set lives once at internal/openaiup
	// (ValidReasoningEfforts); refuse out-of-set values without echoing
	// them, and normalize case/whitespace on accept so "HIGH" persists as
	// "high" (the gateway fail-closes unknown values to "default", so an
	// un-normalized registry entry is pure drift from the TUI/CLI contract).
	effort := strings.ToLower(strings.TrimSpace(m.Capabilities.ReasoningEffort))
	if effort != "" && !openaiup.ValidReasoningEfforts[effort] {
		// G-C5: the allowed list renders from the canonical set, never from a
		// hand-maintained copy: a set change (a new level) must move the
		// refusal message with it, not leave the two drifting apart.
		return providers.Model{}, fmt.Errorf("model spec: field %q is not a supported reasoning effort (allowed: %s; value hidden)", "reasoningEffort", reasoningEffortAllowedList())
	}
	m.Capabilities.ReasoningEffort = effort
	return m, nil
}

// reasoningEffortAllowedList renders the canonical effort set
// (openaiup.ValidReasoningEfforts) as a comma-separated list in deterministic
// (sorted) order. Sorted order, not map order: Go randomizes map iteration, so
// the refusal message would otherwise differ between two runs of the same
// command.
func reasoningEffortAllowedList() string {
	valid := make([]string, 0, len(openaiup.ValidReasoningEfforts))
	for v := range openaiup.ValidReasoningEfforts {
		valid = append(valid, v)
	}
	sort.Strings(valid)
	return strings.Join(valid, ", ")
}

// modelSpecValueLooksSecret reports whether a free-text model field carries
// credential material rather than a name. It is a delegation to the ONE shared
// NAME authority (providers.NameCarriesCredential), the same predicate the TUI
// model form's looksSecretValue calls for the id, family, tags, tool grants and
// effort fields, so the CLI ingest and the form ingest answer identically on
// every input: the union of the policy classifier (providers.SecretShaped:
// credential heads at any length plus one opaque run at the 16-rune floor,
// read raw and after the fold and strip of a decorated value), the operand
// masker (providers.MaskOperand: the decorated-secret and JWT display classes),
// and the registry-slot masker (providers.MaskSlot: the segment arm that reads
// the underscore-head class, and the rewrite arm that re-renders a
// location-shaped value).
//
// The underscore-head class is why this door moved off SecretShaped alone
// (FIX-7E): SecretShaped is false for the planted underscore-head fixture
// (testfixtures.SKLive) while every display sink renders that same value as
// "sk_li****", so the verb accepted a value its own consent transcript masked
// as a key and persisted it raw. The
// union also refuses a single-class 16+ rune id and a location-shaped id (a
// path or a URL), which is the verdict the TUI model form has pinned since
// FIX-7A and is a held product decision (whether a model ID may legally be a
// path or a URL). This door carries no local exemption for those classes: a
// door that accepts a value its frames re-render is exactly the divergence the
// twin pins exist to catch.
//
// The earlier rounds kept this side's own prefix list, its own look-alike
// separator map, and its own strip, and the two doors drifted apart on every
// look-alike discovered after the maps were written. The list, the map, and the
// strip live once in internal/providers/slotpolicy.go; the delegation pin and
// the corpus parity pin fail if a local copy or a local arm reappears here.
func modelSpecValueLooksSecret(v string) bool {
	return providers.NameCarriesCredential(v)
}

// hasYesFlag reports whether the verb operands carry the explicit --yes
// consent token, the non-interactive stand-in for the console y prompt.
func hasYesFlag(args []string) bool {
	for _, a := range args {
		if strings.TrimSpace(a) == "--yes" {
			return true
		}
	}
	return false
}

// modelWriteAtomicSentence is the pre-write presentation's atomic-0600 line,
// the same sentence the M4 approval modal carries (modal.approval.body).
const modelWriteAtomicSentence = "providers.json will be written (0600, atomic) with this exact diff:"

// confirmRegistryMutation presents the exact masked diff and gates the write
// (the never-silent hard rule for the CLI twins: a registry write is presented
// before it happens, never reported after the fact). It is the providers.json
// binding of confirmPersistedWrite, unchanged in bytes from the pre-P5-1 form.
func confirmRegistryMutation(in io.Reader, out, errw io.Writer, diff []string, yes bool) (proceed bool, code int) {
	return confirmPersistedWrite(in, out, errw, "[agy-pro] Approve Registry Write", diff, "providers.json", "a registry write", yes)
}

// confirmPersistedWrite presents one exact diff and gates the write (the
// never-silent hard rule for the CLI twins: a persisted-state write is
// presented before it happens, never reported after the fact). It is the P5-1
// U1 consent mechanism generalized over the three persisted stores the CLI
// writes, so providers.json, state.json, and the profile deployment share ONE
// implementation instead of three transcripts that drift apart:
//
//   - banner: the [agy-pro] line that names the store class.
//   - target: what the console prompt applies the write to.
//   - noun: the refusal's object ("a registry write", "a state write", "a
//     profile deployment").
//
// Console stdin prompts y/n: n, Esc, or an empty answer aborts with nothing
// written. Piped or CI stdin has no one to ask, so the verb requires the
// explicit --yes flag: without it the diff still prints and the verb refuses
// with an honest error naming the flag. Console detection rides the stdinIsTTY
// seam (pro_swarm.go), the same swappable seam the other entry units use. The
// seam alone cannot tell a real console from /dev/null or NUL (both are
// character devices), so an immediate EOF at the prompt (zero bytes, read
// error) takes the same refusal as a pipe instead of a silent cancel.
func confirmPersistedWrite(in io.Reader, out, errw io.Writer, banner string, diff []string, target, noun string, yes bool) (proceed bool, code int) {
	fmt.Fprintln(out, banner)
	for _, line := range diff {
		// The consent transcript is the one place every store's diff passes through,
		// so the control-character neutralisation lives here and not in the line
		// builders (FIX-12 / F-S9F-1): a composed integrations diff can carry the
		// engine's Detail sentences (a path read out of the live hooks.json) and the
		// settings ledger's field-name error rows, and an ESC sequence would let a
		// crafted config value steer the screen the operator is about to approve.
		fmt.Fprintln(out, "  "+stripTerminalControl(line))
	}
	if yes {
		return true, 0
	}
	if stdinIsTTY(in) {
		fmt.Fprintf(out, "Apply this write to %s? [y/N]: ", target)
		// Round-6 seat2-F4: bound the prompt read. A character device without
		// a newline (for example /dev/zero or NUL) would otherwise buffer
		// memory without limit (CWE-400, local self-DoS). 64 bytes is far past
		// any y/n answer: an over-long line is read up to the cap and then
		// treated exactly like any non-y answer, so nothing writes. The
		// immediate-EOF refusal below is unchanged (answer == "" with an error).
		answer, err := bufio.NewReader(io.LimitReader(in, 64)).ReadString('\n')
		if err != nil && answer == "" {
			fmt.Fprintf(errw, "[agy-pro] refusal: non-interactive stdin cannot consent to %s; re-run with --yes after reviewing the diff above (nothing written)\n", noun)
			return false, 1
		}
		if strings.EqualFold(strings.TrimSpace(answer), "y") {
			return true, 0
		}
		fmt.Fprintln(out, "[CANCELLED] nothing written")
		return false, 0
	}
	fmt.Fprintf(errw, "[agy-pro] refusal: non-interactive stdin cannot consent to %s; re-run with --yes after reviewing the diff above (nothing written)\n", noun)
	return false, 1
}

// lookupRegistryModel reads the on-disk registry (the same file the Core
// verbs save) to render before/after diff lines and model counts in the
// pre-write presentation. found reports the provider; modelFound reports the
// exact model ID. Either false renders the diff without that slice; the write
// itself always goes through the Core seam under test.
// registrySnapshot is the presentation-time on-disk registry read: the
// model-update diff is rendered from it, and reverifyPresentedModel then
// re-reads through registryReload. Tests replace this seam so a command can be
// driven against an in-memory registry without touching the live
// ~/.agy-pro/providers.json (a fresh machine has none).
var registrySnapshot = providers.Load

func lookupRegistryModel(providerName, modelID string) (before providers.Model, count int, found, modelFound bool) {
	reg, err := registrySnapshot()
	if err != nil || reg == nil {
		return providers.Model{}, 0, false, false
	}
	p := findProviderCI(reg, providerName)
	if p == nil {
		return providers.Model{}, 0, false, false
	}
	for _, m := range p.Models {
		if m.ID == modelID {
			before = m
			modelFound = true
		}
	}
	return before, len(p.Models), true, modelFound
}

// registryReload re-reads the on-disk registry at write time, immediately
// before a model-update write (G-C3). Tests replace this seam to interleave an
// external edit on the --yes path, where there is no console prompt to hang
// the interleaving on; production keeps providers.Load, the same read the
// presentation and the Core verb use.
var registryReload = func() (*providers.Registry, error) {
	return providers.Load()
}

// presentedModel is the on-disk record one pre-write presentation was
// rendered from, kept so the write can re-verify it after consent (G-C3).
type presentedModel struct {
	provider string
	modelID  string
	before   providers.Model
	count    int
	found    bool
	matched  bool
}

// snapshotRegistryModel captures the on-disk record for a model-update
// presentation.
func snapshotRegistryModel(providerName, modelID string) presentedModel {
	before, count, found, modelFound := lookupRegistryModel(providerName, modelID)
	return presentedModel{
		provider: providerName,
		modelID:  modelID,
		before:   before,
		count:    count,
		found:    found,
		matched:  found && modelFound,
	}
}

// presentable reports whether the presentation actually showed a before
// record. Only then is there a signed state to re-verify.
func (p presentedModel) presentable() bool {
	return p.found && p.matched
}

// reverifyPresentedModel re-reads the registry after consent and refuses the
// write when the presented state moved (G-C3). The CLI presents a diff read
// from disk, then blocks on an unbounded console prompt (or returns instantly
// under --yes), then Core.ModelUpdate re-loads and replaces the whole record:
// without this check an external edit landing between the presentation and the
// write was silently clobbered, and the operator consented to a diff that no
// longer described the file. The comparison is the TUI form's signed-field
// set, so the two write paths refuse the same drift.
//
// The signed state is the presented record when the presentation carried a
// before record, and the record's ABSENCE when it did not (an after-only
// presentation, H-C3). The absence check matters: a model created by an
// external edit while the prompt waited would be replaced in place by
// Core.ModelUpdate, under a consent rendered before it existed. The unchanged
// sentence covers the created case too: the file did change on disk, the
// presented diff is stale, and nothing is written.
//
// Fail-closed: an unreadable registry, a vanished provider, a vanished model,
// and a model that appeared during the window all refuse. Nothing has been
// written at this point.
func reverifyPresentedModel(p presentedModel) error {
	reg, err := registryReload()
	if err != nil || reg == nil {
		return fmt.Errorf("registry re-read failed before the write, so the presented diff could not be re-verified; nothing written")
	}
	prov := findProviderCI(reg, p.provider)
	if !p.presentable() {
		if prov != nil && findRegistryModel(prov, p.modelID) != nil {
			return fmt.Errorf("model %s changed on disk between the presentation and the write, so the presented diff is stale; nothing written (re-run model-update after reviewing the current record)", regOperand(p.modelID))
		}
		return nil
	}
	if prov == nil {
		return fmt.Errorf("provider %s disappeared from providers.json between the presentation and the write; nothing written", regOperand(p.provider))
	}
	m := findRegistryModel(prov, p.modelID)
	if m == nil {
		return fmt.Errorf("model %s disappeared from providers.json between the presentation and the write; nothing written", regOperand(p.modelID))
	}
	if sameSignedModelFields(p.before, *m) {
		return nil
	}
	return fmt.Errorf("model %s changed on disk between the presentation and the write, so the presented diff is stale; nothing written (re-run model-update after reviewing the current record)", regOperand(p.modelID))
}

// sameSignedModelFields reports whether two model records agree on every field
// a model-update diff signs: id, family, tags, enabled, context, maxOut, the
// four capability flags, the tool-use flag, the effort (compared normalized)
// and the tool grants (compared in normalized order). It mirrors
// sameSignedFields in internal/tui/screen_model_form.go, the TUI form's
// stale-base comparison, so an update the TUI would refuse is refused on the
// CLI too. Both sides sign supportsTools: the flag can move on its own (the form
// OR-derives it from the grant list), so neither write path may silently revert
// a concurrent flip of it.
//
// One deliberate difference: the TUI normalizes tool grants into its catalog
// order, which this package does not carry, so the CLI normalization is a
// deduplicated sorted copy; both sides of the comparison get the same treatment,
// so a pure reorder is not drift on either path.
func sameSignedModelFields(a, b providers.Model) bool {
	return a.ID == b.ID &&
		a.Family == b.Family &&
		equalStringList(a.Tags, b.Tags) &&
		a.IsEnabled() == b.IsEnabled() &&
		a.ContextLength == b.ContextLength &&
		a.MaxOutputTokens == b.MaxOutputTokens &&
		a.Capabilities.SupportsTools == b.Capabilities.SupportsTools &&
		a.Capabilities.SupportsThinking == b.Capabilities.SupportsThinking &&
		normModelEffort(a.Capabilities.ReasoningEffort) == normModelEffort(b.Capabilities.ReasoningEffort) &&
		a.Capabilities.SupportsVision == b.Capabilities.SupportsVision &&
		a.Capabilities.SupportsVideo == b.Capabilities.SupportsVideo &&
		equalStringList(normalizeToolGrants(a.Capabilities.AllowedTools), normalizeToolGrants(b.Capabilities.AllowedTools))
}

// normModelEffort is the effort comparison form: case-folded and trimmed, so
// "HIGH" on disk and "high" from the spec read as the same level (the TUI
// form's normEffort).
func normModelEffort(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// equalStringList compares two lists element by element in order.
func equalStringList(a, b []string) bool {
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

// normalizeToolGrants returns the tool grants deduplicated and sorted, the
// order-independent comparison form (see sameSignedModelFields).
func normalizeToolGrants(tools []string) []string {
	seen := make(map[string]bool, len(tools))
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// findProviderCI resolves a provider exact-first, then case-insensitively
// (the Core verbs resolve case-insensitively; the diff presentation must see
// the same record the write will touch).
func findProviderCI(reg *providers.Registry, name string) *providers.Provider {
	for i := range reg.Providers {
		if reg.Providers[i].Name == name {
			return &reg.Providers[i]
		}
	}
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return &reg.Providers[i]
		}
	}
	return nil
}

// findRegistryModel returns the model with the exact ID in p, or nil. Model IDs
// are matched exactly, the same rule the Core verbs and the duplicate check use.
func findRegistryModel(p *providers.Provider, modelID string) *providers.Model {
	for i := range p.Models {
		if p.Models[i].ID == modelID {
			return &p.Models[i]
		}
	}
	return nil
}

// modelAddDiff renders the masked pre-write presentation for model-add-json:
// the mutation sentence, the model count before/after when the on-disk
// provider resolves, the atomic-0600 sentence, and one "+ field: value" line
// per set field (every operand masked).
//
// H-C6: the header sentence's model ID and provider name are registry slots
// too, so they route through the registry-slot masker (regOperand), not through
// the location-exempt MaskOperand: a decorated id such as
// "./ab3def7gh9jk1mnpqr5t" printed raw in the headline while the "+ id" line
// below masked the same value.
func modelAddDiff(providerName string, m providers.Model) []string {
	lines := []string{
		fmt.Sprintf("Adds model %s to provider %s. Key values are untouched.", regOperand(m.ID), regOperand(providerName)),
	}
	if _, count, found, _ := lookupRegistryModel(providerName, m.ID); found {
		lines = append(lines, fmt.Sprintf("Model count: %d -> %d", count, count+1))
	}
	lines = append(lines, modelWriteAtomicSentence)
	return append(lines, modelAddDiffLines(m)...)
}

// modelUpdateDiff renders the masked pre-write presentation for model-update:
// the mutation sentence, the count line, the atomic-0600 sentence, and one
// "  field: before -> after" line per changed field against the on-disk
// record carried by the snapshot. When the on-disk record is unavailable the
// diff falls back to after-only lines so the presentation never invents before
// values. The snapshot is handed back to reverifyPresentedModel after consent
// (G-C3), so the record that was presented is the record that is re-verified.
// The header's model ID and provider name are registry slots (H-C6), so they
// render through regOperand.
func modelUpdateDiff(p presentedModel, m providers.Model) []string {
	lines := []string{
		fmt.Sprintf("Updates model %s in provider %s. Key values are untouched.", regOperand(m.ID), regOperand(p.provider)),
	}
	if p.found {
		lines = append(lines, fmt.Sprintf("Model count: %d -> %d", p.count, p.count))
	}
	lines = append(lines, modelWriteAtomicSentence)
	if p.presentable() {
		return append(lines, modelChangedDiffLines(p.before, m)...)
	}
	return append(lines, modelAddDiffLines(m)...)
}

// modelDeleteDiff renders the masked pre-write presentation for model-delete.
// The header and the "- model" line render registry-slot values (the model ID
// and the provider name), so both route through regOperand (H-C6): a disk-borne
// id such as "./ab3def7gh9jk1mnpqr5t" printed raw under the location-exempt
// MaskOperand.
func modelDeleteDiff(providerName, modelID string) []string {
	lines := []string{
		fmt.Sprintf("Removes model %s from provider %s. Key values are untouched.", regOperand(modelID), regOperand(providerName)),
	}
	if _, count, found, _ := lookupRegistryModel(providerName, modelID); found {
		lines = append(lines, fmt.Sprintf("Model count: %d -> %d", count, count-1))
	}
	lines = append(lines, modelWriteAtomicSentence)
	return append(lines, "- model "+regOperand(modelID))
}

// regOperand renders one registry-slot operand for a CLI transcript line (a
// diff line, a mutation sentence, a success line, or a re-verify refusal). It is
// a delegation to providers.MaskSlot, the single registry-slot display
// authority: control-run strip first (F-C4), then the registry-slot authority
// (F-C1: a marker-decorated credential such as "./<key>" must not ride
// MaskOperand's location exemption), then the shared policy's
// identity-fallback, so a look-alike-decorated plant masks in the CLI
// transcript exactly as it does in the TUI frame (the CLI used to render that
// subclass raw while the TUI masked it). A value the policy does not flag
// renders byte-identically to its input, which is what keeps the benign
// transcript stable.
//
// The name stays because every transcript site already calls it; the body is
// one delegation and the parity pin fails if a local list or a local strip
// reappears here.
func regOperand(v string) string {
	return providers.MaskSlot(v)
}

// maskJoinedReg renders a list-valued registry slot: every item masks on its
// own before the join (the TUI maskJoined rule, so one credential-shaped entry
// cannot hide behind the separator of its neighbors), and the joined string is
// never re-classified as a single operand, which would see the separators and
// stay readable.
func maskJoinedReg(items []string, sep string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = regOperand(it)
	}
	return strings.Join(parts, sep)
}

// modelAddDiffLines renders one "+ field: value" line per set field of the
// incoming record (mirrors the M4 modal's addedDiffLines). The capability
// lines keep the TUI modal's labels (thinking/vision/video, F-C5).
//
// G-C1: EVERY slot routes through addReg, not only the capability slots. The
// id/family/tags/tools operands are registry slots too, and rendering them
// through MaskOperand left the same hole the capability lines had: a
// marker-decorated credential in the family slot ("./<40-rune secret>")
// persisted and printed raw into the consent transcript.
func modelAddDiffLines(m providers.Model) []string {
	lines := []string{}
	// addReg renders one registry-slot operand (see regOperand).
	addReg := func(field, value string) {
		lines = append(lines, "+ "+field+": "+regOperand(value))
	}
	// addList is the list form of addReg: per-item masking, then join.
	addList := func(field string, items []string) {
		lines = append(lines, "+ "+field+": "+maskJoinedReg(items, ", "))
	}
	addReg("id", m.ID)
	if m.Family != "" {
		addReg("family", m.Family)
	}
	if len(m.Tags) > 0 {
		addList("tags", m.Tags)
	}
	if m.Enabled != nil && !*m.Enabled {
		addReg("enabled", "false")
	}
	if m.ContextLength > 0 {
		addReg("contextLength", strconv.Itoa(m.ContextLength))
	}
	if m.MaxOutputTokens > 0 {
		addReg("maxOutputTokens", strconv.Itoa(m.MaxOutputTokens))
	}
	if len(m.Capabilities.AllowedTools) > 0 {
		addList("tools", m.Capabilities.AllowedTools)
	}
	if m.Capabilities.SupportsTools {
		addReg("supportsTools", "true")
	}
	if m.Capabilities.SupportsThinking {
		addReg("thinking", "true")
	}
	if m.Capabilities.ReasoningEffort != "" {
		addReg("reasoningEffort", m.Capabilities.ReasoningEffort)
	}
	if m.Capabilities.SupportsVision {
		addReg("vision", "true")
	}
	if m.Capabilities.SupportsVideo {
		addReg("video", "true")
	}
	return lines
}

// modelChangedDiffLines renders one "  field: before -> after" line per
// changed field (mirrors the M4 modal's changedDiffLines), or the no-changes
// marker. Two rules decide emission and rendering (H-C1):
//   - emission compares the RAW operands, never the rendered columns. A
//     marker-only difference ("fast" -> "./fast"), a case-only difference, or
//     two credential-shaped values that fold to the same first-5+**** mask all
//     move the persisted bytes, so the approval must print the line; deciding
//     on the rendered forms suppressed it and printed "(no field changes)"
//     while Core.ModelUpdate still replaced the record.
//   - rendering masks both columns through the same registry-slot masker as the
//     add side (G-C1), so a value whose raw bytes are a credential never reaches
//     the transcript. A true no-change (raw equality on every field) still
//     prints the byte-stable marker.
func modelChangedDiffLines(before, m providers.Model) []string {
	lines := []string{}
	// chgReg emits on a raw operand difference and renders both columns masked.
	chgReg := func(field, b, a string) {
		if b == a {
			return
		}
		lines = append(lines, "  "+field+": "+regOperand(b)+" -> "+regOperand(a))
	}
	// chgList is the list form of chgReg: the raw lists decide emission (order
	// included), per-item masking then join renders both columns.
	chgList := func(field string, b, a []string) {
		if equalStringList(b, a) {
			return
		}
		lines = append(lines, "  "+field+": "+maskJoinedReg(b, ", ")+" -> "+maskJoinedReg(a, ", "))
	}
	chgReg("id", before.ID, m.ID)
	chgReg("family", before.Family, m.Family)
	chgList("tags", before.Tags, m.Tags)
	chgReg("enabled", strconv.FormatBool(before.IsEnabled()), strconv.FormatBool(m.IsEnabled()))
	chgReg("contextLength", strconv.Itoa(before.ContextLength), strconv.Itoa(m.ContextLength))
	chgReg("maxOutputTokens", strconv.Itoa(before.MaxOutputTokens), strconv.Itoa(m.MaxOutputTokens))
	chgList("tools", before.Capabilities.AllowedTools, m.Capabilities.AllowedTools)
	chgReg("supportsTools", strconv.FormatBool(before.Capabilities.SupportsTools), strconv.FormatBool(m.Capabilities.SupportsTools))
	chgReg("thinking", strconv.FormatBool(before.Capabilities.SupportsThinking), strconv.FormatBool(m.Capabilities.SupportsThinking))
	chgReg("reasoningEffort", before.Capabilities.ReasoningEffort, m.Capabilities.ReasoningEffort)
	chgReg("vision", strconv.FormatBool(before.Capabilities.SupportsVision), strconv.FormatBool(m.Capabilities.SupportsVision))
	chgReg("video", strconv.FormatBool(before.Capabilities.SupportsVideo), strconv.FormatBool(m.Capabilities.SupportsVideo))
	if len(lines) == 0 {
		lines = append(lines, "(no field changes)")
	}
	return lines
}

// lookupRegistryProvider reads the on-disk registry through the same
// presentation seam the model diffs use (registrySnapshot) and returns the
// provider record for name, resolved exactly like the Core verbs resolve it
// (case-insensitive). found reports whether the entry is on disk.
func lookupRegistryProvider(name string) (providers.Provider, bool) {
	reg, err := registrySnapshot()
	if err != nil || reg == nil {
		return providers.Provider{}, false
	}
	p := findProviderCI(reg, name)
	if p == nil {
		return providers.Provider{}, false
	}
	return *p, true
}

// providerBase is the on-disk state one provider-level presentation was rendered
// from, kept so the write can re-verify it after consent (the G-C3 / CWE-367
// rule the model twins follow). It signs ONE addressing slot for add/delete and
// TWO for rename:
//   - name/present/record: the target provider as presented (for rename the OLD
//     name; for add the name being written; for delete the record being removed);
//   - newName/newPresent/newRecord: the rename target as presented, so "the new
//     name was absent" (or "was this exact record") is part of the signed state
//     instead of an assumption.
type providerBase struct {
	name       string
	present    bool
	record     providers.Provider
	newName    string
	newPresent bool
	newRecord  providers.Provider
}

// snapshotRegistryProvider captures the on-disk record for one provider name.
func snapshotRegistryProvider(name string) providerBase {
	rec, found := lookupRegistryProvider(name)
	return providerBase{name: name, present: found, record: rec}
}

// snapshotRegistryRename captures both slots a rename presentation signs: the
// old record and the current occupant of the new name (if any).
func snapshotRegistryRename(oldName, newName string) providerBase {
	b := snapshotRegistryProvider(oldName)
	rec, found := lookupRegistryProvider(newName)
	b.newName, b.newPresent, b.newRecord = newName, found, rec
	return b
}

// sameProviderRecord reports whether two provider records describe the same
// configuration. The deep comparison covers every field of the record (name,
// endpoint, key-variable NAME, and the full model list with its capabilities),
// mirroring the providers screen's stale-base check
// (internal/tui/screen_providers.go sameProviderRecord), so a field added to the
// record later is signed without touching this gate.
func sameProviderRecord(a, b providers.Provider) bool { return reflect.DeepEqual(a, b) }

// reverifyPresentedProvider re-reads the registry after consent and refuses the
// write when the state the presented diff described has moved (G-C3). The CLI
// presents a diff read from disk, then blocks on an unbounded console prompt (or
// returns instantly under --yes), then the Core verb re-loads the file and
// mutates it with no verification of its own: without this check an external
// edit landing in between was silently clobbered or removed under a consent that
// described a different file.
//
// The signed state is per slot (see providerBase). Fail-closed: an unreadable
// registry, a vanished record, a record that appeared, and a record whose body
// moved all refuse. Nothing has been written at this point. verb only fills the
// "re-run <verb>" hint of the refusal so the operator knows the recovery step.
func reverifyPresentedProvider(base providerBase, verb string) error {
	reg, err := registryReload()
	if err != nil {
		// A missing registry is the normal pre-setup state the write verbs can
		// create the first entry from (Core.loadRegistry treats it the same
		// way), so the re-verify compares against an empty registry instead of
		// refusing every first write on a factory-reset home. Any other load
		// error stays fail-closed: an unreadable or corrupt file cannot confirm
		// the presented state.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, providers.ErrNotConfigured) {
			return fmt.Errorf("registry re-read failed before the write, so the presented diff could not be re-verified; nothing written")
		}
		reg = &providers.Registry{}
	}
	if reg == nil {
		return fmt.Errorf("registry re-read failed before the write, so the presented diff could not be re-verified; nothing written")
	}
	if err := providerSlotDrift(reg, base.name, base.present, base.record, verb); err != nil {
		return err
	}
	if base.newName == "" {
		return nil
	}
	return providerSlotDrift(reg, base.newName, base.newPresent, base.newRecord, verb)
}

// providerSlotDrift compares one signed provider slot against the freshly loaded
// registry. present tells which way the slot was signed: an entry that was on
// disk must still be the same record, and an entry that was absent must still be
// absent (the H-C3 after-only case: a record created while the prompt waited
// would be replaced or removed under a diff rendered before it existed).
func providerSlotDrift(reg *providers.Registry, name string, present bool, record providers.Provider, verb string) error {
	p := findProviderCI(reg, name)
	if !present {
		if p != nil {
			return fmt.Errorf("provider %s appeared in providers.json between the presentation and the write, so the presented diff is stale; nothing written (re-run %s after reviewing the current registry)", regOperand(name), verb)
		}
		return nil
	}
	if p == nil {
		return fmt.Errorf("provider %s disappeared from providers.json between the presentation and the write; nothing written", regOperand(name))
	}
	if sameProviderRecord(*p, record) {
		return nil
	}
	return fmt.Errorf("provider %s changed on disk between the presentation and the write, so the presented diff is stale; nothing written (re-run %s after reviewing the current record)", regOperand(name), verb)
}

// providerCountWord renders the model count for a provider diff-context line in
// the TUI's plural form (menu.count.models: "1 model" / "N models").
func providerCountWord(n int) string {
	if n == 1 {
		return "1 model"
	}
	return strconv.Itoa(n) + " models"
}

// providerDiffLine renders one diff-context line for a registry entry, the shape
// the provider approvals show (internal/tui/screen_settings.go providerLine): the
// slot-masked provider name, the layered URL mask, and the model count. The name
// is a registry slot, so it renders through regOperand (the same authority the
// model diffs use); the base URL goes through providers.MaskBaseURL, the shared
// URL display mask, because a benign URL would be rewritten by the slot masker
// and a credential inside userinfo/query must not ride raw.
func providerDiffLine(name, baseURL string, models int) string {
	return fmt.Sprintf("provider %s (%s, %s)", regOperand(name), providers.MaskBaseURL(baseURL), providerCountWord(models))
}

// providerAddDiff renders the masked pre-write presentation for add-json: the
// mutation sentence, the model count before/after, the atomic-0600 sentence, and
// the added line (plus the removed line when the name already exists, because a
// same-named add replaces the whole entry and its model list wholesale).
//
// Line shapes mirror the M4 provider approvals the hub opens
// (screen_settings.go approveProviderAdd): the "add" sentence for a new name, the
// "replace wholesale" sentence for an existing one, the counts line, and the
// +/- provider lines. The header name renders through regOperand like every
// other registry slot.
func providerAddDiff(base providerBase, spec settings.ProviderSpec) []string {
	masked := regOperand(spec.Name)
	after := len(spec.Models)
	if !base.present {
		return []string{
			fmt.Sprintf("Adds provider %s to the registry. Key values are untouched.", masked),
			fmt.Sprintf("Model count: 0 -> %d", after),
			modelWriteAtomicSentence,
			"+ " + providerDiffLine(spec.Name, spec.BaseURL, after),
		}
	}
	before := len(base.record.Models)
	return []string{
		fmt.Sprintf("Replaces provider %s wholesale: the current entry and its whole model list are discarded.", masked),
		fmt.Sprintf("Model count: %d -> %d", before, after),
		modelWriteAtomicSentence,
		"- " + providerDiffLine(base.record.Name, base.record.BaseURL, before),
		"+ " + providerDiffLine(spec.Name, spec.BaseURL, after),
	}
}

// providerRenameDiff renders the masked pre-write presentation for rename: the
// action sentence, the model count (carried over unchanged), the atomic-0600
// sentence, and the removed/added provider lines (screen_settings.go
// renameProvider). The old name is the presented record's own name, so the
// rendered line describes the entry the write will move.
func providerRenameDiff(base providerBase, newName string) []string {
	n := len(base.record.Models)
	return []string{
		fmt.Sprintf("Renames provider %s to %s; the model list carries over unchanged.", regOperand(base.record.Name), regOperand(newName)),
		fmt.Sprintf("Model count: %d -> %d", n, n),
		modelWriteAtomicSentence,
		"- " + providerDiffLine(base.record.Name, base.record.BaseURL, n),
		"+ " + providerDiffLine(newName, base.record.BaseURL, n),
	}
}

// providerDeleteDiff renders the masked pre-write presentation for delete: the
// removal sentence, the model count before/after (the whole model list leaves
// with the entry), the atomic-0600 sentence, and the removed provider line
// (screen_providers.go confirmDelete).
func providerDeleteDiff(base providerBase) []string {
	n := len(base.record.Models)
	return []string{
		fmt.Sprintf("Removes provider %s from the registry. Key values are untouched.", regOperand(base.record.Name)),
		fmt.Sprintf("Model count: %d -> 0", n),
		modelWriteAtomicSentence,
		"- " + providerDiffLine(base.record.Name, base.record.BaseURL, n),
	}
}

// stripDiffControls removes control runes (the Cc class) and the format class
// (Cf: bidi overrides, zero-width joiners, BOM) from a value before it reaches
// a CLI transcript (F-C4). Unlike the TUI painter, the CLI printer has no
// render-time strip, so an escape byte in a registry operand would otherwise
// forge the visual consent presentation. The rule lives in the shared policy
// (providers.StripSlotControls) and the TUI delegates to the same one.
func stripDiffControls(s string) string {
	return providers.StripSlotControls(s)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return ""
}

// --- P5-1 U2: exact-diff gate on the persisted-state CLI writes ---------------
//
// User decision F3(a): every write path to persisted state is presented as an
// exact diff before it happens. U1 gated the providers.json verbs; this section
// covers the state.json writers reachable from the CLI doors and the profile
// deployment that rides a mode switch or restore:
//
//	W20  agy pro settings set <key> <value>
//	W16  agy pro config default-profile <p>
//	W17  agy pro config bridge-tools <t>
//	W18  agy pro hybrid on p1|p2
//	W19  agy pro hybrid off
//	W13  agy pro mode full|clean (and the interactive agy pro mode)
//	W15  agy pro mode restore
//	     plus the second doors agy pro settings mode switch|restore
//
// The gate is the CLI twin (confirmPersistedWrite), not the TUI modal: the
// front-ends own presentation, and Core verbs stay non-interactive (the seam
// contract in internal/settings/core.go). config/hybrid are NOT rerouted
// through Core.Set in this unit; the two-doors-per-key duplication they carry
// is recorded for the P5-3 single-owner work.

const (
	// stateWriteAtomicSentence is the pre-write presentation's atomic-0600 line
	// for one state.json scalar, the state twin of modelWriteAtomicSentence.
	stateWriteAtomicSentence = "state.json will be written (0600, atomic) with this exact diff:"
	// stateModeAtomicSentence is the same line for a mode switch or restore,
	// where state.json is written as the second half of the deployment.
	stateModeAtomicSentence = "state.json will be written (0600, atomic) with the mode change:"
)

// stateSnapshot is the presentation-time state.json read the state and mode
// plans are composed from. Tests replace it (with stateReload) so a command can
// be driven against an in-memory state without touching a live ~/.agy-pro.
var stateSnapshot = mode.LoadState

// stateReload is the write-time re-read: the same presented value is compared
// again immediately before the write (the G-C3 / CWE-367 rule the registry
// verbs follow), so a value that moved while the prompt waited refuses.
var stateReload = mode.LoadState

// splitYesFlag removes every --yes token from args and reports whether one was
// present. The positional doors (set, mode, hybrid) need it because the consent
// token may sit before or after the operands and must never be counted as one.
func splitYesFlag(args []string) (rest []string, yes bool) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if strings.TrimSpace(a) == "--yes" {
			yes = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, yes
}

// confirmStateWrite gates one state.json scalar write: the same mechanics as
// the registry gate, with the store named so the transcript says which file the
// diff describes.
func confirmStateWrite(in io.Reader, out, errw io.Writer, diff []string, yes bool) (bool, int) {
	return confirmPersistedWrite(in, out, errw, "[agy-pro] Approve State Write", diff, "state.json", "a state write", yes)
}

// confirmModeWrite gates one profile deployment (switch or restore). The write
// touches the live ~/.gemini/config tree AND state.json, so the prompt names
// the configuration rather than one file.
func confirmModeWrite(in io.Reader, out, errw io.Writer, diff []string, yes bool) (bool, int) {
	return confirmPersistedWrite(in, out, errw, "[agy-pro] Approve Profile Deployment", diff, "the live profile configuration", "a profile deployment", yes)
}

// stateScalarField maps one CLI settings key onto the state.json row the write
// touches and the raw field reader the write-time re-verify compares.
type stateScalarField struct {
	jsonKey string
	read    func(*mode.State) string
}

// stateScalarFieldFor resolves a CLI settings key to its persisted row. The
// spellings mirror Core.Set's closed key table (hyphen and underscore forms)
// plus hybrid_profile, which only the `agy pro hybrid` door writes. The six
// integration rows are the P5-3 T6 keys, so the presented diff, the write-time
// re-verify and the store all name the same row.
func stateScalarFieldFor(key string) (stateScalarField, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "default-profile", "default_profile":
		return stateScalarField{"default_profile", func(s *mode.State) string { return s.DefaultProfile }}, true
	case "bridge-tools", "bridge_tools":
		return stateScalarField{"bridge_tools", func(s *mode.State) string { return s.BridgeTools }}, true
	case "native-seat-concurrency", "native_seat_concurrency":
		return stateScalarField{"native_seat_concurrency", func(s *mode.State) string {
			return strconv.Itoa(s.NativeSeatConcurrency)
		}}, true
	case "language":
		return stateScalarField{"language", func(s *mode.State) string { return s.Language }}, true
	case "launcher-menu", "launcher_menu":
		return stateScalarField{"launcher_menu", func(s *mode.State) string { return s.LauncherMenu }}, true
	case "hybrid-profile", "hybrid_profile":
		return stateScalarField{"hybrid_profile", func(s *mode.State) string { return s.HybridProfile }}, true
	case "integration-preset", "integration_preset":
		return stateScalarField{"integration_preset", func(s *mode.State) string { return s.IntegrationPreset }}, true
	case "integration-statusline", "integration_statusline":
		return stateScalarField{"integration_statusline", func(s *mode.State) string { return s.IntegrationStatusline }}, true
	case "integration-hook", "integration_hook":
		return stateScalarField{"integration_hook", func(s *mode.State) string { return s.IntegrationHook }}, true
	case "integration-rules", "integration_rules":
		return stateScalarField{"integration_rules", func(s *mode.State) string { return s.IntegrationRules }}, true
	case "integration-plugins", "integration_plugins":
		return stateScalarField{"integration_plugins", func(s *mode.State) string { return s.IntegrationPlugins }}, true
	case "integration-skills", "integration_skills":
		return stateScalarField{"integration_skills", func(s *mode.State) string { return s.IntegrationSkills }}, true
	}
	return stateScalarField{}, false
}

// stateValueDisplay renders one state.json value for a transcript line: masked
// through the registry-slot masker (a scalar slot can carry a misdirected key
// paste, the same class regOperand covers for registry slots), and "(unset)"
// for the empty value the store drops through omitempty.
func stateValueDisplay(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(unset)"
	}
	return regOperand(v)
}

// stateScalarPlan is one presented state.json scalar write: the key that
// changes, the on-disk value it held when the diff was rendered, and the value
// the write will persist.
type stateScalarPlan struct {
	key      string
	jsonKey  string
	knownKey bool
	read     func(*mode.State) string
	before   string
	after    string
}

// snapshotStateScalar reads state.json and captures the presented state of one
// key before anything is printed. after is the value the write will persist
// (see afterStateValue); the plan renders it and re-verifies the before value.
func snapshotStateScalar(key, after string) (stateScalarPlan, error) {
	st, err := stateSnapshot()
	if err != nil || st == nil {
		return stateScalarPlan{}, errors.New("state.json could not be read before the write, so the presented diff could not be composed; nothing written")
	}
	f, known := stateScalarFieldFor(key)
	p := stateScalarPlan{key: key, jsonKey: strings.TrimSpace(key), knownKey: known, after: after}
	if known {
		p.jsonKey, p.read, p.before = f.jsonKey, f.read, f.read(st)
	}
	return p, nil
}

// displayKey renders the key name for the transcript: the persisted row name
// for a known key, the masked operand otherwise (Core.Set owns the refusal for
// an unknown key, so the plan must not echo it raw).
func (p stateScalarPlan) displayKey() string {
	if p.knownKey {
		return p.jsonKey
	}
	return regOperand(p.jsonKey)
}

// diff renders the exact-diff lines for one scalar write. An unknown key has no
// row to change, so the diff states that instead of inventing an old -> new line
// (Core.Set owns the refusal, and the key renders masked).
func (p stateScalarPlan) diff() []string {
	key := p.displayKey()
	lines := []string{fmt.Sprintf("Sets %s in state.json. No other key changes.", key)}
	if p.knownKey {
		lines = append(lines, fmt.Sprintf("  %s: %s -> %s", key, stateValueDisplay(p.before), stateValueDisplay(p.after)))
	} else {
		lines = append(lines, fmt.Sprintf("  %s is not a key this CLI writes; the store refuses unknown keys.", key))
	}
	return append(lines, stateWriteAtomicSentence)
}

// reverify re-reads state.json after consent and refuses the write when the
// presented key moved (G-C3). It returns the freshly loaded state so the caller
// writes the same values it re-verified instead of a stale in-memory copy.
func (p stateScalarPlan) reverify(verb string) (*mode.State, error) {
	st, err := stateReload()
	if err != nil || st == nil {
		return nil, errors.New("state.json re-read failed before the write, so the presented diff could not be re-verified; nothing written")
	}
	if !p.knownKey || p.read == nil {
		return st, nil
	}
	if cur := p.read(st); cur != p.before {
		return nil, fmt.Errorf("%s in state.json changed on disk between the presentation and the write (%s -> %s), so the presented diff is stale; nothing written (re-run %s after reviewing the current state)",
			p.jsonKey, stateValueDisplay(p.before), stateValueDisplay(cur), verb)
	}
	return st, nil
}

// afterStateValue projects the value `agy pro settings set` will persist, so
// the diff renders the bytes that land in state.json instead of the raw
// operand. The projection delegates to the store's own normalizers
// (mode.NormalizeProfile/NormalizeLanguage/NormalizeIntegrationToggle,
// settings.ParseBridgeToolsSpec, integrations.ParsePreset) and mirrors the
// closed key table of Core.Set; a value outside a closed set is rendered as
// given (masked by stateValueDisplay) and Core.Set refuses it after consent, so
// the CLI never owns a second validation authority.
func afterStateValue(key, value string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "default-profile", "default_profile":
		if mode.ValidProfile(value) {
			return mode.NormalizeProfile(value)
		}
	case "bridge-tools", "bridge_tools":
		if tools, err := settings.ParseBridgeToolsSpec(value); err == nil {
			return tools
		}
	case "native-seat-concurrency", "native_seat_concurrency":
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 1 && n <= mode.MaxNativeSeatConcurrency {
			return strconv.Itoa(n)
		}
	case "language":
		if mode.ValidLanguage(value) {
			return mode.NormalizeLanguage(value)
		}
	case "launcher-menu", "launcher_menu":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "on", "1", "true":
			return mode.LauncherMenuOn
		case "off", "0", "false":
			return mode.LauncherMenuOff
		}
	case "integration-preset", "integration_preset":
		if preset, err := integrations.ParsePreset(value); err == nil {
			return string(preset)
		}
	case "integration-statusline", "integration_statusline",
		"integration-hook", "integration_hook",
		"integration-rules", "integration_rules",
		"integration-plugins", "integration_plugins",
		"integration-skills", "integration_skills":
		if toggle := mode.NormalizeIntegrationToggle(value); toggle != "" {
			return toggle
		}
	}
	return value
}

// modeManagedEntry is one managed-set entry's presented on-disk state, the
// backup half of a switch and the pre-restore half of a restore.
type modeManagedEntry struct {
	name    string
	present bool
}

// modeAssetGroup is one top-level group of profile or backup files with its
// file count, so a 64-file deployment fits one frame.
type modeAssetGroup struct {
	name  string
	files int
}

// modePlan is the presented FILE PLAN for one mode switch or restore: the
// target, the current mode, the managed set with its on-disk state, and the
// file groups that will be written (switch) or restored (restore). It is signed
// so the write can re-verify it (see reverify).
type modePlan struct {
	restore     bool
	targetRaw   string
	target      string
	recognized  bool
	current     string
	configDir   string
	managed     []modeManagedEntry
	groups      []modeAssetGroup
	deployCount int
	sourceDir   string
	sourcePaths []string
	sourceCount int
	// sourceSkipped names the backup's top-level entries the managed register
	// does not own. The copy-back leaves them where they are, so the plan says so
	// instead of letting the filtered file count imply the backup was smaller.
	sourceSkipped []string
}

// normalizeModeTarget resolves a mode operand to the profile name the switch
// deploys. recognized is false for anything outside the alias set mode.Switch
// accepts; the plan then presents the managed set only and the switch itself
// owns the refusal (no second validation authority here).
func normalizeModeTarget(target string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "1", mode.ModeFull, "full-stack":
		return mode.ModeFull, true
	case "2", mode.ModeClean, "default":
		return mode.ModeClean, true
	}
	return strings.ToLower(strings.TrimSpace(target)), false
}

// targetDisplay renders the plan's target mode for a transcript line.
func (p modePlan) targetDisplay() string {
	if p.restore || p.recognized {
		return p.target
	}
	return regOperand(p.targetRaw)
}

// snapshotModeBase captures the state every mode presentation signs: the
// current mode, the config directory, and the on-disk presence of each managed
// entry (mode.DefaultManagedItems, the ONE deployment enumeration mode.Switch
// and mode.Restore walk, so the plan cannot drift from the write).
func snapshotModeBase() (modePlan, *mode.State, error) {
	st, err := stateSnapshot()
	if err != nil || st == nil {
		return modePlan{}, nil, errors.New("state.json could not be read before the write, so the presented file plan could not be composed; nothing written")
	}
	configDir, err := paths.GeminiConfigDir()
	if err != nil {
		return modePlan{}, nil, errors.New("the profile config directory could not be resolved before the write, so the presented file plan could not be composed; nothing written")
	}
	p := modePlan{current: st.CurrentMode, configDir: configDir}
	for _, item := range mode.DefaultManagedItems {
		_, statErr := os.Stat(filepath.Join(configDir, item))
		p.managed = append(p.managed, modeManagedEntry{name: item, present: statErr == nil})
	}
	return p, st, nil
}

// snapshotModeSwitch captures the file plan for one switch to target.
func snapshotModeSwitch(target string) (modePlan, error) {
	p, _, err := snapshotModeBase()
	if err != nil {
		return modePlan{}, err
	}
	p.targetRaw = target
	p.target, p.recognized = normalizeModeTarget(target)
	if !p.recognized {
		return p, nil
	}
	groups, count, err := countProfileAssets(p.target)
	if err != nil {
		return modePlan{}, fmt.Errorf("the %s profile assets could not be enumerated before the write, so the presented file plan could not be composed; nothing written", p.target)
	}
	p.groups, p.deployCount = groups, count
	return p, nil
}

// countProfileAssets walks the embedded profile the switch will deploy
// (profiles.GetProfileFS, the same filesystem mode.Switch walks) and counts its
// files per top-level group. No deployment list is invented here: a new asset
// added to the embedded profile appears in the plan automatically.
func countProfileAssets(profile string) ([]modeAssetGroup, int, error) {
	profileFS, err := profiles.GetProfileFS(profile)
	if err != nil {
		return nil, 0, err
	}
	counts := map[string]int{}
	var order []string
	total := 0
	err = fs.WalkDir(profileFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(order)
	groups := make([]modeAssetGroup, 0, len(order))
	for _, name := range order {
		groups = append(groups, modeAssetGroup{name: name, files: counts[name]})
	}
	return groups, total, nil
}

// resolveRestoreSource resolves the backup directory a restore copies from,
// exactly the way mode.Restore does: the recorded LastBackup when it still
// exists, otherwise the newest directory under the backups root
// (mode.ListBackups is the exported enumeration of that same root). The
// "no previous backups found to restore" sentence is mode.Restore's own, so the
// refusal reads the same from either door.
func resolveRestoreSource(st *mode.State) (string, error) {
	if st != nil {
		if dir := strings.TrimSpace(st.LastBackup); dir != "" {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				return dir, nil
			}
		}
	}
	backups, err := mode.ListBackups()
	if err != nil {
		return "", errors.New("the backup directories could not be listed before the write, so the presented file plan could not be composed; nothing written")
	}
	if len(backups) == 0 {
		return "", errors.New("no previous backups found to restore")
	}
	return backups[len(backups)-1], nil
}

// countRestoreSource lists what a restore will copy: the file count per
// top-level entry the managed register owns, plus the sorted relative file list
// (the state the plan signs for its write-time re-verify) and the names of the
// backup's other top-level entries, which the copy-back leaves where they are.
//
// The enumeration is filtered through mode.DefaultManagedItems because that is
// exactly what mode.Restore's copy-back walks (its second half skips every entry
// the register does not own). Counting the whole backup directory made the
// consent diff advertise rules/, skills/ and plugins/ on a legacy backup - files
// the narrowed write never touches (F-S9A-3). The skipped names are returned
// rather than dropped, so the plan can say what stays in place instead of
// silently shortening its own list.
func countRestoreSource(dir string) (groups []modeAssetGroup, total int, paths []string, skipped []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	for _, entry := range entries {
		if !modeManagedSourceEntry(entry.Name()) {
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
		groups = append(groups, modeAssetGroup{name: entry.Name(), files: count})
		total += count
	}
	sort.Strings(paths)
	return groups, total, paths, skipped, nil
}

// modeManagedSourceEntry reports whether the managed register owns a top-level
// backup entry. It is the same closed register mode.Restore walks
// (mode.DefaultManagedItems), so the presented plan and the copy-back cannot
// disagree about which entries a restore writes - including the case-fold
// half: the copy-back resolves its source with Join+Stat and Win32 stats
// case-insensitively, so a byte-exact answer here would count 0 files for a
// backup entry named AGENTS that the same run copies to agents/.
func modeManagedSourceEntry(name string) bool {
	for _, item := range mode.DefaultManagedItems {
		if strings.EqualFold(item, name) {
			return true
		}
	}
	return false
}

// snapshotModeRestore captures the file plan for one restore of the newest
// profile backup.
func snapshotModeRestore() (modePlan, error) {
	p, st, err := snapshotModeBase()
	if err != nil {
		return modePlan{}, err
	}
	p.restore = true
	p.target = "restored"
	source, err := resolveRestoreSource(st)
	if err != nil {
		return modePlan{}, err
	}
	p.sourceDir = source
	groups, count, paths, skipped, err := countRestoreSource(source)
	if err != nil {
		return modePlan{}, fmt.Errorf("the restore source could not be read before the write, so the presented file plan could not be composed; nothing written")
	}
	p.groups, p.sourceCount, p.sourcePaths, p.sourceSkipped = groups, count, paths, skipped
	return p, nil
}

// diff renders the exact file plan for one switch or restore.
func (p modePlan) diff() []string {
	var lines []string
	if p.restore {
		lines = append(lines,
			"Restores the newest profile backup over the live profile configuration.",
			fmt.Sprintf("Current mode: %s -> %s", stateValueDisplay(p.current), p.targetDisplay()),
			fmt.Sprintf("Config dir: %s", p.configDir),
			fmt.Sprintf("Restore source: %s (%d files)", p.sourceDir, p.sourceCount),
		)
		if len(p.sourceSkipped) > 0 {
			lines = append(lines, fmt.Sprintf("Left in place (not a mode-managed item): %s",
				strings.Join(p.sourceSkipped, ", ")))
		}
		for _, g := range p.groups {
			lines = append(lines, fmt.Sprintf("  %s (%s)", g.name, fileCountWord(g.files)))
		}
		lines = append(lines, fmt.Sprintf("Current managed entries move to a pre-restore backup first (under %s):", filepath.Dir(p.sourceDir)))
	} else {
		lines = append(lines,
			fmt.Sprintf("Switches the active profile: %s -> %s", stateValueDisplay(p.current), p.targetDisplay()),
			fmt.Sprintf("Config dir: %s", p.configDir),
			"Managed entries move to a timestamped backup under the agy-pro backups directory, then the new profile is deployed:",
		)
	}
	for _, m := range p.managed {
		state := "absent"
		if m.present {
			state = "present (backed up)"
		}
		lines = append(lines, fmt.Sprintf("  %-14s %s", m.name, state))
	}
	if !p.restore {
		if p.recognized {
			lines = append(lines, fmt.Sprintf("Deploys %s from the embedded %s profile:", fileCountWord(p.deployCount), p.target))
			for _, g := range p.groups {
				lines = append(lines, fmt.Sprintf("  %s (%s)", g.name, fileCountWord(g.files)))
			}
		} else {
			lines = append(lines, fmt.Sprintf("Deploys: the %s profile (unrecognized target: the switch refuses after consent)", p.targetDisplay()))
		}
	}
	return append(lines, stateModeAtomicSentence, fmt.Sprintf("  current_mode: %s -> %s", stateValueDisplay(p.current), p.targetDisplay()))
}

// reverify re-reads the presented state after consent and refuses the write
// when the mode, the managed set, or (for a restore) the restore source's
// signed STATE moved: the file count plus the sorted relative path list the
// plan presented. The signature covers the file SET, not the file BYTES, so a
// same-name content rewrite inside the consent window is not detected here;
// signing a sha256 per path (as deploy's StaleError does for its plan) is the
// follow-up that would close it. Nothing has been written at this point: the
// deployment has not started.
func (p modePlan) reverify(verb string) error {
	st, err := stateReload()
	if err != nil || st == nil {
		return errors.New("state.json re-read failed before the write, so the presented file plan could not be re-verified; nothing written")
	}
	if st.CurrentMode != p.current {
		return fmt.Errorf("current_mode in state.json changed on disk between the presentation and the write (%s -> %s), so the presented file plan is stale; nothing written (re-run %s after reviewing the current state)",
			stateValueDisplay(p.current), stateValueDisplay(st.CurrentMode), verb)
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
	_, total, paths, _, err := countRestoreSource(p.sourceDir)
	if err != nil {
		return fmt.Errorf("the restore source %s could not be re-read before the write, so the presented file plan could not be re-verified; nothing written", p.sourceDir)
	}
	if total != p.sourceCount || !equalStringList(paths, p.sourcePaths) {
		return fmt.Errorf("the restore source %s changed on disk between the presentation and the write, so the presented file plan is stale; nothing written (re-run %s after reviewing the backup)", p.sourceDir, verb)
	}
	return nil
}

// fileCountWord renders a file count in the TUI's plural form ("1 file" /
// "N files"), the shape providerCountWord uses for model counts.
func fileCountWord(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// --- P5-1 U3: exact-diff gate on the CLI key-repair doors ---------------------
//
// User decision F3(a): the key-repair stack writes persisted state and every
// write path is presented as an exact diff first. The writers are W32 (the
// HKCU\Environment key names plus this process's environment), W33 (the DPAPI
// backup file that rides the same operation) and W34 (the rollback), and the
// hub already gates all three (approveFixKeys, screen_settings.go:890;
// approveRollbackKeys, :964). The two CLI doors were ungated; this section is
// their console twin:
//
//	W32/W33  agy pro doctor --fix-keys [--yes]
//	         agy pro settings doctor fix-keys [targets] [--yes]
//	W34      agy pro doctor --rollback-keys <backup-dir> [--yes]
//	         agy pro settings doctor rollback-keys <dir> [--yes]
//
// Information-shape parity with the hub: the fix-keys diff is one line per
// resolved target under a precondition sentence, because the exact per-target
// mutation is decided by live upstream probes the hub deliberately does not run
// twice (screen_settings.go:853-856). The CLI mirror keeps that shape and adds
// the probe-free half of the decision it can see without re-probing: the stored
// clean/BOM-variant entries per target, which is what tells an operator whether
// a target will be rewritten or kept. The rollback diff mirrors
// approveRollbackKeys exactly: one restore/delete line per recorded backup
// entry, read from the file before consent.
//
// Values never render: key NAMES route through MaskOperand (env and variant
// names) and MaskSlot (the provider registry slot), values stay in the store,
// and the backup file is read for its entry names only.

const (
	// repairDiffEntryBudget bounds one key-repair diff's per-entry lines. The
	// hub bounds its modal body to the live frame (approvalBodyBudget +
	// capApprovalEntries, screen_settings.go:814/:832) so the gate legend can
	// never be cut off; the console has no frame, so the CLI mirror holds the
	// hub's 80x24 body budget as a constant and reuses its tail sentence, which
	// keeps an explicitly partial diff honest instead of a silent truncation.
	repairDiffEntryBudget = 20
	// repairDiffEntriesHidden is the hub's bounded-tail sentence
	// (approvalEntriesHidden, screen_settings.go:801), byte-identical.
	repairDiffEntriesHidden = "  ... %d more entries NOT shown (the diff above is partial)"
)

// errRollbackBackupShape marks a key backup file that exists but cannot be read
// as one. The door renders it as a pre-consent refusal instead of the
// byte-stable "Rollback failed:" tail that a store error takes.
var errRollbackBackupShape = errors.New("the backup file is not a readable key backup")

// repairCandidateSource reads the persistent Windows-environment entries stored
// for one variable name (the clean entry plus any BOM variants of it). It is
// the presentation-time read the fix-keys diff is composed from: the same
// providers.ResolveEnvCandidates authority the key audit and the wizard use.
// Tests replace it so a diff can be driven without a live environment store.
var repairCandidateSource = providers.ResolveEnvCandidates

// repairBackupBase names the directory the repair writes its timestamped backup
// under, the same base writeRepairBackup derives from the agy-pro home.
func repairBackupBase() string {
	if dir, err := paths.AgyProBackups(); err == nil && dir != "" {
		return dir
	}
	return "(the agy-pro backups directory)"
}

// repairPlanEntry is one resolved repair target: the env NAME the repair may
// write, the provider that declares it, and the probe-free verdict the diff can
// state (rewrite from a stored BOM variant, or keep).
type repairPlanEntry struct {
	env      string
	provider string
	rewrite  bool
	variant  string // raw BOM-variant name the rewrite copies from
	// variantCount is how many BOM variants of this name are stored; the repair
	// promotes exactly one of them.
	variantCount int
	keepWhy      string // why a non-rewritten target is kept
}

func (e repairPlanEntry) action() string {
	if e.rewrite {
		return "rewrite"
	}
	return "keep"
}

// repairVariantDisplay renders a stored BOM-variant name with the invisible
// prefix made visible ("<BOM>NAME"). The BOM is stripped first and the bare name
// is masked, so a key-shaped variant name cannot survive the annotation.
func repairVariantDisplay(name string) string {
	return "<BOM>" + providers.MaskOperand(stripDiffControls(strings.ReplaceAll(name, providers.BOM, "")))
}

// diffLine renders one target as the operator saw it requested: masked env
// name, masked provider slot, and the probe-free action.
func (e repairPlanEntry) diffLine() string {
	provider := "-"
	if strings.TrimSpace(e.provider) != "" {
		provider = providers.MaskSlot(e.provider)
	}
	env := providers.MaskOperand(e.env)
	if !e.rewrite {
		return fmt.Sprintf("  %s (provider %s): keep (%s)", env, provider, stripDiffControls(e.keepWhy))
	}
	line := fmt.Sprintf("  %s (provider %s): rewrite the clean name from BOM variant %s, then delete that variant",
		env, provider, repairVariantDisplay(e.variant))
	if e.variantCount > 1 {
		line += fmt.Sprintf(" (%d BOM variants stored; one is promoted)", e.variantCount)
	}
	return line
}

// rollbackPlanEntry is one recorded backup entry: the root it writes, the NAME
// it writes, and whether the value is restored or the name removed.
type rollbackPlanEntry struct {
	root    string
	name    string
	existed bool
}

func (e rollbackPlanEntry) diffLine() string {
	root := "HKCU\\Environment"
	if e.root == providers.EnvRootProcess {
		root = "the process environment"
	}
	verb := "delete "
	if e.existed {
		verb = "restore"
	}
	return fmt.Sprintf("  %s %s in %s", verb, providers.MaskOperand(e.name), root)
}

// keyRepairPlan is the presented repair (or rollback) intent plus its signed
// state: the fix-keys half signs the per-target entry set, the rollback half
// signs the backup file's recorded entry set.
type keyRepairPlan struct {
	rollback bool
	target   string
	names    []string
	entries  []repairPlanEntry
	file     string
	restores []rollbackPlanEntry
}

// maskRepairNames renders a resolved target list for a transcript. Env names
// are environment variable NAMEs, so they stay on MaskOperand.
func maskRepairNames(names []string) string {
	masked := make([]string, len(names))
	for i, n := range names {
		masked[i] = providers.MaskOperand(n)
	}
	return strings.Join(masked, ", ")
}

// providerForEnv resolves the registry provider that declares one API-key
// variable, the same first-match walk the repair itself uses.
func providerForEnv(reg *providers.Registry, env string) string {
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

// composeRepairPlan reads the stored entries for every resolved target and
// states the probe-free half of the decision: a target that has a BOM variant
// stored is a rewrite candidate, a target without one is kept. The per-target
// precondition (the clean name absent or probe-invalid, the variant
// probe-valid) is owned by the repair itself and stated once in the diff
// header, so the approval is never broader than the operation.
func composeRepairPlan(reg *providers.Registry, names []string) []repairPlanEntry {
	out := make([]repairPlanEntry, 0, len(names))
	for _, env := range names {
		env = strings.TrimSpace(env)
		if env == "" {
			continue
		}
		e := repairPlanEntry{env: env, provider: providerForEnv(reg, env)}
		var exact, variants []providers.EnvCandidate
		for _, c := range repairCandidateSource(env) {
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

// snapshotKeyRepairPlan composes the fix-keys diff from the resolved targets.
func snapshotKeyRepairPlan(reg *providers.Registry, target string, names []string) keyRepairPlan {
	return keyRepairPlan{target: target, names: names, entries: composeRepairPlan(reg, names)}
}

// rollbackBackupSnapshot mirrors the on-disk key-backup layout for the rollback
// diff. Only entry names, roots and existence flags are decoded; values never
// reach this struct because the field is not declared.
type rollbackBackupSnapshot struct {
	Entries []struct {
		Root    string `json:"root"`
		Name    string `json:"name"`
		Existed bool   `json:"existed"`
	} `json:"entries"`
}

// keyBackupReadHook runs before every backup-file read the rollback door makes
// (the presentation read and the write-time re-read). Production leaves it nil;
// tests use it to land an external edit exactly where the re-read happens.
var keyBackupReadHook func()

// snapshotKeyRollbackPlan reads the backup file the rollback will apply and
// composes its diff before any consent is asked. A missing or unparsable file
// refuses here, so an unreadable backup never reaches a consent prompt.
func snapshotKeyRollbackPlan(target string) (keyRepairPlan, error) {
	p := keyRepairPlan{rollback: true, target: target}
	p.file = providers.ResolveBackupFile(target)
	if keyBackupReadHook != nil {
		keyBackupReadHook()
	}
	raw, err := os.ReadFile(p.file)
	if err != nil {
		return keyRepairPlan{}, err
	}
	var snap rollbackBackupSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return keyRepairPlan{}, fmt.Errorf("%w: %v", errRollbackBackupShape, err)
	}
	for _, e := range snap.Entries {
		p.restores = append(p.restores, rollbackPlanEntry{root: e.Root, name: e.Name, existed: e.Existed})
	}
	return p, nil
}

// capRepairDiffEntries applies the hub's bounded-entry rule to a CLI diff: the
// entries that fit the budget render byte-identically, and a longer list is cut
// to them plus ONE tail line naming exactly how many entries are not shown.
func capRepairDiffEntries(entries []string) []string {
	if len(entries) <= repairDiffEntryBudget {
		return entries
	}
	visible := repairDiffEntryBudget - 1
	out := make([]string, 0, visible+1)
	out = append(out, entries[:visible]...)
	out = append(out, fmt.Sprintf(repairDiffEntriesHidden, len(entries)-visible))
	return out
}

// maskedBackupFileLine renders the resolved backup file for a transcript: a
// key-shaped or decorated operand must not survive the "Backup file:" line, so
// the whole-string classifier runs first and every operand carrying a separator
// class also takes the seeded sentence pass (seeded from the raw target and the
// resolved file).
func maskedBackupFileLine(target, file string) string {
	line := providers.MaskOperand(file)
	if strings.ContainsAny(file, " \t\n\r,;") {
		line = maskOperandSentence(line, append(providers.FlagSeedOperands(target), file)...)
	}
	return line
}

// diff renders the exact presented plan for one fix-keys or rollback op.
func (p keyRepairPlan) diff() []string {
	if p.rollback {
		lines := []string{
			"Restores the recorded key names in HKCU\\Environment and this process's environment from one key backup.",
			"Backup file: " + maskedBackupFileLine(p.target, p.file),
		}
		if len(p.restores) == 0 {
			lines = append(lines, "The backup records no entries: the rollback refuses after consent (nothing written).")
		}
		entries := make([]string, 0, len(p.restores))
		for _, e := range p.restores {
			entries = append(entries, e.diffLine())
		}
		lines = append(lines, capRepairDiffEntries(entries)...)
		return append(lines, "The backup file is only read; nothing under the backup directory changes.")
	}
	lines := []string{
		"Repairs BOM-polluted provider key names in HKCU\\Environment and this process's environment.",
		"Per-target action is probe-gated: a target whose BOM-variant value does not authenticate upstream is kept, and a clean name that already validates upstream is kept.",
	}
	entries := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e.diffLine())
	}
	lines = append(lines, capRepairDiffEntries(entries)...)
	return append(lines,
		fmt.Sprintf("One DPAPI-encrypted backup of every value it touches is written first (0600, atomic) under %s; values are copied, never printed.", repairBackupBase()),
		"Writes the key NAMES above; the process environment and HKCU\\Environment are the only stores it touches.",
	)
}

// repairPlanDrift names the first difference between the presented fix-keys
// plan and a fresh one, or "" when the entry sets match.
func repairPlanDrift(presented, fresh []repairPlanEntry) string {
	if len(presented) != len(fresh) {
		return fmt.Sprintf("the target count moved from %d to %d", len(presented), len(fresh))
	}
	for i := range presented {
		a, b := presented[i], fresh[i]
		if a.env != b.env {
			return fmt.Sprintf("target %d moved from %s to %s", i+1, providers.MaskOperand(a.env), providers.MaskOperand(b.env))
		}
		if a.action() != b.action() {
			return fmt.Sprintf("%s moved from %s to %s", providers.MaskOperand(a.env), a.action(), b.action())
		}
		if a.variant != b.variant {
			return fmt.Sprintf("the stored BOM variant of %s changed", providers.MaskOperand(a.env))
		}
	}
	return ""
}

// rollbackPlanDrift names the first difference between the presented rollback
// entry set and a fresh read of the same file, or "" when they match.
func rollbackPlanDrift(presented, fresh []rollbackPlanEntry) string {
	if len(presented) != len(fresh) {
		return fmt.Sprintf("the recorded entry count moved from %d to %d", len(presented), len(fresh))
	}
	for i := range presented {
		a, b := presented[i], fresh[i]
		if a.name != b.name || a.root != b.root {
			return fmt.Sprintf("entry %d became %s", i+1, providers.MaskOperand(b.name))
		}
		if a.existed != b.existed {
			return fmt.Sprintf("the recorded existence of %s changed", providers.MaskOperand(a.name))
		}
	}
	return ""
}

// reverify re-reads the presented state after consent and refuses the write
// when the entry set moved. Nothing has been written at this point: no HKCU
// value, no process environment value, and no backup file.
func (p keyRepairPlan) reverify(verb string) error {
	if p.rollback {
		fresh, err := snapshotKeyRollbackPlan(p.target)
		if err != nil {
			return errors.New("the key backup could not be re-read before the write, so the presented diff could not be re-verified; nothing written (HKCU untouched)")
		}
		if drift := rollbackPlanDrift(p.restores, fresh.restores); drift != "" {
			return fmt.Errorf("the key backup changed on disk between the presentation and the write (%s), so the presented diff is stale; nothing written (HKCU untouched; re-run %s after reviewing the backup)", drift, verb)
		}
		return nil
	}
	// The signed state is the resolved target set plus the per-target entry
	// verdict: the registry can be edited under the prompt, and the second door
	// re-resolves its targets from the registry inside the Core verb, so a
	// target set that moved must refuse here instead of being applied.
	reg, err := registrySnapshot()
	if err != nil {
		return errors.New("the registry could not be re-read before the write, so the presented key-repair plan could not be re-verified; nothing written (HKCU untouched)")
	}
	names, err := resolveRepairTargets(reg, p.target)
	if err != nil {
		return errors.New("the repair targets could not be re-resolved before the write, so the presented key-repair plan could not be re-verified; nothing written (HKCU untouched)")
	}
	if !equalStringList(names, p.names) {
		return fmt.Errorf("the resolved repair target set changed on disk between the presentation and the write (it was %s, it is now %s), so the presented diff is stale; nothing written (HKCU untouched, no backup file written; re-run %s after reviewing the registry)",
			maskRepairNames(p.names), maskRepairNames(names), verb)
	}
	fresh := composeRepairPlan(reg, p.names)
	if drift := repairPlanDrift(p.entries, fresh); drift != "" {
		return fmt.Errorf("the key-repair plan changed on disk between the presentation and the write (%s), so the presented diff is stale; nothing written (HKCU untouched, no backup file written; re-run %s after reviewing the keys)", drift, verb)
	}
	return nil
}

// confirmKeyRepairWrite gates one Windows-environment key write (repair or
// rollback): the W32/W34 binding of confirmPersistedWrite. W33 (the DPAPI
// backup file) rides this decision, and the prompt names both stores the write
// reaches.
func confirmKeyRepairWrite(in io.Reader, out, errw io.Writer, diff []string, yes bool) (bool, int) {
	return confirmPersistedWrite(in, out, errw, "[agy-pro] Approve Key Repair Write", diff,
		"HKCU\\Environment and this process's environment", "a key-repair write", yes)
}
