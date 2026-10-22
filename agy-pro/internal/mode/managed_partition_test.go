package mode

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"agypro/internal/integrations"
	"agypro/internal/paths"
	"agypro/profiles"
)

// managedPartitionSandbox points the agy-pro home and the Gemini config
// directory at two fresh temp roots and returns the config dir. Two separate
// roots are deliberate: the engine's manifest/backups live under the agy-pro
// home and its destinations under the config dir, so a test that mixed them
// would still pass while proving nothing about the boundary.
func managedPartitionSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, filepath.Join(root, "agy-pro"))
	configDir := filepath.Join(root, "gemini-config")
	t.Setenv(paths.EnvGeminiConfigDir, configDir)
	return configDir
}

// backupFilesNamed returns every path below ~/.agy-pro/backups whose base name
// is name. It is how the collision proof shows that a mode switch did not MOVE
// an integrations-owned file into a backup.
func backupFilesNamed(t *testing.T, name string) []string {
	t.Helper()
	backups, err := paths.AgyProBackups()
	if err != nil {
		t.Fatalf("AgyProBackups: %v", err)
	}
	var found []string
	walkErr := filepath.WalkDir(backups, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk backups %q: %v", backups, walkErr)
	}
	return found
}

// TestManagedSetRetractionLeavesIntegrationsOwnedTrees is the P5-3 collision
// proof for the destination partition (pick record items 1 and 5). It walks the
// exact sequence the partition is meant to survive:
//
//	(a) the integrations engine applies its rules feature, so a file the engine
//	    owns lives under <config>/rules and is recorded in its manifest;
//	(b) mode.Switch runs clean -> full -> clean with the engine not involved at
//	    all (nothing calls integrations between the switches);
//	(c) the engine's file is still there byte for byte, was never moved into a
//	    mode backup, and the engine still reports that destination clean.
//
// (c) is the collision proof proper: rules left DefaultManagedItems in P5-3 T3,
// and P5-3 T7 trimmed the deploy walk source (profiles/full and profiles/clean
// no longer carry rules/, skills/ or plugins/), so neither half of a switch - the
// retraction AND the deploy - can reach the engine's tree. Before the partition a
// switch would MOVE the whole rules tree into a timestamped backup and then lay
// the profile's own rules down in its place: the hybrid merge-vs-replace damage
// class this test exists to keep dead.
//
// T7 closed the interim gap this test used to observe. It now asserts the two
// things the TODO carried: the engine reports ZERO foreign files inside the tree
// it owns, and the tree on disk holds EXACTLY the engine's file set.
//
// NOT covered here: the restore door. mode.Restore copies back only the backup
// entries the managed register owns (SESSION9 T9 narrowing), so a LEGACY backup
// that still holds a rules/ tree (every backup taken before this partition does)
// can no longer overwrite an engine-owned file - that contract is
// TestRestoreNarrowedToManagedRegister's subject, and this test stays about the
// managed-set retraction and the deploy walk.
func TestManagedSetRetractionLeavesIntegrationsOwnedTrees(t *testing.T) {
	configDir := managedPartitionSandbox(t)

	// (a) The engine owns a rules destination. The content is a fake fs.FS: the
	// proof is about OWNERSHIP, not about where T7 will read content from.
	const ownedRel = "engine-owned.md"
	ownedBytes := []byte("integrations-owned rules content\n")
	content := fstest.MapFS{
		"rules/" + ownedRel: &fstest.MapFile{Data: ownedBytes, Mode: 0o644},
	}
	plan, err := integrations.Plan(integrations.FeatureRules, content)
	if err != nil {
		t.Fatalf("integrations.Plan(rules): %v", err)
	}
	if got := plan.Writes(); got != 1 {
		t.Fatalf("plan writes %d destinations; want 1 (actions=%v)", got, plan.Lines())
	}
	summary, err := integrations.Apply(plan)
	if err != nil {
		t.Fatalf("integrations.Apply(rules): %v", err)
	}
	if summary.Created != 1 || summary.Updated != 0 {
		t.Fatalf("apply summary = %+v; want one created destination", summary)
	}

	ownedPath := filepath.Join(configDir, "rules", ownedRel)
	if got, err := os.ReadFile(ownedPath); err != nil || !bytes.Equal(got, ownedBytes) {
		t.Fatalf("the engine's destination was not laid down: %v (%q)", err, string(got))
	}
	before, err := integrations.Status(integrations.FeatureRules)
	if err != nil {
		t.Fatalf("integrations.Status(rules) before the switches: %v", err)
	}
	if !before.OK() || !before.Deployed {
		t.Fatalf("the engine does not report a clean deployment before the switches: %+v", before)
	}

	// (b) Three profile switches, engine untouched.
	var out bytes.Buffer
	for _, target := range []string{ModeClean, ModeFull, ModeClean} {
		out.Reset()
		if err := Switch(target, nil, &out); err != nil {
			t.Fatalf("Switch(%s) with an integrations-owned tree present: %v", target, err)
		}
		if !bytes.Contains(out.Bytes(), []byte("[SUCCESS]")) {
			t.Fatalf("Switch(%s) did not report success: %s", target, out.String())
		}
	}

	// (c) The managed layer never touched the engine's destination.
	after, err := os.ReadFile(ownedPath)
	if err != nil {
		t.Fatalf("the mode switches removed the integrations-owned file %q: %v", ownedPath, err)
	}
	if !bytes.Equal(after, ownedBytes) {
		t.Fatalf("the mode switches rewrote the integrations-owned file: got %q want %q", string(after), string(ownedBytes))
	}
	if hits := backupFilesNamed(t, ownedRel); len(hits) != 0 {
		t.Fatalf("a mode switch moved the integrations-owned file into %d backup location(s): %v", len(hits), hits)
	}
	post, err := integrations.Status(integrations.FeatureRules)
	if err != nil {
		t.Fatalf("integrations.Status(rules) after the switches: %v", err)
	}
	if !post.Deployed {
		t.Fatal("the engine no longer records the rules feature after the switches")
	}
	if post.Drifted != 0 || post.Missing != 0 {
		t.Fatalf("the engine reports drifted/missing destinations after the switches (%d/%d): %+v",
			post.Drifted, post.Missing, post.Destinations)
	}
	if post.Clean != before.Clean || post.Clean == 0 {
		t.Fatalf("the engine's clean destination count moved across the switches: %d -> %d", before.Clean, post.Clean)
	}
	var own integrations.DestinationState
	for _, destination := range post.Destinations {
		if filepath.Clean(destination.Path) == filepath.Clean(ownedPath) {
			own = destination.State
		}
	}
	if own != integrations.StateClean {
		t.Fatalf("the engine's own destination reports %q after the switches; want %q", own, integrations.StateClean)
	}
	// The walk source is trimmed (T7), so nothing the profile ships can land
	// inside the engine's tree: zero foreign files is the observable form of "the
	// two owners never meet on disk".
	if post.Foreign != 0 {
		t.Fatalf("the mode walk left %d foreign file(s) inside the engine's rules tree: %+v",
			post.Foreign, post.Destinations)
	}
	// Stronger half: the tree on disk holds EXACTLY the engine's file set. The
	// listing comes from the filesystem, not from the engine's records, so a file
	// the engine never recorded cannot hide behind a clean Status.
	var onDisk []string
	walkErr := filepath.WalkDir(filepath.Dir(ownedPath), func(current string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(filepath.Dir(ownedPath), current)
		if relErr != nil {
			return relErr
		}
		onDisk = append(onDisk, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk the engine's rules tree: %v", walkErr)
	}
	sort.Strings(onDisk)
	if want := []string{ownedRel}; strings.Join(onDisk, ",") != strings.Join(want, ",") {
		t.Fatalf("the engine's rules tree holds %v after three profile switches; want exactly %v "+
			"(a profile file inside it means the mode walk is still laying integrations-owned content)", onDisk, want)
	}
}

// TestProfileWalkLeavesIntegrationsOwnedTreesAlone is the post-T7 inverse of the
// interim test it replaces: the embedded full and clean profiles no longer carry
// rules/, skills/ or plugins/ at all, so Switch's deploy walk cannot lay a single
// byte inside a tree the integrations engine owns. Three assertions, in the order
// a regression would surface:
//
//	(1) the trim is at the SOURCE - both profile filesystems have no such
//	    directories, so the invariant does not depend on a walk filter;
//	(2) a switch still deploys the trees mode DOES own (agents), so the test
//	    cannot pass by deploying nothing;
//	(3) every integrations-owned tree on disk holds exactly the files that were
//	    put there before the switches - no profile AGENTS.md, agents-index.md,
//	    GEMINI.md, skills README.md or plugins README.md.
func TestProfileWalkLeavesIntegrationsOwnedTreesAlone(t *testing.T) {
	// (1) The trim lives in the embed, not in the walk.
	for _, profile := range []string{ModeFull, ModeClean} {
		profileFS, err := profiles.GetProfileFS(profile)
		if err != nil {
			t.Fatalf("GetProfileFS(%s): %v", profile, err)
		}
		for _, tree := range []string{"rules", "skills", "plugins"} {
			if _, err := fs.Stat(profileFS, tree); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the embedded %s profile still carries %q (err = %v); the P5-3 T7 trim moved that tree to "+
					"agy-pro/profiles/tools/ and it is the integrations engine's to deploy", profile, tree, err)
			}
		}
	}

	configDir := managedPartitionSandbox(t)

	// An engine-owned file in each of the three trees, laid before any switch.
	engineFiles := map[string]string{
		"rules/engine-owned.md":   "engine-rules",
		"skills/engine-owned.md":  "engine-skills",
		"plugins/engine-owned.md": "engine-plugins",
	}
	for rel, body := range engineFiles {
		full := filepath.Join(configDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	for _, target := range []string{ModeFull, ModeClean, ModeFull} {
		out.Reset()
		if err := Switch(target, nil, &out); err != nil {
			t.Fatalf("Switch(%s): %v", target, err)
		}

		// (2) Mode-owned content still lands: the walk did not simply stop working.
		if _, err := os.Stat(filepath.Join(configDir, "agents", "architect", "AGENT.md")); err != nil && target == ModeFull {
			t.Fatalf("Switch(full) deployed no agents/architect/AGENT.md: %v", err)
		}
		if _, err := os.Stat(filepath.Join(configDir, "profile.json")); err != nil {
			t.Fatalf("Switch(%s) deployed no profile.json: %v", target, err)
		}

		// (3) Each engine tree holds exactly the engine's file.
		for tree := range engineFiles {
			treeName := filepath.ToSlash(filepath.Dir(tree))
			root := filepath.Join(configDir, filepath.FromSlash(treeName))
			var onDisk []string
			walkErr := filepath.WalkDir(root, func(current string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(root, current)
				if relErr != nil {
					return relErr
				}
				onDisk = append(onDisk, filepath.ToSlash(rel))
				return nil
			})
			if walkErr != nil {
				t.Fatalf("walk %q after Switch(%s): %v", root, target, walkErr)
			}
			sort.Strings(onDisk)
			if want := []string{"engine-owned.md"}; strings.Join(onDisk, ",") != strings.Join(want, ",") {
				t.Fatalf("after Switch(%s) the integrations-owned %q tree holds %v; want exactly %v "+
					"(the mode walk is laying profile content into an engine-owned destination)", target, treeName, onDisk, want)
			}
		}
	}
}
