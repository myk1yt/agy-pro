// Provenance binding for the run ledger (FIX-F4, findings Q1b / SEC-mimopro
// F-R6-1).
//
// Before this file, <destDir>/manifest.json carried no integrity MAC: a seat
// scoped to a PREVIOUS run directory could rewrite that run's manifest and
// snapshot copies with policy-passing rows, and the operator's documented
// recovery (`agy pro code rollback --run <that dir>`) would install the attacker
// bytes and report them as the recorded baseline. The scope gate could not stop
// it, because the gate only ever compared a scope against the CURRENT run's
// output directory.
//
// Two layers close that. The first is here: every manifest Take writes is signed
// with a per-run secret, and LoadManifest verifies the signature BEFORE it
// believes a single row:
//
//   - the key is 32 bytes from crypto/rand, different for every take;
//   - it is stored OUTSIDE every root containment, under
//     ~/.agy-pro/ledger-keys/<key id> (0700 directory, 0600 file), which the
//     seat-scope containment never reaches - a scoped seat can only touch paths
//     its scope claims, and no scope may claim a governance/credential path;
//   - manifest.sig holds HMAC-SHA256(key, manifest bytes) as lowercase hex, over
//     exactly the bytes on disk, so a hand-edited or forged manifest fails even
//     when every row it names would pass the path policy;
//   - a manifest with no sidecar, or whose key file is gone, is refused as an
//     "unsigned ledger (pre-provenance format)" instead of being trusted.
//
// The key id is DERIVED from the ledger's absolute path, not random, so the
// manifest bytes stay deterministic for a given (root, scopes, destDir) - the
// determinism pin in snapshot_test.go measures that - while the key MATERIAL is
// random per take. A re-take into the SAME directory, spelled the same way,
// replaces the key file, so an older manifest+sig pair kept aside no longer
// verifies. A re-take through a DIFFERENT SPELLING of the same directory (another
// ASCII case on a folding volume, a junction path, a relative form) derives a
// second id and writes a second key, so the older pair KEEPS verifying - the
// disclosed stale-baseline replay bound (the "a genuine manifest replayed over a
// newer one" residue), not a revocation. The id is deliberately not
// case-resolved: the key must name the run directory the operator's spelling
// describes, and the operands of a take are the operands, not the volume's answer.
//
// The id is RECORDED in the manifest (key_id) and read back from there when a
// ledger is verified, instead of being re-derived from the caller's --run spelling:
// the operator may name the same run directory in another ASCII case, in a relative
// form, or through a junction, and none of those may decide which key is consulted.
// key_id sits inside the signed bytes, so pointing it elsewhere only makes the MAC
// fail (the attacker would need the key they just named).
//
// Consequences that are stated rather than hidden:
//
//   - run directories written before this unit have no manifest.sig at all, so
//     they refuse. They are evidence, not restore targets; `agy pro code rollback`
//     over one reports the provenance refusal and touches nothing;
//   - a ledger COPIED to another path still verifies (the key id travels inside the
//     signed bytes), which is what keeps a relocated run directory inspectable - the
//     bytes are the bytes the run signed. It does NOT widen what a rollback may do:
//     Rollback still refuses a manifest whose recorded root is not the tree in front
//     of it (sameRoot), so a copied ledger is restorable only INTO its recorded root;
//   - the binding's SCOPE is seat-tier and below. The key store lives outside every
//     operation root, so no scope a seat can claim reaches it. A process running at
//     the OPERATOR's own privilege (the verify child runs operator-authored argv, and
//     any same-user process) can read ~/.agy-pro/ledger-keys and sign anything - it
//     can also write any file in the tree directly, with no key at all. That is the
//     accepted threat-model boundary, consistent with explicit-verify consent, and it
//     is stated in docs/code.md §4.4 and in decisions.md;
//   - the Windows mode bits below are advisory: NTFS ACLs decide, and the
//     containment that actually keeps the key away from a scoped seat is the scope
//     filter, not the 0600.
package codesnap

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agypro/internal/providers"
)

const (
	// manifestSigName is the provenance sidecar beside manifest.json.
	manifestSigName = "manifest.sig"

	// ledgerKeyBytes is the per-run secret's length. One HMAC-SHA256 key.
	ledgerKeyBytes = 32

	// ledgerKeysDirName is the directory under the agy-pro state home that holds
	// the per-run keys. It lives OUTSIDE every operation root by construction: the
	// state home is a governance/credential path, so no seat scope may claim it.
	ledgerKeysDirName = "ledger-keys"

	// ledgerKeyDirPerm / ledgerKeyFilePerm are the owner-only modes of the key
	// store. On Windows the modes are advisory (NTFS ACLs decide); disclosed in the
	// package documentation rather than implied to be enforcement.
	ledgerKeyDirPerm  os.FileMode = 0o700
	ledgerKeyFilePerm os.FileMode = 0o600
)

// ErrLedgerProvenance is wrapped by every refusal that doubts WHERE a manifest
// came from: a missing sidecar, a missing key, or a signature that does not match
// the bytes. Callers can test errors.Is against it to tell a provenance refusal
// from a shape or path-policy refusal.
var ErrLedgerProvenance = errors.New("codesnap: ledger provenance")

// ledgerKeysRoot returns the directory holding the per-run keys:
// <agy-pro state home>/ledger-keys, resolved through providers.Home() so the one
// definition of "where agy-pro keeps its state" is reused (that home honours
// AGY_PRO_HOME, which is also what keeps a test run from writing into an
// operator's real home).
func ledgerKeysRoot() (string, error) {
	home, err := providers.Home()
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve the agy-pro state home for the ledger key store: %v", ErrLedgerProvenance, err)
	}
	return filepath.Join(home, ledgerKeysDirName), nil
}

// ledgerKeyIDFor renders the key-store file name for a ledger at destAbs.
//
// Deterministic on purpose (see the file comment): the id is the ledger
// directory's own name plus a short digest of its absolute path, so two runs into
// same-named directories under different roots do not share a key, and the
// manifest bytes do not vary with a random value.
func ledgerKeyIDFor(destAbs string) string {
	clean := filepath.Clean(destAbs)
	sum := sha256.Sum256([]byte(clean))
	base := sanitizeKeyID(filepath.Base(clean))
	return base + "-" + hex.EncodeToString(sum[:8])
}

// sanitizeKeyID keeps a directory name usable as a key file name: ASCII letters,
// digits, '.', '_' and '-' survive, everything else becomes '_', a leading dot is
// dropped (so "." and ".." cannot appear) and the result is bounded. The digest
// half of the id is what actually keeps ids unique, so this only has to be safe.
func sanitizeKeyID(name string) string {
	var b strings.Builder
	for i := 0; i < len(name) && b.Len() < 48; i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		case c == '.':
			if b.Len() > 0 {
				b.WriteByte('_')
			}
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}

// validKeyID refuses an id that could name something outside the key store. Load
// derives the id itself rather than reading it from the manifest, so this is
// belt-and-braces for any future caller.
func validKeyID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\:`) {
		return false
	}
	return filepath.Base(id) == id
}

// newLedgerKey returns one fresh 32-byte secret from crypto/rand.
func newLedgerKey() ([]byte, error) {
	key := make([]byte, ledgerKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("%w: cannot draw the per-run ledger key: %v", ErrLedgerProvenance, err)
	}
	return key, nil
}

// writeLedgerKey stores key at <keys root>/<keyID> with owner-only modes. The
// directory is created 0700 BEFORE the file write, because providers.WriteFileAtomic
// creates missing parents 0755 and a key store must not widen itself.
func writeLedgerKey(keyID string, key []byte) error {
	if !validKeyID(keyID) {
		return fmt.Errorf("%w: refusing to store a key under the id %q", ErrLedgerProvenance, keyID)
	}
	root, err := ledgerKeysRoot()
	if err != nil {
		return err
	}
	if merr := os.MkdirAll(root, ledgerKeyDirPerm); merr != nil {
		return fmt.Errorf("%w: create the ledger key store %s: %v", ErrLedgerProvenance, root, merr)
	}
	if werr := providers.WriteFileAtomic(filepath.Join(root, keyID), key, ledgerKeyFilePerm); werr != nil {
		return fmt.Errorf("%w: write the ledger key %s: %v", ErrLedgerProvenance, keyID, werr)
	}
	return nil
}

// readLedgerKey loads the key for one ledger. A missing key is a provenance
// refusal, not a fallback.
func readLedgerKey(keyID string) ([]byte, error) {
	if !validKeyID(keyID) {
		return nil, fmt.Errorf("%w: the derived key id %q is not a single path component", ErrLedgerProvenance, keyID)
	}
	root, err := ledgerKeysRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, keyID)
	key, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, fmt.Errorf("%w: no key at %s (its key file is gone, so nothing can be proven about this manifest; the key id travels INSIDE the signed bytes, so a COPY of a ledger still verifies, while a rollback only ever applies to the root the manifest records)", ErrLedgerProvenance, path)
		}
		return nil, fmt.Errorf("%w: read %s: %v", ErrLedgerProvenance, path, rerr)
	}
	if len(key) != ledgerKeyBytes {
		return nil, fmt.Errorf("%w: the key file %s holds %d bytes, want %d", ErrLedgerProvenance, path, len(key), ledgerKeyBytes)
	}
	return key, nil
}

// manifestMAC renders HMAC-SHA256(key, manifest bytes) as lowercase hex: the exact
// content of manifest.sig.
func manifestMAC(key, manifestBytes []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(manifestBytes)
	return hex.EncodeToString(mac.Sum(nil))
}

// refuseKeysInsideRoot is the containment half of the key store: if the resolved
// state home happens to sit inside the operation root (an unusual but legal
// layout), the key would be reachable by a scope and the binding would be
// worthless, so the take refuses instead of writing a secret a seat could read.
//
// It refuses CONTAINMENT and EQUALITY (FIX-F5, W2). rootRelativeDir answers
// "inside" for a strict descendant only - it returns false for the relative path
// "." - so a root that IS the key store (--root ~/.agy-pro/ledger-keys, or a state
// home whose ledger-keys directory is the root) slipped the guard and landed the
// per-run secret inside the claimable tree. Both doors ask this function: Take
// before it writes a key, Rollback before it reads one (the check was missing there
// until FIX-F5).
func refuseKeysInsideRoot(rootAbs string) error {
	root, err := ledgerKeysRoot()
	if err != nil {
		return err
	}
	keysAbs := canonicalDirish(root)
	if sameRoot(rootAbs, keysAbs) {
		return fmt.Errorf("%w: the ledger key store %s IS the operation root, so a seat scoped to this tree could read the per-run key; move the agy-pro state home outside --root", ErrLedgerProvenance, root)
	}
	if rel, inside := rootRelativeDir(rootAbs, keysAbs); inside {
		return fmt.Errorf("%w: the ledger key store %s is inside the operation root (relative %q), so a seat scoped to this tree could read the per-run key; move the agy-pro state home outside --root", ErrLedgerProvenance, root, rel)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Retention (FIX-F5, finding W8): the key store is bounded.
//
// Every distinct run directory leaves one 32-byte key file at
// <home>/ledger-keys/<key id>, and nothing used to remove one, so the store grew
// with every run ever taken (r7 CR-seat1 P3, CR-charmqwen F-04). The repo already
// has this pattern for a key-adjacent store (providers.pruneOldKeyBackups, S-5), and
// the same shape is used here: keep the newest ledgerKeyRetention records and prune
// beyond them.
//
// The retention rule is COUNT plus EXISTENCE, deliberately, and the order matters:
//
//   - a key is NEVER pruned while the run directory it signs still exists. That is
//     the "live-run keys" rule: a ledger whose directory is gone is inert evidence,
//     while a ledger whose directory is still there is a rollback target, and
//     deleting its key would turn a legitimate restore into a provenance refusal;
//   - a key is only pruned when it is BOTH outside the newest ledgerKeyRetention
//     records AND recorded against a run directory that no longer exists;
//   - a record without a usable path (absent, unreadable, not a directory) is
//     unknown, and unknown is KEPT - the prune never guesses.
//
// Age is deliberately NOT an arm. A 30-day-old run directory can still be the
// operator's restore target, so age alone would delete a working undo key; the
// existence test asks the question that actually matters.
//
// Two properties of that test are load-bearing (FIX-G1, P2-b):
//
//   - the record holds the RESOLVED ledger directory (canonicalDirish), not the
//     spelling the take was given, so removing an alias spelling - a junction, a
//     subst drive, another ASCII case - cannot make a live ledger look dead;
//   - the existence test runs AGAIN immediately before the removal. Between the
//     first stat and the os.Remove a concurrent take can re-create that exact run
//     directory, and the key it writes then is live; the second check can only add
//     keeps, so a dead record is still pruned and no live key is removed.
//
// The index is what makes this possible at all: a key id is a digest, so the key
// file alone cannot say which ledger it signs. It is ONE small JSON file beside the
// keys, written on every take and never trusted for anything security-relevant - a
// missing, unreadable or corrupt index disables the prune (over-retention) and never
// the binding. A key file with no record is never deleted, because a record is the
// only thing that can prove it is dead.
// ---------------------------------------------------------------------------

const (
	// ledgerKeysIndexName is the store's own record of which ledger each key signs.
	// The name carries a suffix no key id can have (sanitizeKeyID turns '.' into
	// '_'), so it can never collide with a key file.
	ledgerKeysIndexName = "ledger-keys.index.json"

	// ledgerKeysIndexVersion is the index format version. An unknown version is
	// treated as unreadable, which disables the prune rather than guessing.
	ledgerKeysIndexVersion = 1

	// ledgerKeyRetention is how many key records the store keeps regardless of
	// whether their run directories still exist.
	ledgerKeyRetention = 50
)

// ledgerKeyRecord is one key's provenance in the local index.
type ledgerKeyRecord struct {
	KeyID   string `json:"key_id"`
	Ledger  string `json:"ledger"`
	TakenAt string `json:"taken_at"`
}

// ledgerKeysIndex is the store's index file.
type ledgerKeysIndex struct {
	Version int               `json:"version"`
	Keys    []ledgerKeyRecord `json:"keys"`
}

// readLedgerKeysIndex loads the index. A missing file yields an empty index (first
// run); an unreadable, corrupt or version-unknown file yields an empty index AND an
// error, so a caller that prunes knows to stop instead of treating "no records" as
// "every key is unknown".
func readLedgerKeysIndex(root string) (ledgerKeysIndex, error) {
	empty := ledgerKeysIndex{Version: ledgerKeysIndexVersion}
	data, err := os.ReadFile(filepath.Join(root, ledgerKeysIndexName))
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, err
	}
	var idx ledgerKeysIndex
	if uerr := json.Unmarshal(data, &idx); uerr != nil {
		return empty, uerr
	}
	if idx.Version != ledgerKeysIndexVersion {
		return empty, fmt.Errorf("ledger key index version %d is not %d", idx.Version, ledgerKeysIndexVersion)
	}
	return idx, nil
}

// writeLedgerKeysIndex stores the index atomically with the store's own mode. The
// records are sorted by take time (then id) so the file is byte-stable and the
// "newest N" cut is a slice, not a sort at prune time.
func writeLedgerKeysIndex(root string, idx ledgerKeysIndex) error {
	idx.Version = ledgerKeysIndexVersion
	sort.SliceStable(idx.Keys, func(i, j int) bool {
		if idx.Keys[i].TakenAt != idx.Keys[j].TakenAt {
			return idx.Keys[i].TakenAt < idx.Keys[j].TakenAt
		}
		return idx.Keys[i].KeyID < idx.Keys[j].KeyID
	})
	if idx.Keys == nil {
		idx.Keys = []ledgerKeyRecord{}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return providers.WriteFileAtomic(filepath.Join(root, ledgerKeysIndexName), data, ledgerKeyFilePerm)
}

// recordLedgerKey adds (or refreshes) the record for one take. Best effort at the
// call site: a take that cannot record still produced a signed ledger, and an
// unrecorded key is simply never pruned.
func recordLedgerKey(root, keyID, destAbs string) error {
	idx, ierr := readLedgerKeysIndex(root)
	if ierr != nil {
		// A corrupt index is replaced by a fresh one on the next successful take:
		// losing records only means losing prune candidates, never a key.
		idx = ledgerKeysIndex{Version: ledgerKeysIndexVersion}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// The record names the RESOLVED directory, not the operator's spelling (FIX-G1,
	// P2-b). The existence test at prune time must track the DIRECTORY: a take
	// through a junction/subst/alias spelling whose alias is later removed leaves the
	// ledger reachable at its real path, and a record that kept the alias would read
	// NotExist and delete a live undo key. canonicalDirish resolves the longest
	// existing prefix and appends the rest, so it answers for a run directory that
	// does not exist yet as well, and it falls back to the absolute spelling.
	ledgerAbs := canonicalDirish(destAbs)
	replaced := false
	for i := range idx.Keys {
		if idx.Keys[i].KeyID == keyID {
			idx.Keys[i].Ledger = ledgerAbs
			idx.Keys[i].TakenAt = now
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Keys = append(idx.Keys, ledgerKeyRecord{KeyID: keyID, Ledger: ledgerAbs, TakenAt: now})
	}
	return writeLedgerKeysIndex(root, idx)
}

// pruneLedgerKeys enforces the retention rule above. Best effort and idempotent: it
// is called after a successful take and its failure never fails the take (the
// precedent is providers.pruneOldKeyBackups, invoked as `_ = pruneOldKeyBackups`).
//
// It removes a key FILE only for a record that is outside the newest
// ledgerKeyRetention records AND whose recorded ledger directory no longer exists.
// Everything else is kept, including records it cannot judge.
func pruneLedgerKeys() error {
	root, err := ledgerKeysRoot()
	if err != nil {
		return err
	}
	idx, ierr := readLedgerKeysIndex(root)
	if ierr != nil {
		return ierr
	}
	if len(idx.Keys) <= ledgerKeyRetention {
		return nil
	}
	kept := make([]ledgerKeyRecord, 0, len(idx.Keys))
	kept = append(kept, idx.Keys[len(idx.Keys)-ledgerKeyRetention:]...)
	var errs []error
	for _, rec := range idx.Keys[:len(idx.Keys)-ledgerKeyRetention] {
		if rec.Ledger == "" || !validKeyID(rec.KeyID) {
			kept = append(kept, rec) // nothing proven: unknown is kept
			continue
		}
		info, serr := os.Stat(rec.Ledger)
		if serr == nil && info.IsDir() {
			kept = append(kept, rec) // the run directory is still there: live key
			continue
		}
		if serr != nil && !os.IsNotExist(serr) {
			kept = append(kept, rec) // cannot tell: keep
			continue
		}
		// Re-checked immediately before the removal (FIX-G1, P2-b / SEC-seat1 LOW-1):
		// a concurrent take can re-create this exact run directory between the stat
		// above and the os.Remove below, and the key it writes then is LIVE. The
		// re-check can only ADD keeps, so the retention window is unchanged and a
		// genuinely dead record is still pruned.
		if rinfo, rserr := os.Stat(rec.Ledger); rserr == nil && rinfo.IsDir() {
			kept = append(kept, rec)
			continue
		}
		if rerr := os.Remove(filepath.Join(root, rec.KeyID)); rerr != nil && !os.IsNotExist(rerr) {
			errs = append(errs, fmt.Errorf("prune ledger key %s: %w", rec.KeyID, rerr))
			kept = append(kept, rec)
		}
	}
	if werr := writeLedgerKeysIndex(root, ledgerKeysIndex{Version: ledgerKeysIndexVersion, Keys: kept}); werr != nil {
		errs = append(errs, werr)
	}
	return errors.Join(errs...)
}

// writeLedgerManifest is Take's write path: it records the manifest and binds it
// to a fresh per-run key. The manifest bytes are written verbatim (they are what
// the signature covers), so a reader that verifies the sidecar is verifying the
// bytes on disk.
func writeLedgerManifest(destAbs string, m *Manifest) error {
	data, err := marshalManifestJSON(m)
	if err != nil {
		return err
	}
	keyID := ledgerKeyIDFor(destAbs)
	key, kerr := newLedgerKey()
	if kerr != nil {
		return kerr
	}
	if werr := writeLedgerKey(keyID, key); werr != nil {
		return werr
	}
	sig := manifestMAC(key, data)
	if werr := providers.WriteFileAtomic(filepath.Join(destAbs, manifestFileName), data, artifactFilePerm); werr != nil {
		return fmt.Errorf("codesnap: write %s: %w", filepath.Join(destAbs, manifestFileName), werr)
	}
	if werr := providers.WriteFileAtomic(filepath.Join(destAbs, manifestSigName), []byte(sig+"\n"), artifactFilePerm); werr != nil {
		return fmt.Errorf("codesnap: write the provenance sidecar for %s: %w", filepath.Join(destAbs, manifestFileName), werr)
	}
	// Retention bookkeeping (FIX-F5, W8): record which ledger this key signs, then
	// prune beyond the retention window. Both are best effort - the ledger above is
	// signed and on disk, and a bookkeeping failure must not fail a take. A missing
	// record only means the key is never pruned.
	if root, rerr := ledgerKeysRoot(); rerr == nil {
		_ = recordLedgerKey(root, keyID, destAbs)
	}
	_ = pruneLedgerKeys()
	return nil
}

// marshalManifestJSON renders a manifest the way it is stored: indented JSON plus
// a trailing newline. MarshalIndent over a struct with sorted slices is
// byte-deterministic, which is what lets the key id be derived instead of random.
func marshalManifestJSON(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("codesnap: marshal %s: %w", manifestFileName, err)
	}
	return append(data, '\n'), nil
}

// verifyLedgerProvenance is the reader half: it refuses manifestBytes unless they
// match the sidecar beside them under the run's own key.
//
// The key is named by the manifest's own key_id field, which is covered by the MAC.
// That is deliberate: the operator's --run spelling (another ASCII case, a relative
// form, a path through a junction) must not decide WHICH key is consulted, or a
// legitimate ledger would refuse for saying its own name differently. Rewriting
// key_id does not help an attacker - they would have to produce the MAC under the
// key they just named.
//
// Every failure wears ErrLedgerProvenance and names what was missing, so the
// operator sees "unsigned ledger (pre-provenance format)" for a pre-FIX-F4 run
// directory rather than a confusing signature error.
func verifyLedgerProvenance(destDir string, manifestBytes []byte) error {
	destAbs, err := filepath.Abs(filepath.Clean(destDir))
	if err != nil {
		return fmt.Errorf("%w: resolve the ledger directory %s: %v", ErrLedgerProvenance, destDir, err)
	}
	var head struct {
		KeyID string `json:"key_id"`
	}
	if uerr := json.Unmarshal(manifestBytes, &head); uerr != nil {
		return fmt.Errorf("%w: %s cannot be parsed, so it cannot be proven: %v", ErrLedgerProvenance, filepath.Join(destAbs, manifestFileName), uerr)
	}
	sigPath := filepath.Join(destAbs, manifestSigName)
	sigRaw, serr := os.ReadFile(sigPath)
	if serr != nil {
		if os.IsNotExist(serr) {
			return fmt.Errorf("%w: unsigned ledger (pre-provenance format): %s has no %s sidecar, so there is nothing to check the manifest against; this run directory predates the provenance binding and is NOT restorable (it stays on disk as evidence - nothing is deleted)", ErrLedgerProvenance, destAbs, manifestSigName)
		}
		return fmt.Errorf("%w: read %s: %v", ErrLedgerProvenance, sigPath, serr)
	}
	want := strings.TrimSpace(string(sigRaw))
	if want == "" {
		return fmt.Errorf("%w: %s is empty; an unsigned ledger is never trusted", ErrLedgerProvenance, sigPath)
	}
	if strings.TrimSpace(head.KeyID) == "" {
		return fmt.Errorf("%w: unsigned ledger (pre-provenance format): %s names no key_id, so this manifest was not written by this binding and is NOT restorable (it stays on disk as evidence)", ErrLedgerProvenance, filepath.Join(destAbs, manifestFileName))
	}
	key, kerr := readLedgerKey(head.KeyID)
	if kerr != nil {
		return kerr
	}
	got := manifestMAC(key, manifestBytes)
	if !hmac.Equal([]byte(got), []byte(want)) {
		return fmt.Errorf("%w: %s does not match its sidecar: the manifest was written by something other than the run that owns this directory (a forged or hand-edited ledger is refused whole, before the tree is touched)", ErrLedgerProvenance, filepath.Join(destAbs, manifestFileName))
	}
	return nil
}
