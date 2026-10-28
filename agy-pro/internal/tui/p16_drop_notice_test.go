package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
)

// TestPresetsDroppedRecordIsNotCorrupt pins R3-F-05: a hand-edited file whose
// only defect is one record carrying a control rune (dropped by the FL-4 load
// gate) must NOT render the corrupt-file claim, must NOT arm the corrupt
// overwrite gate, and must still surface the specific drop warning naming the
// ignored record, while the healthy records stay listed and usable.
func TestPresetsDroppedRecordIsNotCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	// Hand-authored (not SavePresets): one control-rune record, two healthy.
	data := `{"presets":[` +
		`{"name":"bad","task":"review","tools":"read,se\u2028arch"},` +
		`{"name":"healthy","task":"review","n":2},` +
		`{"name":"healthy2","task":"security","n":3}` +
		`],"default_preset":"healthy"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("seed hand-edited preset file: %v", err)
	}
	st := &fanout.PresetFileStore{Path: path}
	app, _ := newPresetsApp(st, nil)
	out := app.CaptureRun(&ScriptedKeys{})
	scr := app.Top().(*presetsScreen)

	if scr.corruptWarn {
		t.Fatalf("drop warning armed the corrupt latch; frames:\n%s", out)
	}
	if strings.Contains(out, "is corrupt") {
		t.Fatalf("healthy-with-one-drop file rendered the corrupt claim; frames:\n%s", out)
	}
	if !strings.Contains(out, "has control/format runes") ||
		!strings.Contains(out, `"bad"`) {
		t.Fatalf("specific drop warning missing; frames:\n%s", out)
	}
	for _, want := range []string{"healthy", "healthy2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("healthy record %q not listed; frames:\n%s", want, out)
		}
	}

	// The overwrite gate must NOT be armed: the next store write proceeds
	// without the corrupt consent modal.
	wrote := false
	scr.gateCorruptSave(func() { wrote = true })
	if app.Modal() != nil {
		t.Fatalf("drop warning opened the corrupt consent gate; frames:\n%s", out)
	}
	if !wrote {
		t.Fatal("ungated write did not proceed")
	}
}

// TestPresetsDuplicateSeedIsNotCorrupt pins the F-12 half of R3-F-05: a file
// holding two reserved-name seed records renders the duplicate collapse
// warning as what it is, without the corrupt-file claim or the overwrite gate.
func TestPresetsDuplicateSeedIsNotCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	data := `{"presets":[` +
		`{"name":"(built-in defaults)","task":"review","n":2},` +
		`{"name":"(built-in defaults)","task":"security","n":3},` +
		`{"name":"healthy","task":"review","n":2}` +
		`]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("seed duplicate-seed preset file: %v", err)
	}
	st := &fanout.PresetFileStore{Path: path}
	app, _ := newPresetsApp(st, nil)
	out := app.CaptureRun(&ScriptedKeys{})
	scr := app.Top().(*presetsScreen)

	if scr.corruptWarn {
		t.Fatalf("duplicate-seed warning armed the corrupt latch; frames:\n%s", out)
	}
	if strings.Contains(out, "is corrupt") {
		t.Fatalf("duplicate-seed file rendered the corrupt claim; frames:\n%s", out)
	}
	if !strings.Contains(out, "more than one record named") {
		t.Fatalf("duplicate-seed disclosure missing; frames:\n%s", out)
	}
	if !strings.Contains(out, "healthy") {
		t.Fatalf("healthy record not listed; frames:\n%s", out)
	}
	wrote := false
	scr.gateCorruptSave(func() { wrote = true })
	if app.Modal() != nil {
		t.Fatalf("duplicate-seed warning opened the corrupt consent gate; frames:\n%s", out)
	}
	if !wrote {
		t.Fatal("ungated write did not proceed")
	}
}

// TestPresetsGenuinelyCorruptFileStillArmsGate pins that the corrupt latch and
// overwrite gate keep their exact pre-P16 contract for a real JSON parse
// failure (the R3-F-05 fix must move only the drop/duplicate class).
func TestPresetsGenuinelyCorruptFileStillArmsGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt preset file: %v", err)
	}
	st := &fanout.PresetFileStore{Path: path}
	app, _ := newPresetsApp(st, nil)
	out := app.CaptureRun(&ScriptedKeys{})
	scr := app.Top().(*presetsScreen)
	if !scr.corruptWarn {
		t.Fatalf("parse failure must still arm the corrupt latch; frames:\n%s", out)
	}
	if !strings.Contains(out, "is corrupt") {
		t.Fatalf("corrupt claim missing for a real parse failure; frames:\n%s", out)
	}
	wrote := false
	scr.gateCorruptSave(func() { wrote = true })
	if app.Modal() == nil {
		t.Fatalf("parse failure must still gate the first write; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if wrote {
		t.Fatal("declined corrupt gate ran the write")
	}
}
