package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// FL-1/FL-7 (door layer): `agy pro swarm` obeys the seat-source priority
//
//	operator --seats  >  fanout default-preset pin  >  required-option error
//
// The argv is the observable: --seats present means the operator's seats run, and
// its absence means the delegated fanout run resolves the preset's lineup. These
// tests drive runSwarmNonInteractive directly (the same entry the CLI uses) with a
// temp AGY_PRO_HOME holding (or not holding) a pinning default preset.

// writeSeatLineupPresets writes a fanout-presets.json whose default_preset pins a
// seats lineup, or an empty file when seats is "".
func writeSeatLineupPresets(t *testing.T, home, seats string) {
	t.Helper()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv("AGY_PRO_FANOUT_PRESET", "")
	if seats == "" {
		return
	}
	body := `{"presets":[{"name":"nightly","task":"review","seats":"` + seats + `"}],"default_preset":"nightly"}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Case 1: an explicit --seats value outranks the pin. The vector carries it, and
// no notice claims otherwise (the FL-1 false-notice regression).
func TestSwarmDoorExplicitSeatsBeatPresetPin(t *testing.T) {
	home := t.TempDir()
	writeSeatLineupPresets(t, home, "native:preset-seat")

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
	echo := strings.TrimSpace(out.String())
	if !strings.Contains(echo, "--seats native:operator-seat") {
		t.Errorf("dry-run echo must carry the operator's seats:\n%s", echo)
	}
	if strings.Contains(echo, "preset-seat") {
		t.Errorf("the pin must not reach the vector when seats were passed:\n%s", echo)
	}
	if strings.Contains(errw.String(), "no seats were passed") {
		t.Errorf("stderr must not claim the preset supplies the lineup: %q", errw.String())
	}
	if !strings.HasPrefix(echo, "agy pro fanout") {
		t.Errorf("the fanout command must be the first thing on stdout:\n%s", echo)
	}
}

// Case 2: no --seats at all + a pin => the pin supplies the lineup, so the door no
// longer demands --seats; the vector omits the pair and the notice explains why.
func TestSwarmDoorPinSuppliesMissingSeats(t *testing.T) {
	home := t.TempDir()
	writeSeatLineupPresets(t, home, "native:preset-seat,prov/m1")

	var out, errw strings.Builder
	code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		prompt:   "check it",
		dryRun:   true,
	})
	if code != 0 {
		t.Fatalf("exit = %d; a pinning preset must satisfy the seat requirement; stderr:\n%s", code, errw.String())
	}
	echo := strings.TrimSpace(out.String())
	if strings.Contains(echo, "--seats") {
		t.Errorf("dry-run echo must omit --seats so the ladder resolves the pin:\n%s", echo)
	}
	if !strings.Contains(errw.String(), `seat lineup from fanout preset "nightly"`) ||
		!strings.Contains(errw.String(), "no seats were passed") {
		t.Errorf("stderr must state the pin supplies the lineup: %q", errw.String())
	}
}

// Case 3: no --seats and no pin => the historical exit 2 missing-option error.
func TestSwarmDoorMissingSeatsWithoutPinStillErrors(t *testing.T) {
	home := t.TempDir()
	writeSeatLineupPresets(t, home, "")

	var out, errw strings.Builder
	code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		prompt:   "check it",
		dryRun:   true,
	})
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (missing --seats, no pin to fall back on)", code)
	}
	if !strings.Contains(errw.String(), "missing required option(s): --seats") {
		t.Errorf("stderr must name the missing option: %q", errw.String())
	}
	if out.String() != "" {
		t.Errorf("stdout must stay empty on the usage error: %q", out.String())
	}
}

// Case 4: a preset that pins no seats does not satisfy the requirement.
func TestSwarmDoorUnpinnedPresetDoesNotSatisfySeats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv("AGY_PRO_FANOUT_PRESET", "")
	body := `{"presets":[{"name":"plain","task":"review"}],"default_preset":"plain"}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errw strings.Builder
	if code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		prompt:   "check it",
		dryRun:   true,
	}); code != 2 {
		t.Fatalf("exit = %d; want 2 (the preset pins no lineup)", code)
	}
}

// R3-item-2 (door layer): the non-interactive --dry-run echo is the vector an
// operator copies into a shell, so a credential-shaped seat token must not ride
// it raw. The masking lives in swarm.FormatCommand; this pins the DOOR path
// (flag parse -> resolveFlagSeats -> echo).
func TestSwarmDoorDryRunEchoMasksUnderscoreHeadSeatToken(t *testing.T) {
	home := t.TempDir()
	writeSeatLineupPresets(t, home, "")

	var out, errw strings.Builder
	code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
		template: "code-review",
		seats:    "native:gemini-a,nw/" + testfixtures.SKLive,
		prompt:   "check it",
		dryRun:   true,
	})
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, errw.String())
	}
	echo := out.String()
	if strings.Contains(echo, testfixtures.SKLive) {
		t.Errorf("dry-run echo leaks the underscore-head seat token raw:\n%s", echo)
	}
	if !strings.Contains(echo, "--seats native:gemini-a,nw/sk****") {
		t.Errorf("dry-run echo must carry the masked seat value:\n%s", echo)
	}
}

// Class-closure sweep (r4): the two --writer doors of `agy pro code` echo a seat
// TOKEN, and the door's selection-error re-wrap must mask the quoted token the
// inventory grammar echoed with the weaker authority.
func TestCodeWriterDoorMasksUnderscoreHeadToken(t *testing.T) {
	plant := testfixtures.SKLive
	for _, tc := range []struct{ name, seat string }{
		{name: "native writer (grammar-valid, provider-only refusal)", seat: "native:" + plant},
		{name: "malformed writer", seat: "native:" + plant + "/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errw strings.Builder
			if code := codeCheckWriterSeat(tc.seat, &errw); code != 2 {
				t.Fatalf("exit = %d; want 2 (usage error)", code)
			}
			if strings.Contains(errw.String(), plant) {
				t.Errorf("--writer refusal leaks the seat token raw:\n%s", errw.String())
			}
			if !strings.Contains(errw.String(), "****") {
				t.Errorf("--writer refusal must be masked:\n%s", errw.String())
			}
			if !strings.HasPrefix(errw.String(), "Error: --writer ") {
				t.Errorf("--writer refusal lost its prefix:\n%s", errw.String())
			}
		})
	}
	// A valid provider writer is accepted (nothing echoed).
	var errw strings.Builder
	if code := codeCheckWriterSeat("nw/glm-5.3-flash", &errw); code != -1 {
		t.Errorf("a provider writer must be accepted, got exit %d (%s)", code, errw.String())
	}
}

func TestSwarmDoorSelectionErrorMasksQuotedToken(t *testing.T) {
	home := t.TempDir()
	writeSeatLineupPresets(t, home, "")
	plant := testfixtures.SKLive

	// The selection arm needs the token to be SELECTION input: a saved preset with
	// empty Numbers makes the name-shaped token one, so the refusal quotes it.
	defer withStubSwarmPresets(t, []swarm.SavedPreset{{Name: plant, Numbers: "  "}})()

	for _, tc := range []struct {
		name     string
		seats    string
		wantCode int
	}{
		{name: "selection grammar", seats: plant, wantCode: 2},
		{name: "seat spec grammar", seats: "native:" + plant + "/x", wantCode: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errw strings.Builder
			code := runSwarmNonInteractive(strings.NewReader(""), &out, &errw, swarmFlagValues{
				template: "code-review",
				seats:    tc.seats,
				prompt:   "p",
				dryRun:   true,
			})
			if code != tc.wantCode {
				t.Fatalf("exit = %d; want %d\nstderr:\n%s", code, tc.wantCode, errw.String())
			}
			if strings.Contains(errw.String(), plant) {
				t.Errorf("door error leaks the seat token raw:\n%s", errw.String())
			}
			if !strings.Contains(errw.String(), "****") && !strings.Contains(errw.String(), "[REDACTED]") {
				t.Errorf("door error must be masked:\n%s", errw.String())
			}
		})
	}
}
