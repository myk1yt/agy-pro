package codesnap_test

// The run-directory rule lives in codesnap and internal/codework CALLS it, and this
// test is the end-to-end pin that the call reaches the same verdict from both sides.
//
// codesnap.Take / Rollback own the rule (snapshot.go ledgerClaimingEntry ->
// scopeMayClaimUnder), and since FIX-F2 they EXPORT it: internal/codework's
// refuseLedgerEntry calls codesnap.LedgerScopeClaims, so the pre-consent gate (which
// must refuse the scope BEFORE the y/N prompt, finding R3-9) and the snapshot-time
// refusal are one implementation, not two copies of one rule. That is the whole
// point of row P-2: a copy is what drifted twice in round 5 (a shell-style "[!]"
// negation, and a probe of process temp instead of the operation root).
//
// The contract pinned here is therefore STRICT in both directions: every case must
// receive the same verdict from RefuseScopeEntries and from codesnap.Take. The
// pre-F2 form of this file allowed one tolerated direction ("the mirror may be
// laxer on case variants, that is the safe direction"); that tolerance is gone,
// because the drift it was hiding is the finding.
//
// It lives in the external test package because importing codework from the
// internal one is an import cycle.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/codesnap"
	"agypro/internal/codework"
)

// parityCase is one scope spelling judged by both layers.
type parityCase struct {
	name        string
	scope       string
	outRel      string // the run directory, root-relative slash form
	wantRefusal bool   // what BOTH layers must say
}

func TestLedgerScopeClaimsAgreeBetweenTheLayers(t *testing.T) {
	cases := []parityCase{
		{name: "the run directory itself", scope: "reports/**", outRel: "reports/code/ts-1", wantRefusal: true},
		{name: "the ledger's parent", scope: "reports", outRel: "reports/code/ts-1", wantRefusal: true},
		{name: "a path inside the ledger", scope: "reports/code/ts-1/manifest.json", outRel: "reports/code/ts-1", wantRefusal: true},
		{name: "a deeper wildcard", scope: "reports/code/*", outRel: "reports/code/ts-1", wantRefusal: true},
		{name: "case-variant run directory", scope: "Reports/**", outRel: "reports/code/ts-1", wantRefusal: true},
		{name: "case-variant ledger path", scope: "REPORTS/CODE/TS-1/MANIFEST.JSON", outRel: "reports/code/ts-1", wantRefusal: true},
		{name: "case-variant run directory as spelled on disk", scope: "reports/**", outRel: "REPORTS/code/ts-1", wantRefusal: true},
		{name: "beside the ledger", scope: "reports/notes.md", outRel: "reports/code/ts-1", wantRefusal: false},
		{name: "elsewhere in the tree", scope: "src/*.go", outRel: "reports/code/ts-1", wantRefusal: false},
	}
	refusals, accepts := 0, 0
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "ws")
			if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
				t.Fatalf("mkdir root: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "src", "alpha.go"), []byte("package alpha\n"), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			out := filepath.Join(root, filepath.FromSlash(c.outRel))

			// The exported authority, asked directly: the pre-consent gate and the
			// snapshot must both agree with THIS, and with each other.
			authority, reason := codesnap.LedgerScopeClaims(root, out, c.scope)
			mirrorErr := codework.RefuseScopeEntries(root, out, []string{c.scope}, "parity")
			_, takeErr := codesnap.Take(root, []string{c.scope}, out)
			gateRefused := errors.Is(mirrorErr, codework.ErrRefusedPlanScope)
			takeRefused := errors.Is(takeErr, codesnap.ErrLedgerRefused)

			if authority != c.wantRefusal {
				t.Fatalf("LedgerScopeClaims(%q@%q) = %v (reason %q); want %v", c.scope, c.outRel, authority, reason, c.wantRefusal)
			}
			if authority && !strings.Contains(reason, "inside the run directory") {
				t.Fatalf("LedgerScopeClaims(%q) refused without naming the run directory: %q", c.scope, reason)
			}
			if gateRefused {
				refusals++
				if !strings.Contains(mirrorErr.Error(), "inside the run directory") {
					t.Fatalf("the gate refused %q for another reason: %v", c.scope, mirrorErr)
				}
			} else {
				accepts++
			}
			// STRICT parity in both directions, against the authority and against Take.
			if gateRefused != authority {
				t.Fatalf("pre-consent gate refused %q = %v but the authority says %v (gate err %v)", c.scope, gateRefused, authority, mirrorErr)
			}
			if takeRefused != authority {
				t.Fatalf("codesnap.Take refused %q = %v but the authority says %v (take err %v)", c.scope, takeRefused, authority, takeErr)
			}
		})
	}
	if refusals == 0 || accepts == 0 {
		t.Fatalf("parity pin is vacuous: %d refusals, %d accepts", refusals, accepts)
	}
}

// TestLedgerScopeClaimsIgnoresARunDirOutsideTheRoot pins the one case where the
// authority must answer "nothing to refuse" without consulting the rule at all: a run
// directory outside the root has no root-relative form a scope could reach it by, so
// both layers must accept every scope.
func TestLedgerScopeClaimsIgnoresARunDirOutsideTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere", "run")
	if refuse, reason := codesnap.LedgerScopeClaims(root, outside, "reports/**"); refuse {
		t.Fatalf("LedgerScopeClaims accepted a run directory outside the root as claimable: %q", reason)
	}
	if dirRel, inside := codesnap.LedgerDirRel(root, outside); inside || dirRel != "" {
		t.Fatalf("LedgerDirRel(outside root) = (%q, true); want not inside", dirRel)
	}
	if err := codework.RefuseScopeEntries(root, outside, []string{"reports/**"}, "parity"); err != nil {
		t.Fatalf("RefuseScopeEntries(outside run dir) = %v; want nil", err)
	}
}
