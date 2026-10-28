// Package integrations owns the agy-tools integrations engine
// (P5-3, docs/261022_0002_session_agy-pro-session8/pick-record-p53.md picks 3,
// 5, 7 and 8):
//
//   - content.go maps each feature onto its destination tree below the Gemini
//     config directory and enumerates an fs.FS content source into hashed
//     files, plus the toolkit-root read the hooks feature needs.
//   - deploy.go plans, applies, retracts and inspects that content, keeping
//     the manifest below honest. It never deletes (retract moves bytes into
//     ~/.agy-pro/backups), never auto-fixes drift, and never writes from a
//     launch path.
//   - resolve.go resolves the toolkit root the deployer reads hooks.json from.
//   - store.go (this file) is the manifest store: the per-destination
//     bookkeeping the deployer writes.
//
// Two stores hold two different things, and this package owns only the second:
//
//   - INTENT (which feature toggles are on, which preset composes them) is the
//     user's decision and belongs in ~/.agy-pro/state.json as additive Core.Set
//     keys (a later unit). Nothing in this package writes state.json.
//   - MANIFEST (per-destination sha256, provenance, backup reference) is engine
//     bookkeeping: what the deployer put where, and what it has to retract or
//     repair. It lives in its own file, ~/.agy-pro/integrations.json, written
//     0600 and atomically, because it must describe a half-applied deployment
//     without ever being consulted as a user preference.
//
// The manifest is forward-compatible in the additive sense the mode.State
// struct established (agy-pro/internal/mode/mode.go, "Additive key" notes): a
// key this build does not know is preserved verbatim across load-and-save
// instead of being dropped, so an older build never eats a newer build's
// bookkeeping. Unknown keys are preserved at the top level and inside each
// entry. A file that cannot be read as a manifest is a typed error naming the
// path; it is never silently reset to an empty manifest, because "empty" and
// "unreadable" must not produce the same action in a repair.
package integrations

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agypro/internal/paths"
)

// Feature is one toggleable integration feature. The value is the word the
// engine records and the CLI verbs accept; the constant names are what the hub
// and the tests use.
type Feature string

const (
	// FeatureStatusline is the settings.json statusLine door (the statusline
	// command baked into the user's settings file).
	FeatureStatusline Feature = "statusline"
	// FeatureHook is the hooks.json lifecycle hook door (PostInvocation and
	// friends).
	FeatureHook Feature = "hook"
	// FeatureRules is the rules/agents tree deployment.
	FeatureRules Feature = "rules"
	// FeaturePlugins is the plugins tree deployment.
	FeaturePlugins Feature = "plugins"
	// FeatureSkills is the skills tree deployment.
	FeatureSkills Feature = "skills"
)

// AllFeatures returns every feature in the canonical order the hub renders
// them (statusline first: it is the one feature the user sees at every launch).
func AllFeatures() []Feature {
	return []Feature{FeatureStatusline, FeatureHook, FeatureRules, FeaturePlugins, FeatureSkills}
}

// featureAliases maps the spellings a hand-typed verb or a hand-edited file may
// use onto the canonical feature. The short forms exist because the hub shows
// one-word rows and users copy what they see.
var featureAliases = map[string]Feature{
	"statusline":  FeatureStatusline,
	"status-line": FeatureStatusline,
	"status_line": FeatureStatusline,
	"hook":        FeatureHook,
	"hooks":       FeatureHook,
	"rules":       FeatureRules,
	"rule":        FeatureRules,
	"r":           FeatureRules,
	"plugins":     FeaturePlugins,
	"plugin":      FeaturePlugins,
	"skills":      FeatureSkills,
	"skill":       FeatureSkills,
}

// ParseFeature normalizes a feature word. It is the single entry point for a
// value that comes from outside the binary (a CLI verb, a hand-edited store),
// so an unrecognized word fails loudly instead of silently toggling nothing.
func ParseFeature(raw string) (Feature, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if feature, ok := featureAliases[key]; ok {
		return feature, nil
	}
	return "", fmt.Errorf("%w: %q (want one of %s)", ErrUnknownFeature, raw, featureList())
}

// featureList renders the canonical feature names for error messages.
func featureList() string {
	names := make([]string, 0, len(AllFeatures()))
	for _, feature := range AllFeatures() {
		names = append(names, string(feature))
	}
	return strings.Join(names, ", ")
}

// Preset is the composed deployment shape a feature set collapses into. It is
// declared here so the engine and the hub share one vocabulary; the value is
// persisted as INTENT in state.json by a later unit, never in this manifest.
type Preset string

const (
	// PresetNative is the empty deployment: nothing but the agy-tools launchers
	// on PATH, no files of ours in the config dir.
	PresetNative Preset = "native"
	// PresetTools is the full agy-tools deployment (every feature on).
	PresetTools Preset = "tools"
	// PresetCustom is any feature set the user composed by hand.
	PresetCustom Preset = "custom"
)

// presetAliases maps the spellings a verb or a hand-edited store may use onto
// the canonical preset.
var presetAliases = map[string]Preset{
	"native": PresetNative,
	"tools":  PresetTools,
	"custom": PresetCustom,
}

// ParsePreset normalizes a preset word, failing loudly on an unknown one.
func ParsePreset(raw string) (Preset, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if preset, ok := presetAliases[key]; ok {
		return preset, nil
	}
	return "", fmt.Errorf("%w: %q (want native, tools, or custom)", ErrUnknownPreset, raw)
}

// Provenance records who wrote a destination last. It is what makes a
// record-only adopt possible: the engine can tell a file it applied itself from
// a file the Node-era installer left behind, and roll back only its own.
type Provenance string

const (
	// ProvenanceAdopted marks a destination that was already on disk and was
	// recorded without its bytes being touched (pick 8: record-only adopt).
	ProvenanceAdopted Provenance = "adopted"
	// ProvenanceNodeEra marks a destination written by the Node-era
	// configure-customizations.js / configure-statusline.js scripts.
	ProvenanceNodeEra Provenance = "node-era"
	// ProvenanceGoApplied marks a destination this Go engine wrote.
	ProvenanceGoApplied Provenance = "go-applied"
	// ProvenanceModeEra marks a destination the mode-switch deployer wrote
	// before the integrations engine existed.
	ProvenanceModeEra Provenance = "mode-era"
)

// IsKnownProvenance reports whether p is one of the four provenance values this
// build can write. It is a save-side check only: a manifest written by a newer
// build may carry a fifth value, and loading it must still work.
func IsKnownProvenance(p Provenance) bool {
	switch p {
	case ProvenanceAdopted, ProvenanceNodeEra, ProvenanceGoApplied, ProvenanceModeEra:
		return true
	default:
		return false
	}
}

// manifestEntryKnownKeys is the set of entry keys this build writes. Any other
// key found in an entry is forwarded verbatim on the next save.
var manifestEntryKnownKeys = []string{"path", "sha256", "provenance", "backup_ref"}

// ManifestEntry is one destination the engine knows about: what the file is,
// the checksum of the bytes it expects to find there, who wrote them, and where
// the pre-change copy was archived.
type ManifestEntry struct {
	// Path is the absolute destination path in the user's config tree. It is
	// also the manifest's map key (normalized on load and save).
	Path string `json:"path"`
	// SHA256 is the hex digest of the bytes the engine last wrote (or adopted).
	SHA256 string `json:"sha256"`
	// Provenance records who wrote the destination last.
	Provenance Provenance `json:"provenance"`
	// BackupRef names the archived pre-change copy, when there is one. It is a
	// backup identifier, not a second live copy.
	BackupRef string `json:"backup_ref,omitempty"`

	// extra holds the keys this build does not know, verbatim, so a
	// load-and-save cycle by an older build cannot destroy a newer build's
	// bookkeeping. It is unexported on purpose: callers go through Set, which
	// validates, and the JSON codec, which preserves it.
	extra map[string]json.RawMessage
}

// entryWire is the typed view of an entry. It exists because the
// forward-compatible decode has to do two passes: one into the known fields,
// one into a raw map to find the unknown ones.
type entryWire struct {
	Path       string     `json:"path"`
	SHA256     string     `json:"sha256"`
	Provenance Provenance `json:"provenance"`
	BackupRef  string     `json:"backup_ref"`
}

// UnmarshalJSON decodes the known entry fields and keeps every unknown key.
func (e *ManifestEntry) UnmarshalJSON(data []byte) error {
	var wire entryWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range manifestEntryKnownKeys {
		delete(raw, key)
	}
	*e = ManifestEntry{
		Path:       wire.Path,
		SHA256:     wire.SHA256,
		Provenance: wire.Provenance,
		BackupRef:  wire.BackupRef,
	}
	if len(raw) > 0 {
		e.extra = raw
	}
	return nil
}

// MarshalJSON emits the known entry fields plus every preserved unknown key.
// Fields are emitted only when set, so an entry that lost a value does not gain
// an empty string on the next save.
func (e ManifestEntry) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(e.extra)+4)
	for key, value := range e.extra {
		out[key] = value
	}
	if e.Path != "" {
		encoded, err := json.Marshal(e.Path)
		if err != nil {
			return nil, err
		}
		out["path"] = encoded
	}
	if e.SHA256 != "" {
		encoded, err := json.Marshal(e.SHA256)
		if err != nil {
			return nil, err
		}
		out["sha256"] = encoded
	}
	if e.Provenance != "" {
		encoded, err := json.Marshal(e.Provenance)
		if err != nil {
			return nil, err
		}
		out["provenance"] = encoded
	}
	if e.BackupRef != "" {
		encoded, err := json.Marshal(e.BackupRef)
		if err != nil {
			return nil, err
		}
		out["backup_ref"] = encoded
	}
	return json.Marshal(out)
}

// Clone returns a deep copy, so a caller that hands an entry back through Set
// cannot alias the map another entry decoded into.
func (e ManifestEntry) Clone() ManifestEntry {
	clone := e
	if e.extra != nil {
		clone.extra = make(map[string]json.RawMessage, len(e.extra))
		for key, value := range e.extra {
			clone.extra[key] = value
		}
	}
	return clone
}

// CurrentManifestSchemaVersion is the schema this build writes. A manifest
// carrying a HIGHER version is refused rather than re-saved: this build does
// not know what the newer keys mean, and a re-save would be a destructive
// downgrade of the engine's bookkeeping.
const CurrentManifestSchemaVersion = 1

// manifestKnownKeys is the set of top-level keys this build writes.
var manifestKnownKeys = []string{"schema_version", "updated_at", "entries"}

// Manifest is the whole ~/.agy-pro/integrations.json document.
type Manifest struct {
	// SchemaVersion is the document schema. Zero (an older or hand-written
	// file) is read as CurrentManifestSchemaVersion.
	SchemaVersion int `json:"-"`
	// UpdatedAt is when the document was last written. Save stamps it when it
	// is zero and preserves any value already set.
	UpdatedAt time.Time `json:"-"`
	// Entries maps a normalized absolute destination path to its record.
	Entries map[string]ManifestEntry `json:"-"`

	// extra holds unknown top-level keys, preserved verbatim across a
	// load-and-save by this build.
	extra map[string]json.RawMessage
}

// manifestWire is the typed view of the document root, for the same two-pass
// reason entryWire exists.
type manifestWire struct {
	SchemaVersion int                      `json:"schema_version"`
	UpdatedAt     time.Time                `json:"updated_at"`
	Entries       map[string]ManifestEntry `json:"entries"`
}

// UnmarshalJSON decodes the known document keys and keeps every unknown one.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	var wire manifestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range manifestKnownKeys {
		delete(raw, key)
	}
	*m = Manifest{
		SchemaVersion: wire.SchemaVersion,
		UpdatedAt:     wire.UpdatedAt,
		Entries:       wire.Entries,
	}
	if len(raw) > 0 {
		m.extra = raw
	}
	return nil
}

// MarshalJSON emits the known document keys plus every preserved unknown key.
func (m Manifest) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(m.extra)+3)
	for key, value := range m.extra {
		out[key] = value
	}
	schema := m.SchemaVersion
	if schema <= 0 {
		schema = CurrentManifestSchemaVersion
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	out["schema_version"] = encodedSchema
	encodedTime, err := json.Marshal(m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	out["updated_at"] = encodedTime
	entries := m.Entries
	if entries == nil {
		entries = map[string]ManifestEntry{}
	}
	encodedEntries, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	out["entries"] = encodedEntries
	return json.Marshal(out)
}

// NewManifest returns an empty manifest at the current schema version.
func NewManifest() *Manifest {
	return &Manifest{
		SchemaVersion: CurrentManifestSchemaVersion,
		Entries:       map[string]ManifestEntry{},
	}
}

// Len returns the number of recorded destinations.
func (m *Manifest) Len() int {
	if m == nil {
		return 0
	}
	return len(m.Entries)
}

// Entry returns the record for a destination path. The lookup normalizes the
// path the same way Save does, so a caller may pass an uncleaned path.
func (m *Manifest) Entry(destination string) (ManifestEntry, bool) {
	if m == nil || len(m.Entries) == 0 {
		return ManifestEntry{}, false
	}
	normalized, err := normalizeDestination(destination)
	if err != nil {
		return ManifestEntry{}, false
	}
	entry, ok := m.Entries[normalized]
	if !ok {
		return ManifestEntry{}, false
	}
	return entry.Clone(), true
}

// Set records one destination. It validates the entry the way Save does, so a
// bad entry is refused at the call site that produced it rather than at the
// next write.
func (m *Manifest) Set(entry ManifestEntry) error {
	if m == nil {
		return &ManifestError{
			Op:     "set",
			Err:    ErrManifestInvalid,
			Detail: "nil manifest",
		}
	}
	normalized, err := normalizeDestination(entry.Path)
	if err != nil {
		return err
	}
	entry.Path = normalized
	entry = entry.Clone()
	if err := validateEntry(entry); err != nil {
		return err
	}
	if m.Entries == nil {
		m.Entries = map[string]ManifestEntry{}
	}
	m.Entries[normalized] = entry
	return nil
}

// Delete removes one destination record and reports whether it was present.
func (m *Manifest) Delete(destination string) bool {
	if m == nil || len(m.Entries) == 0 {
		return false
	}
	normalized, err := normalizeDestination(destination)
	if err != nil {
		return false
	}
	if _, ok := m.Entries[normalized]; !ok {
		return false
	}
	delete(m.Entries, normalized)
	return true
}

// Paths returns every recorded destination, sorted, so callers that render or
// diff the manifest are deterministic.
func (m *Manifest) Paths() []string {
	if m == nil || len(m.Entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.Entries))
	for path := range m.Entries {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// EntriesUnder returns every record whose destination is dir itself or sits
// inside dir, sorted by path. It is the lookup a retract or a rollback uses to
// find the records for one deployed subtree (the skills directory, say) without
// keeping a second index.
func (m *Manifest) EntriesUnder(dir string) []ManifestEntry {
	if m == nil || len(m.Entries) == 0 {
		return nil
	}
	root, err := normalizeDestination(dir)
	if err != nil {
		return nil
	}
	out := make([]ManifestEntry, 0, len(m.Entries))
	for _, path := range m.Paths() {
		if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
			continue
		}
		out = append(out, m.Entries[path].Clone())
	}
	return out
}

// Stamp records the write time, so a caller can pin it (tests, a batched apply
// that wants one timestamp for the whole run). Save stamps a zero UpdatedAt on
// its own.
func (m *Manifest) Stamp(now time.Time) {
	if m == nil {
		return
	}
	m.UpdatedAt = now.UTC()
}

// ManifestFileName is the store's file name inside the agy-pro home.
const ManifestFileName = "integrations.json"

// manifestPerm is the mode the manifest is written with: owner-only, the same
// 0600 state.json uses. The manifest holds absolute paths of the user's config
// tree, which is not a secret but is nobody else's business.
const manifestPerm = 0o600

// atomicWriteFile is the writer seam; tests intercept it to assert the
// requested mode, because Windows does not round-trip mode bits through Stat.
var atomicWriteFile = paths.AtomicWriteFile

// ManifestPath returns the default manifest path, ~/.agy-pro/integrations.json.
// It resolves through paths.AgyProHome, so AGY_PRO_HOME relocation (which every
// test uses) moves the store with the rest of the agy-pro home.
func ManifestPath() (string, error) {
	home, err := paths.AgyProHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ManifestFileName), nil
}

// LoadManifest reads the default manifest.
func LoadManifest() (*Manifest, error) {
	manifestPath, err := ManifestPath()
	if err != nil {
		return nil, err
	}
	return LoadManifestFile(manifestPath)
}

// LoadManifestFile reads the manifest at an explicit path.
//
// A missing file is the normal first-run state and yields an empty manifest. A
// file that exists but cannot be read, parsed, or understood is a typed
// *ManifestError naming the path: the caller must be able to tell "nothing
// deployed yet" from "the bookkeeping is broken", because the two demand
// opposite repairs. Nothing is ever reset as a side effect of a read.
func LoadManifestFile(manifestPath string) (*Manifest, error) {
	clean := filepath.Clean(manifestPath)

	data, err := os.ReadFile(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return NewManifest(), nil
		}
		return nil, &ManifestError{
			Path:   clean,
			Op:     "read",
			Err:    ErrManifestCorrupt,
			Detail: err.Error(),
		}
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		// A blank (torn, truncated) file lands here too: it is the same
		// failure class, and it must not read as an empty deployment.
		return nil, &ManifestError{
			Path:   clean,
			Op:     "parse",
			Err:    ErrManifestCorrupt,
			Detail: err.Error(),
		}
	}

	if manifest.SchemaVersion > CurrentManifestSchemaVersion {
		return nil, &ManifestError{
			Path: clean,
			Op:   "load",
			Err:  ErrManifestSchemaTooNew,
			Detail: fmt.Sprintf("schema_version %d is newer than the %d this build writes",
				manifest.SchemaVersion, CurrentManifestSchemaVersion),
		}
	}
	if manifest.SchemaVersion <= 0 {
		manifest.SchemaVersion = CurrentManifestSchemaVersion
	}
	if manifest.Entries == nil {
		manifest.Entries = map[string]ManifestEntry{}
	}
	if err := manifest.normalizeDestinations(); err != nil {
		return nil, withManifestPath(err, clean)
	}
	return &manifest, nil
}

// SaveManifest writes the default manifest.
func SaveManifest(manifest *Manifest) error {
	manifestPath, err := ManifestPath()
	if err != nil {
		return err
	}
	return SaveManifestFile(manifestPath, manifest)
}

// SaveManifestFile writes the manifest at an explicit path, atomically and 0600.
//
// One writer per machine: the store has no lockfile, so two integrations
// processes that each load-modify-save are not serialized across processes and
// the later save wins the whole entry set. That is the documented posture (see
// the deploy.go header); this function enforces only what one process can
// enforce.
//
// The write is strict where the read is tolerant: every entry must carry an
// absolute destination, a sha256, and a provenance this build knows, and no two
// entries may claim the same destination (the one-owner-per-destination
// invariant the destination partition rests on). A manifest that fails those
// checks is refused instead of written, so the store never becomes the source
// of a later confusion.
func SaveManifestFile(manifestPath string, manifest *Manifest) error {
	clean := filepath.Clean(manifestPath)
	if manifest == nil {
		return &ManifestError{
			Path:   clean,
			Op:     "save",
			Err:    ErrManifestInvalid,
			Detail: "nil manifest",
		}
	}
	if err := manifest.normalizeDestinations(); err != nil {
		return withManifestPath(err, clean)
	}
	for _, path := range manifest.Paths() {
		entry := manifest.Entries[path]
		if err := validateEntry(entry); err != nil {
			return withManifestPath(err, clean)
		}
	}
	if manifest.UpdatedAt.IsZero() {
		manifest.UpdatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return &ManifestError{
			Path:   clean,
			Op:     "save",
			Err:    ErrManifestWrite,
			Detail: err.Error(),
		}
	}
	if err := atomicWriteFile(clean, append(data, '\n'), manifestPerm); err != nil {
		return &ManifestError{
			Path:   clean,
			Op:     "write",
			Err:    ErrManifestWrite,
			Detail: err.Error(),
		}
	}
	return nil
}

// normalizeDestinations rewrites the entry map so every key is the cleaned
// absolute destination, adopting entry.Path as the authority when the key is
// blank. Two keys that normalize onto one destination are a refusal: the
// manifest must never record two owners for one file.
func (m *Manifest) normalizeDestinations() error {
	if m.Entries == nil {
		m.Entries = map[string]ManifestEntry{}
		return nil
	}
	normalized := make(map[string]ManifestEntry, len(m.Entries))
	for key, entry := range m.Entries {
		candidate := strings.TrimSpace(entry.Path)
		if candidate == "" {
			candidate = strings.TrimSpace(key)
		}
		destination, err := normalizeDestination(candidate)
		if err != nil {
			return &ManifestError{
				Op:          "validate",
				Destination: candidate,
				Err:         ErrManifestInvalid,
				Detail:      err.Error(),
			}
		}
		if _, duplicate := normalized[destination]; duplicate {
			return &ManifestError{
				Op:          "validate",
				Destination: destination,
				Err:         ErrManifestDuplicateDestination,
			}
		}
		entry = entry.Clone()
		entry.Path = destination
		normalized[destination] = entry
	}
	m.Entries = normalized
	return nil
}

// normalizeDestination returns the cleaned absolute form of a destination path,
// refusing everything the engine cannot own: a blank path, a relative path, a
// UNC share, and any path with a traversal segment left in it.
func normalizeDestination(candidate string) (string, error) {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return "", errors.New("destination path is empty")
	}
	if isUNCPath(trimmed) {
		return "", fmt.Errorf("destination path %q is a UNC share", trimmed)
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("destination path %q is not absolute", trimmed)
	}
	clean := filepath.Clean(trimmed)
	if clean == string(filepath.Separator) || (len(clean) == 3 && clean[1] == ':') {
		return "", fmt.Errorf("destination path %q is a filesystem root", trimmed)
	}
	if hasTraversalSegment(trimmed) {
		return "", fmt.Errorf("destination path %q contains a %q segment", trimmed, "..")
	}
	return clean, nil
}

// hasTraversalSegment reports whether the raw path walks upwards with "..".
// It inspects the raw string because filepath.Clean has already resolved the
// segment away by the time it is checked.
func hasTraversalSegment(raw string) bool {
	for _, segment := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// validateEntry is the strict, write-side entry check.
func validateEntry(entry ManifestEntry) error {
	if entry.Path == "" {
		return &ManifestError{
			Op:     "validate",
			Err:    ErrManifestInvalid,
			Detail: "entry has no destination path",
		}
	}
	if !isSHA256Hex(entry.SHA256) {
		return &ManifestError{
			Op:          "validate",
			Destination: entry.Path,
			Err:         ErrManifestInvalid,
			Detail:      fmt.Sprintf("sha256 %q is not 64 lowercase hex characters", entry.SHA256),
		}
	}
	if !IsKnownProvenance(entry.Provenance) {
		return &ManifestError{
			Op:          "validate",
			Destination: entry.Path,
			Err:         ErrManifestInvalid,
			Detail: fmt.Sprintf("provenance %q is not one of adopted, node-era, go-applied, mode-era",
				string(entry.Provenance)),
		}
	}
	return nil
}

// isSHA256Hex reports whether value is a lowercase hex sha256 digest.
func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return false
	}
	return strings.ToLower(value) == value
}

// isUNCPath reports whether the candidate is a UNC path (\\server\share or
// //server/share). The Node-era root resolver refuses UNC paths outright and
// the Go side keeps that rule: a share path is not something this engine can
// own or back up predictably.
func isUNCPath(candidate string) bool {
	return strings.HasPrefix(candidate, `\\`) || strings.HasPrefix(candidate, "//")
}

// withManifestPath attaches the manifest file path to an entry-level failure
// that was raised before the path was known.
func withManifestPath(err error, manifestPath string) error {
	var manifestErr *ManifestError
	if errors.As(err, &manifestErr) && manifestErr.Path == "" {
		manifestErr.Path = manifestPath
		return manifestErr
	}
	return err
}

// Error sentinels. Callers branch on errors.Is rather than on message text, so
// the strings stay free to get better.
var (
	// ErrManifestCorrupt means the file exists but cannot be read as a manifest.
	// It is never a reason to start over: the caller reports it and stops.
	ErrManifestCorrupt = errors.New("integrations manifest is unreadable")
	// ErrManifestSchemaTooNew means the file was written by a newer build.
	ErrManifestSchemaTooNew = errors.New("integrations manifest schema is newer than this build")
	// ErrManifestInvalid means a value this build would write is not acceptable.
	ErrManifestInvalid = errors.New("integrations manifest value is invalid")
	// ErrManifestDuplicateDestination means two entries claim one destination.
	ErrManifestDuplicateDestination = errors.New("one destination is claimed twice")
	// ErrManifestWrite means the manifest could not be serialized or written.
	ErrManifestWrite = errors.New("integrations manifest write failed")
	// ErrUnknownFeature means a feature word is not in the closed set.
	ErrUnknownFeature = errors.New("unknown integration feature")
	// ErrUnknownPreset means a preset word is not in the closed set.
	ErrUnknownPreset = errors.New("unknown integration preset")
)

// ManifestError reports a manifest operation this package refused. Path names
// the manifest file so a user can act on it, Op says which step failed, and
// Destination names the entry at fault when the fault is one entry.
type ManifestError struct {
	// Path is the manifest file the operation targeted.
	Path string
	// Op is the step that failed: read, parse, load, validate, save, or write.
	Op string
	// Destination is the destination path at fault, when the fault is one
	// entry rather than the whole document.
	Destination string
	// Err is one of the package sentinels.
	Err error
	// Detail is the human-readable cause, when the sentinel alone is too thin.
	Detail string
}

// Error renders the failure with the file path, so the message is actionable
// without the caller adding context.
func (e *ManifestError) Error() string {
	if e == nil {
		return "integrations manifest failure"
	}
	var builder strings.Builder
	builder.WriteString("integrations manifest")
	if e.Path != "" {
		builder.WriteString(" ")
		builder.WriteString(strconv.Quote(e.Path))
	}
	if e.Op != "" {
		builder.WriteString(": ")
		builder.WriteString(e.Op)
	}
	builder.WriteString(" failed: ")
	if e.Err != nil {
		builder.WriteString(e.Err.Error())
	} else {
		builder.WriteString("unknown error")
	}
	if e.Detail != "" {
		builder.WriteString(": ")
		builder.WriteString(e.Detail)
	}
	if e.Destination != "" {
		builder.WriteString(" (destination ")
		builder.WriteString(strconv.Quote(e.Destination))
		builder.WriteString(")")
	}
	return builder.String()
}

// Unwrap exposes the sentinel to errors.Is and errors.As.
func (e *ManifestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
