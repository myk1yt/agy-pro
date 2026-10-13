package inventory

import (
	"context"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// buildInventory makes a fixed test inventory: 3 native + 5 provider entries
// across 3 provider groups (google, nw, bigmodel), one k3 entry and one
// KeyMissing entry.
func buildInventory(t *testing.T) *Inventory {
	t.Helper()
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{
			{ID: "gemini-2.5-flash"},
			{ID: "gemini-2.5-pro"},
		}},
		{Name: "nw", APIKeyEnv: "NW_KEY_NOT_SET_IN_TESTS", Models: []providers.Model{
			{ID: "glm-5.3-flash"},
		}},
		{Name: "bigmodel", Models: []providers.Model{
			{ID: "kimi-k3"},
			{ID: "glm-4.6"},
		}},
	}}
	native := []NativeModel{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
		{ID: "gemini-3.1-pro-high", DisplayName: "Gemini 3.1 Pro (High)"},
		{ID: "claude-sonnet-5-5-high", DisplayName: "Claude Sonnet 5.5 (High)"},
	}
	inv, err := Load(context.Background(), reg, &stubFetcher{models: native})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestParseSelectionTable(t *testing.T) {
	inv := buildInventory(t)
	// Layout: 1..3 native, 4 gemini-2.5-flash, 5 gemini-2.5-pro, 6 glm-5.3-flash
	// (key missing), 7 kimi-k3, 8 glm-4.6.
	cases := []struct {
		input string
		want  []int // entry numbers
	}{
		{"1", []int{1}},
		{"1,3,5", []int{1, 3, 5}},
		{"2-4", []int{2, 3, 4}},
		{"1 3 5", []int{1, 3, 5}},
		{"1;3;5", []int{1, 3, 5}},
		{"  2 , 3  ", []int{2, 3}},
	}
	for _, tc := range cases {
		got, err := ParseSelection(tc.input, inv)
		if err != nil {
			t.Errorf("ParseSelection(%q) error: %v", tc.input, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("ParseSelection(%q) = %d entries, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i].Number != tc.want[i] {
				t.Errorf("ParseSelection(%q)[%d] = #%d, want #%d", tc.input, i, got[i].Number, tc.want[i])
			}
		}
	}
}

func TestParseSelectionErrors(t *testing.T) {
	inv := buildInventory(t)
	cases := []struct {
		input   string
		wantErr string
	}{
		{"x", `invalid selection token "x"`},
		{"p9", `invalid selection token "p9"`},
		{"99", "seat number 99 out of range (1-8)"},
		{"0", `invalid selection token "0"`},
		{"3-1", `range "3-1" is inverted`},
		{"1-x", `invalid selection token "1-x"`},
	}
	for _, tc := range cases {
		_, err := ParseSelection(tc.input, inv)
		if err == nil {
			t.Errorf("ParseSelection(%q) succeeded, want error %q", tc.input, tc.wantErr)
			continue
		}
		if err.Error() != tc.wantErr {
			t.Errorf("ParseSelection(%q) error = %q, want %q", tc.input, err.Error(), tc.wantErr)
		}
	}
}

func TestParseSelectionExceeds40Cap(t *testing.T) {
	// 41 native entries: "1-41" expands past the cap.
	var native []NativeModel
	for i := 0; i < 41; i++ {
		native = append(native, NativeModel{ID: "m" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('0'+i/26)), DisplayName: "M"})
	}
	inv, err := Load(context.Background(), nil, &stubFetcher{models: native})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Entries) != 41 {
		t.Fatalf("setup: %d entries", len(inv.Entries))
	}
	_, err = ParseSelection("1-41", inv)
	if err == nil || err.Error() != "selection exceeds 40 seats" {
		t.Fatalf("1-41 error = %v, want selection exceeds 40 seats", err)
	}
	// Exactly 40 is fine.
	if _, err := ParseSelection("1-40", inv); err != nil {
		t.Fatalf("1-40 must be accepted: %v", err)
	}
}

func TestParseSelectionEmptyInputDefaultPreset(t *testing.T) {
	inv := buildInventory(t)
	got, err := ParseSelection("   ", inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("empty input must resolve the default preset (p1)")
	}
	// p1 = 2 native + 2 external.
	if len(got) != 4 {
		t.Fatalf("default preset returned %d entries, want 4 (p1: 2 native + 2 external)", len(got))
	}
	if got[0].Kind != "native" || got[1].Kind != "native" {
		t.Errorf("p1 must lead with 2 native entries: %+v", got[:2])
	}
}

func TestParseSelectionPresets(t *testing.T) {
	inv := buildInventory(t)

	p2, err := ParseSelection("p2", inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2) != 3 {
		t.Fatalf("p2 = %d entries, want 3 external", len(p2))
	}
	fams := map[string]bool{}
	for _, e := range p2 {
		if e.Kind != "provider" {
			t.Errorf("p2 picked a non-external entry: %+v", e)
		}
		if IsK3Excluded(e.ModelID) {
			t.Errorf("p2 auto-picked k3 entry (pool policy): %+v", e)
		}
		if e.KeyMissing {
			t.Errorf("p2 auto-picked KeyMissing entry: %+v", e)
		}
		fams[providersFamily(e.ModelID)] = true
	}
	// Family rotation: distinct families preferred.
	if len(fams) < 2 {
		t.Errorf("p2 should prefer distinct families, got %v", fams)
	}

	// Case-insensitive preset ids (contract §2.7: "P3").
	p3, err := ParseSelection("P3", inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(p3) != 4 {
		t.Fatalf("P3 = %d entries, want 4 (1 flash-tier native + 1 pro-tier native + 2 external)", len(p3))
	}
	if p3[0].Kind != "native" || !strings.Contains(strings.ToLower(p3[0].ModelID), "flash") {
		t.Errorf("p3[0] should be a flash-tier native: %+v", p3[0])
	}
	if p3[1].Kind != "native" || !strings.Contains(strings.ToLower(p3[1].ModelID), "-pro") {
		t.Errorf("p3[1] should be a pro-tier native: %+v", p3[1])
	}

	p1, err := ParseSelection("p1", inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1) != 4 {
		t.Fatalf("p1 = %d entries, want 4", len(p1))
	}
	for _, e := range p1[2:] {
		if e.Kind != "provider" {
			t.Errorf("p1 external half picked non-external: %+v", e)
		}
		if IsK3Excluded(e.ModelID) || e.KeyMissing {
			t.Errorf("p1 picked an unavailable external: %+v", e)
		}
	}
}

func TestParseSelectionPresetDegradesNeverErrors(t *testing.T) {
	// Only 1 native + 1 usable external: every preset degrades to "all
	// available" instead of erroring.
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{{ID: "gemini-2.5-flash"}}},
	}}
	inv, _ := Load(context.Background(), reg, &stubFetcher{models: []NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "High"}}})
	for _, id := range []string{"p1", "p2", "p3"} {
		got, err := ParseSelection(id, inv)
		if err != nil {
			t.Errorf("ParseSelection(%q) on a thin inventory must degrade, got error %v", id, err)
		}
		if len(got) == 0 {
			t.Errorf("ParseSelection(%q) returned nothing on a thin inventory", id)
		}
	}
}

// A7 (contract §2.5/§2.7): preset family rotation must use the family the
// fanout engine uses (registry Model.FamilyName()). Inferring it from the id
// prefix let a registry-declared family be double-seated: two ids with
// different prefixes but one declared family looked like two families here.
func TestPresetRotationUsesDeclaredFamily(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "alpha", Models: []providers.Model{
			{ID: "foo-1", Family: "shared"}, // declared family, unrelated id prefix
		}},
		{Name: "beta", Models: []providers.Model{
			{ID: "bar-1", Family: "shared"}, // same declared family
		}},
		{Name: "gamma", Models: []providers.Model{
			{ID: "baz-1", Family: "other"},
		}},
	}}
	inv, err := Load(context.Background(), reg, &stubFetcher{models: nil})
	if err != nil {
		t.Fatal(err)
	}
	// Every entry carries the family the engine would use.
	want := map[string]string{"foo-1": "shared", "bar-1": "shared", "baz-1": "other"}
	for _, e := range inv.Entries {
		if got := entryFamily(e); got != want[e.ModelID] {
			t.Errorf("entry %s: family = %q; want %q", e.ModelID, got, want[e.ModelID])
		}
		if e.Family != want[e.ModelID] {
			t.Errorf("entry %s: Entry.Family = %q; want %q (must be carried, not inferred)", e.ModelID, e.Family, want[e.ModelID])
		}
	}

	// p2 = 3 family-rotated externals. The rotation pass must seat two
	// DISTINCT declared families: foo-1 (shared) then baz-1 (other). Before A7
	// it seated foo-1 then bar-1 - one declared family, two seats.
	p2, err := ParseSelection("p2", inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2) != 3 {
		t.Fatalf("p2 = %d entries, want 3 (2 rotation + 1 same-family fill)", len(p2))
	}
	if entryFamily(p2[0]) == entryFamily(p2[1]) {
		t.Errorf("rotation double-seated family %q: %+v", entryFamily(p2[0]), p2)
	}
	if got := entryFamily(p2[0]) + "," + entryFamily(p2[1]); got != "shared,other" {
		t.Errorf("rotation families = %q; want shared,other", got)
	}
	// The declared family is visible on the entry the caller receives.
	if p2[0].Family != "shared" || p2[1].Family != "other" {
		t.Errorf("entries lost their declared family: %q / %q", p2[0].Family, p2[1].Family)
	}
}

// A7: native rows carry InferFamily(id) - the same inference the native seat
// target uses - so a native entry's rotation family matches fanout's.
func TestEntryFamilyForNativeRows(t *testing.T) {
	inv, err := Load(context.Background(), nil, &stubFetcher{models: []NativeModel{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(inv.Entries))
	}
	if inv.Entries[0].Family != "gemini" {
		t.Errorf("native entry family = %q; want gemini (providers.InferFamily)", inv.Entries[0].Family)
	}
}

// Lexer disjointness (F-MISC): a token is a preset-id iff its first rune is
// p/P - disjoint from number (digit first) and range (digit...).
func TestParseSelectionPresetLexerDisjointness(t *testing.T) {
	inv := buildInventory(t)
	// Digit-first tokens never resolve as presets.
	got, err := ParseSelection("1", inv)
	if err != nil || len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("number token misparsed: %v %v", got, err)
	}
	// A range with p inside the numbers is not a preset.
	if _, err := ParseSelection("2-3", inv); err != nil {
		t.Fatalf("range misparsed: %v", err)
	}
	// p-prefix tokens are presets; p1|p2|p3 contain no delimiters.
	for _, tok := range []string{"p1", "P2", "p3"} {
		if _, err := ParseSelection(tok, inv); err != nil {
			t.Errorf("preset %q rejected: %v", tok, err)
		}
	}
	for _, tok := range []string{"p", "p0", "p4", "px", "preset"} {
		if _, err := ParseSelection(tok, inv); err == nil {
			t.Errorf("token %q must not resolve as a preset", tok)
		}
	}
}
