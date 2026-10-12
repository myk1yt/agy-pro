package fanout

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
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
		return nil, fmt.Errorf("failed to load providers: %w", err)
	}

	// 3. Load rotation state
	state, _ := LoadState()

	// 4. Select Targets (Double-Blind candidate assignment)
	targets, err := SelectTargets(reg, state, cfg.N, cfg.Models)
	if err != nil {
		return nil, fmt.Errorf("target selection failed: %w", err)
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

	results := make([]*SeatResult, len(targets))
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		seatID := i + 1
		tgt := target

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
