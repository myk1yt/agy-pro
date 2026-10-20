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

// English fallbacks for catalog keys this screen resolves, plus one label that
// has no catalog key at all. Every key named below carries an entry in all
// twelve locale files, so a lookup never has to fall back to English or to the
// key ID, and each const stays as the fallback for a catalog lacking the
// translation. Every lookup passes the KEY named in the const's own comment,
// never the const text itself: Bundle.T and Tf are literal-name lookups, so
// handing one of them a fallback sentence reaches no catalog entry and prints
// English in every locale. TestProvidersFallbackConstsResolveInAllLocales gates
// both halves of that claim (entry present per locale, key never handed back).
//
// providersActionTestAll is the exception: the "Test all" row carries an
// English literal and no catalog key. All twelve locale files carried
// providers.action.test_all, but nothing in the module read it (Bundle.T is a
// literal-name lookup, so no dynamic reader could reach it either), so the
// phase-4 catalog wave removed the key and the row prints this const verbatim.
// Localizing the row needs a re-added key plus a lookup at the row itself, so
// it stays English in every locale until then.
const (
	// English literal, no catalog key: the "Test all" row label
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
// "provider\x00" scheme (same convention as the models screen), and the
// action rows carry "add\x00", "test\x00", "edit\x00" or "delete\x00". A
// NUL-terminated kind prefix makes collisions impossible for ANY provider
// name: two row IDs are equal only when both the kind prefix and the name are
// equal, so a hand-edited name such as "delete:x" or "test-all" can never
// shadow another row. The load-time registry validation does not enforce the
// name charset, so this screen must not depend on it. "add\x00" is the one row
// with an empty operand: it is fixed, and the prefix keeps it in the same
// scheme (the literal "test-all" stays exempt because it is compared by exact
// equality before any prefix test).
const (
	providersRowIDTestAll = "test-all"
	providerRowPrefix     = "provider\x00"
	providersRowIDAdd     = "add\x00"
	providersRowIDTest    = "test\x00"
	providersRowIDEdit    = "edit\x00"
	providersRowIDDelete  = "delete\x00"
)

// ProvidersScreen is S2: the providers list. One row per registered provider
// (Enter opens its models screen), plus fixed action rows: "Add provider",
// "Test all", then one test, one edit, and one delete row per provider (delete
// is danger, gated by the M4 approval modal). Add and edit open the provider
// form through the shell callback (decision 1A: registration happens inside the
// TUI, the CLI/wizard is no longer the only door); this screen itself never
// writes providers.json except through the approval modal behind
// Core.ProviderDelete.
type ProvidersScreen struct {
	core       *settings.Core
	openModels func(providerName string, a *App)
	openForm   func(mode, providerName string, a *App)
	bundle     *i18n.Bundle
	list       *stepList
	reg        *providers.Registry
	loadErr    error
	sig        string // registry signature the rows were built from
	app        *App   // set by HandleKey; the list callback needs it
}

// NewProvidersScreen builds the providers list over core. openModels is the
// shell callback that pushes the per-provider models screen, and openForm is
// the provider form factory the Add row and every Edit row call: mode is the
// form screen's own "add" / "edit" contract, and providerName is empty in add
// mode (NewProviderFormScreen(core, mode, providerName), owned by the form
// unit). Tests inject spies.
func NewProvidersScreen(core *settings.Core, openModels func(providerName string, a *App), openForm func(mode, providerName string, a *App)) Screen {
	s := &ProvidersScreen{
		core:       core,
		openModels: openModels,
		openForm:   openForm,
		bundle:     Bundle(),
	}
	s.list = newStepList(s.pick)
	s.reload()
	return s
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

// HandleKey routes one key to the shared stepList. The app pointer is kept
// for the activation callback, which needs it to open screens, set messages,
// and show the approval modal.
func (s *ProvidersScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}

// reload re-reads the registry and rebuilds the rows. A missing file is the
// normal pre-setup state (empty list); any other error is kept for Draw. The
// signature is recorded so reloadIfChanged can tell a stale row set from a
// current one.
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
	s.rebuild()
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

// rebuild composes the rows in the documented order: one row per provider
// (the entity block, so digit 1..N keep opening each provider's models screen),
// then the registry-level "Add provider" and "Test all" verbs, then the
// per-provider blocks grouped by verb: test, edit, delete. Add and Edit are the
// in-TUI registration and repair doors (decision 1A); the delete block stays
// last so the danger rows sit furthest from the entity rows.
//
// A registry that failed to load offers no rows at all: the error line would
// otherwise sit above a live Add/Edit row that could write to a file this
// screen could not read.
func (s *ProvidersScreen) rebuild() {
	var rows []Row
	if s.loadErr != nil {
		s.list.SetRows(rows)
		return
	}
	for _, p := range s.reg.Providers {
		rows = append(rows, Row{ID: providerRowPrefix + p.Name, Label: providers.MaskSlot(p.Name), Note: s.note(p)})
	}
	rows = append(rows, Row{ID: providersRowIDAdd, Label: missingOr(s.bundle, "providers.form.add", providersFormAdd)})
	if len(s.reg.Providers) > 0 {
		rows = append(rows, Row{ID: providersRowIDTestAll, Label: providersActionTestAll})
		for _, p := range s.reg.Providers {
			name := providers.MaskSlot(p.Name)
			note := ""
			if p.APIKeyEnv != "" {
				note = providers.MaskOperand(p.APIKeyEnv)
			}
			rows = append(rows, Row{
				ID:    providersRowIDTest + p.Name,
				Label: s.bundle.T("providers.action.test") + ": " + name,
				Note:  note,
			})
		}
		for _, p := range s.reg.Providers {
			name := providers.MaskSlot(p.Name)
			rows = append(rows, Row{
				ID:    providersRowIDEdit + p.Name,
				Label: missingOr(s.bundle, "providers.form.edit", providersFormEdit) + ": " + name,
			})
		}
		for _, p := range s.reg.Providers {
			name := providers.MaskSlot(p.Name)
			rows = append(rows, Row{
				ID:    providersRowIDDelete + p.Name,
				Label: s.bundle.T("providers.action.delete") + ": " + name,
			})
		}
	}
	s.list.SetRows(rows)
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

// pick is the shared stepList activation callback (digits and Enter both
// land here). Row IDs carry a NUL-terminated kind prefix; the remainder is
// the provider name the row was built from, so an activation always names
// that provider. Add carries no operand (empty remainder) and edit carries the
// provider name; both hand off to the provider form through openForm with the
// form screen's own "add" / "edit" mode contract (the form unit owns those
// strings, so this screen passes them literally like the models screen does).
func (s *ProvidersScreen) pick(idx int) {
	a := s.app
	if a == nil {
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
	case strings.HasPrefix(id, providersRowIDTest):
		s.testOne(strings.TrimPrefix(id, providersRowIDTest), a)
	case strings.HasPrefix(id, providersRowIDEdit):
		if s.openForm != nil {
			s.openForm("edit", strings.TrimPrefix(id, providersRowIDEdit), a)
		}
	case strings.HasPrefix(id, providersRowIDDelete):
		s.confirmDelete(strings.TrimPrefix(id, providersRowIDDelete), a)
	default:
		s.openModels(strings.TrimPrefix(id, providerRowPrefix), a)
	}
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
