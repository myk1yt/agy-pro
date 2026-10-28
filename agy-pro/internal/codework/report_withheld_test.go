package codework

// Tests for the two R3-10 report findings (fix unit D3):
//
//   - S5 surfaced: the manifest's secret-class withholdings reach the report as a
//     bounded one-line tally (and as `withheld` rows in the JSON), because the
//     data was in manifest.json and nothing printed it.
//   - the rollback hint is shell-safe: a run directory that carries whitespace is
//     quoted, so the command the report names still splits into one operand.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
	"agypro/internal/providers"
)

// withheldManifest builds the manifest shape codesnap.Take writes: captured rows
// plus the skipped rows, each carrying codesnap.ReasonSecretClass and a family.
func withheldManifest(paths ...string) *codesnap.Manifest {
	m := &codesnap.Manifest{
		Version:     codesnap.ManifestVersion,
		Root:        ".",
		Scopes:      []string{"src/**"},
		SnapshotDir: "snapshot",
		Files:       []codesnap.ManifestRow{},
		Skipped:     []codesnap.SkippedFile{},
	}
	for _, p := range paths {
		m.Skipped = append(m.Skipped, codesnap.SkippedFile{
			Path:   p,
			Reason: codesnap.ReasonSecretClass + " (*.env): withheld from the snapshot, no bytes copied",
		})
	}
	return m
}

// withheldLines returns the block's lines that start with the withholding tally.
func withheldLines(rendered string) []string {
	var out []string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "withheld:") {
			out = append(out, line)
		}
	}
	return out
}

// TestReportSurfacesWithheldSecretFiles pins the S5 report line: the withheld rows
// are in the JSON, the block carries exactly one tally line, and the file names
// themselves stay out of the block (a secret-class name is the one string the
// tally must not widen).
func TestReportSurfacesWithheldSecretFiles(t *testing.T) {
	outDir := t.TempDir()
	write := &WritePhaseResult{
		Status:     StatusOK,
		Attempts:   1,
		ExitCode:   0,
		Verified:   true,
		Verify:     VerifyResult{Argv: []string{"go", "build", "./..."}, Passed: true},
		FanoutRuns: []string{filepath.Join(outDir, PhaseWrite)},
		Diff:       []codesnap.FileDelta{},
		Manifest:   withheldManifest("config/app.pem", "keys/server.key", "src/dev.env"),
	}
	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)

	if len(report.Withheld) != 3 {
		t.Fatalf("withheld = %+v, want the 3 secret-class rows", report.Withheld)
	}
	for i, want := range []string{"config/app.pem", "keys/server.key", "src/dev.env"} {
		if report.Withheld[i].Path != want {
			t.Errorf("withheld[%d].Path = %q, want %q (the manifest's recorded order)", i, report.Withheld[i].Path, want)
		}
	}

	rendered := report.Render()
	lines := withheldLines(rendered)
	if len(lines) != 1 {
		t.Fatalf("render has %d withholding lines, want exactly 1:\n%s", len(lines), rendered)
	}
	if want := "withheld: 3 secret-class file(s) not snapshotted"; lines[0] != want {
		t.Errorf("withholding line = %q, want %q", lines[0], want)
	}
	for _, path := range []string{"config/app.pem", "keys/server.key", "src/dev.env"} {
		if strings.Contains(rendered, path) {
			t.Errorf("render names the withheld path %q; the tally carries the count only:\n%s", path, rendered)
		}
	}

	// The JSON is where a script reads the rows.
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var decoded CodeRunReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Withheld) != 3 {
		t.Fatalf("JSON withheld = %+v, want 3 rows:\n%s", decoded.Withheld, data)
	}
	if decoded.Withheld[0].Path != "config/app.pem" || !strings.Contains(decoded.Withheld[0].Reason, codesnap.ReasonSecretClass) {
		t.Errorf("JSON withheld row = %+v, want the path and the engine's reason", decoded.Withheld[0])
	}
}

// TestReportWithheldLineAppearsOnlyWhenSomethingWasWithheld pins the "only when
// >0" half: a manifest with no secret-class row renders no line and omits the JSON
// key, so a run over a clean tree looks exactly as it did before this finding.
func TestReportWithheldLineAppearsOnlyWhenSomethingWasWithheld(t *testing.T) {
	outDir := t.TempDir()
	write := &WritePhaseResult{
		Status:     StatusOK,
		FanoutRuns: []string{filepath.Join(outDir, PhaseWrite)},
		Diff:       []codesnap.FileDelta{},
		Manifest: &codesnap.Manifest{
			Version: codesnap.ManifestVersion,
			Files:   []codesnap.ManifestRow{},
			// A non-secret skip reason must not be counted: the line is about the
			// secret class, not about every row the ledger could not handle.
			Skipped: []codesnap.SkippedFile{{Path: "src/pipes", Reason: "not a regular file (p)"}},
		},
	}
	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)

	if len(report.Withheld) != 0 {
		t.Errorf("withheld = %+v, want none for a non-secret skip", report.Withheld)
	}
	if rendered := report.Render(); strings.Contains(rendered, "withheld:") {
		t.Errorf("render carries a withholding line on a run that withheld nothing:\n%s", rendered)
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(data), `"withheld"`) {
		t.Errorf("JSON carries the withheld key on a run that withheld nothing:\n%s", data)
	}
}

// TestReportWithheldRowsDedupeTheSharedBaseline pins the composed-run shape: the
// plan phase takes the snapshot and the write phase reuses it, so the same
// manifest arrives twice. The tally counts files, not mentions.
func TestReportWithheldRowsDedupeTheSharedBaseline(t *testing.T) {
	outDir := t.TempDir()
	manifest := withheldManifest("config/app.pem", "keys/server.key")
	phase := &PhaseResult{
		Phase:    PhasePlan,
		Status:   StatusOK,
		Ran:      true,
		OutDir:   filepath.Join(outDir, PhasePlan),
		Manifest: manifest,
	}
	write := &WritePhaseResult{
		Status:     StatusOK,
		FanoutRuns: []string{filepath.Join(outDir, PhaseWrite)},
		Diff:       []codesnap.FileDelta{},
		Manifest:   manifest,
	}
	report := AssembleReport(CodeConfig{OutDir: outDir}, []*PhaseResult{phase}, write)
	if len(report.Withheld) != 2 {
		t.Fatalf("withheld = %+v, want 2 rows for the two withheld files (the baseline is one document)", report.Withheld)
	}
	if got := withheldLines(report.Render()); len(got) != 1 || got[0] != "withheld: 2 secret-class file(s) not snapshotted" {
		t.Fatalf("withholding lines = %v, want one line naming 2 files", got)
	}
}

// TestReportQuotesSpacedRunDirInRollbackHint pins the quoting rule: the rollback
// command names the run directory exactly, quoted when that path carries
// whitespace, so the operator's shell receives one operand.
func TestReportQuotesSpacedRunDirInRollbackHint(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{name: "no-space stays bare", dir: filepath.Join("reports", "code", "20261024-051500")},
		{name: "space is quoted", dir: filepath.Join("reports", "code", "my run")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write := &WritePhaseResult{
				Status:     StatusVerifyFailed,
				Attempts:   1,
				Verified:   true,
				Verify:     VerifyResult{Argv: []string{"go", "build", "./..."}, ExitCode: 1},
				FanoutRuns: []string{filepath.Join(tc.dir, PhaseWrite)},
				Diff:       []codesnap.FileDelta{},
			}
			report := AssembleReport(CodeConfig{OutDir: tc.dir}, nil, write)
			if report.Rollback == nil {
				t.Fatalf("rollback block is nil on a verify-failed run with a write")
			}
			wantHint := fmt.Sprintf(rollbackHintFmt, tc.dir)
			if strings.ContainsRune(tc.dir, ' ') {
				wantHint = fmt.Sprintf(rollbackHintFmt, `"`+tc.dir+`"`)
			}
			if report.Rollback.Command != wantHint {
				t.Errorf("rollback command = %q, want %q", report.Rollback.Command, wantHint)
			}
			if !strings.Contains(report.Render(), "rollback: "+report.Rollback.Command) {
				t.Errorf("render does not carry the quoted command:\n%s", report.Render())
			}
			// The run directory FIELD stays a bare path: it is data, not a command.
			if want := providers.MaskOperand(filepath.Clean(tc.dir)); report.Rollback.RunDir != want {
				t.Errorf("rollback run_dir = %q, want the plain cleaned path %q", report.Rollback.RunDir, want)
			}
		})
	}
}

// TestReportWorstCaseBlockStaysWithinBudget pins maxReportLines against the
// largest block this report can produce: every optional line present at once
// (three verify rows + tail, five file rows + tail, per-phase drift and
// governance-drift lines plus the note, the withholding tally, a hoisted seat
// header with five rows + tail, cost, artifacts, rollback).
func TestReportWorstCaseBlockStaysWithinBudget(t *testing.T) {
	outDir := t.TempDir()
	planDir := filepath.Join(outDir, PhasePlan)
	reviewDir := filepath.Join(outDir, PhaseReview)
	writeSummaryFixture(t, planDir, "plan-audit",
		narrativeSeat(1, "native", "gemini-3.8-flash-high", "success", nil, nil),
		narrativeSeat(2, "native", "claude-sonnet-5-5-high", "failed", nil, nil))
	writeSummaryFixture(t, reviewDir, "review",
		narrativeSeat(1, "native", "gemini-3.8-flash-high", "failed", nil, nil),
		narrativeSeat(2, "native", "claude-sonnet-5-5-high", "success", nil, nil))

	drift := func(prefix string, n int) []codesnap.FileDelta {
		out := make([]codesnap.FileDelta, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, codesnap.FileDelta{Path: fmt.Sprintf("%s%02d.go", prefix, i), Status: codesnap.StatusModified, Added: 1})
		}
		return out
	}
	phases := []*PhaseResult{
		{Phase: PhasePlan, Status: StatusDegraded, Ran: true, OutDir: planDir, Drift: drift("plan/", 4), GovernanceDrift: drift("gov/", 4), DriftNote: driftNote},
		{Phase: PhaseReview, Status: StatusDegraded, Ran: true, OutDir: reviewDir, Drift: drift("review/", 4), GovernanceDrift: drift("gov/", 4), DriftNote: driftNote},
	}
	write := failingWriteResult(outDir, 40)
	write.Manifest = withheldManifest("keys/a.env", "keys/b.env")
	write.VerifyRuns = []VerifyRun{
		{Attempt: 1, VerifyResult: VerifyResult{Argv: []string{"go", "build", "./..."}, ExitCode: 1}},
		{Attempt: 2, VerifyResult: VerifyResult{Argv: []string{"go", "vet", "./..."}, ExitCode: 1}},
		{Attempt: 2, VerifyResult: VerifyResult{Argv: []string{"go", "test", "./..."}, ExitCode: 1}},
	}

	rendered := AssembleReport(CodeConfig{OutDir: outDir}, phases, write).Render()
	lines := strings.Split(rendered, "\n")
	if len(lines) > maxReportLines {
		t.Fatalf("worst-case render has %d lines, budget is %d:\n%s", len(lines), maxReportLines, rendered)
	}
	// Non-vacuity: the block really does carry each optional line, so the budget
	// above is measured against the maximum and not against a smaller block.
	for _, want := range []string{"verify: ", "... ", "files: ", "withheld: ", "drift: ", "governance-drift: ", "note: ", "seats: ", "cost: ", "artifacts: ", "rollback: "} {
		if !strings.Contains(rendered, want) {
			t.Errorf("worst-case render is missing %q:\n%s", want, rendered)
		}
	}
}
