// Deployment rollback: put a deployed content tree back to the bytes it held
// before the install.
//
// This file is the content-engine twin of RollbackStatusLine (settings.go:850).
// The settings door restores settings.json from its own .bak chain; this verb
// restores the rules/skills/plugins/hooks trees from the archives the manifest
// recorded as each entry's backup_ref. Three rules carry over from the rest of
// the engine:
//
//   - NEVER DELETE. The bytes a restore replaces are archived into
//     ~/.agy-pro/backups/<ts>_rollback first (Apply's copy, deploy.go:850), and a
//     file the apply created is MOVED into that directory (Retract's mechanism,
//     deploy.go:879). The only thing removed is an empty directory left behind
//     by those moves, which Retract already prunes (deploy.go:955).
//   - THE MANIFEST IS THE ONLY SOURCE OF TRUTH. A destination is restored because
//     its entry records a backup_ref, and taken back because its entry is
//     go-applied with no backup_ref. Nothing is guessed from timestamps, from the
//     backup tree's shape, or from the content source the apply read.
//   - A MISSING SOURCE IS A NAMED SKIP, NOT A FALLBACK. A backup_ref that is gone,
//     unreadable, or outside the backup tree leaves the live file exactly as it
//     is, keeps the entry, and is named in the summary.
//
// The claim of a destination a rollback resolved is DROPPED, exactly as
// RollbackStatusLine drops the settings entry (impl-p53-t5.md §8 decision 5): the
// restored bytes are not bytes this engine wrote, so recording them would make
// the next drift report a lie. A restored destination therefore reads as foreign
// in Status until an adopt or a re-apply records it honestly (the single-file hook
// feature has no foreign scan, deploy.go:776, so there it reads as "not
// deployed").
package integrations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/paths"
)

// rollbackBackupTag is the suffix of the backup directory a rollback archives
// into. It is the sibling of retract's "_retract" (deploy.go:880), so one glance
// at a backup directory name says which verb put the bytes there.
const rollbackBackupTag = "_rollback"

// RollbackAction is what a rollback did, or would do, to one destination.
type RollbackAction string

const (
	// RollbackActionRestore puts the entry's recorded backup_ref bytes back.
	RollbackActionRestore RollbackAction = "restore"
	// RollbackActionRemove takes back a file the apply created, by moving it out
	// of the config tree.
	RollbackActionRemove RollbackAction = "remove"
	// RollbackActionSkip leaves the destination exactly as it is.
	RollbackActionSkip RollbackAction = "skip"
)

// Rollback skip reasons. They are stable words a renderer maps onto a sentence
// in the user's language; the sentence itself belongs to the CLI (T9-D2), not to
// this engine.
const (
	// RollbackSkipDestinationMissing means the recorded destination is already
	// gone. A rollback returns a tree to its pre-install bytes; it never
	// resurrects a file the user deleted (re-apply and repair are the verbs for
	// that), so the entry is kept and the skip is reported.
	RollbackSkipDestinationMissing = "destination-missing"
	// RollbackSkipAdopted means the manifest records the destination without
	// having written it (record-only adopt), so its pre-install state IS its
	// current bytes.
	RollbackSkipAdopted = "adopted"
	// RollbackSkipProvenance means another writer (node-era, mode-era) left the
	// file: this engine never wrote those bytes, so there is nothing to undo.
	RollbackSkipProvenance = "not-written-by-this-engine"
	// RollbackSkipBackupMissing means the recorded backup_ref no longer exists.
	RollbackSkipBackupMissing = "backup-missing"
	// RollbackSkipBackupUnreadable means the recorded archive exists but is not a
	// regular file this engine can read back.
	RollbackSkipBackupUnreadable = "backup-unreadable"
	// RollbackSkipBackupOutside means the recorded backup_ref does not sit inside
	// the agy-pro backup tree, so it is not an archive this engine produced (a
	// hand-edited or corrupted manifest).
	RollbackSkipBackupOutside = "backup-outside-backup-tree"
)

// RollbackDestination is one manifest-owned destination and what a rollback did
// to it, or (from PendingRollback) would do to it.
type RollbackDestination struct {
	// Path is the absolute destination the entry claims.
	Path string
	// Action is restore, remove or skip.
	Action RollbackAction
	// Provenance is who the manifest says wrote the destination last.
	Provenance Provenance
	// BackupRef is the archive the entry recorded; empty when it recorded none.
	BackupRef string
	// Source is the archive the restored bytes come from; set on a restore only.
	Source string
	// LiveSHA is the digest found at the destination before the rollback; empty
	// when the destination was already gone.
	LiveSHA string
	// Archive is where the bytes the rollback replaced went; empty when nothing
	// was archived (a skip, or a restore that had nothing to overwrite).
	Archive string
	// ResultSHA is the digest the destination holds afterwards; empty when the
	// destination is absent afterwards.
	ResultSHA string
	// Wrote reports whether the destination's bytes were touched. A restore that
	// finds the archived bytes already in place writes nothing and still counts
	// as a restore: the destination is at its pre-install state.
	Wrote bool
	// SkipReason is one of the RollbackSkip constants; set on a skip only.
	SkipReason string
	// Detail is a short English sentence for a log or a test; a localized
	// renderer keys off Action and SkipReason instead.
	Detail string
}

// Restored reports whether the destination was put back to its archived bytes.
func (d RollbackDestination) Restored() bool { return d.Action == RollbackActionRestore }

// Removed reports whether the destination was moved out of the config tree.
func (d RollbackDestination) Removed() bool { return d.Action == RollbackActionRemove }

// Skipped reports whether the destination was left alone.
func (d RollbackDestination) Skipped() bool { return d.Action == RollbackActionSkip }

// RollbackSummary is what one rollback did.
type RollbackSummary struct {
	// Feature is the feature rolled back.
	Feature Feature
	// DestDir is the destination root that was rolled back.
	DestDir string
	// BackupDir is where the pre-rollback bytes were archived; empty when no
	// destination byte was replaced or moved.
	BackupDir string
	// Destinations are the manifest-owned destinations the rollback handled, in
	// path order.
	Destinations []RollbackDestination
	// ManifestUpdated reports whether the bookkeeping was rewritten.
	ManifestUpdated bool
	// Detail says what an empty run means, e.g. that there was nothing to roll
	// back; empty for a run that restored or removed something.
	Detail string
}

// Restored counts the destinations put back to their archived bytes.
func (s *RollbackSummary) Restored() int {
	if s == nil {
		return 0
	}
	restored := 0
	for _, destination := range s.Destinations {
		if destination.Restored() {
			restored++
		}
	}
	return restored
}

// Removed counts the destinations moved out of the config tree.
func (s *RollbackSummary) Removed() int {
	if s == nil {
		return 0
	}
	removed := 0
	for _, destination := range s.Destinations {
		if destination.Removed() {
			removed++
		}
	}
	return removed
}

// Skipped counts the destinations the rollback left alone.
func (s *RollbackSummary) Skipped() int {
	if s == nil {
		return 0
	}
	skipped := 0
	for _, destination := range s.Destinations {
		if destination.Skipped() {
			skipped++
		}
	}
	return skipped
}

// Changed counts the destinations the rollback resolved (restored or removed),
// whether or not their bytes had to be rewritten.
func (s *RollbackSummary) Changed() int {
	return s.Restored() + s.Removed()
}

// rollbackStep is one classified destination, before anything is written.
type rollbackStep struct {
	entry      ManifestEntry
	action     RollbackAction
	source     string
	restore    []byte
	liveSHA    string
	skipReason string
	detail     string
	// write reports that the destination's bytes still have to be replaced: a
	// remove always does, a restore does unless the archived bytes are already in
	// place.
	write bool
	// dropClaim reports that the manifest entry is resolved by this step and must
	// not survive the run.
	dropClaim bool
}

// rollbackClassification is one feature's rollback, read but not written.
type rollbackClassification struct {
	layout   FeatureLayout
	manifest *Manifest
	steps    []rollbackStep
}

// RollbackFeature returns a deployed content feature to the bytes it held before
// the install.
//
// Order of operations, deliberately (the same shape Apply uses, deploy.go:329):
//
//  1. Refuse a feature this engine does not deploy, before any filesystem work.
//     The statusline door has its own verb (RollbackStatusLine); the refusal
//     names that fact instead of silently doing nothing.
//  2. Load the manifest. A corrupt or too-new manifest stops the run with the
//     config tree untouched (fail closed).
//  3. Classify EVERY destination from the manifest and the live tree. This pass
//     reads only: each destination is either a restore (the entry records a
//     backup_ref), a remove (go-applied with no backup_ref) or a named skip.
//  4. Act in path order. A restore archives the bytes it replaces into
//     ~/.agy-pro/backups/<ts>_rollback and writes the archived ones; a remove
//     moves the file into that same directory.
//  5. Drop the claims of the destinations the run resolved, and save the
//     manifest once.
//
// A failed step stops the run: the destinations resolved before it stay on disk,
// the manifest is not saved, and the error says so, so the caller reads the tree
// as ahead of the bookkeeping rather than as untouched.
//
// Rolling back twice is stable: the first run drops the claims it resolved, so
// the second run finds no entries and writes nothing at all.
func RollbackFeature(feature Feature) (*RollbackSummary, error) {
	deployMu.Lock()
	defer deployMu.Unlock()

	classified, err := classifyRollback(feature)
	if err != nil {
		return nil, err
	}

	summary := &RollbackSummary{
		Feature:      feature,
		DestDir:      classified.layout.DestDir,
		Destinations: make([]RollbackDestination, 0, len(classified.steps)),
	}
	backupDir := ""
	moved := false
	dropped := make([]string, 0, len(classified.steps))

	for _, step := range classified.steps {
		result := rollbackDestinationOf(step)
		switch step.action {
		case RollbackActionRestore:
			if step.write {
				archive, archiveErr := archiveDestinationTagged(classified.layout.DestDir, step.entry.Path, &backupDir, rollbackBackupTag)
				if archiveErr != nil {
					return summary, rollbackFailure(archiveErr, summary, backupDir)
				}
				result.Archive = archive
				if writeErr := deployWriteFile(step.entry.Path, step.restore, deployFilePerm); writeErr != nil {
					return summary, rollbackFailure(fmt.Errorf("%w: restore %q: %v", ErrDeployWrite, step.entry.Path, writeErr), summary, backupDir)
				}
				result.Wrote = true
			}
		case RollbackActionRemove:
			archive, moveErr := moveDestinationToBackupTagged(classified.layout.DestDir, step.entry.Path, &backupDir, rollbackBackupTag)
			if moveErr != nil {
				return summary, rollbackFailure(moveErr, summary, backupDir)
			}
			result.Archive = archive
			result.Wrote = true
			moved = true
		}
		if step.dropClaim {
			dropped = append(dropped, step.entry.Path)
		}
		summary.Destinations = append(summary.Destinations, result)
	}

	if moved {
		// The move is the removal, and it can leave the directories the apply
		// created empty. Retract prunes those (deploy.go:955); a rollback leaves
		// the same empty skeleton otherwise.
		pruneEmptyDirs(classified.layout.DestDir)
	}

	dirty := false
	for _, path := range dropped {
		if classified.manifest.Delete(path) {
			dirty = true
		}
	}
	if dirty {
		if err := SaveManifest(classified.manifest); err != nil {
			return summary, fmt.Errorf("%d destination(s) were rolled back but the manifest was not saved: %w",
				summary.Changed(), err)
		}
		summary.ManifestUpdated = true
	}

	summary.BackupDir = backupDir
	summary.Detail = rollbackDetail(summary)
	return summary, nil
}

// PendingRollback classifies what RollbackFeature would do and writes nothing:
// no lock, no manifest write, no destination touched. It is the advisory view a
// consent gate renders before the write, the same role SettingsSnapshots plays
// for RollbackStatusLine (settings.go:730).
//
// The classification is advisory only: RollbackFeature re-classifies under
// deployMu, so a destination that moves between the two reads is acted on as the
// execute pass finds it, not as the preview showed it.
func PendingRollback(feature Feature) ([]RollbackDestination, error) {
	classified, err := classifyRollback(feature)
	if err != nil {
		return nil, err
	}
	out := make([]RollbackDestination, 0, len(classified.steps))
	for _, step := range classified.steps {
		out = append(out, rollbackDestinationOf(step))
	}
	return out, nil
}

// classifyRollback reads the manifest and the live tree for one feature and
// decides what a rollback would do to each destination. It never writes, and it
// refuses the statusline door and unknown features before anything else.
func classifyRollback(feature Feature) (*rollbackClassification, error) {
	layout, err := LayoutFor(feature)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	backupsRoot, err := paths.AgyProBackups()
	if err != nil {
		return nil, err
	}

	owned := ownedDestinations(manifest, layout)
	classified := &rollbackClassification{
		layout:   layout,
		manifest: manifest,
		steps:    make([]rollbackStep, 0, len(owned)),
	}
	for _, entry := range owned {
		step, stepErr := classifyRollbackStep(entry, backupsRoot)
		if stepErr != nil {
			return nil, stepErr
		}
		classified.steps = append(classified.steps, step)
	}
	return classified, nil
}

// classifyRollbackStep decides one destination's fate from the manifest facts
// alone: the entry's backup_ref, its provenance, and the live file.
func classifyRollbackStep(entry ManifestEntry, backupsRoot string) (rollbackStep, error) {
	step := rollbackStep{entry: entry, action: RollbackActionSkip}

	liveSHA, present, err := liveDestination(entry.Path)
	if err != nil {
		return step, err
	}
	step.liveSHA = liveSHA
	if !present {
		step.skipReason = RollbackSkipDestinationMissing
		step.detail = "the recorded destination is already gone; a rollback does not resurrect a deleted file (re-apply repairs missing destinations)"
		return step, nil
	}

	if entry.BackupRef == "" {
		if entry.Provenance != ProvenanceGoApplied {
			step.skipReason = provenanceSkipReason(entry.Provenance)
			step.detail = fmt.Sprintf("the manifest says %q left these bytes, not this engine; nothing was written here to undo",
				string(entry.Provenance))
			return step, nil
		}
		// An apply that created the file records no backup_ref, so the pre-install
		// state of this destination is "absent". Moving it out is how the engine
		// takes a file back without deleting it.
		step.action = RollbackActionRemove
		step.write = true
		step.dropClaim = true
		step.detail = "the apply created this destination, so the rollback moves it into the backup tree"
		return step, nil
	}

	source, inside := backupSourceInside(backupsRoot, entry.BackupRef)
	if !inside {
		step.skipReason = RollbackSkipBackupOutside
		step.detail = fmt.Sprintf("the recorded backup %q is not inside %q, so it is not an archive this engine wrote",
			entry.BackupRef, backupsRoot)
		return step, nil
	}
	info, statErr := os.Stat(source)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			step.skipReason = RollbackSkipBackupMissing
			step.detail = fmt.Sprintf("the recorded backup %q no longer exists; the live file is left as it is", source)
			return step, nil
		}
		return step, fmt.Errorf("cannot inspect the recorded backup %q: %w", source, statErr)
	}
	if !info.Mode().IsRegular() {
		step.skipReason = RollbackSkipBackupUnreadable
		step.detail = fmt.Sprintf("the recorded backup %q is not a regular file; the live file is left as it is", source)
		return step, nil
	}
	restore, readErr := os.ReadFile(source)
	if readErr != nil {
		step.skipReason = RollbackSkipBackupUnreadable
		step.detail = fmt.Sprintf("the recorded backup %q cannot be read back (%v); the live file is left as it is", source, readErr)
		return step, nil
	}

	step.action = RollbackActionRestore
	step.source = source
	step.restore = restore
	step.dropClaim = true
	if SHA256Hex(restore) == liveSHA {
		step.detail = fmt.Sprintf("the destination already holds the bytes archived in %q; nothing to write", source)
		return step, nil
	}
	step.write = true
	step.detail = fmt.Sprintf("restores the bytes archived in %q", source)
	return step, nil
}

// provenanceSkipReason maps a provenance that is not this engine's own write onto
// its skip reason.
func provenanceSkipReason(provenance Provenance) string {
	if provenance == ProvenanceAdopted {
		return RollbackSkipAdopted
	}
	return RollbackSkipProvenance
}

// backupSourceInside normalizes a recorded backup_ref and reports whether it sits
// inside the agy-pro backup tree. A ref that is relative, blank, or walks out of
// the tree is not an archive this engine produced: the manifest is a file a user
// can edit, and a rollback must not copy an arbitrary path into the config tree.
func backupSourceInside(backupsRoot, ref string) (string, bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return "", false
	}
	cleanRef := filepath.Clean(trimmed)
	root := filepath.Clean(backupsRoot)
	rel, err := filepath.Rel(root, cleanRef)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return cleanRef, true
}

// rollbackDestinationOf renders one classified step as the public result. It is
// shared by the preview and the execute pass so the two can never disagree about
// what a step means.
func rollbackDestinationOf(step rollbackStep) RollbackDestination {
	result := RollbackDestination{
		Path:       step.entry.Path,
		Action:     step.action,
		Provenance: step.entry.Provenance,
		BackupRef:  step.entry.BackupRef,
		LiveSHA:    step.liveSHA,
		SkipReason: step.skipReason,
		Detail:     step.detail,
	}
	if step.action == RollbackActionRestore {
		result.Source = step.source
		result.ResultSHA = SHA256Hex(step.restore)
	} else if step.action == RollbackActionSkip {
		// A skip leaves the destination exactly as it was, so what it holds
		// afterwards is what it held before.
		result.ResultSHA = step.liveSHA
	}
	return result
}

// rollbackFailure annotates a failure that happened after some destinations were
// already resolved. The manifest is deliberately not saved in that case, so the
// caller reads the tree as ahead of the bookkeeping instead of as untouched.
func rollbackFailure(cause error, summary *RollbackSummary, backupDir string) error {
	if summary != nil {
		summary.BackupDir = backupDir
	}
	changed := 0
	if summary != nil {
		changed = summary.Changed()
	}
	if changed == 0 {
		return cause
	}
	return fmt.Errorf("%d destination(s) were rolled back before the failure and the manifest was not saved: %w", changed, cause)
}

// rollbackDetail says what an empty run means, so a caller never has to read
// "no error and no destinations" as a silent success.
func rollbackDetail(summary *RollbackSummary) string {
	if summary == nil {
		return ""
	}
	switch {
	case len(summary.Destinations) == 0:
		return "nothing to roll back: the manifest records no destination for this feature"
	case summary.Changed() == 0:
		return fmt.Sprintf("nothing to roll back: %d recorded destination(s) were skipped", summary.Skipped())
	default:
		return ""
	}
}
