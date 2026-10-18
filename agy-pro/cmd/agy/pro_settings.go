package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
//	                                      non-interactive stdin requires the explicit --yes flag)
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
		fmt.Fprintf(out, "[SUCCESS] provider %s saved (apiKeyEnv %s - env-var NAME only)\n", providers.MaskOperand(spec.Name), providers.MaskOperand(spec.APIKeyEnv))
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
		fmt.Fprintf(out, "[SUCCESS] provider %s key probe ok\n", providers.MaskOperand(args[1]))
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
		fmt.Fprintf(out, "[SUCCESS] provider %s renamed to %s\n", providers.MaskOperand(args[1]), providers.MaskOperand(args[2]))
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
		fmt.Fprintf(out, "[SUCCESS] provider %s removed from the registry (key values untouched)\n", providers.MaskOperand(args[1]))
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
		fmt.Fprintf(out, "[SUCCESS] provider %s: model %s added\n", providers.MaskOperand(args[1]), providers.MaskOperand(m.ID))
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
		proceed, code := confirmRegistryMutation(in, out, errw, modelUpdateDiff(args[1], args[2], m), yes)
		if !proceed {
			return code
		}
		if err := core.ModelUpdate(args[1], args[2], m); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "[SUCCESS] provider %s: model %s updated\n", providers.MaskOperand(args[1]), providers.MaskOperand(m.ID))
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
		fmt.Fprintf(out, "[SUCCESS] provider %s: model %s removed\n", providers.MaskOperand(args[1]), providers.MaskOperand(args[2]))
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
// before Core ever sees the record. The classifier mirrors the TUI form
// guard in internal/tui/screen_model_form.go (secretValuePrefixes plus
// credentialTokenShaped) verbatim, so the CLI ingest and the TUI form cannot
// drift apart: known credential prefixes at any length, plus one
// uninterrupted opaque run of 16+ runes carrying BOTH letters and digits.
// Separators and single-class runs (long words, all-letter ids, serials)
// stay readable on both paths.
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
	if modelSpecValueLooksSecret(m.Capabilities.ReasoningEffort) {
		return providers.Model{}, fmt.Errorf("model spec: field %q looks like secret material (value hidden)", "reasoningEffort")
	}
	return m, nil
}

// modelSpecSecretPrefixes mirrors secretValuePrefixes in
// internal/tui/screen_model_form.go: a value starting with any of these
// reads as key material in any slot, at any length.
var modelSpecSecretPrefixes = []string{
	"sk-", "key-", "gsk_", "nvapi-", "live_", "secret_",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
	"xox", "glpat-", "npm_", "dckr_pat-", "dckr_pat_", "bearer ", "eyj",
}

// modelSpecValueLooksSecret reports whether a free-text model field carries
// credential material rather than a name: a known credential prefix at any
// length (still a secret behind the prefix), or one uninterrupted run of the
// base64 alphabet of 16+ runes with BOTH letter and digit classes present
// and no other separator (the AKIA/AWS-secret-class opaque token). This is
// credentialTokenShaped from the TUI form, kept rune-for-rune so the two
// guards agree on every input.
func modelSpecValueLooksSecret(v string) bool {
	t := strings.TrimSpace(v)
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	for _, p := range modelSpecSecretPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return modelSpecOpaqueToken(t)
}

// modelSpecOpaqueToken mirrors credentialTokenShaped in
// internal/tui/screen_model_form.go: one uninterrupted run of the base64
// alphabet (ASCII letters, digits, and the + / = pad runes), at least 16
// runes long, with both letter and digit classes present. Any other
// separator rune (- . _ : whitespace and friends) or a single-class run
// fails the shape and stays readable. Round-6 F-01: the + / = runes are
// admitted (a 16+ rune base64 AWS-secret-class credential carries them);
// the body stays rune-for-rune identical to the TUI twin.
func modelSpecOpaqueToken(s string) bool {
	n := 0
	letter, digit := false, false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
		case r >= '0' && r <= '9':
			digit = true
		case r == '+', r == '/', r == '=':
		default:
			return false
		}
		n++
	}
	return n >= 16 && letter && digit
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
func lookupRegistryModel(providerName, modelID string) (before providers.Model, count int, found, modelFound bool) {
	reg, err := providers.Load()
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

// modelAddDiff renders the masked pre-write presentation for model-add-json:
// the mutation sentence, the model count before/after when the on-disk
// provider resolves, the atomic-0600 sentence, and one "+ field: value" line
// per set field (every operand masked).
func modelAddDiff(providerName string, m providers.Model) []string {
	lines := []string{
		fmt.Sprintf("Adds model %s to provider %s. Key values are untouched.", providers.MaskOperand(m.ID), providers.MaskOperand(providerName)),
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
// record. When the on-disk record is unavailable the diff falls back to
// after-only lines so the presentation never invents before values.
func modelUpdateDiff(providerName, modelID string, m providers.Model) []string {
	lines := []string{
		fmt.Sprintf("Updates model %s in provider %s. Key values are untouched.", providers.MaskOperand(m.ID), providers.MaskOperand(providerName)),
	}
	before, count, found, modelFound := lookupRegistryModel(providerName, modelID)
	if found {
		lines = append(lines, fmt.Sprintf("Model count: %d -> %d", count, count))
	}
	lines = append(lines, modelWriteAtomicSentence)
	if found && modelFound {
		return append(lines, modelChangedDiffLines(before, m)...)
	}
	return append(lines, modelAddDiffLines(m)...)
}

// modelDeleteDiff renders the masked pre-write presentation for model-delete.
func modelDeleteDiff(providerName, modelID string) []string {
	lines := []string{
		fmt.Sprintf("Removes model %s from provider %s. Key values are untouched.", providers.MaskOperand(modelID), providers.MaskOperand(providerName)),
	}
	if _, count, found, _ := lookupRegistryModel(providerName, modelID); found {
		lines = append(lines, fmt.Sprintf("Model count: %d -> %d", count, count-1))
	}
	lines = append(lines, modelWriteAtomicSentence)
	return append(lines, "- model "+providers.MaskOperand(modelID))
}

// maskJoined masks each list item individually before joining, the TUI
// maskJoined rule (screen_model_form.go) ported to the CLI twins: one
// credential-shaped entry cannot hide behind the join separator of its
// neighbors, and MaskOperand on an already-joined string would see the
// separators and stay readable.
func maskJoined(items []string, sep string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = providers.MaskOperand(it)
	}
	return strings.Join(parts, sep)
}

// modelAddDiffLines renders one "+ field: value" line per set field of the
// incoming record (mirrors the M4 modal's addedDiffLines).
func modelAddDiffLines(m providers.Model) []string {
	lines := []string{}
	add := func(field, value string) {
		lines = append(lines, "+ "+field+": "+providers.MaskOperand(value))
	}
	add("id", m.ID)
	if m.Family != "" {
		add("family", m.Family)
	}
	if len(m.Tags) > 0 {
		add("tags", maskJoined(m.Tags, ", "))
	}
	if m.Enabled != nil && !*m.Enabled {
		add("enabled", "false")
	}
	if m.ContextLength > 0 {
		add("contextLength", strconv.Itoa(m.ContextLength))
	}
	if m.MaxOutputTokens > 0 {
		add("maxOutputTokens", strconv.Itoa(m.MaxOutputTokens))
	}
	if len(m.Capabilities.AllowedTools) > 0 {
		add("tools", maskJoined(m.Capabilities.AllowedTools, ", "))
	}
	return lines
}

// modelChangedDiffLines renders one "  field: before -> after" line per
// changed field (mirrors the M4 modal's changedDiffLines), or the no-changes
// marker.
func modelChangedDiffLines(before, m providers.Model) []string {
	lines := []string{}
	chg := func(field, b, a string) {
		if b == a {
			return
		}
		lines = append(lines, "  "+field+": "+providers.MaskOperand(b)+" -> "+providers.MaskOperand(a))
	}
	chg("id", before.ID, m.ID)
	chg("family", before.Family, m.Family)
	chg("tags", maskJoined(before.Tags, ", "), maskJoined(m.Tags, ", "))
	chg("enabled", strconv.FormatBool(before.IsEnabled()), strconv.FormatBool(m.IsEnabled()))
	chg("contextLength", strconv.Itoa(before.ContextLength), strconv.Itoa(m.ContextLength))
	chg("maxOutputTokens", strconv.Itoa(before.MaxOutputTokens), strconv.Itoa(m.MaxOutputTokens))
	chg("tools", maskJoined(before.Capabilities.AllowedTools, ", "), maskJoined(m.Capabilities.AllowedTools, ", "))
	if len(lines) == 0 {
		lines = append(lines, "(no field changes)")
	}
	return lines
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return ""
}
