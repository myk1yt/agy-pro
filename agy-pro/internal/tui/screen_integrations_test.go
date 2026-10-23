package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/agysettings"
	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/integrations"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/settings"
	"agypro/profiles"
)

// integrationsTestHome points every path this screen touches at one temp tree:
// the agy-pro home (state.json + the manifest), the Gemini config dir (the
// deployment destinations) and the user home (the settings door's ladder). The
// live ~/.agy-pro and ~/.gemini are never read or written.
func integrationsTestHome(t *testing.T) string {
	t.Helper()
	// Every assertion below reads English catalog text, and the bundle is a
	// package-level global another test may have moved; pin it and hand it back.
	SetLanguage("en")
	t.Cleanup(func() { SetLanguage("en") })
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv(paths.EnvAgyProHome, filepath.Join(home, "agy-pro"))
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(home, "gemini-config"))
	return home
}

// installIntegrationsLauncher creates the wrapper the settings door refuses to
// bake a command without. Without it the statusline row reports the documented
// launcher shape instead of a slot posture, which is its own test.
func installIntegrationsLauncher(t *testing.T) {
	t.Helper()
	candidates, err := integrations.StatusLineLauncherCandidates()
	if err != nil || len(candidates) == 0 {
		t.Fatalf("StatusLineLauncherCandidates: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(candidates[0]), 0o755); err != nil {
		t.Fatalf("mkdir launcher dir: %v", err)
	}
	if err := os.WriteFile(candidates[0], []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatalf("write launcher: %v", err)
	}
}

// integrationsWriteState writes a state.json with the given intent rows.
func integrationsWriteState(t *testing.T, st mode.State) {
	t.Helper()
	st.CurrentMode = "clean"
	st.ManagedItems = append([]string(nil), mode.DefaultManagedItems...)
	if err := mode.SaveState(&st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

// integrationsScreenApp mounts the screen on a fresh 80x24 app.
func integrationsScreenApp(t *testing.T) (*App, *integrationsScreen) {
	t.Helper()
	scr := NewIntegrationsScreen(settings.NewCore(settings.Deps{}))
	screen, ok := scr.(*integrationsScreen)
	if !ok {
		t.Fatalf("NewIntegrationsScreen returned %T", scr)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	return app, screen
}

// integrationsFrame renders one frame as text.
func integrationsFrame(app *App) string { return app.Frame().String() }

// TestIntegrationsScreenRows pins the row inventory and the honest posture
// rendering: five feature rows (label = the engine's feature word, note = the
// intent plus the engine verdict), the preset row, and the five actions.
func TestIntegrationsScreenRows(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{IntegrationRules: mode.IntegrationToggleOff})
	app, screen := integrationsScreenApp(t)

	frame := integrationsFrame(app)
	for _, want := range []string{
		"statusline", "hook", "rules", "plugins", "skills",
		i18n.Load("en").T("integrations.row.preset"),
		i18n.Load("en").T("integrations.action.apply_pending"),
		i18n.Load("en").T("integrations.action.retract_all"),
		i18n.Load("en").T("integrations.action.adopt"),
		i18n.Load("en").T("integrations.action.rollback"),
		i18n.Load("en").T("integrations.action.repair"),
		i18n.Load("en").T("integrations.status.not_deployed"),
		i18n.Load("en").T("integrations.intent.off"),
		i18n.Load("en").T("integrations.intent.unset"),
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
	// The unset-vs-off sentence appears because four features carry no intent.
	// It is truncated to the frame width like every body line, so the assertion
	// is on its opening clause.
	if !strings.Contains(frame, i18n.Load("en").T("integrations.intent.legacy")[:40]) {
		t.Fatalf("the legacy-posture sentence is missing:\n%s", frame)
	}
	if got := len(screen.rows); got != 11 {
		t.Fatalf("rows = %d, want 11 (5 features + preset + 5 actions)", got)
	}
}

// TestIntegrationsScreenActionRowNotes pins the explanation every action row
// carries: the five write rows resolve their catalog description in BOTH
// catalogs this screen ships against (en is the schema, ko is the language the
// observation was made in), and the pending row's note carries the live count
// the composition reads instead of a constant. A row whose label is a bare verb
// ("Adopt", "Repair", "Rollback") was the whole problem: nothing on the frame
// said what activating it would do.
func TestIntegrationsScreenActionRowNotes(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{IntegrationRules: mode.IntegrationToggleOn})

	for _, lang := range []string{"en", "ko"} {
		SetLanguage(lang)
		b := i18n.Load(lang)
		app, screen := integrationsScreenApp(t)
		app.Frame()
		if len(screen.pending) != 1 || screen.pending[0] != integrations.FeatureRules {
			t.Fatalf("%s: pending = %v, want the rules feature alone", lang, screen.pending)
		}
		want := []struct {
			idx  int
			text string
		}{
			{6, b.Tf("integrations.row.help.apply_pending", map[string]string{"n": "1"})},
			{7, b.T("integrations.row.help.retract_all")},
			{8, b.T("integrations.row.help.adopt")},
			{9, b.T("integrations.row.help.rollback")},
			{10, b.T("integrations.row.help.repair")},
		}
		for _, tc := range want {
			if strings.TrimSpace(tc.text) == "" {
				t.Fatalf("%s: the row %d description resolves empty", lang, tc.idx+1)
			}
			note := screen.rows[tc.idx].Note
			if tc.idx == 9 {
				// The rollback row is a picker: the marker leads its note.
				if note != integrationsPickerMarker+" "+tc.text {
					t.Fatalf("%s: row %d note = %q, want the marker then %q", lang, tc.idx+1, note, tc.text)
				}
				continue
			}
			if !strings.Contains(note, tc.text) {
				t.Fatalf("%s: row %d note = %q, want it to carry %q", lang, tc.idx+1, note, tc.text)
			}
		}
	}

	// The pending count is state, not a constant: a second pending feature moves
	// the number the row shows.
	SetLanguage("en")
	integrationsWriteState(t, mode.State{
		IntegrationRules:  mode.IntegrationToggleOn,
		IntegrationSkills: mode.IntegrationToggleOn,
	})
	app, screen := integrationsScreenApp(t)
	app.Frame()
	if len(screen.pending) != 2 {
		t.Fatalf("pending = %v, want two features", screen.pending)
	}
	if want := i18n.Load("en").Tf("integrations.row.help.apply_pending", map[string]string{"n": "2"}); !strings.Contains(screen.rows[6].Note, want) {
		t.Fatalf("the pending row note = %q, want it to carry %q", screen.rows[6].Note, want)
	}
}

// TestIntegrationsScreenPickerMarker pins the affordance the observation asked
// for: both rows whose Enter opens a choice advertise it, no other row carries
// the marker, the preset row keeps its state line IN FRONT of the marker, and
// the screen legend names the marker so the gesture is discoverable without
// opening the row.
func TestIntegrationsScreenPickerMarker(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, screen := integrationsScreenApp(t)
	frame := app.Frame()

	if strings.TrimSpace(integrationsPickerMarker) == "" {
		t.Fatal("the picker marker is empty; the picker rows would advertise nothing")
	}
	picker := map[int]bool{5: true, 9: true}
	for i, row := range screen.rows {
		if got := strings.Contains(row.Note, integrationsPickerMarker); got != picker[i] {
			t.Fatalf("row %d (%s) carries the marker = %v, want %v: note = %q", i+1, row.Label, got, picker[i], row.Note)
		}
	}
	state := screen.b.Tf("integrations.preset.note", map[string]string{
		"preset": screen.presetWord(),
		"on":     "0",
		"total":  fmt.Sprint(len(integrations.AllFeatures())),
	})
	presetNote := screen.rows[5].Note
	if !strings.HasPrefix(presetNote, state) || !strings.HasSuffix(presetNote, integrationsPickerMarker) {
		t.Fatalf("the preset note = %q, want the state line %q first and the marker last", presetNote, state)
	}
	if !strings.HasPrefix(screen.rows[9].Note, integrationsPickerMarker) {
		t.Fatalf("the rollback note = %q, want the marker first", screen.rows[9].Note)
	}

	body := strings.Join(frame.Lines, "\n")
	if !strings.Contains(body, integrationsChromeHint) {
		t.Fatalf("the legend naming the marker is not on the 80-column frame:\n%s", body)
	}
	if got := strings.Count(body, integrationsPickerMarker); got < 3 {
		t.Fatalf("the frame carries the marker %d times, want >= 3 (two picker rows and the legend):\n%s", got, body)
	}
}

// TestIntegrationsChromeHintFitsTheFrame pins the legend verbatim AND to the
// frame width. A legend past 80 cells is cut at the compose choke point, and the
// cut takes the tail, which is where the picker sentence sits: the pin keeps the
// sentence inside the frame instead of trusting the truncation.
func TestIntegrationsChromeHintFitsTheFrame(t *testing.T) {
	const want = "every action is gated (y/n) · r re-reads · Enter on a >> row opens a choice"
	if integrationsChromeHint != want {
		t.Fatalf("integrationsChromeHint = %q, want %q", integrationsChromeHint, want)
	}
	if w := cellwidth.String(integrationsChromeHint); w > 80 {
		t.Fatalf("integrationsChromeHint is %d cells, want <= 80", w)
	}
}

// TestIntegrationsScreenRowNotesFitFrameAt80x24 pins the budget after the notes
// landed. A note renders inside the line it belongs to, so Draw still composes
// 17 lines at 80x24 (2 summary + blank + 11 rows + blank + legend + footer), and
// this asserts the whole note survives the row frame in ALL twelve catalogs. The
// CJK catalogs are the tight ones (one character measures two cells) and ar is
// the dangerous one: cellwidth refuses a mid-string cut inside the RTL range, so
// an over-long ar row line would render as the empty string and drop the row
// instead of truncating it.
func TestIntegrationsScreenRowNotesFitFrameAt80x24(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{IntegrationRules: mode.IntegrationToggleOn})
	for _, meta := range i18n.Languages() {
		lang := meta.Code
		SetLanguage(lang)
		app, screen := integrationsScreenApp(t)
		if got, want := len(screen.Draw(app)), 17; got != want {
			t.Fatalf("%s: Draw composed %d lines, want %d (a note renders inside its row, it adds no line)", lang, got, want)
		}
		frame := app.Frame()
		if len(frame.Lines) > 24 {
			t.Fatalf("%s: frame lines = %d, want <= 24:\n%s", lang, len(frame.Lines), frame.String())
		}
		if page := screen.list.PageSize(); page < len(screen.rows) {
			t.Fatalf("%s: page size = %d, want >= %d rows so nothing pages at 80x24", lang, page, len(screen.rows))
		}
		rendered := screen.list.Render(80)
		if len(rendered) != len(screen.rows) {
			t.Fatalf("%s: rendered %d row lines, want %d", lang, len(rendered), len(screen.rows))
		}
		widest := 0
		for i, line := range rendered {
			w := cellwidth.String(line)
			if w > 80 {
				t.Fatalf("%s: row %d renders %d cells at 80 columns: %q", lang, i+1, w, line)
			}
			if w > widest {
				widest = w
			}
			// Every row must survive whole, and in ar none may trip cellwidth's RTL
			// rule, which refuses a mid-string cut and returns the empty line - a
			// row that vanishes instead of truncating. That is what the statusline
			// row used to do on a launcherless machine (FIX-15 gives it the
			// screen's own short English state), so the feature rows are pinned here
			// too, not just the picker and action rows below them.
			if line == "" {
				t.Fatalf("%s: row %d rendered empty at 80 columns (an over-long RTL line is refused, not cut)", lang, i+1)
			}
			// A truncated note would drop its tail from the frame with no other
			// symptom, so each note must appear whole.
			if !strings.Contains(line, screen.rows[i].Note) {
				t.Fatalf("%s: row %d truncated its note; line = %q, note = %q", lang, i+1, line, screen.rows[i].Note)
			}
		}
		body := strings.Join(frame.Lines, "\n")
		if !strings.Contains(body, "[11]") {
			t.Fatalf("%s: the last row is not on the frame at 80x24:\n%s", lang, body)
		}
		if lang == "ko" || lang == "en" {
			t.Logf("%s measured: 17 Draw lines, frame %d of 24, widest row %d of 80 cells", lang, len(frame.Lines), widest)
		}
	}
	SetLanguage("en")
}

// TestIntegrationsStatuslineRowLauncherMissingNote pins the one posture no
// catalog can carry: the settings door's launcher error. Its note is the
// localized prefix plus the documented launcher path, the longest sentence this
// screen renders, and in ar it overran the 80-column row budget - and
// cellwidth.Truncate refuses a mid-string cut inside the RTL range, so the whole
// row rendered as the empty string: the operator saw the feature label with no
// state at all. The screen now answers that one state with its own English
// chrome const, which fits the note column in every catalog.
func TestIntegrationsStatuslineRowLauncherMissingNote(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	SetLanguage("ar")
	t.Cleanup(func() { SetLanguage("en") })

	if w := cellwidth.String(integrationsStatuslineLauncherMissingNote); w > 40 {
		t.Fatalf("integrationsStatuslineLauncherMissingNote is %d cells, want <= 40 so it fits the note column in every locale", w)
	}
	if i18n.Dir("ar") != "rtl" {
		t.Fatalf("ar is not an RTL locale in the catalog, so this test would prove nothing")
	}

	// No launcher is installed in this sandbox, which is the posture under test.
	scr := NewIntegrationsScreen(settings.NewCore(settings.Deps{}))
	screen, ok := scr.(*integrationsScreen)
	if !ok {
		t.Fatalf("NewIntegrationsScreen returned %T", scr)
	}
	app := NewApp(80, 24, dirFor("ar"))
	app.Push(scr)
	frame := app.Frame()

	rendered := screen.list.Render(80)
	if len(rendered) != len(screen.rows) {
		t.Fatalf("rendered %d row lines, want %d", len(rendered), len(screen.rows))
	}
	// The symptom first: an over-long RTL row is refused whole, not cut, so the
	// empty line is what the operator used to see where the statusline state goes.
	if strings.TrimSpace(rendered[0]) == "" {
		t.Fatalf("the statusline row rendered empty at 80 columns in ar (an over-long RTL line is refused, not cut)")
	}
	if !strings.Contains(rendered[0], integrationsStatuslineLauncherMissingNote) {
		t.Fatalf("statusline row = %q, want it to carry the English chrome %q", rendered[0], integrationsStatuslineLauncherMissingNote)
	}
	// The localized sentence this const replaces must be gone from the row: a
	// catalog-resolved posture cannot be cut mid-string once it overruns.
	if localized := i18n.Load("ar").T("integrations.status.launcher_missing"); strings.Contains(rendered[0], localized) {
		t.Fatalf("statusline row = %q, want no catalog posture %q", rendered[0], localized)
	}
	// The mirroring painter keeps text in logical order, so the const survives
	// onto the rendered frame as well.
	body := strings.Join(frame.Lines, "\n")
	if !strings.Contains(body, integrationsStatuslineLauncherMissingNote) {
		t.Fatalf("the RTL frame does not carry the statusline state %q:\n%s", integrationsStatuslineLauncherMissingNote, body)
	}
	if cellwidth.String(rendered[0]) > 80 {
		t.Fatalf("statusline row = %q, %d cells at 80 columns", rendered[0], cellwidth.String(rendered[0]))
	}
	// The state the screen renders is the screen's own chrome, not a catalog
	// value: the localized prefix plus the launcher path is what overran.
	if got := screen.postures[string(integrations.FeatureStatusline)]; got != integrationsStatuslineLauncherMissingNote {
		t.Fatalf("statusline posture = %q, want the screen's English chrome %q", got, integrationsStatuslineLauncherMissingNote)
	}
	// The mechanism, quantitatively: the note this unit replaced does not fit the
	// row budget either, so pinning only "non-empty" would let a later catalog
	// edit reintroduce the empty row silently.
	_, planErr := integrations.StatusLinePlan("")
	var launcher *integrations.StatusLineLauncherError
	if !errors.As(planErr, &launcher) {
		t.Fatalf("StatusLinePlan did not report the launcher error on this sandbox: %v", planErr)
	}
	legacy := screen.intentWord(screen.intentToggle(integrations.FeatureStatusline)) + " · " +
		i18n.Load("ar").T("integrations.status.launcher_missing") + " " + launcher.Shape
	labelW := 0
	for _, row := range screen.rows {
		if w := cellwidth.String(row.Label); w > labelW {
			labelW = w
		}
	}
	budget := 80 - len("  [ 1] ") - labelW - len("  ")
	if w := cellwidth.String(legacy); w <= budget {
		t.Fatalf("the replaced note measures %d cells and would fit the %d-cell budget now; the English chrome is no longer load-bearing - re-decide it", w, budget)
	}
	t.Logf("ar measured: statusline row %d of 80 cells, note %d cells, replaced note %d cells for a %d-cell budget",
		cellwidth.String(rendered[0]), cellwidth.String(screen.rows[0].Note), cellwidth.String(legacy), budget)
}

// TestIntegrationsScreenFitsFrameAt80x24 is the measured frame-fit pin: at the
// default geometry every row is visible without paging, and the frame does not
// exceed its height budget.
func TestIntegrationsScreenFitsFrameAt80x24(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, screen := integrationsScreenApp(t)
	frame := app.Frame()
	if frame.Height > 24 {
		t.Fatalf("frame height = %d, want <= 24", frame.Height)
	}
	if len(frame.Lines) > 24 {
		t.Fatalf("frame lines = %d, want <= 24", len(frame.Lines))
	}
	if screen.list.PageSize() < len(screen.rows) {
		t.Fatalf("page size = %d, want >= %d rows so nothing pages at 80x24", screen.list.PageSize(), len(screen.rows))
	}
	body := strings.Join(frame.Lines, "\n")
	if !strings.Contains(body, "[11]") {
		t.Fatalf("the last row is not on the frame at 80x24:\n%s", body)
	}
}

// TestIntegrationsScreenToggleCancelWritesNothing pins the gate: activating a
// feature row opens the Approval modal over the engine's exact diff, and n writes
// nothing at all (no destination, no manifest, no intent row).
func TestIntegrationsScreenToggleCancelWritesNothing(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(3), KeyNo}})
	if !strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("the approval modal did not open:\n%s", out)
	}
	if !strings.Contains(out, "create") {
		t.Fatalf("the modal did not carry the engine's exact diff:\n%s", out)
	}
	if !strings.Contains(app.Message(), strings.TrimSuffix(i18n.Load("en").T("settings.msg.cancelled"), ".")) {
		t.Fatalf("message = %q, want the cancelled sentence", app.Message())
	}
	if _, err := os.Stat(filepath.Join(integrationsConfigDir(t), "rules")); !os.IsNotExist(err) {
		t.Fatalf("a cancelled toggle created the destination tree (err = %v)", err)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationRules != "" {
		t.Fatalf("a cancelled toggle recorded intent %q", st.IntegrationRules)
	}
}

// TestIntegrationsScreenToggleAppliesAfterYes pins the positive half: y deploys
// from the embedded snapshot and records the intent in the same consent.
func TestIntegrationsScreenToggleAppliesAfterYes(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(3), KeyYes}})
	if !strings.Contains(out, i18n.Load("en").T("common.success")) {
		t.Fatalf("no success message in the frames:\n%s", out)
	}
	snapshot, err := fs.ReadFile(profiles.GetToolsFS(), "rules/AGENTS.md")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	live, err := os.ReadFile(filepath.Join(integrationsConfigDir(t), "rules", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read deployed file: %v", err)
	}
	if string(live) != string(snapshot) {
		t.Fatal("the deployed bytes are not the snapshot's bytes")
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationRules != mode.IntegrationToggleOn {
		t.Fatalf("integration_rules = %q, want on", st.IntegrationRules)
	}
	// The derived preset records the mixed shape the toggle produced.
	if st.IntegrationPreset != string(integrations.PresetCustom) {
		t.Fatalf("integration_preset = %q, want custom", st.IntegrationPreset)
	}
}

// TestIntegrationsScreenLineModeParity pins constraint 12 on this screen: a host
// whose keys arrive as text lines reaches the same gate and writes the same
// bytes, using the mandated words plus the digit row selector.
func TestIntegrationsScreenLineModeParity(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	out := app.CaptureRun(NewLineKeys(strings.NewReader("3\ny\n")))
	if !strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("the gate did not open in line mode:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(integrationsConfigDir(t), "rules", "AGENTS.md")); err != nil {
		t.Fatalf("line mode did not deploy: %v", err)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationRules != mode.IntegrationToggleOn {
		t.Fatalf("integration_rules = %q, want on", st.IntegrationRules)
	}
	// The word vocabulary still answers Esc (back out) instead of writing.
	app2, _ := integrationsScreenApp(t)
	app2.CaptureRun(NewLineKeys(strings.NewReader("3\nesc\n")))
	if msg := app2.Message(); strings.Contains(msg, i18n.Load("en").T("common.success")) {
		t.Fatalf("esc consented to a write: %q", msg)
	}
}

// TestIntegrationsScreenPresetChoice pins the preset row: it offers native and
// tools only (custom is derived, never chosen) and the chosen posture writes the
// whole set plus its preset row.
func TestIntegrationsScreenPresetChoice(t *testing.T) {
	integrationsTestHome(t)
	installIntegrationsLauncher(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(6), Key("2"), KeyYes}})
	if !strings.Contains(out, string(integrations.PresetTools)) {
		t.Fatalf("the preset choice did not offer tools:\n%s", out)
	}
	if strings.Contains(out, string(integrations.PresetCustom)) {
		t.Fatalf("the preset choice offered the derived custom word:\n%s", out)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationPreset != string(integrations.PresetTools) {
		t.Fatalf("integration_preset = %q, want tools", st.IntegrationPreset)
	}
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			continue
		}
		layout, err := integrations.LayoutFor(feature)
		if err != nil {
			t.Fatalf("LayoutFor(%s): %v", feature, err)
		}
		if _, err := os.Stat(layout.DestDir); err != nil {
			t.Fatalf("%s was not deployed to %s by the preset: %v", feature, layout.DestDir, err)
		}
	}
}

// TestIntegrationsScreenPresetApplyDegradesWithoutLauncher is the screen half of
// F-S9C-02 (T8 §2 rule 5). The preset row is a whole-posture apply, so on a machine
// with no agy-tokens launcher it must present the diff with the statusline step
// subtracted and the reason as a note, then deploy the content features - not report
// an error and write nothing. Every other action on this screen still names its
// target, so the per-feature toggle keeps the loud refusal (the statusline row's own
// toggle is that pin) and so does the pending row, which the CLI degrades only
// because recorded intent led it there.
//
// The note is asserted on the modal BODY, which is the diff the operator approves
// (Approval.Body returns the summary verbatim). The rendered frame at 80x24 cuts a
// 100+ line deploy diff at its height budget, so a frame-level assertion would test
// the frame's cut, not this composition.
//
// FIX-9 (F-R2-02) also pins the note's POSITION: it must LEAD the body. The shared
// composer renders notes before step lines for exactly this reason - the reason a
// step was subtracted is the part a caller's height budget must never drop, and a
// TUI-only operator has no CLI transcript to fall back on.
func TestIntegrationsScreenPresetApplyDegradesWithoutLauncher(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	// Row 6 (preset) -> option 2 (tools): the gate is now open and nothing is written.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(6), Key("2")}})
	if !strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("the degraded preset opened no gate (it refused instead):\n%s", out)
	}
	approval, ok := app.modal.(*Approval)
	if !ok {
		t.Fatalf("the preset gate is %T, want the approval modal", app.modal)
	}
	bodyLines := approval.Body(80)
	body := strings.Join(bodyLines, "\n")
	if !strings.Contains(body, "statusline skipped") || !strings.Contains(body, "agy-tokens") {
		t.Fatalf("the approved diff does not name the skipped statusline and its reason:\n%s", body)
	}
	// The leading "note" column is the stable form: a step line starts with its
	// feature name at column 0, so the two can never be confused.
	first := ""
	if len(bodyLines) > 0 {
		first = bodyLines[0]
	}
	if !strings.HasPrefix(first, "note") {
		t.Fatalf("the approval body does not lead with the note (first line = %q):\n%s", first, body)
	}
	firstStep := -1
	for i, line := range bodyLines {
		if !strings.HasPrefix(line, "note") && !strings.HasPrefix(line, " ") {
			firstStep = i
			break
		}
	}
	if firstStep <= 0 {
		t.Fatalf("the body carries no step line after the leading note (first step index = %d):\n%s", firstStep, body)
	}
	if strings.Contains(body, "statusline create") || strings.Contains(body, "settings.json") {
		t.Fatalf("the approved diff still carries the door step:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(integrationsConfigDir(t), "skills")); !os.IsNotExist(err) {
		t.Fatalf("the gate wrote before consent (err = %v)", err)
	}

	if msg := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyYes}}); !strings.Contains(msg, i18n.Load("en").T("common.success")) {
		t.Fatalf("consent did not complete the degraded write:\n%s", msg)
	}
	if msg := app.Message(); strings.Contains(msg, i18n.Load("en").T("common.error")) {
		t.Fatalf("message = %q, want a success line", msg)
	}
	// The content half landed, the door half did not.
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			continue
		}
		layout, err := integrations.LayoutFor(feature)
		if err != nil {
			t.Fatalf("LayoutFor(%s): %v", feature, err)
		}
		if _, err := os.Stat(layout.DestDir); err != nil {
			t.Fatalf("%s was not deployed by the degraded preset: %v", feature, err)
		}
	}
	settingsPath, pathErr := agysettings.GlobalSettingsPath()
	if pathErr != nil {
		t.Fatalf("GlobalSettingsPath: %v", pathErr)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("the degraded preset wrote settings.json (err = %v)", err)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationPreset != string(integrations.PresetTools) {
		t.Fatalf("integration_preset = %q, want tools", st.IntegrationPreset)
	}
	if st.IntegrationStatusline != mode.IntegrationToggleOn {
		t.Fatalf("integration_statusline = %q, want the composer's on fact", st.IntegrationStatusline)
	}
}

// TestIntegrationsScreenStatusLineToggleWithoutLauncherRefuses is the other half
// of the F-S9C-02 screen pin: the statusline row's own toggle NAMES its target, so
// the same launcherless machine must keep the loud refusal (a message line naming
// the launcher, no gate, no file). Only the whole-posture apply degrades.
func TestIntegrationsScreenStatusLineToggleWithoutLauncherRefuses(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(1), KeyYes}})
	if strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("the explicit statusline toggle opened a gate on a launcherless machine:\n%s", out)
	}
	msg := app.Message()
	if !strings.Contains(msg, i18n.Load("en").T("common.error")) || !strings.Contains(msg, "agy-tokens") {
		t.Fatalf("message = %q, want the error naming the launcher", msg)
	}
	settingsPath, pathErr := agysettings.GlobalSettingsPath()
	if pathErr != nil {
		t.Fatalf("GlobalSettingsPath: %v", pathErr)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("the refused toggle wrote settings.json (err = %v)", err)
	}
}

// TestIntegrationsScreenPendingApplyWithoutLauncherRefuses is the third face of
// the same F-S9C-02 rule: the pending row names its target set too (every feature
// whose intent is on), so a launcherless machine must keep the loud refusal there
// as well and not inherit the preset row's degrade. Only the whole-posture apply
// is implicit; the pending row is explicit because intent led the operator there.
//
// The pin exists because the two branches sit on one expression in
// composeFeatureWrite: a preset argument reaching the pending composition would
// flip it to the degrading arm silently, leaving the prose claim at the preset
// test above true of nothing.
func TestIntegrationsScreenPendingApplyWithoutLauncherRefuses(t *testing.T) {
	integrationsTestHome(t)
	// The statusline intent is what puts the door feature into the pending set:
	// without it the row answers "nothing is pending" and never reaches the door.
	integrationsWriteState(t, mode.State{IntegrationStatusline: mode.IntegrationToggleOn})
	app, screen := integrationsScreenApp(t)
	if len(screen.pending) != 1 || screen.pending[0] != integrations.FeatureStatusline {
		t.Fatalf("pending = %v, want the statusline feature alone", screen.pending)
	}

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(7), KeyYes}})
	if strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("the pending row opened a gate on a launcherless machine:\n%s", out)
	}
	msg := app.Message()
	if !strings.Contains(msg, i18n.Load("en").T("common.error")) || !strings.Contains(msg, "agy-tokens") {
		t.Fatalf("message = %q, want the error naming the launcher", msg)
	}
	if strings.Contains(msg, i18n.Load("en").T("integrations.msg.nothing_pending")) {
		t.Fatalf("message = %q, want the door refusal, not the empty pending state", msg)
	}
	if app.modal != nil {
		t.Fatalf("the refused pending row left %T open", app.modal)
	}
	settingsPath, pathErr := agysettings.GlobalSettingsPath()
	if pathErr != nil {
		t.Fatalf("GlobalSettingsPath: %v", pathErr)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("the refused pending row wrote settings.json (err = %v)", err)
	}
}

// TestIntegrationsScreenRollbackWithoutSnapshot pins the named empty state: the
// rollback picker's settings.json option reports it on the message line instead of
// opening a gate.
func TestIntegrationsScreenRollbackWithoutSnapshot(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	// Row 10 is the rollback row (0 then 1 then 0 is the shared compose); the picker's
	// first option is the settings.json door.
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Key("0"), Digit(1), Key("0"), Digit(1)}})
	if strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("an empty rollback opened a gate:\n%s", out)
	}
	if got := app.Message(); got != i18n.Load("en").T("integrations.msg.no_snapshot") {
		t.Fatalf("message = %q, want the named empty state", got)
	}
}

// TestIntegrationsScreenRollbackPickerOptions pins the gesture: the rollback row
// opens a picker carrying the settings.json door plus the four content features the
// deploy-tree verb owns, and it never offers the settings-door feature word (that is
// the same thing as option 1).
func TestIntegrationsScreenRollbackPickerOptions(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, screen := integrationsScreenApp(t)
	screen.app = app
	app.Frame()

	screen.activate(9) // row 10 = rollback
	choice, ok := app.modal.(*Choice)
	if !ok {
		t.Fatalf("the rollback row opened %T, want the picker", app.modal)
	}
	body := strings.Join(choice.Body(80), "\n")
	if !strings.Contains(body, "settings.json") {
		t.Fatalf("the picker does not offer the settings door:\n%s", body)
	}
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			if strings.Contains(body, string(feature)) {
				t.Fatalf("the picker offers the door word %q twice:\n%s", feature, body)
			}
			continue
		}
		if !strings.Contains(body, string(feature)) {
			t.Fatalf("the picker does not offer %q:\n%s", feature, body)
		}
	}
}

// TestIntegrationsScreenRollbackFeatureRestoresPreInstallBytes pins the new path end
// to end: after a deployment, choosing a feature's rollback restores its pre-install
// bytes, removes what the deployment created, and records no intent row.
func TestIntegrationsScreenRollbackFeatureRestoresPreInstallBytes(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	preInstall := []byte("# the user's own rules file\n")
	target := filepath.Join(integrationsConfigDir(t), "rules", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir rules dir: %v", err)
	}
	if err := os.WriteFile(target, preInstall, 0o600); err != nil {
		t.Fatalf("plant the pre-install file: %v", err)
	}
	app, _ := integrationsScreenApp(t)
	if out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(3), KeyYes}}); !strings.Contains(out, i18n.Load("en").T("common.success")) {
		t.Fatalf("the deployment did not land:\n%s", out)
	}

	// Row 10, then option 2 (the first content feature is hook, so pick rules by name:
	// options are settings.json, hook, rules, plugins, skills -> "3").
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Key("0"), Digit(1), Key("0"), Digit(3), KeyYes}})
	if !strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("the rollback gate did not open:\n%s", out)
	}
	live, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the restored file: %v", err)
	}
	if string(live) != string(preInstall) {
		t.Fatalf("the destination holds %q, want the pre-install bytes", live)
	}
	if _, err := os.Stat(filepath.Join(integrationsConfigDir(t), "rules", "GEMINI.md")); !os.IsNotExist(err) {
		t.Fatalf("a file the deployment created survived the rollback (err = %v)", err)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationRules != mode.IntegrationToggleOn {
		t.Fatalf("integration_rules = %q; the rollback rewrote an intent row", st.IntegrationRules)
	}
}

// TestIntegrationsScreenRollbackFeatureEmptyState pins the message for a feature the
// manifest records nothing for: the picker closes, no gate opens, and nothing is
// written.
func TestIntegrationsScreenRollbackFeatureEmptyState(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	// Row 10, then option 5 (skills).
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Key("0"), Digit(1), Key("0"), Digit(5)}})
	if strings.Contains(out, i18n.Load("en").T("integrations.approval.title")) {
		t.Fatalf("an empty feature rollback opened a gate:\n%s", out)
	}
	if got := app.Message(); got != i18n.Load("en").T("integrations.msg.nothing_rollback") {
		t.Fatalf("message = %q, want the named empty state", got)
	}
	if _, err := os.Stat(filepath.Join(integrationsConfigDir(t), "skills")); !os.IsNotExist(err) {
		t.Fatalf("an empty rollback created the destination tree (err = %v)", err)
	}
}

// TestIntegrationsScreenPendingRepairAndAdoptEmptyStates pins the three "nothing
// to do" answers, so an empty gate can never be presented for a no-op and no verb
// answers with another verb's sentence.
//
// Row 9 (adopt) is the F-S9B-02 pin: a machine with nothing recordable used to
// answer "Nothing to repair: every recorded destination matches." - the repair
// verb's sentence, with the repair verb's reasons missing. The CLI twin prints
// "Nothing to write:" for the identical state.
func TestIntegrationsScreenPendingRepairAndAdoptEmptyStates(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, screen := integrationsScreenApp(t)
	screen.app = app
	app.Frame() // builds the rows the activate path indexes

	for _, tc := range []struct {
		row  int
		want string
	}{
		{7, i18n.Load("en").T("integrations.msg.nothing_pending")},
		{9, i18n.Load("en").T("integrations.msg.nothing_write")},
		{11, i18n.Load("en").T("integrations.msg.nothing_repair")},
	} {
		screen.activate(tc.row - 1)
		if got := app.Message(); got != tc.want {
			t.Fatalf("row %d message = %q, want %q", tc.row, got, tc.want)
		}
		if app.modal != nil {
			t.Fatalf("row %d opened a gate for a no-op", tc.row)
		}
		if tc.row == 9 && strings.Contains(app.Message(), "repair") {
			t.Fatalf("the adopt empty state answers with the repair verb: %q", app.Message())
		}
		app.SetMessage("")
	}
}

// TestIntegrationsScreenStatusLineToggle pins the settings-door half of the
// feature rows: with the wrapper installed, row 1 plans the slot through the
// door (its own renderer, not the deployment engine's) and y writes it.
func TestIntegrationsScreenStatusLineToggle(t *testing.T) {
	integrationsTestHome(t)
	installIntegrationsLauncher(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(1), KeyYes}})
	if !strings.Contains(out, "statusLine") {
		t.Fatalf("the statusline diff did not carry the slot; got:\n%s", out)
	}
	settingsPath, err := agysettings.GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatalf("read settings.json: %v", readErr)
	}
	if !strings.Contains(string(data), "statusLine") {
		t.Fatalf("the slot was not written:\n%s", data)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IntegrationStatusline != mode.IntegrationToggleOn {
		t.Fatalf("integration_statusline = %q, want on", st.IntegrationStatusline)
	}
}

// TestIntegrationsScreenRepairAfterDrift pins the repair row: after a destination
// drifts, the repair write re-plans it, restores the engine's bytes, and reports
// nothing left to repair on the next run.
func TestIntegrationsScreenRepairAfterDrift(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, screen := integrationsScreenApp(t)
	app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(3), KeyYes}}) // deploy rules

	target := filepath.Join(integrationsConfigDir(t), "rules", "AGENTS.md")
	if err := os.WriteFile(target, []byte("hand-edited\n"), 0o600); err != nil {
		t.Fatalf("drift the destination: %v", err)
	}
	screen.reload()
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Key("0"), Digit(1), Digit(1), KeyYes}}) // row 11 = repair
	if !strings.Contains(out, "drifted") {
		t.Fatalf("the repair diff does not name the drift:\n%s", out)
	}
	snapshot, err := fs.ReadFile(profiles.GetToolsFS(), "rules/AGENTS.md")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	live, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read repaired file: %v", err)
	}
	if string(live) != string(snapshot) {
		t.Fatal("repair did not restore the engine's bytes")
	}
	screen.reload()
	screen.activate(10)
	if got := app.Message(); got != i18n.Load("en").T("integrations.msg.nothing_repair") {
		t.Fatalf("message = %q, want the nothing-to-repair sentence", got)
	}
}

// TestIntegrationsScreenRefreshRereadsPosture pins the r gesture: an external
// change to state.json shows up after r without reopening the screen.
func TestIntegrationsScreenRefreshRereadsPosture(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	app, _ := integrationsScreenApp(t)

	before := integrationsFrame(app)
	if strings.Contains(featureRowLine(before, "rules"), i18n.Load("en").T("integrations.intent.on")) {
		t.Fatalf("rules already reads on:\n%s", before)
	}
	integrationsWriteState(t, mode.State{IntegrationRules: mode.IntegrationToggleOn})
	app.CaptureRun(&ScriptedKeys{Keys: []Key{Key("r")}})
	after := integrationsFrame(app)
	if !strings.Contains(featureRowLine(after, "rules"), i18n.Load("en").T("integrations.intent.on")) {
		t.Fatalf("r did not re-read the intent:\n%s", after)
	}
}

// TestHubIntegrationsRowOpensTheScreen pins the hub wiring: row 9 reaches the
// integrations screen through the app shell's open factory.
func TestHubIntegrationsRowOpensTheScreen(t *testing.T) {
	integrationsTestHome(t)
	integrationsWriteState(t, mode.State{})
	shell := &setupShell{core: settings.NewCore(settings.Deps{})}
	app := NewApp(80, 24, DirLTR)
	app.Push(NewHubScreen(func() HubCounts { return hubFixture }, shell.open))

	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(9)}})
	title := i18n.Load("en").T("menu.integrations")
	if !strings.Contains(out, title) {
		t.Fatalf("hub row 9 did not open the integrations screen:\n%s", out)
	}
	if top := app.Top(); top == nil || top.Title() != title {
		t.Fatalf("top screen title = %v, want %q", top, title)
	}
}

// integrationsConfigDir resolves the sandboxed deployment root.
func integrationsConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := paths.GeminiConfigDir()
	if err != nil {
		t.Fatalf("GeminiConfigDir: %v", err)
	}
	return dir
}

// featureRowLine returns the frame line carrying one feature row.
func featureRowLine(frame, feature string) string {
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, feature) {
			return line
		}
	}
	return ""
}
