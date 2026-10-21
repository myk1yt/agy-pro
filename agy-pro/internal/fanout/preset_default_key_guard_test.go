package fanout

// F1 guard pins (P3-1): the store's default_preset key is machine
// configuration the caller never named, so it must never veto a flag the
// caller passed. Two caller subsystems omit flags by design - the swarm
// builder's argument vector is pinned at seven flag pairs
// (internal/swarm/build.go:46-54, internal/swarm/swarm_test.go:21-54) and
// partition mode takes its seat source from the plan file - so before this
// guard a default preset pinning models made every `agy pro swarm` run die at
// the "--seats and --models cannot be combined" gate, a seats/models pin
// killed --units, and a write/exec tools pin killed the partition read-only
// gate.
//
// The guard drops the conflicting PRESET knob with one stderr notice and runs
// on the caller's explicit configuration. An explicitly named preset
// (--preset / EnvFanoutPreset) is the user's own choice and keeps the hard
// error.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardPartitionPlan writes a one-unit partition plan and returns its path.
func guardPartitionPlan(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "guard-plan.json")
	body := `[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha"],"deliverable":"do alpha"}]`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return p
}

// guardEngine replaces the engine seam and returns the captured configs.
func guardEngine(t *testing.T) *[]RunConfig {
	t.Helper()
	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, Root: cfg.Root, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })
	return &got
}

// guardOneNotice asserts stderr carries exactly one notice line containing
// every want substring, and that none of the forbidden substrings appear.
func guardOneNotice(t *testing.T, stderr string, wants []string, forbidden []string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("exactly one notice line expected; got %d in %q", len(lines), stderr)
	}
	for _, w := range wants {
		if !strings.Contains(lines[0], w) {
			t.Errorf("notice %q missing %q", lines[0], w)
		}
	}
	for _, f := range forbidden {
		if strings.Contains(stderr, f) {
			t.Errorf("stderr %q must not contain %q", stderr, f)
		}
	}
}

// (1) A models-pinning default preset cannot break a --seats run: the preset
// pin is dropped with one warning and the run proceeds on the flag seats. The
// swarm builder's exact shape: --seats + --tools + --task + --root + --timeout
// + --prompt + --json.
func TestDefaultKeyModelsPresetCannotBreakSeatsRun(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Models: "prov/m1"}},
		DefaultPreset: "nightly",
	})

	f := ladderFlags(stderr)
	f.seats = "native:gemini-3.8-flash-high,prov/glm"
	f.set["seats"] = true
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("a models-pinning default preset must not break --seats: %v", err)
	}
	if len(cfg.Seats) != 2 || cfg.Seats[0].Kind != SeatKindNative {
		t.Errorf("seats = %+v; want the two parsed flag specs", cfg.Seats)
	}
	if len(cfg.Models) != 0 {
		t.Errorf("models = %v; want empty (the preset pin is dropped)", cfg.Models)
	}
	if cfg.Sources["seats"] != "flag" || cfg.Sources["models"] != "engine" {
		t.Errorf("sources = %+v; want seats:flag models:engine", cfg.Sources)
	}
	guardOneNotice(t, stderr.String(),
		[]string{`default preset "nightly" models="prov/m1"`, "--seats", "dropping it"}, nil)

	// Run level, the swarm argument vector shape: exits 0, the engine sees the
	// flag seats, and the warning stays on stderr while stdout carries the
	// report.
	got := guardEngine(t)
	var out, errw bytes.Buffer
	code := Run([]string{
		"--task", "review",
		"--seats", "native:gemini-3.8-flash-high,prov/glm",
		"--root", ".",
		"--timeout", "300",
		"--tools", "read,search",
		"--prompt", "p",
		"--json",
	}, nil, &out, &errw)
	if code != 0 {
		t.Fatalf("Run exit = %d; stderr = %s", code, errw.String())
	}
	if len(*got) != 1 {
		t.Fatalf("engine calls = %d; want 1", len(*got))
	}
	seatCfg := (*got)[0]
	if len(seatCfg.Seats) != 2 || len(seatCfg.Models) != 0 {
		t.Errorf("engine config seats/models = %+v/%v; want the flag seats and no models", seatCfg.Seats, seatCfg.Models)
	}
	if !seatCfg.JSONOutput {
		t.Error("the --json switch must survive the drop")
	}
	if !strings.Contains(errw.String(), `conflicts with the explicit --seats flag`) {
		t.Errorf("stderr = %q; want the drop notice", errw.String())
	}
	if strings.Contains(out.String(), "conflicts with") {
		t.Errorf("the drop notice must never reach stdout: %q", out.String())
	}
	// --json keeps stdout a parseable summary (the swarm builder consumes it).
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Errorf("--json stdout must stay parseable JSON: %v (%q)", err, out.String())
	}
}

// (2) The same pin cannot break a --units run.
func TestDefaultKeyModelsPresetCannotBreakUnitsRun(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Models: "prov/m1"}},
		DefaultPreset: "nightly",
	})
	plan := guardPartitionPlan(t, home)

	f := ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	cfg, err := buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("a models-pinning default preset must not break --units: %v", err)
	}
	if len(cfg.Units) != 1 || len(cfg.Models) != 0 {
		t.Errorf("units/models = %d/%v; want 1 unit and no models", len(cfg.Units), cfg.Models)
	}
	if cfg.Sources["models"] != "engine" {
		t.Errorf("sources[models] = %q; want engine", cfg.Sources["models"])
	}
	guardOneNotice(t, stderr.String(),
		[]string{`models="prov/m1"`, "--units (partition mode assigns one seat per unit)", "dropping it"}, nil)

	// Run level: exit 0, the engine runs the partition plan.
	got := guardEngine(t)
	var out, errw bytes.Buffer
	if code := Run([]string{"--units", plan}, nil, &out, &errw); code != 0 {
		t.Fatalf("Run exit = %d; stderr = %s", code, errw.String())
	}
	if len(*got) != 1 || len((*got)[0].Units) != 1 {
		t.Fatalf("engine config must carry the partition plan: %+v", *got)
	}
	if !strings.Contains(errw.String(), "dropping it") {
		t.Errorf("stderr = %q; want the drop notice", errw.String())
	}
}

// (3) A tools=write default preset cannot break the partition read-only gate,
// while the explicit --tools flag and the gate itself keep their behavior.
func TestDefaultKeyWriteToolsPresetCannotBreakPartitionReadOnlyGate(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Tools: "read,search,write"}},
		DefaultPreset: "nightly",
	})
	plan := guardPartitionPlan(t, home)

	f := ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	cfg, err := buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("a write-tools default preset must not break --units: %v", err)
	}
	if cfg.Tools != defaultFanoutTools {
		t.Errorf("tools = %q; want the engine default %q (the preset pin is dropped)", cfg.Tools, defaultFanoutTools)
	}
	if cfg.Sources["tools"] != "engine" {
		t.Errorf("sources[tools] = %q; want engine", cfg.Sources["tools"])
	}
	guardOneNotice(t, stderr.String(),
		[]string{`tools="read,search,write"`, "partition mode is read-only", "dropping it"},
		[]string{"--tools write/exec"})

	// Boundary 1: an explicit --tools write/exec with --units still errors
	// (partition_test.go:311-320 pins the same gate from the flag side).
	var out, errw bytes.Buffer
	if code := Run([]string{"--units", plan, "--tools", "read,write"}, nil, &out, &errw); code != 2 {
		t.Errorf("explicit --tools read,write with --units exit = %d; want 2", code)
	}
	if !strings.Contains(errw.String(), "read-only by default") {
		t.Errorf("stderr = %q; want the read-only gate error", errw.String())
	}

	// Boundary 2: an explicit --tools flag already wins over the preset, so no
	// drop notice is emitted at all.
	stderr2 := &bytes.Buffer{}
	f2 := ladderFlags(stderr2)
	f2.units = plan
	f2.set["units"] = true
	f2.tools = "read"
	f2.set["tools"] = true
	if _, err := buildRunConfig(f2, ""); err != nil {
		t.Fatalf("explicit --tools read with --units: %v", err)
	}
	if stderr2.Len() != 0 {
		t.Errorf("no notice expected when the flag owns the field; stderr = %q", stderr2.String())
	}
}

// (4) An explicitly named preset keeps the honest hard error: --preset and the
// env selector are the user's own choice, so their conflict is never silently
// resolved.
func TestExplicitlySelectedPresetKeepsTheHardError(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Models: "prov/m1"}},
		DefaultPreset: "nightly",
	})

	// --preset by name.
	f := ladderFlags(stderr)
	f.preset = "nightly"
	f.seats = "native:gemini-x"
	f.set["seats"] = true
	if _, err := buildRunConfig(f, "p"); err == nil ||
		err.Error() != "--seats and --models cannot be combined" {
		t.Errorf("--preset nightly + --seats error = %v; want the hard error", err)
	}

	// Same through Run(): exit 2, no engine call.
	got := guardEngine(t)
	var out, errw bytes.Buffer
	if code := Run([]string{"--prompt", "p", "--preset", "nightly", "--seats", "native:gemini-x"}, nil, &out, &errw); code != 2 {
		t.Errorf("Run exit = %d; want 2", code)
	}
	if !strings.Contains(errw.String(), "--seats and --models cannot be combined") {
		t.Errorf("stderr = %q; want the hard error", errw.String())
	}
	if len(*got) != 0 {
		t.Error("a named-preset conflict must not reach the engine")
	}

	// The env selector is a named choice too.
	t.Setenv(EnvFanoutPreset, "nightly")
	stderr.Reset()
	f = ladderFlags(stderr)
	f.seats = "native:gemini-x"
	f.set["seats"] = true
	if _, err := buildRunConfig(f, "p"); err == nil ||
		err.Error() != "--seats and --models cannot be combined" {
		t.Errorf("env-selected preset + --seats error = %v; want the hard error", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("a named-preset conflict must not warn-and-continue: %q", stderr.String())
	}

	// A named preset whose pin does NOT conflict still applies.
	t.Setenv(EnvFanoutPreset, "")
	stderr.Reset()
	f = ladderFlags(stderr)
	f.preset = "nightly"
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0] != "prov/m1" || stderr.Len() != 0 {
		t.Errorf("named preset models = %v, stderr = %q; want [prov/m1] and no notice", cfg.Models, stderr.String())
	}
}

// (5) The dangling-default behavior is unchanged: one byte-pinned warning, no
// drop notice, engine rungs below.
func TestDefaultKeyDanglingKeepsItsSingleWarning(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", Models: "prov/m1"}},
		DefaultPreset: "ghost",
	})

	f := ladderFlags(stderr)
	f.seats = "native:gemini-x"
	f.set["seats"] = true
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("dangling default key must not error: %v", err)
	}
	if len(cfg.Seats) != 1 || len(cfg.Models) != 0 {
		t.Errorf("seats/models = %+v/%v; want the flag seat and no models", cfg.Seats, cfg.Models)
	}
	want := "fanout: warning: default preset \"ghost\" not found in fanout-presets.json; running without a preset\n"
	if stderr.String() != want {
		t.Errorf("dangling-default warning drifted\n got: %q\nwant: %q", stderr.String(), want)
	}
}

// Regression guard: with no conflicting flag the default-key preset still
// supplies its knob, silently.
func TestDefaultKeyPresetStillAppliesWithoutAConflict(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Models: "prov/m1,prov/m2"}},
		DefaultPreset: "nightly",
	})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 2 || cfg.Models[1] != "prov/m2" {
		t.Errorf("models = %v; want the preset list [prov/m1 prov/m2]", cfg.Models)
	}
	if cfg.Sources["models"] != "preset" {
		t.Errorf("sources[models] = %q; want preset", cfg.Sources["models"])
	}
	if stderr.Len() != 0 {
		t.Errorf("no notice expected without a conflict; stderr = %q", stderr.String())
	}
}

// The seat-source pair from both directions, plus the two cases the guard
// deliberately does NOT resolve: a preset that contradicts itself.
func TestDefaultKeySeatSourceDropsBothDirections(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a"}},
		DefaultPreset: "nightly",
	})

	// preset seats + explicit --models: the preset seats pin goes.
	f := ladderFlags(stderr)
	f.models = "prov/m1"
	f.set["models"] = true
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("a seats-pinning default preset must not break --models: %v", err)
	}
	if len(cfg.Models) != 1 || len(cfg.Seats) != 0 {
		t.Errorf("models/seats = %v/%+v; want the flag model and no seats", cfg.Models, cfg.Seats)
	}
	guardOneNotice(t, stderr.String(),
		[]string{`seats="native:gemini-a"`, "the explicit --models flag", "dropping it"}, nil)

	// --units drops the same pin.
	plan := guardPartitionPlan(t, home)
	stderr.Reset()
	f = ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	cfg, err = buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("a seats-pinning default preset must not break --units: %v", err)
	}
	if len(cfg.Units) != 1 || len(cfg.Seats) != 0 {
		t.Errorf("units/seats = %d/%+v; want 1 unit and no seats", len(cfg.Units), cfg.Seats)
	}
	guardOneNotice(t, stderr.String(),
		[]string{`seats="native:gemini-a"`, "--units (partition mode assigns one seat per unit)"}, nil)

	// A default preset pinning BOTH with an explicit --seats: the models half is
	// the only conflict (--seats already outranks the preset seats), so the run
	// proceeds.
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a", Models: "prov/m1"}},
		DefaultPreset: "nightly",
	})
	stderr.Reset()
	f = ladderFlags(stderr)
	f.seats = "native:gemini-b"
	f.set["seats"] = true
	if _, err := buildRunConfig(f, "p"); err != nil {
		t.Fatalf("--seats must survive a doubly-pinning default preset: %v", err)
	}
	guardOneNotice(t, stderr.String(),
		[]string{`models="prov/m1"`, "the explicit --seats flag"}, []string{`seats=`})

	// The guard does NOT silently pick for a preset that contradicts itself with
	// no explicit flag: that config has no caller intent to honor, so the honest
	// error stays.
	stderr.Reset()
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err == nil ||
		err.Error() != "--seats and --models cannot be combined" {
		t.Errorf("self-conflicting default preset error = %v; want the hard error", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("a self-conflicting preset must not warn-and-continue: %q", stderr.String())
	}
}
