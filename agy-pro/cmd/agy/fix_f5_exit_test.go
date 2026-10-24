package main

// FIX-F5 pin (fix-list-7 row W5, exit-code half): the cross-run ledger refusal must
// reach the operator as the USAGE exit code (2) on every route.
//
// The library guarantees the CLASS (codework.ErrRefusedPlanScope) on both doors - the
// --scope gate and the --plan ingestion - and this pin holds the verb's mapping to
// that class: whatever route produced it, exit 2 with nothing run, never exit 1 as a
// snapshot failure.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"agypro/internal/codework"
)

func TestCrossRunRefusalMapsToUsageExitOnEveryRoute(t *testing.T) {
	// The exact shape both doors produce: a wrapped ErrRefusedPlanScope.
	refusal := fmt.Errorf("codework: write: %s: scope entry %q can claim %q, which is an existing run ledger: %w",
		"--plan unit scope", "reports/code/t1/**", "reports/code/t1", codework.ErrRefusedPlanScope)

	for _, route := range []struct {
		name string
		err  error
	}{
		{name: "--scope door", err: fmt.Errorf("codework: write: --scope[0]: scope entry %q can claim %q, which is an existing run ledger: %w", "reports/code/t1/**", "reports/code/t1", codework.ErrRefusedPlanScope)},
		{name: "--plan door", err: refusal},
	} {
		t.Run(route.name, func(t *testing.T) {
			if got := codeWriteExit(&codework.WritePhaseResult{}, route.err); got != 2 {
				t.Fatalf("codeWriteExit(%v) = %d; want 2 (usage: the invocation has to change)", route.err, got)
			}
			var errw bytes.Buffer
			if !codeRefused(route.err, &errw) {
				t.Fatalf("codeRefused did not treat the cross-run refusal as a usage-class refusal: %v", route.err)
			}
			if !strings.Contains(errw.String(), "existing run ledger") {
				t.Errorf("the printed refusal does not name the ledger: %q", errw.String())
			}
		})
	}

	// Non-vacuity: a non-usage failure still exits 1, so the mapping above is not a
	// constant.
	if got := codeWriteExit(&codework.WritePhaseResult{}, errors.New("boom")); got != 1 {
		t.Errorf("codeWriteExit(ordinary error) = %d; want 1", got)
	}
}
