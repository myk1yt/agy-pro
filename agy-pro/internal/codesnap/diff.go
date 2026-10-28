package codesnap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"agypro/internal/agenttools"
)

// Delta statuses. They are the literal values FileDelta.Status carries and the
// letters RenderDeltas prints.
const (
	// StatusCreated means the file exists on disk inside a recorded scope but
	// no manifest row names it: the run created it.
	StatusCreated = "created"
	// StatusModified means the file exists in both places with different bytes.
	StatusModified = "modified"
	// StatusDeleted means a manifest row's file is gone from disk.
	StatusDeleted = "deleted"
)

const (
	// defaultRenderRows bounds RenderDeltas when the caller passes maxRows <= 0.
	defaultRenderRows = 20

	// maxDeltaScanBytes bounds the per-file LINE-TRIM work of Diff. Below it the
	// counts are exact (common prefix + common suffix trimmed, everything in
	// between counted). Above it the file is reported as wholly replaced:
	// added = new line count, removed = old line count. Either way the number is
	// a pure function of the two byte strings, so it is stable across runs and
	// hosts.
	maxDeltaScanBytes = 1 << 20
)

// FileDelta is one changed file. Added/Removed are LINE counts (the heuristic
// documented on lineDelta), not byte counts; Status and New carry the same fact
// in two forms (New is the boolean a report writer switches on).
type FileDelta struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	New     bool   `json:"new"`
}

// Diff compares the live tree against a manifest and returns the changed files,
// sorted by path (created, modified and deleted all included).
//
// Source of truth: the manifest alone. A row counts as modified/deleted because
// its recorded sha256 differs from / is missing on disk, never because a
// timestamp moved or a scan guessed. Files present on disk inside the recorded
// scopes but absent from the manifest are StatusCreated - the same rule rollback
// uses to decide what to archive - EXCEPT the paths the manifest recorded as
// withheld (ReasonSecretClass): those were never captured, so calling them
// "created by the run" would be a false claim in both directions.
//
// "Absent from the manifest" is decided on the EXACT root-relative path (FIX-E1,
// finding N1): the comparison is never name-folded, so a Unicode look-alike of a
// recorded name is reported as the creation it is instead of being merged into
// the row it resembles.
//
// Root identity: the manifest is applied only to the root it was taken from,
// compared through sameRoot (volume-aware, case-folded on Windows) so a
// case-variant spelling of the same directory is accepted while a different
// directory is refused.
//
// KNOWN GAP, stated so it is not mistaken for coverage: a secret-class file the
// run CREATES after the snapshot is not reported here at all. It has no baseline
// (it was never captured) and this package has no status for "deliberately
// untracked", so silence is the honest answer; Rollback is where such a file is
// named, in rollback.json skipped[].
//
// Fail closed: a nil or unparseable manifest, a manifest naming a
// governance/credential, VCS or secret-class row, a root that does not match
// manifest.Root, a row path that does not resolve inside the root, a missing or
// hash-mismatched snapshot copy, or an unreadable live file all return a named
// error instead of a partial delta list.
func Diff(root string, m *Manifest) ([]FileDelta, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: no manifest", ErrManifestCorrupt)
	}
	if err := validateManifest(m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestCorrupt, err)
	}
	if err := checkManifestPathPolicy(m); err != nil {
		return nil, err
	}
	rootAbs, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	if !sameRoot(rootAbs, m.Root) {
		return nil, fmt.Errorf("%w: manifest root %q, target root %q", ErrRootMismatch, m.Root, rootAbs)
	}

	deltas := []FileDelta{}
	withheld := withheldPaths(m)
	recorded := make(map[string]bool, len(m.Files))
	for _, row := range m.Files {
		recorded[row.Path] = true
		delta, changed, derr := diffRow(rootAbs, m, row)
		if derr != nil {
			return nil, derr
		}
		if changed {
			deltas = append(deltas, delta)
		}
	}

	// Created files: scoped claims the manifest does not name, minus the paths it
	// recorded as withheld (a withheld file was never captured, so it is not a
	// creation either). Both lookups are EXACT (FIX-E1, finding N1): file identity
	// is never folded, because a folded key merges names the filesystem keeps apart
	// and would hide a run-created "src/prınce.txt" behind a recorded
	// "src/prince.txt" - the undo-net escape SEC r4 F1 measured. A file the run
	// created is therefore always reported, whatever it is called.
	//
	// Diff passes the ledger policy the two writers use (FIX-F4, Q3) so the walk's
	// fingerprint test is measured the same way everywhere; it ignores the result,
	// because Diff writes nothing and a run ledger a scope could claim is a TAKE-side
	// integrity question.
	runDir := filepath.Dir(m.SnapshotDir)
	walk, werr := walkScoped(rootAbs, m.Scopes, runDir, policyFor(rootAbs), ledgerPolicyFor(rootAbs, canonicalDirish(runDir)))
	if werr != nil {
		return nil, werr
	}
	for _, rel := range walk.claims {
		if recorded[rel] || withheld[rel] {
			continue
		}
		data, rerr := readRootFile(rootAbs, rel)
		if rerr != nil {
			return nil, fmt.Errorf("codesnap: diff: read created file %q: %w", rel, rerr)
		}
		deltas = append(deltas, FileDelta{
			Path:   rel,
			Status: StatusCreated,
			Added:  countLines(data),
			New:    true,
		})
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].Path < deltas[j].Path })
	return deltas, nil
}

// diffRow classifies one manifest row against the live tree.
func diffRow(rootAbs string, m *Manifest, row ManifestRow) (FileDelta, bool, error) {
	abs, err := absUnderRoot(rootAbs, row.Path)
	if err != nil {
		return FileDelta{}, false, fmt.Errorf("%w: %v", ErrManifestCorrupt, err)
	}
	if _, serr := os.Lstat(abs); serr != nil {
		if !os.IsNotExist(serr) {
			return FileDelta{}, false, fmt.Errorf("codesnap: diff: stat %q: %w", row.Path, serr)
		}
		oldData, oerr := readSnapshotCopy(m, row)
		if oerr != nil {
			return FileDelta{}, false, oerr
		}
		return FileDelta{Path: row.Path, Status: StatusDeleted, Removed: countLines(oldData)}, true, nil
	}
	newData, rerr := readRootFile(rootAbs, row.Path)
	if rerr != nil {
		return FileDelta{}, false, fmt.Errorf("codesnap: diff: read %q: %w", row.Path, rerr)
	}
	if sha256Hex(newData) == row.SHA256 {
		return FileDelta{}, false, nil
	}
	oldData, oerr := readSnapshotCopy(m, row)
	if oerr != nil {
		return FileDelta{}, false, oerr
	}
	added, removed := lineDelta(oldData, newData)
	return FileDelta{Path: row.Path, Status: StatusModified, Added: added, Removed: removed}, true, nil
}

// RenderDeltas renders one line per delta ("A|M|D path +N -N"), bounded to
// maxRows lines with a trailing "... K more" when the list is longer. Rows are
// rendered in the caller's slice order (Diff sorts by path, so the output is
// deterministic). No trailing newline; an empty delta list renders "".
func RenderDeltas(deltas []FileDelta, maxRows int) string {
	if maxRows <= 0 {
		maxRows = defaultRenderRows
	}
	var b bytes.Buffer
	for i, d := range deltas {
		if i == maxRows {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "... %d more", len(deltas)-maxRows)
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%c %s +%d -%d", deltaLetter(d.Status), d.Path, d.Added, d.Removed)
	}
	return b.String()
}

// deltaLetter maps a status to the one-character marker RenderDeltas prints. An
// unknown status renders as "M": a delta a caller invented is still a delta.
func deltaLetter(status string) byte {
	switch status {
	case StatusCreated:
		return 'A'
	case StatusDeleted:
		return 'D'
	default:
		return 'M'
	}
}

// readSnapshotCopy reads the captured bytes for a row and verifies them against
// the recorded digest. A snapshot copy that is missing or does not match its row
// is a hard error: every count derived from it would be a lie, and rollback
// would restore the wrong bytes.
func readSnapshotCopy(m *Manifest, row ManifestRow) ([]byte, error) {
	abs, err := absUnderRoot(m.SnapshotDir, row.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: snapshot row %q: %v", ErrManifestCorrupt, row.Path, err)
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, fmt.Errorf("%w: snapshot copy missing for %q (%s)", ErrManifestCorrupt, row.Path, abs)
		}
		return nil, fmt.Errorf("codesnap: read snapshot copy of %q: %w", row.Path, rerr)
	}
	if sum := sha256Hex(data); sum != row.SHA256 {
		return nil, fmt.Errorf("%w: snapshot copy of %q has sha256 %s, manifest records %s",
			ErrManifestCorrupt, row.Path, sum, row.SHA256)
	}
	return data, nil
}

// readRootFile reads a validated root-relative path, refusing to follow a
// symlink out of the root (the dual-view rule of the package comment, applied to
// this package's own reads).
func readRootFile(rootAbs, rel string) ([]byte, error) {
	abs, err := absUnderRoot(rootAbs, rel)
	if err != nil {
		return nil, err
	}
	if _, rerr := agenttools.ResolvedRelPath(rootAbs, abs); rerr != nil {
		return nil, fmt.Errorf("%q does not resolve inside the root: %w", rel, rerr)
	}
	return os.ReadFile(abs)
}

// lineDelta counts added and removed lines between two versions of one file.
//
// Heuristic, documented as approximate-but-stable: trim the common LINE prefix
// and the common LINE suffix, then count the lines left in the middle of each
// side. It is not an LCS diff (no per-hunk alignment, no move detection) and it
// never merges changes, so "+3 -4" reads as "3 lines appeared and 4 disappeared
// in the changed middle", which is what a reviewer takes it to mean. The result
// is a pure function of the two byte strings: no clock, no map order, no
// randomness.
//
// Cost is O(bytes) time and O(1) extra memory (byte offsets, no line slices).
// Above maxDeltaScanBytes the trim is skipped and the file is reported as wholly
// replaced (added = new line count, removed = old line count), which keeps the
// per-file work a linear scan of the input.
func lineDelta(oldData, newData []byte) (added, removed int) {
	if bytes.Equal(oldData, newData) {
		return 0, 0
	}
	if len(oldData)+len(newData) > maxDeltaScanBytes {
		return countLines(newData), countLines(oldData)
	}
	oldStart, oldEnd := 0, len(oldData)
	newStart, newEnd := 0, len(newData)
	// Common prefix, line by line.
	for oldStart < oldEnd && newStart < newEnd {
		oldLen := firstLineLength(oldData[oldStart:oldEnd])
		newLen := firstLineLength(newData[newStart:newEnd])
		if !bytes.Equal(oldData[oldStart:oldStart+oldLen], newData[newStart:newStart+newLen]) {
			break
		}
		oldStart += oldLen
		newStart += newLen
	}
	// Common suffix, line by line, without crossing the prefix.
	for oldEnd > oldStart && newEnd > newStart {
		oldLineStart := oldStart + lastLineOffset(oldData[oldStart:oldEnd])
		newLineStart := newStart + lastLineOffset(newData[newStart:newEnd])
		if !bytes.Equal(oldData[oldLineStart:oldEnd], newData[newLineStart:newEnd]) {
			break
		}
		oldEnd, newEnd = oldLineStart, newLineStart
	}
	return countLines(newData[newStart:newEnd]), countLines(oldData[oldStart:oldEnd])
}

// firstLineLength returns the byte length of the first line of b, newline
// included (a final line without a newline returns its remaining length).
func firstLineLength(b []byte) int {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return i + 1
	}
	return len(b)
}

// lastLineOffset returns the offset of the start of the last line of b.
func lastLineOffset(b []byte) int {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	if i := bytes.LastIndexByte(b[:end], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// countLines counts the lines of b: newline terminators plus a trailing
// unterminated line, so "" is 0 lines and "a\n" is 1.
func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}
