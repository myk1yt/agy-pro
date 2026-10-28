package tui

import (
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// Row IDs of the fixed action rows at the bottom of the models list, in
// display order. Model rows carry ID "model\x00<model id>" and always sort
// before these.
const (
	modelsActionAddID    = "add"
	modelsActionEditID   = "edit"
	modelsActionDeleteID = "delete"
	modelsActionToggleID = "toggle"
	modelsActionToolsID  = "tools"
	modelsActionBackID   = "back"
)

// modelRowPrefix marks model rows in Row.ID so activation can tell a model
// row from an action row without matching label text.
const modelRowPrefix = "model\x00"

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved). missingOr resolves
// the catalog first, so a const wins only for a catalog that lacks the
// translation (screen_hub.go pattern).
const (
	// catalog-resolved, fallback kept: models.action.tools
	missingModelsActionTools = "Edit tools"
	// catalog-resolved, fallback kept: models.msg.select_model
	missingModelsSelectModel = "Select a model row first."
	// catalog-resolved, fallback kept: models.msg.deleted
	missingModelsDeleted = "Deleted model {{id}} from {{provider}}."
	// catalog-resolved, fallback kept: models.msg.cancelled
	missingModelsCancelled = "Cancelled. Nothing was written."
	// catalog-resolved, fallback kept: models.msg.enabled_set
	missingModelsEnabledSet = "Model {{id}} in {{provider}}: enabled set to {{state}}."
	// catalog-resolved, fallback kept: models.msg.tools_saved
	missingModelsToolsSaved = "Tool grants for {{id}} updated: {{n}} tools."
	// catalog-resolved, fallback kept: models.delete.count
	missingModelsDeleteCount = "Model count: {{before}} -> {{after}}"
	// catalog-resolved, fallback kept: models.delete.last_warning
	missingModelsDeleteLastWarning = "This is the last model of the provider. The provider keeps an empty model list."
	// catalog-resolved, fallback kept: models.tools.title
	missingModelsToolsTitle = "Edit tool grants"
	// catalog-resolved, fallback kept: models.tools.hint
	missingModelsToolsHint = "Space toggles a tool · Enter saves · Esc cancels"
	// catalog-resolved, fallback kept: models.provider_missing
	missingModelsProviderMissing = "Provider {{provider}} not found in the registry."
	// catalog-resolved, fallback kept: models.toggle.title
	missingModelsToggleTitle = "Toggle Enabled?"
	// catalog-resolved, fallback kept: models.toggle.body
	missingModelsToggleBody = "Sets enabled for model {{id}} in {{provider}}."
	// catalog-resolved, fallback kept: models.tools.approval.title
	missingModelsToolsApprovalTitle = "Update Tool Grants?"
	// catalog-resolved, fallback kept: models.tools.approval.body
	missingModelsToolsApprovalBody = "Updates tool grants for model {{id}} in {{provider}}."
	// catalog-resolved, fallback kept: models.tools.min_one
	missingModelsToolsMinOne = "At least one tool must stay granted. An empty grant list means the model may use every tool."
	// catalog-resolved, fallback kept: models.cap.thinking
	missingModelsCapThinking = "thinking"
	// catalog-resolved, fallback kept: models.cap.vision
	missingModelsCapVision = "vision"
	// catalog-resolved, fallback kept: models.cap.video
	missingModelsCapVideo = "video"
)

// knownToolList is the closed tool universe the runtime ships: the agenttools
// dispatch (runner.go) and the OpenAI definitions (types.go, plus the network
// tools in fetch.go/search.go). Order is the picker's row order, and it keeps
// the fanout --tools token groups adjacent (read: read_file,list_dir;
// search: grep,glob; write: write_file,replace_in_file; exec: run_command).
// search_web and read_url_content have no capability token: the engine offers
// them per run (--search-endpoint / --allow-host), and a granted name is what
// the runner's model-level filter checks. The tools overlay multi-selects over
// exactly this list; free-form tool names are the form unit's job.
var knownToolList = []string{
	"read_file", "list_dir", "grep", "glob",
	"write_file", "replace_in_file", "run_command",
	"search_web", "read_url_content",
}

// missingOr resolves a catalog key, falling back to the local English
// const while the catalog lacks the key.
func missingOr(b *i18n.Bundle, key, fallback string) string {
	if s := b.T(key); s != key {
		return s
	}
	return fallback
}

// modelsScreen is the per-provider model management surface (architect-report
// section 3, screen 3). One row per model, then the fixed action rows. Every
// providers.json write goes through the M4 Approval modal (delete, toggle,
// tools); reads go straight through providers.Load, and Draw re-reads the
// registry behind an on-disk signature check (the prompts-screen pattern), so
// rows returning from the model form (or mutated behind the screen's back)
// render fresh on the next frame. Navigation is the shared stepList, so
// digits and arrows+Enter are both always live (requirement 2).
//
// The sticky selection follows the cursor while the cursor rests on a model row
// (user decision 2A), and that rule alone made the action rows unreachable for
// arrow-only users: the action block sits BELOW the model block, so travelling
// down to Edit/Delete/Toggle/Tools passes every remaining model row and each
// rest re-stole the target, leaving the verb on the LAST model. Space pins a row
// explicitly (locked, selectAt) and the cursor-reassert is suspended until the
// pin is consumed.
//
// The pin is held by MODEL ID, not by index (fix round 1, F-01: the providers
// screen's by-name rule). The registry is rewritten whole by other surfaces (the
// model form above this screen, another session), so an index re-clamped after a
// reload can name a DIFFERENT model than the one the user pinned, and a locked
// selection made that silent: the pin survived while the verbs pointed at a
// neighbour. A pin therefore re-anchors by ID on reload and drops (with its lock)
// when the ID no longer resolves, which leaves nothing selected and the verbs
// refusing rather than guessing.
type modelsScreen struct {
	core         *settings.Core
	providerName string
	openForm     func(mode string, modelID string, a *App)
	lang         string
	b            *i18n.Bundle
	list         *stepList
	rows         []Row
	models       []providers.Model
	sel          int
	selID        string // model ID the selection holds (identity the Space pin re-anchors by)
	loadErr      string
	sig          string
	locked       bool // Space pinned sel: the cursor reassert must not overwrite it
	app          *App
}

// NewModelsScreen builds the models list for one provider. openForm is the
// form screen factory: "add" with an empty modelID, or "edit" with the model
// ID; tests inject a spy. The UI language resolves from the persisted state
// (AGY_PRO_HOME state.json), defaulting to English.
func NewModelsScreen(core *settings.Core, providerName string, openForm func(mode string, modelID string, a *App)) Screen {
	s := &modelsScreen{core: core, providerName: providerName, openForm: openForm, sel: -1}
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
func (s *modelsScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// Mode is ModeNav: digits and arrows both navigate.
func (s *modelsScreen) Mode() Mode { return ModeNav }

// Title resolves through the catalog; the provider operand is a disk-borne
// registry slot, so it routes through the slot masker (key-shaped and
// location-decorated provider names can never render raw).
func (s *modelsScreen) Title() string {
	return s.b.Tf("models.screen.title_for", map[string]string{"provider": maskSlotOperand(s.providerName)})
}

// findProviderCI resolves a provider case-insensitively, matching the Core
// verb semantics (wizard upsert precedent).
func findProviderCI(reg *providers.Registry, name string) *providers.Provider {
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return &reg.Providers[i]
		}
	}
	return nil
}

// reload re-reads the registry from disk and rebuilds the rows. Every write
// verb is followed by this, so the sibling models render from the freshly
// saved file (the sibling-survival pin lives in the tests). The cursor is
// restored by ROW ID after the rebuild, so a mutation that shifts the row
// layout cannot leave the visible cursor on a different verb or model than
// the user saw (audit F9); a row that vanished (a deleted model) falls back
// to the old index clamped into the new range by SetCursor, which can land
// past the model block on an action row when the vanished row was the last
// model row. The on-disk signature is recorded so reloadIfChanged can skip the
// rebuild while the file is unchanged.
//
// The sticky selection has two re-anchor rules (fix round 1, F-01): a Space pin
// is resolved by MODEL ID and drops with its lock when the ID is gone, while an
// unpinned selection keeps the historical index clamp. The split is deliberate:
// the pin is the state that can silently re-target a neighbour the user never
// chose, whereas the unlocked clamp is itself a pinned contract
// (TestModelsCursorIdentityAfterDelete: a stray Enter after a delete reopens the
// gate for the survivor) and is always visible through the marker.
func (s *modelsScreen) reload() {
	s.models = nil
	s.loadErr = ""
	s.sig = ""
	reg, err := providers.Load()
	switch {
	case err != nil:
		s.loadErr = err.Error()
	default:
		p := findProviderCI(reg, s.providerName)
		if p == nil {
			s.loadErr = fillHubSlots(missingOr(s.b, "models.provider_missing", missingModelsProviderMissing),
				map[string]string{"provider": maskSlotOperand(s.providerName)})
		} else {
			s.models = append([]providers.Model(nil), p.Models...)
			s.sig = modelsSig(reg, s.providerName)
		}
	}
	if s.locked {
		// Re-anchor the pin by identity. A pinned model that moved keeps its
		// target; a pinned model that is gone leaves NOTHING selected, so the verbs
		// refuse (withCursorModel's select-a-model message) instead of acting on
		// whatever index the clamp landed on.
		s.selectModel(modelsIndexByID(s.models, s.selID))
		if s.sel < 0 {
			s.selID, s.locked = "", false
		}
	} else {
		// Unpinned selection: the historical clamp, floored at -1, with the identity
		// re-read from the row the clamp landed on so a later pin starts from the
		// truth.
		if s.sel > len(s.models)-1 {
			s.sel = len(s.models) - 1
		}
		if s.sel < 0 {
			s.selectModel(-1)
		} else {
			s.selectModel(s.sel)
		}
	}
	s.rebuild()
}

// modelsIndexByID resolves a model ID to its index in models, or -1 when the
// slice does not carry it. The ID is the registry's addressing key inside one
// provider (Core.ModelDelete addresses an ID, never a position), so it is also
// the identity the Space pin is held by.
func modelsIndexByID(models []providers.Model, id string) int {
	if id == "" {
		return -1
	}
	for i := range models {
		if models[i].ID == id {
			return i
		}
	}
	return -1
}

// selectModel points the sticky selection at index i and records the identity it
// holds. Every write site (the cursor reassert, the Space pin, the reload clamp)
// goes through here, so sel and selID cannot disagree while a pin is live.
func (s *modelsScreen) selectModel(i int) {
	s.sel, s.selID = i, ""
	if i >= 0 && i < len(s.models) {
		s.selID = s.models[i].ID
		return
	}
	s.sel = -1
}

// rebuild refreshes the row inventory and restores the cursor by ROW ID, so
// a mutation that shifts the row layout cannot leave the visible cursor on
// a different verb or model than the user saw (audit F9); a row that
// vanished (a deleted model) falls back to the old index clamped into the
// new range by SetCursor, which can land past the model block on an action
// row when the vanished row was the last model row. Selection moves do NOT
// go through here: HandleKey calls syncMarker, which rewrites the marker in
// place so the list window never resets (review finding F-S1).
func (s *modelsScreen) rebuild() {
	oldCursor := s.list.Cursor()
	oldID := ""
	if oldCursor >= 0 && oldCursor < len(s.rows) {
		oldID = s.rows[oldCursor].ID
	}
	s.rows = s.buildRows()
	s.list.SetRows(s.rows)
	cursor := oldCursor
	for i, r := range s.rows {
		if r.ID == oldID {
			cursor = i
			break
		}
	}
	s.list.SetCursor(cursor)
}

// syncMarker moves the visible selection marker to the sticky-selected
// model row by rewriting the row labels IN PLACE. s.rows and the stepList
// share the same backing array (SetRows stores the slice header), so the
// label writes are picked up by the next Render without a SetRows call,
// which would reset the scroll window and make the view jump on every
// selection move (review finding F-S1). Labels are recomputed from the row
// ID (the slot masker over the model id) rather than prefix-stripped, so a
// model id that happens to start with the marker glyph cannot confuse the
// sync, and a decorated on-disk id masks here exactly as it does in buildRows.
func (s *modelsScreen) syncMarker() {
	for i := range s.rows {
		if !strings.HasPrefix(s.rows[i].ID, modelRowPrefix) {
			continue
		}
		label := maskSlotOperand(s.rows[i].ID[len(modelRowPrefix):])
		if i == s.sel {
			label = "● " + label
		}
		if s.rows[i].Label != label {
			s.rows[i].Label = label
		}
	}
}

// modelsSig builds a signature over the provider's on-disk model records:
// every field the rows render (id, family, context, maxOut, enabled, the
// capability flags SupportsTools/SupportsThinking/ReasoningEffort/
// SupportsVision/SupportsVideo, and the tool grants). A change in any of
// them moves the signature.
func modelsSig(reg *providers.Registry, name string) string {
	p := findProviderCI(reg, name)
	if p == nil {
		return "\x00missing"
	}
	parts := make([]string, len(p.Models))
	for i, m := range p.Models {
		en := "nil"
		if m.Enabled != nil {
			en = strconv.FormatBool(*m.Enabled)
		}
		parts[i] = strings.Join([]string{
			m.ID,
			m.Family,
			strconv.Itoa(m.ContextLength),
			strconv.Itoa(m.MaxOutputTokens),
			en,
			strconv.FormatBool(m.Capabilities.SupportsTools),
			strconv.FormatBool(m.Capabilities.SupportsThinking),
			m.Capabilities.ReasoningEffort,
			strconv.FormatBool(m.Capabilities.SupportsVision),
			strconv.FormatBool(m.Capabilities.SupportsVideo),
			strings.Join(m.Capabilities.AllowedTools, ","),
		}, "\x1f")
	}
	return strings.Join(parts, "\x1e")
}

// reloadIfChanged re-reads the registry and rebuilds the rows only when the
// on-disk signature moved (the prompts-screen syncRows pattern). Returning
// from the model form after an add/update therefore shows fresh rows instead
// of the stale snapshot the popped-back screen was built from, and a registry
// mutated behind the screen's back is picked up on the next frame.
func (s *modelsScreen) reloadIfChanged() {
	reg, err := providers.Load()
	if err != nil {
		if s.loadErr == "" {
			s.reload()
		}
		return
	}
	if modelsSig(reg, s.providerName) == s.sig {
		return
	}
	s.reload()
}

// buildRows composes the row inventory: one row per model, then the fixed
// action rows. The model note carries family, context/maxOut summary,
// enabled state, the tool grant count, and the thinking/vision/video
// capability flags. The sticky-selected model row carries a visible marker
// so the target of the Edit/Delete/Toggle/Tools rows is never invisible
// (audit F9 asked for a marker here because a stray Enter after a mutation
// must act on the row the user sees marked; the presets screen now draws the
// same marker, and the three sticky-target screens draw it from one pattern).
func (s *modelsScreen) buildRows() []Row {
	rows := make([]Row, 0, len(s.models)+6)
	for i, m := range s.models {
		// The label is a disk-borne registry slot, so it routes through the
		// slot masker (H-3): MaskOperand's location exemption printed a
		// decorated on-disk id raw in the row.
		label := maskSlotOperand(m.ID)
		if i == s.sel {
			label = "● " + label
		}
		rows = append(rows, Row{ID: modelRowPrefix + m.ID, Label: label, Note: s.modelNote(m)})
	}
	b := s.b
	rows = append(rows,
		Row{ID: modelsActionAddID, Label: b.T("models.action.add")},
		Row{ID: modelsActionEditID, Label: b.T("models.action.edit")},
		Row{ID: modelsActionDeleteID, Label: b.T("models.action.delete")},
		Row{ID: modelsActionToggleID, Label: b.T("models.action.toggle")},
		Row{ID: modelsActionToolsID, Label: missingOr(b, "models.action.tools", missingModelsActionTools)},
		Row{ID: modelsActionBackID, Label: b.T("common.back")},
	)
	return rows
}

// modelNote renders one annotation string: family, context and maxOut
// summary, enabled state, the granted-tool count, and the thinking /
// vision / video capability flags. The thinking flag carries the reasoning
// effort in parentheses (thinking(high)), the same shape the CLI twin's
// Capabilities.Summary renders. Both the family and the effort are
// registry-persisted free text, so each routes through the slot masker (the
// strip plus the registry-slot authority), which also removes a Cf rune the
// painter would otherwise strip at render time and expose the bare value. The
// flags use the models.cap.* catalog keys with English const fallbacks.
func (s *modelsScreen) modelNote(m providers.Model) string {
	b := s.b
	parts := []string{}
	if m.Family != "" {
		// The family slot uses the registry-slot masker (the same authority
		// MaskFamilyName applies to an explicit family), never the raw field.
		parts = append(parts, maskSlotOperand(m.Family))
	}
	if m.ContextLength > 0 {
		parts = append(parts, b.T("models.column.context")+" "+strconv.Itoa(m.ContextLength))
	}
	if m.MaxOutputTokens > 0 {
		parts = append(parts, b.T("models.column.max_out")+" "+strconv.Itoa(m.MaxOutputTokens))
	}
	if m.IsEnabled() {
		parts = append(parts, b.T("common.enabled"))
	} else {
		parts = append(parts, b.T("common.disabled"))
	}
	parts = append(parts, b.T("models.cap.tools")+" "+strconv.Itoa(len(m.Capabilities.AllowedTools)))
	if m.Capabilities.SupportsVision {
		parts = append(parts, missingOr(b, "models.cap.vision", missingModelsCapVision))
	}
	if m.Capabilities.SupportsVideo {
		parts = append(parts, missingOr(b, "models.cap.video", missingModelsCapVideo))
	}
	if m.Capabilities.SupportsThinking {
		effort := maskSlotOperand(m.Capabilities.EffectiveReasoningEffort())
		parts = append(parts, missingOr(b, "models.cap.thinking", missingModelsCapThinking)+"("+effort+")")
	}
	return strings.Join(parts, " · ")
}

// activate is the stepList onPick callback, shared by digit selection and
// Enter. Model rows open the form in edit mode; action rows dispatch.
//
// This is where the Space pin is consumed (the selectAt lifecycle): activating
// a model row navigates into the edit form (a different surface with its own
// state), the four sticky verbs (Edit, Delete, Toggle, Tools) spend the pin
// they acted on, and Back pops the screen, so all of them clear s.locked and
// the follow-on-rest rule resumes. The Add row is not a selection verb: it
// leaves the pin standing, so returning from the add form shows the row the
// user pinned. Esc pop needs no clearing here (App.Pop runs before this screen
// sees the key) and needs none afterwards either: the popped screens are
// discarded instances, so the pin cannot outlive them.
func (s *modelsScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.rows) {
		return
	}
	id := s.rows[idx].ID
	if strings.HasPrefix(id, modelRowPrefix) {
		s.locked = false
		if s.openForm != nil {
			s.openForm("edit", id[len(modelRowPrefix):], s.app)
		}
		return
	}
	switch id {
	case modelsActionAddID:
		if s.openForm != nil {
			s.openForm("add", "", s.app)
		}
	case modelsActionEditID:
		s.locked = false
		s.withCursorModel(func(m providers.Model) {
			if s.openForm != nil {
				s.openForm("edit", m.ID, s.app)
			}
		})
	case modelsActionDeleteID:
		s.locked = false
		s.askDelete()
	case modelsActionToggleID:
		s.locked = false
		s.toggleEnabled()
	case modelsActionToolsID:
		s.locked = false
		s.openTools()
	case modelsActionBackID:
		s.locked = false
		s.app.Pop()
	}
}

// withCursorModel runs fn over the selected model. The selection is sticky:
// it follows the cursor while the cursor sits on a model row and survives
// the move onto an action row (digit activation jumps the cursor to the
// action row, so the action verbs cannot read the cursor position
// directly). The selection starts at -1 (user decision 2A, mirroring the
// presets screen): entering the screen and firing an action verb without
// first resting the cursor on a model row refuses with the select-a-model
// message instead of silently targeting model[0].
func (s *modelsScreen) withCursorModel(fn func(providers.Model)) {
	if s.sel >= 0 && s.sel < len(s.models) {
		fn(s.models[s.sel])
		return
	}
	if s.app != nil {
		s.app.SetMessage(missingOr(s.b, "models.msg.select_model", missingModelsSelectModel))
	}
}

// askDelete opens the M4 Approval modal with the exact mutation summary:
// the registry-entry-only sentence from the catalog (model id and provider
// through the slot masker), the model count N -> N-1, and the last-model
// warning when the provider would keep an empty list. y runs Core.ModelDelete
// and reloads; n or Esc cancels with an explicit nothing-written message.
//
// The id and the provider name are disk-borne registry slots (no validator
// ever saw them on this path), so both route through maskSlotOperand: the
// location-exempt MaskOperand printed a decorated hand-edited id raw here
// (H-3).
func (s *modelsScreen) askDelete() {
	s.withCursorModel(func(m providers.Model) {
		a := s.app
		n := len(s.models)
		maskedID := maskSlotOperand(m.ID)
		maskedProvider := maskSlotOperand(s.providerName)
		summary := []string{
			s.b.Tf("models.delete.body", map[string]string{"id": maskedID, "provider": maskedProvider}),
			fillHubSlots(missingOr(s.b, "models.delete.count", missingModelsDeleteCount), map[string]string{
				"before": strconv.Itoa(n),
				"after":  strconv.Itoa(n - 1),
			}),
		}
		if n == 1 {
			summary = append(summary, missingOr(s.b, "models.delete.last_warning", missingModelsDeleteLastWarning))
		}
		a.ShowModal(NewApproval(s.b.T("models.delete.title"), summary, func(approved bool) {
			if !approved {
				a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
				return
			}
			if err := s.core.ModelDelete(s.providerName, m.ID); err != nil {
				a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
				return
			}
			s.reload()
			a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "models.msg.deleted", missingModelsDeleted), map[string]string{
				"id":       maskedID,
				"provider": maskedProvider,
			}))
		}))
	})
}

// toggleEnabled opens the M4 Approval modal with the exact mutation summary:
// the masked provider + model id sentence and the enabled field before/after.
// y runs Core.ModelSetEnabled and reloads; n or Esc writes nothing. The
// summary lines carry only slot-masked registry operands and catalog values,
// so a key-shaped or decorated model id can never render raw in the approval
// frame.
func (s *modelsScreen) toggleEnabled() {
	s.withCursorModel(func(m providers.Model) {
		a := s.app
		next := !m.IsEnabled()
		maskedID := maskSlotOperand(m.ID)
		maskedProvider := maskSlotOperand(s.providerName)
		before := s.b.T("common.disabled")
		after := s.b.T("common.enabled")
		if !next {
			before, after = after, before
		}
		summary := []string{
			fillHubSlots(missingOr(s.b, "models.toggle.body", missingModelsToggleBody), map[string]string{
				"id":       maskedID,
				"provider": maskedProvider,
			}),
			fillHubSlots(s.b.T("models.form.diff_changed"), map[string]string{
				"field":  s.b.T("models.form.enabled"),
				"before": before,
				"after":  after,
			}),
		}
		a.ShowModal(NewApproval(missingOr(s.b, "models.toggle.title", missingModelsToggleTitle), summary, func(approved bool) {
			if !approved {
				a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
				return
			}
			if err := s.core.ModelSetEnabled(s.providerName, m.ID, next); err != nil {
				a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
				return
			}
			s.reload()
			a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "models.msg.enabled_set", missingModelsEnabledSet), map[string]string{
				"id":       maskedID,
				"provider": maskedProvider,
				"state":    after,
			}))
		}))
	})
}

// openTools opens the M2-style multi-select overlay over the known tool
// list. Space toggles the tool under the cursor, Enter opens the M4 Approval
// modal with the FULL before/after grant list (grants outside the known
// list pass through untouched, union semantics, and appear in the diff so
// nothing is hidden), Esc cancels. The overlay refuses to clear the last
// grant: an empty AllowedTools is read by the engine as allow-every-tool
// (Capabilities.ToolPermitted), the opposite of the gesture. The full
// capabilities editor lives in the form screen.
//
// The displayed selection is the canonical view (grantedList returns known
// order, de-duplicated), but the WRITE target is derived, not assumed: the
// overlay reports whether any row was toggled, and when none was, the record's
// RAW grant slice is committed, so an Enter with no toggle cannot re-sort or
// de-duplicate a hand-edited registry entry behind a no-diff approval (the form
// path's defect class, same door one screen over). The untouched test is
// structural — the checked-row set now versus the set the overlay opened with —
// so it holds for every raw shape, including one that repeats an unlisted
// grant, which no normalized comparison can match (G-2). Once the selection
// really changes, the committed list is the canonical one the overlay showed.
func (s *modelsScreen) openTools() {
	s.withCursorModel(func(m providers.Model) {
		a := s.app
		maskedID := maskSlotOperand(m.ID)
		maskedProvider := maskSlotOperand(s.providerName)
		// The RAW grant slice of the record under the cursor (file order,
		// repeats intact), snapshotted so the consent modal keeps judging
		// against the list it was opened on even if the registry reloads
		// behind it.
		raw := append([]string(nil), m.Capabilities.AllowedTools...)
		ov := newToolsOverlay(
			missingOr(s.b, "models.tools.title", missingModelsToolsTitle),
			missingOr(s.b, "models.tools.hint", missingModelsToolsHint),
			knownToolList,
			raw,
			func(granted []string, untouched bool) {
				if len(granted) == 0 {
					a.SetMessage(missingOr(s.b, "models.tools.min_one", missingModelsToolsMinOne))
					return
				}
				// Untouched is the overlay's structural verdict (no row
				// toggled), not a comparison of shapes: commit the raw slice
				// then, and the canonical selection once a real toggle moved
				// it.
				target := granted
				if untouched {
					target = raw
				}
				// The grant lists render through maskJoined (the form
				// screen's per-item rule), not a raw join: a
				// credential-shaped hand-edited grant must never print raw
				// in the approval diff, and one secret-shaped neighbor must
				// not be able to hide behind the join separator.
				before := maskJoined(m.Capabilities.AllowedTools, ", ")
				if before == "" {
					before = s.b.T("models.form.caps_none")
				}
				after := maskJoined(target, ", ")
				summary := []string{
					fillHubSlots(missingOr(s.b, "models.tools.approval.body", missingModelsToolsApprovalBody), map[string]string{
						"id":       maskedID,
						"provider": maskedProvider,
					}),
					fillHubSlots(s.b.T("models.form.diff_changed"), map[string]string{
						"field":  s.b.T("models.cap.tools"),
						"before": before,
						"after":  after,
					}),
				}
				a.ShowModal(NewApproval(missingOr(s.b, "models.tools.approval.title", missingModelsToolsApprovalTitle), summary, func(approved bool) {
					if !approved {
						a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
						return
					}
					if err := s.core.ModelSetTools(s.providerName, m.ID, target); err != nil {
						a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
						return
					}
					s.reload()
					a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "models.msg.tools_saved", missingModelsToolsSaved), map[string]string{
						"id": maskedID,
						"n":  strconv.Itoa(len(target)),
					}))
				}))
			},
			func() {
				a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
			},
		)
		a.ShowModal(ov)
	})
}

// Draw renders the model rows (or the empty/error line), the list, and the
// two-handled footer hint. All cell math goes through cellwidth. The
// registry is re-read behind the signature check first, so a form commit or
// an external mutation shows up on this frame.
func (s *modelsScreen) Draw(a *App) []string {
	s.app = a
	s.reloadIfChanged()
	s.list.SetPageSize(pageSizeFor(a.Height(), 6))
	var lines []string
	switch {
	case s.loadErr != "":
		lines = append(lines, cellwidth.Truncate(s.b.T("common.error")+": "+providers.MaskOperand(s.loadErr), a.Width()))
	case len(s.models) == 0:
		lines = append(lines, cellwidth.Truncate(s.b.T("models.empty"), a.Width()))
	}
	lines = append(lines, s.list.Render(a.Width())...)
	lines = append(lines, "", s.list.FooterHint())
	return lines
}

// HandleKey delegates to the stepList and keeps the sticky model selection
// in sync while the cursor sits on a model row; when the selection moves
// the marker follows in place (syncMarker), so the list window never
// resets. Esc never arrives here: the App precedence handles it first
// (clears the message, then pops back to the providers screen).
//
// The reassert is gated by s.locked, the Space pin (see the type doc): while a
// pin holds, arrow travel over other model rows leaves the target alone. The
// stepList consumes Space before this block runs, so the pin is already set
// when the gate is read.
func (s *modelsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
	if s.locked {
		return
	}
	if c := s.list.Cursor(); c >= 0 && c < len(s.models) && s.sel != c {
		s.selectModel(c)
		s.syncMarker()
	}
}

// selectAt is the stepList Space callback: the explicit pin. Model rows occupy
// indices 0..len(models)-1 and the action rows (Add, Edit, Delete, Toggle,
// Tools, Back) sit below them, so the row index is the boundary: Space on an
// action row is a consumed no-op with no state change, Space on a model row
// pins it, locks the selection, moves the marker in place and confirms on the
// message line. Pinning again re-pins (the lock simply stays on). Space never
// activates: this callback never reaches activate, so a pin cannot open the
// model form, the approval gate, or the tools overlay, and it cannot shadow the
// overlay's own Space-to-toggle (that overlay is a modal, and its Key handles
// Space before its list ever sees the key). The pin also records the model ID it
// holds, the identity reload re-anchors by (F-01).
func (s *modelsScreen) selectAt(idx int) {
	if idx < 0 || idx >= len(s.models) {
		return
	}
	s.selectModel(idx)
	s.locked = true
	s.syncMarker()
	if s.app != nil {
		s.app.SetMessage(fillHubSlots(missingOr(s.b, "list.msg.pinned", listMsgPinned),
			map[string]string{"name": maskSlotOperand(s.models[idx].ID)}))
	}
}

// toolsOverlay is the M2 numbered multi-select over the known tool list.
// It is built on the shared stepList so digits and arrows both move the
// cursor; activation is deliberately not wired to pick, because toggling is
// a Space gesture and the overlay closes on Enter (propose the M4 approval)
// or Esc (cancel). Space reaches the overlay as KeySpace: while a modal is
// live the App routes every key to it in CONFIRM scope, so the NAV/EDIT
// dual meaning of Space does not apply here.
//
// Grants outside the known list (a hand-edited registry entry, a tool a newer
// build ships) are not row-addressable, but they are preserved:
// newToolsOverlay records them in extra and grantedList unions them back in,
// so saving the overlay never drops a grant the registry ships.
//
// The overlay also carries the checked-row set it OPENED with, so the caller
// can tell an untouched save from a toggled one structurally (untouched)
// instead of by comparing shapes: the granted set repeats nothing and reorders,
// while a raw registry list may repeat an unlisted grant, which no normalized
// comparison can match (G-2).
type toolsOverlay struct {
	title    string
	hint     string
	list     *stepList
	rows     []Row
	granted  map[string]bool
	opening  map[string]bool
	extra    []string
	onSave   func(granted []string, untouched bool)
	onCancel func()
	done     bool
}

// newToolsOverlay builds the overlay over tools, pre-checking the known
// tools in the model's current AllowedTools. The caller passes that list RAW
// (file order, repeats intact) so the checkboxes and the preserved extras
// reflect what is on disk, not a normalized projection of it. Repeats inside
// the raw list stay in extra (the unlisted loop appends every occurrence), so
// an untouched save can hand the raw slice back verbatim. Grants outside the
// known list are not togglable here, but they are kept and pass through every
// save (union semantics), so Core.ModelSetTools never sees a silent truncation.
func newToolsOverlay(title, hint string, tools []string, current []string, onSave func([]string, bool), onCancel func()) *toolsOverlay {
	ov := &toolsOverlay{title: title, hint: hint, granted: map[string]bool{}, onSave: onSave, onCancel: onCancel}
	known := map[string]bool{}
	for _, t := range tools {
		known[t] = true
	}
	for _, t := range current {
		if known[t] {
			ov.granted[t] = true
		} else {
			ov.extra = append(ov.extra, t)
		}
	}
	ov.opening = make(map[string]bool, len(ov.granted))
	for t, on := range ov.granted {
		ov.opening[t] = on
	}
	ov.rows = make([]Row, len(tools))
	for i, tool := range tools {
		ov.rows[i] = Row{ID: tool, Label: ov.toolLabel(tool)}
	}
	// Digit and Enter activation only move the cursor; Space is the toggle.
	ov.list = newStepList(func(idx int) { ov.list.SetCursor(idx) })
	ov.list.SetRows(ov.rows)
	return ov
}

// untouched reports whether the checked-row set is still the one the overlay
// opened with. Only Space moves it (the extras have no rows), so this is
// exactly "no known row was toggled", and a toggle followed by an un-toggle is
// untouched again — nothing effectively changed, so the raw bytes stay. The
// comparison is over the granted map, not over list shapes, so duplicates and
// order in the raw registry entry cannot defeat it.
func (ov *toolsOverlay) untouched() bool {
	if len(ov.granted) != len(ov.opening) {
		return false
	}
	for tool, on := range ov.granted {
		if ov.opening[tool] != on {
			return false
		}
	}
	return true
}

// toolLabel renders the row with a checkbox marker the Space toggle flips.
func (ov *toolsOverlay) toolLabel(tool string) string {
	if ov.granted[tool] {
		return "[x] " + tool
	}
	return "[ ] " + tool
}

// Title returns the overlay title.
func (ov *toolsOverlay) Title() string { return ov.title }

// Body renders the numbered, cursor-marked tool rows.
func (ov *toolsOverlay) Body(w int) []string { return ov.list.Render(w) }

// Footer names the three gestures.
func (ov *toolsOverlay) Footer() string { return ov.hint }

// Key routes one key. Esc cancels, Enter proposes the granted set (known
// tools in known-tool order plus the preserved extras) to the M4 approval
// opened by the onSave callback, together with untouched() so the caller can
// commit the raw registry list when no row moved. Space toggles the cursor row
// and keeps the overlay open. Unchecking the last known grant is refused while
// extras are empty: an empty AllowedTools means allow-every-tool at runtime, so
// the gesture would do the opposite of what the user meant. Arrows and digits
// move through the shared list.
func (ov *toolsOverlay) Key(k Key, a *App) bool {
	if ov.done {
		return false
	}
	switch k {
	case KeyEsc:
		ov.done = true
		if ov.onCancel != nil {
			ov.onCancel()
		}
		return false
	case KeyEnter:
		ov.done = true
		if ov.onSave != nil {
			ov.onSave(ov.grantedList(), ov.untouched())
		}
		return false
	case KeySpace:
		if ov.list.Len() > 0 {
			i := ov.list.Cursor()
			tool := ov.rows[i].ID
			if ov.granted[tool] && len(ov.granted) == 1 && len(ov.extra) == 0 {
				if a != nil {
					a.SetMessage(missingModelsToolsMinOne)
				}
				return true
			}
			ov.granted[tool] = !ov.granted[tool]
			ov.rows[i].Label = ov.toolLabel(tool)
		}
		return true
	default:
		ov.list.HandleKey(k)
		return true
	}
}

// grantedList returns the granted tools in known-tool list order followed
// by the preserved extras in their original order: the canonical SELECTION
// view, with no unlisted grant dropped. It is the write target only when a row
// was toggled; openTools commits the record's raw slice when untouched()
// reports that nothing moved.
func (ov *toolsOverlay) grantedList() []string {
	out := []string{}
	for _, r := range ov.rows {
		if ov.granted[r.ID] {
			out = append(out, r.ID)
		}
	}
	return append(out, ov.extra...)
}
