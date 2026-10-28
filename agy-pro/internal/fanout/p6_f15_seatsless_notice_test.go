package fanout

import (
	"strings"
	"testing"
)

// p6SeatslessSeed is the shape the F-15 row is about: a standing-rung seed
// that pins privilege/egress knobs and NO seats. In this shape the lineup
// echo never prints (it is gated on len(seatSpecs) > 0, cli.go:719), so
// before the F-04 notice this run printed exactly nothing.
var p6SeatslessSeed = Preset{
	Name:           BuiltinPresetName,
	Tools:          "read,search,write",
	SearchEndpoint: "https://search.example/{query}",
	SearchKeyEnv:   "P6_SEARCH_KEY",
	Root:           "/workspace/review",
	OutDir:         "reports/review",
}

// F-15 (fix-list-1 ADJUDICATION APPEND): a seats-less standing seed emits
// EXACTLY ONE stderr line - the privilege/egress notice - and no lineup echo.
// This is the falsifier for the original HIGH-1 claim: remove the notice and
// this run is fully silent while the engine still receives the grant.
func TestStandingSeatslessSeedNoticeIsTheOnlyStderrLine(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{p6SeatslessSeed}})

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources["tools"] != "preset" || cfg.Tools != "read,search,write" {
		t.Errorf("the engine did not adopt the seed's tools grant: tools=%q src=%q", cfg.Tools, cfg.Sources["tools"])
	}
	if cfg.Sources["seats"] != "engine" || len(cfg.Seats) != 0 {
		t.Errorf("a seats-less seed must leave seats on the engine rung: %+v", cfg.Sources)
	}

	want := `fanout: tools="read,search,write", search_endpoint="https://search.example/{query}", search_key_env="P6_SEARCH_KEY", root="/workspace/review", out_dir="reports/review" (privilege/egress knobs: standing preset "(built-in defaults)")`
	if stderr.String() != want+"\n" {
		t.Fatalf("stderr is not exactly the one notice line\n got: %q\nwant: %q", stderr.String(), want)
	}
	if strings.Contains(stderr.String(), "seat lineup") {
		t.Errorf("a seats-less run must not print the lineup echo: %q", stderr.String())
	}
}

// F-15 plus P8-E: the same seats-less seed on the AD-HOC rungs (--preset and
// AGY_PRO_FANOUT_PRESET) adopts and discloses the knobs in EXACTLY ONE line.
// The no-lineup-echo shape is unchanged, and disclosure remains non-blocking.
func TestSeatslessSeedAdhocRungsDiscloseInOneLine(t *testing.T) {
	t.Run("--preset rung", func(t *testing.T) {
		home, stderr := ladderEnv(t)
		savePresetsFile(t, home, &PresetsFile{Presets: []Preset{p6SeatslessSeed}})
		f := ladderFlags(stderr)
		f.preset = BuiltinPresetName
		cfg, err := buildRunConfig(f, "p")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Sources["tools"] != "preset" || cfg.Tools != "read,search,write" {
			t.Errorf("the ad-hoc rung did not adopt the seed's tools grant: tools=%q src=%q", cfg.Tools, cfg.Sources["tools"])
		}
		if stderr.String() != p8AdhocSeatslessNotice+"\n" {
			t.Errorf("the ad-hoc rung must emit exactly one disclosure line: %q", stderr.String())
		}
	})

	t.Run("env rung", func(t *testing.T) {
		home, stderr := ladderEnv(t)
		savePresetsFile(t, home, &PresetsFile{Presets: []Preset{p6SeatslessSeed}})
		t.Setenv(EnvFanoutPreset, BuiltinPresetName)
		cfg, err := buildRunConfig(ladderFlags(stderr), "p")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Sources["tools"] != "preset" || cfg.Tools != "read,search,write" {
			t.Errorf("the env rung did not adopt the seed's tools grant: tools=%q src=%q", cfg.Tools, cfg.Sources["tools"])
		}
		if stderr.String() != p8AdhocSeatslessNotice+"\n" {
			t.Errorf("the env rung must emit exactly one disclosure line: %q", stderr.String())
		}
	})
}

// F-15: a standing preset that pins NO privilege/egress knob must not emit
// the notice - the disclosure must not regress into noise on ordinary
// presets (here an engine-shaped seed that only sets N).
func TestStandingSeedWithoutPrivilegeKnobsStaysSilent(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, N: 7}}})
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Errorf("a knob-free standing seed must print nothing: %q", stderr.String())
	}
}
