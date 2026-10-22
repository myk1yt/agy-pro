package agysettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Managed settings keys. Every key this package can write is declared here;
// ManagedKeys is the exported ledger and the write paths refuse to emit
// anything outside it.
const (
	// KeyModelProvider selects the agy client path. "gemini" is mandatory for
	// custom models: env vars alone do not enable them
	// (custommodels-probe-report.md §4, variant A2).
	KeyModelProvider = "modelProvider"

	// KeyCustomModels is the managed customModels map. The map KEY is the model
	// URL and the --model selector; only the full URL form resolves, a bare
	// modelName is rejected by agy.
	KeyCustomModels = "customModels"

	// KeyStatusLine is the managed statusLine hook object. Node's
	// configure-statusline.js has written this object since wave 1
	// (scripts/lib/configure-statusline.js:121-126); the ledger owns its shape so
	// a Go writer can emit the same bytes. Declaring the key here does NOT open
	// the protected global path: that refusal is a property of the path, not of
	// the key (OpenWithOptions, refuseGlobalWrite), and it stays as it is. The
	// deliberate global door belongs to the caller that passes the override.
	KeyStatusLine = "statusLine"
)

// ErrNotManaged is returned when a caller asks for a settings key that is not
// in the managed ledger. This package never touches unmanaged keys.
var ErrNotManaged = errors.New("agysettings: key is not in the managed ledger")

// keyKind is the value shape a ledger key accepts.
type keyKind int

const (
	kindString keyKind = iota
	kindCustomModels
	kindStatusLine
)

// ledger is the single declaration of the keys this package can write. A key
// absent here cannot be read, written, or deleted, and a document slot that is
// not declared here makes every write fail closed (assertLedgerComplete).
var ledger = map[string]keyKind{
	KeyModelProvider: kindString,
	KeyCustomModels:  kindCustomModels,
	KeyStatusLine:    kindStatusLine,
}

// ManagedKeys returns the managed-key ledger: every settings.json key this
// package can write. The returned slice is a fresh copy.
func ManagedKeys() []string {
	out := make([]string, 0, len(ledger))
	for k := range ledger {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SettingsDoc is the managed view of a settings.json document. Its json tags
// are the complete set of top-level keys this package can emit; ManagedKeys is
// kept in step with them by reflection (TestLedgerCoversSettingsDoc) and by
// the runtime guard assertLedgerComplete. Adding a field here without a ledger
// entry makes every write fail closed until the entry is added.
type SettingsDoc struct {
	ModelProvider string                       `json:"modelProvider,omitempty"`
	CustomModels  map[string]CustomModelConfig `json:"customModels,omitempty"`
	StatusLine    *StatusLineConfig            `json:"statusLine,omitempty"`
}

// CustomModelConfig mirrors agy's CustomModelConfig struct. The json tags are
// the verbatim reflect dump from agy.exe v1.3.5
// (binary-surface-report.md §1.2): modelName, displayName and context_window
// are always emitted, modelFeatures and plan_tier are optional. There is no
// apiKey or baseUrl field on this build, so no credential can be written here.
type CustomModelConfig struct {
	ModelName     string         `json:"modelName"`
	DisplayName   string         `json:"displayName"`
	ContextWindow int            `json:"context_window"`
	PlanTier      string         `json:"plan_tier,omitempty"`
	ModelFeatures *ModelFeatures `json:"modelFeatures,omitempty"`
}

// ModelFeatures is the modelFeatures media-flag object (images/video/pdf/audio).
type ModelFeatures struct {
	Images bool `json:"images,omitempty"`
	Video  bool `json:"video,omitempty"`
	PDF    bool `json:"pdf,omitempty"`
	Audio  bool `json:"audio,omitempty"`
}

// customModelConfigLedger declares the nested entry keys, kept in step with
// CustomModelConfig by reflection in the same way ManagedKeys is kept in step
// with SettingsDoc.
var customModelConfigLedger = []string{
	"modelName",
	"displayName",
	"context_window",
	"plan_tier",
	"modelFeatures",
}

// StatusLineConfig mirrors the statusLine object Node's configure-statusline.js
// writes (scripts/lib/configure-statusline.js:121-126): type, command, enabled
// and stack_with_default, declared in that field order so a Node-shaped object
// round trips through this package without gaining or reordering a field.
//
// Enabled is a plain bool because the Node writer always emits it, and the
// decoder refuses an object that omits it rather than defaulting: a missing
// field must never turn into an explicit enabled:false on the next write.
// StackWithDefault stays a pointer because it is the one optional slot, so its
// absence has to survive a write.
type StatusLineConfig struct {
	Type             string `json:"type"`
	Command          string `json:"command"`
	Enabled          bool   `json:"enabled"`
	StackWithDefault *bool  `json:"stack_with_default,omitempty"`
}

// statusLineTypeCommand is the only statusLine type the Node writer emits, and
// therefore the only one this package writes.
const statusLineTypeCommand = "command"

// NewStatusLine returns the exact object Node's configure-statusline.js writes
// for command: type "command", the command as given, and both enabled and
// stack_with_default true.
func NewStatusLine(command string) *StatusLineConfig {
	return &StatusLineConfig{
		Type:             statusLineTypeCommand,
		Command:          command,
		Enabled:          true,
		StackWithDefault: boolPtr(true),
	}
}

// boolPtr returns a pointer to b, for the optional stack_with_default slot.
func boolPtr(b bool) *bool { return &b }

// clone copies the object so the caller and the document never share a mutable
// value.
func (s *StatusLineConfig) clone() *StatusLineConfig {
	if s == nil {
		return nil
	}
	out := *s
	if s.StackWithDefault != nil {
		out.StackWithDefault = boolPtr(*s.StackWithDefault)
	}
	return &out
}

// validate checks the two slots agy cannot resolve without. The command is never
// echoed in the error: it is usually an absolute path, and this package does not
// put one in a diagnostic.
func (s *StatusLineConfig) validate() error {
	if strings.TrimSpace(s.Type) == "" {
		return fmt.Errorf("type is required (the Node writer always emits %q)", statusLineTypeCommand)
	}
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("command is required")
	}
	return nil
}

// statusLineConfigLedger declares the nested object keys, kept in step with
// StatusLineConfig by reflection in the same way customModelConfigLedger is kept
// in step with CustomModelConfig. A field added to the struct without an entry
// here makes every write fail closed (assertLedgerComplete).
var statusLineConfigLedger = []string{
	"type",
	"command",
	"enabled",
	"stack_with_default",
}

// parseStatusLine decodes the managed statusLine object strictly.
//
// A non-object is refused, an undeclared field inside the object is refused
// (json's DisallowUnknownFields), and an object missing a required slot is
// refused. Nothing is dropped and nothing is defaulted: a foreign field inside
// the managed object would either be lost on the next write or change agy's
// behavior, so the document is left alone and the caller decides.
func parseStatusLine(raw json.RawMessage) (*StatusLineConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("must be a JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var out StatusLineConfig
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}

	// enabled is required but a plain bool cannot tell absent from false, so the
	// presence of the slot is checked separately.
	var presence struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(trimmed, &presence); err != nil {
		return nil, err
	}
	if presence.Enabled == nil {
		return nil, fmt.Errorf("enabled is required (the Node writer always emits it)")
	}
	if err := out.validate(); err != nil {
		return nil, err
	}
	return &out, nil
}

// isManagedKey reports whether key is in the ledger.
func isManagedKey(key string) bool {
	_, ok := ledger[key]
	return ok
}

// jsonFieldNames returns the json tag names of a struct's fields in declaration
// order, ignoring fields with no tag or with "-".
func jsonFieldNames(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// settingsDocKeys returns the top-level keys SettingsDoc can emit.
func settingsDocKeys() []string { return jsonFieldNames(reflect.TypeOf(SettingsDoc{})) }

// customModelConfigKeys returns the nested keys a CustomModelConfig can emit.
func customModelConfigKeys() []string {
	return jsonFieldNames(reflect.TypeOf(CustomModelConfig{}))
}

// statusLineConfigKeys returns the nested keys a StatusLineConfig can emit.
func statusLineConfigKeys() []string {
	return jsonFieldNames(reflect.TypeOf(StatusLineConfig{}))
}

// sameKeySet reports whether two key lists hold the same names, order ignored.
func sameKeySet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	a := append([]string(nil), got...)
	b := append([]string(nil), want...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assertLedgerComplete fails closed when a struct field can emit a key that is
// not declared in a ledger. It runs on every Write and inside
// BuildP2Settings, so an undeclared key can never reach disk: the write is
// refused with this error instead.
func assertLedgerComplete() error {
	if err := assertLedgerMatches(reflect.TypeOf(SettingsDoc{}), ManagedKeys(), "settings"); err != nil {
		return err
	}
	if err := assertLedgerMatches(reflect.TypeOf(CustomModelConfig{}), customModelConfigLedger, "customModels entry"); err != nil {
		return err
	}
	return assertLedgerMatches(reflect.TypeOf(StatusLineConfig{}), statusLineConfigLedger, "statusLine object")
}

// assertLedgerMatches is the guard itself, kept separate so a test can point it
// at a synthetic struct and prove that an undeclared key is refused.
func assertLedgerMatches(docType reflect.Type, declared []string, what string) error {
	got := jsonFieldNames(docType)
	if !sameKeySet(got, declared) {
		return fmt.Errorf("agysettings: %s ledger is out of step with its struct: struct emits %v, ledger declares %v; add the missing entry to the ledger", what, got, declared)
	}
	return nil
}

// notManagedError builds the ErrNotManaged error, naming the ledger so the
// caller can see what is available.
func notManagedError(key string) error {
	return fmt.Errorf("%w: %q (managed keys: %s)", ErrNotManaged, key, strings.Join(ManagedKeys(), ", "))
}

// encodeJSON marshals v as compact JSON without HTML escaping, so preserved
// unmanaged values keep their original bytes.
func encodeJSON(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}

// sortedCustomModelKeys returns the map keys of m in deterministic order so
// validation errors are reproducible.
func sortedCustomModelKeys(m map[string]CustomModelConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
