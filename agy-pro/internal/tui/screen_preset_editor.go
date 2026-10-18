package tui

import (
	"strconv"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// Editor zones: which sub-component owns the keys. The scalar fields live in
// two Form instances split around the allow_hosts sub-list, so the grid
// order is name..max_native, the host rows, then search_endpoint..json.
const (
	editorZoneA = iota
	editorZoneHosts
	editorZoneB
)

// Row IDs of the allow_hosts sub-list. Host rows carry ID "host\x00<host>"
// and always sort before the two action rows.
const (
	hostRowPrefix = "host\x00"
	hostRowAdd    = "add"
	hostRowRemove = "remove"
)

// Engine-default rung values the editor seeds the read-only built-in view
// with. They mirror the flag defaults and engine withDefaults literals in
// internal/fanout (cli.go defaultFanout* consts, engine.go DefaultTimeoutSec);
// they are re-declared here because those consts are unexported, and the
// full-suite fanout tests pin the engine side of the pair.
const (
	presetEngineDefaultTask     = "review"
	presetEngineDefaultN        = 3
	presetEngineDefaultTimeout  = fanout.DefaultTimeoutSec
	presetEngineDefaultTools    = "read,search"
	presetEngineDefaultMaxTurns = 10
	presetEngineDefaultRoot     = "."
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved).
const (
	// catalog-resolved, fallback kept: presets.editor.field.name
	presetsEditorFieldName = "name"
	// catalog-resolved, fallback kept: presets.editor.field.task
	presetsEditorFieldTask = "task"
	// catalog-resolved, fallback kept: presets.editor.field.n
	presetsEditorFieldN = "n"
	// catalog-resolved, fallback kept: presets.editor.field.timeout_seconds
	presetsEditorFieldTimeout = "timeout_seconds"
	// catalog-resolved, fallback kept: presets.editor.field.tools
	presetsEditorFieldTools = "tools"
	// catalog-resolved, fallback kept: presets.editor.field.max_turns
	presetsEditorFieldMaxTurns = "max_turns"
	// catalog-resolved, fallback kept: presets.editor.field.max_native
	presetsEditorFieldMaxNative = "max_native"
	// catalog-resolved, fallback kept: presets.editor.field.allow_hosts
	presetsEditorFieldAllowHosts = "allow_hosts"
	// catalog-resolved, fallback kept: presets.editor.field.search_endpoint
	presetsEditorFieldSearchEndpoint = "search_endpoint"
	// catalog-resolved, fallback kept: presets.editor.field.search_key_env
	presetsEditorFieldSearchKeyEnv = "search_key_env"
	// catalog-resolved, fallback kept: presets.editor.field.out_dir
	presetsEditorFieldOutDir = "out_dir"
	// catalog-resolved, fallback kept: presets.editor.field.root
	presetsEditorFieldRoot = "root"
	// catalog-resolved, fallback kept: presets.editor.field.models
	presetsEditorFieldModels = "models"
	// catalog-resolved, fallback kept: presets.editor.field.seats
	presetsEditorFieldSeats = "seats"
	// catalog-resolved, fallback kept: presets.editor.field.json
	presetsEditorFieldJSON = "json"
	// catalog-resolved, fallback kept: presets.editor.hosts.add
	presetsEditorHostsAdd = "Add host"
	// catalog-resolved, fallback kept: presets.editor.hosts.remove
	presetsEditorHostsRemove = "Remove host"
	// catalog-resolved, fallback kept: presets.editor.hint
	presetsEditorHint = "Tab next section · Enter next field · Ctrl+S save · Esc or Right close"
	// catalog-resolved, fallback kept: presets.editor.hint_builtin
	presetsEditorHintBuiltin = "Read-only view of the engine defaults. Esc or Right closes."
	// catalog-resolved, fallback kept: presets.editor.readonly
	presetsEditorReadonly = "Built-in defaults are read-only."
	// catalog-resolved, fallback kept: presets.editor.discard.title
	presetsEditorDiscardTitle = "Discard Changes?"
	// catalog-resolved, fallback kept: presets.editor.discard.body
	presetsEditorDiscardBody = "Unsaved changes will be lost."
	// catalog-resolved, fallback kept: presets.editor.saved
	presetsEditorSaved = "Preset {{name}} saved."
	// catalog-resolved, fallback kept: presets.overwrite.title
	presetsOverwriteTitle = "Overwrite Preset?"
	// catalog-resolved, fallback kept: presets.overwrite.body
	presetsOverwriteBody = "Preset {{name}} already exists. Overwrite it?"
	// catalog-resolved, fallback kept: presets.editor.validation.name
	presetsEditorValidationName = "Preset name must not be empty."
	// catalog-resolved, fallback kept: presets.editor.validation.number
	presetsEditorValidationNumber = "{{field}} must be a number or empty."
	// catalog-resolved, fallback kept: presets.editor.validation.tools
	presetsEditorValidationTools = "tools must be a comma list of read,search,write,exec."
	// catalog-resolved, fallback kept: presets.editor.validation.json
	presetsEditorValidationJSON = "json must be true, false, or empty."
	// catalog-resolved, fallback kept: presets.editor.validation.key_env
	presetsEditorValidationKeyEnv = "search_key_env must be an environment variable NAME, not a key value."
	// MISSING-KEY: presets.editor.validation.secret
	presetsEditorValidationSecret = "{{field}} looks like secret or key material and cannot be saved (value hidden). Preset fields store names and paths, never keys."
	// catalog-resolved, fallback kept: presets.editor.source.state
	presetsEditorSourceState = "state.json"
	// catalog-resolved, fallback kept: presets.editor.source.none
	presetsEditorSourceNone = "(none)"
)

// presetEditorScreen is S8: the fanout preset editor (architect-report
// section 3, screen 8). A field grid over every pre-configurable knob, with
// a read-only source column per field naming the resolution rung that
// currently supplies the effective value (mirroring buildRunConfig's
// ladder). Save writes ONLY fanout-presets.json through the store (atomic
// 0600); providers.json is never written from this screen.
//
// Esc handling: the App dispatcher consumes Esc before any screen sees it
// (clears the message, then pops), so a dirty editor would silently lose
// edits. The screen therefore follows the model form pattern
// (screen_model_form.go): while the editor is dirty a transparent guard
// modal (presetEditorGuard) receives every key in CONFIRM scope before the
// App Esc precedence, forwards everything except Esc to the editor, and
// turns Esc on a dirty editor into the discard-confirm. Right stays the
// explicit close gesture; a clean Esc never arms the guard and pops
// through the plain App precedence.
type presetEditorScreen struct {
	name       string // original preset name ("" = new)
	builtin    bool   // read-only view of the engine defaults
	store      *fanout.PresetFileStore
	preset     *fanout.Preset // on-disk preset being edited (nil for new)
	notFound   string
	def        providers.Defaults
	valA       []string // seeded zone-A values (name..max_native)
	valB       []string // seeded zone-B values (search_endpoint..json)
	formA      *Form    // name, task, n, timeout_seconds, tools, max_turns, max_native
	formB      *Form    // search_endpoint, search_key_env, out_dir, root, models, seats, json
	hosts      *stepList
	hostVals   []string
	hostsSel   int
	pending    string // add-host inline input
	hostsDirty bool
	zone       int
	b          *i18n.Bundle
	lang       string
	app        *App
	guard      *presetEditorGuard
	corruptLatch
}

// NewPresetEditorScreen builds the editor for one preset. An empty name
// edits a new preset; builtinPresetSentinel opens the read-only built-in
// defaults view. The store resolves through fanout.DefaultPresetsPath
// (AGY_PRO_HOME aware); tests point AGY_PRO_HOME at a temp dir.
func NewPresetEditorScreen(name string) Screen {
	e := &presetEditorScreen{}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	e.setLang(i18n.Normalize(lang))
	if name == builtinPresetSentinel {
		e.builtin = true
	} else {
		e.name = name
	}
	if st, err := fanout.NewPresetFileStore(); err == nil {
		e.store = st
		// Surface the store's corrupt-file warning instead of letting the
		// nil WarnOut discard it: saving into an empty fallback store must
		// never silently overwrite the user's presets (security F5).
		st.WarnOut = corruptPresetSink{count: &e.corruptLatch.corruptEvents}
		before := e.corruptLatch.corruptEvents
		pf, err := st.LoadPresets()
		loadErr := ""
		if err != nil {
			loadErr = err.Error()
		}
		e.corruptLatch.reconcile(e.corruptLatch.corruptEvents > before, loadErr)
		if err == nil && pf != nil {
			if e.builtin {
				// The built-in view seeds the engine defaults; no store row.
			} else if e.name != "" {
				if p, ok := pf.FindPreset(e.name); ok {
					cp := *p
					cp.AllowHosts = append([]string(nil), p.AllowHosts...)
					e.preset = &cp
				} else {
					e.notFound = e.name
				}
			}
		}
	}
	if reg, err := providers.Load(); err == nil && reg != nil {
		e.def = reg.Defaults // read-only ladder input; never written here
	}
	// Seed values come before the forms are built: Form keeps its fields
	// unexported, so values are baked into the Field definitions.
	e.valA = []string{"", "", "", "", "", "", ""}
	e.valB = []string{"", "", "", "", "", "", ""}
	if e.builtin {
		e.valA = []string{builtinPresetRowLabel, presetEngineDefaultTask,
			strconv.Itoa(presetEngineDefaultN), strconv.Itoa(presetEngineDefaultTimeout),
			presetEngineDefaultTools, strconv.Itoa(presetEngineDefaultMaxTurns), ""}
		e.valB = []string{"", "", "", presetEngineDefaultRoot, "", "", "false"}
	} else if e.preset != nil {
		p := e.preset
		e.valA = []string{p.Name, p.Task, intText(p.N), intText(p.TimeoutSeconds),
			p.Tools, intText(p.MaxTurns), intText(p.MaxNative)}
		jsonVal := ""
		if p.JSON {
			jsonVal = "true"
		}
		e.valB = []string{p.SearchEndpoint, p.SearchKeyEnv, p.OutDir, p.Root,
			p.Models, p.Seats, jsonVal}
		e.hostVals = append([]string(nil), p.AllowHosts...)
	}
	e.formA = NewForm(e.fieldsA())
	e.formB = NewForm(e.fieldsB())
	e.hosts = newStepList(e.hostPick)
	e.hostsSel = -1
	e.syncHostRows()
	return e
}

// setLang loads the bundle for lang once per language.
func (e *presetEditorScreen) setLang(lang string) {
	if lang == e.lang && e.b != nil {
		return
	}
	e.lang = lang
	e.b = i18n.Load(lang)
}

// Mode is ModeEdit: the field values accept text through EditRune.
func (e *presetEditorScreen) Mode() Mode { return ModeEdit }

// Title returns the editor title.
func (e *presetEditorScreen) Title() string {
	if e.builtin {
		return builtinPresetRowLabel
	}
	return e.b.T("presets.editor.title")
}

// fieldsA builds the zone-A field definitions (grid rows above the host
// sub-list), with the seeded values baked in.
func (e *presetEditorScreen) fieldsA() []Field {
	return []Field{
		{Label: presetsEditorFieldName, Value: e.valA[0], Validate: e.validateName},
		{Label: presetsEditorFieldTask, Value: e.valA[1]},
		{Label: presetsEditorFieldN, Value: e.valA[2], Validate: e.validateNumber(presetsEditorFieldN)},
		{Label: presetsEditorFieldTimeout, Value: e.valA[3], Validate: e.validateNumber(presetsEditorFieldTimeout)},
		{Label: presetsEditorFieldTools, Value: e.valA[4], Validate: e.validateTools},
		{Label: presetsEditorFieldMaxTurns, Value: e.valA[5], Validate: e.validateNumber(presetsEditorFieldMaxTurns)},
		{Label: presetsEditorFieldMaxNative, Value: e.valA[6], Validate: e.validateNumber(presetsEditorFieldMaxNative)},
	}
}

// fieldsB builds the zone-B field definitions (grid rows below the host
// sub-list), with the seeded values baked in.
func (e *presetEditorScreen) fieldsB() []Field {
	return []Field{
		{Label: presetsEditorFieldSearchEndpoint, Value: e.valB[0]},
		{Label: presetsEditorFieldSearchKeyEnv, Value: e.valB[1], Validate: e.validateKeyEnv},
		{Label: presetsEditorFieldOutDir, Value: e.valB[2]},
		{Label: presetsEditorFieldRoot, Value: e.valB[3]},
		{Label: presetsEditorFieldModels, Value: e.valB[4]},
		{Label: presetsEditorFieldSeats, Value: e.valB[5]},
		{Label: presetsEditorFieldJSON, Value: e.valB[6], Validate: e.validateJSON},
	}
}

// validateName rejects an empty preset name.
func (e *presetEditorScreen) validateName(s string) error {
	if strings.TrimSpace(s) == "" {
		return strError(missingOr(e.b, "presets.editor.validation.name", presetsEditorValidationName))
	}
	return nil
}

// validateNumber accepts empty (unset, ladder fall-through) or a
// non-negative integer.
func (e *presetEditorScreen) validateNumber(field string) func(string) error {
	return func(s string) error {
		if _, err := parsePresetInt(s); err != nil {
			return strError(fillHubSlots(
				missingOr(e.b, "presets.editor.validation.number", presetsEditorValidationNumber),
				map[string]string{"field": field}))
		}
		return nil
	}
}

// validateTools accepts empty or a comma list of the closed tool tokens.
func (e *presetEditorScreen) validateTools(s string) error {
	if validPresetTools(s) {
		return nil
	}
	return strError(missingOr(e.b, "presets.editor.validation.tools", presetsEditorValidationTools))
}

// validateJSON accepts empty, true, or false.
func (e *presetEditorScreen) validateJSON(s string) error {
	switch strings.TrimSpace(s) {
	case "", "true", "false":
		return nil
	}
	return strError(missingOr(e.b, "presets.editor.validation.json", presetsEditorValidationJSON))
}

// validateKeyEnv accepts empty or an environment-variable NAME vetted by
// providers.ValidateAPIKeyEnvName. A key VALUE is rejected; the error text
// never echoes the operand.
func (e *presetEditorScreen) validateKeyEnv(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if err := providers.ValidateAPIKeyEnvName(s); err != nil {
		return strError(missingOr(e.b, "presets.editor.validation.key_env", presetsEditorValidationKeyEnv))
	}
	return nil
}

// parsePresetInt parses an unset-or-non-negative-integer field value.
func parsePresetInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, strError("not a number")
	}
	return n, nil
}

// validPresetTools reports whether s is empty or a comma list of the closed
// fanout tool tokens (the --tools spelling: read,search,write,exec).
func validPresetTools(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	for _, tok := range strings.Split(s, ",") {
		switch strings.TrimSpace(tok) {
		case "read", "search", "write", "exec":
		default:
			return false
		}
	}
	return true
}

// intText renders an unset-or-positive int as field text (0 = empty).
func intText(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// strError is the error adapter for the Form.Validate signatures.
type strError string

func (s strError) Error() string { return string(s) }

// syncHostRows rebuilds the allow_hosts sub-list rows: one per host, then
// Add (carrying the inline pending input) and Remove (carrying the sticky
// selection). The cursor is preserved and clamped.
func (e *presetEditorScreen) syncHostRows() {
	rows := make([]Row, 0, len(e.hostVals)+2)
	for _, h := range e.hostVals {
		rows = append(rows, Row{ID: hostRowPrefix + h, Label: h})
	}
	add := missingOr(e.b, "presets.editor.hosts.add", presetsEditorHostsAdd)
	if e.pending != "" {
		add += ": " + e.pending
	}
	rows = append(rows, Row{ID: hostRowAdd, Label: add})
	remove := missingOr(e.b, "presets.editor.hosts.remove", presetsEditorHostsRemove)
	note := ""
	if e.hostsSel >= 0 && e.hostsSel < len(e.hostVals) {
		note = e.hostVals[e.hostsSel]
	}
	rows = append(rows, Row{ID: hostRowRemove, Label: remove, Note: note})
	cursor := e.hosts.Cursor()
	e.hosts.SetRows(rows)
	e.hosts.SetCursor(cursor)
}

// hostPick is the allow_hosts activation callback. Host rows arm the sticky
// selection for Remove; the Add row commits the inline pending input; the
// Remove row drops the armed host.
func (e *presetEditorScreen) hostPick(idx int) {
	if idx < 0 || idx >= e.hosts.Len() {
		return
	}
	switch id := e.hosts.Row(idx).ID; {
	case strings.HasPrefix(id, hostRowPrefix):
		e.hostsSel = idx
	case id == hostRowAdd:
		e.commitPendingHost()
	case id == hostRowRemove:
		e.removeSelectedHost()
	}
}

// commitPendingHost normalizes and appends the inline input. The engine
// re-normalizes at build time; storing the canonical shape keeps the file
// deterministic. Duplicate and empty entries are dropped.
func (e *presetEditorScreen) commitPendingHost() {
	norm := agenttools.NormalizeAllowHosts([]string{e.pending})
	e.pending = ""
	if len(norm) == 0 {
		return
	}
	for _, h := range e.hostVals {
		if h == norm[0] {
			e.syncHostRows()
			return
		}
	}
	e.hostVals = append(e.hostVals, norm[0])
	e.hostsDirty = true
	e.syncHostRows()
}

// removeSelectedHost drops the armed host, if any.
func (e *presetEditorScreen) removeSelectedHost() {
	if e.hostsSel < 0 || e.hostsSel >= len(e.hostVals) {
		return
	}
	e.hostVals = append(e.hostVals[:e.hostsSel], e.hostVals[e.hostsSel+1:]...)
	e.hostsSel = -1
	e.hostsDirty = true
	e.syncHostRows()
}

// dirty reports unsaved edits across both forms and the host list.
func (e *presetEditorScreen) dirty() bool {
	return e.formA.Dirty() || e.formB.Dirty() || e.hostsDirty
}

// HandleKey runs one key with no modal live: dispatch it, then arm the
// dirty guard. Esc never arrives here (App precedence); Right requests
// close; Ctrl+S saves.
func (e *presetEditorScreen) HandleKey(k Key, a *App) {
	e.app = a
	e.dispatch(k)
	if !e.builtin && e.dirty() && a.Modal() == nil {
		e.rearmGuard(a)
	}
}

// rearmGuard (re)opens the dirty guard. Callers running inside a modal
// key callback rely on ShowModal replacing the live modal, so the App
// stale-modal clear never removes a freshly armed one.
func (e *presetEditorScreen) rearmGuard(a *App) {
	if e.builtin || !e.dirty() {
		return
	}
	if e.guard == nil {
		e.guard = &presetEditorGuard{e: e}
	}
	a.ShowModal(e.guard)
}

// dispatch routes one key by zone. Ctrl+S saves; Right requests close
// (dirty editors get the discard gate); Tab/Down/Up walk the three zones;
// zone keys go to the owning component. The guard forwards here, so every
// key path runs the same routing.
func (e *presetEditorScreen) dispatch(k Key) {
	if e.builtin {
		switch k {
		case KeyRight:
			e.app.Pop()
		case KeyCtrlS:
			e.app.SetMessage(missingOr(e.b, "presets.editor.readonly", presetsEditorReadonly))
		}
		return
	}
	switch k {
	case KeyCtrlS:
		e.save()
		return
	case KeyRight:
		e.requestClose()
		return
	}
	switch e.zone {
	case editorZoneA:
		e.handleZoneA(k)
	case editorZoneHosts:
		e.handleZoneHosts(k)
	case editorZoneB:
		e.handleZoneB(k)
	}
}

// handleZoneA forwards to the zone-A form. Tab, Down, or Enter on the last
// field (max_native) moves the focus into the host sub-list.
func (e *presetEditorScreen) handleZoneA(k Key) {
	before := e.formA.Cursor()
	e.formA.HandleKey(k)
	if (k == KeyTab || k == KeyDown || k == KeyEnter) && before == e.formA.Len()-1 {
		e.zone = editorZoneHosts
	}
}

// handleZoneHosts routes keys inside the allow_hosts sub-list. On the Add
// row, printable runes (digits included) type into the inline input,
// Backspace edits it, and Enter commits; the inline input caps at
// maxLineRunes on the insertion path exactly like a form field, so a large
// paste cannot grow it without bound (seeding existing host values into
// hostVals stays uncapped, mirroring the editor/form seeding rule). On every
// other row the keys go to the shared stepList, so arrows and digits both
// navigate (digits are text only on the Add row, the mode-scoped dual
// meaning from the key map).
// Tab or Down on the last row moves into zone B; Up on the first row moves
// back to zone A.
func (e *presetEditorScreen) handleZoneHosts(k Key) {
	if e.hosts.Len() == 0 {
		return
	}
	// Tab walks the host rows and crosses into zone B on the last one (the
	// stepList itself does not consume Tab).
	if k == KeyTab {
		if e.hosts.Cursor() < e.hosts.Len()-1 {
			e.hosts.SetCursor(e.hosts.Cursor() + 1)
		} else {
			e.zone = editorZoneB
		}
		return
	}
	onAdd := e.hosts.Row(e.hosts.Cursor()).ID == hostRowAdd
	if onAdd {
		switch k {
		case KeyEnter:
			e.commitPendingHost()
			return
		case KeyBackspace:
			if e.pending != "" {
				rs := []rune(e.pending)
				e.pending = string(rs[:len(rs)-1])
				e.syncHostRows()
			}
			return
		default:
			if r, ok := EditRune(k); ok {
				if len([]rune(e.pending)) >= maxLineRunes {
					if e.app != nil {
						e.app.SetMessage(formLimitMsg)
					}
					return
				}
				e.pending += string(r)
				e.syncHostRows()
				return
			}
		}
	}
	before := e.hosts.Cursor()
	e.hosts.HandleKey(k)
	switch k {
	case KeyDown:
		if before == e.hosts.Len()-1 {
			e.zone = editorZoneB
		}
	case KeyUp:
		if before == 0 {
			e.zone = editorZoneA
		}
	}
}

// handleZoneB forwards to the zone-B form. Enter on the last field (json)
// saves, mirroring Ctrl+S; Tab on the last field wraps to zone A; Up on the
// first field moves back into the host sub-list.
func (e *presetEditorScreen) handleZoneB(k Key) {
	if k == KeyEnter && e.formB.Cursor() == e.formB.Len()-1 {
		e.save()
		return
	}
	before := e.formB.Cursor()
	e.formB.HandleKey(k)
	if k == KeyTab && before == e.formB.Len()-1 {
		e.zone = editorZoneA
		e.formA.SetCursor(0)
	}
	if k == KeyUp && before == 0 {
		e.zone = editorZoneHosts
	}
}

// requestClose pops the editor, gating a dirty form behind the discard
// confirm. Right is the explicit close gesture; Esc reaches the same gate
// through the armed guard (see presetEditorGuard). The pop dismisses the
// editor's own modal (the transparent guard or the discard confirm): Pop
// alone would leave it live, and the ghost would swallow the next screen's
// keys until an Esc cleared it.
func (e *presetEditorScreen) requestClose() {
	a := e.app
	if a == nil {
		return
	}
	if !e.dirty() {
		a.ShowModal(nil)
		a.Pop()
		return
	}
	a.ShowModal(NewConfirm(missingOr(e.b, "presets.editor.discard.title", presetsEditorDiscardTitle),
		[]string{missingOr(e.b, "presets.editor.discard.body", presetsEditorDiscardBody)},
		func(approved bool) {
			if approved {
				a.ShowModal(nil)
				a.Pop()
				return
			}
			e.rearmGuard(a)
		}))
}

// askDiscard is the Esc-on-dirty path from the guard. y drops the edits
// and pops back to the caller (dismissing the discard confirm with it); n/Esc
// returns to the editor with field state preserved and the guard re-armed.
func (e *presetEditorScreen) askDiscard() {
	a := e.app
	if a == nil {
		return
	}
	a.ShowModal(NewConfirm(missingOr(e.b, "presets.editor.discard.title", presetsEditorDiscardTitle),
		[]string{missingOr(e.b, "presets.editor.discard.body", presetsEditorDiscardBody)},
		func(approved bool) {
			if approved {
				a.ShowModal(nil)
				a.Pop()
				return
			}
			e.rearmGuard(a)
		}))
}

// collect gathers and validates the grid into a Preset record. Every error
// text names the field only; user operands (in particular a misdirected key
// paste) are never echoed.
func (e *presetEditorScreen) collect() (fanout.Preset, error) {
	var p fanout.Preset
	p.Name = strings.TrimSpace(e.formA.Value(0))
	if p.Name == "" {
		return p, strError(missingOr(e.b, "presets.editor.validation.name", presetsEditorValidationName))
	}
	p.Task = strings.TrimSpace(e.formA.Value(1))
	var err error
	if p.N, err = parsePresetInt(e.formA.Value(2)); err != nil {
		return p, e.numberErr(presetsEditorFieldN)
	}
	if p.TimeoutSeconds, err = parsePresetInt(e.formA.Value(3)); err != nil {
		return p, e.numberErr(presetsEditorFieldTimeout)
	}
	p.Tools = strings.TrimSpace(e.formA.Value(4))
	if !validPresetTools(p.Tools) {
		return p, strError(missingOr(e.b, "presets.editor.validation.tools", presetsEditorValidationTools))
	}
	if p.MaxTurns, err = parsePresetInt(e.formA.Value(5)); err != nil {
		return p, e.numberErr(presetsEditorFieldMaxTurns)
	}
	if p.MaxNative, err = parsePresetInt(e.formA.Value(6)); err != nil {
		return p, e.numberErr(presetsEditorFieldMaxNative)
	}
	p.SearchEndpoint = strings.TrimSpace(e.formB.Value(0))
	p.SearchKeyEnv = strings.TrimSpace(e.formB.Value(1))
	if p.SearchKeyEnv != "" {
		if err := providers.ValidateAPIKeyEnvName(p.SearchKeyEnv); err != nil {
			return p, strError(missingOr(e.b, "presets.editor.validation.key_env", presetsEditorValidationKeyEnv))
		}
	}
	p.OutDir = strings.TrimSpace(e.formB.Value(2))
	p.Root = strings.TrimSpace(e.formB.Value(3))
	p.Models = strings.TrimSpace(e.formB.Value(4))
	p.Seats = strings.TrimSpace(e.formB.Value(5))
	switch strings.TrimSpace(e.formB.Value(6)) {
	case "", "false":
	case "true":
		p.JSON = true
	default:
		return p, strError(missingOr(e.b, "presets.editor.validation.json", presetsEditorValidationJSON))
	}
	// Save-time credential guard on the free-text slots (round-6 fix): the
	// shared classifier from the model form (secretValuePrefixes +
	// credentialTokenShaped, screen_model_form.go) refuses a credential-shaped
	// value in any free-text field, so a misdirected key paste persists into
	// fanout-presets.json only after an explicit override nowhere exists; the
	// refusal names the field label and never echoes the value. Legitimate
	// values (tasks, hostnames, seat/model lists, paths) carry separators or
	// fail the token shape and save normally. tools is a closed whitelist and
	// search_key_env has its own env-NAME rule, so neither is rechecked here.
	for _, f := range []struct {
		label string
		val   string
	}{
		{presetsEditorFieldName, p.Name},
		{presetsEditorFieldTask, p.Task},
		{presetsEditorFieldSearchEndpoint, p.SearchEndpoint},
		{presetsEditorFieldOutDir, p.OutDir},
		{presetsEditorFieldRoot, p.Root},
		{presetsEditorFieldModels, p.Models},
		{presetsEditorFieldSeats, p.Seats},
	} {
		if looksSecretValue(f.val) {
			return p, strError(fillHubSlots(
				missingOr(e.b, "presets.editor.validation.secret", presetsEditorValidationSecret),
				map[string]string{"field": f.label}))
		}
	}
	for _, h := range e.hostVals {
		if looksSecretValue(h) {
			return p, strError(fillHubSlots(
				missingOr(e.b, "presets.editor.validation.secret", presetsEditorValidationSecret),
				map[string]string{"field": presetsEditorFieldAllowHosts}))
		}
	}
	p.AllowHosts = append([]string(nil), e.hostVals...)
	return p, nil
}

// numberErr composes the number-validation message for one field.
func (e *presetEditorScreen) numberErr(field string) error {
	return strError(fillHubSlots(
		missingOr(e.b, "presets.editor.validation.number", presetsEditorValidationNumber),
		map[string]string{"field": field}))
}

// save validates the grid and writes ONLY the preset file through the store
// (atomic 0600). An overwrite of an existing preset name goes through a
// plain confirm (the preset file is not providers.json, so no M4 gate). On
// success the editor re-anchors to the saved name and marks itself clean.
func (e *presetEditorScreen) save() {
	a := e.app
	if a == nil {
		return
	}
	if e.builtin {
		a.SetMessage(missingOr(e.b, "presets.editor.readonly", presetsEditorReadonly))
		return
	}
	p, err := e.collect()
	if err != nil {
		a.SetMessage(e.b.T("common.error") + ": " + err.Error())
		return
	}
	// Fresh load through the latch: if the file turned corrupt again since
	// the last observation, a NEW episode opens and the acknowledgment below
	// re-arms the gate (one ack covers one episode, never the whole session).
	before := e.corruptLatch.corruptEvents
	pf, err := e.store.LoadPresets()
	loadErr := ""
	if err != nil {
		loadErr = err.Error()
	}
	e.corruptLatch.reconcile(e.corruptLatch.corruptEvents > before, loadErr)
	if err != nil {
		a.SetMessage(e.b.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
		return
	}
	// Refuse the first save after a corrupt load until the user acknowledges
	// it: the in-memory store is the empty fallback, so saving now would
	// silently overwrite the corrupt file and destroy any recoverable
	// presets. The confirm names the on-disk path so it can be backed up.
	if e.corruptWarn && !e.corruptAcked {
		path := providers.MaskOperand(presetFilePath(e.store))
		a.ShowModal(NewConfirm(missingOr(e.b, "presets.corrupt.title", presetsCorruptTitle),
			[]string{fillHubSlots(missingOr(e.b, "presets.corrupt.body", presetsCorruptBody),
				map[string]string{"path": path})},
			func(approved bool) {
				if !approved {
					a.SetMessage(missingOr(e.b, "presets.msg.cancelled", presetsMsgCancelled))
					return
				}
				e.corruptAcked = true
				e.save()
			}))
		return
	}
	existing := -1
	for i := range pf.Presets {
		if pf.Presets[i].Name == p.Name {
			existing = i
		}
	}
	if existing >= 0 && p.Name != e.name {
		masked := providers.MaskOperand(p.Name)
		a.ShowModal(NewConfirm(missingOr(e.b, "presets.overwrite.title", presetsOverwriteTitle),
			[]string{fillHubSlots(missingOr(e.b, "presets.overwrite.body", presetsOverwriteBody),
				map[string]string{"name": masked})},
			func(approved bool) {
				if !approved {
					a.SetMessage(missingOr(e.b, "presets.msg.cancelled", presetsMsgCancelled))
					return
				}
				e.writePreset(a, pf, p, existing)
			}))
		return
	}
	e.writePreset(a, pf, p, existing)
}

// writePreset upserts the record and persists through the store. A rename
// (e.name != p.Name) drops BOTH the old name and the target name before the
// append: the target was confirmed through the overwrite modal in save(), so
// the file must end with exactly one row for p.Name, never a duplicate. pf
// is the freshly loaded file; existing is the index of a same-named preset
// or -1.
func (e *presetEditorScreen) writePreset(a *App, pf *fanout.PresetsFile, p fanout.Preset, existing int) {
	if e.name != "" && e.name != p.Name {
		keep := pf.Presets[:0]
		for i := range pf.Presets {
			n := pf.Presets[i].Name
			if n != e.name && n != p.Name {
				keep = append(keep, pf.Presets[i])
			}
		}
		pf.Presets = keep
		existing = -1
	}
	if existing >= 0 {
		pf.Presets[existing] = p
	} else {
		pf.Presets = append(pf.Presets, p)
	}
	if err := e.store.SavePresets(pf); err != nil {
		a.SetMessage(e.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
		return
	}
	// The file just written is healthy by construction (SavePresets
	// validated and rewrote it), so close any corrupt episode: the stale
	// notice must not outlive the save that replaced the corrupt file.
	e.corruptLatch.reconcile(false, "")
	cp := p
	cp.AllowHosts = append([]string(nil), p.AllowHosts...)
	e.preset = &cp
	e.name = p.Name
	e.formA.MarkClean()
	e.formB.MarkClean()
	e.hostsDirty = false
	a.SetMessage(fillHubSlots(missingOr(e.b, "presets.editor.saved", presetsEditorSaved),
		map[string]string{"name": providers.MaskOperand(p.Name)}))
}

// sources returns the rung label per field for the source column. It is
// computed from the ON-DISK ladder (the saved preset, the providers.json
// defaults block, and an empty flag set), mirroring buildRunConfig's
// resolveLadder; unsaved form edits do not move the column. The built-in
// view is all engine defaults.
func (e *presetEditorScreen) sources() map[string]string {
	if e.builtin {
		all := map[string]string{}
		for _, k := range presetSourceKeys {
			all[k] = "engine"
		}
		all["max_native"] = "state"
		for _, k := range []string{"allow_hosts", "search_endpoint", "search_key_env"} {
			all[k] = ""
		}
		return all
	}
	return resolvePresetSources(presetLadderInputs{
		set:    map[string]bool{},
		preset: e.preset,
		def:    e.def,
	})
}

// sourceLabel maps a rung value onto its catalog label.
func (e *presetEditorScreen) sourceLabel(rung string) string {
	b := e.b
	switch rung {
	case "flag":
		return b.T("presets.editor.source.cli")
	case "preset":
		return b.T("presets.editor.source.preset")
	case "defaults":
		return b.T("presets.editor.source.registry")
	case "state":
		return missingOr(b, "presets.editor.source.state", presetsEditorSourceState)
	case "engine":
		return b.T("presets.editor.source.engine")
	default:
		return missingOr(b, "presets.editor.source.none", presetsEditorSourceNone)
	}
}

// presetSourceKeys is the field order of the source column.
var presetSourceKeys = []string{
	"name", "task", "n", "timeout_seconds", "tools", "max_turns", "max_native",
	"allow_hosts", "search_endpoint", "search_key_env", "out_dir", "root",
	"models", "seats", "json",
}

// sourceKeyA maps zone-A field indexes onto their source keys.
var sourceKeyA = []string{"name", "task", "n", "timeout_seconds", "tools", "max_turns", "max_native"}

// sourceKeyB maps zone-B field indexes onto their source keys.
var sourceKeyB = []string{"search_endpoint", "search_key_env", "out_dir", "root", "models", "seats", "json"}

// Draw renders the two form grids around the host sub-list, each row
// carrying its rung label, plus the validation message and the hint. All
// cell math goes through cellwidth.
func (e *presetEditorScreen) Draw(a *App) []string {
	e.app = a
	w := a.Width()
	src := e.sources()
	labelW := 0
	for _, k := range presetSourceKeys {
		if cw := cellwidth.String(k); cw > labelW {
			labelW = cw
		}
	}
	var lines []string
	if e.notFound != "" {
		lines = append(lines, cellwidth.Truncate(
			e.b.T("common.error")+": preset "+providers.MaskOperand(e.notFound)+" not found", w))
	}
	if e.corruptWarn {
		warn := corruptPresetWarning(e.b, presetFilePath(e.store))
		lines = append(lines, cellwidth.Truncate(warn, w))
		if !e.corruptMsgSent && a.Message() == "" {
			e.corruptMsgSent = true
			a.SetMessage(warn)
		}
	}
	lines = append(lines, e.renderForm(e.formA, e.zone == editorZoneA, sourceKeyA, src, labelW, w)...)
	lines = append(lines, e.renderHosts(src, labelW, w)...)
	lines = append(lines, e.renderForm(e.formB, e.zone == editorZoneB, sourceKeyB, src, labelW, w)...)
	if msg := e.formA.Message(); msg != "" {
		lines = append(lines, cellwidth.Truncate(msg, w))
	}
	if msg := e.formB.Message(); msg != "" {
		lines = append(lines, cellwidth.Truncate(msg, w))
	}
	hint := presetsEditorHint
	if e.builtin {
		hint = presetsEditorHintBuiltin
	}
	if a.Modal() != e.guard {
		lines = append(lines, "", cellwidth.Truncate(hint, w))
	}
	return lines
}

// renderForm renders one zone's field grid with the source column appended
// to every row. Field values render verbatim; they are the user's own
// configuration data. The search_key_env slot is the exception: an in-progress
// value is echoed masked through providers.MaskOperand (SH-4), so a
// credential-shaped paste never renders raw in a frame even before the
// save-time validation rejects it. The form keeps editing the raw value.
func (e *presetEditorScreen) renderForm(f *Form, active bool, keys []string, src map[string]string, labelW, w int) []string {
	out := make([]string, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		marker := "  "
		if active && f.Cursor() == i {
			marker = "▶ "
		}
		label := cellwidth.PadRight(f.Field(i).Label, labelW)
		val := f.Value(i)
		if keys[i] == "search_key_env" {
			val = providers.MaskOperand(val)
		}
		line := marker + label + " : " + val
		if rung, ok := src[keys[i]]; ok && keys[i] != "name" {
			line += "  " + e.sourceLabel(rung)
		}
		out = append(out, cellwidth.Truncate(line, w))
	}
	return out
}

// renderHosts renders the allow_hosts sub-list block: the field header row
// (with its rung) plus the numbered host/add/remove rows.
func (e *presetEditorScreen) renderHosts(src map[string]string, labelW, w int) []string {
	marker := "  "
	if e.zone == editorZoneHosts {
		marker = "▶ "
	}
	line := marker + cellwidth.PadRight(presetsEditorFieldAllowHosts, labelW) + " : "
	if rung := src["allow_hosts"]; rung != "" {
		line += e.sourceLabel(rung)
	} else {
		line += missingOr(e.b, "presets.editor.source.none", presetsEditorSourceNone)
	}
	lines := []string{cellwidth.Truncate(line, w)}
	e.hosts.SetPageSize(pageSizeFor(8, 1))
	lines = append(lines, e.hosts.Render(w)...)
	return lines
}

// presetLadderInputs carries the resolution-ladder inputs the source column
// mirrors: the explicitly-set CLI flags (empty in the TUI context), the
// active preset, and the providers.json defaults block (read-only).
type presetLadderInputs struct {
	set    map[string]bool
	preset *fanout.Preset
	def    providers.Defaults
}

// resolvePresetSources maps every pre-configurable field onto the rung that
// currently supplies its effective value, mirroring resolveLadder in
// internal/fanout/cli.go (flag > preset > providers.json defaults > engine,
// with the max_native state.json rung and the unsourced host/search knobs).
// Per-field env rungs do not exist (D6 reserves env spellings to the preset
// selector), so the env label in the catalog is never emitted here.
func resolvePresetSources(in presetLadderInputs) map[string]string {
	src := map[string]string{}
	p := in.preset
	has := func(k string) bool { return in.set[k] }

	// task
	switch {
	case has("task"):
		src["task"] = "flag"
	case p != nil && p.Task != "":
		src["task"] = "preset"
	default:
		src["task"] = "engine"
	}
	// n
	switch {
	case has("n"):
		src["n"] = "flag"
	case p != nil && p.N > 0:
		src["n"] = "preset"
	case in.def.N > 0:
		src["n"] = "defaults"
	default:
		src["n"] = "engine"
	}
	// timeout_seconds
	switch {
	case has("timeout"):
		src["timeout_seconds"] = "flag"
	case p != nil && p.TimeoutSeconds > 0:
		src["timeout_seconds"] = "preset"
	case in.def.TimeoutSeconds > 0:
		src["timeout_seconds"] = "defaults"
	default:
		src["timeout_seconds"] = "engine"
	}
	// tools
	switch {
	case has("tools"):
		src["tools"] = "flag"
	case p != nil && p.Tools != "":
		src["tools"] = "preset"
	case in.def.Tools != "":
		src["tools"] = "defaults"
	default:
		src["tools"] = "engine"
	}
	// max_turns
	switch {
	case has("max-turns"):
		src["max_turns"] = "flag"
	case p != nil && p.MaxTurns > 0:
		src["max_turns"] = "preset"
	case in.def.MaxTurns > 0:
		src["max_turns"] = "defaults"
	default:
		src["max_turns"] = "engine"
	}
	// max_native: the state.json rung supplies the value when neither flag
	// nor preset pins it (resolveMaxNative).
	switch {
	case has("max-native"):
		src["max_native"] = "flag"
	case p != nil && p.MaxNative > 0:
		src["max_native"] = "preset"
	default:
		src["max_native"] = "state"
	}
	// allow_hosts / search_web source: no engine default supplies these.
	if has("allow-host") {
		src["allow_hosts"] = "flag"
	} else if p != nil && len(p.AllowHosts) > 0 {
		src["allow_hosts"] = "preset"
	}
	if has("search-endpoint") {
		src["search_endpoint"] = "flag"
	} else if p != nil && p.SearchEndpoint != "" {
		src["search_endpoint"] = "preset"
	}
	if has("search-key-env") {
		src["search_key_env"] = "flag"
	} else if p != nil && p.SearchKeyEnv != "" {
		src["search_key_env"] = "preset"
	}
	// root / out_dir
	switch {
	case has("root"):
		src["root"] = "flag"
	case p != nil && p.Root != "":
		src["root"] = "preset"
	default:
		src["root"] = "engine"
	}
	switch {
	case has("out"):
		src["out_dir"] = "flag"
	case p != nil && p.OutDir != "":
		src["out_dir"] = "preset"
	default:
		src["out_dir"] = "engine"
	}
	// models / seats
	switch {
	case has("models"):
		src["models"] = "flag"
	case p != nil && p.Models != "":
		src["models"] = "preset"
	default:
		src["models"] = "engine"
	}
	switch {
	case has("seats"):
		src["seats"] = "flag"
	case p != nil && p.Seats != "":
		src["seats"] = "preset"
	default:
		src["seats"] = "engine"
	}
	// json output switch
	switch {
	case has("json"):
		src["json"] = "flag"
	case p != nil && p.JSON:
		src["json"] = "preset"
	default:
		src["json"] = "engine"
	}
	return src
}

// presetEditorGuard is the transparent dirty-editor gate, mirroring
// formGuard in screen_model_form.go. While it is armed (the editor is
// dirty) it receives every key in CONFIRM scope before the App Esc
// precedence: it forwards everything except Esc to the editor dispatch,
// and Esc on a dirty editor opens the discard-confirm. Its body carries
// the editor hint, so the armed gate doubles as the footer.
type presetEditorGuard struct {
	e *presetEditorScreen
}

func (g *presetEditorGuard) Title() string { return "" }

func (g *presetEditorGuard) Body(w int) []string {
	hint := presetsEditorHint
	if g.e.builtin {
		hint = presetsEditorHintBuiltin
	}
	return []string{hint}
}

func (g *presetEditorGuard) Footer() string { return "" }

// Key forwards one key. Esc on a dirty editor opens the discard-confirm and
// keeps the gate; Esc on a clean editor closes the gate (the Esc is
// consumed, and the App pop happens on a further Esc, plain precedence).
func (g *presetEditorGuard) Key(k Key, a *App) bool {
	e := g.e
	if k == KeyEsc {
		if !e.dirty() {
			return false
		}
		e.askDiscard()
		return true
	}
	e.dispatch(k)
	if !e.builtin && e.dirty() && a.Modal() == nil {
		a.ShowModal(g)
	}
	return true
}
