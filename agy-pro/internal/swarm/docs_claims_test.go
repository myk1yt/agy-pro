package swarm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swarmDocWS collapses whitespace runs so a pin survives a legitimate re-wrap of
// the sentence and still fails on a wording regression.
func swarmDocWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// swarmDocHasSentence reports whether want is present in doc modulo whitespace.
func swarmDocHasSentence(doc, want string) bool {
	return strings.Contains(swarmDocWS(doc), swarmDocWS(want))
}

// A13: docs/swarm-hybrid.md claimed "other platforms never echo the raw line",
// which contradicts providers/secretinput_other.go (no raw-console
// implementation; the wizard falls back to an ordinary line read and cannot
// mask the echo). The doc also omitted that `models --json` drops NativeErr.
// Both claims are pinned here so the docs cannot drift back.
func TestSwarmHybridDocMatchesImplementedBehavior(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "swarm-hybrid.md"))
	if err != nil {
		t.Fatalf("read docs/swarm-hybrid.md: %v", err)
	}
	doc := string(data)

	// The refuted echo claim must be gone; the honest fallback must be stated.
	if strings.Contains(doc, "never echo the raw line") {
		t.Error("swarm-hybrid.md must not claim other platforms never echo the raw line")
	}
	if !strings.Contains(doc, "falls back to an ordinary line read") {
		t.Error("swarm-hybrid.md must state the non-Windows fallback (ordinary line read)")
	}
	if !strings.Contains(doc, "cannot mask the echo") {
		t.Error("swarm-hybrid.md must state that the non-Windows path cannot mask the echo")
	}

	// `models --json` drops the native-fetch error: the degrade must be documented.
	if !strings.Contains(doc, "`models --json` dumps the entry rows only and does NOT carry the native-fetch error") {
		t.Error("swarm-hybrid.md must document that models --json omits NativeErr")
	}
	if !strings.Contains(doc, "agy pro models --all") {
		t.Error("swarm-hybrid.md must point to `agy pro models --all` as the honest-degrade surface")
	}
}

// TestSwarmHybridDocPinsPresetRefusalAndSeatWarning pins the two corrected
// sentence sites of docs/swarm-hybrid.md (SESSION7 residual, round-2 code seat P3
// test-coverage) to the builder they describe: an unknown name at the seat step is
// refused and the step RE-PROMPTS, exit 2 happens only when stdin then closes, and
// the swarm seat warning carries the seat head over the shared tail.
func TestSwarmHybridDocPinsPresetRefusalAndSeatWarning(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "swarm-hybrid.md"))
	if err != nil {
		t.Fatalf("read docs/swarm-hybrid.md: %v", err)
	}
	doc := string(data)

	// Site 1 (§2.1, around :199-201) and site 2 (§2.2, around :217-221) state the
	// same rule twice, deliberately (non-interactive reader and builder reader).
	const stepSentenceA = "Typed at the interactive seat step, an unknown name-shaped token is refused with `unknown saved preset`: the builder prints that refusal and prompts for seats again. Only a valid selection ends the seat step; exit 2 happens only when stdin closes at the prompt."
	const stepSentenceB = "At the seat step an unknown name-shaped token is refused with `unknown saved preset` and the builder re-prompts; only a valid selection ends the step, and exit 2 happens only if stdin closes at the prompt."
	for _, s := range []string{stepSentenceA, stepSentenceB} {
		if !swarmDocHasSentence(doc, s) {
			t.Errorf("docs/swarm-hybrid.md must keep the corrected seat-step sentence; missing:\n  %s", s)
		}
	}

	// The refusal itself (internal/swarm/select.go:169).
	if _, err := ExpandSavedPresetNames("nosuch", nil); err == nil || !strings.Contains(err.Error(), "unknown saved preset") {
		t.Errorf("ExpandSavedPresetNames(\"nosuch\") = %v; want an `unknown saved preset` refusal", err)
	}

	// The expansion the same paragraph promises: a saved name resolves to its
	// stored Numbers, and those Numbers then count as EXPLICIT picks
	// (lookupSavedPreset :109-116, ResolveSeatsWith :250-263 explicitSeatNumbers).
	const expands = "A saved name expands to the numbered selection it stores."
	const explicitNumbers = "The numbers a saved preset expands to count as EXPLICIT picks, so an excluded entry it names is honored with the warning (§2.3)."
	for _, s := range []string{expands, explicitNumbers} {
		if !swarmDocHasSentence(doc, s) {
			t.Errorf("docs/swarm-hybrid.md must keep the saved-name expansion sentence; missing:\n  %s", s)
		}
	}
	expanded, err := ExpandSavedPresetNames("deep", []SavedPreset{{Name: "deep", Numbers: "1,3"}})
	if err != nil || expanded != "1,3" {
		t.Errorf("ExpandSavedPresetNames(\"deep\") = %q, %v; want \"1,3\" (the preset's stored Numbers)", expanded, err)
	}
	if nums := explicitSeatNumbers(expanded); !nums[1] || !nums[3] {
		t.Errorf("explicitSeatNumbers(%q) = %v; the expanded numbers must count as explicit picks", expanded, nums)
	}

	// Re-prompt, not exit: an unknown name followed by a valid selection reaches
	// the preview step and exits 0 (builder.go:264-275 `continue`, :281-285
	// `return seats, true`).
	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader("1\nnosuch\n2\nq\n"), Out: &out, ErrW: &errOut, Inv: namesInventory(t)}
	if code := b.Run(); code != 0 {
		t.Fatalf("builder exit = %d after one refused seat line; want 0 (the step re-prompts)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "unknown saved preset") {
		t.Errorf("the refusal was not printed at the seat step:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), "Selected seats:") {
		t.Errorf("the step did not end on the valid selection that followed the refusal:\n%s", out.String())
	}

	// Exit 2 happens only when stdin then closes (builder.go:255-258 -> false,
	// Run :142-145 -> 2).
	var out2, errOut2 bytes.Buffer
	b2 := &Builder{In: strings.NewReader("1\nnosuch\n"), Out: &out2, ErrW: &errOut2, Inv: namesInventory(t)}
	if code := b2.Run(); code != 2 {
		t.Fatalf("builder exit = %d when stdin closes at the seat prompt; want 2\nstdout:\n%s\nstderr:\n%s", code, out2.String(), errOut2.String())
	}
	if !strings.Contains(errOut2.String(), "input closed during seat selection") {
		t.Errorf("the exit-2 path is not the closed-stdin path:\n%s", errOut2.String())
	}

	// §2.3 warning: the swarm seat head over the shared tail
	// (internal/swarm/select.go:388 excludedPickSentence), and fanout's bare-target
	// head documented with the same tail.
	const seatHead = "`warning: seat <n> (<spec>) is excluded (exclusion list); explicit pick honored (the exclusion list covers auto-seating and failover only)`"
	const fanoutHead = "`warning: <provider/model> is excluded (exclusion list); ...`"
	for _, s := range []string{seatHead, fanoutHead} {
		if !swarmDocHasSentence(doc, s) {
			t.Errorf("docs/swarm-hybrid.md §2.3 must document both warning heads; missing:\n  %s", s)
		}
	}
	const wantSentence = "warning: seat 7 (native:x) is excluded (exclusion list); explicit pick honored (the exclusion list covers auto-seating and failover only)"
	if got := excludedPickSentence(7, "native:x"); got != wantSentence {
		t.Errorf("excludedPickSentence drifted from the documented seat head+tail:\n got %q\nwant %q", got, wantSentence)
	}

	// Non-vacuity: the predicate rejects a regressed copy in memory, so the pin
	// fails when the re-prompt sentence regresses instead of matching anything.
	regressed := strings.Replace(doc, "and the builder re-prompts", "and the builder exits 2", 1)
	if swarmDocHasSentence(regressed, stepSentenceB) {
		t.Error("pin is vacuous: it still matches after the re-prompt sentence regresses")
	}
}
