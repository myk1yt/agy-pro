package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// presetEditorSkLiveValue is a synthetic key VALUE for the search_key_env
// boundary pins (a listed credential head plus an opaque run). The head is
// built by concatenation so the tracked source carries no contiguous
// provider-key spelling; the runtime value is byte-identical to the literal it
// replaced.
const presetEditorSkLiveValue = "sk-" + "livekey1234567890abcd"

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

// zoneAKeys walks the zone-A fields (name..max_native): one Tab per field,
// landing the focus in the host zone. Tab (not Enter) is the walker because
// Enter on the task field opens the known-task picker (D13); collect()
// validates every field at save time regardless of how the focus moved.
func zoneAKeys(fields ...string) []Key {
	var ks []Key
	for _, f := range fields {
		ks = append(ks, typePresetRunes(f)...)
		ks = append(ks, KeyTab)
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
	secret := presetEditorSkLiveValue
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
// buildRunConfig ladder: defaults block vs engine default, the role-qualified
// preset rung (D11: default key / env-named / plain saved), and the flag rung
// input.
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

	// Every selection role rides the preset rung: the same on-disk row pins
	// the same fields, and only the label qualifier changes. This is the D11
	// fix pin: the bare "preset" rung (rendered "active preset") is gone.
	fixture := &fanout.Preset{Name: "p", Task: "security", N: 4, SearchKeyEnv: "MY_KEY", Root: "/repo"}
	for _, role := range []string{presetRowDefault, presetRowNamed, presetRowSaved} {
		withPreset := resolvePresetSources(presetLadderInputs{
			set:     map[string]bool{},
			preset:  fixture,
			def:     def,
			rowRole: role,
		})
		wantRung := "preset_" + role
		for field, want := range map[string]string{
			"n":              wantRung,
			"task":           wantRung,
			"search_key_env": wantRung,
			"root":           wantRung,
			"tools":          "engine", // preset pins nothing and the fixture block has no tools
			"max_native":     "state",
		} {
			if withPreset[field] != want {
				t.Fatalf("role %s rung[%s] = %q, want %q (all: %v)", role, field, withPreset[field], want, withPreset)
			}
		}
	}

	// An empty role falls back to the plain saved qualifier (defensive: the
	// screen always passes an explicit role today).
	defaulted := resolvePresetSources(presetLadderInputs{
		set:    map[string]bool{},
		preset: &fanout.Preset{Name: "p", Task: "security"},
		def:    def,
	})
	if defaulted["task"] != "preset_saved" {
		t.Fatalf("empty role should fall back to preset_saved, got %q", defaulted["task"])
	}

	flagged := resolvePresetSources(presetLadderInputs{
		set:     map[string]bool{"n": true, "task": true},
		preset:  &fanout.Preset{Name: "p", N: 4, Task: "security"},
		def:     def,
		rowRole: presetRowDefault,
	})
	if flagged["n"] != "flag" || flagged["task"] != "flag" {
		t.Fatalf("flag rung not honored: %v", flagged)
	}
}

// TestEditorSourceColumnRendersPresetRole pins the rendered D11 labels: the
// same saved row reads "saved preset" by default, "named preset" when
// AGY_PRO_FANOUT_PRESET names it, "default preset" when the store's
// default_preset key names it, and env naming beats the default key. The lie
// this replaces rendered "active preset" for every one of these states.
func TestEditorSourceColumnRendersPresetRole(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{
		DefaultPreset: "p1",
		Presets: []fanout.Preset{
			{Name: "p1", Task: "review", N: 4},
			{Name: "p2", Task: "security", N: 4},
		},
	})

	// Saved row, no selection of any kind: "saved preset", never "active preset".
	app, _ := newEditorApp("p2")
	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, "saved preset") {
		t.Fatalf("plain saved row missing 'saved preset'; frames:\n%s", out)
	}
	if strings.Contains(out, "active preset") {
		t.Fatalf("the lying 'active preset' label is back; frames:\n%s", out)
	}

	// Env-named row beats the default key: "named preset".
	t.Setenv(fanout.EnvFanoutPreset, "p2")
	app2, _ := newEditorApp("p2")
	out2 := app2.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out2, "named preset") {
		t.Fatalf("env-named row missing 'named preset'; frames:\n%s", out2)
	}
	if strings.Contains(out2, "saved preset") || strings.Contains(out2, "default preset") {
		t.Fatalf("env-named row carries a stale role label; frames:\n%s", out2)
	}

	// The default-key row with the env naming a DIFFERENT preset: "default preset".
	t.Setenv(fanout.EnvFanoutPreset, "p2")
	app3, _ := newEditorApp("p1")
	out3 := app3.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out3, "default preset") {
		t.Fatalf("default-key row missing 'default preset'; frames:\n%s", out3)
	}
	if strings.Contains(out3, "named preset") {
		t.Fatalf("default-key row mislabeled as env-named; frames:\n%s", out3)
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

// TestEditorBuiltinReadonly pins the built-in row view: effective engine
// defaults render with the built-in marker (D12: max_native shows the
// state.json-resolved value, out_dir shows the reports/fanout/<ts> pattern
// instead of a blank), edits are ignored, and save writes nothing.
func TestEditorBuiltinReadonly(t *testing.T) {
	path := editorHome(t)
	app, scr := newEditorApp(builtinPresetSentinel)
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{RuneKey('x'), KeyCtrlS}})
	for _, want := range []string{builtinPresetRowLabel, "review", "built-in",
		"reports/fanout/<ts>", "2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("builtin view missing %q; frames:\n%s", want, out)
		}
	}
	// D12 pins: no field renders a blank where the engine supplies a value.
	if scr.formA.Value(6) != "2" {
		t.Fatalf("builtin max_native = %q, want the effective 2", scr.formA.Value(6))
	}
	if scr.formB.Value(2) != "reports/fanout/<ts>" {
		t.Fatalf("builtin out_dir = %q, want the reports/fanout/<ts> pattern", scr.formB.Value(2))
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

// TestEditorModelsSeatsConflictRefused pins defect D9 on the save path: a
// preset pinning both models and seats (the combination the fanout ladder
// hard-errors on at run time) is refused at save time with a message naming
// both fields, nothing is written, the editor stays open, and the offending
// rows keep their values so the user can clear one side. The single-pin
// halves save normally.
func TestEditorModelsSeatsConflictRefused(t *testing.T) {
	keys := func(models, seats string) []Key {
		return joinPresetKeys(
			zoneAKeys("p1", "", "", "", "", "", ""),
			k1(KeyDown, KeyDown), // host zone -> zone B
			zoneBKeys("", "", "", "", models, seats),
			k1(KeyCtrlS),
		)
	}

	// Refusal: both pins set.
	path := editorHome(t)
	app, scr := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: keys("prov/m1", "native:gemini-x")})
	if !strings.Contains(app.Message(), "models and seats cannot be combined") {
		t.Fatalf("refusal message = %q; want the named seat-source conflict", app.Message())
	}
	if strings.Contains(app.Message(), "prov/m1") || strings.Contains(app.Message(), "gemini-x") {
		t.Fatalf("the refusal echoed the user operands: %q", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused save wrote the preset file: %v", err)
	}
	if app.Depth() != 2 {
		t.Fatalf("editor must stay open after a refused save; depth = %d", app.Depth())
	}
	if got := scr.formB.Value(4); got != "prov/m1" {
		t.Fatalf("models row must keep its value for fixing; got %q", got)
	}
	if got := scr.formB.Value(5); got != "native:gemini-x" {
		t.Fatalf("seats row must keep its value for fixing; got %q", got)
	}

	// Positive halves: either pin alone saves.
	path = editorHome(t)
	app2, _ := newEditorApp("")
	app2.CaptureRun(&ScriptedKeys{Keys: keys("prov/m1", "")})
	if !strings.Contains(app2.Message(), "Preset p1 saved.") {
		t.Fatalf("models-only save message = %q", app2.Message())
	}
	pf := reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Models != "prov/m1" || p.Seats != "" {
		t.Fatalf("models-only preset wrong on disk: %+v", pf.Presets)
	}

	path = editorHome(t)
	app3, _ := newEditorApp("")
	app3.CaptureRun(&ScriptedKeys{Keys: keys("", "native:gemini-x")})
	if !strings.Contains(app3.Message(), "Preset p1 saved.") {
		t.Fatalf("seats-only save message = %q", app3.Message())
	}
	pf = reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Models != "" || p.Seats != "native:gemini-x" {
		t.Fatalf("seats-only preset wrong on disk: %+v", pf.Presets)
	}
}

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
	secret := presetEditorSkLiveValue
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
		k1(KeyTab), // task (empty); Enter here would open the task picker
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
		k1(KeyTab),   // task; Enter would open the task picker
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
	const planted = testfixtures.AKIAExample
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
		{"out_dir", testfixtures.GHPSixteen},
		{"root", testfixtures.SlackBotPlain},
		{"search_endpoint", testfixtures.GroqKeyMid},
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

// editorFrameAt80x24 opens the named preset in an 80x24 editor over a stub
// root and returns the app, the concrete screen, and the composed frame
// lines.
func editorFrameAt80x24(t *testing.T, name string) (*App, *presetEditorScreen, []string) {
	t.Helper()
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPresetEditorScreen(name)
	app.Push(scr)
	return app, scr.(*presetEditorScreen), app.Frame().Lines
}

// frameLineMatches reports whether any frame line satisfies pred.
func frameLineMatches(lines []string, pred func(string) bool) bool {
	for _, l := range lines {
		if pred(l) {
			return true
		}
	}
	return false
}

// TestEditorHostsWindowFits80x24 pins audit F10: the allow_hosts window is
// derived from the frame height instead of the retired fixed 7-row window, so
// the hint and the last zone-B field stay on an 80x24 frame however many hosts
// the preset carries. The 0-host and 1-host cases keep the audit's recorded
// line counts (23 and 24) and render their whole row set, so the small-host
// layout did not move.
func TestEditorHostsWindowFits80x24(t *testing.T) {
	all := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com",
		"e.example.com", "f.example.com", "g.example.com", "h.example.com"}
	seed := func(t *testing.T, n int) {
		t.Helper()
		path := editorHome(t)
		seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
			{Name: "p", AllowHosts: append([]string(nil), all[:n]...)},
		}})
	}
	jsonOnFrame := func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "json") }

	cases := []struct {
		name     string
		hosts    int
		wantLine int
		whole    []string // rows of the host block that must all be on the frame
	}{
		{name: "0 hosts", hosts: 0, wantLine: 23, whole: []string{"[ 1] Add host", "[ 2] Remove host"}},
		{name: "1 host", hosts: 1, wantLine: 24, whole: []string{"[ 1] a.example.com", "[ 2] Add host", "[ 3] Remove host"}},
		{name: "8 hosts", hosts: 8, wantLine: 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, tc.hosts)
			_, scr, lines := editorFrameAt80x24(t, "p")
			// 24 rows of frame minus the chrome this body shape spends
			// (title, both separators, base footer, both 7-field grids, the
			// allow_hosts header, the blank and the hint) leaves 3 rows.
			if got := scr.hosts.PageSize(); got != 3 {
				t.Fatalf("allow_hosts page size = %d, want 3", got)
			}
			if len(lines) != tc.wantLine {
				t.Fatalf("frame = %d lines, want %d:\n%s", len(lines), tc.wantLine, strings.Join(lines, "\n"))
			}
			if lines[len(lines)-1] != baseFooter {
				t.Fatalf("last frame line = %q, want the base footer", lines[len(lines)-1])
			}
			if !frameLineMatches(lines, func(l string) bool { return l == presetsEditorHint }) {
				t.Fatalf("editor hint missing from the frame:\n%s", strings.Join(lines, "\n"))
			}
			if !frameLineMatches(lines, jsonOnFrame) {
				t.Fatalf("the last zone-B field (json) is not on the frame:\n%s", strings.Join(lines, "\n"))
			}
			for _, row := range tc.whole {
				if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, row) }) {
					t.Fatalf("host row %q missing from the frame:\n%s", row, strings.Join(lines, "\n"))
				}
			}
		})
	}

	// The audit repro: 8 hosts, the host window pages, and the action rows
	// stay reachable through the window instead of falling off the frame.
	t.Run("8 hosts page to the actions", func(t *testing.T) {
		seed(t, 8)
		app, scr, _ := editorFrameAt80x24(t, "p")
		scr.hosts.SetCursor(scr.hosts.Len() - 1)
		lines := app.Frame().Lines
		if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[10] Remove host") }) {
			t.Fatalf("the Remove row is unreachable in the shrunk window:\n%s", strings.Join(lines, "\n"))
		}
		if !frameLineMatches(lines, func(l string) bool { return l == presetsEditorHint }) {
			t.Fatalf("hint lost after paging the host window:\n%s", strings.Join(lines, "\n"))
		}
		if len(lines) != 24 {
			t.Fatalf("paged frame = %d lines, want 24", len(lines))
		}
	})

	// The last zone-B field is not only visible but still the focused,
	// editable row: the cursor marker sits on it with the frame intact.
	t.Run("json stays focused and on-frame", func(t *testing.T) {
		seed(t, 8)
		app, scr, _ := editorFrameAt80x24(t, "p")
		scr.zone = editorZoneB
		scr.formB.SetCursor(scr.formB.Len() - 1)
		lines := app.Frame().Lines
		if !frameLineMatches(lines, func(l string) bool { return strings.HasPrefix(l, "▶ json") }) {
			t.Fatalf("focused json row missing from the frame:\n%s", strings.Join(lines, "\n"))
		}
		if len(lines) != 24 {
			t.Fatalf("focused frame = %d lines, want 24", len(lines))
		}
	})
}

// TestEditorCorruptEpisodeKeepsHint80x24 pins the one-off-row half of the
// same budget: the corrupt-file warning renders both as a body row and as the
// app message (its own frame section), and at 80x24 the frame still holds the
// hint and the last zone-B field. The decorative blank above the hint is the
// row that gives way when the budget cannot hold it.
func TestEditorCorruptEpisodeKeepsHint80x24(t *testing.T) {
	path := editorHome(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt preset file: %v", err)
	}
	_, scr, lines := editorFrameAt80x24(t, "")
	if !scr.corruptWarn {
		t.Fatal("corrupt file did not arm the warning")
	}
	if len(lines) != 24 {
		t.Fatalf("frame = %d lines, want 24:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return l == presetsEditorHint }) {
		t.Fatalf("hint missing under the corrupt warning:\n%s", strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "json") }) {
		t.Fatalf("json row missing under the corrupt warning:\n%s", strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != baseFooter {
		t.Fatalf("last frame line = %q, want the base footer", lines[len(lines)-1])
	}
}

// TestEditorHostAddRefusedInvalidInput pins D14 (P3-5) commit-time refusal:
// an entry the exact allow-list match can never satisfy (URL scheme,
// wildcard, path, internal space, whitespace-only) refuses with a named
// message, KEEPS the offending input on the Add row for editing, and adds
// nothing. Enter on a truly empty Add row stays a quiet no-op.
func TestEditorHostAddRefusedInvalidInput(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantRefuse bool
	}{
		{"empty_enter", "", false},
		{"url", "https://example.com", true},
		{"wildcard", "*.example.com", true},
		{"path", "example.com/path", true},
		{"space", "ex ample.com", true},
		{"blank", "   ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			editorHome(t)
			app, scr := newEditorApp("")
			keys := joinPresetKeys(
				zoneAKeys("p1", "", "", "", "", "", ""), // focus -> host zone (Add row)
				typePresetRunes(tc.input), k1(KeyEnter),
			)
			app.CaptureRun(&ScriptedKeys{Keys: keys})
			if tc.wantRefuse {
				if !strings.Contains(app.Message(), "not a valid host") {
					t.Fatalf("message = %q; want the invalid-host refusal", app.Message())
				}
				if tc.name != "blank" && strings.Contains(app.Message(), tc.input) {
					t.Fatalf("the refusal echoed the rejected operand: %q", app.Message())
				}
				if scr.pending != tc.input {
					t.Fatalf("pending = %q; want the offending input kept visible for editing", scr.pending)
				}
			} else {
				if strings.Contains(app.Message(), "not a valid host") {
					t.Fatalf("empty Enter must stay a quiet no-op; message = %q", app.Message())
				}
				if scr.pending != "" {
					t.Fatalf("pending = %q; want empty", scr.pending)
				}
			}
			if len(scr.hostVals) != 0 {
				t.Fatalf("hostVals = %v; want empty (nothing added)", scr.hostVals)
			}
		})
	}
}

// TestEditorHostAddDuplicateRefused pins the duplicate half of D14 (P3-5):
// committing an entry that normalizes to one already listed refuses with a
// named message and keeps the input, instead of clearing it silently.
func TestEditorHostAddDuplicateRefused(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		typePresetRunes("example.com"), k1(KeyEnter),
		k1(KeyDown),                                  // commit landed on the host row; back to the Add row
		typePresetRunes("EXAMPLE.com"), k1(KeyEnter), // same entry after normalization
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "already in the list") {
		t.Fatalf("message = %q; want the duplicate refusal", app.Message())
	}
	if len(scr.hostVals) != 1 || scr.hostVals[0] != "example.com" {
		t.Fatalf("hostVals = %v; want [example.com]", scr.hostVals)
	}
	if scr.pending != "EXAMPLE.com" {
		t.Fatalf("pending = %q; want the duplicate input kept visible", scr.pending)
	}
}

// TestEditorRemoveHostNothingArmedRefused pins D15 (P3-5): pressing Remove
// host with no host row armed names the missing arming step instead of
// silently no-oping, and drops nothing.
func TestEditorRemoveHostNothingArmedRefused(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		editorHome(t)
		app, scr := newEditorApp("")
		keys := joinPresetKeys(
			zoneAKeys("p1", "", "", "", "", "", ""),
			k1(KeyDown, KeyEnter), // Remove row (rows: Add, Remove), nothing armed
		)
		app.CaptureRun(&ScriptedKeys{Keys: keys})
		if !strings.Contains(app.Message(), "no host is armed") {
			t.Fatalf("message = %q; want the nothing-armed refusal", app.Message())
		}
		if len(scr.hostVals) != 0 {
			t.Fatalf("hostVals = %v; want empty", scr.hostVals)
		}
	})

	t.Run("seeded host unarmed", func(t *testing.T) {
		path := editorHome(t)
		seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
			{Name: "p1", AllowHosts: []string{"a.example.com"}},
		}})
		app, scr := newEditorApp("p1")
		keys := joinPresetKeys(
			zoneAKeys("", "", "", "", "", "", ""), // Enter-walk zone A into the host zone
			k1(KeyDown, KeyDown, KeyEnter),        // rows: host, Add, Remove; nothing armed
		)
		app.CaptureRun(&ScriptedKeys{Keys: keys})
		if !strings.Contains(app.Message(), "no host is armed") {
			t.Fatalf("message = %q; want the nothing-armed refusal", app.Message())
		}
		if len(scr.hostVals) != 1 || scr.hostVals[0] != "a.example.com" {
			t.Fatalf("hostVals = %v; want the seeded host untouched", scr.hostVals)
		}
	})
}

// TestEditorSeededInvalidHostRefusedAtSave pins the D14 (P3-5) save-time
// guard: a host row seeded from a hand-edited file that the exact allow-list
// match can never satisfy refuses the save like every other collect error
// (P3-2 behavior): the editor stays open and the offending row stays visible.
// (The seed is "*.example.com", not a URL shape: a seeded "https://..."
// value trips the earlier credential-shaped guard, which refuses too, just
// with the secret message.)
func TestEditorSeededInvalidHostRefusedAtSave(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Task: "review", AllowHosts: []string{"*.example.com"}},
	}})
	app, scr := newEditorApp("p1")
	keys := joinPresetKeys(
		zoneAKeys("", "", "", "", "", "", ""), // seeded fields stay; Enter-walk to the host zone
		k1(KeyDown, KeyDown),                  // host zone -> zone B
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "allow_hosts entries must be bare hostnames") {
		t.Fatalf("message = %q; want the allow_hosts save refusal", app.Message())
	}
	if strings.Contains(app.Message(), "*.example.com") {
		t.Fatalf("the refusal echoed the rejected operand: %q", app.Message())
	}
	if app.Depth() != 2 {
		t.Fatalf("editor should stay open after the refusal; depth = %d", app.Depth())
	}
	if len(scr.hostVals) != 1 || scr.hostVals[0] != "*.example.com" {
		t.Fatalf("hostVals = %v; want the offending row kept visible", scr.hostVals)
	}
}

// TestEditorHostAddValidRoundTrip pins the acceptance half: a valid host
// with stray case/space/port commits in the canonical normalized form and
// saves unchanged (normalization as before; validation accepts it).
func TestEditorHostAddValidRoundTrip(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		typePresetRunes(" Example.COM:8443 "), k1(KeyEnter),
		k1(KeyDown, KeyDown, KeyDown), // host row -> zone B
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("save message = %q; want saved", app.Message())
	}
	pf := reloadEditorPresets(t, path)
	if len(pf.Presets) != 1 || len(pf.Presets[0].AllowHosts) != 1 ||
		pf.Presets[0].AllowHosts[0] != "example.com" {
		t.Fatalf("on-disk hosts = %+v; want [example.com]", pf.Presets)
	}
}

// TestEditorTaskPickerListsEngineTaskNames pins the picker half of the D13
// fix: Enter on the task field opens the shared choice overlay, its rows are
// EXACTLY fanout.TaskNames() (derived here from tasks.go, never a copied
// list), picking a row writes the canonical name into the field, and the
// preset saves with it. Tab still advances past the task field untouched.
func TestEditorTaskPickerListsEngineTaskNames(t *testing.T) {
	path := editorHome(t)
	app, scr := newEditorApp("")
	// Type the name, Enter advances to the task field, Enter opens the picker.
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		typePresetRunes("p1"), k1(KeyEnter, KeyEnter))})
	ch, ok := app.Modal().(*Choice)
	if !ok {
		t.Fatalf("modal = %T, want the task picker choice overlay", app.Modal())
	}
	want := fanout.TaskNames()
	if ch.list.Len() != len(want) {
		t.Fatalf("picker rows = %d, want %d (len fanout.TaskNames())", ch.list.Len(), len(want))
	}
	for i, name := range want {
		if got := ch.list.Row(i).Label; got != name {
			t.Fatalf("picker row %d = %q, want %q", i, got, name)
		}
	}
	if title := ch.Title(); title != presetsEditorFieldTask {
		t.Fatalf("picker title = %q, want the reused task field label %q", title, presetsEditorFieldTask)
	}

	// Pick the third row by digit (1-based stepList activation), walk the
	// remaining fields, save, and pin the picked name on disk.
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		k1(Digit(3)),
		k1(KeyTab, KeyTab, KeyTab, KeyTab, KeyTab, KeyTab), // task -> last zone-A field -> host zone
		k1(KeyDown, KeyDown), // -> zone B
		zoneBKeys("", "", "", "", "", ""),
		k1(KeyCtrlS),
	)})
	if got := scr.formA.Value(1); got != want[2] {
		t.Fatalf("task field = %q, want the picked row %q", got, want[2])
	}
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("save message = %q; want saved", app.Message())
	}
	pf := reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Task != want[2] {
		t.Fatalf("picked task not persisted: %+v", pf.Presets)
	}
}

// TestEditorTaskPickerEscKeepsTypedValue pins the picker cancel arm: Esc
// closes the overlay without touching the task field, the cancel message
// renders, and the dirty guard re-arms (the typed value must survive the
// round trip through the modal).
func TestEditorTaskPickerEscKeepsTypedValue(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		typePresetRunes("p1"), k1(KeyEnter),
		typePresetRunes("security"), k1(KeyEnter), // open the picker over "security"
	)})
	if _, ok := app.Modal().(*Choice); !ok {
		t.Fatalf("modal = %T, want the task picker open over a typed value", app.Modal())
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	if got := scr.formA.Value(1); got != "security" {
		t.Fatalf("task field = %q, want the typed value kept after Esc", got)
	}
	if !strings.Contains(app.Message(), "Cancelled") {
		t.Fatalf("cancel message = %q", app.Message())
	}
	if app.Modal() == nil {
		t.Fatal("the dirty guard must re-arm after the picker closes")
	}
}

// TestEditorTaskTypoRefusedAtSave pins the refusal half of the D13 fix with
// the P3-2 shape: a typo'd task name is refused at save with a named message
// (known names only, operand never echoed), nothing is written, the editor
// stays open, and the offending value stays visible in the task field. The
// same field corrected through the picker saves, a typo seeded from a
// hand-edited file refuses the same way without rewriting the file, and a
// case-insensitive spelling of a known name saves verbatim.
func TestEditorTaskTypoRefusedAtSave(t *testing.T) {
	fillKeys := func(task string) []Key {
		return joinPresetKeys(
			zoneAKeys("p1", task, "", "", "", "", ""),
			k1(KeyDown, KeyDown),
			zoneBKeys("", "", "", "", "", ""),
			k1(KeyCtrlS),
		)
	}

	// Refusal: "secuirty" must not silently become review on disk.
	path := editorHome(t)
	app, scr := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: fillKeys("secuirty")})
	if !strings.Contains(app.Message(), "known task names") {
		t.Fatalf("refusal message = %q; want the known-task refusal", app.Message())
	}
	if strings.Contains(app.Message(), "secuirty") {
		t.Fatalf("the refusal echoed the rejected operand: %q", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused save wrote the preset file: %v", err)
	}
	if app.Depth() != 2 {
		t.Fatalf("editor must stay open after a refused save; depth = %d", app.Depth())
	}
	if got := scr.formA.Value(1); got != "secuirty" {
		t.Fatalf("task field must keep the offending value for fixing; got %q", got)
	}

	// Correction through the picker: focus back to the task field, open the
	// picker, pick row 1 (review), save.
	scr.zone = editorZoneA
	scr.formA.SetCursor(presetZoneAFieldTask)
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		k1(KeyEnter), // open the picker
		k1(Digit(1)),
		k1(KeyCtrlS),
	)})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("corrected save message = %q", app.Message())
	}
	pf := reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Task != "review" {
		t.Fatalf("corrected task not persisted: %+v", pf.Presets)
	}

	// Seeded typo (hand-edited file): the save refuses and the file is not
	// rewritten, so the row survives for correction.
	path = editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Task: "secuirty", N: 4},
	}})
	app2, _ := newEditorApp("p1")
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app2.Message(), "known task names") {
		t.Fatalf("seeded-typo refusal message = %q", app2.Message())
	}
	pf = reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Task != "secuirty" || p.N != 4 {
		t.Fatalf("refused save must not rewrite the seeded row: %+v", pf.Presets)
	}

	// Case-insensitive spelling of a known name saves verbatim (the engine
	// normalizes case at resolve time, so the preset round-trips as typed).
	path = editorHome(t)
	app3, _ := newEditorApp("")
	app3.CaptureRun(&ScriptedKeys{Keys: fillKeys("Security")})
	if !strings.Contains(app3.Message(), "Preset p1 saved.") {
		t.Fatalf("case-insensitive save message = %q", app3.Message())
	}
	pf = reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Task != "Security" {
		t.Fatalf("case-insensitive task not persisted verbatim: %+v", pf.Presets)
	}
}

// TestEditorSaveRuleMessagesCoverEveryValidatorRefusal pins FIX-CODE-C3: the
// editor's save gate IS fanout.ValidatePresetForSave, and this screen owns only
// the WORDING of a rule the validator decided. Every sentinel the validator can
// return maps to the editor's own named message, asserted here through a
// fragment that exists in the editor's catalog text only (never in the
// validator's engine text), so a rule can no longer be added to the shared
// validator and reach the user as raw engine prose, and an inline copy of a rule
// cannot come back without this table going stale.
func TestEditorSaveRuleMessagesCoverEveryValidatorRefusal(t *testing.T) {
	editorHome(t)
	_, scr := newEditorApp("")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"name", fanout.ErrPresetNameRequired, "Preset name must not be empty."},
		// Wrapped exactly the way ValidatePresetForSave returns it (the
		// provider's reason appended): errors.Is must still find the rule.
		{"key_env", fmt.Errorf("%w: apiKeyEnv is empty", fanout.ErrPresetSearchKeyEnv),
			"search_key_env must be an environment variable NAME, not a key value."},
		{"seat_conflict", fanout.ErrPresetSeatConflict, "Clear one of them."},
		{"unknown_task", fanout.ErrPresetUnknownTask, "silently run the review prompt"},
		{"allow_hosts", fanout.ErrPresetAllowHosts, "Remove the invalid host row"},
	}
	for _, tc := range cases {
		got := scr.saveRuleMessage(tc.err)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: editor message %q must carry %q (the editor's own wording)", tc.name, got, tc.want)
		}
	}
	// The name rule is the cleanest leak check: the engine text and the editor
	// text differ, so a fall-through to err.Error() is visible.
	if got := scr.saveRuleMessage(fanout.ErrPresetNameRequired); strings.Contains(got, "name is required") {
		t.Errorf("the name refusal leaked the validator's engine text: %q", got)
	}
}

// TestEditorNamelessSaveRefused pins the name rule as the shared validator's
// too: Ctrl+S on an empty form reaches fanout.ValidatePresetForSave (collect()
// carries no name check of its own any more), the refusal arrives as the
// editor's named message, nothing is written, and the editor stays open.
func TestEditorNamelessSaveRefused(t *testing.T) {
	path := editorHome(t)
	if err := fanout.ValidatePresetForSave(fanout.Preset{}); err == nil {
		t.Fatal("the shared save validator must refuse a nameless preset")
	}
	app, _ := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset name must not be empty.") {
		t.Fatalf("nameless save message = %q; want the editor's name refusal", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("nameless save wrote the preset file: %v", err)
	}
	if app.Depth() != 2 {
		t.Fatalf("editor must stay open after a refused save; depth = %d", app.Depth())
	}
}
