package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// ladderEnv isolates the whole resolution environment: a private AGY_PRO_HOME
// (preset store), a providers.json path that does not exist (no defaults
// rung), and an empty AGY_PRO_FANOUT_PRESET. Without this isolation the
// machine's real ~/.agy-pro/providers.json defaults block (the live file
// carries timeoutSeconds 120) would leak into every ladder assertion.
func ladderEnv(t *testing.T) (home string, stderr *bytes.Buffer) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv("AGY_PRO_PROVIDERS", filepath.Join(home, "no-such-providers.json"))
	t.Setenv(EnvFanoutPreset, "")
	return home, &bytes.Buffer{}
}

func ladderFlags(stderr *bytes.Buffer) fanoutFlags {
	return fanoutFlags{set: map[string]bool{}, errw: stderr}
}

func savePresetsFile(t *testing.T, home string, pf *PresetsFile) {
	t.Helper()
	st := &PresetFileStore{Path: filepath.Join(home, "fanout-presets.json")}
	if err := st.SavePresets(pf); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
}

func writeDefaults(t *testing.T, defaultsJSON string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	full := `{"providers":[]`
	if defaultsJSON != "" {
		full += `,"defaults":` + defaultsJSON
	}
	full += `}`
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_PRO_PROVIDERS", path)
}

// P8 behavior pin: with no preset file and no defaults values, the ladder
// resolves to exactly the pre-P8 flag defaults.
func TestLadderNoPresetNoDefaultsMatchesEngineDefaults(t *testing.T) {
	_, stderr := ladderEnv(t)
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 3 || cfg.TimeoutSec != 300 || cfg.Tools != "read,search" || cfg.MaxTurns != 10 {
		t.Errorf("engine rung = N:%d timeout:%d tools:%q maxTurns:%d; want 3/300/read,search/10",
			cfg.N, cfg.TimeoutSec, cfg.Tools, cfg.MaxTurns)
	}
	if cfg.Task != "review" || cfg.Root != "." || cfg.OutDir != "" {
		t.Errorf("engine rung surface = task:%q root:%q out:%q; want review/.//empty", cfg.Task, cfg.Root, cfg.OutDir)
	}
	if len(cfg.Models) != 0 || len(cfg.Seats) != 0 || len(cfg.AllowHosts) != 0 {
		t.Errorf("engine rung must leave models/seats/allowHosts empty: %+v", cfg)
	}
	if cfg.JSONOutput {
		t.Error("engine rung must leave JSONOutput false")
	}
	if stderr.Len() != 0 {
		t.Errorf("no notices expected without a defaults block; stderr = %q", stderr.String())
	}
}

// Ladder order for one field end to end: flag > preset > defaults > engine.
func TestLadderOrderFlagBeatsPresetBeatsDefaultsBeatsEngine(t *testing.T) {
	home, stderr := ladderEnv(t)
	writeDefaults(t, `{"n":7}`)

	// Defaults rung (no preset): stored 7 wins over engine 3, with a notice
	// because 7 != 3.
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 7 {
		t.Errorf("defaults rung N = %d; want 7", cfg.N)
	}
	if cfg.Sources["n"] != "defaults" {
		t.Errorf("sources[n] = %q; want defaults", cfg.Sources["n"])
	}
	if !strings.Contains(stderr.String(), "n=7") || !strings.Contains(stderr.String(), "engine default 3") {
		t.Errorf("defaults drift notice missing: %q", stderr.String())
	}

	// Preset rung beats the defaults rung and silences its notice. The store
	// default key makes "a" the active preset without any flag or env.
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "a", N: 5}}, DefaultPreset: "a"})
	stderr.Reset()
	cfg, err = buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 5 {
		t.Errorf("preset rung N = %d; want 5 (preset beats defaults 7)", cfg.N)
	}
	if cfg.Sources["n"] != "preset" {
		t.Errorf("sources[n] = %q; want preset", cfg.Sources["n"])
	}
	if strings.Contains(stderr.String(), "n=") {
		t.Errorf("no defaults notice expected when the preset supplies the field: %q", stderr.String())
	}

	// Explicit flag beats the preset.
	f := ladderFlags(stderr)
	f.n = 9
	f.set["n"] = true
	cfg, err = buildRunConfig(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 9 {
		t.Errorf("flag rung N = %d; want 9 (flag beats preset 5)", cfg.N)
	}
	if cfg.Sources["n"] != "flag" {
		t.Errorf("sources[n] = %q; want flag", cfg.Sources["n"])
	}

	// Defaults value equal to the engine default: consumed silently, no
	// notice (nothing drifted). The preset file is emptied first so the
	// defaults rung actually supplies the field.
	st := &PresetFileStore{Path: filepath.Join(home, "fanout-presets.json")}
	if err := st.SavePresets(&PresetsFile{}); err != nil {
		t.Fatal(err)
	}
	writeDefaults(t, `{"n":3}`)
	stderr.Reset()
	cfg, err = buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 3 || cfg.Sources["n"] != "defaults" || stderr.Len() != 0 {
		t.Errorf("defaults n=3 (== engine) = N:%d rung:%q stderr:%q; want 3/defaults/no notice",
			cfg.N, cfg.Sources["n"], stderr.String())
	}
}

// Preset selection: --preset flag > AGY_PRO_FANOUT_PRESET env > the store's
// default_preset key.
func TestLadderPresetSelectionFlagEnvDefaultKey(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}, {Name: "other", N: 5}},
		DefaultPreset: "main",
	})

	// Store default key only: main (N 4).
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 4 {
		t.Errorf("default-key selection N = %d; want 4 (preset main)", cfg.N)
	}

	// Env beats the store default key: other (N 5).
	t.Setenv(EnvFanoutPreset, "other")
	cfg, err = buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 5 {
		t.Errorf("env selection N = %d; want 5 (preset other beats default key main)", cfg.N)
	}

	// Flag beats the env.
	f := ladderFlags(stderr)
	f.preset = "main"
	cfg, err = buildRunConfig(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 4 {
		t.Errorf("flag selection N = %d; want 4 (--preset main beats env other)", cfg.N)
	}
}

// Unknown preset names are honest errors, never a silent fallback to another
// preset; a dangling default_preset key only warns and runs preset-less.
func TestLadderUnknownPresetBehavior(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}},
		DefaultPreset: "main",
	})

	// --preset naming a missing preset: usage error, no fallback to main.
	f := ladderFlags(stderr)
	f.preset = "ghost"
	if _, err := buildRunConfig(f, "p"); err == nil || !strings.Contains(err.Error(), `preset "ghost" not found`) {
		t.Errorf("--preset ghost error = %v; want a not-found usage error", err)
	}

	// Same for the env selector.
	t.Setenv(EnvFanoutPreset, "ghost")
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err == nil || !strings.Contains(err.Error(), `preset "ghost" not found`) {
		t.Errorf("env ghost error = %v; want a not-found usage error", err)
	}

	// A dangling default_preset key: warning line, no error, engine default.
	t.Setenv(EnvFanoutPreset, "")
	st := &PresetFileStore{Path: filepath.Join(home, "fanout-presets.json")}
	if err := st.SavePresets(&PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}},
		DefaultPreset: "ghost",
	}); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("dangling default key must not error: %v", err)
	}
	if cfg.N != 3 {
		t.Errorf("dangling default key N = %d; want engine default 3, no wrong-preset fallback", cfg.N)
	}
	if !strings.Contains(stderr.String(), "default preset \"ghost\" not found") {
		t.Errorf("dangling default key must warn: %q", stderr.String())
	}
}

// Corrupt preset file: one warning line, empty preset set, run continues on
// the lower rungs.
func TestLadderCorruptPresetFileFallsBack(t *testing.T) {
	home, stderr := ladderEnv(t)
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("corrupt preset file must not fail the run: %v", err)
	}
	if cfg.N != 3 {
		t.Errorf("N = %d; want engine default 3 after the corrupt fallback", cfg.N)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "corrupt") {
		t.Errorf("exactly one corrupt warning line expected; got %q", stderr.String())
	}
}

// The defaults block consumption (D4): all four consumed fields land in the
// config, the drift notice names field, stored value and engine default, and
// the sources map records the rung per field.
func TestLadderDefaultsConsumptionWithDriftNotice(t *testing.T) {
	_, stderr := ladderEnv(t)
	writeDefaults(t, `{"n":5,"timeoutSeconds":120,"maxTurns":8,"tools":"read"}`)

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 5 || cfg.TimeoutSec != 120 || cfg.MaxTurns != 8 || cfg.Tools != "read" {
		t.Errorf("defaults consumption = N:%d timeout:%d maxTurns:%d tools:%q; want 5/120/8/read",
			cfg.N, cfg.TimeoutSec, cfg.MaxTurns, cfg.Tools)
	}
	if cfg.Sources["n"] != "defaults" || cfg.Sources["timeout"] != "defaults" ||
		cfg.Sources["max_turns"] != "defaults" || cfg.Sources["tools"] != "defaults" {
		t.Errorf("sources rungs = %+v; want defaults for n/timeout/max_turns/tools", cfg.Sources)
	}

	errOut := stderr.String()
	// timeoutSeconds 120 vs engine 300 is the live-file drift: the stored
	// value wins and the notice says why, one line per drifted field.
	for _, want := range []string{"timeout_seconds=120", "engine default 300", "n=5", "max_turns=8", "tools=read"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("defaults notice missing %q; stderr = %q", want, errOut)
		}
	}
	if strings.Count(errOut, "fanout: using providers.json default") != 4 {
		t.Errorf("want exactly 4 drift notice lines (n, timeout, max_turns, tools all differ); got %q", errOut)
	}
}

// A preset timeout is an intentional budget: honored exactly, and a partition
// run does NOT scale it (B7 scaling applies only to the engine-default rung).
func TestLadderTimeoutRungInteractionWithUnits(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "a", TimeoutSeconds: 600}}, DefaultPreset: "a"})

	plan := `[{"id":"u1","title":"t","scope":["src"],"deliverable":"d"}]`
	planPath := filepath.Join(home, "plan.json")
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}

	f := ladderFlags(stderr)
	f.units = planPath
	f.set["units"] = true
	cfg, err := buildRunConfig(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TimeoutSec != 600 {
		t.Errorf("preset timeout with --units = %d; want 600 (configured budgets are honored exactly)", cfg.TimeoutSec)
	}

	// Engine-default rung keeps the B7 behavior: TimeoutSec 0, the engine
	// scales it per unit.
	st := &PresetFileStore{Path: filepath.Join(home, "fanout-presets.json")}
	if err := st.SavePresets(&PresetsFile{}); err != nil {
		t.Fatal(err)
	}
	cfg, err = buildRunConfig(f, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TimeoutSec != 0 {
		t.Errorf("engine-rung timeout with --units = %d; want 0 (B7 engine scaling)", cfg.TimeoutSec)
	}
}

// max-native: the preset sits between the flag and the state.json fallback.
func TestLadderMaxNativePresetBetweenFlagAndState(t *testing.T) {
	home, stderr := ladderEnv(t)
	if err := os.WriteFile(filepath.Join(home, "state.json"),
		[]byte(`{"current_mode":"full","native_seat_concurrency":6}`), 0o600); err != nil {
		t.Fatal(err)
	}
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "a", MaxNative: 4}}, DefaultPreset: "a"})

	// Preset beats the stored state value.
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxNativeConcurrency != 4 {
		t.Errorf("preset maxNative = %d; want 4 (preset beats state.json 6)", cfg.MaxNativeConcurrency)
	}
	if cfg.Sources["max_native"] != "preset" {
		t.Errorf("sources[max_native] = %q; want preset", cfg.Sources["max_native"])
	}

	// The flag beats the preset and clamps.
	f := ladderFlags(stderr)
	f.maxNative = 9
	f.set["max-native"] = true
	cfg, err = buildRunConfig(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxNativeConcurrency != 8 {
		t.Errorf("flag maxNative 9 = %d; want 8 (clamped, flag beats preset)", cfg.MaxNativeConcurrency)
	}
}

// Preset-sourced list/source fields land in the RunConfig (normalized for
// allow-hosts, trimmed for the search source).
func TestLadderPresetAllowHostsAndSearchSource(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{
		Name:           "a",
		AllowHosts:     []string{"Example.COM:8080"},
		SearchEndpoint: " https://search.example/{query} ",
		SearchKeyEnv:   "P8_SEARCH_KEY_NAME",
	}}, DefaultPreset: "a"})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowHosts) != 1 || cfg.AllowHosts[0] != "example.com" {
		t.Errorf("preset allowHosts = %v; want [example.com] (normalized)", cfg.AllowHosts)
	}
	if cfg.SearchSource.Endpoint != "https://search.example/{query}" || cfg.SearchSource.KeyEnv != "P8_SEARCH_KEY_NAME" {
		t.Errorf("preset search source = %+v; want the trimmed endpoint + env NAME", cfg.SearchSource)
	}
	if cfg.Sources["allow_hosts"] != "preset" || cfg.Sources["search"] != "preset" {
		t.Errorf("sources = %+v; want preset for allow_hosts and search", cfg.Sources)
	}

	// An explicit flag list overrides the preset list entirely.
	f := ladderFlags(stderr)
	f.allowHosts = []string{"flag.example.org"}
	f.set["allow-host"] = true
	cfg, err = buildRunConfig(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowHosts) != 1 || cfg.AllowHosts[0] != "flag.example.org" {
		t.Errorf("flag allowHosts = %v; want [flag.example.org] (flag wins over preset)", cfg.AllowHosts)
	}
}

// The preset json field resolves the output switch; an explicit --json flag
// (even --json=false) stays authoritative.
func TestLadderPresetJSONSwitch(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "a", JSON: true}}, DefaultPreset: "a"})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.JSONOutput {
		t.Error("preset json:true must resolve JSONOutput")
	}
	if cfg.Sources["json"] != "preset" {
		t.Errorf("sources[json] = %q; want preset", cfg.Sources["json"])
	}

	f := ladderFlags(stderr)
	f.jsonVal = false
	f.set["json"] = true
	cfg, err = buildRunConfig(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JSONOutput {
		t.Error("explicit --json=false must beat the preset json:true")
	}
}

// Legacy direct callers (nil set) skip the ladder: raw values stay
// authoritative even with a preset file and a defaults block present.
func TestLadderNilSetSkipsLadder(t *testing.T) {
	home, _ := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "a", N: 5}}})
	writeDefaults(t, `{"n":7}`)

	cfg, err := buildRunConfig(fanoutFlags{n: 4, timeout: 90, tools: "read", maxTurns: 2, task: "security", root: "/ws", out: "/tmp/out"}, "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.N != 4 || cfg.TimeoutSec != 90 || cfg.Tools != "read" || cfg.MaxTurns != 2 ||
		cfg.Task != "security" || cfg.Root != "/ws" || cfg.OutDir != "/tmp/out" {
		t.Errorf("legacy path must use raw values: %+v", cfg)
	}
	if cfg.Sources != nil {
		t.Errorf("legacy path must not record sources: %+v", cfg.Sources)
	}
}

func TestInterestingSourcesFiltersExpectedRungs(t *testing.T) {
	got := interestingSources(map[string]string{
		"n": "flag", "task": "engine", "timeout": "preset", "tools": "defaults",
		"max_native": "state", "root": "",
	})
	if len(got) != 2 || got["timeout"] != "preset" || got["tools"] != "defaults" {
		t.Errorf("interestingSources = %+v; want only preset/defaults rungs", got)
	}
	if got["max_native"] != "" {
		t.Errorf("state rung must stay invisible even as the only non-engine entry: %+v", got)
	}
	if interestingSources(map[string]string{"n": "flag", "task": "engine"}) != nil {
		t.Error("an all-flag/engine map must stay nil (summary byte-identical)")
	}
	if interestingSources(map[string]string{"max_native": "state"}) != nil {
		t.Error("a lone state rung must stay nil (summary byte-identical)")
	}
	if interestingSources(nil) != nil {
		t.Error("nil in must stay nil")
	}
}

// F-06 pin: an unconfigured run (no preset file, no defaults values, no
// relevant flags) resolves max_native through the state.json fallback rung,
// which must not surface in the machine summary; interestingSources of the
// full unconfigured rung map stays nil, so summary.json keeps its pre-P8
// bytes (no sources key).
func TestInterestingSourcesUnconfiguredRunStaysNil(t *testing.T) {
	_, stderr := ladderEnv(t)
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources["max_native"] != "state" {
		t.Fatalf("unconfigured run must resolve max_native from the state rung: %+v", cfg.Sources)
	}
	if got := interestingSources(cfg.Sources); got != nil {
		t.Errorf("unconfigured run must produce no sources key: got %+v; want nil", got)
	}
}

// F-06 pin: a configured run surfaces exactly the rungs that supplied a value.
// The preset supplies n, the defaults block supplies timeout; every other
// field resolves to a flag/engine/state rung and stays invisible.
func TestInterestingSourcesConfiguredRunShowsSupplyingRungs(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "a", N: 5}},
		DefaultPreset: "a",
	})
	writeDefaults(t, `{"timeoutSeconds":120}`)

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	got := interestingSources(cfg.Sources)
	if len(got) != 2 || got["n"] != "preset" || got["timeout"] != "defaults" {
		t.Errorf("configured run sources = %+v; want exactly n:preset timeout:defaults", got)
	}
}

// Run()-level wiring: --preset / env selection reaches the engine config, the
// flag beats the env, an unknown preset exits 2, the banner names the preset
// rung, and a preset json field switches the output to the JSON summary.
func TestRunLevelPresetSelection(t *testing.T) {
	home, _ := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets: []Preset{
			{Name: "nightly", Task: "security", N: 5},
			{Name: "other", N: 2, JSON: true},
		},
		DefaultPreset: "nightly",
	})

	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, Root: cfg.Root, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	run := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errw bytes.Buffer
		full := append([]string{"-prompt", "p"}, args...)
		code := Run(full, nil, &out, &errw)
		return code, out.String(), errw.String()
	}

	// Store default key applies with no flags at all; the banner names the
	// preset rung for the task.
	code, out, errw := run()
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %s", code, errw)
	}
	cfg := got[len(got)-1]
	if cfg.Task != "security" || cfg.N != 5 {
		t.Errorf("default-key preset config = task:%q n:%d; want security/5", cfg.Task, cfg.N)
	}
	if !strings.Contains(out, "Task:                  security (preset)") {
		t.Errorf("banner must name the preset rung for task: %q", out)
	}

	// The env selector beats the default key; the other preset pins json:true,
	// so stdout becomes the machine summary.
	t.Setenv(EnvFanoutPreset, "other")
	code, out, errw = run()
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %s", code, errw)
	}
	cfg = got[len(got)-1]
	if cfg.N != 2 || !cfg.JSONOutput {
		t.Errorf("env-selected preset config = n:%d json:%v; want 2/true", cfg.N, cfg.JSONOutput)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Errorf("preset json:true must switch stdout to the JSON summary: %v (%q)", err, out)
	}

	// The --preset flag beats the env, an explicit flag beats the preset.
	code, out, errw = run("-preset", "nightly", "-n", "7")
	if code != 0 {
		t.Fatalf("Run exit = %d, stderr = %s", code, errw)
	}
	cfg = got[len(got)-1]
	if cfg.N != 7 {
		t.Errorf("--preset nightly + -n 7 = N:%d; want 7 (flag beats preset)", cfg.N)
	}
	if cfg.Task != "security" || !strings.Contains(out, "Task:                  security (preset)") {
		t.Errorf("task must come from the flag-selected preset: task:%q out:%q", cfg.Task, out)
	}

	// Unknown preset: honest usage error, exit 2, no engine call.
	before := len(got)
	code, out, errw = run("-preset", "ghost")
	if code != 2 {
		t.Errorf("unknown preset exit = %d; want 2", code)
	}
	if !strings.Contains(errw, `preset "ghost" not found`) {
		t.Errorf("unknown preset stderr = %q; want the not-found error", errw)
	}
	if len(got) != before {
		t.Error("an unknown preset must not reach the engine")
	}
}

// F5 pin: a credential-shaped preset name (the --preset flag, the
// EnvFanoutPreset selector, or a hand-edited default_preset key) is masked in
// every ladder echo: the not-found usage error and the dangling-default
// warning. The value is an opaque 40-rune mixed-case run with no separators,
// the operandLooksSecret contiguous-run shape that no flag seed used to cover.
func TestLadderPresetOperandMaskedEverywhere(t *testing.T) {
	home, stderr := ladderEnv(t)
	const planted = testfixtures.AWSSecretNoSlash
	const maskedHead = "wJalr****"

	// buildRunConfig level: the not-found error masks the flag-selected name.
	f := ladderFlags(stderr)
	f.preset = planted
	if _, err := buildRunConfig(f, "p"); err == nil ||
		strings.Contains(err.Error(), planted) ||
		!strings.Contains(err.Error(), maskedHead) {
		t.Errorf("--preset secret operand error = %v; want a masked not-found error", err)
	}

	// Run level: the usage-error seed list takes --preset and the env
	// selector, so the composite stderr cannot echo the raw name.
	run := func(args ...string) (int, string) {
		t.Helper()
		var out, errw bytes.Buffer
		code := Run(append([]string{"-prompt", "p"}, args...), nil, &out, &errw)
		return code, errw.String()
	}
	code, errw := run("-preset", planted)
	if code != 2 {
		t.Errorf("--preset secret operand exit = %d; want 2", code)
	}
	if strings.Contains(errw, planted) || !strings.Contains(errw, maskedHead) {
		t.Errorf("--preset secret operand stderr = %q; want the masked name", errw)
	}

	// The env selector is seeded the same way.
	t.Setenv(EnvFanoutPreset, planted)
	code, errw = run()
	if code != 2 {
		t.Errorf("env-selected secret operand exit = %d; want 2", code)
	}
	if strings.Contains(errw, planted) || !strings.Contains(errw, maskedHead) {
		t.Errorf("env-selected secret operand stderr = %q; want the masked name", errw)
	}
	t.Setenv(EnvFanoutPreset, "")

	// A hand-edited default_preset carrying the credential shape: warning
	// only, masked, and the run continues preset-less on the engine rung.
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}},
		DefaultPreset: planted,
	})
	stderr.Reset()
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("dangling secret-shaped default key must warn, not error: %v", err)
	}
	if cfg.N != 3 {
		t.Errorf("dangling secret-shaped default key N = %d; want engine default 3", cfg.N)
	}
	if strings.Contains(stderr.String(), planted) || !strings.Contains(stderr.String(), maskedHead) {
		t.Errorf("dangling default key warning = %q; want the masked name", stderr.String())
	}
}

// Round-6 M1 pin: a userinfo-shaped preset name (a URL carrying a credential
// in the userinfo slot) is masked in every reachable echo. MaskOperand's
// "://" location exemption used to render such a name raw in the not-found
// error and the dangling-default warning; a preset name is never a legitimate
// location, so both slots route through providers.MaskSlot, the registry-slot
// display authority, which classifies the stripped core, the "://" segments,
// and the policy's decorated classes. The planted value is
// the package's own AWS-secret fixture wearing a URL userinfo costume.
func TestLadderPresetUserinfoOperandMasked(t *testing.T) {
	home, stderr := ladderEnv(t)
	const planted = "https://" + testfixtures.AWSSecretExample + "@gateway.example/v1"
	const maskedHead = "wJalr****"

	// buildRunConfig level: the not-found error masks the flag-selected name.
	f := ladderFlags(stderr)
	f.preset = planted
	if _, err := buildRunConfig(f, "p"); err == nil ||
		strings.Contains(err.Error(), planted) ||
		!strings.Contains(err.Error(), maskedHead) {
		t.Errorf("--preset userinfo operand error = %v; want a masked not-found error", err)
	}

	// Run level: the composite usage-error line cannot echo the raw name
	// either (the seed list skips location-shaped operands, so the mask must
	// come from the echo site itself).
	run := func(args ...string) (int, string) {
		t.Helper()
		var out, errw bytes.Buffer
		code := Run(append([]string{"-prompt", "p"}, args...), nil, &out, &errw)
		return code, errw.String()
	}
	code, errw := run("-preset", planted)
	if code != 2 {
		t.Errorf("--preset userinfo operand exit = %d; want 2", code)
	}
	if strings.Contains(errw, planted) || !strings.Contains(errw, maskedHead) {
		t.Errorf("--preset userinfo operand stderr = %q; want the masked name", errw)
	}

	// The env selector hits the same echo.
	t.Setenv(EnvFanoutPreset, planted)
	code, errw = run()
	if code != 2 {
		t.Errorf("env userinfo operand exit = %d; want 2", code)
	}
	if strings.Contains(errw, planted) || !strings.Contains(errw, maskedHead) {
		t.Errorf("env userinfo operand stderr = %q; want the masked name", errw)
	}
	t.Setenv(EnvFanoutPreset, "")

	// A hand-edited default_preset carrying the userinfo credential shape:
	// warning only, masked, run continues preset-less.
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}},
		DefaultPreset: planted,
	})
	stderr.Reset()
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("dangling userinfo default key must warn, not error: %v", err)
	}
	if cfg.N != 3 {
		t.Errorf("dangling userinfo default key N = %d; want engine default 3", cfg.N)
	}
	if strings.Contains(stderr.String(), planted) || !strings.Contains(stderr.String(), maskedHead) {
		t.Errorf("dangling userinfo default key warning = %q; want the masked name", stderr.String())
	}
}

// Census O5 pin: the two preset echoes in loadActivePreset (the dangling
// default_preset warning and the flag/env not-found error) render through
// providers.MaskSlot, the single registry-slot display authority, instead of the
// pre-policy providers.MaskRegistryValue. The plant is the discriminating shape:
// MaskRegistryValue is the IDENTITY on a look-alike-decorated value (its core
// classifiers predate the policy fold, so the decoration breaks every run they
// read), which is how the credential core rendered raw in both echoes. The
// benign corpus holds both echoes byte-identically.
func TestLadderPresetEchoesRouteThroughSlotAuthority(t *testing.T) {
	home, stderr := ladderEnv(t)
	const core = "ab3def7gh9jk1mnpqr5t"
	const masked = "ab3de****"
	plant := "\u2025/" + core

	// Premise: the stale authority returns the plant unchanged and the slot
	// authority masks it, so a reverted call site turns every block below red.
	if got := providers.MaskRegistryValue(plant); got != plant {
		t.Fatalf("premise broken: MaskRegistryValue(%q) = %q; want identity", plant, got)
	}
	if got := providers.MaskSlot(plant); got != masked {
		t.Fatalf("premise broken: MaskSlot(%q) = %q; want %q", plant, got, masked)
	}

	// Dangling default_preset: warning only, masked, run continues preset-less.
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}},
		DefaultPreset: plant,
	})
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("dangling decorated default key must warn, not error: %v", err)
	}
	if cfg.N != 3 {
		t.Errorf("dangling decorated default key N = %d; want engine default 3", cfg.N)
	}
	if strings.Contains(stderr.String(), core) || strings.Contains(stderr.String(), plant) {
		t.Errorf("dangling default key warning leaks the credential: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), masked) {
		t.Errorf("dangling default key warning = %q; want the masked name %q", stderr.String(), masked)
	}

	// Flag-selected name: the not-found error masks the same operand.
	f := ladderFlags(stderr)
	f.preset = plant
	if _, err := buildRunConfig(f, "p"); err == nil ||
		strings.Contains(err.Error(), plant) || strings.Contains(err.Error(), core) ||
		!strings.Contains(err.Error(), masked) {
		t.Errorf("--preset decorated operand error = %v; want the masked name %q", err, masked)
	}

	// Benign corpus: both echoes keep the name byte-identically.
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", N: 4}},
		DefaultPreset: "nightly",
	})
	stderr.Reset()
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatalf("dangling benign default key must warn, not error: %v", err)
	}
	wantWarn := "fanout: warning: default preset \"nightly\" not found in fanout-presets.json; running without a preset\n"
	if stderr.String() != wantWarn {
		t.Errorf("benign default-key warning drifted\n got: %q\nwant: %q", stderr.String(), wantWarn)
	}
	f = ladderFlags(stderr)
	f.preset = "nightly"
	if _, err := buildRunConfig(f, "p"); err == nil {
		t.Fatal("benign unknown --preset must fail")
	} else if want := "preset \"nightly\" not found in fanout-presets.json"; err.Error() != want {
		t.Errorf("benign --preset error drifted\n got: %q\nwant: %q", err.Error(), want)
	}
}
