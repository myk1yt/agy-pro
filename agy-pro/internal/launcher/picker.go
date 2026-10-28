package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// Model sub-menu behind launcher choice [2] (plan §4.2 B1, contract §2.7).
// The picker lists the COMBINED inventory (native `agy models` catalog +
// every enabled registered external) and routes the pick by ENTRY PROVENANCE
// (Entry.Kind), never by id-string matching (F-BRIDGE): a native pick runs
// real agy with `--model <id>` and NO bridge even when the registry holds a
// colliding id; an external pick takes the existing gateway+bridge path.

// pickerTimeout bounds the sub-menu's selection read. The user has already
// opted in by choosing [2], so it is deliberately much longer than the top
// menu's 2s deadline; a timeout cancels back to the legacy [2] bridge route.
const pickerTimeout = 60 * time.Second

// ModelPick is the outcome of the launcher model sub-menu.
type ModelPick struct {
	Entry  inventory.Entry
	Picked bool // false = user aborted; fall back to the legacy [2] behavior
}

// launchPlan is what ExecutePassthrough does with a picker outcome. Exactly
// one mode applies: forced-native, forced-bridge (external pick), or legacy
// bridge fallback (no pick).
type launchPlan struct {
	ForcedNativeID string   // non-empty: exec real agy `--model <id>`, NO bridge
	ForceBridge    bool     // activate the gateway+bridge path
	ModelArgs      []string // extra args (external pick: --model <DisplayName>)
}

// pickerInventory is the combined-inventory seam for the sub-menu; tests
// stub it to drive the picker without spawning `agy models`.
var pickerInventory = func(reg *providers.Registry) *inventory.Inventory {
	nf := inventory.Cached(inventory.NewRealAgyFetcher(reg), inventory.DefaultCacheTTL)
	inv, err := inventory.Load(context.Background(), reg, nf)
	if err != nil {
		return &inventory.Inventory{}
	}
	return inv
}

// showModelPicker renders the numbered combined inventory and reads one
// selection line through the shared relay (multi-digit capable - the raw
// console single-key path only maps 1/2/3/Enter, prompt.go:32-43). Returns
// Picked=false on timeout, EOF, empty input, or an invalid selection.
func showModelPicker(out io.Writer, lines lineSource, inv *inventory.Inventory) ModelPick {
	if inv == nil {
		inv = &inventory.Inventory{}
	}
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  Choose a Model (native + external)")
	fmt.Fprintln(out, "===========================================================")
	if inv.NativeErr != nil {
		fmt.Fprintf(out, "[native] unavailable: %v\n", inv.NativeErr)
	}
	inv.RenderNumbered(out)
	fmt.Fprint(out, "Model number (Enter=cancel): ")

	line, ok := lines.readLine(pickerTimeout)
	if !ok {
		return ModelPick{}
	}
	trimmed := strings.TrimSpace(line)
	// Enter alone cancels: the §2.7 "empty input = default preset" rule is the
	// swarm builder's seat-selection default; in the single-model picker the
	// prompt offers "Enter=cancel".
	if trimmed == "" {
		return ModelPick{}
	}
	entries, err := inventory.ParseSelection(trimmed, inv)
	if err != nil || len(entries) == 0 {
		fmt.Fprintln(out, "[agy-pro] no model selected; continuing with External Models (bridge)")
		return ModelPick{}
	}
	return ModelPick{Entry: entries[0], Picked: true}
}

// planFromPick maps a picker outcome onto the launch plan (F-BRIDGE):
// route by Entry.Kind provenance, never by id matching.
func planFromPick(pick ModelPick) launchPlan {
	if !pick.Picked {
		// Legacy choice-[2] behavior preserved as the abort fallback.
		return launchPlan{ForceBridge: true}
	}
	if pick.Entry.Kind == "native" {
		return launchPlan{ForcedNativeID: pick.Entry.ModelID}
	}
	return launchPlan{
		ForceBridge: true,
		ModelArgs:   []string{"--model", pick.Entry.DisplayName},
	}
}

// spawnRealAgy builds the real-agy child command (test seam).
var spawnRealAgy = func(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// executeForcedNative runs real agy as a pure native session for the picked
// native model (contract §2.7 F-BRIDGE): `--model <id>` with a
// CleanChildEnv-scrubbed environment (no AGY_LLM_GATEWAY_*, no provider key
// names), and WITHOUT ever touching ShouldActivateBridge or the bridge - a
// registry id collision must not hijack a native pick into gateway mode.
// Legacy `-m` passthrough behavior is unchanged (direct CLI invocations
// still go through ShouldActivateBridge).
//
// B9: .cmd/.bat real-agy targets get the same Windows batch wrap+escape guard
// as native seats (internal/fanout/native_seat.go isBatchScript /
// buildBatchCmdLine / escapeCmdToken / setRawCmdLine — copied here verbatim
// because those helpers are unexported and internal/fanout is outside this
// change's edit scope). Go os/exec cannot spawn batch files directly; the
// wrap is cmd.exe /d /v:off /s /c with a hand-escaped raw command line.
func executeForcedNative(realAgyPath, modelID string, stdin io.Reader, reg *providers.Registry) (int, error) {
	args := []string{"--model", modelID}
	var cmd *exec.Cmd
	if isBatchScript(realAgyPath) {
		cmd = spawnRealAgy("cmd.exe")
		setRawCmdLine(cmd, buildBatchCmdLine(realAgyPath, args))
	} else {
		cmd = spawnRealAgy(realAgyPath, args...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Provider key env names join the scrub list so a native child never
	// inherits key VALUES (S-3 / F-ENV / F-Db) - the same builder the catalog
	// fetch and native seats use (contract §2.2.4).
	cmd.Env = realagy.CleanChildEnv(realagy.ApiKeyEnvNames(reg))

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		// B2/U8: the operand may be a misdirected key paste; mask before echo.
		return 1, fmt.Errorf("failed to run real agy at %q: %w", providers.MaskOperand(realAgyPath), err)
	}
	return 0, nil
}

// isBatchScript reports whether bin is a Windows batch file that os/exec
// cannot spawn directly. Copied from internal/fanout/native_seat.go (B9).
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
// CommandLineToArgvW tokenizer. Copied from internal/fanout/native_seat.go
// (B9); escaping validated empirically by the fanout .cmd round-trip test.
// /v:off pins delayed expansion off (A11) so a `!` in any token survives
// byte-for-byte.
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
// target program's CommandLineToArgvW tokenization. Copied from
// internal/fanout/native_seat.go (B9) — keep the two in sync.
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
			// A11: the step-out quote is an emitted quote, so a pending
			// backslash run must be flushed DOUBLED in front of it — flushing
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
