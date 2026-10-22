package integrations

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agypro/internal/paths"
)

// preInstallBytes is the content the "user" already had at a destination before
// the install ran. It deliberately differs from the fixture: an apply onto
// different bytes is an update, and an update is the only path that records a
// backup_ref a rollback can restore from.
func preInstallBytes(feature Feature, rel string) []byte {
	return []byte("pre-install " + string(feature) + " " + rel + "\n")
}

// seedPreInstall writes the pre-install bytes at the feature's first deployed
// destination and returns that destination-relative path with the bytes.
func seedPreInstall(t *testing.T, sandbox *deploySandbox, feature Feature) (string, []byte) {
	t.Helper()
	rels := deploys(feature)
	if len(rels) == 0 {
		t.Fatalf("%q deploys no fixture file", feature)
	}
	rel := rels[0]
	want := preInstallBytes(feature, rel)
	writeFileRaw(t, sandbox.dest(feature, rel), want)
	return rel, want
}

// rollbackResult returns the summary row for one destination path.
func rollbackResult(t *testing.T, summary *RollbackSummary, path string) RollbackDestination {
	t.Helper()
	for _, destination := range summary.Destinations {
		if destination.Path == path {
			return destination
		}
	}
	t.Fatalf("the rollback summary does not mention %q: %+v", path, summary.Destinations)
	return RollbackDestination{}
}

// manifestEntryAt reads one destination's record, reporting whether it exists.
func manifestEntryAt(t *testing.T, path string) (ManifestEntry, bool) {
	t.Helper()
	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return manifest.Entry(path)
}

// backupDirEntries lists the directories under the agy-pro backup root.
func backupDirEntries(t *testing.T) []string {
	t.Helper()
	root, err := paths.AgyProBackups()
	if err != nil {
		t.Fatalf("AgyProBackups: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		t.Fatalf("read %q: %v", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestRollbackRestoresPreInstallBytes is the update path: the install replaced
// the user's own file, so the rollback puts those exact bytes back, archives what
// it replaced, and drops the claim.
func TestRollbackRestoresPreInstallBytes(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			rel, preInstall := seedPreInstall(t, sandbox, feature)
			target := sandbox.dest(feature, rel)

			sandbox.apply(sandbox.plan(feature, source))
			entry, ok := manifestEntryAt(t, target)
			if !ok {
				t.Fatalf("the applied destination %q is not recorded", target)
			}
			if entry.BackupRef == "" {
				t.Fatalf("the overwritten destination records no backup_ref: %+v", entry)
			}
			assertFileBytes(t, target, sandbox.sourceBytes(feature, rel))

			edited := []byte("edited after the install\n")
			writeFileRaw(t, target, edited)

			summary, err := RollbackFeature(feature)
			if err != nil {
				t.Fatalf("RollbackFeature(%q): %v", feature, err)
			}

			// The seeded file is restored; every other deployed file was created by
			// the apply and is taken back.
			if got := summary.Restored(); got != 1 {
				t.Fatalf("summary restored %d destination(s), want 1: %+v", got, summary.Destinations)
			}
			if want := len(deploys(feature)) - 1; summary.Removed() != want {
				t.Fatalf("summary removed %d destination(s), want %d: %+v", summary.Removed(), want, summary.Destinations)
			}
			if summary.Skipped() != 0 {
				t.Fatalf("summary skipped %d destination(s): %+v", summary.Skipped(), summary.Destinations)
			}
			if summary.BackupDir == "" {
				t.Fatal("the rollback reported no backup directory")
			}
			if !strings.Contains(filepath.Base(summary.BackupDir), rollbackBackupTag) {
				t.Fatalf("backup directory %q does not carry the %q tag", summary.BackupDir, rollbackBackupTag)
			}
			if !summary.ManifestUpdated {
				t.Fatal("the rollback did not drop the resolved claims")
			}

			// Byte for byte: the destination holds the pre-install bytes again.
			assertFileBytes(t, target, preInstall)
			result := rollbackResult(t, summary, target)
			if !result.Restored() || !result.Wrote {
				t.Fatalf("rollback result = %+v, want a written restore", result)
			}
			if result.Source != entry.BackupRef {
				t.Fatalf("restored from %q, want the recorded backup_ref %q", result.Source, entry.BackupRef)
			}
			if result.ResultSHA != SHA256Hex(preInstall) {
				t.Fatalf("result digest = %q, want the pre-install digest", result.ResultSHA)
			}
			// The bytes the rollback replaced are archived, never dropped.
			if result.Archive == "" || !strings.HasPrefix(result.Archive, summary.BackupDir) {
				t.Fatalf("the replaced bytes were not archived under %q: %+v", summary.BackupDir, result)
			}
			assertFileBytes(t, result.Archive, edited)
			// The source archive is copied, not moved: the pre-install bytes are
			// still in the backup tree afterwards.
			assertFileBytes(t, entry.BackupRef, preInstall)

			if _, ok := manifestEntryAt(t, target); ok {
				t.Fatalf("%q is still claimed after the rollback", target)
			}
			if entries := sandbox.entriesFor(feature); len(entries) != 0 {
				t.Fatalf("the rollback left %d claim(s): %+v", len(entries), entries)
			}

			// The honest consequence of dropping the claim: the restored file is
			// nobody's per the manifest, so Status reports it as foreign until an
			// adopt or a re-apply records it. A single-file feature has no foreign
			// scan (deploy.go:776), so there the posture is simply "not deployed".
			status := sandbox.status(feature)
			if status.Deployed {
				t.Fatalf("the rollback left the feature recorded as deployed: %+v", status.Destinations)
			}
			if layout, layoutErr := LayoutFor(feature); layoutErr == nil && !layout.SingleFile() {
				foreign := false
				for _, destination := range status.Destinations {
					if destination.Path == target && destination.State == StateForeign {
						foreign = true
					}
				}
				if !foreign {
					t.Fatalf("the restored destination is not reported foreign: %+v", status.Destinations)
				}
			}
		})
	}
}

// TestRollbackTakesBackCreatedFiles is the create path: a destination the apply
// created records no backup_ref, so the rollback moves it out of the tree and
// leaves no claim behind.
func TestRollbackTakesBackCreatedFiles(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			sandbox.apply(sandbox.plan(feature, source))

			want := deploys(feature)
			layout, err := LayoutFor(feature)
			if err != nil {
				t.Fatalf("LayoutFor(%q): %v", feature, err)
			}

			summary, err := RollbackFeature(feature)
			if err != nil {
				t.Fatalf("RollbackFeature(%q): %v", feature, err)
			}
			if summary.Removed() != len(want) {
				t.Fatalf("summary removed %d destination(s), want %d: %+v", summary.Removed(), len(want), summary.Destinations)
			}
			if summary.Restored() != 0 {
				t.Fatalf("summary restored %d destination(s), want none: %+v", summary.Restored(), summary.Destinations)
			}
			if summary.BackupDir == "" {
				t.Fatal("the rollback moved files without reporting a backup directory")
			}
			if !strings.Contains(filepath.Base(summary.BackupDir), rollbackBackupTag) {
				t.Fatalf("backup directory %q does not carry the %q tag", summary.BackupDir, rollbackBackupTag)
			}

			for _, rel := range want {
				destination := sandbox.dest(feature, rel)
				assertAbsent(t, destination)
				result := rollbackResult(t, summary, destination)
				if !result.Removed() || !result.Wrote {
					t.Fatalf("%q result = %+v, want a written remove", destination, result)
				}
				if result.Archive == "" || !strings.HasPrefix(result.Archive, summary.BackupDir) {
					t.Fatalf("%q was moved without an archive path: %+v", destination, result)
				}
				// The moved bytes are the deployed bytes, preserved in full.
				assertFileBytes(t, result.Archive, sandbox.sourceBytes(feature, rel))
			}
			if entries := sandbox.entriesFor(feature); len(entries) != 0 {
				t.Fatalf("the rollback left %d claim(s): %+v", len(entries), entries)
			}
			// The tree the apply created is gone, emptied directories included.
			assertAbsent(t, layout.DestDir)
		})
	}
}

// TestRollbackTwiceIsANoOp pins idempotence: the first run drops the claims it
// resolved, so the second run finds nothing and writes nothing.
func TestRollbackTwiceIsANoOp(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			rel, _ := seedPreInstall(t, sandbox, feature)
			sandbox.apply(sandbox.plan(feature, source))

			if _, err := RollbackFeature(feature); err != nil {
				t.Fatalf("first RollbackFeature(%q): %v", feature, err)
			}
			manifestBefore := modTime(t, sandbox.manifestPath)
			backupsBefore := len(backupDirEntries(t))
			writes := countingDeployWrites(t)

			second, err := RollbackFeature(feature)
			if err != nil {
				t.Fatalf("second RollbackFeature(%q): %v", feature, err)
			}
			if len(second.Destinations) != 0 {
				t.Fatalf("the second rollback handled %d destination(s): %+v", len(second.Destinations), second.Destinations)
			}
			if second.BackupDir != "" {
				t.Fatalf("the second rollback allocated a backup directory %q", second.BackupDir)
			}
			if second.ManifestUpdated {
				t.Fatal("the second rollback rewrote the manifest")
			}
			if !strings.Contains(second.Detail, "nothing to roll back") {
				t.Fatalf("the second rollback detail = %q, want it to say there is nothing to roll back", second.Detail)
			}
			if got := atomic.LoadInt32(writes); got != 0 {
				t.Fatalf("the second rollback performed %d destination write(s)", got)
			}
			if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
				t.Fatalf("the manifest mtime moved on a no-op rollback: %v -> %v", manifestBefore, got)
			}
			if got := len(backupDirEntries(t)); got != backupsBefore {
				t.Fatalf("the second rollback created a backup directory: %d -> %d", backupsBefore, got)
			}
			// The restored file is still there, still holding the pre-install bytes.
			assertFileBytes(t, sandbox.dest(feature, rel), preInstallBytes(feature, rel))
		})
	}
}

// TestRollbackAfterRetractHasNothingToRollBack pins the contract between the two
// verbs: retract drops the manifest claims, so a rollback has no source of truth
// afterwards and says so instead of scanning the backup tree.
func TestRollbackAfterRetractHasNothingToRollBack(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	rel, _ := seedPreInstall(t, sandbox, FeatureRules)
	sandbox.apply(sandbox.plan(FeatureRules, source))

	retracted, err := Retract(FeatureRules)
	if err != nil {
		t.Fatalf("Retract: %v", err)
	}
	if len(retracted.Destinations) != 1 || retracted.Destinations[0].BackupPath == "" {
		t.Fatalf("retract summary = %+v, want one archived destination", retracted.Destinations)
	}
	manifestBefore := modTime(t, sandbox.manifestPath)
	writes := countingDeployWrites(t)

	summary, err := RollbackFeature(FeatureRules)
	if err != nil {
		t.Fatalf("RollbackFeature after retract: %v", err)
	}
	if len(summary.Destinations) != 0 {
		t.Fatalf("the rollback handled %d destination(s) after a retract: %+v", len(summary.Destinations), summary.Destinations)
	}
	if summary.ManifestUpdated || summary.BackupDir != "" {
		t.Fatalf("the rollback write something after a retract: %+v", summary)
	}
	if !strings.Contains(summary.Detail, "nothing to roll back") {
		t.Fatalf("detail = %q, want the named empty state", summary.Detail)
	}
	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the rollback performed %d destination write(s)", got)
	}
	if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
		t.Fatalf("the manifest mtime moved: %v -> %v", manifestBefore, got)
	}
	// The retract archive is left exactly where the retract put it: a rollback
	// does not read it back.
	assertAbsent(t, sandbox.dest(FeatureRules, rel))
	assertFileBytes(t, retracted.Destinations[0].BackupPath, sandbox.sourceBytes(FeatureRules, rel))
}

// TestRollbackSkipsAVanishedBackup pins the stale-source rule: a backup_ref that
// is gone is a named skip, the live file is untouched, and the claim survives.
func TestRollbackSkipsAVanishedBackup(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	rel, _ := seedPreInstall(t, sandbox, FeatureRules)
	target := sandbox.dest(FeatureRules, rel)
	sandbox.apply(sandbox.plan(FeatureRules, source))

	entry, ok := manifestEntryAt(t, target)
	if !ok || entry.BackupRef == "" {
		t.Fatalf("no backup_ref recorded for %q: %+v", target, entry)
	}
	installed := sandbox.sourceBytes(FeatureRules, rel)
	if err := os.Remove(entry.BackupRef); err != nil {
		t.Fatalf("remove the recorded backup %q: %v", entry.BackupRef, err)
	}
	manifestBefore := modTime(t, sandbox.manifestPath)
	writes := countingDeployWrites(t)

	summary, err := RollbackFeature(FeatureRules)
	if err != nil {
		t.Fatalf("RollbackFeature: %v", err)
	}
	if summary.Changed() != 0 {
		t.Fatalf("the rollback changed %d destination(s) with no source: %+v", summary.Changed(), summary.Destinations)
	}
	if summary.Skipped() != 1 {
		t.Fatalf("summary skipped %d destination(s), want 1: %+v", summary.Skipped(), summary.Destinations)
	}
	result := rollbackResult(t, summary, target)
	if result.SkipReason != RollbackSkipBackupMissing {
		t.Fatalf("skip reason = %q, want %q", result.SkipReason, RollbackSkipBackupMissing)
	}
	if result.Wrote || result.Archive != "" {
		t.Fatalf("the skipped destination was touched: %+v", result)
	}
	assertFileBytes(t, target, installed)
	if _, ok := manifestEntryAt(t, target); !ok {
		t.Fatalf("the skipped destination lost its claim")
	}
	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the skipped rollback performed %d destination write(s)", got)
	}
	if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
		t.Fatalf("the manifest mtime moved on a skip-only rollback: %v -> %v", manifestBefore, got)
	}
	if !strings.Contains(summary.Detail, "nothing to roll back") {
		t.Fatalf("detail = %q, want the summary to say nothing was rolled back", summary.Detail)
	}
}

// TestRollbackSkipsUnusableBackups pins the two other unprovable sources: a
// recorded path outside the backup tree and a recorded path that is not a
// regular file.
func TestRollbackSkipsUnusableBackups(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		ref    func(t *testing.T, sandbox *deploySandbox, target string) string
	}{
		{
			name:   "outside the backup tree",
			reason: RollbackSkipBackupOutside,
			ref: func(t *testing.T, sandbox *deploySandbox, target string) string {
				t.Helper()
				return filepath.Join(sandbox.home, "elsewhere", "AGENTS.md")
			},
		},
		{
			name:   "not a regular file",
			reason: RollbackSkipBackupUnreadable,
			ref: func(t *testing.T, sandbox *deploySandbox, target string) string {
				t.Helper()
				root, err := paths.AgyProBackups()
				if err != nil {
					t.Fatalf("AgyProBackups: %v", err)
				}
				dir := filepath.Join(root, "20261022_030405_rollback", "rules")
				if err := os.MkdirAll(filepath.Join(dir, "AGENTS.md"), deployDirPerm); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return filepath.Join(dir, "AGENTS.md")
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			rel, _ := seedPreInstall(t, sandbox, FeatureRules)
			target := sandbox.dest(FeatureRules, rel)
			sandbox.apply(sandbox.plan(FeatureRules, source))

			entry, ok := manifestEntryAt(t, target)
			if !ok {
				t.Fatalf("%q is not recorded", target)
			}
			installed := sandbox.sourceBytes(FeatureRules, rel)
			entry.BackupRef = testCase.ref(t, sandbox, target)
			manifest, err := LoadManifest()
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			mustSet(t, manifest, entry)
			if err := SaveManifest(manifest); err != nil {
				t.Fatalf("SaveManifest: %v", err)
			}

			summary, err := RollbackFeature(FeatureRules)
			if err != nil {
				t.Fatalf("RollbackFeature: %v", err)
			}
			result := rollbackResult(t, summary, target)
			if result.SkipReason != testCase.reason {
				t.Fatalf("skip reason = %q, want %q (detail %q)", result.SkipReason, testCase.reason, result.Detail)
			}
			assertFileBytes(t, target, installed)
			if _, ok := manifestEntryAt(t, target); !ok {
				t.Fatalf("the skipped destination lost its claim")
			}
		})
	}
}

// TestRollbackLeavesForeignAndAdoptedDestinationsAlone pins the ownership rule: a
// file no entry claims, and a file the manifest only adopted, are both left
// exactly as they are.
func TestRollbackLeavesForeignAndAdoptedDestinationsAlone(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()

	// An adopted destination: the user's file already equals the fixture bytes, so
	// the apply records it without writing it.
	adoptedRel := "AGENTS.md"
	adoptedPath := sandbox.dest(FeatureRules, adoptedRel)
	writeFileRaw(t, adoptedPath, sandbox.sourceBytes(FeatureRules, adoptedRel))

	summary := sandbox.apply(sandbox.plan(FeatureRules, source))
	if summary.Identical != 1 || summary.Written() != 0 {
		t.Fatalf("apply summary = %+v, want a record-only adopt", summary)
	}
	entry, ok := manifestEntryAt(t, adoptedPath)
	if !ok || entry.Provenance != ProvenanceAdopted {
		t.Fatalf("adopted entry = %+v (present %v), want provenance %q", entry, ok, ProvenanceAdopted)
	}

	// A foreign file nobody recorded.
	foreignPath := sandbox.dest(FeatureRules, "foreign.md")
	writeFileRaw(t, foreignPath, []byte("someone else's file\n"))

	writes := countingDeployWrites(t)
	rolledBack, err := RollbackFeature(FeatureRules)
	if err != nil {
		t.Fatalf("RollbackFeature: %v", err)
	}
	if rolledBack.Changed() != 0 {
		t.Fatalf("the rollback changed %d destination(s): %+v", rolledBack.Changed(), rolledBack.Destinations)
	}
	result := rollbackResult(t, rolledBack, adoptedPath)
	if result.SkipReason != RollbackSkipAdopted {
		t.Fatalf("skip reason = %q, want %q", result.SkipReason, RollbackSkipAdopted)
	}
	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the rollback performed %d destination write(s)", got)
	}
	assertFileBytes(t, adoptedPath, sandbox.sourceBytes(FeatureRules, adoptedRel))
	assertFileBytes(t, foreignPath, []byte("someone else's file\n"))
	if _, ok := manifestEntryAt(t, adoptedPath); !ok {
		t.Fatal("the adopted destination lost its claim")
	}
}

// TestRollbackSkipsADeletedDestination pins that a rollback does not resurrect a
// file the user deleted: the entry stays (Status reports it missing, repair can
// rebuild it) and the archived bytes are not written back.
func TestRollbackSkipsADeletedDestination(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	rel, _ := seedPreInstall(t, sandbox, FeatureRules)
	target := sandbox.dest(FeatureRules, rel)
	sandbox.apply(sandbox.plan(FeatureRules, source))

	entry, ok := manifestEntryAt(t, target)
	if !ok || entry.BackupRef == "" {
		t.Fatalf("no backup_ref recorded for %q: %+v", target, entry)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove the deployed file: %v", err)
	}

	summary, err := RollbackFeature(FeatureRules)
	if err != nil {
		t.Fatalf("RollbackFeature: %v", err)
	}
	result := rollbackResult(t, summary, target)
	if result.SkipReason != RollbackSkipDestinationMissing {
		t.Fatalf("skip reason = %q, want %q", result.SkipReason, RollbackSkipDestinationMissing)
	}
	assertAbsent(t, target)
	assertFileBytes(t, entry.BackupRef, preInstallBytes(FeatureRules, rel))
	if _, ok := manifestEntryAt(t, target); !ok {
		t.Fatal("the deleted destination lost its claim")
	}
}

// TestRollbackDoesNotRewriteIdenticalBytes pins the equality shortcut: when the
// destination already holds the archived bytes, the rollback resolves the claim
// without a write and without a new backup directory.
func TestRollbackDoesNotRewriteIdenticalBytes(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	rel, preInstall := seedPreInstall(t, sandbox, FeatureRules)
	target := sandbox.dest(FeatureRules, rel)
	sandbox.apply(sandbox.plan(FeatureRules, source))

	// Somebody put the pre-install bytes back by hand.
	writeFileRaw(t, target, preInstall)
	backupsBefore := len(backupDirEntries(t))
	writes := countingDeployWrites(t)

	summary, err := RollbackFeature(FeatureRules)
	if err != nil {
		t.Fatalf("RollbackFeature: %v", err)
	}
	result := rollbackResult(t, summary, target)
	if !result.Restored() || result.Wrote {
		t.Fatalf("result = %+v, want a restore that wrote nothing", result)
	}
	if result.Archive != "" || summary.BackupDir != "" {
		t.Fatalf("the rollback archived bytes it did not need to replace: %+v", summary)
	}
	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the rollback performed %d destination write(s)", got)
	}
	if got := len(backupDirEntries(t)); got != backupsBefore {
		t.Fatalf("the rollback created a backup directory: %d -> %d", backupsBefore, got)
	}
	assertFileBytes(t, target, preInstall)
	if _, ok := manifestEntryAt(t, target); ok {
		t.Fatal("the resolved claim survived the rollback")
	}
}

// TestPendingRollbackWritesNothing pins the advisory preview the consent gate
// renders: it names what a rollback would do and touches neither the tree nor the
// bookkeeping.
func TestPendingRollbackWritesNothing(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	rel, _ := seedPreInstall(t, sandbox, FeatureRules)
	target := sandbox.dest(FeatureRules, rel)
	sandbox.apply(sandbox.plan(FeatureRules, source))
	edited := []byte("edited after the install\n")
	writeFileRaw(t, target, edited)
	manifestBefore := modTime(t, sandbox.manifestPath)
	writes := countingDeployWrites(t)

	pending, err := PendingRollback(FeatureRules)
	if err != nil {
		t.Fatalf("PendingRollback: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the preview named %d destination(s), want 1: %+v", len(pending), pending)
	}
	if !pending[0].Restored() {
		t.Fatalf("the preview did not name a restore: %+v", pending[0])
	}
	if pending[0].Source == "" || pending[0].ResultSHA == "" {
		t.Fatalf("the preview does not name the source and the resulting digest: %+v", pending[0])
	}
	if pending[0].Archive != "" || pending[0].Wrote {
		t.Fatalf("the preview claims a write it did not make: %+v", pending[0])
	}
	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the preview performed %d destination write(s)", got)
	}
	if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
		t.Fatalf("the preview rewrote the manifest: %v -> %v", manifestBefore, got)
	}
	assertFileBytes(t, target, edited)
}

// TestRollbackRefusesFeaturesItDoesNotOwn pins the refusals: the statusline door
// has its own verb, and an unknown feature word fails loudly. Nothing is written
// either way.
func TestRollbackRefusesFeaturesItDoesNotOwn(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureRules, source))
	manifestBefore := modTime(t, sandbox.manifestPath)
	writes := countingDeployWrites(t)

	if _, err := RollbackFeature(FeatureStatusline); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("RollbackFeature(statusline) error = %v, want ErrNoContentLayout", err)
	}
	for _, word := range []Feature{Feature("nope"), Feature("")} {
		if _, err := RollbackFeature(word); !errors.Is(err, ErrUnknownFeature) {
			t.Fatalf("RollbackFeature(%q) error = %v, want ErrUnknownFeature", word, err)
		}
		if _, err := PendingRollback(word); !errors.Is(err, ErrUnknownFeature) {
			t.Fatalf("PendingRollback(%q) error = %v, want ErrUnknownFeature", word, err)
		}
	}
	if _, err := PendingRollback(FeatureStatusline); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("PendingRollback(statusline) error = %v, want ErrNoContentLayout", err)
	}

	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the refused rollbacks performed %d destination write(s)", got)
	}
	if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
		t.Fatalf("a refused rollback rewrote the manifest: %v -> %v", manifestBefore, got)
	}
	if entries := sandbox.entriesFor(FeatureRules); len(entries) != 1 {
		t.Fatalf("a refused rollback changed the claims: %+v", entries)
	}
}

// TestRollbackSerializesWithApply pins the package-level mutex for the rollback
// verb: a rollback and an apply running together load-modify-save one manifest,
// and without serialization one of them would drop the other's bookkeeping.
func TestRollbackSerializesWithApply(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	seedPreInstall(t, sandbox, FeatureRules)
	sandbox.apply(sandbox.plan(FeatureRules, source))
	seedPreInstall(t, sandbox, FeatureSkills)
	sandbox.apply(sandbox.plan(FeatureSkills, source))
	pluginsPlan := sandbox.plan(FeaturePlugins, source)

	var wait sync.WaitGroup
	errs := make(chan error, 3)
	wait.Add(3)
	go func() {
		defer wait.Done()
		if _, err := RollbackFeature(FeatureRules); err != nil {
			errs <- err
		}
	}()
	go func() {
		defer wait.Done()
		if _, err := RollbackFeature(FeatureSkills); err != nil {
			errs <- err
		}
	}()
	go func() {
		defer wait.Done()
		if _, err := Apply(pluginsPlan); err != nil {
			errs <- err
		}
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent rollback/apply: %v", err)
	}

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	wantPlugins := len(deploys(FeaturePlugins))
	if manifest.Len() != wantPlugins {
		t.Fatalf("manifest records %d destination(s), want the %d plugin ones: %+v",
			manifest.Len(), wantPlugins, manifest.Paths())
	}
	for _, rel := range deploys(FeaturePlugins) {
		if _, ok := manifest.Entry(sandbox.dest(FeaturePlugins, rel)); !ok {
			t.Fatalf("the concurrent apply lost its claim on %q", rel)
		}
	}
	for _, feature := range []Feature{FeatureRules, FeatureSkills} {
		if entries := sandbox.entriesFor(feature); len(entries) != 0 {
			t.Fatalf("%q still records %d destination(s) after its rollback: %+v", feature, len(entries), entries)
		}
	}
}

// TestRollbackTouchesOneFeatureOnly pins the scope: a rollback is per feature, so
// a second feature's files and claims are exactly as the apply left them.
func TestRollbackTouchesOneFeatureOnly(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureRules, source))
	sandbox.apply(sandbox.plan(FeatureSkills, source))

	if _, err := RollbackFeature(FeatureRules); err != nil {
		t.Fatalf("RollbackFeature(rules): %v", err)
	}

	for _, rel := range deploys(FeatureRules) {
		assertAbsent(t, sandbox.dest(FeatureRules, rel))
	}
	entries := sandbox.entriesFor(FeatureSkills)
	if len(entries) != len(deploys(FeatureSkills)) {
		t.Fatalf("the skills claims changed: %+v", entries)
	}
	for _, rel := range deploys(FeatureSkills) {
		target := sandbox.dest(FeatureSkills, rel)
		assertFileBytes(t, target, sandbox.sourceBytes(FeatureSkills, rel))
	}
	status := sandbox.status(FeatureSkills)
	if !status.OK() {
		t.Fatalf("the untouched feature no longer reads clean: %+v", status.Destinations)
	}
}

// TestRollbackWritesWithTheDeployFileMode pins the write mode at the seam: a
// restored destination is the user's own configuration and must not be world
// readable (Windows does not round-trip mode bits through Stat, so the seam is the
// authority, the rule deploy_test.go:225 half-applies).
func TestRollbackWritesWithTheDeployFileMode(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	rel, _ := seedPreInstall(t, sandbox, FeatureRules)
	target := sandbox.dest(FeatureRules, rel)
	sandbox.apply(sandbox.plan(FeatureRules, source))

	var restorePerms []os.FileMode
	previous := deployWriteFile
	deployWriteFile = func(path string, data []byte, perm os.FileMode) error {
		restorePerms = append(restorePerms, perm)
		return previous(path, data, perm)
	}
	t.Cleanup(func() { deployWriteFile = previous })

	if _, err := RollbackFeature(FeatureRules); err != nil {
		t.Fatalf("RollbackFeature: %v", err)
	}
	if len(restorePerms) != 1 {
		t.Fatalf("the rollback made %d destination write(s), want 1", len(restorePerms))
	}
	if restorePerms[0] != deployFilePerm {
		t.Fatalf("restore write mode = %o, want %o", restorePerms[0], deployFilePerm)
	}
	assertFileBytes(t, target, preInstallBytes(FeatureRules, rel))
}
