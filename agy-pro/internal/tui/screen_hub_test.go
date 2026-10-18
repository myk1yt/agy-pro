package tui

import (
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

// TestHubExitRowQuits pins requirement (e): the exit row calls a.Exit(),
// so the run stops after the exit key and the trailing key is never
// processed (only one frame renders for two scripted keys).
func TestHubExitRowQuits(t *testing.T) {
	out, opened := runHubKeys(t, []Key{Digit(8), Digit(1)})
	if len(opened) != 0 {
		t.Fatalf("exit row reached open with %v; want none", opened)
	}
	if n := strings.Count(out, baseFooter); n != 1 {
		t.Fatalf("frame count = %d, want 1 (exit must stop the run); frames:\n%s", n, out)
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
// settings, doctor, help, language, exit, numbered 1..8. The exit row
// quits instead of opening, so open sees the first seven IDs in order.
func TestHubRowsOrder(t *testing.T) {
	wantIDs := []string{hubIDProviders, hubIDPresets, hubIDPrompts, hubIDSettings, hubIDDoctor, hubIDHelp, hubIDLanguage}
	_, got := runHubKeys(t, []Key{Digit(1), Digit(2), Digit(3), Digit(4), Digit(5), Digit(6), Digit(7), Digit(8)})
	if len(got) != len(wantIDs) {
		t.Fatalf("activated %d rows, want %d: %v", len(got), len(wantIDs), got)
	}
	for i := range wantIDs {
		if got[i] != wantIDs[i] {
			t.Fatalf("row %d ID = %q, want %q (order: %v)", i+1, got[i], wantIDs[i], got)
		}
	}
}
