package codesnap

// Diff pins (work item U1).
//
// What this file locks down:
//
//	D1 statuses  - a live tree is classified against the manifest alone: an
//	               edited file is "modified", a vanished row is "deleted", a
//	               scoped file the manifest never saw is "created" (New=true).
//	D2 counts    - the line heuristic is exact on small files (common prefix and
//	               suffix trimmed, the changed middle counted) and a pure
//	               function of the two byte strings, so the same pair always
//	               reports the same numbers.
//	D3 renderer  - one line per file "A|M|D path +N -N", bounded by maxRows with
//	               a trailing "... K more"; empty input renders "".
//	D4 fail closed - a nil manifest, a root mismatch and a missing snapshot copy
//	               each refuse with a named error instead of a guessed delta.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffStatuses(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"a.go": "package a\n\nfunc A() {}\n",
		"b.go": "package b\n\nfunc B() {}\n",
		"c.go": "package c\n",
	})

	m, err := Take(root, []string{"*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	// The run: edit a.go, delete b.go, create d.go. c.go is untouched.
	writeTree(t, root, map[string]string{
		"a.go": "package a\n\nfunc A() {}\n\nfunc B() {}\n",
		"d.go": "package d\n\nvar X = 1\n",
	})
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatalf("remove b.go: %v", err)
	}

	deltas, err := Diff(root, m)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	want := []FileDelta{
		{Path: "a.go", Status: StatusModified, Added: 2, Removed: 0},
		{Path: "b.go", Status: StatusDeleted, Added: 0, Removed: 3},
		{Path: "d.go", Status: StatusCreated, Added: 3, Removed: 0, New: true},
	}
	if len(deltas) != len(want) {
		t.Fatalf("deltas = %+v; want %+v", deltas, want)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("deltas[%d] = %+v; want %+v", i, deltas[i], want[i])
		}
	}

	// An unchanged tree reports nothing at all (not even empty rows).
	if err := os.Remove(filepath.Join(root, "d.go")); err != nil {
		t.Fatalf("remove d.go: %v", err)
	}
	writeTree(t, root, map[string]string{
		"a.go": "package a\n\nfunc A() {}\n",
		"b.go": "package b\n\nfunc B() {}\n",
	})
	clean, err := Diff(root, m)
	if err != nil {
		t.Fatalf("Diff after restore: %v", err)
	}
	if len(clean) != 0 {
		t.Fatalf("deltas = %+v; want none", clean)
	}
}

func TestRenderDeltasBounded(t *testing.T) {
	deltas := []FileDelta{
		{Path: "src/a.go", Status: StatusCreated, Added: 3, New: true},
		{Path: "src/b.go", Status: StatusModified, Added: 2, Removed: 1},
		{Path: "src/c.go", Status: StatusDeleted, Removed: 4},
	}
	got := RenderDeltas(deltas, 2)
	want := "A src/a.go +3 -0\nM src/b.go +2 -1\n... 1 more"
	if got != want {
		t.Fatalf("RenderDeltas = %q; want %q", got, want)
	}

	// The default bound (maxRows <= 0) renders every row for a short list.
	full := RenderDeltas(deltas, 0)
	if strings.Contains(full, "more") {
		t.Fatalf("default bound truncated a 3-row list: %q", full)
	}
	if lines := strings.Split(full, "\n"); len(lines) != 3 {
		t.Fatalf("default bound rendered %d lines; want 3 (%q)", len(lines), full)
	}
	if got := RenderDeltas(nil, 5); got != "" {
		t.Fatalf("RenderDeltas(nil) = %q; want \"\"", got)
	}
}

func TestLineDelta(t *testing.T) {
	cases := []struct {
		name           string
		old, new       string
		added, removed int
	}{
		{"identical", "a\nb\n", "a\nb\n", 0, 0},
		{"appended lines", "a\nb\n", "a\nb\nc\nd\n", 2, 0},
		{"prepended lines", "b\nc\n", "a\nb\nc\n", 1, 0},
		{"one line replaced", "a\nb\nc\n", "a\nX\nc\n", 1, 1},
		{"middle block replaced", "a\nb\nc\nd\ne\n", "a\nX\nY\ne\n", 2, 3},
		{"everything replaced", "a\nb\n", "X\nY\n", 2, 2},
		{"no trailing newline equal", "a\nb", "a\nb", 0, 0},
		{"no trailing newline changed", "a\nb", "a\nc", 1, 1},
		{"empty to content", "", "a\nb\n", 2, 0},
		{"content to empty", "a\nb\n", "", 0, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, removed := lineDelta([]byte(tc.old), []byte(tc.new))
			if added != tc.added || removed != tc.removed {
				t.Fatalf("lineDelta(%q, %q) = +%d -%d; want +%d -%d", tc.old, tc.new, added, removed, tc.added, tc.removed)
			}
			// Determinism: the same pair always reports the same numbers.
			again, removedAgain := lineDelta([]byte(tc.old), []byte(tc.new))
			if again != added || removedAgain != removed {
				t.Fatalf("lineDelta is not deterministic for %q -> %q", tc.old, tc.new)
			}
		})
	}
}

func TestDiffRefusesBadInput(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	other := filepath.Join(base, "other")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	writeTree(t, other, map[string]string{"a.go": "package a\n"})

	m, err := Take(root, []string{"*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	if _, err := Diff(root, nil); !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Diff(nil manifest) error = %v; want ErrManifestCorrupt", err)
	}
	if _, err := Diff(other, m); !errors.Is(err, ErrRootMismatch) {
		t.Fatalf("Diff(wrong root) error = %v; want ErrRootMismatch", err)
	}

	// A missing snapshot copy is a named refusal: the removed-line count would be
	// a guess without it.
	copyPath := filepath.Join(m.SnapshotDir, "a.go")
	if err := os.Remove(copyPath); err != nil {
		t.Fatalf("remove snapshot copy: %v", err)
	}
	writeTree(t, root, map[string]string{"a.go": "package a\n// edit\n"})
	if _, err := Diff(root, m); !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Diff(missing snapshot copy) error = %v; want ErrManifestCorrupt", err)
	}
}
