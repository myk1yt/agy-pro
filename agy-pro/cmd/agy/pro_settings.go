package main

import (
	"fmt"
	"io"
	"os"
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
//	agy pro settings providers ...        list | add-json | add-interactive | test | rename | delete
//	agy pro settings mode ...             switch | restore | list | status
//	agy pro settings doctor ...           diagnose | fix-keys | rollback-keys
//	agy pro settings update-info | uninstall-info | version-info
//
// Phase 1: unused-but-compilable; U-wiring flips main.go's dispatch line to
// os.Exit(runSettingsCmd(subArgs, os.Stdin, os.Stdout, os.Stderr)).

const proSettingsUsage = `Usage: agy pro settings [--panel <id>] | show [--json] | set <key> <value> |
       providers <list|add-json <file|->|add-interactive|test <name>|rename <old> <new>|delete <name>> |
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
		fmt.Fprintf(out, "[SUCCESS] provider %s saved (apiKeyEnv %s — env-var NAME only)\n", providers.MaskOperand(spec.Name), providers.MaskOperand(spec.APIKeyEnv))
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
			fmt.Fprintf(errw, "[agy-pro] %s\n", maskOperandSentence(err.Error(), args[1], providers.ResolveBackupFile(args[1])))
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

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return ""
}
