package tui

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// Provider form modes: the openForm factory contract of the providers screen
// (screen_providers.go: "add" from the Add row, "edit" from a row's Edit verb).
// In add mode providerName is empty; in edit mode it names the record to edit,
// and the name is fixed identity (Core.ProviderEdit rewrites baseURL and
// apiKeyEnv only, never the name, and never the model list).
const (
	providerFormModeAdd  = "add"
	providerFormModeEdit = "edit"
)

// Semantic field ids. The grid holds all three rows in add mode; in edit mode
// the name is not a form field at all (a read-only line above the grid), so a
// rune or backspace can never reach it. gridOf maps a grid row index onto one
// of these ids, so no caller indexes the grid by mode.
const (
	pfName = iota
	pfBaseURL
	pfAPIKeyEnv
)

// Focus zones. The defaults block is read-only but focusable: its window is the
// one part of the frame that shrinks when the frame is short (pageSizeFor with
// the chrome counted in providerFormChrome), and arrows/PgUp/PgDn scroll it so
// no defaults row is ever unreachable.
const (
	providerFormZoneGrid = iota
	providerFormZoneDefaults
	providerFormZoneSave
)

// providerFormChrome is the non-window row count of the composed frame around
// the defaults window: the app chrome (the screen title, the separator under
// it, the separator above the base footer, and the base footer itself = 4) plus
// the body chrome (the grid or the read-only name line plus its grid rows = 3,
// the blank under the grid, the defaults block title, the blank under the block,
// the Save row, and the blank + hint pair = 9). The one-off rows this frame may
// add (a load/not-found line, a live validation message, the app message block)
// are counted per frame in chrome(), exactly like editorChrome in the preset
// editor. At 80x24 the value leaves 11 defaults rows, so the whole block renders
// without paging; a shorter frame pages it instead of letting the composer cut
// the Save row or the hint off the tail.
const providerFormChrome = 13

// English fallbacks for the catalog keys this screen resolves through missingOr.
// Every one of them is a providers.form.* / providers.validation.* key (plus
// the two providers.add.* prompts the form shares with the console), and each
// resolves in all twelve locales. The const stays as the fallback so a catalog
// lacking the translation still renders this screen: missingOr answers from the
// catalog when the key resolves and from the const otherwise, so no call site
// needs to change when a locale file is reworked.
const (
	// catalog-resolved, fallback kept: providers.add.title
	missingProviderFormTitleAdd = "Add Provider"
	// catalog-resolved, fallback kept: providers.form.title.edit
	missingProviderFormTitleEdit = "Edit Provider"
	// catalog-resolved, fallback kept: providers.add.name
	missingProviderFormName = "Provider name"
	// catalog-resolved, fallback kept: providers.add.base_url
	missingProviderFormBaseURL = "Base URL"
	// catalog-resolved, fallback kept: providers.add.key_env
	missingProviderFormAPIKeyEnv = "API key env-var NAME"
	// catalog-resolved, fallback kept: providers.form.name_fixed
	missingProviderFormNameFixed = "Provider (fixed)"
	// catalog-resolved, fallback kept: providers.form.hint
	missingProviderFormHint = "Enter opens the approval diff · Tab walks the fields, the defaults block, and Save · Esc asks to discard unsaved edits"
	// catalog-resolved, fallback kept: providers.form.defaults.title
	missingProviderFormDefaultsTitle = "providers.json defaults (read-only, never written by this form)"
	// catalog-resolved, fallback kept: providers.form.defaults.empty
	missingProviderFormDefaultsEmpty = "(no defaults block in providers.json; engine defaults apply)"
	// catalog-resolved, fallback kept: providers.form.approval.edit
	missingProviderFormApprovalEdit = "Edits provider {{name}} in place: base URL and key env NAME only. Its model list and every key value are untouched."
	// catalog-resolved, fallback kept: providers.form.diff_added
	missingProviderFormDiffAdded = "+ {{field}}: {{value}}"
	// catalog-resolved, fallback kept: providers.form.diff_changed
	missingProviderFormDiffChanged = "  {{field}}: {{before}} -> {{after}}"
	// catalog-resolved, fallback kept: providers.form.diff_none
	missingProviderFormDiffNone = "(no field changes)"
	// catalog-resolved, fallback kept: providers.form.saved_add
	missingProviderFormSavedAdd = "Provider {{name}} added to providers.json."
	// catalog-resolved, fallback kept: providers.form.saved_edit
	missingProviderFormSavedEdit = "Provider {{name}} updated in providers.json."
	// catalog-resolved, fallback kept: providers.form.unchanged_edit
	missingProviderFormUnchangedEdit = "Provider {{name}}: providers.json already held these endpoint values, so nothing was written."
	// catalog-resolved, fallback kept: providers.form.provider_missing
	missingProviderFormMissing = "Provider {{name}} is not in providers.json, so there is nothing to edit here. Close this form and reopen it from the providers list."
	// catalog-resolved, fallback kept: providers.form.provider_ambiguous
	missingProviderFormAmbiguous = "More than one provider matches {{name}} under case folding, so the edit target is not determinable. Close this form, and rename or delete the duplicate before editing."
	// catalog-resolved, fallback kept: providers.form.disk_check_failed
	// FIX-7A wording: this sentence is shown by BOTH commit gates (the add-mode
	// duplicate-name re-check and the edit-mode stale-base comparison), so it
	// names the neutral class, the on-disk check, instead of the add-mode check
	// alone. The key stays unchanged; the twelve locale catalogs are updated by
	// the translation wave that follows this unit.
	missingProviderFormDiskCheckFailed = "providers.json could not be re-read, so the on-disk check did not run. Nothing was written; close and reopen the form and retry."
	// catalog-resolved, fallback kept: providers.form.stale_base
	missingProviderFormStaleBase = "The provider changed on disk since this form opened. Close and reopen the form; nothing was written."
	// catalog-resolved, fallback kept: providers.form.discard_title
	missingProviderFormDiscardTitle = "Discard unsaved edits?"
	// catalog-resolved, fallback kept: providers.form.discard_body
	missingProviderFormDiscardBody = "The edits on this form are dropped and nothing is written to providers.json."
	// catalog-resolved, fallback kept: providers.form.cancelled
	missingProviderFormCancelled = "Cancelled. Nothing was written."
	// catalog-resolved, fallback kept: providers.validation.name_required
	missingProviderFormNameRequired = "Provider name is required."
	// catalog-resolved, fallback kept: providers.validation.name
	missingProviderFormNameInvalid = "Provider name must be 1-64 characters: start with a letter or digit, then only letters, digits, dot, underscore, or hyphen (no spaces or shell metacharacters)."
	// catalog-resolved, fallback kept: providers.validation.name_exists
	missingProviderFormNameExists = "A provider named {{name}} already exists. Open its Edit row instead; adding it here would replace the entry and drop its models."
	// catalog-resolved, fallback kept: providers.validation.secret
	missingProviderFormValueSecret = "A field value looks like secret/key material and cannot be saved (value hidden). Provider fields store a name and a base URL, never a key value."
	// catalog-resolved, fallback kept: providers.validation.base_url_required
	missingProviderFormBaseURLRequired = "Base URL is required."
	// catalog-resolved, fallback kept: providers.validation.base_url
	missingProviderFormBaseURLInvalid = "Base URL must use https://; http:// is accepted only for loopback hosts (localhost, 127.0.0.1, ::1)."
	// catalog-resolved, fallback kept: providers.validation.api_key_env
	missingProviderFormAPIKeyEnvInvalid = "API key env-var NAME must name an environment variable (set it, or use a known provider key variable such as GEMINI_API_KEY). A key value is never stored."
	// catalog-resolved, fallback kept: providers.validation.api_key_env_secret
	missingProviderFormAPIKeyEnvSecret = "The API key env field holds an environment-variable NAME, never a key. This value looks like key material and was refused (value hidden)."
)

// providerFormScreen is the provider add/edit form (decision 1A). It mirrors
// the CLI twin's add-json validation (cmd/agy/pro_settings.go add-json ->
// settings.ParseProviderSpecJSON -> Core.ProviderAdd -> providers.UpsertProvider),
// so a provider reachable through the console is reachable here with the same
// rules.
//
// The field grid is the shared Form. Above it, edit mode renders the fixed
// provider name as a read-only line. Below it sits the read-only defaults block
// (the rung phase 2 taught the fanout ladder to consume) and the Save row. Enter
// on Save does NOT write: it opens the M4 Approval modal with the exact
// mutation summary, and only y reaches the Core verbs (ProviderAdd in add mode,
// ProviderEdit in edit mode). apiKeyEnv carries an environment-variable NAME;
// a pasted key VALUE is refused by the twin's own guard
// (providers.ValidateAPIKeyEnvName) and is never rendered raw.
//
// The name and baseURL fields carry the same never-persist-a-VALUE rule, at the
// ingest door instead of the display door: a value the grid or the M4 diff would
// MASK is refused outright (see fieldCarriesCredential), so the form cannot
// render "sk-li****" and then write the raw key into providers.json. The refusal
// is a live check on the focused field and a save-time validator, both ahead of
// the M4 gate, and neither echoes the value.
//
// Esc handling mirrors the model form: the App dispatcher consumes Esc before
// any screen sees it, so a dirty form arms the transparent providerFormGuard,
// which opens the discard-confirm on Esc and forwards every other key to the
// form. A clean Esc pops through the plain App precedence.
type providerFormScreen struct {
	core         *settings.Core
	mode         string
	providerName string

	lang string
	b    *i18n.Bundle

	form   *Form
	gridOf []int    // grid row index -> pf* semantic id
	base   []string // construction values (dirty comparison + discard restore)

	reg       *providers.Registry
	loadErr   string // masked load / not-found line; also blocks every save
	orig      providers.Provider
	origFound bool

	defList *stepList // read-only providers.Defaults window

	zone   int
	errMsg string
	app    *App
	guard  *providerFormGuard
}

// NewProviderFormScreen builds the provider add/edit form. modeName is
// providerFormModeAdd ("add", providerName empty) or providerFormModeEdit
// ("edit", providerName names the record to edit). The UI language resolves
// from the persisted state (AGY_PRO_HOME state.json), defaulting to English.
// A provider that is not in the registry renders the honest not-found state and
// refuses every save; a registry that cannot be read renders its masked load
// error and also refuses every save, so a corrupt file can never be rewritten
// from this form.
func NewProviderFormScreen(core *settings.Core, modeName string, providerName string) Screen {
	s := &providerFormScreen{
		core:         core,
		mode:         modeName,
		providerName: providerName,
		reg:          &providers.Registry{},
	}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	s.setLang(i18n.Normalize(lang))
	reg, err := providers.Load()
	switch {
	case err == nil:
		s.reg = reg
	case errors.Is(err, os.ErrNotExist), errors.Is(err, providers.ErrNotConfigured):
		// Pre-setup is the normal state for add mode: the first provider can be
		// created into an empty registry (Core.loadRegistry has the same rule).
	default:
		s.loadErr = providers.MaskOperand(err.Error())
	}
	if s.mode == providerFormModeEdit && s.loadErr == "" {
		p, err := resolveProviderFormTarget(s.reg, providerName)
		switch {
		case errors.Is(err, errProviderFormAmbiguous):
			// The Core verb refuses to guess between two records that match under
			// case folding (core.go:397-431); the form must not prefill one of
			// them either, because the user would be editing a record the write
			// refuses to touch.
			s.loadErr = fillHubSlots(
				missingOr(s.b, "providers.form.provider_ambiguous", missingProviderFormAmbiguous),
				map[string]string{"name": providers.MaskSlot(providerName)})
		case p == nil:
			// The operand is a provider-name operand, so it routes through the
			// registry-slot authority like every other name echo on this screen
			// (FIX-7A: a key-shaped operand is the identity under MaskOperand).
			s.loadErr = fillHubSlots(
				missingOr(s.b, "providers.form.provider_missing", missingProviderFormMissing),
				map[string]string{"name": providers.MaskSlot(providerName)})
		default:
			s.orig = *p
			s.origFound = true
		}
	}
	s.defList = newStepList(nil) // read-only: activation has no callback
	s.defList.SetRows(s.defaultsRows())
	s.buildForm()
	return s
}

// setLang loads the bundle for lang once per language.
func (s *providerFormScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// buildForm composes the grid for the current mode and seeds it. Add mode
// starts empty; edit mode prefills baseURL and apiKeyEnv from the live registry
// record. The name is never a grid row in edit mode (see the pfName comment).
func (s *providerFormScreen) buildForm() {
	seed := map[int]string{}
	if s.origFound {
		seed[pfBaseURL] = s.orig.BaseURL
		seed[pfAPIKeyEnv] = s.orig.APIKeyEnv
	}
	fields := make([]Field, 0, 3)
	s.gridOf = s.gridOf[:0]
	if s.mode != providerFormModeEdit {
		fields = append(fields, Field{
			Label:    missingOr(s.b, "providers.add.name", missingProviderFormName),
			Value:    seed[pfName],
			Validate: s.validateName,
		})
		s.gridOf = append(s.gridOf, pfName)
	}
	fields = append(fields, Field{
		Label:    missingOr(s.b, "providers.add.base_url", missingProviderFormBaseURL),
		Value:    seed[pfBaseURL],
		Validate: s.validateBaseURL,
	})
	s.gridOf = append(s.gridOf, pfBaseURL)
	fields = append(fields, Field{
		Label:    missingOr(s.b, "providers.add.key_env", missingProviderFormAPIKeyEnv),
		Value:    seed[pfAPIKeyEnv],
		Validate: s.validateAPIKeyEnv,
	})
	s.gridOf = append(s.gridOf, pfAPIKeyEnv)
	s.form = NewForm(fields)
	s.base = s.form.Snapshot()
}

// Mode is ModeEdit while the field grid owns the focus (text entry) and
// ModeNav on the read-only defaults block and the Save row.
func (s *providerFormScreen) Mode() Mode {
	if s.zone == providerFormZoneGrid {
		return ModeEdit
	}
	return ModeNav
}

// Title resolves through the catalog (Add Provider / Edit Provider).
func (s *providerFormScreen) Title() string {
	if s.mode == providerFormModeEdit {
		return missingOr(s.b, "providers.form.title.edit", missingProviderFormTitleEdit)
	}
	return missingOr(s.b, "providers.add.title", missingProviderFormTitleAdd)
}

// idx resolves a semantic field id to its grid row, or -1 when the current mode
// has no such row (the name in edit mode).
func (s *providerFormScreen) idx(sem int) int {
	for i, id := range s.gridOf {
		if id == sem {
			return i
		}
	}
	return -1
}

// val reads a semantic field value; an absent row (the name in edit mode)
// answers the form-open record value.
func (s *providerFormScreen) val(sem int) string {
	if i := s.idx(sem); i >= 0 {
		return s.form.Value(i)
	}
	if sem == pfName {
		return s.recordName()
	}
	return ""
}

// recordName is the identity this form edits: the on-disk record's own spelling
// once it resolves (that is the exact-case name the Core verb addresses), and the
// caller's operand otherwise (a not-found or ambiguous target, which never
// writes).
func (s *providerFormScreen) recordName() string {
	if s.origFound {
		return s.orig.Name
	}
	return s.providerName
}

func (s *providerFormScreen) trimName() string      { return strings.TrimSpace(s.val(pfName)) }
func (s *providerFormScreen) trimBaseURL() string   { return strings.TrimSpace(s.val(pfBaseURL)) }
func (s *providerFormScreen) trimAPIKeyEnv() string { return strings.TrimSpace(s.val(pfAPIKeyEnv)) }

// displayValue renders one grid value for the frame. The name and the apiKeyEnv
// slots both go through the registry-slot authority providers.MaskSlot: both are
// fields a key VALUE is likely to be pasted into, and MaskSlot cannot be argued
// out of masking by a location-shaped decoration. The name arm of
// fieldCarriesCredential is that same authority (through the shared
// providers.NameCarriesCredential), so the row a frame masks is exactly the row
// the form refuses (FIX-K: under plain MaskOperand an underscore-head key such
// as testfixtures.SKLive rendered RAW while the form refused it). The base
// URL keeps the URL-layered maskBaseURL, which starts with MaskOperand and drops
// URL userinfo; its ingest arm is the shared URL authority
// (providers.BaseURLCarriesCredential), and the two stay separate on purpose:
// the display masker keeps a benign path readable by design while the ingest
// authority must refuse a credential in any slot, including the host label
// (FIX-8f) and a benign userinfo URL's credential-shaped parts. A benign name or
// env NAME ("ACME_API_KEY") is identity under MaskSlot, so the field stays
// readable while it is typed.
func (s *providerFormScreen) displayValue(sem int) string {
	v := s.val(sem)
	switch sem {
	case pfName:
		return maskSlotOperand(v)
	case pfBaseURL:
		return maskBaseURL(v)
	default:
		return maskSlotOperand(v)
	}
}

// renderGrid renders the field grid with the display masking applied per row.
// It mirrors Form.Render's layout (cursor marker, label column padded through
// cellwidth, " : " separator) exactly, because the screen needs one row masked
// and the shared Form renders raw values; the preset editor's renderForm does
// the same for its search_key_env row. Every line is clamped through
// cellwidth.Truncate, the same clamp Form.Render uses.
func (s *providerFormScreen) renderGrid(w int) []string {
	labelW := 0
	for i := 0; i < s.form.Len(); i++ {
		if cw := cellwidth.String(s.form.Field(i).Label); cw > labelW {
			labelW = cw
		}
	}
	out := make([]string, 0, s.form.Len())
	for i := 0; i < s.form.Len(); i++ {
		marker := "  "
		if s.zone == providerFormZoneGrid && s.form.Cursor() == i {
			marker = "▶ "
		}
		line := marker + cellwidth.PadRight(s.form.Field(i).Label, labelW) + " : " + s.displayValue(s.gridOf[i])
		out = append(out, cellwidth.Truncate(line, w))
	}
	return out
}

// defaultsRows renders the read-only providers.json defaults block as one row
// per set field, in the JSON order, plus an honest empty row when the block
// carries nothing. Free-text values (tools, excludeFamilies) are disk-borne, so
// each item routes through the registry-slot masker; the numeric values are
// literals.
func (s *providerFormScreen) defaultsRows() []Row {
	def := s.reg.Defaults
	var rows []Row
	if def.N != 0 {
		rows = append(rows, Row{Label: "n", Note: strconv.Itoa(def.N)})
	}
	if def.TimeoutSeconds != 0 {
		rows = append(rows, Row{Label: "timeoutSeconds", Note: strconv.Itoa(def.TimeoutSeconds)})
	}
	if def.MaxTurns != 0 {
		rows = append(rows, Row{Label: "maxTurns", Note: strconv.Itoa(def.MaxTurns)})
	}
	if def.Tools != "" {
		rows = append(rows, Row{Label: "tools", Note: maskJoined(strings.Split(def.Tools, ","), ",")})
	}
	if def.TokenBudget != 0 {
		rows = append(rows, Row{Label: "tokenBudget", Note: strconv.Itoa(def.TokenBudget)})
	}
	if len(def.ExcludeFamilies) > 0 {
		rows = append(rows, Row{Label: "excludeFamilies", Note: maskJoined(def.ExcludeFamilies, ", ")})
	}
	if len(rows) == 0 {
		rows = append(rows, Row{Label: missingOr(s.b, "providers.form.defaults.empty", missingProviderFormDefaultsEmpty)})
	}
	return rows
}

// chrome counts the rows one composed frame spends outside the defaults window:
// the fixed chrome plus every one-off row this frame actually renders. The app
// message block and the form message block each count as two because the
// composer gives them their own section (a separator plus the line).
func (s *providerFormScreen) chrome(a *App) int {
	chrome := providerFormChrome
	if s.loadErr != "" {
		chrome++
	}
	if s.messageLine() != "" {
		chrome += 2
	}
	if a != nil && a.Message() != "" {
		chrome += 2
	}
	return chrome
}

// Draw renders the load/not-found line, the grid (with the fixed name line in
// edit mode), the read-only defaults block, the Save row, the hint, and the
// message line. The defaults window is sized from the real chrome of this frame
// so the Save row and the hint always survive; the hint is rendered by the guard
// modal while it is armed, so it is skipped here in that case.
func (s *providerFormScreen) Draw(a *App) []string {
	w := a.Width()
	var lines []string
	if s.loadErr != "" {
		lines = append(lines, cellwidth.Truncate(s.b.T("common.error")+": "+s.loadErr, w))
	}
	if s.mode == providerFormModeEdit {
		// FIX-7A: the on-disk name is rendered through the registry-slot
		// authority, not the bare operand masker. A hand-planted key-shaped name
		// (testfixtures.SKLive) is the identity under MaskOperand, so it
		// rendered RAW in this read-only line while the add-mode name row masked
		// it; MaskSlot is the one authority that reads that class.
		line := "  " + missingOr(s.b, "providers.form.name_fixed", missingProviderFormNameFixed) + " : " + providers.MaskSlot(s.recordName())
		lines = append(lines, cellwidth.Truncate(line, w))
	}
	lines = append(lines, s.renderGrid(w)...)

	title := "  " + missingOr(s.b, "providers.form.defaults.title", missingProviderFormDefaultsTitle)
	if s.zone == providerFormZoneDefaults {
		title = "▶ " + missingOr(s.b, "providers.form.defaults.title", missingProviderFormDefaultsTitle)
	}
	lines = append(lines, "", cellwidth.Truncate(title, w))
	s.defList.SetPageSize(pageSizeFor(a.Height(), s.chrome(a)))
	lines = append(lines, s.defList.Render(w)...)

	saveRow := "  " + s.b.T("common.save")
	if s.zone == providerFormZoneSave {
		saveRow = "▶ " + s.b.T("common.save")
	}
	lines = append(lines, "", cellwidth.Truncate(saveRow, w))
	if s.app == nil || a.Modal() != s.guard {
		lines = append(lines, "", cellwidth.Truncate(missingOr(s.b, "providers.form.hint", missingProviderFormHint), w))
	}
	if msg := s.messageLine(); msg != "" {
		lines = append(lines, "", cellwidth.Truncate(msg, w))
	}
	return lines
}

// messageLine composes the form message: a save-validation error first, then
// the focused field's validation error, then the live checks on the focused
// field (key-material value, duplicate name), so the user sees the problem while
// typing. No refusal ever echoes the value it refuses. The secrets check runs
// before the name-duplicate check for the reason the model form gives: a pasted
// key is never a duplicate, so naming it a duplicate would be wrong and would
// echo part of the value.
func (s *providerFormScreen) messageLine() string {
	if s.errMsg != "" {
		return s.errMsg
	}
	if msg := s.form.Message(); msg != "" {
		return msg
	}
	if s.zone != providerFormZoneGrid {
		return ""
	}
	i := s.form.Cursor()
	if i < 0 || i >= len(s.gridOf) {
		return ""
	}
	switch s.gridOf[i] {
	case pfName:
		if name := s.trimName(); name != "" {
			if fieldCarriesCredential(pfName, name) {
				return s.secretMessage()
			}
			if s.nameTaken(name) {
				return s.dupMessage(name)
			}
		}
	case pfBaseURL:
		if raw := s.trimBaseURL(); raw != "" && fieldCarriesCredential(pfBaseURL, raw) {
			return s.secretMessage()
		}
	case pfAPIKeyEnv:
		if env := s.trimAPIKeyEnv(); env != "" && providers.LooksLikeAPIKey(env) {
			return missingOr(s.b, "providers.validation.api_key_env_secret", missingProviderFormAPIKeyEnvSecret)
		}
	}
	return ""
}

// secretMessage is the value-free refusal for a credential-shaped name or base
// URL. It names no part of the operand: the same sentence serves both fields, so
// the message cannot hint at what was pasted.
func (s *providerFormScreen) secretMessage() string {
	return missingOr(s.b, "providers.validation.secret", missingProviderFormValueSecret)
}

// fieldCarriesCredential reports whether a grid value carries credential
// material rather than the name or endpoint it is meant to hold. FIX-A: without
// this, a misdirected paste of key material passes the charset and transport
// validators, renders MASKED in the grid and in the M4 diff ("+ name: sk-li****"),
// and is then persisted RAW, which is the one thing providers.json must never
// hold.
//
// The predicate is the field's own shared authority, not a new heuristic:
//   - pfName: providers.NameCarriesCredential is the ONE ingest authority for a
//     name slot (FIX-7A). It is the union of providers.SecretShaped, the shared
//     operand masker (providers.MaskOperand), and the registry-slot authority
//     the grid renders that row through (providers.MaskSlot), so "the frame
//     masks it" and "the form refuses it" stay one rule. The slot authority is
//     the arm that catches an underscore-head key: the five planted fixtures
//     (testfixtures.SKLive, TokSample, PKLive, KeyHex, AKIAUnderscore) carry no
//     head in slotSecretPrefixes and no uninterrupted run (the "_" breaks it),
//     so the first two arms are the identity on all five while the 16+
//     alphanumeric segment behind the separator masks. The union is deliberately
//     not an enumerated prefix list: a list would refuse legal names such as
//     "key_backup" and would reopen on the next unlisted head.
//   - pfBaseURL: providers.BaseURLCarriesCredential, the ONE URL ingest
//     authority (FIX-8). It classifies the whole value (a bare key pasted into
//     the URL slot, a decorated secret, a JWT), the host labels, the userinfo
//     username and password, every path segment plus the Opaque spelling, every
//     query key and value, and the fragment. The form keeps no local part rule,
//     so this door, the setup wizard, and the add-json parser cannot drift.
//     The earlier form arm compared providers.MaskBaseURL(v) against v, which
//     drops ANY userinfo for display and therefore refused a benign
//     "https://user:pw@host/v1" that both other doors accept (FIX-8h); the
//     shared function states the userinfo rule instead of leaning on a display
//     layer's side effect. MaskBaseURL and the screen's maskBaseURL keep their
//     display behavior unchanged.
//
// Benign values stay identity under every arm: every name in the provider-name
// charset and every URL the transport rule accepts keeps its exact ingest
// verdict (the form pins assert exactly that), and a benign userinfo URL is now
// accepted here exactly as at the wizard and the CLI.
func fieldCarriesCredential(sem int, v string) bool {
	switch sem {
	case pfName:
		return providers.NameCarriesCredential(v)
	case pfBaseURL:
		return providers.BaseURLCarriesCredential(v)
	}
	return false
}

// dirty reports whether the form differs from its construction values.
func (s *providerFormScreen) dirty() bool {
	cur := s.form.Snapshot()
	if len(cur) != len(s.base) {
		return true
	}
	for i := range cur {
		if cur[i] != s.base[i] {
			return true
		}
	}
	return false
}

// HandleKey runs one key with no modal live: dispatch it, then arm the dirty
// guard. Esc never arrives here (App precedence).
func (s *providerFormScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.dispatch(k)
	if s.dirty() && a.Modal() == nil {
		s.rearmGuard(a)
	}
}

// dispatch routes one key by zone: field grid, read-only defaults block, Save
// row. Enter on the Save row opens the M4 approval; Enter never writes.
func (s *providerFormScreen) dispatch(k Key) {
	s.errMsg = ""
	switch s.zone {
	case providerFormZoneSave:
		switch k {
		case KeyUp, KeyLeft:
			s.zone = providerFormZoneDefaults
		case KeyEnter:
			s.trySave()
		}
		return
	case providerFormZoneDefaults:
		s.handleDefaults(k)
		return
	}
	s.handleGrid(k)
}

// handleGrid forwards to the field grid. Tab, Down, or a VALID Enter on the last
// field moves the focus into the read-only defaults block (the preset editor's
// zone-A rule). A failing Enter keeps the focus on the field it refused, so the
// message line points at the repair site.
func (s *providerFormScreen) handleGrid(k Key) {
	last := s.form.Len() - 1
	before := s.form.Cursor()
	if (k == KeyTab || k == KeyDown) && before == last {
		s.zone = providerFormZoneDefaults
		return
	}
	s.form.HandleKey(k)
	if k == KeyEnter && before == last && s.form.Message() == "" {
		s.zone = providerFormZoneDefaults
	}
}

// handleDefaults routes keys inside the read-only defaults block. The shared
// stepList moves the window (arrows, PgUp/PgDn, Home/End); its activation
// callback is nil, so Enter and digits can only move the cursor, never mutate
// anything. Tab moves on to the Save row; Down on the last row and Up on the
// first row cross the block boundary.
func (s *providerFormScreen) handleDefaults(k Key) {
	switch k {
	case KeyTab, KeyEnter:
		s.zone = providerFormZoneSave
		return
	case KeyUp:
		if s.defList.Cursor() == 0 {
			s.zone = providerFormZoneGrid
			s.form.SetCursor(s.form.Len() - 1)
			return
		}
	}
	before := s.defList.Cursor()
	s.defList.HandleKey(k)
	if k == KeyDown && before == s.defList.Len()-1 {
		s.zone = providerFormZoneSave
	}
}

// rearmGuard (re)opens the dirty guard. Callers run inside a modal key
// callback, so the App stale-modal clear never removes a freshly armed one.
func (s *providerFormScreen) rearmGuard(a *App) {
	if !s.dirty() {
		return
	}
	if s.guard == nil {
		s.guard = &providerFormGuard{s: s}
	}
	a.ShowModal(s.guard)
}

// askDiscard opens the discard-confirm over a dirty form. y restores the
// construction values (Snapshot/Restore) and pops back to the caller; n/Esc
// returns to the form with field state preserved and the guard re-armed.
func (s *providerFormScreen) askDiscard() {
	a := s.app
	if a == nil {
		return
	}
	a.ShowModal(NewConfirm(
		missingOr(s.b, "providers.form.discard_title", missingProviderFormDiscardTitle),
		[]string{missingOr(s.b, "providers.form.discard_body", missingProviderFormDiscardBody)},
		func(ok bool) {
			if !ok {
				s.rearmGuard(a)
				return
			}
			s.form.Restore(s.base)
			s.errMsg = ""
			a.Pop()
			a.SetMessage(missingOr(s.b, "providers.form.cancelled", missingProviderFormCancelled))
		}))
}

// trySave validates every field, then opens the M4 Approval modal with the exact
// mutation summary. Enter here does NOT write; only y reaches the Core verbs.
func (s *providerFormScreen) trySave() {
	a := s.app
	if a == nil {
		return
	}
	if s.loadErr != "" {
		// The load/not-found line is already at the top of the body; the save
		// path refuses here so a corrupt or unknown-target form can never open an
		// approval that would write.
		return
	}
	for i := 0; i < s.form.Len(); i++ {
		f := s.form.Field(i)
		if f.Validate == nil {
			continue
		}
		if err := f.Validate(s.form.Value(i)); err != nil {
			s.errMsg = err.Error()
			// Leave the Save row and focus the first failing field so the error
			// points at the repair site (the model form's round-2 F3 rule).
			s.zone = providerFormZoneGrid
			s.form.SetCursor(i)
			return
		}
	}
	summary := s.approvalSummary()
	a.ShowModal(NewApproval(s.b.T("modal.approval.title"), summary, func(ok bool) {
		if !ok {
			// A declined approval leaves the edited values untouched, so the
			// form stays dirty and the Esc gate stays armed.
			a.SetMessage(missingOr(s.b, "providers.form.cancelled", missingProviderFormCancelled))
			s.rearmGuard(a)
			return
		}
		s.commit(a)
	}))
}

// approvalSummary composes the M4 diff. Add mode signs the new record's three
// fields; edit mode signs the two editable fields against the form-open snapshot
// (changed fields only, or the no-changes marker), plus the model count, which
// an edit never moves. Add mode reuses the settings screen's add sentence and
// the shared counts/atomic-write templates, so the two in-TUI add paths present
// the same shape.
func (s *providerFormScreen) approvalSummary() []string {
	counts := func(before, after int) string {
		return s.b.Tf("modal.approval.counts", map[string]string{
			"before": strconv.Itoa(before),
			"after":  strconv.Itoa(after),
		})
	}
	if s.mode == providerFormModeAdd {
		summary := []string{
			fillHubSlots(missingOr(s.b, "settings.approval.add", settingsApprovalAdd), map[string]string{
				"name": providers.MaskSlot(s.trimName()),
			}),
			// An add carries no models, so the count is 0 -> 0: the line is the
			// same shape the settings screen's add presents, and it states
			// honestly that the model list is empty.
			counts(0, 0),
			s.b.T("modal.approval.body"),
		}
		return append(summary, s.addedDiffLines()...)
	}
	summary := []string{
		fillHubSlots(missingOr(s.b, "providers.form.approval.edit", missingProviderFormApprovalEdit), map[string]string{
			// The edit target is an ON-DISK name (FIX-7A): a hand-planted
			// key-shaped name is the identity under MaskOperand, so the approval
			// frame the operator signs would have shown it raw. MaskSlot is the
			// registry-slot authority the fixed-name line and the name row use.
			"name": providers.MaskSlot(s.recordName()),
		}),
		counts(len(s.orig.Models), len(s.orig.Models)),
		s.b.T("modal.approval.body"),
	}
	return append(summary, s.changedDiffLines()...)
}

// addedDiffLines renders one "+ field: value" line per written field. The name
// and the base URL mask like the grid; the env NAME masks through the
// registry-slot authority, and an empty env renders the catalog "(empty)" marker
// so the operator can see that no key variable is declared.
func (s *providerFormScreen) addedDiffLines() []string {
	lines := []string{}
	add := func(field, value string) {
		lines = append(lines, fillHubSlots(missingOr(s.b, "providers.form.diff_added", missingProviderFormDiffAdded), map[string]string{
			"field": field,
			"value": value,
		}))
	}
	add("name", providers.MaskSlot(s.trimName()))
	add("baseURL", maskBaseURL(s.trimBaseURL()))
	add("apiKeyEnv", s.shownEnv(s.trimAPIKeyEnv()))
	return lines
}

// changedDiffLines renders one "  field: before -> after" line per changed
// field. Emission compares the trimmed form value against the raw on-disk value,
// so a value that differs only by padding still signs (the write trims, so the
// persisted bytes move); rendering masks both operands.
func (s *providerFormScreen) changedDiffLines() []string {
	lines := []string{}
	// sign emits when the RAW operands differ and renders the masked forms of
	// both; comparing the masked forms instead would let two credentials that
	// share their first five runes emit nothing while the write still moves.
	sign := func(field, beforeRaw, afterRaw, beforeShown, afterShown string) {
		if beforeRaw == afterRaw {
			return
		}
		lines = append(lines, fillHubSlots(missingOr(s.b, "providers.form.diff_changed", missingProviderFormDiffChanged), map[string]string{
			"field":  field,
			"before": beforeShown,
			"after":  afterShown,
		}))
	}
	base := s.trimBaseURL()
	sign("baseURL", s.orig.BaseURL, base, maskBaseURL(s.orig.BaseURL), maskBaseURL(base))
	env := s.trimAPIKeyEnv()
	sign("apiKeyEnv", s.orig.APIKeyEnv, env, s.shownEnv(s.orig.APIKeyEnv), s.shownEnv(env))
	if len(lines) == 0 {
		lines = append(lines, missingOr(s.b, "providers.form.diff_none", missingProviderFormDiffNone))
	}
	return lines
}

// shownEnv renders an apiKeyEnv operand for a diff line: the registry-slot mask,
// or the catalog "(empty)" marker when no variable is declared.
func (s *providerFormScreen) shownEnv(v string) string {
	if v == "" {
		return s.b.T("common.empty")
	}
	return maskSlotOperand(v)
}

// validateName mirrors the twin's provider-name rule: required, the closed
// shell-safe charset (providers.ValidateProviderName, the same call
// settings.ParseProviderSpecJSON makes at spec.go:45), and uniqueness in add
// mode. Uniqueness is a form rule, not a twin rule: the twin's write path
// (UpsertProvider, registry_ops.go:118-124) REPLACES a case-insensitive
// same-named entry, which from an Add row would silently drop that provider's
// model list. The refusal names the reason instead.
//
// The secrets guard runs ahead of the charset for the reason the model form
// gives (validateEffort): the charset happens to admit every bare alnum key ID
// (testfixtures.AKIAExample, or an "sk-live-..." shaped value), so a pasted
// value would otherwise be refused as a naming problem when the real problem is
// that it is a key. The refusal names the class and echoes nothing.
func (s *providerFormScreen) validateName(v string) error {
	name := strings.TrimSpace(v)
	if name == "" {
		return errors.New(missingOr(s.b, "providers.validation.name_required", missingProviderFormNameRequired))
	}
	if fieldCarriesCredential(pfName, name) {
		return errors.New(s.secretMessage())
	}
	if err := providers.ValidateProviderName(name); err != nil {
		return errors.New(missingOr(s.b, "providers.validation.name", missingProviderFormNameInvalid))
	}
	if s.nameTaken(name) {
		return errors.New(s.dupMessage(name))
	}
	return nil
}

// nameTaken reports whether the loaded registry already holds name under case
// folding (the rule UpsertProvider replaces by).
func (s *providerFormScreen) nameTaken(name string) bool {
	return findProviderCI(s.reg, name) != nil
}

// dupMessage renders the duplicate-name refusal. The name is a registry slot, so
// it routes through the registry-slot authority (providers.MaskSlot) like every
// other name echo on this screen; the bare operand masker is the identity on the
// underscore-head key class (FIX-7A). The template resolves through missingOr +
// fillHubSlots: the key is looked up with the const as the fallback, then its
// {{name}} slot is filled from the resolved template (Tf takes a template as a
// lookup KEY, so it could never reach the catalog entry).
func (s *providerFormScreen) dupMessage(name string) string {
	return fillHubSlots(
		missingOr(s.b, "providers.validation.name_exists", missingProviderFormNameExists),
		map[string]string{"name": providers.MaskSlot(name)})
}

// errProviderFormAmbiguous marks the edit-target ambiguity class; the caller
// maps it onto a catalog sentence.
var errProviderFormAmbiguous = errors.New("provider form: edit target is ambiguous")

// resolveProviderFormTarget locates the record an edit applies to. The identity
// rule is NOT restated here: it delegates to settings.ResolveProviderEditTarget,
// the exported resolver Core.ProviderEdit itself calls, so the form's prefill can
// never address a different record than the verb that writes. An exact-case match
// wins outright (the registry cannot hold two of those), a case-folded match is
// accepted only when it is the single one, and anything else answers nil (not
// found) or nil with errProviderFormAmbiguous. Prefilling a guess here would show
// one record while the verb refuses (or edits another), which is the wrong-record
// credential-routing hazard the verb's own comment documents.
func resolveProviderFormTarget(reg *providers.Registry, name string) (*providers.Provider, error) {
	if reg == nil {
		return nil, nil
	}
	idx, err := settings.ResolveProviderEditTarget(reg, name)
	if err != nil {
		if errors.Is(err, settings.ErrProviderEditAmbiguous) {
			return nil, errProviderFormAmbiguous
		}
		// Not found (including an empty operand): the caller renders the honest
		// missing state and refuses every save.
		return nil, nil
	}
	if idx < 0 || idx >= len(reg.Providers) {
		return nil, nil
	}
	return &reg.Providers[idx], nil
}

// validateBaseURL mirrors the twin's transport rule without copying it: the
// registry's own load-time validator (providers.Registry.Validate ->
// validateBaseURL, providers.go:296-331 and 382-390) is the single authority
// Core.ProviderEdit and UpsertProvider both end in. The rule is applied to a
// minimal probe record with a constant benign name and no key env, so the ONLY
// rule that can refuse it is the base-URL rule: a name or env fault can never
// mask the verdict, and no local copy of the scheme/host logic exists to drift.
//
// The secrets guard runs first: a key pasted into this field can render as a
// perfectly valid endpoint shape ("https://host/v1/sk-live-...", or a key that
// sits in a PATH component and is invisible to the shared maskers), so the
// transport rule would accept it and the form would persist a VALUE. The
// transport rule itself is unchanged (a hostless https:// is still accepted, as
// the twin and the parity pin require).
func (s *providerFormScreen) validateBaseURL(v string) error {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return errors.New(missingOr(s.b, "providers.validation.base_url_required", missingProviderFormBaseURLRequired))
	}
	if fieldCarriesCredential(pfBaseURL, raw) {
		return errors.New(s.secretMessage())
	}
	if providerBaseURLRefused(raw) {
		return errors.New(missingOr(s.b, "providers.validation.base_url", missingProviderFormBaseURLInvalid))
	}
	return nil
}

// providerBaseURLRefused asks the registry's load-time validator whether a lone
// record carrying raw is acceptable.
func providerBaseURLRefused(raw string) bool {
	probe := &providers.Registry{Providers: []providers.Provider{{Name: "probe", BaseURL: raw}}}
	return probe.Validate() != nil
}

// validateAPIKeyEnv mirrors the twin's apiKeyEnv rule by delegation:
// providers.ValidateAPIKeyEnvName is the exact guard settings.ParseProviderSpecJSON
// runs (spec.go:34-38) and Core.ProviderEdit re-runs (core.go:374-378). An empty
// value is accepted: the twin treats the field as optional (spec.go:33), and an
// empty env means the provider needs no key. The refusal never echoes the value;
// providers.LooksLikeAPIKey only PICKS the message (key material vs. an
// unprovable name), it never decides validity.
func (s *providerFormScreen) validateAPIKeyEnv(v string) error {
	env := strings.TrimSpace(v)
	if env == "" {
		return nil
	}
	if err := providers.ValidateAPIKeyEnvName(env); err != nil {
		if providers.LooksLikeAPIKey(env) {
			return errors.New(missingOr(s.b, "providers.validation.api_key_env_secret", missingProviderFormAPIKeyEnvSecret))
		}
		return errors.New(missingOr(s.b, "providers.validation.api_key_env", missingProviderFormAPIKeyEnvInvalid))
	}
	return nil
}

// errProviderFormDiskCheck is the "the re-read failed, so the check could not
// run" refusal, shared by both commit gates: the add-mode duplicate check
// (FIX-F) and the edit-mode stale comparison (FIX-I). It carries no operand: the
// load error text may name a path, and the form already renders the class
// without it.
var errProviderFormDiskCheck = errors.New("provider form: registry could not be re-read")

// nameExistsOnDisk reloads the registry and reports whether name is already
// there. It closes the window between form-open validation and the approved
// write: an external add of the same name would otherwise turn this form's
// ProviderAdd into a silent replace (UpsertProvider semantics).
//
// A load failure now fails CLOSED (FIX-F): the old "report false" answer widened
// the duplicate-replace window to every transient I/O or corrupt-file window,
// because false is indistinguishable from "the name is free". The caller refuses
// the add with a named message and writes nothing. A missing registry is not a
// failure: pre-setup is the normal add-mode state (providers.Load answers
// os.ErrNotExist, the same rule Core.loadRegistry and NewProviderFormScreen
// apply), so the first provider can still be created into an empty home.
func (s *providerFormScreen) nameExistsOnDisk(name string) (bool, error) {
	reg, err := providers.Load()
	switch {
	case err == nil:
		return findProviderCI(reg, name) != nil, nil
	case errors.Is(err, os.ErrNotExist), errors.Is(err, providers.ErrNotConfigured):
		return false, nil
	default:
		return false, errProviderFormDiskCheck
	}
}

// baseOnDiskState reloads the registry and reports the edit gate's verdict plus
// the on-disk record it compared, so the caller can answer the no-op question
// from the SAME read. changed is true (the write must be refused) when the
// provider vanished, when the name no longer resolves to exactly one record, or
// when either signed field drifted from the form-open snapshot. Core.ProviderEdit
// documents that this re-check is the front-end's duty (core.go:353-359): the
// verb reloads and writes last-writer-wins, so without this gate an external edit
// made while the approval was open would be silently reverted.
//
// The target resolves through settings.ResolveProviderEditTarget, the SAME
// resolver the verb uses (FIX-B). Resolving with a first-case-insensitive match
// (findProviderCI) was wrong on a legal case-duplicate registry ("acme" plus
// "ACME", which Registry.Validate permits): it compared the snapshot against
// whichever duplicate sat earlier, so it either refused a legal edit or, when the
// duplicate mirrored the snapshot's endpoint fields, passed this gate while the
// verb wrote over the real target, silently reverting the concurrent change. An
// ambiguity (or any other resolver refusal) now reports changed, i.e. it fails
// closed exactly where the verb refuses.
//
// A pure load error now fails CLOSED (FIX-I): the old (false, nil) answer skipped
// the drift comparison entirely, so an approved write landed with no stale check
// at all and could revert a concurrent change inside every transient I/O or
// corrupt-file window. The error is the add-mode gate's sentinel, so the caller
// refuses with the same named, value-free message and writes nothing.
func (s *providerFormScreen) baseOnDiskState() (bool, *providers.Provider, error) {
	if s.mode != providerFormModeEdit || !s.origFound {
		return false, nil, nil
	}
	reg, err := providers.Load()
	if err != nil {
		return false, nil, errProviderFormDiskCheck
	}
	idx, err := settings.ResolveProviderEditTarget(reg, s.recordName())
	if err != nil {
		// Not found, ambiguous, or empty: all three are refusals from the verb,
		// so the gate refuses too rather than writing to a guessed record.
		return true, nil, nil
	}
	if idx < 0 || idx >= len(reg.Providers) {
		return true, nil, nil
	}
	p := &reg.Providers[idx]
	return p.BaseURL != s.orig.BaseURL || p.APIKeyEnv != s.orig.APIKeyEnv, p, nil
}

// commit runs the approved write and pops back to the caller with the success
// message. This is the ONLY write path out of the form. Add mode refuses a name
// that appeared on disk while the approval was open (a replace, not an add) and
// refuses when that check could not run at all; edit mode refuses a record that
// moved on disk (the stale-base gate) and refuses when that comparison could not
// run (FIX-I).
func (s *providerFormScreen) commit(a *App) {
	if s.mode == providerFormModeAdd {
		name := s.trimName()
		exists, err := s.nameExistsOnDisk(name)
		if err != nil {
			a.SetMessage(s.b.T("common.error") + ": " + missingOr(s.b, "providers.form.disk_check_failed", missingProviderFormDiskCheckFailed))
			s.rearmGuard(a)
			return
		}
		if exists {
			a.SetMessage(s.b.T("common.error") + ": " + s.dupMessage(name))
			s.rearmGuard(a)
			return
		}
		err = s.core.ProviderAdd(settings.ProviderSpec{
			Name:      name,
			BaseURL:   s.trimBaseURL(),
			APIKeyEnv: s.trimAPIKeyEnv(),
		})
		if err != nil {
			a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
			s.rearmGuard(a)
			return
		}
		a.Pop()
		// The success sentence opens with the localized common.success label so
		// the message line paints the success role (user decision D7-7B); the
		// error paths above prefix common.error the same way.
		a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(
			missingOr(s.b, "providers.form.saved_add", missingProviderFormSavedAdd),
			map[string]string{"name": providers.MaskSlot(name)}))
		return
	}
	changed, onDisk, gateErr := s.baseOnDiskState()
	if gateErr != nil {
		// FIX-I: the stale comparison could not run, so the write must not land
		// unchecked. Same named, value-free refusal the add-mode gate uses.
		a.SetMessage(s.b.T("common.error") + ": " + missingOr(s.b, "providers.form.disk_check_failed", missingProviderFormDiskCheckFailed))
		s.rearmGuard(a)
		return
	}
	if changed {
		a.SetMessage(s.b.T("common.error") + ": " + missingOr(s.b, "providers.form.stale_base", missingProviderFormStaleBase))
		s.rearmGuard(a)
		return
	}
	base, env := s.trimBaseURL(), s.trimAPIKeyEnv()
	// FIX-E: an approved edit that changes nothing must not claim a write.
	// Core.ProviderEdit performs NO write when the approved values equal the
	// record's (idempotence is pinned), so the success sentence is chosen from the
	// record this gate just read: the verb re-loads and applies exactly this
	// predicate (core.go:380), and the gates above already proved the record still
	// carries the snapshot. onDisk is nil only when the form never resolved the
	// record at open (origFound false: the verb then refuses with its own error),
	// so the conservative choice there is the write sentence.
	unchanged := onDisk != nil && onDisk.BaseURL == base && onDisk.APIKeyEnv == env
	if err := s.core.ProviderEdit(s.recordName(), base, env); err != nil {
		a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
		s.rearmGuard(a)
		return
	}
	a.Pop()
	sentence := missingProviderFormSavedEdit
	key := "providers.form.saved_edit"
	if unchanged {
		sentence = missingProviderFormUnchangedEdit
		key = "providers.form.unchanged_edit"
	}
	a.SetMessage(s.b.T("common.success") + ": " + fillHubSlots(
		missingOr(s.b, key, sentence),
		map[string]string{"name": providers.MaskSlot(s.recordName())}))
}

// providerFormGuard is the transparent dirty-form gate (the model form's
// formGuard, for this screen). While it is armed (the form is dirty) it receives
// every key in CONFIRM scope before the App Esc precedence: it forwards
// everything except Esc to the form, and Esc on a dirty form opens the
// discard-confirm. Its body carries the form hint, so the armed gate doubles as
// the form footer.
type providerFormGuard struct {
	s *providerFormScreen
}

func (g *providerFormGuard) Title() string { return "" }

func (g *providerFormGuard) Body(w int) []string {
	return []string{missingOr(g.s.b, "providers.form.hint", missingProviderFormHint)}
}

func (g *providerFormGuard) Footer() string { return "" }

// Key forwards one key. Esc on a dirty form opens the discard-confirm and keeps
// the gate; Esc on a clean form closes the gate (the Esc is consumed, and the
// App pop happens on a further Esc, plain precedence).
func (g *providerFormGuard) Key(k Key, a *App) bool {
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
