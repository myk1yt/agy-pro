package codesnap

// FIX-E1 pins (fix-list-4 rows N1, N2, N3, N6, N7).
//
// What this file locks down:
//
//	N1 file identity - the ledger's recorded/withheld lookups and the diff
//	                    exclusion key on the EXACT root-relative path. A run-created
//	                    "src/prınce.txt" is a DIFFERENT file from a recorded
//	                    "src/prince.txt" on this host (U+0131 is not folded by
//	                    NTFS), so it is archived and reported as created instead of
//	                    being merged into the row it resembles. The fold that
//	                    remains is the ASCII one, and only for DIRECTORY PREFIX
//	                    comparisons: "src" and "SRc" are one directory here, while
//	                    "reſults" is not "results".
//	N2 claim semantics - the ledger-claim refusal is the UNION of the pattern
//	                    AS AUTHORED (path.Match, the runtime matcher's own
//	                    semantics) and a folded-LITERAL comparison that never
//	                    rewrites a character class. Strictly more refusing than both
//	                    prior variants across an ASCII matrix, with the class-range
//	                    counterexample ("reports/[A-z]run" covers "reports/_run")
//	                    as the witness.
//	N3 fold gate      - the case gate is a RUNTIME PROBE of the root's own volume,
//	                    not GOOS, and its answer matches a direct measurement of the
//	                    filesystem; an inconclusive probe fails closed (fold on).
//	                    (FIX-F2: the probe is a write/stat pair INSIDE the root, see
//	                    fix_f2_test.go - it used to flip the whole root path, which
//	                    made the answer depend on the mount's parent.)
//	N6 resolved chain - a manifest row whose written path transits an in-root link
//	                    into a governance/credential directory refuses the whole
//	                    rollback, through the same predicate the write guard uses.
//	N7 deep tails     - the exported chain predicate rejoins a deep tail in walk
//	                    order (the linearization itself is pinned on the Windows
//	                    expansion arm by TestWin32ExpansionWalksLongestExistingAncestor).
//
// Every pin is behavioral or predicate-level; none asserts a source line.

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/agenttools"
)

// foldOn / foldOff are the two policies a root can resolve to. Pins that must
// assert BOTH branches on any host construct the policy explicitly instead of
// asking the probe, so the assertion does not depend on the machine running it.
var (
	foldOn  = namePolicy{foldCase: true}
	foldOff = namePolicy{foldCase: false}
)

// ---------------------------------------------------------------------------
// N1: file identity is exact, directory prefixes fold ASCII case only.
// ---------------------------------------------------------------------------

// TestFileIdentityIsExactNotFolded is the N1 pin at the behavioral level: a
// run-created Unicode look-alike of a recorded name is archived by Rollback and
// reported as created by Diff, and the recorded file keeps its own bytes.
func TestFileIdentityIsExactNotFolded(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"src/prince.txt": "RECORDED\n"})

	m, err := Take(root, []string{"src/*"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got, want := rowPaths(m), []string{"src/prince.txt"}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v", got, want)
	}

	// U+0131 LATIN SMALL LETTER DOTLESS I: a different file from "src/prince.txt"
	// on this host (measured, coder-report-fix-d1a.md M5).
	const lookalike = "src/prınce.txt"
	writeTree(t, root, map[string]string{lookalike: "SEAT-CREATED\n"})

	origInfo, oerr := os.Stat(filepath.Join(root, "src", "prince.txt"))
	lookInfo, lerr := os.Stat(filepath.Join(root, filepath.FromSlash(lookalike)))
	if oerr != nil || lerr != nil {
		t.Fatalf("stat the two names: %v / %v", oerr, lerr)
	}
	if os.SameFile(origInfo, lookInfo) {
		t.Skipf("this volume maps %q onto %q; the pin cannot distinguish them here", lookalike, "src/prince.txt")
	}

	// Diff: the creation is REPORTED. A folded identity key would merge it into
	// the recorded row and report nothing at all (SEC r4 F1).
	deltas, derr := Diff(root, m)
	if derr != nil {
		t.Fatalf("Diff: %v", derr)
	}
	if len(deltas) != 1 || deltas[0].Path != lookalike || deltas[0].Status != StatusCreated {
		t.Fatalf("Diff = %+v; want exactly one created delta for %q", deltas, lookalike)
	}

	// Rollback: the creation is MOVED into the archive and reported, never merged
	// into "already recorded" and never deleted.
	report, rerr := Rollback(root, dest)
	if rerr != nil {
		t.Fatalf("Rollback: %v", rerr)
	}
	wantArchived := "rollback-archive/" + lookalike
	if got := report.Archived; len(got) != 1 || got[0] != wantArchived {
		t.Fatalf("archived = %v; want [%s] (a run-created look-alike name is not the recorded file)", got, wantArchived)
	}
	if _, serr := os.Stat(filepath.Join(dest, filepath.FromSlash(wantArchived))); serr != nil {
		t.Errorf("the archived copy must exist (nothing is deleted): %v", serr)
	}
	if got := readDisk(t, dest, wantArchived); got != "SEAT-CREATED\n" {
		t.Errorf("archived bytes = %q; want the seat's bytes", got)
	}
	if got := readDisk(t, root, "src/prince.txt"); got != "RECORDED\n" {
		t.Errorf("src/prince.txt = %q; want the recorded bytes", got)
	}
	if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(lookalike))); !os.IsNotExist(serr) {
		t.Errorf("the created look-alike is still in the tree (stat err %v); it must be archived", serr)
	}
}

// TestDirectoryPrefixFoldIsASCIICaseOnly is the other half of N1: the fold that
// survived is the ASCII one, applied to DIRECTORY PREFIX comparisons only.
func TestDirectoryPrefixFoldIsASCIICaseOnly(t *testing.T) {
	cases := []struct {
		name   string
		policy namePolicy
		dirRel string
		rel    string
		want   bool
	}{
		{
			name:   "case-variant directory prefix on a folding volume",
			policy: foldOn, dirRel: "src", rel: "SRc/x.go", want: true,
		},
		{
			name:   "case-variant ledger prefix, every component",
			policy: foldOn, dirRel: "reports/code/ts", rel: "REPORTS/CODE/TS/manifest.json", want: true,
		},
		{
			name:   "the directory decides, the file's own name is not folded",
			policy: foldOn, dirRel: "reports/code/ts", rel: "reports/code/ts/prınce.txt", want: true,
		},
		{
			// "reſults" is "results" with U+017F standing in for the first "s": the
			// governance fold merges that pair (Win32NameFold maps U+017F onto "s"),
			// NTFS keeps the two files apart, and the ASCII fold keeps them apart.
			// This is F1's stealth variant; the control below asserts the collision
			// the governance fold would produce, so the row is not vacuous.
			name:   "long s does not fold onto the ledger's directory",
			policy: foldOn, dirRel: "results/code/ts", rel: "reſults/code/ts/payload", want: false,
		},
		{
			name:   "dotless i does not fold onto the ledger's directory",
			policy: foldOn, dirRel: "ınner/code/ts", rel: "inner/code/ts/payload", want: false,
		},
		{
			name:   "the same spelling still matches its exact prefix",
			policy: foldOn, dirRel: "results/code/ts", rel: "results/code/ts/payload", want: true,
		},
		{
			name:   "a case-sensitive volume folds nothing",
			policy: foldOff, dirRel: "src", rel: "SRc/x.go", want: false,
		},
		{
			name:   "a case-sensitive volume still matches the exact prefix",
			policy: foldOff, dirRel: "reports/code/ts", rel: "reports/code/ts/manifest.json", want: true,
		},
		{
			name:   "an empty prefix matches nothing",
			policy: foldOn, dirRel: "", rel: "src/x.go", want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.policy.pathUnder(c.dirRel, c.rel); got != c.want {
				t.Fatalf("pathUnder(%q, %q) with foldCase=%v = %v; want %v",
					c.dirRel, c.rel, c.policy.foldCase, got, c.want)
			}
		})
	}

	// Non-vacuous control for the two look-alike rows: the governance fold DOES merge
	// both pairs, which is exactly why it must not be the fold a directory prefix
	// uses.
	if agenttools.Win32NameFold("reſults/code/ts") != agenttools.Win32NameFold("results/code/ts") {
		t.Fatalf("the governance fold no longer merges the long-s spelling; the row above would be vacuous")
	}
	if agenttools.Win32NameFold("ınner/code/ts") != agenttools.Win32NameFold("inner/code/ts") {
		t.Fatalf("the governance fold no longer merges the dotless-i spelling; the row above would be vacuous")
	}
}

// TestWalkExclusionDoesNotSwallowAFoldedLookalike is the end-to-end N1 pin for the
// walk exclusion: with the ledger at results/code/ts inside the root, a file the
// run placed at "reſults/code/ts/payload" (U+017F for the "s") is CAPTURED, not
// excluded as "inside the ledger", because the exclusion compares ASCII case only.
//
// Pre-fix this claim vanished: compareFold was agenttools.Win32NameFold, which maps
// U+017F onto "s", so the look-alike directory folded onto the ledger's and
// fs.SkipDir removed it from the snapshot, the diff and the archive (reproduced on
// the pre-fix tree; see the E1 report's reproduction section).
func TestWalkExclusionDoesNotSwallowAFoldedLookalike(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(root, "results", "code", "ts")
	const lookalike = "reſults/code/ts/payload"
	writeTree(t, root, map[string]string{
		"results/notes.md": "# notes\n",
		lookalike:          "SEAT-CREATED\n",
	})

	m, err := Take(root, []string{"reſults/**"}, dest)
	if err != nil {
		t.Fatalf("Take(scope over the look-alike directory): %v", err)
	}
	if got, want := rowPaths(m), []string{lookalike}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v (the look-alike directory is not the ledger)", got, want)
	}
}

// TestWithheldIdentityIsExact pins the withheld map's keys: exact paths, so a
// look-alike or a case variant is a different key. The ASCII-uppercase row is the
// load-bearing one: a folded map would store "config/secret.env" and this lookup
// would miss.
func TestWithheldIdentityIsExact(t *testing.T) {
	m := &Manifest{Skipped: []SkippedFile{
		{Path: "config/.env", Reason: ReasonSecretClass},
		{Path: "config/s.key", Reason: ReasonSecretClass},
		{Path: "config/SECRET.ENV", Reason: ReasonSecretClass},
	}}
	got := withheldPaths(m)
	for _, hit := range []string{"config/.env", "config/s.key", "config/SECRET.ENV"} {
		if !got[hit] {
			t.Fatalf("withheldPaths lost the exact key %q: %v", hit, got)
		}
	}
	for _, miss := range []string{"config/.ENV", "CONFIG/.env", "config/ſ.key", "config/s.KEY", "config/secret.env"} {
		if got[miss] {
			t.Errorf("withheldPaths[%q] is set; the map must key exact paths only", miss)
		}
	}
}

// ---------------------------------------------------------------------------
// N2: the ledger claim is the union of the lexical pattern and a folded literal.
// ---------------------------------------------------------------------------

// TestLedgerClaimPatternSemantics pins the cases the row names.
func TestLedgerClaimPatternSemantics(t *testing.T) {
	cases := []struct {
		name   string
		policy namePolicy
		entry  string
		dirRel string
		want   bool
	}{
		{
			name:   "a legal range that covers the ledger lexically",
			policy: foldOn, entry: "reports/[A-z]run", dirRel: "reports/_run", want: true,
		},
		{
			name:   "the same range on a case-sensitive volume",
			policy: foldOff, entry: "reports/[A-z]run", dirRel: "reports/_run", want: true,
		},
		{
			name:   "a folded literal segment still meets the ledger's spelling",
			policy: foldOn, entry: "RePoRtS/**", dirRel: "reports/code/ts", want: true,
		},
		{
			name:   "the ledger's own spelling is a case variant",
			policy: foldOn, entry: "reports/**", dirRel: "RePoRtS/code/ts", want: true,
		},
		{
			name:   "an uppercase class meets a lowercase directory",
			policy: foldOn, entry: "[A-Z]run", dirRel: "arun", want: true,
		},
		{
			name:   "a wildcard with folded literals around it",
			policy: foldOn, entry: "Re*ORTS/**", dirRel: "reports/code", want: true,
		},
		{
			name:   "a benign scope is unaffected",
			policy: foldOn, entry: "src/*.go", dirRel: "reports/code/ts", want: false,
		},
		{
			name:   "a benign scope beside the ledger is unaffected",
			policy: foldOn, entry: "reports/code/*.md", dirRel: "reports/code/ts", want: false,
		},
		{
			name:   "a case variant on a case-sensitive volume claims nothing",
			policy: foldOff, entry: "RePoRtS/**", dirRel: "reports/code/ts", want: false,
		},
		{
			name:   "a deeper path inside the ledger still claims it",
			policy: foldOn, entry: "reports/code/ts-1/manifest.json", dirRel: "reports/code/ts-1", want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.policy.scopeMayClaimUnder(c.entry, c.dirRel); got != c.want {
				t.Fatalf("scopeMayClaimUnder(%q, %q) with foldCase=%v = %v; want %v",
					c.entry, c.dirRel, c.policy.foldCase, got, c.want)
			}
		})
	}
}

// legacyExactClaim is the pre-D1b form: path.Match on the strings as written.
func legacyExactClaim(entry, dirRel string) bool {
	return claimChainMatches(strings.Split(entry, "/"), strings.Split(dirRel, "/"), false)
}

// legacyFoldedClaim is the D1b form: the WHOLE pattern folded through the
// governance name fold, then path.Match component by component.
func legacyFoldedClaim(entry, dirRel string) bool {
	pc := strings.Split(agenttools.Win32NameFold(entry), "/")
	dc := strings.Split(agenttools.Win32NameFold(dirRel), "/")
	n := len(pc)
	if len(dc) < n {
		n = len(dc)
	}
	for i := 0; i < n; i++ {
		ok, err := path.Match(pc[i], dc[i])
		if err != nil {
			return true
		}
		if !ok {
			return false
		}
	}
	return true
}

// TestLedgerClaimIsMoreRefusingThanBothPriorVariants is N2's contract in the form
// the row asks for: over an ASCII matrix of patterns and ledger directories, the
// shipped rule refuses EVERYTHING the byte-exact rule refused and everything the
// whole-pattern fold refused, and at least one case only it refuses.
//
// Scope of the claim, stated so it is not over-read: the superset is asserted for
// ASCII patterns and targets, which is where the fold's over-refusals and the
// class-range semantics live. It is deliberately NOT asserted over non-ASCII
// names: the whole-pattern fold also refused names it had merged ("ſrun" vs
// "srun", "provıders.json"), and those extra refusals ARE the merge N1 removed -
// keeping them would restore the hiding vector this unit exists to close.
func TestLedgerClaimIsMoreRefusingThanBothPriorVariants(t *testing.T) {
	patterns := []string{
		"reports/**", "reports", "reports/[A-z]run", "RePoRtS/**", "Re*ORTS/**",
		"[A-Z]run", "reports/code/*", "reports/code/ts-1/manifest.json",
		"reports/code/@(ts)", "src/*.go", "*/code/ts-1", "REPORTS/CODE/TS-1",
		"reports/[a-z]*/ts-1", "reports/code/ts-1/snapshot/*",
	}
	targets := []string{
		"reports/code/ts-1", "REPORTS/code/ts-1", "reports/_run", "RePoRtS/_RUN",
		"src", "src/a.go", "reports/code/ts-1/manifest.json", "arun", "_run",
		"records", "reports/code/@(ts)", "code/ts-1",
	}
	stricter := 0
	for _, entry := range patterns {
		for _, dirRel := range targets {
			got := foldOn.scopeMayClaimUnder(entry, dirRel)
			if legacyExactClaim(entry, dirRel) && !got {
				t.Errorf("scopeMayClaimUnder(%q, %q) = false; the byte-exact rule refused it", entry, dirRel)
			}
			if legacyFoldedClaim(entry, dirRel) && !got {
				t.Errorf("scopeMayClaimUnder(%q, %q) = false; the whole-pattern fold refused it", entry, dirRel)
			}
			if got && !legacyExactClaim(entry, dirRel) && !legacyFoldedClaim(entry, dirRel) {
				stricter++
			}
		}
	}
	if stricter == 0 {
		t.Fatal("the matrix produced no case the shipped rule refuses and both prior variants accepted; the pin would be vacuous")
	}
	t.Logf("%d matrix cases are refused only by the shipped rule (class-range and folded-literal coverage)", stricter)

	// The named witness, so the count above is never the only evidence.
	if !foldOn.scopeMayClaimUnder("reports/[A-z]run", "reports/_run") {
		t.Error("the class-range counterexample is no longer refused")
	}
	if legacyFoldedClaim("reports/[A-z]run", "reports/_run") {
		t.Error("the legacy fold refused the class-range case too; the witness is not a witness")
	}
}

// TestFoldLiteralRunsKeepsClassesIntact pins the helper's contract: literal runs
// are folded in both directions, class payloads are copied verbatim, and (FIX-F2,
// finding P-2) an ESCAPED metacharacter is a literal in both forms - an escaped
// '[' opens no class, an escaped ']' closes none.
func TestFoldLiteralRunsKeepsClassesIntact(t *testing.T) {
	lo, hi := FoldLiteralRuns("Re[A-z]PoRtS*")
	if want := "re[A-z]ports*"; lo != want {
		t.Errorf("lower form = %q; want %q", lo, want)
	}
	if want := "RE[A-z]PORTS*"; hi != want {
		t.Errorf("upper form = %q; want %q", hi, want)
	}
	// The class stays a superset of the author's set: '_' is still inside A-z.
	if ok, err := path.Match(lo, "re_ports"); err != nil || !ok {
		t.Errorf("folded pattern %q no longer matches %q (err %v); a class range must never be rewritten", lo, "re_ports", err)
	}

	// Escaped class delimiters (SEC-seat1's R5-2 witness and CR-seat1's P1 one).
	for _, c := range []struct {
		name        string
		pattern     string
		wantLower   string
		wantUpper   string
		mustMatch   string // a target the LOWER form must match
		mustNotFold string // a byte that must survive unfolded in both forms
	}{
		{
			name: "escaped '[' is a literal, not a class opener", pattern: `Re\[Ports`,
			wantLower: `re\[ports`, wantUpper: `RE\[PORTS`, mustMatch: `re[ports`,
		},
		{
			name: "escaped ']' does not close the class", pattern: `R[A\]B][x]S`,
			wantLower: `r[A\]B][x]s`, wantUpper: `R[A\]B][x]S`, mustNotFold: `[A\]B]`,
		},
		{
			name: "a class member that is the class's own opening byte", pattern: `a[b[]c`,
			wantLower: `a[b[]c`, wantUpper: `A[b[]C`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := FoldLiteralRuns(c.pattern)
			if lo != c.wantLower {
				t.Errorf("lower form = %q; want %q (lowercased the escaped/class bytes?)", lo, c.wantLower)
			}
			if hi != c.wantUpper {
				t.Errorf("upper form = %q; want %q", hi, c.wantUpper)
			}
			if c.mustMatch != "" {
				if ok, err := path.Match(lo, c.mustMatch); err != nil || !ok {
					t.Errorf("folded pattern %q does not match %q (err %v); the literal '[' must survive the fold", lo, c.mustMatch, err)
				}
			}
			if c.mustNotFold != "" && !strings.Contains(lo, c.mustNotFold) {
				t.Errorf("lower form %q folded class payload %q; a class is never rewritten", lo, c.mustNotFold)
			}
		})
	}

	// The malformed shapes stay length- and byte-carrying from the point Go's
	// matcher would give up on them: the lexical arm already refuses those patterns
	// fail-closed, so the fold must not invent a class boundary either.
	for _, pat := range []string{`[`, `[a`, `[]A]x`, `[^]aA`, `a\`} {
		if _, err := path.Match(pat, "zz"); err == nil {
			t.Fatalf("path.Match(%q) accepted a pattern this pin calls malformed", pat)
		}
		lo, hi := FoldLiteralRuns(pat)
		if len(lo) != len(pat) || len(hi) != len(pat) {
			t.Errorf("FoldLiteralRuns(%q) = (%q, %q); folding never changes the byte count", pat, lo, hi)
		}
	}
}

// ---------------------------------------------------------------------------
// N3: the fold gate is a probe of the root's own volume.
// ---------------------------------------------------------------------------

// TestCaseFoldProbeMatchesTheFilesystem proves the probe answers the question by
// measuring the same filesystem directly, and that the policy the operations get
// is the probe's answer.
func TestCaseFoldProbeMatchesTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "SubDir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lower := filepath.Join(dir, "probe_lower.txt")
	if err := os.WriteFile(lower, []byte("x"), 0o600); err != nil {
		t.Fatalf("write probe fixture: %v", err)
	}
	info, serr := os.Stat(lower)
	if serr != nil {
		t.Fatalf("stat probe fixture: %v", serr)
	}
	otherInfo, oerr := os.Stat(filepath.Join(dir, "PROBE_LOWER.TXT"))
	measured := oerr == nil && os.SameFile(info, otherInfo)

	for _, p := range []string{dir, sub} {
		folds, conclusive := probeCaseFolding(p)
		if !conclusive {
			t.Fatalf("probeCaseFolding(%q) was inconclusive; a temp path carries ASCII letters and exists", p)
		}
		if folds != measured {
			t.Fatalf("probeCaseFolding(%q) = %v; the filesystem measurement says %v", p, folds, measured)
		}
		if got := policyFor(p).foldCase; got != measured {
			t.Fatalf("policyFor(%q).foldCase = %v; want the measured %v", p, got, measured)
		}
	}

	// Inconclusive -> fail closed. Both inputs here carry no toggleable letter or
	// do not exist, which is exactly the fallback the row asks for.
	for _, p := range []string{"1234/5678", filepath.Join(dir, "does-not-exist")} {
		if _, conclusive := probeCaseFolding(p); conclusive {
			t.Errorf("probeCaseFolding(%q) claimed an answer it cannot have", p)
		}
		if !policyFor(p).foldCase {
			t.Errorf("policyFor(%q) must fail closed (fold on) when the probe cannot answer", p)
		}
	}

	// The probe is a WRITE probe since FIX-F2 (finding P-3): it creates one
	// throw-away directory with one probe file inside it and removes both, so the
	// fixture directory must look exactly as the fixture left it. This is the
	// residue check, and it is why the probe's cleanup is deferred rather than
	// best-effort at each return.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("read dir: %v", rerr)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the probe left artifacts behind: %v", names)
	}
}

// ---------------------------------------------------------------------------
// N6: the resolved directory chain gates restore and archive.
// ---------------------------------------------------------------------------

// makeChainLink plants a directory link and reports how it was made. A symlink is
// tried first; on Windows without the symlink privilege a junction ("cmd /c mklink
// /J") is used, which needs no elevation. Skips when neither is permitted, so the
// pin never reports a pass it did not measure.
func makeChainLink(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return "symlink"
	}
	if runtime.GOOS == "windows" {
		abs, aerr := filepath.Abs(target)
		if aerr == nil {
			out, cerr := exec.Command("cmd", "/c", "mklink", "/J", link, abs).CombinedOutput()
			if cerr == nil {
				return "junction: " + strings.TrimSpace(string(out))
			}
			t.Logf("mklink /J refused (%v): %s", cerr, strings.TrimSpace(string(out)))
		}
	}
	t.Skipf("no directory link can be created on this host (symlink and junction both refused); the N6 arm is UNVERIFIED here")
	return ""
}

// TestRollbackRefusesResolvedChainGovernanceRows is N6's pin: a row whose WRITTEN
// path is clear but whose directory chain resolves into a governance directory
// refuses the whole rollback, before the tree is touched.
func TestRollbackRefusesResolvedChainGovernanceRows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"src/alpha.go":                   "package alpha\n",
		".agy-pro/registry-keys.v2.json": "GOVERNED\n",
	})
	kind := makeChainLink(t, filepath.Join(root, ".agy-pro"), filepath.Join(root, "link"))
	t.Logf("link kind: %s", kind)

	m, err := Take(root, []string{"src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	edited := "package alpha\n\n// seat edit\n"
	writeTree(t, root, map[string]string{"src/alpha.go": edited})

	// The forged row: no governed component in its written form, a governed
	// directory in its resolved one.
	const rel = "link/registry-keys.v2.json"
	body := "RESTORED-FROM-THE-LEDGER\n"
	forged := *m
	forged.Files = append(append([]ManifestRow{}, m.Files...), ManifestRow{
		Path: rel, Size: int64(len(body)), SHA256: digest(body), Mode: 0o600, Existed: true,
	})
	copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(rel))
	if werr := os.MkdirAll(filepath.Dir(copyPath), 0o755); werr != nil {
		t.Fatalf("mkdir snapshot copy: %v", werr)
	}
	if werr := os.WriteFile(copyPath, []byte(body), 0o600); werr != nil {
		t.Fatalf("plant snapshot copy: %v", werr)
	}
	d1bForgeManifest(t, dest, forged)

	// The lexical row policy must ACCEPT this row, or the resolved arm would be
	// untested (a governed name would have been refused before it).
	if _, lerr := LoadManifest(dest); lerr != nil {
		t.Fatalf("LoadManifest refused the row lexically (%v); the pin would not exercise the resolved chain", lerr)
	}

	_, rerr := Rollback(root, dest)
	if !errors.Is(rerr, ErrGovernanceRefused) {
		t.Fatalf("Rollback(resolved-chain governance row) error = %v; want ErrGovernanceRefused", rerr)
	}
	// Refused WHOLE, before the first write: the seat's edit is still there and the
	// governed file still holds its own bytes.
	if got := readDisk(t, root, "src/alpha.go"); got != edited {
		t.Errorf("src/alpha.go = %q; want the run's bytes (a refusal must not restore anything)", got)
	}
	if got := readDisk(t, root, ".agy-pro/registry-keys.v2.json"); got != "GOVERNED\n" {
		t.Errorf("the governance file was rewritten: %q", got)
	}
}

// TestRollbackRestoresThroughAClearResolvedChain is N6's over-refusal control: a
// row that transits an in-root link into an ORDINARY directory restores normally.
func TestRollbackRestoresThroughAClearResolvedChain(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"src/alpha.go":     "package alpha\n",
		"realdir/data.txt": "RECORDED\n",
	})
	kind := makeChainLink(t, filepath.Join(root, "realdir"), filepath.Join(root, "alias"))
	t.Logf("link kind: %s", kind)

	m, err := Take(root, []string{"src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	const rel = "alias/data.txt"
	body := "RECORDED\n"
	forged := *m
	forged.Files = append(append([]ManifestRow{}, m.Files...), ManifestRow{
		Path: rel, Size: int64(len(body)), SHA256: digest(body), Mode: 0o600, Existed: true,
	})
	copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(rel))
	if werr := os.MkdirAll(filepath.Dir(copyPath), 0o755); werr != nil {
		t.Fatalf("mkdir snapshot copy: %v", werr)
	}
	if werr := os.WriteFile(copyPath, []byte(body), 0o600); werr != nil {
		t.Fatalf("plant snapshot copy: %v", werr)
	}
	d1bForgeManifest(t, dest, forged)

	// The seat overwrote the real file behind the link; the restore goes through it.
	writeTree(t, root, map[string]string{"realdir/data.txt": "SEAT-EDIT\n"})

	report, rerr := Rollback(root, dest)
	if rerr != nil {
		t.Fatalf("Rollback(clear resolved chain) refused: %v", rerr)
	}
	if len(report.Restored) != 1 || report.Restored[0] != rel {
		t.Errorf("restored = %v; want [%s]", report.Restored, rel)
	}
	if got := readDisk(t, root, "realdir/data.txt"); got != body {
		t.Errorf("realdir/data.txt = %q; want %q", got, body)
	}
}

// TestArchiveMoveRefusesResolvedChainGovernanceSource pins N6's archive half at
// the function the archive loop calls (a function-level pin, stated plainly: a
// claim whose chain reaches a governance directory cannot reach the archive loop
// end-to-end, because walkScoped already refuses a governance-real-path claim).
func TestArchiveMoveRefusesResolvedChainGovernanceSource(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{".agy-pro/registry-keys.v2.json": "GOVERNED\n"})
	kind := makeChainLink(t, filepath.Join(root, ".agy-pro"), filepath.Join(root, "link"))
	t.Logf("link kind: %s", kind)

	archived, reason := archiveMove(root, dest, "reports/code/ts", "link/registry-keys.v2.json", foldOn)
	if archived != "" {
		t.Fatalf("archiveMove moved the governance file behind a link to %q; want a refusal", archived)
	}
	if !strings.Contains(reason, "resolves to") {
		t.Errorf("skip reason = %q; want it to name the resolved governance component", reason)
	}
	if got := readDisk(t, root, ".agy-pro/registry-keys.v2.json"); got != "GOVERNED\n" {
		t.Errorf("the governance file changed: %q", got)
	}
}

// ---------------------------------------------------------------------------
// N7: a deep ancestor tail is rejoined in walk order.
// ---------------------------------------------------------------------------

// TestResolvedDirChainJoinsDeepTailsInOrder drives the exported chain predicate
// over a deep path: the resolved directory is the path the walk built, component
// by component, which is what the linear (single-append, reverse-at-join) tail
// assembles.
func TestResolvedDirChainJoinsDeepTailsInOrder(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	comps := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		comps = append(comps, "d"+strconv.Itoa(i))
	}
	deep := filepath.Join(append([]string{root}, comps...)...)
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir deep chain: %v", err)
	}
	wantDeep := strings.Join(comps, "/")

	got, comp, refused, err := agenttools.ResolvedDirChainRefusal(root, filepath.Join(deep, "x.json"))
	if err != nil {
		t.Fatalf("ResolvedDirChainRefusal(deep existing chain): %v", err)
	}
	if refused {
		t.Fatalf("ResolvedDirChainRefusal(deep existing chain) refused at %q; the chain has no governed component", comp)
	}
	if got != wantDeep {
		t.Fatalf("resolved dir = %q; want %q (the tail must be rejoined in walk order)", got, wantDeep)
	}

	// An existing ancestor plus a not-yet-existing tail: the tail is re-attached
	// verbatim, still in walk order.
	got2, _, refused2, err2 := agenttools.ResolvedDirChainRefusal(root, filepath.Join(deep, "n1", "n2", "n3", "x.json"))
	if err2 != nil || refused2 {
		t.Fatalf("ResolvedDirChainRefusal(deep tail): err %v, refused %v", err2, refused2)
	}
	if want := wantDeep + "/n1/n2/n3"; got2 != want {
		t.Fatalf("resolved dir = %q; want %q", got2, want)
	}
}
