package tui

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// presetsListChrome is the non-list line count of the composed frame around
// the preset list when the explanation block renders (every console tall
// enough to hold it): the screen title, the separator under it, the status
// line, the blank under the status line, the four explanation lines naming
// what a preset is and how it is consumed (survey 2.7/U4), the blank above
// the list hint, the hint, the separator above the base footer, and the base
// footer itself. The models screen's body carries no status line, which is
// why its chrome value is 6; this body shape needs 12 with the block, 8
// without. Draw drops the block on consoles too short to hold it (it is the
// first body content a short console loses), so pins below the 80x24 budget
// keep their historical chrome.
const presetsListChrome = 12

// presetsExplainLines is the line count of the explanation block Draw
// renders above the preset list. It is part of presetsListChrome; when a
// short console cannot afford it, Draw removes exactly this many lines from
// the chrome so the status line, the notices, the list, the hint and the
// base footer stay on the frame.
const presetsExplainLines = 4

// Row IDs of the fixed rows around the preset list, in display order.
// Preset rows carry ID "preset\x00<name>" and always sort between the
// built-in row and the action rows.
const (
	presetsRowIDBuiltin    = "builtin"
	presetsRowIDNew        = "new"
	presetsRowIDEdit       = "edit"
	presetsRowIDDuplicate  = "duplicate"
	presetsRowIDDelete     = "delete"
	presetsRowIDSetDefault = "set-default"
	presetRowPrefix        = "preset\x00"
	builtinPresetRowLabel  = "(built-in defaults)"
	builtinPresetSentinel  = "\x00builtin"
	presetCopySuffix       = " copy"
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved). The consts stay so
// a catalog missing a translation still renders English.
const (
	// catalog-resolved, fallback kept: presets.status.default
	presetsStatusDefault = "Default preset: {{name}}"
	// catalog-resolved, fallback kept: presets.status.none
	presetsStatusNone = "No default preset set."
	// catalog-resolved, fallback kept: presets.msg.select_first
	presetsMsgSelectFirst = "Select a preset row first."
	// catalog-resolved, fallback kept: presets.msg.deleted
	presetsMsgDeleted = "Preset {{name}} deleted."
	// catalog-resolved, fallback kept: presets.msg.cancelled
	presetsMsgCancelled = "Cancelled. Nothing was written."
	// catalog-resolved, fallback kept: presets.msg.duplicated
	presetsMsgDuplicated = "Duplicated as {{name}}."
	// catalog-resolved, fallback kept: presets.msg.default_set
	presetsMsgDefaultSet = "Default preset set to {{name}}."
	// catalog-resolved, fallback kept: presets.corrupt.warning
	presetsCorruptWarning = "warning: {{file}} is corrupt; ignored and loaded empty. Back up {{path}} before saving; the first save overwrites it."
	// catalog-resolved, fallback kept: presets.corrupt.title
	presetsCorruptTitle = "Corrupt Preset File"
	// catalog-resolved, fallback kept: presets.corrupt.body
	presetsCorruptBody = "{{path}} is corrupt and was ignored. Saving now overwrites it. Continue?"
	// catalog-resolved, fallback kept: presets.explain.what
	presetsExplainWhat = "A preset is a named run config (seat/tool/time budgets), not a prompt."
	// catalog-resolved, fallback kept: presets.explain.run
	presetsExplainRun = "Not a seat list. Run: agy pro fanout --preset <name> · env AGY_PRO_FANOUT_PRESET"
	// catalog-resolved, fallback kept: presets.explain.file
	presetsExplainFile = "File: {{path}}"
	// catalog-resolved, fallback kept: presets.explain.default
	presetsExplainDefault = "default_preset applies when no preset is named; conflicts drop with a warning."
	// catalog-resolved, fallback kept: presets.summary.pinned
	presetsSummaryPinned = "{{count}} of 14 fields pinned: see Edit"
)

// corruptPresetSink captures the store's one-line corrupt-file fallback
// warning. The screens wire it as PresetFileStore.WarnOut at load time: a
// nil WarnOut silently discards the warning and the next save overwrites the
// user's presets with no visible notice (security review F5). The sink only
// counts the warnings; the rendered text is composed by
// corruptPresetWarning so it can carry the full on-disk path. The wiring is
// unconditional: the app shell pre-sets WarnOut on the production path
// (presetStore in app.go), and a conditional "only when nil" skip left the
// corrupt gate disarmed there; replacing the sink keeps the latch honest on
// every construction path. The counter (not a bool) is what lets the screens
// tell "the same corruption, observed again" from "a new corrupt event":
// each load snapshots the counter before and after, so per-frame reloads of
// one still-corrupt file never masquerade as a new episode.
type corruptPresetSink struct{ count *int }

func (w corruptPresetSink) Write(p []byte) (int, error) {
	*w.count++
	return len(p), nil
}

// corruptLatch is the corrupt-file episode state both preset screens carry.
// The store sink counts corrupt-load events into events; every store load
// reconciles the latch against the counter (corruptObserved = the counter
// moved during this load). One acknowledgment covers exactly one episode:
// repeated observations of the SAME corruption keep it, while a corrupt
// observation with no episode open starts a new one, re-arming the gate
// after an earlier acknowledged save, and a healthy observation closes the
// open episode and clears the notice, so the warning never outlives the
// corruption it names.
type corruptLatch struct {
	corruptEvents  int  // corrupt-load events counted by the sink
	corruptSeen    bool // an observed corrupt episode is open
	corruptWarn    bool // the notice renders and store writes gate
	corruptAcked   bool // the user acknowledged overwriting the corrupt file for the open episode
	corruptMsgSent bool // the one-time message-line warning was shown for the open episode
}

// reconcile folds one store load into the latch. corruptObserved reports
// whether the load fired the sink; loadErr is the load error ("" on
// success).
func (l *corruptLatch) reconcile(corruptObserved bool, loadErr string) {
	switch {
	case corruptObserved:
		if !l.corruptSeen {
			l.corruptSeen = true
			l.corruptAcked = false
			l.corruptMsgSent = false
		}
		l.corruptWarn = true
	case loadErr == "" && l.corruptSeen:
		l.corruptSeen = false
		l.corruptWarn = false
		l.corruptAcked = false
	}
}

// presetFilePath resolves the display path of the preset file (AGY_PRO_HOME
// aware), matching the store's own path resolution. fs may be nil (a non-file
// store), in which case the default path is used.
func presetFilePath(fs *fanout.PresetFileStore) string {
	if fs != nil && fs.Path != "" {
		return fs.Path
	}
	if p, err := fanout.DefaultPresetsPath(); err == nil && p != "" {
		return p
	}
	return "fanout-presets.json"
}

// corruptPresetWarning renders the persistent corrupt-file notice for the
// message line and the frame body. It names the on-disk path so the user can
// back the file up before any save; the path is display data and passes
// through MaskOperand like every other echoed operand.
func corruptPresetWarning(b *i18n.Bundle, path string) string {
	return fillHubSlots(missingOr(b, "presets.corrupt.warning", presetsCorruptWarning),
		map[string]string{
			"file": filepath.Base(path),
			"path": providers.MaskOperand(path),
		})
}

// presetsScreen is S7: the fanout preset list (architect-report section 3,
// screen 7). One read-only built-in row (the engine defaults), one row per
// saved preset, a status line naming the default preset, and the fixed
// action rows (New / Edit / Duplicate / Delete / Set default). Every write
// goes exclusively through the preset store (fanout-presets.json, atomic
// 0600); providers.json is never touched here. Navigation is the shared
// stepList, so digits and arrows+Enter are both always live (requirement 2).
type presetsScreen struct {
	store      fanout.PresetStore
	openEditor func(name string, a *App)
	b          *i18n.Bundle
	lang       string
	list       *stepList
	rows       []Row
	pf         *fanout.PresetsFile
	loadErr    string
	sel        int // sticky preset selection (index into pf.Presets)
	app        *App
	sig        string // rendered-state signature; reloadIfChanged compares against it
	corruptLatch
}

// NewPresetsScreen builds the preset list over store. openEditor is the
// shell callback that pushes the preset editor; it receives the preset name
// ("" for New, builtinPresetSentinel for the read-only built-in row). The
// UI language resolves from the persisted state (AGY_PRO_HOME state.json),
// defaulting to English.
func NewPresetsScreen(store fanout.PresetStore, openEditor func(name string, a *App)) Screen {
	s := &presetsScreen{store: store, openEditor: openEditor, sel: -1}
	// Surface the store's corrupt-file warning instead of letting the
	// WarnOut sink discard it: the empty fallback store renders honestly,
	// but the first write must not silently overwrite the user's file. The
	// wiring replaces any pre-set sink (the app shell sets one before
	// handing the store over), so the screen's corrupt gate arms on the
	// production path too; the shell already consumed its own warning into
	// the app message line at that point.
	if fs, ok := store.(*fanout.PresetFileStore); ok {
		fs.WarnOut = corruptPresetSink{count: &s.corruptLatch.corruptEvents}
	}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	s.setLang(i18n.Normalize(lang))
	s.list = newStepList(s.activate)
	s.reload()
	return s
}

// setLang loads the bundle for lang once per language.
func (s *presetsScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// Mode is ModeNav: digits and arrows both navigate.
func (s *presetsScreen) Mode() Mode { return ModeNav }

// Title returns the screen title.
func (s *presetsScreen) Title() string { return s.b.T("presets.screen.title") }

// reload re-reads the preset file and rebuilds the rows. Called on
// construction and after every store write; the cursor and the sticky
// selection are kept and clamped into the new row range. The rendered-state
// signature is recorded so reloadIfChanged can skip the rebuild while the
// store is unchanged.
func (s *presetsScreen) reload() {
	pf, loadErr := s.loadPresets()
	s.adopt(pf, loadErr)
}

// loadPresets reads the store once through the corrupt-episode latch: the
// sink counter is snapshotted around the load so the latch can tell a new
// corrupt event from a repeated observation of the same one.
func (s *presetsScreen) loadPresets() (*fanout.PresetsFile, string) {
	pf := &fanout.PresetsFile{}
	loadErr := ""
	before := s.corruptLatch.corruptEvents
	if s.store != nil {
		if p, err := s.store.LoadPresets(); err != nil {
			loadErr = providers.MaskOperand(err.Error())
		} else if p != nil {
			pf = p
		}
	}
	s.corruptLatch.reconcile(s.corruptLatch.corruptEvents > before, loadErr)
	return pf, loadErr
}

// presetsSig builds a signature over every field the rows and the status
// line render: each preset's full record (name plus every flag knob and the
// allow_hosts list) and the default key, plus the load error when one is
// showing. A change in any of them moves the signature.
func presetsSig(pf *fanout.PresetsFile, loadErr string) string {
	if loadErr != "" {
		return "err\x00" + loadErr
	}
	parts := make([]string, len(pf.Presets))
	for i := range pf.Presets {
		p := &pf.Presets[i]
		parts[i] = strings.Join([]string{
			p.Name,
			p.Task,
			strconv.Itoa(p.N),
			strconv.Itoa(p.TimeoutSeconds),
			p.Tools,
			strconv.Itoa(p.MaxTurns),
			strconv.Itoa(p.MaxNative),
			p.SearchEndpoint,
			p.SearchKeyEnv,
			p.OutDir,
			p.Root,
			p.Models,
			p.Seats,
			strconv.FormatBool(p.JSON),
			strings.Join(p.AllowHosts, ","),
		}, "\x1f")
	}
	return strconv.Itoa(len(parts)) + "\x1e" + strings.Join(parts, "\x1e") + "\x1e" + pf.DefaultPreset
}

// reloadIfChanged re-reads the store and rebuilds the rows only when the
// rendered-state signature moved (the models/prompts screen pattern). An
// editor save between this screen's own writes is picked up before the next
// list action, so delete/duplicate/set-default write the current file state
// instead of reverting it from a stale in-memory snapshot. A corrupt file
// re-fires the store warning into the sink on every load; loadPresets
// reconciles the latch each time, and repeated observations of one
// corruption never reset its acknowledgment.
func (s *presetsScreen) reloadIfChanged() {
	if s.store == nil {
		return
	}
	pf, loadErr := s.loadPresets()
	if presetsSig(pf, loadErr) == s.sig {
		return
	}
	s.adopt(pf, loadErr)
}

// adopt swaps in a freshly loaded store state and rebuilds the rows. The
// cursor is restored by ROW ID, so a mutation that shifts the row layout
// cannot leave the visible cursor on a different verb than the one the user
// activated (preset rows and action rows keep their IDs across a rebuild); a
// row that vanished (a deleted preset) falls back to the old index, which
// SetCursor clamps into the new range. The sticky selection is kept and
// clamped into the new preset range; the signature is recorded from the
// adopted state.
func (s *presetsScreen) adopt(pf *fanout.PresetsFile, loadErr string) {
	s.pf = pf
	s.loadErr = loadErr
	s.sig = presetsSig(pf, loadErr)
	if s.sel > len(s.pf.Presets)-1 {
		s.sel = len(s.pf.Presets) - 1
	}
	cursor := s.list.Cursor()
	oldID := ""
	if cursor >= 0 && cursor < len(s.rows) {
		oldID = s.rows[cursor].ID
	}
	s.rows = s.buildRows()
	s.list.SetRows(s.rows)
	target := cursor
	for i, r := range s.rows {
		if r.ID == oldID {
			target = i
			break
		}
	}
	s.list.SetCursor(target)
}

// buildRows composes the row inventory: the fixed built-in row, one row per
// preset, then the five action rows. The preset note is the one-line flag
// summary (task, n, timeout, tools) or, when only hidden fields are pinned,
// the pinned-count digest (survey defect D10); the default preset carries the
// badge.
func (s *presetsScreen) buildRows() []Row {
	rows := make([]Row, 0, len(s.pf.Presets)+6)
	rows = append(rows, Row{ID: presetsRowIDBuiltin, Label: builtinPresetRowLabel, Note: builtinDefaultsSummary(s.b)})
	for i := range s.pf.Presets {
		p := &s.pf.Presets[i]
		note := presetFlagSummary(s.b, p)
		if p.Name == s.pf.DefaultPreset {
			note = noteWithDefaultBadge(note, s.b.T("presets.default.badge"))
		}
		rows = append(rows, Row{ID: presetRowPrefix + p.Name, Label: providers.MaskOperand(p.Name), Note: note})
	}
	b := s.b
	rows = append(rows,
		Row{ID: presetsRowIDNew, Label: b.T("presets.action.new")},
		Row{ID: presetsRowIDEdit, Label: b.T("presets.action.edit")},
		Row{ID: presetsRowIDDuplicate, Label: b.T("presets.action.duplicate")},
		Row{ID: presetsRowIDDelete, Label: b.T("presets.action.delete")},
		Row{ID: presetsRowIDSetDefault, Label: b.T("presets.action.set_default")},
	)
	return rows
}

// presetFlagSummary renders the one-line flag summary of a preset: the
// configured knobs among task, n, timeout, tools. The free-text operands
// (task, tools) render through MaskOperand like every other echoed operand:
// a credential-shaped value pasted into the preset paints masked in the row
// note; benign values (review, read,search) stay readable. The note shows only
// those four knobs, so a preset pinning only hidden fields (seats, models,
// allow_hosts, max_native, search_*, out_dir, root, json) used to render the
// same "(empty)" as a truly empty preset (survey defect D10): when every
// visible knob is unset the summary falls back to the pinned-count digest
// ("N of 14 fields pinned: see Edit"), and only a preset with nothing pinned
// at all renders the honest "(empty)".
func presetFlagSummary(b *i18n.Bundle, p *fanout.Preset) string {
	if p == nil {
		return "(empty)"
	}
	parts := []string{}
	if p.Task != "" {
		parts = append(parts, "task="+providers.MaskOperand(p.Task))
	}
	if p.N > 0 {
		parts = append(parts, "n="+strconv.Itoa(p.N))
	}
	if p.TimeoutSeconds > 0 {
		parts = append(parts, "timeout="+strconv.Itoa(p.TimeoutSeconds)+"s")
	}
	if p.Tools != "" {
		parts = append(parts, "tools="+providers.MaskOperand(p.Tools))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if pinned := countPresetPinnedFields(p); pinned > 0 {
		return fillHubSlots(missingOr(b, "presets.summary.pinned", presetsSummaryPinned),
			map[string]string{"count": strconv.Itoa(pinned)})
	}
	return "(empty)"
}

// noteWithDefaultBadge appends the default badge to a non-empty note.
func noteWithDefaultBadge(note, badge string) string {
	if note == "" {
		return badge
	}
	return note + " · " + badge
}

// countPresetPinnedFields counts the preset's 14 optional run knobs that carry
// a value. Name is the identity, not a knob; json pins only when true (a false
// does not survive the omitempty save). The count feeds the D10 digest, so it
// must walk exactly the fields the editor can set.
func countPresetPinnedFields(p *fanout.Preset) int {
	n := 0
	if p.Task != "" {
		n++
	}
	if p.N > 0 {
		n++
	}
	if p.TimeoutSeconds > 0 {
		n++
	}
	if p.Tools != "" {
		n++
	}
	if p.MaxTurns > 0 {
		n++
	}
	if p.MaxNative > 0 {
		n++
	}
	if len(p.AllowHosts) > 0 {
		n++
	}
	if p.SearchEndpoint != "" {
		n++
	}
	if p.SearchKeyEnv != "" {
		n++
	}
	if p.OutDir != "" {
		n++
	}
	if p.Root != "" {
		n++
	}
	if p.Models != "" {
		n++
	}
	if p.Seats != "" {
		n++
	}
	if p.JSON {
		n++
	}
	return n
}

// builtinDefaultsSummary renders the engine-default note for the fixed
// built-in row.
func builtinDefaultsSummary(b *i18n.Bundle) string {
	return presetFlagSummary(b, &fanout.Preset{
		Task:           presetEngineDefaultTask,
		N:              presetEngineDefaultN,
		TimeoutSeconds: presetEngineDefaultTimeout,
		Tools:          presetEngineDefaultTools,
	})
}

// statusLine renders which preset is the store default.
func (s *presetsScreen) statusLine() string {
	name := strings.TrimSpace(s.pf.DefaultPreset)
	if name == "" {
		return missingOr(s.b, "presets.status.none", presetsStatusNone)
	}
	return fillHubSlots(missingOr(s.b, "presets.status.default", presetsStatusDefault),
		map[string]string{"name": providers.MaskOperand(name)})
}

// activate is the stepList onPick callback, shared by digit selection and
// arrows+Enter. Preset rows open the editor; action rows dispatch.
func (s *presetsScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.rows) {
		return
	}
	id := s.rows[idx].ID
	switch {
	case id == presetsRowIDBuiltin:
		s.open(builtinPresetSentinel)
	case strings.HasPrefix(id, presetRowPrefix):
		s.open(id[len(presetRowPrefix):])
	case id == presetsRowIDNew:
		s.open("")
	case id == presetsRowIDEdit:
		s.withSelected(func(p fanout.Preset) { s.open(p.Name) })
	case id == presetsRowIDDuplicate:
		s.withSelected(s.duplicate)
	case id == presetsRowIDDelete:
		s.withSelected(s.askDelete)
	case id == presetsRowIDSetDefault:
		s.withSelected(s.setDefault)
	}
}

// open forwards to the shell-supplied editor factory.
func (s *presetsScreen) open(name string) {
	if s.openEditor != nil {
		s.openEditor(name, s.app)
	}
}

// withSelected runs fn over the sticky-selected preset. With no preset
// selected the user gets a message instead of a silent no-op.
func (s *presetsScreen) withSelected(fn func(fanout.Preset)) {
	if s.sel >= 0 && s.sel < len(s.pf.Presets) {
		fn(s.pf.Presets[s.sel])
		return
	}
	if s.app != nil {
		s.app.SetMessage(missingOr(s.b, "presets.msg.select_first", presetsMsgSelectFirst))
	}
}

// selectedName returns the sticky-selected preset name, or "".
func (s *presetsScreen) selectedName() string {
	if s.sel >= 0 && s.sel < len(s.pf.Presets) {
		return s.pf.Presets[s.sel].Name
	}
	return ""
}

// gateCorruptSave refuses the first store write after a corrupt load until
// the user acknowledges it through an explicit confirm naming the on-disk
// path (back it up before saving). Afterwards the write proceeds directly,
// but the acknowledgment covers exactly one corrupt episode: a later healthy
// load closes it, and a new corrupt event re-arms the gate (the latch is
// episode-based, never a construction-time one-way switch).
func (s *presetsScreen) gateCorruptSave(proceed func()) {
	a := s.app
	if a == nil || !s.corruptWarn || s.corruptAcked {
		proceed()
		return
	}
	path := providers.MaskOperand(presetFilePath(storeFile(s.store)))
	a.ShowModal(NewConfirm(missingOr(s.b, "presets.corrupt.title", presetsCorruptTitle),
		[]string{fillHubSlots(missingOr(s.b, "presets.corrupt.body", presetsCorruptBody),
			map[string]string{"path": path})},
		func(approved bool) {
			if !approved {
				a.SetMessage(missingOr(s.b, "presets.msg.cancelled", presetsMsgCancelled))
				return
			}
			s.corruptAcked = true
			proceed()
		}))
}

// storeFile returns the store as a *fanout.PresetFileStore, or nil.
func storeFile(st fanout.PresetStore) *fanout.PresetFileStore {
	fs, _ := st.(*fanout.PresetFileStore)
	return fs
}

// duplicate saves a copy of the selected preset under a fresh name
// ("<name> copy", "<name> copy 2", ...). The copy lands at the end of the
// list; the selection moves to it. The in-memory list is only mutated inside
// the corrupt gate callback, after the gate answered, so a declined gate
// leaves memory untouched.
//
// A copy is a NEW record, and the only write path here is SavePresets, which
// runs just the store-boundary rules (see fanout.ValidatePresetForSave): the
// D9/D13/D14 save rules live in the validator alone. Without the gate below a
// copy of a row that violates them (a hand-edited models+seats row, an
// unknown task, a bad allow_hosts entry) would persist a preset the editor
// would refuse to save, which is exactly the bypass s5 deviation 3 named.
// The gate runs the same authority over the copy BEFORE anything is written
// and renders the editor's own wording for the refusal (presetSaveRuleMessage),
// so both writers name a rule identically.
func (s *presetsScreen) duplicate(p fanout.Preset) {
	a := s.app
	name := uniquePresetCopyName(s.pf, p.Name)
	cp := p
	cp.Name = name
	cp.AllowHosts = append([]string(nil), p.AllowHosts...)
	if err := fanout.ValidatePresetForSave(cp); err != nil {
		a.SetMessage(s.b.T("common.error") + ": " + presetSaveRuleMessage(s.b, err))
		return
	}
	s.gateCorruptSave(func() {
		s.pf.Presets = append(s.pf.Presets, cp)
		if err := s.store.SavePresets(s.pf); err != nil {
			s.pf.Presets = s.pf.Presets[:len(s.pf.Presets)-1]
			a.SetMessage(s.b.T("common.error") + ": " + s.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
			return
		}
		s.reload()
		s.sel = len(s.pf.Presets) - 1
		a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "presets.msg.duplicated", presetsMsgDuplicated),
			map[string]string{"name": providers.MaskOperand(name)}))
	})
}

// presetSaveRuleMessages is the preset list's copy of the editor's refusal
// table (presetEditorScreen.saveRuleMessage): the rules and their sentinels
// are fanout.ValidatePresetForSave's, and the wording reuses the editor's
// presets.editor.validation.* keys and consts, so a rule is named the same
// way whichever writer refused the save. The table is PRESENTATION only; a
// refusal it does not know (a rule added to the validator later) falls back to
// the validator's own masked text instead of vanishing.
var presetSaveRuleMessages = []struct {
	err      error
	key      string
	fallback string
}{
	{fanout.ErrPresetNameRequired, "presets.editor.validation.name", presetsEditorValidationName},
	{fanout.ErrPresetSearchKeyEnv, "presets.editor.validation.key_env", presetsEditorValidationKeyEnv},
	{fanout.ErrPresetSeatConflict, "presets.editor.validation.seat_conflict", presetsEditorValidationSeatConflict},
	{fanout.ErrPresetUnknownTask, "presets.editor.validation.task", presetsEditorValidationTask},
	{fanout.ErrPresetAllowHosts, "presets.editor.validation.host", presetsEditorValidationHost},
}

// presetSaveRuleMessage renders a fanout.ValidatePresetForSave refusal in the
// editor's wording.
func presetSaveRuleMessage(b *i18n.Bundle, err error) string {
	for _, m := range presetSaveRuleMessages {
		if errors.Is(err, m.err) {
			return missingOr(b, m.key, m.fallback)
		}
	}
	return providers.MaskOperand(err.Error())
}

// uniquePresetCopyName finds the first free "<base> copy" variant.
func uniquePresetCopyName(pf *fanout.PresetsFile, base string) string {
	candidate := base + presetCopySuffix
	for n := 2; ; n++ {
		if _, ok := pf.FindPreset(candidate); !ok {
			return candidate
		}
		candidate = base + presetCopySuffix + " " + strconv.Itoa(n)
	}
}

// askDelete opens the M3 y/n gate (plain Confirm: the preset file is not
// providers.json, so no M4 approval is needed). y removes the preset and
// clears the default key when it pointed there; n/Esc cancels with an
// explicit nothing-written message. The in-memory list is only mutated
// inside the corrupt gate callback, after both gates answered, so a declined
// gate leaves memory untouched and the next acknowledged save cannot pick up
// a half-applied deletion.
func (s *presetsScreen) askDelete(p fanout.Preset) {
	a := s.app
	masked := providers.MaskOperand(p.Name)
	a.ShowModal(NewConfirm(s.b.T("presets.delete.title"),
		[]string{s.b.Tf("presets.delete.body", map[string]string{"name": masked})},
		func(approved bool) {
			if !approved {
				a.SetMessage(missingOr(s.b, "presets.msg.cancelled", presetsMsgCancelled))
				return
			}
			s.gateCorruptSave(func() {
				keep := make([]fanout.Preset, 0, len(s.pf.Presets))
				for i := range s.pf.Presets {
					if s.pf.Presets[i].Name != p.Name {
						keep = append(keep, s.pf.Presets[i])
					}
				}
				s.pf.Presets = keep
				if s.pf.DefaultPreset == p.Name {
					s.pf.DefaultPreset = ""
				}
				if err := s.store.SavePresets(s.pf); err != nil {
					a.SetMessage(s.b.T("common.error") + ": " + s.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
					s.reload()
					return
				}
				s.reload()
				a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "presets.msg.deleted", presetsMsgDeleted),
					map[string]string{"name": masked}))
			})
		}))
}

// setDefault writes the store's default_preset key to the selected preset.
func (s *presetsScreen) setDefault(p fanout.Preset) {
	a := s.app
	s.gateCorruptSave(func() {
		s.pf.DefaultPreset = p.Name
		if err := s.store.SavePresets(s.pf); err != nil {
			a.SetMessage(s.b.T("common.error") + ": " + s.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
			s.reload()
			return
		}
		s.reload()
		a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "presets.msg.default_set", presetsMsgDefaultSet),
			map[string]string{"name": providers.MaskOperand(p.Name)}))
	})
}

// Draw renders the default-status line, the four explanation lines naming
// what a preset is and how it is consumed (the U4 copy: run config, not a
// prompt; the consumer command; the env var; the on-disk path; the
// default_preset rule), the numbered rows, and the two-handled footer hint.
// All cell math goes through cellwidth. The window is sized against the real
// chrome of this frame: the fixed chrome rows plus every one-off line the
// body renders above the list, so the body always fits the frame and the
// overflow path (which shrinks the body from its tail) never runs on a
// full-page list. The explanation lines are part of presetsListChrome, so a
// full page still ends on the list hint and the base footer, the only line
// naming Esc, Ctrl+L and Ctrl+C, stays on the frame.
func (s *presetsScreen) Draw(a *App) []string {
	s.app = a
	s.reloadIfChanged()
	warn := ""
	if s.corruptWarn {
		warn = corruptPresetWarning(s.b, presetFilePath(storeFile(s.store)))
		if !s.corruptMsgSent && a.Message() == "" {
			s.corruptMsgSent = true
			a.SetMessage(warn)
		}
	}
	// notice is the one-off block above the list: the load error, the
	// corrupt-file warning, and the empty state (a line plus the blank that
	// keeps the list off it). Its length is what the window has to leave room
	// for; the explanation lines and the list hint with its blank are already
	// part of presetsListChrome.
	var notice []string
	if s.loadErr != "" {
		notice = append(notice, cellwidth.Truncate(s.b.T("common.error")+": "+s.loadErr, a.Width()))
	}
	if warn != "" {
		notice = append(notice, cellwidth.Truncate(warn, a.Width()))
	}
	if len(s.pf.Presets) == 0 {
		notice = append(notice, cellwidth.Truncate(s.b.T("presets.empty"), a.Width()), "")
	}
	// The app message line is its own block under the body (a blank separator
	// plus the line), and the composer appends it after the body. A full-page
	// list leaves those two rows out of the window as well, so the message
	// frame neither cuts the list tail nor drops the base footer.
	chrome := presetsListChrome + len(notice)
	if a.Message() != "" {
		chrome += 2
	}
	explain := s.explainLines(a.Width())
	// The explanation block is the first body content a short console loses:
	// when the frame cannot hold the full chrome plus at least one list row,
	// the prose gives way so the status line, the notices, the list, the
	// hint and the base footer stay. At the 80x24 budget the block always
	// renders and the page size stays 24-presetsListChrome.
	if a.Height()-chrome < 1 {
		explain = explain[:0]
		chrome -= presetsExplainLines
	}
	s.list.SetPageSize(pageSizeFor(a.Height(), chrome))
	lines := make([]string, 0, len(notice)+len(explain)+s.list.PageSize()+4)
	lines = append(lines, cellwidth.Truncate(s.statusLine(), a.Width()), "")
	lines = append(lines, explain...)
	lines = append(lines, notice...)
	lines = append(lines, s.list.Render(a.Width())...)
	lines = append(lines, "", s.list.FooterHint())
	return lines
}

// explainLines renders the four permanent lines that name the object on this
// screen (survey 2.7): what a fanout preset is (a named run config of
// seat/tool/time budgets, not a prompt, not a seat selection), how a run
// consumes it (agy pro fanout --preset <name>, or the AGY_PRO_FANOUT_PRESET
// env var), where it lives (the store's on-disk path, through MaskOperand
// like every other echoed path), and the default_preset rule (the key
// auto-applies to runs that name no preset; since P3-1 a conflicting knob is
// dropped with a run-time warning instead of killing the run). Each line is
// truncated to the frame width, so an AGY_PRO_HOME-long path can only ever
// cut its own line, never the rule lines below it.
func (s *presetsScreen) explainLines(w int) []string {
	b := s.b
	path := providers.MaskOperand(presetFilePath(storeFile(s.store)))
	return []string{
		cellwidth.Truncate(missingOr(b, "presets.explain.what", presetsExplainWhat), w),
		cellwidth.Truncate(missingOr(b, "presets.explain.run", presetsExplainRun), w),
		cellwidth.Truncate(fillHubSlots(missingOr(b, "presets.explain.file", presetsExplainFile),
			map[string]string{"path": path}), w),
		cellwidth.Truncate(missingOr(b, "presets.explain.default", presetsExplainDefault), w),
	}
}

// HandleKey re-reads the store behind the signature check first, then
// delegates to the stepList and keeps the sticky preset selection in sync
// while the cursor sits on a preset row. Esc never arrives here: the App
// precedence handles it first.
func (s *presetsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.reloadIfChanged()
	s.list.HandleKey(k)
	if c := s.list.Cursor(); c >= 1 && c <= len(s.pf.Presets) {
		s.sel = c - 1
	}
}

// pageSizeFor derives a list window from the frame height, reserving the
// given number of chrome lines.
func pageSizeFor(height, chrome int) int {
	page := height - chrome
	if page < 1 {
		page = 1
	}
	return page
}
