package fanout

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

func TestPresetStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	st := &PresetFileStore{Path: path}

	want := &PresetsFile{
		Presets: []Preset{
			{
				Name:           "nightly",
				Task:           "security",
				N:              5,
				TimeoutSeconds: 600,
				Tools:          "read,search,write",
				MaxTurns:       12,
				MaxNative:      3,
				AllowHosts:     []string{"example.com", "docs.example.org"},
				SearchEndpoint: "https://search.example/{query}",
				SearchKeyEnv:   "P8_SEARCH_KEY_NAME",
				OutDir:         "reports/nightly",
				Root:           "D:/ws",
				Models:         "prov/m1,prov/m2",
				JSON:           true,
			},
			{Name: "minimal", Seats: "native:gemini-x,prov/m1"},
		},
		DefaultPreset: "nightly",
	}
	if err := st.SavePresets(want); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// The record contract (E.1) pins snake_case JSON keys with omitempty
// everywhere; a camelCase drift would silently break hand-edited files and
// the future TUI editor.
func TestPresetRecordShapeSnakeCaseOmitempty(t *testing.T) {
	full, err := json.Marshal(Preset{
		Name:           "nightly",
		Task:           "security",
		N:              5,
		TimeoutSeconds: 600,
		Tools:          "read",
		MaxTurns:       12,
		MaxNative:      3,
		AllowHosts:     []string{"example.com"},
		SearchEndpoint: "https://search.example/{query}",
		SearchKeyEnv:   "P8_SEARCH_KEY_NAME",
		OutDir:         "out",
		Root:           ".",
		Models:         "m",
		Seats:          "s",
		JSON:           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(full, &keys); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{
		"name", "task", "n", "timeout_seconds", "tools", "max_turns",
		"max_native", "allow_hosts", "search_endpoint", "search_key_env",
		"out_dir", "root", "models", "seats", "json",
	}
	for _, k := range wantKeys {
		if _, ok := keys[k]; !ok {
			t.Errorf("full preset JSON missing key %q (got %s)", k, full)
		}
	}
	for k := range keys {
		ok := false
		for _, w := range wantKeys {
			if k == w {
				ok = true
			}
		}
		if !ok {
			t.Errorf("unexpected key %q in full preset JSON (want snake_case contract only)", k)
		}
	}

	// Empty preset: only "name" survives (omitempty everywhere).
	min, err := json.Marshal(Preset{Name: "minimal"})
	if err != nil {
		t.Fatal(err)
	}
	if string(min) != `{"name":"minimal"}` {
		t.Errorf("empty preset JSON = %s; want {\"name\":\"minimal\"} (omitempty everywhere)", min)
	}

	// The file-level default_preset key round-trips.
	fileJSON, err := json.Marshal(PresetsFile{DefaultPreset: "nightly"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fileJSON), `"default_preset":"nightly"`) {
		t.Errorf("PresetsFile JSON missing default_preset: %s", fileJSON)
	}
}

func TestPresetStoreCorruptFileFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fanout-presets.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	st := &PresetFileStore{Path: path, WarnOut: &stderr}
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("corrupt file must not error: %v", err)
	}
	if len(got.Presets) != 0 || got.DefaultPreset != "" {
		t.Errorf("corrupt file fallback = %+v; want an empty store", got)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "corrupt") {
		t.Errorf("corrupt fallback must emit exactly one warning line containing %q; got %q", "corrupt", stderr.String())
	}
}

func TestPresetStoreMissingFileIsEmptyStore(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "nope", "fanout-presets.json")}
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(got.Presets) != 0 {
		t.Errorf("missing file = %+v; want empty store", got)
	}
}

func TestPresetStoreCreateOnDemand(t *testing.T) {
	// The presets directory does not exist yet; SavePresets must create it
	// and the file (WriteFileAtomic does MkdirAll).
	path := filepath.Join(t.TempDir(), "fresh-home", "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	if err := st.SavePresets(&PresetsFile{Presets: []Preset{{Name: "a"}}}); err != nil {
		t.Fatalf("SavePresets into a missing directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file was not created on demand: %v", err)
	}
	got, err := st.LoadPresets()
	if err != nil || len(got.Presets) != 1 || got.Presets[0].Name != "a" {
		t.Errorf("reload after create-on-demand = %+v, %v; want one preset %q", got, err, "a")
	}
}

func TestPresetStoreWrites0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	if err := st.SavePresets(&PresetsFile{Presets: []Preset{{Name: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows reports only the read-only attribute (0666/0444), not unix
		// mode bits; real modes live in the ACL (agysettings precedent).
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("presets file mode = %#o; want 0600", mode)
	}
}

func TestPresetStoreSearchKeyEnvNameAccepted(t *testing.T) {
	t.Setenv("P8_SEARCH_KEY_NAME", "dummy-value")
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	p := Preset{Name: "a", SearchEndpoint: "https://search.example/{query}", SearchKeyEnv: "P8_SEARCH_KEY_NAME"}
	if err := st.SavePresets(&PresetsFile{Presets: []Preset{p}}); err != nil {
		t.Fatalf("env-var NAME must be accepted at save time: %v", err)
	}
	got, err := st.LoadPresets()
	if err != nil || got.Presets[0].SearchKeyEnv != "P8_SEARCH_KEY_NAME" {
		t.Errorf("search_key_env did not round-trip: %+v, %v", got, err)
	}
}

// A value-looking search_key_env is rejected at save time and the error must
// never echo the value (B4/U8 class).
func TestPresetStoreSearchKeyEnvValueRejectedNotEchoed(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	values := []string{
		"sk-abcdef1234567890123456789",
		testfixtures.AKIAExample,
	}
	for _, v := range values {
		err := st.SavePresets(&PresetsFile{Presets: []Preset{{Name: "a", SearchKeyEnv: v}}})
		if err == nil {
			t.Errorf("value-shaped search_key_env %q must be rejected", v)
			continue
		}
		if strings.Contains(err.Error(), v) {
			t.Errorf("error echoes the rejected value: %v", err)
		}
		// The file must not have been written.
		if _, statErr := os.Stat(st.Path); statErr == nil {
			t.Errorf("a rejected save must not write the file (value %q)", v)
		}
	}
}

func TestPresetStoreRejectsEmptyName(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	if err := st.SavePresets(&PresetsFile{Presets: []Preset{{N: 3}}}); err == nil {
		t.Fatal("a preset without a name must be rejected")
	}
}

func TestDefaultPresetsPathRespectsAgyProHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	p, err := DefaultPresetsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "fanout-presets.json"); p != want {
		t.Errorf("DefaultPresetsPath = %q; want %q (AGY_PRO_HOME aware)", p, want)
	}
}

// D9: the user-driven save entry point refuses a preset that pins both seat
// sources (the combination buildRunConfig hard-errors on at run time), with a
// message naming both fields; either pin alone is accepted.
func TestValidatePresetForSaveRejectsModelsAndSeatsTogether(t *testing.T) {
	both := Preset{Name: "a", Models: "prov/m1", Seats: "native:gemini-x"}
	err := ValidatePresetForSave(both)
	if err == nil {
		t.Fatal("a preset pinning both models and seats must be refused at save time")
	}
	// The refusal carries the sentinel the TUI editor matches to render its own
	// localized message (FIX-CODE-C3: one rule set, two registers).
	if !errors.Is(err, ErrPresetSeatConflict) {
		t.Errorf("refusal %v must wrap ErrPresetSeatConflict", err)
	}
	for _, field := range []string{"models", "seats", "cannot be combined"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("refusal %q must name %q", err.Error(), field)
		}
	}
	// Never echo the operands (the F5 masking family): the refusal names the
	// fields only.
	if strings.Contains(err.Error(), "prov/m1") || strings.Contains(err.Error(), "gemini-x") {
		t.Errorf("refusal echoed the user operands: %q", err.Error())
	}

	if err := ValidatePresetForSave(Preset{Name: "a", Models: "prov/m1"}); err != nil {
		t.Errorf("models-only preset must be accepted: %v", err)
	}
	if err := ValidatePresetForSave(Preset{Name: "a", Seats: "native:gemini-x"}); err != nil {
		t.Errorf("seats-only preset must be accepted: %v", err)
	}

	// The entry point keeps every store-boundary rule too.
	if err := ValidatePresetForSave(Preset{Models: "prov/m1"}); !errors.Is(err, ErrPresetNameRequired) {
		t.Errorf("a nameless preset must still be refused with ErrPresetNameRequired; got %v", err)
	}
}

// D13 (FIX-CODE-C3/LOW-4): the shared save entry point carries the unknown-task
// rule the editor used to hold inline, so a user-driven writer cannot persist a
// task name the engine would silently resolve to the review prompt. An empty
// task stays legal (it means "engine default"), and the refusal never echoes
// the typed name.
func TestValidatePresetForSaveRefusesUnknownTask(t *testing.T) {
	err := ValidatePresetForSave(Preset{Name: "a", Task: "secuirty"})
	if !errors.Is(err, ErrPresetUnknownTask) {
		t.Fatalf("unknown task must be refused with ErrPresetUnknownTask; got %v", err)
	}
	if strings.Contains(err.Error(), "secuirty") {
		t.Errorf("refusal echoed the rejected operand: %q", err.Error())
	}
	for _, task := range []string{"", "review", "Security", " custom "} {
		if err := ValidatePresetForSave(Preset{Name: "a", Task: task}); err != nil {
			t.Errorf("task %q must be accepted: %v", task, err)
		}
	}
}

// D14 (FIX-CODE-C3/LOW-4): the shared save entry point carries the allow_hosts
// shape rule, so a save can no longer persist a host entry the exact allow-list
// match can never satisfy (scheme prefix, wildcard, path). Blank rows and
// host:port entries stay legal (agenttools.ValidateAllowHosts is the same
// predicate the run-time host list uses), and the refusal never echoes the
// rejected entry.
func TestValidatePresetForSaveRefusesInvalidAllowHosts(t *testing.T) {
	err := ValidatePresetForSave(Preset{Name: "a", AllowHosts: []string{"*.example.com"}})
	if !errors.Is(err, ErrPresetAllowHosts) {
		t.Fatalf("a wildcard host must be refused with ErrPresetAllowHosts; got %v", err)
	}
	if strings.Contains(err.Error(), "*.example.com") {
		t.Errorf("refusal echoed the rejected operand: %q", err.Error())
	}
	valid := [][]string{
		nil,
		{"example.com"},
		{"", "docs.example.org", "127.0.0.1:8443"},
	}
	for _, hosts := range valid {
		if err := ValidatePresetForSave(Preset{Name: "a", AllowHosts: hosts}); err != nil {
			t.Errorf("allow_hosts %v must be accepted: %v", hosts, err)
		}
	}
}

// The store-boundary half of the same split: SavePresets runs ONLY the
// store-boundary rules, so a loadable-but-unrunnable row (seat conflict,
// unknown task, bad host) still round-trips. That permissiveness is what the
// P3-1 ladder guard depends on (preset_default_key_guard_test.go seeds these
// shapes through SavePresets), and ValidatePresetForSave is the one layer that
// refuses them for user-driven writers.
func TestPresetStoreSaveRoundTripsSelfConflictingPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	rec := Preset{Name: "a", Models: "prov/m1", Seats: "native:gemini-x"}
	want := &PresetsFile{Presets: []Preset{rec}}
	if err := st.SavePresets(want); err != nil {
		t.Fatalf("SavePresets must keep accepting loadable state: %v", err)
	}
	// Same record, user-driven entry point: refused (P3-1 vs D9 boundary).
	if err := ValidatePresetForSave(rec); !errors.Is(err, ErrPresetSeatConflict) {
		t.Errorf("ValidatePresetForSave must refuse the record SavePresets accepts; got %v", err)
	}
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// The D13/D14 shapes follow the same division: the store round-trips them.
	unrunnable := &PresetsFile{Presets: []Preset{
		{Name: "typo", Task: "secuirty"},
		{Name: "badhost", AllowHosts: []string{"https://example.com/x"}},
	}}
	if err := st.SavePresets(unrunnable); err != nil {
		t.Fatalf("SavePresets must keep accepting loadable state (task/host rules): %v", err)
	}
	got, err = st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if !reflect.DeepEqual(got, unrunnable) {
		t.Errorf("round trip mismatch (task/host rules):\n got %+v\nwant %+v", got, unrunnable)
	}
	if err := ValidatePresetForSave(unrunnable.Presets[0]); !errors.Is(err, ErrPresetUnknownTask) {
		t.Errorf("the same unknown-task row must be refused for a user-driven save; got %v", err)
	}
	if err := ValidatePresetForSave(unrunnable.Presets[1]); !errors.Is(err, ErrPresetAllowHosts) {
		t.Errorf("the same bad-host row must be refused for a user-driven save; got %v", err)
	}
}
