package fanout

import (
	"bytes"
	"encoding/json"
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
				Seats:          "native:gemini-x,prov/m1",
				JSON:           true,
			},
			{Name: "minimal"},
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
