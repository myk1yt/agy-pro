package mode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
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
