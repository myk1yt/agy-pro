package integrations

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"agypro/profiles"
)

// TestEmbeddedSnapshotIsTheDeploySource is the P5-3 T7 end-to-end wiring proof:
// the snapshot the CLI hands the engine (profiles.GetToolsFS, mirrored from the
// repo trees and guarded by the profiles sync gate) deploys all four features
// into a temp config dir, is recorded in the manifest, reports clean afterwards,
// and is a no-op on a second pass. No fake fs.FS and no toolkit root are
// involved, so this is the closest a unit test gets to "the real content lands".
func TestEmbeddedSnapshotIsTheDeploySource(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := profiles.GetToolsFS()

	features := []Feature{FeatureRules, FeatureSkills, FeaturePlugins, FeatureHook}
	totalFiles := 0
	for _, feature := range features {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			layout, err := LayoutFor(feature)
			if err != nil {
				t.Fatalf("LayoutFor(%q): %v", feature, err)
			}
			entries, err := Enumerate(feature, source)
			if err != nil {
				t.Fatalf("Enumerate(%q) over the embedded snapshot: %v", feature, err)
			}
			if len(entries) == 0 {
				t.Fatalf("the embedded snapshot has no content for %q", feature)
			}
			totalFiles += len(entries)

			plan, err := Plan(feature, source)
			if err != nil {
				t.Fatalf("Plan(%q): %v", feature, err)
			}
			if got := plan.Writes(); got != len(entries) {
				t.Fatalf("plan writes %d destinations; want %d", got, len(entries))
			}
			summary := sandbox.apply(plan)
			if summary.Created != len(entries) || summary.Updated != 0 {
				t.Fatalf("apply summary = %+v; want %d created destination(s)", summary, len(entries))
			}

			// Every destination holds the snapshot's bytes, byte for byte.
			for _, entry := range entries {
				destination, destErr := layout.Destination(entry.RelPath)
				if destErr != nil {
					t.Fatalf("Destination(%q): %v", entry.RelPath, destErr)
				}
				assertFileBytes(t, destination, entry.Data)
			}

			status, err := Status(feature)
			if err != nil {
				t.Fatalf("Status(%q): %v", feature, err)
			}
			if !status.OK() || !status.Deployed {
				t.Fatalf("Status(%q) after applying the snapshot = %+v; want a clean deployment", feature, status)
			}
			if status.Foreign != 0 {
				t.Fatalf("Status(%q) reports %d foreign file(s) after a fresh snapshot deploy: %+v", feature, status.Foreign, status.Destinations)
			}

			// Second pass: identical, and no destination is written.
			again, err := Plan(feature, source)
			if err != nil {
				t.Fatalf("second Plan(%q): %v", feature, err)
			}
			if got := again.Writes(); got != 0 {
				t.Fatalf("second plan writes %d destination(s); want 0 (%v)", got, again.Lines())
			}
		})
	}
	if totalFiles < 90 {
		t.Fatalf("the embedded snapshot deploys only %d files in total; the repo trees carry ~97 (rules 3, skills 50, plugins 43, hooks 1)", totalFiles)
	}

	// The hook landed from the SNAPSHOT, not from the toolkit root the sandbox also
	// provides with different bytes.
	hookBytes, err := fs.ReadFile(source, sourceDirHooks+"/"+hookSourceFile)
	if err != nil {
		t.Fatalf("read hooks.json from the snapshot: %v", err)
	}
	assertFileBytes(t, filepath.Join(sandbox.configDir, sourceDirHooks, hookSourceFile), hookBytes)
	if string(hookBytes) == hookFixtureJSON {
		t.Fatal("the snapshot's hooks.json is the test fixture body; the embed is not the repo's hooks/hooks.json")
	}
}

// TestEmbeddedSnapshotRefusesStatusLine pins the partition on the snapshot path:
// statusLine is not a content feature, so asking the engine to lay it out is a
// refusal rather than an empty deployment.
func TestEmbeddedSnapshotRefusesStatusLine(t *testing.T) {
	if _, err := LayoutFor(FeatureStatusline); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("LayoutFor(statusLine) error = %v, want ErrNoContentLayout", err)
	}
	if _, err := Plan(FeatureStatusline, profiles.GetToolsFS()); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("Plan(statusLine, snapshot) error = %v, want ErrNoContentLayout", err)
	}
}

// TestEmbeddedSnapshotSkipsNoRepoFile pins the engine's deploy filter against the
// real snapshot: nothing the repo ships in the four trees is silently dropped as
// "deployer noise", so a .bak or a .git entry appearing there is a deliberate
// decision rather than an accident. The comparison is Enumerate (what deploys)
// against a raw walk of the same tree (what exists).
func TestEmbeddedSnapshotSkipsNoRepoFile(t *testing.T) {
	source := profiles.GetToolsFS()
	for _, tree := range []string{sourceDirRules, sourceDirSkills, sourceDirPlugins} {
		var onDisk []string
		walkErr := fs.WalkDir(source, tree, func(current string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(filepath.FromSlash(tree), filepath.FromSlash(current))
			if relErr != nil {
				return relErr
			}
			onDisk = append(onDisk, filepath.ToSlash(rel))
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk the snapshot tree %q: %v", tree, walkErr)
		}
		entries, err := Enumerate(Feature(tree), source)
		if err != nil {
			t.Fatalf("Enumerate(%q): %v", tree, err)
		}
		if len(entries) != len(onDisk) {
			t.Fatalf("Enumerate(%q) deploys %d file(s) but the snapshot tree holds %d; the deploy filter is dropping content",
				tree, len(entries), len(onDisk))
		}
	}
}

// TestSnapshotHookIsTheRepoHooksJSON checks the one file whose runtime fallback
// still exists: the snapshot's hooks/hooks.json must be the repo's file, not a
// stale copy, because that is what a deploy now writes into <config>/hooks.
func TestSnapshotHookIsTheRepoHooksJSON(t *testing.T) {
	repoPath := filepath.Join("..", "..", "..", "hooks", hookSourceFile)
	repoBytes, err := os.ReadFile(repoPath)
	if err != nil {
		t.Skipf("no repo-level hooks/%s at %q: a standalone agy-pro checkout ships no sibling hooks tree (not a product failure)", hookSourceFile, repoPath)
	}
	snapshotBytes, err := fs.ReadFile(profiles.GetToolsFS(), sourceDirHooks+"/"+hookSourceFile)
	if err != nil {
		t.Fatalf("read hooks.json from the embedded snapshot: %v", err)
	}
	if string(snapshotBytes) != string(repoBytes) {
		t.Fatalf("the snapshot's hooks/hooks.json (%d bytes) differs from the repo's hooks/%s (%d bytes)", len(snapshotBytes), hookSourceFile, len(repoBytes))
	}
}
