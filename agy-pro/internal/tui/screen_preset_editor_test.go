package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
)

// editorHome isolates the editor: AGY_PRO_HOME gets a fresh temp dir (the
// preset store and state.json resolve under it) and AGY_PRO_PROVIDERS points
// at a nonexistent temp path (no defaults block unless a test writes one).
// It returns the preset-file path the editor will use.
func editorHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv(providers.EnvProviders, filepath.Join(home, "providers.json"))
	return filepath.Join(home, "fanout-presets.json")
}

// editorStore returns the file store at the isolated presets path.
func editorStore(t *testing.T, path string) *fanout.PresetFileStore {
	t.Helper()
	return &fanout.PresetFileStore{Path: path}
}

// seedEditorPresets writes a presets file at the isolated path.
func seedEditorPresets(t *testing.T, path string, pf *fanout.PresetsFile) {
	t.Helper()
	if err := editorStore(t, path).SavePresets(pf); err != nil {
		t.Fatalf("seed presets: %v", err)
	}
}

// newEditorApp pushes the editor over a stub root, so pop flows have
// somewhere to land. It returns the app and the concrete screen for
// white-box assertions.
func newEditorApp(name string) (*App, *presetEditorScreen) {
	app := NewApp(120, 40, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPresetEditorScreen(name)
	app.Push(scr)
	return app, scr.(*presetEditorScreen)
}

// typePresetRunes converts text into the per-rune key sequence EDIT mode expects.
func typePresetRunes(s string) []Key {
	var ks []Key
	for _, r := range s {
		ks = append(ks, RuneKey(r))
	}
	return ks
}

// joinPresetKeys concatenates key sequences.
func joinPresetKeys(parts ...[]Key) []Key {
	var out []Key
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// k1 wraps bare keys for joinPresetKeys.
func k1(keys ...Key) []Key { return keys }

// zoneAKeys walks the zone-A fields (name..max_native): one Enter per field,
// each validating and advancing, landing the focus in the host zone.
func zoneAKeys(fields ...string) []Key {
	var ks []Key
	for _, f := range fields {
		ks = append(ks, typePresetRunes(f)...)
		ks = append(ks, KeyEnter)
	}
	return ks
}

// zoneBKeys walks the zone-B fields (search_endpoint..json) by Enter.
func zoneBKeys(fields ...string) []Key {
	var ks []Key
	for _, f := range fields {
		if f != "" {
			ks = append(ks, typePresetRunes(f)...)
		}
		ks = append(ks, KeyEnter)
	}
	return ks
}

// TestEditorSavesRoundTrip drives the full grid: scalar fields, the host
// sub-list (inline add), and the json switch, then Ctrl+S, and pins the
// on-disk record field by field.
func TestEditorSavesRoundTrip(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "review", "4", "120", "read,search", "8", "2"),
		// Host zone (focus lands on the Add row): type + commit one host,
		// then Down past the last row into zone B.
		typePresetRunes("example.com"), k1(KeyEnter),
		k1(KeyDown, KeyDown, KeyDown),
		// Zone B: skip search_endpoint, search_key_env, out_dir, root,
		// models, seats; set json; save.
		zoneBKeys("", "", "", "", "", ""),
		typePresetRunes("true"),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("save message = %q; want saved", app.Message())
	}
	if app.Depth() != 2 {
		t.Fatalf("editor should stay open after save; depth = %d", app.Depth())
	}
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil {
		t.Fatalf("reload presets: %v", err)
	}
	if len(pf.Presets) != 1 {
		t.Fatalf("on-disk presets = %+v, want exactly one", pf.Presets)
	}
	got := pf.Presets[0]
	want := fanout.Preset{
		Name:           "p1",
		Task:           "review",
		N:              4,
		TimeoutSeconds: 120,
		Tools:          "read,search",
		MaxTurns:       8,
		MaxNative:      2,
		AllowHosts:     []string{"example.com"},
		JSON:           true,
	}
	if got.Name != want.Name || got.Task != want.Task || got.N != want.N ||
		got.TimeoutSeconds != want.TimeoutSeconds || got.Tools != want.Tools ||
		got.MaxTurns != want.MaxTurns || got.MaxNative != want.MaxNative ||
		got.JSON != want.JSON || strings.Join(got.AllowHosts, ",") != strings.Join(want.AllowHosts, ",") {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestEditorSearchKeyEnvRejectsValue pins the secrets boundary: a
// value-shaped search_key_env is rejected at save time, the error never
// echoes the operand, and no file is written.
func TestEditorSearchKeyEnvRejectsValue(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp("")
	secret := "sk-livekey1234567890abcd"
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		// Host zone -> zone B (no hosts): two Downs cross into zone B on
		// the Remove row, one Enter lands on search_key_env.
		k1(KeyDown, KeyDown, KeyEnter),
		typePresetRunes(secret),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(app.Message(), "environment variable NAME") {
		t.Fatalf("rejection message = %q; want the NAME-only error", app.Message())
	}
	if strings.Contains(app.Message(), secret) {
		t.Fatal("the rejection message echoed the rejected operand")
	}
	// The field keeps the text for correction; the echo rule is about
	// messages and persistence, and the file check below pins the rest.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("preset file was written despite rejection: %v", err)
	}
}

// TestEditorAcceptsEnvVarName pins the positive half of the boundary: a
// conventional UPPER_SNAKE env-var NAME is accepted and persisted.
func TestEditorAcceptsEnvVarName(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown, KeyEnter),
		typePresetRunes("MY_SEARCH_KEY"),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil || len(pf.Presets) != 1 {
		t.Fatalf("expected one saved preset: err=%v pf=%+v", err, pf)
	}
	if pf.Presets[0].SearchKeyEnv != "MY_SEARCH_KEY" {
		t.Fatalf("search_key_env = %q, want MY_SEARCH_KEY", pf.Presets[0].SearchKeyEnv)
	}
}

// TestResolvePresetSources pins the source-column rung mapping against the
// buildRunConfig ladder: defaults block vs engine default, preset rung, and
// the flag rung input.
func TestResolvePresetSources(t *testing.T) {
	def := providers.Defaults{N: 5, TimeoutSeconds: 120}

	noPreset := resolvePresetSources(presetLadderInputs{set: map[string]bool{}, def: def})
	for field, want := range map[string]string{
		"n":               "defaults",
		"timeout_seconds": "defaults",
		"task":            "engine",
		"tools":           "engine",
		"max_turns":       "engine",
		"json":            "engine",
		"max_native":      "state",
		"allow_hosts":     "",
		"search_key_env":  "",
	} {
		if noPreset[field] != want {
			t.Fatalf("no-preset rung[%s] = %q, want %q (all: %v)", field, noPreset[field], want, noPreset)
		}
	}

	withPreset := resolvePresetSources(presetLadderInputs{
		set:    map[string]bool{},
		preset: &fanout.Preset{Name: "p", Task: "security", N: 4, SearchKeyEnv: "MY_KEY"},
		def:    def,
	})
	for field, want := range map[string]string{
		"n":              "preset",
		"task":           "preset",
		"tools":          "engine", // preset pins nothing and the fixture block has no tools
		"search_key_env": "preset",
		"max_native":     "state",
	} {
		if withPreset[field] != want {
			t.Fatalf("preset rung[%s] = %q, want %q (all: %v)", field, withPreset[field], want, withPreset)
		}
	}

	flagged := resolvePresetSources(presetLadderInputs{
		set:    map[string]bool{"n": true, "task": true},
		preset: &fanout.Preset{Name: "p", N: 4, Task: "security"},
		def:    def,
	})
	if flagged["n"] != "flag" || flagged["task"] != "flag" {
		t.Fatalf("flag rung not honored: %v", flagged)
	}
}

// TestEditorSourceColumnRendersDefaultsVsEngine pins the rendered column:
// with a defaults block on disk, n and timeout_seconds name the providers.json
// defaults rung while task names the engine default.
func TestEditorSourceColumnRendersDefaultsVsEngine(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	providersPath := filepath.Join(home, "providers.json")
	t.Setenv(providers.EnvProviders, providersPath)
	if err := providers.WriteFileAtomic(providersPath, []byte(`{"providers": [], "defaults": {"n": 5, "timeoutSeconds": 120}}`), 0o600); err != nil {
		t.Fatalf("seed providers.json: %v", err)
	}
	app, _ := newEditorApp("")
	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, "providers.json defaults") {
		t.Fatalf("defaults rung label missing; frames:\n%s", out)
	}
	if !strings.Contains(out, "engine default") {
		t.Fatalf("engine rung label missing; frames:\n%s", out)
	}
}

// TestEditorDiscardGate pins the dirty-close flow: Right on a dirty form
// opens the discard confirm; n stays, y pops. A clean form pops directly.
func TestEditorDiscardGate(t *testing.T) {
	editorHome(t)
	app, _ := newEditorApp("")
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x'), KeyRight}})
	if !strings.Contains(out, "Discard Changes?") {
		t.Fatalf("dirty close did not open the gate; frames:\n%s", out)
	}
	if app.Depth() != 2 || app.Modal() == nil {
		t.Fatalf("gate should hold the editor open; depth %d modal %v", app.Depth(), app.Modal())
	}

	// n keeps the edit; Right again, then y discards and pops.
	app2, _ := newEditorApp("")
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x'), KeyRight, KeyNo, KeyRight, KeyYes}})
	if app2.Depth() != 1 {
		t.Fatalf("y should pop the editor; depth = %d", app2.Depth())
	}

	// Clean form: Right pops without a gate.
	app3, _ := newEditorApp("")
	app3.CaptureRun(&ScriptedKeys{Keys: []Key{KeyRight}})
	if app3.Depth() != 1 || app3.Modal() != nil {
		t.Fatalf("clean close should pop silently; depth %d modal %v", app3.Depth(), app3.Modal())
	}
}

// TestEditorEscGatesDirtyForm pins the guard behavior (mirroring the model
// form): while the editor is dirty, the transparent guard receives Esc
// before the App pop precedence and opens the discard confirm. n/Esc stays
// with the guard re-armed, y discards and pops. A clean editor never arms
// the guard and pops through the plain App precedence.
func TestEditorEscGatesDirtyForm(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	if scr.dirty() {
		t.Fatal("precondition: fresh form must be clean")
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x')}})
	if !scr.dirty() {
		t.Fatal("precondition: typing must dirty the form")
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	if app.Depth() != 2 || app.Modal() == nil {
		t.Fatalf("Esc on a dirty form must hold the editor open; depth = %d, modal = %v", app.Depth(), app.Modal())
	}
	if !strings.Contains(out, presetsEditorDiscardTitle) {
		t.Fatalf("Esc on a dirty form did not open the discard gate; frames:\n%s", out)
	}

	// n keeps the edit and re-arms the guard.
	app2, _ := newEditorApp("")
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x'), KeyEsc, KeyNo}})
	if app2.Depth() != 2 || app2.Modal() == nil {
		t.Fatalf("n must keep the dirty editor open; depth = %d, modal = %v", app2.Depth(), app2.Modal())
	}

	// y discards and pops.
	app3, _ := newEditorApp("")
	app3.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x'), KeyEsc, KeyYes}})
	if app3.Depth() != 1 {
		t.Fatalf("y should pop the editor; depth = %d", app3.Depth())
	}

	// Clean form: Esc pops without a gate.
	app4, _ := newEditorApp("")
	app4.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	if app4.Depth() != 1 || app4.Modal() != nil {
		t.Fatalf("clean Esc should pop silently; depth %d modal %v", app4.Depth(), app4.Modal())
	}
}

// TestEditorOverwriteConfirm pins the overwrite flow: saving a new preset
// under an existing name opens a plain confirm; n writes nothing, y writes.
func TestEditorOverwriteConfirm(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Task: "review"},
	}})

	// n branch: nothing changes on disk.
	app, _ := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "security"),
		k1(KeyCtrlS, KeyNo),
	)})
	pf := reloadEditorPresets(t, path)
	if len(pf.Presets) != 1 || pf.Presets[0].Task != "review" {
		t.Fatalf("overwrite-n mutated the file: %+v", pf.Presets)
	}
	if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
		t.Fatalf("cancel message = %q", app.Message())
	}

	// y branch: the record is replaced.
	app2, _ := newEditorApp("")
	app2.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "security"),
		k1(KeyCtrlS, KeyYes),
	)})
	pf = reloadEditorPresets(t, path)
	if len(pf.Presets) != 1 || pf.Presets[0].Task != "security" {
		t.Fatalf("overwrite-y did not replace: %+v", pf.Presets)
	}
}

// reloadEditorPresets reads the isolated presets file.
func reloadEditorPresets(t *testing.T, path string) *fanout.PresetsFile {
	t.Helper()
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil {
		t.Fatalf("reload presets: %v", err)
	}
	return pf
}

// TestEditorSavesExistingInPlaceWithoutConfirm pins the update path: saving
// an opened preset under its own name is not an overwrite and needs no gate.
func TestEditorSavesExistingInPlaceWithoutConfirm(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Task: "review", N: 4, AllowHosts: []string{"a.example.com"}},
	}})
	app, scr := newEditorApp("p1")
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{"p1", "review", "a.example.com"} {
		if !strings.Contains(out, want) {
			t.Fatalf("seeded editor frame missing %q; frames:\n%s", want, out)
		}
	}
	if scr.zone != editorZoneA || scr.formA.Cursor() != 0 {
		t.Fatalf("editor should open on the name field")
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("in-place save message = %q", app.Message())
	}
	pf := reloadEditorPresets(t, path)
	if len(pf.Presets) != 1 || pf.Presets[0].Task != "review" || pf.Presets[0].N != 4 {
		t.Fatalf("in-place save mutated the record: %+v", pf.Presets)
	}
	if len(pf.Presets[0].AllowHosts) != 1 || pf.Presets[0].AllowHosts[0] != "a.example.com" {
		t.Fatalf("in-place save dropped hosts: %+v", pf.Presets[0].AllowHosts)
	}
}

// TestEditorRenameDropsOldName pins the rename path: the old name disappears
// from the file and the record moves to the new name.
func TestEditorRenameDropsOldName(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Task: "review"},
		{Name: "keep", Task: "security"},
	}})
	app, _ := newEditorApp("p1")
	// Name field holds "p1": clear it and type the new name, then save.
	keys := append([]Key{KeyBackspace, KeyBackspace}, typePresetRunes("p12")...)
	keys = append(keys, KeyCtrlS)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	pf := reloadEditorPresets(t, path)
	if len(pf.Presets) != 2 {
		t.Fatalf("rename changed the count: %+v", pf.Presets)
	}
	if _, ok := pf.FindPreset("p1"); ok {
		t.Fatalf("old name survived the rename: %+v", pf.Presets)
	}
	renamed, ok := pf.FindPreset("p12")
	if !ok || renamed.Task != "review" {
		t.Fatalf("renamed record wrong: %+v", pf.Presets)
	}
	if _, ok := pf.FindPreset("keep"); !ok {
		t.Fatalf("sibling preset lost: %+v", pf.Presets)
	}
}

// TestEditorBuiltinReadonly pins the built-in row view: engine defaults
// render with the engine rung, edits are ignored, and save writes nothing.
func TestEditorBuiltinReadonly(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp(builtinPresetSentinel)
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x'), KeyCtrlS}})
	for _, want := range []string{builtinPresetRowLabel, "review", "engine default"} {
		if !strings.Contains(out, want) {
			t.Fatalf("builtin view missing %q; frames:\n%s", want, out)
		}
	}
	if !strings.Contains(app.Message(), "Built-in defaults are read-only.") {
		t.Fatalf("readonly message = %q", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("builtin view wrote the preset file: %v", err)
	}
	// Right closes the read-only view.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyRight}})
	if app.Depth() != 1 {
		t.Fatalf("builtin Right should pop; depth = %d", app.Depth())
	}
}

// TestEditorHostsAddRemove pins the allow_hosts sub-list: inline add
// (including a digit typed as text on the Add row), sticky arming via a host
// row, and removal through the Remove row.
func TestEditorHostsAddRemove(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""), // focus -> host zone (Add row)
		typePresetRunes("example.com"), k1(KeyEnter),
		// The commit moved the cursor onto the committed host row; step to
		// the Add row. A digit is text here (EDIT scope): host "h2".
		k1(KeyDown),
		k1(RuneKey('h'), Digit(2), KeyEnter),
		// Arm the first host (Up, then Enter), then walk to the Remove row.
		k1(KeyUp, KeyEnter),
		k1(KeyDown, KeyDown, KeyDown),
		k1(KeyEnter), // Remove row drops the armed host
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if len(scr.hostVals) != 1 || scr.hostVals[0] != "h2" {
		t.Fatalf("hostVals = %v, want [h2]", scr.hostVals)
	}
}

// TestEditorZoneTabCycle pins the three-zone focus walk: Tab on the last
// zone-A field enters the host zone, Tab on the last host row enters zone B,
// and Tab on the last zone-B field wraps to zone A.
func TestEditorZoneTabCycle(t *testing.T) {
	editorHome(t)
	_, scr := newEditorApp("")
	if scr.zone != editorZoneA || scr.formA.Cursor() != 0 {
		t.Fatalf("fresh editor zone = %d cursor %d, want zone A field 0", scr.zone, scr.formA.Cursor())
	}
	// Tab through the 7 zone-A fields; the last Tab crosses into the host zone.
	for i := 0; i < 7; i++ {
		scr.HandleKey(KeyTab, nil)
	}
	if scr.zone != editorZoneHosts {
		t.Fatalf("after 7 Tabs zone = %d, want host zone", scr.zone)
	}
	// Host rows: [add, remove]. Tab on the last row crosses into zone B.
	scr.HandleKey(KeyTab, nil)
	if scr.zone != editorZoneHosts {
		t.Fatalf("Tab on the Add row must stay in the host zone; zone = %d", scr.zone)
	}
	scr.HandleKey(KeyTab, nil)
	if scr.zone != editorZoneB || scr.formB.Cursor() != 0 {
		t.Fatalf("crossing Tab: zone = %d cursor %d, want zone B field 0", scr.zone, scr.formB.Cursor())
	}
	// Tab through the 7 zone-B fields; the last Tab wraps to zone A.
	for i := 0; i < 7; i++ {
		scr.HandleKey(KeyTab, nil)
	}
	if scr.zone != editorZoneA || scr.formA.Cursor() != 0 {
		t.Fatalf("wrap Tab: zone = %d cursor %d, want zone A field 0", scr.zone, scr.formA.Cursor())
	}
}

// TestEditorRenameOntoExistingReplacesWithoutDuplicate pins the F-06 fix:
// renaming a preset onto an existing name must end with exactly one row for
// that name on disk and in the rendered list, behind the overwrite confirm.
func TestEditorRenameOntoExistingReplacesWithoutDuplicate(t *testing.T) {
	seed := func(t *testing.T) string {
		t.Helper()
		path := editorHome(t)
		seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
			{Name: "a", Task: "review"},
			{Name: "b", Task: "security"},
		}})
		return path
	}

	// y branch: the target is replaced by the renamed record, no duplicate.
	path := seed(t)
	app, _ := newEditorApp("a")
	keys := append([]Key{KeyBackspace}, typePresetRunes("b")...)
	keys = append(keys, KeyCtrlS, KeyYes)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	pf := reloadEditorPresets(t, path)
	if len(pf.Presets) != 1 {
		t.Fatalf("rename-y must collapse onto one row, got %+v", pf.Presets)
	}
	if _, ok := pf.FindPreset("a"); ok {
		t.Fatalf("old name survived the rename: %+v", pf.Presets)
	}
	replaced, ok := pf.FindPreset("b")
	if !ok || replaced.Task != "review" {
		t.Fatalf("renamed record must replace the target: %+v", pf.Presets)
	}
	// The rendered list shows exactly one row per name.
	scr := NewPresetsScreen(editorStore(t, path), nil).(*presetsScreen)
	rowsB := 0
	for _, r := range scr.rows {
		if r.ID == presetRowPrefix+"b" {
			rowsB++
		}
	}
	if rowsB != 1 {
		t.Fatalf("rendered list has %d rows for b, want 1: %+v", rowsB, scr.rows)
	}

	// n branch: nothing changes on disk.
	path = seed(t)
	app2, _ := newEditorApp("a")
	keys = append([]Key{KeyBackspace}, typePresetRunes("b")...)
	keys = append(keys, KeyCtrlS, KeyNo)
	app2.CaptureRun(&ScriptedKeys{Keys: keys})
	pf = reloadEditorPresets(t, path)
	if len(pf.Presets) != 2 {
		t.Fatalf("rename-n must leave the file untouched, got %+v", pf.Presets)
	}
	if p, ok := pf.FindPreset("b"); !ok || p.Task != "security" {
		t.Fatalf("rename-n mutated the target: %+v", pf.Presets)
	}
}

// TestEditorCorruptFileWarnsAndGatesFirstSave pins the security F5 fix on
// this screen: the corrupt-file warning is user-visible (frame body and
// message line, naming the on-disk path) and the first save is refused until
// the user explicitly acknowledges overwriting the corrupt file.
func TestEditorCorruptFileWarnsAndGatesFirstSave(t *testing.T) {
	path := editorHome(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt preset file: %v", err)
	}
	app, scr := newEditorApp("")
	out := app.CaptureRun(&ScriptedKeys{})
	if !scr.corruptWarn {
		t.Fatal("editor must record the corrupt-load warning")
	}
	if !strings.Contains(out, "is corrupt") || !strings.Contains(out, "fanout-presets.json") {
		t.Fatalf("corrupt warning not user-visible; frames:\n%s", out)
	}

	// First save: the gate opens, n refuses and leaves the file byte-intact.
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown), // host zone -> zone B
		k1(KeyCtrlS),
	)
	out = app.CaptureRun(&ScriptedKeys{Keys: keys})
	if app.Modal() == nil {
		t.Fatalf("first save after corrupt load must open the gate; frames:\n%s", out)
	}
	if !strings.Contains(out, presetsCorruptTitle) {
		t.Fatalf("corrupt gate title missing; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if data, err := os.ReadFile(path); err != nil || string(data) != "{not json" {
		t.Fatalf("refused save mutated the corrupt file: %q, %v", data, err)
	}
	if !strings.Contains(app.Message(), "Cancelled. Nothing was written.") {
		t.Fatalf("gate cancel message = %q", app.Message())
	}

	// Acknowledged save overwrites with the new preset. (The episode latch
	// closes on the successful write, so corruptAcked resets with the save
	// itself; the acknowledgment is proven by the write landing at all.)
	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS, KeyYes}})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("post-ack save message = %q; frames:\n%s", app.Message(), out)
	}
	pf := reloadEditorPresets(t, path)
	if len(pf.Presets) != 1 || pf.Presets[0].Name != "p1" {
		t.Fatalf("acknowledged save did not land: %+v", pf.Presets)
	}
}

// TestEditorSearchKeyEnvEchoMaskedInFrame pins the SH-4 fix: a
// credential-shaped in-progress search_key_env value never renders raw in a
// frame (MaskOperand echo), while the form keeps the editable raw value and
// the save-time validation still rejects it.
func TestEditorSearchKeyEnvEchoMaskedInFrame(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	secret := "sk-livekey1234567890abcd"
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown, KeyEnter), // -> zone B, focus on search_key_env
		typePresetRunes(secret),
	)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if strings.Contains(out, secret) {
		t.Fatalf("frame echoed the raw search_key_env value; frames:\n%s", out)
	}
	if want := providers.MaskSecret(secret); !strings.Contains(out, want) {
		t.Fatalf("frame missing the masked echo %q; frames:\n%s", want, out)
	}
	if got := scr.formB.Value(1); got != secret {
		t.Fatalf("form must keep the raw editable value, got %q", got)
	}

	// Save-time validation still rejects the value and never echoes it.
	out = app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "environment variable NAME") {
		t.Fatalf("rejection message = %q; frames:\n%s", app.Message(), out)
	}
	if strings.Contains(app.Message(), secret) || strings.Contains(out, secret) {
		t.Fatal("the rejection path echoed the rejected operand")
	}
}

// TestEditorValidationBlocksSave pins field-level validation at save time:
// a non-number n and an unknown tools token each block the write.
func TestEditorValidationBlocksSave(t *testing.T) {
	// Run A: n = "lots" is not a number. Tab (not Enter) walks past the
	// field so the invalid text stays in the form for collect() to reject.
	path := editorHome(t)
	app, _ := newEditorApp("")
	keys := joinPresetKeys(
		typePresetRunes("p1"), k1(KeyEnter),
		k1(KeyEnter), // task (empty)
		typePresetRunes("lots"), k1(KeyTab),
		k1(KeyTab, KeyTab, KeyTab), // timeout, tools, max_turns -> max_native is last
		k1(KeyTab),                 // last zone-A field -> host zone
		k1(KeyDown, KeyDown),       // -> zone B
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "n must be a number or empty.") {
		t.Fatalf("number rejection message = %q", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid form wrote the preset file: %v", err)
	}

	// Run B: tools carries an unknown token.
	path = editorHome(t)
	app2, _ := newEditorApp("")
	keys = joinPresetKeys(
		typePresetRunes("p1"), k1(KeyEnter),
		k1(KeyEnter), // task
		k1(KeyEnter), // n
		k1(KeyEnter), // timeout
		typePresetRunes("read,debug"), k1(KeyTab),
		k1(KeyTab, KeyTab),   // max_turns, max_native (last) -> host zone
		k1(KeyDown, KeyDown), // -> zone B
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)
	app2.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app2.Message(), "tools must be a comma list") {
		t.Fatalf("tools rejection message = %q", app2.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid tools wrote the preset file: %v", err)
	}
}

// TestEditorHostAddCapsPendingInput pins the third uncapped text path fix:
// the allow_hosts Add-row inline buffer caps at maxLineRunes on the rune
// insertion path, with the same limit message the form uses; committing
// still lands the capped value.
func TestEditorHostAddCapsPendingInput(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""), // focus -> host zone (Add row)
		typePresetRunes(strings.Repeat("a", maxLineRunes+10)),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if got := len([]rune(scr.pending)); got != maxLineRunes {
		t.Fatalf("pending host input = %d runes, want the %d cap", got, maxLineRunes)
	}
	if !strings.Contains(app.Message(), "4096") {
		t.Fatalf("limit message = %q, want the 4096 notice", app.Message())
	}

	// Committing lands the capped value as a normal host entry.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
	if len(scr.hostVals) != 1 || len([]rune(scr.hostVals[0])) != maxLineRunes {
		t.Fatalf("committed host = %d entries, want one %d-rune host", len(scr.hostVals), maxLineRunes)
	}
}

// TestEditorSeededHostsStayUncapped pins the seeding half of the cap rule:
// an existing on-disk host longer than maxLineRunes loads in full (the cap
// bounds typing only, mirroring the editor/form seeding contract).
func TestEditorSeededHostsStayUncapped(t *testing.T) {
	path := editorHome(t)
	long := strings.Repeat("h", maxLineRunes+10) + ".example.com"
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", AllowHosts: []string{long}},
	}})
	_, scr := newEditorApp("p1")
	if len(scr.hostVals) != 1 || scr.hostVals[0] != long {
		t.Fatalf("seeded host was capped: %d runes", len([]rune(scr.hostVals[0])))
	}
}

// TestEditorCredentialShapedFieldRefused pins the round-6 save-time
// credential guard in both directions: a credential-shaped free-text value
// (task) is refused at collect time with a masked refusal (the value is
// never echoed) and nothing is written; a legitimate task value saves
// normally.
func TestEditorCredentialShapedFieldRefused(t *testing.T) {
	// Refusal: the credential-shaped task never reaches the file.
	path := editorHome(t)
	app, _ := newEditorApp("")
	const planted = "AKIAIOSFODNN7EXAMPLE"
	keys := joinPresetKeys(
		zoneAKeys("p1", planted, "", "", "", "", ""),
		k1(KeyDown, KeyDown), // host zone -> zone B
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "cannot be saved") {
		t.Fatalf("refusal message = %q; frames:\n%s", app.Message(), out)
	}
	if strings.Contains(app.Message(), planted) {
		t.Fatalf("the refusal echoed the rejected operand: %q", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused save wrote the preset file: %v", err)
	}

	// Legitimate: an ordinary task value saves verbatim.
	path = editorHome(t)
	app2, _ := newEditorApp("")
	keys = joinPresetKeys(
		zoneAKeys("p1", "security", "", "", "", "", ""),
		k1(KeyDown, KeyDown),
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)
	app2.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app2.Message(), "Preset p1 saved.") {
		t.Fatalf("legitimate save message = %q", app2.Message())
	}
	pf := reloadEditorPresets(t, path)
	p, ok := pf.FindPreset("p1")
	if !ok || p.Task != "security" {
		t.Fatalf("legitimate task did not persist: %+v", pf.Presets)
	}
}

// TestEditorCredentialShapedFreeTextSlotsRefused pins the guard on the zone-B
// free-text slots the reviewer named (models, seats, out_dir, root,
// search_endpoint): each credential-shaped value is refused with nothing
// written.
func TestEditorCredentialShapedFreeTextSlotsRefused(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"models", "Ab3dEf7Gh9jK1mNpQr5t"},
		{"seats", "sk-abcdefghijklmnop"},
		{"out_dir", "ghp_abcdefghijklmnop"},
		{"root", "xoxb-abcdefghijklmnop"},
		{"search_endpoint", "gsk_abcdefghijklmnop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := editorHome(t)
			app, _ := newEditorApp("")
			zoneB := []string{"", "", "", "", "", ""}
			switch tc.name {
			case "search_endpoint":
				zoneB[0] = tc.val
			case "out_dir":
				zoneB[2] = tc.val
			case "root":
				zoneB[3] = tc.val
			case "models":
				zoneB[4] = tc.val
			case "seats":
				zoneB[5] = tc.val
			}
			keys := joinPresetKeys(
				zoneAKeys("p1", "", "", "", "", "", ""),
				k1(KeyDown, KeyDown),
				zoneBKeys(zoneB...),
				k1(KeyCtrlS),
			)
			out := app.CaptureRun(&ScriptedKeys{Keys: keys})
			if !strings.Contains(app.Message(), "cannot be saved") {
				t.Fatalf("refusal message = %q; frames:\n%s", app.Message(), out)
			}
			if strings.Contains(app.Message(), tc.val) {
				t.Fatalf("the refusal echoed the rejected operand: %q", app.Message())
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("refused save wrote the preset file: %v", err)
			}
		})
	}
}

// TestEditorCorruptLatchRearmsOnNewEpisode pins the round-6 latch fix on
// the editor: after an acknowledged save turns the file healthy, a second
// external corrupt event re-arms the save gate (one acknowledgment covers
// one episode, never the whole session).
func TestEditorCorruptLatchRearmsOnNewEpisode(t *testing.T) {
	path := editorHome(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt preset file: %v", err)
	}
	app, scr := newEditorApp("")
	if !scr.corruptWarn {
		t.Fatal("first corrupt load must arm the latch")
	}
	saveKeys := func() []Key {
		return joinPresetKeys(
			zoneAKeys("p1", "", "", "", "", "", ""),
			k1(KeyDown, KeyDown), // host zone -> zone B
			zoneBKeys("", "", "", "", "", ""),
			k1(KeyCtrlS, KeyYes),
		)
	}

	// Acknowledged save lands and turns the file healthy.
	app.CaptureRun(&ScriptedKeys{Keys: saveKeys()})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("acknowledged save message = %q", app.Message())
	}
	if scr.corruptWarn {
		t.Fatal("healthy save must close the corrupt episode")
	}

	// Second external corrupt event: the save re-gates. The form still holds
	// the saved values, so Ctrl+S alone re-runs the save flow.
	if err := os.WriteFile(path, []byte("{still not json"), 0o600); err != nil {
		t.Fatalf("re-corrupt preset file: %v", err)
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if app.Modal() == nil {
		t.Fatalf("second corrupt episode must re-gate the save; frames:\n%s", out)
	}
	if !strings.Contains(out, presetsCorruptTitle) {
		t.Fatalf("re-gated save missing the corrupt confirm; frames:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
	if data, err := os.ReadFile(path); err != nil || string(data) != "{still not json" {
		t.Fatalf("declined re-gate mutated the file: %q, %v", data, err)
	}
}
