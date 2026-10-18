package tui

import (
	"errors"
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// Model form modes (the openForm factory contract of the models screen).
const (
	modelFormModeAdd  = "add"
	modelFormModeEdit = "edit"
)

// Form field indices: the grid order. The capabilities row is display-only;
// Enter on it opens the tool-picker overlay. Below the grid sits the Save
// row, which is screen-level navigation state (onSaveRow), not a Form field.
const (
	fModelID = iota
	fFamily
	fTags
	fEnabled
	fContext
	fMaxOut
	fCaps
	fCount
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved). Keys already present
// in en.json (the models.form.* labels, models.validation.id_*/context/max_out,
// models.delete.count via the models screen const) resolve directly.
const (
	// catalog-resolved, fallback kept: models.form.hint
	missingModelFormHint = "Enter advances · Enter on Capabilities opens the tool picker · Esc asks to discard unsaved edits"
	// catalog-resolved, fallback kept: models.form.caps_none
	missingModelFormCapsNone = "(none)"
	// catalog-resolved, fallback kept: models.validation.enabled
	missingModelFormEnabledInvalid = "Enabled must be y or n."
	// catalog-resolved, fallback kept: models.validation.secret
	missingModelFormSecret = "A field value looks like secret/key material and cannot be saved (value hidden). Model fields store names, never keys."
	// catalog-resolved, fallback kept: models.form.approval.add
	missingModelFormApprovalAdd = "Adds model {{id}} to provider {{provider}}. Key values are untouched."
	// catalog-resolved, fallback kept: models.form.approval.update
	missingModelFormApprovalUpdate = "Updates model {{id}} in provider {{provider}}. Key values are untouched."
	// catalog-resolved, fallback kept: models.form.diff_added
	missingModelFormDiffAdded = "+ {{field}}: {{value}}"
	// catalog-resolved, fallback kept: models.form.diff_changed
	missingModelFormDiffChanged = "  {{field}}: {{before}} -> {{after}}"
	// catalog-resolved, fallback kept: models.form.diff_none
	missingModelFormDiffNone = "(no field changes)"
	// catalog-resolved, fallback kept: models.form.saved_add
	missingModelFormSavedAdd = "Model {{id}} added to {{provider}}."
	// catalog-resolved, fallback kept: models.form.saved_edit
	missingModelFormSavedEdit = "Model {{id}} updated in {{provider}}."
	// catalog-resolved, fallback kept: models.form.discard_title
	missingModelFormDiscardTitle = "Discard unsaved edits?"
	// catalog-resolved, fallback kept: models.form.discard_body
	missingModelFormDiscardBody = "The edits on this form are dropped and nothing is written to providers.json."
	// catalog-resolved, fallback kept: models.form.model_missing
	missingModelFormModelMissing = "Model {{id}} not found in provider {{provider}}."
)

// secretValuePrefixes is the known credential-head list, aligned with the
// operandLooksSecret classifier in internal/providers/keyaudit.go: a value
// starting with any of these reads as key material in any slot, at any
// length.
var secretValuePrefixes = []string{
	"sk-", "key-", "gsk_", "nvapi-", "live_", "secret_",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
	"xox", "glpat-", "npm_", "dckr_pat-", "dckr_pat_", "bearer ", "eyj",
}

// looksSecretValue is the conservative local guard for free-text model
// fields. providers.LooksLikeAPIKey cannot be used here verbatim: it flags
// ANY operand containing "-" or "." (correct for env-var names, fatal for
// ordinary model ids such as "acme-fast" and for hyphenated tags). The guard
// mirrors the credential-shape precedent of providers.operandLooksSecret
// (keyaudit.go): known credential prefixes at any length, plus an opaque
// token of 16+ runes of the base64 alphabet carrying BOTH letters and digits
// with no other separator.
// Model ids and tags carry separators (anthropic/claude-3.5-sonnet,
// meta-llama/Llama-3.3-70B-Instruct-Turbo), so they pass; a 20-rune
// AKIA-class key (AKIAIOSFODNN7EXAMPLE) is one uninterrupted mixed run and
// is rejected. The error path never echoes the value.
func looksSecretValue(val string) bool {
	v := strings.TrimSpace(val)
	if v == "" {
		return false
	}
	lower := strings.ToLower(v)
	for _, p := range secretValuePrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return credentialTokenShaped(v)
}

// credentialTokenShaped reports whether v is one uninterrupted run of the
// base64 alphabet (ASCII letters, digits, and the + / = pad runes), at least
// 16 runes long, with both letter and digit classes present: the shape of an
// opaque credential (AWS secret access keys, which typically carry "/" or
// "+", API tokens). Any other separator rune (- . _ : whitespace and
// friends) or a single-class run (a long word, an all-digit serial) fails
// the shape and stays readable.
//
// Round-6 F-01: the + / = runes were dropped in the round-5 narrowing and a
// 16+ rune base64 credential carrying them passed this guard; they are
// admitted again, mirroring providers.contiguousSecretRun (the display
// classifier) rune for rune. The CLI twin modelSpecOpaqueToken in
// cmd/agy/pro_settings.go is a rune-for-rune copy of this function and must
// stay identical.
func credentialTokenShaped(v string) bool {
	n := 0
	letter, digit := false, false
	for _, r := range v {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
		case r >= '0' && r <= '9':
			digit = true
		case r == '+', r == '/', r == '=':
		default:
			return false
		}
		n++
	}
	return n >= 16 && letter && digit
}

// modelFormScreen is the model add/edit form (architect-report section 5,
// flow S3 -> S3b). The field grid is the shared Form; below it sits the Save
// row. Enter on Save does NOT write: it opens the M4 Approval modal with the
// exact mutation summary, and only y reaches the Core verbs. apiKeyEnv is
// never part of this form: model adds never touch key material, and the
// provider's existing env NAME stays.
//
// Esc handling: the App dispatcher consumes Esc before any screen sees it
// (clears the message, then pops). A dirty form must instead gate Esc behind
// a discard-confirm, so while the form is dirty the screen arms a
// transparent guard modal (formGuard): the App routes every key to it in
// CONFIRM scope, it forwards everything except Esc to the form, and Esc on a
// dirty form opens the discard-confirm. That is the only way to run the
// confirm BEFORE the App Esc precedence without touching tui.go. A clean Esc
// never arms the guard and pops through the plain App precedence.
type modelFormScreen struct {
	core         *settings.Core
	providerName string
	mode         string
	modelID      string
	lang         string
	b            *i18n.Bundle
	form         *Form
	orig         providers.Model
	origFound    bool
	existing     map[string]bool
	count        int
	loadErr      string
	baseVals     []string
	tools        []string
	baseTools    []string
	onSaveRow    bool
	errMsg       string
	app          *App
	guard        *formGuard
}

// NewModelFormScreen builds the add/edit form for one provider. mode is
// "add" (empty fields, modelID ignored) or "edit" (fields pre-filled from
// the named model). The UI language resolves from the persisted state
// (AGY_PRO_HOME state.json), defaulting to English.
func NewModelFormScreen(core *settings.Core, providerName, modeName, modelID string) Screen {
	s := &modelFormScreen{core: core, providerName: providerName, mode: modeName, modelID: modelID}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	s.setLang(i18n.Normalize(lang))
	s.existing = map[string]bool{}
	if reg, err := providers.Load(); err != nil {
		s.loadErr = err.Error()
	} else if p := findProviderCI(reg, providerName); p == nil {
		s.loadErr = fillHubSlots(missingOr(s.b, "models.provider_missing", missingModelsProviderMissing),
			map[string]string{"provider": providers.MaskOperand(providerName)})
	} else {
		s.count = len(p.Models)
		for _, m := range p.Models {
			s.existing[m.ID] = true
		}
		if modeName == modelFormModeEdit {
			for j := range p.Models {
				if p.Models[j].ID == modelID {
					s.orig = p.Models[j]
					s.origFound = true
				}
			}
			if !s.origFound {
				s.loadErr = fillHubSlots(missingOr(s.b, "models.form.model_missing", missingModelFormModelMissing),
					map[string]string{"id": providers.MaskOperand(modelID), "provider": providers.MaskOperand(providerName)})
			}
		}
	}
	if s.origFound {
		s.tools = normalizeTools(s.orig.Capabilities.AllowedTools)
	}
	s.baseTools = append([]string(nil), s.tools...)
	vals := make([]string, fCount)
	vals[fEnabled] = "y"
	if s.origFound {
		vals[fModelID] = s.orig.ID
		vals[fFamily] = s.orig.Family
		vals[fTags] = strings.Join(s.orig.Tags, ", ")
		if !s.orig.IsEnabled() {
			vals[fEnabled] = "n"
		}
		if s.orig.ContextLength > 0 {
			vals[fContext] = strconv.Itoa(s.orig.ContextLength)
		}
		if s.orig.MaxOutputTokens > 0 {
			vals[fMaxOut] = strconv.Itoa(s.orig.MaxOutputTokens)
		}
	}
	vals[fCaps] = s.capsDisplay()
	s.form = NewForm(s.buildFields(vals))
	s.baseVals = s.form.Snapshot()
	return s
}

// setLang loads the bundle for lang once per language.
func (s *modelFormScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// Mode is ModeEdit on a text field and ModeNav on the Save row.
func (s *modelFormScreen) Mode() Mode {
	if s.onSaveRow {
		return ModeNav
	}
	return ModeEdit
}

// Title resolves through the catalog (Add Model / Edit Model).
func (s *modelFormScreen) Title() string {
	if s.mode == modelFormModeEdit {
		return s.b.T("models.form.title.edit")
	}
	return s.b.T("models.form.title.add")
}

// normalizeTools orders a tool-grant list deterministically without dropping
// anything: grants present in knownToolList keep that list's order first,
// grants outside the list (hand-edited registry entries such as
// replace_in_file or read_url_content) pass through untouched in their
// original order. The picker overlay can only toggle known rows, so unlisted
// grants must survive every form path (edit seed, overlay persist, approval
// diff, commit) or a save would silently strip them from the registry.
func normalizeTools(tools []string) []string {
	known := map[string]bool{}
	for _, k := range knownToolList {
		known[k] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, k := range knownToolList {
		for _, t := range tools {
			if t == k && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	for _, t := range tools {
		if !known[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// unlistedTools returns the grants outside knownToolList, in their original
// order. The picker overlay has no rows for them, so they cannot be toggled;
// they only pass through.
func unlistedTools(tools []string) []string {
	known := map[string]bool{}
	for _, k := range knownToolList {
		known[k] = true
	}
	out := []string{}
	for _, t := range tools {
		if !known[t] {
			out = append(out, t)
		}
	}
	return out
}

// maskJoined masks each item individually before joining, so one
// credential-shaped entry cannot hide behind the join separator of its
// neighbors (MaskOperand on the already-joined string would see the
// separators and stay readable).
func maskJoined(items []string, sep string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = providers.MaskOperand(it)
	}
	return strings.Join(parts, sep)
}

// capsDisplay renders the capabilities field value.
func (s *modelFormScreen) capsDisplay() string {
	if len(s.tools) == 0 {
		return missingOr(s.b, "models.form.caps_none", missingModelFormCapsNone)
	}
	return maskJoined(s.tools, ", ")
}

// dirty reports whether the form differs from its construction state. It is
// computed from the values (not the Form latch), so it survives the form
// rebuilds the tool picker triggers.
func (s *modelFormScreen) dirty() bool {
	cur := s.form.Snapshot()
	if len(cur) != len(s.baseVals) {
		return true
	}
	for i := range cur {
		if cur[i] != s.baseVals[i] {
			return true
		}
	}
	return !equalKeyList(s.tools, s.baseTools)
}

func equalKeyList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildFields composes the grid over the given values. Validators are
// closures over the screen (they need the catalog and the duplicate set).
func (s *modelFormScreen) buildFields(vals []string) []Field {
	return []Field{
		{Label: s.b.T("models.form.id"), Value: vals[fModelID], Validate: s.validateID},
		{Label: s.b.T("models.form.family"), Value: vals[fFamily], Validate: s.validateFamily},
		{Label: s.b.T("models.form.tags"), Value: vals[fTags], Validate: s.validateTags},
		{Label: s.b.T("models.form.enabled"), Value: vals[fEnabled], Validate: s.validateEnabled},
		{Label: s.b.T("models.form.context"), Value: vals[fContext], Validate: s.validateContext},
		{Label: s.b.T("models.form.max_out"), Value: vals[fMaxOut], Validate: s.validateMaxOut},
		{Label: s.b.T("models.form.capabilities"), Value: vals[fCaps]},
	}
}

func (s *modelFormScreen) idTaken(id string) bool {
	if s.mode == modelFormModeEdit && id == s.modelID {
		return false
	}
	return s.existing[id]
}

func (s *modelFormScreen) dupMessage(id string) string {
	return s.b.Tf("models.validation.id_exists", map[string]string{"id": providers.MaskOperand(id)})
}

func (s *modelFormScreen) validateID(v string) error {
	id := strings.TrimSpace(v)
	if id == "" {
		return errors.New(s.b.T("models.validation.id_empty"))
	}
	if looksSecretValue(id) {
		return errors.New(missingOr(s.b, "models.validation.secret", missingModelFormSecret))
	}
	if s.idTaken(id) {
		return errors.New(s.dupMessage(id))
	}
	return nil
}

func (s *modelFormScreen) validateFamily(v string) error {
	if looksSecretValue(v) {
		return errors.New(missingOr(s.b, "models.validation.secret", missingModelFormSecret))
	}
	return nil
}

func (s *modelFormScreen) validateTags(v string) error {
	for _, part := range strings.Split(v, ",") {
		if looksSecretValue(part) {
			return errors.New(missingOr(s.b, "models.validation.secret", missingModelFormSecret))
		}
	}
	return nil
}

func (s *modelFormScreen) validateEnabled(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "y", "n", "yes", "no":
		return nil
	}
	return errors.New(missingOr(s.b, "models.validation.enabled", missingModelFormEnabledInvalid))
}

func (s *modelFormScreen) validateContext(v string) error {
	return validatePositiveInt(v, s.b.T("models.validation.context"))
}

func (s *modelFormScreen) validateMaxOut(v string) error {
	return validatePositiveInt(v, s.b.T("models.validation.max_out"))
}

// validatePositiveInt accepts empty (unset) or a positive integer.
func validatePositiveInt(v, msg string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return errors.New(msg)
	}
	return nil
}

// messageLine composes the form message: a save-validation error first, then
// the focused field's validation error, then the live checks on the focused
// free-text field (duplicate id, secret-looking value): the user sees the
// problem while typing, without leaving the form.
func (s *modelFormScreen) messageLine() string {
	if s.errMsg != "" {
		return s.errMsg
	}
	if msg := s.form.Message(); msg != "" {
		return msg
	}
	if s.onSaveRow {
		return ""
	}
	secret := func(vals ...string) string {
		for _, v := range vals {
			if looksSecretValue(v) {
				return missingOr(s.b, "models.validation.secret", missingModelFormSecret)
			}
		}
		return ""
	}
	switch s.form.Cursor() {
	case fModelID:
		id := strings.TrimSpace(s.form.Value(fModelID))
		if id != "" {
			if msg := secret(id); msg != "" {
				return msg
			}
			if s.idTaken(id) {
				return s.dupMessage(id)
			}
		}
	case fFamily:
		return secret(s.form.Value(fFamily))
	case fTags:
		return secret(strings.Split(s.form.Value(fTags), ",")...)
	}
	return ""
}

// Draw renders the field grid, the Save row, the hint, and the message
// line. All cell math goes through cellwidth. The hint is rendered by the
// guard modal while it is armed, so it is skipped here in that case.
func (s *modelFormScreen) Draw(a *App) []string {
	var lines []string
	if s.loadErr != "" {
		lines = append(lines, cellwidth.Truncate(s.b.T("common.error")+": "+providers.MaskOperand(s.loadErr), a.Width()))
	}
	lines = append(lines, s.form.Render(a.Width())...)
	saveRow := "  " + s.b.T("common.save")
	if s.onSaveRow {
		saveRow = "▶ " + s.b.T("common.save")
	}
	lines = append(lines, "", cellwidth.Truncate(saveRow, a.Width()))
	if s.app == nil || a.Modal() != s.guard {
		lines = append(lines, "", cellwidth.Truncate(missingOr(s.b, "models.form.hint", missingModelFormHint), a.Width()))
	}
	if msg := s.messageLine(); msg != "" {
		lines = append(lines, "", cellwidth.Truncate(msg, a.Width()))
	}
	return lines
}

// HandleKey runs one key with no modal live: dispatch it, then arm the
// dirty guard. Esc never arrives here (App precedence).
func (s *modelFormScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.dispatch(k)
	if s.dirty() && a.Modal() == nil {
		s.rearmGuard(a)
	}
}

// dispatch routes one key between the Save row and the field grid. Enter on
// the capabilities row opens the tool picker instead of advancing.
func (s *modelFormScreen) dispatch(k Key) {
	s.errMsg = ""
	if s.onSaveRow {
		switch k {
		case KeyUp, KeyLeft:
			s.onSaveRow = false
			s.form.SetCursor(fCount - 1)
		case KeyEnter:
			s.trySave()
		}
		return
	}
	switch k {
	case KeyTab, KeyDown:
		if s.form.Cursor() == fCount-1 {
			s.onSaveRow = true
			return
		}
	case KeyEnter:
		if s.form.Cursor() == fCaps {
			s.openTools()
			return
		}
	}
	s.form.HandleKey(k)
}

// rearmGuard (re)opens the dirty guard. Callers run inside a modal key
// callback, so the App stale-modal clear never removes a freshly armed one.
func (s *modelFormScreen) rearmGuard(a *App) {
	if !s.dirty() {
		return
	}
	if s.guard == nil {
		s.guard = &formGuard{s: s}
	}
	a.ShowModal(s.guard)
}

// askDiscard opens the discard-confirm over a dirty form. y restores the
// construction values (Snapshot/Restore) and pops back to the caller; n/Esc
// returns to the form with field state preserved and the guard re-armed.
func (s *modelFormScreen) askDiscard() {
	a := s.app
	if a == nil {
		return
	}
	a.ShowModal(NewConfirm(
		missingOr(s.b, "models.form.discard_title", missingModelFormDiscardTitle),
		[]string{missingOr(s.b, "models.form.discard_body", missingModelFormDiscardBody)},
		func(ok bool) {
			if !ok {
				s.rearmGuard(a)
				return
			}
			s.form.Restore(s.baseVals)
			s.tools = append([]string(nil), s.baseTools...)
			a.Pop()
			a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
		}))
}

// openTools opens the shared M2 multi-select over the known tool list,
// pre-checking the current selection. Enter persists into the form (nothing
// is written to disk here); Esc cancels. The overlay can only toggle known
// rows, so the result is merged with the current selection: unlisted grants
// (hand-edited registry entries) pass through untouched. An empty merged
// selection is refused, because the engine reads AllowedTools empty (with
// SupportsTools set) as allow-EVERY-tool (providers.ToolPermitted).
func (s *modelFormScreen) openTools() {
	a := s.app
	if a == nil {
		return
	}
	ov := newToolsOverlay(
		missingOr(s.b, "models.tools.title", missingModelsToolsTitle),
		missingOr(s.b, "models.tools.hint", missingModelsToolsHint),
		knownToolList,
		s.tools,
		func(granted []string) {
			// Known rows: the overlay's checked set is authoritative (an
			// unchecked known grant stays removed). Unlisted grants have no
			// rows and pass through from the current selection.
			merged := normalizeTools(append(granted, unlistedTools(s.tools)...))
			if len(merged) == 0 {
				s.errMsg = missingOr(s.b, "models.tools.min_one", missingModelsToolsMinOne)
				s.syncCapsField()
				s.rearmGuard(a)
				return
			}
			s.tools = merged
			s.syncCapsField()
			s.rearmGuard(a)
		},
		func() {
			a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
			s.rearmGuard(a)
		},
	)
	a.ShowModal(ov)
}

// syncCapsField rebuilds the form with the updated capabilities display,
// keeping the cursor. Values and validators are preserved; dirty tracking is
// state-based, so the rebuild loses nothing.
func (s *modelFormScreen) syncCapsField() {
	vals := s.form.Snapshot()
	vals[fCaps] = s.capsDisplay()
	cursor := s.form.Cursor()
	s.form = NewForm(s.buildFields(vals))
	s.form.SetCursor(cursor)
}

// trySave validates every field, then opens the M4 Approval modal with the
// exact mutation summary: the masked provider + model id sentence, the model
// count before/after, and the field diff. Enter here does NOT write.
func (s *modelFormScreen) trySave() {
	a := s.app
	if a == nil {
		return
	}
	for i := 0; i < fCount; i++ {
		f := s.form.Field(i)
		if f.Validate == nil {
			continue
		}
		if err := f.Validate(s.form.Value(i)); err != nil {
			s.errMsg = err.Error()
			return
		}
	}
	m, tools := s.buildModel()
	var summary []string
	if s.mode == modelFormModeAdd {
		summary = append(summary,
			fillHubSlots(missingOr(s.b, "models.form.approval.add", missingModelFormApprovalAdd), map[string]string{
				"id":       providers.MaskOperand(m.ID),
				"provider": providers.MaskOperand(s.providerName),
			}),
			fillHubSlots(missingOr(s.b, "models.delete.count", missingModelsDeleteCount), map[string]string{
				"before": strconv.Itoa(s.count),
				"after":  strconv.Itoa(s.count + 1),
			}),
		)
		summary = append(summary, s.addedDiffLines(m, tools)...)
	} else {
		summary = append(summary,
			fillHubSlots(missingOr(s.b, "models.form.approval.update", missingModelFormApprovalUpdate), map[string]string{
				"id":       providers.MaskOperand(m.ID),
				"provider": providers.MaskOperand(s.providerName),
			}),
			fillHubSlots(missingOr(s.b, "models.delete.count", missingModelsDeleteCount), map[string]string{
				"before": strconv.Itoa(s.count),
				"after":  strconv.Itoa(s.count),
			}),
		)
		summary = append(summary, s.changedDiffLines(m, tools)...)
	}
	a.ShowModal(NewApproval(s.b.T("modal.approval.title"), summary, func(ok bool) {
		if !ok {
			// A declined approval leaves the edited values untouched, so
			// the form stays dirty and the Esc gate (formGuard) stays armed.
			// The approval swallows every key except y/n/Esc, so the live
			// values are unchanged since the modal opened; restoring a
			// snapshot here would only clear the Form dirty latch and let a
			// follow-up Esc pop silently.
			a.SetMessage(missingOr(s.b, "models.msg.cancelled", missingModelsCancelled))
			s.rearmGuard(a)
			return
		}
		s.commit(a, m, tools)
	}))
}

// buildModel composes the model record from the field values. Enabled "y"
// stores nil (default) unless the original was an explicit true; "n" stores
// an explicit false. Other capability flags (vision, thinking, ...) are
// preserved from the original in edit mode.
func (s *modelFormScreen) buildModel() (providers.Model, []string) {
	enabled := parseEnabled(s.form.Value(fEnabled))
	disabled := false
	var enabledPtr *bool
	if !enabled {
		enabledPtr = &disabled
	} else if s.origFound && s.orig.Enabled != nil && *s.orig.Enabled {
		tr := true
		enabledPtr = &tr
	}
	caps := providers.Capabilities{SupportsTools: len(s.tools) > 0}
	if s.origFound {
		caps = s.orig.Capabilities
		caps.SupportsTools = caps.SupportsTools || len(s.tools) > 0
		caps.AllowedTools = nil
	}
	m := providers.Model{
		ID:              strings.TrimSpace(s.form.Value(fModelID)),
		Family:          strings.TrimSpace(s.form.Value(fFamily)),
		Tags:            splitTags(s.form.Value(fTags)),
		Enabled:         enabledPtr,
		ContextLength:   parsePositiveInt(s.form.Value(fContext)),
		MaxOutputTokens: parsePositiveInt(s.form.Value(fMaxOut)),
		Capabilities:    caps,
	}
	return m, append([]string(nil), s.tools...)
}

func parseEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "n", "no":
		return false
	}
	return true
}

func parsePositiveInt(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func splitTags(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// addedDiffLines renders one "+ field: value" line per set field (add mode).
// Every operand passes through MaskOperand.
func (s *modelFormScreen) addedDiffLines(m providers.Model, tools []string) []string {
	lines := []string{}
	add := func(field, value string) {
		lines = append(lines, fillHubSlots(missingOr(s.b, "models.form.diff_added", missingModelFormDiffAdded), map[string]string{
			"field": field,
			"value": providers.MaskOperand(value),
		}))
	}
	add("id", m.ID)
	if m.Family != "" {
		add("family", m.Family)
	}
	if len(m.Tags) > 0 {
		add("tags", maskJoined(m.Tags, ", "))
	}
	if m.Enabled != nil && !*m.Enabled {
		add("enabled", "false")
	}
	if m.ContextLength > 0 {
		add("contextLength", strconv.Itoa(m.ContextLength))
	}
	if m.MaxOutputTokens > 0 {
		add("maxOutputTokens", strconv.Itoa(m.MaxOutputTokens))
	}
	if len(tools) > 0 {
		add("tools", maskJoined(tools, ", "))
	}
	return lines
}

// changedDiffLines renders one "  field: before -> after" line per changed
// field (edit mode), or the no-changes marker.
func (s *modelFormScreen) changedDiffLines(m providers.Model, tools []string) []string {
	lines := []string{}
	chg := func(field, before, after string) {
		if before == after {
			return
		}
		lines = append(lines, fillHubSlots(missingOr(s.b, "models.form.diff_changed", missingModelFormDiffChanged), map[string]string{
			"field":  field,
			"before": providers.MaskOperand(before),
			"after":  providers.MaskOperand(after),
		}))
	}
	chg("id", s.orig.ID, m.ID)
	chg("family", s.orig.Family, m.Family)
	chg("tags", maskJoined(s.orig.Tags, ", "), maskJoined(m.Tags, ", "))
	chg("enabled", strconv.FormatBool(s.orig.IsEnabled()), strconv.FormatBool(m.IsEnabled()))
	chg("contextLength", strconv.Itoa(s.orig.ContextLength), strconv.Itoa(m.ContextLength))
	chg("maxOutputTokens", strconv.Itoa(s.orig.MaxOutputTokens), strconv.Itoa(m.MaxOutputTokens))
	chg("tools", maskJoined(normalizeTools(s.orig.Capabilities.AllowedTools), ", "), maskJoined(tools, ", "))
	if len(lines) == 0 {
		lines = append(lines, missingOr(s.b, "models.form.diff_none", missingModelFormDiffNone))
	}
	return lines
}

// commit runs the approved write: Core.ModelAdd (add) or Core.ModelUpdate +
// Core.ModelSetTools (edit), then reloads and pops back to the caller
// screen with the success message. This is the ONLY write path out of the
// form.
func (s *modelFormScreen) commit(a *App, m providers.Model, tools []string) {
	var err error
	if s.mode == modelFormModeAdd {
		mm := m
		mm.Capabilities.AllowedTools = append([]string(nil), tools...)
		err = s.core.ModelAdd(s.providerName, mm)
	} else {
		mm := m
		mm.Capabilities.AllowedTools = append([]string(nil), tools...)
		if err = s.core.ModelUpdate(s.providerName, s.modelID, mm); err == nil {
			err = s.core.ModelSetTools(s.providerName, mm.ID, tools)
		}
	}
	if err != nil {
		a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
		s.rearmGuard(a)
		return
	}
	s.reload()
	a.Pop()
	if s.mode == modelFormModeAdd {
		a.SetMessage(fillHubSlots(missingOr(s.b, "models.form.saved_add", missingModelFormSavedAdd), map[string]string{
			"id":       providers.MaskOperand(m.ID),
			"provider": providers.MaskOperand(s.providerName),
		}))
		return
	}
	a.SetMessage(fillHubSlots(missingOr(s.b, "models.form.saved_edit", missingModelFormSavedEdit), map[string]string{
		"id":       providers.MaskOperand(m.ID),
		"provider": providers.MaskOperand(s.providerName),
	}))
}

// reload re-reads the registry after a write (the count and duplicate set
// stay fresh if the screen survives an error path).
func (s *modelFormScreen) reload() {
	s.existing = map[string]bool{}
	s.count = 0
	if reg, err := providers.Load(); err == nil {
		if p := findProviderCI(reg, s.providerName); p != nil {
			s.count = len(p.Models)
			for _, m := range p.Models {
				s.existing[m.ID] = true
			}
		}
	}
}

// formGuard is the transparent dirty-form gate. While it is armed (the form
// is dirty) it receives every key in CONFIRM scope before the App Esc
// precedence: it forwards everything except Esc to the form, and Esc on a
// dirty form opens the discard-confirm. Its body carries the form hint, so
// the armed gate doubles as the form footer.
type formGuard struct {
	s *modelFormScreen
}

func (g *formGuard) Title() string { return "" }

func (g *formGuard) Body(w int) []string {
	return []string{missingOr(g.s.b, "models.form.hint", missingModelFormHint)}
}

func (g *formGuard) Footer() string { return "" }

// Key forwards one key. Esc on a dirty form opens the discard-confirm and
// keeps the gate; Esc on a clean form closes the gate (the Esc is consumed,
// and the App pop happens on a further Esc, plain precedence).
func (g *formGuard) Key(k Key, a *App) bool {
	s := g.s
	if k == KeyEsc {
		if !s.dirty() {
			return false
		}
		s.askDiscard()
		return true
	}
	s.dispatch(k)
	if s.dirty() && a.Modal() == nil {
		a.ShowModal(g)
	}
	return true
}
