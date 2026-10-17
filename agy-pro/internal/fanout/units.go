package fanout

// Partition-mode units plan (work item B1): the INPUT FORMAT for partition mode.
//
// A plan file is a JSON document: a top-level array of unit objects. Each unit
// is one disjoint slice of work; partition mode runs one agent (seat) per unit
// concurrently and merges the results afterwards (merge is a later work item).
// The types here are the CONTRACT consumed by the per-unit prompt plumbing, so
// the exported surface is deliberately small and stable: Unit, ParsePlan,
// UnitBody.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// Unit is one disjoint work unit in a partition plan.
type Unit struct {
	ID          string   `json:"id"`              // required, unique across the plan
	Title       string   `json:"title"`           // required, human-readable
	Scope       []string `json:"scope"`           // required, at least one entry; file globs/paths this unit may touch
	Deliverable string   `json:"deliverable"`     // required; what the unit must produce
	Seat        string   `json:"seat,omitempty"`  // optional "kind:model" seat override; empty = use the run default
	Body        string   `json:"body,omitempty"`  // optional explicit prompt body; empty = synthesized
	Write       bool     `json:"write,omitempty"` // optional per-unit write opt-in (B4-1); default false = the seat's write_file/replace_in_file stay refused
}

// maxPlanUnits is the D4 unit-count ceiling: ParsePlan rejects a plan with
// more units, and validatePartitionUnits re-checks it for callers that build
// Units directly (the engine spawns ONE CONCURRENT PROVIDER SEAT PER UNIT,
// so an unbounded supply-chain plan could exhaust provider quota/CPU, and the
// B7 deadline books up to partitionMaxTimeoutSec for it). 64 is generous for
// real decomposition work and bounded for the machine.
const maxPlanUnits = 64

// ParsePlan parses a JSON plan (a top-level array of unit objects) into units.
// It returns a clear error (never a partial silent result) when:
//   - the JSON is malformed;
//   - the plan carries more than maxPlanUnits units (D4 fan-out ceiling);
//   - a unit is missing any required field (id, title, scope, deliverable) or scope is empty;
//   - two units share an id;
//   - two units have a syntactically-detectable scope overlap.
//
// Unknown JSON fields are ignored (forward compatible). An empty top-level
// array (or null) is accepted and yields zero units; it is the caller's job to
// decide whether a zero-unit run is meaningful. Values are kept verbatim from
// the JSON (no trimming); only validation trims.
func ParsePlan(data []byte) ([]Unit, error) {
	var units []Unit
	if err := json.Unmarshal(data, &units); err != nil {
		return nil, fmt.Errorf("plan: invalid JSON: %w", err)
	}
	// D4: fail fast before any per-unit work or the O(n^2) overlap probe: the
	// engine would otherwise spawn one uncapped provider goroutine per unit.
	if len(units) > maxPlanUnits {
		return nil, maskPlanError(units, fmt.Errorf("plan: %d units exceeds the maximum of %d (partition mode runs one concurrent seat per unit; split the work into smaller plans)", len(units), maxPlanUnits))
	}
	seen := make(map[string]int, len(units))
	for i := range units {
		if err := validateUnit(i, units[i]); err != nil {
			return nil, maskPlanError(units, err)
		}
		// B3: dedupe on the SAME trimmed id validateUnit accepts, so "u1" and
		// "u1 " collide instead of slipping past as two distinct ids.
		id := strings.TrimSpace(units[i].ID)
		if prev, dup := seen[id]; dup {
			return nil, maskPlanError(units, fmt.Errorf("plan: units %d and %d: duplicate unit id %q", prev+1, i+1, id))
		}
		seen[id] = i
	}
	if err := checkScopeOverlap(units); err != nil {
		return nil, maskPlanError(units, err)
	}
	return units, nil
}

// maskPlanError renders a plan-authored validation error through the D3/E2
// plan-field redactor (planFieldSeeds over the parsed units: id/title/seat/
// scope, only SeedEligible values seed). ParsePlan, validatePartitionUnits and
// checkScopeOverlap quote unit ids (via unitLabel) and scope entries verbatim,
// and they run BEFORE the merge redactor exists: without the seeds a key-shaped
// id or scope entry would survive to the CLI stderr (the --units error paths
// seed only --seats/--models, which are always empty in --units mode) and to
// any library caller of ParsePlan. Benign ids stay readable because they never
// seed; the wrapped chain is dropped because these errors are displayed, not
// inspected with errors.Is/As.
func maskPlanError(units []Unit, err error) error {
	if err == nil {
		return nil
	}
	seeds := planFieldSeeds(units)
	if len(seeds) == 0 {
		return err
	}
	return errors.New(providers.NewRedactor(seeds...)(err.Error()))
}

// validateUnit rejects a unit that misses a required field or carries a blank
// scope entry. The error names the offending unit by 1-based position, plus its
// id when one is present.
func validateUnit(idx int, u Unit) error {
	label := unitLabel(idx, u.ID)
	if strings.TrimSpace(u.ID) == "" {
		return fmt.Errorf("plan: %s: missing required field %q", label, "id")
	}
	if strings.TrimSpace(u.Title) == "" {
		return fmt.Errorf("plan: %s: missing required field %q", label, "title")
	}
	if len(u.Scope) == 0 {
		return fmt.Errorf("plan: %s: missing required field %q (at least one entry)", label, "scope")
	}
	for j, s := range u.Scope {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("plan: %s: scope[%d]: empty scope entry", label, j)
		}
		// B2: reject a root-wide entry (".", "/", blank-after-clean). PathInScope
		// treats it as match-everything, so an unbounded unit would be blessed
		// as disjoint from every other unit while its seat can read the whole
		// workspace.
		if normalizeScopeEntry(s) == "" {
			return fmt.Errorf("plan: %s: scope[%d]: root-wide scope entry %q is rejected (partition mode requires a bounded scope; \".\", \"/\" and blank-after-clean match every path)", label, j, s)
		}
		// C2: reject a bare-wildcard entry ("*", "**", "?*", "[a-z]*", "*/*").
		// PathInScope's directory-prefix rule makes such an entry grant
		// (nearly) every path, re-opening the blank->"." closure for a
		// single-unit unbounded seat. Bounded globs with a literal anchor
		// ("src/*.go") stay accepted.
		if isRootWideGlob(normalizeScopeEntry(s)) {
			return fmt.Errorf("plan: %s: scope[%d]: root-wide-equivalent scope entry %q is rejected (a bare-wildcard entry matches every path; partition mode requires a bounded scope)", label, j, s)
		}
	}
	if strings.TrimSpace(u.Deliverable) == "" {
		return fmt.Errorf("plan: %s: missing required field %q", label, "deliverable")
	}
	return nil
}

// unitLabel renders "unit N" or "unit N (id \"x\")" for error messages.
func unitLabel(idx int, id string) string {
	if strings.TrimSpace(id) == "" {
		return fmt.Sprintf("unit %d", idx+1)
	}
	return fmt.Sprintf("unit %d (id %q)", idx+1, id)
}

// checkScopeOverlap rejects plans where two units have scope entries that could
// match a common path. Iteration is plan order, so the reported pair is
// deterministic. This is a fast early guard only; the merge step re-checks
// overlap at runtime and stays the authority.
//
// H1: every probe in the loop below draws from ONE overlapBudget shared by the
// whole plan, so a pathological plan with many units cannot multiply the
// per-pair search cost without bound (aggregate parser-DoS). When the shared
// budget runs out, the loop fails SAFE: the plan is rejected with a
// conservative-overlap error instead of admitting scopes that could not be
// proven disjoint within the budget.
func checkScopeOverlap(units []Unit) error {
	budget := newOverlapBudget()
	for i := 0; i < len(units); i++ {
		for j := i + 1; j < len(units); j++ {
			for _, a := range units[i].Scope {
				for _, b := range units[j].Scope {
					if scopesOverlapBudgeted(a, b, budget) {
						if budget.exhausted {
							return fmt.Errorf("plan: units %d and %d: scope-overlap probe budget exhausted (%d nodes across the whole plan); the plan is rejected conservatively because the scopes could not be proven disjoint within the parse-time budget (split the work into smaller plans or use narrower scope globs)", i+1, j+1, budget.limit)
						}
						return fmt.Errorf("plan: units %d and %d: scope overlap: %q (unit %d) and %q (unit %d) could match a common path", i+1, j+1, a, i+1, b, j+1)
					}
				}
			}
		}
	}
	return nil
}

// scopesOverlap reports whether two scope entries could match a common path.
// It first delegates to agenttools.PathInScope so the parse-time detector and
// the runtime scope filter agree BY CONSTRUCTION (single source of truth,
// guard.go): two entries a and b overlap iff b is a path that pattern a
// grants, OR a is a path that pattern b grants. This is why the contested pair
// "src/*" vs "src/foo/bar.go" now reports OVERLAP: PathInScope grants "src/*"
// access to "src/foo/bar.go" through its directory-prefix rule, so a plan that
// assigned them to different units would let two seats read the same file.
// Distinct siblings ("src/a*.go" vs "src/b*.go") stay DISJOINT because
// PathInScope returns false both ways.
//
// C3: the entry-vs-entry test is still blind to glob-vs-glob intersections
// that share a common matched path ("src/a*.go" vs "src/*b.go" both grant
// "src/ab.go" yet neither grants the other). When both entries carry glob
// metacharacters, a conservative probe cross-check runs: a concrete sample
// derived from one pattern granted by the other, or a constructed common
// probe matching both, reports overlap. This guard MAY over-report and is
// deliberately conservative; the runtime merge is the authoritative
// disjointness check and stays the final word.
func scopesOverlap(a, b string) bool {
	return scopesOverlapBudgeted(a, b, newOverlapBudget())
}

// scopesOverlapBudgeted is scopesOverlap drawing every probe-search node from
// the caller-shared budget. H1: when the shared budget is exhausted the probe
// fails SAFE and reports OVERLAP (conservative reject) instead of admitting
// scopes that could not be proven disjoint; the per-pair cap in
// commonComponentSample stays in place on top of the shared budget.
func scopesOverlapBudgeted(a, b string, budget *overlapBudget) bool {
	if agenttools.PathInScope(b, []string{a}) || agenttools.PathInScope(a, []string{b}) {
		return true
	}
	if !hasGlobMeta(a) || !hasGlobMeta(b) {
		return false
	}
	for _, probe := range scopeEntryProbes(a) {
		if agenttools.PathInScope(probe, []string{b}) {
			return true
		}
	}
	for _, probe := range scopeEntryProbes(b) {
		if agenttools.PathInScope(probe, []string{a}) {
			return true
		}
	}
	_, ok := commonScopeProbe(a, b, budget)
	if budget.exhausted {
		return true // H1 fail-safe: budget exhausted -> conservative overlap
	}
	return ok
}

// hasGlobMeta reports whether the entry carries any glob metacharacter.
func hasGlobMeta(entry string) bool {
	return strings.ContainsAny(entry, "*?[")
}

// scopeEntryProbes derives a concrete probe path from a scope entry by
// substituting a fixed sample ("x") for every wildcard run and character
// class, component by component. The probe matches the entry by construction.
func scopeEntryProbes(entry string) []string {
	comps := strings.Split(normalizeScopeEntry(entry), "/")
	for i, c := range comps {
		comps[i] = globComponentSample(c)
	}
	return []string{strings.Join(comps, "/")}
}

// globComponentSample renders one concrete string that matches the component.
func globComponentSample(c string) string {
	var b strings.Builder
	for i := 0; i < len(c); {
		switch c[i] {
		case '*', '?':
			b.WriteByte('x')
			for i < len(c) && (c[i] == '*' || c[i] == '?') {
				i++
			}
		case '[':
			b.WriteByte('x')
			if j := strings.IndexByte(c[i:], ']'); j >= 0 {
				i += j + 1
			} else {
				i++
			}
		default:
			b.WriteByte(c[i])
			i++
		}
	}
	return b.String()
}

// overlapBudget is the H1 aggregate DoS budget for the glob-vs-glob overlap
// probe: ONE budget per ParsePlan call, shared by every unit pair, so a plan
// with many units cannot multiply the per-pair search cost without bound. The
// count is of probe-search nodes (deterministic), not wall-clock. When the
// budget runs out, exhausted latches and every further probe fails safe
// (reported as overlap); see checkScopeOverlap.
type overlapBudget struct {
	nodes     int
	limit     int
	exhausted bool
}

// overlapProbeBudgetNodes is the shared node budget for the whole overlap
// phase of one ParsePlan call. It is generous for real plans (bounded globs
// are proven disjoint in a handful of nodes; the per-component cap of 200k in
// commonComponentSample is the backstop only for adversarial glob pairs) and
// bounds a malicious plan to a small, constant amount of probe work.
const overlapProbeBudgetNodes = 1_000_000

func newOverlapBudget() *overlapBudget {
	return &overlapBudget{limit: overlapProbeBudgetNodes}
}

// consume spends one probe-search node from the shared budget; it returns
// false (and latches exhausted) once the budget is spent.
func (b *overlapBudget) consume() bool {
	if b.exhausted {
		return false
	}
	b.nodes++
	if b.nodes > b.limit {
		b.exhausted = true
		return false
	}
	return true
}

// commonScopeProbe tries to construct a concrete path granted by BOTH entries,
// proving a glob-vs-glob intersection the entry-vs-entry test cannot see. It
// brute-forces, per component, the shortest candidate over the joint alphabet
// (the entries' literal characters plus a fixed sample set) that matches both
// components, then verifies the joined probe against both full entries. The
// search is bounded (depth 6 plus a per-component node cap) and draws every
// node from the plan-shared budget; both bounds are deterministic. A common
// match that exists only beyond the bounds makes the guard under-report, which
// is safe: this is an early parse-time guard, not the authority. A budget
// exhaustion is signaled separately via budget.exhausted (fail-safe overlap).
func commonScopeProbe(a, b string, budget *overlapBudget) (string, bool) {
	ac := strings.Split(normalizeScopeEntry(a), "/")
	bc := strings.Split(normalizeScopeEntry(b), "/")
	n := len(ac)
	if len(bc) > n {
		n = len(bc)
	}
	alpha := probeAlphabet(a, b)
	out := make([]string, n)
	for i := 0; i < n; i++ {
		var ca, cb string
		haveA, haveB := i < len(ac), i < len(bc)
		if haveA {
			ca = ac[i]
		}
		if haveB {
			cb = bc[i]
		}
		s, ok := commonComponentSample(ca, cb, haveA, haveB, alpha, budget)
		if !ok {
			return "", false
		}
		out[i] = s
	}
	probe := strings.Join(out, "/")
	if agenttools.PathInScope(probe, []string{a}) && agenttools.PathInScope(probe, []string{b}) {
		return probe, true
	}
	return "", false
}

// probeAlphabet builds the deterministic candidate alphabet for the common-
// probe search: every literal character of both entries (in order), then the
// two fixed fill samples 'x' and 'y' when not already present. Two fills keep
// the search able to satisfy two distinct wildcard runs while bounding the
// branching factor.
func probeAlphabet(entries ...string) string {
	var b strings.Builder
	seen := map[byte]bool{}
	for _, e := range entries {
		for i := 0; i < len(e); i++ {
			ch := e[i]
			if ch == '*' || ch == '?' || ch == '[' || ch == ']' || ch == '\\' || ch == '/' {
				continue
			}
			if !seen[ch] {
				seen[ch] = true
				b.WriteByte(ch)
			}
		}
	}
	for _, ch := range []byte("xy") {
		if !seen[ch] {
			seen[ch] = true
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// commonComponentSample finds the shortest string (depth- and budget-bounded,
// deterministic depth-first over alpha) that matches both components. A
// missing component (the entry is shorter, so its directory-prefix rule
// grants anything) leaves only the other side constraining. Branches whose
// prefix no component can ever extend to a match are pruned by
// componentPrefixMatchable, so the search stays near the viable path. The
// depth bound, the per-component node cap, and the plan-shared budget are the
// guard's deliberate ceilings: a common match that exists only beyond them
// makes the guard under-report, which is safe - this is an early parse-time
// guard, not the authority.
func commonComponentSample(ca, cb string, haveA, haveB bool, alpha string, budget *overlapBudget) (string, bool) {
	const maxDepth = 6
	const nodeBudget = 200000
	nodes := 0
	var gen func(prefix string, depth int) (string, bool)
	gen = func(prefix string, depth int) (string, bool) {
		nodes++
		if nodes > nodeBudget || !budget.consume() {
			return "", false
		}
		if depth > 0 {
			if (!haveA || componentMatches(ca, prefix)) && (!haveB || componentMatches(cb, prefix)) {
				return prefix, true
			}
			if (haveA && !componentPrefixMatchable(ca, prefix)) || (haveB && !componentPrefixMatchable(cb, prefix)) {
				return "", false // no extension of this prefix can match that side
			}
		}
		if depth == maxDepth {
			return "", false
		}
		for i := 0; i < len(alpha); i++ {
			if s, ok := gen(prefix+alpha[i:i+1], depth+1); ok {
				return s, true
			}
		}
		return "", false
	}
	return gen("", 0)
}

// componentPrefixMatchable reports whether SOME string beginning with prefix
// matches the path.Match component c: the pattern may consume the rest of the
// prefix and finish inside the unseen tail (so this is a necessary condition
// for any common match extending prefix, never a sufficient one). It is the
// search prune for commonComponentSample.
func componentPrefixMatchable(c, prefix string) bool {
	memo := make([][]int8, len(c)+1)
	for i := range memo {
		memo[i] = make([]int8, len(prefix)+1)
		for j := range memo[i] {
			memo[i][j] = -1
		}
	}
	var f func(pi, ti int) bool
	f = func(pi, ti int) bool {
		if pi == len(c) {
			return ti == len(prefix)
		}
		if memo[pi][ti] >= 0 {
			return memo[pi][ti] == 1
		}
		ans := false
		switch c[pi] {
		case '*':
			for n := 0; n <= len(prefix)-ti && !ans; n++ {
				ans = f(pi+1, ti+n)
			}
			if !ans {
				ans = patternMatchable(c[pi+1:]) // '*' eats the prefix remainder and into the tail
			}
		case '?':
			if ti < len(prefix) {
				ans = f(pi+1, ti+1)
			} else {
				ans = patternMatchable(c[pi+1:])
			}
		case '[':
			j := strings.IndexByte(c[pi:], ']')
			switch {
			case j < 0:
				ans = false // malformed class: path.Match fails it
			case ti < len(prefix):
				if cls := c[pi : pi+j+1]; classByteMatch(cls, prefix[ti]) {
					ans = f(pi+j+1, ti+1)
				}
			default:
				ans = patternMatchable(c[pi+j+1:])
			}
		default:
			if ti < len(prefix) {
				ans = c[pi] == prefix[ti] && f(pi+1, ti+1)
			} else {
				ans = patternMatchable(c[pi+1:])
			}
		}
		if ans {
			memo[pi][ti] = 1
		} else {
			memo[pi][ti] = 0
		}
		return ans
	}
	return f(0, 0)
}

// classByteMatch reports whether the path.Match character class cls matches
// the single byte b.
func classByteMatch(cls string, b byte) bool {
	ok, err := path.Match(cls, string(b))
	return err == nil && ok
}

// patternMatchable reports whether the pattern fragment can match SOME string
// (every character class is well-formed). Used for the unseen-tail cases of
// componentPrefixMatchable.
func patternMatchable(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] == '[' {
			j := strings.IndexByte(p[i:], ']')
			if j < 0 {
				return false
			}
			i += j
		}
	}
	return true
}

func componentMatches(c, s string) bool {
	ok, err := path.Match(c, s)
	return err == nil && ok
}

// isRootWideGlob reports whether a NORMALIZED scope entry is root-wide-
// equivalent (C2): an entry made only of unanchored wildcards that
// PathInScope's directory-prefix rule grants (nearly) every path, the same
// fail-open as a literal "." entry. An entry is root-wide-equivalent when
// EVERY component matches any name ("*", "**", "?*") or consists only of
// glob metacharacters with at least one "*" anywhere ("[a-z]*", "*/*",
// "?/*"). A bounded glob with a literal anchor in ANY component ("src/*.go",
// "*.go", "src/[a-z]*") keeps at least one component anchored and stays
// accepted. The check is deterministic and lexical; no filesystem access.
func isRootWideGlob(entry string) bool {
	comps := strings.Split(entry, "/")
	allMatchAny, allWildcardOnly, hasStar := true, true, false
	for _, c := range comps {
		if !componentMatchesAnyName(c) {
			allMatchAny = false
		}
		wildcardOnly, star := componentWildcardOnly(c)
		if !wildcardOnly {
			allWildcardOnly = false
		}
		hasStar = hasStar || star
	}
	return allMatchAny || (allWildcardOnly && hasStar)
}

// componentMatchesAnyName reports whether the single path.Match component c
// matches EVERY real file-name component (real names are never empty): the
// component must contain at least one "*" (which absorbs any run, including
// empty) and at most one "?" (a second "?" would require a two-character
// name, so one-character names escape). Any other token - a literal or a
// character class - anchors the component to some names, so it is bounded.
func componentMatchesAnyName(c string) bool {
	hasStar, qmarks := false, 0
	for i := 0; i < len(c); i++ {
		switch c[i] {
		case '*':
			hasStar = true
		case '?':
			qmarks++
		default:
			return false
		}
	}
	return hasStar && qmarks <= 1
}

// componentWildcardOnly reports whether the component is built exclusively of
// glob metacharacters (star, question mark, character classes) - i.e. it
// carries no literal anchor anywhere - and whether it contains at least one
// "*".
func componentWildcardOnly(c string) (wildcardOnly bool, hasStar bool) {
	for i := 0; i < len(c); i++ {
		switch c[i] {
		case '*':
			hasStar = true
		case '?':
		case '[':
			// A character class is one wildcard token; skip to its close.
			if j := strings.IndexByte(c[i:], ']'); j >= 0 {
				i += j
			} else {
				return false, hasStar // malformed class: treat as anchored
			}
		default:
			return false, hasStar
		}
	}
	return true, hasStar
}

// normalizeScopeEntry renders a scope entry in the canonical form the runtime
// matcher compares: forward slashes, path.Clean'd, no leading "/". It mirrors
// agenttools' own normalization so the two never drift. An entry that denotes
// the workspace root itself ("", ".", "/", "./", "src/..", ...) normalizes to
// "" - PathInScope treats that as match-everything, so validateUnit and
// validatePartitionUnits reject it (B2): an unbounded unit cannot be a disjoint
// slice.
func normalizeScopeEntry(s string) string {
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

// validatePartitionUnits re-checks every partition unit's scope the way
// ParsePlan does, for callers that build RunConfig.Units directly and bypass the
// CLI's ParsePlan (a library caller, a test, swarm delegation). A unit with an
// empty, blank, or root-wide Scope would yield a nil ToolRunner scope filter: an
// UNRESTRICTED seat (fail-open). The engine calls this at the top of the
// partition branch so it fails closed instead (B4).
func validatePartitionUnits(units []Unit) error {
	// D4: same ceiling ParsePlan enforces, for callers that build Units
	// directly and would otherwise fan out one uncapped provider seat per unit.
	if len(units) > maxPlanUnits {
		return maskPlanError(units, fmt.Errorf("partition: %d units exceeds the maximum of %d (split the work into smaller plans)", len(units), maxPlanUnits))
	}
	for i, u := range units {
		label := unitLabel(i, u.ID)
		if len(u.Scope) == 0 {
			return maskPlanError(units, fmt.Errorf("partition: %s: missing scope (a unit must carry at least one bounded scope entry)", label))
		}
		for j, s := range u.Scope {
			if strings.TrimSpace(s) == "" {
				return maskPlanError(units, fmt.Errorf("partition: %s: scope[%d]: empty scope entry", label, j))
			}
			if normalizeScopeEntry(s) == "" {
				return maskPlanError(units, fmt.Errorf("partition: %s: scope[%d]: root-wide scope entry %q is rejected (partition mode requires a bounded scope)", label, j, s))
			}
			// C2: same bare-wildcard rejection as validateUnit (see there).
			if isRootWideGlob(normalizeScopeEntry(s)) {
				return maskPlanError(units, fmt.Errorf("partition: %s: scope[%d]: root-wide-equivalent scope entry %q is rejected (a bare-wildcard entry matches every path; partition mode requires a bounded scope)", label, j, s))
			}
		}
	}
	return nil
}

// UnitBody returns the prompt body for a unit: the explicit Body when non-empty,
// otherwise a DETERMINISTIC synthesis built only from Title + Scope +
// Deliverable (same unit -> byte-identical body every call; no timestamps, no
// randomness). A whitespace-only Body counts as empty.
func UnitBody(u Unit) string {
	if strings.TrimSpace(u.Body) != "" {
		return u.Body
	}
	var b strings.Builder
	b.WriteString("Unit: ")
	b.WriteString(u.Title)
	b.WriteString("\n\nScope (the only paths this unit may touch):\n")
	for _, s := range u.Scope {
		b.WriteString("  - ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	b.WriteString("\nDeliverable: ")
	b.WriteString(u.Deliverable)
	b.WriteString("\n")
	return b.String()
}
