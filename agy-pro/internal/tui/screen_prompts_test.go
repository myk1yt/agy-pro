package tui

import (
	"strconv"
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

// promptsAppAt pushes the prompt library over a stub root at one console
// geometry and returns the screen handle alongside the app.
func promptsAppAt(w, h int) (*App, *promptsScreen) {
	app := NewApp(w, h, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPromptsScreen(nil).(*promptsScreen)
	app.Push(scr)
	return app, scr
}

// TestPromptsBaseFooterSurvivesFullPage pins audit F7 on the prompt library.
// The library holds seven slot rows, so its window fills exactly at the
// heights below: 80x12 and 80x14 cannot show all seven rows, and the pre-fix
// window (height - 5) overran the frame there and dropped the shared base
// footer. The body shape is the same status-plus-list shape the presets list
// uses (title, separator, status line, blank, rows, blank, hint, separator,
// footer), so every case must end with the base footer and keep the hint.
// 80x24 is the standard console and must carry the same chrome.
func TestPromptsBaseFooterSurvivesFullPage(t *testing.T) {
	for _, h := range []int{12, 14, 24} {
		t.Run(strconv.Itoa(h)+"rows", func(t *testing.T) {
			promptTestHome(t)
			app, scr := promptsAppAt(80, h)
			app.CaptureRun(&ScriptedKeys{})

			frame := app.Frame()
			body := strings.Join(frame.Lines, "\n")
			if got, want := scr.list.PageSize(), h-promptsListChrome; got != want {
				t.Fatalf("page size = %d, want %d (80x%d minus %d chrome rows)", got, want, h, promptsListChrome)
			}
			if len(frame.Lines) > h {
				t.Fatalf("frame = %d lines, want at most %d; frames:\n%s", len(frame.Lines), h, body)
			}
			if last := frame.Lines[len(frame.Lines)-1]; last != baseFooter {
				t.Fatalf("base footer cut; last line = %q; frames:\n%s", last, body)
			}
			if !strings.Contains(body, scr.list.FooterHint()) {
				t.Fatalf("list hint missing; frames:\n%s", body)
			}
		})
	}
}

// TestPromptsChromeSurvivesMessageBlock pins the same class for the app
// message line: a library frame that carries a message spends a blank
// separator plus the message line below the body, so the window reserves
// those two rows too. Without the reservation the message and the base footer
// both fell off a full-page frame.
func TestPromptsChromeSurvivesMessageBlock(t *testing.T) {
	promptTestHome(t)
	app, scr := promptsAppAt(80, 14)
	app.SetMessage("Prompt review saved.")
	app.CaptureRun(&ScriptedKeys{})

	frame := app.Frame()
	body := strings.Join(frame.Lines, "\n")
	if !strings.Contains(body, "Prompt review saved.") {
		t.Fatalf("message line cut; frames:\n%s", body)
	}
	if !strings.Contains(body, scr.list.FooterHint()) {
		t.Fatalf("list hint cut by the message block; frames:\n%s", body)
	}
	if last := frame.Lines[len(frame.Lines)-1]; last != baseFooter {
		t.Fatalf("base footer cut by the message block; last line = %q; frames:\n%s", last, body)
	}
	if len(frame.Lines) > 14 {
		t.Fatalf("frame = %d lines, want at most 14; frames:\n%s", len(frame.Lines), body)
	}
}

// TestPromptsLastSlotReachable pins the paging check: at a height where the
// seven slots do not fit the window, End and PgDn both reach the last slot
// (the native seat preamble), the window scrolls to keep it visible, and the
// chrome survives the scroll.
func TestPromptsLastSlotReachable(t *testing.T) {
	promptTestHome(t)
	app, scr := promptsAppAt(80, 14)

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEnd}})
	if got, want := scr.list.Cursor(), len(promptSlots)-1; got != want {
		t.Fatalf("End cursor = %d, want %d (last slot)", got, want)
	}
	if c, o, p := scr.list.Cursor(), scr.list.Offset(), scr.list.PageSize(); c < o || c >= o+p {
		t.Fatalf("last slot %d outside the window [%d,%d) after End", c, o, o+p)
	}
	body := strings.Join(app.Frame().Lines, "\n")
	if !strings.Contains(body, "Native seat preamble") {
		t.Fatalf("last slot row not visible after End; frames:\n%s", body)
	}
	if !strings.Contains(body, baseFooter) || !strings.Contains(body, scr.list.FooterHint()) {
		t.Fatalf("chrome cut after End; frames:\n%s", body)
	}

	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyHome}})
	if c := scr.list.Cursor(); c != 0 {
		t.Fatalf("Home cursor = %d, want 0", c)
	}
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyPgDn}})
	if got, want := scr.list.Cursor(), len(promptSlots)-1; got != want {
		t.Fatalf("PgDn cursor = %d, want %d (last slot)", got, want)
	}
	if c, o, p := scr.list.Cursor(), scr.list.Offset(), scr.list.PageSize(); c < o || c >= o+p {
		t.Fatalf("last slot %d outside the window [%d,%d) after PgDn", c, o, o+p)
	}
}

// TestPromptsCursorSurvivesBadgeFlip pins the selection check on the read-only
// library: every frame re-resolves the slots, so a save from the editor flips a
// row badge and rebuilds the row inventory under the screen. The row order is
// fixed (promptSlots), so the cursor must come back on the slot the user
// opened, with the flipped badge and the new override count on the frame.
func TestPromptsCursorSurvivesBadgeFlip(t *testing.T) {
	promptTestHome(t)
	app := NewApp(110, 30, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	scr := NewPromptsScreen(func(slot string, a *App) {
		a.Push(NewPromptEditorScreen(slot))
	}).(*promptsScreen)
	app.Push(scr)

	// Down lands on the security slot (row 2), Enter opens its editor, the
	// typed text is saved through the confirm gate.
	keys := []Key{KeyDown, KeyEnter}
	keys = append(keys, typeRunes("pinned override")...)
	keys = append(keys, KeyCtrlS, KeyYes)
	app.CaptureRun(&ScriptedKeys{Keys: keys})

	if c := scr.list.Cursor(); c != 1 {
		t.Fatalf("cursor = %d after the editor save, want 1 (security slot)", c)
	}
	body := strings.Join(app.Frame().Lines, "\n")
	if !strings.Contains(body, "security task prompt") {
		t.Fatalf("cursor row not visible after the rebuild; frames:\n%s", body)
	}
	if !strings.Contains(body, "1 of 7 slots have an active override.") {
		t.Fatalf("override count did not follow the save; frames:\n%s", body)
	}
	if !strings.Contains(body, baseFooter) || !strings.Contains(body, scr.list.FooterHint()) {
		t.Fatalf("chrome cut after the badge flip; frames:\n%s", body)
	}
}
