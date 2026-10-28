package fanout

import (
	"strings"
	"testing"
)

const p8AdhocSeatslessNotice = `fanout: tools="read,search,write", search_endpoint="https://search.example/{query}", search_key_env="P6_SEARCH_KEY", root="/workspace/review", out_dir="reports/review" (privilege/egress knobs: preset "(built-in defaults)")`

func TestP8AdhocRungsDiscloseEffectivePrivilegeKnobs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T, *fanoutFlags)
	}{
		{name: "named --preset", check: func(t *testing.T, f *fanoutFlags) { f.preset = BuiltinPresetName }},
		{name: "environment preset", check: func(t *testing.T, f *fanoutFlags) { t.Setenv(EnvFanoutPreset, BuiltinPresetName) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			savePresetsFile(t, home, &PresetsFile{Presets: []Preset{p6SeatslessSeed}})
			f := ladderFlags(stderr)
			tc.check(t, &f)
			cfg, err := buildRunConfig(f, "p")
			if err != nil {
				t.Fatalf("disclosure must not become a refusal or consent gate: %v", err)
			}
			if cfg.Sources["tools"] != "preset" || cfg.Tools != "read,search,write" || cfg.Sources["search"] != "preset" {
				t.Errorf("ad-hoc rung did not adopt the preset knobs: %+v", cfg.Sources)
			}
			if stderr.String() != p8AdhocSeatslessNotice+"\n" {
				t.Errorf("ad-hoc disclosure drifted\n got: %q\nwant: %q", stderr.String(), p8AdhocSeatslessNotice+"\n")
			}
			if strings.Contains(stderr.String(), "seat lineup") {
				t.Errorf("a seats-less ad-hoc rung must not print a lineup echo: %q", stderr.String())
			}
		})
	}
}

func TestP8AdhocRungsWithoutPrivilegeKnobsStaySilent(t *testing.T) {
	for _, named := range []bool{false, true} {
		home, stderr := ladderEnv(t)
		savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: BuiltinPresetName, N: 7}}})
		f := ladderFlags(stderr)
		if named {
			f.preset = BuiltinPresetName
		} else {
			t.Setenv(EnvFanoutPreset, BuiltinPresetName)
		}
		if _, err := buildRunConfig(f, "p"); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 {
			t.Errorf("knob-free ad-hoc rung produced noise (named=%v): %q", named, stderr.String())
		}
	}
}
