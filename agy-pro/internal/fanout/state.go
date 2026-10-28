package fanout

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"agypro/internal/providers"
)

// FanoutState persists previous model family selections to guarantee rotation diversity.
type FanoutState struct {
	LastUsedFamilies []string    `json:"last_used_families"`
	History          []RunRecord `json:"history,omitempty"`
}

// RunRecord logs a past fanout execution.
type RunRecord struct {
	Timestamp string       `json:"timestamp"`
	Task      string       `json:"task"`
	Seats     []SeatRecord `json:"seats"`
}

// SeatRecord logs the target selected for a seat in a past run.
type SeatRecord struct {
	SeatID   int    `json:"seat_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Family   string `json:"family"`
	Status   string `json:"status"`
}

// StatePath returns the path to state-fanout.json.
func StatePath() (string, error) {
	home, err := providers.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "state-fanout.json"), nil
}

// LoadState reads the fanout state file. If missing, returns empty state.
func LoadState() (*FanoutState, error) {
	path, err := StatePath()
	if err != nil {
		return &FanoutState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &FanoutState{}, nil
		}
		return nil, err
	}
	var state FanoutState
	if err := json.Unmarshal(data, &state); err != nil {
		return &FanoutState{}, nil
	}
	return &state, nil
}

// SaveState writes the fanout state file atomically.
func SaveState(state *FanoutState) error {
	path, err := StatePath()
	if err != nil {
		return err
	}
	// Keep history bounded to last 50 runs
	if len(state.History) > 50 {
		state.History = state.History[len(state.History)-50:]
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return providers.WriteFileAtomic(path, data, 0o600)
}

// RecordRun updates the state with the current run's seat allocations.
func (s *FanoutState) RecordRun(task string, seats []SeatRecord) {
	var currentFamilies []string
	famSeen := map[string]bool{}
	for _, st := range seats {
		if st.Family != "" && !famSeen[st.Family] {
			famSeen[st.Family] = true
			currentFamilies = append(currentFamilies, st.Family)
		}
	}
	s.LastUsedFamilies = currentFamilies
	s.History = append(s.History, RunRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Task:      task,
		Seats:     seats,
	})
}
