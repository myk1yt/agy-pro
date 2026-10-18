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
)

// knownToolList is the closed tool universe the setup wizard documents
// (read,search = read_file,list_dir,grep,glob; write = write_file;
// exec = run_command). The tools overlay multi-selects over exactly this
// list; free-form tool names are the form unit's job.
var knownToolList = []string{"read_file", "list_dir", "grep", "glob", "write_file", "run_command"}

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
	loadErr      string
	sig          string
	app          *App
}

// NewModelsScreen builds the models list for one provider. openForm is the
// form screen factory: "add" with an empty modelID, or "edit" with the model
// ID; tests inject a spy. The UI language resolves from the persisted state
// (AGY_PRO_HOME state.json), defaulting to English.
func NewModelsScreen(core *settings.Core, providerName string, openForm func(mode string, modelID string, a *App)) Screen {
	s := &modelsScreen{core: core, providerName: providerName, openForm: openForm}
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
func (s *modelsScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// Mode is ModeNav: digits and arrows both navigate.
func (s *modelsScreen) Mode() Mode { return ModeNav }

// Title resolves through the catalog; the provider operand is masked
// (key-shaped provider names can never render raw).
func (s *modelsScreen) Title() string {
	return s.b.Tf("models.screen.title_for", map[string]string{"provider": providers.MaskOperand(s.providerName)})
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
// kept and clamped into the new row range. The on-disk signature is recorded
// so reloadIfChanged can skip the rebuild while the file is unchanged.
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
				map[string]string{"provider": providers.MaskOperand(s.providerName)})
		} else {
			s.models = append([]providers.Model(nil), p.Models...)
			s.sig = modelsSig(reg, s.providerName)
		}
	}
	if s.sel > len(s.models)-1 {
		s.sel = len(s.models) - 1
	}
	if s.sel < 0 {
		s.sel = 0
	}
	cursor := s.list.Cursor()
	s.rows = s.buildRows()
	s.list.SetRows(s.rows)
	s.list.SetCursor(cursor)
}

// modelsSig builds a signature over the provider's on-disk model records:
// every field the rows render (id, family, context, maxOut, enabled, tool
// grants, SupportsTools). A change in any of them moves the signature.
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
// enabled state, and the tool grant count.
func (s *modelsScreen) buildRows() []Row {
	rows := make([]Row, 0, len(s.models)+6)
	for _, m := range s.models {
		rows = append(rows, Row{ID: modelRowPrefix + m.ID, Label: providers.MaskOperand(m.ID), Note: s.modelNote(m)})
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
// summary, enabled state, and the granted-tool count.
func (s *modelsScreen) modelNote(m providers.Model) string {
	b := s.b
	parts := []string{}
	if m.Family != "" {
		// The family slot uses the registry-slot masker (the same
		// MaskRegistryValue MaskFamilyName applies to an explicit family),
		// never the raw field.
		parts = append(parts, providers.MaskRegistryValue(m.Family))
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
	return strings.Join(parts, " · ")
}

// activate is the stepList onPick callback, shared by digit selection and
// Enter. Model rows open the form in edit mode; action rows dispatch.
func (s *modelsScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.rows) {
		return
	}
	id := s.rows[idx].ID
	if strings.HasPrefix(id, modelRowPrefix) {
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
		s.withCursorModel(func(m providers.Model) {
			if s.openForm != nil {
				s.openForm("edit", m.ID, s.app)
			}
		})
	case modelsActionDeleteID:
		s.askDelete()
	case modelsActionToggleID:
		s.toggleEnabled()
	case modelsActionToolsID:
		s.openTools()
	case modelsActionBackID:
		s.app.Pop()
	}
}

// withCursorModel runs fn over the selected model. The selection is sticky:
// it follows the cursor while the cursor sits on a model row and survives
// the move onto an action row (digit activation jumps the cursor to the
// action row, so the action verbs cannot read the cursor position directly).
// With no model selected the user gets a message instead of a silent no-op.
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
// masked), the model count N -> N-1, and the last-model warning when the
// provider would keep an empty list. y runs Core.ModelDelete and reloads;
// n or Esc cancels with an explicit nothing-written message.
func (s *modelsScreen) askDelete() {
	s.withCursorModel(func(m providers.Model) {
		a := s.app
		n := len(s.models)
		maskedID := providers.MaskOperand(m.ID)
		maskedProvider := providers.MaskOperand(s.providerName)
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
			a.SetMessage(fillHubSlots(missingOr(s.b, "models.msg.deleted", missingModelsDeleted), map[string]string{
				"id":       maskedID,
				"provider": maskedProvider,
			}))
		}))
	})
}

// toggleEnabled opens the M4 Approval modal with the exact mutation summary:
// the masked provider + model id sentence and the enabled field before/after.
// y runs Core.ModelSetEnabled and reloads; n or Esc writes nothing. The
// summary lines carry only MaskOperand operands, so a key-shaped model id
// can never render raw in the approval frame.
func (s *modelsScreen) toggleEnabled() {
	s.withCursorModel(func(m providers.Model) {
		a := s.app
		next := !m.IsEnabled()
		maskedID := providers.MaskOperand(m.ID)
		maskedProvider := providers.MaskOperand(s.providerName)
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
			a.SetMessage(fillHubSlots(missingOr(s.b, "models.msg.enabled_set", missingModelsEnabledSet), map[string]string{
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
func (s *modelsScreen) openTools() {
	s.withCursorModel(func(m providers.Model) {
		a := s.app
		maskedID := providers.MaskOperand(m.ID)
		maskedProvider := providers.MaskOperand(s.providerName)
		ov := newToolsOverlay(
			missingOr(s.b, "models.tools.title", missingModelsToolsTitle),
			missingOr(s.b, "models.tools.hint", missingModelsToolsHint),
			knownToolList,
			m.Capabilities.AllowedTools,
			func(granted []string) {
				if len(granted) == 0 {
					a.SetMessage(missingOr(s.b, "models.tools.min_one", missingModelsToolsMinOne))
					return
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
				after := maskJoined(granted, ", ")
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
					if err := s.core.ModelSetTools(s.providerName, m.ID, granted); err != nil {
						a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
						return
					}
					s.reload()
					a.SetMessage(fillHubSlots(missingOr(s.b, "models.msg.tools_saved", missingModelsToolsSaved), map[string]string{
						"id": maskedID,
						"n":  strconv.Itoa(len(granted)),
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
	s.reloadIfChanged()
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
// in sync while the cursor sits on a model row. Esc never arrives here: the
// App precedence handles it first (clears the message, then pops back to
// the providers screen).
func (s *modelsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
	if c := s.list.Cursor(); c >= 0 && c < len(s.models) {
		s.sel = c
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
// Grants outside the known list are not row-addressable, but they are
// preserved: newToolsOverlay records them in extra and grantedList unions
// them back in, so saving the overlay never drops a grant the registry
// ships (replace_in_file, read_url_content, search_web, custom names).
type toolsOverlay struct {
	title    string
	hint     string
	list     *stepList
	rows     []Row
	granted  map[string]bool
	extra    []string
	onSave   func(granted []string)
	onCancel func()
	done     bool
}

// newToolsOverlay builds the overlay over tools, pre-checking the known
// tools in the model's current AllowedTools. Grants outside the known list
// are not togglable here, but they are kept in extra (original order) and
// pass through every save (union semantics), so Core.ModelSetTools never
// sees a silent truncation.
func newToolsOverlay(title, hint string, tools []string, current []string, onSave func([]string), onCancel func()) *toolsOverlay {
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
	ov.rows = make([]Row, len(tools))
	for i, tool := range tools {
		ov.rows[i] = Row{ID: tool, Label: ov.toolLabel(tool)}
	}
	// Digit and Enter activation only move the cursor; Space is the toggle.
	ov.list = newStepList(func(idx int) { ov.list.SetCursor(idx) })
	ov.list.SetRows(ov.rows)
	return ov
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
// opened by the onSave callback, Space toggles the cursor row and keeps
// the overlay open. Unchecking the last known grant is refused while extras
// are empty: an empty AllowedTools means allow-every-tool at runtime, so
// the gesture would do the opposite of what the user meant. Arrows and
// digits move through the shared list.
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
			ov.onSave(ov.grantedList())
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
// by the preserved extras in their original order, so the persisted
// AllowedTools is deterministic and no unlisted grant is ever dropped.
func (ov *toolsOverlay) grantedList() []string {
	out := []string{}
	for _, r := range ov.rows {
		if ov.granted[r.ID] {
			out = append(out, r.ID)
		}
	}
	return append(out, ov.extra...)
}
