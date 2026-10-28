package main

// Pins for the FIX-D2 rows at the VERB level (R3-9): the composed `--ask --scope`
// route refuses a governed / non-canonical / root-wide / ledger-claiming scope
// BEFORE it authors a plan and before any consent prompt, and the refusal is the
// usage exit code (2) - the same code the `--plan` route already produces. Before
// this unit the composed route exited 1 (the charm-glm observation), because the
// refusal travelled as a plain BuildPlan/WritePhase error.
//
// Every case stubs codework.RunFanout, so nothing spawns a seat and no provider is
// charged.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodeWriteComposedGovernedScopeExitsTwo is the composed-route half of R3-9:
// `write --ask x --scope rules/... --writer ...` was exit 1 and is now exit 2,
// with nothing run and no consent prompt printed.
func TestCodeWriteComposedGovernedScopeExitsTwo(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "rules/team.md",
		"--writer", "nw/glm-5.3-flash", "--yes", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("composed governed scope exit = %d; want 2 (was 1 before FIX-D2)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused composed scope spawned %d fanout call(s); want 0", len(*calls))
	}
	for _, want := range []string{"rules/team.md", "governance/credential path", "--scope[0]"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}
	// Pre-consent: no metered echo, no consent line, no run directory.
	if strings.Contains(stderr, "metered seat(s)") || strings.Contains(stderr, "consent:") {
		t.Errorf("the door asked for consent for a scope it refuses:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "write")); err == nil {
		t.Error("a refused composed scope still created the write run directory")
	}
}

// TestCodeRunComposedGovernedScopeExitsTwo is the same rule on the composed `run`
// door, and it must fire BEFORE the plan phase (whose seats may already snapshot
// the scope).
func TestCodeRunComposedGovernedScopeExitsTwo(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"run", "--ask", "implement x", "--scope", "settings.json",
		"--writer", "nw/glm-5.3-flash", "--yes", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("composed governed scope (run) exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused composed scope ran %d fanout call(s) (the plan phase must not start); want 0", len(*calls))
	}
	if !strings.Contains(stderr, "governance/credential path") {
		t.Errorf("refusal does not name the class:\n%s", stderr)
	}
}

// TestCodeWriteComposedLedgerScopeExitsTwo pins the ledger half of R3-9 at the
// verb: a --scope that can claim the run directory is a usage error before
// anything runs, on both doors. The run directory here lives INSIDE the root,
// which is the shape that makes the ledger reachable.
func TestCodeWriteComposedLedgerScopeExitsTwo(t *testing.T) {
	withCodePhaseStreams(t)
	root, _ := codeRoot(t)
	out := filepath.Join(root, "reports", "code", "run-1")
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "reports/code/*",
		"--writer", "nw/glm-5.3-flash", "--yes", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("ledger scope exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused ledger scope spawned %d fanout call(s); want 0", len(*calls))
	}
	for _, want := range []string{"run directory", "before the snapshot"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}

	// Non-vacuity: the same tree, the same run directory, a bounded scope beside
	// the ledger is still accepted (dry run, so nothing is spent).
	dryCode, dryOut, dryErr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "pkg/a.go",
		"--writer", "nw/glm-5.3-flash", "--yes", "--dry-run", "--root", root, "--out", out)
	if dryCode != 0 {
		t.Fatalf("benign composed scope exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", dryCode, dryOut, dryErr)
	}
	if !strings.Contains(dryErr, "scope: pkg/a.go") {
		t.Errorf("the dry-run echo lost the effective scope:\n%s", dryErr)
	}
}

// TestCodeWriteComposedDryRunAlsoRefuses pins that --dry-run does not become a way
// to print a vector for a scope the phase would refuse: the gate sits before the
// dry-run branch of WritePhase and before the verb's own echo.
func TestCodeWriteComposedDryRunAlsoRefuses(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	withCodeFanout(t, nil)

	code, _, stderr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "AGENTS.md.",
		"--writer", "nw/glm-5.3-flash", "--yes", "--dry-run", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("dry-run with a non-canonical scope exit = %d; want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "path component") {
		t.Errorf("refusal does not name the canonical-form rule:\n%s", stderr)
	}
	if strings.Contains(stderr, "fanout:") {
		t.Errorf("a refused dry run still echoed a vector:\n%s", stderr)
	}
}
