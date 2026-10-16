package fanout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// RunConfig holds options for a fanout swarm execution.
type RunConfig struct {
	Task         string
	Prompt       string
	N            int
	Models       []string
	Root         string
	Tools        string
	MaxTurns     int
	TimeoutSec   int
	OutDir       string
	RegistryPath string
	Client       *Client // optional injected client (for testing)

	// ProgressOut receives one line per seat start/finish as events happen
	// (nil = discard). Terminal runs wire stdout so the operator watches seats
	// stream instead of a 15-minute silence; --json mode wires stderr so the
	// stdout summary stays parseable. The same lines are always appended to
	// progress.log in the artifact directory, sink or no sink.
	ProgressOut io.Writer

	// Seats is the parsed --seats spec (contract §2.1). nil = legacy path
	// (SelectTargets over Models/N). When set, targets are built directly
	// from the seat specs in order, mixing native (real-agy subprocess) and
	// provider (HTTP) seats in one swarm.
	Seats []SeatSpec
	// MaxNativeConcurrency caps simultaneous native seats; <=0 falls back to
	// mode.DefaultNativeSeatConcurrency (contract §2.3). Resolution
	// (flag > state.json > default) happens in cli.go.
	MaxNativeConcurrency int
	// NativeResolver resolves the real-agy binary; nil -> realagy.Resolve
	// (test seam, contract §2.1).
	NativeResolver func() (string, error)
	// SpawnCommand builds the native-seat subprocess; nil ->
	// exec.CommandContext (test seam, contract §2.1).
	SpawnCommand func(ctx context.Context, bin string, args ...string) *exec.Cmd
	// ExcludeModel is the F-C pool-policy exclusion seam consulted by
	// SelectTargets rotation AND worker failover candidate filtering; it is
	// NEVER applied to explicit user picks (--models / --seats provider
	// tokens are honored as-is). nil -> providers.IsK3Family (contract §2.5b).
	ExcludeModel func(providers.Model) bool
}

// DefaultTimeoutSec is the swarm wall-clock budget when neither the --timeout
// flag nor RunConfig.TimeoutSec sets one. It is the single source of truth the
// settings panel surfaces as "Fanout Wall Timeout (--timeout)".
//
// 300s, not the historical 120s: a real seat cannot finish in 120s. The
// provider seat loop (worker.go runChatLoop) runs up to 10 sequential chat
// turns with tool calls and reasoning models take tens of seconds per call.
// Live evidence (2026-10-14): both provider seats in reports/fanout/
// 20261014-042933 degraded with "all failover attempts exhausted; last error:
// context deadline exceeded", and a fixed native seat needed 305s to complete
// a real review.
const DefaultTimeoutSec = 300

// withDefaults returns cfg with the engine defaults applied, so a RunConfig
// built outside the CLI (library caller, test, swarm delegation) resolves to
// the same surface the flag defaults produce.
func (cfg RunConfig) withDefaults() RunConfig {
	if cfg.Task == "" {
		cfg.Task = "review"
	}
	if cfg.N <= 0 {
		cfg.N = 3
	}
	if cfg.Root == "" {
		cfg.Root = "."
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = DefaultTimeoutSec
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 10
	}
	return cfg
}

// Execute orchestrates the full double-blind multi-model swarm run.
func Execute(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
	// 1. Defaults & validation
	cfg = cfg.withDefaults()

	ts := time.Now().UTC().Format("20060102-150405")
	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join("reports", "fanout", ts)
	}

	// 2. Load Registry
	var reg *providers.Registry
	var err error
	if cfg.RegistryPath != "" {
		reg, err = providers.LoadFrom(cfg.RegistryPath)
	} else {
		reg, err = providers.Load()
	}
	if err != nil {
		// F-REG (contract §2.1): a registry-less machine must still run an
		// all-native --seats swarm. Tolerate a missing-file load error ONLY
		// when every seat is native; any provider seat or any other load
		// error keeps the hard failure.
		if len(cfg.Seats) > 0 && allNative(cfg.Seats) && errors.Is(err, os.ErrNotExist) {
			reg = &providers.Registry{}
		} else {
			return nil, fmt.Errorf("failed to load providers: %w", err)
		}
	}

	// 3. Load rotation state
	state, _ := LoadState()

	// 4. Select Targets (Double-Blind candidate assignment) OR build the
	// explicit seat list (mixed native+provider swarm, contract §2.1/§2.4).
	exclude := cfg.ExcludeModel
	if exclude == nil {
		exclude = providers.IsK3Family
	}

	var targets []providers.Target
	var nativeSeats []bool // parallel to targets: true = native subprocess seat
	if len(cfg.Seats) > 0 {
		targets = make([]providers.Target, 0, len(cfg.Seats))
		nativeSeats = make([]bool, 0, len(cfg.Seats))
		for _, spec := range cfg.Seats {
			if spec.Kind == SeatKindNative {
				targets = append(targets, nativeTarget(spec.ModelID))
				nativeSeats = append(nativeSeats, true)
				continue
			}
			tgt, terr := reg.FindTarget(spec.ModelID)
			if terr != nil {
				return nil, fmt.Errorf("failed to resolve model %q: %w", providers.MaskOperand(spec.ModelID), terr)
			}
			targets = append(targets, tgt)
			nativeSeats = append(nativeSeats, false)
		}
	} else {
		targets, err = SelectTargets(reg, state, cfg.N, cfg.Models, exclude)
		if err != nil {
			return nil, fmt.Errorf("target selection failed: %w", err)
		}
		nativeSeats = make([]bool, len(targets)) // all false: legacy provider path
	}

	// 5. Initialize Tool Environment & Permissions
	toolsSpec := strings.ToLower(cfg.Tools)
	allowWrite := strings.Contains(toolsSpec, "write")
	allowExec := strings.Contains(toolsSpec, "exec")

	home, _ := providers.Home()
	logPath := filepath.Join(home, "logs", fmt.Sprintf("fanout-%s.jsonl", ts))
	logger, _ := agenttools.NewLogger(logPath, reg.Redactor())

	openAITools := agenttools.BuildOpenAITools(allowWrite, allowExec)

	// Collect assigned families to inform failover exclusions
	var assignedFamilies []string
	for _, t := range targets {
		assignedFamilies = append(assignedFamilies, t.Model.FamilyName())
	}

	// 6. Spawn Double-Blind Concurrent Workers
	client := newSwarmClient(cfg)

	ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()

	// Native-seat machinery (contract §2.2/§2.3): one shared runner, a
	// FIFO buffered-channel semaphore bounding concurrent native subprocesses,
	// and the shared seat prompt (seat-role preamble + system preset + user
	// text, see buildNativeSeatPrompt). Provider seats keep today's unbounded
	// parallel goroutines.
	//
	// A1: the cap goes through mode.ClampNativeSeatConcurrency, the same
	// choke point the state.json and --max-native paths use, so a config built
	// outside the CLI (library caller, test, swarm delegation) can never widen
	// the native-seat pool past 8 either.
	nativeCap := mode.ClampNativeSeatConcurrency(cfg.MaxNativeConcurrency)
	nativeSem := make(chan struct{}, nativeCap)
	nativeRunner := &NativeRunner{
		Resolver:   cfg.NativeResolver,
		Spawn:      cfg.SpawnCommand,
		ViaStdin:   false, // kickoff verdict: multi-line argv verified (F-P)
		scrubNames: realagy.ApiKeyEnvNames(reg),
	}
	nativePrompt := buildNativeSeatPrompt(cfg.Task, cfg.Prompt)
	swarmDeadline, _ := ctxWithTimeout.Deadline()
	redact := reg.Redactor()

	// Live seat progress: created after every hard-failure path (registry
	// load, target selection) and before the first seat starts, so the
	// artifact directory and progress.log inside it exist from run start and
	// an agent can tail a background run immediately - while an instant
	// failure still leaves no reports/ behind (launcher pin). A nil
	// ProgressOut keeps the log-only mode.
	progress := newProgressEmitter(cfg.OutDir, cfg.ProgressOut)
	defer progress.close()

	results := make([]*SeatResult, len(targets))
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		seatID := i + 1
		tgt := target
		isNative := nativeSeats[i]

		if isNative {
			go func(idx, sID int, t providers.Target) {
				defer wg.Done()
				// Progress finish is registered FIRST so it runs LAST: after
				// the panic-recovery defer below has produced results[idx].
				defer func() {
					progress.seatFinished(sID, len(targets), SeatKindNative, t, results[idx])
				}()
				defer func() {
					if rec := recover(); rec != nil && results[idx] == nil {
						msg := fmt.Sprintf("%v", rec)
						if len(msg) > 200 {
							msg = msg[:200] + "..."
						}
						results[idx] = &SeatResult{
							SeatID:        sID,
							Kind:          SeatKindNative,
							InitialTarget: t,
							FinalTarget:   t,
							Status:        "failed",
							Degraded:      true,
							Error:         fmt.Sprintf("panic: %s", msg),
						}
					}
				}()

				// Concurrency cap: acquire before spawn, release after exit;
				// excess native seats queue here (contract §2.3).
				nativeSem <- struct{}{}
				defer func() { <-nativeSem }()

				spec := SeatSpec{Kind: SeatKindNative, ModelID: t.Model.ID}
				// "running" is emitted AFTER the semaphore acquire: a queued
				// seat has not started yet.
				progress.seatStarted(sID, len(targets), SeatKindNative, t)
				outcome := nativeRunner.Run(ctx, cfg, spec, nativePrompt, swarmDeadline)
				results[idx] = nativeSeatResult(sID, t, outcome, redact)
			}(i, seatID, tgt)
			continue
		}

		// Build a dedicated ToolRunner per seat to allow per-model allowedTools filter
		seatRunner := agenttools.NewRunner(cfg.Root, allowWrite, allowExec, logger)
		if len(tgt.Model.Capabilities.AllowedTools) > 0 {
			seatRunner.AllowedModelTools = tgt.Model.Capabilities.AllowedTools
		}

		go func(idx, sID int, t providers.Target, r *agenttools.ToolRunner) {
			defer wg.Done()
			// Progress finish runs after the panic-recovery defer below (LIFO),
			// so a panicked seat still emits its finish line.
			defer func() {
				progress.seatFinished(sID, len(targets), SeatKindProvider, t, results[idx])
			}()
			// Last-ditch guard: a panic must never leave results[idx] nil, or
			// state recording and reconciliation would break for this seat.
			defer func() {
				if rec := recover(); rec != nil && results[idx] == nil {
					msg := fmt.Sprintf("%v", rec)
					if len(msg) > 200 {
						msg = msg[:200] + "..."
					}
					results[idx] = &SeatResult{
						SeatID:        sID,
						Kind:          SeatKindProvider,
						InitialTarget: t,
						FinalTarget:   t,
						Status:        "failed",
						Degraded:      true,
						Error:         fmt.Sprintf("panic: %s", msg),
					}
				}
			}()

			progress.seatStarted(sID, len(targets), SeatKindProvider, t)

			worker := &SeatWorker{
				SeatID:               sID,
				InitialTarget:        t,
				Registry:             reg,
				ExcludedFamilies:     assignedFamilies,
				Exclude:              exclude,
				Preset:               GetTaskPreset(cfg.Task),
				UserPrompt:           BuildUserPrompt(cfg.Task, cfg.Prompt),
				Runner:               r,
				Client:               client,
				MaxTurns:             cfg.MaxTurns,
				Tools:                openAITools,
				AllowToolsForSession: len(openAITools) > 0,
			}

			results[idx] = worker.Run(ctxWithTimeout)
		}(i, seatID, tgt, seatRunner)
	}

	wg.Wait()

	// 7. Update rotation state history
	var seatRecords []SeatRecord
	for _, res := range results {
		if res != nil {
			seatRecords = append(seatRecords, SeatRecord{
				SeatID:   res.SeatID,
				Provider: res.FinalTarget.Provider.Name,
				Model:    res.FinalTarget.Model.ID,
				Family:   res.FinalTarget.Model.FamilyName(),
				Status:   res.Status,
			})
		}
	}
	state.RecordRun(cfg.Task, seatRecords)
	_ = SaveState(state)

	// 8. Reconcile outputs, generate seat reports, summary.json & reconciliation.md
	summary, err := Reconcile(cfg.Task, cfg.Root, cfg.OutDir, len(targets), results)
	if err != nil {
		return nil, fmt.Errorf("reconciliation failed: %w", err)
	}

	return summary, nil
}

// progressFileName is the live per-seat progress log written inside the run's
// artifact directory, next to reconciliation.md / summary.json / seat reports.
// It lets an agent tail a background run:
// `tail -f reports/fanout/<ts>/progress.log`.
const progressFileName = "progress.log"

// progressEmitter renders the streaming per-seat progress lines to the
// terminal sink (RunConfig.ProgressOut) and to progress.log in the artifact
// directory. Seats run concurrently, so every emission holds the mutex: a line
// is never interleaved mid-line and the log stays byte-identical to the
// terminal stream. Progress must never fail a run: a nil sink discards, a
// failed log open degrades to terminal-only.
type progressEmitter struct {
	mu   sync.Mutex
	w    io.Writer
	file *os.File
}

// newProgressEmitter creates the artifact directory (at run start, so the log
// exists from the beginning) and opens progress.log for append inside it.
func newProgressEmitter(outDir string, w io.Writer) *progressEmitter {
	p := &progressEmitter{w: w}
	if outDir == "" {
		return p
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return p
	}
	f, err := os.OpenFile(filepath.Join(outDir, progressFileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return p
	}
	p.file = f
	return p
}

// close releases the log file. Safe to call once no seat goroutine is running
// (after wg.Wait / at Execute exit).
func (p *progressEmitter) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file != nil {
		_ = p.file.Close()
		p.file = nil
	}
}

// emit writes one complete line to both sinks under the mutex.
func (p *progressEmitter) emit(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w != nil {
		fmt.Fprintln(p.w, line)
	}
	if p.file != nil {
		fmt.Fprintln(p.file, line)
	}
}

// seatStarted renders "[seat N/M] <spec> running" when a seat begins work.
func (p *progressEmitter) seatStarted(seatID, total int, kind SeatKind, t providers.Target) {
	p.emit(fmt.Sprintf("[seat %d/%d] %s running", seatID, total, seatSpecLabel(kind, t)))
}

// seatFinished renders "[seat N/M] <spec> -> <status> (<duration>s[, findings:
// k][, thin][, degraded])" when a seat produces its result. The spec names the
// seat's FINAL target (a failover seat names the model that actually answered);
// the initial target is the fallback when no result exists.
func (p *progressEmitter) seatFinished(seatID, total int, initialKind SeatKind, initial providers.Target, res *SeatResult) {
	if res == nil {
		// The engine's panic recovery guarantees a non-nil result; a nil here
		// still owes the run one finish line.
		res = &SeatResult{
			SeatID:        seatID,
			Kind:          initialKind,
			InitialTarget: initial,
			FinalTarget:   initial,
			Status:        "failed",
			Degraded:      true,
		}
	}
	line := fmt.Sprintf("[seat %d/%d] %s -> %s (%.1fs",
		seatID, total, seatSpecLabel(res.Kind, res.FinalTarget), res.Status,
		float64(res.DurationMs)/1000)
	// The finding count reuses the reconciliation parser, so the number on the
	// progress line matches what summary.json carries for this seat. The thin
	// marker goes through the same classifier reconciliation applies (one
	// source of truth for "did this seat do the job"), so the line and the
	// consensus denominator can never disagree.
	if n := len(parseFindings(res.SeatID, "", "", res.Content)); n > 0 {
		line += fmt.Sprintf(", findings: %d", n)
	} else if seatIsThin(res) {
		line += ", thin"
	}
	if res.Degraded {
		line += ", degraded"
	}
	line += ")"
	p.emit(line)
}

// seatSpecLabel renders the progress-line spec: "native:<id>" for a native
// seat, "provider/model" (Target.FullName) for a provider seat. Registry- and
// user-derived ids are masked like every other echoed model name (E8), so a
// key-shaped id cannot ride onto the console or into the log.
func seatSpecLabel(kind SeatKind, t providers.Target) string {
	if kind == SeatKindNative {
		return providers.MaskOperand(nativePrefix + t.Model.ID)
	}
	return providers.MaskOperand(t.FullName())
}

// newSwarmClient resolves the shared provider client for a run: an injected
// client (RunConfig.Client test seam) is honored as-is, otherwise the client's
// per-request timeout is derived from the swarm budget. The client timeout
// must never be shorter than the budget: a flat 120s cap killed single chat
// calls mid-seat under any longer budget (the second, independent half of the
// 2026-10-14 "context deadline exceeded" degradations). Context propagation
// stays the hard backstop: ctxWithTimeout still bounds the whole swarm, so a
// longer client timeout can never outlive the budget.
func newSwarmClient(cfg RunConfig) *Client {
	if cfg.Client != nil {
		return cfg.Client
	}
	return NewClientWithBudget(cfg.TimeoutSec)
}

// nativeSeatPreamble is the anti-orchestration control that OPENS every native
// seat prompt. A native seat is a COMPLETE agent session: it has subagent
// capability and it inherits the user's plugins, so a capable model does what
// agentic CLIs are tuned to do - delegate - and burns the whole time budget
// with zero findings. Live evidence: reports/fanout/20261014-042933 (123.7s,
// 0 findings, "Waiting for the subagent swarm") and reports/fanout/
// 20261014-005544 (244s, 0 findings, "I have dispatched a concurrent
// 3-subagent review swarm"), both recorded status=success.
//
// The provider-seat path (SeatWorker Preset/UserPrompt) is deliberately NOT
// prefixed: provider seats are stateless HTTP chats and cannot nest.
//
// The timeboxing clause exists because a seat can also starve itself: a real
// seat burned its whole 300s budget exploring and agy's own --print-timeout
// returned partial (empty) output at 303.4s (reports/fanout/20261014-065355),
// so the seat must stop reading early enough to still write its report.
const nativeSeatPreamble = `## Seat role - read this first

You are ONE independent seat of a parallel multi-model swarm. The other seats
run right now in separate processes; you cannot talk to them, coordinate with
them, or wait for them.

Do the assigned work yourself, in this single session:
- NEVER spawn subagents, and NEVER delegate any part of the work to another
  agent, model, or team.
- NEVER launch, join, or wait for another swarm, another fanout, or any
  parallel agent team. You ARE the swarm for this task.
- Use your own tools (read_file, grep, glob, list_dir) directly.
- Spend at most HALF the time budget on reading and exploration, then write
  the complete report with whatever you have.
- Write the COMPLETE final report before the time budget runs out. A status
  update, a plan, or a promise to report later counts as a failed seat.`

// buildNativeSeatPrompt assembles the native-seat prompt: the seat-role
// preamble FIRST (anti-nesting control), then the task preset system prompt
// and the user text. Kept as one function so the preamble's presence and
// position are test-pinned for every task preset.
func buildNativeSeatPrompt(task, userPrompt string) string {
	return nativeSeatPreamble + "\n\n" + GetTaskPreset(task).SystemPrompt + "\n\n" + BuildUserPrompt(task, userPrompt)
}
