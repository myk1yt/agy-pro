package tui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// presetsFixture is the standard two-preset store every list test reads:
// alpha (full flag summary, the store default) and beta (n only). All tests
// run against a temp AGY_PRO_HOME; the live presets file is never touched.
func presetsFixture(t *testing.T) (*fanout.PresetFileStore, *fanout.PresetsFile) {
	t.Helper()
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	pf := &fanout.PresetsFile{
		Presets: []fanout.Preset{
			{Name: "alpha", Task: "review", N: 4, TimeoutSeconds: 120, Tools: "read,search"},
			{Name: "beta", N: 2},
		},
		DefaultPreset: "alpha",
	}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("seed presets fixture: %v", err)
	}
	return st, pf
}

// reloadPresets reads the on-disk presets file for byte-level assertions.
func reloadPresets(t *testing.T, st *fanout.PresetFileStore) *fanout.PresetsFile {
	t.Helper()
	pf, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("reload presets: %v", err)
	}
	return pf
}

// newPresetsApp wires the preset list with an openEditor spy over a stub
// root, so Esc and pop flows have somewhere to land.
func newPresetsApp(st fanout.PresetStore, spy func(name string, a *App)) (*App, *[]string) {
	var opened []string
	app := NewApp(100, 30, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPresetsScreen(st, func(name string, a *App) {
		opened = append(opened, name)
		if spy != nil {
			spy(name, a)
		}
	}))
	return app, &opened
}

// presetsKeys drives keys on the preset list and returns the captured frames.
func presetsKeys(t *testing.T, st fanout.PresetStore, keys []Key) (string, *[]string) {
	t.Helper()
	app, opened := newPresetsApp(st, nil)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	return out, opened
}

// TestPresetsListRendersSummariesAndDefault pins the row inventory: the
// built-in row with its engine-default summary, one row per preset with the
// one-line flag summary, the default badge on the default preset, and the
// status line naming the default.
func TestPresetsListRendersSummariesAndDefault(t *testing.T) {
	st, _ := presetsFixture(t)
	out, _ := presetsKeys(t, st, nil)
	for _, want := range []string{
		fanout.BuiltinPresetName,
		"task=review n=3 timeout=300s tools=read,search",
		"alpha",
		"task=review n=4 timeout=120s tools=read,search",
		"beta",
		"n=2",
		"default",
		"Default preset: alpha",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list frame missing %q; frames:\n%s", want, out)
		}
	}
}

// TestPresetsExplainLinesNameTheObject pins the U4 explanation half on the
// list screen (survey 2.7): the frame names what a fanout preset is (a named
// run config, not a prompt, not a seat selection), the consumer command
// (agy pro fanout --preset <name>), the env var (AGY_PRO_FANOUT_PRESET), the
// on-disk path (the store's own path truth), and the default_preset rule
// including the P3-1 conflict-drop warning.
func TestPresetsExplainLinesNameTheObject(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	// The store lives in a temp dir; the File: line is asserted through the
	// same cellwidth truncation Draw applies, so the assertion holds whatever
	// the temp path length (a short path renders whole, a long one truncates
	// its own tail and never the rule lines below it).
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&fanout.PresetsFile{Presets: []fanout.Preset{{Name: "alpha", N: 2}}}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	out, _ := presetsKeys(t, st, nil)
	for _, want := range []string{
		"A preset is a named run config (seat/tool/time budgets), not a prompt.",
		"A preset may pin the seat lineup. Run: agy pro fanout --preset <name>",
		"AGY_PRO_FANOUT_PRESET",
		cellwidth.Truncate("File: "+st.Path, 100),
		"default_preset applies when no preset is named; conflicts drop with a warning.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list frame missing explanation line %q; frames:\n%s", want, out)
		}
	}
}

// TestPresetsExplainLinesDropOnShortConsole pins the frame-budget rule: the
// explanation block is the first body content a short console loses. At 80x12
// (below the 80x24 budget) the prose is gone, and the list hint and the base
// footer still survive; at 80x24 the prose is present and the frame is still
// exactly 24 lines with page size 24-presetsListChrome.
func TestPresetsExplainLinesDropOnShortConsole(t *testing.T) {
	st, _ := presetsFixture(t)

	short, scr := presetsAppAt(st, 80, 12)
	short.CaptureRun(&ScriptedKeys{})
	shortBody := strings.Join(short.Frame().Lines, "\n")
	if strings.Contains(shortBody, "named run config") {
		t.Fatalf("explanation block must drop on a 12-row console; frames:\n%s", shortBody)
	}
	if !strings.Contains(shortBody, scr.list.FooterHint()) || !strings.Contains(shortBody, baseFooter) {
		t.Fatalf("hint or base footer cut on a 12-row console; frames:\n%s", shortBody)
	}

	full, _ := presetsAppAt(presetsBulkStore(t, 13), 80, 24)
	full.CaptureRun(&ScriptedKeys{})
	fullBody := strings.Join(full.Frame().Lines, "\n")
	if !strings.Contains(fullBody, "named run config") {
		t.Fatalf("explanation block missing at 80x24; frames:\n%s", fullBody)
	}
	if n := len(full.Frame().Lines); n != 24 {
		t.Fatalf("80x24 frame = %d lines, want 24; frames:\n%s", n, fullBody)
	}
}

// TestPresetFlagSummaryHonestAboutHiddenPins pins defect D10: a preset that
// pins only fields the one-line summary hides (seats, models, out_dir, json)
// must not render the same "(empty)" as a truly empty preset; it renders the
// pinned-count digest, while a name-only preset keeps the truthful "(empty)"
// and visible fields still win.
func TestPresetFlagSummaryHonestAboutHiddenPins(t *testing.T) {
	b := i18n.Load("en")
	cases := []struct {
		name   string
		preset fanout.Preset
		want   string
	}{
		{"seats only", fanout.Preset{Name: "x", Seats: "native:gemini-x"}, "1 of 13 fields pinned: see Edit"},
		{"seats+out_dir+json", fanout.Preset{Name: "x", Seats: "native:gemini-x", OutDir: "reports", JSON: true}, "3 of 13 fields pinned: see Edit"},
		{"truly empty", fanout.Preset{Name: "x"}, "(empty)"},
		{"nil preset", fanout.Preset{}, "(empty)"},
		{"visible still win", fanout.Preset{Name: "x", Task: "review", MaxNative: 4}, "task=review"},
		{"hidden plus visible", fanout.Preset{Name: "x", N: 2, Seats: "native:gemini-x"}, "n=2"},
	}
	for _, tc := range cases {
		p := tc.preset
		if got := presetFlagSummary(b, &p); got != tc.want {
			t.Errorf("%s: summary = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPresetsRowRendersHiddenFieldDigest pins the digest end to end on the
// composed list frame: the hidden-only preset row shows the pinned count and
// the truly empty row still shows "(empty)".
func TestPresetsRowRendersHiddenFieldDigest(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	pf := &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "hidden", Seats: "native:gemini-x"},
		{Name: "empty"},
	}}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	out, _ := presetsKeys(t, st, nil)
	for _, want := range []string{"hidden", "1 of 13 fields pinned: see Edit", "empty", "(empty)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list frame missing %q; frames:\n%s", want, out)
		}
	}
}

// TestPresetsDualNavigation pins requirement 2 on this list: a digit press
// and arrows+Enter activate the same row (the openEditor spy sees the same
// name from both paths).
func TestPresetsDualNavigation(t *testing.T) {
	st, _ := presetsFixture(t)
	_, digitOpened := presetsKeys(t, st, []Key{Digit(2)})
	_, arrowOpened := presetsKeys(t, st, []Key{KeyDown, KeyEnter})
	if len(*digitOpened) != 1 || len(*arrowOpened) != 1 {
		t.Fatalf("digit opened %v, arrows opened %v; want one row each", *digitOpened, *arrowOpened)
	}
	if (*digitOpened)[0] != "alpha" || (*arrowOpened)[0] != "alpha" {
		t.Fatalf("digit opened %q, arrows opened %q; want alpha from both",
			(*digitOpened)[0], (*arrowOpened)[0])
	}
}

// TestPresetsBuiltinRowOpensSeedEditor pins the fixed first row: Enter
// opens the editor with the REAL reserved name (fanout.BuiltinPresetName),
// which edits the seed record like any preset (contract A4: the sentinel is
// retired from the open path).
func TestPresetsBuiltinRowOpensSeedEditor(t *testing.T) {
	st, _ := presetsFixture(t)
	_, opened := presetsKeys(t, st, []Key{Digit(1)})
	if len(*opened) != 1 || (*opened)[0] != fanout.BuiltinPresetName {
		t.Fatalf("builtin row opened %v, want [%q]", *opened, fanout.BuiltinPresetName)
	}
}

// TestPresetsNewRowOpensEmptyEditor pins the New action: it opens the
// editor with an empty name.
func TestPresetsNewRowOpensEmptyEditor(t *testing.T) {
	st, _ := presetsFixture(t)
	_, opened := presetsKeys(t, st, []Key{Digit(4)})
	if len(*opened) != 1 || (*opened)[0] != "" {
		t.Fatalf("new row opened %v, want [empty name]", *opened)
	}
}

// TestPresetsEditUsesStickySelection pins the selection model: moving onto
// an action row keeps the preset selected under the cursor, so Edit opens
// that preset even after digit-style jumps.
func TestPresetsEditUsesStickySelection(t *testing.T) {
	st, _ := presetsFixture(t)
	// Down lands on alpha (selection arms); digit 5 then activates Edit.
	_, opened := presetsKeys(t, st, []Key{KeyDown, Digit(5)})
	if len(*opened) != 1 || (*opened)[0] != "alpha" {
		t.Fatalf("edit opened %v, want [alpha] (sticky selection)", *opened)
	}
}

// TestPresetsDuplicateWritesStore pins the Duplicate flow end to end: the
// copy lands in the file under a fresh name and the selection follows it.
func TestPresetsDuplicateWritesStore(t *testing.T) {
	st, _ := presetsFixture(t)
	// Down arms alpha (row 2); digit 6 activates Duplicate directly.
	out, _ := presetsKeys(t, st, []Key{KeyDown, Digit(6)})
	pf := reloadPresets(t, st)
	if len(pf.Presets) != 3 {
		t.Fatalf("after duplicate: %d presets, want 3: %+v", len(pf.Presets), pf.Presets)
	}
	if _, ok := pf.FindPreset("alpha copy"); !ok {
		t.Fatalf("copy missing on disk: %+v", pf.Presets)
	}
	if pf.DefaultPreset != "alpha" {
		t.Fatalf("default changed by duplicate: %q", pf.DefaultPreset)
	}
	if !strings.Contains(out, "Duplicated as alpha copy.") {
		t.Fatalf("duplicate message missing; frames:\n%s", out)
	}
}

// TestPresetsDuplicateRunsSaveGate pins s5 deviation 3: Duplicate creates a
// NEW record and writes it through SavePresets, which runs only the
// store-boundary rules (fanout.ValidatePresetForSave is the single save-time
// authority for the D13/D14 rules). A copy of a row that violates them must
// therefore be refused by the same authority before anything is written, and a
// copy of a valid row must still round-trip to disk unchanged.
func TestPresetsDuplicateRunsSaveGate(t *testing.T) {
	t.Run("refuses a copy of an invalid row", func(t *testing.T) {
		// SavePresets is deliberately permissive (the P3-1 default-key guard
		// seeds exactly this shape), so the unknown-task row below loads fine and
		// only Duplicate would turn it into a second unrunnable record.
		t.Setenv(providers.EnvHome, t.TempDir())
		path := filepath.Join(t.TempDir(), "fanout-presets.json")
		st := &fanout.PresetFileStore{Path: path}
		seed := &fanout.PresetsFile{Presets: []fanout.Preset{
			{Name: "typo", Task: "secuirty", Seats: "native:gemini-x"},
		}}
		if err := st.SavePresets(seed); err != nil {
			t.Fatalf("seed conflicting preset: %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read store before duplicate: %v", err)
		}

		// One preset row puts Duplicate at digit 5 (builtin, preset, new, edit,
		// duplicate); KeyDown arms the invalid row.
		app, opened := newPresetsApp(st, nil)
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(5)}})
		if len(*opened) != 0 {
			t.Fatalf("duplicate opened the editor: %v", *opened)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read store after duplicate: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("refused duplicate rewrote the store:\nbefore=%s\nafter=%s", before, after)
		}
		if pf := reloadPresets(t, st); len(pf.Presets) != 1 {
			t.Fatalf("refused duplicate persisted a record: %+v", pf.Presets)
		}
		// The refusal names the rule in the editor's own wording, under the
		// common.error label, so the two writers cannot describe the rule
		// differently. app.Message() is the untruncated text; the frame line
		// carries the same message cut to the console width.
		if want := Bundle().T("common.error") + ": " + presetsEditorValidationTask; app.Message() != want {
			t.Fatalf("refusal message = %q, want %q", app.Message(), want)
		}
		if !strings.Contains(out, "security") {
			t.Fatalf("refusal missing from the frame; frames:\n%s", out)
		}
		if strings.Contains(out, "Duplicated as") {
			t.Fatalf("refused duplicate still reported success; frames:\n%s", out)
		}
	})

	t.Run("valid copy round-trips", func(t *testing.T) {
		st, _ := presetsFixture(t)
		// Two preset rows put Duplicate at digit 6; KeyDown arms alpha.
		out, _ := presetsKeys(t, st, []Key{KeyDown, Digit(6)})
		pf := reloadPresets(t, st)
		cp, ok := pf.FindPreset("alpha copy")
		if !ok {
			t.Fatalf("valid copy missing on disk: %+v", pf.Presets)
		}
		if cp.Task != "review" || cp.N != 4 || cp.TimeoutSeconds != 120 || cp.Tools != "read,search" {
			t.Fatalf("copy lost fields: %+v", *cp)
		}
		if !strings.Contains(out, "Duplicated as alpha copy.") {
			t.Fatalf("duplicate message missing; frames:\n%s", out)
		}
	})
}

// TestPresetsDeleteGateYes pins the delete flow: the y/n gate opens, y
// removes the preset, clears a dangling default key, and persists.
func TestPresetsDeleteGateYes(t *testing.T) {
	st, _ := presetsFixture(t)
	// Down arms alpha (row 2); digit 7 activates Delete; y confirms the gate.
	out, _ := presetsKeys(t, st, []Key{KeyDown, Digit(7), KeyYes})
	pf := reloadPresets(t, st)
	if len(pf.Presets) != 1 || pf.Presets[0].Name != "beta" {
		t.Fatalf("after delete-y: %+v, want beta only", pf.Presets)
	}
	if pf.DefaultPreset != "" {
		t.Fatalf("dangling default not cleared: %q", pf.DefaultPreset)
	}
	if !strings.Contains(out, "Preset alpha deleted.") {
		t.Fatalf("delete message missing; frames:\n%s", out)
	}
}

// TestPresetsDeleteCancelWritesNothing pins the gate's cancel side: both n
// and Esc leave the on-disk file byte-identical in content.
func TestPresetsDeleteCancelWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  Key
	}{
		{"n", KeyNo},
		{"esc", KeyEsc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, before := presetsFixture(t)
			presetsKeys(t, st, []Key{KeyDown, Digit(7), tc.key})
			after := reloadPresets(t, st)
			if len(after.Presets) != len(before.Presets) || after.DefaultPreset != before.DefaultPreset {
				t.Fatalf("cancel wrote: before %+v, after %+v", before, after)
			}
			for i := range before.Presets {
				if after.Presets[i].Name != before.Presets[i].Name {
					t.Fatalf("cancel mutated preset %d: %q -> %q", i,
						before.Presets[i].Name, after.Presets[i].Name)
				}
			}
		})
	}
}

// TestPresetsSetDefaultWritesStoreKey pins the Set-default flow: the selected
// preset is armed, the M4 approval gate names the key change, and y rewrites the
// store's default_preset key so the status line follows. PIN MOVED (P5-1 U6): the
// healthy path used to write with no consent, so the key pressed here is the
// approval answer, not the write.
func TestPresetsSetDefaultWritesStoreKey(t *testing.T) {
	st, _ := presetsFixture(t)
	// Down Down arms beta (row 3); digit 8 activates Set default; y approves.
	out, _ := presetsKeys(t, st, []Key{KeyDown, KeyDown, Digit(8), KeyYes})
	pf := reloadPresets(t, st)
	if pf.DefaultPreset != "beta" {
		t.Fatalf("default_preset = %q, want beta", pf.DefaultPreset)
	}
	if !strings.Contains(out, "Default preset set to beta.") {
		t.Fatalf("set-default message missing; frames:\n%s", out)
	}
	if !strings.Contains(out, "Default preset: beta") {
		t.Fatalf("status line did not follow the new default; frames:\n%s", out)
	}
}

// TestPresetsCorruptFileWarnsAndGatesWrite pins the security F5 fix on the
// list screen: the corrupt-file warning is user-visible (frame body and
// message line, naming the on-disk path) and a store write is refused until
// the user explicitly acknowledges overwriting the corrupt file.
func TestPresetsCorruptFileWarnsAndGatesWrite(t *testing.T) {
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
		t.Fatal("list screen must record the corrupt-load warning")
	}
	if !strings.Contains(out, "is corrupt") || !strings.Contains(out, "fanout-presets.json") {
		t.Fatalf("corrupt warning not user-visible; frames:\n%s", out)
	}

	// First write: the gate opens, n refuses and leaves the file byte-intact.
	scr.gateCorruptSave(func() {
		if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
			t.Errorf("gated save: %v", err)
		}
	})
	out = app.CaptureRun(&ScriptedKeys{})
	if app.Modal() == nil {
		t.Fatalf("first write after corrupt load must open the gate; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if data, err := os.ReadFile(path); err != nil || string(data) != "{not json" {
		t.Fatalf("refused write mutated the corrupt file: %q, %v", data, err)
	}
	if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
		t.Fatalf("gate cancel message = %q", app.Message())
	}

	// Acknowledged write proceeds and replaces the corrupt file. (The
	// episode latch closes on the next healthy observation, so corruptAcked
	// resets with the frame that renders the saved file; the acknowledgment
	// itself is proven by the write landing at all.)
	scr.gateCorruptSave(func() {
		if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
			t.Errorf("gated save: %v", err)
		}
	})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	pf, err := st.LoadPresets()
	if err != nil || pf == nil || len(pf.Presets) != 0 {
		t.Fatalf("acknowledged write did not land: err=%v pf=%+v", err, pf)
	}
}

// TestPresetsActionWithoutSelection pins the honest empty-selection path: on
// an empty store the Edit action says so instead of silently no-oping, and
// nothing is written.
func TestPresetsActionWithoutSelection(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
		t.Fatalf("seed empty store: %v", err)
	}
	out, opened := presetsKeys(t, st, []Key{Digit(3)})
	if len(*opened) != 0 {
		t.Fatalf("edit with no selection opened %v; want none", *opened)
	}
	if !strings.Contains(out, "Select a preset row first.") {
		t.Fatalf("select-first message missing; frames:\n%s", out)
	}
	pf := reloadPresets(t, st)
	if len(pf.Presets) != 0 || pf.DefaultPreset != "" {
		t.Fatalf("empty-selection action wrote: %+v", pf)
	}
	// The empty-store hint and the built-in row still render.
	if !strings.Contains(out, "No presets yet.") || !strings.Contains(out, fanout.BuiltinPresetName) {
		t.Fatalf("empty-state lines missing; frames:\n%s", out)
	}
}

// TestPresetsListPicksUpEditorSaveBeforeDelete pins the stale-snapshot fix:
// a save from the preset editor must survive a subsequent delete from the
// list. The list re-reads the store behind the rendered-state signature on
// every frame, so the delete writes the current file state (alpha with the
// edited task, beta removed) instead of reverting the editor's change from
// the stale in-memory snapshot. The store sits at the default presets path
// under AGY_PRO_HOME so the real editor (which resolves that path) shares
// the file with the list, exactly like the app shell wires it.
func TestPresetsListPicksUpEditorSaveBeforeDelete(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	st := &fanout.PresetFileStore{Path: path}
	if err := st.SavePresets(&fanout.PresetsFile{
		Presets: []fanout.Preset{
			{Name: "alpha", Task: "review", N: 4, TimeoutSeconds: 120, Tools: "read,search"},
			{Name: "beta", N: 2},
		},
		DefaultPreset: "alpha",
	}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	// Production shape: the openEditor spy pushes the REAL editor.
	app, _ := newPresetsApp(st, func(name string, a *App) {
		a.Push(NewPresetEditorScreen(name))
	})
	// Open alpha (row 2), change task review -> security, save, close.
	keys := []Key{Digit(2), KeyEnter} // editor: name field -> task field
	for i := 0; i < len("review"); i++ {
		keys = append(keys, KeyBackspace)
	}
	keys = append(keys, typePresetRunes("security")...)
	keys = append(keys, KeyCtrlS, KeyRight)
	// List: arm beta (row 3), delete it, confirm.
	keys = append(keys, KeyDown, KeyDown, Digit(7), KeyYes)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})

	pf := reloadPresets(t, st)
	alpha, ok := pf.FindPreset("alpha")
	if !ok || alpha.Task != "security" {
		t.Fatalf("editor save did not survive the list delete: %+v", pf.Presets)
	}
	if _, ok := pf.FindPreset("beta"); ok {
		t.Fatalf("delete did not land: %+v", pf.Presets)
	}
	if !strings.Contains(out, "Preset alpha saved.") || !strings.Contains(out, "Preset beta deleted.") {
		t.Fatalf("save/delete messages missing; frames:\n%s", out)
	}
}

// TestPresetsCorruptGateArmsWithPreSetWarnOut pins the production-shaped
// corrupt gate: the app shell pre-sets the store's WarnOut and pre-loads
// before handing the store to the screen (presetStore in app.go). The screen
// must still arm its corrupt state on that path, and the first write must
// require the explicit acknowledgment naming the path.
func TestPresetsCorruptGateArmsWithPreSetWarnOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt preset file: %v", err)
	}
	// Shell shape: WarnOut pre-set and one pre-load already consumed.
	st := &fanout.PresetFileStore{Path: path, WarnOut: &bytes.Buffer{}}
	if _, err := st.LoadPresets(); err != nil {
		t.Fatalf("shell pre-load: %v", err)
	}
	app, _ := newPresetsApp(st, nil)
	out := app.CaptureRun(&ScriptedKeys{})
	scr := app.Top().(*presetsScreen)
	if !scr.corruptWarn {
		t.Fatal("corrupt gate must arm even when the shell pre-set WarnOut")
	}
	if !strings.Contains(out, "is corrupt") || !strings.Contains(out, "fanout-presets.json") {
		t.Fatalf("corrupt warning not user-visible; frames:\n%s", out)
	}

	// The first write after the corrupt load opens the gate on every write
	// path (delete/duplicate/set-default all funnel through gateCorruptSave,
	// as does the guarded store write itself). Decline: file byte-intact.
	scr.gateCorruptSave(func() {
		if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
			t.Errorf("gated save: %v", err)
		}
	})
	out = app.CaptureRun(&ScriptedKeys{})
	if app.Modal() == nil {
		t.Fatalf("first write after corrupt load must open the gate; frames:\n%s", out)
	}
	if !strings.Contains(out, presetsCorruptTitle) || !strings.Contains(out, "fanout-presets.json") {
		t.Fatalf("gate must name the on-disk path; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if data, err := os.ReadFile(path); err != nil || string(data) != "{not json" {
		t.Fatalf("refused write mutated the corrupt file: %q, %v", data, err)
	}
}

// TestPresetsDeclinedCorruptGateLeavesMemoryUntouched pins the askDelete
// ordering fix: the in-memory list is only mutated after the corrupt gate
// answers, so a delete approved by the y/n gate but declined by the corrupt
// gate leaves both memory and file untouched.
func TestPresetsDeclinedCorruptGateLeavesMemoryUntouched(t *testing.T) {
	st, _ := presetsFixture(t)
	app, _ := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)
	// Arm the corrupt gate the way a corrupt load would (the arming itself is
	// pinned by the corrupt tests above); the file on disk stays healthy.
	scr.corruptWarn = true

	// Arm alpha, activate Delete, approve the y/n gate: the corrupt gate must
	// open next (declining the delete gate is pinned elsewhere).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(7), KeyYes}})
	if app.Modal() == nil {
		t.Fatalf("corrupt gate must open after the delete gate approved; frames:\n%s", out)
	}
	if !strings.Contains(out, presetsCorruptTitle) {
		t.Fatalf("corrupt gate title missing; frames:\n%s", out)
	}
	if n := len(scr.pf.Presets); n != 2 {
		t.Fatalf("in-memory list mutated before the gate answered: %d presets", n)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if n := len(scr.pf.Presets); n != 2 {
		t.Fatalf("declined gate mutated memory: %d presets, want 2", n)
	}
	if _, ok := scr.pf.FindPreset("alpha"); !ok {
		t.Fatalf("declined gate dropped alpha from memory: %+v", scr.pf.Presets)
	}
	if pf := reloadPresets(t, st); len(pf.Presets) != 2 {
		t.Fatalf("declined gate wrote the file: %+v", pf.Presets)
	}
	if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
		t.Fatalf("gate cancel message = %q", app.Message())
	}
}

// TestPresetsNoteMasksCredentialShapedTask pins the round-6 note fix: the
// row-note operands (task, tools) render through MaskOperand, so a
// credential-shaped value planted in the preset file paints masked in the
// list and never raw, while benign values stay readable.
func TestPresetsNoteMasksCredentialShapedTask(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	const planted = testfixtures.AKIAExample
	if err := st.SavePresets(&fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "alpha", Task: planted, Tools: "read,search"},
		{Name: "beta", Task: "review"},
	}}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	out, _ := presetsKeys(t, st, nil)
	if strings.Contains(out, planted) {
		t.Fatalf("raw credential-shaped task rendered in the note; frames:\n%s", out)
	}
	if !strings.Contains(out, "task=AKIAI****") {
		t.Fatalf("masked task note missing; frames:\n%s", out)
	}
	if !strings.Contains(out, "task=review") || !strings.Contains(out, "tools=read,search") {
		t.Fatalf("benign note operands must stay readable; frames:\n%s", out)
	}
}

// TestPresetsCorruptLatchRearmsOnNewEpisode pins the round-6 latch fix on
// the list screen: corrupt -> ack -> healthy save closes the episode (the
// stale notice clears), and a SECOND external corrupt event re-arms the
// gate, so every distinct corrupt load re-gates the next write.
func TestPresetsCorruptLatchRearmsOnNewEpisode(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt preset file: %v", err)
	}
	st := &fanout.PresetFileStore{Path: path}
	app, _ := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)
	if !scr.corruptWarn {
		t.Fatal("first corrupt load must arm the latch")
	}

	// Acknowledge and write: the file turns healthy, the episode closes, and
	// the notice clears on the next frame.
	scr.gateCorruptSave(func() {
		if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
			t.Errorf("gated save: %v", err)
		}
	})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	out := app.CaptureRun(&ScriptedKeys{})
	if scr.corruptWarn {
		t.Fatalf("stale corrupt notice survived a healthy save; frames:\n%s", out)
	}
	if strings.Contains(out, "is corrupt") {
		t.Fatalf("stale corrupt warning line rendered after recovery; frames:\n%s", out)
	}

	// Second external corrupt event: a NEW episode opens, the acknowledgment
	// resets, and the next write re-gates.
	if err := os.WriteFile(path, []byte("{still not json"), 0o600); err != nil {
		t.Fatalf("re-corrupt preset file: %v", err)
	}
	out = app.CaptureRun(&ScriptedKeys{})
	if !scr.corruptWarn || scr.corruptAcked {
		t.Fatalf("new corrupt episode must re-arm the gate (warn=%v acked=%v); frames:\n%s",
			scr.corruptWarn, scr.corruptAcked, out)
	}
	if !strings.Contains(out, "is corrupt") {
		t.Fatalf("new-episode warning line missing; frames:\n%s", out)
	}
	wrote := false
	scr.gateCorruptSave(func() { wrote = true })
	if app.Modal() == nil {
		t.Fatal("second corrupt episode must re-gate the next write")
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if wrote {
		t.Fatal("declined re-gate must not run the write")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{still not json" {
		t.Fatalf("declined re-gate mutated the file: %q, %v", data, err)
	}
}

// presetsBulkStore seeds n presets behind the fixed rows (the read-only
// built-in row plus the five action rows), so the list overflows a standard
// 80x24 window instead of fitting it.
func presetsBulkStore(t *testing.T, n int) *fanout.PresetFileStore {
	t.Helper()
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	pf := &fanout.PresetsFile{DefaultPreset: "p1"}
	for i := 1; i <= n; i++ {
		pf.Presets = append(pf.Presets, fanout.Preset{Name: "p" + strconv.Itoa(i), Task: "review", N: i})
	}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("seed bulk presets: %v", err)
	}
	return st
}

// presetsAppAt pushes the preset list over a stub root at one console
// geometry and returns the screen handle alongside the app.
func presetsAppAt(st fanout.PresetStore, w, h int) (*App, *presetsScreen) {
	app := NewApp(w, h, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPresetsScreen(st, nil).(*presetsScreen)
	app.Push(scr)
	return app, scr
}

// TestPresetsBaseFooterSurvivesFullPage pins audit F7 on the preset list: at
// 80x24 with more presets than the window holds, the composed frame still
// ends with the shared base footer (the only line naming Esc, Ctrl+L and
// Ctrl+C) and still carries the list hint. The window reserves every non-list
// line of the body shape (title, separator, status line, the blank under it,
// the four explanation lines, the blank above the hint, the hint, the
// separator above the footer, the footer), so a full page can no longer push
// the footer off the frame.
func TestPresetsBaseFooterSurvivesFullPage(t *testing.T) {
	st := presetsBulkStore(t, 13)
	app, scr := presetsAppAt(st, 80, 24)
	app.CaptureRun(&ScriptedKeys{})

	frame := app.Frame()
	if got, want := scr.list.PageSize(), 24-presetsListChrome; got != want {
		t.Fatalf("page size = %d, want %d (80x24 minus %d chrome rows)", got, want, presetsListChrome)
	}
	if len(scr.rows) <= scr.list.PageSize() {
		t.Fatalf("fixture does not fill the window: %d rows, page %d", len(scr.rows), scr.list.PageSize())
	}
	if len(frame.Lines) != 24 {
		t.Fatalf("full-page frame = %d lines, want 24; frames:\n%s", len(frame.Lines), strings.Join(frame.Lines, "\n"))
	}
	if last := frame.Lines[len(frame.Lines)-1]; last != baseFooter {
		t.Fatalf("base footer cut on a full page; last line = %q; frames:\n%s", last, strings.Join(frame.Lines, "\n"))
	}
	if body := strings.Join(frame.Lines, "\n"); !strings.Contains(body, scr.list.FooterHint()) {
		t.Fatalf("list hint missing from the full-page frame; frames:\n%s", body)
	}
}

// TestPresetsChromeSurvivesNoticeBlocks pins the adjacent class on the one-off
// lines Draw renders above the list: the corrupt-file warning and the empty
// state are counted against the window, so on a short console they cannot
// push the list hint or the base footer off the frame. Both cases sit at
// heights where the pre-fix window (height - 5) overran the frame.
func TestPresetsChromeSurvivesNoticeBlocks(t *testing.T) {
	t.Run("corrupt warning", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(providers.EnvHome, home)
		path := filepath.Join(home, "fanout-presets.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("seed corrupt preset file: %v", err)
		}
		app, scr := presetsAppAt(&fanout.PresetFileStore{Path: path}, 80, 16)
		app.CaptureRun(&ScriptedKeys{})

		frame := app.Frame()
		body := strings.Join(frame.Lines, "\n")
		if !scr.corruptWarn || !strings.Contains(body, "is corrupt") {
			t.Fatalf("corrupt warning missing from the frame; frames:\n%s", body)
		}
		if !strings.Contains(body, scr.list.FooterHint()) {
			t.Fatalf("list hint cut by the corrupt warning block; frames:\n%s", body)
		}
		if last := frame.Lines[len(frame.Lines)-1]; last != baseFooter {
			t.Fatalf("base footer cut by the corrupt warning block; last line = %q; frames:\n%s", last, body)
		}
	})

	t.Run("empty state", func(t *testing.T) {
		t.Setenv(providers.EnvHome, t.TempDir())
		st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
		if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
			t.Fatalf("seed empty store: %v", err)
		}
		app, scr := presetsAppAt(st, 80, 12)
		app.CaptureRun(&ScriptedKeys{})

		frame := app.Frame()
		body := strings.Join(frame.Lines, "\n")
		if !strings.Contains(body, "No presets yet.") {
			t.Fatalf("empty-state line missing from the frame; frames:\n%s", body)
		}
		if !strings.Contains(body, scr.list.FooterHint()) {
			t.Fatalf("list hint cut by the empty-state block; frames:\n%s", body)
		}
		if last := frame.Lines[len(frame.Lines)-1]; last != baseFooter {
			t.Fatalf("base footer cut by the empty-state block; last line = %q; frames:\n%s", last, body)
		}
	})
}

// TestPresetsLastRowReachable pins the paging check: with more rows than the
// window holds, End and PgDn both reach the last row, the window scrolls to
// keep it visible, and the chrome survives the scroll.
func TestPresetsLastRowReachable(t *testing.T) {
	st := presetsBulkStore(t, 13)
	app, scr := presetsAppAt(st, 80, 24)

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnd}})
	if got, want := scr.list.Cursor(), len(scr.rows)-1; got != want {
		t.Fatalf("End cursor = %d, want %d (last row)", got, want)
	}
	if c, o, p := scr.list.Cursor(), scr.list.Offset(), scr.list.PageSize(); c < o || c >= o+p {
		t.Fatalf("last row %d outside the window [%d,%d) after End", c, o, o+p)
	}
	if !strings.Contains(strings.Join(app.Frame().Lines, "\n"), scr.list.FooterHint()) {
		t.Fatalf("list hint cut after End; frames:\n%s", strings.Join(app.Frame().Lines, "\n"))
	}

	// PgDn pages down to the same last row: 19 rows with a 16-row window is
	// two pages, so the cursor must clamp onto the last row, not past it.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyHome}})
	if c := scr.list.Cursor(); c != 0 {
		t.Fatalf("Home cursor = %d, want 0", c)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyPgDn, KeyPgDn}})
	if got, want := scr.list.Cursor(), len(scr.rows)-1; got != want {
		t.Fatalf("PgDn cursor = %d, want %d (last row)", got, want)
	}
	if c, o, p := scr.list.Cursor(), scr.list.Offset(), scr.list.PageSize(); c < o || c >= o+p {
		t.Fatalf("last row %d outside the window [%d,%d) after PgDn", c, o, o+p)
	}
}

// TestPresetsStickySelectionAfterMutation pins the selection check: after
// Duplicate, the sticky selection names the copy and the cursor stays on the
// Duplicate row, so the next Enter repeats the gesture the user saw instead of
// landing on a different verb. After Delete, the sticky index is clamped into
// the shrunken list and names a preset that still exists.
func TestPresetsStickySelectionAfterMutation(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	// KeyDown arms alpha (row 2); digit 6 activates Duplicate on it.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(6)}})
	if len(*opened) != 0 {
		t.Fatalf("duplicate opened the editor: %v", *opened)
	}
	if got, want := scr.selectedName(), "alpha copy"; got != want {
		t.Fatalf("sticky selection after Duplicate = %q, want %q", got, want)
	}
	if c := scr.list.Cursor(); c < 0 || c >= len(scr.rows) {
		t.Fatalf("cursor %d outside the row range %d", c, len(scr.rows))
	}
	if got, want := scr.rows[scr.list.Cursor()].ID, presetsRowIDDuplicate; got != want {
		t.Fatalf("cursor drifted to row %q after Duplicate; want %q", got, want)
	}

	// The next Edit (digit 6 on the enlarged list) must target the copy.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(6)}})
	if len(*opened) != 1 || (*opened)[0] != "alpha copy" {
		t.Fatalf("Edit after Duplicate opened %v, want [alpha copy]", *opened)
	}

	// Delete the copy (digit 8 on the enlarged list) and confirm.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(8), KeyYes}})
	pf := reloadPresets(t, st)
	if _, ok := pf.FindPreset("alpha copy"); ok {
		t.Fatalf("delete did not land: %+v", pf.Presets)
	}
	if got, want := scr.selectedName(), "beta"; got != want {
		t.Fatalf("sticky selection after Delete = %q, want %q (clamped into the list)", got, want)
	}
	if _, ok := scr.pf.FindPreset(scr.selectedName()); !ok {
		t.Fatalf("sticky selection %q names a preset that does not exist", scr.selectedName())
	}
	if got, want := scr.rows[scr.list.Cursor()].ID, presetsRowIDDelete; got != want {
		t.Fatalf("cursor drifted to row %q after Delete; want %q", got, want)
	}
}

// failingPresetStore serves the in-memory fixture but refuses every save, for
// the write-failure message-role pin.
type failingPresetStore struct{ pf *fanout.PresetsFile }

func (s failingPresetStore) LoadPresets() (*fanout.PresetsFile, error) { return s.pf, nil }

func (s failingPresetStore) SavePresets(*fanout.PresetsFile) error {
	return errors.New("preset store is read-only")
}

// TestPresetsMessageRoles pins user decision D7-7B at the preset list: a
// completed duplicate opens with the localized common.success label and a
// refused store write with common.error, so the two compose DIFFERENT palette
// roles (RoleSuccess vs RoleDanger) instead of the neutral amber every message
// used to get. Both frames go through the same composition path the app-level
// pin uses, and the plain tier still emits zero escape bytes for them.
func TestPresetsMessageRoles(t *testing.T) {
	success, _ := presetsFixture(t)
	successApp, _ := newPresetsApp(success, nil)
	// KeyDown arms alpha (row 2); digit 6 activates Duplicate.
	successApp.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(6)}})
	successMsg := successApp.Message()
	successLabel := Bundle().T("common.success")
	if !strings.HasPrefix(successMsg, successLabel+": ") {
		t.Fatalf("duplicate success message = %q, want the %q label prefix", successMsg, successLabel)
	}
	successRole := roleOfLine(t, composeFrame(successApp, nil), successMsg)
	if successRole != RoleSuccess {
		t.Fatalf("success message role = %v, want RoleSuccess", successRole)
	}

	// The same gesture over a store whose save always fails: the write-failure
	// path must paint RoleDanger, not the neutral note role.
	_, pf := presetsFixture(t)
	failureApp, _ := newPresetsApp(failingPresetStore{pf: pf}, nil)
	failureApp.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(6)}})
	failureMsg := failureApp.Message()
	errorLabel := Bundle().T("common.error")
	if !strings.HasPrefix(failureMsg, errorLabel+": ") {
		t.Fatalf("write failure message = %q, want the %q label prefix", failureMsg, errorLabel)
	}
	failureRole := roleOfLine(t, composeFrame(failureApp, nil), failureMsg)
	if failureRole != RoleDanger {
		t.Fatalf("write failure role = %v, want RoleDanger", failureRole)
	}
	if successRole == failureRole {
		t.Fatalf("success and write failure both paint %v; want different roles", successRole)
	}

	// Plain tier: the same frames resolve to no bytes at all.
	frames := []struct {
		name string
		app  *App
	}{{"success", successApp}, {"failure", failureApp}}
	for _, f := range frames {
		var out bytes.Buffer
		p := NewPainter(&out, plainTestCap())
		if code := paintLoop(f.app, &ScriptedKeys{}, p, nil); code != 0 {
			t.Fatalf("%s frame paintLoop exit = %d, want 0", f.name, code)
		}
		if strings.Contains(out.String(), "\x1b") {
			t.Fatalf("%s plain-tier paint emitted escape bytes:\n%q", f.name, out.String())
		}
	}
}

// ---------------------------------------------------------------------------
// P5-1 U6: the default_preset write gate (W28).
//
// The healthy path used to rewrite fanout-presets.json with no consent; it now
// opens the M4 approval modal over the exact key change, re-reads the store
// before the write, and refuses a stale presentation by name.
// ---------------------------------------------------------------------------

// openSetDefaultGate arms one preset row on the fixture (two presets) and
// activates Set default, leaving the M4 approval gate live.
func openSetDefaultGate(t *testing.T, app *App, downs int) Modal {
	t.Helper()
	for i := 0; i < downs; i++ {
		app.Step(KeyDown)
	}
	app.Step(Digit(8)) // builtin, 2 presets, new, edit, duplicate, delete, set default
	m := app.Modal()
	if m == nil {
		t.Fatalf("set-default approval gate did not open")
	}
	return m
}

// TestPresetsSetDefaultGateShowsExactDiff pins the W28 gate body: the key
// sentence (store path, old value, new value), the atomic sentence for the same
// file, and the removed/added diff lines. Nothing is written before the answer.
func TestPresetsSetDefaultGateShowsExactDiff(t *testing.T) {
	st, before := presetsFixture(t)
	app, _ := newPresetsApp(st, nil)
	m := openSetDefaultGate(t, app, 2) // arm beta

	if got, want := m.Title(), "Set default"; got != want {
		t.Errorf("gate title = %q; want %q", got, want)
	}
	body := strings.Join(m.Body(app.Width()), "\n")
	for _, want := range []string{
		"Sets default_preset: alpha -> beta.",
		"No preset record changes",
		"File: " + st.Path,
		"fanout-presets.json will be written (0600, atomic) with this exact diff:",
		"- default_preset = alpha",
		"+ default_preset = beta",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("set-default gate body missing %q\n%s", want, body)
		}
	}
	if got := reloadPresets(t, st); got.DefaultPreset != before.DefaultPreset {
		t.Fatalf("the gate wrote default_preset = %q before the answer", got.DefaultPreset)
	}
}

// TestPresetsSetDefaultGateConsentMatrix pins y/n/Esc on the W28 gate.
func TestPresetsSetDefaultGateConsentMatrix(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  Key
		want string
	}{
		{"y", KeyYes, "beta"},
		{"n", KeyNo, "alpha"},
		{"esc", KeyEsc, "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := presetsFixture(t)
			app, _ := newPresetsApp(st, nil)
			openSetDefaultGate(t, app, 2)
			app.Step(tc.key)
			if got := reloadPresets(t, st).DefaultPreset; got != tc.want {
				t.Fatalf("after %s: default_preset = %q, want %q", tc.name, got, tc.want)
			}
			if tc.want == "alpha" {
				if msg := app.Message(); !strings.Contains(msg, "Cancelled. Nothing was written.") {
					t.Fatalf("%s cancel message = %q", tc.name, msg)
				}
			} else if msg := app.Message(); !strings.Contains(msg, "Default preset set to beta.") {
				t.Fatalf("y message = %q; want the default-set notice", msg)
			}
		})
	}
}

// TestPresetsSetDefaultGateStaleBaseRefused pins the write-time re-read: another
// writer moves the same key while the diff is on screen, so y refuses by name
// and leaves the external value in place.
func TestPresetsSetDefaultGateStaleBaseRefused(t *testing.T) {
	st, before := presetsFixture(t)
	app, _ := newPresetsApp(st, nil)
	openSetDefaultGate(t, app, 2) // arm beta, diff says alpha -> beta

	external := *before
	external.DefaultPreset = "beta" // another session already made beta the default
	external.Presets = append([]fanout.Preset(nil), before.Presets...)
	if err := st.SavePresets(&external); err != nil {
		t.Fatalf("external write: %v", err)
	}

	app.Step(KeyYes)
	msg := app.Message()
	if !strings.Contains(msg, "changed on disk between the diff and the write") {
		t.Fatalf("message = %q; want the stale-base refusal", msg)
	}
	if !strings.Contains(msg, "default_preset moved from alpha to beta") {
		t.Fatalf("refusal does not name the moved key: %q", msg)
	}
	if got := reloadPresets(t, st); got.DefaultPreset != "beta" {
		t.Fatalf("the refused write changed the store: %+v", got)
	}
	if frame := app.Frame().String(); !strings.Contains(frame, "Default preset: beta") {
		t.Errorf("the list did not reload the on-disk default\n%s", frame)
	}
}

// TestPresetsSetDefaultGateMasksName pins the display discipline on the identity
// slot: a credential-shaped preset name renders masked in the gate body, and
// neither the raw plant nor its tail survives.
func TestPresetsSetDefaultGateMasksName(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	const planted = testfixtures.AKIAExample
	if err := st.SavePresets(&fanout.PresetsFile{
		Presets:       []fanout.Preset{{Name: "alpha", N: 2}, {Name: planted, N: 3}},
		DefaultPreset: "alpha",
	}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	app, _ := newPresetsApp(st, nil)
	m := openSetDefaultGate(t, app, 2) // arm the planted row

	body := strings.Join(m.Body(app.Width()), "\n")
	for _, want := range []string{
		"alpha -> AKIAI****",
		"+ default_preset = AKIAI****",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("set-default gate body missing the masked form %q\n%s", want, body)
		}
	}
	for _, raw := range []string{planted, "IOSFODNN7EXAMPLE"} {
		if strings.Contains(body, raw) {
			t.Errorf("set-default gate body leaks %q\n%s", raw, body)
		}
	}
}

// TestPresetsSetDefaultGateAnswersLineModeWords pins the off-console answer path:
// the real line-mode reader maps the mandated words to the keys the console
// produces, and those keys answer the real gate with no arm-window delay.
func TestPresetsSetDefaultGateAnswersLineModeWords(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Key
		def  string
	}{
		{"y\n", KeyYes, "beta"},
		{"yes\n", KeyYes, "beta"},
		{"n\n", KeyNo, "alpha"},
		{"no\n", KeyNo, "alpha"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			st, _ := presetsFixture(t)
			app, _ := newPresetsApp(st, nil)
			openSetDefaultGate(t, app, 2)
			k, ok := NewLineKeys(strings.NewReader(tc.line)).ReadKey()
			if !ok || k != tc.want {
				t.Fatalf("%q mapped to %q,%v; want %q", tc.line, k, ok, tc.want)
			}
			app.Step(k)
			if got := reloadPresets(t, st).DefaultPreset; got != tc.def {
				t.Fatalf("%q: default_preset = %q, want %q", tc.line, got, tc.def)
			}
		})
	}
}

// presetsPinMessage is the confirmation line Space writes on this screen,
// resolved the way the screen resolves it (catalog entry, then the shared
// const) with the preset name through the row-label masker.
func presetsPinMessage(name string) string {
	return fillHubSlots(missingOr(i18n.Load("en"), "list.msg.pinned", listMsgPinned),
		map[string]string{"name": providers.MaskOperand(name)})
}

// TestPresetsSpaceShowsTheMarkerAndPinsThroughTransit covers the two halves of
// the preset-list fix: the sticky selection used to be INVISIBLE here (audit F9
// asked for the marker the models screen already drew), and the action rows sit
// below the preset block, so arrow travel to Edit passed the remaining preset
// rows and each rest re-stole the target. The marker now follows the cursor on a
// rest, Space pins a row explicitly, and the pin survives the transit.
func TestPresetsSpaceShowsTheMarkerAndPinsThroughTransit(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	// Rows: 1 built-in, 2 alpha, 3 beta, 4 New, 5 Edit, 6 Duplicate, 7 Delete,
	// 8 Set default. The opening frame marks nothing (sel starts at -1).
	if body := strings.Join(app.Frame().Lines, "\n"); strings.Contains(body, "●") {
		t.Fatalf("the opening frame marks a preset before any selection:\n%s", body)
	}

	app.Step(KeyDown) // rest on alpha: the marker follow makes the target visible
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● alpha") {
		t.Fatalf("resting on a preset row does not mark it:\n%s", body)
	}

	app.Step(KeySpace) // pin alpha explicitly
	if got, want := app.Message(), presetsPinMessage("alpha"); got != want {
		t.Fatalf("pin confirmation = %q, want %q", got, want)
	}
	if len(*opened) != 0 {
		t.Fatalf("Space activated the editor: %v", *opened)
	}

	// Arrow-only transit to the Edit row (alpha -> beta -> New -> Edit): without
	// the pin each rest would leave beta selected and Edit would open beta.
	for i := 0; i < 3; i++ {
		app.Step(KeyDown)
		if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● alpha") {
			t.Fatalf("the pin was stolen after %d Down(s):\n%s", i+1, body)
		}
	}
	if body := strings.Join(app.Frame().Lines, "\n"); strings.Contains(body, "● beta") {
		t.Fatalf("the transit row stole the mark:\n%s", body)
	}
	app.Step(KeyEnter)
	if len(*opened) != 1 || (*opened)[0] != "alpha" {
		t.Fatalf("Edit after the transit opened %v, want [alpha] (the pinned preset)", *opened)
	}
	if scr.locked {
		t.Fatal("the pin survived the verb that consumed it")
	}
}

// TestPresetsSpaceOnNonPresetRowsIsANoOp pins the boundary. Without the
// seed record, row [1] is only the engine-defaults preview and Space there
// changes nothing. The five action rows are never selectable, so Space on
// an action row is a consumed no-op, and a Space on an action row AFTER a
// pin leaves the pin standing.
func TestPresetsSpaceOnNonPresetRowsIsANoOp(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	app.Step(KeySpace) // row 1 is the built-in row, no seed on disk: no pin, no message
	if got := app.Message(); got != "" {
		t.Fatalf("Space on the built-in row wrote a message: %q", got)
	}
	if scr.sel != -1 || scr.locked {
		t.Fatalf("Space on the built-in row selected something: sel=%d locked=%v", scr.sel, scr.locked)
	}
	if body := strings.Join(app.Frame().Lines, "\n"); strings.Contains(body, "●") {
		t.Fatalf("Space on the built-in row marked a row:\n%s", body)
	}

	app.Step(KeyDown) // alpha
	app.Step(KeySpace)
	pinned := app.Message()
	if pinned != presetsPinMessage("alpha") {
		t.Fatalf("pin confirmation = %q, want %q", pinned, presetsPinMessage("alpha"))
	}
	app.Step(KeyDown) // beta (pin holds)
	app.Step(KeyDown) // New
	app.Step(KeySpace)
	if got := app.Message(); got != pinned {
		t.Fatalf("Space on the New row rewrote the message to %q, want the pin line %q", got, pinned)
	}
	if !scr.locked || scr.sel != 0 {
		t.Fatalf("Space on the New row dropped the pin: sel=%d locked=%v", scr.sel, scr.locked)
	}
	if len(*opened) != 0 {
		t.Fatalf("Space opened the editor: %v (Space must only pin)", *opened)
	}
}

// TestPresetsSpacePinsTheSeedRow pins the row-[1] half of the editable
// built-in defaults: with the seed record on disk, Space on row [1] pins
// THE SEED (selName is the reserved name, sel stays -1 because the seed is
// not a visible preset), the marker renders on row [1], and the verbs below
// act on the seed: Edit opens its editor by the real name.
func TestPresetsSpacePinsTheSeedRow(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: fanout.BuiltinPresetName, Seats: "native:a,nw/m1"},
		{Name: "alpha", N: 2},
	}}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	app.Step(KeySpace) // row 1 = the seed row
	if !scr.locked || !scr.seedSelected() || scr.sel != -1 {
		t.Fatalf("the seed pin did not take: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	if got, want := app.Message(), presetsPinMessage(fanout.BuiltinPresetName); got != want {
		t.Fatalf("seed pin confirmation = %q, want %q", got, want)
	}
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● "+fanout.BuiltinPresetName) {
		t.Fatalf("the marker did not render on row [1]:\n%s", body)
	}

	// Edit (row 4 on this list: seed, alpha, new, edit) opens the seed editor.
	app.Step(Digit(4))
	if len(*opened) != 1 || (*opened)[0] != fanout.BuiltinPresetName {
		t.Fatalf("Edit on the seed pin opened %v, want [%q]", *opened, fanout.BuiltinPresetName)
	}
	if scr.locked {
		t.Fatal("the pin survived the verb that consumed it")
	}
}

// TestPresetsSpacePinUnlocksAfterTheVerb pins the unlock transition: a sticky
// verb spends the pin, so the follow-on-rest rule resumes and the marker follows
// the cursor again.
func TestPresetsSpacePinUnlocksAfterTheVerb(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	app.Step(KeyDown) // alpha
	app.Step(KeySpace)
	for i := 0; i < 3; i++ {
		app.Step(KeyDown) // row 5 = Edit
	}
	app.Step(KeyEnter) // Edit consumes the pin
	if len(*opened) != 1 || (*opened)[0] != "alpha" {
		t.Fatalf("Edit opened %v, want [alpha]", *opened)
	}
	if scr.locked {
		t.Fatal("the pin survived the verb that consumed it")
	}

	app.Step(KeyHome) // row 1 = built-in
	app.Step(KeyDown) // row 2 = alpha
	app.Step(KeyDown) // row 3 = beta: the rest re-asserts the selection again
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● beta") {
		t.Fatalf("the marker did not follow the cursor after the verb:\n%s", body)
	}
}

// TestPresetsSpacePinDropsWhenItsTargetVanishes is the F-01 regression pin for
// the preset list. The selection used to be index-anchored, so an EXTERNAL
// mid-list removal of the pinned preset (picked up by reloadIfChanged) clamped the
// index onto the following preset while the lock stayed on: the pin silently
// re-targeted a preset the user never chose, and Delete would have opened its
// gate. The pin is now re-anchored by preset name, so a vanished target drops the
// selection AND the lock, which leaves the verbs refusing instead of guessing.
func TestPresetsSpacePinDropsWhenItsTargetVanishes(t *testing.T) {
	st, _ := presetsFixture(t) // alpha (index 0, MID-LIST), beta
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	// Rows: 1 built-in, 2 alpha, 3 beta, 4 New, 5 Edit, 6 Duplicate, 7 Delete,
	// 8 Set default.
	app.Step(KeyDown)  // rest on alpha
	app.Step(KeySpace) // pin alpha
	if !scr.locked || scr.selName != "alpha" || scr.sel != 0 {
		t.Fatalf("the pin did not take: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}

	// External mid-list removal: alpha is gone, so beta slides into index 0 and an
	// index clamp would point Edit/Duplicate/Delete/Set default at it.
	err := st.SavePresets(&fanout.PresetsFile{
		Presets:       []fanout.Preset{{Name: "beta", N: 2}},
		DefaultPreset: "beta",
	})
	if err != nil {
		t.Fatalf("external store rewrite: %v", err)
	}
	app.Frame() // reloadIfChanged adopts the new store state, as a live screen does

	if scr.locked || scr.sel != -1 || scr.selName != "" {
		t.Fatalf("the pin outlived its target: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	if body := strings.Join(app.Frame().Lines, "\n"); strings.Contains(body, "●") {
		t.Fatalf("a vanished pin still marks a row:\n%s", body)
	}

	// Delete (row 6 on the shrunk list: 1 built-in, 2 beta, 3 New, 4 Edit,
	// 5 Duplicate, 6 Delete) must refuse instead of opening a gate for beta.
	app.Step(Digit(6))
	if got, want := app.Message(), Bundle().T("presets.msg.select_first"); got != want {
		t.Fatalf("Delete after the pinned preset vanished = %q, want the refusal %q", got, want)
	}
	if app.Modal() != nil {
		t.Fatal("Delete opened a gate for a preset nobody selected")
	}
	if len(*opened) != 0 {
		t.Fatalf("the vanished pin opened the editor: %v", *opened)
	}
}

// TestPresetsSpaceRePinMovesThePin pins the re-pin transition on this screen:
// another Space on another preset row moves the pin, the message names the new
// target and the verbs below act on it.
func TestPresetsSpaceRePinMovesThePin(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	app.Step(KeyDown)  // alpha
	app.Step(KeySpace) // pin alpha
	app.Step(KeyDown)  // beta: the pin holds
	if scr.selectedName() != "alpha" {
		t.Fatalf("the rest stole the pin: selection = %q, want alpha", scr.selectedName())
	}
	app.Step(KeySpace) // re-pin onto beta
	if got, want := app.Message(), presetsPinMessage("beta"); got != want {
		t.Fatalf("re-pin confirmation = %q, want %q", got, want)
	}
	if !scr.locked || scr.selName != "beta" || scr.sel != 1 {
		t.Fatalf("the re-pin did not move: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● beta") {
		t.Fatalf("the marker did not follow the re-pin:\n%s", body)
	}

	app.Step(KeyDown) // row 4 New
	app.Step(KeyDown) // row 5 Edit
	app.Step(KeyEnter)
	if len(*opened) != 1 || (*opened)[0] != "beta" {
		t.Fatalf("Edit after the re-pin opened %v, want [beta]", *opened)
	}
}

// TestPresetsSpacePinUnlocksWhenAPresetRowIsActivated pins the navigation
// transition on this screen: Enter on a preset row opens its editor and consumes
// the pin, because a preset row activation addresses the CURSOR row, never the
// pin.
func TestPresetsSpacePinUnlocksWhenAPresetRowIsActivated(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	app.Step(KeyDown)  // alpha
	app.Step(KeySpace) // pin alpha
	app.Step(KeyDown)  // beta: the pin holds
	if !scr.locked || scr.selName != "alpha" {
		t.Fatalf("the pin did not survive the rest: selName=%q locked=%v", scr.selName, scr.locked)
	}
	app.Step(KeyEnter) // activates the CURSOR row (beta) and clears the pin
	if len(*opened) != 1 || (*opened)[0] != "beta" {
		t.Fatalf("preset row activation opened %v, want [beta]", *opened)
	}
	if scr.locked {
		t.Fatal("the pin survived the navigation into the editor")
	}
	if body := strings.Join(app.Frame().Lines, "\n"); !strings.Contains(body, "● beta") {
		t.Fatalf("the marker did not adopt the activated row:\n%s", body)
	}
}

// TestPresetsNoSpaceTransitTargetsTheLastRow is the no-Space negative control
// (fix round 1, F-02): the exact key sequence the pin tests use, minus Space,
// still lands Edit on the LAST preset. It keeps the pin tests honest and
// documents the defect the gesture fixes.
func TestPresetsNoSpaceTransitTargetsTheLastRow(t *testing.T) {
	st, _ := presetsFixture(t)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)

	for i := 0; i < 4; i++ {
		app.Step(KeyDown) // alpha, beta, New, Edit: every rest re-steals the selection
	}
	app.Step(KeyEnter)
	if len(*opened) != 1 || (*opened)[0] != "beta" {
		t.Fatalf("the no-Space transit opened %v, want the LAST preset (beta)", *opened)
	}
	if scr.locked {
		t.Fatal("the no-Space transit locked something; Space is the only pin")
	}
}

// threePresetStore is the fixture the T-01 move test reads: alpha, beta, gamma so
// the pinned row is genuinely MID-LIST (a neighbour on each side), the shape an
// index anchor breaks on. The three names are mutually distinct (alpha/beta are
// shared with the two-preset fixture, but each test runs on its own temp store),
// so a stale reference cannot pass by matching the wrong record within a run.
func threePresetStore(t *testing.T) *fanout.PresetFileStore {
	t.Helper()
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	pf := &fanout.PresetsFile{
		Presets: []fanout.Preset{
			{Name: "alpha", Task: "review", N: 1},
			{Name: "beta", Task: "review", N: 2},
			{Name: "gamma", Task: "review", N: 3},
		},
		DefaultPreset: "alpha",
	}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("seed three-preset store: %v", err)
	}
	return st
}

// TestPresetsSpacePinnedTargetSurvivesAMovedRow is the T-01 regression pin for the
// preset list: the other half of the F-01 identity re-anchor. A pinned preset that
// MOVES through an external store rewrite (a reorder, or a removal of an entry
// BEFORE it) must keep its target: the selection re-anchors onto the same preset
// NAME at its NEW index, the lock stays on, the marker follows the row, and the
// verb below fires on it. The reference structure is the providers screen's
// TestProvidersStickyTargetHeldByProviderName ("reordered registry keeps the
// marked provider"); before the re-anchor, the clamp left the pin on the index,
// which after the move names a different preset than the user pinned.
func TestPresetsSpacePinnedTargetSurvivesAMovedRow(t *testing.T) {
	cases := []struct {
		name string
		// store is the externally rewritten store state the screen reloads from.
		store func(t *testing.T) *fanout.PresetsFile
		// wantIdx is where the pinned preset (beta) sits after the reload.
		wantIdx int
		// editRow is the digit that reaches the Edit row on the mutated list.
		editRow Key
	}{
		{
			// [alpha, beta, gamma] -> [gamma, alpha, beta]: beta moves 1 -> 2.
			name: "reordered store keeps the pinned preset",
			store: func(t *testing.T) *fanout.PresetsFile {
				return &fanout.PresetsFile{
					Presets: []fanout.Preset{
						{Name: "gamma", Task: "review", N: 3},
						{Name: "alpha", Task: "review", N: 1},
						{Name: "beta", Task: "review", N: 2},
					},
					DefaultPreset: "alpha",
				}
			},
			wantIdx: 2,
			// rows: 1 built-in, 2 gamma, 3 alpha, 4 beta, 5 New, 6 Edit
			editRow: Digit(6),
		},
		{
			// [alpha, beta, gamma] -> [beta, gamma]: the row BEFORE the pin is gone,
			// so beta moves 1 -> 0.
			name: "removal before the pinned preset shifts its index",
			store: func(t *testing.T) *fanout.PresetsFile {
				return &fanout.PresetsFile{
					Presets: []fanout.Preset{
						{Name: "beta", Task: "review", N: 2},
						{Name: "gamma", Task: "review", N: 3},
					},
					DefaultPreset: "beta",
				}
			},
			wantIdx: 0,
			// rows: 1 built-in, 2 beta, 3 gamma, 4 New, 5 Edit
			editRow: Digit(5),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := threePresetStore(t)
			app, opened := newPresetsApp(st, nil)
			scr := app.Top().(*presetsScreen)

			// Rows: 1 built-in, 2 alpha, 3 beta, 4 gamma, 5 New, 6 Edit, ...
			app.Step(KeyDown)  // rest on alpha
			app.Step(KeyDown)  // rest on beta: the MID preset row
			app.Step(KeySpace) // pin it
			if !scr.locked || scr.selName != "beta" || scr.sel != 1 {
				t.Fatalf("the pin did not take: sel=%d selName=%q locked=%v", scr.sel, scr.selName, scr.locked)
			}

			if err := st.SavePresets(tc.store(t)); err != nil {
				t.Fatalf("external store rewrite: %v", err)
			}
			app.Frame() // reloadIfChanged adopts the new store state, as a live screen does

			if !scr.locked {
				t.Fatal("the pin dropped although the pinned preset still exists; a move must keep it")
			}
			if scr.selName != "beta" || scr.sel != tc.wantIdx {
				t.Fatalf("the pin re-anchored to sel=%d selName=%q, want index %d of beta", scr.sel, scr.selName, tc.wantIdx)
			}
			body := strings.Join(app.Frame().Lines, "\n")
			if !strings.Contains(body, "● beta") {
				t.Fatalf("the marker did not follow the moved pin:\n%s", body)
			}
			if strings.Contains(body, "● alpha") || strings.Contains(body, "● gamma") {
				t.Fatalf("the marker also sits on a neighbour:\n%s", body)
			}

			// The verb below the preset block must act on the moved pin, not on
			// whatever now occupies the old index.
			app.Step(tc.editRow)
			if len(*opened) != 1 || (*opened)[0] != "beta" {
				t.Fatalf("Edit after the move opened %v, want [beta]", *opened)
			}
			if scr.locked {
				t.Fatal("the pin survived the verb that consumed it")
			}
		})
	}
}

// TestPresetsRowShowsSeatsPinnedNote pins spec item 3 of the seat-lineup unit:
// a preset that fixes the lineup says so on its list row, so the user can see
// WHICH presets pin seats without opening each editor. The count comes from the
// stored comma string, the note keeps the visible-knob summary and the pinned
// digest in front of it, and the default badge still closes the line.
func TestPresetsRowShowsSeatsPinnedNote(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	pf := &fanout.PresetsFile{
		DefaultPreset: "lineup",
		Presets: []fanout.Preset{
			{Name: "lineup", Seats: "native:a,nw/m1,nw/m2"},
			{Name: "visible", Task: "review", Seats: "native:a"},
			{Name: "auto"},
		},
	}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	scr := NewPresetsScreen(st, nil).(*presetsScreen)
	notes := map[string]string{}
	for _, r := range scr.rows {
		if strings.HasPrefix(r.ID, presetRowPrefix) {
			notes[strings.TrimPrefix(r.ID, presetRowPrefix)] = r.Note
		}
	}
	// The lineup-only row would otherwise render the pinned digest, and the
	// lineup segment follows it.
	if got := notes["lineup"]; !strings.Contains(got, "seats pinned: 3") {
		t.Fatalf("lineup note = %q; want the pinned seat count", got)
	}
	if got := notes["lineup"]; !strings.Contains(got, "default") {
		t.Fatalf("lineup note = %q; want the default badge after the seat segment", got)
	}
	// A visible-knob summary keeps its own text and gains the seat segment.
	if got := notes["visible"]; !strings.Contains(got, "task=review") || !strings.Contains(got, "seats pinned: 1") {
		t.Fatalf("visible note = %q; want the flag summary plus the seat segment", got)
	}
	// A preset that pins no seats says nothing about them (the engine picks).
	if got := notes["auto"]; strings.Contains(got, "seats pinned") {
		t.Fatalf("auto note = %q; must not claim a pinned lineup", got)
	}

	// The rendered-state digest must move when only the lineup changes: the row
	// note is derived from it, so a stale signature would hide an external edit.
	base := &fanout.PresetsFile{Presets: []fanout.Preset{{Name: "a", Seats: "native:a"}}}
	moved := &fanout.PresetsFile{Presets: []fanout.Preset{{Name: "a", Seats: "native:a,nw/m1"}}}
	if presetsSig(base, "") == presetsSig(moved, "") {
		t.Fatal("presetsSig ignores the seat lineup, so a roster change would not re-render")
	}

	// countSeatSpecs is the one counter behind the note: blanks are not seats.
	for _, tc := range []struct {
		seats string
		want  int
	}{
		{"", 0},
		{"   ", 0},
		{"native:a", 1},
		{"native:a,nw/m1", 2},
		{"native:a, ,nw/m1", 2},
	} {
		if got := countSeatSpecs(tc.seats); got != tc.want {
			t.Errorf("countSeatSpecs(%q) = %d, want %d", tc.seats, got, tc.want)
		}
	}
}

// --- r4 name-slot close-out (M-1) ----------------------------------------
//
// The preset NAME is a file-sourced registry slot: fanout-presets.json accepts
// any non-empty name and the load gate checks only control runes, so a
// credential-shaped name reaches this screen verbatim. Every name-bearing slot
// here now renders through presetNameOperand (providers.MaskSlot), the authority
// the rest of the tree already used - MaskOperand is the IDENTITY on the
// underscore-head class (testfixtures.SKLive), which is what this screen used to
// print raw.

// TestPresetsNameSlotsUseSlotAuthority pins every name slot on the preset list:
// the row label, the in-place marker recompute (syncMarker), the default status
// line, the duplicate confirmation and the delete body.
func TestPresetsNameSlotsUseSlotAuthority(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	plant := testfixtures.SKLive
	tail := plant[len("sk_live_"):]
	mask := providers.MaskSlot(plant)
	if mask == plant {
		t.Fatalf("fixture is not the underscore-head class: MaskSlot(%q) = %q", plant, mask)
	}
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&fanout.PresetsFile{
		DefaultPreset: plant,
		Presets: []fanout.Preset{
			{Name: plant, Seats: "native:a"},
			{Name: "nightly", N: 2},
		},
	}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	app, scr := presetsAppAt(st, 100, 30)
	scr.app = app
	out := app.Frame().String()
	if strings.Contains(out, plant) || strings.Contains(out, tail) {
		t.Fatalf("the list frame echoed the credential-shaped preset name raw:\n%s", out)
	}
	if !strings.Contains(out, mask) {
		t.Fatalf("the list frame is missing the masked name %q:\n%s", mask, out)
	}
	if !strings.Contains(out, "nightly") {
		t.Fatalf("benign preset name lost its exact rendering:\n%s", out)
	}
	if got := scr.statusLine(); strings.Contains(got, plant) || !strings.Contains(got, mask) {
		t.Fatalf("status line = %q; want the masked name and no raw plant", got)
	}

	// The ● marker line goes through the in-place recompute (syncMarker), the
	// slot that stayed on MaskOperand longest.
	scr.selectPreset(0)
	scr.syncMarker()
	lines := app.Frame().String()
	if !strings.Contains(lines, mask) || strings.Contains(lines, plant) {
		t.Fatalf("marker recompute is not masked:\n%s", lines)
	}
	// The recomputed label must equal the build-time label (the ● is the only
	// difference) - that is the byte-identical benign half.
	for _, r := range scr.rows {
		if r.ID != presetRowPrefix+plant {
			continue
		}
		if want := "● " + mask; r.Label != want {
			t.Fatalf("marker label = %q, want %q", r.Label, want)
		}
	}

	// Duplicate: the success message names the copy derived from the plant.
	scr.selectPreset(0)
	scr.duplicate(scr.pf.Presets[0])
	if msg := app.Message(); strings.Contains(msg, plant) || strings.Contains(msg, tail) {
		t.Fatalf("duplicate message echoed the plant: %q", msg)
	}

	// Delete: the confirm body names the row.
	scr.selectPreset(0)
	scr.askDelete(scr.pf.Presets[0])
	if app.Modal() == nil {
		t.Fatal("delete did not open its confirm")
	}
	body := strings.Join(app.Modal().Body(100), "\n")
	if strings.Contains(body, plant) || strings.Contains(body, tail) {
		t.Fatalf("delete body echoed the plant: %q", body)
	}
	if !strings.Contains(body, mask) {
		t.Fatalf("delete body missing the masked name %q: %q", mask, body)
	}

	// Default-set: the M4 approval body and the success message both name the
	// row (r5 code-seat3 F-04 - the one M-1 slot with no planted pin).
	for i := range scr.pf.Presets {
		if scr.pf.Presets[i].Name == "nightly" {
			scr.selectPreset(i)
			scr.setDefault(scr.pf.Presets[i])
		}
	}
	if app.Modal() == nil {
		t.Fatal("set-default did not open its approval gate")
	}
	approval := strings.Join(app.Modal().Body(100), "\n")
	if strings.Contains(approval, "nightly") == false {
		t.Fatalf("the approval body lost the benign name: %q", approval)
	}
	app.Step(KeyYes)
	if msg := app.Message(); strings.Contains(msg, plant) || strings.Contains(msg, tail) {
		t.Fatalf("default-set message echoed the plant: %q", msg)
	}

	// The plant ITSELF through the same slot (r5 code-seat3 F-04): the M4 approval
	// always opens, and both the gate body and the success line name the row
	// through the slot authority.
	scr.selectPreset(0)
	scr.setDefault(scr.pf.Presets[0])
	if app.Modal() == nil {
		t.Fatal("set-default did not open its approval gate for the planted row")
	}
	gate := strings.Join(app.Modal().Body(100), " / ")
	if strings.Contains(gate, plant) || strings.Contains(gate, tail) {
		t.Fatalf("default-set gate echoed the plant: %q", gate)
	}
	if !strings.Contains(gate, mask) {
		t.Fatalf("default-set gate missing the masked name %q: %q", mask, gate)
	}
	app.Step(KeyYes)
	if msg := app.Message(); strings.Contains(msg, plant) || strings.Contains(msg, tail) {
		t.Fatalf("default-set message echoed the plant: %q", msg)
	}
	if msg := app.Message(); !strings.Contains(msg, "Default preset set to "+mask) {
		t.Fatalf("default-set message = %q; want the masked default-set wording", msg)
	}
}

// TestPresetsBuiltinCopyNoteFitsAndRenders pins r11 F-04 (P3): the built-in
// row's copy note fits its 80-cell budget in every locale, and it renders in
// full on a frame wide enough to hold the row (list rows are single-line, so
// at 80 cols the row shows the engine-default summary plus the note's leading
// cells; the note itself is never dropped or reordered).
func TestPresetsBuiltinCopyNoteFitsAndRenders(t *testing.T) {
	locales := []string{"en", "ko", "ja", "vi", "de", "fr", "it", "ru", "ar", "hi", "zh-Hans", "zh-Hant"}
	for _, loc := range locales {
		b := i18n.Load(loc)
		if n := cellwidth.String(b.T("presets.builtin.copy_note")); n > 80 {
			t.Fatalf("presets.builtin.copy_note [%s] = %d cells, want <= 80", loc, n)
		}
	}

	st, _ := presetsFixture(t)
	b := i18n.Load("en")
	note := b.T("presets.builtin.copy_note")

	// Wide frame: the whole row, note included, renders untruncated.
	wide, _ := presetsAppAt(st, 220, 30)
	wide.CaptureRun(&ScriptedKeys{})
	builtinRow := ""
	for _, l := range wide.Frame().Lines {
		if strings.Contains(l, fanout.BuiltinPresetName) {
			builtinRow = l
		}
	}
	if builtinRow == "" || !strings.Contains(builtinRow, note) {
		t.Fatalf("built-in row at 220 cols must carry the full copy note %q; row = %q", note, builtinRow)
	}

	// Canonical 80-col frame: the row is the honest cellwidth cut of the same
	// line, so the summary stays and the note leads the visible tail. The
	// label column width mirrors stepList.Render: the widest visible label.
	narrow, narrowScr := presetsAppAt(st, 80, 30)
	narrow.CaptureRun(&ScriptedKeys{})
	labelW := 0
	for _, r := range narrowScr.rows {
		if cw := cellwidth.String(r.Label); cw > labelW {
			labelW = cw
		}
	}
	want := cellwidth.Truncate("▶ [ 1] "+cellwidth.PadRight(fanout.BuiltinPresetName, labelW)+"  "+
		builtinDefaultsSummary(b)+" · "+note, 80)
	got := ""
	for _, l := range narrow.Frame().Lines {
		if strings.Contains(l, fanout.BuiltinPresetName) {
			got = l
		}
	}
	if got != want {
		t.Fatalf("built-in row at 80 cols =\n  %q\nwant\n  %q", got, want)
	}
	if !strings.Contains(got, "task=review") {
		t.Fatalf("the 80-col row must keep the engine-default summary: %q", got)
	}
}

// --- P2: the built-in defaults row is an editable seed record -------------
//
// Contract A4: row [1] renders the seed record when it exists on disk (its
// flag summary plus the seats-pinned note, and the default badge when the
// key points at it) and the engine composite when it does not. The ordinary
// preset-row loop skips the seed record; every count/index site routes
// through visiblePresets. Delete on row [1] is RESET; Set default is legal
// and equivalent to no key only while the seed record exists.

// seedStore seeds a store with the seed record (seats pinned, one visible
// field) plus the ordinary alpha row, so row [1] is the seed and row [2] is
// the first ordinary preset.
func seedStore(t *testing.T, defaultName string) *fanout.PresetFileStore {
	t.Helper()
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	pf := &fanout.PresetsFile{
		Presets: []fanout.Preset{
			{Name: fanout.BuiltinPresetName, Task: "security", Seats: "native:a,nw/m1"},
			{Name: "alpha", N: 2},
		},
		DefaultPreset: defaultName,
	}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return st
}

// TestPresetsSeedRecordRendersOnRowOneOnly pins the A4 visibility rule: the
// seed record renders ON row [1] (its own flag summary plus the seats-pinned
// note), never again as an ordinary preset row, and the digits keep their
// meaning (the seed adds no row of its own).
func TestPresetsSeedRecordRendersOnRowOneOnly(t *testing.T) {
	st := seedStore(t, "alpha")
	app, scr := presetsAppAt(st, 100, 30)
	app.CaptureRun(&ScriptedKeys{})
	body := strings.Join(app.Frame().Lines, "\n")
	for _, want := range []string{fanout.BuiltinPresetName, "task=security", "seats pinned: 2", "alpha", "n=2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("seed list frame missing %q; frames:\n%s", want, body)
		}
	}
	count := 0
	for _, r := range scr.rows {
		if r.ID == presetRowPrefix+fanout.BuiltinPresetName {
			t.Fatalf("the seed rendered as an ordinary preset row: %+v", r)
		}
		if strings.Contains(r.Label, fanout.BuiltinPresetName) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("row [1] name rendered %d times, want exactly 1 (rows: %+v)", count, scr.rows)
	}
	// Row [1] note must NOT show the absent-seed engine preview.
	for _, r := range scr.rows {
		if r.ID == presetsRowIDBuiltin && strings.Contains(r.Note, "task=review") {
			t.Fatalf("row [1] still shows the engine preview although the seed exists: %q", r.Note)
		}
	}
	// Digits: the seed is not a row, so alpha stays digit 2 and Edit digit 4.
	app2, opened := newPresetsApp(st, nil)
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(2)}})
	if len(*opened) != 1 || (*opened)[0] != "alpha" {
		t.Fatalf("digit 2 opened %v, want [alpha] (no seed off-by-one)", *opened)
	}
	app2, opened = newPresetsApp(st, nil)
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, Digit(4)}})
	if len(*opened) != 1 || (*opened)[0] != "alpha" {
		t.Fatalf("Edit opened %v, want [alpha]", *opened)
	}
}

// TestPresetsSeedRowDefaultBadgeSaysNoKey pins the A4 set-default note: when
// default_preset points at the seed, row [1] carries the default badge with
// the equivalence parenthetical (the key resolves the same record as no
// key).
func TestPresetsSeedRowDefaultBadgeSaysNoKey(t *testing.T) {
	st := seedStore(t, fanout.BuiltinPresetName)
	app, scr := presetsAppAt(st, 220, 30)
	app.CaptureRun(&ScriptedKeys{})
	for _, r := range scr.rows {
		if r.ID != presetsRowIDBuiltin {
			continue
		}
		if !strings.Contains(r.Note, "default") || !strings.Contains(r.Note, "same as no key") {
			t.Fatalf("keyed seed row note = %q; want the badge plus the no-key equivalence", r.Note)
		}
	}
	if got := scr.statusLine(); !strings.Contains(got, fanout.BuiltinPresetName) {
		t.Fatalf("status line = %q; want the seed named as the default", got)
	}
}

// TestPresetsSeedRowFrameAccountingExact pins the chrome side of A4: with
// the seed record on disk plus 13 ordinary presets, the seed contributes
// zero rows (1 built-in + 13 visible + 5 actions), the 80x24 frame stays
// exactly 24 lines, the page size stays 24-presetsListChrome, and the last
// row is reachable.
func TestPresetsSeedRowFrameAccountingExact(t *testing.T) {
	st := presetsBulkStore(t, 13)
	pf := reloadPresets(t, st)
	pf.Presets = append(pf.Presets, fanout.Preset{Name: fanout.BuiltinPresetName, Seats: "native:a"})
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("add the seed: %v", err)
	}
	app, scr := presetsAppAt(st, 80, 24)
	app.CaptureRun(&ScriptedKeys{})
	if got, want := len(scr.rows), 1+13+5; got != want {
		t.Fatalf("rows = %d, want %d (the seed adds no row): %+v", got, want, scr.rows)
	}
	if got, want := scr.list.PageSize(), 24-presetsListChrome; got != want {
		t.Fatalf("page size = %d, want %d (80x24 minus %d chrome rows)", got, want, presetsListChrome)
	}
	if len(scr.rows) <= scr.list.PageSize() {
		t.Fatalf("fixture does not fill the window: %d rows, page %d", len(scr.rows), scr.list.PageSize())
	}
	frame := app.Frame()
	if len(frame.Lines) != 24 {
		t.Fatalf("full-page frame = %d lines, want 24; frames:\n%s", len(frame.Lines), strings.Join(frame.Lines, "\n"))
	}
	if last := frame.Lines[len(frame.Lines)-1]; last != baseFooter {
		t.Fatalf("base footer cut on a full page; last line = %q", last)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnd}})
	if got, want := scr.list.Cursor(), len(scr.rows)-1; got != want {
		t.Fatalf("End cursor = %d, want %d (last row reachable with the seed on disk)", got, want)
	}
}

// TestPresetsBuiltinResetGate pins the A4 reset verb: with the seed pinned,
// Delete is RESET - the M3 y/n gate keeps its shape, y removes the seed
// record (every field, seats included), clears a default key that pointed at
// it, and the message says the row returns to engine defaults. n writes
// nothing.
func TestPresetsBuiltinResetGate(t *testing.T) {
	t.Run("yes resets", func(t *testing.T) {
		st := seedStore(t, fanout.BuiltinPresetName)
		app, _ := newPresetsApp(st, nil)
		// Rows: 1 seed, 2 alpha, 3 New, 4 Edit, 5 Duplicate, 6 Delete, 7 Set default.
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeySpace, Digit(6)}})
		m := app.Modal()
		if m == nil {
			t.Fatal("reset did not open its confirmation gate")
		}
		if got, want := m.Title(), Bundle().T("presets.reset.title"); got != want {
			t.Fatalf("reset gate title = %q, want %q", got, want)
		}
		body := strings.Join(m.Body(app.Width()), "\n")
		wantBody := Bundle().Tf("presets.reset.body", map[string]string{"name": fanout.BuiltinPresetName})
		if body != wantBody || !strings.Contains(body, "engine defaults") {
			t.Fatalf("reset gate body = %q, want %q", body, wantBody)
		}
		if strings.Contains(body, "will be deleted") || strings.Contains(m.Title(), "Delete") {
			t.Fatalf("reset gate still uses delete copy: title=%q body=%q", m.Title(), body)
		}
		out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
		pf := reloadPresets(t, st)
		if _, ok := pf.FindPreset(fanout.BuiltinPresetName); ok {
			t.Fatalf("reset left the seed on disk: %+v", pf.Presets)
		}
		if pf.DefaultPreset != "" {
			t.Fatalf("reset left the dangling default key: %q", pf.DefaultPreset)
		}
		if _, ok := pf.FindPreset("alpha"); !ok {
			t.Fatalf("reset removed a neighbour: %+v", pf.Presets)
		}
		if !strings.Contains(app.Message(), "returns to the engine defaults") {
			t.Fatalf("reset message = %q; want the engine-defaults sentence", app.Message())
		}
		// Row [1] is the engine preview again.
		if !strings.Contains(out, "task=review n=3 timeout=300s tools=read,search") {
			t.Fatalf("row [1] did not return to the engine preview; frames:\n%s", out)
		}
	})

	t.Run("n writes nothing", func(t *testing.T) {
		st := seedStore(t, fanout.BuiltinPresetName)
		before := reloadPresets(t, st)
		app, _ := newPresetsApp(st, nil)
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeySpace, Digit(6), KeyNo}})
		after := reloadPresets(t, st)
		if len(after.Presets) != len(before.Presets) || after.DefaultPreset != before.DefaultPreset {
			t.Fatalf("reset-n wrote: before %+v, after %+v", before, after)
		}
		if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
			t.Fatalf("reset-n message = %q", app.Message())
		}
	})
}

// TestPresetsSetDefaultOnSeedRow pins the A4 set-default legality: the M4
// approval gate names the seed byte-identically through the slot authority,
// y writes default_preset = BuiltinPresetName, and the success message names
// the seed.
func TestPresetsSetDefaultOnSeedRow(t *testing.T) {
	st := seedStore(t, "alpha")
	app, _ := newPresetsApp(st, nil)
	// Pin the seed row, activate Set default (row 7), read the gate.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeySpace, Digit(7)}})
	m := app.Modal()
	if m == nil {
		t.Fatal("set-default on the seed did not open the M4 approval gate")
	}
	body := strings.Join(m.Body(app.Width()), "\n")
	for _, want := range []string{
		"Sets default_preset: alpha -> " + fanout.BuiltinPresetName,
		"+ default_preset = " + fanout.BuiltinPresetName,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("set-default gate body missing %q\n%s", want, body)
		}
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
	pf := reloadPresets(t, st)
	if pf.DefaultPreset != fanout.BuiltinPresetName {
		t.Fatalf("default_preset = %q, want the seed", pf.DefaultPreset)
	}
	if !strings.Contains(app.Message(), "Default preset set to "+fanout.BuiltinPresetName) {
		t.Fatalf("set-default message = %q", app.Message())
	}
}

// TestPresetsSeedRowMaskingAtNameSinks is the A7 planted-token pin for the
// P2-owned name sinks: row [1]'s label renders the benign reserved name
// BYTE-IDENTICAL through presetNameOperand, the same authority at the same
// frame masks a credential-shaped ordinary preset name (the 16-rune
// underscore-head class, masked to sk_li****), the status line naming the
// keyed seed is byte-identical, and the reset gate body names the seed
// byte-identical.
func TestPresetsSeedRowMaskingAtNameSinks(t *testing.T) {
	t.Setenv(providers.EnvHome, t.TempDir())
	plant := testfixtures.SKLive
	tail := plant[len("sk_live_"):]
	mask := providers.MaskSlot(plant)
	if mask == plant {
		t.Fatalf("fixture is not the underscore-head class: MaskSlot(%q) = %q", plant, mask)
	}
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&fanout.PresetsFile{
		DefaultPreset: fanout.BuiltinPresetName,
		Presets: []fanout.Preset{
			{Name: fanout.BuiltinPresetName, Seats: "native:a"},
			{Name: plant, N: 3},
		},
	}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	app, scr := presetsAppAt(st, 100, 30)
	scr.app = app
	out := app.Frame().String()
	// Benign half: the reserved name renders unchanged at every sink.
	if !strings.Contains(out, fanout.BuiltinPresetName) {
		t.Fatalf("row [1] lost the reserved name:\n%s", out)
	}
	if got := presetNameOperand(fanout.BuiltinPresetName); got != fanout.BuiltinPresetName {
		t.Fatalf("the slot authority must be byte-identical on the benign name, got %q", got)
	}
	if got := scr.statusLine(); !strings.Contains(got, fanout.BuiltinPresetName) {
		t.Fatalf("status line lost the keyed seed name: %q", got)
	}
	// Masking half: the planted name in the same frame rides masked, raw
	// plant and tail both absent.
	if strings.Contains(out, plant) || strings.Contains(out, tail) {
		t.Fatalf("the list frame echoed the credential-shaped name raw:\n%s", out)
	}
	if !strings.Contains(out, mask) {
		t.Fatalf("the list frame is missing the masked name %q:\n%s", mask, out)
	}
	// The marker recompute (syncMarker) is the same sink after a move.
	scr.selectPreset(0)
	scr.syncMarker()
	if lines := app.Frame().String(); !strings.Contains(lines, mask) || strings.Contains(lines, plant) {
		t.Fatalf("marker recompute is not masked:\n%s", lines)
	}
	// Reset gate body: names the seed byte-identical through the authority.
	scr.selectSeed()
	scr.askDelete(*seedRecord(scr.pf))
	if app.Modal() == nil {
		t.Fatal("reset did not open its confirm")
	}
	gate := strings.Join(app.Modal().Body(100), "\n")
	if !strings.Contains(gate, fanout.BuiltinPresetName) {
		t.Fatalf("reset gate body lost the seed name: %q", gate)
	}
	scr.askResetBuiltin(presetNameOperand(plant))
	gate = strings.Join(app.Modal().Body(100), "\n")
	if !strings.Contains(gate, mask) || strings.Contains(gate, plant) || strings.Contains(gate, tail) {
		t.Fatalf("reset gate body did not preserve the planted-name mask: %q", gate)
	}
}

// TestPresetsRowOneEditorSaveCreatesSeed is the acceptance-shape pin: row
// [1] > Enter opens the REAL editor, a seat edit there saves the seed record
// to disk (P1's ladder arm is what a later bare run consumes), and the list
// row [1] note follows it - all without leaving the TUI flow.
func TestPresetsRowOneEditorSaveCreatesSeed(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	st := &fanout.PresetFileStore{Path: path}
	if err := st.SavePresets(&fanout.PresetsFile{Presets: []fanout.Preset{{Name: "alpha", N: 2}}}); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
	app, _ := newPresetsApp(st, func(name string, a *App) {
		a.Push(NewPresetEditorScreen(name))
	})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(1), KeyEnter}})
	top, ok := app.Top().(*presetEditorScreen)
	if !ok || !top.seedEdit() {
		t.Fatalf("row [1] Enter must push the seed editor, got %T", app.Top())
	}
	// Type one seat into the lineup and save, then close back to the list.
	// (Pop clears the transient message by design, C1-F13, so the save
	// message is read before the close.)
	top.appendSeat("native:gemini-seed-a")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset "+fanout.BuiltinPresetName+" saved.") {
		t.Fatalf("seed save message = %q", app.Message())
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyRight}})
	pf := reloadPresets(t, st)
	seed, ok := pf.FindPreset(fanout.BuiltinPresetName)
	if !ok || seed.Seats != "native:gemini-seed-a" {
		t.Fatalf("seed record missing on disk: %+v", pf.Presets)
	}
	if seed.Task != "" || seed.N != 0 {
		t.Fatalf("untouched seed fields must stay empty: %+v", *seed)
	}
	// The list follows: row [1] shows the seats-pinned note, still once.
	if !strings.Contains(out, "seats pinned: 1") {
		t.Fatalf("row [1] note did not follow the save; frames:\n%s", out)
	}
	if strings.Contains(out, presetRowPrefix+fanout.BuiltinPresetName) {
		t.Fatalf("the seed leaked into an ordinary row after the save")
	}
}

func builtinOnlyStore(t *testing.T, defaultName string) *fanout.PresetFileStore {
	t.Helper()
	t.Setenv(providers.EnvHome, t.TempDir())
	st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&fanout.PresetsFile{
		Presets:       []fanout.Preset{{Name: fanout.BuiltinPresetName, Seats: "native:a"}},
		DefaultPreset: defaultName,
	}); err != nil {
		t.Fatalf("save seed-only store: %v", err)
	}
	return st
}

func TestPresetsEmptyNoticeTracksStoreContents(t *testing.T) {
	t.Run("neither seed nor ordinary preset", func(t *testing.T) {
		t.Setenv(providers.EnvHome, t.TempDir())
		st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
		if err := st.SavePresets(&fanout.PresetsFile{}); err != nil {
			t.Fatalf("save empty store: %v", err)
		}
		out, _ := presetsKeys(t, st, nil)
		if !strings.Contains(out, "No presets yet.") {
			t.Fatalf("fully empty store lost its empty notice:\n%s", out)
		}
	})
	t.Run("seed only", func(t *testing.T) {
		out, _ := presetsKeys(t, builtinOnlyStore(t, ""), nil)
		if strings.Contains(out, "No presets yet.") {
			t.Fatalf("seed-only store shows an empty notice over the populated row:\n%s", out)
		}
		if !strings.Contains(out, fanout.BuiltinPresetName) || !strings.Contains(out, "seats pinned: 1") {
			t.Fatalf("seed-only row is not populated:\n%s", out)
		}
	})
}

func TestPresetsSeedAwareCursorReassertIgnoresActionRows(t *testing.T) {
	st := seedStore(t, "alpha")
	app, _ := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)
	app.Step(KeyDown)
	if got := scr.selectedName(); got != "alpha" {
		t.Fatalf("first ordinary row selected %q, want alpha", got)
	}
	app.Step(KeyDown) // New is an action row, immediately after the only ordinary row.
	if got := scr.selectedName(); got != "alpha" {
		t.Fatalf("action-row transit cleared or moved the selection to %q", got)
	}
	if body := app.Frame().String(); !strings.Contains(body, "● alpha") {
		t.Fatalf("action-row transit removed the ordinary-row marker:\n%s", body)
	}
}

func TestPresetsSeedAwareSpaceBoundaryIgnoresActionRows(t *testing.T) {
	app, _ := newPresetsApp(builtinOnlyStore(t, ""), nil)
	scr := app.Top().(*presetsScreen)
	app.Step(KeyDown) // With no ordinary rows, row 2 is New, not a preset.
	app.Step(KeySpace)
	if scr.locked || scr.sel != -1 || scr.selName != "" {
		t.Fatalf("Space on New selected a nonexistent ordinary preset: sel=%d name=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	if got := app.Message(); got != "" {
		t.Fatalf("Space on New produced a pin message %q", got)
	}
}

func TestPresetsSeedPinDropsWhenSeedVanishes(t *testing.T) {
	st := seedStore(t, fanout.BuiltinPresetName)
	app, opened := newPresetsApp(st, nil)
	scr := app.Top().(*presetsScreen)
	app.Step(KeySpace)
	if !scr.locked || !scr.seedSelected() {
		t.Fatalf("seed pin did not arm: sel=%d name=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	if err := st.SavePresets(&fanout.PresetsFile{
		Presets:       []fanout.Preset{{Name: "alpha", N: 2}},
		DefaultPreset: "alpha",
	}); err != nil {
		t.Fatalf("externally remove seed: %v", err)
	}
	app.Frame()
	if scr.locked || scr.sel != -1 || scr.selName != "" {
		t.Fatalf("seed pin survived its record: sel=%d name=%q locked=%v", scr.sel, scr.selName, scr.locked)
	}
	if body := app.Frame().String(); strings.Contains(body, "●") {
		t.Fatalf("vanished seed still marks a row:\n%s", body)
	}
	app.Step(Digit(6)) // Delete on the remaining one-ordinary-row list.
	if got, want := app.Message(), Bundle().T("presets.msg.select_first"); got != want {
		t.Fatalf("Delete after seed vanished = %q, want %q", got, want)
	}
	if app.Modal() != nil || len(*opened) != 0 {
		t.Fatalf("vanished seed retargeted a verb: modal=%T opened=%v", app.Modal(), *opened)
	}
}

func TestPresetsBuiltinChromeLocaleFitMatrix(t *testing.T) {
	locales := []string{"en", "ko", "ja", "vi", "de", "fr", "it", "ru", "ar", "hi", "zh-Hans", "zh-Hant"}
	for _, loc := range locales {
		b := i18n.Load(loc)
		equiv := b.T("presets.builtin.default_equiv")
		title := b.T("presets.reset.title")
		body := b.Tf("presets.reset.body", map[string]string{"name": fanout.BuiltinPresetName})
		for key, value := range map[string]string{
			"presets.builtin.default_equiv": equiv,
			"presets.reset.title":           title,
			"presets.reset.body":            body,
		} {
			if value == "" || strings.Contains(value, "MISSING-KEY") {
				t.Fatalf("%s [%s] did not resolve: %q", key, loc, value)
			}
			if got := cellwidth.String(value); got > 80 {
				t.Fatalf("%s [%s] is %d cells at the 80-column budget: %q", key, loc, got, value)
			}
		}
		gate := NewConfirm(title, []string{body}, nil)
		if got := gate.Title(); got != title {
			t.Fatalf("%s reset title changed at the modal sink: %q", loc, got)
		}
		if got := gate.Body(80); len(got) != 1 || got[0] != body {
			t.Fatalf("%s reset body changed or gained rows at the modal sink: %q", loc, got)
		}
	}
}

func TestPresetsDefaultEquivalenceRequiresSeed(t *testing.T) {
	t.Run("existing seed", func(t *testing.T) {
		st := seedStore(t, fanout.BuiltinPresetName)
		_, scr := presetsAppAt(st, 220, 30)
		note := scr.builtinRowNote()
		if want := i18n.Load("en").T("presets.builtin.default_equiv"); !strings.Contains(note, want) {
			t.Fatalf("existing keyed seed note = %q, want equivalence %q", note, want)
		}
	})
	t.Run("absent seed with dangling key", func(t *testing.T) {
		t.Setenv(providers.EnvHome, t.TempDir())
		st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
		if err := st.SavePresets(&fanout.PresetsFile{DefaultPreset: fanout.BuiltinPresetName}); err != nil {
			t.Fatalf("save dangling key: %v", err)
		}
		app, scr := presetsAppAt(st, 220, 30)
		note := scr.builtinRowNote()
		if !strings.Contains(note, i18n.Load("en").T("presets.default.badge")) {
			t.Fatalf("dangling key lost its default badge: %q", note)
		}
		if strings.Contains(note, i18n.Load("en").T("presets.builtin.default_equiv")) || strings.Contains(note, "same as no key") {
			t.Fatalf("absent seed still claims no-key equivalence: %q", note)
		}
		app.Step(KeySpace) // No seed means row [1] cannot be pinned for Set default.
		app.Step(Digit(6)) // Set default on the zero-ordinary-row list.
		if got, want := app.Message(), Bundle().T("presets.msg.select_first"); got != want {
			t.Fatalf("Set default without a seed = %q, want %q", got, want)
		}
		if app.Modal() != nil {
			t.Fatal("Set default without a seed opened a write gate")
		}
	})
}
