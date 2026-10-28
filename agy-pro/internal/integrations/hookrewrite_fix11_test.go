package integrations

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// hookDuplicateCommandJSON is the F-01 shape: VALID JSON, one object, the same
// "command" key twice, both values portable. A JSON decoder keeps the last value,
// so the document has one decodable command but two literals, and the rewrite's
// verifier declines instead of guessing which site is the real one. Nothing about
// the document is malformed enough to refuse the deploy: the source bytes ship.
const hookDuplicateCommandJSON = "{\n" +
	"  \"hooks\": {\n" +
	"    \"PostInvocation\": {\n" +
	"      \"enabled\": true,\n" +
	"      \"command\": \"agy-tokens\",\n" +
	"      \"command\": \"agy-tokens --hook\",\n" +
	"      \"args\": [\"--hook\"]\n" +
	"    }\n" +
	"  }\n" +
	"}\n"

// hookMultiCommandJSON names the launcher at two hook sites, one of them with its
// own arguments: both sites are the same command and both are pinned.
const hookMultiCommandJSON = "{\n" +
	"  \"hooks\": {\n" +
	"    \"PostInvocation\": {\"command\": \"agy-tokens\", \"args\": [\"--hook\"]},\n" +
	"    \"SessionStart\": {\"command\": \"agy-tokens --hook --raw\"}\n" +
	"  }\n" +
	"}\n"

// hookSourceFS wraps raw hook bytes as a deploy source.
func hookSourceFS(data []byte) fs.FS {
	return fstest.MapFS{"hooks/" + hookSourceFile: {Data: data}}
}

// hookOnlyStep reports whether a composed write carries a step for the hook.
func hookOnlyStep(write *Write) (Step, bool) {
	for _, step := range write.Steps() {
		if step.Feature == FeatureHook {
			return step, true
		}
	}
	return Step{}, false
}

// TestHookDuplicateCommandKeyDeclineIsNotDrift is F-01's regression pin: when the
// rewrite DECLINES a document, Status must not report a drift that tells the
// operator to re-run a verb which re-plans to zero writes.
//
// The decision has to agree with the deploy decision, so the check calls the same
// function the deploy path calls: a document the rewrite declines has the source
// bytes as its expected bytes, which is exactly what is on disk, so the
// destination is CLEAN and the Detail names the decline instead of promising a
// repair that cannot happen.
func TestHookDuplicateCommandKeyDeclineIsNotDrift(t *testing.T) {
	sandbox := newDeploySandbox(t)
	writeSandboxLauncher(t, sandbox, "windows")
	source := hookSourceFS([]byte(hookDuplicateCommandJSON))

	// The rewrite declines, and says so.
	data, posture := hookCommandRewriteFor([]byte(hookDuplicateCommandJSON))
	if posture.Posture != HookCommandUnchanged || posture.Rewritten() {
		t.Fatalf("posture = %+v, want a decline", posture)
	}
	if string(data) != hookDuplicateCommandJSON {
		t.Fatalf("a declined rewrite still changed the bytes:\n%q", string(data))
	}
	assertContains(t, "decline reason", posture.Detail, "did not verify")

	// The deploy ships the source bytes and records their digest.
	plan := sandbox.plan(FeatureHook, source)
	if plan.HookCommand == nil || plan.HookCommand.Posture != HookCommandUnchanged {
		t.Fatalf("plan posture = %+v, want the decline carried through", plan.HookCommand)
	}
	if plan.Actions[0].NewSHA != SHA256Hex([]byte(hookDuplicateCommandJSON)) {
		t.Fatalf("plan digest = %q, want the digest of the source bytes", plan.Actions[0].NewSHA)
	}
	sandbox.apply(plan)
	destination := sandbox.dest(FeatureHook, hookSourceFile)
	assertFileBytes(t, destination, []byte(hookDuplicateCommandJSON))

	// Status: clean with the decline as the reason. NOT drift.
	status := sandbox.status(FeatureHook)
	if status.Drifted != 0 || status.Clean != 1 || !status.OK() {
		t.Fatalf("status = %+v, want 1 clean and 0 drifted (a decline is not a repairable drift)", status.Destinations)
	}
	detail := status.Destinations[0].Detail
	assertContains(t, "decline detail", detail, "will not rewrite it", "did not verify")
	if strings.Contains(detail, "re-run apply to pin it") {
		t.Fatalf("the decline detail tells the operator to re-run a verb that writes nothing: %q", detail)
	}

	// Repair composes no hook step, and the state does not move: no loop.
	repair := ComposeRepair(source)
	if step, found := hookOnlyStep(repair); found {
		t.Fatalf("repair composed a hook step for a declined document: %+v", step.Lines)
	}
	after := sandbox.status(FeatureHook)
	if after.Clean != 1 || after.Drifted != 0 {
		t.Fatalf("a second status read = %+v, want the same clean state", after.Destinations)
	}

	// The pin for the sibling direction: THIS document with a launcher present and
	// the destination hand-edited to the pinned form would be drift, so the clean
	// verdict above is about the decline, not about the check being asleep.
	if _, stale := hookLiveCommandPosture(destination); stale {
		t.Fatal("the declined document reported posture drift")
	}
}

// TestHookMultiCommandDocumentPinsEverySite is K3 F-3's pin: a document with more
// than one portable command value pins every one of them, keeps each site's own
// arguments, and the posture record counts the sites instead of pretending the
// first one is all there was.
func TestHookMultiCommandDocumentPinsEverySite(t *testing.T) {
	_, launcher := hookLauncherSandbox(t, "windows")

	data, posture := hookCommandRewriteFor([]byte(hookMultiCommandJSON))
	if posture.Posture != HookCommandAbsolute {
		t.Fatalf("posture = %+v, want an absolute rewrite", posture)
	}
	if posture.Sites != 2 {
		t.Fatalf("posture.Sites = %d, want 2 (one per command value)", posture.Sites)
	}
	if posture.Value != quoteLauncher(launcher) {
		t.Fatalf("posture.Value = %q, want the first site's pinned value %q", posture.Value, quoteLauncher(launcher))
	}
	if posture.Portable != hookPortableCommand {
		t.Fatalf("posture.Portable = %q, want the first site's source value %q", posture.Portable, hookPortableCommand)
	}
	assertContains(t, "posture text", posture.PostureText(), "2 command values pinned")

	doc := hookDoc(t, data)
	if got := hookCommandAt(t, doc, "PostInvocation"); got != quoteLauncher(launcher) {
		t.Fatalf("PostInvocation command = %q, want the pinned launcher", got)
	}
	if got := hookCommandAt(t, doc, "SessionStart"); got != quoteLauncher(launcher)+" --hook --raw" {
		t.Fatalf("SessionStart command = %q, want the pinned launcher with that site's own arguments", got)
	}
	if got := doc["hooks"].(map[string]any)["PostInvocation"].(map[string]any)["args"]; !equalAny(got, []any{"--hook"}) {
		t.Fatalf("the first site's args changed: %v", got)
	}
	// Idempotent: a second pass finds nothing portable left.
	again, second := hookCommandRewriteFor(data)
	if second.Posture != HookCommandUnchanged || string(again) != string(data) {
		t.Fatalf("second pass = %+v and changed the bytes: %v", second, string(again) != string(data))
	}
}

// TestHookPinnedCommandStaysCleanWhenTheLauncherIsGone is the visibility half of
// the reverse direction (the final review's P2, resolved as visibility, not
// drift): a pinned command whose launcher is gone stays CLEAN, because repairing
// it would have to unpin it back to a PATH name and re-open F-V1. The engine says
// so in Detail instead, and repair composes nothing.
func TestHookPinnedCommandStaysCleanWhenTheLauncherIsGone(t *testing.T) {
	sandbox := newDeploySandbox(t)
	launcher := writeSandboxLauncher(t, sandbox, "windows")
	source := hookPortableFixtureContent()

	sandbox.apply(sandbox.plan(FeatureHook, source))
	destination := sandbox.dest(FeatureHook, hookSourceFile)
	rewritten, _ := hookCommandRewriteFor(hookPortableFixtureJSON)

	// Healthy pinned deploy: clean, and no detail to print.
	status := sandbox.status(FeatureHook)
	if status.Clean != 1 || status.Drifted != 0 {
		t.Fatalf("status after the pinned deploy = %+v, want clean", status.Destinations)
	}
	if detail := status.Destinations[0].Detail; detail != "" {
		t.Fatalf("a healthy pinned deploy carries detail %q, want none", detail)
	}

	// The launcher goes away (uninstall, home move, manual delete).
	if err := os.Remove(launcher); err != nil {
		t.Fatalf("remove the launcher: %v", err)
	}

	status = sandbox.status(FeatureHook)
	if status.Drifted != 0 || status.Clean != 1 || !status.OK() {
		t.Fatalf("status with the launcher gone = %+v, want clean (repair must never unpin)", status.Destinations)
	}
	detail := status.Destinations[0].Detail
	assertContains(t, "advisory detail", detail, "is not on disk", "will not unpin it")
	if strings.Contains(detail, "re-run apply to pin it") {
		t.Fatalf("the advisory detail claims the bare form is the repair: %q", detail)
	}

	// Repair composes no hook step, so nothing unpins the command.
	repair := ComposeRepair(source)
	if step, found := hookOnlyStep(repair); found {
		t.Fatalf("repair composed a hook step while the launcher was gone: %+v", step.Lines)
	}
	assertFileBytes(t, destination, rewritten)
	if after := sandbox.status(FeatureHook); after.Clean != 1 || after.Drifted != 0 {
		t.Fatalf("status after a repair attempt = %+v, want the same clean state", after.Destinations)
	}

	// A command that names some other tool is not a launcher this engine claims
	// anything about.
	if path := hookCommandLauncherPath("other-tool --flag"); path != "" {
		t.Fatalf("hookCommandLauncherPath(other tool) = %q, want no claim", path)
	}
	if path := hookCommandLauncherPath("agy-tokens"); path != "" {
		t.Fatalf("hookCommandLauncherPath(bare name) = %q, want no claim (not an absolute path)", path)
	}
	if path := hookCommandLauncherPath(`"` + launcher + `" --hook`); path != launcher {
		t.Fatalf("hookCommandLauncherPath(quoted launcher) = %q, want %q", path, launcher)
	}
}

// TestRepairDriftTagRequiresARealWrite is the repair-label pin (the final review's
// P3 race): the drifted/missing tag describes the state that triggered the repair,
// and it is only honest when the re-plan of that same state still writes. The
// classification and the plan are two machine-state reads, so a launcher that
// vanishes in between must not leave a no-op step labeled as reconciliation.
func TestRepairDriftTagRequiresARealWrite(t *testing.T) {
	sandbox := newDeploySandbox(t)
	writeSandboxLauncher(t, sandbox, "windows")
	source := hookPortableFixtureContent()

	writing := sandbox.plan(FeatureHook, source)
	if writing.Writes() != 1 {
		t.Fatalf("the fresh plan writes %d destination(s), want 1", writing.Writes())
	}
	status := &FeatureStatus{Feature: FeatureHook, Drifted: 1}
	if tag := repairDriftTag(writing, status); tag != " (1 drifted, 0 missing)" {
		t.Fatalf("tag for a writing plan = %q", tag)
	}

	// The race: the status said drifted, the re-plan now finds the bytes in place.
	sandbox.apply(writing)
	noop := sandbox.plan(FeatureHook, source)
	if noop.Writes() != 0 {
		t.Fatalf("the second plan writes %d destination(s), want 0", noop.Writes())
	}
	if tag := repairDriftTag(noop, status); tag != "" {
		t.Fatalf("tag for a no-op plan = %q, want no tag at all", tag)
	}
	if tag := repairDriftTag(nil, status); tag != "" {
		t.Fatalf("tag for a nil plan = %q, want no tag", tag)
	}
	if tag := repairDriftTag(writing, nil); tag != "" {
		t.Fatalf("tag for a nil status = %q, want no tag", tag)
	}

	// The composed repair of a genuinely drifted hook still carries the tag, so the
	// helper did not silence the normal path.
	edited := strings.Replace(string(readFileRaw(t, sandbox.dest(FeatureHook, hookSourceFile))), `"enabled": true`, `"enabled": false`, 1)
	writeFileRaw(t, sandbox.dest(FeatureHook, hookSourceFile), []byte(edited))
	repair := ComposeRepair(source)
	step, found := hookOnlyStep(repair)
	if !found {
		t.Fatal("repair composed no hook step for a genuinely drifted file")
	}
	assertContains(t, "repair headline", step.Lines[0], "(1 drifted", "0 missing)")
}

// TestHookRewriteWithoutAHomeRecordsTheNonTypedFailure covers the launcher
// resolver's OTHER failure arm (MiMo F-02): a home that cannot be resolved at all
// returns a plain error rather than a *StatusLineLauncherError, and the rewrite must
// still deploy the source bytes with that reason recorded instead of refusing the
// deploy or panicking on a nil shape.
func TestHookRewriteWithoutAHomeRecordsTheNonTypedFailure(t *testing.T) {
	// An unresolvable home: os.UserHomeDir reads USERPROFILE on Windows and HOME
	// elsewhere, so emptying both makes the ladder itself fail.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	pinRuntimeGOOS(t, hostGOOS())

	data, posture := hookCommandRewriteFor(hookPortableFixtureJSON)
	if string(data) != string(hookPortableFixtureJSON) {
		t.Fatalf("the rewrite changed the bytes without a resolvable home:\n%q", string(data))
	}
	if posture.Posture != HookCommandBare {
		t.Fatalf("posture = %+v, want the bare fallback", posture)
	}
	if posture.Detail == "" {
		t.Fatalf("the fallback record carries no reason: %+v", posture)
	}
	if posture.Shape != "" {
		t.Fatalf("a non-typed resolver failure claimed the documented launcher shape %q", posture.Shape)
	}
	assertContains(t, "posture text", posture.PostureText(), "keeps the bare PATH name")
}

// equalAny compares two decoded JSON values without importing reflect into the
// assertion (the fixture values are arrays of plain strings).
func equalAny(got, want any) bool {
	gotList, ok := got.([]any)
	if !ok || len(gotList) != len(want.([]any)) {
		return false
	}
	for index := range gotList {
		if gotList[index] != want.([]any)[index] {
			return false
		}
	}
	return true
}

// TestHookStatusDetailIsNotADriftCount keeps the counts honest: an advisory Detail
// (decline, gone launcher) must never inflate Clean/Drifted/Missing, so `OK()`
// still answers "is this feature as the engine wants it".
func TestHookStatusDetailIsNotADriftCount(t *testing.T) {
	sandbox := newDeploySandbox(t)
	launcher := writeSandboxLauncher(t, sandbox, "windows")
	source := hookSourceFS([]byte(hookDuplicateCommandJSON))

	sandbox.apply(sandbox.plan(FeatureHook, source))
	status := sandbox.status(FeatureHook)
	if status.Clean+status.Drifted+status.Missing != len(status.Destinations) {
		t.Fatalf("counts %d/%d/%d do not add up to %d destination(s)",
			status.Clean, status.Drifted, status.Missing, len(status.Destinations))
	}
	if !status.OK() {
		t.Fatalf("a detail-bearing clean deployment is not OK: %+v", status.Destinations)
	}

	// The same file with its launcher gone and the command still bare stays clean
	// too: the decline is the reason, not the launcher.
	if err := os.Remove(launcher); err != nil {
		t.Fatalf("remove the launcher: %v", err)
	}
	status = sandbox.status(FeatureHook)
	if status.Clean != 1 || status.Drifted != 0 {
		t.Fatalf("status without a launcher = %+v, want clean", status.Destinations)
	}
	if path := filepath.Clean(sandbox.dest(FeatureHook, hookSourceFile)); status.Destinations[0].Path != path {
		t.Fatalf("destination path = %q, want %q", status.Destinations[0].Path, path)
	}
}
