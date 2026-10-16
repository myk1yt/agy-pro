package agysettings

import (
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
)

// ErrNotManaged is returned when a caller asks for a settings key that is not
// in the managed ledger. This package never touches unmanaged keys.
var ErrNotManaged = errors.New("agysettings: key is not in the managed ledger")

// keyKind is the value shape a ledger key accepts.
type keyKind int

const (
	kindString keyKind = iota
	kindCustomModels
)

// ledger is the single declaration of the keys this package can write. A key
// absent here cannot be read, written, or deleted, and a document slot that is
// not declared here makes every write fail closed (assertLedgerComplete).
var ledger = map[string]keyKind{
	KeyModelProvider: kindString,
	KeyCustomModels:  kindCustomModels,
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
	return assertLedgerMatches(reflect.TypeOf(CustomModelConfig{}), customModelConfigLedger, "customModels entry")
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
