package tui

import (
	"fmt"
	"strings"
	"testing"

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
// carries the "(logical order)" qualifier in every language.
func TestLanguageArabicQualifier(t *testing.T) {
	out, _ := newLanguageApp("", nil)
	qualifier := i18n.Load("en").T("lang.logical_order")
	if qualifier == "lang.logical_order" {
		t.Fatalf("lang.logical_order missing from the en catalog")
	}
	want := i18n.Endonym("ar") + " (Arabic) " + qualifier
	if !strings.Contains(out, want) {
		t.Fatalf("Arabic row missing %q; frames:\n%s", want, out)
	}
}

// TestLanguageFooterHint pins the footer: the first-run hint on a fresh run,
// the keep line when a language is already stored, never both.
func TestLanguageFooterHint(t *testing.T) {
	b := i18n.Load("en")
	hint, keep := b.T("lang.screen.hint"), b.T("lang.screen.keep")

	first, _ := newLanguageApp("", nil)
	if !strings.Contains(first, hint) {
		t.Fatalf("first run missing hint %q; frames:\n%s", hint, first)
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
