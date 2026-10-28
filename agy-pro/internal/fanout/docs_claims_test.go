package fanout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// docWS collapses whitespace runs in a doc or a pin, so a pin survives a
// legitimate re-wrap of the sentence and still fails on a wording regression.
func docWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// docHasSentence reports whether want is present in doc modulo whitespace.
func docHasSentence(doc, want string) bool {
	return strings.Contains(docWS(doc), docWS(want))
}

// A13: docs/fanout.md overstated the artifact filename (`seat-<n>-<kind>-...`)
// and the quota classifier ("AGY_ERROR shows 429 ... triggers reason=quota").
// The wording is restored to the writer's actual behavior (provider name +
// model id, see formatSeatReport) and the real classifier (isQuotaExhausted).
// This test pins the corrected claims so the doc cannot drift back.
func TestFanoutDocMatchesImplementedBehavior(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "fanout.md"))
	if err != nil {
		t.Fatalf("read docs/fanout.md: %v", err)
	}
	doc := string(data)

	// Filename wording: provider name + model id, not <kind>.
	if !strings.Contains(doc, "seat-<n>-<provider>-<model>.md") {
		t.Error("fanout.md must document the writer's seat-<n>-<provider>-<model>.md naming")
	}
	if strings.Contains(doc, "seat-<n>-<kind>") {
		t.Error("fanout.md must not claim a <kind> filename segment (the writer uses the provider name)")
	}
	// Quota-classifier wording: AGY_ERROR alone is not a quota signal.
	if !strings.Contains(doc, "an `AGY_ERROR` line on its own does not trigger it") {
		t.Error("fanout.md must state that an AGY_ERROR line alone does not trigger reason=quota")
	}
	if strings.Contains(doc, "`AGY_ERROR` shows 429") {
		t.Error("fanout.md must not claim AGY_ERROR shows 429 (the classifier reads the stderr tail)")
	}
}

// A13 companion: the doc claim "an AGY_ERROR line on its own does not trigger
// reason=quota" must match the classifier itself (isQuotaExhausted reads the
// stderr tail for 429 / quota / resource_exhausted only).
func TestQuotaClassifierAgreesWithDocClaim(t *testing.T) {
	// AGY_ERROR alone is not a quota signal.
	if isQuotaExhausted("AGY_ERROR: something broke") {
		t.Error("AGY_ERROR alone must not classify as quota")
	}
	if isQuotaExhausted("AGY_ERROR: {\"status\":500}") {
		t.Error("AGY_ERROR with a non-429 status must not classify as quota")
	}
	// The documented markers still do.
	for _, tail := range []string{
		"429 too many requests",
		"quota exceeded",
		"resource_exhausted",
	} {
		if !isQuotaExhausted(tail) {
			t.Errorf("isQuotaExhausted(%q) = false; want true (documented marker)", tail)
		}
	}
}

// TestFanoutDocPinsExclusionRuleWording pins the exclusion-rule sentences of
// docs/fanout.md §4.1 to the behavior they describe (SESSION7 residual, round-2
// code seat P3 test-coverage: the corrected sentences carried no automated claim
// pin, so a wording regression could silently re-land). Each sentence is checked
// against the shipped code in the same test, and the loader clauses are exercised
// through the single entry authority, so a pin that no longer describes the code
// fails here rather than passing on a stale sentence.
func TestFanoutDocPinsExclusionRuleWording(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "fanout.md"))
	if err != nil {
		t.Fatalf("read docs/fanout.md: %v", err)
	}
	doc := string(data)

	// §4.1 loader rule. Behavior: providers.NormalizeExcludeModels ->
	// excludeEntries -> checkExcludeEntry (internal/providers/providers.go:566-586,
	// :635-657): a Cc or Cf rune in the RAW value is refused, the entry is
	// trimmed, interior whitespace is refused, the loader DROPS a blank entry,
	// and a non-nil list whose every entry is blank is refused as a whole.
	const loaderRule = "Entries are trimmed at load, an entry carrying interior whitespace, a control rune (Cc) or an invisible format rune (Cf) is refused, an individual entry that is empty after the trim is dropped, and a list whose every entry is blank is refused as a whole."
	if !docHasSentence(doc, loaderRule) {
		t.Errorf("docs/fanout.md §4.1 must document the loader rule verbatim (trimmed at load, blank entry dropped, interior whitespace and Cc/Cf refused, all-blank list refused as a whole); missing sentence:\n  %s", loaderRule)
	}
	trimmed, err := providers.NormalizeExcludeModels([]string{"  nw  ", "", "family:k3"})
	if err != nil || len(trimmed) != 2 || trimmed[0] != "nw" || trimmed[1] != "family:k3" {
		t.Errorf("pinned sentence drifted from the loader: trim+drop of [\"  nw  \" \"\" \"family:k3\"] = %#v, %v; want [nw family:k3] and no error", trimmed, err)
	}
	for _, bad := range [][]string{
		{"nw glm"},   // interior whitespace
		{"nw\x00"},   // Cc (control rune)
		{"nw\u202e"}, // Cf (right-to-left override)
		{"", "   "},  // every entry blank -> refused as a whole
	} {
		if _, err := providers.NormalizeExcludeModels(bad); err == nil {
			t.Errorf("pinned sentence drifted from the loader: NormalizeExcludeModels(%q) accepted an entry shape the doc says is refused", bad)
		}
	}

	// §4.1 warning: ONE shared tail under two heads. fanout's own sentence is the
	// excludedPickWarning const (internal/fanout/selector.go:37); the swarm seat
	// form is excludedPickSentence (internal/swarm/select.go:388).
	const sharedTail = "is excluded (exclusion list); explicit pick honored (the exclusion list covers auto-seating and failover only)"
	if !docHasSentence(doc, sharedTail) {
		t.Errorf("docs/fanout.md must carry the shared warning tail %q", sharedTail)
	}
	for _, head := range []string{
		"`warning: <provider/model> is excluded ...`",
		"`warning: seat <n> (<spec>) is excluded ...`",
	} {
		if !docHasSentence(doc, head) {
			t.Errorf("docs/fanout.md must document both warning heads; missing %s", head)
		}
	}
	if want := "warning: %s " + sharedTail; excludedPickWarning != want {
		t.Errorf("excludedPickWarning drifted from the pinned heads+tail: got %q want %q", excludedPickWarning, want)
	}

	// §4.1 precision (SESSION7 security trio LOW): "Nothing is rejected because
	// of the list" was contradicted twelve lines above by the all-excluded hard
	// failure. The precise rule: nothing an EXPLICIT pick names is rejected; a
	// pool whose every auto-seat candidate is excluded still fails.
	const explicitOnly = "Nothing an EXPLICIT pick names is rejected: the warning line is the only consequence of picking an excluded entry."
	if !docHasSentence(doc, explicitOnly) {
		t.Errorf("docs/fanout.md must scope the non-rejection rule to explicit picks; missing sentence:\n  %s", explicitOnly)
	}
	const allExcluded = "a run whose every auto-seat candidate is excluded errors out with `no models available for auto-seating: every candidate is excluded by the exclusion list`"
	if !docHasSentence(doc, allExcluded) {
		t.Errorf("docs/fanout.md must name the all-excluded hard failure next to the non-rejection rule; missing sentence:\n  %s", allExcluded)
	}
	if docHasSentence(doc, "Nothing is rejected because of the list") {
		t.Error("docs/fanout.md must not claim that nothing is rejected: a pool whose every candidate is excluded is the named hard failure (§4.1)")
	}

	// The failure the sentence names, produced by the code path it names
	// (internal/fanout/selector.go:123).
	trueVal := true
	t.Setenv("KEY_DOCPIN_ALL_EXCLUDED", "k")
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		BaseURL:   "https://example.invalid/v1",
		APIKeyEnv: "KEY_DOCPIN_ALL_EXCLUDED",
		Models:    []providers.Model{{ID: "glm-5.3-pro", Family: "glm", Enabled: &trueVal}},
	}}}
	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"nw"}})
	_, selErr := SelectTargets(reg, nil, 3, nil, PolicyExcluder(policy), nil)
	if selErr == nil {
		t.Fatal("premise broken: an all-excluded pool must fail; the pinned sentence depends on it")
	}
	if !strings.Contains(selErr.Error(), "no models available for auto-seating: every candidate is excluded by the exclusion list") {
		t.Errorf("all-excluded error drifted from the documented sentence: %v", selErr)
	}

	// Non-vacuity: the predicate rejects the pre-fix wording in memory, so the
	// pin fails when the sentence regresses instead of matching anything.
	regressed := strings.Replace(doc, "Nothing an EXPLICIT pick names is rejected", "Nothing is rejected because of the list", 1)
	if docHasSentence(regressed, explicitOnly) {
		t.Error("pin is vacuous: it still matches after the sentence regresses to the pre-fix wording")
	}
}
