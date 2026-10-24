package codework

// Tests for the V8 run report: the JSON is deterministic and relative, the human
// block is bounded and masked, and the rollback line appears exactly when the
// tree may hold written changes that were not proven good.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
)

// plantedKey is an AWS-key-shaped string: it must never survive into a rendered
// report. It is built from parts so the source file carries no contiguous
// credential-shaped literal of its own.
var plantedKey = "AKIA" + "ABCDEFGHIJKLMNOP"

// summaryFixtureSeat is the subset of fanout.SeatResult the report reads,
// spelled with the tags internal/fanout/worker.go documents.
type summaryFixtureSeat struct {
	SeatID      int    `json:"seat_id"`
	Kind        string `json:"kind"`
	FinalTarget struct {
		Provider struct {
			Name string `json:"name"`
		} `json:"provider"`
		Model struct {
			ID string `json:"id"`
		} `json:"model"`
	} `json:"final_target"`
	Status string `json:"status"`
	Usage  *struct {
		PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
		CompletionTokens *int64 `json:"completion_tokens,omitempty"`
	} `json:"usage"`
}

// writeSummaryFixture writes a fanout-shaped summary.json into dir.
func writeSummaryFixture(t *testing.T, dir string, task string, seats ...summaryFixtureSeat) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	doc := map[string]any{
		"task":            task,
		"root":            ".",
		"n_requested":     len(seats),
		"n_completed":     len(seats),
		"degraded":        false,
		"seats":           seats,
		"issues":          []any{},
		"confirmed_count": 0,
		"disputed_count":  0,
		"untrusted_data":  "untrusted workspace/model output",
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	path := filepath.Join(dir, summaryFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// narrativeSeat builds one fixture seat.
func narrativeSeat(id int, kind, model, status string, prompt, completion *int64) summaryFixtureSeat {
	var s summaryFixtureSeat
	s.SeatID = id
	s.Kind = kind
	s.Status = status
	s.FinalTarget.Provider.Name = "fixture"
	s.FinalTarget.Model.ID = model
	if prompt != nil || completion != nil {
		s.Usage = &struct {
			PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
			CompletionTokens *int64 `json:"completion_tokens,omitempty"`
		}{PromptTokens: prompt, CompletionTokens: completion}
	}
	return s
}

// int64p is the pointer helper the usage fields need.
func int64p(v int64) *int64 { return &v }

// failingWriteResult is the write outcome the report tests reuse: a write that
// ran, was verified, failed, and used one repair.
func failingWriteResult(outDir string, deltas int) *WritePhaseResult {
	diff := make([]codesnap.FileDelta, 0, deltas)
	for i := 0; i < deltas; i++ {
		status := codesnap.StatusModified
		if i%7 == 0 {
			status = codesnap.StatusCreated
		}
		diff = append(diff, codesnap.FileDelta{
			Path:    fmt.Sprintf("internal/alpha/f%02d.go", i),
			Status:  status,
			Added:   3,
			Removed: 1,
		})
	}
	return &WritePhaseResult{
		Status:   StatusVerifyFailed,
		Attempts: 2,
		ExitCode: 0,
		Verified: true,
		Verify: VerifyResult{
			Argv:     []string{"go", "test", "./internal/alpha"},
			ExitCode: 1,
			LogPath:  filepath.Join(outDir, verifyDirName, "attempt-2-1.log"),
		},
		PlanPath:   filepath.Join(outDir, PhaseWrite, planFileName),
		FanoutRuns: []string{filepath.Join(outDir, PhaseWrite), filepath.Join(outDir, "repair-1")},
		Diff:       diff,
	}
}

func TestReportRenderIsBoundedAndMasksASeatSecret(t *testing.T) {
	outDir := t.TempDir()
	writeSummaryFixture(t, filepath.Join(outDir, PhaseWrite), "plan-audit",
		narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil),
		narrativeSeat(2, "provider", plantedKey, "success", int64p(1200), int64p(340)),
		narrativeSeat(3, "provider", "nw/glm-5.3", "failed", nil, nil),
	)
	write := failingWriteResult(outDir, 40)
	cfg := CodeConfig{OutDir: outDir, Scope: []string{"internal/alpha/*.go"}}

	report := AssembleReport(cfg, nil, write)
	rendered := report.Render()
	lines := strings.Split(rendered, "\n")
	if len(lines) > maxReportLines {
		t.Fatalf("render has %d lines, budget is %d:\n%s", len(lines), maxReportLines, rendered)
	}
	if strings.Contains(rendered, plantedKey) {
		t.Fatalf("render leaked the planted key:\n%s", rendered)
	}
	if !strings.Contains(rendered, "****") {
		t.Errorf("render shows no mask marker for the key-shaped model:\n%s", rendered)
	}
	if !strings.Contains(rendered, "... 35 more") {
		t.Errorf("render does not bound the file rows:\n%s", rendered)
	}
	for _, want := range []string{
		"agy pro code: verify-failed",
		"files: 40 changed",
		"seats: 3 seat occurrence(s) (1 native, 2 provider, 3 unique model(s))",
		"rollback: agy pro code rollback --run",
		"verify: go test ./internal/alpha FAIL attempt=2",
		"artifacts: ",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("render missing %q:\n%s", want, rendered)
		}
	}

	// The JSON is the machine block: the planted key must not be there either.
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(data), plantedKey) {
		t.Fatalf("JSON leaked the planted key:\n%s", data)
	}
}

func TestReportJSONIsDeterministicAndRelative(t *testing.T) {
	outDir := t.TempDir()
	writeSummaryFixture(t, filepath.Join(outDir, PhaseWrite), "plan-audit",
		narrativeSeat(1, "provider", "nw/glm-5.3", "success", int64p(100), int64p(20)),
	)
	write := failingWriteResult(outDir, 3)
	cfg := CodeConfig{OutDir: outDir}
	planPhase := &PhaseResult{
		Phase:     PhasePlan,
		Status:    StatusOK,
		Ran:       true,
		OutDir:    filepath.Join(outDir, PhasePlan),
		Artifacts: []string{filepath.Join(outDir, PhasePlan, askFileName)},
	}

	first, err := AssembleReport(cfg, []*PhaseResult{planPhase}, write).JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	second, err := AssembleReport(cfg, []*PhaseResult{planPhase}, write).JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("two renders of the same run differ:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(string(first), "timestamp") {
		t.Errorf("report carries a timestamp, which makes it non-deterministic:\n%s", first)
	}

	var decoded CodeRunReport
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Phases) != 2 {
		t.Fatalf("phases = %d, want 2 (plan, write)", len(decoded.Phases))
	}
	if decoded.Phases[0].Phase != PhasePlan || decoded.Phases[1].Phase != PhaseWrite {
		t.Fatalf("phase order = %q,%q, want plan,write", decoded.Phases[0].Phase, decoded.Phases[1].Phase)
	}
	for _, p := range decoded.Phases {
		for _, rel := range append(append([]string{}, p.Runs...), p.Artifacts...) {
			if filepath.IsAbs(rel) || strings.HasPrefix(rel, outDir) {
				t.Errorf("phase %q carries the absolute path %q; artifact paths are relative to the run directory", p.Phase, rel)
			}
		}
	}
	if len(decoded.Verify) != 1 || filepath.IsAbs(decoded.Verify[0].Log) {
		t.Fatalf("verify block = %+v, want one row with a relative log path", decoded.Verify)
	}
	if decoded.Cost.NativeSeatOccurrences != 0 || decoded.Cost.ProviderSeatOccurrences != 1 {
		t.Errorf("cost = %+v, want 1 provider seat occurrence", decoded.Cost)
	}
	if decoded.Rollback == nil || !strings.Contains(decoded.Rollback.Command, "agy pro code rollback --run") {
		t.Errorf("rollback block = %+v, want the undo command", decoded.Rollback)
	}
}

func TestLoadFanoutSummary(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFanoutSummary(filepath.Join(dir, summaryFileName)); err == nil {
		t.Fatalf("LoadFanoutSummary accepted a missing file")
	}
	junk := filepath.Join(dir, "junk.json")
	if err := os.WriteFile(junk, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	if _, err := LoadFanoutSummary(junk); err == nil {
		t.Fatalf("LoadFanoutSummary accepted a non-summary file")
	}

	path := writeSummaryFixture(t, dir, "review",
		narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil),
		narrativeSeat(2, "provider", "nw/glm-5.3", "success", int64p(10), int64p(5)),
	)
	summary, err := LoadFanoutSummary(path)
	if err != nil {
		t.Fatalf("LoadFanoutSummary: %v", err)
	}
	if len(summary.Seats) != 2 {
		t.Fatalf("seats = %d, want 2", len(summary.Seats))
	}
	if summary.Seats[1].Usage == nil || summary.Seats[1].Usage.PromptTokens == nil || *summary.Seats[1].Usage.PromptTokens != 10 {
		t.Errorf("usage = %+v, want 10 prompt tokens on seat 2", summary.Seats[1].Usage)
	}
}

func TestAssembleReportHarvestsSeatsAcrossPhases(t *testing.T) {
	outDir := t.TempDir()
	planDir := filepath.Join(outDir, PhasePlan)
	reviewDir := filepath.Join(outDir, PhaseReview)
	writeDir := filepath.Join(outDir, PhaseWrite)
	writeSummaryFixture(t, planDir, "plan-audit", narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil))
	writeSummaryFixture(t, reviewDir, "review", narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil))
	writeSummaryFixture(t, writeDir, "plan-audit", narrativeSeat(1, "provider", "nw/glm-5.3", "success", int64p(900), int64p(120)))

	cfg := CodeConfig{OutDir: outDir}
	phases := []*PhaseResult{
		{Phase: PhaseReview, Status: StatusOK, Ran: true, OutDir: reviewDir},
		{Phase: PhasePlan, Status: StatusOK, Ran: true, OutDir: planDir},
	}
	write := &WritePhaseResult{
		Status:     StatusOK,
		Attempts:   1,
		ExitCode:   0,
		Verified:   true,
		Verify:     VerifyResult{Argv: []string{"go", "build", "./..."}, Passed: true, LogPath: filepath.Join(outDir, verifyDirName, "attempt-1-1.log")},
		FanoutRuns: []string{writeDir},
		Diff:       []codesnap.FileDelta{},
	}

	report := AssembleReport(cfg, phases, write)
	if len(report.Seats) != 3 {
		t.Fatalf("seats = %d, want 3 (one from each phase): %+v", len(report.Seats), report.Seats)
	}
	// Phase attribution follows the report's fixed order, not the caller's.
	wantPhases := []string{PhasePlan, PhaseWrite, PhaseReview}
	for i, want := range wantPhases {
		if report.Seats[i].Phase != want {
			t.Errorf("seat %d phase = %q, want %q", i, report.Seats[i].Phase, want)
		}
	}
	if report.Cost.NativeSeatOccurrences != 2 || report.Cost.ProviderSeatOccurrences != 1 {
		t.Errorf("cost = %+v, want 2 native + 1 provider seat occurrences", report.Cost)
	}
	if report.Cost.UniqueModels != 2 {
		t.Errorf("unique models = %d, want 2 (the native seat appears twice): %+v", report.Cost.UniqueModels, report.Cost)
	}
	if report.Status != StatusOK {
		t.Errorf("status = %q, want %q", report.Status, StatusOK)
	}
	if report.Rollback != nil {
		t.Errorf("rollback = %+v on a verified run, want nil", report.Rollback)
	}
	if !strings.Contains(report.Render(), "verify: go build ./... PASS attempt=1") {
		t.Errorf("render = %q, want the passing verification line", report.Render())
	}
}

func TestAssembleReportWithoutTelemetrySaysSo(t *testing.T) {
	report := AssembleReport(CodeConfig{OutDir: t.TempDir()}, nil, nil)
	if len(report.Seats) != 0 {
		t.Fatalf("seats = %d, want 0", len(report.Seats))
	}
	if !strings.Contains(report.Cost.Note, "no summary.json") {
		t.Errorf("cost note = %q, want it to explain the missing telemetry", report.Cost.Note)
	}
	if report.Status != StatusDryRun {
		t.Errorf("status = %q, want %q for a run with no phase", report.Status, StatusDryRun)
	}
}

func TestReportRollbackLineOnlyOnUnprovenWrites(t *testing.T) {
	outDir := t.TempDir()
	ran := []string{filepath.Join(outDir, PhaseWrite)}
	cases := []struct {
		name   string
		phases []*PhaseResult
		write  *WritePhaseResult
		want   bool
	}{
		{
			name:  "ok",
			write: &WritePhaseResult{Status: StatusOK, FanoutRuns: ran, Verified: true, Verify: VerifyResult{Passed: true}},
			want:  false,
		},
		{name: "unverified", write: &WritePhaseResult{Status: StatusUnverified, FanoutRuns: ran}, want: true},
		{name: "verify-failed", write: &WritePhaseResult{Status: StatusVerifyFailed, FanoutRuns: ran}, want: true},
		{name: "write-failed", write: &WritePhaseResult{Status: StatusWriteFailed, FanoutRuns: ran}, want: true},
		{
			// A repair spawn failure after the write: the tree is written and no
			// verification proved it, so the undo command must still be named.
			name:  "failed-after-write",
			write: &WritePhaseResult{Status: StatusFailed, FanoutRuns: ran},
			want:  true,
		},
		{name: "consent-required", write: &WritePhaseResult{Status: StatusConsentRequired}, want: false},
		{name: "dry-run", write: &WritePhaseResult{Status: StatusDryRun}, want: false},
		{
			// A plan-only failure: nothing was written, so there is nothing to undo.
			name:   "plan-failed-no-write",
			phases: []*PhaseResult{{Phase: PhasePlan, Status: StatusFailed, OutDir: filepath.Join(outDir, PhasePlan)}},
			want:   false,
		},
		{
			name:   "rolled-back",
			phases: []*PhaseResult{{Phase: PhaseRollback, Status: StatusRolledBack, OutDir: outDir}},
			write:  &WritePhaseResult{Status: StatusVerifyFailed, FanoutRuns: ran},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := AssembleReport(CodeConfig{OutDir: outDir}, tc.phases, tc.write)
			hasLine := strings.Contains(report.Render(), "rollback: agy pro code rollback --run")
			if (report.Rollback != nil) != tc.want || hasLine != tc.want {
				t.Fatalf("rollback block=%v line=%v, want %v\n%s", report.Rollback != nil, hasLine, tc.want, report.Render())
			}
		})
	}
}

func TestReportMasksScopeOperandSeed(t *testing.T) {
	outDir := t.TempDir()
	cfg := CodeConfig{OutDir: outDir, Scope: []string{"internal/" + plantedKey + "/x.go"}}
	write := &WritePhaseResult{
		Status: StatusUnverified,
		Diff: []codesnap.FileDelta{{
			Path:   "internal/" + plantedKey + "/x.go",
			Status: codesnap.StatusModified,
			Added:  1,
		}},
	}
	rendered := AssembleReport(cfg, write2Phases(), write).Render()
	if strings.Contains(rendered, plantedKey) {
		t.Fatalf("render leaked a scope-seeded secret:\n%s", rendered)
	}
}

// write2Phases is the empty phase slice helper for tests that only need the
// write half.
func write2Phases() []*PhaseResult { return nil }

// TestReportCostNamesCardinalityHonestly pins the renamed CostSummary fields: the
// counts are seat OCCURRENCES (one row per seat per phase run), the distinct-model
// count is its own field, and neither number is named in a way that invites the
// other reading.
func TestReportCostNamesCardinalityHonestly(t *testing.T) {
	outDir := t.TempDir()
	planDir := filepath.Join(outDir, PhasePlan)
	reviewDir := filepath.Join(outDir, PhaseReview)
	writeSummaryFixture(t, planDir, "plan-audit",
		narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil),
		narrativeSeat(2, "native", "claude-sonnet-5-5-high", "success", nil, nil))
	writeSummaryFixture(t, reviewDir, "review",
		narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil),
		narrativeSeat(2, "native", "claude-sonnet-5-5-high", "success", nil, nil))

	report := AssembleReport(CodeConfig{OutDir: outDir}, []*PhaseResult{
		{Phase: PhasePlan, Status: StatusOK, Ran: true, OutDir: planDir},
		{Phase: PhaseReview, Status: StatusOK, Ran: true, OutDir: reviewDir},
	}, nil)
	if report.Cost.NativeSeatOccurrences != 4 || report.Cost.ProviderSeatOccurrences != 0 {
		t.Fatalf("cost = %+v, want 4 native seat occurrences", report.Cost)
	}
	if report.Cost.UniqueModels != 2 {
		t.Errorf("unique models = %d, want 2 (four rows, two models)", report.Cost.UniqueModels)
	}
	if !strings.Contains(report.Render(), "seats: 4 seat occurrence(s) (4 native, 0 provider, 2 unique model(s))") {
		t.Errorf("render =\n%s\nwant the occurrence/model wording", report.Render())
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, want := range []string{`"native_seat_occurrences": 4`, `"provider_seat_occurrences": 0`, `"unique_seat_models": 2`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON missing %q:\n%s", want, data)
		}
	}
	for _, stale := range []string{`"native_seats"`, `"provider_seats"`} {
		if strings.Contains(string(data), stale) {
			t.Errorf("JSON still carries the ambiguous key %s:\n%s", stale, data)
		}
	}
	// Six seats is the same shape: no failed seat means no qualification.
	if report.SeatStatus != "" {
		t.Errorf("seat_status = %q on an all-success run, want empty", report.SeatStatus)
	}
	if strings.Contains(report.Render(), StatusDegraded) {
		t.Errorf("render =\n%s\nwant no degraded wording on an all-success run", report.Render())
	}
}
