package tui

import (
	"bytes"
	"errors"
	"os"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/realagy"
	"agypro/internal/settings"
)

// English chrome constants for this screen. The doctor pane renders each of
// them directly instead of resolving through the catalog (decision 6B keeps
// shared chrome English, and the settings screen reads the translatable
// doctor.structural_fail key itself); a "catalog-resolved" comment only names
// the en.json entry that carries the same wording.
const (
	// catalog-resolved, fallback kept: doctor.tab.status
	doctorTabStatus = "Status"
	// catalog-resolved, fallback kept: doctor.tab.doctor
	doctorTabDoctor = "Doctor"
	// catalog-resolved, fallback kept: doctor.section.registry
	doctorSectionRegistry = "Registry"
	// catalog-resolved, fallback kept: doctor.key.resolved
	doctorKeyResolved = "resolved"
	// catalog-resolved, fallback kept: doctor.empty.providers
	doctorEmptyProviders = "No providers registered."
	// catalog-resolved, fallback kept: doctor.not_found
	doctorNotFound = "not found"
	// English chrome (decision 6B keeps chrome English, so this hint carries
	// no catalog key). The first line names the refresh gesture so the key is
	// discoverable, the next two lines name the in-TUI repair path, and the
	// CLI pair below stays as the terminal alternative.
	doctorFixHint = "This screen is read-only; press r to re-run diagnose."
	// English chrome, wrapped so every hint line fits an 80-cell frame.
	doctorFixTUIPath = "To repair key names, use the in-TUI path:"
	// English chrome: the settings grid path with its section title and item
	// label exactly as the settings screen renders them, under the M4 gate.
	// P1-6: the section is named by its title text, without the "[5]" the
	// title used to embed (the hub owns row numbers; a second [n] here read
	// as hub row 5, which is Doctor, not Diagnostics & Keys).
	doctorFixTUIWhere = "  Hub > Settings > Diagnostics & Keys > Repair Key Names (--fix-keys)"
	// English chrome.
	doctorFixCLI = "Or run in your terminal:"
	// English chrome (decision 6B).
	doctorFixKeysCmd = "agy pro settings doctor fix-keys all"
	// English chrome (decision 6B).
	doctorRollbackCmd = "agy pro settings doctor rollback-keys <backup-dir>"
	// catalog-resolved, fallback kept: doctor.structural_fail
	doctorStructuralFail = "Doctor reported structural failures: {{err}}"
)

// doctorPane identifies the tab-switched pane of the doctor screen.
type doctorPane int

const (
	paneStatus doctorPane = iota
	paneDoctor
)

// statusField is one label/value row of the Status pane. The label column is
// padded through cellwidth so CJK provider names still align with ASCII ones.
type statusField struct {
	label string
	value string
}

// textScroller is a read-only vertical scroll window over precomposed lines.
// Both screens of this unit (doctor S11, help S12) render static text taller
// than the frame, so they share this one window primitive instead of the
// selectable stepList: there is nothing to pick, only lines to page through.
type textScroller struct {
	lines []string
	off   int
}

func (t *textScroller) setLines(lines []string) {
	t.lines = lines
	t.off = 0
}

// replace swaps the content without touching the offset. Draw paths that
// rebuild their lines per frame (width-dependent wrapping) use it so a
// redraw does not lose the scroll position; window clamps the offset.
func (t *textScroller) replace(lines []string) {
	t.lines = lines
}

func (t *textScroller) clamp(page int) {
	if page < 1 {
		page = 1
	}
	max := len(t.lines) - page
	if max < 0 {
		max = 0
	}
	if t.off > max {
		t.off = max
	}
	if t.off < 0 {
		t.off = 0
	}
}

// scroll moves the window by n pages (negative pages up) and clamps it.
func (t *textScroller) scroll(n, page int) {
	t.off += n * page
	t.clamp(page)
}

// scrollLines moves the window by single lines (arrow-key scrolling).
func (t *textScroller) scrollLines(n, page int) {
	t.off += n
	t.clamp(page)
}

func (t *textScroller) home() { t.off = 0 }

// end moves the window to the last page.
func (t *textScroller) end(page int) {
	t.off = len(t.lines)
	t.clamp(page)
}

// window returns the visible slice of at most page lines, clamped first.
func (t *textScroller) window(page int) []string {
	t.clamp(page)
	if len(t.lines) == 0 {
		return nil
	}
	end := t.off + page
	if end > len(t.lines) {
		end = len(t.lines)
	}
	return t.lines[t.off:end]
}

// doctorScreen is S11: the tab-switched Doctor / Status screen. Both panes
// are strictly read-only: the Status pane renders state.json fields, the
// resolved real agy binary path, and a registry summary (env-var NAMES with
// resolved/unset status, never values); the Doctor pane renders the output
// of Core.DoctorDiagnose into a scrollable window. The actions are a refresh
// gesture that retakes both snapshots (r) and printed repair hints that name
// the in-TUI settings path and the CLI twins; nothing on this screen ever
// writes a file or executes a fix.
type doctorScreen struct {
	core   *settings.Core
	bundle *i18n.Bundle

	pane   doctorPane
	status []statusField
	diag   *textScroller
	off    int // scroll offset of the Status pane
	page   int // last window height derived in Draw
}

// NewDoctorScreen builds the Doctor / Status screen over core. A nil core
// builds one with the real (read-only here) defaults. The diagnose run and
// the status snapshot happen at construction and again on every r press, so
// the screen opens on a point-in-time report the operator can retake without
// leaving it.
func NewDoctorScreen(core *settings.Core) Screen {
	if core == nil {
		core = settings.NewCore(settings.Deps{})
	}
	s := &doctorScreen{
		core:   core,
		bundle: Bundle(),
		diag:   &textScroller{},
		page:   1,
	}
	s.composeStatus()
	s.runDiagnose()
	return s
}

// Title returns the screen title.
func (s *doctorScreen) Title() string { return s.bundle.T("doctor.screen.title") }

// Mode is ModeNav: this screen scrolls and tab-switches, it never edits.
func (s *doctorScreen) Mode() Mode { return ModeNav }

// composeStatus snapshots state.json, the real agy path, and the registry
// summary into label/value rows. The provider NAME is a registry slot and
// routes through the slot authority (MaskSlot); the env-var NAME and every
// error detail keep MaskOperand; key VALUES are never touched (KeyAvailable
// reports presence only).
func (s *doctorScreen) composeStatus() {
	var fields []statusField
	st, err := mode.LoadState()
	switch {
	case err != nil:
		fields = append(fields, statusField{
			label: s.bundle.T("view.current_mode.label"),
			value: s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(err.Error())}),
		})
	default:
		fields = append(fields,
			statusField{s.bundle.T("view.current_mode.label"), st.CurrentMode},
			statusField{s.bundle.T("view.default_profile.label"), st.EffectiveDefaultProfile()},
		)
	}

	realPath, rerr := realagy.Resolve()
	value := realPath
	if rerr != nil {
		value = doctorNotFound + ": " + providers.MaskOperand(rerr.Error())
	}
	fields = append(fields, statusField{s.bundle.T("view.version.real_agy.label"), value})

	reg, regErr := providers.Load()
	switch {
	case regErr != nil && errors.Is(regErr, os.ErrNotExist):
		fields = append(fields, statusField{doctorSectionRegistry, doctorEmptyProviders})
	case regErr != nil:
		fields = append(fields, statusField{doctorSectionRegistry,
			s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(regErr.Error())})})
	default:
		modelCount := 0
		for _, p := range reg.Providers {
			modelCount += len(p.Models)
		}
		fields = append(fields, statusField{doctorSectionRegistry,
			s.bundle.Tn("menu.count.providers", len(reg.Providers)) + " · " +
				s.bundle.Tn("menu.count.models", modelCount)})
		for _, p := range reg.Providers {
			fields = append(fields, statusField{providers.MaskSlot(p.Name), s.keyStatus(p)})
		}
	}
	s.status = fields
}

// keyStatus renders one provider's key line: the env-var NAME plus a
// resolved/unset verdict. The VALUE itself is never read for display.
func (s *doctorScreen) keyStatus(p providers.Provider) string {
	if p.APIKeyEnv == "" {
		return s.bundle.T("providers.key.unset")
	}
	env := providers.MaskOperand(p.APIKeyEnv)
	if p.KeyAvailable() {
		return env + " (" + doctorKeyResolved + ")"
	}
	return env + " " + s.bundle.T("providers.key.unset")
}

// runDiagnose captures Core.DoctorDiagnose into the scrollable window and
// appends the read-only repair hints (the in-TUI settings path first, the CLI
// twins as the terminal alternative) plus a structural-failure tail when the
// diagnose run returned an error.
func (s *doctorScreen) runDiagnose() {
	var buf bytes.Buffer
	diagErr := s.core.DoctorDiagnose(&buf, false)
	var lines []string
	if trimmed := strings.TrimRight(buf.String(), "\n"); trimmed != "" {
		lines = append(lines, strings.Split(trimmed, "\n")...)
	}
	lines = append(lines, "",
		doctorFixHint, doctorFixTUIPath, doctorFixTUIWhere, doctorFixCLI,
		"  "+doctorFixKeysCmd, "  "+doctorRollbackCmd)
	if diagErr != nil {
		lines = append(lines, "", fillHubSlots(doctorStructuralFail, map[string]string{
			"err": providers.MaskOperand(diagErr.Error()),
		}))
	}
	s.diag.setLines(lines)
}

// refresh retakes both read-only snapshots: the Status fields are recomposed
// and the Doctor pane re-runs the diagnose seam, with the Status window
// rewound to the top (setLines already rewinds the Doctor window) so the fresh
// report is read from its first line. Only the read-only seams run here;
// nothing is written and no repair is executed.
func (s *doctorScreen) refresh() {
	s.composeStatus()
	s.runDiagnose()
	s.off = 0
}

// isRefreshKey reports whether k is the refresh gesture. The console reader
// delivers a printable letter as the rune carrier (Caps Lock included), while
// the scripted and line-mode seams hand over the plain spelling, so both forms
// resolve here.
func isRefreshKey(k Key) bool {
	if k == Key("r") {
		return true
	}
	r, ok := RuneOf(k)
	return ok && (r == 'r' || r == 'R')
}

// tabBar renders the two pane tabs with the active one in brackets.
func (s *doctorScreen) tabBar() string {
	left := " " + doctorTabStatus + " "
	right := " " + doctorTabDoctor + " "
	if s.pane == paneStatus {
		left = "[" + doctorTabStatus + "]"
	} else {
		right = "[" + doctorTabDoctor + "]"
	}
	return left + " | " + right
}

// Draw renders the tab bar over the active pane's scroll window plus the
// read-only scroll hint. All cell math goes through cellwidth.
func (s *doctorScreen) Draw(a *App) []string {
	w := a.Width()
	s.page = a.Height() - 8
	if s.page < 1 {
		s.page = 1
	}
	lines := []string{s.tabBar(), ""}
	if s.pane == paneStatus {
		lines = append(lines, s.statusLines(w)...)
	} else {
		for _, l := range s.diag.window(s.page) {
			lines = append(lines, cellwidth.Truncate(l, w))
		}
	}
	return append(lines, "", s.bundle.T("doctor.scroll_hint"))
}

// statusLines renders the Status pane: the label column padded through
// cellwidth so ASCII and CJK labels share one value column, windowed by
// the pane's scroll offset.
func (s *doctorScreen) statusLines(w int) []string {
	labelW := 0
	for _, f := range s.status {
		if cw := cellwidth.String(f.label); cw > labelW {
			labelW = cw
		}
	}
	avail := w - labelW - 2
	if avail < 1 {
		avail = 1
	}
	lines := make([]string, 0, len(s.status))
	for _, f := range s.status {
		lines = append(lines, cellwidth.PadRight(f.label, labelW)+"  "+cellwidth.Truncate(f.value, avail))
	}
	end := s.off + s.page
	if end > len(lines) {
		end = len(lines)
	}
	if s.off >= len(lines) {
		return nil
	}
	return lines[s.off:end]
}

// HandleKey routes one key. Tab toggles the pane (resetting the scroll
// window); Up/Down/PgUp/PgDn/Home/End scroll the active pane; r retakes both
// snapshots on either pane; every other key is ignored because the screen is
// read-only.
func (s *doctorScreen) HandleKey(k Key, a *App) {
	up := func() {
		if s.pane == paneStatus {
			s.scrollStatus(-1)
		} else {
			s.diag.scrollLines(-1, s.page)
		}
	}
	down := func() {
		if s.pane == paneStatus {
			s.scrollStatus(1)
		} else {
			s.diag.scrollLines(1, s.page)
		}
	}
	switch k {
	case KeyTab:
		if s.pane == paneStatus {
			s.pane = paneDoctor
		} else {
			s.pane = paneStatus
		}
		s.off = 0
		s.diag.home()
	case KeyUp:
		up()
	case KeyDown:
		down()
	case KeyPgUp:
		if s.pane == paneStatus {
			s.scrollStatus(-s.page)
		} else {
			s.diag.scroll(-1, s.page)
		}
	case KeyPgDn:
		if s.pane == paneStatus {
			s.scrollStatus(s.page)
		} else {
			s.diag.scroll(1, s.page)
		}
	case KeyHome:
		s.off = 0
		s.diag.home()
	case KeyEnd:
		s.off = len(s.status)
		s.clampStatus()
		s.diag.end(s.page)
	default:
		if isRefreshKey(k) {
			s.refresh()
		}
	}
}

// scrollStatus pages the Status pane window by n lines.
func (s *doctorScreen) scrollStatus(n int) {
	s.off += n
	s.clampStatus()
}

func (s *doctorScreen) clampStatus() {
	max := len(s.status) - s.page
	if max < 0 {
		max = 0
	}
	if s.off > max {
		s.off = max
	}
	if s.off < 0 {
		s.off = 0
	}
}
