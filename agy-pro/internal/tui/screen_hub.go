package tui

import (
	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
)

// HubCounts is the live data the hub health summary and rows display.
// The app shell supplies a counts func so the hub re-reads state on every
// frame instead of caching it at construction.
type HubCounts struct {
	Providers       int
	Presets         int
	PromptOverrides int
	RegistryOK      bool
	Language        string
}

// Row IDs of the hub list, in display order. The open factory maps these
// IDs to screens; "exit" is resolved locally and never reaches open.
const (
	hubIDProviders = "providers"
	hubIDPresets   = "presets"
	hubIDPrompts   = "prompts"
	hubIDSettings  = "settings"
	hubIDDoctor    = "doctor"
	hubIDHelp      = "help"
	hubIDLanguage  = "language"
	hubIDExit      = "exit"
)

// Catalog keys the hub needs that are not in en.json yet. Per mission rule
// the screen ships local English stand-ins and en.json gains the keys in a
// later catalog pass; the stand-in wins only while the catalog lacks the key.
const (
	// catalog-resolved, fallback kept: menu.summary
	missingMenuSummary = "{{providers}} | {{presets}} | {{prompts}} | {{registry}} | {{lang}}"
	// catalog-resolved, fallback kept: menu.registry.ok
	missingRegistryOK = "registry OK"
	// catalog-resolved, fallback kept: menu.registry.fail
	missingRegistryFail = "registry load failed"
)

// hubScreen is the setup TUI main menu: a health summary line over the
// numbered row list. Navigation is the shared stepList, so digits and
// arrows+Enter are both always live (requirement 2).
type hubScreen struct {
	counts func() HubCounts
	open   func(id string, a *App)
	list   *stepList
	rows   []Row
	lang   string
	app    *App
}

// NewHubScreen builds the hub. counts supplies the live summary data and
// the current UI language; open maps a row ID to its screen (registered by
// the app shell). The exit row calls a.Exit() and never reaches open.
func NewHubScreen(counts func() HubCounts, open func(id string, a *App)) Screen {
	h := &hubScreen{counts: counts, open: open}
	h.list = newStepList(h.activate)
	return h
}

// currentCounts reads the counts supplier, tolerating a nil func.
func (h *hubScreen) currentCounts() HubCounts {
	if h.counts == nil {
		return HubCounts{Language: "en"}
	}
	return h.counts()
}

// Mode is ModeNav: digits and arrows both navigate.
func (h *hubScreen) Mode() Mode { return ModeNav }

// Title resolves through the catalog in the current UI language.
func (h *hubScreen) Title() string {
	return i18n.Load(h.currentCounts().Language).T("menu.title")
}

// syncRows rebuilds the row labels when the UI language changed since the
// last frame. The language row carries the current language endonym as its
// note so the selector always shows what is active.
func (h *hubScreen) syncRows() {
	lang := i18n.Normalize(h.currentCounts().Language)
	if lang == h.lang && h.rows != nil {
		return
	}
	h.lang = lang
	b := i18n.Load(lang)
	h.rows = []Row{
		{Label: b.T("menu.providers"), ID: hubIDProviders},
		{Label: b.T("menu.presets"), ID: hubIDPresets},
		{Label: b.T("menu.prompts"), ID: hubIDPrompts},
		{Label: b.T("menu.settings"), ID: hubIDSettings},
		{Label: b.T("menu.doctor"), ID: hubIDDoctor},
		{Label: b.T("menu.help"), ID: hubIDHelp},
		{Label: b.T("view.appearance.language.label"), ID: hubIDLanguage, Note: i18n.Endonym(lang)},
		{Label: b.T("menu.quit"), ID: hubIDExit},
	}
	h.list.SetRows(h.rows)
}

// activate is the stepList onPick callback. The exit row quits the app;
// every other row goes through the open factory with its ID.
func (h *hubScreen) activate(idx int) {
	if h.app == nil || idx < 0 || idx >= len(h.rows) {
		return
	}
	id := h.rows[idx].ID
	if id == hubIDExit {
		h.app.Exit()
		return
	}
	if h.open != nil {
		h.open(id, h.app)
	}
}

// summary composes the one-line health summary above the list. The counts
// ride {{n}} Tn phrases and the registry status and language are Tf slots.
func (h *hubScreen) summary() string {
	c := h.currentCounts()
	b := i18n.Load(c.Language)
	registry := missingRegistryOK
	if !c.RegistryOK {
		registry = missingRegistryFail
	}
	tpl := b.T("menu.summary")
	if tpl == "menu.summary" {
		tpl = missingMenuSummary
	}
	return fillHubSlots(tpl, map[string]string{
		"providers": b.Tn("menu.count.providers", c.Providers),
		"presets":   b.Tn("menu.count.presets", c.Presets),
		"prompts":   b.Tn("menu.count.prompts", c.PromptOverrides),
		"registry":  registry,
		"lang":      i18n.Endonym(c.Language),
	})
}

// fillHubSlots replaces literal {{name}} placeholders in a template that may
// come from a local fallback const rather than a catalog entry, so Tf's key
// lookup cannot be used directly; it delegates to i18n.Fill, the same
// single-pass scan Tf uses (a slot value is inserted verbatim and never
// rescanned, so it cannot inject a new substitution).
func fillHubSlots(s string, slots map[string]string) string {
	return i18n.Fill(s, slots)
}

// Draw renders the health summary, the numbered rows, and the list footer
// hint. All cell math goes through cellwidth.
func (h *hubScreen) Draw(a *App) []string {
	h.syncRows()
	lines := []string{cellwidth.Truncate(h.summary(), a.Width()), ""}
	lines = append(lines, h.list.Render(a.Width())...)
	lines = append(lines, "", h.list.FooterHint())
	return lines
}

// HandleKey delegates to the stepList. Esc never arrives here: the App
// precedence handles it first (at the root it exits), so the hub only sees
// list keys and ignores everything else.
func (h *hubScreen) HandleKey(k Key, a *App) {
	h.app = a
	h.list.HandleKey(k)
}
