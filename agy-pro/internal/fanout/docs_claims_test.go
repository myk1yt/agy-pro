package fanout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
