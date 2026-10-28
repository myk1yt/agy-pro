package fanout

// Partition merge pins (work items B5 + B6).
//
// B5 (merge step): two DISJOINT units merge into ONE artifact with both
// deliverables in PLAN ORDER, the manifest lists every unit with its
// claimed-file count, and the verification pass passes. A partition seat's
// Content is a WORK PRODUCT: it must never reach the findings parser or the
// thin classifier, on the console progress line or in summary.json.
//
// B6 (conflict policy): any overlap (a file claimed by 2+ units) or any
// verification failure FAILS THE RUN with a conflict report naming the file
// and both units; the run is never silently merged. The merged manifest rides
// in summary.json as an omitempty block, so a non-partition summary.json stays
// byte-identical.
//
// Every pin here is mutation-verified (see impl-b5/b5b6-merge-report.md):
// dropping/reordering a unit in the artifact fails B5a, routing the merge
// through the findings machinery fails B5b/B5c, and making the policy silently
// merge fails B6a; removing the omitempty fails B6b.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// mergeRunResult is one partition-mode engine run driven by mergePartitionRun:
// the recorded requests, the merged summary (nil when the run failed), the
// workspace root, and the engine error (conflict/verification failures fail
// the run BY DESIGN, so the B6 pins need it).
type mergeRunResult struct {
	rec     *chatRecorder
	summary *SummaryReport
	root    string
	err     error
}

// mergePartitionRun stands up one provider seat per unit on one stub server
// (same shape as partitionRun) with a progress sink and a scripted responder,
// runs the REAL engine in partition mode, and returns the outcome including a
// possible engine error.
func mergePartitionRun(t *testing.T, units []Unit, sink io.Writer, respond func(call int, req capturedChatRequest) ChatResponse) *mergeRunResult {
	t.Helper()
	rec := &chatRecorder{respond: respond}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	for _, rel := range []string{
		filepath.Join("src", "alpha", "ok.go"),
		filepath.Join("src", "beta", "no.go"),
		filepath.Join("src", "aba.go"),
		filepath.Join("keys", testfixtures.AKIAExample+".txt"),
		"loose.go",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		body := "package " + strings.ReplaceAll(rel, string(filepath.Separator), "_") + "\n"
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	t.Setenv(providers.EnvHome, dir)

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "MERGE_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "MERGE_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("MERGE_KEY_A", "key-a")
	t.Setenv("MERGE_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	cfg := RunConfig{
		Task:         partTask,
		Root:         dir,
		OutDir:       filepath.Join(dir, "reports"),
		Units:        units,
		RegistryPath: regPath,
		MaxTurns:     6,
		TimeoutSec:   60,
		ProgressOut:  sink,
	}
	summary, err := Execute(context.Background(), cfg)
	return &mergeRunResult{rec: rec, summary: summary, root: dir, err: err}
}

// proseByModel answers each seat with a plain-language work product that
// carries NO findings markers at all, so the pins prove a work product is
// never routed through the findings machinery (a findings-shaped answer would
// mask a thin-classification regression).
func proseByModel(call int, req capturedChatRequest) ChatResponse {
	body := "WORK-PRODUCT-A: plain analysis prose for the alpha unit, no severity markers anywhere"
	if req.Model == "model-b" {
		body = "WORK-PRODUCT-B: plain analysis prose for the beta unit, no severity markers anywhere"
	}
	return ChatResponse{Choices: []ChatChoice{{
		Index:        0,
		FinishReason: "stop",
		Message:      ChatMessage{Role: "assistant", Content: body},
	}}}
}

func mergeOutDir(r *mergeRunResult) string { return filepath.Join(r.root, "reports") }

// readMergeSummaryJSON re-reads the on-disk summary.json of a partition run.
// The engine returns a nil summary on a failed run, so the failed-run pins
// inspect the manifest from disk (the artifacts are written for inspection).
func readMergeSummaryJSON(t *testing.T, r *mergeRunResult) *SummaryReport {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(mergeOutDir(r), "summary.json"))
	if err != nil {
		t.Fatalf("read summary.json: %v", err)
	}
	var s SummaryReport
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("summary.json is not parseable: %v\n%s", err, data)
	}
	return &s
}

// --- B5a: disjoint units merge into one artifact, plan order ---------------

// Two DISJOINT units merge into ONE artifact containing both deliverables in
// PLAN ORDER; the manifest lists each unit with its claimed-file count; the
// verification passes and the run succeeds.
func TestMergePartitionDisjointUnitsMergeIntoOneArtifact(t *testing.T) {
	units := partitionUnits()
	run := mergePartitionRun(t, units, nil, proseByModel)
	if run.err != nil {
		t.Fatalf("disjoint partition run failed: %v", run.err)
	}

	// ONE merged artifact, both deliverables, plan order.
	merged, err := os.ReadFile(filepath.Join(mergeOutDir(run), "merged.md"))
	if err != nil {
		t.Fatalf("merged artifact not written: %v", err)
	}
	ia := strings.Index(string(merged), "## Unit unit-alpha: Alpha work")
	ib := strings.Index(string(merged), "## Unit unit-beta: Beta work")
	if ia < 0 || ib < 0 {
		t.Fatalf("merged artifact is missing a unit section:\n%s", merged)
	}
	if ia > ib {
		t.Errorf("merged artifact violated plan order (beta before alpha):\n%s", merged)
	}
	if !strings.Contains(string(merged), "WORK-PRODUCT-A") || !strings.Contains(string(merged), "WORK-PRODUCT-B") {
		t.Errorf("merged artifact dropped a deliverable:\n%s", merged)
	}

	// The manifest lists every unit, in plan order, with its claimed files.
	m := run.summary.Partition
	if m == nil {
		t.Fatal("partition summary carries no merged manifest block")
	}
	if m.Mode != "partition" || m.Artifact != "merged.md" {
		t.Errorf("manifest mode/artifact = %q/%q; want partition/merged.md", m.Mode, m.Artifact)
	}
	if m.Status != partitionStatusMerged {
		t.Errorf("manifest status = %q; want %q on the happy path", m.Status, partitionStatusMerged)
	}
	if len(m.Units) != 2 {
		t.Fatalf("manifest units = %d; want 2", len(m.Units))
	}
	if m.Units[0].UnitID != "unit-alpha" || m.Units[1].UnitID != "unit-beta" {
		t.Errorf("manifest units out of plan order: %q then %q", m.Units[0].UnitID, m.Units[1].UnitID)
	}
	if len(m.Units[0].Files) != 1 || m.Units[0].Files[0] != "src/alpha/ok.go" {
		t.Errorf("unit-alpha claims = %v; want [src/alpha/ok.go]", m.Units[0].Files)
	}
	if len(m.Units[1].Files) != 1 || m.Units[1].Files[0] != "src/beta/no.go" {
		t.Errorf("unit-beta claims = %v; want [src/beta/no.go]", m.Units[1].Files)
	}
	if m.Units[0].Status != "success" {
		t.Errorf("unit-alpha seat status = %q; want success", m.Units[0].Status)
	}
	if len(m.Conflicts) != 0 {
		t.Errorf("disjoint plan reported conflicts: %v", m.Conflicts)
	}

	// Verification passes.
	if !m.Verification.Passed || !m.Verification.UnitsDelivered ||
		!m.Verification.EachUnitInArtifactOnce || !m.Verification.NoSilentDoubleAssignment {
		t.Errorf("verification = %+v; want all checks passed", m.Verification)
	}

	// Run-level: both units delivered; the deliverables are not findings, so
	// nothing is thin and the denominator is full.
	if run.summary.NRequested != 2 || run.summary.NCompleted != 2 {
		t.Errorf("NRequested/NCompleted = %d/%d; want 2/2", run.summary.NRequested, run.summary.NCompleted)
	}
	if run.summary.ThinSeats != 0 || len(run.summary.ThinSeatIDs) != 0 {
		t.Errorf("partition run carried thin seats: %d %v", run.summary.ThinSeats, run.summary.ThinSeatIDs)
	}

	// summary.json carries the merged block; the human report exists too.
	raw, err := os.ReadFile(filepath.Join(mergeOutDir(run), "summary.json"))
	if err != nil {
		t.Fatalf("summary.json not written: %v", err)
	}
	if !strings.Contains(string(raw), "\"partition\"") {
		t.Errorf("summary.json lacks the merged partition block:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(mergeOutDir(run), "reconciliation.md")); err != nil {
		t.Errorf("partition run left no human-readable report: %v", err)
	}
}

// --- B5b: the merge never uses the findings machinery ----------------------

// Grep-level pin: merge.go (the merge module) must not reference the findings
// machinery at all - not the reconciliation entry point, not the thin-seat
// classifier, not the findings parser/clusterer.
func TestMergeStepHasNoFindingsMachinery(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "merge.go"))
	if err != nil {
		t.Fatalf("read merge.go: %v", err)
	}
	for _, banned := range []string{"seatIsThin", "parseFindings", "consolidateFindings", "findingRe", "Reconcile"} {
		if strings.Contains(string(data), banned) {
			t.Errorf("merge.go references %q; the merge must never route work products through the findings machinery", banned)
		}
	}
}

// Behavioral pin: partition deliverables carry NO findings markers, and the
// run still counts BOTH seats (a findings-shaped pipeline would classify both
// thin and drop them from the denominator).
func TestMergePartitionWorkProductIsNeverThin(t *testing.T) {
	run := mergePartitionRun(t, partitionUnits(), nil, proseByModel)
	if run.err != nil {
		t.Fatalf("partition run failed: %v", run.err)
	}
	if run.summary.NCompleted != 2 {
		t.Errorf("NCompleted = %d; want 2 (both work products count; nothing is thin)", run.summary.NCompleted)
	}
	if run.summary.ThinSeats != 0 || len(run.summary.ThinSeatIDs) != 0 {
		t.Errorf("thin seats = %d %v; want none in a partition run", run.summary.ThinSeats, run.summary.ThinSeatIDs)
	}
	for _, seat := range run.summary.Seats {
		if seat.Thin {
			t.Errorf("seat %d (unit %q) classified thin; a work product must never be", seat.SeatID, seat.UnitID)
		}
	}
}

// --- B5c: the partition finish line is not labeled thin --------------------

// A partition seat's progress finish line reports its real status and is NEVER
// labeled thin / findings-counted, even when the deliverable carries no
// findings markers.
func TestPartitionSeatFinishLineIsNotLabeledThin(t *testing.T) {
	sink := &progressSink{}
	mergePartitionRun(t, partitionUnits(), sink, proseByModel)
	if sink.badLine != "" {
		t.Fatalf("engine wrote a non-atomic line (mutex gap): %q", sink.badLine)
	}

	starts, finishes := splitProgressLines(t, sink.String())
	if len(starts) != 2 || len(finishes) != 2 {
		t.Fatalf("progress lines = %d starts / %d finishes; want 2/2:\n%s", len(starts), len(finishes), sink.String())
	}
	assertEachSeatOnce(t, "finish lines", seatIDsOf(t, finishes, progressFinishRe), 2)
	for _, line := range finishes {
		if !strings.Contains(line, "-> success (") {
			t.Errorf("partition finish line lost the real status: %q", line)
		}
		if strings.Contains(line, "thin") {
			t.Errorf("partition work product labeled thin on the progress line: %q", line)
		}
		if strings.Contains(line, "findings:") {
			t.Errorf("partition finish line reported a findings count: %q", line)
		}
		if strings.Contains(line, "degraded") {
			t.Errorf("healthy partition seat labeled degraded: %q", line)
		}
	}
}

// --- B6a: overlapping scopes produce a conflict report and fail the run ----

// Two units whose scopes both claim src/aba.go produce a CONFLICT report (the
// shared file named, BOTH units named) and the run is marked FAILED - never
// silently merged, no winner picked. The merged artifact is still written for
// inspection.
func TestMergePartitionOverlappingScopesFailRun(t *testing.T) {
	units := []Unit{
		{ID: "unit-a", Title: "A", Scope: []string{"src/a*.go"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "unit-b", Title: "B", Scope: []string{"src/*a.go"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
	run := mergePartitionRun(t, units, nil, proseByModel)
	if run.err == nil {
		t.Fatal("overlapping partition scopes must FAIL the run; got nil error (silent merge)")
	}
	for _, want := range []string{"conflict", "src/aba.go", "unit-a", "unit-b"} {
		if !strings.Contains(run.err.Error(), want) {
			t.Errorf("run error = %q; want it to name %q", run.err.Error(), want)
		}
	}

	// The merged artifact is still written for inspection.
	merged, err := os.ReadFile(filepath.Join(mergeOutDir(run), "merged.md"))
	if err != nil {
		t.Fatalf("merged artifact not written on a failed run: %v", err)
	}
	if !strings.Contains(string(merged), "## Unit unit-a:") || !strings.Contains(string(merged), "## Unit unit-b:") {
		t.Errorf("failed-run artifact lost a unit section:\n%s", merged)
	}

	// The manifest on disk records the failure with the conflict named.
	s := readMergeSummaryJSON(t, run)
	if s.Partition == nil {
		t.Fatal("failed-run summary.json carries no partition block")
	}
	if s.Partition.Status != partitionStatusFailed {
		t.Errorf("manifest status = %q; want %q", s.Partition.Status, partitionStatusFailed)
	}
	if len(s.Partition.Conflicts) != 1 {
		t.Fatalf("conflicts = %v; want exactly one", s.Partition.Conflicts)
	}
	c := s.Partition.Conflicts[0]
	if c.Path != "src/aba.go" {
		t.Errorf("conflict path = %q; want src/aba.go", c.Path)
	}
	if len(c.Units) != 2 || c.Units[0] != "unit-a" || c.Units[1] != "unit-b" {
		t.Errorf("conflict units = %v; want [unit-a unit-b]", c.Units)
	}
	if s.Partition.Verification.Passed {
		// Fine: the three self-checks concern the merge machinery (all units
		// delivered, one heading each, the overlap reported). The CONFLICT is
		// what fails the run, and it must be reported, not silently merged.
		t.Logf("verification self-checks passed; the run still fails on the conflict")
	}
	if !s.Partition.Verification.NoSilentDoubleAssignment {
		t.Errorf("verification = %+v; want the overlap reported (NoSilentDoubleAssignment true)", s.Partition.Verification)
	}

	// The human-readable report names the units and the file.
	report, err := os.ReadFile(filepath.Join(mergeOutDir(run), "reconciliation.md"))
	if err != nil {
		t.Fatalf("conflict report not written: %v", err)
	}
	for _, want := range []string{"Conflicts", "src/aba.go", "unit-a", "unit-b"} {
		if !strings.Contains(string(report), want) {
			t.Errorf("conflict report lacks %q:\n%s", want, report)
		}
	}
}

// --- B6: a missing deliverable fails the run (verification pass) -----------

// A partition seat that fails (provider 500, failover exhausts) leaves its
// unit with no deliverable: the verification pass fails the run, the failed
// unit is named, and its finish line reports the real failed/degraded status
// without a thin label.
func TestMergePartitionMissingDeliverableFailsRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var wire struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			http.Error(w, "decode body", http.StatusBadRequest)
			return
		}
		if wire.Model == "model-b" {
			http.Error(w, "provider boom", http.StatusInternalServerError)
			return
		}
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			FinishReason: "stop",
			Message:      ChatMessage{Role: "assistant", Content: "WORK-PRODUCT-A: alpha delivered"},
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "alpha", "ok.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "beta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "beta", "no.go"), []byte("package y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvHome, dir)

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "VF_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "VF_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("VF_KEY_A", "key-a")
	t.Setenv("VF_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	sink := &progressSink{}
	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:         partTask,
		Root:         dir,
		OutDir:       outDir,
		Units:        partitionUnits(),
		RegistryPath: regPath,
		MaxTurns:     4,
		TimeoutSec:   60,
		ProgressOut:  sink,
	}
	summary, err := Execute(context.Background(), cfg)
	if err == nil {
		t.Fatal("a unit with no deliverable must FAIL the run; got nil error")
	}
	if !strings.Contains(err.Error(), "verification") || !strings.Contains(err.Error(), "unit-beta") {
		t.Errorf("run error = %q; want it to name the verification failure and the unit", err.Error())
	}
	if summary != nil {
		t.Errorf("engine returned a non-nil summary on a failed run: %+v", summary)
	}

	// The failed unit is named in the on-disk manifest.
	var disk SummaryReport
	data, rerr := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if rerr != nil {
		t.Fatalf("summary.json not written on a failed run: %v", rerr)
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatalf("summary.json not parseable: %v", err)
	}
	if disk.Partition == nil || disk.Partition.Verification.Passed || disk.Partition.Verification.UnitsDelivered {
		t.Errorf("manifest verification = %+v; want UnitsDelivered=false and Passed=false", disk.Partition.Verification)
	}
	if disk.Partition.Status != partitionStatusFailed {
		t.Errorf("manifest status = %q; want failed", disk.Partition.Status)
	}
	var betaFound bool
	for _, u := range disk.Partition.Units {
		if u.UnitID == "unit-beta" {
			betaFound = true
			if u.Status != "failed" {
				t.Errorf("unit-beta seat status = %q; want failed", u.Status)
			}
		}
	}
	if !betaFound {
		t.Errorf("manifest units = %+v; want unit-beta named", disk.Partition.Units)
	}

	// The failed seat's finish line reports the real status (failed, degraded)
	// and is NOT labeled thin.
	_, finishes := splitProgressLines(t, sink.String())
	if len(finishes) != 2 {
		t.Fatalf("finish lines = %d; want 2:\n%s", len(finishes), sink.String())
	}
	var failedLine string
	for _, line := range finishes {
		if strings.Contains(line, "-> failed (") {
			failedLine = line
		}
		if strings.Contains(line, "thin") {
			t.Errorf("finish line labeled thin: %q", line)
		}
	}
	if failedLine == "" || !strings.Contains(failedLine, ", degraded)") {
		t.Errorf("failed seat finish line = %q; want failed with the degraded marker", failedLine)
	}
}

// --- B6b: non-partition summary.json stays byte-identical ------------------

// The merged manifest block appears ONLY in partition mode: a cross-verification
// summary.json carries no "partition" key at all, while a partition run's
// summary.json carries it.
func TestNonPartitionSummaryJSONStaysByteIdentical(t *testing.T) {
	legacy := requestShapeRun(t, requestShapeAnswer)
	if legacy.summary.Partition != nil {
		t.Fatalf("cross-verification summary carries a partition block: %+v", legacy.summary.Partition)
	}
	raw, err := os.ReadFile(filepath.Join(legacy.outDir, "summary.json"))
	if err != nil {
		t.Fatalf("read legacy summary.json: %v", err)
	}
	if strings.Contains(string(raw), "\"partition\"") {
		t.Errorf("non-partition summary.json gained the partition block (omitempty broken):\n%s", raw)
	}

	part := mergePartitionRun(t, partitionUnits(), nil, proseByModel)
	if part.err != nil {
		t.Fatalf("partition run failed: %v", part.err)
	}
	praw, err := os.ReadFile(filepath.Join(mergeOutDir(part), "summary.json"))
	if err != nil {
		t.Fatalf("read partition summary.json: %v", err)
	}
	if !strings.Contains(string(praw), "\"partition\"") {
		t.Errorf("partition summary.json lacks the merged block:\n%s", praw)
	}
}

// --- D1: the partition success banner names the merged deliverable ---------
//
// The terminal banner is partition-aware (audit finding D1): a successful
// partition run names merged.md and the merge status, and never prints the
// phantom cross-verification language (per-seat report lines for seat-*.md
// files the merge never writes, confirmed/disputed counts, "all findings
// verified") - partition mode runs no findings reconciliation. The
// cross-verification banner stays byte-shaped.
func TestPartitionSuccessBannerNamesMergedArtifact(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())

	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		if len(cfg.Units) > 0 {
			return &SummaryReport{
				Task:       cfg.Task,
				Root:       cfg.Root,
				NRequested: len(cfg.Units),
				NCompleted: len(cfg.Units),
				Seats:      []SeatResult{{SeatID: 1}, {SeatID: 2}},
				Partition: &PartitionManifest{
					Mode:      "partition",
					Status:    partitionStatusMerged,
					Artifact:  partitionArtifactName,
					Units:     []PartitionUnitEntry{{UnitID: "unit-alpha", Title: "Alpha work"}, {UnitID: "unit-beta", Title: "Beta work"}},
					Conflicts: []PartitionConflict{},
				},
			}, nil
		}
		return &SummaryReport{
			Task:           cfg.Task,
			NRequested:     2,
			NCompleted:     2,
			Seats:          []SeatResult{{SeatID: 1}, {SeatID: 2}},
			ConfirmedCount: 1,
		}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	// A real --units run through the flag surface (plan file parsed by
	// buildRunConfig), engine stubbed to return the partition summary.
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	planData, err := json.Marshal(partitionUnits())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, planData, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	code := Run([]string{"-units", planPath, "-out", "reports/fanout/banner"}, nil, &out, &errw)
	if code != 0 {
		t.Fatalf("partition run exit = %d; want 0\nstderr:\n%s", code, errw.String())
	}
	for _, want := range []string{
		"Merged Deliverable:  " + filepath.Join("reports/fanout/banner", "merged.md"),
		"Partition Units:       2",
		"Partition Status:      merged",
		"Conflicts:             0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("partition banner lacks %q:\n%s", want, out.String())
		}
	}
	for _, banned := range []string{
		"Seat 1 Report", "Seat 2 Report", "seat-1-", "seat-2-",
		"Confirmed Issues", "Disputed Issues", "findings verified", "Thin Seats",
	} {
		if strings.Contains(out.String(), banned) {
			t.Errorf("partition banner printed cross-verification language %q:\n%s", banned, out.String())
		}
	}

	// The cross-verification banner keeps its findings language and its
	// per-seat report lines (byte-shaped; the partition branch must not have
	// leaked into it).
	var out2, errw2 bytes.Buffer
	code = Run([]string{"-prompt", "p"}, nil, &out2, &errw2)
	if code != 0 {
		t.Fatalf("cross-verification run exit = %d; want 0\nstderr:\n%s", code, errw2.String())
	}
	for _, want := range []string{
		"Confirmed Issues:      1",
		"Disputed Issues:       0",
		"  Seat 1 Report:       seat-1--.md",
		"✅ All findings verified across swarm models.",
	} {
		if !strings.Contains(out2.String(), want) {
			t.Errorf("cross-verification banner lost %q:\n%s", want, out2.String())
		}
	}
}

// --- D2: colon-bearing unit ids do not alias the heading anchor ------------

// Audit finding D2: countUnitHeadings anchored on the "## Unit <id>:" prefix,
// so id "api" matched the heading of unit "api:v2". Anchoring on the unit's
// WHOLE heading line makes the match exact: a healthy plan with colon ids
// passes verification, and a dropped section is still caught.
func TestVerifyPartitionMergeColonIDsDoNotAlias(t *testing.T) {
	identity := func(s string) string { return s }
	units := []Unit{
		{ID: "api", Title: "Alpha", Scope: []string{"src/alpha"}, Deliverable: "d"},
		{ID: "api:v2", Title: "Beta", Scope: []string{"src/beta"}, Deliverable: "d"},
	}
	results := []*SeatResult{
		{Status: "success", Content: "alpha work product"},
		{Status: "success", Content: "beta work product"},
	}
	merged := buildMergedArtifact("review", "ts", units, results, identity)
	claims := [][]string{{"src/alpha/ok.go"}, {"src/beta/no.go"}}

	// Healthy plan with colon ids: "api" must match exactly its own heading,
	// never the "## Unit api:v2: ..." line (the old prefix anchor counted 2).
	v := verifyPartitionMerge(units, results, merged, claims, nil, identity)
	if !v.Passed || !v.EachUnitInArtifactOnce {
		t.Errorf("healthy colon-id plan failed verification: %+v", v)
	}

	// A dropped section is still caught: without the api:v2 heading the
	// EachUnitInArtifactOnce check fails the run.
	const betaHeading = partitionUnitHeading + "api:v2: Beta"
	if !strings.Contains(merged, betaHeading) {
		t.Fatalf("fixture artifact lacks the colon-id heading %q:\n%s", betaHeading, merged)
	}
	dropped := strings.Replace(merged, betaHeading+"\n\n", "", 1)
	v2 := verifyPartitionMerge(units, results, dropped, claims, nil, identity)
	if v2.EachUnitInArtifactOnce || v2.Passed {
		t.Errorf("a dropped colon-id section was not caught: %+v", v2)
	}
}

// Behavioral twin: a real engine run whose unit ids are "api" and "api:v2"
// merges cleanly (the pre-D2 prefix anchor made the healthy run fail).
func TestMergePartitionColonIDsMergeEndToEnd(t *testing.T) {
	units := []Unit{
		{ID: "api", Title: "A", Scope: []string{"src/alpha"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "api:v2", Title: "B", Scope: []string{"src/beta"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
	run := mergePartitionRun(t, units, nil, proseByModel)
	if run.err != nil {
		t.Fatalf("healthy colon-id partition run failed: %v", run.err)
	}
	merged, err := os.ReadFile(filepath.Join(mergeOutDir(run), "merged.md"))
	if err != nil {
		t.Fatalf("merged artifact not written: %v", err)
	}
	for _, want := range []string{"## Unit api: A", "## Unit api:v2: B"} {
		if !strings.Contains(string(merged), want) {
			t.Errorf("merged artifact lacks the exact heading %q:\n%s", want, merged)
		}
	}
	if !run.summary.Partition.Verification.Passed || !run.summary.Partition.Verification.EachUnitInArtifactOnce {
		t.Errorf("colon-id run verification = %+v; want all passed", run.summary.Partition.Verification)
	}
}

// --- D3: key-shaped plan fields are masked in artifacts and errors ---------

// Audit finding D3: unit id/title/seat are plan-authored and echoed verbatim
// into merged.md headings, the report table, summary.json's partition block
// and the run-failure error. The merge seeds the artifact redactor with every
// unit's id/title/seat (the shared SeedEligible rule), so a key-shaped field
// renders [REDACTED] everywhere while benign ids stay readable.
func TestMergePartitionMasksKeyShapedPlanFields(t *testing.T) {
	const keyID = testfixtures.AWSSecretExample
	units := []Unit{
		{ID: keyID, Title: "Alpha work", Scope: []string{"src/alpha"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "unit-beta", Title: "Beta work", Scope: []string{"src/beta"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
	run := mergePartitionRun(t, units, nil, proseByModel)
	if run.err != nil {
		t.Fatalf("partition run failed: %v", run.err)
	}

	// merged.md: the heading carries the masked id, never the raw key.
	merged, err := os.ReadFile(filepath.Join(mergeOutDir(run), "merged.md"))
	if err != nil {
		t.Fatalf("merged artifact not written: %v", err)
	}
	if strings.Contains(string(merged), keyID) {
		t.Errorf("merged.md leaks the key-shaped unit id:\n%s", merged)
	}
	if !strings.Contains(string(merged), partitionUnitHeading+"[REDACTED]: Alpha work") {
		t.Errorf("merged.md lost the masked unit heading:\n%s", merged)
	}

	// summary.json: the partition block AND the per-seat unit_id echo mask it.
	raw, err := os.ReadFile(filepath.Join(mergeOutDir(run), "summary.json"))
	if err != nil {
		t.Fatalf("summary.json not written: %v", err)
	}
	if strings.Contains(string(raw), keyID) {
		t.Errorf("summary.json leaks the key-shaped unit id:\n%s", raw)
	}
	s := readMergeSummaryJSON(t, run)
	if len(s.Partition.Units) != 2 || s.Partition.Units[0].UnitID != "[REDACTED]" {
		t.Errorf("manifest unit_id = %+v; want the masked id", s.Partition.Units)
	}
	var maskedSeat bool
	for _, seat := range s.Seats {
		if seat.UnitID == "[REDACTED]" {
			maskedSeat = true
		}
		if seat.UnitID == keyID {
			t.Errorf("summary.json seat echoes the raw key id: %+v", seat.UnitID)
		}
	}
	if !maskedSeat {
		t.Errorf("no summary.json seat carries the masked unit_id: %+v", s.Seats)
	}

	// reconciliation.md: the Units table masks it too.
	report, err := os.ReadFile(filepath.Join(mergeOutDir(run), "reconciliation.md"))
	if err != nil {
		t.Fatalf("reconciliation.md not written: %v", err)
	}
	if strings.Contains(string(report), keyID) {
		t.Errorf("reconciliation.md leaks the key-shaped unit id:\n%s", report)
	}
	if !strings.Contains(string(report), "[REDACTED]") {
		t.Errorf("reconciliation.md lost the masked unit id entirely:\n%s", report)
	}

	// The failure error masks it: the same key id on an overlapping plan must
	// not reach the console error line raw.
	overlap := []Unit{
		{ID: keyID, Title: "A", Scope: []string{"src/a*.go"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "unit-b", Title: "B", Scope: []string{"src/*a.go"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
	run2 := mergePartitionRun(t, overlap, nil, proseByModel)
	if run2.err == nil {
		t.Fatal("overlapping scopes must FAIL the run; got nil error")
	}
	if strings.Contains(run2.err.Error(), keyID) {
		t.Errorf("failure error leaks the key-shaped unit id: %v", run2.err)
	}
	if !strings.Contains(run2.err.Error(), "[REDACTED]") {
		t.Errorf("failure error carries no redaction marker: %v", run2.err)
	}
}

// --- D4: the plan unit-count ceiling ---------------------------------------

// Audit finding D4: the engine spawns one uncapped provider goroutine per
// unit, so a huge supply-chain plan could exhaust provider quota/CPU.
// ParsePlan rejects a plan over maxPlanUnits with a clear error (fail-safe);
// a plan at the cap passes. validatePartitionUnits re-checks the ceiling for
// callers that build Units directly.
func TestParsePlanUnitCountCeiling(t *testing.T) {
	unit := func(i int) Unit {
		return Unit{ID: fmt.Sprintf("u%d", i), Title: "t", Scope: []string{fmt.Sprintf("src/pkg%d", i)}, Deliverable: "d"}
	}
	over := make([]Unit, 0, maxPlanUnits+1)
	for i := 0; i < maxPlanUnits+1; i++ {
		over = append(over, unit(i))
	}
	data, err := json.Marshal(over)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePlan(data); err == nil || !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Errorf("plan with %d units: err = %v; want the ceiling error", len(over), err)
	}

	at := make([]Unit, 0, maxPlanUnits)
	for i := 0; i < maxPlanUnits; i++ {
		at = append(at, unit(i))
	}
	data, err = json.Marshal(at)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePlan(data)
	if err != nil {
		t.Fatalf("plan with %d units rejected: %v", len(at), err)
	}
	if len(parsed) != maxPlanUnits {
		t.Errorf("plan with %d units parsed into %d; want the cap count", len(at), len(parsed))
	}

	// The engine backstop (library callers that bypass ParsePlan).
	if err := validatePartitionUnits(over); err == nil || !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Errorf("validatePartitionUnits(%d units) = %v; want the ceiling error", len(over), err)
	}
}

// --- D5: an in-scope symlink to another unit's file is a conflict ----------

// Audit finding D5: the disjointness audit matched claims lexically, so an
// in-scope symlink to another unit's real file was claimed by its lexical
// owner alone and the overlap stayed hidden from the manifest. The claim scan
// now attributes every file to its symlink-RESOLVED real path (the same
// resolution the per-seat read filter uses), so both the lexical owner and
// the real owner claim the one real path and the run FAILS with a conflict.
// The control run (same plan, no symlink) merges cleanly.
func TestMergePartitionSymlinkOverlapFailsRun(t *testing.T) {
	setup := func(t *testing.T, plantSymlink bool) *mergeRunResult {
		t.Helper()
		rec := &chatRecorder{respond: proseByModel}
		server := httptest.NewServer(rec)
		t.Cleanup(server.Close)

		dir := t.TempDir()
		for _, rel := range []string{
			filepath.Join("src", "alpha", "ok.go"),
			filepath.Join("src", "beta", "no.go"),
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", rel, err)
			}
			if err := os.WriteFile(filepath.Join(dir, rel), []byte("package x\n"), 0o600); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
		}
		if plantSymlink {
			link := filepath.Join(dir, "src", "alpha", "alias.go")
			if err := os.Symlink(filepath.Join(dir, "src", "beta", "no.go"), link); err != nil {
				t.Skipf("symlink creation not permitted on this host: %v", err)
			}
		}
		t.Setenv(providers.EnvHome, dir)

		trueVal := true
		reg := &providers.Registry{Providers: []providers.Provider{
			{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "SL_KEY_A",
				Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
					Capabilities: providers.Capabilities{SupportsTools: true}}}},
			{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "SL_KEY_B",
				Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
					Capabilities: providers.Capabilities{SupportsTools: true}}}},
		}}
		t.Setenv("SL_KEY_A", "key-a")
		t.Setenv("SL_KEY_B", "key-b")
		regPath := filepath.Join(dir, "providers.json")
		writeRegistry(t, regPath, reg)

		cfg := RunConfig{
			Task:         partTask,
			Root:         dir,
			OutDir:       filepath.Join(dir, "reports"),
			Units:        unitsAB(),
			RegistryPath: regPath,
			MaxTurns:     6,
			TimeoutSec:   60,
		}
		summary, err := Execute(context.Background(), cfg)
		return &mergeRunResult{rec: rec, summary: summary, root: dir, err: err}
	}

	// Control: same plan, no symlink -> disjoint -> merged.
	clean := setup(t, false)
	if clean.err != nil {
		t.Fatalf("disjoint control run failed: %v", clean.err)
	}
	if clean.summary.Partition.Status != partitionStatusMerged {
		t.Errorf("control status = %q; want merged", clean.summary.Partition.Status)
	}

	// D5: unit-a's scope contains a symlink to unit-b's real file -> CONFLICT.
	run := setup(t, true)
	if run.err == nil {
		t.Fatal("an in-scope symlink to another unit's file must FAIL the run with a conflict; got a silent merge")
	}
	for _, want := range []string{"conflict", "src/beta/no.go", "unit-a", "unit-b"} {
		if !strings.Contains(run.err.Error(), want) {
			t.Errorf("run error = %q; want it to name %q", run.err.Error(), want)
		}
	}
	s := readMergeSummaryJSON(t, run)
	if s.Partition.Status != partitionStatusFailed {
		t.Errorf("manifest status = %q; want failed", s.Partition.Status)
	}
	var found bool
	for _, c := range s.Partition.Conflicts {
		if c.Path == "src/beta/no.go" && len(c.Units) == 2 && c.Units[0] == "unit-a" && c.Units[1] == "unit-b" {
			found = true
		}
	}
	if !found {
		t.Errorf("manifest conflicts = %+v; want src/beta/no.go claimed by [unit-a unit-b]", s.Partition.Conflicts)
	}
}

// unitsAB are the two disjoint units the D5 fixture plans around.
func unitsAB() []Unit {
	return []Unit{
		{ID: "unit-a", Title: "A", Scope: []string{"src/alpha"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "unit-b", Title: "B", Scope: []string{"src/beta"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
}
