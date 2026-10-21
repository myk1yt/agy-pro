// Command agy is the universal Go shim and router for Antigravity CLI.
// It intercepts `agy pro <subcommand>` commands to provide profile switching,
// provider management, and multi-model swarms, while transparently passing
// all other invocations to the authentic agy binary with standard I/O and exit codes preserved.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/fanout"
	"agypro/internal/launcher"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/tui"
	"agypro/internal/wirecheck"
)

const Version = "0.1.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pro" {
		handleProCommand(os.Args[2:])
		return
	}

	// Passthrough to the authentic agy binary
	realAgy, err := launcher.FindRealAgy()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agy-pro] Error: %v\n", err)
		os.Exit(1)
	}

	exitCode, err := launcher.ExecutePassthrough(realAgy, os.Args[1:])
	if err != nil {
		if !errors.Is(err, launcher.ErrRecursionDetected) {
			fmt.Fprintf(os.Stderr, "[agy-pro] Execution failed: %v\n", err)
		}
	}
	os.Exit(exitCode)
}

func handleProCommand(args []string) {
	if len(args) == 0 {
		printProHelp()
		return
	}

	subcommand := strings.ToLower(args[0])
	subArgs := args[1:]

	switch subcommand {
	case "setup":
		runSetup(subArgs)
	case "mode":
		runMode(subArgs)
	case "status":
		runStatus()
	case "models":
		os.Exit(runModelsCmd(subArgs))
	case "swarm":
		os.Exit(runSwarmCmd(subArgs, os.Stdin, os.Stdout, os.Stderr))
	case "settings":
		os.Exit(runSettingsCmd(subArgs, os.Stdin, os.Stdout, os.Stderr))
	case "hybrid":
		os.Exit(runHybridCmd(subArgs))
	case "doctor":
		runDoctor(subArgs)
	case "fanout":
		runFanout(subArgs)
	case "config":
		runConfig(subArgs)
	case "version", "--version", "-v":
		runVersion()
	case "help", "--help", "-h":
		printProHelp()
	default:
		// D2: mask the ORIGINAL-CASE argument. The switch matches on the
		// case-folded copy, but masking that copy never fires the
		// uppercase-anchored AKIA/ASIA credential class (a pasted key id,
		// the testfixtures.AKIAExample fixture, used to echo case-folded).
		fmt.Fprintf(os.Stderr, "[agy-pro] Unknown subcommand: %q\n\n", providers.MaskOperand(args[0]))
		printProHelp()
		os.Exit(1)
	}
}

func printProHelp() {
	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro v%s - High-Performance CLI Shim & Orchestrator\n", Version)
	fmt.Println("===========================================================")
	fmt.Println("Usage: agy pro <command> [arguments...]")
	fmt.Println()
	fmt.Println(">>> agy pro setup  opens the full-screen setup TUI <<<")
	fmt.Println("    scope: providers, models, fanout presets, prompts, language")
	fmt.Println("    classic line wizard: agy pro setup --wizard")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  setup           Full-screen setup TUI: providers, models, fanout presets, prompts, language")
	fmt.Println("  mode            Switch profile between full-stack and clean (or restore)")
	fmt.Println("  status          Display current mode, providers, and real agy status")
	fmt.Println("  models          Configured providers & models; --all adds the numbered combined inventory")
	fmt.Println("  swarm           Build and run a multi-model swarm with numbered seat selection")
	fmt.Println("  settings        Show or change settings (TUI, show/set, providers, mode, doctor)")
	fmt.Println("  doctor          Diagnose toolchain, paths, keys (--strict, --fix-keys, --rollback-keys)")
	fmt.Println("  fanout          Run multi-model parallel audit, review, or research swarm")
	fmt.Println("  config          Show or set persistent defaults (default-profile, bridge-tools)")
	fmt.Println("  version         Display agy-pro and underlying agy version information")
	fmt.Println("  help            Show this help text")
	fmt.Println()
	fmt.Println("All other commands (e.g., `agy agents`, `agy -m ...`) pass through to real agy.")
}

// setup seams: the entry picks between the full-screen setup TUI and the
// line-mode wizard by host capability, and every branch is indirected so
// tests drive the dispatch without a console, the live registry, or a real
// process exit (same pattern as the doctor seams below).
var (
	setupTUIRunner    = tui.RunSetup
	setupWizardRunner = providers.InteractiveSetupWizard
	setupConsoleCap   = tui.Detect
	setupStdinConsole = tui.StdinIsConsole
	setupCapRestore   = restoreSetupCap
	setupExit         = os.Exit
)

// restoreSetupCap hands the console output mode back to the state the setup
// gate's capability probe found it in. It is a seam of its own because Cap's
// restore hook is unexported: a test in this package can observe the call
// site, but cannot build a Cap that carries a hook. The hook's own semantics
// (a no-op on a cap that never enabled VT, one console-mode write on repeat
// calls) are pinned in internal/tui/termcap_test.go.
func restoreSetupCap(cap tui.Cap) { cap.Restore() }

// runSetup handles `agy pro setup [arguments...]`. No arguments open the
// setup TUI on a host where BOTH standard handles are consoles and the
// classic line wizard everywhere else; `--wizard` (hidden alias `wizard`)
// forces the wizard on any host; any other argument fails closed with the
// masked operand, the help text, and exit code 2 (today unknown flags were
// silently ignored).
func runSetup(args []string) {
	if len(args) > 0 {
		// D2: the switch key is case-folded, but the masked echo takes the
		// ORIGINAL-CASE operand so the uppercase-anchored credential class
		// still fires on `agy pro setup AKIA...`.
		sub := strings.ToLower(strings.TrimSpace(args[0]))
		if sub == "--wizard" || sub == "wizard" {
			runSetupWizard()
			return
		}
		fmt.Fprintf(os.Stderr, "[agy-pro] Unknown setup argument: %q\n\n", providers.MaskOperand(args[0]))
		printProHelp()
		setupExit(2)
		return
	}

	// Console gate: tui.Detect() runs the capability ladder once per
	// process. SuppressBanner is the non-console signal (stdout is not a
	// console: pipe, non-Windows TTY, or mintty-class host), and on those
	// hosts the byte-stable degrade is the existing line-mode wizard, so
	// piped and scripted callers keep today's transcript exactly.
	//
	// The stdin probe is the second half of the gate. The TUI key source
	// reads the Windows console input buffer, so a console stdout paired with
	// a PIPED stdin (`producer | agy pro setup`) would open a full-screen
	// surface that can never accept typed text; with the provider add/edit
	// form that is a real input-loss shape, not a cosmetic one. Both handles
	// must therefore be consoles before the TUI opens, and every other shape
	// takes the same byte-stable wizard with no new output and no new exit
	// code. The stdout test stays first (short-circuit), so a scripted caller
	// whose stdin is closed never has that handle probed at all.
	if cap := setupConsoleCap(); cap.SuppressBanner || !setupStdinConsole() {
		// FIX-C: the gate above already ran the capability ladder, and Detect
		// enables VT processing on the console output handle, so that mode now
		// belongs to this process and must go back before the line wizard keeps
		// the process alive. This restores the exact Cap the gate probed, once,
		// and it runs BEFORE the wizard: runSetupWizard exits through setupExit
		// on failure and an os.Exit skips defers, so a deferred restore would
		// leak the mode on exactly the failure path. It emits no bytes and
		// requests no exit code, so both the wizard transcript and the exit code
		// are unchanged. The TUI branch below is deliberately not restored here:
		// tui.RunSetup owns its own defer cap.Restore() (internal/tui/app.go:50).
		setupCapRestore(cap)
		runSetupWizard()
		return
	}

	setupExit(setupTUIRunner(os.Stdin, os.Stdout, os.Stderr))
}

// runSetupWizard runs the classic line-mode setup wizard, byte-identical to
// the pre-TUI behavior on every host (plan v2 section 1).
func runSetupWizard() {
	if err := setupWizardRunner(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "[agy-pro] Setup failed: %v\n", err)
		setupExit(1)
	}
}

func runMode(args []string) {
	if len(args) == 0 {
		if err := mode.Switch("", os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Mode switch failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "restore":
		if err := mode.Restore(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Restore failed: %v\n", err)
			os.Exit(1)
		}
	case "list", "backups":
		backups, err := mode.ListBackups()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to list backups: %v\n", err)
			os.Exit(1)
		}
		if len(backups) == 0 {
			fmt.Println("No backups available.")
			return
		}
		fmt.Println("Available backups:")
		for i, b := range backups {
			fmt.Printf("  %d. %s\n", i+1, b)
		}
	case "full", "clean", "default", "1", "2":
		if err := mode.Switch(sub, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Mode switch failed: %v\n", err)
			os.Exit(1)
		}
	case "status":
		if err := mode.Status(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Status failed: %v\n", err)
			os.Exit(1)
		}
	default:
		// D2: pass the ORIGINAL-CASE argument; the case-folded copy is only
		// the switch key and must not be the masked echo source.
		fmt.Fprint(os.Stderr, invalidModeArgMessage(args[0]))
		os.Exit(1)
	}
}

// invalidModeArgMessage renders the `agy pro mode` usage error. The hint lists
// every accepted mode argument, including the newer `status` and `list`. The
// operand is masked in its original case (C2/D2): masking a lowercased copy
// never fires the uppercase-anchored AKIA/ASIA credential class.
func invalidModeArgMessage(operand string) string {
	return fmt.Sprintf("[agy-pro] Invalid mode argument: %q (use 'full', 'clean', 'restore', 'status', or 'list')\n", providers.MaskOperand(operand))
}

// configUsage is the shared usage line for `agy pro config` (help text and
// error paths must not drift apart).
const configUsage = "Usage: agy pro config <show | default-profile <prompt|native|gateway> | bridge-tools <read,search,write,exec|none>>"

// bridgeToolLabels maps each capability token to the tools the bridge loop
// exposes for it, mirroring agenttools' token set so the CLI can print a
// readable summary without importing tool internals.
var bridgeToolLabels = []struct{ token, tools string }{
	{"read", "read_file, list_dir"},
	{"search", "grep, glob"},
	{"write", "write_file, replace_in_file"},
	{"exec", "run_command"},
}

// parseBridgeToolsSpec validates a `config bridge-tools` argument: the closed
// token set (read,search,write,exec) canonicalized by agenttools, or the
// literal "none" that disables the loop. Fail-closed: anything else errors.
func parseBridgeToolsSpec(spec string) (string, error) {
	raw := strings.TrimSpace(spec)
	if strings.ToLower(raw) == mode.BridgeToolsNone {
		return mode.BridgeToolsNone, nil
	}
	if raw == "" {
		return "", fmt.Errorf("empty bridge-tools value (want read,search,write,exec or none)")
	}
	// I1: the parser echoes the token; pass the ORIGINAL-case operand so the
	// error quotes it verbatim and the seeded redactor can match (pre-folding
	// made the original-case seed inert against the case-folded echo).
	toks, err := agenttools.ParseToolTokens(raw)
	if err != nil {
		return "", errors.New(maskOperandSentence(err.Error(), providers.FlagSeedOperands(raw)...))
	}
	return toks, nil
}

// describeBridgeTools renders the tools behind an allow-list for CLI output.
func describeBridgeTools(tools string) string {
	if tools == mode.BridgeToolsNone {
		return "none (loop disabled)"
	}
	enabled := make(map[string]bool)
	for _, tok := range strings.Split(tools, ",") {
		enabled[strings.TrimSpace(tok)] = true
	}
	var parts []string
	for _, entry := range bridgeToolLabels {
		if enabled[entry.token] {
			parts = append(parts, entry.tools)
		}
	}
	if len(parts) == 0 {
		return "none (loop disabled)"
	}
	return strings.Join(parts, ", ")
}

// runConfig handles `agy pro config <show|default-profile ...|bridge-tools ...>`,
// persistent settings that survive mode switches (stored in ~/.agy-pro/state.json).
func runConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, configUsage)
		os.Exit(1)
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "show":
		st, err := mode.LoadState()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to load state: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("===========================================================")
		fmt.Printf("  agy-pro Config (v%s)\n", Version)
		fmt.Println("===========================================================")
		fmt.Printf("Default Launch Profile : %s\n", st.EffectiveDefaultProfile())
		fmt.Printf("Profile Mode           : %s\n", strings.ToUpper(st.CurrentMode))
		fmt.Printf("Bridge Tools           : %s\n", st.EffectiveBridgeTools())
		fmt.Println()
		fmt.Println("Profiles: prompt = fast launcher on plain `agy` (default)")
		fmt.Println("          native = straight to native Gemini")
		fmt.Println("          gateway = local LLM bridge with external models")
		fmt.Println("Bridge Tools: capability tokens the gateway tool loop exposes")
		fmt.Println("          to external models (read,search,write,exec or none).")
		fmt.Println("          AGY_PRO_BRIDGE_TOOLS overrides this when set.")

	case "default-profile":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: agy pro config default-profile <prompt|native|gateway>")
			os.Exit(1)
		}
		profile := strings.ToLower(strings.TrimSpace(args[1]))
		if !mode.ValidProfile(profile) {
			fmt.Fprintf(os.Stderr, "[agy-pro] Invalid default profile: %q (use 'prompt', 'native', or 'gateway')\n", providers.MaskOperand(args[1]))
			os.Exit(1)
		}
		st, err := mode.LoadState()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to load state: %v\n", err)
			os.Exit(1)
		}
		st.DefaultProfile = mode.NormalizeProfile(profile)
		if err := mode.SaveState(st); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to save state: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[SUCCESS] Default launch profile set to: %s\n", st.DefaultProfile)
		switch st.DefaultProfile {
		case mode.ProfilePrompt:
			fmt.Println("          Plain `agy` shows the 2s fast launcher (Enter = native).")
		case mode.ProfileNative:
			fmt.Println("          Plain `agy` runs native Gemini immediately.")
		case mode.ProfileGateway:
			fmt.Println("          Plain `agy` starts the local bridge with your external models.")
		}

	case "bridge-tools":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: agy pro config bridge-tools <read,search,write,exec|none>")
			os.Exit(1)
		}
		tools, err := parseBridgeToolsSpec(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Invalid bridge tools: %v\n", err)
			os.Exit(1)
		}
		st, err := mode.LoadState()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to load state: %v\n", err)
			os.Exit(1)
		}
		st.BridgeTools = tools
		if err := mode.SaveState(st); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to save state: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[SUCCESS] Bridge tools set to: %s\n", tools)
		fmt.Printf("          Gateway exposes: %s\n", describeBridgeTools(tools))
		if tools == mode.BridgeToolsNone {
			fmt.Println("          Gateway mode runs plain passthrough: no local tool loop.")
		} else {
			fmt.Println("          write/exec run real workspace changes; enable them only when needed.")
			fmt.Println("          AGY_PRO_BRIDGE_TOOLS overrides this at run time.")
		}

	default:
		// D2: mask the ORIGINAL-CASE argument (the lowercased copy is only the
		// switch key; masking it never fires the AKIA/ASIA class).
		fmt.Fprintf(os.Stderr, "[agy-pro] Unknown config subcommand: %q\n\n", providers.MaskOperand(args[0]))
		fmt.Fprintln(os.Stderr, configUsage)
		os.Exit(1)
	}
}

func runStatus() {
	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro Status (v%s)\n", Version)
	fmt.Println("===========================================================")

	// 1. Mode & State
	st, err := mode.LoadState()
	if err != nil {
		fmt.Printf("Profile Mode : Error loading state (%v)\n", err)
	} else {
		fmt.Printf("Profile Mode : %s\n", strings.ToUpper(st.CurrentMode))
		fmt.Printf("Default Launch Profile : %s\n", st.EffectiveDefaultProfile())
		fmt.Printf("Last Updated : %s\n", st.UpdatedAt.Format("2006-01-02 15:04:05 MST"))
		if st.LastBackup != "" {
			fmt.Printf("Last Backup  : %s\n", st.LastBackup)
		}
	}

	// 2. Real Agy binary
	realAgy, err := launcher.FindRealAgy()
	if err != nil {
		fmt.Printf("Real agy.exe : NOT FOUND (%v)\n", err)
	} else {
		fmt.Printf("Real agy.exe : %s\n", realAgy)
	}

	// 3. Providers registry
	reg, err := providers.Load()
	if err != nil {
		fmt.Printf("Providers    : %v\n", err)
	} else {
		fmt.Printf("Providers    : %d configured\n\n", len(reg.Providers))
		printConfiguredProvidersAndModels(reg)
	}
}

func runModels() {
	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro Configured Models (v%s)\n", Version)
	fmt.Println("===========================================================")

	reg, err := providers.Load()
	if err != nil {
		fmt.Printf("Providers registry error: %v\n", err)
		fmt.Println("Run `agy pro setup` to configure providers and models.")
		return
	}

	if len(reg.Providers) == 0 {
		fmt.Println("No providers configured.")
		fmt.Println("Run `agy pro setup` to configure providers and models.")
		return
	}

	printConfiguredProvidersAndModels(reg)
}

func printConfiguredProvidersAndModels(reg *providers.Registry) {
	if reg == nil || len(reg.Providers) == 0 {
		fmt.Println("Configured Providers & Models: None")
		return
	}

	fmt.Println("Configured Providers & Models:")

	maxIDLen := 24
	maxFamilyLen := 8
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if n := len(providers.DisplayName(m.ID, p.Name)); n > maxIDLen {
				maxIDLen = n
			}
			fam := m.FamilyName()
			if len(fam) > maxFamilyLen {
				maxFamilyLen = len(fam)
			}
		}
	}

	for i, p := range reg.Providers {
		keyStatus := "Configured"
		if p.APIKeyEnv == "" {
			keyStatus = "Not Required"
		} else if p.KeyValue() == "" {
			// E8: mask INSIDE the composite — the whole-string classification
			// on "Missing (<env>)" always fails, so a key-shaped env name from
			// the user-editable registry would echo raw.
			keyStatus = fmt.Sprintf("Missing (%s)", providers.MaskOperand(p.APIKeyEnv))
		}

		// D3: registry-driven display slots echo user-editable JSON fields; a
		// misdirected paste can land a key-shaped value in a provider name,
		// baseURL, env name, or model id. The BaseURL slot routes through
		// MaskBaseURL (the layered URL mask) so credential material in the
		// userinfo slot cannot ride raw under MaskOperand's location exemption.
		//
		// FIX-8 convergence: the provider NAME is a registry slot (providers.json
		// borne), so it routes through providers.MaskSlot, the single
		// registry-slot display authority. MaskOperand is its first stage, so
		// every benign name renders byte-identically, while the underscore-head
		// credential class ("sk_live_...", "tok_...", "AKIA_..."), which the
		// operand masker cannot classify, now masks here too. The env NAME in
		// the key-status composite stays on MaskOperand: it is an environment
		// variable NAME, not a registry slot.
		fmt.Printf("[%d] %s (%s) [API Key: %s]\n", i+1,
			providers.MaskSlot(p.Name), providers.MaskBaseURL(p.BaseURL), providers.MaskOperand(keyStatus))

		if len(p.Models) == 0 {
			fmt.Println("    (No models configured)")
			continue
		}

		for _, m := range p.Models {
			ctxDisplay := formatTokenCount(m.ContextLength)
			maxOutDisplay := formatTokenCount(m.MaxOutputTokens)

			// K1: the capability summary embeds the registry-controlled
			// reasoningEffort (Summary composes thinking(<effort>) and masks
			// the effort there); this banner slot masks the composed summary
			// again (defense in depth), matching the models --json sink.
			//
			// Convergence: the model id column and the composed summary are
			// registry slots holding providers.json-borne values (a model id is
			// free text at load), so both route through providers.MaskSlot, the
			// single registry-slot display authority, instead of plain
			// MaskOperand: a location-shaped or look-alike-decorated id
			// ("./<key>", U+2025 + "/" + a key core) masked here used to ride
			// raw under the operand masker's location exemption. The family
			// column routes through MaskFamilyName, which delegates to the same
			// authority.
			capSummary := providers.MaskSlot(m.CapabilitySummary())
			fmt.Printf("    - %-*s  [Family: %-*s | Context: %s | MaxOutput: %s | Capabilities: %s]\n",
				maxIDLen, providers.MaskSlot(providers.DisplayName(m.ID, p.Name)),
				maxFamilyLen, providers.MaskFamilyName(m),
				ctxDisplay, maxOutDisplay, capSummary)
		}
	}
}

func formatTokenCount(tokens int) string {
	if tokens <= 0 {
		return "max"
	}
	if tokens >= 1048576 {
		return fmt.Sprintf("%dM", tokens/1048576)
	}
	if tokens >= 1000 {
		if tokens%1000 == 0 {
			return fmt.Sprintf("%dk", tokens/1000)
		}
		if tokens%1024 == 0 {
			return fmt.Sprintf("%dk", tokens/1024)
		}
		return fmt.Sprintf("%dk", tokens/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

// Provider-key operations are indirected so doctor tests can drive audit and
// repair without touching the live registry, the network, or the Windows
// environment (the real RepairWindowsKeyNames refuses to run under `go test`).
// findRealAgy / selfExecutable / doctorExit follow the same pattern so the
// --strict gate can be exercised without spawning processes or terminating
// the test binary.
var (
	auditProviderKeys       = providers.AuditProviderKeys
	resolveRepairTargets    = providers.ResolveRepairTargets
	repairedWindowsKeyNames = providers.RepairWindowsKeyNames
	rollbackKeyNames        = providers.RollbackWindowsKeyNames
	findRealAgy             = launcher.FindRealAgy
	selfExecutable          = os.Executable
	doctorExit              = os.Exit
)

// doctorAction selects what `agy pro doctor` does once its flags are parsed.
type doctorAction int

const (
	doctorDiagnose doctorAction = iota
	doctorFixKeys
	doctorRollbackKeys
)

// doctorArgs is the parsed `agy pro doctor` command line.
type doctorArgs struct {
	action doctorAction
	// target is the --fix-keys target list ("" or "all" = every provider) or
	// the --rollback-keys backup directory (or backup file).
	target string
	// strict requests exit code 1 on structural failures (real agy missing,
	// shim not running from the installed PATH location). See doctorUsage.
	strict bool
}

const doctorUsage = "Usage: agy pro doctor [--strict] [--fix-keys[=<targets>] | --rollback-keys <backup-dir>]"

// parseDoctorArgs reads the doctor flags. --fix-keys accepts an optional
// provider/env target list ("all", "nw", "nw,charm"); --rollback-keys requires
// the backup directory printed by a previous --fix-keys run; --strict is
// orthogonal and combines with either action.
func parseDoctorArgs(args []string) (doctorArgs, error) {
	out := doctorArgs{action: doctorDiagnose}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--strict":
			out.strict = true
		case a == "--fix-keys":
			if out.action != doctorDiagnose {
				return out, errors.New("--fix-keys and --rollback-keys cannot be combined")
			}
			out.action = doctorFixKeys
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				out.target = args[i]
			}
		case strings.HasPrefix(a, "--fix-keys="):
			if out.action != doctorDiagnose {
				return out, errors.New("--fix-keys and --rollback-keys cannot be combined")
			}
			out.action = doctorFixKeys
			out.target = strings.TrimPrefix(a, "--fix-keys=")
		case a == "--rollback-keys":
			if out.action != doctorDiagnose {
				return out, errors.New("--fix-keys and --rollback-keys cannot be combined")
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return out, errors.New("--rollback-keys requires a backup directory")
			}
			i++
			out.action = doctorRollbackKeys
			out.target = args[i]
		case strings.HasPrefix(a, "--rollback-keys="):
			if out.action != doctorDiagnose {
				return out, errors.New("--fix-keys and --rollback-keys cannot be combined")
			}
			out.target = strings.TrimPrefix(a, "--rollback-keys=")
			if out.target == "" {
				return out, errors.New("--rollback-keys requires a backup directory")
			}
			out.action = doctorRollbackKeys
		default:
			// D3: the unknown option is an operand echo slot; mask the
			// original-case argument before it lands in the error text.
			return out, fmt.Errorf("unknown doctor option %q", providers.MaskOperand(a))
		}
	}
	return out, nil
}

func runDoctor(args []string) {
	da, err := parseDoctorArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agy-pro] %v\n%s\n", err, doctorUsage)
		os.Exit(1)
	}
	switch da.action {
	case doctorFixKeys:
		runDoctorFixKeys(da.target)
		return
	case doctorRollbackKeys:
		runDoctorRollback(da.target)
		return
	}

	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro Diagnostic Doctor (v%s)\n", Version)
	fmt.Println("===========================================================")

	allGood := true
	// structuralFail tracks only installation-structure verdicts: the real agy
	// binary and the shim's installed location. It is the sole input to the
	// --strict exit gate. Provider-key probe verdicts are deliberately kept
	// out of it (see the N-3 note at the key-audit check).
	structuralFail := false

	// Check 1: Real agy
	realAgy, err := findRealAgy()
	if err != nil {
		fmt.Println("[FAIL] Real agy binary not detected.")
		fmt.Printf("       Fix: Verify %%LOCALAPPDATA%%\\agy\\bin\\agy.exe exists or set %s\n", paths.EnvAgyProRealAgy)
		fmt.Println("       The authentic Antigravity CLI must be installed first (official site: https://antigravity.google);")
		fmt.Printf("       after installing it, re-run this doctor or set %s to its absolute path.\n", paths.EnvAgyProRealAgy)
		structuralFail = true
		allGood = false
	} else {
		fmt.Printf("[OK]   Real agy located: %s\n", realAgy)
		cmd := exec.Command(realAgy, "--version")
		// Default-deny child env (FIX-12): the third-party binary must never
		// inherit the full shim environment (provider key values included).
		cmd.Env = wirecheck.FilterChildEnv(nil)
		if out, err := cmd.Output(); err == nil {
			fmt.Printf("       Version: %s\n", strings.TrimSpace(string(out)))
		}
	}

	// Check 2: agy-pro shim binary and PATH precedence. The shim is "installed"
	// only when it runs from ~/.agy-pro/bin (the directory the installer
	// prepends to the User PATH); running from a build tree means the PATH
	// registration never took effect.
	selfPath, err := selfExecutable()
	if err != nil {
		fmt.Printf("[WARN] Could not determine shim binary path: %v\n", err)
		if da.strict {
			structuralFail = true
		}
	} else if binDir, binErr := paths.AgyProBin(); binErr != nil {
		fmt.Printf("[WARN] Could not determine installed shim location: %v\n", binErr)
		if da.strict {
			structuralFail = true
		}
	} else if sameDirPath(selfPath, binDir) {
		fmt.Printf("[OK]   Running shim executable: %s\n", selfPath)
	} else {
		fmt.Printf("[WARN] Running shim executable: %s\n", selfPath)
		fmt.Printf("       The installed shim location is %s; this copy is not first on PATH.\n", binDir)
		if da.strict {
			structuralFail = true
		}
	}

	// Check 3: State & directories
	home, _ := paths.AgyProHome()
	cfgDir, _ := paths.GeminiConfigDir()
	fmt.Printf("[OK]   agy-pro Home: %s\n", home)
	fmt.Printf("[OK]   Gemini Config: %s\n", cfgDir)
	if st, stErr := mode.LoadState(); stErr == nil {
		fmt.Printf("[OK]   Default launch profile: %s\n", st.EffectiveDefaultProfile())
	}

	// Check 4: provider API keys (probe-based: presence alone is never OK).
	// N-3 semantics: the probe verdict (valid/invalid/indeterminate/SKIP) is
	// informational and must NOT influence the exit code, even under --strict.
	// Rationale: an invalid or unset provider key is a normal pre-setup state
	// for a fresh install - the installer gates only on structural breakage
	// (missing real agy, shim not on PATH); the key verdicts drive the human
	// [WARN]/[FAIL] display and `--fix-keys`, not automation exit codes.
	reg, err := providers.Load()
	if err != nil {
		fmt.Printf("[WARN] Providers registry not loaded: %v\n", err)
	} else if audits := auditProviderKeys(reg, providers.AuditOptions{}); len(audits) == 0 {
		fmt.Println("[INFO] No provider API keys to audit.")
	} else if !printKeyAudits(audits) {
		allGood = false
	}

	// Check 5: Go toolchain
	goCmd := exec.Command("go", "version")
	// Default-deny child env (round 2b, F1/MEDIUM): a PATH-shadowed `go` must
	// never inherit the full shim environment (provider key values included).
	// `go version` needs only PATH (and SystemRoot on Windows), both allowed.
	goCmd.Env = wirecheck.FilterChildEnv(nil)
	if out, err := goCmd.Output(); err == nil {
		fmt.Printf("[OK]   Go toolchain: %s", string(out))
	} else {
		defGo := "C:\\Program Files\\Go\\bin\\go.exe"
		if _, statErr := os.Stat(defGo); statErr == nil {
			fmt.Printf("[OK]   Go toolchain located at: %s\n", defGo)
		} else {
			fmt.Println("[INFO] Go is not in PATH (runtime does not require Go once compiled).")
		}
	}

	fmt.Println()
	if allGood {
		fmt.Println("[PASS] Doctor diagnostics completed successfully. System is operational.")
	} else {
		fmt.Println("[WARN] Some items need attention. Review messages above.")
	}

	// --strict gate: exit 1 only for structural failures. Provider-key probe
	// verdicts never reach structuralFail (N-3 semantics, see Check 4), so a
	// fresh install without configured keys still passes strict.
	if da.strict && structuralFail {
		fmt.Println("[FAIL] Strict diagnostics: structural checks failed (exit code 1).")
		fmt.Println("       Fix the [FAIL] items above, then re-run `agy pro doctor --strict`.")
		doctorExit(1)
		return
	}
}

// sameDirPath reports whether file lives directly inside dir. Comparison is
// case-insensitive on Windows (case-preserving, case-insensitive filesystem)
// and exact elsewhere.
func sameDirPath(file, dir string) bool {
	file = filepath.Clean(file)
	dir = filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Dir(file), dir)
	}
	return filepath.Dir(file) == dir
}

// printKeyAudits renders one line per provider credential: probe verdict
// (valid/invalid/indeterminate/SKIP), the source the value resolved from
// (env / HKCU / BOM-variant), and a masked hint. Values are never printed in
// clear. It returns false when any key probed invalid.
func printKeyAudits(audits []providers.KeyAudit) bool {
	allValid := true
	for _, a := range audits {
		marker := "[OK]  "
		verdict := "valid"
		switch a.Probe.Status {
		case providers.ProbeValid:
			verdict = "valid"
		case providers.ProbeInvalid:
			marker = "[FAIL]"
			verdict = fmt.Sprintf("invalid (HTTP %d)", a.Probe.HTTPStatus)
			allValid = false
		case providers.ProbeIndeterminate:
			marker = "[WARN]"
			verdict = fmt.Sprintf("indeterminate (HTTP %d)", a.Probe.HTTPStatus)
		default:
			marker = "[SKIP]"
			verdict = "SKIP"
		}
		// D3: the provider/env names come from the user-editable registry. FIX-8:
		// the PROVIDER name is a registry slot and routes through the
		// registry-slot authority (MaskSlot), which is MaskOperand plus the
		// underscore-head credential arm. The ENV name stays on MaskOperand: it
		// is an environment variable NAME, not a registry slot.
		fmt.Printf("%s key %s (%s): %s | source: %s | mask: %s\n",
			marker, providers.MaskSlot(a.Provider), providers.MaskOperand(a.Env), verdict, keySourceLabel(a), keyMaskLabel(a))
		if a.Conflict {
			fmt.Printf("       conflict: clean name and BOM variant both present with different values (try `agy pro doctor --fix-keys`)\n")
		}
		if a.Probe.Detail != "" {
			fmt.Printf("       probe: %s\n", a.Probe.Detail)
		}
	}
	return allValid
}

// keySourceLabel names where the audited value resolved from: process env,
// HKCU/HKLM exact name, or one of the BOM-variant origins.
func keySourceLabel(a providers.KeyAudit) string {
	if !a.Resolved {
		return "none"
	}
	if a.Origin == "" {
		return "unknown"
	}
	return string(a.Origin)
}

// keyMaskLabel is the masked hint for the resolved value (F-1 requirement).
func keyMaskLabel(a providers.KeyAudit) string {
	if a.MaskedHint == "" {
		return "-"
	}
	return a.MaskedHint
}

// runDoctorFixKeys wires `agy pro doctor --fix-keys` to the repair stack:
// resolve the target list, rewrite BOM-polluted key names onto their clean
// names, and print the backup path that `--rollback-keys` needs to undo it.
func runDoctorFixKeys(target string) {
	fmt.Println("===========================================================")
	fmt.Println("  agy-pro Key Repair (--fix-keys)")
	fmt.Println("===========================================================")

	reg, err := providers.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agy-pro] Providers registry not loaded: %v\n", err)
		doctorExit(1)
		return
	}
	names, err := resolveRepairTargets(reg, target)
	if err != nil {
		// G2: the resolve error quotes individual target tokens; seed with the
		// whole target plus its comma/space/semicolon tokens so a misdirected
		// paste in the --fix-keys slot cannot reach stderr raw.
		fmt.Fprintf(os.Stderr, "[agy-pro] Cannot resolve repair targets: %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(target)...))
		doctorExit(1)
		return
	}
	// D3: env names resolve from the user-editable registry; mask for display
	// only (the repair call below still receives the raw names).
	maskedNames := make([]string, len(names))
	for i, n := range names {
		maskedNames[i] = providers.MaskOperand(n)
	}
	fmt.Printf("Targets: %s\n", strings.Join(maskedNames, ", "))

	report, repairErr := repairedWindowsKeyNames(reg, providers.RepairOptions{EnvNames: names})
	if report == nil {
		// F7: the nil-report failure tail echoes the raw repair error (a
		// composite that embeds registry-derived env names); redact it seeded
		// with the raw target names, same rule as the E6 aggregate tail below.
		fmt.Fprintf(os.Stderr, "[agy-pro] Key repair failed: %s\n", maskOperandSentence(fmt.Sprint(repairErr), names...))
		doctorExit(1)
		return
	}
	printRepairReport(report)
	if repairErr != nil {
		// E6: the aggregate error embeds registry-derived env names; redact it
		// seeded with the raw target names so a key-shaped name (misdirected
		// paste in the registry) cannot survive the failure tail.
		fmt.Fprintf(os.Stderr, "[agy-pro] Key repair finished with errors: %s\n", maskOperandSentence(repairErr.Error(), names...))
		doctorExit(1)
		return
	}
}

// printRepairReport renders the --fix-keys entries. Provider, env and variant
// names resolve from the user-editable registry (D3): masked for display, with
// benign names readable. FIX-8: the PROVIDER name is a registry slot, so it
// routes through the registry-slot authority (MaskSlot); the ENV and VARIANT
// names are environment variable NAMEs and stay on MaskOperand.
// e.MaskedValue is already a mask. Notes can embed wrapped store errors that
// repeat the raw env/variant name (E6), so they are redacted seeded with those
// names.
func printRepairReport(report *providers.RepairReport) {
	for _, e := range report.Entries {
		provider, env, variant := providers.MaskSlot(e.Provider), providers.MaskOperand(e.Env), providers.MaskOperand(e.VariantName)
		note := maskOperandSentence(e.Note, e.Env, e.VariantName)
		switch e.Action {
		case providers.RepairActionRewrite:
			fmt.Printf("[FIXED] %s (%s): clean name <- %s (mask %s)\n", provider, env, variant, e.MaskedValue)
		case providers.RepairActionRemoveVariant:
			fmt.Printf("[FIXED] %s (%s): removed BOM variant %s\n", provider, env, variant)
		default:
			fmt.Printf("[SKIP]  %s (%s): %s\n", provider, env, note)
		}
		if e.Action != "" && e.Note != "" {
			fmt.Printf("        note: %s\n", note)
		}
	}
	fmt.Printf("Repaired: %d | Skipped: %d | Failed: %d\n", report.Repaired, report.Skipped, report.Failed)
	if report.BackupPath != "" {
		backupDir := filepath.Dir(report.BackupPath)
		fmt.Printf("Backup written: %s\n", report.BackupPath)
		fmt.Printf("Rollback target (backup dir): %s\n", backupDir)
		fmt.Printf("Undo with: agy pro doctor --rollback-keys %s\n", backupDir)
	}
}

// runDoctorRollback wires `agy pro doctor --rollback-keys <backup-dir>` to
// RollbackWindowsKeyNames. The flag takes the backup directory printed by
// --fix-keys; a direct path to registry-keys.json is accepted too.
func runDoctorRollback(target string) {
	fmt.Println("===========================================================")
	fmt.Println("  agy-pro Key Repair Rollback (--rollback-keys)")
	fmt.Println("===========================================================")

	file := providers.ResolveBackupFile(target)
	// E5/G4/H1: the target is an untrusted CLI operand — a misdirected paste
	// puts a key in the --rollback-keys slot, and the resolved path re-embeds
	// it (also in decorated form, which MaskOperand alone cannot classify).
	// Batch K: the hardened two-pass slot. The whole-string classifier runs
	// first (first-5+**** rendering; single-token, one-space, and
	// location-shaped renderings stay byte-identical), then EVERY
	// whitespace- or comma/semicolon-carrying operand routes through the
	// seeded sentence masker, applied to the already-masked line and seeded
	// from the original target (plus the resolved file). The old
	// equality-only fallback was skipped whenever MaskOperand PARTIALLY
	// masked the operand, so a compound target carrying both a pattern-shaped
	// credential and a bare decorated secret echoed the bare secret raw.
	// Batch L: the gate also carries the comma/semicolon class the old
	// equality fallback used to seed, so a whitespace-free comma-decorated
	// target ("./backups,<secret>") no longer keeps its bare secret raw.
	backupLine := providers.MaskOperand(file)
	if strings.ContainsAny(file, " \t\n\r,;") {
		backupLine = maskOperandSentence(backupLine, append(providers.FlagSeedOperands(target), file)...)
	}
	fmt.Printf("Backup file: %s\n", backupLine)
	if err := rollbackKeyNames(file, providers.RepairOptions{}); err != nil {
		// The forwarded error (os.ReadFile's *fs.PathError) re-embeds the raw
		// path; redact seeded with the raw target (plus per-token forms) and
		// the resolved file.
		fmt.Fprintf(os.Stderr, "[agy-pro] Rollback failed: %s\n", maskOperandSentence(err.Error(), append(providers.FlagSeedOperands(target), file)...))
		doctorExit(1)
		return
	}
	fmt.Println("[SUCCESS] Registry key names restored from backup.")
}

func runFanout(args []string) {
	code := fanout.Run(args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}

func runVersion() {
	fmt.Printf("agy-pro version %s (Go shim & swarm orchestrator, %s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)

	realAgy, err := launcher.FindRealAgy()
	if err == nil {
		cmd := exec.Command(realAgy, "--version")
		// Default-deny child env (FIX-12): the third-party binary must never
		// inherit the full shim environment (provider key values included).
		cmd.Env = wirecheck.FilterChildEnv(nil)
		if out, err := cmd.Output(); err == nil {
			fmt.Printf("upstream: %s\n", strings.TrimSpace(string(out)))
		}
	}
}
