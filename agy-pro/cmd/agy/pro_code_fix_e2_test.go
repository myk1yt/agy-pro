package main

// Pins for FIX-E2 at the VERB level: the two shapes the round-4 rows asked the
// operator to see named, before anything runs and before any consent prompt.
//
//   - a scope entry carrying Win32 stream syntax is refused with the named class
//     "Windows stream syntax not allowed in scope entries" (N8(g)), instead of a
//     downstream "outside unit scope" from the seat's own write attempt;
//   - a CASE-VARIANT ledger scope is refused pre-consent with exit 2 (N4), the same
//     code the byte-identical spelling already produced.
//
// Every case stubs codework.RunFanout, so nothing spawns a seat and no provider is
// charged.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCodeWriteStreamScopeNamesTheReasonClass is the verb half of N8(g): the scope
// AUTHORING refusal (BuildPlan / RefuseScopeEntries) is where the operator is told
// the class, and it is a usage error before any plan, snapshot or seat exists.
func TestCodeWriteStreamScopeNamesTheReasonClass(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the stream arm is Windows-gated (the guard's own arm is host-gated the same way)")
	}
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "marker5/AGENTS.md:evil",
		"--writer", "nw/glm-5.3-flash", "--yes", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("stream-shaped scope exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused stream scope spawned %d fanout call(s); want 0", len(*calls))
	}
	for _, want := range []string{
		"Windows stream syntax not allowed in scope entries",
		"Win32 alternate-data-stream separator",
		"--scope[0]",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "outside unit scope") {
		t.Errorf("the authoring refusal borrowed the seat-level message:\n%s", stderr)
	}
	if strings.Contains(stderr, "metered seat(s)") || strings.Contains(stderr, "consent:") {
		t.Errorf("the door asked for consent for a scope it refuses:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "write")); err == nil {
		t.Error("a refused stream scope still created the write run directory")
	}

	// Non-vacuity: the same tree with the benign prefix of that scope is accepted.
	dryCode, _, dryErr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "marker5/",
		"--writer", "nw/glm-5.3-flash", "--yes", "--dry-run", "--root", root, "--out", out)
	if dryCode != 0 {
		t.Fatalf("benign scope beside the same tree exit = %d; want 0\nstderr:\n%s", dryCode, dryErr)
	}
}

// TestCodeWriteCaseVariantLedgerScopeExitsTwo is the verb half of N4: the mirror
// folds like the snapshot authority, so a scope spelled in the other case is
// refused BEFORE the consent prompt instead of failing the snapshot after it (which
// was exit 1, not the documented 2).
func TestCodeWriteCaseVariantLedgerScopeExitsTwo(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the fold follows the host's filesystem: only a case-insensitive host merges the two spellings")
	}
	withCodePhaseStreams(t)
	root, _ := codeRoot(t)
	out := filepath.Join(root, "reports", "code", "run-e2")
	calls := withCodeFanout(t, nil)

	code, stdout, stderr := codeInvoke(t, nil,
		"write", "--ask", "implement x", "--scope", "RePoRtS/**",
		"--writer", "nw/glm-5.3-flash", "--yes", "--root", root, "--out", out)
	if code != 2 {
		t.Fatalf("case-variant ledger scope exit = %d; want 2 (the pre-consent mirror must fold, N4)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused case-variant ledger scope spawned %d fanout call(s); want 0", len(*calls))
	}
	for _, want := range []string{"run directory", "before any consent prompt", "--scope[0]"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "metered seat(s)") || strings.Contains(stderr, "consent:") {
		t.Errorf("the door asked for consent for a scope it refuses:\n%s", stderr)
	}
}
