// Package wirecheck is the drift canary for the hybrid coexistence profiles
// (design §3, docs/261014_0006_session_native-coexistence-design/NATIVE_COEXISTENCE_DESIGN.md).
// agy auto-updates at launch, and both hybrid profiles depend on exact binary
// behaviors of the real agy binary, so before any hybrid session launches this
// package verifies that the RESOLVED binary (realagy.Resolve, never PATH -
// PATH's first `agy` on this machine is an 11-byte mock stub) still carries
// the required surface: the CLOUD_CODE_URL override and /v1internal route
// literals (profile P1), the customModels schemes and gemini-mode keys
// (profile P2), plus a `--version` pin. The GO/NOGO verdict gates the
// launcher: red means fall back to native with a clear notice, never a
// half-configured session. Leaf package: stdlib + internal/realagy.
package wirecheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/realagy"
)

// PinnedVersion is the agy version the canary table was verified against
// (agy.exe v1.3.5, design session 261014_0006). A different version sets
// Report.VersionDrift: it is recorded and surfaced, but the canaries alone
// decide GO/NOGO.
const PinnedVersion = "1.3.5"

// Verdict values returned by Report.Verdict.
const (
	VerdictGO   = "GO"
	VerdictNOGO = "NOGO"
)

// Canary is one binary-string probe for one hybrid profile. Literal is a
// public binary string (safe to print); Found records presence only, never
// the matched bytes.
type Canary struct {
	Profile string // ProfileP1 or ProfileP2
	Name    string // stable snake_case id used in reasons and render output
	Literal string // the exact byte string searched for in the binary
	Found   bool   // set by the scan
}

// Report is the outcome of one wirecheck run over the resolved real binary.
type Report struct {
	// BinaryPath is the absolute path of the resolved real-agy binary; empty
	// when resolution failed.
	BinaryPath string
	// Version is the parsed `<binary> --version` string; empty when the probe
	// failed or never ran.
	Version string
	// VersionDrift is true when the probe succeeded but reported a version
	// other than PinnedVersion. Drift never decides the verdict alone.
	VersionDrift bool
	// VersionErr is the `--version` probe failure (timeout, non-zero exit,
	// empty output). A hard fail forces NOGO.
	VersionErr string
	// ScanErr is why the binary surface could not be verified at all
	// (resolution or read failure). It forces NOGO because no canary was
	// evaluated.
	ScanErr string
	// Canaries is the evaluated canary table; empty when the scan never ran.
	Canaries []Canary
}

// maxBinaryBytes caps how much of the resolved binary is read into memory.
// agy.exe is ~193MB, so the cap only rejects pathological files, but it keeps
// a wrong resolution from exhausting memory before the scan starts.
var maxBinaryBytes int64 = 256 << 20

// Check is the production entry point: resolve the real agy binary, scan it
// for the canary table, and probe `--version` against PinnedVersion. It
// spawns nothing interactive and touches no network.
func Check() Report {
	return CheckWith(realagy.Resolve, readBinary, runVersionProbe)
}

// CheckWith is the injectable core of Check (same decomposition as
// realagy.ResolveWith): resolve locates the binary, readFile returns its
// bytes, runVersion probes `<path> --version`. Every nil seam fails closed to
// NOGO: a nil resolver or reader leaves nothing verified, and a nil version
// probe is recorded as an explicit probe failure (a report with zero version
// verification must never read GO). Tests inject fakes and temp files; the
// real binary and the network are never touched.
func CheckWith(
	resolve func() (string, error),
	readFile func(string) ([]byte, error),
	runVersion func(string) (string, error),
) Report {
	var r Report

	if resolve == nil {
		r.ScanErr = "real agy binary not resolved: no resolver"
		return r
	}
	path, err := resolve()
	if err != nil {
		r.ScanErr = fmt.Sprintf("real agy binary not resolved: %v", err)
		return r
	}
	if strings.TrimSpace(path) == "" {
		r.ScanErr = "real agy binary not resolved: resolver returned an empty path"
		return r
	}
	// FIX-14b: pin the resolved path to its absolute form BEFORE the read and
	// the exec, so both operate on the same file. A bare-name resolution would
	// otherwise reach Go's LookPath at exec time and could run a different
	// binary than the one scanned. A failure to absolutize stays NOGO.
	absPath, absErr := filepath.Abs(path)
	if absErr != nil {
		r.ScanErr = fmt.Sprintf("real agy binary not resolved: %v", absErr)
		return r
	}
	r.BinaryPath = absPath

	if readFile != nil {
		data, readErr := readFile(absPath)
		switch {
		case readErr != nil:
			r.ScanErr = fmt.Sprintf("real agy binary not readable: %v", readErr)
		case int64(len(data)) > maxBinaryBytes:
			r.ScanErr = fmt.Sprintf("real agy binary over the %d-byte scan cap: read %d bytes", maxBinaryBytes, len(data))
		default:
			r.Canaries = scanCanaries(data)
		}
	}

	if runVersion == nil {
		// Fail closed (FIX-8): a skipped probe is a failed probe. Without it a
		// canary-clean report would read GO with zero version verification.
		r.VersionErr = "version probe skipped: no probe seam"
	} else {
		version, versionErr := runVersion(absPath)
		switch {
		case versionErr != nil:
			r.VersionErr = versionErr.Error()
		case strings.TrimSpace(version) == "":
			r.VersionErr = "version probe returned an empty version"
		default:
			r.Version = version
			r.VersionDrift = version != PinnedVersion
		}
	}

	return r
}

// Verdict is the launcher gate: GO when every canary of at least one profile
// is Found and the version probe did not hard-fail, NOGO otherwise. Version
// drift alone never flips the verdict; the canaries decide.
func (r Report) Verdict() string {
	if r.VersionErr != "" {
		return VerdictNOGO
	}
	for _, profile := range profileOrder() {
		if r.ProfileOK(profile) {
			return VerdictGO
		}
	}
	return VerdictNOGO
}

// ProfileOK reports whether every canary of the named profile was found. A
// profile with no canaries in the report, or an unknown name, is not OK.
func (r Report) ProfileOK(profile string) bool {
	want := normalizeProfile(profile)
	if want == "" {
		return false
	}
	total := 0
	for _, c := range r.Canaries {
		if normalizeProfile(c.Profile) != want {
			continue
		}
		total++
		if !c.Found {
			return false
		}
	}
	return total > 0
}

// Reasons lists the human-readable NOGO and DRIFT causes: why the binary
// surface could not be verified, which profile canaries are missing (only
// when the verdict is NOGO), a failed version probe, and version drift.
// Canary literals are public binary strings, so they are safe to print;
// nothing else about the binary is echoed. A clean GO with no drift returns
// nil; a GO with one incomplete profile reports only drift, because the
// per-profile gap is carried by ProfileOK, not by a GO reason list.
func (r Report) Reasons() []string {
	var reasons []string
	if r.ScanErr != "" {
		reasons = append(reasons, r.ScanErr)
	}
	if r.ScanErr == "" && r.Verdict() == VerdictNOGO {
		for _, profile := range profileOrder() {
			if r.ProfileOK(profile) {
				continue
			}
			var missing []string
			for _, c := range r.Canaries {
				if normalizeProfile(c.Profile) == profile && !c.Found {
					missing = append(missing, fmt.Sprintf("%s (%q)", c.Name, c.Literal))
				}
			}
			if len(missing) == 0 {
				reasons = append(reasons, fmt.Sprintf("profile %s incomplete: no canaries verified", profile))
				continue
			}
			reasons = append(reasons, fmt.Sprintf("profile %s incomplete: missing %s", profile, strings.Join(missing, ", ")))
		}
	}
	if r.VersionErr != "" {
		reasons = append(reasons, "version probe failed: "+r.VersionErr)
	}
	if r.VersionDrift {
		reasons = append(reasons, fmt.Sprintf("version drift: binary reports %q, pinned %q", r.Version, PinnedVersion))
	}
	return reasons
}

// canariesFor returns the report's canaries for one profile, in table order.
func (r Report) canariesFor(profile string) []Canary {
	want := normalizeProfile(profile)
	var out []Canary
	for _, c := range r.Canaries {
		if normalizeProfile(c.Profile) == want {
			out = append(out, c)
		}
	}
	return out
}

// readBinary is the production readFile seam: stat first so an oversized
// binary is rejected without reading it, then read the bytes.
func readBinary(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a binary", path)
	}
	if fi.Size() > maxBinaryBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte scan cap", path, fi.Size(), maxBinaryBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}
