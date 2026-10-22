package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/swarm"
)

// Swarm builder command (contract §2.6, U3):
//
//	agy pro swarm
//	    interactive numbered builder (TTY stdin, zero non-interactive flags)
//	agy pro swarm --template T --seats S [--root R] [--prompt P | --prompt-file F]
//	              [--timeout N] [--dry-run]
//	    non-interactive (zero prompts), the documented power-user/CI fallback
//
// F-B interactivity rule (binding): the interactive builder runs ONLY when
// stdin is a TTY AND NONE of the non-interactive flags are present; ANY of
// --template/--seats/--root/--prompt/--prompt-file/--timeout/--dry-run present
// means ZERO prompts, even on a TTY. Missing required input errors out with
// exit 2 and never prompts.
//
// Phase 1: unused-but-compilable; U-wiring adds the one-line main.go dispatch.

// proSwarmUsage is the `agy pro swarm` usage text. The timeout default is
// rendered from fanout.DefaultTimeoutSec (the single source of truth shared
// with the fanout CLI flag), so this copy cannot drift from the constant the
// way the old hardcoded "120" did.
var proSwarmUsage = fmt.Sprintf(`Usage: agy pro swarm [options]

Interactive (default): numbered template + seat builder (needs a TTY).
Non-interactive (any option below present; zero prompts):
  --template NAME     code-review | security-audit | custom   (required)
  --seats SPEC        numbered selection ("1,3,5", "2-4", "p1".."p3") or the
                      name of a saved seat preset ("deep"), or explicit seat
                      specs ("native:id,prov/model")  (required)
  --root DIR          workspace root (default ".")
  --prompt TEXT       user prompt (default: template scaffold)
  --prompt-file PATH  prompt file, or '-' for stdin
  --tools SPEC        allowed tools: read,search,write,exec (default: the
                      template's tools)
  --task NAME         fanout task type (default: the template's task)
  --timeout SECONDS   swarm timeout (default %d)
  --dry-run           print the equivalent "agy pro fanout" command and exit 0

Seat assignment is numbered selection in the happy path; flags are the
power-user/CI fallback.
Seats excluded by the user-owned exclusion list (defaults.excludeModels in
providers.json) are never auto-seated by p1/p2/p3. Naming one explicitly (a
number, a range, or a seat spec) honors it and prints a warning; nothing is
refused because of the exclusion list.`, fanout.DefaultTimeoutSec)

// loadSwarmInventory builds the combined inventory with the canonical
// env-scrubbed, cached real-agy fetcher (contract §2.5). Tests replace it to
// drive runSwarmCmd without spawning `agy models`.
var loadSwarmInventory = func(ctx context.Context) (*inventory.Inventory, error) {
	reg, _ := providers.Load()
	nf := inventory.Cached(inventory.NewRealAgyFetcher(reg), inventory.DefaultCacheTTL)
	return inventory.Load(ctx, reg, nf)
}

// loadSwarmPresets is the saved-preset seam for --seats name resolution (F04):
// the names a user saved in Settings > Saved Seat Presets become selection
// tokens. A store that cannot be opened or read degrades to "no saved presets"
// (the numbers/preset grammar is untouched); the store's own corrupt-file
// warning still prints on its default WarnOut. Tests replace it to drive
// resolveFlagSeats without touching the live ~/.agy-pro/swarm-state.json.
var loadSwarmPresets = func() []swarm.SavedPreset {
	st, err := swarm.NewFileStore()
	if err != nil {
		return nil
	}
	presets, err := st.LoadPresets()
	if err != nil {
		return nil
	}
	return presets
}

// loadSwarmPolicy is the exclusion-policy seam for the explicit seat-spec path
// (P4-4): a spec line never loads the inventory, so its hand-built rows need
// the policy directly or they cannot carry Entry.Excluded. A registry that
// cannot be read degrades to the built-in default policy - exactly what
// inventory.Load does for a nil registry - so the spec path keeps working
// without a providers.json. Tests replace it to pin the verdict without
// touching the live ~/.agy-pro/providers.json.
//
// FIX-G F1 (round-2 LOW): the second result reports a failure that dropped a
// user-owned list (fanout.ExclusionListUnread), so the caller can print the
// one-line notice. The degradation itself is unchanged: a missing or unreadable
// registry still resolves the pick.
var loadSwarmPolicy = func() (providers.ExclusionPolicy, bool) {
	reg, err := providers.Load()
	if err != nil || reg == nil {
		return providers.ExclusionPolicyFromDefaults(providers.Defaults{}), fanout.ExclusionListUnread(reg, err)
	}
	return providers.ExclusionPolicyFromDefaults(reg.Defaults), false
}

// stdinIsTTY reports whether the input reader is an interactive terminal
// (contract F-B). Tests replace it to simulate a TTY with an instrumented
// reader.
var stdinIsTTY = func(in io.Reader) bool {
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

// outIsTTY reports whether the output writer is an interactive terminal (C3:
// ANSI only on a TTY). Tests replace it alongside stdinIsTTY.
var outIsTTY = func(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runSwarmCmd executes `agy pro swarm [flags]` and returns the process exit
// code (0 ok/copy/cancel, 1 run failure, 2 flag/usage/input error).
func runSwarmCmd(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("swarm", flag.ContinueOnError)
	// D8: stdlib flag output routes through the echo-line redactor so parse
	// errors cannot echo a planted operand.
	fs.SetOutput(&maskedFlagWriter{w: errw})
	var (
		templateFlag = fs.String("template", "", "swarm template: code-review|security-audit|custom")
		seatsFlag    = fs.String("seats", "", "seat selection: numbered ('1,3,5', '2-4', 'p1'..'p3'), a saved preset name, or explicit specs")
		rootFlag     = fs.String("root", ".", "workspace root directory")
		promptFlag   = fs.String("prompt", "", "inline user prompt text")
		promptFile   = fs.String("prompt-file", "", "path to prompt file, or '-' for stdin")
		toolsFlag    = fs.String("tools", "", "allowed tools: read,search,write,exec (default: the template's tools)")
		taskFlag     = fs.String("task", "", "fanout task type (default: the template's task)")
		timeoutFlag  = fs.Int("timeout", 0, fmt.Sprintf("swarm execution timeout in seconds (default %d)", fanout.DefaultTimeoutSec))
		dryRunFlag   = fs.Bool("dry-run", false, "print the equivalent agy pro fanout command and exit 0")
	)
	fs.Usage = func() {
		fmt.Fprintln(errw, proSwarmUsage)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(errw, proSwarmUsage)
		return 2
	}

	// F-B: presence, not value. Any non-interactive flag set switches to the
	// zero-prompt path even on a TTY.
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "template", "seats", "root", "prompt", "prompt-file", "tools", "task", "timeout", "dry-run":
			setFlags[f.Name] = true
		}
	})
	nonInteractive := len(setFlags) > 0

	if nonInteractive {
		return runSwarmNonInteractive(in, out, errw, swarmFlagValues{
			template:   *templateFlag,
			seats:      *seatsFlag,
			root:       *rootFlag,
			prompt:     *promptFlag,
			promptFile: *promptFile,
			tools:      *toolsFlag,
			task:       *taskFlag,
			timeout:    *timeoutFlag,
			timeoutSet: setFlags["timeout"],
			dryRun:     *dryRunFlag,
		})
	}

	// Interactive builder: TTY gate (F-B). No flags were set, so nothing else
	// to validate before prompting.
	if !stdinIsTTY(in) {
		fmt.Fprintln(errw, "Error: interactive swarm builder requires a terminal; pass --template/--seats (and friends) for non-interactive runs")
		fmt.Fprintln(errw, proSwarmUsage)
		return 2
	}
	return runSwarmInteractive(in, out, errw)
}

// runSwarmInteractive drives the numbered builder (contract U3 flow).
func runSwarmInteractive(in io.Reader, out, errw io.Writer) int {
	inv, err := loadSwarmInventory(context.Background())
	if err != nil {
		fmt.Fprintf(errw, "Error: model inventory error: %v\n", err)
		return 1
	}
	if inv == nil || len(inv.Entries) == 0 {
		fmt.Fprintln(errw, "Error: model inventory is empty; register providers or install real agy first")
		return 1
	}
	var store swarm.Store
	if s, serr := swarm.NewFileStore(); serr == nil {
		store = s
	}
	b := &swarm.Builder{
		In:      in,
		Out:     out,
		ErrW:    errw,
		Inv:     inv,
		Store:   store,
		UseANSI: outIsTTY(out),
		Root:    ".",
	}
	return b.Run()
}

// swarmFlagValues carries the parsed non-interactive flag surface (values plus
// set-presence, which is what the F-B rule keys on).
type swarmFlagValues struct {
	template   string
	seats      string
	root       string
	prompt     string
	promptFile string
	tools      string
	task       string
	timeout    int
	timeoutSet bool
	dryRun     bool
}

// runSwarmNonInteractive resolves the flag surface with ZERO prompts (F-B/C2).
// Missing required input is an error (exit 2), never a prompt.
func runSwarmNonInteractive(in io.Reader, out, errw io.Writer, fv swarmFlagValues) int {
	var missing []string
	if strings.TrimSpace(fv.template) == "" {
		missing = append(missing, "--template")
	}
	if strings.TrimSpace(fv.seats) == "" {
		missing = append(missing, "--seats")
	}
	if len(missing) > 0 {
		fmt.Fprintf(errw, "Error: missing required option(s): %s\n", strings.Join(missing, ", "))
		fmt.Fprintln(errw, proSwarmUsage)
		return 2
	}

	tmpl, ok := swarm.FindTemplate(fv.template)
	if !ok {
		fmt.Fprintf(errw, "Error: unknown template %q (want code-review|security-audit|custom)\n", providers.MaskOperand(fv.template))
		fmt.Fprintln(errw, proSwarmUsage)
		return 2
	}
	// A2 follow-up: layer the --task/--tools flag equivalents of the
	// interactive builder's edits onto the template (empty = keep default).
	tmpl = swarm.SwarmFlags{Tools: fv.tools, Task: fv.task}.ApplyToTemplate(tmpl)

	seats, code := resolveFlagSeats(fv.seats, errw)
	if code >= 0 {
		return code
	}

	prompt := fv.prompt
	if fv.promptFile != "" {
		data, err := readPromptSource(in, fv.promptFile)
		if err != nil {
			// C2/D1: os.ReadFile embeds the operand path in its error text;
			// seed the redactor with the raw operand so the planted secret is
			// masked wherever it appears in the composite sentence. Batch K:
			// the hardened two-pass slot. The whole-string classifier runs
			// first (single-token, one-space, location-shaped, and benign
			// renderings stay byte-identical), then EVERY whitespace- or
			// comma/semicolon-carrying operand routes through the seeded
			// sentence masker with the per-token seeds (the wide SeedEligible
			// core), applied to the already-masked slot and seeded from the
			// ORIGINAL operand. The old equality-only fallback was skipped
			// whenever MaskOperand PARTIALLY masked the operand, so a compound
			// path carrying both a pattern-shaped credential and a bare
			// decorated secret echoed the bare secret raw. Batch L: the gate
			// also carries the comma/semicolon class the old equality fallback
			// used to seed, so a whitespace-free comma-decorated operand
			// ("./backups,<secret>") no longer keeps its bare secret raw.
			pathSlot := providers.MaskOperand(fv.promptFile)
			if strings.ContainsAny(fv.promptFile, " \t\n\r,;") {
				pathSlot = maskOperandSentence(pathSlot, providers.FlagSeedOperands(fv.promptFile)...)
			}
			// Batch M: the composite tail beside the slot takes the same
			// per-token seeds as the slot (and as the cmd/agy main.go rollback
			// twin). The whole operand alone is not enough: a location-shaped
			// whole operand ("./backups,<secret>") never seeds (SeedEligible's
			// location exemption), so the sentence fell back to MaskOperand,
			// whose generic patterns do not cover a bare secret token, and the
			// tail re-echoed it raw while the slot beside it was masked.
			// FlagSeedOperands keeps the whole value AND adds each
			// comma/space/semicolon-separated token, so the token seed closes
			// the sentence; separator-free operands seed exactly what they
			// seeded before (byte-identical).
			fmt.Fprintf(errw, "Error reading prompt file %s: %v\n", pathSlot, maskOperandSentence(err.Error(), providers.FlagSeedOperands(fv.promptFile)...))
			return 1
		}
		prompt = data
	}

	timeout := fanout.DefaultTimeoutSec
	if fv.timeoutSet {
		if fv.timeout <= 0 {
			fmt.Fprintln(errw, "Error: --timeout must be a positive integer of seconds")
			fmt.Fprintln(errw, proSwarmUsage)
			return 2
		}
		timeout = fv.timeout
	}

	root := fv.root
	if root == "" {
		root = "."
	}
	spec := swarm.RunSpec{
		Template:   tmpl,
		Seats:      seats,
		Root:       root,
		Prompt:     prompt,
		PromptFile: fv.promptFile,
		TimeoutSec: timeout,
	}

	args := spec.Args()
	if fv.dryRun {
		fmt.Fprintln(out, swarm.FormatCommand(args))
		return 0
	}
	return swarm.Execute(spec, in, out, errw, outIsTTY(out))
}

// seatSeedOperands returns the redactor seeds for a --seats value: the whole
// value plus each comma/space/semicolon-separated token, because selection and
// seat-spec errors quote individual tokens ("invalid selection token
// \"native:<plant>\"") while a whole-value seed would not match them.
func seatSeedOperands(seatsVal string) []string {
	return providers.FlagSeedOperands(seatsVal)
}

// resolveFlagSeats turns a --seats value into builder entries. Numbered
// selection resolves against the combined inventory; explicit seat specs are
// the power-user fallback (validated by fanout.ParseSeatSpecs). Returns a
// non-negative process exit code on failure.
//
// F04: a token that names a saved preset makes the line selection input, so
// `--seats deep` resolves that preset's stored Numbers. An unknown name that
// is still a valid bare seat spec stays on the spec path (the pre-F04
// precedence: a bare provider model id is a legal spec and must not be
// hijacked by the name grammar).
//
// Every exclusion warning this function produces rides errw, never stdout: the
// fanout CLI prints the very same sentence on its error writer (RunConfig.
// WarnOut = errw in internal/fanout/cli.go), and stdout on this path is the
// machine-relayed dry-run command echo, which a warning line would interleave
// (S7-C10).
func resolveFlagSeats(seatsVal string, errw io.Writer) ([]inventory.Entry, int) {
	presets := loadSwarmPresets()
	if swarm.IsSelectionInputWith(seatsVal, presets) {
		inv, err := loadSwarmInventory(context.Background())
		if err != nil {
			fmt.Fprintf(errw, "Error: model inventory error: %v\n", err)
			return nil, 1
		}
		entries, err := swarm.ResolveSeatsWith(seatsVal, inv, presets, errw)
		if err != nil {
			// C2/D1: selection errors embed the raw token; a misdirected paste
			// in the --seats slot must not reach stderr unmasked. The error
			// text routes through providers.MaskSentence (maskOperandSentence),
			// which seeds the redactor with the raw --seats value when
			// providers.SeedEligible says it may seed, so the planted secret is
			// masked wherever the sentence echoes it. F04: a saved name expands
			// to its stored Numbers, so the seed list also carries each resolved
			// Numbers value (an out-of-range seat error quotes a number token).
			fmt.Fprintf(errw, "Error: %v\n", maskOperandSentence(err.Error(), swarm.SelectionSeeds(seatsVal, presets)...))
			return nil, 2
		}
		if len(entries) == 0 {
			fmt.Fprintln(errw, "Error: --seats selection is empty")
			return nil, 2
		}
		return entries, -1
	}
	specs, err := fanout.ParseSeatSpecs(seatsVal)
	if err != nil {
		// C2/D1: seat-spec errors quote the raw token; seed with the raw
		// --seats value so the planted secret is masked wherever it appears.
		fmt.Fprintf(errw, "Error: %v\n", maskOperandSentence(err.Error(), seatSeedOperands(seatsVal)...))
		return nil, 2
	}
	policy, policyUnread := loadSwarmPolicy()
	if policyUnread {
		// FIX-G F1: the read failed while a user-owned exclusion list may exist,
		// so the rows below carry the built-in default policy instead and every
		// excluded-pick advisory is suppressed. The run is unchanged (the pick is
		// still honored, nothing is rejected); only the silence is fixed. The
		// notice rides errw, never stdout: stdout here is the machine-relayed
		// dry-run command echo (S7-C10).
		fanout.WarnExclusionUnread(errw)
	}
	entries := swarm.EntriesFromSeatSpecs(specs, policy)
	swarm.WarnExcludedSpecs(entries, errw)
	return entries, -1
}

// readPromptSource returns the prompt text from --prompt-file ('-' = stdin).
func readPromptSource(in io.Reader, file string) (string, error) {
	if file == "-" {
		data, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
