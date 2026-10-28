package codesnap

// FIX-F4 pins (fix-list-6 rows Q1, Q2, Q3) plus the test-process guard that keeps
// the per-run key store out of an operator's real home.
//
// What this file locks down:
//
//	Q1a  a scope that could claim ANY existing run ledger under the root refuses the
//	     whole take and the whole rollback - not just a scope against this run's own
//	     output directory.
//	Q1b  every manifest Take writes carries a manifest.sig sidecar (HMAC-SHA256
//	     under a per-run key outside every root containment); a forged, hand-edited
//	     or unsigned ledger refuses before a row is believed, and a legitimate
//	     round-trip still restores.
//	Q2   a claim whose INODE is shared with a governance/credential, VCS or
//	     secret-class path of the same walk is withheld (never copied, never
//	     archived), because a name filter cannot see a hardlink.
//	Q3   the ledger claim tests measure the fold of BOTH the root and the ledger's
//	     deepest existing ancestor.
//
// The pins are all t.TempDir()-based; the key store is redirected by TestMain, so a
// `go test ./internal/codesnap/` run never writes into ~/.agy-pro.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain redirects the agy-pro state home into a temp directory for the whole
// test binary: the per-run ledger keys live under it (see sig.go), and a suite run
// must not leave key files in an operator's real home. AGY_PRO_HOME is the shipped
// override (providers.Home), not a test-only door, and a value the caller already
// set is honoured.
func TestMain(m *testing.M) {
	if os.Getenv("AGY_PRO_HOME") == "" {
		dir, err := os.MkdirTemp("", "agy-codesnap-home-")
		if err != nil {
			panic("codesnap tests: cannot create a temporary agy-pro home: " + err.Error())
		}
		_ = os.Setenv("AGY_PRO_HOME", dir)
		code := m.Run()
		_ = os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// resignManifest re-binds a hand-edited manifest.json to the directory's own key.
//
// The pins that tamper a manifest to exercise the ROW policy, the resolved-chain
// gate or the ledger claim need this: since FIX-F4 a hand-edited manifest refuses at
// the provenance layer first, so without a re-signature those arms are unreachable.
// Re-signing is exactly what a compromised key would give an attacker, which is why
// the arms are still worth pinning as defense in depth.
func resignManifest(t *testing.T, dest string) {
	t.Helper()
	destAbs, err := filepath.Abs(filepath.Clean(dest))
	if err != nil {
		t.Fatalf("resign: resolve %s: %v", dest, err)
	}
	data, rerr := os.ReadFile(filepath.Join(destAbs, manifestFileName))
	if rerr != nil {
		t.Fatalf("resign: read manifest: %v", rerr)
	}
	key, kerr := readLedgerKey(ledgerKeyIDFor(destAbs))
	if kerr != nil {
		t.Fatalf("resign: read the ledger key: %v", kerr)
	}
	sig := manifestMAC(key, data)
	if werr := os.WriteFile(filepath.Join(destAbs, manifestSigName), []byte(sig+"\n"), 0o600); werr != nil {
		t.Fatalf("resign: write the sidecar: %v", werr)
	}
}

// ---------------------------------------------------------------------------
// Q1a: a scope may not reach ANY run ledger.
// ---------------------------------------------------------------------------

// TestTakeRefusesAScopeOverAPriorRunLedger is the ladder from the live reproduction
// in coder-report-fix-f4.md STEP 0: run 1 owns reports/code/t1, run 2 is granted
// reports/code/t1/** with an output of its own, and the take must refuse - before
// the fix it accepted, and the forged ledger that followed installed attacker bytes
// through the operator's own rollback.
func TestTakeRefusesAScopeOverAPriorRunLedger(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{"src/main.go": "package main\n"})
	t1 := filepath.Join(root, "reports", "code", "t1")
	t2 := filepath.Join(root, "reports", "code", "t2")

	if _, err := Take(root, []string{"src/**"}, t1); err != nil {
		t.Fatalf("run 1 (scope src/**): %v", err)
	}
	if !isLedgerFingerprintDir(t1) {
		t.Fatalf("%s is not recognized as a ledger fingerprint; the refusal below would be vacuous", t1)
	}

	// The cross-run grant. Every spelling of the same reach: the whole prior ledger,
	// one file inside it, and a wildcard over the directory that holds it.
	for _, scope := range []string{"reports/code/t1/**", "reports/code/t1", "reports/code/t1/manifest.json"} {
		_, err := Take(root, []string{scope}, t2)
		if !errors.Is(err, ErrLedgerRefused) {
			t.Fatalf("Take(scope %q, out t2) error = %v; want ErrLedgerRefused (a prior run's ledger is not claimable)", scope, err)
		}
		if !strings.Contains(err.Error(), "existing run ledger") {
			t.Errorf("the refusal for %q does not name the prior ledger: %v", scope, err)
		}
	}
	// A wildcard over the parent is refused by the CURRENT-run rule first (it also
	// covers t1's own output), which is the older arm doing its job; assert only the
	// sentinel so the two arms are not confused.
	if _, err := Take(root, []string{"reports/code/*"}, t2); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take(scope reports/code/*) error = %v; want ErrLedgerRefused", err)
	}
	// Nothing was written: the refusal lands before the first copy.
	if _, serr := os.Stat(filepath.Join(t2, manifestFileName)); !os.IsNotExist(serr) {
		t.Errorf("the refused take still wrote %s (stat err %v)", filepath.Join(t2, manifestFileName), serr)
	}

	// The manifest written by run 1 is intact: the refusal is not achieved by
	// rewriting anything.
	if got := readDisk(t, root, "reports/code/t1/manifest.json"); !strings.Contains(got, "src/main.go") {
		t.Errorf("run 1's manifest no longer names the recorded row:\n%s", got)
	}

	// Non-vacuous controls. The same scope run 1 used is accepted into a different
	// output (the rule fires on the prior LEDGER, not on the word "reports"), and an
	// output outside the root does not switch the rule off - it is a claim question,
	// not an output-location question, so the cross-run grant is refused either way.
	if _, err := Take(root, []string{"src/**"}, t2); err != nil {
		t.Fatalf("the same scope that run 1 used is refused: %v (the rule must fire only on the prior ledger)", err)
	}
	if _, err := Take(root, []string{"reports/code/t1/**"}, filepath.Join(base, "outside")); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take(prior-ledger scope) with an out-of-root output error = %v; want ErrLedgerRefused (the claim is what matters, not where this run writes)", err)
	}
	if _, err := Take(root, []string{"src/**"}, filepath.Join(base, "outside")); err != nil {
		t.Fatalf("Take(scope clear of every ledger) with an out-of-root output refused: %v", err)
	}
}

// TestLedgerFingerprintNeedsBothHalves pins the fingerprint's SHAPE rule (FIX-F5,
// W3): a directory is a run ledger only when it holds the ledger shape - a bounded
// regular manifest.json whose bytes declare this package's schema version and a
// snapshot_dir - beside a snapshot/ directory. The refusal never consults the
// signature, so a ledger whose sidecar was removed (the shape an attacker would
// prefer) still refuses.
//
// The flip is pinned too: FIX-F4's content-blind "these two names" rule classified
// an npm-style dist/manifest.json + dist/snapshot/ as a ledger and refused every
// scope over it forever (r7 CR-mimoflash F-R7-1, SEC-step F-R7-1, blind-qa r7 row 6).
// That tree is now an ordinary directory, which is what the door-level pin below
// measures end to end.
func TestLedgerFingerprintNeedsBothHalves(t *testing.T) {
	dir := t.TempDir()
	if isLedgerFingerprintDir(dir) {
		t.Fatal("an empty directory was classified as a run ledger")
	}
	// A manifest of no shape: the npm/build-tool case. Both names, neither field.
	writeTree(t, dir, map[string]string{manifestFileName: "{\"name\":\"myapp\",\"version\":\"1.0.0\"}\n"})
	if isLedgerFingerprintDir(dir) {
		t.Fatal("a dist-style manifest.json WITHOUT the ledger fields was classified as a run ledger")
	}
	if err := os.MkdirAll(filepath.Join(dir, snapshotDirName), 0o755); err != nil {
		t.Fatalf("mkdir snapshot: %v", err)
	}
	if isLedgerFingerprintDir(dir) {
		t.Fatal("manifest.json + snapshot/ without the ledger schema version was classified as a run ledger")
	}
	// Half a shape is not a shape: the version alone, then the snapshot_dir alone.
	writeTree(t, dir, map[string]string{manifestFileName: "{\"version\":1}\n"})
	if isLedgerFingerprintDir(dir) {
		t.Fatal("a manifest naming only the schema version was classified as a run ledger")
	}
	writeTree(t, dir, map[string]string{manifestFileName: "{\"snapshot_dir\":\"/tmp/x\"}\n"})
	if isLedgerFingerprintDir(dir) {
		t.Fatal("a manifest naming only a snapshot_dir was classified as a run ledger")
	}
	// The full shape, with both halves present: THIS is a ledger.
	writeTree(t, dir, map[string]string{manifestFileName: "{\"version\":1,\"snapshot_dir\":\"/tmp/x/snapshot\"}\n"})
	if !isLedgerFingerprintDir(dir) {
		t.Fatal("a directory holding a ledger-shaped manifest.json + snapshot/ was not classified as a run ledger")
	}
	// The read is bounded: a valid head buried in an oversized file is not read at
	// all, so a seat cannot make the walk read a huge file by naming two paths.
	big := "{\"version\":1,\"snapshot_dir\":\"/tmp/x/snapshot\"}\n" + strings.Repeat(" ", ledgerFingerprintMaxBytes)
	writeTree(t, dir, map[string]string{manifestFileName: big})
	if isLedgerFingerprintDir(dir) {
		t.Fatalf("a %d-byte manifest.json was read; the fingerprint bound is %d bytes", len(big), ledgerFingerprintMaxBytes)
	}
	// Garbage bytes are not a shape either.
	writeTree(t, dir, map[string]string{manifestFileName: "not json at all\n"})
	if isLedgerFingerprintDir(dir) {
		t.Fatal("a non-JSON manifest.json was classified as a run ledger")
	}
}

// TestTakeAcceptsAnNpmShapedManifestDirectory is the FLIP pin for W3, measured at the
// door: a workspace whose build output holds manifest.json next to snapshot/ (the
// two names an npm/dist-style toolchain makes, and the exact tree r7 blind-qa row 6
// recorded) used to refuse every scope over it. It now takes, and nothing about the
// cross-run rule changed for a real ledger - the pin below proves both halves.
func TestTakeAcceptsAnNpmShapedManifestDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeTree(t, root, map[string]string{
		"src/main.go":                  "package main\n",
		"myapp/dist/manifest.json":     "{\"name\":\"myapp\",\"version\":\"1.0.0\",\"files\":[]}\n",
		"myapp/dist/snapshot/keep.txt": "built output\n",
	})

	// Before the flip this refused with ErrLedgerRefused (the false fingerprint).
	dest := filepath.Join(base, "run")
	m, terr := Take(root, []string{"myapp/dist/**", "src/**"}, dest)
	if terr != nil {
		t.Fatalf("Take over an npm-shaped dist directory refused: %v (the shape-aware fingerprint accepts a manifest without the ledger fields)", terr)
	}
	claimed := map[string]bool{}
	for _, row := range m.Files {
		claimed[row.Path] = true
	}
	if !claimed["myapp/dist/manifest.json"] || !claimed["myapp/dist/snapshot/keep.txt"] {
		t.Fatalf("the npm-shaped files were not claimed as ordinary files: %v", m.Files)
	}
	if dirRel, entry, refuse := PriorLedgerClaim(root, []string{"myapp/dist/**"}); refuse {
		t.Fatalf("the pre-consent gate refused the npm-shaped directory as a ledger (%q via %q)", dirRel, entry)
	}

	// Non-vacuity: a REAL ledger under the same root still refuses, so the flip
	// narrowed the fingerprint instead of switching the rule off.
	if _, err := Take(root, []string{"src/**"}, filepath.Join(root, "reports", "code", "t1")); err != nil {
		t.Fatalf("plant a genuine ledger: %v", err)
	}
	if _, err := Take(root, []string{"reports/code/t1/**"}, filepath.Join(root, "reports", "code", "t2")); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take over a genuine ledger error = %v; want ErrLedgerRefused", err)
	}
	if _, _, refuse := PriorLedgerClaim(root, []string{"reports/code/t1/**"}); !refuse {
		t.Fatal("the pre-consent gate no longer refuses a genuine ledger")
	}
}

// ---------------------------------------------------------------------------
// Q1b: provenance binding.
// ---------------------------------------------------------------------------

// TestTakeBindsTheManifestToItsRun is the positive half: Take writes a sidecar, the
// key lives in the state home (outside the root), and the ledger it wrote restores.
func TestTakeBindsTheManifestToItsRun(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpharaw\n"})

	if _, err := Take(root, []string{"src/*.go"}, dest); err != nil {
		t.Fatalf("Take: %v", err)
	}
	sigPath := filepath.Join(dest, manifestSigName)
	sig, serr := os.ReadFile(sigPath)
	if serr != nil {
		t.Fatalf("Take did not write %s: %v", manifestSigName, serr)
	}
	if got := len(strings.TrimSpace(string(sig))); got != 64 {
		t.Errorf("the sidecar holds %d hex characters; want a 64-character HMAC-SHA256", got)
	}
	keyRoot, kerr := ledgerKeysRoot()
	if kerr != nil {
		t.Fatalf("resolve the key store: %v", kerr)
	}
	if rel, inside := rootRelativeDir(root, keyRoot); inside {
		t.Fatalf("the key store %s is inside the root (relative %q): a scoped seat could read it", keyRoot, rel)
	}
	if _, kerr := os.Stat(filepath.Join(keyRoot, ledgerKeyIDFor(dest))); kerr != nil {
		t.Fatalf("the per-run key was not stored: %v", kerr)
	}

	// The legitimate round-trip still works with the sidecar present.
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n\n// seat edit\n"})
	report, rerr := Rollback(root, dest)
	if rerr != nil {
		t.Fatalf("Rollback over a signed ledger: %v", rerr)
	}
	if got, want := report.Restored, []string{"src/alpha.go"}; !equalStrings(got, want) {
		t.Fatalf("restored = %v; want %v", got, want)
	}
	if got := readDisk(t, root, "src/alpha.go"); got != "package alpharaw\n" {
		t.Errorf("src/alpha.go = %q; want the recorded bytes", got)
	}

	// Idempotence: a second rollback over the same signed ledger still loads.
	if _, err := Rollback(root, dest); err != nil {
		t.Fatalf("the second Rollback over the same ledger refused: %v", err)
	}
}

// TestRollbackRefusesAForgedOrUnsignedLedger is the Q1b chain, sealed: the exact
// forgery the STEP 0 harness performed is now refused, and so is every shape of
// "no provenance".
func TestRollbackRefusesAForgedOrUnsignedLedger(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	dest := filepath.Join(base, "run")
	original := "package main\n\nfunc main() { println(\"original\") }\n"
	attacker := "package main\n\n// attacker payload\nfunc main() { println(\"pwned\") }\n"
	writeTree(t, root, map[string]string{"src/main.go": original})

	if _, err := Take(root, []string{"src/**"}, dest); err != nil {
		t.Fatalf("Take: %v", err)
	}
	signed, rerr := os.ReadFile(filepath.Join(dest, manifestFileName))
	if rerr != nil {
		t.Fatalf("read manifest: %v", rerr)
	}

	// The forgery: rows that pass every path policy, digests that match the bytes it
	// plants, and the snapshot copy rewritten to the same bytes. Only the signature
	// stands between this and the tree.
	forge := func(t *testing.T) {
		t.Helper()
		m, merr := LoadManifest(dest)
		if merr != nil {
			t.Fatalf("load the genuine manifest: %v", merr)
		}
		forged := *m
		forged.Files = []ManifestRow{{
			Path: "src/main.go", Size: int64(len(attacker)), SHA256: digest(attacker), Mode: 0o644, Existed: true,
		}}
		data, jerr := marshalManifestJSON(&forged)
		if jerr != nil {
			t.Fatalf("marshal the forgery: %v", jerr)
		}
		if werr := os.WriteFile(filepath.Join(dest, manifestFileName), data, 0o600); werr != nil {
			t.Fatalf("write the forgery: %v", werr)
		}
		if werr := os.WriteFile(filepath.Join(m.SnapshotDir, "src", "main.go"), []byte(attacker), 0o600); werr != nil {
			t.Fatalf("write the forged snapshot copy: %v", werr)
		}
	}
	restoreOriginal := func(t *testing.T) {
		t.Helper()
		if werr := os.WriteFile(filepath.Join(dest, manifestFileName), signed, 0o600); werr != nil {
			t.Fatalf("restore the genuine manifest: %v", werr)
		}
		resignManifest(t, dest)
	}

	t.Run("forged rows, genuine sidecar", func(t *testing.T) {
		forge(t)
		_, err := Rollback(root, dest)
		if !errors.Is(err, ErrLedgerProvenance) {
			t.Fatalf("Rollback(forged manifest) error = %v; want ErrLedgerProvenance", err)
		}
		if got := readDisk(t, root, "src/main.go"); got != original {
			t.Errorf("src/main.go = %q; the forged bytes landed in the tree", got)
		}
		restoreOriginal(t)
	})

	t.Run("genuine manifest, tampered sidecar", func(t *testing.T) {
		if werr := os.WriteFile(filepath.Join(dest, manifestSigName), []byte(strings.Repeat("0", 64)+"\n"), 0o600); werr != nil {
			t.Fatalf("tamper the sidecar: %v", werr)
		}
		if _, err := Rollback(root, dest); !errors.Is(err, ErrLedgerProvenance) {
			t.Fatalf("Rollback(tampered sidecar) error = %v; want ErrLedgerProvenance", err)
		}
		resignManifest(t, dest)
	})

	t.Run("unsigned ledger (pre-provenance format)", func(t *testing.T) {
		if rerr := os.Remove(filepath.Join(dest, manifestSigName)); rerr != nil {
			t.Fatalf("remove the sidecar: %v", rerr)
		}
		_, err := Rollback(root, dest)
		if !errors.Is(err, ErrLedgerProvenance) {
			t.Fatalf("Rollback(no sidecar) error = %v; want ErrLedgerProvenance", err)
		}
		if !strings.Contains(err.Error(), "unsigned ledger (pre-provenance format)") {
			t.Errorf("the refusal does not name the unsigned class: %v", err)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		destAbs, aerr := filepath.Abs(dest)
		if aerr != nil {
			t.Fatalf("abs: %v", aerr)
		}
		keyRoot, kerr := ledgerKeysRoot()
		if kerr != nil {
			t.Fatalf("key store: %v", kerr)
		}
		// Re-create the sidecar for the genuine bytes, then take the key away: a
		// ledger whose key file is gone cannot be verified, whatever else is intact.
		resignManifest(t, dest)
		if rerr := os.Remove(filepath.Join(keyRoot, ledgerKeyIDFor(destAbs))); rerr != nil {
			t.Fatalf("remove the key: %v", rerr)
		}
		_, err := Rollback(root, dest)
		if !errors.Is(err, ErrLedgerProvenance) {
			t.Fatalf("Rollback(no key) error = %v; want ErrLedgerProvenance", err)
		}
		if !strings.Contains(err.Error(), "no key") {
			t.Errorf("the refusal does not name the missing key: %v", err)
		}
	})
}

// TestRollbackAcceptsADifferentlySpelledRunDir pins the reason the key id lives
// INSIDE the signed manifest: the operator's --run spelling must not decide which key
// is consulted. On a case-folding volume the SAME directory can be named in another
// ASCII case, and a key id re-derived from that spelling would refuse a ledger whose
// signature is perfectly good.
func TestRollbackAcceptsADifferentlySpelledRunDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	dest := filepath.Join(base, "run-dir")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})
	if !policyFor(base).foldCase {
		if _, err := Take(root, []string{"src/*.go"}, dest); err != nil {
			t.Fatalf("Take: %v", err)
		}
		if _, err := Rollback(root, dest); err != nil {
			t.Fatalf("Rollback over the same spelling: %v", err)
		}
		t.Skip("this host does not fold case, so there is no second spelling of the same directory to test")
	}
	if _, err := Take(root, []string{"src/*.go"}, dest); err != nil {
		t.Fatalf("Take: %v", err)
	}
	spelled := filepath.Join(base, strings.ToUpper("run-dir"))
	if spelled == dest {
		t.Fatalf("the case flip produced no change (%q); the pin would be vacuous", spelled)
	}
	// The different spelling opens the same directory, so the ledger must verify and
	// the restore must run.
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n\n// seat edit\n"})
	report, err := Rollback(root, spelled)
	if err != nil {
		t.Fatalf("Rollback(%q) refused a genuine ledger because of its spelling: %v", spelled, err)
	}
	if got, want := report.Restored, []string{"src/alpha.go"}; !equalStrings(got, want) {
		t.Errorf("restored = %v; want %v", got, want)
	}
	if got := readDisk(t, root, "src/alpha.go"); got != "package alpha\n" {
		t.Errorf("src/alpha.go = %q; want the recorded bytes", got)
	}
}

// TestManifestStaysByteDeterministicUnderSigning guards the reason the key ID is
// derived instead of random: the determinism pin in snapshot_test.go compares two
// takes of one tree, and a random value in the manifest would break it.
func TestManifestStaysByteDeterministicUnderSigning(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n", "src/beta.go": "package beta\n"})

	if _, err := Take(root, []string{"src/*.go"}, dest); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("read first manifest: %v", err)
	}
	if _, err := Take(root, []string{"src/*.go"}, dest); err != nil {
		t.Fatalf("second Take: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("read second manifest: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("the signed manifest is not byte-deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// The second take re-keys the ledger, so the pair on disk verifies.
	if _, err := LoadManifest(dest); err != nil {
		t.Fatalf("the re-keyed ledger does not load: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Q2: a hardlink alias of a denylisted file is withheld.
// ---------------------------------------------------------------------------

// TestTakeWithholdsAHardlinkAliasOfASecretOrGovernanceFile is the F-R6-2 pin: a
// benign-named hardlink to .env (secret class) or AGENTS.md (governance) is the same
// bytes under a neutral name, so the snapshot must not copy it and the rollback must
// not archive it.
func TestTakeWithholdsAHardlinkAliasOfASecretOrGovernanceFile(t *testing.T) {
	for _, target := range []string{".env", "AGENTS.md"} {
		t.Run(target, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "ws")
			dest := filepath.Join(base, "run")
			writeTree(t, root, map[string]string{
				"src/alpha.go": "package alpha\n",
				target:         "SECRET-OR-GOVERNED=1\n",
			})
			link := filepath.Join(root, "src", "notes.txt")
			if lerr := os.Link(filepath.Join(root, filepath.FromSlash(target)), link); lerr != nil {
				t.Skipf("this filesystem cannot create a hardlink (%v); the Q2 arm is UNVERIFIED here", lerr)
			}
			before, serr := os.Stat(link)
			if serr != nil {
				t.Fatalf("stat the hardlink: %v", serr)
			}
			recorded, rerr := os.Stat(filepath.Join(root, filepath.FromSlash(target)))
			if rerr != nil {
				t.Fatalf("stat the denylisted file: %v", rerr)
			}
			if !os.SameFile(before, recorded) {
				t.Fatalf("the fixture is not a hardlink (the two stats are different files); the pin would be vacuous")
			}

			m, terr := Take(root, []string{"src/**"}, dest)
			if terr != nil {
				t.Fatalf("Take: %v", terr)
			}
			// (1) The alias is a skipped row, never a files row.
			for _, row := range m.Files {
				if row.Path == "src/notes.txt" {
					t.Fatalf("the manifest CAPTURED the hardlink alias as a file row; the bytes of %s are now in the ledger", target)
				}
			}
			found := false
			for _, skip := range m.Skipped {
				if skip.Path == "src/notes.txt" {
					found = true
					if !strings.Contains(skip.Reason, ReasonHardlinkAlias) {
						t.Errorf("the skip reason for the alias is %q; want it to name %q", skip.Reason, ReasonHardlinkAlias)
					}
					if !strings.Contains(skip.Reason, target) {
						t.Errorf("the skip reason %q does not name the denylisted path it aliases (%s)", skip.Reason, target)
					}
				}
			}
			if !found {
				t.Fatalf("the manifest does not record the withheld alias (skipped = %+v)", m.Skipped)
			}
			// (2) No byte of it reached the ledger.
			if _, serr := os.Stat(filepath.Join(m.SnapshotDir, "src", "notes.txt")); !os.IsNotExist(serr) {
				t.Fatalf("the snapshot copied the alias (stat err %v); secret/governance bytes are in the run directory", serr)
			}
			// (3) Rollback neither restores nor archives it, and the denylisted file
			// keeps its own entry.
			report, rberr := Rollback(root, dest)
			if rberr != nil {
				t.Fatalf("Rollback: %v", rberr)
			}
			for _, a := range report.Archived {
				if strings.Contains(a, "notes.txt") {
					t.Errorf("rollback ARCHIVED the alias as %q; a hardlink to a denylisted file is left in place", a)
				}
			}
			if _, serr := os.Stat(link); serr != nil {
				t.Errorf("the alias was moved out of the tree (stat err %v); nothing is deleted or archived for a withheld claim", serr)
			}
			if got := readDisk(t, root, target); got != "SECRET-OR-GOVERNED=1\n" {
				t.Errorf("%s = %q; the rollback touched the denylisted file", target, got)
			}
		})
	}
}

// TestHardlinkArmLeavesAnOrdinaryClaimAlone is the Q2 control: the inode arm must
// not withhold a plain file, and it must not turn the pre-F4 hardlink-archived rule
// (finding P-4) into a spare.
func TestHardlinkArmLeavesAnOrdinaryClaimAlone(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n", ".env": "X=1\n"})
	m, err := Take(root, []string{"src/**"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got, want := rowPaths(m), []string{"src/alpha.go"}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v", got, want)
	}
	for _, skip := range m.Skipped {
		if strings.Contains(skip.Reason, ReasonHardlinkAlias) {
			t.Errorf("an ordinary claim was withheld as a hardlink alias: %+v", skip)
		}
	}
}

// ---------------------------------------------------------------------------
// Q3: the fold probe is measured at the ledger's deepest existing ancestor.
// ---------------------------------------------------------------------------

// TestDeepestExistingDirWalksUpToTheLedgerParent pins the helper the ledger probe
// stands on: a not-yet-existing run directory resolves to the nearest existing
// ancestor, which is the directory whose entries the ledger will occupy.
func TestDeepestExistingDirWalksUpToTheLedgerParent(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "ws", "reports", "code")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, c := range []struct{ in, want string }{
		{filepath.Join(existing, "ts-1"), existing},
		{filepath.Join(existing, "ts-1", "nested", "deeper"), existing},
		{existing, existing},
	} {
		if got := deepestExistingDir(c.in); got != c.want {
			t.Errorf("deepestExistingDir(%q) = %q; want %q", c.in, got, c.want)
		}
	}
	if got := deepestExistingDir(""); got != "" {
		t.Errorf("deepestExistingDir(\"\") = %q; want \"\"", got)
	}
}

// TestFoldArmVerdictsWithTheFoldForcedBothWays is the Q4 companion of the two
// `fold:`/`foldDependent:` flags added to internal/codework's tables.
//
// Those five rows assert a FOLDED-literal verdict, so their expectation depends on
// the host volume: on a case-sensitive CI host the fold arm never runs and both
// spellings stay apart, which is why the flags make the rows assert
// `want && hostFoldsCase`. This pin proves the same thing at the unit level, where
// the fold policy is an argument instead of a measurement, so a maintainer reading a
// red row on Linux can see the intended both-ways answer in one place.
func TestFoldArmVerdictsWithTheFoldForcedBothWays(t *testing.T) {
	rows := []struct{ pattern, target string }{
		{"[!]aA", "!aa"},       // the codework r5 witness rows
		{"[!]Aa", "!AA"},       //
		{"[!]sS", "!ss"},       //
		{"[!]aA", "!aa"},       // the parity-table spellings (same pairs)
		{"[!]Aa", "!AA"},       //
		{"RePoRtS", "reports"}, // a plain case variant, for contrast
	}
	for _, row := range rows {
		if !segmentMayMatch(row.pattern, row.target, true) {
			t.Errorf("segmentMayMatch(%q, %q, fold=true) = false; the folded arm must reach it", row.pattern, row.target)
		}
	}
	// The rows whose verdict must NOT be fold-independent: with the fold off, the
	// class-preserving arm is not consulted at all, so only a byte-exact pattern can
	// match. This is what a case-sensitive CI host sees, and the reason the codework
	// rows carry the flag.
	for _, row := range []struct{ pattern, target string }{
		{"[!]aA", "!aa"},
		{"[!]Aa", "!AA"},
		{"[!]sS", "!ss"},
		{"RePoRtS", "reports"},
	} {
		if segmentMayMatch(row.pattern, row.target, false) {
			t.Errorf("segmentMayMatch(%q, %q, fold=false) = true; with the fold off only a byte-exact component matches, so the row would be a false expectation on a case-sensitive host", row.pattern, row.target)
		}
	}
	// And the lexical arm is fold-independent in the direction that matters: a
	// pattern the authored bytes match answers true with either policy.
	if !segmentMayMatch("[!]aA", "!aA", false) || !segmentMayMatch("[!]aA", "!aA", true) {
		t.Errorf("the lexical arm must answer true with either fold policy")
	}
}

// TestLedgerPolicyMeasuresTheLedgerSubtree pins WHERE the ledger claim's fold comes
// from, through the probe cache the package already keeps:
//
//   - a case-SENSITIVE root: the root probe answers fold=false, so
//     ledgerPolicyFor must go on to measure the ledger's deepest existing ancestor
//     (its cache key appears). That is the arm SEC-charmglm's F-01 / Q3 needs: a
//     fold boundary BELOW a byte-exact root would otherwise be invisible.
//   - a case-FOLDING root (this Windows deployment): the first answer already folds,
//     so the ancestor probe is skipped and only the root's key is cached - the
//     Q3 arm costs nothing where the volume folds uniformly.
//
// Both directions are measured against the host's own filesystem answer, so the pin
// is honest on either CI host instead of asserting one platform's behavior.
func TestLedgerPolicyMeasuresTheLedgerSubtree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	parent := filepath.Join(root, "reports", "code")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dest := filepath.Join(parent, "ts-1") // does not exist yet, as on a first take

	rootFolds, conclusive := probeCaseFolding(root)
	if !conclusive {
		t.Skipf("the case-fold probe is inconclusive on %s (this host cannot answer); the placement pin needs a conclusive root probe", root)
	}

	policyMu.Lock()
	policyCache = map[string]namePolicy{}
	policyMu.Unlock()

	p := ledgerPolicyFor(root, dest)

	policyMu.Lock()
	_, rootCached := policyCache[policyKey(root)]
	_, parentCached := policyCache[policyKey(parent)]
	policyMu.Unlock()

	if rootFolds {
		if !p.foldCase {
			t.Fatalf("the root folds case but the ledger policy does not; the claim test would be laxer than the volume")
		}
		if parentCached {
			t.Errorf("the ancestor %s was probed although the root already answered fold=true (a needless probe inside the workspace)", parent)
		}
		if !rootCached {
			t.Errorf("the root's conclusive answer was not cached")
		}
		return
	}
	if !rootCached {
		t.Errorf("the root's conclusive answer was not cached")
	}
	if !parentCached {
		t.Fatalf("on a case-sensitive root the ledger's ancestor %s was never measured; a fold boundary below the root would be invisible (SEC-charmglm F-01 / Q3)", parent)
	}
}
