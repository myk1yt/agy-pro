package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// U2a spec 3/4/6: the drift policy split.
//
//   - a PRESET-sourced lineup skips a seat that no longer resolves (one warning
//     per seat, rest of the lineup runs, summary counts the loss);
//   - an explicit --seats lineup keeps the hard error;
//   - an all-drifted preset lineup fails with ONE message naming the preset.

// driftFixture builds a one-model registry reached over an httptest server that
// answers every seat with a findings-bearing review, so a surviving seat counts
// as completed rather than thin.
func driftFixture(t *testing.T) (regPath, outDir string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := ChatResponse{Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role: "assistant",
				Content: `## Findings
- **[HIGH]** Nil Pointer Dereference: Missing nil check on context (Location: ctx.go:10)
`,
			},
			FinishReason: "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("DRIFT_KEY", "drift-key")

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "prov-a",
		BaseURL:   server.URL,
		APIKeyEnv: "DRIFT_KEY",
		Models: []providers.Model{{
			ID: "model-a", Family: "family-a", Enabled: &trueVal,
			Capabilities: providers.Capabilities{SupportsTools: true},
		}},
	}}}
	data, _ := json.Marshal(reg)
	regPath = filepath.Join(tmpDir, "providers.json")
	if err := os.WriteFile(regPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return regPath, filepath.Join(tmpDir, "reports")
}

// driftConfig builds the engine config for one drift case. origin is the preset
// origin the run carries: a STANDING one (default_preset rung) is the only shape
// that tolerates drift (FL-3), an empty origin is an explicit --seats lineup, and
// a NAMED-preset origin (flag/env rung) is ad-hoc and also hard-errors.
func driftConfig(regPath, outDir string, specs []SeatSpec, origin SeatLineupOrigin, warn *bytes.Buffer) RunConfig {
	return RunConfig{
		Task:             "review",
		Prompt:           "Audit this module",
		N:                len(specs),
		Root:             filepath.Dir(regPath),
		OutDir:           outDir,
		RegistryPath:     regPath,
		TimeoutSec:       15,
		Seats:            specs,
		SeatLineup:       origin,
		WarnOut:          warn,
	}
}

// Mid-list drift: seat 2 of a 2-seat preset lineup no longer resolves. The run
// continues with the surviving seat, the warning names the seat and the preset,
// and the summary refuses to report a full quorum.
func TestPresetLineupDriftSkipsSeatAndCountsIt(t *testing.T) {
	regPath, outDir := driftFixture(t)
	specs, err := ParseSeatSpecs("prov-a/model-a,prov-a/removed-model")
	if err != nil {
		t.Fatal(err)
	}
	var warn, progress bytes.Buffer
	cfg := driftConfig(regPath, outDir, specs, SeatLineupOrigin{Preset: "nightly", Rung: RungDefaultPreset}, &warn)
	cfg.ProgressOut = &progress

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("a drifted preset seat must not fail the run: %v", err)
	}
	if summary.NCompleted != 1 {
		t.Errorf("NCompleted = %d; want 1 (the surviving seat)", summary.NCompleted)
	}
	if summary.NRequested != 2 {
		t.Errorf("NRequested = %d; want 2 (the preset's original lineup)", summary.NRequested)
	}
	if summary.SkippedSeats != 1 {
		t.Errorf("SkippedSeats = %d; want 1", summary.SkippedSeats)
	}
	if got := strings.Join(summary.SkippedSeatSpecs, ","); got != "prov-a/removed-model" {
		t.Errorf("SkippedSeatSpecs = %q; want the dropped token", got)
	}
	if !summary.Degraded {
		t.Error("a run that lost a seat must report degraded=true")
	}
	if len(summary.Seats) != 1 {
		t.Errorf("seats in summary = %d; want 1", len(summary.Seats))
	}

	// The per-seat warning names the ORIGINAL lineup position and the preset.
	wantWarn := `seat 2 (prov-a/removed-model) from preset "nightly" is no longer available`
	if !strings.Contains(warn.String(), wantWarn) {
		t.Errorf("stderr = %q; want it to contain %q", warn.String(), wantWarn)
	}

	// The streaming line lands on the sink AND in progress.log, so a tailing
	// agent sees the loss too. It names the preset's lineup position, never the
	// run's renumbered seat ids.
	wantLine := `[preset "nightly" seat 2/2] prov-a/removed-model skipped (no longer available)`
	if !strings.Contains(progress.String(), wantLine) {
		t.Errorf("progress sink = %q; want %q", progress.String(), wantLine)
	}
	logData, rerr := os.ReadFile(filepath.Join(outDir, progressFileName))
	if rerr != nil {
		t.Fatalf("read progress.log: %v", rerr)
	}
	if !strings.Contains(string(logData), wantLine) {
		t.Errorf("progress.log = %q; want %q", string(logData), wantLine)
	}

	// summary.json carries the count so no machine consumer reads a clean run.
	raw, rerr := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if rerr != nil {
		t.Fatalf("read summary.json: %v", rerr)
	}
	var decoded map[string]any
	if uerr := json.Unmarshal(raw, &decoded); uerr != nil {
		t.Fatalf("summary.json is not JSON: %v", uerr)
	}
	if got, ok := decoded["skipped_seats"]; !ok || got != float64(1) {
		t.Errorf("summary.json skipped_seats = %v (present %v); want 1", got, ok)
	}
	if got, ok := decoded["degraded"]; !ok || got != true {
		t.Errorf("summary.json degraded = %v; want true", got)
	}
	// The consensus denominator keeps the requested count.
	if got := decoded["n_requested"]; got != float64(2) {
		t.Errorf("summary.json n_requested = %v; want 2", got)
	}
	// reconciliation.md names the skipped seat under that denominator.
	recon, rerr := os.ReadFile(filepath.Join(outDir, "reconciliation.md"))
	if rerr != nil {
		t.Fatalf("read reconciliation.md: %v", rerr)
	}
	if !strings.Contains(string(recon), "**Skipped Seats:** `1 / 2` (prov-a/removed-model)") {
		t.Errorf("reconciliation.md must name the skipped seat; got:\n%s", string(recon))
	}
}

// The same lineup as an EXPLICIT --seats pick keeps the hard error: the operator
// named that exact seat on the command line.
func TestExplicitLineupDriftStillHardErrors(t *testing.T) {
	regPath, outDir := driftFixture(t)
	specs, err := ParseSeatSpecs("prov-a/model-a,prov-a/removed-model")
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	_, err = Execute(context.Background(), driftConfig(regPath, outDir, specs, SeatLineupOrigin{}, &warn))
	if err == nil {
		t.Fatal("an explicit --seats lineup must hard-error on an unresolvable model")
	}
	if !strings.Contains(err.Error(), `failed to resolve model "prov-a/removed-model"`) {
		t.Errorf("error = %v; want the pre-U2a resolve failure", err)
	}
	if warn.Len() != 0 {
		t.Errorf("no drift warning expected on the explicit path; got %q", warn.String())
	}
}

// Every seat drifted: one clear failure naming the preset, not a cascade.
func TestAllPresetSeatsDriftedFailsNamingPreset(t *testing.T) {
	regPath, outDir := driftFixture(t)
	specs, err := ParseSeatSpecs("prov-a/gone-one,prov-a/gone-two")
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	_, err = Execute(context.Background(), driftConfig(regPath, outDir, specs, SeatLineupOrigin{Preset: "nightly", Rung: RungDefaultPreset}, &warn))
	if err == nil {
		t.Fatal("an all-drifted preset lineup must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, `preset "nightly"`) {
		t.Errorf("error = %q; want it to name the preset", msg)
	}
	if !strings.Contains(msg, "none of them") {
		t.Errorf("error = %q; want the single all-skipped sentence", msg)
	}
	// Both seats are announced before the failure, so the operator sees WHY.
	if got := strings.Count(warn.String(), "is no longer available"); got != 2 {
		t.Errorf("warnings = %d; want 2 (one per drifted seat): %q", got, warn.String())
	}
}

// A preset lineup that fully resolves reports no loss: summary stays clean and
// keeps its pre-U2a shape (omitempty fields absent).
func TestCleanPresetLineupReportsNoLoss(t *testing.T) {
	regPath, outDir := driftFixture(t)
	specs, err := ParseSeatSpecs("prov-a/model-a")
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	summary, err := Execute(context.Background(), driftConfig(regPath, outDir, specs, SeatLineupOrigin{Preset: "nightly", Rung: RungDefaultPreset}, &warn))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if summary.SkippedSeats != 0 || len(summary.SkippedSeatSpecs) != 0 {
		t.Errorf("skipped = %d/%q; want none", summary.SkippedSeats, summary.SkippedSeatSpecs)
	}
	if summary.Degraded {
		t.Error("a fully resolving preset lineup must not report degraded")
	}
	if summary.NRequested != 1 || summary.NCompleted != 1 {
		t.Errorf("counts = %d/%d; want 1/1", summary.NCompleted, summary.NRequested)
	}
	raw, rerr := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if rerr != nil {
		t.Fatalf("read summary.json: %v", rerr)
	}
	if strings.Contains(string(raw), "skipped_seats") {
		t.Errorf("summary.json must omit skipped_seats on a clean run:\n%s", string(raw))
	}
}

// The CLI-level console block names the lost seats (spec 4: never a
// full-quorum reading).
func TestConsoleNamesSkippedSeats(t *testing.T) {
	var out bytes.Buffer
	summary := &SummaryReport{
		Task:             "review",
		NRequested:       3,
		NCompleted:       2,
		Degraded:         true,
		SkippedSeats:     1,
		SkippedSeatSpecs: []string{"prov-a/removed-model"},
	}
	printSkippedSeats(&out, summary)
	want := "Skipped Seats:         1 (prov-a/removed-model)\n"
	if out.String() != want {
		t.Errorf("console line = %q; want %q", out.String(), want)
	}
	// Nothing when no seat was lost.
	out.Reset()
	printSkippedSeats(&out, &SummaryReport{Task: "review", NRequested: 2, NCompleted: 2})
	if out.String() != "" {
		t.Errorf("console must stay silent without skips; got %q", out.String())
	}
}
