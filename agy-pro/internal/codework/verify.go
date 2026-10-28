package codework

// Engine-owned verification: `agy pro code` runs the verifier itself, not
// through a seat, so the pass/fail verdict is one the engine can print honestly
// and a repair plan can be built from.
//
// Two halves:
//
//   - DetectVerify picks the commands to run from the tree and the plan scopes
//     alone: no model input, no seat suggestion, no network. It is COMPILE-ONLY
//     by construction (finding S1): the commands it schedules are `go build` and
//     `go vet` over the package directories the plan scopes name, so nothing it
//     picks EXECUTES tree-authored code. A tree that offers no verifier yields
//     nil, which the caller reports as "no verifier detected" instead of
//     inventing a pass.
//   - RunVerify executes ONE argv under the engine's own trust: the full output
//     streams to logPath, only a rolling window of it stays in memory, a bounded
//     redacted tail rides back in the result, and a non-zero exit is a RESULT
//     (Passed=false), never an error. Only a genuine spawn failure is an error.
//
// Trust boundary. A seat's run_command is clamped to 30s by the tool layer
// (agenttools/runner.go:213-217 clampToolTimeout) because it runs a MODEL-chosen
// command in a loop; this runner is called by engine code with a
// code-chosen argv, so its default is 600s (defaultVerifyTimeout). The two are
// deliberately different limits for different trust levels.
//
// What the engine still cannot make safe by choosing the argv: the FILES the
// verifier reads and compiles were written by a provider seat in the same phase.
// That is why detection stopped scheduling test runners (a test binary executes
// `init`, and an npm test script is an arbitrary program) and why phases.go
// echoes verifyTrustNote before the run: an explicit --verify is the operator's
// own argv, and the disclosure says plainly that it runs over the working tree.
//
// Budget. Per command the deadline stays defaultVerifyTimeout; across a whole run
// the verifier gets maxVerifyTotalSec (verifyBudget below), and a command that
// would start after the budget is spent is recorded as skipped-budget with
// Passed=false - a skipped verifier is never a pass.
//
// Environment. The child env mirrors agenttools' tool-side allow-list
// (tools.go:670-704 commandEnvAllowList + sanitizedCommandEnv) so a verification
// subprocess cannot read provider credentials either. The list itself is
// UNEXPORTED, so it is mirrored here field-for-field and pinned by
// TestVerifyEnvAllowListMatchesAgenttools in verify_test.go.
//
// Environment split (finding R3-6, widened by N3). The allow-list answers "which
// names may the child see"; it does not answer what the toolchain may DO. That is
// decided by what the command IS, not by who named it:
//
//   - a verifier that runs the GO TOOLCHAIN - the engine's own detected commands
//     and an operator's `--verify "go test ./..."` alike - gets the hermetic trio
//     merged on top of the sanitized env (verifyChildEnv -> detectedVerifyEnv:
//     GOPROXY=off, GOTOOLCHAIN=local, CGO_ENABLED=0). A seat-authored
//     go.mod/go.sum otherwise makes the build fetch an attacker-named module,
//     GOTOOLCHAIN makes it download a toolchain, and cgo makes it spawn a C
//     compiler - and every one of those reads only the tree and the argv, never the
//     provenance. N3 is exactly this: the first cut hardened only the detected
//     path, so the ordinary `--verify "go test ./..."` reopened the channel.
//   - any OTHER explicit verifier (`--verify "python x.py"`) runs with the
//     sanitized allow-list and nothing added: the engine does not rewrite the
//     environment of a command whose toolchain it knows nothing about. An operator
//     who needs a non-hermetic go run names a wrapper command instead of the bare
//     tool.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultVerifyTimeout is the engine-side wall clock for one verification
	// run: a cold `go build ./...` on this workspace takes tens of seconds, and
	// the engine is not a model loop, so the 30s seat clamp does not apply. 600s
	// bounds a hung verifier without truncating a real one.
	defaultVerifyTimeout = 600 * time.Second

	// maxVerifyTotalSec is the RUN-TOTAL verification wall budget in seconds
	// (finding S1): 1800s across every command of every attempt, including the
	// re-verification after each repair. Per command the deadline above stays;
	// this is the ceiling their sum may reach, so a multi-command multi-repair
	// run cannot chain unbounded execution at engine trust. When it is spent, the
	// remaining commands are recorded as verifyBudgetMarker and the run does NOT
	// pass (fail-closed: a skipped verifier is not a pass).
	maxVerifyTotalSec = 1800

	// verifyTailBytes bounds VerifyResult.OutputTail. The tail is what a repair
	// body actually carries (plan.go repairTailBytes is the smaller of the
	// two), and a bounded field is what keeps a runaway log out of the report.
	verifyTailBytes = 4096

	// verifyStreamTailBytes is the rolling window of child output kept in
	// memory (64KiB). The FULL output still reaches the log file; only the
	// in-process buffer is bounded, so a compiler error wall cannot grow the
	// engine's heap without limit.
	verifyStreamTailBytes = 64 * 1024

	// verifyLogPerm is the mode of the verification log. Compiler output can
	// echo values from the tree it read (a fixture, a dotenv), so the log is
	// owner-only.
	verifyLogPerm = 0o600

	// verifyTimeoutMarker is appended to the log and tail when the run was
	// killed by the deadline instead of finishing. VerifyResult has no
	// TimedOut field, so this line plus ExitCode -1 is how a reader tells a
	// timeout from a verifier that exited with a real status.
	verifyTimeoutMarker = "[codework] verify timed out"

	// verifyBudgetMarker is the OutputTail of a command that did not run because
	// maxVerifyTotalSec was already spent. ExitCode is exitNotRun (-1) and
	// Passed is false, so the row reads as a failure in the report: skipping a
	// verifier must never look like passing one.
	verifyBudgetMarker = "[codework] verify skipped-budget: the run's total verification budget is spent, so this command did not run; a skipped verifier is not a pass"

	goModFileName = "go.mod"
)

// detectedVerifyEnv is the hermetic toolchain trio a verifier gets. It is applied
// to every verifier the ENGINE chose (DetectVerify), and - finding N3, fix E2 - to
// every verifier whose argv[0] is the go tool, whatever its provenance, including
// an operator-named `--verify "go test ./..."`.
//
// Why these three: a verifier runs at engine trust over a tree a provider seat
// wrote in the same phase, and the toolchain reads that tree as configuration. A
// seat-authored go.mod/go.sum is a dependency list the build would fetch (an
// attacker-named module, run through its own init), GOTOOLCHAIN is a download
// instruction, and cgo is a build-time exec of a compiler the tree could have
// planted on PATH. Detection is compile-only, but `go test`/`go build` on a tree a
// seat wrote reaches the same toolchain, so the argv's PROVENANCE cannot decide
// whether the toolchain may fetch, download or exec: the toolchain itself decides
// that, and the engine closes it for every go invocation.
//
// Why they are APPENDED rather than filtered: the allow-list is a default-deny
// name filter (credentials), it cannot express "this name, this value". Go's
// exec dedupe keeps the LAST occurrence of a name, and none of these names is
// allow-listed, so appending wins over anything the parent process carried while
// leaving the credential filter intact. That is also why an operator's own
// GOPROXY/GOTOOLCHAIN/CGO_ENABLED are NOT passed through: the engine's value is
// the one in force, and the echo says so before the consent prompt. An operator who
// truly needs the network or cgo names a shell wrapper as the verifier (the wrapper
// is not a go argv, so nothing is forced), instead of relying on the silent
// inheritance that used to leave the channel open.
//
// The identifier keeps its "detected" name: docs/code.md and the docs pins cite it
// by name (FIX-E3 owns the prose for the widened scope).
var detectedVerifyEnv = []string{
	"GOPROXY=off",       // no module download: a seat-written go.mod/go.sum cannot fetch its own dependency
	"GOTOOLCHAIN=local", // no toolchain download: the host toolchain only
	"CGO_ENABLED=0",     // no C compiler: the build cannot exec a compiler it found in the tree
}

// verifyChildEnv returns the extra environment a verifier child receives: the
// hermetic trio when the command is engine-chosen (unchanged R3-6 behavior) or when
// argv[0] is the go tool (finding N3), and nothing for any other explicit command.
func verifyChildEnv(argv []string, detected bool) []string {
	if detected || isGoToolchainArgv(argv) {
		return detectedVerifyEnv
	}
	return nil
}

// goExecutable is the go tool's base name. A verifier that spells it differently
// (a wrapper, a shell, an absolute path to something else) is NOT treated as the
// toolchain.
const goExecutable = "go"

// isGoToolchainArgv reports whether argv[0] names the go tool: the base name with
// any directory part removed, a trailing ".exe" dropped and the name folded.
//
// Both separators are stripped on every host on purpose: a Windows path
// ("C:\Go\bin\go.exe") must be recognized even when the code runs somewhere
// filepath.Base would not split it, and a POSIX path must be recognized on Windows.
// The fold is unconditional (executable names are case-insensitive on the host this
// door mostly runs on) and it can only ADD hermeticity: seeing "GO.EXE" as go
// hardens a command that would otherwise inherit, and the reverse mistake (treating
// a go tool as non-go) is what N3 closed.
func isGoToolchainArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	base := strings.TrimSpace(argv[0])
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(strings.ToLower(base), ".exe")
	return base == goExecutable
}

// VerifyResult is one verification run. It is a value the engine can report and
// a repair plan can be built from; it never carries an error channel of its own.
type VerifyResult struct {
	Argv       []string // the command as executed (argv[0] included)
	ExitCode   int      // the verifier's exit status; -1 when killed (see verifyTimeoutMarker)
	Passed     bool     // ExitCode == 0
	DurationMS int64    // wall clock of the run, milliseconds
	LogPath    string   // the file holding the FULL combined output
	OutputTail string   // last ~4KiB of that output, redacted
}

// RunVerify executes argv with cmd.Dir = root, streams the combined
// stdout+stderr to logPath, and returns the verdict.
//
// Result vs error:
//
//   - a non-zero exit is a RESULT: Passed=false, ExitCode set, err nil.
//   - a deadline/cancellation kill is a RESULT too: ExitCode -1, Passed=false,
//     and verifyTimeoutMarker appended to the log and the tail. A verifier that
//     hangs must still produce a repair input rather than abort the door.
//   - a spawn failure (binary missing, root unreadable) is an ERROR: nothing
//     ran, so there is no verdict to report and no log is left behind.
//   - a log write failure is an ERROR: the verdict cannot be recorded, and a
//     verdict nobody can inspect is not one the report may print.
//
// Memory: the child's output is streamed (verifyOutput) - the full log lands in
// logPath as it arrives, and only the last verifyStreamTailBytes stay in memory.
//
// root is resolved to an absolute path and must be an existing directory: an
// absent cwd would fail at spawn with a message about the process, not about
// the workspace, and the door refuses rather than report a confusing failure.
// A relative root is resolved once against the engine's working directory.
//
// timeout <= 0 selects defaultVerifyTimeout.
//
// Environment: the sanitized allow-list, plus the hermetic toolchain trio when the
// argv is the go tool (verifyChildEnv, finding N3) - see the file header's
// provenance section. RunDetectedVerify is the same runner with the trio always
// merged in; it is what an engine-chosen command goes through.
func RunVerify(ctx context.Context, root string, argv []string, timeout time.Duration, logPath string) (VerifyResult, error) {
	return runVerifyWithEnv(ctx, root, argv, timeout, logPath, verifyChildEnv(argv, false))
}

// RunDetectedVerify runs a verifier the ENGINE chose (DetectVerify): the same
// runner as RunVerify, with the hermetic trio merged on top of the sanitized
// environment. It exists as a separate function rather than a flag so the
// provenance of a command is visible at every call site and at the seam
// (phases.go RunVerifyCmd / RunDetectedVerifyCmd). It is not the only way to get
// the trio any more (finding N3: a go-toolchain argv from --verify gets it too,
// through RunVerify), but it still marks the engine's own choice.
//
// The argv is not rewritten: detection already picked a compile-only command
// (go build / go vet), and the point of this function is the environment, not
// the command.
func RunDetectedVerify(ctx context.Context, root string, argv []string, timeout time.Duration, logPath string) (VerifyResult, error) {
	return runVerifyWithEnv(ctx, root, argv, timeout, logPath, verifyChildEnv(argv, true))
}

// runVerifyWithEnv is the runner both entry points share. extraEnv is merged
// after the sanitized environment (empty for an explicit verifier that is not the
// go tool).
func runVerifyWithEnv(ctx context.Context, root string, argv []string, timeout time.Duration, logPath string, extraEnv []string) (VerifyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return VerifyResult{ExitCode: -1}, errors.New("codework: verify: empty argv (a verification command is required)")
	}
	if strings.TrimSpace(logPath) == "" {
		return VerifyResult{ExitCode: -1}, errors.New("codework: verify: empty log path (the engine records every verification run)")
	}

	rootAbs, err := resolveVerifyRoot(root)
	if err != nil {
		return VerifyResult{Argv: append([]string(nil), argv...), ExitCode: -1}, err
	}
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = rootAbs
	cmd.Env = mergeVerifyEnv(sanitizedVerifyEnv(), extraEnv)

	// Both streams point at one streaming writer: os/exec copies each on its own
	// goroutine, so the writer is serialized internally.
	out := newVerifyOutput(logPath)
	cmd.Stdout = out
	cmd.Stderr = out

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	res := VerifyResult{
		Argv:       append([]string(nil), argv...),
		ExitCode:   -1,
		DurationMS: duration.Milliseconds(),
		LogPath:    logPath,
	}

	timedOut := false
	switch {
	case runErr == nil:
		res.ExitCode = 0
	case runCtx.Err() != nil:
		// Killed by the deadline or by the caller's cancellation. Normalized to
		// -1 on every host: a killed child's own exit status is platform noise.
		timedOut = true
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else if out.Err() == nil {
			// Spawn failure (exec.Error): the process never started, so no log
			// exists and LogPath must not claim one.
			res.LogPath = ""
			return res, fmt.Errorf("codework: verify: cannot run %q: %w", argv[0], runErr)
		}
	}

	marker := ""
	if timedOut {
		marker = fmt.Sprintf("\n%s after %s\n", verifyTimeoutMarker, timeout.Round(time.Millisecond))
	}
	if writeErr := out.finish(marker); writeErr != nil {
		return res, fmt.Errorf("codework: verify: cannot write the verification log %q: %w", logPath, writeErr)
	}

	res.Passed = res.ExitCode == 0
	res.OutputTail = boundedRedact(out.Tail(), verifyTailBytes)
	return res, nil
}

// resolveVerifyRoot resolves root to an absolute path and refuses a root that
// is not an existing directory (root containment: the verifier may only run
// inside the workspace the door was pointed at, and that workspace must exist).
func resolveVerifyRoot(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", errors.New("codework: verify: empty root (the verifier needs the workspace it must run inside)")
	}
	abs, err := filepath.Abs(filepath.Clean(trimmed))
	if err != nil {
		return "", fmt.Errorf("codework: verify: cannot resolve root %q: %w", trimmed, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("codework: verify: root %q does not exist: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("codework: verify: root %q is not a directory", abs)
	}
	return abs, nil
}

// DetectVerify derives the verification commands for a plan from the tree and
// the plan scopes ONLY: same tree + same scopes => same slice, every call.
//
// COMPILE-ONLY BY CONSTRUCTION (finding S1):
//
//   - go.mod at root and at least one scope entry naming a .go file (literal or
//     glob, e.g. "internal/x/a.go", "internal/x/*.go") => ["go","build","./<dir>"]
//     then ["go","vet","./<dir>"] per distinct directory, in directory order.
//   - otherwise nil, which the caller reports as "no verifier detected".
//
// There is deliberately NO `go test` and NO `npm test` line here any more. The
// engine runs its verifier at its own trust level (the operator's user, the whole
// disk, the network) and the files under it were written by a provider seat in
// the same phase; a test runner executes what it is given (package `init`,
// example functions, an npm test script that is an arbitrary program), so
// scheduling one automatically would hand the seat's bytes engine trust. Any
// test or script verifier now requires an explicit --verify, which is the
// operator's own argv and is disclosed in the phase echo (phases.go
// verifyTrustNote). `go build` and `go vet` compile and type-check the packages
// without running the tree's own programs.
//
// Scope entries are read lexically: a directory scope ("internal/x") does not
// imply that the directory holds Go files, because deciding that would mean
// walking the tree and the answer would then depend on files the plan never
// named. A directory scope therefore contributes nothing; a .go-naming scope
// does. Entries whose directory itself carries glob metacharacters
// ("src/**/*.go") are skipped: a package path cannot be a pattern.
func DetectVerify(root string, scope []string) [][]string {
	if !fileExists(filepath.Join(root, goModFileName)) {
		return nil
	}
	dirs := map[string]bool{}
	for _, entry := range scope {
		if dir, ok := scopeGoDir(entry); ok {
			dirs[dir] = true
		}
	}
	sorted := sortedKeys(dirs)
	if len(sorted) == 0 {
		return nil
	}
	cmds := make([][]string, 0, 2*len(sorted))
	for _, dir := range sorted {
		target := goTarget(dir)
		cmds = append(cmds, []string{"go", "build", target}, []string{"go", "vet", target})
	}
	return cmds
}

// scopeGoDir extracts the package directory of a scope entry that names a Go
// file. ok is false for anything else (a non-Go entry, a malformed glob, or a
// directory carrying glob metacharacters).
func scopeGoDir(entry string) (dir string, ok bool) {
	normalized := normalizeScopeText(entry)
	if normalized == "" || !strings.HasSuffix(normalized, ".go") {
		return "", false
	}
	dir = path.Dir(normalized)
	if hasGlobMetaChars(dir) {
		return "", false
	}
	return dir, true
}

// goTarget renders a package directory as a `go` command target.
func goTarget(dir string) string {
	if dir == "" || dir == "." {
		return "."
	}
	return "./" + dir
}

// hasGlobMetaChars reports whether s carries a glob metacharacter.
func hasGlobMetaChars(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// fileExists reports whether p exists and is not a directory.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// verifyOutput streams the verifier's combined output to the log file and keeps
// only a rolling window of it in memory (finding S1's preemptive row: the
// previous body accumulated the WHOLE child output in a bytes.Buffer).
//
// The log file is opened lazily on the first write the child produces, so a
// command that never starts (spawn failure) leaves no log behind - the same
// guarantee WriteFileAtomic gave, without holding the output in memory first.
// finish() creates the (empty) log for a verifier that exits silently, so the
// artifact the report names always exists after a real run.
//
// cmd.Stdout and cmd.Stderr both point here and os/exec copies each stream on
// its own goroutine, so every method serializes on one mutex.
type verifyOutput struct {
	path string

	mu   sync.Mutex
	file *os.File
	err  error
	tail []byte
}

// newVerifyOutput returns a writer that will log to path with verifyLogPerm.
func newVerifyOutput(path string) *verifyOutput {
	return &verifyOutput{path: path}
}

// Write implements io.Writer for both child streams.
func (o *verifyOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.writeLocked(p)
	if o.err != nil {
		return 0, o.err
	}
	return len(p), nil
}

// writeLocked appends p to the log and to the rolling window. A write error is
// sticky: the verdict could not be recorded, so the caller turns it into an
// error instead of a result.
func (o *verifyOutput) writeLocked(p []byte) {
	if o.err != nil || len(p) == 0 {
		return
	}
	if o.file == nil {
		f, err := os.OpenFile(o.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, verifyLogPerm)
		if err != nil {
			o.err = err
			return
		}
		o.file = f
	}
	if _, err := o.file.Write(p); err != nil {
		o.err = err
		return
	}
	o.tail = append(o.tail, p...)
	// Trim in one step once the window doubles, so the buffer cannot grow past
	// 2*verifyStreamTailBytes and the per-write cost stays amortized constant.
	if len(o.tail) > 2*verifyStreamTailBytes {
		o.tail = append(o.tail[:0], o.tail[len(o.tail)-verifyStreamTailBytes:]...)
	}
}

// Err reports the first log-writing failure, if any.
func (o *verifyOutput) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

// Tail returns the rolling window (at most 2*verifyStreamTailBytes) for the
// caller to redact and bound.
func (o *verifyOutput) Tail() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.tail)
}

// finish writes the trailing marker (the timeout line, or ""), guarantees the
// log exists, and closes it.
func (o *verifyOutput) finish(marker string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if marker != "" {
		o.writeLocked([]byte(marker))
	}
	if o.err != nil {
		o.closeLocked()
		return o.err
	}
	if o.file == nil {
		f, err := os.OpenFile(o.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, verifyLogPerm)
		if err != nil {
			o.err = err
			return err
		}
		o.file = f
	}
	return o.closeLocked()
}

// closeLocked closes the log file and records a close failure as sticky error.
func (o *verifyOutput) closeLocked() error {
	if o.file == nil {
		return o.err
	}
	cerr := o.file.Close()
	o.file = nil
	if cerr != nil && o.err == nil {
		o.err = cerr
	}
	return o.err
}

// verifyBudget is the RUN-TOTAL verification wall budget (maxVerifyTotalSec):
// every command of every attempt is charged its wall clock, and a command that
// would start after the budget is spent is skipped with verifyBudgetMarker
// instead of running. Per-command deadlines stay defaultVerifyTimeout; this is
// the ceiling on their sum.
type verifyBudget struct {
	total time.Duration
	spent time.Duration
}

// newVerifyBudget returns the run's budget.
func newVerifyBudget() *verifyBudget {
	return &verifyBudget{total: time.Duration(maxVerifyTotalSec) * time.Second}
}

// remaining is what is left; <= 0 means spent.
func (b *verifyBudget) remaining() time.Duration { return b.total - b.spent }

// exhausted reports whether no further command may start.
func (b *verifyBudget) exhausted() bool { return b.remaining() <= 0 }

// timeout is the deadline for the NEXT command: the engine default, clamped to
// what is left of the budget, so one command cannot carry the run past it.
// A spent budget returns 0, which the caller must not turn into
// defaultVerifyTimeout (that is what exhausted() is checked for first).
func (b *verifyBudget) timeout() time.Duration {
	rem := b.remaining()
	if rem <= 0 {
		return 0
	}
	if rem < defaultVerifyTimeout {
		return rem
	}
	return defaultVerifyTimeout
}

// charge books one command's wall clock (VerifyResult.DurationMS). A zero or
// negative duration (a seam that does not measure) charges nothing.
func (b *verifyBudget) charge(ms int64) {
	if ms <= 0 {
		return
	}
	b.spent += time.Duration(ms) * time.Millisecond
}

// sortedKeys returns the map keys in sorted order (deterministic output).
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// verifyEnvAllowList mirrors agenttools.commandEnvAllowList (tools.go:670-687)
// field-for-field: the engine-side verifier gets the same default-deny
// environment as a seat command, so neither can read a provider credential.
// Kept in sync by TestVerifyEnvAllowListMatchesAgenttools in verify_test.go;
// the authority is the agenttools list.
var verifyEnvAllowList = map[string]bool{
	// process/runtime essentials
	"path": true, "pathext": true, "comspec": true,
	"systemroot": true, "systemdrive": true, "windir": true,
	"temp": true, "tmp": true, "tmpdir": true,
	"home": true, "userprofile": true, "homedrive": true, "homepath": true,
	"appdata": true, "localappdata": true,
	"username": true, "user": true, "logname": true, "userdomain": true, "userdnsdomain": true,
	"allusersprofile": true, "public": true, "programdata": true,
	"programfiles": true, "programfiles(x86)": true, "programw6432": true,
	"commonprogramfiles": true, "commonprogramfiles(x86)": true,
	"number_of_processors": true, "os": true,
	"processor_architecture": true, "processor_identifier": true,
	"processor_level": true, "processor_revision": true,
	// locale/shell basics so ordinary commands behave
	"lang": true, "lc_all": true, "lc_ctype": true, "term": true, "shell": true,
	"pwd": true, "oldpwd": true,
}

// sanitizedVerifyEnv returns the parent environment filtered down to
// verifyEnvAllowList, exactly as agenttools.sanitizedCommandEnv (tools.go:692)
// does: only allow-listed NAMES pass, so anything else - including
// *_API_KEY, AGY_LLM_GATEWAY_* and the gateway session key - is dropped by
// construction rather than by pattern matching.
func sanitizedVerifyEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if verifyEnvAllowList[strings.ToLower(kv[:eq])] {
			out = append(out, kv)
		}
	}
	return out
}

// mergeVerifyEnv appends extra to a sanitized environment. The additions come
// LAST so os/exec's own dedupe (it keeps the last definition of a name) makes
// them authoritative, and they are names the allow-list never passes through, so
// the sanitizing rule cannot contradict them. An empty extra returns base
// unchanged (an explicit verifier that is not the go tool - see verifyChildEnv).
func mergeVerifyEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}
