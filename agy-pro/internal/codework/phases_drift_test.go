package codework

// U9 pins: a native seat is a real `agy` subprocess with its own tool surface, so
// the engine cannot contain it (internal/fanout/engine.go:913-916 builds no
// ToolRunner for a native seat). These tests hold the containment-by-observation
// contract instead:
//
//	(a) the snapshot happens BEFORE the plan phase when a write follows;
//	(b) a write during the plan phase lands in PhaseReport.Drift, the human block,
//	    drift.json and the appended `native-drift` status token;
//	(c) the write phase reuses that baseline instead of re-taking one;
//	(d) the plan/review asks carry the fixed read-only instruction (the belt);
//	(e) an out-of-scope governance/credential write is caught by the watchlist and
//	    reported as `governance-drift`;
//	(f) a rollback after plan-phase drift restores the PRE-plan bytes - the exact
//	    E2E failure (e2e-code-demo.md, run A) this unit exists to make impossible.
//
// The drift tests that assert real bytes use the REAL codesnap seams and replace
// only RunFanout, so the snapshot, the diff and the rollback are the shipped
// implementations.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
)

// driftFanoutLog is the call log of a test that keeps the real codesnap seams.
type driftFanoutLog struct {
	calls [][]string
	out   strings.Builder
}

// installRealSeamFanout replaces ONLY the fanout seam (plus the phase streams),
// so TakeSnapshot / ComputeDiff / RollbackTree stay the shipped codesnap code.
// hook runs inside the fake fanout call, which is how a test plants what a real
// seat does to the tree.
func installRealSeamFanout(t *testing.T, hook func(call int, argv []string)) *driftFanoutLog {
	t.Helper()
	prevFanout, prevIn, prevOut, prevErr := RunFanout, PhaseStdin, PhaseStdout, PhaseStderr
	log := &driftFanoutLog{}
	RunFanout = func(args []string, _ io.Reader, _, _ io.Writer) int {
		log.calls = append(log.calls, append([]string(nil), args...))
		if hook != nil {
			hook(len(log.calls)-1, args)
		}
		return 0
	}
	PhaseStdin = strings.NewReader("")
	PhaseStdout = &log.out
	PhaseStderr = &log.out
	t.Cleanup(func() {
		RunFanout, PhaseStdin, PhaseStdout, PhaseStderr = prevFanout, prevIn, prevOut, prevErr
	})
	return log
}

// sha256Of is the digest form the manifest records.
func sha256Of(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// driftPlanConfig is the plan phase of a run that continues into a write.
func driftPlanConfig(root, outDir string) CodeConfig {
	return CodeConfig{
		Root:          root,
		OutDir:        outDir,
		Ask:           "audit the alpha request",
		Scope:         []string{"internal/alpha/*.go"},
		PlanSeats:     []string{"native:m1"},
		SnapshotFirst: true,
	}
}

// TestPlanPhaseSnapshotsBeforeTheSeatsWhenAWriteFollows is pin (a): with
// SnapshotFirst the FIRST seam call of the plan phase is the snapshot, before any
// fanout call can reach a seat.
func TestPlanPhaseSnapshotsBeforeTheSeatsWhenAWriteFollows(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)

	res, err := PlanPhase(driftPlanConfig(root, outDir))
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	if len(rec.calls) == 0 || rec.calls[0] != "snapshot" {
		t.Fatalf("calls = %v, want the snapshot first (a seat must not run before the baseline exists)", rec.calls)
	}
	snap := phaseCallIndex(rec.calls, "snapshot")
	fanout := phaseCallIndex(rec.calls, "fanout")
	if snap < 0 || fanout < 0 || snap > fanout {
		t.Fatalf("calls = %v, want snapshot before fanout", rec.calls)
	}
	if rec.fanoutCalls == nil || !strings.Contains(strings.Join(rec.fanoutCalls[0], " "), "--task plan-audit") {
		t.Fatalf("argv = %v, want the plan-audit vector", rec.fanoutCalls)
	}
	if res.Manifest == nil {
		t.Fatal("res.Manifest is nil: the snapshot the plan phase took is not handed to the write phase")
	}
	if len(res.Manifest.Scopes) != 1 || res.Manifest.Scopes[0] != "internal/alpha/*.go" {
		t.Errorf("manifest scopes = %v, want the run's scope", res.Manifest.Scopes)
	}

	// A plan phase with no write behind it takes no snapshot at all: the
	// standalone `code plan` door has nothing to baseline.
	rec2 := installPhaseRecorder(t, nil)
	cfg := driftPlanConfig(root, filepath.Join(t.TempDir(), "plain"))
	cfg.SnapshotFirst = false
	if _, perr := PlanPhase(cfg); perr != nil {
		t.Fatalf("PlanPhase (no write follows): %v", perr)
	}
	if phaseCallIndex(rec2.calls, "snapshot") >= 0 {
		t.Fatalf("calls = %v, a run with no write phase took a snapshot", rec2.calls)
	}
}

// TestPlanDriftIsDetectedReportedAndLedgered is pin (b), on real codesnap seams:
// a seat that edits a scoped file during the plan phase is named in
// PhaseReport.Drift, in the human block, in drift.json and in the status token,
// and the snapshot still holds the PRE-plan bytes.
func TestPlanDriftIsDetectedReportedAndLedgered(t *testing.T) {
	root, outDir := phaseTree(t)
	target := filepath.Join(root, "internal", "alpha", "a.go")
	original := []byte("package alpha\n")
	edited := []byte("package alpha // the seat rewrote this during the plan phase\n")

	log := installRealSeamFanout(t, func(call int, _ []string) {
		if call != 0 {
			return
		}
		if werr := os.WriteFile(target, edited, 0o644); werr != nil {
			t.Errorf("plant the drift: %v", werr)
		}
	})

	cfg := driftPlanConfig(root, outDir)
	res, err := PlanPhase(cfg)
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	if len(log.calls) != 1 {
		t.Fatalf("fanout calls = %d, want 1", len(log.calls))
	}

	// The snapshot recorded the PRE-plan bytes: this is the E2E defect's root.
	manifest, merr := codesnap.LoadManifest(outDir)
	if merr != nil {
		t.Fatalf("manifest.json: %v", merr)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("manifest rows = %+v, want the one scoped file", manifest.Files)
	}
	if got := manifest.Files[0].SHA256; got != sha256Of(original) {
		t.Fatalf("manifest sha256 = %s, want the pre-plan bytes %s (a post-mutation baseline is the defect)", got, sha256Of(original))
	}

	// PhaseResult carries the drift.
	if len(res.Drift) != 1 {
		t.Fatalf("drift = %+v, want the one changed file", res.Drift)
	}
	if res.Drift[0].Path != "internal/alpha/a.go" || res.Drift[0].Status != codesnap.StatusModified {
		t.Errorf("drift row = %+v, want a modified internal/alpha/a.go", res.Drift[0])
	}
	if res.DriftNote != driftNote {
		t.Errorf("drift note = %q, want the pinned honest note", res.DriftNote)
	}
	if !strings.Contains(log.out.String(), "native-drift: plan") {
		t.Errorf("phase stream = %q, want the native-drift finding", log.out.String())
	}

	// drift.json names the same row.
	ledger, lerr := os.ReadFile(filepath.Join(outDir, driftFileName))
	if lerr != nil {
		t.Fatalf("drift.json: %v", lerr)
	}
	var doc driftDocument
	if uerr := json.Unmarshal(ledger, &doc); uerr != nil {
		t.Fatalf("drift.json is not the documented schema: %v", uerr)
	}
	if doc.Version != driftVersion || doc.Note != driftNote {
		t.Errorf("drift.json header = %+v, want version %d and the pinned note", doc, driftVersion)
	}
	if len(doc.Phases) != 1 || doc.Phases[0].Phase != PhasePlan || len(doc.Phases[0].Files) != 1 {
		t.Fatalf("drift.json phases = %+v, want one plan entry with one file", doc.Phases)
	}
	// The ledger is an artifact of the phase, so the phase log names it.
	if !containsArtifact(res.Artifacts, filepath.Join(outDir, driftFileName)) {
		t.Errorf("artifacts = %v, want drift.json", res.Artifacts)
	}
	phaseLog, rerr := os.ReadFile(filepath.Join(outDir, phaseLogFileName))
	if rerr != nil {
		t.Fatalf("phases.log: %v", rerr)
	}
	if !strings.Contains(string(phaseLog), "drift.json") {
		t.Errorf("phases.log = %q, want the drift ledger named", phaseLog)
	}

	// The report renders the drift BEFORE the seat rows and appends the token.
	report := AssembleReport(cfg, []*PhaseResult{res}, nil)
	if report.DriftStatus != StatusNativeDrift {
		t.Errorf("drift_status = %q, want %q", report.DriftStatus, StatusNativeDrift)
	}
	if len(report.Phases) != 1 || len(report.Phases[0].Drift) != 1 {
		t.Fatalf("report phases = %+v, want a plan phase carrying the drift row", report.Phases)
	}
	if len(report.Drift) != 1 || report.Drift[0].Phase != PhasePlan {
		t.Fatalf("report drift = %+v, want the plan entry", report.Drift)
	}
	rendered := report.Render()
	first := strings.Split(rendered, "\n")[0]
	if first != "agy pro code: ok (native-drift)" {
		t.Errorf("status line = %q, want the appended native-drift token", first)
	}
	if !strings.Contains(rendered, "drift: plan changed 1 file(s) (M internal/alpha/a.go)") {
		t.Errorf("render =\n%s\nwant the drift line naming the file", rendered)
	}
	if strings.Index(rendered, "drift: plan") > strings.Index(rendered, "seats:") {
		t.Errorf("render =\n%s\nwant the drifted files BEFORE the seats line", rendered)
	}
	if !strings.Contains(rendered, "note: "+driftNote) {
		t.Errorf("render =\n%s\nwant the honest note", rendered)
	}
	// The write phase has not run, so the diff section is empty: drift is the
	// only evidence, and it is present.
	if !strings.Contains(rendered, "files: none changed") {
		t.Errorf("render =\n%s\nwant the unchanged write diff for a plan-only run", rendered)
	}
	data, jerr := report.JSON()
	if jerr != nil {
		t.Fatalf("JSON: %v", jerr)
	}
	var decoded CodeRunReport
	if uerr := json.Unmarshal(data, &decoded); uerr != nil {
		t.Fatalf("decode report JSON: %v", uerr)
	}
	if decoded.DriftStatus != StatusNativeDrift || decoded.Status != StatusOK {
		t.Errorf("JSON status = %q, drift_status = %q; want the base verdict plus the token", decoded.Status, decoded.DriftStatus)
	}
}

// TestWritePhaseReusesThePlanBaselineInsteadOfRetaking is pin (c): the write
// phase keeps the plan phase's manifest. A re-take would re-baseline the drift
// above and make rollback restore the seat's edit as "original".
func TestWritePhaseReusesThePlanBaselineInsteadOfRetaking(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := driftPlanConfig(root, outDir)
	cfg.WriterSeat = providerSeat
	cfg.Yes = true
	cfg.MaxRepairs = 0
	cfg.VerifyCmds = [][]string{{"go", "build", "./internal/alpha"}}

	planRes, err := PlanPhase(cfg)
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	snapshots := countCalls(rec.calls, "snapshot")
	if snapshots != 1 {
		t.Fatalf("calls = %v, want exactly one snapshot in the plan phase", rec.calls)
	}

	cfg.Baseline = planRes.Manifest
	writeRes, werr := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if werr != nil {
		t.Fatalf("WritePhase: %v", werr)
	}
	if got := countCalls(rec.calls, "snapshot"); got != snapshots {
		t.Fatalf("calls = %v, the write phase re-took the snapshot (%d -> %d)", rec.calls, snapshots, got)
	}
	if writeRes.Manifest != planRes.Manifest {
		t.Fatalf("write manifest is not the plan baseline: %p vs %p", writeRes.Manifest, planRes.Manifest)
	}
	if phaseCallIndex(rec.calls, "diff") < 0 {
		t.Errorf("calls = %v, want the post-write diff", rec.calls)
	}

	// A baseline taken from another root is not reused: the write phase takes a
	// fresh one rather than diffing a tree it does not describe.
	other := filepath.Join(t.TempDir(), "otherrun")
	rec2 := installPhaseRecorder(t, nil)
	cfg2 := cfg
	cfg2.OutDir = other
	cfg2.Baseline = planRes.Manifest
	cfg2.Root = filepath.Join(t.TempDir(), "otherroot")
	if err := os.MkdirAll(filepath.Join(cfg2.Root, "internal", "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg2.Root, "internal", "alpha", "a.go"), []byte("package alpha\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, oerr := WritePhase(cfg2, phasePlanDoc(t, providerSeat)); oerr != nil {
		t.Fatalf("WritePhase (foreign baseline): %v", oerr)
	}
	if phaseCallIndex(rec2.calls, "snapshot") != 0 {
		t.Fatalf("calls = %v, want a fresh snapshot for a foreign-root baseline", rec2.calls)
	}
}

// TestReviewDriftNamesOnlyTheReviewChange covers the review half of the native
// phase rule: the baseline is the tree at phase start, so the write phase's own
// (authorized) change is not re-reported, and the seat's edit is.
func TestReviewDriftNamesOnlyTheReviewChange(t *testing.T) {
	root, outDir := phaseTree(t)
	scoped := []string{"internal/alpha/*.go"}
	if _, err := codesnap.Take(root, scoped, outDir); err != nil {
		t.Fatalf("Take: %v", err)
	}
	// The write phase's authorized creation, present before the review starts.
	written := filepath.Join(root, "internal", "alpha", "b.go")
	if err := os.WriteFile(written, []byte("package alpha // written by the provider seat\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	target := filepath.Join(root, "internal", "alpha", "a.go")
	log := installRealSeamFanout(t, func(call int, _ []string) {
		if call != 0 {
			return
		}
		if werr := os.WriteFile(target, []byte("package alpha // the review seat edited this\n"), 0o644); werr != nil {
			t.Errorf("plant the review drift: %v", werr)
		}
	})

	cfg := CodeConfig{Root: root, OutDir: outDir, ReviewSeats: []string{"native:m1"}}
	res, err := ReviewPhase(cfg, "")
	if err != nil {
		t.Fatalf("ReviewPhase: %v", err)
	}
	if len(log.calls) != 1 {
		t.Fatalf("fanout calls = %d, want 1", len(log.calls))
	}
	if len(res.Drift) != 1 {
		t.Fatalf("drift = %+v, want exactly the review seat's edit (the write's own file is not drift)", res.Drift)
	}
	if res.Drift[0].Path != "internal/alpha/a.go" {
		t.Errorf("drift path = %q, want internal/alpha/a.go", res.Drift[0].Path)
	}
	if !strings.Contains(log.out.String(), "native-drift: review") {
		t.Errorf("phase stream = %q, want the review drift finding", log.out.String())
	}

	// Pin (d) for the review half: the prompt carries the read-only instruction.
	prompt, rerr := os.ReadFile(filepath.Join(outDir, PhaseReview, promptFileName))
	if rerr != nil {
		t.Fatalf("review prompt: %v", rerr)
	}
	if !strings.Contains(string(prompt), "produce a review ONLY: do not create, edit, or delete ANY file") {
		t.Errorf("review prompt lost the read-only instruction:\n%s", prompt)
	}

	report := AssembleReport(cfg, []*PhaseResult{res}, nil)
	if report.DriftStatus != StatusNativeDrift {
		t.Errorf("drift_status = %q, want %q", report.DriftStatus, StatusNativeDrift)
	}
	if !strings.Contains(report.Render(), "drift: review changed 1 file(s) (M internal/alpha/a.go)") {
		t.Errorf("render =\n%s\nwant the review drift row", report.Render())
	}
}

// TestGovernanceWatchlistCatchesAnOutOfScopeNativeWrite is pin (e): AGENTS.md
// sits OUTSIDE the snapshot scope, so the scope walk cannot see it. The
// watchlist (the U2 denylist names that exist under the root) does, and the run
// says governance-drift instead of reporting a clean tree.
func TestGovernanceWatchlistCatchesAnOutOfScopeNativeWrite(t *testing.T) {
	root, outDir := phaseTree(t)
	gov := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(gov, []byte("# workspace rules (operator-owned)\n"), 0o600); err != nil {
		t.Fatalf("write governance file: %v", err)
	}
	log := installRealSeamFanout(t, func(call int, _ []string) {
		if call != 0 {
			return
		}
		if werr := os.WriteFile(gov, []byte("# workspace rules (rewritten by a native seat)\n"), 0o600); werr != nil {
			t.Errorf("plant the governance drift: %v", werr)
		}
	})

	cfg := driftPlanConfig(root, outDir)
	res, err := PlanPhase(cfg)
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	if len(res.Drift) != 0 {
		t.Fatalf("drift = %+v, want none: AGENTS.md is outside the snapshot scope", res.Drift)
	}
	if len(res.GovernanceDrift) != 1 {
		t.Fatalf("governance drift = %+v, want the rewritten AGENTS.md", res.GovernanceDrift)
	}
	if res.GovernanceDrift[0].Path != "AGENTS.md" || res.GovernanceDrift[0].Status != codesnap.StatusModified {
		t.Errorf("governance row = %+v, want a modified AGENTS.md", res.GovernanceDrift[0])
	}
	if !strings.Contains(log.out.String(), "governance-drift: plan") {
		t.Errorf("phase stream = %q, want the governance-drift finding", log.out.String())
	}

	ledger, lerr := os.ReadFile(filepath.Join(outDir, driftFileName))
	if lerr != nil {
		t.Fatalf("drift.json: %v", lerr)
	}
	if !strings.Contains(string(ledger), `"governance_files"`) || !strings.Contains(string(ledger), "AGENTS.md") {
		t.Errorf("drift.json does not name the governance file:\n%s", ledger)
	}

	report := AssembleReport(cfg, []*PhaseResult{res}, nil)
	if report.DriftStatus != StatusGovernanceDrift {
		t.Errorf("drift_status = %q, want %q (the watchlist outranks an in-scope edit)", report.DriftStatus, StatusGovernanceDrift)
	}
	rendered := report.Render()
	if !strings.Contains(rendered, "agy pro code: ok (governance-drift)") {
		t.Errorf("render =\n%s\nwant the governance-drift status token", rendered)
	}
	if !strings.Contains(rendered, "governance-drift: plan changed 1 governance file(s) (M AGENTS.md)") {
		t.Errorf("render =\n%s\nwant the governance-drift line", rendered)
	}
}

// TestRollbackAfterPlanDriftRestoresThePrePlanBytes is pin (f): the exact E2E
// failure. A native plan seat edits a scoped file; the snapshot was taken before
// the plan, so the rollback restores the bytes the operator had, not the seat's.
func TestRollbackAfterPlanDriftRestoresThePrePlanBytes(t *testing.T) {
	root, outDir := phaseTree(t)
	target := filepath.Join(root, "internal", "alpha", "a.go")
	original := []byte("package alpha\n")

	installRealSeamFanout(t, func(call int, _ []string) {
		if call != 0 {
			return
		}
		if werr := os.WriteFile(target, []byte("package alpha // unauthorized plan-seat edit\n"), 0o644); werr != nil {
			t.Errorf("plant the drift: %v", werr)
		}
	})
	cfg := driftPlanConfig(root, outDir)
	res, err := PlanPhase(cfg)
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	if len(res.Drift) != 1 {
		t.Fatalf("drift = %+v, want the planted plan-phase edit", res.Drift)
	}
	if live, _ := os.ReadFile(target); sha256Of(live) == sha256Of(original) {
		t.Fatal("the planted edit did not land; the pin would be vacuous")
	}

	rb, rerr := RollbackPhase(CodeConfig{Root: root, OutDir: outDir})
	if rerr != nil {
		t.Fatalf("RollbackPhase: %v", rerr)
	}
	if rb.Status != StatusRolledBack || rb.Rollback == nil {
		t.Fatalf("rollback result = %+v, want a rolled-back status", rb)
	}
	restored, _ := os.ReadFile(target)
	if string(restored) != string(original) {
		t.Fatalf("restored bytes = %q, want the PRE-plan bytes %q", restored, original)
	}
	if len(rb.Rollback.Restored) != 1 || rb.Rollback.Restored[0] != "internal/alpha/a.go" {
		t.Errorf("restored = %v, want the drifted file", rb.Rollback.Restored)
	}
}

// TestPlanAskCarriesTheReadOnlyBlock pins the belt of pin (d) directly: the plan
// ask file is the fixed block plus the request, and the request is not altered.
func TestPlanAskCarriesTheReadOnlyBlock(t *testing.T) {
	const ask = "fix TrimSpaceAll so it trims and drops empty fields"
	got := planAsk(ask)
	if !strings.HasPrefix(got, planReadOnlyPrefix) {
		t.Fatalf("planAsk does not start with the read-only block:\n%s", got)
	}
	for _, want := range []string{
		"Produce a PLAN ONLY. Do not create, edit, or delete ANY file. You have read-only tools for this phase.",
		"## Request",
		ask,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("planAsk missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, ask) != 1 {
		t.Errorf("planAsk repeats the request:\n%s", got)
	}
}

// TestDriftLedgerMergesPhasesInOrder pins drift.json determinism: a review entry
// written after a plan entry keeps phase order, not write order.
func TestDriftLedgerMergesPhasesInOrder(t *testing.T) {
	outDir := t.TempDir()
	review := &PhaseResult{Phase: PhaseReview, Drift: []codesnap.FileDelta{
		{Path: "internal/alpha/b.go", Status: codesnap.StatusModified, Added: 1},
	}}
	plan := &PhaseResult{Phase: PhasePlan, Drift: []codesnap.FileDelta{
		{Path: "internal/alpha/a.go", Status: codesnap.StatusModified, Added: 2},
	}}
	if _, err := writeDriftArtifact(outDir, review); err != nil {
		t.Fatalf("write review entry: %v", err)
	}
	if _, err := writeDriftArtifact(outDir, plan); err != nil {
		t.Fatalf("write plan entry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, driftFileName))
	if err != nil {
		t.Fatalf("drift.json: %v", err)
	}
	var doc driftDocument
	if uerr := json.Unmarshal(data, &doc); uerr != nil {
		t.Fatalf("decode: %v", uerr)
	}
	if len(doc.Phases) != 2 || doc.Phases[0].Phase != PhasePlan || doc.Phases[1].Phase != PhaseReview {
		t.Fatalf("phases = %+v, want plan then review (fixed order, not write order)", doc.Phases)
	}
}

// countCalls counts the recorder entries with the given name.
func countCalls(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

// containsArtifact reports whether the artifact list names path.
func containsArtifact(artifacts []string, path string) bool {
	for _, a := range artifacts {
		if a == path {
			return true
		}
	}
	return false
}
