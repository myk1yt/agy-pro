package codework

// FIX-F4 (fix-list-6 row Q1) gate pin: the CROSS-RUN ledger rule must fire on BOTH
// doors, and on the pre-consent door first.
//
// The finding's chain does not need a forged byte: run 1 owns reports/code/t1, run 2
// is granted --scope reports/code/t1/**, and the operator's documented rollback over
// t1 then installs whatever run 2's seat wrote there. The ledger claim rule already
// refused a scope aimed at THIS run's directory before the consent prompt (finding
// R3-9); the cross-run arm has to obey the same timing, or the operator would answer
// a y/N prompt for a run whose snapshot must then fail.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
)

func TestPriorLedgerScopeRefusedBeforeConsent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	writeCodeworkTree(t, root, map[string]string{"src/main.go": "package main\n"})
	t1 := filepath.Join(root, "reports", "code", "t1")
	t2 := filepath.Join(root, "reports", "code", "t2")

	if _, err := codesnap.Take(root, []string{"src/**"}, t1); err != nil {
		t.Fatalf("run 1: %v", err)
	}

	for _, scope := range []string{"reports/code/t1/**", "reports/code/t1", "reports/code/t1/manifest.json"} {
		err := RefuseScopeEntries(root, t2, []string{scope}, "--scope")
		if !errors.Is(err, ErrRefusedPlanScope) {
			t.Fatalf("RefuseScopeEntries(%q) error = %v; want ErrRefusedPlanScope (exit 2, pre-consent)", scope, err)
		}
		if !strings.Contains(err.Error(), "existing run ledger") {
			t.Errorf("the pre-consent refusal for %q does not name the prior ledger: %v", scope, err)
		}
		// Both doors agree; the ledger is the one that would have written bytes.
		if _, terr := codesnap.Take(root, []string{scope}, t2); !errors.Is(terr, codesnap.ErrLedgerRefused) {
			t.Fatalf("codesnap.Take(%q) error = %v; want ErrLedgerRefused (parity with the gate)", scope, terr)
		}
	}

	// Non-vacuity: a scope that stays clear of the prior ledger passes the gate, and
	// the take it describes succeeds.
	if err := RefuseScopeEntries(root, t2, []string{"src/**"}, "--scope"); err != nil {
		t.Fatalf("the gate refused a scope that cannot reach any ledger: %v", err)
	}
	if _, err := codesnap.Take(root, []string{"src/**"}, t2); err != nil {
		t.Fatalf("the accepted scope failed at Take: %v", err)
	}

	// And a root with NO ledger under it keeps the gate inert, so the refusal above is
	// caused by the prior ledger and not by the word "reports": the same reports/*
	// scope passes when the run directory is outside the root (the current-run rule is
	// inert there) and no prior ledger exists.
	clean := filepath.Join(base, "clean")
	writeCodeworkTree(t, clean, map[string]string{"reports/notes.md": "# notes\n"})
	if err := RefuseScopeEntries(clean, filepath.Join(base, "outside-run"), []string{"reports/*"}, "--scope"); err != nil {
		t.Fatalf("the gate refused reports/* on a tree with no run ledger: %v", err)
	}
}

// writeCodeworkTree plants one file tree under root.
func writeCodeworkTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
}
