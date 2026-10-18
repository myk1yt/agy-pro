package tui

import (
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
)

// promptTestHome isolates the prompt store in a temp AGY_PRO_HOME and
// clears both prompt env rungs so the ambient environment cannot leak
// into a test.
func promptTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv(fanout.EnvPromptsDir, "")
	t.Setenv(fanout.EnvSeatPreamble, "")
	return home
}

// newPromptsApp wires the library with an openEditor spy over a stub
// root, so Enter/digit activations and pop flows have somewhere to land.
func newPromptsApp(spy func(slot string, a *App)) (*App, *[]string) {
	var opened []string
	app := NewApp(110, 30, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptsScreen(func(slot string, a *App) {
		opened = append(opened, slot)
		if spy != nil {
			spy(slot, a)
		}
	}))
	return app, &opened
}

// promptsKeys drives keys on the library and returns the captured frames.
func promptsKeys(t *testing.T, keys []Key) (string, *[]string) {
	t.Helper()
	app, opened := newPromptsApp(nil)
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	return out, opened
}

// TestPromptsLibraryListsSevenSlotsWithDefaultBadges pins the row
// inventory with no overrides anywhere: all seven slot rows render, each
// with the compiled-default badge, and the honest empty status line
// replaces the override count.
func TestPromptsLibraryListsSevenSlotsWithDefaultBadges(t *testing.T) {
	promptTestHome(t)
	out, _ := promptsKeys(t, nil)
	for _, want := range []string{
		"review task prompt",
		"security task prompt",
		"plan-audit task prompt",
		"research task prompt",
		"ideas task prompt",
		"custom task prompt",
		"Native seat preamble",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("library frame missing slot row %q; frames:\n%s", want, out)
		}
	}
	// The badge sits at the end of every row line; the status line says
	// "compiled defaults.", which the plain substring would also match.
	if n := strings.Count(out, "compiled default\n"); n != len(promptSlots) {
		t.Fatalf("compiled-default badge count = %d, want %d; frames:\n%s", n, len(promptSlots), out)
	}
	if !strings.Contains(out, "All prompt slots use compiled defaults.") {
		t.Fatalf("library frame missing the empty status line; frames:\n%s", out)
	}
}

// TestPromptsLibraryFileBadgeAfterSave pins the badge flip: once a slot
// file exists the row note reads "file" and the status line counts the
// active override.
func TestPromptsLibraryFileBadgeAfterSave(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("review", "saved override text"); err != nil {
		t.Fatalf("seed review override: %v", err)
	}
	out, _ := promptsKeys(t, nil)
	if !strings.Contains(out, "file") {
		t.Fatalf("library frame missing the file badge; frames:\n%s", out)
	}
	if !strings.Contains(out, "1 of 7 slots have an active override.") {
		t.Fatalf("library frame missing the override count; frames:\n%s", out)
	}
	if strings.Contains(out, "All prompt slots use compiled defaults.") {
		t.Fatalf("empty status line must not render when an override exists; frames:\n%s", out)
	}
}

// TestPromptsLibraryEnvBadgeForSeat pins the env rung: with
// AGY_PRO_SEAT_PREAMBLE set the seat row carries the env badge and
// counts as the one active override.
func TestPromptsLibraryEnvBadgeForSeat(t *testing.T) {
	promptTestHome(t)
	t.Setenv(fanout.EnvSeatPreamble, "inline preamble override")
	out, _ := promptsKeys(t, nil)
	if !strings.Contains(out, "env") {
		t.Fatalf("library frame missing the env badge; frames:\n%s", out)
	}
	if !strings.Contains(out, "1 of 7 slots have an active override.") {
		t.Fatalf("library frame missing the override count; frames:\n%s", out)
	}
	if n := strings.Count(out, "compiled default\n"); n != len(promptSlots)-1 {
		t.Fatalf("compiled-default badge count = %d, want %d; frames:\n%s", n, len(promptSlots)-1, out)
	}
}

// TestPromptsLibraryDualNavigation pins requirement 2 on this list: a
// digit press and arrows+Enter activate rows, and the spy receives the
// canonical slot ID from both input languages.
func TestPromptsLibraryDualNavigation(t *testing.T) {
	promptTestHome(t)
	_, digitOpened := promptsKeys(t, []Key{Digit(7)})
	if len(*digitOpened) != 1 || (*digitOpened)[0] != "seat" {
		t.Fatalf("digit 7 opened %v, want [seat]", *digitOpened)
	}
	_, arrowOpened := promptsKeys(t, []Key{KeyDown, KeyEnter})
	if len(*arrowOpened) != 1 || (*arrowOpened)[0] != "security" {
		t.Fatalf("arrows+Enter opened %v, want [security]", *arrowOpened)
	}
}

// TestPromptsLibraryBadgeFlipsAfterEditorSave drives a full round trip:
// open the editor from the library through the spy callback, save new
// text through the confirm gate, and read the library frame underneath
// for the flipped badge.
func TestPromptsLibraryBadgeFlipsAfterEditorSave(t *testing.T) {
	promptTestHome(t)
	var app *App
	app, opened := newPromptsApp(func(slot string, a *App) {
		a.Push(NewPromptEditorScreen(slot))
	})
	keys := []Key{KeyEnter} // open editor for row 1 (review)
	keys = append(keys, typeRunes("round trip override")...)
	keys = append(keys, KeyCtrlS, KeyYes) // confirm the save gate
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if len(*opened) != 1 || (*opened)[0] != "review" {
		t.Fatalf("library opened editor with %v, want [review]", *opened)
	}
	if got := fanout.TaskPromptSource("review"); got != fanout.PromptSourceFile {
		t.Fatalf("review source after save = %q, want %q", got, fanout.PromptSourceFile)
	}
	// The frame rendered after the pop is the library again: its status
	// line must count the override that the editor just wrote.
	if !strings.Contains(out, "1 of 7 slots have an active override.") {
		t.Fatalf("library frame after save missing the override count; frames:\n%s", out)
	}
}
