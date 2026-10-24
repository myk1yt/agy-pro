package codework

// Doc-claim pins for the FIX-F4 sections of `agy-pro/docs/code.md` §4.4 (round 6,
// fix-list-6 rows Q1-Q3). Each claim is matched in the document AND in the source
// that must make it true, so a sentence cannot outlive the code it describes.
//
// The four claims pinned here are the ones an operator relies on when deciding
// whether a run directory is a restore target:
//
//   - the manifest is signed (manifest.sig) and the key lives outside every root;
//   - a ledger with no sidecar refuses as an unsigned, pre-provenance directory;
//   - a scope may not claim ANY run ledger under the root, not just its own;
//   - a hardlink alias of a governed/secret file is withheld, never copied.

import (
	"strings"
	"testing"
)

func TestCodeDocPinsLedgerProvenance(t *testing.T) {
	doc, _ := codeDocRead(t)
	sig := codeSnapSource(t, "sig.go")
	snapshot := codeSnapSource(t, "snapshot.go")

	for _, sentence := range []string{
		// the section's opening claim
		"**The manifest is bound to its run (`manifest.sig`).**",
		// the key store and its location
		"`~/.agy-pro/ledger-keys/<key id>`",
		"(key store 0700, key file 0600",
		// the refusals the operator will actually see
		"`unsigned ledger (pre-provenance format)`",
		"`ErrLedgerProvenance`",
		// the two disclosures the fix list demanded be stated, not hidden
		"`key_id`, inside the signed bytes",
		"stays on disk as evidence, never as a\n  restore target",
	} {
		if !codeDocHas(doc, sentence) {
			t.Errorf("docs/code.md §4.4 must carry the provenance claim %q", sentence)
		}
	}

	// The source half: the sidecar, the HMAC, the key store and the sentinel.
	for _, want := range []struct{ file, s string }{
		{sig, `manifestSigName = "manifest.sig"`},
		{sig, "HMAC-SHA256(key, manifest bytes)"},
		{sig, `ledgerKeysDirName = "ledger-keys"`},
		{sig, "func verifyLedgerProvenance("},
		{sig, "func writeLedgerManifest("},
		{sig, "ErrLedgerProvenance = errors.New(\"codesnap: ledger provenance\")"},
		{snapshot, "if perr := verifyLedgerProvenance(destDir, data); perr != nil {"},
		{sig, "func ledgerKeyIDFor("},
	} {
		if !strings.Contains(want.file, want.s) {
			t.Errorf("the source the doc claim rests on does not contain %q", want.s)
		}
	}
	// The doc must not still describe the pre-F4 trust model.
	if codeDocHas(doc, "no integrity MAC on `manifest.json`") {
		t.Error("docs/code.md still states the pre-F4 no-integrity position")
	}
}

func TestCodeDocPinsCrossRunLedgerRefusal(t *testing.T) {
	doc, _ := codeDocRead(t)
	snapshot := codeSnapSource(t, "snapshot.go")

	const sentence = "**A scope may not claim ANY run ledger, not just this run's.**"
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md §4.4 must carry the cross-run rule; missing:\n  %s", sentence)
	}
	for _, sentence := range []string{
		"a directory holding `manifest.json` and a `snapshot/` subdirectory, where the\n  manifest's own bytes declare this schema version and a `snapshot_dir`",
		"it\n  does not consult the signature, so a ledger whose key is gone still refuses",
		"the shape itself is plantable - a seat can write a\n  `manifest.json` carrying `version` and `snapshot_dir` and re-create the refusal on\n  purpose",
		"delete the planted pair and re-run",
		// FIX-G1 disclosures: the 1 MiB escape (SEC-qwen L-1) and the planting run's
		// own self-denial (SEC-mimopro finding 3) are stated, not implied.
		"A candidate whose `manifest.json` is LARGER than that bound is not classified as a ledger at all",
		"planting a pair inside a run's scope refuses that run's OWN undo too",
		"A\n  directory LINK at a ledger (symlink or junction) is resolved and judged by the same\n  test",
	} {
		if !codeDocHas(doc, sentence) {
			t.Errorf("docs/code.md must carry the cross-run detail %q", sentence)
		}
	}
	// The flip must stay stated in the doc: the old coarse rule over-refused a build
	// tree, and the sentence that said so is replaced by this one, not deleted.
	if !codeDocHas(doc, "an npm-style `myapp/dist/manifest.json` beside `dist/snapshot/` is an ordinary\n  directory") {
		t.Error("docs/code.md no longer states that the two-NAMES coincidence alone is not a ledger")
	}
	// The source half: the fingerprint and the two call sites that turn it into a
	// refusal (Take and Rollback), never a silent skip.
	for _, s := range []string{
		"func isLedgerFingerprintDir(",
		"func refusalForPriorLedger(",
		"func refusalForDiscoveredLedger(",
		"func PriorLedgerClaim(",
	} {
		if !strings.Contains(snapshot, s) {
			t.Errorf("internal/codesnap/snapshot.go does not contain %q, which the cross-run doc claim rests on", s)
		}
	}
	// The PRE-CONSENT door must ask the same question, or the operator is prompted for
	// a run that Take then refuses.
	phases := codeworkSource(t, "phases.go")
	if !strings.Contains(phases, "codesnap.PriorLedgerClaim(") {
		t.Error("internal/codework/phases.go never asks codesnap.PriorLedgerClaim; the cross-run rule would fire only after consent")
	}
	// Both doors refuse through the discovered-ledger form, which re-measures the fold
	// at the ledger itself (FIX-F5, W6) - and each does it exactly once.
	if got := strings.Count(snapshot, "refusalForDiscoveredLedger(rootAbs, normalized"); got != 1 {
		t.Errorf("snapshot.go calls refusalForDiscoveredLedger for the TAKE %d time(s); want exactly 1", got)
	}
	rollback := codeSnapSource(t, "rollback.go")
	if !strings.Contains(rollback, "refusalForDiscoveredLedger(rootAbs, m.Scopes") {
		t.Error("rollback.go does not apply the cross-run rule to the manifest's own scopes")
	}
	if !strings.Contains(rollback, "refuseKeysInsideRoot(rootAbs)") {
		t.Error("rollback.go never runs the key-store containment check; the guard would be Take-only (FIX-F5, W2)")
	}
}

func TestCodeDocPinsHardlinkAliasWithholding(t *testing.T) {
	doc, _ := codeDocRead(t)
	snapshot := codeSnapSource(t, "snapshot.go")

	const sentence = "**An inode is not a name: a hardlink alias is withheld, never copied.**"
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md §4.4 must carry the hardlink rule; missing:\n  %s", sentence)
	}
	if !codeDocHas(doc, "`hardlink alias of withheld path (<the governed path>): withheld from the\n  snapshot, no bytes copied`") {
		t.Error("docs/code.md must quote the shipped skip reason for a hardlink alias")
	}
	if !codeDocHas(doc, "`Rollback` neither restores nor\n  archives it") {
		t.Error("docs/code.md must state that rollback leaves a withheld alias in place")
	}
	for _, s := range []string{
		`ReasonHardlinkAlias = "hardlink alias of withheld path"`,
		"func denylistedName(",
		"func hardlinkAliasOf(",
		"os.SameFile(info, d.info)",
	} {
		if !strings.Contains(snapshot, s) {
			t.Errorf("internal/codesnap/snapshot.go does not contain %q, which the hardlink doc claim rests on", s)
		}
	}
	// The reason string the doc quotes must be the constant, not a literal copy.
	if !strings.Contains(snapshot, "ReasonHardlinkAlias + \" (\" + other + \"): withheld from the snapshot, no bytes copied\"") {
		t.Error("the shipped skip reason does not interpolate the aliased path")
	}
	// Platform honesty: Windows has no link count, so the doc must not claim a
	// fast gate this host does not have.
	windows := codeSnapSource(t, "linkcount_windows.go")
	if !strings.Contains(windows, "return 0, false") {
		t.Error("linkcount_windows.go reports a link count; the doc's Windows disclosure would be wrong")
	}
}

func TestCodeDocPinsLedgerSubtreeFoldProbe(t *testing.T) {
	doc, _ := codeDocRead(t)
	snapshot := codeSnapSource(t, "snapshot.go")

	for _, sentence := range []string{
		"**The ledger claim tests measure\n  ONE MORE place**",
		"the deepest existing ancestor of the run directory itself",
		"the claim folds when EITHER measurement folds (`ledgerPolicyFor`)",
		"The walk\n  EXCLUSION keeps the root-only policy on purpose",
	} {
		if !codeDocHas(doc, sentence) {
			t.Errorf("docs/code.md §4.4 must carry the ledger-subtree probe claim %q", sentence)
		}
	}
	for _, s := range []string{
		"func ledgerPolicyFor(",
		"func deepestExistingDir(",
		"func policyForDir(",
	} {
		if !strings.Contains(snapshot, s) {
			t.Errorf("internal/codesnap/snapshot.go does not contain %q, which the Q3 doc claim rests on", s)
		}
	}
	// The claim tests must actually be fed the merged policy: Take, Rollback, Diff
	// and the exported LedgerScopeClaims.
	for _, want := range []struct{ file, s string }{
		{snapshot, "claimPolicy := ledgerPolicyFor(rootAbs, destSpace)"},
		{snapshot, "if !ledgerPolicyFor(rootAbs, canonicalDirish(destAbs)).scopeMayClaimUnder(pattern, destRel) {"},
		{codeSnapSource(t, "rollback.go"), "claimPolicy := ledgerPolicyFor(rootAbs, destSpace)"},
		{codeSnapSource(t, "diff.go"), "ledgerPolicyFor(rootAbs, canonicalDirish(runDir))"},
	} {
		if !strings.Contains(want.file, want.s) {
			t.Errorf("the source does not contain %q, so the doc's ledger-fold claim is not wired", want.s)
		}
	}
}
