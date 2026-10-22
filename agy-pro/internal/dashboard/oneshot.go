package dashboard

// One-shot refresh exec (P5-2 S4).
//
// The screen's Refresh action has to make the Node side rewrite its artifacts:
// the aggregation pipeline stays Node's (session 8 pick record, mechanism
// M-B permanently rejected), so Go runs ONE bounded headless Node invocation
// and then reloads what landed on disk through the S1 loaders.
//
// Three properties are load-bearing and every one of them is a belt:
//
//  1. HEADLESS. The verb is `agy-tokens --html --no-open`. Bare
//     `agy-dashboard` injects `--open` (bin/agy-tokens.js: the isAgyDashboard
//     branch pushes '--serve' and '--open' when neither open flag is present),
//     so this package never resolves that binary by bare name for an execution
//     argv; and refuseUnsafeArgv rejects --open / --serve outright, so even a
//     mis-typed AGY_DASHBOARD_ENTRY cannot launch a browser or a server.
//  2. BOUNDED. RefreshTimeout (3s) caps one run. `--html` is a
//     write-and-return verb (src/index.js: the html branch ends with a return
//     after writeDashboardFiles), so a run that exceeds the bound is a hung
//     child, not a slow but healthy one.
//  3. SCRUBBED. The child environment is the default-deny allow-list
//     (wirecheck.FilterChildEnv): HOME / USERPROFILE / PATH / SYSTEMROOT
//     survive because a Node process cannot start without them, while every
//     provider key value in the shim environment is dropped.
//
// Discovery follows the SESSION8 pick-record default 3 ladder. A persisted
// path setting is P5-3's; nothing here writes a setting.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agypro/internal/providers"
	"agypro/internal/wirecheck"
)

const (
	// EnvDashboardEntry is the full-argv override: the documented escape hatch
	// for a tools tree that is neither on PATH nor at AGY_PRO_TOOLS_ROOT. It is
	// whitespace-split with minimal double-quote grouping (see splitArgv) and
	// names the whole command line, e.g.
	// `AGY_DASHBOARD_ENTRY=node D:/tools/agy-tools/bin/agy-tokens.js`.
	EnvDashboardEntry = "AGY_DASHBOARD_ENTRY"

	// EnvProToolsRoot points at an agy-tools checkout (the directory holding
	// bin/agy-tokens.js). It backs the `node + <root>/bin/agy-tokens.js` rung.
	EnvProToolsRoot = "AGY_PRO_TOOLS_ROOT"

	// RefreshTimeout is the one-shot bound (contract S4: <= 3s).
	RefreshTimeout = 3 * time.Second

	// refreshOutputCap bounds each captured stream. A --html run prints two
	// lines; the cap keeps a chatty or hijacked child from ballooning the
	// capture. Output past the cap is truncated and flagged, never parsed.
	refreshOutputCap = 8 << 10

	// tokensBinName / dashboardBinName are the PATH candidates, in ladder
	// order. Both are resolved through exec.LookPath only; the bare
	// `agy-dashboard` NAME is never handed to a shell as an execution argv
	// without --no-open appended (it self-injects --serve --open).
	tokensBinName    = "agy-tokens"
	dashboardBinName = "agy-dashboard"

	// tokensEntryRel is the repo-relative Node entry inside a tools root.
	tokensEntryRel = "bin/agy-tokens.js"

	// nodeBinName is the interpreter rung's executable.
	nodeBinName = "node"

	// refreshHTMLFlag / refreshNoOpenFlag are appended AFTER the entry is
	// resolved, so any resolved argv (an env override, a PATH shim, node + a
	// script) receives them. --html forces writeDashboardFiles; --no-open
	// keeps the run headless.
	refreshHTMLFlag   = "--html"
	refreshNoOpenFlag = "--no-open"
)

// Source names the discovery rung an Entry came from. It is diagnostic only
// (the screen never branches on it); a test pins each rung.
type Source string

const (
	// SourceEnvOverride is the AGY_DASHBOARD_ENTRY full-argv rung.
	SourceEnvOverride Source = "env-override"
	// SourcePath is the exec.LookPath rung (agy-tokens, then agy-dashboard).
	SourcePath Source = "path"
	// SourceNodeEntry is the `node <root>/bin/agy-tokens.js` rung.
	SourceNodeEntry Source = "node-entry"
)

// Entry is one resolved dashboard command: the argv to spawn (before the
// refresh flags are appended) plus where it came from.
type Entry struct {
	Argv   []string
	Source Source
	// Detail names the rung in one short phrase for a message line, e.g.
	// "agy-tokens on PATH" or "node + AGY_PRO_TOOLS_ROOT". It carries no
	// absolute user path unless the user set one in the environment.
	Detail string
}

// NotFoundError is the typed "no dashboard entry on this host" result. It
// names every rung that was checked, so the screen can say what is missing
// instead of "failed".
type NotFoundError struct {
	Checked []string
}

func (e *NotFoundError) Error() string {
	if len(e.Checked) == 0 {
		return "dashboard entry not found"
	}
	return "dashboard entry not found (checked: " + strings.Join(e.Checked, ", ") + ")"
}

// RefusedArgvError is the typed belt: the resolved argv carries a verb this
// package never runs (--open launches a browser, --serve starts a long-lived
// server that cannot finish inside RefreshTimeout).
type RefusedArgvError struct {
	Token  string
	Reason string
}

func (e *RefusedArgvError) Error() string {
	return "refused dashboard argv token " + e.Token + ": " + e.Reason
}

// TimeoutError is the typed deadline result. It unwraps to
// context.DeadlineExceeded so a caller can match either form.
type TimeoutError struct {
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("dashboard refresh exceeded %s", e.Timeout)
}

func (e *TimeoutError) Unwrap() error { return context.DeadlineExceeded }

// ExecError is a non-zero child exit. Detail is already masked (see
// Refresh), so it can reach a frame.
type ExecError struct {
	ExitCode int
	Detail   string
}

func (e *ExecError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("dashboard refresh exited with code %d", e.ExitCode)
	}
	return fmt.Sprintf("dashboard refresh exited with code %d: %s", e.ExitCode, e.Detail)
}

// Result is one completed refresh run.
type Result struct {
	Argv     []string
	ExitCode int
	Stdout   string
	Stderr   string
	Elapsed  time.Duration
}

// ResolveEntry walks the discovery ladder and returns the argv to refresh
// through:
//
//  1. AGY_DASHBOARD_ENTRY, a full argv override (escape hatch);
//  2. exec.LookPath("agy-tokens"), then exec.LookPath("agy-dashboard");
//  3. node + <AGY_PRO_TOOLS_ROOT>/bin/agy-tokens.js.
//
// On Windows a rung-2 hit is usually an npm .cmd shim, which os/exec cannot
// spawn directly. When node and the tools-root entry are both resolvable the
// rung-3 argv is preferred over the batch wrap: no cmd.exe quoting, and the
// same script the shim would have run. Otherwise the shim stays the argv and
// Refresh wraps it in cmd.exe /d /v:off /s /c (see batchCmd).
func ResolveEntry() (Entry, error) {
	checked := make([]string, 0, 5)

	if raw := strings.TrimSpace(os.Getenv(EnvDashboardEntry)); raw != "" {
		argv, err := splitArgv(raw)
		if err != nil {
			// A malformed override is a configuration error, not a missing
			// entry, but it is reported through the same typed path so the
			// screen has one failure shape.
			return Entry{}, &NotFoundError{Checked: []string{EnvDashboardEntry + " (" + err.Error() + ")"}}
		}
		if err := refuseUnsafeArgv(argv); err != nil {
			return Entry{}, err
		}
		return Entry{Argv: argv, Source: SourceEnvOverride, Detail: EnvDashboardEntry}, nil
	}
	checked = append(checked, EnvDashboardEntry+" unset")

	for _, name := range []string{tokensBinName, dashboardBinName} {
		path, err := exec.LookPath(name)
		if err != nil {
			checked = append(checked, name+" not on PATH")
			continue
		}
		if isBatchScript(path) {
			// An npm shim. Prefer the Node entry when it resolves, so the
			// spawn needs no cmd.exe wrap at all.
			if e, ok := nodeEntry(); ok {
				return e, nil
			}
		}
		return Entry{Argv: []string{path}, Source: SourcePath, Detail: name + " on PATH"}, nil
	}

	if e, ok := nodeEntry(); ok {
		return e, nil
	}
	checked = append(checked, nodeBinName+" not on PATH, or "+EnvProToolsRoot+"/"+tokensEntryRel+" missing")
	return Entry{}, &NotFoundError{Checked: checked}
}

// nodeEntry is the `node + <root>/bin/agy-tokens.js` rung. Both halves must
// resolve: a script without an interpreter, or an interpreter without the
// script, is not a runnable entry.
func nodeEntry() (Entry, bool) {
	node, err := exec.LookPath(nodeBinName)
	if err != nil {
		return Entry{}, false
	}
	root := strings.TrimSpace(os.Getenv(EnvProToolsRoot))
	if root == "" {
		return Entry{}, false
	}
	entry := filepath.Join(filepath.Clean(root), filepath.FromSlash(tokensEntryRel))
	info, err := os.Stat(entry)
	if err != nil || info.IsDir() {
		return Entry{}, false
	}
	return Entry{
		Argv:   []string{node, entry},
		Source: SourceNodeEntry,
		Detail: nodeBinName + " + " + EnvProToolsRoot + "/" + tokensEntryRel,
	}, true
}

// splitArgv splits an AGY_DASHBOARD_ENTRY value into argv. Whitespace
// separates tokens; a double-quoted run groups spaces into one token (no
// escape sequences, so a literal quote in an override is unsupported and
// documented as such). This is enough for a Windows path with spaces without
// pulling a shell-quoting library into a stdlib-only module.
func splitArgv(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
		open    bool // cur holds a token (possibly empty and quoted)
	)
	flush := func() {
		if open {
			out = append(out, cur.String())
			cur.Reset()
			open = false
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			open = true
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	if inQuote {
		return nil, errors.New("unbalanced quote")
	}
	flush()
	if len(out) == 0 {
		return nil, errors.New("empty command line")
	}
	return out, nil
}

// refuseUnsafeArgv is the belt pin: no resolved argv may carry --open (the
// browser escape this whole headless discipline exists to prevent) or
// --serve (a server that cannot finish inside RefreshTimeout). Matched
// case-insensitively, including the `--flag=value` spelling.
func refuseUnsafeArgv(argv []string) error {
	for _, a := range argv {
		low := strings.ToLower(strings.TrimSpace(a))
		switch {
		case low == "--open" || strings.HasPrefix(low, "--open="):
			return &RefusedArgvError{Token: "--open", Reason: "the refresh run is headless; a browser launch is never allowed"}
		case low == "--serve" || strings.HasPrefix(low, "--serve="):
			return &RefusedArgvError{Token: "--serve", Reason: "a dashboard server outlives the bounded one-shot"}
		}
	}
	return nil
}

// spawnOneShot builds the child command. The default is the production path
// (batchCmd, which wraps Windows .cmd/.bat shims); tests swap it for a
// test-binary helper so the exec path is exercised without Node.
var spawnOneShot = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return batchCmd(ctx, name, args...)
}

// Refresh runs one bounded headless refresh and returns the capped output.
// argv is the resolved entry argv (ResolveEntry); the refresh flags are
// appended here so a shim, a script and a bare binary all receive them.
//
// The caller reloads the artifacts on success: this function never reads or
// interprets the payload. Anything above RefreshTimeout is clamped down, a
// non-positive timeout means the default, and the child environment is the
// default-deny allow-list.
func Refresh(ctx context.Context, argv []string, timeout time.Duration) (Result, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return Result{}, &NotFoundError{Checked: []string{"empty argv"}}
	}
	if timeout <= 0 || timeout > RefreshTimeout {
		timeout = RefreshTimeout
	}
	if err := refuseUnsafeArgv(argv); err != nil {
		return Result{}, err
	}
	full := make([]string, 0, len(argv)+2)
	full = append(full, argv...)
	full = append(full, refreshHTMLFlag, refreshNoOpenFlag)
	if err := refuseUnsafeArgv(full); err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := spawnOneShot(runCtx, full[0], full[1:]...)
	// Default-deny: only the OS/runtime variables a Node child cannot start
	// without cross. Provider key values stay out (the child aggregates
	// session logs; it needs no provider credential to do so).
	cmd.Env = wirecheck.FilterChildEnv(nil)
	cmd.Stdin = nil

	stdout := &cappedCapture{limit: refreshOutputCap}
	stderr := &cappedCapture{limit: refreshOutputCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	elapsed := time.Since(start)
	res := Result{
		Argv:    full,
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Elapsed: elapsed,
	}
	if runErr == nil {
		return res, nil
	}
	// A deadline (or a cancel) is reported as the typed timeout: the child was
	// killed by the bound, so its exit status carries no information. A plain
	// non-zero exit keeps its code.
	if err := runCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return res, &TimeoutError{Timeout: timeout}
		}
		return res, fmt.Errorf("dashboard refresh cancelled: %w", err)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, &ExecError{ExitCode: res.ExitCode, Detail: maskedTail(res.Stderr, res.Stdout)}
	}
	return res, fmt.Errorf("dashboard refresh failed: %s", providers.MaskOperand(runErr.Error()))
}

// maskedTail renders the child's own failure text as one bounded, masked
// line: stderr when it said anything, stdout otherwise (Node prints some
// failures on stdout). Masking happens here as well as at the render site,
// because a child that echoes a token-shaped operand must not carry it into
// any error value at all.
func maskedTail(stderr, stdout string) string {
	text := strings.TrimSpace(stderr)
	if text == "" {
		text = strings.TrimSpace(stdout)
	}
	if text == "" {
		return ""
	}
	// Collapse to one line BEFORE masking so a 200-rune cut cannot split a
	// pattern-shaped secret into a fragment no pattern matches (the ordering
	// internal/inventory/fetch.go documents for the same reason).
	text = strings.Join(strings.Fields(text), " ")
	text = providers.MaskOperand(text)
	runes := []rune(text)
	if len(runes) > 200 {
		text = string(runes[:200]) + "..."
	}
	return text
}

// batchCmd builds the child command, wrapping Windows batch shims. A .cmd /
// .bat target cannot be spawned by os/exec directly, so it runs through
// cmd.exe with a hand-escaped raw command line (the B9 picker/fanout guard).
func batchCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	if !isBatchScript(name) {
		return exec.CommandContext(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, "cmd.exe")
	setRawCmdLine(cmd, buildBatchCmdLine(name, args))
	return cmd
}

// isBatchScript reports whether bin is a Windows batch file that os/exec
// cannot spawn directly. Copied from internal/launcher/picker.go, which copied
// internal/fanout/native_seat.go (B9) - keep the three in sync.
func isBatchScript(bin string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	switch strings.ToLower(filepath.Ext(bin)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}

// buildBatchCmdLine renders `cmd.exe /d /v:off /s /c "<bin> <args...>"` with
// every token escaped for the two cmd parse passes plus the target's
// CommandLineToArgvW tokenizer. Copied from internal/launcher/picker.go (which
// copied internal/fanout/native_seat.go, B9; the escaping there is validated
// empirically by the fanout .cmd round-trip test) - keep them in sync. /v:off
// pins delayed expansion off so a `!` in any token survives byte-for-byte.
func buildBatchCmdLine(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, escapeCmdToken(bin))
	for _, a := range args {
		parts = append(parts, escapeCmdToken(a))
	}
	return `cmd.exe /d /v:off /s /c "` + strings.Join(parts, " ") + `"`
}

// escapeCmdToken escapes one argument for a raw cmd.exe /d /s /c line so the
// bytes survive: pass 1 (cmd /c parse), pass 2 (batch %* re-parse) and the
// target program's CommandLineToArgvW tokenization. Copied verbatim from
// internal/launcher/picker.go / internal/fanout/native_seat.go (B9 cluster) -
// keep them in sync; the rules are documented there and are not re-derived
// here.
func escapeCmdToken(a string) string {
	var b strings.Builder
	b.WriteByte('"')
	cmdIn := true // cmd quote parity (toggles on every emitted `"` char)
	pending := 0  // pending literal backslashes
	flushBS := func(doubled bool) {
		n := pending
		if doubled {
			n *= 2
		}
		b.WriteString(strings.Repeat(`\`, n))
		pending = 0
	}
	for i := 0; i < len(a); i++ {
		switch c := a[i]; c {
		case '\\':
			pending++
		case '"':
			flushBS(true)
			b.WriteString(`\"`)
			cmdIn = !cmdIn
		case '%':
			// A11: the step-out quote is an emitted quote, so a pending
			// backslash run must be flushed DOUBLED in front of it - flushing
			// it single lets the target's CommandLineToArgvW read `\"` as an
			// escaped literal quote and re-frame the argument. Outside cmd
			// quotes `^%` emits NO quote, so the run stays literal.
			if cmdIn {
				flushBS(true)
				b.WriteString(`"^%"`)
			} else {
				flushBS(false)
				b.WriteString(`^%`)
			}
		case '&', '|', '<', '>', '(', ')':
			flushBS(false)
			if !cmdIn {
				b.WriteString("^^^")
			}
			b.WriteByte(c)
		case '^':
			flushBS(false)
			if !cmdIn {
				b.WriteString("^^^^")
			} else {
				b.WriteByte('^')
			}
		default:
			flushBS(false)
			b.WriteByte(c)
		}
	}
	flushBS(true) // trailing backslashes doubled before the closing quote
	b.WriteByte('"')
	return b.String()
}

// cappedCapture captures up to limit bytes and silently truncates past it.
// Write always reports the full length consumed, so a capped stream can never
// block the child on a "full" pipe (the wirecheck/version.go idiom, minus the
// overflow verdict: this output is diagnostic only, never parsed).
type cappedCapture struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedCapture) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		keep := c.limit - c.buf.Len()
		if keep > 0 {
			c.buf.Write(p[:keep])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedCapture) String() string { return c.buf.String() }
