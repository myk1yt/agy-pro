package mode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
)

// A10: state.json used to be written 0644 while every sibling state file
// (swarm-state.json, fanout reports) is 0600. The write seam is what can be
// asserted on Windows, where mode bits do not round-trip through Stat.
func TestSaveStateWritesOwnerOnly(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	type writeCall struct {
		path string
		perm os.FileMode
	}
	var calls []writeCall
	old := atomicWriteFile
	atomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		calls = append(calls, writeCall{path, perm})
		return old(path, data, perm)
	}
	t.Cleanup(func() { atomicWriteFile = old })

	if err := SaveState(&State{CurrentMode: ModeFull}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly one state write, got %d: %+v", len(calls), calls)
	}
	if calls[0].perm != 0o600 {
		t.Errorf("state.json written with mode %04o; want 0600", calls[0].perm)
	}
	// The write seam must still write through: the file exists and reloads.
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState after save: %v", err)
	}
	if reloaded.CurrentMode != ModeFull {
		t.Errorf("reloaded state = %+v; want current_mode=full", reloaded)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "state.json")); err != nil {
		t.Errorf("state.json not written through the seam: %v", err)
	}
}

// A10b: both mode-echo paths print a pasted value back to the user. A secret
// pasted by mistake must come out masked (providers.MaskSecret), never raw.
func TestSwitchErrorsMaskPastedValues(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	const pasted = "sk-pasted-into-the-mode-prompt-42"

	// Non-interactive spelling: the raw argv value is echoed in the error.
	err := Switch(pasted, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an unknown mode must error")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Errorf("unknown-mode error echoed the raw pasted value: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pa****") {
		t.Errorf("unknown-mode error = %v; want the MaskSecret rendering", err)
	}

	// Interactive spelling: the pasted line is echoed in the error too.
	err = Switch("", strings.NewReader(pasted+"\n"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("an invalid interactive choice must error")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Errorf("invalid-selection error echoed the raw pasted value: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pa****") {
		t.Errorf("invalid-selection error = %v; want the MaskSecret rendering", err)
	}

	// The mask stays value-free for short input (<= 5 runes render as "****").
	if err := Switch("nope", nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `"****"`) {
		t.Errorf("short invalid mode error = %v; want the fully masked form", err)
	}
}
