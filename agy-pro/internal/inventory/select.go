package inventory

import (
	"fmt"
	"strconv"
	"strings"
)

// maxSeats caps a selection (contract §2.7).
const maxSeats = 40

// ParseSelection resolves a selection line against the inventory (contract
// §2.7 grammar):
//
//	selection   := sel-token ( ("," | " " | ";") sel-token )*
//	sel-token   := number | range | preset-id
//	number      := "1" .. "<len(Entries)>"
//	range       := number "-" number   (start <= end)
//	preset-id   := "p1" | "p2" | "p3"  (case-insensitive)
//	empty input := the presented default preset (p1)
//
// Errors: "invalid selection token %q", "seat number %d out of range (1-%d)",
// "selection exceeds 40 seats", "range %q is inverted".
func ParseSelection(input string, inv *Inventory) ([]Entry, error) {
	if inv == nil {
		return nil, fmt.Errorf("invalid selection token %q", strings.TrimSpace(input))
	}
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return resolvePreset("p1", inv)
	}

	var tokens []string
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			tokens = append(tokens, t)
		}
		cur.Reset()
	}
	for _, r := range trimmed {
		switch r {
		case ',', ' ', ';':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()

	var out []Entry
	for _, tok := range tokens {
		entries, err := resolveToken(tok, inv)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
		if len(out) > maxSeats {
			return nil, fmt.Errorf("selection exceeds %d seats", maxSeats)
		}
	}
	return out, nil
}

// resolveToken expands one selection token into entries.
func resolveToken(tok string, inv *Inventory) ([]Entry, error) {
	lower := strings.ToLower(tok)
	if strings.HasPrefix(lower, "p") {
		// Preset ids are exactly p1/p2/p3; any other p-token is invalid.
		switch lower {
		case "p1", "p2", "p3":
			return resolvePreset(lower, inv)
		default:
			return nil, fmt.Errorf("invalid selection token %q", tok)
		}
	}
	if i := strings.IndexByte(tok, '-'); i >= 0 {
		// Range: number "-" number.
		rest := strings.TrimSpace(tok[i+1:])
		if rest == "" {
			return nil, fmt.Errorf("invalid selection token %q", tok)
		}
		startNum, err1 := parseSeatNumber(tok[:i], tok, inv)
		endNum, err2 := parseSeatNumber(rest, tok, inv)
		if err1 != nil {
			return nil, err1
		}
		if err2 != nil {
			return nil, err2
		}
		if startNum > endNum {
			return nil, fmt.Errorf("range %q is inverted", tok)
		}
		var out []Entry
		for n := startNum; n <= endNum; n++ {
			out = append(out, inv.Entries[n-1])
			if len(out) > maxSeats {
				return nil, fmt.Errorf("selection exceeds %d seats", maxSeats)
			}
		}
		return out, nil
	}
	n, err := parseSeatNumber(tok, tok, inv)
	if err != nil {
		return nil, err
	}
	return []Entry{inv.Entries[n-1]}, nil
}

// parseSeatNumber validates one 1-based seat number against the inventory.
// Invalid shapes report the full user token (full) so error messages name
// what the user actually typed; out-of-range reports the seat number.
func parseSeatNumber(tok, full string, inv *Inventory) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(tok))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid selection token %q", full)
	}
	if n > len(inv.Entries) {
		return 0, fmt.Errorf("seat number %d out of range (1-%d)", n, len(inv.Entries))
	}
	return n, nil
}

// resolvePreset expands a frozen preset id (contract §2.7) against the live
// inventory. Fewer available entries than a preset needs degrades to "all
// available" - never an error (happy path must not dead-end).
func resolvePreset(id string, inv *Inventory) ([]Entry, error) {
	native := availableNative(inv)
	external := availableExternal(inv)

	switch strings.ToLower(id) {
	case "p1": // 2 native + 2 external (family-rotated)
		out := take(native, 2)
		return append(out, rotateFamilies(external, 2)...), nil
	case "p2": // 3 external (family-rotated)
		return rotateFamilies(external, 3), nil
	case "p3": // 1 native flash-tier + 1 native pro-tier + 2 family-rotated externals
		flash := firstMatching(native, func(e Entry) bool {
			return strings.Contains(strings.ToLower(e.ModelID), "flash")
		})
		pro := firstMatching(native, func(e Entry) bool {
			return strings.Contains(strings.ToLower(e.ModelID), "-pro")
		})
		if pro != nil && flash != nil && pro.Number == flash.Number {
			pro = nil
		}
		if pro == nil {
			// "else next native": the first native that is not the flash pick.
			for _, e := range native {
				if flash == nil || e.Number != flash.Number {
					pro = &e
					break
				}
			}
		}
		var out []Entry
		if flash != nil {
			out = append(out, *flash)
		}
		if pro != nil {
			out = append(out, *pro)
		}
		return append(out, rotateFamilies(external, 2)...), nil
	default:
		return nil, fmt.Errorf("invalid selection token %q", id)
	}
}

// availableNative lists usable native entries in render order.
func availableNative(inv *Inventory) []Entry {
	var out []Entry
	for _, e := range inv.Entries {
		if e.Kind == "native" {
			out = append(out, e)
		}
	}
	return out
}

// availableExternal lists usable provider entries: keyless-in-a-bad-way rows
// (KeyMissing) and k3-family rows are never auto-picked (pool policy).
func availableExternal(inv *Inventory) []Entry {
	var out []Entry
	for _, e := range inv.Entries {
		if e.Kind != "provider" {
			continue
		}
		if e.KeyMissing || IsK3Excluded(e.ModelID) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// rotateFamilies picks up to n provider entries with distinct families
// (same fresh-family preference as SelectTargets rotation), falling back to
// same-family fill so a single-family registry still yields a full preset.
func rotateFamilies(candidates []Entry, n int) []Entry {
	if n <= 0 || len(candidates) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []Entry
	for _, e := range candidates {
		fam := providersFamily(e.ModelID)
		if seen[fam] {
			continue
		}
		seen[fam] = true
		out = append(out, e)
		if len(out) == n {
			return out
		}
	}
	// Fill pass: allow family repeats rather than under-deliver.
	for _, e := range candidates {
		already := false
		for _, o := range out {
			if o.Number == e.Number {
				already = true
				break
			}
		}
		if already {
			continue
		}
		out = append(out, e)
		if len(out) == n {
			break
		}
	}
	return out
}

// providersFamily mirrors providers.Model.FamilyName() for an inventory entry
// (explicit family is not carried on Entry; infer from the id).
func providersFamily(modelID string) string {
	id := strings.ToLower(modelID)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	end := 0
	for end < len(id) && id[end] >= 'a' && id[end] <= 'z' {
		end++
	}
	if end == 0 {
		return id
	}
	return id[:end]
}

func take(entries []Entry, n int) []Entry {
	if n > len(entries) {
		n = len(entries)
	}
	return entries[:n]
}

func firstMatching(entries []Entry, match func(Entry) bool) *Entry {
	for i := range entries {
		if match(entries[i]) {
			return &entries[i]
		}
	}
	return nil
}
