package mode

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/profiles"
)

func TestStatePersistence(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)

	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.CurrentMode != "clean" {
		t.Fatalf("expected default clean mode, got %q", state.CurrentMode)
	}

	state.CurrentMode = "full"
	state.LastBackup = filepath.Join(tempDir, "backups", "test")
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("reload LoadState failed: %v", err)
	}
	if reloaded.CurrentMode != "full" {
		t.Fatalf("expected full, got %q", reloaded.CurrentMode)
	}
	if reloaded.LastBackup != state.LastBackup {
		t.Fatalf("expected %q, got %q", state.LastBackup, reloaded.LastBackup)
	}
}

// TestSwitchModesAndBackup pins the two halves of one switch side by side: a
// mode-owned entry is MOVED to the timestamped backup (never deleted), while an
// integrations-owned entry in the same config directory is left alone. The rule
// file used to be moved too; P5-3 removed rules/skills/plugins from
// DefaultManagedItems, so the assertion moved with the ownership, not because
// the check was relaxed.
func TestSwitchModesAndBackup(t *testing.T) {
	tempDir := t.TempDir()
	agyHome := filepath.Join(tempDir, "agy-pro")
	geminiConfig := filepath.Join(tempDir, "gemini-config")

	t.Setenv(paths.EnvAgyProHome, agyHome)
	t.Setenv(paths.EnvGeminiConfigDir, geminiConfig)

	// Step 1: one entry per owner. The custom rule is an integrations-owned
	// destination (rules/); the agent file is a mode-owned one (agents/).
	customRule := filepath.Join(geminiConfig, "rules", "custom.md")
	if err := os.MkdirAll(filepath.Dir(customRule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customRule, []byte("user-rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	managedAgent := filepath.Join(geminiConfig, "agents", "custom", "AGENT.md")
	if err := os.MkdirAll(filepath.Dir(managedAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedAgent, []byte("user-agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Step 2: Switch to "clean"
	var outBuf bytes.Buffer
	if err := Switch("clean", nil, &outBuf); err != nil {
		t.Fatalf("Switch to clean failed: %v", err)
	}

	// Check output
	if !strings.Contains(outBuf.String(), "[SUCCESS]") {
		t.Fatalf("expected success message, got: %s", outBuf.String())
	}

	// The mode-owned entry was MOVED to the backup (not deleted)...
	if _, err := os.Stat(managedAgent); !os.IsNotExist(err) {
		t.Fatal("expected the managed agents entry to be moved out of the config dir")
	}

	state, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentMode != ModeClean {
		t.Fatalf("expected state mode clean, got %q", state.CurrentMode)
	}
	if state.LastBackup == "" {
		t.Fatal("expected LastBackup to be populated")
	}

	// ...and the backup contains it.
	backedUpFile := filepath.Join(state.LastBackup, "agents", "custom", "AGENT.md")
	if _, err := os.Stat(backedUpFile); err != nil {
		t.Fatalf("backed up file not found at %q: %v", backedUpFile, err)
	}

	// The integrations-owned rule SURVIVED the switch: rules/ is no longer in
	// the managed register, so the retraction half of the partition cannot move
	// it, and no backup may hold it.
	if got, err := os.ReadFile(customRule); err != nil || string(got) != "user-rule" {
		t.Fatalf("the integrations-owned rule did not survive the clean switch: %v (%q)", err, string(got))
	}
	if hits := backupFilesNamed(t, "custom.md"); len(hits) != 0 {
		t.Fatalf("a switch moved the integrations-owned rule into backup: %v", hits)
	}

	// Step 3: Switch to "full"
	outBuf.Reset()
	if err := Switch("full", nil, &outBuf); err != nil {
		t.Fatalf("Switch to full failed: %v", err)
	}

	// Verify the full profile deployed a mode-owned destination. rules/AGENTS.md
	// was the pre-P5-3 witness; after the T7 trim the full profile no longer
	// carries a rules/ tree at all, so the witness moved to agents/, which mode
	// still owns and deploys.
	agentsRule := filepath.Join(geminiConfig, "agents", "architect", "AGENT.md")
	if _, err := os.Stat(agentsRule); err != nil {
		t.Fatalf("expected agents/architect/AGENT.md in config dir after full switch: %v", err)
	}

	// Step 4: Test Restore
	outBuf.Reset()
	if err := Restore(&outBuf); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if !strings.Contains(outBuf.String(), "[SUCCESS]") {
		t.Fatalf("expected restore success, got: %s", outBuf.String())
	}

	// The restore derives its pre-restore move from the same register: the live
	// mode-owned agents tree is moved aside (never deleted) into the pre-restore
	// backup, while the integrations-owned rule is left alone.
	reloaded, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CurrentMode != "restored" {
		t.Fatalf("expected the restored marker, got %q", reloaded.CurrentMode)
	}
	if _, err := os.Stat(filepath.Join(reloaded.LastBackup, "agents", "architect", "AGENT.md")); err != nil {
		t.Fatalf("the mode-owned agents tree was not preserved in the pre-restore backup %q: %v", reloaded.LastBackup, err)
	}
	if got, err := os.ReadFile(customRule); err != nil || string(got) != "user-rule" {
		t.Fatalf("the integrations-owned rule did not survive the restore: %v (%q)", err, string(got))
	}
}

func TestSwitchInteractive(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, filepath.Join(tempDir, "agy"))
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	inBuf := bytes.NewBufferString("1\n")
	var outBuf bytes.Buffer

	if err := Switch("", inBuf, &outBuf); err != nil {
		t.Fatalf("interactive switch failed: %v", err)
	}

	state, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentMode != ModeFull {
		t.Fatalf("expected full mode from choice 1, got %q", state.CurrentMode)
	}
}

// TestFullCleanRoundTripNoResidue verifies that the mode-owned half of the
// managed register (agents, references, profile.json) makes full<->clean
// switching a complete backup/restore cycle for its own destinations: zero
// residue of THOSE entries in the config dir after clean, every removed asset
// recoverable from the backup, and files outside the register untouched.
//
// rules/, skills/ and plugins/ are integrations-owned after P5-3, so they are
// deliberately not part of "zero residue" here: the clean profile walks its own
// rules/README.md, skills/README.md and plugins/README.md down (interim, T7
// trims the walk), and the engine's own files in those trees must survive. The
// collision proof for that half is
// TestManagedSetRetractionLeavesIntegrationsOwnedTrees.
func TestFullCleanRoundTripNoResidue(t *testing.T) {
	tempDir := t.TempDir()
	agyHome := filepath.Join(tempDir, "agy-pro")
	geminiConfig := filepath.Join(tempDir, "gemini-config")

	t.Setenv(paths.EnvAgyProHome, agyHome)
	t.Setenv(paths.EnvGeminiConfigDir, geminiConfig)

	// A non-managed neighbor must survive every switch untouched.
	hooksFile := filepath.Join(geminiConfig, "hooks", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksFile, []byte("hook"), 0o644); err != nil {
		t.Fatal(err)
	}

	newlyManaged := []string{"agents", "references", "profile.json"}
	var out bytes.Buffer

	if err := Switch(ModeFull, nil, &out); err != nil {
		t.Fatalf("Switch to full failed: %v", err)
	}
	for _, item := range newlyManaged {
		if _, err := os.Stat(filepath.Join(geminiConfig, item)); err != nil {
			t.Fatalf("full profile should deploy %s: %v", item, err)
		}
	}

	out.Reset()
	if err := Switch(ModeClean, nil, &out); err != nil {
		t.Fatalf("Switch to clean failed: %v", err)
	}

	state, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentMode != ModeClean {
		t.Fatalf("expected clean mode, got %q", state.CurrentMode)
	}

	// references ships only with the full profile: zero residue after clean, and
	// recoverable from the backup taken by the clean switch.
	fullOnly := []string{"references"}
	for _, item := range fullOnly {
		if _, err := os.Stat(filepath.Join(geminiConfig, item)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed from config dir after clean switch", item)
		}
		if _, err := os.Stat(filepath.Join(state.LastBackup, item)); err != nil {
			t.Fatalf("%s missing from backup %q: %v", item, state.LastBackup, err)
		}
	}
	// agents is retracted too, with ONE exception since P33: the native-search
	// asset is feature content BOTH profiles ship (a clean switch used to retract
	// it and leave the restricted search route unreachable until the installer ran
	// again; TestSearchAgentAssetSurvivesEveryModeSwitch owns that contract). Every
	// PROFILE agent leaves the config dir and is recoverable from the backup, and
	// the tree on disk holds exactly that one feature asset afterwards.
	for _, name := range []string{"architect", "security-reviewer"} {
		if _, err := os.Stat(filepath.Join(geminiConfig, "agents", name)); !os.IsNotExist(err) {
			t.Fatalf("expected agents/%s to be removed from config dir after clean switch", name)
		}
		if _, err := os.Stat(filepath.Join(state.LastBackup, "agents", name)); err != nil {
			t.Fatalf("agents/%s missing from backup %q: %v", name, state.LastBackup, err)
		}
	}
	if got := agentFilesUnder(t, filepath.Join(geminiConfig, "agents")); strings.Join(got, ",") != searchAgentInAgentsTree {
		t.Fatalf("agents tree after the clean switch = %v; want exactly %q (the one asset both profiles ship)", got, searchAgentInAgentsTree)
	}
	// profile.json ships with both profiles: it must be swapped to the clean
	// manifest while the full manifest is preserved in the backup.
	cleanProfileFS, err := profiles.GetProfileFS(ModeClean)
	if err != nil {
		t.Fatal(err)
	}
	wantClean, err := fs.ReadFile(cleanProfileFS, "profile.json")
	if err != nil {
		t.Fatal(err)
	}
	gotClean, err := os.ReadFile(filepath.Join(geminiConfig, "profile.json"))
	if err != nil {
		t.Fatalf("profile.json missing after clean switch: %v", err)
	}
	if !bytes.Equal(wantClean, gotClean) {
		t.Fatal("expected clean profile manifest after clean switch")
	}
	wantFull, err := fs.ReadFile(fullFS(t), "profile.json")
	if err != nil {
		t.Fatal(err)
	}
	gotBackedUp, err := os.ReadFile(filepath.Join(state.LastBackup, "profile.json"))
	if err != nil {
		t.Fatalf("full profile.json missing from backup: %v", err)
	}
	if !bytes.Equal(wantFull, gotBackedUp) {
		t.Fatal("expected full profile manifest preserved in backup")
	}
	if !reflect.DeepEqual(state.ManagedItems, DefaultManagedItems) {
		t.Fatalf("expected persisted manifest %v, got %v", DefaultManagedItems, state.ManagedItems)
	}
	if _, err := os.Stat(hooksFile); err != nil {
		t.Fatalf("hooks.json must survive mode switches: %v", err)
	}

	// clean -> full round trip: every asset is redeployed byte-identical.
	out.Reset()
	if err := Switch(ModeFull, nil, &out); err != nil {
		t.Fatalf("Switch back to full failed: %v", err)
	}
	wantProfile, err := fs.ReadFile(fullFS(t), "profile.json")
	if err != nil {
		t.Fatal(err)
	}
	gotProfile, err := os.ReadFile(filepath.Join(geminiConfig, "profile.json"))
	if err != nil {
		t.Fatalf("profile.json missing after round trip: %v", err)
	}
	if !bytes.Equal(wantProfile, gotProfile) {
		t.Fatal("profile.json content diverged after full->clean->full round trip")
	}
	for _, item := range fullOnly {
		if _, err := os.Stat(filepath.Join(geminiConfig, item)); err != nil {
			t.Fatalf("%s missing after round trip: %v", item, err)
		}
	}
	if _, err := os.Stat(filepath.Join(geminiConfig, "agents", "architect", "AGENT.md")); err != nil {
		t.Fatalf("the full agent roster was not redeployed by the round trip: %v", err)
	}

	// Each switch must own its own backup directory even when several
	// switches land within the same second.
	stateAfterFull, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if stateAfterFull.LastBackup == state.LastBackup {
		t.Fatal("distinct switches within the same second must not share one backup directory")
	}

	// Restore returns the config to the state captured by the last switch
	// backup (the clean snapshot). The full deployment active at restore
	// time is moved aside to a pre-restore backup, so nothing is lost.
	out.Reset()
	if err := Restore(&out); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	// Every entry of a post-partition snapshot is register-owned, so the T9
	// narrowing has nothing to report as left in place.
	if strings.Contains(out.String(), "Left in place") {
		t.Fatalf("a snapshot holding only register-owned entries reported a skip:\n%s", out.String())
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CurrentMode != "restored" {
		t.Fatalf("expected restored marker, got %q", reloaded.CurrentMode)
	}
	for _, item := range fullOnly {
		if _, err := os.Stat(filepath.Join(geminiConfig, item)); !os.IsNotExist(err) {
			t.Fatalf("expected %s absent from config after restoring clean snapshot", item)
		}
		if _, err := os.Stat(filepath.Join(reloaded.LastBackup, item)); err != nil {
			t.Fatalf("%s missing from pre-restore backup %q: %v", item, reloaded.LastBackup, err)
		}
	}
	// The agents half of the same shape: the full roster is moved aside (never
	// deleted) and the clean snapshot's tree - the feature asset alone - comes
	// back, which is why the asset survives a restore as well as a switch.
	if _, err := os.Stat(filepath.Join(reloaded.LastBackup, "agents", "architect")); err != nil {
		t.Fatalf("the full agent roster was not preserved in the pre-restore backup %q: %v", reloaded.LastBackup, err)
	}
	if got := agentFilesUnder(t, filepath.Join(geminiConfig, "agents")); strings.Join(got, ",") != searchAgentInAgentsTree {
		t.Fatalf("agents tree after restoring the clean snapshot = %v; want exactly %q", got, searchAgentInAgentsTree)
	}
}

// fullFS returns the embedded full profile filesystem.
func fullFS(t *testing.T) fs.FS {
	t.Helper()
	profileFS, err := profiles.GetProfileFS(ModeFull)
	if err != nil {
		t.Fatalf("failed to load full profile: %v", err)
	}
	return profileFS
}

// TestRestoreLegacyBackupKeepsUnmanagedAssets verifies compatibility with
// backups written before the managed set was expanded AND before the P5-3
// destination partition: the snapshot is the legacy four-item shape (rules/,
// skills/, plugins/, config.json). Restore must still work, it restores the one
// entry the current register still owns (config.json), and it leaves the three
// integrations-owned trees exactly as the live config dir had them - a legacy
// snapshot entry is no longer permission to re-lay an engine-owned destination
// (SESSION9 T9 narrowing). Residue from old full deployments is still preserved
// (moved aside, never deleted) and files outside the managed set still stay in
// place.
func TestRestoreLegacyBackupKeepsUnmanagedAssets(t *testing.T) {
	tempDir := t.TempDir()
	agyHome := filepath.Join(tempDir, "agy-pro")
	geminiConfig := filepath.Join(tempDir, "gemini-config")

	t.Setenv(paths.EnvAgyProHome, agyHome)
	t.Setenv(paths.EnvGeminiConfigDir, geminiConfig)

	// Legacy-shaped backup: only the four pre-expansion items.
	backupDir := filepath.Join(agyHome, "backups", "20261011_071900")
	legacyFiles := map[string]string{
		"rules/AGENTS.md":   "legacy-rules",
		"skills/README.md":  "legacy-skills",
		"plugins/README.md": "legacy-plugins",
		"config.json":       "{}",
	}
	for rel, content := range legacyFiles {
		p := filepath.Join(backupDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Active config: an integrations-owned rules file that predates the
	// partition, full-profile residue under the mode-owned agents tree, and an
	// unmanaged hooks file.
	staleRules := filepath.Join(geminiConfig, "rules", "stale.md")
	if err := os.MkdirAll(filepath.Dir(staleRules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleRules, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	residueAgent := filepath.Join(geminiConfig, "agents", "architect", "AGENT.md")
	if err := os.MkdirAll(filepath.Dir(residueAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(residueAgent, []byte("residue-agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	hooksFile := filepath.Join(geminiConfig, "hooks", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksFile, []byte("hook"), 0o644); err != nil {
		t.Fatal(err)
	}

	// State file in the legacy schema (four-item manifest).
	if err := SaveState(&State{
		CurrentMode:  ModeFull,
		UpdatedAt:    time.Now().UTC(),
		LastBackup:   backupDir,
		ManagedItems: []string{"rules", "skills", "plugins", "config.json"},
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Restore(&out); err != nil {
		t.Fatalf("Restore from legacy backup failed: %v", err)
	}

	// The register-owned entry is restored: config.json is the one item this
	// snapshot carries that the managed register still lists.
	if got, err := os.ReadFile(filepath.Join(geminiConfig, "config.json")); err != nil || string(got) != "{}" {
		t.Fatalf("the register-owned config.json was not restored: %v (%q)", err, string(got))
	}
	// The three integrations-owned trees stayed on disk untouched: no legacy
	// rules/AGENTS.md appears, and the live rules file the engine (or a
	// pre-partition build) put there keeps its bytes.
	if _, err := os.Stat(filepath.Join(geminiConfig, "rules", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("the restore re-laid the legacy rules/AGENTS.md (err = %v); the copy-back restores only "+
			"entries the managed register owns", err)
	}
	if got, err := os.ReadFile(staleRules); err != nil || string(got) != "stale" {
		t.Fatalf("the restore touched the live integrations-owned rules file: %v (%q)", err, string(got))
	}
	for _, tree := range []string{"skills", "plugins"} {
		if _, err := os.Stat(filepath.Join(geminiConfig, tree)); !os.IsNotExist(err) {
			t.Fatalf("the restore re-laid the legacy %q tree (err = %v)", tree, err)
		}
	}
	// The skip is visible and deterministic (name order from os.ReadDir).
	if got := out.String(); !strings.Contains(got, "Left in place (not a mode-managed item): plugins, rules, skills") {
		t.Fatalf("the restore transcript does not name the skipped entries:\n%s", got)
	}
	// Residue agents moved aside to the pre-restore backup, not deleted.
	reloaded, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CurrentMode != "restored" {
		t.Fatalf("expected restored marker, got %q", reloaded.CurrentMode)
	}
	if _, err := os.Stat(filepath.Join(geminiConfig, "agents")); !os.IsNotExist(err) {
		t.Fatal("expected residue agents dir to be moved out of config dir")
	}
	if _, err := os.Stat(filepath.Join(reloaded.LastBackup, "agents", "architect", "AGENT.md")); err != nil {
		t.Fatalf("residue agents not preserved in pre-restore backup: %v", err)
	}
	// Unmanaged hooks untouched.
	if _, err := os.Stat(hooksFile); err != nil {
		t.Fatalf("hooks.json must survive restore: %v", err)
	}
}

// TestLoadStateMigratesLegacyManifest verifies the load-time reconciliation of
// the managed manifest: a state.json written by an older build (here the
// pre-expansion four, which includes the three destinations that have since
// moved to the integrations engine) loads with the CURRENT register, and the
// original fields survive.
//
// Before P5-3 the load merged the stored list with the defaults, so a legacy
// entry whose owner had not moved was preserved. Now the register is replaced
// wholesale, because a stored claim on a destination another owner holds is the
// shared-path ambiguity the partition exists to remove. The assertion moved with
// the ownership: it still pins that a legacy file loads, that only the register
// is present afterwards, and that no stale claim survives.
func TestLoadStateMigratesLegacyManifest(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	legacy := `{"current_mode":"full","updated_at":"2026-10-11T07:19:00Z","last_backup":"C:\\agy\\backups\\20261011_071900","managed_items":["rules","skills","plugins","config.json"]}`
	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState of legacy state file failed: %v", err)
	}
	want := append([]string(nil), DefaultManagedItems...)
	if !reflect.DeepEqual(state.ManagedItems, want) {
		t.Fatalf("expected the reconciled manifest %v, got %v", want, state.ManagedItems)
	}
	for _, moved := range []string{"rules", "skills", "plugins"} {
		for _, item := range state.ManagedItems {
			if item == moved {
				t.Fatalf("the stored claim %q survived the load although its owner moved: %v", moved, state.ManagedItems)
			}
		}
	}
	if state.CurrentMode != ModeFull {
		t.Fatalf("expected current_mode preserved, got %q", state.CurrentMode)
	}
	if state.LastBackup != `C:\agy\backups\20261011_071900` {
		t.Fatalf("expected last_backup preserved, got %q", state.LastBackup)
	}

	// The reconciled set round-trips through save/load without duplicates.
	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.ManagedItems, want) {
		t.Fatalf("expected the reconciled manifest to persist, got %v", reloaded.ManagedItems)
	}
}

// TestSwitchSurvivesCorruptState verifies that a corrupt state.json does not
// crash the switch (previously a nil dereference) and that the switch rewrites
// a valid state file.
func TestSwitchSurvivesCorruptState(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Switch(ModeClean, nil, &out); err != nil {
		t.Fatalf("Switch with corrupt state failed: %v", err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("state not rewritten after switch: %v", err)
	}
	if reloaded.CurrentMode != ModeClean {
		t.Fatalf("expected clean mode, got %q", reloaded.CurrentMode)
	}
	if !reflect.DeepEqual(reloaded.ManagedItems, DefaultManagedItems) {
		t.Fatalf("expected default manifest after recovery, got %v", reloaded.ManagedItems)
	}
}

// TestStatusReportsState verifies that the read-only status path prints the
// mode state and succeeds (nil error) on both a fresh home and after a switch.
func TestStatusReportsState(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, filepath.Join(tempDir, "agy"))
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	var out bytes.Buffer
	if err := Status(&out); err != nil {
		t.Fatalf("Status on fresh home failed: %v", err)
	}
	if !strings.Contains(out.String(), "clean") {
		t.Fatalf("expected default clean mode in status output, got: %s", out.String())
	}

	out.Reset()
	if err := Switch(ModeFull, nil, &out); err != nil {
		t.Fatalf("Switch to full failed: %v", err)
	}

	out.Reset()
	if err := Status(&out); err != nil {
		t.Fatalf("Status after switch failed: %v", err)
	}
	status := out.String()
	for _, want := range []string{"Current Mode", ModeFull, "Manifest", "agents", "Backups"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status output missing %q, got: %s", want, status)
		}
	}
}

// TestBridgeToolsAdditiveKeyRoundTrip covers the bridge_tools key added for
// the gateway tool loop (R1-6): it round-trips through state.json, defaults to
// read,search when absent, and is normalized only by the config subcommand.
func TestBridgeToolsAdditiveKeyRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	// A fresh state has no key: the effective value is the read-only default.
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.BridgeTools != "" {
		t.Fatalf("fresh state should not carry bridge_tools, got %q", state.BridgeTools)
	}
	if got := state.EffectiveBridgeTools(); got != DefaultBridgeTools {
		t.Fatalf("EffectiveBridgeTools() = %q; want %q", got, DefaultBridgeTools)
	}

	// The key survives a save/load cycle alongside the pre-existing keys.
	state.BridgeTools = "read,search,write"
	state.DefaultProfile = ProfileGateway
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("reload LoadState failed: %v", err)
	}
	if reloaded.BridgeTools != "read,search,write" {
		t.Fatalf("bridge_tools did not round-trip, got %q", reloaded.BridgeTools)
	}
	if reloaded.EffectiveDefaultProfile() != ProfileGateway {
		t.Fatalf("default_profile did not survive alongside bridge_tools, got %q", reloaded.DefaultProfile)
	}
	if got := reloaded.EffectiveBridgeTools(); got != "read,search,write" {
		t.Fatalf("EffectiveBridgeTools() = %q; want stored value", got)
	}

	// "none" is a real stored value, not a blank that falls back to default.
	reloaded.BridgeTools = BridgeToolsNone
	if err := SaveState(reloaded); err != nil {
		t.Fatal(err)
	}
	disabled, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := disabled.EffectiveBridgeTools(); got != BridgeToolsNone {
		t.Fatalf("EffectiveBridgeTools() = %q; want %q", got, BridgeToolsNone)
	}
}

// TestLegacyStateKeepsOriginalKeys verifies the additive-key contract: a
// state.json written before bridge_tools (and default_profile) existed loads
// and re-saves with its original four keys byte-for-byte intact and no new key
// injected.
func TestLegacyStateKeepsOriginalKeys(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	legacy := `{"current_mode":"full","updated_at":"2026-10-11T07:19:00Z","last_backup":"C:\\agy\\backups\\20261011_071900","managed_items":["rules","skills","plugins","agents","references","profile.json","config.json"]}`
	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState of legacy state file failed: %v", err)
	}
	if state.BridgeTools != "" {
		t.Fatalf("legacy state must not gain bridge_tools on load, got %q", state.BridgeTools)
	}
	if state.DefaultProfile != "" {
		t.Fatalf("legacy state must not gain default_profile on load, got %q", state.DefaultProfile)
	}

	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"current_mode"`, `"updated_at"`, `"last_backup"`, `"managed_items"`} {
		if !strings.Contains(string(saved), key) {
			t.Fatalf("re-saved legacy state lost %s:\n%s", key, saved)
		}
	}
	for _, key := range []string{`"bridge_tools"`, `"default_profile"`, `"hybrid_profile"`} {
		if strings.Contains(string(saved), key) {
			t.Fatalf("re-saved legacy state must not inject %s:\n%s", key, saved)
		}
	}
}

// TestHybridProfileAdditiveKeyRoundTrip covers the hybrid_profile key added
// for the native-coexistence profiles (session 261014_0007): it round-trips
// through state.json, defaults to HybridProfileNone when absent, normalizes
// aliases, and never appears in a legacy state file.
func TestHybridProfileAdditiveKeyRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	// A fresh state has no key: the effective value is none.
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.HybridProfile != "" {
		t.Fatalf("fresh state should not carry hybrid_profile, got %q", state.HybridProfile)
	}
	if got := state.EffectiveHybridProfile(); got != HybridProfileNone {
		t.Fatalf("EffectiveHybridProfile() = %q; want %q", got, HybridProfileNone)
	}

	// The key survives a save/load cycle alongside the pre-existing keys.
	state.HybridProfile = "p2"
	state.DefaultProfile = ProfileNative
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("reload LoadState failed: %v", err)
	}
	if reloaded.HybridProfile != "p2" {
		t.Fatalf("hybrid_profile did not round-trip, got %q", reloaded.HybridProfile)
	}
	if reloaded.EffectiveHybridProfile() != "p2" {
		t.Fatalf("EffectiveHybridProfile() = %q; want p2", reloaded.EffectiveHybridProfile())
	}
	if reloaded.EffectiveDefaultProfile() != ProfileNative {
		t.Fatalf("default_profile did not survive alongside hybrid_profile, got %q", reloaded.DefaultProfile)
	}

	// Aliases normalize; an unrecognized value fails closed to none.
	reloaded.HybridProfile = "picker"
	if got := reloaded.EffectiveHybridProfile(); got != "p1" {
		t.Fatalf("EffectiveHybridProfile(picker) = %q; want p1", got)
	}
	reloaded.HybridProfile = "garbage"
	if got := reloaded.EffectiveHybridProfile(); got != HybridProfileNone {
		t.Fatalf("EffectiveHybridProfile(garbage) = %q; want %q", got, HybridProfileNone)
	}

	// A nil state reads as none (the pre-existing Effective* contract).
	var nilState *State
	if got := nilState.EffectiveHybridProfile(); got != HybridProfileNone {
		t.Fatalf("nil.EffectiveHybridProfile() = %q; want %q", got, HybridProfileNone)
	}
}

// The profile config.json files are deployed verbatim into the user's
// ~/.gemini/config/config.json, and agy parses that file as protojson: the
// plugins map values must be OBJECTS with an "enabled" bool. A bare boolean
// ("code_reviewer": true) makes agy fail the whole config parse
// ("unexpected token true"), which silently drops every plugin enablement
// record. Fail-without-fix pin for the 2026-10-14 corruption.
func TestProfileConfigJSONPluginEnablementShape(t *testing.T) {
	for _, profile := range []string{ModeFull, ModeClean} {
		t.Run(profile, func(t *testing.T) {
			profileFS, err := profiles.GetProfileFS(profile)
			if err != nil {
				t.Fatal(err)
			}
			data, err := fs.ReadFile(profileFS, "config.json")
			if err != nil {
				t.Fatalf("profile %s has no config.json: %v", profile, err)
			}
			var doc struct {
				Plugins map[string]json.RawMessage `json:"plugins"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("profile %s config.json is not JSON: %v", profile, err)
			}
			for name, raw := range doc.Plugins {
				var entry struct {
					Enabled *bool `json:"enabled"`
				}
				if err := json.Unmarshal(raw, &entry); err != nil || entry.Enabled == nil {
					t.Errorf("profile %s: plugins[%q] = %s, want an object with an \"enabled\" bool", profile, name, raw)
				}
			}
		})
	}
}

// The profile pin must also assert the plugins map KEYS name real plugin
// directories. The full profile's config.json references plugins that the
// ROOT installer deploys (repo plugins/ -> ~/.gemini/config/plugins/), not
// files inside the profile FS itself, so the check accepts either source.
// The repo plugins/ tree three directories up exists only in the full workspace;
// a standalone agy-pro checkout carries no sibling plugin trees, so keys that
// resolve through that tree alone make the subtest SKIP (naming the missing
// keys) rather than fail. Everything else - including the full-repo run, where
// the tree is present - asserts exactly as before.
func TestProfileConfigPluginKeysNameRealDirs(t *testing.T) {
	repoPlugins := filepath.Join("..", "..", "..", "plugins")
	_, repoErr := os.Stat(repoPlugins)
	repoPresent := repoErr == nil
	for _, profile := range []string{ModeFull, ModeClean} {
		t.Run(profile, func(t *testing.T) {
			profileFS, err := profiles.GetProfileFS(profile)
			if err != nil {
				t.Fatal(err)
			}
			data, err := fs.ReadFile(profileFS, "config.json")
			if err != nil {
				t.Fatalf("profile %s has no config.json: %v", profile, err)
			}
			var doc struct {
				Plugins map[string]json.RawMessage `json:"plugins"`
			}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("profile %s config.json is not JSON: %v", profile, err)
			}
			if !repoPresent {
				// Standalone checkout: no sibling tree to resolve against, so a key
				// absent from the profile FS is unverifiable, not wrong.
				var unresolved []string
				for name := range doc.Plugins {
					if _, inProfile := fs.Stat(profileFS, "plugins/"+name); inProfile != nil {
						unresolved = append(unresolved, name)
					}
				}
				if len(unresolved) > 0 {
					t.Skipf("profile %s: plugins key(s) %v resolve only through the repo plugins/ tree, absent at %q (a standalone agy-pro checkout ships no sibling plugin trees)", profile, unresolved, repoPlugins)
				}
			}
			for name := range doc.Plugins {
				_, inProfile := fs.Stat(profileFS, "plugins/"+name)
				_, inRepo := os.Stat(filepath.Join(repoPlugins, name))
				if inProfile != nil && inRepo != nil {
					t.Errorf("profile %s: plugins key %q matches neither the profile FS nor the repo plugins/ dir", profile, name)
				}
			}
		})
	}
}

// TestRestoreRefusesLastBackupOutsideBackupsTree: state.json is a file the user
// can edit, so a hand-written last_backup is not permission to copy an arbitrary
// readable directory into the config tree. Restore refuses such a value and
// writes nothing; it deliberately does NOT fall back to the newest in-tree
// snapshot, because the presented plan named the outside directory and copying a
// different one after consent would make the write disagree with the plan.
func TestRestoreRefusesLastBackupOutsideBackupsTree(t *testing.T) {
	tempDir := t.TempDir()
	agyHome := filepath.Join(tempDir, "agy-pro")
	geminiConfig := filepath.Join(tempDir, "gemini-config")
	t.Setenv(paths.EnvAgyProHome, agyHome)
	t.Setenv(paths.EnvGeminiConfigDir, geminiConfig)

	outside := filepath.Join(tempDir, "hand-made-snapshot")
	if err := os.MkdirAll(filepath.Join(outside, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "agents", "AGENT.md"), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(&State{
		CurrentMode:  ModeFull,
		UpdatedAt:    time.Now().UTC(),
		LastBackup:   outside,
		ManagedItems: DefaultManagedItems,
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := Restore(&out)
	if err == nil {
		t.Fatalf("Restore accepted a last_backup outside the backups tree:%s", out.String())
	}
	if !strings.Contains(err.Error(), "not inside the agy-pro backups directory") {
		t.Fatalf("the refusal does not name the containment rule: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("a refused restore reported progress it did not make: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(geminiConfig, "agents")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused restore copied the outside snapshot into the config tree (err = %v)", statErr)
	}
	after, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.LastBackup != outside {
		t.Fatalf("the refused restore rewrote last_backup: %q", after.LastBackup)
	}
}

// TestManagedItemOwnerFoldsCase pins the predicate the restore transcript and
// both presented plans share: it folds case, because the copy-back resolves its
// source with Join+Stat and Win32 stats case-insensitively. A byte-exact answer
// reported a backup entry named AGENTS as "left in place" while the same run
// copied it to agents/ (F-S9R1-SEC-7).
func TestManagedItemOwnerFoldsCase(t *testing.T) {
	for _, name := range []string{"agents", "AGENTS", "Agents", "profile.json", "PROFILE.JSON"} {
		if !managedItemOwner(name) {
			t.Errorf("managedItemOwner(%q) = false; the copy-back's Join+Stat lookup is case-insensitive on Windows", name)
		}
	}
	for _, name := range []string{"", "rules", "skills", "plugins", "agents.md"} {
		if managedItemOwner(name) {
			t.Errorf("managedItemOwner(%q) = true; %q is not in the mode-managed register", name, name)
		}
	}
}
