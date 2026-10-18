package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/swarm"
)

// settingsTestCore is an in-memory Core harness: every seam is faked, so no
// live file, registry, network probe, or persistent store can be touched.
type settingsTestCore struct {
	core     *settings.Core
	state    *mode.State
	reg      *providers.Registry
	saved    []*mode.State
	switched string
	restored bool
	added    []settings.ProviderSpec
	renamed  [][2]string
	deleted  []string
	tested   []string
	presets  []swarm.SavedPreset
	fixed    string
	rolled   string
}

// testPresetStore is the in-memory swarm.Store behind the harness.
type testPresetStore struct{ h *settingsTestCore }

func (s testPresetStore) LoadPresets() ([]swarm.SavedPreset, error) { return s.h.presets, nil }
func (s testPresetStore) SavePresets(p []swarm.SavedPreset) error {
	s.h.presets = p
	return nil
}

// newSettingsTestCore builds the harness over a temp AGY_PRO_HOME and
// guaranteed-unset operand env vars.
func newSettingsTestCore(t *testing.T) *settingsTestCore {
	t.Helper()
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	t.Setenv("AGY_PRO_REAL_AGY", "")
	t.Setenv("AGY_LAUNCHER", "")
	t.Setenv("AGY_PRO_BRIDGE_TOOLS", "")
	unsetEnv(t, "AGY_P11_SET_KEY")

	h := &settingsTestCore{
		state: &mode.State{
			CurrentMode:    mode.ModeFull,
			DefaultProfile: mode.ProfilePrompt,
			ManagedItems:   append([]string(nil), mode.DefaultManagedItems...),
			UpdatedAt:      time.Now().UTC(),
		},
		reg: &providers.Registry{Providers: []providers.Provider{
			{
				Name:      "alpha",
				BaseURL:   "https://alpha.example.com",
				APIKeyEnv: "AGY_P11_SET_KEY",
				Models:    []providers.Model{{ID: "a1"}},
			},
		}},
	}
	h.core = settings.NewCore(settings.Deps{
		LoadState: func() (*mode.State, error) { return h.state, nil },
		SaveState: func(st *mode.State) error {
			cp := *st
			h.saved = append(h.saved, &cp)
			h.state = &cp
			return nil
		},
		LoadReg: func() (*providers.Registry, error) { return h.reg, nil },
		SaveReg: func(reg *providers.Registry) error {
			h.reg = reg
			return h.persistReg(reg)
		},
		AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
			if len(reg.Providers) > 0 {
				h.tested = append(h.tested, reg.Providers[0].Name)
			}
			return []providers.KeyAudit{{
				Provider: "alpha",
				Env:      "AGY_P11_SET_KEY",
				Resolved: true,
				Probe:    providers.KeyProbeResult{Status: providers.ProbeValid},
			}}
		},
		Doctor: func(out io.Writer, strict bool) error {
			_, _ = io.WriteString(out, "[OK] doctor line\n")
			return nil
		},
		FixKeys:      func(target string) error { h.fixed = target; return nil },
		RollbackKeys: func(dir string) error { h.rolled = dir; return nil },
		SwitchMode:   func(target string, in io.Reader, out io.Writer) error { h.switched = target; return nil },
		RestoreMode:  func(out io.Writer) error { h.restored = true; return nil },
		VersionFunc:  func() string { return "test-version" },
		SwarmStore:   testPresetStore{h},
	})
	if err := h.persistReg(h.reg); err != nil {
		t.Fatalf("seed temp providers.json: %v", err)
	}
	return h
}

// persistReg mirrors the in-memory registry into the temp AGY_PRO_HOME
// providers.json. The screen's pre-write approval diff reads the live file
// (the same source as the Core's default LoadReg seam), so the file must
// carry the harness state for the before/after pins to be meaningful.
func (h *settingsTestCore) persistReg(reg *providers.Registry) error {
	path, err := providers.DefaultPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return providers.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

// regFileBytes reads the temp providers.json for the no-write-without-
// approval byte pins.
func regFileBytes(t *testing.T) []byte {
	t.Helper()
	path, err := providers.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// unsetEnv guarantees the named variable resolves empty for the test
// duration (process env and, by the unique name, persistent stores).
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	if v, ok := os.LookupEnv(name); ok {
		os.Unsetenv(name)
		t.Cleanup(func() { _ = os.Setenv(name, v) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(name) })
	}
}

// typeKeys feeds one rune key per character of s.
func typeKeys(s string) []Key {
	var keys []Key
	for _, r := range s {
		keys = append(keys, RuneKey(r))
	}
	return keys
}

// newSettingsApp pushes a fresh settings screen over the harness core.
func newSettingsApp(h *settingsTestCore) *App {
	app := NewApp(110, 44, DirLTR)
	app.Push(NewSettingsScreen(h.core))
	return app
}

// TestSettingsViewMsgIDAndEnByteIdentity is the plan-v2 section 3 golden
// test: for lang=en the catalog-resolved label and note of EVERY settings
// row equal the compiled view.go literals byte-for-byte, no row renders an
// em-dash, and every row carries its MsgID join (Item ID, "provider.key"
// for the dynamic provider-key rows).
func TestSettingsViewMsgIDAndEnByteIdentity(t *testing.T) {
	h := newSettingsTestCore(t)
	view, err := h.core.View()
	if err != nil {
		t.Fatalf("View(): %v", err)
	}
	b := i18n.Load("en")
	items := 0
	for _, sec := range view.Sections {
		for _, it := range sec.Items {
			items++
			if it.MsgID == "" {
				t.Errorf("item %q: MsgID not set", it.ID)
			}
			if strings.HasPrefix(it.ID, "provider.key.") {
				if it.MsgID != "provider.key" {
					t.Errorf("provider-key row %q: MsgID = %q; want provider.key", it.ID, it.MsgID)
				}
			} else if it.MsgID != it.ID {
				t.Errorf("item %q: MsgID = %q; want the Item ID", it.ID, it.MsgID)
			}
			label, note := localizeItem(b, it)
			if label != it.Label {
				t.Errorf("item %q: catalog label %q != compiled literal %q", it.ID, label, it.Label)
			}
			if note != it.Note {
				t.Errorf("item %q: catalog note %q != compiled literal %q", it.ID, note, it.Note)
			}
			if strings.ContainsRune(label, '—') || strings.ContainsRune(note, '—') {
				t.Errorf("item %q: em-dash in resolved label/note", it.ID)
			}
		}
	}
	if items < 60 {
		t.Fatalf("View() rendered %d items; want the full 62-row surface", items)
	}
}

// TestLocalizeItemFallback pins the compiled-default fallback: an unknown
// MsgID (or none) resolves to the item's own English Label/Note, never to
// the key ID and never to an empty string.
func TestLocalizeItemFallback(t *testing.T) {
	b := i18n.Load("en")
	it := settings.Item{ID: "nope.row", MsgID: "nope.row", Label: "Literal L", Note: "Literal N"}
	label, note := localizeItem(b, it)
	if label != "Literal L" || note != "Literal N" {
		t.Errorf("fallback = %q/%q; want compiled literals", label, note)
	}
	it.MsgID = ""
	label, note = localizeItem(b, it)
	if label != "Literal L" || note != "Literal N" {
		t.Errorf("empty MsgID fallback = %q/%q; want compiled literals", label, note)
	}
}

// TestSettingsScreenRenderAndSectionSwitch drives the screen frames:
// section header, item rows with value and source in the note column, the
// cursor detail block, and Left/Right section switching.
func TestSettingsScreenRenderAndSectionSwitch(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	frame := app.Frame().String()
	for _, want := range []string{
		"Settings",
		"[1] Launch & Profile  (1/8)",
		"Default Launch Profile",
		"prompt  (state)",
		"value   : prompt",
		"note    : prompt = fast launcher on plain agy",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q\n%s", want, frame)
		}
	}

	app.Step(KeyRight)
	frame = app.Frame().String()
	if !strings.Contains(frame, "[2] Models & Providers  (2/8)") {
		t.Errorf("Right did not switch to section 2\n%s", frame)
	}
	if !strings.Contains(frame, "Key status - alpha") {
		t.Errorf("dynamic provider-key row missing ({{name}} slot join)\n%s", frame)
	}

	// Danger + read-only markers ride through to the detail block: move the
	// cursor onto providers.delete (row 6) without activating it.
	for i := 0; i < 5; i++ {
		app.Step(KeyDown)
	}
	frame = app.Frame().String()
	if !strings.Contains(frame, "danger  : y/n confirmation required") {
		t.Errorf("danger marker missing on providers.delete\n%s", frame)
	}
}

// TestSettingsScreenChoiceSet activates a plain choice item: the M2 overlay
// opens, a digit pick writes through Core.Set, and the reloaded frame shows
// the new value.
func TestSettingsScreenChoiceSet(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(Digit(1)) // default_profile
	if app.Modal() == nil {
		t.Fatal("choice overlay did not open")
	}
	app.Step(Digit(2)) // native
	if len(h.saved) != 1 || h.state.DefaultProfile != mode.ProfileNative {
		t.Fatalf("Core.Set not written: saved=%d profile=%q", len(h.saved), h.state.DefaultProfile)
	}
	if got := app.Message(); !strings.Contains(got, "[SUCCESS]") {
		t.Errorf("message = %q; want [SUCCESS]", got)
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "native  (state)") {
		t.Errorf("reloaded frame does not show the new value\n%s", frame)
	}
}

// TestSettingsScreenLauncherChoice drives the launcher toggle row: show/hide
// choices map onto the on/off values Core.Set persists.
func TestSettingsScreenLauncherChoice(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	for i := 0; i < 7; i++ { // p1 -> p8
		app.Step(KeyRight)
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "[8] Appearance  (8/8)") {
		t.Fatalf("not on the Appearance section\n%s", frame)
	}
	app.Step(Digit(2)) // launcher_menu
	if app.Modal() == nil {
		t.Fatal("choice overlay did not open")
	}
	app.Step(Digit(1)) // show -> on
	if len(h.saved) != 1 || h.state.LauncherMenu != mode.LauncherMenuOn {
		t.Fatalf("launcher_menu not persisted: saved=%d value=%q", len(h.saved), h.state.LauncherMenu)
	}
}

// TestSettingsScreenLanguageChoice drives the language row: endonym choices
// map back to catalog codes for Core.Set.
func TestSettingsScreenLanguageChoice(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	for i := 0; i < 7; i++ {
		app.Step(KeyRight)
	}
	app.Step(Digit(1)) // appearance.language
	if app.Modal() == nil {
		t.Fatal("choice overlay did not open")
	}
	app.Step(Digit(2)) // 简体中文 -> zh-Hans
	if len(h.saved) != 1 || h.state.Language != "zh-Hans" {
		t.Fatalf("language not persisted: saved=%d language=%q", len(h.saved), h.state.Language)
	}
}

// TestSettingsScreenDangerChoiceGate pins the danger flow for choice items:
// a mode-switch pick first gates behind the y/n confirm; n cancels without
// a write, y runs Core.ModeSwitch.
func TestSettingsScreenDangerChoiceGate(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(Digit(2)) // current_mode
	if app.Modal() == nil {
		t.Fatal("choice overlay did not open")
	}
	app.Step(Digit(2)) // clean
	if app.Modal() == nil {
		t.Fatal("danger gate did not open after the pick")
	}
	app.Step(KeyNo)
	if h.switched != "" {
		t.Fatalf("ModeSwitch ran on n: %q", h.switched)
	}

	app.Step(Digit(2))
	app.Step(Digit(2))
	app.Step(KeyYes)
	if h.switched != mode.ModeClean {
		t.Fatalf("ModeSwitch = %q; want clean", h.switched)
	}
	if got := app.Message(); !strings.Contains(got, "[SUCCESS]") {
		t.Errorf("message = %q; want [SUCCESS]", got)
	}
}

// TestSettingsScreenModeRestoreGate pins the direct danger confirm:
// mode.restore asks y/n and only y runs Core.ModeRestore.
func TestSettingsScreenModeRestoreGate(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(Digit(8)) // mode.restore
	if app.Modal() == nil {
		t.Fatal("confirm gate did not open")
	}
	app.Step(KeyEsc)
	if h.restored {
		t.Fatal("ModeRestore ran on Esc")
	}

	app.Step(Digit(8))
	app.Step(KeyYes)
	if !h.restored {
		t.Fatal("ModeRestore did not run on y")
	}
}

// TestSettingsScreenProviderTestPrompt drives a free-text item through the
// inline editor: typing a name and submitting runs Core.ProviderTest and
// reports the verdict.
func TestSettingsScreenProviderTestPrompt(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(KeyRight) // p2
	app.Step(Digit(4)) // providers.test
	if app.Top().Mode() != ModeEdit {
		t.Fatalf("prompt screen is not in edit mode")
	}
	for _, k := range append(typeKeys("alpha"), KeyCtrlS) {
		app.Step(k)
	}
	if len(h.tested) != 1 || h.tested[0] != "alpha" {
		t.Fatalf("ProviderTest = %v; want [alpha]", h.tested)
	}
	if got := app.Message(); !strings.Contains(got, "connection OK") {
		t.Errorf("message = %q; want the OK verdict", got)
	}
}

// TestSettingsScreenDeletePromptGate drives the provider delete flow: prompt
// for the name, then the M4 approval modal with the exact mutation summary.
// Until the approval answer providers.json must not change by a single byte;
// y is the only path to Core.ProviderDelete.
func TestSettingsScreenDeletePromptGate(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	before := regFileBytes(t)
	app.Step(KeyRight)
	app.Step(Digit(6)) // providers.delete
	for _, k := range append(typeKeys("alpha"), KeyCtrlS) {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("delete approval modal did not open after the prompt")
	}
	body := strings.Join(m.Body(110), "\n")
	for _, want := range []string{
		"Removes the registry entry for alpha only",
		"Key values and files are untouched",
		"Models before: 1, after: 0",
		"providers.json will be written (0600, atomic)",
		"- provider alpha (https://alpha.example.com, 1 model)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("approval body missing %q\n%s", want, body)
		}
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed before the approval answer")
	}
	app.Step(KeyNo)
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed on n")
	}
	if n := len(h.reg.Providers); n != 1 {
		t.Fatalf("ProviderDelete ran on n: registry holds %d providers", n)
	}
	if got := app.Message(); !strings.Contains(got, "Cancelled") {
		t.Errorf("message = %q; want the cancelled note", got)
	}

	app.Step(Digit(6))
	for _, k := range append(typeKeys("alpha"), KeyCtrlS) {
		app.Step(k)
	}
	app.Step(KeyYes)
	if n := len(h.reg.Providers); n != 0 {
		t.Fatalf("registry holds %d providers after y; want 0", n)
	}
	if got := app.Message(); !strings.Contains(got, "deleted") {
		t.Errorf("message = %q; want the deleted confirmation", got)
	}
}

// TestSettingsScreenAddJSONPrompt feeds a spec file path through the inline
// editor; the write opens the M4 approval modal and only y lands the
// provider through Core.ProviderAdd. Until the approval answer, providers.json
// must not change by a single byte.
func TestSettingsScreenAddJSONPrompt(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	spec := `{"name":"beta","baseURL":"https://beta.example.com","apiKeyEnv":"AGY_P11_SET_KEY","models":[{"id":"b1"}]}`
	path := t.TempDir() + string(os.PathSeparator) + "beta.json"
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}

	before := regFileBytes(t)
	app.Step(KeyRight)
	app.Step(Digit(3)) // providers.add_json
	for _, k := range append(typeKeys(path), KeyCtrlS) {
		app.Step(k)
	}
	if app.Modal() == nil {
		t.Fatal("add approval modal did not open after the prompt")
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed before the approval answer")
	}
	app.Step(KeyNo)
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed on n")
	}
	if findProvider(h.reg, "beta") != nil {
		t.Fatal("ProviderAdd landed on n")
	}
	if got := app.Message(); !strings.Contains(got, "Cancelled") {
		t.Errorf("message = %q; want the cancelled note", got)
	}

	// y approves: the write lands and the reloaded frame shows the count.
	app.Step(Digit(3))
	for _, k := range append(typeKeys(path), KeyCtrlS) {
		app.Step(k)
	}
	app.Step(KeyYes)
	if findProvider(h.reg, "beta") == nil {
		t.Fatalf("ProviderAdd did not land in the registry: %+v", h.reg.Providers)
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "2 registered") {
		t.Errorf("reloaded frame does not show the added provider\n%s", frame)
	}
}

// TestSettingsScreenAddJSONReplaceApproval pins the wholesale-replace hazard:
// a spec whose name matches a registered provider must render the before/
// after model counts AND both diff lines in the approval body, so a silent
// model-list wipe is visible before y. Esc writes nothing.
func TestSettingsScreenAddJSONReplaceApproval(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	spec := `{"name":"alpha","baseURL":"https://alpha2.example.com","apiKeyEnv":"AGY_P11_SET_KEY","models":[{"id":"a1"},{"id":"a2"}]}`
	path := t.TempDir() + string(os.PathSeparator) + "alpha.json"
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}

	before := regFileBytes(t)
	app.Step(KeyRight)
	app.Step(Digit(3)) // providers.add_json
	for _, k := range append(typeKeys(path), KeyCtrlS) {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("replace approval modal did not open")
	}
	body := strings.Join(m.Body(110), "\n")
	for _, want := range []string{
		"Replace provider alpha wholesale",
		"Models before: 1, after: 2",
		"providers.json will be written (0600, atomic)",
		"- provider alpha (https://alpha.example.com, 1 model)",
		"+ provider alpha (https://alpha2.example.com, 2 models)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("approval body missing %q\n%s", want, body)
		}
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed before the approval answer")
	}
	app.Step(KeyEsc)
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed on Esc")
	}
	if p := findProvider(h.reg, "alpha"); p == nil || len(p.Models) != 1 {
		t.Fatalf("registry changed on Esc: %+v", h.reg.Providers)
	}

	// y approves the replace: the new model list stands.
	app.Step(Digit(3))
	for _, k := range append(typeKeys(path), KeyCtrlS) {
		app.Step(k)
	}
	app.Step(KeyYes)
	p := findProvider(h.reg, "alpha")
	if p == nil || len(p.Models) != 2 {
		t.Fatalf("wholesale replace did not land: %+v", h.reg.Providers)
	}
}

// TestSettingsScreenSavePresetChainedPrompts drives both chained prompts of
// the save-preset item and the preset listing result screen.
func TestSettingsScreenSavePresetChainedPrompts(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	for i := 0; i < 3; i++ { // p1 -> p4
		app.Step(KeyRight)
	}
	app.Step(KeyEnd)   // last row: swarm.save_preset
	app.Step(KeyEnter) // prompt: preset name
	for _, k := range append(typeKeys("p9"), KeyCtrlS) {
		app.Step(k)
	}
	if app.Top().Mode() != ModeEdit {
		t.Fatalf("second prompt (numbers) did not open")
	}
	for _, k := range append(typeKeys("1,3"), KeyCtrlS) {
		app.Step(k)
	}
	if len(h.presets) != 1 || h.presets[0].Name != "p9" || h.presets[0].Numbers != "1,3" {
		t.Fatalf("presets = %+v; want p9=1,3", h.presets)
	}

	// swarm.presets is row 13 (index 12, past the digit range): digit 9
	// lands on row 9, then arrows + Enter reach it.
	app.Step(Digit(9))
	for i := 0; i < 4; i++ {
		app.Step(KeyDown)
	}
	app.Step(KeyEnter)
	if _, ok := app.Top().(*settingsResultScreen); !ok {
		t.Fatalf("preset listing did not push the result screen: %T", app.Top())
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "p9 = 1,3") {
		t.Errorf("preset listing missing the saved preset\n%s", frame)
	}
}

// TestSettingsScreenDiagnoseResult runs the doctor row into the read-only
// result screen.
func TestSettingsScreenDiagnoseResult(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	for i := 0; i < 4; i++ { // p1 -> p5
		app.Step(KeyRight)
	}
	app.Step(Digit(1)) // doctor.diagnose
	if _, ok := app.Top().(*settingsResultScreen); !ok {
		t.Fatalf("diagnose did not push the result screen: %T", app.Top())
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "[OK] doctor line") {
		t.Errorf("doctor output missing from the result screen\n%s", frame)
	}
}

// TestSettingsScreenBackupsResult covers the mode.backups listing on an
// empty backup dir (the honest "(no backups)" line).
func TestSettingsScreenBackupsResult(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(Digit(9)) // mode.backups
	if _, ok := app.Top().(*settingsResultScreen); !ok {
		t.Fatalf("mode.backups did not push the result screen: %T", app.Top())
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "(no backups)") {
		t.Errorf("backup listing missing the empty-state line\n%s", frame)
	}
}

// TestSettingsScreenReadOnlyMessage pins the read-only activation path: a
// read-only row only sets the honest message, no modal, no write.
func TestSettingsScreenReadOnlyMessage(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(KeyRight)
	app.Step(Digit(7)) // models.inventory (read-only)
	if app.Modal() != nil {
		t.Fatal("read-only row opened a modal")
	}
	if got := app.Message(); !strings.HasPrefix(got, "read-only: ") {
		t.Errorf("message = %q; want the read-only note", got)
	}
}

// TestSettingsScreenWizardRow pins the interactive-wizard row: it points at
// the CLI wizard and never writes.
func TestSettingsScreenWizardRow(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(KeyRight)
	app.Step(Digit(2)) // providers.add_interactive
	if got := app.Message(); !strings.Contains(got, "agy pro setup") {
		t.Errorf("message = %q; want the wizard pointer", got)
	}
	if len(h.saved) != 0 || len(h.added) != 0 {
		t.Error("wizard row wrote state or registry")
	}
}

// TestSettingsScreenFrameHasNoEmDash scans every rendered frame of a walk
// over all sections for the banned character.
func TestSettingsScreenFrameHasNoEmDash(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)
	for i := 0; i < 8; i++ {
		if strings.ContainsRune(app.Frame().String(), '—') {
			t.Fatalf("section %d frame renders an em-dash", i+1)
		}
		app.Step(KeyRight)
	}
}

// TestSettingsScreenRenameApprovalGate pins the rename flow: both prompts,
// then the M4 approval modal with the carried-over model count and the
// removed/added diff lines. n writes nothing; y renames through
// Core.ProviderRename.
func TestSettingsScreenRenameApprovalGate(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	before := regFileBytes(t)
	app.Step(KeyRight)
	app.Step(Digit(5)) // providers.rename
	for _, k := range append(typeKeys("alpha"), KeyCtrlS) {
		app.Step(k)
	}
	for _, k := range append(typeKeys("beta"), KeyCtrlS) {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("rename approval modal did not open")
	}
	body := strings.Join(m.Body(110), "\n")
	for _, want := range []string{
		"Rename provider alpha to beta",
		"Models before: 1, after: 1",
		"providers.json will be written (0600, atomic)",
		"- provider alpha (https://alpha.example.com, 1 model)",
		"+ provider beta (https://alpha.example.com, 1 model)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("approval body missing %q\n%s", want, body)
		}
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed before the approval answer")
	}
	app.Step(KeyNo)
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed on n")
	}
	if findProvider(h.reg, "alpha") == nil {
		t.Fatal("rename landed on n")
	}

	// y approves: the rename lands with the model list carried over.
	app.Step(Digit(5))
	for _, k := range append(typeKeys("alpha"), KeyCtrlS) {
		app.Step(k)
	}
	for _, k := range append(typeKeys("beta"), KeyCtrlS) {
		app.Step(k)
	}
	app.Step(KeyYes)
	p := findProvider(h.reg, "beta")
	if p == nil || len(p.Models) != 1 || p.Models[0].ID != "a1" {
		t.Fatalf("rename did not land: %+v", h.reg.Providers)
	}
	if findProvider(h.reg, "alpha") != nil {
		t.Fatal("old provider name still registered after y")
	}
}

// TestSettingsScreenFixKeysApprovalGate pins the doctor fix-keys gate: the
// prompt answer opens the M4 approval with the resolved target diff, and only
// y runs Core.DoctorFixKeys.
func TestSettingsScreenFixKeysApprovalGate(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	before := regFileBytes(t)
	for i := 0; i < 4; i++ { // p1 -> p5
		app.Step(KeyRight)
	}
	app.Step(Digit(2)) // doctor.fix_keys
	for _, k := range append(typeKeys("all"), KeyCtrlS) {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("fix-keys approval modal did not open")
	}
	body := strings.Join(m.Body(110), "\n")
	for _, want := range []string{
		"Repair BOM-variant key names for all",
		"Models before: 1, after: 1",
		"providers.json is not modified",
		"AGY_P11_SET_KEY (provider alpha)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("approval body missing %q\n%s", want, body)
		}
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed before the approval answer")
	}
	app.Step(KeyNo)
	if h.fixed != "" {
		t.Fatalf("DoctorFixKeys ran on n: %q", h.fixed)
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed on n")
	}

	app.Step(Digit(2))
	for _, k := range append(typeKeys("all"), KeyCtrlS) {
		app.Step(k)
	}
	app.Step(KeyYes)
	if h.fixed != "all" {
		t.Fatalf("DoctorFixKeys = %q; want all", h.fixed)
	}
}

// TestSettingsScreenRollbackKeysApprovalGate pins the doctor rollback-keys
// gate: the backup entries render as the exact diff (restore/delete, names
// masked) and only y runs Core.DoctorRollbackKeys.
func TestSettingsScreenRollbackKeysApprovalGate(t *testing.T) {
	h := newSettingsTestCore(t)

	// A minimal key backup the approval can diff (names and existence only).
	dir := t.TempDir()
	backup := `{"version":2,"createdAt":"2026-01-01T00:00:00Z","tool":"agypro/internal/providers.RepairWindowsKeyNames","entries":[{"root":"user","name":"FOO_API_KEY","existed":true},{"root":"user","name":"BAR_API_KEY","existed":false}]}`
	if err := os.WriteFile(dir+string(os.PathSeparator)+"registry-keys.v2.json", []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}

	app := newSettingsApp(h)
	before := regFileBytes(t)
	for i := 0; i < 4; i++ { // p1 -> p5
		app.Step(KeyRight)
	}
	app.Step(Digit(3)) // doctor.rollback_keys
	for _, k := range append(typeKeys(dir), KeyCtrlS) {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("rollback approval modal did not open")
	}
	body := strings.Join(m.Body(110), "\n")
	for _, want := range []string{
		"Restore registry key names from backup",
		"Models before: 1, after: 1",
		"No file is written",
		"FOO_API_KEY: restore recorded value",
		"- BAR_API_KEY: delete (recorded as absent)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("approval body missing %q\n%s", want, body)
		}
	}
	if !bytes.Equal(regFileBytes(t), before) {
		t.Fatal("providers.json changed before the approval answer")
	}
	app.Step(KeyEsc)
	if h.rolled != "" {
		t.Fatalf("DoctorRollbackKeys ran on Esc: %q", h.rolled)
	}

	app.Step(Digit(3))
	for _, k := range append(typeKeys(dir), KeyCtrlS) {
		app.Step(k)
	}
	app.Step(KeyYes)
	if h.rolled != dir {
		t.Fatalf("DoctorRollbackKeys = %q; want %q", h.rolled, dir)
	}
}
