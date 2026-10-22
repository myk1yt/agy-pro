package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// --- fixtures & seams -------------------------------------------------------

// swarmFixtureInventory is the D1 happy-path fixture: 2 native rows (flash +
// pro tier so preset p3 resolves its frozen shape) + 2 externals across two
// families.
func swarmFixtureInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{{
			ID:           "gemini-2.5-flash",
			Capabilities: providers.Capabilities{SupportsTools: true, SupportsVision: true},
		}}},
		// No APIKeyEnv: a keyless provider entry is never KeyMissing and stays
		// preset-eligible (inventory.go contract §2.5).
		{Name: "nw", Models: []providers.Model{{
			ID:           "glm-5.3-flash-flex",
			Capabilities: providers.Capabilities{SupportsTools: true, SupportsThinking: true},
		}}},
	}}
	native := staticFetcher{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
		{ID: "gemini-3.8-pro", DisplayName: "Gemini 3.8 Pro"},
	}
	inv, err := inventory.Load(context.Background(), reg, native)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// withStubSwarmInventory points loadSwarmInventory at the fixture.
func withStubSwarmInventory(t *testing.T, inv *inventory.Inventory, err error) func() {
	t.Helper()
	old := loadSwarmInventory
	loadSwarmInventory = func(context.Context) (*inventory.Inventory, error) {
		return inv, err
	}
	return func() { loadSwarmInventory = old }
}

// withTTY drives the F-B TTY detection seams. outIsTTY stays false so captured
// output is asserted ANSI-free unless a test flips it.
func withTTY(t *testing.T, inTTY bool) func() {
	t.Helper()
	oldIn, oldOut := stdinIsTTY, outIsTTY
	stdinIsTTY = func(io.Reader) bool { return inTTY }
	outIsTTY = func(io.Writer) bool { return false }
	return func() { stdinIsTTY, outIsTTY = oldIn, oldOut }
}

// stubFanoutRun replaces swarm.RunFanout (the "stubbed fanout execution
// seam"): it records the generated args and writes a canned SummaryReport.
func stubFanoutRun(t *testing.T, summary *fanout.SummaryReport, gotArgs *[]string, ran *bool) func() {
	t.Helper()
	old := swarm.RunFanout
	swarm.RunFanout = func(args []string, in io.Reader, out, errw io.Writer) int {
		if ran != nil {
			*ran = true
		}
		if gotArgs != nil {
			*gotArgs = append([]string(nil), args...)
		}
		data, err := json.Marshal(summary)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write(data); err != nil {
			t.Fatal(err)
		}
		return 0
	}
	return func() { swarm.RunFanout = old }
}

func cannedSummary() *fanout.SummaryReport {
	seat := func(id int, kind fanout.SeatKind, model, status string, degraded bool) fanout.SeatResult {
		prov := "external"
		if kind == fanout.SeatKindNative {
			prov = "native"
		}
		return fanout.SeatResult{
			SeatID:   id,
			Kind:     kind,
			Status:   status,
			Degraded: degraded,
			FinalTarget: providers.Target{
				Provider: providers.Provider{Name: prov},
				Model:    providers.Model{ID: model},
			},
		}
	}
	return &fanout.SummaryReport{
		Task:       "review",
		NRequested: 4,
		NCompleted: 4,
		Seats: []fanout.SeatResult{
			seat(1, fanout.SeatKindNative, "gemini-3.8-flash-high", "success", false),
			seat(2, fanout.SeatKindNative, "gemini-3.8-pro", "success", false),
			seat(3, fanout.SeatKindProvider, "gemini-2.5-flash", "failed", true),
			seat(4, fanout.SeatKindProvider, "glm-5.3-flash-flex", "success", false),
		},
	}
}

// countingReader is the F-B instrumented reader: every Read is recorded.
type countingReader struct {
	reads int
	body  string
	pos   int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	if c.pos >= len(c.body) {
		return 0, io.EOF
	}
	n := copy(p, c.body[c.pos:])
	c.pos += n
	return n, nil
}

// --- D1: zero flag-writing happy path ---------------------------------------

func TestRunSwarmCmdD1Transcript(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir()) // isolate swarm-state.json
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer withTTY(t, true)()
	var gotArgs []string
	defer stubFanoutRun(t, cannedSummary(), &gotArgs, nil)()

	// Happy path is numbered selection only: template "1", preset "p3",
	// confirm "y". Zero non-interactive flags (contract §2.7 F-B note).
	input := "1\np3\ny\n"
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd(nil, strings.NewReader(input), &outBuf, &errBuf)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s\nstdout = %s", code, errBuf.String(), outBuf.String())
	}

	// (b) user input carries zero flag tokens.
	for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
		if strings.HasPrefix(line, "-") {
			t.Errorf("user input line %q must not write flags", line)
		}
	}
	if strings.Contains(input, "--") {
		t.Errorf("user input %q must not contain flag tokens", input)
	}

	got := outBuf.String()
	t.Logf("D1 transcript:\n%s", got)

	// (c) numbered lists at every step + the final run summary.
	if !strings.Contains(got, "[1] Code Review") {
		t.Error("template step must render a numbered pick list")
	}
	for _, marker := range []string{"p1", "p2", "p3"} {
		if !strings.Contains(got, marker) {
			t.Errorf("seat step must present preset %s as a numbered default", marker)
		}
	}
	if !strings.Contains(got, "[ 1] gemini-3.8-flash-high") {
		t.Error("seat step must render the numbered inventory")
	}
	if !strings.Contains(got, "Selected seats:") {
		t.Error("seat step must confirm the selection with provenance")
	}
	if !strings.Contains(got, "free Antigravity quota") || !strings.Contains(got, "metered API") {
		t.Error("per-seat quota note missing (native quota vs provider metered)")
	}
	if !strings.Contains(got, "agy pro fanout") {
		t.Error("step [4] must preview the exact fanout command")
	}
	if !strings.Contains(got, "seat | kind | model | verdict") {
		t.Errorf("final run summary table missing:\n%s", got)
	}
	if !strings.Contains(got, "gemini-3.8-flash-high | success") {
		t.Errorf("summary rows missing:\n%s", got)
	}
	if !strings.Contains(got, "failed (degraded)") {
		t.Errorf("degraded verdict missing:\n%s", got)
	}

	// The delegated run received the generated fanout args (same pattern as
	// launcher's runSwarmFromMenu).
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--seats native:gemini-3.8-flash-high,native:gemini-3.8-pro,google/gemini-2.5-flash,nw/glm-5.3-flash-flex") {
		t.Errorf("generated --seats mismatch: %q", joined)
	}
	if !strings.Contains(joined, "--task review") {
		t.Errorf("generated --task mismatch: %q", joined)
	}
}

// --- F-B: one interactivity rule --------------------------------------------

// TTY stdin + ONLY --root present => zero prompts, missing seats/template =>
// exit 2 with usage, stdin never consumed. ANY non-interactive flag behaves
// the same way (table-driven over the whole flag surface).
func TestRunSwarmCmdFBRuleZeroPromptReads(t *testing.T) {
	cases := [][]string{
		{"--root", "ws"},
		{"--template", "code-review"},
		{"--seats", "p1"},
		{"--prompt", "hello"},
		{"--prompt-file", "prompt.txt"},
		{"--timeout", "60"},
		{"--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "="), func(t *testing.T) {
			t.Setenv("AGY_PRO_HOME", t.TempDir())
			defer withTTY(t, true)() // TTY stdin: interactive is still forbidden
			ran := false
			defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

			cr := &countingReader{body: "1\np3\ny\n"}
			var outBuf, errBuf strings.Builder
			code := runSwarmCmd(args, cr, &outBuf, &errBuf)

			if code != 2 {
				t.Errorf("exit = %d, want 2 (missing required input; never prompt)\nstderr = %s", code, errBuf.String())
			}
			if cr.reads != 0 {
				t.Errorf("stdin reads = %d, want 0 (F-B: zero prompts when any non-interactive flag is present)", cr.reads)
			}
			if ran {
				t.Error("fanout must not run on a usage error")
			}
			if !strings.Contains(errBuf.String(), "--template") || !strings.Contains(errBuf.String(), "--seats") {
				t.Errorf("error must name the missing seats/template options: %s", errBuf.String())
			}
			if !strings.Contains(errBuf.String(), "Usage:") {
				t.Errorf("usage must be printed on the exit-2 path: %s", errBuf.String())
			}
		})
	}
}

// Non-TTY stdin + no flags: no interactive fallback, exit 2, no read.
func TestRunSwarmCmdNonTTYNoFlagsExit2(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withTTY(t, false)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	cr := &countingReader{body: "1\np3\ny\n"}
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd(nil, cr, &outBuf, &errBuf)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if cr.reads != 0 {
		t.Errorf("stdin reads = %d, want 0", cr.reads)
	}
	if ran {
		t.Error("fanout must not run")
	}
	if !strings.Contains(errBuf.String(), "terminal") {
		t.Errorf("error must explain the TTY requirement: %s", errBuf.String())
	}
}

// The non-interactive surface covers the same configuration as the builder
// (C2): full flag set on a TTY still means zero prompts, and the run works.
func TestRunSwarmCmdNonInteractiveRunsOnTTY(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer withTTY(t, true)() // TTY present, but flags force the zero-prompt path
	var gotArgs []string
	ran := false
	defer stubFanoutRun(t, cannedSummary(), &gotArgs, &ran)()

	cr := &countingReader{body: "MUST-NOT-BE-READ\n"}
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--template", "security-audit",
		"--seats", "p3",
		"--prompt", "audit the workspace",
		"--timeout", "90",
		"--root", "ws",
	}, cr, &outBuf, &errBuf)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errBuf.String())
	}
	if cr.reads != 0 {
		t.Errorf("stdin reads = %d, want 0 (flag path never prompts)", cr.reads)
	}
	if !ran {
		t.Fatal("fanout must run")
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--task security") || !strings.Contains(joined, "--timeout 90") || !strings.Contains(joined, "--root ws") {
		t.Errorf("generated args = %q", joined)
	}
	if !strings.Contains(joined, "audit the workspace") {
		t.Errorf("--prompt must override the template scaffold: %q", joined)
	}
	if !strings.Contains(outBuf.String(), "seat | kind | model | verdict") {
		t.Errorf("summary table missing:\n%s", outBuf.String())
	}
}

// Pinned timeout default (2026-10-14, residual round): the non-interactive
// `agy pro swarm` path (the surface the /agypro_swarm plugin command drives)
// must seed the swarm budget from fanout.DefaultTimeoutSec (300s), not a local
// 120s constant, so the dry-run echo and the real run both carry the same
// budget the engine applies.
func TestRunSwarmCmdNonInteractiveDefaultTimeoutIs300(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	// The seat-spec line reads the exclusion policy through the seam; pin the
	// built-in default so the dry-run echo stays hermetic.
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", "native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex",
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errBuf.String())
	}
	got := outBuf.String()
	if !strings.Contains(got, "--timeout 300") {
		t.Errorf("dry-run echo must carry the 300s default budget: %q", got)
	}
	if strings.Contains(got, "--timeout 120") {
		t.Errorf("dry-run echo still carries the stale 120s budget: %q", got)
	}

	// The usage text renders the shared constant, not a hardcoded literal.
	if !strings.Contains(proSwarmUsage, fmt.Sprintf("(default %d)", fanout.DefaultTimeoutSec)) {
		t.Errorf("proSwarmUsage must document the shared default (default %d):\n%s", fanout.DefaultTimeoutSec, proSwarmUsage)
	}
	if strings.Contains(proSwarmUsage, "default 120") {
		t.Errorf("proSwarmUsage still documents the stale 120s default:\n%s", proSwarmUsage)
	}
}

func TestRunSwarmCmdDryRunPrintsCommand(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", "native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex",
		"--prompt", "check it",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errBuf.String())
	}
	if ran {
		t.Error("--dry-run must not execute the swarm")
	}
	got := outBuf.String()
	if !strings.HasPrefix(got, "agy pro fanout") {
		t.Errorf("dry-run must print the equivalent fanout command: %q", got)
	}
	if !strings.Contains(got, "--seats native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex") {
		t.Errorf("dry-run command must carry the seat specs: %q", got)
	}
}

func TestRunSwarmCmdNonInteractiveUnknownTemplateExit2(t *testing.T) {
	defer withTTY(t, true)()
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{"--template", "nope", "--seats", "p1"}, strings.NewReader(""), &outBuf, &errBuf)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown template") {
		t.Errorf("stderr = %s", errBuf.String())
	}
}

// --- C3: no ANSI/OSC when not a TTY -----------------------------------------

func TestRunSwarmCmdNoAnsiInPipe(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer withTTY(t, false)() // piped stdin AND piped stdout
	defer stubFanoutRun(t, cannedSummary(), nil, nil)()

	// 1. Interactive transcript on a pipe-like writer.
	oldIn := stdinIsTTY
	stdinIsTTY = func(io.Reader) bool { return true } // force the builder; out is still non-TTY
	var interactive strings.Builder
	if code := runSwarmCmd(nil, strings.NewReader("1\np3\ny\n"), &interactive, &strings.Builder{}); code != 0 {
		t.Fatalf("interactive exit = %d", code)
	}
	stdinIsTTY = oldIn

	// 2. Non-interactive run on a pipe-like writer.
	var nonInteractive strings.Builder
	if code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p3", "--prompt", "x"},
		strings.NewReader(""), &nonInteractive, &strings.Builder{}); code != 0 {
		t.Fatalf("non-interactive exit = %d", code)
	}

	// 3. --dry-run output.
	var dry strings.Builder
	if code := runSwarmCmd([]string{"--dry-run", "--template", "code-review", "--seats", "p1", "--prompt", "x"},
		strings.NewReader(""), &dry, &strings.Builder{}); code != 0 {
		t.Fatalf("dry-run exit = %d", code)
	}

	for name, buf := range map[string]string{
		"interactive":     interactive.String(),
		"non-interactive": nonInteractive.String(),
		"dry-run":         dry.String(),
	} {
		if strings.Contains(buf, "\x1b") || strings.Contains(buf, "\x9b") {
			t.Errorf("%s output must be ANSI-free when not a TTY; found escapes:\n%q", name, buf)
		}
		if strings.Contains(buf, "\x1b]") {
			t.Errorf("%s output must be OSC-free when not a TTY", name)
		}
	}
}

// ANSI is used only on a TTY (C3 positive direction).
func TestRunSwarmCmdAnsiOnlyOnTTY(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer stubFanoutRun(t, cannedSummary(), nil, nil)()
	oldIn, oldOut := stdinIsTTY, outIsTTY
	stdinIsTTY = func(io.Reader) bool { return true }
	outIsTTY = func(io.Writer) bool { return true }
	defer func() { stdinIsTTY, outIsTTY = oldIn, oldOut }()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt", "x"},
		strings.NewReader(""), &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "\x1b[1m") {
		t.Errorf("TTY output should decorate the summary header with ANSI: %q", outBuf.String())
	}
}

func TestRunSwarmCmdToolsTaskFlagsReachFanoutArgs(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	defer withStubSwarmPolicy(t, providers.ExclusionPolicy{})()
	defer withTTY(t, true)()
	ran := false
	defer stubFanoutRun(t, cannedSummary(), nil, &ran)()

	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{
		"--dry-run",
		"--template", "code-review",
		"--seats", "native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex",
		"--tools", "read,search",
		"--task", "review",
	}, strings.NewReader("MUST-NOT-BE-READ\n"), &outBuf, &errBuf)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errBuf.String())
	}
	got := outBuf.String()
	if !strings.Contains(got, "--tools read,search") {
		t.Errorf("dry-run command must carry the --tools override: %q", got)
	}
	if !strings.Contains(got, "--task review") {
		t.Errorf("dry-run command must carry the --task override: %q", got)
	}
}

func TestResolveFlagSeatsMasksDecoratedTokenInMultiTokenLine(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()

	plant := testfixtures.AWSSecretExample
	var errBuf strings.Builder
	_, code := resolveFlagSeats("1,native:"+plant, &errBuf)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (invalid token)", code)
	}
	if strings.Contains(errBuf.String(), plant) {
		t.Errorf("multi-token --seats error leaked the decorated plant: %q", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "wJalr****") && !strings.Contains(errBuf.String(), "[REDACTED]") {
		t.Errorf("multi-token --seats error must render the plant masked: %q", errBuf.String())
	}
}

// S2: the --prompt-file read failure is a whole-line sink (os.ReadFile wraps
// the path into the composite sentence). A multi-word or tab-decorated operand
// defeats the whole-string classifier, so the operand slot routes through the
// seeded sentence masker with the per-token seeds (the wide SeedEligible core
// restored in phase 1): the planted secret is masked in BOTH slots. Under the
// batch-I narrow seed rule the operand did not seed and the composite echoed
// the secret raw.
func TestRunSwarmCmdPromptFileErrorMasksMultiWordOperand(t *testing.T) {
	plant := testfixtures.AWSSecretExample
	for _, tc := range []struct {
		name string
		path string
	}{
		{"multi-word path", "swarm notes " + plant + " prompt.txt"},
		{"tab-decorated path", "swarm\tnotes " + plant + " prompt.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGY_PRO_HOME", t.TempDir())
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var outBuf, errBuf strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", tc.path},
				strings.NewReader(""), &outBuf, &errBuf)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
			}
			if strings.Contains(errBuf.String(), plant) {
				t.Errorf("prompt-file error leaked the decorated plant:\n%s", errBuf.String())
			}
			for _, want := range []string{"Error reading prompt file", "[REDACTED]"} {
				if !strings.Contains(errBuf.String(), want) {
					t.Errorf("prompt-file error missing %q:\n%s", want, errBuf.String())
				}
			}
		})
	}
}

// S2 counterpart: the seeded fallback must not touch a benign multi-word path.
// No token is seed-eligible, so the slot keeps the whole-string classifier's
// readable rendering and no redaction marker appears.
func TestRunSwarmCmdPromptFileErrorBenignMultiWordPathReadable(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	const path = "my prompts missing.md"
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", path},
		strings.NewReader(""), &outBuf, &errBuf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), path) {
		t.Errorf("benign multi-word path must stay readable:\n%s", errBuf.String())
	}
	if strings.Contains(errBuf.String(), "[REDACTED]") {
		t.Errorf("benign operand must not trigger any redaction:\n%s", errBuf.String())
	}
}

// Batch K slot-hardening pin: the MaskOperand-first slot with an
// equality-only fallback skipped the fallback whenever MaskOperand PARTIALLY
// masked the operand. A compound operand carrying both a pattern-shaped
// credential (AKIA, masked by the generic redactor inside MaskOperand) and a
// bare decorated secret (wJalr..., which no generic pattern catches)
// therefore echoed the bare secret raw in the prompt-file slot. The hardened
// two-pass slot runs the seeded sentence pass for every whitespace-carrying
// operand, seeded from the original operand and applied to the already-masked
// slot, so the bare secret is masked too. Without the hardening the raw plant
// reaches stderr and this test fails.
func TestRunSwarmCmdPromptFileSlotHardenedCompoundOperand(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	const bare = testfixtures.AWSSecretExample
	const akia = testfixtures.AKIAExample
	operand := "prefix " + akia + " " + bare
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", operand},
		strings.NewReader(""), &outBuf, &errBuf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
	}
	if strings.Contains(errBuf.String(), bare) || strings.Contains(errBuf.String(), akia) {
		t.Errorf("hardened prompt-file slot leaked the compound operand raw:\n%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "[REDACTED]") {
		t.Errorf("hardened prompt-file slot must render the compound operand masked:\n%s", errBuf.String())
	}
}

// Batch K byte-identity pins for the hardened prompt-file slot: a
// single-token bare plant, a single-token decorated plant, a one-space
// operand, a location-shaped path, and a benign multi-word path must all
// render exactly as the plain MaskOperand-first slot rendered before the
// hardening (proved by the temporary-revert run in the batch-K report).
func TestRunSwarmCmdPromptFileSlotRenderingPins(t *testing.T) {
	for _, tc := range []struct{ name, path, wantSlot string }{
		{"single-token bare plant", testfixtures.AWSSecretExample, "wJalr****"},
		{"single-token decorated plant", "native:" + testfixtures.AWSSecretExample, "nativ****"},
		{"one-space operand", "a b", "a b"},
		{"location-shaped path", "./registry-keys.json", "./registry-keys.json"},
		{"benign multi-word path", "my prompts missing.md", "my prompts missing.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGY_PRO_HOME", t.TempDir())
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var outBuf, errBuf strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", tc.path},
				strings.NewReader(""), &outBuf, &errBuf)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
			}
			want := "Error reading prompt file " + tc.wantSlot + ": "
			if !strings.HasPrefix(errBuf.String(), want) {
				t.Errorf("slot rendering drifted:\ngot:  %q\nwant prefix: %q", errBuf.String(), want)
			}
		})
	}
}

// Batch L slot-gate pin: the batch-K gate ran the seeded pass only for
// whitespace-carrying operands, so the comma/semicolon-decorated class the
// OLD equality fallback used to seed (21827f6) rode raw again — a
// masked->raw regression vs 21827f6. The widened gate
// (ContainsAny(op, " \t\n\r,;")) re-runs the seeded pass for the comma
// class, so both plants render masked in the prompt-file slot. Without the
// widening the raw plants reach the slot and this test fails.
//
// Scope note: the trailing os.ReadFile sentence now takes the same per-token
// seeds as the slot (batch M), so the full slot+tail line is pinned in
// TestRunSwarmCmdPromptFileCompositeTailSeedsTokens below; this table keeps
// asserting the slot rendering exactly like the rendering-pin table above.
func TestRunSwarmCmdPromptFileSlotCommaDecoratedOperands(t *testing.T) {
	for _, tc := range []struct{ name, path, wantSlot string }{
		{"comma-decorated compound plant", "./backups," + testfixtures.AWSSecretExample, "./backups,[REDACTED]"},
		{"comma-decorated bare secret", "note,abcdefghijklmnopqrs", "note,[REDACTED]"},
		{"benign comma list", "read,search", "read,search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGY_PRO_HOME", t.TempDir())
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var outBuf, errBuf strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", tc.path},
				strings.NewReader(""), &outBuf, &errBuf)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
			}
			want := "Error reading prompt file " + tc.wantSlot + ": "
			if !strings.HasPrefix(errBuf.String(), want) {
				t.Errorf("slot rendering drifted:\ngot:  %q\nwant prefix: %q", errBuf.String(), want)
			}
		})
	}
}

// Batch M (round-4 CONFIRMED MEDIUM, security + code swarm): the composite
// error TAIL beside the masked slot re-echoed the secret raw. The tail seeded
// the redactor with the WHOLE prompt-file operand only, and a location-shaped
// whole operand ("./backups,<secret>") never seeds (SeedEligible's location
// exemption), so MaskSentence fell back to MaskOperand, whose generic patterns
// do not cover a bare secret token: the slot beside it masked (per-token
// seeds) while the os.ReadFile sentence rode raw. The tail now takes the same
// per-token seeds as the slot (providers.FlagSeedOperands, the composition the
// cmd/agy main.go rollback twin already used).
//
// The pins assert the FULL output line (slot + tail). Without the fix the raw
// plant reaches stderr through the sentence and the equality pins fail.
func TestRunSwarmCmdPromptFileCompositeTailSeedsTokens(t *testing.T) {
	awsPlant := testfixtures.AWSSecretExample

	for _, tc := range []struct{ name, path, plant, wantSlot string }{
		{"comma-decorated compound plant", "./backups," + awsPlant, awsPlant, "./backups,[REDACTED]"},
		{"semicolon-decorated compound plant", "./backups;" + awsPlant, awsPlant, "./backups;[REDACTED]"},
		{"comma-decorated bare secret", "note,abcdefghijklmnopqrs", "abcdefghijklmnopqrs", "note,[REDACTED]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGY_PRO_HOME", t.TempDir())
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var outBuf, errBuf strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", tc.path},
				strings.NewReader(""), &outBuf, &errBuf)
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
			}
			// The expected full line: masked slot + the composite sentence with
			// the plant replaced by the redaction marker. The OS wording comes
			// from a real os.ReadFile of the same path, so the pin is
			// platform-independent and still byte-exact.
			_, readErr := os.ReadFile(tc.path)
			if readErr == nil {
				t.Fatalf("os.ReadFile(%q) unexpectedly succeeded", tc.path)
			}
			want := "Error reading prompt file " + tc.wantSlot + ": " +
				strings.ReplaceAll(readErr.Error(), tc.plant, "[REDACTED]") + "\n"
			if errBuf.String() != want {
				t.Errorf("full prompt-file error line drifted:\ngot:  %q\nwant: %q", errBuf.String(), want)
			}
			if strings.Contains(errBuf.String(), tc.plant) {
				t.Errorf("prompt-file error tail leaks the planted secret %q:\n%s", tc.plant, errBuf.String())
			}
		})
	}

	// The other side of the composition: a benign comma list seeds no token,
	// so the added per-token seeds must not start masking ordinary values.
	// The full line stays byte-identical to the whole-operand seeding.
	const benign = "read,search"
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
	var outBuf, errBuf strings.Builder
	code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", benign},
		strings.NewReader(""), &outBuf, &errBuf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", code, errBuf.String())
	}
	_, readErr := os.ReadFile(benign)
	if readErr == nil {
		t.Fatalf("os.ReadFile(%q) unexpectedly succeeded", benign)
	}
	want := "Error reading prompt file " + benign + ": " + readErr.Error() + "\n"
	if errBuf.String() != want {
		t.Errorf("benign comma list full line drifted:\ngot:  %q\nwant: %q", errBuf.String(), want)
	}
}

// --- FIX-G F1 (round-2 LOW): the spec-path policy read fails open LOUDLY.

// withSwarmPolicyRegistry isolates the whole env for the exclusion-policy read:
// AGY_PRO_HOME holds swarm-state.json, and providers.json lives at an explicit
// path. An empty body writes NO file, which is the absent-registry case.
func withSwarmPolicyRegistry(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	path := filepath.Join(home, "providers.json")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(providers.EnvProviders, path)
	return path
}

// specPathSeats is the explicit seat-spec line the notice tests drive. Neither
// spec is excluded by the built-in default policy, so the ONLY warning these
// runs can carry is the exclusion-unread notice.
const specPathSeats = "native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex"

// TestResolveFlagSeatsPolicyReadNotice pins the fix on the resolveFlagSeats seam
// with the REAL loadSwarmPolicy: an unreadable registry still resolves the pick
// (no rejection, no verdict change) and now says so in one line on the error
// writer, naming the class only. The control cases keep the loud path honest: a
// readable registry and an absent one both stay silent, because a missing file
// carries no user-owned list to drop.
func TestResolveFlagSeatsPolicyReadNotice(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantNotice bool
	}{
		{name: "healthy registry", body: `{"providers":[]}`},
		{name: "corrupt registry", body: `{"providers": [`, wantNotice: true},
		{name: "absent registry", body: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := withSwarmPolicyRegistry(t, tc.body)
			defer withStubSwarmPresets(t, nil)()

			var errOut bytes.Buffer
			entries, code := resolveFlagSeats(specPathSeats, &errOut)
			if code >= 0 {
				t.Fatalf("exit = %d, want success (nothing is rejected)\nstderr:\n%s", code, errOut.String())
			}
			if len(entries) != 2 {
				t.Fatalf("spec line seated %d entries, want 2 (the pick is still honored): %+v", len(entries), entries)
			}
			hasNotice := strings.Contains(errOut.String(), fanout.ExclusionUnreadNotice)
			if hasNotice != tc.wantNotice {
				t.Fatalf("notice present = %v; want %v\nstderr:\n%s", hasNotice, tc.wantNotice, errOut.String())
			}
			if !tc.wantNotice {
				if errOut.Len() != 0 {
					t.Errorf("no notice expected; stderr = %q", errOut.String())
				}
				return
			}
			if n := strings.Count(errOut.String(), "warning:"); n != 1 {
				t.Errorf("warning count = %d; want exactly the one notice line:\n%s", n, errOut.String())
			}
			if strings.Contains(errOut.String(), path) {
				t.Errorf("the notice echoed the registry path; it must name the class only:\n%s", errOut.String())
			}
		})
	}
}

// TestRunSwarmCmdDryRunPolicyNoticeLeavesStdoutByteIdentical pins the relay
// contract: the notice rides stderr, so the machine-relayed dry-run command echo
// on stdout is byte-identical on the healthy, corrupt, and absent paths, and the
// corrupt path still exits 0 with the seat specs in the relayed vector.
func TestRunSwarmCmdDryRunPolicyNoticeLeavesStdoutByteIdentical(t *testing.T) {
	args := []string{"--dry-run", "--template", "code-review", "--seats", specPathSeats, "--prompt", "x"}
	cases := []struct {
		name       string
		body       string
		wantNotice bool
	}{
		{name: "healthy registry", body: `{"providers":[]}`},
		{name: "corrupt registry", body: `{"providers": [`, wantNotice: true},
		{name: "absent registry", body: ""},
	}
	var baseline string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := withSwarmPolicyRegistry(t, tc.body)

			var outBuf, errBuf strings.Builder
			code := runSwarmCmd(args, strings.NewReader(""), &outBuf, &errBuf)
			if code != 0 {
				t.Fatalf("exit = %d, want 0 (the pick is honored)\nstderr:\n%s", code, errBuf.String())
			}
			stdout := outBuf.String()
			if i == 0 {
				baseline = stdout
			}
			if stdout != baseline {
				t.Errorf("dry-run stdout drifted (byte identity is the contract):\ngot:  %q\nwant: %q", stdout, baseline)
			}
			if !strings.Contains(stdout, "--seats "+specPathSeats) {
				t.Errorf("dry-run echo lost the seat specs: %q", stdout)
			}
			if strings.Contains(stdout, "warning:") {
				t.Errorf("the notice leaked into stdout: %q", stdout)
			}
			hasNotice := strings.Contains(errBuf.String(), fanout.ExclusionUnreadNotice)
			if hasNotice != tc.wantNotice {
				t.Fatalf("notice present = %v; want %v\nstderr:\n%s", hasNotice, tc.wantNotice, errBuf.String())
			}
			if tc.wantNotice && strings.Contains(errBuf.String(), path) {
				t.Errorf("the notice echoed the registry path:\n%s", errBuf.String())
			}
		})
	}
}
