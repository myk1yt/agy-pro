package codework

// Tests for engine-owned verification. Exit codes are exercised through a
// re-exec of this test binary (TestVerifyHelperProcess) so a pass, a failure and
// a timeout are real child processes, not mocks; the helper's parameters travel
// in argv because the sanitized environment deliberately drops a sentinel env
// var (which is itself pinned below).

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperArgPrefix marks a helper-process invocation in argv.
const helperArgPrefix = "codework-helper="

// helperArgv builds the argv that re-runs this test binary as a helper.
func helperArgv(t *testing.T, spec string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve the test binary: %v", err)
	}
	return []string{exe, "-test.run=^TestVerifyHelperProcess$", helperArgPrefix + spec}
}

// TestVerifyHelperProcess is the child side: it does nothing unless argv carries
// a helper spec.
func TestVerifyHelperProcess(t *testing.T) {
	spec := ""
	for _, a := range os.Args {
		if strings.HasPrefix(a, helperArgPrefix) {
			spec = strings.TrimPrefix(a, helperArgPrefix)
		}
	}
	if spec == "" {
		t.Skip("not a helper invocation")
	}
	code := 0
	for _, part := range strings.Split(spec, ";") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "exit":
			n, err := strconv.Atoi(value)
			if err != nil {
				os.Exit(90)
			}
			code = n
		case "sleep":
			d, err := time.ParseDuration(value)
			if err != nil {
				os.Exit(91)
			}
			time.Sleep(d)
		case "print":
			os.Stdout.WriteString(value + "\n")
		case "pad":
			// Emit n filler bytes, so an output-volume test does not need a
			// command line the OS would refuse.
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				os.Exit(92)
			}
			os.Stdout.WriteString(strings.Repeat("x", n))
		case "stderr":
			os.Stderr.WriteString(value + "\n")
		case "probe":
			// Report whether an env var survived the allow-list filter.
			if v := os.Getenv(value); v != "" {
				os.Stdout.WriteString("ENV_VISIBLE=" + v + "\n")
			} else {
				os.Stdout.WriteString("ENV_HIDDEN=" + value + "\n")
			}
		}
	}
	os.Exit(code)
}

func TestRunVerifyPass(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "verify.log")
	argv := helperArgv(t, "exit=0;print=verification ran")

	res, err := RunVerify(context.Background(), root, argv, 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if !res.Passed || res.ExitCode != 0 {
		t.Fatalf("Passed=%v ExitCode=%d, want true/0", res.Passed, res.ExitCode)
	}
	if !strings.Contains(res.OutputTail, "verification ran") {
		t.Errorf("OutputTail = %q, want the child's stdout", res.OutputTail)
	}
	if res.LogPath != logPath {
		t.Errorf("LogPath = %q, want %q", res.LogPath, logPath)
	}
	if res.DurationMS < 0 {
		t.Errorf("DurationMS = %d", res.DurationMS)
	}
	if len(res.Argv) != len(argv) || res.Argv[0] != argv[0] {
		t.Errorf("Argv = %v, want %v", res.Argv, argv)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(log), "verification ran") {
		t.Errorf("log does not hold the combined output: %q", log)
	}
}

func TestRunVerifyNonZeroExitIsAResult(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "verify.log")
	res, err := RunVerify(context.Background(), t.TempDir(), helperArgv(t, "exit=3;stderr=compile error: undefined: x"), 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("a non-zero exit must be a result, not an error: %v", err)
	}
	if res.Passed {
		t.Fatal("Passed = true for a failing verifier")
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.OutputTail, "compile error") {
		t.Errorf("OutputTail lost the stderr text: %q", res.OutputTail)
	}
}

func TestRunVerifySpawnFailureIsAnError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "verify.log")
	res, err := RunVerify(context.Background(), t.TempDir(), []string{"codework-no-such-verifier-xyz"}, 5*time.Second, logPath)
	if err == nil {
		t.Fatalf("a spawn failure must be an error, got result %+v", res)
	}
	if !strings.Contains(err.Error(), "cannot run") {
		t.Errorf("error = %v, want it to name the spawn failure", err)
	}
	if res.Passed {
		t.Error("Passed = true for a command that never started")
	}
	if res.LogPath != "" {
		t.Errorf("LogPath = %q, want empty: nothing ran, so no log exists", res.LogPath)
	}
	if _, statErr := os.Stat(logPath); statErr == nil {
		t.Errorf("a log file was written for a command that never started")
	}
}

func TestRunVerifyTimeoutIsAFailedResult(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "verify.log")
	start := time.Now()
	res, err := RunVerify(context.Background(), t.TempDir(), helperArgv(t, "sleep=30s;exit=0"), 250*time.Millisecond, logPath)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a timeout must be a result, not an error: %v", err)
	}
	if res.Passed {
		t.Fatal("Passed = true for a timed-out verifier")
	}
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1 for a killed verifier", res.ExitCode)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("RunVerify waited %s on a 250ms deadline", elapsed)
	}
	if !strings.Contains(res.OutputTail, verifyTimeoutMarker) {
		t.Errorf("OutputTail does not announce the timeout: %q", res.OutputTail)
	}
	log, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(log), verifyTimeoutMarker) {
		t.Errorf("log does not announce the timeout: %q", log)
	}
}

func TestRunVerifyRefusesBadInput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "verify.log")
	if _, err := RunVerify(context.Background(), t.TempDir(), nil, time.Second, logPath); err == nil {
		t.Error("RunVerify accepted an empty argv")
	}
	if _, err := RunVerify(context.Background(), t.TempDir(), []string{"  "}, time.Second, logPath); err == nil {
		t.Error("RunVerify accepted a blank argv[0]")
	}
	if _, err := RunVerify(context.Background(), t.TempDir(), []string{"go"}, time.Second, ""); err == nil {
		t.Error("RunVerify accepted an empty log path")
	}
	if _, err := RunVerify(context.Background(), "  ", []string{"go"}, time.Second, logPath); err == nil {
		t.Error("RunVerify accepted an empty root")
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := RunVerify(context.Background(), missing, []string{"go"}, time.Second, logPath)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("RunVerify did not refuse a missing root: %v", err)
	}
	file := filepath.Join(t.TempDir(), "a.txt")
	if werr := os.WriteFile(file, []byte("x"), 0o600); werr != nil {
		t.Fatalf("write fixture: %v", werr)
	}
	if _, err := RunVerify(context.Background(), file, []string{"go"}, time.Second, logPath); err == nil {
		t.Error("RunVerify accepted a root that is a file")
	}
}

func TestRunVerifyTailIsBoundedAndRedacted(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "verify.log")
	const secret = "tok-abcdefghijklmnopqrst"
	// 6000 filler bytes then the secret: the tail must be the END and must
	// still be redacted after truncation.
	res, err := RunVerify(context.Background(), t.TempDir(),
		helperArgv(t, "print="+strings.Repeat("f", 6000)+";print=leaked="+secret), 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if len(res.OutputTail) > verifyTailBytes+128 {
		t.Fatalf("OutputTail is %d bytes, want <= %d", len(res.OutputTail), verifyTailBytes)
	}
	if strings.Contains(res.OutputTail, secret) {
		t.Fatalf("OutputTail leaks the credential pattern: %q", res.OutputTail)
	}
	if !strings.Contains(res.OutputTail, "[REDACTED]") {
		t.Errorf("OutputTail carries no redaction marker: %q", res.OutputTail)
	}
	if !strings.Contains(res.OutputTail, "leaked=") {
		t.Errorf("OutputTail is not the END of the log: %q", res.OutputTail)
	}
	log, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if len(log) <= verifyTailBytes {
		t.Errorf("log is %d bytes; the FULL output must be written, not the tail", len(log))
	}
}

func TestRunVerifyEnvironmentIsSanitized(t *testing.T) {
	const probe = "CODEWORK_VERIFY_PROBE_TOKEN"
	const value = "sk-shouldneverreachachild0123456789"
	t.Setenv(probe, value)

	logPath := filepath.Join(t.TempDir(), "verify.log")
	res, err := RunVerify(context.Background(), t.TempDir(),
		helperArgv(t, "probe="+probe+";probe=PATH;exit=0"), 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if !strings.Contains(res.OutputTail, "ENV_HIDDEN="+probe) {
		t.Fatalf("the credential env var reached the verifier: %q", res.OutputTail)
	}
	if strings.Contains(res.OutputTail, value) {
		t.Fatalf("the verifier printed the credential value: %q", res.OutputTail)
	}
	if !strings.Contains(res.OutputTail, "ENV_VISIBLE=") {
		t.Fatalf("the allow-listed PATH did not reach the verifier: %q", res.OutputTail)
	}
}

// TestRunDetectedVerifyMergesTheOfflineToolchainEnv is the R3-6 pin on the
// detected half: an engine-chosen verifier receives GOPROXY=off, GOTOOLCHAIN=local
// and CGO_ENABLED=0, so a seat-written go.mod/go.sum cannot make it fetch a
// module, download a toolchain or build cgo at engine trust.
func TestRunDetectedVerifyMergesTheOfflineToolchainEnv(t *testing.T) {
	want := "GOPROXY=off,GOTOOLCHAIN=local,CGO_ENABLED=0"
	if got := strings.Join(detectedVerifyEnv, ","); got != want {
		t.Fatalf("detectedVerifyEnv = %q, want %q (the agreed offline toolchain set)", got, want)
	}
	logPath := filepath.Join(t.TempDir(), "detected.log")
	res, err := RunDetectedVerify(context.Background(), t.TempDir(),
		helperArgv(t, "probe=GOPROXY;probe=GOTOOLCHAIN;probe=CGO_ENABLED;exit=0"), 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunDetectedVerify: %v", err)
	}
	for _, line := range []string{"ENV_VISIBLE=off", "ENV_VISIBLE=local", "ENV_VISIBLE=0"} {
		if !strings.Contains(res.OutputTail, line) {
			t.Errorf("the detected verifier did not receive %s:\n%s", line, res.OutputTail)
		}
	}
}

// TestRunVerifyExplicitNonGoAddsNoEnv is the R3-6 half that SURVIVES finding N3: an
// explicit --verify that is NOT the go tool is the operator's own command, so the
// runner adds nothing to the sanitized environment and the parent's own values for
// those names stay dropped by the allow-list. The go-toolchain arm (which does get
// the trio) is TestVerifyExplicitGoArgvRunsHermetic in fix_e2_test.go.
func TestRunVerifyExplicitNonGoAddsNoEnv(t *testing.T) {
	t.Setenv("GOPROXY", "https://proxy.invalid")
	t.Setenv("GOTOOLCHAIN", "go1.99.0")
	t.Setenv("CGO_ENABLED", "1")
	logPath := filepath.Join(t.TempDir(), "explicit.log")
	res, err := RunVerify(context.Background(), t.TempDir(),
		helperArgv(t, "probe=GOPROXY;probe=GOTOOLCHAIN;probe=CGO_ENABLED;exit=0"), 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	for _, name := range []string{"GOPROXY", "GOTOOLCHAIN", "CGO_ENABLED"} {
		if !strings.Contains(res.OutputTail, "ENV_HIDDEN="+name) {
			t.Errorf("the explicit verifier saw %s, which neither the allow-list nor the engine may pass:\n%s", name, res.OutputTail)
		}
	}
	if !res.Passed {
		t.Errorf("the explicit verifier did not run to completion: %+v", res)
	}
}

// TestVerifyEnvAllowListMatchesAgenttools pins the env mirror against the tool
// side: the two allow-lists must stay identical or a verification subprocess
// would see a different environment than a seat command. Reading the authority
// source is the only mechanical check available (the list is unexported).
func TestVerifyEnvAllowListMatchesAgenttools(t *testing.T) {
	path := filepath.Join("..", "agenttools", "tools.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read authority %s: %v", path, err)
	}
	authority := literalKeys(t, string(src), "commandEnvAllowList")
	assertSameSet(t, "commandEnvAllowList", authority, verifyEnvAllowList)
}

func TestDetectVerifyGo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	scope := []string{
		"internal/c/y_test.go",
		"docs/notes.md",
		"internal/b/*.go",
		"internal/a/x.go",
		"internal/d",
	}
	// COMPILE-ONLY (S1): build + vet per package directory, in directory order,
	// and no `go test` line even though the scope names a _test.go file.
	want := [][]string{
		{"go", "build", "./internal/a"},
		{"go", "vet", "./internal/a"},
		{"go", "build", "./internal/b"},
		{"go", "vet", "./internal/b"},
		{"go", "build", "./internal/c"},
		{"go", "vet", "./internal/c"},
	}
	got := DetectVerify(root, scope)
	if len(got) != len(want) {
		t.Fatalf("DetectVerify = %v, want %v", got, want)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("cmd[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestDetectVerifyGoRootPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	got := DetectVerify(root, []string{"main.go", "*_test.go"})
	want := "go build . | go vet ."
	var gotStrings []string
	for _, c := range got {
		gotStrings = append(gotStrings, strings.Join(c, " "))
	}
	if strings.Join(gotStrings, " | ") != want {
		t.Fatalf("DetectVerify = %v, want %s", gotStrings, want)
	}
}

// TestDetectVerifySchedulesNoTestRunner is the S1 pin: detection is compile-only.
// No tree shape may make the engine schedule `go test` or `npm test`, because a
// test runner EXECUTES the code a paid seat just wrote, at engine trust.
func TestDetectVerifySchedulesNoTestRunner(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// An npm project that declares a test script: previously one
	// ["npm","test","--silent"] command, now nothing.
	manifest := `{"name":"x","scripts":{"test":"node test/run.js","build":"tsc"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	scopes := [][]string{
		{"src/index.js"},
		{"internal/x/a_test.go"},
		{"main.go", "*_test.go"},
		{"internal/x/a.go", "internal/x/a_test.go"},
	}
	for _, scope := range scopes {
		for _, argv := range DetectVerify(root, scope) {
			joined := strings.Join(argv, " ")
			if strings.Contains(joined, "test") {
				t.Errorf("DetectVerify(%v) scheduled %q; detection must not run a test runner (S1)", scope, joined)
			}
			if argv[0] == "npm" || argv[0] == "node" {
				t.Errorf("DetectVerify(%v) scheduled %q; detection must not run a package script (S1)", scope, joined)
			}
		}
	}

	// Non-vacuity: the same scan sees the tool names the source WOULD have to
	// carry to schedule them.
	src, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatalf("read verify.go: %v", err)
	}
	for _, banned := range []string{`"test", "--silent"`, `{"go", "test"`, `{"npm"`, "packageHasTestScript"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("verify.go still carries %q; detection is compile-only (S1)", banned)
		}
	}
	if !strings.Contains(string(src), `"go", "build"`) || !strings.Contains(string(src), `"go", "vet"`) {
		t.Error("verify.go does not carry the compile-only commands it is supposed to schedule")
	}
}

func TestDetectVerifyNone(t *testing.T) {
	root := t.TempDir()
	if got := DetectVerify(root, []string{"internal/a/x.go", "src/index.js"}); got != nil {
		t.Fatalf("DetectVerify on a tree with no manifest = %v, want nil", got)
	}

	// go.mod present but no scope names a .go file: still nil.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := DetectVerify(root, []string{"internal/a", "src/index.js"}); got != nil {
		t.Fatalf("DetectVerify with no .go scope = %v, want nil", got)
	}

	// A package.json - with or without a test script, parseable or not - is not a
	// verifier: the engine does not run a package script on its own (S1).
	for _, manifest := range []string{`{"scripts":{"build":"tsc"}}`, `{"scripts":{"test":"node t.js"}}`, "{not json"} {
		if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o600); err != nil {
			t.Fatalf("write package.json: %v", err)
		}
		if got := DetectVerify(root, []string{"src/index.js"}); got != nil {
			t.Fatalf("DetectVerify with package.json %s = %v, want nil", manifest, got)
		}
	}
}

func TestDetectVerifySkipsGlobDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := DetectVerify(root, []string{"src/**/*.go"}); got != nil {
		t.Fatalf("DetectVerify invented a package target from a glob directory: %v", got)
	}
}

func TestDetectVerifyIsDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	scope := []string{"z/z.go", "a/a.go", "m/m_test.go", "b/b.go"}
	first := DetectVerify(root, scope)
	for i := 0; i < 5; i++ {
		again := DetectVerify(root, scope)
		if strings.Join(flatten(again), ";") != strings.Join(flatten(first), ";") {
			t.Fatalf("DetectVerify is not deterministic:\n%v\n%v", first, again)
		}
	}
	if got := flatten(first); got[0] != "go build ./a" {
		t.Fatalf("output is not sorted by directory: %v", got)
	}
}

func flatten(cmds [][]string) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// TestRunVerifyRunsTheRealGoToolchain is the end-to-end check: the engine-side
// env allow-list and cwd handling must be enough to actually build this package
// with the Go toolchain on the host. Skipped in -short mode (it compiles).
func TestRunVerifyRunsTheRealGoToolchain(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the package; skipped in -short")
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Skipf("module root not found (%v)", err)
	}
	logPath := filepath.Join(t.TempDir(), "go-build.log")

	res, err := RunVerify(context.Background(), moduleRoot, []string{"go", "build", "./internal/codework"}, 300*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify(go build): %v", err)
	}
	if !res.Passed {
		log, _ := os.ReadFile(logPath)
		t.Fatalf("go build failed under the sanitized env (exit %d):\n%s", res.ExitCode, log)
	}

	// DetectVerify on the real tree must produce the compile-only pair for that
	// package (build then vet, no test runner: S1).
	got := DetectVerify(moduleRoot, []string{"internal/codework/plan.go"})
	want := []string{"go build ./internal/codework", "go vet ./internal/codework"}
	if len(got) != len(want) {
		t.Fatalf("DetectVerify on the real tree = %v, want %v", flatten(got), want)
	}
	for i := range want {
		if strings.Join(got[i], " ") != want[i] {
			t.Errorf("DetectVerify[%d] = %v, want %s", i, got[i], want[i])
		}
	}

	// And the DETECTED half must still work under the offline toolchain env (R3-6):
	// GOPROXY=off would fail a module the cache cannot satisfy, so this arm is the
	// proof that the engine's own default verifier is not broken by the pin. It
	// compiles, hence the -short skip above covers it too.
	detectedLog := filepath.Join(t.TempDir(), "go-build-detected.log")
	dres, derr := RunDetectedVerify(context.Background(), moduleRoot, got[0], 300*time.Second, detectedLog)
	if derr != nil {
		t.Fatalf("RunDetectedVerify(go build): %v", derr)
	}
	if !dres.Passed {
		log, _ := os.ReadFile(detectedLog)
		t.Fatalf("go build failed under the offline toolchain env (exit %d):\n%s", dres.ExitCode, log)
	}
}

// TestRunVerifyStreamsABoundedTail is the S1e pin: the full output reaches the
// log while only a rolling window stays in memory (verifyStreamTailBytes), and
// the returned tail is the END of it, redacted and bounded. The memory bound is
// a constant here rather than a heap measurement; what this test proves
// mechanically is that a 300KiB verifier run logs 300KiB and still returns a
// verifyTailBytes-sized excerpt.
func TestRunVerifyStreamsABoundedTail(t *testing.T) {
	if verifyStreamTailBytes != 64*1024 {
		t.Fatalf("verifyStreamTailBytes = %d, want 64KiB (the agreed rolling window)", verifyStreamTailBytes)
	}
	logPath := filepath.Join(t.TempDir(), "verify.log")
	// Three 100KiB bursts: well past the rolling window, so a whole-output
	// buffer would grow to 300KiB.
	res, err := RunVerify(context.Background(), t.TempDir(),
		helperArgv(t, "pad=102400;pad=102400;pad=102400;print=FINAL-LINE"), 60*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	log, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if len(log) < 300*1024 {
		t.Fatalf("log is %d bytes; the FULL output must be streamed to the log, not the window", len(log))
	}
	if !strings.Contains(string(log), "FINAL-LINE") {
		t.Error("the log does not carry the end of the output")
	}
	if len(res.OutputTail) > verifyTailBytes+128 {
		t.Errorf("OutputTail is %d bytes, want <= %d", len(res.OutputTail), verifyTailBytes)
	}
	if !strings.Contains(res.OutputTail, "FINAL-LINE") {
		t.Errorf("OutputTail is not the END of the log: %q", res.OutputTail)
	}
}

// TestRunVerifyWritesAnEmptyLogForASilentVerifier pins the lazy log open: a
// verifier that exits 0 with no output still leaves the log the result points at
// (the streaming writer creates it on finish, not on the first byte).
func TestRunVerifyWritesAnEmptyLogForASilentVerifier(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "silent.log")
	res, err := RunVerify(context.Background(), t.TempDir(), helperArgv(t, "exit=0"), 30*time.Second, logPath)
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if !res.Passed {
		t.Fatalf("Passed = false for a silent passing verifier: %+v", res)
	}
	if res.LogPath != logPath {
		t.Errorf("LogPath = %q, want %q", res.LogPath, logPath)
	}
	if _, statErr := os.Stat(logPath); statErr != nil {
		t.Errorf("no log for a verifier that ran: %v", statErr)
	}
}

// TestBoundedRedactKeepsRuneBoundaries pins the UTF-8 boundary of the bounding
// helper (a split rune would make the JSON body invalid and the plan file
// unreadable).
func TestBoundedRedactKeepsRuneBoundaries(t *testing.T) {
	text := strings.Repeat("가", 3000) // 3 bytes each
	got := boundedRedact(text, 100)
	if !strings.HasPrefix(got, "[... ") {
		t.Fatalf("boundedRedact dropped the omission marker: %q", got)
	}
	body := got[strings.IndexByte(got, '\n')+1:]
	if !regexp.MustCompile(`^가+$`).MatchString(body) {
		t.Fatalf("boundedRedact split a rune: %q", body)
	}
	if len(body) > 100 {
		t.Fatalf("boundedRedact returned %d bytes, want <= 100", len(body))
	}
}
