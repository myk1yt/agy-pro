package fanout

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// isolatePromptEnv isolates a test from the ambient prompt store: a fresh
// empty AGY_PRO_HOME plus both override env vars unset, so resolution falls
// to the compiled defaults no matter which machine runs the test. Tests that
// pin the compiled constants call this; tests that exercise overrides set
// the env vars themselves after calling it.
func isolatePromptEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv(EnvPromptsDir, "")
	t.Setenv(EnvSeatPreamble, "")
	return home
}

// writeSlot writes a prompt slot file directly into home/prompts.
func writeSlot(t *testing.T, home, name, text string) string {
	t.Helper()
	path := filepath.Join(home, "prompts", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- byte identity with no overrides ----------------------------------------

// TestResolvePrompts_ByteIdentityNoOverrides is the explicit resolver twin of
// the preamble byte-identity pins: with no file and no env, every slot
// resolves to the compiled constant exactly.
func TestResolvePrompts_ByteIdentityNoOverrides(t *testing.T) {
	isolatePromptEnv(t)
	for _, task := range []string{"review", "security", "plan-audit", "research", "ideas", "custom"} {
		got, err := ResolveTaskPreset(task)
		if err != nil {
			t.Fatalf("ResolveTaskPreset(%q): %v", task, err)
		}
		if want := GetTaskPreset(task).SystemPrompt; got != want {
			t.Errorf("ResolveTaskPreset(%q) drifted from the compiled constant:\n got %q\nwant %q", task, got, want)
		}
		if src := TaskPromptSource(task); src != PromptSourceCompiled {
			t.Errorf("TaskPromptSource(%q) = %q; want %q", task, src, PromptSourceCompiled)
		}
	}
	seat, err := ResolveSeatPreamble()
	if err != nil {
		t.Fatalf("ResolveSeatPreamble: %v", err)
	}
	if seat != nativeSeatPreamble {
		t.Errorf("ResolveSeatPreamble drifted from the compiled constant:\n got %q\nwant %q", seat, nativeSeatPreamble)
	}
	if src := SeatPreambleSource(); src != PromptSourceCompiled {
		t.Errorf("SeatPreambleSource() = %q; want %q", src, PromptSourceCompiled)
	}

	// The assembled native seat prompt matches the pre-store concatenation
	// byte-for-byte.
	got, err := buildNativeSeatPrompt("security", "user text")
	if err != nil {
		t.Fatalf("buildNativeSeatPrompt: %v", err)
	}
	want := nativeSeatPreamble + "\n\n" + GetTaskPreset("security").SystemPrompt + "\n\n" + "user text"
	if got != want {
		t.Errorf("buildNativeSeatPrompt drifted from the compiled concatenation:\n got %q\nwant %q", got, want)
	}
}

// --- file override per slot --------------------------------------------------

func TestResolveTaskPreset_FileOverride(t *testing.T) {
	home := isolatePromptEnv(t)
	text := "file-authored security prompt\nwith two lines"
	writeSlot(t, home, "security.txt", text)

	got, err := ResolveTaskPreset("security")
	if err != nil {
		t.Fatalf("ResolveTaskPreset(security): %v", err)
	}
	if got != text {
		t.Errorf("file override not applied verbatim:\n got %q\nwant %q", got, text)
	}
	if src := TaskPromptSource("security"); src != PromptSourceFile {
		t.Errorf("TaskPromptSource(security) = %q; want %q", src, PromptSourceFile)
	}
	// Sibling slots stay on the compiled default.
	if got, err := ResolveTaskPreset("review"); err != nil || got != GetTaskPreset("review").SystemPrompt {
		t.Errorf("review must stay on the compiled default; got %q, err %v", got, err)
	}
}

func TestResolveSeatPreamble_FileOverride(t *testing.T) {
	home := isolatePromptEnv(t)
	text := "custom seat role from file"
	writeSlot(t, home, "seat.txt", text)

	got, err := ResolveSeatPreamble()
	if err != nil {
		t.Fatalf("ResolveSeatPreamble: %v", err)
	}
	if got != text {
		t.Errorf("file override not applied verbatim:\n got %q\nwant %q", got, text)
	}
	if src := SeatPreambleSource(); src != PromptSourceFile {
		t.Errorf("SeatPreambleSource() = %q; want %q", src, PromptSourceFile)
	}
}

// --- env override precedence over file ---------------------------------------

func TestResolveSeatPreamble_EnvBeatsFile(t *testing.T) {
	home := isolatePromptEnv(t)
	writeSlot(t, home, "seat.txt", "file text")
	t.Setenv(EnvSeatPreamble, "inline env text")

	got, err := ResolveSeatPreamble()
	if err != nil {
		t.Fatalf("ResolveSeatPreamble: %v", err)
	}
	if got != "inline env text" {
		t.Errorf("env rung must beat the file rung; got %q", got)
	}
	if src := SeatPreambleSource(); src != PromptSourceEnv {
		t.Errorf("SeatPreambleSource() = %q; want %q", src, PromptSourceEnv)
	}
}

func TestResolveTaskPreset_PromptsDirEnvRedirection(t *testing.T) {
	isolatePromptEnv(t)
	redirect := t.TempDir()
	if err := os.MkdirAll(redirect, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirect, "research.txt"), []byte("redirected research"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPromptsDir, redirect)

	got, err := ResolveTaskPreset("research")
	if err != nil {
		t.Fatalf("ResolveTaskPreset(research): %v", err)
	}
	if got != "redirected research" {
		t.Errorf("AGY_PRO_PROMPTS_DIR redirection not applied; got %q", got)
	}
	if src := TaskPromptSource("research"); src != PromptSourceFile {
		t.Errorf("TaskPromptSource(research) = %q; want %q (env redirects the directory; the file still supplies the value)", src, PromptSourceFile)
	}
	if dir, err := PromptsDir(); err != nil || dir != redirect {
		t.Errorf("PromptsDir() = %q, %v; want the env value %q", dir, err, redirect)
	}
}

// --- missing directory: defaults in force, no error ---------------------------

func TestResolvePrompts_MissingDirDefaultsSilently(t *testing.T) {
	isolatePromptEnv(t) // fresh home, no prompts dir at all
	for _, task := range []string{"review", "custom"} {
		got, err := ResolveTaskPreset(task)
		if err != nil {
			t.Errorf("missing prompts dir must not error for %q: %v", task, err)
		}
		if got != GetTaskPreset(task).SystemPrompt {
			t.Errorf("missing prompts dir must leave %q on the compiled default", task)
		}
	}
	if got, err := ResolveSeatPreamble(); err != nil || got != nativeSeatPreamble {
		t.Errorf("missing prompts dir must leave the seat preamble on the compiled constant; got %q, err %v", got, err)
	}
	// A redirection env pointing at a non-existent directory behaves the same.
	t.Setenv(EnvPromptsDir, filepath.Join(t.TempDir(), "does-not-exist"))
	if got, err := ResolveTaskPreset("review"); err != nil || got != GetTaskPreset("review").SystemPrompt {
		t.Errorf("missing redirected dir must fall back to the compiled default; got %q, err %v", got, err)
	}
}

// --- corrupt / unreadable slot files: honest error naming the path ------------

func TestResolvePrompts_EmptyFileErrorsNamingPath(t *testing.T) {
	home := isolatePromptEnv(t)
	emptyPath := writeSlot(t, home, "review.txt", "")

	_, err := ResolveTaskPreset("review")
	if err == nil {
		t.Fatal("empty slot file must error, never resolve to a silent empty prompt")
	}
	if !strings.Contains(err.Error(), emptyPath) {
		t.Errorf("error must name the path %q: %v", emptyPath, err)
	}
	// The corrupt review slot must not break the unrelated seat slot.
	if _, err := ResolveSeatPreamble(); err != nil {
		t.Errorf("unrelated corrupt slot broke the seat preamble: %v", err)
	}
}

func TestResolveSeatPreamble_EmptyFileErrorsNamingPath(t *testing.T) {
	home := isolatePromptEnv(t)
	emptyPath := writeSlot(t, home, "seat.txt", "")

	_, err := ResolveSeatPreamble()
	if err == nil {
		t.Fatal("empty seat.txt must error, never resolve to a silent empty prompt")
	}
	if !strings.Contains(err.Error(), emptyPath) {
		t.Errorf("error must name the path %q: %v", emptyPath, err)
	}
}

func TestResolvePrompts_UnreadableFileErrorsNamingPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod is not enforced on Windows; the empty-file and directory-slot cases cover the honest-error path")
	}
	home := isolatePromptEnv(t)
	path := writeSlot(t, home, "ideas.txt", "secret-ish content")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := ResolveTaskPreset("ideas")
	if err == nil {
		t.Fatal("unreadable slot file must error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the path %q: %v", path, err)
	}
}

// TestResolvePrompts_DirectoryAsSlotErrorsNamingPath covers the unreadable-
// slot class on every OS (including Windows, where chmod is not enforced): a
// directory sitting where the slot file belongs makes os.ReadFile fail, and
// the failure must surface as an honest error naming the path.
func TestResolvePrompts_DirectoryAsSlotErrorsNamingPath(t *testing.T) {
	home := isolatePromptEnv(t)
	dirPath := filepath.Join(home, "prompts", "custom.txt")
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveTaskPreset("custom")
	if err == nil {
		t.Fatal("a directory in place of the slot file must error")
	}
	if !strings.Contains(err.Error(), dirPath) {
		t.Errorf("error must name the path %q: %v", dirPath, err)
	}
}

// --- source labels per rung ---------------------------------------------------

func TestPromptSourceLabels(t *testing.T) {
	home := isolatePromptEnv(t)

	// Default rung.
	if got := TaskPromptSource("review"); got != PromptSourceCompiled {
		t.Errorf("no file: TaskPromptSource = %q; want %q", got, PromptSourceCompiled)
	}
	if got := SeatPreambleSource(); got != PromptSourceCompiled {
		t.Errorf("no env, no file: SeatPreambleSource = %q; want %q", got, PromptSourceCompiled)
	}

	// File rung.
	writeSlot(t, home, "review.txt", "x")
	writeSlot(t, home, "seat.txt", "y")
	if got := TaskPromptSource("review"); got != PromptSourceFile {
		t.Errorf("with file: TaskPromptSource = %q; want %q", got, PromptSourceFile)
	}
	if got := SeatPreambleSource(); got != PromptSourceFile {
		t.Errorf("with file: SeatPreambleSource = %q; want %q", got, PromptSourceFile)
	}

	// Env rung for the seat preamble.
	t.Setenv(EnvSeatPreamble, "z")
	if got := SeatPreambleSource(); got != PromptSourceEnv {
		t.Errorf("with env: SeatPreambleSource = %q; want %q", got, PromptSourceEnv)
	}
}

// --- save path ----------------------------------------------------------------

func TestSavePromptSlots_Atomic0600RoundTrip(t *testing.T) {
	home := isolatePromptEnv(t)

	path, err := SaveTaskPrompt("plan-audit", "saved plan-audit prompt")
	if err != nil {
		t.Fatalf("SaveTaskPrompt: %v", err)
	}
	want := filepath.Join(home, "prompts", "plan-audit.txt")
	if path != want {
		t.Errorf("SaveTaskPrompt path = %q; want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved slot: %v", err)
	}
	// Windows reports only the read-only attribute (0666 for a writable
	// file); real modes live in the ACL. Same convention as
	// providers/model_ops_test.go.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("saved slot perm = %o; want 600", info.Mode().Perm())
	}
	got, err := ResolveTaskPreset("plan-audit")
	if err != nil {
		t.Fatalf("ResolveTaskPreset after save: %v", err)
	}
	if got != "saved plan-audit prompt" {
		t.Errorf("round trip = %q; want the saved text", got)
	}

	seatPath, err := SaveSeatPrompt("saved seat preamble")
	if err != nil {
		t.Fatalf("SaveSeatPrompt: %v", err)
	}
	if seatPath != filepath.Join(home, "prompts", "seat.txt") {
		t.Errorf("SaveSeatPrompt path = %q", seatPath)
	}
	if got, err := ResolveSeatPreamble(); err != nil || got != "saved seat preamble" {
		t.Errorf("seat round trip = %q, err %v", got, err)
	}
}

func TestSaveTaskPrompt_RefusesEmpty(t *testing.T) {
	home := isolatePromptEnv(t)
	if _, err := SaveTaskPrompt("review", ""); err == nil {
		t.Fatal("saving an empty prompt must error (it would write a slot that fails resolution later)")
	}
	if _, err := SaveSeatPrompt(""); err == nil {
		t.Fatal("saving an empty seat preamble must error")
	}
	// The refusal must not have created the slot files.
	if _, err := os.Stat(filepath.Join(home, "prompts", "review.txt")); !os.IsNotExist(err) {
		t.Errorf("empty save left a review.txt behind (err %v)", err)
	}
}

func TestSaveTaskPrompt_TaskNameNormalization(t *testing.T) {
	home := isolatePromptEnv(t)
	// Mixed case and whitespace follow GetTaskPreset's normalization.
	if _, err := SaveTaskPrompt("  SECURITY ", "norm"); err != nil {
		t.Fatalf("SaveTaskPrompt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "prompts", "security.txt")); err != nil {
		t.Errorf("slot file must be security.txt: %v", err)
	}
	if got, err := ResolveTaskPreset("Security"); err != nil || got != "norm" {
		t.Errorf("ResolveTaskPreset(Security) = %q, err %v; want the security slot file", got, err)
	}
	// Unknown task names fall back to the review slot, matching GetTaskPreset.
	if _, err := SaveTaskPrompt("not-a-task", "review-slot"); err != nil {
		t.Fatalf("SaveTaskPrompt: %v", err)
	}
	if got, err := ResolveTaskPreset("not-a-task"); err != nil || got != "review-slot" {
		t.Errorf("unknown task must resolve through the review slot; got %q, err %v", got, err)
	}
}
