package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	// WarnOut optionally receives the one-line corrupt-file fallback
	// warning, matching the fanout preset store (internal/fanout/
	// preset_store.go): a corrupt swarm-state.json used to drop every saved
	// preset silently. NewFileStore defaults it to os.Stderr; embedders with
	// a message line (the TUI app surface) can redirect it there.
	WarnOut io.Writer
}

// DefaultStatePath returns ~/.agy-pro/swarm-state.json (AGY_PRO_HOME aware).
func DefaultStatePath() (string, error) {
	home, err := providers.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "swarm-state.json"), nil
}

// NewFileStore returns a FileStore at the default state path. WarnOut
// defaults to os.Stderr so the corrupt-file warning reaches the console on
// every construction site (settings Core default, swarm builder, CLI)
// without per-site wiring.
func NewFileStore() (*FileStore, error) {
	p, err := DefaultStatePath()
	if err != nil {
		return nil, err
	}
	return &FileStore{Path: p, WarnOut: os.Stderr}, nil
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

// LoadPresets reads the saved presets. A missing file falls back to an
// empty list with no error; a corrupt file falls back to an empty list with
// ONE warning line on WarnOut (the fanout preset-store precedent,
// internal/fanout/preset_store.go:102-121), never an error: the happy path
// must not dead-end on a bad state file, but a corrupt file must not drop
// every saved preset silently either.
//
// r7 F-01: a record whose Numbers does not parse as §2.7 selection text is
// DROPPED with one warning (the fanout store's dropControlPresets pattern). The
// numbers are echoed by several display surfaces and consumed by the selection
// expander, and a hand-edited swarm-state.json can hold any string there. A
// record with EMPTY numbers is kept: it is the legacy shape the selection path
// already refuses by name ("the saved preset has no seat numbers"), and dropping
// it here would hide the row the user has to fix.
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
		// s != nil is load-bearing here, not defensive noise (code review
		// C7). path() is nil-safe by construction (it falls back to
		// DefaultStatePath), so a typed-nil *FileStore held in a swarm.Store
		// interface reaches this line through the method call and would panic
		// on s.WarnOut without the guard. TestNilFileStoreLoadsWithoutPanic
		// pins the reachability.
		if s != nil && s.WarnOut != nil {
			fmt.Fprintf(s.WarnOut, "warning: %s is corrupt; ignoring it and starting with no saved presets\n", filepath.Base(path))
		}
		return nil, nil // corrupt-file fallback
	}
	return s.dropInvalidNumbers(path, st.Presets), nil
}

// ErrSavedPresetNumbersEmpty is the sentinel the numbers validator returns for an
// empty value: the SAVE door refuses it, while the LOAD door keeps the record
// (see LoadPresets).
var ErrSavedPresetNumbersEmpty = errors.New("saved preset numbers are empty")

// ValidateSavedPresetNumbers vets a saved preset's Numbers against the §2.7
// selection grammar the selection expander itself uses (splitSelTokens +
// isSelectionSyntaxToken + tokenNumbers, NOT a restated regex): a comma list of
// seat numbers, ranges and p1/p2/p3 preset ids, each within the parser's bounds.
//
// It is the one grammar authority for the two doors this field has - the save
// prompt and the store load - so a stored value that the expander would reject
// can no longer be written, and a hand-edited one cannot survive a load. The
// error names the token POSITION and the rule and never echoes the token.
func ValidateSavedPresetNumbers(numbers string) error {
	text := strings.TrimSpace(numbers)
	if text == "" {
		return ErrSavedPresetNumbersEmpty
	}
	tokens := splitSelTokens(text)
	if len(tokens) == 0 {
		return ErrSavedPresetNumbersEmpty
	}
	for i, tok := range tokens {
		if isPresetIDToken(tok) {
			continue
		}
		if !isSelectionSyntaxToken(tok) {
			return fmt.Errorf("token %d is not a seat number, a range or a p1/p2/p3 preset id", i+1)
		}
		nums, err := tokenNumbers(tok)
		if err != nil {
			// The parser's own text quotes the token, so it is not reused here;
			// the position plus the rule is what the operator needs.
			return fmt.Errorf("token %d is not a valid seat number or range (numbers must be positive and ranges at most %d wide)", i+1, maxSelectionRange)
		}
		// tokenNumbers bounds only RANGE endpoints (its own A8/C4 rules), so a bare
		// "0" slips through it; seat numbers are 1-based, which is the grammar's
		// own reading of a selection token.
		for _, n := range nums {
			if n < 1 || n > maxSeatNumber {
				return fmt.Errorf("token %d is not a seat number (seat numbers start at 1)", i+1)
			}
		}
	}
	return nil
}

// dropInvalidNumbers removes the records whose non-empty Numbers is not §2.7
// selection text, warning once per dropped record. The warning names the preset
// (masked) and the rule only, never the stored value.
func (s *FileStore) dropInvalidNumbers(path string, in []SavedPreset) []SavedPreset {
	if len(in) == 0 {
		return in
	}
	out := make([]SavedPreset, 0, len(in))
	for _, p := range in {
		err := ValidateSavedPresetNumbers(p.Numbers)
		if err == nil || errors.Is(err, ErrSavedPresetNumbersEmpty) {
			out = append(out, p)
			continue
		}
		if s != nil && s.WarnOut != nil {
			fmt.Fprintf(s.WarnOut, "warning: %s: saved preset %q has numbers that are not seat numbers (%s); ignoring that preset\n",
				filepath.Base(path), providers.MaskSlot(p.Name), err)
		}
	}
	return out
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
