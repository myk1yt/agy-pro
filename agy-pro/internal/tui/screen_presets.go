package tui

import (
	"errors"
	"fmt"
	"io"
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
// built-in row and the action rows. Row [1] is the built-in defaults row:
// its label is fanout.BuiltinPresetName (the reserved seed identity), and
// the preset-row loop skips any record carrying that name so the seed never
// renders twice (row [1] and an ordinary row).
const (
	presetsRowIDBuiltin    = "builtin"
	presetsRowIDNew        = "new"
	presetsRowIDEdit       = "edit"
	presetsRowIDDuplicate  = "duplicate"
	presetsRowIDDelete     = "delete"
	presetsRowIDSetDefault = "set-default"
	presetRowPrefix        = "preset\x00"
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
	presetsExplainRun = "A preset may pin the seat lineup. Run: agy pro fanout --preset <name> · env AGY_PRO_FANOUT_PRESET"
	// catalog-resolved, fallback kept: presets.explain.file
	presetsExplainFile = "File: {{path}}"
	// catalog-resolved, fallback kept: presets.explain.default
	presetsExplainDefault = "default_preset applies when no preset is named; conflicts drop with a warning."
	// catalog-resolved, fallback kept: presets.summary.pinned
	presetsSummaryPinned = "{{count}} of 13 fields pinned: see Edit"
	// catalog-resolved, fallback kept: presets.summary.seats
	presetsSummarySeats = "seats pinned: {{count}}"
	// catalog-resolved, fallback kept: presets.notice.models_removed
	// (The store's load door discloses every record whose JSON still carries the
	// RETIRED "models" key; a localized surface renders this sentence instead of
	// the store's English line, which it routes to a discard sink.)
	presetsNoticeModelsRemoved = "warning: preset {{name}}: the legacy models field is removed; use seats."
	// catalog-resolved, fallback kept: presets.builtin.copy_note
	// (The built-in row is directly editable; Ctrl+A remains the optional
	// gesture for copying its visible defaults into a new preset.)
	presetsBuiltinCopyNote = "Enter edits directly · Ctrl+A copies into a new preset"
	// catalog-resolved, fallback kept: presets.builtin.default_equiv
	// Shown only when default_preset points at an existing seed record.
	presetsBuiltinDefaultEquiv = "same as no key"
	// catalog-resolved, fallback kept: presets.reset.title
	presetsResetTitle = "Reset Built-in Defaults?"
	// catalog-resolved, fallback kept: presets.reset.body
	presetsResetBody = "Reset {{name}} to engine defaults? All saved settings are removed."
	// catalog-resolved, fallback kept: presets.msg.builtin_reset — the
	// reset-after message for the Delete verb on row [1]: the seed record
	// (every field, seats included) is gone, so runs fall back to pure
	// engine defaults. All 12 catalogs carry the key (R3-F-07b).
	presetsMsgBuiltinReset = "Built-in defaults reset. Row [1] returns to the engine defaults."
)

// P5-1 U6: the default_preset gate (W28). The healthy path used to rewrite
// fanout-presets.json with no consent at all; it now opens the M4 approval modal
// over the exact key change. The store is not providers.json, so the catalog's
// modal.approval.body sentence does not apply: the store sentences below are
// documented English chrome (decision 6B, the approvalEntriesHidden precedent)
// because they are new keys and a new key costs twelve catalog edits, while the
// +/- markers reuse their catalog spellings and the preset names and the file
// path render through the display authorities.
const (
	// presetsApprovalDefault names the key change and states the no-collateral
	// promise. The store is named on its own "File:" line and in the atomic
	// sentence below, so a deep AGY_PRO_HOME cannot clamp the change away.
	presetsApprovalDefault = "Sets default_preset: %s -> %s. No preset record changes; the key applies to every run that names no preset."
	// presetsApprovalAtomic names the file (base name) and the write mode the
	// diff applies to.
	presetsApprovalAtomic = "%s will be written (0600, atomic) with this exact diff:"
	// presetsApprovalKeyFmt is the inner text of one +/- diff entry.
	presetsApprovalKeyFmt = "default_preset = %s"
	// presetsApprovalUnset renders the empty default key honestly instead of as
	// an empty string, so the diff line cannot read as a deletion of nothing.
	presetsApprovalUnset = "(unset)"
	// presetsApprovalDrift is the write-time stale-base refusal: it names the
	// store (base name) and the first difference, so a refusal says WHAT moved.
	presetsApprovalDrift = "refused: %s changed on disk between the diff and the write (%s), so the presented diff is stale; nothing was written."
)

// presetDefaultDisplay renders one default_preset value for the gate body.
func presetDefaultDisplay(name string) string {
	if strings.TrimSpace(name) == "" {
		return presetsApprovalUnset
	}
	return presetNameOperand(name)
}

// presetsWriteBase is the signed state of one fanout preset write: the ordered
// preset names and the default_preset value the diff described. The preset
// RECORDS are signed by name only because the write rebuilds the file from a
// fresh read, so an external edit to a record (another run's field change) is
// carried over instead of reverted; the default key is signed by value because
// that is the field this write changes.
type presetsWriteBase struct {
	names []string
	def   string
}

// presetsWriteBaseFrom signs the store state a gate presents.
func presetsWriteBaseFrom(pf *fanout.PresetsFile) presetsWriteBase {
	b := presetsWriteBase{}
	if pf == nil {
		return b
	}
	b.names = make([]string, 0, len(pf.Presets))
	for i := range pf.Presets {
		b.names = append(b.names, pf.Presets[i].Name)
	}
	b.def = pf.DefaultPreset
	return b
}

// drift names the first difference between the presented base and a fresh read,
// or "" when they agree. Operands render masked.
func (b presetsWriteBase) drift(live *fanout.PresetsFile) string {
	if live == nil {
		live = &fanout.PresetsFile{}
	}
	if len(b.names) != len(live.Presets) {
		return fmt.Sprintf("the preset count moved from %d to %d", len(b.names), len(live.Presets))
	}
	for i := range b.names {
		if b.names[i] != live.Presets[i].Name {
			return fmt.Sprintf("preset %d moved from %s to %s", i+1,
				presetNameOperand(b.names[i]), presetNameOperand(live.Presets[i].Name))
		}
	}
	if b.def != live.DefaultPreset {
		return fmt.Sprintf("default_preset moved from %s to %s",
			presetDefaultDisplay(b.def), presetDefaultDisplay(live.DefaultPreset))
	}
	return ""
}

// corruptPresetSink captures the store's one-line corrupt-file fallback
// warning. The screens wire it as PresetFileStore.WarnOut at load time: a
// nil WarnOut silently discards the warning and the next save overwrites the
// user's presets with no visible notice (security review F5). The sink only
// counts the warnings; the rendered text is composed by
// corruptPresetWarning so it can carry the full on-disk path.
//
// R3-F-05 class split: WarnOut now carries ONLY the corrupt-file fallback
// (the JSON parse failure at preset_store.go LoadPresets); the per-record
// notices (the FL-4 control drop, the F-12 duplicate-seed collapse) are
// routed to the store's NoticeOut and presetDropSink below. A dropped record
// is NOT a corrupt file: the records that parsed stay loaded and usable, so
// counting a drop notice here would tell the operator their file is corrupt
// and arm the overwrite gate against data that is healthy (the round-3
// review's quoted frame showed the corruption line beside a healthy row).
// The wiring is unconditional: the app shell pre-sets WarnOut on the
// production path (presetStore in app.go), and a conditional "only when nil"
// skip left the corrupt gate disarmed there; replacing the sink keeps the
// latch honest on every construction path. The counter (not a bool) is what
// lets the screens tell "the same corruption, observed again" from "a new
// corrupt event": each load snapshots the counter before and after, so
// per-frame reloads of one still-corrupt file never masquerade as a new
// episode.
type corruptPresetSink struct{ count *int }

func (w corruptPresetSink) Write(p []byte) (int, error) {
	*w.count++
	return len(p), nil
}

// presetDropSink captures the store's per-record load notices (the FL-4
// control drop and the F-12 duplicate-seed collapse, one line per ignored
// record). These are shown as what they are, a specific record was ignored,
// and never arm the corrupt latch or its overwrite gate. The store masks and
// escapes the record name and names only the field; each renderer owns width
// truncation.
type presetDropSink struct{ lines *[]string }

func (w presetDropSink) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		*w.lines = append(*w.lines, line)
	}
	return len(p), nil
}

// presetLegacyModelsNotice renders the localized advisory for ONE record whose
// on-disk JSON still carries the RETIRED "models" key. The store detects it
// (PresetsFile.LegacyModels) and prints its English line for the CLI; a TUI
// surface renders this sentence instead, which is why both preset screens route
// the store's line to the discard sink (PresetFileStore.ModelsDropOut) and
// append this one. The name is already display-masked by the store.
func presetLegacyModelsNotice(b *i18n.Bundle, name string) string {
	return fillHubSlots(
		missingOr(b, "presets.notice.models_removed", presetsNoticeModelsRemoved),
		map[string]string{"name": name})
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
// screen 7). One built-in defaults row (row [1]: the engine defaults preview
// until the seed record exists on disk, then the seed record itself), one row
// per saved preset, a status line naming the default preset, and the fixed
// action rows (New / Edit / Duplicate / Delete / Set default). Every write
// goes exclusively through the preset store (fanout-presets.json, atomic
// 0600); providers.json is never touched here. Navigation is the shared
// stepList, so digits and arrows+Enter are both always live (requirement 2).
//
// The sticky selection follows the cursor while the cursor rests on a preset
// row, and that rule alone made the action rows unreachable for arrow-only
// users: the action block sits BELOW the preset block, so travelling down to
// Edit/Delete passes every remaining preset row and each rest re-stole the
// target, leaving the verb on the LAST preset. Space pins a row explicitly
// (locked, selectAt) and the cursor-reassert is suspended until the pin is
// consumed.
//
// The pin is held by PRESET NAME, not by index (fix round 1, F-01; the providers
// screen's by-name and the models screen's by-ID rule). The store is rewritten
// whole by other surfaces (the editor above this screen, another session), so an
// index re-clamped after a reload can name a DIFFERENT preset than the one the
// user pinned, and a locked selection made that silent: the pin survived while
// Edit/Duplicate/Delete/Set default pointed at a neighbour. A pin therefore
// re-anchors by name on reload and drops (with its lock) when the name no longer
// resolves, which leaves nothing selected and the verbs refusing rather than
// guessing.
type presetsScreen struct {
	store      fanout.PresetStore
	openEditor func(name string, a *App)
	b          *i18n.Bundle
	lang       string
	list       *stepList
	rows       []Row
	pf         *fanout.PresetsFile
	loadErr    string
	sel        int    // sticky preset selection (index into visiblePresets; -1 = none or the seed pin)
	selName    string // preset NAME the selection holds (identity the Space pin re-anchors by)
	locked     bool   // Space pinned sel: the cursor reassert must not overwrite it
	app        *App
	sig        string // rendered-state signature; reloadIfChanged compares against it
	corruptLatch
	dropNotices []string // per-record load notices from the last load (R3-F-05); never arm the latch
}

// NewPresetsScreen builds the preset list over store. openEditor is the
// shell callback that pushes the preset editor; it receives the preset name
// ("" for New, fanout.BuiltinPresetName for the built-in defaults row [1],
// whose editor edits the seed record directly). The UI language resolves
// from the persisted state (AGY_PRO_HOME state.json), defaulting to English.
func NewPresetsScreen(store fanout.PresetStore, openEditor func(name string, a *App)) Screen {
	s := &presetsScreen{store: store, openEditor: openEditor, sel: -1}
	// Surface the store's corrupt-file warning instead of letting the
	// WarnOut sink discard it: the empty fallback store renders honestly,
	// but the first write must not silently overwrite the user's file. The
	// per-record drop/duplicate notices take their own channel (NoticeOut)
	// so they show as what they are and never arm the corrupt latch
	// (R3-F-05). The WarnOut wiring replaces any pre-set sink (the app
	// shell sets one before handing the store over), so the screen's
	// corrupt gate arms on the production path too; the shell already
	// consumed its own warning into the app message line at that point.
	if fs, ok := store.(*fanout.PresetFileStore); ok {
		fs.WarnOut = corruptPresetSink{count: &s.corruptLatch.corruptEvents}
		fs.NoticeOut = presetDropSink{lines: &s.dropNotices}
		// The retired-models advisory is rendered from the localized catalog
		// (loadPresets appends presetLegacyModelsNotice per record), so the
		// store's English twin is discarded here instead of printing twice.
		fs.ModelsDropOut = io.Discard
	}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	s.setLang(i18n.Normalize(lang))
	s.list = newStepList(s.activate)
	s.list.SetOnSelect(s.selectAt)
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
// corrupt event from a repeated observation of the same one. The per-record
// notices ride the separate NoticeOut channel, so they refill dropNotices
// (cleared before every load) without touching the corrupt counter. The
// retired-models advisories are appended here from the load's LegacyModels list:
// the store's English twin goes to the discard sink this screen wires, so the
// operator reads one localized line per record instead of two lines per fact.
func (s *presetsScreen) loadPresets() (*fanout.PresetsFile, string) {
	pf := &fanout.PresetsFile{}
	loadErr := ""
	s.dropNotices = s.dropNotices[:0]
	before := s.corruptLatch.corruptEvents
	if s.store != nil {
		if p, err := s.store.LoadPresets(); err != nil {
			loadErr = providers.MaskOperand(err.Error())
		} else if p != nil {
			pf = p
		}
	}
	for _, legacy := range pf.LegacyModels {
		s.dropNotices = append(s.dropNotices, presetLegacyModelsNotice(s.b, legacy))
	}
	s.corruptLatch.reconcile(s.corruptLatch.corruptEvents > before, loadErr)
	return pf, loadErr
}

// presetsSig builds a signature over every field the rows and the status
// line render: each preset's rendered fields (name plus every flag knob and
// the allow_hosts list, deliberately excluding SeatsSHA256) and the default
// key, plus the load error when one is showing. A change in any of them moves
// the signature.
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
// SetCursor clamps into the new range. The signature is recorded from the
// adopted state.
//
// The sticky selection has two re-anchor rules (fix round 1, F-01): a Space pin
// is resolved by preset NAME and drops with its lock when the name is gone, while
// an unpinned selection keeps the historical index clamp. The split is deliberate:
// the pin is the state that can silently re-target a neighbour the user never
// chose, whereas the unlocked clamp is itself a pinned contract
// (TestPresetsStickySelectionAfterMutation: after a Delete the selection clamps
// onto a preset that still exists) and is always visible through the marker.
func (s *presetsScreen) adopt(pf *fanout.PresetsFile, loadErr string) {
	s.pf = pf
	s.loadErr = loadErr
	s.sig = presetsSig(pf, loadErr)
	if s.locked {
		// Re-anchor the pin by identity: a pinned preset that reordered keeps its
		// target, a pinned preset that is gone leaves NOTHING selected, so the verbs
		// refuse (withSelected's select-first message) instead of acting on whatever
		// index the clamp landed on. A pin on the built-in defaults row re-anchors
		// onto the seed record the same way; a reset (the record is gone) drops the
		// pin with its lock.
		if fanout.IsBuiltinPresetName(s.selName) {
			if seedRecord(s.pf) != nil {
				s.selectSeed()
			} else {
				s.sel, s.selName, s.locked = -1, "", false
			}
		} else {
			s.selectPreset(presetsIndexByName(s.pf, s.selName))
			if s.sel < 0 {
				s.selName, s.locked = "", false
			}
		}
	} else {
		n := len(visiblePresets(s.pf))
		if s.sel > n-1 {
			s.sel = n - 1
		}
		if s.sel < 0 {
			s.selectPreset(-1)
		} else {
			s.selectPreset(s.sel)
		}
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

// presetsIndexByName resolves a preset NAME to its index among the VISIBLE
// presets (the seed record filtered out), or -1 when the store does not
// carry it. The name is the store's addressing key (the row IDs
// and every store verb address a name, never a position), so it is also the
// identity the Space pin is held by.
func presetsIndexByName(pf *fanout.PresetsFile, name string) int {
	if pf == nil || name == "" {
		return -1
	}
	vis := visiblePresets(pf)
	for i := range vis {
		if vis[i].Name == name {
			return i
		}
	}
	return -1
}

// visiblePresets returns the store's preset records with the built-in
// defaults seed record filtered out: row [1] renders that record, so it must
// never also appear as an ordinary preset row (it would double-count every
// index, digit, and frame-math site). Every site that counts or indexes
// presets routes through this helper; the raw pf.Presets stays the store's
// own listing (the write-signature and the rendered-state digest sign it
// whole, seed included, because a seed change must re-render and re-gate).
func visiblePresets(pf *fanout.PresetsFile) []fanout.Preset {
	if pf == nil {
		return nil
	}
	out := make([]fanout.Preset, 0, len(pf.Presets))
	for i := range pf.Presets {
		if fanout.IsBuiltinPresetName(pf.Presets[i].Name) {
			continue
		}
		out = append(out, pf.Presets[i])
	}
	return out
}

// seedRecord returns the built-in defaults seed record when the store
// carries it, or nil when row [1] is still the pure engine-defaults preview.
func seedRecord(pf *fanout.PresetsFile) *fanout.Preset {
	if pf == nil {
		return nil
	}
	if p, ok := pf.FindPreset(fanout.BuiltinPresetName); ok {
		return p
	}
	return nil
}

// selectPreset points the sticky selection at visible-index i and records
// the identity it holds. Every write site (the cursor reassert, the Space
// pin, the reload clamp, Duplicate's move onto the copy) goes through here,
// so sel and selName cannot disagree while a pin is live.
func (s *presetsScreen) selectPreset(i int) {
	s.sel, s.selName = i, ""
	vis := visiblePresets(s.pf)
	if i >= 0 && i < len(vis) {
		s.selName = vis[i].Name
		return
	}
	s.sel = -1
}

// selectSeed points the sticky selection at row [1] (the seed record). The
// seed is not a visible preset, so sel stays -1 and the identity is the
// reserved name itself; seedSelected is the predicate the marker, the verbs,
// and the adopt re-anchor read.
func (s *presetsScreen) selectSeed() {
	s.sel, s.selName = -1, fanout.BuiltinPresetName
}

// seedSelected reports whether the sticky selection currently holds row [1].
// The name comparison is exact: an ordinary preset can never carry the
// reserved name (the preset-row loop filters it out), so a selName equal to
// BuiltinPresetName can only be the seed pin.
func (s *presetsScreen) seedSelected() bool {
	return s.selName == fanout.BuiltinPresetName
}

// buildRows composes the row inventory: the fixed built-in row, one row per
// VISIBLE preset (the seed record filtered out: it is row [1], never row
// [n]), then the five action rows. The preset note is the one-line flag
// summary (task, n, timeout, tools) or, when only hidden fields are pinned,
// the pinned-count digest (survey defect D10); the default preset carries the
// badge. The built-in row's note is the engine-default composite while no
// seed record exists on disk; once the seed exists the note renders THE SEED
// RECORD (its flag summary plus the seats-pinned note), with the default
// badge and the copy hint appended the same way. The sticky-selected row
// carries the visible "●" marker the providers and models screens already
// draw, so the target of Edit/Duplicate/Delete/Set default is never invisible
// (audit F9's rule). The built-in row carries the marker only while the seed
// itself is pinned, and its label renders through presetNameOperand like any
// other name slot (a credential-shaped name cannot ride raw; the reserved
// name is benign and renders byte-identical).
func (s *presetsScreen) buildRows() []Row {
	rows := make([]Row, 0, len(s.pf.Presets)+6)
	rows = append(rows, Row{ID: presetsRowIDBuiltin, Label: s.builtinRowLabel(), Note: s.builtinRowNote()})
	vis := visiblePresets(s.pf)
	for i := range vis {
		p := &vis[i]
		note := presetFlagSummary(s.b, p)
		note = noteWithSeatsPinned(note, s.b, p.Seats)
		if p.Name == s.pf.DefaultPreset {
			note = noteWithDefaultBadge(note, s.b.T("presets.default.badge"))
		}
		label := presetNameOperand(p.Name)
		if i == s.sel {
			label = "● " + label
		}
		rows = append(rows, Row{ID: presetRowPrefix + p.Name, Label: label, Note: note})
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

// builtinRowLabel renders row [1]'s label: the reserved seed name through the
// same slot authority every preset name renders with, plus the marker while
// the seed is the sticky selection.
func (s *presetsScreen) builtinRowLabel() string {
	label := presetNameOperand(fanout.BuiltinPresetName)
	if s.seedSelected() {
		label = "● " + label
	}
	return label
}

// builtinRowNote composes row [1]'s note. With no seed record on disk it is
// the engine-default composite (the absent-seed preview, unchanged). With the
// seed on disk it is the seed record's own flag summary plus the
// seats-pinned note; when default_preset points at an existing seed the badge
// says so and carries the equivalence parenthetical (the key resolves the same
// record as no key, contract A3). A dangling key keeps the default badge but
// not that equivalence claim. The copy hint closes the line in both states.
func (s *presetsScreen) builtinRowNote() string {
	note := builtinDefaultsSummary(s.b)
	if seed := seedRecord(s.pf); seed != nil {
		note = presetFlagSummary(s.b, seed)
		note = noteWithSeatsPinned(note, s.b, seed.Seats)
	}
	if s.pf != nil && s.pf.DefaultPreset == fanout.BuiltinPresetName {
		badge := s.b.T("presets.default.badge")
		if seedRecord(s.pf) != nil {
			badge += " (" + missingOr(s.b, "presets.builtin.default_equiv", presetsBuiltinDefaultEquiv) + ")"
		}
		note = noteWithDefaultBadge(note, badge)
	}
	return noteWithDefaultBadge(note, missingOr(s.b, "presets.builtin.copy_note", presetsBuiltinCopyNote))
}

// syncMarker moves the visible selection marker to the sticky-selected row
// by rewriting the row labels IN PLACE (the providers and models screens'
// F-S1 pattern): s.rows and the stepList share the same backing array (SetRows
// stores the slice header), so the label writes are picked up by the next
// Render without a SetRows call, which would reset the scroll window and make
// the view jump on every selection move. Labels are recomputed from the row ID
// (presetNameOperand over the preset name) rather than prefix-stripped, so a name
// that happens to start with the marker glyph cannot confuse the sync, and a
// credential-shaped on-disk name masks here exactly as it does in buildRows (the
// registry-slot authority, which owns the underscore-head class; M-1 closed the
// last MaskOperand call site on this screen). The built-in row sits at index 0,
// so the visible preset at selection index i is row i+1; the built-in row
// itself is recomputed from its own label helper, which carries the marker
// only while the seed is the sticky selection (sel is -1 there, so the
// i == sel+1 test can never touch it).
func (s *presetsScreen) syncMarker() {
	for i := range s.rows {
		if s.rows[i].ID == presetsRowIDBuiltin {
			if label := s.builtinRowLabel(); s.rows[i].Label != label {
				s.rows[i].Label = label
			}
			continue
		}
		if !strings.HasPrefix(s.rows[i].ID, presetRowPrefix) {
			continue
		}
		label := presetNameOperand(s.rows[i].ID[len(presetRowPrefix):])
		if i == s.sel+1 {
			label = "● " + label
		}
		if s.rows[i].Label != label {
			s.rows[i].Label = label
		}
	}
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

// countSeatSpecs counts the non-empty tokens of a stored seats value (the seat
// lineup the preset pins). An empty value means "the engine picks the lineup",
// which is why the caller renders nothing for it.
func countSeatSpecs(seats string) int {
	n := 0
	for _, tok := range strings.Split(seats, ",") {
		if strings.TrimSpace(tok) != "" {
			n++
		}
	}
	return n
}

// noteWithSeatsPinned appends the pinned-lineup count to a preset note. The row
// is where a user sees WHICH presets fix the seats (the editor's seat list is
// where the lineup is edited), so a preset that pins four seats says so even
// when its visible knobs already fill the note. Hallucination guard: the count
// comes from the stored string itself, never from a parallel field.
func noteWithSeatsPinned(note string, b *i18n.Bundle, seats string) string {
	n := countSeatSpecs(seats)
	if n == 0 {
		return note
	}
	return noteWithDefaultBadge(note, fillHubSlots(
		missingOr(b, "presets.summary.seats", presetsSummarySeats),
		map[string]string{"count": strconv.Itoa(n)}))
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
		map[string]string{"name": presetNameOperand(name)})
}

// activate is the stepList onPick callback, shared by digit selection and
// arrows+Enter. Preset rows open the editor; action rows dispatch.
//
// This is where the Space pin is consumed (the selectAt lifecycle): activating a
// preset row navigates into the editor (a different surface with its own state)
// and the four sticky verbs (Edit, Duplicate, Delete, Set default) spend the pin
// they acted on, so both clear s.locked and the follow-on-rest rule resumes. The
// built-in row and the New row open a view/editor that reads no selection, so
// they leave the pin standing: the user comes back to the row they pinned. Esc
// pop needs no clearing: the popped screen instance is discarded, so the pin
// cannot outlive it.
func (s *presetsScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.rows) {
		return
	}
	id := s.rows[idx].ID
	switch {
	case id == presetsRowIDBuiltin:
		s.open(fanout.BuiltinPresetName)
	case strings.HasPrefix(id, presetRowPrefix):
		s.locked = false
		s.open(id[len(presetRowPrefix):])
	case id == presetsRowIDNew:
		s.open("")
	case id == presetsRowIDEdit:
		s.locked = false
		s.withSelected(func(p fanout.Preset) { s.open(p.Name) })
	case id == presetsRowIDDuplicate:
		s.locked = false
		s.withSelected(s.duplicate)
	case id == presetsRowIDDelete:
		s.locked = false
		s.withSelected(s.askDelete)
	case id == presetsRowIDSetDefault:
		s.locked = false
		s.withSelected(s.setDefault)
	}
}

// open forwards to the shell-supplied editor factory.
func (s *presetsScreen) open(name string) {
	if s.openEditor != nil {
		s.openEditor(name, s.app)
	}
}

// withSelected runs fn over the sticky-selected preset (or the pinned seed
// record, when row [1] is the selection). With no preset selected the user
// gets a message instead of a silent no-op.
func (s *presetsScreen) withSelected(fn func(fanout.Preset)) {
	if p, ok := s.selectedPreset(); ok {
		fn(p)
		return
	}
	if s.app != nil {
		s.app.SetMessage(missingOr(s.b, "presets.msg.select_first", presetsMsgSelectFirst))
	}
}

// selectedPreset resolves the sticky selection to a record: the seed record
// when row [1] is pinned, otherwise the visible preset at sel. The second
// return is false when the selection names nothing the store carries (the
// pin re-anchor normally drops those, so this is the defensive path).
func (s *presetsScreen) selectedPreset() (fanout.Preset, bool) {
	if s.seedSelected() {
		if seed := seedRecord(s.pf); seed != nil {
			return *seed, true
		}
		return fanout.Preset{}, false
	}
	vis := visiblePresets(s.pf)
	if s.sel >= 0 && s.sel < len(vis) {
		return vis[s.sel], true
	}
	return fanout.Preset{}, false
}

// selectedName returns the sticky-selected preset name, or "".
func (s *presetsScreen) selectedName() string {
	if p, ok := s.selectedPreset(); ok {
		return p.Name
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
	cp.PreserveSeatsMarker = true
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
		s.selectPreset(len(visiblePresets(s.pf)) - 1)
		// The marker now follows the selection, so the reload-time marker (the
		// pre-duplicate selection, clamped) has to move onto the copy: the
		// cursor stays on the Duplicate row, so no later reassert would.
		s.syncMarker()
		a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "presets.msg.duplicated", presetsMsgDuplicated),
			map[string]string{"name": presetNameOperand(name)}))
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
	{fanout.ErrPresetSeats, "presets.editor.validation.seats", presetsEditorValidationSeats},
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

// askDelete opens the M3 y/n gate. y removes the preset and clears the default
// key when it pointed there; n/Esc cancels with an explicit nothing-written
// message. The in-memory list is only mutated inside the corrupt gate
// callback, after both gates answered, so a declined gate leaves memory
// untouched and the next acknowledged save cannot pick up a half-applied
// deletion. On the built-in defaults row the same gate is RESET: it removes
// the seed record (every field, seats included) instead of an ordinary
// preset, so runs fall back to pure engine defaults.
//
// Gate class, stated precisely (P5-1 U6): delete keeps the M3 y/n because the
// body already names the one record that disappears, while the default_preset
// write next door is M4 (approveSetDefault) because it changes a KEY the record
// listing does not show. The old note here said "the preset file is not
// providers.json, so no M4 approval is needed", which stopped being true the
// moment the same file's default write got the exact-diff door.
func (s *presetsScreen) askDelete(p fanout.Preset) {
	a := s.app
	masked := presetNameOperand(p.Name)
	if fanout.IsBuiltinPresetName(p.Name) {
		s.askResetBuiltin(masked)
		return
	}
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

// askResetBuiltin is the Delete verb on row [1]: the M3 y/n gate keeps its
// ordinary shape and names the seed row, but y removes the SEED record (every
// field, seats included) so the store falls back to pure engine defaults, and
// a default_preset key pointing at the seed is cleared with it (left dangling
// it would outrank the engine rungs and only warn). The after-message says
// the row returns to engine defaults.
func (s *presetsScreen) askResetBuiltin(masked string) {
	a := s.app
	a.ShowModal(NewConfirm(missingOr(s.b, "presets.reset.title", presetsResetTitle),
		[]string{fillHubSlots(missingOr(s.b, "presets.reset.body", presetsResetBody),
			map[string]string{"name": masked})},
		func(approved bool) {
			if !approved {
				a.SetMessage(missingOr(s.b, "presets.msg.cancelled", presetsMsgCancelled))
				return
			}
			s.gateCorruptSave(func() {
				keep := make([]fanout.Preset, 0, len(s.pf.Presets))
				for i := range s.pf.Presets {
					if !fanout.IsBuiltinPresetName(s.pf.Presets[i].Name) {
						keep = append(keep, s.pf.Presets[i])
					}
				}
				s.pf.Presets = keep
				if fanout.IsBuiltinPresetName(s.pf.DefaultPreset) {
					s.pf.DefaultPreset = ""
				}
				if err := s.store.SavePresets(s.pf); err != nil {
					a.SetMessage(s.b.T("common.error") + ": " + s.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
					s.reload()
					return
				}
				s.reload()
				a.SetMessage(s.b.T("common.success") + ": " +
					missingOr(s.b, "presets.msg.builtin_reset", presetsMsgBuiltinReset))
			})
		}))
}

// setDefault writes the store's default_preset key to the selected preset. The
// corrupt acknowledgment stays the outermost gate (a precondition about the
// file's readability); the exact-diff consent it releases is the M4 decision
// that runs the write (P5-1 U6: this key used to be written with no approval on
// a healthy file).
func (s *presetsScreen) setDefault(p fanout.Preset) {
	a := s.app
	if a == nil {
		return
	}
	s.gateCorruptSave(func() { s.approveSetDefault(a, p) })
}

// approveSetDefault opens the M4 approval modal for the default_preset write
// (W28): the key sentence (old value -> new value, both display-masked), the
// store's own "File:" line, the atomic sentence for that file, and the
// removed/added diff lines. y re-reads the store and refuses when the presented
// preset list or the default key moved, then writes the FRESH store with the key
// set; n/Esc writes nothing. Nothing here mutates s.pf before the answer.
func (s *presetsScreen) approveSetDefault(a *App, p fanout.Preset) {
	storePath := presetFilePath(storeFile(s.store))
	storeBase := providers.MaskOperand(filepath.Base(storePath))
	path := providers.MaskOperand(storePath)
	base := presetsWriteBaseFrom(s.pf)
	name := presetNameOperand(p.Name)
	fileLine := fillHubSlots(missingOr(s.b, "presets.explain.file", presetsExplainFile), map[string]string{"path": path})
	summary := []string{
		fmt.Sprintf(presetsApprovalDefault, presetDefaultDisplay(s.pf.DefaultPreset), name),
		fileLine,
		fmt.Sprintf(presetsApprovalAtomic, storeBase),
	}
	if strings.TrimSpace(s.pf.DefaultPreset) == "" {
		summary = append(summary, s.b.Tf("modal.approval.diff_added", map[string]string{
			"line": fmt.Sprintf(presetsApprovalKeyFmt, name),
		}))
	} else {
		summary = append(summary,
			s.b.Tf("modal.approval.diff_removed", map[string]string{
				"line": fmt.Sprintf(presetsApprovalKeyFmt, presetDefaultDisplay(s.pf.DefaultPreset)),
			}),
			s.b.Tf("modal.approval.diff_added", map[string]string{
				"line": fmt.Sprintf(presetsApprovalKeyFmt, name),
			}),
		)
	}
	a.ShowModal(NewApproval(s.b.T("presets.action.set_default"), summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.b, "presets.msg.cancelled", presetsMsgCancelled))
			return
		}
		// Write-time re-read: the store the modal described is the store the
		// write extends, and a moved file is refused by name instead of being
		// silently overwritten from a stale in-memory snapshot.
		fresh, loadErr := s.loadPresets()
		if loadErr != "" {
			a.SetMessage(s.b.T("common.error") + ": " + loadErr)
			s.reload()
			return
		}
		if drift := base.drift(fresh); drift != "" {
			s.reload()
			a.SetMessage(providers.MaskSentence(fmt.Sprintf(presetsApprovalDrift, storeBase, drift)))
			return
		}
		fresh.DefaultPreset = p.Name
		if err := s.store.SavePresets(fresh); err != nil {
			a.SetMessage(s.b.T("common.error") + ": " + s.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
			s.reload()
			return
		}
		s.reload()
		a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "presets.msg.default_set", presetsMsgDefaultSet),
			map[string]string{"name": presetNameOperand(p.Name)}))
	}))
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
	// per-record drop notices (a specific record was ignored; never the
	// corrupt-file claim), the corrupt-file warning, and the empty state (a
	// line plus the blank that keeps the list off it). Its length is what the
	// window has to leave room for; the explanation lines and the list hint
	// with its blank are already part of presetsListChrome.
	var notice []string
	if s.loadErr != "" {
		notice = append(notice, cellwidth.Truncate(s.b.T("common.error")+": "+s.loadErr, a.Width()))
	}
	for _, drop := range s.dropNotices {
		notice = append(notice, cellwidth.Truncate(drop, a.Width()))
	}
	if warn != "" {
		notice = append(notice, cellwidth.Truncate(warn, a.Width()))
	}
	if len(visiblePresets(s.pf)) == 0 && seedRecord(s.pf) == nil {
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
// seat/tool/time budgets, not a prompt; it MAY pin the seat lineup, and the
// editor's seat list is where that is edited), how a run consumes it (agy pro
// fanout --preset <name>, or the AGY_PRO_FANOUT_PRESET env var), where it lives
// (the store's on-disk path, through MaskOperand like every other echoed path),
// and the default_preset rule (the key auto-applies to runs that name no preset;
// since P3-1 a conflicting knob is dropped with a run-time warning instead of
// killing the run). Each line is truncated to the frame width, so an
// AGY_PRO_HOME-long path can only ever cut its own line, never the rule lines
// below it.
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
//
// The reassert is gated by s.locked, the Space pin (see the type doc): while a
// pin holds, arrow travel over other preset rows leaves the target alone. The
// stepList consumes Space before this block runs, so the pin is already set
// when the gate is read.
func (s *presetsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.reloadIfChanged()
	s.list.HandleKey(k)
	if isPresetDeleteKey(k) {
		s.locked = false
		s.withSelected(s.askDelete)
		return
	}
	if s.locked {
		return
	}
	if c := s.list.Cursor(); c >= 1 && c <= len(visiblePresets(s.pf)) {
		if s.sel != c-1 {
			s.selectPreset(c - 1)
			s.syncMarker()
		}
	}
}

// isPresetDeleteKey accepts the console's printable-rune form and the plain
// spelling used by scripted keys and line mode, matching isRefreshKey.
func isPresetDeleteKey(k Key) bool {
	if k == Key("d") {
		return true
	}
	r, ok := RuneOf(k)
	return ok && (r == 'd' || r == 'D')
}

// selectAt is the stepList Space callback: the explicit pin. Row 0 is the
// built-in defaults row: with the seed record on disk it pins THE SEED (the
// verbs then act on row [1]: Edit opens its editor, Delete resets it, Set
// default writes the key); with no seed it stays a consumed no-op, because
// there is no record to act on and a pin pointing at nothing would only
// confuse the verbs below. Rows 1..len(visiblePresets) are the ordinary
// presets, so the boundary is the row index minus one: Space on an action row
// is a consumed no-op with no state change, Space on a preset row pins it,
// locks the selection, moves the marker in place and confirms on the message
// line. Pinning again re-pins (the lock simply stays on). Space never
// activates: this callback never reaches activate, so a pin cannot open the
// editor or any write gate. The pin also records the NAME it holds, the
// identity reload re-anchors by (F-01); the seed pin holds the reserved name.
func (s *presetsScreen) selectAt(idx int) {
	if idx == 0 {
		if seedRecord(s.pf) == nil {
			return
		}
		s.selectSeed()
		s.locked = true
		s.syncMarker()
		if s.app != nil {
			s.app.SetMessage(fillHubSlots(missingOr(s.b, "list.msg.pinned", listMsgPinned),
				map[string]string{"name": presetNameOperand(fanout.BuiltinPresetName)}))
		}
		return
	}
	vis := visiblePresets(s.pf)
	if idx < 1 || idx > len(vis) {
		return
	}
	s.selectPreset(idx - 1)
	s.locked = true
	s.syncMarker()
	if s.app != nil {
		s.app.SetMessage(fillHubSlots(missingOr(s.b, "list.msg.pinned", listMsgPinned),
			map[string]string{"name": presetNameOperand(vis[s.sel].Name)}))
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
