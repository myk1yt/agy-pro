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

const proSwarmUsage = `Usage: agy pro swarm [options]

Interactive (default): numbered template + seat builder (needs a TTY).
Non-interactive (any option below present; zero prompts):
  --template NAME     code-review | security-audit | custom   (required)
  --seats SPEC        numbered selection ("1,3,5", "2-4", "p1".."p3")
                      or explicit seat specs ("native:id,prov/model")  (required)
  --root DIR          workspace root (default ".")
  --prompt TEXT       user prompt (default: template scaffold)
  --prompt-file PATH  prompt file, or '-' for stdin
  --tools SPEC        allowed tools: read,search,write,exec (default: the
                      template's tools)
  --task NAME         fanout task type (default: the template's task)
  --timeout SECONDS   swarm timeout (default 120)
  --dry-run           print the equivalent "agy pro fanout" command and exit 0

Seat assignment is numbered selection in the happy path; flags are the
power-user/CI fallback.`

// loadSwarmInventory builds the combined inventory with the canonical
// env-scrubbed, cached real-agy fetcher (contract §2.5). Tests replace it to
// drive runSwarmCmd without spawning `agy models`.
var loadSwarmInventory = func(ctx context.Context) (*inventory.Inventory, error) {
	reg, _ := providers.Load()
	nf := inventory.Cached(inventory.NewRealAgyFetcher(reg), inventory.DefaultCacheTTL)
	return inventory.Load(ctx, reg, nf)
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
		seatsFlag    = fs.String("seats", "", "seat selection: numbered ('1,3,5', '2-4', 'p1'..'p3') or explicit specs")
		rootFlag     = fs.String("root", ".", "workspace root directory")
		promptFlag   = fs.String("prompt", "", "inline user prompt text")
		promptFile   = fs.String("prompt-file", "", "path to prompt file, or '-' for stdin")
		toolsFlag    = fs.String("tools", "", "allowed tools: read,search,write,exec (default: the template's tools)")
		taskFlag     = fs.String("task", "", "fanout task type (default: the template's task)")
		timeoutFlag  = fs.Int("timeout", 0, "swarm execution timeout in seconds (default 120)")
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

	seats, code := resolveFlagSeats(fv.seats, out, errw)
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
			fmt.Fprintf(errw, "Error reading prompt file %s: %v\n", pathSlot, maskOperandSentence(err.Error(), fv.promptFile))
			return 1
		}
		prompt = data
	}

	timeout := swarm.DefaultTimeoutSec
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
func resolveFlagSeats(seatsVal string, out, errw io.Writer) ([]inventory.Entry, int) {
	if swarm.IsSelectionInput(seatsVal) {
		inv, err := loadSwarmInventory(context.Background())
		if err != nil {
			fmt.Fprintf(errw, "Error: model inventory error: %v\n", err)
			return nil, 1
		}
		entries, err := swarm.ResolveSeats(seatsVal, inv, out)
		if err != nil {
			// C2/D1: selection errors embed the raw token; a misdirected paste
			// in the --seats slot must not reach stderr unmasked. The error
			// text routes through providers.MaskSentence (maskOperandSentence),
			// which seeds the redactor with the raw --seats value when
			// providers.SeedEligible says it may seed, so the planted secret is
			// masked wherever the sentence echoes it.
			fmt.Fprintf(errw, "Error: %v\n", maskOperandSentence(err.Error(), seatSeedOperands(seatsVal)...))
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
	entries := swarm.EntriesFromSeatSpecs(specs)
	swarm.WarnK3Specs(entries, out)
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
