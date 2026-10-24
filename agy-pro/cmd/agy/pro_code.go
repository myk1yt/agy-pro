package main

// `agy pro code` verb (contract U5): the command surface over
// internal/codework (U3 plan/verify, U4 phases/report) and internal/codesnap
// (U1 snapshot/rollback). The library decides what a phase does; this file
// owns flag parsing, the governance defaults, stdout discipline and the exit
// code, exactly as cmd/agy/pro_swarm.go owns them for the swarm builder.
//
//	agy pro code plan     --ask T | --ask-file F [--root --out --seats --timeout --json --dry-run]
//	agy pro code write    (--plan P | --ask T --scope G --writer S) [--verify CMD] [--max-repairs N]
//	                      [--yes --root --out --timeout --json --dry-run]
//	agy pro code review   --run DIR [--seats --root --out --yes --json --dry-run]
//	agy pro code rollback --run DIR [--root --out --yes --json --dry-run]
//	agy pro code run      plan -> write -> review in one run directory
//
// House rules this door keeps:
//
//   - FAIL-CLOSED FLAGS. One flag.FlagSet per subcommand with ContinueOnError, so
//     an unknown flag, a bad value or a stray operand is exit 2 before anything
//     runs, and the stdlib's own echo lines route through maskedFlagWriter
//     (masking.go:97) so a planted credential in a misdirected slot is masked.
//   - STDOUT CARRIES ONE DOCUMENT. The human CodeRunReport block (or, with
//     --json, the CodeRunReport JSON) owns stdout. Every phase stream is bound
//     to the error writer instead, because the write/plan/review vectors pass
//     `--json` and the engine prints its own summary JSON to the phase stdout
//     (U4 report §6 P2-3: the two documents would interleave). A pin asserts the
//     rule: a phase writing to its stdout writer never reaches stdout.
//   - CONSENT BEFORE SPEND. The library's gate (phases.go gateConsent) is the
//     only consent authority: a provider seat in the plan/review list, or ANY
//     write run, is metered with no cap. A TTY is asked y/N, a non-TTY reader is
//     refused with ErrConsentRequired naming --yes, and a dry run precedes the
//     gate (U4 §4.1) so the vectors can be read without a flag. NOTHING runs on a
//     refusal, so this door maps it to exit 2. The library prints the phase's own
//     truth lines (scope, exec disclosure, verify budget) before the prompt
//     (R3-4); this file adds nothing to that order and only binds the streams.
//   - EXIT CODES 0 ok / 1 run failure / 2 usage. 1 covers a refused plan
//     (BuildPlan), a non-zero fanout exit, an ultimately failed verification
//     (whose report carries the rollback command) and a phase whose every seat
//     failed. 2 covers every flag/operand error, a refused consent and a refused
//     plan/scope (the invocation has to change). The refused scope is exit 2 on
//     BOTH routes (R3-9): `--plan` reaches the refusal inside WritePhase's
//     ingestion check, and the composed `--ask --scope` route reaches the same
//     rules through codework.RefuseScopeEntries BEFORE BuildPlan and before the
//     consent prompt - a governed, non-canonical, root-wide or ledger-claiming
//     entry is never something the operator is asked to consent to. A `degraded`
//     phase - some seats delivered, some did not - is still exit 0: its artifacts
//     exist, and the report's status token says what is missing.
//   - EVERY ECHOED OPERAND IS MASKED (providers.MaskOperand for a single
//     operand, providers.MaskSentence seeded with providers.FlagSeedOperands for
//     a sentence that embeds one). The --ask body is never echoed at all: it is
//     the operator's own model input and it lands in the run directory verbatim.

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agypro/internal/codesnap"
	"agypro/internal/codework"
	"agypro/internal/fanout"
	"agypro/internal/providers"
)

const (
	// codeDefaultPlanSeats / codeDefaultReviewSeats are the documented native
	// line-up (AGENTS.md auto-seat fallback), native-only so the plan and review
	// phases stay free. Both ids were verified present in the live combined
	// inventory (`agy pro models --all`, entries 4 and 12: gemini-3.8-flash-high,
	// claude-sonnet-5-5-high), so the names have not drifted from what the
	// fanout parser resolves. internal/codework ships NO built-in list on
	// purpose (phases.go PlanPhase): the governance line-up lives at the door.
	codeDefaultPlanSeats   = "native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high"
	codeDefaultReviewSeats = "native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high"

	// codeReportsDir / codeTimestampLayout mirror the fanout default output
	// directory (internal/fanout/engine.go:200-202: reports/fanout/<ts>, UTC
	// 20060102-150405). The code door keeps the same convention one level over,
	// which is also the path internal/codesnap's own fixtures assume
	// (codesnap/snapshot_test.go:323).
	codeReportsDir      = "reports/code"
	codeTimestampLayout = "20060102-150405"

	// codeReportFileName is the run's change report: the human block a later
	// `code review --run` reads as its diff artifact. It is written once the
	// tree has been touched (a snapshot exists), so its presence is exactly
	// "this run wrote something".
	codeReportFileName = "report.txt"

	// codeRollbackFileName is the artifact codesnap writes on a restore; the
	// rollback phase reports it as its artifact.
	codeRollbackFileName = "rollback.json"

	// codeListRows bounds each list a rollback print renders, so a run over a
	// large scope cannot flood the console (the full lists stay in
	// <out>/rollback.json).
	codeListRows = 8

	codeDirPerm  = 0o700
	codeFilePerm = 0o600
)

// proCodeUsage is the `agy pro code` usage text (subcommand help, unknown flag,
// missing operand). It is static on purpose: no operand is ever interpolated.
var proCodeUsage = `Usage: agy pro code <subcommand> [options]

  plan      audit the request with the native seats (free) and record the run
  write     execute a partition plan: snapshot -> write run -> verify -> repairs -> diff
  review    review the change report of a completed write run
  rollback  restore the tree from the run's snapshot manifest (never asks, never deletes)
  run       plan -> write -> review in one run directory

Common options:
  --root DIR        workspace the seats and the verifier run inside (default ".")
  --out DIR         run directory (default reports/code/<ts>)
  --timeout SEC     fanout wall clock in seconds (default: the engine's own)
  --json            print the run report as JSON on stdout
  --dry-run         print the exact resolved commands and exit 0; nothing runs

plan:
  --ask TEXT | --ask-file PATH   the request text (- reads stdin)          (required)
  --seats A,B                    plan seats (default the native line-up below)

write:
  --plan PATH                    execute an existing partition plan file
  --scope GLOB                   workspace path a write unit may touch (repeatable)
  --writer SEAT                  the write seat: provider/model (native is refused)
  --verify CMD                   a verification command line (repeatable; all must pass;
                                 omit = compile-only go build+go vet detection)
  --max-repairs N                repair runs after a failed verification (default 1)
  --yes                          consent to the metered seats without asking
  Composing form: --ask/--ask-file + --scope + --writer (a one-unit plan is authored).

review:
  --run DIR                      the run directory holding report.txt       (required)
  --seats A,B                    review seats (default the native line-up below)
  --yes                          consent to the metered seats without asking

rollback:
  --run DIR                      the run directory holding manifest.json + snapshot/ (required)
  --yes                          accepted for uniformity; rollback never asks

review/rollback:
  --run DIR is the single source of truth: it is both the directory read (report.txt
  / manifest.json + snapshot/) and the directory written to. --out is accepted only
  when it names that same directory; a divergent --out is a usage error (exit 2).

run:
  plan -> write -> review. Without --scope it stops after the plan phase and prints
  the derived-scope instruction (exit 0). --plan implies --skip-plan.

Default native seats (plan and review): ` + codeDefaultPlanSeats + `

Cost and consent: a provider seat in a seat list, or any write run, is a metered API
call with no token/cost cap. A terminal asks y/N; a reader that cannot answer is
refused unless --yes is given. Native-only phases never ask.
Exit codes: 0 ok (or a plan-only stop; a degraded phase still exits 0), 1 run failure
(a failed verification is one; the report names the rollback command), 2 usage error,
refused consent, or a refused plan/scope (the invocation has to change).
Artifacts: <out>/phases.log (codework trail), <out>/report.txt (the change report a
review reads), the engine's summary.json / seat-*.md / progress.log in each phase run
directory, and <out>/rollback.json after a rollback.`

// runCodeCmd executes `agy pro code [args]` and returns the process exit code
// (0 ok, 1 run failure, 2 usage/refused consent).
func runCodeCmd(args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errw, proCodeUsage)
		return 2
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	switch sub {
	case "help", "--help", "-h":
		fmt.Fprintln(out, proCodeUsage)
		return 0
	case "plan":
		return runCodePlanCmd(rest, in, out, errw)
	case "write":
		return runCodeWriteCmd(rest, in, out, errw)
	case "review":
		return runCodeReviewCmd(rest, in, out, errw)
	case "rollback":
		return runCodeRollbackCmd(rest, in, out, errw)
	case "run":
		return runCodeRunCmd(rest, in, out, errw)
	default:
		// D2: mask the ORIGINAL-CASE operand (a pasted key id is echoed here).
		fmt.Fprintf(errw, "Error: unknown code subcommand %q (want plan|write|review|rollback|run)\n%s\n", providers.MaskOperand(args[0]), proCodeUsage)
		return 2
	}
}

// codeCommonFlags is the option set every subcommand shares.
type codeCommonFlags struct {
	root    string
	out     string
	timeout int
	jsonOut bool
	dryRun  bool
}

// addCodeCommonFlags registers the shared option set on one FlagSet.
func addCodeCommonFlags(fs *flag.FlagSet, c *codeCommonFlags) {
	fs.StringVar(&c.root, "root", ".", "workspace the seats and the verifier run inside")
	fs.StringVar(&c.out, "out", "", "run directory (default reports/code/<ts>)")
	fs.IntVar(&c.timeout, "timeout", 0, "fanout wall clock in seconds (0 = the engine's own)")
	fs.BoolVar(&c.jsonOut, "json", false, "print the run report as JSON on stdout")
	fs.BoolVar(&c.dryRun, "dry-run", false, "print the exact resolved commands and exit 0; nothing runs")
}

// check rejects a shared flag value the parser cannot judge. -1 means "keep
// going"; any other value is the process exit code.
func (c codeCommonFlags) check(errw io.Writer) int {
	if c.timeout < 0 {
		fmt.Fprintf(errw, "Error: --timeout must be zero or a positive number of seconds (got %d)\n%s\n", c.timeout, proCodeUsage)
		return 2
	}
	return -1
}

// config resolves the shared flags into the library config: root defaults to
// the working directory, the run directory to the fanout-mirrored
// reports/code/<ts>.
func (c codeCommonFlags) config() codework.CodeConfig {
	root := strings.TrimSpace(c.root)
	if root == "" {
		root = "."
	}
	outDir := strings.TrimSpace(c.out)
	if outDir == "" {
		outDir = filepath.Join(codeReportsDir, time.Now().UTC().Format(codeTimestampLayout))
	}
	return codework.CodeConfig{
		Root:       root,
		OutDir:     outDir,
		DryRun:     c.dryRun,
		JSONOut:    c.jsonOut,
		TimeoutSec: c.timeout,
	}
}

// runCodePlanCmd runs the plan-audit phase. Native seats are free, so the
// default invocation asks nothing; a provider seat in --seats routes through
// the library's consent gate.
func runCodePlanCmd(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("code plan", flag.ContinueOnError)
	fs.SetOutput(&maskedFlagWriter{w: errw})
	var (
		askFlag = fs.String("ask", "", "the request text the plan phase audits")
		askFile = fs.String("ask-file", "", "path to the request text, or '-' for stdin")
		seats   = fs.String("seats", "", "plan seats (default: the documented native line-up)")
		common  codeCommonFlags
	)
	addCodeCommonFlags(fs, &common)
	fs.Usage = func() { fmt.Fprintln(errw, proCodeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := codeRefuseExtraArgs(fs, errw); code != 0 {
		return code
	}
	if code := common.check(errw); code >= 0 {
		return code
	}
	ask, code := codeResolveAsk(in, *askFlag, *askFile, errw)
	if code >= 0 {
		return code
	}

	cfg := common.config()
	cfg.Ask = ask
	cfg.PlanSeats = codeSeats(*seats, codeDefaultPlanSeats)

	restore := bindCodePhaseIO(in, errw)
	defer restore()

	res, perr := codework.PlanPhase(cfg)
	if codeRefused(perr, errw) {
		return 2
	}
	if res == nil {
		if perr != nil {
			fmt.Fprintf(errw, "Error: %v\n", perr)
		}
		return 1
	}
	rep := codework.AssembleReport(cfg, []*codework.PhaseResult{res}, nil)
	if c := emitCodeReport(rep, "", cfg.JSONOut, out, errw); c != 0 {
		return c
	}
	if perr != nil {
		echoCodePhaseError(errw, perr)
		return 1
	}
	return 0
}

// runCodeWriteCmd executes a plan: an existing plan file, or a one-unit plan
// composed from --ask + --scope + --writer.
func runCodeWriteCmd(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("code write", flag.ContinueOnError)
	fs.SetOutput(&maskedFlagWriter{w: errw})
	var (
		planFlag   = fs.String("plan", "", "execute an existing partition plan file")
		askFlag    = fs.String("ask", "", "the request text; composed into a one-unit plan")
		askFile    = fs.String("ask-file", "", "path to the request text, or '-' for stdin")
		writerFlag = fs.String("writer", "", "the write seat: provider/model (native seats cannot be scoped)")
		maxRepairs = fs.Int("max-repairs", 1, "repair runs after a failed verification (0 = report and stop)")
		yes        = fs.Bool("yes", false, "consent to the metered seats without asking")
		scopes     stringList
		verifies   stringList
		common     codeCommonFlags
	)
	fs.Var(&scopes, "scope", "workspace path a write unit may touch (repeatable)")
	fs.Var(&verifies, "verify", "a verification command line; repeatable, every one must pass (omit = compile-only go build+go vet detection)")
	addCodeCommonFlags(fs, &common)
	fs.Usage = func() { fmt.Fprintln(errw, proCodeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := codeRefuseExtraArgs(fs, errw); code != 0 {
		return code
	}
	if code := common.check(errw); code >= 0 {
		return code
	}

	cfg := common.config()
	cfg.Yes = *yes
	cfg.Scope = scopes.slice()
	cfg.WriterSeat = strings.TrimSpace(*writerFlag)
	verifyCmds, code := codeVerifyCmds([]string(verifies), errw)
	if code >= 0 {
		return code
	}
	cfg.VerifyCmds = verifyCmds
	if *maxRepairs < 0 {
		fmt.Fprintf(errw, "Error: --max-repairs must be zero or positive (got %d)\n%s\n", *maxRepairs, proCodeUsage)
		return 2
	}
	cfg.MaxRepairs = *maxRepairs
	if cfg.WriterSeat != "" {
		if code := codeCheckWriterSeat(cfg.WriterSeat, errw); code >= 0 {
			return code
		}
	}
	// R3-9: the composed route authors its plan FROM these entries, and the write
	// phase's consent prompt comes later. The layer-1 scope refusals (governance/
	// credential names, non-canonical Win32 components, root-wide entries) and the
	// ledger rule (a scope that could claim the run directory) therefore have to
	// fire here - before BuildPlan, before any prompt - and they carry
	// ErrRefusedPlanScope, so the verb maps them to the usage exit code on this
	// route exactly as the --plan route does inside WritePhase. One function backs
	// both, so the two routes cannot disagree.
	if serr := codework.RefuseScopeEntries(cfg.Root, cfg.OutDir, cfg.Scope, "--scope"); serr != nil {
		codeRefused(serr, errw)
		return 2
	}

	planBytes, code := codeWritePlanBytes(in, errw, *planFlag, *askFlag, *askFile, cfg)
	if code >= 0 {
		return code
	}

	restore := bindCodePhaseIO(in, errw)
	defer restore()

	if !cfg.DryRun {
		echoCodeRunDir(errw, "write", cfg.OutDir)
	}
	writeRes, perr := codework.WritePhase(cfg, planBytes)
	if codeRefused(perr, errw) {
		return 2
	}
	if writeRes == nil {
		if perr != nil {
			fmt.Fprintf(errw, "Error: %v\n", perr)
		}
		return 1
	}
	rep := codework.AssembleReport(cfg, nil, writeRes)
	reportPath := ""
	if !cfg.DryRun && writeRes.Manifest != nil {
		reportPath = filepath.Join(cfg.OutDir, codeReportFileName)
	}
	if c := emitCodeReport(rep, reportPath, cfg.JSONOut, out, errw); c != 0 {
		return c
	}
	// The phase finished (a report exists) and still failed: the reason belongs on
	// the error stream (finding C2). An exit 1 with a report and no sentence is a
	// silent failure an operator cannot act on.
	echoCodePhaseError(errw, perr)
	return codeWriteExit(writeRes, perr)
}

// runCodeReviewCmd reviews the change report a completed write run left in its
// run directory.
func runCodeReviewCmd(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("code review", flag.ContinueOnError)
	fs.SetOutput(&maskedFlagWriter{w: errw})
	var (
		runFlag = fs.String("run", "", "the run directory holding report.txt")
		seats   = fs.String("seats", "", "review seats (default: the documented native line-up)")
		yes     = fs.Bool("yes", false, "consent to the metered seats without asking")
		common  codeCommonFlags
	)
	addCodeCommonFlags(fs, &common)
	fs.Usage = func() { fmt.Fprintln(errw, proCodeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := codeRefuseExtraArgs(fs, errw); code != 0 {
		return code
	}
	if code := common.check(errw); code >= 0 {
		return code
	}
	runDir := strings.TrimSpace(*runFlag)
	if runDir == "" {
		fmt.Fprintf(errw, "Error: missing --run DIR (the run directory a `agy pro code write` left behind)\n%s\n", proCodeUsage)
		return 2
	}
	// --run is the single source of truth (finding C5): a divergent --out is a
	// usage error instead of a silent override of what gets reviewed.
	if code := codeRunDirRefusal("review", runDir, common.out, errw); code >= 0 {
		return code
	}

	cfg := common.config()
	cfg.OutDir = runDir
	cfg.Yes = *yes
	cfg.ReviewSeats = codeSeats(*seats, codeDefaultReviewSeats)

	// The diff artifact gate: a review of nothing would spend a metered run on an
	// empty report, so a run directory with no write behind it is refused here
	// with the command that produces one.
	reportPath := filepath.Join(runDir, codeReportFileName)
	if info, err := os.Stat(reportPath); err != nil || info.IsDir() {
		fmt.Fprintf(errw, "Error: no change report in %s (want %s); run `agy pro code write` (or `agy pro code run`) first\n",
			providers.MaskOperand(filepath.ToSlash(runDir)), codeReportFileName)
		return 2
	}

	restore := bindCodePhaseIO(in, errw)
	defer restore()

	if !cfg.DryRun {
		echoCodeRunDir(errw, "review", cfg.OutDir)
	}
	res, perr := codework.ReviewPhase(cfg, reportPath)
	if codeRefused(perr, errw) {
		return 2
	}
	if res == nil {
		if perr != nil {
			fmt.Fprintf(errw, "Error: %v\n", perr)
		}
		return 1
	}
	rep := codework.AssembleReport(cfg, []*codework.PhaseResult{res}, nil)
	if c := emitCodeReport(rep, "", cfg.JSONOut, out, errw); c != 0 {
		return c
	}
	if perr != nil {
		echoCodePhaseError(errw, perr)
		return 1
	}
	return 0
}

// runCodeRollbackCmd restores the tree from a run's manifest. It never asks for
// consent: rollback is the undo button, and codesnap's own rollback is
// idempotent, so a second call is harmless (phases.go RollbackPhase).
func runCodeRollbackCmd(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("code rollback", flag.ContinueOnError)
	fs.SetOutput(&maskedFlagWriter{w: errw})
	var (
		runFlag = fs.String("run", "", "the run directory holding manifest.json and snapshot/")
		common  codeCommonFlags
	)
	// --yes is accepted and ignored: the flag exists so one scripted invocation
	// shape covers every subcommand, and the help says so. A rollback is
	// restorative, so it never gates (phases.go RollbackPhase).
	fs.Bool("yes", false, "accepted for uniformity; rollback never asks")
	addCodeCommonFlags(fs, &common)
	fs.Usage = func() { fmt.Fprintln(errw, proCodeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := codeRefuseExtraArgs(fs, errw); code != 0 {
		return code
	}
	if code := common.check(errw); code >= 0 {
		return code
	}
	runDir := strings.TrimSpace(*runFlag)
	if runDir == "" {
		fmt.Fprintf(errw, "Error: missing --run DIR (the run directory holding the snapshot manifest)\n%s\n", proCodeUsage)
		return 2
	}
	// --run is the single source of truth (finding C5): a divergent --out would
	// restore from another run's manifest, so it is refused instead.
	if code := codeRunDirRefusal("rollback", runDir, common.out, errw); code >= 0 {
		return code
	}

	cfg := common.config()
	cfg.OutDir = runDir

	restore := bindCodePhaseIO(in, errw)
	defer restore()

	res, perr := codework.RollbackPhase(cfg)
	if res == nil {
		if perr != nil {
			fmt.Fprintf(errw, "Error: %v\n", perr)
		}
		return 1
	}
	rep := codework.AssembleReport(cfg, []*codework.PhaseResult{res}, nil)
	if cfg.DryRun {
		// The library already printed the dry-run line through PhaseStdout
		// (bound to errw); the report adds the artifact/status block.
		if c := emitCodeReport(rep, "", cfg.JSONOut, out, errw); c != 0 {
			return c
		}
		return 0
	}
	if perr != nil {
		fmt.Fprintf(errw, "Error: %v\n", perr)
		return 1
	}
	printCodeRollback(errw, res.Rollback)
	if c := emitCodeReport(rep, "", cfg.JSONOut, out, errw); c != 0 {
		return c
	}
	return 0
}

// runCodeRunCmd is the composed door: plan -> write -> review in one run
// directory, with one CodeRunReport at the end.
func runCodeRunCmd(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("code run", flag.ContinueOnError)
	fs.SetOutput(&maskedFlagWriter{w: errw})
	var (
		askFlag    = fs.String("ask", "", "the request text (the plan prompt, the write composition)")
		askFile    = fs.String("ask-file", "", "path to the request text, or '-' for stdin")
		planFlag   = fs.String("plan", "", "execute an existing partition plan file (implies --skip-plan)")
		writerFlag = fs.String("writer", "", "the write seat: provider/model (native seats cannot be scoped)")
		seatsFlag  = fs.String("seats", "", "plan and review seats (default: the documented native line-up)")
		maxRepairs = fs.Int("max-repairs", 1, "repair runs after a failed verification (0 = report and stop)")
		skipPlan   = fs.Bool("skip-plan", false, "skip the plan-audit phase")
		yes        = fs.Bool("yes", false, "consent to the metered seats without asking")
		scopes     stringList
		verifies   stringList
		common     codeCommonFlags
	)
	fs.Var(&scopes, "scope", "workspace path a write unit may touch (repeatable)")
	fs.Var(&verifies, "verify", "a verification command line; repeatable, every one must pass (omit = compile-only go build+go vet detection)")
	addCodeCommonFlags(fs, &common)
	fs.Usage = func() { fmt.Fprintln(errw, proCodeUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if code := codeRefuseExtraArgs(fs, errw); code != 0 {
		return code
	}
	if code := common.check(errw); code >= 0 {
		return code
	}

	cfg := common.config()
	cfg.Yes = *yes
	cfg.Scope = scopes.slice()
	cfg.WriterSeat = strings.TrimSpace(*writerFlag)
	verifyCmds, code := codeVerifyCmds([]string(verifies), errw)
	if code >= 0 {
		return code
	}
	cfg.VerifyCmds = verifyCmds
	if *maxRepairs < 0 {
		fmt.Fprintf(errw, "Error: --max-repairs must be zero or positive (got %d)\n%s\n", *maxRepairs, proCodeUsage)
		return 2
	}
	cfg.MaxRepairs = *maxRepairs
	if cfg.WriterSeat != "" {
		if code := codeCheckWriterSeat(cfg.WriterSeat, errw); code >= 0 {
			return code
		}
	}
	// One --seats value covers both read-only phases: the operator picked a
	// cross-verification line-up, and splitting it per phase would need a second
	// flag nobody asked for. It is also the run-wide pool, so the report's cost
	// seeds name every seat this run used.
	seats := codeSeats(*seatsFlag, codeDefaultPlanSeats)
	cfg.PlanSeats = seats
	cfg.ReviewSeats = codeSeats(*seatsFlag, codeDefaultReviewSeats)
	cfg.Seats = seats

	planPath := strings.TrimSpace(*planFlag)
	hasAskFlag := strings.TrimSpace(*askFlag) != "" || strings.TrimSpace(*askFile) != ""
	if planPath != "" && hasAskFlag {
		fmt.Fprintf(errw, "Error: pass --plan or --ask/--ask-file, not both (--plan already carries the units; --ask would compose a different one)\n%s\n", proCodeUsage)
		return 2
	}
	// R3-9, composed route: same pre-consent scope gate as `code write`. It runs
	// BEFORE the plan phase, because that phase may already snapshot the scope.
	if serr := codework.RefuseScopeEntries(cfg.Root, cfg.OutDir, cfg.Scope, "--scope"); serr != nil {
		codeRefused(serr, errw)
		return 2
	}

	restore := bindCodePhaseIO(in, errw)
	defer restore()

	if !cfg.DryRun {
		echoCodeRunDir(errw, "run", cfg.OutDir)
	}

	var phases []*codework.PhaseResult
	var planRes *codework.PhaseResult
	var planBytes []byte
	if planPath != "" {
		planBytes, code = codeReadPlan(planPath, errw)
		if code >= 0 {
			return code
		}
	}
	if planBytes == nil && !*skipPlan {
		ask, code := codeResolveAsk(in, *askFlag, *askFile, errw)
		if code >= 0 {
			return code
		}
		cfg.Ask = ask
		// The snapshot precedes the plan seats when a write can follow: a native
		// seat holds its own tool surface, so anything it edits before the write
		// phase's snapshot would become the rollback baseline (U9). No scope means
		// the documented plan-only stop, so nothing is baselined.
		cfg.SnapshotFirst = len(cfg.Scope) > 0
		res, perr := codework.PlanPhase(cfg)
		if codeRefused(perr, errw) {
			return 2
		}
		if res == nil {
			if perr != nil {
				fmt.Fprintf(errw, "Error: %v\n", perr)
			}
			return 1
		}
		planRes = res
		phases = append(phases, res)
		if perr != nil {
			rep := codework.AssembleReport(cfg, phases, nil)
			if c := emitCodeReport(rep, "", cfg.JSONOut, out, errw); c != 0 {
				return c
			}
			echoCodePhaseError(errw, perr)
			return 1
		}
	}

	if planBytes == nil {
		if len(cfg.Scope) == 0 {
			if planRes == nil {
				// --skip-plan (or a plan-less invocation) with no scope leaves
				// nothing to derive a scope from, so the run is a usage error
				// rather than a silent stop.
				fmt.Fprintf(errw, "Error: missing --scope (a write unit must name at least one bounded workspace path; --skip-plan leaves nothing to derive one from)\n%s\n", proCodeUsage)
				return 2
			}
			// The documented plan-only stop: with no scope there is no bounded
			// write to author, and inventing one is exactly what the plan phase
			// exists to prevent.
			rep := codework.AssembleReport(cfg, phases, nil)
			if c := emitCodeReport(rep, "", cfg.JSONOut, out, errw); c != 0 {
				return c
			}
			printCodeScopeInstruction(errw, cfg, planRes)
			return 0
		}
		if cfg.WriterSeat == "" {
			fmt.Fprintf(errw, "Error: missing --writer (a write unit is worked by exactly one provider seat; the run has no default)\n%s\n", proCodeUsage)
			return 2
		}
		if strings.TrimSpace(cfg.Ask) == "" {
			fmt.Fprintf(errw, "Error: missing --ask/--ask-file (the unit body is composed from the request text)\n%s\n", proCodeUsage)
			return 2
		}
		composed, berr := codework.BuildPlan(cfg.Ask, []codework.UnitSpec{{Scope: cfg.Scope, Seat: cfg.WriterSeat}})
		if berr != nil {
			// Same mapping as codeWritePlanBytes: the entries were gated before the
			// plan phase, so a scope-class refusal here is BuildPlan's own rule and
			// still a usage-class error (the invocation has to change).
			if codeRefused(berr, errw) {
				return 2
			}
			fmt.Fprintf(errw, "Error: %v\n", berr)
			return 1
		}
		planBytes = composed
	}

	if !cfg.DryRun && strings.TrimSpace(cfg.Ask) == "" {
		// A --plan run never resolved an ask; the report and the repair context
		// read it, so an empty one is named, not invented.
		echoCodeNoAsk(errw)
	}
	// The plan phase already snapshotted (SnapshotFirst) when it ran; hand that
	// baseline to the write phase so it does NOT re-take one. A re-take here would
	// record whatever a native plan seat changed as "original".
	if planRes != nil && planRes.Manifest != nil {
		cfg.Baseline = planRes.Manifest
	}
	writeRes, werr := codework.WritePhase(cfg, planBytes)
	if codeRefused(werr, errw) {
		return 2
	}
	if writeRes == nil {
		if werr != nil {
			fmt.Fprintf(errw, "Error: %v\n", werr)
		}
		return 1
	}
	reportPath := filepath.Join(cfg.OutDir, codeReportFileName)
	interim := codework.AssembleReport(cfg, phases, writeRes)
	if !cfg.DryRun && writeRes.Manifest != nil {
		// The change report a review reads. It is written BEFORE the review so
		// the seats get the file, and rewritten after it with the final report
		// (a superset: the review phase's own line joins the block). The exact
		// bytes the seats saw stay in <out>/review/prompt.txt.
		if err := writeCodeReportFile(reportPath, interim.Render()+"\n"); err != nil {
			fmt.Fprintf(errw, "Error: %v\n", err)
			return 1
		}
	}
	if exit := codeWriteExit(writeRes, werr); exit != 0 {
		// A failed write or verification stops here: reviewing a change the
		// verifier rejected would spend a second metered run on it.
		if c := emitCodeReport(interim, "", cfg.JSONOut, out, errw); c != 0 {
			return c
		}
		echoCodePhaseError(errw, werr)
		return exit
	}

	reviewInput := ""
	if !cfg.DryRun {
		reviewInput = reportPath
	}
	reviewRes, rerr := codework.ReviewPhase(cfg, reviewInput)
	if codeRefused(rerr, errw) {
		if c := emitCodeReport(interim, "", cfg.JSONOut, out, errw); c != 0 {
			return c
		}
		return 2
	}
	if reviewRes == nil {
		if rerr != nil {
			fmt.Fprintf(errw, "Error: %v\n", rerr)
		}
		return 1
	}
	phases = append(phases, reviewRes)
	final := codework.AssembleReport(cfg, phases, writeRes)
	persist := ""
	if !cfg.DryRun && writeRes.Manifest != nil {
		persist = reportPath
	}
	if c := emitCodeReport(final, persist, cfg.JSONOut, out, errw); c != 0 {
		return c
	}
	if rerr != nil {
		echoCodePhaseError(errw, rerr)
		return 1
	}
	return 0
}

// bindCodePhaseIO binds the codework phase streams for the duration of one
// subcommand and returns the restore func.
//
// PhaseStdout is bound to the ERROR writer, never to stdout: the phase vectors
// pass `--json`, so the engine prints its own summary document to that stream
// (U4 report §6 P2-3). Sending it to stderr keeps stdout as exactly one
// document (the human block or the report JSON). PhaseStderr shares it, and
// PhaseStdin is the caller's reader so the consent gate reads the terminal the
// door was handed.
func bindCodePhaseIO(in io.Reader, errw io.Writer) func() {
	prevIn, prevOut, prevErr := codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr
	codework.PhaseStdin = in
	codework.PhaseStdout = errw
	codework.PhaseStderr = errw
	return func() {
		codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr = prevIn, prevOut, prevErr
	}
}

// emitCodeReport prints the report and, when path is non-empty, persists the
// human block as the run's change report. Returns 0 on success, else the exit
// code.
func emitCodeReport(rep *codework.CodeRunReport, path string, asJSON bool, out, errw io.Writer) int {
	if rep == nil {
		fmt.Fprintln(errw, "Error: no run report to print")
		return 1
	}
	if path != "" {
		if err := writeCodeReportFile(path, rep.Render()+"\n"); err != nil {
			fmt.Fprintf(errw, "Error: %v\n", err)
			return 1
		}
	}
	if asJSON {
		data, err := rep.JSON()
		if err != nil {
			fmt.Fprintf(errw, "Error: %v\n", err)
			return 1
		}
		if _, err := out.Write(data); err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprintln(out, rep.Render())
	return 0
}

// writeCodeReportFile persists the change report atomically.
func writeCodeReportFile(path, body string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, codeDirPerm); err != nil {
			return fmt.Errorf("cannot create %q: %w", providers.MaskOperand(filepath.ToSlash(dir)), err)
		}
	}
	if err := providers.WriteFileAtomic(path, []byte(body), codeFilePerm); err != nil {
		return fmt.Errorf("cannot write %q: %w", providers.MaskOperand(filepath.ToSlash(path)), err)
	}
	return nil
}

// echoCodeRunDir names the run directory before a metered phase starts, so the
// operator can tail the engine's progress.log while the seats work (U4 handoff
// item 8).
func echoCodeRunDir(errw io.Writer, sub, outDir string) {
	fmt.Fprintf(errw, "agy pro code %s: run dir %s (progress: write/progress.log, trail: phases.log)\n",
		sub, providers.MaskOperand(filepath.ToSlash(outDir)))
}

// echoCodeNoAsk notes that a --plan run carries no request text, so the report
// and the repair prompts have no run-level ask. It is a notice, not a failure.
func echoCodeNoAsk(errw io.Writer) {
	fmt.Fprintln(errw, "note: no --ask given, so the run has no request text (the plan file is the whole input)")
}

// printCodeScopeInstruction is the plan-only stop: what the operator does next.
func printCodeScopeInstruction(errw io.Writer, cfg codework.CodeConfig, plan *codework.PhaseResult) {
	fmt.Fprintln(errw, "next: this run stopped after the plan phase because no --scope was given.")
	if plan != nil && plan.ReconcilePath != "" {
		fmt.Fprintf(errw, "  read: %s\n", providers.MaskOperand(filepath.ToSlash(plan.ReconcilePath)))
	}
	fmt.Fprintf(errw, "  then: agy pro code run --ask \"...\" --scope <path> [--scope <path>...] --writer <provider/model> --out %s\n",
		providers.MaskOperand(filepath.ToSlash(cfg.OutDir)))
	fmt.Fprintln(errw, "  (each --scope entry is one bounded path a write unit may touch; native seats cannot be write units)")
}

// printCodeRollback renders what a rollback restored, archived and skipped. The
// full lists are in <out>/rollback.json; this block is bounded so a large scope
// cannot flood the console.
func printCodeRollback(errw io.Writer, rep *codesnap.RollbackReport) {
	if rep == nil {
		fmt.Fprintln(errw, "rollback: no report (nothing was restored)")
		return
	}
	printCodePathList(errw, "restored", rep.Restored)
	printCodePathList(errw, "archived", rep.Archived)
	printCodePathList(errw, "unchanged", rep.Unchanged)
	skipped := make([]string, 0, len(rep.Skipped))
	for _, row := range rep.Skipped {
		skipped = append(skipped, row.Path+" ("+row.Reason+")")
	}
	printCodePathList(errw, "skipped", skipped)
	fmt.Fprintf(errw, "rollback: %d restored, %d archived, %d unchanged, %d skipped\n",
		len(rep.Restored), len(rep.Archived), len(rep.Unchanged), len(rep.Skipped))
}

// printCodePathList prints one bounded list: a header with the count, then at
// most codeListRows masked entries and an explicit "... K more".
func printCodePathList(w io.Writer, label string, rows []string) {
	fmt.Fprintf(w, "%s: %d\n", label, len(rows))
	for i, row := range rows {
		if i == codeListRows {
			fmt.Fprintf(w, "  ... %d more\n", len(rows)-codeListRows)
			break
		}
		fmt.Fprintf(w, "  %s %s\n", strings.ToUpper(label[:1]), providers.MaskOperand(filepath.ToSlash(row)))
	}
}

// codeSeats resolves a --seats value against the documented default line-up.
func codeSeats(flagVal, def string) []string {
	v := strings.TrimSpace(flagVal)
	if v == "" {
		v = def
	}
	return []string{v}
}

// codeOutDir resolves the run directory for the subcommands that read another
// run: an explicit --out is accepted ONLY when it names the same directory as
// --run, and the --run directory is what every input artifact is loaded from.
//
// Before this rule, --out silently overrode --run, so `review --run A --out B`
// read B's report (or nothing) while the operator believed A was being reviewed -
// and `rollback --run A --out B` would restore from B's manifest. One flag is the
// source of truth (finding C5); the divergent pair is a usage error that names
// both, because the invocation has to change.
func codeOutDir(outFlag, runDir string) (dir string, diverge bool) {
	out := strings.TrimSpace(outFlag)
	return out, out != "" && !codeSamePath(out, runDir)
}

// codeRunDirRefusal refuses an --out that names a directory other than --run.
// -1 means "keep going"; any other value is the process exit code. --run itself
// is required by the caller, whose message names the artifact it reads.
func codeRunDirRefusal(sub, runDir, outFlag string, errw io.Writer) int {
	out, diverge := codeOutDir(outFlag, runDir)
	if !diverge {
		return -1
	}
	fmt.Fprintf(errw, "Error: --out %s and --run %s name different directories; %s reads and writes the --run directory only (its change report / manifest + snapshot), so a divergent --out is refused\n%s\n",
		providers.MaskOperand(filepath.ToSlash(out)), providers.MaskOperand(filepath.ToSlash(runDir)), sub, proCodeUsage)
	return 2
}

// codeSamePath reports whether two paths denote the same directory (absolute,
// clean, case-insensitive on Windows - the same rule codework.samePath applies,
// restated here because that helper is unexported and internal/codework must not
// be widened for a flag check).
func codeSamePath(a, b string) bool {
	absA, errA := filepath.Abs(filepath.Clean(strings.TrimSpace(a)))
	absB, errB := filepath.Abs(filepath.Clean(strings.TrimSpace(b)))
	if errA != nil || errB != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(absA, absB)
	}
	return absA == absB
}

// codeResolveAsk resolves the request text from --ask or --ask-file ('-' reads
// stdin, the pro_swarm.go:421 readPromptSource helper). The text itself is never
// echoed: it is the operator's own model input.
func codeResolveAsk(in io.Reader, ask, askFile string, errw io.Writer) (string, int) {
	inline := strings.TrimSpace(ask)
	file := strings.TrimSpace(askFile)
	switch {
	case inline != "" && file != "":
		fmt.Fprintf(errw, "Error: pass one of --ask or --ask-file, not both\n%s\n", proCodeUsage)
		return "", 2
	case inline == "" && file == "":
		fmt.Fprintf(errw, "Error: missing --ask TEXT or --ask-file PATH (the run needs the request text)\n%s\n", proCodeUsage)
		return "", 2
	}
	if file == "" {
		return ask, -1
	}
	data, err := readPromptSource(in, file)
	if err != nil {
		fmt.Fprintf(errw, "Error: cannot read --ask-file %s: %s\n",
			providers.MaskOperand(file), maskOperandSentence(err.Error(), providers.FlagSeedOperands(file)...))
		return "", 2
	}
	if strings.TrimSpace(string(data)) == "" {
		fmt.Fprintf(errw, "Error: --ask-file %s is empty\n", providers.MaskOperand(file))
		return "", 2
	}
	return string(data), -1
}

// codeWritePlanBytes resolves the write phase's input: an existing plan file, or
// a one-unit plan composed from the request text, the scope and the writer.
func codeWritePlanBytes(in io.Reader, errw io.Writer, planPath, ask, askFile string, cfg codework.CodeConfig) ([]byte, int) {
	path := strings.TrimSpace(planPath)
	hasAsk := strings.TrimSpace(ask) != "" || strings.TrimSpace(askFile) != ""
	if path != "" {
		if hasAsk {
			fmt.Fprintf(errw, "Error: pass --plan or --ask/--ask-file, not both (--plan already carries the units; --ask would compose a different one)\n%s\n", proCodeUsage)
			return nil, 2
		}
		return codeReadPlan(path, errw)
	}
	request, code := codeResolveAsk(in, ask, askFile, errw)
	if code >= 0 {
		return nil, code
	}
	if len(cfg.Scope) == 0 {
		fmt.Fprintf(errw, "Error: missing --scope (a write unit must name at least one bounded workspace path)\n%s\n", proCodeUsage)
		return nil, 2
	}
	if cfg.WriterSeat == "" {
		fmt.Fprintf(errw, "Error: missing --writer (a write unit is worked by exactly one provider seat; there is no default)\n%s\n", proCodeUsage)
		return nil, 2
	}
	composed, berr := codework.BuildPlan(request, []codework.UnitSpec{{Scope: cfg.Scope, Seat: cfg.WriterSeat}})
	if berr != nil {
		// The scope entries were already put through codework.RefuseScopeEntries by
		// the caller, so a scope-class refusal can only arrive here if BuildPlan
		// grows a rule of its own; mapping it keeps the exit code honest (usage, not
		// run failure) either way.
		if codeRefused(berr, errw) {
			return nil, 2
		}
		fmt.Fprintf(errw, "Error: %v\n", berr)
		return nil, 1
	}
	return composed, -1
}

// codeReadPlan reads an existing partition plan file.
func codeReadPlan(path string, errw io.Writer) ([]byte, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(errw, "Error: cannot read --plan %s: %s\n",
			providers.MaskOperand(path), maskOperandSentence(err.Error(), providers.FlagSeedOperands(path)...))
		return nil, 2
	}
	if len(bytes.TrimSpace(data)) == 0 {
		fmt.Fprintf(errw, "Error: --plan %s is empty\n", providers.MaskOperand(path))
		return nil, 2
	}
	return data, -1
}

// codeCheckWriterSeat refuses a --writer that is not exactly one provider seat
// at USAGE time (the library refuses it too, but only after the phase starts, so
// that refusal would read as exit 1; a native writer is an invocation the
// operator can fix).
func codeCheckWriterSeat(seat string, errw io.Writer) int {
	specs, err := fanout.ParseSeatSpecs(seat)
	if err != nil {
		fmt.Fprintf(errw, "Error: --writer %s: %s\n", providers.MaskOperand(seat), maskOperandSentence(err.Error(), providers.FlagSeedOperands(seat)...))
		return 2
	}
	if len(specs) != 1 || specs[0].Kind != fanout.SeatKindProvider {
		fmt.Fprintf(errw, "Error: --writer %s must be exactly one provider seat: a native seat builds no scoped ToolRunner, so it cannot be bounded to a unit scope (internal/fanout/engine.go:883-889)\n",
			providers.MaskOperand(seat))
		return 2
	}
	return -1
}

// codeVerifyCmds resolves the repeatable --verify flags into the argv list the
// engine runs, in flag order. codework.CodeConfig.VerifyCmds is a list of
// command lines, and the write phase runs ALL of them: every verifier must pass,
// and the first failure starts the repair flow (phases.go runVerifyPlan).
//
// Parsing is fail-closed: an operand that does not split into an argv (an
// unbalanced quote), or a BLANK one (`--verify ""`, which used to be skipped in
// silence while docs/code.md promised a usage error), is exit 2 before anything
// runs - a blank verifier is an invocation mistake, not "no verifier". Dropping
// the flag entirely is how an operator asks for detection. The echoed operand is
// masked.
func codeVerifyCmds(values []string, errw io.Writer) ([][]string, int) {
	cmds := make([][]string, 0, len(values))
	for _, raw := range values {
		line := strings.TrimSpace(raw)
		if line == "" {
			fmt.Fprintf(errw, "Error: --verify with an empty command line (drop the flag to let the engine detect a verifier, or name the command to run)\n%s\n", proCodeUsage)
			return nil, 2
		}
		argv, err := splitCodeArgv(line)
		if err != nil {
			fmt.Fprintf(errw, "Error: --verify %s: %s\n",
				providers.MaskOperand(line), maskOperandSentence(err.Error(), providers.FlagSeedOperands(line)...))
			return nil, 2
		}
		cmds = append(cmds, argv)
	}
	if len(cmds) == 0 {
		return nil, -1
	}
	return cmds, -1
}

// splitCodeArgv splits a command line into argv: whitespace separates tokens and
// a double-quoted run groups spaces into one token (no escape sequences). The
// body is restated from internal/dashboard/oneshot.go:254 splitArgv, which is
// unexported and lives in a package the door has no other reason to import.
func splitCodeArgv(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
		open    bool
	)
	flush := func() {
		if open {
			out = append(out, cur.String())
			cur.Reset()
			open = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			open = true
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	if inQuote {
		return nil, errors.New("unbalanced quote")
	}
	flush()
	if len(out) == 0 {
		return nil, errors.New("empty command line")
	}
	return out, nil
}

// codeRefuseExtraArgs refuses a stray positional operand. Returns 0 when there
// is none, else 2 (the caller returns the same code).
func codeRefuseExtraArgs(fs *flag.FlagSet, errw io.Writer) int {
	if fs.NArg() == 0 {
		return 0
	}
	fmt.Fprintf(errw, "Error: unexpected argument %q (agy pro code takes flags only)\n%s\n", providers.MaskOperand(fs.Arg(0)), proCodeUsage)
	return 2
}

// codeRefused reports whether the error is one of the library's USAGE-class
// refusals - a refused consent, or a refused plan/scope - and prints the sentence
// the phase already ended with. Nothing ran in either case, so the caller returns
// the usage exit code (2): the invocation has to change.
//
// The consent hint names --yes on BOTH refusal shapes: the library's non-TTY
// branch already says it, but a decline at a prompt (including the Windows NUL
// device, which stdinIsTTY's ModeCharDevice test reads as a terminal) does not,
// and the operator needs the same next step either way. A refused plan/scope
// takes no hint: the error already names the offending entry and says nothing ran.
func codeRefused(err error, errw io.Writer) bool {
	switch {
	case errors.Is(err, codework.ErrConsentRequired):
		fmt.Fprintf(errw, "Error: %v\n", err)
		fmt.Fprintln(errw, "hint: pass --yes to confirm the metered run without a terminal prompt (nothing was run)")
		return true
	case errors.Is(err, codework.ErrRefusedPlanScope):
		// Exit 2, not 1: the plan or the --scope names something the door refuses
		// on purpose (a governance/credential path, a root-wide entry), and nothing
		// started - no snapshot, no plan file, no seat. That is a usage error.
		fmt.Fprintf(errw, "Error: %v\n", err)
		return true
	}
	return false
}

// echoCodePhaseError prints a finished phase's error reason on the error stream.
// A phase that produced a report and still failed must say WHY (finding C2): the
// exit code alone leaves the operator guessing between a refused write, a failed
// verification and a post-write failure.
func echoCodePhaseError(errw io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(errw, "Error: %v\n", err)
}

// codeWriteExit maps a write phase outcome to the exit code: 0 ok (including
// "unverified", which is a status, not a failure, and `degraded`, which only ever
// qualifies a read-only phase), 1 for a failed write or an ultimately failed
// verification (the report names the rollback command), 2 for a usage-class
// refusal that reached the mapping.
func codeWriteExit(res *codework.WritePhaseResult, err error) int {
	switch {
	case errors.Is(err, codework.ErrConsentRequired), errors.Is(err, codework.ErrRefusedPlanScope):
		return 2
	case err != nil:
		return 1
	case res == nil:
		return 1
	}
	switch res.Status {
	case codework.StatusVerifyFailed, codework.StatusWriteFailed, codework.StatusFailed:
		return 1
	default:
		return 0
	}
}

// stringList is a repeatable string flag: each occurrence appends one value
// (used for --scope and --verify).
type stringList []string

func (s *stringList) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

// Set appends one occurrence.
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// slice returns the entries with surrounding whitespace trimmed and empty
// entries dropped, preserving order.
func (s stringList) slice() []string {
	out := make([]string, 0, len(s))
	for _, raw := range s {
		if v := strings.TrimSpace(raw); v != "" {
			out = append(out, v)
		}
	}
	return out
}
