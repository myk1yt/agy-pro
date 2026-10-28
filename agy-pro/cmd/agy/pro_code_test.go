package main

// Pins for the `agy pro code` verb (U5). Every test stubs the library seams
// (codework.RunFanout, the codesnap seams through codework) so NOTHING spawns a
// seat and no provider is charged: the composed argv is asserted on the recorder,
// not inferred from the tree.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
	"agypro/internal/codework"
	"agypro/internal/fanout"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// codeDispatchHelperEnv is the child-side switch for the dispatch pin below.
const codeDispatchHelperEnv = "AGY_PRO_CODE_DISPATCH_HELPER"

// u5FanoutMarker is planted in a fake fanout stdout so the stdout-discipline
// pin can prove the engine document never reaches the report stream.
const u5FanoutMarker = "u5-fanout-summary-marker"

// codeInvoke drives one `agy pro code ...` invocation with buffer streams.
func codeInvoke(t *testing.T, in io.Reader, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	if in == nil {
		in = strings.NewReader("")
	}
	code = runCodeCmd(args, in, &outBuf, &errBuf)
	return code, outBuf.String(), errBuf.String()
}

// withCodeFanout replaces the execute seam and records every composed vector.
// body, when non-nil, runs instead of the "exit 0" default (it receives the
// phase's stdout writer so a test can simulate the engine's own --json echo).
func withCodeFanout(t *testing.T, body func(argv []string, out io.Writer) int) *[][]string {
	t.Helper()
	prev := codework.RunFanout
	calls := &[][]string{}
	codework.RunFanout = func(args []string, _ io.Reader, out, _ io.Writer) int {
		*calls = append(*calls, append([]string(nil), args...))
		if body != nil {
			return body(args, out)
		}
		return 0
	}
	t.Cleanup(func() { codework.RunFanout = prev })
	return calls
}

// withCodePhaseStreams restores the phase streams a subcommand rebinds.
func withCodePhaseStreams(t *testing.T) {
	t.Helper()
	in, out, errw := codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr
	t.Cleanup(func() {
		codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr = in, out, errw
	})
}

// codeRoot builds a temp workspace with one scoped file and returns (root, out)
// as siblings so the run directory is never the workspace root.
func codeRoot(t *testing.T) (root, out string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "ws")
	out = filepath.Join(base, "run")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, out
}

// TestProCodeDispatchHelperProcess is the child side of the dispatch pin: it
// runs the real main() switch in a subprocess so the exit code is a real
// process exit code, exactly like TestProDispatchHelperProcess (main_test.go:709).
func TestProCodeDispatchHelperProcess(t *testing.T) {
	switch os.Getenv(codeDispatchHelperEnv) {
	case "code-help":
		os.Args = []string{"agy-test", "pro", "code", "--help"}
		main()
	case "code-unknown-subcommand":
		os.Args = []string{"agy-test", "pro", "code", "bogus"}
		main()
	case "code-plan-dry-run":
		os.Args = []string{"agy-test", "pro", "code", "plan", "--ask", "x", "--dry-run",
			"--out", filepath.Join(os.Getenv("TEMP"), "agy-pro-u5-dispatch")}
		main()
	}
}

// runCodeDispatchHelper spawns this test binary as a helper subprocess for one
// dispatch case and returns the real exit code plus stdout/stderr.
func runCodeDispatchHelper(t *testing.T, dispatchCase string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProCodeDispatchHelperProcess")
	cmd.Env = append(os.Environ(),
		codeDispatchHelperEnv+"="+dispatchCase,
		paths.EnvAgyProHome+"="+filepath.Join(t.TempDir(), "home"),
		providers.EnvProviders+"="+filepath.Join(t.TempDir(), "missing-providers.json"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("dispatch helper %q failed to run: %v\nstderr:\n%s", dispatchCase, err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// TestProCodeReachesTheProDispatch proves the `case "code"` line in
// main.go's pro-verb switch is wired: the subprocess runs the real main().
func TestProCodeReachesTheProDispatch(t *testing.T) {
	cases := []struct {
		name       string
		dispatch   string
		wantCode   int
		wantStdout []string
	}{
		{
			name:       "code --help prints the verb usage and exits 0",
			dispatch:   "code-help",
			wantCode:   0,
			wantStdout: []string{"Usage: agy pro code <subcommand>"},
		},
		{
			name:       "code plan --dry-run reaches the phase and exits 0",
			dispatch:   "code-plan-dry-run",
			wantCode:   0,
			wantStdout: []string{"agy pro code: dry-run"},
		},
		{
			name:     "code unknown subcommand exits 2",
			dispatch: "code-unknown-subcommand",
			wantCode: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCodeDispatchHelper(t, tc.dispatch)
			if code != tc.wantCode {
				t.Fatalf("dispatch case %q exit code = %d; want %d\nstdout:\n%s\nstderr:\n%s",
					tc.dispatch, code, tc.wantCode, stdout, stderr)
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(stdout, want) {
					t.Errorf("dispatch case %q stdout missing %q:\n%s", tc.dispatch, want, stdout)
				}
			}
		})
	}
}

// TestPrintProHelpHasCodeRow pins the one additive help row.
func TestPrintProHelpHasCodeRow(t *testing.T) {
	out := captureStdout(t, printProHelp)
	want := "code            Plan, write, review, and roll back a bounded multi-seat code change"
	if !strings.Contains(out, want) {
		t.Errorf("help missing the code row %q:\n%s", want, out)
	}
	// The row sits in the Commands block, next to fanout.
	if strings.Index(out, "  fanout ") > strings.Index(out, "  code ") {
		t.Errorf("help row order changed: `code` should follow `fanout`:\n%s", out)
	}
}

// TestCodeUsageErrorMatrix pins the usage matrix: every missing or invalid
// operand is exit 2 BEFORE anything runs, and the echoed operand is masked.
func TestCodeUsageErrorMatrix(t *testing.T) {
	_, emptyRun := codeRoot(t)
	if err := os.MkdirAll(emptyRun, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
		// plant is a credential-shaped operand that must NOT survive to stderr.
		plant string
	}{
		{name: "plan without an ask", args: []string{"plan"}, want: "missing --ask TEXT or --ask-file"},
		{name: "plan with both asks", args: []string{"plan", "--ask", "x", "--ask-file", "y"}, want: "not both"},
		{name: "plan with a negative timeout", args: []string{"plan", "--ask", "x", "--timeout", "-3"}, want: "--timeout must be zero or a positive"},
		{name: "plan with a stray operand", args: []string{"plan", "--ask", "x", "extra"}, want: "unexpected argument"},
		{name: "plan with an unknown flag", args: []string{"plan", "--ask", "x", "--bogus-u5"}, want: "flag provided but not defined"},
		{name: "write without a writer", args: []string{"write", "--ask", "x", "--scope", "pkg/a.go"}, want: "missing --writer"},
		{name: "write without a scope", args: []string{"write", "--ask", "x", "--writer", "nw/glm-5.3-flash"}, want: "missing --scope"},
		{name: "write with a native writer", args: []string{"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "native:foo"}, want: "must be exactly one provider seat"},
		{name: "write with a negative repair budget", args: []string{"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash", "--max-repairs", "-1"}, want: "--max-repairs must be zero or positive"},
		{name: "write with an unsplittable verifier", args: []string{"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash", "--verify", `go "build`}, want: "unbalanced quote"},
		{name: "write with an unreadable plan", args: []string{"write", "--plan", filepath.Join(emptyRun, "nope.json")}, want: "cannot read --plan"},
		{name: "write with plan and ask", args: []string{"write", "--plan", filepath.Join(emptyRun, "nope.json"), "--ask", "x"}, want: "not both"},
		{name: "review without a run directory", args: []string{"review"}, want: "missing --run DIR"},
		{name: "review of a run that never wrote", args: []string{"review", "--run", emptyRun}, want: "run `agy pro code write`"},
		{name: "rollback without a run directory", args: []string{"rollback"}, want: "missing --run DIR"},
		{name: "unknown subcommand", args: []string{"bogus"}, want: "unknown code subcommand"},
		{name: "run with a native writer", args: []string{"run", "--ask", "x", "--scope", "pkg/a.go", "--writer", "native:foo"}, want: "must be exactly one provider seat"},
		{name: "run skip-plan without a scope", args: []string{"run", "--ask", "x", "--skip-plan"}, want: "missing --scope"},
		{
			name:  "unknown subcommand masks a planted key",
			args:  []string{"AKI" + "AIOSFODNN7EXAMPLE-bogus"},
			want:  "unknown code subcommand",
			plant: testfixtures.AKIAExample,
		},
		{
			name:  "native writer masks a planted key",
			args:  []string{"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "native:" + testfixtures.AnthropicPlanted},
			want:  "must be exactly one provider seat",
			plant: testfixtures.AnthropicPlanted,
		},
		{
			name:  "unreadable plan masks a planted key in the path",
			args:  []string{"write", "--plan", filepath.Join(emptyRun, testfixtures.AnthropicPlanted)},
			want:  "cannot read --plan",
			plant: testfixtures.AnthropicPlanted,
		},
		{
			name:  "review run directory masks a planted key in the path",
			args:  []string{"review", "--run", filepath.Join(emptyRun, testfixtures.AnthropicPlanted)},
			want:  "no change report in",
			plant: testfixtures.AnthropicPlanted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := withCodeFanout(t, nil)
			code, stdout, stderr := codeInvoke(t, nil, tc.args...)
			if code != 2 {
				t.Fatalf("agy pro code %v exit = %d; want 2\nstdout:\n%s\nstderr:\n%s",
					tc.args, code, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
			if len(*calls) != 0 {
				t.Errorf("a usage error spawned %d fanout call(s); want 0", len(*calls))
			}
			if tc.plant != "" {
				if strings.Contains(stderr, tc.plant) || strings.Contains(stdout, tc.plant) {
					t.Errorf("planted operand %q survived to the console:\nstderr:\n%s\nstdout:\n%s", tc.plant, stderr, stdout)
				}
			}
			if strings.Contains(stdout, "agy pro code:") {
				t.Errorf("a usage error printed a run report on stdout:\n%s", stdout)
			}
		})
	}
}

// TestCodePlanDefaultsAreTheNativeLineUp ties the documented default line-up to
// the shipped seat grammar: both tokens parse, and both are native (free), so
// the plan and review phases never trigger the consent gate by default.
func TestCodePlanDefaultsAreTheNativeLineUp(t *testing.T) {
	for _, def := range []string{codeDefaultPlanSeats, codeDefaultReviewSeats} {
		specs, err := fanout.ParseSeatSpecs(def)
		if err != nil {
			t.Fatalf("default seats %q do not parse: %v", def, err)
		}
		if len(specs) != 2 {
			t.Fatalf("default seats %q resolve to %d seats; want 2", def, len(specs))
		}
		for _, spec := range specs {
			if spec.Kind != fanout.SeatKindNative {
				t.Errorf("default seat %q is %s; the default line-up must be native (free)", spec.Raw, spec.Kind)
			}
		}
		if got, want := specs[0].ModelID, "gemini-3.8-flash-high"; got != want {
			t.Errorf("default seat 1 = %q; want %q", got, want)
		}
		if got, want := specs[1].ModelID, "claude-sonnet-5-5-high"; got != want {
			t.Errorf("default seat 2 = %q; want %q", got, want)
		}
	}
}

// TestCodePlanDryRunRunsNothing pins the dry-run rule (U4 §4.1): the exact
// vector is echoed, nothing spawns, and the only file the run directory gains is
// the library's own dry-run trail line (phases.go logPhase).
func TestCodePlanDryRunRunsNothing(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, func([]string, io.Writer) int {
		t.Error("dry run must not execute a fanout call")
		return 0
	})

	code, stdout, stderr := codeInvoke(t, nil, "plan", "--ask", "audit this", "--dry-run", "--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("dry-run plan exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Fatalf("dry run made %d fanout call(s); want 0", len(*calls))
	}
	if !strings.Contains(stderr, "agy pro code plan: dry-run (nothing executed)") {
		t.Errorf("dry run did not echo its header:\n%s", stderr)
	}
	for _, want := range []string{
		"agy pro fanout --task plan-audit",
		"--seats " + codeDefaultPlanSeats,
		"--tools read,search",
		"--json",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("dry-run vector missing %q:\n%s", want, stderr)
		}
	}
	if !strings.Contains(stdout, "agy pro code: dry-run") {
		t.Errorf("dry run did not print the report block on stdout:\n%s", stdout)
	}
	// Nothing a real plan writes: no ask file, no run directory, no artifacts.
	for _, unwanted := range []string{
		filepath.Join(out, "plan", "ask.txt"),
		filepath.Join(out, "plan"),
		filepath.Join(out, "plan", "summary.json"),
		filepath.Join(out, "manifest.json"),
	} {
		if _, err := os.Stat(unwanted); err == nil {
			t.Errorf("dry run created %q; want nothing", unwanted)
		}
	}
	// The one artifact the library records for a dry run is its own trail line.
	trail, err := os.ReadFile(filepath.Join(out, "phases.log"))
	if err != nil {
		t.Fatalf("phases.log missing after a dry run (the library records it): %v", err)
	}
	if strings.TrimSpace(string(trail)) != "phase=plan status=dry-run artifacts=-" {
		t.Errorf("phases.log = %q; want the single dry-run line", string(trail))
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "phases.log" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dry-run run directory = %v; want only phases.log", names)
	}
}

// TestCodeWriteRefusesMeteredRunWithoutConsent is the consent pin: a non-TTY
// provider write without --yes is refused, the refusal names --yes, nothing
// spawns, and the run directory holds no write tree.
func TestCodeWriteRefusesMeteredRunWithoutConsent(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, func([]string, io.Writer) int {
		t.Error("a refused consent must not execute a fanout call")
		return 0
	})

	code, stdout, stderr := codeInvoke(t, strings.NewReader(""),
		"write", "--ask", "add a doc comment", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("refused write exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Fatalf("refused write made %d fanout call(s); want 0", len(*calls))
	}
	if !strings.Contains(stderr, "re-run with --yes") {
		t.Errorf("refusal does not name --yes:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the verb hint does not name --yes:\n%s", stderr)
	}
	if !strings.Contains(stderr, "metered seat(s)") {
		t.Errorf("refusal does not echo the metered seats:\n%s", stderr)
	}
	if strings.Contains(stdout, "agy pro code:") {
		t.Errorf("a refusal printed a report block:\n%s", stdout)
	}
	for _, unwanted := range []string{"write", "manifest.json", "snapshot"} {
		if _, err := os.Stat(filepath.Join(out, unwanted)); err == nil {
			t.Errorf("refused write created %q; want nothing", unwanted)
		}
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("run directory missing: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "phases.log" {
		names := make([]string, 0, len(entries))
		for i, e := range entries {
			if i > 8 {
				break
			}
			names = append(names, e.Name())
		}
		t.Errorf("refused run directory = %v; want only phases.log", names)
	}
}

// TestCodeWriteStdoutIsOneDocument is the U4 P2-3 pin: the phase stream carries
// the engine's --json summary, the report stream carries exactly one document,
// and with --json that document parses as the CodeRunReport schema.
func TestCodeWriteStdoutIsOneDocument(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	withCodeFanout(t, func(_ []string, w io.Writer) int {
		// The engine prints its own summary JSON to the phase stdout.
		io.WriteString(w, `{"marker":"`+u5FanoutMarker+`"}`+"\n")
		return 0
	})

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "add a doc comment", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--json", "--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("write exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, u5FanoutMarker) {
		t.Fatalf("the fanout document interleaved with the report on stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, u5FanoutMarker) {
		t.Errorf("the fanout document did not land on the phase (stderr) stream:\n%s", stderr)
	}
	var rep codework.CodeRunReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the CodeRunReport schema: %v\nstdout:\n%s", err, stdout)
	}
	if rep.Status != codework.StatusUnverified {
		t.Errorf("report status = %q; want %q (no verifier in the temp tree)", rep.Status, codework.StatusUnverified)
	}
	if len(rep.Phases) != 1 || rep.Phases[0].Phase != codework.PhaseWrite || !rep.Phases[0].Ran {
		t.Errorf("report phases = %+v; want one ran write phase", rep.Phases)
	}
	// The change report a review reads is written once the tree was touched.
	body, err := os.ReadFile(filepath.Join(out, codeReportFileName))
	if err != nil {
		t.Fatalf("report.txt missing after a write: %v", err)
	}
	if !strings.Contains(string(body), "agy pro code: "+codework.StatusUnverified) {
		t.Errorf("report.txt does not carry the human block:\n%s", string(body))
	}
	// The write tree the engine would have produced is named, not invented.
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Errorf("write phase left no snapshot manifest: %v", err)
	}
}

// TestCodeWriteVerifyFailExitsOne is the verification pin: an ultimately failed
// verification is exit 1 and the report names the rollback command.
func TestCodeWriteVerifyFailExitsOne(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	withCodeFanout(t, nil)

	// A real child process that exits non-zero: this test binary rejects the
	// unknown flag it is given (exit 2), which is a portable failing verifier
	// without a helper test the env allow-list would have to reach.
	verifier := `"` + os.Args[0] + `" -test.run=TestProCodeVerifyFailHelperProcess -agy-pro-u5-force-failure`

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "add a doc comment", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--verify", verifier, "--root", root, "--out", out)
	if code != 1 {
		t.Fatalf("failed verification exit = %d; want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "agy pro code: "+codework.StatusVerifyFailed) {
		t.Errorf("report does not carry the verify-failed status:\n%s", stdout)
	}
	wantRollback := "rollback: agy pro code rollback --run"
	if !strings.Contains(stdout, wantRollback) {
		t.Errorf("report does not name the rollback command (%q):\n%s", wantRollback, stdout)
	}
	if !strings.Contains(stdout, "verify:") || !strings.Contains(stdout, "FAIL") {
		t.Errorf("report does not show the failing verification:\n%s", stdout)
	}
}

// TestCodeWriteRunsBothVerifiersForReal is the end-to-end pin with real child
// processes: the first --verify passes, the second fails, and the report carries
// both rows with the first marked PASS. The passing verifier is this test binary
// with a run filter that matches nothing (go test exits 0); the failing one is
// the same binary given an unknown flag (exit 2).
func TestCodeWriteRunsBothVerifiersForReal(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	withCodeFanout(t, nil)
	passer := `"` + os.Args[0] + `" -test.run=TestNoSuchTestAtAll`
	failer := `"` + os.Args[0] + `" -test.run=TestProCodeVerifyFailHelperProcess -agy-pro-u5-force-failure`

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "add a doc comment", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--json",
		"--verify", passer, "--verify", failer, "--root", root, "--out", out)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (the second verifier failed)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var rep codework.CodeRunReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the CodeRunReport schema: %v\nstdout:\n%s", err, stdout)
	}
	if len(rep.Verify) != 2 {
		t.Fatalf("verify rows = %d; want 2 (both flags ran)\n%s", len(rep.Verify), stdout)
	}
	if !rep.Verify[0].Passed {
		t.Errorf("row 1 = %+v; want the first verifier to pass", rep.Verify[0])
	}
	if !strings.Contains(strings.Join(rep.Verify[0].Argv, " "), "TestNoSuchTestAtAll") {
		t.Errorf("row 1 argv = %v; want the first flag", rep.Verify[0].Argv)
	}
	if rep.Verify[1].Passed {
		t.Errorf("row 2 = %+v; want the failing verifier", rep.Verify[1])
	}
	if rep.Verify[0].Attempt != 1 || rep.Verify[1].Attempt != 1 {
		t.Errorf("attempts = %d,%d; want 1,1", rep.Verify[0].Attempt, rep.Verify[1].Attempt)
	}
	for i, row := range rep.Verify {
		if row.Log == "" {
			t.Errorf("row %d has no log", i+1)
			continue
		}
		if _, err := os.Stat(filepath.Join(out, row.Log)); err != nil {
			t.Errorf("row %d log %q: %v", i+1, row.Log, err)
		}
	}
	if rep.Status != codework.StatusVerifyFailed {
		t.Errorf("status = %q; want %q", rep.Status, codework.StatusVerifyFailed)
	}
}

// TestCodeRunVerifyFailSkipsReview proves the flow stops before the review: no
// seat pays to review a change the verifier rejected.
func TestCodeRunVerifyFailSkipsReview(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)
	verifier := `"` + os.Args[0] + `" -test.run=TestProCodeVerifyFailHelperProcess -agy-pro-u5-force-failure`

	code, stdout, stderr := codeInvoke(t, nil,
		"run", "--ask", "add a doc comment", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--verify", verifier, "--root", root, "--out", out)
	if code != 1 {
		t.Fatalf("run exit = %d; want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 2 {
		t.Fatalf("run made %d fanout call/s; want 2 (plan + write, no review): %v", len(*calls), *calls)
	}
	if got := (*calls)[0]; !strings.Contains(strings.Join(got, " "), "--task plan-audit") {
		t.Errorf("call 1 = %v; want the plan-audit vector", got)
	}
	if got := (*calls)[1]; !strings.Contains(strings.Join(got, " "), "--units") {
		t.Errorf("call 2 = %v; want the partition write vector", got)
	}
	if _, err := os.Stat(filepath.Join(out, "review")); err == nil {
		t.Error("a failed verification still ran the review phase")
	}
	if !strings.Contains(stdout, "rollback: agy pro code rollback --run") {
		t.Errorf("report does not name the rollback command:\n%s", stdout)
	}
}

// TestCodeRunComposesPlanWriteReview is the composed door: one run directory,
// three phases, and the review prompt carries the change report.
func TestCodeRunComposesPlanWriteReview(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"run", "--ask", "add a doc comment", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--json", "--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("run exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 3 {
		t.Fatalf("run made %d fanout call/s; want 3 (plan + write + review): %v", len(*calls), *calls)
	}
	tasks := []string{"--task plan-audit", "--units", "--task review"}
	for i, want := range tasks {
		if joined := strings.Join((*calls)[i], " "); !strings.Contains(joined, want) {
			t.Errorf("call %d = %v; want it to carry %q", i+1, (*calls)[i], want)
		}
	}
	var rep codework.CodeRunReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not the CodeRunReport schema: %v\nstdout:\n%s", err, stdout)
	}
	if len(rep.Phases) != 3 {
		t.Fatalf("report phases = %+v; want plan, write, review", rep.Phases)
	}
	for i, want := range []string{codework.PhasePlan, codework.PhaseWrite, codework.PhaseReview} {
		if rep.Phases[i].Phase != want {
			t.Errorf("report phase %d = %q; want %q", i, rep.Phases[i].Phase, want)
		}
	}
	// The review prompt carries the change report the write phase rendered.
	prompt, err := os.ReadFile(filepath.Join(out, "review", "prompt.txt"))
	if err != nil {
		t.Fatalf("review prompt missing: %v", err)
	}
	if !strings.Contains(string(prompt), "## Change report") || !strings.Contains(string(prompt), "agy pro code:") {
		t.Errorf("review prompt does not carry the change report:\n%s", string(prompt))
	}
	// The final report is persisted over the interim one (a superset).
	final, err := os.ReadFile(filepath.Join(out, codeReportFileName))
	if err != nil {
		t.Fatalf("report.txt missing after a run: %v", err)
	}
	if !strings.Contains(string(final), "agy pro code: ") {
		t.Errorf("report.txt lost the human block:\n%s", string(final))
	}
}

// TestCodeRunStopsAfterPlanWithoutScope pins the documented plan-only stop.
func TestCodeRunStopsAfterPlanWithoutScope(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil, "run", "--ask", "audit this", "--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("plan-only stop exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("plan-only stop made %d fanout call/s; want 1 (the plan phase): %v", len(*calls), *calls)
	}
	if !strings.Contains(stderr, "stopped after the plan phase") {
		t.Errorf("stop note missing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--scope") || !strings.Contains(stderr, "--writer") {
		t.Errorf("derived-scope instruction missing:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "write")); err == nil {
		t.Error("the plan-only stop wrote a write tree")
	}
}

// TestCodeRunSnapshotsBeforeThePlanAndNamesNativeDrift is the U9 pin at the verb
// level: the `run` composition snapshots before the plan seats, so a write they
// make during the plan phase is DRIFT the report names - not the baseline the
// write phase diffs against. Before U9 this shape produced `files: none changed`
// with a silently rewritten tree (e2e-code-demo.md run A).
func TestCodeRunSnapshotsBeforeThePlanAndNamesNativeDrift(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	target := filepath.Join(root, "pkg", "a.go")
	original := []byte("package pkg\n")
	withCodeFanout(t, func(argv []string, _ io.Writer) int {
		if strings.Contains(strings.Join(argv, " "), "--task plan-audit") {
			if werr := os.WriteFile(target, []byte("package pkg // the plan seat edited this\n"), 0o600); werr != nil {
				t.Errorf("plant the plan-phase write: %v", werr)
			}
		}
		return 0
	})

	code, stdout, stderr := codeInvoke(t, nil,
		"run", "--ask", "add a doc comment", "--scope", "pkg/*.go", "--writer", "nw/glm-5.3-flash",
		"--yes", "--max-repairs", "0", "--verify", `"`+os.Args[0]+`" -test.run=TestNoSuchTestAtAll`,
		"--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("run exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// The manifest holds the PRE-plan bytes: the snapshot happened first.
	manifest, err := codesnap.LoadManifest(out)
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "pkg/a.go" {
		t.Fatalf("manifest rows = %+v; want the one scoped file", manifest.Files)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(original)); manifest.Files[0].SHA256 != want {
		t.Fatalf("manifest sha256 = %s; want the pre-plan bytes %s (a post-mutation baseline is the U9 defect)",
			manifest.Files[0].SHA256, want)
	}
	// drift.json is the machine record, and the human block names the file.
	if _, err := os.Stat(filepath.Join(out, "drift.json")); err != nil {
		t.Errorf("drift.json missing: %v", err)
	}
	if !strings.Contains(stdout, "agy pro code: ok (native-drift)") {
		t.Errorf("report does not append the native-drift token:\n%s", stdout)
	}
	if !strings.Contains(stdout, "drift: plan changed 1 file(s) (M pkg/a.go)") {
		t.Errorf("report does not name the drifted file:\n%s", stdout)
	}
	// The write diff is measured against the pre-plan baseline, so the run's
	// accounting names every mutation whichever phase made it.
	if !strings.Contains(stdout, "files: 1 changed") || !strings.Contains(stdout, "M pkg/a.go") {
		t.Errorf("report does not count the mutation:\n%s", stdout)
	}
}

// TestCodeRollbackRestoresFabricatedRun is the rollback pin: a run directory

// fabricated with the real codesnap snapshot (manifest + snapshot/, planted in a
// temp tree) restores the recorded bytes and archives what the run created.
func TestCodeRollbackRestoresFabricatedRun(t *testing.T) {
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
	manifest, err := codesnap.Take(root, []string{"pkg"}, out)
	if err != nil {
		t.Fatalf("cannot fabricate the run directory: %v", err)
	}
	if manifest == nil || len(manifest.Files) == 0 {
		t.Fatal("the fabricated manifest has no rows")
	}
	// The run's write: change the scoped file and add one the manifest does not name.
	if err := os.WriteFile(target, []byte("package pkg\n\n// changed by the run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "pkg", "b.go")
	if err := os.WriteFile(created, []byte("package pkg\n\n// created by the run\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := codeInvoke(t, nil, "rollback", "--run", out, "--root", root)
	if code != 0 {
		t.Fatalf("rollback exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("restored file unreadable: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Errorf("restored bytes = %q; want %q", string(restored), string(original))
	}
	if _, err := os.Stat(created); err == nil {
		t.Error("the file the run created is still in the tree; it should have been archived")
	}
	if _, err := os.Stat(filepath.Join(out, "rollback-archive", "pkg", "b.go")); err != nil {
		t.Errorf("created file was not archived: %v", err)
	}
	for _, want := range []string{"restored: 1", "archived: 1", "rollback: 1 restored, 1 archived"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("rollback print missing %q:\n%s", want, stderr)
		}
	}
	if !strings.Contains(stdout, "agy pro code: "+codework.StatusRolledBack) {
		t.Errorf("report does not carry the rolled-back status:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(out, codeRollbackFileName)); err != nil {
		t.Errorf("rollback.json missing: %v", err)
	}
}

// TestCodeRollbackRefusesWithoutManifest proves the fail-closed shape: no
// manifest, no restore, exit 1, and the tree is untouched.
func TestCodeRollbackRefusesWithoutManifest(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "pkg", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := codeInvoke(t, nil, "rollback", "--run", out, "--root", root)
	if code != 1 {
		t.Fatalf("rollback without a manifest exit = %d; want 1\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "no usable manifest") {
		t.Errorf("refusal does not name the missing manifest:\n%s", stderr)
	}
	after, err := os.ReadFile(filepath.Join(root, "pkg", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused rollback still touched the tree")
	}
}

// TestCodeReviewNeedsTheWriteReportAndReviewsIt pins both halves of the review
// gate: a run directory with no change report is a usage error naming write,
// and one with a report is reviewed.
func TestCodeReviewNeedsTheWriteReportAndReviewsIt(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	if err := os.MkdirAll(filepath.Join(out, "review"), 0o700); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := codeInvoke(t, nil, "review", "--run", out, "--root", root)
	if code != 2 {
		t.Fatalf("review without a report exit = %d; want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "run `agy pro code write`") {
		t.Errorf("refusal does not name the write command:\n%s", stderr)
	}

	if err := os.WriteFile(filepath.Join(out, codeReportFileName), []byte("agy pro code: ok\nfiles: 1 changed (1 modified) +2 -0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := withCodeFanout(t, nil)
	code, stdout, stderr := codeInvoke(t, nil, "review", "--run", out, "--root", root)
	if code != 0 {
		t.Fatalf("review exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("review made %d fanout call/s; want 1", len(*calls))
	}
	joined := strings.Join((*calls)[0], " ")
	if !strings.Contains(joined, "--task review") || !strings.Contains(joined, "--seats "+codeDefaultReviewSeats) {
		t.Errorf("review vector = %v; want the review task and the native line-up", (*calls)[0])
	}
	prompt, err := os.ReadFile(filepath.Join(out, "review", "prompt.txt"))
	if err != nil {
		t.Fatalf("review prompt missing: %v", err)
	}
	if !strings.Contains(string(prompt), "files: 1 changed") {
		t.Errorf("review prompt does not carry the change report:\n%s", string(prompt))
	}
	if !strings.Contains(stdout, "agy pro code: ok") {
		t.Errorf("review report missing the status line:\n%s", stdout)
	}
}

// TestCodeVerifyCmdsCollectsEveryFlag pins the repeatable --verify: every
// occurrence becomes one argv, in flag order, and the write phase runs all of
// them. A line that cannot be split stays a usage error before anything runs.
func TestCodeVerifyCmdsCollectsEveryFlag(t *testing.T) {
	cmds, code := codeVerifyCmds([]string{"go build ./...", "go vet ./..."}, io.Discard)
	if code != -1 {
		t.Fatalf("two --verify exit = %d; want -1 (a second verifier is legal)", code)
	}
	want := [][]string{{"go", "build", "./..."}, {"go", "vet", "./..."}}
	if joinedArgv(cmds) != joinedArgv(want) {
		t.Fatalf("codeVerifyCmds = %v; want %v (flag order preserved)", cmds, want)
	}

	// A blank occurrence is a usage error, not a silent skip: dropping the flag is
	// how an operator asks for detection, and a blank verifier would otherwise
	// disappear while the docs promised exit 2.
	var blankBuf bytes.Buffer
	if cmds, code := codeVerifyCmds([]string{"go build ./...", "   ", "go test ./x"}, &blankBuf); code != 2 {
		t.Fatalf("codeVerifyCmds with a blank entry = (%v, %d); want exit 2", cmds, code)
	}
	if !strings.Contains(blankBuf.String(), "empty command line") {
		t.Errorf("blank-entry refusal does not say why: %q", blankBuf.String())
	}
	if !strings.Contains(blankBuf.String(), "drop the flag") {
		t.Errorf("blank-entry refusal does not name the fix: %q", blankBuf.String())
	}

	// No --verify at all is "let the engine detect it", not an error.
	if cmds, code := codeVerifyCmds(nil, io.Discard); code != -1 || cmds != nil {
		t.Fatalf("codeVerifyCmds(nil) = (%v, %d); want (nil, -1)", cmds, code)
	}

	// Fail-closed: an unsplittable line is exit 2 and the echo is masked.
	var buf bytes.Buffer
	if _, code := codeVerifyCmds([]string{"go build ./x", `go "vet`}, &buf); code != 2 {
		t.Fatalf("unbalanced quote exit = %d; want 2", code)
	}
	if !strings.Contains(buf.String(), "unbalanced quote") {
		t.Errorf("refusal does not say why: %q", buf.String())
	}
}

// joinedArgv flattens an argv list into one comparable string.
func joinedArgv(cmds [][]string) string {
	parts := make([]string, 0, len(cmds))
	for _, argv := range cmds {
		parts = append(parts, strings.Join(argv, "\x00"))
	}
	return strings.Join(parts, "\x01")
}

// TestCodeWriteDryRunPrintsEveryVerifyVector is the live-canary shape as a pin:
// two --verify flags, a dry run, both vectors printed in flag order, one metered
// seat, and nothing spawned.
func TestCodeWriteDryRunPrintsEveryVerifyVector(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "x", "--scope", "pkg/a.go", "--writer", "nw/glm-5.3-flash",
		"--verify", "go build ./...", "--verify", "go vet ./...",
		"--dry-run", "--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("dry-run write exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Fatalf("dry run spawned %d fanout call/s; want 0", len(*calls))
	}
	build := strings.Index(stderr, "verify: go build ./...")
	vet := strings.Index(stderr, "verify: go vet ./...")
	if build < 0 || vet < 0 {
		t.Fatalf("stderr does not carry both verify vectors\nstderr:\n%s", stderr)
	}
	if build > vet {
		t.Errorf("verify vectors are not in flag order (build@%d, vet@%d):\n%s", build, vet, stderr)
	}
	if !strings.Contains(stderr, "1 metered seat(s)") {
		t.Errorf("dry-run echo does not count the writer once:\n%s", stderr)
	}
	if !strings.Contains(stdout, "agy pro code: "+codework.StatusDryRun) {
		t.Errorf("report status is not the dry run:\n%s", stdout)
	}
}

// TestSplitCodeArgv pins the quote-aware splitter the --verify operand needs.
func TestSplitCodeArgv(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "go build ./...", want: []string{"go", "build", "./..."}},
		{in: `"C:\Program Files\go\bin\go.exe" test ./pkg`, want: []string{`C:\Program Files\go\bin\go.exe`, "test", "./pkg"}},
		{in: "  npm   test  ", want: []string{"npm", "test"}},
		{in: `node -e "a b"`, want: []string{"node", "-e", "a b"}},
		{in: "", wantErr: true},
		{in: `go "build`, wantErr: true},
	}
	for _, tc := range cases {
		got, err := splitCodeArgv(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("splitCodeArgv(%q) = %v; want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitCodeArgv(%q) error: %v", tc.in, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("splitCodeArgv(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

// TestCodeSubcommandsRestorePhaseStreams proves the seam binding is undone: a
// subcommand must not leave the package-level phase streams pointing at its
// buffers.
func TestCodeSubcommandsRestorePhaseStreams(t *testing.T) {
	prevIn, prevOut, prevErr := codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr
	withCodeFanout(t, nil)
	_, _, _ = codeInvoke(t, nil, "plan", "--ask", "x", "--dry-run", "--out", t.TempDir())
	if codework.PhaseStdin != prevIn || codework.PhaseStdout != prevOut || codework.PhaseStderr != prevErr {
		t.Error("the phase streams were not restored after the subcommand returned")
	}
}
