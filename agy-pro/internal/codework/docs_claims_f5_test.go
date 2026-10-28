package codework

// FIX-F5 doc-claim pins (fix-list-7 rows W1, W7, W8, W10).
//
// Round 7 filed three wording defects and two disclosure gaps, and each one is a
// claim the tree makes about itself. The pins here hold the corrected sentences to
// the code they describe, in the E2/E3/F4 pattern: a doc sentence is only allowed to
// stand when the source half it rests on is present.

import (
	"strings"
	"testing"
)

// TestCodeDocPinsCopiedLedgerVerifiesIntoItsRecordedRoot is W7. The package comment
// of snapshot.go said the OPPOSITE of the shipped property ("a ledger copied away from
// the path it was taken in does not verify"), while sig.go, docs/code.md and the
// implementation all say it does verify - the key id travels inside the signed bytes -
// and that Rollback still refuses to apply it to a different tree. A contract that
// over-asserts strictness on a security property is the defect: a future reader would
// believe relocation kills a ledger, and the disclosed stale-baseline replay is exactly
// the reason it does not.
func TestCodeDocPinsCopiedLedgerVerifiesIntoItsRecordedRoot(t *testing.T) {
	doc, _ := codeDocRead(t)
	snapshot := codeSnapSource(t, "snapshot.go")
	sig := codeSnapSource(t, "sig.go")

	// The FALSE sentence must be gone from both halves of the package.
	for _, file := range []struct{ name, src string }{{"snapshot.go", snapshot}, {"sig.go", sig}} {
		if strings.Contains(file.src, "copied away from the path it was taken in does not") {
			t.Errorf("internal/codesnap/%s still states that a copied ledger does not verify", file.name)
		}
	}
	// And both halves must state the shipped property, with the root bound.
	for _, want := range []struct{ name, src, s string }{
		{"snapshot.go", snapshot, "A ledger COPIED to another path still verifies"},
		{"snapshot.go", snapshot, "only INTO its recorded root"},
		{"sig.go", sig, "a ledger COPIED to another path still verifies"},
		{"sig.go", sig, "restorable only INTO its recorded root"},
	} {
		if !strings.Contains(want.src, want.s) {
			t.Errorf("internal/codesnap/%s does not state %q", want.name, want.s)
		}
	}
	// The refusal the claim rests on is the root check, and it must still be there.
	rollback := codeSnapSource(t, "rollback.go")
	if !strings.Contains(rollback, "if !sameRoot(rootAbs, m.Root)") {
		t.Error("rollback.go no longer refuses a manifest whose root is not the tree; the copied-ledger claim would be false")
	}
	// docs/code.md states the same thing in operator words.
	if !codeDocHas(doc, "A ledger COPIED elsewhere still verifies") {
		t.Error("docs/code.md no longer states that a copied ledger still verifies")
	}
	if !codeDocHas(doc, "only INTO its recorded root") {
		t.Error("docs/code.md does not bound the copied ledger to its recorded root")
	}
}

// TestCodeDocPinsRetakeRekeyWording is W8's wording half (r7 mimopro F-R7-3,
// CR-charmglm P3-3, SEC-nwglmflash LOW-5): "a re-take into the same directory re-keys
// it" is true only for the same SPELLING. A case-variant re-take on a folding volume
// derives a second id and leaves the older pair verifying - the disclosed
// stale-baseline bound - so the shipped sentences must say so, in the source and in
// the operator doc.
func TestCodeDocPinsRetakeRekeyWording(t *testing.T) {
	doc, _ := codeDocRead(t)
	sig := codeSnapSource(t, "sig.go")

	for _, want := range []struct{ name, src, s string }{
		{"sig.go", sig, "spelled the same way"},
		{"sig.go", sig, "KEEPS verifying"},
	} {
		if !strings.Contains(want.src, want.s) {
			t.Errorf("internal/codesnap/%s does not state %q, so the re-take claim is still unqualified", want.name, want.s)
		}
	}
	for _, s := range []string{
		"a re-take into the same directory SPELLED THE\n  SAME WAY re-keys it",
		"the disclosed stale-baseline\n  replay bound, not a revocation",
	} {
		if !codeDocHas(doc, s) {
			t.Errorf("docs/code.md must carry the re-take qualification %q", s)
		}
	}
	// The index/retention machinery the wording points at must be the shipped one.
	if !strings.Contains(sig, "ledgerKeyRetention") || !strings.Contains(sig, "ledgerKeysIndexName") {
		t.Error("sig.go no longer carries the retention constants the doc names")
	}
}

// TestCodeDocPinsKeyStoreRetention is W8's retention half: the store is bounded by
// count PLUS existence, the state home owns the prune, and the doc says both.
func TestCodeDocPinsKeyStoreRetention(t *testing.T) {
	doc, _ := codeDocRead(t)
	sig := codeSnapSource(t, "sig.go")

	if !codeDocHas(doc, "The store is bounded:") {
		t.Error("docs/code.md does not state that the key store is bounded")
	}
	if !codeDocHas(doc, "keep the newest 50 records") {
		t.Error("docs/code.md does not state the retention window")
	}
	if !codeDocHas(doc, "never remove one whose recorded run directory\n  still exists") {
		t.Error("docs/code.md does not state the live-run protection that makes the prune safe")
	}
	if !strings.Contains(sig, "func pruneLedgerKeys(") {
		t.Error("sig.go does not carry the prune the doc describes")
	}
	if !strings.Contains(sig, "if serr == nil && info.IsDir() {") {
		t.Error("the prune no longer checks that the recorded run directory exists; a live key could be deleted")
	}
	if !strings.Contains(sig, "_ = pruneLedgerKeys()") {
		t.Error("the prune is not called after a take (best effort), so the store would still grow unbounded")
	}
}

// TestCodeDocPinsThreatModelBoundaries is W1 + W10: the provenance binding's SCOPE
// and the out-of-root hardlink residue are disclosures, so they must be stated where
// an operator reads them - and the tier argument must be the one the seats reached
// independently (seat-tier and below; an operator-privilege process can read the keys
// exactly as it can write any file), not a claim of operator-proof integrity.
func TestCodeDocPinsThreatModelBoundaries(t *testing.T) {
	doc, _ := codeDocRead(t)
	sig := codeSnapSource(t, "sig.go")

	for _, s := range []string{
		"binds code at\n  seat tier and below",
		"can read `~/.agy-pro/ledger-keys` and sign\n  anything",
		"it can also write any file in the tree directly, with no\n  key at all",
	} {
		if !codeDocHas(doc, s) {
			t.Errorf("docs/code.md must carry the binding-scope sentence %q", s)
		}
	}
	if !strings.Contains(sig, "the binding's SCOPE is seat-tier and below") {
		t.Error("sig.go does not state the binding's threat-model scope")
	}
	// W10: the out-of-root hardlink alias residue, with its tier and the future arm.
	if !codeDocHas(doc, "**Out-of-root hardlink aliases are not withheld.**") {
		t.Error("docs/code.md §5 does not carry the out-of-root hardlink residue")
	}
	if !codeDocHas(doc, "GetFileInformationByHandle") {
		t.Error("docs/code.md does not name the Windows link-count API as the future hardening arm")
	}
	if !codeDocHas(doc, "§5.12") {
		t.Error("the hardlink bullet in §4.4 does not point at the residue paragraph")
	}
}
