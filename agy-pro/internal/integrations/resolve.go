// Toolkit-root resolution for the integrations engine.
//
// Wave 1 made the agy-tools launchers self-healing: the installer writes a root
// marker (one absolute path, one line) and every wrapper re-resolves the
// repository through it at run time, so moving or renaming the clone no longer
// breaks the statusLine hook. scripts/lib/make-launchers.js:341-346 is the
// Windows side of that contract and scripts/install.sh:30 the POSIX side.
//
// The Go engine reads the SAME marker file. It does not invent a second
// convention and it does not shell out to the Node resolver: the file is the
// interface, and this file's job is to read it, validate it, and fail loudly
// with the reinstall hint when it is gone or stale.
//
// Ladder (first hit wins):
//
//  1. AGY_PRO_TOOLS_ROOT, when set. A value that does not validate is a loud
//     error, not a fall-through: silently ignoring an explicit override would
//     send the engine at a different tree than the operator asked for.
//  2. The root marker. The ladder has one rung per marker convention in the
//     wild, in priority order:
//       - Windows: <home>\.gemini\antigravity-cli\agy-root.txt
//         (scripts/lib/make-launchers.js MARKER_TAIL, written by the installer's
//         `marker` subcommand and read by agy-root-resolve.js)
//       - POSIX primary: <home>/.local/share/agy-tools-root
//         (scripts/install.sh ROOT_MARKER, written by install.sh)
//       - POSIX fallback: <home>/.gemini/antigravity-cli/agy-root.txt
//         (the same Node-written marker Windows uses; make-launchers.js runs on
//         every OS, so a POSIX machine that ran the Node helper instead of
//         install.sh has only this file)
//     The recorded directory is adopted only when it exists and contains
//     bin/agy-tokens.js, which is the entry the launchers themselves require.
//  3. A typed not-found error carrying the marker path(s) consulted and the
//     reinstall hint string.
//
// Home resolution is explicit (F-W1-17): paths.UserHome, i.e. os.UserHomeDir,
// which honors USERPROFILE on Windows and HOME elsewhere. Nothing here derives
// a path from the current working directory, the executable location, or a
// second environment variable, and the tests pin that with HOME and USERPROFILE
// pointed at two different temp directories.

package integrations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/paths"
)

// EnvToolkitRoot overrides the toolkit-root ladder with one absolute path.
const EnvToolkitRoot = "AGY_PRO_TOOLS_ROOT"

// ToolkitEntryScript is the file a directory must contain to be adopted as the
// toolkit root. It is the same entry the generated launchers run, so a root the
// markers accept is a root the statusline hook can execute.
const ToolkitEntryScript = "bin/agy-tokens.js"

// ToolkitReinstallHint is the sentence every resolution failure carries. It is
// exported so a CLI verb can print the same words without restating them.
const ToolkitReinstallHint = `re-run the installer from the toolkit repository: bash scripts/install.sh (POSIX) or scripts\install.bat (Windows)`

// Marker locations, kept in the exact per-OS spelling wave 1 landed:
//   - Windows: <home>\.gemini\antigravity-cli\agy-root.txt
//     (scripts/lib/make-launchers.js MARKER_TAIL, written by the installer's
//     `marker` subcommand and read by agy-root-resolve.js)
//   - POSIX:   <home>/.local/share/agy-tools-root
//     (scripts/install.sh ROOT_MARKER)
//
// The Windows spelling is ALSO the POSIX fallback: make-launchers.js writes that
// tail on every OS, so a POSIX machine that ran the Node helper rather than
// install.sh has no install.sh marker at all. install.sh stays the primary
// rung because it is the POSIX installer this product tells users to run.
const (
	windowsMarkerDir  = ".gemini/antigravity-cli"
	windowsMarkerName = "agy-root.txt"
	posixMarkerDir    = ".local/share"
	posixMarkerName   = "agy-tools-root"
)

// ToolkitRootSource names which rung of the ladder produced the root.
type ToolkitRootSource string

const (
	// ToolkitRootSourceEnv means AGY_PRO_TOOLS_ROOT supplied the root.
	ToolkitRootSourceEnv ToolkitRootSource = "env"
	// ToolkitRootSourceMarker means the root marker supplied the root.
	ToolkitRootSourceMarker ToolkitRootSource = "marker"
)

// ToolkitRootInfo is a resolved root plus where it came from, so a status line
// or a drift notice can say "from AGY_PRO_TOOLS_ROOT" instead of guessing.
type ToolkitRootInfo struct {
	// Path is the cleaned absolute toolkit repository root.
	Path string
	// Source is the rung that produced Path.
	Source ToolkitRootSource
	// Marker is the marker file Path was read from; empty when Source is env.
	Marker string
}

// ToolkitRoot resolves the toolkit root and returns just the path.
func ToolkitRoot() (string, error) {
	info, err := ResolveToolkitRoot()
	if err != nil {
		return "", err
	}
	return info.Path, nil
}

// ResolveToolkitRoot walks the ladder described in the package comment.
//
// AGY_PRO_TOOLS_ROOT that does not validate is ErrToolkitRootInvalid even when
// a marker would resolve: an ignored override is worse than a stopped run.
// Everything else that fails ends as ErrToolkitRootNotFound carrying the marker
// path(s) it consulted and the reinstall hint.
func ResolveToolkitRoot() (ToolkitRootInfo, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvToolkitRoot)); raw != "" {
		root, err := validateToolkitRoot(raw)
		if err != nil {
			return ToolkitRootInfo{}, &ToolkitRootError{
				Source:    ToolkitRootSourceEnv,
				Env:       EnvToolkitRoot,
				Candidate: raw,
				Detail:    err.Error(),
				Err:       ErrToolkitRootInvalid,
			}
		}
		return ToolkitRootInfo{Path: root, Source: ToolkitRootSourceEnv}, nil
	}

	markers, markerErr := resolveMarkerPaths()
	if markerErr != nil {
		return ToolkitRootInfo{}, &ToolkitRootError{
			Source: ToolkitRootSourceMarker,
			Detail: markerErr.Error(),
			Err:    ErrToolkitRootNotFound,
		}
	}

	details := make([]string, 0, len(markers))
	for _, marker := range markers {
		recorded, readErr := readMarker(marker)
		if readErr != nil {
			details = append(details, fmt.Sprintf("%s: %v", marker, readErr))
			continue
		}
		root, validateErr := validateToolkitRoot(recorded)
		if validateErr != nil {
			details = append(details, fmt.Sprintf("%s records %q: %v", marker, recorded, validateErr))
			continue
		}
		return ToolkitRootInfo{Path: root, Source: ToolkitRootSourceMarker, Marker: marker}, nil
	}

	return ToolkitRootInfo{}, &ToolkitRootError{
		Source:  ToolkitRootSourceMarker,
		Markers: markers,
		Detail:  strings.Join(details, "; "),
		Err:     ErrToolkitRootNotFound,
	}
}

// resolveMarkerPaths is the seam between the resolver and the per-host marker
// ladder. It exists so the POSIX two-rung ladder (install.sh marker, then the
// Node-written fallback) is exercised on a Windows host too; the same
// interception idiom the manifest store uses for its atomic writer.
var resolveMarkerPaths = ToolkitRootMarkerPaths

// ToolkitRootMarkerPath returns the FIRST (primary) root-marker path for the
// current OS. ToolkitRootMarkerPaths is the whole ladder.
func ToolkitRootMarkerPath() (string, error) {
	markers, err := ToolkitRootMarkerPaths()
	if err != nil {
		return "", err
	}
	return markers[0], nil
}

// ToolkitRootMarkerPaths returns every marker path consulted on the current OS,
// in ladder order. It is a slice because the ladder is a slice: the drift
// notice and the repair verb report the whole list, and a second marker
// convention per OS needs no signature change.
func ToolkitRootMarkerPaths() ([]string, error) {
	home, err := paths.UserHome()
	if err != nil {
		return nil, fmt.Errorf("toolkit root marker path: %w", err)
	}
	return markerPathsFor(runtime.GOOS, home), nil
}

// markerPathsFor renders the marker ladder for a named OS, so the per-OS
// spelling AND the ladder order are testable on any host. The separators are
// spelled out rather than produced by filepath.Join because the target OS is an
// argument, not the host.
//
// Windows has one rung (make-launchers.js is the only writer there). POSIX has
// two: install.sh's marker first, then the Node-written marker as a fallback.
func markerPathsFor(goos, home string) []string {
	primary := markerPathFor(goos, home)
	if isWindowsGOOS(goos) {
		return []string{primary}
	}
	return []string{primary, joinMarkerPath(home, windowsMarkerDir, windowsMarkerName, "/")}
}

// markerPathFor renders the primary (first-rung) marker path for a named OS.
func markerPathFor(goos, home string) string {
	if isWindowsGOOS(goos) {
		return joinMarkerPath(home, windowsMarkerDir, windowsMarkerName, `\`)
	}
	return joinMarkerPath(home, posixMarkerDir, posixMarkerName, "/")
}

// joinMarkerPath joins a home directory, a slash-separated marker directory,
// and a marker file name with the given separator, tolerating a home that ends
// in one.
func joinMarkerPath(home, markerDir, markerName, separator string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(home), `/\`)
	segments := append(strings.Split(markerDir, "/"), markerName)
	return trimmed + separator + strings.Join(segments, separator)
}

// isWindowsGOOS reports whether the GOOS name denotes Windows.
func isWindowsGOOS(goos string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(goos)), "windows")
}

// readMarker returns the first line of a marker file, trimmed. The Node side
// writes the root followed by CRLF (make-launchers.js writeMarkerAtomic), so a
// stray CR or a trailing newline must never become part of the path.
func readMarker(markerPath string) (string, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("marker file is absent")
		}
		return "", fmt.Errorf("marker file is unreadable: %v", err)
	}
	raw := strings.TrimPrefix(string(data), "\ufeff")
	if index := strings.IndexAny(raw, "\r\n"); index >= 0 {
		raw = raw[:index]
	}
	recorded := strings.TrimSpace(raw)
	if recorded == "" {
		return "", errors.New("marker file is empty")
	}
	return recorded, nil
}

// validateToolkitRoot checks that a candidate really is a toolkit repository
// root and returns its cleaned absolute form. "Exists" is not the test: the
// directory must carry the entry script the launchers run, because a marker
// left behind by a moved or deleted clone points at a directory that either
// does not exist or no longer holds this product.
func validateToolkitRoot(candidate string) (string, error) {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return "", errors.New("path is empty")
	}
	if isUNCPath(trimmed) {
		return "", fmt.Errorf("path %q is a UNC share", trimmed)
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("path %q is not absolute", trimmed)
	}
	clean := filepath.Clean(trimmed)
	info, err := os.Stat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path %q does not exist", clean)
		}
		return "", fmt.Errorf("path %q is unreadable: %v", clean, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", clean)
	}
	entry := filepath.Join(clean, "bin", "agy-tokens.js")
	entryInfo, err := os.Stat(entry)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%q does not contain %s", clean, ToolkitEntryScript)
		}
		return "", fmt.Errorf("%q: %s is unreadable: %v", clean, ToolkitEntryScript, err)
	}
	if entryInfo.IsDir() {
		return "", fmt.Errorf("%q: %s is a directory, not the agy-tokens entry script", clean, ToolkitEntryScript)
	}
	return clean, nil
}

// Error sentinels for the ladder.
var (
	// ErrToolkitRootNotFound means no rung produced a usable root.
	ErrToolkitRootNotFound = errors.New("agy-tools toolkit root not found")
	// ErrToolkitRootInvalid means an explicit override (AGY_PRO_TOOLS_ROOT) did
	// not name a usable root. It is distinct from not-found on purpose: with an
	// override set, falling back would hide the operator's mistake.
	ErrToolkitRootInvalid = errors.New("agy-tools toolkit root is not usable")
)

// ToolkitRootError reports why toolkit-root resolution failed. It carries the
// marker paths consulted, the rejected candidate, and the reinstall hint, so a
// caller can print it as-is and never has to reconstruct the context.
type ToolkitRootError struct {
	// Source is the rung that failed last.
	Source ToolkitRootSource
	// Env is the environment variable consulted, when Source is env.
	Env string
	// Candidate is the raw value the failed rung supplied.
	Candidate string
	// Markers lists the marker paths consulted, in ladder order.
	Markers []string
	// Detail is the precise reason the candidate was rejected.
	Detail string
	// Err is one of the package sentinels.
	Err error
}

// Error renders the failure, the context, and the fix in one sentence chain.
func (e *ToolkitRootError) Error() string {
	if e == nil {
		return "agy-tools toolkit root unresolved"
	}
	parts := make([]string, 0, 4)
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	} else {
		parts = append(parts, "agy-tools toolkit root unresolved")
	}
	if e.Source == ToolkitRootSourceEnv {
		parts = append(parts, fmt.Sprintf("%s=%q: %s", e.Env, e.Candidate, e.Detail))
	} else {
		if len(e.Markers) > 0 {
			parts = append(parts, "markers consulted: "+strings.Join(e.Markers, ", "))
		}
		if e.Detail != "" {
			parts = append(parts, e.Detail)
		}
	}
	parts = append(parts, "fix: "+ToolkitReinstallHint)
	return strings.Join(parts, "; ")
}

// Unwrap exposes the sentinel to errors.Is and errors.As.
func (e *ToolkitRootError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
