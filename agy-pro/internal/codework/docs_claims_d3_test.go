package codework

// Doc-claim pins for the FIX-D3 docs sync (fix-list-3 row R3-10): the §3/§4.2/
// §4.3/§4.4/§4.6 sentences that describe the report surface and the snapshot
// trust model are each checked against the code that implements them, in the same
// test, so a sentence that stops describing the door fails here instead of
// passing on stale prose.
//
// Kept in its own file (same package, same helpers as docs_claims_test.go) so the
// unit that owns this sync does not rewrite the file other units pin their own
// sentences in.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/agenttools"
	"agypro/internal/codesnap"
)

// codeSnapSource reads one non-test source file of internal/codesnap, so a
// sentence about the snapshot can be pinned to the file that implements it.
func codeSnapSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "codesnap", name))
	if err != nil {
		t.Fatalf("read internal/codesnap/%s: %v", name, err)
	}
	return string(data)
}

// codeworkSource reads one non-test source file of this package, for the doc
// sentences whose implementation is a sibling file the doc names.
func codeworkSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read internal/codework/%s: %v", name, err)
	}
	return string(data)
}

// TestCodeDocPinsWin32FoldAndStreamArms pins §4.2's canonical-form layer: the
// name fold the doc describes is the one the guard compares with, the stream arm
// exists, and the stale "component-exact, case-insensitive" wording is gone.
func TestCodeDocPinsWin32FoldAndStreamArms(t *testing.T) {
	doc, _ := codeDocRead(t)
	guard, err := os.ReadFile(filepath.Join("..", "agenttools", "guard.go"))
	if err != nil {
		t.Fatalf("read internal/agenttools/guard.go: %v", err)
	}
	src := string(guard)

	const foldSentence = "`win32NameFold`: an uppercase-then-lowercase approximation of the NTFS `$UpCase` table"
	if !codeDocHas(doc, foldSentence) {
		t.Fatalf("docs/code.md must carry the name-fold sentence; missing:\n  %s", foldSentence)
	}
	if !strings.Contains(src, "func win32NameFold(name string) string {") {
		t.Fatal("internal/agenttools/guard.go declares no win32NameFold; the doc claims one fold for name classes")
	}
	if !strings.Contains(src, "strings.ToLower(strings.ToUpper(name))") {
		t.Errorf("win32NameFold is no longer the uppercase-based fold the doc describes")
	}
	if !strings.Contains(src, "func Win32NameFold(name string) string {") {
		t.Errorf("the doc claims the fold is exported for the snapshot ledger")
	}

	const streamSentence = "on Windows a component that contains `:` is refused (`HasWin32StreamSyntax`)"
	if !codeDocHas(doc, streamSentence) {
		t.Fatalf("docs/code.md must carry the stream-syntax sentence; missing:\n  %s", streamSentence)
	}
	for _, want := range []string{"func HasWin32StreamSyntax(component string) bool {", "win32StreamSyntaxRule"} {
		if !strings.Contains(src, want) {
			t.Errorf("internal/agenttools/guard.go is missing %q, which the doc's stream claim rests on", want)
		}
	}
	// The behavior, not only the names: a colon component is refused on Windows,
	// and the refusal is what the shape arm reports first.
	if runtime.GOOS != "windows" {
		t.Skip("not a Windows host: the stream refusal is Windows-gated by design")
	}
	if comp, ok := agenttools.CanonicalPathComponentOK("sub/AGENTS.md:stream"); ok {
		t.Errorf("CanonicalPathComponentOK accepted a stream-syntax component (%q clear)", comp)
	}
	if _, ok := agenttools.CanonicalPathComponentOK("sub/ſtate.json"); !ok {
		t.Errorf("CanonicalPathComponentOK refused a homoglyph name, but the fold should CLEAR the shape and let the governance compare refuse it")
	}
	if _, hit := agenttools.GovernanceWriteRefusal("sub/ſtate.json"); !hit {
		t.Errorf("GovernanceWriteRefusal cleared sub/ſtate.json; the fold exists to catch it")
	}

	// Non-vacuity: the stale pre-fix wording must not come back.
	if codeDocHas(doc, "is component-exact, case-insensitive and matched at any depth") {
		t.Error("docs/code.md still carries the pre-fix 'case-insensitive' wording the fold replaced")
	}
}

// TestCodeDocPinsVerifyEnvAndFence pins §4.8's two claims: the go-toolchain
// verifier's hermetic environment (both provenances, FIX-E2 / finding N3) and the
// fence that cannot be closed from inside the excerpt (capped and neutralized,
// finding N5). Both halves are checked - the doc names them, and the behavior is
// reproduced here - so the sentence cannot outlive the code.
func TestCodeDocPinsVerifyEnvAndFence(t *testing.T) {
	doc, _ := codeDocRead(t)
	verifySrc := codeworkSource(t, "verify.go")

	const envSentence = "runs hermetic: `GOPROXY=off`, `GOTOOLCHAIN=local`, `CGO_ENABLED=0` (`detectedVerifyEnv`"
	if !codeDocHas(doc, envSentence) {
		t.Fatalf("docs/code.md must carry the hermetic go-toolchain sentence; missing:\n  %s", envSentence)
	}
	if !strings.Contains(verifySrc, "detectedVerifyEnv = []string{") {
		t.Fatal("internal/codework/verify.go declares no detectedVerifyEnv")
	}
	for _, want := range detectedVerifyEnv {
		if !strings.Contains(verifySrc, `"`+want+`"`) {
			t.Errorf("verify.go does not apply %q; the doc lists it", want)
		}
	}
	// The provenance split the doc used to state is GONE: after N3 the seam picks
	// the runner, never what the toolchain may reach. The doc must say so, and the
	// old sentence must not come back.
	const noPassThrough = "An operator-set `GOPROXY`/`GOTOOLCHAIN`/`CGO_ENABLED` is therefore NOT passed through"
	if !codeDocHas(doc, noPassThrough) {
		t.Errorf("docs/code.md must state that the operator's own toolchain names are not passed through; missing:\n  %s", noPassThrough)
	}
	for _, stale := range []string{
		"do not share an environment",
		"runs in the operator's own environment",
		"cgo code does not compile under the detected verifier",
	} {
		if codeDocHas(doc, stale) {
			t.Errorf("docs/code.md still carries the pre-N3 sentence %q", stale)
		}
	}
	if strings.Count(verifySrc, "detectedVerifyEnv") < 2 {
		t.Errorf("detectedVerifyEnv appears only in its declaration; no verify path applies it")
	}

	// FIX-F2 (finding P-5) honesty half: the trio is a TOOLCHAIN restriction, not a
	// network sandbox, and the doc must say that instead of calling the arm "offline".
	const sandboxSentence = "the verifier's own code is not network-sandboxed"
	if !codeDocHas(doc, sandboxSentence) {
		t.Errorf("docs/code.md must state that the hermetic trio does not sandbox the verifier's own code; missing:\n  %s", sandboxSentence)
	}
	if codeDocHas(doc, "with the offline sentence") {
		t.Error("docs/code.md still calls the hermetic arm's echo sentence 'offline' (FIX-F2 P-5 retired that claim)")
	}
	// And the echo itself carries the same clause, from the same predicate.
	if line := verifyEnvLine([][]string{{"go", "test", "./..."}}, true); !strings.Contains(line, sandboxSentence) {
		t.Errorf("the consent echo does not carry the not-network-sandboxed clause:\n%s", line)
	}

	// Behavior, both provenances: an explicit go argv gets the trio, a non-go
	// explicit argv gets nothing, and the detected seam is unchanged.
	if got := verifyChildEnv([]string{"go", "build", "./pkg"}, false); len(got) != len(detectedVerifyEnv) {
		t.Errorf("verifyChildEnv(explicit go) = %v, want the hermetic trio the doc names", got)
	}
	if got := verifyChildEnv([]string{"go", "vet", "./pkg"}, true); len(got) != len(detectedVerifyEnv) {
		t.Errorf("verifyChildEnv(detected go) = %v, want the hermetic trio", got)
	}
	if got := verifyChildEnv([]string{"python", "x.py"}, false); len(got) != 0 {
		t.Errorf("verifyChildEnv(explicit non-go) = %v, want nothing added (the doc's second arm)", got)
	}

	// The fence: the doc names the mechanism, and the mechanism holds.
	const fenceSentence = "The fence pair is widened to one backtick longer than the longest backtick run inside the excerpt (`repairFenceFor`), capped at 16 backticks (`repairFenceMaxTicks`)"
	if !codeDocHas(doc, fenceSentence) {
		t.Fatalf("docs/code.md must carry the capped fence-width sentence; missing:\n  %s", fenceSentence)
	}
	const markerSentence = "[N backticks elided]` (`neutralizeFenceRuns`)"
	if !codeDocHas(doc, markerSentence) {
		t.Errorf("docs/code.md must name the neutralization marker and its function; missing:\n  %s", markerSentence)
	}
	if repairFenceMaxTicks != 16 {
		t.Errorf("repairFenceMaxTicks = %d, the doc says 16", repairFenceMaxTicks)
	}
	plain := fencedVerifyTail("build failed: undefined: x\n")
	if !strings.Contains(plain, "```text\n") || strings.Contains(plain, "````") {
		t.Errorf("fencedVerifyTail of a run with no backticks does not keep the three-backtick pair:\n%s", plain)
	}
	for _, run := range []int{3, 4, 7} {
		body := "log start\n" + strings.Repeat("`", run) + "\nrun: true\n"
		fenced := fencedVerifyTail(body)
		want := strings.Repeat("`", run+1) + "text\n"
		if !strings.Contains(fenced, want) {
			t.Errorf("fencedVerifyTail of a %d-backtick run does not widen the fence to %d ticks:\n%s", run, run+1, fenced)
		}
	}
	// Behavior above the cap: the run is neutralized, the fence stays at the cap,
	// and the block still closes (the pre-N5 failure was an OPEN fence).
	oversized := "build failed\n" + strings.Repeat("`", 3000) + "\nrun: true\n"
	fencedOversized := fencedVerifyTail(oversized)
	if got := longestBacktickRun(strings.ReplaceAll(fencedOversized, "\n", "")); got > repairFenceMaxTicks {
		t.Errorf("fencedVerifyTail of a 3000-backtick run leaves a %d-tick run; the cap is %d", got, repairFenceMaxTicks)
	}
	if !strings.Contains(fencedOversized, "[3000 backticks elided]") {
		t.Errorf("fencedVerifyTail of a 3000-backtick run does not carry the documented marker:\n%s", fencedOversized)
	}
	if !strings.Contains(fencedOversized, "build failed") {
		t.Errorf("fencedVerifyTail lost the diagnostic text it exists to carry:\n%s", fencedOversized)
	}
}

// TestCodeDocPinsComposedRouteRefusalExitTwo pins §3's R3-9 sentence: one
// pre-consent gate backs every route, and the verb really calls it on both the
// `write` and the `run` path before anything is authorized.
func TestCodeDocPinsComposedRouteRefusalExitTwo(t *testing.T) {
	doc, verb := codeDocRead(t)
	phases := codeworkSource(t, "phases.go")

	const sentence = "One function backs every route (`codework.RefuseScopeEntries`)"
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the one-gate sentence; missing:\n  %s", sentence)
	}
	if !codeDocHas(doc, "before the plan is built and before the consent prompt") {
		t.Errorf("docs/code.md must state that the gate runs pre-consent")
	}
	if !strings.Contains(phases, "func RefuseScopeEntries(") {
		t.Fatal("codework has no exported RefuseScopeEntries; the doc claims one gate for every route")
	}
	call := "codework.RefuseScopeEntries("
	if got := strings.Count(verb, call); got != 2 {
		t.Fatalf("cmd/agy/pro_code.go calls %s %d time(s), want 2 (the write and the run route)", call, got)
	}
	for i, idx := 0, strings.Index(verb, call); i < 2; i++ {
		if idx < 0 {
			t.Fatalf("call %d not found", i)
		}
		tail := verb[idx:]
		if end := strings.Index(tail, "}\n"); end > 0 {
			tail = tail[:end]
		}
		if !strings.Contains(tail, "return 2") {
			t.Errorf("a RefuseScopeEntries call does not return the usage exit code:\n%s", tail)
		}
		next := strings.Index(verb[idx+1:], call)
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	if codeDocHas(doc, "a refused scope is a run failure (exit 1)") {
		t.Error("pin is vacuous: it matches a sentence the door never claims")
	}
}

// TestCodeDocPinsWithheldReportLine pins the S5 report surface in both
// directions: the doc names the exact line the block prints, and the block prints
// it from the manifest's own skipped rows (codesnap.ReasonSecretClass), not from a
// count the caller invents.
func TestCodeDocPinsWithheldReportLine(t *testing.T) {
	doc, _ := codeDocRead(t)

	const docLine = "withheld: N secret-class file(s) not snapshotted"
	if !codeDocHas(doc, docLine) {
		t.Fatalf("docs/code.md must carry the withholding line verbatim; missing:\n  %s", docLine)
	}
	if !codeDocHas(doc, "plus the `withheld` rows in the JSON") {
		t.Errorf("docs/code.md must name the JSON `withheld` rows")
	}
	if codesnap.ReasonSecretClass != "secret-class" {
		t.Errorf("codesnap.ReasonSecretClass = %q, the doc says secret-class", codesnap.ReasonSecretClass)
	}

	// The code half: a real assembly over a manifest carrying three withheld rows.
	outDir := t.TempDir()
	write := &WritePhaseResult{
		Status:     StatusOK,
		FanoutRuns: []string{filepath.Join(outDir, PhaseWrite)},
		Diff:       []codesnap.FileDelta{},
		Manifest:   withheldManifest("config/app.pem", "keys/server.key", "src/dev.env"),
	}
	report := AssembleReport(CodeConfig{OutDir: outDir}, nil, write)
	want := strings.Replace(docLine, "N", "3", 1)
	if rendered := report.Render(); !strings.Contains(rendered, want) {
		t.Fatalf("render =\n%s\nwant the documented line %q", rendered, want)
	}

	// The JSON key the doc names is the one the document actually carries.
	data, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["withheld"]; !ok {
		t.Errorf("the JSON document carries no `withheld` key:\n%s", data)
	}

	// Non-vacuity: the same predicate rejects a line with no count placeholder or
	// a different unit of measure.
	if codeDocHas(doc, "withheld: secret-class file(s) not snapshotted") && !codeDocHas(doc, docLine) {
		t.Error("pin is vacuous: it still matches a withholding line with no count")
	}
}

// TestCodeDocPinsCostFieldNames pins the §4.6 cost sentence to the report's own
// JSON tags: the doc must name `native_seat_occurrences`,
// `provider_seat_occurrences` and `unique_seat_models` (the final names), and no
// ambiguous alias may survive in either place.
func TestCodeDocPinsCostFieldNames(t *testing.T) {
	doc, _ := codeDocRead(t)

	typ := reflect.TypeOf(CostSummary{})
	keys := make(map[string]string, typ.NumField()) // field name -> json key
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		keys[f.Name] = name
	}
	want := map[string]string{
		"NativeSeatOccurrences":   "native_seat_occurrences",
		"ProviderSeatOccurrences": "provider_seat_occurrences",
		"UniqueModels":            "unique_seat_models",
	}
	for field, key := range want {
		if keys[field] != key {
			t.Errorf("CostSummary.%s json tag = %q, want %q", field, keys[field], key)
		}
		if !codeDocHas(doc, "`"+key+"`") {
			t.Errorf("docs/code.md must name the shipped cost field %q", key)
		}
	}
	for _, stale := range []string{"`native_seats`", "`provider_seats`", "`seat_count`"} {
		if codeDocHas(doc, stale) {
			t.Errorf("docs/code.md still names the retired cost field %s", stale)
		}
	}
	if !codeDocHas(doc, "the human line says `seat occurrence(s)`") {
		t.Errorf("docs/code.md must explain the seat-occurrence wording")
	}
	// The human line the sentence describes really says it.
	if got := (&CodeRunReport{Seats: []SeatReport{{Kind: "native"}}}).seatHeaderLine(); !strings.Contains(got, "seat occurrence(s)") {
		t.Errorf("seatHeaderLine = %q, want the seat-occurrence wording the doc quotes", got)
	}
}

// TestCodeDocPinsSnapshotTrustModel pins the §4.4 claims that the snapshot
// refuses or withholds instead of copying: the secret class list, the
// governance/VCS refusal at take, the run-directory exclusion and the manifest
// row policy are each checked against internal/codesnap's source, because that is
// the package that decides them.
func TestCodeDocPinsSnapshotTrustModel(t *testing.T) {
	doc, _ := codeDocRead(t)
	snapshot := codeSnapSource(t, "snapshot.go")

	// (1) the secret class list, in the doc and in the source.
	const classList = "prefixes `.env*`, `id_rsa*`; suffixes `*.env`, `*.pem`, `*.key`, `*.pfx`, `*.p12`, `*.kdbx`, `*.ppk`"
	if !codeDocHas(doc, classList) {
		t.Fatalf("docs/code.md must carry the secret-class list; missing:\n  %s", classList)
	}
	for _, name := range []string{".env", "id_rsa", ".pem", ".key", ".pfx", ".p12", ".kdbx", ".ppk"} {
		if !strings.Contains(snapshot, `"`+name) && !strings.Contains(snapshot, `"*`+name) {
			t.Errorf("internal/codesnap/snapshot.go carries no %q entry for the secret class the doc lists", name)
		}
	}
	if !codeDocHas(doc, "inside the walk, before the claim is taken") {
		t.Errorf("docs/code.md must say the withholding decision happens before the claim is taken")
	}
	if !codeDocHas(doc, "`Diff` never reports a withheld path as `created`") {
		t.Errorf("docs/code.md must state that Diff does not report a withheld path as created")
	}
	if codeDocHas(doc, "`skipped` rows are never produced by Take") {
		t.Error("pin is vacuous: it matches a sentence the door never claims")
	}

	// (2) governance/VCS claims fail the take (A2's contract), and the stale
	// pre-fix sentence about .git is gone.
	for _, sentence := range []string{
		"A governance, credential or VCS claim fails the whole take.",
		"VCS metadata is refused at take like any other protected name",
	} {
		if !codeDocHas(doc, sentence) {
			t.Errorf("docs/code.md must carry the VCS claim; missing:\n  %s", sentence)
		}
	}
	if !strings.Contains(snapshot, "agenttools.VCSMetadataRefusal(") {
		t.Error("internal/codesnap/snapshot.go never calls VCSMetadataRefusal; the doc claims VCS claims are refused")
	}
	if codeDocHas(doc, "`.git` bytes are ordinary files that no scope claims unless a scope names them") {
		t.Error("docs/code.md still carries the pre-fix .git sentence, which A2 replaced")
	}

	// (3) the run directory is not claimable.
	if !codeDocHas(doc, "The run directory is not claimable.") {
		t.Errorf("docs/code.md must state the ledger exclusion")
	}
	if !strings.Contains(snapshot, "ErrLedgerRefused") {
		t.Error("internal/codesnap/snapshot.go does not raise ErrLedgerRefused; the doc claims the exclusion")
	}
	// The claim test the doc now states: a UNION of the lexical match and the
	// folded-LITERAL match, with character classes copied verbatim (finding N2).
	const unionSentence = "The claim test is a UNION and both arms refuse"
	if !codeDocHas(doc, unionSentence) {
		t.Errorf("docs/code.md must state the union the ledger claim applies; missing:\n  %s", unionSentence)
	}
	for _, sentence := range []string{
		"A character CLASS is never rewritten",
		"strictly more refusing than either rule this used to have",
		"`TestLedgerClaimIsMoreRefusingThanBothPriorVariants`",
	} {
		if !codeDocHas(doc, sentence) {
			t.Errorf("docs/code.md must carry the ledger-claim rule %q", sentence)
		}
	}
	if !strings.Contains(snapshot, "func FoldLiteralRuns(") {
		t.Error("internal/codesnap/snapshot.go has no FoldLiteralRuns; the doc claims the folded-literal arm")
	}
	if codeDocHas(doc, "The test is lexical and conservative by design") {
		t.Error("docs/code.md still calls the ledger claim lexical only; the union replaced that rule (N2)")
	}

	// (4) the manifest row policy at load.
	if !codeDocHas(doc, "A manifest row is re-checked at load.") {
		t.Errorf("docs/code.md must state the manifest row policy")
	}
	if !strings.Contains(snapshot, "checkManifestPathPolicy") {
		t.Error("internal/codesnap/snapshot.go has no checkManifestPathPolicy; the doc claims a load-time policy")
	}

	// (5) FIX-E1's two identity rules, each pinned to the shipped form.
	rollback := codeSnapSource(t, "rollback.go")
	for _, pair := range []struct{ doc, code string }{
		{"FILE IDENTITY is the exact root-relative path", "recorded[row.Path] = true"},
		{"DIRECTORY PREFIXES fold ASCII case only", "func (p namePolicy) dirKey("},
		{"The fold decision is a runtime probe of the root, not `GOOS`", "func probeCaseFolding("},
		{"`refuseResolvedChainRows` runs `agenttools.ResolvedDirChainRefusal` over every `files` row", "func refuseResolvedChainRows("},
		{"A file the manifest already describes is not archived.", "func sameAsRecorded("},
	} {
		if !codeDocHas(doc, pair.doc) {
			t.Errorf("docs/code.md must carry the identity/chain sentence %q", pair.doc)
		}
		if !strings.Contains(snapshot, pair.code) && !strings.Contains(rollback, pair.code) {
			t.Errorf("neither snapshot.go nor rollback.go carries %q, which the doc sentence %q rests on", pair.code, pair.doc)
		}
	}
	// The behavior behind "identity is exact" already has its own package pins;
	// this arm only guards the doc's own stale claim.
	if codeDocHas(doc, "so the write guard and the snapshot ledger cannot disagree about which spellings are one name") {
		t.Error("docs/code.md still claims the fold decides file identity; only NAME CLASSES fold (N1)")
	}
}

// TestCodeDocPinsRollbackHintQuoting pins the §3 hint sentence to the renderer:
// the command names the run directory as typed, quoted when it carries
// whitespace, and no other spelling.
func TestCodeDocPinsRollbackHintQuoting(t *testing.T) {
	doc, _ := codeDocRead(t)

	const sentence = "quotes it when the path contains whitespace, so the command it prints still splits into one operand in the shell the run was started in"
	if !codeDocHas(doc, sentence) {
		t.Fatalf("docs/code.md must carry the quoting sentence; missing:\n  %s", sentence)
	}
	if got := quoteRunDir(filepath.Join("reports", "code", "my run")); !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("quoteRunDir(spaced path) = %q, want a quoted operand", got)
	}
	plain := filepath.Join("reports", "code", "20261024-051500")
	if got := quoteRunDir(plain); got != plain {
		t.Errorf("quoteRunDir(%q) = %q, want the path unchanged when it has no whitespace", plain, got)
	}
	if codeDocHas(doc, "never quotes the run directory") {
		t.Error("pin is vacuous: it matches a sentence the door never claims")
	}
}

// sameFilePair reports whether two spellings written into dir name ONE file, and
// removes what it created. It is the measurement the doc's §4.2 fold sentence is
// about: `A.txt`/`a.txt` are one file on a case-folding volume, the folded
// look-alikes are not.
func sameFilePair(t *testing.T, dir, a, b string) bool {
	t.Helper()
	pa := filepath.Join(dir, a)
	pb := filepath.Join(dir, b)
	if err := os.WriteFile(pa, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %q: %v", a, err)
	}
	defer os.Remove(pa)
	defer os.Remove(pb)
	ia, err := os.Stat(pa)
	if err != nil {
		t.Fatalf("stat %q: %v", a, err)
	}
	ib, err := os.Stat(pb)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

// TestCodeDocPinsFoldOverRefusalTruth pins the N8(a) correction: the doc must
// state the fold's real failure direction (it refuses a look-alike that is a
// DISTINCT file, because `$UpCase` is per-volume) instead of the measured-false
// claim that `ſtate.json` opens `state.json`. The measurement is repeated here,
// so the sentence cannot drift into either error: claiming a collision the volume
// does not have, or claiming the fold only merges files that are the same file.
func TestCodeDocPinsFoldOverRefusalTruth(t *testing.T) {
	doc, _ := codeDocRead(t)

	for _, stale := range []string{
		"because the volume maps the unicode case-fold homoglyphs NTFS",
		"`ſtate.json` opens `state.json`",
	} {
		if codeDocHas(doc, stale) {
			t.Errorf("docs/code.md still carries the pre-N8(a) claim %q; the measurement says the look-alikes are distinct files", stale)
		}
	}
	for _, sentence := range []string{
		"a look-alike that is a distinct file is refused as if it were the governed name",
		"`$UpCase` is a per-volume table",
		"file identity is the exact path",
	} {
		if !codeDocHas(doc, sentence) {
			t.Errorf("docs/code.md must state the fold's real direction; missing:\n  %s", sentence)
		}
	}

	// The predicate half: the fold merges the long-s spelling and the governance
	// class refuses it, which is the over-refusal the sentence describes.
	if got := agenttools.Win32NameFold("\u017ftate.json"); got != "state.json" {
		t.Fatalf("Win32NameFold(ſtate.json) = %q, want the folded key the doc describes", got)
	}
	if _, hit := agenttools.GovernanceWriteRefusal("sub/\u017ftate.json"); !hit {
		t.Fatal("GovernanceWriteRefusal cleared the long-s spelling; the doc says the fold refuses it")
	}

	// The measurement half: ASCII case collides, the folded look-alikes do not.
	// A host whose volume collides on a look-alike fails here, because the doc
	// scopes its claim to the volumes this tree was measured on.
	dir := t.TempDir()
	if !sameFilePair(t, dir, "A.txt", "a.txt") {
		t.Skip("this volume does not fold ASCII case; the doc's non-collision claim is about case-folding volumes")
	}
	for _, pair := range [][2]string{
		{"\u017f.txt", "s.txt"},
		{"\u0131.txt", "i.txt"},
		{"\u212a.txt", "k.txt"},
	} {
		if sameFilePair(t, dir, pair[0], pair[1]) {
			t.Fatalf("this volume maps %q onto %q; docs/code.md claims they are distinct files on the volumes it was measured on, so the sentence needs a volume qualifier for this host", pair[0], pair[1])
		}
		if agenttools.Win32NameFold(pair[0]) != agenttools.Win32NameFold(pair[1]) {
			t.Fatalf("Win32NameFold does not merge %q with %q; the doc counts it as an over-fold", pair[0], pair[1])
		}
	}
}

// TestCodeDocPinsEightThreeOverRefusalBand pins the N9 owner decision word for
// word: the doc states the ONE TO FIVE digit band and the >=6-digit writable
// side, and the plan-time arm really implements exactly that band. The half-true
// "date-like" phrasing the fix-list retired must not come back.
func TestCodeDocPinsEightThreeOverRefusalBand(t *testing.T) {
	doc, _ := codeDocRead(t)

	if !codeDocHas(doc, "ONE TO FIVE digits") {
		t.Error("docs/code.md must state the exact 8.3 digit band (one to five digits)")
	}
	if !codeDocHas(doc, "`report~20261024.md` stays writable") {
		t.Error("docs/code.md must state the writable side of the band (six or more digits)")
	}
	if codeDocHas(doc, "date-like names stay writable") {
		t.Error("docs/code.md still carries the half-true 'date-like' phrasing the owner decision replaced")
	}
	if codeDocHas(doc, "and a `~N` component are") {
		t.Error("docs/code.md still refuses `~N` generically; the band is one to five digits")
	}

	if !runtimeNameRules {
		t.Skip("not a Windows host: the 8.3 shape arm is host-gated by design")
	}
	for _, tc := range []struct {
		comp    string
		refused bool
		why     string
	}{
		{"notes~2026.md", true, "four digits: the band the owner accepted as an over-refusal"},
		{"plan~12.txt", true, "two digits"},
		{"report~1", true, "one digit at the end of the component"},
		{"report~20261024.md", false, "eight digits: past the band, so the doc says it stays writable"},
		{"~1.md", false, "the ~ is the first character, not a tail"},
		{"notes.md", false, "no tail at all"},
	} {
		if got := scopeComponentShape(tc.comp) != ""; got != tc.refused {
			t.Errorf("scopeComponentShape(%q) refused = %v, want %v (%s)", tc.comp, got, tc.refused, tc.why)
		}
		if got := agenttools.Win32ShortAliasShape(tc.comp); got != tc.refused {
			t.Errorf("agenttools.Win32ShortAliasShape(%q) = %v, want %v (the guard and the plan mirror must agree)", tc.comp, got, tc.refused)
		}
	}
}

// TestCodeDocPinsCaseVariantLedgerScopeExitsTwo pins the exit-code table's N4/E2
// row: a case-variant scope aimed at the run directory is refused BEFORE the
// consent prompt (exit 2), which the pre-E2 mirror could not do (it accepted the
// spelling and the run failed as exit 1 after consent).
func TestCodeDocPinsCaseVariantLedgerScopeExitsTwo(t *testing.T) {
	doc, _ := codeDocRead(t)

	const tableRow = "in any case spelling the host's filesystem folds"
	if !codeDocHas(doc, tableRow) {
		t.Errorf("docs/code.md's exit-code table must state the case-variant ledger scope; missing:\n  %s", tableRow)
	}
	const mirrorSentence = "A case-variant scope aimed at the run directory is therefore refused here, as exit 2 before the echo"
	if !codeDocHas(doc, mirrorSentence) {
		t.Errorf("docs/code.md must state the pre-consent case-variant refusal; missing:\n  %s", mirrorSentence)
	}

	root := t.TempDir()
	if !hostFoldsCase(t, root) {
		t.Skip("this host does not fold case; the case-variant arm is inert here")
	}
	out := filepath.Join(root, "reports", "code", "run-x")
	err := RefuseScopeEntries(root, out, []string{"RePoRtS/**"}, "--scope")
	if !errors.Is(err, ErrRefusedPlanScope) {
		t.Fatalf("RefuseScopeEntries(RePoRtS/**) err = %v, want ErrRefusedPlanScope (exit 2, pre-consent)", err)
	}
	// Non-vacuity: this very gate must still accept a scope that cannot reach the
	// run directory, so the row above is not "refuse everything".
	if err := RefuseScopeEntries(root, out, []string{"src/*.go"}, "--scope"); err != nil {
		t.Fatalf("RefuseScopeEntries(src/*.go) err = %v, want nil (the gate must not refuse a clean scope)", err)
	}
}
