package fanout

// Native seat execution (contract §2.2): one seat = one real-agy subprocess
// run in print mode. Transport: `<realAgy> --model <id>
// --disable-slash-commands -p "<seat prompt>" --print-timeout <budget>s`
// (argv; multi-line fidelity live-verified at U1 kickoff). ViaStdin is the
// pre-designed fallback seam (F-P); it is forced for .cmd/.bat targets with
// multi-line prompts (F-Dc) because cmd.exe cannot carry newlines in argv.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agypro/internal/providers"
	"agypro/internal/realagy"
)

const (
	// nativeProviderName / nativeBaseURL populate the synthetic Target of a
	// native seat so every existing provenance writer keeps working (§2.4).
	nativeProviderName = "native"
	nativeBaseURL      = "local-subprocess"

	// nativeStdoutCap bounds the captured seat answer (4 MiB, §2.2.2).
	nativeStdoutCap = 4 << 20
	// nativeStderrTailBytes keeps the last 512 bytes of stderr (§2.2.2).
	nativeStderrTailBytes = 512

	// nativeTimeoutMargin is subtracted from the remaining swarm deadline to
	// form agy's own --print-timeout budget (§2.2.2 timeout layering).
	nativeTimeoutMargin = 5 * time.Second
	// nativeTimeoutFloorSec is the minimum --print-timeout budget in seconds.
	nativeTimeoutFloorSec = 10
	// nativeCtxBackstop is added to the remaining swarm deadline for the Go
	// context passed to Spawn: agy's own --print-timeout fires first, the
	// ctx cancel is the hard backstop (kill -> reason=timeout).
	nativeCtxBackstop = 10 * time.Second

	// nativeTruncationNote marks a seat answer cut at nativeStdoutCap.
	nativeTruncationNote = "\n[output truncated at 4 MiB]"
)

// NativeOutcome is the raw result of one real-agy print-mode subprocess run.
type NativeOutcome struct {
	Content    string
	StderrTail string
	ExitCode   int
	TimedOut   bool
	DurationMs int64
	// SpawnErr is non-nil when the subprocess never started (resolver failure
	// or missing binary). Additive to the contract sketch: it lets the engine
	// render the §2.2.3 "spawn failed" mapping without conflating it with a
	// nonzero exit. Never serialized.
	SpawnErr error
	// stdoutTruncated records a 4 MiB cap hit (§2.2.2 "truncate + note").
	stdoutTruncated bool
}

// NativeRunner executes native seats as real-agy subprocesses. The zero value
// is usable: Resolver defaults to realagy.Resolve, Spawn defaults to
// exec.CommandContext, ViaStdin defaults to false (argv transport — the
// kickoff-verified default per F-P).
type NativeRunner struct {
	Resolver func() (string, error)                                            // default realagy.Resolve
	Spawn    func(ctx context.Context, bin string, args ...string) *exec.Cmd   // default exec.CommandContext
	ViaStdin bool                                                              // false = argv transport (kickoff verdict)

	// scrubNames are the provider APIKeyEnv names removed from the child env
	// (§2.2.4 item 3); the engine sets them from realagy.ApiKeyEnvNames(reg).
	scrubNames []string
}

func (r *NativeRunner) resolve() (string, error) {
	if r.Resolver != nil {
		return r.Resolver()
	}
	return realagy.Resolve()
}

func (r *NativeRunner) spawn(ctx context.Context, bin string, args ...string) *exec.Cmd {
	if r.Spawn != nil {
		return r.Spawn(ctx, bin, args...)
	}
	return exec.CommandContext(ctx, bin, args...)
}

// Run executes one native seat and returns the raw outcome. The seat prompt
// is transported via argv (default) or stdin (ViaStdin). deadline is the
// swarm deadline; agy's own --print-timeout is set to remaining-5s (floor
// 10s) and the spawn context to remaining+10s so the child's own timeout
// fires first with clean diagnostics (§2.2.2).
func (r *NativeRunner) Run(ctx context.Context, cfg RunConfig, spec SeatSpec, prompt string, deadline time.Time) NativeOutcome {
	start := time.Now()
	out := NativeOutcome{ExitCode: -1}
	finish := func() NativeOutcome {
		out.DurationMs = time.Since(start).Milliseconds()
		return out
	}

	bin, err := r.resolve()
	if err != nil {
		out.SpawnErr = err
		return finish()
	}

	remaining := time.Until(deadline)
	budgetSec := int((remaining - nativeTimeoutMargin) / time.Second)
	if budgetSec < nativeTimeoutFloorSec {
		budgetSec = nativeTimeoutFloorSec
	}
	spawnCtx, cancel := context.WithTimeout(ctx, remaining+nativeCtxBackstop)
	defer cancel()

	batch := isBatchScript(bin)
	// F-Dc: cmd.exe cannot carry newlines in argv; a multi-line prompt on a
	// .cmd/.bat target FORCES the stdin fallback.
	viaStdin := r.ViaStdin || (batch && strings.ContainsAny(prompt, "\r\n"))

	args := []string{"--model", spec.ModelID}
	// Hardening flag (probe 2026-10-14): `agy.exe --help` lists
	// "--disable-slash-commands   Disable slash command and skill expansion in
	// print mode". Live probe with -p: `agy.exe --model gemini-3.8-flash-low -p
	// "Reply with exactly: PROBE_OK" --disable-slash-commands --print-timeout
	// 30s` exited 0 and printed PROBE_OK, so the flag is compatible with the
	// print-mode spawn. It shrinks the child's inherited plugin/skill surface
	// (the nesting vector); the anti-orchestration mandate itself rides in the
	// seat prompt preamble (buildNativeSeatPrompt). The probed --help output
	// lists NO flag that disables subagents outright (`agy agents` is a
	// read-only list subcommand with only -h/--help), so the preamble is the
	// primary control.
	args = append(args, "--disable-slash-commands")
	if viaStdin {
		args = append(args, "-p")
	} else {
		args = append(args, "-p", prompt)
	}
	args = append(args, "--print-timeout", fmt.Sprintf("%ds", budgetSec))

	var cmd *exec.Cmd
	if batch {
		// Windows .cmd/.bat spawn hazard (§2.2.2 F-MISC/F-Dc): Go os/exec
		// cannot execute batch files directly; wrap in cmd.exe /d /s /c with
		// a hand-escaped raw command line (Go's own argv quoting is not
		// cmd-compatible).
		cmd = r.spawn(spawnCtx, "cmd.exe")
		setRawCmdLine(cmd, buildBatchCmdLine(bin, args))
	} else {
		cmd = r.spawn(spawnCtx, bin, args...)
	}

	root := cfg.Root
	if root == "" {
		root = "."
	}
	cmd.Dir = root
	// §2.2.4: single env builder (F-ENV/F-Db). A test seam may pre-set Env
	// (helper-process markers); the same scrubbing rules apply to that base.
	cmd.Env = realagy.CleanChildEnvWithBase(cmd.Env, r.scrubNames)

	var stdout cappedBuffer
	var stderr tailBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if viaStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	// argv transport: cmd.Stdin stays nil (/dev/null). Never interactive.

	runErr := cmd.Run()
	out.Content = stdout.String()
	out.StderrTail = stderr.String()

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			out.ExitCode = exitErr.ExitCode()
		} else {
			// Start failure (binary missing, cmd.exe unavailable, ...)
			out.SpawnErr = runErr
			if spawnCtx.Err() != nil {
				// A5: a seat that queued behind the concurrency semaphore can
				// find its own backstop already expired when Start runs. That
				// is a killed seat, not a broken binary: flag the timeout so
				// nativeSeatResult reports reason=timeout (with degraded)
				// instead of the "spawn failed" mapping.
				out.TimedOut = true
			}
			return finish()
		}
	} else {
		out.ExitCode = 0
	}
	if spawnCtx.Err() != nil {
		// ctx deadline or parent cancel killed the child: hard backstop.
		out.TimedOut = true
	}
	return finish()
}

// isQuotaExhausted classifies native stderr per plan R2: 429 / quota /
// resource_exhausted markers mean Antigravity rate limiting.
func isQuotaExhausted(stderrTail string) bool {
	s := strings.ToLower(stderrTail)
	return strings.Contains(s, "429") ||
		strings.Contains(s, "quota") ||
		strings.Contains(s, "resource_exhausted")
}

// nativeSeatResult maps a NativeOutcome onto the §2.2.3 SeatResult table.
// redact is the registry redactor: stderr tails can echo credential
// fragments and must never reach artifacts unredacted (A4). A nil redactor
// fails closed to the generic credential shapes rather than passing native
// stderr through raw.
func nativeSeatResult(seatID int, target providers.Target, o NativeOutcome, redact func(string) string) *SeatResult {
	if redact == nil {
		redact = providers.NewRedactor()
	}
	res := &SeatResult{
		SeatID:        seatID,
		Kind:          SeatKindNative,
		InitialTarget: target,
		FinalTarget:   target,
		Content:       o.Content,
		DurationMs:    o.DurationMs,
		// No failover for native seats (§2.2.3): Failovers stays empty.
	}
	tail := strings.TrimSpace(redact(o.StderrTail))
	switch {
	case o.TimedOut:
		// A5: the timeout mapping wins over the spawn error, because a seat
		// killed by its own backstop while queued on the semaphore reaches
		// this function with BOTH flags set.
		res.Status = "failed"
		res.Degraded = true
		res.Error = "reason=timeout native seat exceeded its deadline and was killed"
		if tail != "" {
			res.Error += ": " + tail
		}
	case o.SpawnErr != nil:
		// A6: a seat that never started is a degraded seat. The summary and
		// the console table used to show "Degraded Mode: false" for it.
		res.Status = "failed"
		res.Degraded = true
		res.Error = fmt.Sprintf("native seat spawn failed: %v", redact(o.SpawnErr.Error()))
	case o.ExitCode != 0:
		res.Status = "failed"
		res.Degraded = true
		msg := fmt.Sprintf("native seat exited %d", o.ExitCode)
		if tail != "" {
			msg += ": " + tail
		}
		if isQuotaExhausted(o.StderrTail) {
			msg = "reason=quota " + msg
		}
		res.Error = msg
	case strings.TrimSpace(o.Content) == "":
		// A2: no textless hang - an empty answer is a failed seat. The
		// redacted stderr tail rides along, same pattern as the timeout and
		// exit-code cases: agy -p hitting its own --print-timeout exits 0
		// with partial (here empty) output, so without the tail the operator
		// sees no reason at all (live: reports/fanout/20261014-065355, 303.4s
		// under a 300s budget, error was the bare "native seat produced empty
		// output" while stderr carried the print-timeout line).
		res.Status = "failed"
		msg := "native seat produced empty output"
		if tail != "" {
			msg += ": " + tail
		}
		res.Error = msg
	default:
		res.Status = "success"
	}
	return res
}

// nativeTarget builds the synthetic provider Target of a native seat (§2.4)
// so every existing provenance writer (seat reports, summary.json,
// reconciliation.md, state records) keeps working unchanged.
func nativeTarget(modelID string) providers.Target {
	return providers.Target{
		Provider: providers.Provider{Name: nativeProviderName, BaseURL: nativeBaseURL},
		Model:    providers.Model{ID: modelID, Family: providers.InferFamily(modelID)},
	}
}

// isBatchScript reports whether bin is a Windows batch file that os/exec
// cannot spawn directly (§2.2.2 F-MISC).
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
// CommandLineToArgvW tokenizer (§2.2.2 F-Dc; escaping validated empirically
// by the .cmd round-trip test).
//
// /v:off pins delayed expansion off (A11): with it on, a `!` inside the seat
// prompt would be expanded at the target's parse pass, so the prompt text
// would not survive byte-for-byte.
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
// target program's CommandLineToArgvW tokenization.
//
// Rules (empirically verified, see coder-report-u1.md live evidence):
//   - the token is wrapped in double quotes;
//   - `"` -> `\"` (CommandLineToArgvW literal quote; cmd sees a bare quote
//     and toggles its own quote parity - tracked below);
//   - backslash runs are doubled only before an escaped quote or the closing
//     quote (CommandLineToArgvW), literal elsewhere;
//   - cmd metacharacters (& | < > ( )) are literal while cmd-parity is
//     inside quotes; outside they need three carets (one consumed per cmd
//     pass plus the escape itself);
//   - `^` is literal inside quotes; outside it needs four carets;
//   - `%` is never safe inside cmd quotes (variable expansion ignores
//     quotes): step out, emit `^%` (percent is not re-expanded after batch
//     %* substitution, so one caret survives both passes), step back in.
//
// A11: a pending backslash run is ALWAYS flushed doubled before an emitted
// quote (the `"` of the step-out, the `\"` of a literal quote and the closing
// quote). Flushing it single lets the target's CommandLineToArgvW read `\"` as
// an escaped literal quote, which re-frames the argument.
//
// Newlines must never reach this function on the argv path: multi-line
// prompts on .cmd/.bat targets force ViaStdin (F-Dc).
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
			if cmdIn {
				// The step-out quote is an emitted quote: backslashes in
				// front of it must be doubled (A11), or CommandLineToArgvW
				// turns them into an escaped literal quote and the argument
				// re-frames.
				flushBS(true)
				b.WriteString(`"^%"`)
			} else {
				// Already outside cmd quotes: `^%` emits NO quote, so the
				// pending run stays literal. Doubling it here (A11 probe)
				// rewrote `a"\%b` into `a"\\%b` at the target.
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

// cappedBuffer captures at most nativeStdoutCap bytes (§2.2.2 stdout cap).
type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := nativeStdoutCap - c.buf.Len()
	if room <= 0 {
		c.truncated = true
		return len(p), nil // discard, but report success to the writer
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string {
	s := c.buf.String()
	if c.truncated {
		s += nativeTruncationNote
	}
	return s
}

// tailBuffer keeps the last nativeStderrTailBytes bytes written (§2.2.2).
type tailBuffer struct {
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > nativeStderrTailBytes {
		t.buf = t.buf[len(t.buf)-nativeStderrTailBytes:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

var _ io.Writer = (*cappedBuffer)(nil)
var _ io.Writer = (*tailBuffer)(nil)
