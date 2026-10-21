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
	fThinking
	fEffort
	fVision
	fVideo
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
	// catalog-resolved, fallback kept: models.form.thinking
	missingModelFormThinkingLabel = "Thinking (y/n)"
	// catalog-resolved, fallback kept: models.form.effort
	missingModelFormEffortLabel = "Reasoning effort"
	// catalog-resolved, fallback kept: models.form.vision
	missingModelFormVisionLabel = "Vision (y/n)"
	// catalog-resolved, fallback kept: models.form.video
	missingModelFormVideoLabel = "Video (y/n)"
	// catalog-resolved, fallback kept: models.validation.effort
	missingModelFormEffortInvalid = "Reasoning effort must be one of: default, none, low, medium, high, xhigh, max."
	// catalog-resolved, fallback kept: models.validation.yes_no. The three
	// y/n fields (thinking, vision, video) share one generic refusal
	// message; the phase's catalog work shipped the key in all 12 catalogs
	// and the const stays as the documented fallback.
	missingModelFormYesNoInvalid = "Value must be y or n."
	// catalog-resolved, fallback kept: models.form.stale_base. The stale-base
	// refusal shipped in this phase and the catalog work landed the key in all
	// 12 catalogs; the const stays as the documented fallback. The message
	// carries no operands, so nothing untrusted can echo through it.
	missingModelFormStaleBase = "The model changed on disk since this form opened. Close and reopen the form; nothing was written."
)

// looksSecretValue is the model form's ingest guard: it answers whether a
// free-text model field carries credential material rather than a name.
// providers.LooksLikeAPIKey cannot be used here: it flags ANY operand
// containing "-" or "." (correct for env-var names, fatal for ordinary model
// ids such as "acme-fast" and for hyphenated tags).
//
// The guard is a delegation, not an implementation. Both doors (this form and
// the CLI model verbs) and every display sink now share ONE policy in
// internal/providers (SecretShaped / MaskSlot): a category-based separator
// class instead of an enumerated marker list, the invisible classes deleted
// first, a fixpoint strip of the leading decoration, and the same shape test
// over the raw value and its stripped core. The four earlier rounds each grew
// a local list here and in the CLI twin and the two drifted apart; a local list
// in this file would re-open that class, and the parity pin fails if one
// reappears. The error path never echoes the value.
//
// The body is the NAME authority (providers.NameCarriesCredential), the union
// of all three shared arms: SecretShaped OR MaskOperand changed OR MaskSlot
// changed. SecretShaped alone left this screen with two disagreeing answers for
// one value (FIX-7): it is false for the underscore-head class
// (testfixtures.SKLive), while the grid row and the M4 diff already
// render that value through providers.MaskSlot ("sk_li****"), so the form
// accepted a value its own frames masked as a key and the M4 y persisted it RAW
// into models[].id. With the union the two answers cannot disagree: the
// accepted set is exactly the set every sink on this screen renders
// byte-identically (MaskSlot is the identity), and a refused value is one the
// same authority masks. The union is the expression the provider form's name
// arm has used since FIX-K, so both forms now carry one rule.
//
// The consequence is wider than the key class on purpose: a value the slot
// authority re-renders for ANY reason is refused, including a 16+ rune
// single-class word, a 16-digit serial, and a drive-decorated, relative or URL
// location (whose rendering is the stripped core rather than the typed bytes).
// Accepting those while the frames show something else is the same
// masked-display/raw-persist divergence in a milder dress. The accept side is
// pinned against the realistic model-ID corpus in
// TestModelFormIDAuthorityAddsOnlyCredentialClass.
func looksSecretValue(val string) bool {
	return providers.NameCarriesCredential(val)
}

// secretValuePrefixes is a VIEW of the shared policy's credential-head list
// (providers.SlotSecretPrefixes), not a list kept in this package: the prompt
// editor (screen_prompt_editor.go) scans a multi-line buffer line by line
// against it. The list itself lives once, in internal/providers, and the
// parity pin fails if a literal prefix list reappears here.
var secretValuePrefixes = providers.SlotSecretPrefixes()

// credentialTokenShaped is the policy's opaque-run shape test
// (providers.OpaqueRunShaped) under the name the prompt editor uses: one
// uninterrupted base64-alphabet run at the shared floor with both letter and
// digit classes. It carries no local implementation for the same reason.
func credentialTokenShaped(v string) bool {
	return providers.OpaqueRunShaped(v)
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

	// effortTouched latches a real edit of the reasoning effort field (G-M5):
	// a backspace, a forward delete, or a rune insertion that actually changed
	// the field text. The seed is normalized, so the field value alone cannot
	// tell an untouched seed from a normalized-equal retype ("HIGH" on disk,
	// "high" retyped); the latch is what lets buildModel persist the retyped
	// bytes and the approval diff show them. Edge keys are no-ops in the Form
	// (backspace at the value start, delete at the value end), so the latch
	// follows the text, not the key: a keystroke that wrote nothing must not
	// rewrite pristine raw bytes on the next save.
	effortTouched bool
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
			map[string]string{"provider": maskSlotOperand(providerName)})
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
					map[string]string{"id": maskSlotOperand(modelID), "provider": maskSlotOperand(providerName)})
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
		c := s.orig.Capabilities
		if c.SupportsThinking {
			vals[fThinking] = "y"
		}
		// Seeded normalized so an uppercase or padded hand-edited registry
		// value neither dirties the form nor diffs against its own save
		// (F-M2); the raw original still persists untouched (buildModel).
		if e := normEffort(c.ReasoningEffort); e != "" {
			vals[fEffort] = e
		}
		if c.SupportsVision {
			vals[fVision] = "y"
		}
		if c.SupportsVideo {
			vals[fVideo] = "y"
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

// stripDiffControls removes control runes (the Cc class) and the format class
// (Cf: bidi overrides, zero-width joiners and spaces, BOM) from a value before
// it reaches the M4 approval body. The painter strips control runes from
// untrusted frame lines at render time, but the approval summary is composed
// here as screen content, and a Cf rune inside a credential defeats the
// classifier before any painter sees it. The rule lives in the shared policy
// (providers.StripSlotControls) and the CLI twin delegates to the same one, so
// the two doors cannot drift.
func stripDiffControls(s string) string {
	return providers.StripSlotControls(s)
}

// maskSlotOperand renders one registry-slot operand for this screen. It is a
// delegation to providers.MaskSlot, the single registry-slot display authority:
// control-run strip, then MaskRegistryValue (a registry slot is never a
// legitimate path, so MaskOperand's deliberate location exemption must not let
// a marker-decorated credential ride raw into the approval frame), then the
// shared policy's identity-fallback (fold + strip + mask the core) for the
// decoration classes the authority's ASCII marker set cannot see. A value the
// policy does not flag is returned byte-identical, so the diff stays
// byte-stable.
//
// The name stays because every model-screen sink already calls it; the body is
// one delegation and the parity pin fails if a local list or a local strip
// reappears here.
func maskSlotOperand(v string) string {
	return providers.MaskSlot(v)
}

// maskJoined masks each item individually before joining, so one
// credential-shaped entry cannot hide behind the join separator of its
// neighbors (MaskRegistryValue on the already-joined string would see the
// separators and stay readable). Every item is a registry slot (a model tag, a
// tool grant), so each one routes through maskSlotOperand.
func maskJoined(items []string, sep string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = maskSlotOperand(it)
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
		{Label: missingOr(s.b, "models.form.thinking", missingModelFormThinkingLabel), Value: vals[fThinking], Validate: s.validateYesNo},
		{Label: missingOr(s.b, "models.form.effort", missingModelFormEffortLabel), Value: vals[fEffort], Validate: s.validateEffort},
		{Label: missingOr(s.b, "models.form.vision", missingModelFormVisionLabel), Value: vals[fVision], Validate: s.validateYesNo},
		{Label: missingOr(s.b, "models.form.video", missingModelFormVideoLabel), Value: vals[fVideo], Validate: s.validateYesNo},
		{Label: s.b.T("models.form.capabilities"), Value: vals[fCaps]},
	}
}

func (s *modelFormScreen) idTaken(id string) bool {
	if s.mode == modelFormModeEdit && id == s.modelID {
		return false
	}
	return s.existing[id]
}

// dupMessage renders the duplicate-id refusal. The id is a registry slot (it
// names an existing on-disk model), so it routes through the slot masker, not
// MaskOperand's location exemption.
func (s *modelFormScreen) dupMessage(id string) string {
	return s.b.Tf("models.validation.id_exists", map[string]string{"id": maskSlotOperand(id)})
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

// validateFamily classifies the family value as buildModel stores it, trimmed:
// the shared name authority refuses any value the registry-slot authority
// re-renders, and MaskSlot trims, so an untrimmed read refused a padded benign
// family (" swift") as secret/key material while storage would have kept
// "swift". Same class as FIX-8a, one field over.
func (s *modelFormScreen) validateFamily(v string) error {
	if looksSecretValue(strings.TrimSpace(v)) {
		return errors.New(missingOr(s.b, "models.validation.secret", missingModelFormSecret))
	}
	return nil
}

// validateTags classifies every tag of the comma-separated field. The split is
// splitTags — the storage splitter itself — not a local strings.Split: edit mode
// prefills this field with strings.Join(tags, ", "), so an untrimmed split
// handed the classifier " tools" for every tag after the first, and the shared
// name authority refuses any value the registry-slot display authority
// re-renders (MaskSlot trims, so it re-renders a padded tag). That refused the
// wizard-default 2+ tag record on every save and a natural "beta, fast" add.
// Reading one splitter closes the validation/storage asymmetry by construction:
// a tag this accepts is a tag splitTags stores byte-identically, and the
// trim-and-drop-empty rules stay the storage ones.
func (s *modelFormScreen) validateTags(v string) error {
	for _, part := range splitTags(v) {
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

// validateYesNo mirrors validateEnabled for the capability flag fields
// (thinking, vision, video): empty means off, y/yes on, n/no off.
func (s *modelFormScreen) validateYesNo(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "y", "n", "yes", "no":
		return nil
	}
	return errors.New(missingOr(s.b, "models.validation.yes_no", missingModelFormYesNoInvalid))
}

// validEffortValues is the closed reasoning-effort set the gateway
// understands (internal/openaiup/effort.go ValidReasoningEfforts; the test
// file pins the two sets identical). An empty value is accepted and means
// runtime default (EffectiveReasoningEffort).
var validEffortValues = map[string]bool{
	"default": true,
	"none":    true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

// normEffort applies the validator's normalization (trim + lowercase) to a
// reasoning-effort value. Every comparison site (validator, dirty/diff
// path, buildModel) routes through it so a non-normalized on-disk value
// cannot phantom-diff against its own save (F-M2).
func normEffort(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

// validateEffort accepts empty (runtime default) or one of
// validEffortValues, case-insensitively. The secret guard runs before the
// set check so a pasted key is refused with the secret message (never
// echoed), the same treatment every other free-text field gets.
func (s *modelFormScreen) validateEffort(v string) error {
	e := normEffort(v)
	if e == "" {
		return nil
	}
	if looksSecretValue(e) {
		return errors.New(missingOr(s.b, "models.validation.secret", missingModelFormSecret))
	}
	if !validEffortValues[e] {
		return errors.New(missingOr(s.b, "models.validation.effort", missingModelFormEffortInvalid))
	}
	return nil
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
		// Trimmed as validateFamily and buildModel read it: padding alone must
		// not raise a warning the save path would accept.
		return secret(strings.TrimSpace(s.form.Value(fFamily)))
	case fTags:
		// Same splitter as validateTags and buildModel: the live check must
		// judge the tags the save would store, not their padded text.
		return secret(splitTags(s.form.Value(fTags))...)
	case fEffort:
		// Same normalization as validateEffort and buildModel (trim + lower).
		return secret(normEffort(s.form.Value(fEffort)))
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
	if s.form.Cursor() == fEffort && fieldEditKey(k) {
		before := s.form.Value(fEffort)
		s.form.HandleKey(k)
		if s.form.Value(fEffort) != before {
			s.effortTouched = true
		}
		return
	}
	s.form.HandleKey(k)
}

// fieldEditKey reports whether k is one of the keys Form.HandleKey writes the
// focused text field on: a backspace, a forward delete, or a rune insertion.
// The Form drops the edge cases (backspace at the value start, forward delete
// at the value end), so the caller confirms the write by comparing the field
// text around the call instead of trusting the key set alone.
func fieldEditKey(k Key) bool {
	if k == KeyBackspace || k == KeyDelete {
		return true
	}
	_, ok := EditRune(k)
	return ok
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
//
// H-4: the restore also clears the effort latch. The latch is a screen-level
// bit that the Form does not own, so Restore alone left it set; a save of the
// restored (pristine-looking) form then persisted the normalized bytes over a
// raw original. Restore is the one path that returns the form to its
// construction values, so the latch is cleared here, next to it.
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
			s.effortTouched = false
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
			// Leave the Save row and focus the first failing field so the
			// error points at the repair site (round-2 F3).
			s.onSaveRow = false
			s.form.SetCursor(i)
			return
		}
	}
	m, tools := s.buildModel()
	var summary []string
	// The header sentences name two registry slots: the model id and the
	// provider name. Both come from providers.json (the id from the form or the
	// disk record, the provider from the caller's row), so both route through
	// the slot masker (H-2): MaskOperand's location exemption printed a
	// drive-decorated credential raw into the consent header while the field
	// line below it masked the same value.
	if s.mode == modelFormModeAdd {
		summary = append(summary,
			fillHubSlots(missingOr(s.b, "models.form.approval.add", missingModelFormApprovalAdd), map[string]string{
				"id":       maskSlotOperand(m.ID),
				"provider": maskSlotOperand(s.providerName),
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
				"id":       maskSlotOperand(m.ID),
				"provider": maskSlotOperand(s.providerName),
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
// an explicit false. The capability flags (thinking, effort, vision, video)
// are read from the form in both modes: add mode must not zero them, and
// edit mode must not pass them through invisibly from the original.
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
	caps := providers.Capabilities{
		SupportsTools:    len(s.tools) > 0,
		SupportsThinking: parseYesNo(s.form.Value(fThinking)),
		ReasoningEffort:  normEffort(s.form.Value(fEffort)),
		SupportsVision:   parseYesNo(s.form.Value(fVision)),
		SupportsVideo:    parseYesNo(s.form.Value(fVideo)),
	}
	if s.origFound {
		caps = s.orig.Capabilities
		caps.SupportsTools = caps.SupportsTools || len(s.tools) > 0
		caps.SupportsThinking = parseYesNo(s.form.Value(fThinking))
		// F-M2 + G-M5: when the effort field is untouched (its normalized form
		// value equals the normalized original), persist the RAW original bytes
		// so a pristine save of a "HIGH"/padded on-disk value does not rewrite
		// the registry. A field the user edited stores the normalized typed
		// value even when it is normalized-equal, so the approval diff (which
		// compares raw bytes, G-M5) and the persisted bytes cannot disagree.
		if e := normEffort(s.form.Value(fEffort)); s.effortTouched || e != normEffort(s.orig.Capabilities.ReasoningEffort) {
			caps.ReasoningEffort = e
		}
		caps.SupportsVision = parseYesNo(s.form.Value(fVision))
		caps.SupportsVideo = parseYesNo(s.form.Value(fVideo))
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

// parseYesNo interprets a capability-flag field: only an explicit y/yes is
// on, everything else (empty, n, no) is off. Unlike parseEnabled the flags
// default to off.
func parseYesNo(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "y", "yes":
		return true
	}
	return false
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
// Every operand is display-safe before the template sees it: the registry slots
// (id, family, the joined tag and tool lists, reasoning effort) route through
// maskSlotOperand or the per-item maskJoined, so a marker-decorated credential
// cannot ride MaskOperand's location exemption (F-M1/G-M1). The numeric and
// boolean operands are literals.
func (s *modelFormScreen) addedDiffLines(m providers.Model, tools []string) []string {
	lines := []string{}
	add := func(field, value string) {
		lines = append(lines, fillHubSlots(missingOr(s.b, "models.form.diff_added", missingModelFormDiffAdded), map[string]string{
			"field": field,
			"value": value,
		}))
	}
	add("id", maskSlotOperand(m.ID))
	if m.Family != "" {
		add("family", maskSlotOperand(m.Family))
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
	// G-M3: buildModel OR-derives SupportsTools from the grant list, so a
	// record can save tool use under a diff that names no field at all. The
	// line mirrors the CLI twin (cmd/agy/pro_settings.go).
	if m.Capabilities.SupportsTools {
		add("supportsTools", "true")
	}
	if m.Capabilities.SupportsThinking {
		add("thinking", "true")
	}
	if m.Capabilities.ReasoningEffort != "" {
		add("reasoningEffort", maskSlotOperand(m.Capabilities.ReasoningEffort))
	}
	if m.Capabilities.SupportsVision {
		add("vision", "true")
	}
	if m.Capabilities.SupportsVideo {
		add("video", "true")
	}
	return lines
}

// changedDiffLines renders one "  field: before -> after" line per changed
// field (edit mode), or the no-changes marker. Two rules decide emission and
// rendering:
//   - emission compares the RAW operands (G-M5): a value that differs only by
//     case or padding still moves the persisted bytes, so the approval must
//     show it. Raw difference is strictly wider than normalized difference, so
//     the pristine normalized-equal case still emits nothing (F-M2).
//   - rendering masks the RAW bytes (G-M4): normalizing before the mask
//     lowercases the operand first, which defeats the case-sensitive secret
//     classes and let a separator-carrying plant render raw.
func (s *modelFormScreen) changedDiffLines(m providers.Model, tools []string) []string {
	lines := []string{}
	emit := func(field, before, after string) {
		lines = append(lines, fillHubSlots(missingOr(s.b, "models.form.diff_changed", missingModelFormDiffChanged), map[string]string{
			"field":  field,
			"before": before,
			"after":  after,
		}))
	}
	// chg emits when the raw operands differ and renders both through the
	// registry-slot authority (a literal operand such as "true" is identity).
	chg := func(field, before, after string) {
		if before == after {
			return
		}
		emit(field, maskSlotOperand(before), maskSlotOperand(after))
	}
	// chgList compares the raw lists, so a difference the mask would fold away
	// (two credentials sharing their first five runes) still emits, and renders
	// each item masked individually.
	chgList := func(field string, before, after []string) {
		if equalKeyList(before, after) {
			return
		}
		emit(field, maskJoined(before, ", "), maskJoined(after, ", "))
	}
	chg("id", s.orig.ID, m.ID)
	chg("family", s.orig.Family, m.Family)
	chgList("tags", s.orig.Tags, m.Tags)
	chg("enabled", strconv.FormatBool(s.orig.IsEnabled()), strconv.FormatBool(m.IsEnabled()))
	chg("contextLength", strconv.Itoa(s.orig.ContextLength), strconv.Itoa(m.ContextLength))
	chg("maxOutputTokens", strconv.Itoa(s.orig.MaxOutputTokens), strconv.Itoa(m.MaxOutputTokens))
	// G-M3: the OR-derivation in buildModel can switch tool use on with no
	// other field moving, so the flag signs in the diff like every other field.
	chg("supportsTools", strconv.FormatBool(s.orig.Capabilities.SupportsTools), strconv.FormatBool(m.Capabilities.SupportsTools))
	chg("thinking", strconv.FormatBool(s.orig.Capabilities.SupportsThinking), strconv.FormatBool(m.Capabilities.SupportsThinking))
	// G-M4/G-M5: the operands are the RAW on-disk and raw built bytes, and the
	// raw difference decides emission. The normalized forms stay the comparison
	// key of the stale-base sign (sameSignedFields), not of this display.
	chg("reasoningEffort", s.orig.Capabilities.ReasoningEffort, m.Capabilities.ReasoningEffort)
	chg("vision", strconv.FormatBool(s.orig.Capabilities.SupportsVision), strconv.FormatBool(m.Capabilities.SupportsVision))
	chg("video", strconv.FormatBool(s.orig.Capabilities.SupportsVideo), strconv.FormatBool(m.Capabilities.SupportsVideo))
	chgList("tools", normalizeTools(s.orig.Capabilities.AllowedTools), tools)
	if len(lines) == 0 {
		lines = append(lines, missingOr(s.b, "models.form.diff_none", missingModelFormDiffNone))
	}
	return lines
}

// sameSignedFields reports whether two model records agree on every field the
// M4 diff signs: id, family, tags, enabled, context, maxOut, the tool-use flag,
// the four capability flags (effort compared normalized, F-M2) and the tool
// grants (compared in normalized order, mirroring the diff's before column).
// SupportsTools signs because it is the one flag the edit-mode write can move
// from the snapshot alone (buildModel OR-derives it from the grant list, G-M3):
// leaving it out let an external supportsTools flip pass the stale-base gate
// and be silently reverted by the approved write (G-M2).
func sameSignedFields(a, b providers.Model) bool {
	return a.ID == b.ID &&
		a.Family == b.Family &&
		equalKeyList(a.Tags, b.Tags) &&
		a.IsEnabled() == b.IsEnabled() &&
		a.ContextLength == b.ContextLength &&
		a.MaxOutputTokens == b.MaxOutputTokens &&
		a.Capabilities.SupportsTools == b.Capabilities.SupportsTools &&
		a.Capabilities.SupportsThinking == b.Capabilities.SupportsThinking &&
		normEffort(a.Capabilities.ReasoningEffort) == normEffort(b.Capabilities.ReasoningEffort) &&
		a.Capabilities.SupportsVision == b.Capabilities.SupportsVision &&
		a.Capabilities.SupportsVideo == b.Capabilities.SupportsVideo &&
		equalKeyList(normalizeTools(a.Capabilities.AllowedTools), normalizeTools(b.Capabilities.AllowedTools))
}

// baseChangedOnDisk reloads the registry and compares the current on-disk
// record of the edited model against the form-open snapshot. It reports
// true (the write must be refused) when the model or provider vanished, a
// signed field drifted, or the registry cannot be located at all. A pure
// load error reports false: Core.ModelUpdate runs its own load and
// surfaces the error, so the refusal message would mislabel it.
func (s *modelFormScreen) baseChangedOnDisk() bool {
	if s.mode != modelFormModeEdit || !s.origFound {
		return false
	}
	reg, err := providers.Load()
	if err != nil {
		return false
	}
	p := findProviderCI(reg, s.providerName)
	if p == nil {
		return true
	}
	for j := range p.Models {
		if p.Models[j].ID == s.modelID {
			return !sameSignedFields(s.orig, p.Models[j])
		}
	}
	return true
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
		// F-M3 (CWE-367): the approval diff was signed against the
		// form-open snapshot. Reload the registry and re-verify the base
		// record before writing; an external edit to the same model while
		// the form was open is refused, not silently reverted.
		if s.baseChangedOnDisk() {
			a.SetMessage(s.b.T("common.error") + ": " + missingOr(s.b, "models.form.stale_base", missingModelFormStaleBase))
			s.rearmGuard(a)
			return
		}
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
	// The success sentences name the same two registry slots as the header, so
	// they route through the slot masker for the same reason (H-7 sweep). Each
	// one opens with the localized common.success label so the message line
	// paints the success role, not the error amber (user decision D7-7B); the
	// error paths above prefix common.error the same way.
	if s.mode == modelFormModeAdd {
		a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "models.form.saved_add", missingModelFormSavedAdd), map[string]string{
			"id":       maskSlotOperand(m.ID),
			"provider": maskSlotOperand(s.providerName),
		}))
		return
	}
	a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(missingOr(s.b, "models.form.saved_edit", missingModelFormSavedEdit), map[string]string{
		"id":       maskSlotOperand(m.ID),
		"provider": maskSlotOperand(s.providerName),
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
