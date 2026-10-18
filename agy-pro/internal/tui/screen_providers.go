package tui

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved); each const names
// its key and stays as the fallback for a catalog lacking the translation.
const (
	// catalog-resolved, fallback kept: providers.action.test_all
	providersActionTestAll = "Test all"
	// catalog-resolved, fallback kept: providers.test.all_summary
	providersTestAllSummary = "{{ok}}/{{total}} providers OK"
	// catalog-resolved, fallback kept: providers.delete.done
	providersDeleteDone = "Provider {{name}} deleted."
	// catalog-resolved, fallback kept: providers.delete.canceled
	providersDeleteCanceled = "Delete canceled. providers.json unchanged."
)

// Row IDs for the fixed action rows. Provider rows carry the NUL-prefixed
// "provider\x00" scheme (same convention as the models screen), and the
// action rows carry "test\x00" / "delete\x00". A NUL-terminated kind prefix
// makes collisions impossible for ANY provider name: two row IDs are equal
// only when both the kind prefix and the name are equal, so a hand-edited
// name such as "delete:x" or "test-all" can never shadow another row. The
// load-time registry validation does not enforce the name charset, so this
// screen must not depend on it.
const (
	providersRowIDTestAll = "test-all"
	providerRowPrefix     = "provider\x00"
	providersRowIDTest    = "test\x00"
	providersRowIDDelete  = "delete\x00"
)

// ProvidersScreen is S2: the providers list. One row per registered provider
// (Enter opens its models screen), plus fixed action rows: "Test all",
// one test row per provider, and one delete row per provider (danger, gated
// by the M4 approval modal). Provider ADD stays on the existing CLI/wizard
// path; this screen never writes providers.json except through the approval
// modal behind Core.ProviderDelete.
type ProvidersScreen struct {
	core       *settings.Core
	openModels func(providerName string, a *App)
	bundle     *i18n.Bundle
	list       *stepList
	reg        *providers.Registry
	loadErr    error
	app        *App // set by HandleKey; the list callback needs it
}

// NewProvidersScreen builds the providers list over core. openModels is the
// shell callback that pushes the per-provider models screen.
func NewProvidersScreen(core *settings.Core, openModels func(providerName string, a *App)) Screen {
	s := &ProvidersScreen{
		core:       core,
		openModels: openModels,
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
// registry renders the honest empty line; a load failure renders the error.
func (s *ProvidersScreen) Draw(a *App) []string {
	page := a.Height() - 5
	if page < 1 {
		page = 1
	}
	s.list.SetPageSize(page)
	var lines []string
	switch {
	case s.loadErr != nil:
		lines = append(lines, s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(s.loadErr.Error())}))
	case s.list.Len() == 0:
		lines = append(lines, s.bundle.T("providers.empty"))
	default:
		lines = s.list.Render(a.Width())
	}
	return append(lines, "", s.list.FooterHint())
}

// HandleKey routes one key to the shared stepList. The app pointer is kept
// for the activation callback, which needs it to open screens, set messages,
// and show the approval modal.
func (s *ProvidersScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}

// reload re-reads the registry and rebuilds the rows. A missing file is the
// normal pre-setup state (empty list); any other error is kept for Draw.
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
	s.rebuild()
}

// rebuild composes the rows: one per provider, then "Test all", then one
// test row and one delete row per provider.
func (s *ProvidersScreen) rebuild() {
	var rows []Row
	for _, p := range s.reg.Providers {
		rows = append(rows, Row{ID: providerRowPrefix + p.Name, Label: providers.MaskOperand(p.Name), Note: s.note(p)})
	}
	if len(s.reg.Providers) > 0 {
		rows = append(rows, Row{ID: providersRowIDTestAll, Label: providersActionTestAll})
		for _, p := range s.reg.Providers {
			name := providers.MaskOperand(p.Name)
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
			name := providers.MaskOperand(p.Name)
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
// that provider.
func (s *ProvidersScreen) pick(idx int) {
	a := s.app
	if a == nil {
		return
	}
	id := s.list.Row(idx).ID
	switch {
	case id == providersRowIDTestAll:
		s.testAll(a)
	case strings.HasPrefix(id, providersRowIDTest):
		s.testOne(strings.TrimPrefix(id, providersRowIDTest), a)
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
// verdict on the message line. Operands render masked; key values never do.
func (s *ProvidersScreen) testOne(name string, a *App) {
	masked := providers.MaskOperand(name)
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
func (s *ProvidersScreen) testAll(a *App) {
	ok := 0
	for _, p := range s.reg.Providers {
		if s.core.ProviderTest(p.Name) == nil {
			ok++
		}
	}
	a.SetMessage(s.bundle.Tf(providersTestAllSummary, map[string]string{
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
// authority is cut at the last "@" before any query and the registry-slot
// masker gets the final say when even that cannot isolate the host.
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
		if m := providers.MaskRegistryValue(trimmed); m != trimmed {
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
// n/Esc cancel with nothing written.
func (s *ProvidersScreen) confirmDelete(name string, a *App) {
	p, found := s.provider(name)
	if !found {
		a.SetMessage(s.bundle.Tf("err.unexpected", map[string]string{
			"err": providers.MaskOperand(fmt.Sprintf("provider %q not found", name)),
		}))
		return
	}
	masked := providers.MaskOperand(p.Name)
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
			a.SetMessage(providersDeleteCanceled)
			return
		}
		if err := s.core.ProviderDelete(p.Name); err != nil {
			a.SetMessage(s.bundle.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
			return
		}
		s.reload()
		a.SetMessage(s.bundle.Tf(providersDeleteDone, map[string]string{"name": masked}))
	}))
}
