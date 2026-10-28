package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"agypro/internal/agenttools"
	"agypro/internal/cellwidth"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// executeEngine is the engine seam (A3): Run() resolves the whole flag surface
// into a RunConfig and hands it here. Tests replace it to assert flag binding
// at Run() level without spawning seats (same idiom as swarm.RunFanout).
var executeEngine = Execute

// setupWizardRunner is the --setup door's wizard seam (P5-1 R1). The wizard is
// a persisted-state writer (providers.json W12, the Windows user environment
// W31) and its composed write gate asks y/N, so the door must hand it the same
// consent context the other three doors pass. A var, like executeEngine, so
// this door's flag binding and consent plumbing are pinned without a dialog.
var setupWizardRunner = providers.InteractiveSetupWizardWithGate

// setupStdinIsConsole is the console half of that consent context: the door's
// answer to "can this reader answer a y/N question".
//
// It is the SAME reader check every other wizard door uses. cmd/agy's
// stdinIsTTY (pro_swarm.go:111) is the authority `agy pro setup`
// (main.go setupWizardOptions) and `agy pro settings providers add-interactive`
// (pro_settings.go:249) pass as WizardOptions.Console, and it is the only thing
// wizard.go consults that field for (wizard.go:978: Console false prints the
// refusal sentence naming --yes instead of a prompt). The predicate is
// package-private to cmd/agy and internal/fanout must not import it (that would
// invert the dependency), so the body is restated byte for byte: a reader that
// is an *os.File whose Stat reports a character device. Every scripted or piped
// caller (io.Reader, pipe, strings.Reader) is therefore NOT a console - the
// case this door used to get wrong by hard-coding Console: true.
var setupStdinIsConsole = func(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// parseEndedAtTerminator distinguishes a parser-consumed "--" from the same
// token consumed as a flag value. It runs only after a successful Parse.
func parseEndedAtTerminator(flags *flag.FlagSet, args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return true
		}
		if len(arg) < 2 || arg[0] != '-' || arg == "-" {
			return false
		}
		name := strings.TrimPrefix(arg[1:], "-")
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, hasValue = name[:eq], true
		}
		f := flags.Lookup(name)
		if f == nil {
			return false
		}
		boolean := false
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
			boolean = bf.IsBoolFlag()
		}
		if !hasValue && !boolean {
			i++
		}
	}
	return false
}

// Run executes the fanout CLI command with standard exit codes.
func Run(args []string, in io.Reader, out, errw io.Writer) int {
	flags := flag.NewFlagSet("fanout", flag.ContinueOnError)
	// D8: stdlib flag output routes through the echo-line redactor so parse
	// errors cannot echo a planted operand.
	echo := &maskedFlagWriter{w: errw}
	flags.SetOutput(echo)

	var (
		taskFlag       = flags.String("task", "review", "Task type: review|security|plan-audit|research|ideas|custom")
		promptFlag     = flags.String("prompt", "", "Inline user prompt text")
		promptFileFlag = flags.String("prompt-file", "", "Path to prompt file, or '-' for stdin")
		unitsFlag      = flags.String("units", "", "Path to a partition plan file (JSON array of units): PARTITION mode runs one seat per unit, each bounded to its unit's scope (mutually exclusive with --prompt, --prompt-file and --seats)")
		nFlag          = flags.Int("n", 3, "Number of models to fan out across")
		seatsFlag      = flags.String("seats", "", "Comma-separated seat specs mixing native and provider seats: 'native:<model-id>' for a real agy (Gemini) subprocess seat, 'provider/model' or 'model' for an external provider seat (e.g. 'native:gemini-3.8-flash-high,bigmodel/glm-4.6')")
		maxNativeFlag  = flags.Int("max-native", 0, "Maximum concurrent native (real agy) seats; 0 uses state.json native_seat_concurrency or the default 1 (excess native seats queue)")
		rootFlag       = flags.String("root", ".", "Workspace root directory for tool execution")
		toolsFlag      = flags.String("tools", "read,search", "Allowed tools: read,search,write,exec (closed set; unknown tokens are rejected)")
		maxTurnsFlag   = flags.Int("max-turns", defaultFanoutMaxTurns, "Maximum turns for agentic tool loop")
		timeoutFlag    = flags.Int("timeout", DefaultTimeoutSec, "Swarm execution timeout in seconds (with --units and no explicit --timeout, the default auto-scales: 300 + 120 per unit, capped at 3600)")
		outFlag        = flags.String("out", "", "Output directory for generated reports (default ./reports/fanout/<ts>)")
		jsonFlag       = flags.Bool("json", false, "Output summary JSON to stdout")
		setupFlag      = flags.Bool("setup", false, "Launch the interactive provider setup wizard. It writes providers.json and the Windows user environment, so it prints the exact diff and asks y/N first; on a reader that cannot answer it refuses, naming --yes")
		yesFlag        = flags.Bool("yes", false, "Consent to the --setup wizard's composed write without asking (the scripted-caller stand-in for the y/N answer, after the diff prints). Valid only with --setup")
		presetFlag     = flags.String("preset", "", "Name of a saved fanout preset from ~/.agy-pro/fanout-presets.json (fields not set by explicit flags resolve from the preset, then the providers.json defaults block, then the engine default)")
	)
	// B4-2a: repeatable --allow-host entries accumulate verbatim here;
	// buildRunConfig normalizes them into RunConfig.AllowHosts.
	var allowHosts []string
	flags.Func("allow-host", "Hostname allowed for the read_url_content tool (repeatable; port ignored, matched lowercase). Provider seats get read_url_content only when at least one host is listed", func(v string) error {
		allowHosts = append(allowHosts, v)
		return nil
	})
	// B4-2b: the search_web source; the key VALUE lives only in the named
	// environment variable and is read at call time, never by the CLI.
	var searchEndpoint, searchKeyEnv string
	searchNative := SearchNativeAuto
	flags.StringVar(&searchEndpoint, "search-endpoint", "", "Endpoint URL template for search_web with one {query} placeholder (https; loopback http). Empty uses the native agy fallback unless --search-native=off")
	flags.StringVar(&searchKeyEnv, "search-key-env", "", "NAME of the environment variable holding the search endpoint's API key (read at call time, never logged). A key without an endpoint still uses native fallback")
	flags.StringVar(&searchNative, "search-native", SearchNativeAuto, "Empty-endpoint search fallback: auto runs one native agy turn per search (subscription quota); off keeps the legacy no-search behavior")

	flags.Usage = func() {
		fmt.Fprintln(errw, "Usage: agy pro fanout [options]")
		fmt.Fprintln(errw, "\nOptions:")
		// PrintDefaults renders static flag metadata only (names, types,
		// defaults and our own usage strings) - never a user operand - so it
		// writes straight to errw instead of the echo-line writer. That keeps
		// our own examples readable: 'deepseek/deepseek-chat' carries the
		// 17-rune run 'deepseek/deepseek' that the E1 unquoted scan would
		// otherwise mask inside help text ('./reports/fanout/<ts>' is under
		// the 16-rune floor at 14 runes and never qualified). Swarm and models
		// route their usage the same way.
		flags.SetOutput(errw)
		flags.PrintDefaults()
		flags.SetOutput(echo)
	}

	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if !parseEndedAtTerminator(flags, args) {
		for _, arg := range flags.Args() {
			if arg == "--help" || arg == "-h" {
				flags.Usage()
				return 0
			}
		}
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(errw, "Error: fanout takes no arguments: %q\n\n", providers.MaskSlot(flags.Arg(0)))
		flags.Usage()
		return 2
	}

	// B7/P8: detect which flags were EXPLICITLY set. The flag defaults are
	// indistinguishable from an explicit `--flag <default>` by value alone
	// (e.g. `--timeout 300` vs the flat 300 default); flags.Visit visits only
	// flags present on the command line. timeoutSet feeds the B7 partition
	// scaling; the full set feeds the P8 resolution ladder (an unset flag
	// falls through to preset > providers.json defaults > engine default).
	timeoutSet := false
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) {
		set[f.Name] = true
		if f.Name == "timeout" {
			timeoutSet = true
		}
	})

	// 1. Setup Wizard flag check
	//
	// P5-1 R1: this door used to call the 2-arg providers.InteractiveSetupWizard,
	// which hard-codes Console: true (wizard.go:42-51), and offered no --yes. A
	// piped run therefore printed a [y/N] prompt nobody could answer, and the
	// refusal hint it printed (`re-run with --yes`) exited 2 here as an unknown
	// flag: safe (nothing written) but unactionable. Both halves now match the
	// other three doors: Console is DERIVED from the reader this door was handed
	// (setupStdinIsConsole, the same check cmd/agy's stdinIsTTY makes) and --yes
	// is the scripted stand-in for the answer.
	if *setupFlag {
		err := setupWizardRunner(in, out, providers.WizardOptions{Yes: *yesFlag, Console: setupStdinIsConsole(in)})
		if err != nil {
			if errors.Is(err, providers.ErrWizardWriteRefused) {
				// The gate already printed the refusal sentence naming --yes;
				// this door only carries the code (nothing was written), exactly
				// as the setup and add-interactive doors do.
				return 1
			}
			fmt.Fprintf(errw, "Setup failed: %v\n", err)
			return 1
		}
		return 0
	}
	// A lone --yes would be a silent no-op standing in for a consent this run
	// never asks for, so it fails closed instead (before this change the flag
	// was unknown, i.e. exit 2 either way - no caller loses anything).
	if *yesFlag {
		fmt.Fprintf(errw, "Error: --yes is only meaningful with --setup (it consents to that wizard's providers.json write); nothing was run\n")
		return 2
	}

	// 2. Resolve Prompt
	promptText := *promptFlag
	if *unitsFlag != "" {
		// PARTITION mode (B2): the per-unit bodies come from the plan file, so
		// the single-prompt sources are refused instead of read. Reading is not
		// merely redundant here: `--prompt-file -` would block on stdin forever
		// in a run whose real input is the plan file.
		if *promptFlag != "" || *promptFileFlag != "" {
			fmt.Fprintf(errw, "Error: --units cannot be combined with --prompt or --prompt-file (each unit carries its own body in the plan file)\n")
			return 2
		}
	}
	if *unitsFlag == "" && *promptFileFlag != "" {
		if *promptFileFlag == "-" {
			data, err := io.ReadAll(in)
			if err != nil {
				fmt.Fprintf(errw, "Error reading prompt from stdin: %v\n", err)
				return 1
			}
			promptText = string(data)
		} else {
			data, err := os.ReadFile(*promptFileFlag)
			if err != nil {
				// C2/D3: os.ReadFile embeds the operand path in its error
				// text; mask both the slot and the composite sentence.
				// S2 residual (a): the whole-string classifier is narrow by
				// design (a multi-word path must stay readable so spawn errors
				// name the file), so a >=2-space or tab-decorated path with an
				// embedded bare secret classified as benign and echoed raw at
				// the slot while the sentence beside it was masked. The slot
				// therefore keeps the classifier first and only then routes
				// through the seeded sentence masker with the per-token seeds,
				// matching the cmd/agy pro_swarm.go precedent.
				//
				// Batch K: the equality-only fallback skipped that seeded pass
				// whenever MaskOperand PARTIALLY masked the operand, so a
				// compound path carrying both a pattern-shaped credential (the
				// generic pass masks it) and a bare decorated secret (no pattern
				// shape; seed-shaped only) echoed the bare secret raw. The slot
				// now runs the seeded pass on every separator-carrying
				// operand, with the seeds taken from the ORIGINAL operand and
				// applied to the already-masked slot: single-token, one-space,
				// location-shaped and benign multi-word renderings stay
				// byte-identical, while a multi-token operand's bare secret
				// token is masked too.
				//
				// Batch L: the gate covers comma/semicolon as well as
				// whitespace. FlagSeedOperands splits on those separators, and
				// the pre-K equality fallback ran the seeded pass for a
				// whitespace-free comma-decorated operand whenever MaskOperand
				// left it unchanged (it did: the classifier sees no secret
				// shape in either class), so the whitespace-only gate was a
				// masked->raw regression vs 21827f6 for
				// "./backups,wJalr.../bPxRfiCYEXAMPLEKEY" (the leading ./
				// marker exempts the display class) and
				// "note,abcdefghijklmnopqrs" (the all-lowercase squash fails
				// the uppercase gate). A benign comma list ("read,search")
				// seeds nothing and stays byte-identical.
				pathSlot := providers.MaskOperand(*promptFileFlag)
				if strings.ContainsAny(*promptFileFlag, " \t\n\r,;") {
					pathSlot = maskOperandSentence(pathSlot, providers.FlagSeedOperands(*promptFileFlag)...)
				}
				// Batch M: the composite tail beside the slot takes the same
				// per-token seeds as the slot (and as the cmd/agy main.go
				// rollback twin). The whole operand alone is not enough: a
				// location-shaped whole operand ("./backups,<secret>") never
				// seeds (SeedEligible's location exemption), so the sentence
				// fell back to MaskOperand, whose generic patterns do not cover
				// a bare secret token, and the tail re-echoed it raw while the
				// slot beside it was masked. FlagSeedOperands keeps the whole
				// value AND adds each comma/space/semicolon-separated token, so
				// the token seed closes the sentence; separator-free operands
				// seed exactly what they seeded before (byte-identical).
				fmt.Fprintf(errw, "Error reading prompt file %s: %v\n",
					pathSlot, maskOperandSentence(err.Error(), providers.FlagSeedOperands(*promptFileFlag)...))
				return 1
			}
			promptText = string(data)
		}
	}

	// 3. The remaining flag surface is validated and turned into a RunConfig by
	// buildRunConfig: the ONE choke point where raw flag values become an
	// engine-ready config (A1/A3).
	cfg, err := buildRunConfig(fanoutFlags{
		task:           *taskFlag,
		n:              *nFlag,
		seats:          *seatsFlag,
		units:          *unitsFlag,
		maxNative:      *maxNativeFlag,
		root:           *rootFlag,
		tools:          *toolsFlag,
		maxTurns:       *maxTurnsFlag,
		timeout:        *timeoutFlag,
		timeoutSet:     timeoutSet,
		out:            *outFlag,
		allowHosts:     allowHosts,
		searchEndpoint: strings.TrimSpace(searchEndpoint),
		searchKeyEnv:   strings.TrimSpace(searchKeyEnv),
		searchNative:   strings.TrimSpace(searchNative),
		jsonVal:        *jsonFlag,
		preset:         strings.TrimSpace(*presetFlag),
		set:            set,
		errw:           errw,
	}, promptText)
	if err != nil {
		// C2/D3: usage errors quote raw tokens from --seats/--tools (seat-spec
		// and tool-token validators). Seed the redactor with the raw flag
		// values so a planted secret is masked wherever the sentence echoes it.
		// --units joins the seeds: a plan-file path is an operand like any
		// other and its read/parse errors quote it verbatim.
		// F5: --preset and EnvFanoutPreset join the seeds too; the ladder's
		// not-found error and the dangling-default warning quote the selected
		// preset name, and a misdirected key paste in that operand must not
		// reach stderr raw.
		fmt.Fprintf(errw, "Error: %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(*seatsFlag, *toolsFlag, *unitsFlag, *presetFlag, os.Getenv(EnvFanoutPreset))...))
		return 2
	}

	// FL-3: disclose the effective search route on every actual run, after the
	// full ladder and before any seat starts. Library/ladder-only calls keep
	// their existing quiet behavior.
	//
	// P32: the native line names the child's ACTUAL scope, so the "is the
	// restricted agent deployed?" question is answered here, once, through the
	// same resolver/spawn seams the engine's native runner uses, and the answer
	// rides cfg.SearchAgentPresence into the run (the engine reuses it and never
	// probes a second time). Only the native route probes: an endpoint or
	// --search-native off has no restricted child to name.
	restrictedAgent := ""
	if strings.TrimSpace(cfg.SearchSource.Endpoint) == "" && !strings.EqualFold(strings.TrimSpace(cfg.SearchNative), SearchNativeOff) {
		cfg.SearchAgentPresence = &SearchAgentPresence{
			Probe:          searchAgentProbe(cfg),
			FallbackNotice: func(line string) { fmt.Fprintln(errw, line) },
		}
		restrictedAgent = cfg.SearchAgentPresence.Agent(context.Background(), nil)
	}
	fmt.Fprintln(errw, searchSourceNotice(cfg.SearchSource.Endpoint, cfg.SearchNative, cfg.Sources["search"], restrictedAgent))

	// 4. Live seat progress sink: the engine streams one line per seat
	// start/finish (the same lines land in <out>/progress.log). Terminal runs
	// watch stdout next to the final banner; --json keeps stdout pure (progress
	// rides stderr) so the machine summary stays parseable - the swarm builder
	// consumes that JSON. cfg.JSONOutput is the LADDER-resolved json switch
	// (--json flag > preset json field > false), not the raw flag value.
	if cfg.JSONOutput {
		cfg.ProgressOut = errw
	} else {
		cfg.ProgressOut = out
	}
	// Exclusion warnings ride stderr in BOTH output modes: they describe an
	// honored pick, never the machine summary, so stdout stays parseable.
	cfg.WarnOut = errw

	// 5. Execute Fanout Engine
	ctx := context.Background()
	summary, err := executeEngine(ctx, cfg)
	if err != nil {
		// S-7: provider error snippets may echo credential fragments; the
		// console line uses the same Redactor path as the reports. The raw
		// --seats value is seeded too, so a secret in a spec that
		// passes validation but fails to resolve (e.g. a pasted key used as a
		// provider token) cannot reach stderr through the engine error.
		fmt.Fprintf(errw, "Fanout error: %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(*seatsFlag)...))
		return 1
	}

	// 6. Output formatting
	if cfg.JSONOutput {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return 0
	}

	// Terminal human-readable report
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Multi-Model Fanout Swarm Complete")
	fmt.Fprintln(out, "===========================================================")
	// E4: the completion banner echoes the raw --task/--out flag values; a
	// misdirected paste must not reach stdout, so every echoed slot routes
	// through providers.MaskOperand (credential shapes mask, benign values
	// stay readable).
	// P8: when the task came from the preset rung of the resolution ladder,
	// the banner names the rung so the operator sees where the value came
	// from. The machine summary's sources map is narrower (interestingSources
	// in engine.go): it keeps only rungs that actually supplied a value from
	// a P8-introduced source, i.e. preset and defaults; flag, engine, and
	// state rungs stay invisible there, so an unconfigured run's summary.json
	// keeps its pre-P8 omitempty shape.
	taskLine := providers.MaskOperand(summary.Task)
	if cfg.Sources["task"] == "preset" {
		taskLine += " (preset)"
	}
	fmt.Fprintf(out, "Task:                  %s\n", taskLine)
	fmt.Fprintf(out, "Seats Evaluated:       %d / %d\n", summary.NCompleted, summary.NRequested)

	// D1: PARTITION mode merges per-unit work products and runs NO findings
	// reconciliation, so the cross-verification block below (confirmed/disputed
	// counts, per-seat report lines, "all findings verified") is phantom
	// language here: MergePartition never writes seat-*.md files. The
	// partition banner names the merged deliverable, the unit count and the
	// merge status instead. A failed partition run exits above (the engine
	// error path), so this banner is only ever the success path.
	if summary.Partition != nil {
		p := summary.Partition
		fmt.Fprintf(out, "Degraded Mode:         %v\n", summary.Degraded)
		fmt.Fprintf(out, "Partition Units:       %d\n", len(p.Units))
		fmt.Fprintf(out, "Partition Status:      %s\n", p.Status)
		fmt.Fprintf(out, "Conflicts:             %d\n", len(p.Conflicts))
		fmt.Fprintln(out)

		fmt.Fprintln(out, "Artifacts Generated:")
		fmt.Fprintf(out, "  Merged Deliverable:  %s\n", providers.MaskOperand(filepath.Join(cfg.OutDir, p.Artifact)))
		fmt.Fprintf(out, "  Reconciliation:      %s\n", providers.MaskOperand(filepath.Join(cfg.OutDir, "reconciliation.md")))
		fmt.Fprintf(out, "  Machine Summary:     %s\n", providers.MaskOperand(filepath.Join(cfg.OutDir, "summary.json")))
		fmt.Fprintln(out)
		if len(p.Conflicts) > 0 {
			fmt.Fprintf(out, "⚠️ Notice: %d partition conflict(s) require plan arbitration in reconciliation.md\n", len(p.Conflicts))
		} else {
			fmt.Fprintln(out, "✅ Partition merge complete: every unit delivered and all scopes were disjoint.")
		}
		return 0
	}

	// Thin seats succeeded but reported no findings, so they are not in the
	// numerator above. Naming them keeps the count honest instead of silently
	// shrinking "Seats Evaluated" (the 2026-10-14 quality-variance gap).
	if summary.ThinSeats > 0 {
		fmt.Fprintf(out, "Thin Seats Excluded:   %d (%s)\n", summary.ThinSeats, seatList(summary.ThinSeatIDs))
	}
	// Drift policy: preset-lineup seats the run dropped because the registry no
	// longer resolves them. They are already missing from "Seats Evaluated"
	// (which keeps the requested count as its denominator) and from the seat
	// list below, so naming them here is what stops a lost-seat run from reading
	// as a clean one.
	printSkippedSeats(out, summary)
	fmt.Fprintf(out, "Degraded Mode:         %v\n", summary.Degraded)
	fmt.Fprintf(out, "Confirmed Issues:      %d\n", summary.ConfirmedCount)
	fmt.Fprintf(out, "Disputed Issues:       %d\n", summary.DisputedCount)
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Artifacts Generated:")
	if rel, err := filepath.Rel(".", filepath.Join(summary.Root, "reports")); err == nil {
		_ = rel
	}
	fmt.Fprintf(out, "  Reconciliation:      %s\n", providers.MaskOperand(filepath.Join(cfg.OutDir, "reconciliation.md")))
	fmt.Fprintf(out, "  Machine Summary:     %s\n", providers.MaskOperand(filepath.Join(cfg.OutDir, "summary.json")))
	for _, seat := range summary.Seats {
		// G6/H2: banner seat-report lines echo registry-derived provider/model
		// names; mask FIRST, then sanitize (sanitizing first breaks the
		// contiguous-run classifier and leaks key-shaped ids). Benign names
		// stay readable. FIX-8: both are registry slots, so they take the
		// registry-slot authority (MaskSlot) rather than the operand masker,
		// which is the identity on the underscore-head credential class.
		fmt.Fprintf(out, "  Seat %d Report:       seat-%d-%s-%s.md\n",
			seat.SeatID, seat.SeatID,
			sanitizeFilename(providers.MaskSlot(seat.FinalTarget.Provider.Name)),
			sanitizeFilename(providers.MaskSlot(seat.FinalTarget.Model.ID)),
		)
	}

	fmt.Fprintln(out)
	switch {
	case summary.Degraded:
		// FL-5: a degraded run must never wear the green "everything verified"
		// banner. The line says what is wrong instead, and names the artifact the
		// reader has to consult before treating the verdict as final.
		fmt.Fprintf(out, "⚠️ Notice: degraded run (%s); the verdict is partial - see reconciliation.md before relying on it\n", degradedSummaryNote(summary))
	case summary.DisputedCount > 0:
		fmt.Fprintf(out, "⚠️ Notice: %d disputed high-severity finding(s) require main-agent arbitration in reconciliation.md\n", summary.DisputedCount)
	default:
		fmt.Fprintln(out, "✅ All findings verified across swarm models.")
	}

	return 0
}

// degradedSummaryNote names the concrete reasons a run is degraded, in the
// banner line above. It is deliberately additive and terse: skipped lineup seats,
// thin seats excluded from the consensus denominator, and seats that reported a
// degraded verdict of their own (spawn failure, timeout, provider error).
func degradedSummaryNote(summary *SummaryReport) string {
	var parts []string
	if summary.SkippedSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d seat(s) skipped", summary.SkippedSeats))
	}
	if summary.ThinSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d thin seat(s) excluded", summary.ThinSeats))
	}
	degradedSeats := 0
	for _, s := range summary.Seats {
		if s.Degraded {
			degradedSeats++
		}
	}
	if degradedSeats > 0 {
		parts = append(parts, fmt.Sprintf("%d seat(s) degraded", degradedSeats))
	}
	if len(parts) == 0 {
		return "no per-seat detail recorded"
	}
	return strings.Join(parts, ", ")
}

// fanoutFlags is the parsed `agy pro fanout` flag surface (minus the prompt
// text, which Run() resolves from --prompt / --prompt-file / stdin first).
type fanoutFlags struct {
	task      string
	n         int
	seats     string
	units     string
	maxNative int
	root      string
	tools     string
	maxTurns  int
	timeout   int
	// timeoutSet reports whether --timeout was explicitly present on the
	// command line (flags.Visit in Run); the flag default is indistinguishable
	// from `--timeout 300` by value alone. Used by the legacy (nil set)
	// buildRunConfig path; ladder mode derives it from set["timeout"].
	timeoutSet bool
	out        string
	// allowHosts carries the raw repeatable --allow-host values (B4-2a);
	// buildRunConfig normalizes them into RunConfig.AllowHosts.
	allowHosts []string
	// searchEndpoint / searchKeyEnv carry the raw B4-2b search_web source
	// flags; buildRunConfig trims them into RunConfig.SearchSource.
	searchEndpoint string
	searchKeyEnv   string
	searchNative   string
	// jsonVal carries the raw --json value; the ladder resolves the effective
	// JSON output mode (--json flag > preset json field > false) because a
	// preset may pin it.
	jsonVal bool
	// preset carries the raw --preset name (P8). Empty means no explicit
	// selection; buildRunConfig then consults EnvFanoutPreset and the store's
	// default_preset key.
	preset string
	// set names the flags EXPLICITLY present on the command line
	// (flags.Visit in Run). A nil map means "constructed by hand / library
	// caller": every raw value is authoritative and the ladder is skipped,
	// which keeps the pre-P8 buildRunConfig behavior (and its direct tests)
	// byte-identical. A non-nil map engages the resolution ladder: a flag
	// absent from set resolves through active preset > providers.json
	// defaults block > engine default.
	set map[string]bool
	// errw receives the P8 stderr notices (defaults-rung drift lines, corrupt
	// preset file warning, dangling default_preset warning). nil discards.
	errw io.Writer
}

// buildRunConfig turns the flag surface into a validated RunConfig. It is the
// ONE choke point for the flag path (A1/A3):
//
//   - --tools is parsed fail-closed (S-8): an unknown token is an error, never
//     a substring match ("nowrite" must not enable write_file);
//   - --units (partition mode, B2) is mutually exclusive with the
//     cross-verification seat source --seats AND with the
//     single-prompt sources --prompt/--prompt-file: a partition run takes one
//     body per unit from the plan file, so mixing either source in is a usage
//     error, never a silently ignored flag;
//   - the plan file is read and parsed here, so a missing file or a malformed
//     plan is a clear usage error (exit 2) instead of an engine failure;
//   - the native-seat concurrency is clamped through
//     mode.ClampNativeSeatConcurrency, the same helper the state.json path
//     uses, so no flag value can widen the native-seat pool past 8;
//   - --timeout: an explicit value is honored exactly; a --units run WITHOUT
//     an explicit --timeout leaves TimeoutSec at 0 so the engine scales the
//     default with the unit count (B7); any other run keeps the flat flag
//     default;
//   - --seats wins over --n for the seat count (the spec list is the truth),
//     and --units wins over both (the plan is the truth);
//   - --allow-host (B4-2a, repeatable) accumulates verbatim and is
//     normalized (lowercase, port-stripped, deduped) into RunConfig.
//     AllowHosts; a non-empty list is what exposes read_url_content to the
//     provider seats. It is a run-level switch, so partition mode accepts
//     it for every provider seat; it never touches native seats. Explicit
//     flag entries are also validated (D14, agenttools.ValidateAllowHosts):
//     a scheme prefix, wildcard, or path can never match the exact
//     allow-list and is a usage error (exit 2), never a silently dead entry.
//   - --search-endpoint / --search-key-env bind the explicit search_web
//     source (the key is an env-var NAME, never read here). With no endpoint,
//     --search-native=auto offers a resolved real-agy fallback and =off keeps
//     the legacy refusal. KeyEnv alone never selects explicit mode.
//   - P8 resolution ladder (only when f.set is non-nil, i.e. Run() built the
//     struct): a flag NOT explicitly present on the command line resolves
//     through the active preset (--preset flag > EnvFanoutPreset env >
//     the store's default_preset key) > the providers.json defaults block
//     (n, timeout_seconds, max_turns, tools, exclude_models;
//     tokenBudget/excludeFamilies stay reserved, read-only here) > the engine
//     default. A nil f.set keeps the legacy behavior: every raw value is
//     authoritative. With no preset file and no defaults values the resolved
//     config equals the pre-P8 flag defaults.
//   - defaults.excludeModels (D1) is CONSUMED here as the run's pool-exclusion
//     policy (RunConfig.ExcludeModel): auto-seating and failover skip the
//     models it names, while an explicit --seats / plan-unit pick is
//     honored and warned about on stderr. An absent key keeps the built-in
//     default (the k3 rule); an explicit empty list excludes nothing.
//   - an explicitly selected preset (--preset or EnvFanoutPreset) that does
//     not exist is a usage error (exit 2), never a silent fallback to another
//     preset; a dangling default_preset key only warns and runs preset-less.
//   - defaults-rung drift is surfaced, never normalized (D4): when the
//     providers.json defaults block supplies a value that differs from the
//     engine default, one notice line per field on f.errw names the field,
//     the stored value and the engine default.
//   - an explicit --seats / --units flag DROPS a conflicting knob
//     of the store's default_preset preset with one notice line (F1 guard,
//     dropConflictingDefaultPresetKnobs), because a caller subsystem that
//     omits a flag by design (the pinned swarm argument vector, partition
//     mode) must never be vetoed by machine configuration it never named. An
//     explicitly selected preset keeps the honest hard error.
//   - the seat lineup is a ladder field like any other: with no explicit
//     --seats, a pinning preset (the store's default_preset, an
//     EnvFanoutPreset name, or --preset) IS the run's lineup, in seat order,
//     and RunConfig.SeatLineupPreset records that it came from a preset. That
//     name is what arms the engine's drift policy (a preset seat that no
//     longer resolves is skipped with a warning instead of failing the run);
//     an explicit --seats lineup keeps the hard error. With no preset and no
//     pin, the lineup is empty and the engine auto-selects exactly as before.
//
// A returned error is a usage error: Run() prints it and exits 2.
func buildRunConfig(f fanoutFlags, promptText string) (RunConfig, error) {
	lad, err := resolveLadder(f)
	if err != nil {
		return RunConfig{}, err
	}
	g := lad.flags
	src := lad.sources
	g.searchNative = strings.ToLower(strings.TrimSpace(g.searchNative))
	if g.searchNative == "" {
		g.searchNative = SearchNativeAuto
	}
	if g.searchNative != SearchNativeAuto && g.searchNative != SearchNativeOff {
		return RunConfig{}, errors.New("--search-native must be auto or off")
	}

	var seatSpecs []SeatSpec
	if g.seats != "" {
		specs, err := ParseSeatSpecs(g.seats)
		if err != nil {
			// P8/R3-item-3: ParseSeatSpecs renders its quoted token through the
			// registry-slot authority, so the token cannot ride this error even for
			// a preset-sourced lineup that never entered Run()'s flag seed list.
			// The seeded MaskSentence pass stays as defense in depth for the
			// preset's OTHER file-sourced fields that a future parser message
			// might quote.
			if src["seats"] == "preset" {
				return RunConfig{}, errors.New(providers.MaskSentence(err.Error(), g.seats, g.tools))
			}
			return RunConfig{}, err
		}
		seatSpecs = specs
	}

	var units []Unit
	if f.units != "" {
		if g.seats != "" {
			return RunConfig{}, errors.New("--units cannot be combined with --seats (partition mode assigns one seat per unit)")
		}
		if promptText != "" {
			return RunConfig{}, errors.New("--units cannot be combined with --prompt or --prompt-file (each unit carries its own body in the plan file)")
		}
		data, err := os.ReadFile(f.units)
		if err != nil {
			return RunConfig{}, fmt.Errorf("failed to read units plan file: %w", err)
		}
		parsed, err := ParsePlan(data)
		if err != nil {
			return RunConfig{}, err
		}
		if len(parsed) == 0 {
			return RunConfig{}, errors.New("units plan file contains no units")
		}
		units = parsed
	}

	normalizedTools, err := ParseToolTokens(g.tools)
	if err != nil {
		return RunConfig{}, err
	}

	// B6/B4-1: partition mode has no CLI-wide write switch. The per-unit scope
	// filter bounds the path tools, and a unit that needs write capability opts
	// in per plan ("write": true); a run-level --tools write/exec would grant
	// every unit's seat write access, which the per-unit opt-in exists to
	// prevent. Refuse the combination here (exit 2, matching the
	// mutual-exclusion style above) instead of running an over-privileged
	// seat. normalizedTools is the canonical closed-set join, so the substring
	// test cannot misfire.
	if len(units) > 0 {
		if toolsGrantsWrite(normalizedTools) {
			return RunConfig{}, errors.New("--units (partition mode) is read-only by default and cannot be combined with --tools write/exec; a unit opts into write per plan (\"write\": true), there is no CLI-wide write switch")
		}
	}

	nSeats := g.n
	switch {
	case len(units) > 0:
		nSeats = len(units)
	case len(seatSpecs) > 0:
		nSeats = len(seatSpecs)
	}

	// B7: an explicit timeout is honored exactly. A partition run WITHOUT
	// one leaves TimeoutSec at 0 so the engine scales the default with the
	// unit count (partitionDefaultTimeoutSec in withDefaults); the non-
	// partition default stays the flat flag default (300) so the config
	// surface the engine receives is unchanged there. P8: "explicit" now
	// covers the preset and defaults rungs too (a configured timeout is
	// intentional and is honored exactly, no partition scaling); only the
	// engine-default rung leaves TimeoutSec at 0 for --units scaling.
	timeoutSec := g.timeout
	if len(units) > 0 && !lad.timeoutSet {
		timeoutSec = 0
	}

	// D14 (P3-5): explicit --allow-host entries are validated, not just
	// normalized. Matching is exact on the normalized URL hostname, so a
	// scheme prefix, wildcard, or path can never match and would sit in the
	// list silently refusing every read_url_content call; refuse at
	// collection time (usage error, exit 2) instead. Only the flag rung is
	// checked: the editor save path validates its own rows, and SavePresets
	// stays permissive for round-tripping hand-edited files. The offending
	// value is never echoed (F5 masking family).
	if src["allow_hosts"] == "flag" && !agenttools.ValidateAllowHosts(g.allowHosts) {
		return RunConfig{}, errors.New("--allow-host entries must be bare hostnames or IPs (no scheme, wildcard, or path)")
	}

	// U2a / FL-3: when the PRESET rung supplied the seat lineup, record where it
	// came from. The engine reads SeatLineup.Standing() to decide whether the
	// lineup carries standing cost approval (default_preset rung only: a seat
	// that no longer resolves is skipped there, and the run needs no per-run
	// re-ask). A named preset (--preset / AGY_PRO_FANOUT_PRESET) resolves the
	// same lineup but stays ad-hoc: its metered seats keep the per-run echo and
	// the caller's go-ahead, and a drifted seat is still a hard error. An
	// explicit --seats run has no origin at all (src["seats"] == "flag"), which is
	// what keeps the honest hard error for the operator's own pick.
	lineup := SeatLineupOrigin{}
	if src["seats"] == "preset" {
		lineup = SeatLineupOrigin{Preset: lad.presetName, Rung: lad.presetRung}
	}
	// FL-3(b): the lineup echo. It is unconditional (not gated on --json, a
	// terminal, or the standing rung) because it is the only place a caller
	// learns which seats and how many METERED seats this run is about to use:
	// the vector itself carries no --seats on the pin path, so without this line
	// a launcher-triggered run consumes the pin silently (FL-11). Ad-hoc rungs
	// get the extra clause, because for them the echo IS the consent surface.
	if lineup.Preset != "" && len(seatSpecs) > 0 && f.errw != nil {
		fmt.Fprintln(f.errw, seatLineupEchoWithKeyState(lineup, seatSpecs, lad.defaultKeySet))
	}

	return RunConfig{
		Task:                 g.task,
		Prompt:               promptText,
		N:                    nSeats,
		Seats:                seatSpecs,
		SeatLineup:           lineup,
		Units:                units,
		MaxNativeConcurrency: resolveMaxNative(g.maxNative),
		Root:                 g.root,
		Tools:                normalizedTools,
		MaxTurns:             g.maxTurns,
		TimeoutSec:           timeoutSec,
		OutDir:               g.out,
		AllowHosts:           agenttools.NormalizeAllowHosts(g.allowHosts),
		SearchSource: agenttools.SearchSource{
			Endpoint: g.searchEndpoint,
			KeyEnv:   g.searchKeyEnv,
		},
		SearchNative: g.searchNative,
		Sources:      src,
		JSONOutput:   lad.jsonOut,
		// D1 (SESSION7 P4-3): the run's pool-exclusion policy, built from the
		// providers.json defaults block the ladder read. The engine applies it
		// to auto-seating and failover only; explicit picks are honored and
		// warned about.
		ExcludeModel: PolicyExcluder(lad.exclusion),
	}, nil
}

// seatLineupEcho renders the ONE run-level line that states where the seat
// lineup came from, how many seats it holds and how many of them are metered.
// The preset name is a file-sourced registry slot (MaskSlot) and the lineup is a
// comma list of seat/model TOKENS, each rendered through the same registry-slot
// authority (MaskSeatTokens = MaskSlot per token, which also strips the Cc/Cf
// class): R2-1 proved that MaskOperand is the identity on the underscore-head
// credential class, and this line lands in stderr, in an agent's scrollback and
// in every run log.
//
// The ad-hoc clause is not decoration: for the --preset and env rungs the run is
// NOT covered by standing approval, so this line is the operator's chance to see
// the metered seat count before the spend. A seed adopted without a store key
// keeps the default_preset rung but also says "no key set".
//
// Production callers must use seatLineupEchoWithKeyState with the measured key
// state. This two-argument wrapper hardcodes key-set-true and is retained only
// for test/pin compatibility.
func seatLineupEcho(origin SeatLineupOrigin, specs []SeatSpec) string {
	return seatLineupEchoWithKeyState(origin, specs, true)
}

func seatLineupEchoWithKeyState(origin SeatLineupOrigin, specs []SeatSpec, defaultKeySet bool) string {
	raw := make([]string, 0, len(specs))
	metered := 0
	for _, s := range specs {
		raw = append(raw, s.Raw)
		if s.Kind != SeatKindNative {
			metered++
		}
	}
	plural := "s"
	if metered == 1 {
		plural = ""
	}
	rung := providers.MaskOperand(origin.Rung)
	rungSuffix := ""
	if origin.Standing() && !defaultKeySet {
		rungSuffix = ", no key set"
	}
	line := fmt.Sprintf("fanout: seat lineup from preset %q (%s rung%s): %d seat(s), %d metered provider seat%s (%s)",
		providers.MaskSlot(providers.StripSlotControls(origin.Preset)),
		rung, rungSuffix, len(specs), metered, plural,
		MaskSeatTokens(strings.Join(raw, ",")))
	if !origin.Standing() {
		line += "; a named preset is ad-hoc - confirm the metered seats above before the run spends"
	}
	return line
}

// appliedPresetSeats reports whether this preset's seats pin is the value the run
// will actually use: nothing explicit outranks it (an explicit --seats flag, or
// a --units partition run, both of which win over the preset
// rung). It is the gate every preset-seats notice goes through, so no advisory
// describes a lineup the run does not adopt (R2-4).
func appliedPresetSeats(f fanoutFlags, preset *Preset) bool {
	if preset == nil || preset.Seats == "" || f.set == nil {
		return false
	}
	if f.set["seats"] {
		return false
	}
	return strings.TrimSpace(f.units) == ""
}

// presetSeatsProvenanceNotice renders the ONE advisory line about a standing
// (store default) seats pin whose provenance is unclear, or "" when the marker
// vouches for it. Neither cause blocks anything: the run uses the on-disk lineup
// either way, and this line is what stops a hand-authored pin from silently
// arming standing cost approval (R2-3). The preset name is a file-sourced
// registry slot, so it renders through MaskSlot and never echoes a raw operand.
func presetSeatsProvenanceNotice(preset *Preset) string {
	name := providers.MaskSlot(providers.StripSlotControls(preset.Name))
	switch {
	case preset.SeatsSHA256 == "":
		return fmt.Sprintf("fanout: notice: preset %q pins a seat lineup with NO provenance marker (hand-authored or pre-marker record); using it as the store default lineup anyway - advisory only, and a hand-authored pin is not standing approval",
			name)
	case preset.SeatsSHA256 != SeatsHash(preset.Seats):
		return fmt.Sprintf("fanout: notice: preset %q seats were edited outside the preset editor (stored hash mismatch); using the on-disk lineup anyway - this is advisory, not a seal, and a hand-edited pin is not standing approval",
			name)
	}
	return ""
}

// Engine-default rung values for the P8 ladder. They mirror the flag defaults
// registered in Run() and the engine's withDefaults literals; naming them
// keeps the drift notices and the fall-through branches honest.
const (
	defaultFanoutN         = 3
	defaultFanoutMaxTurns  = 500
	defaultFanoutMaxNative = 1
	defaultFanoutTools     = "read,search"
)

// ladderResult is resolveLadder's output: the effective (post-ladder) flag
// values, the B7 explicit-timeout flag for the effective values, the
// per-field rung map, the resolved --json switch, and the pool-exclusion
// policy built from the providers.json defaults block.
type ladderResult struct {
	flags      fanoutFlags
	timeoutSet bool
	sources    map[string]string
	jsonOut    bool
	// presetName names the preset the ladder adopted ("" when the run had no
	// preset at all), and presetRung says how it was selected
	// (RungDefaultPreset / RungPresetFlag / RungPresetEnv). buildRunConfig turns
	// the pair into RunConfig.SeatLineup when the preset rung supplied the seat
	// lineup; the rung is what scopes standing cost approval (FL-3) and what
	// summary.json records.
	presetName    string
	presetRung    string
	defaultKeySet bool
	// exclusion is the user-owned exclusion policy (D1) derived from the
	// defaults block this ladder read. The zero value (no user list) is the
	// built-in default policy, which is what a ladder-less caller and an
	// absent excludeModels key both yield.
	exclusion providers.ExclusionPolicy
}

// ExclusionUnreadNotice is the ONE line that tells the operator a providers.json
// read failed for a reason other than "no registry file", so the user-owned
// exclusion list (defaults.excludeModels) could not be applied and the
// excluded-pick advisories are suppressed for this run. It names the class of the
// failure and nothing else: no path, no file contents, no key value. The run
// itself keeps failing open onto the built-in default policy, exactly as the
// ladder did before the notice existed (the engine's own hard failure on a
// corrupt registry is unchanged and still governs the run).
const ExclusionUnreadNotice = "warning: providers.json could not be read; the user-owned exclusion list (defaults.excludeModels) is not applied, and excluded-seat advisories are suppressed for this run"

// ExclusionListUnread classifies a providers.Load() result for the notice above.
// A readable registry is healthy. A missing one (os.ErrNotExist, or the
// helpers' providers.ErrNotConfigured) carries no user-owned list to drop, so it
// stays silent: that is the same absent-registry reading the settings editor's
// reload() makes, and the pre-notice behavior every ladder test pins. Anything
// else - a malformed file, a permission error, a nil registry - means a list may
// exist but could not be read.
func ExclusionListUnread(reg *providers.Registry, err error) bool {
	if err == nil && reg != nil {
		return false
	}
	if err != nil && (errors.Is(err, os.ErrNotExist) || errors.Is(err, providers.ErrNotConfigured)) {
		return false
	}
	return true
}

// WarnExclusionUnread writes ExclusionUnreadNotice. A nil writer (a library
// caller that passed none) is a no-op, matching the ladder's own w fallback.
func WarnExclusionUnread(w io.Writer) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, ExclusionUnreadNotice)
}

// resolveLadder applies the P8 resolution ladder to the parsed flag surface
// and returns the effective raw values:
//
//	CLI flag (explicitly set) > active preset > providers.json defaults
//	block > engine default
//
// The active preset is selected by --preset, else the EnvFanoutPreset
// environment variable (the ladder's env rung for the preset as a whole;
// per-field env spellings were deliberately not added, D6 reserves env
// names), else the store's default_preset key. A nil f.set means the struct
// was constructed by hand (direct buildRunConfig callers, pre-P8 tests):
// every raw value is authoritative and the ladder is skipped, which keeps the
// legacy behavior byte-identical.
func resolveLadder(f fanoutFlags) (ladderResult, error) {
	if f.set == nil {
		// Ladder-less callers keep the zero policy: the built-in default
		// exclusion rule, exactly what the engine substitutes for a nil seam.
		return ladderResult{flags: f, timeoutSet: f.timeoutSet}, nil
	}
	w := f.errw
	if w == nil {
		w = io.Discard
	}

	preset, fromDefaultKey, defaultKeySet, err := loadActivePresetWithKeyState(f, w)
	if err != nil {
		return ladderResult{}, err
	}
	presetRung := ""
	if preset != nil {
		presetRung = selectPresetRung(f)
	}
	if fromDefaultKey {
		// F1 guard (P3-1) FIRST: the store's default_preset key is machine
		// configuration the caller never named, so it must not veto a flag the
		// caller did pass. See dropConflictingDefaultPresetKnobs.
		preset = dropConflictingDefaultPresetKnobs(f, preset, w)

		// FL-3(e) / R2-3 / R2-4: advisory provenance, printed only when this
		// preset's seats pin is the value the run will actually USE. The guard
		// above may have dropped it (--units) and an explicit --seats
		// outranks it, so a notice printed before that decision would describe a
		// lineup the run never adopts (R2-4). Two distinct causes, never a
		// block:
		//   - the store stamped a marker and the on-disk value no longer matches
		//     it: the seats were edited outside the preset editor;
		//   - the record carries NO marker at all: a hand-authored row, or one
		//     written before the marker existed. Silence there would arm standing
		//     cost approval on a lineup no editor ever produced (R2-3).
		if preset != nil && appliedPresetSeats(f, preset) {
			if notice := presetSeatsProvenanceNotice(preset); notice != "" {
				fmt.Fprintln(w, notice)
			}
		}
	}

	// Rung 4: the providers.json defaults block, READ-ONLY here (D4: every
	// providers.json write stays gated by the M4 approval modal). A missing
	// or unreadable registry simply contributes no defaults; TokenBudget and
	// ExcludeFamilies stay reserved and are not consumed.
	def := providers.Defaults{}
	reg, lerr := providers.Load()
	if lerr == nil && reg != nil {
		def = reg.Defaults
	}
	// FIX-G F1 (round-2 LOW): the fail-open read above is unchanged - it still
	// contributes no defaults and the run still resolves - but it is no longer
	// SILENT when a user-owned exclusion list exists and could not be read. One
	// line on the notice writer names the class, so the operator knows the
	// excluded-pick advisories are suppressed for this run. A missing registry
	// carries no list to drop and stays silent (ExclusionListUnread).
	if ExclusionListUnread(reg, lerr) {
		WarnExclusionUnread(w)
	}

	// D1 (SESSION7 P4-3): the defaults block's excludeModels list IS consumed
	// here, as the run's pool-exclusion policy. The engine applies it to
	// auto-seating and failover; an explicit pick stays honored. An absent key
	// leaves the zero policy, whose built-in default is the k3 rule; an
	// explicit empty list excludes nothing because the user owns the list.
	exclusion := providers.ExclusionPolicyFromDefaults(def)

	// defaultsNotice surfaces rung-4 drift, never normalizes it (D4: the live
	// file carries timeoutSeconds 120 while the engine default is 300; the
	// stored value wins and the operator sees why).
	defaultsNotice := func(field string, stored, engineDefault any) {
		// MaskSlot handles credential shapes; stripEchoControls also removes the
		// Zl/Zp class MaskSlot deliberately preserves. Benign bytes stay unchanged.
		value := providers.MaskSlot(stripEchoControls(fmt.Sprint(stored)))
		fmt.Fprintf(w, "fanout: using providers.json default %s=%s (engine default %v)\n", field, value, engineDefault)
	}

	g := f
	src := map[string]string{}

	// task
	switch {
	case f.set["task"]:
		src["task"] = "flag"
	case preset != nil && preset.Task != "":
		g.task = preset.Task
		src["task"] = "preset"
	default:
		g.task = "review"
		src["task"] = "engine"
	}

	// n (the --seats/--units seat-count override applies afterwards,
	// unchanged).
	switch {
	case f.set["n"]:
		src["n"] = "flag"
	case preset != nil && preset.N > 0:
		g.n = preset.N
		src["n"] = "preset"
	case def.N > 0:
		g.n = def.N
		src["n"] = "defaults"
		if def.N != defaultFanoutN {
			defaultsNotice("n", def.N, defaultFanoutN)
		}
	default:
		g.n = defaultFanoutN
		src["n"] = "engine"
	}

	// seats (the raw spec string; parsed downstream, so a preset-sourced parse
	// error is masked at the parse site). It is the ONLY seat source: the
	// retired preset `models` field is gone, and an empty value means the engine
	// auto-selects the lineup exactly as before.
	switch {
	case f.set["seats"]:
		src["seats"] = "flag"
	case preset != nil && preset.Seats != "":
		g.seats = preset.Seats
		src["seats"] = "preset"
	default:
		g.seats = ""
		src["seats"] = "engine"
	}

	// root / out. Only the preset rung is traversal-checked here: explicit
	// flags keep their existing contract, and an overridden bad preset value
	// does not block a run that never adopts it. Absolute paths stay legal.
	switch {
	case f.set["root"]:
		src["root"] = "flag"
	case preset != nil && preset.Root != "":
		if hasParentPathComponent(preset.Root) {
			return ladderResult{}, fmt.Errorf("preset root refused: %w", ErrPresetPathTraversal)
		}
		g.root = preset.Root
		src["root"] = "preset"
	default:
		g.root = "."
		src["root"] = "engine"
	}
	switch {
	case f.set["out"]:
		src["out"] = "flag"
	case preset != nil && preset.OutDir != "":
		if hasParentPathComponent(preset.OutDir) {
			return ladderResult{}, fmt.Errorf("preset out_dir refused: %w", ErrPresetPathTraversal)
		}
		g.out = preset.OutDir
		src["out"] = "preset"
	default:
		g.out = ""
		src["out"] = "engine"
	}

	// tools
	switch {
	case f.set["tools"]:
		src["tools"] = "flag"
	case preset != nil && preset.Tools != "":
		g.tools = preset.Tools
		src["tools"] = "preset"
	case def.Tools != "":
		g.tools = def.Tools
		src["tools"] = "defaults"
		if def.Tools != defaultFanoutTools {
			defaultsNotice("tools", def.Tools, defaultFanoutTools)
		}
	default:
		g.tools = defaultFanoutTools
		src["tools"] = "engine"
	}

	// max-turns
	switch {
	case f.set["max-turns"]:
		src["max_turns"] = "flag"
	case preset != nil && preset.MaxTurns > 0:
		g.maxTurns = preset.MaxTurns
		src["max_turns"] = "preset"
	case def.MaxTurns > 0:
		g.maxTurns = def.MaxTurns
		src["max_turns"] = "defaults"
		if def.MaxTurns != defaultFanoutMaxTurns {
			defaultsNotice("max_turns", def.MaxTurns, defaultFanoutMaxTurns)
		}
	default:
		g.maxTurns = defaultFanoutMaxTurns
		src["max_turns"] = "engine"
	}

	// timeout
	timeoutSet := false
	switch {
	case f.set["timeout"]:
		timeoutSet = true
		src["timeout"] = "flag"
	case preset != nil && preset.TimeoutSeconds > 0:
		g.timeout = preset.TimeoutSeconds
		timeoutSet = true
		src["timeout"] = "preset"
	case def.TimeoutSeconds > 0:
		g.timeout = def.TimeoutSeconds
		timeoutSet = true
		src["timeout"] = "defaults"
		if def.TimeoutSeconds != DefaultTimeoutSec {
			defaultsNotice("timeout_seconds", def.TimeoutSeconds, DefaultTimeoutSec)
		}
	default:
		g.timeout = DefaultTimeoutSec
		src["timeout"] = "engine"
	}

	// max-native: 0/negative keeps the existing state.json fallback rung
	// (flag > state.json > default 1); the preset sits between them.
	switch {
	case f.set["max-native"]:
		src["max_native"] = "flag"
	case preset != nil && preset.MaxNative > 0:
		g.maxNative = preset.MaxNative
		src["max_native"] = "preset"
	default:
		g.maxNative = 0
		src["max_native"] = "state"
	}

	// allow-hosts (the repeatable flag accumulates verbatim; the preset list
	// fills in only when the flag supplied none).
	if len(g.allowHosts) > 0 {
		src["allow_hosts"] = "flag"
	} else if preset != nil && len(preset.AllowHosts) > 0 {
		g.allowHosts = preset.AllowHosts
		src["allow_hosts"] = "preset"
	}

	// search_web source (endpoint + key env NAME). The endpoint is the explicit
	// mode switch; a key by itself keeps the native fallback and its rung notice.
	if g.searchEndpoint != "" || g.searchKeyEnv != "" {
		src["search"] = "flag"
	} else if preset != nil && (preset.SearchEndpoint != "" || preset.SearchKeyEnv != "") {
		g.searchEndpoint = strings.TrimSpace(preset.SearchEndpoint)
		g.searchKeyEnv = strings.TrimSpace(preset.SearchKeyEnv)
		src["search"] = "preset"
	} else {
		src["search"] = "engine"
	}
	if f.set["search-native"] && strings.TrimSpace(g.searchEndpoint) == "" {
		src["search"] = "flag"
	}

	// json output switch
	jsonOut := false
	switch {
	case f.set["json"]:
		jsonOut = f.jsonVal
		src["json"] = "flag"
	case preset != nil && preset.JSON:
		jsonOut = true
		src["json"] = "preset"
	default:
		src["json"] = "engine"
	}

	// Disclosure on every preset-selection rung, deliberately not a gate: one
	// line lists every effective privilege/egress knob after flag precedence and
	// the F1 drop guard. Named and environment presets keep their existing ad-hoc
	// echo/confirm policy; this notice never refuses a run or requests consent.
	if notice := presetPrivilegeEgressNotice(preset, src, fromDefaultKey); notice != "" {
		fmt.Fprintln(w, notice)
	}

	return ladderResult{
		flags:         g,
		timeoutSet:    timeoutSet,
		sources:       src,
		jsonOut:       jsonOut,
		exclusion:     exclusion,
		presetName:    activePresetName(preset),
		presetRung:    presetRung,
		defaultKeySet: defaultKeySet,
	}, nil
}

// searchSourceNotice discloses the effective search route on every CLI run.
// The bracketed value is the resolution rung. The native route has TWO lines
// since P32, and the caller picks between them from the run's presence answer:
//
//   - restricted child: the deployed `agypro-search` agent asset is listed by
//     `agy agents`, so the engine passes `--agent agypro-search` and the child's
//     tool surface is structurally web-only (4 tools vs 12 on the live binary,
//     and the restriction holds under --dangerously-skip-permissions).
//   - fallback child: the asset is not deployed, so the pre-P32 argv runs and
//     the prompt sentence is the only (soft) control. The line says so.
//
// F-10A disclosure half (disclose-only doctrine): the line always names the
// CHILD's scope as well as the quota cost. Since P33 BOTH native lines carry the
// `; subscription per search` clause, and the restricted one names the scope
// qualitatively instead of enumerating the tool pair: the enumeration
// (`restricted child: search_web+read_url_content`) spent exactly all 80 display
// columns, so it could not also disclose the quota cost, and the cost is the
// property an operator has to know before a run. The pair stays documented by the
// deployed asset's own frontmatter (agents/agypro-search/AGENT.md) and by the
// changelog. Widths at the widest rung label (engine/preset, 6 cells): the
// fallback line is exactly 80 and the restricted line 76.
func searchSourceNotice(endpoint, nativeMode, rung, restrictedAgent string) string {
	if rung == "" {
		rung = "engine"
	}
	line := ""
	if strings.TrimSpace(endpoint) != "" {
		host := "invalid-endpoint"
		if u, err := url.Parse(strings.TrimSpace(endpoint)); err == nil && u.Hostname() != "" {
			host = maskPresetNoticeOperand("search_endpoint", u.Hostname())
		}
		line = fmt.Sprintf("fanout[%s] search=endpoint(%s)", rung, host)
	} else if strings.EqualFold(strings.TrimSpace(nativeMode), SearchNativeOff) {
		line = fmt.Sprintf("fanout[%s] search=off", rung)
	} else if strings.TrimSpace(restrictedAgent) != "" {
		line = fmt.Sprintf("fanout[%s] search=agy-native (restricted child; subscription per search)", rung)
	} else {
		line = fmt.Sprintf("fanout[%s] search=agy-native (full agy agent child; subscription per search)", rung)
	}
	return cellwidth.Truncate(line, 80)
}

// presetPrivilegeEgressNotice renders at most ONE notice for preset values that
// change tool privilege, search egress, the tool root, or the artifact
// destination. src is the post-ladder source map, so dropped or flag-overridden
// knobs are absent and seats never duplicate the lineup echo. The preset name
// uses MaskSlot. Operands render through the union guard (maskPresetNoticeOperand,
// round-10 adjudication): measure-then-emit with a normalized-window gate and a
// lexical word grammar, so no credential plant leaves a >=16 raw window while
// benign locations keep their bytes. Endpoints keep MaskBaseURL's
// userinfo/secret-parameter handling before the positional arm.
//
// LAYOUT (F-9C): the knob clauses lead the line and the label clause trails it,
// so the operative `knob="…"` clause sits inside the first 80 display columns
// even with the real standing label (the shipped leading label pushed the first
// knob to column 88 and cellwidth.Truncate(notice, 80) lost every knob). The
// strings "privilege/egress knobs:" and `preset "(built-in defaults)"` remain
// exact substrings of the line, and the line stays a single line.
func presetPrivilegeEgressNotice(preset *Preset, src map[string]string, standing bool) string {
	if preset == nil {
		return ""
	}
	fields := []struct {
		knob   string
		source string
		value  string
	}{
		{"tools", "tools", preset.Tools},
		{"search_endpoint", "search", preset.SearchEndpoint},
		{"search_key_env", "search", preset.SearchKeyEnv},
		{"root", "root", preset.Root},
		{"out_dir", "out", preset.OutDir},
	}
	disclosed := make([]string, 0, len(fields))
	for _, field := range fields {
		if src[field.source] != "preset" || field.value == "" {
			continue
		}
		disclosed = append(disclosed, fmt.Sprintf("%s=%q", field.knob,
			maskPresetNoticeOperand(field.knob, field.value)))
	}
	if len(disclosed) == 0 {
		return ""
	}
	label := "preset"
	if standing {
		label = "standing preset"
	}
	// LAYOUT (F-9C): clauses first, label clause last — the operative knob
	// clause then sits within 80 display columns with the real standing label.
	return fmt.Sprintf("fanout: %s (privilege/egress knobs: %s %q)", strings.Join(disclosed, ", "),
		label, providers.MaskSlot(providers.StripSlotControls(preset.Name)))
}

// noticeFloor is the suite's shared opaque-run floor (p25PlaintextRunAtFloor /
// providers' slotOpaqueRunFloorRunes): a window of this many raw base64-alphabet
// runes is the exposure metric the notice contract is judged by.
const noticeFloor = 16

// maskPresetNoticeOperand is the round-10 union guard (debug-adjudication-report
// 7.2): one review seat's measure-then-emit skeleton with another seat's lexical
// sub-run word grammar substituted for the hump arm. A positional render (with
// the word-safe bypass) is the candidate; if its separator-normalized form
// carries no >=16 raw window it is emitted unchanged (benign byte identity and
// F-18's two-mask shape fall out here). Otherwise a credential-cased class
// verdict escalates to the whole-value MaskSlot render, with a MaskSecret
// determinism floor and a metric backstop. The gate reads the exact string it
// emits (or its classification-only separator normalization). Gate-false implies
// window-free output; gate-true plus credential-cased implies masked output.
// Gate-true plus class-false is the disclosed fail-open residual pinned by
// TestP27ResidualDisclosure and TestP28DisclosedResidual_R11MaskedButLeakyEndpoint /
// TestP28DisclosedResidual_Class1EndpointAndInteriorDecoration.
func maskPresetNoticeOperand(knob, value string) string {
	value = providers.StripSlotControls(value)
	classify := noticeClassifyCopy(knob, value)
	r0 := noticePositionalFate(knob, value, true) // positional arm + word-safe bypass
	if !noticeRunAtFloor(strings.ReplaceAll(r0, `\`, "/")) {
		return r0
	}
	if !noticeCredentialCasedValue(classify) {
		return r0
	}
	out := providers.MaskSlot(value)
	if out == value {
		out = providers.MaskSecret(value)
	}
	if noticeRunAtFloor(strings.ReplaceAll(out, `\`, "/")) {
		out = providers.MaskSecret(value)
	}
	return out
}

// noticeClassifyCopy is the classification-only normalization: the whole value
// is classified, including any endpoint text before a scheme, and backslashes
// join windows exactly as slashes do (a base64 secret can wear UNC /
// drive-backslash spellings). Rendering always uses the original bytes.
func noticeClassifyCopy(_, value string) string {
	return strings.ReplaceAll(value, `\`, "/")
}

// noticeRunAtFloor is the sink-local twin of the suite's p25PlaintextRunAtFloor
// metric (16 consecutive raw [A-Za-z0-9+/=] runes). The gate measures the
// separator-normalized render so a backslash plant cannot hide behind the raw
// metric's backslash blindness.
func noticeRunAtFloor(v string) bool {
	n := 0
	for _, r := range v {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
			n++
			if n >= noticeFloor {
				return true
			}
		default:
			n = 0
		}
	}
	return false
}

// noticePositionalFate selects the positional render for a knob: endpoints keep
// MaskBaseURL's userinfo/secret-parameter handling before the component arm,
// bare endpoints and scalars use MaskSlot, and root/out_dir split on the path
// separators. wordSafe enables the bypass below.
func noticePositionalFate(knob, value string, wordSafe bool) string {
	if knob == "search_endpoint" {
		if strings.Contains(value, "://") {
			masked := providers.MaskBaseURL(value)
			masked = strings.ReplaceAll(masked, "%7Bquery%7D", "{query}")
			masked = strings.ReplaceAll(masked, "%7bquery%7d", "{query}")
			return noticePositionalArm(masked, "/"+`\`+"?&=#", wordSafe)
		}
		return providers.MaskSlot(value)
	}
	if knob != "root" && knob != "out_dir" {
		return providers.MaskSlot(value)
	}
	return noticePositionalArm(value, "/"+`\`, wordSafe)
}

// noticePositionalArm renders each separator-delimited segment through MaskSlot,
// rejoining with the RAW separator. The word-safe bypass keeps a >=16-rune pure
// alnum segment that is not credential-cased byte-identical: MaskSlot's
// contiguous-run arm would otherwise destroy word components
// (kubernetesmanifests, PersonalProjects2026, 20-digit timestamps) — the F-9B
// render-layer defect. Head/decorated segments still route through MaskSlot, so
// the F-18 prefix-overlap shape (sk_li****/sk_li****) is preserved.
func noticePositionalArm(value, separators string, wordSafe bool) string {
	var out strings.Builder
	out.Grow(len(value))
	start := 0
	emit := func(seg string) {
		if wordSafe && utf8.RuneCountInString(seg) >= noticeFloor && noticePureAlnum(seg) &&
			!noticeCredentialCasedComponent(seg) {
			out.WriteString(seg)
			return
		}
		out.WriteString(providers.MaskSlot(seg))
	}
	for i := 0; i < len(value); i++ {
		if !strings.ContainsRune(separators, rune(value[i])) {
			continue
		}
		emit(value[start:i])
		out.WriteByte(value[i])
		start = i + 1
	}
	emit(value[start:])
	return out.String()
}

func noticeDigitBlocks(s string) int {
	n, in := 0, false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			if !in {
				n++
				in = true
			}
		} else {
			in = false
		}
	}
	return n
}

func noticePureAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func noticePureAlnumOrSlash(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			r == '/' || r == '+' || r == '=') {
			return false
		}
	}
	return true
}

// noticeHeadPieces reports whether a credential head (the frozen
// providers.SlotSecretPrefixes policy list) opens a piece of the component at a
// decoration boundary (start, or after an ASCII space/colon). Anchored
// HasPrefix only — "task-queue" must not fire on its embedded "sk-".
func noticeHeadPieces(c string) bool {
	for _, piece := range strings.FieldsFunc(c, func(r rune) bool { return r == ' ' || r == ':' }) {
		lower := strings.ToLower(piece)
		for _, p := range providers.SlotSecretPrefixes() {
			if strings.HasPrefix(lower, p) {
				return true
			}
		}
	}
	return false
}

// noticeSubRunWordShaped is a review seat's lexical sub-run grammar (its 3.2,
// acronym cap 6): one maximal alnum run parses as maximal LOW/CAPS/DIGIT class
// runs, and only shapes a naming convention produces are accepted. A lone
// lowercase between capitals, a lone digit between capitals, and CAPS runs
// beyond the acronym band are key structure (wJalr…, K7MDENG,
// ABCDEFGHIJKLMNOPQRST), not words. The grammar has no length floor, so the
// 15/16/17 boundary is eliminated rather than tuned.
func noticeSubRunWordShaped(rs []rune) bool {
	type classRun struct {
		kind byte
		n    int
	}
	runs := make([]classRun, 0, 8)
	for _, r := range rs {
		kind := byte('l')
		switch {
		case r >= 'A' && r <= 'Z':
			kind = 'u'
		case r >= '0' && r <= '9':
			kind = 'd'
		}
		if n := len(runs); n > 0 && runs[n-1].kind == kind {
			runs[n-1].n++
			continue
		}
		runs = append(runs, classRun{kind: kind, n: 1})
	}
	for i, r := range runs {
		prevDigit := i > 0 && runs[i-1].kind == 'd'
		nextDigit := i+1 < len(runs) && runs[i+1].kind == 'd'
		nextLower := i+1 < len(runs) && runs[i+1].kind == 'l'
		switch r.kind {
		case 'l':
			if r.n >= 2 || len(runs) == 1 {
				continue
			}
			if prevDigit || nextDigit {
				continue
			}
			if i > 0 && runs[i-1].kind == 'u' && runs[i-1].n == 1 {
				if i+1 == len(runs) {
					continue
				}
				if i+2 < len(runs) && runs[i+1].kind == 'u' && runs[i+2].kind == 'l' && runs[i+2].n >= 2 {
					continue
				}
			}
			return false
		case 'u':
			if r.n >= 2 && r.n <= 6 {
				continue
			}
			if len(runs) == 1 && r.n == 1 {
				continue
			}
			if nextLower && runs[i+1].n >= 2 {
				continue
			}
			if nextLower && runs[i+1].n == 1 && i+2 < len(runs) && runs[i+2].kind == 'u' &&
				i+3 < len(runs) && runs[i+3].kind == 'l' && runs[i+3].n >= 2 {
				continue
			}
			if nextDigit && runs[i+1].n <= 2 &&
				(i+2 == len(runs) || (runs[i+2].kind == 'u' && runs[i+2].n == 1 && i+3 < len(runs) &&
					runs[i+3].kind == 'l' && runs[i+3].n >= 2)) {
				continue
			}
			if prevDigit && i+1 == len(runs) {
				continue
			}
			if prevDigit && nextDigit {
				continue
			}
			return false
		case 'd':
			if r.n >= 2 || len(runs) == 1 {
				continue
			}
			if i > 0 && runs[i-1].kind == 'l' {
				continue
			}
			if nextLower {
				continue
			}
			if i+1 == len(runs) {
				continue
			}
			if i > 0 && runs[i-1].kind == 'u' && runs[i-1].n == 1 && i+2 < len(runs) &&
				runs[i+1].kind == 'u' && runs[i+1].n == 1 && runs[i+2].kind == 'l' && runs[i+2].n >= 2 {
				continue
			}
			return false
		}
	}
	return true
}

func noticeAlnum(c rune) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// noticeComponentWordShaped reads every maximal alnum run of the component;
// any decoration is a separator, so ":C", "{query}" or "agy-pro" are shaped by
// their alnum runs only.
func noticeComponentWordShaped(c string) bool {
	runes := []rune(c)
	for i := 0; i < len(runes); {
		if !noticeAlnum(runes[i]) {
			i++
			continue
		}
		j := i
		for j < len(runes) && noticeAlnum(runes[j]) {
			j++
		}
		if !noticeSubRunWordShaped(runes[i:j]) {
			return false
		}
		i = j
	}
	return true
}

// noticeCredentialCasedComponent is the union's component class test: the head
// arm (H1, mimo), the digit-interleaving arm (H3, mimo), and the lexical word
// grammar (a review seat; this is the arm that replaced another seat's hump
// heuristic, closing letters-only splinters like QwErTyUiO/… while keeping
// docs/APIReference2026 and build/AGYPRO2026/artifacts byte-identical).
func noticeCredentialCasedComponent(c string) bool {
	if c == "" {
		return false
	}
	if noticeHeadPieces(c) {
		return true
	}
	n := utf8.RuneCountInString(c)
	if n >= 8 && noticePureAlnum(c) && noticeDigitBlocks(c) >= 3 {
		return true
	}
	return !noticeComponentWordShaped(c)
}

// noticeMetricRuns splits into maximal base64-alphabet runs (the exposure
// metric's own unit).
func noticeMetricRuns(s string) []string {
	var runs []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			runs = append(runs, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return runs
}

// noticeCredentialCasedValue is the dual-unit class verdict: path components
// (case grammar) plus metric runs (digit interleaving survives decoration and
// slash joins — "token: k9lm2qw7zt4/rb8nc3…" has its digit blocks in one run).
func noticeCredentialCasedValue(classify string) bool {
	for _, c := range strings.Split(classify, "/") {
		if noticeCredentialCasedComponent(c) {
			return true
		}
	}
	for _, run := range noticeMetricRuns(classify) {
		if utf8.RuneCountInString(run) >= 8 && noticePureAlnumOrSlash(run) && noticeDigitBlocks(run) >= 3 {
			return true
		}
	}
	return false
}

// maskPresetLocationComponents keeps its historical signature (a pinned test
// calls it directly); the word-safe bypass is interior, per the F-9B render
// contract.
func maskPresetLocationComponents(value, separators string) string {
	return noticePositionalArm(value, separators, true)
}

// selectPresetRung names how the preset for this run is selected, which is what
// scopes standing cost approval (only the store default_preset rung carries it)
// and what every lineup echo prints. The order mirrors loadActivePreset:
// --preset > AGY_PRO_FANOUT_PRESET > the store key or built-in seed fallback.
func selectPresetRung(f fanoutFlags) string {
	if strings.TrimSpace(f.preset) != "" {
		return RungPresetFlag
	}
	if strings.TrimSpace(os.Getenv(EnvFanoutPreset)) != "" {
		return RungPresetEnv
	}
	return RungDefaultPreset
}

// activePresetName returns the adopted preset's name ("" for no preset). The
// F1 guard hands back a copy, so the name survives a dropped knob.
func activePresetName(preset *Preset) string {
	if preset == nil {
		return ""
	}
	return preset.Name
}

// loadActivePreset resolves the preset for this run (P8 rung 3) and loads it.
// Selection order: --preset flag > EnvFanoutPreset env > the store's
// default_preset key > the built-in defaults seed record (A3, editable
// built-in defaults: a seed record written by the row-[1] editor save is the
// last arm, so a plain run consumes it only when nothing more explicit named
// a preset). The second return value reports the LAST rung: true means the run
// picked the preset up without the caller naming it (the store's
// default_preset key, or the seed record through the same store-default
// rung), which is what arms the F1 drop guard in resolveLadder (an explicitly
// named preset keeps the honest hard error). An explicitly selected name
// (--preset or env) that does not exist is an honest usage error, never a
// silent fallback to another preset; a dangling default_preset key only warns
// on w and runs preset-less (the seed arm is NOT reached on that path: a set
// key, even dangling, outranks the seed). An unavailable presets home or a
// missing file yields no preset (the store's create-on-demand contract); a
// corrupt file is the store's one-line-warning fallback.
func loadActivePreset(f fanoutFlags, w io.Writer) (*Preset, bool, error) {
	preset, fromDefaultKey, _, err := loadActivePresetWithKeyState(f, w)
	return preset, fromDefaultKey, err
}

func loadActivePresetWithKeyState(f fanoutFlags, w io.Writer) (*Preset, bool, bool, error) {
	name := strings.TrimSpace(f.preset)
	if name == "" {
		name = strings.TrimSpace(os.Getenv(EnvFanoutPreset))
	}
	st, err := NewPresetFileStore()
	if err != nil {
		return nil, false, false, nil
	}
	st.WarnOut = w
	pf, err := st.LoadPresets()
	if err != nil {
		return nil, false, false, fmt.Errorf("cannot load fanout presets: %w", err)
	}
	defaultKeySet := strings.TrimSpace(pf.DefaultPreset) != ""
	fromDefaultKey := false
	if name == "" {
		name = strings.TrimSpace(pf.DefaultPreset)
		fromDefaultKey = defaultKeySet
	}
	if name == "" {
		// A3 ladder arm: the built-in defaults seed record. fromDefaultKey MUST
		// stay true here - the seed rides the store-default rung so that (1)
		// selectPresetRung reports RungDefaultPreset and SeatLineupOrigin keeps
		// standing cost approval, (2) the F1 guard
		// (dropConflictingDefaultPresetKnobs) shields the seed's knobs against
		// an explicit --seats/--units and write-granting --tools, and
		// (3) the provenance-marker advisory runs. Returning false would keep
		// the standing approval while silently disarming 2 and 3 - the one
		// dangerous corner of this design, pinned by test. An absent seed is
		// the normal state (A2: nothing is written until the first row-[1]
		// save), so this arm prints nothing.
		if p, ok := pf.FindPreset(BuiltinPresetName); ok {
			return p, true, defaultKeySet, nil
		}
		return nil, false, defaultKeySet, nil
	}
	if p, ok := pf.FindPreset(name); ok {
		return p, fromDefaultKey, defaultKeySet, nil
	}
	if fromDefaultKey {
		// F5/round-6 M1: the name is file-sourced (a hand-edited
		// fanout-presets.json can plant a credential-shaped default_preset), so
		// the slot is masked through the single registry-slot display authority
		// MaskSlot. Not MaskOperand: a preset name is never a legitimate
		// location, so the "://" location exemption must not let a userinfo
		// credential ("https://user:pass@host/x") ride raw in this echo, and the
		// authority's policy arms (control strip, look-alike fold, composed-label
		// segment arm) are what classify a decorated plant that the pre-policy
		// MaskRegistryValue rendered unchanged.
		fmt.Fprintf(w, "fanout: warning: default preset %q not found in fanout-presets.json; running without a preset\n", providers.MaskSlot(name))
		return nil, false, defaultKeySet, nil
	}
	return nil, false, defaultKeySet, fmt.Errorf("preset %q not found in fanout-presets.json", providers.MaskSlot(name))
}

// defaultPresetConflict is one store-default preset knob the F1 guard drops
// because an explicitly passed flag on this run owns the conflicting field.
type defaultPresetConflict struct {
	knob   string // "seats" | "tools"
	value  string // the preset's raw value (masked before it is echoed)
	reason string // the explicit flag it collides with, and the rule
}

// dropConflictingDefaultPresetKnobs is the F1 guard (P3-1). The store's
// default_preset key is machine configuration the caller never named, and two
// caller subsystems omit flags BY DESIGN: the swarm builder's argument vector
// is pinned at seven flag pairs (internal/swarm/build.go:46-54, pinned by
// internal/swarm/swarm_test.go:21-54) and partition mode takes its seat source
// from the plan file. A default preset pinning seats would otherwise make
// every `agy pro fanout --units` run exit with "--units cannot be combined with
// --seats (partition mode assigns one seat per unit)" at the buildRunConfig
// gate this file owns, and a tools pin granting write/exec breaks the partition
// read-only gate.
//
// Only the STORE-DEFAULT route reaches here: an explicitly selected preset
// (--preset / EnvFanoutPreset) is the user's named choice, so its conflict
// stays the honest hard error. Only knobs that would actually be EFFECTIVE
// are considered (an explicit flag for the same field already wins over the
// preset), each dropped knob emits ONE notice line on w (stderr only, so the
// --json summary on stdout and the dry-run vector stay untouched), and the
// field falls through to the next rung of the ladder.
func dropConflictingDefaultPresetKnobs(f fanoutFlags, preset *Preset, w io.Writer) *Preset {
	if preset == nil || f.set == nil {
		return preset
	}
	partition := strings.TrimSpace(f.units) != ""
	seatsEffective := !f.set["seats"] && preset.Seats != ""
	toolsEffective := !f.set["tools"] && preset.Tools != ""

	var conflicts []defaultPresetConflict
	// The seat source: an explicit --units flag owns the run outright, so the
	// preset's seats pin goes. (An explicit --seats flag already outranks the
	// preset rung by construction, so it needs no drop.)
	if seatsEffective && partition {
		conflicts = append(conflicts, defaultPresetConflict{"seats", preset.Seats,
			"--units (partition mode assigns one seat per unit)"})
	}
	// The read-only gate is value-shaped, not field-shaped: only a pin that
	// actually grants write capability collides with it. A preset tools value
	// that does not parse is left alone, so buildRunConfig still reports the
	// parse error on the preset rung's masked path.
	if partition && toolsEffective {
		if norm, err := ParseToolTokens(preset.Tools); err == nil && toolsGrantsWrite(norm) {
			conflicts = append(conflicts, defaultPresetConflict{"tools", preset.Tools,
				"--units (partition mode is read-only; a unit opts into write per plan)"})
		}
	}
	if len(conflicts) == 0 {
		return preset
	}

	// F5/R3-item-1: the preset name and every preset value are file-sourced (a
	// hand-edited fanout-presets.json can plant credential shapes), so EACH value
	// renders through the authority for its class: the name and the seats
	// token list ride the registry-slot authority (MaskSlot / MaskSeatTokens -
	// MaskOperand is the identity on the underscore-head credential class), and
	// the remaining scalar knob (tools) keeps the operand masker.
	dropped := *preset
	for _, c := range conflicts {
		value := providers.MaskOperand(c.value)
		if c.knob == "seats" {
			value = MaskSeatTokens(c.value)
		}
		fmt.Fprintf(w, "fanout: warning: default preset %q %s=%q conflicts with %s; dropping it\n",
			providers.MaskSlot(preset.Name), c.knob, value, c.reason)
		switch c.knob {
		case "seats":
			dropped.Seats = ""
		case "tools":
			dropped.Tools = ""
		}
	}
	return &dropped
}

// toolsGrantsWrite reports whether a normalized tool list grants a write
// capability (write_file / replace_in_file / exec). The partition read-only
// gate in buildRunConfig and the default-preset drop guard share this ONE
// predicate so the two cannot drift.
func toolsGrantsWrite(normalized string) bool {
	lt := strings.ToLower(normalized)
	return strings.Contains(lt, "write") || strings.Contains(lt, "exec")
}

// resolveMaxNative applies the native-seat concurrency precedence of contract
// §2.3 (--max-native flag > state.json native_seat_concurrency > default 1)
// with the shared 1..8 clamp on every branch (A1): `--max-native 100` binds to
// 8, `--max-native 0`/negative resolves the state path, and an unreadable
// state file falls back to the default.
func resolveMaxNative(flagVal int) int {
	if flagVal > 0 {
		return mode.ClampNativeSeatConcurrency(flagVal)
	}
	if st, err := mode.LoadState(); err == nil && st.NativeSeatConcurrency > 0 {
		return mode.ClampNativeSeatConcurrency(st.NativeSeatConcurrency)
	}
	return defaultFanoutMaxNative
}

// ParseToolTokens is a thin wrapper over agenttools.ParseToolTokens (R1-9):
// the parser lives in agenttools so the bridge tool loop can share it without
// importing fanout. Signature and error wording are unchanged.
func ParseToolTokens(spec string) (string, error) {
	return agenttools.ParseToolTokens(spec)
}

// maskOperandSentence renders an error message that may embed raw CLI
// operands (D1/D3 composite-error class). MaskOperand alone cannot mask a
// secret embedded in a full sentence (the sentence never classifies
// secret-shaped), so providers.MaskSentence seeds the redactor with every
// eligible operand (see providers.SeedEligible) plus its trimmed form, and
// falls back to providers.MaskOperand when no operand is eligible, keeping
// benign operands readable. The seed rule lives ONCE in providers (F-alpha) so
// this twin and cmd/agy cannot drift.
func maskOperandSentence(msg string, operands ...string) string {
	return providers.MaskSentence(msg, operands...)
}

// quotedEchoRe splits a flag echo line into double-quoted and unquoted spans,
// mirroring providers' quoted-segment rule: the stdlib quotes the offending
// value in its errors, and RedactEchoLine has already masked that content, so
// the unquoted scan below must not touch it.
var quotedEchoRe = regexp.MustCompile(`"[^"]*"`)

// unquotedRunRe matches a 16+ rune base64-shaped run in an unquoted span: the
// shape of an opaque credential (AWS secret keys, most carrying "/" or "+").
var unquotedRunRe = regexp.MustCompile(`[A-Za-z0-9+/=]{16,}`)

// unquotedJWTRe matches a 3-segment dotted token (JWT shape) in an unquoted
// span. The 16-rune total floor keeps SHORT dotted tokens readable, but it
// does not exempt 16+ rune dotted names: `--config.dev.override` prints
// "-confi****" through this scan (r5 N1, still open outside F-alpha — the
// operand-seeding rule providers.SeedEligible carries the tighter per-segment
// floor; this unquoted scan does not share it).
var unquotedJWTRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+){2}`)

// maskUnquotedEcho masks credential-shaped runs in the UNQUOTED spans of a
// stdlib flag echo line (E1). The flag package prints an undefined flag name
// and a bad-syntax argument bare — `flag provided but not defined:
// -wJalr...EXAMPLEKEY` echoed all 40 runes (live PoC),
// and `bad flag syntax: ----wJalr...` does the same — so the quoted-segment
// redactor cannot see them and the generic patterns only cover sk-/AKIA
// shapes. Each 16+ rune base64 run renders through the canonical classifier
// (providers.MaskOperand) and each 16+ rune JWT-shaped token renders as
// MaskSecret (U8: first 5 runes + ****). Quoted spans are left exactly as
// RedactEchoLine rendered them.
func maskUnquotedEcho(line string) string {
	var b strings.Builder
	last := 0
	for _, loc := range quotedEchoRe.FindAllStringIndex(line, -1) {
		b.WriteString(maskUnquotedSpan(line[last:loc[0]]))
		b.WriteString(line[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(maskUnquotedSpan(line[last:]))
	return b.String()
}

func maskUnquotedSpan(span string) string {
	span = unquotedJWTRe.ReplaceAllStringFunc(span, func(tok string) string {
		if len([]rune(tok)) < 16 {
			return tok
		}
		return providers.MaskSecret(tok)
	})
	return unquotedRunRe.ReplaceAllStringFunc(span, providers.MaskOperand)
}

// maskedFlagWriter is the flag.FlagSet output sink (D8, mirrors cmd/agy's
// writer): stdlib parse errors echo the raw operand — `invalid value "sk-..."
// for flag -timeout` — and undefined flag names unquoted. Every complete line
// passes through providers.RedactEchoLine (quoted segments), then
// providers.MaskOperand (generic credential patterns catch unquoted sk-/AKIA
// runs), and finally maskUnquotedEcho (E1: 16+ rune base64 runs and
// JWT-shaped tokens outside quotes) before it reaches stderr. Benign lines are
// unchanged by all three passes.
type maskedFlagWriter struct {
	w    io.Writer
	tail []byte
}

func (m *maskedFlagWriter) Write(p []byte) (int, error) {
	m.tail = append(m.tail, p...)
	for {
		i := bytes.IndexByte(m.tail, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(m.tail[:i]), "\r")
		m.tail = m.tail[i+1:]
		if _, err := fmt.Fprintln(m.w, maskUnquotedEcho(providers.MaskOperand(providers.RedactEchoLine(line)))); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}
