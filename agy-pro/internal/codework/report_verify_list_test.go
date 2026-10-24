package codework

// Pins for the report's verification list (U5b): the run can name several
// verifiers, so the machine block carries one row per run and the human block
// renders them bounded.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
)

// verifyRunRow builds one recorded verification run.
func verifyRunRow(attempt int, argv []string, passed bool, outDir, log string) VerifyRun {
	run := VerifyRun{Attempt: attempt}
	run.Argv = argv
	run.Passed = passed
	run.LogPath = filepath.Join(outDir, verifyDirName, log)
	if !passed {
		run.ExitCode = 1
	}
	return run
}

// TestReportListsEveryVerificationRun: three runs across two attempts become
// three rows, in execution order, with work-directory-relative log paths.
func TestReportListsEveryVerificationRun(t *testing.T) {
	outDir := t.TempDir()
	write := &WritePhaseResult{
		Status:   StatusVerifyFailed,
		Attempts: 2,
		Verified: true,
		Verify:   VerifyResult{Argv: []string{"go", "vet", "./..."}, ExitCode: 1, LogPath: filepath.Join(outDir, verifyDirName, "attempt-2-2.log")},
		VerifyRuns: []VerifyRun{
			verifyRunRow(1, []string{"go", "build", "./..."}, false, outDir, "attempt-1-1.log"),
			verifyRunRow(2, []string{"go", "build", "./..."}, true, outDir, "attempt-2-1.log"),
			verifyRunRow(2, []string{"go", "vet", "./..."}, false, outDir, "attempt-2-2.log"),
		},
		Diff: []codesnap.FileDelta{},
	}

	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)
	if len(report.Verify) != 3 {
		t.Fatalf("verify rows = %d, want 3: %+v", len(report.Verify), report.Verify)
	}
	wantAttempts := []int{1, 2, 2}
	wantCommands := []string{"go build ./...", "go build ./...", "go vet ./..."}
	for i, row := range report.Verify {
		if row.Attempt != wantAttempts[i] {
			t.Errorf("row %d attempt = %d, want %d", i+1, row.Attempt, wantAttempts[i])
		}
		if got := strings.Join(row.Argv, " "); got != wantCommands[i] {
			t.Errorf("row %d argv = %q, want %q", i+1, got, wantCommands[i])
		}
		if filepath.IsAbs(row.Log) || filepath.Dir(row.Log) != verifyDirName {
			t.Errorf("row %d log = %q, want a path relative to the run directory", i+1, row.Log)
		}
	}
	if report.Verify[0].Passed || !report.Verify[1].Passed || report.Verify[2].Passed {
		t.Errorf("pass flags = %v/%v/%v, want false/true/false",
			report.Verify[0].Passed, report.Verify[1].Passed, report.Verify[2].Passed)
	}

	rendered := report.Render()
	for _, want := range []string{
		"verify: go build ./... FAIL attempt=1",
		"verify: go build ./... PASS attempt=2",
		"verify: go vet ./... FAIL attempt=2",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("render missing %q:\n%s", want, rendered)
		}
	}

	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var decoded CodeRunReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Verify) != 3 {
		t.Fatalf("decoded verify rows = %d, want 3", len(decoded.Verify))
	}
	if decoded.Verify[2].Attempt != 2 || len(decoded.Verify[2].Argv) != 3 {
		t.Errorf("decoded last row = %+v, want attempt 2 with a 3-token argv", decoded.Verify[2])
	}
}

// TestReportVerifyRowsAreBounded proves the human block stays inside
// maxReportLines even when a run collected many verification rows.
func TestReportVerifyRowsAreBounded(t *testing.T) {
	outDir := t.TempDir()
	runs := make([]VerifyRun, 0, maxReportVerifyRows+2)
	for i := 0; i < maxReportVerifyRows+2; i++ {
		runs = append(runs, verifyRunRow(1, []string{"go", "build", "./pkg" + string(rune('a'+i))}, true, outDir, "attempt-1-"+string(rune('1'+i))+".log"))
	}
	write := &WritePhaseResult{
		Status:     StatusOK,
		Attempts:   1,
		Verified:   true,
		VerifyRuns: runs,
		Diff:       []codesnap.FileDelta{},
	}
	rendered := AssembleReport(CodeConfig{OutDir: outDir}, nil, write).Render()
	if lines := strings.Split(rendered, "\n"); len(lines) > maxReportLines {
		t.Fatalf("render has %d lines, budget is %d:\n%s", len(lines), maxReportLines, rendered)
	}
	if !strings.Contains(rendered, "... 2 more") {
		t.Errorf("render does not bound the verify rows:\n%s", rendered)
	}
	if n := strings.Count(rendered, "verify: "); n != maxReportVerifyRows {
		t.Errorf("render shows %d verify rows, want %d:\n%s", n, maxReportVerifyRows, rendered)
	}
}

// TestReportFallsBackToTheDecisiveRun covers a caller-built WritePhaseResult
// that filled only Verify: "verified" must never render as an empty block.
func TestReportFallsBackToTheDecisiveRun(t *testing.T) {
	outDir := t.TempDir()
	write := &WritePhaseResult{
		Status:   StatusVerifyFailed,
		Attempts: 2,
		Verified: true,
		Verify:   VerifyResult{Argv: []string{"go", "test", "./x"}, ExitCode: 1, LogPath: filepath.Join(outDir, verifyDirName, "attempt-2-1.log")},
		Diff:     []codesnap.FileDelta{},
	}
	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)
	if len(report.Verify) != 1 {
		t.Fatalf("verify rows = %d, want the decisive run as one row", len(report.Verify))
	}
	if report.Verify[0].Attempt != 2 {
		t.Errorf("attempt = %d, want the write attempt count", report.Verify[0].Attempt)
	}
	if !strings.Contains(report.Render(), "verify: go test ./x FAIL attempt=2") {
		t.Errorf("render = %q, want the decisive line", report.Render())
	}
}

// TestReportUnverifiedHasNoVerifyRows: no verifier, no rows, and the render says
// so instead of rendering an empty block.
func TestReportUnverifiedHasNoVerifyRows(t *testing.T) {
	outDir := t.TempDir()
	write := &WritePhaseResult{Status: StatusUnverified, Diff: []codesnap.FileDelta{}}
	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)
	if len(report.Verify) != 0 {
		t.Fatalf("verify rows = %+v, want none", report.Verify)
	}
	if !strings.Contains(report.Render(), "verify: none detected") {
		t.Errorf("render = %q, want the unverified line", report.Render())
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(data), `"verify"`) {
		t.Errorf("JSON carries an empty verify block:\n%s", data)
	}
}

// TestReportMarksABudgetSkippedRow pins the distinct row token for a verifier that
// never ran because the run's budget was spent: "the verifier failed" and "the
// verifier never started" are different facts, and the write phase's status stays
// verify-failed for both.
func TestReportMarksABudgetSkippedRow(t *testing.T) {
	outDir := t.TempDir()
	skipped := VerifyRun{Attempt: 1, VerifyResult: VerifyResult{
		Argv:       []string{"go", "vet", "./..."},
		ExitCode:   exitNotRun,
		OutputTail: verifyBudgetMarker,
	}}
	write := &WritePhaseResult{
		Status:     StatusVerifyFailed,
		Attempts:   1,
		Verified:   true,
		VerifyRuns: []VerifyRun{verifyRunRow(1, []string{"go", "build", "./..."}, true, outDir, "attempt-1-1.log"), skipped},
		Diff:       []codesnap.FileDelta{},
	}

	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)
	if len(report.Verify) != 2 {
		t.Fatalf("verify rows = %+v, want 2", report.Verify)
	}
	if report.Verify[0].Skipped != "" {
		t.Errorf("row 1 skipped = %q, want empty (it ran and passed)", report.Verify[0].Skipped)
	}
	if report.Verify[1].Skipped != verifySkippedBudgetToken {
		t.Fatalf("row 2 skipped = %q, want %q", report.Verify[1].Skipped, verifySkippedBudgetToken)
	}
	if report.Status != StatusVerifyFailed {
		t.Errorf("status = %q, want %q (a skip is never a pass, and never a new status)", report.Status, StatusVerifyFailed)
	}
	if !strings.Contains(report.Render(), "skipped="+verifySkippedBudgetToken) {
		t.Errorf("render =\n%s\nwant the skipped token on the row", report.Render())
	}
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(data), `"skipped": "`+verifySkippedBudgetToken+`"`) {
		t.Errorf("JSON row does not carry the token:\n%s", data)
	}
	// The token is a ROW fact, not a status: the verified-failed verdict is still
	// exactly the one the rest of the door maps to exit 1.
	if token := verifySkippedBudgetToken; token == StatusVerifyFailed || token == StatusFailed {
		t.Errorf("the row token %q collides with a status", token)
	}
}
