package profiles

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// toolsSnapshotTrees are the four repo-level content trees the integrations
// engine deploys (P5-3 T7). tools/ mirrors each of them under the same name, so
// the engine's layout can treat the snapshot root as the source tree root.
var toolsSnapshotTrees = []string{"rules", "skills", "plugins", "hooks"}

// toolsRegenEnv is the opt-in switch for regeneration. Regeneration writes, so
// it can never happen as a side effect of a plain test run.
const toolsRegenEnv = "AGY_TOOLS_SYNC_REGEN"

// toolsRepoTrees splits the four repo-level content trees into the ones this
// checkout actually has and the ones it does not, naming each missing path. A
// standalone agy-pro checkout has no sibling rules/skills/plugins/hooks, so the
// gate has nothing to compare against and must skip rather than fail: that is the
// standalone-checkout pattern the sibling-tree tests in internal/settings and
// internal/mode use (t.Skipf with the named path).
//
// The split is PER TREE on purpose (F-S9A-7): requiring all four let a repo that
// lost one tree (or a partial mirror) skip the whole gate, so a snapshot drifted
// against the surviving three passed vacuously. Only the shape with no trees at
// all skips the gate; every other shape compares the trees that exist and logs
// the ones that do not.
func toolsRepoTrees(base string) (present []string, missing []string) {
	for _, tree := range toolsSnapshotTrees {
		dir := filepath.Join(base, tree)
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			present = append(present, tree)
			continue
		}
		missing = append(missing, dir)
	}
	return present, missing
}

// hashFileTree maps every regular file below root to its SHA-256, keyed by the
// slash-separated relative path. Non-regular entries (symlinks, sockets) are
// skipped: they are not content the engine can deploy, and following them would
// make the gate filesystem- and OS-dependent.
func hashFileTree(root string) (map[string]string, error) {
	sums := make(map[string]string)
	err := filepath.WalkDir(root, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(current)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		sums[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	return sums, err
}

// hashSnapshotTree is hashFileTree over one tree of the embedded snapshot.
func hashSnapshotTree(t *testing.T, tree string) map[string]string {
	t.Helper()
	sums := make(map[string]string)
	err := fs.WalkDir(GetToolsFS(), tree, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(filepath.FromSlash(tree), filepath.FromSlash(current))
		if relErr != nil {
			return relErr
		}
		data, readErr := fs.ReadFile(GetToolsFS(), current)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		sums[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk the embedded snapshot tree %q: %v", tree, err)
	}
	return sums
}

// toolsSnapshotDrift compares one tree and returns one human-readable line per
// drifted pair: a path only the repo tree has, a path only the snapshot has, or
// a path whose bytes differ (both digests named, truncated to 12 hex chars so a
// failure message stays readable).
func toolsSnapshotDrift(t *testing.T, tree string, repo, snapshot map[string]string) []string {
	t.Helper()
	var drift []string
	for rel, repoSum := range repo {
		snapshotSum, ok := snapshot[rel]
		switch {
		case !ok:
			drift = append(drift, fmt.Sprintf("%s/%s: present in the repo tree, MISSING from the snapshot (%s)", tree, rel, repoSum[:12]))
		case snapshotSum != repoSum:
			drift = append(drift, fmt.Sprintf("%s/%s: content differs (repo %s, snapshot %s)", tree, rel, repoSum[:12], snapshotSum[:12]))
		}
	}
	for rel := range snapshot {
		if _, ok := repo[rel]; !ok {
			drift = append(drift, fmt.Sprintf("%s/%s: present in the snapshot, GONE from the repo tree", tree, rel))
		}
	}
	sort.Strings(drift)
	return drift
}

// regenerateSnapshotTree rewrites one snapshot tree so it equals the repo tree:
// every repo file is copied over, and every snapshot file whose repo source is
// gone is removed. It only runs behind toolsRegenEnv, and it only ever writes
// inside agy-pro/profiles/tools/<tree> - the derived copy, never the repo tree
// itself.
func regenerateSnapshotTree(t *testing.T, tree, repoBase string, repo map[string]string) {
	t.Helper()
	snapshotRoot := filepath.Join("tools", tree)
	repoRoot := filepath.Join(repoBase, tree)

	for rel := range repo {
		source := filepath.Join(repoRoot, filepath.FromSlash(rel))
		destination := filepath.Join(snapshotRoot, filepath.FromSlash(rel))
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("regenerate: read %q: %v", source, err)
		}
		info, err := os.Stat(source)
		if err != nil {
			t.Fatalf("regenerate: stat %q: %v", source, err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatalf("regenerate: create %q: %v", filepath.Dir(destination), err)
		}
		if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
			t.Fatalf("regenerate: write %q: %v", destination, err)
		}
	}

	existing, err := hashFileTree(snapshotRoot)
	if err != nil {
		t.Fatalf("regenerate: walk %q: %v", snapshotRoot, err)
	}
	var stale []string
	for rel := range existing {
		if _, ok := repo[rel]; !ok {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	for _, rel := range stale {
		victim := filepath.Join(snapshotRoot, filepath.FromSlash(rel))
		if err := os.Remove(victim); err != nil {
			t.Fatalf("regenerate: remove stale snapshot file %q: %v", victim, err)
		}
		t.Logf("regenerate: removed stale snapshot file %s/%s (its repo source no longer exists)", tree, rel)
	}
}

// TestToolsSnapshotMatchesRepoTrees is the P5-3 T7 sync gate: the embedded
// content snapshot the integrations engine deploys (agy-pro/profiles/tools/)
// must be a byte-for-byte copy of the repo-level rules/, skills/, plugins/ and
// hooks/ trees. The repo trees are the semantic authority; tools/ is the
// committed deploy copy. Drift in either direction fails here, naming every
// drifted pair, so the copy can never quietly become a second source of truth.
//
// The gate runs per tree over the trees this checkout actually carries (F-S9A-7):
// a standalone checkout with none of them skips, a checkout missing one of four
// compares the other three and logs the absent one instead of retiring the whole
// gate.
//
// Regeneration is opt-in and needs no script file:
//
//	cd agy-pro
//	AGY_TOOLS_SYNC_REGEN=1 go test ./profiles/ -run TestToolsSnapshotMatchesRepoTrees -count=1
func TestToolsSnapshotMatchesRepoTrees(t *testing.T) {
	repoBase := filepath.Join("..", "..")
	present, missing := toolsRepoTrees(repoBase)
	if reason := toolsGateSkipReason(present, missing); reason != "" {
		t.Skip(reason)
	}
	if len(missing) != 0 {
		t.Logf("repo trees this checkout does not carry, so not compared: %s", strings.Join(missing, ", "))
	}

	regen := os.Getenv(toolsRegenEnv) == "1"
	if regen {
		t.Logf("%s=1: rewriting agy-pro/profiles/tools/ from the repo trees", toolsRegenEnv)
	}

	for _, tree := range present {
		tree := tree
		t.Run(tree, func(t *testing.T) {
			verifySnapshotTree(t, tree, repoBase, regen)
		})
	}
}

// toolsGateSkipReason is the whole-gate skip decision, in one place so the pin
// below can state it: the gate has nothing to compare only when NO repo tree
// exists. A partial checkout (some trees present, some absent) must run.
func toolsGateSkipReason(present, missing []string) string {
	if len(present) > 0 {
		return ""
	}
	return fmt.Sprintf("no repo-level content tree at %s: a standalone agy-pro checkout ships no sibling rules/skills/plugins/hooks trees, so there is nothing to compare the snapshot against (not a product failure)",
		strings.Join(missing, ", "))
}

// verifySnapshotTree is the gate for ONE tree: hash the repo tree, optionally
// regenerate the snapshot from it, then compare the two. Split out of the test
// above so the per-tree pin can drive the comparison against a temp repo tree.
func verifySnapshotTree(t *testing.T, tree, repoBase string, regen bool) {
	t.Helper()
	repoRoot := filepath.Join(repoBase, tree)
	repo, err := hashFileTree(repoRoot)
	if err != nil {
		t.Fatalf("walk the repo tree %q: %v", tree, err)
	}
	if regen {
		regenerateSnapshotTree(t, tree, repoBase, repo)
		// The embedded copy is fixed at compile time, so a regeneration run
		// can only verify the tree it just wrote by reading it back from disk.
		repo = mustHashFileTreeAt(t, repoRoot)
		snapshot := mustHashFileTreeAt(t, filepath.Join("tools", tree))
		if drift := toolsSnapshotDrift(t, tree, repo, snapshot); len(drift) != 0 {
			t.Fatalf("regeneration did not converge for %q:\n  %s", tree, strings.Join(drift, "\n  "))
		}
		t.Logf("regenerated %s (%d files); re-run without %s to verify against the embed", tree, len(repo), toolsRegenEnv)
		return
	}

	snapshot := hashSnapshotTree(t, tree)
	if len(repo) == 0 {
		t.Fatalf("the repo tree %q has no files; the snapshot gate would be vacuous", tree)
	}
	if drift := toolsSnapshotDrift(t, tree, repo, snapshot); len(drift) != 0 {
		t.Fatalf("the content snapshot agy-pro/profiles/tools/%s/ drifted from the repo tree %s/ (%d pair(s)).\n"+
			"The repo tree is the authority: regenerate the snapshot with\n"+
			"  cd agy-pro && %s=1 go test ./profiles/ -run TestToolsSnapshotMatchesRepoTrees -count=1\n\n  %s",
			tree, tree, len(drift), toolsRegenEnv, strings.Join(drift, "\n  "))
	}
}

// TestToolsSnapshotGateComparesEveryPresentTree is the F-S9A-7 pin: one missing
// repo tree must not retire the comparison of the trees that do exist. The old
// all-or-nothing existence check skipped the WHOLE gate when any of the four was
// absent, so a snapshot drifted against the surviving trees passed vacuously in
// that shape - the drifted-snapshot hole this test keeps closed.
//
// Chosen semantics, pinned here: the gate skips per tree (a partial checkout is a
// legitimate shape, named in the test log with its path) and skips wholesale only
// when no tree exists at all. Hard-failing a partial checkout instead would fail
// the repo for its shape rather than for drift, which is the opposite of what the
// standalone-checkout fix in internal/settings and internal/mode does.
func TestToolsSnapshotGateComparesEveryPresentTree(t *testing.T) {
	base := t.TempDir()
	for _, tree := range []string{"rules", "skills", "plugins"} {
		dir := filepath.Join(base, tree)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %q: %v", dir, err)
		}
	}

	present, missing := toolsRepoTrees(base)
	if strings.Join(present, ",") != "rules,skills,plugins" {
		t.Fatalf("present = %v, want the three trees this checkout carries", present)
	}
	if len(missing) != 1 || missing[0] != filepath.Join(base, "hooks") {
		t.Fatalf("missing = %v, want exactly the absent hooks tree at %q", missing, filepath.Join(base, "hooks"))
	}
	if reason := toolsGateSkipReason(present, missing); reason != "" {
		t.Fatalf("a partial checkout skipped the whole gate: %s", reason)
	}

	// Every present tree is compared, not skipped: a file only the repo tree has
	// is drift, which is what used to go unreported when one tree was missing.
	for _, tree := range present {
		probe := filepath.Join(base, tree, "drift-probe-only.md")
		if err := os.WriteFile(probe, []byte("repo-only\n"), 0o644); err != nil {
			t.Fatalf("write probe %q: %v", probe, err)
		}
		drift := toolsSnapshotDrift(t, tree, mustHashFileTreeAt(t, filepath.Join(base, tree)), hashSnapshotTree(t, tree))
		if len(drift) == 0 {
			t.Fatalf("the gate reported no drift for %q; a partial checkout is skipping the trees it carries", tree)
		}
		if joined := strings.Join(drift, "\n"); !strings.Contains(joined, "drift-probe-only.md") {
			t.Fatalf("the gate did not name the repo-only file in %q:\n%s", tree, joined)
		}
	}

	// The whole-gate skip survives only for the standalone shape.
	standalone := t.TempDir()
	if present, missing := toolsRepoTrees(standalone); len(present) != 0 || len(missing) != len(toolsSnapshotTrees) {
		t.Fatalf("standalone split = %v present / %v missing, want 0 present and all %d trees named",
			present, missing, len(toolsSnapshotTrees))
	} else if reason := toolsGateSkipReason(present, missing); !strings.Contains(reason, "standalone agy-pro checkout") {
		t.Fatalf("the standalone shape does not take the whole-gate skip: %q", reason)
	}

	// A path that exists but is not a directory is not a comparable tree either,
	// so it must land in the missing half like the old info.IsDir check did.
	notADir := t.TempDir()
	if err := os.WriteFile(filepath.Join(notADir, "rules"), []byte("not a tree\n"), 0o644); err != nil {
		t.Fatalf("write non-directory tree: %v", err)
	}
	present, missing = toolsRepoTrees(notADir)
	if len(present) != 0 || missing[0] != filepath.Join(notADir, "rules") {
		t.Fatalf("non-directory split = %v present / %v missing, want the file-typed rules path reported missing", present, missing)
	}
}

// mustHashFileTreeAt is hashFileTree with a test-fatal error path, used by the
// regeneration branch where a read failure means the regeneration is suspect.
func mustHashFileTreeAt(t *testing.T, root string) map[string]string {
	t.Helper()
	sums, err := hashFileTree(root)
	if err != nil {
		t.Fatalf("walk %q: %v", root, err)
	}
	return sums
}

// TestToolsSnapshotIsDeployShaped pins the shape the engine's layouts depend on:
// the snapshot root exposes exactly the four feature directories, and each one
// is non-empty. A path added at the snapshot root (a stray README, a stray
// editor directory) is not deployed by any layout, but it would silently hide a
// tree that failed to regenerate, so it is called out here.
func TestToolsSnapshotIsDeployShaped(t *testing.T) {
	entries, err := fs.ReadDir(GetToolsFS(), ".")
	if err != nil {
		t.Fatalf("read the snapshot root: %v", err)
	}
	var dirs, others []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
			continue
		}
		others = append(others, entry.Name())
	}
	sort.Strings(dirs)
	sort.Strings(others)
	want := append([]string(nil), toolsSnapshotTrees...)
	sort.Strings(want)
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("snapshot root directories = %v; want exactly %v", dirs, want)
	}
	if strings.Join(others, ",") != "README.md" {
		t.Fatalf("snapshot root non-directory entries = %v; want only README.md (the committed-copy note)", others)
	}
	for _, tree := range toolsSnapshotTrees {
		files := hashSnapshotTree(t, tree)
		if len(files) == 0 {
			t.Fatalf("snapshot tree %q is empty; the engine's layout would report ErrSourceUnusable", tree)
		}
	}
	if _, err := fs.Stat(GetToolsFS(), path.Join("hooks", "hooks.json")); err != nil {
		t.Fatalf("the snapshot carries no hooks/hooks.json: %v", err)
	}
}
