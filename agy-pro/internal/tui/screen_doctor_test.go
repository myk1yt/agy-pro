package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/cellwidth"
	"agypro/internal/mode"
	"agypro/internal/settings"
)

// doctorAlphaEnv / doctorUnsetEnv are the env-var NAMES the doctor tests
// register. The set one's VALUE must never appear in any frame; the unset
// one must render as "(unset)".
const (
	doctorAlphaEnv = "AGY_P11_ALPHA_KEY"
	doctorUnsetEnv = "AGY_P11_UNSET_KEY_QZX"
	doctorAlphaVal = "p11-secret-value-xyzzy"
)

// doctorTestEnv points AGY_PRO_HOME at a temp dir and writes a state.json
// (mode full, profile native) plus a providers.json (2 providers, 3 models,
// one CJK provider name for the alignment test).
func doctorTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv("AGY_PRO_PROVIDERS", "")
	t.Setenv("AGY_PRO_REAL_AGY", "")

	st := mode.State{
		CurrentMode:    "full",
		DefaultProfile: "native",
		ManagedItems:   append([]string(nil), mode.DefaultManagedItems...),
		UpdatedAt:      time.Now().UTC(),
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "state.json"), data, 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	reg := `{"providers":[
		{"name":"alpha","baseURL":"https://alpha.example.com","apiKeyEnv":"` + doctorAlphaEnv + `","models":[{"id":"a1"},{"id":"a2"}]},
		{"name":"プロバイダ","baseURL":"https://beta.example.com","apiKeyEnv":"` + doctorUnsetEnv + `","models":[{"id":"b1"}]}
	]}`
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(reg), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	return home
}

// unsetDoctorEnv guarantees the named variable resolves empty for the test
// duration (process env and, by the unique name, persistent stores).
func unsetDoctorEnv(t *testing.T, name string) {
	t.Helper()
	if v, ok := os.LookupEnv(name); ok {
		os.Unsetenv(name)
		t.Cleanup(func() { _ = os.Setenv(name, v) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(name) })
	}
}

// doctorTestCore builds a Core whose Doctor seam prints the given lines and
// returns diagErr; no live doctor, network probe, or file write can run.
func doctorTestCore(lines []string, diagErr error) *settings.Core {
	return settings.NewCore(settings.Deps{
		Doctor: func(out io.Writer, strict bool) error {
			for _, l := range lines {
				fmt.Fprintln(out, l)
			}
			return diagErr
		},
	})
}

// TestDoctorStatusPane pins the Status pane: state.json fields, the real
// agy path row, the registry summary (provider/model counts), and key
// env-var NAMES with resolved/unset status. The resolved key VALUE must
// never appear, and the Doctor pane output must stay hidden before Tab.
func TestDoctorStatusPane(t *testing.T) {
	doctorTestEnv(t)
	t.Setenv(doctorAlphaEnv, doctorAlphaVal)
	unsetDoctorEnv(t, doctorUnsetEnv)

	app := NewApp(80, 24, DirLTR)
	app.Push(NewDoctorScreen(doctorTestCore([]string{"DIAG-CHECK-1"}, nil)))
	out := app.CaptureRun(&ScriptedKeys{})

	for _, want := range []string{
		"Profile Mode", "full",
		"Default Launch Profile", "native",
		"Real agy Path",
		"2 providers", "3 models",
		"alpha", "プロバイダ",
		doctorAlphaEnv, "(resolved)",
		doctorUnsetEnv, "(unset)",
		doctorTabStatus, "Arrows and PgUp/PgDn scroll. This screen is read-only.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status pane missing %q; frames:\n%s", want, out)
		}
	}
	if strings.Contains(out, doctorAlphaVal) {
		t.Fatalf("key VALUE leaked into frames:\n%s", out)
	}
	if strings.Contains(out, "DIAG-CHECK-1") {
		t.Fatalf("doctor pane visible before Tab; frames:\n%s", out)
	}
}

// TestDoctorTabTogglesPanes pins the Tab contract: frame 1 shows the Status
// tab marker, after Tab the Doctor tab marker and the diagnose output
// appear.
func TestDoctorTabTogglesPanes(t *testing.T) {
	doctorTestEnv(t)
	t.Setenv(doctorAlphaEnv, doctorAlphaVal)
	unsetDoctorEnv(t, doctorUnsetEnv)

	app := NewApp(80, 24, DirLTR)
	app.Push(NewDoctorScreen(doctorTestCore([]string{"DIAG-CHECK-1"}, nil)))
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab}})

	if !strings.Contains(out, "[Status]") {
		t.Fatalf("frame 1 missing [Status] tab marker; frames:\n%s", out)
	}
	if !strings.Contains(out, "[Doctor]") {
		t.Fatalf("frame 2 missing [Doctor] tab marker; frames:\n%s", out)
	}
	if !strings.Contains(out, "DIAG-CHECK-1") {
		t.Fatalf("doctor pane missing diagnose output after Tab; frames:\n%s", out)
	}
}

// TestDoctorPaneScrollsByPgDn pins read-only scrolling: with a small frame
// the tail line is off-screen at first and reaches the window after
// repeated PgDn presses on the Doctor pane.
func TestDoctorPaneScrollsByPgDn(t *testing.T) {
	doctorTestEnv(t)
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("DIAG-%02d", i)
	}

	baseline := NewApp(80, 12, DirLTR)
	baseline.Push(NewDoctorScreen(doctorTestCore(lines, nil)))
	baseOut := baseline.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab}})
	if strings.Contains(baseOut, "DIAG-39") {
		t.Fatalf("tail visible before scrolling; frames:\n%s", baseOut)
	}

	app := NewApp(80, 12, DirLTR)
	app.Push(NewDoctorScreen(doctorTestCore(lines, nil)))
	var keys []Key
	keys = append(keys, KeyTab)
	// page = 12-8 = 4; nine pages land the window on lines 36..39.
	for i := 0; i < 9; i++ {
		keys = append(keys, KeyPgDn)
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "DIAG-39") {
		t.Fatalf("PgDn did not reach the diagnose tail; frames:\n%s", out)
	}
}

// TestDoctorScreenNeverWrites pins the read-only rule: driving digits,
// Enter, y/n, Space, Ctrl+S, Tab, and scroll keys over the screen leaves
// providers.json and state.json byte-identical.
func TestDoctorScreenNeverWrites(t *testing.T) {
	home := doctorTestEnv(t)
	t.Setenv(doctorAlphaEnv, doctorAlphaVal)
	unsetDoctorEnv(t, doctorUnsetEnv)

	beforeReg, err := os.ReadFile(filepath.Join(home, "providers.json"))
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	beforeState, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}

	app := NewApp(80, 24, DirLTR)
	app.Push(NewDoctorScreen(doctorTestCore([]string{"DIAG-X"}, nil)))
	app.CaptureRun(&ScriptedKeys{Keys: []Key{
		KeyTab, KeyEnter, Digit(1), Digit(9), KeyYes, KeyNo, KeySpace, KeyCtrlS,
		KeyDown, KeyUp, KeyPgDn, KeyPgUp, KeyHome, KeyEnd,
	}})

	afterReg, err := os.ReadFile(filepath.Join(home, "providers.json"))
	if err != nil {
		t.Fatalf("re-read providers.json: %v", err)
	}
	afterState, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err != nil {
		t.Fatalf("re-read state.json: %v", err)
	}
	if !bytes.Equal(beforeReg, afterReg) {
		t.Fatal("providers.json changed after driving the read-only screen")
	}
	if !bytes.Equal(beforeState, afterState) {
		t.Fatal("state.json changed after driving the read-only screen")
	}
}

// TestDoctorCJKRowsAlignThroughCellwidth pins the layout contract: the CJK
// provider row and the ASCII provider row place their value column at the
// same CELL offset even though their byte offsets differ.
func TestDoctorCJKRowsAlignThroughCellwidth(t *testing.T) {
	doctorTestEnv(t)
	t.Setenv(doctorAlphaEnv, doctorAlphaVal)
	unsetDoctorEnv(t, doctorUnsetEnv)

	scr := NewDoctorScreen(doctorTestCore(nil, nil)).(*doctorScreen)
	labelW := 0
	for _, f := range scr.status {
		if cw := cellwidth.String(f.label); cw > labelW {
			labelW = cw
		}
	}

	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	out := app.CaptureRun(&ScriptedKeys{})

	alphaLine := doctorLineContaining(t, out, doctorAlphaEnv)
	cjkLine := doctorLineContaining(t, out, doctorUnsetEnv)
	alphaIdx := strings.Index(alphaLine, doctorAlphaEnv)
	cjkIdx := strings.Index(cjkLine, doctorUnsetEnv)
	if alphaIdx < 0 || cjkIdx < 0 {
		t.Fatalf("env names not found in their rows: %q / %q", alphaLine, cjkLine)
	}
	alphaCells := cellwidth.String(alphaLine[:alphaIdx])
	cjkCells := cellwidth.String(cjkLine[:cjkIdx])
	if alphaCells != cjkCells {
		t.Fatalf("value columns misaligned: alpha at cell %d, CJK at cell %d\n%s", alphaCells, cjkCells, out)
	}
	if alphaCells != labelW+2 {
		t.Fatalf("value column at cell %d, want labelW+2 = %d", alphaCells, labelW+2)
	}
	if alphaIdx == cjkIdx {
		t.Fatalf("byte offsets equal (%d); the test must exercise CJK width handling", alphaIdx)
	}
}

// TestDoctorStructuralFailureTail pins the error tail: a diagnose run that
// returns an error renders its masked message at the end of the Doctor
// pane.
func TestDoctorStructuralFailureTail(t *testing.T) {
	doctorTestEnv(t)
	app := NewApp(80, 24, DirLTR)
	app.Push(NewDoctorScreen(doctorTestCore(nil, errors.New("boom structural"))))
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab}})
	if !strings.Contains(out, "Doctor reported structural failures: boom structural") {
		t.Fatalf("missing structural-failure tail; frames:\n%s", out)
	}
	// The fix commands render as read-only hints.
	for _, want := range []string{"agy pro settings doctor fix-keys all", "agy pro settings doctor rollback-keys <backup-dir>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing fix hint %q; frames:\n%s", want, out)
		}
	}
}

// doctorLineContaining returns the single frame line containing s.
func doctorLineContaining(t *testing.T, out, s string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, s) {
			return line
		}
	}
	t.Fatalf("no line contains %q; frames:\n%s", s, out)
	return ""
}
