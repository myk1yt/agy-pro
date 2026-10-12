// Package mode manages profile switching between full-stack (orchestrator rules,
// specialized subagent skills, and plugins) and clean (minimal vanilla) profiles.
// All switches are atomic, non-destructive, and automatically backed up.
package mode

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agypro/internal/paths"
	"agypro/profiles"
)

const (
	ModeFull  = "full"
	ModeClean = "clean"
)

// Default launch profiles stored in State.DefaultProfile. They control what
// a plain `agy` invocation does before the real binary runs:
// prompt shows the interactive fast launcher, native runs straight through,
// gateway starts the local LLM bridge without asking.
const (
	ProfilePrompt  = "prompt"
	ProfileNative  = "native"
	ProfileGateway = "gateway"
)

// DefaultBridgeTools is the tool allow-list the bridge tool loop exposes when
// neither AGY_PRO_BRIDGE_TOOLS nor the stored bridge_tools key is set: the
// read-only, search-only surface. write and exec stay opt-in.
const DefaultBridgeTools = "read,search"

// BridgeToolsNone is the stored bridge_tools value that disables the tool loop
// entirely (agy sees plain passthrough).
const BridgeToolsNone = "none"

// DefaultNativeSeatConcurrency is the default cap on simultaneously running
// native (real-agy subprocess) fanout seats; excess native seats queue
// (contract §2.3).
const DefaultNativeSeatConcurrency = 2

// ValidProfile reports whether s names a supported default launch profile.
func ValidProfile(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ProfilePrompt, ProfileNative, ProfileGateway:
		return true
	}
	return false
}

// NormalizeProfile canonicalizes a user-supplied profile name.
// Unknown values fall back to ProfilePrompt.
func NormalizeProfile(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ProfileNative:
		return ProfileNative
	case ProfileGateway:
		return ProfileGateway
	default:
		return ProfilePrompt
	}
}

// DefaultManagedItems lists the top-level entries under the Gemini config
// directory owned by a profile switch: each is moved to the timestamped
// backup before being replaced, and removed when the active profile does
// not deploy it. agents, references, and profile.json ship with the full
// profile, so they belong to the managed set; hooks and agy-native files
// (import_manifest.json, .migrated) are intentionally left untouched.
var DefaultManagedItems = []string{
	"rules",
	"skills",
	"plugins",
	"agents",
	"references",
	"profile.json",
	"config.json",
}

// State represents the serialized agy-pro configuration state in ~/.agy-pro/state.json.
type State struct {
	CurrentMode  string    `json:"current_mode"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastBackup   string    `json:"last_backup,omitempty"`
	ManagedItems []string  `json:"managed_items"`
	// DefaultProfile is the plain-`agy` launch profile (prompt|native|gateway).
	// Additive key: state files written before it existed keep loading and
	// re-saving with the original four keys intact.
	DefaultProfile string `json:"default_profile,omitempty"`
	// BridgeTools is the gateway tool-loop allow-list ("read,search,write,exec"
	// subset, canonical order, or "none"). Additive key with the same
	// forward-compatibility contract as DefaultProfile: absent means
	// DefaultBridgeTools at run time.
	BridgeTools string `json:"bridge_tools,omitempty"`
	// NativeSeatConcurrency caps how many native (real-agy subprocess)
	// fanout seats run at the same time; excess native seats queue
	// (contract §2.3). Additive key with the same forward-compatibility
	// contract as DefaultProfile: absent means DefaultNativeSeatConcurrency.
	NativeSeatConcurrency int `json:"native_seat_concurrency,omitempty"`
}

// EffectiveDefaultProfile returns the stored launch profile, or ProfilePrompt
// when the key is missing (pre-existing state files) or unrecognized.
func (s *State) EffectiveDefaultProfile() string {
	if s == nil {
		return ProfilePrompt
	}
	return NormalizeProfile(s.DefaultProfile)
}

// EffectiveBridgeTools returns the stored tool-loop allow-list, or
// DefaultBridgeTools when the key is missing (pre-existing state files) or
// blank. The stored value is validated by the config subcommand before it is
// saved; the launcher still re-parses it fail-closed at run time.
func (s *State) EffectiveBridgeTools() string {
	if s == nil {
		return DefaultBridgeTools
	}
	if trimmed := strings.TrimSpace(s.BridgeTools); trimmed != "" {
		return trimmed
	}
	return DefaultBridgeTools
}

// EffectiveNativeSeatConcurrency returns the stored native-seat concurrency
// cap, or DefaultNativeSeatConcurrency when the key is missing/invalid, and
// clamps the result to 1..8 (contract §2.3).
func (s *State) EffectiveNativeSeatConcurrency() int {
	if s == nil {
		return DefaultNativeSeatConcurrency
	}
	v := s.NativeSeatConcurrency
	if v <= 0 {
		return DefaultNativeSeatConcurrency
	}
	if v > 8 {
		return 8
	}
	return v
}

// LoadState reads the state file. If missing, returns a default state.
func LoadState() (*State, error) {
	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{
				CurrentMode:  "clean",
				UpdatedAt:    time.Now().UTC(),
				ManagedItems: append([]string(nil), DefaultManagedItems...),
			}, nil
		}
		return nil, fmt.Errorf("failed to read state file %q: %w", stateFile, err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file %q: %w", stateFile, err)
	}
	if len(state.ManagedItems) == 0 {
		state.ManagedItems = append([]string(nil), DefaultManagedItems...)
	} else {
		state.ManagedItems = mergeManagedItems(state.ManagedItems)
	}
	return &state, nil
}

// mergeManagedItems returns the stored manifest with every default item that
// was introduced after the state file was written appended to it. State files
// written by older builds list only the four legacy items; merging on load
// keeps them forward-compatible without touching the file on disk. The next
// Switch persists the merged set.
func mergeManagedItems(stored []string) []string {
	known := make(map[string]bool, len(stored))
	for _, item := range stored {
		known[item] = true
	}
	merged := append([]string(nil), stored...)
	for _, item := range DefaultManagedItems {
		if !known[item] {
			merged = append(merged, item)
		}
	}
	return merged
}

// SaveState saves state to ~/.agy-pro/state.json atomically.
func SaveState(state *State) error {
	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize state: %w", err)
	}

	return paths.AtomicWriteFile(stateFile, append(data, '\n'), 0o644)
}

// Switch switches the active profile in ~/.gemini/config to targetMode ("full" or "clean").
// If targetMode is empty, prompts the user via in/out.
func Switch(targetMode string, in io.Reader, out io.Writer) error {
	mode := strings.ToLower(strings.TrimSpace(targetMode))

	// Interactive prompt if mode not given
	if mode == "" {
		fmt.Fprintln(out, "===========================================================")
		fmt.Fprintln(out, "  agy-pro Profile Switcher")
		fmt.Fprintln(out, "===========================================================")
		fmt.Fprintln(out, "Select environment profile mode:")
		fmt.Fprintln(out, "  1. full-stack     (VP Orchestrator rules, specialized agent skills & plugins)")
		fmt.Fprintln(out, "  2. default (clean) (Minimal clean baseline environment)")
		fmt.Fprint(out, "Choice [1/2]: ")

		reader := bufio.NewReader(in)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("failed reading choice: %w", err)
		}
		choice := strings.TrimSpace(line)
		switch choice {
		case "1", "full", "full-stack":
			mode = ModeFull
		case "2", "clean", "default":
			mode = ModeClean
		default:
			return fmt.Errorf("invalid selection: %q (must be 1 or 2)", choice)
		}
	} else {
		switch mode {
		case "1", "full", "full-stack":
			mode = ModeFull
		case "2", "clean", "default":
			mode = ModeClean
		default:
			return fmt.Errorf("unknown mode: %q (supported: full, clean)", mode)
		}
	}

	configDir, err := paths.GeminiConfigDir()
	if err != nil {
		return err
	}
	backupBase, err := paths.AgyProBackups()
	if err != nil {
		return err
	}

	// 1. Prepare timestamped backup directory (unique per switch: two
	// switches within the same second must not share one snapshot dir)
	backupDir := uniqueBackupDir(filepath.Join(backupBase, paths.NewBackupTimestamp()))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("failed to create backup directory %q: %w", backupDir, err)
	}

	// 2. Move existing managed items to backup (NEVER permanently delete)
	backedUpCount := 0
	for _, item := range DefaultManagedItems {
		itemPath := filepath.Join(configDir, item)
		if fi, err := os.Stat(itemPath); err == nil {
			if _, moveErr := paths.MoveToBackup(itemPath, backupDir); moveErr != nil {
				return fmt.Errorf("failed to backup %q: %w", itemPath, moveErr)
			}
			backedUpCount++
			_ = fi
		}
	}

	// 3. Deploy new profile assets from embedded templates
	profileFS, err := profiles.GetProfileFS(mode)
	if err != nil {
		return fmt.Errorf("failed to load profile %q: %w", mode, err)
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("failed to create config dir %q: %w", configDir, err)
	}

	err = fs.WalkDir(profileFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		destPath := filepath.Join(configDir, path)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}

		data, readErr := fs.ReadFile(profileFS, path)
		if readErr != nil {
			return fmt.Errorf("failed to read embedded file %q: %w", path, readErr)
		}

		return paths.AtomicWriteFile(destPath, data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("failed to deploy profile files: %w", err)
	}

	// 4. Update and persist state
	state, err := LoadState()
	if err != nil {
		// A corrupt state file must not abort an already-applied switch:
		// fall back to a fresh state; the lines below rewrite every field
		// the switch owns.
		state = &State{}
	}
	state.CurrentMode = mode
	state.UpdatedAt = time.Now().UTC()
	if backedUpCount > 0 {
		state.LastBackup = backupDir
	}
	state.ManagedItems = append([]string(nil), DefaultManagedItems...)

	if err := SaveState(state); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	fmt.Fprintf(out, "[SUCCESS] Switched mode to: %s\n", mode)
	if backedUpCount > 0 {
		fmt.Fprintf(out, "          Previous configuration backed up to: %s\n", backupDir)
	}
	fmt.Fprintf(out, "          Config directory: %s\n", configDir)
	return nil
}

// Status prints the current profile mode state without touching the
// configuration. It is the read-only counterpart of Switch and the handler
// for `agy pro mode status`.
func Status(out io.Writer) error {
	state, err := LoadState()
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Profile Mode Status")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintf(out, "Current Mode : %s\n", state.CurrentMode)
	updated := state.UpdatedAt.Format("2006-01-02 15:04:05 MST")
	if state.UpdatedAt.IsZero() {
		updated = "unknown"
	}
	fmt.Fprintf(out, "Last Updated : %s\n", updated)
	if state.LastBackup != "" {
		fmt.Fprintf(out, "Last Backup  : %s\n", state.LastBackup)
	}
	fmt.Fprintf(out, "Manifest     : %s\n", strings.Join(state.ManagedItems, ", "))
	if configDir, err := paths.GeminiConfigDir(); err == nil {
		fmt.Fprintf(out, "Config Dir   : %s\n", configDir)
	}

	backups, err := ListBackups()
	if err != nil {
		fmt.Fprintf(out, "Backups      : unavailable (%v)\n", err)
	} else if len(backups) == 0 {
		fmt.Fprintln(out, "Backups      : none")
	} else {
		fmt.Fprintf(out, "Backups      : %d available (latest: %s)\n", len(backups), backups[len(backups)-1])
	}
	return nil
}

// Restore restores the latest backup from ~/.agy-pro/backups into ~/.gemini/config.
func Restore(out io.Writer) error {
	state, err := LoadState()
	if err != nil {
		return err
	}

	backupDir := state.LastBackup
	if backupDir == "" || !dirExists(backupDir) {
		// Discover latest backup directory by timestamp
		backupBase, err := paths.AgyProBackups()
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(backupBase)
		if err != nil || len(entries) == 0 {
			return errors.New("no previous backups found to restore")
		}

		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(backupBase, e.Name()))
			}
		}
		if len(dirs) == 0 {
			return errors.New("no previous backups found to restore")
		}
		sort.Strings(dirs)
		backupDir = dirs[len(dirs)-1]
	}

	configDir, err := paths.GeminiConfigDir()
	if err != nil {
		return err
	}

	// Move current active items into a pre-restore backup
	preRestoreDir := uniqueBackupDir(filepath.Join(filepath.Dir(backupDir), paths.NewBackupTimestamp()+"_pre_restore"))
	for _, item := range DefaultManagedItems {
		activePath := filepath.Join(configDir, item)
		if _, err := os.Stat(activePath); err == nil {
			_, _ = paths.MoveToBackup(activePath, preRestoreDir)
		}
	}

	// Restore items from backupDir
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return fmt.Errorf("failed to read backup directory %q: %w", backupDir, err)
	}

	restoredCount := 0
	for _, entry := range entries {
		src := filepath.Join(backupDir, entry.Name())
		dst := filepath.Join(configDir, entry.Name())
		if err := paths.CopyRecursive(src, dst); err != nil {
			return fmt.Errorf("failed to restore %q: %w", entry.Name(), err)
		}
		restoredCount++
	}

	state.CurrentMode = "restored"
	state.UpdatedAt = time.Now().UTC()
	state.LastBackup = preRestoreDir
	_ = SaveState(state)

	fmt.Fprintf(out, "[SUCCESS] Restored %d items from: %s\n", restoredCount, backupDir)
	fmt.Fprintf(out, "          Active config: %s\n", configDir)
	return nil
}

// ListBackups returns all available backup directories sorted chronologically.
func ListBackups() ([]string, error) {
	backupBase, err := paths.AgyProBackups()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(backupBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var results []string
	for _, e := range entries {
		if e.IsDir() {
			results = append(results, filepath.Join(backupBase, e.Name()))
		}
	}
	sort.Strings(results)
	return results, nil
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// uniqueBackupDir returns path with a numeric suffix appended when it already
// exists, so switches or restores occurring within the same second never
// share (and thereby interleave) one backup directory. The suffix keeps
// lexical order aligned with chronological order for ListBackups and for
// Restore's latest-backup discovery.
func uniqueBackupDir(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", path, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}
