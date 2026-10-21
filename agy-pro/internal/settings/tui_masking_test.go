package settings

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// Plants for the F6 TUI masking rows, shaped like the CLI E5 rollback rows.
const (
	tuiAWSPlant = testfixtures.AWSSecretNoSlash
	tuiSkPlant  = testfixtures.AnthropicPlanted
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

	// Batch N (M6 preemptive, adjacent-pattern closure): the rollback danger
	// gate seeded the frame mask with the whole prompted dir and the resolved
	// file only. A location-shaped whole operand ("./backups,<secret>") never
	// seeds (SeedEligible's location exemption), so MaskSentence fell back to
	// MaskOperand, whose generic patterns do not cover a bare secret token:
	// the forwarded PathError re-echoed it raw. The gate now takes the
	// batch-M composition (providers.FlagSeedOperands), mirroring the CLI
	// rollback twin (cmd/agy pro_settings.go).
	//
	// The pin asserts the FULL frame message. Without the fix the raw plant
	// reaches the frame through the sentence and the equality pin fails.
	t.Run("comma-decorated operand seeds the composite tail", func(t *testing.T) {
		tui, _ := newPlantTUI(t, func(d *Deps) {
			d.RollbackKeys = func(dir string) error {
				return fmt.Errorf("open %s: The system cannot find the file specified.", providers.ResolveBackupFile(dir))
			}
		})
		findItem(t, tui, "doctor.rollback_keys")
		operand := "./backups," + tuiAWSPlant
		tui.lines = &ScriptedLines{Lines: []string{operand}}
		tui.Step(KeyEnter)
		tui.Step(KeyYes)
		msg := tui.Frame().Message
		want := "error: " + strings.ReplaceAll(
			fmt.Sprintf("open %s: The system cannot find the file specified.", providers.ResolveBackupFile(operand)),
			tuiAWSPlant, tuiRedact)
		if msg != want {
			t.Errorf("full rollback error frame drifted:\ngot:  %q\nwant: %q", msg, want)
		}
		if strings.Contains(msg, tuiAWSPlant) {
			t.Errorf("rollback error frame leaks the planted secret %q: %q", tuiAWSPlant, msg)
		}
	})
}

// Batch N (M6 preemptive, adjacent-pattern closure): the delete danger gate
// seeded the frame mask with the whole prompted name only, so a
// location-shaped decorated operand ("./backups,<secret>") never seeded
// (SeedEligible's location exemption) and the composite not-found error
// re-echoed the bare secret token raw. The gate now takes the batch-M
// composition (providers.FlagSeedOperands), mirroring the promptLine twin.
//
// The pin asserts the FULL frame message. Without the fix the raw plant
// reaches the frame through the sentence and the equality pin fails.
func TestTUIDeleteGateCompositeTailSeedsTokens(t *testing.T) {
	tui, _ := newPlantTUI(t, nil)
	findItem(t, tui, "providers.delete")
	operand := "./backups," + tuiAWSPlant
	tui.lines = &ScriptedLines{Lines: []string{operand}}
	tui.Step(KeyEnter) // prompt reads the operand and arms the danger gate
	if tui.pending == nil {
		t.Fatal("delete prompt did not arm the danger gate")
	}
	tui.Step(KeyYes) // runs the op; the forwarded not-found error is masked
	msg := tui.Frame().Message
	// providers.DeleteProvider quotes the (already MaskOperand-passed, here
	// unchanged) name; the seed masks the plant inside the quotes.
	want := "error: " + strings.ReplaceAll(
		fmt.Sprintf("provider %q not found", providers.MaskOperand(operand)),
		tuiAWSPlant, tuiRedact)
	if msg != want {
		t.Errorf("full delete error frame drifted:\ngot:  %q\nwant: %q", msg, want)
	}
	if strings.Contains(msg, tuiAWSPlant) {
		t.Errorf("delete error frame leaks the planted secret %q: %q", tuiAWSPlant, msg)
	}

	// The other side of the composition: a benign comma list seeds no token,
	// so the added per-token seeds must not start masking ordinary values.
	tui2, _ := newPlantTUI(t, nil)
	findItem(t, tui2, "providers.delete")
	const benign = "read,search"
	tui2.lines = &ScriptedLines{Lines: []string{benign}}
	tui2.Step(KeyEnter)
	tui2.Step(KeyYes)
	wantBenign := "error: " + fmt.Sprintf("provider %q not found", providers.MaskOperand(benign))
	if msg2 := tui2.Frame().Message; msg2 != wantBenign {
		t.Errorf("benign delete error frame drifted:\ngot:  %q\nwant: %q", msg2, wantBenign)
	}
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

// Batch N (round-5 M6 preemptive): the promptLine composite tail seeded the
// frame mask with the WHOLE prompted value only. A location-shaped whole
// operand ("./backups,<secret>") never seeds (SeedEligible's location
// exemption), so MaskSentence fell back to MaskOperand, whose generic
// patterns do not cover a bare secret token: the read error sentence
// re-echoed it raw. promptLine now takes the batch-M composition
// (providers.FlagSeedOperands), mirroring the doctor.fix_keys gate and the
// CLI twins.
//
// The pin asserts the FULL frame message. Without the fix the raw plant
// reaches the frame through the sentence and the equality pin fails.
func TestTUIPromptErrorCompositeTailSeedsTokens(t *testing.T) {
	tui, _ := newPlantTUI(t, nil)
	findItem(t, tui, "providers.add_json")
	operand := "./backups," + tuiAWSPlant
	tui.lines = &ScriptedLines{Lines: []string{operand}}
	tui.Step(KeyEnter)
	msg := tui.Frame().Message
	// The expected full message: the read error sentence with the plant
	// replaced by the redaction marker. readSpecSource wraps os.ReadFile's
	// error, so the wording comes from a real read of the same path.
	_, readErr := os.ReadFile(strings.TrimSpace(operand))
	if readErr == nil {
		t.Fatalf("os.ReadFile(%q) unexpectedly succeeded", operand)
	}
	want := "error: " + strings.ReplaceAll("read spec file: "+readErr.Error(), tuiAWSPlant, tuiRedact)
	if msg != want {
		t.Errorf("full prompt error frame drifted:\ngot:  %q\nwant: %q", msg, want)
	}
	if strings.Contains(msg, tuiAWSPlant) {
		t.Errorf("prompt error frame leaks the planted secret %q: %q", tuiAWSPlant, msg)
	}

	// The other side of the composition: a benign comma list seeds no token,
	// so the added per-token seeds must not start masking ordinary values.
	tui2, _ := newPlantTUI(t, nil)
	findItem(t, tui2, "providers.add_json")
	const benign = "read,search"
	tui2.lines = &ScriptedLines{Lines: []string{benign}}
	tui2.Step(KeyEnter)
	if msg2 := tui2.Frame().Message; msg2 != "error: read spec file: "+mustReadErr(t, benign).Error() {
		t.Errorf("benign prompt error frame drifted:\ngot:  %q", msg2)
	}
}

// mustReadErr returns the os.ReadFile error for a path the test expects to be
// unreadable (the full-line pins build their expectation from the real OS
// wording, so they stay byte-exact and platform-independent).
func mustReadErr(t *testing.T, path string) error {
	t.Helper()
	_, err := os.ReadFile(path)
	if err == nil {
		t.Fatalf("os.ReadFile(%q) unexpectedly succeeded", path)
	}
	return err
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
