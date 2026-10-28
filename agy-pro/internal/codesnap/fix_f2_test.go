package codesnap

// FIX-F2 pins (fix-list-5 rows P-2, P-3, P-4, P-5).
//
// What this file locks down:
//
//	P-2  the folded-literal scanner is ONE escape-aware implementation, exported
//	     (FoldLiteralRuns) and consumed by internal/codework through LedgerScopeClaims.
//	     Its grammar is pinned against Go's own path.Match - not against a
//	     description of it - by a differential fuzz over an alphabet of glob
//	     metacharacters, on both fold policies. The three superset properties the
//	     r5 rows argue about are asserted mechanically for every pair in the space:
//	     the authored pattern's own matches, the pre-fix whole-pattern fold, and the
//	     pre-fix class-aware scanner all still refuse.
//	P-3  the case-fold probe measures a directory INSIDE the operation root, and every
//	     failure that is not a genuine "not found" stays inconclusive (fold on).
//	P-4  the archive restraint is indexed by ASCII-folded path, so only a case-alias
//	     spelling of a recorded row can be spared (pinned in fix_d1b/fix_e1 too; the
//	     hardlink side is pinned in fix_f2_hardlink_test).
//
// Nothing here asserts a source line except the malformed-shape fallbacks, which are
// properties of the exported function's output.

import (
	"math/rand"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/agenttools"
)

// ---------------------------------------------------------------------------
// P-2: the scanner, pinned differentially against path.Match.
// ---------------------------------------------------------------------------

// legacyAuthorityFold is the PRE-F2 scanner, copied faithfully: it looks for the
// first ']' from the '[' with strings.IndexByte (so an escaped ']' closes the class
// early and an escaped '[' opens one), copies that slice verbatim, and folds every
// other byte. It is the implementation CR-seat1's P1 and SEC-seat1's R5-2 were
// filed against, and the differential below proves the shipped scanner never refuses
// less than it does.
func legacyAuthorityFold(pat string) (lower, upper string) {
	var lo, hi strings.Builder
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		if c == '[' {
			if j := strings.IndexByte(pat[i:], ']'); j >= 0 {
				lo.WriteString(pat[i : i+j+1])
				hi.WriteString(pat[i : i+j+1])
				i += j
				continue
			}
			lo.WriteString(pat[i:])
			hi.WriteString(pat[i:])
			return lo.String(), hi.String()
		}
		lo.WriteByte(asciiLowerByte(c))
		hi.WriteByte(asciiUpperByte(c))
	}
	return lo.String(), hi.String()
}

// legacyAuthorityClaim is the pre-F2 claim predicate for a single component: the
// lexical match, then the pre-F2 scanner's two forms.
func legacyAuthorityClaim(pat, tgt string) bool {
	if ok, err := path.Match(pat, tgt); err != nil || ok {
		return true
	}
	lo, hi := legacyAuthorityFold(pat)
	if ok, err := path.Match(lo, asciiCaseFold(tgt)); err != nil || ok {
		return true
	}
	if ok, err := path.Match(hi, asciiUpper(tgt)); err != nil || ok {
		return true
	}
	return false
}

// wholePatternFoldClaim is D1b's rule, the one the pre-F2 code replaced: fold the
// whole pattern and the whole target through the governance name fold, then match.
// (Over ASCII input Win32NameFold is the ASCII fold, and the fuzz alphabet is ASCII,
// so this is the rule the 168-pair matrix compares against.)
func wholePatternFoldClaim(pat, tgt string) bool {
	ok, err := path.Match(agenttools.Win32NameFold(pat), agenttools.Win32NameFold(tgt))
	return err != nil || ok
}

// foldDifferential is the result of walking a (pattern, target) space.
type foldDifferential struct {
	pairs     int
	matches   int // pairs where path.Match(pattern, target) succeeds
	legacy    int // pairs the pre-fix class-aware scanner refuses
	wholeFold int // pairs D1b's whole-pattern fold refuses
	stricter  int // pairs only the shipped rule refuses
	malformed int // patterns path.Match rejects (the lexical arm refuses them)
	diverged  []string
}

// check asserts the three superset properties for one pair on both policies and
// records the pair's shape. Divergences are reported by the caller.
func (d *foldDifferential) check(pat, tgt string) {
	d.pairs++
	ok, err := path.Match(pat, tgt)
	if err != nil {
		d.malformed++
	}
	if err == nil && ok {
		d.matches++
	}
	gotOff := foldOff.scopeMayClaimUnder(pat, tgt)
	gotOn := foldOn.scopeMayClaimUnder(pat, tgt)
	legacyRefuses := legacyAuthorityClaim(pat, tgt)
	wholeRefuses := wholePatternFoldClaim(pat, tgt)
	if legacyRefuses {
		d.legacy++
	}
	if wholeRefuses {
		d.wholeFold++
	}
	if gotOn && !gotOff {
		d.stricter++
	}

	// (1) the authored pattern's own matches are refused, on BOTH policies: this is
	// the lexical arm, i.e. the ledger is never laxer than the runtime matcher.
	if err == nil && ok && !gotOff {
		d.diverged = append(d.diverged, "lexical("+pat+","+tgt+")")
	}
	// (2) the pre-fix class-aware scanner's refusals are refused: escape-aware class
	// parsing must not LOSE a refusal the buggy parser made.
	if legacyRefuses && !gotOn {
		d.diverged = append(d.diverged, "legacy-class-aware("+pat+","+tgt+")")
	}
	// (3) D1b's whole-pattern fold's refusals are refused: this is the property CR-seat1's
	// P1 witness ("R[A\]B][x]S" against "raxs") turns on, and it is why the shipped
	// rule keeps a whole-pattern-fold arm as a backstop.
	if wholeRefuses && !gotOn {
		d.diverged = append(d.diverged, "whole-pattern-fold("+pat+","+tgt+")")
	}
	// (4) monotonicity: folding on can only add refusals.
	if gotOff && !gotOn {
		d.diverged = append(d.diverged, "monotonic("+pat+","+tgt+")")
	}
	// (5) the scan itself: folding never changes the byte count, on any input.
	lo, hi := FoldLiteralRuns(pat)
	if len(lo) != len(pat) || len(hi) != len(pat) {
		d.diverged = append(d.diverged, "width("+pat+")")
	}
}

func (d *foldDifferential) report(t *testing.T, space string) {
	t.Helper()
	if len(d.diverged) > 0 {
		show := d.diverged
		if len(show) > 20 {
			show = show[:20]
		}
		t.Fatalf("%d divergence(s) in the %s space (first 20): %v", len(d.diverged), space, show)
	}
	t.Logf("%s space: %d pairs, %d lexical matches, %d pre-fix-scanner refusals, %d whole-pattern-fold refusals, %d malformed patterns, %d pairs only the shipped rule refuses, 0 divergences",
		space, d.pairs, d.matches, d.legacy, d.wholeFold, d.malformed, d.stricter)
}

// product walks the cartesian product of two enumerated pattern/target sets.
func product(patterns, targets []string) foldDifferential {
	var d foldDifferential
	for _, p := range patterns {
		for _, tg := range targets {
			d.check(p, tg)
		}
	}
	return d
}

// enumerate returns every string over alphabet with length 0..maxLen (inclusive).
func enumerate(alphabet []string, maxLen int) []string {
	out := []string{""}
	level := []string{""}
	for n := 1; n <= maxLen; n++ {
		next := make([]string, 0, len(level)*len(alphabet))
		for _, prefix := range level {
			for _, a := range alphabet {
				next = append(next, prefix+a)
			}
		}
		out = append(out, next...)
		level = next
	}
	return out
}

// TestFoldScannerDifferentialAgainstPathMatch is P-2's contract, mechanically.
//
// The space is EXHAUSTIVE over a small alphabet that contains every byte the r5
// findings turn on - "[", "]", "!", "^", the escape "\" - plus letters, a digit, and
// (fix-list-6 Q4) the two ASCII bytes the ledger path grammar also carries: "-" (the
// timestamp separator in `reports/code/20261024-101010`) and "~" (the 8.3 short-name
// tail the guard refuses). Patterns up to three bytes and targets up to three bytes:
// 1,464 patterns x 585 targets = 856,440 pairs. A second exhaustive phase replaces the
// letters with a MULTI-BYTE non-ASCII one ("é", two UTF-8 bytes) so the scanner meets
// a pattern whose byte length and character length differ: 40 x 40 = 1,600 pairs. A
// deterministic random phase (seed 20261024, 40,000 pairs, patterns up to 8 bytes,
// targets up to 6 bytes) then reaches the longer shapes an exhaustive walk cannot
// afford, e.g. "R[A\]B][x]S" and "Re\[Ports".
//
// The properties are listed in foldDifferential.check: the ledger must refuse every
// pair the authored pattern matches (both policies), every pair the pre-fix scanner
// refused, and every pair D1b's whole-pattern fold refused; and it must stay
// monotone in the fold policy. Any divergence is a failure with the witness pair.
func TestFoldScannerDifferentialAgainstPathMatch(t *testing.T) {
	patternAlphabet := []string{"a", "A", "b", "!", "^", "]", "[", `\`, "1", "-", "~"}
	targetAlphabet := []string{"a", "A", "!", "]", "b", "1", "-", "~"}
	exhaustive := product(enumerate(patternAlphabet, 3), enumerate(targetAlphabet, 3))
	exhaustive.report(t, "exhaustive (alphabet a A b ! ^ ] [ \\ 1 - ~ / a A ! ] b 1 - ~, len<=3)")

	// The non-ASCII phase: a two-byte letter in both roles, with the class and escape
	// bytes around it. FoldLiteralRuns is byte-oriented and the name folds are
	// ASCII-only, so this is the shape where "fold every byte" could collide with a
	// continuation byte; path.Match is the oracle either way.
	nonASCII := []string{"a", "A", "é"}
	multiByte := product(enumerate(nonASCII, 3), enumerate(nonASCII, 3))
	multiByte.report(t, "non-ASCII (alphabet a A é, len<=3)")

	const seed = 20261024
	rng := rand.New(rand.NewSource(seed))
	longPatternAlphabet := append(append([]string{}, patternAlphabet...), "~")
	longTargetAlphabet := []string{"a", "A", "z", "Z", "!", "]", "[", "1", "-", "_", `\`, "^", "~", "é"}
	var random foldDifferential
	for i := 0; i < 40000; i++ {
		pat := randomString(rng, longPatternAlphabet, rng.Intn(9))
		tgt := randomString(rng, longTargetAlphabet, rng.Intn(7))
		random.check(pat, tgt)
	}
	random.report(t, "deterministic random (seed 20261024, 40000 pairs, len<=8/6)")
}

func randomString(rng *rand.Rand, alphabet []string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(alphabet[rng.Intn(len(alphabet))])
	}
	return b.String()
}

// TestFoldScannerKeepsLetterFreePatternsVerbatim is the cheapest possible check that
// the scanner folds LITERALS and nothing else: a pattern carrying no ASCII letter at
// all - classes of symbols, an escaped ']', "!", "^" - must come back byte-identical
// in both forms, because there is nothing in it to fold.
func TestFoldScannerKeepsLetterFreePatternsVerbatim(t *testing.T) {
	for _, pat := range []string{
		`[!]`, `[]]`, `[^]`, `[\]]`, `[!^]`, `!`, `^`, `]`, `[`, `1`, `1-2`,
		`[!]1`, `[\[]`, `x[\]]y`, `[!]!`,
	} {
		if strings.ContainsAny(pat, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			continue
		}
		lo, hi := FoldLiteralRuns(pat)
		if lo != pat || hi != pat {
			t.Errorf("FoldLiteralRuns(%q) = (%q, %q); a letter-free pattern has nothing to fold", pat, lo, hi)
		}
	}
}

// TestFoldScannerNamedRoundFiveWitnesses pins the exact pairs the r5 seats filed, so
// the differential's counts are never the only evidence. Each row states what the
// AUTHORITY must say, and the two pre-fix rules' verdicts are asserted alongside so a
// future "simplification" cannot quietly drop one of the three superset properties.
func TestFoldScannerNamedRoundFiveWitnesses(t *testing.T) {
	cases := []struct {
		name           string
		pattern        string
		target         string
		wantRefuse     bool
		lexicalRefuses bool // path.Match(pattern, target), a malformed pattern counting as a match
		wholeRefuses   bool // D1b's whole-pattern fold
		foldArmOnly    bool // the row's refusals can only come from a fold arm
		why            string
	}{
		{
			// CR-seat1 P1: the escaped ']' is class CONTENT, so "[A\]B]" is the class
			// {A, ], B} and "[x]" is the class {x}; "raxs" meets it only after a fold.
			name: "escaped ']' inside a class (CR-seat1 P1)", pattern: `R[A\]B][x]S`, target: "raxs",
			wantRefuse: true, wholeRefuses: true, foldArmOnly: true,
			why: "the class-preserving fold cannot match it (one class wants upper, the other lower), and the whole-pattern-fold backstop can",
		},
		{
			// SEC-seat1 R5-2: '\\[' is a literal '[', not a class opener.
			name: "escaped '[' literal (SEC-seat1 R5-2)", pattern: `Re\[Ports`, target: "re[ports",
			wantRefuse: true, wholeRefuses: true, foldArmOnly: true,
			why: "the escape is consumed as a pair and the escaped letter folds with the rest of the literal run",
		},
		{
			// charmqwen F-01: '!' is a plain class member; the mirror treated it as a
			// negation marker and lost six pairs.
			name: "[!] is the class {'!'} (charmqwen F-01)", pattern: `[!]aA`, target: "!aa",
			wantRefuse: true, wholeRefuses: true,
			why: "class {'!'} then the folded literal run aa",
		},
		{
			name: "[!] reaches the uppercase spelling too", pattern: `[!]aA`, target: "!AA",
			wantRefuse: true, wholeRefuses: true,
			why: "the upper attempt folds the literals",
		},
		{
			name: "[!] does not become a negation", pattern: `[!]aA`, target: "xa",
			wantRefuse: false,
			why:        "if '!' negated, any byte would match; the class is the single member '!'",
		},
		{
			// Go's own grammar, measured: a ']' as the first class element is
			// ErrBadPattern, not a literal member (the r5 reports described a
			// different regex dialect here). The lexical arm refuses fail-closed.
			name: "leading ']' class is malformed (Go grammar)", pattern: `[]A]x`, target: "Ax",
			wantRefuse: true, lexicalRefuses: true, wholeRefuses: true,
			why: "path.Match answers ErrBadPattern, and a malformed pattern is a possible match",
		},
		{
			name: "negation followed by ']' is malformed (Go grammar)", pattern: `[^]aA`, target: "xaA",
			wantRefuse: true, lexicalRefuses: true, wholeRefuses: true,
			why: "ErrBadPattern again: nrange==0 when the ']' is reached",
		},
		{
			name: "a class member that is '[' inside a class", pattern: `x[A[]y`, target: "xb[y",
			wantRefuse: false,
			why:        "the class is {A, [} and 'b' is in neither member",
		},
		{
			name: "escaped metacharacters in a literal run", pattern: `a\*b`, target: "a*b",
			wantRefuse: true, lexicalRefuses: true, wholeRefuses: true,
			why: "the authored pattern matches it outright",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := foldOn.scopeMayClaimUnder(c.pattern, c.target); got != c.wantRefuse {
				t.Fatalf("scopeMayClaimUnder(%q, %q) with fold on = %v, want %v (%s)", c.pattern, c.target, got, c.wantRefuse, c.why)
			}
			// With the fold off the ledger has exactly one arm left, so its answer must
			// be the runtime matcher's answer - not a stale copy of it.
			if got, want := foldOff.scopeMayClaimUnder(c.pattern, c.target), foldLiteralLexical(c.pattern, c.target); got != want {
				t.Errorf("fold-off verdict for (%q, %q) = %v, want the lexical %v", c.pattern, c.target, got, want)
			}
			if lexical := foldLiteralLexical(c.pattern, c.target); lexical != c.lexicalRefuses {
				t.Errorf("lexical verdict for (%q, %q) = %v, want %v (the row's own premise)", c.pattern, c.target, lexical, c.lexicalRefuses)
			}
			if whole := wholePatternFoldClaim(c.pattern, c.target); whole != c.wholeRefuses {
				t.Errorf("whole-pattern-fold verdict for (%q, %q) = %v, want %v (the row's own premise)", c.pattern, c.target, whole, c.wholeRefuses)
			}
			if c.foldArmOnly && c.lexicalRefuses {
				t.Errorf("row %q is vacuous: the lexical arm already refuses it, so it does not pin a fold arm", c.name)
			}
		})
	}
}

// foldLiteralLexical is the lexical arm alone (path.Match on the authored bytes, with
// a malformed pattern counting as a match), used to prove each witness still needs
// the folded arm it was filed for.
func foldLiteralLexical(pat, tgt string) bool {
	ok, err := path.Match(pat, tgt)
	return err != nil || ok
}

// ---------------------------------------------------------------------------
// P-3: the probe measures the root's own directory and fails closed.
// ---------------------------------------------------------------------------

// TestCaseFoldProbeIsNotAWholePathStat is P-3's contract, in the form this host can
// prove without elevation.
//
// The pre-F2 probe flipped the ASCII case of the WHOLE path and stat'd it, so the
// answer came from the ANCESTORS: a case-sensitive ancestor made the flipped spelling
// die with ENOENT before the root was ever reached, and the probe reported "the
// volume is case-sensitive" about a directory that folds (SEC R5-1). The fixture that
// separates the two is a case-sensitive parent with a case-INsensitive child, which on
// Windows needs `fsutil file setCaseSensitiveInfo` (elevation) and on POSIX needs an
// ext4 casefold directory (also privileged). This test builds it when the host allows
// it and states plainly what it could not measure when it does not.
func TestCaseFoldProbeIsNotAWholePathStat(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "Outer")
	inner := filepath.Join(outer, "Inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	// The measured truth of the directory whose entries the walk compares.
	if folds, conclusive := probeCaseFolding(inner); !conclusive || folds != measureFolder(t, inner) {
		t.Fatalf("probeCaseFolding(%q) = (%v, %v); the directory's own measurement says %v", inner, folds, conclusive, measureFolder(t, inner))
	}
	// Pre-fix mechanism, re-derived: the whole path flipped, stat'd. On this fixture it
	// resolves (NTFS folds every component), which is why a SINGLE-LEVEL fixture cannot
	// expose the defect; the mixed-case fixture below is what does.
	flipped := flipASCIICaseForTest(inner)
	if flipped != inner {
		if _, err := os.Stat(flipped); err == nil {
			t.Logf("whole-path flip of %q resolves on this host, so a single-level fixture cannot separate the two probes (recorded limit)", inner)
		} else {
			t.Logf("whole-path flip of %q does not resolve (%v): this host WOULD have exposed the pre-fix defect", inner, err)
		}
	}

	// The mixed-case fixture: case-sensitive outer, case-INsensitive inner.
	if !setDirCaseSensitive(t, outer, true) {
		t.Skip("this host cannot mark a directory case-sensitive from an unprivileged process (fsutil setCaseSensitiveInfo needs elevation; ext4 casefold needs mkfs/chattr), so the mixed-case fixture (SEC R5-1's /tmp/vfat/Workspace shape) is UNVERIFIED here. The contract asserted instead: the probe answers from the root's own directory pair, and every failure to make that pair stays inconclusive (see TestCaseFoldProbeFailsClosed).")
	}
	if !setDirCaseSensitive(t, inner, false) {
		t.Skip("this host can enable per-directory case sensitivity but not clear it on a child, so the mixed-case fixture is UNVERIFIED here")
	}
	wholePathSays := false
	if _, err := os.Stat(flipASCIICaseForTest(inner)); err == nil {
		wholePathSays = true
	}
	if wholePathSays {
		t.Skipf("the case-sensitive ancestor did not take effect on this host; the fixture is UNVERIFIED here")
	}
	// Pre-fix answer for this fixture: the flipped path dies at the case-sensitive
	// ancestor, which the old code read as "the volume is case-sensitive".
	folds, conclusive := probeCaseFolding(inner)
	if !conclusive || !folds {
		t.Fatalf("probeCaseFolding(%q) = (%v, %v) on a case-insensitive directory below a case-sensitive parent; the pre-fix whole-path probe answered (false, true) here, which is SEC R5-1", inner, folds, conclusive)
	}
	if got := policyFor(inner).foldCase; !got {
		t.Fatalf("policyFor(%q).foldCase = false; the root's own directory folds", inner)
	}
}

// TestCaseFoldProbeFailsClosed pins the direction SEC R5-1 and CR-nwglm F-03 asked
// for: anything that is not a genuine "the other spelling does not exist" stays
// INCONCLUSIVE, and policyFor then folds. The pre-fix code answered "case-sensitive"
// for a root the probe could not even read.
func TestCaseFoldProbeFailsClosed(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "regular.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, p := range []string{
		file,                              // a regular file: no directory to probe inside
		filepath.Join(base, "missing"),    // does not exist
		"",                                // blank
		filepath.Join(base, "..", "nope"), // parent exists, target does not
	} {
		if folds, conclusive := probeCaseFolding(p); conclusive {
			t.Errorf("probeCaseFolding(%q) = (%v, true); a path with no probe-able directory must stay inconclusive", p, folds)
		}
		if !policyFor(p).foldCase {
			t.Errorf("policyFor(%q).foldCase = false; an inconclusive probe must fold", p)
		}
	}
	// A probe-able directory is conclusive and agrees with a direct measurement.
	if folds, conclusive := probeCaseFolding(base); !conclusive || folds != measureFolder(t, base) {
		t.Errorf("probeCaseFolding(%q) = (%v, %v); direct measurement %v", base, folds, conclusive, measureFolder(t, base))
	}
	// And it leaves nothing behind: the probe directory it created is gone.
	entries, rerr := os.ReadDir(base)
	if rerr != nil {
		t.Fatalf("read dir: %v", rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agy-casefold-probe-") {
			t.Errorf("the probe left %q behind", e.Name())
		}
	}
}

// measureFolder answers the same question independently of the implementation: write
// one file, stat its ASCII-case twin in the SAME directory.
func measureFolder(t *testing.T, dir string) bool {
	t.Helper()
	name := filepath.Join(dir, "measure-Aa")
	if err := os.WriteFile(name, nil, 0o600); err != nil {
		t.Fatalf("write the measurement probe: %v", err)
	}
	defer os.Remove(name)
	orig, serr := os.Stat(name)
	if serr != nil {
		t.Fatalf("stat the measurement probe: %v", serr)
	}
	other, oerr := os.Stat(filepath.Join(dir, "measure-aA"))
	return oerr == nil && os.SameFile(orig, other)
}

// flipASCIICaseForTest is the pre-fix probe's helper, kept only so this file can ask
// what the old mechanism would have answered.
func flipASCIICaseForTest(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - ('a' - 'A')
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// setDirCaseSensitive asks the OS to set (or clear) the per-directory case-sensitivity
// flag and reports whether the flag now reads back as wanted.
//
// Windows is the only host this can be attempted on from an unprivileged test:
// `fsutil file setCaseSensitiveInfo` refuses with ERROR_ACCESS_DENIED unless the
// directory's volume and the caller's token allow it, and a POSIX per-directory
// casefold directory needs an ext4 feature set at mkfs time. Returning false is
// therefore a normal outcome, and the caller skips with the reason rather than
// pretending the fixture was built.
func setDirCaseSensitive(t *testing.T, dir string, enable bool) bool {
	t.Helper()
	if runtime.GOOS != "windows" {
		return false
	}
	arg := "disable"
	if enable {
		arg = "enable"
	}
	out, err := exec.Command("fsutil.exe", "file", "setCaseSensitiveInfo", dir, arg).CombinedOutput()
	if err != nil {
		t.Logf("fsutil setCaseSensitiveInfo %s refused (%v): %s", arg, err, strings.TrimSpace(string(out)))
		return false
	}
	query, qerr := exec.Command("fsutil.exe", "file", "queryCaseSensitiveInfo", dir).CombinedOutput()
	if qerr != nil {
		t.Logf("fsutil queryCaseSensitiveInfo failed (%v): %s", qerr, strings.TrimSpace(string(query)))
		return false
	}
	enabled := strings.Contains(string(query), "is enabled")
	return enabled == enable
}
