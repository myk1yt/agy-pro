// Rollback: put a workspace back to the bytes a snapshot recorded, without ever
// deleting anything.
//
// The rules of this file (the NEVER DELETE trio of integrations/rollback.go:10-14
// applied to a workspace tree, plus the S3 restore-side gate):
//
//   - NEVER DELETE. A file present in a recorded scope but absent from the
//     manifest was created by the run, so it is MOVED into
//     <destDir>/rollback-archive/<relpath>. If the move fails (a locked file, a
//     cross-volume destination, an archive target that already exists) the file
//     stays exactly where it is and rollback.json names it in skipped[]. Nothing
//     in this file removes a workspace file, and the package has exactly TWO
//     removal call sites, neither of which can reach a file the run owns: the
//     key-store retention prune (sig.go pruneLedgerKeys), which works inside the
//     agy-pro state home and never touches a root, and the case-fold probe's
//     cleanup of the throw-away directory it created seconds earlier (FIX-F5, W8;
//     the count is FIX-G1).
//   - THE MANIFEST IS THE ONLY SOURCE OF TRUTH. What gets restored is decided by
//     the recorded sha256, and what gets archived is decided by the recorded
//     scope set. No timestamp comparison, no git state, no "looks original"
//     heuristic.
//   - A MISSING OR CORRUPT SOURCE IS A NAMED FAILURE, NOT A FALLBACK. A manifest
//     that cannot be read, a snapshot copy whose bytes do not match its row, or a
//     root that is not the root the manifest was taken from stops the rollback
//     before a single byte is written.
//   - NOTHING GOVERNANCE-SHAPED IS RESTORED OR MOVED (S3). Every restore target
//     and every archive destination passes the same governance/credential,
//     VCS-metadata and canonical-form refusals the seat write guard applies
//     (agenttools.GovernanceWriteRefusal guard.go:574, VCSMetadataRefusal
//     guard.go:728, CanonicalPathComponentOK guard.go:558, whose component scan
//     carries the stream-syntax and 8.3 shape arms too). A manifest that
//     names such a row is refused WHOLE at load time - never a selective restore
//     of the rows that look safe - and an archive move onto such a destination is
//     refused per file, which leaves the file exactly where it is.
//   - THE RESOLVED DIRECTORY CHAIN IS GATED TOO (FIX-E1, N6). The row policy above
//     is lexical, so a row "link/state.json" whose "link" resolves into .agy-pro
//     passes it. Both the restore preflight (refuseResolvedChainRows) and every
//     archive move run agenttools.ResolvedDirChainRefusal - the same ancestor walk
//     the write guard uses (guard.go:187-198) - and a row whose chain resolves
//     into a governance/credential directory or VCS metadata refuses the whole
//     manifest before the first write.
//   - WITHHELD FILES ARE LEFT ALONE (S5). A path the manifest recorded as
//     ReasonSecretClass is neither restored nor archived: moving it would copy
//     the same bytes into the run directory by another route.
//   - THE RUN DIRECTORY IS OFF LIMITS (S3). A manifest whose scopes could claim
//     the run directory is refused before the tree is touched, and every archive
//     destination must resolve inside it.
package codesnap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

const (
	archiveDirName   = "rollback-archive"
	rollbackFileName = "rollback.json"
)

// ErrRollbackRefused is wrapped when the rollback stops before touching the
// tree (see Rollback's preflight step). Callers can test errors.Is against it to
// tell "refused, nothing changed" from "changed, with skips".
var ErrRollbackRefused = errors.New("codesnap: rollback refused")

// RollbackReport is <destDir>/rollback.json: what the rollback did, file by
// file. Every list is sorted and non-nil, so the JSON carries explicit empty
// arrays instead of a missing key. Restored/Unchanged name files by their
// root-relative path; Archived names the ARCHIVE-relative destination
// ("rollback-archive/<relpath>"), which is where the bytes now live.
type RollbackReport struct {
	RestoredAt string        `json:"restored_at"`
	Restored   []string      `json:"restored"`
	Archived   []string      `json:"archived"`
	Unchanged  []string      `json:"unchanged"`
	Skipped    []SkippedFile `json:"skipped"`
}

// RestoredCount is the number of files put back to their recorded bytes.
func (r *RollbackReport) RestoredCount() int {
	if r == nil {
		return 0
	}
	return len(r.Restored)
}

// Rollback restores the tree under root to the state its manifest recorded and
// archives every file the run created.
//
// Order of operations, chosen so a failure is always the safe failure:
//
//  1. Load and validate <destDir>/manifest.json (missing/corrupt => named error)
//     and refuse the whole manifest if any row names a governance/credential,
//     VCS-metadata or secret-class path (S3/S5: a hand-forged manifest must not
//     restore one, and the snapshot could never have produced such a row).
//  2. Refuse a root that is not manifest.Root (sameRoot: volume-aware and
//     case-folded on Windows, after the symlink resolution Take used).
//  3. Refuse a manifest whose scope could claim the run directory (S3).
//  4. Enumerate the archive claims now, before the first write, so a scope that
//     reaches a governance/credential or VCS path refuses the rollback while the
//     tree is still untouched.
//  5. Preflight every snapshot copy and verify its sha256 against its row. Any
//     mismatch refuses the whole rollback BEFORE the tree is touched.
//  6. Restore: a row whose live bytes differ from the recorded digest (or whose
//     file is gone) is rewritten atomically with the recorded mode. A row that
//     already matches is "unchanged" and is not touched.
//  7. Archive: MOVE every claimed file the manifest neither records nor withheld
//     into <destDir>/rollback-archive/<relpath>.
//  8. Write <destDir>/rollback.json.
//
// Rollback is idempotent: a second call over the same destDir reports every
// manifest row as unchanged and archives nothing, because the created files are
// already in the archive and the restored bytes match their digests.
func Rollback(root, destDir string) (*RollbackReport, error) {
	m, err := LoadManifest(destDir)
	if err != nil {
		return nil, err
	}
	rootAbs, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	if !sameRoot(rootAbs, m.Root) {
		return nil, fmt.Errorf("%w: manifest root %q, rollback root %q", ErrRootMismatch, m.Root, rootAbs)
	}
	// The containment half of the key store applies to the READ door too (FIX-F5,
	// W2). Take refuses to write a key inside the tree a seat can reach; rollback
	// must refuse to read one there, or the same layout planted after a take would
	// make the restore trust a store the operator does not control. Fail-closed, and
	// symmetric with Take by construction: both doors call this one function.
	if kerr := refuseKeysInsideRoot(rootAbs); kerr != nil {
		return nil, kerr
	}
	destAbs, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return nil, fmt.Errorf("codesnap: rollback destination %s: %w", destDir, err)
	}
	// Same path space as the resolved root (see canonicalDirish): a ledger spelled
	// through the root's own link must still be recognized as inside it.
	destSpace := canonicalDirish(destAbs)
	if sameRoot(destSpace, rootAbs) {
		return nil, fmt.Errorf("codesnap: rollback destination must not be the root itself (%s)", rootAbs)
	}
	ledgerRel, ledgerInside := rootRelativeDir(rootAbs, destSpace)
	policy := policyFor(rootAbs)
	// The ledger claim tests use the more-refusing of the root's fold and the
	// ledger's own subtree (FIX-F4, Q3); the walk exclusion keeps the root policy.
	claimPolicy := ledgerPolicyFor(rootAbs, destSpace)
	if ledgerInside {
		if entry, hit := ledgerClaimingEntry(claimPolicy, m.Scopes, ledgerRel); hit {
			return nil, fmt.Errorf("%w: manifest scope entry %q can claim %q, which is inside the run directory %q; a ledger inside its own scope can be rewritten by the run it describes", ErrLedgerRefused, entry, ledgerRel, ledgerRel)
		}
	}

	// N6 (FIX-E1): the RESOLVED DIRECTORY CHAIN gate. The row policy above is
	// lexical, while every write tool also resolves the directory chain a target
	// traverses (agenttools.refuseProtectedWrite -> resolvedWriteRelPath, guard.go
	// ResolvedDirChainRefusal). A row "link/registry-keys.v2.json" whose "link"
	// resolves to ".agy-pro" carries no governed component in its written form, so
	// only this arm sees it. Refused WHOLE, before the walk and before the first
	// write, exactly like a governed row: never a selective restore of the rows
	// that look safe.
	if err := refuseResolvedChainRows(rootAbs, m); err != nil {
		return nil, err
	}

	// Archive claims first: a governance/VCS claim inside the recorded scopes
	// refuses here, while nothing has been written yet.
	walk, werr := walkScoped(rootAbs, m.Scopes, destSpace, policy, claimPolicy)
	if werr != nil {
		return nil, werr
	}
	// Q1 (FIX-F4) on the restore side: a manifest whose scope could claim ANY
	// existing run ledger refuses whole, for the same reason the take refuses to
	// produce one - such a run had write access to another run's baseline, so its
	// own manifest cannot be trusted to describe this tree.
	if len(walk.ledgerDirs) > 0 {
		return nil, refusalForDiscoveredLedger(rootAbs, m.Scopes, walk.ledgerDirs[0])
	}

	report := &RollbackReport{
		RestoredAt: time.Now().UTC().Format(time.RFC3339),
		Restored:   []string{},
		Archived:   []string{},
		Unchanged:  []string{},
		Skipped:    []SkippedFile{},
	}
	report.Skipped = append(report.Skipped, walk.refusals...)
	// Withheld rows: reported by name, and never restored or archived. The
	// manifest rows are authoritative; a withheld claim the walk found that the
	// manifest does not name was created after the snapshot. The lookups are EXACT
	// paths (FIX-E1, finding N1): file identity is never folded.
	withheld := withheldPaths(m)
	for _, skip := range m.Skipped {
		report.Skipped = append(report.Skipped, SkippedFile{Path: skip.Path, Reason: skip.Reason + "; left in place by rollback"})
	}
	for _, skip := range walk.withheld {
		if withheld[skip.Path] {
			continue
		}
		withheld[skip.Path] = true
		report.Skipped = append(report.Skipped, SkippedFile{Path: skip.Path, Reason: skip.Reason + "; created after the snapshot, left in place"})
	}

	// Preflight: every restore source must exist and match its recorded digest
	// BEFORE the tree is touched. The bytes are not kept here: the restore loop
	// re-reads each copy (and re-verifies it), so a large scoped tree never sits
	// in memory all at once.
	for _, row := range m.Files {
		if _, rerr := readSnapshotCopy(m, row); rerr != nil {
			return nil, fmt.Errorf("%w: preflight failed: %w", ErrRollbackRefused, rerr)
		}
	}

	recorded := make(map[string]bool, len(m.Files))
	for _, row := range m.Files {
		recorded[row.Path] = true
		// Containment authority for the write side: ResolveSafePath refuses a
		// target that escapes root through ".." or through a symlinked ancestor
		// (guard.go:14), so a manifest that names a linked path cannot make this
		// package write outside the root. The governance/VCS/canonical refusals
		// already ran on every row at load time (checkManifestPathPolicy).
		target, serr := agenttools.ResolveSafePath(rootAbs, filepath.FromSlash(row.Path))
		if serr != nil {
			report.Skipped = append(report.Skipped, SkippedFile{Path: row.Path, Reason: "unsafe restore target: " + serr.Error()})
			continue
		}
		if live, ok := liveDigest(target); ok && live == row.SHA256 {
			report.Unchanged = append(report.Unchanged, row.Path)
			continue
		}
		mode := os.FileMode(row.Mode)
		if mode == 0 {
			mode = artifactFilePerm
		}
		data, rerr := readSnapshotCopy(m, row)
		if rerr != nil {
			report.Skipped = append(report.Skipped, SkippedFile{Path: row.Path, Reason: "snapshot copy unreadable at restore time: " + rerr.Error()})
			continue
		}
		if werr := providers.WriteFileAtomic(target, data, mode); werr != nil {
			report.Skipped = append(report.Skipped, SkippedFile{Path: row.Path, Reason: "restore failed: " + werr.Error()})
			continue
		}
		report.Restored = append(report.Restored, row.Path)
	}

	// Archive: scoped files the manifest neither records nor withheld were
	// created by the run. A withheld file was never captured, so it is left where
	// it is rather than moved into the run directory.
	//
	// Both lookups are keyed by the EXACT root-relative path (FIX-E1, finding N1).
	// D1b folded them, which merged the governance fold's Unicode look-alikes with
	// the names they resemble - and a merge here is an undo-net escape: the run
	// creates "src/prınce.txt" next to a recorded "src/prince.txt", the folded
	// lookup answers "already recorded", and the file the run created is neither
	// archived nor reported (SEC r4 F1).
	//
	// The exact key has a mirror-image cost D1b measured: a manifest row spelled in
	// ANOTHER ASCII case names this file on a case-folding volume, and an exact key
	// does not see that. That case is answered by FILE identity instead of name
	// identity (sameAsRecorded below, over the ASCII-folded candidate index and
	// os.SameFile): the file is then left in place and named in skipped[], never
	// moved - so no spelling difference can make this package move a file the
	// manifest describes, and no look-alike name can make it skip one the manifest
	// does not. The candidate index is what keeps the restraint to CASE ALIASES only
	// (FIX-F2, finding P-4): a run-created hardlink under any other name is a created
	// file this rollback archives like any other.
	recordedFiles := indexRecordedFiles(rootAbs, m)
	for _, rel := range walk.claims {
		if recorded[rel] || withheld[rel] {
			continue
		}
		if rowPath, same := sameAsRecorded(recordedFiles, rootAbs, rel); same {
			report.Skipped = append(report.Skipped, SkippedFile{
				Path:   rel,
				Reason: "the manifest records this file under another spelling (" + rowPath + "); left in place (nothing is deleted)",
			})
			continue
		}
		archived, skipReason := archiveMove(rootAbs, destAbs, ledgerRel, rel, policy)
		if skipReason != "" {
			report.Skipped = append(report.Skipped, SkippedFile{Path: rel, Reason: skipReason})
			continue
		}
		report.Archived = append(report.Archived, archived)
	}

	sort.Strings(report.Restored)
	sort.Strings(report.Archived)
	sort.Strings(report.Unchanged)
	sortSkips(report.Skipped)
	if err := writeJSON(filepath.Join(destAbs, rollbackFileName), report); err != nil {
		return nil, err
	}
	return report, nil
}

// liveDigest returns the sha256 of the file at abs, and false when the file
// cannot be read (missing, a directory, an unreadable file): every such case is
// "not equal to the recorded bytes", which is what the restore path needs.
func liveDigest(abs string) (string, bool) {
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	return sha256Hex(data), true
}

// archiveMove moves one run-created file into the rollback archive and returns
// its ARCHIVE-relative path ("rollback-archive/<relpath>"). A non-empty second
// return is the skip reason; in that case the file was NOT touched and was NOT
// deleted.
//
// The move has three sides and EVERY one is gated (S3 + FIX-E1 N6):
//
//   - the source must not be inside the run directory (a ledger file is never
//     moved into the ledger), compared through the policy's directory-prefix form
//     so a run directory spelled in another ASCII case still owns its ledger
//     files (R3-7),
//   - the source's RESOLVED DIRECTORY CHAIN must be clear: a claimed
//     "link/registry-keys.v2.json" whose "link" resolves into .agy-pro moves a
//     governance file out of its directory under a name no lexical arm judged, so
//     the same agenttools.ResolvedDirChainRefusal the write guard uses is applied
//     here too, and
//   - the destination must pass the same governance/credential, VCS-metadata and
//     canonical-form refusals the seat write guard applies, and must resolve
//     inside destDir. A refused destination means the file stays exactly where
//     it is: refusing the move is the safe failure, because the archive is a copy
//     destination and the tree is the original.
//
// os.Rename is the only move attempted: it is atomic on one volume and fails
// cleanly on a cross-volume or locked destination, which is reported as a skip.
// A copy+delete fallback is deliberately NOT implemented - the delete half of it
// is the one operation this package must never perform.
func archiveMove(rootAbs, destAbs, ledgerRel, rel string, policy namePolicy) (archived string, skipReason string) {
	if policy.pathUnder(ledgerRel, rel) {
		return "", "source is inside the run directory; left in place (nothing is deleted)"
	}
	src, err := agenttools.ResolveSafePath(rootAbs, filepath.FromSlash(rel))
	if err != nil {
		return "", "unsafe archive source: " + err.Error()
	}
	resolvedDir, comp, refused, cerr := agenttools.ResolvedDirChainRefusal(rootAbs, filepath.FromSlash(rel))
	if cerr != nil {
		return "", "archive source directory chain cannot be resolved; left in place (nothing is deleted): " + cerr.Error()
	}
	if refused {
		return "", "archive source transits " + resolvedDir + ", which resolves to the governance/credential or VCS path component " + comp + "; left in place (nothing is deleted)"
	}
	archivedRel := archiveDirName + "/" + rel
	if comp, ok := agenttools.CanonicalPathComponentOK(archivedRel); !ok {
		return "", "archive destination path component " + comp + " is not in Win32 canonical form; left in place (nothing is deleted)"
	}
	if comp, hit := agenttools.GovernanceWriteRefusal(archivedRel); hit {
		return "", "archive destination is a governance/credential path (" + comp + "); left in place (nothing is deleted)"
	}
	if comp, hit := agenttools.VCSMetadataRefusal(archivedRel); hit {
		return "", "archive destination is protected VCS metadata (" + comp + "); left in place (nothing is deleted)"
	}
	dst := filepath.Join(destAbs, filepath.FromSlash(archivedRel))
	back, berr := filepath.Rel(destAbs, dst)
	if berr != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", "archive destination does not resolve inside the run directory; left in place (nothing is deleted)"
	}
	if derr := os.MkdirAll(filepath.Dir(dst), artifactDirPerm); derr != nil {
		return "", "archive directory could not be created: " + derr.Error()
	}
	if _, lerr := os.Lstat(dst); lerr == nil {
		return "", "archive target already exists; left in place (nothing is deleted)"
	}
	if rerr := os.Rename(src, dst); rerr != nil {
		return "", "move to archive failed; left in place (nothing is deleted): " + rerr.Error()
	}
	return archivedRel, ""
}

// refuseResolvedChainRows is the N6 gate: every manifest row a rollback would
// WRITE (the restore targets) is judged by the resolved-directory-chain check the
// write tools already pass through, so a row whose written path transits an in-root
// link into a governance/credential directory or VCS metadata refuses the whole
// manifest before the tree is touched.
//
// Why the row policy cannot answer this: checkManifestPathPolicy is LEXICAL, by
// design (a manifest is judged by the names it carries). A row
// "link/registry-keys.v2.json" whose "link" is a symlink or junction to ".agy-pro"
// carries no governed component, while the restore would open
// ".agy-pro/registry-keys.v2.json" - the same shape the write guard's resolved-dir
// arm exists for (guard.go:187-198).
//
// A chain that cannot be resolved at all is also a refusal: this package must not
// restore a row whose destination it cannot describe. The final component is never
// resolved (only the directory chain), for the same reason the write guard does
// not: the restore is an atomic temp+rename, which replaces a file symlink instead
// of writing through it.
func refuseResolvedChainRows(rootAbs string, m *Manifest) error {
	for _, row := range m.Files {
		resolvedDir, comp, refused, err := agenttools.ResolvedDirChainRefusal(rootAbs, filepath.FromSlash(row.Path))
		if err != nil {
			return fmt.Errorf("%w: files row %q: the directory chain a restore would traverse cannot be resolved (%v)", ErrRollbackRefused, row.Path, err)
		}
		if refused {
			return fmt.Errorf("%w: files row %q transits %q, which resolves to the governance/credential or VCS path component %q; a restore would write inside it", ErrGovernanceRefused, row.Path, resolvedDir, comp)
		}
	}
	return nil
}

// recordedFile is one manifest row's live file identity: the row's path and the
// os.FileInfo the OS reports for the file that path currently opens, captured
// AFTER the restore loop so a row this rollback rewrote is described by the bytes
// and identity it has now.
type recordedFile struct {
	path string
	info os.FileInfo
}

// recordedFileIndex is the once-per-rollback lookup the archive restraint uses,
// keyed by the row's path folded through the ASCII-only case fold. Only a case-alias
// spelling of a recorded row can be in it, which is the whole point (FIX-F2, finding
// P-4): the restraint may never spare an unrelated directory entry, and the pre-F2
// form scanned EVERY row with os.SameFile for EVERY claim (O(M×N)).
//
// Why the fold is not gated on the root's policy: the key only nominates a
// CANDIDATE. The restraint still asks os.SameFile, so on a case-sensitive volume two
// case-variant names that are two files are not spared, and on a folding volume they
// are one file and must be.
type recordedFileIndex map[string]recordedFile

// indexRecordedFiles stats every manifest row's restore target once and files it
// under its ASCII-folded path. Rows whose target cannot be reached (an unsafe path,
// a file that does not exist) are skipped: those describe nothing and cannot be the
// file a claim names. Two rows that fold onto one key keep the first: they name one
// file, so either spelling answers.
func indexRecordedFiles(rootAbs string, m *Manifest) recordedFileIndex {
	out := make(recordedFileIndex, len(m.Files))
	for _, row := range m.Files {
		target, serr := agenttools.ResolveSafePath(rootAbs, filepath.FromSlash(row.Path))
		if serr != nil {
			continue
		}
		info, ierr := os.Stat(target)
		if ierr != nil {
			continue
		}
		key := asciiCaseFold(filepath.ToSlash(row.Path))
		if _, dup := out[key]; dup {
			continue
		}
		out[key] = recordedFile{path: row.Path, info: info}
	}
	return out
}

// sameAsRecorded reports whether the claim rel is a CASE-ALIAS spelling of one of
// the files the manifest records, and names the row it aliases.
//
// This is the archive arm's answer to a manifest row spelled in another case: on a
// case-folding volume "SRC/ALPHA.GO" and "src/alpha.go" are one file, so moving the
// second would move the file the first describes. Two conditions must both hold,
// and the index supplies only the first: the claim's folded path must equal a
// row's, and os.SameFile must confirm the two paths really open one file. The
// confirmation is what keeps the restraint honest (FIX-E1 N1): a Unicode look-alike
// ("src/prınce.txt" against "src/prince.txt") folds to no row, and on a
// case-sensitive volume a case variant is a different file, so neither is spared.
//
// What the restraint deliberately does NOT cover (finding P-4): a run-created
// HARDLINK of a recorded file under a name that is not a case alias. It was
// previously spared because the check was an unconditional SameFile scan of every
// row, with a reason that called a hardlink "another spelling". Moving such a
// directory entry moves nothing else - the recorded path keeps its own entry and its
// bytes - so the archive must take it, exactly like any other created file.
func sameAsRecorded(ix recordedFileIndex, rootAbs, rel string) (string, bool) {
	if len(ix) == 0 {
		return "", false
	}
	rf, ok := ix[asciiCaseFold(filepath.ToSlash(rel))]
	if !ok {
		return "", false
	}
	abs, err := absUnderRoot(rootAbs, rel)
	if err != nil {
		return "", false
	}
	info, serr := os.Stat(abs)
	if serr != nil {
		return "", false
	}
	if !os.SameFile(rf.info, info) {
		return "", false
	}
	return rf.path, true
}
