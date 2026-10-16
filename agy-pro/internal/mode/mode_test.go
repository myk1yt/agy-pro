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

func TestSwitchModesAndBackup(t *testing.T) {
	tempDir := t.TempDir()
	agyHome := filepath.Join(tempDir, "agy-pro")
	geminiConfig := filepath.Join(tempDir, "gemini-config")

	t.Setenv(paths.EnvAgyProHome, agyHome)
	t.Setenv(paths.EnvGeminiConfigDir, geminiConfig)

	// Step 1: Create an existing custom rule in configDir
	customRule := filepath.Join(geminiConfig, "rules", "custom.md")
	if err := os.MkdirAll(filepath.Dir(customRule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customRule, []byte("user-rule"), 0o644); err != nil {
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

	// Verify original customRule was MOVED to backup (not deleted)
	if _, err := os.Stat(customRule); !os.IsNotExist(err) {
		t.Fatal("expected original custom.md to be moved out of config dir")
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

	// Verify backup contains the moved file
	backedUpFile := filepath.Join(state.LastBackup, "rules", "custom.md")
	if _, err := os.Stat(backedUpFile); err != nil {
		t.Fatalf("backed up file not found at %q: %v", backedUpFile, err)
	}

	// Step 3: Switch to "full"
	outBuf.Reset()
	if err := Switch("full", nil, &outBuf); err != nil {
		t.Fatalf("Switch to full failed: %v", err)
	}

	// Verify full profile deployed AGENTS.md
	agentsRule := filepath.Join(geminiConfig, "rules", "AGENTS.md")
	if _, err := os.Stat(agentsRule); err != nil {
		t.Fatalf("expected AGENTS.md in config dir after full switch: %v", err)
	}

	// Step 4: Test Restore
	outBuf.Reset()
	if err := Restore(&outBuf); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if !strings.Contains(outBuf.String(), "[SUCCESS]") {
		t.Fatalf("expected restore success, got: %s", outBuf.String())
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

// TestFullCleanRoundTripNoResidue verifies that the expanded managed set
// (agents, references, profile.json) makes full<->clean switching a complete
// backup/restore cycle: zero residue in the config dir after clean, every
// removed asset recoverable from the backup, and non-managed files untouched.
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

	// agents and references ship only with the full profile: zero residue
	// after clean, and recoverable from the backup taken by the clean switch.
	fullOnly := []string{"agents", "references"}
	for _, item := range fullOnly {
		if _, err := os.Stat(filepath.Join(geminiConfig, item)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed from config dir after clean switch", item)
		}
		if _, err := os.Stat(filepath.Join(state.LastBackup, item)); err != nil {
			t.Fatalf("%s missing from backup %q: %v", item, state.LastBackup, err)
		}
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
// backups written before the managed set was expanded: a legacy backup holds
// only the four original items, restore must still work, residue from old
// full deployments must be preserved (moved aside, never deleted), and files
// outside the managed set must stay in place.
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

	// Active config: legacy-managed rules, full-profile residue (agents),
	// and an unmanaged hooks file.
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

	// Legacy items restored.
	got, err := os.ReadFile(filepath.Join(geminiConfig, "rules", "AGENTS.md"))
	if err != nil || string(got) != "legacy-rules" {
		t.Fatalf("legacy rules not restored: %v (%q)", err, string(got))
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

// TestLoadStateMigratesLegacyManifest verifies the additive migration of the
// managed manifest: a state.json written by an older build (four items) loads
// with the new items appended, original fields intact.
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
	want := []string{"rules", "skills", "plugins", "config.json", "agents", "references", "profile.json"}
	if !reflect.DeepEqual(state.ManagedItems, want) {
		t.Fatalf("expected merged manifest %v, got %v", want, state.ManagedItems)
	}
	if state.CurrentMode != ModeFull {
		t.Fatalf("expected current_mode preserved, got %q", state.CurrentMode)
	}
	if state.LastBackup != `C:\agy\backups\20261011_071900` {
		t.Fatalf("expected last_backup preserved, got %q", state.LastBackup)
	}

	// The merged set round-trips through save/load without duplicates.
	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.ManagedItems, want) {
		t.Fatalf("expected merged manifest to persist, got %v", reloaded.ManagedItems)
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
func TestProfileConfigPluginKeysNameRealDirs(t *testing.T) {
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
			for name := range doc.Plugins {
				_, inProfile := fs.Stat(profileFS, "plugins/"+name)
				_, inRepo := os.Stat(filepath.Join("..", "..", "..", "plugins", name))
				if inProfile != nil && inRepo != nil {
					t.Errorf("profile %s: plugins key %q matches neither the profile FS nor the repo plugins/ dir", profile, name)
				}
			}
		})
	}
}
