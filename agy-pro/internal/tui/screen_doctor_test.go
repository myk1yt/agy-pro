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
	"agypro/internal/testfixtures"
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

	doctorWriteState(t, home, "full")

	reg := `{"providers":[
		{"name":"alpha","baseURL":"https://alpha.example.com","apiKeyEnv":"` + doctorAlphaEnv + `","models":[{"id":"a1"},{"id":"a2"}]},
		{"name":"プロバイダ","baseURL":"https://beta.example.com","apiKeyEnv":"` + doctorUnsetEnv + `","models":[{"id":"b1"}]}
	]}`
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(reg), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	return home
}

// doctorWriteState overwrites state.json in home with the given current mode
// (profile native, the default managed-item set), so a refresh test can change
// the state under an open screen and prove the Status snapshot is retaken.
func doctorWriteState(t *testing.T, home, modeName string) {
	t.Helper()
	st := mode.State{
		CurrentMode:    modeName,
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
		RuneKey('r'), Key("r"), RuneKey('R'),
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

// TestDoctorRefreshKeyRerunsDiagnose pins the D9-9B refresh fix: r re-runs the
// injected diagnose seam and the Doctor pane shows the fresh run instead of
// the construction snapshot, without leaving the screen. Both key spellings
// the seams produce (the console rune carrier and the plain spelling) are
// driven.
func TestDoctorRefreshKeyRerunsDiagnose(t *testing.T) {
	doctorTestEnv(t)
	calls := 0
	core := settings.NewCore(settings.Deps{
		Doctor: func(out io.Writer, strict bool) error {
			calls++
			fmt.Fprintf(out, "DIAG-RUN-%d", calls)
			return nil
		},
	})
	scr := NewDoctorScreen(core)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)

	app.Frame() // the construction snapshot
	app.Step(KeyTab)
	if calls != 1 {
		t.Fatalf("diagnose ran %d times before any r press, want 1", calls)
	}
	before := app.Frame().String()
	if !strings.Contains(before, "DIAG-RUN-1") {
		t.Fatalf("Doctor pane missing the construction snapshot; frames:\n%s", before)
	}

	for _, key := range []Key{RuneKey('r'), Key("r")} {
		app.Step(key)
	}
	if calls != 3 {
		t.Fatalf("diagnose ran %d times after two r presses, want 3", calls)
	}
	after := app.Frame().String()
	if !strings.Contains(after, "DIAG-RUN-3") {
		t.Fatalf("Doctor pane missing the refreshed diagnose output; frames:\n%s", after)
	}
	if strings.Contains(after, "DIAG-RUN-2") || strings.Contains(after, "DIAG-RUN-1") {
		t.Fatalf("Doctor pane still carries a stale diagnose line; frames:\n%s", after)
	}
	if app.Depth() != 1 || app.Top() != scr {
		t.Fatalf("r must not close the screen (depth %d)", app.Depth())
	}
}

// TestDoctorRefreshRetakesStatusSnapshot pins the other half of the refresh: r
// recomposes the Status pane from state.json, so a state change made while the
// screen is open is visible after one press on the Status pane.
func TestDoctorRefreshRetakesStatusSnapshot(t *testing.T) {
	home := doctorTestEnv(t)
	app := NewApp(80, 24, DirLTR)
	app.Push(NewDoctorScreen(doctorTestCore([]string{"DIAG-X"}, nil)))

	label := Bundle().T("view.current_mode.label")
	before := app.Frame().String()
	if line := doctorLineContaining(t, before, label); !strings.Contains(line, "full") {
		t.Fatalf("Status pane does not show the stored mode before the refresh: %q", line)
	}

	doctorWriteState(t, home, "clean")
	app.Step(RuneKey('r'))

	after := app.Frame().String()
	line := doctorLineContaining(t, after, label)
	if !strings.Contains(line, "clean") {
		t.Fatalf("Status pane was not recomposed by r: %q\n%s", line, after)
	}
	if strings.Contains(line, "full") {
		t.Fatalf("Status pane still carries the stale mode: %q", line)
	}
}

// TestDoctorFixHintNamesTUIAndCLIPaths pins the D9-9B rewording: the repair
// hint names the in-TUI settings path first and keeps the CLI commands as the
// alternative, so this screen and the settings screen no longer disagree about
// whether repair is possible inside the TUI. The tab bar and the scroll hint
// stay unchanged, and the same block makes the refresh gesture discoverable.
func TestDoctorFixHintNamesTUIAndCLIPaths(t *testing.T) {
	doctorTestEnv(t)
	scr := NewDoctorScreen(doctorTestCore(nil, nil)).(*doctorScreen)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyTab}})

	for _, want := range []string{
		doctorFixTUIPath,
		"Hub > Settings > [5] Diagnostics & Keys > Repair Key Names (--fix-keys)",
		doctorFixCLI,
		doctorFixKeysCmd,
		doctorRollbackCmd,
		"press r to re-run diagnose",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("repair hint missing %q; frames:\n%s", want, out)
		}
	}
	if strings.Contains(out, "This screen is read-only. To repair key names, run in your terminal:") {
		t.Fatalf("the old CLI-only repair hint is still rendered; frames:\n%s", out)
	}
	if got, want := scr.tabBar(), " Status  | [Doctor]"; got != want {
		t.Fatalf("tab bar = %q, want %q (the refresh fix must not touch it)", got, want)
	}
	if !strings.Contains(out, scr.bundle.T("doctor.scroll_hint")) {
		t.Fatalf("scroll hint changed or missing; frames:\n%s", out)
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

// TestDoctorProviderNameSinkUsesSlotAuthority (fix7-D) pins the doctor screen's
// provider-NAME sink against the registry-slot display authority. Identity
// half: the benign corpus (openai, gemini, deepseek, nw, charm) renders as a
// Status label byte-identically. Credential half: the underscore-headed plant,
// which providers.MaskOperand leaves raw (its "_" breaks the contiguous-run
// test and its head is not in the shared prefix list), renders masked
// first-5 + **** and never raw in any frame.
func TestDoctorProviderNameSinkUsesSlotAuthority(t *testing.T) {
	const (
		plant = testfixtures.SKLive
		mask  = "sk_li****"
	)
	// The required names plus the separator-bearing forms the pre-existing
	// doctor pins exercise (anthropic, acme-corp, my_provider, cedar.2024).
	corpus := []string{
		"openai", "gemini", "deepseek", "nw", "charm",
		"anthropic", "acme-corp", "my_provider", "cedar.2024",
	}

	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv("AGY_PRO_PROVIDERS", "")
	t.Setenv("AGY_PRO_REAL_AGY", "")
	doctorWriteState(t, home, "full")

	var entries []string
	for _, n := range append(corpus, plant) {
		entries = append(entries, `{"name":"`+n+`","baseURL":"https://gw.example/v1","models":[{"id":"m1"}]}`)
	}
	reg := `{"providers":[` + strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(reg), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}

	scr := NewDoctorScreen(doctorTestCore(nil, nil)).(*doctorScreen)
	labels := make(map[string]bool, len(scr.status))
	for _, f := range scr.status {
		labels[f.label] = true
	}
	for _, n := range corpus {
		if !labels[n] {
			t.Errorf("Status pane lost the %q provider label", n)
		}
	}
	if !labels[mask] {
		t.Errorf("Status pane lacks the masked provider label %q (labels: %v)", mask, labels)
	}
	for label := range labels {
		if strings.Contains(label, plant) {
			t.Errorf("Status pane renders the credential-shaped provider name raw: %q", label)
		}
	}

	app := NewApp(120, 40, DirLTR)
	app.Push(scr)
	out := app.CaptureRun(&ScriptedKeys{})
	if strings.Contains(out, plant) {
		t.Fatalf("the plant rendered raw in a doctor frame\n%s", out)
	}
	// The frame is a window over the Status rows (an overflowing pane scrolls),
	// so the frame check carries the plant's mask and one benign neighbour;
	// every corpus label is pinned above, from the composed snapshot itself.
	for _, want := range []string{mask, "gemini"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor frame missing %q\n%s", want, out)
		}
	}
}
