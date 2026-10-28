package fanout_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codework"
	"agypro/internal/fanout"
	"agypro/internal/swarm"
)

const p8DoorNoticeWithRoot = `fanout: search_endpoint="https://search.invalid/{query}", root="/workspace/review" (privilege/egress knobs: standing preset "(built-in defaults)")`
const p8DoorNoticeEndpointOnly = `fanout: search_endpoint="https://search.invalid/{query}" (privilege/egress knobs: standing preset "(built-in defaults)")`

func p8DoorEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv("AGY_PRO_PROVIDERS", filepath.Join(home, "absent-providers.json"))
	t.Setenv(fanout.EnvFanoutPreset, "")
	data, err := json.Marshal(&fanout.PresetsFile{Presets: []fanout.Preset{{
		Name:           fanout.BuiltinPresetName,
		Seats:          "native:",
		SearchEndpoint: "https://search.invalid/{query}",
		Root:           "/workspace/review",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func p8DisclosureLine(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if strings.Contains(line, "privilege/egress knobs:") {
			return line
		}
	}
	t.Fatalf("no privilege/egress disclosure reached the door: %q", stderr)
	return ""
}

func TestP8PrivilegeDisclosureReachesBareSwarmAndCodeworkDoors(t *testing.T) {
	t.Run("bare CLI", func(t *testing.T) {
		p8DoorEnv(t)
		var out, errw bytes.Buffer
		if code := fanout.Run([]string{"--prompt", "p"}, strings.NewReader(""), &out, &errw); code != 2 {
			t.Fatalf("malformed no-spawn seat pin should stop before the engine with exit 2, got %d; stderr=%q", code, errw.String())
		}
		if got := p8DisclosureLine(t, errw.String()); got != p8DoorNoticeWithRoot {
			t.Errorf("bare CLI disclosure drifted: %q", got)
		} else {
			t.Logf("bare CLI rendered: %s", got)
		}
	})

	t.Run("swarm", func(t *testing.T) {
		p8DoorEnv(t)
		var out, errw bytes.Buffer
		spec := swarm.RunSpec{
			Template: swarm.Templates()[0],
			Root:     t.TempDir(),
			Prompt:   "p",
			PresetLineup: fanout.SeatLineup{
				PresetName: fanout.BuiltinPresetName,
				Rung:       fanout.RungDefaultPreset,
				Raw:        "native:unused",
				Specs:      []fanout.SeatSpec{{Kind: fanout.SeatKindNative, Raw: "native:unused", ModelID: "unused"}},
			},
		}
		if code := swarm.Execute(spec, strings.NewReader(""), &out, &errw, false); code != 2 {
			t.Fatalf("swarm no-spawn run exit = %d; stderr=%q", code, errw.String())
		}
		if got := p8DisclosureLine(t, errw.String()); got != p8DoorNoticeEndpointOnly {
			t.Errorf("swarm disclosure drifted: %q", got)
		} else {
			t.Logf("swarm rendered: %s", got)
		}
	})

	t.Run("codework", func(t *testing.T) {
		p8DoorEnv(t)
		root := t.TempDir()
		diffPath := filepath.Join(root, "diff.md")
		if err := os.WriteFile(diffPath, []byte("diff"), 0o600); err != nil {
			t.Fatal(err)
		}
		oldIn, oldOut, oldErr := codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr
		var out, errw bytes.Buffer
		codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr = strings.NewReader(""), &out, &errw
		t.Cleanup(func() { codework.PhaseStdin, codework.PhaseStdout, codework.PhaseStderr = oldIn, oldOut, oldErr })
		res, _ := codework.ReviewPhase(codework.CodeConfig{
			Root:        root,
			OutDir:      filepath.Join(root, "codework"),
			ReviewSeats: []string{"p8.invalid/no-such-seat"},
			Yes:         true,
		}, diffPath)
		if res == nil || res.ExitCode != 1 {
			t.Fatalf("codework no-registry result = %+v; stderr=%q", res, errw.String())
		}
		if got := p8DisclosureLine(t, errw.String()); got != p8DoorNoticeEndpointOnly {
			t.Errorf("codework disclosure drifted: %q", got)
		} else {
			t.Logf("codework rendered: %s", got)
		}
	})
}
