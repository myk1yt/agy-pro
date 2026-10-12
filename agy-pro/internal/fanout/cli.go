package fanout

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// Run executes the fanout CLI command with standard exit codes.
func Run(args []string, in io.Reader, out, errw io.Writer) int {
	flags := flag.NewFlagSet("fanout", flag.ContinueOnError)
	flags.SetOutput(errw)

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
		flags.PrintDefaults()
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
				fmt.Fprintf(errw, "Error reading prompt file %s: %v\n", *promptFileFlag, err)
				return 1
			}
			promptText = string(data)
		}
	}

	// 3. Resolve explicit models list
	var explicitModels []string
	if *modelsFlag != "" {
		for _, m := range strings.Split(*modelsFlag, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				explicitModels = append(explicitModels, m)
			}
		}
	}

	// 3b. Resolve seat specs (--seats) — contract §2.1. --seats and --models
	// are mutually exclusive (fail-closed, exit 2).
	var seatSpecs []SeatSpec
	if *seatsFlag != "" {
		if *modelsFlag != "" {
			fmt.Fprintln(errw, "Error: --seats and --models cannot be combined")
			return 2
		}
		specs, err := ParseSeatSpecs(*seatsFlag)
		if err != nil {
			fmt.Fprintf(errw, "Error: %v\n", err)
			return 2
		}
		seatSpecs = specs
	}

	// 4. Construct RunConfig
	// --tools parsing is fail-closed (S-8): unknown tokens are rejected
	// instead of being ignored or substring-matched ("nowrite" must never
	// enable write_file).
	normalizedTools, err := ParseToolTokens(*toolsFlag)
	if err != nil {
		fmt.Fprintf(errw, "Error: %v\n", err)
		return 2
	}

	// Native-seat concurrency precedence (contract §2.3):
	// --max-native flag > state.json native_seat_concurrency > default 2.
	maxNative := *maxNativeFlag
	if maxNative <= 0 {
		if st, serr := mode.LoadState(); serr == nil {
			maxNative = st.EffectiveNativeSeatConcurrency()
		} else {
			maxNative = mode.DefaultNativeSeatConcurrency
		}
	}

	nSeats := *nFlag
	if len(seatSpecs) > 0 {
		nSeats = len(seatSpecs)
	}

	cfg := RunConfig{
		Task:                 *taskFlag,
		Prompt:               promptText,
		N:                    nSeats,
		Models:               explicitModels,
		Seats:                seatSpecs,
		MaxNativeConcurrency: maxNative,
		Root:                 *rootFlag,
		Tools:                normalizedTools,
		MaxTurns:             *maxTurnsFlag,
		TimeoutSec:           *timeoutFlag,
		OutDir:               *outFlag,
	}

	// 5. Execute Fanout Engine
	ctx := context.Background()
	summary, err := Execute(ctx, cfg)
	if err != nil {
		// S-7: provider error snippets may echo credential fragments; the
		// console line uses the same Redactor path as the reports.
		fmt.Fprintf(errw, "Fanout error: %v\n", providers.NewRedactor()(err.Error()))
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
	fmt.Fprintf(out, "Task:                  %s\n", summary.Task)
	fmt.Fprintf(out, "Seats Evaluated:       %d / %d\n", summary.NCompleted, summary.NRequested)
	fmt.Fprintf(out, "Degraded Mode:         %v\n", summary.Degraded)
	fmt.Fprintf(out, "Confirmed Issues:      %d\n", summary.ConfirmedCount)
	fmt.Fprintf(out, "Disputed Issues:       %d\n", summary.DisputedCount)
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Artifacts Generated:")
	if rel, err := filepath.Rel(".", filepath.Join(summary.Root, "reports")); err == nil {
		_ = rel
	}
	fmt.Fprintf(out, "  Reconciliation:      %s\n", filepath.Join(cfg.OutDir, "reconciliation.md"))
	fmt.Fprintf(out, "  Machine Summary:     %s\n", filepath.Join(cfg.OutDir, "summary.json"))
	for _, seat := range summary.Seats {
		fmt.Fprintf(out, "  Seat %d Report:       seat-%d-%s-%s.md\n",
			seat.SeatID, seat.SeatID,
			sanitizeFilename(seat.FinalTarget.Provider.Name),
			sanitizeFilename(seat.FinalTarget.Model.ID),
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

// ParseToolTokens is a thin wrapper over agenttools.ParseToolTokens (R1-9):
// the parser lives in agenttools so the bridge tool loop can share it without
// importing fanout. Signature and error wording are unchanged.
func ParseToolTokens(spec string) (string, error) {
	return agenttools.ParseToolTokens(spec)
}
