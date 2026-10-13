package settings

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/mode"
)

// Plants for the F6 TUI masking rows, shaped like the CLI E5 rollback rows.
const (
	tuiAWSPlant = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
	tuiSkPlant  = "sk-ant-api03-PLANT0123456789abcdef"
	tuiRedact   = "[REDACTED]"
)

// newPlantTUI builds a TUI over a core whose Deps are overridden by mutate.
func newPlantTUI(t *testing.T, mutate func(d *Deps)) (*TUI, *fakeDeps) {
	t.Helper()
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	d := f.deps()
	if mutate != nil {
		mutate(&d)
	}
	c := NewCore(d)
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	return NewTUI(c, view, &ScriptedKeys{}, &ScriptedLines{}, &bytes.Buffer{}), f
}

// findItem jumps the cursor to the row with the given item id.
func findItem(t *testing.T, tui *TUI, id string) {
	t.Helper()
	for si, sec := range tui.view.Sections {
		for i, it := range sec.Items {
			if it.ID == id {
				tui.section = si
				tui.row = i
				return
			}
		}
	}
	t.Fatalf("item %q missing from the view", id)
}

// TestTUIRollbackErrorMasksPromptedBackupDir (F6): the rollback confirm path
// forwards the raw ReadFile PathError, which re-embeds the prompted backup
// dir; the frame message must render it masked (seeded with the prompted value
// and the resolved file), benign dirs readable.
func TestTUIRollbackErrorMasksPromptedBackupDir(t *testing.T) {
	t.Run("planted backup dir", func(t *testing.T) {
		tui, _ := newPlantTUI(t, func(d *Deps) {
			d.RollbackKeys = func(dir string) error {
				return fmt.Errorf("open %s: The system cannot find the file specified.", dir)
			}
		})
		findItem(t, tui, "doctor.rollback_keys")
		tui.lines = &ScriptedLines{Lines: []string{tuiAWSPlant}}
		tui.Step(KeyEnter) // prompt reads the plant and arms the danger gate
		if tui.pending == nil {
			t.Fatal("rollback prompt did not arm the danger gate")
		}
		tui.Step(KeyYes) // runs the op; the forwarded error is masked
		msg := tui.Frame().Message
		if strings.Contains(msg, tuiAWSPlant) {
			t.Errorf("rollback error frame echoes the planted key raw: %q", msg)
		}
		if !strings.Contains(msg, tuiRedact) {
			t.Errorf("rollback error frame missing the mask: %q", msg)
		}
	})

	t.Run("benign backup dir stays readable", func(t *testing.T) {
		tui, _ := newPlantTUI(t, func(d *Deps) {
			d.RollbackKeys = func(dir string) error {
				return fmt.Errorf("open %s: The system cannot find the file specified.", dir)
			}
		})
		findItem(t, tui, "doctor.rollback_keys")
		benign := filepath.Join(t.TempDir(), "backups")
		tui.lines = &ScriptedLines{Lines: []string{benign}}
		tui.Step(KeyEnter)
		tui.Step(KeyYes)
		msg := tui.Frame().Message
		if !strings.Contains(msg, benign) {
			t.Errorf("benign backup dir must stay readable in the frame: %q", msg)
		}
	})
}

// TestTUIPromptErrorMasksPromptedValue (F6, runPrompt/promptLine site): the
// add-json path error is a composite the library built from the prompted
// value; the frame must render it masked, benign paths readable.
func TestTUIPromptErrorMasksPromptedValue(t *testing.T) {
	tui, _ := newPlantTUI(t, nil)
	findItem(t, tui, "providers.add_json")
	tui.lines = &ScriptedLines{Lines: []string{tuiAWSPlant}}
	tui.Step(KeyEnter)
	msg := tui.Frame().Message
	if strings.Contains(msg, tuiAWSPlant) {
		t.Errorf("prompt error frame echoes the planted key raw: %q", msg)
	}
	if !strings.Contains(msg, tuiRedact) {
		t.Errorf("prompt error frame missing the mask: %q", msg)
	}

	// A benign failing path keeps its readable form (the JSON is invalid).
	tui2, _ := newPlantTUI(t, nil)
	findItem(t, tui2, "providers.add_json")
	tui2.lines = &ScriptedLines{Lines: []string{"not-json-at-all"}}
	tui2.Step(KeyEnter)
	if msg := tui2.Frame().Message; !strings.Contains(msg, "not-json-at-all") {
		t.Errorf("benign prompt value must stay readable in the frame: %q", msg)
	}
}

// TestTUIChoiceErrorMasksChoiceValue (F6, applyChoice site): a failing choice
// application echoes the chosen value inside the composite error; the frame
// must render it masked.
func TestTUIChoiceErrorMasksChoiceValue(t *testing.T) {
	tui, _ := newPlantTUI(t, nil)
	tui.choices = &choiceState{
		item:    Item{ID: "test.masked_choice", Label: "Masked choice"},
		choices: []string{tuiAWSPlant},
		after: func(v string) error {
			return fmt.Errorf("apply failed for %s", v)
		},
	}
	tui.Step(KeyEnter) // applies choices[0]
	msg := tui.Frame().Message
	if strings.Contains(msg, tuiAWSPlant) {
		t.Errorf("choice error frame echoes the planted value raw: %q", msg)
	}
	if !strings.Contains(msg, tuiRedact) {
		t.Errorf("choice error frame missing the mask: %q", msg)
	}
}

// TestTUIDoctorErrorMasksPlantedDetail (F6, sibling "doctor:" render site): a
// forwarded doctor error carrying a credential-shaped run must reach the frame
// masked by the generic credential patterns.
func TestTUIDoctorErrorMasksPlantedDetail(t *testing.T) {
	tui, _ := newPlantTUI(t, func(d *Deps) {
		d.Doctor = func(out io.Writer, strict bool) error {
			return fmt.Errorf("probe failed for %s", tuiSkPlant)
		}
	})
	findItem(t, tui, "doctor.diagnose")
	tui.Step(KeyEnter)
	msg := tui.Frame().Message
	if strings.Contains(msg, tuiSkPlant) {
		t.Errorf("doctor error frame echoes the planted key raw: %q", msg)
	}
	if !strings.Contains(msg, tuiRedact) {
		t.Errorf("doctor error frame missing the mask: %q", msg)
	}
	if !strings.Contains(msg, "doctor: probe failed for") {
		t.Errorf("doctor error sentence wording must stay intact: %q", msg)
	}
}
