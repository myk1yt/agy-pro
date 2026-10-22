// Composed integrations writes: the one place the step model, the engine diff
// lines, the preset rule and the pending-set rule live.
//
// Both doors turn a decision into ONE composed write: an ordered list of engine
// calls, the exact diff lines each call contributes, and the notes that describe
// what is deliberately not a step. Until SESSION9 T9-D2E those parts existed
// twice (P5-3 T8 §3 disclosed it: "the composition exists twice because package
// main is not importable"), with the preset rule and the pending set stated once
// per door and a comment naming the other file. This file is that single site.
//
// The consent composition is therefore stated once, here, and both doors keep
// their own renderer over it:
//
//	compose  ->  ONE Write: steps (engine calls, in order) + notes
//	          ->  ONE diff: the steps' lines, then the notes, then the door's
//	              own state.json block (the CLI's stateScalarPlan rows, the
//	              screen's integrationIntentRow rows)
//	          ->  ONE consent: confirmIntegrationsWrite / the Approval modal
//	          ->  execute: re-verify every intent row, then Step.Run in order
//
// That is safe because the gates live at the DOORS, not in the verbs:
// integrations.Apply and settings.Core.Set are ungated library calls, so calling
// them directly after an approved diff is exactly what the approval means.
// Nesting a second confirm per key row would ask the same operator the same
// question up to seven times, which is consent fatigue: the failure mode where a
// `y` is typed without reading.
//
// What stays at each door, on purpose:
//
//   - the consent renderer (banner + [y/N] + exit codes vs. the Approval modal);
//   - the intent ROWS: state.json's vocabulary, its key spelling and its
//     stale-base re-verify are the state layer's, so the CLI keeps its
//     stateScalarPlan and the screen keeps integrationIntentRow. This file
//     reports the FACTS (which feature ends up on or off, which preset the result
//     collapses into) and never a key spelling or a stored word;
//   - the user's language: nothing here is localized. Every line is English
//     chrome (action words, masked paths, counts), which is the package's
//     existing posture and decision 6B's; a translated sentence is the door's.
//
// This package must not import internal/mode or internal/settings: internal/
// settings imports this package and internal/mode's tests do too, so either
// import would be an import cycle. That is why the intent vocabulary is the small
// enum below and each door binds it to mode.NormalizeIntegrationToggle.
package integrations

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"agypro/internal/agysettings"
	"agypro/internal/providers"
)

// Intent is one feature's recorded posture, as the compose layer needs it.
//
// It is an enum rather than the stored word because the stored spelling belongs
// to internal/mode (see the file header for why this package cannot import it).
// A door maps mode.NormalizeIntegrationToggle onto this and maps an Intent back
// through the same normalizer's canonical words.
type Intent int

const (
	// IntentUnset is the legacy posture: no integrations-owned intent recorded.
	// It is NOT off. An explicit off asks for a retraction; unset claims nothing,
	// so a pending apply must not treat the two alike.
	IntentUnset Intent = iota
	// IntentOn is an explicit on.
	IntentOn
	// IntentOff is an explicit off.
	IntentOff
)

// IntentReader reports one feature's recorded posture.
type IntentReader func(Feature) Intent

// AnythingRecorded reports whether the reader reports any explicit on or off.
// The CLI's operand-less apply uses it to decide whether the machine records
// intent at all (and therefore whether the manifest fallback applies); the
// screen never falls back, because it shows the per-row posture instead.
func AnythingRecorded(current IntentReader) bool {
	if current == nil {
		return false
	}
	for _, feature := range AllFeatures() {
		if current(feature) != IntentUnset {
			return true
		}
	}
	return false
}

// PendingFeatures is the shared half of the operand-less apply: every feature
// whose recorded intent is an explicit on, in canonical order. A feature whose
// intent is off is never pending, and a feature with no intent at all is not
// pending either.
func PendingFeatures(current IntentReader) []Feature {
	if current == nil {
		return nil
	}
	var pending []Feature
	for _, feature := range AllFeatures() {
		if current(feature) == IntentOn {
			pending = append(pending, feature)
		}
	}
	return pending
}

// ResultIntent returns the toggle map that results from setting every target to
// want. Features outside the target set keep the posture they already record.
func ResultIntent(current IntentReader, targets []Feature, want Intent) map[Feature]Intent {
	after := make(map[Feature]Intent, len(AllFeatures()))
	for _, feature := range AllFeatures() {
		if current != nil {
			after[feature] = current(feature)
		}
	}
	for _, feature := range targets {
		after[feature] = want
	}
	return after
}

// ComposedPreset derives the preset row from the resulting toggle map.
//
//	tools   every feature is explicitly on
//	native  every feature is explicitly off
//	custom  anything else, but ONLY when at least one feature ends up
//	        explicitly on
//
// The last clause matters: recording `custom` for a machine whose toggles are all
// absent would assert a composed shape the user never stated, so "" (no row at
// all) is returned there and the caller leaves the row alone.
func ComposedPreset(after map[Feature]Intent) Preset {
	all := AllFeatures()
	on, off := 0, 0
	for _, feature := range all {
		switch after[feature] {
		case IntentOn:
			on++
		case IntentOff:
			off++
		}
	}
	switch {
	case on == len(all):
		return PresetTools
	case off == len(all):
		return PresetNative
	case on > 0:
		return PresetCustom
	}
	return ""
}

// DiffLineLimit caps how many destination lines one feature contributes to a
// consent diff. The count line above them is exact, so a bounded list never hides
// a write: the operator still sees how many destinations change and which the
// first ones are.
const DiffLineLimit = 12

// DeployLines renders one feature's plan: the count line, then the engine's
// deploy.go diff lines for the destinations that change. An identical
// destination writes nothing, so its line is summarized by the count instead of
// filling the gate with no-op rows.
func DeployLines(feature Feature, plan *DeployPlan) []string {
	created, updated, identical := plan.Counts()
	lines := []string{fmt.Sprintf("%-11s%d create, %d update, %d identical", string(feature), created, updated, identical)}
	shown := 0
	for _, line := range plan.Lines() {
		if strings.HasPrefix(line, string(PlanActionIdentical)) {
			continue
		}
		if shown == DiffLineLimit {
			lines = append(lines, "  ... and more (the count above is exact)")
			break
		}
		lines = append(lines, "  "+line)
		shown++
	}
	if shown == 0 {
		lines = append(lines, "  (nothing to write: every destination already holds the engine's bytes)")
	}
	return lines
}

// RetractLines renders one feature's retraction from the engine's read-only
// Status: the destinations the manifest claims (they move to a timestamped
// backup) and the foreign files the retract leaves alone. A retraction is never
// applied to a foreign file, so those lines say so instead of listing them as
// planned moves. The move rows carry the same DiffLineLimit cap and truncation
// note as DeployLines and RollbackLines; the head count is exact either way, and
// the "left alone" rows are never counted or capped (they are not moves).
func RetractLines(feature Feature, status *FeatureStatus) []string {
	owned := 0
	lines := []string{}
	for _, destination := range status.Destinations {
		if destination.State == StateForeign {
			lines = append(lines, fmt.Sprintf("%-11s%s is not ours; left alone", "", providers.MaskOperand(destination.Path)))
			continue
		}
		owned++
		if owned > DiffLineLimit {
			if owned == DiffLineLimit+1 {
				lines = append(lines, "  ... and more (the count above is exact)")
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("%-11s%s", "retract", providers.MaskOperand(destination.Path)))
	}
	head := fmt.Sprintf("%-11s%d destination(s) move to a timestamped backup", string(feature), owned)
	if owned == 0 {
		head = fmt.Sprintf("%-11sno recorded destination (nothing to retract)", string(feature))
	}
	return append([]string{head}, lines...)
}

// SettingsRollbackLines renders the settings.json rollback chain, newest first:
// the file, the snapshot that would be restored, the rest of the chain, and the
// two facts a user needs before approving (the live bytes are archived first, and
// the bookkeeping entry is dropped rather than re-recorded).
func SettingsRollbackLines(snapshots []SettingsSnapshot) []string {
	if len(snapshots) == 0 {
		return nil
	}
	lines := []string{
		fmt.Sprintf("%-11s%s", "rollback", "restores the newest snapshot holding valid JSON"),
		fmt.Sprintf("%-11s%s", "file", providers.MaskOperand(snapshots[0].Path)),
	}
	for i, snapshot := range snapshots {
		if i == 0 {
			continue
		}
		if i == DiffLineLimit {
			lines = append(lines, fmt.Sprintf("%-11s%s", "note", "... and more (the chain is ordered newest first)"))
			break
		}
		lines = append(lines, fmt.Sprintf("%-11s%s (%s chain, %s)", "", providers.MaskOperand(snapshot.Path),
			snapshot.Scheme, snapshot.Taken.Format("2006-01-02 15:04:05")))
	}
	lines = append(lines,
		fmt.Sprintf("%-11s%s", "note", "the bytes that are live now are archived first; the snapshot is copied, never moved"),
		fmt.Sprintf("%-11s%s", "note", "the bookkeeping entry is dropped, not re-recorded (run adopt to record the restored bytes)"),
	)
	return lines
}

// RollbackLines renders what RollbackFeature would do to a deploy tree, from the
// read-only PendingRollback classification: the count line, then one row per
// destination. A skip carries its stable reason word (RollbackSkip*) rather than
// a sentence, so a renderer never has to parse Detail.
func RollbackLines(feature Feature, pending []RollbackDestination) []string {
	restored, removed, skipped := 0, 0, 0
	for _, destination := range pending {
		switch destination.Action {
		case RollbackActionRestore:
			restored++
		case RollbackActionRemove:
			removed++
		default:
			skipped++
		}
	}
	lines := []string{fmt.Sprintf("%-11s%d destination(s): %d restore, %d remove, %d skip",
		string(feature), len(pending), restored, removed, skipped)}
	shown := 0
	for _, destination := range pending {
		if shown == DiffLineLimit {
			lines = append(lines, "  ... and more (the count above is exact)")
			break
		}
		masked := providers.MaskOperand(destination.Path)
		switch destination.Action {
		case RollbackActionRestore:
			lines = append(lines, fmt.Sprintf("%-11s%s <- %s", string(destination.Action), masked,
				providers.MaskOperand(destination.Source)))
		case RollbackActionRemove:
			lines = append(lines, fmt.Sprintf("%-11s%s (the apply created it; it moves into the backup tree)",
				string(destination.Action), masked))
		default:
			lines = append(lines, fmt.Sprintf("%-11s%s (%s)", string(destination.Action), masked, destination.SkipReason))
		}
		shown++
	}
	return lines
}

// StepKind is what one engine call does.
type StepKind int

const (
	// StepApply writes a feature's plan (create/update/identical).
	StepApply StepKind = iota
	// StepAdopt applies an identical-only plan, which records destinations
	// without writing a byte (record-only adopt).
	StepAdopt
	// StepRetract moves a feature's recorded destinations into a backup.
	StepRetract
	// StepRollback returns a feature's deploy tree to its pre-install bytes.
	StepRollback
	// StepDoorApply writes the statusLine slot through the settings ledger.
	StepDoorApply
	// StepDoorRetract removes the statusLine slot through the ledger.
	StepDoorRetract
	// StepDoorAdopt records the statusLine slot that is already on disk.
	StepDoorAdopt
	// StepDoorRollback restores settings.json from its snapshot chain.
	StepDoorRollback
)

// Step is one engine call inside a composed write, together with the exact-diff
// lines it contributes.
type Step struct {
	// Feature is the feature the call addresses (the statusline door for the
	// StepDoor* kinds).
	Feature Feature
	// Kind is the call.
	Kind StepKind
	// Plan is the deployment plan StepApply and StepAdopt apply.
	Plan *DeployPlan
	// Door is the settings-door plan StepDoorApply writes.
	Door *StatusLineDoor
	// Lines are the exact diff lines this step contributes. A nil Lines means
	// the step was not composed at all (the caller reports the note instead of
	// gating a write that cannot happen).
	Lines []string
}

// StepResult is what one engine call reported. Detail is the short English chrome
// sentence a screen shows on its message line; the typed summaries are there for
// a door that prints counts (the CLI's one-line [SUCCESS] report).
type StepResult struct {
	Feature      Feature
	Kind         StepKind
	Detail       string
	Apply        *ApplySummary
	Retract      *RetractSummary
	Rollback     *RollbackSummary
	Door         *StatusLineSummary
	DoorRollback *StatusLineRollback
}

// Run performs the step's one engine call. It never prompts and never renders:
// the consent happened before it was called. A step whose required plan is missing
// is a composition bug, reported as an error rather than dereferenced.
func (s Step) Run() (StepResult, error) {
	result := StepResult{Feature: s.Feature, Kind: s.Kind}
	switch s.Kind {
	case StepApply, StepAdopt:
		if s.Plan == nil {
			return result, fmt.Errorf("the composed step for %q carries no deployment plan", s.Feature)
		}
		summary, err := Apply(s.Plan)
		if err != nil {
			return result, err
		}
		result.Apply = summary
		if s.Kind == StepAdopt {
			result.Detail = fmt.Sprintf("%s %d recorded", s.Feature, len(summary.Entries))
		} else {
			result.Detail = fmt.Sprintf("%s %d/%d/%d", s.Feature, summary.Created, summary.Updated, summary.Identical)
		}
	case StepRetract:
		summary, err := Retract(s.Feature)
		if err != nil {
			return result, err
		}
		result.Retract = summary
		result.Detail = fmt.Sprintf("%s -%d", s.Feature, len(summary.Destinations))
	case StepRollback:
		summary, err := RollbackFeature(s.Feature)
		if err != nil {
			return result, err
		}
		result.Rollback = summary
		result.Detail = fmt.Sprintf("%s %d restored, %d removed, %d skipped",
			s.Feature, summary.Restored(), summary.Removed(), summary.Skipped())
	case StepDoorApply:
		if s.Door == nil {
			return result, fmt.Errorf("the statusLine apply step carries no door plan")
		}
		summary, err := ApplyStatusLine(s.Door)
		if err != nil {
			return result, err
		}
		result.Door = summary
		result.Detail = "statusline " + string(summary.Action)
	case StepDoorRetract:
		summary, err := RetractStatusLine()
		if err != nil {
			return result, err
		}
		result.Door = summary
		result.Detail = "statusline retract " + summary.Detail
	case StepDoorAdopt:
		summary, err := AdoptStatusLine()
		if err != nil {
			return result, err
		}
		result.Door = summary
		result.Detail = "statusline " + string(summary.Provenance)
	case StepDoorRollback:
		summary, err := RollbackStatusLine()
		if err != nil {
			return result, err
		}
		result.DoorRollback = summary
		result.Detail = string(summary.Scheme) + " " + summary.Taken.Format("2006-01-02 15:04:05")
	}
	return result, nil
}

// Write is ONE composed integrations write: the ordered engine calls, the exact
// diff lines they contribute, and the notes that say what is not a step.
type Write struct {
	// Verb is the verb that composed it, for a door that wants to name it.
	Verb string

	steps []Step
	notes []string
}

// NewWrite starts a composed write for verb.
func NewWrite(verb string) *Write { return &Write{Verb: verb} }

// AddStep appends one engine call. Step order is execution order.
func (w *Write) AddStep(step Step) { w.steps = append(w.steps, step) }

// AddNote appends observations that write nothing. A note is how a plan says
// "this half of the request is not a step" (a retract with nothing owned, a
// statusline slot that needs no repair) without gating a write that will not
// happen.
func (w *Write) AddNote(notes ...string) {
	for _, note := range notes {
		if note != "" {
			w.notes = append(w.notes, note)
		}
	}
}

// Steps returns the engine calls in plan order.
func (w *Write) Steps() []Step { return w.steps }

// Notes returns the notes in composition order.
func (w *Write) Notes() []string { return w.notes }

// Lines renders the exact diff a consent gate shows: every note first, then every
// step's lines. The door appends its own state.json block after this.
//
// The notes lead because the diff IS the approved artifact and a caller renders it
// inside its own height budget: a note says what the write will NOT do (a
// subtracted door step, a retraction with nothing recorded), so trailing it past a
// 100+ line deploy diff would let an operator consent to the degraded write without
// ever seeing the reason. The leading "note" column is the stable form callers and
// pins key on (it never collides with a step's feature column).
func (w *Write) Lines() []string {
	if w == nil {
		return nil
	}
	lines := make([]string, 0, len(w.notes)+len(w.steps))
	for _, note := range w.notes {
		lines = append(lines, fmt.Sprintf("%-11s%s", "note", note))
	}
	for _, step := range w.steps {
		lines = append(lines, step.Lines...)
	}
	return lines
}

// Writes reports whether any step changes anything on disk or in the manifest. An
// apply whose every destination already holds the engine's bytes does not count;
// every other kind does, including the record-only adopt (bookkeeping is a
// persisted write).
func (w *Write) Writes() bool {
	if w == nil {
		return false
	}
	for _, step := range w.steps {
		if step.Kind == StepApply {
			if step.Plan != nil && step.Plan.Writes() > 0 {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// ComposeDeployStep composes one content feature's engine call via the engine's
// own renderers.
//
// emptyRetractIsNote makes a retraction with nothing recorded a NOTE instead of a
// step, which is the CLI's rule (a gate is never presented for a write that
// cannot happen). The screen keeps the step so the operator sees the feature
// named in the diff it approves; the two doors differ there on purpose, and this
// flag is the one place that difference is stated.
func ComposeDeployStep(feature Feature, kind StepKind, content fs.FS, emptyRetractIsNote bool) (Step, []string, error) {
	step := Step{Feature: feature, Kind: kind}
	switch kind {
	case StepApply, StepAdopt:
		plan, err := Plan(feature, content)
		if err != nil {
			return step, nil, err
		}
		step.Plan = plan
		if kind == StepAdopt && plan.Writes() > 0 {
			// Record-only: an adopt that wrote bytes would be an apply wearing
			// another name.
			return step, nil, fmt.Errorf("%d destination(s) differ from the engine's content; adopt records only what already matches (run `agy pro integrations apply %s`)",
				plan.Writes(), feature)
		}
		step.Lines = DeployLines(feature, plan)
	case StepRetract:
		status, err := Status(feature)
		if err != nil {
			return step, nil, err
		}
		owned := 0
		for _, destination := range status.Destinations {
			if destination.State != StateForeign {
				owned++
			}
		}
		if owned == 0 && emptyRetractIsNote {
			return Step{Feature: feature, Kind: kind},
				[]string{fmt.Sprintf("%s: no recorded destination (nothing to retract)", feature)}, nil
		}
		step.Lines = RetractLines(feature, status)
	}
	return step, nil, nil
}

// ComposeDoorStep composes the settings-line door's engine call for the
// statusline feature.
//
// explicit marks a verb that named the feature: an explicit target fails loudly
// when the door cannot be planned, while an implicit (whole-posture or pending)
// target degrades to a note, so a missing launcher cannot block the content
// features a user did ask for.
func ComposeDoorStep(kind StepKind, explicit bool) (Step, []string, error) {
	step := Step{Feature: FeatureStatusline, Kind: kind}
	door, err := StatusLinePlan("")
	if err != nil {
		var launcher *StatusLineLauncherError
		switch {
		case errors.As(err, &launcher) && kind == StepDoorRetract:
			// A missing launcher does not block the removal: RetractStatusLine
			// reads the file itself through the ledger. The classification is what
			// the launcher blockaded, so the diff says exactly that instead of
			// claiming the slot is absent.
			path, pathErr := agysettings.GlobalSettingsPath()
			if pathErr != nil {
				return degradeDoorStep(kind, explicit, pathErr)
			}
			step.Lines = []string{
				fmt.Sprintf("%-11s%s", "statusline", "retract"),
				fmt.Sprintf("%-11s%s", "file", providers.MaskOperand(path)),
			}
			return step, []string{"the agy-tokens launcher is missing (" + launcher.Shape + "), so the current object could not be classified; the ledger archives the previous bytes first"}, nil
		case errors.As(err, &launcher) && kind == StepDoorAdopt:
			// Adoption needs no launcher (it is the step a machine with a broken
			// install still has to be able to take), but without the classification
			// there is no way to say what would be recorded, and a record-only verb
			// must not present a gate it cannot describe.
			return Step{Feature: FeatureStatusline, Kind: kind},
				[]string{"statusline: the agy-tokens launcher is missing, so the slot could not be classified; adopt records nothing here"}, nil
		default:
			return degradeDoorStep(kind, explicit, err)
		}
	}
	switch kind {
	case StepDoorApply:
		step.Door = door
		step.Lines = append([]string{fmt.Sprintf("%-11s%s", "statusline", string(door.Action))}, door.Lines()...)
	case StepDoorAdopt:
		if door.Current == nil {
			return Step{Feature: FeatureStatusline, Kind: kind},
				[]string{"statusline: no slot on disk, so there is nothing to adopt"}, nil
		}
		step.Lines = []string{fmt.Sprintf("%-11s%s", "statusline",
			"adopt: records the slot already on disk (bytes untouched, provenance from the command form)")}
	case StepDoorRetract:
		step.Lines = append([]string{fmt.Sprintf("%-11s%s", "statusline", "retract")},
			StatusLineRemovalLines(door.SettingsPath, door.Current, door.UnmanagedKeys)...)
	}
	return step, nil, nil
}

// degradeDoorStep turns a statusLine plan failure into either a hard error (an
// explicit target) or a note plus no step (an implicit one).
func degradeDoorStep(kind StepKind, explicit bool, err error) (Step, []string, error) {
	if explicit {
		return Step{}, nil, err
	}
	return Step{Feature: FeatureStatusline, Kind: kind},
		[]string{"statusline skipped: " + providers.MaskOperand(err.Error())}, nil
}

// FeatureRequest is the input of one apply / retract / pending composition.
//
// The fields after Intent exist because the two doors make different, documented
// choices; each one is named with the choice it carries so the difference lives
// here instead of in two files.
type FeatureRequest struct {
	// Verb names the composition (the CLI prints it on a refused re-verify).
	Verb string
	// Features is the resolved target set, in the order the door wants the
	// intent rows written.
	Features []Feature
	// Enable is true for a deployment and false for a retraction.
	Enable bool
	// Content is the deployment source (the embedded snapshot in production).
	Content fs.FS
	// Intent reads the posture the result is composed against. It may be nil for
	// a request that records no intent.
	Intent IntentReader
	// Preset is the posture row to record; "" derives it from the result.
	Preset Preset
	// RecordIntent false composes a write that states no intent at all: the CLI's
	// operand-less pending apply reconciles what the intent already says and must
	// not record intent the user never stated. The screen records it, because its
	// pending row is an explicit operator choice.
	RecordIntent bool
	// EmptyRetractIsNote is ComposeDeployStep's flag (the CLI sets it, the screen
	// does not).
	EmptyRetractIsNote bool
	// DoorExplicit marks a request that named its target, so an unplannable
	// settings door fails loudly instead of degrading to a note. Two requests are
	// implicit instead: the CLI's pending apply and a whole-posture apply (a
	// preset deploy, which degrades so that a launcherless machine still deploys
	// the content features). The screen reaches the same shape from its preset
	// row, where the operator picked the posture rather than one feature
	// (screen_integrations.go composeFeatureWrite).
	DoorExplicit bool
}

// FeatureIntent is the intent one composed write wants recorded for a feature.
type FeatureIntent struct {
	Feature Feature
	After   Intent
}

// IntentPlan is the intent side of a composed write, as facts. Turning them into
// rows is the door's job: the key spelling, the stored word and the re-verify are
// the state layer's (the CLI's stateScalarPlan, the screen's integrationIntentRow).
type IntentPlan struct {
	// Features is one row per composed feature, in request order.
	Features []FeatureIntent
	// Preset is the posture row to record, or "" when the rule does not fire and
	// the row must be left alone.
	Preset Preset
}

// ComposeFeatureWrite composes an apply (Enable) or a retract over a feature set,
// plus the intent facts the write wants recorded.
func ComposeFeatureWrite(req FeatureRequest) (*Write, *IntentPlan, error) {
	write := NewWrite(req.Verb)
	intent := &IntentPlan{}
	want := IntentOff
	if req.Enable {
		want = IntentOn
	}
	for _, feature := range req.Features {
		var (
			step  Step
			notes []string
			err   error
		)
		switch {
		case feature == FeatureStatusline && req.Enable:
			step, notes, err = ComposeDoorStep(StepDoorApply, req.DoorExplicit)
		case feature == FeatureStatusline:
			step, notes, err = ComposeDoorStep(StepDoorRetract, req.DoorExplicit)
		case req.Enable:
			step, notes, err = ComposeDeployStep(feature, StepApply, req.Content, req.EmptyRetractIsNote)
		default:
			step, notes, err = ComposeDeployStep(feature, StepRetract, req.Content, req.EmptyRetractIsNote)
		}
		if err != nil {
			return nil, nil, err
		}
		if step.Lines != nil {
			write.AddStep(step)
		}
		write.AddNote(notes...)
		if !req.RecordIntent {
			continue
		}
		intent.Features = append(intent.Features, FeatureIntent{Feature: feature, After: want})
	}
	if req.RecordIntent {
		intent.Preset = req.Preset
		if intent.Preset == "" {
			intent.Preset = ComposedPreset(ResultIntent(req.Intent, req.Features, want))
		}
	}
	return write, intent, nil
}

// ComposeAdopt composes the record-only adopt: the settings door's slot plus every
// content feature whose destinations already hold the engine's bytes. A
// destination that differs is never adopted (that is an apply), so it becomes a
// note naming the verb that would.
func ComposeAdopt(content fs.FS) (*Write, error) {
	write := NewWrite("adopt")
	step, notes, err := ComposeDoorStep(StepDoorAdopt, true)
	if err != nil {
		return nil, err
	}
	if step.Lines != nil {
		write.AddStep(step)
	}
	write.AddNote(notes...)
	for _, feature := range AllFeatures() {
		if feature == FeatureStatusline {
			continue
		}
		step, notes, err := ComposeDeployStep(feature, StepAdopt, content, false)
		if err != nil {
			write.AddNote(fmt.Sprintf("%s not adopted: %s", feature, providers.MaskOperand(err.Error())))
			continue
		}
		if step.Lines != nil {
			write.AddStep(step)
		}
		write.AddNote(notes...)
	}
	return write, nil
}

// ComposeRepair composes the drift reconciliation: the settings door's slot when
// it differs from what it should hold, and every content feature the manifest
// claims whose destinations drifted or went missing. A drifted destination is
// re-planned and re-applied, which IS the repair. Foreign files are named and left
// alone, and a feature with nothing recorded is never invented.
func ComposeRepair(content fs.FS) *Write {
	write := NewWrite("repair")
	door, doorErr := StatusLinePlan("")
	switch {
	case doorErr == nil && (door.Action == StatusLineActionCreate || door.Action == StatusLineActionUpdate):
		write.AddStep(Step{
			Feature: FeatureStatusline,
			Kind:    StepDoorApply,
			Door:    door,
			Lines: append([]string{fmt.Sprintf("%-11s%s", "statusline", "repair: "+string(door.Action))},
				door.Lines()...),
		})
	case doorErr == nil:
		write.AddNote("statusline is already in its intended posture; nothing to repair")
	default:
		var foreign *StatusLineForeignError
		if errors.As(doorErr, &foreign) {
			write.AddNote("statusline needs a repair decision: " + providers.MaskOperand(foreign.Repair))
		} else {
			write.AddNote("statusline could not be planned: " + providers.MaskOperand(doorErr.Error()))
		}
	}
	for _, feature := range AllFeatures() {
		if feature == FeatureStatusline {
			continue
		}
		status, err := Status(feature)
		if err != nil {
			write.AddNote(fmt.Sprintf("%s could not be inspected: %s", feature, providers.MaskOperand(err.Error())))
			continue
		}
		if status.Foreign > 0 {
			write.AddNote(fmt.Sprintf("%s: %d foreign file(s) are not ours and are left alone", feature, status.Foreign))
		}
		if !status.Deployed || (status.Drifted == 0 && status.Missing == 0) {
			continue
		}
		step, notes, err := ComposeDeployStep(feature, StepApply, content, false)
		if err != nil {
			write.AddNote(fmt.Sprintf("%s could not be re-planned: %s", feature, providers.MaskOperand(err.Error())))
			continue
		}
		write.AddNote(notes...)
		if step.Lines == nil {
			continue
		}
		step.Lines[0] = fmt.Sprintf("%s (%d drifted, %d missing)", step.Lines[0], status.Drifted, status.Missing)
		write.AddStep(step)
	}
	return write
}

// ErrNoSettingsSnapshot means the machine has no settings.json snapshot in either
// documented chain, so a settings rollback has nothing to restore. It is a named
// empty state, not a failure: no write was composed.
var ErrNoSettingsSnapshot = errors.New("no settings.json snapshot to roll back to")

// ComposeSettingsRollback composes the settings.json restore from the newest
// coherent snapshot. A machine with no snapshot at all returns
// ErrNoSettingsSnapshot and a nil write.
func ComposeSettingsRollback() (*Write, error) {
	snapshots, err := SettingsSnapshots()
	if err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return nil, ErrNoSettingsSnapshot
	}
	write := NewWrite("rollback")
	write.AddStep(Step{
		Feature: FeatureStatusline,
		Kind:    StepDoorRollback,
		Lines:   SettingsRollbackLines(snapshots),
	})
	return write, nil
}

// ComposeFeatureRollback composes one content feature's deploy-tree rollback from
// the read-only PendingRollback classification.
//
// The returned write has no step and one note when there is nothing to do: no
// manifest destination at all (a post-retract or never-applied feature, which the
// D1 contract calls a named empty state) or only skipped destinations. A gate is
// never presented for a write that cannot happen, and the note says which of the
// two it is. A content rollback writes NO intent rows: it returns a tree to its
// pre-install bytes, which is not a posture the user stated.
func ComposeFeatureRollback(feature Feature) (*Write, error) {
	pending, err := PendingRollback(feature)
	if err != nil {
		return nil, err
	}
	write := NewWrite("rollback " + string(feature))
	if len(pending) == 0 {
		write.AddNote(fmt.Sprintf("%s: nothing to roll back: the manifest records no destination for this feature", feature))
		return write, nil
	}
	resolved, skipped := 0, 0
	reasons := []string{}
	seen := map[string]bool{}
	for _, destination := range pending {
		if destination.Action == RollbackActionSkip {
			skipped++
			if destination.SkipReason != "" && !seen[destination.SkipReason] {
				seen[destination.SkipReason] = true
				reasons = append(reasons, destination.SkipReason)
			}
			continue
		}
		resolved++
	}
	if resolved == 0 {
		write.AddNote(fmt.Sprintf("%s: nothing to roll back: %d recorded destination(s) were skipped (%s)",
			feature, skipped, strings.Join(reasons, ", ")))
		return write, nil
	}
	write.AddStep(Step{Feature: feature, Kind: StepRollback, Lines: RollbackLines(feature, pending)})
	return write, nil
}
