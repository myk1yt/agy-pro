package tui

import (
	"agypro/internal/i18n"
)

// languageScreen is screen 0 of the setup flow: the 12-language selector.
// Each row shows the endonym alone (a code constant, readable before any
// catalog loads); the Arabic row adds the honest "(logical order)"
// qualifier. The stored language's row is preselected (English on first
// run).
//
// The row carries no second name on purpose: lang.name.<code> exists in
// none of the 12 catalogs, and an always-English parenthetical beside an
// endonym states something the endonym already says while pretending to be
// the current UI language. The endonym is the row's identifier; the row ID
// (m.Code) is the stored value.
//
// The screen renders with the bundle it was constructed with and never
// writes state itself: activation delegates to onChoose, and the app shell
// persists through Core.Set, reloads the bundle, flips Dir for ar, and pops
// this screen (one write path stays in the shell).
type languageScreen struct {
	b        *i18n.Bundle
	current  string // stored language code; "" on a first run
	onChoose func(code string, a *App)
	list     *stepList
	codes    []string
	app      *App // set by HandleKey; the pick callback needs it
}

// NewLanguageScreen builds the language selector. b is the bundle of the
// current UI language; current is the stored language code ("" on first
// run), which preselects the matching row and switches the footer from the
// first-run hint to the keep line. onChoose fires on digit selection or
// Enter with the chosen code and the app.
func NewLanguageScreen(b *i18n.Bundle, current string, onChoose func(code string, a *App)) Screen {
	if b == nil {
		b = Bundle()
	}
	s := &languageScreen{b: b, current: current, onChoose: onChoose}
	langs := i18n.Languages()
	rows := make([]Row, len(langs))
	s.codes = make([]string, len(langs))
	for i, m := range langs {
		s.codes[i] = m.Code
		rows[i] = Row{Label: s.rowLabel(m), ID: m.Code}
	}
	s.list = newStepList(s.pick)
	s.list.SetRows(rows)
	s.list.SetCursor(s.preselected())
	return s
}

// rowLabel composes one selector row: the endonym, then the RTL qualifier
// for Arabic. The endonym is the identifier (unique across the closed
// registry and readable in its own script), so no second name is appended.
func (s *languageScreen) rowLabel(m i18n.LangMeta) string {
	label := m.Endonym
	if m.Dir == "rtl" {
		label += " " + s.b.T("lang.logical_order")
	}
	return label
}

// preselected is the row index of the stored language. An empty or unknown
// current normalizes to en, which is row 1, so the cursor starts on English
// on a first run.
func (s *languageScreen) preselected() int {
	code := i18n.Normalize(s.current)
	for i, c := range s.codes {
		if c == code {
			return i
		}
	}
	return 0
}

// Title resolves through the catalog in the current UI language.
func (s *languageScreen) Title() string { return s.b.T("lang.screen.title") }

// Mode is ModeNav: digits and arrows both navigate.
func (s *languageScreen) Mode() Mode { return ModeNav }

// Draw renders the numbered rows and the footer: the first-run hint, or the
// keep line on a returning run (a language is already stored). The closed
// registry holds 12 languages and digits reach rows 1-9 only, so a list
// longer than 9 rows adds the shared rows-past-9 clause on its own footer
// line (same clause, same threshold, and the same English fallback as
// stepList.FooterHint; the key the help overlay uses makes it follow the UI
// language).
//
// Height budget: the frame is title + blank + rows + blank + footer lines +
// blank + base footer, so the page holds Height - 5 - len(footer) rows. The
// clause line is paid for out of the page, so the frame height is unchanged
// and a 24-row first frame still fits end to end.
func (s *languageScreen) Draw(a *App) []string {
	line := s.b.T("lang.screen.hint")
	if s.current != "" {
		line = s.b.T("lang.screen.keep")
	}
	footer := []string{line}
	if s.list.Len() > 9 {
		footer = append(footer, missingOr(s.b, "help.keys.rows_past_9", helpKeysRowsPast9))
	}
	page := a.Height() - 5 - len(footer)
	if page < 1 {
		page = 1
	}
	s.list.SetPageSize(page)
	return append(append(s.list.Render(a.Width()), ""), footer...)
}

// HandleKey delegates to the shared stepList, so digits select+activate and
// arrows move with Enter activating (both input languages are always live).
// Esc never arrives here: the App precedence handles it first.
func (s *languageScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}

// pick is the shared stepList activation callback (digits and Enter both
// land here). It only reports the choice; persisting, reloading, and popping
// are the shell's job inside onChoose.
func (s *languageScreen) pick(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.codes) {
		return
	}
	if s.onChoose != nil {
		s.onChoose(s.codes[idx], s.app)
	}
}
