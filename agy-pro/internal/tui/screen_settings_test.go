package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"agypro/internal/cellwidth"
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
	// probe overrides the audit seam's verdict when non-nil (the fix7
	// probe-failure pin needs an invalid verdict with a detail).
	probe *providers.KeyProbeResult
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
			probe := providers.KeyProbeResult{Status: providers.ProbeValid}
			if h.probe != nil {
				probe = *h.probe
			}
			return []providers.KeyAudit{{
				Provider: "alpha",
				Env:      "AGY_P11_SET_KEY",
				Resolved: true,
				Probe:    probe,
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

// newSettingsAppSized pushes a fresh settings screen over the harness core at
// an explicit frame geometry. The approval-bound pins need the standard 80x24
// console frame, which is smaller than the 110x44 default harness frame.
func newSettingsAppSized(h *settingsTestCore, w, ht int) *App {
	app := NewApp(w, ht, DirLTR)
	app.Push(NewSettingsScreen(h.core))
	return app
}

// seedDoctorProviders replaces the harness registry with n providers, each
// declaring its own API key variable, so the fix-keys target "all" resolves n
// per-entry diff lines. The Core seam and the on-disk providers.json the
// approval diff reads are updated together.
func (h *settingsTestCore) seedDoctorProviders(t *testing.T, n int) {
	t.Helper()
	reg := &providers.Registry{Providers: make([]providers.Provider, 0, n)}
	for i := 1; i <= n; i++ {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name:      fmt.Sprintf("p%02d", i),
			BaseURL:   fmt.Sprintf("https://p%02d.example.com", i),
			APIKeyEnv: fmt.Sprintf("AGY_P11_SET_%02d", i),
			Models:    []providers.Model{{ID: fmt.Sprintf("m%02d", i)}},
		})
	}
	h.reg = reg
	if err := h.persistReg(reg); err != nil {
		t.Fatalf("seed %d providers: %v", n, err)
	}
}

// seedRollbackBackup writes one key backup (the on-disk shape the rollback
// gate diffs) and returns its directory. names[i] is entry i+1 and existing[i]
// says whether it restores (true) or deletes (false).
func seedRollbackBackup(t *testing.T, names []string, existing []bool) string {
	t.Helper()
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"root":"user","name":%q,"existed":%t}`, n, existing[i])
	}
	backup := `{"version":2,"createdAt":"2026-01-01T00:00:00Z","tool":"agypro/internal/providers.RepairWindowsKeyNames","entries":[` + b.String() + `]}`
	dir := t.TempDir()
	if err := os.WriteFile(dir+string(os.PathSeparator)+"registry-keys.v2.json", []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openFixKeysGate drives the settings screen to the doctor section, activates
// the fix-keys row, answers the target prompt with target, and returns the
// live modal. app must already carry the settings screen.
func openFixKeysGate(t *testing.T, app *App, target string) Modal {
	t.Helper()
	for i := 0; i < 4; i++ { // p1 -> p5
		app.Step(KeyRight)
	}
	app.Step(Digit(2)) // doctor.fix_keys
	for _, k := range append(typeKeys(target), KeyCtrlS) {
		app.Step(k)
	}
	m := app.Modal()
	if m == nil {
		t.Fatal("fix-keys approval modal did not open")
	}
	return m
}

// openRollbackGate is the rollback-keys counterpart of openFixKeysGate.
func openRollbackGate(t *testing.T, app *App, dir string) Modal {
	t.Helper()
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
	return m
}

// gateFrame asserts the shared gate frame contract and returns the composed
// frame with the gate block bounds. The gate block must be CONTIGUOUS and
// COMPLETE (its first line is the gate title, its last line is the gate
// legend, and it carries every body line the modal reports), the frame must
// fit the height, and the shared footer must be the last frame line: those
// four together are "nothing was cut off-frame".
func gateFrame(t *testing.T, app *App, m Modal) (Frame, int, int) {
	t.Helper()
	c := app.ComposeFrame(nil)
	f := c.Frame
	if len(f.Lines) > app.Height() {
		t.Fatalf("gate frame = %d rows, want at most %d:\n%s", len(f.Lines), app.Height(), f.String())
	}
	first, last, rows := -1, -1, 0
	for i, k := range c.Kinds {
		if k != BlockModal {
			continue
		}
		if first < 0 {
			first = i
		}
		rows++
		if last >= 0 && i != last+1 {
			t.Fatalf("gate block is not contiguous at row %d:\n%s", i, f.String())
		}
		last = i
	}
	if first < 0 {
		t.Fatalf("no gate block in the frame:\n%s", f.String())
	}
	body := m.Body(app.Width())
	if want := len(body) + 2; rows != want {
		t.Fatalf("gate block rows = %d, want %d (title + %d body lines + legend):\n%s", rows, want, len(body), f.String())
	}
	if f.Lines[first] != m.Title() || f.Lines[last] != m.Footer() {
		t.Fatalf("gate block is cut: first %q, last %q:\n%s", f.Lines[first], f.Lines[last], f.String())
	}
	if got := f.Lines[len(f.Lines)-1]; got != baseFooter {
		t.Fatalf("shared footer missing: last frame line = %q:\n%s", got, f.String())
	}
	return f, first, last
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

// TestSettingsScreenAddProviderGuidance pins the reworded provider-add
// guidance (audit gap G1). The old message pointed at `agy pro setup`, which on
// a console host opens this very TUI, so following the row sent the user in a
// circle. Moved pin: the row now names the in-TUI add path as the primary route
// and keeps the classic wizard as the documented terminal alternative, and it
// still never writes.
func TestSettingsScreenAddProviderGuidance(t *testing.T) {
	// The guidance is English chrome (decision 6B) and its catalog key
	// resolves in all twelve locales, so bind en explicitly: the assertions
	// below quote the English sentence and must stay deterministic.
	SetLanguage("en")
	h := newSettingsTestCore(t)
	app := newSettingsApp(h)

	app.Step(KeyRight)
	app.Step(Digit(2)) // providers.add_interactive
	got := app.Message()

	const tuiPath = "Hub > Providers > Add provider"
	if !strings.Contains(got, tuiPath) {
		t.Fatalf("message = %q; want the in-TUI add path %q named", got, tuiPath)
	}
	for _, want := range []string{
		"Terminal alternative:",
		"`agy pro setup --wizard`",
		"`agy pro settings providers add-interactive`",
		"Raw key echo is masked first-5 + ****",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message = %q; missing %q", got, want)
		}
	}
	// The in-TUI route is the primary clause, so it precedes the terminal
	// alternative; and the circular form (plain `agy pro setup`, the command
	// that opens this TUI on a console host) is never named as the route.
	if strings.Index(got, tuiPath) > strings.Index(got, "Terminal alternative:") {
		t.Errorf("message = %q; want the in-TUI path before the terminal alternative", got)
	}
	if strings.Contains(got, "run `agy pro setup`") {
		t.Errorf("message = %q; still routes to the circular command", got)
	}
	if len(h.saved) != 0 || len(h.added) != 0 {
		t.Error("guidance row wrote state or registry")
	}
}

// TestSettingsScreenAddProviderGuidanceFitsFrame pins the width behavior of the
// new guidance at the standard 80x24 console frame: the message is one line, so
// the shared composer clamps it there (no composed line over the frame width and
// no wrapped row), and the in-TUI clause survives the cut because it comes
// first.
func TestSettingsScreenAddProviderGuidanceFitsFrame(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsAppSized(h, 80, 24)

	app.Step(KeyRight)
	app.Step(Digit(2)) // providers.add_interactive
	full := app.Message()
	if cells := cellwidth.String(full); cells <= app.Width() {
		t.Fatalf("guidance = %d cells; want an over-wide line the composer clamps", cells)
	}
	clamped := cellwidth.Truncate(full, app.Width())
	if !strings.Contains(clamped, "Hub > Providers > Add provider") {
		t.Fatalf("clamped guidance = %q; the in-TUI clause must survive the cut", clamped)
	}
	frame := app.Frame()
	if !frameHasLine(frame.String(), clamped) {
		t.Fatalf("frame carries no clamped guidance line %q:\n%s", clamped, frame.String())
	}
	for i, line := range frame.Lines {
		if cells := cellwidth.String(line); cells > app.Width() {
			t.Fatalf("composed line %d = %d cells, want <= %d: %q", i, cells, app.Width(), line)
		}
	}
}

// TestSettingsScreenAddProviderGuidancePlainTier pins the render policy: the
// guidance is plain text, so a plain-tier paint of the frame (the NO_COLOR and
// VT-enable-failure console path) emits zero escape bytes.
func TestSettingsScreenAddProviderGuidancePlainTier(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsAppSized(h, 80, 24)

	app.Step(KeyRight)
	app.Step(Digit(2)) // providers.add_interactive

	var out bytes.Buffer
	p := NewPainter(&out, plainTestCap())
	if code := paintLoop(app, &ScriptedKeys{}, p, nil); code != 0 {
		t.Fatalf("plain-tier paint exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain-tier paint emitted escape bytes:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "Hub > Providers > Add provider") {
		t.Fatalf("plain-tier paint lost the guidance line:\n%q", out.String())
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

// TestCapApprovalEntries pins the tail arithmetic of the bounded approval
// summary at the unit level: an exact fit keeps every entry and adds nothing,
// one entry over the budget keeps the leading lines plus ONE tail line naming
// the exact remainder, and a budget that cannot hold a single entry still
// renders the tail (an honest empty diff) instead of entries it has no room
// for.
func TestCapApprovalEntries(t *testing.T) {
	entries := []string{"e1", "e2", "e3", "e4", "e5"}
	tail := func(hidden int) string {
		return fmt.Sprintf("  ... %d more entries NOT shown (the diff above is partial)", hidden)
	}
	cases := []struct {
		name   string
		fixed  int
		budget int
		want   []string
	}{
		{"exact fit", 3, 8, []string{"e1", "e2", "e3", "e4", "e5"}},
		{"one over budget", 3, 7, []string{"e1", "e2", "e3", tail(2)}},
		{"prefix only", 3, 4, []string{tail(5)}},
		{"no room at all", 6, 2, []string{tail(5)}},
	}
	for _, c := range cases {
		got := capApprovalEntries(entries, c.fixed, c.budget)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: capApprovalEntries(fixed=%d, budget=%d) =\n%q\nwant\n%q",
				c.name, c.fixed, c.budget, got, c.want)
		}
	}
	// The caller's slice is never mutated in place: entries stays the full
	// diff so a later gate over the same data still starts from all of it.
	if strings.Join(entries, ",") != "e1,e2,e3,e4,e5" {
		t.Errorf("capApprovalEntries mutated its input: %q", entries)
	}
}

// TestSettingsApprovalBoundFixKeysAt80x24 is the F-1 pin for the settings half
// of the fence. With 21 resolved fix-keys targets at the standard 80x24 frame
// the gate's per-entry diff is bounded to the frame budget, so the composed
// frame keeps the gate title, the visible diff lines, the exact
// "N more entries NOT shown" tail, the gate legend and the shared footer, and
// never exceeds 24 rows. Unbounded, the same summary is 24 body lines and does
// not fit: the composer either trims the last diff lines with no marker (the
// current cut ladder: 17 of 21 targets visible) or, on the tail cut it
// replaced, takes the gate legend and the shared footer off-frame.
func TestSettingsApprovalBoundFixKeysAt80x24(t *testing.T) {
	h := newSettingsTestCore(t)
	const targets = 21
	h.seedDoctorProviders(t, targets)
	app := newSettingsAppSized(h, 80, 24)
	m := openFixKeysGate(t, app, "all")

	f, first, last := gateFrame(t, app, m)
	frame := f.String()
	if !strings.Contains(frame, m.Title()) {
		t.Errorf("gate title %q missing from the frame:\n%s", m.Title(), frame)
	}
	if !strings.Contains(frame, m.Footer()) {
		t.Errorf("gate legend %q missing from the frame:\n%s", m.Footer(), frame)
	}

	// Exactness: the visible diff lines plus the tail count add up to the
	// number of resolved targets, so the operator can tell exactly how much of
	// the write scope is not on screen.
	visible := 0
	for i := first; i <= last; i++ {
		if strings.Contains(f.Lines[i], " (provider ") {
			visible++
		}
	}
	hidden := targets - visible
	if visible < 1 || hidden < 1 {
		t.Fatalf("bounded gate shows %d of %d targets and hides %d; want a partial diff with a tail:\n%s",
			visible, targets, hidden, frame)
	}
	if want := fmt.Sprintf("%d more entries NOT shown", hidden); !strings.Contains(frame, want) {
		t.Errorf("tail %q missing from the frame:\n%s", want, frame)
	}

	// The gate keeps the LEADING targets, so the hidden scope is the tail of
	// the resolved list, never an arbitrary subset.
	firstShown := "AGY_P11_SET_01 (provider p01)"
	lastShown := fmt.Sprintf("AGY_P11_SET_%02d (provider p%02d)", visible, visible)
	firstHidden := fmt.Sprintf("AGY_P11_SET_%02d (provider p%02d)", visible+1, visible+1)
	if !strings.Contains(frame, firstShown) {
		t.Errorf("first target %q missing from the visible diff:\n%s", firstShown, frame)
	}
	if !strings.Contains(frame, lastShown) {
		t.Errorf("last visible target %q missing:\n%s", lastShown, frame)
	}
	if strings.Contains(frame, firstHidden) {
		t.Errorf("hidden target %q rendered anyway:\n%s", firstHidden, frame)
	}
}

// TestSettingsApprovalBoundRollbackAt80x24 is the rollback-keys arm of the
// same pin: 24 backup entries at 80x24 render a bounded diff with one exact
// tail line, and the gate legend plus the shared footer stay on frame.
func TestSettingsApprovalBoundRollbackAt80x24(t *testing.T) {
	h := newSettingsTestCore(t)
	const entries = 24
	names := make([]string, 0, entries)
	existing := make([]bool, 0, entries)
	for i := 1; i <= entries; i++ {
		names = append(names, fmt.Sprintf("E%02d", i))
		existing = append(existing, i%2 == 1)
	}
	dir := seedRollbackBackup(t, names, existing)
	app := newSettingsAppSized(h, 80, 24)
	m := openRollbackGate(t, app, dir)

	f, first, last := gateFrame(t, app, m)
	frame := f.String()
	visible := 0
	for i := first; i <= last; i++ {
		if strings.Contains(f.Lines[i], ": restore recorded value") ||
			strings.Contains(f.Lines[i], ": delete (recorded as absent)") {
			visible++
		}
	}
	hidden := entries - visible
	if visible < 1 || hidden < 1 {
		t.Fatalf("bounded rollback gate shows %d of %d entries and hides %d:\n%s",
			visible, entries, hidden, frame)
	}
	if want := fmt.Sprintf("%d more entries NOT shown", hidden); !strings.Contains(frame, want) {
		t.Errorf("tail %q missing from the frame:\n%s", want, frame)
	}
	if !strings.Contains(frame, "E01: restore recorded value") {
		t.Errorf("first backup entry missing from the visible diff:\n%s", frame)
	}
	firstHidden := fmt.Sprintf("E%02d", visible+1)
	if strings.Contains(frame, firstHidden) {
		t.Errorf("hidden backup entry %q rendered anyway:\n%s", firstHidden, frame)
	}
}

// TestSettingsApprovalBoundReservesMessageRowAt80x24 pins the message-line
// reservation of the bound: a live message line is one more frame section, so
// the gate body budget shrinks by its cost and the 80x24 frame still ends with
// the shared footer. Without the reservation the frame would be two rows over
// and the gate legend plus the footer would be cut.
func TestSettingsApprovalBoundReservesMessageRowAt80x24(t *testing.T) {
	h := newSettingsTestCore(t)
	h.seedDoctorProviders(t, 21)
	app := NewApp(80, 24, DirLTR)
	scr := NewSettingsScreen(h.core).(*settingsScreen)
	app.Push(scr)
	app.SetMessage("last action: provider added")
	scr.approveFixKeys("Repair Key Names (--fix-keys)", "all", app)
	m := app.Modal()
	if m == nil {
		t.Fatal("fix-keys approval modal did not open")
	}

	f, first, last := gateFrame(t, app, m)
	frame := f.String()
	if !strings.Contains(frame, "last action: provider added") {
		t.Errorf("live message line missing from the frame:\n%s", frame)
	}
	visible := 0
	for i := first; i <= last; i++ {
		if strings.Contains(f.Lines[i], " (provider ") {
			visible++
		}
	}
	hidden := 21 - visible
	if want := fmt.Sprintf("%d more entries NOT shown", hidden); hidden < 1 || !strings.Contains(frame, want) {
		t.Errorf("tail %q missing (visible=%d, hidden=%d):\n%s", want, visible, hidden, frame)
	}
}

// TestSettingsApprovalBoundFollowsFrameHeight pins that the bound is derived
// from the frame geometry instead of a fixed entry cap: the same 21 targets
// that are bounded at 80x24 render in full at 80x40, with no tail line.
func TestSettingsApprovalBoundFollowsFrameHeight(t *testing.T) {
	h := newSettingsTestCore(t)
	const targets = 21
	h.seedDoctorProviders(t, targets)
	app := newSettingsAppSized(h, 80, 40)
	m := openFixKeysGate(t, app, "all")

	body := strings.Join(m.Body(app.Width()), "\n")
	if strings.Contains(body, "NOT shown") {
		t.Fatalf("tall frame bounded a summary it can hold:\n%s", body)
	}
	if got := strings.Count(body, " (provider "); got != targets {
		t.Fatalf("visible targets = %d, want %d in an 80x40 frame:\n%s", got, targets, body)
	}
	if f := app.Frame(); len(f.Lines) > app.Height() {
		t.Fatalf("frame = %d rows, want at most %d", len(f.Lines), app.Height())
	}
}

// TestSettingsApprovalSummaryUnchangedForFewEntries pins the small-count arm
// of the bound: a one-target fix-keys gate at 80x24 renders exactly the
// pre-bound body, byte for byte, and carries no tail line.
func TestSettingsApprovalSummaryUnchangedForFewEntries(t *testing.T) {
	h := newSettingsTestCore(t)
	app := newSettingsAppSized(h, 80, 24)
	m := openFixKeysGate(t, app, "all")

	want := []string{
		"Repair BOM-variant key names for all; an entry is rewritten only when its clean name probes dead and a BOM variant probes valid.",
		"Models before: 1, after: 1",
		"A DPAPI-encrypted backup of every touched value is written (0600, atomic) before any change; providers.json is not modified.",
		"  AGY_P11_SET_KEY (provider alpha)",
	}
	if got := m.Body(app.Width()); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("small-gate body changed:\n--- got ---\n%s\n--- want ---\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(m.Footer(), "NOT shown") || strings.Contains(app.Frame().String(), "NOT shown") {
		t.Error("a tail line appeared on a gate that fits")
	}
}

// TestSettingsApprovalSummaryUnchangedForSmallRollback is the rollback-side
// small-count pin: three backup entries at 80x24 render exactly the pre-bound
// body, byte for byte, with no tail line.
func TestSettingsApprovalSummaryUnchangedForSmallRollback(t *testing.T) {
	h := newSettingsTestCore(t)
	dir := seedRollbackBackup(t,
		[]string{"FOO_API_KEY", "BAR_API_KEY", "BAZ_API_KEY"},
		[]bool{true, false, true})
	app := newSettingsAppSized(h, 80, 24)
	m := openRollbackGate(t, app, dir)

	want := []string{
		"Restore registry key names from backup " + providers.MaskOperand(dir) + "; entries recorded as absent are deleted, the rest are rewritten.",
		"Models before: 1, after: 1",
		"No file is written; the user environment store is mutated in place.",
		"  FOO_API_KEY: restore recorded value",
		"- BAR_API_KEY: delete (recorded as absent)",
		"  BAZ_API_KEY: restore recorded value",
	}
	if got := m.Body(app.Width()); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("small rollback body changed:\n--- got ---\n%s\n--- want ---\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(app.Frame().String(), "NOT shown") {
		t.Error("a tail line appeared on a rollback gate that fits")
	}
}

// TestSettingsApprovalBoundedRollbackKeepsMasking pins the masking discipline
// on both sides of the bound: a secret-shaped backup entry name inside the
// visible window still renders through MaskOperand (first-5 + ****), the
// secret-shaped entry that the bound hides never reaches the frame, and no raw
// value survives anywhere in the composed frame.
func TestSettingsApprovalBoundedRollbackKeepsMasking(t *testing.T) {
	h := newSettingsTestCore(t)
	const entries = 22
	const shownSecret = "AKIAABCDEFGHIJKLMNOP"
	const hiddenSecret = "AKIAZZZZZZZZZZZZZZZZ"
	names := make([]string, 0, entries)
	existing := make([]bool, 0, entries)
	for i := 1; i <= entries; i++ {
		switch i {
		case 1:
			names = append(names, shownSecret)
			existing = append(existing, true)
		case entries:
			names = append(names, hiddenSecret)
			existing = append(existing, false)
		default:
			names = append(names, fmt.Sprintf("E%02d", i))
			existing = append(existing, i%2 == 1)
		}
	}
	dir := seedRollbackBackup(t, names, existing)
	app := newSettingsAppSized(h, 80, 24)
	m := openRollbackGate(t, app, dir)

	f, first, last := gateFrame(t, app, m)
	frame := f.String()
	if strings.Contains(frame, shownSecret) || strings.Contains(frame, hiddenSecret) {
		t.Fatalf("a raw secret-shaped entry name reached the frame:\n%s", frame)
	}
	if !strings.Contains(frame, providers.MaskOperand(shownSecret)) {
		t.Errorf("visible secret-shaped name is not masked first-5 + ****:\n%s", frame)
	}
	if want := providers.MaskOperand(shownSecret); want != "AKIAA****" {
		t.Fatalf("masking authority changed: MaskOperand(%q) = %q", shownSecret, want)
	}
	// The bound hid the last entry, so only its (masked) absence is on frame.
	if strings.Contains(frame, providers.MaskOperand(hiddenSecret)) {
		t.Errorf("hidden secret-shaped entry rendered anyway:\n%s", frame)
	}
	visible := 0
	for i := first; i <= last; i++ {
		if strings.Contains(f.Lines[i], ": restore recorded value") ||
			strings.Contains(f.Lines[i], ": delete (recorded as absent)") {
			visible++
		}
	}
	hidden := entries - visible
	if hidden < 1 {
		t.Fatalf("bound did not fire for %d entries:\n%s", entries, frame)
	}
	if want := fmt.Sprintf("%d more entries NOT shown", hidden); !strings.Contains(frame, want) {
		t.Errorf("tail %q missing from the frame:\n%s", want, frame)
	}
}

// ---------------------------------------------------------------------------
// fix7 - the legacy settings screen composes every provider NAME operand
// through the registry-slot authority (providers.MaskSlot).
// ---------------------------------------------------------------------------

const (
	settingsPlantName   = "sk_live_0123456789abcdef"
	settingsPlantMasked = "sk_li****"
	settingsPlantTail   = "0123456789abcdef"
)

// seedProviderName appends one registry record and mirrors it into the temp
// providers.json the approval diff reads.
func seedProviderName(t *testing.T, h *settingsTestCore, name, baseURL, env string) {
	t.Helper()
	h.reg.Providers = append(h.reg.Providers, providers.Provider{
		Name:      name,
		BaseURL:   baseURL,
		APIKeyEnv: env,
		Models:    []providers.Model{{ID: "p1"}},
	})
	if err := h.persistReg(h.reg); err != nil {
		t.Fatalf("seed provider %q: %v", name, err)
	}
}

// assertNoPlantedName fails when haystack carries the planted name or the
// alphanumeric tail that a partial mask would leave behind.
func assertNoPlantedName(t *testing.T, haystack string) {
	t.Helper()
	if strings.Contains(haystack, settingsPlantName) {
		t.Errorf("output echoes the planted provider name raw: %q", haystack)
	}
	if strings.Contains(haystack, settingsPlantTail) {
		t.Errorf("output leaks the planted name tail: %q", haystack)
	}
}

// TestSettingsScreenProviderNameOperandsSlotMasked pins every name sink of the
// legacy settings screen: the credential-shaped name masks in the delete,
// rename, add, test and fix-keys compositions, and no raw fragment survives.
func TestSettingsScreenProviderNameOperandsSlotMasked(t *testing.T) {
	t.Run("delete gate masks the removed name", func(t *testing.T) {
		h := newSettingsTestCore(t)
		seedProviderName(t, h, settingsPlantName, "https://plant.example.com", "AGY_PLANT_KEY")
		app := newSettingsApp(h)
		app.Step(KeyRight)
		app.Step(Digit(6)) // providers.delete
		for _, k := range append(typeKeys(settingsPlantName), KeyCtrlS) {
			app.Step(k)
		}
		m := app.Modal()
		if m == nil {
			t.Fatal("delete approval modal did not open")
		}
		body := strings.Join(m.Body(110), "\n")
		for _, want := range []string{
			"Removes the registry entry for " + settingsPlantMasked + " only.",
			"- provider " + settingsPlantMasked + " (https://plant.example.com, 1 model)",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("approval body missing %q\n%s", want, body)
			}
		}
		assertNoPlantedName(t, body)
	})

	t.Run("rename gate masks both name operands", func(t *testing.T) {
		h := newSettingsTestCore(t)
		seedProviderName(t, h, settingsPlantName, "https://plant.example.com", "AGY_PLANT_KEY")
		app := newSettingsApp(h)

		// old operand: the planted record is the rename source.
		app.Step(KeyRight)
		app.Step(Digit(5)) // providers.rename
		for _, k := range append(typeKeys(settingsPlantName), KeyCtrlS) {
			app.Step(k)
		}
		for _, k := range append(typeKeys("beta"), KeyCtrlS) {
			app.Step(k)
		}
		m := app.Modal()
		if m == nil {
			t.Fatal("rename approval modal did not open (planted source)")
		}
		body := strings.Join(m.Body(110), "\n")
		for _, want := range []string{
			"Rename provider " + settingsPlantMasked + " to beta;",
			"- provider " + settingsPlantMasked + " (https://plant.example.com, 1 model)",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rename body missing %q\n%s", want, body)
			}
		}
		assertNoPlantedName(t, body)
		app.Step(KeyNo)

		// new operand: the planted name is the rename destination.
		app.Step(Digit(5))
		for _, k := range append(typeKeys("alpha"), KeyCtrlS) {
			app.Step(k)
		}
		for _, k := range append(typeKeys(settingsPlantName), KeyCtrlS) {
			app.Step(k)
		}
		m = app.Modal()
		if m == nil {
			t.Fatal("rename approval modal did not open (planted destination)")
		}
		body = strings.Join(m.Body(110), "\n")
		for _, want := range []string{
			"Rename provider alpha to " + settingsPlantMasked + ";",
			"+ provider " + settingsPlantMasked + " (https://alpha.example.com, 1 model)",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rename body missing %q\n%s", want, body)
			}
		}
		assertNoPlantedName(t, body)
	})

	t.Run("add gate masks the spec name", func(t *testing.T) {
		h := newSettingsTestCore(t)
		app := newSettingsApp(h)
		raw := `{"name":"` + settingsPlantName + `","baseURL":"https://plant2.example.com","apiKeyEnv":"AGY_PLANT_KEY","models":[{"id":"p1"}]}`

		// The JSON add door refuses the underscore-head name at ingest
		// (providers.NameCarriesCredential), so this name can no longer reach
		// the approval through the prompt. The screen sink is still pinned by
		// handing the approval the spec shape the door would accept, which is
		// where a disk-borne record could arrive out of band.
		if _, err := settings.ParseProviderSpecJSON([]byte(raw)); err == nil {
			t.Fatalf("the add door accepted the credential-shaped name %q", settingsPlantName)
		}

		spec := settings.ProviderSpec{
			Name:      settingsPlantName,
			BaseURL:   "https://plant2.example.com",
			APIKeyEnv: "AGY_PLANT_KEY",
			Models:    []providers.Model{{ID: "p1"}},
		}
		scr, ok := app.Top().(*settingsScreen)
		if !ok {
			t.Fatalf("top screen is %T; want *settingsScreen", app.Top())
		}
		scr.approveProviderAdd("providers.add.label", spec, "plant.json", app)
		m := app.Modal()
		if m == nil {
			t.Fatalf("add approval modal did not open; message %q", app.Message())
		}
		body := strings.Join(m.Body(110), "\n")
		for _, want := range []string{
			"Add provider " + settingsPlantMasked + " to the registry.",
			"+ provider " + settingsPlantMasked + " (https://plant2.example.com, 1 model)",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("approval body missing %q\n%s", want, body)
			}
		}
		assertNoPlantedName(t, body)
	})

	t.Run("provider test verdict masks the name", func(t *testing.T) {
		h := newSettingsTestCore(t)
		seedProviderName(t, h, settingsPlantName, "https://plant.example.com", "AGY_PLANT_KEY")
		app := newSettingsApp(h)
		app.Step(KeyRight)
		app.Step(Digit(4)) // providers.test
		for _, k := range append(typeKeys(settingsPlantName), KeyCtrlS) {
			app.Step(k)
		}
		want := settingsPlantMasked + ": connection OK"
		if got := app.Message(); got != want {
			t.Errorf("test verdict drifted:\ngot:  %q\nwant: %q", got, want)
		}
		assertNoPlantedName(t, app.Message())
	})

	t.Run("provider test failure masks the name in the detail", func(t *testing.T) {
		h := newSettingsTestCore(t)
		seedProviderName(t, h, settingsPlantName, "https://plant.example.com", "AGY_PLANT_KEY")
		invalid := providers.KeyProbeResult{Status: providers.ProbeInvalid, Detail: "dest plant.example: rejected by upstream"}
		h.probe = &invalid
		app := newSettingsApp(h)
		app.Step(KeyRight)
		app.Step(Digit(4)) // providers.test
		for _, k := range append(typeKeys(settingsPlantName), KeyCtrlS) {
			app.Step(k)
		}
		want := settingsPlantMasked + ": test failed. Details (English, redacted): " +
			`provider "` + settingsPlantMasked + `": key probe status invalid (dest plant.example: rejected by upstream)`
		if got := app.Message(); got != want {
			t.Errorf("failure sentence drifted:\ngot:  %q\nwant: %q", got, want)
		}
		assertNoPlantedName(t, app.Message())
	})

	t.Run("fix-keys entry masks the provider name", func(t *testing.T) {
		h := newSettingsTestCore(t)
		seedProviderName(t, h, settingsPlantName, "https://plant.example.com", "AGY_PLANT_KEY")
		app := newSettingsApp(h)
		m := openFixKeysGate(t, app, "all")
		body := strings.Join(m.Body(110), "\n")
		want := "AGY_PLANT_KEY (provider " + settingsPlantMasked + ")"
		if !strings.Contains(body, want) {
			t.Errorf("fix-keys body missing %q\n%s", want, body)
		}
		if !strings.Contains(body, "AGY_P11_SET_KEY (provider alpha)") {
			t.Errorf("benign fix-keys entry stopped rendering readable\n%s", body)
		}
		assertNoPlantedName(t, body)
	})
}

// TestSettingsScreenBenignProviderNamesByteIdentical pins the identity half of
// the fix7 contract: every corpus name renders exactly as the pre-fix screen
// did, in both the sentence and the diff line.
func TestSettingsScreenBenignProviderNamesByteIdentical(t *testing.T) {
	corpus := []string{"openai", "gemini", "deepseek", "nw", "charm", "acme-corp", "alpha", "beta"}
	h := newSettingsTestCore(t)
	for _, name := range corpus {
		if name == "alpha" {
			continue // already seeded by the harness
		}
		seedProviderName(t, h, name, "https://"+name+".example.com", "AGY_CORPUS_KEY")
	}
	app := newSettingsApp(h)
	app.Step(KeyRight) // p1 -> p2

	for _, name := range corpus {
		app.Step(Digit(6)) // providers.delete
		for _, k := range append(typeKeys(name), KeyCtrlS) {
			app.Step(k)
		}
		m := app.Modal()
		if m == nil {
			t.Fatalf("delete approval modal did not open for %q", name)
		}
		body := strings.Join(m.Body(110), "\n")
		for _, want := range []string{
			"Removes the registry entry for " + name + " only.",
			"- provider " + name + " (https://" + name + ".example.com, 1 model)",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("benign name %q stopped rendering byte-identically; missing %q\n%s", name, want, body)
			}
		}
		if strings.Contains(body, "****") {
			t.Errorf("benign name %q was masked:\n%s", name, body)
		}
		app.Step(KeyNo)
	}
}

// ---------------------------------------------------------------------------
// fixM3b / census O6 - the legacy settings screen composes the SAVED PRESET
// name and its numbers through the operand authority (providers.MaskOperand),
// the same authority the preset screens render the same value kind with.
// ---------------------------------------------------------------------------

const (
	// The contiguous base64 run class: the plant the preset list surface pins
	// for this same authority (screen_presets_test.go, the row-note pin).
	settingsPresetNamePlant = "AKIAIOSFODNN7EXAMPLE"
	settingsPresetNameMask  = "AKIAI****"
	settingsPresetNameTail  = "IOSFODNN7EXAMPLE"
	// The credential-head prefix class, planted in the numbers field.
	settingsPresetNumsPlant = "ghp_a1b2c3d4e5f6g7h8i9j0k1l2"
	settingsPresetNumsMask  = "ghp_a****"
	settingsPresetNumsTail  = "a1b2c3d4e5f6g7h8i9j0k1l2"
)

// openSettingsRow selects one settings row by item ID in the screen's loaded
// view (any section) and activates it with Enter, the same path a keypress
// takes: the list cursor moves, then the pick callback runs.
func openSettingsRow(t *testing.T, app *App, id string) {
	t.Helper()
	scr, ok := app.Top().(*settingsScreen)
	if !ok {
		t.Fatalf("top screen is %T; want *settingsScreen", app.Top())
	}
	for si, sec := range scr.view.Sections {
		for ri, it := range sec.Items {
			if it.ID != id {
				continue
			}
			scr.section = si
			scr.rebuild()
			scr.list.SetCursor(ri)
			app.Step(KeyEnter)
			return
		}
	}
	t.Fatalf("row %q missing from the settings view", id)
}

// TestSettingsScreenPresetOperandsMasked pins presetLines (the
// internal/tui/screen_settings.go listing composer): a credential-shaped preset
// name and a credential-shaped numbers field render as first-5+**** in the
// preset listing result screen, and no raw fragment of either survives.
func TestSettingsScreenPresetOperandsMasked(t *testing.T) {
	h := newSettingsTestCore(t)
	h.presets = []swarm.SavedPreset{
		{Name: settingsPresetNamePlant, Numbers: "1,3"},
		{Name: "quick", Numbers: settingsPresetNumsPlant},
	}
	app := newSettingsApp(h)
	openSettingsRow(t, app, "swarm.presets")
	if _, ok := app.Top().(*settingsResultScreen); !ok {
		t.Fatalf("preset listing did not push the result screen: %T", app.Top())
	}
	frame := app.Frame().String()
	for _, want := range []string{
		settingsPresetNameMask + " = 1,3",
		"quick = " + settingsPresetNumsMask,
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("preset listing missing %q\n%s", want, frame)
		}
	}
	for _, raw := range []string{settingsPresetNamePlant, settingsPresetNumsPlant} {
		if strings.Contains(frame, raw) {
			t.Errorf("preset listing echoes the credential-shaped preset %q raw\n%s", raw, frame)
		}
	}
	for _, tail := range []string{settingsPresetNameTail, settingsPresetNumsTail} {
		if strings.Contains(frame, tail) {
			t.Errorf("preset listing leaks the planted preset tail %q\n%s", tail, frame)
		}
	}
}

// TestSettingsScreenBenignPresetOperandsByteIdentical pins the identity half:
// a benign preset name and a benign numbers list render exactly as the
// pre-change "%s = %s" composition did, and no mask marker appears.
func TestSettingsScreenBenignPresetOperandsByteIdentical(t *testing.T) {
	presets := []swarm.SavedPreset{
		{Name: "p9", Numbers: "1,3,19,23"},
		{Name: "deep-review", Numbers: "2,5"},
		{Name: "swift", Numbers: "1"},
	}
	h := newSettingsTestCore(t)
	h.presets = presets
	app := newSettingsApp(h)
	openSettingsRow(t, app, "swarm.presets")
	frame := app.Frame().String()
	for _, p := range presets {
		want := p.Name + " = " + p.Numbers
		if !strings.Contains(frame, want) {
			t.Errorf("benign preset %q stopped rendering byte-identically; missing %q\n%s", p.Name, want, frame)
		}
	}
	if strings.Contains(frame, "****") {
		t.Errorf("a benign preset name or numbers list was masked:\n%s", frame)
	}
}
