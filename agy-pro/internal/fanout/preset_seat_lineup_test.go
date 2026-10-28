package fanout

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// U2a spec 1/6: the seat lineup is a ladder field. These tests pin the priority
// order (explicit --seats > preset > nothing => engine auto-select) and the
// RunConfig.SeatLineup origin marker that selects the drift policy downstream.
//
// ladderEnv/savePresetsFile/writeDefaults/ladderFlags come from
// preset_ladder_test.go; they isolate AGY_PRO_HOME (preset store) and
// AGY_PRO_PROVIDERS (defaults block) so no live machine state can leak in.

// seatsOf renders the parsed lineup as raw tokens, so an assertion failure
// prints what actually resolved instead of Go structs.
func seatsOf(specs []SeatSpec) string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Raw)
	}
	return strings.Join(out, ",")
}

// A bare run (no --seats, no --preset) adopts the default_preset's pinned
// lineup: that IS the wiring U2a verifies, and it must report the preset rung.
func TestBareRunAdoptsDefaultPresetSeatLineup(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Task: "review", Seats: "native:gemini-g,prov/m1"}},
		DefaultPreset: "nightly",
	})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:gemini-g,prov/m1" {
		t.Errorf("seats = %q; want the preset lineup native:gemini-g,prov/m1", got)
	}
	if cfg.Seats[0].Kind != SeatKindNative || cfg.Seats[1].Kind != SeatKindProvider {
		t.Errorf("kinds = %q/%q; want native/provider in preset order", cfg.Seats[0].Kind, cfg.Seats[1].Kind)
	}
	if cfg.Sources["seats"] != "preset" {
		t.Errorf("sources[seats] = %q; want preset", cfg.Sources["seats"])
	}
	if cfg.SeatLineup.Preset != "nightly" || !cfg.SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; want the standing nightly origin (this is what arms the drift policy)", cfg.SeatLineup)
	}
	// --seats wins over --n, so the lineup size is the seat count.
	if cfg.N != 2 {
		t.Errorf("N = %d; want 2 (the lineup size)", cfg.N)
	}
	// FL-3(b): the lineup echo is unconditional, and it names the preset, the
	// rung and the metered-seat count, so an automatic run never consumes a pin
	// silently (also the FL-11 visibility gap).
	echo := stderr.String()
	if strings.Count(echo, "\n") != 1 {
		t.Fatalf("want exactly the one lineup echo line; got %d line(s): %q", strings.Count(echo, "\n"), echo)
	}
	for _, want := range []string{`seat lineup from preset "nightly"`, "(default_preset rung)", "2 seat(s), 1 metered provider seat", "native:gemini-g,prov/m1"} {
		if !strings.Contains(echo, want) {
			t.Errorf("lineup echo %q missing %q", echo, want)
		}
	}
	// Standing approval carries no ad-hoc clause.
	if strings.Contains(echo, "ad-hoc") {
		t.Errorf("the default_preset rung must not read as ad-hoc: %q", echo)
	}
}

// An explicit --seats flag still outranks the default preset's pin, and the
// preset marker stays empty so the engine keeps the hard-error drift policy for
// the operator's own pick.
func TestExplicitSeatsOutrankDefaultPresetLineup(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-g,prov/m1"}},
		DefaultPreset: "nightly",
	})

	f := ladderFlags(stderr)
	f.set["seats"] = true
	f.seats = "native:gemini-flag"
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:gemini-flag" {
		t.Errorf("seats = %q; want the explicit flag seat native:gemini-flag", got)
	}
	if cfg.Sources["seats"] != "flag" {
		t.Errorf("sources[seats] = %q; want flag", cfg.Sources["seats"])
	}
	if cfg.SeatLineup.FromPreset() {
		t.Errorf("SeatLineup = %+v; want no preset origin (an explicit --seats lineup keeps the hard error)", cfg.SeatLineup)
	}
}

// No preset and no pin: the lineup stays empty so the engine auto-selects,
// byte-identically to the pre-U2a behavior.
func TestNoPresetKeepsAutoSelection(t *testing.T) {
	_, stderr := ladderEnv(t)
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if len(cfg.Seats) != 0 {
		t.Errorf("seats = %q; want none (engine auto-selects)", seatsOf(cfg.Seats))
	}
	if cfg.Sources["seats"] != "engine" {
		t.Errorf("sources[seats] = %q; want engine", cfg.Sources["seats"])
	}
	if cfg.SeatLineup.FromPreset() {
		t.Errorf("SeatLineup = %+v; want no preset origin", cfg.SeatLineup)
	}
}

// A preset whose seats field is empty pins no lineup: the auto-select fallback
// must not change (explicit user decision recorded for U2a).
func TestPresetWithoutSeatsKeepsAutoSelection(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", N: 4}},
		DefaultPreset: "nightly",
	})
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if len(cfg.Seats) != 0 {
		t.Errorf("seats = %q; want none", seatsOf(cfg.Seats))
	}
	if cfg.N != 4 {
		t.Errorf("N = %d; want 4 from the preset", cfg.N)
	}
	if cfg.Sources["seats"] != "engine" {
		t.Errorf("sources[seats] = %q; want engine", cfg.Sources["seats"])
	}
	if cfg.SeatLineup.FromPreset() {
		t.Errorf("SeatLineup = %+v; want no preset origin", cfg.SeatLineup)
	}
}

// The drift-tolerant policy keys on the PRESET rung, not on the default key, so
// a named preset (--preset) or an env-selected one marks the lineup the same
// way. That is what makes "preset-sourced lineup" one policy instead of two.
func TestNamedPresetLineupMarksPresetSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preset  string
		envName string
	}{
		{name: "flag", preset: "nightly"},
		{name: "env", envName: "nightly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			savePresetsFile(t, home, &PresetsFile{
				Presets: []Preset{{Name: "nightly", Seats: "native:gemini-g,prov/m1"}},
			})
			if tc.envName != "" {
				t.Setenv(EnvFanoutPreset, tc.envName)
			}
			f := ladderFlags(stderr)
			f.preset = tc.preset
			cfg, err := buildRunConfig(f, "p")
			if err != nil {
				t.Fatalf("buildRunConfig: %v", err)
			}
			if got := seatsOf(cfg.Seats); got != "native:gemini-g,prov/m1" {
				t.Errorf("seats = %q; want the preset lineup", got)
			}
			if cfg.Sources["seats"] != "preset" {
				t.Errorf("sources[seats] = %q; want preset", cfg.Sources["seats"])
			}
			if cfg.SeatLineup.Preset != "nightly" {
				t.Errorf("SeatLineup = %+v; want nightly", cfg.SeatLineup)
			}
			// FL-3: only the store default_preset rung is standing approval; a
			// NAMED preset resolves the same lineup but stays ad-hoc.
			if tc.name == "flag" && cfg.SeatLineup.Standing() {
				t.Errorf("SeatLineup rung = %q; a --preset selection must NOT be standing", cfg.SeatLineup.Rung)
			}
		})
	}
}

// The whole seat rung as one table: rung, resolved lineup, source, and whether
// the drift policy is armed.
func TestSeatLineupLadderTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presets *PresetsFile
		env     string
		flag    string
		preset  string
		want    string
		source  string
		marker  string
		rung    string
	}{
		{
			name: "explicit flag beats preset",
			presets: &PresetsFile{Presets: []Preset{{Name: "nightly", Seats: "native:p"}},
				DefaultPreset: "nightly"},
			flag:   "native:f",
			want:   "native:f",
			source: "flag",
			marker: "",
			rung:   "",
		},
		{
			name:    "default key supplies the lineup",
			presets: &PresetsFile{Presets: []Preset{{Name: "nightly", Seats: "native:p"}}, DefaultPreset: "nightly"},
			want:    "native:p",
			source:  "preset",
			marker:  "nightly",
			rung:    RungDefaultPreset,
		},
		{
			name:    "env preset beats the default key",
			presets: &PresetsFile{Presets: []Preset{{Name: "nightly", Seats: "native:p"}, {Name: "envp", Seats: "native:e"}}, DefaultPreset: "nightly"},
			env:     "envp",
			want:    "native:e",
			source:  "preset",
			marker:  "envp",
			rung:    RungPresetEnv,
		},
		{
			name:    "--preset flag beats the default key",
			presets: &PresetsFile{Presets: []Preset{{Name: "nightly", Seats: "native:p"}, {Name: "named", Seats: "native:n"}}, DefaultPreset: "nightly"},
			preset:  "named",
			want:    "native:n",
			source:  "preset",
			marker:  "named",
			rung:    RungPresetFlag,
		},
		{
			name:   "no preset at all: engine auto-select",
			want:   "",
			source: "engine",
			marker: "",
		},
		{
			name:    "preset present but pins no lineup",
			presets: &PresetsFile{Presets: []Preset{{Name: "nightly", N: 2}}, DefaultPreset: "nightly"},
			want:    "",
			source:  "engine",
			marker:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			if tc.presets != nil {
				savePresetsFile(t, home, tc.presets)
			}
			if tc.env != "" {
				t.Setenv(EnvFanoutPreset, tc.env)
			}
			f := ladderFlags(stderr)
			f.preset = tc.preset
			if tc.flag != "" {
				f.set["seats"] = true
				f.seats = tc.flag
			}
			cfg, err := buildRunConfig(f, "p")
			if err != nil {
				t.Fatalf("buildRunConfig: %v", err)
			}
			if got := seatsOf(cfg.Seats); got != tc.want {
				t.Errorf("seats = %q; want %q", got, tc.want)
			}
			if cfg.Sources["seats"] != tc.source {
				t.Errorf("sources[seats] = %q; want %q", cfg.Sources["seats"], tc.source)
			}
			if cfg.SeatLineup.Preset != tc.marker {
				t.Errorf("SeatLineup.Preset = %q; want %q", cfg.SeatLineup.Preset, tc.marker)
			}
			if cfg.SeatLineup.Rung != tc.rung {
				t.Errorf("SeatLineup.Rung = %q; want %q", cfg.SeatLineup.Rung, tc.rung)
			}
		})
	}
}

// ResolvePresetSeatLineup is the store-facing API the swarm door reads instead
// of parsing fanout-presets.json itself.
func TestResolvePresetSeatLineupTable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		presets    *PresetsFile
		env        string
		wantPreset string
		wantSpecs  string
		wantErr    string
	}{
		{
			name:       "default key pin",
			presets:    &PresetsFile{Presets: []Preset{{Name: "nightly", Seats: "native:a,prov/m"}}, DefaultPreset: "nightly"},
			wantPreset: "nightly",
			wantSpecs:  "native:a,prov/m",
		},
		{
			name:       "env pin",
			presets:    &PresetsFile{Presets: []Preset{{Name: "envp", Seats: "prov/m"}}},
			env:        "envp",
			wantPreset: "envp",
			wantSpecs:  "prov/m",
		},
		{
			name:    "no preset file",
			wantErr: "",
		},
		{
			name:       "preset pins no lineup",
			presets:    &PresetsFile{Presets: []Preset{{Name: "nightly", N: 3}}, DefaultPreset: "nightly"},
			wantPreset: "nightly",
		},
		{
			name:    "dangling default key is not an error",
			presets: &PresetsFile{Presets: []Preset{{Name: "a", Seats: "prov/m"}}, DefaultPreset: "ghost"},
		},
		{
			name:    "env naming a missing preset is an error",
			presets: &PresetsFile{Presets: []Preset{{Name: "a", Seats: "prov/m"}}},
			env:     "ghost",
			wantErr: "ghost",
		},
		{
			name:    "unparsable seats value is an error",
			presets: &PresetsFile{Presets: []Preset{{Name: "bad", Seats: "native:"}}, DefaultPreset: "bad"},
			wantErr: "bad",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := ladderEnv(t)
			if tc.presets != nil {
				savePresetsFile(t, home, tc.presets)
			}
			if tc.env != "" {
				t.Setenv(EnvFanoutPreset, tc.env)
			}
			lineup, err := ResolvePresetSeatLineup()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v; want it to name %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePresetSeatLineup: %v", err)
			}
			if lineup.PresetName != tc.wantPreset {
				t.Errorf("PresetName = %q; want %q", lineup.PresetName, tc.wantPreset)
			}
			if got := seatsOf(lineup.Specs); got != tc.wantSpecs {
				t.Errorf("Specs = %q; want %q", got, tc.wantSpecs)
			}
			if lineup.Pinned() != (tc.wantSpecs != "") {
				t.Errorf("Pinned() = %v; want %v", lineup.Pinned(), tc.wantSpecs != "")
			}
		})
	}
}

// --- Editable built-in defaults: the seed record on the ladder (A2/A3) -------

// A2 pin: a store with user presets but NO seed record and no default_preset
// key behaves exactly as before the seed arm existed - the bare run falls to
// the engine literals, src[*] stays "engine", and no lineup echo prints. The
// seed arm must not misfire on ordinary records.
func TestNoSeedRecordKeepsEngineRungs(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "nightly", N: 4, Seats: "native:gemini-a"}}})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if len(cfg.Seats) != 0 || cfg.Sources["seats"] != "engine" {
		t.Errorf("seats = %q rung %q; want none and the engine rung (no key, no seed)", seatsOf(cfg.Seats), cfg.Sources["seats"])
	}
	if cfg.N != 3 || cfg.Sources["n"] != "engine" || cfg.Sources["task"] != "engine" ||
		cfg.Sources["tools"] != "engine" || cfg.Sources["timeout"] != "engine" {
		t.Errorf("an unkeyed store must stay on the engine rung: %+v", cfg.Sources)
	}
	if cfg.SeatLineup.FromPreset() {
		t.Errorf("SeatLineup = %+v; want no preset origin", cfg.SeatLineup)
	}
	if stderr.Len() != 0 {
		t.Errorf("no echo and no notice expected without a resolved preset: %q", stderr.String())
	}
}

// A3 core: a seed record written through SavePresets (so it carries the
// seats_sha256 marker), with ONLY Seats set, is consumed by a plain bare run:
// the lineup applies with src["seats"]=="preset", every untouched field stays
// on the engine rung, the origin is the standing store-default rung, and the
// provenance advisory is SILENT (SavePresets stamped the marker).
func TestBareRunAdoptsBuiltinSeedLineup(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"}}})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:gemini-a,prov/m1" {
		t.Errorf("seats = %q; want the seed lineup native:gemini-a,prov/m1", got)
	}
	if cfg.Sources["seats"] != "preset" {
		t.Errorf("sources[seats] = %q; want preset", cfg.Sources["seats"])
	}
	// Every untouched field stays on the engine rung (A2 per-field semantics).
	if cfg.Task != "review" || cfg.Sources["task"] != "engine" {
		t.Errorf("task = %q rung %q; want the engine default review", cfg.Task, cfg.Sources["task"])
	}
	if cfg.N != 2 || cfg.Sources["n"] != "engine" {
		t.Errorf("n = %d rung %q; want the lineup size 2 with the engine rung label", cfg.N, cfg.Sources["n"])
	}
	if cfg.Tools != "read,search" || cfg.Sources["tools"] != "engine" {
		t.Errorf("tools = %q rung %q; want the engine default read,search", cfg.Tools, cfg.Sources["tools"])
	}
	if cfg.TimeoutSec != 300 || cfg.Sources["timeout"] != "engine" || cfg.MaxTurns != 500 || cfg.Sources["max_turns"] != "engine" {
		t.Errorf("timeout/max_turns = %d/%d rungs %q/%q; want 300/500 on the engine rung",
			cfg.TimeoutSec, cfg.MaxTurns, cfg.Sources["timeout"], cfg.Sources["max_turns"])
	}
	if cfg.Root != "." || cfg.Sources["root"] != "engine" || cfg.JSONOutput || cfg.Sources["json"] != "engine" {
		t.Errorf("root/json = %q/%v rungs %q/%q; want ./false on the engine rung", cfg.Root, cfg.JSONOutput, cfg.Sources["root"], cfg.Sources["json"])
	}
	// The standing-rung proof: the seed arm returns fromDefaultKey=true, so the
	// origin is RungDefaultPreset and Standing() stays true (FL-3).
	if cfg.SeatLineup.Preset != BuiltinPresetName || cfg.SeatLineup.Rung != RungDefaultPreset || !cfg.SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; want the standing %q origin on the default_preset rung", cfg.SeatLineup, BuiltinPresetName)
	}
	// stderr carries EXACTLY the one lineup echo line, byte-identical through
	// the MaskSlot authority, and no provenance advisory of any kind.
	echo := stderr.String()
	if strings.Count(echo, "\n") != 1 {
		t.Fatalf("want exactly the one lineup echo line; got %q", echo)
	}
	wantEcho := "fanout: seat lineup from preset \"(built-in defaults)\" (default_preset rung, no key set): 2 seat(s), 1 metered provider seat (native:gemini-a,prov/m1)\n"
	if echo != wantEcho {
		t.Errorf("lineup echo drifted\n got: %q\nwant: %q", echo, wantEcho)
	}
	if strings.Contains(echo, "ad-hoc") || strings.Contains(echo, "notice") || strings.Contains(echo, "provenance") {
		t.Errorf("a store-stamped seed is standing approval: no ad-hoc clause, no advisory: %q", echo)
	}
}

// A3 consequence 3: a seed record hand-written to disk (no seats_sha256
// marker) arms the existing provenance advisory - a hand-authored pin is not
// standing approval - while the run still consumes the on-disk lineup.
func TestHandWrittenSeedArmsProvenanceAdvisory(t *testing.T) {
	home, stderr := ladderEnv(t)
	raw := `{"presets":[{"name":"(built-in defaults)","seats":"native:gemini-a,prov/m1"}]}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("a marker-less seed must never block the run: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:gemini-a,prov/m1" {
		t.Errorf("seats = %q; want the on-disk seed lineup used anyway", got)
	}
	if !cfg.SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; the seed arm still rides the standing rung", cfg.SeatLineup)
	}
	out := stderr.String()
	if !strings.Contains(out, `preset "(built-in defaults)" pins a seat lineup with NO provenance marker`) {
		t.Errorf("the hand-authored seed advisory must name the seed byte-identically: %q", out)
	}
	if strings.Contains(out, "edited outside the preset editor") {
		t.Errorf("a marker-less record is not an edit-after-save: %q", out)
	}
}

// A3 consequence 2 / F1: the seed's knobs are shielded by the same
// dropConflictingDefaultPresetKnobs guard as any store-default record, so the
// swarm pinned vector and the codework --units vector survive a pinning seed.
func TestBuiltinSeedKnobsHitTheF1DropGuard(t *testing.T) {
	// (a) A --units partition run drops the seed's seats pin.
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:gemini-a"}}})
	plan := guardPartitionPlan(t, home)
	f := ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	cfg, err := buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("a seats-pinning seed must not break --units: %v", err)
	}
	if len(cfg.Units) != 1 || len(cfg.Seats) != 0 || cfg.Sources["seats"] != "engine" {
		t.Errorf("units/seats = %d/%+v rung %q; want 1 unit, no seats, seats on the engine rung", len(cfg.Units), cfg.Seats, cfg.Sources["seats"])
	}
	guardOneNotice(t, stderr.String(),
		[]string{`default preset "(built-in defaults)" seats="native:gemini-a"`, "--units (partition mode assigns one seat per unit)", "dropping it"}, nil)

	// (c) A --units partition run drops the seed's WRITE-GRANTING tools pin
	// (the partition read-only gate survives).
	home, stderr = ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Tools: "read,search,write"}}})
	plan = guardPartitionPlan(t, home)
	f = ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	cfg, err = buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("a write-tools seed must not break --units: %v", err)
	}
	if cfg.Tools != defaultFanoutTools || cfg.Sources["tools"] != "engine" {
		t.Errorf("tools = %q rung %q; want the engine default %q (the pin is dropped)", cfg.Tools, cfg.Sources["tools"], defaultFanoutTools)
	}
	guardOneNotice(t, stderr.String(),
		[]string{`default preset "(built-in defaults)" tools="read,search,write"`, "partition mode is read-only", "dropping it"}, nil)
}

// An explicit --seats outranks the seed lineup exactly like any store-default
// pin: the flag owns the run, the origin marker stays empty (the honest
// hard-error drift policy for the operator's own pick), and no echo prints.
func TestExplicitSeatsOutrankBuiltinSeedLineup(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"}}})

	f := ladderFlags(stderr)
	f.set["seats"] = true
	f.seats = "native:gemini-flag"
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:gemini-flag" {
		t.Errorf("seats = %q; want the explicit flag seat", got)
	}
	if cfg.Sources["seats"] != "flag" || cfg.SeatLineup.FromPreset() {
		t.Errorf("sources/origin = %q/%+v; want the flag rung and no preset origin", cfg.Sources["seats"], cfg.SeatLineup)
	}
	if stderr.Len() != 0 {
		t.Errorf("no echo expected for an explicit-seat run: %q", stderr.String())
	}
}

// Doctrine (A3): the KEY, not the record, carries standing approval. Naming the
// seed explicitly classifies the same record through the named rung: ad-hoc,
// echo clause armed, Standing() false.
func TestNamedBuiltinSeedSelectionIsAdHoc(t *testing.T) {
	for _, tc := range []struct {
		name   string
		preset string
		env    string
		rung   string
	}{
		{name: "--preset flag", preset: BuiltinPresetName, rung: RungPresetFlag},
		{name: "env", env: BuiltinPresetName, rung: RungPresetEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:gemini-a,nw/glm"}}})
			if tc.env != "" {
				t.Setenv(EnvFanoutPreset, tc.env)
			}
			f := ladderFlags(stderr)
			f.preset = tc.preset
			cfg, err := buildRunConfig(f, "p")
			if err != nil {
				t.Fatalf("buildRunConfig: %v", err)
			}
			if got := seatsOf(cfg.Seats); got != "native:gemini-a,nw/glm" {
				t.Errorf("seats = %q; want the seed lineup (the record resolves)", got)
			}
			if cfg.SeatLineup.Rung != tc.rung || cfg.SeatLineup.Standing() {
				t.Errorf("SeatLineup = %+v; want the ad-hoc %q rung, NOT standing", cfg.SeatLineup, tc.rung)
			}
			echo := stderr.String()
			if !strings.Contains(echo, `seat lineup from preset "(built-in defaults)" (`+tc.rung+` rung)`) {
				t.Errorf("echo must name the seed and the %q rung: %q", tc.rung, echo)
			}
			if !strings.Contains(echo, "ad-hoc") {
				t.Errorf("a named seed selection must carry the ad-hoc consent clause: %q", echo)
			}
		})
	}
}

// The swarm door (ResolvePresetSeatLineup) picks the seed up with its empty
// flag set: no --preset by definition, no env, no default key, so the A3 arm
// supplies the lineup with the standing rung.
func TestResolvePresetSeatLineupReadsBuiltinSeed(t *testing.T) {
	home, _ := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"}}})

	lineup, err := ResolvePresetSeatLineup()
	if err != nil {
		t.Fatalf("ResolvePresetSeatLineup: %v", err)
	}
	if lineup.PresetName != BuiltinPresetName {
		t.Errorf("PresetName = %q; want the seed record", lineup.PresetName)
	}
	if got := seatsOf(lineup.Specs); got != "native:gemini-a,prov/m1" {
		t.Errorf("Specs = %q; want the seed lineup", got)
	}
	if !lineup.Pinned() || lineup.Rung != RungDefaultPreset || !lineup.Standing() {
		t.Errorf("lineup = %+v; want pinned, on the default_preset rung, standing", lineup)
	}

	// Absent seed: the swarm door keeps the empty (auto-select) lineup.
	home, _ = ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "nightly", Seats: "native:gemini-a"}}})
	lineup, err = ResolvePresetSeatLineup()
	if err != nil {
		t.Fatalf("ResolvePresetSeatLineup without a seed: %v", err)
	}
	if lineup.Pinned() || lineup.PresetName != "" {
		t.Errorf("lineup = %+v; want empty without a key, env or seed", lineup)
	}
}

// A7 masking pin at the ResolvePresetSeatLineup parse-error sink
// (preset_store.go): the error names the preset through providers.MaskSlot, so
// the benign seed name renders byte-identical AND a planted 16-rune in-class
// credential in the same slot is masked.
func TestResolvePresetSeatLineupParseErrorMasksPresetName(t *testing.T) {
	// Benign: the seed name renders byte-identical in the parse error.
	home, _ := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:"}}})
	_, err := ResolvePresetSeatLineup()
	if err == nil {
		t.Fatal("an unparsable seed lineup must surface as a resolver error")
	}
	if want := `preset "(built-in defaults)" seats:`; !strings.Contains(err.Error(), want) {
		t.Errorf("parse error = %q; want it to name the seed byte-identically (%q)", err.Error(), want)
	}

	// Planted: the same slot masks a 16-rune in-class credential (the
	// sk_live_<16> underscore-head class, above the slot authority's tail
	// floor) and never echoes it raw. The key points at the planted record so
	// the resolver actually selects it (a planted NAME with no key is just an
	// unselected user record, which is also why it cannot hijack the seed arm).
	home, _ = ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: testfixtures.SKLive, Seats: "native:"}},
		DefaultPreset: testfixtures.SKLive,
	})
	_, err = ResolvePresetSeatLineup()
	if err == nil {
		t.Fatal("an unparsable planted lineup must surface as a resolver error")
	}
	if strings.Contains(err.Error(), testfixtures.SKLive) {
		t.Errorf("parse error leaks the planted credential raw: %q", err.Error())
	}
	if !strings.Contains(err.Error(), `preset "sk_li****" seats:`) {
		t.Errorf("parse error = %q; want the masked rendering sk_li****", err.Error())
	}
}

// Run() level, the acceptance-shape scenario: a plain `agy pro fanout` (only
// -prompt, no --seats/--preset/env) against a store whose seed pins seats
// executes with the seed lineup, the standing origin, its echo, and the
// standing native-search disclosure.
func TestRunLevelBuiltinSeedStandingLineup(t *testing.T) {
	home, _ := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, Seats: "native:gemini-a,prov/m1"}}})

	got := guardEngine(t)
	var out, errw bytes.Buffer
	code := Run([]string{"-task", "review", "-prompt", "p", "-json"}, nil, &out, &errw)
	if code != 0 {
		t.Fatalf("Run exit = %d; stderr = %s", code, errw.String())
	}
	if len(*got) != 1 {
		t.Fatalf("engine calls = %d; want 1", len(*got))
	}
	cfg := (*got)[0]
	if s := seatsOf(cfg.Seats); s != "native:gemini-a,prov/m1" {
		t.Errorf("engine seats = %q; want the seed lineup", s)
	}
	if cfg.SeatLineup.Preset != BuiltinPresetName || cfg.SeatLineup.Rung != RungDefaultPreset || !cfg.SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; want the standing seed origin", cfg.SeatLineup)
	}
	echo := errw.String()
	if strings.Count(echo, "\n") != 2 || !strings.Contains(echo, `seat lineup from preset "(built-in defaults)" (default_preset rung, no key set)`) ||
		!strings.Contains(echo, "fanout[engine] search=agy-native (full agy agent child; subscription per search)") {
		t.Errorf("stderr = %q; want the standing echo plus native-search disclosure", echo)
	}
	if strings.Contains(echo, "ad-hoc") || strings.Contains(echo, "provenance") {
		t.Errorf("a store-stamped seed run must not warn: %q", echo)
	}
}
