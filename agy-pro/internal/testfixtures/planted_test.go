package testfixtures

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/tabwriter"
)

// This file is the standing guard for the planted-fixture contract. It was
// added for recorded latent item S3 / F-2 of the TUI follow-up mission: the
// same concatenation trick that keeps secret scanners quiet ("sk" + "_live_...")
// is also a generic pattern-evasion technique, so nothing in the tree asserted
// mechanically that a contract value is synthetic. A contributor could add
// `Leak = "sk" + "_live_" + <24 chars of a real key>` and it would pass the
// pattern grep, pass push protection, and read to a reviewer as one more
// planted fixture. This test folds every constant in planted.go to its runtime
// value and refuses anything it cannot prove synthetic, either by shape or by
// an explicit reviewed entry that pins the exact bytes.
//
// The failure class this closes is the recorded 2026-10-18 RED finding: a real
// DEEPSEEK_API_KEY literal reached a test file and is now in git history.

const plantedPath = "planted.go"

// runtimeInputTokens would let a value arrive from outside the source text. A
// fixture must be a compile-time literal, so any of these spellings in
// planted.go is a failure even if every constant still classifies.
var runtimeInputTokens = []string{"os.", "Getenv", "ReadFile", "http", "exec", "Command"}

type routeName string

const (
	routeShape    routeName = "S"
	routeReviewed routeName = "R"
)

type constant struct {
	name  string
	value string
}

type classification struct {
	name    string
	value   string
	route   routeName
	routeBy string
}

// parseConsts parses the fixture file and folds every constant to its runtime
// value, refusing any top-level declaration that is not a const block. The
// shape rule is closed on purpose: a var, func, type or import is exactly where
// a runtime-supplied or non-literal value could enter the inventory, and a
// closed const file is the property this guard is built on.
func parseConsts(path string, src []byte) ([]constant, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []constant
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.CONST {
				return nil, fmt.Errorf("unexpected top-level %s declaration %q at %s: the fixture file may contain only const declarations, because a var, type or import is where a runtime-supplied or non-literal value could enter the inventory",
					d.Tok.String(), genDeclFirstName(d), fset.Position(d.Pos()))
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					return nil, fmt.Errorf("unexpected %T spec inside the const block at %s", spec, fset.Position(spec.Pos()))
				}
				if len(vs.Values) != len(vs.Names) {
					return nil, fmt.Errorf("constant %q at %s has %d name(s) and %d value(s): an implicit or iota-style value cannot be folded, and an unfoldable value is where a real credential could hide",
						vs.Names[0].Name, fset.Position(vs.Pos()), len(vs.Names), len(vs.Values))
				}
				for i, ident := range vs.Names {
					value, ok := foldString(vs.Values[i])
					if !ok {
						return nil, fmt.Errorf("constant %q at %s is not a foldable string literal concatenation: an unfoldable value is where a real credential could hide",
							ident.Name, fset.Position(ident.Pos()))
					}
					out = append(out, constant{name: ident.Name, value: value})
				}
			}
		case *ast.FuncDecl:
			return nil, fmt.Errorf("unexpected top-level func declaration %q at %s: the fixture file may contain only const declarations, because a func is where a runtime-supplied value could enter the inventory",
				d.Name.Name, fset.Position(d.Pos()))
		default:
			return nil, fmt.Errorf("unexpected top-level %T declaration at %s: the fixture file may contain only const declarations", decl, fset.Position(decl.Pos()))
		}
	}
	return out, nil
}

func genDeclFirstName(gd *ast.GenDecl) string {
	for _, spec := range gd.Specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			if len(s.Names) > 0 {
				return s.Names[0].Name
			}
		case *ast.TypeSpec:
			return s.Name.Name
		}
	}
	return ""
}

// foldString folds a literal concatenation to its runtime value. Only string
// literals joined by + are accepted: a call, an identifier, a non-string
// literal or any other expression could carry a value that no reader can see in
// the source, which is the exact failure this guard exists to prevent.
func foldString(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		unquoted, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return unquoted, true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := foldString(e.X)
		if !ok {
			return "", false
		}
		right, ok := foldString(e.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

const (
	minDigitRun       = 8
	minLetterRun      = 8
	minRepeatRun      = 8
	minInterleavedRun = 10
)

// selfLabels are the words a fixture author uses to say "this is synthetic".
// The list is closed and matched case-insensitively.
var selfLabels = []string{"planted", "plant", "mock", "example", "fake", "sample", "dummy", "test", "shape", "token"}

type predicate struct {
	id     string
	reason string
	match  func(string) bool
}

// shapePredicates are the mechanical shape rules behind Route S. Order matters
// only for which rule is reported first.
var shapePredicates = []predicate{
	{"P1", "sequential digit run (at least 8 ascending digits, 9-to-0 wrap allowed)", hasSequentialDigitRun},
	{"P2", "alphabet run (at least 8 ascending letters, case-insensitive, no wrap)", hasAlphabetRun},
	{"P3", "repeated rune (one character repeated at least 8 times consecutively)", hasRepeatedRuneRun},
	{"P4", "interleaved monotone run (at least 10 strictly alternating letter/digit characters, the letters consecutive ascending and the digits consecutive ascending with 9-to-0 wrap)", hasInterleavedMonotoneRun},
	{"P5", "self-labeled marker (planted, plant, mock, example, fake, sample, dummy, test, shape, token)", hasSelfLabel},
}

// hasSequentialDigitRun accepts a maximal ascending digit run with 9-to-0 wrap:
// hand-counted ascending bodies are the classic synthetic placeholder, and a
// real random body does not contain one by chance at this length.
func hasSequentialDigitRun(s string) bool {
	run := 0
	var prev rune
	for _, r := range s {
		if !isDigit(r) {
			run, prev = 0, 0
			continue
		}
		if run > 0 && int(prev-'0'+1)%10 == int(r-'0') {
			run++
		} else {
			run = 1
		}
		prev = r
		if run >= minDigitRun {
			return true
		}
	}
	return false
}

// hasAlphabetRun accepts a maximal ascending letter run, case-insensitive and
// without wraparound: "abcdefghij" style bodies are typed by hand, while a real
// key body is not alphabetically ordered over 8 or more characters.
func hasAlphabetRun(s string) bool {
	run := 0
	var prev rune
	for _, r := range s {
		if !isLetter(r) {
			run, prev = 0, 0
			continue
		}
		cur := toLower(r)
		if run > 0 && prev+1 == cur {
			run++
		} else {
			run = 1
		}
		prev = cur
		if run >= minLetterRun {
			return true
		}
	}
	return false
}

// hasRepeatedRuneRun accepts one character repeated 8 or more times in a row:
// padding blocks such as ZZZZ or 0000 are placeholders by construction.
func hasRepeatedRuneRun(s string) bool {
	run := 0
	var prev rune
	for _, r := range s {
		if run > 0 && r == prev {
			run++
		} else {
			run = 1
		}
		prev = r
		if run >= minRepeatRun {
			return true
		}
	}
	return false
}

// hasInterleavedMonotoneRun accepts a maximal strictly alternating letter/digit
// sequence whose letters and digits are each consecutive ascending: this is the
// a1b2c3... body every printer-style placeholder uses, and a live random body
// does not produce it.
func hasInterleavedMonotoneRun(s string) bool {
	rs := []rune(s)
	class := func(r rune) byte {
		switch {
		case isDigit(r):
			return 'd'
		case isLetter(r):
			return 'l'
		}
		return 0
	}
	for i := 0; i < len(rs); {
		if class(rs[i]) == 0 {
			i++
			continue
		}
		j := i + 1
		for j < len(rs) {
			cj, cp := class(rs[j]), class(rs[j-1])
			if cj == 0 || cj == cp {
				break
			}
			j++
		}
		if j-i >= minInterleavedRun && monotoneInterleaved(rs[i:j]) {
			return true
		}
		i = j
	}
	return false
}

func monotoneInterleaved(seg []rune) bool {
	letters := 0
	digits := 0
	var prevLetter, prevDigit rune
	for _, r := range seg {
		if isDigit(r) {
			if digits > 0 && int(prevDigit-'0'+1)%10 != int(r-'0') {
				return false
			}
			prevDigit = r
			digits++
			continue
		}
		cur := toLower(r)
		if letters > 0 && prevLetter+1 != cur {
			return false
		}
		prevLetter = cur
		letters++
	}
	return letters >= 2 && digits >= 2
}

// hasSelfLabel accepts a value that names itself synthetic: a self-describing
// marker is the cheapest honest evidence that the bytes are not a real secret.
func hasSelfLabel(s string) bool {
	lower := strings.ToLower(s)
	for _, label := range selfLabels {
		if strings.Contains(lower, label) {
			return true
		}
	}
	return false
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// reviewedEntry pins a constant whose exact bytes are reviewed instead of
// shape-classified. The pin is the whole point: swapping the value for a real
// credential fails here even when the replacement has no recognizable shape.
type reviewedEntry struct {
	value         string
	justification string
}

// reviewedFixtures is Route R. Route precedence: reviewed membership wins over
// a shape match, so every constant lands in exactly one route. The published
// documentation examples (AWS access-key-id, AWS secret-access-key, the jwt.io
// demo segments) are pinned on purpose even where a shape predicate would also
// match: those spellings are the most likely cover story for a real credential,
// and an exact pin is strictly stronger than a shape guess. PKLiveHex is the
// one entry beyond the documentation and jwt.io families; its justification
// says why no predicate can honestly classify it.
//
// Every value below is built from two or more string literals joined with +, so
// that no contiguous provider-key spelling appears in tracked source. A
// contiguous spelling trips push protection, and a local grep exclusion cannot
// help because the scan runs over the pushed bytes, not over a local ignore
// list. The split points are chosen for that rule alone and deliberately do not
// claim to match planted.go's splits: both fold to the same runtime bytes,
// which is all the pin needs, and a concatenation of string literals is still a
// constant expression, so the pin compares exactly the bytes it compared
// before.
var reviewedFixtures = map[string]reviewedEntry{
	"AKIAExample": {
		value:         "AKIA" + "IOSFODNN7EXAMPLE",
		justification: "AWS documentation access-key-id example. Syntactic shape of a live id (AKIA head plus 16 uppercase runes) with an EXAMPLE tail, so the label is the only soft signal; the pin makes the bytes the hard signal.",
	},
	"ASIADocExample": {
		value:         "ASIA" + "IOSFODNN7EXAMPLE",
		justification: "session-credential head spelling of the same AWS documentation example (ASIA + IOSFODNN7EXAMPLE), named by the keyaudit seed rule. Pinned for the same reason as AKIAExample.",
	},
	"AWSSecretExample": {
		value:         "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEKEY",
		justification: "AWS documentation secret-access-key example. The first 32 runes are a real-looking base64 body with a slash; only the exact value separates it from a live key, so shape cannot carry this one alone.",
	},
	"AWSSecretNoSlash": {
		value:         "wJal" + "rXUtnFEMIK7MDENGbPxRfiCY" + "EXAMPLEKEY",
		justification: "separator-stripped variant of the AWS documentation secret example, used to check that separator handling does not change masking. Documentation-derived, therefore pinned.",
	},
	"AWSSecretLong": {
		value:         "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEKEY0123456789ABCDEFGHIJKLMNOPQRST",
		justification: "length variant: the AWS documentation example followed by an ascending tail. Documentation-derived body, pinned exactly.",
	},
	"AWSSecretQuery": {
		value:         "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEQUERY",
		justification: "query-tail variant of the AWS documentation example (EXAMPLEQUERY tail), used for the query-string false-positive case. Documentation-derived, pinned exactly.",
	},
	"AWSSecretShort": {
		value:         "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMP",
		justification: "truncated tail variant of the AWS documentation example: the marker is cut to EXAMP, which contains neither \"example\" nor any other self-label, and the body has no run structure. No shape predicate can honestly classify it, so the exact pin is the only guard.",
	},
	"AWSSecretPlusSeparator": {
		value:         "wJal" + "r+XU/tnF/EMI/K7M/DENG/bPxRfiCY" + "EXAMPLEKEY",
		justification: "plus-and-slash separator variant of the AWS documentation secret example (URL-encoded shape). Documentation-derived, pinned exactly.",
	},
	"JWTHeader": {
		value:         "ey" + "JhbGciOiJIUzI1NiJ9",
		justification: "jwt.io demo header segment (base64url of {\"alg\":\"HS256\"}). Base64url output has no digit or letter run, so only the exact bytes can be checked.",
	},
	"JWTPayload": {
		value:         "ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0",
		justification: "jwt.io demo payload segment (base64url of {\"sub\":\"1234567890\"}), pinned so the demo claim bytes stay fixed.",
	},
	"JWTFull": {
		value:         "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		justification: "jwt.io demo header of a full token (base64url of {\"alg\":\"HS256\",\"typ\":\"JWT\"}). No run structure, pinned exactly.",
	},
	"JWTFullBadSep": {
		value:         "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6kpXVCJ9",
		justification: "deliberately broken base64url spelling of the full header (corrupted separator region), used as the negative JWT case. Pinned so the deliberate corruption cannot drift.",
	},
	"JWTPayloadFull": {
		value:         "ey" + "JzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ",
		justification: "jwt.io demo full payload segment (base64url of {\"sub\":\"1234567890\",\"name\":\"John Doe\",\"iat\":1516239022}), pinned so the demo claim bytes stay fixed.",
	},
	"PKLiveHex": {
		value:         "pk" + "_live_4f9a2b7c1d3e5f6a",
		justification: "16-rune lowercase hex body for the underscore-head family (pk_live_ plus 4f9a2b7c1d3e5f6a). Its digits (4,9,2,7,1,3,5,6) and letters (f,a,b,c,d,e,f,a) are both unordered, so P1 to P5 all miss while the body still reads like a random key body. Reviewed and pinned because the predicates cannot honestly classify it.",
	},
}

// digestHeadRunes is how much of a value fixtureDigest keeps readable: enough to
// tell a family apart ("sk_li", "AKIA"), far too little to reuse.
const digestHeadRunes = 5

// fixtureDigest renders a value the way the repository's display authority
// renders a slot: a short head, a mask, and the byte length. The guard exists
// to catch a real credential, and a caught credential must not be copied
// verbatim into test output, CI logs, or a session report on its way there. The
// head is sliced on runes so a multi-byte rune is never split, and the length is
// in bytes so the digest still identifies which value was seen.
func fixtureDigest(value string) string {
	runes := []rune(value)
	if len(runes) > digestHeadRunes {
		runes = runes[:digestHeadRunes]
	}
	return fmt.Sprintf("%s**** (%d bytes)", string(runes), len(value))
}

// classify assigns one route. Route R is tested first so a reviewed constant
// never counts twice; Route S is the shape fallback; anything else fails and
// names the constant with a digest of its folded value.
func classify(reviewed map[string]reviewedEntry, name, value string) (classification, error) {
	matched := matchShape(value)
	if entry, ok := reviewed[name]; ok {
		if value != entry.value {
			return classification{}, fmt.Errorf("reviewed fixture %q changed value: pinned %s, found %s. Reviewed examples are pinned byte for byte, so a swapped-in real credential fails here",
				name, fixtureDigest(entry.value), fixtureDigest(value))
		}
		evidence := "reviewed: " + entry.justification
		if len(matched) > 0 {
			evidence += " (shape also matches " + predicateIDs(matched) + ")"
		}
		return classification{name: name, value: value, route: routeReviewed, routeBy: evidence}, nil
	}
	if len(matched) == 0 {
		return classification{}, fmt.Errorf("constant %q with folded value %s matches no shape predicate and is not in the reviewed table: build the value from a recognizable synthetic shape, or add a reviewed entry that pins the exact bytes with a written justification",
			name, fixtureDigest(value))
	}
	return classification{name: name, value: value, route: routeShape, routeBy: describePredicates(matched)}, nil
}

func matchShape(value string) []predicate {
	var matched []predicate
	for _, p := range shapePredicates {
		if p.match(value) {
			matched = append(matched, p)
		}
	}
	return matched
}

func predicateIDs(matched []predicate) string {
	ids := make([]string, 0, len(matched))
	for _, p := range matched {
		ids = append(ids, p.id)
	}
	return strings.Join(ids, ", ")
}

func describePredicates(matched []predicate) string {
	parts := make([]string, 0, len(matched))
	for _, p := range matched {
		parts = append(parts, p.id+": "+p.reason)
	}
	return strings.Join(parts, "; ")
}

// TestPlantedFixturesAreSynthetic is the guard itself: read the fixture file
// from disk, prove its shape is closed, fold and classify every constant, and
// account for every one of them.
func TestPlantedFixturesAreSynthetic(t *testing.T) {
	src, err := os.ReadFile(plantedPath)
	if err != nil {
		t.Fatalf("read %s: %v", plantedPath, err)
	}
	if len(src) == 0 {
		t.Fatalf("%s is empty", plantedPath)
	}
	for _, token := range runtimeInputTokens {
		if bytes.Contains(src, []byte(token)) {
			t.Errorf("%s contains %q: a fixture value must be a compile-time literal, so no runtime-input spelling may appear in the file", plantedPath, token)
		}
	}

	consts, err := parseConsts(plantedPath, src)
	if err != nil {
		t.Fatalf("fixture file shape check failed for %s: %v", plantedPath, err)
	}
	if len(consts) == 0 {
		t.Fatalf("%s declares no constants: the inventory collapsed and this guard would pass vacuously", plantedPath)
	}

	names := make(map[string]bool, len(consts))
	for _, c := range consts {
		if names[c.name] {
			t.Errorf("constant %q is inventoried more than once", c.name)
		}
		names[c.name] = true
	}
	reviewedNames := make([]string, 0, len(reviewedFixtures))
	for name, entry := range reviewedFixtures {
		reviewedNames = append(reviewedNames, name)
		if entry.value == "" {
			t.Errorf("reviewed fixture %q has an empty pinned value", name)
		}
		if strings.TrimSpace(entry.justification) == "" {
			t.Errorf("reviewed fixture %q has no written justification", name)
		}
		if !names[name] {
			t.Errorf("reviewed fixture %q does not exist in %s: a stale reviewed entry is a failure", name, plantedPath)
		}
	}
	sort.Strings(reviewedNames)

	var (
		buf        strings.Builder
		shapeN     int
		reviewedN  int
		classified int
	)
	w := tabwriter.NewWriter(&buf, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "CONSTANT\tROUTE\tBYTES\tEVIDENCE")
	for _, c := range consts {
		if c.value == "" {
			t.Errorf("constant %q folds to an empty string: an empty fixture classifies nothing", c.name)
			fmt.Fprintf(w, "%s\t%s\t0\t%s\n", c.name, "MISSING", "empty value")
			continue
		}
		got, err := classify(reviewedFixtures, c.name, c.value)
		if err != nil {
			t.Errorf("unclassified fixture: %v", err)
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", c.name, "UNCLASSIFIED", len(c.value), "no shape predicate and no reviewed entry")
			continue
		}
		classified++
		switch got.route {
		case routeShape:
			shapeN++
		case routeReviewed:
			reviewedN++
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", got.name, got.route, len(got.value), got.routeBy)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush classification table: %v", err)
	}
	t.Logf("planted fixture classification: %d constants, %d classified (%d Route S, %d Route R)\n%s",
		len(consts), classified, shapeN, reviewedN, strings.TrimRight(buf.String(), "\n"))
	t.Logf("Route R inventory (%d): %s", len(reviewedNames), strings.Join(reviewedNames, ", "))

	if shapeN+reviewedN != len(consts) {
		t.Errorf("inventory accounting mismatch: %d Route S + %d Route R = %d, want %d constants (every constant must land in exactly one route)",
			shapeN, reviewedN, shapeN+reviewedN, len(consts))
	}
}

// TestClassificationRejectsNonSynthetic keeps the teeth permanent without
// touching the tree: the same classify and parse paths that guard planted.go
// must reject an unlabelled probe value, a swapped reviewed value, an
// unfoldable expression and a non-const declaration.
func TestClassificationRejectsNonSynthetic(t *testing.T) {
	probeLeak := "sk" + "_live_9fK2mQ7xR4tY8wZ1bN3pL0"
	probeSwap := "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY9fK2mQ7x"

	t.Run("unclassified constant fails", func(t *testing.T) {
		_, err := classify(reviewedFixtures, "ProbeLeak", probeLeak)
		if err == nil {
			t.Fatalf("ProbeLeak classified as synthetic; a realistic random-looking body with no label and no run must fail")
		}
		if !strings.Contains(err.Error(), "ProbeLeak") || !strings.Contains(err.Error(), fixtureDigest(probeLeak)) {
			t.Fatalf("unclassified error must name the constant and a digest of its value, got: %v", err)
		}
		if strings.Contains(err.Error(), probeLeak) {
			t.Fatalf("unclassified error echoes the rejected value verbatim; a caught credential must never be copied into test output, got: %v", err)
		}
		if !strings.Contains(err.Error(), "matches no shape predicate") {
			t.Fatalf("unclassified error must route through the unclassified path, got: %v", err)
		}
	})

	t.Run("reviewed value swap fails", func(t *testing.T) {
		_, err := classify(reviewedFixtures, "AWSSecretExample", probeSwap)
		if err == nil {
			t.Fatalf("a reviewed constant accepted a different value; the exact pin is the guard")
		}
		if !strings.Contains(err.Error(), "reviewed fixture") || !strings.Contains(err.Error(), "pinned") {
			t.Fatalf("reviewed swap must route through the reviewed-value pin, got: %v", err)
		}
		if strings.Contains(err.Error(), probeSwap) {
			t.Fatalf("reviewed swap error echoes the swapped-in value verbatim; a real credential must never be copied into test output, got: %v", err)
		}
		if !strings.Contains(err.Error(), fixtureDigest(probeSwap)) {
			t.Fatalf("reviewed swap error must report a digest of the value it found, got: %v", err)
		}
	})

	t.Run("unfoldable constant fails", func(t *testing.T) {
		src := "package testfixtures\n\nconst Bad = len(\"x\")\n"
		_, err := parseConsts("probe.go", []byte(src))
		if err == nil {
			t.Fatalf("an unfoldable constant was accepted; it must fail naming the constant")
		}
		if !strings.Contains(err.Error(), "Bad") || !strings.Contains(err.Error(), "foldable") {
			t.Fatalf("unfoldable error must name the constant and the fold rule, got: %v", err)
		}
	})

	t.Run("non-const declaration fails", func(t *testing.T) {
		src := "package testfixtures\n\nvar Bad = \"x\"\n"
		_, err := parseConsts("probe.go", []byte(src))
		if err == nil {
			t.Fatalf("a top-level var was accepted; the fixture file shape must stay closed to const declarations")
		}
		if !strings.Contains(err.Error(), "var") || !strings.Contains(err.Error(), "only const declarations") {
			t.Fatalf("non-const error must name the declaration kind and the rule, got: %v", err)
		}
	})
}

// providerPatternGate is the standing 15-alternative provider-key pattern the
// repository greps over its tracked Go tree, recorded as section 10 of the F1
// follow-up. This file is source like any other, so the reviewed values above
// are split to satisfy it. The pattern text is safe against itself: every
// alternative needs a run of trailing runes immediately after its head, and the
// source text has a character class on the far side of that boundary.
//
// The hyphen-family arm is composed as `sk-live` plus six-or-more body runes
// rather than `sk-live-` plus six, so the one arm covers both live spellings: a
// value that keeps a separator after the head and a value whose body continues
// straight past "live". The narrow form admitted the second spelling with zero
// hits, and two standing copies of this rule had already drifted apart by the
// time it was measured, so the composed arm is the only one kept.
const providerPatternGate = `sk_live_[A-Za-z0-9]{6,}|pk_live_[A-Za-z0-9]{6,}|ghp_[A-Za-z0-9]{16,}|AKIA[A-Z0-9]{12,}|ASIA[A-Z0-9]{16}|AIza[A-Za-z0-9_-]{30,}|wJalr[A-Za-z0-9/+]{20,}|nvapi-[A-Za-z0-9-]{10,}|gsk_[A-Za-z0-9]{10,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-api03-[A-Za-z0-9-]{10,}|eyJ[A-Za-z0-9_-]{10,}|sk-proj-[A-Za-z0-9]{12,}|Sk_Live_[A-Za-z0-9]{16}|sk-live[A-Za-z0-9-]{6,}`

// offsetPosition converts a byte offset in src to a 1-based line and column, so
// a whole-file scan can report file:line:col for every match. The line is exact.
// The column counts BYTES from the start of the line, not runes, because
// FindAllStringIndex reports byte offsets and this function never decodes runes.
// For a line whose text before the match is pure ASCII the byte column and the
// rune column are the same number. For a line carrying a multi-byte rune before
// the match the reported column is larger than the rune column, by one per extra
// continuation byte. An ASCII match body does not make the column a rune column:
// what precedes the match on that line decides. This is a diagnostic position
// that a human reads to find a line, so the byte column is reported unchanged
// rather than converted.
func offsetPosition(src []byte, offset int) (line, column int) {
	line, column = 1, 1
	if offset > len(src) {
		offset = len(src)
	}
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return line, column
}

// gateMatch is one contiguous provider-pattern hit, reported as a byte position
// in the scanned source. The digest stands in for the matched bytes so a caught
// credential is never copied into test output or CI logs verbatim.
type gateMatch struct {
	line   int
	column int
	bytes  int
	digest string
}

// scanProviderPattern applies the provider pattern gate to src in memory and
// returns every match with its 1-based line and byte column. The scan is split
// out of the reading test so the detection and the offset reporting can be
// proven against synthetic sources, with no file mutated on disk to prove them.
func scanProviderPattern(gate *regexp.Regexp, src []byte) []gateMatch {
	text := string(src)
	locs := gate.FindAllStringIndex(text, -1)
	matches := make([]gateMatch, 0, len(locs))
	for _, loc := range locs {
		line, column := offsetPosition(src, loc[0])
		matches = append(matches, gateMatch{
			line:   line,
			column: column,
			bytes:  loc[1] - loc[0],
			digest: fixtureDigest(text[loc[0]:loc[1]]),
		})
	}
	return matches
}

// TestReviewedFixturesSourceHasNoContiguousPattern is the permanent self-guard
// for the table above. The split that keeps the gate quiet is easy to undo by
// accident, so this test applies the same gate the tree is held to over both
// files that carry the rule: planted_test.go, which pins the reviewed values,
// and planted.go, which holds the contract constants a future contributor would
// edit. Without the second file a contiguous spelling could land in the
// contract, leave this package green, and be caught only by push protection
// after a review round had already passed.
//
// The scan runs over the whole file content rather than line by line, so every
// match is reported with a file:line:col position instead of only the first one
// per line. A concatenation-built value still cannot match: the source text
// between two operands (" +\n\t") breaks the run.
//
// This test only proves the clean case, so the detection and reporting path it
// depends on is proven separately by TestProviderPatternScanReportsExactPositions
// against synthetic sources held in memory. That keeps the failure path covered
// without appending a real spelling to a file on disk.
func TestReviewedFixturesSourceHasNoContiguousPattern(t *testing.T) {
	gate := regexp.MustCompile(providerPatternGate)
	probe := "ey" + "JhbGciOiJIUzI1NiJ9"
	if !gate.MatchString(probe) {
		t.Fatalf("the gate does not match its own probe %s: a pattern that matches nothing would clear every file vacuously", fixtureDigest(probe))
	}

	// Non-vacuity probes for the hyphen-family arm. Both values fold at compile
	// time from fragments, so this file carries no contiguous spelling of
	// either; what the gate sees is the runtime bytes. The first probe is the
	// separator spelling, the second is the no-hyphen continuation that the
	// narrow `sk-live-` form missed entirely. Both must be flagged, or the arm
	// only looks like it covers the family.
	for _, hyphen := range []string{
		"sk-" + "live-" + "0123456789abcdefXYZ",
		"sk-" + "livekey" + "1234567890abcd",
	} {
		if !gate.MatchString(hyphen) {
			t.Fatalf("the gate does not match its own hyphen-family probe %s: the arm would report zero hits over a planted spelling of this family", fixtureDigest(hyphen))
		}
	}

	// The other half of the same claim: the fragments that carry a hyphen-family
	// value in tracked source must report zero matches, or the probe above would
	// be satisfied by a pattern that also trips on every split fixture. The
	// source is assembled at runtime, so this file stays free of the contiguous
	// spelling it is asserting about.
	hyphenParts := []string{"sk-", "live-", "0123456789abcdefXYZ"}
	quotedParts := make([]string, 0, len(hyphenParts))
	for _, part := range hyphenParts {
		quotedParts = append(quotedParts, `"`+part+`"`)
	}
	fragmentedSrc := []byte("package probe\nconst Leak = " + strings.Join(quotedParts, " + ") + "\n")
	if bytes.Contains(fragmentedSrc, []byte(strings.Join(hyphenParts, ""))) {
		t.Fatalf("the fragmented control source still carries the contiguous hyphen-family spelling, so its zero-match result would be vacuous")
	}
	if got := len(scanProviderPattern(gate, fragmentedSrc)); got != 0 {
		t.Fatalf("scan reported %d match(es) over a hyphen-family value joined with +, want 0: %+v", got, scanProviderPattern(gate, fragmentedSrc))
	}

	for _, path := range []string{"planted_test.go", plantedPath} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(src) == 0 {
			t.Fatalf("%s is empty: an empty source clears this guard vacuously", path)
		}
		matches := scanProviderPattern(gate, src)
		for _, m := range matches {
			t.Errorf("%s:%d:%d: matches the provider pattern gate (%s, %d bytes): every fixture value must be built from two or more literals joined with +, because a contiguous provider-key spelling in tracked source trips push protection",
				path, m.line, m.column, m.digest, m.bytes)
		}
		if len(matches) == 0 {
			t.Logf("provider pattern gate applied to %s: 0 matches", path)
		}
	}
}

// TestProviderPatternScanReportsExactPositions is the permanent negative test
// for the reporting machinery behind the self-guard above. Before this test the
// failure path of that guard, both the offset-to-line-column conversion and the
// masked digest in the message, was proven only by mutating a file on disk
// during a review round, so a silent break in the reporting would have left the
// guard printing a wrong position or no position at all while staying green.
//
// The probe value is assembled at runtime from a []string, so this file holds
// no contiguous provider-key spelling of its own and the self-guard above stays
// clean over it.
func TestProviderPatternScanReportsExactPositions(t *testing.T) {
	gate := regexp.MustCompile(providerPatternGate)

	parts := []string{"sk", "_live_", "9fK2mQ7x"}
	contiguous := strings.Join(parts, "")
	const wantBytes = 16

	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, `"`+part+`"`)
	}
	concatenated := strings.Join(quoted, " + ")

	t.Run("contiguous spelling is reported at the exact line and column", func(t *testing.T) {
		src := []byte("package probe\n" +
			`const LeakA = "` + contiguous + "\"\n" +
			`const LeakB = "` + contiguous + "\"\n")

		matches := scanProviderPattern(gate, src)
		if len(matches) != 2 {
			t.Fatalf("scan reported %d matches over a source carrying two planted contiguous spellings, want exactly 2: %+v", len(matches), matches)
		}
		want := []struct{ line, column int }{{2, 16}, {3, 16}}
		for i, w := range want {
			if matches[i].line != w.line {
				t.Errorf("match %d reported line %d, want %d", i, matches[i].line, w.line)
			}
			if matches[i].column != w.column {
				t.Errorf("match %d reported column %d, want %d", i, matches[i].column, w.column)
			}
			if matches[i].bytes != wantBytes {
				t.Errorf("match %d reported %d matched bytes, want %d", i, matches[i].bytes, wantBytes)
			}
			if matches[i].digest == "" {
				t.Errorf("match %d reported no digest, so the failure message would name no value", i)
			}
			if strings.Contains(matches[i].digest, contiguous) {
				t.Errorf("match %d reported the matched bytes verbatim in its digest, so a caught credential would reach CI logs, got %q", i, matches[i].digest)
			}
		}
	})

	t.Run("concatenation spelling reports zero matches", func(t *testing.T) {
		if !gate.MatchString(contiguous) {
			t.Fatalf("the probe value does not match the gate, so a zero on the concatenated form would prove nothing")
		}
		src := []byte("package probe\nconst LeakC = " + concatenated + "\n")
		if bytes.Contains(src, []byte(contiguous)) {
			t.Fatalf("the concatenated control source still carries the contiguous spelling, so its zero-match result would be vacuous")
		}
		matches := scanProviderPattern(gate, src)
		if len(matches) != 0 {
			t.Fatalf("scan reported %d matches over a source whose value is joined with +, want 0: %+v", len(matches), matches)
		}
	})
}
