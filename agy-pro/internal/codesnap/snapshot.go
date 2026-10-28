// Package codesnap is the snapshot / diff / rollback primitive behind the
// `agy pro code` write door: the engine-side ledger that lets one run of
// external-model seats touch a workspace and be undone byte-for-byte.
//
// Contract invariants (the door's fail-closed charter, design-decision.md
// "Phase 1 - U1"):
//
//   - COPY-BASED. A snapshot is a byte copy under <destDir>/snapshot/, never a
//     hardlink, never a reflink, never a VCS index.
//   - NEVER INVOKES GIT. git is never run, imported or parsed here; .git bytes
//     are ordinary files that belong to no scope unless a scope claims them,
//     and a scope that claims them is refused (the VCS-metadata arm below).
//   - NEVER DELETES. A file the run created is archived by MOVING it to
//     <destDir>/rollback-archive/; if the move fails the file stays on disk and
//     is named in rollback.json skipped[] (the NEVER DELETE rule of
//     integrations/rollback.go:10-14).
//   - THE MANIFEST IS THE ONLY SOURCE OF TRUTH. Every rollback decision reads
//     manifest.json and nothing else: no timestamps, no git state, no
//     heuristic rescan of the tree for "what looks original".
//   - SECRET NAMES ARE NEVER COPIED (S5). A claimed file whose final name is in
//     the secret class below (.env*, *.env, *.pem, *.key, *.pfx, *.p12,
//     *.kdbx, *.ppk, id_rsa*) is WITHHELD: not one byte of it is read into the
//     ledger, and the manifest records a skipped row naming it, so a diff or a
//     rollback never pretends the file was covered. The suffix arms cover a name
//     that IS the suffix (a file called exactly ".pem"), and the whole compare
//     runs on the FOLDED name, so a case variant is the same class member. A
//     withheld file is also never archived by rollback (moving it would copy the
//     same bytes into the run directory by another route).
//   - GOVERNANCE AND VCS PATHS FAIL THE TAKE (S3). A claim on a
//     governance/credential file or on VCS metadata refuses the whole take with
//     a named error instead of being skipped: the door must not snapshot a
//     governance baseline, and a rollback must never restore one (a restored
//     .git/config or AGENTS.md outlives the run). Rollback refuses a manifest
//     that names such a row at LOAD time, before the tree is touched.
//   - THE RUN DIRECTORY IS NEVER SNAPSHOTABLE (S3). <destDir> is excluded from
//     the walk, no claim may live inside it, and a scope entry that could claim
//     a path there is refused up front - otherwise a seat scoped to reports/**
//     could rewrite its own manifest.json and snapshot bytes.
//   - NO RUN LEDGER IS CLAIMABLE, NOT JUST THIS ONE (FIX-F4, finding Q1). The
//     same rule is applied to every EXISTING ledger under the root: the walk
//     recognizes a ledger fingerprint (a directory holding manifest.json - whose
//     bytes declare this package's schema version and a snapshot_dir - plus a
//     snapshot/ subdirectory) and a scope that could claim one refuses the whole
//     take, so "run 2 is granted reports/code/t1/**" cannot reach run 1's
//     manifest and snapshot copies. The shape test keeps an ordinary tree that
//     merely holds those two names (an npm-style dist/manifest.json) out of the
//     refusal; a planted VALID shape still refuses, and the operator deletes it.
//     A directory link planted at a ledger (symlink or junction) is resolved and
//     judged by the same test, because the walk does not follow it (FIX-F5, W2).
//   - THE MANIFEST IS BOUND TO ITS RUN (FIX-F4, finding Q1). Every manifest Take
//     writes is signed with a per-run secret kept outside every root containment
//     (~/.agy-pro/ledger-keys, 0600), as manifest.sig = HMAC-SHA256 over the
//     manifest bytes; LoadManifest verifies it BEFORE judging a single row, so a
//     forged or hand-edited manifest is refused whole and the undo net cannot be
//     turned into an installer. A ledger with no sidecar - every run directory
//     written before this binding - refuses as unsigned rather than being
//     trusted. A ledger COPIED to another path still verifies: its key_id travels
//     inside the signed bytes and names the key, so a relocated run directory stays
//     restorable - but only INTO its recorded root, because Rollback refuses a
//     manifest whose root is not the tree in front of it (sameRoot). Copying the
//     pair therefore preserves provenance and moves nothing (sig.go states both
//     halves).
//   - AN INODE IS NOT A NAME (FIX-F4, finding Q2). A claim whose inode is shared
//     with a governance/credential, VCS or secret-class path in the same walk is
//     a copy of those bytes under a neutral name, so it is WITHHELD like the
//     secret class: never read into the ledger, never archived, named in a
//     skipped row.
//   - FAIL CLOSED. Any ambiguity - a claimed file that cannot be read, an
//     empty or root-wide scope entry, a snapshot copy whose hash does not match
//     its row, a missing/corrupt manifest, a root mismatch - refuses with a
//     named error instead of guessing.
//
// Scope claiming mirrors the partition engine's dual-view rule
// (internal/fanout/merge.go:336-401 expandClaimedFiles): a walked file is
// claimed when EITHER its lexical root-relative path OR its symlink-resolved
// real path is in scope, and the claim is recorded under the REAL path. The two
// predicates are the EXPORTED authorities from internal/agenttools/guard.go -
// PathInScope (guard.go:231) and ResolvedRelPath (guard.go:313) - so codesnap
// cannot drift from the read filter's own definition of "in scope". The
// governance/credential and VCS-metadata refusals use that file's other two
// exports - GovernanceWriteRefusal (guard.go:574) and VCSMetadataRefusal
// (guard.go:728) - and the manifest-row canonical-form, stream-syntax and 8.3
// shape check uses CanonicalPathComponentOK (guard.go:558), so the write guard
// and this ledger share one definition of "governed", "VCS metadata", "a name
// the host opens as written" and "canonical name". Every NAME-CLASS refusal
// (governance, VCS, secret) folds its sides through agenttools.Win32NameFold
// (guard.go:824), the guard's NTFS $UpCase approximation, so the ledger and the
// write guard cannot disagree about which spellings are one governed name
// (R3-1/R3-7).
//
// THREE comparisons use a different form on purpose (FIX-E1, finding N1):
//
//   - FILE IDENTITY. Whether a walked path is a file the manifest recorded, or
//     recorded as withheld, is decided by the EXACT root-relative path. The
//     governance fold merges spellings the filesystem keeps distinct (U+0131
//     dotless i, U+017F long s: measured distinct on NTFS, see
//     coder-report-fix-d1a.md M3/M5/M7), so folding here would hide a
//     run-created "src/prınce.txt" behind a recorded "src/prince.txt" - absent
//     from the diff AND from the archive, i.e. a file the run created that
//     survives its own rollback (SEC r4 F1).
//   - DIRECTORY PREFIX (the walk exclusion, pathUnder) and the ledger claim
//     (scopeMayClaimUnder). These tolerate the ASCII case variance the volume
//     itself maps - "--out <root>/REPORTS/run" against a scope "reports/**" is
//     one directory on a case-folding host, which was R3-7's legitimate need -
//     and nothing else: the fold is ASCII-only, so "reſults" does NOT become
//     "results" and cannot smuggle a file past the exclusion.
//   - The fold is applied only when the ROOT'S OWN FILESYSTEM folds case, decided
//     by a runtime probe of that path (probeCaseFolding), not by GOOS: a
//     case-insensitive APFS volume needs the tolerance the same way NTFS does,
//     and a case-sensitive volume must never see two directories merged (N3).
//
// The run-directory rule has ONE implementation (FIX-F2, finding P-2). This
// package owns it and EXPORTS it: LedgerScopeClaims answers the question for a
// root and a run directory, and FoldLiteralRuns is the scanner behind it.
// internal/codework/phases.go refuseLedgerEntry now CALLS LedgerScopeClaims, so a
// scope that would fail the take is refused BEFORE the consent prompt (finding
// R3-9) by the same code that refuses it at snapshot time. codework imports this
// package, which is what makes the call possible, and the direction is one-way:
// this package must never import codework.
//
// The tests below pin the two layers end to end (ledger_mirror_test.go here, and
// TestLedgerMirrorParityWithTheSnapshotAuthority in internal/codework), and
// fix_f2_test.go pins the scanner itself against Go's path.Match with a
// differential fuzz over an alphabet of glob metacharacters.
package codesnap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

const (
	// ManifestVersion is the schema version stamped into manifest.json. A
	// loader refuses a version it does not know (fail closed).
	ManifestVersion = 1

	snapshotDirName  = "snapshot"
	manifestFileName = "manifest.json"

	// ReasonSecretClass is the recorded reason of a claimed file the snapshot
	// WITHHELD because its name is in the secret class (S5). The row is the
	// ledger's honesty: the file is not captured, so neither Diff nor Rollback
	// may treat it as covered.
	ReasonSecretClass = "secret-class"

	// ReasonHardlinkAlias is the recorded reason of a claimed file the snapshot
	// WITHHELD because its inode is shared with a governance/credential, VCS or
	// secret-class path of the same walk (FIX-F4, finding Q2): the bytes are the
	// same bytes under another name, so the same rule applies - never read into
	// the ledger, never archived. The reason names the path the inode belongs to.
	ReasonHardlinkAlias = "hardlink alias of withheld path"

	// artifactFilePerm is the mode of every byte this package writes: the
	// snapshot copies and the JSON ledgers are the run's private record, and a
	// copied source file may hold secrets (a local config), so the copies are
	// owner-only rather than mirroring the source mode.
	artifactFilePerm os.FileMode = 0o600
	artifactDirPerm  os.FileMode = 0o755
)

// Named errors. Callers (internal/codework, the CLI verb) branch on these
// instead of matching message text.
var (
	// ErrScopeRejected is wrapped by every scope-validation refusal.
	ErrScopeRejected = errors.New("codesnap: scope rejected")
	// ErrManifestMissing is wrapped when <destDir>/manifest.json is absent.
	ErrManifestMissing = errors.New("codesnap: manifest not found")
	// ErrManifestCorrupt is wrapped when the manifest cannot be decrypted into
	// the expected shape or carries an impossible value.
	ErrManifestCorrupt = errors.New("codesnap: manifest corrupt")
	// ErrRootMismatch is wrapped when a manifest is applied to a different
	// root than the one it was taken from.
	ErrRootMismatch = errors.New("codesnap: manifest root does not match the target root")
	// ErrSnapshotIncomplete is wrapped by *SnapshotError: at least one claimed
	// file could not be captured, so no manifest was written.
	ErrSnapshotIncomplete = errors.New("codesnap: snapshot refused")
	// ErrGovernanceRefused is wrapped when a claim, a manifest row or a restore
	// target names a governance/credential file or VCS metadata (S3).
	ErrGovernanceRefused = errors.New("codesnap: governance/credential or VCS path refused")
	// ErrLedgerRefused is wrapped when a scope could claim the run directory
	// itself, so the run could rewrite its own manifest and snapshot (S3).
	ErrLedgerRefused = errors.New("codesnap: scope can claim the run directory")
)

// secretClassPrefixes / secretClassSuffixes are the secret-name class of S5: the
// final component of a path, canonicalized (trailing dots and spaces trimmed) and
// folded through agenttools.Win32NameFold, matching one of these is never read
// into the ledger. A suffix entry also matches a name that IS the suffix.
//
// The list is deliberately tight and NAME-only (no content sniffing): a dotenv,
// a private key or a key-store container. Two over-refusals are accepted and
// recorded rather than resolving ambiguity at runtime:
//
//   - ".env*" also withholds ".env.example" (a template, no secret) and
//     ".envrc" (direnv config) - a template is one edit away from a live
//     dotenv, and the ledger must not be the file that holds either.
//   - "id_rsa*" also withholds "id_rsa.pub" (a public key) and any
//     "id_rsa_test.go"-style fixture.
//
// Counter-tradeoff, stated so it is not mistaken for coverage: a secret in a
// file with a different name (".npmrc", "config/db.yml", "*.crt", a bare
// "secrets"), or a secret inside a regular source file, is NOT withheld. This
// class is a name filter, not a secret scanner.
var (
	secretClassPrefixes = []string{".env", "id_rsa"}
	secretClassSuffixes = []string{".env", ".pem", ".key", ".pfx", ".p12", ".kdbx", ".ppk"}
)

// ManifestRow is one captured file. Existed is always true for a row Take
// writes (a row describes a file that WAS present); the field is explicit so a
// future "recorded as absent" row cannot be confused with a captured one.
type ManifestRow struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Mode    uint32 `json:"mode"`
	Existed bool   `json:"existed"`
}

// Manifest is <destDir>/manifest.json: the only source of truth a rollback or
// a diff is allowed to read.
type Manifest struct {
	Version int      `json:"version"`
	Root    string   `json:"root"`
	Scopes  []string `json:"scopes"`
	// SnapshotDir is the absolute directory holding the captured bytes
	// (<destDir>/snapshot). Diff reads it to count removed lines; rollback
	// reads it to restore.
	SnapshotDir string `json:"snapshot_dir"`
	// KeyID names the per-run key that signs this manifest (FIX-F4, finding Q1b):
	// manifest.sig must equal HMAC-SHA256 of these bytes under the key stored at
	// <agy-pro state home>/ledger-keys/<KeyID>. The id is DERIVED from the ledger's
	// absolute path, so it is stable - a second take into the same directory writes
	// the same id, which is why the manifest stays byte-deterministic - while the key
	// MATERIAL is fresh per take, so a re-take invalidates an older genuine pair.
	//
	// It travels INSIDE the signed bytes on purpose. Reading it from the manifest
	// rather than re-deriving it from the caller's --run spelling is what keeps a
	// ledger verifiable when the operator names the same directory a different way
	// (another ASCII case, a relative form, a path through a junction) - and it costs
	// no security, because an attacker who rewrites this field cannot produce a
	// matching MAC without the key it now names.
	KeyID string `json:"key_id"`
	// Files are the captured rows. Skipped are the claims the snapshot
	// deliberately did NOT capture (today: the secret class). A skipped row is
	// recorded so no reader mistakes the file for "not in scope": Diff does not
	// report it as created and Rollback neither restores nor archives it.
	Files   []ManifestRow `json:"files"`
	Skipped []SkippedFile `json:"skipped"`
}

// SkippedFile is one named refusal: the root-relative path and why it could not
// be handled. A skip is never silent and never a fallback.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// SnapshotError is returned by Take when one or more claimed files could not be
// captured. No manifest is written in that case, so a later rollback refuses
// (it has no source of truth) instead of archiving files the snapshot never
// recorded. A secret-class withholding is NOT this error: it is a recorded
// decision, not a failure.
type SnapshotError struct {
	Skipped []SkippedFile
}

func (e *SnapshotError) Error() string {
	return fmt.Sprintf("%s: %d claimed file(s) could not be captured: %s",
		ErrSnapshotIncomplete.Error(), len(e.Skipped), renderSkips(e.Skipped, 10))
}

// Unwrap exposes ErrSnapshotIncomplete to errors.Is.
func (e *SnapshotError) Unwrap() error { return ErrSnapshotIncomplete }

// renderSkips renders a deterministic, bounded skip list for an error message.
func renderSkips(skips []SkippedFile, max int) string {
	parts := make([]string, 0, len(skips))
	for i, s := range skips {
		if max > 0 && i == max {
			parts = append(parts, fmt.Sprintf("... %d more", len(skips)-max))
			break
		}
		parts = append(parts, s.Path+" ("+s.Reason+")")
	}
	return strings.Join(parts, "; ")
}

// walkResult is what walkScoped hands back: the claim set (sorted, recorded
// under real root-relative paths), the secret-class and hardlink-alias
// withholdings (claims that exist but must not be captured), the ledger
// fingerprint directories a scope could claim (FIX-F4, Q1), and the structural
// refusals the caller decides on.
type walkResult struct {
	claims     []string
	withheld   []SkippedFile
	refusals   []SkippedFile
	ledgerDirs []string
}

// Take captures every file the scopes claim and writes both the byte copies and
// the manifest.
//
// Walk: filepath.WalkDir over the resolved root (lexical visit order), each
// entry claimed through the dual view described in the package comment. Only
// regular files are claims (a directory reached through a symlink is a prefix,
// not a file).
//
// Output: <destDir>/snapshot/<relpath> per captured file (0600, atomic
// temp+rename) and <destDir>/manifest.json plus its manifest.sig provenance
// sidecar (sorted rows and sorted skipped rows, forward slashes, no timestamp).
// The manifest is byte-deterministic for a given (root contents, scopes,
// destDir) - the key ID is derived from destDir rather than random, so signing
// does not perturb a byte of it (see sig.go).
//
// Refused before the walk: an empty/root-wide/absolute/traversing scope entry, a
// destination that is the root, and a scope entry that could claim a path under
// destDir (S3 - the run directory holds the manifest and the snapshot bytes, so
// a scope over it would let the run rewrite its own ledger).
//
// Refused after the walk, before the first copy (FIX-F4, finding Q1): a scope
// entry that could claim ANY existing ledger under the root. The walk recognizes
// a ledger fingerprint - a directory holding manifest.json and a snapshot/
// subdirectory - and names it, so a scope granted over a PREVIOUS run's output
// directory cannot rewrite that run's baseline. Over-refusal is deliberate: a
// user directory that happens to hold those two names refuses as well, and the
// refusal text names the directory so the operator can narrow the scope.
//
// Refused during the walk: a claim on a governance/credential file or on VCS
// metadata (S3, named error, no manifest). A claimed file that cannot be read,
// cannot be resolved, or resolves outside the root refuses the whole take with a
// *SnapshotError naming it, and no manifest is written.
//
// Withheld, not refused: a claim whose final name is in the secret class (S5),
// and a claim whose inode is shared with a governance/credential, VCS or
// secret-class path of the same walk (FIX-F4, finding Q2). Their bytes are never
// read; the manifest carries a skipped row with reason ReasonSecretClass or
// ReasonHardlinkAlias.
func Take(root string, scopes []string, destDir string) (*Manifest, error) {
	normalized, err := normalizeScopes(scopes)
	if err != nil {
		return nil, err
	}
	rootAbs, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("codesnap: snapshot root %s: %w", rootAbs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("codesnap: snapshot root %s is not a directory", rootAbs)
	}
	destAbs, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return nil, fmt.Errorf("codesnap: snapshot destination %s: %w", destDir, err)
	}
	// The root is symlink-resolved (canonicalRoot); the destination is rendered in
	// the same space so a linked root cannot switch the two ledger checks below off.
	destSpace := canonicalDirish(destAbs)
	if sameRoot(destSpace, rootAbs) {
		return nil, fmt.Errorf("codesnap: snapshot destination must not be the root itself (%s)", rootAbs)
	}
	// The per-run key must live outside the tree a seat can reach (FIX-F4, Q1b).
	if kerr := refuseKeysInsideRoot(rootAbs); kerr != nil {
		return nil, kerr
	}
	ledgerRel, ledgerInside := rootRelativeDir(rootAbs, destSpace)
	policy := policyFor(rootAbs)
	// The LEDGER claim tests run on the more-refusing of two measurements: the
	// root's own fold and the fold of the ledger's deepest existing ancestor
	// (FIX-F4, finding Q3). The walk exclusion deliberately keeps the root policy:
	// widening it could hide a run-created directory from the archive.
	claimPolicy := ledgerPolicyFor(rootAbs, destSpace)
	if ledgerInside {
		if entry, hit := ledgerClaimingEntry(claimPolicy, normalized, ledgerRel); hit {
			return nil, fmt.Errorf("%w: scope entry %q can claim %q, which is inside the run directory %q; the ledger holding this manifest and its snapshot bytes must stay outside every scope (keep --out outside --root, or narrow the scope)", ErrLedgerRefused, entry, ledgerRel, ledgerRel)
		}
	}

	walk, werr := walkScoped(rootAbs, normalized, destSpace, policy, claimPolicy)
	if werr != nil {
		return nil, werr
	}
	if len(walk.ledgerDirs) > 0 {
		return nil, refusalForDiscoveredLedger(rootAbs, normalized, walk.ledgerDirs[0])
	}
	if len(walk.refusals) > 0 {
		sortSkips(walk.refusals)
		return nil, &SnapshotError{Skipped: walk.refusals}
	}
	m := &Manifest{
		Version:     ManifestVersion,
		Root:        rootAbs,
		Scopes:      normalized,
		SnapshotDir: filepath.Join(destAbs, snapshotDirName),
		KeyID:       ledgerKeyIDFor(destAbs),
		Files:       []ManifestRow{},
		Skipped:     walk.withheld,
	}
	for _, rel := range walk.claims {
		// Belt and braces over the walk's own exclusion: a claim can never be
		// inside the ledger, because a snapshot of the ledger would capture its
		// own manifest into itself.
		if policy.pathUnder(ledgerRel, rel) {
			return nil, fmt.Errorf("%w: claimed file %q is inside the run directory %q", ErrLedgerRefused, rel, ledgerRel)
		}
		src := filepath.Join(rootAbs, filepath.FromSlash(rel))
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			return nil, &SnapshotError{Skipped: []SkippedFile{{Path: rel, Reason: "unreadable: " + rerr.Error()}}}
		}
		srcInfo, serr := os.Stat(src)
		if serr != nil {
			return nil, &SnapshotError{Skipped: []SkippedFile{{Path: rel, Reason: "unstat-able: " + serr.Error()}}}
		}
		dst := filepath.Join(m.SnapshotDir, filepath.FromSlash(rel))
		if werr := providers.WriteFileAtomic(dst, data, artifactFilePerm); werr != nil {
			return nil, fmt.Errorf("codesnap: failed to write snapshot copy of %q: %w", rel, werr)
		}
		m.Files = append(m.Files, ManifestRow{
			Path:    rel,
			Size:    int64(len(data)),
			SHA256:  sha256Hex(data),
			Mode:    uint32(srcInfo.Mode().Perm()),
			Existed: true,
		})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	// The manifest and its provenance sidecar are written together (FIX-F4, Q1b):
	// a manifest that is not bound to a per-run key is a manifest anything could
	// have written, and the operator's rollback trusts it.
	if err := writeLedgerManifest(destAbs, m); err != nil {
		return nil, err
	}
	return m, nil
}

// LoadManifest reads <destDir>/manifest.json and validates it.
//
// It refuses in two layers, and the order is deliberate. The bytes are first
// PARSED (a manifest that is not JSON cannot be judged at all: ErrManifestCorrupt),
// then PROVENANCE is proven (FIX-F4, finding Q1b): the bytes must match manifest.sig
// under the run's own key, or the whole manifest is refused before a row is
// believed. A ledger with no sidecar - every run directory written before this
// binding - refuses as unsigned instead of being trusted, so an older run directory
// is evidence, never a restore target. Only then are the shape and path policies
// applied.
//
// Parsing before verifying is not a trust decision: nothing is read from the tree
// and nothing is written before the signature holds. It buys a sharper diagnosis -
// a truncated ledger says "corrupt" and a forged one says "provenance".
//
// After that the validation is deliberately strict: every row path must be a
// clean root-relative slash path with no ".." component, because rollback joins it
// onto the root and a hostile or truncated manifest must not be able to name a
// file outside it.
//
// On top of the shape check it runs the row policy (checkManifestPathPolicy):
// a manifest that names a governance/credential or VCS path refuses the whole
// rollback here, at load, before the tree is touched - a hand-forged manifest
// cannot restore a governance file, and it cannot make this package move one
// into the archive either. It also refuses a Files row whose name is in the
// secret class, because Take never captures one: such a row is forged.
//
// The row policy is defense in depth rather than the primary gate: a manifest that
// reaches it at all is one whose signature verified, so the policy layers guard
// against a compromised key as much as against a forged file.
func LoadManifest(destDir string) (*Manifest, error) {
	p := filepath.Join(destDir, manifestFileName)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrManifestMissing, p)
		}
		return nil, fmt.Errorf("codesnap: read %s: %w", p, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrManifestCorrupt, p, err)
	}
	if perr := verifyLedgerProvenance(destDir, data); perr != nil {
		return nil, perr
	}
	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrManifestCorrupt, p, err)
	}
	if err := checkManifestPathPolicy(&m); err != nil {
		return nil, fmt.Errorf("codesnap: %s: %w", p, err)
	}
	return &m, nil
}

// validateManifest is the loader's shape check (see LoadManifest).
func validateManifest(m *Manifest) error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported version %d (want %d)", m.Version, ManifestVersion)
	}
	if strings.TrimSpace(m.Root) == "" {
		return errors.New("missing root")
	}
	if !filepath.IsAbs(m.Root) {
		return fmt.Errorf("root %q is not absolute", m.Root)
	}
	// The key id is a single path component naming a file in the key store; anything
	// else would let a manifest name a key outside it (FIX-F4, finding Q1b).
	if !validKeyID(m.KeyID) {
		return fmt.Errorf("key_id %q is not a single path component (the provenance sidecar's key name)", m.KeyID)
	}
	if strings.TrimSpace(m.SnapshotDir) == "" {
		return errors.New("missing snapshot_dir")
	}
	if len(m.Scopes) == 0 {
		return errors.New("missing scopes (an empty scope set cannot be rolled back safely)")
	}
	for i, s := range m.Scopes {
		if _, err := normalizeScopes([]string{s}); err != nil {
			return fmt.Errorf("scopes[%d]: %v", i, err)
		}
	}
	seen := make(map[string]bool, len(m.Files))
	for i, row := range m.Files {
		if err := checkRelPath(row.Path); err != nil {
			return fmt.Errorf("files[%d]: %v", i, err)
		}
		if len(row.SHA256) != 64 {
			return fmt.Errorf("files[%d] %q: sha256 %q is not a 64-character hex digest", i, row.Path, row.SHA256)
		}
		if seen[row.Path] {
			return fmt.Errorf("files[%d] %q: duplicate row", i, row.Path)
		}
		seen[row.Path] = true
	}
	for i, skip := range m.Skipped {
		if err := checkRelPath(skip.Path); err != nil {
			return fmt.Errorf("skipped[%d]: %v", i, err)
		}
		if strings.TrimSpace(skip.Reason) == "" {
			return fmt.Errorf("skipped[%d] %q: empty reason (a skip is never silent)", i, skip.Path)
		}
	}
	return nil
}

// checkManifestPathPolicy is the manifest's PATH policy, split from the shape
// check because its refusals carry their own sentinels (ErrGovernanceRefused
// for a governed row, ErrManifestCorrupt for a row the snapshot could not have
// produced). It runs at load and again in Diff, so a manifest held in memory by
// a caller is judged by the same rule as one read from disk.
//
// Every row is also checked for Win32 canonical form through
// agenttools.CanonicalPathComponentOK (guard.go:558): a row named "AGENTS.md."
// passes the lexical governance test only for a name Windows never opens, and
// the restore writer must not be the one place that skipped the FIX-A1 layer.
// That predicate is also this package's shape authority for the R3-2 stream
// syntax and the R3-3 8.3 arm: FIX-D1a put both arms inside its per-component
// scan (componentShapeFailure, guard.go:892), so every files and skipped row is
// refused at LOAD, whole, for `PROVID~1.JSON`, `.AGY-PR~1/x.md`,
// `AGENTS.md:evil` or `sub/AGENTS.md::$DATA`. The archive destination runs the
// same predicate (rollback.go archiveMove), so a claimed file whose archive path
// carries one of those shapes is left in place rather than moved.
func checkManifestPathPolicy(m *Manifest) error {
	for _, row := range m.Files {
		if err := checkRowPathPolicy(row.Path, "files"); err != nil {
			return err
		}
		if family, hit := secretClassName(row.Path); hit {
			return fmt.Errorf("%w: files row %q is a secret-class name (%s), and Take never copies such bytes (reason %s); the row cannot be genuine",
				ErrManifestCorrupt, row.Path, family, ReasonSecretClass)
		}
	}
	for _, skip := range m.Skipped {
		if err := checkRowPathPolicy(skip.Path, "skipped"); err != nil {
			return err
		}
	}
	return nil
}

// checkRowPathPolicy applies the governance/credential, VCS-metadata and
// canonical-form refusals to one manifest path.
func checkRowPathPolicy(rel, list string) error {
	if comp, ok := agenttools.CanonicalPathComponentOK(rel); !ok {
		return fmt.Errorf("%w: %s row %q carries path component %q, which is not in Win32 canonical form (the name the restore would open is not the name this check read)",
			ErrManifestCorrupt, list, rel, comp)
	}
	if comp, hit := agenttools.GovernanceWriteRefusal(rel); hit {
		return fmt.Errorf("%w: %s row %q names a governance/credential path (%s); this package never snapshots or restores one",
			ErrGovernanceRefused, list, rel, comp)
	}
	if comp, hit := agenttools.VCSMetadataRefusal(rel); hit {
		return fmt.Errorf("%w: %s row %q names protected VCS metadata (%s); this package never snapshots or restores one",
			ErrGovernanceRefused, list, rel, comp)
	}
	return nil
}

// checkRelPath enforces the root-relative slash-path shape every path field of
// a manifest must have: non-empty, forward slashes, no volume, no leading "/",
// clean, and free of ".." components.
func checkRelPath(rel string) error {
	if rel == "" {
		return errors.New("empty path")
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf("path %q is not a forward-slash path", rel)
	}
	if filepath.VolumeName(rel) != "" || path.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("path %q is absolute; manifest paths must be root-relative", rel)
	}
	if path.Clean(rel) != rel {
		return fmt.Errorf("path %q is not clean", rel)
	}
	for _, c := range strings.Split(rel, "/") {
		if c == ".." {
			return fmt.Errorf("path %q escapes the root", rel)
		}
	}
	return nil
}

// normalizeScopes validates the scope set and returns it in the canonical form
// the matcher compares (forward slashes, path.Clean'd, no leading "/").
//
// It mirrors internal/fanout/units.go validateUnit (units.go:107-155) and its
// two helpers normalizeScopeEntry (units.go:595) and isRootWideGlob
// (units.go:527), which are unexported there: the rule is re-stated here with
// the authority named rather than reached across the package boundary. On top of
// the fanout rule it refuses three shapes fanout never has to see, because a
// snapshot scope is also a WRITE boundary: an absolute entry, a ".."
// component, and an empty scope set.
func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("%w: no scope entries (at least one bounded entry is required)", ErrScopeRejected)
	}
	out := make([]string, 0, len(scopes))
	for i, s := range scopes {
		raw := strings.TrimSpace(s)
		if raw == "" {
			return nil, fmt.Errorf("%w: scope[%d]: empty scope entry", ErrScopeRejected, i)
		}
		slash := filepath.ToSlash(raw)
		if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" || strings.HasPrefix(slash, "/") {
			return nil, fmt.Errorf("%w: scope[%d]: absolute scope entry %q is rejected (scopes are root-relative)", ErrScopeRejected, i, s)
		}
		for _, c := range strings.Split(slash, "/") {
			if c == ".." {
				return nil, fmt.Errorf("%w: scope[%d]: scope entry %q escapes the root", ErrScopeRejected, i, s)
			}
		}
		norm := normalizeScopeEntry(raw)
		if norm == "" {
			return nil, fmt.Errorf("%w: scope[%d]: root-wide scope entry %q is rejected (\".\", \"/\" and blank-after-clean match every path)", ErrScopeRejected, i, s)
		}
		if isRootWideGlob(norm) {
			return nil, fmt.Errorf("%w: scope[%d]: root-wide-equivalent scope entry %q is rejected (a bare-wildcard entry matches every path)", ErrScopeRejected, i, s)
		}
		out = append(out, norm)
	}
	return out, nil
}

// normalizeScopeEntry renders a scope entry the way PathInScope compares it.
// Authority: internal/fanout/units.go:595 normalizeScopeEntry (unexported), kept
// byte-compatible so a plan validated there is validated identically here. An
// entry denoting the root normalizes to "" (match-everything), which
// normalizeScopes rejects.
func normalizeScopeEntry(s string) string {
	s = filepath.ToSlash(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}

// isRootWideGlob reports whether a NORMALIZED scope entry is root-wide-
// equivalent. Authority: internal/fanout/units.go:527 isRootWideGlob
// (unexported). An entry is root-wide-equivalent when EVERY component matches
// any name ("*", "**", "?*") or consists only of glob metacharacters with at
// least one "*" anywhere; a bounded glob with a literal anchor in any component
// ("src/*.go", "*.go") keeps that anchor and stays accepted.
func isRootWideGlob(entry string) bool {
	comps := strings.Split(entry, "/")
	allMatchAny, allWildcardOnly, hasStar := true, true, false
	for _, c := range comps {
		if !componentMatchesAnyName(c) {
			allMatchAny = false
		}
		wildcardOnly, star := componentWildcardOnly(c)
		if !wildcardOnly {
			allWildcardOnly = false
		}
		hasStar = hasStar || star
	}
	return allMatchAny || (allWildcardOnly && hasStar)
}

// componentMatchesAnyName reports whether one path.Match component matches
// EVERY real file name: it needs at least one "*" (which absorbs any run) and
// at most one "?" (a second "?" would require a two-character name). Authority:
// internal/fanout/units.go:549.
func componentMatchesAnyName(c string) bool {
	hasStar, qmarks := false, 0
	for i := 0; i < len(c); i++ {
		switch c[i] {
		case '*':
			hasStar = true
		case '?':
			qmarks++
		default:
			return false
		}
	}
	return hasStar && qmarks <= 1
}

// componentWildcardOnly reports whether the component is built exclusively of
// glob metacharacters and whether it contains a "*". Authority:
// internal/fanout/units.go:568.
func componentWildcardOnly(c string) (wildcardOnly bool, hasStar bool) {
	for i := 0; i < len(c); i++ {
		switch c[i] {
		case '*':
			hasStar = true
		case '?':
		case '[':
			if j := strings.IndexByte(c[i:], ']'); j >= 0 {
				i += j
			} else {
				return false, hasStar
			}
		default:
			return false, hasStar
		}
	}
	return true, hasStar
}

// ---------------------------------------------------------------------------
// S3: the run directory is not claimable.
//
// The ledger (manifest.json, snapshot/, rollback-archive/) usually lives INSIDE
// the workspace: `agy pro code` defaults --out to reports/code/<ts> under
// --root. Excluding that subtree from the walk (walkScoped's excludeAbs) stops
// the snapshot from capturing its own bytes, but it does NOT stop a scope from
// covering the subtree: a seat scoped to reports/** could rewrite manifest.json
// and the snapshot copies in place, and the next rollback would then restore
// forged bytes. So the refusal is moved to the scope: a scope entry that could
// claim a path at or below the run directory refuses the whole take.
//
// APPROXIMATION, stated plainly: the test is LEXICAL and CONSERVATIVE. It
// compares the scope entry's components against the run directory's components,
// one path.Match per component, and treats a full-prefix match as an
// intersection in both directions (the pattern may cover the directory, or it
// may name something deeper inside it). A malformed glob is an intersection
// (fail closed). It does not resolve symlinks and does not enumerate the tree:
// a run directory reached through a link could still be under-refused here. The
// over-refusal side is bounded and deliberate: "reports" alone refuses a run
// directory at reports/code/<ts>, because that scope does cover the ledger.
//
// The two sides of the claim come from different sources: the scope entry is the
// operator's text, while the run directory's relative form is derived from the
// filesystem (filepath.Rel keeps the spelling of the path it was given), so
// "--out <root>/REPORTS/run" against a scope "reports/**" names one directory on
// a case-folding host and two on a case-sensitive one. The claim matcher closes
// that gap with a SUPERPOSITION of two tests (FIX-E1, finding N2).

// namePolicy is the name-comparison policy for one operation, resolved once from
// the root's own filesystem (probeCaseFolding) and then threaded through the
// walk, the ledger claim and the exclusion. It gates the ASCII case fold ONLY:
// file identity never consults it (exact paths, see the package comment).
type namePolicy struct {
	// foldCase is true when the volume holding the operation's root maps ASCII
	// case onto one file, which is what makes "REPORTS" and "reports" one
	// directory there.
	foldCase bool
}

// policyFor resolves the policy for a root by probing that root's own filesystem,
// and CACHES the answer per root (FIX-F2, finding P-3).
//
// The cache exists because the probe writes (see probeCaseFolding): one throw-away
// probe directory per root per process instead of one per call, so the walk, the
// ledger claim, the exclusion, Diff and Rollback all read the same measurement.
// The per-ledger measurement (ledgerPolicyFor) adds at most one more probe per
// ledger CANDIDATE, never one per walked directory: the walk gates it behind the
// fold-ON claim test and the ledger fingerprint (FIX-G1, P2-a).
// Only a CONCLUSIVE answer is cached: an inconclusive probe returns the fail-closed
// guess without storing it, so a root that does not exist yet is measured again
// once it does, instead of freezing a guess for the whole process.
func policyFor(rootAbs string) namePolicy {
	return policyForDir(rootAbs)
}

// policyForDir is policyFor for ANY directory: it probes dir itself (which must
// exist for a conclusive answer) and caches per absolute path. Split from policyFor
// by FIX-F4 (finding Q3) so the ledger's own subtree can be measured too.
func policyForDir(dirAbs string) namePolicy {
	key := policyKey(dirAbs)
	policyMu.Lock()
	cached, ok := policyCache[key]
	policyMu.Unlock()
	if ok {
		return cached
	}
	folds, conclusive := probeCaseFolding(dirAbs)
	if !conclusive {
		// FAIL CLOSED (N3): a probe that cannot answer (a root that does not exist,
		// a root this process may not write to, a filesystem that refuses the probe)
		// assumes the volume folds. Folding is the conservative side for the two
		// comparisons that use it: the ledger claim refuses MORE scopes and the walk
		// exclusion excludes MORE of the run directory. The alternative (assuming a
		// case-sensitive volume and silently un-excluding the ledger) is the worse
		// guess.
		return namePolicy{foldCase: true}
	}
	p := namePolicy{foldCase: folds}
	policyMu.Lock()
	policyCache[key] = p
	policyMu.Unlock()
	return p
}

// ledgerPolicyFor is the policy the LEDGER CLAIM tests use (FIX-F4, finding Q3).
//
// It answers the question those tests actually ask - "can a scope spelling reach
// the ledger directory" - with the fold of BOTH places that can map a spelling
// onto it: the operation root (the F1..F3 rule) and the ledger's own deepest
// existing ancestor. The second probe closes the review's F-01 / Q3 shape: a
// byte-exact root whose LEDGER sits below an interior case-insensitive boundary (a
// bind-mounted casefold subtree, an ext4 casefold directory, an APFS volume mounted
// under the workspace) measured fold OFF from the root alone, so `reports/CODE/run`
// was accepted for a ledger at `reports/code/run`. Either measurement folding makes
// the claim tests fold, which can only ADD refusals.
//
// The walk EXCLUSION keeps the root policy, deliberately: widening it could merge a
// run-created directory with the ledger's name and hide a created file from the
// archive, which is the SEC r4 F1 escape the ASCII-only fold exists to prevent.
//
// The ancestor probe is a NO-OP cost on a folding root (Windows NTFS), where the
// first answer already folds; and it is cached (policyForDir) so each ledger
// location is measured at most once per process.
func ledgerPolicyFor(rootAbs, destSpace string) namePolicy {
	p := policyFor(rootAbs)
	if p.foldCase {
		return p
	}
	if anc := deepestExistingDir(destSpace); anc != "" {
		if q := policyForDir(anc); q.foldCase {
			return namePolicy{foldCase: true}
		}
	}
	return p
}

// deepestExistingDir walks p upwards until it finds a directory that exists, and
// returns it ("" when nothing on the chain does). This is the path a probe can
// actually measure: the run directory usually does not exist yet on the first take,
// and its PARENT is the directory whose entries the ledger occupies.
func deepestExistingDir(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	cur := filepath.Clean(p)
	for {
		if info, err := os.Stat(cur); err == nil && info.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// policyCache / policyMu hold the per-root policy answers. Guarded because the
// write phase may resolve a policy from more than one goroutine.
var (
	policyMu    sync.Mutex
	policyCache = map[string]namePolicy{}
)

// policyKey normalizes a root into the cache key: the absolute, cleaned path, so
// two spellings of the same directory that differ only by a redundant "." or a
// relative form share one measurement. The on-disk casing is NOT normalized here -
// doing so through the filesystem is the very question the probe answers.
func policyKey(rootAbs string) string {
	if abs, err := filepath.Abs(filepath.Clean(rootAbs)); err == nil {
		return abs
	}
	return rootAbs
}

// dirKey renders a root-relative slash path for a DIRECTORY PREFIX comparison:
// ASCII-lowercased when the volume folds case, the input itself otherwise.
//
// ASCII only, on purpose. agenttools.Win32NameFold folds Unicode look-alikes too
// (ı->i, ſ->s), and those name DIFFERENT files on this host, so using it here
// reproduced SEC r4 F1's stealth variant: a created "reſults/code/<ts>/payload"
// folded onto the excluded ledger directory and disappeared from both the diff
// and the archive. ASCII case is exactly the part of the name the filesystem
// really maps (measured: 'A'-'Z' collide, U+0131/U+017F/U+212A do not).
func (p namePolicy) dirKey(s string) string {
	if !p.foldCase {
		return s
	}
	return asciiCaseFold(s)
}

// asciiCaseFold lowercases the ASCII letters of s and leaves every other byte
// alone, so a Unicode look-alike never merges with its ASCII twin.
func asciiCaseFold(s string) string {
	upper := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			upper = true
			break
		}
	}
	if !upper {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// asciiUpper is asciiCaseFold's mirror, used by the claim matcher's second
// attempt (a character class that spells uppercase must still meet a folded
// target).
func asciiUpper(s string) string {
	lower := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'a' && c <= 'z' {
			lower = true
			break
		}
	}
	if !lower {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// pathUnder reports whether the root-relative slash path rel is dirRel itself or
// lives below it, compared through the ASCII case fold when the volume applies
// one. An empty dirRel matches nothing.
func (p namePolicy) pathUnder(dirRel, rel string) bool {
	if dirRel == "" {
		return false
	}
	dirRel, rel = p.dirKey(dirRel), p.dirKey(rel)
	return rel == dirRel || strings.HasPrefix(rel, dirRel+"/")
}

// ledgerClaimingEntry returns the first scope entry that could claim a path at or
// below dirRel, plus whether one exists.
func ledgerClaimingEntry(p namePolicy, scopes []string, dirRel string) (string, bool) {
	for _, entry := range scopes {
		if p.scopeMayClaimUnder(entry, dirRel) {
			return entry, true
		}
	}
	return "", false
}

// LedgerScopeClaims reports whether one NORMALIZED scope pattern could claim a path
// at or below destDir, which must lie inside root, plus the reason to show when it
// can.
//
// This is the EXPORTED form of the ledger rule, and it is the only form of it in the
// tree (FIX-F2, findings P-2 and P-3). Take, Rollback and Diff ask it here through
// namePolicy, and internal/codework/phases.go refuseLedgerEntry asks it for the
// pre-consent gate, so the scope refused before the consent prompt is refused by
// the same code that would refuse the snapshot afterwards. Before F2 codework
// carried a hand-copied mirror of this rule and its own temp-directory case probe;
// both are gone.
//
// The pattern must already be normalized (forward slashes, no ".", no empty
// components): normalizeScopes does that for a take, normalizeScopeText for the
// write phase. An empty pattern claims nothing, and so does an unresolvable root -
// the callers' own root checks (Take's Stat, the write phase's root validation)
// refuse such a run before a scope could matter.
func LedgerScopeClaims(root, destDir, pattern string) (refuse bool, reason string) {
	rootAbs, err := canonicalRoot(root)
	if err != nil {
		return false, ""
	}
	destRel, inside := ledgerDirRelForRoot(rootAbs, destDir)
	if !inside || pattern == "" {
		return false, ""
	}
	// The more-refusing of the root's fold and the ledger subtree's own fold
	// (FIX-F4, finding Q3): the question is whether a spelling reaches the LEDGER,
	// so a case-insensitive boundary ABOVE the ledger counts even when the root is
	// byte-exact.
	destAbs, aerr := filepath.Abs(filepath.Clean(destDir))
	if aerr != nil {
		return false, ""
	}
	if !ledgerPolicyFor(rootAbs, canonicalDirish(destAbs)).scopeMayClaimUnder(pattern, destRel) {
		return false, ""
	}
	return true, fmt.Sprintf("scope entry can claim %q, which is inside the run directory %q", destRel, destRel)
}

// LedgerDirRel renders destDir as a root-relative slash path and reports whether it
// is inside root at all. Exported for the pre-consent gate, which needs the same
// resolution the snapshot uses: the root symlink-resolved (canonicalRoot) and the
// destination rendered in that path space through its longest existing prefix
// (canonicalDirish), so a linked root cannot make the two spellings incomparable and
// switch the ledger refusal off.
//
// A destination outside the root, or the root itself, is not inside: there is no
// root-relative path a scope entry could reach it by.
func LedgerDirRel(root, destDir string) (string, bool) {
	rootAbs, err := canonicalRoot(root)
	if err != nil {
		return "", false
	}
	return ledgerDirRelForRoot(rootAbs, destDir)
}

func ledgerDirRelForRoot(rootAbs, destDir string) (string, bool) {
	destAbs, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return "", false
	}
	return rootRelativeDir(rootAbs, canonicalDirish(destAbs))
}

// scopeMayClaimUnder reports whether the NORMALIZED scope entry could match a
// path at or below dirRel. Components are compared pairwise with path.Match until
// one side runs out, and a match on every compared component counts as a possible
// intersection (the pattern is either a prefix of the directory or a deeper path
// inside it). A malformed glob is an intersection (fail closed).
//
// TWO tests, and a refusal from either one refuses the scope (FIX-E1, finding N2):
//
//  1. the pattern AS AUTHORED, against the directory as spelled - exactly the
//     path.Match semantics agenttools.PathInScope applies to a claim, so the
//     ledger authority can never be laxer than the runtime scope matcher. N2's
//     counterexample lived here: "reports/[A-z]run" covers "reports/_run"
//     lexically ('_' is inside the A-z range), and D1b's whole-pattern fold
//     rewrote the class to "[a-z]" and lost that coverage.
//  2. the pattern's LITERAL RUNS ASCII-folded, against the directory ASCII-folded
//     (and, second attempt, uppercased, so a class spelled in one case still meets
//     the other). Character-class bytes are copied verbatim into both attempts - a
//     class range is a set of characters the author chose, never something a fold
//     may rewrite - so this arm can only ADD refusals to (1). It is what keeps
//     R3-7 closed: a scope "RePoRtS/**" or a run directory spelled
//     "REPORTS/code/<ts>" still meets, on a volume that folds case.
//  3. one CRUDE but complete backstop (FIX-F2, finding P-2): the whole pattern
//     ASCII-folded, against the directory ASCII-folded (and uppercased against
//     uppercased). This is exactly D1b's whole-pattern fold restricted to ASCII,
//     and it is here so the escape-aware scanner in (2) can never LOSE a refusal
//     that rule made: a pattern mixing a class the author spelled "in one case"
//     with another spelled "in the other" (CR-seat1's "R[A\]B][x]S" against
//     "raxs") is refused by (3) even though neither (2) attempt can match it. (3)
//     is a superset in the refusing direction only, which is why it is a backstop
//     and not the primary rule: on a class payload it can also LOSE coverage
//     ("[A-z]" becomes "[a-z]", which drops "_"), and (1) is what keeps that.
//
// The result is therefore strictly more refusing than the byte-exact comparison
// D1b replaced AND than the whole-pattern fold D1b shipped. The same code answers
// for the snapshot and for the pre-consent gate: internal/codework/phases.go
// refuseLedgerEntry calls LedgerScopeClaims, which is this method behind one
// root-scoped entry point (FIX-F2, finding P-2 - the duplicated mirror is gone).
func (p namePolicy) scopeMayClaimUnder(entry, dirRel string) bool {
	pc := strings.Split(entry, "/")
	dc := strings.Split(dirRel, "/")
	n := len(pc)
	if len(dc) < n {
		n = len(dc)
	}
	if n == 0 {
		return true
	}
	if claimChainMatches(pc[:n], dc[:n], false) {
		return true
	}
	if !p.foldCase {
		return false
	}
	return claimChainMatches(pc[:n], dc[:n], true)
}

// ---------------------------------------------------------------------------
// Q1a: EVERY existing run ledger is unclaimable, not just this run's.
//
// The run-directory rule above compares a scope against ONE directory, the one
// this take is about to write. SEC-mimopro's F-R6-1 chain (reproduced live by
// coder-report-fix-f4.md STEP 0) lives in the gap: run 1 writes
// reports/code/t1, run 2 is granted `--scope reports/code/t1/**` with
// `--out reports/code/t2`, and every pairwise comparison says "t1 is not t2" - so
// run 2's seat may rewrite t1's manifest and snapshot copies, and the operator's
// documented recovery installs them.
//
// The fingerprint below is the detector, and it is SHAPE-AWARE (FIX-F5, finding
// W3): a directory holding manifest.json AND a snapshot/ subdirectory AND a
// manifest.json whose bytes carry a known schema version plus a snapshot_dir IS
// treated as a run ledger - whether or not its signature verifies. Verifying first
// would make the refusal depend on the key store being intact, and a ledger whose
// key is gone is exactly the one an attacker would rather you could not see; the
// shape test instead answers "does this directory CLAIM to be one of our ledgers",
// which is the question the refusal needs (the undo evidence of a previous run).
//
// Why not content-blind any more: the coarse "these two names" rule (FIX-F4)
// refused ordinary trees, and an npm-style myapp/dist/manifest.json next to a
// dist/snapshot/ directory cost the operator a durable scope refusal with no
// dangerous shape behind it (r7 CR-mimoflash F-R7-1, SEC-step F-R7-1, blind-qa r7
// row 6). Reading the head of the candidate is cheap next to the two stats it
// already costs, because it happens only for directories that pass both.
//
// What it does NOT buy, stated rather than implied: a seat can still plant a
// VALID-SHAPE manifest.json (version + snapshot_dir are public constants) and
// re-create the availability refusal it wants. The refusal names the directory, and
// the cure is the operator's: delete the planted pair. This layer is an availability
// guard and a fail-closed detector, never an integrity one - integrity is
// manifest.sig, which a planted shape cannot produce.

// ledgerFingerprintMaxBytes bounds how much of a candidate manifest.json the
// fingerprint reads. A real manifest is a few KB (one row per claimed file); 1 MiB
// is far above it and far below "read whatever file a seat points us at".
const ledgerFingerprintMaxBytes = 1 << 20

// isLedgerFingerprintDir reports whether abs holds a run ledger: a regular file
// named manifest.json (bounded size) whose JSON declares the schema version this
// package writes plus a snapshot_dir, beside a directory named snapshot/.
func isLedgerFingerprintDir(abs string) bool {
	manifestPath := filepath.Join(abs, manifestFileName)
	fi, err := os.Stat(manifestPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > ledgerFingerprintMaxBytes {
		return false
	}
	if sfi, serr := os.Stat(filepath.Join(abs, snapshotDirName)); serr != nil || !sfi.IsDir() {
		return false
	}
	head, herr := readLedgerFingerprintHead(manifestPath)
	if herr != nil {
		return false
	}
	return head.Version == ManifestVersion && strings.TrimSpace(head.SnapshotDir) != ""
}

// readLedgerFingerprintHead parses the two manifest fields the fingerprint judges.
// The read is bounded TWICE: the caller refuses a file larger than
// ledgerFingerprintMaxBytes by its stat size, and the reader stops one byte past the
// bound so a file that grew between the stat and the read cannot be read whole.
func readLedgerFingerprintHead(path string) (struct {
	Version     int    `json:"version"`
	SnapshotDir string `json:"snapshot_dir"`
}, error) {
	var head struct {
		Version     int    `json:"version"`
		SnapshotDir string `json:"snapshot_dir"`
	}
	f, err := os.Open(path)
	if err != nil {
		return head, err
	}
	defer f.Close()
	data, rerr := io.ReadAll(io.LimitReader(f, ledgerFingerprintMaxBytes+1))
	if rerr != nil || len(data) > ledgerFingerprintMaxBytes {
		return head, errors.New("codesnap: ledger fingerprint head is unreadable or too large")
	}
	if uerr := json.Unmarshal(data, &head); uerr != nil {
		return head, uerr
	}
	return head, nil
}

// refusalForPriorLedger builds the refusal for a fingerprint directory a scope
// could claim, naming the scope entry so the operator can narrow it.
func refusalForPriorLedger(p namePolicy, scopes []string, dirRel string) error {
	entry, _ := ledgerClaimingEntry(p, scopes, dirRel)
	return fmt.Errorf("%w: scope entry %q can claim %q, which is an existing run ledger (it holds %s and %s/); a scope may not reach ANY run's manifest and snapshot bytes, not just this run's, because a seat that can rewrite a previous run's ledger makes the operator's rollback install attacker bytes (narrow the scope so it stays clear of %q; if that directory is not a run ledger - no version/snapshot_dir in its %s - delete the planted pair and re-run)",
		ErrLedgerRefused, entry, dirRel, manifestFileName, snapshotDirName, dirRel, manifestFileName)
}

// refusalForDiscoveredLedger builds the refusal for a ledger directory the WALK
// found, judged on the fold measured at that directory itself (FIX-F5, W6): the
// claim that matters is "could a scope spelling reach THIS ledger", and a folding
// boundary enclosing the ledger but neither the root nor this run's destination is
// outside both measurements the caller took.
func refusalForDiscoveredLedger(rootAbs string, scopes []string, dirRel string) error {
	return refusalForPriorLedger(ledgerPolicyFor(rootAbs, filepath.Join(rootAbs, filepath.FromSlash(dirRel))), scopes, dirRel)
}

// PriorLedgerClaim answers the same question as the refusal above for a caller that
// has no destination yet: the PRE-CONSENT gate. internal/codework/phases.go asks it
// after its own per-entry rules, so a scope granted over a previous run's output
// directory is refused BEFORE the consent prompt with the usage exit code, instead
// of being refused later by Take whose refusal lands after the operator has already
// said yes (the finding R3-9 shape, one run boundary further out).
//
// The scan walks root once and gates every directory on the claim test FIRST, so the
// stats and the bounded head read a fingerprint costs are paid only for directories a
// scope could reach. A directory LINK at a prior ledger is resolved and judged the
// same way the snapshot walk does it (FIX-F5, W2): the gate must name the same ledger
// the take would refuse, or the operator is prompted for a run that must fail.
// The claim test here uses the FOLD-ON policy on purpose: the pre-consent gate
// has no destination, so it cannot measure the fold of each prior ledger's own
// subtree, and folding refuses more. A stricter gate than Take is the allowed
// divergence direction (it can only cost the operator one narrowing edit; the
// opposite direction would let a consented run fail).
func PriorLedgerClaim(root string, scopes []string) (dirRel string, entry string, refuse bool) {
	rootAbs, err := canonicalRoot(root)
	if err != nil || len(scopes) == 0 {
		return "", "", false
	}
	policy := namePolicy{foldCase: true}
	found := ""
	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree cannot hide a ledger the gate must name
		}
		if !d.IsDir() {
			// A link to a directory is not a directory to this walk (FIX-F5, W2).
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			info, serr := os.Stat(p)
			if serr != nil || !info.IsDir() {
				return nil
			}
		}
		rel, rerr := filepath.Rel(rootAbs, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if _, claimable := ledgerClaimingEntry(policy, scopes, rel); !claimable {
			return nil
		}
		if isLedgerFingerprintDir(p) {
			found = rel
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil || found == "" {
		return "", "", false
	}
	e, _ := ledgerClaimingEntry(policy, scopes, found)
	return found, e, true
}

// claimChainMatches compares paired components, reporting whether every pair may
// match. With fold off this is the lexical test only. It compares the shorter of
// the two chains (a pattern longer than the directory is a deeper path inside it,
// which the caller's pairing already limits).
func claimChainMatches(pc, dc []string, fold bool) bool {
	n := len(pc)
	if len(dc) < n {
		n = len(dc)
	}
	for i := 0; i < n; i++ {
		if !segmentMayMatch(pc[i], dc[i], fold) {
			return false
		}
	}
	return true
}

// segmentMayMatch reports whether one pattern component may match one target
// component. A malformed pattern is a possible match (fail closed).
func segmentMayMatch(pat, tgt string, fold bool) bool {
	ok, err := path.Match(pat, tgt)
	if err != nil {
		return true
	}
	if ok {
		return true
	}
	if !fold {
		return false
	}
	lowerPat, upperPat := FoldLiteralRuns(pat)
	if ok, err := path.Match(lowerPat, asciiCaseFold(tgt)); err != nil || ok {
		return true
	}
	if ok, err := path.Match(upperPat, asciiUpper(tgt)); err != nil || ok {
		return true
	}
	// The whole-pattern ASCII fold (D1b's rule), kept as an additive backstop so
	// the class-preserving arms above cannot lose a refusal it made (FIX-F2, P-2).
	if ok, err := path.Match(asciiCaseFold(pat), asciiCaseFold(tgt)); err != nil || ok {
		return true
	}
	if ok, err := path.Match(asciiUpper(pat), asciiUpper(tgt)); err != nil || ok {
		return true
	}
	return false
}

// FoldLiteralRuns renders a pattern component twice: with its LITERAL runs
// lowercased, and with them uppercased. Character-class payloads ("[...]") are
// copied byte for byte into both forms, because a class is a character set the
// author wrote and folding inside it would change which characters it covers
// ("[A-z]" contains '_'; "[a-z]" does not).
//
// The scan follows the grammar of Go's own path.Match (path/match.go, matchChunk
// and getEsc), which is what makes "copied verbatim" mean the same thing here as
// it does inside the matcher this output is fed back into:
//
//   - '\' escapes the NEXT BYTE everywhere, in and out of a class. An escaped
//     metacharacter is a literal: `\[` opens no class, `\]` closes none. The
//     escaped byte is folded like any literal (an escaped letter is a letter), and
//     the backslash is carried through, because the folded forms are re-parsed by
//     path.Match and must keep meaning "the literal".
//   - a class is `[`, an OPTIONAL '^' negation, then ranges until the first
//     UNESCAPED ']'. Go negates with '^' only: '!' is an ordinary member, so
//     `[!]` is the one-character class {'!'} - treating '!' as a negation marker
//     was the mirror's bug (finding P-2b).
//   - a ']' immediately after `[` or `[^` is NOT a member and NOT the terminator:
//     Go's getEsc rejects ']' as the first element, so path.Match answers
//     ErrBadPattern for "[]..." (measured in this toolchain; the round-5 reports
//     that called a leading ']' a literal member described a different regex
//     dialect). Such a pattern is malformed, the lexical pass already answered
//     fail-closed before this fold is consulted, and the scanner therefore keeps
//     the remainder verbatim instead of inventing a class boundary.
//   - an unterminated class, a class whose last element is a lone escape, and a
//     trailing '\' are malformed for the same reason and take the same verbatim
//     fallback.
//
// The two forms are exported because they are the definition of "the folded-literal
// arm", not an internal convenience: internal/codework's pre-consent gate reaches
// this rule through LedgerScopeClaims, and the differential fuzz in fix_f2_test.go
// pins the whole scan against path.Match itself.
func FoldLiteralRuns(pattern string) (lower, upper string) {
	var lo, hi strings.Builder
	lo.Grow(len(pattern))
	hi.Grow(len(pattern))
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; {
		case c == '\\':
			// Escape: the backslash and the byte it protects are copied together,
			// with only the protected byte folded. A trailing '\' has nothing to
			// protect; the pattern is malformed and is kept verbatim.
			lo.WriteByte('\\')
			hi.WriteByte('\\')
			if i+1 >= len(pattern) {
				return lo.String(), hi.String()
			}
			lo.WriteByte(asciiLowerByte(pattern[i+1]))
			hi.WriteByte(asciiUpperByte(pattern[i+1]))
			i += 2
		case c == '[':
			end := classEnd(pattern, i)
			if end < 0 {
				lo.WriteString(pattern[i:])
				hi.WriteString(pattern[i:])
				return lo.String(), hi.String()
			}
			lo.WriteString(pattern[i:end])
			hi.WriteString(pattern[i:end])
			i = end
		default:
			lo.WriteByte(asciiLowerByte(c))
			hi.WriteByte(asciiUpperByte(c))
			i++
		}
	}
	return lo.String(), hi.String()
}

// classEnd returns the index just past the class that starts at pattern[start]
// (which must be '['), or -1 when the class is malformed in the sense of Go's
// path.Match. It implements the "first unescaped ']'" rule only:
//
//   - a '\' inside the class consumes the byte after it, so `[A\]B]` closes at the
//     LAST ']' (the class is {A, ']', B}), which is the misparse CR-seat1's
//     "R[A\]B][x]S" witness was built on;
//   - a ']' as the first element (after an optional '^') is malformed, because
//     path.Match's getEsc refuses it - so the caller keeps the remainder verbatim
//     rather than pretending the class ended there;
//   - `[^...]` is the only negation form; '!' is a plain member.
func classEnd(pattern string, start int) int {
	j := start + 1
	if j < len(pattern) && pattern[j] == '^' {
		j++
	}
	if j >= len(pattern) || pattern[j] == ']' {
		return -1
	}
	for j < len(pattern) {
		switch pattern[j] {
		case '\\':
			j += 2 // the escaped byte is never a terminator
			continue
		case ']':
			return j + 1
		}
		j++
	}
	return -1
}

// asciiLowerByte / asciiUpperByte fold one ASCII letter and pass every other byte
// through, including the pattern escape '\' (which therefore still escapes the
// character it did before the fold).
func asciiLowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func asciiUpperByte(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

// probeCaseFolding asks the filesystem holding p whether it maps ASCII case onto
// one name, and whether it could answer at all (FIX-E1 finding N3, rewritten by
// FIX-F2 finding P-3).
//
// Method (SEC-seat1's R5-1 diff): make a throw-away directory INSIDE p, write one
// empty file named "probe-Aa" in it, and stat "probe-aA" in the SAME directory. On
// a volume that folds case the other spelling IS the same file (os.SameFile); a
// genuine "not found" is the conclusive case-sensitive answer; anything else is
// inconclusive and policyFor folds.
//
// Why inside p and not by flipping p: the pre-F2 probe flipped the ASCII case of
// the WHOLE path and stat'd that. On a case-insensitive directory below a
// case-sensitive parent (/tmp/vfat/Workspace, or an ext4 casefold directory under
// a plain parent) the flipped spelling dies at the case-sensitive ANCESTOR, so the
// probe answered "case-sensitive" about a directory that folds - which switched the
// ledger fold off and let a case-variant scope reach the ledger (SEC R5-1). A
// probe of a child measures the directory whose entries the walk actually compares,
// and measures nothing about ancestors.
//
// Side effect, stated plainly: the probe CREATES one directory and one empty file
// inside p, then removes both (RemoveAll of the directory this call just made,
// which by construction holds only its own probe file). A caller that cannot write
// to p gets the inconclusive answer, i.e. fold on - the refusing side. The removal
// is best effort: a failure leaves a ".agy-casefold-probe-*" directory behind, and
// policyFor's per-root cache means this happens at most once per root per process.
//
// conclusive=false means the path gave no answer: the probe could not be created
// (a missing or read-only root), the probe file could not be written, or the second
// stat failed for a reason other than "not found". policyFor treats that as
// fold=true, the conservative side.
func probeCaseFolding(p string) (folds bool, conclusive bool) {
	if strings.TrimSpace(p) == "" {
		return false, false
	}
	dir, err := os.MkdirTemp(p, caseFoldProbePrefix)
	if err != nil {
		return false, false
	}
	observeFoldProbe(p)
	defer func() {
		// Best effort: this function created the directory seconds ago and it holds
		// nothing but the probe file below.
		_ = os.RemoveAll(dir)
	}()
	probe := filepath.Join(dir, "probe-Aa")
	if werr := os.WriteFile(probe, nil, 0o600); werr != nil {
		return false, false
	}
	orig, oerr := os.Stat(probe)
	if oerr != nil {
		return false, false
	}
	other, ferr := os.Stat(filepath.Join(dir, "probe-aA"))
	if ferr == nil {
		return os.SameFile(orig, other), true
	}
	if errors.Is(ferr, fs.ErrNotExist) {
		// Both spellings were looked up in p's OWN directory: the volume keeps the
		// two apart there.
		return false, true
	}
	return false, false
}

// caseFoldProbePrefix names the throw-away directory probeCaseFolding creates
// inside the root. Exported to nobody: it is a marker an operator can recognize in
// the (rare) case the removal fails.
const caseFoldProbePrefix = ".agy-casefold-probe-"

// foldProbeObserver, when non-nil, is called with the directory every case-fold
// probe measures, right after the probe created its throw-away directory - so a
// caller counts real MkdirTemp creations, not cache hits. Tests install it to count
// probes (FIX-G1, P2-a); production never sets it, and the nil check costs one load.
var foldProbeObserver func(dirAbs string)

func observeFoldProbe(dirAbs string) {
	if foldProbeObserver != nil {
		foldProbeObserver(dirAbs)
	}
}

// rootRelativeDir renders abs relative to rootAbs as a slash path when abs is
// inside rootAbs, and reports whether it is. A run directory outside the root,
// or equal to it, is not a ledger subtree (the equal case is refused by the
// callers separately).
func rootRelativeDir(rootAbs, abs string) (string, bool) {
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// canonicalDirish renders a destination directory in the same path space
// canonicalRoot put the root in: the LONGEST EXISTING PREFIX is symlink-resolved
// and the not-yet-existing tail is re-attached verbatim.
//
// Why it is needed: <destDir> usually does not exist yet on the first take, so a
// plain EvalSymlinks fails, while the root may well be reached through a link or
// junction (canonicalRoot resolves it). Comparing a logical <destDir> spelling
// against a resolved root would then fail the filepath.Rel test and silently
// switch the ledger refusal OFF - the one case where the run directory really is
// inside the root. A destination outside the root is unaffected: the resolution
// only makes the two sides comparable.
func canonicalDirish(p string) string {
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return p
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil && resolved != "" {
		return resolved
	}
	head, tail := abs, ""
	for {
		parent := filepath.Dir(head)
		if parent == head {
			return abs
		}
		tail = filepath.Join(filepath.Base(head), tail)
		head = parent
		if resolved, rerr := filepath.EvalSymlinks(head); rerr == nil && resolved != "" {
			return filepath.Join(resolved, tail)
		}
	}
}

// ---------------------------------------------------------------------------
// S5: the secret class.
//
// The check is on the FINAL component only, after the Win32 trailing-dot/space
// trim agenttools.canonicalTrimSet (guard.go:426) applies, compared through the
// same name fold the governance and VCS arms use (agenttools.Win32NameFold,
// guard.go:824): R3-1's "every name-class comparison" includes this one, and
// docs/code.md §4.4 states it. Both the lexical name the seat sees and the
// recorded real path are checked by the caller, so a link named config.md -> .env
// is withheld as well as a plain .env.
//
// A suffix arm matches a whole name that IS the suffix (R3-8): a file literally
// called ".pem" or ".key" is withheld, where the earlier "longer than the suffix"
// test let it through - the one shape where the class name and the file name are
// the same string. The prefix arms already covered ".env" itself.

// secretCanonicalComponent returns the name Win32 would open for this component:
// the component with the trailing dots and spaces it strips. It mirrors
// agenttools.canonicalWriteComponent (guard.go:467) for the one character class
// the secret names need; the trim set is a two-character constant there and
// here, and the parity test pins them together.
func secretCanonicalComponent(comp string) string {
	return strings.TrimRight(comp, ". ")
}

// secretClassName reports whether the final component of the root-relative slash
// path rel is in the secret class, and names the family that matched (for the
// error text; the recorded reason is always ReasonSecretClass).
func secretClassName(rel string) (string, bool) {
	comps := strings.Split(filepath.ToSlash(rel), "/")
	last := secretCanonicalComponent(comps[len(comps)-1])
	if last == "" {
		return "", false
	}
	folded := agenttools.Win32NameFold(last)
	for _, prefix := range secretClassPrefixes {
		if strings.HasPrefix(folded, prefix) {
			return prefix + "*", true
		}
	}
	for _, suffix := range secretClassSuffixes {
		if len(folded) >= len(suffix) && strings.HasSuffix(folded, suffix) {
			return "*" + suffix, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Root identity.
//
// A manifest is only ever applied to the tree it was taken from, and the two
// comparison sites (Diff, Rollback) compare through sameRoot. The comparison is
// volume-aware and case-folded on Windows: Win32 paths are case-insensitive, so
// "d:/x" and "D:/X" name one directory, and a byte-exact string comparison
// would refuse a legitimate rollback (the CR-seat1 F-03 / CR-glm F-05 finding).
//
// The fold here is strings.EqualFold and NOT agenttools.Win32NameFold, on
// purpose: this compares two DIRECTORY paths for identity, and its two answers
// have opposite costs. Refusing a spelling NTFS maps is an availability bug the
// operator sees and retries; accepting a spelling NTFS keeps distinct would apply
// a manifest to the wrong tree, which is the failure this check exists to stop.
// EqualFold already merges every case pair it needs to (guard.go:134
// governanceRefusal folds name CLASSES with Win32NameFold, which is a different
// question; the root-relative comparisons this package makes inside one tree use
// namePolicy, the probed ASCII fold, which is a third and narrower question).
//
// Deliberately NOT folded on other hosts: /tmp/X and /tmp/x are two different
// directories on Linux, so folding there would ACCEPT a manifest for the wrong
// tree - the opposite of the failure direction this check exists for.
//
// This is the ONE remaining GOOS gate in the package, and it is kept on purpose
// (FIX-E1, N3 scope note): the probe below answers "does this volume map ASCII
// case onto one name" for a path that exists, while this check compares two
// arbitrary SPELLINGS, one of which (the manifest's root) may no longer exist and
// neither of which may be writable - so a probe here would answer "inconclusive"
// exactly when the answer matters. Neither finding that motivated the probe (F1's
// file identity, N2's claim semantics) goes through sameRoot. Recorded residue: on
// a case-insensitive non-Windows volume a case-variant root spelling is refused
// here as ErrRootMismatch, which is the availability side and is documented in
// coder-report-fix-d1b.md §9.4.
var caseFoldedRoots = runtime.GOOS == "windows"

// sameRoot reports whether two absolute paths name the same directory: equal
// volume names (case-folded) and equal cleaned paths (case-folded on Windows).
func sameRoot(a, b string) bool {
	if !strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b)) {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if caseFoldedRoots {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// canonicalRoot renders root as the absolute, symlink-resolved directory every
// other function compares against. Take stamps it into manifest.Root and
// rollback/diff compare their own root through this same function and sameRoot,
// so a manifest can only ever be applied to the tree it was taken from.
//
// Resolution mirrors the E3 rule of expandClaimedFiles (merge.go:349-353): the
// walk must run on the resolved root, otherwise a root that is itself a
// symlink/junction walks nothing. A resolution error falls back to the logical
// absolute path (the pre-E3 behaviour) so a not-yet-existing root still produces
// a named error from the caller's Stat rather than a bogus one here.
func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("codesnap: root is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("codesnap: resolve root %s: %w", root, err)
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil && resolved != "" {
		return resolved, nil
	}
	return abs, nil
}

// walkScoped enumerates the files the scopes claim, in lexical walk order, and
// what it had to withhold or refuse.
//
// Claims are recorded under the symlink-RESOLVED real path (merge.go:358-390):
// when a symlink and its target are both walked, both entries resolve to one
// real path, so the file is claimed exactly once. A file is claimed when its
// lexical path OR its real path is in scope (agenttools.PathInScope, guard.go:231).
//
// excludeAbs is this package's own artifact directory (<destDir>): it is
// excluded from the walk so the ledger - the previous snapshot's copies, the
// manifest, the archive - is never walked into a claim. The exclusion's relative
// form keeps the spelling of the --out operand while the walk reports the on-disk
// spelling, so the comparison goes through the policy's DIRECTORY PREFIX form
// (namePolicy.pathUnder: the ASCII case fold on a volume that has one, exact
// elsewhere). A spelling difference must not switch the exclusion off (R3-7), and
// the ASCII-only fold must not switch it on for a directory that merely LOOKS
// like the ledger's name (a created "reſults/code/<ts>/payload" stays visible to
// diff and rollback, SEC r4 F1).
//
// Refused (error, fail closed): a claim on a governance/credential file or on
// VCS metadata, judged on the recorded real path through
// agenttools.GovernanceWriteRefusal (guard.go:574) and
// agenttools.VCSMetadataRefusal (guard.go:728). This is an error rather than a
// skip because both callers must stop: Take must not write a governance
// baseline, and Rollback must not archive a governance file.
//
// Withheld (withheld list, not an error): a claim whose final name is in the
// secret class - checked on the lexical name AND on the recorded real path - so
// no byte is ever copied. Take records the withheld rows in the manifest;
// Rollback leaves the files in place and names them in rollback.json.
//
// Two further arms were added by FIX-F4, and neither reads a byte of the file it
// judges:
//
//   - Q1: ledger fingerprints. A directory a scope could claim that carries the
//     ledger SHAPE (manifest.json with this package's schema version and a
//     snapshot_dir, plus snapshot/) is recorded in ledgerDirs, and the caller
//     refuses the whole take. The test is gated on the cheap claim check first, so
//     the stats and the bounded head read it costs are paid only for directories a
//     scope could actually reach. A directory LINK to such a directory is resolved
//     and judged the same way (FIX-F5, W2), because this walk does not follow it.
//   - Q2: hardlink aliases. Every file in the walk whose NAME is denylisted
//     (governance/credential, VCS metadata, secret class) contributes its identity
//     to a set, and a claim whose INODE is one of those identities is withheld like
//     a secret: no bytes are copied, and rollback will not archive it either. The
//     name arms cannot see this shape - a hardlink has no alias view, so
//     ResolvedRelPath answers with the benign name - which is what SEC-mimopro's
//     F-R6-2 measured.
//
// refusals are structural problems the caller decides on: Take turns a non-empty
// list into a hard error (fail closed, no manifest), rollback records them in
// rollback.json and keeps going (it must not touch what it could not judge).
//
// policy is the ROOT policy and gates the exclusion; claimPolicy is this run's
// ledger policy (ledgerPolicyFor) and is the FLOOR for the fingerprint test: each
// DISCOVERED ledger is additionally judged on the fold measured at the ledger's own
// subtree (FIX-F5, W6), so a fold boundary that encloses a prior ledger but neither
// the root nor this run's destination cannot switch the refusal off. The exclusion
// is never widened (FIX-F4, Q3).
func walkScoped(rootAbs string, scopes []string, excludeAbs string, policy, claimPolicy namePolicy) (walkResult, error) {
	var res walkResult
	excludeRel := ""
	if excludeAbs != "" {
		// Same path space as rootAbs: a destination spelled through the root's own
		// link would otherwise fail the Rel test and switch the exclusion off.
		if rel, ok := rootRelativeDir(rootAbs, canonicalDirish(excludeAbs)); ok {
			excludeRel = rel
		}
	}

	res.claims = []string{}
	res.withheld = []SkippedFile{}
	res.refusals = []SkippedFile{}
	res.ledgerDirs = []string{}
	seen := map[string]bool{}
	// denylistedInodes holds the identity of every walked file whose NAME is in a
	// denylisted class, whether or not it is in scope (FIX-F4, Q2). It is collected
	// in the walk and consulted after it, because the claim may be visited before
	// the file it aliases.
	var denylistedInodes []denylistedFile
	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(rootAbs, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if policy.pathUnder(excludeRel, rel) {
				return fs.SkipDir
			}
			// Q1: a run ledger under the walk. Only a directory a scope could claim is
			// worth two stats, and only a directory that CARRIES THE LEDGER SHAPE is
			// worth the fold probe measured inside it (FIX-G1, P2-a). The pre-gate
			// runs the claim test under the FOLD-ON policy, which is a proven
			// superset of every measured-fold verdict (scopeMayClaimUnder only ADDS
			// the folded chain arm), so no claimable directory is skipped - and the
			// probe, which creates a throw-away directory inside the one it measures,
			// is paid per ledger CANDIDATE instead of per walked directory.
			// The claim test for THIS ledger then runs on the fold measured at the
			// ledger itself (FIX-F5, W6): root+destination alone miss a folding
			// boundary that encloses the PRIOR ledger but neither the root nor this
			// run's output directory (a Linux container bind-mount shape).
			if rel != "." {
				if _, maybe := ledgerClaimingEntry(namePolicy{foldCase: true}, scopes, rel); maybe && isLedgerFingerprintDir(p) {
					if _, claimable := ledgerClaimingEntry(ledgerPolicyFor(rootAbs, p), scopes, rel); claimable {
						res.ledgerDirs = append(res.ledgerDirs, rel)
					}
				}
			}
			return nil
		}
		if policy.pathUnder(excludeRel, rel) {
			return nil
		}
		// Q1 (FIX-F5, W2): a directory LINK hides a prior ledger from a walk that
		// does not follow it - WalkDir reports the junction/symlink as a non-directory
		// entry, so the arm above never sees the directory behind it. Resolve the
		// entry and run the same fingerprint + claim test on the real directory; a
		// link that resolves to a file (or to nothing) falls through to the claim
		// logic below unchanged, because the dual view claims files through links on
		// purpose. Refusing the whole take is the fail-closed direction the Q1a rule
		// already chose.
		if d.Type()&fs.ModeSymlink != 0 {
			if info, serr := os.Stat(p); serr == nil && info.IsDir() {
				if rel != "." {
					// Same gated order as the directory arm (FIX-G1, P2-a).
					if _, maybe := ledgerClaimingEntry(namePolicy{foldCase: true}, scopes, rel); maybe && isLedgerFingerprintDir(p) {
						if _, claimable := ledgerClaimingEntry(ledgerPolicyFor(rootAbs, p), scopes, rel); claimable {
							res.ledgerDirs = append(res.ledgerDirs, rel)
						}
					}
				}
				return nil
			}
			// A link to a FILE (or a broken one) falls through to the claim logic below,
			// unchanged: the dual view claims files through links on purpose.
		}
		// Q2: remember the identity behind every denylisted NAME before the scope
		// test, so an out-of-scope .env or AGENTS.md still contributes its inode.
		if denylistedName(rel) {
			if info, ierr := d.Info(); ierr == nil {
				denylistedInodes = append(denylistedInodes, denylistedFile{rel: rel, info: info})
			}
		}
		lexicalInScope := agenttools.PathInScope(rel, scopes)
		// Dual view: the read tools follow symlinks, so a file is also reached
		// through its real path. A resolution failure is fail-closed: if the
		// lexical path is in scope we cannot tell what the file really is, so it
		// becomes a named refusal; out of scope, the entry is simply not claimed.
		realRel, resErr := agenttools.ResolvedRelPath(rootAbs, p)
		if resErr != nil {
			if lexicalInScope {
				res.refusals = append(res.refusals, SkippedFile{
					Path:   rel,
					Reason: "real path does not resolve inside the root (symlink escape or broken link): " + resErr.Error(),
				})
			}
			return nil
		}
		realRel = filepath.ToSlash(realRel)
		if policy.pathUnder(excludeRel, realRel) {
			return nil
		}
		if !lexicalInScope && !agenttools.PathInScope(realRel, scopes) {
			return nil
		}
		if seen[realRel] {
			return nil
		}
		// Only a regular file is a claim: a directory reached through a symlink is
		// a prefix of the scope, not a file, and this walk does not descend into
		// it (WalkDir does not follow directory links).
		info, serr := os.Stat(p)
		if serr != nil {
			res.refusals = append(res.refusals, SkippedFile{Path: rel, Reason: "unstat-able: " + serr.Error()})
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			res.refusals = append(res.refusals, SkippedFile{Path: rel, Reason: "not a regular file (" + info.Mode().Type().String() + ")"})
			return nil
		}
		// S3, before the claim is taken: a governance/credential or VCS-metadata
		// target stops both callers (see the doc comment).
		if comp, hit := agenttools.GovernanceWriteRefusal(realRel); hit {
			return fmt.Errorf("%w: claimed file %q is a governance/credential path (%s); the door neither snapshots nor restores one (narrow the scope so it does not reach it)",
				ErrGovernanceRefused, realRel, comp)
		}
		if comp, hit := agenttools.VCSMetadataRefusal(realRel); hit {
			return fmt.Errorf("%w: claimed file %q is protected VCS metadata (%s); the door neither snapshots nor restores one (narrow the scope so it does not reach it)",
				ErrGovernanceRefused, realRel, comp)
		}
		// S5: withhold, do not copy. Both views are checked: a link named
		// config.md -> .env is as much a copy of the secret as the dotenv itself.
		lexicalFamily, lexicalSecret := secretClassName(rel)
		realFamily, realSecret := secretClassName(realRel)
		if lexicalSecret || realSecret {
			family := realFamily
			if family == "" {
				family = lexicalFamily
			}
			res.withheld = append(res.withheld, SkippedFile{
				Path:   realRel,
				Reason: ReasonSecretClass + " (" + family + "): withheld from the snapshot, no bytes copied",
			})
			seen[realRel] = true
			return nil
		}
		seen[realRel] = true
		res.claims = append(res.claims, realRel)
		return nil
	})
	if walkErr != nil {
		return walkResult{}, walkErr
	}
	// Q2, after the walk (the alias may be visited before the file it aliases):
	// withhold every claim whose inode is a denylisted file's inode.
	if len(denylistedInodes) > 0 && len(res.claims) > 0 {
		kept := make([]string, 0, len(res.claims))
		for _, rel := range res.claims {
			if other, alias := hardlinkAliasOf(rootAbs, rel, denylistedInodes); alias {
				res.withheld = append(res.withheld, SkippedFile{
					Path:   rel,
					Reason: ReasonHardlinkAlias + " (" + other + "): withheld from the snapshot, no bytes copied",
				})
				continue
			}
			kept = append(kept, rel)
		}
		res.claims = kept
	}
	sort.Strings(res.claims)
	sort.Strings(res.ledgerDirs)
	sortSkips(res.withheld)
	sortSkips(res.refusals)
	return res, nil
}

// denylistedFile is one walked file whose NAME is denylisted, with its identity.
type denylistedFile struct {
	rel  string
	info os.FileInfo
}

// denylistedName reports whether a root-relative path belongs to a class the
// ledger must never copy: a governance/credential path, protected VCS metadata, or
// the secret class. It is the union of the three name arms the walk already
// applies to CLAIMS, asked of every file so the Q2 inode set is complete.
func denylistedName(rel string) bool {
	if _, hit := agenttools.GovernanceWriteRefusal(rel); hit {
		return true
	}
	if _, hit := agenttools.VCSMetadataRefusal(rel); hit {
		return true
	}
	_, secret := secretClassName(rel)
	return secret
}

// hardlinkAliasOf reports whether the claim rel is the same INODE as one of the
// denylisted files, and names that file.
//
// This is the arm that no name test can replace: os.Link creates a second
// directory entry with no alias view, so ResolvedRelPath answers with the benign
// name and every name-class arm passes. The identity comparison is os.SameFile,
// which is authoritative on every platform (on Unix it compares device+inode, on
// Windows the volume serial + file index a hardlink shares).
//
// The nlink test is a fast gate only, and only where the platform reports it: a
// file with one link cannot be an alias, so the comparison is skipped. Where the
// count is unavailable (Windows) the comparison always runs.
//
// RESIDUE, stated rather than implied: the denied set is what THIS walk met, so the
// arm covers an alias of an in-root governance/credential/VCS/secret file - the
// shape the round-6 finding filed, where the target is a relative "../.env" inside
// the same tree. A hardlink whose target lives OUTSIDE the root (same volume) has no
// denylisted name anywhere in the walk, and nothing on a name-blind, inode-partial
// walk can name it: withholding every multi-link file would refuse legitimate
// hardlink trees (and Windows does not report the count at all), which is a bigger
// availability cost than the residual exposure a same-tier attacker already has by
// reading the file they linked. Recorded in coder-report-fix-f4.md §5.
func hardlinkAliasOf(rootAbs, rel string, denylisted []denylistedFile) (string, bool) {
	abs, err := absUnderRoot(rootAbs, rel)
	if err != nil {
		return "", false
	}
	info, serr := os.Stat(abs)
	if serr != nil {
		return "", false
	}
	if n, known := linkCount(info); known && n <= 1 {
		return "", false
	}
	for _, d := range denylisted {
		if d.rel == rel {
			// The claim IS the denylisted file (reached by its own name): the name arms
			// have already refused or withheld it.
			return "", false
		}
		if os.SameFile(info, d.info) {
			return d.rel, true
		}
	}
	return "", false
}

// sortSkips orders a skip list by path (deterministic error text and JSON).
func sortSkips(skips []SkippedFile) {
	sort.Slice(skips, func(i, j int) bool {
		if skips[i].Path != skips[j].Path {
			return skips[i].Path < skips[j].Path
		}
		return skips[i].Reason < skips[j].Reason
	})
}

// withheldPaths is the set of paths a manifest recorded as withheld, so a diff
// or a rollback never treats them as covered or as created.
//
// Keys are the EXACT root-relative paths (FIX-E1, finding N1): file identity is
// never folded, because the governance fold merges spellings the filesystem keeps
// apart (".ENV" and ".env" are one file, "ı" and "i" are not), and a merged key
// would let a run-created file hide behind a withheld row just as it could hide
// behind a recorded one.
func withheldPaths(m *Manifest) map[string]bool {
	out := make(map[string]bool, len(m.Skipped))
	for _, s := range m.Skipped {
		out[s.Path] = true
	}
	return out
}

// writeJSON writes v as indented JSON plus a trailing newline, atomically.
// MarshalIndent over a struct with sorted slices is byte-deterministic, which is
// what the manifest's determinism pin measures.
func writeJSON(p string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("codesnap: marshal %s: %w", filepath.Base(p), err)
	}
	data = append(data, '\n')
	if werr := providers.WriteFileAtomic(p, data, artifactFilePerm); werr != nil {
		return fmt.Errorf("codesnap: write %s: %w", p, werr)
	}
	return nil
}

// absUnderRoot joins a validated root-relative slash path onto rootAbs and
// re-checks containment. checkRelPath already refuses ".." and absolute forms,
// so this is belt-and-braces for the "never touch anything outside root" rule:
// a caller that hand-builds a manifest still cannot make rollback write outside
// the root.
func absUnderRoot(rootAbs, rel string) (string, error) {
	if err := checkRelPath(rel); err != nil {
		return "", err
	}
	abs := filepath.Join(rootAbs, filepath.FromSlash(rel))
	back, err := filepath.Rel(rootAbs, abs)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the root", rel)
	}
	return abs, nil
}

// sha256Hex is the digest form the manifest records and compares: lowercase
// hex, 64 characters.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
