package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/fanout"
)

// newPromptEditorApp pushes the prompt editor over a stub root so save
// and reset pops have somewhere to land.
func newPromptEditorApp(slot string) *App {
	app := NewApp(110, 40, DirLTR)
	app.Push(&stubScreen{title: "root", mode: ModeNav})
	app.Push(NewPromptEditorScreen(slot))
	return app
}

// TestPromptEditorSeedsCompiledDefaultLabeled pins the no-override view:
// the buffer carries the compiled default text, the label marks it as a
// read-only reference, the mode is ModeEdit, and the seat slot renders
// the anti-nesting guardrail.
func TestPromptEditorSeedsCompiledDefaultLabeled(t *testing.T) {
	promptTestHome(t)
	app := newPromptEditorApp("review")
	out := app.Frame().String()
	if !strings.Contains(out, "You are a Staff Code Reviewer") {
		t.Fatalf("editor frame missing the compiled default seed text; frame:\n%s", out)
	}
	if !strings.Contains(out, "read-only reference") {
		t.Fatalf("editor frame missing the compiled-default label; frame:\n%s", out)
	}
	if !strings.Contains(out, "compiled default") {
		t.Fatalf("editor frame missing the source badge; frame:\n%s", out)
	}
	if top := app.Top(); top == nil || top.Mode() != ModeEdit {
		t.Fatalf("editor mode = %v, want ModeEdit while editing", top.Mode())
	}

	seatApp := newPromptEditorApp("seat")
	seatOut := seatApp.Frame().String()
	if !strings.Contains(seatOut, "anti-nesting control text") {
		t.Fatalf("seat editor frame missing the guardrail line; frame:\n%s", seatOut)
	}
}

// TestPromptEditorSaveWritesAndFlipsBadge drives the full save flow:
// type new text, Ctrl+S, the confirm shows the path and byte count, y
// writes through the store, the screen pops back, and the badge flips to
// file.
func TestPromptEditorSaveWritesAndFlipsBadge(t *testing.T) {
	promptTestHome(t)
	const text = "My custom review prompt."
	app := newPromptEditorApp("review")
	keys := append(typeRunes(text), KeyCtrlS)
	for _, k := range keys {
		app.Step(k)
	}
	// The save gate is live: path and byte count rendered, content not.
	// Typing lands at the top-left caret, so the buffer is the typed text
	// prepended to the compiled default.
	wantText := text + fanout.GetTaskPreset("review").SystemPrompt
	out := app.Frame().String()
	if !strings.Contains(out, "Save prompt?") {
		t.Fatalf("save gate not open after Ctrl+S; frame:\n%s", out)
	}
	if want := "(" + strconv.Itoa(len(wantText)) + " bytes)"; !strings.Contains(out, want) {
		t.Fatalf("save gate missing byte count %q; frame:\n%s", want, out)
	}
	if !strings.Contains(out, "review.txt") {
		t.Fatalf("save gate missing the target path; frame:\n%s", out)
	}
	app.Step(KeyYes)
	if got := fanout.TaskPromptSource("review"); got != fanout.PromptSourceFile {
		t.Fatalf("review source after save = %q, want %q", got, fanout.PromptSourceFile)
	}
	saved, err := fanout.ResolveTaskPreset("review")
	if err != nil {
		t.Fatalf("resolve saved review prompt: %v", err)
	}
	if saved != wantText {
		t.Fatalf("saved prompt = %q, want %q", saved, wantText)
	}
	if top := app.Top(); top == nil || top.Title() != "root" {
		t.Fatalf("top after save = %v, want root screen", top.Title())
	}
	if msg := app.Message(); !strings.Contains(msg, "Prompt saved:") {
		t.Fatalf("message after save = %q, want the saved notice", msg)
	}
}

// TestPromptEditorResetDeletesBehindGate seeds a file override, declines
// the gate once (file stays), then approves it: the file is deleted, the
// compiled constant is back in force, and the reset notice names the
// path.
func TestPromptEditorResetDeletesBehindGate(t *testing.T) {
	promptTestHome(t)
	if _, err := fanout.SaveTaskPrompt("security", "override text"); err != nil {
		t.Fatalf("seed security override: %v", err)
	}
	app := newPromptEditorApp("security")
	for _, k := range []Key{KeyTab, KeyDown, KeyEnter} {
		app.Step(k)
	}
	if top := app.Top(); top.Mode() != ModeNav {
		t.Fatalf("mode on action rows = %v, want ModeNav", top.Mode())
	}
	out := app.Frame().String()
	if !strings.Contains(out, "Restore security task prompt to the compiled default text?") {
		t.Fatalf("reset gate missing; frame:\n%s", out)
	}
	app.Step(KeyNo)
	if _, err := os.Stat(filepath.Join(mustPromptsDir(t), "security.txt")); err != nil {
		t.Fatalf("gate decline must keep the file: %v", err)
	}
	if msg := app.Message(); !strings.Contains(msg, "Cancelled. Nothing was written.") {
		t.Fatalf("message after declined reset = %q", msg)
	}
	for _, k := range []Key{KeyEnter, KeyYes} {
		app.Step(k)
	}
	if _, err := os.Stat(filepath.Join(mustPromptsDir(t), "security.txt")); !os.IsNotExist(err) {
		t.Fatalf("approved reset must delete the file, stat err = %v", err)
	}
	if got := fanout.TaskPromptSource("security"); got != fanout.PromptSourceCompiled {
		t.Fatalf("security source after reset = %q, want %q", got, fanout.PromptSourceCompiled)
	}
	if msg := app.Message(); !strings.Contains(msg, "Override file deleted:") {
		t.Fatalf("message after reset = %q, want the reset notice", msg)
	}
}

// TestPromptEditorResetWithoutFile pins the honest no-op: with no slot
// file the reset row answers with a message instead of a gate.
func TestPromptEditorResetWithoutFile(t *testing.T) {
	promptTestHome(t)
	app := newPromptEditorApp("research")
	for _, k := range []Key{KeyTab, KeyDown, KeyEnter} {
		app.Step(k)
	}
	if app.Modal() != nil {
		t.Fatalf("reset without a file must not open a gate")
	}
	if msg := app.Message(); !strings.Contains(msg, "No override file exists") {
		t.Fatalf("message = %q, want the no-override notice", msg)
	}
}

// TestPromptEditorCredentialGate pins the credential-shaped flow: the
// buffer triggers a warning on the message line and a y/n gate before
// the save confirm; declining writes nothing and keeps the buffer, while
// approving both gates saves the file.
func TestPromptEditorCredentialGate(t *testing.T) {
	promptTestHome(t)
	const secret = "sk-abcdef0123456789"
	app := newPromptEditorApp("ideas")
	for _, k := range append(typeRunes(secret), KeyCtrlS) {
		app.Step(k)
	}
	if msg := app.Message(); !strings.Contains(msg, "looks like secret or key material") {
		t.Fatalf("message line = %q, want the credential warning", msg)
	}
	out := app.Frame().String()
	if !strings.Contains(out, "Save secret-looking text?") {
		t.Fatalf("credential gate not open; frame:\n%s", out)
	}
	// The gate (and the warning) must never echo the buffer. The editor
	// viewport itself shows what the user typed, so scope the assertion
	// to the modal body.
	if app.Modal() == nil {
		t.Fatalf("no modal live at the credential gate")
	}
	if body := strings.Join(app.Modal().Body(app.Width()), "\n"); strings.Contains(body, secret) {
		t.Fatalf("credential gate echoes the buffer content; body:\n%s", body)
	}
	app.Step(KeyNo)
	if _, err := os.Stat(filepath.Join(mustPromptsDir(t), "ideas.txt")); !os.IsNotExist(err) {
		t.Fatalf("declined credential save must not write, stat err = %v", err)
	}
	if !strings.Contains(app.Frame().String(), secret) {
		t.Fatalf("declining the gate must keep the buffer; frame:\n%s", app.Frame().String())
	}
	for _, k := range []Key{KeyCtrlS, KeyYes, KeyYes} {
		app.Step(k)
	}
	if got := fanout.TaskPromptSource("ideas"); got != fanout.PromptSourceFile {
		t.Fatalf("ideas source after approved save = %q, want %q", got, fanout.PromptSourceFile)
	}
}

// TestPromptEditorDirtyEscAsksDiscard pins the Esc-on-dirty guard: the
// App precedence would pop silently, so the guard intercepts Esc and
// asks; declining preserves the buffer and re-arms, approving pops.
func TestPromptEditorDirtyEscAsksDiscard(t *testing.T) {
	promptTestHome(t)
	app := newPromptEditorApp("review")
	for _, k := range typeRunes("x") {
		app.Step(k)
	}
	app.Step(KeyEsc)
	out := app.Frame().String()
	if !strings.Contains(out, "Discard unsaved edits?") {
		t.Fatalf("dirty Esc must open the discard gate; frame:\n%s", out)
	}
	app.Step(KeyNo)
	if !strings.Contains(app.Frame().String(), "xYou are a Staff Code Reviewer") {
		t.Fatalf("declined discard must keep the buffer; frame:\n%s", app.Frame().String())
	}
	app.Step(KeyEsc)
	app.Step(KeyYes)
	if top := app.Top(); top == nil || top.Title() != "root" {
		t.Fatalf("top after approved discard = %v, want root screen", top.Title())
	}
	if got := fanout.TaskPromptSource("review"); got != fanout.PromptSourceCompiled {
		t.Fatalf("review source after discard = %q, want %q", got, fanout.PromptSourceCompiled)
	}
}

// TestPromptEditorSaveConfirmNotesSeatEnv pins the honest env note: with
// AGY_PRO_SEAT_PREAMBLE set, the seat save gate warns that the env rung
// still wins over the file being written.
func TestPromptEditorSaveConfirmNotesSeatEnv(t *testing.T) {
	promptTestHome(t)
	t.Setenv(fanout.EnvSeatPreamble, "inline preamble override")
	app := newPromptEditorApp("seat")
	for _, k := range append(typeRunes("edited preamble"), KeyCtrlS) {
		app.Step(k)
	}
	out := app.Frame().String()
	if !strings.Contains(out, "AGY_PRO_SEAT_PREAMBLE is set") {
		t.Fatalf("seat save gate missing the env note; frame:\n%s", out)
	}
}

// TestPromptTextLooksSecret pins the conservative credential shape:
// natural prompt sentences, URLs, and paths never match; key prefixes
// and opaque mixed tokens do. The token floor is the keyaudit AKIA-class
// 16 runes (aligned with the model form classifier), so a 16-23 rune
// credential triggers the gate while a 39-char identifier-shaped string
// with separators does not.
func TestPromptTextLooksSecret(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain sentence", "You are a Staff Code Reviewer conducting a rigorous review.", false},
		{"long prose with dashes", "Double-blind, cross-verified - cite exact files and line numbers.", false},
		{"url line", "See https://example.com/docs/reference-guide for details.", false},
		{"windows path line", `Read C:\Users\tester\project\prompts\review.txt first.`, false},
		{"short token", "sk-abc", true},
		{"key prefix", "sk-abcdef0123456789", true},
		{"bearer prefix", "Bearer abcdef0123456789", true},
		{"opaque mixed token", "AbCdEf0123456789GhIjKl9876543210", true},
		{"token inside prose", "Use key AbCdEf0123456789GhIjKl9876543210 for the API.", false},
		{"akia class 20 rune", "AKIAIOSFODNN7EXAMPLE", true},
		{"16 rune opaque token", "ABCD1234EFGH5678", true},
		{"23 rune opaque token", "AbCdEf0123456789GhIjKl9", true},
		{"15 rune run stays readable", "AbCdEf012345678", false},
		{"39 char identifier shaped", "meta-llama/Llama-3.3-70B-Instruct-Turbo", false},
		{"long prose with punctuation", "Summarize the findings - files, line numbers, and severity - in plain prose, please.", false},
	}
	for _, tc := range cases {
		if got := promptTextLooksSecret(tc.text); got != tc.want {
			t.Errorf("%s: promptTextLooksSecret(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

// mustPromptsDir resolves the prompt store directory the tests just
// wrote into.
func mustPromptsDir(t *testing.T) string {
	t.Helper()
	dir, err := fanout.PromptsDir()
	if err != nil {
		t.Fatalf("resolve prompts dir: %v", err)
	}
	return dir
}
