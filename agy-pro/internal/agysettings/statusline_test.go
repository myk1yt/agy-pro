package agysettings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The exact bytes Node's scripts/lib/configure-statusline.js:121-126 writes for
// statusLine, compacted. Every round-trip pin below is anchored to this shape.
const (
	nodeStatusLineJSON = `{"type":"command","command":"C:\\Tools\\agy-statusline.cmd","enabled":true,"stack_with_default":true}`
	nodeStatusLineCmd  = `C:\Tools\agy-statusline.cmd`
)

// nodeSettingsDoc is a settings.json as the Node writer leaves it: the managed
// statusLine object plus the unmanaged trustedWorkspaces array Node also writes.
const nodeSettingsDoc = `{"statusLine":` + nodeStatusLineJSON + `,"trustedWorkspaces":["C:\\Work\\proj","C:\\Work\\other"]}`

// docRaw decodes a document into its raw per-key bytes so a test can compare a
// slot byte for byte without re-encoding it.
func docRaw(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("document is not valid JSON: %v\n%s", err, data)
	}
	return out
}

// TestNewStatusLineMatchesTheNodeShape pins the constructor to the bytes the
// Node writer emits: same four fields, same order, same values.
func TestNewStatusLineMatchesTheNodeShape(t *testing.T) {
	enc, err := encodeJSON(NewStatusLine(nodeStatusLineCmd))
	if err != nil {
		t.Fatalf("encodeJSON: %v", err)
	}
	if string(enc) != nodeStatusLineJSON {
		t.Errorf("NewStatusLine encoded as\n got %s\nwant %s", enc, nodeStatusLineJSON)
	}
}

// A Node-shaped statusLine object round trips: the managed slot keeps its exact
// bytes and the unmanaged neighbour is untouched.
func TestNodeShapedStatusLineRoundTrips(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(nodeSettingsDoc)); err != nil {
		t.Fatal(err)
	}
	before := docRaw(t, []byte(nodeSettingsDoc))

	f := openEmpty(t, path)
	defer f.Close()

	got, err := f.Get(KeyStatusLine)
	if err != nil {
		t.Fatalf("Get(%s): %v", KeyStatusLine, err)
	}
	sl, ok := got.(*StatusLineConfig)
	if !ok {
		t.Fatalf("Get(%s) = %T; want *StatusLineConfig", KeyStatusLine, got)
	}
	if sl.Type != statusLineTypeCommand || sl.Command != nodeStatusLineCmd {
		t.Errorf("decoded object = %+v; want type %q and the planted command", sl, statusLineTypeCommand)
	}
	if !sl.Enabled {
		t.Error("decoded enabled = false; want true")
	}
	if sl.StackWithDefault == nil || !*sl.StackWithDefault {
		t.Errorf("decoded stack_with_default = %v; want true", sl.StackWithDefault)
	}
	if keys := f.Keys(); len(keys) != 1 || keys[0] != KeyStatusLine {
		t.Errorf("Keys() = %v; want [%s] for a document carrying only that managed slot", keys, KeyStatusLine)
	}

	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	after := docRaw(t, readFileForTest(t, path))
	if string(after[KeyStatusLine]) != string(before[KeyStatusLine]) {
		t.Errorf("statusLine bytes changed:\n got %s\nwant %s", after[KeyStatusLine], before[KeyStatusLine])
	}
	if string(after["trustedWorkspaces"]) != string(before["trustedWorkspaces"]) {
		t.Errorf("unmanaged trustedWorkspaces changed:\n got %s\nwant %s", after["trustedWorkspaces"], before["trustedWorkspaces"])
	}
	if string(after[KeyModelProvider]) != `"gemini"` {
		t.Errorf("modelProvider = %s; want the managed write to land", after[KeyModelProvider])
	}
}

// An explicit false is data, not absence: both booleans survive a write as false.
func TestStatusLineExplicitFalseBooleansSurvive(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"statusLine":{"type":"command","command":"C:\\Tools\\s.cmd","enabled":false,"stack_with_default":false}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := docRaw(t, readFileForTest(t, path))
	want := `{"type":"command","command":"C:\\Tools\\s.cmd","enabled":false,"stack_with_default":false}`
	if string(got[KeyStatusLine]) != want {
		t.Errorf("statusLine = %s; want %s", got[KeyStatusLine], want)
	}
}

// An absent stack_with_default stays absent: it is the one optional slot.
func TestStatusLineAbsentStackWithDefaultStaysAbsent(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"statusLine":{"type":"command","command":"C:\\Tools\\s.cmd","enabled":true}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := docRaw(t, readFileForTest(t, path))
	if string(got[KeyStatusLine]) != `{"type":"command","command":"C:\\Tools\\s.cmd","enabled":true}` {
		t.Errorf("statusLine = %s; want the absent optional slot to stay absent", got[KeyStatusLine])
	}
}

// Set replaces exactly the managed slot, and Delete removes exactly it: the
// unmanaged neighbour keeps its bytes through both writes.
func TestStatusLineSetAndDeleteTouchOnlyTheManagedSlot(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(nodeSettingsDoc)); err != nil {
		t.Fatal(err)
	}
	before := docRaw(t, []byte(nodeSettingsDoc))

	f := openEmpty(t, path)
	defer f.Close()

	replacement := `C:\Users\k1yt\bin\agy-statusline.cmd`
	if err := f.Set(KeyStatusLine, NewStatusLine(replacement)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after := docRaw(t, readFileForTest(t, path))
	want := `{"type":"command","command":"C:\\Users\\k1yt\\bin\\agy-statusline.cmd","enabled":true,"stack_with_default":true}`
	if string(after[KeyStatusLine]) != want {
		t.Errorf("statusLine after Set = %s; want %s", after[KeyStatusLine], want)
	}
	if string(after["trustedWorkspaces"]) != string(before["trustedWorkspaces"]) {
		t.Errorf("Set damaged trustedWorkspaces:\n got %s\nwant %s", after["trustedWorkspaces"], before["trustedWorkspaces"])
	}

	if err := f.Delete(KeyStatusLine); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	after = docRaw(t, readFileForTest(t, path))
	if _, present := after[KeyStatusLine]; present {
		t.Errorf("statusLine survived Delete: %s", after[KeyStatusLine])
	}
	if string(after["trustedWorkspaces"]) != string(before["trustedWorkspaces"]) {
		t.Errorf("Delete damaged trustedWorkspaces:\n got %s\nwant %s", after["trustedWorkspaces"], before["trustedWorkspaces"])
	}
	if keys := f.Keys(); len(keys) != 0 {
		t.Errorf("Keys() = %v; want none after deleting the only managed slot", keys)
	}
}

// A document that never carried a statusLine must not gain one: the ledger entry
// is additive, not an injection.
func TestStatusLineIsNotInjectedIntoADocumentWithoutIt(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"trustedWorkspaces":["C:\\Work\\proj"]}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	if v, err := f.Get(KeyStatusLine); err != nil {
		t.Fatalf("Get: %v", err)
	} else if v != nil {
		t.Errorf("Get(statusLine) = %v; want nil for a document without the key", v)
	}
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := docRaw(t, readFileForTest(t, path))
	if _, present := got[KeyStatusLine]; present {
		t.Errorf("the write injected statusLine: %s", got[KeyStatusLine])
	}
	if string(got["trustedWorkspaces"]) != `["C:\\Work\\proj"]` {
		t.Errorf("unmanaged trustedWorkspaces changed: %s", got["trustedWorkspaces"])
	}
}

// An undeclared field INSIDE the managed object fails closed: the document is
// the user's, and silently dropping a field would change agy's behavior.
func TestStatusLineUnknownFieldFailsClosed(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"statusLine":{"type":"command","command":"C:\\Tools\\s.cmd","enabled":true,"theme":"dark"}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}
	md5Before := md5Sum(readFileForTest(t, path))

	f, err := Open(path)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("Open accepted a statusLine object carrying an undeclared field")
	}
	for _, want := range []string{KeyStatusLine, "theme"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	assertNoLeakedPath(t, err, home)
	if md5After := md5Sum(readFileForTest(t, path)); md5After != md5Before {
		t.Error("the refused Open modified the document")
	}
}

// enabled is required: a plain bool cannot distinguish absent from false, so an
// object without it is refused rather than defaulted to false.
func TestStatusLineRequiresEnabled(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"statusLine":{"type":"command","command":"C:\\Tools\\s.cmd","stack_with_default":true}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("Open accepted a statusLine object without enabled")
	}
	if !strings.Contains(err.Error(), "enabled") {
		t.Errorf("error does not name the missing slot: %v", err)
	}
	assertNoLeakedPath(t, err, home)
}

// A statusLine slot that is not an object is a clear error, never a guess.
func TestStatusLineWrongShapeFailsClosed(t *testing.T) {
	cases := map[string]string{
		"string": `{"statusLine":"C:\\Tools\\s.cmd"}`,
		"number": `{"statusLine":42}`,
		"array":  `{"statusLine":[]}`,
		"bool":   `{"statusLine":true}`,
		"null":   `{"statusLine":null}`,
		"empty":  `{"statusLine":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home := sandboxHome(t)
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(body)); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			if err == nil {
				t.Fatalf("Open accepted %s", body)
			}
			if !strings.Contains(err.Error(), KeyStatusLine) {
				t.Errorf("error does not name the key: %v", err)
			}
			assertNoLeakedPath(t, err, home)
		})
	}
}

// Set accepts the two managed shapes and nothing else.
func TestStatusLineSetRejectsForeignShapes(t *testing.T) {
	home := sandboxHome(t)
	f := openEmpty(t, filepath.Join(home, "settings.json"))
	defer f.Close()

	for _, value := range []any{
		nodeStatusLineCmd,
		42,
		map[string]any{"type": "command"},
		map[string]string{"command": nodeStatusLineCmd},
	} {
		err := f.Set(KeyStatusLine, value)
		if err == nil {
			t.Errorf("Set(statusLine, %T) succeeded; want a shape error", value)
			continue
		}
		if !strings.Contains(err.Error(), KeyStatusLine) {
			t.Errorf("Set(statusLine, %T) error does not name the key: %v", value, err)
		}
	}

	// The value form and the pointer form are both accepted.
	if err := f.Set(KeyStatusLine, *NewStatusLine(nodeStatusLineCmd)); err != nil {
		t.Errorf("Set(statusLine, StatusLineConfig) failed: %v", err)
	}
	if err := f.Set(KeyStatusLine, NewStatusLine(nodeStatusLineCmd)); err != nil {
		t.Errorf("Set(statusLine, *StatusLineConfig) failed: %v", err)
	}
}

// Set validates the two slots agy cannot resolve without, and never echoes the
// command path in the diagnostic.
func TestStatusLineSetValidatesRequiredSlots(t *testing.T) {
	home := sandboxHome(t)
	f := openEmpty(t, filepath.Join(home, "settings.json"))
	defer f.Close()

	if err := f.Set(KeyStatusLine, &StatusLineConfig{Command: nodeStatusLineCmd}); err == nil {
		t.Error("Set accepted a statusLine without type; want a validation error")
	} else if !strings.Contains(err.Error(), "type") {
		t.Errorf("error does not name the missing type: %v", err)
	}

	err := f.Set(KeyStatusLine, &StatusLineConfig{Type: statusLineTypeCommand, Command: "  "})
	if err == nil {
		t.Fatal("Set accepted a statusLine with a blank command")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error does not name the missing command: %v", err)
	}
	if strings.Contains(err.Error(), `C:\Tools`) {
		t.Errorf("the error echoed a command path: %v", err)
	}

	// A nil value clears the key, exactly like Delete.
	if err := f.Set(KeyStatusLine, NewStatusLine(nodeStatusLineCmd)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Set(KeyStatusLine, (*StatusLineConfig)(nil)); err != nil {
		t.Fatalf("Set(nil *StatusLineConfig): %v", err)
	}
	if v, err := f.Get(KeyStatusLine); err != nil {
		t.Fatalf("Get: %v", err)
	} else if v != nil {
		t.Errorf("Get(statusLine) = %v after a nil Set; want nil", v)
	}
}

// The relock merge keeps a caller's statusLine intent over the on-disk value.
func TestMergeManagedKeepsStatusLineIntent(t *testing.T) {
	onDisk := &StatusLineConfig{Type: statusLineTypeCommand, Command: "disk"}
	intent := NewStatusLine("intent")

	got := mergeManaged(SettingsDoc{StatusLine: intent}, SettingsDoc{StatusLine: onDisk})
	if got.StatusLine == nil || got.StatusLine.Command != "intent" {
		t.Errorf("mergeManaged StatusLine = %+v; want the caller's intent to win", got.StatusLine)
	}

	got = mergeManaged(SettingsDoc{}, SettingsDoc{StatusLine: onDisk})
	if got.StatusLine == nil || got.StatusLine.Command != "disk" {
		t.Errorf("mergeManaged StatusLine = %+v; want the on-disk value for an empty intent", got.StatusLine)
	}
}

// The nested ledger is kept in step with the struct, and the guard bites when a
// field is added without an entry, exactly like the other managed kinds.
func TestStatusLineLedgerGuard(t *testing.T) {
	emitted := statusLineConfigKeys()
	if !sameKeySet(emitted, statusLineConfigLedger) {
		t.Fatalf("StatusLineConfig emits %v but the object ledger declares %v", emitted, statusLineConfigLedger)
	}
	if err := assertLedgerMatches(reflect.TypeOf(StatusLineConfig{}), statusLineConfigLedger, "statusLine object"); err != nil {
		t.Errorf("assertLedgerMatches refused the matching ledger: %v", err)
	}

	type grown struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Enabled bool   `json:"enabled"`
		Theme   string `json:"theme"`
	}
	err := assertLedgerMatches(reflect.TypeOf(grown{}), statusLineConfigLedger, "statusLine object")
	if err == nil {
		t.Fatal("assertLedgerMatches accepted an object that emits an undeclared field")
	}
	if !strings.Contains(err.Error(), "theme") {
		t.Errorf("error does not name the undeclared field: %v", err)
	}

	if err := assertLedgerComplete(); err != nil {
		t.Errorf("assertLedgerComplete: %v", err)
	}
	if !isManagedKey(KeyStatusLine) {
		t.Error("statusLine is not in the managed ledger")
	}
}

// Declaring the key in the ledger does NOT open the protected global path: the
// refusal is a property of the path, and the explicit override is still the only
// door. This pin exists because T1 is a ledger change, not a guard change.
func TestStatusLineLedgerDoesNotOpenTheGlobalDoor(t *testing.T) {
	sandboxHome(t)
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}

	// Open still refuses the path outright.
	f, oerr := Open(global)
	if f != nil {
		_ = f.Close()
	}
	if !errors.Is(oerr, ErrGlobalSettingsPath) {
		t.Fatalf("Open(global) error = %v; want ErrGlobalSettingsPath", oerr)
	}

	// The write path refuses a handle that never passed through the guarded Open,
	// even when the pending intent is a statusLine.
	if err := os.MkdirAll(filepath.Dir(global), settingsDirPerm); err != nil {
		t.Fatalf("create the settings directory: %v", err)
	}
	owner, _, err := acquireLock(global)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	handle := &File{path: global, owner: owner, raw: map[string]json.RawMessage{}, doc: SettingsDoc{StatusLine: NewStatusLine(nodeStatusLineCmd)}}

	if werr := handle.Write(); !errors.Is(werr, ErrGlobalSettingsPath) {
		t.Fatalf("Write error = %v; want ErrGlobalSettingsPath", werr)
	}
	if _, serr := os.Stat(global); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the refused Write still created the global settings file (stat err = %v)", serr)
	}

	// The deliberate override is what unblocks it, and only that.
	handle.allowGlobal = true
	if werr := handle.Write(); werr != nil {
		t.Fatalf("Write with the override: %v", werr)
	}
	if cerr := handle.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	got := docRaw(t, readFileForTest(t, global))
	if string(got[KeyStatusLine]) != nodeStatusLineJSON {
		t.Errorf("global statusLine = %s; want %s", got[KeyStatusLine], nodeStatusLineJSON)
	}
}
