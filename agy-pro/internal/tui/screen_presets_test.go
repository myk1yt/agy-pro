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
		builtinPresetRowLabel,
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
		"Not a seat list. Run: agy pro fanout --preset <name>",
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
		{"seats only", fanout.Preset{Name: "x", Seats: "native:gemini-x"}, "1 of 14 fields pinned: see Edit"},
		{"models+out_dir+json", fanout.Preset{Name: "x", Models: "prov/m1", OutDir: "reports", JSON: true}, "3 of 14 fields pinned: see Edit"},
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
	for _, want := range []string{"hidden", "1 of 14 fields pinned: see Edit", "empty", "(empty)"} {
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

// TestPresetsBuiltinRowOpensReadonlyView pins the fixed first row: Enter
// opens the editor with the builtin sentinel (the shell maps it to the
// read-only engine-defaults view).
func TestPresetsBuiltinRowOpensReadonlyView(t *testing.T) {
	st, _ := presetsFixture(t)
	_, opened := presetsKeys(t, st, []Key{Digit(1)})
	if len(*opened) != 1 || (*opened)[0] != builtinPresetSentinel {
		t.Fatalf("builtin row opened %v, want [%q]", *opened, builtinPresetSentinel)
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
// authority for the D9/D13/D14 rules). A copy of a row that violates them must
// therefore be refused by the same authority before anything is written, and a
// copy of a valid row must still round-trip to disk unchanged.
func TestPresetsDuplicateRunsSaveGate(t *testing.T) {
	t.Run("refuses a copy of an invalid row", func(t *testing.T) {
		// SavePresets is deliberately permissive (the P3-1 default-key guard
		// seeds exactly this shape), so the model+seats row below loads fine and
		// only Duplicate would turn it into a second unrunnable record.
		t.Setenv(providers.EnvHome, t.TempDir())
		path := filepath.Join(t.TempDir(), "fanout-presets.json")
		st := &fanout.PresetFileStore{Path: path}
		seed := &fanout.PresetsFile{Presets: []fanout.Preset{
			{Name: "conflict", Models: "prov/m1", Seats: "native:gemini-x"},
		}}
		if err := st.SavePresets(seed); err != nil {
			t.Fatalf("seed conflicting preset: %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read store before duplicate: %v", err)
		}

		// One preset row puts Duplicate at digit 5 (builtin, preset, new, edit,
		// duplicate); KeyDown arms the conflicting row.
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
		if want := Bundle().T("common.error") + ": " + presetsEditorValidationSeatConflict; app.Message() != want {
			t.Fatalf("refusal message = %q, want %q", app.Message(), want)
		}
		if !strings.Contains(out, "models and seats cannot be combined") {
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

// TestPresetsSetDefaultWritesStoreKey pins the Set-default flow: the store's
// default_preset key is rewritten and the status line follows.
func TestPresetsSetDefaultWritesStoreKey(t *testing.T) {
	st, _ := presetsFixture(t)
	// Down Down arms beta (row 3); digit 8 activates Set default.
	out, _ := presetsKeys(t, st, []Key{KeyDown, KeyDown, Digit(8)})
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
	if !strings.Contains(out, "No presets yet.") || !strings.Contains(out, builtinPresetRowLabel) {
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
