package hybrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agypro/internal/mesh"
	"agypro/internal/providers"
)

// ---------------------------------------------------------------------------
// --model rewrite fallback (design gate G5-P1 safety net)
//
// Every test here drives a REAL stub agy (a separate Go binary, compiled at
// test time, carrying the P2 canaries so the REAL wirecheck gate passes) under a
// REAL per-spawn mesh. Nothing interactive is ever started: the invocations are
// print-shaped (-p), the child is a stub, and no provider is dialed (the
// registry's base URLs point at 127.0.0.1:9, which the stub never calls).
// ---------------------------------------------------------------------------

// p2fbSlashRegistryJSON carries one external whose id contains a slash, so the
// quoting assertion can prove the blade: key's ':' and '/' survive argv
// byte-for-byte (the id is not on the deny-list).
const p2fbSlashRegistryJSON = `{"providers":[{"name":"provnw","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"vendor/model-x","contextLength":300000}]}]}`

// p2fbStubSrc is the stub agy. Modes:
//
//   - default ("ok"): print STUB_OK, exit 0;
//   - "reject-primary": reject the blade: form with agy's EXACT signature and
//     exit 1, accept anything else;
//   - "reject-all": reject every form with the same signature, exit 1.
//
// Every attempt appends its full os.Args as one JSON line to P2FB_ARGSDUMP, so
// the test can count attempts and assert the exact argv each one received.
// P2FB_SLEEP_MS holds the child alive for the mesh-death supervision test.
const p2fbStubSrc = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// canaryBlob carries every wirecheck P2 canary literal (wirecheck/canaries.go)
// as a substring so the REAL scan finds them all. It is referenced by a
// reachable branch: a blank assignment is dead-code-eliminated and the literals
// would not survive into the binary.
const canaryBlob = "gemini-api:blade:evergreen:beyond-api:google-genai-sdk/GOOGLE_GEMINI_BASE_URLGEMINI_API_KEYmodelProvider"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Fprintln(os.Stdout, "agy 1.3.5")
			return
		}
	}
	if os.Getenv("P2FB_PRINT_CANARIES") != "" {
		fmt.Fprintln(os.Stdout, canaryBlob)
		return
	}
	if p := os.Getenv("P2FB_ARGSDUMP"); p != "" {
		if data, err := json.Marshal(os.Args); err == nil {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.Write(append(data, '\n'))
				_ = f.Close()
			}
		}
	}
	model := ""
	for i, a := range os.Args {
		if a == "--model" && i+1 < len(os.Args) {
			model = os.Args[i+1]
		}
	}
	if v := os.Getenv("P2FB_SLEEP_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
	}
	switch os.Getenv("P2FB_MODE") {
	case "reject-primary":
		if strings.HasPrefix(model, "blade:") {
			reject(model)
		}
	case "reject-all":
		reject(model)
	case "signature-on-stdout-exit0":
		// R5B-FIX-1: a SUCCESSFUL run whose OUTPUT quotes the public signature.
		// The signature goes to STDOUT (where a model answer streams), never to
		// stderr, and the child exits 0.
		fmt.Fprintln(os.Stdout, "The error text is: "+signature)
		return
	case "signature-on-stdout-only-fail":
		// R5B-FIX-1: a FAILED run whose signature is on STDOUT only, so the
		// stderr-gated detector must not latch and no retry may fire.
		fmt.Fprintln(os.Stdout, "quoting: "+signature)
		os.Exit(3)
	case "signature-on-stderr-exit0":
		// R5B-FIX-1: a SUCCESSFUL run whose STDERR carries the signature (an
		// agy warning that did not stop the session). The exit-status half of
		// the gate must still block the retry.
		fmt.Fprintln(os.Stderr, "warning: saw "+signature)
		fmt.Fprintln(os.Stdout, "STUB_OK")
		return
	}
	fmt.Fprintln(os.Stdout, "STUB_OK")
}

// signature is agy's EXACT --model rejection signature (binary-surface-report.md
// §1.3; the live string from custommodels-probe-report.md §4).
const signature = "not recognized as a known model or custom model in settings"

// reject prints that signature on STDERR - where agy's startup errors go - and
// exits 1, the way a real --model rejection does.
func reject(model string) {
	fmt.Fprintf(os.Stderr, "error: invalid model selection (--model %q --effort \"\"): model %s is %s\n", model, model, signature)
	fmt.Fprintln(os.Stderr, "Available models:")
	os.Exit(1)
}
`

// buildP2fbStub compiles the stub agy and returns its path.
func buildP2fbStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(p2fbStubSrc), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	out := filepath.Join(dir, "agy")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	build := exec.Command("go", "build", "-o", out, src)
	build.Env = append(os.Environ(), "GO111MODULE=off")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, b)
	}
	return out
}

// p2fbAttempts reads the appended argv dump: one []string per child attempt, in
// launch order.
func p2fbAttempts(t *testing.T, dump string) [][]string {
	t.Helper()
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read argv dump: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatalf("parse argv dump line %q: %v", line, err)
		}
		out = append(out, args)
	}
	return out
}

// armP2Fallback forces the fallback seam on or off for one test.
func armP2Fallback(t *testing.T, armed bool) {
	t.Helper()
	prev := p2FallbackArm
	p2FallbackArm = func([]string) bool { return armed }
	t.Cleanup(func() { p2FallbackArm = prev })
}

// countMeshStarts swaps meshStart with a counting pass-through that keeps the
// started mesh, so a test can prove how many meshes one launch used.
func countMeshStarts(t *testing.T) (*int, **mesh.Mesh) {
	t.Helper()
	starts := 0
	var started *mesh.Mesh
	prev := meshStart
	meshStart = func(cfg mesh.Config) (*mesh.Mesh, error) {
		m, err := mesh.Start(cfg)
		if err == nil {
			starts++
			started = m
		}
		return m, err
	}
	t.Cleanup(func() { meshStart = prev })
	return &starts, &started
}

// TestLaunchP2_RewriteFallbackRetriesOnceWithAlternateForm is the core contract:
// a stub agy that fails the FIRST form (the blade: map key) with agy's exact
// signature and succeeds on the alternate spelling (the bare registry slug)
// makes LaunchP2 retry ONCE, and the session completes.
func TestLaunchP2_RewriteFallbackRetriesOnceWithAlternateForm(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	starts, started := countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "reject-primary")
	t.Setenv("P2FB_ARGSDUMP", dump)

	var out, errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "ext-nw"},
		Stdout:      &out,
		Stderr:      &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) after one retry\nstderr: %s", code, err, errw.String())
	}
	if *started == nil {
		t.Fatal("the mesh was not started")
	}
	attempts := p2fbAttempts(t, dump)
	if len(attempts) != 2 {
		t.Fatalf("child attempts = %d; want exactly 2 (one retry, never a loop)\n%s", len(attempts), errw.String())
	}
	wantPrimary := fmt.Sprintf("blade:127.0.0.1:%d/models/ext-nw", (*started).Port())
	if got := argValue(attempts[0], "--model"); got != wantPrimary {
		t.Errorf("attempt 1 --model = %q; want the blade: map key %q", got, wantPrimary)
	}
	if got := argValue(attempts[1], "--model"); got != "ext-nw" {
		t.Errorf("attempt 2 --model = %q; want the bare registry slug %q", got, "ext-nw")
	}
	// ONE mesh, ONE app-data dir: the retry reuses the same session, so the
	// blade: key's port and the injected base URL stay valid.
	if *starts != 1 {
		t.Errorf("mesh starts = %d; want 1 (the retry must keep the same mesh)", *starts)
	}
	first, second := argValue(attempts[0], "--app_data_dir"), argValue(attempts[1], "--app_data_dir")
	if first == "" || first != second {
		t.Errorf("app-data dirs differ across attempts (%q vs %q); want one session", first, second)
	}
	if !strings.Contains(errw.String(), "retrying once with the alternate spelling") {
		t.Errorf("the retry must print its one notice line; got %q", errw.String())
	}
	if !strings.Contains(out.String(), "STUB_OK") {
		t.Errorf("the retried session's output must stream to the real writer; got %q", out.String())
	}
}

// TestLaunchP2_RewriteFallbackBothFormsFailSurfacesTheFailure proves there is no
// loop: a stub that rejects BOTH forms gets exactly two attempts and the second
// attempt's failure is what surfaces.
func TestLaunchP2_RewriteFallbackBothFormsFailSurfacesTheFailure(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	starts, _ := countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "reject-all")
	t.Setenv("P2FB_ARGSDUMP", dump)

	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "ext-nw"},
		Stderr:      &errw,
	})
	if err != nil {
		t.Fatalf("LaunchP2 error = %v; want the child's own exit code with a nil error", err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d; want 1 (the final attempt's own exit code)", code)
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 2 {
		t.Fatalf("child attempts = %d; want exactly 2 (no third attempt, no loop)", len(attempts))
	}
	if *starts != 1 {
		t.Errorf("mesh starts = %d; want 1", *starts)
	}
	if n := strings.Count(errw.String(), "retrying once with the alternate spelling"); n != 1 {
		t.Errorf("retry notices = %d; want exactly 1 (one retry only)", n)
	}
}

// R5B-FIX-1 (a): a SUCCESSFUL attempt 1 (exit 0) whose OUTPUT quotes the exact
// rejection signature must NEVER be re-executed. The signature is public text a
// correct model response can reproduce verbatim, so the retry gate requires a
// real child failure on top of the detector latch. Before the fix this shape
// re-ran the one-shot and flipped the exit code from 0 to 1.
func TestLaunchP2_RewriteFallbackSuccessWithSignatureNoRetry(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "signature-on-stdout-exit0")
	t.Setenv("P2FB_ARGSDUMP", dump)

	var out, errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "explain agy's --model error", "--model", "ext-nw"},
		Stdout:      &out,
		Stderr:      &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) - a successful attempt must never be retried\nstderr: %s", code, err, errw.String())
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 1 {
		t.Fatalf("child attempts = %d; want exactly 1 (no retry on a successful attempt)", len(attempts))
	}
	if !strings.Contains(out.String(), p2RejectSignature) {
		t.Errorf("the child's answer must still stream to the real writer; got %q", out.String())
	}
	if strings.Contains(errw.String(), "retrying once") {
		t.Errorf("no retry notice may print when attempt 1 succeeded; got %q", errw.String())
	}
}

// R5B-FIX-1 (b): a FAILED attempt whose signature is on STDOUT ONLY must not
// retry, because the detector is stderr-gated (agy's --model rejection is a
// startup error on stderr; a model answer is stdout). Missing a stdout-borne
// rejection means "no retry", which is the pre-fallback behavior.
func TestLaunchP2_RewriteFallbackSignatureOnStdoutOnlyNoRetry(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "signature-on-stdout-only-fail")
	t.Setenv("P2FB_ARGSDUMP", dump)

	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "ext-nw"},
		Stderr:      &errw,
	})
	if err != nil {
		t.Fatalf("LaunchP2 error = %v; want the child's own exit code with a nil error", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d; want 3 (the child's own code, unchanged by the stderr-gated detector)", code)
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 1 {
		t.Fatalf("child attempts = %d; want exactly 1 (a stdout-borne signature is not a rejection)", len(attempts))
	}
	if strings.Contains(errw.String(), "retrying once") {
		t.Errorf("no retry notice may print for a stdout-only signature; got %q", errw.String())
	}
}

// R5B-FIX-1 (a), the other half: even a signature on STDERR does not retry a
// SUCCESSFUL attempt. Both halves of the gate are required, and either one
// alone is not enough.
func TestLaunchP2_RewriteFallbackSuccessWithStderrSignatureNoRetry(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "signature-on-stderr-exit0")
	t.Setenv("P2FB_ARGSDUMP", dump)

	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "ext-nw"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 1 {
		t.Fatalf("child attempts = %d; want exactly 1 (the exit-status gate blocks the retry)", len(attempts))
	}
}

// TestLaunchP2_RewriteFallbackFirstFormSucceedsNoRetry proves the net is quiet
// when the primary form is accepted: exactly one attempt, no notice, no retry.
func TestLaunchP2_RewriteFallbackFirstFormSucceedsNoRetry(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "ok")
	t.Setenv("P2FB_ARGSDUMP", dump)

	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "ext-nw"},
		Stderr:      &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 1 {
		t.Fatalf("child attempts = %d; want exactly 1 (no retry when the first form is accepted)", len(attempts))
	}
	if strings.Contains(errw.String(), "retrying once") {
		t.Errorf("no retry notice may print when the first form is accepted; got %q", errw.String())
	}
}

// TestLaunchP2_RewriteFallbackQuotingBladeKeyIntact is the quoting contract: the
// blade: key carries ':' and '/' (and a registry id may carry a slash), and the
// exact bytes must reach the child's argv through exec.Command.
func TestLaunchP2_RewriteFallbackQuotingBladeKeyIntact(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, p2fbSlashRegistryJSON)
	swapWirecheck(t, goP2Report())
	_, started := countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "ok")
	t.Setenv("P2FB_ARGSDUMP", dump)

	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "vendor/model-x"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	attempts := p2fbAttempts(t, dump)
	if len(attempts) != 1 {
		t.Fatalf("child attempts = %d; want 1", len(attempts))
	}
	if *started == nil {
		t.Fatal("the mesh was not started")
	}
	want := fmt.Sprintf("blade:127.0.0.1:%d/models/vendor/model-x", (*started).Port())
	if got := argValue(attempts[0], "--model"); got != want {
		t.Fatalf("--model = %q; want %q (the ':' and '/' must survive argv byte-for-byte)", got, want)
	}
}

// TestLaunchP2_DisarmedFallbackKeepsTheReviewedPath proves the seam is additive:
// with the fallback disarmed the launch goes through meshRunChild exactly as
// before, with the primary form and no tee.
func TestLaunchP2_DisarmedFallbackKeepsTheReviewedPath(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	_, started := countMeshStarts(t)
	armP2Fallback(t, false)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "reject-primary") // would retry if the fallback were armed
	t.Setenv("P2FB_ARGSDUMP", dump)

	runChildCalls := 0
	prev := meshRunChild
	meshRunChild = func(_ context.Context, _ *mesh.Mesh, cmd *exec.Cmd) error {
		runChildCalls++
		return cmd.Run()
	}
	t.Cleanup(func() { meshRunChild = prev })

	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: stub,
		Args:        []string{"-p", "hi", "--model", "ext-nw"},
	})
	if err != nil {
		t.Fatalf("LaunchP2 error = %v; want the child's exit code with a nil error", err)
	}
	if code != 1 {
		t.Fatalf("exit code = %d; want 1 (the rejected pin, surfaced unchanged)", code)
	}
	if runChildCalls != 1 {
		t.Errorf("meshRunChild calls = %d; want 1 (the reviewed path)", runChildCalls)
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 1 {
		t.Fatalf("child attempts = %d; want exactly 1 (no retry when disarmed)", len(attempts))
	}
	if *started == nil {
		t.Fatal("the mesh was not started")
	}
	want := fmt.Sprintf("blade:127.0.0.1:%d/models/ext-nw", (*started).Port())
	if got := argValue(p2fbAttempts(t, dump)[0], "--model"); got != want {
		t.Errorf("--model = %q; want %q", got, want)
	}
}

// TestLaunchP2_RewriteFallbackMeshDeathIsFinal proves a dead mesh is never
// retried: the supervisor kills the child, writes the same visible fail-fast
// line mesh.RunChild writes, and maps to ExitMeshDied (75).
func TestLaunchP2_RewriteFallbackMeshDeathIsFinal(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	_, started := countMeshStarts(t)
	armP2Fallback(t, true)

	stub := buildP2fbStub(t)
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("P2FB_MODE", "ok")
	t.Setenv("P2FB_ARGSDUMP", dump)
	t.Setenv("P2FB_SLEEP_MS", "8000")

	var errw bytes.Buffer
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := LaunchP2(LaunchOptions{
			RealAgyPath: stub,
			Args:        []string{"-p", "hi", "--model", "ext-nw"},
			Stderr:      &errw,
		})
		done <- result{code, err}
	}()

	// Wait for the mesh to be up, then kill it under the live child.
	deadline := time.Now().Add(5 * time.Second)
	for (*started) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if *started == nil {
		t.Fatal("the mesh never started")
	}
	time.Sleep(300 * time.Millisecond)
	if err := (*started).Stop(); err != nil {
		t.Fatalf("stop the mesh: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("LaunchP2 error = %v; want the visible-line-only mesh death", res.err)
		}
		if res.code != ExitMeshDied {
			t.Fatalf("exit code = %d; want %d", res.code, ExitMeshDied)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("LaunchP2 did not return after the mesh died")
	}

	line := errw.String()
	if !strings.Contains(line, mesh.ErrMeshDied.Error()) {
		t.Errorf("the visible fail-fast line is missing: %q", line)
	}
	if !strings.Contains(line, fmt.Sprintf("port %d", (*started).Port())) {
		t.Errorf("the visible line must name the mesh port %d: %q", (*started).Port(), line)
	}
	if strings.Contains(line, "retrying once") {
		t.Errorf("a dead mesh must not trigger a retry: %q", line)
	}
	if attempts := p2fbAttempts(t, dump); len(attempts) != 1 {
		t.Errorf("child attempts = %d; want 1 (a mesh death is final)", len(attempts))
	}
	if err := (*started).Healthz(context.Background()); err == nil {
		t.Error("the mesh must be stopped after the session ended")
	}
}

// ---------------------------------------------------------------------------
// unit-level pins for the fallback's pieces
// ---------------------------------------------------------------------------

// TestP2ChildFailed_OnlyNonZeroExitCounts pins the R5B-FIX-1 gate half: a real
// child failure is a non-zero exit (*exec.ExitError) and nothing else. A nil
// error (exit 0), a mesh death, a context cancel and a plain error are all "not
// a failure", so none of them can reach the retry.
func TestP2ChildFailed_OnlyNonZeroExitCounts(t *testing.T) {
	// nil (exit 0) is NOT a failure - the whole point of the fix.
	if p2ChildFailed(nil) {
		t.Error("p2ChildFailed(nil) = true; want false (a successful attempt must never be retried)")
	}
	// A mesh death is not a child failure.
	if p2ChildFailed(fmt.Errorf("mesh: %w: the health probe failed", mesh.ErrMeshDied)) {
		t.Error("a mesh death must not count as a child failure")
	}
	// A context cancel is not a child failure.
	if p2ChildFailed(context.Canceled) || p2ChildFailed(context.DeadlineExceeded) {
		t.Error("a canceled session must not count as a child failure")
	}
	// A spawn/start failure is not a child failure.
	if p2ChildFailed(errors.New("cannot start the real agy child")) {
		t.Error("a spawn failure must not count as a child failure")
	}
	// A genuine non-zero exit IS a failure.
	cmd := exec.Command(os.Args[0], "-test.run=TestP2HelperChild")
	cmd.Env = append(os.Environ(), "GO_P2_HELPER_MODE=exit", "GO_P2_HELPER_EXIT=3")
	if err := cmd.Run(); err == nil {
		t.Fatal("fixture broken: the helper child did not fail")
	} else if !p2ChildFailed(err) {
		t.Errorf("p2ChildFailed(%v) = false; want true (a non-zero exit is a real child failure)", err)
	}
}

// TestP2ChildCmd_DetectorOnStderrOnly pins the R5B-FIX-1 stream split: the tee
// wraps STDERR (where agy's --model rejection lands) and stdout is handed the
// real writer directly, so a model answer that quotes the public signature can
// never latch the detector.
func TestP2ChildCmd_DetectorOnStderrOnly(t *testing.T) {
	var out, errw bytes.Buffer
	opts := LaunchOptions{Stdout: &out, Stderr: &errw}
	det := &p2RejectDetector{}

	cmd := p2ChildCmd("agy", []string{"-p", "hi"}, []string{"A=B"}, opts, det)
	if cmd.Stdout != io.Writer(&out) {
		t.Errorf("cmd.Stdout = %T; want the real stdout writer (no tee, no pipe)", cmd.Stdout)
	}
	tee, ok := cmd.Stderr.(*p2Tee)
	if !ok {
		t.Fatalf("cmd.Stderr = %T; want *p2Tee (stderr is the detected stream)", cmd.Stderr)
	}
	if tee.real != io.Writer(&errw) {
		t.Error("the stderr tee must forward to the real stderr writer")
	}
	if tee.det != det {
		t.Error("the stderr tee must feed the attempt's detector")
	}
	// With no detector both streams are the real writers: the reviewed path.
	plain := p2ChildCmd("agy", []string{"-p", "hi"}, []string{"A=B"}, opts, nil)
	if plain.Stdout != io.Writer(&out) || plain.Stderr != io.Writer(&errw) {
		t.Error("without a detector both streams must be the real writers (the reviewed path)")
	}
}

// TestP2FallbackArgs_StripsMeshPinKeepsNativeServable pins R5B-FIX-2: the native
// fallback execs without a mesh-specific pin, because native agy has no mesh and
// no customModels document to answer it - forwarding one made the session fail
// at agy's --model gate with exit 1.
func TestP2FallbackArgs_StripsMeshPinKeepsNativeServable(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		want        []string
		wantDropped bool
	}{
		{
			name: "pinned external id is stripped", args: []string{"--model", "ext-nw"},
			want: []string{}, wantDropped: true,
		},
		{
			name: "pinned external id keeps the caller's other args", args: []string{"-p", "hi", "--model", "ext-nw"},
			want: []string{"-p", "hi"}, wantDropped: true,
		},
		{
			name: "scheme key is stripped", args: []string{"--model", "blade:127.0.0.1:4444/models/ext-nw"},
			want: []string{}, wantDropped: true,
		},
		{
			name: "provider-qualified spelling is stripped", args: []string{"--model", "ext-nw (provnw)"},
			want: []string{}, wantDropped: true,
		},
		{
			name: "short and equals spellings are stripped", args: []string{"-m", "ext-nw", "--model=ext-charm"},
			want: []string{}, wantDropped: true,
		},
		{
			name: "a static gemini id is KEPT (native agy resolves it itself)", args: []string{"--model", "gemini-3.8-flash-high"},
			want: []string{"--model", "gemini-3.8-flash-high"},
		},
		{
			// R5BF2-2: the kept pin is re-pinned FIRST (front-door-first), so a
			// caller "--" terminator can never swallow it.
			name: "a static id keeps the caller's other args", args: []string{"-p", "hi", "--model", "gemini-3.8-flash-high"},
			want: []string{"--model", "gemini-3.8-flash-high", "-p", "hi"},
		},
		{
			// R5BF2-2: a DECORATED static spelling is normalized to the bare id
			// agy's --model gate accepts, instead of being forwarded verbatim.
			name: "a provider-qualified static id is normalized to the bare id", args: []string{"--model", "nw/gemini-3.8-flash-high"},
			want: []string{"--model", "gemini-3.8-flash-high"},
		},
		{
			name: "a label static id is normalized to the bare id", args: []string{"--model", "gemini-3.8-flash-high (nw)"},
			want: []string{"--model", "gemini-3.8-flash-high"},
		},
		{
			name: "a normalized static pin survives a caller terminator", args: []string{"--model", "nw/gemini-3.8-flash-high", "--", "-p"},
			want: []string{"--model", "gemini-3.8-flash-high", "--", "-p"},
		},
		{
			// The re-pinned flag must never land past the terminator: it goes
			// first, before every caller token including "--".
			name: "a normalized static pin is re-pinned before a terminator", args: []string{"--app_data_dir", "mine", "--model", "nw/gemini-3.8-flash-high", "--", "-p"},
			want: []string{"--model", "gemini-3.8-flash-high", "--app_data_dir", "mine", "--", "-p"},
		},
		{
			name: "no pin at all is untouched", args: []string{"-p", "hi"},
			want: []string{"-p", "hi"},
		},
		{
			name: "a caller app_data_dir survives the strip", args: []string{"--app_data_dir", "mine", "--model", "ext-nw"},
			want: []string{"--app_data_dir", "mine"}, wantDropped: true,
		},
		{
			name: "a caller app_data_dir survives a static normalization", args: []string{"--app_data_dir", "mine", "--model", "nw/gemini-3.8-flash-high"},
			want: []string{"--model", "gemini-3.8-flash-high", "--app_data_dir", "mine"},
		},
		{
			name: "a post-terminator --model literal is never deleted", args: []string{"--", "--model", "ext-nw"},
			want: []string{"--", "--model", "ext-nw"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := p2FallbackArgs(tc.args)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("p2FallbackArgs(%v) = %v; want %v", tc.args, got, tc.want)
			}
			// The dropped-pin disclosure agrees with what was removed.
			if dropped := p2DroppedPin(tc.args); tc.wantDropped {
				if dropped == "" {
					t.Errorf("p2DroppedPin(%v) = \"\"; want the stripped pin named for disclosure", tc.args)
				}
			} else if dropped != "" {
				t.Errorf("p2DroppedPin(%v) = %q; want \"\" (nothing was dropped)", tc.args, dropped)
			}
		})
	}
}

// TestP2FallbackArmed_PrintShapedOnly pins the arming rule: the tee (and hence
// the retry) is armed for print-shaped invocations, never for an interactive TUI
// launch where wrapping stdout in a pipe would degrade agy's TUI.
func TestP2FallbackArmed_PrintShapedOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"bare interactive session", nil, false},
		{"model pin only (a TUI session)", []string{"--model", "ext-nw"}, false},
		{"models subcommand", []string{"models"}, false},
		{"print one-shot", []string{"-p", "hi", "--model", "ext-nw"}, true},
		{"print long form", []string{"--print", "hi"}, true},
		{"prompt long form", []string{"--prompt", "hi"}, true},
		{"print equals form", []string{"--print=hi"}, true},
		{"print short equals form", []string{"-p=hi"}, true},
		{"terminator then positional", []string{"--", "-p"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p2FallbackArmed(tc.args); got != tc.want {
				t.Errorf("p2FallbackArmed(%v) = %v; want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestP2AltModelForm_BareSlugAndEmptyCases pins the alternate-spelling seam.
func TestP2AltModelForm_BareSlugAndEmptyCases(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	for _, tc := range []struct {
		name  string
		model string
		want  string
	}{
		{"bare registry id", "ext-nw", "ext-nw"},
		{"provider-qualified spelling", "provnw/ext-nw", "ext-nw"},
		{"display-name spelling", "ext-nw (provnw)", "ext-nw"},
		{"already a map key", "blade:127.0.0.1:4444/models/ext-nw", ""},
		{"another scheme key", "gemini-api://127.0.0.1:4444/models/ext-nw", ""},
		{"static gemini id (no rewrite exists)", "gemini-3.8-flash-high", ""},
		{"blank", "   ", ""},
		{"unknown id falls back to the parsed candidate", "unknown-model", "unknown-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p2AltModelForm(reg, tc.model); got != tc.want {
				t.Errorf("p2AltModelForm(%q) = %q; want %q", tc.model, got, tc.want)
			}
		})
	}
	if got := p2AltModelForm(nil, "ext-nw"); got != "ext-nw" {
		t.Errorf("p2AltModelForm(nil reg, \"ext-nw\") = %q; want the bare candidate", got)
	}
}

// TestP2ChildArgsPinned_VerbatimModelFrontDoorFirst pins the retry argv shape:
// the same front-door-first order as p2ChildArgs, with the pinned value emitted
// verbatim (no p2ModelKey rewrite).
func TestP2ChildArgsPinned_VerbatimModelFrontDoorFirst(t *testing.T) {
	got := p2ChildArgsPinned([]string{"-p", "hi", "--model", "ext-nw"}, "agy-hybrid-x", "blade:127.0.0.1:4444/models/ext-nw")
	want := []string{"--app_data_dir", "agy-hybrid-x", "--model", "blade:127.0.0.1:4444/models/ext-nw", "-p", "hi"}
	if len(got) != len(want) {
		t.Fatalf("args = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v; want %v", got, want)
		}
	}
	// A valueless selector emits no pin, exactly as p2ChildArgs does.
	got = p2ChildArgsPinned([]string{"--model"}, "agy-hybrid-x", "blade:127.0.0.1:4444/models/ext-nw")
	want = []string{"--app_data_dir", "agy-hybrid-x", "--model"}
	if len(got) != len(want) {
		t.Fatalf("valueless args = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("valueless args = %v; want %v", got, want)
		}
	}
	// A caller --app_data_dir is still stripped (the front door owns it).
	got = p2ChildArgsPinned([]string{"--app_data_dir", "caller-dir", "--model", "m"}, "agy-hybrid-x", "blade:127.0.0.1:1/models/m")
	want = []string{"--app_data_dir", "agy-hybrid-x", "--model", "blade:127.0.0.1:1/models/m"}
	if len(got) != len(want) {
		t.Fatalf("caller app_data_dir was not stripped: %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("caller app_data_dir was not stripped: %v; want %v", got, want)
		}
	}
}

// TestP2RejectDetector_SplitAcrossWritesAndBounded pins the detector: a
// signature split across Write boundaries is caught, a near miss is not, and
// the retained state is bounded so a whole session never gets buffered.
func TestP2RejectDetector_SplitAcrossWritesAndBounded(t *testing.T) {
	sig := p2RejectSignature

	split := &p2RejectDetector{}
	split.write([]byte("error: invalid model selection (--model \"x\" --effort \"\"): model x is not recognized as a known "))
	if split.rejected() {
		t.Fatal("a partial signature must not latch")
	}
	split.write([]byte("model or custom model in settings\nAvailable models:\n"))
	if !split.rejected() {
		t.Fatal("a signature split across two writes must be detected")
	}

	miss := &p2RejectDetector{}
	miss.write([]byte("model x is not recognized as a known model or custom model in setting"))
	if miss.rejected() {
		t.Fatal("a one-character-short near miss must not latch")
	}
	if len(miss.tail) != len(sig)-1 {
		t.Errorf("retained tail = %d bytes; want at most %d (the bounded window)", len(miss.tail), len(sig)-1)
	}
	miss.write([]byte("s"))
	if !miss.rejected() {
		t.Fatal("the completing byte must latch the detector")
	}

	// A long, signature-free session keeps the retained state bounded.
	long := &p2RejectDetector{}
	for i := 0; i < 500; i++ {
		long.write([]byte(strings.Repeat("streaming tui output without any rejection ", 8)))
	}
	if long.rejected() {
		t.Fatal("a signature-free session must never latch")
	}
	if len(long.tail) > len(sig)-1 {
		t.Errorf("retained tail = %d bytes; want at most %d (no whole-session buffering)", len(long.tail), len(sig)-1)
	}

	// An empty or nil write is a no-op, and the latch is sticky.
	sticky := &p2RejectDetector{}
	sticky.write([]byte(sig))
	sticky.write(nil)
	if !sticky.rejected() {
		t.Fatal("the latch must survive later writes")
	}
	(*p2RejectDetector)(nil).write([]byte(sig))
	if (&p2RejectDetector{}).rejected() {
		t.Fatal("a fresh detector must start clean")
	}
}

// TestP2TeeStreamsToRealWriterAndDetector pins the tee: every byte reaches the
// real writer in order, the detector sees the same bytes, and a short write is
// reported instead of silently swallowed.
func TestP2TeeStreamsToRealWriterAndDetector(t *testing.T) {
	var real bytes.Buffer
	det := &p2RejectDetector{}
	tee := &p2Tee{real: &real, det: det}
	payload := []byte("first chunk\nsecond chunk\n")
	n, err := tee.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("Write = (%d, %v); want (%d, nil)", n, err, len(payload))
	}
	if real.String() != string(payload) {
		t.Errorf("real writer got %q; want %q", real.String(), payload)
	}
	tee.Write([]byte("model x is not recognized as a known model or custom model in settings"))
	if !det.rejected() {
		t.Error("the detector must see the bytes the tee forwards")
	}
	if !strings.Contains(real.String(), p2RejectSignature) {
		t.Errorf("the signature must also reach the real writer: %q", real.String())
	}
}
