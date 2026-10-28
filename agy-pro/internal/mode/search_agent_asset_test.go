package mode

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/profiles"
)

// searchAgentAssetRel is the native-search child's asset path relative to the
// config directory and to each embedded profile root. The agent name is repeated
// here rather than imported: internal/fanout imports this package for the state
// and mode vocabulary, so an import back (even test-only) would be a cycle. The
// two spellings are kept honest by TestSearchAgentAssetIsProfileContent, which
// compares the profile copies to the repo asset the fanout package reads.
const searchAgentAssetRel = "agents/agypro-search/AGENT.md"

// searchAgentInAgentsTree is the same path relative to the agents tree itself,
// the root agentFilesUnder reports against.
const searchAgentInAgentsTree = "agypro-search/AGENT.md"

// agentFilesUnder returns the slash-separated file paths below root, sorted, so
// a test can assert the exact shape of a deployed tree.
func agentFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", root, err)
	}
	return files
}

// repoSearchAgentAsset reads the repo copy the installer deploys from, which is
// the content all three committed copies must share.
func repoSearchAgentAsset(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "assets", "agents", "agypro-search", "AGENT.md"))
	if err != nil {
		t.Fatalf("read the repo asset (the installer's deploy source): %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatal("the repo asset is empty; an empty asset would deploy a child with no tool restriction")
	}
	return data
}

// TestSearchAgentAssetIsProfileContent pins the P33 half of the native-search
// restriction: `agy pro mode` owns the config agents tree, so an asset that lived
// only in the installer's deploy step disappeared on the next switch and the
// restricted route silently degraded to the full-agent fallback. The asset is
// therefore PROFILE CONTENT of both profiles, byte-identical to the repo source.
//
// Why content in BOTH profiles rather than a second deploy step: the destination
// partition in this package ("ownership is partitioned by DESTINATION", mode.go
// DefaultManagedItems) gives agents/ to the mode layer alone, and the mode
// layer's deploy mechanism for a mode-owned destination IS the embedded profile
// walk. A separate walker, or an engine-side write at run time, would make two
// owners for one path - the merge-vs-replace damage class the partition exists to
// prevent. Clean mode ships exactly this one agent: the asset is feature plumbing
// for the native-search child, not a profile agent, and TestFullCleanRoundTripNoResidue
// pins that the rest of the agents tree still leaves on a clean switch.
func TestSearchAgentAssetIsProfileContent(t *testing.T) {
	repoAsset := repoSearchAgentAsset(t)
	for _, profile := range []string{ModeFull, ModeClean} {
		profileFS, err := profiles.GetProfileFS(profile)
		if err != nil {
			t.Fatalf("GetProfileFS(%s): %v", profile, err)
		}
		embedded, err := fs.ReadFile(profileFS, searchAgentAssetRel)
		if err != nil {
			t.Fatalf("the embedded %s profile does not ship %s: %v (a switch would retract the asset from the config dir)", profile, searchAgentAssetRel, err)
		}
		if !bytes.Equal(embedded, repoAsset) {
			t.Fatalf("the %s profile copy of %s drifted from the repo asset the installer deploys (embedded %d bytes, repo %d bytes); all three copies are ONE content, not three sources",
				profile, searchAgentAssetRel, len(embedded), len(repoAsset))
		}
	}
}

// TestSearchAgentAssetSurvivesEveryModeSwitch is the survival pin the P32 asset
// lacked: switching profiles must not remove the native-search route. Temp roots
// only (paths.EnvAgyProHome + paths.EnvGeminiConfigDir through
// managedPartitionSandbox), so no real ~/.gemini/config is touched.
//
// Both outcomes are pinned because they differ by design:
//   - full: the profile roster plus the asset.
//   - clean: the profile roster is retracted (moved to the switch backup, never
//     deleted) and the asset alone remains - the restricted route stays reachable
//     in the minimal profile instead of silently falling back to the full agent
//     child.
func TestSearchAgentAssetSurvivesEveryModeSwitch(t *testing.T) {
	configDir := managedPartitionSandbox(t)
	repoAsset := repoSearchAgentAsset(t)
	deployed := filepath.Join(configDir, filepath.FromSlash(searchAgentAssetRel))

	var out bytes.Buffer
	for _, target := range []string{ModeFull, ModeClean, ModeFull} {
		out.Reset()
		if err := Switch(target, nil, &out); err != nil {
			t.Fatalf("Switch(%s): %v", target, err)
		}
		got, err := os.ReadFile(deployed)
		if err != nil {
			t.Fatalf("after Switch(%s) the native-search asset is gone from %q: %v (the restricted route falls back to the full agent child)", target, deployed, err)
		}
		if !bytes.Equal(got, repoAsset) {
			t.Fatalf("after Switch(%s) the deployed asset differs from the repo source (%d vs %d bytes)", target, len(got), len(repoAsset))
		}
		tree := agentFilesUnder(t, filepath.Join(configDir, "agents"))
		switch target {
		case ModeClean:
			if strings.Join(tree, ",") != searchAgentInAgentsTree {
				t.Fatalf("after Switch(clean) the agents tree = %v; want exactly %q (the clean profile retracts its roster but keeps the one feature asset)", tree, searchAgentInAgentsTree)
			}
		case ModeFull:
			if len(tree) < 2 || !strings.Contains(strings.Join(tree, ","), "architect/AGENT.md") {
				t.Fatalf("after Switch(full) the agents tree = %v; want the profile roster redeployed alongside the asset", tree)
			}
		}
	}
}
