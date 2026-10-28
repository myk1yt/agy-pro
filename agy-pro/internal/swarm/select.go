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
//
// With no saved presets this is the pre-F04 grammar byte for byte: an unknown
// name is NOT selection input, so it stays on the documented seat-spec path (a
// bare provider model id is a valid spec and must not be hijacked). Callers
// that hold the saved-preset store use IsSelectionInputWith.
func IsSelectionInput(input string) bool {
	return IsSelectionInputWith(input, nil)
}

// IsSelectionInputWith is IsSelectionInput plus the saved-preset name namespace
// (F04): a token that names a saved preset makes the line selection input, so
// `--seats deep` resolves the preset instead of becoming a provider seat spec.
//
// Precedence is fixed and unchanged where it already existed: p1/p2/p3 stay
// preset ids, and a digit-leading token stays a number/range, so a saved name
// spelled like either of those never wins (internal/tui's validPresetName
// already refuses to save those spellings; this is the defensive half). An
// UNKNOWN name is deliberately not selection input here - the named refusal
// belongs to ResolveSeatsWith, which is only reached on a line already known to
// be a selection, so a typo in a genuine seat spec ("native:x") is untouched.
func IsSelectionInputWith(input string, presets []SavedPreset) bool {
	tokens := splitSelTokens(input)
	if len(tokens) == 0 {
		return true // empty selection = default preset
	}
	for _, tok := range tokens {
		if isSelectionSyntaxToken(tok) {
			continue
		}
		if _, ok := lookupSavedPreset(presets, tok); ok {
			continue
		}
		return false
	}
	return true
}

// isPresetIDToken reports whether tok is one of the three frozen preset ids.
func isPresetIDToken(tok string) bool {
	switch strings.ToLower(tok) {
	case "p1", "p2", "p3":
		return true
	}
	return false
}

// isSelectionSyntaxToken reports whether tok is already spelled in the §2.7
// selection syntax: a preset id, or a token that starts with a digit. A
// malformed digit-leading token ("1-3x") still counts - ParseSelection reports
// it, which is the pre-F04 behavior.
func isSelectionSyntaxToken(tok string) bool {
	if tok == "" {
		return false
	}
	return isPresetIDToken(tok) || (tok[0] >= '0' && tok[0] <= '9')
}

// savedPresetNameShape reports whether tok is spelled like a saved-preset name
// in the selection grammar. internal/tui's validPresetName owns what may be
// SAVED (letters, digits, '-' and '_' plus no separators, never a bare number
// or p1/p2/p3); this is the typed-token half of the same namespace: a
// seat-spec spelling ("native:id", "prov/model", "./path") carries ':' or '/'
// and keeps routing to the spec path, so a spec is never mistaken for a
// mistyped preset name. Digit-leading tokens are excluded by
// isSelectionSyntaxToken, so numbers and ranges keep their reading.
func savedPresetNameShape(tok string) bool {
	if isSelectionSyntaxToken(tok) {
		return false
	}
	return !strings.ContainsAny(tok, ":/")
}

// lookupSavedPreset returns the stored Numbers of the saved preset whose name
// equals tok case-insensitively (the settings Core verbs' precedent:
// indexPresetFold in internal/tui/screen_settings.go).
func lookupSavedPreset(presets []SavedPreset, tok string) (string, bool) {
	for _, p := range presets {
		if p.Name != "" && strings.EqualFold(p.Name, tok) {
			return p.Numbers, true
		}
	}
	return "", false
}

// SelectionGrammarHint is the accepted-grammar sentence every selection refusal
// ends with, on every surface (the swarm builder, the --seats flag, the
// launcher model picker), so the three copies of the grammar cannot drift.
// invSize > 0 names the inventory bound (the picker renders it); 0 renders the
// bound-free form.
func SelectionGrammarHint(invSize int) string {
	if invSize > 0 {
		return fmt.Sprintf("want a number 1-%d, a range like 3-5, p1/p2/p3, or a saved preset name", invSize)
	}
	return "want a number, a range like 3-5, p1/p2/p3, or a saved preset name"
}

// ExpandSavedPresetNames rewrites a selection line so every token that names a
// saved preset becomes that preset's stored Numbers (F04). Preset ids and
// numbers/ranges pass through byte for byte, and a seat-spec-shaped token also
// passes through so ParseSelection reports it exactly as before.
//
// Two refusals are NAMED, never silent (both keep the "invalid selection token"
// prefix of the existing §2.7 refusals, so every selection surface renders the
// same sentence family):
//   - a name-shaped token that names no saved preset ("unknown saved preset"
//     plus the accepted grammar): a typo must not fall back to another meaning;
//   - a saved preset whose stored Numbers is empty: a broken preset must not
//     resolve to nothing.
//
// The quoted token is a seat/model TOKEN (a number, a range, a preset id, a
// saved-preset name or a raw seat spec), so it renders through the REGISTRY-SLOT
// authority (fanout.MaskSeatToken). providers.MaskOperand - which the seeded
// sentence pass reduces to on its own - is provably the identity on the
// underscore-head credential class, and the seeded pass is SeedEligible-gated, so
// neither could close it (r4 H-2). The seeded sentence pass still runs over the
// finished sentence as the depth arm for the decorated-compound class.
func ExpandSavedPresetNames(input string, presets []SavedPreset) (string, error) {
	tokens := splitSelTokens(input)
	if len(tokens) == 0 {
		return strings.TrimSpace(input), nil
	}
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if isSelectionSyntaxToken(tok) {
			out = append(out, tok)
			continue
		}
		if nums, ok := lookupSavedPreset(presets, tok); ok {
			if strings.TrimSpace(nums) == "" {
				// R4-class: a selection token can be a seat/model TOKEN, so it takes
				// the registry-slot authority (MaskOperand is identity on the
				// underscore-head credential class).
				return "", fmt.Errorf("invalid selection token %q: the saved preset has no seat numbers; edit it in Settings > Saved Seat Presets", fanout.MaskSeatToken(tok))
			}
			out = append(out, nums)
			continue
		}
		if savedPresetNameShape(tok) {
			return "", fmt.Errorf("invalid selection token %q: unknown saved preset (%s)", fanout.MaskSeatToken(tok), SelectionGrammarHint(0))
		}
		out = append(out, tok)
	}
	return strings.Join(out, ","), nil
}

// SelectionSeeds returns the mask seeds for a selection line: the whole line,
// its §2.7 tokens, and the stored Numbers behind every token that names a saved
// preset. The expansion means an inventory/parse error quotes the Numbers text
// rather than what the user typed, so the Numbers value has to be seedable at
// the call sites exactly like the typed line already is.
func SelectionSeeds(input string, presets []SavedPreset) []string {
	tokens := splitSelTokens(input)
	seeds := make([]string, 0, len(tokens)*2+1)
	seeds = append(seeds, input)
	seeds = append(seeds, tokens...)
	for _, tok := range tokens {
		if nums, ok := lookupSavedPreset(presets, tok); ok {
			seeds = append(seeds, nums)
			seeds = append(seeds, splitSelTokens(nums)...)
		}
	}
	return seeds
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
//   - an explicit number/range pick of an excluded row is HONORED with the
//     shared warning sentence (the user named it on purpose; the exclusion
//     list governs AUTO-seating and failover only);
//   - presets never auto-pick an excluded entry: any excluded row an auto-pick
//     produced is dropped with a warning (never auto-seated); a row the same
//     line also named EXPLICITLY is reported once, by the honored-pick warning,
//     with the reason that actually applied to it;
//   - a preset that finds fewer available entries than its frozen size
//     degrades to "all available" with a printed warning (never an error).
//
// Nothing on this path REJECTS a selection because of the exclusion list
// (user decision D2 = b). Saved preset names are not resolved here (no store is
// in scope); callers that hold one use ResolveSeatsWith.
func ResolveSeats(input string, inv *inventory.Inventory, warnw io.Writer) ([]inventory.Entry, error) {
	return ResolveSeatsWith(input, inv, nil, warnw)
}

// ResolveSeatsWith is ResolveSeats plus the F04 saved-preset names: every token
// that names a saved preset is replaced by that preset's stored Numbers before
// the §2.7 rules run, so a saved preset resolves to exactly the seats its
// Numbers select and nothing else. The expanded Numbers count as EXPLICIT picks
// (an excluded row a saved preset names is honored with the shared warning, not
// dropped), and the degrade count is the preset's own seat count, so a short
// inventory still warns instead of silently seating fewer models.
func ResolveSeatsWith(input string, inv *inventory.Inventory, presets []SavedPreset, warnw io.Writer) ([]inventory.Entry, error) {
	if inv == nil {
		// F7: the nil-inventory error echoes the raw selection line; seed the
		// masker with it (providers.SeedEligible) so a pasted key in the
		// selection slot cannot survive the error text.
		return nil, fmt.Errorf("%s", providers.MaskSentence(fmt.Sprintf("invalid selection token %q", fanout.MaskSeatToken(strings.TrimSpace(input))), input))
	}
	line, err := ExpandSavedPresetNames(input, presets)
	if err != nil {
		return nil, err
	}
	entries, err := inventory.ParseSelection(line, inv)
	if err != nil {
		return nil, err
	}
	explicit := explicitSeatNumbers(line)
	kept := applyExclusionPolicy(entries, explicit, warnw)
	if warnw != nil {
		warnSkippedPresetRows(line, inv, explicit, warnw)
		expected := expectedSeats(line)
		if expected > len(kept) {
			fmt.Fprintf(warnw, "warning: preset degraded to all available (%d of %d seats); not enough eligible entries\n", len(kept), expected)
		}
	}
	return kept, nil
}

// warnSkippedPresetRows names every row a preset token would have auto-seated
// but the exclusion policy took (D2 = b: auto-seating SKIPS, and the skip is
// loud). The inventory's preset expansion already drops excluded rows, so the
// dropped ones cannot be read from its output; this re-runs the SAME inventory
// preset algorithm over a copy whose exclusion flag is cleared and reports the
// rows that are excluded in the real snapshot. That way the preset algorithm
// stays the one inventory owns - no second copy can drift - and a preset that
// silently shrank is impossible.
//
// explicit carries the seat numbers the line named EXPLICITLY. Those rows are
// already reported once by the honored-pick warning in applyExclusionPolicy, so
// the preset report skips them: a mixed line ("p2,7") names one kept row once,
// with the reason that actually applied to it (S7-C8). Every other excluded row
// the preset would have auto-seated is still reported here, so a pure preset
// line loses nothing.
func warnSkippedPresetRows(line string, inv *inventory.Inventory, explicit map[int]bool, warnw io.Writer) {
	if inv == nil || warnw == nil || len(inv.Entries) == 0 {
		return
	}
	var open *inventory.Inventory
	reported := make(map[int]bool)
	for _, tok := range splitSelTokens(line) {
		if PresetNominal(tok) <= 0 {
			continue
		}
		if open == nil {
			entries := make([]inventory.Entry, len(inv.Entries))
			copy(entries, inv.Entries)
			for i := range entries {
				entries[i].Excluded = false
			}
			open = &inventory.Inventory{Entries: entries, NativeErr: inv.NativeErr, GeneratedAt: inv.GeneratedAt}
		}
		rows, err := inventory.ParseSelection(tok, open)
		if err != nil {
			continue // preset-token errors surface from ParseSelection on the real inventory
		}
		for _, r := range rows {
			if r.Number < 1 || r.Number > len(inv.Entries) {
				continue
			}
			e := inv.Entries[r.Number-1]
			if !e.Excluded || reported[e.Number] || explicit[e.Number] {
				continue
			}
			reported[e.Number] = true
			warnSkippedSeat(warnw, e)
		}
	}
}

// warnSkippedSeat prints the auto-pick skip sentence for one excluded row. Both
// layers that can drop an auto-seated row (the inventory preset expansion and
// this package's defensive filter) render it, so the wording lives in one place.
// F7: Spec/ModelID come from the user-editable registry, so the sentence routes
// through the seeded masker like every sibling warning (key-shaped operands
// masked, benign specs readable).
func warnSkippedSeat(warnw io.Writer, e inventory.Entry) {
	if warnw == nil {
		return
	}
	fmt.Fprintln(warnw, providers.MaskSentence(fmt.Sprintf("skipped seat %d (%s): %s", e.Number, fanout.MaskSeatToken(e.Spec), providers.ExcludedMarker()), e.Spec, e.ModelID))
}

// applyExclusionPolicy is the D2 = b rule where the swarm holds both halves of
// it. An excluded row the user named EXPLICITLY is KEPT and warned about (the
// pick is honored); an excluded row an auto-pick produced is SKIPPED with a
// warning (preset expansion never auto-seats an excluded row). No row is ever
// rejected. The skip arm is defensive on top of inventory's own preset filter
// (skipExcluded in internal/inventory/select.go): whichever layer drops the
// row, the rule holds, and the warning names it instead of silently shrinking
// the preset.
func applyExclusionPolicy(entries []inventory.Entry, explicit map[int]bool, warnw io.Writer) []inventory.Entry {
	kept := entries[:0:0]
	for _, e := range entries {
		if !e.Excluded {
			kept = append(kept, e)
			continue
		}
		if explicit[e.Number] {
			WarnExcludedSeat(warnw, e.Number, e.Spec, e.ModelID)
			kept = append(kept, e)
			continue
		}
		warnSkippedSeat(warnw, e)
	}
	return kept
}

// explicitSeatNumbers returns the seat numbers the selection line names
// EXPLICITLY: the numbers and ranges of number/range tokens. A preset token
// names no seat number (its expansion is auto-seating), and a saved-preset name
// has already been rewritten to its stored Numbers by ExpandSavedPresetNames,
// so a user who named a saved preset gets its seats honored.
func explicitSeatNumbers(input string) map[int]bool {
	out := make(map[int]bool)
	for _, tok := range splitSelTokens(input) {
		if PresetNominal(tok) > 0 {
			continue
		}
		nums, err := tokenNumbers(tok)
		if err != nil {
			continue // unparsable/out-of-bound tokens surface from ParseSelection
		}
		for _, n := range nums {
			out[n] = true
		}
	}
	return out
}

// excludedPickSentence is the ONE sentence every surface prints when an
// explicit pick names an excluded row (D2 = b): the numbered-pick warning and
// the explicit-spec warning render it verbatim, so the two subsystems read the
// same sentence and cannot drift.
func excludedPickSentence(seat int, spec string) string {
	// R4-class: spec is a seat/model TOKEN (typed --seats value or a registry row),
	// so it renders through the registry-slot authority; the seeded MaskSentence
	// pass at the call site is SeedEligible-gated and cannot close this class.
	return fmt.Sprintf("warning: seat %d (%s) is %s; explicit pick honored (the exclusion list covers auto-seating and failover only)", seat, fanout.MaskSeatToken(spec), providers.ExcludedMarker())
}

// WarnExcludedSeat prints the shared explicit-pick warning for one seat. spec
// and modelID come from the user-editable registry (or the pasted --seats line):
// the SPEC renders through the registry-slot authority inside
// excludedPickSentence (that is what masks the underscore-head credential class),
// and the seeded sentence pass here is the depth arm for the model id. Benign
// specs stay readable.
func WarnExcludedSeat(warnw io.Writer, seat int, spec, modelID string) {
	if warnw == nil {
		return
	}
	// R4-class: the sentence body already carries the spec through the
	// registry-slot authority (see excludedPickSentence); the seeded pass stays as
	// the depth arm for the model id.
	fmt.Fprintln(warnw, providers.MaskSentence(excludedPickSentence(seat, spec), modelID))
}

// maxSelectionRange is the ceiling on how many seat numbers ONE range token
// may expand to (A8). The grammar already caps a whole selection at 40 seats
// (inventory.ParseSelection) and any seat number past the inventory length is
// out of range, so a wider range can never select anything: expanding it is
// pure memory-exhaustion surface. `1-2000000000` used to materialize two
// billion ints inside the explicit-pick scan - before any bound check ran -
// which is a prompt-injected exec turning into an OOM kill.
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
		// error text does not surface from explicitSeatNumbers (ParseSelection
		// reports the out-of-range seat to the user), so it only needs to be
		// distinct from the too-wide refusal.
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

// WarnExcludedSpecs flags explicit seat specs that name an excluded row.
// Explicit power-user/CI picks are honored as-is (the exclusion list governs
// AUTO-seating and failover only), but the exclusion is loud, and it is the
// SAME sentence the numbered-pick warning prints (excludedPickSentence).
func WarnExcludedSpecs(entries []inventory.Entry, warnw io.Writer) {
	if warnw == nil {
		return
	}
	for i, e := range entries {
		if !e.Excluded {
			continue
		}
		seat := e.Number
		if seat <= 0 {
			seat = i + 1
		}
		WarnExcludedSeat(warnw, seat, e.Spec, e.ModelID)
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

// loadSeatSpecRegistry is the declared-family seam of the explicit seat-spec
// path (P4-4b). That path never loads the inventory, so with no registry the
// exclusion verdict could only see the family INFERRED from the model id: a
// model whose exclusion comes from a registry-DECLARED family ("k3" on a benign
// id) carried no Entry.Excluded, so the D2 warning never printed, while the seat
// list flagged the very same model. Reading the registry here gives the spec
// path the same three operands inventory.Load uses (provider name, bare model
// id, Model.FamilyName), so the two surfaces cannot disagree about one model.
//
// It is a var, like RunFanout, because tests must not read the live
// ~/.agy-pro/providers.json: they replace this seam or point providers.Load at a
// temp AGY_PRO_HOME. A registry that cannot be read degrades to nil, which is
// the pre-P4-4b behavior (id-prefix inference only), so the spec path keeps
// working without a providers.json. cmd/agy's loadSwarmPolicy seam already reads
// this same file for the policy; the single-load shape threads that registry
// into EntriesFromSeatSpecsWithRegistry, which needs a cmd-owned file, so the
// swarm-owned seam is the route that stays inside this package.
var loadSeatSpecRegistry = func() *providers.Registry {
	reg, err := providers.Load()
	if err != nil || reg == nil {
		return nil
	}
	return reg
}

// EntriesFromSeatSpecs converts explicit fanout seat specs (the power-user/CI
// fallback spelling of --seats) into builder entries. Spec is the raw token so
// BuildFanoutArgs rejoins them byte-for-byte.
//
// policy is the ONE exclusion authority (providers.ExclusionPolicy), threaded
// in because this path never loads the inventory: a hand-built row must carry
// the same Entry.Excluded verdict an inventory row gets at Load, or the
// explicit-spec warning could disagree with the seat list about the same model.
// The operands are the same three inventory.Load passes - provider name (with
// "native" for a native row), bare model id, family - derived through
// providers.ParseModelSpec plus the registry's own family for the matching model
// (loadSeatSpecRegistry), which is the resolution fanout performs later at
// execution time.
func EntriesFromSeatSpecs(specs []fanout.SeatSpec, policy providers.ExclusionPolicy) []inventory.Entry {
	return EntriesFromSeatSpecsWithRegistry(specs, policy, loadSeatSpecRegistry())
}

// EntriesFromSeatSpecsWithRegistry is EntriesFromSeatSpecs with the registry
// supplied by the caller, so the family operand can be the REGISTRY-DECLARED
// family instead of the id-prefix inference (P4-4b). A nil registry, a native
// seat, and a spec the registry does not hold all keep the inference, which is
// the pre-P4-4b behavior byte for byte.
func EntriesFromSeatSpecsWithRegistry(specs []fanout.SeatSpec, policy providers.ExclusionPolicy, reg *providers.Registry) []inventory.Entry {
	out := make([]inventory.Entry, 0, len(specs))
	for i, s := range specs {
		kind := "provider"
		providerName, modelID := "", s.ModelID
		if s.Kind == fanout.SeatKindNative {
			kind = "native"
			providerName = "native"
		} else {
			// A provider spec is "provider/model", "model (provider)" or a bare
			// model id; ParseModelSpec is the registry's own reader for all
			// three spellings, so the exclusion operands match FindTarget's.
			base, prov := providers.ParseModelSpec(s.ModelID)
			providerName, modelID = prov, base
		}
		family := providers.InferFamily(modelID)
		// A native seat is not a registry model, so the declared-family lookup
		// stays provider-only (the native catalog carries no family).
		if kind != "native" {
			if m, ok := findRegistryModel(reg, providerName, modelID); ok {
				family = registryDeclaredFamily(m)
			}
		}
		out = append(out, inventory.Entry{
			Number:  i + 1,
			Kind:    kind,
			ModelID: s.ModelID,
			Spec:    s.Raw,
			Family:  family,
			// The one verdict, evaluated exactly as inventory.Load does.
			Excluded: policy.Excludes(providerName, modelID, family),
		})
	}
	return out
}

// findRegistryModel returns the registry record for the model a seat spec names,
// preferring the provider the spec qualified and falling back to a model-only
// match. That is the same resolution precedence Registry.FindTarget applies at
// execution time (its provider-scoped passes first, then any provider), so the
// family this returns is the family of the model fanout will actually run.
// Provider names and model ids fold case here like FindTarget does, not exactly
// like Registry.Provider, so a spec spelled with different case still finds its
// declared family; a nil registry or a missing model answers false and the
// caller keeps the inference.
func findRegistryModel(reg *providers.Registry, providerName, modelID string) (providers.Model, bool) {
	if reg == nil || modelID == "" {
		return providers.Model{}, false
	}
	if providerName != "" {
		for _, p := range reg.Providers {
			if !strings.EqualFold(p.Name, providerName) {
				continue
			}
			for _, m := range p.Models {
				if strings.EqualFold(m.ID, modelID) {
					return m, true
				}
			}
		}
	}
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, modelID) {
				return m, true
			}
		}
	}
	return providers.Model{}, false
}

// registryDeclaredFamily is the family one registry model carries: the explicit
// family case-preserved, else Model.FamilyName (the id-prefix inference). It
// mirrors internal/inventory's familyForEntry, which is unexported there, so a
// spec-path row and the seat-list row for the same model carry the same Family
// bytes. The exclusion verdict itself folds case (IsK3Family uses EqualFold, a
// family: entry folds its name), and rotation re-folds through entryFamily.
func registryDeclaredFamily(m providers.Model) string {
	if f := strings.TrimSpace(m.Family); f != "" {
		return f
	}
	return m.FamilyName()
}
