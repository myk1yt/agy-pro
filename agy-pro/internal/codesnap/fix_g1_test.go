package codesnap

// FIX-G1 pins (the r8 preemptive-fix candidates that touch this package).
//
// What this file locks down:
//
//	P2-a  the per-directory case-fold measurement in the walk's ledger arm is paid
//	      per ledger CANDIDATE, not per walked directory - measured by counting the
//	      probe's own MkdirTemp creations on a root whose fold reads OFF.
//	P2-b  the key-store prune keeps the key of a ledger that is still reachable at
//	      its RESOLVED path after the alias spelling the take used is gone, and it
//	      re-checks the recorded directory immediately before the removal.
//	SEC-R8-1  a corrupt index disables the prune: zero keys removed.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// countFoldProbes installs the probe observer for one measurement window and hands
// back the counting function. The observer fires in probeCaseFolding right after the
// throw-away directory was created, so the count is real MkdirTemp creations - not
// policyForDir cache hits, which is why the pin measures the paid cost rather than
// the call count.
func countFoldProbes(t *testing.T) func() int {
	t.Helper()
	var probes []string
	prev := foldProbeObserver
	foldProbeObserver = func(dirAbs string) { probes = append(probes, dirAbs) }
	t.Cleanup(func() { foldProbeObserver = prev })
	return func() int { return len(probes) }
}

// forceFoldOffRoot caches a fold-OFF answer for the root, which is the condition the
// P2-a finding is about: on a case-SENSITIVE volume `ledgerPolicyFor` cannot answer
// from the root, so it measures the ledger's own subtree. NTFS folds uniformly, so
// the condition is injected instead of waited for (the same technique the W6 pin uses
// with the probe cache); the control below proves the injection actually routes
// through probeCaseFolding on this host.
func forceFoldOffRoot(t *testing.T, rootAbs string) {
	t.Helper()
	policyMu.Lock()
	policyCache = map[string]namePolicy{policyKey(rootAbs): {foldCase: false}}
	policyMu.Unlock()
}

// TestWalkProbesTheFoldOnlyForLedgerCandidates is P2-a: before the fix, a fold-OFF
// root made the walk measure EVERY directory it visited, and each measurement creates
// and removes a throw-away directory inside the one it measures - O(dirs) probes and
// O(dirs) workspace side effects on every Take and every Rollback. The arm is now
// gated on the cheap claim test (fold-ON, a proven superset) plus the ledger
// fingerprint, so only a directory that IS a ledger candidate is measured.
func TestWalkProbesTheFoldOnlyForLedgerCandidates(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	dirs := map[string]string{
		"src/a.go":            "package a\n",
		"src/sub/b.go":        "package sub\n",
		"lib/x/y/z.go":        "package y\n",
		"docs/one/note.md":    "one\n",
		"docs/two/note.md":    "two\n",
		"docs/three/note.md":  "three\n",
		"tools/build/main.go": "package main\n",
	}
	writeTree(t, root, dirs)
	ledger := filepath.Join(root, "reports", "code", "t1")
	if _, err := Take(root, []string{"src/**"}, ledger); err != nil {
		t.Fatalf("plant the prior ledger: %v", err)
	}
	rootAbs, err := canonicalRoot(root)
	if err != nil {
		t.Fatalf("canonicalRoot: %v", err)
	}

	// Control (non-vacuity): with the fold forced OFF for the root, ONE measurement
	// really does create a probe directory. Without this, "0 probes" below could mean
	// "the probe path was never reachable on this host".
	forceFoldOffRoot(t, rootAbs)
	count := countFoldProbes(t)
	_ = ledgerPolicyFor(rootAbs, filepath.Join(rootAbs, "src"))
	if got := count(); got != 1 {
		t.Fatalf("control: ledgerPolicyFor on a fold-OFF root made %d probe(s); want 1 - the measurement path is not reachable, so this pin cannot judge gating", got)
	}

	// Case A: no ledger candidate is claimable by the scope. Nothing measured.
	forceFoldOffRoot(t, rootAbs)
	count = countFoldProbes(t)
	walkA, werr := walkScoped(rootAbs, []string{"src/**"}, "", namePolicy{foldCase: false}, namePolicy{foldCase: false})
	if werr != nil {
		t.Fatalf("walkScoped (case A): %v", werr)
	}
	probesA := count()
	if len(walkA.ledgerDirs) != 0 {
		t.Fatalf("case A: the walk found ledger(s) %v under a scope that cannot reach one", walkA.ledgerDirs)
	}
	if probesA != 0 {
		t.Fatalf("case A: the walk made %d case-fold probe(s) with nothing in scope that carries the ledger shape; want 0 (the P2-a gate)", probesA)
	}

	// Case B: the scope reaches the one genuine ledger. Exactly one probe, at that
	// directory - independent of how many ordinary directories the walk visits.
	forceFoldOffRoot(t, rootAbs)
	count = countFoldProbes(t)
	walkB, werr := walkScoped(rootAbs, []string{"reports/**"}, "", namePolicy{foldCase: false}, namePolicy{foldCase: false})
	if werr != nil {
		t.Fatalf("walkScoped (case B): %v", werr)
	}
	probesB := count()
	if len(walkB.ledgerDirs) != 1 || walkB.ledgerDirs[0] != "reports/code/t1" {
		t.Fatalf("case B: ledgerDirs = %v; want exactly [reports/code/t1] (the pin is vacuous without the discovery)", walkB.ledgerDirs)
	}
	if probesB != 1 {
		t.Fatalf("case B: the walk made %d case-fold probe(s) for ONE ledger candidate; want exactly 1 (per-ledger-hit, not per-walked-directory)", probesB)
	}

	// And the outcome the gating must not change: Take still refuses that ledger, on
	// the same tree, with the same class.
	if _, terr := Take(root, []string{"reports/**"}, filepath.Join(base, "out")); terr == nil {
		t.Fatal("Take accepted a scope that can claim the prior ledger; the P2-a gate dropped a refusal")
	} else if !errors.Is(terr, ErrLedgerRefused) {
		t.Fatalf("Take refused with %v; want ErrLedgerRefused", terr)
	}
}

// removeDirectoryLink removes a directory LINK (symlink or junction) without touching
// its target: os.Remove first, then the rmdir form Windows needs for a junction.
func removeDirectoryLink(t *testing.T, linkPath string) {
	t.Helper()
	if err := os.Remove(linkPath); err == nil {
		return
	}
	if out, err := exec.Command("cmd", "/c", "rmdir", linkPath).CombinedOutput(); err != nil {
		t.Fatalf("remove the planted link %s: %v: %s", linkPath, err, strings.TrimSpace(string(out)))
	}
}

// TestPruneKeepsTheKeyOfALedgerReachableOnlyAtItsResolvedPath is P2-b's first half
// (CR-mimoflash F-02). The record used to hold the take's SPELLING, so a take through
// a junction/subst/alias whose alias was later removed read NotExist at prune time and
// deleted a live undo key - the one thing the retention rule says cannot happen. The
// record now holds the RESOLVED directory, so the existence test tracks the ledger.
func TestPruneKeepsTheKeyOfALedgerReachableOnlyAtItsResolvedPath(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("AGY_PRO_HOME", home)
	keysRoot := filepath.Join(home, "ledger-keys")
	if err := os.MkdirAll(keysRoot, 0o700); err != nil {
		t.Fatalf("mkdir keys root: %v", err)
	}

	realLedger := filepath.Join(base, "real", "run1")
	if err := os.MkdirAll(realLedger, 0o755); err != nil {
		t.Fatalf("mkdir the real ledger directory: %v", err)
	}
	alias := filepath.Join(base, "alias-run")
	if err := os.Symlink(realLedger, alias); err != nil {
		if out, jerr := exec.Command("cmd", "/c", "mklink", "/J", alias, realLedger).CombinedOutput(); jerr != nil {
			t.Skipf("this host grants no directory link primitive (symlink: %v, junction: %v: %s); the alias-spelling pin needs one", err, jerr, strings.TrimSpace(string(out)))
		}
	}

	keyID := ledgerKeyIDFor(alias)
	if err := os.WriteFile(filepath.Join(keysRoot, keyID), make([]byte, ledgerKeyBytes), 0o600); err != nil {
		t.Fatalf("write the key: %v", err)
	}
	if err := recordLedgerKey(keysRoot, keyID, alias); err != nil {
		t.Fatalf("recordLedgerKey: %v", err)
	}
	idx, ierr := readLedgerKeysIndex(keysRoot)
	if ierr != nil {
		t.Fatalf("read index: %v", ierr)
	}
	if len(idx.Keys) != 1 {
		t.Fatalf("index holds %d records; want 1", len(idx.Keys))
	}
	recorded := idx.Keys[0].Ledger
	if !sameRoot(recorded, realLedger) {
		t.Fatalf("the record names %q; want the RESOLVED ledger %q (the take's spelling is the alias)", recorded, realLedger)
	}
	if sameRoot(recorded, alias) {
		t.Fatalf("the record still names the alias spelling %q; the prune would follow a spelling, not the directory", recorded)
	}

	// Remove ONLY the alias. The ledger directory itself is untouched - exactly the
	// situation in which the pre-fix record read NotExist.
	removeDirectoryLink(t, alias)
	if _, serr := os.Stat(alias); !os.IsNotExist(serr) {
		t.Fatalf("the alias %s survived its removal (%v); the pin cannot judge the spelling", alias, serr)
	}
	if info, serr := os.Stat(recorded); serr != nil || !info.IsDir() {
		t.Fatalf("the resolved ledger %s is not reachable as a directory (%v); the scenario is broken", recorded, serr)
	}

	// Push the live record OUTSIDE the newest window, and give two dead records real
	// key files so the same prune run proves it still prunes what is proven dead.
	deadKeptSamples := []string{"dead-000", "dead-001"}
	full := ledgerKeysIndex{Version: ledgerKeysIndexVersion}
	full.Keys = append(full.Keys, ledgerKeyRecord{KeyID: keyID, Ledger: recorded, TakenAt: "2020-01-01T00:00:00Z"})
	for i := 0; i < ledgerKeyRetention+10; i++ {
		id := "dead-" + itoa3(i)
		full.Keys = append(full.Keys, ledgerKeyRecord{
			KeyID:   id,
			Ledger:  filepath.Join(base, "gone", id),
			TakenAt: "2030-01-01T00:00:" + itoa3(i) + "Z",
		})
	}
	for _, id := range deadKeptSamples {
		if werr := os.WriteFile(filepath.Join(keysRoot, id), make([]byte, ledgerKeyBytes), 0o600); werr != nil {
			t.Fatalf("write dead key %s: %v", id, werr)
		}
	}
	if werr := writeLedgerKeysIndex(keysRoot, full); werr != nil {
		t.Fatalf("write index: %v", werr)
	}

	if perr := pruneLedgerKeys(); perr != nil {
		t.Fatalf("pruneLedgerKeys: %v", perr)
	}
	if _, serr := os.Stat(filepath.Join(keysRoot, keyID)); serr != nil {
		t.Errorf("the key of a ledger that still EXISTS at %s was pruned because the alias spelling was gone: %v", recorded, serr)
	}
	// Non-vacuity: the same run did remove keys whose recorded directory is gone.
	for _, id := range deadKeptSamples {
		if _, serr := os.Stat(filepath.Join(keysRoot, id)); !os.IsNotExist(serr) {
			t.Errorf("dead key %s was not pruned (%v); the prune did nothing and the keep above proves nothing", id, serr)
		}
	}
	after, aerr := readLedgerKeysIndex(keysRoot)
	if aerr != nil {
		t.Fatalf("read index after prune: %v", aerr)
	}
	found := false
	for _, rec := range after.Keys {
		if rec.KeyID == keyID {
			found = true
		}
	}
	if !found {
		t.Error("the live record was dropped from the index although its key was kept")
	}
}

// TestPruneReChecksLivenessImmediatelyBeforeRemoval is P2-b's second half (SEC-seat1
// LOW-1): a concurrent take can re-create the recorded run directory between the
// prune's stat and its os.Remove, and the key it writes then is live. The re-check is
// pinned by ORDER inside the function, because the window itself is a race: a test
// cannot hold the other process still at exactly that instruction, and a timing-based
// pin would be flaky rather than stronger. The behavioral half of the rule (a live
// directory is kept) is pinned by TestLedgerKeysRetentionPrunesOnlyDeadLedgerKeys and
// by the alias pin above.
func TestPruneReChecksLivenessImmediatelyBeforeRemoval(t *testing.T) {
	src, err := os.ReadFile("sig.go")
	if err != nil {
		t.Fatalf("read sig.go: %v", err)
	}
	body := funcBody(string(src), "pruneLedgerKeys")
	if body == "" {
		t.Fatal("premise broken: pruneLedgerKeys not found in sig.go")
	}
	if got := strings.Count(body, "os.Stat(rec.Ledger)"); got != 2 {
		t.Fatalf("pruneLedgerKeys stats the recorded ledger %d time(s); want 2 (the keep decision and the pre-removal re-check)", got)
	}
	first := strings.Index(body, "os.Stat(rec.Ledger)")
	second := strings.LastIndex(body, "os.Stat(rec.Ledger)")
	remove := strings.Index(body, "os.Remove(")
	if remove < 0 {
		t.Fatal("pruneLedgerKeys no longer removes anything; the re-check has nothing to guard")
	}
	if !(first < remove && second < remove) {
		t.Fatalf("the recorded-ledger stat(s) do not precede the removal (first=%d second=%d remove=%d); the re-check must run immediately before os.Remove", first, second, remove)
	}
	// The re-check must be the NEAREST statement before the removal, not a stray
	// earlier one: nothing but the keep/bookkeeping lines may sit between them.
	if gap := body[second:remove]; strings.Count(gap, "os.") != 1 {
		t.Errorf("the pre-removal gap %q contains more than the re-check itself; the re-check must immediately precede the removal", strings.TrimSpace(gap))
	}
}

// funcBody returns the body of one function in src (from its declaration to the next
// top-level func), or "" when the declaration is absent.
func funcBody(src, name string) string {
	decl := "\nfunc " + name + "("
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	rest := src[i+1:]
	if j := strings.Index(rest[len(decl):], "\nfunc "); j >= 0 {
		return rest[:len(decl)+j]
	}
	return rest
}

// TestPruneIsDisabledByACorruptIndex is SEC-R8-1 (SEC-nwglmflash): the documented
// fail-safe direction - a corrupt index disables the prune instead of reading as "no
// records" - was stated in sig.go and untested. Plant a full store with a corrupt
// index: the prune must return an error and remove NOTHING, and the next take must
// replace the index with a parseable one.
func TestPruneIsDisabledByACorruptIndex(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("AGY_PRO_HOME", home)
	keysRoot := filepath.Join(home, "ledger-keys")
	if err := os.MkdirAll(keysRoot, 0o700); err != nil {
		t.Fatalf("mkdir keys root: %v", err)
	}

	// A store well past the retention window, with EVERY key file present, so a prune
	// that ran on a "no records" reading would delete all of them.
	total := ledgerKeyRetention + 10
	var ids []string
	for i := 0; i < total; i++ {
		id := "dead-" + itoa3(i)
		ids = append(ids, id)
		if werr := os.WriteFile(filepath.Join(keysRoot, id), make([]byte, ledgerKeyBytes), 0o600); werr != nil {
			t.Fatalf("write key %s: %v", id, werr)
		}
	}
	corrupt := []byte("{not json at all")
	if werr := os.WriteFile(filepath.Join(keysRoot, ledgerKeysIndexName), corrupt, ledgerKeyFilePerm); werr != nil {
		t.Fatalf("corrupt the index: %v", werr)
	}

	if perr := pruneLedgerKeys(); perr == nil {
		t.Fatal("pruneLedgerKeys returned nil over a corrupt index; the documented fail-safe is gone")
	}
	for _, id := range ids {
		if _, serr := os.Stat(filepath.Join(keysRoot, id)); serr != nil {
			t.Fatalf("key %s was removed by a prune that could not read the index: %v", id, serr)
		}
	}

	// The next take replaces the index: losing records only loses prune candidates.
	if rerr := recordLedgerKey(keysRoot, "fresh-key", filepath.Join(base, "fresh-run")); rerr != nil {
		t.Fatalf("recordLedgerKey over a corrupt index: %v", rerr)
	}
	idx, ierr := readLedgerKeysIndex(keysRoot)
	if ierr != nil {
		t.Fatalf("the index is still unreadable after a take: %v", ierr)
	}
	if len(idx.Keys) != 1 || idx.Keys[0].KeyID != "fresh-key" {
		t.Fatalf("the replaced index holds %v; want exactly the fresh record", idx.Keys)
	}
}
