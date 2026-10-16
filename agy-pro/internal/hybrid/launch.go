package hybrid

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"agypro/internal/mesh"
	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// ExitMeshDied is the process exit code returned when the mesh dies mid-session.
// It is sysexits.h EX_TEMPFAIL (75): a distinct, documented, non-zero code so a
// caller can tell a mesh death from a normal child exit or a launch failure.
const ExitMeshDied = 75

// launchNativeFallback execs the real agy with NO mesh and NO CLOUD_CODE_URL,
// after printing a clear notice to the child's stderr. It is the never-a-half-
// configured-session path: a wirecheck NOGO, an unreadable registry, or zero
// enabled externals all land here. The child environment is still scrubbed of
// provider key VALUES (S-3 precedent): the keys stay in this process for the
// mesh, never in the child.
//
// The mesh-specific --model pin is stripped (R5B-FIX-2 parity with P2): in a P1
// session the pinned id is a mesh-catalog id, and native agy has no mesh and no
// catalog to answer it, so forwarding it made the session exit 1 at agy's own
// --model gate. The policy is the shared nativeFallbackArgs/nativeFallbackDroppedPin
// pair, the same one p2NativeFallback uses, so the two fallback twins cannot drift
// on this axis.
func launchNativeFallback(realAgy string, opts LaunchOptions, reg *providers.Registry, notice string) (int, error) {
	if notice != "" {
		// R5AF3-5: mask + control-strip + path-withhold the notice, the same
		// rendering P2's p2NativeFallback applies, so a raw path interpolated by
		// a source notice (registry-read error) never reaches stderr.
		fmt.Fprintf(stderrOr(opts.Stderr), "[agy-pro] %s - falling back to native agy (no mesh, no CLOUD_CODE_URL)\n", renderFallbackNotice(notice))
	}
	args := nativeFallbackArgs(opts.Args)
	if dropped := nativeFallbackDroppedPin(opts.Args); dropped != "" {
		printDroppedPinNotice(stderrOr(opts.Stderr), dropped)
	}
	cmd := exec.Command(realAgy, args...)
	cmd.Env = realagy.CleanChildEnv(ChildKeyScrubNames(reg))
	cmd.Env = append(cmd.Env, opts.ChildEnv...)
	cmd.Stdin = stdinOr(opts.Stdin)
	cmd.Stdout = stdoutOr(opts.Stdout)
	cmd.Stderr = stderrOr(opts.Stderr)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		// B2/U8: the operand may be a misdirected key paste; mask before echo.
		return 1, fmt.Errorf("failed to run real agy at %q: %w", providers.MaskOperand(realAgy), err)
	}
	return 0, nil
}

// nativeFallbackArgs returns the argv a native fallback execs: the caller's args
// with a mesh-specific --model pin removed (R5B-FIX-2). It is the shared policy
// behind BOTH fallback twins (P1's launchNativeFallback and P2's
// p2NativeFallback); the p2FallbackArgs / p2DroppedPin names are thin aliases
// kept for the P2 call sites and their tests.
//
// A pin is KEPT when native agy can resolve it from its own static catalog -
// i.e. it parses to one of the static Gemini ids - and it is kept in the BARE
// static spelling agy's --model gate accepts (R5BF2-2): the mesh path already
// normalizes the same value through p2ModelKey, and forwarding a decorated
// spelling ("nw/gemini-3.8-flash-high", "gemini-3.8-flash-high (nw)") would
// re-acquire the exact hard-fail this strip exists to remove. Everything else
// (a registry id, a customModels scheme key, a provider-qualified spelling of a
// non-static id, a best-effort blade: slug) exists only because the mesh's
// settings document or catalog carries it, so it is stripped.
//
// A blanket strip was considered and rejected: it would silently substitute a
// different model for a static-id pin the user chose, which is the failure mode
// the static-id policy exists to prevent ("never a silent substitution").
func nativeFallbackArgs(args []string) []string {
	model, _, _ := extractChildSelectors(args)
	if model == "" {
		return args
	}
	bare, ok := p2NativePinValue(model)
	if !ok {
		return p2StripModelSelector(args)
	}
	// Keep the CHOICE in the spelling native agy resolves. The re-pinned flag
	// goes FIRST, the same front-door-first order p2ChildArgs uses: appending it
	// after the stripped args would place it past a caller "--" terminator (or a
	// positional), where agy's Go-flag parser never reads it and the pin would
	// be silently lost.
	rest := p2StripModelSelector(args)
	out := make([]string, 0, len(rest)+2)
	out = append(out, "--model", bare)
	return append(out, rest...)
}

// nativeFallbackDroppedPin returns the pinned model value nativeFallbackArgs
// removes, or "" when nothing was dropped. It exists so the fallback can disclose
// the drop instead of substituting silently. A pin that is KEPT in a normalized
// bare spelling is not a drop: the user's choice still runs, so nothing is
// disclosed for it.
func nativeFallbackDroppedPin(args []string) string {
	model, _, _ := extractChildSelectors(args)
	if model == "" {
		return ""
	}
	if _, ok := p2NativePinValue(model); ok {
		return ""
	}
	return model
}

// printDroppedPinNotice discloses a stripped pin on a masked, control-stripped
// line. Shared by both fallback twins so the disclosure cannot drift: the user
// picked (or typed) a model this fallback cannot serve, so the line names it and
// states what runs.
//
// R5BF2-1: stripControlChars FIRST-class parity with the sibling sinks
// (p2DropSink, renderFallbackNotice, StaticIDTarget). MaskOperand masks
// secret-shaped operands only; it does not remove control bytes, so a crafted
// registry id or argv token carrying ESC/CR/BS could otherwise repaint the
// terminal or forge a "[agy-pro]" line into this very disclosure - the line whose
// whole job is to be trustworthy. Wording is deliberately class-neutral: the
// stripped set is BOTH mesh-specific pins (a registry id, a scheme key, a blade:
// slug) and non-static native-catalog ids (claude-*, gpt-oss-*), which native
// subscription mode can sometimes serve but this profile's static-only keep rule
// does not. "Needs the P2 mesh" alone would misdescribe the second class
// (round-5b-fix re-review, qwen F-02 nuance).
func printDroppedPinNotice(errw io.Writer, dropped string) {
	fmt.Fprintf(errw,
		"[agy-pro] the pinned model %s is not resolvable on this native fallback (only agy's static Gemini ids are); the session runs agy's default native model instead\n",
		stripControlChars(providers.MaskOperand(dropped)))
}

// ChildKeyScrubNames is the provider-key env-name scrub list for a real-agy
// child. It is the registry's apiKeyEnv names (realagy.ApiKeyEnvNames) UNION the
// compiled-in known-preset KeyEnv names (providers.KnownProviders), ALWAYS
// (FIX-4): a readable registry must not be able to skip the preset union, or an
// exported preset key for a provider ABSENT from providers.json (e.g.
// OPENAI_API_KEY, GROQ_API_KEY) would ride into the child and everything it
// spawns (the S-3 class). The presets are independent of providers.json, so the
// union is strictly safer: the child never needs any provider key VALUE (the
// mesh resolves keys in-process from names). Exported so the launcher's
// true-native exec (FIX-53/FIX-60) shares the exact same union.
func ChildKeyScrubNames(reg *providers.Registry) []string {
	names := realagy.ApiKeyEnvNames(reg)
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[n] = true
	}
	for _, h := range providers.KnownProviders() {
		n := strings.TrimSpace(h.KeyEnv)
		if n == "" {
			continue
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, n)
	}
	return names
}

// p1ExitCode maps a mesh.RunChild outcome to a process exit code:
//   - nil (the child exited 0) -> 0;
//   - *exec.ExitError (the child exited non-zero) -> the child's code, unchanged;
//   - an error wrapping mesh.ErrMeshDied -> ExitMeshDied (75), with a nil error
//     because the mesh already wrote the one visible fail-fast line;
//   - anything else (refuse-to-launch, start failure, context cancel) -> 1 with
//     the error propagated so the caller prints it.
func p1ExitCode(runErr error) (int, error) {
	if runErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(runErr, mesh.ErrMeshDied) {
		return ExitMeshDied, nil
	}
	return 1, runErr
}

// stdinOr returns r, or os.Stdin when r is nil.
func stdinOr(r io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return os.Stdin
}

// stdoutOr returns w, or os.Stdout when w is nil.
func stdoutOr(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stdout
}

// stderrOr returns w, or os.Stderr when w is nil.
func stderrOr(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stderr
}

// noticePathShapeRe detects a path-shaped substring in a wirecheck reason: a
// Windows volume path ("C:\...", "C:/..."), a multi-segment POSIX absolute path
// ("/home/user/..."), or a backslash UNC path ("\\server\share\...",
// "\\?\UNC\server\share\..."). The boundaries mirror agysettings.scrubAbsolutePaths
// (unexported) so a URL scheme does not false-positive: the "e:" of blade: and
// the "i:/" of gemini-api:// sit after an alphanumeric and are excluded, and a
// single-segment route literal ("/v1internal:x") is not a multi-segment path.
// The UNC alternative (host-segment backslash shape) closes the R5AF3-2 gap so a
// UNC real-agy path is withheld too. agysettings exports no location mask and
// providers.MaskOperand exempts locations, so the notice side needs its own
// guard (FIX-65 / R5AF2-5 / R5AF3-2).
var noticePathShapeRe = regexp.MustCompile(`(?i)(?:^|[^a-zA-Z0-9-])[a-z]:[\\/]|(?:^|[\s"'(=\[])/[^\s"':,;)]+/|\\\\[^\s"':,;)]+\\`)

// renderWirecheckReasons joins wirecheck NOGO reasons for a fallback notice,
// location-safely (FIX-65 / R5AF2-5). providers.MaskOperand exempts locations,
// so a ScanErr reason that embeds the real-agy absolute path (a read failure
// names the binary) would otherwise reach stderr unredacted. Such a reason is
// NOT interpolated: it collapses to a class label and the operator is pointed at
// the agy-pro logs for the detail. Non-path reasons (public canary literals,
// version strings) are kept verbatim. Shared by P1's nogoNotice and P2's
// p2NogoNotice so the two notice twins cannot drift on this axis.
func renderWirecheckReasons(reasons []string) string {
	kept := make([]string, 0, len(reasons)+1)
	sawPath := false
	for _, r := range reasons {
		if noticePathShapeRe.MatchString(r) {
			sawPath = true
			continue
		}
		kept = append(kept, r)
	}
	if sawPath {
		kept = append(kept, "a diagnostic reason that names a local path was withheld; see the agy-pro logs for the detail")
	}
	return strings.Join(kept, "; ")
}

// renderFallbackNotice renders a native-fallback notice for stderr: masked,
// control-stripped, and location-safe (FIX-6 / R5AF3-5). providers.MaskOperand
// exempts locations, so a notice whose SOURCE still interpolates a raw absolute
// path (P1's registry-read error names the file; a UNC real-agy path) would
// otherwise reach stderr unredacted. When a path shape survives the mask the
// path-bearing notice is withheld (collapsed to a class label that points at the
// agy-pro logs) rather than echoed: fail-safe, the fallback still runs, only the
// diagnostic text is dropped. Shared by P1's launchNativeFallback and P2's
// p2NativeFallback so the two twins cannot drift on this axis (the FIX-10
// concern).
func renderFallbackNotice(notice string) string {
	masked := stripControlChars(providers.MaskOperand(notice))
	if noticePathShapeRe.MatchString(masked) {
		return "the diagnostic detail was withheld because it names a local path; see the agy-pro logs for the reason"
	}
	return masked
}
