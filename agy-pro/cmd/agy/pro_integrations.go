package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"agypro/internal/agysettings"
	"agypro/internal/integrations"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/profiles"
)

// Integrations surface command (P5-3 T8):
//
//	agy pro integrations [--yes]                 usage
//	agy pro integrations status                  read-only posture report (no gate)
//	agy pro integrations apply [W] [--yes]       W = feature | preset; no W = pending
//	agy pro integrations retract [W] [--yes]     W = feature | preset; no W = everything
//	agy pro integrations preset <native|tools> [--yes]
//	agy pro integrations adopt [--yes]           record-only: no file is written
//	agy pro integrations rollback [W] [--yes]    W = a content feature; no W = settings.json
//	agy pro integrations repair [--yes]          reconcile drifted and missing destinations
//
// Every writing verb composes ONE plan and prints ONE exact diff, then asks for
// consent once. The plan can name deployments, retractions, the settings door
// and the six state.json intent rows at the same time; the consent covers all
// of them, and the engine calls plus Core.Set run afterwards without a second
// prompt (see integrationPlan's doc comment for why the gates are not nested).
//
// The vocabulary is not this file's: features and presets come from
// internal/integrations (ParseFeature / ParsePreset), the intent rows from
// internal/settings' Core.Set key table (P5-3 T6), the composition itself from
// internal/integrations/compose.go (the step model, the diff renderers, the
// preset rule and the pending set, shared with the hub screen). This door binds
// the intent rows and asks.

const proIntegrationsUsage = `Usage: agy pro integrations <verb> [--yes]
  status                        Read-only posture report: state.json intent plus the engine state
  apply [<feature>|<preset>]    Deploy; no operand applies the pending set
  retract [<feature>|<preset>]  Remove what the engine deployed; no operand removes everything
  preset native|tools           Reach that preset and record it
  adopt                         Record what is already on disk (no file is written)
  rollback [<feature>]          Restore settings.json (no operand), or a feature's pre-install tree
  repair                        Reconcile drifted and missing destinations
  features: statusline | hook | rules | plugins | skills
  presets:  native | tools        (custom is derived from the toggles, never applied)
  Every writing verb prints its exact diff and then asks for a y/N answer; a
  non-interactive stdin must pass --yes, and nothing is written without it.`

// confirmIntegrationsWrite gates one composed integrations write. It is the
// same mechanism (and the same transcript shape) as confirmStateWrite and
// confirmModeWrite; only the banner, the prompt target and the refusal noun
// differ, because one consent here covers a file deployment, the settings door
// and the state.json intent rows together.
func confirmIntegrationsWrite(in io.Reader, out, errw io.Writer, diff []string, yes bool) (bool, int) {
	return confirmPersistedWrite(in, out, errw, "[agy-pro] Approve Integrations Write", diff,
		"the integrations configuration", "an integrations write", yes)
}

// runIntegrationsCmd executes `agy pro integrations [args]` and returns the
// process exit code (0 ok, 1 runtime error, 2 usage/flag error).
func runIntegrationsCmd(args []string, in io.Reader, out, errw io.Writer) int {
	rest, yes := splitYesFlag(args)
	if len(rest) == 0 {
		fmt.Fprintf(errw, "[agy-pro] integrations needs a verb\n%s\n", proIntegrationsUsage)
		return 2
	}
	verb := strings.ToLower(strings.TrimSpace(rest[0]))
	operands := rest[1:]
	switch verb {
	case "status":
		if len(operands) != 0 {
			return integrationsUsageError(errw, operands[0])
		}
		return integrationsStatus(out, errw)
	case "apply":
		return integrationsWrite(verb, operands, in, out, errw, yes)
	case "retract":
		return integrationsWrite(verb, operands, in, out, errw, yes)
	case "preset":
		return integrationsPreset(operands, in, out, errw, yes)
	case "adopt":
		if len(operands) != 0 {
			return integrationsUsageError(errw, operands[0])
		}
		return integrationsAdopt(in, out, errw, yes)
	case "rollback":
		return integrationsRollbackVerb(operands, in, out, errw, yes)
	case "repair":
		if len(operands) != 0 {
			return integrationsUsageError(errw, operands[0])
		}
		return integrationsRepair(in, out, errw, yes)
	case "help", "--help", "-h":
		fmt.Fprintln(out, proIntegrationsUsage)
		return 0
	default:
		return integrationsUsageError(errw, rest[0])
	}
}

// integrationsUsageError reports an unusable operand (masked: an operand can be
// a misdirected key paste) and returns the usage exit code.
func integrationsUsageError(errw io.Writer, operand string) int {
	fmt.Fprintf(errw, "[agy-pro] unknown integrations verb or operand %q\n%s\n", providers.MaskOperand(operand), proIntegrationsUsage)
	return 2
}

// ---------------------------------------------------------------------------
// intent reads

// intentToggle reads one feature's persisted intent. "" is the legacy posture
// (no integrations-owned intent), which is NOT the same as an explicit "off":
// off asks for a retract, "" claims nothing. The reader is mode's normalizer, so
// an unrecognized stored value is inert here instead of being reinterpreted.
func intentToggle(st *mode.State, feature integrations.Feature) string {
	if st == nil {
		return ""
	}
	switch feature {
	case integrations.FeatureStatusline:
		return mode.NormalizeIntegrationToggle(st.IntegrationStatusline)
	case integrations.FeatureHook:
		return mode.NormalizeIntegrationToggle(st.IntegrationHook)
	case integrations.FeatureRules:
		return mode.NormalizeIntegrationToggle(st.IntegrationRules)
	case integrations.FeaturePlugins:
		return mode.NormalizeIntegrationToggle(st.IntegrationPlugins)
	case integrations.FeatureSkills:
		return mode.NormalizeIntegrationToggle(st.IntegrationSkills)
	}
	return ""
}

// intentPreset reads the composed preset intent, normalized by mode.
func intentPreset(st *mode.State) string {
	if st == nil {
		return ""
	}
	return mode.NormalizeIntegrationPreset(st.IntegrationPreset)
}

// intentToggleKey is the Core.Set key of one feature. The spelling is the
// canonical singular/hyphenated one T6 pins; the aliases ParseFeature accepts
// never reach the state layer.
func intentToggleKey(feature integrations.Feature) string {
	return "integration-" + string(feature)
}

// intentWord renders a normalized toggle for the transcript. "unset" is spelled
// out separately from "off" so the two postures never read alike.
func intentWord(v string) string {
	switch v {
	case mode.IntegrationToggleOn:
		return "on"
	case mode.IntegrationToggleOff:
		return "off"
	default:
		return "unset"
	}
}

// loadIntentState reads state.json through the presentation seam. A read failure
// refuses the whole verb: a plan built on an unknown base could present a diff
// the write then cannot honour.
func loadIntentState() (*mode.State, error) {
	st, err := stateSnapshot()
	if err != nil || st == nil {
		return nil, errors.New("state.json could not be read, so the integrations intent could not be composed; nothing written")
	}
	return st, nil
}

// ---------------------------------------------------------------------------
// status

// integrationsStatus prints the per-feature posture: the state.json intent, the
// engine's own verdict, and for the statusLine feature the settings door's
// classification. It writes nothing and takes no consent gate (pick 7: a launch
// path may read this).
//
// The engine's per-destination Detail sentences print as continuation lines under
// their feature row. The engine computes them for the cases where a count is not
// the whole story (why a drifted file drifted, what a clean file cannot do), and a
// count alone would leave the operator with "1 drifted" and no next step.
func integrationsStatus(out, errw io.Writer) int {
	st, err := loadIntentState()
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	fmt.Fprintln(out, "agy pro integrations status (read-only)")
	fmt.Fprintf(out, "%-11s%-9s%s\n", "feature", "intent", "engine")
	code := 0
	for _, feature := range integrations.AllFeatures() {
		posture, details, perr := featurePosture(feature)
		if perr != nil {
			code = 1
		}
		fmt.Fprintf(out, "%-11s%-9s%s\n", string(feature), intentWord(intentToggle(st, feature)), stripTerminalControl(posture))
		for _, detail := range details {
			// Aligned under the posture column: the row above is 11 + 9 columns of
			// feature and intent. Both fields pass stripTerminalControl at this sink
			// rather than at their builders (FIX-12): a posture word and a Detail can
			// embed a path or a field name read out of the user-editable live config,
			// and the printer is the one place that covers every source of a line.
			fmt.Fprintf(out, "%-20s%s\n", "", stripTerminalControl(detail))
		}
	}
	preset := intentPreset(st)
	if preset == "" {
		preset = "unset"
	}
	fmt.Fprintf(out, "%-11s%s\n", "preset", preset)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "unset intent is the legacy posture: no integrations-owned claim is recorded for")
	fmt.Fprintln(out, "that feature. It is not the same as off, which asks for a retract.")
	return code
}

// featurePosture renders one feature's engine verdict plus the engine's own detail
// sentences for its destinations. The statusLine feature has no content layout (it
// is the settings door), so it is classified by StatusLinePlan; every other feature
// is classified by the deploy engine's read-only Status.
func featurePosture(feature integrations.Feature) (string, []string, error) {
	if feature == integrations.FeatureStatusline {
		posture, err := statusLinePosture()
		return posture, nil, err
	}
	status, err := integrations.Status(feature)
	if err != nil {
		return "unavailable: " + providers.MaskOperand(err.Error()), nil, err
	}
	if status == nil || !status.Deployed {
		if status != nil && status.Foreign > 0 {
			return fmt.Sprintf("not deployed, %d foreign file(s) (left alone)", status.Foreign), nil, nil
		}
		return "not deployed", nil, nil
	}
	parts := []string{}
	if status.Clean > 0 {
		parts = append(parts, fmt.Sprintf("%d clean", status.Clean))
	}
	if status.Drifted > 0 {
		parts = append(parts, fmt.Sprintf("%d drifted", status.Drifted))
	}
	if status.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", status.Missing))
	}
	if status.Foreign > 0 {
		parts = append(parts, fmt.Sprintf("%d foreign (left alone)", status.Foreign))
	}
	if len(parts) == 0 {
		parts = append(parts, "no destinations")
	}
	return strings.Join(parts, ", "), destinationDetails(status), nil
}

// destinationDetails collects the engine's Detail sentence per destination, in
// path order. The engine masks the secrets it names but not the control characters
// they can carry (providers.MaskOperand is a credential masker, not a transcript
// boundary), so the strings are handed on unchanged and the PRINT sink applies
// stripTerminalControl: the same neutralisation then covers the posture word, the
// consent diff and the ledger's field-name rows without a second copy per builder.
func destinationDetails(status *integrations.FeatureStatus) []string {
	if status == nil {
		return nil
	}
	details := make([]string, 0, len(status.Destinations))
	for _, destination := range status.Destinations {
		if destination.Detail == "" {
			continue
		}
		details = append(details, destination.Detail)
	}
	return details
}

// statusLinePosture renders the settings door's classification as one line (the
// "statusLine ledger line" of the report). It never writes and never repairs.
func statusLinePosture() (string, error) {
	door, err := integrations.StatusLinePlan("")
	if err != nil {
		var foreign *integrations.StatusLineForeignError
		if errors.As(err, &foreign) {
			// A decision, not a crash: the ledger refuses the document and
			// rewriting it would lose a field agy reads.
			return "repair decision needed: " + providers.MaskOperand(foreign.Detail), nil
		}
		var launcher *integrations.StatusLineLauncherError
		if errors.As(err, &launcher) {
			return "launcher missing (" + launcher.Shape + "); apply refuses until it exists", nil
		}
		return "unavailable: " + providers.MaskOperand(err.Error()), err
	}
	word := ""
	switch door.Action {
	case integrations.StatusLineActionCreate:
		word = "slot absent; apply adds it"
	case integrations.StatusLineActionUpdate:
		word = "slot present with a different object; apply replaces it"
	case integrations.StatusLineActionAdopt:
		word = "slot holds the door's object, unrecorded; apply records it"
	case integrations.StatusLineActionIdentical:
		word = "slot holds the door's object and is recorded; nothing to write"
	default:
		word = string(door.Action)
	}
	if door.Recorded {
		word += " (bookkeeping matches)"
	}
	return word, nil
}

// ---------------------------------------------------------------------------
// the composed write plan

// integrationPlan is ONE composed integrations write: the shared composition from
// internal/integrations (the ordered engine calls, their exact diff lines and the
// notes that say what is not a step) plus the state.json intent rows this door
// binds to it.
//
// Consent composition (stated once, in internal/integrations/compose.go, and
// obeyed here): the user approves ONE diff and answers ONE prompt, and then
// everything that diff described happens. The engine calls and the six state.json
// intent rows therefore run with NO further gate. That is safe because the
// CLI-level gates live at the doors, not in the verbs: integrations.Apply and
// Core.Set are ungated library calls, so calling them directly is exactly what a
// single consent means, and nesting a second confirmStateWrite per key row would
// ask the same user the same question up to seven times.
//
// The write-time re-verify is not skipped: every intent row is re-read through
// stateScalarPlan.reverify after consent, and every deploy plan re-verifies its
// destinations inside integrations.Apply (T4's stale-base rule). A moved base
// refuses the whole run before the first byte.
type integrationPlan struct {
	write  *integrations.Write
	intent []stateScalarPlan
}

// diff renders the one exact diff the consent gate shows: the composed write's
// lines, then the state.json rows the same consent covers.
func (p *integrationPlan) diff() []string {
	lines := p.write.Lines()
	if len(p.intent) > 0 {
		lines = append(lines, stateWriteAtomicSentence)
		for _, row := range p.intent {
			lines = append(lines, fmt.Sprintf("  %s: %s -> %s", row.displayKey(),
				stateValueDisplay(row.before), stateValueDisplay(row.after)))
		}
	}
	return lines
}

// writes reports whether the plan changes anything at all.
func (p *integrationPlan) writes() bool {
	return len(p.intent) > 0 || p.write.Writes()
}

// execute runs the plan after ONE consent: state re-verify, the engine calls in
// plan order, then the intent rows. It stops at the first failure and reports
// what already landed - and, when the failing step was the first one, that
// nothing did - because the manifest and state.json are two stores and a
// half-applied plan must not read as "nothing happened" (nor the reverse).
func (p *integrationPlan) execute(core *settings.Core, out, errw io.Writer) int {
	for _, row := range p.intent {
		if _, err := row.reverify("agy pro integrations " + p.write.Verb); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
			return 1
		}
	}
	for i, step := range p.write.Steps() {
		result, err := step.Run()
		if err != nil {
			fmt.Fprintln(errw, stepFailureRow(step.Feature, "failed", err))
			fmt.Fprintln(errw, stepFailureNotice(i, len(p.intent)))
			return 1
		}
		printStepResult(result, out)
	}
	for _, row := range p.intent {
		if err := core.Set(row.key, row.after); err != nil {
			fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
			return 1
		}
	}
	return 0
}

// stepFailureNotice is the transcript line after a step failed. At index 0
// NOTHING has landed: no step result was printed, no destination was written and
// the intent rows run after the whole loop, so the "already landed" sentence
// would state a fact that is false (its old guard fired on len(intent) > 0 for
// exactly that case). From index 1 on, the steps printed above really did land
// and the sentence is the honest one.
func stepFailureNotice(index, intentRows int) string {
	if index > 0 {
		return "[agy-pro] the steps printed above already landed; the steps after this one did not run"
	}
	if intentRows > 0 {
		return "[agy-pro] nothing was written; the remaining steps did not run and the state.json rows were not written"
	}
	return "[agy-pro] nothing was written; the remaining steps did not run"
}

// printStepResult prints this door's one-line [SUCCESS] report for one engine
// call. The summaries are the engine's; the wording is the door's, which is why the
// shared composition does not carry it.
//
// The settings.json rollback prints its own longer report (the skipped snapshots and
// the pre-restore archive have no place in one line); the case below is here so the
// switch stays total, not because a composed plan routes through it.
func printStepResult(result integrations.StepResult, out io.Writer) {
	switch {
	case result.Apply != nil:
		tail := ""
		if result.Apply.BackupDir != "" {
			tail = " (pre-change bytes archived in " + result.Apply.BackupDir + ")"
		}
		verb := "deployed"
		if result.Kind == integrations.StepAdopt {
			verb = "adopted"
		}
		fmt.Fprintf(out, "[SUCCESS] %s %s: %d created, %d updated, %d identical%s\n",
			result.Feature, verb, result.Apply.Created, result.Apply.Updated, result.Apply.Identical, tail)
	case result.Retract != nil:
		tail := ""
		if result.Retract.BackupDir != "" {
			tail = " into " + result.Retract.BackupDir
		}
		fmt.Fprintf(out, "[SUCCESS] %s retracted: %d destination(s)%s\n",
			result.Feature, len(result.Retract.Destinations), tail)
	case result.Door != nil:
		switch result.Kind {
		case integrations.StepDoorApply:
			fmt.Fprintf(out, "[SUCCESS] statusline %s: %s%s\n", result.Door.Action,
				providers.MaskOperand(result.Door.SettingsPath), settingsDoorTail(result.Door.Detail))
		case integrations.StepDoorRetract:
			fmt.Fprintf(out, "[SUCCESS] statusline retract: %s%s\n",
				providers.MaskOperand(result.Door.SettingsPath), settingsDoorTail(result.Door.Detail))
		case integrations.StepDoorAdopt:
			fmt.Fprintf(out, "[SUCCESS] statusline adopt: %s (provenance %s)%s\n",
				providers.MaskOperand(result.Door.SettingsPath), result.Door.Provenance, settingsDoorTail(result.Door.Detail))
		}
	case result.DoorRollback != nil:
		fmt.Fprintf(out, "[SUCCESS] settings.json restored from %s (%s chain, %s)\n",
			providers.MaskOperand(result.DoorRollback.Snapshot), result.DoorRollback.Scheme,
			result.DoorRollback.Taken.Format("2006-01-02 15:04:05"))
	}
}

// settingsDoorTail appends a door detail sentence when the summary carries one.
func settingsDoorTail(detail string) string {
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

// ---------------------------------------------------------------------------
// plan composition helpers

// addIntentRow appends one state.json intent row, skipping the rows whose value
// does not change so the diff is the exact set of differing keys and a second
// identical run presents nothing to write.
func (p *integrationPlan) addIntentRow(key, after string) error {
	row, err := snapshotStateScalar(key, after)
	if err != nil {
		return err
	}
	if !row.knownKey {
		return fmt.Errorf("%s is not a key this CLI writes", providers.MaskOperand(key))
	}
	if row.before == row.after {
		return nil
	}
	p.intent = append(p.intent, row)
	return nil
}

// ---------------------------------------------------------------------------
// apply / retract

// integrationTargets is the resolved operand set of one apply/retract verb.
type integrationTargets struct {
	// features is the resolved target set; enable is true for a deployment and
	// false for a retraction.
	features []integrations.Feature
	enable   bool
	// pending marks the operand-less apply, which records no new intent.
	pending bool
	// wholePosture marks a preset target: the verb names the whole feature set
	// rather than one feature. A whole-posture APPLY is the second implicit
	// target (T8 §2 rule 5): a statusline step that cannot be planned is
	// subtracted with a note, so a launcherless install still deploys
	// rules/skills/plugins/hook. The whole-posture RETRACT stays explicit
	// because its door step never hard-fails on a missing launcher.
	wholePosture bool
}

// resolveIntegrationTargets maps the operand list onto the feature set the verb
// touches. Feature words go through integrations.ParseFeature (the engine's
// alias table) and preset words through integrations.ParsePreset, so this door
// never owns a second vocabulary.
func resolveIntegrationTargets(verb string, operands []string) (integrationTargets, error) {
	targets := integrationTargets{enable: verb == "apply"}
	if len(operands) == 0 {
		if verb == "apply" {
			targets.pending = true
			return targets, nil
		}
		targets.features = integrations.AllFeatures()
		return targets, nil
	}
	if len(operands) > 1 {
		return targets, fmt.Errorf("one feature or preset per run (got %d operands)", len(operands))
	}
	word := operands[0]
	if feature, err := integrations.ParseFeature(word); err == nil {
		targets.features = []integrations.Feature{feature}
		return targets, nil
	}
	preset, err := integrations.ParsePreset(word)
	if err != nil {
		return targets, err
	}
	if preset == integrations.PresetCustom {
		return targets, errors.New("custom is the composed shape of a mixed toggle set, so it is derived, never applied; apply or retract one feature instead")
	}
	targets.features = integrations.AllFeatures()
	targets.enable = targets.enable && preset == integrations.PresetTools
	targets.wholePosture = true
	return targets, nil
}

// integrationsWrite runs `apply` and `retract`.
func integrationsWrite(verb string, operands []string, in io.Reader, out, errw io.Writer, yes bool) int {
	targets, err := resolveIntegrationTargets(verb, operands)
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n%s\n", transcriptError(err), proIntegrationsUsage)
		return 2
	}
	st, err := loadIntentState()
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	if targets.pending {
		pending, perr := pendingFeatures(st)
		if perr != nil {
			fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(perr))
			return 1
		}
		if len(pending) == 0 {
			fmt.Fprintln(out, "Nothing is pending: no feature carries an `on` intent and nothing is deployed.")
			fmt.Fprintln(out, "Pick a target explicitly, for example: agy pro integrations apply tools")
			return 0
		}
		targets.features = pending
	}
	plan, err := composeIntegrationPlan(verb, targets, st)
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	return runComposedIntegrationPlan(plan, in, out, errw, yes)
}

// pendingFeatures is the operand-less apply set. The rule itself lives in
// internal/integrations (PendingFeatures / AnythingRecorded, shared with the
// screen); what this door adds is the legacy fallback: on a machine that records
// no intent at all, every feature the engine's manifest already claims, so a
// pre-intent install can be reconciled without inventing intent. A feature whose
// intent is `off` is never pending.
func pendingFeatures(st *mode.State) ([]integrations.Feature, error) {
	reader := intentReader(st)
	if integrations.AnythingRecorded(reader) {
		return integrations.PendingFeatures(reader), nil
	}
	var pending []integrations.Feature
	for _, feature := range integrations.AllFeatures() {
		if feature == integrations.FeatureStatusline {
			continue
		}
		status, err := integrations.Status(feature)
		if err != nil {
			return nil, err
		}
		if status != nil && status.Deployed {
			pending = append(pending, feature)
		}
	}
	return pending, nil
}

// intentReader binds the state layer's stored words to the engine's intent enum.
// The binding lives at the door because the spelling is state.json's (internal/mode
// owns it, and internal/integrations cannot import it; see the engine's package
// comment).
func intentReader(st *mode.State) integrations.IntentReader {
	return func(feature integrations.Feature) integrations.Intent {
		switch intentToggle(st, feature) {
		case mode.IntegrationToggleOn:
			return integrations.IntentOn
		case mode.IntegrationToggleOff:
			return integrations.IntentOff
		}
		return integrations.IntentUnset
	}
}

// intentModeWord renders one engine intent as the word state.json stores.
func intentModeWord(intent integrations.Intent) string {
	switch intent {
	case integrations.IntentOn:
		return mode.IntegrationToggleOn
	case integrations.IntentOff:
		return mode.IntegrationToggleOff
	}
	return ""
}

// composeIntegrationPlan builds the one plan the gate presents: the shared
// composition (steps, lines, notes) plus this door's intent rows.
func composeIntegrationPlan(verb string, targets integrationTargets, st *mode.State) (*integrationPlan, error) {
	write, facts, err := integrations.ComposeFeatureWrite(integrations.FeatureRequest{
		Verb:     verb,
		Features: targets.features,
		Enable:   targets.enable,
		Content:  profiles.GetToolsFS(),
		Intent:   intentReader(st),
		// A pending apply reconciles what the intent already says; it records no
		// intent the user never stated.
		RecordIntent: !targets.pending,
		// A gate is never presented for a write that cannot happen: a retract with
		// nothing recorded is a note (T8 §2 rule 4). The screen keeps the step so
		// the feature is named in the diff it approves.
		EmptyRetractIsNote: true,
		// Two requests are implicit (T8 §2 rule 5), so an unplannable settings door
		// degrades to a note instead of blocking the content features: the pending
		// apply, and a whole-posture APPLY (a preset deploy - the launcherless
		// install still deploys rules/skills/plugins/hook). A named feature stays
		// explicit, and so does a whole-posture retract, whose door step never
		// hard-fails on a missing launcher.
		DoorExplicit: !targets.pending && !(targets.wholePosture && targets.enable),
	})
	if err != nil {
		return nil, err
	}
	plan := &integrationPlan{write: write}
	for _, row := range facts.Features {
		if err := plan.addIntentRow(intentToggleKey(row.Feature), intentModeWord(row.After)); err != nil {
			return nil, err
		}
	}
	if facts.Preset != "" {
		if err := plan.addIntentRow("integration-preset", string(facts.Preset)); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// runComposedIntegrationPlan presents the plan once and executes it after one
// consent. A plan that changes nothing is reported without a gate: there is no
// write to approve. Both transcript arms neutralise their lines with
// stripTerminalControl (through transcriptRow, the same rendering the rollback and
// repair arms use): the "nothing to write" report is the only diff a user sees on
// that path, and a step line can embed a path or a field name read out of the live
// config (FIX-12, widened in FIX-14).
func runComposedIntegrationPlan(plan *integrationPlan, in io.Reader, out, errw io.Writer, yes bool) int {
	if !plan.writes() {
		fmt.Fprintln(out, "Nothing to write:")
		for _, line := range plan.diff() {
			fmt.Fprintln(out, transcriptRow(line))
		}
		return 0
	}
	proceed, code := confirmIntegrationsWrite(in, out, errw, plan.diff(), yes)
	if !proceed {
		return code
	}
	return plan.execute(newSettingsCore(), out, errw)
}

// ---------------------------------------------------------------------------
// preset

// integrationsPreset runs `preset native|tools`: the whole posture plus its
// recorded row. `custom` is refused because it is derived, never applied.
func integrationsPreset(operands []string, in io.Reader, out, errw io.Writer, yes bool) int {
	if len(operands) != 1 {
		fmt.Fprintf(errw, "[agy-pro] preset requires <native|tools>\n%s\n", proIntegrationsUsage)
		return 2
	}
	preset, err := integrations.ParsePreset(operands[0])
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 2
	}
	if preset == integrations.PresetCustom {
		fmt.Fprintln(errw, "[agy-pro] custom is the composed shape of a mixed toggle set, so it is derived, never applied; apply or retract one feature instead")
		return 2
	}
	targets := integrationTargets{
		features:     integrations.AllFeatures(),
		enable:       preset == integrations.PresetTools,
		wholePosture: true,
	}
	st, err := loadIntentState()
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	plan, err := composeIntegrationPlan("preset", targets, st)
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	return runComposedIntegrationPlan(plan, in, out, errw, yes)
}

// ---------------------------------------------------------------------------
// adopt

// integrationsAdopt runs the record-only adopt: the settings door's slot and
// every content feature whose destinations already hold the engine's bytes. A
// destination that differs is never adopted (that is an apply).
func integrationsAdopt(in io.Reader, out, errw io.Writer, yes bool) int {
	write, err := integrations.ComposeAdopt(profiles.GetToolsFS())
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	return runComposedIntegrationPlan(&integrationPlan{write: write}, in, out, errw, yes)
}

// ---------------------------------------------------------------------------
// rollback

// integrationsRollbackVerb dispatches the rollback operand grammar.
//
//	rollback              restore the newest settings.json snapshot (unchanged)
//	rollback <feature>    restore that content feature's pre-install deploy tree
//
// `statusline` is a feature word, but the settings door is the only thing that can
// put settings.json back, so the refusal names that primitive instead of letting
// the content engine refuse it. A preset word is refused: a posture has no single
// pre-install tree.
func integrationsRollbackVerb(operands []string, in io.Reader, out, errw io.Writer, yes bool) int {
	if len(operands) == 0 {
		return integrationsRollback(in, out, errw, yes)
	}
	if len(operands) > 1 {
		return integrationsUsageError(errw, operands[1])
	}
	word := operands[0]
	if feature, err := integrations.ParseFeature(word); err == nil {
		if feature == integrations.FeatureStatusline {
			fmt.Fprintln(errw, "[agy-pro] statusline is the settings.json door: `agy pro integrations rollback` (no operand) restores settings.json from its snapshot chain")
			return 2
		}
		return integrationsRollbackTree(feature, in, out, errw, yes)
	}
	if preset, err := integrations.ParsePreset(word); err == nil {
		if preset == integrations.PresetCustom {
			fmt.Fprintln(errw, "[agy-pro] custom is the composed shape of a mixed toggle set, so it is derived, never rolled back; roll back one feature instead")
			return 2
		}
		fmt.Fprintf(errw, "[agy-pro] rollback takes one feature or the settings door (no operand); %q names a whole posture, which has no single pre-install tree\n", string(preset))
		fmt.Fprintln(errw, "  Roll back the features one at a time, or use `agy pro integrations retract` / `apply` for the whole posture.")
		return 2
	}
	return integrationsUsageError(errw, word)
}

// integrationsRollbackTree restores one content feature's deploy tree through the
// engine verb. A feature with nothing recorded (never applied, already retracted, or
// every destination a named skip) is a named empty state printed without a gate:
// there is no write to approve. The verb writes NO intent row: a rollback returns a
// tree to its pre-install bytes, which is not a posture the user stated.
func integrationsRollbackTree(feature integrations.Feature, in io.Reader, out, errw io.Writer, yes bool) int {
	write, err := integrations.ComposeFeatureRollback(feature)
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	if len(write.Steps()) == 0 {
		fmt.Fprintln(out, "Nothing to roll back:")
		for _, line := range write.Lines() {
			fmt.Fprintln(out, transcriptRow(line))
		}
		return 0
	}
	proceed, code := confirmIntegrationsWrite(in, out, errw, write.Lines(), yes)
	if !proceed {
		return code
	}
	for _, step := range write.Steps() {
		result, err := step.Run()
		if err != nil {
			fmt.Fprintln(errw, stepFailureRow(step.Feature, "rollback failed", err))
			return 1
		}
		printRollbackReport(result, out)
	}
	return 0
}

// printRollbackReport prints one feature's rollback result: the counts, the archive
// the replaced bytes went to, and every skip with its engine reason word, so a
// partly-skipped rollback never reads as a clean one.
func printRollbackReport(result integrations.StepResult, out io.Writer) {
	summary := result.Rollback
	if summary == nil {
		return
	}
	fmt.Fprintf(out, "[SUCCESS] %s rolled back: %d restored, %d removed, %d skipped\n",
		summary.Feature, summary.Restored(), summary.Removed(), summary.Skipped())
	if summary.BackupDir != "" {
		fmt.Fprintf(out, "          the bytes it replaced are archived in %s\n", providers.MaskOperand(summary.BackupDir))
	}
	for _, destination := range summary.Destinations {
		if destination.Action != integrations.RollbackActionSkip {
			continue
		}
		fmt.Fprintf(out, "          skipped (%s): %s\n", destination.SkipReason, providers.MaskOperand(destination.Path))
	}
	if summary.Restored() > 0 {
		fmt.Fprintln(out, "          a restored destination is no longer recorded by this engine; `agy pro integrations adopt` records the restored bytes")
	}
}

// integrationsRollback restores the newest coherent settings.json snapshot. A
// machine with no snapshot at all is a named empty state, not a crash and not a
// gate: there is no write to approve.
func integrationsRollback(in io.Reader, out, errw io.Writer, yes bool) int {
	write, err := integrations.ComposeSettingsRollback()
	if errors.Is(err, integrations.ErrNoSettingsSnapshot) {
		path, pathErr := agysettings.GlobalSettingsPath()
		if pathErr != nil {
			fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(pathErr))
			return 1
		}
		fmt.Fprintf(out, "No settings.json snapshot to roll back to: neither settings.bak.<ts>.json (node) nor\nsettings.json.bak.<ts> (agy-pro) sits beside %s\n", providers.MaskOperand(path))
		fmt.Fprintln(out, "Nothing was written. A content feature's deploy tree is restored by")
		fmt.Fprintln(out, "`agy pro integrations rollback <feature>` (rules, plugins, skills, hook).")
		return 0
	}
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	proceed, code := confirmIntegrationsWrite(in, out, errw, write.Lines(), yes)
	if !proceed {
		return code
	}
	result, err := write.Steps()[0].Run()
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %s\n", transcriptError(err))
		return 1
	}
	summary := result.DoorRollback
	if summary == nil {
		fmt.Fprintln(errw, "[agy-pro] the settings rollback reported no result")
		return 1
	}
	printStepResult(result, out)
	for _, skipped := range summary.Skipped {
		fmt.Fprintf(out, "          skipped (not valid JSON): %s\n", providers.MaskOperand(skipped))
	}
	if summary.PreRestoreArchive != "" {
		fmt.Fprintf(out, "          the bytes it replaced are archived in %s\n", providers.MaskOperand(summary.PreRestoreArchive))
	}
	return 0
}

// ---------------------------------------------------------------------------
// repair

// integrationsRepair reconciles drift: a feature the manifest already claims whose
// destinations are drifted or missing is re-planned and re-applied (T4 handoff
// note 4: Plan+Apply IS the repair). Foreign files are never touched, and a
// feature with nothing recorded is not invented. The settings door is repaired
// the same way, through the same plan.
//
// The "nothing to repair" arm prints without a gate and its notes are engine
// sentences that can embed the settings ledger's field-name text, so every row goes
// through transcriptRow (FIX-14).
func integrationsRepair(in io.Reader, out, errw io.Writer, yes bool) int {
	write := integrations.ComposeRepair(profiles.GetToolsFS())
	if len(write.Steps()) == 0 {
		fmt.Fprintln(out, "Nothing to repair:")
		for _, note := range write.Notes() {
			fmt.Fprintln(out, transcriptRow(note))
		}
		return 0
	}
	return runComposedIntegrationPlan(&integrationPlan{write: write}, in, out, errw, yes)
}
