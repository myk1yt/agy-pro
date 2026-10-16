package wirecheck

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// versionProbeTimeout bounds one `<resolved binary> --version` run. The probe
// is headless and read-only; the bound keeps a hung child from stalling a
// launch that gates on this verdict.
const versionProbeTimeout = 15 * time.Second

// versionOutputCap bounds the captured probe output (review round 1c, FIX-14a).
// A version line is a few dozen bytes, so 4 KB leaves room for a decorated
// banner while a chatty child cannot balloon the capture; output past the cap
// is a probe failure, never a truncated parse.
const versionOutputCap = 4 << 10

// childEnvAllowList is the DEFAULT-DENY environment allow-list for the
// version-probe child (review round 1, FIX-7). The probe execs the very
// binary the canary gate exists to distrust, so the child must never inherit
// the full shim environment: env-resident provider API key VALUES live there
// (the primary KeyValue() source). Only the OS/runtime variables a child
// process needs to start are allowed; nothing else crosses, which makes any
// knowledge of the user's providers.json unnecessary.
var childEnvAllowList = map[string]bool{
	"SYSTEMROOT":  true,
	"WINDIR":      true,
	"COMSPEC":     true,
	"PATHEXT":     true,
	"PATH":        true,
	"TEMP":        true,
	"TMP":         true,
	"USERPROFILE": true,
	"HOME":        true,
	"LANG":        true,
}

// nameInAllowList reports whether an environment name is on the allow-list.
// Windows environment names are case-insensitive and the block preserves the
// creation case (e.g. "Path", "SystemRoot"), so matching there folds case;
// POSIX keeps exact matching so a differently-cased lookalike cannot sneak in.
func nameInAllowList(name string) bool {
	if childEnvAllowList[name] {
		return true
	}
	if runtime.GOOS == "windows" {
		return childEnvAllowList[strings.ToUpper(name)]
	}
	return false
}

// FilterChildEnv is the exported default-deny child-environment filter (review
// round 1c, FIX-12): it keeps only allow-listed entries (name and value
// verbatim) and drops everything else, including malformed entries without
// "=". base nil means os.Environ(). Callers that exec a third-party binary use
// it so the child never inherits the full shim environment (provider key
// values, AGY_LLM_GATEWAY_API_KEY); cmd/agy's two `--version` spawns
// (runDoctor Check 1 and runVersion) route through it.
func FilterChildEnv(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		if nameInAllowList(strings.TrimSpace(kv[:i])) {
			out = append(out, kv)
		}
	}
	return out
}

// childEnv builds the environment the version probe hands to the child.
func childEnv() []string {
	return FilterChildEnv(nil)
}

// probeCommand builds the configured `<path> --version` command: stdin is the
// null device (cmd.Stdin stays nil) and the environment is the default-deny
// allow-list. Tests capture the child env from the returned command without
// exec'ing anything.
func probeCommand(ctx context.Context, path string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = childEnv()
	return cmd
}

// cappedBuffer captures up to versionOutputCap bytes and records whether the
// child wrote past the cap. Write always reports the full length consumed (the
// child must never block on a "full" pipe just because we stopped keeping).
type cappedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > versionOutputCap {
		c.overflow = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// String returns the captured bytes (only meaningful when overflow is false).
func (c *cappedBuffer) String() string {
	return c.buf.String()
}

// runVersionProbe runs `<path> --version` and returns the parsed version.
// Stdin is the null device and the env is the default-deny allow-list, so the
// probe can neither block on an interactive prompt nor leak the shim's
// environment (provider key values included) to the binary under test. The
// output capture is capped; a timeout, a non-zero exit, over-cap output, or
// output without a version is an error: a hard fail that forces NOGO.
func runVersionProbe(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()

	cmd := probeCommand(ctx, path)
	out := &cappedBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out

	return probeResult(cmd.Run(), out, filepath.Base(path))
}

// probeResult turns one finished probe run into (version, error).
func probeResult(runErr error, out *cappedBuffer, name string) (string, error) {
	if runErr != nil {
		return "", fmt.Errorf("%s --version failed: %v%s", name, runErr, outputTail(out.String()))
	}
	if out.overflow {
		return "", fmt.Errorf("%s --version produced more than %d bytes of output", name, versionOutputCap)
	}
	version := parseVersion(out.String())
	if version == "" {
		return "", fmt.Errorf("%s --version produced no version output", name)
	}
	return version, nil
}

// versionPattern matches the first semver-ish token in `--version` output
// (agy prints e.g. "1.3.5"; a decorated line like "agy 1.3.5 (build 7)"
// still yields 1.3.5).
var versionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.-]+)?`)

// parseVersion extracts the version token from probe output. Output with no
// digits at all is returned trimmed, so a nonsense build still shows up as
// drift instead of silently matching.
func parseVersion(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	if match := versionPattern.FindString(trimmed); match != "" {
		return match
	}
	return trimmed
}

// outputTail collapses probe output to one bounded line for error messages.
// Version output carries no secrets, but an unbounded child-output echo in an
// error message is a leak-shaped habit; the cap keeps errors terminal-sized.
func outputTail(s string) string {
	collapsed := strings.Join(strings.Fields(s), " ")
	runes := []rune(collapsed)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return collapsed
}
