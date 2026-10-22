package tui

import (
	"fmt"
	"strings"
	"testing"

	"agypro/internal/i18n"
)

// hubFixture is the shared fake counts the hub tests read.
var hubFixture = HubCounts{
	Providers:       2,
	Presets:         3,
	PromptOverrides: 4,
	RegistryOK:      true,
	Language:        "en",
}

// newHubApp wires a hub screen with the given open spy into a fresh app.
func newHubApp(open func(id string, a *App)) *App {
	scr := NewHubScreen(func() HubCounts { return hubFixture }, open)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	return app
}

// runHubKeys drives one capture run over the scripted keys and returns the
// captured frames plus the IDs the open spy saw.
func runHubKeys(t *testing.T, keys []Key) (string, []string) {
	t.Helper()
	var opened []string
	app := newHubApp(func(id string, a *App) { opened = append(opened, id) })
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	return out, opened
}

// TestHubDigitActivatesRow pins requirement (a): a digit press selects AND
// activates the matching row, calling open with that row's ID.
func TestHubDigitActivatesRow(t *testing.T) {
	cases := []struct {
		digit Key
		id    string
	}{
		{Digit(1), hubIDProviders},
		{Digit(3), hubIDPrompts},
		{Digit(7), hubIDLanguage},
		{Digit(8), hubIDDashboard},
		{Digit(9), hubIDIntegrations},
	}
	for _, tc := range cases {
		_, opened := runHubKeys(t, []Key{tc.digit})
		if len(opened) != 1 || opened[0] != tc.id {
			t.Fatalf("digit %q opened %v, want [%s]", tc.digit, opened, tc.id)
		}
	}
}

// TestHubArrowEnterDualNavigation pins requirement (b): arrows plus Enter
// activate the same row digits do (the dual-navigation structural pin).
func TestHubArrowEnterDualNavigation(t *testing.T) {
	_, digitOpened := runHubKeys(t, []Key{Digit(3)})
	_, arrowOpened := runHubKeys(t, []Key{KeyDown, KeyDown, KeyEnter})
	if len(digitOpened) != 1 || len(arrowOpened) != 1 {
		t.Fatalf("digit opened %v, arrows opened %v; want one row each", digitOpened, arrowOpened)
	}
	if digitOpened[0] != arrowOpened[0] {
		t.Fatalf("digit opened %q, arrows opened %q; want the same row", digitOpened[0], arrowOpened[0])
	}
	if digitOpened[0] != hubIDPrompts {
		t.Fatalf("both paths opened %q, want %q", digitOpened[0], hubIDPrompts)
	}
}

// TestHubHealthSummary pins requirement (c): the summary line above the
// list carries the provider count, preset count, active prompt override
// count, registry status, and current language.
func TestHubHealthSummary(t *testing.T) {
	out, _ := runHubKeys(t, nil)
	for _, want := range []string{"2 providers", "3 presets", "4 prompt overrides", missingRegistryOK, "English"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q; frames:\n%s", want, out)
		}
	}

	failFixture := hubFixture
	failFixture.RegistryOK = false
	var app2out string
	{
		scr := NewHubScreen(func() HubCounts { return failFixture }, nil)
		app := NewApp(80, 24, DirLTR)
		app.Push(scr)
		app2out = app.CaptureRun(&ScriptedKeys{})
	}
	if !strings.Contains(app2out, missingRegistryFail) {
		t.Fatalf("registry-fail summary missing %q; frames:\n%s", missingRegistryFail, app2out)
	}
}

// TestHubLanguageRowNote pins requirement (d): the language row shows the
// current language endonym as its note.
func TestHubLanguageRowNote(t *testing.T) {
	c := hubFixture
	c.Language = "ko"
	scr := NewHubScreen(func() HubCounts { return c }, nil)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, i18n.Endonym("ko")) {
		t.Fatalf("language row note missing endonym %q; frames:\n%s", i18n.Endonym("ko"), out)
	}
}

// TestHubFrameFitsTenRowsWithBanner is the P5-3 T8 frame-fit measurement: with
// the Integrations row the hub carries ten rows, and at the default 80x24 console
// geometry the compact banner (the variant bannerFor picks for rows>=22) still
// leaves room for all ten numbered rows plus both legends. The assertion is on
// the composed frame, so it measures what the operator actually sees.
func TestHubFrameFitsTenRowsWithBanner(t *testing.T) {
	app := newHubApp(nil)
	cap := Cap{Tier: TierPlain, Palette: DefaultPalette()}
	banner := bannerFor(80, 24, cap, "tagline")
	if len(banner) == 0 {
		t.Fatalf("bannerFor(80,24) returned no banner; the 80x24 console frame carries one")
	}
	frame := composeFrame(app, banner).frame
	if len(frame.Lines) > 24 {
		t.Fatalf("frame lines = %d, want <= 24:\n%s", len(frame.Lines), frame.String())
	}
	t.Logf("measured: %d banner line(s) + frame %d of 24 lines", len(banner), len(frame.Lines))
	body := frame.String()
	for row := 1; row <= 10; row++ {
		want := fmt.Sprintf("[%2d]", row)
		if !strings.Contains(body, want) {
			t.Fatalf("row marker %q is not on the 80x24 frame:\n%s", want, body)
		}
	}
	if !strings.Contains(body, baseFooter) {
		t.Fatalf("the base footer was cut:\n%s", body)
	}
}

// TestHubExitRowQuits pins requirement (e): the exit row calls a.Exit(),
// so the run stops after the exit key and the trailing key is never
// processed (only one frame renders for two scripted keys).
//
// P5-3 T8 moved Exit to row 10 (the Integrations row landed at 9), so the exit
// row is reached through the shared list's 0-prefix compose: 0 then 1 then 0
// spells row 10. That is the documented multi-digit gesture, not a second
// navigation path (stepList.HandleKey owns it), so the frame count still proves
// the exit stopped the run: three keys are consumed (one frame painted before
// each) and the fourth key, which would have opened row 1 again, is never read.
func TestHubExitRowQuits(t *testing.T) {
	out, opened := runHubKeys(t, []Key{Key("0"), Digit(1), Key("0"), Digit(1)})
	if len(opened) != 0 {
		t.Fatalf("exit row reached open with %v; want none", opened)
	}
	if n := strings.Count(out, baseFooter); n != 3 {
		t.Fatalf("frame count = %d, want 3 (the exit key must stop the run before the trailing key); frames:\n%s", n, out)
	}
}

// TestHubTitleAndMode pins the surface contract: NAV mode and the catalog
// title in the current language.
func TestHubTitleAndMode(t *testing.T) {
	scr := NewHubScreen(func() HubCounts { return hubFixture }, nil)
	if scr.Mode() != ModeNav {
		t.Fatalf("hub mode = %v, want ModeNav", scr.Mode())
	}
	if got, want := scr.Title(), i18n.Load("en").T("menu.title"); got != want {
		t.Fatalf("hub title = %q, want %q", got, want)
	}
}

// TestHubRowsOrder pins the row inventory: providers, presets, prompts,
// settings, doctor, help, language, dashboard, integrations, exit, numbered
// 1..10. P5-3 T8 added the Integrations row at 9, so every single-digit
// shortcut that existed keeps its meaning and only Exit moved, to 10; rows past
// 9 are reached with the shared list's 0-prefix compose (one navigation path,
// not a second one). The exit row quits instead of opening, so open sees the
// first nine IDs in order and nothing for row 10.
func TestHubRowsOrder(t *testing.T) {
	wantIDs := []string{hubIDProviders, hubIDPresets, hubIDPrompts, hubIDSettings, hubIDDoctor, hubIDHelp, hubIDLanguage, hubIDDashboard, hubIDIntegrations}
	_, got := runHubKeys(t, []Key{Digit(1), Digit(2), Digit(3), Digit(4), Digit(5), Digit(6), Digit(7), Digit(8), Key("0"), Digit(9)})
	if len(got) != len(wantIDs) {
		t.Fatalf("activated %d rows, want %d: %v", len(got), len(wantIDs), got)
	}
	for i := range wantIDs {
		if got[i] != wantIDs[i] {
			t.Fatalf("row %d ID = %q, want %q (order: %v)", i+1, got[i], wantIDs[i], got)
		}
	}
	if _, opened := runHubKeys(t, []Key{Key("0"), Digit(1), Key("0")}); len(opened) != 0 {
		t.Fatalf("row 10 reached open with %v; want the exit row", opened)
	}
}

// TestHubCursorSurvivesLanguageChange pins the F21 fix: syncRows rebuilds the
// row labels when the UI language changes, and the cursor index survives that
// rebuild instead of snapping back to row 1. The rows themselves still refresh
// to the new language, so the position is kept by index and not by freezing
// the old list.
func TestHubCursorSurvivesLanguageChange(t *testing.T) {
	lang := "en"
	scr := NewHubScreen(func() HubCounts {
		c := hubFixture
		c.Language = lang
		return c
	}, nil)
	hub, ok := scr.(*hubScreen)
	if !ok {
		t.Fatalf("NewHubScreen returned %T, want *hubScreen", scr)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(hub)

	app.Frame() // the first draw builds the English rows
	// Row 7 is the Language row; six Down presses are the position a language
	// round trip must hand back to the operator.
	for i := 0; i < 6; i++ {
		hub.HandleKey(KeyDown, app)
	}
	if got := hub.list.Cursor(); got != 6 {
		t.Fatalf("cursor = %d after six Down presses, want 6", got)
	}
	before := app.Frame().String()
	if line := hubFrameRowLine(t, before, 7); !strings.HasPrefix(line, "▶ ") {
		t.Fatalf("cursor marker is not on row 7 before the language change: %q", line)
	}

	lang = "ko"
	frame := app.Frame().String()

	if got := hub.list.Cursor(); got != 6 {
		t.Fatalf("cursor = %d after the language change, want 6 (syncRows must keep the position); frames:\n%s", got, frame)
	}
	if got, want := hub.list.Row(0).Label, i18n.Load("ko").T("menu.providers"); got != want {
		t.Fatalf("row 1 label = %q, want %q (rows must still refresh)", got, want)
	}
	if got, want := hub.list.Row(6).Label, i18n.Load("ko").T("view.appearance.language.label"); got != want {
		t.Fatalf("language row label = %q, want %q (rows must still refresh)", got, want)
	}
	if n := strings.Count(frame, "▶ "); n != 1 {
		t.Fatalf("frame carries %d cursor markers, want exactly 1; frames:\n%s", n, frame)
	}
	line := hubFrameRowLine(t, frame, 7)
	if !strings.HasPrefix(line, "▶ ") {
		t.Fatalf("cursor marker left row 7 after the rebuild: %q\n%s", line, frame)
	}
	if !strings.Contains(line, i18n.Endonym("ko")) {
		t.Fatalf("row 7 does not carry the new endonym %q: %q", i18n.Endonym("ko"), line)
	}
}

// hubFrameRowLine returns the rendered frame line carrying the given 1-based
// row number (the "[ 7]" column stepList.Render writes).
func hubFrameRowLine(t *testing.T, frame string, n int) string {
	t.Helper()
	marker := fmt.Sprintf("[%2d]", n)
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("no frame line carries row %d (%s); frames:\n%s", n, marker, frame)
	return ""
}
