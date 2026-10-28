package fanout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// EnvFanoutPreset names the environment variable that selects the active
// fanout preset for a run (D6 spelling). It carries a preset NAME only.
const EnvFanoutPreset = "AGY_PRO_FANOUT_PRESET"

// BuiltinPresetName is the reserved identity of the built-in defaults seed
// record (design contract A1, editable built-in defaults). Identity is an exact
// byte match with the TUI list row [1] label; near-miss names remain ordinary
// user presets. ValidatePresetForSave refuses this name for NEW user records
// (ErrPresetNameReserved), but the store still round-trips an existing record.
// Consequently, a byte-exact record left by a pre-change editor IS adopted:
// the TUI presents it as row [1], and a bare run consumes it on the standing
// store-default rung. The same record also resolves through --preset and
// EnvFanoutPreset on their ad-hoc rungs. The TUI and the ladder both use this
// exact identity so the display label and the guard cannot drift apart.
const BuiltinPresetName = "(built-in defaults)"

// IsBuiltinPresetName reports whether name is exactly the reserved seed
// identity BuiltinPresetName. The comparison is exact: no trimming, no case
// folding. The ladder resolves the seed with an exact FindPreset match, so a
// near-miss spelling neither hijacks the seed arm nor is refused as a user
// preset name - only the byte-identical spelling is the seed.
func IsBuiltinPresetName(name string) bool { return name == BuiltinPresetName }

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
	Seats          string   `json:"seats,omitempty"`
	// SeatsSHA256 is the ADVISORY provenance marker of the Seats field: the hex
	// SHA-256 of the stored string. SavePresets (the single write door every
	// editor/duplicate/Set-default path goes through) respects an incoming marker
	// that already matches non-empty Seats, stamps a new or changed record whose
	// marker is missing or stale (except a Duplicate carrying PreserveSeatsMarker),
	// and otherwise restores the stored marker. A run that reads the store DEFAULT
	// preset compares it and prints one notice when
	// the on-disk value no longer matches, which is the signature of a hand edit
	// outside the editor.
	//
	// It is advisory, NOT a seal: a hand editor can also paste a matching hash.
	// A missing or stale marker prints an advisory on the standing run that
	// consumes the lineup, and the marker never blocks a run, a save or the
	// editor. Its only job is to make "this lineup was not written by the preset
	// editor" visible, because a hand-edited pin is explicitly NOT standing cost
	// approval (FL-3).
	SeatsSHA256 string `json:"seats_sha256,omitempty"`
	// PreserveSeatsMarker is in-memory only: Duplicate sets it so copying a
	// hand-authored record cannot upgrade its missing or stale advisory marker.
	PreserveSeatsMarker bool `json:"-"`
	JSON                bool `json:"json,omitempty"`
}

// PresetsFile is the on-disk shape of fanout-presets.json: the preset list
// plus the file-level default_preset key (the preset an env-less --preset-less
// run picks up).
type PresetsFile struct {
	Presets       []Preset `json:"presets"`
	DefaultPreset string   `json:"default_preset,omitempty"`
	// LegacyModels is LOAD-ONLY and never serialized: the display-masked names
	// of the still-valid records whose JSON carried the RETIRED "models" key.
	// The field is gone from Preset, so encoding/json would drop such a key
	// silently; LoadPresets fills this list (and prints the English advisory)
	// so a hand-edited or pre-removal file DISCLOSES the ignore instead of
	// changing a run's lineup without a word (FL-3 disclose-only posture). A
	// TUI surface renders its own localized copy from this list and routes the
	// English line to a discard sink (PresetFileStore.ModelsDropOut), so the
	// fact prints once. Display-only: every name is already masked through the
	// registry-slot authority.
	LegacyModels []string `json:"-"`
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
//
// NoticeOut optionally receives the per-record load notices (the FL-4 control
// drop and the F-12 duplicate-seed collapse, one line per ignored record);
// nil falls back to WarnOut. The two channels are separate because the
// corrupt-file fallback is a different class from a dropped record: the
// fallback means the whole file was unreadable, while a drop notice means the
// file parsed fine and one specific record was ignored (R3-F-05), so a
// surface counting corrupt-file episodes (the TUI's corrupt latch) must never
// see the drop class.
type PresetFileStore struct {
	Path      string
	WarnOut   io.Writer
	NoticeOut io.Writer
	// ModelsDropOut optionally receives the retired-models-field advisories
	// (one line per record whose JSON still carried the removed "models" key);
	// nil falls back to noticeOut(), which is what the CLI ladder and the store
	// tests get. It is a separate channel for ONE reason: a surface that renders
	// its own localized copy of that sentence (the TUI preset editor and list,
	// key presets.notice.models_removed) sets this to a discard sink, so the fact
	// is disclosed once in the user's language instead of twice.
	ModelsDropOut io.Writer
}

// noticeOut resolves the per-record load-notice channel: NoticeOut when set,
// WarnOut as the compatibility fallback so a caller that wires only WarnOut
// (the CLI ladder, the store tests) keeps receiving drop and duplicate
// notices where it always did.
func (s *PresetFileStore) noticeOut() io.Writer {
	if s != nil {
		if s.NoticeOut != nil {
			return s.NoticeOut
		}
		return s.WarnOut
	}
	return nil
}

// modelsDropOut resolves the retired-models-field channel: the dedicated writer
// when one is wired, otherwise the same channel the other drop notices use.
func (s *PresetFileStore) modelsDropOut() io.Writer {
	if s != nil && s.ModelsDropOut != nil {
		return s.ModelsDropOut
	}
	return s.noticeOut()
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
//
// F-12 duplicate-seed gate: a hand-edited file can carry more than one record
// named BuiltinPresetName, and the first-match ladder (FindPreset) and the
// last-match TUI overwrite path would then disagree on which record
// authorizes a preset-less run (a silent authority split on the one record
// that steers every preset-less run). The load door keeps the FIRST reserved-
// name record (the resolution every consumer already agrees on once only one
// exists) and drops the later duplicates with ONE masked warning, never an
// error: the duplicate is disclosed, not propagated.
//
// FL-4 control gate (the providers.json precedent, providers.Registry.Validate):
// a record whose echoed or artifact-written strings carry a Cc, Cf, Zl or Zp
// rune is DROPPED from the loaded set with ONE warning line, never stripped and
// never allowed through. The file is hand-editable, and those strings reach the
// run ladder, artifacts, progress log and console banner; a record that cannot
// be displayed cannot be trusted to describe a lineup. The drop is per-record
// so one planted row cannot cost the user every other preset (that is the
// corrupt-file fallback's job), and the warning names the preset.
//
// Retired-models gate: the preset "models" key is GONE (seats is the only seat
// source), so a pre-removal or hand-edited file can still carry it and
// encoding/json would ignore it in silence - a lineup that stops applying with
// no word. The load door therefore DISCLOSES and IGNORES it: one advisory line
// per surviving record that still carries the key (the value is never echoed,
// applied or re-saved), and the same record names are exposed on
// PresetsFile.LegacyModels for a surface with its own localized copy. Nothing
// blocks: a stale key can never crash or veto a run.
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
	if hasSeatControlRune(f.DefaultPreset) {
		if w := s.noticeOut(); w != nil {
			fmt.Fprint(w, "warning: fanout-presets.json default_preset contains control or invisible format characters; ignoring that key\n")
		}
		f.DefaultPreset = ""
	}
	f.Presets = s.dropControlPresets(f.Presets)
	f.Presets = s.dropDuplicateBuiltinPresets(f.Presets)
	f.LegacyModels = s.reportLegacyModels(data, f.Presets)
	return &f, nil
}

// reportLegacyModels announces the records that still carry the RETIRED
// "models" key. It reads the raw bytes a second time because the Preset struct
// no longer has the field (the whole point: the value cannot be applied
// accidentally), so the detection has to come from the JSON keys themselves.
//
// The advisory is scoped to records that SURVIVED the load gates, so a row
// already dropped for control runes or a duplicate-seed collapse cannot produce
// a second, contradictory line. Names render through the registry-slot
// authority and the stored value is never echoed. Malformed shapes yield no
// notices: this pass is disclosure only and must never turn a loadable file
// into an error.
func (s *PresetFileStore) reportLegacyModels(data []byte, kept []Preset) []string {
	if len(kept) == 0 {
		return nil
	}
	var probe struct {
		Presets []map[string]json.RawMessage `json:"presets"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil
	}
	carrying := make(map[string]bool, len(probe.Presets))
	for _, rec := range probe.Presets {
		if _, ok := rec["models"]; !ok {
			continue
		}
		var name string
		if raw, ok := rec["name"]; ok {
			_ = json.Unmarshal(raw, &name)
		}
		carrying[name] = true
	}
	if len(carrying) == 0 {
		return nil
	}
	w := s.modelsDropOut()
	var names []string
	seen := make(map[string]bool, len(kept))
	for _, p := range kept {
		if !carrying[p.Name] || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		masked := providers.MaskSlot(providers.StripSlotControls(p.Name))
		names = append(names, masked)
		if w != nil {
			fmt.Fprintf(w, "warning: preset %q: the legacy models field is removed; use seats (ignoring the stored value)\n", masked)
		}
	}
	return names
}

// dropControlPresets removes records whose echoed or artifact-written strings
// carry a Cc, Cf, Zl or Zp rune, warning once per dropped record on the
// notice channel (NoticeOut, or WarnOut when NoticeOut is unset). The warning
// names the preset and the field, never the operand value.
// presetControlField is also the write door's field set, so a saved value can
// never be one this loader would drop.
func (s *PresetFileStore) dropControlPresets(in []Preset) []Preset {
	if len(in) == 0 {
		return in
	}
	out := make([]Preset, 0, len(in))
	for _, p := range in {
		field := presetControlField(p)
		if field == "" {
			out = append(out, p)
			continue
		}
		if w := s.noticeOut(); w != nil {
			fmt.Fprintf(w, "warning: ignoring preset; %s has control/format runes; record name %q\n",
				field, providers.MaskSlot(p.Name))
		}
	}
	return out
}

func stripEchoControls(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return -1
		}
		return r
	}, v)
}

// presetControlField returns the first echoed or artifact-written field that
// carries a Cc, Cf, Zl or Zp rune. The complete set is name, task, root,
// out_dir, seats, tools, search_endpoint, search_key_env and every
// allow_hosts entry. Both store doors use this one authority.
func presetControlField(p Preset) string {
	switch {
	case hasSeatControlRune(p.Name):
		return "name"
	case hasSeatControlRune(p.Task):
		return "task"
	case hasSeatControlRune(p.Root):
		return "root"
	case hasSeatControlRune(p.OutDir):
		return "out_dir"
	case hasSeatControlRune(p.Seats):
		return "seats"
	case hasSeatControlRune(p.Tools):
		return "tools"
	case hasSeatControlRune(p.SearchEndpoint):
		return "search_endpoint"
	case hasSeatControlRune(p.SearchKeyEnv):
		return "search_key_env"
	case hasControlRuneInStrings(p.AllowHosts):
		return "allow_hosts"
	}
	return ""
}

func hasControlRuneInStrings(values []string) bool {
	for _, value := range values {
		if hasSeatControlRune(value) {
			return true
		}
	}
	return false
}

// dropDuplicateBuiltinPresets enforces the F-12 single-seed invariant at the
// load door. The first reserved-name record is kept; every later duplicate is
// dropped, with ONE warning naming the record through the registry-slot
// authority. Keeping the first (not the last) matches FindPreset's natural
// resolution, so dedupe changes nothing about which record runs; it only
// guarantees that the record the ladder runs and the record the TUI edits
// are the same single record.
func (s *PresetFileStore) dropDuplicateBuiltinPresets(in []Preset) []Preset {
	first := -1
	for i := range in {
		if IsBuiltinPresetName(in[i].Name) {
			first = i
			break
		}
	}
	if first < 0 {
		return in
	}
	dupes := 0
	out := make([]Preset, 0, len(in))
	out = append(out, in[:first+1]...)
	for i := first + 1; i < len(in); i++ {
		if IsBuiltinPresetName(in[i].Name) {
			dupes++
			continue
		}
		out = append(out, in[i])
	}
	if dupes > 0 {
		if w := s.noticeOut(); w != nil {
			fmt.Fprint(w, duplicateBuiltinPresetWarning(BuiltinPresetName, dupes))
		}
	}
	return out
}

// duplicateBuiltinPresetWarning renders the single F-12 disclosure for a file
// that held more than one reserved-name record. The record name is
// file-sourced, so it rides MaskSlot: the benign built-in label renders
// byte-identical and a credential planted in the same slot masks. The message
// names the rule and the record only.
func duplicateBuiltinPresetWarning(name string, dupes int) string {
	return fmt.Sprintf("warning: fanout-presets.json holds more than one record named %q; keeping the first and ignoring %d later duplicate record(s)\n",
		providers.MaskSlot(providers.StripSlotControls(name)), dupes)
}

// SeatsHash returns the hex SHA-256 of a seats value as the advisory provenance
// marker stores it (Preset.SeatsSHA256). Empty stays empty: a preset that pins
// no lineup has nothing to mark.
func SeatsHash(seats string) string {
	v := strings.TrimSpace(seats)
	if v == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// SavePresets validates and writes the presets file atomically (0600,
// create-on-demand via providers.WriteFileAtomic). Validation is fail-closed
// and runs on EVERY preset before a single byte is written: a preset needs a
// name, and SearchKeyEnv must be an environment-variable NAME vetted by
// providers.ValidateAPIKeyEnvName (a value-looking string is rejected; the
// error names the preset only and never echoes the value).
//
// An incoming Preset.SeatsSHA256 that already matches non-empty Seats is
// respected: a deliberate writer that passed its validation door has already
// proved the value. Honouring that hash adds no power beyond the accepted
// advisory doctrine, under which a hand editor can paste the same matching
// hash. Otherwise a new or changed record gets a fresh stamp, while an
// unchanged record keeps its stored marker byte-for-byte, including missing or
// stale hand-edit evidence. Duplicate sets PreserveSeatsMarker so a copy keeps
// exactly that evidence instead of acquiring an editor-reviewed appearance.
// The marker never gates a save or run. The default_preset key follows the same
// Cc/Cf/Zl/Zp gate at both load and save, without echoing its value.
//
// F-12: the file can hold at most ONE record named BuiltinPresetName (the
// reserved seed identity). A file with two reserved-name records splits
// authority between the first-match ladder and the last-match TUI overwrite
// path, so the write door refuses to persist that shape at all
// (ErrPresetDuplicateBuiltin); LoadPresets separately discloses and collapses
// a hand-authored duplicate so it cannot reach a run silently.
func (s *PresetFileStore) SavePresets(f *PresetsFile) error {
	path := s.path()
	if path == "" {
		return errors.New("fanout presets path unavailable")
	}
	if f == nil {
		f = &PresetsFile{}
	}
	if hasSeatControlRune(f.DefaultPreset) {
		return fmt.Errorf("default_preset: %w", ErrPresetFieldsControl)
	}
	builtin := 0
	for i := range f.Presets {
		if IsBuiltinPresetName(f.Presets[i].Name) {
			builtin++
		}
	}
	if builtin > 1 {
		return ErrPresetDuplicateBuiltin
	}
	previous := storedPresetContent(path)
	for i := range f.Presets {
		if err := validatePreset(f.Presets[i]); err != nil {
			return fmt.Errorf("preset[%d]: %w", i, err)
		}
		preserve := f.Presets[i].PreserveSeatsMarker
		f.Presets[i].PreserveSeatsMarker = false
		if preserve {
			continue
		}
		if hash := SeatsHash(f.Presets[i].Seats); hash != "" && f.Presets[i].SeatsSHA256 == hash {
			continue
		}
		old, exists := previous[f.Presets[i].Name]
		if !exists || !samePresetContent(old, f.Presets[i]) {
			f.Presets[i].SeatsSHA256 = SeatsHash(f.Presets[i].Seats)
		} else {
			f.Presets[i].SeatsSHA256 = old.SeatsSHA256
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return providers.WriteFileAtomic(path, data, 0o600)
}

func storedPresetContent(path string) map[string]Preset {
	stored := make(map[string]Preset)
	data, err := os.ReadFile(path)
	if err != nil {
		return stored
	}
	var f PresetsFile
	if json.Unmarshal(data, &f) != nil {
		return stored
	}
	for _, p := range f.Presets {
		if _, seen := stored[p.Name]; !seen {
			stored[p.Name] = p
		}
	}
	return stored
}

func samePresetContent(a, b Preset) bool {
	a.SeatsSHA256 = ""
	b.SeatsSHA256 = ""
	a.PreserveSeatsMarker = false
	b.PreserveSeatsMarker = false
	return reflect.DeepEqual(a, b)
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
	// ErrPresetNameControl refuses the same Cc/Cf/Zl/Zp class the load gate
	// drops, so a saved name cannot disappear on the next load. The message names
	// the rule only and never echoes the operand.
	ErrPresetNameControl = errors.New("name must not contain control or invisible format characters")
	// ErrPresetPathControl applies the same writer/load symmetry to root and
	// out_dir without echoing either path operand.
	ErrPresetPathControl = errors.New("root and out_dir must not contain control or invisible format characters")
	// ErrPresetFieldsControl covers the remaining string fields in the shared
	// writer/load set. The wrapped message names the field, never its value.
	ErrPresetFieldsControl = errors.New("preset field must not contain control or invisible format characters")
	// ErrPresetNameReserved (A1): BuiltinPresetName is the seed record's
	// reserved identity, so a user-driven writer can never persist a record
	// under it - a typed or renamed record with that name would hijack TUI
	// row [1] and the ladder's seed arm. The message names the rule only and
	// never echoes the operand. The store door (SavePresets/validatePreset)
	// deliberately does NOT carry this rule: the store must stay able to
	// round-trip the seed record it legitimately holds, same reasoning as
	// D13/D14 staying out of validatePreset.
	ErrPresetNameReserved = errors.New("this preset name is reserved for the built-in defaults seed record")
	// ErrPresetSearchKeyEnv: search_key_env stores an environment-variable
	// NAME only; a key VALUE is refused (B4, providers.ValidateAPIKeyEnvName).
	ErrPresetSearchKeyEnv = errors.New("search_key_env must be an environment variable NAME, not a key value")
	// ErrPresetPathTraversal: root and out_dir may be absolute, but a complete
	// ".." component would escape the preset's stated location. The refusal
	// names the rule only and never echoes the path operand.
	ErrPresetPathTraversal = errors.New("root and out_dir must not contain .. path components")
	// ErrPresetUnknownTask (D13): the engine resolves an unknown task name to
	// the review prompt silently (tasks.go GetTaskPreset fallback), so a typo
	// would persist a preset that runs a different task than its name says.
	ErrPresetUnknownTask = errors.New("task must be one of the known task names (review, security, plan-audit, research, ideas, custom), or empty for the engine default")
	// ErrPresetAllowHosts (D14): an allow_hosts entry the exact allow-list match
	// can never satisfy (scheme prefix, wildcard, path) refuses the save.
	ErrPresetAllowHosts = errors.New("allow_hosts entries must be bare hostnames or IPs (no scheme, wildcard, or path)")
	// ErrPresetSeats: a seats value the run-time grammar cannot parse
	// (ParseSeatSpecs, seatspec.go) refuses the save. The wrapped parser error
	// names the offending seat NUMBER and never restates the rule; the empty
	// value stays valid because it means "the engine auto-selects the lineup",
	// and that fallback is deliberately untouched.
	ErrPresetSeats = errors.New("seats entries must be a comma list of native:<model id> or provider/model tokens")
	// ErrPresetDuplicateBuiltin (F-12): the store can hold at most ONE record
	// named BuiltinPresetName. Two reserved-name records would let the
	// first-match ladder and the last-match TUI overwrite path disagree on
	// which record authorizes a preset-less run, so the single write door
	// refuses to persist that shape. The message names the rule only and
	// never echoes the record name.
	ErrPresetDuplicateBuiltin = errors.New("fanout presets can hold at most one built-in defaults seed record")
)

// validatePreset vets one preset at the store boundary. The search_key_env
// guard is the same providers.ValidateAPIKeyEnvName every other entry path
// shares (B4): env-var NAME only, a key VALUE is rejected, and the error
// text never echoes the operand.
func validatePreset(p Preset) error {
	if p.Name == "" {
		return ErrPresetNameRequired
	}
	switch field := presetControlField(p); field {
	case "":
	case "name":
		return ErrPresetNameControl
	case "root", "out_dir":
		return ErrPresetPathControl
	default:
		return fmt.Errorf("%s: %w", field, ErrPresetFieldsControl)
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
//     ErrPresetNameControl, ErrPresetPathControl, ErrPresetFieldsControl,
//     ErrPresetSearchKeyEnv),
//   - ErrPresetNameReserved (A1): the reserved seed name BuiltinPresetName is
//     refused here unconditionally. Every record that reaches this function is
//     a NON-seed record by construction: the seed path (TUI row [1]) saves
//     through ValidateSeedPresetForSave, the separate door below. Splitting
//     the exemption into its own entry point - instead of a second parameter
//     or a package-level flag - is what keeps ValidatePresetForSave a
//     single-argument function for the existing callers and makes the seed
//     exemption impossible to reach by accident: a writer must name the seed
//     door explicitly,
//   - ErrPresetUnknownTask (D13): an unknown task name would silently run the
//     review prompt instead of the task the preset is named for,
//   - ErrPresetAllowHosts (D14): every allow_hosts entry must be a bare
//     hostname the exact allow-list match can satisfy
//     (agenttools.ValidateAllowHosts),
//   - ErrPresetPathTraversal: root and out_dir refuse complete ".." components
//     on either slash convention; absolute paths and look-alike names stay legal,
//   - ErrPresetSeats: a non-empty seats value must parse with the run-time
//     grammar (ParseSeatSpecs). A row the engine would reject at run start is
//     refused at save time instead, so a typo cannot persist as a preset that
//     fails later; the empty value keeps meaning "auto-select the lineup" and
//     passes untouched.
//
// SavePresets deliberately does NOT apply the reserved-name/D13/D14/path/seats
// rules; it runs only the store-boundary ones. LoadPresets never validates,
// the P3-1 ladder guard keeps an already-on-disk conflicting preset from
// breaking a run, and the store must stay able to round-trip any loadable
// state (the P3-1 guard tests seed exactly this shape through SavePresets).
func ValidatePresetForSave(p Preset) error {
	if err := validatePreset(p); err != nil {
		return err
	}
	if IsBuiltinPresetName(p.Name) {
		return ErrPresetNameReserved
	}
	return validatePresetRunRules(p)
}

// ValidateSeedPresetForSave is the seed path's half of the A1 rule and the
// ONLY door that may validate a record carrying the reserved name
// BuiltinPresetName. The TUI row-[1] editor forces that exact name before it
// calls this door. Any other name, including a near-miss, is refused with
// ErrPresetNameRequired so an ordinary save routed here by mistake cannot
// bypass ValidatePresetForSave's reserved-name refusal. The full user-writer
// rule set - ErrPresetUnknownTask (D13), ErrPresetAllowHosts (D14), parent-path
// traversal, and the seats grammar - applies to the seed exactly as to a user
// preset, because the seed feeds the same ladder and the same engine.
func ValidateSeedPresetForSave(p Preset) error {
	if err := validatePreset(p); err != nil {
		return err
	}
	if !IsBuiltinPresetName(p.Name) {
		return ErrPresetNameRequired
	}
	return validatePresetRunRules(p)
}

// validatePresetRunRules is the rule set both save doors share: D13 known
// task, D14 allow_hosts shape, parent-path traversal, and the run-time seats
// grammar. Keeping it in one unexported function is what guarantees the two
// doors cannot drift: the seed record is held to exactly the user rules.
func validatePresetRunRules(p Preset) error {
	if strings.TrimSpace(p.Task) != "" && !IsKnownTask(p.Task) {
		return ErrPresetUnknownTask
	}
	if !agenttools.ValidateAllowHosts(p.AllowHosts) {
		return ErrPresetAllowHosts
	}
	if hasParentPathComponent(p.Root) || hasParentPathComponent(p.OutDir) {
		return ErrPresetPathTraversal
	}
	if strings.TrimSpace(p.Seats) != "" {
		if _, err := ParseSeatSpecs(p.Seats); err != nil {
			return fmt.Errorf("%w: %v", ErrPresetSeats, err)
		}
	}
	return nil
}

// hasParentPathComponent recognizes traversal on both slash conventions,
// independent of the host OS. Absolute paths remain legal; only a complete
// ".." component is refused, so names such as "..." and "..cache" still pass.
func hasParentPathComponent(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

// The three rungs that can select a preset, as they appear in every echo and in
// summary.json's seat_lineup block. Only RungDefaultPreset carries standing cost
// approval (FL-3): a named preset (--preset / AGY_PRO_FANOUT_PRESET) is an
// ad-hoc choice for one run, so its metered seats keep the per-run echo and the
// caller's go-ahead.
const (
	RungDefaultPreset = "default_preset"
	RungPresetFlag    = "--preset"
	RungPresetEnv     = "env"
)

// SeatLineup is the seat lineup the fanout preset ladder resolves for a run
// that names no seats of its own: the ACTIVE preset's Seats field, parsed with
// the shipped run-time grammar (ParseSeatSpecs). It is the store-side half of
// the pinned-lineup wiring: a caller subsystem (the swarm door) can ask
// "would a bare `agy pro fanout` run take its lineup from a preset?" through
// this API instead of re-implementing the preset selection or parsing
// fanout-presets.json itself.
//
// An empty lineup (Pinned() false) is the unchanged case: the run auto-selects
// its seats exactly as it did before presets carried a lineup.
type SeatLineup struct {
	// PresetName is the preset the ladder adopts. File-sourced, so every echo
	// renders it through providers.MaskSlot.
	PresetName string
	// Rung names how the preset was selected (RungDefaultPreset, RungPresetFlag,
	// RungPresetEnv; "" when no preset resolved).
	Rung string
	// Raw is the preset's seats value verbatim ("" = the preset pins no
	// lineup, so the engine auto-selects).
	Raw string
	// Specs is Raw parsed into seat specs; empty when the preset pins none.
	Specs []SeatSpec
}

// Pinned reports whether this lineup names at least one seat.
func (l SeatLineup) Pinned() bool { return len(l.Specs) > 0 }

// Standing reports whether this lineup carries the user's standing cost
// approval: only the store default_preset rung does (FL-3). A named preset keeps
// resolving the lineup, but its metered seats are ad-hoc for that run.
func (l SeatLineup) Standing() bool { return l.Rung == RungDefaultPreset }

// MeteredSeats counts the provider seats in the lineup (the ones that spend
// money); native seats draw on the Antigravity quota.
func (l SeatLineup) MeteredSeats() int {
	n := 0
	for _, s := range l.Specs {
		if s.Kind != SeatKindNative {
			n++
		}
	}
	return n
}

// ResolvePresetSeatLineup resolves the seat lineup a fanout run adopts when it
// passes neither --seats nor --preset: the active preset's Seats field, with
// the same name selection the ladder uses (EnvFanoutPreset, else the store's
// default_preset key, else the exact BuiltinPresetName seed record on the
// standing store-default rung; --preset is by definition absent). A set but
// dangling default_preset key still suppresses the seed arm. The store read
// goes through the PresetStore API, so AGY_PRO_HOME isolation and the
// corrupt-file fallback stay the store's business.
//
// A preset that pins no seats yields an empty (not pinned) lineup and no error:
// that is the auto-select case. A runner error (the env var naming a preset
// that does not exist, an unavailable presets home) or an unparsable seats
// value comes back as an error; the caller decides whether to surface it. Store
// warnings still go to process stderr: resolving the lineup must not silently
// discard a hand-authored record that the load gate dropped.
func ResolvePresetSeatLineup() (SeatLineup, error) {
	p, _, err := loadActivePreset(fanoutFlags{}, os.Stderr)
	if err != nil {
		return SeatLineup{}, err
	}
	if p == nil {
		return SeatLineup{}, nil
	}
	l := SeatLineup{
		PresetName: p.Name,
		Rung:       selectPresetRung(fanoutFlags{}),
		Raw:        strings.TrimSpace(p.Seats),
	}
	if l.Raw == "" {
		return l, nil
	}
	specs, perr := ParseSeatSpecs(l.Raw)
	if perr != nil {
		// R3-item-3 (F-01): this arm used to claim "same masking the ladder's
		// parse site applies" while only seeding MaskSentence with the raw value,
		// which is identity on the underscore-head credential class. The parser now
		// masks its own quoted token through the registry-slot authority, so the
		// text is token-safe before it gets here; the preset NAME is the remaining
		// file-sourced slot and rides MaskSlot. No seed pass is needed - masking an
		// already-masked sentence could only be a no-op.
		return SeatLineup{}, fmt.Errorf("preset %q seats: %v", providers.MaskSlot(providers.StripSlotControls(l.PresetName)), perr)
	}
	l.Specs = specs
	return l, nil
}

// FindPreset returns the named preset, or false. After LoadPresets the store
// carries at most ONE record named BuiltinPresetName (F-12 dedupe), so the
// first match is the only match and every consumer - ladder, resolver, and
// the TUI - sees the same record.
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
