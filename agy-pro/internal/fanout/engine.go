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

// SeatLineupOrigin records where a run's seat lineup came from. It is the
// split FL-3 requires: the store's default_preset rung is standing cost approval
// (the user configured it once, so a drifted seat is skipped and no per-run
// re-ask happens), while a NAMED preset (--preset / AGY_PRO_FANOUT_PRESET) is an
// ad-hoc choice for one run: it resolves the same lineup, but its metered seats
// keep the per-run echo and the caller's go-ahead, and a drifted seat stays a
// hard error. The zero value means "not from a preset" (an explicit --seats
// lineup or the engine's auto-selection).
type SeatLineupOrigin struct {
	// Preset is the preset name ("" = no preset supplied the lineup).
	Preset string
	// Rung is RungDefaultPreset, RungPresetFlag or RungPresetEnv ("" when no
	// preset supplied the lineup).
	Rung string
}

// FromPreset reports whether a preset supplied this run's lineup.
func (o SeatLineupOrigin) FromPreset() bool { return o.Preset != "" }

// Standing reports whether the lineup carries standing cost approval: only the
// store default_preset rung does.
func (o SeatLineupOrigin) Standing() bool { return o.Rung == RungDefaultPreset }

const (
	SearchNativeAuto = "auto"
	SearchNativeOff  = "off"
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

	// AllowHosts is the B4-2a read_url_content host allow-list (normalized:
	// lowercase hostnames, port ignored; see agenttools.NormalizeAllowHosts).
	// Populated from the repeatable --allow-host flag in buildRunConfig. A
	// non-empty list is ALSO the exposure gate: only then does every
	// provider seat's tool list carry read_url_content and its runner this
	// allow-list (cross-verification and partition alike; native seats are
	// unaffected). nil/empty leaves the wire byte-identical to pre-B4-2a.
	AllowHosts []string

	// SearchSource configures provider-seat search_web. A non-empty endpoint
	// selects the explicit HTTP transport. With an empty endpoint, the engine
	// installs NativeSearch when SearchNative is auto and the real agy binary
	// resolves; KeyEnv alone does not select explicit mode. Native seats never
	// use this runner-injected fallback.
	SearchSource agenttools.SearchSource
	// SearchNative is auto (empty-endpoint native fallback, the default) or
	// off (the legacy no-source gate and not-configured refusal).
	SearchNative string
	// SearchAgentPresence answers the once-per-run "is the restricted search
	// agent deployed?" question (P32) and carries its fallback notice. The CLI
	// resolves it before the run notice, so the disclosed route is the route the
	// child takes, and threads the same object here so the engine never
	// re-probes. nil -> the native runner builds and probes its own at the first
	// search (library callers, tests).
	SearchAgentPresence *SearchAgentPresence

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
	// SeatLineup records the preset that supplied Seats (zero value when the
	// lineup came from an explicit --seats flag or from the engine's
	// auto-selection). Set only by the CLI's buildRunConfig, and only when the
	// preset rung won the seat field. The engine reads it twice: Rung selects the
	// drift policy (a STANDING default_preset lineup skips an unresolvable seat
	// with a warning and counts the loss; every other lineup keeps the hard
	// error, because either the operator typed that seat or the preset was named
	// for this run only), and the whole struct is copied into summary.json so the
	// artifact says which preset and which rung produced the lineup.
	SeatLineup SeatLineupOrigin
	// Units is the parsed partition plan (B2, `--units <planfile>`). When it
	// is non-empty the engine runs PARTITION mode: exactly one seat per unit,
	// each seat's user prompt is that unit's body (UnitBody), and each seat's
	// ToolRunner is bounded to that unit's Scope (B3). nil/empty = the legacy
	// single-prompt cross-verification path, which stays byte-identical: the
	// shared system prompt is the preamble, so every unit keeps the same
	// cacheable prefix and only the body varies.
	//
	// Units and Seats/Models are mutually exclusive at the CLI (buildRunConfig);
	// a library caller that sets both gets partition mode (Units wins).
	Units []Unit
	// MaxNativeConcurrency caps simultaneous native seats; <=0 falls back to
	// defaultFanoutMaxNative (contract §2.3). Resolution
	// (flag > state.json > default) happens in cli.go.
	MaxNativeConcurrency int
	// NativeResolver resolves the real-agy binary; nil -> realagy.Resolve
	// (test seam, contract §2.1).
	NativeResolver func() (string, error)
	// SpawnCommand builds the native-seat subprocess; nil ->
	// exec.CommandContext (test seam, contract §2.1).
	SpawnCommand func(ctx context.Context, bin string, args ...string) *exec.Cmd
	// ExcludeModel is the F-C pool-exclusion seam consulted by SelectTargets
	// rotation AND worker failover candidate filtering; it matches on the
	// provider name, the model id and the family, so the user-owned exclusion
	// list (D1, defaults.excludeModels) can name a provider. It is NEVER applied
	// to explicit user picks: --seats provider tokens and plan-unit
	// seats are honored, and an excluded pick carries the shared warning on
	// WarnOut. nil -> the built-in default policy (the k3 rule, through
	// providers.ExclusionPolicy).
	ExcludeModel ExcludeFunc

	// WarnOut receives the exclusion warning for each honored-but-excluded
	// explicit pick (nil = discard). The CLI wires stderr; the sentence is the
	// shared one providers/swarm also print.
	WarnOut io.Writer

	// Sources records which resolution rung supplied each config field
	// (P8 ladder: flag|preset|defaults|engine|state). Set only by the CLI's
	// buildRunConfig; nil for library callers. The summary keeps only the
	// preset/defaults rungs (interestingSources); the engine never acts on it.
	Sources map[string]string
	// JSONOutput is the P8-resolved --json switch (--json flag > preset json
	// field > false). Set only by the CLI's buildRunConfig; the engine never
	// acts on it (Run() uses it to route the progress sink and the summary).
	JSONOutput bool
}

// DefaultTimeoutSec is the swarm wall-clock budget when neither the --timeout
// flag nor RunConfig.TimeoutSec sets one. It is the single source of truth the
// settings panel surfaces as "Fanout Wall Timeout (--timeout)".
//
// 300s, not the historical 120s: a real seat cannot finish in 120s. The
// provider seat loop (worker.go runChatLoop) runs up to 500 sequential chat
// turns with tool calls and reasoning models take tens of seconds per call.
// Live evidence (2026-10-14): both provider seats in reports/fanout/
// 20261014-042933 degraded with "all failover attempts exhausted; last error:
// context deadline exceeded", and a fixed native seat needed 305s to complete
// a real review.
const DefaultTimeoutSec = 300

// B7 partition-mode budget scaling. A partition run runs one provider seat
// PER UNIT, concurrently and uncapped (see the engine comment at the seat
// loop), so the flat 300s cross-verification default starves any plan with
// more than a couple of units: every seat shares the ONE swarm deadline, and
// the deadline also has to absorb the rate-limit/failover storm that N
// concurrent seats create on one provider account. The default therefore
// grows with the plan: the flat 300s base plus a per-unit allowance, capped
// so a pathological plan cannot book the machine for hours.
const (
	// partitionTimeoutPerUnitSec is the extra wall-clock budget each plan
	// unit adds to the partition-mode default deadline. Linear, not sqrt:
	// the allowance covers provider contention (retries, failover hops) that
	// grows with the concurrent seat count, not the units' own work, which
	// runs in parallel and is bounded by its scope.
	partitionTimeoutPerUnitSec = 120
	// partitionMaxTimeoutSec clamps the scaled default: the largest budget a
	// partition run may book WITHOUT an explicit --timeout.
	partitionMaxTimeoutSec = 3600
)

// partitionDefaultTimeoutSec is the B7 effective default deadline for a
// partition run of nUnits units with no explicit --timeout:
// DefaultTimeoutSec + partitionTimeoutPerUnitSec per unit, clamped to
// partitionMaxTimeoutSec. An explicit --timeout never passes through here.
func partitionDefaultTimeoutSec(nUnits int) int {
	t := DefaultTimeoutSec + partitionTimeoutPerUnitSec*nUnits
	if t > partitionMaxTimeoutSec {
		return partitionMaxTimeoutSec
	}
	return t
}

// withDefaults returns cfg with the engine defaults applied, so a RunConfig
// built outside the CLI (library caller, test, swarm delegation) resolves to
// the same surface the flag defaults produce.
//
// B7 timeout resolution: TimeoutSec > 0 is ALWAYS honored exactly (it is an
// explicit budget, from --timeout or a library caller). TimeoutSec <= 0 means
// "no explicit budget": a partition run (cfg.Units non-empty) gets the
// unit-scaled default (partitionDefaultTimeoutSec), anything else gets the
// flat DefaultTimeoutSec. The CLI's buildRunConfig leaves TimeoutSec at 0 for
// --units runs without an explicit --timeout (detected via flags.Visit), so
// the scaling applies there too; a library caller that sets TimeoutSec
// explicitly is by definition not relying on the default.
func (cfg RunConfig) withDefaults() RunConfig {
	if cfg.Task == "" {
		cfg.Task = "review"
	}
	if cfg.SearchNative == "" {
		cfg.SearchNative = SearchNativeAuto
	}
	if cfg.N <= 0 {
		cfg.N = 3
	}
	if cfg.Root == "" {
		cfg.Root = "."
	}
	if cfg.TimeoutSec <= 0 {
		if len(cfg.Units) > 0 {
			cfg.TimeoutSec = partitionDefaultTimeoutSec(len(cfg.Units))
		} else {
			cfg.TimeoutSec = DefaultTimeoutSec
		}
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = defaultFanoutMaxTurns
	}
	if cfg.MaxNativeConcurrency <= 0 {
		cfg.MaxNativeConcurrency = defaultFanoutMaxNative
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

	// P9 prompt store: resolve the seat preamble and task preset ONCE per run
	// (env > file > compiled default) so every seat in the run shares one
	// resolution. A corrupt or empty slot file fails the run here, before a
	// single seat spawns, with an error naming the path. With no file and no
	// env the resolved values ARE the compiled constants, so every prompt
	// byte stays identical to the pre-store behavior.
	seatPreamble, err := ResolveSeatPreamble()
	if err != nil {
		return nil, err
	}
	presetText, err := ResolveTaskPreset(cfg.Task)
	if err != nil {
		return nil, err
	}
	seatPreset := GetTaskPreset(cfg.Task)
	seatPreset.SystemPrompt = presetText

	// 2. Load Registry
	var reg *providers.Registry
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
		// Library callers and tests may leave the seam unset; the built-in
		// default policy is the zero ExclusionPolicy (no user list), which
		// reproduces the k3 rule through the ONE matching authority.
		exclude = PolicyExcluder(providers.ExclusionPolicy{})
	}

	var targets []providers.Target
	var nativeSeats []bool // parallel to targets: true = native subprocess seat
	var units []Unit       // parallel to targets; non-nil ONLY in partition mode (one seat per unit)
	// skipped collects the PRESET-lineup seats this run had to drop (drift
	// policy, resolveSeatTargets). Empty on every other path, so the run and its
	// artifacts stay byte-identical unless a preset seat actually drifted.
	var skipped []skippedSeat
	if len(cfg.Units) > 0 {
		// PARTITION mode (B2/B3): one seat per plan unit. The parallel slices
		// follow the nativeSeats precedent above.
		//
		// B4: fail closed. cfg.Units can arrive from a library caller that never
		// ran the CLI's ParsePlan. A unit with an empty, blank, or root-wide
		// Scope would yield a nil ToolRunner scope filter: an UNRESTRICTED seat
		// (fail-open). Re-check every unit's scope the way ParsePlan does before
		// a single seat starts, and refuse a bad unit instead of running it.
		if verr := validatePartitionUnits(cfg.Units); verr != nil {
			return nil, verr
		}
		targets, nativeSeats, units, err = partitionTargets(cfg, reg, state, exclude)
		if err != nil {
			return nil, err
		}
	} else if len(cfg.Seats) > 0 {
		var serr error
		targets, nativeSeats, skipped, serr = resolveSeatTargets(cfg, reg, exclude)
		if serr != nil {
			return nil, serr
		}
	} else {
		targets, err = SelectTargets(reg, state, cfg.N, cfg.Models, exclude, cfg.WarnOut)
		if err != nil {
			return nil, fmt.Errorf("target selection failed: %w", err)
		}
		nativeSeats = make([]bool, len(targets)) // all false: legacy provider path
	}

	// 5. Initialize Tool Environment & Permissions
	toolsSpec := strings.ToLower(cfg.Tools)
	allowWrite := strings.Contains(toolsSpec, "write")
	allowExec := strings.Contains(toolsSpec, "exec")

	// B6/B4-1: partition mode is read-only BY DEFAULT. The per-unit scope
	// filter now bounds the write tools too (guard.go), but write/exec tools
	// still need an explicit per-unit opt-in before a seat may use them. The
	// run-level flags from --tools are therefore forced off here (the CLI
	// gate refuses --units with write/exec outright; this clamp is the
	// fail-closed backstop for a library caller that bypassed that gate), and
	// the single deliberate carve-out below re-enables write ONLY for a unit
	// that opted in per plan ("write": true).
	if len(units) > 0 {
		allowWrite = false
		allowExec = false
	}

	home, _ := providers.Home()
	logPath := filepath.Join(home, "logs", fmt.Sprintf("fanout-%s.jsonl", ts))
	logger, _ := agenttools.NewLogger(logPath, reg.Redactor())

	openAITools := agenttools.BuildOpenAITools(allowWrite, allowExec)

	ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
	defer cancel()
	swarmDeadline, _ := ctxWithTimeout.Deadline()

	// Native seats and native search fallback share one runner and concurrency
	// pool, so a search turn cannot bypass the run's native subprocess cap.
	nativeCap := mode.ClampNativeSeatConcurrency(cfg.MaxNativeConcurrency)
	nativeSem := make(chan struct{}, nativeCap)
	nativeScrubNames := realagy.ApiKeyEnvNames(reg)
	if keyEnv := strings.TrimSpace(cfg.SearchSource.KeyEnv); keyEnv != "" {
		nativeScrubNames = append(nativeScrubNames, keyEnv)
	}
	nativeRunner := &NativeRunner{
		Resolver:    cfg.NativeResolver,
		Spawn:       cfg.SpawnCommand,
		ViaStdin:    false,
		SearchAgent: searchAgentPresenceFor(cfg),
		scrubNames:  nativeScrubNames,
	}

	// B4-2a exposure gate: read_url_content joins every provider seat's tool
	// list ONLY when the run carries a host allow-list (--allow-host). The
	// runner re-checks the list on every call (an empty list refuses), so a
	// schema that somehow reached a model still cannot fetch unlisted.
	netTool := len(cfg.AllowHosts) > 0
	if netTool {
		openAITools = append(openAITools, agenttools.ReadURLContentToolDef())
	}

	// An explicit endpoint always wins. Otherwise auto mode offers search_web
	// only when the real agy binary resolves; off and resolution failure keep
	// the legacy no-source gate and honest call-time refusal.
	searchTool := strings.TrimSpace(cfg.SearchSource.Endpoint) != ""
	if cfg.SearchNative == SearchNativeOff {
		cfg.SearchSource.NativeSearch = nil
	} else if !searchTool && cfg.SearchSource.NativeSearch == nil {
		if _, err := nativeRunner.resolve(); err == nil {
			cfg.SearchSource.NativeSearch = func(query string) (string, error) {
				select {
				case nativeSem <- struct{}{}:
					defer func() { <-nativeSem }()
				case <-ctxWithTimeout.Done():
					return "", ctxWithTimeout.Err()
				}
				return runNativeSearch(ctxWithTimeout, cfg, nativeRunner, query, swarmDeadline)
			}
		}
	}
	if !searchTool && cfg.SearchSource.NativeSearch != nil {
		searchTool = true
	}
	if searchTool {
		openAITools = append(openAITools, agenttools.SearchWebToolDef())
	}

	// Collect assigned families to inform failover exclusions
	var assignedFamilies []string
	for _, t := range targets {
		assignedFamilies = append(assignedFamilies, t.Model.FamilyName())
	}

	// 6. Spawn Double-Blind Concurrent Workers
	client := newSwarmClient(cfg)

	// The shared native-seat prompt for the cross-verification path (seat-role
	// preamble + system preset + user text, assembled by assembleSeatPrompt
	// from the once-per-run prompt-store resolution).
	// Partition mode builds a per-unit prompt inside the seat goroutine
	// instead: the preamble and preset stay byte-identical across units, only
	// the unit body varies, so cross-unit prefix caching survives.
	var nativePrompt string
	if len(cfg.Units) == 0 {
		nativePrompt = assembleSeatPrompt(seatPreamble, presetText, cfg.Task, cfg.Prompt)
	}
	redact := reg.Redactor()

	// Live seat progress: created after every hard-failure path (registry
	// load, target selection) and before the first seat starts, so the
	// artifact directory and progress.log inside it exist from run start and
	// an agent can tail a background run immediately - while an instant
	// failure still leaves no reports/ behind (launcher pin). A nil
	// ProgressOut keeps the log-only mode.
	progress := newProgressEmitter(cfg.OutDir, cfg.ProgressOut)
	defer progress.close()

	// Drift policy: a preset seat that no longer resolves is announced BEFORE
	// the first seat starts and against the ORIGINAL lineup size, so
	// progress.log (and the live sink) carries "seat 2/3 ... skipped" instead of
	// letting a shrunken run look like a full-quorum one. The per-seat warning
	// already went to WarnOut in resolveSeatTargets; this is the streaming
	// line's copy.
	for _, sk := range skipped {
		progress.seatSkipped(sk.number, len(cfg.Seats), cfg.SeatLineup.Preset, sk.spec)
	}

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
				// Partition mode (B2/B3): this seat works on one unit, so it
				// carries the unit id, the per-unit prompt (shared preamble +
				// this unit's body) and a per-unit config copy. The legacy path
				// passes the run-level prompt and cfg unchanged.
				unitCfg, unitPrompt := cfg, nativePrompt
				if len(units) > 0 {
					u := units[idx]
					spec.UnitID = u.ID
					unitCfg = unitRunConfig(cfg, u)
					unitPrompt = assembleSeatPrompt(seatPreamble, presetText, cfg.Task, UnitBody(u))
				}
				outcome := nativeRunner.Run(ctx, unitCfg, spec, unitPrompt, swarmDeadline)
				results[idx] = nativeSeatResult(sID, t, outcome, redact)
				// B7: per-unit attribution, symmetric with the provider path.
				// spec.UnitID is only set in partition mode, and B5 refuses
				// native partition seats, so this is a no-op in practice today.
				if spec.UnitID != "" {
					results[idx].UnitID = spec.UnitID
				}
			}(i, seatID, tgt)
			continue
		}

		// B4-1 carve-out: a write unit's seat gets AllowWrite=true and the
		// write tools in its tool list; every other partition seat (and every
		// cross-verification seat) keeps the run-level flags exactly. The
		// write-scope guard inside the runner (the same dual-view rule as the
		// reads) confines every write to the unit's scope, and the B6 claim
		// walk catches cross-unit output conflicts at merge time.
		seatAllowWrite := allowWrite
		seatTools := openAITools
		if len(units) > 0 && units[i].Write {
			seatAllowWrite = true
			seatTools = agenttools.BuildOpenAITools(true, false)
			if netTool {
				seatTools = append(seatTools, agenttools.ReadURLContentToolDef())
			}
			if searchTool {
				seatTools = append(seatTools, agenttools.SearchWebToolDef())
			}
		}
		// Build a dedicated ToolRunner per seat to allow per-model allowedTools filter
		seatRunner := agenttools.NewRunner(cfg.Root, seatAllowWrite, allowExec, logger)
		// B4-2a: the host allow-list reaches EVERY provider seat's runner,
		// cross-verification and partition alike (native seats spawn real agy
		// and never build this runner).
		seatRunner.AllowHosts = cfg.AllowHosts
		// B4-2b: the search source reaches EVERY provider seat's runner,
		// cross-verification and partition alike (native seats spawn real agy
		// and never build this runner).
		seatRunner.SearchSource = cfg.SearchSource
		if len(tgt.Model.Capabilities.AllowedTools) > 0 {
			seatRunner.AllowedModelTools = tgt.Model.Capabilities.AllowedTools
		}
		// Partition mode (B2/B3): this seat works on one unit, so its user
		// prompt is that unit's body and its path-reading AND path-writing
		// tools are bounded to that unit's scope (B4-1). On the legacy path
		// every value below is the run-level one and Scope stays nil
		// (dispatch byte-identical).
		unitID := ""
		userPrompt := BuildUserPrompt(cfg.Task, cfg.Prompt)
		var unitScope []string
		if len(units) > 0 {
			u := units[i]
			unitID = u.ID
			userPrompt = UnitBody(u)
			unitScope = u.Scope
		}
		seatRunner.Scope = unitScope

		go func(idx, sID int, t providers.Target, r *agenttools.ToolRunner, unitID, userPrompt string, tools []agenttools.ToolDef) {
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
				Preset:               seatPreset,
				UserPrompt:           userPrompt,
				UnitID:               unitID,
				Runner:               r,
				Client:               client,
				MaxTurns:             cfg.MaxTurns,
				Tools:                tools,
				AllowToolsForSession: len(tools) > 0,
			}

			results[idx] = worker.Run(ctxWithTimeout)
			// B7: surface the per-unit id for attribution only (it never enters
			// the request/prompt). unitID is "" on the cross-verification path,
			// so this is a partition-only write and the summary stays
			// byte-identical without --units (UnitID is omitempty).
			if unitID != "" {
				results[idx].UnitID = unitID
			}
		}(i, seatID, tgt, seatRunner, unitID, userPrompt, seatTools)
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

	// 8. Produce the run outputs. PARTITION mode (B5/B6) MERGES the per-unit
	// work products into one artifact with the authoritative disjointness
	// scan; the cross-verification path RECONCILES findings. The
	// cross-verification call stays byte-identical to the pre-partition
	// behavior.
	var summary *SummaryReport
	if len(cfg.Units) > 0 {
		summary, err = MergePartition(cfg.Task, cfg.Root, cfg.OutDir, cfg.Units, results)
		if err != nil {
			return nil, fmt.Errorf("partition merge failed: %w", err)
		}
	} else {
		// nRequested is the ORIGINAL seat count of the requested lineup, so a
		// run that lost seats to drift reports "2 / 3" instead of presenting the
		// shrunken quorum as the request (C4-adjacent honesty rule that
		// ThinSeats already follows).
		nRequested := len(targets)
		var meta []RunMeta
		{
			m := RunMeta{}
			if len(skipped) > 0 {
				nRequested = len(cfg.Seats)
				specs := make([]string, 0, len(skipped))
				for _, sk := range skipped {
					// R2-1: a seat TOKEN, so it takes the registry-slot authority
					// (MaskSlot = MaskOperand + the underscore-head arm, and it strips
					// the Cc/Cf class internally). The value lands in summary.json,
					// a shareable artifact.
					specs = append(specs, MaskSeatToken(sk.spec))
				}
				m.Loss = SeatLoss{Count: len(skipped), Specs: specs}
			}
			// FL-3(c): the artifact names the preset AND the rung that supplied the
			// lineup, so a reader can tell a standing default_preset lineup from an
			// ad-hoc named one - the distinction the cost policy turns on.
			if cfg.SeatLineup.FromPreset() && len(cfg.Seats) > 0 {
				metered := 0
				for _, s := range cfg.Seats {
					if s.Kind != SeatKindNative {
						metered++
					}
				}
				m.SeatLineup = &SeatLineupReport{
					Preset:      providers.MaskSlot(providers.StripSlotControls(cfg.SeatLineup.Preset)),
					Rung:        providers.StripSlotControls(cfg.SeatLineup.Rung),
					Seats:       len(cfg.Seats),
					MeteredSeat: metered,
					Standing:    cfg.SeatLineup.Standing(),
				}
			}
			if m.Loss.Count > 0 || m.SeatLineup != nil {
				meta = []RunMeta{m}
			}
		}
		summary, err = Reconcile(cfg.Task, cfg.Root, cfg.OutDir, nRequested, results, meta...)
		if err != nil {
			return nil, fmt.Errorf("reconciliation failed: %w", err)
		}
	}

	// P8: surface the resolution rung per field in the machine summary. Only
	// rungs that actually supplied a value are kept (preset and defaults;
	// flag/engine/state are the expected case), so an unconfigured run's
	// summary.json stays byte-identical.
	if s := interestingSources(cfg.Sources); len(s) > 0 {
		summary.Sources = s
	}

	return summary, nil
}

// interestingSources keeps only the preset and defaults rungs from the P8
// resolution map: flag, engine, and state rungs are the expected case (a
// state.json max_native fallback is pre-P8 behavior, not a supplied value) and
// stay invisible, so an unconfigured run's summary.json stays byte-identical.
// Returns nil when nothing interesting remains (map length 0 stays nil, so the
// summary's omitempty key disappears).
func interestingSources(src map[string]string) map[string]string {
	var out map[string]string
	for field, rung := range src {
		if rung != "preset" && rung != "defaults" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[field] = rung
	}
	return out
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

// seatSkipped renders "[preset \"<name>\" seat N/M] <spec> skipped (<reason>)" for
// a PRESET-lineup seat the drift policy dropped before the run started. N/M
// counts the preset's ORIGINAL lineup and the label says so, because the
// surviving seats are renumbered by the ordinary seat ordering: an unreachable
// "[seat 2/3] ... skipped" beside a "[seat 1/2] ... running" would read as a
// contradiction, while this form cannot. Only provider seats can reach here (a
// native id is never registry-checked); the spec is a seat TOKEN, so it renders
// through the registry-slot authority (MaskSeatToken), and the preset name is a
// file-sourced registry slot (MaskSlot).
func (p *progressEmitter) seatSkipped(number, total int, presetName, spec string) {
	p.emit(fmt.Sprintf("[preset %q seat %d/%d] %s skipped (%s)",
		providers.MaskSlot(providers.StripSlotControls(presetName)), number, total,
		MaskSeatToken(spec), skippedSeatReason))
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
	if res.UnitID != "" {
		// B5: a partition seat's Content is a work product, not a findings
		// list, so the findings parser and the thin classifier must never
		// label it. The partition finish line reports the seat's real status
		// (success/failed/degraded) and nothing else.
		if res.Degraded {
			line += ", degraded"
		}
		line += ")"
		p.emit(line)
		return
	}
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
//
// FIX-8 / R2-1: BOTH branches render through the registry-slot authority
// (MaskSeatToken for the native form, MaskSlot for the composed provider form),
// not the operand masker: MaskOperand is the identity on the underscore-head
// credential class, so it would let a key-shaped id ride whichever slot it
// reached. A native id is a seat TOKEN the operator typed (--seats) or picked in
// the catalog, which is exactly a value of that class, so it takes the same
// authority the provider form does. When the mask fires the whole native token
// renders opaque - the "native:" prefix goes with the credential core, which is
// intended (a masked token is opaque) - and benign specs keep their exact bytes
// in both branches.
func seatSpecLabel(kind SeatKind, t providers.Target) string {
	if kind == SeatKindNative {
		// R2-1: a native id is still a seat TOKEN the operator typed, and the
		// underscore-head credential class is identity under MaskOperand, so this
		// branch takes the same registry-slot authority as the provider branch.
		// Benign native ids keep their exact bytes.
		return MaskSeatToken(nativePrefix + t.Model.ID)
	}
	return providers.MaskSlot(t.FullName())
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

// assembleSeatPrompt concatenates the resolved seat prompt parts in the
// pinned order: the seat-role preamble FIRST (anti-nesting control), then
// the task preset system prompt, then the user text, each pair separated by
// a blank line.
func assembleSeatPrompt(preamble, preset, task, userPrompt string) string {
	return preamble + "\n\n" + preset + "\n\n" + BuildUserPrompt(task, userPrompt)
}

// buildNativeSeatPrompt resolves the prompt store (env > file > compiled
// default, see prompt_store.go) and assembles the native-seat prompt via
// assembleSeatPrompt. With no overrides the result is byte-identical to the
// compiled constants. Kept as one function so the preamble's presence and
// position stay test-pinned for every task preset.
func buildNativeSeatPrompt(task, userPrompt string) (string, error) {
	preamble, err := ResolveSeatPreamble()
	if err != nil {
		return "", err
	}
	preset, err := ResolveTaskPreset(task)
	if err != nil {
		return "", err
	}
	return assembleSeatPrompt(preamble, preset, task, userPrompt), nil
}

// skippedSeat records one seat a PRESET-sourced lineup lost because it no
// longer resolves (drift policy). The number is the seat's 1-based position in
// the preset's lineup, so the operator can find it in the preset editor; the
// surviving seats are renumbered by the engine's ordinary seat ordering and the
// summary reports the ORIGINAL requested count, so "2 / 3" plus this list is
// the honest picture.
type skippedSeat struct {
	number int
	spec   string // the raw seat token (masked before every echo)
}

// skippedSeatReason is the cause every skipped seat shares today: the registry
// no longer resolves the model the preset named.
const skippedSeatReason = "no longer available"

// resolveSeatTargets builds the explicit seat list (mixed native + provider,
// contract §2.1/§2.4) and applies the policy split for a seat that does not
// resolve:
//
//   - an EXPLICIT --seats lineup (!cfg.SeatLineup.FromPreset()) hard-errors, as
//     it always has: the operator typed that exact seat, so silently running a
//     different lineup would misreport the quorum.
//   - a NAMED preset (--preset / AGY_PRO_FANOUT_PRESET) also hard-errors: the
//     user asked for that preset on THIS run, so it is ad-hoc, not standing
//     configuration (FL-3).
//   - a STANDING preset lineup (the store's default_preset rung, which the user
//     configured once and did not re-name) is machine configuration the caller
//     never typed, so a drifted seat is skipped with ONE warning per seat and the
//     rest of the lineup still runs. When EVERY seat has drifted the run fails
//     with a single message naming the preset, because a run with no seats is not
//     a run.
//
// Native seats are not drift-checked here: a native id is never validated
// against the registry (nativeTarget builds the subprocess target directly), so
// an unknown native id surfaces as a degraded seat inside the summary instead.
func resolveSeatTargets(cfg RunConfig, reg *providers.Registry, exclude ExcludeFunc) (targets []providers.Target, nativeSeats []bool, skipped []skippedSeat, err error) {
	targets = make([]providers.Target, 0, len(cfg.Seats))
	nativeSeats = make([]bool, 0, len(cfg.Seats))
	for i, spec := range cfg.Seats {
		if spec.Kind == SeatKindNative {
			targets = append(targets, nativeTarget(spec.ModelID))
			nativeSeats = append(nativeSeats, true)
			continue
		}
		tgt, terr := reg.FindTarget(spec.ModelID)
		if terr != nil {
			if !cfg.SeatLineup.Standing() {
				// R2-1: both halves of this error carry the token. The %q operand is
				// masked with the registry-slot authority (the underscore-head class is
				// identity under MaskOperand), and the WRAPPED registry error is
				// masked too: FindTarget's message re-embeds the raw model spec
				// ("model \"provider/sk_live_...\" not found in registry"), which reached
				// stderr and the CLI's error path raw before this.
				return nil, nil, nil, fmt.Errorf("failed to resolve model %q: %w",
					MaskSeatToken(spec.ModelID), maskSeatError(terr, spec.ModelID, spec.Raw))
			}
			skipped = append(skipped, skippedSeat{number: i + 1, spec: spec.Raw})
			if cfg.WarnOut != nil {
				fmt.Fprintf(cfg.WarnOut, "fanout: warning: %s\n", skippedSeatWarning(i+1, spec.Raw, cfg.SeatLineup.Preset))
			}
			continue
		}
		targets = append(targets, tgt)
		nativeSeats = append(nativeSeats, false)
		// D2=(b): an explicit --seats pick is honored even when the
		// exclusion list covers it; the warning is the only consequence.
		// Native specs above are skipped by design: they never enter the
		// registry pool the list names.
		warnExplicitPick(cfg.WarnOut, exclude, tgt)
	}
	if len(skipped) > 0 && len(targets) == 0 {
		return nil, nil, nil, fmt.Errorf("preset %q pins %d seat(s), but none of them resolve any more; nothing would run (fix the lineup in the preset, or pass --seats)",
			providers.MaskSlot(cfg.SeatLineup.Preset), len(cfg.Seats))
	}
	return targets, nativeSeats, skipped, nil
}

// maskSeatError rewrites a registry-side error so the seat/model TOKENS it
// re-embeds in its own message render through the registry-slot authority. The
// registry's not-found text quotes the raw spec ("model \"provider/sk_live_...\" not
// found in registry"), and MaskOperand is the identity on that underscore-head
// class, so the seeded CLI masker could not close it either (R2-1).
//
// The error is wrapped, not flattened, so errors.Is/As chains survive; when no
// seed actually appears the original value is returned untouched.
func maskSeatError(err error, seeds ...string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	masked := text
	for _, seed := range seeds {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			continue
		}
		masked = strings.ReplaceAll(masked, seed, MaskSeatToken(seed))
	}
	// R3-item-4: the final text also drops the Cc/Cf class. A registry message
	// can quote a hand-edited model id byte for byte, and a control rune there
	// repaints the terminal line it lands on; MaskSlot strips inside its own
	// renderings, so this closes the segments the seeds did not touch.
	masked = providers.StripSlotControls(masked)
	if masked == text {
		return err
	}
	return maskedSeatError{err: err, text: masked}
}

// maskedSeatError keeps the original error in the Unwrap chain while rendering
// the seat-token-masked text.
type maskedSeatError struct {
	err  error
	text string
}

func (e maskedSeatError) Error() string { return e.text }
func (e maskedSeatError) Unwrap() error { return e.err }

// skippedSeatWarning is the ONE per-seat drift line (stderr through
// RunConfig.WarnOut, and the console copy the CLI banner prints). The seat token
// and the preset name are both file-sourced registry slots, so each renders
// through the registry-slot authority (MaskSeatToken for the token list entry,
// MaskSlot for the name - MaskOperand is the identity on the underscore-head
// credential class). Both also strip the Cc/Cf class, which MaskSlot does
// internally: a control rune in a hand-edited preset must not reach the terminal
// even if it somehow survived the load gate (FL-4).
func skippedSeatWarning(number int, spec, presetName string) string {
	return fmt.Sprintf("seat %d (%s) from preset %q is %s; skipping it",
		number,
		MaskSeatToken(spec),
		providers.MaskSlot(providers.StripSlotControls(presetName)),
		skippedSeatReason)
}

// partitionTargets builds the seat list for PARTITION mode (B2/B3): exactly one
// seat per plan unit, in plan order. A unit that names a Seat is resolved
// through the SAME ParseSeatSpecs grammar the cross-verification path uses (so
// "native:<id>" and "provider/model" mean the same thing here); a unit with no
// Seat gets the run default seat, one rotation-selected target shared by every
// seatless unit so a plan that names no seats is still deterministic.
//
// The three returned slices are parallel: targets[i] is the seat that works on
// units[i], and nativeSeats[i] says whether that seat is a native subprocess.
//
// E2: every refusal error below quotes a plan-authored value (the unit id,
// the seat token), so it rides through maskPlanError before it leaves: these
// errors run before the merge redactor exists and reach the CLI engine-error
// path, which seeds only --seats (empty in --units mode).
func partitionTargets(cfg RunConfig, reg *providers.Registry, state *FanoutState, exclude ExcludeFunc) (targets []providers.Target, nativeSeats []bool, units []Unit, err error) {
	var defaultTarget *providers.Target
	for _, u := range cfg.Units {
		spec := SeatSpec{UnitID: u.ID}
		if strings.TrimSpace(u.Seat) != "" {
			specs, perr := ParseSeatSpecs(u.Seat)
			if perr != nil {
				return nil, nil, nil, maskPlanError(cfg.Units, fmt.Errorf("unit %q: %w", u.ID, perr))
			}
			if len(specs) != 1 {
				// R3-item-3: the plan-authored seat token is quoted here, so it
				// takes the registry-slot authority like every other seat-token
				// echo (MaskOperand is identity on the underscore-head class, and
				// maskPlanError's seeds are SeedEligible-gated).
				return nil, nil, nil, maskPlanError(cfg.Units, fmt.Errorf("unit %q: seat %q must name exactly one seat", u.ID, MaskSeatTokens(u.Seat)))
			}
			spec = specs[0]
			spec.UnitID = u.ID
		}
		if spec.Kind == "" && defaultTarget == nil {
			sel, serr := SelectTargets(reg, state, 1, nil, exclude, cfg.WarnOut)
			if serr != nil {
				return nil, nil, nil, fmt.Errorf("partition default seat selection failed: %w", serr)
			}
			defaultTarget = &sel[0]
		}
		switch {
		case spec.Kind == SeatKindNative:
			// B5/B4-1: a native partition seat has NO code-level scope
			// boundary. It runs real agy at the workspace root and can
			// read/write across units; the per-unit write guard exists only in
			// the provider ToolRunner. Refuse the run instead of blessing an
			// unconfined seat, even when the unit opted into write.
			return nil, nil, nil, maskPlanError(cfg.Units, fmt.Errorf("unit %q: partition mode supports provider seats only; native seats have no per-unit scope boundary and stay refused (also for write units)", u.ID))
		case spec.Kind != "":
			tgt, terr := reg.FindTarget(spec.ModelID)
			if terr != nil {
				// R2-1: the plan-authored seat token rides both halves of this error
				// (the %q operand and the registry error that re-quotes the raw spec),
				// so both go through the registry-slot authority.
				return nil, nil, nil, maskPlanError(cfg.Units, fmt.Errorf("unit %q: failed to resolve model %q: %w", u.ID, MaskSeatToken(spec.ModelID), maskSeatError(terr, spec.ModelID, u.Seat)))
			}
			targets = append(targets, tgt)
			nativeSeats = append(nativeSeats, false)
			// D2=(b): a unit that names an excluded seat keeps that seat (the
			// plan is the user's own instruction), so the pick is honored and
			// the shared warning is the only consequence.
			warnExplicitPick(cfg.WarnOut, exclude, tgt)
		default:
			targets = append(targets, *defaultTarget)
			nativeSeats = append(nativeSeats, false)
		}
		units = append(units, u)
	}
	return targets, nativeSeats, units, nil
}

// unitRunConfig returns the per-unit RunConfig copy a native seat runs with.
// Root stays the workspace root for the read-only slice: the hard boundary for
// a provider seat is the ToolRunner scope filter, and native write-scoping is a
// later held work item. The copy is still taken per unit and threaded from the
// single call site, so that work item lands in one place instead of touching
// every seat construction.
func unitRunConfig(cfg RunConfig, u Unit) RunConfig {
	unitCfg := cfg
	unitCfg.Root = cfg.Root
	if strings.TrimSpace(unitCfg.Root) == "" {
		unitCfg.Root = "."
	}
	return unitCfg
}
