package codework

// Pins for the repeatable verifier list (U5b): every --verify line the operator
// named runs, in order, and all of them must pass; the first failure starts the
// repair flow and the re-verify after a repair runs the FULL list again. The
// detection path is pinned the same way, and the gate echo is pinned to count a
// seat once.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestWritePhaseRunsEveryVerifierInOrder: two verifiers, both pass, both ran.
func TestWritePhaseRunsEveryVerifierInOrder(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Yes:        true,
		MaxRepairs: 1,
		VerifyCmds: [][]string{{"go", "build", "./..."}, {"go", "vet", "./..."}},
	}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, StatusOK)
	}
	if len(rec.verifyCalls) != 2 {
		t.Fatalf("verify calls = %v; want both commands", rec.verifyCalls)
	}
	if got := strings.Join(rec.verifyCalls[0], " "); got != "go build ./..." {
		t.Errorf("first verifier = %q; want the first flag", got)
	}
	if got := strings.Join(rec.verifyCalls[1], " "); got != "go vet ./..." {
		t.Errorf("second verifier = %q; want the second flag", got)
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d; want 1 (both passed, no repair)", res.Attempts)
	}
	if len(res.VerifyRuns) != 2 {
		t.Fatalf("verify runs = %+v; want one row per command", res.VerifyRuns)
	}
	for i, run := range res.VerifyRuns {
		if run.Attempt != 1 {
			t.Errorf("run %d attempt = %d; want 1", i+1, run.Attempt)
		}
		if want := filepath.Join(outDir, verifyDirName, "attempt-1-"+strconv.Itoa(i+1)+".log"); run.LogPath != want {
			t.Errorf("run %d log = %q; want %q", i+1, run.LogPath, want)
		}
	}
	// The decisive run is the LAST one when every command passed.
	if !res.Verify.Passed || res.Verify.Argv[len(res.Verify.Argv)-1] != "./..." || res.Verify.Argv[1] != "vet" {
		t.Errorf("decisive run = %+v; want the last (vet) command", res.Verify)
	}
}

// TestWritePhaseStopsAtFirstFailingVerifierAndReverifiesAll: the first failure
// short-circuits the attempt (the later verifiers would report against a tree the
// repair is about to change), and the repair attempt runs the whole list again.
func TestWritePhaseStopsAtFirstFailingVerifierAndReverifiesAll(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, &phaseRecorder{
		fanoutExits: []int{0, 0},
		verifyPass:  []bool{false, true, true},
		verifyTail:  "build failed: undefined: alpha",
	})
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Yes:        true,
		MaxRepairs: 1,
		VerifyCmds: [][]string{{"go", "build", "./..."}, {"go", "vet", "./..."}},
	}

	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v (a failed verification is a result, not an error)", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q; want %q (the repair fixed the build)", res.Status, StatusOK)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d; want 2", res.Attempts)
	}
	// attempt 1: build fails, vet never runs; attempt 2: build + vet.
	wantCalls := []string{"go build ./...", "go build ./...", "go vet ./..."}
	if len(rec.verifyCalls) != len(wantCalls) {
		t.Fatalf("verify calls = %v; want %v", rec.verifyCalls, wantCalls)
	}
	for i, want := range wantCalls {
		if got := strings.Join(rec.verifyCalls[i], " "); got != want {
			t.Errorf("verify call %d = %q; want %q", i+1, got, want)
		}
	}
	wantAttempts := []int{1, 2, 2}
	if len(res.VerifyRuns) != len(wantAttempts) {
		t.Fatalf("verify runs = %+v; want %d rows", res.VerifyRuns, len(wantAttempts))
	}
	for i, want := range wantAttempts {
		if res.VerifyRuns[i].Attempt != want {
			t.Errorf("run %d attempt = %d; want %d", i+1, res.VerifyRuns[i].Attempt, want)
		}
	}
	// The repair plan was built from the FAILING command's tail.
	repairPlan, rerr := os.ReadFile(filepath.Join(outDir, "repair-1", planFileName))
	if rerr != nil {
		t.Fatalf("repair plan: %v", rerr)
	}
	if !strings.Contains(string(repairPlan), "undefined: alpha") {
		t.Errorf("repair plan does not carry the failing verifier's tail:\n%s", repairPlan)
	}
}

// TestWritePhaseDetectedVerifiersAllRun is the detection-side pin: DetectVerify
// returns one compile-only pair per named package directory (build then vet, S1),
// and every detected line executes. No `go test` line exists any more, even
// though the plan names a _test.go file.
func TestWritePhaseDetectedVerifiersAllRun(t *testing.T) {
	root, outDir := phaseTree(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "alpha", "a_test.go"), []byte("package alpha\n"), 0o600); err != nil {
		t.Fatalf("write _test.go: %v", err)
	}
	rec := installPhaseRecorder(t, nil)

	// The plan names a .go file and a _test.go file; detection yields the
	// compile-only pair for the package, and nothing that executes it.
	plan, berr := BuildPlan("implement the alpha feature", []UnitSpec{{
		Title: "alpha work",
		Scope: []string{"internal/alpha/a.go", "internal/alpha/a_test.go"},
		Seat:  providerSeat,
		Body:  "Implement the alpha work.",
	}})
	if berr != nil {
		t.Fatalf("BuildPlan: %v", berr)
	}
	cfg := CodeConfig{Root: root, OutDir: outDir, Yes: true, MaxRepairs: 1}

	res, err := WritePhase(cfg, plan)
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	wantCalls := []string{"go build ./internal/alpha", "go vet ./internal/alpha"}
	if len(rec.verifyCalls) != len(wantCalls) {
		t.Fatalf("verify calls = %v; want the detected %v", rec.verifyCalls, wantCalls)
	}
	for i, want := range wantCalls {
		if got := strings.Join(rec.verifyCalls[i], " "); got != want {
			t.Errorf("verify call %d = %q; want %q", i+1, got, want)
		}
	}
	if res.Status != StatusOK {
		t.Errorf("status = %q; want %q", res.Status, StatusOK)
	}
}

// TestWritePhaseGateSeatsCountedOnce pins the double-count defect: a one-unit
// plan authored from --writer carries the writer as its unit seat, so the gate
// list is the same spec twice. The echo is what the operator consents to, so it
// must say one metered seat.
func TestWritePhaseGateSeatsCountedOnce(t *testing.T) {
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Yes:        true,
		MaxRepairs: 0,
		WriterSeat: providerSeat, // the same spec the plan's unit names
	}

	if _, err := WritePhase(cfg, phasePlanDoc(t, providerSeat)); err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	echo := rec.out.String()
	if !strings.Contains(echo, "1 metered seat(s)") {
		t.Fatalf("echo = %q; want one metered seat for a single-seat run", echo)
	}
	if strings.Contains(echo, "2 metered seat(s)") {
		t.Errorf("echo = %q; the same seat was counted twice", echo)
	}
	if n := strings.Count(echo, "seat 1  [provider]"); n != 1 {
		t.Errorf("echo lists %d seat-1 rows; want 1:\n%s", n, echo)
	}
	if strings.Contains(echo, "seat 2") {
		t.Errorf("echo invented a second seat row:\n%s", echo)
	}
	if echo := rec.errOut.String(); echo != "" {
		t.Errorf("stderr = %q; want nothing", echo)
	}
}

// TestDedupeSeatsKeepsOrderAndFirstSpelling pins the helper: exact-spec dedupe,
// first occurrence wins, native seats survive (they are gated out later).
func TestDedupeSeatsKeepsOrderAndFirstSpelling(t *testing.T) {
	got := dedupeSeats([]string{"a/b", " native:m1 ", "a/b", "", "native:m1", "c/d"})
	want := []string{"a/b", "native:m1", "c/d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("dedupeSeats = %v; want %v", got, want)
	}
	if got := dedupeSeats(nil); got != nil {
		t.Fatalf("dedupeSeats(nil) = %v; want nil", got)
	}
	if got := meteredSeats([]string{providerSeat, providerSeat}); len(got) != 1 {
		t.Fatalf("meteredSeats on a doubled spec = %v; want one entry", got)
	}
}
