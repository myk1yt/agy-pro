package codesnap

// FIX-F2 P-4: the archive restraint spares a CASE-ALIAS spelling of a recorded row,
// and nothing else.
//
// Before F2 the restraint was an unconditional os.SameFile scan of every recorded
// row for every claim, so a run-created HARDLINK of a recorded file - a different
// name, not another spelling of the same name - was left in place and reported with a
// reason that called it "another spelling". The containment argument for sparing a
// case alias is that it names the very file the manifest records; that argument does
// not transfer to a hardlink, whose directory entry can be moved without touching the
// recorded path or its bytes.
//
// The pins below are the two halves of that sentence: the hardlink IS archived, and
// the case alias is still spared (the second half lives in fix_d1b_test.go's
// TestRollbackKeepsCaseVariantRecordedRows; it is re-asserted here so a future edit
// cannot satisfy one half by breaking the other).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRollbackArchivesARunCreatedHardlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})

	m, err := Take(root, []string{"src/*"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	// The run edits the recorded file and then creates a hardlink to it under a name
	// that is not a case alias of anything the manifest records.
	edited := "package alpha\n\n// seat edit\n"
	writeTree(t, root, map[string]string{"src/alpha.go": edited})
	link := filepath.Join(root, "src", "beta.txt")
	if lerr := os.Link(filepath.Join(root, "src", "alpha.go"), link); lerr != nil {
		t.Skipf("this filesystem cannot create a hardlink (%v); the P-4 arm is UNVERIFIED here", lerr)
	}
	before, serr := os.Stat(link)
	if serr != nil {
		t.Fatalf("stat the hardlink: %v", serr)
	}
	recorded, rerr := os.Stat(filepath.Join(root, "src", "alpha.go"))
	if rerr != nil {
		t.Fatalf("stat the recorded file: %v", rerr)
	}
	if !os.SameFile(before, recorded) {
		t.Fatalf("the fixture is not a hardlink (the two stats are different files); the pin would be vacuous")
	}

	report, err := Rollback(root, dest)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// The hardlink's own directory entry is a created file: it must be MOVED, and the
	// recorded path must be untouched by that move.
	if !containsString(report.Archived, "rollback-archive/src/beta.txt") {
		t.Fatalf("archived = %v; want rollback-archive/src/beta.txt (a hardlink under another NAME is not the recorded file's spelling)", report.Archived)
	}
	if _, serr := os.Stat(link); !os.IsNotExist(serr) {
		t.Errorf("the hardlink is still in the tree (stat err %v); it must be archived", serr)
	}
	if got := readDisk(t, root, "src/alpha.go"); got != "package alpha\n" {
		t.Errorf("src/alpha.go = %q; want the recorded bytes restored", got)
	}
	if got := readDisk(t, dest, "rollback-archive/src/beta.txt"); got != edited {
		t.Errorf("the archived copy = %q; want the bytes the hardlink held", got)
	}
	// And the skip list must not name it as a spelling variant.
	for _, skip := range report.Skipped {
		if skip.Path == "src/beta.txt" {
			t.Errorf("rollback.json skips the hardlink with reason %q; a created hardlink is archived, not spared", skip.Reason)
		}
	}
	if len(m.Files) == 0 {
		t.Fatalf("the manifest recorded nothing; the pin would be vacuous")
	}
}

// TestRestraintOnlySparatesCaseAliases pins the index's own shape, without a
// filesystem: only a row whose ASCII-folded path equals the claim's is a candidate.
func TestRestraintOnlySparatesCaseAliases(t *testing.T) {
	ix := recordedFileIndex{}
	ix[asciiCaseFold("src/Alpha.go")] = recordedFile{path: "src/Alpha.go"}
	for _, c := range []struct {
		rel       string
		candidate bool
	}{
		{"src/alpha.go", true},
		{"SRC/ALPHA.GO", true},
		{"src/beta.txt", false},   // a hardlink's name: not a spelling of the row
		{"src/prınce.txt", false}, // a Unicode look-alike: a different file AND a different key
		{"src/alpha.go.bak", false},
	} {
		_, hit := ix[asciiCaseFold(c.rel)]
		if hit != c.candidate {
			t.Errorf("index lookup for %q = %v, want %v (only case aliases are candidates)", c.rel, hit, c.candidate)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
