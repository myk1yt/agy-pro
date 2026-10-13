package swarm

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// PresetNominal is the frozen seat count of preset id (contract §2.7):
// p1 = 2 native + 2 external (4), p2 = 3 external, p3 = 4 mixed. Returns 0 for
// anything that is not a preset id.
func PresetNominal(id string) int {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "p1":
		return 4
	case "p2":
		return 3
	case "p3":
		return 4
	}
	return 0
}

// IsSelectionInput reports whether a --seats value uses the numbered selection
// grammar (contract §2.7: numbers, ranges, preset ids) rather than raw fanout
// seat specs. Every token must start with a digit (number/range) or be exactly
// a preset id p1/p2/p3; anything else is a seat spec ("native:x", "prov/m",
// bare "m"). Mixed input is rejected by ResolveSpecsOrSelection.
func IsSelectionInput(input string) bool {
	tokens := splitSelTokens(input)
	if len(tokens) == 0 {
		return true // empty selection = default preset
	}
	for _, tok := range tokens {
		lower := strings.ToLower(tok)
		if lower == "p1" || lower == "p2" || lower == "p3" {
			continue
		}
		if tok[0] < '0' || tok[0] > '9' {
			return false
		}
	}
	return true
}

// splitSelTokens splits a selection line on the §2.7 delimiters ("," | " " | ";").
func splitSelTokens(input string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			tokens = append(tokens, t)
		}
		cur.Reset()
	}
	for _, r := range strings.TrimSpace(input) {
		switch r {
		case ',', ' ', ';':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// ResolveSeats resolves a numbered selection line against the combined
// inventory (contract §2.7) and layers the builder rules on top:
//
//   - explicit number/range picks of a k3 row are REJECTED (the user typed
//     the excluded entry on purpose - pool policy says no);
//   - presets never auto-pick k3 entries: any k3 row a preset expansion
//     produced is dropped with a warning (never auto-seated);
//   - a preset that finds fewer available entries than its frozen size
//     degrades to "all available" with a printed warning (never an error).
func ResolveSeats(input string, inv *inventory.Inventory, warnw io.Writer) ([]inventory.Entry, error) {
	if inv == nil {
		// F7: the nil-inventory error echoes the raw selection line; seed the
		// masker with it (providers.SeedEligible) so a pasted key in the
		// selection slot cannot survive the error text.
		return nil, fmt.Errorf("%s", providers.MaskSentence(fmt.Sprintf("invalid selection token %q", strings.TrimSpace(input)), input))
	}
	if err := rejectExplicitK3(input, inv); err != nil {
		return nil, err
	}
	entries, err := inventory.ParseSelection(input, inv)
	if err != nil {
		return nil, err
	}
	kept := entries[:0:0]
	for _, e := range entries {
		if inventory.IsK3Excluded(e.ModelID) {
			if warnw != nil {
				// F7: Spec/ModelID come from the user-editable registry; mask
				// key-shaped ones inside the warning (benign specs readable),
				// same rule as the builder's seat-list column.
				fmt.Fprintln(warnw, providers.MaskSentence(fmt.Sprintf("skipped seat %d (%s): %s", e.Number, e.Spec, k3Marker), e.Spec, e.ModelID))
			}
			continue
		}
		kept = append(kept, e)
	}
	if warnw != nil {
		expected := expectedSeats(input)
		if expected > len(kept) {
			fmt.Fprintf(warnw, "warning: preset degraded to all available (%d of %d seats); not enough eligible entries\n", len(kept), expected)
		}
	}
	return kept, nil
}

// rejectExplicitK3 rejects a selection whose explicit number/range tokens name
// a k3 row (contract DoD 3: "explicit typing of a k3 entry is rejected").
// Preset tokens are exempt here - they never auto-pick k3 (and any k3 row a
// preset still yields is dropped by ResolveSeats).
func rejectExplicitK3(input string, inv *inventory.Inventory) error {
	for _, tok := range splitSelTokens(input) {
		if PresetNominal(tok) > 0 {
			continue
		}
		nums, err := tokenNumbers(tok)
		if err != nil {
			continue // unparsable tokens surface from ParseSelection
		}
		for _, n := range nums {
			if n < 1 || n > len(inv.Entries) {
				continue // range errors surface from ParseSelection
			}
			e := inv.Entries[n-1]
			if inventory.IsK3Excluded(e.ModelID) {
				// F7: the rejection sentence embeds the registry-derived spec;
				// seed the masker with it so a key-shaped model ID cannot
				// survive the error (benign specs readable).
				return fmt.Errorf("%s", providers.MaskSentence(fmt.Sprintf("seat %d (%s) is %s: k3 models are excluded from swarm auto-seating; pick another entry", e.Number, e.Spec, k3Marker), e.Spec, e.ModelID))
			}
		}
	}
	return nil
}

// maxSelectionRange is the ceiling on how many seat numbers ONE range token
// may expand to (A8). The grammar already caps a whole selection at 40 seats
// (inventory.ParseSelection) and any seat number past the inventory length is
// out of range, so a wider range can never select anything: expanding it is
// pure memory-exhaustion surface. `1-2000000000` used to materialize two
// billion ints inside rejectExplicitK3 - before any bound check ran - which is
// a prompt-injected exec turning into an OOM kill.
const maxSelectionRange = 4096

// maxSeatNumber bounds every ENDPOINT of a range token (C4). Real inventories
// cap at 40 rows (inventory.ParseSelection), so a larger number can never
// select a seat - but accepting MaxInt64 endpoints made the A8 width check
// pass on an equal pair (`end-start` = 0) and then hung the expansion loop:
// `n++` past math.MaxInt64 wraps instead of exceeding `end`, so
// `9223372036854775807-9223372036854775807` appended forever. Comparing each
// endpoint against this bound BEFORE the `end-start` subtraction keeps both
// the width check and the loop overflow-free.
const maxSeatNumber = 2147483647 // math.MaxInt32

// tokenNumbers expands one number or range token to its seat numbers. Range
// expansion is bounded by maxSelectionRange BEFORE anything is allocated (A8)
// and both endpoints are bounded by maxSeatNumber before the subtraction (C4).
func tokenNumbers(tok string) ([]int, error) {
	if i := strings.IndexByte(tok, '-'); i >= 0 {
		start, err1 := strconv.Atoi(strings.TrimSpace(tok[:i]))
		end, err2 := strconv.Atoi(strings.TrimSpace(tok[i+1:]))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("invalid selection token %q", tok)
		}
		if start > end {
			return nil, fmt.Errorf("range %q is inverted", tok)
		}
		// C4: overflow-safe endpoint bound, checked BEFORE end-start. The
		// error text does not surface from rejectExplicitK3 (ParseSelection
		// reports the out-of-range seat to the user), so it only needs to be
		// distinct from the too-wide rejection.
		if start < 1 || end > maxSeatNumber {
			return nil, fmt.Errorf("range %q exceeds the seat-number bound (%d)", tok, maxSeatNumber)
		}
		// A8: reject (not truncate) an over-wide range. A truncated range
		// would silently select the wrong seats; an error names the problem.
		if end-start >= maxSelectionRange {
			return nil, fmt.Errorf("range %q is too wide: one range may expand at most %d seats", tok, maxSelectionRange)
		}
		out := make([]int, 0, end-start+1)
		for n := start; n <= end; n++ {
			out = append(out, n)
		}
		return out, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(tok))
	if err != nil {
		return nil, fmt.Errorf("invalid selection token %q", tok)
	}
	return []int{n}, nil
}

// WarnK3Specs flags explicit seat specs that name a k3 model. Explicit
// power-user/CI picks are honored as-is (pool policy governs AUTO-seating
// only), but the exclusion is loud.
func WarnK3Specs(entries []inventory.Entry, warnw io.Writer) {
	if warnw == nil {
		return
	}
	for i, e := range entries {
		if inventory.IsK3Excluded(e.ModelID) {
			// G6: e.Spec comes from the user-editable registry / pasted specs;
			// mask key-shaped ones (benign specs readable).
			fmt.Fprintln(warnw, providers.MaskSentence(fmt.Sprintf(
				"warning: seat %d (%s) is %s; explicit pick honored (pool policy covers auto-seating only)",
				i+1, e.Spec, k3Marker), e.Spec, e.ModelID))
		}
	}
}

// expectedSeats computes the nominal seat count of a selection line (sum of
// preset nominals plus explicit numbers/ranges). Only presets can under-deliver.
// Range endpoints outside the C4 seat-number bound count as a single token:
// ParseSelection rejects them, so they can never under-deliver a preset.
func expectedSeats(input string) int {
	total := 0
	for _, tok := range splitSelTokens(input) {
		if n := PresetNominal(tok); n > 0 {
			total += n
			continue
		}
		if i := strings.IndexByte(tok, '-'); i >= 0 {
			start, err1 := strconv.Atoi(strings.TrimSpace(tok[:i]))
			end, err2 := strconv.Atoi(strings.TrimSpace(tok[i+1:]))
			// C4: same endpoint bound as tokenNumbers, so the
			// `end-start+1` sum cannot overflow either.
			if err1 == nil && err2 == nil && end >= start && start >= 1 && end <= maxSeatNumber {
				total += end - start + 1
				continue
			}
		}
		total++ // single number (or unparsable token: ParseSelection errors first)
	}
	if total == 0 {
		// Empty input resolves the default preset (p1).
		return PresetNominal("p1")
	}
	return total
}

// EntriesFromSeatSpecs converts explicit fanout seat specs (the power-user/CI
// fallback spelling of --seats) into builder entries. Spec is the raw token so
// BuildFanoutArgs rejoins them byte-for-byte.
func EntriesFromSeatSpecs(specs []fanout.SeatSpec) []inventory.Entry {
	out := make([]inventory.Entry, 0, len(specs))
	for i, s := range specs {
		kind := "provider"
		if s.Kind == fanout.SeatKindNative {
			kind = "native"
		}
		out = append(out, inventory.Entry{
			Number:  i + 1,
			Kind:    kind,
			ModelID: s.ModelID,
			Spec:    s.Raw,
		})
	}
	return out
}
