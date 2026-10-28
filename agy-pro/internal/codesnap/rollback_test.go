package codesnap

// Rollback pins (work item U1).
//
// What this file locks down:
//
//	R1 round trip  - modify + delete + create round-trips to the pre-run tree
//	                 byte for byte: edited files are restored, deleted files come
//	                 back, and the tree's file SET is the pre-run set again.
//	R2 archive     - a file the run created is MOVED into rollback-archive/, not
//	                 deleted, and its bytes survive there.
//	R3 idempotence - a second Rollback reports every row unchanged and archives
//	                 nothing.
//	R4 refusal     - a root mismatch, a missing manifest, a corrupt manifest and a
//	                 tampered snapshot copy each refuse with a named error BEFORE
//	                 the tree is touched.
//	R5 never delete - when the archive destination already exists, the created
//	                 file stays exactly where it is and rollback.json names it in
//	                 skipped[] with the reason.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// treeSnapshot renders every regular file under root as rel -> content, so a
// round-trip can assert the WHOLE tree (set + bytes), not just the files it
// remembered to check.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree %s: %v", root, err)
	}
	return out
}

func equalTree(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tree has %d files; want %d\n got: %v\nwant: %v", len(got), len(want), sortedKeys(got), sortedKeys(want))
	}
	for rel, wantBody := range want {
		body, ok := got[rel]
		if !ok {
			t.Errorf("missing %q after rollback", rel)
			continue
		}
		if body != wantBody {
			t.Errorf("%q = %q; want %q", rel, body, wantBody)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// rollbackFixture builds the standard round-trip scenario: a tree, a snapshot,
// and a run that edited one file, deleted another and created a third.
type rollbackFixture struct {
	root, dest string
	manifest   *Manifest
	preRun     map[string]string
	createdNew string
}

func newRollbackFixture(t *testing.T) *rollbackFixture {
	t.Helper()
	base := t.TempDir()
	f := &rollbackFixture{
		root: filepath.Join(base, "root"),
		dest: filepath.Join(base, "run"),
	}
	writeTree(t, f.root, map[string]string{
		"a.go":     "package a\n\nfunc A() {}\n",
		"b.go":     "package b\n\nfunc B() {}\n",
		"sub/c.go": "package c\n",
	})
	f.preRun = treeSnapshot(t, f.root)
	m, err := Take(f.root, []string{"*.go", "sub/*.go"}, f.dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	f.manifest = m
	// The run: edit a.go, delete b.go, create new.go (in scope).
	f.createdNew = "package new\n\nvar N = 1\n"
	writeTree(t, f.root, map[string]string{
		"a.go":   "package a\n\nfunc A() {}\n\nfunc Injected() {}\n",
		"new.go": f.createdNew,
	})
	if err := os.Remove(filepath.Join(f.root, "b.go")); err != nil {
		t.Fatalf("remove b.go: %v", err)
	}
	return f
}

func TestRollbackRoundTripAndArchive(t *testing.T) {
	f := newRollbackFixture(t)

	report, err := Rollback(f.root, f.dest)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got, want := report.Restored, []string{"a.go", "b.go"}; !equalStrings(got, want) {
		t.Errorf("restored = %v; want %v", got, want)
	}
	if got, want := report.Archived, []string{"rollback-archive/new.go"}; !equalStrings(got, want) {
		t.Errorf("archived = %v; want %v", got, want)
	}
	if got, want := report.Unchanged, []string{"sub/c.go"}; !equalStrings(got, want) {
		t.Errorf("unchanged = %v; want %v", got, want)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("skipped = %v; want none", report.Skipped)
	}
	if report.RestoredAt == "" {
		t.Error("restored_at is empty")
	}

	// R1: the whole tree is the pre-run tree again, byte for byte.
	equalTree(t, treeSnapshot(t, f.root), f.preRun)

	// R2: the created file is NOT deleted - it moved into the archive with its
	// bytes intact.
	archivedPath := filepath.Join(f.dest, "rollback-archive", "new.go")
	archived, aerr := os.ReadFile(archivedPath)
	if aerr != nil {
		t.Fatalf("read archived copy: %v", aerr)
	}
	if string(archived) != f.createdNew {
		t.Errorf("archived copy = %q; want %q", archived, f.createdNew)
	}
	if _, serr := os.Stat(filepath.Join(f.root, "new.go")); !os.IsNotExist(serr) {
		t.Errorf("new.go is still in the tree (stat err %v); it must be archived", serr)
	}

	// rollback.json on disk carries the same ledger.
	raw, rerr := os.ReadFile(filepath.Join(f.dest, rollbackFileName))
	if rerr != nil {
		t.Fatalf("read rollback.json: %v", rerr)
	}
	var decoded RollbackReport
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("rollback.json does not parse: %v", err)
	}
	if !equalStrings(decoded.Restored, report.Restored) || !equalStrings(decoded.Archived, report.Archived) {
		t.Errorf("rollback.json = %s; want the returned report", raw)
	}
	// The empty list is an explicit [] in the JSON, never a missing key.
	if !strings.Contains(string(raw), `"skipped": []`) {
		t.Errorf("rollback.json does not carry an empty skipped array:\n%s", raw)
	}
}

func TestRollbackIdempotent(t *testing.T) {
	f := newRollbackFixture(t)
	if _, err := Rollback(f.root, f.dest); err != nil {
		t.Fatalf("first Rollback: %v", err)
	}
	after := treeSnapshot(t, f.root)

	second, err := Rollback(f.root, f.dest)
	if err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	wantUnchanged := make([]string, 0, len(f.manifest.Files))
	for _, row := range f.manifest.Files {
		wantUnchanged = append(wantUnchanged, row.Path)
	}
	if len(second.Restored) != 0 {
		t.Errorf("second restored = %v; want none", second.Restored)
	}
	if len(second.Archived) != 0 {
		t.Errorf("second archived = %v; want none", second.Archived)
	}
	if !equalStrings(second.Unchanged, wantUnchanged) {
		t.Errorf("second unchanged = %v; want %v", second.Unchanged, wantUnchanged)
	}
	if len(second.Skipped) != 0 {
		t.Errorf("second skipped = %v; want none", second.Skipped)
	}
	equalTree(t, treeSnapshot(t, f.root), after)
}

func TestRollbackRefusesRootMismatch(t *testing.T) {
	f := newRollbackFixture(t)
	other := t.TempDir()
	writeTree(t, other, map[string]string{"a.go": "package other\n"})
	before := treeSnapshot(t, other)

	_, err := Rollback(other, f.dest)
	if !errors.Is(err, ErrRootMismatch) {
		t.Fatalf("Rollback(wrong root) error = %v; want ErrRootMismatch", err)
	}
	equalTree(t, treeSnapshot(t, other), before)
}

func TestRollbackRefusesMissingAndCorruptManifest(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	before := treeSnapshot(t, root)

	// Missing manifest.
	emptyDest := filepath.Join(base, "empty-run")
	if err := os.MkdirAll(emptyDest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := Rollback(root, emptyDest); !errors.Is(err, ErrManifestMissing) {
		t.Fatalf("Rollback(missing manifest) error = %v; want ErrManifestMissing", err)
	}

	// Corrupt JSON.
	corruptDest := filepath.Join(base, "corrupt-run")
	if err := os.MkdirAll(corruptDest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDest, manifestFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}
	if _, err := Rollback(root, corruptDest); !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Rollback(corrupt manifest) error = %v; want ErrManifestCorrupt", err)
	}

	// A manifest whose row path escapes the root (a hand-edited or hostile file).
	dest := filepath.Join(base, "run")
	if _, err := Take(root, []string{"*.go"}, dest); err != nil {
		t.Fatalf("Take: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	m.Files[0].Path = "../escape.go"
	tampered, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dest, manifestFileName), tampered, 0o600); werr != nil {
		t.Fatalf("write tampered manifest: %v", werr)
	}
	// Re-sign the tampered row so the SHAPE arm is what refuses it: since FIX-F4 an
	// un-signed tamper refuses one layer earlier, at provenance (pinned in
	// fix_f4_test.go). Both layers must hold.
	resignManifest(t, dest)
	if _, err := Rollback(root, dest); !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Rollback(escaping row) error = %v; want ErrManifestCorrupt", err)
	}
	equalTree(t, treeSnapshot(t, root), before)
}

// TestRollbackRefusesAnUnsignedTamper is the FIX-F4 companion to the escaping-row
// case above: the SAME tamper without a re-signature stops at the provenance layer,
// which is the layer a seat can actually reach.
func TestRollbackRefusesAnUnsignedTamper(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	if _, err := Take(root, []string{"*.go"}, dest); err != nil {
		t.Fatalf("Take: %v", err)
	}
	before := treeSnapshot(t, root)
	raw, err := os.ReadFile(filepath.Join(dest, manifestFileName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	m.Files[0].Path = "../escape.go"
	tampered, merr := json.MarshalIndent(m, "", "  ")
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if werr := os.WriteFile(filepath.Join(dest, manifestFileName), tampered, 0o600); werr != nil {
		t.Fatalf("write tampered manifest: %v", werr)
	}
	if _, err := Rollback(root, dest); !errors.Is(err, ErrLedgerProvenance) {
		t.Fatalf("Rollback(unsigned tamper) error = %v; want ErrLedgerProvenance", err)
	}
	equalTree(t, treeSnapshot(t, root), before)
}

func TestRollbackRefusesTamperedSnapshotCopy(t *testing.T) {
	f := newRollbackFixture(t)
	// Tamper with the captured bytes of the file the run modified.
	copyPath := filepath.Join(f.manifest.SnapshotDir, "a.go")
	if err := os.WriteFile(copyPath, []byte("package a\n// tampered\n"), 0o600); err != nil {
		t.Fatalf("tamper snapshot copy: %v", err)
	}
	before := treeSnapshot(t, f.root)

	_, err := Rollback(f.root, f.dest)
	if !errors.Is(err, ErrRollbackRefused) {
		t.Fatalf("Rollback(tampered copy) error = %v; want ErrRollbackRefused", err)
	}
	if !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Rollback(tampered copy) error = %v; want the underlying ErrManifestCorrupt", err)
	}
	// Fail closed means untouched: the preflight runs before any write.
	equalTree(t, treeSnapshot(t, f.root), before)
}

func TestRollbackNeverDeletesWhenArchiveTargetExists(t *testing.T) {
	f := newRollbackFixture(t)
	blocked := filepath.Join(f.dest, "rollback-archive", "new.go")
	if err := os.MkdirAll(filepath.Dir(blocked), 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(blocked, []byte("pre-existing archive bytes\n"), 0o600); err != nil {
		t.Fatalf("write archive placeholder: %v", err)
	}

	report, err := Rollback(f.root, f.dest)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(report.Archived) != 0 {
		t.Fatalf("archived = %v; want none (the destination was occupied)", report.Archived)
	}
	var skip *SkippedFile
	for i := range report.Skipped {
		if report.Skipped[i].Path == "new.go" {
			skip = &report.Skipped[i]
		}
	}
	if skip == nil {
		t.Fatalf("skipped = %v; want a named new.go skip", report.Skipped)
	}
	if !strings.Contains(skip.Reason, "already exists") {
		t.Errorf("skip reason = %q; want it to name the existing archive target", skip.Reason)
	}
	// R5: the created file is still in the tree, untouched, and the placeholder
	// archive bytes were not overwritten.
	got, rerr := os.ReadFile(filepath.Join(f.root, "new.go"))
	if rerr != nil {
		t.Fatalf("new.go must stay on disk (nothing is deleted): %v", rerr)
	}
	if string(got) != f.createdNew {
		t.Errorf("new.go = %q; want %q", got, f.createdNew)
	}
	placeholder, perr := os.ReadFile(blocked)
	if perr != nil {
		t.Fatalf("read placeholder: %v", perr)
	}
	if string(placeholder) != "pre-existing archive bytes\n" {
		t.Errorf("archive placeholder = %q; want it untouched", placeholder)
	}
	// The edited/deleted files are still restored in the same pass.
	if got, want := report.Restored, []string{"a.go", "b.go"}; !equalStrings(got, want) {
		t.Errorf("restored = %v; want %v", got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
