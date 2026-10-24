// Package codework is the engine-owned half of the `agy pro code` door: it
// turns a decomposition into a fanout partition plan (plan.go) and runs the
// verification command the engine itself trusts (verify.go). The door composes
// the SHIPPED partition write mode (`agy pro fanout --units`, per-unit
// `"write": true`) instead of adding a second write capability, so this package
// authors plans for that mode and never executes a seat.
//
// Contract invariants:
//
//   - DETERMINISTIC. The same inputs produce byte-identical output: no
//     timestamps, no randomness, no map iteration order in the emitted JSON.
//     A plan is auditable by regenerating it.
//   - PARSER-AS-VALIDATOR. internal/fanout owns the plan schema and its rules,
//     and ParsePlan (units.go:56) is the only authority for them. BuildPlan
//     round-trips its own output through fanout.ParsePlan, so a plan this
//     package emits is one the `--units` CLI accepts BY CONSTRUCTION. Nothing
//     here re-implements scope disjunctness, root-wide rejection or the unit
//     ceiling.
//   - FAIL CLOSED. No scope is ever invented: a unit without a bounded scope
//     would be an unrestricted write seat, so it is refused instead of
//     defaulted. A native seat is refused as a writer. A governance/credential
//     path in a scope is refused BEFORE the plan is authored.
//   - EVERY ECHOED OPERAND IS MASKED. Plan-authored fields (request, title,
//     scope entry, seat token) can carry a pasted credential and they are
//     quoted in errors; every one of them passes providers.MaskOperand /
//     providers.MaskSentence (providers/keyaudit.go:144, :818) before it
//     reaches an error string.
//
// Governance mirror (layer 1 of 2). This package refuses a governance path in a
// SCOPE (verb level, before any run is authored); the seat write tools refuse
// the same names at write time (layer 2, agenttools/guard.go:134
// governanceRefusal). The denylist below is copied field-for-field from
// internal/agenttools/guard.go:89-119 because that set and its sibling-prefix
// arm are UNEXPORTED; internal/codesnap sets the same precedent for
// authority-cited mirrors, and plan_test.go pins the two sets for parity.
package codework

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/fanout"
	"agypro/internal/providers"
)

const (
	// unitIDStem is the deterministic id stem ("u1", "u2", ...): the plan's
	// unit ids are positional, so a regenerated plan diffs cleanly.
	unitIDStem = "u"

	// contractHeading / contractLines are the fixed write contract every
	// authored unit carries. They are constant text (no per-unit
	// interpolation) so the unit bodies share a common prefix, and they are
	// the only lines standing between a provider seat and an unbounded edit.
	contractHeading = "## Contract (fixed)"
	contractLines   = "- produce a minimal diff\n" +
		"- only edit files inside your scope\n" +
		"- end your answer with a `## Changed Files` list\n"

	// repairHeading / repairInstruction are the fixed repair additions: the
	// heading names the bounded excerpt that follows it, the instruction is
	// what turns "verification failed" into an edit rule.
	repairHeading     = "## Verification failed (bounded output)"
	repairInstruction = "Fix so verification passes; only touch files in your scope."

	// repairTailBytes bounds the verification excerpt carried into a repair
	// unit body. A compiler/test log is unbounded and a provider seat pays for
	// every byte of its prompt, so the tail (the part that actually names the
	// failure) is kept and the rest is dropped with an explicit marker.
	repairTailBytes = 2048

	// defaultTitleMaxRunes bounds the title derived from Request for the
	// single-spec case, so a pasted wall of text cannot become a plan title.
	defaultTitleMaxRunes = 80

	// deliverableFormat is the synthesized deliverable. fanout.ParsePlan
	// requires a non-empty deliverable and UnitSpec deliberately carries no
	// deliverable field, so it is derived from the title deterministically.
	deliverableFormat = "minimal diff implementing: %s"
)

// UnitSpec is one disjoint slice of work as the caller describes it: what
// (Title), where (Scope), who (Seat) and the task text (Body). It maps onto
// fanout.Unit (units.go:26-34) minus the fields this package owns:
//
//   - ID is positional ("u1".."uN"), assigned here, never caller-supplied:
//     a caller-chosen id can collide or carry a key-shaped string into the
//     plan and its error echoes.
//   - Deliverable is derived from Title (deliverableFormat): the schema
//     requires one, and a second free-text field per unit buys nothing the
//     title does not already say.
//   - Write is set by BuildPlan, never by the caller - every authored unit is
//     a write unit, which is the entire point of the door.
type UnitSpec struct {
	Title string   // required unless a single spec: then Title falls back to Request
	Scope []string // required, at least one bounded entry (globs allowed, root-wide refused)
	Seat  string   // required, exactly one PROVIDER seat spec (native is refused)
	Body  string   // optional task text prepended to the fixed contract lines
}

// BuildPlan synthesizes a fanout partition plan JSON document (the top-level
// array of unit objects that `agy pro fanout --units <file>` reads) from the
// caller's decomposition.
//
// request is the run's overall goal. It is used in exactly two places: as the
// fallback title of a single-spec plan whose Title is empty (bounded to
// defaultTitleMaxRunes) and as a redaction seed for every error this function
// returns. It is NOT injected into unit bodies: fanout partition mode sends
// each seat only its own unit body, and a shared request line would duplicate
// the whole goal into N prompts.
//
// One spec yields a one-unit plan (the single-unit default); N specs yield N
// units. Every returned plan has passed fanout.ParsePlan, so:
//
//   - scope entries are non-empty and bounded (root-wide "." / "/" and
//     bare-wildcard "*" / "**" entries are refused, units.go:128-138);
//   - the unit scopes are provably disjoint (units.go:165-182);
//   - the plan stays under the 64-unit ceiling (units.go:42).
//
// Beyond the parser's rules, this function refuses:
//
//   - a spec with no scope (an invented scope is an unrestricted seat);
//   - a scope entry that names a governance/credential component at any depth
//     (governanceScopeRefusal, mirroring agenttools/guard.go:134);
//   - a Seat that is empty, not exactly one token, or a native seat
//     (validateSeat).
//
// Errors are fail-closed and name the offending unit and operand, masked.
func BuildPlan(request string, specs []UnitSpec) ([]byte, error) {
	if len(specs) == 0 {
		return nil, planError(request, errors.New("codework: plan: at least one unit spec is required (a plan with no units, or a unit with no bounded scope invented here, would be an unrestricted write seat)"))
	}

	units := make([]fanout.Unit, 0, len(specs))
	for i, spec := range specs {
		unit, err := buildUnit(request, i, spec, len(specs))
		if err != nil {
			return nil, planError(request, err)
		}
		units = append(units, unit)
	}

	data, err := marshalUnits(units)
	if err != nil {
		return nil, planError(request, err)
	}
	// Parser-as-validator: the emitted bytes are the bytes the `--units` file
	// reader hands to ParsePlan (fanout/cli.go:573), so the check here is the
	// CLI's own check, not a second opinion.
	if _, err := fanout.ParsePlan(data); err != nil {
		return nil, planError(request, fmt.Errorf("codework: plan: the generated plan was refused by the partition parser: %w", err))
	}
	return data, nil
}

// buildUnit renders one UnitSpec as a fanout.Unit, refusing the shapes the
// parser cannot see (governance scope, native or ambiguous seat, a scope that
// was never provided).
func buildUnit(request string, idx int, spec UnitSpec, nSpecs int) (fanout.Unit, error) {
	title := strings.TrimSpace(spec.Title)
	if title == "" {
		// Single-spec default: the goal IS the title. With N specs there is
		// no honest fallback - naming two units after one request would make
		// the plan lie about which slice each seat owns.
		if nSpecs == 1 {
			title = titleFromRequest(request)
		}
		if title == "" {
			return fanout.Unit{}, fmt.Errorf("codework: plan: unit %d: missing title (and no request text to derive one from)", idx+1)
		}
	}

	scope, err := buildScope(idx, spec.Scope)
	if err != nil {
		return fanout.Unit{}, err
	}
	if err := validateSeat(idx, spec.Seat); err != nil {
		return fanout.Unit{}, err
	}

	return fanout.Unit{
		ID:          fmt.Sprintf("%s%d", unitIDStem, idx+1),
		Title:       title,
		Scope:       scope,
		Deliverable: fmt.Sprintf(deliverableFormat, title),
		Seat:        strings.TrimSpace(spec.Seat),
		Body:        composeBody(title, spec.Body),
		// Every authored unit is a write unit. fanout.Unit.Write is the
		// per-unit opt-in that lets a seat call write_file/replace_in_file
		// (units.go:33); the CLI's per-unit scope filter is what bounds it.
		Write: true,
	}, nil
}

// buildScope trims the scope entries and refuses a governance/credential
// target. Root-wide and bare-wildcard entries are left to fanout.ParsePlan
// (units.go:128-138) so that rule exists once, in the parser.
//
// FIX-A1 (S2) adds the second refusal: a component whose WRITTEN form is not its
// Win32 canonical form (a trailing dot or space, a reserved DOS device name, a
// component Win32 strips to nothing) as well as a governance name written in one
// of those forms ("AGENTS.md."). The write-time guard refuses the same shapes
// (agenttools/guard.go canonicalWriteRefusal), so a plan that carried one would
// author a unit whose scope can never be honored.
func buildScope(idx int, entries []string) ([]string, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("codework: plan: unit %d: missing scope (a write unit must carry at least one bounded scope entry)", idx+1)
	}
	scope := make([]string, 0, len(entries))
	for j, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, fmt.Errorf("codework: plan: unit %d: scope[%d]: empty scope entry", idx+1, j)
		}
		if comp, why, refuse := governanceScopeRefusal(entry); refuse {
			if why == "" {
				return nil, fmt.Errorf("codework: plan: unit %d: scope[%d]: %q is a governance/credential path (%s) that configures the agent system itself; the code door refuses to author a write unit over it (agenttools/guard.go:134 refuses the same component at write time)", idx+1, j, providers.MaskOperand(entry), providers.MaskOperand(comp))
			}
			// The stream arm gets the class name instead of the generic shape
			// sentence (N8(g)): a colon-shaped scope entry is refused by its own
			// named rule here, before any plan, snapshot or seat exists, so the
			// operator never learns about it from a downstream "outside unit scope"
			// message.
			if why == scopeStreamReason {
				return nil, fmt.Errorf("codework: plan: unit %d: scope[%d]: %q is refused: %s (component %q %s); the code door refuses to author a write unit over it (agenttools/guard.go refuses the same component at write time)", idx+1, j, providers.MaskOperand(entry), scopeStreamClass, providers.MaskOperand(comp), scopeStreamDetail)
			}
			return nil, fmt.Errorf("codework: plan: unit %d: scope[%d]: %q path component %q %s, so the scope would not bound the path the write lands on (agenttools/guard.go refuses the same component at write time)", idx+1, j, providers.MaskOperand(entry), providers.MaskOperand(comp), why)
		}
		scope = append(scope, entry)
	}
	return scope, nil
}

// validateSeat enforces the seat rule the engine cannot enforce for us: a write
// unit is worked by a PROVIDER seat, exactly one.
//
// Grammar is fanout's own, via the exported ParseSeatSpecs (seatspec.go:53:
// "native:<model-id>" | a provider spec such as "provider/model"). Native seats
// are refused because the engine cannot bound them: a native seat is a real agy
// subprocess that never builds a ToolRunner, so it holds no scope filter at all
// (internal/fanout/engine.go:883-889) - the whole containment story of an
// authored plan rests on the per-unit ToolRunner scope. An empty Seat is
// refused rather than defaulted for the same reason: the run default may be a
// native seat.
func validateSeat(idx int, seat string) error {
	s := strings.TrimSpace(seat)
	if s == "" {
		return fmt.Errorf("codework: plan: unit %d: missing seat (a write unit must name exactly one provider seat; an empty seat falls back to the run default, which may be a native seat that the engine cannot scope)", idx+1)
	}
	specs, err := fanout.ParseSeatSpecs(s)
	if err != nil {
		return fmt.Errorf("codework: plan: unit %d: seat %q is not a valid seat spec: %w", idx+1, providers.MaskOperand(s), err)
	}
	if len(specs) != 1 {
		return fmt.Errorf("codework: plan: unit %d: seat %q names %d seats; a write unit is worked by exactly one provider seat", idx+1, providers.MaskOperand(s), len(specs))
	}
	if specs[0].Kind != fanout.SeatKindProvider {
		return fmt.Errorf("codework: plan: unit %d: seat %q is a native seat; write units are provider-only (a native seat is unscopable by engine design, internal/fanout/engine.go:883-889, so it cannot be bounded to a unit scope)", idx+1, providers.MaskOperand(s))
	}
	return nil
}

// composeBody builds the unit body: the caller's task text followed by the
// fixed contract lines. An empty Body still yields a body (the title plus the
// contract) so a unit is never bodyless - fanout.UnitBody would otherwise
// synthesize one that says nothing about the write contract.
func composeBody(title, body string) string {
	var b strings.Builder
	if text := strings.TrimSpace(body); text != "" {
		b.WriteString(text)
	} else {
		b.WriteString("Unit: ")
		b.WriteString(title)
	}
	b.WriteString("\n\n")
	b.WriteString(contractHeading)
	b.WriteString("\n")
	b.WriteString(contractLines)
	return b.String()
}

// titleFromRequest derives a bounded one-line title from the request text.
func titleFromRequest(request string) string {
	s := strings.TrimSpace(request)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > defaultTitleMaxRunes {
		s = strings.TrimSpace(string(r[:defaultTitleMaxRunes]))
	}
	return s
}

// BuildRepairPlan carries a failed verification back into the plan that failed:
// it returns the same units with the bounded, redacted verification tail
// appended to the body of every unit named in failedUnitIDs. The other units
// keep their bodies unchanged, so the repaired plan is the SAME plan (same ids,
// same scopes, same order - a diff of the two files shows exactly what the
// engine added) and its merge artifact still covers every unit of the run.
//
// failedUnitIDs must name units of orig: an unknown id is refused instead of
// ignored, because a repair that silently targets nothing would burn a whole
// provider run reproducing the failure. Duplicates are allowed and collapse.
//
// verifyTail is the verification excerpt (RunVerify's OutputTail). It is
// redacted with providers.NewRedactor BEFORE it is bounded, so a truncated
// pattern cannot survive as a fragment, and bounded to repairTailBytes so one
// log dump cannot blow up a seat prompt.
func BuildRepairPlan(orig []byte, failedUnitIDs []string, verifyTail string) ([]byte, error) {
	units, err := fanout.ParsePlan(orig)
	if err != nil {
		return nil, repairError(fmt.Errorf("codework: repair: the plan under repair is not a valid partition plan: %w", err))
	}
	if len(units) == 0 {
		return nil, repairError(errors.New("codework: repair: the plan under repair has no units"))
	}

	failed, err := repairTargets(units, failedUnitIDs)
	if err != nil {
		return nil, repairError(err)
	}

	block := "\n\n" + repairHeading + "\n" + boundedRedact(verifyTail, repairTailBytes) + "\n" + repairInstruction + "\n"
	for i := range units {
		// Materialize the effective body (fanout.UnitBody, units.go:645) so the
		// repair plan carries its own text instead of depending on the engine's
		// synthesis for the units it did not touch.
		body := fanout.UnitBody(units[i])
		if failed[strings.TrimSpace(units[i].ID)] {
			body += block
		}
		units[i].Body = body
	}

	data, err := marshalUnits(units)
	if err != nil {
		return nil, repairError(err)
	}
	if _, err := fanout.ParsePlan(data); err != nil {
		return nil, repairError(fmt.Errorf("codework: repair: the generated repair plan was refused by the partition parser: %w", err))
	}
	return data, nil
}

// repairTargets validates failedUnitIDs against the plan and returns the
// trimmed id set to annotate.
func repairTargets(units []fanout.Unit, failedUnitIDs []string) (map[string]bool, error) {
	known := make(map[string]bool, len(units))
	for _, u := range units {
		known[strings.TrimSpace(u.ID)] = true
	}
	if len(failedUnitIDs) == 0 {
		return nil, errors.New("codework: repair: no failed unit ids (nothing to repair; an unannotated re-run would reproduce the same failure)")
	}
	failed := make(map[string]bool, len(failedUnitIDs))
	for _, raw := range failedUnitIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if !known[id] {
			return nil, fmt.Errorf("codework: repair: unit id %q is not in the plan under repair", providers.MaskOperand(id))
		}
		failed[id] = true
	}
	if len(failed) == 0 {
		return nil, errors.New("codework: repair: no failed unit ids (every given id was blank)")
	}
	return failed, nil
}

// marshalUnits renders units as the plan document: indented (a plan is read by
// humans and diffed between a run and its repair) with a trailing newline.
func marshalUnits(units []fanout.Unit) ([]byte, error) {
	data, err := json.MarshalIndent(units, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("codework: plan: cannot marshal the units plan: %w", err)
	}
	return append(data, '\n'), nil
}

// boundedRedact redacts then bounds a log excerpt: providers.NewRedactor
// (providers.go:915) applies the process-wide credential patterns first, so no
// pattern-shaped secret is ever truncated into a half-match, and the tail is
// then cut to the last maxBytes bytes on a rune boundary. An omitted prefix is
// announced, so a reader of the body knows the excerpt is partial.
func boundedRedact(s string, maxBytes int) string {
	redacted := providers.NewRedactor()(s)
	if len(redacted) <= maxBytes {
		return redacted
	}
	start := len(redacted) - maxBytes
	for start < len(redacted) && redacted[start]&0xC0 == 0x80 {
		start++ // do not split a UTF-8 sequence
	}
	return fmt.Sprintf("[... %d bytes omitted ...]\n", start) + redacted[start:]
}

// planError masks a plan-synthesis error: request is a redaction seed because
// the request text is model/user input that can carry a pasted credential and
// the message may echo a derived title.
func planError(request string, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(providers.MaskSentence(err.Error(), request))
}

// repairError masks a repair-plan error. The operands it quotes (unit ids) are
// masked individually where they are formatted; this applies the generic
// credential patterns to the finished sentence.
func repairError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(providers.NewRedactor()(err.Error()))
}

// governanceScopeNames mirrors agenttools.governanceWriteNames
// (internal/agenttools/guard.go:89-103) literally: the governance and
// credential chain - the files that configure the agent system itself. Keys are
// lowercase and compared case-insensitively. Kept in sync by
// TestGovernanceScopeSetMatchesGuard in plan_test.go; source of truth is the
// guard, which is the layer that refuses the actual write.
var governanceScopeNames = map[string]bool{
	"agents.md":           true,
	"gemini.md":           true,
	"claude.md":           true,
	"providers.json":      true,
	"state.json":          true,
	"state-fanout.json":   true,
	"fanout-presets.json": true,
	"integrations.json":   true,
	"settings.json":       true,
	// directory components
	"rules":    true,
	".agy-pro": true,
	".gemini":  true,
}

// governanceSiblingPrefix mirrors agenttools.governanceSiblingPrefix
// (guard.go:109): "providers.json.bak", "providers.json.tmp" and friends are
// the same escalation step as the live registry.
const governanceSiblingPrefix = "providers.json."

// governanceDocSuffixes mirrors agenttools.governanceDocSuffixes
// (guard.go:115): documentation ABOUT the registry ("docs/providers.json.md")
// stays writable, so the prefix arm keeps the same carve-out. Without it the
// mirror would refuse a scope the engine's own guard allows, and a look-alike
// doc path would become unauthorable by the door.
var governanceDocSuffixes = map[string]bool{
	"md": true, "markdown": true, "txt": true,
	"rst": true, "adoc": true, "asciidoc": true,
	"html": true, "htm": true,
}

// governanceScopeRefusal reports whether a scope entry addresses a
// governance/credential target, and names the matching component. It is the
// layer-1 mirror of agenttools.governanceRefusal (guard.go:134-150): the entry
// is normalized the way the scope matcher normalizes it (units.go:595
// normalizeScopeEntry: forward slashes, path.Clean, no leading "/") and then
// compared COMPONENT-EXACT over the component's WIN32 CANONICAL form and
// case-insensitively at any depth, so "sub/.gemini/settings.json" is refused
// while names that merely contain a denylisted substring ("docs/agents-notes.md")
// stay authorable.
//
// FIX-A1 (S2) widened it in two ways, both mirroring the write-time guard:
//
//   - the comparison runs on the canonical component (trailing dots and spaces
//     removed), so "AGENTS.md." and "agents.MD " are the same governed target as
//     "agents.md" - Win32 opens all three files as "agents.md";
//   - a component that is not in canonical form at all (trailing dot or space, a
//     reserved DOS device name, a component Win32 strips to nothing) is refused
//     with a NON-EMPTY reason even when no denylisted name matches, because such
//     a scope would bound a name the write never uses.
//
// The return is (component, reason, refuse): reason == "" means the component is
// a governed NAME, a non-empty reason means the component is not the shape the
// guard can bound.
//
// FIX-D1a mirrors the three write-time arms:
//
//   - the compared key is the FOLDED STREAM BASE (win32FoldName + streamBase), so
//     "ſtate.json" and "AGENTS.md:evil" are the same governed names the write
//     guard refuses on the host that folds/resolves them;
//   - a component carrying the Win32 stream separator is refused by shape
//     (scopeComponentShape) on Windows;
//   - an 8.3 alias shape on ANY component is refused by shape (R3-3), because the
//     write side creates alias-named intermediate directories through the alias.
//
// Glob components are compared literally: "*" does not match "agents.md" here.
// That asymmetry is deliberate - this is a lexical pre-check over plan text, and
// the authoritative test is the write-time guard against the resolved path.
// Refusing every scope a glob could expand onto would refuse every plan.
func governanceScopeRefusal(entry string) (string, string, bool) {
	for _, comp := range strings.Split(normalizeScopeText(entry), "/") {
		if comp == "" {
			continue
		}
		if why := scopeComponentShape(comp); why != "" {
			return comp, why, true
		}
		lower := win32FoldName(streamBase(canonicalScopeComponent(comp)))
		if governanceScopeNames[lower] {
			return comp, "", true
		}
		if strings.HasPrefix(lower, governanceSiblingPrefix) {
			if !governanceDocSuffixes[lower[len(governanceSiblingPrefix):]] {
				return comp, "", true
			}
		}
	}
	return "", "", false
}

// canonicalTrimSet mirrors agenttools.canonicalTrimSet (guard.go, the FIX-A1
// section): the characters Win32 strips from the end of every path component
// before it opens the file. Kept in sync by TestGovernanceScopeSetMatchesGuard.
const canonicalTrimSet = ". "

// reservedDeviceNames mirrors agenttools.reservedDeviceNames (guard.go, the
// FIX-A1 section): the DOS device names Win32 resolves in every directory and
// with any extension. Kept in sync by TestGovernanceScopeSetMatchesGuard.
var reservedDeviceNames = map[string]bool{
	"con":  true,
	"prn":  true,
	"aux":  true,
	"nul":  true,
	"com1": true,
	"com2": true,
	"com3": true,
	"com4": true,
	"com5": true,
	"com6": true,
	"com7": true,
	"com8": true,
	"com9": true,
	"lpt1": true,
	"lpt2": true,
	"lpt3": true,
	"lpt4": true,
	"lpt5": true,
	"lpt6": true,
	"lpt7": true,
	"lpt8": true,
	"lpt9": true,
}

// canonicalScopeComponent mirrors agenttools.canonicalWriteComponent: the Win32
// canonical form of one component. The literal "." and ".." are returned
// unchanged so a traversal entry stays visible as a traversal.
func canonicalScopeComponent(comp string) string {
	if comp == "." || comp == ".." {
		return comp
	}
	return strings.TrimRight(comp, canonicalTrimSet)
}

// deviceNameOf mirrors agenttools.deviceNameOf: the DOS device base of a
// component (canonically trimmed, cut at the first dot, folded with the guard's
// name fold - finding N8: this used to be strings.ToLower, which is not the fold
// the write guard compares with, so a homoglyph spelling of a device name was
// authorable here and refused there).
func deviceNameOf(comp string) string {
	base := canonicalScopeComponent(comp)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return agenttools.Win32NameFold(base)
}

// scopeComponentHygiene mirrors agenttools.componentHygieneFailure: the reason a
// component cannot bound a write, or "" when it can. The wording is the plan
// side's own (a scope, not a target), so the two refusals read correctly at their
// own layer while refusing the same shapes.
func scopeComponentHygiene(comp string) string {
	if comp == "" {
		return ""
	}
	if comp == "." || comp == ".." {
		return "is a path traversal component"
	}
	canon := canonicalScopeComponent(comp)
	switch {
	case canon == "":
		return "canonicalizes to an empty name (Win32 strips every trailing dot and space)"
	case canon != comp:
		return "ends with a dot or a space, which Win32 strips"
	}
	if reservedDeviceNames[deviceNameOf(comp)] {
		return "is a reserved DOS device name, which addresses a device instead of a file"
	}
	return ""
}

// normalizeScopeText renders a scope entry in the canonical form the runtime
// matcher compares, mirroring fanout's normalizeScopeEntry (units.go:595) and
// agenttools' normalizeScopePath (guard.go:428): forward slashes, path.Clean'd,
// no leading "/", and "" for an entry that denotes the workspace root itself.
//
// This is a DELIBERATE byte-copy of three unexported functions in packages this one
// must not reach across (fanout owns the parser, agenttools owns the matcher, and
// neither exports its normalizer), and of codesnap.normalizeScopeEntry
// (snapshot.go), which IS exported-package code but unexported-function code, so the
// body is restated rather than called. The copy PRE-DATES the FIX-F4 delta; it was
// recorded as a P3 residue by the round-6 reviewers and is kept deliberately: a call
// into one of the three would make this package's consent-time scope text depend on
// whichever package won the argument, and the four bodies have to agree anyway
// (all four run filepath.ToSlash first, which is why a POSIX escape authored on
// Windows becomes a separator before any matcher sees it).
//
// Drift risk, stated so it is not mistaken for coverage: nothing pins these four
// bodies to each other byte for byte. What IS pinned is their EFFECT on the shipped
// rows - internal/codework/fix_e2_test.go runs every scope spelling through
// normalizeScopeText and then through BOTH codesnap.LedgerScopeClaims and
// agenttools.PathInScope, so a normalization that moved would show up as a parity
// break there rather than as a silent disagreement. A future change to any of the
// four belongs in the same commit as that table.
func normalizeScopeText(s string) string {
	s = filepath.ToSlash(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}

// ---------------------------------------------------------------------------
// FIX-D1a (R3-1 / R3-2 / R3-3) mirror: the name-class fold, the Win32 stream
// syntax and the component-wide 8.3 shape rule.
//
// Same rule as the rest of this mirror block: the authority is
// internal/agenttools/guard.go and these are copies, because the guard's sets and
// predicates it needs here are unexported and this package must not depend on
// the tool layer's internals. TestGovernanceScopeSetMatchesGuard pins the fold
// behaviour, the gate and the shape table in both directions.

// runtimeNameRules reports whether the host-side name SHAPE rules apply: the
// stream separator (R3-2) and the 8.3 alias shape (R3-3). It mirrors
// agenttools.win32StreamSyntaxRule (guard_win32_windows.go: true,
// guard_win32_other.go: false). The guard can express that switch as a
// build-tagged const because it already has a platform file per behaviour; this
// package has none, so it reads the same host property from runtime. The rules
// that do not depend on the host (trailing dot/space, reserved device names) stay
// unconditional in both layers.
var runtimeNameRules = runtime.GOOS == "windows"

// win32FoldName is agenttools.Win32NameFold (guard.go:824), the name-class fold
// this package's mirror compares with. It used to be a local copy
// (strings.ToLower(strings.ToUpper(name))); it now calls the exported authority so
// the plan layer and the write layer cannot fold differently (finding N8: the
// remaining ToLower call sites in deviceNameOf and the governance watchlist were
// the last two compares that did not). The fold's limits are the guard's limits:
// it over-folds a few characters (fail closed) and does not fold superscript
// look-alikes.
func win32FoldName(name string) string {
	return agenttools.Win32NameFold(name)
}

// streamBase mirrors agenttools.streamBaseOf: the part of a component that names
// the FILE, i.e. everything before the first Win32 stream separator.
func streamBase(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

// scopeStreamClass names the refusal CLASS for a scope entry that carries Win32
// stream syntax (finding R3-2, named by N8(g)). The class is a stable phrase on
// purpose: the blind-qa smoke row asked for a reason CLASS in the refusal rather
// than only "this path is not allowed", because the same shape reaches three
// different layers (this one, the write guard, the scope matcher) and an operator
// needs to read the same name at all three.
const scopeStreamClass = "Windows stream syntax not allowed in scope entries"

// scopeStreamDetail is why such an entry cannot bound a write: it names a stream of
// a file, so no path.Match over its components describes where a write lands.
const scopeStreamDetail = "contains ':', the Win32 alternate-data-stream separator, so the scope would name a stream of a file instead of a file"

// scopeStreamReason is the arm label scopeComponentShape returns. The two message
// emitters - buildScope here and refuseScopeEntry (phases.go) - compare against it
// to name scopeStreamClass instead of folding the stream arm into the generic
// "component is not the shape the guard can bound" sentence.
const scopeStreamReason = scopeStreamClass + ": " + scopeStreamDetail

// scopeAliasReason mirrors agenttools.shortAliasWriteReason, worded for a scope
// entry.
const scopeAliasReason = "has the Win32 8.3 short-name shape (NAME~1.EXT, one to five digits), which can address a directory or file whose lexical name the plan never names"

// scopeComponentShape mirrors agenttools.componentShapeFailure: everything wrong
// with one component as a scope component, in the guard's order (stream syntax,
// canonical form, 8.3 shape), or "" when it is clear. The hygiene half keeps its
// own wording (scopeComponentHygiene) so a refusal reads correctly at this layer.
func scopeComponentShape(comp string) string {
	if runtimeNameRules && strings.IndexByte(comp, ':') >= 0 {
		return scopeStreamReason
	}
	if why := scopeComponentHygiene(comp); why != "" {
		return why
	}
	if runtimeNameRules && win32ShortAliasShape(comp) {
		return scopeAliasReason
	}
	return ""
}

// win32ShortAliasShape mirrors agenttools.win32ShortAliasShape
// (guard_win32_windows.go): a "~" followed by one to five digits directly before
// a dot or at the end of the component. On a host with no 8.3 names the caller
// (scopeComponentShape) never runs it, mirroring the false stub.
func win32ShortAliasShape(comp string) bool {
	base := canonicalScopeComponent(comp)
	end := len(base)
	for {
		if aliasTailDigits(base[:end]) {
			return true
		}
		i := strings.LastIndexByte(base[:end], '.')
		if i <= 0 {
			return false
		}
		end = i
	}
}

// aliasTailDigits mirrors agenttools.aliasTailDigits: s ends with "~" followed by
// one to five digits.
func aliasTailDigits(s string) bool {
	i := strings.LastIndexByte(s, '~')
	if i <= 0 || i == len(s)-1 {
		return false
	}
	digits := s[i+1:]
	if len(digits) > 5 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
