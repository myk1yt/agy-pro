package fanout

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// EnvFanoutPreset names the environment variable that selects the active
// fanout preset for a run (D6 spelling). It carries a preset NAME only.
const EnvFanoutPreset = "AGY_PRO_FANOUT_PRESET"

// Preset is one saved fanout configuration (record shape E.1, plan section
// 6). Every field is omitempty: an empty field means "this preset does not
// pin the knob", so the resolution ladder falls through to the next rung.
// SearchKeyEnv stores an environment-variable NAME only; the key VALUE lives
// in the process environment and is read at call time, never persisted.
type Preset struct {
	Name           string   `json:"name"`
	Task           string   `json:"task,omitempty"`
	N              int      `json:"n,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Tools          string   `json:"tools,omitempty"`
	MaxTurns       int      `json:"max_turns,omitempty"`
	MaxNative      int      `json:"max_native,omitempty"`
	AllowHosts     []string `json:"allow_hosts,omitempty"`
	SearchEndpoint string   `json:"search_endpoint,omitempty"`
	SearchKeyEnv   string   `json:"search_key_env,omitempty"`
	OutDir         string   `json:"out_dir,omitempty"`
	Root           string   `json:"root,omitempty"`
	Models         string   `json:"models,omitempty"`
	Seats          string   `json:"seats,omitempty"`
	JSON           bool     `json:"json,omitempty"`
}

// PresetsFile is the on-disk shape of fanout-presets.json: the preset list
// plus the file-level default_preset key (the preset an env-less --preset-less
// run picks up).
type PresetsFile struct {
	Presets       []Preset `json:"presets"`
	DefaultPreset string   `json:"default_preset,omitempty"`
}

// PresetStore persists fanout presets (contract E.1: ~/.agy-pro/
// fanout-presets.json, atomic via providers.WriteFileAtomic). Consumed by the
// fanout CLI ladder (P8) and, read-only, by the settings/TUI surface (S8).
type PresetStore interface {
	LoadPresets() (*PresetsFile, error)
	SavePresets(*PresetsFile) error
}

// PresetFileStore is the default PresetStore backed by fanout-presets.json.
// Path "" resolves through DefaultPresetsPath (AGY_PRO_HOME aware).
// WarnOut optionally receives the one-line corrupt-file fallback warning;
// nil discards it.
type PresetFileStore struct {
	Path    string
	WarnOut io.Writer
}

// DefaultPresetsPath returns ~/.agy-pro/fanout-presets.json (AGY_PRO_HOME
// aware, via the same providers.Home helper the swarm store uses).
func DefaultPresetsPath() (string, error) {
	home, err := providers.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "fanout-presets.json"), nil
}

// NewPresetFileStore returns a PresetFileStore at the default presets path.
func NewPresetFileStore() (*PresetFileStore, error) {
	p, err := DefaultPresetsPath()
	if err != nil {
		return nil, err
	}
	return &PresetFileStore{Path: p}, nil
}

func (s *PresetFileStore) path() string {
	if s != nil && s.Path != "" {
		return s.Path
	}
	p, err := DefaultPresetsPath()
	if err != nil {
		return ""
	}
	return p
}

// LoadPresets reads the presets file. A missing file yields an empty store
// (create-on-demand: the file appears on the first SavePresets). A corrupt
// file falls back to an empty store with ONE warning line on WarnOut,
// never an error (swarm FileStore corrupt-file precedent,
// internal/swarm/state.go): the happy path must not dead-end on a bad
// state file.
func (s *PresetFileStore) LoadPresets() (*PresetsFile, error) {
	path := s.path()
	if path == "" {
		return &PresetsFile{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &PresetsFile{}, nil
		}
		return nil, err
	}
	var f PresetsFile
	if err := json.Unmarshal(data, &f); err != nil {
		if s != nil && s.WarnOut != nil {
			fmt.Fprintf(s.WarnOut, "warning: %s is corrupt; ignoring it and starting with no presets\n", filepath.Base(path))
		}
		return &PresetsFile{}, nil
	}
	return &f, nil
}

// SavePresets validates and writes the presets file atomically (0600,
// create-on-demand via providers.WriteFileAtomic). Validation is fail-closed
// and runs on EVERY preset before a single byte is written: a preset needs a
// name, and SearchKeyEnv must be an environment-variable NAME vetted by
// providers.ValidateAPIKeyEnvName (a value-looking string is rejected; the
// error names the preset only and never echoes the value).
func (s *PresetFileStore) SavePresets(f *PresetsFile) error {
	path := s.path()
	if path == "" {
		return errors.New("fanout presets path unavailable")
	}
	if f == nil {
		f = &PresetsFile{}
	}
	for i := range f.Presets {
		if err := validatePreset(f.Presets[i]); err != nil {
			return fmt.Errorf("preset[%d]: %w", i, err)
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return providers.WriteFileAtomic(path, data, 0o600)
}

// The sentinel refusals ValidatePresetForSave returns. The validator owns
// WHICH rules a user-driven save must pass; a presentation layer (the TUI
// preset editor) matches them with errors.Is and renders its own localized
// message, so the rule set has exactly one home and the wording has two
// registers. Every message names the rule or the fields only and never echoes
// an operand (F5 masking family).
var (
	// ErrPresetNameRequired: a preset row needs a name to be addressable by
	// --preset / AGY_PRO_FANOUT_PRESET / default_preset.
	ErrPresetNameRequired = errors.New("name is required")
	// ErrPresetSearchKeyEnv: search_key_env stores an environment-variable
	// NAME only; a key VALUE is refused (B4, providers.ValidateAPIKeyEnvName).
	ErrPresetSearchKeyEnv = errors.New("search_key_env must be an environment variable NAME, not a key value")
	// ErrPresetSeatConflict (D9): models and seats pin the same seat source, so
	// a preset carrying both could never run.
	ErrPresetSeatConflict = errors.New("models and seats cannot be combined: both pin the seat source, so this preset could never run")
	// ErrPresetUnknownTask (D13): the engine resolves an unknown task name to
	// the review prompt silently (tasks.go GetTaskPreset fallback), so a typo
	// would persist a preset that runs a different task than its name says.
	ErrPresetUnknownTask = errors.New("task must be one of the known task names (review, security, plan-audit, research, ideas, custom), or empty for the engine default")
	// ErrPresetAllowHosts (D14): an allow_hosts entry the exact allow-list match
	// can never satisfy (scheme prefix, wildcard, path) refuses the save.
	ErrPresetAllowHosts = errors.New("allow_hosts entries must be bare hostnames or IPs (no scheme, wildcard, or path)")
)

// validatePreset vets one preset at the store boundary. The search_key_env
// guard is the same providers.ValidateAPIKeyEnvName every other entry path
// shares (B4): env-var NAME only, a key VALUE is rejected, and the error
// text never echoes the operand.
func validatePreset(p Preset) error {
	if p.Name == "" {
		return ErrPresetNameRequired
	}
	if p.SearchKeyEnv != "" {
		if err := providers.ValidateAPIKeyEnvName(p.SearchKeyEnv); err != nil {
			return fmt.Errorf("%w: %s", ErrPresetSearchKeyEnv, err)
		}
	}
	return nil
}

// ValidatePresetForSave is the SINGLE save-time validation authority for a
// user-driven preset writer. Its two production callers are the TUI preset
// editor (presetEditorScreen.collect, internal/tui/screen_preset_editor.go) and
// the Presets list's Duplicate verb (presetsScreen.duplicate,
// internal/tui/screen_presets.go), which routes a copy through this gate before
// writing it because a copy is a NEW record. Each renders a refusal through its
// own localized message and never restates a rule of its own; a future writer
// (`agy pro fanout --setup` configures PROVIDERS and writes no fanout preset)
// must route its save here too.
//
// The rule set, in evaluation order:
//   - the store-boundary rules from validatePreset (ErrPresetNameRequired,
//     ErrPresetSearchKeyEnv),
//   - ErrPresetSeatConflict (D9): a preset carrying both seats and models can
//     never run (buildRunConfig hard-errors with "--seats and --models cannot
//     be combined"),
//   - ErrPresetUnknownTask (D13): an unknown task name would silently run the
//     review prompt instead of the task the preset is named for,
//   - ErrPresetAllowHosts (D14): every allow_hosts entry must be a bare
//     hostname the exact allow-list match can satisfy
//     (agenttools.ValidateAllowHosts).
//
// SavePresets deliberately does NOT apply the D9/D13/D14 rules; it runs only
// the store-boundary ones. LoadPresets never validates, the P3-1 ladder guard
// keeps an already-on-disk conflicting preset from breaking a run, and the
// store must stay able to round-trip any loadable state (the P3-1 guard tests
// seed exactly this shape through SavePresets).
func ValidatePresetForSave(p Preset) error {
	if err := validatePreset(p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Models) != "" && strings.TrimSpace(p.Seats) != "" {
		return ErrPresetSeatConflict
	}
	if strings.TrimSpace(p.Task) != "" && !IsKnownTask(p.Task) {
		return ErrPresetUnknownTask
	}
	if !agenttools.ValidateAllowHosts(p.AllowHosts) {
		return ErrPresetAllowHosts
	}
	return nil
}

// FindPreset returns the named preset, or false.
func (f *PresetsFile) FindPreset(name string) (*Preset, bool) {
	if f == nil {
		return nil, false
	}
	for i := range f.Presets {
		if f.Presets[i].Name == name {
			return &f.Presets[i], true
		}
	}
	return nil, false
}
