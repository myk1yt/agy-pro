package tui

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// English fallbacks for catalog keys this screen resolves. Every key named
// below carries an entry in all twelve locale files, so a lookup never has to
// fall back to English or to the key ID, and each const stays as the fallback
// for a catalog lacking the translation. Every lookup passes the KEY named in
// the const's own comment, never the const text itself: Bundle.T and Tf are
// literal-name lookups, so handing one of them a fallback sentence reaches no
// catalog entry and prints English in every locale.
// TestProvidersFallbackConstsResolveInAllLocales gates both halves of that
// claim (entry present per locale, key never handed back).
const (
	// catalog-resolved, fallback kept: providers.action.test_all
	providersActionTestAll = "Test all"
	// catalog-resolved, fallback kept: providers.test.all_summary
	providersTestAllSummary = "{{ok}}/{{total}} providers OK"
	// catalog-resolved, fallback kept: providers.delete.done
	providersDeleteDone = "Provider {{name}} deleted."
	// catalog-resolved, fallback kept: providers.delete.canceled
	providersDeleteCanceled = "Delete canceled. providers.json unchanged."
	// catalog-resolved, fallback kept: providers.form.add
	providersFormAdd = "Add provider"
	// catalog-resolved, fallback kept: providers.form.edit
	providersFormEdit = "Edit"
	// catalog-resolved, fallback kept: providers.form.empty_hint
	providersFormEmptyHint = `Use the "{{action}}" row below to register one.`
	// catalog-resolved, fallback kept: providers.msg.select_provider
	providersMsgSelectProvider = "Select a provider row first."
)

// providersListChrome is the non-list line count of the composed frame around
// the provider rows: the screen title, its separator, the blank and the list
// hint under the rows, the separator above the base footer, and the base
// footer itself. A live app message is its own block under the body (a blank
// separator plus the line) and the composer appends it after the body, so Draw
// folds two more rows into this count. Without that fold a message frame cut
// the list hint or the base footer off the frame; the presets screen carries
// the same reservation (presetsListChrome plus two).
const providersListChrome = 6

// The answer-path refusal line and its reason phrases. English consts with no
// catalog keys: decision 6B keeps the shared chrome English this round, so the
// refusal joins the fallback constants above instead of the catalog. The
// provider name slot routes through MaskSlot (the one registry-slot display
// authority) and any load detail through MaskOperand; the reason phrases
// themselves carry no registry content.
const (
	providersDeleteRefused = "Refused to delete {{name}}: {{reason}}. Nothing was written; reopen the delete to review the current entry."
	// providersDeleteReasonUnreadable prefixes a re-read failure; the masked
	// load detail follows it.
	providersDeleteReasonUnreadable = "providers.json could not be re-read"
	// providersDeleteReasonGone: the approved name is not in the re-read file.
	providersDeleteReasonGone = "the approved entry is no longer in providers.json"
	// providersDeleteReasonAmbiguous: Core's delete removes the first
	// case-insensitive name match, so a differently-cased entry sorting first
	// would be a different record than the one the modal showed.
	providersDeleteReasonAmbiguous = "a differently-cased entry now matches this name first"
	// providersDeleteReasonChanged: same name, different record body.
	providersDeleteReasonChanged = "the approved entry changed while the approval was open"
)

// Row IDs for the action rows. Provider rows carry the NUL-prefixed
// "provider\x00" scheme (same convention as the models screen), and the fixed
// action rows carry "add\x00", "test\x00", "edit\x00" or "delete\x00". A
// NUL-terminated kind prefix makes collisions impossible for ANY provider
// name: two row IDs are equal only when both the kind prefix and the name are
// equal, so a hand-edited name such as "delete:x" or "test-all" can never
// shadow another row. The load-time registry validation does not enforce the
// name charset, so this screen must not depend on it. Every action row carries
// an empty operand now that the verbs act on the sticky-selected provider
// (user ask U2), and the prefix keeps them in the same scheme (the literal
// "test-all" stays exempt because it is compared by exact equality before any
// prefix test).
const (
	providersRowIDTestAll = "test-all"
	providerRowPrefix     = "provider\x00"
	providersRowIDAdd     = "add\x00"
	providersRowIDTest    = "test\x00"
	providersRowIDEdit    = "edit\x00"
	providersRowIDDelete  = "delete\x00"
)

// ProvidersScreen is S2: the providers list. One row per registered provider
// (Enter opens its models screen), then the registry-level verbs "Add provider"
// and "Test all", then the three sticky-target verbs Test, Edit, and Delete
// (delete is danger, gated by the M4 approval modal). The three verbs act on the
// provider the cursor last rested on, so the row set no longer grows with the
// registry (user ask U2; the models screen's withCursorModel pattern). Add and
// edit open the provider form through the shell callback (decision 1A:
// registration happens inside the TUI, the CLI/wizard is no longer the only
// door); this screen itself never writes providers.json except through the
// approval modal behind Core.ProviderDelete.
//
// The sticky selection is held by PROVIDER NAME, not by index (the models and
// presets screens' by-ID convention, code review W1): the registry is rewritten
// whole by other surfaces (this screen's own delete, the form above it, another
// session), so a raw index re-clamped after a reload can point at a DIFFERENT
// provider than the user selected. A name survives a reorder and, when the
// provider is gone, resolves to nothing: the marker clears and the verbs refuse
// with the select-a-provider message instead of silently hitting a neighbour.
//
// The selection has two modes. By default it FOLLOWS THE CURSOR: every key that
// rests the cursor on a provider row re-asserts that row (user ask U2). That
// rule alone made the verbs unreachable for arrow-only users, because the verb
// rows sit BELOW the whole provider block: travelling down to Edit/Delete
// passes every remaining provider row and each rest re-stole the target, so the
// verb always hit the LAST provider. Space therefore PINS a row explicitly
// (locked), and the cursor-reassert is suppressed until the pin is consumed
// (selectAt, HandleKey, pick).
type ProvidersScreen struct {
	core       *settings.Core
	openModels func(providerName string, a *App)
	openForm   func(mode, providerName string, a *App)
	bundle     *i18n.Bundle
	list       *stepList
	rows       []Row // row inventory; shared with the stepList for in-place marker writes
	reg        *providers.Registry
	loadErr    error
	sig        string // registry signature the rows were built from
	selName    string // sticky-selected provider NAME: the identity the verbs resolve
	sel        int    // derived marker index of selName in reg.Providers, -1 when none
	locked     bool   // Space pinned selName: the cursor reassert must not overwrite it
	app        *App   // set by HandleKey; the list callback needs it
}

// NewProvidersScreen builds the providers list over core. openModels is the
// shell callback that pushes the per-provider models screen, and openForm is
// the provider form factory the Add row and the sticky-target Edit row call:
// mode is the form screen's own "add" / "edit" contract, and providerName is
// empty in add mode (NewProviderFormScreen(core, mode, providerName), owned by
// the form unit). Tests inject spies. The sticky selection starts at -1 (user
// decision 2A, the models and presets screens' rule): entering the screen and
// firing an action verb without first resting the cursor on a provider row
// refuses with the select-a-provider message instead of silently targeting
// provider[0].
func NewProvidersScreen(core *settings.Core, openModels func(providerName string, a *App), openForm func(mode, providerName string, a *App)) Screen {
	s := &ProvidersScreen{
		core:       core,
		openModels: openModels,
		openForm:   openForm,
		bundle:     Bundle(),
		sel:        -1,
	}
	s.list = newStepList(s.pick)
	s.list.SetOnSelect(s.selectAt)
	s.reload()
	return s
}

// pushProvidersScreen opens the providers screen with the standard factory
// wiring (the hub row inlines the same callbacks at app.go). The settings grid
// routes its retired typed-name test/delete rows here so both actions live on
// one row-picked surface instead of asking for a typed name (survey-1 debt
// item 18).
func pushProvidersScreen(core *settings.Core, a *App) {
	a.Push(NewProvidersScreen(core, func(name string, app *App) {
		app.Push(NewModelsScreen(core, name, func(modeName, modelID string, app2 *App) {
			app2.Push(NewModelFormScreen(core, name, modeName, modelID))
		}))
	}, func(modeName, providerName string, app *App) {
		app.Push(NewProviderFormScreen(core, modeName, providerName))
	}))
}

// Title returns the screen title.
func (s *ProvidersScreen) Title() string { return s.bundle.T("providers.screen.title") }

// Mode is ModeNav: every list on this screen goes through the shared
// stepList, so digits and arrows are both always live.
func (s *ProvidersScreen) Mode() Mode { return ModeNav }

// Draw renders the list window plus the two-handled legend. An empty
// registry renders the honest empty line AND the Add row under it, so the
// pointer at the in-TUI add path and its target are never separated; a load
// failure renders the error alone. The window is sized against the real chrome
// of this frame: the constant providersListChrome (title, both section
// separators, the blank and the list hint under the rows, and the base footer)
// plus the two rows a live app message adds (its own separator and the line),
// so a message frame neither cuts the list tail nor drops the base footer.
// Every one-off line is width-capped like the models and presets screens'
// equivalents, so an over-wide path in a load error cannot compose past the
// frame edge. The registry is re-read behind a signature check first, so a
// provider added or edited on the form above this screen (or mutated behind
// its back) renders on the next frame instead of on the next visit.
func (s *ProvidersScreen) Draw(a *App) []string {
	s.reloadIfChanged()
	chrome := providersListChrome
	if a.Message() != "" {
		chrome += 2
	}
	s.list.SetPageSize(pageSizeFor(a.Height(), chrome))
	var lines []string
	switch {
	case s.loadErr != nil:
		lines = append(lines, cellwidth.Truncate(
			s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(s.loadErr.Error())}), a.Width()))
	case len(s.reg.Providers) == 0:
		lines = append(lines, cellwidth.Truncate(providersEmptyLine(s.bundle), a.Width()), "")
		lines = append(lines, s.list.Render(a.Width())...)
	default:
		lines = s.list.Render(a.Width())
	}
	return append(lines, "", s.list.FooterHint())
}

// providersEmptyLine composes the empty-registry line: the catalog's empty
// notice plus a pointer at the Add row, with the action name interpolated so
// the pointer names the row by the label the row actually carries (decision
// 1A made the in-TUI add path the primary one, so the pre-1A text, which named
// only the registry's emptiness, was a dead end). The hint sentence resolves
// providers.form.empty_hint through missingOr with the const as the fallback,
// then fills the {{action}} slot with fillHubSlots: Tf would take a template
// as the lookup KEY and never reach the catalog entry, so the slot-free
// notice resolves through T above and the templated sentence goes through the
// missingOr plus Fill pair the hub summary and the provider form's slot
// templates use.
func providersEmptyLine(b *i18n.Bundle) string {
	action := missingOr(b, "providers.form.add", providersFormAdd)
	hint := fillHubSlots(missingOr(b, "providers.form.empty_hint", providersFormEmptyHint),
		map[string]string{"action": action})
	return b.T("providers.empty") + " " + hint
}

// HandleKey routes one key to the shared stepList and keeps the sticky provider
// selection in sync while the cursor sits on a provider row; when the selection
// moves, the marker follows in place (syncMarker), so the list window never
// resets. Every key that reaches this screen passes here, so an arrow key that
// does not move the cursor still re-asserts the selection of the row it rests
// on. The app pointer is kept for the activation callback, which needs it to
// open screens, set messages, and show the approval modal.
//
// The follow-on-rest rule is exactly what a Space pin has to escape (see the
// type doc), so the reassert is gated by s.locked: while the pin holds, arrow
// travel over other provider rows leaves the target alone, and the stepList
// consumes Space before this block runs (selectAt sets the pin).
func (s *ProvidersScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
	if s.locked {
		return
	}
	if c := s.list.Cursor(); c >= 0 && c < len(s.reg.Providers) {
		name := s.reg.Providers[c].Name
		if s.sel != c || s.selName != name {
			s.sel, s.selName = c, name
			s.syncMarker()
		}
	}
}

// selectAt is the stepList Space callback: the explicit pin. Provider rows
// occupy indices 0..len(reg.Providers)-1 and the verb rows (Add, Test all, Test,
// Edit, Delete) sit below them, so the boundary IS the row index: a Space on a
// verb row is a consumed no-op that changes no state, while a Space on an entity
// row sets the sticky target, locks it, moves the marker in place and confirms
// on the message line. Pinning again on another (or the same) row simply
// re-pins: the lock stays on and the follow-on-rest rule stays suspended. Space
// never activates anything: this callback never reaches pick, so a pin can never
// open the models screen, the form, or the delete gate.
func (s *ProvidersScreen) selectAt(idx int) {
	if idx < 0 || idx >= len(s.reg.Providers) {
		return
	}
	name := s.reg.Providers[idx].Name
	s.sel, s.selName = idx, name
	s.locked = true
	s.syncMarker()
	if s.app != nil {
		s.app.SetMessage(fillHubSlots(missingOr(s.bundle, "list.msg.pinned", listMsgPinned),
			map[string]string{"name": providers.MaskSlot(name)}))
	}
}

// syncMarker moves the visible selection marker to the sticky-selected provider
// row by rewriting the row labels IN PLACE (the models screen's F-S1 pattern):
// s.rows and the stepList share the same backing array (SetRows stores the slice
// header), so the label writes are picked up by the next Render without a
// SetRows call, which would reset the scroll window and make the view jump on
// every selection move. Labels are recomputed from the row ID (the slot masker
// over the provider name) rather than prefix-stripped, so a name that happens to
// start with the marker glyph cannot confuse the sync, and a credential-shaped
// on-disk name masks here exactly as it does in rebuild.
func (s *ProvidersScreen) syncMarker() {
	for i := range s.rows {
		if !strings.HasPrefix(s.rows[i].ID, providerRowPrefix) {
			continue
		}
		label := providers.MaskSlot(s.rows[i].ID[len(providerRowPrefix):])
		if i == s.sel {
			label = "● " + label
		}
		if s.rows[i].Label != label {
			s.rows[i].Label = label
		}
	}
}

// reload re-reads the registry and rebuilds the rows. A missing file is the
// normal pre-setup state (empty list); any other error is kept for Draw. The
// signature is recorded so reloadIfChanged can tell a stale row set from a
// current one. The sticky selection is re-anchored by provider NAME against the
// fresh registry (code review W1): a provider that still exists keeps its
// selection wherever it now sits, and a selected provider that is gone clears
// the selection and floors at -1 (none selected), so no action verb can inherit
// the index of the record that used to sit at that position.
func (s *ProvidersScreen) reload() {
	reg, err := providers.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.reg = &providers.Registry{}
			s.loadErr = nil
		} else {
			s.reg = &providers.Registry{}
			s.loadErr = err
		}
	} else {
		s.reg = reg
		s.loadErr = nil
	}
	s.sig = providersSig(s.reg)
	s.sel = providersIndexByName(s.reg, s.selName)
	if s.sel < 0 {
		// The selected provider is no longer in the registry: drop the identity
		// too, so a later provider that happens to reuse the name starts from an
		// unselected screen rather than re-adopting a stale selection. A Space pin
		// cannot outlive its target either: with nothing selected the lock would
		// keep the cursor-reassert suspended over a list that has no target, so it
		// drops together with the identity.
		s.selName = ""
		s.locked = false
	}
	s.rebuild()
}

// providersIndexByName resolves a provider NAME to its index in reg, or -1 when
// the registry does not carry it. The name is the registry's addressing key (the
// row IDs and Core.ProviderDelete both address a name, never a position), so it
// is also the identity the sticky selection is held by.
func providersIndexByName(reg *providers.Registry, name string) int {
	if reg == nil || name == "" {
		return -1
	}
	for i := range reg.Providers {
		if reg.Providers[i].Name == name {
			return i
		}
	}
	return -1
}

// providersSig signs the registry fields the provider rows render: the name,
// the endpoint, the key env-var NAME, and the model count. A change in any of
// them moves the signature (the models screen's modelsSig pattern).
func providersSig(reg *providers.Registry) string {
	parts := make([]string, len(reg.Providers))
	for i, p := range reg.Providers {
		parts[i] = strings.Join([]string{
			p.Name,
			p.BaseURL,
			p.APIKeyEnv,
			strconv.Itoa(len(p.Models)),
		}, "\x1f")
	}
	return strings.Join(parts, "\x1e")
}

// reloadIfChanged re-reads the registry and rebuilds the rows only when the
// on-disk signature moved. The provider form pushes ABOVE this screen and pops
// back after an approved write, and App.Pop does not re-run a constructor, so
// without this the list would keep rendering the snapshot it was built from: a
// just-added provider would stay invisible until the screen was re-entered.
// Draw runs it before composing, so the rows a frame shows are the rows the
// file holds. A registry that cannot be read keeps its error line (refreshed
// only on a real transition, so a corrupt file is not re-read into the same
// message every frame), and a file that becomes readable again clears it.
func (s *ProvidersScreen) reloadIfChanged() {
	reg, err := providers.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if s.loadErr == nil {
			s.reload()
		}
		return
	}
	if err != nil {
		reg = &providers.Registry{} // a missing file is the empty-registry state
	}
	if s.loadErr == nil && providersSig(reg) == s.sig {
		return
	}
	s.reload()
}

// rebuild composes the rows in the documented order: one row per provider (the
// entity block, so digit 1..N keep opening each provider's models screen), then
// the registry-level "Add provider" and "Test all" verbs, then the three
// sticky-target verbs Test, Edit, and Delete. Add and Edit are the in-TUI
// registration and repair doors (decision 1A); Delete stays last so the danger
// row sits furthest from the entity rows. The verb rows carry no provider
// operand (the label is the bare verb), so the row set no longer grows with the
// registry: the target is the sticky-selected provider row, marked with "●" so
// the object an action will hit is never invisible (the models screen's buildRows
// shape).
//
// The cursor is restored by ROW ID after the rebuild, so a mutation that shifts
// the row layout cannot leave the visible cursor on a different verb or provider
// than the user saw (the models screen's audit-F9 rule); a row that vanished
// falls back to the old index clamped into the new range by SetCursor.
//
// A registry that failed to load offers no rows at all: the error line would
// otherwise sit above a live Add/Edit row that could write to a file this
// screen could not read.
func (s *ProvidersScreen) rebuild() {
	var rows []Row
	oldCursor := s.list.Cursor()
	oldID := ""
	if oldCursor >= 0 && oldCursor < len(s.rows) {
		oldID = s.rows[oldCursor].ID
	}
	if s.loadErr == nil {
		for i, p := range s.reg.Providers {
			label := providers.MaskSlot(p.Name)
			if i == s.sel {
				label = "● " + label
			}
			rows = append(rows, Row{ID: providerRowPrefix + p.Name, Label: label, Note: s.note(p)})
		}
		rows = append(rows, Row{ID: providersRowIDAdd, Label: missingOr(s.bundle, "providers.form.add", providersFormAdd)})
		if len(s.reg.Providers) > 0 {
			rows = append(rows,
				Row{ID: providersRowIDTestAll, Label: missingOr(s.bundle, "providers.action.test_all", providersActionTestAll)},
				Row{ID: providersRowIDTest, Label: s.bundle.T("providers.action.test")},
				Row{ID: providersRowIDEdit, Label: missingOr(s.bundle, "providers.form.edit", providersFormEdit)},
				Row{ID: providersRowIDDelete, Label: s.bundle.T("providers.action.delete")},
			)
		}
	}
	s.rows = rows
	s.list.SetRows(rows)
	cursor := oldCursor
	for i, r := range rows {
		if r.ID == oldID {
			cursor = i
			break
		}
	}
	s.list.SetCursor(cursor)
}

// note composes the provider row annotation: model count plus key status.
// Only the env-var NAME is shown; a resolved key VALUE never is.
func (s *ProvidersScreen) note(p providers.Provider) string {
	count := s.bundle.Tn("menu.count.models", len(p.Models))
	env := p.APIKeyEnv
	if env == "" {
		return count + " · " + s.bundle.T("providers.key.unset")
	}
	status := s.bundle.T("providers.key.unset")
	if p.KeyAvailable() {
		status = "(" + s.bundle.T("common.ok") + ")"
	}
	return count + " · " + providers.MaskOperand(env) + " " + status
}

// pick is the shared stepList activation callback (digits and Enter both land
// here). Provider rows carry "provider\x00" plus the registry name and open that
// provider's models screen. The fixed rows carry a NUL-terminated kind prefix
// with an EMPTY operand: the three verbs (test, edit, delete) resolve their
// target from the sticky selection through withSelectedProvider, so an
// activation never reads the cursor position for a provider name, and the Add
// row hands off to the provider form with the form screen's own "add" mode
// contract (the form unit owns those strings, so this screen passes them
// literally like the models screen does).
//
// The pin lifecycle ends here, on the two activations that consume it: a sticky
// verb (Test, Edit, Delete) spends the pin it acted on, and a provider row
// navigates into the models screen, which is a different surface with its own
// selection. Both clear s.locked, so the follow-on-rest rule resumes the moment
// the user is back on this list. Add and Test all are registry-level rows that
// never read the selection, so they leave a pin standing: the user comes back
// from the add form to the row they pinned. Popping this screen needs no
// clearing: App.Pop drops the screen object, so the pin cannot outlive it (and
// the hub builds a fresh instance on every push).
func (s *ProvidersScreen) pick(idx int) {
	a := s.app
	if a == nil || idx < 0 || idx >= s.list.Len() {
		return
	}
	id := s.list.Row(idx).ID
	switch {
	case id == providersRowIDAdd:
		if s.openForm != nil {
			s.openForm("add", "", a)
		}
	case id == providersRowIDTestAll:
		s.testAll(a)
	case id == providersRowIDTest:
		s.locked = false
		s.withSelectedProvider(func(p providers.Provider) { s.testOne(p.Name, a) })
	case id == providersRowIDEdit:
		s.locked = false
		s.withSelectedProvider(func(p providers.Provider) {
			if s.openForm != nil {
				s.openForm("edit", p.Name, a)
			}
		})
	case id == providersRowIDDelete:
		s.locked = false
		s.withSelectedProvider(func(p providers.Provider) { s.confirmDelete(p.Name, a) })
	case strings.HasPrefix(id, providerRowPrefix):
		s.locked = false
		s.openModels(strings.TrimPrefix(id, providerRowPrefix), a)
	}
}

// withSelectedProvider runs fn over the sticky-selected provider (the models
// screen's withCursorModel rule). The selection follows the cursor while the
// cursor sits on a provider row and survives the move onto an action row (a
// digit activation jumps the cursor to the action row, so the verbs cannot read
// the cursor position directly). The target is resolved by NAME against the
// CURRENT registry, never by the stored index (code review W1): an index
// re-clamped after a reload can name a different record than the one the user
// selected. With no provider selected (or a selected one that is gone) the user
// gets a named message instead of a silent no-op or a silent hit on a neighbour.
func (s *ProvidersScreen) withSelectedProvider(fn func(providers.Provider)) {
	if p, ok := s.selectedProvider(); ok {
		fn(p)
		return
	}
	if s.app != nil {
		s.app.SetMessage(missingOr(s.bundle, "providers.msg.select_provider", providersMsgSelectProvider))
	}
}

// selectedProvider resolves the sticky selection against the current registry:
// the provider whose name the selection holds, or false when the selection is
// empty or the provider is no longer registered.
func (s *ProvidersScreen) selectedProvider() (providers.Provider, bool) {
	if i := providersIndexByName(s.reg, s.selName); i >= 0 {
		return s.reg.Providers[i], true
	}
	return providers.Provider{}, false
}

func (s *ProvidersScreen) provider(name string) (providers.Provider, bool) {
	for _, p := range s.reg.Providers {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return providers.Provider{}, false
}

// testOne probes one provider through Core.ProviderTest and reports the
// verdict on the message line. The provider name is a registry slot, so it
// renders through MaskSlot; the error detail keeps MaskOperand; key values
// never render at all.
func (s *ProvidersScreen) testOne(name string, a *App) {
	masked := providers.MaskSlot(name)
	a.SetMessage(s.bundle.Tf("providers.test.running", map[string]string{"name": masked}))
	if err := s.core.ProviderTest(name); err != nil {
		a.SetMessage(s.bundle.Tf("providers.test.fail", map[string]string{
			"name": masked,
			"err":  providers.MaskOperand(err.Error()),
		}))
		return
	}
	a.SetMessage(s.bundle.Tf("providers.test.ok", map[string]string{"name": masked}))
}

// testAll probes every provider and reports the OK count on the message line.
// The summary resolves providers.test.all_summary through missingOr with the
// const as the fallback, then fills the {{ok}}/{{total}} slots with
// fillHubSlots: Tf would take the fallback TEXT as the lookup KEY and never
// reach the catalog entry, so every locale printed the English summary while
// the catalogs carried a translation (the providersEmptyLine shape).
func (s *ProvidersScreen) testAll(a *App) {
	ok := 0
	for _, p := range s.reg.Providers {
		if s.core.ProviderTest(p.Name) == nil {
			ok++
		}
	}
	a.SetMessage(fillHubSlots(missingOr(s.bundle, "providers.test.all_summary", providersTestAllSummary),
		map[string]string{
			"ok":    strconv.Itoa(ok),
			"total": strconv.Itoa(len(s.reg.Providers)),
		}))
}

// maskBaseURL renders a registry BaseURL for a frame. MaskOperand keeps a
// benign URL readable, but its location exemption also lets credential
// material inside the URL ride raw. A url.Parse pass drops the userinfo it
// can see AND masks credential-shaped query keys/values plus a
// credential-shaped fragment (maskURLSecretParams), keeping scheme, host, and
// path readable while a benign query stays byte-identical; a value still
// carrying "@" afterwards is malformed enough that url.Parse misreads the
// credential as a host (a "/" inside it reads as a path separator), so the
// authority is cut at the last "@" before any query and the shared
// registry-slot masker (providers.MaskSlot) gets the final say when even that
// cannot isolate the host. The slot masker is the same authority the package
// level providers.MaskBaseURL ends on, so the screen-local copy and the shared
// one carry the policy fallback for a look-alike-decorated value on this arm
// too; the bare authority (providers.MaskRegistryValue) lacks that fallback.
func maskBaseURL(raw string) string {
	if m := providers.MaskOperand(raw); m != raw {
		return m
	}
	trimmed := strings.TrimSpace(raw)
	if u, err := url.Parse(trimmed); err == nil {
		changed := false
		if u.User != nil {
			u.User = nil
			changed = true
		}
		if maskURLSecretParams(u) {
			changed = true
		}
		if changed {
			stripped := u.String()
			if m := providers.MaskOperand(stripped); m != stripped {
				return m
			}
			return stripped
		}
	}
	if strings.Contains(trimmed, "@") {
		if cut := cutUserinfo(trimmed); cut != trimmed {
			if m := providers.MaskOperand(cut); m != cut {
				return m
			}
			return cut
		}
		if m := providers.MaskSlot(trimmed); m != trimmed {
			return m
		}
	}
	return raw
}

// maskURLSecretParams masks credential-shaped query keys/values and a
// credential-shaped fragment of an already-parsed URL in place, reporting
// whether it changed anything. It mirrors providers' maskURLSecretParams
// exactly, classifying with the shared exported rule
// providers.OperandLooksSecret so the screen-local copy and the package-level
// MaskBaseURL cannot drift on what counts as credential material. Benign
// queries leave u.RawQuery byte-unchanged: the re-encode runs only when a
// slot actually masked, so "?q={query}" and friends round-trip exactly.
func maskURLSecretParams(u *url.URL) bool {
	changed := false
	if q := u.Query(); len(q) > 0 {
		qChanged := false
		for key, vals := range q {
			newKey := key
			if providers.OperandLooksSecret(key) {
				newKey = providers.MaskSecret(key)
				qChanged = true
			}
			for i, v := range vals {
				if providers.OperandLooksSecret(v) {
					vals[i] = providers.MaskSecret(v)
					qChanged = true
				}
			}
			if newKey != key {
				delete(q, key)
			}
			q[newKey] = vals
		}
		if qChanged {
			u.RawQuery = q.Encode()
			changed = true
		}
	}
	if frag := strings.TrimSpace(u.Fragment); frag != "" && providers.OperandLooksSecret(frag) {
		u.Fragment = providers.MaskSecret(frag)
		changed = true
	}
	return changed
}

// cutUserinfo removes everything between "://" and the last "@" that appears
// before any "?" or "#", rebuilding scheme://host/path. It only answers the
// malformed-userinfo case maskBaseURL documents; well-formed URLs never
// reach it.
func cutUserinfo(s string) string {
	scheme := strings.Index(s, "://")
	if scheme < 0 {
		return s
	}
	rest := s[scheme+3:]
	end := len(rest)
	for _, sep := range []string{"?", "#"} {
		if i := strings.Index(rest, sep); i >= 0 && i < end {
			end = i
		}
	}
	if at := strings.LastIndex(rest[:end], "@"); at >= 0 {
		return s[:scheme+3] + rest[at+1:]
	}
	return s
}

// confirmDelete opens the M4 approval modal with the exact mutation summary:
// the provider, its model count before/after, the atomic-0600 write
// sentence, and the diff line. y is the only path to Core.ProviderDelete;
// n/Esc cancel with nothing written. The not-found branch masks the NAME
// operand through MaskSlot before it goes into the sentence: the registry
// sentence is the error body (so it stays a sentence, never a slot), but the
// name inside it is a registry slot and a credential-shaped one must not ride
// raw on the row-vanished path.
func (s *ProvidersScreen) confirmDelete(name string, a *App) {
	p, found := s.provider(name)
	if !found {
		a.SetMessage(s.bundle.Tf("err.unexpected", map[string]string{
			"err": fmt.Sprintf("provider %q not found", providers.MaskSlot(name)),
		}))
		return
	}
	masked := providers.MaskSlot(p.Name)
	diff := fmt.Sprintf("provider %s (%s, %s)", masked, maskBaseURL(p.BaseURL), s.bundle.Tn("menu.count.models", len(p.Models)))
	summary := []string{
		s.bundle.Tf("providers.delete.body", map[string]string{"name": masked}),
		s.bundle.Tf("modal.approval.counts", map[string]string{
			"before": strconv.Itoa(len(p.Models)),
			"after":  "0",
		}),
		s.bundle.T("modal.approval.body"),
		s.bundle.Tf("modal.approval.diff_removed", map[string]string{"line": diff}),
	}
	a.ShowModal(NewApproval(s.bundle.T("providers.delete.title"), summary, func(ok bool) {
		if !ok {
			// Slot-free sentence: missingOr resolves providers.delete.canceled by
			// KEY with the const as the fallback, so a translated catalog entry
			// reaches the message line instead of the raw English const.
			a.SetMessage(missingOr(s.bundle, "providers.delete.canceled", providersDeleteCanceled))
			return
		}
		// Shown-vs-written (M4): the modal summarized the s.reg snapshot, while
		// Core.ProviderDelete re-loads the file and removes the first
		// case-insensitive name match. Re-read and confirm the approved record
		// is still the one a write would remove; on any doubt, refuse.
		fresh, reason := s.reverifyDelete(p)
		if reason != "" {
			s.reload()
			a.SetMessage(s.deleteRefusal(p.Name, reason))
			return
		}
		if err := s.core.ProviderDelete(fresh.Name); err != nil {
			a.SetMessage(s.bundle.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
			return
		}
		s.reload()
		a.SetMessage(fillHubSlots(missingOr(s.bundle, "providers.delete.done", providersDeleteDone),
			map[string]string{"name": masked}))
	}))
}

// reverifyDelete re-reads the registry through the screen's own loader and
// confirms that the entry Core.ProviderDelete would remove is exactly the
// record the approval modal summarized. It returns the verified record with an
// empty reason when the write may proceed, or a refusal reason otherwise.
//
// Identity: Core's delete removes the FIRST case-insensitive (strings.EqualFold)
// name match in the freshly loaded file, so the shown and the written record
// are the same entry only when that first match carries the approved name
// byte-exact AND its body equals the approved record field by field. The name
// is the addressing key; the endpoint and the model list are part of the check
// because the M4 diff line the operator approved commits to them, and a
// same-named entry with a different endpoint or model list is a different
// configuration than the one summarized. Order beyond the first-match rule is
// irrelevant: the delete addresses a name, not a position.
//
// Every doubt refuses the write: an unreadable file (the same fail-closed
// posture as a corrupt load), an entry that is gone, a differently-cased
// duplicate that now sorts first, or a record whose body moved. The caller
// never reaches Core.ProviderDelete after a refusal.
func (s *ProvidersScreen) reverifyDelete(approved providers.Provider) (providers.Provider, string) {
	reg, err := providers.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return providers.Provider{}, providersDeleteReasonGone
		}
		return providers.Provider{}, providersDeleteReasonUnreadable + ": " + providers.MaskOperand(err.Error())
	}
	for i := range reg.Providers {
		if !strings.EqualFold(reg.Providers[i].Name, approved.Name) {
			continue
		}
		// First case-insensitive match: this is the entry the Core verb removes.
		if reg.Providers[i].Name != approved.Name {
			return providers.Provider{}, providersDeleteReasonAmbiguous
		}
		if !sameProviderRecord(reg.Providers[i], approved) {
			return providers.Provider{}, providersDeleteReasonChanged
		}
		return reg.Providers[i], ""
	}
	return providers.Provider{}, providersDeleteReasonGone
}

// sameProviderRecord reports whether two provider records describe the same
// configuration. The deep comparison covers every field of the record (name,
// endpoint, key variable NAME, and the full model list with its capabilities),
// so a field added to the record later is compared without touching this gate.
func sameProviderRecord(a, b providers.Provider) bool { return reflect.DeepEqual(a, b) }

// deleteRefusal composes the refusal message line: the provider name slot
// routes through MaskSlot (it is a registry slot) and the load detail through
// MaskOperand, like every other echoed value on this screen. The sentence has
// no catalog key (decision 6B keeps the refusal English), so the const
// template is filled directly instead of going through Tf, whose lookup would
// treat the whole template as a key name and find nothing.
func (s *ProvidersScreen) deleteRefusal(name, reason string) string {
	return fillHubSlots(providersDeleteRefused, map[string]string{
		"name":   providers.MaskSlot(name),
		"reason": reason,
	})
}
