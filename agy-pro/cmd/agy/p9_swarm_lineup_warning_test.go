package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

func writeBrokenLineup(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv(fanout.EnvFanoutPreset, "")
	name := testfixtures.SKLive
	data, err := json.Marshal(fanout.PresetsFile{
		Presets:       []fanout.Preset{{Name: name, Task: "review", Seats: "native:"}},
		DefaultPreset: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSwarmDoorMissingSeatsSurfacesLineupLookupWarning(t *testing.T) {
	writeBrokenLineup(t)
	var out, errw strings.Builder
	code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		prompt:   "check it",
		dryRun:   true,
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr:\n%s", code, errw.String())
	}
	stderr := errw.String()
	for _, want := range []string{
		`swarm: warning: preset "sk_li****" seats:`,
		"using the requested seats",
		"Error: missing required option(s): --seats",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, testfixtures.SKLive) {
		t.Errorf("warning leaked the preset name raw:\n%s", stderr)
	}
	if got := strings.Count(stderr, "swarm: warning:"); got != 1 {
		t.Errorf("warning count = %d, want 1:\n%s", got, stderr)
	}
	t.Logf("stderr:\n%s", stderr)
}

func TestSwarmDoorExplicitSeatsSuppressLineupLookupWarning(t *testing.T) {
	writeBrokenLineup(t)
	var out, errw strings.Builder
	code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		seats:    "native:operator-seat",
		prompt:   "check it",
		dryRun:   true,
	})
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, errw.String())
	}
	if strings.Contains(errw.String(), "swarm: warning:") || strings.Contains(errw.String(), "using the requested seats") {
		t.Errorf("explicit --seats vector printed the preset warning:\n%s", errw.String())
	}
	if !strings.Contains(out.String(), "--seats native:operator-seat") {
		t.Errorf("explicit seats missing from dry-run echo:\n%s", out.String())
	}
}

func TestSwarmDoorDoesNotDuplicateDelegatedLineupWarning(t *testing.T) {
	writeBrokenLineup(t)
	_, warning := swarm.ResolveRunSpecLineup()
	if warning == "" {
		t.Fatal("fixture did not produce a lineup warning")
	}
	original := swarm.RunFanout
	t.Cleanup(func() { swarm.RunFanout = original })
	swarm.RunFanout = func(_ []string, _ io.Reader, _ io.Writer, errw io.Writer) int {
		fmt.Fprintln(errw, warning)
		return 2
	}

	var out, errw strings.Builder
	code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		seats:    "native:operator-seat",
		prompt:   "check it",
	})
	if code != 2 {
		t.Fatalf("exit = %d, want delegated 2; stderr:\n%s", code, errw.String())
	}
	if got := strings.Count(errw.String(), warning); got != 1 {
		t.Errorf("delegated warning count = %d, want 1:\n%s", got, errw.String())
	}
}
