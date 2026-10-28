package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// R2-1 (P1 security): the underscore-head credential class (`sk_live_...`) is
// identity under MaskOperand, so every seat/models TOKEN echo has to go through
// the registry-slot authority (providers.MaskSlot, wrapped by MaskSeatToken /
// MaskSeatTokens). One planted test per sink, so a future edit cannot quietly
// re-introduce the raw token on any of them.

// plantedSpec returns a provider seat spec carrying the underscore-head plant and
// the sentinel that must never appear in output.
func plantedSpec() (spec, plant string) {
	return "nw/" + testfixtures.SKLive, testfixtures.SKLive
}

// Sink 1: the run-level lineup echo (buildRunConfig/Run path).
func TestSeatLineupEchoMasksUnderscoreHeadTokens(t *testing.T) {
	home, stderr := ladderEnv(t)
	seed := "native:gemini-a," + plantedSpec2()
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: seed}},
		DefaultPreset: "nightly",
	})

	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	echo := stderr.String()
	_, plant := plantedSpec()
	if strings.Contains(echo, plant) {
		t.Errorf("lineup echo leaks the underscore-head token raw: %q", echo)
	}
	if !strings.Contains(echo, "seat lineup from preset \"nightly\"") {
		t.Fatalf("echo line missing: %q", echo)
	}
	// Benign tokens stay readable.
	if !strings.Contains(echo, "native:gemini-a") {
		t.Errorf("benign native seat must stay readable: %q", echo)
	}
}

// plantedSpec2 is the provider token used by the sink tests that need a lineup
// with a plant in a later position.
func plantedSpec2() string { return "nw/" + testfixtures.SKLive }

// Sink 2 + 3: the drift-skip warning, the progress line and the summary's
// skipped_seat_specs (engine path, standing origin).
func TestDriftSkipSurfacesMaskUnderscoreHeadTokens(t *testing.T) {
	regPath, outDir := driftFixture(t)
	specs, err := ParseSeatSpecs("prov-a/model-a," + plantedSpec2())
	if err != nil {
		t.Fatal(err)
	}
	var warn, progress bytes.Buffer
	cfg := driftConfig(regPath, outDir, specs, SeatLineupOrigin{Preset: "nightly", Rung: RungDefaultPreset}, &warn)
	cfg.ProgressOut = &progress

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_, plant := plantedSpec()

	if strings.Contains(warn.String(), plant) {
		t.Errorf("drift warning leaks the token raw: %q", warn.String())
	}
	if !strings.Contains(warn.String(), "is no longer available; skipping it") {
		t.Fatalf("drift warning missing: %q", warn.String())
	}
	if strings.Contains(progress.String(), plant) {
		t.Errorf("progress line leaks the token raw: %q", progress.String())
	}
	logData, rerr := os.ReadFile(filepath.Join(outDir, progressFileName))
	if rerr != nil {
		t.Fatalf("read progress.log: %v", rerr)
	}
	if strings.Contains(string(logData), plant) {
		t.Errorf("progress.log leaks the token raw: %q", string(logData))
	}

	// Shareable artifact: every byte of summary.json must be clean.
	raw, rerr := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if rerr != nil {
		t.Fatalf("read summary.json: %v", rerr)
	}
	if strings.Contains(string(raw), plant) {
		t.Errorf("summary.json leaks the token raw")
	}
	var decoded struct {
		SkippedSeats     int      `json:"skipped_seats"`
		SkippedSeatSpecs []string `json:"skipped_seat_specs"`
	}
	if uerr := json.Unmarshal(raw, &decoded); uerr != nil {
		t.Fatalf("summary.json: %v", uerr)
	}
	if decoded.SkippedSeats != 1 || len(decoded.SkippedSeatSpecs) != 1 {
		t.Fatalf("skipped = %d/%v; want one masked token", decoded.SkippedSeats, decoded.SkippedSeatSpecs)
	}
	if decoded.SkippedSeatSpecs[0] == "nw/"+plant || !strings.Contains(decoded.SkippedSeatSpecs[0], "****") {
		t.Errorf("summary skipped spec = %q; want a masked token", decoded.SkippedSeatSpecs[0])
	}
	// The console banner row reads the same masked value.
	var banner bytes.Buffer
	printSkippedSeats(&banner, summary)
	if strings.Contains(banner.String(), plant) {
		t.Errorf("banner row leaks the token raw: %q", banner.String())
	}

	// Sanity: the summary struct is the source of the banner row.
	if summary.SkippedSeatSpecs[0] != decoded.SkippedSeatSpecs[0] {
		t.Errorf("struct/file disagree: %q vs %q", summary.SkippedSeatSpecs[0], decoded.SkippedSeatSpecs[0])
	}
}

// Sink 4: the hard error on an unresolvable explicit seat token (--seats).
func TestResolveErrorMasksUnderscoreHeadToken(t *testing.T) {
	regPath, outDir := driftFixture(t)
	specs, err := ParseSeatSpecs(plantedSpec2())
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	_, err = Execute(context.Background(), driftConfig(regPath, outDir, specs, SeatLineupOrigin{}, &warn))
	if err == nil {
		t.Fatal("an explicit unresolvable seat must hard-error")
	}
	_, plant := plantedSpec()
	if strings.Contains(err.Error(), plant) {
		t.Errorf("resolve error leaks the token raw: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to resolve model") {
		t.Errorf("resolve error lost its message: %v", err)
	}
}

// Sink 6: the seats-hash provenance notices (R2-3 wordings) never echo the
// lineup, and the preset name is a slot (MaskSlot).
func TestProvenanceNoticesAreMaskedAndNeverBlock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sha       string
		wantWords string
	}{
		{name: "no marker", sha: "", wantWords: "NO provenance marker"},
		{name: "stale marker", sha: "deadbeef", wantWords: "stored hash mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			body := `{"presets":[{"name":"nightly","seats":"` + plantedSpec2() + `","seats_sha256":"` + tc.sha + `"}],"default_preset":"nightly"}`
			if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := buildRunConfig(ladderFlags(stderr), "p")
			if err != nil {
				t.Fatalf("an advisory notice must never fail the run: %v", err)
			}
			if len(cfg.Seats) != 1 {
				t.Errorf("seats = %d; want the on-disk lineup still applied", len(cfg.Seats))
			}
			out := stderr.String()
			if !strings.Contains(out, tc.wantWords) || !strings.Contains(out, "advisory") {
				t.Errorf("notice must state %q and that it is advisory: %q", tc.wantWords, out)
			}
			_, plant := plantedSpec()
			if strings.Contains(out, plant) {
				t.Errorf("provenance notice leaks the lineup token raw: %q", out)
			}
		})
	}
}

// R2-4: the provenance notice prints ONLY when the preset's seats pin is the
// value the run adopts. With an explicit --seats (flag wins) there is nothing to
// warn about, so stderr must stay silent about the preset's marker.
func TestStaleMarkerNoticeSilentWhenFlagSeatsWin(t *testing.T) {
	home, stderr := ladderEnv(t)
	body := `{"presets":[{"name":"nightly","seats":"native:preset-seat","seats_sha256":"deadbeef"}],"default_preset":"nightly"}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f := ladderFlags(stderr)
	f.set["seats"] = true
	f.seats = "native:flag-seat"
	cfg, err := buildRunConfig(f, "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:flag-seat" {
		t.Fatalf("seats = %q; want the flag seats", got)
	}
	if strings.Contains(stderr.String(), "provenance marker") || strings.Contains(stderr.String(), "stored hash mismatch") {
		t.Errorf("no provenance notice is due when the flag wins: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "seat lineup from preset") {
		t.Errorf("no lineup echo is due when the flag wins: %q", stderr.String())
	}

	// Control: the same record without --seats DOES warn (the notice is real).
	stderr.Reset()
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if !strings.Contains(stderr.String(), "stored hash mismatch") {
		t.Errorf("the control run must print the advisory: %q", stderr.String())
	}
}

// R2-4 companion: a --units partition run drops the seats pin in the F1 guard, so
// the provenance notice must not print there either.
func TestStaleMarkerNoticeSilentUnderUnits(t *testing.T) {
	home, stderr := ladderEnv(t)
	body := `{"presets":[{"name":"nightly","seats":"native:preset-seat","seats_sha256":"deadbeef"}],"default_preset":"nightly"}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(home, "plan.json")
	if err := os.WriteFile(plan, []byte(validPlanBody), 0o600); err != nil {
		t.Fatal(err)
	}
	f := ladderFlags(stderr)
	f.units = plan
	f.set["units"] = true
	// --units refuses a prompt alongside the plan, so the partition run carries none.
	cfg, err := buildRunConfig(f, "")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if len(cfg.Seats) != 0 {
		t.Fatalf("seats = %d; --units owns the seat source", len(cfg.Seats))
	}
	if strings.Contains(stderr.String(), "provenance marker") || strings.Contains(stderr.String(), "stored hash mismatch") {
		t.Errorf("no provenance notice is due when --units drops the pin: %q", stderr.String())
	}
}
