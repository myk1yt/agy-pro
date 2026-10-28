package fanout

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// r3 masking wave (code lane) pins. Every planted value uses the underscore-head
// credential class (`sk_live_...`), which `providers.MaskOperand` renders raw; the
// authority for a seat/model TOKEN (and for a registry-file slot) is
// `MaskSeatToken` / `MaskSeatTokens` / `providers.MaskSlot`.

// plantSpec is the planted provider seat token and the sentinel to search for.
func plantSpec() (token, plant string) { return "nw/" + testfixtures.SKLive, testfixtures.SKLive }

// --- Item 1: the F1 drop-conflict notice -------------------------------------

// A default preset whose SEATS pin carries a planted token, dropped by the F1
// guard because the operator passed --units: the notice must mask the value per
// token. (The retired `models` pin no longer exists, so the seats knob is the
// only seat-source half the guard can drop.)
func TestDropConflictNoticeMasksSeatTokens(t *testing.T) {
	home, stderr := ladderEnv(t)
	token, plant := plantSpec()
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: token}},
		DefaultPreset: "nightly",
	})
	plan := guardPartitionPlan(t, home)
	f := ladderFlags(stderr)
	f.set["units"] = true
	f.units = plan
	if _, err := buildRunConfig(f, ""); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	out := stderr.String()
	if !strings.Contains(out, `seats=`) || !strings.Contains(out, "--units (partition mode assigns one seat per unit)") {
		t.Fatalf("drop notice missing: %q", out)
	}
	if strings.Contains(out, plant) {
		t.Errorf("drop notice leaks the underscore-head seats token raw: %q", out)
	}
}

// Benign drop notices keep their exact bytes (the pre-existing guard pins, plus
// this one as the byte-identity control for item 1).
func TestDropConflictNoticeBenignValueByteStable(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a"}},
		DefaultPreset: "nightly",
	})
	plan := guardPartitionPlan(t, home)
	f := ladderFlags(stderr)
	f.set["units"] = true
	f.units = plan
	if _, err := buildRunConfig(f, ""); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if !strings.Contains(stderr.String(), `seats="native:gemini-a"`) {
		t.Errorf("benign seats value must render byte-identically: %q", stderr.String())
	}
}

// --- Item 3: the ParseSeatSpecs quoted-token family --------------------------

// Every parse refusal that quotes the offending token renders it masked, and the
// plant never reaches the error text.
func TestParseSeatSpecsQuotedTokenIsMasked(t *testing.T) {
	_, plant := plantSpec()
	for _, tc := range []struct {
		name string
		spec string
		want string
	}{
		{name: "native id with a slash", spec: "native:" + plant + "/x", want: "native model id must not contain"},
		{name: "native grammar", spec: "native:" + plant + " x", want: "invalid native model id"},
		{name: "second seat in the list", spec: "native:gemini-a, native:" + plant + "/x", want: "seat 2:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSeatSpecs(tc.spec)
			if err == nil {
				t.Fatalf("ParseSeatSpecs(%q) accepted a malformed token", tc.spec)
			}
			if strings.Contains(err.Error(), plant) {
				t.Errorf("parse error leaks the underscore-head token raw: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parse error lost its explanation %q: %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "****") {
				t.Errorf("parse error must render the quoted token masked: %v", err)
			}
		})
	}

	// Benign malformed tokens stay readable: the message is a UX surface.
	_, err := ParseSeatSpecs("native:prov/model")
	if err == nil || !strings.Contains(err.Error(), `"native:prov/model"`) {
		t.Errorf("benign malformed token must stay readable: %v", err)
	}
}

// The preset-side wrappers of that family: the save validator and the store-side
// lineup resolver (the arm whose comment used to overclaim protection).
func TestPresetSeatParseErrorsAreMasked(t *testing.T) {
	token, plant := plantSpec()
	bad := "native:" + plant + "/x"

	// Save-time validator wrap (ErrPresetSeats).
	if err := ValidatePresetForSave(Preset{Name: "p", Seats: bad}); err == nil {
		t.Fatal("ValidatePresetForSave accepted a malformed native token")
	} else {
		if strings.Contains(err.Error(), plant) {
			t.Errorf("save validator leaks the token raw: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "seats entries must be") {
			t.Errorf("save validator lost its sentinel: %v", err)
		}
	}

	// Store-side resolver: env-selected preset with a malformed lineup.
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv(EnvFanoutPreset, "p")
	body := `{"presets":[{"name":"p","seats":"` + bad + `"}]}`
	if werr := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); werr != nil {
		t.Fatal(werr)
	}
	_, lerr := ResolvePresetSeatLineup()
	if lerr == nil {
		t.Fatal("ResolvePresetSeatLineup accepted a malformed lineup")
	}
	if strings.Contains(lerr.Error(), plant) {
		t.Errorf("store-side resolver leaks the token raw: %v", lerr)
	}
	if !strings.Contains(lerr.Error(), `preset "p" seats:`) {
		t.Errorf("store-side resolver lost its message: %v", lerr)
	}

	// Run-level: a preset-sourced malformed lineup reaches the CLI as a usage
	// error with the token masked (Run() seeds only flag operands).
	home2, _ := ladderEnv(t)
	saveRaw := `{"presets":[{"name":"nightly","seats":"` + bad + `"}],"default_preset":"nightly"}`
	if werr := os.WriteFile(filepath.Join(home2, "fanout-presets.json"), []byte(saveRaw), 0o600); werr != nil {
		t.Fatal(werr)
	}
	var out, errw bytes.Buffer
	if code := Run([]string{"-prompt", "p"}, nil, &out, &errw); code != 2 {
		t.Fatalf("exit = %d; want 2 (usage error)\nstderr:\n%s", code, errw.String())
	}
	if strings.Contains(errw.String(), plant) {
		t.Errorf("run-level preset parse error leaks the token raw: %q", errw.String())
	}
	if !strings.Contains(errw.String(), "****") {
		t.Errorf("run-level preset parse error must be masked: %q", errw.String())
	}
	_ = token
}

// The partition plan's seat slot: a plan-authored seat token that names more than
// one seat is quoted back, so it takes the registry-slot authority.
func TestPartitionPlanSeatTokenIsMasked(t *testing.T) {
	regPath, outDir := driftFixture(t)
	token, plant := plantSpec()
	plan := filepath.Join(filepath.Dir(regPath), "plan.json")
	body := `[{"id":"u1","title":"T","scope":["src"],"deliverable":"d","seat":"` + token + `,native:gemini-a"}]`
	if err := os.WriteFile(plan, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	units, perr := ParsePlan([]byte(body))
	if perr != nil {
		t.Fatalf("ParsePlan: %v", perr)
	}
	var warn bytes.Buffer
	cfg := RunConfig{Task: "review", Root: filepath.Dir(regPath), OutDir: outDir, RegistryPath: regPath, TimeoutSec: 10, Units: units, WarnOut: &warn}
	_, err := Execute(context.Background(), cfg)
	if err == nil {
		t.Fatal("a two-seat write unit must be refused")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("plan seat error leaks the token raw: %v", err)
	}
	if !strings.Contains(err.Error(), "must name exactly one seat") {
		t.Errorf("plan seat error lost its message: %v", err)
	}
}

// --- Item 4: defense-in-depth sinks -----------------------------------------

// The defaults-rung drift notice renders the stored value through the
// registry-file authority (it exists for the `tools` knob too).
func TestDefaultsNoticeMasksUnderscoreHeadStoredValue(t *testing.T) {
	_, stderr := ladderEnv(t)
	_, plant := plantSpec()
	writeDefaults(t, `{"tools":"`+plant+`"}`)
	// providers.json is a different file from the presets store; writeDefaults
	// already points AGY_PRO_PROVIDERS at it. The run itself errors later (the
	// tool-token gate refuses the unknown token) - the notice under test is
	// printed by the ladder BEFORE that gate, which is the point.
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err == nil {
		t.Fatal("the tool-token gate must still refuse the stored tools value")
	}
	out := stderr.String()
	if !strings.Contains(out, "using providers.json default tools=") {
		t.Fatalf("defaults notice missing: %q", out)
	}
	if strings.Contains(out, plant) {
		t.Errorf("defaults notice leaks the stored value raw: %q", out)
	}
}

// maskSeatError drops the Cc/Cf class from the final text (defense in depth for a
// registry message that quotes a hand-edited id byte for byte).
func TestMaskSeatErrorStripsControls(t *testing.T) {
	_, plant := plantSpec()
	inner := &stubErr{text: "model \"nw/" + plant + "\" not found\x1b[2J"}
	masked := maskSeatError(inner, "nw/"+plant)
	if masked == nil {
		t.Fatal("maskSeatError returned nil for a non-nil error")
	}
	for _, r := range masked.Error() {
		if r == 0x1b {
			t.Fatalf("maskSeatError left a control rune: %q", masked.Error())
		}
	}
	if strings.Contains(masked.Error(), plant) {
		t.Errorf("maskSeatError left the plant raw: %q", masked.Error())
	}
}

// stubErr is the minimal error carrier for the maskSeatError test.
type stubErr struct{ text string }

func (e *stubErr) Error() string { return e.text }

// --- Item 6: separator spacing is preserved ----------------------------------

func TestMaskSeatTokensPreservesSeparatorSpacing(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"model-a, model-b", "model-a, model-b"},
		{"model-a,model-b", "model-a,model-b"},
		{"a ,  b ,c", "a ,  b ,c"},
		{"", ""},
		{"   ", "   "},
		{"a,,b", "a,,b"},
		// Edge commas (r5 code-seat3 F-03): the empty segments are preserved, so a
		// leading/trailing separator never moves and never gains a token.
		{",a", ",a"},
		{"a,", "a,"},
		{",", ","},
		{",,", ",,"},
		{"a, ,b", "a, ,b"},
	} {
		if got := MaskSeatTokens(tc.in); got != tc.want {
			t.Errorf("MaskSeatTokens(%q) = %q; want %q (separator spans preserved)", tc.in, got, tc.want)
		}
	}

	// A plant inside a spaced list masks in place while its benign siblings keep
	// their exact bytes.
	_, plant := plantSpec()
	got := MaskSeatTokens("native:gemini-a, nw/" + plant + " , native:gemini-b")
	if strings.Contains(got, plant) {
		t.Errorf("planted list leaks the token: %q", got)
	}
	if !strings.HasPrefix(got, "native:gemini-a, ") || !strings.HasSuffix(got, " , native:gemini-b") {
		t.Errorf("benign siblings/separators drifted: %q", got)
	}
}
