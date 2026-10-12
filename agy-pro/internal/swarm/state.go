package swarm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"agypro/internal/providers"
)

// SavedPreset is a user-saved numbered seat selection (contract U3).
// Numbers is numbered-selection text, e.g. "1,3,19,23" (§2.7 grammar).
type SavedPreset struct {
	Name    string `json:"name"`
	Numbers string `json:"numbers"`
}

// Store persists swarm presets (contract U3: ~/.agy-pro/swarm-state.json,
// atomic via providers.WriteFileAtomic). Consumed read-only by settings (U6).
type Store interface {
	LoadPresets() ([]SavedPreset, error)
	SavePresets([]SavedPreset) error
}

// FileStore is the default Store backed by swarm-state.json.
type FileStore struct {
	Path string // "" -> DefaultStatePath()
}

// DefaultStatePath returns ~/.agy-pro/swarm-state.json (AGY_PRO_HOME aware).
func DefaultStatePath() (string, error) {
	home, err := providers.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "swarm-state.json"), nil
}

// NewFileStore returns a FileStore at the default state path.
func NewFileStore() (*FileStore, error) {
	p, err := DefaultStatePath()
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: p}, nil
}

// swarmStateFile is the on-disk shape of swarm-state.json.
type swarmStateFile struct {
	Presets []SavedPreset `json:"presets"`
}

func (s *FileStore) path() string {
	if s != nil && s.Path != "" {
		return s.Path
	}
	p, err := DefaultStatePath()
	if err != nil {
		return ""
	}
	return p
}

// LoadPresets reads the saved presets. A missing or corrupt file falls back to
// an empty list with no error (fanout LoadState precedent,
// internal/fanout/state.go:45-62): the happy path must not dead-end on a bad
// state file.
func (s *FileStore) LoadPresets() ([]SavedPreset, error) {
	path := s.path()
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var st swarmStateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil // corrupt-file fallback
	}
	return st.Presets, nil
}

// SavePresets writes the saved presets atomically (0600).
func (s *FileStore) SavePresets(presets []SavedPreset) error {
	path := s.path()
	if path == "" {
		return errors.New("swarm state path unavailable")
	}
	data, err := json.MarshalIndent(swarmStateFile{Presets: presets}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return providers.WriteFileAtomic(path, data, 0o600)
}
