package codework

// FIX-F5 pin (fix-list-7 row W5): the CROSS-RUN ledger rule must fire on the
// PLAN-BYTES door too, at the same pre-consent moment the --scope door does.
//
// The chain is the same one Q1 closed, reached through the other route: a --plan
// document whose UNIT scope names a prior run's directory (reports/code/t1/**) passed
// refuseIngestedPlanScopes (which only ran the per-entry arms), the operator answered
// the y/N prompt, and only codesnap.Take refused afterwards - as a snapshot failure
// with the runtime exit code instead of the usage exit 2, i.e. the R3-9 timing
// invariant half-applied to the one route that had not been given the arm.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
	"agypro/internal/fanout"
)

func TestPlanIngestRouteRefusesAPriorLedgerBeforeConsent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeCodeworkTree(t, root, map[string]string{"src/main.go": "package main\n"})
	t1 := filepath.Join(root, "reports", "code", "t1")
	if _, err := codesnap.Take(root, []string{"src/**"}, t1); err != nil {
		t.Fatalf("plant the prior ledger: %v", err)
	}
	outDir := filepath.Join(root, "reports", "code", "t2")

	for _, scope := range []string{"reports/code/t1/**", "reports/code/t1", "reports/code/t1/manifest.json"} {
		rec := installPhaseRecorder(t, nil)
		// The plan's UNIT scope is the only place the prior ledger is named; the
		// operator's own --scope stays clear of it.
		_, err := WritePhase(CodeConfig{
			Root:       root,
			OutDir:     outDir,
			MaxRepairs: 0,
			Scope:      []string{"src/**"},
		}, handWrittenPlanDoc(scope))
		if !errors.Is(err, ErrRefusedPlanScope) {
			t.Fatalf("--plan unit scope %q: error = %v; want ErrRefusedPlanScope (pre-consent, exit 2)", scope, err)
		}
		if errors.Is(err, ErrConsentRequired) {
			t.Fatalf("--plan unit scope %q reached the consent gate before refusing: %v", scope, err)
		}
		if !strings.Contains(err.Error(), "existing run ledger") {
			t.Errorf("the plan-route refusal for %q does not name the prior ledger: %v", scope, err)
		}
		if !strings.Contains(err.Error(), "--plan unit scope") {
			t.Errorf("the plan-route refusal for %q does not name the route it came from: %v", scope, err)
		}
		if len(rec.calls) != 0 {
			t.Errorf("--plan unit scope %q: engine calls %v; nothing may run before the refusal", scope, rec.calls)
		}
	}

	// Non-vacuity: the same route with a unit scope that cannot reach the ledger
	// proceeds to the consent gate (which refuses a non-TTY run - the point is that it
	// is the CONSENT gate, not the scope gate, that stops it).
	rec := installPhaseRecorder(t, nil)
	_, cerr := WritePhase(CodeConfig{
		Root:       root,
		OutDir:     outDir,
		MaxRepairs: 0,
		Scope:      []string{"src/**"},
	}, handWrittenPlanDoc("src/**"))
	if errors.Is(cerr, ErrRefusedPlanScope) {
		t.Fatalf("a plan scope clear of every ledger was refused as a scope error: %v", cerr)
	}
	if !errors.Is(cerr, ErrConsentRequired) {
		t.Fatalf("want ErrConsentRequired for a clean plan on a non-TTY run, got %v", cerr)
	}
	if len(rec.calls) != 0 {
		t.Errorf("engine calls %v; a refused consent runs nothing", rec.calls)
	}
}

// TestBothDoorsRefuseTheCrossRunGrantAsUsage is the exit-2 shape on BOTH routes: the
// --scope door (RefuseScopeEntries) and the --plan door (WritePhase's ingestion) must
// produce the same error CLASS, because that class is what the verb maps to exit 2.
func TestBothDoorsRefuseTheCrossRunGrantAsUsage(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeCodeworkTree(t, root, map[string]string{"src/main.go": "package main\n"})
	t1 := filepath.Join(root, "reports", "code", "t1")
	if _, err := codesnap.Take(root, []string{"src/**"}, t1); err != nil {
		t.Fatalf("plant the prior ledger: %v", err)
	}
	outDir := filepath.Join(root, "reports", "code", "t2")
	const scope = "reports/code/t1/**"

	scopeErr := RefuseScopeEntries(root, outDir, []string{scope}, "--scope")
	if !errors.Is(scopeErr, ErrRefusedPlanScope) {
		t.Fatalf("--scope door error = %v; want ErrRefusedPlanScope", scopeErr)
	}
	planErr := refuseIngestedPlanScopes(root, outDir, unitsFromPlan(t, scope))
	if !errors.Is(planErr, ErrRefusedPlanScope) {
		t.Fatalf("--plan door error = %v; want ErrRefusedPlanScope", planErr)
	}
	for _, e := range []error{scopeErr, planErr} {
		if !errors.Is(e, ErrRefusedPlanScope) {
			t.Errorf("%v is not ErrRefusedPlanScope, so the verb cannot map it to exit 2", e)
		}
	}
}

// unitsFromPlan parses a one-unit plan document into the units the ingestion check
// walks, so the pin exercises the door rather than a hand-built slice.
func unitsFromPlan(t *testing.T, scope string) []fanout.Unit {
	t.Helper()
	units, err := fanout.ParsePlan(handWrittenPlanDoc(scope))
	if err != nil {
		t.Fatalf("parse the probe plan: %v", err)
	}
	if len(units) == 0 {
		t.Fatal("the probe plan has no units")
	}
	return units
}
