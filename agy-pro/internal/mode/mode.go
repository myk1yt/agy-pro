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

var DefaultManagedItems = []string{"rules", "skills", "plugins", "config.json"}

// State represents the serialized agy-pro configuration state in ~/.agy-pro/state.json.
type State struct {
	CurrentMode  string    `json:"current_mode"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastBackup   string    `json:"last_backup,omitempty"`
	ManagedItems []string  `json:"managed_items"`
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
				ManagedItems: DefaultManagedItems,
			}, nil
		}
		return nil, fmt.Errorf("failed to read state file %q: %w", stateFile, err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state file %q: %w", stateFile, err)
	}
	if len(state.ManagedItems) == 0 {
		state.ManagedItems = DefaultManagedItems
	}
	return &state, nil
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

	// 1. Prepare timestamped backup directory
	timestamp := paths.NewBackupTimestamp()
	backupDir := filepath.Join(backupBase, timestamp)
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
	state, _ := LoadState()
	state.CurrentMode = mode
	state.UpdatedAt = time.Now().UTC()
	if backedUpCount > 0 {
		state.LastBackup = backupDir
	}
	state.ManagedItems = DefaultManagedItems

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
	preRestoreDir := filepath.Join(filepath.Dir(backupDir), paths.NewBackupTimestamp()+"_pre_restore")
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
