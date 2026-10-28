package main

// Pins for the FIX-C half of round-2 review reconciliation at the VERB level:
// C2 (a finished phase's failure reason reaches stderr), C5 (--run is the single
// source of truth for review/rollback), FIX-B handoff item 4 (a refused plan/scope
// is a usage error, exit 2) and the two preemptive rows (a blank --verify operand
// is exit 2; a degraded plan phase still exits 0 while an all-failed one exits 1).
//
// Every case stubs codework.RunFanout (or the codesnap seam), so nothing spawns a
// seat and no provider is charged.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
	"agypro/internal/codework"
)

// codeArgValue returns the value following flag in a composed fanout vector.
func codeArgValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// codeSeatRow builds one seat entry of a fanout summary.json.
func codeSeatRow(id int, kind, model, status string) map[string]any {
	return map[string]any{
		"seat_id": id,
		"kind":    kind,
		"initial_target": map[string]any{
			"provider": map[string]any{"name": "fixture"},
			"model":    map[string]any{"id": model},
		},
		"final_target": map[string]any{
			"provider": map[string]any{"name": "fixture"},
			"model":    map[string]any{"id": model},
		},
		"status": status,
	}
}

// writeCodeSummary writes the summary.json the engine would have written into dir:
// the seat rows a phase reads its verdict from.
func writeCodeSummary(t *testing.T, dir string, seats ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	document := map[string]any{
		"task":            "canned",
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
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}
}

// withCodeSummaryFanout stubs the fanout seam so every call writes a canned
// summary.json into its own --out directory, and returns the recorded calls.
func withCodeSummaryFanout(t *testing.T, seats ...map[string]any) *[][]string {
	t.Helper()
	return withCodeFanout(t, func(argv []string, _ io.Writer) int {
		if dir := codeArgValue(argv, "--out"); dir != "" {
			writeCodeSummary(t, dir, seats...)
		}
		return 0
	})
}

// TestCodeReviewRefusesDivergentOut pins C5's review half: --run is the run being
// reviewed, so an --out naming another directory is exit 2 before anything runs.
func TestCodeReviewRefusesDivergentOut(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	other := filepath.Join(filepath.Dir(out), "other-run")
	for _, dir := range []string{out, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, codeReportFileName), []byte("agy pro code: ok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil, "review", "--run", out, "--out", other, "--root", root)
	if code != 2 {
		t.Fatalf("review with a divergent --out exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused --run/--out pair spawned %d fanout call(s); want 0", len(*calls))
	}
	for _, want := range []string{"name different directories", "--out", "--run"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stdout, "agy pro code:") {
		t.Errorf("a usage refusal printed a report block:\n%s", stdout)
	}

	// An --out that names the SAME directory is accepted (it is the historical
	// spelling), and the report comes from --run.
	code, stdout, stderr = codeInvoke(t, nil, "review", "--run", out, "--out", filepath.Join(out, "."), "--root", root)
	if code != 0 {
		t.Fatalf("review with a redundant --out exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("review made %d fanout call(s); want 1", len(*calls))
	}
	if !strings.Contains(stdout, "agy pro code:") {
		t.Errorf("review printed no report:\n%s", stdout)
	}
}

// TestCodeRollbackRefusesDivergentOut pins C5's rollback half: the manifest comes
// from --run, so a divergent --out cannot redirect the restore, and the tree is
// left exactly as it was.
func TestCodeRollbackRefusesDivergentOut(t *testing.T) {
	withCodePhaseStreams(t)
	// The fabricated run directory carries a provenance sidecar bound to a key under
	// the agy-pro state home (FIX-F4); keep it out of the operator's real home.
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	root, out := codeRoot(t)
	original := []byte("package pkg\n\n// original\n")
	target := filepath.Join(root, "pkg", "a.go")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codesnap.Take(root, []string{"pkg"}, out); err != nil {
		t.Fatalf("fabricate the run: %v", err)
	}
	// The run's write.
	written := []byte("package pkg\n\n// written by the run\n")
	if err := os.WriteFile(target, written, 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(out), "other-run")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := codeInvoke(t, nil, "rollback", "--run", out, "--out", other, "--root", root)
	if code != 2 {
		t.Fatalf("rollback with a divergent --out exit = %d; want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "name different directories") {
		t.Errorf("refusal does not name the divergent pair:\n%s", stderr)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(written) {
		t.Error("a refused rollback still touched the tree")
	}

	// The same pair spelled redundantly restores from --run.
	code, _, stderr = codeInvoke(t, nil, "rollback", "--run", out, "--out", out, "--root", root)
	if code != 0 {
		t.Fatalf("rollback with a redundant --out exit = %d; want 0\nstderr:\n%s", code, stderr)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Errorf("restored bytes = %q; want %q", string(restored), string(original))
	}
}

// TestCodeRollbackWithoutManifestIsExitOne pins the documented code: a missing
// artifact is a RUN failure (1), not a usage error (2) - the invocation is right.
func TestCodeRollbackWithoutManifestIsExitOne(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := codeInvoke(t, nil, "rollback", "--run", out, "--root", root)
	if code != 1 {
		t.Fatalf("rollback without a manifest exit = %d; want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "no usable manifest") {
		t.Errorf("refusal does not name the missing manifest:\n%s", stderr)
	}
}

// TestCodeVerifyBlankOperandExitsTwo pins the preemptive row: `--verify ""` used to
// be skipped in silence while the docs promised exit 2.
func TestCodeVerifyBlankOperandExitsTwo(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--verify", "", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("blank --verify exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a blank --verify spawned %d fanout call(s); want 0", len(*calls))
	}
	if !strings.Contains(stderr, "empty command line") {
		t.Errorf("refusal does not say why:\n%s", stderr)
	}
	if !strings.Contains(stderr, "drop the flag") {
		t.Errorf("refusal does not name the fix:\n%s", stderr)
	}
}

// TestCodeWriteRefusedPlanScopeExitsTwo pins FIX-B handoff item 4: a --plan whose
// unit scope names a governance path is a usage-class refusal (exit 2), and
// nothing started.
func TestCodeWriteRefusedPlanScopeExitsTwo(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	planPath := filepath.Join(filepath.Dir(out), "plan.json")
	plan := `[{"id":"u1","title":"t","scope":["rules/team.md"],"deliverable":"d","seat":"nw/glm-5.3-flash","write":true}]`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil, "write", "--plan", planPath, "--yes", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("refused plan scope exit = %d; want 2 (was 1 before FIX-C)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused plan spawned %d fanout call(s); want 0", len(*calls))
	}
	for _, want := range []string{"rules/team.md", "governance/credential path", "before the snapshot"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err == nil {
		t.Error("a refused plan still took a snapshot")
	}
}

// TestCodeWritePrintsThePhaseErrorReason pins C2 at the verb: a phase that
// finished, produced a report and failed still says WHY on stderr, and the hint
// names the run directory instead of `--run .` (C4).
func TestCodeWritePrintsThePhaseErrorReason(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	withCodeFanout(t, nil)
	prev := codework.ComputeDiff
	codework.ComputeDiff = func(string, *codesnap.Manifest) ([]codesnap.FileDelta, error) {
		return nil, errors.New("snapshot manifest is unreadable")
	}
	t.Cleanup(func() { codework.ComputeDiff = prev })

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--root", root, "--out", out)
	if code != 1 {
		t.Fatalf("diff-failure exit = %d; want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	// The verdict, not just the exit code.
	if !strings.Contains(stdout, "agy pro code: "+codework.StatusFailed) {
		t.Errorf("report does not carry the failed status:\n%s", stdout)
	}
	if !strings.Contains(stdout, "rollback: agy pro code rollback --run") {
		t.Errorf("report does not name the rollback command:\n%s", stdout)
	}
	// The reason, on the error stream.
	for _, want := range []string{
		"cannot diff the tree against the snapshot",
		"snapshot manifest is unreadable",
		"--run " + out,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "--run .") {
		t.Errorf("stderr carries the `--run .` hint:\n%s", stderr)
	}
}

// TestCodePlanDegradedExitsZeroAndAllFailedExitsOne pins the exit-code decision for
// C3: a partly failed seat list keeps exit 0 (the artifacts exist and the status
// token says what is missing), while a phase whose every seat failed exits 1.
func TestCodePlanDegradedExitsZeroAndAllFailedExitsOne(t *testing.T) {
	t.Run("one of two seats failed keeps exit 0", func(t *testing.T) {
		withCodePhaseStreams(t)
		root, out := codeRoot(t)
		calls := withCodeSummaryFanout(t,
			codeSeatRow(1, "native", "gemini-3.8-flash-high", "success"),
			codeSeatRow(2, "native", "claude-sonnet-5-5-high", "failed"),
		)
		code, stdout, stderr := codeInvoke(t, nil, "plan", "--ask", "audit this", "--root", root, "--out", out)
		if code != 0 {
			t.Fatalf("degraded plan exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if len(*calls) != 1 {
			t.Fatalf("plan made %d fanout call(s); want 1", len(*calls))
		}
		if !strings.Contains(stdout, "agy pro code: "+codework.StatusDegraded) {
			t.Errorf("report does not carry the degraded status:\n%s", stdout)
		}
		if !strings.Contains(stdout, "degraded: 1 of 2 seat(s) did not report success") {
			t.Errorf("report does not name the failed seat:\n%s", stdout)
		}
		if !strings.Contains(stderr, "the phase verdict is degraded") {
			t.Errorf("phase stream does not name the verdict:\n%s", stderr)
		}
	})

	t.Run("every seat failed exits 1", func(t *testing.T) {
		withCodePhaseStreams(t)
		root, out := codeRoot(t)
		withCodeSummaryFanout(t,
			codeSeatRow(1, "native", "gemini-3.8-flash-high", "failed"),
			codeSeatRow(2, "native", "claude-sonnet-5-5-high", "failed"),
		)
		code, stdout, stderr := codeInvoke(t, nil, "plan", "--ask", "audit this", "--root", root, "--out", out)
		if code != 1 {
			t.Fatalf("all-failed plan exit = %d; want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "every seat failed") {
			t.Errorf("the reason is not on stderr:\n%s", stderr)
		}
		if !strings.Contains(stdout, "agy pro code: "+codework.StatusFailed) {
			t.Errorf("report does not carry the failed status:\n%s", stdout)
		}
		if !strings.Contains(stdout, "failed: 2 of 2 seat(s) did not report success") {
			t.Errorf("report does not name the all-failed tally:\n%s", stdout)
		}
	})
}

// TestCodeUsageMentionsCompileOnlyDetection pins the FIX-B handoff item 6 wording:
// the shipped help no longer promises bare "detected" verification.
func TestCodeUsageMentionsCompileOnlyDetection(t *testing.T) {
	if strings.Contains(proCodeUsage, "omit = detected") {
		t.Error("proCodeUsage still says `omit = detected`")
	}
	if !strings.Contains(proCodeUsage, "omit = compile-only go build+go vet detection") {
		t.Error("proCodeUsage does not say `omit = compile-only go build+go vet detection`")
	}
	// The written contract for review/rollback is in the usage text too.
	if !strings.Contains(proCodeUsage, "--run DIR is the single source of truth") {
		t.Error("proCodeUsage does not state the --run/--out contract")
	}
	if !strings.Contains(proCodeUsage, "a degraded phase still exits 0") {
		t.Error("proCodeUsage does not state the degraded exit code")
	}
}
