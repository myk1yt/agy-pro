package swarm

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// --- F04 saved-preset NAME selectability in the §2.7 selection grammar ---

// namesInventory is a 4-entry fixture (2 native, 2 external) so numbers,
// ranges, preset ids and saved names all resolve without degrading.
func namesInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	inv := buildInventory(t,
		[]inventory.NativeModel{
			{ID: "native-a", DisplayName: "Native A"},
			{ID: "native-b", DisplayName: "Native B"},
		},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{
			{ID: "m-one"},
			{ID: "m-two"},
		}}})
	if n := len(inv.Entries); n != 4 {
		t.Fatalf("fixture entries = %d, want 4", n)
	}
	return inv
}

// entryNumbers maps resolved entries back to their 1-based inventory numbers so
// a name resolution can be compared with the numbers it stands for.
func entryNumbers(inv *inventory.Inventory, entries []inventory.Entry) []int {
	nums := make([]int, 0, len(entries))
	for _, e := range entries {
		nums = append(nums, e.Number)
	}
	return nums
}

// TestSelectionGrammarSavedPresetNameTokens is the grammar table for the F04
// decision: a token that names an existing saved preset resolves to that
// preset's stored Numbers; numbers, ranges and p1/p2/p3 keep their exact
// pre-F04 meaning; a name spelled like a bare number or a preset id never wins
// over the grammar spelling it collides with; an unknown name is a refusal, not
// a silent fallback.
func TestSelectionGrammarSavedPresetNameTokens(t *testing.T) {
	inv := namesInventory(t)
	presets := []SavedPreset{
		{Name: "deep", Numbers: "1,3"},
		{Name: "Swift", Numbers: "2"},
		// Hand-edited swarm-state.json rows: the settings surface refuses to
		// SAVE these names (validPresetName), and the grammar still must not
		// let them shadow a bare number or a preset id.
		{Name: "42", Numbers: "1"},
		{Name: "p2", Numbers: "4"},
	}

	t.Run("saved name resolves to its numbers", func(t *testing.T) {
		seats, err := ResolveSeatsWith("deep", inv, presets, nil)
		if err != nil {
			t.Fatalf("ResolveSeatsWith(deep): %v", err)
		}
		if got, want := entryNumbers(inv, seats), []int{1, 3}; !equalInts(got, want) {
			t.Errorf("deep resolved to seats %v, want %v", got, want)
		}
	})

	t.Run("name lookup is case-insensitive", func(t *testing.T) {
		seats, err := ResolveSeatsWith("sWiFt", inv, presets, nil)
		if err != nil {
			t.Fatalf("ResolveSeatsWith(sWiFt): %v", err)
		}
		if got, want := entryNumbers(inv, seats), []int{2}; !equalInts(got, want) {
			t.Errorf("sWiFt resolved to seats %v, want %v", got, want)
		}
	})

	t.Run("name mixes with numbers and preset ids", func(t *testing.T) {
		seats, err := ResolveSeatsWith("deep,4", inv, presets, nil)
		if err != nil {
			t.Fatalf("ResolveSeatsWith(deep,4): %v", err)
		}
		if got, want := entryNumbers(inv, seats), []int{1, 3, 4}; !equalInts(got, want) {
			t.Errorf("deep,4 resolved to seats %v, want %v", got, want)
		}
	})

	t.Run("numbers keep their meaning", func(t *testing.T) {
		for _, tok := range []string{"1", "4"} {
			seats, err := ResolveSeats(tok, inv, nil)
			if err != nil {
				t.Fatalf("ResolveSeats(%s): %v", tok, err)
			}
			if len(seats) != 1 {
				t.Fatalf("%s resolved to %d seats, want 1", tok, len(seats))
			}
		}
	})

	t.Run("ranges keep their meaning", func(t *testing.T) {
		seats, err := ResolveSeats("2-4", inv, nil)
		if err != nil {
			t.Fatalf("ResolveSeats(2-4): %v", err)
		}
		if got, want := entryNumbers(inv, seats), []int{2, 3, 4}; !equalInts(got, want) {
			t.Errorf("2-4 resolved to seats %v, want %v", got, want)
		}
	})

	t.Run("preset ids keep their meaning", func(t *testing.T) {
		// p2 = 3 external (family-rotated). The inventory has 2 external rows,
		// so the preset degrades to both of them: 2 seats. The hand-edited
		// "p2" saved preset would have yielded exactly 1 (seat 4), so this
		// count separates the two readings.
		var warn bytes.Buffer
		seats, err := ResolveSeatsWith("p2", inv, presets, &warn)
		if err != nil {
			t.Fatalf("ResolveSeatsWith(p2): %v", err)
		}
		if len(seats) != 2 {
			t.Errorf("p2 resolved to %d seats, want the preset's 2 external rows", len(seats))
		}
		for _, e := range seats {
			if e.Kind != "provider" {
				t.Errorf("p2 seated a %s row (%s); the preset id must not degrade to the saved name", e.Kind, e.Spec)
			}
		}
	})

	t.Run("bare-number spellings never shadow the number grammar", func(t *testing.T) {
		// "42" is out of range for a 4-entry inventory: the number path must
		// report it instead of quietly seating the "42" saved preset's seat 1.
		_, err := ResolveSeatsWith("42", inv, presets, nil)
		if err == nil {
			t.Fatal("42 must stay a seat number (out of range here), not a saved name")
		}
		if !strings.Contains(err.Error(), "out of range") {
			t.Errorf("42 error = %v; want the out-of-range seat message", err)
		}
	})

	t.Run("unknown name is a named refusal", func(t *testing.T) {
		_, err := ResolveSeatsWith("nosuch", inv, presets, nil)
		if err == nil {
			t.Fatal("an unknown saved-preset name must be refused")
		}
		for _, want := range []string{"unknown saved preset", "nosuch", "saved preset name"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal missing %q: %v", want, err)
			}
		}
	})

	t.Run("seat-spec spellings stay seat specs", func(t *testing.T) {
		// The name grammar must not hijack the power-user spec spelling.
		for _, tok := range []string{"native:gemini-3.8-flash-high", "nw/m-one", "./backups"} {
			if IsSelectionInputWith(tok, presets) {
				t.Errorf("%q must stay a seat spec, not selection input", tok)
			}
		}
	})
}

// TestIsSelectionInputWithSavedPresetName is the non-vacuity pin for the wiring:
// the SAME token is not selection input without the store (pre-F04 behavior, so
// the --seats mode switch cannot hijack a bare provider spec) and is selection
// input with it. Without the store parameter this test cannot be written at all,
// which is what makes it red against the pre-F04 code.
func TestIsSelectionInputWithSavedPresetName(t *testing.T) {
	presets := []SavedPreset{{Name: "deep", Numbers: "1,3"}}

	if IsSelectionInput("deep") {
		t.Fatal("premise broken: a name must not be selection input without the saved-preset store")
	}
	if !IsSelectionInputWith("deep", presets) {
		t.Fatal("a known saved-preset name must be selection input")
	}
	if IsSelectionInputWith("nosuch", presets) {
		t.Fatal("an unknown name must not become selection input (the refusal belongs to the resolver)")
	}
	// Unchanged grammar still passes with a store in scope.
	for _, tok := range []string{"", "1", "1,3", "2-4", "p1", "p3", "p1,deep"} {
		if !IsSelectionInputWith(tok, presets) {
			t.Errorf("%q must stay selection input", tok)
		}
	}
}

// TestSavedPresetNameRoundTripThroughFileStore proves the name resolves through
// the REAL store the settings surface writes (swarm-state.json on disk), not a
// memory-only stub: save a preset, load it back, resolve it by name, and compare
// against the same Numbers typed directly.
func TestSavedPresetNameRoundTripThroughFileStore(t *testing.T) {
	inv := namesInventory(t)
	st := &FileStore{Path: filepath.Join(t.TempDir(), "swarm-state.json")}
	if err := st.SavePresets([]SavedPreset{
		{Name: "deep-review", Numbers: "1,3"},
		{Name: "solo", Numbers: "2"},
	}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	presets, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if len(presets) != 2 {
		t.Fatalf("loaded %d presets, want 2", len(presets))
	}

	for _, tc := range []struct{ name, numbers string }{
		{"deep-review", "1,3"},
		{"solo", "2"},
	} {
		byName, err := ResolveSeatsWith(tc.name, inv, presets, nil)
		if err != nil {
			t.Fatalf("ResolveSeatsWith(%s): %v", tc.name, err)
		}
		byNumbers, err := ResolveSeats(tc.numbers, inv, nil)
		if err != nil {
			t.Fatalf("ResolveSeats(%s): %v", tc.numbers, err)
		}
		gotN, wantN := entryNumbers(inv, byName), entryNumbers(inv, byNumbers)
		if !equalInts(gotN, wantN) {
			t.Errorf("%s resolved to %v; its stored Numbers %q resolve to %v", tc.name, gotN, tc.numbers, wantN)
		}
		if len(gotN) == 0 {
			t.Errorf("%s resolved to zero seats", tc.name)
		}
	}

	// The vocabulary the settings input surface accepts (validPresetName) is
	// the vocabulary the grammar resolves: hyphenated and underscored names work.
	if err := st.SavePresets(append(presets, SavedPreset{Name: "my_preset-2", Numbers: "4"})); err != nil {
		t.Fatalf("SavePresets(2): %v", err)
	}
	presets, err = st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets(2): %v", err)
	}
	if !IsSelectionInputWith("my_preset-2", presets) {
		t.Fatal("a hyphen/underscore saved name must be selection input")
	}
	seats, err := ResolveSeatsWith("my_preset-2", inv, presets, nil)
	if err != nil {
		t.Fatalf("ResolveSeatsWith(my_preset-2): %v", err)
	}
	if got, want := entryNumbers(inv, seats), []int{4}; !equalInts(got, want) {
		t.Errorf("my_preset-2 resolved to %v, want %v", got, want)
	}
}

// TestSavedPresetNameRefusalsAreNamedAndMasked pins the two named refusals and
// their masking: a key-shaped token stays masked (first-5 form, the sibling
// sink rule for a typed operand) and a benign name stays readable.
func TestSavedPresetNameRefusalsAreNamedAndMasked(t *testing.T) {
	inv := namesInventory(t)

	t.Run("empty stored numbers is named", func(t *testing.T) {
		presets := []SavedPreset{{Name: "blank", Numbers: "   "}}
		_, err := ResolveSeatsWith("blank", inv, presets, nil)
		if err == nil {
			t.Fatal("a saved preset with no numbers must be refused")
		}
		for _, want := range []string{"no seat numbers", "blank"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal missing %q: %v", want, err)
			}
		}
	})

	t.Run("key-shaped unknown name stays masked", func(t *testing.T) {
		plant := testfixtures.AnthropicPlanted
		if IsSelectionInputWith(plant, nil) {
			t.Fatalf("premise broken: %q must not be known selection grammar", plant)
		}
		_, err := ResolveSeatsWith(plant, inv, nil, nil)
		if err == nil {
			t.Fatal("an unknown key-shaped name must be refused")
		}
		if strings.Contains(err.Error(), plant) {
			t.Errorf("refusal echoes the planted key raw: %v", err)
		}
		if !strings.Contains(err.Error(), "****") {
			t.Errorf("refusal carries no mask: %v", err)
		}
	})

	t.Run("benign unknown name stays readable", func(t *testing.T) {
		_, err := ResolveSeatsWith("typo-name", inv, nil, nil)
		if err == nil || !strings.Contains(err.Error(), `"typo-name"`) {
			t.Errorf("benign name must stay readable in the refusal: %v", err)
		}
	})
}

// TestBuilderSeatStepResolvesSavedPresetName drives the interactive builder end
// to end: the seat prompt announces the name grammar, a saved name typed at that
// prompt seats the preset's numbers, and an unknown name is refused without
// advancing the step.
func TestBuilderSeatStepResolvesSavedPresetName(t *testing.T) {
	inv := namesInventory(t)
	store := presetStoreStub{presets: []SavedPreset{{Name: "deep", Numbers: "1,3"}}}

	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader("1\nnosuch\ndeep\nq\n"), Out: &out, ErrW: &errOut, Inv: inv, Store: store}
	if code := b.Run(); code != 0 {
		t.Fatalf("builder exit = %d; want 0 (cancel)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "or a saved preset name") {
		t.Errorf("seat prompt does not announce the name grammar:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "unknown saved preset") {
		t.Errorf("unknown name at the seat prompt must be refused by name:\n%s", errOut.String())
	}

	block := out.String()[strings.Index(out.String(), "Selected seats:"):]
	block = block[:strings.Index(block, "=== tools & budget ===")]
	rows := 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "seat ") {
			rows++
		}
	}
	if rows != 2 {
		t.Errorf("deep seated %d rows, want 2 (its numbers 1,3)\n%s", rows, block)
	}
	if !strings.Contains(block, "native-a") || !strings.Contains(block, "m-one") {
		t.Errorf("deep did not seat seats 1 and 3:\n%s", block)
	}
}

// TestPickSeatsLoadsStoreOnceForListingAndResolution keeps the listing and the
// resolution reading the SAME snapshot: a store whose LoadPresets returns a
// different list on each call would let the user type a name the prompt just
// listed and be refused (or seat a different preset).
func TestPickSeatsLoadsStoreOnceForListingAndResolution(t *testing.T) {
	inv := namesInventory(t)
	calls := 0
	counting := countingStore{loads: func() []SavedPreset {
		calls++
		if calls == 1 {
			return []SavedPreset{{Name: "deep", Numbers: "1,3"}}
		}
		return nil
	}}

	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader("1\ndeep\nq\n"), Out: &out, ErrW: &errOut, Inv: inv, Store: counting}
	if code := b.Run(); code != 0 {
		t.Fatalf("builder exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if calls != 1 {
		t.Errorf("Store.LoadPresets called %d times in the seat step, want 1", calls)
	}
	if errOut.Len() != 0 {
		t.Errorf("listed name must resolve to the same snapshot; stderr:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), "Selected seats:") {
		t.Errorf("builder did not reach the selected-seat summary:\n%s", out.String())
	}
}

// countingStore is a Store whose LoadPresets answers through load.
type countingStore struct{ loads func() []SavedPreset }

func (s countingStore) LoadPresets() ([]SavedPreset, error) { return s.loads(), nil }
func (s countingStore) SavePresets([]SavedPreset) error     { return nil }

// TestSelectionSeedsCoverResolvedNumbers pins the mask-seed contract the call
// sites rely on: a saved name's stored Numbers must be seedable, because the
// inventory error quotes the expanded number token rather than the typed name.
func TestSelectionSeedsCoverResolvedNumbers(t *testing.T) {
	plant := testfixtures.AWSSecretNoSlash
	presets := []SavedPreset{{Name: "planted", Numbers: plant}}
	seeds := SelectionSeeds("planted", presets)
	joined := strings.Join(seeds, "\x00")
	for _, want := range []string{"planted", plant} {
		if !strings.Contains(joined, want) {
			t.Errorf("seeds missing %q: %q", want, seeds)
		}
	}
	if got := SelectionSeeds("1,3", presets); len(got) != 3 {
		t.Errorf("number-only seeds = %q, want the line plus its 2 tokens", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSeatSelectionSplitTokensKeepNamesAsOneToken guards the grammar the name
// regex relies on: a name never contains a §2.7 separator, so it reaches the
// resolver as exactly one token.
func TestSeatSelectionSplitTokensKeepNamesAsOneToken(t *testing.T) {
	tokens := splitSelTokens("deep-review,1,3")
	if len(tokens) != 3 || tokens[0] != "deep-review" {
		t.Fatalf("splitSelTokens = %q, want [deep-review 1 3]", tokens)
	}
}
