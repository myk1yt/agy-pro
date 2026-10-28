package codesnap

// Snapshot pins (work item U1, `agy pro code` write door).
//
// What this file locks down:
//
//	S1 claims      - a scope claims exactly the files PathInScope grants, and a
//	                 symlink alias of an in-scope file is claimed ONCE, under its
//	                 real path (the dual-view rule of merge.go:336-401).
//	S2 determinism - two Takes over the same tree into the same destination write
//	                 byte-identical manifest.json (sorted rows, forward slashes,
//	                 no timestamp).
//	S3 scope gate  - empty, root-wide and root-wide-equivalent scope entries are
//	                 refused before anything is written (fanout units.go:107-155
//	                 semantics), as are absolute and "..."-escaping entries.
//	S4 fail closed - a claimed file that cannot be read or that resolves OUTSIDE
//	                 the root refuses the whole take with a named *SnapshotError
//	                 and writes NO manifest, so no rollback can ever be derived
//	                 from a partial ledger.
//
// Every pin is behavioral: it asserts on the manifest bytes, the snapshot copies
// and the tree, never on internals.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree materializes rel-path -> body entries under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// readDisk returns the bytes of root/rel.
func readDisk(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// digest is the independently computed sha256 of body (not the package helper,
// so the manifest assertion is not tautological).
func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// resolvedRoot is the manifest's own root rendering: absolute and
// symlink-resolved.
func resolvedRoot(t *testing.T, root string) string {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs %s: %v", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("eval %s: %v", abs, err)
	}
	return resolved
}

// makeSymlink plants a symlink fixture, skipping the test when the host does not
// permit symlink creation (Windows without Developer Mode), the same guard
// agenttools/sandbox_test.go:27 uses.
func makeSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation not permitted on this host: %v", err)
	}
}

func TestTakeClaimsScopedFilesDeterministic(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"src/alpha.go":     "package alpha\n\nfunc A() {}\n",
		"src/beta.go":      "package beta\n",
		"src/sub/gamma.go": "package gamma\n",
		"docs/readme.md":   "# docs\n",
	})

	scopes := []string{"src/*.go"}
	m, err := Take(root, scopes, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	// S1: exactly the two bounded claims, sorted, forward slashes.
	wantPaths := []string{"src/alpha.go", "src/beta.go"}
	if len(m.Files) != len(wantPaths) {
		t.Fatalf("claimed %d files (%v); want %d (%v)", len(m.Files), rowPaths(m), len(wantPaths), wantPaths)
	}
	for i, want := range wantPaths {
		row := m.Files[i]
		if row.Path != want {
			t.Errorf("row[%d].Path = %q; want %q", i, row.Path, want)
		}
		body := readDisk(t, root, want)
		if row.SHA256 != digest(body) {
			t.Errorf("%s sha256 = %q; want %q", want, row.SHA256, digest(body))
		}
		if row.Size != int64(len(body)) {
			t.Errorf("%s size = %d; want %d", want, row.Size, len(body))
		}
		if !row.Existed {
			t.Errorf("%s existed = false; want true", want)
		}
		if row.Mode == 0 {
			t.Errorf("%s mode = 0; want the source permission bits", want)
		}
		// The snapshot copy is a byte copy of the source.
		copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(want))
		copied, cerr := os.ReadFile(copyPath)
		if cerr != nil {
			t.Fatalf("snapshot copy %s: %v", want, cerr)
		}
		if string(copied) != body {
			t.Errorf("snapshot copy of %s = %q; want %q", want, copied, body)
		}
	}
	// The scope set and the resolved absolute root are recorded.
	if len(m.Scopes) != 1 || m.Scopes[0] != "src/*.go" {
		t.Errorf("scopes = %v; want [src/*.go]", m.Scopes)
	}
	if m.Root != resolvedRoot(t, root) {
		t.Errorf("root = %q; want %q", m.Root, resolvedRoot(t, root))
	}
	if m.Version != ManifestVersion {
		t.Errorf("version = %d; want %d", m.Version, ManifestVersion)
	}

	// S2: the manifest on disk is byte-deterministic across runs.
	first, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	second, err := Take(root, scopes, dest)
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if second.Root != m.Root {
		t.Errorf("second root = %q; want %q", second.Root, m.Root)
	}
	again, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("re-read manifest: %v", err)
	}
	if string(first) != string(again) {
		t.Errorf("manifest.json is not byte-deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, again)
	}
	// The rows are sorted by path and hold no backslash.
	var decoded Manifest
	if err := json.Unmarshal(again, &decoded); err != nil {
		t.Fatalf("manifest.json does not parse: %v", err)
	}
	for i := 1; i < len(decoded.Files); i++ {
		if decoded.Files[i-1].Path >= decoded.Files[i].Path {
			t.Errorf("manifest rows are not sorted: %q before %q", decoded.Files[i-1].Path, decoded.Files[i].Path)
		}
	}
	for _, row := range decoded.Files {
		if strings.Contains(row.Path, `\`) {
			t.Errorf("manifest path %q uses a backslash", row.Path)
		}
	}
}

// rowPaths lists the claimed paths in manifest order (test diagnostics).
func rowPaths(m *Manifest) []string {
	out := make([]string, 0, len(m.Files))
	for _, row := range m.Files {
		out = append(out, row.Path)
	}
	return out
}

func TestTakeRejectsEmptyAndRootWideScopes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})

	rejected := []struct {
		name   string
		scopes []string
	}{
		{"nil", nil},
		{"empty slice", []string{}},
		{"blank entry", []string{""}},
		{"dot", []string{"."}},
		{"dot slash", []string{"./"}},
		{"slash", []string{"/"}},
		{"star", []string{"*"}},
		{"double star", []string{"**"}},
		{"question star", []string{"?*"}},
		{"star slash star", []string{"*/*"}},
		{"class star", []string{"[a-z]*"}},
		{"outside", []string{"../etc"}},
		{"traversal", []string{"src/../.."}},
		{"absolute posix", []string{"/etc/passwd"}},
		{"absolute windows", []string{"C:/Windows"}},
		{"bounded then root-wide", []string{"src/*.go", "*"}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "run")
			m, err := Take(root, tc.scopes, dest)
			if err == nil {
				t.Fatalf("Take(%v) accepted the scope (files: %v); want a refusal", tc.scopes, rowPaths(m))
			}
			if !errors.Is(err, ErrScopeRejected) {
				t.Fatalf("Take(%v) error = %v; want ErrScopeRejected", tc.scopes, err)
			}
			// S3: nothing is written before the scope gate passes.
			if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
				t.Fatalf("Take(%v) created %s despite refusing; want nothing written", tc.scopes, dest)
			}
		})
	}

	// The accepted side of the boundary: a bounded glob with a literal anchor.
	if _, err := Take(root, []string{"src/*.go"}, filepath.Join(base, "ok")); err != nil {
		t.Fatalf("Take(src/*.go) refused a bounded scope: %v", err)
	}
}

func TestTakeRefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	writeTree(t, outside, map[string]string{"credentials.txt": "SECRET-TOKEN\n"})
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})
	makeSymlink(t, filepath.Join(outside, "credentials.txt"), filepath.Join(root, "src", "leak.go"))

	dest := filepath.Join(base, "run")
	_, err := Take(root, []string{"src/*.go"}, dest)
	if err == nil {
		t.Fatal("Take followed an in-scope symlink out of the root; want a fail-closed refusal")
	}
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("error = %v; want ErrSnapshotIncomplete", err)
	}
	var snapErr *SnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("error %v is not a *SnapshotError (the skip list must be named)", err)
	}
	if len(snapErr.Skipped) != 1 || snapErr.Skipped[0].Path != "src/leak.go" {
		t.Fatalf("skipped = %v; want exactly src/leak.go", snapErr.Skipped)
	}
	// Fail closed: no manifest, and the outside bytes were never copied in.
	if _, serr := os.Stat(filepath.Join(dest, manifestFileName)); !os.IsNotExist(serr) {
		t.Fatalf("manifest.json was written despite the refusal (stat err %v)", serr)
	}
	err = filepath.WalkDir(dest, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "SECRET-TOKEN") {
			t.Errorf("outside-root bytes leaked into the snapshot at %s", p)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk snapshot dir: %v", err)
	}
	// The outside file is untouched (nothing is ever written out of the root).
	if got := readDisk(t, outside, "credentials.txt"); got != "SECRET-TOKEN\n" {
		t.Fatalf("outside file = %q; want it untouched", got)
	}
}

func TestTakeClaimsSymlinkAliasOnceUnderRealPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"src/alpha.go": "package alpha\n",
		"src/beta.go":  "package beta\n",
	})
	makeSymlink(t, filepath.Join(root, "src", "beta.go"), filepath.Join(root, "src", "alias.go"))

	m, err := Take(root, []string{"src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	// Dual view (merge.go:358-390): the alias resolves to the same real file, so
	// the pair is claimed ONCE, under the real path.
	wantPaths := []string{"src/alpha.go", "src/beta.go"}
	if len(m.Files) != len(wantPaths) {
		t.Fatalf("claimed %v; want %v", rowPaths(m), wantPaths)
	}
	for i, want := range wantPaths {
		if m.Files[i].Path != want {
			t.Errorf("row[%d].Path = %q; want %q", i, m.Files[i].Path, want)
		}
	}
}

func TestTakeNeverClaimsItsOwnArtifactDir(t *testing.T) {
	// The code verb keeps its run directory under the workspace (reports/code/<ts>),
	// so the ledger must never appear in its own manifest. Two arms cover it: a
	// scope that could CLAIM the subtree is refused up front
	// (TestTakeRefusesScopeCoveringRunDir), and a symlink alias inside the scope
	// whose REAL path lands in the ledger is dropped from the claim set here,
	// before any copy.
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(root, "reports", "code", "run")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})
	writeTree(t, dest, map[string]string{"snapshot/decoy.go": "package decoy\n"})
	makeSymlink(t, filepath.Join(dest, "snapshot", "decoy.go"), filepath.Join(root, "src", "alias.go"))

	scopes := []string{"src/*.go"}
	first, err := Take(root, scopes, dest)
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}
	// The alias resolves into the run directory, so it is not a claim: exactly one
	// row, and no row path at or below the ledger.
	if got, want := rowPaths(first), []string{"src/alpha.go"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("first claims = %v; want %v", got, want)
	}
	for _, row := range first.Files {
		if foldOff.pathUnder("reports/code/run", row.Path) {
			t.Errorf("manifest row %q is inside the run directory", row.Path)
		}
	}
	firstBytes, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	second, err := Take(root, scopes, dest)
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if got, want := rowPaths(second), []string{"src/alpha.go"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("second claims = %v; want %v (the artifact dir must be excluded)", got, want)
	}
	secondBytes, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("re-read manifest: %v", err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("manifest changed once the artifact dir existed:\n--- first ---\n%s\n--- second ---\n%s", firstBytes, secondBytes)
	}
}
