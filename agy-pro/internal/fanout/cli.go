package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// executeEngine is the engine seam (A3): Run() resolves the whole flag surface
// into a RunConfig and hands it here. Tests replace it to assert flag binding
// at Run() level without spawning seats (same idiom as swarm.RunFanout).
var executeEngine = Execute

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
		unitsFlag      = flags.String("units", "", "Path to a partition plan file (JSON array of units): PARTITION mode runs one seat per unit, each bounded to its unit's scope (mutually exclusive with --prompt, --prompt-file, --seats and --models)")
		nFlag          = flags.Int("n", 3, "Number of models to fan out across")
		modelsFlag     = flags.String("models", "", "Comma-separated model identifiers (e.g. 'openai/gpt-4o,deepseek/deepseek-chat')")
		seatsFlag      = flags.String("seats", "", "Comma-separated seat specs mixing native and provider seats: 'native:<model-id>' for a real agy (Gemini) subprocess seat, 'provider/model' or 'model' for an external provider seat (e.g. 'native:gemini-3.8-flash-high,nw/glm-5.3-flash')")
		maxNativeFlag  = flags.Int("max-native", 0, "Maximum concurrent native (real agy) seats; 0 uses state.json native_seat_concurrency or the default 2 (excess native seats queue)")
		rootFlag       = flags.String("root", ".", "Workspace root directory for tool execution")
		toolsFlag      = flags.String("tools", "read,search", "Allowed tools: read,search,write,exec (closed set; unknown tokens are rejected)")
		maxTurnsFlag   = flags.Int("max-turns", 10, "Maximum turns for agentic tool loop")
		timeoutFlag    = flags.Int("timeout", DefaultTimeoutSec, "Swarm execution timeout in seconds (with --units and no explicit --timeout, the default auto-scales: 300 + 120 per unit, capped at 3600)")
		outFlag        = flags.String("out", "", "Output directory for generated reports (default ./reports/fanout/<ts>)")
		jsonFlag       = flags.Bool("json", false, "Output summary JSON to stdout")
		setupFlag      = flags.Bool("setup", false, "Launch interactive provider setup wizard")
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
	flags.StringVar(&searchEndpoint, "search-endpoint", "", "Endpoint URL template for the search_web tool, containing one {query} placeholder (https only; http for loopback hosts). Provider seats get search_web only when this is set")
	flags.StringVar(&searchKeyEnv, "search-key-env", "", "NAME of the environment variable holding the search endpoint's API key (the value is read at call time and never logged). Optional when the endpoint needs no key")

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
	if *setupFlag {
		if err := providers.InteractiveSetupWizard(in, out); err != nil {
			fmt.Fprintf(errw, "Setup failed: %v\n", err)
			return 1
		}
		return 0
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
		models:         *modelsFlag,
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

	// 5. Execute Fanout Engine
	ctx := context.Background()
	summary, err := executeEngine(ctx, cfg)
	if err != nil {
		// S-7: provider error snippets may echo credential fragments; the
		// console line uses the same Redactor path as the reports. The raw
		// --seats/--models values are seeded too, so a secret in a spec that
		// passes validation but fails to resolve (e.g. a pasted key used as a
		// provider token) cannot reach stderr through the engine error.
		fmt.Fprintf(errw, "Fanout error: %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(*seatsFlag, *modelsFlag)...))
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
		// stay readable.
		fmt.Fprintf(out, "  Seat %d Report:       seat-%d-%s-%s.md\n",
			seat.SeatID, seat.SeatID,
			sanitizeFilename(providers.MaskOperand(seat.FinalTarget.Provider.Name)),
			sanitizeFilename(providers.MaskOperand(seat.FinalTarget.Model.ID)),
		)
	}

	fmt.Fprintln(out)
	if summary.DisputedCount > 0 {
		fmt.Fprintf(out, "⚠️ Notice: %d disputed high-severity finding(s) require main-agent arbitration in reconciliation.md\n", summary.DisputedCount)
	} else {
		fmt.Fprintln(out, "✅ All findings verified across swarm models.")
	}

	return 0
}

// fanoutFlags is the parsed `agy pro fanout` flag surface (minus the prompt
// text, which Run() resolves from --prompt / --prompt-file / stdin first).
type fanoutFlags struct {
	task      string
	n         int
	models    string
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
//   - --seats and --models are mutually exclusive (contract §2.1, exit 2);
//   - --units (partition mode, B2) is mutually exclusive with the
//     cross-verification seat sources --seats/--models AND with the
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
//     it for every provider seat; it never touches native seats.
//   - --search-endpoint / --search-key-env (B4-2b) bind the search_web
//     source into RunConfig.SearchSource (trimmed; the key is referenced by
//     env-var NAME only, the value is never read here). A non-empty endpoint
//     is what exposes search_web to the provider seats; partition mode
//     accepts it for every provider seat; it never touches native seats.
//   - P8 resolution ladder (only when f.set is non-nil, i.e. Run() built the
//     struct): a flag NOT explicitly present on the command line resolves
//     through the active preset (--preset flag > EnvFanoutPreset env >
//     the store's default_preset key) > the providers.json defaults block
//     (n, timeout_seconds, max_turns, tools; tokenBudget/excludeFamilies
//     stay reserved, read-only here) > the engine default. A nil f.set keeps
//     the legacy behavior: every raw value is authoritative. With no preset
//     file and no defaults values the resolved config equals the pre-P8 flag
//     defaults.
//   - an explicitly selected preset (--preset or EnvFanoutPreset) that does
//     not exist is a usage error (exit 2), never a silent fallback to another
//     preset; a dangling default_preset key only warns and runs preset-less.
//   - defaults-rung drift is surfaced, never normalized (D4): when the
//     providers.json defaults block supplies a value that differs from the
//     engine default, one notice line per field on f.errw names the field,
//     the stored value and the engine default.
//
// A returned error is a usage error: Run() prints it and exits 2.
func buildRunConfig(f fanoutFlags, promptText string) (RunConfig, error) {
	lad, err := resolveLadder(f)
	if err != nil {
		return RunConfig{}, err
	}
	g := lad.flags
	src := lad.sources

	var explicitModels []string
	if g.models != "" {
		for _, m := range strings.Split(g.models, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				explicitModels = append(explicitModels, m)
			}
		}
	}

	var seatSpecs []SeatSpec
	if g.seats != "" {
		if g.models != "" {
			return RunConfig{}, errors.New("--seats and --models cannot be combined")
		}
		specs, err := ParseSeatSpecs(g.seats)
		if err != nil {
			// P8: a preset-sourced --seats string is not in Run()'s flag
			// seed list, so the error is masked HERE with the preset's raw
			// seat/model/tool strings as seeds (ParseSeatSpecs quotes the
			// offending token; a planted credential in a hand-edited preset
			// file must not ride the echo).
			if src["seats"] == "preset" {
				return RunConfig{}, errors.New(providers.MaskSentence(err.Error(), g.seats, g.models, g.tools))
			}
			return RunConfig{}, err
		}
		seatSpecs = specs
	}

	var units []Unit
	if f.units != "" {
		if g.seats != "" || g.models != "" {
			return RunConfig{}, errors.New("--units cannot be combined with --seats or --models (partition mode assigns one seat per unit)")
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
		lt := strings.ToLower(normalizedTools)
		if strings.Contains(lt, "write") || strings.Contains(lt, "exec") {
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

	return RunConfig{
		Task:                 g.task,
		Prompt:               promptText,
		N:                    nSeats,
		Models:               explicitModels,
		Seats:                seatSpecs,
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
		Sources:    src,
		JSONOutput: lad.jsonOut,
	}, nil
}

// Engine-default rung values for the P8 ladder. They mirror the flag defaults
// registered in Run() and the engine's withDefaults literals; naming them
// keeps the drift notices and the fall-through branches honest.
const (
	defaultFanoutN        = 3
	defaultFanoutMaxTurns = 10
	defaultFanoutTools    = "read,search"
)

// ladderResult is resolveLadder's output: the effective (post-ladder) flag
// values, the B7 explicit-timeout flag for the effective values, the
// per-field rung map, and the resolved --json switch.
type ladderResult struct {
	flags      fanoutFlags
	timeoutSet bool
	sources    map[string]string
	jsonOut    bool
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
		return ladderResult{flags: f, timeoutSet: f.timeoutSet}, nil
	}
	w := f.errw
	if w == nil {
		w = io.Discard
	}

	preset, err := loadActivePreset(f, w)
	if err != nil {
		return ladderResult{}, err
	}

	// Rung 4: the providers.json defaults block, READ-ONLY here (D4: every
	// providers.json write stays gated by the M4 approval modal). A missing
	// or unreadable registry simply contributes no defaults; TokenBudget and
	// ExcludeFamilies stay reserved and are not consumed.
	def := providers.Defaults{}
	if reg, lerr := providers.Load(); lerr == nil && reg != nil {
		def = reg.Defaults
	}

	// defaultsNotice surfaces rung-4 drift, never normalizes it (D4: the live
	// file carries timeoutSeconds 120 while the engine default is 300; the
	// stored value wins and the operator sees why).
	defaultsNotice := func(field string, stored, engineDefault any) {
		// F5: the stored value is file-sourced (a hand-edited providers.json
		// can plant a credential-shaped value), so the operand slot rides the
		// shared masker like every other echo in this file.
		fmt.Fprintf(w, "fanout: using providers.json default %s=%s (engine default %v)\n", field, providers.MaskOperand(fmt.Sprint(stored)), engineDefault)
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

	// models / seats (raw spec strings; parsed downstream, so a preset-sourced
	// parse error is masked at the parse site).
	switch {
	case f.set["models"]:
		src["models"] = "flag"
	case preset != nil && preset.Models != "":
		g.models = preset.Models
		src["models"] = "preset"
	default:
		g.models = ""
		src["models"] = "engine"
	}
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

	// root / out
	switch {
	case f.set["root"]:
		src["root"] = "flag"
	case preset != nil && preset.Root != "":
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
	// (flag > state.json > default 2); the preset sits between them.
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

	// search_web source (endpoint + key env NAME)
	if g.searchEndpoint != "" || g.searchKeyEnv != "" {
		src["search"] = "flag"
	} else if preset != nil && (preset.SearchEndpoint != "" || preset.SearchKeyEnv != "") {
		g.searchEndpoint = strings.TrimSpace(preset.SearchEndpoint)
		g.searchKeyEnv = strings.TrimSpace(preset.SearchKeyEnv)
		src["search"] = "preset"
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

	return ladderResult{flags: g, timeoutSet: timeoutSet, sources: src, jsonOut: jsonOut}, nil
}

// loadActivePreset resolves the preset for this run (P8 rung 3) and loads it.
// Selection order: --preset flag > EnvFanoutPreset env > the store's
// default_preset key. An explicitly selected name (--preset or env) that does
// not exist is an honest usage error, never a silent fallback to another
// preset; a dangling default_preset key only warns on w and runs preset-less.
// An unavailable presets home or a missing file yields no preset (the store's
// create-on-demand contract); a corrupt file is the store's one-line-warning
// fallback.
func loadActivePreset(f fanoutFlags, w io.Writer) (*Preset, error) {
	name := strings.TrimSpace(f.preset)
	if name == "" {
		name = strings.TrimSpace(os.Getenv(EnvFanoutPreset))
	}
	st, err := NewPresetFileStore()
	if err != nil {
		return nil, nil
	}
	st.WarnOut = w
	pf, err := st.LoadPresets()
	if err != nil {
		return nil, fmt.Errorf("cannot load fanout presets: %w", err)
	}
	fromDefaultKey := false
	if name == "" {
		name = strings.TrimSpace(pf.DefaultPreset)
		fromDefaultKey = name != ""
	}
	if name == "" {
		return nil, nil
	}
	if p, ok := pf.FindPreset(name); ok {
		return p, nil
	}
	if fromDefaultKey {
		// F5/round-6 M1: the name is file-sourced (a hand-edited
		// fanout-presets.json can plant a credential-shaped default_preset), so
		// the slot is masked. MaskRegistryValue, not MaskOperand: a preset name
		// is never a legitimate location, so the "://" location exemption must
		// not let a userinfo credential ("https://user:pass@host/x") ride raw
		// in this echo.
		fmt.Fprintf(w, "fanout: warning: default preset %q not found in fanout-presets.json; running without a preset\n", providers.MaskRegistryValue(name))
		return nil, nil
	}
	return nil, fmt.Errorf("preset %q not found in fanout-presets.json", providers.MaskRegistryValue(name))
}

// resolveMaxNative applies the native-seat concurrency precedence of contract
// §2.3 (--max-native flag > state.json native_seat_concurrency > default 2)
// with the shared 1..8 clamp on every branch (A1): `--max-native 100` binds to
// 8, `--max-native 0`/negative resolves the state path, and an unreadable
// state file falls back to the default.
func resolveMaxNative(flagVal int) int {
	if flagVal > 0 {
		return mode.ClampNativeSeatConcurrency(flagVal)
	}
	if st, err := mode.LoadState(); err == nil {
		return st.EffectiveNativeSeatConcurrency()
	}
	return mode.DefaultNativeSeatConcurrency
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
// -wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` echoed all 40 runes (live PoC),
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
