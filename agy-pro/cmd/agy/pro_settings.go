package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"agypro/internal/openaiup"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// Settings surface command (contract §2.6, U6):
//
//	agy pro settings                      full-screen keybinding-driven TUI (M2)
//	agy pro settings --panel <id>         deep-link one TUI panel
//	agy pro settings show [--json]        SettingsView snapshot (non-interactive)
//	agy pro settings set <key> <value>    default-profile | bridge-tools | native-seat-concurrency
//	agy pro settings providers ...        list | add-json | add-interactive | test | rename | delete |
//	                                      model-add-json | model-update | model-delete
//	                                      (the model twins gate the write: console stdin prompts y/n,
//	                                      non-interactive stdin requires the explicit --yes flag, and
//	                                      model-update re-verifies the presented record after consent)
//	agy pro settings mode ...             switch | restore | list | status
//	agy pro settings doctor ...           diagnose | fix-keys | rollback-keys
//	agy pro settings update-info | uninstall-info | version-info
//
// Phase 1: unused-but-compilable; U-wiring flips main.go's dispatch line to
// os.Exit(runSettingsCmd(subArgs, os.Stdin, os.Stdout, os.Stderr)).

const proSettingsUsage = `Usage: agy pro settings [--panel <id>] | show [--json] | set <key> <value> |
       providers <list|add-json <file|->|add-interactive|test <name>|rename <old> <new>|delete <name>|
                  model-add-json <name> <file|-> [--yes]|model-update <name> <model-id> <file|-> [--yes]|
                  model-delete <name> <model-id> [--yes]> |
       mode <switch <full|clean>|restore|list|status> |
       doctor <diagnose [--strict]|fix-keys [targets]|rollback-keys <dir>> |
       update-info | uninstall-info | version-info`

// newSettingsCore builds the production Core. Tests replace this seam.
var newSettingsCore = func() *settings.Core {
	return settings.NewCore(settings.Deps{
		VersionFunc: func() string { return Version },
	})
}

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
		if len(args) < 3 {
			fmt.Fprintf(errw, "[agy-pro] set requires <key> <value>\n%s\n", proSettingsUsage)
			return 2
		}
		if err := core.Set(args[1], args[2]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] %s = %s\n", providers.MaskOperand(args[1]), providers.MaskOperand(args[2]))
		return 0
	case "providers":
		return settingsProvidersCmd(core, args[1:], in, out, errw)
	case "mode":
		return settingsModeCmd(core, args[1:], out, errw)
	case "doctor":
		return settingsDoctorCmd(core, args[1:], out, errw)
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
		if err := core.RunWizard(in, out); err != nil {
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
		if err := core.ProviderRename(args[1], args[2]); err != nil {
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
		if err := core.ProviderDelete(args[1]); err != nil {
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

func settingsModeCmd(core *settings.Core, args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(errw, "[agy-pro] mode requires a subcommand\n%s\n", proSettingsUsage)
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "switch":
		if len(args) < 2 {
			fmt.Fprintf(errw, "[agy-pro] switch requires <full|clean>\n%s\n", proSettingsUsage)
			return 2
		}
		if err := core.ModeSwitch(args[1]); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] mode switched to %s\n", strings.ToLower(args[1]))
		return 0
	case "restore":
		if err := core.ModeRestore(); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintln(out, "[SUCCESS] latest profile backup restored")
		return 0
	case "list", "backups":
		paths, err := core.ModeBackups()
		if err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		for i, p := range paths {
			fmt.Fprintf(out, "  %d. %s\n", i+1, p)
		}
		if len(paths) == 0 {
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
		fmt.Fprintf(errw, "[agy-pro] unknown mode subcommand %q\n%s\n", providers.MaskOperand(args[0]), proSettingsUsage)
		return 2
	}
}

func settingsDoctorCmd(core *settings.Core, args []string, out, errw io.Writer) int {
	if len(args) == 0 {
		args = []string{"diagnose"}
	}
	switch {
	case args[0] == "diagnose":
		strict := len(args) > 1 && args[1] == "--strict"
		if err := core.DoctorDiagnose(out, strict); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		return 0
	case args[0] == "fix-keys":
		target := "all"
		if len(args) > 1 {
			target = args[1]
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
	case args[0] == "rollback-keys":
		if len(args) < 2 {
			fmt.Fprintf(errw, "[agy-pro] rollback-keys requires a backup dir\n%s\n", proSettingsUsage)
			return 2
		}
		if err := core.DoctorRollbackKeys(args[1]); err != nil {
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
			fmt.Fprintf(errw, "[agy-pro] %s\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(args[1], providers.ResolveBackupFile(args[1]))...))
			return 1
		}
		fmt.Fprintln(out, "[SUCCESS] registry key names restored from backup")
		return 0
	default:
		fmt.Fprintf(errw, "[agy-pro] unknown doctor subcommand %q\n%s\n", providers.MaskOperand(args[0]), proSettingsUsage)
		return 2
	}
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
// before it happens, never reported after the fact).
//
// Console stdin prompts y/n: n, Esc, or an empty answer aborts with nothing
// written. Piped or CI stdin has no one to ask, so the verb requires the
// explicit --yes flag: without it the diff still prints and the verb refuses
// with an honest error naming the flag. Console detection rides the stdinIsTTY
// seam (pro_swarm.go), the same swappable seam the other entry units use. The
// seam alone cannot tell a real console from /dev/null or NUL (both are
// character devices), so an immediate EOF at the prompt (zero bytes, read
// error) takes the same refusal as a pipe instead of a silent cancel.
func confirmRegistryMutation(in io.Reader, out, errw io.Writer, diff []string, yes bool) (proceed bool, code int) {
	fmt.Fprintln(out, "[agy-pro] Approve Registry Write")
	for _, line := range diff {
		fmt.Fprintln(out, "  "+line)
	}
	if yes {
		return true, 0
	}
	if stdinIsTTY(in) {
		fmt.Fprint(out, "Apply this write to providers.json? [y/N]: ")
		// Round-6 seat2-F4: bound the prompt read. A character device without
		// a newline (for example /dev/zero or NUL) would otherwise buffer
		// memory without limit (CWE-400, local self-DoS). 64 bytes is far past
		// any y/n answer: an over-long line is read up to the cap and then
		// treated exactly like any non-y answer, so nothing writes. The
		// immediate-EOF refusal below is unchanged (answer == "" with an error).
		answer, err := bufio.NewReader(io.LimitReader(in, 64)).ReadString('\n')
		if err != nil && answer == "" {
			fmt.Fprintln(errw, "[agy-pro] refusal: non-interactive stdin cannot consent to a registry write; re-run with --yes after reviewing the diff above (nothing written)")
			return false, 1
		}
		if strings.EqualFold(strings.TrimSpace(answer), "y") {
			return true, 0
		}
		fmt.Fprintln(out, "[CANCELLED] nothing written")
		return false, 0
	}
	fmt.Fprintln(errw, "[agy-pro] refusal: non-interactive stdin cannot consent to a registry write; re-run with --yes after reviewing the diff above (nothing written)")
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
