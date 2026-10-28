package fanout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// Class-closure sweep (r4) — the authority-composition pins for this package.
// Every planted value is the underscore-head credential class (`sk_live_...`),
// the one `providers.MaskOperand` renders raw.

// L-1: Zl/Zp join the refused class, so a token carrying one can neither run nor
// lane-split an echo.
func TestParseSeatSpecsRefusesLineSeparators(t *testing.T) {
	for _, tc := range []struct{ name, spec string }{
		{name: "U+2028 line separator", spec: "nw/mod\u2028el"},
		{name: "U+2029 paragraph separator", spec: "native:gemini\u2029a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSeatSpecs(tc.spec); err == nil {
				t.Fatalf("ParseSeatSpecs(%q) accepted a line/paragraph separator", tc.spec)
			} else if !strings.Contains(err.Error(), "control or invisible format") {
				t.Errorf("error lost its class message: %v", err)
			}
		})
	}
	// A benign token is unaffected.
	if _, err := ParseSeatSpecs("native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex"); err != nil {
		t.Errorf("benign specs must stay valid: %v", err)
	}
}

// L-2: plan-authored unit ids render through the registry-slot authority.
func TestPlanUnitIDMaskedThroughSlotAuthority(t *testing.T) {
	plant := testfixtures.SKLive
	doc := `[{"id":"` + plant + `","title":"a","scope":["src/a"],"deliverable":"da"},
	         {"id":"` + plant + `","title":"b","scope":["src/b"],"deliverable":"db"}]`
	_, err := ParsePlan([]byte(doc))
	if err == nil {
		t.Fatal("a duplicate unit id must be refused")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("duplicate-id error leaks the unit id raw: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate unit id") {
		t.Errorf("duplicate-id error lost its message: %v", err)
	}
	// unitLabel (the "unit N (id %q)" form) is the other id slot: a missing field
	// on a key-shaped id must not echo it raw.
	doc2 := `[{"id":"` + plant + `","title":"","scope":["src/a"],"deliverable":"da"}]`
	_, err2 := ParsePlan([]byte(doc2))
	if err2 == nil {
		t.Fatal("a unit missing its title must be refused")
	}
	if strings.Contains(err2.Error(), plant) {
		t.Errorf("validateUnit error leaks the unit id raw: %v", err2)
	}
}

// MaskQuotedSeatTokens: the display-boundary closer for sentences built below this
// package (the inventory selection grammar quotes its token with MaskOperand).
func TestMaskQuotedSeatTokens(t *testing.T) {
	_, plant := plantSpec()
	sentence := `invalid selection token "` + plant + `": unknown saved preset (want a number)`
	got := MaskQuotedSeatTokens(sentence)
	if strings.Contains(got, plant) {
		t.Errorf("quoted token rode raw: %q", got)
	}
	if !strings.Contains(got, `"sk_li****"`) {
		t.Errorf("quoted token must render through the slot authority: %q", got)
	}
	// The sentence frame is untouched.
	if !strings.HasPrefix(got, "invalid selection token ") || !strings.HasSuffix(got, "(want a number)") {
		t.Errorf("sentence frame drifted: %q", got)
	}

	// Benign sentences are byte-identical (identity on a benign token), including
	// a two-token list and a benign quoted name.
	for _, benign := range []string{
		`invalid selection token "1,3,5": bad range`,
		`invalid selection token "p9": unknown saved preset`,
		`no quotes at all: nw/glm-5.3-flash`,
		`unbalanced "tail stays as-is`,
	} {
		if got := MaskQuotedSeatTokens(benign); got != benign {
			t.Errorf("benign sentence drifted\n got: %q\nwant: %q", got, benign)
		}
	}
}

// The retired `models` field: a hand-edited or pre-removal record that still
// carries it is DISCLOSED and IGNORED - never applied, never a failure, and the
// stored value never echoed. The load door prints one advisory naming the preset
// and the field; the run then resolves the seat source from the ladder exactly
// as it would without the key (empty -> the engine auto-selects the lineup).
func TestRetiredModelsFieldIsDisclosedAndIgnored(t *testing.T) {
	home, stderr := ladderEnv(t)
	body := `{"presets":[{"name":"nightly","n":4,"models":"prov/m1,nw/planted\u2028model"}],"default_preset":"nightly"}`
	if werr := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("a legacy models record must not fail the run: %v", err)
	}
	if !strings.Contains(stderr.String(), `the legacy models field is removed; use seats`) {
		t.Errorf("the load door must disclose the retired field: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `preset "nightly"`) {
		t.Errorf("the advisory must name the preset: %q", stderr.String())
	}
	// Ignored, not applied: no lineup, no explicit models, and the rest of the
	// record still resolves through the ladder.
	if len(cfg.Seats) != 0 || len(cfg.Models) != 0 || cfg.Sources["seats"] != "engine" {
		t.Errorf("legacy models leaked into the lineup: seats=%v models=%v src=%q",
			cfg.Seats, cfg.Models, cfg.Sources["seats"])
	}
	if cfg.N != 4 {
		t.Errorf("the record's other knobs must still apply: n = %d; want 4", cfg.N)
	}
	for _, r := range stderr.String() {
		if r == 0x2028 {
			t.Fatalf("the advisory echoes the stored value: %q", stderr.String())
		}
	}
}

// --- r5 code-seat3 P2/P3: quoted-span and edge-shape pins ------------------

// TestMaskQuotedSeatTokensBenignLocationShaped PINS the DISCLOSE decision on
// MaskQuotedSeatTokens (r5 code-seat3 F-01/P2). A quoted span takes the FULL
// registry-slot authority, and that authority includes MaskRegistryValue's
// location-marker rewrite, so a benign location-shaped token echoed inside a
// refusal renders marker-stripped:
//
//	"./backups"            -> "backups"
//	"https://gw.example/v1" -> "gw.example/v1"
//	"C:\notes"             -> "notes"
//	"../up"                -> "up"
//
// This is a deliberate OVER-MASK (fail-safe: markers are stripped, never
// unmasked) and the alternative - exempting location-rewritten spans - would add
// a second rendering path the credential arms also traverse. The table exists so
// a future edit that "fixes" the stripping has to come here and argue with the
// decision. Non-location benign spans stay byte-identical, which the rows below
// assert next to the stripped ones.
func TestMaskQuotedSeatTokensBenignLocationShaped(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "relative path span",
			in:   `invalid selection token "./backups": want a number`,
			want: `invalid selection token "backups": want a number`,
		},
		{
			name: "URL span loses its scheme",
			in:   `invalid selection token "https://gw.example/v1": unknown saved preset`,
			want: `invalid selection token "gw.example/v1": unknown saved preset`,
		},
		{
			name: "windows drive span",
			in:   `invalid selection token "C:\notes": bad range`,
			want: `invalid selection token "notes": bad range`,
		},
		{
			name: "parent-relative span",
			in:   `invalid selection token "../up": bad range`,
			want: `invalid selection token "up": bad range`,
		},
		{
			name: "two spans: the location one is stripped, the benign one is not",
			in:   `invalid selection token "./backups" and "p9": mixed`,
			want: `invalid selection token "backups" and "p9": mixed`,
		},
		{
			name: "escaped quote inside a span is respected",
			in:   `invalid selection token "a\"b": escaped`,
			want: `invalid selection token "a\"b": escaped`,
		},
		{
			name: "benign non-location span is byte-identical",
			in:   `invalid selection token "nw/glm-5.3-flash": fine`,
			want: `invalid selection token "nw/glm-5.3-flash": fine`,
		},
	}
	for _, tc := range cases {
		if got := MaskQuotedSeatTokens(tc.in); got != tc.want {
			t.Errorf("%s:\n in : %q\n got: %q\nwant: %q", tc.name, tc.in, got, tc.want)
		}
	}

	// The plant in the SECOND of two spans masks while the first keeps its bytes
	// (the loop must keep scanning after a benign span).
	_, plant := plantSpec()
	in := `invalid selection token "p9" and "nw/` + plant + `": mixed`
	got := MaskQuotedSeatTokens(in)
	if strings.Contains(got, plant) {
		t.Fatalf("the second span's plant rode raw: %q", got)
	}
	if !strings.HasPrefix(got, `invalid selection token "p9" and "nw/`) {
		t.Fatalf("the first span or the frame drifted: %q", got)
	}
}
