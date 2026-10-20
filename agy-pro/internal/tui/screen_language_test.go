package tui

import (
	"fmt"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
)

// newLanguageApp wires a language screen with a recording onChoose spy into
// a fresh app and runs the scripted keys.
func newLanguageApp(current string, keys []Key) (string, []string) {
	var chosen []string
	scr := NewLanguageScreen(i18n.Load("en"), current, func(code string, a *App) {
		chosen = append(chosen, code)
	})
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	return out, chosen
}

// cursorRow returns the frame line carrying the visible cursor marker.
func cursorRow(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "▶ ") {
			return line
		}
	}
	t.Fatalf("no cursor row in frames:\n%s", out)
	return ""
}

// TestLanguageRowsRender pins the row inventory: all 12 closed-set languages
// render, each led by its endonym (a code constant readable before any
// catalog loads), numbered 1..12.
func TestLanguageRowsRender(t *testing.T) {
	out, _ := newLanguageApp("", nil)
	langs := i18n.Languages()
	if len(langs) != 12 {
		t.Fatalf("registry has %d languages, want 12", len(langs))
	}
	for i, m := range langs {
		if !strings.Contains(out, m.Endonym) {
			t.Fatalf("row %d missing endonym %q; frames:\n%s", i+1, m.Endonym, out)
		}
		if !strings.Contains(out, fmt.Sprintf("[%2d]", i+1)) {
			t.Fatalf("row %d missing number [%2d]; frames:\n%s", i+1, i+1, out)
		}
	}
}

// TestLanguagePreselectedCursor pins the starting cursor: the stored
// language's row is marked; English is the default on a first run.
func TestLanguagePreselectedCursor(t *testing.T) {
	out, _ := newLanguageApp("fr", nil)
	row := cursorRow(t, out)
	if !strings.Contains(row, i18n.Endonym("fr")) {
		t.Fatalf("cursor on %q, want the French row; frames:\n%s", row, out)
	}
	if strings.Contains(row, i18n.Endonym("en")) {
		t.Fatalf("cursor row shows English alongside French; frames:\n%s", out)
	}

	first, _ := newLanguageApp("", nil)
	if row := cursorRow(t, first); !strings.Contains(row, i18n.Endonym("en")) {
		t.Fatalf("first-run cursor on %q, want English; frames:\n%s", row, first)
	}
}

// TestLanguageDigitConfirms pins digit selection: the digit activates the
// matching row and onChoose fires exactly once with the row's code. Row 4 is
// Korean (en, zh-Hans, zh-Hant, ko).
func TestLanguageDigitConfirms(t *testing.T) {
	out, chosen := newLanguageApp("", []Key{Digit(4)})
	if len(chosen) != 1 || chosen[0] != "ko" {
		t.Fatalf("onChoose = %v, want [ko] exactly once", chosen)
	}
	if !strings.Contains(out, i18n.Endonym("ko")) {
		t.Fatalf("frames missing the Korean row:\n%s", out)
	}
}

// TestLanguageArrowEnterDualNavigation pins the dual-navigation contract:
// arrows plus Enter confirm the same language a digit press does, and each
// run calls onChoose exactly once.
func TestLanguageArrowEnterDualNavigation(t *testing.T) {
	_, digitChosen := newLanguageApp("", []Key{Digit(4)})
	_, arrowChosen := newLanguageApp("", []Key{KeyDown, KeyDown, KeyDown, KeyEnter})
	if len(digitChosen) != 1 || len(arrowChosen) != 1 {
		t.Fatalf("digit chose %v, arrows chose %v; want one choice each", digitChosen, arrowChosen)
	}
	if digitChosen[0] != arrowChosen[0] {
		t.Fatalf("digit chose %q, arrows chose %q; want the same language", digitChosen[0], arrowChosen[0])
	}
	if digitChosen[0] != "ko" {
		t.Fatalf("both paths chose %q, want %q", digitChosen[0], "ko")
	}
}

// TestLanguageArabicQualifier pins the honest RTL scope note: the Arabic row
// carries the "(logical order)" qualifier in every language, appended
// directly to the endonym (the row carries no second language name, audit
// F20).
func TestLanguageArabicQualifier(t *testing.T) {
	out, _ := newLanguageApp("", nil)
	qualifier := i18n.Load("en").T("lang.logical_order")
	if qualifier == "lang.logical_order" {
		t.Fatalf("lang.logical_order missing from the en catalog")
	}
	want := i18n.Endonym("ar") + " " + qualifier
	if !strings.Contains(out, want) {
		t.Fatalf("Arabic row missing %q; frames:\n%s", want, out)
	}
}

// TestLanguageFooterHint pins the footer: the first-run hint on a fresh run,
// the keep line when a language is already stored, never both.
func TestLanguageFooterHint(t *testing.T) {
	b := i18n.Load("en")
	hint, keep := b.T("lang.screen.hint"), b.T("lang.screen.keep")

	// Moved pin: the width clamp moved to the shared composition path
	// (framePlan.compose -> clampLines, phase-3 fix-2), so the 139-cell hint
	// reaches the 80-cell frame edge as this cut form. The old expectation
	// asserted the whole hint, content only the unclamped test path ever
	// composed; the clamped prefix is what the console painter always showed.
	const clampedHint = "Digits 1-9 select, arrows move, Enter confirms. The choice applies immediately a"
	if got := cellwidth.Truncate(hint, defaultFrameWidth); got != clampedHint {
		t.Fatalf("clamped hint = %q, want %q", got, clampedHint)
	}

	first, _ := newLanguageApp("", nil)
	if !frameHasLine(first, clampedHint) {
		t.Fatalf("first run missing the clamped hint %q on its own line; frames:\n%s", clampedHint, first)
	}
	for i, line := range strings.Split(first, "\n") {
		if cw := cellwidth.String(line); cw > defaultFrameWidth {
			t.Fatalf("first-run frame line %d = %d cells, want at most %d: %q", i, cw, defaultFrameWidth, line)
		}
	}
	if strings.Contains(first, keep) {
		t.Fatalf("first run must not show the keep line %q; frames:\n%s", keep, first)
	}

	returning, _ := newLanguageApp("de", nil)
	if !strings.Contains(returning, keep) {
		t.Fatalf("returning run missing keep line %q; frames:\n%s", keep, returning)
	}
	if strings.Contains(returning, hint) {
		t.Fatalf("returning run must not show the hint %q; frames:\n%s", hint, returning)
	}
}

// TestLanguageTitleAndMode pins the surface contract: NAV mode and the
// catalog title.
func TestLanguageTitleAndMode(t *testing.T) {
	scr := NewLanguageScreen(i18n.Load("en"), "", nil)
	if scr.Mode() != ModeNav {
		t.Fatalf("language screen mode = %v, want ModeNav", scr.Mode())
	}
	if got, want := scr.Title(), i18n.Load("en").T("lang.screen.title"); got != want {
		t.Fatalf("language screen title = %q, want %q", got, want)
	}
}

// frameHasLine reports whether out carries want as a whole line.
func frameHasLine(out, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// countListRows counts the rendered list lines (numbered rows, cursor
// marked or not) in a screen body.
func countListRows(body []string) int {
	n := 0
	for _, line := range body {
		if strings.HasPrefix(line, "  [") || strings.HasPrefix(line, "▶ ") {
			n++
		}
	}
	return n
}

// shrinkLanguageRows cuts a language selector down to n rows so the footer
// threshold and the height budget can be exercised without a second
// registry.
func shrinkLanguageRows(scr Screen, n int) *languageScreen {
	s := scr.(*languageScreen)
	if n > len(s.codes) {
		n = len(s.codes)
	}
	rows := make([]Row, n)
	for i := range rows {
		rows[i] = s.list.Row(i)
	}
	s.codes = s.codes[:n]
	s.list.SetRows(rows)
	return s
}

// TestLanguageRowsEndonymOnly pins audit F20: no row renders an English
// parenthetical. lang.name.<code> exists in none of the 12 catalogs, so a
// parenthetical could only ever state the registry English name; the
// endonym alone identifies the row, and the Arabic qualifier stays.
func TestLanguageRowsEndonymOnly(t *testing.T) {
	out, _ := newLanguageApp("", nil)
	for _, m := range i18n.Languages() {
		if !strings.Contains(out, m.Endonym) {
			t.Fatalf("%s row missing endonym %q; frames:\n%s", m.Code, m.Endonym, out)
		}
		if english := "(" + m.English + ")"; strings.Contains(out, english) {
			t.Fatalf("%s row renders the English parenthetical %q; frames:\n%s", m.Code, english, out)
		}
	}
}

// TestLanguageFooterRowsPast9 pins audit F26: digits reach rows 1-9 only, so
// a list longer than 9 rows names the arrows + Enter path in its footer and
// a list of 9 or fewer rows does not. The clause follows the UI language
// instead of hardcoding English.
func TestLanguageFooterRowsPast9(t *testing.T) {
	en := i18n.Load("en")
	clause := en.T("help.keys.rows_past_9")
	if clause == "help.keys.rows_past_9" {
		t.Fatalf("help.keys.rows_past_9 missing from the en catalog")
	}
	if !strings.Contains(clause, "arrows + Enter") {
		t.Fatalf("shared clause %q does not name the arrows + Enter path", clause)
	}

	first, _ := newLanguageApp("", nil)
	if !frameHasLine(first, clause) {
		t.Fatalf("12-row first-run footer lacks the clause %q on its own line; frames:\n%s", clause, first)
	}

	returning, _ := newLanguageApp("de", nil)
	if !frameHasLine(returning, clause) {
		t.Fatalf("12-row returning-run footer lacks the clause %q on its own line; frames:\n%s", clause, returning)
	}

	app := NewApp(80, 24, DirLTR)
	app.Push(shrinkLanguageRows(NewLanguageScreen(en, "", nil), 9))
	body := strings.Join(app.Top().Draw(app), "\n")
	if strings.Contains(body, clause) {
		t.Fatalf("9-row footer carries the rows-past-9 clause %q; body:\n%s", clause, body)
	}

	ko := i18n.Load("ko")
	koClause := ko.T("help.keys.rows_past_9")
	if koClause == clause {
		t.Fatalf("Korean clause equals the English one %q; the footer is not catalog-resolved", clause)
	}
	appKo := NewApp(80, 24, DirLTR)
	appKo.Push(NewLanguageScreen(ko, "", nil))
	if koBody := strings.Join(appKo.Top().Draw(appKo), "\n"); !strings.Contains(koBody, koClause) {
		t.Fatalf("Korean UI footer lacks its own clause %q; body:\n%s", koClause, koBody)
	}
}

// TestLanguageFooterHeightBudget pins the height half of audit F26's fix:
// the extra clause line is paid for out of the page, so title + blank + rows
// + blank + footer lines + blank + base footer never exceeds the frame
// height, and the base footer still lands inside a 24-row first frame.
func TestLanguageFooterHeightBudget(t *testing.T) {
	cases := []struct{ height, rows int }{
		{24, 12}, // page 17, the whole registry fits with slack
		{22, 12},
		{18, 11}, // page 11
		{12, 5},  // page 5
		{8, 1},   // page clamps at 1, the smallest budgeted frame
	}
	for _, c := range cases {
		app := NewApp(80, c.height, DirLTR)
		app.Push(NewLanguageScreen(i18n.Load("en"), "", nil))
		body := app.Top().Draw(app)
		if got := len(body) + 4; got > c.height { // + title, blank, blank, base footer
			t.Fatalf("height %d: composed frame needs %d lines; body:\n%s", c.height, got, strings.Join(body, "\n"))
		}
		if got := countListRows(body); got != c.rows {
			t.Fatalf("height %d: %d rows rendered, want %d; body:\n%s", c.height, got, c.rows, strings.Join(body, "\n"))
		}
		frame := app.Frame()
		if len(frame.Lines) == 0 || frame.Lines[len(frame.Lines)-1] != baseFooter {
			t.Fatalf("height %d: base footer missing from the frame end; frame:\n%s", c.height, frame.String())
		}
	}
}

// TestLanguageFooterClauseFitsDefaultWidth pins the width half of the same
// check: the clause is its own untruncated footer line, so it must fit the
// default frame width in every catalog, otherwise a terminal wraps it and
// the frame grows by a row no budget counted.
func TestLanguageFooterClauseFitsDefaultWidth(t *testing.T) {
	for _, m := range i18n.Languages() {
		clause := i18n.Load(m.Code).T("help.keys.rows_past_9")
		if w := cellwidth.String(clause); w > defaultFrameWidth {
			t.Fatalf("%s clause is %d cells wide, past the %d-cell default frame", m.Code, w, defaultFrameWidth)
		}
	}
}
