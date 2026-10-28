package codework

// Doc-claim pins for `agy-pro/docs/code.md` (U6): every pinned sentence is
// checked against the code that implements it in the SAME test, so a sentence
// that stops describing the door fails here instead of passing on stale prose.
// House pattern: internal/fanout/docs_claims_test.go (docWS/docHasSentence plus a
// non-vacuity arm), widened to the verb's own source for the surface claims that
// live in cmd/agy/pro_code.go.
//
// Five sentences, six assertions:
//   - the subcommand list (§2) against the runCodeCmd switch;
//   - the exit-code line (§3) against the shipped usage text, plus the
//     "failed verification is manual rollback" rule against a real WritePhase;
//   - the no-cap sentence (§4.6) against a rendered metered report;
//   - the archive-move sentence (§4.4) against a real codesnap Take + Rollback;
//   - the consent sentence (§4.5) against the gate's two branches;
//   - the verification trust model (§4.8: compile-only detection, explicit exec,
//     the run budget, the untrusted-data fence) and the plan-ingestion /
//     effective-scope rules (§4.9) against the constants and the phase behavior.

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"agypro/internal/codesnap"
)

// The doc and the verb live outside this package; both paths are relative to
// internal/codework.
const (
	codeDocPath  = "../../docs/code.md"
	codeVerbPath = "../../cmd/agy/pro_code.go"
)

// codeDocRead loads docs/code.md and cmd/agy/pro_code.go once per test.
func codeDocRead(t *testing.T) (doc, verb string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(codeDocPath))
	if err != nil {
		t.Fatalf("read docs/code.md: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(codeVerbPath))
	if err != nil {
		t.Fatalf("read cmd/agy/pro_code.go: %v", err)
	}
	return string(data), string(src)
}

// codeDocWS collapses whitespace runs so a pin survives a legitimate re-wrap of
// the sentence and still fails on a wording regression.
func codeDocWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// codeDocHas reports whether want is present in doc modulo whitespace.
func codeDocHas(doc, want string) bool {
	return strings.Contains(codeDocWS(doc), codeDocWS(want))
}

// enclosingFuncDecl reads the function name out of a trimmed `func ...` line, with
// or without a receiver: "func pruneLedgerKeys() error {" -> "pruneLedgerKeys",
// "func (p namePolicy) pathUnder(...)" -> "pathUnder". A name it cannot parse comes
// back as the remaining text, which no allow-list matches - the safe direction for a
// pin.
func enclosingFuncDecl(trimmed string) string {
	rest := strings.TrimPrefix(trimmed, "func ")
	if strings.HasPrefix(rest, "(") {
		if i := strings.Index(rest, ")"); i >= 0 {
			rest = strings.TrimSpace(rest[i+1:])
		}
	}
	if i := strings.Index(rest, "("); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

// codeDocCodeNames reads the subcommand names out of the runCodeCmd switch: every
// quoted case token that is neither a flag spelling nor the `help` case.
func codeDocCodeNames(t *testing.T, verb string) []string {
	t.Helper()
	caseLine := regexp.MustCompile(`(?m)^\s*case ((?:"[^"]+"(?:,\s*)?)+):`)
	token := regexp.MustCompile(`"([^"]+)"`)
	var names []string
	for _, m := range caseLine.FindAllStringSubmatch(verb, -1) {
		for _, tok := range token.FindAllStringSubmatch(m[1], -1) {
			v := tok[1]
			if strings.HasPrefix(v, "-") || v == "help" {
				continue
			}
			names = append(names, v)
		}
	}
	return names
}

// TestCodeDocPinsSubcommandSurface pins the §2 sentence "exactly five
// subcommands" to the dispatch switch: the names the sentence lists must be the
// names runCodeCmd accepts, in both directions.
func TestCodeDocPinsSubcommandSurface(t *testing.T) {
	doc, verb := codeDocRead(t)

	const sentence = "`agy pro code` has exactly five subcommands: `plan`, `write`, `review`, `rollback`, and `run`."
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the subcommand sentence verbatim; missing:\n  %s", sentence)
	}

	// The names the sentence claims.
	var claimed []string
	for _, m := range regexp.MustCompile("`([a-z]+)`").FindAllStringSubmatch(sentence, -1) {
		claimed = append(claimed, m[1])
	}
	// The names the door dispatches.
	implemented := codeDocCodeNames(t, verb)
	if len(implemented) != len(claimed) {
		t.Fatalf("runCodeCmd accepts %v (%d names), the doc sentence lists %v (%d names)",
			implemented, len(implemented), claimed, len(claimed))
	}
	inCode := map[string]bool{}
	for _, n := range implemented {
		inCode[n] = true
	}
	for _, n := range claimed {
		if !inCode[n] {
			t.Errorf("docs/code.md claims subcommand %q that runCodeCmd does not dispatch (implemented: %v)", n, implemented)
		}
		if !codeDocHas(doc, "agy pro code "+n) {
			t.Errorf("docs/code.md must show `agy pro code %s` somewhere in its surface section", n)
		}
	}
	// --help is a dispatch case of its own, not a subcommand; the doc names it.
	if !strings.Contains(verb, `case "help", "--help", "-h":`) || !strings.Contains(doc, "--help") {
		t.Error("the doc's --help claim and the runCodeCmd help case must both exist")
	}

	// Non-vacuity: the same predicate rejects a regressed sentence.
	if codeDocHas("`agy pro code` has exactly four subcommands: `plan`, `write`, `review`, `rollback`", sentence) {
		t.Error("pin is vacuous: it still matches a sentence that drops a subcommand")
	}
}

// TestCodeDocPinsFlagSurface pins §2's "every flag is registered on the
// subcommand's own fail-closed flag.FlagSet" to the registrations in
// cmd/agy/pro_code.go: no documented flag may be invented, and no registered flag
// may be undocumented. The fanout flags the doc also mentions (`--units`,
// `--tools`) are checked against the parser that owns them.
func TestCodeDocPinsFlagSurface(t *testing.T) {
	doc, verb := codeDocRead(t)

	const sentence = "Every flag is registered on the subcommand's own fail-closed `flag.FlagSet`, so an unknown flag, an unparsable value or a stray positional operand is exit 2 before anything runs."
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the flag-surface sentence verbatim; missing:\n  %s", sentence)
	}

	registration := regexp.MustCompile(`(?:StringVar|BoolVar|IntVar|Var|String|Bool|Int)\([^)\n]*?"([a-z][a-z0-9-]*)"`)
	registered := func(src string) map[string]bool {
		out := map[string]bool{}
		for _, m := range registration.FindAllStringSubmatch(src, -1) {
			out[m[1]] = true
		}
		return out
	}
	verbFlags := registered(verb)
	fanoutSrc, err := os.ReadFile(filepath.Join("..", "fanout", "cli.go"))
	if err != nil {
		t.Fatalf("read internal/fanout/cli.go: %v", err)
	}
	fanoutFlags := registered(string(fanoutSrc))
	if len(verbFlags) == 0 || len(fanoutFlags) == 0 {
		t.Fatal("premise broken: the registration scan found no flags")
	}

	// Doc -> code: every documented flag must exist somewhere in the surface.
	docFlag := regexp.MustCompile(`--[a-z][a-z0-9-]*`)
	seen := map[string]bool{}
	for _, flag := range docFlag.FindAllString(doc, -1) {
		name := strings.TrimPrefix(flag, "--")
		if seen[name] {
			continue
		}
		seen[name] = true
		if name == "help" {
			if !strings.Contains(verb, `"--help"`) {
				t.Errorf("docs/code.md documents --help but runCodeCmd has no such case")
			}
			continue
		}
		if !verbFlags[name] && !fanoutFlags[name] {
			t.Errorf("docs/code.md documents --%s, which neither cmd/agy/pro_code.go nor internal/fanout/cli.go registers", name)
		}
	}

	// Code -> doc: every flag the verb registers must be documented.
	for name := range verbFlags {
		if !seen[name] {
			t.Errorf("cmd/agy/pro_code.go registers --%s but docs/code.md never documents it", name)
		}
	}

	// Non-vacuity: an invented flag must be rejected by the same scan.
	if verbFlags["bogus"] || fanoutFlags["bogus"] {
		t.Fatal("premise broken: an unregistered flag name must not resolve")
	}
}

// TestCodeDocPinsPerSubcommandFlags pins the §2 flag table row by row: the
// subcommand each flag is claimed for must be the subcommand whose FlagSet
// registers it. A flag moved to the wrong column is an operator-facing lie that
// the whole-document scan above cannot see.
func TestCodeDocPinsPerSubcommandFlags(t *testing.T) {
	doc, verb := codeDocRead(t)

	// One chunk per subcommand function (top-level `func ` boundaries).
	subcommands := []struct {
		fn     string
		column int
	}{
		{"runCodePlanCmd", 0},
		{"runCodeWriteCmd", 1},
		{"runCodeReviewCmd", 2},
		{"runCodeRollbackCmd", 3},
		{"runCodeRunCmd", 4},
	}
	registration := regexp.MustCompile(`(?:StringVar|BoolVar|IntVar|Var|String|Bool|Int)\([^)\n]*?"([a-z][a-z0-9-]*)"`)
	common := []string{"root", "out", "timeout", "json", "dry-run"}

	chunks := map[string]string{}
	for _, part := range strings.Split(verb, "\nfunc ") {
		name := part
		if i := strings.IndexAny(name, "("); i >= 0 {
			name = name[:i]
		}
		chunks[name] = part
	}
	codeFlags := make([]map[string]bool, len(subcommands))
	for i, sc := range subcommands {
		body, ok := chunks[sc.fn]
		if !ok {
			t.Fatalf("premise broken: %s not found in cmd/agy/pro_code.go", sc.fn)
		}
		set := map[string]bool{}
		for _, m := range registration.FindAllStringSubmatch(body, -1) {
			set[m[1]] = true
		}
		if strings.Contains(body, "addCodeCommonFlags(") {
			for _, name := range common {
				set[name] = true
			}
		}
		if len(set) == 0 {
			t.Fatalf("premise broken: %s registers no flags", sc.fn)
		}
		codeFlags[i] = set
	}

	// The doc's table: `| `--flag ...` | yes | - | ... |` in the fixed column
	// order plan, write, review, rollback, run.
	flagCell := regexp.MustCompile("`--([a-z][a-z0-9-]*)")
	rows := 0
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(line, "| `--") {
			continue
		}
		cells := strings.Split(strings.TrimSpace(strings.Trim(line, "|")), "|")
		if len(cells) != len(subcommands)+1 {
			t.Fatalf("doc table row %q has %d cells, want %d", line, len(cells), len(subcommands)+1)
		}
		m := flagCell.FindStringSubmatch(cells[0])
		if m == nil {
			t.Fatalf("doc table row %q names no flag", line)
		}
		name := m[1]
		rows++
		for _, sc := range subcommands {
			claimed := strings.TrimSpace(cells[sc.column+1]) != "-"
			if claimed != codeFlags[sc.column][name] {
				t.Errorf("docs/code.md table says --%s is %s for %s, the FlagSet says it is %s",
					name, claimedWord(claimed), subcommands[sc.column].fn, claimedWord(codeFlags[sc.column][name]))
			}
		}
	}
	if rows == 0 {
		t.Fatal("premise broken: the doc's flag table was not found")
	}
}

// claimedWord renders a table cell's meaning for an error message.
func claimedWord(on bool) string {
	if on {
		return "present"
	}
	return "absent"
}

// TestCodeDocPinsExitCodesAndManualRollback pins §3: the exit-code line is the
// shipped usage text (the contract source of truth), and the "a failed
// verification is a run failure whose report names the rollback command, never an
// automatic rollback" rule is what a real verify-failed write phase does.
func TestCodeDocPinsExitCodesAndManualRollback(t *testing.T) {
	doc, verb := codeDocRead(t)

	// The usage text owns the exit-code contract; the doc quotes it.
	usageLine := regexp.MustCompile(`(?s)Exit codes:(.*?)\nArtifacts:`).FindStringSubmatch(verb)
	if usageLine == nil {
		t.Fatal("premise broken: cmd/agy/pro_code.go carries no `Exit codes:` usage line")
	}
	if want := "Exit codes:" + usageLine[1]; !codeDocHas(doc, want) {
		t.Errorf("docs/code.md must carry the shipped exit-code line verbatim; missing:\n  %s", want)
	}
	const mockRollback = "1 run failure (a failed verification is one; the report names the rollback command)"
	if !codeDocHas(doc, mockRollback) {
		t.Errorf("docs/code.md must state that a failed verification is a run failure; missing:\n  %s", mockRollback)
	}

	// Behavior: a failed verification is a RESULT, the tree keeps its writes, and
	// the undo is a command the operator runs.
	root, outDir := phaseTree(t)
	rec := installPhaseRecorder(t, &phaseRecorder{verifyPass: []bool{false}})
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Ask:        "do the alpha work",
		WriterSeat: providerSeat,
		Yes:        true,
		MaxRepairs: 0,
		VerifyCmds: [][]string{{"go", "build", "./internal/alpha"}},
	}
	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v (a failed verification is a result, not an error)", err)
	}
	if res.Status != StatusVerifyFailed {
		t.Fatalf("status = %q, want %q (the documented run failure)", res.Status, StatusVerifyFailed)
	}
	if phaseCallIndex(rec.calls, "rollback") >= 0 {
		t.Fatalf("calls = %v, the write phase rolled back on its own", rec.calls)
	}
	report := AssembleReport(cfg, nil, res)
	if report.Status != StatusVerifyFailed {
		t.Errorf("report status = %q, want %q", report.Status, StatusVerifyFailed)
	}
	if report.Rollback == nil || !strings.Contains(report.Rollback.Command, "agy pro code rollback --run") {
		t.Errorf("report.Rollback = %+v, want the documented manual rollback command", report.Rollback)
	}
	if !strings.Contains(report.Render(), "rollback: agy pro code rollback --run") {
		t.Errorf("render =\n%s\nwant the rollback line the doc names", report.Render())
	}
}

// TestCodeDocPinsNoCostCap pins §4.6's no-cap sentence to the rendered report: the
// cost line states the absence of a cap and carries no dollar figure, because no
// price table exists in this path.
func TestCodeDocPinsNoCostCap(t *testing.T) {
	doc, _ := codeDocRead(t)

	const sentence = "No token or dollar cap is enforced anywhere in this door: no flag, no default, and no code path compares a spend against a ceiling, so the report prints no dollar figure."
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the no-cap sentence verbatim; missing:\n  %s", sentence)
	}

	root, outDir := phaseTree(t)
	installPhaseRecorder(t, &phaseRecorder{verifyPass: []bool{true}})
	cfg := CodeConfig{
		Root:       root,
		OutDir:     outDir,
		Ask:        "do the alpha work",
		WriterSeat: providerSeat,
		Yes:        true,
		VerifyCmds: [][]string{{"go", "build", "./internal/alpha"}},
	}
	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	rendered := AssembleReport(cfg, nil, res).Render()

	var costLine string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, "cost: ") {
			costLine = line
			break
		}
	}
	if costLine == "" {
		t.Fatalf("render =\n%s\nwant a cost line", rendered)
	}
	if !strings.Contains(costLine, "no token/cost cap is enforced") {
		t.Errorf("cost line = %q, want the no-cap statement", costLine)
	}
	if strings.Contains(costLine, "$") {
		t.Errorf("cost line = %q carries a dollar figure; this path has no price table", costLine)
	}
	if !strings.Contains(costNote, "no token/cost cap is enforced") {
		t.Errorf("costNote = %q drifted from the pinned sentence", costNote)
	}
}

// TestCodeDocPinsNeverDeletes pins §4.4's archive-move sentence to codesnap: a
// created file is MOVED into rollback-archive/ (not deleted), the restored file
// is byte-identical, and the package's removal call sites are exactly the TWO the
// sentence names - the key-store retention prune (inside the agy-pro state home,
// never a root) and the case-fold probe's cleanup of its own throw-away directory.
//
// The static arm was a blanket "no os.Remove anywhere" until FIX-F5. That sentence
// stopped being true when the key store gained a retention prune, so the pin
// restated it as a COUNT of removal call sites. FIX-G1 widened the matcher from the
// exact token "os.Remove(" to the PREFIX "os.Remove", so os.RemoveAll is counted
// too: the probe's cleanup uses exactly that shape and was invisible before. The
// count is therefore TWO by measurement, each site tied to its function, and a new
// removal anywhere else still fails this pin.
func TestCodeDocPinsNeverDeletes(t *testing.T) {
	doc, _ := codeDocRead(t)

	const sentence = "A file the run created is moved into `<out>/rollback-archive/`, and neither a diff\nnor a rollback deletes anything in the workspace: the package has exactly two\nremoval call sites, and neither can reach a file the run owns - the key-store\nretention prune, which works inside `~/.agy-pro/ledger-keys` and never touches a\nroot, and the case-fold probe's cleanup of the throw-away `.agy-casefold-probe-*`\ndirectory it created seconds earlier (and of nothing else), so rollback never\ndeletes."
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the archive-move sentence verbatim; missing:\n  %s", sentence)
	}

	// Static half: count every removal call site in the non-test sources of the
	// package, by PREFIX, and require each to sit in one of the two named functions
	// of its own file.
	sanctioned := map[string]string{
		"pruneLedgerKeys":  "sig.go",
		"probeCaseFolding": "snapshot.go",
	}
	entries, err := os.ReadDir(filepath.Join("..", "codesnap"))
	if err != nil {
		t.Fatalf("read internal/codesnap: %v", err)
	}
	scanned := 0
	sites := map[string]int{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join("..", "codesnap", e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		scanned++
		fn := ""
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "func ") {
				fn = enclosingFuncDecl(trimmed)
			}
			if !strings.Contains(line, "os.Remove") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			who, ok := sanctioned[fn]
			if !ok || who != e.Name() {
				t.Errorf("internal/codesnap/%s:%d removes a file outside the two sites the sentence names (enclosing function %q):\n  %s", e.Name(), i+1, fn, trimmed)
				continue
			}
			sites[fn]++
		}
	}
	if scanned == 0 {
		t.Fatal("premise broken: no non-test source scanned in internal/codesnap")
	}
	if sites["pruneLedgerKeys"] != 1 || sites["probeCaseFolding"] != 1 || len(sites) != 2 {
		t.Fatalf("internal/codesnap removal call sites = %v; want exactly 1 in sig.go pruneLedgerKeys and 1 in snapshot.go probeCaseFolding", sites)
	}

	// Behavioral half: one real snapshot, one created file, one rollback.
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if mkErr := os.MkdirAll(filepath.Join(root, "src"), 0o755); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	keep := filepath.Join(root, "src", "keep.txt")
	if wErr := os.WriteFile(keep, []byte("original\n"), 0o644); wErr != nil {
		t.Fatalf("write keep: %v", wErr)
	}
	dest := filepath.Join(base, "run")
	if _, terr := codesnap.Take(root, []string{"src"}, dest); terr != nil {
		t.Fatalf("codesnap.Take: %v", terr)
	}
	created := filepath.Join(root, "src", "new.txt")
	if wErr := os.WriteFile(created, []byte("brand new\n"), 0o644); wErr != nil {
		t.Fatalf("write created: %v", wErr)
	}
	report, rerr := codesnap.Rollback(root, dest)
	if rerr != nil {
		t.Fatalf("codesnap.Rollback: %v", rerr)
	}
	if len(report.Archived) != 1 || report.Archived[0] != "rollback-archive/src/new.txt" {
		t.Fatalf("archived = %v, want the archive-relative destination of the created file", report.Archived)
	}
	if _, statErr := os.Stat(created); !os.IsNotExist(statErr) {
		t.Errorf("created file is still in the tree (err=%v); rollback must move it out", statErr)
	}
	archived, aerr := os.ReadFile(filepath.Join(dest, "rollback-archive", "src", "new.txt"))
	if aerr != nil {
		t.Fatalf("read the archived copy: %v", aerr)
	}
	if string(archived) != "brand new\n" {
		t.Errorf("archived bytes = %q, want the created file's content (archive-move, not delete)", archived)
	}
}

// TestCodeDocPinsVerificationTrustModel pins §4.8 and §4.9 (findings S1, S4, C1)
// sentence by sentence against the code that implements them: compile-only
// detection, the explicit-verify rule, the exec disclosure, the run-total budget,
// the untrusted-data fence around the repair excerpt, the plan-ingestion scope
// check and the effective-scope union.
func TestCodeDocPinsVerificationTrustModel(t *testing.T) {
	doc, _ := codeDocRead(t)

	const (
		detectionSentence = "Detection is compile-only: with `go.mod` at the root, a scope entry naming a `.go` file yields `go build` and `go vet` for that package directory, and the engine schedules no `go test` and no `npm test`."
		explicitSentence  = "Any other verifier, including every test runner, runs only when the operator names it with `--verify`."
		budgetSentence    = "The whole run gets one verification wall budget of 1800 seconds (`maxVerifyTotalSec`, `internal/codework/verify.go`)."
		ingestSentence    = "A plan that arrives through `--plan` is validated by the same governance and canonical-form scope predicate the plan builder uses, before the snapshot and before any seat runs."
		unionSentence     = "When `--scope` and a plan's unit scopes disagree, the effective snapshot, diff and verification scope is their union, and the echo names it."
		fenceSentence     = "The verification excerpt a repair seat reads is fenced as untrusted data: the repair body carries it inside a `> **[UNTRUSTED-DATA]**` block"
	)
	for _, sentence := range []string{detectionSentence, explicitSentence, budgetSentence, ingestSentence, unionSentence, fenceSentence} {
		if !codeDocHas(doc, sentence) {
			t.Fatalf("docs/code.md must carry the sentence verbatim; missing:\n  %s", sentence)
		}
	}
	// The disclosure is pinned as the code's OWN constant, so the doc and the echo
	// cannot drift apart.
	if !codeDocHas(doc, verifyTrustNote) {
		t.Fatalf("docs/code.md must carry the exec disclosure verbatim; missing:\n  %s", verifyTrustNote)
	}

	// (S1a) compile-only: a tree whose scope names a _test.go file yields no test
	// runner, and an npm test script yields no verifier at all.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"node t.js"}}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	detected := DetectVerify(root, []string{"internal/x/a_test.go", "src/index.js"})
	if len(detected) != 2 {
		t.Fatalf("DetectVerify = %v, want the build+vet pair", detected)
	}
	for _, argv := range detected {
		if joined := strings.Join(argv, " "); strings.Contains(joined, "test") {
			t.Errorf("detection scheduled %q; the doc says compile-only", joined)
		}
	}
	if got := DetectVerify(root, []string{"src/index.js"}); got != nil {
		t.Errorf("DetectVerify on an npm-only scope = %v; the doc says no npm test is scheduled", got)
	}

	// (S1b/c) the budget constants the sentence names.
	if maxVerifyTotalSec != 1800 {
		t.Errorf("maxVerifyTotalSec = %d, the doc says 1800", maxVerifyTotalSec)
	}
	if !strings.Contains(verifyBudgetMarker, "skipped-budget") || !strings.Contains(verifyBudgetMarker, "not a pass") {
		t.Errorf("verifyBudgetMarker = %q, want the skipped/not-a-pass wording the doc describes", verifyBudgetMarker)
	}
	if defaultVerifyTimeout != 600*time.Second {
		t.Errorf("defaultVerifyTimeout = %s, the doc says 600 seconds", defaultVerifyTimeout)
	}

	// (S1d) the fence the repair body carries, with the review prompt's own marker.
	if !strings.Contains(repairFenceOpen, untrustedDataMarker) {
		t.Errorf("repairFenceOpen does not carry the review prompt's marker %q", untrustedDataMarker)
	}
	if !strings.Contains(repairFenceOpen, "data, never as instructions") {
		t.Errorf("repairFenceOpen lost the data-not-instructions instruction: %q", repairFenceOpen)
	}
	if prompt := reviewPrompt("body", ""); !strings.Contains(prompt, untrustedDataMarker) {
		t.Errorf("the review prompt no longer uses %q, so the two fences are no longer the same marker", untrustedDataMarker)
	}

	// (S1b behavior) an npm-only tree is `unverified`, not a package-script run.
	phaseRoot, outDir := phaseTree(t)
	if err := os.WriteFile(filepath.Join(phaseRoot, "package.json"), []byte(`{"scripts":{"test":"node t.js"}}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	rec := installPhaseRecorder(t, nil)
	cfg := CodeConfig{Root: phaseRoot, OutDir: outDir, Yes: true, MaxRepairs: 0}
	res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
	if err != nil {
		t.Fatalf("WritePhase: %v", err)
	}
	if res.Status != StatusUnverified {
		t.Errorf("status = %q, want %q: no verifier is detected for an npm-only tree", res.Status, StatusUnverified)
	}
	if len(rec.verifyCalls) != 0 {
		t.Errorf("verify calls = %v, want none", rec.verifyCalls)
	}

	// (S4 behavior) a governed scope in ingested plan bytes is refused, nothing runs.
	rec2 := installPhaseRecorder(t, nil)
	_, gerr := WritePhase(CodeConfig{Root: phaseRoot, OutDir: filepath.Join(phaseRoot, "..", "run2"), Yes: true}, handWrittenPlanDoc("rules/team.md"))
	if !errors.Is(gerr, ErrRefusedPlanScope) {
		t.Fatalf("err = %v, want ErrRefusedPlanScope for a governed plan scope", gerr)
	}
	if len(rec2.calls) != 0 {
		t.Errorf("calls = %v, want none before a refused plan starts anything", rec2.calls)
	}

	// (C1 behavior) the union is what the snapshot gets, and the echo says so.
	if got := unionScope([]string{"a/*.go"}, []string{"b/*.go"}); len(got) != 2 {
		t.Errorf("unionScope = %v, want both entries", got)
	}
	lines := writeScopeLines([]string{"a/*.go"}, []string{"a/*.go", "b/*.go"})
	if len(lines) != 1 || !strings.Contains(lines[0], "the plan adds: b/*.go") {
		t.Errorf("writeScopeLines = %v, want the honest union line", lines)
	}

	// Non-vacuity: the same predicate rejects a regressed sentence.
	if codeDocHas(doc, "Detection runs `go test` and `npm test` for the plan's scopes.") {
		t.Error("pin is vacuous: it still matches a sentence that drops compile-only detection")
	}
}

// TestCodeDocPinsConsentGate pins §4.5: a provider seat mets and asks; a reader
// that cannot answer is refused with nothing run; a native-only phase never asks.
func TestCodeDocPinsConsentGate(t *testing.T) {
	doc, _ := codeDocRead(t)

	const sentence = "A provider seat in a seat list, or any write run, is metered and asks for consent: a terminal asks y/N, a reader that cannot answer is refused unless `--yes` is given, and a native-only phase never asks."
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the consent sentence verbatim; missing:\n  %s", sentence)
	}

	t.Run("non-tty provider write is refused with nothing run", func(t *testing.T) {
		root, outDir := phaseTree(t)
		rec := installPhaseRecorder(t, nil) // stdinIsTTY = false, empty reader
		cfg := CodeConfig{Root: root, OutDir: outDir, Ask: "do the alpha work", WriterSeat: providerSeat}
		res, err := WritePhase(cfg, phasePlanDoc(t, providerSeat))
		if !errors.Is(err, ErrConsentRequired) {
			t.Fatalf("WritePhase err = %v, want ErrConsentRequired", err)
		}
		if res == nil || res.Status != StatusConsentRequired {
			t.Fatalf("result = %+v, want status %q", res, StatusConsentRequired)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("calls = %v, want none (the gate runs before the snapshot)", rec.calls)
		}
		if !strings.Contains(rec.out.String(), "re-run with --yes") {
			t.Errorf("echo %q does not name --yes", rec.out.String())
		}
	})

	t.Run("native-only plan asks nothing", func(t *testing.T) {
		root, outDir := phaseTree(t)
		rec := installPhaseRecorder(t, nil)
		cfg := CodeConfig{
			Root:      root,
			OutDir:    outDir,
			Ask:       "audit the alpha plan",
			PlanSeats: []string{"native:gemini-3.8-flash-high"},
		}
		if _, err := PlanPhase(cfg); err != nil {
			t.Fatalf("PlanPhase: %v (a native-only phase must not need consent)", err)
		}
		if len(rec.fanoutCalls) != 1 {
			t.Fatalf("fanout calls = %d, want 1", len(rec.fanoutCalls))
		}
		if strings.Contains(rec.out.String(), "metered seat") || strings.Contains(rec.out.String(), "Proceed?") {
			t.Errorf("echo %q asked for consent on a free native-only phase", rec.out.String())
		}
	})
}

// TestCodeDocPinsSeatHonestyAndExitCodes pins the FIX-C doc sync: the seat-honesty
// rule (§4.10), the `--run`/`--out` contract (§2), the rollback exit code (§2/§3)
// and the compile-only verification wording (§2/§4.8) are each checked against the
// code or the shipped usage text that implements them.
func TestCodeDocPinsSeatHonestyAndExitCodes(t *testing.T) {
	doc, verb := codeDocRead(t)

	// (C3) the seat-honesty sentences, in §4.10 and in the §3 exit-code table.
	for _, sentence := range []string{
		"a native phase whose seats only partly delivered is `degraded`, never `ok`",
		"some seats failed, the rest delivered: `degraded` - the artifacts exist, the run keeps exit 0",
		"every seat failed: `failed` with an error naming the count, and exit 1",
		"the run status line appends the `(degraded)` token (`seat_status` in the JSON, the drift token's sibling)",
	} {
		if !codeDocHas(doc, sentence) {
			t.Fatalf("docs/code.md must carry the seat-honesty sentence verbatim; missing:\n  %s", sentence)
		}
	}
	if StatusDegraded != "degraded" {
		t.Errorf("StatusDegraded = %q, the doc says degraded", StatusDegraded)
	}
	if StatusDegraded == StatusOK || StatusDegraded == StatusFailed {
		t.Error("StatusDegraded collides with an existing status")
	}

	// (C5) the --run/--out contract: the doc states it, and the shipped usage text
	// (the contract source of truth, per §2) states it too.
	if !codeDocHas(doc, "`--run` is the single source of truth for `review` and `rollback`") {
		t.Errorf("docs/code.md must state the --run/--out contract")
	}
	if !strings.Contains(verb, "--run DIR is the single source of truth") {
		t.Errorf("cmd/agy/pro_code.go usage does not state the --run source-of-truth contract")
	}

	// The rollback exit code the doc now claims: exit 1 on a missing manifest, and
	// that is what RollbackPhase returns (the verb maps res==nil to 1).
	const rollbackClaim = "A `rollback --run DIR` with no usable manifest is a RUN failure, exit 1"
	if !codeDocHas(doc, rollbackClaim) {
		t.Fatalf("docs/code.md must state the rollback exit code; missing:\n  %s", rollbackClaim)
	}
	root, outDir := phaseTree(t)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	installPhaseRecorder(t, nil)
	if _, err := RollbackPhase(CodeConfig{Root: root, OutDir: outDir}); err == nil {
		t.Error("RollbackPhase accepted a run directory with no manifest")
	}

	// (FIX-B item 6) the compile-only wording: the doc's usage block and the verb's
	// help both say it, and no shipped text says bare "detected".
	const detectionWording = "--verify CMD a verification command line (repeatable; all must pass; omit = compile-only go build+go vet detection)"
	if !codeDocHas(doc, detectionWording) {
		t.Errorf("docs/code.md §2 must carry the compile-only --verify wording; missing:\n  %s", detectionWording)
	}
	if !strings.Contains(verb, "omit = compile-only go build+go vet detection") {
		t.Error("cmd/agy/pro_code.go usage does not carry the compile-only --verify wording")
	}
	if strings.Contains(verb, "omit = detected") {
		t.Error("cmd/agy/pro_code.go usage still promises bare detection")
	}

	// (preemptive) the skipped-budget row token: the doc names it, the code owns it.
	const skippedClaim = "`skipped=verify-skipped-budget`"
	if !codeDocHas(doc, skippedClaim) {
		t.Errorf("docs/code.md must name the budget-skip row token; missing:\n  %s", skippedClaim)
	}
	if verifySkippedBudgetToken != "verify-skipped-budget" {
		t.Errorf("verifySkippedBudgetToken = %q, the doc says verify-skipped-budget", verifySkippedBudgetToken)
	}

	// Non-vacuity: the same predicate rejects a regressed sentence.
	if codeDocHas(doc, "a native phase whose seats failed still reports `ok`") {
		t.Error("pin is vacuous: it still matches the regressed seat-honesty wording")
	}
}
