package mode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"agypro/internal/integrations"
	"agypro/internal/paths"
)

// TestRestoreNarrowedToManagedRegister is the T9 flip of the guard that pinned
// the restore door's overlay behaviour (it was TestRestoreOverlaysLegacyIntegrationsTrees
// when P5-3 T7 §8 surfaced the decision; T7 decision 1 and the SESSION9 ruling
// resolved it toward narrowing, so this test now asserts the narrowed contract).
//
// The restore door derives BOTH halves from the managed register:
//
//   - the move-aside half moves the live register-owned entries into a
//     pre-restore backup and touches nothing else;
//   - the copy-back half restores ONLY the snapshot entries the register still
//     lists. A backup taken before the destination partition also carries a
//     rules/ tree (and, on older snapshots, skills/ and plugins/), which the
//     integrations engine owns now. Those entries are left where they are and
//     named in the transcript, never re-laid over an engine destination.
//
// The hazard the narrowing closes: re-laying rules/ used to OVERWRITE the
// engine's file with stale snapshot bytes, after which the engine reported
// exactly one drifted destination that nothing repaired (correctly: a launch
// must never write). The assertions below are the mirror image, in the order a
// regression would surface.
func TestRestoreNarrowedToManagedRegister(t *testing.T) {
	configDir := managedPartitionSandbox(t)

	// The engine owns a rules destination, applied through the real engine so the
	// manifest records it.
	const ownedRel = "engine-owned.md"
	engineBytes := []byte("engine-owned content\n")
	plan, err := integrations.Plan(integrations.FeatureRules, fstest.MapFS{
		"rules/" + ownedRel: &fstest.MapFile{Data: engineBytes, Mode: 0o644},
	})
	if err != nil {
		t.Fatalf("integrations.Plan(rules): %v", err)
	}
	if _, err := integrations.Apply(plan); err != nil {
		t.Fatalf("integrations.Apply(rules): %v", err)
	}
	ownedPath := filepath.Join(configDir, "rules", ownedRel)
	before, err := integrations.Status(integrations.FeatureRules)
	if err != nil {
		t.Fatalf("integrations.Status(rules) before the restore: %v", err)
	}
	if !before.OK() || !before.Deployed || before.Clean == 0 {
		t.Fatalf("the engine does not report a clean deployment before the restore: %+v", before)
	}

	// A PRE-PARTITION backup: it still carries a rules/ tree (the shape every
	// backup taken before the partition has) plus one file per mode-owned entry,
	// so the same fixture proves both halves at once. rules/<ownedRel> is the
	// collision file; rules/AGENTS.md has no live counterpart, so it also proves
	// the skip is decided per top-level ENTRY, not per colliding file.
	agyHome, err := paths.AgyProHome()
	if err != nil {
		t.Fatalf("AgyProHome: %v", err)
	}
	legacyBackup := filepath.Join(agyHome, "backups", "20260101_000000")
	legacyFiles := map[string]string{
		"rules/" + ownedRel:         "legacy-snapshot content\n",
		"rules/AGENTS.md":           "legacy rules\n",
		"skills/README.md":          "legacy skills\n",
		"agents/architect/AGENT.md": "legacy agent\n",
		"profile.json":              "{\"source\":\"snapshot\"}\n",
	}
	for rel, body := range legacyFiles {
		full := filepath.Join(legacyBackup, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Live register-owned entries, so the move-aside half has something to move
	// and the copy-back has something to rewrite.
	liveFiles := map[string]string{
		"agents/architect/AGENT.md": "live agent\n",
		"profile.json":              "{\"source\":\"live\"}\n",
	}
	for rel, body := range liveFiles {
		full := filepath.Join(configDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveState(&State{CurrentMode: ModeFull, LastBackup: legacyBackup}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	var out bytes.Buffer
	if err := Restore(&out); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("[SUCCESS]")) {
		t.Fatalf("Restore() did not report success: %s", out.String())
	}

	// FLIPPED BEHAVIOUR 1: the register-covered entries are restored from a
	// pre-partition snapshot. The count and transcript are asserted further down,
	// after the on-disk facts, so a code regression surfaces at the rule it
	// breaks rather than at a string.
	for rel, want := range map[string]string{
		"agents/architect/AGENT.md": legacyFiles["agents/architect/AGENT.md"],
		"profile.json":              legacyFiles["profile.json"],
	} {
		got, readErr := os.ReadFile(filepath.Join(configDir, filepath.FromSlash(rel)))
		if readErr != nil {
			t.Fatalf("the restore did not write the register-owned entry %q: %v", rel, readErr)
		}
		if string(got) != want {
			t.Fatalf("restore of %q = %q; want the snapshot bytes %q", rel, string(got), want)
		}
	}

	// FLIPPED BEHAVIOUR 2: the legacy rules/ and skills/ entries are NOT re-laid.
	// rules/AGENTS.md has no live counterpart, so its absence on disk is the
	// proof that the whole entry was skipped rather than merged.
	if _, statErr := os.Stat(filepath.Join(configDir, "rules", "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Fatalf("the restore re-laid the legacy rules/AGENTS.md (err = %v); the copy-back must restore only entries "+
			"the managed register owns", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(configDir, "skills")); !os.IsNotExist(statErr) {
		t.Fatalf("the restore re-laid the legacy skills/ tree (err = %v); skills is integrations-owned", statErr)
	}
	// The engine's own bytes survive untouched.
	survived, err := os.ReadFile(ownedPath)
	if err != nil {
		t.Fatalf("read the engine's destination after the restore: %v", err)
	}
	if !bytes.Equal(survived, engineBytes) {
		t.Fatalf("the restore overwrote the engine's destination: got %q; want the engine's own bytes %q "+
			"(a legacy rules/ entry must never be re-laid over an integrations-owned destination)",
			string(survived), string(engineBytes))
	}
	// ...and the engine reports NO drift: the narrowing removes the damage class
	// instead of leaving a repair job behind.
	after, err := integrations.Status(integrations.FeatureRules)
	if err != nil {
		t.Fatalf("integrations.Status(rules): %v", err)
	}
	if !after.OK() || after.Drifted != 0 || after.Missing != 0 {
		t.Fatalf("the restore produced engine drift (%+v); the narrowed copy-back must not touch "+
			"integrations-owned destinations", after)
	}
	if after.Clean != before.Clean {
		t.Fatalf("the engine's clean destination count moved across the restore: %d -> %d", before.Clean, after.Clean)
	}

	// FLIPPED BEHAVIOUR 3: the skip is visible and deterministic. The report
	// names every skipped entry in name order (os.ReadDir), and the success line
	// counts the register-filtered set ("Restored 2 items"), not however many
	// entries the backup happens to hold, so an operator learns what the snapshot
	// contains but the restore did not put back.
	if got := out.String(); !strings.Contains(got, "[SUCCESS] Restored 2 items from: "+legacyBackup) {
		t.Fatalf("the restore did not report the register-filtered item count:\n%s", got)
	}
	wantSkip := "Left in place (not a mode-managed item): rules, skills"
	if got := out.String(); !strings.Contains(got, wantSkip) {
		t.Fatalf("the restore transcript does not name the skipped entries %q:\n%s", wantSkip, got)
	}
	if !strings.Contains(out.String(), "agy pro integrations apply") {
		t.Fatalf("the restore transcript does not tell the operator who owns the skipped destinations:\n%s", out.String())
	}

	// BEHAVIOUR UNCHANGED 4: the move-aside half DOES derive from the register, so
	// the pre-restore backup holds the live register-owned entries and no
	// engine-owned tree.
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if reloaded.CurrentMode != "restored" {
		t.Fatalf("expected the restored marker, got %q", reloaded.CurrentMode)
	}
	if reloaded.LastBackup == "" {
		t.Fatal("the restore recorded no pre-restore backup")
	}
	for rel, want := range liveFiles {
		got, readErr := os.ReadFile(filepath.Join(reloaded.LastBackup, filepath.FromSlash(rel)))
		if readErr != nil {
			t.Fatalf("the live entry %q was not moved into the pre-restore backup: %v", rel, readErr)
		}
		if string(got) != want {
			t.Fatalf("the pre-restore backup holds %q for %q; want the live bytes %q", string(got), rel, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(reloaded.LastBackup, "rules")); !os.IsNotExist(statErr) {
		t.Fatalf("the pre-restore move-aside touched the integrations-owned rules tree (err = %v); the managed "+
			"register is supposed to be the only enumeration the move half reads", statErr)
	}

	// BEHAVIOUR UNCHANGED 5: the source backup is copied, never consumed, so the
	// user's snapshot survives a restore that already used it - including the
	// entries the restore declined to re-lay.
	for rel, want := range legacyFiles {
		got, readErr := os.ReadFile(filepath.Join(legacyBackup, filepath.FromSlash(rel)))
		if readErr != nil {
			t.Fatalf("the restore consumed the source backup entry %q: %v", rel, readErr)
		}
		if string(got) != want {
			t.Fatalf("the restore rewrote the source backup entry %q: got %q want %q", rel, string(got), want)
		}
	}
}
