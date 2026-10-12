package swarm

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
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
		return nil, fmt.Errorf("invalid selection token %q", strings.TrimSpace(input))
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
				fmt.Fprintf(warnw, "skipped seat %d (%s): %s\n", e.Number, e.Spec, k3Marker)
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
				return fmt.Errorf("seat %d (%s) is %s: k3 models are excluded from swarm auto-seating; pick another entry", e.Number, e.Spec, k3Marker)
			}
		}
	}
	return nil
}

// tokenNumbers expands one number or range token to its seat numbers.
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
		var out []int
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
			fmt.Fprintf(warnw, "warning: seat %d (%s) is %s; explicit pick honored (pool policy covers auto-seating only)\n", i+1, e.Spec, k3Marker)
		}
	}
}

// expectedSeats computes the nominal seat count of a selection line (sum of
// preset nominals plus explicit numbers/ranges). Only presets can under-deliver.
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
			if err1 == nil && err2 == nil && end >= start {
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
