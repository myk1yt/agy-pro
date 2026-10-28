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
		"out_dir", "root", "seats", "json",
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

// The retired preset `models` key is DISCLOSED and IGNORED at the load door: the
// record survives (its other knobs still apply), the value is never echoed,
// applied or re-saved, and PresetsFile.LegacyModels carries the masked name for a
// surface with its own localized copy. A file without the key stays silent, so
// the advisory is evidence of a legacy row, never of a healthy load.
func TestLoadPresetsDisclosesRetiredModelsField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fanout-presets.json")
	// The struct cannot carry the key any more, so the on-disk shape is the only
	// way to reproduce a pre-removal or hand-edited file.
	body := `{"presets":[{"name":"legacy","n":4,"models":"prov/m1,prov/LEGACYOPERAND"},{"name":"clean","seats":"native:gemini-a"}],"default_preset":"legacy"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var notice bytes.Buffer
	st := &PresetFileStore{Path: path, NoticeOut: &notice}
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("a legacy models key must never fail the load: %v", err)
	}
	if len(got.Presets) != 2 {
		t.Fatalf("both records must survive: %+v", got.Presets)
	}
	if got.Presets[0].N != 4 || got.Presets[0].Seats != "" {
		t.Errorf("the record's kept knobs drifted: %+v", got.Presets[0])
	}
	lines := strings.Split(strings.TrimRight(notice.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one advisory for the one legacy record; got %d: %q", len(lines), notice.String())
	}
	for _, want := range []string{`preset "legacy"`, "the legacy models field is removed; use seats"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("advisory %q missing %q", lines[0], want)
		}
	}
	if strings.Contains(notice.String(), "LEGACYOPERAND") {
		t.Errorf("the advisory echoed the stored value: %q", notice.String())
	}
	if len(got.LegacyModels) != 1 || got.LegacyModels[0] != "legacy" {
		t.Errorf("LegacyModels = %v; want the one masked record name", got.LegacyModels)
	}

	// Silent without the key: a clean file is not an advisory source.
	notice.Reset()
	if _, err := st.LoadPresets(); err != nil {
		t.Fatal(err)
	}
	notice.Reset()
	if err := st.SavePresets(got); err != nil {
		t.Fatal(err)
	}
	after, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if notice.Len() != 0 {
		t.Errorf("a rewritten file must load silently; got %q", notice.String())
	}
	if len(after.LegacyModels) != 0 {
		t.Errorf("LegacyModels survived the rewrite: %v", after.LegacyModels)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"models"`) {
		t.Errorf("the save door re-emitted the retired key: %s", raw)
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

// The retired preset `models` field is GONE from the record shape: the save-time
// entry point accepts a seats pin and, because the JSON key no longer exists,
// there is no both-pin conflict to refuse any more. This pin states the
// replacement contract: seats is the only seat source a writer can set.
func TestValidatePresetForSaveAcceptsSeatsPinAndHasNoModelsField(t *testing.T) {
	if err := ValidatePresetForSave(Preset{Name: "a", Seats: "native:gemini-x"}); err != nil {
		t.Errorf("a seats-pinned preset must be accepted: %v", err)
	}
	// A hand-edited record carrying the retired key loads with the value
	// IGNORED (see TestLoadPresetsDisclosesRetiredModelsField), so the save
	// rules never see it: the only seat source left is seats.
	if _, ok := reflect.TypeOf(Preset{}).FieldByName("Models"); ok {
		t.Fatal("Preset still carries a Models field; the retired key must be gone from the shape")
	}
}

// U8 hardening (explore-report section 5): a hand-edited fanout-presets.json
// can carry a name the TUI can never type. The FL-4 load gate drops that row,
// and the independent store boundary refuses any direct save carrying a NUL or
// another C0 control rune, fail-closed for the whole file.
func TestPresetStoreSaveRefusesControlName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	raw := `{"presets":[{"name":"\u0000builtin","task":"review"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("hand-seed preset file: %v", err)
	}
	var warn bytes.Buffer
	st := &PresetFileStore{Path: path, WarnOut: &warn}
	pf, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if len(pf.Presets) != 0 {
		t.Fatalf("loaded presets = %d, want the control-named row dropped", len(pf.Presets))
	}
	if !strings.Contains(warn.String(), "name has control/format runes") {
		t.Errorf("load warning = %q; want the name-field drop notice", warn.String())
	}
	// A direct save still carries the independent write-side guard.
	if err := st.SavePresets(&PresetsFile{Presets: []Preset{{Name: "\x00builtin"}}}); !errors.Is(err, ErrPresetNameControl) {
		t.Fatalf("SavePresets of a NUL-named preset = %v, want ErrPresetNameControl", err)
	}
	// The same guard covers every other C0 rune, not only NUL.
	for _, name := range []string{"a\x01b", "x\x1ff", "\x00"} {
		err := st.SavePresets(&PresetsFile{Presets: []Preset{{Name: name}}})
		if !errors.Is(err, ErrPresetNameControl) {
			t.Errorf("SavePresets of name %q = %v, want ErrPresetNameControl", name, err)
		}
	}
	// Names without control runes still save, so the guard never overreaches.
	clean := &PresetsFile{Presets: []Preset{{Name: "fine"}}}
	if err := st.SavePresets(clean); err != nil {
		t.Fatalf("SavePresets of a clean name: %v", err)
	}
	reloaded, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("reload after clean save: %v", err)
	}
	if len(reloaded.Presets) != 1 || reloaded.Presets[0].Name != "fine" {
		t.Fatalf("clean save round trip = %+v, want [fine]", reloaded.Presets)
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
// store-boundary rules, so a loadable-but-unrunnable row (unknown task, bad
// host) still round-trips. That permissiveness is what the P3-1 ladder guard
// depends on (preset_default_key_guard_test.go seeds these shapes through
// SavePresets), and ValidatePresetForSave is the one layer that refuses them
// for user-driven writers.
func TestPresetStoreSaveRoundTripsSelfConflictingPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	rec := Preset{Name: "a", Task: "secuirty", Seats: "native:gemini-x"}
	want := &PresetsFile{Presets: []Preset{rec}}
	if err := st.SavePresets(want); err != nil {
		t.Fatalf("SavePresets must keep accepting loadable state: %v", err)
	}
	// Same record, user-driven entry point: refused (P3-1 vs D13 boundary).
	if err := ValidatePresetForSave(rec); !errors.Is(err, ErrPresetUnknownTask) {
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

// Sweep 2 (structured seat lineup): the shared save entry point carries the
// run-time seats grammar, so a user-driven writer cannot persist a lineup the
// engine would reject at run start (ParseSeatSpecs, seatspec.go). The empty
// value stays legal: it means "the engine auto-selects the lineup", and that
// native-first fallback is deliberately untouched. The refusal names the seat
// NUMBER through the wrapped parser error and never restates the rule.
func TestValidatePresetForSaveRefusesMalformedSeats(t *testing.T) {
	cases := []struct {
		name  string
		seats string
		want  string // the seat number the refusal must name
	}{
		{"empty native id", "native:gemini-x,native:", "seat 2"},
		{"slash in native id", "native:a/b", "seat 1"},
		{"empty token", "native:gemini-x,,nw/mimo", "seat 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePresetForSave(Preset{Name: "a", Seats: tc.seats})
			if !errors.Is(err, ErrPresetSeats) {
				t.Fatalf("malformed seats %q must be refused with ErrPresetSeats; got %v", tc.seats, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q must name %q", err.Error(), tc.want)
			}
		})
	}

	// Every spelling the run-time parser accepts is accepted here too, and an
	// empty value means auto-select.
	valid := []string{
		"",
		"native:gemini-3.8-flash-high",
		"native:gemini-3.8-flash-high,nw/mimo-v2.6-pro-flex",
		"gemini-3.8-flash-high", // bare token = provider spec (backward compatible)
		"model (nw)",
		"native:x,native:x", // duplicates are a soft warning, never a refusal
	}
	for _, seats := range valid {
		if err := ValidatePresetForSave(Preset{Name: "a", Seats: seats}); err != nil {
			t.Errorf("seats %q must be accepted: %v", seats, err)
		}
	}

	// The store-boundary save still round-trips a malformed lineup: the load side
	// must never dead-end on a hand-edited file, and the editor shows the rows
	// for repair (tolerant load, strict save).
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	broken := &PresetsFile{Presets: []Preset{{Name: "a", Seats: "native:a/b"}}}
	if err := st.SavePresets(broken); err != nil {
		t.Fatalf("SavePresets must keep accepting loadable state: %v", err)
	}
	if err := ValidatePresetForSave(broken.Presets[0]); !errors.Is(err, ErrPresetSeats) {
		t.Errorf("the same broken lineup must be refused for a user-driven save; got %v", err)
	}
}
// --- Editable built-in defaults (design contract A1) -------------------------

// The seed identity is byte-identical to the TUI row [1] label, and the
// predicate that guards it is an EXACT match: only the byte-identical spelling
// is the seed, so a near-miss user name neither hijacks the ladder's seed arm
// nor is refused as a user preset name.
func TestBuiltinPresetNameIdentity(t *testing.T) {
	if BuiltinPresetName != "(built-in defaults)" {
		t.Fatalf("BuiltinPresetName = %q; want the byte-exact TUI row label", BuiltinPresetName)
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"(built-in defaults)", true},
		{"(built-in defaults) ", false}, // trailing space
		{" (built-in defaults)", false}, // leading space
		{"(Built-in defaults)", false},  // case
		{"(built-in defaults)x", false}, // prefix plus suffix
		{"built-in defaults", false},    // bare
		{"", false},
	} {
		if got := IsBuiltinPresetName(tc.name); got != tc.want {
			t.Errorf("IsBuiltinPresetName(%q) = %v; want %v (exact match only)", tc.name, got, tc.want)
		}
	}
}

// A1 store boundary: the user-driven save door refuses ANY record carrying the
// reserved seed name (empty record and seats-pinned record alike), with the
// sentinel and a message that names the rule only, never the operand. The
// store door (SavePresets) deliberately still round-trips the seed record -
// that permissiveness is what the seed arm consumes.
func TestValidatePresetForSaveRefusesReservedSeedName(t *testing.T) {
	for _, p := range []Preset{
		{Name: BuiltinPresetName},
		{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"},
	} {
		err := ValidatePresetForSave(p)
		if !errors.Is(err, ErrPresetNameReserved) {
			t.Errorf("ValidatePresetForSave(%+v) = %v; want ErrPresetNameReserved", p, err)
		}
		if err != nil && strings.Contains(err.Error(), BuiltinPresetName) {
			t.Errorf("the refusal must not echo the reserved name: %q", err.Error())
		}
	}

	// The store door accepts the same bytes: the seed record legitimately lives
	// in the store, and SavePresets stamps its provenance marker like any record.
	path := filepath.Join(t.TempDir(), "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	seed := Preset{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"}
	if err := st.SavePresets(&PresetsFile{Presets: []Preset{seed}}); err != nil {
		t.Fatalf("SavePresets of the seed record: %v", err)
	}
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if len(got.Presets) != 1 || got.Presets[0].Name != BuiltinPresetName {
		t.Fatalf("seed round trip = %+v; want the one seed record", got.Presets)
	}
	if got.Presets[0].SeatsSHA256 != SeatsHash(seed.Seats) || got.Presets[0].SeatsSHA256 == "" {
		t.Errorf("SeatsSHA256 = %q; want the marker SavePresets stamps", got.Presets[0].SeatsSHA256)
	}
}

// The seed path's door accepts the seed record: an all-empty record (every
// field falls through to the engine rung) and a seats-pinned record both
// validate, so the row-[1] editor save can ride the ONE shared rule core.
func TestValidateSeedPresetForSaveAcceptsSeedRecord(t *testing.T) {
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName}); err != nil {
		t.Errorf("the all-empty seed record must validate: %v", err)
	}
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"}); err != nil {
		t.Errorf("a seats-pinned seed record must validate: %v", err)
	}
}

// The seed record is held to exactly the user-writer rules: D13, D14 and
// the seats grammar refuse a seed-named record with the same sentinels, so a
// typo in row [1] cannot persist a seed the engine would reject later.
func TestValidateSeedPresetForSaveAppliesRunRules(t *testing.T) {
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName, Task: "secuirty"}); !errors.Is(err, ErrPresetUnknownTask) {
		t.Errorf("seed D13 unknown task = %v; want ErrPresetUnknownTask", err)
	}
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName, AllowHosts: []string{"*.example.com"}}); !errors.Is(err, ErrPresetAllowHosts) {
		t.Errorf("seed D14 wildcard host = %v; want ErrPresetAllowHosts", err)
	}
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName, Seats: "native:a/b"}); !errors.Is(err, ErrPresetSeats) {
		t.Errorf("seed malformed seats = %v; want ErrPresetSeats", err)
	}
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName, Task: "review", AllowHosts: []string{"example.com"}, Seats: "native:gemini-a"}); err != nil {
		t.Errorf("a fully valid seed record must validate: %v", err)
	}
}
