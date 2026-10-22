package integrations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// deploySandbox is one isolated deployment environment: a temp home with the
// Gemini config dir inside it, a temp AGY_PRO_HOME for the manifest and the
// backups, and a fake toolkit root carrying the hooks.json the hook feature
// deploys. Nothing in this file can reach a real ~/.gemini, ~/.agy-pro or
// toolkit root.
type deploySandbox struct {
	t            *testing.T
	home         string
	configDir    string
	manifestPath string
	toolkitRoot  string
}

func newDeploySandbox(t *testing.T) *deploySandbox {
	t.Helper()
	home := sandboxHome(t)
	configDir := filepath.Join(home, ".gemini", "config")
	t.Setenv(paths.EnvGeminiConfigDir, configDir)
	manifestPath := sandboxStore(t)
	root := fakeToolkitRoot(t)
	writeFileRaw(t, filepath.Join(root, sourceDirHooks, hookSourceFile), []byte(hookFixtureJSON))
	t.Setenv(EnvToolkitRoot, root)
	return &deploySandbox{
		t:            t,
		home:         home,
		configDir:    configDir,
		manifestPath: manifestPath,
		toolkitRoot:  root,
	}
}

// dest returns the absolute destination a feature-relative path should reach.
func (s *deploySandbox) dest(feature Feature, rel string) string {
	s.t.Helper()
	layout, err := LayoutFor(feature)
	if err != nil {
		s.t.Fatalf("LayoutFor(%q): %v", feature, err)
	}
	destination, err := layout.Destination(rel)
	if err != nil {
		s.t.Fatalf("Destination(%q): %v", rel, err)
	}
	return destination
}

// plan builds a plan and fails the test when the engine refuses it.
func (s *deploySandbox) plan(feature Feature, source fs.FS) *DeployPlan {
	s.t.Helper()
	plan, err := Plan(feature, source)
	if err != nil {
		s.t.Fatalf("Plan(%q): %v", feature, err)
	}
	return plan
}

// apply applies a plan and fails the test when the engine refuses it.
func (s *deploySandbox) apply(plan *DeployPlan) *ApplySummary {
	s.t.Helper()
	summary, err := Apply(plan)
	if err != nil {
		s.t.Fatalf("Apply(%q): %v", plan.Feature, err)
	}
	return summary
}

// status reads a feature's live posture and fails the test on a refusal.
func (s *deploySandbox) status(feature Feature) *FeatureStatus {
	s.t.Helper()
	status, err := Status(feature)
	if err != nil {
		s.t.Fatalf("Status(%q): %v", feature, err)
	}
	return status
}

// entriesFor returns the manifest entries a feature owns right now.
func (s *deploySandbox) entriesFor(feature Feature) []ManifestEntry {
	s.t.Helper()
	manifest, err := LoadManifest()
	if err != nil {
		s.t.Fatalf("LoadManifest: %v", err)
	}
	layout, err := LayoutFor(feature)
	if err != nil {
		s.t.Fatalf("LayoutFor(%q): %v", feature, err)
	}
	return ownedDestinations(manifest, layout)
}

// sourceBytes returns the bytes a feature's fixture holds for a relative path.
func (s *deploySandbox) sourceBytes(feature Feature, rel string) []byte {
	s.t.Helper()
	switch feature {
	case FeatureHook:
		return []byte(hookFixtureJSON)
	default:
		data, err := fs.ReadFile(fixtureContent(), sourceDirOf(feature)+"/"+rel)
		if err != nil {
			s.t.Fatalf("read fixture %q: %v", rel, err)
		}
		return data
	}
}

// deploys returns the feature-relative paths a feature's fixture deploys.
func deploys(feature Feature) []string {
	if feature == FeatureHook {
		return []string{hookSourceFile}
	}
	return fixtureFiles(feature)
}

// deployFeatures is every feature this engine deploys (the settings-door
// feature is T5's and has no layout here).
func deployFeatures() []Feature {
	return []Feature{FeatureRules, FeatureSkills, FeaturePlugins, FeatureHook}
}

// countingDeployWrites installs a counter on the destination-writer seam and
// returns the counter. It counts DESTINATION writes only: the manifest has its
// own seam, so bookkeeping writes never show up here.
func countingDeployWrites(t *testing.T) *int32 {
	t.Helper()
	var count int32
	previous := deployWriteFile
	deployWriteFile = func(path string, data []byte, perm os.FileMode) error {
		atomic.AddInt32(&count, 1)
		return previous(path, data, perm)
	}
	t.Cleanup(func() { deployWriteFile = previous })
	return &count
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%q holds %q, want %q", path, string(got), string(want))
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err == nil {
		t.Fatalf("%q still exists (%d bytes, dir=%v)", path, info.Size(), info.IsDir())
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %q: %v", path, err)
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info.ModTime()
}

// TestApplyFreshDeployPerFeature is the first column of the deployment matrix:
// one feature, a fresh config tree, everything created and recorded.
func TestApplyFreshDeployPerFeature(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()

			plan := sandbox.plan(feature, source)
			created, updated, identical := plan.Counts()
			want := deploys(feature)
			if created != len(want) || updated != 0 || identical != 0 {
				t.Fatalf("plan counts = %d create / %d update / %d identical, want %d create",
					created, updated, identical, len(want))
			}
			if plan.Writes() != len(want) {
				t.Fatalf("plan.Writes() = %d, want %d", plan.Writes(), len(want))
			}

			writes := countingDeployWrites(t)
			summary := sandbox.apply(plan)
			if got := atomic.LoadInt32(writes); got != int32(len(want)) {
				t.Fatalf("apply performed %d destination write(s), want %d", got, len(want))
			}
			if summary.Created != len(want) || summary.Updated != 0 || summary.Identical != 0 {
				t.Fatalf("apply counts = %d/%d/%d, want %d create", summary.Created, summary.Updated, summary.Identical, len(want))
			}
			if !summary.ManifestUpdated {
				t.Fatal("the first apply did not record its destinations")
			}
			if summary.BackupDir != "" {
				t.Fatalf("a create-only apply archived %q", summary.BackupDir)
			}

			for _, rel := range want {
				destination := sandbox.dest(feature, rel)
				assertFileBytes(t, destination, sandbox.sourceBytes(feature, rel))
				if runtime.GOOS != "windows" {
					info, err := os.Stat(destination)
					if err != nil {
						t.Fatalf("stat %q: %v", destination, err)
					}
					if perm := info.Mode().Perm(); perm != deployFilePerm {
						t.Fatalf("%q permission = %o, want %o", destination, perm, deployFilePerm)
					}
				}
			}

			entries := sandbox.entriesFor(feature)
			if len(entries) != len(want) {
				t.Fatalf("manifest records %d destination(s), want %d", len(entries), len(want))
			}
			for _, entry := range entries {
				if entry.Provenance != ProvenanceGoApplied {
					t.Fatalf("%q provenance = %q, want %q", entry.Path, entry.Provenance, ProvenanceGoApplied)
				}
				if entry.SHA256 != SHA256Hex(sandbox.sourceBytes(feature, relOf(feature, entry.Path))) {
					t.Fatalf("%q recorded digest %q, want the digest of the bytes on disk", entry.Path, entry.SHA256)
				}
			}

			status := sandbox.status(feature)
			if !status.Deployed {
				t.Fatal("status reports the feature as not deployed after an apply")
			}
			if status.Clean != len(want) || status.Drifted != 0 || status.Missing != 0 || status.Foreign != 0 {
				t.Fatalf("status = %d clean / %d drifted / %d missing / %d foreign, want %d clean",
					status.Clean, status.Drifted, status.Missing, status.Foreign, len(want))
			}
			if !status.OK() {
				t.Fatal("status.OK() = false on a clean deployment")
			}
		})
	}
}

// relOf returns the destination-relative slash path of a recorded destination.
func relOf(feature Feature, destination string) string {
	dir := sourceDirOf(feature)
	index := strings.Index(filepath.ToSlash(destination), "/"+dir+"/")
	if index < 0 {
		return filepath.Base(destination)
	}
	return filepath.ToSlash(destination)[index+len(dir)+2:]
}

// TestApplyTwiceIsIdenticalAndWriteFree is the second column: an immediate
// re-apply writes nothing at all, not even the manifest.
func TestApplyTwiceIsIdenticalAndWriteFree(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			want := deploys(feature)

			sandbox.apply(sandbox.plan(feature, source))

			before := make(map[string]time.Time, len(want))
			for _, rel := range want {
				destination := sandbox.dest(feature, rel)
				before[destination] = modTime(t, destination)
			}
			manifestBefore := modTime(t, sandbox.manifestPath)

			second := sandbox.plan(feature, source)
			created, updated, identical := second.Counts()
			if created != 0 || updated != 0 || identical != len(want) {
				t.Fatalf("second plan = %d create / %d update / %d identical, want all identical",
					created, updated, identical)
			}
			if second.Writes() != 0 {
				t.Fatalf("second plan reports %d write(s)", second.Writes())
			}

			writes := countingDeployWrites(t)
			summary := sandbox.apply(second)
			if got := atomic.LoadInt32(writes); got != 0 {
				t.Fatalf("the second apply performed %d destination write(s), want 0", got)
			}
			if summary.Identical != len(want) || summary.Written() != 0 {
				t.Fatalf("second apply = %d identical / %d written", summary.Identical, summary.Written())
			}
			if summary.ManifestUpdated {
				t.Fatal("the second apply rewrote the manifest")
			}
			if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
				t.Fatalf("the manifest mtime moved on a no-op apply: %v -> %v", manifestBefore, got)
			}
			for destination, want := range before {
				if got := modTime(t, destination); !got.Equal(want) {
					t.Fatalf("%q mtime moved on a no-op apply: %v -> %v", destination, want, got)
				}
			}
		})
	}
}

// TestStatusReportsDriftAfterLiveEdit is the third column: an edited live file
// is drift, and Status repairs nothing.
func TestStatusReportsDriftAfterLiveEdit(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			sandbox.apply(sandbox.plan(feature, source))

			want := deploys(feature)
			editedRel := want[0]
			edited := []byte("edited by the user\n")
			writeFileRaw(t, sandbox.dest(feature, editedRel), edited)

			status := sandbox.status(feature)
			if status.Drifted != 1 {
				t.Fatalf("status.Drifted = %d, want 1 (%+v)", status.Drifted, status.Destinations)
			}
			if status.OK() {
				t.Fatal("status.OK() = true with a drifted destination")
			}
			var found bool
			for _, destination := range status.Destinations {
				if destination.Path != sandbox.dest(feature, editedRel) {
					continue
				}
				found = true
				if destination.State != StateDrifted {
					t.Fatalf("state = %q, want %q", destination.State, StateDrifted)
				}
				if destination.WantSHA == destination.LiveSHA || destination.LiveSHA != SHA256Hex(edited) {
					t.Fatalf("want/live digests = %q/%q", destination.WantSHA, destination.LiveSHA)
				}
			}
			if !found {
				t.Fatalf("the drifted destination is missing from %+v", status.Destinations)
			}

			// Drift is reported, never repaired (pick 7).
			assertFileBytes(t, sandbox.dest(feature, editedRel), edited)
		})
	}
}

// TestRetractBacksUpAndClears is the fourth column: retract moves the
// destinations into the backup tree and clears the bookkeeping.
func TestRetractBacksUpAndClears(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			sandbox.apply(sandbox.plan(feature, source))

			want := deploys(feature)
			summary, err := Retract(feature)
			if err != nil {
				t.Fatalf("Retract(%q): %v", feature, err)
			}
			if len(summary.Destinations) != len(want) {
				t.Fatalf("retract handled %d destination(s), want %d", len(summary.Destinations), len(want))
			}
			if len(summary.BackupDir) == 0 {
				t.Fatal("retract reported no backup directory")
			}
			if !summary.ManifestUpdated {
				t.Fatal("retract did not clear the manifest")
			}
			if summary.BackedUp() != len(want) {
				t.Fatalf("retract reported %d moved destination(s), want %d", summary.BackedUp(), len(want))
			}

			for _, rel := range want {
				destination := sandbox.dest(feature, rel)
				assertAbsent(t, destination)
			}
			for _, result := range summary.Destinations {
				if result.Missing || result.Drifted {
					t.Fatalf("unexpected retract state for %q: %+v", result.DestPath, result)
				}
				if result.BackupPath == "" {
					t.Fatalf("%q was reported as moved but has no backup path", result.DestPath)
				}
				if !strings.HasPrefix(result.BackupPath, summary.BackupDir) {
					t.Fatalf("backup path %q is not inside %q", result.BackupPath, summary.BackupDir)
				}
				assertFileBytes(t, result.BackupPath, sandbox.sourceBytes(feature, relOf(feature, result.DestPath)))
			}

			if entries := sandbox.entriesFor(feature); len(entries) != 0 {
				t.Fatalf("retract left %d manifest record(s): %+v", len(entries), entries)
			}
			manifest, loadErr := LoadManifest()
			if loadErr != nil {
				t.Fatalf("LoadManifest after retract: %v", loadErr)
			}
			if manifest.Len() != 0 {
				t.Fatalf("manifest still records %d destination(s)", manifest.Len())
			}

			// The emptied destination tree is pruned, so a retract leaves no
			// skeleton of empty directories behind.
			layout, layoutErr := LayoutFor(feature)
			if layoutErr != nil {
				t.Fatalf("LayoutFor: %v", layoutErr)
			}
			assertAbsent(t, layout.DestDir)
		})
	}
}

// TestRetractTwiceIsStable is the fifth column: a second retract is a no-op
// with no error and no new backup directory.
func TestRetractTwiceIsStable(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			sandbox.apply(sandbox.plan(feature, source))

			first, err := Retract(feature)
			if err != nil {
				t.Fatalf("first Retract: %v", err)
			}
			manifestBefore := modTime(t, sandbox.manifestPath)

			second, err := Retract(feature)
			if err != nil {
				t.Fatalf("second Retract: %v", err)
			}
			if len(second.Destinations) != 0 {
				t.Fatalf("the second retract handled %d destination(s): %+v", len(second.Destinations), second.Destinations)
			}
			if second.BackupDir != "" {
				t.Fatalf("the second retract allocated a backup directory %q", second.BackupDir)
			}
			if second.ManifestUpdated {
				t.Fatal("the second retract rewrote the manifest")
			}
			if got := modTime(t, sandbox.manifestPath); !got.Equal(manifestBefore) {
				t.Fatalf("the manifest mtime moved on a no-op retract: %v -> %v", manifestBefore, got)
			}
			if first.BackupDir == second.BackupDir {
				t.Fatalf("both retracts report backup dir %q", first.BackupDir)
			}
		})
	}
}

// TestReapplyAfterRetract is the sixth column: the feature comes back.
func TestReapplyAfterRetract(t *testing.T) {
	for _, feature := range deployFeatures() {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			source := fixtureContent()
			sandbox.apply(sandbox.plan(feature, source))
			if _, err := Retract(feature); err != nil {
				t.Fatalf("Retract: %v", err)
			}

			replan := sandbox.plan(feature, source)
			created, _, _ := replan.Counts()
			if created != len(deploys(feature)) {
				t.Fatalf("re-plan after retract = %d create, want %d", created, len(deploys(feature)))
			}
			summary := sandbox.apply(replan)
			if summary.Created != len(deploys(feature)) {
				t.Fatalf("re-apply created %d destination(s), want %d", summary.Created, len(deploys(feature)))
			}
			for _, rel := range deploys(feature) {
				assertFileBytes(t, sandbox.dest(feature, rel), sandbox.sourceBytes(feature, rel))
			}
			if status := sandbox.status(feature); status.Clean != len(deploys(feature)) {
				t.Fatalf("status after re-apply = %d clean, want %d", status.Clean, len(deploys(feature)))
			}
		})
	}
}

// TestApplyRefusesStaleBase pins the CWE-367 rule: a destination that changed
// between Plan and Apply stops the whole run with nothing written.
func TestApplyRefusesStaleBase(t *testing.T) {
	t.Run("identical destination edited after the plan", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		source := fixtureContent()
		sandbox.apply(sandbox.plan(FeatureRules, source))

		plan := sandbox.plan(FeatureRules, source)
		target := sandbox.dest(FeatureRules, "AGENTS.md")
		interloper := []byte("changed between plan and apply\n")
		writeFileRaw(t, target, interloper)
		manifestBefore := readFileRaw(t, sandbox.manifestPath)

		writes := countingDeployWrites(t)
		_, err := Apply(plan)
		assertStaleRefusal(t, err, target, interloper, SHA256Hex([]byte("# agents rule\n")))
		if got := atomic.LoadInt32(writes); got != 0 {
			t.Fatalf("a refused apply performed %d write(s)", got)
		}
		assertFileBytes(t, target, interloper)
		assertFileBytes(t, sandbox.manifestPath, manifestBefore)
	})

	t.Run("update destination edited after the plan", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		source := fixtureContent()
		sandbox.apply(sandbox.plan(FeatureRules, source))

		changed := fixtureContent()
		changed["rules/AGENTS.md"] = &fstest.MapFile{Data: []byte("# agents rule v2\n")}
		plan := sandbox.plan(FeatureRules, changed)
		created, updated, _ := plan.Counts()
		if created != 0 || updated != 1 {
			t.Fatalf("plan = %d create / %d update, want one update", created, updated)
		}

		target := sandbox.dest(FeatureRules, "AGENTS.md")
		interloper := []byte("someone else was here\n")
		writeFileRaw(t, target, interloper)
		manifestBefore := readFileRaw(t, sandbox.manifestPath)

		writes := countingDeployWrites(t)
		_, err := Apply(plan)
		assertStaleRefusal(t, err, target, interloper, SHA256Hex([]byte("# agents rule\n")))
		if got := atomic.LoadInt32(writes); got != 0 {
			t.Fatalf("a refused apply performed %d write(s)", got)
		}
		assertFileBytes(t, target, interloper)
		assertFileBytes(t, sandbox.manifestPath, manifestBefore)
	})

	t.Run("destination appeared after a create plan", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		source := fixtureContent()

		plan := sandbox.plan(FeatureRules, source)
		target := sandbox.dest(FeatureRules, "AGENTS.md")
		appeared := []byte("a file that was not there\n")
		writeFileRaw(t, target, appeared)

		writes := countingDeployWrites(t)
		_, err := Apply(plan)
		assertStaleRefusal(t, err, target, appeared, "")
		if got := atomic.LoadInt32(writes); got != 0 {
			t.Fatalf("a refused apply performed %d write(s)", got)
		}
		assertFileBytes(t, target, appeared)
		assertAbsent(t, sandbox.manifestPath)
	})

	t.Run("destination removed after the plan", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		source := fixtureContent()
		sandbox.apply(sandbox.plan(FeatureRules, source))

		plan := sandbox.plan(FeatureRules, source)
		target := sandbox.dest(FeatureRules, "AGENTS.md")
		if err := os.Remove(target); err != nil {
			t.Fatalf("remove %q: %v", target, err)
		}

		writes := countingDeployWrites(t)
		_, err := Apply(plan)
		if !errors.Is(err, ErrPlanStale) {
			t.Fatalf("Apply error = %v, want ErrPlanStale", err)
		}
		var stale *StaleError
		if !errors.As(err, &stale) {
			t.Fatalf("error %v is not a *StaleError", err)
		}
		if stale.LiveSHA != "" {
			t.Fatalf("stale live digest = %q, want empty for an absent destination", stale.LiveSHA)
		}
		if stale.WantSHA != SHA256Hex([]byte("# agents rule\n")) {
			t.Fatalf("stale want digest = %q, want the digest the plan read", stale.WantSHA)
		}
		if got := atomic.LoadInt32(writes); got != 0 {
			t.Fatalf("a refused apply performed %d write(s)", got)
		}
		assertAbsent(t, target)
	})

	t.Run("one stale destination stops the whole run", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		source := fixtureContent()

		// skills deploys two files; mutate the second one only. The first must
		// stay absent, because the refusal happens before the first write.
		plan := sandbox.plan(FeatureSkills, source)
		stale := sandbox.dest(FeatureSkills, "alpha/notes/more.md")
		writeFileRaw(t, stale, []byte("interloper\n"))

		writes := countingDeployWrites(t)
		if _, err := Apply(plan); !errors.Is(err, ErrPlanStale) {
			t.Fatalf("Apply error = %v, want ErrPlanStale", err)
		}
		if got := atomic.LoadInt32(writes); got != 0 {
			t.Fatalf("a refused multi-file apply performed %d write(s)", got)
		}
		assertAbsent(t, sandbox.dest(FeatureSkills, "alpha/SKILL.md"))
		assertAbsent(t, sandbox.manifestPath)
	})
}

// assertStaleRefusal checks the shape of a stale-base refusal.
func assertStaleRefusal(t *testing.T, err error, destination string, live []byte, wantSHA string) {
	t.Helper()
	if err == nil {
		t.Fatal("Apply accepted a stale plan")
	}
	if !errors.Is(err, ErrPlanStale) {
		t.Fatalf("error = %v, want ErrPlanStale", err)
	}
	var stale *StaleError
	if !errors.As(err, &stale) {
		t.Fatalf("error %v is not a *StaleError", err)
	}
	if stale.Destination != destination {
		t.Fatalf("stale destination = %q, want %q", stale.Destination, destination)
	}
	if stale.LiveSHA != SHA256Hex(live) {
		t.Fatalf("stale live digest = %q, want the digest of %q", stale.LiveSHA, string(live))
	}
	if stale.WantSHA != wantSHA {
		t.Fatalf("stale want digest = %q, want %q", stale.WantSHA, wantSHA)
	}
	assertContains(t, "stale error", err.Error(),
		"nothing was written", strconv.Quote(destination), "re-run the plan")
}

// TestApplyRefusesBadManifest pins fail-closed bookkeeping: an unreadable,
// too-new or double-claimed manifest stops the apply before any write.
func TestApplyRefusesBadManifest(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr error
	}{
		{
			name:    "corrupt manifest",
			content: "{ this is not a manifest",
			wantErr: ErrManifestCorrupt,
		},
		{
			name:    "empty manifest",
			content: "",
			wantErr: ErrManifestCorrupt,
		},
		{
			name:    "schema too new",
			content: `{"schema_version": 99, "updated_at": "2026-10-22T03:04:05Z", "entries": {}}`,
			wantErr: ErrManifestSchemaTooNew,
		},
		{
			name:    "one destination claimed twice",
			wantErr: ErrManifestDuplicateDestination,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			sandbox := newDeploySandbox(t)
			content := testCase.content
			if testCase.wantErr == ErrManifestDuplicateDestination {
				// The duplicate only exists once both keys normalize onto one
				// absolute destination, so the fixture is built from the real
				// path this sandbox would deploy.
				destination := sandbox.dest(FeatureRules, "AGENTS.md")
				content = fmt.Sprintf(`{"schema_version":1,"updated_at":"2026-10-22T03:04:05Z","entries":{`+
					`%q:{"path":%q,"sha256":%q,"provenance":"go-applied"},`+
					`%q:{"path":%q,"sha256":%q,"provenance":"go-applied"}}}`,
					destination, destination, sampleSHA,
					destination+" (other key)", destination, sampleSHA)
			}
			writeFileRaw(t, sandbox.manifestPath, []byte(content))
			before := readFileRaw(t, sandbox.manifestPath)

			plan := sandbox.plan(FeatureRules, fixtureContent())
			writes := countingDeployWrites(t)
			_, err := Apply(plan)
			if err == nil {
				t.Fatalf("Apply accepted a %s", testCase.name)
			}
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("error = %v, want %v", err, testCase.wantErr)
			}
			if got := atomic.LoadInt32(writes); got != 0 {
				t.Fatalf("a refused apply performed %d write(s)", got)
			}
			for _, rel := range deploys(FeatureRules) {
				assertAbsent(t, sandbox.dest(FeatureRules, rel))
			}
			assertFileBytes(t, sandbox.manifestPath, before)
		})
	}
}

// TestApplyRefusesInvalidPlan pins the pre-flight refusals: a plan that cannot
// describe the destination partition is rejected before any filesystem work.
func TestApplyRefusesInvalidPlan(t *testing.T) {
	sandbox := newDeploySandbox(t)
	layout, err := LayoutFor(FeatureRules)
	if err != nil {
		t.Fatalf("LayoutFor: %v", err)
	}
	body := []byte("# rule\n")
	bodySHA := SHA256Hex(body)
	inTree := filepath.Join(layout.DestDir, "AGENTS.md")
	outside := filepath.Join(sandbox.home, "elsewhere.md")

	base := func() *DeployPlan {
		return &DeployPlan{
			Feature: FeatureRules,
			Layout:  layout,
			Actions: []PlanAction{{
				Kind:     PlanActionCreate,
				DestPath: inTree,
				RelPath:  "AGENTS.md",
				NewSHA:   bodySHA,
				Data:     body,
			}},
		}
	}

	cases := []struct {
		name string
		plan func() *DeployPlan
		word string
	}{
		{name: "nil plan", plan: func() *DeployPlan { return nil }, word: "nil plan"},
		{name: "layout feature mismatch", plan: func() *DeployPlan {
			broken := base()
			broken.Feature = FeatureSkills
			return broken
		}, word: "does not match"},
		{name: "destination outside the tree", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].DestPath = outside
			return broken
		}, word: "outside"},
		{name: "unknown action", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].Kind = PlanActionKind("clobber")
			return broken
		}, word: "clobber"},
		{name: "create with an old digest", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].OldSHA = sampleSHA
			return broken
		}, word: "old digest"},
		{name: "update without a change", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].Kind = PlanActionUpdate
			broken.Actions[0].OldSHA = bodySHA
			return broken
		}, word: "does not describe a change"},
		{name: "identical with two digests", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].Kind = PlanActionIdentical
			broken.Actions[0].OldSHA = sampleSHA
			broken.Actions[0].Data = nil
			return broken
		}, word: "reports"},
		{name: "content that does not match its digest", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].Data = []byte("# something else\n")
			return broken
		}, word: "digesting to"},
		{name: "destination claimed twice", plan: func() *DeployPlan {
			broken := base()
			second := broken.Actions[0]
			second.RelPath = "AGENTS-again.md"
			broken.Actions = append(broken.Actions, second)
			return broken
		}, word: "claimed twice"},
		{name: "create without content", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].Data = nil
			return broken
		}, word: "no content"},
		{name: "non-absolute destination", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].DestPath = "rules/AGENTS.md"
			return broken
		}, word: "absolute"},
		{name: "digest that is not sha256", plan: func() *DeployPlan {
			broken := base()
			broken.Actions[0].NewSHA = "deadbeef"
			broken.Actions[0].Data = nil
			return broken
		}, word: "deadbeef"},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			writes := countingDeployWrites(t)
			summary, err := Apply(testCase.plan())
			if err == nil {
				t.Fatalf("Apply accepted a %s and returned %+v", testCase.name, summary)
			}
			if !errors.Is(err, ErrPlanInvalid) {
				t.Fatalf("error = %v, want ErrPlanInvalid", err)
			}
			assertContains(t, "refusal", err.Error(), testCase.word)
			if got := atomic.LoadInt32(writes); got != 0 {
				t.Fatalf("a refused plan performed %d write(s)", got)
			}
			assertAbsent(t, inTree)
		})
	}
}

// TestApplyArchivesPreChangeBytesOnUpdate pins the Never-Delete rule for the
// overwrite path: the bytes an update replaces are archived and recorded as the
// entry's backup_ref.
func TestApplyArchivesPreChangeBytesOnUpdate(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureRules, source))

	target := sandbox.dest(FeatureRules, "AGENTS.md")
	original := readFileRaw(t, target)

	changed := fixtureContent()
	changed["rules/AGENTS.md"] = &fstest.MapFile{Data: []byte("# agents rule v2\n")}
	summary := sandbox.apply(sandbox.plan(FeatureRules, changed))

	if summary.Updated != 1 || summary.BackupDir == "" {
		t.Fatalf("apply summary = %+v, want one update with a backup directory", summary)
	}
	var updated ManifestEntry
	for _, entry := range summary.Entries {
		if entry.Path == target {
			updated = entry
		}
	}
	if updated.BackupRef == "" {
		t.Fatalf("the updated entry records no backup_ref: %+v", updated)
	}
	if !strings.HasPrefix(updated.BackupRef, summary.BackupDir) {
		t.Fatalf("backup_ref %q is not inside %q", updated.BackupRef, summary.BackupDir)
	}
	assertFileBytes(t, updated.BackupRef, original)
	assertFileBytes(t, target, []byte("# agents rule v2\n"))

	// The recorded ref survives a reload, so a rollback has something to read.
	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	reloaded, ok := manifest.Entry(target)
	if !ok {
		t.Fatalf("the updated destination is not in the manifest")
	}
	if reloaded.BackupRef != updated.BackupRef {
		t.Fatalf("reloaded backup_ref = %q, want %q", reloaded.BackupRef, updated.BackupRef)
	}
	if reloaded.SHA256 != SHA256Hex([]byte("# agents rule v2\n")) {
		t.Fatalf("reloaded digest = %q, want the new bytes", reloaded.SHA256)
	}
}

// TestApplyRecordsAdoptForIdenticalPreexisting pins the record-only adopt: a
// destination that already holds our bytes is recorded without being written.
func TestApplyRecordsAdoptForIdenticalPreexisting(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	target := sandbox.dest(FeatureRules, "AGENTS.md")
	writeFileRaw(t, target, []byte("# agents rule\n"))
	manifestBefore := modTime(t, target)

	plan := sandbox.plan(FeatureRules, source)
	if _, _, identical := plan.Counts(); identical != 1 {
		t.Fatalf("plan reports %d identical destination(s), want 1", identical)
	}
	writes := countingDeployWrites(t)
	summary := sandbox.apply(plan)
	if got := atomic.LoadInt32(writes); got != 0 {
		t.Fatalf("the adopt apply performed %d write(s)", got)
	}
	if summary.Identical != 1 || summary.Written() != 0 {
		t.Fatalf("apply summary = %+v", summary)
	}
	if got := modTime(t, target); !got.Equal(manifestBefore) {
		t.Fatalf("the adopted file was touched: %v -> %v", manifestBefore, got)
	}

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	entry, ok := manifest.Entry(target)
	if !ok {
		t.Fatal("the adopted destination was not recorded")
	}
	if entry.Provenance != ProvenanceAdopted {
		t.Fatalf("provenance = %q, want %q (the engine did not write those bytes)",
			entry.Provenance, ProvenanceAdopted)
	}
}

// TestRetractPreservesDriftedBytesInBackup pins that retracting an edited file
// archives the edit instead of destroying it, and says so.
func TestRetractPreservesDriftedBytesInBackup(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureRules, source))

	target := sandbox.dest(FeatureRules, "AGENTS.md")
	edited := []byte("the user's own edit\n")
	writeFileRaw(t, target, edited)

	summary, err := Retract(FeatureRules)
	if err != nil {
		t.Fatalf("Retract: %v", err)
	}
	if len(summary.Destinations) != 1 {
		t.Fatalf("retract handled %d destination(s), want 1", len(summary.Destinations))
	}
	result := summary.Destinations[0]
	if !result.Drifted {
		t.Fatalf("retract did not flag the edited destination: %+v", result)
	}
	assertAbsent(t, target)
	assertFileBytes(t, result.BackupPath, edited)
}

// TestRetractLeavesForeignFiles pins that retract removes exactly the
// manifest-owned destinations and nothing else.
func TestRetractLeavesForeignFiles(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureRules, source))

	foreign := filepath.Join(sandbox.configDir, "rules", "someone-elses.md")
	writeFileRaw(t, foreign, []byte("not ours\n"))

	summary, err := Retract(FeatureRules)
	if err != nil {
		t.Fatalf("Retract: %v", err)
	}
	if len(summary.Destinations) != 1 {
		t.Fatalf("retract handled %d destination(s), want only the recorded one", len(summary.Destinations))
	}
	assertAbsent(t, sandbox.dest(FeatureRules, "AGENTS.md"))
	assertFileBytes(t, foreign, []byte("not ours\n"))
}

// TestStatusIsReadOnly pins pick 7 at the Status level: inspecting a machine
// that was never deployed writes nothing at all.
func TestStatusIsReadOnly(t *testing.T) {
	sandbox := newDeploySandbox(t)

	status := sandbox.status(FeatureRules)
	if status.Deployed || len(status.Destinations) != 0 {
		t.Fatalf("status on a fresh machine = %+v", status)
	}
	if !status.OK() {
		t.Fatal("status.OK() = false on a machine with nothing deployed")
	}
	assertAbsent(t, filepath.Join(sandbox.configDir, "rules"))
	assertAbsent(t, sandbox.manifestPath)
}

// TestStatusReportsMissingAndForeign pins the two states a repair verb will
// have to act on.
func TestStatusReportsMissingAndForeign(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureRules, source))

	target := sandbox.dest(FeatureRules, "AGENTS.md")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove %q: %v", target, err)
	}
	status := sandbox.status(FeatureRules)
	if status.Missing != 1 || status.Drifted != 0 {
		t.Fatalf("status = %d missing / %d drifted, want 1 missing", status.Missing, status.Drifted)
	}
	if status.OK() {
		t.Fatal("status.OK() = true with a missing destination")
	}

	writeFileRaw(t, filepath.Join(sandbox.configDir, "rules", "stray.md"), []byte("stray\n"))
	status = sandbox.status(FeatureRules)
	if status.Foreign != 1 {
		t.Fatalf("status.Foreign = %d, want 1: %+v", status.Foreign, status.Destinations)
	}
	var foreign DestinationStatus
	for _, destination := range status.Destinations {
		if destination.State == StateForeign {
			foreign = destination
		}
	}
	if filepath.Base(foreign.Path) != "stray.md" {
		t.Fatalf("foreign destination = %q, want the stray file", foreign.Path)
	}
	if foreign.WantSHA != "" || foreign.Provenance != "" {
		t.Fatalf("a foreign destination carries manifest metadata: %+v", foreign)
	}
	if foreign.LiveSHA != SHA256Hex([]byte("stray\n")) {
		t.Fatalf("foreign live digest = %q", foreign.LiveSHA)
	}
}

// TestStatusFailsClosedOnBadManifest pins that a broken manifest is reported,
// not read as "nothing deployed".
func TestStatusFailsClosedOnBadManifest(t *testing.T) {
	sandbox := newDeploySandbox(t)
	writeFileRaw(t, sandbox.manifestPath, []byte("{ truncated"))

	if _, err := Status(FeatureRules); !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Status error = %v, want ErrManifestCorrupt", err)
	}
	if _, err := Retract(FeatureRules); !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("Retract error = %v, want ErrManifestCorrupt", err)
	}
	// The unreadable manifest is left exactly as it was.
	assertFileBytes(t, sandbox.manifestPath, []byte("{ truncated"))
}

// TestStatusRefusesFeaturesItDoesNotOwn pins the layout boundary from the
// status side.
func TestStatusRefusesFeaturesItDoesNotOwn(t *testing.T) {
	newDeploySandbox(t)
	if _, err := Status(FeatureStatusline); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("Status(statusline) error = %v, want ErrNoContentLayout", err)
	}
	if _, err := Retract(FeatureStatusline); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("Retract(statusline) error = %v, want ErrNoContentLayout", err)
	}
}

// TestPlanLines pins the diff-shaped output T5/T8 render, including the masking
// the brief requires: every path in a line goes through providers.MaskOperand.
func TestPlanLines(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()

	fresh := sandbox.plan(FeatureSkills, source)
	lines := fresh.Lines()
	if len(lines) != len(fresh.Actions) {
		t.Fatalf("Lines() returned %d line(s) for %d action(s)", len(lines), len(fresh.Actions))
	}
	for index, action := range fresh.Actions {
		masked := providers.MaskOperand(action.DestPath)
		if !strings.HasPrefix(lines[index], "create     ") {
			t.Fatalf("line %d = %q, want a create line", index, lines[index])
		}
		if !strings.Contains(lines[index], masked) {
			t.Fatalf("line %d = %q, want it to carry the masked destination %q", index, lines[index], masked)
		}
	}

	sandbox.apply(fresh)
	after := sandbox.plan(FeatureSkills, source).Lines()
	for _, line := range after {
		if !strings.HasPrefix(line, "identical  ") {
			t.Fatalf("line = %q, want an identical line", line)
		}
	}

	changed := fixtureContent()
	changed["skills/alpha/SKILL.md"] = &fstest.MapFile{Data: []byte("# alpha skill v2\n")}
	update := sandbox.plan(FeatureSkills, changed)
	updateLines := update.Lines()
	var sawUpdate bool
	for _, line := range updateLines {
		if !strings.HasPrefix(line, "update     ") {
			continue
		}
		sawUpdate = true
		if !strings.Contains(line, " -> ") {
			t.Fatalf("update line = %q, want old and new digests", line)
		}
		if !strings.Contains(line, providers.MaskOperand(sandbox.dest(FeatureSkills, "alpha/SKILL.md"))) {
			t.Fatalf("update line = %q, want the changed destination", line)
		}
	}
	if !sawUpdate {
		t.Fatalf("no update line in %v", updateLines)
	}

	var nilPlan *DeployPlan
	if got := nilPlan.Lines(); got != nil {
		t.Fatalf("nil plan Lines() = %v, want nil", got)
	}
	if created, updated, identical := nilPlan.Counts(); created != 0 || updated != 0 || identical != 0 {
		t.Fatal("nil plan Counts() is not zero")
	}
	if got := nilPlan.Writes(); got != 0 {
		t.Fatalf("nil plan Writes() = %d", got)
	}
	var nilSummary *ApplySummary
	if got := nilSummary.Written(); got != 0 {
		t.Fatalf("nil summary Written() = %d", got)
	}
	var nilRetract *RetractSummary
	if got := nilRetract.BackedUp(); got != 0 {
		t.Fatalf("nil retract BackedUp() = %d", got)
	}
	var nilStatus *FeatureStatus
	if !nilStatus.OK() {
		t.Fatal("nil status OK() = false")
	}
	var nilStale *StaleError
	if got := nilStale.Error(); got == "" {
		t.Fatal("nil StaleError rendered an empty message")
	}
	if err := nilStale.Unwrap(); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("nil StaleError Unwrap() = %v", err)
	}
}

// TestApplyWriteFailureStopsTheRun pins the write-error path: a destination
// that cannot be written is reported, and the bookkeeping is not saved, so the
// manifest never claims a file that is not there.
func TestApplyWriteFailureStopsTheRun(t *testing.T) {
	t.Run("the first write fails", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		plan := sandbox.plan(FeatureRules, fixtureContent())

		previous := deployWriteFile
		deployWriteFile = func(path string, data []byte, perm os.FileMode) error {
			return errors.New("the disk is full")
		}
		t.Cleanup(func() { deployWriteFile = previous })

		summary, err := Apply(plan)
		if err == nil {
			t.Fatalf("Apply accepted a failing writer and returned %+v", summary)
		}
		if !errors.Is(err, ErrDeployWrite) {
			t.Fatalf("error = %v, want ErrDeployWrite", err)
		}
		assertContains(t, "write failure", err.Error(), "the disk is full")
		if summary == nil || summary.Written() != 0 {
			t.Fatalf("summary = %+v, want zero written destinations", summary)
		}
		assertAbsent(t, sandbox.manifestPath)
		status := sandbox.status(FeatureRules)
		if status.Deployed || !status.OK() {
			t.Fatalf("status after a failed write = %+v", status)
		}
	})

	t.Run("the second write fails after the first landed", func(t *testing.T) {
		sandbox := newDeploySandbox(t)
		plan := sandbox.plan(FeatureSkills, fixtureContent())

		var calls int32
		previous := deployWriteFile
		deployWriteFile = func(path string, data []byte, perm os.FileMode) error {
			if atomic.AddInt32(&calls, 1) == 1 {
				return previous(path, data, perm)
			}
			return errors.New("the disk is full")
		}
		t.Cleanup(func() { deployWriteFile = previous })

		summary, err := Apply(plan)
		if !errors.Is(err, ErrDeployWrite) {
			t.Fatalf("error = %v, want ErrDeployWrite", err)
		}
		assertContains(t, "partial failure", err.Error(),
			"1 destination(s) were written before the failure", "the disk is full")
		if summary.Written() != 1 {
			t.Fatalf("summary reports %d written destination(s), want 1", summary.Written())
		}
		assertAbsent(t, sandbox.manifestPath)

		// The half-written tree is visible as drift rather than silently
		// claimed: the first file is on disk, no record says so, and the repair
		// is T8's verb, not something this engine does on its own.
		status := sandbox.status(FeatureSkills)
		if status.Foreign != 1 || status.Deployed {
			t.Fatalf("status after a partial apply = %+v, want one foreign file and no records", status)
		}
		assertFileBytes(t, sandbox.dest(FeatureSkills, "alpha/SKILL.md"), []byte("# alpha skill\n"))
	})
}

// TestApplyManifestSetRefusalCarriesThePartialAnnotation pins F-S9A-6: a manifest
// this build did not write can carry an unknown provenance on a destination that
// already holds the engine's bytes. The load side tolerates that provenance and
// the save side refuses it, so the run reaches manifest.Set inside the loop - and
// a refusal there used to return the bare error even though earlier destinations
// had already landed. It now carries the same "N destination(s) were written"
// annotation as the archive and write failure paths.
func TestApplyManifestSetRefusalCarriesThePartialAnnotation(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fstest.MapFS{
		"skills/alpha/SKILL.md": {Data: []byte("# alpha skill\n")},
		"skills/bruno/SKILL.md": {Data: []byte("# bruno skill\n")},
	}

	// Plan once against the empty tree to learn the plan's order, then make the
	// SECOND destination identical on disk and record it with a stale digest and a
	// provenance this build refuses.
	first := sandbox.plan(FeatureSkills, source)
	if len(first.Actions) != 2 {
		t.Fatalf("plan actions = %d, want 2", len(first.Actions))
	}
	held := first.Actions[1]
	heldBytes, err := fs.ReadFile(source, "skills/"+held.RelPath)
	if err != nil {
		t.Fatalf("read the held destination's source: %v", err)
	}
	writeFileRaw(t, held.DestPath, heldBytes)

	staleSHA := strings.Repeat("a", 64)
	handwritten := fmt.Sprintf(`{"schema_version":%d,"entries":{%q:{"path":%q,"sha256":%q,"provenance":"handwritten"}}}`,
		CurrentManifestSchemaVersion, held.DestPath, held.DestPath, staleSHA) + "\n"
	writeFileRaw(t, sandbox.manifestPath, []byte(handwritten))

	plan := sandbox.plan(FeatureSkills, source)
	if plan.Actions[1].Kind != PlanActionIdentical {
		t.Fatalf("action 1 = %q, want the destination to be identical so the loop reaches Set", plan.Actions[1].Kind)
	}

	summary, applyErr := Apply(plan)
	if applyErr == nil {
		t.Fatalf("Apply accepted a manifest entry it must refuse on save; summary = %+v", summary)
	}
	if !errors.Is(applyErr, ErrManifestInvalid) {
		t.Fatalf("error = %v, want it to wrap ErrManifestInvalid", applyErr)
	}
	assertContains(t, "manifest refusal", applyErr.Error(),
		"1 destination(s) were written before the failure", "provenance", "handwritten")
	if summary == nil || summary.Written() != 1 {
		t.Fatalf("summary = %+v, want exactly one written destination", summary)
	}
	// The bookkeeping is deliberately not saved, so the file this build refuses to
	// read back is still byte-identical to what the run found.
	unchanged, err := os.ReadFile(sandbox.manifestPath)
	if err != nil {
		t.Fatalf("read the manifest: %v", err)
	}
	if string(unchanged) != handwritten {
		t.Fatalf("the refused run rewrote the manifest:\n got: %s\nwant: %s", unchanged, handwritten)
	}
	assertFileBytes(t, plan.Actions[0].DestPath, plan.Actions[0].Data)
}

// TestRetractRecordsMissingDestinations pins the branch a user creates by
// deleting a deployed file by hand: retract reports it as missing, removes the
// record, and allocates no backup directory.
func TestRetractRecordsMissingDestinations(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()
	sandbox.apply(sandbox.plan(FeatureSkills, source))

	want := deploys(FeatureSkills)
	for _, rel := range want {
		if err := os.Remove(sandbox.dest(FeatureSkills, rel)); err != nil {
			t.Fatalf("remove %q: %v", rel, err)
		}
	}

	summary, err := Retract(FeatureSkills)
	if err != nil {
		t.Fatalf("Retract: %v", err)
	}
	if len(summary.Destinations) != len(want) {
		t.Fatalf("retract handled %d destination(s), want %d", len(summary.Destinations), len(want))
	}
	if summary.BackedUp() != 0 {
		t.Fatalf("retract reported %d moved destination(s), want 0", summary.BackedUp())
	}
	if summary.BackupDir != "" {
		t.Fatalf("retract allocated a backup directory %q with nothing to move", summary.BackupDir)
	}
	for _, result := range summary.Destinations {
		if !result.Missing {
			t.Fatalf("destination %q is not reported as missing: %+v", result.DestPath, result)
		}
		if result.BackupPath != "" {
			t.Fatalf("missing destination %q reports backup path %q", result.DestPath, result.BackupPath)
		}
		if result.Drifted {
			t.Fatalf("missing destination %q is reported as drifted", result.DestPath)
		}
	}
	if entries := sandbox.entriesFor(FeatureSkills); len(entries) != 0 {
		t.Fatalf("retract left %d record(s) for missing destinations", len(entries))
	}
}

// TestPlanHookPrefersTheContentSource pins the P5-3 T7 precedence on the deploy
// path: with a content source in hand the hook plan reads THAT source (the
// embedded snapshot in production), even when a toolkit root marker resolves to
// a hooks.json with different bytes. Ambient installed state must never win over
// the source the caller passed.
func TestPlanHookPrefersTheContentSource(t *testing.T) {
	sandbox := newDeploySandbox(t)
	clearToolkitEnv(t)
	writeMarker(t, hostMarkerPath(sandbox.home), sandbox.toolkitRoot+"\r\n")
	const toolkitOnlyJSON = "{\"hooks\":{\"PostInvocation\":[{\"command\":\"toolkit-root-copy\"}]}}\n"
	writeFileRaw(t, filepath.Join(sandbox.toolkitRoot, "hooks", hookSourceFile), []byte(toolkitOnlyJSON))

	plan, err := Plan(FeatureHook, fixtureContent())
	if err != nil {
		t.Fatalf("Plan(hook): %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("hook plan has %d action(s), want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	want := filepath.Join(sandbox.configDir, "hooks", hookSourceFile)
	if action.DestPath != want {
		t.Fatalf("hook destination = %q, want %q", action.DestPath, want)
	}
	if action.NewSHA != SHA256Hex([]byte(hookFixtureJSON)) {
		t.Fatalf("hook digest = %q, want the digest of the CONTENT SOURCE's hooks.json (the toolkit root must not win)", action.NewSHA)
	}

	summary := sandbox.apply(plan)
	if summary.Created != 1 {
		t.Fatalf("hook apply created %d destination(s), want 1", summary.Created)
	}
	assertFileBytes(t, want, []byte(hookFixtureJSON))
}

// TestPlanHookFallsBackToToolkitRootMarker pins the fallback half of the same
// rule: a caller with NO content source still deploys hooks/hooks.json from the
// toolkit root named by the root marker (and not from AGY_PRO_TOOLS_ROOT, which
// is unset here). This is the Node-era install path.
func TestPlanHookFallsBackToToolkitRootMarker(t *testing.T) {
	sandbox := newDeploySandbox(t)
	clearToolkitEnv(t)
	writeMarker(t, hostMarkerPath(sandbox.home), sandbox.toolkitRoot+"\r\n")

	plan, err := Plan(FeatureHook, nil)
	if err != nil {
		t.Fatalf("Plan(hook, nil): %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("hook plan has %d action(s), want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Kind != PlanActionCreate {
		t.Fatalf("hook action = %q, want create", action.Kind)
	}
	want := filepath.Join(sandbox.configDir, "hooks", hookSourceFile)
	if action.DestPath != want {
		t.Fatalf("hook destination = %q, want %q", action.DestPath, want)
	}
	if action.NewSHA != SHA256Hex([]byte(hookFixtureJSON)) {
		t.Fatalf("hook digest = %q, want the digest of the toolkit's hooks.json", action.NewSHA)
	}

	summary := sandbox.apply(plan)
	if summary.Created != 1 {
		t.Fatalf("hook apply created %d destination(s), want 1", summary.Created)
	}
	assertFileBytes(t, want, []byte(hookFixtureJSON))
}

// TestPlanRequiresContentSourceForTrees pins the content-source contract: the
// tree features have no runtime read of their own, so a nil source is a loud
// refusal rather than an empty deployment.
func TestPlanRequiresContentSourceForTrees(t *testing.T) {
	newDeploySandbox(t)
	for _, feature := range []Feature{FeatureRules, FeatureSkills, FeaturePlugins} {
		if _, err := Plan(feature, nil); !errors.Is(err, ErrNoContentSource) {
			t.Fatalf("Plan(%q, nil) error = %v, want ErrNoContentSource", feature, err)
		}
	}
}

// TestApplySerializesConcurrentRuns pins the package-level mutex: concurrent
// applies load-modify-save one manifest, and without serialization the last
// writer would silently drop the others' entries.
func TestApplySerializesConcurrentRuns(t *testing.T) {
	sandbox := newDeploySandbox(t)
	source := fixtureContent()

	features := []Feature{FeatureRules, FeatureSkills, FeaturePlugins}
	plans := make([]*DeployPlan, 0, len(features))
	wantEntries := 0
	for _, feature := range features {
		plans = append(plans, sandbox.plan(feature, source))
		wantEntries += len(deploys(feature))
	}

	var wait sync.WaitGroup
	errs := make(chan error, len(plans))
	for _, plan := range plans {
		plan := plan
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := Apply(plan); err != nil {
				errs <- err
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Apply: %v", err)
	}

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.Len() != wantEntries {
		t.Fatalf("manifest records %d destination(s), want %d: a lost update", manifest.Len(), wantEntries)
	}
	for _, feature := range features {
		if entries := sandbox.entriesFor(feature); len(entries) != len(deploys(feature)) {
			t.Fatalf("%q records %d destination(s), want %d", feature, len(entries), len(deploys(feature)))
		}
	}
}

// TestBackupTargetMirrorsTheTree pins the collision rule: two files with one
// base name in different subtrees must not land on one backup path.
func TestBackupTargetMirrorsTheTree(t *testing.T) {
	layout, err := LayoutFor(FeatureSkills)
	if err != nil {
		t.Fatalf("LayoutFor: %v", err)
	}
	backupDir := t.TempDir()

	first, err := backupTarget(backupDir, layout.DestDir, filepath.Join(layout.DestDir, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("backupTarget(alpha): %v", err)
	}
	second, err := backupTarget(backupDir, layout.DestDir, filepath.Join(layout.DestDir, "beta", "SKILL.md"))
	if err != nil {
		t.Fatalf("backupTarget(beta): %v", err)
	}
	if first == second {
		t.Fatalf("two SKILL.md files share the backup path %q", first)
	}
	if want := filepath.Join(backupDir, "alpha", "SKILL.md"); first != want {
		t.Fatalf("backupTarget = %q, want %q", first, want)
	}
	if _, err := backupTarget(backupDir, layout.DestDir, filepath.Join(layout.DestDir, "..", "escape.md")); err == nil {
		t.Fatal("backupTarget accepted a destination outside the feature root")
	}
	if _, err := backupTarget("", layout.DestDir, filepath.Join(layout.DestDir, "x.md")); err == nil {
		t.Fatal("backupTarget accepted an unallocated backup directory")
	}
}

// TestUniqueBackupDir pins the same-second suffix mode.go uses.
func TestUniqueBackupDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "20261022_030405")
	if got := uniqueBackupDir(base); got != base {
		t.Fatalf("uniqueBackupDir on a free path = %q, want %q", got, base)
	}
	writeFileRaw(t, filepath.Join(base, "AGENTS.md"), []byte("x"))
	if got, want := uniqueBackupDir(base), base+"_2"; got != want {
		t.Fatalf("uniqueBackupDir with one taken = %q, want %q", got, want)
	}
	writeFileRaw(t, filepath.Join(base+"_2", "AGENTS.md"), []byte("x"))
	if got, want := uniqueBackupDir(base), base+"_3"; got != want {
		t.Fatalf("uniqueBackupDir with two taken = %q, want %q", got, want)
	}
}

// TestPruneEmptyDirs pins that the prune removes empty directories only.
func TestPruneEmptyDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	writeFileRaw(t, filepath.Join(root, "alpha", "SKILL.md"), []byte("x"))
	pruneEmptyDirs(root)
	if _, err := os.Stat(filepath.Join(root, "alpha", "SKILL.md")); err != nil {
		t.Fatalf("prune removed a directory that still holds a file: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "alpha", "SKILL.md")); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}
	pruneEmptyDirs(root)
	assertAbsent(t, root)
	pruneEmptyDirs(root) // idempotent on an absent root
}
