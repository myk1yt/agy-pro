// The settings door: agy-pro's ownership of the global settings.json statusLine
// slot (P5-3 D5, docs/261022_0002_session_agy-pro-session8/pick-record-p53.md
// items 4, 6 and 8).
//
// Node's scripts/lib/configure-statusline.js owned this slot until this unit; it
// now refuses to write and points at the verb that will. Three rules shape the
// code below.
//
//   - THE LEDGER, NOT THE DOCUMENT. Every read and write goes through
//     agypro/internal/agysettings (OpenGlobal, Set, Delete, Write). The door
//     never edits settings.json itself, so unmanaged top-level keys
//     (trustedWorkspaces, colorScheme, ...) pass through byte for byte and a
//     statusLine object this build does not understand is surfaced as a repair
//     decision instead of being normalized away.
//   - THE GUARD STAYS. OpenGlobal is the only opener used here; the path guard
//     in agysettings is untouched (pick 4). The deliberate override is visible
//     twice: in the agysettings warning and in the consent gate the CLI/TUI
//     doors put in front of ApplyStatusLine (T8).
//   - NEVER DELETE, NEVER AUTO-FIX. A write backs the previous bytes up through
//     the ledger's settings.json.bak.<ts> chain, a retract backs them up too, and
//     a rollback archives the bytes it replaces before restoring a snapshot.
//     Drift between plan and apply refuses the run (stale base) instead of
//     writing over whatever landed.
//
// The statusLine command is the absolute stable wrapper (pick 6): a bare PATH
// name breaks when the PATH entry moves, and wave 1 already made the launchers
// self-healing through the root marker.
package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"agypro/internal/agysettings"
	"agypro/internal/paths"
)

// statusLineCommandArgs is the argv tail the statusLine command runs. It is the
// command line Node's writer baked in (scripts/lib/configure-statusline.js,
// DEFAULT_COMMAND = "agy-tokens --hook --raw --write-dashboard"), with the
// absolute launcher in front (pick 6).
var statusLineCommandArgs = []string{"--hook", "--raw", "--write-dashboard"}

// StatusLineHookArgs returns the argv tail as a copy, so a caller cannot mutate
// the command line the door builds.
func StatusLineHookArgs() []string { return append([]string(nil), statusLineCommandArgs...) }

// Launcher locations. Windows: scripts/install.bat sets
// USER_BIN=%USERPROFILE%\.gemini\antigravity-cli\bin and make-launchers.js
// writes agy-tokens.bat there. POSIX: scripts/install.sh sets
// USER_BIN="$HOME/.local/bin" and writes the agy-tokens wrapper there. The
// antigravity-cli/bin directory is ALSO the POSIX fallback because
// make-launchers.js runs on every OS and writes its .bat shims beside the root
// marker.
const (
	launcherRelAntigravityBin = ".gemini/antigravity-cli/bin"
	launcherRelPosixUserBin   = ".local/bin"

	// launcherShapeWindows and launcherShapePosix are what a diagnostic names
	// instead of a resolved absolute path, the way agysettings names the
	// documented settings shape instead of the user's directory.
	launcherShapeWindows = `~\.gemini\antigravity-cli\bin\agy-tokens.bat`
	launcherShapePosix   = `~/.local/bin/agy-tokens`

	// settingsDoorShape is the documented shape of the file this door owns. It is
	// the same shape agysettings uses, restated here so this package's sentences
	// never spell a resolved user path.
	settingsDoorShape = "~/.gemini/antigravity-cli/settings.json"
)

// statusLineLauncherNames lists the launcher file names checked per OS, in
// priority order. A .cmd or .exe launcher is accepted on Windows because an npm
// install of the package creates those, and refusing them would make the door
// unusable on a machine that never ran the .bat installer.
func statusLineLauncherNames(goos string) []string {
	if isWindowsGOOS(goos) {
		return []string{"agy-tokens.bat", "agy-tokens.cmd", "agy-tokens.exe"}
	}
	return []string{"agy-tokens"}
}

// launcherDirsFor renders the launcher directories consulted for a named OS, in
// priority order. The separators are spelled through filepath.Join because the
// host is the thing that runs the command; only the ladder is per-OS.
func launcherDirsFor(goos, home string) []string {
	antigravity := filepath.Join(home, filepath.FromSlash(launcherRelAntigravityBin))
	if isWindowsGOOS(goos) {
		return []string{antigravity}
	}
	return []string{
		filepath.Join(home, filepath.FromSlash(launcherRelPosixUserBin)),
		antigravity,
	}
}

// launcherShapeFor renders the documented launcher location for a named OS.
func launcherShapeFor(goos string) string {
	if isWindowsGOOS(goos) {
		return launcherShapeWindows
	}
	return launcherShapePosix
}

// StatusLineLauncherCandidates returns every absolute launcher path the door
// checks, in priority order. A test pins the order; a diagnostic prints the
// list when none of them exists.
func StatusLineLauncherCandidates() ([]string, error) {
	home, err := paths.UserHome()
	if err != nil {
		return nil, fmt.Errorf("statusLine launcher: %w", err)
	}
	dirs := launcherDirsFor(runtimeGOOS, home)
	names := statusLineLauncherNames(runtimeGOOS)
	out := make([]string, 0, len(dirs)*len(names))
	for _, dir := range dirs {
		for _, name := range names {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}

// runtimeGOOS is the OS whose launcher ladder is used. It is a package seam so a
// test can exercise the POSIX ladder on a Windows host without a second binary
// (the same interception idiom resolve.go uses for its marker ladder).
var runtimeGOOS = hostGOOS()

// hostGOOS returns the host OS name in the form isWindowsGOOS understands.
func hostGOOS() string { return runtime.GOOS }

// ResolveStatusLineLauncher returns the first launcher that exists, or a typed
// StatusLineLauncherError naming the shape and the fix.
//
// A launcher is required before anything is written: the command the door bakes
// into settings.json is executed by agy at every launch, so writing one that
// does not resolve would break the user's status line on the next start.
func ResolveStatusLineLauncher() (string, error) {
	candidates, err := StatusLineLauncherCandidates()
	if err != nil {
		return "", err
	}
	for _, candidate := range candidates {
		info, statErr := os.Stat(candidate)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return "", &StatusLineLauncherError{
				Candidates: candidates,
				Shape:      launcherShapeFor(runtimeGOOS),
				Detail:     "a launcher path is unreadable",
				Err:        ErrStatusLineLauncherMissing,
			}
		}
		if info.IsDir() {
			continue
		}
		return candidate, nil
	}
	return "", &StatusLineLauncherError{
		Candidates: candidates,
		Shape:      launcherShapeFor(runtimeGOOS),
		Detail:     "no launcher exists at any consulted path",
		Err:        ErrStatusLineLauncherMissing,
	}
}

// commandForLauncher builds the statusLine command line: the absolute launcher,
// quoted when the path needs it, followed by the hook arguments.
func commandForLauncher(launcher string) string {
	parts := make([]string, 0, len(statusLineCommandArgs)+1)
	parts = append(parts, quoteLauncher(launcher))
	parts = append(parts, statusLineCommandArgs...)
	return strings.Join(parts, " ")
}

// quoteLauncher renders a launcher path for the command line agy runs.
//
// A path with no shell-significant character is emitted bare, so the common case
// stays readable. A Windows path with a space is double-quoted (cmd.exe treats
// the quotes as grouping); a POSIX path with anything outside the safe set is
// single-quoted, with an embedded single quote closed and re-opened the standard
// '\” way.
func quoteLauncher(launcher string) string {
	if launcher == "" {
		return launcher
	}
	if isWindowsGOOS(runtimeGOOS) {
		if strings.ContainsAny(launcher, " \t&()^") {
			return `"` + launcher + `"`
		}
		return launcher
	}
	if isPOSIXSafeWord(launcher) {
		return launcher
	}
	return "'" + strings.ReplaceAll(launcher, "'", `'\''`) + "'"
}

// isPOSIXSafeWord reports whether s survives an unquoted POSIX shell.
func isPOSIXSafeWord(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/', r == '.', r == '_', r == '-', r == '+', r == ':', r == '=', r == '@', r == '%':
		default:
			return false
		}
	}
	return s != ""
}

// StatusLineLauncherError reports that no launcher exists where the installers
// put one.
type StatusLineLauncherError struct {
	// Candidates are the absolute launcher paths consulted, in priority order.
	Candidates []string
	// Shape is the documented launcher location for this OS.
	Shape string
	// ToolkitRoot is the resolved toolkit root, when one was supplied.
	ToolkitRoot string
	// ToolkitIssue is why the toolkit root itself is unusable, when it is.
	ToolkitIssue string
	// Detail is the precise reason the ladder failed.
	Detail string
	// Err is ErrStatusLineLauncherMissing.
	Err error
}

// Error renders the failure, the toolkit diagnosis, and the fix.
func (e *StatusLineLauncherError) Error() string {
	if e == nil {
		return ErrStatusLineLauncherMissing.Error()
	}
	parts := []string{
		fmt.Sprintf("%s: no launcher at %s (or any other consulted location)",
			ErrStatusLineLauncherMissing.Error(), e.Shape),
	}
	if e.ToolkitIssue != "" {
		parts = append(parts, e.ToolkitIssue)
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	parts = append(parts, "the statusLine command would fail at every agy launch, so nothing was written; fix: "+ToolkitReinstallHint)
	return strings.Join(parts, "; ")
}

// Unwrap exposes the sentinel to errors.Is and errors.As.
func (e *StatusLineLauncherError) Unwrap() error { return e.Err }

// StatusLineForeignError reports a settings.json this door refuses to touch: the
// document does not parse, or its statusLine slot is not the object the ledger
// declares (an undeclared field, a missing required slot, or a non-object).
//
// It is a repair decision, not a retry: the ledger refuses the document, and
// normalizing it silently would either lose a field agy reads or write over an
// object the user authored.
type StatusLineForeignError struct {
	// Shape is the documented settings path, not the resolved one.
	Shape string
	// Detail is the ledger's own explanation, which names the offending field.
	Detail string
	// Repair is the recommended decision.
	Repair string
	// Err is ErrStatusLineForeign.
	Err error
}

// Error renders the refusal with the reason and the repair decision.
func (e *StatusLineForeignError) Error() string {
	if e == nil {
		return ErrStatusLineForeign.Error()
	}
	detail := e.Detail
	if detail == "" {
		detail = "the document is not one this build can read"
	}
	return fmt.Sprintf("%s: %s: %s; %s", ErrStatusLineForeign.Error(), e.Shape, detail, statusLineRepairAdvice)
}

// Unwrap exposes the sentinel to errors.Is and errors.As.
func (e *StatusLineForeignError) Unwrap() error { return e.Err }

// statusLineRepairAdvice is the sentence a foreign document carries. It names the
// decision, not a command that does not exist yet (the repair verb is T8's).
const statusLineRepairAdvice = "repair decision needed: back up or fix statusLine by hand, then re-run the plan; agy-pro will not rewrite an object it does not understand"

// StatusLineStaleError reports that settings.json changed between the plan and
// the write. Nothing is written when it is returned.
type StatusLineStaleError struct {
	// Shape is the documented settings path.
	Shape string
	// WantSHA is the digest the plan read; empty means the file was absent.
	WantSHA string
	// LiveSHA is the digest found at apply time; empty means the file is gone.
	LiveSHA string
}

// Error names what the plan expected, what it found, and what to do next.
func (e *StatusLineStaleError) Error() string {
	if e == nil {
		return ErrStatusLineStale.Error()
	}
	want := e.WantSHA
	if want == "" {
		want = "absent"
	}
	live := e.LiveSHA
	if live == "" {
		live = "absent"
	}
	return fmt.Sprintf("%s: %s changed after the plan was built (expected %s, found %s); nothing was written, re-run the plan",
		ErrStatusLineStale.Error(), settingsDoorShape, want, live)
}

// Unwrap exposes the sentinel to errors.Is.
func (e *StatusLineStaleError) Unwrap() error { return ErrStatusLineStale }

// StatusLineAction is what the door does to the statusLine slot.
type StatusLineAction string

const (
	// StatusLineActionCreate means the slot is absent and the door adds it.
	StatusLineActionCreate StatusLineAction = "create"
	// StatusLineActionUpdate means the slot exists with a different object and
	// the door replaces it.
	StatusLineActionUpdate StatusLineAction = "update"
	// StatusLineActionAdopt means the slot already holds exactly the object the
	// door writes: the bookkeeping starts recording it, no byte is touched.
	StatusLineActionAdopt StatusLineAction = "adopt"
	// StatusLineActionIdentical means the slot holds the door's object and the
	// manifest already records it: nothing to do.
	StatusLineActionIdentical StatusLineAction = "identical"
	// StatusLineActionRetract means the slot is removed (the bookkeeping entry
	// goes with it).
	StatusLineActionRetract StatusLineAction = "retract"
)

// knownStatusLineAction reports whether action is one this build performs.
func knownStatusLineAction(action StatusLineAction) bool {
	switch action {
	case StatusLineActionCreate, StatusLineActionUpdate, StatusLineActionAdopt,
		StatusLineActionIdentical, StatusLineActionRetract:
		return true
	default:
		return false
	}
}

// StatusLineDoor is one plan against the global settings.json statusLine slot,
// together with the live state it was classified from.
//
// It is the settings-door counterpart of DeployPlan: a plan is self-contained
// enough to render as an exact diff (diff.go) and to be applied, and the digest
// it carries is what ApplyStatusLine re-verifies before writing.
type StatusLineDoor struct {
	// SettingsPath is the absolute global settings.json the door addresses.
	SettingsPath string
	// Launcher is the absolute launcher the command runs.
	Launcher string
	// ToolkitRoot is the toolkit root the launcher belongs to, when one resolved.
	ToolkitRoot string
	// Command is the whole command line the door writes.
	Command string
	// Desired is the statusLine object the door writes.
	Desired agysettings.StatusLineConfig
	// Current is the object found on disk; nil when the slot is absent.
	Current *agysettings.StatusLineConfig
	// Action is what ApplyStatusLine does.
	Action StatusLineAction
	// BaseSHA is the digest of settings.json at plan time; empty when the file
	// was absent.
	BaseSHA string
	// Recorded reports that the manifest already claims SettingsPath with
	// BaseSHA, i.e. the file is this engine's own last write.
	Recorded bool
	// UnmanagedKeys are the top-level keys the ledger does not manage. They are
	// carried through byte for byte; the diff says so.
	UnmanagedKeys []string
}

// StatusLineSummary is what one statusLine verb did.
type StatusLineSummary struct {
	// Action is the verb that ran.
	Action StatusLineAction
	// SettingsPath is the file the verb addressed.
	SettingsPath string
	// Command is the command line the door holds, when it holds one.
	Command string
	// Previous is the object found before the run; nil when the slot was absent.
	Previous *agysettings.StatusLineConfig
	// LiveSHA is the digest of settings.json after the run; empty when the file
	// is absent.
	LiveSHA string
	// BackupPath is the archive this run created (the ledger's
	// settings.json.bak.<ts>); empty when nothing was overwritten.
	BackupPath string
	// Provenance is the provenance recorded for the file, when the run recorded
	// one.
	Provenance Provenance
	// Recorded reports whether the manifest now claims settings.json.
	Recorded bool
	// ManifestUpdated reports whether the bookkeeping was rewritten.
	ManifestUpdated bool
	// Detail is a short operator sentence (a no-op explanation). The user-facing
	// sentence is T8's to compose.
	Detail string
}

// StatusLinePlan builds the settings-door plan for the current machine.
//
// toolkitRoot is the marker-resolved toolkit root, used only for the repair
// diagnosis of a missing launcher: "" means "resolve the ladder, and carry on
// when it does not resolve" (a machine whose marker is gone must still reach the
// launcher check, which is the failure that actually breaks the status line).
//
// The plan reads; it never writes. It fails closed on a foreign statusLine
// object (StatusLineForeignError) and on a manifest it cannot read, because a
// plan built on unreadable bookkeeping would misreport adopt as create.
//
// The document is read before the launcher is resolved on purpose: a settings
// file this build cannot parse is the blocker the user has to clear first, and
// reporting a missing launcher instead would send them to the installer for a
// problem the installer cannot fix.
func StatusLinePlan(toolkitRoot string) (*StatusLineDoor, error) {
	state, err := readStatusLineFile()
	if err != nil {
		return nil, err
	}

	launcher, err := ResolveStatusLineLauncher()
	if err != nil {
		var launcherErr *StatusLineLauncherError
		if errors.As(err, &launcherErr) {
			launcherErr.ToolkitRoot, launcherErr.ToolkitIssue = toolkitDiagnosis(toolkitRoot)
		}
		return nil, err
	}
	root, _ := toolkitDiagnosis(toolkitRoot)

	door := &StatusLineDoor{
		SettingsPath:  state.Path,
		Launcher:      launcher,
		ToolkitRoot:   root,
		Command:       commandForLauncher(launcher),
		Desired:       *agysettings.NewStatusLine(commandForLauncher(launcher)),
		Current:       state.Current,
		BaseSHA:       state.SHA,
		UnmanagedKeys: state.Unmanaged,
	}

	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	entry, recorded := manifest.Entry(door.SettingsPath)
	door.Recorded = recorded && entry.SHA256 == door.BaseSHA

	switch {
	case door.Current == nil:
		door.Action = StatusLineActionCreate
	case sameStatusLineObject(door.Current, &door.Desired):
		if door.Recorded {
			door.Action = StatusLineActionIdentical
		} else {
			door.Action = StatusLineActionAdopt
		}
	default:
		door.Action = StatusLineActionUpdate
	}
	return door, nil
}

// toolkitDiagnosis resolves the toolkit root for a diagnostic and reports why it
// is unusable when it is. It never spells a repository path into a sentence.
func toolkitDiagnosis(toolkitRoot string) (string, string) {
	trimmed := strings.TrimSpace(toolkitRoot)
	if trimmed == "" {
		info, err := ResolveToolkitRoot()
		if err != nil {
			return "", "the toolkit root does not resolve either (no marker and no AGY_PRO_TOOLS_ROOT)"
		}
		return info.Path, ""
	}
	root, err := validateToolkitRoot(trimmed)
	if err != nil {
		return "", "the toolkit root supplied by the caller does not carry " + ToolkitEntryScript
	}
	return root, ""
}

// ApplyStatusLine performs what the plan describes.
//
// Order of operations, deliberately (the deployment engine's shape, deploy.go
// Apply): load the bookkeeping first, re-verify the base digest the plan read,
// and only then write. A refused apply therefore leaves settings.json exactly as
// it was.
func ApplyStatusLine(door *StatusLineDoor) (*StatusLineSummary, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	if err := validateStatusLineDoor(door); err != nil {
		return nil, err
	}

	liveSHA, err := settingsFileSHA(door.SettingsPath)
	if err != nil {
		return nil, err
	}
	if liveSHA != door.BaseSHA {
		return nil, &StatusLineStaleError{Shape: settingsDoorShape, WantSHA: door.BaseSHA, LiveSHA: liveSHA}
	}

	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}

	summary := &StatusLineSummary{
		Action:       door.Action,
		SettingsPath: door.SettingsPath,
		Command:      door.Command,
		Previous:     door.Current,
		LiveSHA:      liveSHA,
	}

	switch door.Action {
	case StatusLineActionIdentical:
		// Files and bookkeeping already agree: an identical plan writes nothing
		// and does not touch the manifest.
		summary.Recorded = true
		return summary, nil
	case StatusLineActionAdopt:
		// The object on disk is already the door's object, so the bytes were not
		// written by this run: record-only adopt (pick 8).
		return recordStatusLineAdopt(manifest, door.SettingsPath, liveSHA, ProvenanceAdopted, summary)
	case StatusLineActionCreate, StatusLineActionUpdate:
		backup, writeErr := writeStatusLineSlot(door.SettingsPath, &door.Desired)
		if writeErr != nil {
			return summary, writeErr
		}
		summary.BackupPath = backup
		written, digestErr := settingsFileSHA(door.SettingsPath)
		if digestErr != nil {
			return summary, digestErr
		}
		if written == "" {
			return summary, fmt.Errorf("%s is missing after the statusLine write", settingsDoorShape)
		}
		summary.LiveSHA = written
		entry := ManifestEntry{
			Path:       door.SettingsPath,
			SHA256:     written,
			Provenance: ProvenanceGoApplied,
			BackupRef:  backup,
		}
		if err := manifest.Set(entry); err != nil {
			return summary, err
		}
		if err := SaveManifest(manifest); err != nil {
			return summary, fmt.Errorf("settings.json was written but the manifest was not saved: %w", err)
		}
		summary.Provenance = ProvenanceGoApplied
		summary.Recorded = true
		summary.ManifestUpdated = true
		return summary, nil
	default:
		return nil, fmt.Errorf("%w: action %q", ErrStatusLineDoorInvalid, string(door.Action))
	}
}

// AdoptStatusLine records the statusLine slot that is already on disk without
// writing a byte of settings.json (pick 8: adopt-current with provenance).
//
// It needs no launcher: adoption is exactly the step a machine with a broken
// install still has to be able to take. The provenance comes from the command
// form, because that is the evidence the file itself carries: the wrapper form
// (an absolute path first) is this door's own, the bare PATH form is the Node
// writer's (configure-statusline.js DEFAULT_COMMAND).
func AdoptStatusLine() (*StatusLineSummary, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	state, err := readStatusLineFile()
	if err != nil {
		return nil, err
	}
	summary := &StatusLineSummary{
		Action:       StatusLineActionAdopt,
		SettingsPath: state.Path,
		Previous:     state.Current,
		LiveSHA:      state.SHA,
	}
	if state.Current == nil {
		// Nothing statusLine-shaped on disk. There is nothing to adopt: adding the
		// slot is a create plan, and recording the file as somebody's work would
		// claim bytes this engine never described.
		summary.Detail = "no statusLine slot to adopt; run the apply plan to add one"
		return summary, nil
	}

	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	provenance := ProvenanceNodeEra
	if launcher := commandLauncher(state.Current.Command); isAbsoluteLauncherPath(launcher) {
		provenance = ProvenanceAdopted
	}
	return recordStatusLineAdopt(manifest, state.Path, state.SHA, provenance, summary)
}

// recordStatusLineAdopt writes the manifest entry for a settings.json whose bytes
// this run did not touch.
func recordStatusLineAdopt(manifest *Manifest, settingsPath, sha string, provenance Provenance, summary *StatusLineSummary) (*StatusLineSummary, error) {
	if sha == "" {
		summary.Detail = "settings.json is absent; there is nothing to adopt"
		return summary, nil
	}
	entry := ManifestEntry{Path: settingsPath, SHA256: sha, Provenance: provenance}
	if err := manifest.Set(entry); err != nil {
		return summary, err
	}
	if err := SaveManifest(manifest); err != nil {
		return summary, err
	}
	summary.Provenance = provenance
	summary.Recorded = true
	summary.ManifestUpdated = true
	return summary, nil
}

// RetractStatusLine removes the statusLine slot through the ledger and drops the
// bookkeeping entry.
//
// It is not a delete: the ledger archives the previous bytes into its own
// settings.json.bak.<ts> chain before the atomic write, and an absent slot is a
// no-op that writes nothing at all.
func RetractStatusLine() (*StatusLineSummary, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	state, err := readStatusLineFile()
	if err != nil {
		return nil, err
	}
	summary := &StatusLineSummary{
		Action:       StatusLineActionRetract,
		SettingsPath: state.Path,
		Previous:     state.Current,
		LiveSHA:      state.SHA,
	}

	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}

	if state.Current != nil {
		backup, writeErr := writeStatusLineSlot(state.Path, nil)
		if writeErr != nil {
			return summary, writeErr
		}
		summary.BackupPath = backup
		liveSHA, digestErr := settingsFileSHA(state.Path)
		if digestErr != nil {
			return summary, digestErr
		}
		summary.LiveSHA = liveSHA
	} else {
		summary.Detail = "no statusLine slot to retract"
	}

	if manifest.Delete(state.Path) {
		if err := SaveManifest(manifest); err != nil {
			return summary, fmt.Errorf("statusLine was retracted but the manifest was not saved: %w", err)
		}
		summary.ManifestUpdated = true
	}
	return summary, nil
}

// SettingsSnapshotScheme names which writer's backup chain a snapshot came from.
type SettingsSnapshotScheme string

const (
	// SettingsSnapshotNode is Node's configure-statusline.js naming:
	// settings.bak.YYYYMMDD-HHMMSS.json (scripts/lib/configure-statusline.js).
	SettingsSnapshotNode SettingsSnapshotScheme = "node"
	// SettingsSnapshotLedger is agysettings' naming:
	// settings.json.bak.YYYYMMDD-HHMMSS, with a -2, -3 collision suffix.
	SettingsSnapshotLedger SettingsSnapshotScheme = "ledger"
)

// Snapshot name patterns, anchored so neither chain can claim the other's file
// and no unrelated settings.*.json file is ever restored.
var (
	nodeSnapshotPattern   = regexp.MustCompile(`^settings\.bak\.(\d{8}-\d{6})\.json$`)
	ledgerSnapshotPattern = regexp.MustCompile(`^settings\.json\.bak\.(\d{8}-\d{6})(?:-(\d+))?$`)

	// snapshotTimestampLayout is the layout both writers stamp, in local time.
	snapshotTimestampLayout = "20060102-150405"
)

// SettingsSnapshot is one archived copy of settings.json.
type SettingsSnapshot struct {
	// Path is the absolute snapshot path.
	Path string
	// Scheme is the writer whose chain produced it.
	Scheme SettingsSnapshotScheme
	// Taken is the timestamp parsed out of the name.
	Taken time.Time
	// Index is the collision suffix (-2, -3, ...), 1 for the primary name.
	Index int
}

// SettingsSnapshots returns every settings.json snapshot for the global file,
// newest first.
func SettingsSnapshots() ([]SettingsSnapshot, error) {
	settingsPath, err := agysettings.GlobalSettingsPath()
	if err != nil {
		return nil, err
	}
	return settingsSnapshotsFor(settingsPath)
}

// settingsSnapshotsFor lists the snapshots beside one settings file, newest
// first. A directory that does not exist has no snapshots; that is not an error.
func settingsSnapshotsFor(settingsPath string) ([]SettingsSnapshot, error) {
	dir := filepath.Dir(settingsPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot inspect the settings directory: %w", err)
	}
	out := make([]SettingsSnapshot, 0, 4)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if match := nodeSnapshotPattern.FindStringSubmatch(name); match != nil {
			taken, timeErr := time.ParseInLocation(snapshotTimestampLayout, match[1], time.Local)
			if timeErr != nil {
				continue
			}
			out = append(out, SettingsSnapshot{
				Path:   filepath.Join(dir, name),
				Scheme: SettingsSnapshotNode,
				Taken:  taken,
				Index:  1,
			})
			continue
		}
		if match := ledgerSnapshotPattern.FindStringSubmatch(name); match != nil {
			taken, timeErr := time.ParseInLocation(snapshotTimestampLayout, match[1], time.Local)
			if timeErr != nil {
				continue
			}
			index := 1
			if match[2] != "" {
				parsed, convErr := strconv.Atoi(match[2])
				if convErr != nil {
					continue
				}
				index = parsed
			}
			out = append(out, SettingsSnapshot{
				Path:   filepath.Join(dir, name),
				Scheme: SettingsSnapshotLedger,
				Taken:  taken,
				Index:  index,
			})
		}
	}
	sortSnapshotsNewestFirst(out)
	return out, nil
}

// sortSnapshotsNewestFirst orders snapshots by the timestamp in the name, then
// by the collision suffix, then by chain, then by path, so a rollback is
// deterministic even when two chains stamped the same second.
//
// The chain tie-break is explicit rather than alphabetical: for one timestamp the
// Node-era archive is the older snapshot (the ledger only writes at apply time, so
// its archive was taken later), and restoring the older of the two is the
// conservative direction.
func sortSnapshotsNewestFirst(snapshots []SettingsSnapshot) {
	sort.SliceStable(snapshots, func(i, j int) bool {
		a, b := snapshots[i], snapshots[j]
		if !a.Taken.Equal(b.Taken) {
			return a.Taken.After(b.Taken)
		}
		if a.Index != b.Index {
			return a.Index > b.Index
		}
		if a.Scheme != b.Scheme {
			return a.Scheme == SettingsSnapshotNode
		}
		return a.Path > b.Path
	})
}

// StatusLineRollback is what one rollback did.
type StatusLineRollback struct {
	// SettingsPath is the file that was restored.
	SettingsPath string
	// Snapshot is the archive the bytes came from.
	Snapshot string
	// Scheme is the chain that archive belongs to.
	Scheme SettingsSnapshotScheme
	// Taken is the snapshot's timestamp.
	Taken time.Time
	// PreRestoreArchive is where the bytes that were live before the restore
	// were archived; empty when there was nothing to archive.
	PreRestoreArchive string
	// LiveSHA is the digest of settings.json after the restore.
	LiveSHA string
	// Skipped lists the newer archives that were not coherent JSON and were
	// therefore not restored.
	Skipped []string
	// ManifestUpdated reports whether the bookkeeping entry was dropped.
	ManifestUpdated bool
}

// RollbackStatusLine restores the newest coherent settings.json snapshot,
// byte for byte, from either writer's backup chain.
//
// Nothing is destroyed: the bytes that were live before the restore are archived
// into the ledger's own chain first, and the snapshot itself is copied, never
// moved. An archive that does not parse as a JSON object is skipped rather than
// restored (a torn snapshot would brick settings.json), and the skip is reported.
//
// The bookkeeping entry is dropped rather than re-recorded: after a rollback the
// file holds bytes this engine did not write, and claiming them would make the
// next drift report a lie. A follow-up AdoptStatusLine records them honestly.
func RollbackStatusLine() (*StatusLineRollback, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	settingsPath, err := agysettings.GlobalSettingsPath()
	if err != nil {
		return nil, err
	}
	snapshots, err := settingsSnapshotsFor(settingsPath)
	if err != nil {
		return nil, err
	}

	result := &StatusLineRollback{SettingsPath: settingsPath}
	var restore *SettingsSnapshot
	for i := range snapshots {
		snapshot := snapshots[i]
		data, readErr := os.ReadFile(snapshot.Path)
		if readErr != nil {
			result.Skipped = append(result.Skipped, snapshot.Path)
			continue
		}
		if !coherentSettingsSnapshot(data) {
			result.Skipped = append(result.Skipped, snapshot.Path)
			continue
		}
		restore = &snapshot
		break
	}
	if restore == nil {
		return result, fmt.Errorf("%w: no settings.bak.<ts>.json (node) or settings.json.bak.<ts> (ledger) archive beside %s holds valid JSON",
			ErrStatusLineNoBackup, settingsDoorShape)
	}

	// Load the bookkeeping before touching the file, so a corrupt manifest stops
	// the run with settings.json untouched.
	manifest, err := LoadManifest()
	if err != nil {
		return result, err
	}

	data, err := os.ReadFile(restore.Path)
	if err != nil {
		return result, err
	}
	if current, _, readErr := readIfPresent(settingsPath); readErr != nil {
		return result, readErr
	} else if current != nil {
		archive, archiveErr := archiveSettingsBytes(settingsPath, current)
		if archiveErr != nil {
			return result, archiveErr
		}
		result.PreRestoreArchive = archive
	}
	if err := deployWriteFile(settingsPath, data, deployFilePerm); err != nil {
		return result, fmt.Errorf("%w: restore %s: %v", ErrDeployWrite, settingsDoorShape, err)
	}

	result.Snapshot = restore.Path
	result.Scheme = restore.Scheme
	result.Taken = restore.Taken
	result.LiveSHA = SHA256Hex(data)

	if manifest.Delete(settingsPath) {
		if err := SaveManifest(manifest); err != nil {
			return result, fmt.Errorf("settings.json was restored but the manifest was not saved: %w", err)
		}
		result.ManifestUpdated = true
	}
	return result, nil
}

// statusLineFileState is the live state of the slot, read through the ledger.
type statusLineFileState struct {
	// Path is the absolute global settings path.
	Path string
	// SHA is the digest of the file's bytes; empty when the file is absent.
	SHA string
	// Current is the object on disk; nil when the slot (or the file) is absent.
	Current *agysettings.StatusLineConfig
	// Unmanaged are the top-level keys the ledger does not manage.
	Unmanaged []string
}

// readStatusLineFile reads the door's file: the bytes for the base digest and the
// unmanaged-key list, and the managed view through the ledger.
//
// A file that does not exist is not opened at all, so a plan on a machine that
// never ran agy writes nothing (not even the ledger's lockfile). A file that
// exists is opened through OpenGlobal, which is also the strictest reader: a
// foreign statusLine object fails there and is surfaced as a repair decision.
func readStatusLineFile() (*statusLineFileState, error) {
	settingsPath, err := agysettings.GlobalSettingsPath()
	if err != nil {
		return nil, err
	}
	state := &statusLineFileState{Path: settingsPath}

	data, present, err := readIfPresent(settingsPath)
	if err != nil {
		return nil, err
	}
	if !present {
		return state, nil
	}
	// An empty file carries no document, so it has no base digest: an empty file
	// and an absent one are the same base state, which keeps the plan digest and
	// the apply re-verification comparable (settingsFileSHA applies the same
	// rule).
	if len(data) > 0 {
		state.SHA = SHA256Hex(data)
	}
	state.Unmanaged = unmanagedTopLevelKeys(data)

	f, err := agysettings.OpenGlobal(settingsPath)
	if err != nil {
		return nil, statusLineOpenError(err)
	}
	defer func() { _ = f.Close() }()

	value, err := f.Get(agysettings.KeyStatusLine)
	if err != nil {
		return nil, statusLineOpenError(err)
	}
	if value == nil {
		return state, nil
	}
	current, ok := value.(*agysettings.StatusLineConfig)
	if !ok {
		return nil, statusLineOpenError(fmt.Errorf("the ledger returned %T for %q", value, agysettings.KeyStatusLine))
	}
	state.Current = current
	return state, nil
}

// statusLineOpenError maps a ledger read failure onto the door's vocabulary: a
// document or slot the ledger refuses needs a repair decision, not a retry.
func statusLineOpenError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrStatusLineForeign) {
		return err
	}
	return &StatusLineForeignError{
		Shape:  settingsDoorShape,
		Detail: err.Error(),
		Err:    ErrStatusLineForeign,
	}
}

// writeStatusLineSlot sets the slot (desired != nil) or clears it (desired ==
// nil) through the ledger, and returns the archive the ledger made of the
// previous bytes.
func writeStatusLineSlot(settingsPath string, desired *agysettings.StatusLineConfig) (string, error) {
	f, err := agysettings.OpenGlobal(settingsPath)
	if err != nil {
		return "", statusLineOpenError(err)
	}
	defer func() { _ = f.Close() }()

	if desired == nil {
		if err := f.Delete(agysettings.KeyStatusLine); err != nil {
			return "", fmt.Errorf("the ledger refused to clear statusLine: %w", err)
		}
	} else if err := f.Set(agysettings.KeyStatusLine, *desired); err != nil {
		return "", fmt.Errorf("the ledger refused the statusLine object: %w", err)
	}
	if err := f.Write(); err != nil {
		return "", fmt.Errorf("the ledger refused the settings write: %w", err)
	}
	return f.LastBackup(), nil
}

// validateStatusLineDoor refuses a door this engine cannot apply safely. It runs
// before any filesystem work, so a hand-built door cannot aim the ledger's
// deliberate global override at a file this door does not own.
func validateStatusLineDoor(door *StatusLineDoor) error {
	if door == nil {
		return fmt.Errorf("%w: nil door", ErrStatusLineDoorInvalid)
	}
	if !knownStatusLineAction(door.Action) {
		return fmt.Errorf("%w: action %q", ErrStatusLineDoorInvalid, string(door.Action))
	}
	global, err := agysettings.GlobalSettingsPath()
	if err != nil {
		return err
	}
	if door.SettingsPath == "" || !sameDoorPath(door.SettingsPath, global) {
		return fmt.Errorf("%w: %q is not the global settings file", ErrStatusLineDoorInvalid, door.SettingsPath)
	}
	if strings.TrimSpace(door.Desired.Type) == "" || strings.TrimSpace(door.Desired.Command) == "" {
		return fmt.Errorf("%w: the desired statusLine object has an empty type or command", ErrStatusLineDoorInvalid)
	}
	if door.Command == "" {
		return fmt.Errorf("%w: the door carries no command line", ErrStatusLineDoorInvalid)
	}
	return nil
}

// sameDoorPath compares two paths for identity under this platform's rules.
func sameDoorPath(a, b string) bool {
	cleanA, cleanB := filepath.Clean(a), filepath.Clean(b)
	if isWindowsGOOS(runtimeGOOS) {
		return strings.EqualFold(cleanA, cleanB)
	}
	return cleanA == cleanB
}

// sameStatusLineObject reports whether two objects carry the same four slots.
// The pointer slot compares by value, so an absent stack_with_default is not
// equal to an explicit true.
func sameStatusLineObject(a, b *agysettings.StatusLineConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Type != b.Type || a.Command != b.Command || a.Enabled != b.Enabled {
		return false
	}
	switch {
	case a.StackWithDefault == nil && b.StackWithDefault == nil:
		return true
	case a.StackWithDefault == nil || b.StackWithDefault == nil:
		return false
	default:
		return *a.StackWithDefault == *b.StackWithDefault
	}
}

// commandLauncher returns the leading token of a command line, quotes removed.
func commandLauncher(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	for _, quote := range []byte{'"', '\''} {
		if trimmed[0] == quote {
			if end := strings.IndexByte(trimmed[1:], quote); end >= 0 {
				return trimmed[1 : end+1]
			}
			return strings.Trim(trimmed, string(quote))
		}
	}
	if index := strings.IndexAny(trimmed, " \t"); index >= 0 {
		return trimmed[:index]
	}
	return trimmed
}

// isAbsoluteLauncherPath reports whether a command's leading token names an
// absolute location. It accepts every spelling on every platform, because the
// value may have been produced elsewhere (the agysettings rule for the same
// check).
func isAbsoluteLauncherPath(candidate string) bool {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return false
	}
	if filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" {
		return true
	}
	return strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`)
}

// settingsFileSHA returns the digest of the settings file's bytes, or "" when the
// file (or an empty file) holds no document.
func settingsFileSHA(settingsPath string) (string, error) {
	data, _, err := readIfPresent(settingsPath)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	return SHA256Hex(data), nil
}

// readIfPresent returns a file's bytes and whether it exists. An empty file is
// present with no bytes.
func readIfPresent(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cannot read %s: %w", filepath.Base(path), err)
	}
	return data, true, nil
}

// coherentSettingsSnapshot reports whether bytes can be a settings.json: a JSON
// object and nothing else. A torn or truncated archive from either writer's
// chain fails here and is skipped by the rollback instead of bricking the file.
func coherentSettingsSnapshot(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var doc map[string]json.RawMessage
	return json.Unmarshal(trimmed, &doc) == nil
}

// archiveSettingsBytes copies bytes into the ledger's snapshot naming
// (<settings>.bak.<ts>, then -2, -3, ... on a same-second collision) and returns
// the path it used.
//
// The scheme is agysettings' own (file.go writeBackup); it is reimplemented here
// because that helper is unexported and this unit does not touch the ledger
// package. The naming is the interface, and the rollback reads both chains.
func archiveSettingsBytes(settingsPath string, data []byte) (string, error) {
	base := settingsPath + ".bak." + time.Now().Format(snapshotTimestampLayout)
	name := base
	for i := 2; ; i++ {
		_, err := os.Stat(name)
		if err == nil {
			name = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("cannot inspect the archive target: %w", err)
		}
		break
	}
	if err := deployWriteFile(name, data, deployFilePerm); err != nil {
		return "", fmt.Errorf("cannot archive the current settings bytes: %w", err)
	}
	return name, nil
}

// unmanagedTopLevelKeys returns the top-level keys the ledger does not manage, in
// sorted order. A document that cannot be decoded has none: the ledger refuses it
// before this list would be rendered.
func unmanagedTopLevelKeys(data []byte) []string {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	managed := make(map[string]bool, len(agysettings.ManagedKeys()))
	for _, key := range agysettings.ManagedKeys() {
		managed[key] = true
	}
	out := make([]string, 0, len(doc))
	for key := range doc {
		if managed[key] {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Error sentinels for the settings door.
var (
	// ErrStatusLineLauncherMissing means no agy-tokens launcher exists where the
	// installers put one, so a command written into settings.json would fail at
	// every agy launch.
	ErrStatusLineLauncherMissing = errors.New("the agy-tokens launcher is not installed")
	// ErrStatusLineForeign means the settings document, or its statusLine slot,
	// is not one this build can read. It needs a repair decision.
	ErrStatusLineForeign = errors.New("the settings.json statusLine slot needs a repair decision")
	// ErrStatusLineStale means settings.json changed between plan and apply.
	// Nothing was written.
	ErrStatusLineStale = errors.New("the settings.json base changed after the plan was built")
	// ErrStatusLineNoBackup means no coherent snapshot exists beside settings.json.
	ErrStatusLineNoBackup = errors.New("no coherent settings.json snapshot to roll back to")
	// ErrStatusLineDoorInvalid means a door is malformed and was refused before
	// any filesystem work.
	ErrStatusLineDoorInvalid = errors.New("statusLine door plan is invalid")
)
