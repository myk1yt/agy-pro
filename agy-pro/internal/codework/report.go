package codework

// V8 run report for the `agy pro code` door: the machine block (JSON) and the
// compact human block (Render) that answer one question - what did this run ask
// for, what ran, what changed, who paid, and how do I undo it.
//
// Rules this file keeps:
//
//   - DETERMINISTIC. No timestamps, no map iteration, no randomness: the JSON is
//     a function of the run's artifacts, so two renders of the same run are
//     byte-identical and a report can be diffed across runs. Artifact paths are
//     relative to the run directory; the one absolute-ish path is the rollback
//     command's target, which is the run directory exactly as the operator
//     named it (a relative path stays relative).
//   - ADDITIVE-ONLY JSON. Every field is a fixed key with an explicit value; the
//     optional blocks use omitempty. A consumer written against an older report
//     keeps working, and nothing is ever keyed by a phase-supplied string. The
//     native-phase drift block (`drift`, `drift_status`) is additive in the same
//     way: `status` keeps the base verdict and the drift token is its own key.
//   - PATHS ARE RELATIVE TO THE RUN DIRECTORY. Artifact paths render through
//     relTo(OutDir, p). The Diff rows are the exception on purpose: they are
//     WORKSPACE paths (root-relative), which is what a reader needs to open the
//     file. Drift rows are workspace paths for the same reason.
//   - MASKED OUTPUT. Render passes every string it prints through a
//     providers.NewRedactor seeded with the run's seed-eligible scope and seat
//     operands, and NewRedactor always applies the generic credential patterns,
//     so a seated model name, a path or a scope entry that carries a pasted key
//     renders masked. The JSON keeps what the engine wrote for its own fields:
//     internal/fanout masks its artifacts (reconcile.go seatRedactor) and the one
//     display string this package imports (the seat model) is masked at harvest.
//   - BOUNDED OUTPUT. Render is a console block: at most maxReportFileRows file
//     rows, maxReportSeatRows seat rows, maxReportDriftNames drift names and one
//     withholding tally line, each list ending in an explicit "... K more". The
//     long lists live in the JSON and in the engine artifacts (<out>/drift.json
//     for the drift rows, manifest.json's `skipped` rows for the withheld ones).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/codesnap"
	"agypro/internal/fanout"
	"agypro/internal/providers"
)

const (
	// maxReportFileRows / maxReportSeatRows / maxReportVerifyRows bound the human
	// block's row lists, and maxReportLines is the block's overall budget. The
	// structural maximum is 1 status + (maxReportVerifyRows verify + 1 more) +
	// (1 files + maxReportFileRows + 1 more) + 1 withheld + (1 seats +
	// maxReportSeatRows + 1 more) + 1 cost + 1 artifacts + 1 rollback = 23, plus
	// the drift block (at most one drift line and one governance line per native
	// phase, so 4, and the standing note, 1) = 28. The budget is pinned by
	// TestReportRenderIsBoundedAndMasksASeatSecret and padded to 29.
	maxReportFileRows   = 5
	maxReportSeatRows   = 5
	maxReportVerifyRows = 3
	maxReportLines      = 29

	// maxReportDriftNames bounds the file names one drift line carries inline; a
	// longer list ends in "+K more" and the full rows stay in the JSON and in
	// <out>/drift.json.
	maxReportDriftNames = 3

	// moreRowsFormat announces an elided tail. Number-first, like codesnap's
	// RenderDeltas ("... K more"), so the number is the first thing read.
	moreRowsFormat = "... %d more"

	// costNote is the standing cost statement the report renders on its `cost:`
	// line: it restates what the phase echo (phases.go echoMetered) told the
	// operator before the run, so the report and the consent gate agree.
	costNote        = "native seats: free Antigravity quota (rate limits); provider seats: metered API; no token/cost cap is enforced"
	noTelemetryNote = "seat telemetry: no summary.json from any phase"
)

// CodeRunReport is the `agy pro code` run report. It is the additive-only JSON
// document (CodeRunReport.JSON) and the source of the compact human block
// (Render).
type CodeRunReport struct {
	Status   string         `json:"status"`
	Phases   []PhaseReport  `json:"phases"`
	Diff     []FileDiffRow  `json:"diff"`
	Verify   []VerifyReport `json:"verify,omitempty"`
	Seats    []SeatReport   `json:"seats"`
	Rollback *RollbackRef   `json:"rollback,omitempty"`
	Cost     CostSummary    `json:"cost"`

	// Withheld are the manifest's secret-class rows: claims the snapshot
	// deliberately did NOT capture (finding S5). They are additive and omitted
	// when empty, and they answer the one question a reader would otherwise have
	// to open manifest.json for - how much of the tree this run's baseline does
	// not cover, and why. The rows carry no bytes, only the recorded path and the
	// engine's own reason string.
	Withheld []WithheldReport `json:"withheld,omitempty"`

	// Drift is what the native phases did to the tree. It is an ADDITIVE block:
	// a consumer that does not know the key keeps reading status/phases/diff,
	// which never change shape because of drift. DriftStatus names the token the
	// human status line appends ("native-drift", or "governance-drift" when the
	// watchlist fired too) and is empty when nothing drifted.
	Drift       []DriftReport `json:"drift,omitempty"`
	DriftStatus string        `json:"drift_status,omitempty"`

	// SeatStatus is the seat-honesty token (finding C3), the drift token's
	// sibling: it is "degraded" when any phase's seats only partly delivered,
	// and empty when every seat reported success. The run keeps its own verdict
	// (a degraded plan never turns a verified write into a failure); the token
	// rides along in the human status line and the seats line leads the block.
	SeatStatus string `json:"seat_status,omitempty"`

	// outDir and seeds are render-time context, deliberately unexported so they
	// never reach the JSON: the absolute run directory would make the document
	// machine-dependent, and the seeds are display-only.
	outDir string
	seeds  []string
}

// DriftReport is one native phase's drift entry: the files that changed during
// the phase and the honest one-liner that says the engine could not have stopped
// them. GovernanceRows are the watchlist files, kept separate because a
// governance/credential change is a different finding from an in-scope edit.
type DriftReport struct {
	Phase      string        `json:"phase"`
	Files      []FileDiffRow `json:"files,omitempty"`
	Governance []FileDiffRow `json:"governance_files,omitempty"`
	Note       string        `json:"note,omitempty"`
}

// PhaseReport is one phase's line in the report.
type PhaseReport struct {
	Phase     string   `json:"phase"`
	Status    string   `json:"status"`
	Ran       bool     `json:"ran"`
	ExitCode  int      `json:"exit_code"`
	Runs      []string `json:"runs,omitempty"`      // fanout run directories, relative to OutDir
	Artifacts []string `json:"artifacts,omitempty"` // files this phase produced, relative to OutDir

	// SeatsTotal / SeatsFailed are the phase's seat tally (phases.go
	// phaseSeatTally): how many seats the engine reported and how many did not
	// report success. They are the evidence behind a `degraded` phase status and
	// are omitted when the phase wrote no summary.
	SeatsTotal  int `json:"seats_total,omitempty"`
	SeatsFailed int `json:"seats_failed,omitempty"`

	// Drift / GovernanceDrift / DriftNote are the native-phase observation for
	// this phase (see internal/codework/phases.go finishNativePhase). They are
	// empty for a phase that changed nothing and for the provider write phase.
	Drift           []FileDiffRow `json:"drift,omitempty"`
	GovernanceDrift []FileDiffRow `json:"governance_drift,omitempty"`
	DriftNote       string        `json:"drift_note,omitempty"`
}

// FileDiffRow is one changed file. Path is root-relative (a workspace path, not
// a run-directory path).
type FileDiffRow struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// WithheldReport is one secret-class file the snapshot refused to copy (finding
// S5, codesnap's ReasonSecretClass rows). Path is root-relative, exactly as
// manifest.json records it, so a reader can match the row against the ledger;
// Reason is the engine's own string ("secret-class (<family>): withheld from the
// snapshot, no bytes copied").
//
// The human block prints the COUNT only, never these paths: a secret-class file
// name is the one string this line must not spread any wider than the run
// directory already holds it.
type WithheldReport struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

// VerifyReport is one verification command's outcome inside the write phase. The
// report carries one row per run (see maxReportVerifyRows) because a run can
// name several verifiers, and "which one failed" is the first thing a reader
// asks; Attempt names the write attempt that produced the row.
type VerifyReport struct {
	Attempt  int      `json:"attempt"`
	Argv     []string `json:"argv"`
	Passed   bool     `json:"passed"`
	ExitCode int      `json:"exit_code"`
	Log      string   `json:"log,omitempty"`

	// Skipped names why a verifier never ran, with the token
	// verifySkippedBudgetToken for the only reason that exists today: the run's
	// verification wall budget was spent (finding S1c). An empty field means the
	// command ran. It is additive and omitted, so a normal row is unchanged.
	Skipped string `json:"skipped,omitempty"`
}

// SeatReport is one seat of one phase. The token fields are pointers and are
// omitted when the seat reported nothing: "no usage reported" must never render
// as "zero tokens spent" (the fanout SeatUsage rule, worker.go:47-54).
type SeatReport struct {
	ID               int    `json:"id"`
	Kind             string `json:"kind"`
	Model            string `json:"model"`
	Phase            string `json:"phase"`
	Status           string `json:"status"`
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
	CachedTokens     *int64 `json:"cached_tokens,omitempty"`
}

// RollbackRef is the undo recipe for a run that wrote to the tree.
type RollbackRef struct {
	Command string `json:"command"`
	RunDir  string `json:"run_dir"`
}

// CostSummary is the run-level spend shape. It names the seat mix and the cost
// rule; it never invents a dollar figure, because the engine has no price table
// for a user-editable registry.
//
// The counts are seat OCCURRENCES, not distinct seats: a seat row is one seat in
// one phase's fanout run, so the default native line-up used by both the plan and
// the review phase is four occurrences of two models. The field names say so
// (finding: cardinality was ambiguous), and UniqueModels answers the other
// question - how many different models this run seated - so neither number has to
// be inferred from the other.
type CostSummary struct {
	NativeSeatOccurrences   int    `json:"native_seat_occurrences"`
	ProviderSeatOccurrences int    `json:"provider_seat_occurrences"`
	UniqueModels            int    `json:"unique_seat_models"`
	Note                    string `json:"note"`
}

// verifySkippedBudgetToken is the row token for a verifier that never ran because
// the run's verification budget was spent. It is a TOKEN, not a status change:
// the write phase still reports `verify-failed`, and the row says why no verdict
// exists (finding S1c / FIX-B handoff item 5).
const verifySkippedBudgetToken = "verify-skipped-budget"

// AssembleReport builds the run report from what the phases returned. phases
// carries every *PhaseResult the verb collected (plan, review, rollback); write
// carries the write phase's own result. Reports are emitted in the fixed order
// plan, write, review, rollback so two runs of the same shape produce the same
// document.
//
// Seat telemetry is harvested from each run directory's summary.json when one
// exists (LoadFanoutSummary). A phase that produced no summary contributes no
// seats; the cost note says so instead of rendering an empty table as "no spend".
func AssembleReport(cfg CodeConfig, phases []*PhaseResult, write *WritePhaseResult) *CodeRunReport {
	r := &CodeRunReport{
		Phases: []PhaseReport{},
		Diff:   []FileDiffRow{},
		Seats:  []SeatReport{},
		Cost:   CostSummary{Note: costNote},
		outDir: cfg.OutDir,
		seeds:  cfg.reportSeeds(),
	}
	byPhase := make(map[string]*PhaseResult, len(phases))
	for _, p := range phases {
		if p != nil {
			byPhase[p.Phase] = p
		}
	}

	if p := byPhase[PhasePlan]; p != nil {
		r.Phases = append(r.Phases, r.phaseReport(p))
		r.harvest(PhasePlan, []string{p.OutDir})
	}
	if write != nil {
		r.Phases = append(r.Phases, r.writeReport(write))
		r.harvest(PhaseWrite, write.FanoutRuns)
	}
	if p := byPhase[PhaseReview]; p != nil {
		r.Phases = append(r.Phases, r.phaseReport(p))
		r.harvest(PhaseReview, []string{p.OutDir})
	}
	if p := byPhase[PhaseRollback]; p != nil {
		r.Phases = append(r.Phases, r.phaseReport(p))
	}

	if write != nil {
		r.Verify = r.verifyReports(write)
		for _, d := range write.Diff {
			r.Diff = append(r.Diff, FileDiffRow{Path: d.Path, Status: d.Status, Added: d.Added, Removed: d.Removed})
		}
	}
	r.harvestWithheld(byPhase, write)

	r.Status = deriveStatus(byPhase, write, r.Phases)
	r.Drift, r.DriftStatus = driftReports(r.Phases)
	r.SeatStatus = seatToken(r.Phases)
	models := make(map[string]bool, len(r.Seats))
	for _, s := range r.Seats {
		if s.Model != "" {
			models[s.Model] = true
		}
		if s.Kind == string(fanout.SeatKindProvider) {
			r.Cost.ProviderSeatOccurrences++
		} else {
			r.Cost.NativeSeatOccurrences++
		}
	}
	r.Cost.UniqueModels = len(models)
	if len(r.Seats) == 0 {
		r.Cost.Note = costNote + "; " + noTelemetryNote
	}
	if r.rollbackSuggested() {
		r.Rollback = &RollbackRef{
			// The path is quoted when it carries whitespace: the command is meant to
			// be copied back into the operator's shell, and `--run reports/code/my
			// run` would split into two operands (finding: unquoted hint).
			Command: providers.MaskOperand(fmt.Sprintf(rollbackHintFmt, quoteRunDir(cfg.OutDir))),
			// The run directory is named exactly as the caller passed it: it is
			// what the rollback command needs, and artifact paths inside it are
			// relative (relTo) so the document stays machine-independent.
			RunDir: providers.MaskOperand(filepath.Clean(cfg.OutDir)),
		}
	}
	return r
}

// quoteRunDir renders a run directory the way the rollback command needs it: bare
// when it carries no whitespace (so the pinned `--run reports/code/<ts>` shape is
// unchanged), double-quoted when it does. A path that also contains a double quote
// has it doubled, which is the cmd.exe and PowerShell convention; bash would read
// that differently, and a path with both a space and a quote is not a shape the
// verb can produce from its own default.
func quoteRunDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if !strings.ContainsAny(dir, " \t") {
		return dir
	}
	return `"` + strings.ReplaceAll(dir, `"`, `""`) + `"`
}

// harvestWithheld collects the manifest rows the snapshot withheld because their
// name is in the secret class (codesnap.ReasonSecretClass, finding S5). The plan
// phase's manifest and the write phase's are the SAME document in a composed run
// (the write reuses the plan's baseline) and two separate ones in the standalone
// paths, so rows are deduped by path and keep the order each manifest recorded
// (codesnap sorts its skip rows before writing the ledger).
func (r *CodeRunReport) harvestWithheld(byPhase map[string]*PhaseResult, write *WritePhaseResult) {
	manifests := make([]*codesnap.Manifest, 0, 2)
	if p := byPhase[PhasePlan]; p != nil {
		manifests = append(manifests, p.Manifest)
	}
	if write != nil {
		manifests = append(manifests, write.Manifest)
	}
	seen := make(map[string]bool)
	for _, m := range manifests {
		if m == nil {
			continue
		}
		for _, skip := range m.Skipped {
			if !strings.HasPrefix(skip.Reason, codesnap.ReasonSecretClass) || seen[skip.Path] {
				continue
			}
			seen[skip.Path] = true
			r.Withheld = append(r.Withheld, WithheldReport{Path: skip.Path, Reason: skip.Reason})
		}
	}
}

// phaseReport converts a read-only phase result.
func (r *CodeRunReport) phaseReport(p *PhaseResult) PhaseReport {
	out := PhaseReport{
		Phase:           p.Phase,
		Status:          p.Status,
		Ran:             p.Ran,
		ExitCode:        p.ExitCode,
		SeatsTotal:      p.SeatsTotal,
		SeatsFailed:     p.SeatsFailed,
		Drift:           diffRows(p.Drift),
		GovernanceDrift: diffRows(p.GovernanceDrift),
		DriftNote:       p.DriftNote,
	}
	for _, a := range p.Artifacts {
		out.Artifacts = append(out.Artifacts, relTo(r.outDir, a))
	}
	if p.OutDir != "" {
		out.Runs = []string{relTo(r.outDir, p.OutDir)}
	}
	return out
}

// diffRows converts codesnap deltas into report rows. The paths stay
// workspace-relative: a drift row names the file a reader must open.
func diffRows(deltas []codesnap.FileDelta) []FileDiffRow {
	if len(deltas) == 0 {
		return nil
	}
	rows := make([]FileDiffRow, 0, len(deltas))
	for _, d := range deltas {
		rows = append(rows, FileDiffRow{Path: d.Path, Status: d.Status, Added: d.Added, Removed: d.Removed})
	}
	return rows
}

// driftReports collects the per-phase drift rows into the run-level block (in
// the report's fixed phase order) and names the status token the human line
// appends. A governance violation outranks an in-scope edit: both are reported,
// but the token names the worse one.
func driftReports(phases []PhaseReport) ([]DriftReport, string) {
	out := make([]DriftReport, 0, len(phases))
	status := ""
	for _, p := range phases {
		if len(p.Drift) == 0 && len(p.GovernanceDrift) == 0 {
			continue
		}
		out = append(out, DriftReport{
			Phase:      p.Phase,
			Files:      p.Drift,
			Governance: p.GovernanceDrift,
			Note:       p.DriftNote,
		})
		if status == "" || len(p.GovernanceDrift) > 0 {
			status = StatusNativeDrift
			if len(p.GovernanceDrift) > 0 {
				status = StatusGovernanceDrift
			}
		}
	}
	return out, status
}

// seatToken names the run-level seat-honesty token (finding C3). It is derived
// from the phase statuses, exactly as driftReports derives the drift token, so the
// token and the phase rows cannot disagree: one phase whose seats only partly
// delivered makes the run `(degraded)`.
func seatToken(phases []PhaseReport) string {
	for _, p := range phases {
		if p.Status == StatusDegraded {
			return StatusDegraded
		}
	}
	return ""
}

// writeReport converts the write phase result. Every fanout run of the phase
// (the initial write and each repair) is listed, so the merge artifacts of the
// attempts that were superseded stay reachable.
func (r *CodeRunReport) writeReport(w *WritePhaseResult) PhaseReport {
	out := PhaseReport{
		Phase:    PhaseWrite,
		Status:   w.Status,
		Ran:      len(w.FanoutRuns) > 0,
		ExitCode: w.ExitCode,
	}
	for _, dir := range w.FanoutRuns {
		out.Runs = append(out.Runs, relTo(r.outDir, dir))
	}
	if w.PlanPath != "" {
		out.Artifacts = append(out.Artifacts, relTo(r.outDir, w.PlanPath))
	}
	if w.Manifest != nil {
		out.Artifacts = append(out.Artifacts, relTo(r.outDir, filepath.Join(r.outDir, "manifest.json")))
	}
	return out
}

// verifyReports converts the write phase's verification runs. VerifyRuns is the
// primary source (every command of every attempt, in execution order). A
// caller-built WritePhaseResult that only filled the decisive Verify still
// produces one row, so "verified" can never render as an empty block.
func (r *CodeRunReport) verifyReports(w *WritePhaseResult) []VerifyReport {
	if len(w.VerifyRuns) == 0 {
		if !w.Verified {
			return nil
		}
		row := VerifyReport{
			Attempt:  w.Attempts,
			Argv:     append([]string(nil), w.Verify.Argv...),
			Passed:   w.Verify.Passed,
			ExitCode: w.Verify.ExitCode,
			Log:      relTo(r.outDir, w.Verify.LogPath),
		}
		if budgetSkipped(w.Verify) {
			row.Skipped = verifySkippedBudgetToken
		}
		return []VerifyReport{row}
	}
	out := make([]VerifyReport, 0, len(w.VerifyRuns))
	for _, run := range w.VerifyRuns {
		row := VerifyReport{
			Attempt:  run.Attempt,
			Argv:     append([]string(nil), run.Argv...),
			Passed:   run.Passed,
			ExitCode: run.ExitCode,
			Log:      relTo(r.outDir, run.LogPath),
		}
		if budgetSkipped(run.VerifyResult) {
			row.Skipped = verifySkippedBudgetToken
		}
		out = append(out, row)
	}
	return out
}

// harvest appends the seats of every summary.json found in runDirs.
func (r *CodeRunReport) harvest(phase string, runDirs []string) {
	for _, dir := range runDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		summary, err := LoadFanoutSummary(filepath.Join(dir, summaryFileName))
		if err != nil {
			continue // no summary = no telemetry for this phase (see the cost note)
		}
		for _, s := range summary.Seats {
			r.Seats = append(r.Seats, SeatReport{
				ID:   s.SeatID,
				Kind: string(s.Kind),
				// The model id comes from a user-editable registry and is display
				// text in both outputs, so it is masked at harvest: neither the
				// JSON nor the render can carry a key-shaped model name.
				// L-3: a model id is a registry slot, so it takes the registry-slot
				// authority even though the harvest input is already report-masked;
				// a hand-forged summary.json is the residual this closes.
				Model:            providers.MaskSlot(providers.StripSlotControls(seatModel(s))),
				Phase:            phase,
				Status:           s.Status,
				PromptTokens:     tokenCount(s, func(u *fanout.SeatUsage) *int64 { return u.PromptTokens }),
				CompletionTokens: tokenCount(s, func(u *fanout.SeatUsage) *int64 { return u.CompletionTokens }),
				CachedTokens:     tokenCount(s, func(u *fanout.SeatUsage) *int64 { return u.CachedTokens }),
			})
		}
	}
}

// seatModel renders the model a seat actually finished on. The final target is
// the one that produced the answer (a failover changes it), so the report names
// that, not the initial pick.
func seatModel(s fanout.SeatResult) string {
	if id := strings.TrimSpace(s.FinalTarget.Model.ID); id != "" {
		return id
	}
	if full := strings.TrimSpace(s.FinalTarget.FullName()); full != "" {
		return full
	}
	return strings.TrimSpace(s.InitialTarget.Model.ID)
}

// tokenCount reads one usage field without inventing a zero.
func tokenCount(s fanout.SeatResult, pick func(*fanout.SeatUsage) *int64) *int64 {
	if s.Usage == nil {
		return nil
	}
	return pick(s.Usage)
}

// deriveStatus is the run's verdict: a completed rollback wins (the tree is back
// to its recorded state, whatever went wrong before), then the first phase that
// did not end ok, then the write phase's own status, then ok. Phases are read in
// the report's fixed order, so the verdict is deterministic.
//
// A `degraded` phase is a QUALIFICATION, not a verdict (finding C3): it is
// skipped like a dry run, the run keeps the verdict its other phases earned, and
// the SeatStatus token names the partial seat failure. A run made only of
// degraded phases has no better verdict to report, so it returns `degraded`
// itself (and the status line then carries no redundant suffix).
func deriveStatus(byPhase map[string]*PhaseResult, write *WritePhaseResult, reported []PhaseReport) string {
	if p := byPhase[PhaseRollback]; p != nil && p.Status == StatusRolledBack {
		return StatusRolledBack
	}
	sawOK, sawDegraded := false, false
	for _, p := range reported {
		switch p.Status {
		case StatusOK:
			sawOK = true
		case StatusDryRun:
			// A dry run is not a verdict; keep looking.
		case StatusDegraded:
			sawDegraded = true
		default:
			return p.Status
		}
	}
	if write != nil {
		return write.Status
	}
	if sawOK {
		return StatusOK
	}
	if sawDegraded {
		return StatusDegraded
	}
	return StatusDryRun
}

// rollbackSuggested reports whether the report should name the rollback command:
// the write phase actually ran (there is something to undo), and the verdict is
// not one where the tree is already correct or was never touched. A plan-only
// failure, a dry run, a consent refusal and a completed rollback never carry it.
func (r *CodeRunReport) rollbackSuggested() bool {
	if r == nil || !r.wrote() {
		return false
	}
	switch r.Status {
	case StatusOK, StatusDryRun, StatusRolledBack, StatusConsentRequired:
		return false
	default:
		return true
	}
}

// wrote reports whether a write phase reached the tree, i.e. whether the phase
// list carries a write phase that ran.
func (r *CodeRunReport) wrote() bool {
	for _, p := range r.Phases {
		if p.Phase == PhaseWrite && p.Ran {
			return true
		}
	}
	return false
}

// LoadFanoutSummary reads one phase's summary.json. It is the only way seat
// telemetry enters the report, so a missing or unparsable summary is an error
// here and the caller decides (harvest treats it as "no telemetry").
func LoadFanoutSummary(path string) (*fanout.SummaryReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("codework: report: cannot read %q: %w", providers.MaskOperand(filepath.ToSlash(path)), err)
	}
	var summary fanout.SummaryReport
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("codework: report: %q is not a fanout summary: %w", providers.MaskOperand(filepath.ToSlash(path)), err)
	}
	return &summary, nil
}

// JSON renders the machine block: indented, trailing newline, byte-stable for
// the same run.
func (r *CodeRunReport) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("codework: report: cannot marshal the run report: %w", err)
	}
	return append(data, '\n'), nil
}

// Render renders the compact human block (at most maxReportLines lines): the
// status, the verification verdict, the changed-file rows, the withholding tally
// when the snapshot refused to copy a secret-class file, the seat rows with
// their cost note, the artifact directory, and - on a non-success verdict - the
// exact rollback command.
//
// A run whose seats did not all deliver LEADS with its seat tally (finding C3):
// a rate-limited review seat is invisible in a block that opens with `ok`, so the
// seats line moves above the status line and names the degraded/failed count.
// Every other run keeps the original order.
func (r *CodeRunReport) Render() string {
	if r == nil {
		return ""
	}
	lines := make([]string, 0, maxReportFileRows+maxReportSeatRows+6)
	seatHeader := r.seatHeaderLine()
	hoisted := seatHeader != "" && r.seatFailures() > 0
	if hoisted {
		lines = append(lines, r.mask(seatHeader))
	}
	lines = append(lines, r.mask(fmt.Sprintf("agy pro code: %s", r.statusLine())))

	switch {
	case len(r.Verify) > 0:
		for i, run := range r.Verify {
			if i == maxReportVerifyRows {
				lines = append(lines, r.mask(fmt.Sprintf("  "+moreRowsFormat, len(r.Verify)-maxReportVerifyRows)))
				break
			}
			verdict := "FAIL"
			if run.Passed {
				verdict = "PASS"
			}
			line := fmt.Sprintf("verify: %s %s attempt=%d", formatArgv(run.Argv), verdict, run.Attempt)
			if run.Log != "" {
				line += " log=" + run.Log
			}
			if run.Skipped != "" {
				line += " skipped=" + run.Skipped
			}
			lines = append(lines, r.mask(line))
		}
	case r.Status == StatusUnverified:
		lines = append(lines, "verify: none detected (the write was not verified)")
	}

	lines = append(lines, r.mask(diffSummaryLine(r.Diff)))
	for i, row := range r.Diff {
		if i == maxReportFileRows {
			lines = append(lines, r.mask(fmt.Sprintf("  "+moreRowsFormat, len(r.Diff)-maxReportFileRows)))
			break
		}
		lines = append(lines, r.mask(fmt.Sprintf("  %c %s +%d -%d", fileDeltaLetter(row.Status), row.Path, row.Added, row.Removed)))
	}

	// The withholding tally sits right under the change rows: it explains a gap in
	// the baseline the rows above were measured against, before the drift block
	// says what a native seat did.
	if line := r.withheldLine(); line != "" {
		lines = append(lines, r.mask(line))
	}

	// The drift block sits between the change rows and the seat rows: a reader
	// sees what the run claims to have changed, then what a native seat changed
	// that the engine could not stop.
	lines = append(lines, r.driftLines()...)

	if seatHeader != "" {
		if !hoisted {
			lines = append(lines, r.mask(seatHeader))
		}
		for i, seat := range r.Seats {
			if i == maxReportSeatRows {
				lines = append(lines, r.mask(fmt.Sprintf("  "+moreRowsFormat, len(r.Seats)-maxReportSeatRows)))
				break
			}
			lines = append(lines, r.mask(seatLine(seat)))
		}
	}
	lines = append(lines, r.mask("cost: "+r.Cost.Note))

	if dir := strings.TrimSpace(r.outDir); dir != "" {
		lines = append(lines, r.mask("artifacts: "+filepath.ToSlash(dir)))
	}
	if r.Rollback != nil {
		lines = append(lines, r.mask("rollback: "+r.Rollback.Command))
	}
	return strings.Join(lines, "\n")
}

// statusLine renders the verdict plus the qualification tokens. A run that
// drifted is still `ok` or `verify-failed` - the drift qualifies the result
// instead of replacing it - so the token is appended in parentheses, and the
// seat token rides in the same list. A token equal to the status itself (a run
// made only of degraded phases) is not repeated.
func (r *CodeRunReport) statusLine() string {
	if r == nil {
		return ""
	}
	tokens := r.statusTokens()
	if len(tokens) == 0 {
		return r.Status
	}
	return r.Status + " (" + strings.Join(tokens, ", ") + ")"
}

// statusTokens lists the qualification tokens the status line appends, in a fixed
// order: the seat token, then the drift token.
func (r *CodeRunReport) statusTokens() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, 2)
	for _, token := range []string{r.SeatStatus, r.DriftStatus} {
		token = strings.TrimSpace(token)
		if token == "" || token == r.Status {
			continue
		}
		out = append(out, token)
	}
	return out
}

// seatHeaderLine renders the seat tally header: the row count, the kind split and
// the distinct-model count, plus - when any seat did not deliver - the token and
// the failed count. An empty string means the run harvested no seat rows.
func (r *CodeRunReport) seatHeaderLine() string {
	if r == nil || len(r.Seats) == 0 {
		return ""
	}
	line := fmt.Sprintf("seats: %d seat occurrence(s) (%d native, %d provider, %d unique model(s))",
		len(r.Seats), r.Cost.NativeSeatOccurrences, r.Cost.ProviderSeatOccurrences, r.Cost.UniqueModels)
	if failed := r.seatFailures(); failed > 0 {
		token := StatusDegraded
		if failed == len(r.Seats) {
			token = StatusFailed
		}
		line += fmt.Sprintf(" - %s: %d of %d seat(s) did not report success", token, failed, len(r.Seats))
	}
	return line
}

// seatFailures counts the harvested seat rows that did not report success.
func (r *CodeRunReport) seatFailures() int {
	if r == nil {
		return 0
	}
	failed := 0
	for _, seat := range r.Seats {
		if !seatSucceeded(seat.Status) {
			failed++
		}
	}
	return failed
}

// driftLines renders the native-phase drift block: one line per drifting phase,
// one line per watchlist violation, and the standing note. It is bounded by
// construction: at most two native phases, and maxReportDriftNames names per
// line. The full rows are in the JSON and in <out>/drift.json.
func (r *CodeRunReport) driftLines() []string {
	if r == nil || len(r.Drift) == 0 {
		return nil
	}
	lines := make([]string, 0, 2*len(r.Drift)+1)
	for _, d := range r.Drift {
		if len(d.Files) > 0 {
			lines = append(lines, r.mask(fmt.Sprintf("drift: %s changed %d file(s) (%s)", d.Phase, len(d.Files), driftNames(d.Files))))
		}
		if len(d.Governance) > 0 {
			lines = append(lines, r.mask(fmt.Sprintf("governance-drift: %s changed %d governance file(s) (%s)", d.Phase, len(d.Governance), driftNames(d.Governance))))
		}
	}
	lines = append(lines, r.mask("note: "+driftNote))
	return lines
}

// driftNames renders up to maxReportDriftNames rows inline, then an explicit
// "+K more" tail.
func driftNames(rows []FileDiffRow) string {
	parts := make([]string, 0, maxReportDriftNames+1)
	for i, row := range rows {
		if i == maxReportDriftNames {
			parts = append(parts, fmt.Sprintf("+%d more", len(rows)-maxReportDriftNames))
			break
		}
		parts = append(parts, fmt.Sprintf("%c %s", fileDeltaLetter(row.Status), row.Path))
	}
	return strings.Join(parts, ", ")
}

// withheldLine renders the one-line withholding tally (finding S5): the COUNT
// only, because the paths are exactly what the snapshot declined to copy. It is
// one line at most, and empty - no line at all - when the manifest withheld
// nothing, so a run over a tree with no secret-class files renders as before.
func (r *CodeRunReport) withheldLine() string {
	if r == nil || len(r.Withheld) == 0 {
		return ""
	}
	return fmt.Sprintf("withheld: %d secret-class file(s) not snapshotted", len(r.Withheld))
}

// diffSummaryLine renders the aggregate file line: `files: 3 changed (1 created,
// 2 modified) +42 -7`. An empty delta list says so instead of rendering zeroes.
func diffSummaryLine(rows []FileDiffRow) string {
	if len(rows) == 0 {
		return "files: none changed"
	}
	created, modified, deleted, added, removed := 0, 0, 0, 0, 0
	for _, row := range rows {
		switch row.Status {
		case codesnap.StatusCreated:
			created++
		case codesnap.StatusDeleted:
			deleted++
		default:
			modified++
		}
		added += row.Added
		removed += row.Removed
	}
	parts := []string{fmt.Sprintf("%d created", created), fmt.Sprintf("%d modified", modified)}
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", deleted))
	}
	return fmt.Sprintf("files: %d changed (%s) +%d -%d", len(rows), strings.Join(parts, ", "), added, removed)
}

// seatLine renders one seat row: phase, kind, model, status and the cost note.
func seatLine(s SeatReport) string {
	note := freeQuotaNote
	if s.Kind == string(fanout.SeatKindProvider) {
		note = meteredAPINote
		if s.PromptTokens != nil || s.CompletionTokens != nil {
			note += fmt.Sprintf(", %s prompt / %s completion tokens", tokenOrDash(s.PromptTokens), tokenOrDash(s.CompletionTokens))
		}
	}
	return fmt.Sprintf("  %-8s [%s] %s  %s  (%s)", s.Phase, s.Kind, s.Model, s.Status, note)
}

// fileDeltaLetter maps a codesnap status to the one-character marker the report
// prints, mirroring codesnap's deltaLetter (diff.go:171), which is unexported. An
// unknown status renders "M": a delta a caller invented is still a delta.
func fileDeltaLetter(status string) byte {
	switch status {
	case codesnap.StatusCreated:
		return 'A'
	case codesnap.StatusDeleted:
		return 'D'
	default:
		return 'M'
	}
}

// tokenOrDash renders an optional count without inventing a zero.
func tokenOrDash(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

// mask applies the run's redaction discipline: a NewRedactor seeded with the
// scope and seat operands that could be echoed here, plus the process-wide
// credential patterns (providers.NewRedactor always applies those). Seeding is
// gated by providers.SeedEligible, the same rule MaskSentence uses, so a benign
// operand is never swapped out of the text: seeding a path like
// "internal/alpha/a.go" verbatim would turn the diff row that names it into
// "[REDACTED]". MaskOperand is deliberately NOT used on a whole line here - its
// display authority truncates a line to "first-5+****" as soon as the line holds
// one 16-rune alphanumeric run, which a temp directory name is.
func (r *CodeRunReport) mask(s string) string {
	if r == nil {
		return s
	}
	seeds := make([]string, 0, len(r.seeds)*2)
	for _, op := range r.seeds {
		if !providers.SeedEligible(op) {
			continue
		}
		seeds = append(seeds, op, strings.TrimSpace(op))
	}
	return providers.NewRedactor(seeds...)(s)
}

// reportSeeds is the redaction seed list for one run: every operand the renderer
// can quote (scope entries, seat specs, the declared writer).
func (c CodeConfig) reportSeeds() []string {
	seeds := make([]string, 0, len(c.Scope)+len(c.Seats)+len(c.PlanSeats)+len(c.ReviewSeats)+1)
	seeds = append(seeds, c.Scope...)
	seeds = append(seeds, c.Seats...)
	seeds = append(seeds, c.PlanSeats...)
	seeds = append(seeds, c.ReviewSeats...)
	if w := strings.TrimSpace(c.WriterSeat); w != "" {
		seeds = append(seeds, w)
	}
	return seeds
}
