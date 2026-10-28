package codesnap

// FIX-F5 pins (fix-list-7 rows W2, W6, W8).
//
// What this file locks down:
//
//	W2  a DIRECTORY LINK planted at a prior ledger (symlink or Windows junction) is
//	     resolved and judged by the ledger fingerprint, on all three doors (Take,
//	     Rollback, the pre-consent PriorLedgerClaim) - the walk does not follow the
//	     link, so without this a benign name hides the ledger. And the key-store
//	     containment check refuses EQUALITY (keys root == operation root) as well as
//	     containment, on the take door AND the rollback door.
//	W6  the fold a DISCOVERED ledger's claim is tested with is measured at that
//	     ledger's own subtree, not only from the root and this run's destination.
//	W8  the key store is bounded: the retention prune removes only keys that are BOTH
//	     outside the newest window AND recorded against a run directory that is gone.
//
// The pins are t.TempDir()-based and the key store is redirected by TestMain.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkDirectoryTo plants a DIRECTORY link at linkPath pointing to target, and reports
// false (with the reason) when the host refuses the capability. Two shapes are tried
// because they are two different primitives: os.Symlink needs the symlink privilege
// on Windows, while an NTFS junction (mklink /J) does not - and a junction is exactly
// the shape the r7 charmglm harness used to bridge into a prior ledger.
func linkDirectoryTo(t *testing.T, target, linkPath string, junction bool) bool {
	t.Helper()
	if junction {
		out, err := exec.Command("cmd", "/c", "mklink", "/J", linkPath, target).CombinedOutput()
		if err != nil {
			t.Logf("junction %s -> %s not available on this host (%v): %s", linkPath, target, err, strings.TrimSpace(string(out)))
			return false
		}
		return true
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Logf("directory symlink %s -> %s not available on this host: %v", linkPath, target, err)
		return false
	}
	return true
}

// TestTakeRefusesADirectoryLinkAtAPriorLedger is W2's first half: the walk does not
// follow directory links, so a link planted under a claimed scope used to hide the
// prior ledger completely - both ledger scans saw an ordinary non-directory entry and
// every pairwise comparison said "no ledger here". The link is now resolved and
// judged, which is the same fail-closed direction the Q1a rule already chose.
func TestTakeRefusesADirectoryLinkAtAPriorLedger(t *testing.T) {
	shapes := []struct {
		name     string
		junction bool
	}{
		{name: "directory symlink", junction: false},
		{name: "ntfs junction", junction: true},
	}
	planted := 0
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "ws")
			writeTree(t, root, map[string]string{"src/main.go": "package main\n"})
			t1 := filepath.Join(root, "reports", "code", "t1")
			if _, err := Take(root, []string{"src/**"}, t1); err != nil {
				t.Fatalf("plant the prior ledger: %v", err)
			}
			// A manifest taken BEFORE the link exists: its rollback must refuse the tree
			// it now finds, because its own scope reaches the link.
			before := filepath.Join(root, "reports", "code", "t0")
			if _, err := Take(root, []string{"src/**"}, before); err != nil {
				t.Fatalf("take before planting: %v", err)
			}
			linkRel := "src/ledJunc"
			if !linkDirectoryTo(t, t1, filepath.Join(root, "src", "ledJunc"), shape.junction) {
				t.Skipf("%s is not available on this host; the arm needs a directory link to test", shape.name)
			}
			planted++

			// The pre-consent door: the operator is about to grant src/**, which reaches
			// the link, which reaches the prior ledger.
			if dirRel, entry, refuse := PriorLedgerClaim(root, []string{"src/**"}); !refuse {
				t.Fatalf("PriorLedgerClaim(src/**) with %s at %s did not refuse (dirRel=%q entry=%q)", shape.name, linkRel, dirRel, entry)
			} else if dirRel != linkRel {
				t.Fatalf("PriorLedgerClaim named %q; want the link path %q (the operator has to see where the grant lands)", dirRel, linkRel)
			}

			// The take door: no run directory is created.
			t2 := filepath.Join(root, "reports", "code", "t2")
			_, terr := Take(root, []string{"src/**"}, t2)
			if !errors.Is(terr, ErrLedgerRefused) {
				t.Fatalf("Take(src/**) with %s error = %v; want ErrLedgerRefused", shape.name, terr)
			}
			if !strings.Contains(terr.Error(), linkRel) {
				t.Errorf("the refusal does not name the link path %q: %v", linkRel, terr)
			}
			if _, serr := os.Stat(filepath.Join(t2, manifestFileName)); !os.IsNotExist(serr) {
				t.Errorf("a manifest was written despite the refusal (%v)", serr)
			}

			// The restore door: a manifest taken BEFORE the link was planted now walks a
			// tree in which its own scope meets the link, and must refuse whole.
			if _, rerr := Rollback(root, before); !errors.Is(rerr, ErrLedgerRefused) {
				t.Fatalf("Rollback over a tree with %s at %q error = %v; want ErrLedgerRefused", shape.name, linkRel, rerr)
			}
		})
	}
	if planted == 0 {
		t.Skip("this host created neither a directory symlink nor a junction; the W2 arm needs one to be reachable")
	}
}

// TestTakeAcceptsADirectoryLinkToAnOrdinaryDirectory is the non-vacuity control for
// the pin above: resolving a directory link must not turn every link into a refusal.
// A link to an ordinary directory is still just a link the walk does not descend.
func TestTakeAcceptsADirectoryLinkToAnOrdinaryDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n", "other/notes.md": "notes\n"})
	if !linkDirectoryTo(t, filepath.Join(root, "other"), filepath.Join(root, "src", "otherLink"), false) {
		if !linkDirectoryTo(t, filepath.Join(root, "other"), filepath.Join(root, "src", "otherLink"), true) {
			t.Skip("this host created neither a directory symlink nor a junction")
		}
	}
	if _, err := Take(root, []string{"src/**"}, filepath.Join(base, "run")); err != nil {
		t.Fatalf("Take refused a link to an ordinary directory: %v", err)
	}
}

// TestRefuseKeysInsideRootRefusesTheEqualityEdge is W2's second half (CR-seat1
// F-01): rootRelativeDir answers false for the relative path ".", so the layout
// "the key store IS the operation root" slipped the guard and would have written the
// per-run secret into the very tree a scope may claim. The equality arm refuses now.
func TestRefuseKeysInsideRootRefusesTheEqualityEdge(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	keysRoot := filepath.Join(home, "ledger-keys")
	if err := os.MkdirAll(keysRoot, 0o755); err != nil {
		t.Fatalf("mkdir keys root: %v", err)
	}
	t.Setenv("AGY_PRO_HOME", home)

	// The premise: this is the layout, not a hypothetical.
	got, kerr := ledgerKeysRoot()
	if kerr != nil {
		t.Fatalf("ledgerKeysRoot: %v", kerr)
	}
	if !sameRoot(got, keysRoot) {
		t.Fatalf("premise broken: the key store resolved to %q, want %q", got, keysRoot)
	}
	// The old containment test alone does not see it - which is the whole finding.
	if _, inside := rootRelativeDir(keysRoot, keysRoot); inside {
		t.Fatal("premise broken: rootRelativeDir now reports equality as inside, so the arm would be redundant")
	}

	if err := refuseKeysInsideRoot(keysRoot); !errors.Is(err, ErrLedgerProvenance) {
		t.Fatalf("refuseKeysInsideRoot(keys root) error = %v; want ErrLedgerProvenance (equality must be refused)", err)
	}

	// The take door refuses before it writes anything, and no key file lands there.
	writeTree(t, keysRoot, map[string]string{"src/main.go": "package main\n"})
	if _, terr := Take(keysRoot, []string{"src/**"}, filepath.Join(base, "run")); !errors.Is(terr, ErrLedgerProvenance) {
		t.Fatalf("Take with root == keys store error = %v; want ErrLedgerProvenance", terr)
	}
	entries, rerr := os.ReadDir(keysRoot)
	if rerr != nil {
		t.Fatalf("read keys root: %v", rerr)
	}
	for _, e := range entries {
		if e.Name() != "src" {
			t.Errorf("a file named %q was written into the root == keys-store layout", e.Name())
		}
	}
}

// TestRollbackRefusesAKeyStorePlantedInsideTheRoot is W2's rollback half (SEC-nwglm
// LOW-2): containment was enforced only at the write door, so an operator (or a
// same-tier process) could point AGY_PRO_HOME inside --root after a legitimate take
// and have the ROLLBACK read its keys from the tree. Both doors ask the one function.
//
// The equality sub-case cannot reach this code: a ledger whose directory IS the root
// is refused earlier by "rollback destination must not be the root itself", and Take
// can never create one. Containment is the reachable shape, and it is the one pinned.
func TestRollbackRefusesAKeyStorePlantedInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("AGY_PRO_HOME", home)
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n"})
	dest := filepath.Join(root, "reports", "code", "t1")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n\n// seat edit\n"})
	if _, err := Take(root, []string{"src/**"}, dest); err != nil {
		t.Fatalf("take: %v", err)
	}

	// A legitimate rollback first: the layout is the only thing that changes below.
	if _, err := Rollback(root, dest); err != nil {
		t.Fatalf("baseline rollback: %v", err)
	}
	if _, err := Take(root, []string{"src/**"}, dest); err != nil {
		t.Fatalf("re-take after rollback: %v", err)
	}

	// Now the planted layout: the state home sits INSIDE the root, so the key store
	// does too, and the same key material is copied there so the provenance layer would
	// be satisfied - which is exactly the state the missing check let through.
	inside := filepath.Join(root, "state")
	insideKeys := filepath.Join(inside, "ledger-keys")
	if err := os.MkdirAll(insideKeys, 0o755); err != nil {
		t.Fatalf("mkdir planted store: %v", err)
	}
	keyID := ledgerKeyIDFor(dest)
	key, kerr := os.ReadFile(filepath.Join(home, "ledger-keys", keyID))
	if kerr != nil {
		t.Fatalf("read the legitimate key: %v", kerr)
	}
	if werr := os.WriteFile(filepath.Join(insideKeys, keyID), key, 0o600); werr != nil {
		t.Fatalf("plant the key: %v", werr)
	}
	t.Setenv("AGY_PRO_HOME", inside)

	_, rerr := Rollback(root, dest)
	if !errors.Is(rerr, ErrLedgerProvenance) {
		t.Fatalf("Rollback with the key store inside the root error = %v; want ErrLedgerProvenance", rerr)
	}
	if !strings.Contains(rerr.Error(), "inside the operation root") {
		t.Errorf("the refusal does not say the key store is inside the root: %v", rerr)
	}
	// And Take refuses the same layout, so the two doors agree by construction.
	if _, terr := Take(root, []string{"src/**"}, dest); !errors.Is(terr, ErrLedgerProvenance) {
		t.Errorf("Take with the key store inside the root error = %v; want ErrLedgerProvenance", terr)
	}
}

// TestDiscoveredLedgerPolicyIsMeasuredAtTheLedger is W6: the fold a DISCOVERED
// prior ledger's claim is judged with must come from that ledger's own subtree, not
// only from the root and this run's destination. Root+dest are computed once in the
// caller; a folding boundary enclosing the prior ledger (a bind-mounted casefold
// subtree in a Linux container, `reports/code` casefold while the root is byte-exact)
// is invisible to both.
//
// Measured through the probe cache the package keeps, in BOTH host directions, so the
// pin is honest on either CI host instead of asserting one platform's behavior.
func TestDiscoveredLedgerPolicyIsMeasuredAtTheLedger(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n"})
	ledger := filepath.Join(root, "reports", "code", "t1")
	if _, err := Take(root, []string{"src/**"}, ledger); err != nil {
		t.Fatalf("plant the prior ledger: %v", err)
	}

	rootFolds, conclusive := probeCaseFolding(root)
	if !conclusive {
		t.Skipf("the case-fold probe is inconclusive on %s; the placement pin needs a conclusive root probe", root)
	}

	policyMu.Lock()
	policyCache = map[string]namePolicy{}
	policyMu.Unlock()

	// The refusal path is the real caller: it must measure the ledger before it speaks.
	err := refusalForDiscoveredLedger(root, []string{"reports/code/t1/**"}, "reports/code/t1")
	if !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("refusalForDiscoveredLedger did not produce the refusal: %v", err)
	}

	policyMu.Lock()
	_, rootCached := policyCache[policyKey(root)]
	_, ledgerCached := policyCache[policyKey(ledger)]
	policyMu.Unlock()

	if !rootCached {
		t.Error("the root's conclusive answer was not cached")
	}
	if rootFolds {
		// On a uniformly folding volume the root already answers fold=true, so the
		// ledger probe is a no-op by design (the Q3/W6 cost claim).
		if ledgerCached {
			t.Errorf("the ledger %s was probed although the root already answered fold=true (a needless probe)", ledger)
		}
		return
	}
	if !ledgerCached {
		t.Fatalf("on a case-sensitive root the discovered ledger %s was never measured; a fold boundary enclosing it would be invisible (W6)", ledger)
	}
}

// TestLedgerKeysRetentionPrunesOnlyDeadLedgerKeys is W8's core: the key store is
// bounded by COUNT plus EXISTENCE. A key whose recorded run directory still exists is
// never pruned (deleting it would turn a legitimate rollback into a provenance
// refusal), a record outside the newest window whose directory is gone IS pruned, and
// a key with no record at all is never touched.
func TestLedgerKeysRetentionPrunesOnlyDeadLedgerKeys(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("AGY_PRO_HOME", home)
	keysRoot := filepath.Join(home, "ledger-keys")
	if err := os.MkdirAll(keysRoot, 0o700); err != nil {
		t.Fatalf("mkdir keys root: %v", err)
	}

	// One LIVE ledger, aged to the FRONT of the window: its key must survive even
	// though it is far outside the newest N.
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n"})
	liveDest := filepath.Join(base, "live-run")
	if _, err := Take(root, []string{"src/**"}, liveDest); err != nil {
		t.Fatalf("take the live ledger: %v", err)
	}
	liveID := ledgerKeyIDFor(liveDest)

	// Fill the store with records whose run directories do NOT exist, oldest first, so
	// the cutoff lands well past the live entry.
	total := ledgerKeyRetention + 10
	idx := ledgerKeysIndex{Version: ledgerKeysIndexVersion}
	deadIDs := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id := "dead-" + itoa3(i)
		deadIDs = append(deadIDs, id)
		if werr := os.WriteFile(filepath.Join(keysRoot, id), make([]byte, ledgerKeyBytes), 0o600); werr != nil {
			t.Fatalf("write dead key %s: %v", id, werr)
		}
		idx.Keys = append(idx.Keys, ledgerKeyRecord{
			KeyID:   id,
			Ledger:  filepath.Join(base, "gone", id),
			TakenAt: "2026-01-01T00:00:" + itoa3(i) + "Z",
		})
	}
	// The live record is the OLDEST of all, so the newest-N window cannot save it.
	idx.Keys = append(idx.Keys, ledgerKeyRecord{KeyID: liveID, Ledger: liveDest, TakenAt: "2020-01-01T00:00:00Z"})
	if werr := writeLedgerKeysIndex(keysRoot, idx); werr != nil {
		t.Fatalf("write index: %v", werr)
	}
	// An unrecorded key file: the prune must never guess about it.
	unrecorded := "orphan-norecord"
	if werr := os.WriteFile(filepath.Join(keysRoot, unrecorded), make([]byte, ledgerKeyBytes), 0o600); werr != nil {
		t.Fatalf("write unrecorded key: %v", werr)
	}

	if err := pruneLedgerKeys(); err != nil {
		t.Fatalf("pruneLedgerKeys: %v", err)
	}

	if _, serr := os.Stat(filepath.Join(keysRoot, liveID)); serr != nil {
		t.Errorf("the key of a LIVE run directory was pruned: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(keysRoot, unrecorded)); serr != nil {
		t.Errorf("an unrecorded key file was pruned: %v", serr)
	}
	after, ierr := readLedgerKeysIndex(keysRoot)
	if ierr != nil {
		t.Fatalf("read index after prune: %v", ierr)
	}
	keptDead := 0
	for _, rec := range after.Keys {
		if rec.KeyID == liveID {
			continue
		}
		keptDead++
		if _, serr := os.Stat(filepath.Join(keysRoot, rec.KeyID)); serr != nil {
			t.Errorf("record %s was kept but its key file is gone: %v", rec.KeyID, serr)
		}
	}
	if keptDead != ledgerKeyRetention {
		t.Errorf("kept %d dead-ledger records; want exactly the newest %d", keptDead, ledgerKeyRetention)
	}
	// The pruned ones are the OLDEST records, and their files are gone.
	for _, id := range deadIDs[:total-ledgerKeyRetention] {
		if _, serr := os.Stat(filepath.Join(keysRoot, id)); !os.IsNotExist(serr) {
			t.Errorf("dead key %s survived the prune (%v)", id, serr)
		}
	}

	// Idempotent: a second prune changes nothing.
	before := len(after.Keys)
	if err := pruneLedgerKeys(); err != nil {
		t.Fatalf("second pruneLedgerKeys: %v", err)
	}
	again, _ := readLedgerKeysIndex(keysRoot)
	if len(again.Keys) != before {
		t.Errorf("the second prune changed the record count from %d to %d", before, len(again.Keys))
	}
}

// TestTakeRecordsItsKeyInTheIndex pins the bookkeeping half: without a record the
// prune can never prove a key is dead, so the take must write one - and a re-take must
// refresh it instead of adding a second record for the same ledger.
func TestTakeRecordsItsKeyInTheIndex(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("AGY_PRO_HOME", home)
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n"})
	dest := filepath.Join(base, "run")

	if _, err := Take(root, []string{"src/**"}, dest); err != nil {
		t.Fatalf("take: %v", err)
	}
	idx, ierr := readLedgerKeysIndex(filepath.Join(home, "ledger-keys"))
	if ierr != nil {
		t.Fatalf("read index: %v", ierr)
	}
	if len(idx.Keys) != 1 {
		t.Fatalf("index holds %d records after one take; want 1", len(idx.Keys))
	}
	if idx.Keys[0].KeyID != ledgerKeyIDFor(dest) {
		t.Errorf("record names key %q; want %q", idx.Keys[0].KeyID, ledgerKeyIDFor(dest))
	}
	if !sameRoot(idx.Keys[0].Ledger, canonicalDirish(dest)) {
		t.Errorf("record names ledger %q; want the RESOLVED ledger %q (FIX-G1 records the directory, not the spelling)", idx.Keys[0].Ledger, canonicalDirish(dest))
	}

	if _, err := Take(root, []string{"src/**"}, dest); err != nil {
		t.Fatalf("re-take: %v", err)
	}
	after, _ := readLedgerKeysIndex(filepath.Join(home, "ledger-keys"))
	if len(after.Keys) != 1 {
		t.Fatalf("a re-take added a record: %d records, want 1", len(after.Keys))
	}
	if !sameRoot(after.Keys[0].Ledger, canonicalDirish(dest)) {
		t.Errorf("the refreshed record names %q; want the RESOLVED ledger %q", after.Keys[0].Ledger, canonicalDirish(dest))
	}
}

// itoa3 renders i as a fixed three-digit decimal, so the test's timestamps sort
// lexicographically in numeric order without pulling fmt into a hot path.
func itoa3(i int) string {
	if i < 0 || i > 999 {
		return "000"
	}
	return string([]byte{byte('0' + i/100), byte('0' + (i/10)%10), byte('0' + i%10)})
}
