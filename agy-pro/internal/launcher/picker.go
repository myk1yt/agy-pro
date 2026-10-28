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

	"agypro/internal/fanout"
	"agypro/internal/hybrid"
	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/realagy"
	"agypro/internal/swarm"
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

// swarmPromptTimeout bounds the [3] swarm prompt read. Free text needs more
// thought time than a seat number; a timeout cancels back to the native
// default (same outcome as an empty prompt).
const swarmPromptTimeout = 300 * time.Second

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

// readConsoleLineFn is the console line-reader seam. The Windows build points
// it at the raw-console implementation; other builds at the unavailable stub.
// Tests override it to drive the picker's console path deterministically.
var readConsoleLineFn = readConsoleLine

// pickerSavedPresets is the saved-preset seam for the model picker (F04): the
// prompt accepts the same §2.7 selection grammar as the swarm seat prompt, so a
// saved seat preset name resolves here too. A store that cannot be opened or
// read yields no names (the numbers/range/preset grammar is untouched); the
// store's corrupt-file warning keeps printing on its default WarnOut. Tests
// replace it to drive the picker without touching ~/.agy-pro/swarm-state.json.
var pickerSavedPresets = func() []swarm.SavedPreset {
	st, err := swarm.NewFileStore()
	if err != nil {
		return nil
	}
	presets, err := st.LoadPresets()
	if err != nil {
		return nil
	}
	return presets
}

// showModelPicker renders the numbered combined inventory and reads one
// selection. When stdin is a real console the line comes from the console path
// (multi-digit capable, nothing consumed until the line is complete), so the
// shared relay is never touched and the real-agy child can inherit the console
// directly; the relay stays reserved for non-console stdin. Returns
// Picked=false on timeout, EOF, empty input, or an invalid selection.
func showModelPicker(out io.Writer, consoleIn *os.File, lines lineSource, inv *inventory.Inventory) ModelPick {
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

	if consoleIn != nil {
		if line, status := readConsoleLineFn(consoleIn, pickerTimeout); status == consoleChoice {
			return resolvePickerLine(line, inv, out)
		} else if status == consoleTimeout {
			// Deadline: nothing was consumed, so any partial line stays in the
			// buffer for the child. Cancel like an empty answer.
			fmt.Fprintln(out, "[agy-pro] no model selected (timed out); continuing with External Models (bridge)")
			return ModelPick{}
		}
		// consoleUnavailable: fall through to the shared line source.
	}
	line, ok := lines.readLine(pickerTimeout)
	if !ok {
		fmt.Fprintln(out, "[agy-pro] no model selected (input closed or timed out); continuing with External Models (bridge)")
		return ModelPick{}
	}
	return resolvePickerLine(line, inv, out)
}

// resolvePickerLine maps one selection line onto a pick. An empty line cancels
// silently-by-default no more: every cancel now states what happens next, so a
// user who typed nothing (or typed a version-shaped token) is never left
// wondering why the session did not advance.
//
// F04: the prompt accepts the SAME §2.7 grammar as the swarm seat prompt, so a
// saved seat preset name resolves to that preset's stored Numbers (the first
// number is the picked model, exactly like a p1/p2/p3 answer already is). An
// unknown name is refused by name through the shared swarm sentence, so the two
// surfaces cannot drift; a seat-spec-shaped answer ("native:x", "./backups")
// stays on the old path and keeps its typed-operand rendering.
func resolvePickerLine(line string, inv *inventory.Inventory, out io.Writer) ModelPick {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		fmt.Fprintln(out, "[agy-pro] no model selected (cancelled); continuing with External Models (bridge)")
		return ModelPick{}
	}
	presets := pickerSavedPresets()
	expanded, expErr := swarm.ExpandSavedPresetNames(trimmed, presets)
	if expErr != nil {
		if len(inv.Entries) == 0 {
			fmt.Fprintln(out, "[agy-pro] no models in the combined inventory; continuing with External Models (bridge)")
			return ModelPick{}
		}
		// F04: the swarm sentence names the refusal ("unknown saved preset") and
		// quotes the token through providers.MaskOperand; the seeded sentence
		// pass here is the same one the swarm surfaces run over the same seeds
		// (SelectionSeeds carries the typed line and any resolved Numbers), so a
		// decorated compound cannot slip through at this echo.
		fmt.Fprintf(out, "[agy-pro] %s; continuing with External Models (bridge)\n",
			providers.MaskSentence(expErr.Error(), swarm.SelectionSeeds(trimmed, presets)...))
		return ModelPick{}
	}
	entries, err := inventory.ParseSelection(expanded, inv)
	if err != nil || len(entries) == 0 {
		if len(inv.Entries) == 0 {
			fmt.Fprintln(out, "[agy-pro] no models in the combined inventory; continuing with External Models (bridge)")
			return ModelPick{}
		}
		// FIX-8 / R5-1 / r6: the selection line is a TYPED operand, so the operand
		// masker owns it and its location exemption ("./backups" stays readable,
		// which is what makes the refusal actionable). On top of that the seat-token
		// authority is applied, because MaskOperand is provably the identity on the
		// underscore-head credential class (`sk_live_...`) and an operator can paste
		// one here as easily as into --seats.
		//
		// A LOCATION-SHAPED answer keeps the operand rendering byte-identical
		// (MaskSlot would strip its "./" marker and change the bytes of a benign
		// non-answer - the FIX-8 rationale), but ONLY when the location carries no
		// credential material: r6 closed the costume where a credential is dressed AS
		// a location ("./sk_live_.../x", "../sk_live_...", "C:\sk_live_...",
		// "scheme://sk_live_..."). See pickerLocationShaped.
		quoted := providers.MaskOperand(trimmed)
		if !pickerLocationShaped(trimmed) {
			if masked := fanout.MaskSeatToken(trimmed); masked != trimmed {
				quoted = masked
			}
		}
		fmt.Fprintf(out, "[agy-pro] invalid selection %q (%s); continuing with External Models (bridge)\n",
			quoted, swarm.SelectionGrammarHint(len(inv.Entries)))
		return ModelPick{}
	}
	return ModelPick{Entry: entries[0], Picked: true}
}

// pickerLocationShaped reports whether a typed picker answer names a location that
// carries NO credential material, i.e. whether the operand rendering may be kept
// byte-for-byte.
//
// Two conditions, both required:
//
//  1. the answer is location-DECORATED - a URL scheme ("://"), an absolute path,
//     or a leading ./ ../ .\ ..\ marker (providers' unexported isLocationShaped,
//     restated here because internal/providers is outside this change's scope);
//  2. no path SEGMENT of it carries credential material.
//
// Condition 2 is r6: r5 gated only on (1), so a credential dressed as a location
// ("./sk_live_0123456789abcdef/x") was treated as a benign path and echoed raw.
// The decoration is stripped, the remainder split on "/" and "\", and each segment
// is classified with the authority's own predicate - providers.NameCarriesCredential,
// the union of the operand, looks-secret and slot arms (NOT a new regex). A
// segment that is an identifier run (all-lowercase-hex, or the UUID form) is
// exempt, mirroring the slot policy's documented identifier exemption, and a
// filename's stem is what gets classified so "./prompts/3f2a8b9c1d2e3f4a.md"
// (a hash-named file, the shape the FIX-8 pin exists for) stays readable. A
// credential dressed as a FILENAME ("./sk_live_0123456789abcdef.md") still masks,
// because its stem is not an identifier run and the classifier flags it.
func pickerLocationShaped(s string) bool {
	if !pickerLocationDecorated(s) {
		return false
	}
	return !pickerLocationCarriesCredential(s)
}

// pickerLocationDecorated is the shape test above (condition 1).
func pickerLocationDecorated(s string) bool {
	if strings.Contains(s, "://") || filepath.IsAbs(s) {
		return true
	}
	for _, p := range []string{"./", "../", `.\`, `..\`} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// pickerLocationCarriesCredential scans the location's segments (condition 2).
//
// r7 F-02: the identifier-run exemption needs FILE EVIDENCE. A hash-named file
// ("./prompts/3f2a8b9c1d2e3f4a.md") is a name the operator typed and stays
// readable, while a BARE lowercase-hex run ("./3f2a8b9c1d2e3f4a", 16 runes) is
// indistinguishable from an opaque credential segment and takes the seat-token
// authority. So the exemption applies only when the segment carries a filename
// extension - the shape that says "this is a file", not "this is a value". That
// also closes the 32-hex "<hash>.bin" costume the r6 review flagged: the .bin
// stem is hex, but the reviewer's own point was the missing file evidence on the
// BARE spelling, which now masks; a hash-named file WITH an extension stays
// readable, which is the FIX-8 side of the trade and is pinned both ways.
func pickerLocationCarriesCredential(s string) bool {
	for _, seg := range pickerLocationSegments(s) {
		stem, hasExt := pickerFileNameStem(seg)
		if hasExt {
			// A filename: its stem is a name, so the identifier-run exemption
			// applies, and anything else is classified as usual.
			if pickerIdentifierRun(stem) {
				continue
			}
			if providers.NameCarriesCredential(stem) {
				return true
			}
			continue
		}
		if providers.NameCarriesCredential(seg) {
			return true
		}
	}
	return false
}

// pickerLocationSegments strips the URL scheme, the Windows drive prefix and the
// trivial "."/".." markers, then splits on both separators. Empty results are
// dropped.
func pickerLocationSegments(s string) []string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// A drive prefix ("C:") is decoration, not a segment; "user:pass@host" keeps
	// its colon because it is not a single-letter drive.
	if len(s) >= 2 && s[1] == ':' && len(s) > 2 && (s[2] == '\\' || s[2] == '/') {
		s = s[2:]
	}
	raw := strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' })
	out := make([]string, 0, len(raw))
	for _, seg := range raw {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// pickerFileNameStem returns the stem of a filename segment: a final dot followed
// by 1-4 alphanumerics ("prompts/3f2a...9.md" -> "3f2a...9"). Anything else is not
// a filename and comes back unchanged.
func pickerFileNameStem(seg string) (string, bool) {
	i := strings.LastIndexByte(seg, '.')
	if i <= 0 || i == len(seg)-1 {
		return seg, false
	}
	ext := seg[i+1:]
	if len(ext) > 4 {
		return seg, false
	}
	for _, r := range ext {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return seg, false
		}
	}
	return seg[:i], true
}

// pickerIdentifierRun reports whether s is an identifier shape the slot policy
// exempts from credential classification: an uninterrupted all-lowercase-hex run,
// or the 8-4-4-4-12 UUID form. It is applied to a filename STEM only (r7 F-02):
// a hash-named file is a name the operator typed, and masking it would break the
// readable echoes the FIX-8 pin protects, while the same hex run with no file
// evidence takes the authority. The authority's own URL-part arm sets a 20-rune
// floor on the hex shape; the divergence is deliberate and now scoped to
// filenames, not to every path segment. Uppercase hex is NOT exempt, matching the
// authority.
func pickerIdentifierRun(s string) bool {
	if s == "" {
		return false
	}
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		for i := 0; i < len(s); i++ {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				continue
			}
			if !pickerHexRune(s[i]) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func pickerHexRune(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
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
	// R5BF2-4 (adjacent-pattern scan of the S-3 class R5B-FIX-4 just closed on
	// the inventory fetch): the scrub list is the FIX-4 PRESET UNION
	// (hybrid.ChildKeyScrubNames = registry apiKeyEnv names UNION the compiled-in
	// known-preset KeyEnv names), not the registry-only list. An exported preset
	// key for a provider ABSENT from providers.json (OPENAI_API_KEY,
	// GROQ_API_KEY, ...) used to ride into this menu-[2] native child and
	// everything it spawns - the same hole the P2 session child, the P2 native
	// fallback and the `agy models` fetch already close. Strictly wider, no
	// behavior regression: a native pick needs no provider key value.
	cmd.Env = realagy.CleanChildEnv(hybrid.ChildKeyScrubNames(reg))

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
