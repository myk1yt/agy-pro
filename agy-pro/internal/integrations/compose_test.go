package integrations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// intentOf renders a static intent map as an IntentReader, so a rule can be
// pinned without a state.json.
func intentOf(rows map[Feature]Intent) IntentReader {
	return func(feature Feature) Intent { return rows[feature] }
}

// contentFeatureList is every feature the content engine deploys, in canonical
// order (the settings-door feature has its own step kinds and no layout).
func contentFeatureList() []Feature {
	var features []Feature
	for _, feature := range AllFeatures() {
		if feature != FeatureStatusline {
			features = append(features, feature)
		}
	}
	return features
}

// TestComposedPresetDerivesThePostureRow pins the one preset rule both doors now
// consume: all on is tools, all off is native, anything else is custom only when
// something ends up explicitly on, and no row at all otherwise.
func TestComposedPresetDerivesThePostureRow(t *testing.T) {
	build := func(want Intent) map[Feature]Intent {
		after := map[Feature]Intent{}
		for _, feature := range AllFeatures() {
			after[feature] = want
		}
		return after
	}
	if got := ComposedPreset(build(IntentOn)); got != PresetTools {
		t.Fatalf("all on = %q, want tools", got)
	}
	if got := ComposedPreset(build(IntentOff)); got != PresetNative {
		t.Fatalf("all off = %q, want native", got)
	}
	mixed := build(IntentUnset)
	mixed[FeatureRules] = IntentOn
	if got := ComposedPreset(mixed); got != PresetCustom {
		t.Fatalf("one on and nothing else = %q, want custom", got)
	}
	mixed[FeatureRules] = IntentOff
	if got := ComposedPreset(mixed); got != "" {
		t.Fatalf("mixed with nothing on = %q, want no row at all", got)
	}
	if got := ComposedPreset(map[Feature]Intent{}); got != "" {
		t.Fatalf("empty map = %q, want no row at all", got)
	}
}

// TestResultIntentLayersTargetsOverTheRecordedPosture pins the map the preset rule
// reads: the recorded posture everywhere, the request's value on its targets.
func TestResultIntentLayersTargetsOverTheRecordedPosture(t *testing.T) {
	current := intentOf(map[Feature]Intent{FeatureRules: IntentOn, FeatureHook: IntentOff})
	after := ResultIntent(current, []Feature{FeatureSkills}, IntentOn)
	if after[FeatureRules] != IntentOn || after[FeatureSkills] != IntentOn {
		t.Fatalf("rules/skills = %v/%v, want on/on", after[FeatureRules], after[FeatureSkills])
	}
	if after[FeatureHook] != IntentOff {
		t.Fatalf("hook = %v, want off (it is not a target)", after[FeatureHook])
	}
	if after[FeaturePlugins] != IntentUnset {
		t.Fatalf("plugins = %v, want unset (nobody stated it)", after[FeaturePlugins])
	}
}

// TestPendingSetReadsOnlyExplicitOn pins the shared half of the operand-less
// apply: an explicit on is pending, an explicit off is not, and the legacy
// no-intent posture is not either.
func TestPendingSetReadsOnlyExplicitOn(t *testing.T) {
	reader := intentOf(map[Feature]Intent{FeatureRules: IntentOn, FeatureHook: IntentOff})
	pending := PendingFeatures(reader)
	if len(pending) != 1 || pending[0] != FeatureRules {
		t.Fatalf("pending = %v, want [rules]", pending)
	}
	if !AnythingRecorded(reader) {
		t.Fatal("AnythingRecorded = false, want true (an off is recorded intent)")
	}
	if AnythingRecorded(intentOf(nil)) {
		t.Fatal("AnythingRecorded = true on a machine that records nothing")
	}
	if got := PendingFeatures(nil); got != nil {
		t.Fatalf("PendingFeatures(nil) = %v, want nil", got)
	}
}

// TestDeployLinesCollapseIdenticalDestinations pins the renderer both doors used
// to keep their own copy of: the count line is exact, identical destinations are
// summarized instead of listed, and an all-identical plan says so.
func TestDeployLinesCollapseIdenticalDestinations(t *testing.T) {
	sandbox := newDeploySandbox(t)
	plan := sandbox.plan(FeatureRules, fixtureContent())
	first := DeployLines(FeatureRules, plan)
	if len(first) != 2 || !strings.Contains(first[0], "1 create, 0 update, 0 identical") {
		t.Fatalf("fresh plan lines = %q", first)
	}
	if !strings.Contains(first[1], "create") {
		t.Fatalf("the destination line does not name the action: %q", first[1])
	}
	sandbox.apply(plan)
	second := DeployLines(FeatureRules, sandbox.plan(FeatureRules, fixtureContent()))
	if !strings.Contains(second[0], "0 create, 0 update, 1 identical") {
		t.Fatalf("re-plan count line = %q", second[0])
	}
	if !strings.Contains(strings.Join(second, "\n"), "nothing to write") {
		t.Fatalf("an all-identical plan does not say so: %q", second)
	}
}

// TestRetractLinesNameForeignFiles pins the retract renderer: recorded
// destinations are the moves, and a file this engine does not claim is named as
// left alone instead of being listed as a write.
func TestRetractLinesNameForeignFiles(t *testing.T) {
	sandbox := newDeploySandbox(t)
	sandbox.apply(sandbox.plan(FeatureRules, fixtureContent()))
	writeFileRaw(t, sandbox.dest(FeatureRules, "NOTES.md"), []byte("mine\n"))
	lines := RetractLines(FeatureRules, sandbox.status(FeatureRules))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "1 destination(s) move to a timestamped backup") {
		t.Fatalf("head line = %q", lines[0])
	}
	if !strings.Contains(joined, "is not ours; left alone") {
		t.Fatalf("the foreign file is not named: %q", joined)
	}
}

// TestRetractLinesNoteTheTruncation pins the F-S9A-5 renderer fix: past
// DiffLineLimit the retract rows carry the same "... and more (the count above is
// exact)" note DeployLines and RollbackLines emit, so a bounded list never looks
// like the whole list. The head count stays exact, and foreign rows are not moves,
// so they neither count toward the cap nor disappear with it.
func TestRetractLinesNoteTheTruncation(t *testing.T) {
	status := &FeatureStatus{Feature: FeatureSkills}
	for i := 0; i < DiffLineLimit+3; i++ {
		status.Destinations = append(status.Destinations, DestinationStatus{
			Path:  filepath.Join("skills", fmt.Sprintf("s%02d.md", i)),
			State: StateClean,
		})
	}
	status.Destinations = append(status.Destinations, DestinationStatus{
		Path:  filepath.Join("skills", "FOREIGN.md"),
		State: StateForeign,
	})

	lines := RetractLines(FeatureSkills, status)
	joined := strings.Join(lines, "\n")
	if want := fmt.Sprintf("%d destination(s) move to a timestamped backup", DiffLineLimit+3); !strings.Contains(joined, want) {
		t.Fatalf("the head count is not exact (%q missing): %q", want, joined)
	}
	if note := "  ... and more (the count above is exact)"; strings.Count(joined, note) != 1 {
		t.Fatalf("want exactly one truncation note, got %d:\n%s", strings.Count(joined, note), joined)
	}
	moves := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "retract") {
			moves++
		}
	}
	if moves != DiffLineLimit {
		t.Fatalf("rendered %d move rows, want the %d-line cap:\n%s", moves, DiffLineLimit, joined)
	}
	if !strings.Contains(joined, "FOREIGN.md is not ours; left alone") {
		t.Fatalf("the foreign row was dropped by the cap: %q", joined)
	}

	// Below the cap there is nothing to announce.
	short := &FeatureStatus{Feature: FeatureRules, Destinations: status.Destinations[:2]}
	if got := strings.Join(RetractLines(FeatureRules, short), "\n"); strings.Contains(got, "... and more") {
		t.Fatalf("a two-row retraction claimed a truncation: %q", got)
	}
}

// TestFeatureWriteComposesIntentFacts pins the facts the doors turn into rows: one
// per-feature posture in request order plus the derived preset, and none at all
// for a request that records no intent.
func TestFeatureWriteComposesIntentFacts(t *testing.T) {
	_ = newDeploySandbox(t)
	write, intent, err := ComposeFeatureWrite(FeatureRequest{
		Verb:         "apply",
		Features:     AllFeatures(),
		Enable:       true,
		Content:      fixtureContent(),
		Intent:       intentOf(nil),
		RecordIntent: true,
		DoorExplicit: false, // the implicit case: no launcher, so the door degrades
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite: %v", err)
	}
	if len(intent.Features) != len(AllFeatures()) {
		t.Fatalf("intent rows = %d, want %d", len(intent.Features), len(AllFeatures()))
	}
	for i, row := range intent.Features {
		if row.Feature != AllFeatures()[i] || row.After != IntentOn {
			t.Fatalf("row %d = %+v, want %s on", i, row, AllFeatures()[i])
		}
	}
	if intent.Preset != PresetTools {
		t.Fatalf("preset = %q, want tools", intent.Preset)
	}
	if len(write.Notes()) == 0 {
		t.Fatal("the implicit statusline half did not degrade to a note")
	}
	if len(write.Steps()) != len(contentFeatureList()) {
		t.Fatalf("steps = %d, want %d", len(write.Steps()), len(contentFeatureList()))
	}

	_, quiet, err := ComposeFeatureWrite(FeatureRequest{
		Verb:     "apply",
		Features: []Feature{FeatureRules},
		Enable:   true,
		Content:  fixtureContent(),
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite (pending): %v", err)
	}
	if len(quiet.Features) != 0 || quiet.Preset != "" {
		t.Fatalf("a request that records no intent produced %+v", quiet)
	}
}

// TestFeatureWriteEmptyRetractPolicies pins the one documented difference between
// the doors: the CLI turns a retract with nothing recorded into a note (no gate for
// a write that cannot happen), while the screen keeps the step so the feature is
// named in the diff it approves.
func TestFeatureWriteEmptyRetractPolicies(t *testing.T) {
	_ = newDeploySandbox(t)
	noteWrite, _, err := ComposeFeatureWrite(FeatureRequest{
		Verb:               "retract",
		Features:           []Feature{FeatureRules},
		Content:            fixtureContent(),
		EmptyRetractIsNote: true,
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite: %v", err)
	}
	if len(noteWrite.Steps()) != 0 {
		t.Fatalf("the CLI policy composed %d step(s) for an empty retract", len(noteWrite.Steps()))
	}
	if !strings.Contains(strings.Join(noteWrite.Notes(), " "), "nothing to retract") {
		t.Fatalf("notes = %q", noteWrite.Notes())
	}
	stepWrite, _, err := ComposeFeatureWrite(FeatureRequest{
		Verb:     "retract",
		Features: []Feature{FeatureRules},
		Content:  fixtureContent(),
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite (screen policy): %v", err)
	}
	if len(stepWrite.Steps()) != 1 {
		t.Fatalf("the screen policy composed %d step(s), want 1", len(stepWrite.Steps()))
	}
	if !strings.Contains(strings.Join(stepWrite.Lines(), " "), "no recorded destination") {
		t.Fatalf("lines = %q", stepWrite.Lines())
	}
}

// TestWriteWritesOnlyForRealChanges pins the predicate the CLI gates on: an apply
// whose every destination already matches writes nothing, while every other kind
// does (a record-only adopt included: bookkeeping is a persisted write).
func TestWriteWritesOnlyForRealChanges(t *testing.T) {
	sandbox := newDeploySandbox(t)
	write, _, err := ComposeFeatureWrite(FeatureRequest{
		Verb:     "apply",
		Features: []Feature{FeatureRules},
		Enable:   true,
		Content:  fixtureContent(),
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite: %v", err)
	}
	if !write.Writes() {
		t.Fatal("a create plan reports no write")
	}
	sandbox.apply(write.Steps()[0].Plan)
	again, _, err := ComposeFeatureWrite(FeatureRequest{
		Verb:     "apply",
		Features: []Feature{FeatureRules},
		Enable:   true,
		Content:  fixtureContent(),
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite: %v", err)
	}
	if again.Writes() {
		t.Fatal("an all-identical plan reports a write")
	}
	adopt, err := ComposeAdopt(fixtureContent())
	if err != nil {
		t.Fatalf("ComposeAdopt: %v", err)
	}
	if !adopt.Writes() {
		t.Fatal("the record-only adopt reports no write; recording is a persisted write")
	}
}

// TestStepRunReportsTypedSummaries pins the executor: one call per step, the typed
// summary for a door that prints counts, and the short detail chrome for a screen.
func TestStepRunReportsTypedSummaries(t *testing.T) {
	sandbox := newDeploySandbox(t)
	write, _, err := ComposeFeatureWrite(FeatureRequest{
		Verb:     "apply",
		Features: []Feature{FeatureRules},
		Enable:   true,
		Content:  fixtureContent(),
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite: %v", err)
	}
	result, err := write.Steps()[0].Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Apply == nil || result.Apply.Created != 1 {
		t.Fatalf("apply summary = %+v, want 1 created", result.Apply)
	}
	if result.Detail != "rules 1/0/0" {
		t.Fatalf("detail = %q", result.Detail)
	}
	if _, err := os.Stat(sandbox.dest(FeatureRules, "AGENTS.md")); err != nil {
		t.Fatalf("the step did not deploy: %v", err)
	}
	retract, _, err := ComposeFeatureWrite(FeatureRequest{
		Verb:     "retract",
		Features: []Feature{FeatureRules},
		Content:  fixtureContent(),
	})
	if err != nil {
		t.Fatalf("ComposeFeatureWrite (retract): %v", err)
	}
	retractResult, err := retract.Steps()[0].Run()
	if err != nil {
		t.Fatalf("Run (retract): %v", err)
	}
	if retractResult.Retract == nil || len(retractResult.Retract.Destinations) != 1 {
		t.Fatalf("retract summary = %+v, want 1 destination", retractResult.Retract)
	}
	if retractResult.Detail != "rules -1" {
		t.Fatalf("retract detail = %q", retractResult.Detail)
	}
}

// TestComposeFeatureRollbackNamedEmptyStates pins the two cases that must never
// reach a gate: a feature the manifest records nothing for (never applied, or
// retracted already) and a feature whose every destination is a named skip.
func TestComposeFeatureRollbackNamedEmptyStates(t *testing.T) {
	sandbox := newDeploySandbox(t)
	never, err := ComposeFeatureRollback(FeatureRules)
	if err != nil {
		t.Fatalf("ComposeFeatureRollback: %v", err)
	}
	if len(never.Steps()) != 0 {
		t.Fatalf("a never-applied feature composed %d step(s)", len(never.Steps()))
	}
	if !strings.Contains(strings.Join(never.Notes(), " "), "manifest records no destination") {
		t.Fatalf("notes = %q", never.Notes())
	}

	sandbox.apply(sandbox.plan(FeatureRules, fixtureContent()))
	if _, err := Retract(FeatureRules); err != nil {
		t.Fatalf("Retract: %v", err)
	}
	postRetract, err := ComposeFeatureRollback(FeatureRules)
	if err != nil {
		t.Fatalf("ComposeFeatureRollback (post-retract): %v", err)
	}
	if len(postRetract.Steps()) != 0 {
		t.Fatalf("a post-retract rollback composed %d step(s); the retract dropped every claim", len(postRetract.Steps()))
	}

	// A recorded destination whose live file is gone is a named skip, and a skip
	// alone is not a write to approve.
	sandbox.apply(sandbox.plan(FeatureRules, fixtureContent()))
	if err := os.Remove(sandbox.dest(FeatureRules, "AGENTS.md")); err != nil {
		t.Fatalf("remove the deployed fixture file: %v", err)
	}
	skipped, err := ComposeFeatureRollback(FeatureRules)
	if err != nil {
		t.Fatalf("ComposeFeatureRollback (skipped): %v", err)
	}
	if len(skipped.Steps()) != 0 {
		t.Fatalf("an all-skip rollback composed %d step(s)", len(skipped.Steps()))
	}
	if !strings.Contains(strings.Join(skipped.Notes(), " "), RollbackSkipDestinationMissing) {
		t.Fatalf("the note does not name the skip reason: %q", skipped.Notes())
	}
}

// TestComposeFeatureRollbackRendersAndRuns pins the rollback composition end to
// end: the diff names every destination with its action (and its skip reason), and
// the step returns a tree to its pre-install bytes.
func TestComposeFeatureRollbackRendersAndRuns(t *testing.T) {
	sandbox := newDeploySandbox(t)
	preInstall := []byte("# a skill the user wrote\n")
	writeFileRaw(t, sandbox.dest(FeatureSkills, "alpha/SKILL.md"), preInstall)
	sandbox.apply(sandbox.plan(FeatureSkills, fixtureContent()))

	write, err := ComposeFeatureRollback(FeatureSkills)
	if err != nil {
		t.Fatalf("ComposeFeatureRollback: %v", err)
	}
	if len(write.Steps()) != 1 {
		t.Fatalf("steps = %d, want 1", len(write.Steps()))
	}
	lines := strings.Join(write.Lines(), "\n")
	for _, want := range []string{
		"2 destination(s): 1 restore, 1 remove, 0 skip",
		"restore",
		"remove",
		"the apply created it; it moves into the backup tree",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("rollback diff missing %q:\n%s", want, lines)
		}
	}
	result, err := write.Steps()[0].Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Rollback == nil || result.Rollback.Restored() != 1 || result.Rollback.Removed() != 1 {
		t.Fatalf("summary = %+v, want 1 restored and 1 removed", result.Rollback)
	}
	if got := string(readFileRaw(t, sandbox.dest(FeatureSkills, "alpha/SKILL.md"))); got != string(preInstall) {
		t.Fatalf("the destination holds %q, want the pre-install bytes", got)
	}
	if _, err := os.Stat(sandbox.dest(FeatureSkills, "alpha/notes/more.md")); !os.IsNotExist(err) {
		t.Fatalf("the created destination survived the rollback (err = %v)", err)
	}
	// A content rollback states no intent, and a second run finds nothing left to
	// resolve.
	again, err := ComposeFeatureRollback(FeatureSkills)
	if err != nil {
		t.Fatalf("ComposeFeatureRollback (second run): %v", err)
	}
	if len(again.Steps()) != 0 {
		t.Fatalf("a second rollback composed %d step(s)", len(again.Steps()))
	}
}

// TestComposeSettingsRollbackNamedEmptyState pins the sentinel the doors map onto
// their own empty-state sentence.
func TestComposeSettingsRollbackNamedEmptyState(t *testing.T) {
	_ = newDeploySandbox(t)
	write, err := ComposeSettingsRollback()
	if !errors.Is(err, ErrNoSettingsSnapshot) {
		t.Fatalf("err = %v, want ErrNoSettingsSnapshot", err)
	}
	if write != nil {
		t.Fatalf("a snapshotless machine composed a write: %+v", write)
	}
}

// TestComposeAdoptAndRepairEmptyStates pins what the two record-only
// compositions do on a machine with nothing installed: adopt has nothing it could
// present (no slot to record, no destination that already matches), and repair's
// only step is the settings door's absent slot. Neither invents a content feature.
func TestComposeAdoptAndRepairEmptyStates(t *testing.T) {
	_ = newDeploySandbox(t)
	adopt, err := ComposeAdopt(fixtureContent())
	if err != nil {
		t.Fatalf("ComposeAdopt: %v", err)
	}
	if len(adopt.Steps()) != 0 {
		t.Fatalf("an empty machine's adopt composed %d step(s)", len(adopt.Steps()))
	}
	if !strings.Contains(strings.Join(adopt.Notes(), " "), "launcher is missing") {
		t.Fatalf("adopt notes = %q, want the door's named refusal", adopt.Notes())
	}
	repair := ComposeRepair(fixtureContent())
	if len(repair.Steps()) > 1 {
		t.Fatalf("an empty machine's repair composed %d step(s), want at most the door's", len(repair.Steps()))
	}
	for _, step := range repair.Steps() {
		if step.Kind != StepDoorApply {
			t.Fatalf("repair invented a %v step for %s", step.Kind, step.Feature)
		}
	}
}

// TestComposeDoorStepRefusesExplicitlyButDegradesImplicitly pins the launcher
// policy: an explicit target fails loudly, an implicit one becomes a note so a
// missing launcher cannot block the content features a user did ask for.
func TestComposeDoorStepRefusesExplicitlyButDegradesImplicitly(t *testing.T) {
	sandbox := newDeploySandbox(t)
	_ = sandbox
	if _, _, err := ComposeDoorStep(StepDoorApply, true); err == nil {
		t.Fatal("an explicit statusline apply planned without the launcher")
	}
	step, notes, err := ComposeDoorStep(StepDoorApply, false)
	if err != nil {
		t.Fatalf("ComposeDoorStep (implicit): %v", err)
	}
	if step.Lines != nil {
		t.Fatalf("the implicit case composed lines: %q", step.Lines)
	}
	if !strings.Contains(strings.Join(notes, " "), "statusline skipped") {
		t.Fatalf("notes = %q", notes)
	}
	// A retraction never needs the launcher: the ledger reads the file itself.
	step, _, err = ComposeDoorStep(StepDoorRetract, true)
	if err != nil {
		t.Fatalf("ComposeDoorStep (retract without a launcher): %v", err)
	}
	if step.Kind != StepDoorRetract || len(step.Lines) == 0 {
		t.Fatalf("the retract step = %+v", step)
	}
}
