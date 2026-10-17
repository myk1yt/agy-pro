package fanout

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agypro/internal/paths"
)

// B7 pins (2026-10-17): partition-mode timeout scaling. A partition run
// (--units, no explicit --timeout) must get a default deadline that grows
// with the unit count (300s base + 120s per unit, capped at 3600s), an
// explicit --timeout must win exactly, and a cross-verification run must keep
// the flat 300s default byte-for-byte. Mutation-verified: changing
// partitionTimeoutPerUnitSec, partitionMaxTimeoutSec, or the buildRunConfig
// unset condition fails these tests.

// partitionTestUnits builds n syntactically valid, pairwise-disjoint units
// (plain literal directory scopes, no globs) for withDefaults pins.
func partitionTestUnits(n int) []Unit {
	units := make([]Unit, 0, n)
	for i := 1; i <= n; i++ {
		units = append(units, Unit{
			ID:          fmt.Sprintf("u%d", i),
			Title:       fmt.Sprintf("Unit %d", i),
			Scope:       []string{fmt.Sprintf("pkg/u%d", i)},
			Deliverable: "report",
		})
	}
	return units
}

// The scaled default formula itself: base + per-unit, clamped.
func TestPartitionDefaultTimeoutSecFormula(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want int
	}{
		{1, 420},    // 300 + 120
		{2, 540},    // 300 + 240
		{3, 660},    // 300 + 360
		{5, 900},    // 300 + 600
		{27, 3540},  // 300 + 3240, just under the clamp
		{28, 3600},  // 300 + 3360 = 3660 -> clamped
		{100, 3600}, // far past the clamp
	} {
		if got := partitionDefaultTimeoutSec(tc.n); got != tc.want {
			t.Errorf("partitionDefaultTimeoutSec(%d) = %d; want %d", tc.n, got, tc.want)
		}
	}
}

// withDefaults: partition run without a budget gets the scaled default; an
// explicit budget is never overridden; a budget-less cross-verification run
// keeps the flat 300s default.
func TestWithDefaultsScalesPartitionTimeout(t *testing.T) {
	cfg := RunConfig{Units: partitionTestUnits(3)}.withDefaults()
	if cfg.TimeoutSec != 660 {
		t.Errorf("partition withDefaults(3 units).TimeoutSec = %d; want 660 (300 + 120*3)", cfg.TimeoutSec)
	}
	cfg = RunConfig{Units: partitionTestUnits(1)}.withDefaults()
	if cfg.TimeoutSec != 420 {
		t.Errorf("partition withDefaults(1 unit).TimeoutSec = %d; want 420 (300 + 120)", cfg.TimeoutSec)
	}
	cfg = RunConfig{Units: partitionTestUnits(100)}.withDefaults()
	if cfg.TimeoutSec != partitionMaxTimeoutSec {
		t.Errorf("partition withDefaults(100 units).TimeoutSec = %d; want the %ds clamp", cfg.TimeoutSec, partitionMaxTimeoutSec)
	}
	cfg = RunConfig{Units: partitionTestUnits(3), TimeoutSec: 900}.withDefaults()
	if cfg.TimeoutSec != 900 {
		t.Errorf("withDefaults overrode an explicit partition budget: got %d; want 900", cfg.TimeoutSec)
	}
	cfg = RunConfig{}.withDefaults()
	if cfg.TimeoutSec != DefaultTimeoutSec {
		t.Errorf("cross-verification withDefaults().TimeoutSec = %d; want the flat %ds default", cfg.TimeoutSec, DefaultTimeoutSec)
	}
}

// writePartitionPlan writes a valid disjoint plan of n units and returns its
// path.
func writePartitionPlan(t *testing.T, n int) string {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("[\n")
	for i := 1; i <= n; i++ {
		sep := ","
		if i == n {
			sep = ""
		}
		fmt.Fprintf(&b, `  {"id":"u%d","title":"Unit %d","scope":["pkg/u%d"],"deliverable":"report"}%s`+"\n", i, i, i, sep)
	}
	b.WriteString("]\n")
	p := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return p
}

// CLI seam: --units without an explicit --timeout binds the scaled default to
// the engine config; an explicit --timeout wins exactly; and the plain
// cross-verification default stays 300 at the same boundary.
func TestRunUnitsTimeoutDefaultScalesAndExplicitWins(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())

	var got RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = cfg
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	runUnits := func(args ...string) RunConfig {
		t.Helper()
		var out, errw bytes.Buffer
		full := append([]string{"-json"}, args...)
		if code := Run(full, nil, &out, &errw); code != 0 {
			t.Fatalf("Run(%v) exit = %d, stderr = %s", full, code, errw.String())
		}
		return got
	}

	plan3 := writePartitionPlan(t, 3)
	cfg := runUnits("-units", plan3)
	if cfg.TimeoutSec != 0 {
		t.Errorf("--units (3 units, no --timeout) bound TimeoutSec = %d; want 0 (unset = engine-scaled)", cfg.TimeoutSec)
	}
	// Composition pin: the config the CLI hands to the engine, defaulted, is
	// the scaled deadline (assert the computed value, not just "nonzero").
	if scaled := cfg.withDefaults().TimeoutSec; scaled != 660 {
		t.Errorf("CLI partition config withDefaults().TimeoutSec = %d; want 660 (300 + 120*3)", scaled)
	}
	if cfg := runUnits("-units", plan3, "-timeout", "900"); cfg.TimeoutSec != 900 {
		t.Errorf("--units --timeout 900 bound TimeoutSec = %d; want 900 (explicit override wins)", cfg.TimeoutSec)
	}
	// --timeout 300 spelled out explicitly must also win over the scaled
	// default (5 units would scale to 900).
	plan5 := writePartitionPlan(t, 5)
	if cfg := runUnits("-units", plan5, "-timeout", "300"); cfg.TimeoutSec != 300 {
		t.Errorf("--units --timeout 300 bound TimeoutSec = %d; want 300 (explicit wins over the 900 scaled default)", cfg.TimeoutSec)
	}
	// Cross-verification at the same CLI boundary: flat 300, byte-for-byte.
	var out, errw bytes.Buffer
	if code := Run([]string{"-prompt", "p", "-json"}, nil, &out, &errw); code != 0 {
		t.Fatalf("Run(cross-verification) exit = %d, stderr = %s", code, errw.String())
	}
	if got.TimeoutSec != 300 {
		t.Errorf("cross-verification default bound TimeoutSec = %d; want 300", got.TimeoutSec)
	}
}
