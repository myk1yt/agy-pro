package tui

import (
	"errors"
	"fmt"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/integrations"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/profiles"
)

// Row IDs of the integrations screen, in display order. The five feature rows
// toggle one feature each; two rows open a choice instead of composing a write
// (the preset row opens the native/tools choice, the rollback row opens the
// snapshot picker) and both carry integrationsPickerMarker; the five action
// rows compose the multi-feature writes.
const (
	integrationsIDPreset   = "integrations.preset"
	integrationsIDApply    = "integrations.apply"
	integrationsIDRetract  = "integrations.retract"
	integrationsIDAdopt    = "integrations.adopt"
	integrationsIDRollback = "integrations.rollback"
	integrationsIDRepair   = "integrations.repair"
)

// integrationsFeatureID is the row ID of one feature toggle.
func integrationsFeatureID(feature integrations.Feature) string {
	return "integrations.feature." + string(feature)
}

// integrationsPickerMarker flags a row whose activation opens a choice instead
// of composing a write. Two rows carry it (the preset row and the rollback row)
// and the chrome legend below names it, so the gesture is visible without
// opening the row.
//
// It is ASCII on purpose. A second arrow glyph would read as the list's own
// "▶" cursor sitting one column to the right, and two ASCII cells measure the
// same in every cellwidth class, so the marker costs the note budget nothing on
// a frame whose notes are CJK.
const integrationsPickerMarker = ">>"

// integrationsChromeHint is the screen's own legend. English chrome by decision
// 6B, like the doctor screen's hints: it names the gestures the list footer does
// not (the gate, the refresh and the picker marker), and no catalog key carries
// chrome. It is 75 cells so it fits the 80-column frame whole; a longer legend
// would be cut at compose and lose the tail, which is where the picker sentence
// sits.
const integrationsChromeHint = "every action is gated (y/n) · r re-reads · Enter on a " +
	integrationsPickerMarker + " row opens a choice"

// integrationsStatuslineLauncherMissingNote is the screen's own chrome for the
// one statusline posture no catalog can carry. The settings door's launcher
// error renders as a localized prefix plus the documented launcher path (a
// 44-cell path behind the prefix), the longest posture this screen puts in a row
// note; past the row budget cellwidth refuses a mid-string cut inside the RTL
// range, so in ar the whole row rendered as the empty string and the operator saw
// a feature label with no state at all. English chrome by decision 6B (the screen
// owns its state chrome, no catalog key), 32 cells: the note column fits it in
// every catalog at 80 columns. The exact launcher path stays on the CLI door's
// error and in the engine's approval diff.
const integrationsStatuslineLauncherMissingNote = "launcher missing - apply refuses"

// integrationsScreen is the hub's Integrations surface: five feature toggles, the
// preset row, and the five actions that compose a multi-feature write. Every
// write is presented through the M4 Approval modal over the engine's own diff
// lines, so the diff the operator approves is the diff the engine performs.
//
// Posture reads (state.json intent plus the engine's Status) are taken at
// construction, after every completed write, and on the r gesture, the pattern
// the doctor screen uses: the engine's Status walks the destination trees, so a
// per-frame read would re-walk them for nothing.
type integrationsScreen struct {
	core *settings.Core
	b    *i18n.Bundle

	list  *stepList
	app   *App
	rows  []Row
	lang  string
	dirty bool

	st       *mode.State            // last intent read
	postures map[string]string      // feature -> rendered engine posture
	loadErr  string                 // intent read failure, rendered instead of rows
	pending  []integrations.Feature // features the pending-apply row would deploy
}

// NewIntegrationsScreen builds the integrations screen over core. A nil core
// builds the production Core.
func NewIntegrationsScreen(core *settings.Core) Screen {
	if core == nil {
		core = settings.NewCore(settings.Deps{})
	}
	s := &integrationsScreen{core: core, b: Bundle(), postures: map[string]string{}, dirty: true}
	s.list = newStepList(s.activate)
	s.reload()
	return s
}

// Title resolves through the catalog in the active language.
func (s *integrationsScreen) Title() string { return s.b.T("menu.integrations") }

// Mode is ModeNav: digits and arrows both navigate, and no row edits text.
func (s *integrationsScreen) Mode() Mode { return ModeNav }

// reload re-reads the intent and the engine posture. Any read failure is latched
// and rendered on the screen instead of leaving a stale posture behind.
func (s *integrationsScreen) reload() {
	s.dirty = true
	s.loadErr = ""
	s.st = nil
	s.postures = map[string]string{}
	s.pending = nil
	st, err := mode.LoadState()
	if err != nil || st == nil {
		s.loadErr = providers.MaskOperand(errText(err))
		return
	}
	s.st = st
	for _, feature := range integrations.AllFeatures() {
		s.postures[string(feature)] = s.readPosture(feature)
		if s.intentToggle(feature) == mode.IntegrationToggleOn {
			s.pending = append(s.pending, feature)
		}
	}
}

// errText renders a read failure without a nil dereference.
func errText(err error) string {
	if err == nil {
		return "state.json could not be read"
	}
	return err.Error()
}

// intentToggle reads one feature's persisted intent through the state layer's
// normalizer. "" is the legacy posture, not an explicit off.
func (s *integrationsScreen) intentToggle(feature integrations.Feature) string {
	if s.st == nil {
		return ""
	}
	switch feature {
	case integrations.FeatureStatusline:
		return mode.NormalizeIntegrationToggle(s.st.IntegrationStatusline)
	case integrations.FeatureHook:
		return mode.NormalizeIntegrationToggle(s.st.IntegrationHook)
	case integrations.FeatureRules:
		return mode.NormalizeIntegrationToggle(s.st.IntegrationRules)
	case integrations.FeaturePlugins:
		return mode.NormalizeIntegrationToggle(s.st.IntegrationPlugins)
	case integrations.FeatureSkills:
		return mode.NormalizeIntegrationToggle(s.st.IntegrationSkills)
	}
	return ""
}

// intentWord renders one normalized toggle for the row note.
func (s *integrationsScreen) intentWord(toggle string) string {
	switch toggle {
	case mode.IntegrationToggleOn:
		return s.b.T("integrations.intent.on")
	case mode.IntegrationToggleOff:
		return s.b.T("integrations.intent.off")
	default:
		return s.b.T("integrations.intent.unset")
	}
}

// presetWord renders the composed preset, or "unset" when no row is recorded.
func (s *integrationsScreen) presetWord() string {
	if s.st == nil {
		return s.b.T("integrations.intent.unset")
	}
	if preset := mode.NormalizeIntegrationPreset(s.st.IntegrationPreset); preset != "" {
		return preset
	}
	return s.b.T("integrations.intent.unset")
}

// onCount counts the features whose intent is an explicit on.
func (s *integrationsScreen) onCount() int {
	n := 0
	for _, feature := range integrations.AllFeatures() {
		if s.intentToggle(feature) == mode.IntegrationToggleOn {
			n++
		}
	}
	return n
}

// readPosture renders one feature's engine verdict with the engine's read-only
// verbs only: Status for the content features, StatusLinePlan for the settings
// door (which has no content layout).
func (s *integrationsScreen) readPosture(feature integrations.Feature) string {
	if feature == integrations.FeatureStatusline {
		door, err := integrations.StatusLinePlan("")
		if err != nil {
			var launcher *integrations.StatusLineLauncherError
			if errors.As(err, &launcher) {
				// The wrapper is what the door bakes into settings.json, so a
				// missing launcher is the one posture a user must fix before any
				// statusLine write can be planned. The sentence is chrome, not
				// catalog text: a localized prefix plus launcher.Shape, the longest
				// posture this screen renders, and past the row budget cellwidth
				// refuses a mid-string cut inside the RTL range, so in ar the whole
				// row rendered EMPTY - a label with no state. The exact launcher path
				// stays on the CLI door's error and in the engine's approval diff,
				// where it is a copyable operand.
				return integrationsStatuslineLauncherMissingNote
			}
			return s.b.T("integrations.status.unavailable") + ": " + providers.MaskOperand(errText(err))
		}
		switch door.Action {
		case integrations.StatusLineActionCreate:
			return s.b.T("integrations.status.slot_absent")
		case integrations.StatusLineActionUpdate:
			return s.b.T("integrations.status.slot_differs")
		case integrations.StatusLineActionAdopt:
			return s.b.T("integrations.status.slot_unrecorded")
		}
		return s.b.T("integrations.status.slot_recorded")
	}
	status, err := integrations.Status(feature)
	if err != nil {
		return s.b.T("integrations.status.unavailable") + ": " + providers.MaskOperand(errText(err))
	}
	if status == nil || !status.Deployed {
		if status != nil && status.Foreign > 0 {
			return s.b.T("integrations.status.not_deployed") + " · " +
				s.b.Tf("integrations.status.foreign", map[string]string{"n": fmt.Sprint(status.Foreign)})
		}
		return s.b.T("integrations.status.not_deployed")
	}
	var parts []string
	if status.Clean > 0 {
		parts = append(parts, s.b.Tf("integrations.status.clean", map[string]string{"n": fmt.Sprint(status.Clean)}))
	}
	if status.Drifted > 0 {
		parts = append(parts, s.b.Tf("integrations.status.drifted", map[string]string{"n": fmt.Sprint(status.Drifted)}))
	}
	if status.Missing > 0 {
		parts = append(parts, s.b.Tf("integrations.status.missing", map[string]string{"n": fmt.Sprint(status.Missing)}))
	}
	if status.Foreign > 0 {
		parts = append(parts, s.b.Tf("integrations.status.foreign", map[string]string{"n": fmt.Sprint(status.Foreign)}))
	}
	return strings.Join(parts, ", ")
}

// syncRows rebuilds the row list when the language or the posture changed. The
// feature rows carry the intent and the engine posture side by side, which is the
// honest reading of the two stores: the intent is what the user asked for, the
// posture is what is on disk. The action rows carry what the row DOES instead -
// they are writes, not features, so a description is the only thing that tells a
// reader the difference between "Retract all" and "Rollback" without opening the
// gate. The pending row's description is stateful: it carries the count of the
// features Enter would deploy, read from the same slice the composition reads.
func (s *integrationsScreen) syncRows() {
	if !s.dirty && s.rows != nil {
		return
	}
	s.dirty = false
	s.rows = nil
	for _, feature := range integrations.AllFeatures() {
		s.rows = append(s.rows, Row{
			Label: string(feature),
			Note:  s.intentWord(s.intentToggle(feature)) + " · " + s.postures[string(feature)],
			ID:    integrationsFeatureID(feature),
		})
	}
	s.rows = append(s.rows,
		Row{
			Label: s.b.T("integrations.row.preset"),
			// The marker trails the state line: the composed preset and the
			// n-of-5 count are what this row is read for, so they keep the
			// leading position and the marker only adds the gesture.
			Note: s.b.Tf("integrations.preset.note", map[string]string{
				"preset": s.presetWord(),
				"on":     fmt.Sprint(s.onCount()),
				"total":  fmt.Sprint(len(integrations.AllFeatures())),
			}) + " · " + integrationsPickerMarker,
			ID: integrationsIDPreset,
		},
		Row{
			Label: s.b.T("integrations.action.apply_pending"),
			Note:  s.b.Tf("integrations.row.help.apply_pending", map[string]string{"n": fmt.Sprint(len(s.pending))}),
			ID:    integrationsIDApply,
		},
		Row{
			Label: s.b.T("integrations.action.retract_all"),
			Note:  s.b.T("integrations.row.help.retract_all"),
			ID:    integrationsIDRetract,
		},
		Row{
			Label: s.b.T("integrations.action.adopt"),
			Note:  s.b.T("integrations.row.help.adopt"),
			ID:    integrationsIDAdopt,
		},
		Row{
			Label: s.b.T("integrations.action.rollback"),
			// The marker leads here: this row has no state line to keep in front
			// of it, so the gesture is the first thing the eye must catch.
			Note: integrationsPickerMarker + " " + s.b.T("integrations.row.help.rollback"),
			ID:   integrationsIDRollback,
		},
		Row{
			Label: s.b.T("integrations.action.repair"),
			Note:  s.b.T("integrations.row.help.repair"),
			ID:    integrationsIDRepair,
		},
	)
	cursor := s.list.Cursor()
	s.list.SetRows(s.rows)
	s.list.SetCursor(cursor)
}

// summaryLines renders the preset sentence plus, when any feature carries no
// intent at all, the sentence that keeps "unset" and "off" apart (P5-3 T6: they
// are different postures and collapsing them changes behaviour on every existing
// install).
func (s *integrationsScreen) summaryLines(w int) []string {
	lines := []string{cellwidth.Truncate(s.b.Tf("integrations.preset.note", map[string]string{
		"preset": s.presetWord(),
		"on":     fmt.Sprint(s.onCount()),
		"total":  fmt.Sprint(len(integrations.AllFeatures())),
	}), w)}
	for _, feature := range integrations.AllFeatures() {
		if s.intentToggle(feature) == "" {
			lines = append(lines, cellwidth.Truncate(s.b.T("integrations.intent.legacy"), w))
			break
		}
	}
	return lines
}

// Draw renders the summary, the rows, and the two legends. All cell math goes
// through cellwidth.
func (s *integrationsScreen) Draw(a *App) []string {
	s.app = a
	s.syncRows()
	s.list.SetPageSize(pageSizeFor(a.Height(), 8))
	if s.loadErr != "" {
		return []string{cellwidth.Truncate(s.b.T("common.error")+": "+s.loadErr, a.Width()),
			"", integrationsChromeHint}
	}
	lines := s.summaryLines(a.Width())
	lines = append(lines, "")
	lines = append(lines, s.list.Render(a.Width())...)
	lines = append(lines, "", integrationsChromeHint, s.list.FooterHint())
	return lines
}

// HandleKey routes one key: the shared list keys, the r refresh gesture, and
// nothing else (every write goes through a row activation and its gate).
func (s *integrationsScreen) HandleKey(k Key, a *App) {
	s.app = a
	if isRefreshKey(k) {
		s.reload()
		a.SetMessage("")
		return
	}
	s.list.HandleKey(k)
}

// activate is the list callback: it composes the row's write and shows the gate.
func (s *integrationsScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.rows) {
		return
	}
	id := s.rows[idx].ID
	switch {
	case strings.HasPrefix(id, "integrations.feature."):
		feature, err := integrations.ParseFeature(strings.TrimPrefix(id, "integrations.feature."))
		if err == nil {
			s.askFeatureToggle(feature)
		}
	case id == integrationsIDPreset:
		s.askPreset()
	case id == integrationsIDApply:
		s.askWrite(func() (*integrationWrite, error) { return s.composePending() })
	case id == integrationsIDRetract:
		s.askWrite(func() (*integrationWrite, error) { return s.composeRetractAll() })
	case id == integrationsIDAdopt:
		s.askWrite(func() (*integrationWrite, error) { return s.composeAdopt() })
	case id == integrationsIDRollback:
		s.askRollback()
	case id == integrationsIDRepair:
		s.askWrite(func() (*integrationWrite, error) { return s.composeRepair() })
	}
}

// askFeatureToggle composes one feature's toggle and shows its gate.
func (s *integrationsScreen) askFeatureToggle(feature integrations.Feature) {
	enable := s.intentToggle(feature) != mode.IntegrationToggleOn
	s.askWrite(func() (*integrationWrite, error) {
		return s.composeFeatureWrite([]integrations.Feature{feature}, enable, "")
	})
}

// askPreset opens the native/tools choice (custom is derived, never chosen) and
// then the gate for the chosen posture.
func (s *integrationsScreen) askPreset() {
	a := s.app
	options := []string{string(integrations.PresetNative), string(integrations.PresetTools)}
	a.ShowModal(NewChoice(s.b.T("integrations.row.preset"), options, func(idx int, value string) {
		a.ShowModal(nil)
		if idx < 0 {
			return
		}
		preset, err := integrations.ParsePreset(value)
		if err != nil {
			a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
			return
		}
		s.askWrite(func() (*integrationWrite, error) {
			return s.composeFeatureWrite(integrations.AllFeatures(), preset == integrations.PresetTools, preset)
		})
	}))
}

// integrationsRollbackDoorWord is the rollback picker's first option: the
// settings.json door, which is what the row did before the deploy-tree verb
// existed. It is chrome (a file name), so it carries no catalog key, exactly like
// the feature words beside it: a translated word would stop matching the CLI
// operand a user copies from the picker.
const integrationsRollbackDoorWord = "settings.json"

// askRollback opens the rollback picker: settings.json through its snapshot chain
// (the row's original behaviour) or one content feature's pre-install deploy tree.
// The picker is how the tree rollback lands without growing the row frame.
func (s *integrationsScreen) askRollback() {
	a := s.app
	options := []string{integrationsRollbackDoorWord}
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			continue
		}
		options = append(options, string(feature))
	}
	a.ShowModal(NewChoice(s.b.T("integrations.action.rollback"), options, func(idx int, value string) {
		a.ShowModal(nil)
		if idx < 0 {
			return
		}
		if idx == 0 {
			s.askWrite(s.composeRollback)
			return
		}
		feature, err := integrations.ParseFeature(value)
		if err != nil {
			a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
			return
		}
		s.askWrite(func() (*integrationWrite, error) { return s.composeFeatureRollback(feature) })
	}))
}

// askWrite composes one write and, when it changes nothing, reports that instead
// of asking for consent for a no-op.
func (s *integrationsScreen) askWrite(compose func() (*integrationWrite, error)) {
	a := s.app
	write, err := compose()
	if err != nil {
		if errors.Is(err, errIntegrationNoSnapshot) {
			a.SetMessage(s.b.T("integrations.msg.no_snapshot"))
			return
		}
		if errors.Is(err, errIntegrationNothingPending) {
			a.SetMessage(s.b.T("integrations.msg.nothing_pending"))
			return
		}
		if errors.Is(err, errIntegrationNothingRollback) {
			a.SetMessage(s.b.T("integrations.msg.nothing_rollback"))
			return
		}
		if errors.Is(err, errIntegrationNothingRepair) {
			a.SetMessage(s.b.T("integrations.msg.nothing_repair"))
			return
		}
		if errors.Is(err, errIntegrationNothingAdopt) {
			a.SetMessage(s.b.T("integrations.msg.nothing_write"))
			return
		}
		a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(errText(err)))
		return
	}
	if len(write.lines) == 0 && len(write.intent) == 0 {
		// Verb-neutral by construction: this fallback covers whichever action
		// produced an empty write, so it must not name a verb the operator did
		// not invoke (adopt used to read "Nothing to repair" here).
		a.SetMessage(s.b.T("integrations.msg.nothing_write"))
		return
	}
	a.ShowModal(NewApproval(s.b.T("integrations.approval.title"), write.diff(s.b), func(approved bool) {
		a.ShowModal(nil)
		if !approved {
			a.SetMessage(s.b.T("settings.msg.cancelled"))
			return
		}
		detail, err := s.executeWrite(write)
		if err != nil {
			if errors.Is(err, errIntegrationStale) {
				a.SetMessage(s.b.T("integrations.msg.stale"))
			} else {
				a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(errText(err)))
			}
			s.reload()
			return
		}
		a.SetMessage(s.b.T("common.success") + ": " + write.title + detailSuffix(detail))
		s.reload()
	}))
}

// detailSuffix appends the engine's own count sentence to a localized result
// line. The counts are chrome (decision 6B keeps chrome English, which is also
// the language of the file paths beside them); the frame of the sentence is
// localized.
func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " · " + detail
}

// ---------------------------------------------------------------------------
// composed writes

// Errors the composers report as "there is nothing to do", so the screen can say
// so instead of showing an empty gate.
var (
	errIntegrationStale           = errors.New("state.json changed while the approval was open")
	errIntegrationNothingPending  = errors.New("nothing is pending")
	errIntegrationNothingRepair   = errors.New("nothing to repair")
	errIntegrationNothingAdopt    = errors.New("nothing to adopt")
	errIntegrationNothingRollback = errors.New("nothing to roll back")
	errIntegrationNoSnapshot      = errors.New("no settings.json snapshot")
)

// integrationIntentRow is one state.json row a gated write sets.
type integrationIntentRow struct {
	key    string
	before string
	after  string
}

// integrationWrite is ONE gated write composed by this screen: the exact-diff
// lines the approval modal shows, the intent rows Core.Set writes after them under
// the same consent, and the engine calls.
//
// The composition itself (the steps, their lines and the notes) comes from
// internal/integrations/compose.go, which both doors share; the consent
// composition is stated once there too (one diff, one approval, then every engine
// call and every intent row with no second gate). The gates live at the doors, not
// in the verbs, so Core.Set and integrations.Apply are called directly here;
// nesting one approval per row would ask the same operator the same question up to
// six times.
type integrationWrite struct {
	title  string
	lines  []string
	intent []integrationIntentRow
	// run performs the engine calls in plan order and returns a short detail
	// sentence (counts, backup path) for the message line.
	run func() (string, error)
}

// diff renders the modal summary: the engine's exact lines, then the state.json
// rows the same consent covers.
func (w *integrationWrite) diff(b *i18n.Bundle) []string {
	lines := append([]string{}, w.lines...)
	if len(w.intent) > 0 {
		lines = append(lines, b.T("integrations.approval.intent"))
		for _, row := range w.intent {
			lines = append(lines, fmt.Sprintf("  %s: %s -> %s", row.key, displayIntentValue(row.before), displayIntentValue(row.after)))
		}
	}
	return lines
}

// displayIntentValue renders an intent value, spelling out the absent row the
// store drops through omitempty.
func displayIntentValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(unset)"
	}
	return v
}

// executeWrite re-verifies the presented intent rows, runs the engine calls, and
// then writes the rows. A moved row refuses the whole write before the first
// engine call (the CLI door's stale-base rule, and the same rule the M4 modal
// keeps for every other screen).
func (s *integrationsScreen) executeWrite(w *integrationWrite) (string, error) {
	if len(w.intent) > 0 {
		st, err := mode.LoadState()
		if err != nil || st == nil {
			return "", errors.New("state.json re-read failed before the write")
		}
		for _, row := range w.intent {
			if current := intentRowValue(st, row.key); current != row.before {
				return "", errIntegrationStale
			}
		}
	}
	detail, err := w.run()
	if err != nil {
		return "", err
	}
	for _, row := range w.intent {
		if err := s.core.Set(row.key, row.after); err != nil {
			return detail, err
		}
	}
	return detail, nil
}

// intentRowValue reads one intent row with the same closed key table Core.Set
// writes (P5-3 T6). It is the re-verify side of the stale-base check.
func intentRowValue(st *mode.State, key string) string {
	switch key {
	case "integration-preset":
		return st.IntegrationPreset
	case "integration-statusline":
		return st.IntegrationStatusline
	case "integration-hook":
		return st.IntegrationHook
	case "integration-rules":
		return st.IntegrationRules
	case "integration-plugins":
		return st.IntegrationPlugins
	case "integration-skills":
		return st.IntegrationSkills
	}
	return ""
}

// composeFeatureWrite composes the toggle/apply/retract write for a feature set
// through the shared composer (internal/integrations/compose.go).
//
// enable true deploys every listed feature and records intent on; false removes
// them and records intent off. preset is the explicit posture row to record, or ""
// to derive it from the resulting toggle map by the shared rule (tools when every
// feature is on, native when every feature is off, custom otherwise, and nothing at
// all when no feature ends up on).
func (s *integrationsScreen) composeFeatureWrite(features []integrations.Feature, enable bool, preset integrations.Preset) (*integrationWrite, error) {
	verb := "retract"
	if enable {
		verb = "apply"
	}
	// A preset-row apply is the whole-posture apply (T8 §2 rule 5): the operator
	// chose the posture, not one feature, so a settings door that cannot be planned
	// is subtracted with a note exactly like the CLI's `apply tools`, and a
	// launcherless machine still deploys the content features. Every other write
	// this screen composes names its target - the per-feature toggles, and the
	// pending row, which the CLI degrades only because intent led it there - so it
	// keeps the loud error.
	doorExplicit := !(enable && preset != "")
	write, facts, err := integrations.ComposeFeatureWrite(integrations.FeatureRequest{
		Verb:     verb,
		Features: features,
		Enable:   enable,
		Content:  profiles.GetToolsFS(),
		Intent:   s.intentReader(),
		Preset:   preset,
		// The operator picked this row, so the intent it states is recorded; that
		// half stays the opposite of the CLI's operand-less pending apply.
		RecordIntent: true,
		DoorExplicit: doorExplicit,
	})
	if err != nil {
		return nil, err
	}
	return s.writeFrom(write, facts, s.writeTitle(features, enable, preset)), nil
}

// writeFrom binds one shared composition to this screen's consent renderer: the
// exact diff lines, the intent rows, and the engine calls under one approval.
//
// The intent rows stay here on purpose. Their key spelling, the stored word and the
// stale-base re-verify are the state layer's vocabulary (internal/mode and
// internal/settings own it), which is why the composer reports posture FACTS and
// this method turns them into rows.
func (s *integrationsScreen) writeFrom(write *integrations.Write, facts *integrations.IntentPlan, title string) *integrationWrite {
	out := &integrationWrite{title: title, lines: write.Lines()}
	if facts != nil {
		for _, row := range facts.Features {
			out.intent = appendIntentRow(out.intent, s.st, "integration-"+string(row.Feature), intentValue(row.After))
		}
		if facts.Preset != "" {
			out.intent = appendIntentRow(out.intent, s.st, "integration-preset", string(facts.Preset))
		}
	}
	out.run = func() (string, error) {
		var details []string
		for _, step := range write.Steps() {
			result, err := step.Run()
			if err != nil {
				return "", err
			}
			if result.Detail != "" {
				details = append(details, result.Detail)
			}
		}
		return strings.Join(details, ", "), nil
	}
	return out
}

// intentReader binds the state layer's stored words to the engine's intent enum.
// The binding lives at the door because the spelling is state.json's (internal/mode
// owns it, and internal/integrations cannot import it; see the engine's compose.go).
func (s *integrationsScreen) intentReader() integrations.IntentReader {
	return func(feature integrations.Feature) integrations.Intent {
		switch s.intentToggle(feature) {
		case mode.IntegrationToggleOn:
			return integrations.IntentOn
		case mode.IntegrationToggleOff:
			return integrations.IntentOff
		}
		return integrations.IntentUnset
	}
}

// intentValue renders one engine intent as the word state.json stores.
func intentValue(intent integrations.Intent) string {
	switch intent {
	case integrations.IntentOn:
		return mode.IntegrationToggleOn
	case integrations.IntentOff:
		return mode.IntegrationToggleOff
	}
	return ""
}

// writeTitle is the localized one-line description the approval modal's result
// message carries (the summary lines themselves are the engine's exact diff).
func (s *integrationsScreen) writeTitle(features []integrations.Feature, enable bool, preset integrations.Preset) string {
	if preset != "" {
		return s.b.Tf("integrations.approval.preset", map[string]string{"preset": string(preset)})
	}
	if len(features) == 1 {
		key := "integrations.approval.toggle_off"
		if enable {
			key = "integrations.approval.toggle_on"
		}
		return s.b.Tf(key, map[string]string{"feature": string(features[0])})
	}
	if enable {
		return s.b.T("integrations.approval.apply_pending")
	}
	return s.b.T("integrations.approval.retract_all")
}

// appendIntentRow appends one intent row, skipping a row whose value does not
// change so the gate lists exactly the rows that move.
func appendIntentRow(rows []integrationIntentRow, st *mode.State, key, after string) []integrationIntentRow {
	before := ""
	if st != nil {
		before = intentRowValue(st, key)
	}
	if before == after {
		return rows
	}
	return append(rows, integrationIntentRow{key: key, before: before, after: after})
}

// composePending composes the operand-less apply: every feature whose intent is
// on. Unlike the CLI door it does not fall back to the manifest on a machine with
// no intent at all: this screen shows that posture per row, so the operator picks
// a target instead of guessing what a fallback would deploy.
func (s *integrationsScreen) composePending() (*integrationWrite, error) {
	if len(s.pending) == 0 {
		return nil, errIntegrationNothingPending
	}
	return s.composeFeatureWrite(s.pending, true, "")
}

// composeRetractAll composes the full removal and records the native preset.
func (s *integrationsScreen) composeRetractAll() (*integrationWrite, error) {
	return s.composeFeatureWrite(integrations.AllFeatures(), false, integrations.PresetNative)
}

// composeAdopt composes the record-only adopt through the shared composer: the
// settings door's slot plus every content feature whose destinations already hold
// the engine's bytes. A destination that differs is never adopted (that is an
// apply), and the diff says which verb would.
func (s *integrationsScreen) composeAdopt() (*integrationWrite, error) {
	write, err := integrations.ComposeAdopt(profiles.GetToolsFS())
	if err != nil {
		return nil, err
	}
	if !write.Writes() {
		return nil, errIntegrationNothingAdopt
	}
	return s.writeFrom(write, nil, s.b.T("integrations.approval.adopt")), nil
}

// composeRollback composes the settings.json restore through the shared composer.
// A machine with no snapshot is a named empty state, not an empty gate and not a
// crash.
func (s *integrationsScreen) composeRollback() (*integrationWrite, error) {
	write, err := integrations.ComposeSettingsRollback()
	if errors.Is(err, integrations.ErrNoSettingsSnapshot) {
		return nil, errIntegrationNoSnapshot
	}
	if err != nil {
		return nil, err
	}
	return s.writeFrom(write, nil, s.b.T("integrations.approval.rollback")), nil
}

// composeFeatureRollback composes one content feature's deploy-tree rollback. A
// feature the manifest records nothing for -- never applied, already retracted, or
// every destination a named skip -- is an empty state on the message line, not a
// gate over a write that cannot happen.
func (s *integrationsScreen) composeFeatureRollback(feature integrations.Feature) (*integrationWrite, error) {
	write, err := integrations.ComposeFeatureRollback(feature)
	if err != nil {
		return nil, err
	}
	if len(write.Steps()) == 0 {
		return nil, errIntegrationNothingRollback
	}
	title := s.b.Tf("integrations.approval.rollback_feature", map[string]string{"feature": string(feature)})
	return s.writeFrom(write, nil, title), nil
}

// composeRepair composes the drift reconciliation through the shared composer: a
// feature the manifest already claims whose destinations drifted or went missing is
// re-planned and re-applied (the engine's Plan+Apply IS the repair). Foreign files
// are named and left alone, and a feature with nothing recorded is never invented.
func (s *integrationsScreen) composeRepair() (*integrationWrite, error) {
	write := integrations.ComposeRepair(profiles.GetToolsFS())
	if len(write.Steps()) == 0 {
		return nil, errIntegrationNothingRepair
	}
	return s.writeFrom(write, nil, s.b.T("integrations.approval.repair")), nil
}
