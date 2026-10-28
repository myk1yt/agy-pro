package fanout

// F1 guard pins (P3-1): the store's default_preset key is machine
// configuration the caller never named, so it must never veto a flag the
// caller passed. Two caller subsystems omit flags by design - the swarm
// builder's argument vector is pinned at seven flag pairs
// (internal/swarm/build.go:46-54, internal/swarm/swarm_test.go:21-54) and
// partition mode takes its seat source from the plan file - so before this
// guard a default preset pinning seats killed --units, and a write/exec tools
// pin killed the partition read-only gate.
//
// The retired preset `models` field is gone (seats is the only seat source), so
// the seat-source half of the guard has one direction left: a seats pin facing
// an explicit --units run.
//
// The guard drops the conflicting PRESET knob with one stderr notice and runs
// on the caller's explicit configuration. An explicitly named preset
// (--preset / EnvFanoutPreset) is the user's own choice and keeps the hard
// error.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardPartitionPlan writes a one-unit partition plan and returns its path.
func guardPartitionPlan(t *testing.T, dir string) string {
	t.Helper()
	plan := filepath.Join(dir, "guard-plan.json")
	body := `[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha"],"deliverable":"do alpha"}]`
	if err := os.WriteFile(plan, []byte(body), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return plan
}

// guardEngine swaps executeEngine for a recorder and returns the captured
// configs.
func guardEngine(t *testing.T) *[]RunConfig {
	t.Helper()
	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task}, nil
	}
	t.Cleanup(func() { executeEngine = old })
	return &got
}

// guardOneNotice asserts stderr is exactly one notice line carrying every
// wanted clause and none of the forbidden ones.
func guardOneNotice(t *testing.T, stderr string, wants, forbidden []string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one notice line; got %d: %q", len(lines), stderr)
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

// (1) A tools=write default preset cannot break the partition read-only gate,
// because that is the one value-shaped conflict a record can still carry.
func TestDefaultKeyWriteToolsPresetCannotBreakPartitionReadOnlyGate(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Tools: "read,write"}},
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
	if len(cfg.Units) != 1 {
		t.Errorf("units = %d; want the plan's one unit", len(cfg.Units))
	}
	guardOneNotice(t, stderr.String(),
		[]string{`default preset "nightly" tools="read,write"`, "--units (partition mode is read-only", "dropping it"}, nil)

	// Run level: exit 0, the engine runs the partition plan.
	got := guardEngine(t)
	var out, errw bytes.Buffer
	if code := Run([]string{"--units", plan}, nil, &out, &errw); code != 0 {
		t.Fatalf("Run exit = %d; stderr = %s", code, errw.String())
	}
	if len(*got) != 1 || len((*got)[0].Units) != 1 {
		t.Fatalf("engine config must carry the partition plan: %+v", *got)
	}
	if !strings.Contains(errw.String(), "conflicts with --units") {
		t.Errorf("stderr = %q; want the drop notice", errw.String())
	}
}

// (2) An explicitly named preset keeps the honest hard error: --preset and the
// env selector are the user's own choice, so their conflict is never silently
// resolved. The remaining seat-source conflict is a seats pin facing --units.
func TestExplicitlySelectedPresetKeepsTheHardError(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a"}},
		DefaultPreset: "nightly",
	})
	plan := guardPartitionPlan(t, home)

	// --preset by name, contradicted by --units.
	f := ladderFlags(stderr)
	f.preset = "nightly"
	f.units = plan
	f.set["units"] = true
	if _, err := buildRunConfig(f, ""); err == nil ||
		err.Error() != "--units cannot be combined with --seats (partition mode assigns one seat per unit)" {
		t.Errorf("--preset nightly + --units error = %v; want the hard error", err)
	}

	// Same through Run(): exit 2, no engine call.
	got := guardEngine(t)
	var out, errw bytes.Buffer
	if code := Run([]string{"--preset", "nightly", "--units", plan}, nil, &out, &errw); code != 2 {
		t.Errorf("Run exit = %d; want 2", code)
	}
	if !strings.Contains(errw.String(), "--units cannot be combined with --seats") {
		t.Errorf("stderr = %q; want the hard error", errw.String())
	}
	if len(*got) != 0 {
		t.Error("a named-preset conflict must not reach the engine")
	}

	// The env selector is a named choice too.
	t.Setenv(EnvFanoutPreset, "nightly")
	stderr.Reset()
	f = ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	if _, err := buildRunConfig(f, ""); err == nil ||
		err.Error() != "--units cannot be combined with --seats (partition mode assigns one seat per unit)" {
		t.Errorf("env-selected preset + --units error = %v; want the hard error", err)
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
	if len(cfg.Seats) != 1 || cfg.Seats[0].ModelID != "gemini-a" {
		t.Errorf("named preset seats = %+v; want the preset lineup", cfg.Seats)
	}
	// A named preset is ad-hoc, so the run MUST echo the lineup it adopts (FL-3):
	// the only stderr line here is that echo, never a drop notice.
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "seat lineup from preset") {
		t.Errorf("stderr = %q; want the ad-hoc lineup echo only", stderr.String())
	}
}

// (3) The dangling-default behavior is unchanged: one byte-pinned warning, no
// drop notice, engine rungs below.
func TestDefaultKeyDanglingKeepsItsSingleWarning(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "main", Seats: "native:gemini-a"}},
		DefaultPreset: "ghost",
	})

	f := ladderFlags(stderr)
	f.seats = "native:gemini-x"
	f.set["seats"] = true
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("dangling default key must not error: %v", err)
	}
	if len(cfg.Seats) != 1 {
		t.Errorf("seats = %+v; want the flag seat", cfg.Seats)
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
		Presets:       []Preset{{Name: "nightly", Task: "security", N: 2}},
		DefaultPreset: "nightly",
	})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Task != "security" || cfg.N != 2 {
		t.Errorf("task/n = %q/%d; want the preset's security/2", cfg.Task, cfg.N)
	}
	if cfg.Sources["task"] != "preset" {
		t.Errorf("sources[task] = %q; want preset", cfg.Sources["task"])
	}
	if stderr.Len() != 0 {
		t.Errorf("no notice expected without a conflict; stderr = %q", stderr.String())
	}
}

// The remaining seat-source direction: a seats pin facing an explicit --units
// run goes, with one notice, and the partition plan runs.
func TestDefaultKeySeatsPinDropsForUnits(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a"}},
		DefaultPreset: "nightly",
	})
	plan := guardPartitionPlan(t, home)

	f := ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	cfg, err := buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("a seats-pinning default preset must not break --units: %v", err)
	}
	if len(cfg.Units) != 1 || len(cfg.Seats) != 0 {
		t.Errorf("units/seats = %d/%+v; want 1 unit and no seats", len(cfg.Units), cfg.Seats)
	}
	guardOneNotice(t, stderr.String(),
		[]string{`seats="native:gemini-a"`, "--units (partition mode assigns one seat per unit)"}, nil)

	// An explicit --seats flag already outranks the preset rung, so the guard
	// stays silent: no notice, and the flag lineup wins.
	stderr.Reset()
	f = ladderFlags(stderr)
	f.seats = "native:gemini-b"
	f.set["seats"] = true
	cfg, err = buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("--seats must survive a seats-pinning default preset: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:gemini-b" {
		t.Errorf("seats = %q; want the flag's pin", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("an explicit --seats run must not collect a drop notice; stderr = %q", stderr.String())
	}
}
