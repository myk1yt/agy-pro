package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
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
	const planted = "AKIAIOSFODNN7EXAMPLE"
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
