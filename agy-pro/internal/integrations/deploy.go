// Deployment engine: plan, apply, retract, status.
//
// This is the Go replacement for the Node-era deployer
// (scripts/lib/configure-customizations.js, whose created/updated/identical
// vocabulary and idempotence the engine keeps). It is deliberately
// non-interactive core: no prompting, no rendering, no i18n. The plan produces
// plain diff-shaped lines and typed summaries; T5/T8 own the consent gate, the
// sentences a user reads, and the exit codes.
//
// Three rules shape the code below.
//
//   - NEVER DELETE. A destination is archived into ~/.agy-pro/backups by
//     moving it (retract) or copying it before an overwrite (update), mirroring
//     internal/mode's scheme (mode.go:427-443, mode.go:644-659). The manifest
//     records the archive as the entry's backup_ref, so a rollback has a source
//     of truth.
//   - NEVER AUTO-FIX DRIFT. Apply re-verifies every destination against the
//     bytes the plan read and refuses the whole run when any of them moved
//     (stale base, CWE-367). Status reports drift, foreign files and missing
//     files, and repairs nothing: the repair verb is T8's (pick record item 7).
//   - LAUNCH PATHS NEVER WRITE. Nothing in this file is called from a launch
//     path; Status is read-only by construction and takes no lock.
//
// Applies and retracts are serialized by one package-level mutex. The manifest
// store has no lockfile (T2 handoff note 5: writes are atomic renames, and two
// concurrent load-modify-saves would lose one another's entries), so the engine
// that owns both the files and the bookkeeping serializes itself.
//
// That mutex is PROCESS-LOCAL, so the contract this engine is documented under
// is single-writer: ONE integrations command at a time per machine. Two
// `agy pro integrations` processes are not serialized by anything, and the
// manifest they would both edit is last-writer-wins: the later save replaces the
// earlier one's entry set. The earlier run's files then stay on disk with no
// bookkeeping, so Status reports them as foreign and Retract leaves them alone.
// Repair reconciles drift only for the destinations the manifest still claims; a
// file whose entry was lost comes back through `adopt`, or through a re-apply,
// which records identical bytes as adopted. No lockfile is added here on
// purpose: the posture is documented, not enforced.
package integrations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// deployMu serializes Apply and Retract. Status is read-only and takes no lock.
var deployMu sync.Mutex

// deployWriteFile is the destination-writer seam. It is separate from the
// manifest's own seam so a test can count DESTINATION writes without counting
// the bookkeeping write that follows them.
var deployWriteFile = paths.AtomicWriteFile

// deployFilePerm is the mode deployed files are written with. The manifest is
// 0600 (store.go); deployed content is the user's own configuration, and it
// carries no reason to be readable by anyone else.
const deployFilePerm os.FileMode = 0o600

// deployDirPerm is the mode created directories get, matching mode.go:465.
const deployDirPerm os.FileMode = 0o755

// PlanActionKind is what a plan does to one destination.
type PlanActionKind string

const (
	// PlanActionCreate means the destination does not exist yet.
	PlanActionCreate PlanActionKind = "create"
	// PlanActionUpdate means the destination exists with different bytes.
	PlanActionUpdate PlanActionKind = "update"
	// PlanActionIdentical means the destination already holds exactly these
	// bytes; applying it writes nothing.
	PlanActionIdentical PlanActionKind = "identical"
)

// knownPlanAction reports whether kind is one of the three this build writes.
func knownPlanAction(kind PlanActionKind) bool {
	switch kind {
	case PlanActionCreate, PlanActionUpdate, PlanActionIdentical:
		return true
	default:
		return false
	}
}

// PlanAction is one destination and what the plan does to it.
type PlanAction struct {
	// Kind is create, update or identical, decided from the live bytes at plan
	// time.
	Kind PlanActionKind
	// DestPath is the absolute destination path below the layout's DestDir.
	DestPath string
	// RelPath is the destination relative to the layout's DestDir, in slash
	// form. It is what a renderer can show without repeating the user's home
	// path.
	RelPath string
	// OldSHA is the digest of the bytes found at the destination when the plan
	// was built; empty for a create.
	OldSHA string
	// NewSHA is the digest of the bytes the plan would write: the source bytes, or
	// for the hook file the deploy-time rewritten form of them (hookrewrite.go, and
	// the bare source bytes when no launcher resolves).
	NewSHA string
	// Data is what the plan would write: the source content, or the hook file's
	// rewritten content. It is empty for an identical action, which writes
	// nothing.
	Data []byte
}

// Writes reports whether applying the action touches the destination.
func (a PlanAction) Writes() bool {
	return a.Kind == PlanActionCreate || a.Kind == PlanActionUpdate
}

// DeployPlan is one feature's ordered deployment plan. It is self-contained:
// the bytes it reports are the bytes Apply writes, so a plan built from a tree
// that later changes still writes what it showed.
type DeployPlan struct {
	// Feature is the feature being deployed.
	Feature Feature
	// Layout is the destination map the plan was built against.
	Layout FeatureLayout
	// Actions are the per-destination actions, ordered by RelPath.
	Actions []PlanAction
	// HookCommand is the deploy-time command rewrite's posture record for the
	// hook feature (hookrewrite.go): whether the deployed command names this
	// machine's absolute launcher or the portable bare fallback. It is nil for
	// every tree feature.
	HookCommand *HookCommandRewrite
}

// Counts returns how many destinations each action kind covers.
func (p *DeployPlan) Counts() (created, updated, identical int) {
	if p == nil {
		return 0, 0, 0
	}
	for _, action := range p.Actions {
		switch action.Kind {
		case PlanActionCreate:
			created++
		case PlanActionUpdate:
			updated++
		case PlanActionIdentical:
			identical++
		}
	}
	return created, updated, identical
}

// Writes returns how many destinations applying the plan would write.
func (p *DeployPlan) Writes() int {
	if p == nil {
		return 0
	}
	writes := 0
	for _, action := range p.Actions {
		if action.Writes() {
			writes++
		}
	}
	return writes
}

// Lines renders the plan as plain diff-shaped lines, one per destination:
//
//	create     <path>
//	update     <path>  <old digest> -> <new digest>
//	identical  <path>
//
// Every path goes through providers.MaskOperand, the display authority the rest
// of agy-pro routes operands through (agy-pro/internal/mode/mode.go:401 is the
// same call in the same situation). Destinations are under the user's home, so
// they are not secrets; masking is the belt for the one case that matters, a
// source tree whose file name happens to be key-shaped.
func (p *DeployPlan) Lines() []string {
	if p == nil {
		return nil
	}
	lines := make([]string, 0, len(p.Actions))
	for _, action := range p.Actions {
		destination := providers.MaskOperand(action.DestPath)
		switch action.Kind {
		case PlanActionUpdate:
			lines = append(lines, fmt.Sprintf("update     %s  %s -> %s",
				destination, shortSHA(action.OldSHA), shortSHA(action.NewSHA)))
		case PlanActionIdentical:
			lines = append(lines, fmt.Sprintf("identical  %s", destination))
		default:
			lines = append(lines, fmt.Sprintf("create     %s", destination))
		}
	}
	return lines
}

// shortSHA renders the first 8 hex characters of a digest for a diff line.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// Plan builds a deployment plan for one feature against the live config tree.
//
// The content the plan reads is whatever fs.FS the caller hands over; a caller
// with no source falls back to ToolkitRoot() for the hook feature only.
// OpenFeatureSource is that decision. The plan reads destinations to classify
// them (create/update/identical) and writes nothing at all.
//
// The hook file's bytes are rewritten at read time to this machine's absolute
// launcher (hookrewrite.go), so every digest this plan carries - NewSHA, and the
// manifest entry Apply records from it - describes the bytes that land on disk.
// plan.HookCommand says which posture the plan took, for a renderer that wants to
// show it.
func Plan(feature Feature, content fs.FS) (*DeployPlan, error) {
	layout, err := LayoutFor(feature)
	if err != nil {
		return nil, err
	}
	source, err := OpenFeatureSource(feature, content)
	if err != nil {
		return nil, err
	}
	files, err := enumerateLayout(layout, source)
	if err != nil {
		return nil, err
	}

	plan := &DeployPlan{Feature: feature, Layout: layout, Actions: make([]PlanAction, 0, len(files))}
	seen := make(map[string]string, len(files))
	for _, file := range files {
		oldSHA, present, err := liveDestination(file.DestPath)
		if err != nil {
			return nil, err
		}
		if other, duplicate := seen[file.DestPath]; duplicate {
			return nil, fmt.Errorf("%w: %q is claimed by both %q and %q",
				ErrPlanInvalid, file.DestPath, other, file.RelPath)
		}
		seen[file.DestPath] = file.RelPath
		if file.HookCommand != nil {
			plan.HookCommand = file.HookCommand
		}

		action := PlanAction{
			DestPath: file.DestPath,
			RelPath:  file.RelPath,
			OldSHA:   oldSHA,
			NewSHA:   file.SHA256,
		}
		switch {
		case !present:
			action.Kind = PlanActionCreate
			action.OldSHA = ""
			action.Data = file.Data
		case oldSHA == file.SHA256:
			action.Kind = PlanActionIdentical
		default:
			action.Kind = PlanActionUpdate
			action.Data = file.Data
		}
		plan.Actions = append(plan.Actions, action)
	}
	return plan, nil
}

// StaleError reports a destination whose bytes changed between Plan and Apply.
// Nothing is written when it is returned: the whole run is refused before the
// first byte, so a half-applied deployment cannot come out of a race.
type StaleError struct {
	// Destination is the path that moved.
	Destination string
	// Kind is the action the plan had built for it.
	Kind PlanActionKind
	// WantSHA is the digest the plan expected; empty means it expected the
	// destination to be absent.
	WantSHA string
	// LiveSHA is the digest actually found; empty means nothing is there.
	LiveSHA string
}

// Error names the destination, what the plan expected, what it found, and what
// to do next.
func (e *StaleError) Error() string {
	if e == nil {
		return ErrPlanStale.Error()
	}
	want := "absent"
	if e.WantSHA != "" {
		want = e.WantSHA
	}
	live := "absent"
	if e.LiveSHA != "" {
		live = e.LiveSHA
	}
	return fmt.Sprintf("refusing to %s %q: the live bytes changed after the plan was built (expected %s, found %s); nothing was written, re-run the plan",
		string(e.Kind), e.Destination, want, live)
}

// Unwrap exposes the sentinel to errors.Is.
func (e *StaleError) Unwrap() error {
	return ErrPlanStale
}

// ApplySummary is what one apply did.
type ApplySummary struct {
	// Feature is the feature applied.
	Feature Feature
	// Created, Updated and Identical count destinations by action kind.
	Created   int
	Updated   int
	Identical int
	// Entries are the manifest records that now describe the destinations, in
	// plan order.
	Entries []ManifestEntry
	// BackupDir is where pre-overwrite bytes were archived; empty when no
	// destination was overwritten.
	BackupDir string
	// ManifestUpdated reports whether the bookkeeping was rewritten. An
	// all-identical re-apply leaves both the files and the manifest untouched.
	ManifestUpdated bool
}

// Written reports how many destinations apply wrote.
func (s *ApplySummary) Written() int {
	if s == nil {
		return 0
	}
	return s.Created + s.Updated
}

// Apply writes a plan and records it in the manifest.
//
// Order of operations, deliberately:
//
//  1. Load the manifest. A corrupt or too-new manifest stops the run before
//     anything is written (fail closed; the bytes on disk stay as they are).
//  2. Re-verify EVERY destination against the digest the plan recorded. Any
//     mismatch is a StaleError naming that destination, and the run stops with
//     zero writes.
//  3. Write files (create/update), archiving the previous bytes of an update
//     into ~/.agy-pro/backups and recording that path as the entry's backup_ref.
//  4. Record every destination with provenance go-applied (a destination the
//     plan found already identical is recorded as adopted instead, because its
//     bytes were never touched), then save the manifest once.
//
// Rules 1 and 2 are what make a refused apply harmless: by the time the first
// byte is written, every destination has been proven to still be what the plan
// read.
func Apply(plan *DeployPlan) (*ApplySummary, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	if err := validatePlan(plan); err != nil {
		return nil, err
	}

	manifest, err := LoadManifest()
	if err != nil {
		// Fail closed: a manifest that cannot be read is not "nothing deployed".
		return nil, err
	}

	if err := verifyAgainstLiveTree(plan); err != nil {
		return nil, err
	}

	summary := &ApplySummary{Feature: plan.Feature, Entries: make([]ManifestEntry, 0, len(plan.Actions))}
	backupDir := ""
	dirty := false

	for _, action := range plan.Actions {
		existing, recorded := manifest.Entry(action.DestPath)

		entry := ManifestEntry{
			Path:       action.DestPath,
			SHA256:     action.NewSHA,
			Provenance: ProvenanceGoApplied,
		}

		switch action.Kind {
		case PlanActionIdentical:
			summary.Identical++
			if recorded {
				// The destination was already ours: keep who wrote it and where
				// its pre-change copy lives.
				entry.Provenance = existing.Provenance
				entry.BackupRef = existing.BackupRef
			} else {
				// Record-only adopt (pick record item 8): the bytes were never
				// touched, so the engine did not write them.
				entry.Provenance = ProvenanceAdopted
			}
		case PlanActionCreate, PlanActionUpdate:
			if action.Kind == PlanActionUpdate {
				archived, archiveErr := archiveDestination(plan.Layout.DestDir, action.DestPath, &backupDir)
				if archiveErr != nil {
					return summary, partialApply(archiveErr, summary)
				}
				entry.BackupRef = archived
				summary.BackupDir = backupDir
			}
			if writeErr := writeDestination(action); writeErr != nil {
				return summary, partialApply(writeErr, summary)
			}
			if action.Kind == PlanActionCreate {
				summary.Created++
			} else {
				summary.Updated++
			}
		default:
			return summary, fmt.Errorf("%w: destination %q carries action %q",
				ErrPlanInvalid, action.DestPath, string(action.Kind))
		}

		if recorded && existing.equalKnown(entry) {
			// Nothing to record: the bookkeeping already says exactly this.
			summary.Entries = append(summary.Entries, existing)
			continue
		}
		if err := manifest.Set(entry); err != nil {
			// Same annotation as the archive/write failures above: a plan whose
			// earlier destinations already landed must not read as "nothing
			// happened" just because the refusal came from the bookkeeping.
			return summary, partialApply(err, summary)
		}
		dirty = true
		summary.Entries = append(summary.Entries, entry)
	}

	if !dirty {
		// Every destination was already in place and already recorded, so the
		// bookkeeping is left byte-identical: a second apply is a no-op down to
		// the manifest's mtime.
		return summary, nil
	}

	if err := SaveManifest(manifest); err != nil {
		// The files are on disk but the bookkeeping is not. Say so instead of
		// pretending the run was clean; Status will report the written files as
		// foreign until a repair records them.
		return summary, fmt.Errorf("deployed %d destination(s) but the manifest was not saved: %w", summary.Written(), err)
	}
	summary.ManifestUpdated = true
	return summary, nil
}

// partialApply annotates a failure that happened after some destinations were
// already written. The manifest is deliberately not saved in that case, so the
// caller (and Status) must know the tree is ahead of the bookkeeping instead of
// reading the run as "nothing happened".
func partialApply(cause error, summary *ApplySummary) error {
	written := 0
	if summary != nil {
		written = summary.Written()
	}
	if written == 0 {
		return cause
	}
	return fmt.Errorf("%d destination(s) were written before the failure and the manifest was not saved: %w", written, cause)
}

// validatePlan refuses a plan this engine cannot apply safely. It runs before
// any filesystem work, so a hand-built plan cannot steer a write outside the
// destination partition or claim one destination twice.
func validatePlan(plan *DeployPlan) error {
	if plan == nil {
		return fmt.Errorf("%w: nil plan", ErrPlanInvalid)
	}
	if plan.Layout.Feature != plan.Feature || plan.Feature == "" {
		return fmt.Errorf("%w: plan feature %q does not match its layout feature %q",
			ErrPlanInvalid, string(plan.Feature), string(plan.Layout.Feature))
	}
	seen := make(map[string]bool, len(plan.Actions))
	for _, action := range plan.Actions {
		if !knownPlanAction(action.Kind) {
			return fmt.Errorf("%w: destination %q carries action %q", ErrPlanInvalid, action.DestPath, string(action.Kind))
		}
		if action.DestPath == "" || !filepath.IsAbs(action.DestPath) {
			return fmt.Errorf("%w: destination %q is not an absolute path", ErrPlanInvalid, action.DestPath)
		}
		clean := filepath.Clean(action.DestPath)
		if clean != plan.Layout.DestDir &&
			!strings.HasPrefix(clean, plan.Layout.DestDir+string(filepath.Separator)) {
			return fmt.Errorf("%w: destination %q is outside %q", ErrPlanInvalid, clean, plan.Layout.DestDir)
		}
		if seen[clean] {
			return fmt.Errorf("%w: destination %q is claimed twice", ErrPlanInvalid, clean)
		}
		seen[clean] = true

		if !isSHA256Hex(action.NewSHA) {
			return fmt.Errorf("%w: destination %q has digest %q", ErrPlanInvalid, clean, action.NewSHA)
		}
		if action.OldSHA != "" && !isSHA256Hex(action.OldSHA) {
			return fmt.Errorf("%w: destination %q has old digest %q", ErrPlanInvalid, clean, action.OldSHA)
		}
		switch action.Kind {
		case PlanActionCreate:
			if action.OldSHA != "" {
				return fmt.Errorf("%w: create %q carries an old digest", ErrPlanInvalid, clean)
			}
			if action.Data == nil {
				return fmt.Errorf("%w: create %q carries no content", ErrPlanInvalid, clean)
			}
		case PlanActionUpdate:
			if action.OldSHA == "" || action.OldSHA == action.NewSHA {
				return fmt.Errorf("%w: update %q does not describe a change", ErrPlanInvalid, clean)
			}
			if action.Data == nil {
				return fmt.Errorf("%w: update %q carries no content", ErrPlanInvalid, clean)
			}
		case PlanActionIdentical:
			if action.OldSHA != action.NewSHA {
				return fmt.Errorf("%w: identical %q reports %q -> %q", ErrPlanInvalid, clean, action.OldSHA, action.NewSHA)
			}
		}
		if action.Data != nil {
			if got := SHA256Hex(action.Data); got != action.NewSHA {
				return fmt.Errorf("%w: destination %q carries %d byte(s) digesting to %q, not the planned %q",
					ErrPlanInvalid, clean, len(action.Data), got, action.NewSHA)
			}
		}
	}
	return nil
}

// verifyAgainstLiveTree re-reads every destination and refuses the run when any
// of them no longer matches what the plan recorded. It never writes.
func verifyAgainstLiveTree(plan *DeployPlan) error {
	for _, action := range plan.Actions {
		liveSHA, present, err := liveDestination(action.DestPath)
		if err != nil {
			return err
		}
		switch action.Kind {
		case PlanActionCreate:
			if present {
				return &StaleError{Destination: action.DestPath, Kind: action.Kind, WantSHA: "", LiveSHA: liveSHA}
			}
		case PlanActionUpdate, PlanActionIdentical:
			if !present {
				return &StaleError{Destination: action.DestPath, Kind: action.Kind, WantSHA: action.OldSHA, LiveSHA: ""}
			}
			if liveSHA != action.OldSHA {
				return &StaleError{Destination: action.DestPath, Kind: action.Kind, WantSHA: action.OldSHA, LiveSHA: liveSHA}
			}
		}
	}
	return nil
}

// writeDestination writes one planned file atomically, creating its parent
// directories first (mode.go:464-473 is the same shape).
func writeDestination(action PlanAction) error {
	if err := os.MkdirAll(filepath.Dir(action.DestPath), deployDirPerm); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", filepath.Dir(action.DestPath), err)
	}
	if err := deployWriteFile(action.DestPath, action.Data, deployFilePerm); err != nil {
		return fmt.Errorf("%w: %q: %v", ErrDeployWrite, action.DestPath, err)
	}
	return nil
}

// RetractedDestination is one destination a retract moved out of the config
// tree, with what it found and where the bytes went.
type RetractedDestination struct {
	// DestPath is the path the destination had.
	DestPath string
	// BackupPath is where the bytes were archived; empty when the destination
	// was already gone.
	BackupPath string
	// SHA256 is the digest the manifest recorded.
	SHA256 string
	// LiveSHA is the digest found on disk; empty when the destination was
	// already gone.
	LiveSHA string
	// Drifted reports that the live bytes were not the bytes the engine wrote,
	// i.e. the user (or another tool) changed the file after it was deployed.
	// Retract still archives it, because archiving is not deleting.
	Drifted bool
	// Missing reports that the destination was recorded but already absent, so
	// there was nothing to move.
	Missing bool
}

// RetractSummary is what one retract did.
type RetractSummary struct {
	// Feature is the feature retracted.
	Feature Feature
	// BackupDir is where the destinations were archived; empty when there was
	// nothing to move.
	BackupDir string
	// Destinations are the manifest-owned destinations the retract handled, in
	// path order.
	Destinations []RetractedDestination
	// ManifestUpdated reports whether the bookkeeping was rewritten.
	ManifestUpdated bool
}

// BackedUp reports how many destinations were moved into the backup tree.
func (s *RetractSummary) BackedUp() int {
	if s == nil {
		return 0
	}
	moved := 0
	for _, destination := range s.Destinations {
		if !destination.Missing {
			moved++
		}
	}
	return moved
}

// Retract removes exactly the destinations the manifest says this engine
// deployed for a feature.
//
// It is not a "delete" verb: every destination is MOVED into a timestamped
// directory under ~/.agy-pro/backups, mirroring the destination tree below the
// feature root so two files with one base name never collide (mode.go:432-443
// moves whole items to a snapshot directory; this is the per-file form of the
// same rule). A destination the user edited is moved too, and reported as
// drifted, because the move preserves the edit rather than destroying it.
//
// Files nobody recorded are left alone: a foreign file in the destination tree
// is not this engine's to remove. Directories left empty by the moves are
// pruned, and nothing else.
//
// Retracting twice is stable: the second run finds no entries and no files.
func Retract(feature Feature) (*RetractSummary, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	layout, err := LayoutFor(feature)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}

	owned := ownedDestinations(manifest, layout)
	summary := &RetractSummary{Feature: feature, Destinations: make([]RetractedDestination, 0, len(owned))}
	if len(owned) == 0 {
		return summary, nil
	}

	backupDir := ""
	dirty := false
	for _, entry := range owned {
		result := RetractedDestination{DestPath: entry.Path, SHA256: entry.SHA256}
		liveSHA, present, liveErr := liveDestination(entry.Path)
		if liveErr != nil {
			return summary, liveErr
		}
		if !present {
			result.Missing = true
			summary.Destinations = append(summary.Destinations, result)
			if manifest.Delete(entry.Path) {
				dirty = true
			}
			continue
		}
		result.LiveSHA = liveSHA
		result.Drifted = liveSHA != entry.SHA256

		moved, moveErr := moveDestinationToBackup(layout.DestDir, entry.Path, &backupDir)
		if moveErr != nil {
			return summary, moveErr
		}
		result.BackupPath = moved
		summary.BackupDir = backupDir
		summary.Destinations = append(summary.Destinations, result)
		if manifest.Delete(entry.Path) {
			dirty = true
		}
	}

	pruneEmptyDirs(layout.DestDir)

	if dirty {
		if err := SaveManifest(manifest); err != nil {
			return summary, fmt.Errorf("retracted %d destination(s) but the manifest was not saved: %w", summary.BackedUp(), err)
		}
		summary.ManifestUpdated = true
	}
	return summary, nil
}

// DestinationState is the live posture of one destination relative to the
// manifest.
type DestinationState string

const (
	// StateClean means the manifest records the destination and the live bytes
	// are exactly the recorded bytes.
	StateClean DestinationState = "clean"
	// StateDrifted means the manifest records the destination but the live bytes
	// are not the bytes this machine calls for: someone else changed the file, or
	// the file is a posture the engine no longer stands behind (a bare hook
	// command once a launcher exists). Detail says which.
	StateDrifted DestinationState = "drifted"
	// StateMissing means the manifest records the destination but it is gone.
	StateMissing DestinationState = "missing"
	// StateForeign means a file sits in the destination tree that no manifest
	// entry claims.
	StateForeign DestinationState = "foreign"
)

// DestinationStatus is what Status found at one path.
type DestinationStatus struct {
	// Path is the absolute path.
	Path string
	// State is the live posture.
	State DestinationState
	// WantSHA is the manifest's digest; empty for a foreign file.
	WantSHA string
	// LiveSHA is the digest on disk; empty when the file is missing.
	LiveSHA string
	// Provenance is who the manifest says wrote it; empty for a foreign file.
	Provenance Provenance
	// BackupRef is the recorded archive of the previous bytes, when there is
	// one.
	BackupRef string
	// Detail is the engine's plain-English note about the destination when a
	// digest match is not the whole story: why a drifted destination drifted, or
	// what a still-Clean destination cannot do (a hook command the rewrite
	// declines to pin, a hook command pointing at a launcher that is gone). It is
	// empty when the state says everything. A door renders it verbatim.
	Detail string
}

// FeatureStatus is the live posture of one feature. It is derived from the
// manifest plus the filesystem; it never changes either.
type FeatureStatus struct {
	// Feature is the feature whose destinations were inspected.
	Feature Feature
	// DestDir is the destination root inspected.
	DestDir string
	// Deployed reports whether the manifest records anything for the feature.
	Deployed bool
	// Destinations are the recorded and foreign destinations, in path order.
	Destinations []DestinationStatus
	// Clean, Drifted, Missing and Foreign count destinations per state.
	Clean   int
	Drifted int
	Missing int
	Foreign int
}

// OK reports whether the feature's destinations agree with the manifest and
// nothing foreign sits in the tree. Drift, a missing file or a foreign file all
// make it false: the engine never fixes any of them on its own (pick 7).
func (s *FeatureStatus) OK() bool {
	if s == nil {
		return true
	}
	return s.Drifted == 0 && s.Missing == 0 && s.Foreign == 0
}

// Status derives a feature's live posture from the manifest and the filesystem.
//
// Drift is reported, never repaired: a changed file is StateDrifted, a file no
// entry claims is StateForeign, a recorded file that is gone is StateMissing.
// Nothing is written, so this is safe on a launch path (pick 7).
//
// The hook feature carries one extra check, because its deployed bytes depend on
// the machine rather than on the source alone (hookrewrite.go,
// hookLiveCommandPosture). It has two outcomes, and the difference matters:
//
//   - drift, when the live file is still the portable bare form and this machine
//     would deploy the pinned form. That is the visible half of the fallback
//     posture: a launcherless machine deploys the bare form and reads clean, and
//     the same machine reads drifted the moment the launcher lands, so repair can
//     pin it.
//   - an advisory Detail with NO drift change, when the live bytes are what this
//     engine would deploy but the operator has to know something: the rewrite
//     declines this document (so apply cannot pin it), or the deployed command
//     points at a launcher that is gone (so repairing it would have to unpin it
//     first, re-opening F-V1).
func Status(feature Feature) (*FeatureStatus, error) {
	layout, err := LayoutFor(feature)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}

	owned := ownedDestinations(manifest, layout)
	status := &FeatureStatus{Feature: feature, DestDir: layout.DestDir, Deployed: len(owned) > 0}
	recorded := make(map[string]bool, len(owned))
	for _, entry := range owned {
		recorded[filepath.Clean(entry.Path)] = true
		destination := DestinationStatus{
			Path:       entry.Path,
			WantSHA:    entry.SHA256,
			Provenance: entry.Provenance,
			BackupRef:  entry.BackupRef,
		}
		liveSHA, present, liveErr := liveDestination(entry.Path)
		if liveErr != nil {
			return nil, liveErr
		}
		switch {
		case !present:
			destination.State = StateMissing
			status.Missing++
		case liveSHA != entry.SHA256:
			destination.LiveSHA = liveSHA
			destination.State = StateDrifted
			status.Drifted++
		default:
			destination.LiveSHA = liveSHA
			destination.State = StateClean
			status.Clean++
		}
		if feature == FeatureHook && destination.State == StateClean {
			if detail, stale := hookLiveCommandPosture(entry.Path); detail != "" || stale {
				destination.Detail = detail
				if stale {
					destination.State = StateDrifted
					status.Clean--
					status.Drifted++
				}
			}
		}
		status.Destinations = append(status.Destinations, destination)
	}

	if !layout.SingleFile() {
		foreign, foreignErr := foreignDestinations(layout.DestDir, recorded)
		if foreignErr != nil {
			return nil, foreignErr
		}
		for _, path := range foreign {
			liveSHA, _, _ := liveDestination(path)
			status.Destinations = append(status.Destinations, DestinationStatus{
				Path:    path,
				State:   StateForeign,
				LiveSHA: liveSHA,
			})
			status.Foreign++
		}
	}

	return status, nil
}

// ownedDestinations returns the manifest entries a feature owns: the exact file
// for a single-file feature, every entry below the destination directory for a
// tree feature.
func ownedDestinations(manifest *Manifest, layout FeatureLayout) []ManifestEntry {
	if manifest == nil {
		return nil
	}
	if layout.SingleFile() {
		destination, err := layout.Destination(layout.SourceFile)
		if err != nil {
			return nil
		}
		entry, ok := manifest.Entry(destination)
		if !ok {
			return nil
		}
		return []ManifestEntry{entry}
	}
	return manifest.EntriesUnder(layout.DestDir)
}

// foreignDestinations lists the files below root that no manifest entry claims.
// A missing root is not an error: a machine that never deployed the feature has
// nothing foreign in it.
func foreignDestinations(root string, recorded map[string]bool) ([]string, error) {
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot inspect %q: %w", root, err)
	}
	foreign := make([]string, 0, 4)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("cannot inspect %q: %w", path, err)
		}
		if entry.IsDir() {
			return nil
		}
		if recorded[filepath.Clean(path)] {
			return nil
		}
		foreign = append(foreign, path)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return foreign, nil
}

// archiveDestination copies a destination's current bytes into the backup tree
// before Apply overwrites them, creating the backup directory on first use. It
// is a copy, not a move: the destination has to stay in place until the atomic
// replace lands.
func archiveDestination(root, destination string, backupDir *string) (string, error) {
	return archiveDestinationTagged(root, destination, backupDir, "")
}

// archiveDestinationTagged is archiveDestination with the backup directory's tag
// spelled out: "" for an apply's pre-overwrite archive, "_rollback" for the bytes
// a rollback replaces (rollback.go:23). The tag is the only difference, so one
// glance at a backup directory name says which verb wrote it.
func archiveDestinationTagged(root, destination string, backupDir *string, tag string) (string, error) {
	if *backupDir == "" {
		dir, err := newBackupDir(tag)
		if err != nil {
			return "", err
		}
		*backupDir = dir
	}
	target, err := backupTarget(*backupDir, root, destination)
	if err != nil {
		return "", err
	}
	if err := paths.CopyRecursive(destination, target); err != nil {
		return "", fmt.Errorf("failed to archive %q before overwriting it: %w", destination, err)
	}
	return target, nil
}

// moveDestinationToBackup moves a destination into the backup tree, creating
// the backup directory on first use. This is the retract path, where the
// destination really does leave the config tree.
func moveDestinationToBackup(root, destination string, backupDir *string) (string, error) {
	return moveDestinationToBackupTagged(root, destination, backupDir, "_retract")
}

// moveDestinationToBackupTagged is moveDestinationToBackup with the backup
// directory's tag spelled out: "_retract" for a retract, "_rollback" for a
// rollback taking back a file an apply created.
func moveDestinationToBackupTagged(root, destination string, backupDir *string, tag string) (string, error) {
	if *backupDir == "" {
		dir, err := newBackupDir(tag)
		if err != nil {
			return "", err
		}
		*backupDir = dir
	}
	target, err := backupTarget(*backupDir, root, destination)
	if err != nil {
		return "", err
	}
	moved, err := paths.MoveToBackup(destination, filepath.Dir(target))
	if err != nil {
		return "", fmt.Errorf("failed to move %q to its backup: %w", destination, err)
	}
	return moved, nil
}

// backupTarget returns where a destination's copy is archived inside backupDir.
// The destination's path below root is mirrored, so rules/AGENTS.md and
// skills/foo/AGENTS.md land in two different directories instead of colliding
// on one base name (paths.MoveToBackup names the target by base name).
func backupTarget(backupDir, root, destination string) (string, error) {
	if backupDir == "" {
		return "", errors.New("backup directory is not allocated yet")
	}
	rel, err := filepath.Rel(root, destination)
	if err != nil {
		return "", fmt.Errorf("cannot place %q in the backup tree: %w", destination, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("cannot place %q in the backup tree: it is outside %q", destination, root)
	}
	return filepath.Join(backupDir, rel), nil
}

// newBackupDir allocates a timestamped directory below ~/.agy-pro/backups,
// mirroring mode.go:427 (unique per second, so two runs never interleave). The
// snapshot is owner-only (paths.BackupDirPerm): it holds copies of the user's
// own configuration, not world-readable content.
func newBackupDir(suffix string) (string, error) {
	base, err := paths.AgyProBackups()
	if err != nil {
		return "", err
	}
	dir := uniqueBackupDir(filepath.Join(base, paths.NewBackupTimestamp()+suffix))
	if err := os.MkdirAll(dir, paths.BackupDirPerm); err != nil {
		return "", fmt.Errorf("failed to create backup directory %q: %w", dir, err)
	}
	return dir, nil
}

// uniqueBackupDir is mode.go:644-659: a numeric suffix keeps two runs in the
// same second out of one snapshot directory, and keeps lexical order aligned
// with chronological order.
func uniqueBackupDir(candidate string) string {
	if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
		return candidate
	}
	for i := 2; ; i++ {
		next := fmt.Sprintf("%s_%d", candidate, i)
		if _, err := os.Stat(next); errors.Is(err, fs.ErrNotExist) {
			return next
		}
	}
}

// pruneEmptyDirs removes directories that the retract left empty, deepest
// first, including root itself when the whole tree is gone. It removes nothing
// that still holds a file: a foreign file keeps its directory alive.
func pruneEmptyDirs(root string) {
	dirs := make([]string, 0, 8)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if walkErr != nil {
		return
	}
	// Deepest first, so a parent is considered only after its children left.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}

// equalKnown compares the four recorded fields of two entries. It exists
// because ManifestEntry carries an unexported forward-compatibility map and
// therefore cannot be compared with ==.
func (e ManifestEntry) equalKnown(other ManifestEntry) bool {
	return e.Path == other.Path &&
		e.SHA256 == other.SHA256 &&
		e.Provenance == other.Provenance &&
		e.BackupRef == other.BackupRef
}

// Error sentinels for the deployment layer.
var (
	// ErrPlanInvalid means a plan is malformed and was refused before any
	// filesystem work.
	ErrPlanInvalid = errors.New("deployment plan is invalid")
	// ErrPlanStale means a destination changed between Plan and Apply. Nothing
	// was written.
	ErrPlanStale = errors.New("deployment plan is stale")
	// ErrDeployWrite means a destination could not be written.
	ErrDeployWrite = errors.New("deployed file write failed")
)
