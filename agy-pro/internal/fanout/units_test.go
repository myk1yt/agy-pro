package fanout

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"agypro/internal/agenttools"
)

func TestParsePlan_ValidMultiUnit(t *testing.T) {
	plan := []byte(`[
      {"id":"u1","title":"Cache layer","scope":["src/cache/*.go","src/cache"],"deliverable":"a cache manager","seat":"native:gemini-3.8-flash-high"},
      {"id":"u2","title":"Dashboard","scope":["src/dashboard/*.js"],"deliverable":"a dashboard view","body":"explicit body for u2"},
      {"id":"u3","title":"Docs","scope":["docs/*.md"],"deliverable":"updated docs"}
    ]`)
	units, err := ParsePlan(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(units) != 3 {
		t.Fatalf("expected 3 units, got %d: %+v", len(units), units)
	}
	want := []Unit{
		{ID: "u1", Title: "Cache layer", Scope: []string{"src/cache/*.go", "src/cache"}, Deliverable: "a cache manager", Seat: "native:gemini-3.8-flash-high"},
		{ID: "u2", Title: "Dashboard", Scope: []string{"src/dashboard/*.js"}, Deliverable: "a dashboard view", Body: "explicit body for u2"},
		{ID: "u3", Title: "Docs", Scope: []string{"docs/*.md"}, Deliverable: "updated docs"},
	}
	for i := range want {
		got := units[i]
		if got.ID != want[i].ID || got.Title != want[i].Title || got.Deliverable != want[i].Deliverable {
			t.Errorf("unit %d scalars = %+v; want %+v", i+1, got, want[i])
		}
		if got.Seat != want[i].Seat {
			t.Errorf("unit %d Seat = %q; want %q", i+1, got.Seat, want[i].Seat)
		}
		if got.Body != want[i].Body {
			t.Errorf("unit %d Body = %q; want %q", i+1, got.Body, want[i].Body)
		}
		if len(got.Scope) != len(want[i].Scope) {
			t.Fatalf("unit %d scope = %v; want %v", i+1, got.Scope, want[i].Scope)
		}
		for j := range got.Scope {
			if got.Scope[j] != want[i].Scope[j] {
				t.Errorf("unit %d scope[%d] = %q; want %q", i+1, j, got.Scope[j], want[i].Scope[j])
			}
		}
	}
}

func TestParsePlan_MalformedJSON(t *testing.T) {
	cases := map[string]string{
		"garbage":        `not json at all`,
		"truncated":      `[{"id":"u1","title":`,
		"non-array":      `{"id":"u1","title":"t","scope":["s"],"deliverable":"d"}`,
		"wrong-type":     `[{"id":42,"title":"t","scope":["s"],"deliverable":"d"}]`,
		"bad-escape":     `[{"id":"u1","title":"t","scope":["\x"],"deliverable":"d"}]`,
		"unterminated-2": `[{"id":"u1","title":"t","scope":["s"],"deliverable":"d"}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			units, err := ParsePlan([]byte(doc))
			if err == nil {
				t.Fatalf("expected error for malformed JSON, got units %+v", units)
			}
			if len(units) != 0 {
				t.Errorf("expected no partial result, got %+v", units)
			}
			if !strings.Contains(err.Error(), "plan:") {
				t.Errorf("error %q should carry the plan: prefix", err)
			}
		})
	}
}

func TestParsePlan_MissingRequiredField(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantSub string // must appear in the error (offending unit + field)
	}{
		{"no-id", `[{"title":"t","scope":["src/a.go"],"deliverable":"d"}]`, "unit 1"},
		{"no-id-field-word", `[{"title":"t","scope":["src/a.go"],"deliverable":"d"}]`, `"id"`},
		{"no-title", `[{"id":"u1","scope":["src/a.go"],"deliverable":"d"}]`, `"title"`},
		{"no-title-names-unit", `[{"id":"u7","scope":["src/a.go"],"deliverable":"d"}]`, `unit 1 (id "u7")`},
		{"empty-scope-array", `[{"id":"u1","title":"t","scope":[],"deliverable":"d"}]`, "scope"},
		{"null-scope", `[{"id":"u1","title":"t","scope":null,"deliverable":"d"}]`, "scope"},
		{"no-deliverable", `[{"id":"u1","title":"t","scope":["src/a.go"]}]`, `"deliverable"`},
		{"blank-title", `[{"id":"u1","title":"   ","scope":["src/a.go"],"deliverable":"d"}]`, `"title"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			units, err := ParsePlan([]byte(tc.doc))
			if err == nil {
				t.Fatalf("expected error, got units %+v", units)
			}
			if len(units) != 0 {
				t.Errorf("expected no partial result, got %+v", units)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q must name the offending unit/field (want substring %q)", err, tc.wantSub)
			}
		})
	}
}

func TestParsePlan_DuplicateID(t *testing.T) {
	doc := []byte(`[
      {"id":"u1","title":"a","scope":["src/a/*.go"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["src/b/*.go"],"deliverable":"db"},
      {"id":"u1","title":"c","scope":["src/c/*.go"],"deliverable":"dc"}
    ]`)
	units, err := ParsePlan(doc)
	if err == nil {
		t.Fatalf("expected duplicate-id error, got units %+v", units)
	}
	if len(units) != 0 {
		t.Errorf("expected no partial result, got %+v", units)
	}
	if !strings.Contains(err.Error(), `duplicate unit id "u1"`) {
		t.Errorf("error %q must name the duplicated id", err)
	}
	if !strings.Contains(err.Error(), "units 1 and 3") {
		t.Errorf("error %q must name both offending unit positions", err)
	}
}

func TestParsePlan_ScopeOverlapRejected(t *testing.T) {
	doc := []byte(`[
      {"id":"u1","title":"a","scope":["src/foo"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["src/foo/bar.go"],"deliverable":"db"}
    ]`)
	units, err := ParsePlan(doc)
	if err == nil {
		t.Fatalf("expected scope-overlap error, got units %+v", units)
	}
	if len(units) != 0 {
		t.Errorf("expected no partial result, got %+v", units)
	}
	if !strings.Contains(err.Error(), "scope overlap") || !strings.Contains(err.Error(), `"src/foo"`) || !strings.Contains(err.Error(), `"src/foo/bar.go"`) {
		t.Errorf("error %q must name the overlapping scope entries", err)
	}
}

func TestParsePlan_DistinctSiblingsNoError(t *testing.T) {
	doc := []byte(`[
      {"id":"u1","title":"a","scope":["src/a*.go"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["src/b*.go"],"deliverable":"db"},
      {"id":"u3","title":"c","scope":["docs/x/*.md"],"deliverable":"dc"},
      {"id":"u4","title":"d","scope":["docs/y/*.md"],"deliverable":"dd"},
      {"id":"u5","title":"e","scope":["src/foo"],"deliverable":"de"},
      {"id":"u6","title":"f","scope":["src/foobar"],"deliverable":"df"}
    ]`)
	units, err := ParsePlan(doc)
	if err != nil {
		t.Fatalf("distinct siblings must not be reported as overlapping: %v", err)
	}
	if len(units) != 6 {
		t.Fatalf("expected 6 units, got %d", len(units))
	}
}

func TestParsePlan_BlankScopeEntry(t *testing.T) {
	cases := map[string]string{
		"whitespace-only": `[{"id":"u1","title":"t","scope":["  "],"deliverable":"d"}]`,
		"tab-only":        `[{"id":"u1","title":"t","scope":["src/a.go","\t"],"deliverable":"d"}]`,
		"empty-string":    `[{"id":"u1","title":"t","scope":[""],"deliverable":"d"}]`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			units, err := ParsePlan([]byte(doc))
			if err == nil {
				t.Fatalf("expected blank-scope error, got units %+v", units)
			}
			if len(units) != 0 {
				t.Errorf("expected no partial result, got %+v", units)
			}
			if !strings.Contains(err.Error(), "scope") {
				t.Errorf("error %q must mention scope", err)
			}
		})
	}
}

// TestScopesOverlap_Rule pins scopesOverlap, which now delegates to
// agenttools.PathInScope so the parse-time detector and the runtime scope
// filter agree by construction. The overlap list is every pair PathInScope
// grants a common path; the disjoint list is every pair it denies both ways.
func TestScopesOverlap_Rule(t *testing.T) {
	overlap := []struct{ a, b string }{
		{"src/foo", "src/foo"},          // exact equal
		{"src/foo", "src/foo/bar.go"},   // dir prefix (short first)
		{"src/foo/bar.go", "src/foo"},   // dir prefix (long first)
		{"src/foo", "src/foo/*"},        // dir vs glob under it
		{"src/foo/*", "src/foo/bar.go"}, // wildcard component of equal depth
		{"src/*", "src/foo"},            // "*" matches any component
		{"src/*/x.go", "src/foo/x.go"},  // interior wildcard
		{"src/f?o", "src/foo"},          // glob vs literal, equal depth
		{`src\foo`, "src/foo/bar.go"},   // backslash normalization
		{"src/foo/", "src/foo/bar.go"},  // trailing slash normalization
		{"./src/foo", "src/foo/bar.go"}, // "./" normalization
		// The three below were "disjoint" under the OLD prefix rule but the
		// runtime filter (PathInScope) GRANTS them a common path, so the aligned
		// parse-time detector must report overlap too (B1). The old rule let a
		// plan assign these to different units while both seats could read the
		// same file.
		{"src/*.go", "src/a*.go"},           // "src/*.go" also matches "src/aX.go"
		{"src/foo/*", "src/foo/bar.go/baz"}, // "src/foo/*" is a dir prefix of the deeper path
		{"src/*", "src/foo/bar.go"},         // "src/*" is a dir prefix of the deeper path
	}
	for _, tc := range overlap {
		if !scopesOverlap(tc.a, tc.b) {
			t.Errorf("scopesOverlap(%q, %q) = false; want true", tc.a, tc.b)
		}
		if !scopesOverlap(tc.b, tc.a) {
			t.Errorf("scopesOverlap(%q, %q) = false; want true (symmetry)", tc.b, tc.a)
		}
	}
	disjoint := []struct{ a, b string }{
		{"src/a*.go", "src/b*.go"},     // distinct sibling globs (PathInScope false both ways)
		{"src/foo", "src/foobar"},      // distinct literals
		{"docs/x/*.md", "docs/y/*.md"}, // sibling dirs
		{"src/a.go", "src/b.go"},       // distinct files
		{"src/foo", "other/foo"},       // distinct roots
	}
	for _, tc := range disjoint {
		if scopesOverlap(tc.a, tc.b) {
			t.Errorf("scopesOverlap(%q, %q) = true; want false", tc.a, tc.b)
		}
		if scopesOverlap(tc.b, tc.a) {
			t.Errorf("scopesOverlap(%q, %q) = true; want false (symmetry)", tc.b, tc.a)
		}
	}
}

// TestScopesOverlap_AgreesWithPathInScope is the B1 alignment pin: for a table
// of contested scope pairs, the parse-time scopesOverlap MUST return exactly the
// PathInScope-derived answer (b granted by pattern a, OR a granted by pattern
// b). PathInScope is the runtime authority, so any disagreement is a bug that
// would let two units' seats touch a common path undetected. Mutation-verified:
// reintroducing the old prefix rule makes the contested cases disagree and fails
// this pin.
func TestScopesOverlap_AgreesWithPathInScope(t *testing.T) {
	cases := []struct{ a, b string }{
		{"src/*", "src/foo/bar.go"},         // contested: now overlap (was disjoint)
		{"src/*.go", "src/a*.go"},           // contested: now overlap (was disjoint)
		{"src/foo/*", "src/foo/bar.go/baz"}, // contested: now overlap (was disjoint)
		{"src/a*.go", "src/b*.go"},          // distinct siblings: disjoint
		{"src/foo", "src/foobar"},           // distinct literals: disjoint
		{"docs/x/*.md", "docs/y/*.md"},      // sibling dirs: disjoint
		{"src/a.go", "src/b.go"},            // distinct files: disjoint
		{"src/foo", "other/foo"},            // distinct roots: disjoint
		{"src/foo", "src/foo/bar.go"},       // dir prefix: overlap
		{"src/foo", "src/foo/*"},            // dir vs glob: overlap
		{`src\foo`, "src/foo/bar.go"},       // backslash normalization: overlap
	}
	for _, tc := range cases {
		want := agenttools.PathInScope(tc.b, []string{tc.a}) || agenttools.PathInScope(tc.a, []string{tc.b})
		if got := scopesOverlap(tc.a, tc.b); got != want {
			t.Errorf("scopesOverlap(%q, %q) = %v; want %v (must match PathInScope)", tc.a, tc.b, got, want)
		}
		if got := scopesOverlap(tc.b, tc.a); got != want {
			t.Errorf("scopesOverlap(%q, %q) = %v; want %v (must match PathInScope, symmetry)", tc.b, tc.a, got, want)
		}
	}
}

// TestParsePlan_RootWideScopeRejected is the B2 pin: a scope entry that
// normalizes to the workspace root ("", ".", "/", "./", "src/..") is
// match-everything under PathInScope, so an unbounded unit must be rejected
// instead of blessed as disjoint. Mutation-verified.
func TestParsePlan_RootWideScopeRejected(t *testing.T) {
	cases := map[string]string{
		"dot":         `[{"id":"u1","title":"t","scope":["."],"deliverable":"d"}]`,
		"slash":       `[{"id":"u1","title":"t","scope":["/"],"deliverable":"d"}]`,
		"dot-slash":   `[{"id":"u1","title":"t","scope":["./"],"deliverable":"d"}]`,
		"up-to-root":  `[{"id":"u1","title":"t","scope":["src/.."],"deliverable":"d"}]`,
		"among-valid": `[{"id":"u1","title":"t","scope":["src/a.go","."],"deliverable":"d"}]`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			units, err := ParsePlan([]byte(doc))
			if err == nil {
				t.Fatalf("expected a root-wide-scope rejection, got units %+v", units)
			}
			if len(units) != 0 {
				t.Errorf("expected no partial result, got %+v", units)
			}
			if !strings.Contains(err.Error(), "root-wide") || !strings.Contains(err.Error(), "scope") {
				t.Errorf("error %q must name the root-wide scope rejection", err)
			}
		})
	}
}

// TestParsePlan_DuplicateIDTrimmed is the B3 pin: the duplicate-id check must
// use the SAME trimmed id the required-field validation accepts, so "u1" and
// "u1 " collide instead of slipping past as two distinct ids.
func TestParsePlan_DuplicateIDTrimmed(t *testing.T) {
	doc := []byte(`[
	      {"id":"u1","title":"a","scope":["src/a/*.go"],"deliverable":"da"},
	      {"id":"  u1  ","title":"b","scope":["src/b/*.go"],"deliverable":"db"}
	    ]`)
	units, err := ParsePlan(doc)
	if err == nil {
		t.Fatalf("expected a trimmed duplicate-id error, got units %+v", units)
	}
	if len(units) != 0 {
		t.Errorf("expected no partial result, got %+v", units)
	}
	if !strings.Contains(err.Error(), `duplicate unit id "u1"`) {
		t.Errorf("error %q must name the trimmed duplicated id", err)
	}
	if !strings.Contains(err.Error(), "units 1 and 2") {
		t.Errorf("error %q must name both offending unit positions", err)
	}
}

func TestUnitBody_ExplicitReturnedVerbatim(t *testing.T) {
	u := Unit{
		ID:          "u1",
		Title:       "Cache layer",
		Scope:       []string{"src/cache/*.go"},
		Deliverable: "a cache manager",
		Body:        "  Custom body.\n\nSecond paragraph with  spaces and trailing space \n",
	}
	got := UnitBody(u)
	if got != u.Body {
		t.Fatalf("UnitBody must return the explicit body verbatim\n got: %q\nwant: %q", got, u.Body)
	}
}

func TestUnitBody_EmptyBodyDeterministicSynthesis(t *testing.T) {
	u := Unit{
		ID:          "u1",
		Title:       "Cache layer",
		Scope:       []string{"src/cache/*.go", "src/cache"},
		Deliverable: "a cache manager",
	}
	got := UnitBody(u)

	// Pin 1: synthesis contains the Title, every Scope entry, and the Deliverable.
	for _, want := range []string{u.Title, u.Scope[0], u.Scope[1], u.Deliverable} {
		if !strings.Contains(got, want) {
			t.Errorf("synthesized body must contain %q\nbody:\n%s", want, got)
		}
	}

	// Pin 2: byte-identical across calls (determinism).
	for i := 0; i < 5; i++ {
		if again := UnitBody(u); again != got {
			t.Fatalf("call %d differs:\nfirst: %q\nagain: %q", i+2, got, again)
		}
	}

	// Pin 3: no volatile content (a golden body cannot contain a timestamp or
	// randomness); also assert the current year does not leak in.
	want := "Unit: Cache layer\n\nScope (the only paths this unit may touch):\n  - src/cache/*.go\n  - src/cache\n\nDeliverable: a cache manager\n"
	if got != want {
		t.Fatalf("synthesized body not deterministic/volatile-free\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, strconv.Itoa(time.Now().Year())) {
		t.Errorf("synthesized body must not contain a timestamp/year: %q", got)
	}
}

func TestUnitBody_WhitespaceBodyCountsAsEmpty(t *testing.T) {
	u := Unit{ID: "u1", Title: "Docs", Scope: []string{"docs/*.md"}, Deliverable: "updated docs", Body: "  \n\t "}
	got := UnitBody(u)
	if !strings.Contains(got, u.Title) || !strings.Contains(got, u.Deliverable) {
		t.Errorf("whitespace-only Body must fall back to synthesis, got: %q", got)
	}
}

func TestParsePlan_EmptyArrayAccepted(t *testing.T) {
	for _, doc := range []string{`[]`, `null`} {
		units, err := ParsePlan([]byte(doc))
		if err != nil {
			t.Errorf("ParsePlan(%s) unexpected error: %v", doc, err)
		}
		if len(units) != 0 {
			t.Errorf("ParsePlan(%s) = %+v; want zero units", doc, units)
		}
	}
}

// TestParsePlan_RootWideGlobRejected is the C2 pin: a bare-wildcard scope
// entry ("*", "**", "?*", "[a-z]*", "*/*") is root-wide-equivalent under
// PathInScope's directory-prefix rule, so it must be rejected exactly like a
// literal "." entry, while bounded globs with a literal anchor ("src/*.go")
// stay accepted. Mutation-verified.
func TestParsePlan_RootWideGlobRejected(t *testing.T) {
	rejected := []string{`*`, `**`, `?*`, `*?`, `*/*`, `?/*`, `[a-z]*`, `**/*`}
	for _, entry := range rejected {
		t.Run("reject/"+entry, func(t *testing.T) {
			doc := []byte(`[{"id":"u1","title":"t","scope":["` + entry + `"],"deliverable":"d"}]`)
			units, err := ParsePlan(doc)
			if err == nil {
				t.Fatalf("expected a root-wide-glob rejection for %q, got units %+v", entry, units)
			}
			if len(units) != 0 {
				t.Errorf("expected no partial result, got %+v", units)
			}
			if !strings.Contains(err.Error(), "root-wide") || !strings.Contains(err.Error(), "scope") {
				t.Errorf("error %q must name the root-wide scope rejection", err)
			}
		})
	}
	accepted := []string{`src/*.go`, `*.go`, `src/*`, `src/[a-z]*`, `src/a?.go`, `src/a*.go`}
	for _, entry := range accepted {
		t.Run("accept/"+entry, func(t *testing.T) {
			doc := []byte(`[{"id":"u1","title":"t","scope":["` + entry + `"],"deliverable":"d"}]`)
			units, err := ParsePlan(doc)
			if err != nil {
				t.Fatalf("bounded glob %q must stay accepted: %v", entry, err)
			}
			if len(units) != 1 {
				t.Errorf("expected 1 unit, got %+v", units)
			}
		})
	}
}

// TestValidatePartitionUnits_RootWideGlobRejected pins the C2 check on the
// engine-side validator too (validatePartitionUnits, for callers that bypass
// ParsePlan).
func TestValidatePartitionUnits_RootWideGlobRejected(t *testing.T) {
	bad := []Unit{{ID: "u1", Title: "t", Scope: []string{"*"}, Deliverable: "d"}}
	if err := validatePartitionUnits(bad); err == nil || !strings.Contains(err.Error(), "root-wide") {
		t.Errorf("validatePartitionUnits([*]) = %v; want a root-wide rejection", err)
	}
	bad2 := []Unit{{ID: "u1", Title: "t", Scope: []string{"src/*.go", "**"}, Deliverable: "d"}}
	if err := validatePartitionUnits(bad2); err == nil {
		t.Error("validatePartitionUnits with a bare-wildcard entry among valid ones must fail")
	}
	good := []Unit{{ID: "u1", Title: "t", Scope: []string{"src/*.go"}, Deliverable: "d"}}
	if err := validatePartitionUnits(good); err != nil {
		t.Errorf("validatePartitionUnits([src/*.go]) = %v; want nil", err)
	}
}

// TestParsePlan_OverlapProbeGlobalBudget is the H1 pin: a plan whose glob-vs-
// glob overlap probes exceed the aggregate probe-node budget shared across the
// whole ParsePlan overlap phase is rejected quickly with the conservative-
// overlap error (fail safe), not left to burn per-pair budget times O(units^2)
// probes. Mutation-verified: raising overlapProbeBudgetNodes makes the pin
// fail because every pair here is genuinely disjoint and parses clean.
func TestParsePlan_OverlapProbeGlobalBudget(t *testing.T) {
	// Each unit's scope component is one star-padded chain of 10 letters; the
	// letter sets are pairwise disjoint, so no common matched path exists
	// below the search depth (any common path needs at least 20 chars) and
	// every pair burns the full per-component search without pruning. A
	// handful of units therefore exhausts the shared budget deterministically.
	groups := []string{
		"abcdefghij", "klmnopqrst", "uvwxyzABCD", "EFGHIJKLMN",
		"OPQRSTUVWX", "YZ01234567", "89)!+-_=.~",
	}
	var units []string
	for i, g := range groups {
		units = append(units, `{"id":"u`+strconv.Itoa(i+1)+`","title":"t","scope":["src/*`+strings.Join(strings.Split(g, ""), "*")+`*"],"deliverable":"d"}`)
	}
	doc := []byte("[" + strings.Join(units, ",") + "]")

	start := time.Now()
	parsed, err := ParsePlan(doc)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a conservative budget-exhaustion rejection, got units %+v", parsed)
	}
	if len(parsed) != 0 {
		t.Errorf("expected no partial result, got %+v", parsed)
	}
	for _, want := range []string{"budget exhausted", "conservatively"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q", err, want)
		}
	}
	// The whole point of the budget: rejection is quick (the search is cut the
	// moment the shared budget is spent, not after all O(n^2) pair probes).
	// Generous bound; the unbudgeted path takes orders of magnitude longer.
	if elapsed > 10*time.Second {
		t.Errorf("budget-exhaustion rejection took %v; want a quick bounded rejection", elapsed)
	}
}

// TestParsePlan_DisjointGlobPlanUnderBudgetAccepted is the H1 control: the
// same aggregate phase still ACCEPTS a plan of many units whose globs are
// realistically bounded (each pair's probe is proven disjoint in a handful of
// nodes), proving the shared budget is not a hidden unit-count cap.
func TestParsePlan_DisjointGlobPlanUnderBudgetAccepted(t *testing.T) {
	var units []string
	for i := 0; i < 30; i++ {
		units = append(units, `{"id":"u`+strconv.Itoa(i+1)+`","title":"t","scope":["src/pkg`+strconv.Itoa(i)+`/*.go"],"deliverable":"d"}`)
	}
	doc := []byte("[" + strings.Join(units, ",") + "]")
	parsed, err := ParsePlan(doc)
	if err != nil {
		t.Fatalf("30-unit bounded-globs plan must stay accepted: %v", err)
	}
	if len(parsed) != 30 {
		t.Errorf("expected 30 units, got %d", len(parsed))
	}
}

// TestScopesOverlap_GlobCrossIntersection is the C3 pin: glob-vs-glob pairs
// that share a common matched path ("src/a*.go" vs "src/*b.go" both grant
// "src/ab.go") now report OVERLAP, while genuinely disjoint sibling globs
// ("src/a*.go" vs "src/b*.go") stay DISJOINT (no over-report). Mutation-
// verified.
func TestScopesOverlap_GlobCrossIntersection(t *testing.T) {
	overlap := []struct{ a, b string }{
		{"src/a*.go", "src/*b.go"},     // common path "src/ab.go"
		{"src/a?c.go", "src/*bc.go"},   // common path "src/abc.go"
		{"src/[a-c]*.go", "src/*b.go"}, // common path "src/ab.go"
		{"src/a*", "src/*b"},           // common path "src/ab"
	}
	for _, tc := range overlap {
		if !scopesOverlap(tc.a, tc.b) {
			t.Errorf("scopesOverlap(%q, %q) = false; want true (glob-vs-glob intersection)", tc.a, tc.b)
		}
		if !scopesOverlap(tc.b, tc.a) {
			t.Errorf("scopesOverlap(%q, %q) = false; want true (symmetry)", tc.b, tc.a)
		}
	}
	disjoint := []struct{ a, b string }{
		{"src/a*.go", "src/b*.go"},     // distinct sibling globs
		{"docs/x/*.md", "docs/y/*.md"}, // sibling dirs
		{"src/a[0-9].go", "src/b[0-9].go"},
	}
	for _, tc := range disjoint {
		if scopesOverlap(tc.a, tc.b) {
			t.Errorf("scopesOverlap(%q, %q) = true; want false (must not over-report)", tc.a, tc.b)
		}
		if scopesOverlap(tc.b, tc.a) {
			t.Errorf("scopesOverlap(%q, %q) = true; want false (symmetry)", tc.b, tc.a)
		}
	}
}

// TestParsePlan_GlobCrossOverlapRejected pins the C3 guard at plan level: a
// plan assigning two glob-vs-glob intersecting scopes to different units is
// rejected, and a plan with genuinely disjoint sibling globs is accepted.
func TestParsePlan_GlobCrossOverlapRejected(t *testing.T) {
	doc := []byte(`[
      {"id":"u1","title":"a","scope":["src/a*.go"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["src/*b.go"],"deliverable":"db"}
    ]`)
	units, err := ParsePlan(doc)
	if err == nil {
		t.Fatalf("expected a glob-cross-overlap error, got units %+v", units)
	}
	if !strings.Contains(err.Error(), "scope overlap") {
		t.Errorf("error %q must name the scope overlap", err)
	}

	ok := []byte(`[
      {"id":"u1","title":"a","scope":["src/a*.go"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["src/b*.go"],"deliverable":"db"}
    ]`)
	if units, err := ParsePlan(ok); err != nil || len(units) != 2 {
		t.Errorf("disjoint sibling globs must parse: units=%+v err=%v", units, err)
	}
}
