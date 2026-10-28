package fanout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/providers"
)

// Prompt store (P9): the user-authored override layer for the seven compiled
// prompt slots (the six task presets in tasks.go plus the native seat-role
// preamble in engine.go). Files live in the prompts directory, one slot per
// file: review.txt, security.txt, plan-audit.txt, research.txt, ideas.txt,
// custom.txt, seat.txt.
//
// Resolution per slot follows the ladder flag > env > file > compiled
// default. The flag rung has no v1 CLI flag and stays open for the TUI
// editor; the env rungs are AGY_PROMPTS_DIR-style directory redirection
// (EnvPromptsDir, task prompts only) and the inline seat preamble
// (EnvSeatPreamble, seat slot only). A slot with no file and no env resolves
// to the compiled constant BYTE-IDENTICALLY, so every preamble byte-identity
// pin stays green with no overrides in force.
//
// The store is read through ResolveTaskPreset / ResolveSeatPreamble and
// written through SaveTaskPrompt / SaveSeatPrompt (0600, atomic,
// create-on-demand, the swarm FileStore pattern: providers.Home for the base
// directory and providers.WriteFileAtomic for the write).

const (
	// EnvPromptsDir redirects the whole prompt store to another directory
	// (directory-only override, decided for v1; a per-task env var was
	// explicitly rejected). Non-empty wins over the default ~/.agy-pro/prompts.
	EnvPromptsDir = "AGY_PRO_PROMPTS_DIR"

	// EnvSeatPreamble overrides the native seat-role preamble with inline
	// text. Non-empty wins over prompts/seat.txt and the compiled
	// nativeSeatPreamble constant.
	EnvSeatPreamble = "AGY_PRO_SEAT_PREAMBLE"
)

// Source rung labels returned by TaskPromptSource and SeatPreambleSource.
// The TUI source badges (P11) render these verbatim.
const (
	PromptSourceCompiled = "compiled default"
	PromptSourceFile     = "file"
	PromptSourceEnv      = "env"
)

// seatPromptFile is the slot file for the native seat-role preamble.
const seatPromptFile = "seat.txt"

// PromptsDir returns the prompt store directory: EnvPromptsDir when set,
// else ~/.agy-pro/prompts (AGY_PRO_HOME aware via providers.Home, the same
// path helper the swarm FileStore uses). The directory is created on demand
// by the save path only; readers never create it.
func PromptsDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvPromptsDir)); v != "" {
		return v, nil
	}
	home, err := providers.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "prompts"), nil
}

// taskSlotName maps a task name to its prompt slot file, mirroring
// GetTaskPreset's normalization (lowercase, trimmed, unknown names fall back
// to review) so the file slot always matches the preset that would run.
func taskSlotName(task string) string {
	name := strings.ToLower(strings.TrimSpace(task))
	switch name {
	case "security", "plan-audit", "research", "ideas", "custom":
		return name
	default:
		return "review"
	}
}

// readPromptFile reads one slot file. found reports whether the file supplied
// the value: a missing file is ("", false, nil) so the compiled default stays
// in force with no error. Every other failure is an honest error naming the
// path, and a zero-byte file is one too: a corrupt slot must never degrade a
// seat silently into an empty prompt.
func readPromptFile(path string) (text string, found bool, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to read prompt file %s: %w", path, rerr)
	}
	if len(data) == 0 {
		return "", false, fmt.Errorf("prompt file %s is empty; write a prompt or delete the file to use the compiled default", path)
	}
	return string(data), true, nil
}

// resolveSlot reads <prompts dir>/<name>. An unresolvable home directory
// degrades to "not found" rather than an error: the prompt layer is an
// optional override and must never break a run that worked before it
// existed (the engine already treats providers.Home the same way for its
// log path). A corrupt or empty slot file IS an error, from readPromptFile.
func resolveSlot(name string) (text string, found bool, err error) {
	dir, derr := PromptsDir()
	if derr != nil {
		return "", false, nil
	}
	return readPromptFile(filepath.Join(dir, name))
}

// ResolveTaskPreset returns the system prompt for a task: the slot file
// prompts/<task>.txt when present, else the compiled GetTaskPreset constant
// byte-identically.
func ResolveTaskPreset(task string) (string, error) {
	if text, found, rerr := resolveSlot(taskSlotName(task) + ".txt"); rerr != nil {
		return "", rerr
	} else if found {
		return text, nil
	}
	return GetTaskPreset(task).SystemPrompt, nil
}

// ResolveSeatPreamble returns the native seat-role preamble: EnvSeatPreamble
// inline text when set, else prompts/seat.txt when present, else the
// compiled nativeSeatPreamble constant byte-identically.
func ResolveSeatPreamble() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvSeatPreamble)); v != "" {
		return v, nil
	}
	if text, found, err := resolveSlot(seatPromptFile); err != nil {
		return "", err
	} else if found {
		return text, nil
	}
	return nativeSeatPreamble, nil
}

// TaskPromptSource reports which rung supplied the task prompt value:
// PromptSourceFile when the slot file exists, PromptSourceCompiled
// otherwise. (A directory redirection via EnvPromptsDir still resolves
// through the file rung; resolution errors surface from ResolveTaskPreset,
// not here.)
func TaskPromptSource(task string) string {
	if _, found, err := resolveSlot(taskSlotName(task) + ".txt"); err == nil && found {
		return PromptSourceFile
	}
	return PromptSourceCompiled
}

// SeatPreambleSource reports which rung supplied the seat preamble value:
// PromptSourceEnv for EnvSeatPreamble, PromptSourceFile for prompts/seat.txt,
// PromptSourceCompiled otherwise.
func SeatPreambleSource() string {
	if strings.TrimSpace(os.Getenv(EnvSeatPreamble)) != "" {
		return PromptSourceEnv
	}
	if _, found, err := resolveSlot(seatPromptFile); err == nil && found {
		return PromptSourceFile
	}
	return PromptSourceCompiled
}

// SaveTaskPrompt writes a task prompt slot (0600, atomic, creating the
// prompts directory on demand) and returns the file path. An empty prompt is
// refused: it would write a slot that fails resolution on every later run.
func SaveTaskPrompt(task, text string) (string, error) {
	return savePromptSlot(taskSlotName(task)+".txt", text)
}

// SaveSeatPrompt writes the native seat preamble slot and returns the file
// path. Same contract as SaveTaskPrompt.
func SaveSeatPrompt(text string) (string, error) {
	return savePromptSlot(seatPromptFile, text)
}

func savePromptSlot(name, text string) (string, error) {
	dir, err := PromptsDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if text == "" {
		return "", fmt.Errorf("refusing to write empty prompt file %s", path)
	}
	if err := providers.WriteFileAtomic(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("failed to write prompt file %s: %w", path, err)
	}
	return path, nil
}
