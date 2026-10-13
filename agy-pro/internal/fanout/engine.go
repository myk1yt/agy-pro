package fanout

import (
	"context"
	"errors"
	"fmt"
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

// Execute orchestrates the full double-blind multi-model swarm run.
func Execute(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
	// 1. Defaults & validation
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
		cfg.TimeoutSec = 120
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 10
	}

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
				return nil, fmt.Errorf("failed to resolve model %q: %w", spec.ModelID, terr)
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
	client := cfg.Client
	if client == nil {
		client = NewClient()
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()

	// Native-seat machinery (contract §2.2/§2.3): one shared runner, a
	// FIFO buffered-channel semaphore bounding concurrent native subprocesses,
	// and the shared seat prompt (system preset + user text). Provider seats
	// keep today's unbounded parallel goroutines.
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
	nativePrompt := GetTaskPreset(cfg.Task).SystemPrompt + "\n\n" + BuildUserPrompt(cfg.Task, cfg.Prompt)
	swarmDeadline, _ := ctxWithTimeout.Deadline()
	redact := reg.Redactor()

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
