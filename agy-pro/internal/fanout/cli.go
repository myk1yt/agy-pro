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
		nFlag          = flags.Int("n", 3, "Number of models to fan out across")
		modelsFlag     = flags.String("models", "", "Comma-separated model identifiers (e.g. 'openai/gpt-4o,deepseek/deepseek-chat')")
		seatsFlag      = flags.String("seats", "", "Comma-separated seat specs mixing native and provider seats: 'native:<model-id>' for a real agy (Gemini) subprocess seat, 'provider/model' or 'model' for an external provider seat (e.g. 'native:gemini-3.8-flash-high,nw/glm-5.3-flash')")
		maxNativeFlag  = flags.Int("max-native", 0, "Maximum concurrent native (real agy) seats; 0 uses state.json native_seat_concurrency or the default 2 (excess native seats queue)")
		rootFlag       = flags.String("root", ".", "Workspace root directory for tool execution")
		toolsFlag      = flags.String("tools", "read,search", "Allowed tools: read,search,write,exec (closed set; unknown tokens are rejected)")
		maxTurnsFlag   = flags.Int("max-turns", 10, "Maximum turns for agentic tool loop")
		timeoutFlag    = flags.Int("timeout", 120, "Swarm execution timeout in seconds")
		outFlag        = flags.String("out", "", "Output directory for generated reports (default ./reports/fanout/<ts>)")
		jsonFlag       = flags.Bool("json", false, "Output summary JSON to stdout")
		setupFlag      = flags.Bool("setup", false, "Launch interactive provider setup wizard")
	)

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
	if *promptFileFlag != "" {
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
				fmt.Fprintf(errw, "Error reading prompt file %s: %v\n",
					providers.MaskOperand(*promptFileFlag), maskOperandSentence(err.Error(), *promptFileFlag))
				return 1
			}
			promptText = string(data)
		}
	}

	// 3. The remaining flag surface is validated and turned into a RunConfig by
	// buildRunConfig: the ONE choke point where raw flag values become an
	// engine-ready config (A1/A3).
	cfg, err := buildRunConfig(fanoutFlags{
		task:      *taskFlag,
		n:         *nFlag,
		models:    *modelsFlag,
		seats:     *seatsFlag,
		maxNative: *maxNativeFlag,
		root:      *rootFlag,
		tools:     *toolsFlag,
		maxTurns:  *maxTurnsFlag,
		timeout:   *timeoutFlag,
		out:       *outFlag,
	}, promptText)
	if err != nil {
		// C2/D3: usage errors quote raw tokens from --seats/--tools (seat-spec
		// and tool-token validators). Seed the redactor with the raw flag
		// values so a planted secret is masked wherever the sentence echoes it.
		fmt.Fprintf(errw, "Error: %v\n", maskOperandSentence(err.Error(), providers.FlagSeedOperands(*seatsFlag, *toolsFlag)...))
		return 2
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
	if *jsonFlag {
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
	fmt.Fprintf(out, "Task:                  %s\n", providers.MaskOperand(summary.Task))
	fmt.Fprintf(out, "Seats Evaluated:       %d / %d\n", summary.NCompleted, summary.NRequested)
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
	maxNative int
	root      string
	tools     string
	maxTurns  int
	timeout   int
	out       string
}

// buildRunConfig turns the flag surface into a validated RunConfig. It is the
// ONE choke point for the flag path (A1/A3):
//
//   - --tools is parsed fail-closed (S-8): an unknown token is an error, never
//     a substring match ("nowrite" must not enable write_file);
//   - --seats and --models are mutually exclusive (contract §2.1, exit 2);
//   - the native-seat concurrency is clamped through
//     mode.ClampNativeSeatConcurrency, the same helper the state.json path
//     uses, so no flag value can widen the native-seat pool past 8;
//   - --seats wins over --n for the seat count (the spec list is the truth).
//
// A returned error is a usage error: Run() prints it and exits 2.
func buildRunConfig(f fanoutFlags, promptText string) (RunConfig, error) {
	var explicitModels []string
	if f.models != "" {
		for _, m := range strings.Split(f.models, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				explicitModels = append(explicitModels, m)
			}
		}
	}

	var seatSpecs []SeatSpec
	if f.seats != "" {
		if f.models != "" {
			return RunConfig{}, errors.New("--seats and --models cannot be combined")
		}
		specs, err := ParseSeatSpecs(f.seats)
		if err != nil {
			return RunConfig{}, err
		}
		seatSpecs = specs
	}

	normalizedTools, err := ParseToolTokens(f.tools)
	if err != nil {
		return RunConfig{}, err
	}

	nSeats := f.n
	if len(seatSpecs) > 0 {
		nSeats = len(seatSpecs)
	}

	return RunConfig{
		Task:                 f.task,
		Prompt:               promptText,
		N:                    nSeats,
		Models:               explicitModels,
		Seats:                seatSpecs,
		MaxNativeConcurrency: resolveMaxNative(f.maxNative),
		Root:                 f.root,
		Tools:                normalizedTools,
		MaxTurns:             f.maxTurns,
		TimeoutSec:           f.timeout,
		OutDir:               f.out,
	}, nil
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
