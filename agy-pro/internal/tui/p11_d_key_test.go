package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"agypro/internal/fanout"
)

func TestPresetsDKeyResetGateAndCancelWritesNothing(t *testing.T) {
	st, pf := presetsFixture(t)
	pf.Presets = append(pf.Presets, fanout.Preset{
		Name:  fanout.BuiltinPresetName,
		Seats: "native:gemini-3.8-flash-high",
	})
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("save seed preset: %v", err)
	}
	before, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatal(err)
	}

	app, _ := newPresetsApp(st, nil)
	app.Step(KeySpace)
	app.Step(RuneKey('D'))
	if app.Modal() == nil {
		t.Fatal("D on the selected built-in row did not open the reset gate")
	}
	gate := strings.Join(app.Modal().Body(100), "\n")
	t.Logf("rendered reset gate:\n%s", gate)
	want := Bundle().Tf("presets.reset.body", map[string]string{"name": fanout.BuiltinPresetName})
	if !strings.Contains(gate, want) {
		t.Fatalf("reset gate = %q, want body %q", gate, want)
	}

	app.Step(KeyNo)
	if app.Modal() != nil {
		t.Fatal("cancelling the reset gate left a modal open")
	}
	if got, want := app.Message(), Bundle().T("presets.msg.cancelled"); got != want {
		t.Fatalf("cancel message = %q, want %q", got, want)
	}
	after, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("cancelled D reset changed the preset file\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestPresetsDKeyMatchesOrdinaryDeleteAction(t *testing.T) {
	st, _ := presetsFixture(t)
	openGate := func(keys []Key) string {
		app, _ := newPresetsApp(st, nil)
		for _, k := range keys {
			app.Step(k)
		}
		if app.Modal() == nil {
			t.Fatalf("keys %v did not open the delete gate", keys)
		}
		return strings.Join(app.Modal().Body(100), "\n")
	}

	byD := openGate([]Key{KeyDown, Key("d")})
	byDigit := openGate([]Key{KeyDown, Digit(7)})
	if byD != byDigit {
		t.Fatalf("D gate differs from the Delete action row\nD:\n%s\ndigit:\n%s", byD, byDigit)
	}
	if !strings.Contains(byD, "alpha") {
		t.Fatalf("ordinary D delete gate does not name the selection: %q", byD)
	}
}

func TestPresetsDKeyWithoutSelectionUsesSelectFirst(t *testing.T) {
	st, _ := presetsFixture(t)
	app, _ := newPresetsApp(st, nil)
	app.Step(Key("d"))
	if app.Modal() != nil {
		t.Fatal("D without a selection opened a gate")
	}
	if got, want := app.Message(), Bundle().T("presets.msg.select_first"); got != want {
		t.Fatalf("D without selection message = %q, want %q", got, want)
	}
}
