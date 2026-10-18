package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helpTestEnv points AGY_PRO_HOME at a short-lived temp dir (a short
// prefix, so 80-column frames still show the full storage paths) so the
// storage section resolves to deterministic paths.
func helpTestEnv(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "agyp11")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("AGY_PRO_HOME", home)
	return home
}

// newHelpAppAt pushes a help overlay over an optional root stub at the
// given frame height.
func newHelpAppAt(height int, root *stubScreen) *App {
	app := NewApp(80, height, DirLTR)
	if root != nil {
		app.Push(root)
	}
	app.Push(NewHelpScreen())
	return app
}

// newHelpApp pushes a help overlay at a tall frame (everything visible).
func newHelpApp(root *stubScreen) *App {
	return newHelpAppAt(48, root)
}

// TestHelpKeyMapRows pins the full key map from plan-v2 section 4: arrows,
// digits 1-9, Enter, Esc, Tab, Backspace/Delete, PgUp/PgDn, Home/End, y/n,
// Ctrl+C, Ctrl+L, Space, Ctrl+S, EDIT-mode runes, and the rows-past-9
// clause, plus the numbered-selection reminder.
func TestHelpKeyMapRows(t *testing.T) {
	helpTestEnv(t)
	app := newHelpApp(nil)
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{
		"Key Map",
		"1-9", "Digits select the numbered row",
		"↑↓←→", "Arrows move the cursor, Enter activates",
		"Enter", "Activates the cursor row",
		"Esc / Ctrl+C", "Esc goes back, Ctrl+C exits",
		"Tab",
		"Backspace / Delete",
		"PgUp/PgDn", "Scrolls one page up or down",
		"Home/End", "Jumps to the first or last row",
		"y/n", "Answers yes / no confirmation prompts",
		"Ctrl+L", "Redraws the screen",
		"Space",
		"Ctrl+S",
		"a-z 0-9 symbols", "printable runes",
		"10+", "Rows past 9: arrows + Enter reach them",
		"Every list numbers its rows", "digits 1-9 select and activate",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q; frames:\n%s", want, out)
		}
	}
}

// TestHelpStorageLocations pins the storage section: all four locations
// with their resolved paths and purposes.
func TestHelpStorageLocations(t *testing.T) {
	home := helpTestEnv(t)
	app := newHelpApp(nil)
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{
		"Storage Locations",
		"providers.json", filepath.Join(home, "providers.json"), "provider registry",
		"state.json", filepath.Join(home, "state.json"), "profile mode, default profile",
		"fanout-presets.json", filepath.Join(home, "fanout-presets.json"), "saved fanout seat presets",
		"prompts", filepath.Join(home, "prompts"), "custom system prompts",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("storage section missing %q; frames:\n%s", want, out)
		}
	}
}

// TestHelpRTLNote pins the RTL scope note: the title and the four
// documented not-promises (single words, so line wrapping cannot split
// them).
func TestHelpRTLNote(t *testing.T) {
	helpTestEnv(t)
	app := newHelpApp(nil)
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{
		"Right-to-Left (Arabic) Scope",
		"reordering", "shaping", "truncation", "cursor",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing RTL note fragment %q; frames:\n%s", want, out)
		}
	}
}

// TestHelpColorTierNote pins the color-tier note: the detection ladder and
// the AGY_PRO_COLOR override.
func TestHelpColorTierNote(t *testing.T) {
	helpTestEnv(t)
	app := newHelpApp(nil)
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{
		"Color Tier", "AGY_PRO_COLOR", "WT_SESSION", "COLORTERM=truecolor", "NO_COLOR",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing color-tier fragment %q; frames:\n%s", want, out)
		}
	}
}

// TestHelpScrollsByPgDn pins overlay scrolling: the color-tier section sits
// past the first window at a small frame height and reaches it via PgDn;
// scroll keys never close the overlay.
func TestHelpScrollsByPgDn(t *testing.T) {
	helpTestEnv(t)
	baseline := newHelpAppAt(10, nil)
	baselineOut := baseline.CaptureRun(&ScriptedKeys{})
	if strings.Contains(baselineOut, "WT_SESSION") {
		t.Fatalf("color-tier note visible before scrolling; frames:\n%s", baselineOut)
	}

	root := &stubScreen{title: "Hub", mode: ModeNav}
	helpScr := NewHelpScreen()
	app := NewApp(80, 10, DirLTR)
	app.Push(root)
	app.Push(helpScr)
	var keys []Key
	for i := 0; i < 10; i++ {
		keys = append(keys, KeyPgDn)
	}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, "WT_SESSION") {
		t.Fatalf("PgDn did not reach the color-tier note; frames:\n%s", out)
	}
	if app.Depth() != 2 || app.Top() != helpScr {
		t.Fatalf("scroll keys must not close the overlay (depth %d)", app.Depth())
	}
}

// TestHelpClosesOnNonScrollKey pins the close contract: any key other than
// the four scroll keys pops the overlay back to the screen beneath.
func TestHelpClosesOnNonScrollKey(t *testing.T) {
	helpTestEnv(t)
	for _, k := range []Key{KeyEnter, KeyYes, KeySpace, Digit(1), RuneKey('x')} {
		root := &stubScreen{title: "Hub", mode: ModeNav}
		app := newHelpApp(root)
		app.CaptureRun(&ScriptedKeys{Keys: []Key{k}})
		if app.Depth() != 1 || app.Top() != root {
			t.Fatalf("key %q did not close the overlay (depth %d)", k, app.Depth())
		}
	}
}

// TestHelpEscCloses pins the Esc path: the App-level precedence pops the
// overlay before HandleKey ever sees the key.
func TestHelpEscCloses(t *testing.T) {
	helpTestEnv(t)
	root := &stubScreen{title: "Hub", mode: ModeNav}
	app := newHelpApp(root)
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	if app.Depth() != 1 || app.Top() != root {
		t.Fatalf("Esc did not pop the overlay (depth %d)", app.Depth())
	}
}

// TestHelpAnyKeyClosesAtRoot pins the degenerate standalone case: help as
// the root screen has nothing beneath it, so the non-scroll key closes the
// app (the documented Pop-fallback-to-Exit choice) instead of hanging.
func TestHelpAnyKeyClosesAtRoot(t *testing.T) {
	helpTestEnv(t)
	app := newHelpApp(nil)
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnter}})
	if n := strings.Count(out, helpCloseHint); n != 1 {
		t.Fatalf("frame count = %d, want 1 (run must end); frames:\n%s", n, out)
	}
}

// TestHelpTitleAndMode pins the surface contract: NAV mode and the catalog
// title.
func TestHelpTitleAndMode(t *testing.T) {
	scr := NewHelpScreen()
	if scr.Mode() != ModeNav {
		t.Fatalf("help mode = %v, want ModeNav", scr.Mode())
	}
	if got, want := scr.Title(), "Help"; got != want {
		t.Fatalf("help title = %q, want %q", got, want)
	}
}
