package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/inventory"
	"agypro/internal/mode"
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

// backspaceKeys returns n Backspace keys: the idiom for clearing a U8-seeded
// field (the new-preset editor pre-fills the engine defaults, so a test that
// types a full replacement value drops the seed runes first).
func backspaceKeys(n int) []Key {
	ks := make([]Key, n)
	for i := range ks {
		ks[i] = KeyBackspace
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

// zoneBKeys walks the zone-B fields (search_endpoint..json) by Enter. The last
// field gets no trailing Enter: Enter on json is the save gesture, and a walker
// that ended with it would save before the caller's own Ctrl+S (and before any
// later key).
func zoneBKeys(fields ...string) []Key {
	var ks []Key
	for i, f := range fields {
		if f != "" {
			ks = append(ks, typePresetRunes(f)...)
		}
		if i < len(fields)-1 {
			ks = append(ks, KeyEnter)
		}
	}
	return ks
}

// presetNewSeedLensA/B are the rune counts of the U8 new-preset pre-fill per
// grid field (task=review, n=3, timeout_seconds=300, tools=read,search,
// max_turns=500, root=.): typing appends at the caret, so a test that types a
// full replacement value clears the seed runes first.
var presetNewSeedLensA = []int{0, 6, 1, 3, 11, 3, 0}

// replaceZoneAKeys walks zone A like zoneAKeys, clearing each seeded field
// before typing the replacement value.
func replaceZoneAKeys(fields ...string) []Key {
	var ks []Key
	for i, f := range fields {
		if i < len(presetNewSeedLensA) {
			ks = append(ks, backspaceKeys(presetNewSeedLensA[i])...)
		}
		if f != "" {
			ks = append(ks, typePresetRunes(f)...)
		}
		ks = append(ks, KeyTab)
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
		replaceZoneAKeys("p1", "review", "4", "120", "read,search", "8", "2"),
		// Host zone (focus lands on the Add row): type + commit one host,
		// then Down past the last row into zone B.
		typePresetRunes("example.com"), k1(KeyEnter),
		k1(KeyDown, KeyDown, KeyDown),
		// Zone B: skip search_endpoint, search_key_env, out_dir, root,
		// models, seats; set json; save.
		zoneBKeys("", "", "", "", ""),
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
		replaceZoneAKeys("p1", "security"),
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
		replaceZoneAKeys("p1", "security"),
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

// TestEditorSeedEditorAbsentSeed pins the seed editor with NO seed record on
// disk: it opens as a real editor named fanout.BuiltinPresetName (notFound
// stays empty, the first save creates the record), the frame still shows the
// engine literals (task=review, n=3, timeout=300, tools=read,search,
// max_turns=500, the reports/fanout/<ts> out_dir pattern, root=., and the
// LIVE effective max_native, D12's no-blank-cell rule), the source column
// labels the unset fields "built-in" with the max_native state.json rung,
// and a save writes ONLY the fields the user actually set (contract A2: the
// untouched fields stay empty, so the ladder keeps src="engine").
func TestEditorSeedEditorAbsentSeed(t *testing.T) {
	path := editorHome(t)
	app, scr := newEditorApp(fanout.BuiltinPresetName)
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{fanout.BuiltinPresetName, "review", "built-in",
		"reports/fanout/<ts>", "state.json"} {
		if !strings.Contains(out, want) {
			t.Fatalf("seed editor missing %q; frames:\n%s", want, out)
		}
	}
	if scr.notFound != "" {
		t.Fatalf("absent seed must not render not-found; notFound = %q", scr.notFound)
	}
	if !scr.seedEdit() {
		t.Fatal("the reserved name must open the seed editor")
	}
	if got := scr.formA.Value(0); got != fanout.BuiltinPresetName {
		t.Fatalf("seed name field = %q, want the reserved name", got)
	}
	// The form values stay EMPTY (display-only engine literals, contract A2).
	if got := scr.formA.Value(1); got != "" {
		t.Fatalf("seed task form value = %q, want empty (display literal only)", got)
	}
	// D12 pins: no field renders a blank where the engine supplies a value.
	if got := seedEngineDisplayValue("max_native"); got != "1" {
		t.Fatalf("seed max_native preview = %q, want engine fallback 1", got)
	}
	if !strings.Contains(out, "max_native      : 1  state.json") {
		t.Fatalf("frame must show max_native fallback 1, not a blank cell:\n%s", out)
	}
	src := scr.sources()
	for _, k := range []string{"task", "n", "timeout_seconds", "tools", "max_turns", "out_dir", "root", "json"} {
		if src[k] != "builtin" {
			t.Fatalf("seed source[%s] = %q, want builtin (absent seed)", k, src[k])
		}
	}
	if src["max_native"] != "state" {
		t.Fatalf("seed source[max_native] = %q, want the state.json rung", src["max_native"])
	}
	if got := scr.dirty(); got {
		t.Fatal("a fresh seed editor must start clean")
	}

	// Pin one seat and save: the record is created with ONLY seats set.
	scr.appendSeat("native:gemini-seed-a")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset "+fanout.BuiltinPresetName+" saved.") {
		t.Fatalf("seed save message = %q", app.Message())
	}
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil {
		t.Fatalf("reload presets: %v", err)
	}
	if len(pf.Presets) != 1 {
		t.Fatalf("on-disk presets = %+v, want exactly the seed", pf.Presets)
	}
	seed := pf.Presets[0]
	if seed.Name != fanout.BuiltinPresetName || seed.Seats != "native:gemini-seed-a" {
		t.Fatalf("seed record = %+v, want the reserved name with the one seat", seed)
	}
	if seed.Task != "" || seed.N != 0 || seed.TimeoutSeconds != 0 || seed.Tools != "" ||
		seed.MaxTurns != 0 || seed.MaxNative != 0 || seed.OutDir != "" || seed.Root != "" ||
		seed.JSON || len(seed.AllowHosts) != 0 {
		t.Fatalf("untouched fields must stay empty (contract A2): %+v", seed)
	}
	// Right closes the editor (clean after the save).
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyRight}})
	if app.Depth() != 1 {
		t.Fatalf("seed Right should pop; depth = %d", app.Depth())
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

// TestEditorZoneTabCycle pins the four-zone focus walk: Tab on the last
// zone-A field enters the host zone, Tab on the last host row enters zone B,
// Tab on the last zone-B field crosses into the seats sub-list, and Tab on the
// last seat row wraps to zone A.
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
	// Tab through the 5 zone-B fields; the last Tab crosses into the seats zone.
	for i := 0; i < 5; i++ {
		scr.HandleKey(KeyTab, nil)
	}
	if scr.zone != editorZoneSeats {
		t.Fatalf("after the zone-B walk zone = %d, want the seats zone", scr.zone)
	}
	// The empty seat list is [add, up, down, remove]: Tab walks the rows and
	// wraps to zone A on the last one.
	for i := 0; i < 3; i++ {
		scr.HandleKey(KeyTab, nil)
	}
	if scr.zone != editorZoneSeats {
		t.Fatalf("Tab inside the seat rows must stay in the seats zone; zone = %d", scr.zone)
	}
	scr.HandleKey(KeyTab, nil)
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

// The preset editor's retired models FIELD is gone: zone B holds the five
// remaining rows (search_endpoint, search_key_env, out_dir, root, json), the
// source column walks the same key order, and no save can write the retired
// "models" key. The seats pin stays the only seat source the editor writes.
func TestEditorHasNoModelsFieldAndSavesSeatsOnly(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gemini-x"},
	}})
	app, scr := newEditorApp("p1")
	if got := scr.formB.Len(); got != 5 {
		t.Fatalf("zone B rows = %d; want 5 (models row removed)", got)
	}
	for _, key := range presetSourceKeys {
		if key == "models" {
			t.Fatalf("the source column still walks a models key: %v", presetSourceKeys)
		}
	}
	if got := len(presetSourceKeys); got != 14 {
		t.Fatalf("source-column keys = %d; want 14 (the name identity plus the 13 knobs, models removed)", got)
	}
	// A save of the opened seats-pinned preset writes the lineup and nothing else.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("seats-only save message = %q", app.Message())
	}
	pf := reloadEditorPresets(t, path)
	if p, ok := pf.FindPreset("p1"); !ok || p.Seats != "native:gemini-x" {
		t.Fatalf("seats-only preset wrong on disk: %+v", pf.Presets)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"models"`) {
		t.Fatalf("the editor wrote the retired models key: %s", raw)
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
		zoneBKeys("", "", "", "", ""),
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
		zoneBKeys("", "", "", "", ""),
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
		zoneBKeys("", "", "", "", ""),
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

	// Legitimate: an ordinary task value saves verbatim. The U8 pre-fill seeds
	// the task field with "review", so the seed runes are dropped before the
	// replacement value is typed.
	path = editorHome(t)
	app2, _ := newEditorApp("")
	keys = joinPresetKeys(
		typePresetRunes("p1"), k1(KeyTab),
		backspaceKeys(len(presetEngineDefaultTask)),
		typePresetRunes("security"), k1(KeyTab),
		k1(KeyTab, KeyTab, KeyTab, KeyTab, KeyTab), // n..max_native, then host zone
		k1(KeyDown, KeyDown),
		zoneBKeys("", "", "", "", ""),
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
// free-text slots the reviewer named (out_dir, root, search_endpoint): each
// credential-shaped value is refused with nothing written. The seats slot is
// gone from the grid (the seat rows own it now) and the retired models slot is
// gone with its field, so those two are covered elsewhere: the guard still runs
// over the joined seat value, which TestEditorCredentialShapedSeatRowRefused
// pins.
func TestEditorCredentialShapedFreeTextSlotsRefused(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"out_dir", testfixtures.GHPSixteen},
		{"root", testfixtures.SlackBotPlain},
		{"search_endpoint", testfixtures.GroqKeyMid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := editorHome(t)
			app, _ := newEditorApp("")
			zoneB := []string{"", "", "", "", ""}
			switch tc.name {
			case "search_endpoint":
				zoneB[0] = tc.val
			case "out_dir":
				zoneB[2] = tc.val
			case "root":
				zoneB[3] = tc.val
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
			zoneBKeys("", "", "", "", ""),
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
// the preset carries. The window SHARES the frame's spare rows with the seats
// sub-list (subListPages) and, since r11, with the two-row explain block
// above the hint: at 80x24 the hosts get 1 row and the seats get 1, the first
// host row renders for 0 hosts, and a longer host list pages to its action
// rows instead of falling off the frame.
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
		{name: "0 hosts", hosts: 0, wantLine: 24, whole: []string{"[ 1] Add host"}},
		{name: "1 host", hosts: 1, wantLine: 24, whole: []string{"[ 1] a.example.com"}},
		{name: "8 hosts", hosts: 8, wantLine: 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(t, tc.hosts)
			_, scr, lines := editorFrameAt80x24(t, "p")
			// 24 rows of frame minus the chrome this body shape spends (title,
			// both separators, base footer, the 7-field zone-A grid, the 5-field
			// zone-B grid, both sub-list headers, the two-row explain block and
			// the hint) leaves 3 rows: seats take 1, hosts take the other 2
			// (r11: the explain sentence owns the two rows above the hint, so
			// the windows yield before the hint does; the model-field removal
			// gave the extra row to the host window).
			if got := scr.hosts.PageSize(); got != 2 {
				t.Fatalf("allow_hosts page size = %d, want 2", got)
			}
			if got := scr.seats.PageSize(); got != 1 {
				t.Fatalf("seats page size = %d, want 1", got)
			}
			// The explain block owns both variable rows: the armed field's
			// sentence wraps over them above the hint (r11 F-01).
			explain := wrapExplainLines(scr.explainLine(), 80, 2)
			if len(explain) != 2 {
				t.Fatalf("explain block = %d rows, want 2: %q", len(explain), explain)
			}
			if !frameLineMatches(lines, func(l string) bool { return l == explain[0] }) ||
				!frameLineMatches(lines, func(l string) bool { return l == explain[1] }) {
				t.Fatalf("two-row explain block missing from the frame:\n%s", strings.Join(lines, "\n"))
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

	// The second host action row pages with the one-row window instead of
	// rendering beside the first: at 0 hosts the Remove row is row 2, and
	// stepping onto it scrolls it into the window (the reachability the two
	// visible rows used to provide).
	t.Run("0 hosts Remove row pages into the window", func(t *testing.T) {
		seed(t, 0)
		app, scr, _ := editorFrameAt80x24(t, "p")
		scr.hosts.SetCursor(scr.hosts.Len() - 1)
		lines := app.Frame().Lines
		if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[ 2] Remove host") }) {
			t.Fatalf("the Remove row is unreachable in the one-row window:\n%s", strings.Join(lines, "\n"))
		}
	})

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
		zoneBKeys("", "", "", "", ""),
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
		zoneBKeys("", "", "", "", ""),
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
		zoneBKeys("", "", "", "", ""),
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
		backspaceKeys(len(presetEngineDefaultTask)), // drop the U8 task seed
		typePresetRunes("security"), k1(KeyEnter),   // open the picker over "security"
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
			typePresetRunes("p1"), k1(KeyTab),
			backspaceKeys(len(presetEngineDefaultTask)), // drop the U8 task seed
			typePresetRunes(task), k1(KeyTab),
			k1(KeyTab, KeyTab, KeyTab, KeyTab, KeyTab), // n..max_native, then host zone
			k1(KeyDown, KeyDown),
			zoneBKeys("", "", "", "", ""),
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
		{"unknown_task", fanout.ErrPresetUnknownTask, "silently run the review prompt"},
		{"allow_hosts", fanout.ErrPresetAllowHosts, "Remove the invalid host row"},
		{"seats", fanout.ErrPresetSeats, "Remove or fix the offending seat row"},
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

// --- Structured seat lineup (U1) -----------------------------------------
//
// The seats section replaced the free-text `seats` field: one numbered row per
// seat, Enter arms and Space toggles a row, the action rows reorder or remove it, and
// "+ Add seat" opens the model picker over the combined inventory. The stored
// value is still the SAME comma string the run-time parser reads, so these
// tests pin the interaction AND the on-disk grammar.

// seatInvFixture is the stubbed model inventory the picker tests read: two
// native rows, two provider rows, and a second gemini row so the family
// duplicate warning has something to fire on. Rows are in inventory order
// (native first, then the provider group), which is what the picker's sections
// mirror.
func seatInvFixture() *inventory.Inventory {
	return &inventory.Inventory{Entries: []inventory.Entry{
		{Kind: "native", Group: "native", ModelID: "gemini-3.8-flash-high",
			DisplayName: "Gemini 3.8 Flash High", Spec: "native:gemini-3.8-flash-high", Family: "gemini"},
		{Kind: "native", Group: "native", ModelID: "gemini-3.8-pro-high",
			DisplayName: "Gemini 3.8 Pro High", Spec: "native:gemini-3.8-pro-high", Family: "gemini"},
		{Kind: "provider", Group: "nw", ModelID: "mimo-v2.6-pro-flex",
			DisplayName: "MiMo v2.6 Pro", Spec: "nw/mimo-v2.6-pro-flex", Family: "mimo"},
		{Kind: "provider", Group: "nw", ModelID: "deepseek-v4.1-flash-flex",
			DisplayName: "DeepSeek v4.1 Flash", Spec: "nw/deepseek-v4.1-flash-flex", Family: "deepseek"},
	}}
}

// stubSeatInventory swaps the picker's model inventory for one test, so no
// `agy models` subprocess is spawned.
func stubSeatInventory(t *testing.T, inv *inventory.Inventory) {
	t.Helper()
	prev := presetSeatInventory
	presetSeatInventory = func(context.Context) (*inventory.Inventory, error) { return inv, nil }
	t.Cleanup(func() { presetSeatInventory = prev })
}

// tabN is n Tab keys.
func tabN(n int) []Key {
	ks := make([]Key, 0, n)
	for i := 0; i < n; i++ {
		ks = append(ks, KeyTab)
	}
	return ks
}

// seatZoneKeys is the Tab walk from a fresh editor into the seats zone: the 7
// zone-A fields, the Add and Remove host rows, and the 6 zone-B fields.
func seatZoneKeys() []Key { return tabN(7 + 2 + 5) }

// TestEditorSeatPickerAppendsAtEnd drives the whole add flow through scripted
// keys: walk into the seats zone, open the picker on the empty "+ Add seat"
// row, pick a native model, step back to the Add row, pick a provider model,
// and save. The seats land at the END of the lineup in pick order, and the
// stored value is the comma grammar the run-time parser reads.
func TestEditorSeatPickerAppendsAtEnd(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	app, scr := newEditorApp("")
	keys := joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown), // host zone -> zone B
		zoneBKeys("", "", "", "", ""),
		k1(KeyTab),            // zone B -> seats zone, cursor on the Add row
		k1(KeyEnter),          // open the picker (cursor on the native section row)
		k1(KeyDown, KeyEnter), // row 2 = native:gemini-3.8-flash-high
		// The append parked the cursor on the new seat row; the Add row is the
		// next row down.
		k1(KeyDown, KeyEnter), // open the picker again
		// Picker rows: 1 native header, 2 native flash, 3 native pro, 4 nw
		// header, 5 nw/mimo-v2.6-pro-flex.
		k1(KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter),
		k1(KeyCtrlS),
	)
	app.CaptureRun(&ScriptedKeys{Keys: keys})

	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("save message = %q", app.Message())
	}
	want := []string{"native:gemini-3.8-flash-high", "nw/mimo-v2.6-pro-flex"}
	if strings.Join(scr.seatVals, ",") != strings.Join(want, ",") {
		t.Fatalf("seat rows = %v, want %v", scr.seatVals, want)
	}
	p, ok := reloadEditorPresets(t, path).FindPreset("p1")
	if !ok {
		t.Fatalf("preset p1 not saved")
	}
	if p.Seats != strings.Join(want, ",") {
		t.Fatalf("stored seats = %q, want %q", p.Seats, strings.Join(want, ","))
	}
}

// TestEditorSeatPickerSectionsNativeFirst pins the picker layout: the native
// section comes first, then one section per provider group, and every model row
// shows the exact token that will be stored. Activating a section header is
// inert: the overlay stays open instead of closing on a value that is not a
// model.
func TestEditorSeatPickerSectionsNativeFirst(t *testing.T) {
	editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	app := NewApp(100, 30, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPresetEditorScreen("").(*presetEditorScreen)
	app.Push(scr)
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown),
		zoneBKeys("", "", "", "", ""),
		k1(KeyTab, KeyEnter), // seats zone -> picker, cursor on the native header
		k1(KeyEnter),         // Enter on a section header: consumed, overlay stays
	)})
	if app.Modal() == nil {
		t.Fatal("activating a section header closed the picker")
	}
	out := strings.Join(app.Frame().Lines, "\n")
	nativeHeader := strings.Index(out, "native (2)")
	nwHeader := strings.Index(out, "nw (2)")
	if nativeHeader < 0 || nwHeader < 0 {
		t.Fatalf("picker sections missing from the frame:\n%s", out)
	}
	if nativeHeader > nwHeader {
		t.Fatalf("native section must precede the provider section:\n%s", out)
	}
	for _, spec := range []string{"native:gemini-3.8-flash-high", "native:gemini-3.8-pro-high",
		"nw/mimo-v2.6-pro-flex", "nw/deepseek-v4.1-flash-flex"} {
		if !strings.Contains(out, spec) {
			t.Fatalf("picker row %q missing (the stored token must be visible):\n%s", spec, out)
		}
	}
	if len(scr.seatVals) != 0 {
		t.Fatalf("a section header added a seat: %v", scr.seatVals)
	}
}

// TestEditorSeatReorderAndRemove drives the seat verbs: Enter arms a seat row,
// the ▲/▼ rows swap the armed seat with its neighbour, and the ✕ row drops it.
// The seat order IS the report order, so the assertions read the same order the
// engine will use.
func TestEditorSeatReorderAndRemove(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gemini-3.8-flash-high,nw/mimo-v2.6-pro-flex,nw/deepseek-v4.1-flash-flex"},
	}})
	app, scr := newEditorApp("p1")
	if len(scr.seatVals) != 3 {
		t.Fatalf("seeded seat rows = %v, want 3", scr.seatVals)
	}
	// Walk to the seats zone, arm seat 3 (Down twice), then walk to the ▲ row:
	// rows are [seat1, seat2, seat3, add, up, down, remove].
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		seatZoneKeys(),
		k1(KeyDown, KeyDown, KeyEnter), // arm seat 3
		k1(KeyDown, KeyDown),           // -> the ▲ row
		k1(KeyEnter),                   // seat 3 moves into slot 2
	)})
	want := []string{"native:gemini-3.8-flash-high", "nw/deepseek-v4.1-flash-flex", "nw/mimo-v2.6-pro-flex"}
	if strings.Join(scr.seatVals, ",") != strings.Join(want, ",") {
		t.Fatalf("after the ▲ move seat rows = %v, want %v", scr.seatVals, want)
	}
	if scr.seatSel != 1 {
		t.Fatalf("the armed selection must follow the moved seat; seatSel = %d", scr.seatSel)
	}
	// The cursor stayed on the ▲ row, which is now index 4 (three seats, then
	// add). One step down is ▼: push the armed seat back down.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, KeyEnter}})
	want = []string{"native:gemini-3.8-flash-high", "nw/mimo-v2.6-pro-flex", "nw/deepseek-v4.1-flash-flex"}
	if strings.Join(scr.seatVals, ",") != strings.Join(want, ",") {
		t.Fatalf("after the ▼ move seat rows = %v, want %v", scr.seatVals, want)
	}
	// One more step down is ✕: it removes the armed seat, which the ▼ move left
	// back at slot 3.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, KeyEnter}})
	want = []string{"native:gemini-3.8-flash-high", "nw/mimo-v2.6-pro-flex"}
	if strings.Join(scr.seatVals, ",") != strings.Join(want, ",") {
		t.Fatalf("after the ✕ removal seat rows = %v, want %v", scr.seatVals, want)
	}
	if scr.seatSel != -1 {
		t.Fatalf("removal must clear the armed seat; seatSel = %d", scr.seatSel)
	}
	// Delete on a seat row is the same verb without arming: park on seat 1 and
	// press Delete.
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyHome, KeyDelete}})
	if len(scr.seatVals) != 1 || scr.seatVals[0] != "nw/mimo-v2.6-pro-flex" {
		t.Fatalf("Delete on a seat row did not remove it: %v", scr.seatVals)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset p1 saved.") {
		t.Fatalf("save message = %q", app.Message())
	}
	if p, ok := reloadEditorPresets(t, path).FindPreset("p1"); !ok || p.Seats != "nw/mimo-v2.6-pro-flex" {
		t.Fatalf("stored seats = %+v, want the surviving seat only", p)
	}
}

// TestEditorSeatSpaceToggleAndEnterArm pins Space as a selection-only toggle and
// Enter's existing arm behavior, with the marker reflecting each state.
func TestEditorSeatSpaceToggleAndEnterArm(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:a,nw/b"},
	}})
	app, scr := newEditorApp("p1")
	app.CaptureRun(&ScriptedKeys{Keys: seatZoneKeys()})
	if got := scr.seats.Row(0).Label; got != "[ ] native:a" {
		t.Fatalf("initial seat label = %q, want unarmed marker", got)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeySpace}})
	if scr.seatSel != 0 || scr.seats.Row(0).Label != "[x] native:a" || scr.seats.Row(1).Label != "[ ] nw/b" {
		t.Fatalf("Space must arm only the cursor row: seatSel=%d labels=%q/%q", scr.seatSel, scr.seats.Row(0).Label, scr.seats.Row(1).Label)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeySpace}})
	if scr.seatSel != -1 || scr.seats.Row(0).Label != "[ ] native:a" {
		t.Fatalf("Space on the armed row must unarm it: seatSel=%d label=%q", scr.seatSel, scr.seats.Row(0).Label)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
	if scr.seatSel != 0 || scr.seats.Row(0).Label != "[x] native:a" {
		t.Fatalf("Enter must retain its arm behavior: seatSel=%d label=%q", scr.seatSel, scr.seats.Row(0).Label)
	}
}

// TestEditorSeatSpaceArmedActionsMatchesEnter proves every verb consumes the
// Space-armed seat exactly as it consumes the Enter-armed seat.
func TestEditorSeatSpaceArmedActionsMatchesEnter(t *testing.T) {
	actions := []struct {
		name      string
		actionRow int
		want      []string
		wantSel   int
	}{
		{name: "up", actionRow: 4, want: []string{"nw/b", "native:a", "native:c"}, wantSel: 0},
		{name: "down", actionRow: 5, want: []string{"native:a", "native:c", "nw/b"}, wantSel: 2},
		{name: "remove", actionRow: 6, want: []string{"native:a", "native:c"}, wantSel: -1},
	}
	for _, arm := range []struct {
		name string
		key  Key
	}{{name: "Space", key: KeySpace}, {name: "Enter", key: KeyEnter}} {
		for _, action := range actions {
			t.Run(arm.name+"/"+action.name, func(t *testing.T) {
				path := editorHome(t)
				seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
					{Name: "p1", Seats: "native:a,nw/b,native:c"},
				}})
				app, scr := newEditorApp("p1")
				keys := joinPresetKeys(seatZoneKeys(), k1(KeyDown, arm.key))
				for i := 1; i < action.actionRow; i++ {
					keys = append(keys, KeyDown)
				}
				keys = append(keys, KeyEnter)
				app.CaptureRun(&ScriptedKeys{Keys: keys})
				if strings.Join(scr.seatVals, ",") != strings.Join(action.want, ",") || scr.seatSel != action.wantSel {
					t.Fatalf("%s-armed %s: seats=%v seatSel=%d, want %v seatSel=%d", arm.name, action.name, scr.seatVals, scr.seatSel, action.want, action.wantSel)
				}
			})
		}
	}
}

// TestEditorSeatArmedMarkerFits80x24 keeps the four-cell marker visible ahead
// of a long token without breaking the canonical frame.
func TestEditorSeatArmedMarkerFits80x24(t *testing.T) {
	path := editorHome(t)
	spec := "native:" + strings.Repeat("x", 60)
	if len([]rune(spec)) < 60 {
		t.Fatal("frame fixture must carry at least 60 runes")
	}
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: spec},
	}})
	app, scr, _ := editorFrameAt80x24(t, "p1")
	scr.zone = editorZoneSeats
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeySpace}})
	lines := app.Frame().Lines
	if len(lines) != 24 || lines[len(lines)-1] != baseFooter {
		t.Fatalf("armed long-token frame broke its 80x24 shape:\n%s", strings.Join(lines, "\n"))
	}
	if !frameLineMatches(lines, func(l string) bool { return strings.Contains(l, "[x] native:") }) {
		t.Fatalf("armed marker missing from the long-token 80-column frame:\n%s", strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if got := cellwidth.String(line); got > 80 {
			t.Fatalf("frame line is %d cells, want <=80: %q", got, line)
		}
	}
}

// TestEditorSeatRemoveNothingArmedRefused pins the arming hint: pressing a seat
// action row with no armed seat names the missing step instead of no-oping.
func TestEditorSeatRemoveNothingArmedRefused(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gemini-3.8-flash-high"},
	}})
	app, scr := newEditorApp("p1")
	// Rows: [seat1, add, up, down, remove]. Walk into the seats zone, then to the
	// ✕ row without arming anything.
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		seatZoneKeys(),
		k1(KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter),
	)})
	if !strings.Contains(app.Message(), "no seat is selected") || !strings.Contains(app.Message(), "Enter or Space") {
		t.Fatalf("message = %q; want the arming hint naming Enter or Space", app.Message())
	}
	if len(scr.seatVals) != 1 {
		t.Fatalf("nothing armed must remove nothing: %v", scr.seatVals)
	}
}

// TestEditorSeatSaveRoundTripGrammar pins the serialization: a lineup built
// through the picker writes exactly the comma string a hand-written value would,
// compared token by token through the run-time parser.
func TestEditorSeatSaveRoundTripGrammar(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	app, _ := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown),
		zoneBKeys("", "", "", "", ""),
		k1(KeyTab, KeyEnter),
		k1(KeyDown, KeyDown, KeyEnter),                   // native:gemini-3.8-pro-high
		k1(KeyDown, KeyEnter),                            // back to the Add row, then the picker
		k1(KeyDown, KeyDown, KeyDown, KeyDown, KeyEnter), // nw/mimo-v2.6-pro-flex
		k1(KeyCtrlS),
	)})
	p, ok := reloadEditorPresets(t, path).FindPreset("p1")
	if !ok {
		t.Fatal("preset not saved")
	}
	handWritten := "native:gemini-3.8-pro-high, nw/mimo-v2.6-pro-flex"
	gotSeats, err := fanout.ParseSeatSpecs(p.Seats)
	if err != nil {
		t.Fatalf("stored seats do not parse: %q (%v)", p.Seats, err)
	}
	wantSeats, err := fanout.ParseSeatSpecs(handWritten)
	if err != nil {
		t.Fatalf("fixture seats do not parse: %v", err)
	}
	if len(gotSeats) != len(wantSeats) {
		t.Fatalf("seat count = %d, want %d", len(gotSeats), len(wantSeats))
	}
	for i := range wantSeats {
		if gotSeats[i].Raw != wantSeats[i].Raw || gotSeats[i].Kind != wantSeats[i].Kind {
			t.Fatalf("seat %d = %+v, want %+v", i+1, gotSeats[i], wantSeats[i])
		}
	}
	if p.Seats != "native:gemini-3.8-pro-high,nw/mimo-v2.6-pro-flex" {
		t.Fatalf("stored spelling = %q, want the normalized comma grammar", p.Seats)
	}
}

// TestEditorEmptySeatListSavesAuto pins the untouched fallback: with no seat
// rows the preset stores no seats at all, so the engine keeps auto-selecting the
// lineup, and the section header says so on the frame.
func TestEditorEmptySeatListSavesAuto(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	app, scr := newEditorApp("")
	out := app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown),
		zoneBKeys("", "", "", "", ""),
		k1(KeyCtrlS),
	)})
	if len(scr.seatVals) != 0 {
		t.Fatalf("seat rows = %v, want none", scr.seatVals)
	}
	p, ok := reloadEditorPresets(t, path).FindPreset("p1")
	if !ok || p.Seats != "" {
		t.Fatalf("empty lineup must store no seats: %+v", p)
	}
	if !strings.Contains(out, "none pinned") {
		t.Fatalf("the empty-lineup hint is missing from the frame:\n%s", out)
	}
}

// TestEditorMalformedStoredSeatsLoadsAndRefusesSave pins the tolerant-load /
// strict-save split: a hand-edited seats value loads into rows verbatim (the
// editor never crashes and never silently drops a token), and the save refuses
// with the offending SEAT NUMBER, writing nothing.
func TestEditorMalformedStoredSeatsLoadsAndRefusesSave(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Task: "review", Seats: "native:gemini-x,native:,native:a/b"},
	}})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	app, scr := newEditorApp("p1")
	if len(scr.seatVals) != 3 {
		t.Fatalf("seat rows = %v, want the 3 stored tokens verbatim", scr.seatVals)
	}
	out := strings.Join(app.Frame().Lines, "\n")
	if !strings.Contains(out, "native:") || !strings.Contains(out, "native:a/b") {
		t.Fatalf("the malformed rows must render as stored:\n%s", out)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "seat 2") {
		t.Fatalf("refusal = %q; want it to name seat 2", app.Message())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read after refused save: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("refused save rewrote the file:\nbefore %s\nafter  %s", before, after)
	}
}

// TestEditorSeatDuplicateWarningFires pins the soft duplicate warning: two seats
// on the same model still save (the lineup is the user's call) and the save
// confirmation carries the named caution. The EXACT-duplicate branch must use the
// "same model" sentence and never the family sentence: the two lines name
// different fixes (delete a row vs. swap a seat for another family), which is
// what fix-list FL-8(b) split apart.
func TestEditorSeatDuplicateWarningFires(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gemini-3.8-flash-high,native:gemini-3.8-flash-high"},
	}})
	app, _ := newEditorApp("p1")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app.Message()
	if !strings.Contains(msg, "Preset p1 saved.") {
		t.Fatalf("duplicate lineup must still save; message = %q", msg)
	}
	if !strings.Contains(msg, "warning:") || !strings.Contains(msg, "seats 1 and 2") {
		t.Fatalf("duplicate warning missing from the save confirmation: %q", msg)
	}
	if !strings.Contains(msg, "point at the same model") {
		t.Fatalf("exact-duplicate warning must name the same MODEL: %q", msg)
	}
	if strings.Contains(msg, "same model family") || strings.Contains(msg, "cross-check") {
		t.Fatalf("exact-duplicate warning borrowed the family wording: %q", msg)
	}
	p, _ := reloadEditorPresets(t, path).FindPreset("p1")
	if p.Seats != "native:gemini-3.8-flash-high,native:gemini-3.8-flash-high" {
		t.Fatalf("the warning must not rewrite the lineup: %q", p.Seats)
	}
}

// TestEditorSeatFamilyDuplicateWarningFires pins the inventory-backed half of
// the warning: two different ids that resolve to the SAME family are called out
// with the FAMILY sentence (fix-list FL-8(b): it must not say they "point at the
// same model", because they are different models and the fix is a family swap,
// not a deletion), and a seat missing from the inventory gets the stale-lineup
// caution.
func TestEditorSeatFamilyDuplicateWarningFires(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	// p1 IS the store default, so the missing provider seat takes the standing-rung
	// skip sentence (R2-2: a non-default row would take the hard-error sentence).
	seedEditorPresets(t, path, &fanout.PresetsFile{
		DefaultPreset: "p1",
		Presets: []fanout.Preset{
			{Name: "p1", Seats: "native:gemini-3.8-flash-high,native:gemini-3.8-pro-high,nw/gone-model"},
		},
	})
	app, scr := newEditorApp("p1")
	// Opening the picker is what reads the inventory (rows: 3 seats, then the
	// Add row at index 3, so End walks down to the action rows and back up);
	// the save then warns from the cached copy instead of spawning again.
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		seatZoneKeys(),
		k1(KeyEnd, KeyUp, KeyUp, KeyUp, KeyEnter), // -> the Add row -> picker
		k1(KeyEsc),
	)})
	if scr.seatInv == nil {
		t.Fatal("the picker open did not cache the model inventory")
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app.Message()
	if !strings.Contains(msg, "seats 1 and 2") {
		t.Fatalf("family duplicate warning missing: %q", msg)
	}
	if !strings.Contains(msg, "same model family") {
		t.Fatalf("family warning must name the FAMILY: %q", msg)
	}
	if strings.Contains(msg, "point at the same model") {
		t.Fatalf("family warning reused the exact-duplicate sentence (FL-8b): %q", msg)
	}
	if !strings.Contains(msg, "seat 3") || !strings.Contains(msg, "skips it and finishes marked degraded") {
		t.Fatalf("stale-lineup warning missing: %q", msg)
	}
	if len(scr.seatVals) != 3 {
		t.Fatalf("the warnings must not rewrite the lineup: %v", scr.seatVals)
	}
}

// TestEditorCredentialShapedSeatRowRefused keeps the secrets boundary on the
// seat rows: a credential-shaped token in a stored lineup is refused at save
// time by the shared free-text guard, with nothing written and no operand echo.
func TestEditorCredentialShapedSeatRowRefused(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	planted := "sk-" + "abcdefghijklmnop"
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: planted},
	}})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	app, _ := newEditorApp("p1")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "cannot be saved") {
		t.Fatalf("message = %q; want the credential refusal", app.Message())
	}
	if strings.Contains(app.Message(), planted) {
		t.Fatalf("the refusal echoed the operand: %q", app.Message())
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("refused save rewrote the file: %v", err)
	}
}

// TestEditorSeatPickerInventoryFailure pins the honest degrade: an unreadable
// model list opens no picker, adds nothing, and says so.
func TestEditorSeatPickerInventoryFailure(t *testing.T) {
	editorHome(t)
	prev := presetSeatInventory
	presetSeatInventory = func(context.Context) (*inventory.Inventory, error) {
		return nil, errors.New("agy models failed: no catalog")
	}
	t.Cleanup(func() { presetSeatInventory = prev })
	app, scr := newEditorApp("")
	app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown),
		zoneBKeys("", "", "", "", ""),
		k1(KeyTab, KeyEnter),
	)})
	if !strings.Contains(app.Message(), "could not be read") {
		t.Fatalf("message = %q; want the inventory failure notice", app.Message())
	}
	// The dirty-field guard may be live, so the assertion is about the PICKER:
	// a failed read must never open an empty one.
	if _, ok := app.Modal().(*seatPicker); ok {
		t.Fatal("a failed inventory read must not open an empty picker")
	}
	if len(scr.seatVals) != 0 {
		t.Fatalf("failed read added seats: %v", scr.seatVals)
	}
}

// TestEditorSeatRowLabelsUseSlotAuthority pins fix-list FL-10: the seat-row label
// echoes the stored token through providers.MaskSlot, the registry-slot display
// authority this screen already renders preset names with (presetNameOperand) and
// the picker rows use, instead of plain MaskOperand. Two properties matter and
// both are asserted here:
//
//   - a legitimate spec keeps its exact spelling, including the provider/model
//     separator ("/") that the seat grammar needs, so the row shows what the file
//     carries;
//   - a credential-shaped token renders masked, and the authority strips control
//     runes that MaskOperand leaves in place, so a hand-edited token cannot paint
//     terminal controls into the frame through its row label.
func TestEditorSeatRowLabelsUseSlotAuthority(t *testing.T) {
	// The helper is the authority choice itself: MaskSlot strips a control rune
	// (ESC) that MaskOperand would pass through, and it keeps a benign
	// provider/model token byte-identical.
	const escSpec = "nw/mimo\x1b[31m-v2"
	if got := seatSpecOperand(escSpec); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("seatSpecOperand(%q) = %q; a control rune reached the label", escSpec, got)
	}
	for _, spec := range []string{"native:gemini-3.8-flash-high", "nw/mimo-v2.6-pro-flex", "gemini-3.8-flash-high (nw)"} {
		if got := seatSpecOperand(spec); got != spec {
			t.Fatalf("seatSpecOperand(%q) = %q; a legitimate token must render byte-identical", spec, got)
		}
	}

	// End to end on the frame: the provider separator survives, and a
	// credential-shaped seat row paints masked with no raw token anywhere.
	path := editorHome(t)
	stubSeatInventory(t, seatInvFixture())
	planted := "sk-" + "abcdefghijklmnop"
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "nw/mimo-v2.6-pro-flex," + planted},
	}})
	app, scr := newEditorApp("p1")
	out := strings.Join(app.Frame().Lines, "\n")
	if scr.seats.Len() < 2 {
		t.Fatalf("seat rows = %d, want the two seeded rows", scr.seats.Len())
	}
	if got := scr.seats.Row(0).Label; got != "[ ] nw/mimo-v2.6-pro-flex" {
		t.Fatalf("provider seat label = %q; want the unarmed marker and an untouched \"/\" separator", got)
	}
	if label := scr.seats.Row(1).Label; label == planted || !strings.Contains(label, "****") {
		t.Fatalf("credential-shaped seat label = %q; want the masked form", label)
	}
	if !strings.Contains(out, "nw/mimo-v2.6-pro-flex") {
		t.Fatalf("provider seat row missing from the frame:\n%s", out)
	}
	if strings.Contains(out, planted) {
		t.Fatalf("the raw credential-shaped token reached the frame:\n%s", out)
	}
}

// seatInvReadable is the inventory shape the FL-6 pins read: the registry was
// read (one provider row), the native catalog was read successfully, and it does
// not carry the ids the fixtures pin as stale. A successful native fetch always
// yields rows in production, so "readable catalog without this id" is exactly the
// stale-native-id state the editor must describe truthfully.
func seatInvReadable() *inventory.Inventory {
	return &inventory.Inventory{Entries: []inventory.Entry{
		{Kind: "provider", Group: "nw", ModelID: "mimo-v2.6-pro-flex",
			DisplayName: "MiMo v2.6 Pro", Spec: "nw/mimo-v2.6-pro-flex", Family: "mimo"},
	}}
}

// TestEditorSeatNativeStaleWarningTellsTheTruth pins fix-list FL-6(a): a native
// seat whose id is not in the native catalog is NOT skipped by the engine (native
// targets are appended unconditionally), so the editor must not promise a skip.
// The native half carries its own sentence, and it names the seat number only:
// the row already shows the token.
func TestEditorSeatNativeStaleWarningTellsTheTruth(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvReadable())
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gone-native,nw/mimo-v2.6-pro-flex"},
	}})
	app, scr := newEditorApp("p1")
	// The save branch reads the inventory a picker open would have cached.
	scr.seatInv = seatInvReadable()
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app.Message()
	if !strings.Contains(msg, "Preset p1 saved.") {
		t.Fatalf("a stale native seat must still save; message = %q", msg)
	}
	if !strings.Contains(msg, "warning:") || !strings.Contains(msg, "seat 1 is a native seat") {
		t.Fatalf("native stale warning missing its own sentence: %q", msg)
	}
	if !strings.Contains(msg, "will start and fail instead of being skipped") {
		t.Fatalf("native warning must state the real failure mode: %q", msg)
	}
	if strings.Contains(msg, "skips it and finishes marked degraded") {
		t.Fatalf("native warning borrowed the provider skip promise (FL-6a): %q", msg)
	}
	if strings.Contains(msg, "gone-native") {
		t.Fatalf("the native sentence takes only the seat number, not the token: %q", msg)
	}
	// The provider seat in the same lineup is present in the inventory, so no
	// provider caution may appear for it either.
	if strings.Contains(msg, "seat 2") {
		t.Fatalf("a present provider seat was flagged: %q", msg)
	}
}

// TestEditorSeatNativeWarningSilentWhenCatalogFailed pins FL-6(b): when the
// native catalog fetch failed (inv.NativeErr), the inventory holds zero native
// rows, so absence proves nothing and a valid native id must not be accused.
func TestEditorSeatNativeWarningSilentWhenCatalogFailed(t *testing.T) {
	path := editorHome(t)
	inv := seatInvReadable()
	inv.NativeErr = errors.New("agy models failed: no catalog rows")
	stubSeatInventory(t, inv)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gemini-3.8-flash-high,native:claude-opus-5-5-high"},
	}})
	app, scr := newEditorApp("p1")
	scr.seatInv = inv
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app.Message()
	if !strings.Contains(msg, "Preset p1 saved.") {
		t.Fatalf("save message = %q", msg)
	}
	if strings.Contains(msg, "warning:") {
		t.Fatalf("a failed native catalog must not accuse a valid lineup (FL-6b): %q", msg)
	}
	if len(scr.seatVals) != 2 {
		t.Fatalf("the lineup must be untouched: %v", scr.seatVals)
	}
}

// TestEditorSeatProviderStaleWarningKeepsSkipSentence keeps the provider half as a
// RUNG-QUALIFIED promise: when the edited preset is the store default AND the run
// names no preset, a run really does skip an unresolvable provider seat before it
// starts. The sentence must say that in those words, because the same preset
// selected BY NAME (--preset / AGY_PRO_FANOUT_PRESET) is the named rung and
// hard-errors. The named-rung arm is pinned separately
// (TestEditorSeatNamedRungWarningPromisesNoSkip).
func TestEditorSeatProviderStaleWarningKeepsSkipSentence(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, seatInvReadable())
	seedEditorPresets(t, path, &fanout.PresetsFile{
		DefaultPreset: "p1",
		Presets: []fanout.Preset{
			{Name: "p1", Seats: "nw/mimo-v2.6-pro-flex,nw/gone-model"},
		},
	})
	app, scr := newEditorApp("p1")
	scr.seatInv = seatInvReadable()
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app.Message()
	if !strings.Contains(msg, "seat 2 (nw/gone-model) is not in the current model list") {
		t.Fatalf("provider stale warning changed shape: %q", msg)
	}
	if !strings.Contains(msg, "skips it and finishes marked degraded") {
		t.Fatalf("provider warning lost the skip promise: %q", msg)
	}
	// The qualification is the r3 fix: the skip is conditional on the DEFAULT rung,
	// so the sentence has to name the condition and the counter-case.
	if !strings.Contains(msg, "as the store default and names no preset") {
		t.Fatalf("skip promise is not rung-qualified (r3): %q", msg)
	}
	if !strings.Contains(msg, "selects this preset by name stops with an error") {
		t.Fatalf("warning omits the named-rung counter-case (r3): %q", msg)
	}
	if strings.Contains(msg, "is a native seat") {
		t.Fatalf("a provider seat was described as native: %q", msg)
	}
	// Mutual exclusion against the sibling key's own wording.
	if strings.Contains(msg, "Only the store default preset may skip") {
		t.Fatalf("standing arm borrowed the named-rung sentence: %q", msg)
	}
}

// TestEditorSeatNamedRungWarningPromisesNoSkip pins R2-2's other arm: for a preset
// that is NOT the store default_preset row, FL-3 narrowed the skip tolerance away,
// so a run that selects the preset by name refuses to start on a missing provider
// seat. The warning must say that and must NOT reuse the standing-rung skip
// sentence - the two arms are mutually exclusive by construction.
func TestEditorSeatNamedRungWarningPromisesNoSkip(t *testing.T) {
	cases := []struct {
		name       string
		defaultKey string
	}{
		{"default key names another preset", "other"},
		{"no default key at all", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := editorHome(t)
			stubSeatInventory(t, seatInvReadable())
			seedEditorPresets(t, path, &fanout.PresetsFile{
				DefaultPreset: tc.defaultKey,
				Presets: []fanout.Preset{
					{Name: "p1", Seats: "nw/mimo-v2.6-pro-flex,nw/gone-model"},
					{Name: "other", Seats: "nw/mimo-v2.6-pro-flex"},
				},
			})
			app, scr := newEditorApp("p1")
			scr.seatInv = seatInvReadable()
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
			msg := app.Message()
			if !strings.Contains(msg, "Preset p1 saved.") {
				t.Fatalf("save message = %q", msg)
			}
			if !strings.Contains(msg, "Only the store default preset may skip a missing seat") {
				t.Fatalf("named-rung warning missing its own sentence: %q", msg)
			}
			if !strings.Contains(msg, "--preset or AGY_PRO_FANOUT_PRESET") {
				t.Fatalf("named-rung warning must name how such a run selects the preset: %q", msg)
			}
			if !strings.Contains(msg, "seat 2 (nw/gone-model)") {
				t.Fatalf("named-rung warning lost the seat number and token: %q", msg)
			}
			if strings.Contains(msg, "skips it and finishes marked degraded") {
				t.Fatalf("named-rung warning promised a skip the rung never performs (R2-2): %q", msg)
			}
			if strings.Contains(msg, "as the store default and names no preset") {
				t.Fatalf("named-rung warning borrowed the default-rung qualification: %q", msg)
			}
			if strings.Contains(msg, "is a native seat") {
				t.Fatalf("a provider seat was described as native: %q", msg)
			}
		})
	}
}

// TestSeatLineupRungIsStanding pins the rung test itself, including the shapes
// the end-to-end arms cannot reach: no name on disk (a new preset can never be the
// store default), a whitespace-only store key, and case-sensitive identity (the
// store addresses presets by exact name, so "P1" is a different preset than "p1").
func TestSeatLineupRungIsStanding(t *testing.T) {
	cases := []struct {
		name        string
		edited      string
		defaultName string
		want        bool
	}{
		{"default row", "p1", "p1", true},
		{"padded store key", "p1", " p1 ", true},
		{"no default key", "p1", "", false},
		{"different default row", "p1", "other", false},
		{"new preset", "", "p1", false},
		{"case differs", "P1", "p1", false},
	}
	for _, tc := range cases {
		e := &presetEditorScreen{name: tc.edited, defaultName: tc.defaultName}
		if got := e.seatLineupRungIsStanding(); got != tc.want {
			t.Errorf("%s: seatLineupRungIsStanding() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestEditorSeatWarningSilentWhenInventoryUnreadable pins the recorded decision
// for the shape the inventory cannot attribute: no rows and no native error means
// neither the registry nor the catalog was read (an unreadable or missing
// providers.json), so a missing row proves nothing for EITHER kind and the whole
// caution stays quiet instead of accusing a healthy lineup.
func TestEditorSeatWarningSilentWhenInventoryUnreadable(t *testing.T) {
	path := editorHome(t)
	stubSeatInventory(t, &inventory.Inventory{})
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "p1", Seats: "native:gemini-3.8-flash-high,nw/mimo-v2.6-pro-flex"},
	}})
	app, scr := newEditorApp("p1")
	scr.seatInv = &inventory.Inventory{}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app.Message()
	if !strings.Contains(msg, "Preset p1 saved.") {
		t.Fatalf("save message = %q", msg)
	}
	if strings.Contains(msg, "warning:") {
		t.Fatalf("an unreadable inventory must not accuse either seat kind: %q", msg)
	}
}

// TestSeatTokenIsNative pins the kind classifier the warning branches on: the
// grammar decides (case-insensitive prefix), and a token the parser refuses still
// classifies by prefix so a malformed native row can never be handed the provider
// sentence.
func TestSeatTokenIsNative(t *testing.T) {
	cases := []struct {
		token string
		want  bool
	}{
		{"native:gemini-3.8-flash-high", true},
		{"NATIVE:Gemini-3.8-Flash-High", true},
		{" native:gemini-x ", true},
		{"nw/mimo-v2.6-pro-flex", false},
		{"gemini-3.8-flash-high (nw)", false},
		{"native:", true},    // malformed (no id): prefix fallback keeps it native
		{"native:a/b", true}, // malformed (slash): prefix fallback keeps it native
		{"", false},
	}
	for _, tc := range cases {
		if got := seatTokenIsNative(tc.token); got != tc.want {
			t.Errorf("seatTokenIsNative(%q) = %v, want %v", tc.token, got, tc.want)
		}
	}
}

// --- r4 name-slot close-out (M-1), editor side ---------------------------
//
// The editor's name slots render the preset NAME through presetNameOperand, and
// this screen ALSO refuses a credential-shaped name at save (the shared
// looksSecretValue guard runs before the write). Two layers, both pinned here:
// the reachable name line is masked, and the plant cannot reach the
// confirmation/overwrite bodies at all because the save never starts.

// TestEditorPresetNameSlotsUseSlotAuthority pins the not-found line (the one name
// slot reachable without a write) plus the byte-identical benign confirmation and
// overwrite body.
func TestEditorPresetNameSlotsUseSlotAuthority(t *testing.T) {
	plant := testfixtures.SKLive
	tail := plant[len("sk_live_"):]
	mask := providers.MaskSlot(plant)
	if mask == plant {
		t.Fatalf("fixture is not the underscore-head class: MaskSlot(%q) = %q", plant, mask)
	}

	// not-found line: reachable, and masked.
	path := editorHome(t)
	app, _ := newEditorApp(plant)
	out := strings.Join(app.Frame().Lines, "\n")
	if strings.Contains(out, plant) || strings.Contains(out, tail) {
		t.Fatalf("not-found line echoed the plant raw:\n%s", out)
	}
	if !strings.Contains(out, mask) {
		t.Fatalf("not-found line missing the masked name %q:\n%s", mask, out)
	}

	// Save guard: the plant cannot be saved at all, so the confirmation body is
	// unreachable for it (the refusal names the FIELD only, never the value).
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: plant, Seats: "native:a"},
	}})
	app2, _ := newEditorApp(plant)
	app2.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	msg := app2.Message()
	if !strings.Contains(msg, "cannot be saved") {
		t.Fatalf("the name guard must refuse a credential-shaped name before any write; message = %q", msg)
	}
	if strings.Contains(msg, plant) || strings.Contains(msg, tail) {
		t.Fatalf("the refusal echoed the plant: %q", msg)
	}

	// Benign half: the confirmation and the overwrite body carry the exact name.
	path = editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: "nightly"},
		{Name: "nocturnal"},
	}})
	app3, _ := newEditorApp("nightly")
	app3.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if got := app3.Message(); !strings.Contains(got, "Preset nightly saved.") {
		t.Fatalf("benign save confirmation changed shape: %q", got)
	}

	// Overwrite body: rename nightly onto nocturnal (the dirty guard is a live
	// modal too, so the assertion is about the CONFIRM, not any modal).
	app4, _ := newEditorApp("nightly")
	keys := []Key{KeyHome}
	for i := 0; i < len("nightly"); i++ {
		keys = append(keys, KeyDelete)
	}
	for _, r := range "nocturnal" {
		keys = append(keys, RuneKey(r))
	}
	keys = append(keys, KeyCtrlS)
	app4.CaptureRun(&ScriptedKeys{Keys: keys})
	confirm, ok := app4.Modal().(*Confirm)
	if !ok {
		t.Fatalf("rename onto an existing name did not open the overwrite confirm; modal = %T, message=%q",
			app4.Modal(), app4.Message())
	}
	body := strings.Join(confirm.Body(100), " / ")
	if !strings.Contains(body, "nocturnal") {
		t.Fatalf("overwrite body lost the exact benign name: %q", body)
	}
	if strings.Contains(body, "****") {
		t.Fatalf("overwrite body masked a benign name: %q", body)
	}
}

// TestEditorSearchKeyEnvUnderscoreHeadEchoMasked pins the field-echo authority on
// the one credential-shaped slot that renders before validation runs: the
// underscore-head class (identity under MaskOperand) must not ride raw into the
// frame. Same class as the name slots above, on the value side.
func TestEditorSearchKeyEnvUnderscoreHeadEchoMasked(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	plant := testfixtures.SKLive
	tail := plant[len("sk_live_"):]
	mask := providers.MaskSlot(plant)
	if mask == plant {
		t.Fatalf("fixture is not the underscore-head class: MaskSlot(%q) = %q", plant, mask)
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: joinPresetKeys(
		zoneAKeys("p1", "", "", "", "", "", ""),
		k1(KeyDown, KeyDown, KeyEnter), // -> zone B, focus on search_key_env
		typePresetRunes(plant),
	)})
	if strings.Contains(out, plant) || strings.Contains(out, tail) {
		t.Fatalf("frame echoed the underscore-head value raw:\n%s", out)
	}
	if !strings.Contains(out, mask) {
		t.Fatalf("frame missing the masked echo %q:\n%s", mask, out)
	}
	if got := scr.formB.Value(1); got != plant {
		t.Fatalf("form must keep the raw editable value, got %q", got)
	}
}

// TestEditorNewPresetPrefill pins U8 item 1: a NEW preset editor opens with
// the built-in composite pre-filled as real editable values (task=review,
// n=3, timeout=300, tools=read,search, max_turns=500, root=.), while out_dir
// and max_native stay empty (the engine stamps the report dir, the state.json
// rung owns the native concurrency) and every other field stays empty too.
func TestEditorNewPresetPrefill(t *testing.T) {
	editorHome(t)
	_, scr := newEditorApp("")
	wantA := []string{"", "review", "3", "300", "read,search", "500", ""}
	for i, want := range wantA {
		if got := scr.formA.Value(i); got != want {
			t.Fatalf("zone-A field %d = %q, want %q (full valA %v)", i, got, want, scr.valA)
		}
	}
	wantB := []string{"", "", "", ".", ""}
	for i, want := range wantB {
		if got := scr.formB.Value(i); got != want {
			t.Fatalf("zone-B field %d = %q, want %q (full valB %v)", i, got, want, scr.valB)
		}
	}
	if len(scr.hostVals) != 0 || len(scr.seatVals) != 0 {
		t.Fatalf("new preset must open with empty hosts and seats, got %v / %v", scr.hostVals, scr.seatVals)
	}
	// The pre-fill is program state, not a user edit: the editor stays clean,
	// so closing never arms the discard gate.
	if scr.dirty() {
		t.Fatal("a fresh pre-filled new editor must not start dirty")
	}
}

func TestEditorSeedDirtyCtrlAMaterializesThroughDiscardGate(t *testing.T) {
	openDirtySeed := func(t *testing.T) (string, *App, *presetEditorScreen) {
		t.Helper()
		path := editorHome(t)
		app, seed := newEditorApp(fanout.BuiltinPresetName)
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyDown, KeyDown, RuneKey('4')}})
		if !seed.dirty() || app.Modal() != seed.guard {
			t.Fatal("precondition: the seed edit must arm its discard guard")
		}
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlA}})
		if app.Depth() != 2 || app.Top() != seed || app.Modal() == seed.guard || app.Modal() == nil {
			t.Fatalf("dirty Ctrl+A must open the discard gate before navigation; depth=%d top=%T modal=%T", app.Depth(), app.Top(), app.Modal())
		}
		return path, app, seed
	}

	t.Run("copy receives input and seed stays off disk", func(t *testing.T) {
		path, app, seed := openDirtySeed(t)
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}})
		copyEditor, ok := app.Top().(*presetEditorScreen)
		if !ok || copyEditor.seedEdit() || copyEditor.name != "" || app.Depth() != 2 || app.Modal() != nil {
			t.Fatalf("approved gate must replace the seed with a clean copy editor; depth=%d top=%T modal=%T", app.Depth(), app.Top(), app.Modal())
		}
		keys := joinPresetKeys(typePresetRunes("copy1"), k1(KeyDown, KeyDown, KeyBackspace, RuneKey('7')))
		app.CaptureRun(&ScriptedKeys{Keys: keys})
		if got := copyEditor.formA.Value(0); got != "copy1" {
			t.Fatalf("visible copy name = %q, want copy1", got)
		}
		if got := copyEditor.formA.Value(2); got != "7" {
			t.Fatalf("visible copy n = %q, want 7", got)
		}
		if got := seed.formA.Value(2); got != "4" {
			t.Fatalf("hidden seed received copy input: n = %q, want the original edit 4", got)
		}
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read saved presets: %v", err)
		}
		if strings.Contains(string(raw), fanout.BuiltinPresetName) {
			t.Fatalf("copy-directed keystrokes wrote the seed record: %s", raw)
		}
		pf, err := editorStore(t, path).LoadPresets()
		if err != nil || len(pf.Presets) != 1 || pf.Presets[0].Name != "copy1" || pf.Presets[0].N != 7 {
			t.Fatalf("on-disk presets = %+v, err=%v; want only copy1 with n=7; raw=%s", pf, err, raw)
		}
	})

	t.Run("cancel keeps dirty seed and guard", func(t *testing.T) {
		path, app, seed := openDirtySeed(t)
		app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyNo}})
		if app.Depth() != 2 || app.Top() != seed || app.Modal() != seed.guard || seed.formA.Value(2) != "4" {
			t.Fatalf("cancel must restore the dirty guarded seed; depth=%d top=%T modal=%T n=%q", app.Depth(), app.Top(), app.Modal(), seed.formA.Value(2))
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cancel must not write the store; stat err=%v", err)
		}
	})

	for _, closeKey := range []Key{KeyEsc, KeyRight} {
		t.Run("copy close "+string(closeKey), func(t *testing.T) {
			_, app, _ := openDirtySeed(t)
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes, closeKey}})
			if app.Depth() != 1 || app.Modal() != nil {
				t.Fatalf("%s must pop the visible copy to the root; depth=%d modal=%T", closeKey, app.Depth(), app.Modal())
			}
		})
	}
}

func TestEditorSeedDefaultsPreviewMatchesSource(t *testing.T) {
	fields := []struct {
		key          string
		engineValue  string
		defaultValue string
	}{
		{key: "n", engineValue: "3", defaultValue: "7"},
		{key: "timeout_seconds", engineValue: "300", defaultValue: "99"},
		{key: "tools", engineValue: "read,search", defaultValue: "read"},
		{key: "max_turns", engineValue: "500", defaultValue: "42"},
	}
	for _, existingSeed := range []bool{false, true} {
		name := "absent seed"
		if existingSeed {
			name = "existing seed"
		}
		t.Run(name, func(t *testing.T) {
			path := editorHome(t)
			if existingSeed {
				seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{{
					Name: fanout.BuiltinPresetName, Seats: "native:gemini-seed-a",
				}}})
			}
			providersPath := filepath.Join(filepath.Dir(path), "providers.json")
			defaults := []byte(`{"providers":[],"defaults":{"n":7,"timeoutSeconds":99,"tools":"read","maxTurns":42}}`)
			if err := providers.WriteFileAtomic(providersPath, defaults, 0o600); err != nil {
				t.Fatalf("write providers defaults: %v", err)
			}
			app, scr := newEditorApp(fanout.BuiltinPresetName)
			out := app.CaptureRun(&ScriptedKeys{})
			src := scr.sources()
			for _, field := range fields {
				row := fmt.Sprintf("%-15s : %s  providers.json defaults", field.key, field.defaultValue)
				if !strings.Contains(out, row) {
					t.Errorf("effective defaults row missing %q; frames:\n%s", row, out)
				}
				if src[field.key] != "defaults" {
					t.Errorf("source[%s] = %q, want defaults", field.key, src[field.key])
				}
				if got := scr.formA.Value(map[string]int{"n": 2, "timeout_seconds": 3, "tools": 4, "max_turns": 5}[field.key]); got != "" {
					t.Errorf("display preview changed form %s to %q; want empty (A2)", field.key, got)
				}
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
			pf, err := editorStore(t, path).LoadPresets()
			if err != nil || len(pf.Presets) != 1 {
				t.Fatalf("reload saved seed: pf=%+v err=%v", pf, err)
			}
			seed := pf.Presets[0]
			if seed.N != 0 || seed.TimeoutSeconds != 0 || seed.Tools != "" || seed.MaxTurns != 0 {
				t.Fatalf("display-only defaults leaked into the saved seed (A2): %+v", seed)
			}
		})
	}

	t.Run("engine fallback without defaults", func(t *testing.T) {
		editorHome(t)
		app, scr := newEditorApp(fanout.BuiltinPresetName)
		out := app.CaptureRun(&ScriptedKeys{})
		for _, field := range fields {
			row := fmt.Sprintf("%-15s : %s  built-in", field.key, field.engineValue)
			if !strings.Contains(out, row) {
				t.Errorf("engine fallback row missing %q; frames:\n%s", row, out)
			}
			if scr.sources()[field.key] != "builtin" {
				t.Errorf("source[%s] = %q, want builtin", field.key, scr.sources()[field.key])
			}
		}
	})
}

// TestEditorSeedCtrlAMaterializes pins the U8 copy gesture on the now-
// editable seed row: Ctrl+A opens a NEW-mode editor carrying the engine
// default pre-fill, and saving there writes a USER preset while the seed row
// itself keeps its own editor (which saves the seed record in place now).
func TestEditorSeedCtrlAMaterializes(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp(fanout.BuiltinPresetName)
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlA}})
	if app.Depth() != 3 {
		t.Fatalf("Ctrl+A must push a new editor (depth 3), got %d", app.Depth())
	}
	top, ok := app.Top().(*presetEditorScreen)
	if !ok || top.seedEdit() {
		t.Fatalf("Ctrl+A must open a NEW-mode editor, got %T seedEdit=%v", app.Top(), top.seedEdit())
	}
	if top.name != "" {
		t.Fatalf("materialized editor name = %q, want empty (user names it)", top.name)
	}
	for i, want := range []string{"", "review", "3", "300", "read,search", "500", ""} {
		if got := top.formA.Value(i); got != want {
			t.Fatalf("materialized zone-A field %d = %q, want %q", i, got, want)
		}
	}
	if got := top.formB.Value(3); got != "." {
		t.Fatalf("materialized root = %q, want the . seed", got)
	}
	if got := top.formB.Value(2); got != "" {
		t.Fatalf("materialized out_dir = %q, want empty", got)
	}
	// Name it and save: the record lands as a user preset.
	keys := append(typePresetRunes("base1"), KeyCtrlS)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "Preset base1 saved.") {
		t.Fatalf("save message = %q, want saved", app.Message())
	}
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil {
		t.Fatalf("reload presets: %v", err)
	}
	if len(pf.Presets) != 1 || pf.Presets[0].Name != "base1" {
		t.Fatalf("on-disk presets = %+v, want exactly base1", pf.Presets)
	}
	got := pf.Presets[0]
	if got.Task != "review" || got.N != 3 || got.TimeoutSeconds != 300 ||
		got.Tools != "read,search" || got.MaxTurns != 500 || got.MaxNative != 0 ||
		got.OutDir != "" || got.Root != "." {
		t.Fatalf("materialized record = %+v, want the built-in composite seeds", got)
	}
	// No default key moved, and the reserved name still opens the seed editor.
	if pf.DefaultPreset != "" {
		t.Fatalf("materialize touched the default key: %q", pf.DefaultPreset)
	}
	if again := NewPresetEditorScreen(fanout.BuiltinPresetName); !again.(*presetEditorScreen).seedEdit() {
		t.Fatal("the reserved name no longer opens the seed editor after materialize")
	}
}

// TestEditorExplainLineFollowsArmedField pins U8 item 3 (English): the block
// above the key hint renders the armed field's presets.editor.explain.*
// sentence (word-wrapped over up to two rows since r11), and it moves as the
// focus walks every zone: grid A, the host block, grid B, and the seats block.
func TestEditorExplainLineFollowsArmedField(t *testing.T) {
	editorHome(t)
	app, scr := newEditorApp("")
	en := i18n.Load("en")
	// r11: the explain block word-wraps over up to two rows, so a sentence
	// renders when ANY of its wrapped rows is on the frame.
	frameHas := func(key string) bool {
		for _, l := range app.Frame().Lines {
			for _, want := range wrapExplainLines(en.T(key), app.Width(), 2) {
				if l == want {
					return true
				}
			}
		}
		return false
	}
	step := func(k Key) {
		t.Helper()
		scr.HandleKey(k, nil)
	}
	if !frameHas("presets.editor.explain.name") {
		t.Fatal("fresh editor must explain the name field")
	}
	step(KeyTab) // -> task
	if !frameHas("presets.editor.explain.task") {
		t.Fatal("explain line did not follow the focus to task")
	}
	for i := 0; i < 6; i++ {
		step(KeyTab)
	} // -> host block
	if scr.zone != editorZoneHosts || !frameHas("presets.editor.explain.allow_hosts") {
		t.Fatalf("host zone explain missing (zone %d)", scr.zone)
	}
	step(KeyTab) // host add row
	step(KeyTab) // -> zone B, search_endpoint
	if !frameHas("presets.editor.explain.search_endpoint") {
		t.Fatal("explain line missing for search_endpoint")
	}
	for _, key := range []string{
		"presets.editor.explain.search_key_env",
		"presets.editor.explain.out_dir",
		"presets.editor.explain.root",
	} {
		step(KeyEnter)
		if !frameHas(key) {
			t.Fatalf("explain line missing for %s", key)
		}
	}
	step(KeyTab) // -> json
	if !frameHas("presets.editor.explain.json") {
		t.Fatal("explain line missing for json")
	}
	step(KeyTab) // -> seats block
	if scr.zone != editorZoneSeats || !frameHas("presets.editor.explain.seats") {
		t.Fatalf("seats zone explain missing (zone %d)", scr.zone)
	}
}

// TestEditorExplainLineKorean pins the same row in ko: the sentence resolves
// through the Korean catalog, not the English fallback.
func TestEditorExplainLineKorean(t *testing.T) {
	editorHome(t)
	home := os.Getenv(providers.EnvHome)
	st := mode.State{
		CurrentMode:    "full",
		DefaultProfile: "native",
		ManagedItems:   append([]string(nil), mode.DefaultManagedItems...),
		UpdatedAt:      time.Now().UTC(),
		Language:       "ko",
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "state.json"), data, 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	app, scr := newEditorApp("")
	if scr.lang != "ko" {
		t.Fatalf("editor language = %q, want ko", scr.lang)
	}
	ko := i18n.Load("ko")
	frameHas := func(key string) bool {
		for _, l := range app.Frame().Lines {
			for _, want := range wrapExplainLines(ko.T(key), app.Width(), 2) {
				if l == want {
					return true
				}
			}
		}
		return false
	}
	if !frameHas("presets.editor.explain.name") {
		t.Fatal("ko editor must render the Korean name explanation")
	}
	scr.HandleKey(KeyTab, nil) // -> task
	if !frameHas("presets.editor.explain.task") {
		t.Fatal("ko explain line did not follow the focus to task")
	}
	if frameHas("presets.editor.explain.name") {
		t.Fatal("stale name explanation still on the frame after arming moved")
	}
}

// editorAppWithLang seeds state.json with the given display language and
// pushes a fresh editor, so a catalog other than English resolves through the
// real language path (same wiring TestEditorExplainLineKorean pins).
func editorAppWithLang(t *testing.T, lang string) (*App, *presetEditorScreen) {
	t.Helper()
	editorHome(t)
	home := os.Getenv(providers.EnvHome)
	st := mode.State{
		CurrentMode:    "full",
		DefaultProfile: "native",
		ManagedItems:   append([]string(nil), mode.DefaultManagedItems...),
		UpdatedAt:      time.Now().UTC(),
		Language:       lang,
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "state.json"), data, 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	return newEditorApp("")
}

// explainRowsAboveHint returns the explain block rows rendered immediately
// above the key hint, in frame order.
func explainRowsAboveHint(t *testing.T, lines []string, hint string) []string {
	t.Helper()
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == hint {
			return lines[i-2 : i]
		}
	}
	t.Fatalf("hint line %q not on the frame:\n%s", hint, strings.Join(lines, "\n"))
	return nil
}

// TestEditorExplainLineTwoSlot pins r11 F-01 (P1): at the canonical 80x24
// geometry the armed field's explain sentence renders word-wrapped over the
// TWO variable rows above the hint, so the native-fallback clause stays on
// the frame instead of starting past the old one-line 80-cell cut.
func TestEditorExplainLineTwoSlot(t *testing.T) {
	editorHome(t)
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPresetEditorScreen("").(*presetEditorScreen)
	app.Push(scr)
	scr.zone = editorZoneB
	scr.formB.SetCursor(0) // arm search_endpoint
	en := i18n.Load("en")
	want := wrapExplainLines(en.T("presets.editor.explain.search_endpoint"), 80, 2)
	if len(want) != 2 {
		t.Fatalf("en search_endpoint explain = %d rows, want the two-slot wrap", len(want))
	}
	rows := explainRowsAboveHint(t, app.Frame().Lines, presetsEditorHint)
	if rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("explain block above the hint =\n  %q\n  %q\nwant\n  %q\n  %q",
			rows[0], rows[1], want[0], want[1])
	}
	joined := rows[0] + " " + rows[1]
	if !strings.Contains(joined, "agy Google search") {
		t.Fatalf("the native-fallback clause is not visible in the two-slot wrap:\n  %q\n  %q", rows[0], rows[1])
	}
	for _, r := range rows {
		if cellwidth.String(r) > 80 {
			t.Fatalf("explain row wider than 80 cells: %q", r)
		}
	}
	if path := os.Getenv("AGY_PRO_VISUAL_OUT"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(app.Frame().Lines, "\n")), 0o600); err != nil {
			t.Fatalf("write visual frame: %v", err)
		}
	}
}

// TestEditorSeedHintIsTheNormalEditorHint pins the A5 half that lives on the
// editor: the seed row is a normal editable editor, so its footer hint is
// the ordinary editor hint naming Ctrl+S, and NO code path renders the old
// read-only wording as the truth about row [1].
func TestEditorSeedHintIsTheNormalEditorHint(t *testing.T) {
	editorHome(t)
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPresetEditorScreen(fanout.BuiltinPresetName))
	lines := app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool {
		return l == cellwidth.Truncate(presetsEditorHint, 80) && strings.Contains(l, "Ctrl+S")
	}) {
		t.Fatalf("normal editor hint missing from the seed editor's 80-col frame:\n%s",
			strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if strings.Contains(l, "Read-only") {
			t.Fatalf("seed editor frame still renders read-only wording:\n%s", strings.Join(lines, "\n"))
		}
	}
}

// TestEditorExplainSeatsKoreanClause pins r11 F-02 (P2): the ko seats explain
// names the missing object correctly (the seat's MODEL is missing from the
// model list) and no longer says "a seat missing from the default preset".
func TestEditorExplainSeatsKoreanClause(t *testing.T) {
	app, scr := editorAppWithLang(t, "ko")
	scr.zone = editorZoneSeats
	ko := i18n.Load("ko")
	hint := ko.T("presets.editor.hint")
	rows := explainRowsAboveHint(t, app.Frame().Lines, cellwidth.Truncate(hint, app.Width()))
	joined := strings.Join(rows, " ")
	if !strings.Contains(joined, "모델이") {
		t.Fatalf("ko seats explain must name the model; got %q", joined)
	}
	if strings.Contains(joined, "기본 프리셋에 없는") {
		t.Fatalf("ko seats explain still carries the inverted 'missing from the default preset' clause: %q", joined)
	}
}

// TestEditorExplainLocaleFitMatrix pins r11 F-01 delivery across the catalog:
// every presets.editor.explain.* string in every locale word-wraps into at
// most two 80-cell rows with no text loss (no continuation ellipsis), so the
// full sentence, empty-state clauses included, renders at the canonical
// 80-column geometry.
func TestEditorExplainLocaleFitMatrix(t *testing.T) {
	keys := []string{
		"name", "task", "n", "timeout_seconds", "tools", "max_turns", "max_native",
		"allow_hosts", "search_endpoint", "search_key_env", "out_dir", "root",
		"models", "seats", "json",
	}
	locales := []string{"en", "ko", "ja", "vi", "de", "fr", "it", "ru", "ar", "hi", "zh-Hans", "zh-Hant"}
	for _, loc := range locales {
		b := i18n.Load(loc)
		for _, key := range keys {
			full := "presets.editor.explain." + key
			s := b.T(full)
			rows := wrapExplainLines(s, 80, 2)
			if len(rows) == 0 || len(rows) > 2 {
				t.Fatalf("%s [%s]: wrap = %d rows, want 1-2", full, loc, len(rows))
			}
			for _, row := range rows {
				if cellwidth.String(row) > 80 {
					t.Fatalf("%s [%s]: row wider than 80 cells: %q", full, loc, row)
				}
				// r12 F-03 invariant, locale-independent: a wrapped row never
				// begins with a detached combining mark.
				if r := leadRune(row); r != 0 && isMark(r) {
					t.Fatalf("%s [%s]: row begins with a combining mark %q: %q", full, loc, r, row)
				}
			}
			// Seam check: an unspaced CJK/RTL run wraps mid-word, so compare
			// with all whitespace removed; any real text loss (a truncation
			// ellipsis the budget should have made unnecessary) fails here.
			flat := func(v string) string { return strings.ReplaceAll(v, " ", "") }
			if got, want := flat(strings.Join(rows, "")), flat(strings.Join(strings.Fields(s), "")); got != want {
				t.Fatalf("%s [%s]: wrap lost text (truncation without the matrix budget):\n  got  %q\n  want %q", full, loc, strings.Join(rows, "\n  "), s)
			}
			for _, row := range rows {
				if strings.Contains(row, "…") {
					t.Fatalf("%s [%s]: unexpected continuation ellipsis in a fully-fitting string: %q", full, loc, row)
				}
			}
		}
	}
}

// TestEditorExplainLineArabicNoBlank pins r11 F-05 (P3): when the wrap must
// cut an RTL string, the row degrades to a first-N-rune cut with a
// continuation ellipsis instead of rendering blank. (The original r11 defect
// was cellwidth.Truncate's retired mid-RTL refusal, removed in P9; the pin
// now guards the wrap path's own never-blank contract.)
func TestEditorExplainLineArabicNoBlank(t *testing.T) {
	app, scr := editorAppWithLang(t, "ar")
	app.Resize(40, 24)
	scr.zone = editorZoneB
	scr.formB.SetCursor(0) // arm search_endpoint
	ar := i18n.Load("ar")
	want := wrapExplainLines(ar.T("presets.editor.explain.search_endpoint"), 40, 2)
	if len(want) != 2 {
		t.Fatalf("ar 40-col wrap = %d rows, want 2", len(want))
	}
	for i, row := range want {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("ar explain row %d blanked at 40 cols (the RTL Truncate hole)", i)
		}
	}
	if !strings.HasSuffix(want[1], "…") {
		t.Fatalf("ar explain second row must end with the continuation ellipsis: %q", want[1])
	}
	lines := app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return l == want[0] }) ||
		!frameLineMatches(lines, func(l string) bool { return l == want[1] }) {
		t.Fatalf("ar 40-col explain rows missing from the frame:\n%s", strings.Join(lines, "\n"))
	}
}

// leadRune returns the first rune of s, or 0 for an empty string.
func leadRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// TestWrapCutMarkSafe pins r12 F-03: a mid-run cut never detaches a
// combining mark from its base consonant. The synthetic word is a chain of
// Devanagari base+matra pairs (क + ि, category Mc, width 1 - the class the
// zero-width tables do not protect), long enough that chunk-split and the
// mid-run fill both have to cut inside the chain at odd row widths.
func TestWrapCutMarkSafe(t *testing.T) {
	word := strings.Repeat("कि", 60) // 120 cells, no spaces, no ASCII
	// Odd row width forces cut boundaries onto base|mark joints.
	rows := wrapExplainLines(word, 7, 30)
	if len(rows) < 2 {
		t.Fatalf("expected chunk-split rows, got %d: %q", len(rows), rows)
	}
	joined := ""
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("row %d blanked", i)
		}
		if r := leadRune(row); isMark(r) {
			t.Fatalf("row %d begins with a detached combining mark %q: %q", i, r, row)
		}
		joined += row
	}
	if joined != word {
		t.Fatalf("mid-run cut lost text: got %d runes, want %d", len([]rune(joined)), len([]rune(word)))
	}
	// Ellipsis path: the row budget cuts the chain short and still never
	// hands a mark to the next row.
	short := wrapExplainLines(word, 7, 2)
	if len(short) != 2 || !strings.HasSuffix(short[1], "…") {
		t.Fatalf("ellipsis wrap = %q, want 2 rows with a trailing ellipsis", short)
	}
	for i, row := range short {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("ellipsis row %d blanked", i)
		}
		if r := leadRune(row); isMark(r) {
			t.Fatalf("ellipsis row %d begins with a detached combining mark: %q", i, row)
		}
	}
}

// TestWrapCutMarkSafeLeadingCluster covers the branch where the base+mark
// pair starts the string: the cluster stays whole instead of blanking the
// row or detaching the mark.
func TestWrapCutMarkSafeLeadingCluster(t *testing.T) {
	word := "किकि" + strings.Repeat("स", 30) // leading pairs, then long base run
	rows := wrapExplainLines(word, 7, 30)
	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			t.Fatalf("row %d blanked", i)
		}
		if r := leadRune(row); isMark(r) {
			t.Fatalf("row %d begins with a detached combining mark: %q", i, row)
		}
	}
}

// --- P2: the seed row editor is a normal editor (contract A5/A5b) ----------

// TestEditorSeedExistingRecordEditPersists pins the A5 present-seed path:
// the editor loads the seed from the store like any record, non-empty fields
// label the preset rung and empty fields still show the engine value with
// the built-in label, a seat edit persists to disk next to the untouched
// fields, and the record reloads with exactly what the user set.
func TestEditorSeedExistingRecordEditPersists(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: fanout.BuiltinPresetName, Task: "security", Seats: "native:a"},
	}})
	app, scr := newEditorApp(fanout.BuiltinPresetName)
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{fanout.BuiltinPresetName, "security", "native:a", "saved preset", "built-in"} {
		if !strings.Contains(out, want) {
			t.Fatalf("seed editor frame missing %q; frames:\n%s", want, out)
		}
	}
	// Source labels: set fields are the preset rung, unset fields stay
	// engine-supplied ("built-in"); max_native keeps the state.json rung.
	src := scr.sources()
	if src["task"] != "preset_saved" || src["seats"] != "preset_saved" {
		t.Fatalf("seed set-field sources = task:%q seats:%q, want the preset rung", src["task"], src["seats"])
	}
	for _, k := range []string{"n", "timeout_seconds", "tools", "max_turns", "out_dir", "root", "json"} {
		if src[k] != "builtin" {
			t.Fatalf("seed source[%s] = %q, want builtin for an unset field", k, src[k])
		}
	}
	if src["max_native"] != "state" {
		t.Fatalf("seed source[max_native] = %q, want the state.json rung", src["max_native"])
	}

	// Edit the lineup and save: both seats persist, the untouched fields
	// (task included) stay exactly as stored.
	scr.appendSeat("nw/m2")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil {
		t.Fatalf("reload presets: %v", err)
	}
	if len(pf.Presets) != 1 {
		t.Fatalf("seed save changed the record count: %+v", pf.Presets)
	}
	seed := pf.Presets[0]
	if seed.Name != fanout.BuiltinPresetName || seed.Seats != "native:a,nw/m2" || seed.Task != "security" {
		t.Fatalf("seed record after edit = %+v", seed)
	}
	if seed.N != 0 || seed.Tools != "" || seed.MaxTurns != 0 || seed.MaxNative != 0 ||
		seed.OutDir != "" || seed.Root != "" || seed.JSON {
		t.Fatalf("untouched seed fields must stay empty: %+v", seed)
	}

	// Reload into a fresh editor: the record round-trips.
	again := NewPresetEditorScreen(fanout.BuiltinPresetName).(*presetEditorScreen)
	if again.preset == nil || again.formA.Value(1) != "security" || len(again.seatVals) != 2 {
		t.Fatalf("seed editor reload = preset:%+v task:%q seats:%v", again.preset, again.formA.Value(1), again.seatVals)
	}
}

// TestEditorRenameIntoReservedNameRefused pins the A5b ordinary door: a user
// preset's save (new, edit, or rename) carrying the reserved name is refused
// by fanout.ValidatePresetForSave, the message names the rule without
// echoing the operand, and nothing is written.
func TestEditorRenameIntoReservedNameRefused(t *testing.T) {
	path := editorHome(t)
	app, _ := newEditorApp("")
	keys := append(typePresetRunes(fanout.BuiltinPresetName), KeyCtrlS)
	app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(app.Message(), "reserved") {
		t.Fatalf("reserved-name refusal message = %q", app.Message())
	}
	if strings.Contains(app.Message(), fanout.BuiltinPresetName) {
		t.Fatalf("the refusal echoed the reserved operand: %q", app.Message())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused save wrote the preset file: %v", err)
	}
}

// TestEditorSeedNameFieldFixed pins the A5 name rule on the seed row: the
// name field is fixed to the reserved identity (text-editing keys are
// swallowed, so a rename-out can never orphan a second preset), and the seed
// editor otherwise behaves like a normal record: a seat edit dirties it and
// arms the dirty guard.
func TestEditorSeedNameFieldFixed(t *testing.T) {
	path := editorHome(t)
	seedEditorPresets(t, path, &fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: fanout.BuiltinPresetName, Seats: "native:a"},
	}})
	app, scr := newEditorApp(fanout.BuiltinPresetName)
	// The editor opens on the name field: typing and deleting are swallowed.
	app.CaptureRun(&ScriptedKeys{Keys: append(typePresetRunes("hacked"), KeyBackspace, KeyBackspace)})
	if got := scr.formA.Value(0); got != fanout.BuiltinPresetName {
		t.Fatalf("the seed name field moved: %q", got)
	}
	if scr.dirty() {
		t.Fatal("swallowed name keys must not dirty the editor")
	}
	// A real edit arms the dirty guard like any record.
	scr.appendSeat("nw/m2")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyLeft}})
	if app.Modal() == nil {
		t.Fatal("a dirty seed editor must arm the discard guard like a normal record")
	}
}

// TestEditorSeedTitleRendersNameByteIdentical is the A7 editor-title sink:
// the title renders the reserved name through the slot authority
// byte-identically, and the same authority still masks the planted
// credential class (the title's masking half is the one function at both
// sinks).
func TestEditorSeedTitleRendersNameByteIdentical(t *testing.T) {
	editorHome(t)
	scr := NewPresetEditorScreen(fanout.BuiltinPresetName).(*presetEditorScreen)
	if got := scr.Title(); got != fanout.BuiltinPresetName {
		t.Fatalf("seed editor title = %q, want the reserved name byte-identical", got)
	}
	if mask := presetNameOperand(testfixtures.SKLive); mask == testfixtures.SKLive {
		t.Fatal("the title's slot authority lost the planted credential class")
	}
}

// The retired preset `models` key surfaces in the TUI in the USER'S LANGUAGE:
// the store detects the key and exposes the record names (PresetsFile.LegacyModels)
// while its English line goes to the discard sink the screen wires, so the editor
// and the presets list each show the localized catalog sentence exactly once,
// naming the record.
func TestEditorAndListRenderLocalizedRetiredModelsNotice(t *testing.T) {
	path := editorHome(t)
	raw := []byte(`{"presets":[{"name":"legacy","n":2,"models":"prov/m1"}],"default_preset":"legacy"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	want := fillHubSlots(missingOr(Bundle(), "presets.notice.models_removed", presetsNoticeModelsRemoved),
		map[string]string{"name": "legacy"})
	if !strings.Contains(want, "legacy") || strings.Contains(want, "{{name}}") {
		t.Fatalf("localized sentence did not fill its slot: %q", want)
	}

	// The editor shows it in the frame (and copies it onto the message line once).
	app, scr := newEditorApp("legacy")
	out := app.CaptureRun(&ScriptedKeys{})
	if len(scr.dropNotices) != 1 || scr.dropNotices[0] != want {
		t.Fatalf("editor drop notices = %q; want the one localized advisory", scr.dropNotices)
	}
	if !strings.Contains(out, want) && !strings.Contains(app.Message(), want) {
		t.Fatalf("editor frame/message lost the advisory %q:\n%s", want, out)
	}
	if strings.Contains(out, "ignoring the stored value") {
		t.Fatalf("the store's English twin must be discarded, not doubled:\n%s", out)
	}

	// The presets list renders the same sentence for the same record.
	st := editorStore(t, path)
	listOut, _ := presetsKeys(t, st, nil)
	if !strings.Contains(listOut, want) {
		t.Fatalf("presets list lost the advisory %q:\n%s", want, listOut)
	}
	if strings.Contains(listOut, "ignoring the stored value") {
		t.Fatalf("the presets list must not double the advisory:\n%s", listOut)
	}
}
