package codework

// Tests for the phase orchestration. The seams (RunFanout, TakeSnapshot,
// ComputeDiff, RollbackTree, RunVerifyCmd, the phase streams) are replaced by one
// recorder, so every assertion here is about the ENGINE'S OWN DECISIONS: the
// exact argv, the call order, the consent refusal, and the fact that a failure
// never rolls the tree back on its own. Nothing in this file spawns a process or
// spends money.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"agypro/internal/codesnap"
)

// phaseRecorder is the call log and the canned answers for one test.
type phaseRecorder struct {
	calls       []string
	fanoutCalls [][]string
	verifyCalls [][]string
	fanoutExits []int
	verifyPass  []bool
	verifyTail  string
	deltas      []codesnap.FileDelta
	snapshotErr error
	diffErr     error
	out         bytes.Buffer
	errOut      bytes.Buffer

	// fanoutSummary, when non-nil, is written into the fake fanout call's --out
	// directory as a summary.json right after the call returns. It is how a test
	// hands a phase the engine's per-seat outcome (a rate-limited seat, an
	// all-failed seat list) without an engine.
	fanoutSummary []summaryFixtureSeat

	// verifyTimeouts records the deadline each verification call was given (the
	// budget clamp is observable only here), and verifyDurations overrides the
	// reported wall clock so a test can spend a budget without sleeping.
	verifyTimeouts  []time.Duration
	verifyDurations []int64

	// verifyDetected records, per verification call, which seam answered it: true
	// for RunDetectedVerifyCmd (an engine-chosen command) and false for RunVerifyCmd
	// (an explicit --verify). It is how a test asserts the R3-6 provenance split.
	verifyDetected []bool

	// fanoutHook runs inside the fake fanout call, before it returns: it is how
	// a test plants what a real seat does to the tree (a write during the plan,
	// a governance-file edit) without spawning anything.
	fanoutHook func(call int, argv []string)
}

// installPhaseRecorder replaces every seam for the duration of the test and
// restores them afterwards. LoadSnapshotManifest is deliberately NOT replaced:
// the rollback phase's manifest requirement must be exercised against the real
// loader to be worth anything.
func installPhaseRecorder(t *testing.T, rec *phaseRecorder) *phaseRecorder {
	t.Helper()
	if rec == nil {
		rec = &phaseRecorder{}
	}
	prevFanout, prevTake, prevDiff, prevRoll, prevVerify := RunFanout, TakeSnapshot, ComputeDiff, RollbackTree, RunVerifyCmd
	prevDetectedVerify := RunDetectedVerifyCmd
	prevIn, prevOut, prevErr, prevTTY := PhaseStdin, PhaseStdout, PhaseStderr, stdinIsTTY
	t.Cleanup(func() {
		RunFanout, TakeSnapshot, ComputeDiff, RollbackTree, RunVerifyCmd = prevFanout, prevTake, prevDiff, prevRoll, prevVerify
		RunDetectedVerifyCmd = prevDetectedVerify
		PhaseStdin, PhaseStdout, PhaseStderr, stdinIsTTY = prevIn, prevOut, prevErr, prevTTY
	})

	RunFanout = func(args []string, in io.Reader, out, errw io.Writer) int {
		rec.calls = append(rec.calls, "fanout")
		rec.fanoutCalls = append(rec.fanoutCalls, append([]string(nil), args...))
		i := len(rec.fanoutCalls) - 1
		if rec.fanoutHook != nil {
			rec.fanoutHook(i, args)
		}
		if len(rec.fanoutSummary) > 0 {
			if dir := phaseArgValue(args, "--out"); dir != "" {
				writeSummaryFixture(t, dir, "canned", rec.fanoutSummary...)
			}
		}
		if i < len(rec.fanoutExits) {
			return rec.fanoutExits[i]
		}
		return 0
	}
	TakeSnapshot = func(root string, scopes []string, destDir string) (*codesnap.Manifest, error) {
		rec.calls = append(rec.calls, "snapshot")
		if rec.snapshotErr != nil {
			return nil, rec.snapshotErr
		}
		return &codesnap.Manifest{
			Version:     codesnap.ManifestVersion,
			Root:        root,
			Scopes:      append([]string(nil), scopes...),
			SnapshotDir: filepath.Join(destDir, "snapshot"),
			Files:       []codesnap.ManifestRow{},
		}, nil
	}
	ComputeDiff = func(root string, m *codesnap.Manifest) ([]codesnap.FileDelta, error) {
		rec.calls = append(rec.calls, "diff")
		if rec.diffErr != nil {
			return nil, rec.diffErr
		}
		if rec.deltas == nil {
			return []codesnap.FileDelta{}, nil
		}
		return rec.deltas, nil
	}
	RollbackTree = func(root, destDir string) (*codesnap.RollbackReport, error) {
		rec.calls = append(rec.calls, "rollback")
		return &codesnap.RollbackReport{Restored: []string{}, Archived: []string{}, Unchanged: []string{}, Skipped: []codesnap.SkippedFile{}}, nil
	}
	RunVerifyCmd = func(ctx context.Context, root string, argv []string, timeout time.Duration, logPath string) (VerifyResult, error) {
		return rec.recordVerify(false, argv, timeout, logPath)
	}
	RunDetectedVerifyCmd = func(ctx context.Context, root string, argv []string, timeout time.Duration, logPath string) (VerifyResult, error) {
		return rec.recordVerify(true, argv, timeout, logPath)
	}

	PhaseStdin = strings.NewReader("")
	PhaseStdout = &rec.out
	PhaseStderr = &rec.errOut
	stdinIsTTY = func(io.Reader) bool { return false }
	return rec
}

// recordVerify is the single verification answer both seams return. detected
// carries the provenance of the call (RunDetectedVerifyCmd vs RunVerifyCmd) into
// verifyDetected, so the R3-6 split is assertable.
func (r *phaseRecorder) recordVerify(detected bool, argv []string, timeout time.Duration, logPath string) (VerifyResult, error) {
	r.calls = append(r.calls, "verify")
	r.verifyCalls = append(r.verifyCalls, append([]string(nil), argv...))
	r.verifyTimeouts = append(r.verifyTimeouts, timeout)
	r.verifyDetected = append(r.verifyDetected, detected)
	i := len(r.verifyCalls) - 1
	pass := true
	if i < len(r.verifyPass) {
		pass = r.verifyPass[i]
	}
	duration := int64(1)
	if i < len(r.verifyDurations) {
		duration = r.verifyDurations[i]
	}
	res := VerifyResult{Argv: append([]string(nil), argv...), LogPath: logPath, DurationMS: duration}
	if pass {
		res.ExitCode = 0
		res.Passed = true
		return res, nil
	}
	res.ExitCode = 1
	res.OutputTail = r.verifyTail
	return res, nil
}

// phaseTree builds a workspace with one scoped Go file and a separate run
// directory. The run directory is OUTSIDE the root because the door refuses
// OutDir == Root.
func phaseTree(t *testing.T) (root, outDir string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outDir = filepath.Join(base, "run")
	if err := os.MkdirAll(filepath.Join(root, "internal", "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "alpha", "a.go"), []byte("package alpha\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root, outDir
}

// phasePlanDoc is a one-unit plan authored by the shipped BuildPlan.
func phasePlanDoc(t *testing.T, seat string) []byte {
	t.Helper()
	data, err := BuildPlan("implement the alpha feature", []UnitSpec{{
		Title: "alpha work",
		Scope: []string{"internal/alpha/*.go"},
		Seat:  seat,
		Body:  "Implement the alpha work.",
	}})
	if err != nil {
		t.Fatalf("BuildPlan(%q): %v", seat, err)
	}
	return data
}

// phaseCallIndex returns the position of the first call with the given name.
func phaseCallIndex(calls []string, name string) int {
	for i, c := range calls {
		if c == name {
			return i
		}
	}
	return -1
}

// phaseArgValue returns the value following flag in an argument vector ("" when
// the flag is absent), so the recorder can find the --out directory a phase
// composed without re-implementing the vector.
func phaseArgValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestConsentGateRefusesNonTTYWithoutYes(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{Root: root, OutDir: outDir, Ask: "do the alpha work", WriterSeat: providerSeat}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("WritePhase err = %v, want ErrConsentRequired", err)
	}
	if res == nil || res.Status != StatusConsentRequired {
		t.Fatalf("result = %+v, want status %q (a refusal is still reported)", res, StatusConsentRequired)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v, want none (the consent gate refuses before any snapshot or fanout call)", rec.calls)
	}
	if len(rec.fanoutCalls) != 0 {
		t.Fatalf("fanout calls = %d, want 0", len(rec.fanoutCalls))
	}

	echo := rec.out.String()
	for _, want := range []string{"metered seat", "[provider]", meteredAPINote, noCapNote, "re-run with --yes"} {
		if !strings.Contains(echo, want) {
			t.Errorf("consent echo %q does not contain %q", echo, want)
		}
	}
	// Nothing may have been written under the run directory except the phase
	// log line that records the refusal.
	if _, statErr := os.Stat(filepath.Join(outDir, PhaseWrite, planFileName)); !os.IsNotExist(statErr) {
		t.Errorf("plan.json exists after a consent refusal (err=%v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "manifest.json")); !os.IsNotExist(statErr) {
		t.Errorf("manifest.json exists after a consent refusal (err=%v)", statErr)
	}
	if log, rerr := os.ReadFile(filepath.Join(outDir, phaseLogFileName)); rerr != nil {
		t.Fatalf("phases.log: %v", rerr)
	} else if !strings.Contains(string(log), "phase=write status=consent-required") {
		t.Errorf("phases.log = %q, want the refusal recorded", log)
	}
}

func TestConsentGateTTYAnswerDecides(t *testing.T) {
	root, outDir := phaseTree(t)
	cfg := CodeConfig{Root: root, OutDir: outDir, Ask: "do the alpha work"}
	plan := phasePlanDoc(t, providerSeat)

	t.Run("yes runs", func(t *testing.T) {
		rec := installPhaseRecorder(t, nil)
		PhaseStdin = strings.NewReader("y\n")
		stdinIsTTY = func(io.Reader) bool { return true }
		res, err := WritePhase(cfg, plan)
		if err != nil {
			t.Fatalf("WritePhase: %v", err)
		}
		if len(rec.fanoutCalls) != 1 {
			t.Fatalf("fanout calls = %d, want 1", len(rec.fanoutCalls))
		}
		if res.Status != StatusUnverified {
			t.Fatalf("status = %q, want %q (no verifier in the temp tree)", res.Status, StatusUnverified)
		}
		if !strings.Contains(rec.out.String(), "Proceed? [y/N]") {
			t.Errorf("stdout %q does not show the prompt", rec.out.String())
		}
	})

	t.Run("no refuses", func(t *testing.T) {
		rec := installPhaseRecorder(t, nil)
		PhaseStdin = strings.NewReader("n\n")
		stdinIsTTY = func(io.Reader) bool { return true }
		_, err := WritePhase(cfg, plan)
		if !errors.Is(err, ErrConsentRequired) {
			t.Fatalf("err = %v, want ErrConsentRequired", err)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("calls = %v, want none", rec.calls)
		}
		if !strings.Contains(rec.out.String(), "declined") {
			t.Errorf("stdout %q does not say the run was declined", rec.out.String())
		}
	})
}

func TestConsentGateYesSkipsThePrompt(t *testing.T) {
	root, outDir := phaseTree(t)
	cfg := CodeConfig{Root: root, OutDir: outDir, Ask: "x", WriterSeat: providerSeat, Yes: true}
	rec := installPhaseRecorder(t, nil)

	if _, err := WritePhase(cfg, phasePlanDoc(t, providerSeat)); err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	if len(rec.fanoutCalls) != 1 {
		t.Fatalf("fanout calls = %d, want 1", len(rec.fanoutCalls))
	}
	if strings.Contains(rec.out.String(), "Proceed?") {
		t.Errorf("stdout %q asked for consent despite --yes", rec.out.String())
	}
	if !strings.Contains(rec.out.String(), "consent: --yes") {
		t.Errorf("stdout %q does not record the --yes consent", rec.out.String())
	}
}

func TestPlanPhaseArgvGoldenAndFreeSeats(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Ask:        "audit the plan for the alpha feature",
		PlanSeats:  []string{"native:m1", "native:m2"},
		TimeoutSec: 900,
	}

	res, err := PlanPhase(cfg)
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	if res.Status != StatusOK || !res.Ran || res.ExitCode != 0 {
		t.Fatalf("result = %+v, want a ran ok phase", res)
	}

	phaseDir := filepath.Join(outDir, PhasePlan)
	want := []string{
		"--task", "plan-audit",
		"--seats", "native:m1,native:m2",
		"--tools", "read,search",
		"--root", root,
		"--prompt-file", filepath.Join(phaseDir, askFileName),
		"--out", phaseDir,
		"--json",
		"--timeout", "900",
	}
	if len(rec.fanoutCalls) != 1 {
		t.Fatalf("fanout calls = %d, want 1", len(rec.fanoutCalls))
	}
	if got := rec.fanoutCalls[0]; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v\nwant   %#v", got, want)
	}
	// Native-only seats are free: no consent echo, no prompt.
	if strings.Contains(rec.out.String(), "metered") {
		t.Errorf("native-only plan phase printed a metered consent echo: %q", rec.out.String())
	}
	ask, rerr := os.ReadFile(filepath.Join(phaseDir, askFileName))
	if rerr != nil {
		t.Fatalf("ask file: %v", rerr)
	}
	// The ask file = the fixed read-only block (prompt-level mitigation, the
	// belt) + the request verbatim. The snapshot and the drift rows are the
	// braces; see TestPlanPhaseSnapshotsBeforeTheSeatsWhenAWriteFollows.
	if want := planReadOnlyPrefix + "\n" + cfg.Ask + "\n"; string(ask) != want {
		t.Errorf("ask file = %q, want the read-only block + the request verbatim %q", ask, want)
	}
	if !strings.Contains(string(ask), "Produce a PLAN ONLY. Do not create, edit, or delete ANY file.") {
		t.Errorf("ask file lost the read-only instruction:\n%s", ask)
	}
	log, lerr := os.ReadFile(filepath.Join(outDir, phaseLogFileName))
	if lerr != nil {
		t.Fatalf("phases.log: %v", lerr)
	}
	if !strings.Contains(string(log), "phase=plan status=ok artifacts=plan/ask.txt") {
		t.Errorf("phases.log = %q, want the plan line naming its artifact", log)
	}
}

func TestPlanPhaseDryRunEchoesTheVector(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{Root: root, OutDir: outDir, Ask: "audit", PlanSeats: []string{"native:m1"}, DryRun: true}

	res, err := PlanPhase(cfg)
	if err != nil {
		t.Fatalf("PlanPhase: %v", err)
	}
	if res.Status != StatusDryRun || res.Ran {
		t.Fatalf("result = %+v, want a dry run that ran nothing", res)
	}
	if len(rec.fanoutCalls) != 0 {
		t.Fatalf("fanout calls = %d, want 0 (a dry run must not spawn anything)", len(rec.fanoutCalls))
	}
	if _, statErr := os.Stat(filepath.Join(outDir, PhasePlan, askFileName)); !os.IsNotExist(statErr) {
		t.Errorf("ask.txt exists after a plan dry run (err=%v)", statErr)
	}
	echo := rec.out.String()
	if !strings.Contains(echo, "dry-run") || !strings.Contains(echo, "agy pro fanout --task plan-audit --seats native:m1") {
		t.Errorf("dry-run echo = %q, want the composed command", echo)
	}
	if !strings.Contains(echo, "--out "+filepath.Join(outDir, PhasePlan)) {
		t.Errorf("dry-run echo = %q, want the exact --out path", echo)
	}
}

func TestWritePhaseDryRunEchoesWriteVerifyAndRollback(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Ask:        "x",
		VerifyCmds: [][]string{{"go", "test", "./internal/alpha"}},
		MaxRepairs: 1,
		DryRun:     true,
	}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	if res.Status != StatusDryRun {
		t.Fatalf("status = %q, want %q", res.Status, StatusDryRun)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v, want none", rec.calls)
	}
	writeDir := filepath.Join(outDir, PhaseWrite)
	echo := rec.out.String()
	for _, want := range []string{
		"agy pro fanout --units " + filepath.Join(writeDir, planFileName),
		"--root " + root,
		"--out " + writeDir,
		"--json",
		"verify: go test ./internal/alpha",
		"rollback: agy pro code rollback --run " + outDir,
		noCapNote,
	} {
		if !strings.Contains(echo, want) {
			t.Errorf("dry-run echo = %q\nmissing %q", echo, want)
		}
	}
	// The consent echo precedes the dry-run block only for a metered run; either
	// way, nothing was created.
	if _, statErr := os.Stat(filepath.Join(writeDir, planFileName)); !os.IsNotExist(statErr) {
		t.Errorf("plan.json exists after a write dry run (err=%v)", statErr)
	}
}

func TestWritePhaseSnapshotPrecedesTheFirstFanoutCall(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{Root: root, OutDir: outDir, Yes: true, MaxRepairs: 0}

	if _, err := WritePhase(cfg, phasePlanDoc(t, providerSeat)); err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	snap := phaseCallIndex(rec.calls, "snapshot")
	fanout := phaseCallIndex(rec.calls, "fanout")
	if snap < 0 {
		t.Fatalf("calls = %v, no snapshot call", rec.calls)
	}
	if fanout < 0 {
		t.Fatalf("calls = %v, no fanout call", rec.calls)
	}
	if snap > fanout {
		t.Fatalf("calls = %v, the fanout call precedes the snapshot (rollback would have no source of truth)", rec.calls)
	}
	if rec.calls[0] != "snapshot" {
		t.Fatalf("calls = %v, the phase must snapshot before anything else", rec.calls)
	}
	diff := phaseCallIndex(rec.calls, "diff")
	if diff < fanout {
		t.Fatalf("calls = %v, the diff ran before the write", rec.calls)
	}
}

func TestWritePhaseVerifyFailRepairRefailNamesRollback(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, &phaseRecorder{
		fanoutExits: []int{0, 0},
		verifyPass:  []bool{false, false},
		verifyTail:  "FAIL: TestAlpha: want 1, got 2",
		deltas: []codesnap.FileDelta{
			{Path: "internal/alpha/a.go", Status: codesnap.StatusModified, Added: 3, Removed: 1},
		},
	})
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Yes:        true,
		MaxRepairs: 1,
		VerifyCmds: [][]string{{"go", "test", "./internal/alpha"}},
	}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v (a failed verification is a result, not an error)", err)
	}
	if res.Status != StatusVerifyFailed {
		t.Fatalf("status = %q, want %q", res.Status, StatusVerifyFailed)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (initial write + one repair)", res.Attempts)
	}
	if len(rec.fanoutCalls) != 2 {
		t.Fatalf("fanout calls = %d, want 2", len(rec.fanoutCalls))
	}
	if len(rec.verifyCalls) != 2 {
		t.Fatalf("verify calls = %d, want 2 (re-verify after the repair)", len(rec.verifyCalls))
	}
	repairDir := filepath.Join(outDir, "repair-1")
	repairPlanPath := filepath.Join(repairDir, planFileName)
	if got := rec.fanoutCalls[1]; got[0] != "--units" || got[1] != repairPlanPath {
		t.Fatalf("second argv = %#v, want --units %s", got, repairPlanPath)
	}
	// The repair plan carries the bounded verification tail, and the unit ids
	// are unchanged (same plan, annotated body).
	repairPlan, rerr := os.ReadFile(repairPlanPath)
	if rerr != nil {
		t.Fatalf("repair plan: %v", rerr)
	}
	if !strings.Contains(string(repairPlan), "FAIL: TestAlpha: want 1, got 2") {
		t.Errorf("repair plan does not carry the verification tail:\n%s", repairPlan)
	}
	if !strings.Contains(string(repairPlan), `"id": "u1"`) {
		t.Errorf("repair plan lost the unit id:\n%s", repairPlan)
	}
	if len(res.Diff) != 1 || res.Diff[0].Path != "internal/alpha/a.go" {
		t.Fatalf("diff = %+v, want the recorded delta", res.Diff)
	}
	// No automatic rollback, ever.
	if phaseCallIndex(rec.calls, "rollback") >= 0 {
		t.Fatalf("calls = %v, the write phase rolled the tree back on its own", rec.calls)
	}
	// The rollback command is what the report must name.
	report := AssembleReport(cfg, nil, res)
	rendered := report.Render()
	if !strings.Contains(rendered, "rollback: agy pro code rollback --run") {
		t.Errorf("render =\n%s\nwant a rollback line", rendered)
	}
	if report.Rollback == nil {
		t.Errorf("report.Rollback is nil on a verify-failed run")
	}
}

func TestWritePhaseVerifyPassesAfterOneRepair(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, &phaseRecorder{
		fanoutExits: []int{0, 0},
		verifyPass:  []bool{false, true},
		verifyTail:  "FAIL",
	})
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Yes:        true,
		MaxRepairs: 2,
		VerifyCmds: [][]string{{"go", "build", "./internal/alpha"}},
	}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, StatusOK)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (the second repair is not spent)", res.Attempts)
	}
	if len(rec.fanoutCalls) != 2 {
		t.Fatalf("fanout calls = %d, want 2", len(rec.fanoutCalls))
	}
	if !res.Verified || !res.Verify.Passed {
		t.Fatalf("verify = %+v, want a passing decisive run", res.Verify)
	}
	if report := AssembleReport(cfg, nil, res); report.Rollback != nil {
		t.Errorf("report.Rollback = %+v on a verified run, want nil", report.Rollback)
	}
}

func TestWritePhaseNoVerifierIsUnverified(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{Root: root, OutDir: outDir, Yes: true}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	if res.Status != StatusUnverified {
		t.Fatalf("status = %q, want %q (never an invented pass)", res.Status, StatusUnverified)
	}
	if res.Verified {
		t.Errorf("Verified = true with no verifier")
	}
	if len(rec.verifyCalls) != 0 {
		t.Fatalf("verify calls = %d, want 0", len(rec.verifyCalls))
	}
}

func TestWritePhaseRefusesUnsafePlans(t *testing.T) {
	root, outDir := phaseTree(t)

	// A hand-written plan is the only way to name a native write seat: BuildPlan
	// refuses one, so this pins the second layer.
	nativePlan := []byte(`[{"id":"u1","title":"t","scope":["internal/alpha/*.go"],"deliverable":"d",
		"seat":"native:m1","write":true}]`)

	cases := []struct {
		name string
		cfg  CodeConfig
		plan []byte
		want string
	}{
		{
			name: "native unit seat",
			cfg:  CodeConfig{Root: root, OutDir: outDir, Yes: true},
			plan: nativePlan,
			want: "provider-only",
		},
		{
			name: "empty unit seat",
			cfg:  CodeConfig{Root: root, OutDir: outDir, Yes: true},
			plan: []byte(`[{"id":"u1","title":"t","scope":["internal/alpha/*.go"],"deliverable":"d","write":true}]`),
			want: "no seat",
		},
		{
			name: "native writer seat",
			cfg:  CodeConfig{Root: root, OutDir: outDir, WriterSeat: "native:m1", Yes: true},
			plan: phasePlanDoc(t, providerSeat),
			want: "exactly one provider seat",
		},
		{
			name: "malformed plan",
			cfg:  CodeConfig{Root: root, OutDir: outDir, Yes: true},
			plan: []byte(`{"not":"an array"}`),
			want: "not a valid partition plan",
		},
		{
			name: "empty plan",
			cfg:  CodeConfig{Root: root, OutDir: outDir, Yes: true},
			plan: []byte("  \n"),
			want: "empty plan",
		},
		{
			name: "out dir is the root",
			cfg:  CodeConfig{Root: root, OutDir: root, Yes: true},
			plan: phasePlanDoc(t, providerSeat),
			want: "must not be the workspace root",
		},
		{
			name: "missing root",
			cfg:  CodeConfig{Root: filepath.Join(root, "absent"), OutDir: outDir, Yes: true},
			plan: phasePlanDoc(t, providerSeat),
			want: "does not exist",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := installPhaseRecorder(t, nil)
			_, err := WritePhase(tc.cfg, tc.plan)
			if err == nil {
				t.Fatalf("WritePhase accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want %q", err.Error(), tc.want)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("calls = %v, want none (a refused plan must not snapshot or run)", rec.calls)
			}
		})
	}
}

func TestWritePhaseSnapshotRefusalStopsTheRun(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, &phaseRecorder{snapshotErr: errors.New("snapshot refused: 1 claimed file")})
	cfg := CodeConfig{Root: root, OutDir: outDir, Yes: true}

	_, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err == nil || !strings.Contains(err.Error(), "snapshot refused") {
		t.Fatalf("err = %v, want the snapshot refusal", err)
	}
	if len(rec.fanoutCalls) != 0 {
		t.Fatalf("fanout calls = %d, want 0 (no write without a snapshot)", len(rec.fanoutCalls))
	}
}

func TestWritePhaseFanoutFailureIsAFailedResult(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, &phaseRecorder{fanoutExits: []int{3}})
	cfg := CodeConfig{Root: root, OutDir: outDir, Yes: true, VerifyCmds: [][]string{{"go", "build", "./..."}}}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err == nil {
		t.Fatalf("WritePhase returned no error for a non-zero fanout exit")
	}
	if res == nil || res.Status != StatusWriteFailed {
		t.Fatalf("result = %+v, want status %q", res, StatusWriteFailed)
	}
	if len(rec.verifyCalls) != 0 {
		t.Errorf("verify calls = %d, want 0 (a failed write is not verified)", len(rec.verifyCalls))
	}
	if phaseCallIndex(rec.calls, "diff") < 0 {
		t.Errorf("calls = %v, want a post-failure diff", rec.calls)
	}
	if !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("err = %q, want the exit code", err.Error())
	}
}

func TestReviewPhasePromptAndArgv(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	reportPath := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(reportPath, []byte("files: 1 changed (0 created, 1 modified) +2 -1\n"), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	cfg := CodeConfig{Root: root, OutDir: outDir, ReviewSeats: []string{"native:m1"}}

	res, err := ReviewPhase(cfg, reportPath)
	if err != nil {
		t.Fatalf("ReviewPhase: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, StatusOK)
	}
	if len(rec.fanoutCalls) != 1 {
		t.Fatalf("fanout calls = %d, want 1", len(rec.fanoutCalls))
	}
	phaseDir := filepath.Join(outDir, PhaseReview)
	want := []string{
		"--task", "review",
		"--seats", "native:m1",
		"--tools", "read,search",
		"--root", root,
		"--prompt-file", filepath.Join(phaseDir, promptFileName),
		"--out", phaseDir,
		"--json",
	}
	if got := rec.fanoutCalls[0]; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v\nwant   %#v", got, want)
	}
	prompt, rerr := os.ReadFile(filepath.Join(phaseDir, promptFileName))
	if rerr != nil {
		t.Fatalf("prompt: %v", rerr)
	}
	for _, wantPart := range []string{"# Code review request", "## Rules (fixed)", "UNTRUSTED-DATA", "files: 1 changed"} {
		if !strings.Contains(string(prompt), wantPart) {
			t.Errorf("prompt missing %q:\n%s", wantPart, prompt)
		}
	}
}

func TestReviewPhaseRefusesUnreadableReport(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{Root: root, OutDir: outDir, ReviewSeats: []string{"native:m1"}}

	if _, err := ReviewPhase(cfg, filepath.Join(outDir, "absent.md")); err == nil {
		t.Fatalf("ReviewPhase accepted a missing change report")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v, want none", rec.calls)
	}
}

func TestResolveSeatsFailsClosed(t *testing.T) {
	root, outDir := phaseTree(t)
	installPhaseRecorder(t, nil)

	cases := []struct {
		name string
		cfg  CodeConfig
		want string
	}{
		{"no seats anywhere", CodeConfig{Root: root, OutDir: outDir, Ask: "x"}, "no seats"},
		{"malformed seat", CodeConfig{Root: root, OutDir: outDir, Ask: "x", PlanSeats: []string{"native:bad/id"}}, "seat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PlanPhase(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// The run-wide pool is the documented fallback.
	rec := installPhaseRecorder(t, nil)
	if _, err := PlanPhase(CodeConfig{Root: root, OutDir: outDir, Ask: "x", Seats: []string{"native:m1"}}); err != nil {
		t.Fatalf("PlanPhase with the run-wide pool: %v", err)
	}
	if got := rec.fanoutCalls[0]; !strings.Contains(strings.Join(got, " "), "--seats native:m1") {
		t.Fatalf("argv = %v, want the run-wide seat pool", got)
	}
}

func TestRollbackPhaseRequiresManifest(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := RollbackPhase(CodeConfig{Root: root, OutDir: outDir})
	if err == nil {
		t.Fatalf("RollbackPhase accepted a run directory with no manifest")
	}
	if !strings.Contains(err.Error(), "manifest") {
		t.Errorf("err = %q, want it to name the missing manifest", err.Error())
	}
	if phaseCallIndex(rec.calls, "rollback") >= 0 {
		t.Fatalf("calls = %v, the rollback touched the tree without a manifest", rec.calls)
	}
}

func TestRollbackPhaseRestoresAndArchives(t *testing.T) {
	root, outDir := phaseTree(t)
	// Real codesnap seams: this test is the end-to-end pin for the rollback path.
	cfg := CodeConfig{Root: root, OutDir: outDir}
	scoped := []string{"internal/alpha/*.go"}

	if _, err := TakeSnapshot(root, scoped, outDir); err != nil {
		t.Fatalf("Take: %v", err)
	}
	target := filepath.Join(root, "internal", "alpha", "a.go")
	if err := os.WriteFile(target, []byte("package alpha // changed\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	created := filepath.Join(root, "internal", "alpha", "b.go")
	if err := os.WriteFile(created, []byte("package alpha\n"), 0o644); err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := RollbackPhase(cfg)
	if err != nil {
		t.Fatalf("RollbackPhase: %v", err)
	}
	if res.Status != StatusRolledBack || res.Rollback == nil {
		t.Fatalf("result = %+v, want a rolled-back status with the report", res)
	}
	restored, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatalf("read restored file: %v", rerr)
	}
	if string(restored) != "package alpha\n" {
		t.Errorf("restored file = %q, want the snapshot bytes", restored)
	}
	if _, statErr := os.Stat(created); !os.IsNotExist(statErr) {
		t.Errorf("created file still in the tree after rollback (err=%v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "rollback-archive", "internal", "alpha", "b.go")); statErr != nil {
		t.Errorf("created file was not archived: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "rollback.json")); statErr != nil {
		t.Errorf("rollback.json missing: %v", statErr)
	}
}

func TestRollbackPhaseDryRunTouchesNothing(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	res, err := RollbackPhase(CodeConfig{Root: root, OutDir: outDir, DryRun: true})
	if err != nil {
		t.Fatalf("RollbackPhase: %v", err)
	}
	if res.Status != StatusDryRun {
		t.Fatalf("status = %q, want %q", res.Status, StatusDryRun)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v, want none", rec.calls)
	}
	if !strings.Contains(rec.out.String(), "dry-run") {
		t.Errorf("stdout = %q, want the dry-run echo", rec.out.String())
	}
}

// TestComposedArgvFlagsExistInCli pins every flag the phase vectors use against
// the SHIPPED parser's flag set. internal/fanout's parser is fail-closed: an
// unknown flag exits 2, and a run that dies there has already cost the operator
// the setup. Reading cli.go is the only way to assert this without spawning the
// binary.
func TestComposedArgvFlagsExistInCli(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "fanout", "cli.go"))
	if err != nil {
		t.Fatalf("read cli.go: %v", err)
	}
	flagRe := regexp.MustCompile(`flags\.(?:String|Int|Bool|Func|StringVar)\("([a-z-]+)"`)
	known := map[string]bool{}
	for _, m := range flagRe.FindAllStringSubmatch(string(source), -1) {
		known[m[1]] = true
	}
	if len(known) < 10 {
		t.Fatalf("extracted only %d flags from cli.go; the regexp or the file shape changed", len(known))
	}

	root, outDir := phaseTree(t)
	cfg := CodeConfig{
		Root:        root,
		OutDir:      outDir,
		Ask:         "x",
		TimeoutSec:  600,
		VerifyCmds:  [][]string{{"go", "build", "./..."}},
		PlanSeats:   []string{"native:m1"},
		ReviewSeats: []string{"native:m1"},
	}
	vectors := map[string][]string{
		"plan":   planPhaseArgs(cfg, cfg.PlanSeats, filepath.Join(outDir, PhasePlan, askFileName), filepath.Join(outDir, PhasePlan)),
		"write":  unitsPhaseArgs(cfg, filepath.Join(outDir, PhaseWrite, planFileName), filepath.Join(outDir, PhaseWrite)),
		"review": reviewPhaseArgs(cfg, cfg.ReviewSeats, filepath.Join(outDir, PhaseReview, promptFileName), filepath.Join(outDir, PhaseReview)),
	}
	for name, argv := range vectors {
		for _, token := range argv {
			if strings.HasPrefix(token, "--") {
				if !known[strings.TrimPrefix(token, "--")] {
					t.Errorf("%s vector uses %q, which cli.go does not define", name, token)
				}
			}
		}
		// Partition mode's mutual exclusions (cli.go:563-581, :594-597).
		if name == "write" {
			for _, forbidden := range []string{"--seats", "--models", "--prompt", "--prompt-file", "--tools", "--task"} {
				if strings.Contains(strings.Join(argv, " "), forbidden) {
					t.Errorf("write vector carries %q, which partition mode refuses", forbidden)
				}
			}
		}
	}
}

func TestFormatCommandMasksAndQuotes(t *testing.T) {
	got := formatCommand([]string{"--prompt", "two words", "--task", "AKIAABCDEFGHIJKLMNOP"})
	if !strings.Contains(got, `"two words"`) {
		t.Errorf("formatCommand = %q, want the spaced argument quoted", got)
	}
	if strings.Contains(got, "AKIAABCDEFGHIJKLMNOP") {
		t.Errorf("formatCommand = %q, leaked a key-shaped operand", got)
	}
	if !strings.HasPrefix(got, "agy pro fanout ") {
		t.Errorf("formatCommand = %q, want the fanout prefix", got)
	}
	if bare := formatArgv([]string{"go", "test", "./x"}); bare != "go test ./x" {
		t.Errorf("formatArgv = %q", bare)
	}
}

func TestSeamFunctionsAreBound(t *testing.T) {
	// A nil seam would panic far from the cause; this is the cheap pin that the
	// package-level seams are all assigned (the verb relies on the defaults).
	seams := map[string]any{
		"RunFanout":            RunFanout,
		"TakeSnapshot":         TakeSnapshot,
		"ComputeDiff":          ComputeDiff,
		"RollbackTree":         RollbackTree,
		"LoadSnapshotManifest": LoadSnapshotManifest,
		"RunVerifyCmd":         RunVerifyCmd,
		"PhaseStdin":           PhaseStdin,
		"PhaseStdout":          PhaseStdout,
		"PhaseStderr":          PhaseStderr,
		"stdinIsTTY":           stdinIsTTY,
	}
	for name, fn := range seams {
		if fn == nil {
			t.Errorf("seam %s is nil", name)
		}
	}
}
