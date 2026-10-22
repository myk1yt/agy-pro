package agysettings

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagedKeysIsTheDeclaredLedger(t *testing.T) {
	got := ManagedKeys()
	want := []string{KeyCustomModels, KeyModelProvider, KeyStatusLine}
	if len(got) != len(want) {
		t.Fatalf("ManagedKeys() = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ManagedKeys() = %v; want %v", got, want)
		}
	}
}

// ManagedKeys must hand out a copy: a caller sorting or trimming the result
// must not be able to corrupt the ledger.
func TestManagedKeysReturnsACopy(t *testing.T) {
	first := ManagedKeys()
	for i := range first {
		first[i] = "mutated"
	}
	second := ManagedKeys()
	for _, key := range second {
		if key == "mutated" {
			t.Fatalf("ManagedKeys() returned a shared slice: %v", second)
		}
	}
}

// The completeness guard: the ledger must cover every key the write paths can
// emit, in both directions.
func TestLedgerCoversSettingsDoc(t *testing.T) {
	emitted := settingsDocKeys()
	if !sameKeySet(emitted, ManagedKeys()) {
		t.Fatalf("SettingsDoc emits %v but the ledger declares %v; every writable key needs a ledger entry", emitted, ManagedKeys())
	}
	declared := ManagedKeys()
	for _, key := range declared {
		if !isManagedKey(key) {
			t.Errorf("ledger key %q is not accepted by isManagedKey", key)
		}
	}
}

func TestLedgerCoversCustomModelConfig(t *testing.T) {
	emitted := customModelConfigKeys()
	if !sameKeySet(emitted, customModelConfigLedger) {
		t.Fatalf("CustomModelConfig emits %v but the entry ledger declares %v", emitted, customModelConfigLedger)
	}
}

// Proof that the guard bites: adding a field to the document struct without a
// ledger entry must be refused, not silently written.
func TestLedgerGuardRefusesAnUndeclaredKey(t *testing.T) {
	type grown struct {
		ModelProvider string `json:"modelProvider,omitempty"`
		CustomModels  string `json:"customModels,omitempty"`
		Theme         string `json:"theme,omitempty"`
	}
	err := assertLedgerMatches(reflect.TypeOf(grown{}), ManagedKeys(), "settings")
	if err == nil {
		t.Fatal("assertLedgerMatches accepted a struct that emits an undeclared key")
	}
	if !strings.Contains(err.Error(), "theme") {
		t.Errorf("error does not name the undeclared key: %v", err)
	}
}

func TestLedgerGuardAcceptsAMatchingLedger(t *testing.T) {
	type same struct {
		A string `json:"modelProvider,omitempty"`
		B string `json:"customModels,omitempty"`
		C string `json:"statusLine,omitempty"`
	}
	if err := assertLedgerMatches(reflect.TypeOf(same{}), ManagedKeys(), "settings"); err != nil {
		t.Errorf("assertLedgerMatches refused a matching ledger: %v", err)
	}
}

func TestAssertLedgerCompletePassesToday(t *testing.T) {
	if err := assertLedgerComplete(); err != nil {
		t.Fatalf("assertLedgerComplete: %v", err)
	}
}

func TestSameKeySetIgnoresOrder(t *testing.T) {
	if !sameKeySet([]string{"b", "a"}, []string{"a", "b"}) {
		t.Error("sameKeySet should ignore order")
	}
	if sameKeySet([]string{"a"}, []string{"a", "b"}) {
		t.Error("sameKeySet should reject a length mismatch")
	}
	if sameKeySet([]string{"a", "c"}, []string{"a", "b"}) {
		t.Error("sameKeySet should reject a content mismatch")
	}
}

func TestJSONFieldNamesSkipsUntaggedAndIgnoredFields(t *testing.T) {
	type doc struct {
		Tagged   string `json:"tagged"`
		Optioned string `json:"optioned,omitempty"`
		Ignored  string `json:"-"`
		Untagged string
	}
	got := jsonFieldNames(reflect.TypeOf(doc{}))
	want := []string{"tagged", "optioned"}
	if !sameKeySet(got, want) {
		t.Errorf("jsonFieldNames = %v; want %v", got, want)
	}
}

// Set and Delete refuse everything outside the ledger: this package never
// touches unmanaged keys, not even to copy them through.
func TestSetDeleteRejectUnmanagedKeys(t *testing.T) {
	home := sandboxHome(t)
	f := openEmpty(t, filepath.Join(home, "settings.json"))
	defer f.Close()

	for _, key := range []string{"toolPermission", "theme", "mcpServers", "", "MODELPROVIDER", "custommodels"} {
		if err := f.Set(key, "x"); err == nil {
			t.Errorf("Set(%q) succeeded; want ErrNotManaged", key)
		} else if !isNotManaged(err) {
			t.Errorf("Set(%q) error = %v; want ErrNotManaged", key, err)
		}
		if err := f.Delete(key); err == nil {
			t.Errorf("Delete(%q) succeeded; want ErrNotManaged", key)
		} else if !isNotManaged(err) {
			t.Errorf("Delete(%q) error = %v; want ErrNotManaged", key, err)
		}
		if _, err := f.Get(key); err == nil {
			t.Errorf("Get(%q) succeeded; want ErrNotManaged", key)
		} else if !isNotManaged(err) {
			t.Errorf("Get(%q) error = %v; want ErrNotManaged", key, err)
		}
	}
}

func TestSetRejectsWrongValueShape(t *testing.T) {
	home := sandboxHome(t)
	f := openEmpty(t, filepath.Join(home, "settings.json"))
	defer f.Close()

	if err := f.Set(KeyModelProvider, 42); err == nil {
		t.Error("Set(modelProvider, 42) succeeded; want a type error")
	}
	if err := f.Set(KeyModelProvider, map[string]string{}); err == nil {
		t.Error("Set(modelProvider, map) succeeded; want a type error")
	}
	if err := f.Set(KeyCustomModels, "nope"); err == nil {
		t.Error("Set(customModels, string) succeeded; want a type error")
	}
	if err := f.Set(KeyCustomModels, map[string]string{"a": "b"}); err == nil {
		t.Error("Set(customModels, map[string]string) succeeded; want a type error")
	}
}

func TestSetCustomModelsRejectsAnEntryWithoutModelName(t *testing.T) {
	home := sandboxHome(t)
	f := openEmpty(t, filepath.Join(home, "settings.json"))
	defer f.Close()

	bad := map[string]CustomModelConfig{
		"gemini-api://127.0.0.1:8791/models/probe": {DisplayName: "no modelName"},
	}
	err := f.Set(KeyCustomModels, bad)
	if err == nil {
		t.Fatal("Set accepted a customModels entry without modelName; agy would reject the file")
	}
	if !strings.Contains(err.Error(), "modelName") {
		t.Errorf("error does not mention modelName: %v", err)
	}
}

// The document a write cycle emits must carry only ledger keys plus whatever
// unmanaged keys were already in the file.
func TestWrittenDocumentCarriesOnlyLedgerAndPreexistingKeys(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	plant := []byte(`{"toolPermission":"always-proceed","mcpServers":{"a":{"command":"x"}}}`)
	if err := writeFileForTest(path, plant); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var doc map[string]json.RawMessage
	raw := readFileForTest(t, path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written document is not valid JSON: %v", err)
	}
	for key := range doc {
		if isManagedKey(key) {
			continue
		}
		if _, planted := map[string]bool{"toolPermission": true, "mcpServers": true}[key]; !planted {
			t.Errorf("write emitted an unexpected key %q", key)
		}
	}
	if _, ok := doc[KeyModelProvider]; !ok {
		t.Errorf("managed key %q missing from the written document", KeyModelProvider)
	}
}

func isNotManaged(err error) bool {
	return errors.Is(err, ErrNotManaged)
}
