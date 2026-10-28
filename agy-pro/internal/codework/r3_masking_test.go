package codework

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// r3 masking wave (code lane), codework mirror: the dry-run/echo renderers are a
// restatement of internal/swarm/build.go FormatCommand, so they carry the same
// masking rule - a `--seats` / `--models` VALUE is a seat/models TOKEN list and
// takes the registry-slot authority (fanout.MaskSeatTokens). Masking only: the
// consent-gate semantics of this door are untouched.

func TestFormatCommandMasksUnderscoreHeadSeatValue(t *testing.T) {
	plant := testfixtures.SKLive
	argv := []string{
		"--task", "review",
		"--seats", "native:gemini-a,nw/" + plant,
		"--root", ".",
		"--tools", "read,search",
		"--json",
	}
	got := formatCommand(argv)
	if strings.Contains(got, plant) {
		t.Errorf("dry-run echo leaks the underscore-head seat token raw:\n%s", got)
	}
	if !strings.Contains(got, "--seats native:gemini-a,nw/sk****") {
		t.Errorf("dry-run echo must carry the masked seat value:\n%s", got)
	}
	if !strings.HasPrefix(got, "agy pro fanout ") {
		t.Errorf("command prefix drifted: %q", got)
	}
}

// Benign vectors are byte-identical (the pre-existing pin plus a value-shape
// control), and a non-seat flag whose value merely LOOKS like a token list keeps
// the operand masker.
func TestFormatArgvBenignByteStableAndNonSeatFlagsUnchanged(t *testing.T) {
	want := `--task review --seats native:gemini-a,prov/m1 --root . --json`
	if got := formatArgv([]string{"--task", "review", "--seats", "native:gemini-a,prov/m1", "--root", ".", "--json"}); got != want {
		t.Errorf("benign argv drifted\n got: %q\nwant: %q", got, want)
	}
	// --tools is not a seat-source flag: the operand masker still owns it.
	if got := formatArgv([]string{"--tools", "read,search"}); got != "--tools read,search" {
		t.Errorf("non-seat flag value drifted: %q", got)
	}
}

// The write-phase seat slot in a refusal sentence is a seat token too
// (planSeats quotes the plan-authored seat).
func TestWritePhaseSeatRefusalMasksUnderscoreHeadToken(t *testing.T) {
	plant := testfixtures.SKLive
	_, _, err := planSeats([]fanout.Unit{{ID: "u1", Title: "T", Scope: []string{"src"}, Seat: "native:" + plant + "/x"}})
	if err == nil {
		t.Fatal("a malformed write-unit seat must be refused")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("write-phase refusal leaks the underscore-head seat token raw: %v", err)
	}
	if !strings.Contains(err.Error(), "is not a valid seat spec") {
		t.Errorf("write-phase refusal lost its message: %v", err)
	}
	// The native-seat refusal quotes the seat as well.
	_, _, nerr := planSeats([]fanout.Unit{{ID: "u1", Title: "T", Scope: []string{"src"}, Seat: "native:" + plant}})
	if nerr == nil {
		t.Fatal("a native write-unit seat must be refused")
	}
	if strings.Contains(nerr.Error(), plant) {
		t.Errorf("native write-seat refusal leaks the token raw: %v", nerr)
	}
}

// --- Class-closure sweep (r4) ----------------------------------------------

// H-1: the consent screen's per-seat row is the one seat-token sink on the
// surface the operator reads before authorizing spend.
func TestEchoMeteredMasksUnderscoreHeadSeatToken(t *testing.T) {
	plant := testfixtures.SKLive
	oldStdout := PhaseStdout
	t.Cleanup(func() { PhaseStdout = oldStdout })
	var buf bytes.Buffer
	PhaseStdout = &buf

	if !echoMetered("write", []string{"native:gemini-a", "nw/" + plant}) {
		t.Fatal("echoMetered must report a metered seat")
	}
	out := buf.String()
	if strings.Contains(out, plant) {
		t.Errorf("consent row leaks the underscore-head seat token raw:\n%s", out)
	}
	if !strings.Contains(out, "nw/sk****") {
		t.Errorf("consent row must render the seat masked:\n%s", out)
	}
	// Benign rows and the count line stay byte-identical.
	if !strings.Contains(out, "1 metered seat(s) - this run spends money") {
		t.Errorf("consent header drifted:\n%s", out)
	}
	buf.Reset()
	echoMetered("plan", []string{"nw/glm-5.3-flash"})
	if !strings.Contains(buf.String(), "seat 1  [provider]  nw/glm-5.3-flash  (metered API)") {
		t.Errorf("benign consent row drifted:\n%s", buf.String())
	}
}

// The three write-unit seat refusals in the PLAN builder (validateSeat) are the
// same token class as planSeats' (already converted in r3).
func TestValidateSeatRefusalsMaskUnderscoreHeadToken(t *testing.T) {
	plant := testfixtures.SKLive
	for _, tc := range []struct{ name, seat, want string }{
		{name: "malformed", seat: "native:" + plant + "/x", want: "is not a valid seat spec"},
		{name: "two seats", seat: "nw/" + plant + ",native:gemini-a", want: "names 2 seats"},
		{name: "native", seat: "native:" + plant, want: "is a native seat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSeat(0, tc.seat)
			if err == nil {
				t.Fatalf("validateSeat(%q) accepted the seat", tc.seat)
			}
			if strings.Contains(err.Error(), plant) {
				t.Errorf("refusal leaks the token raw: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal lost its message %q: %v", tc.want, err)
			}
		})
	}
	// Benign seats stay silent (no error, nothing to mask).
	if err := validateSeat(0, "nw/glm-5.3-flash"); err != nil {
		t.Errorf("benign seat refused: %v", err)
	}
}

// L-3: the run-report harvest takes the registry-slot authority.
func TestHarvestMasksModelThroughSlotAuthority(t *testing.T) {
	plant := testfixtures.SKLive
	if got := providers.MaskSlot(providers.StripSlotControls(plant)); got == plant {
		t.Fatalf("premise broken: the authority renders the plant raw (%q)", got)
	}
	// The harvest path itself is exercised through the report builder; this pin
	// guards the site's masker choice against a future regression to MaskOperand.
	src := readPhasesSourceForMaskCheck(t)
	if !strings.Contains(src, "Model:            providers.MaskSlot(providers.StripSlotControls(seatModel(s)))") {
		t.Error("report.go harvest must keep the registry-slot authority on the model slot (L-3)")
	}
}

// readPhasesSourceForMaskCheck reads report.go so the L-3 pin can assert the
// masker choice at a site whose runtime path needs a full run directory.
func readPhasesSourceForMaskCheck(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("report.go"))
	if err != nil {
		t.Fatalf("read report.go: %v", err)
	}
	return string(data)
}

// Class closure (r4 L-2 twin): plan-authored unit ids in this package render
// through the registry-slot authority too.
func TestPlanUnitIDSlotsMaskThroughSlotAuthority(t *testing.T) {
	plant := testfixtures.SKLive
	units, perr := fanout.ParsePlan([]byte(`[{"id":"u1","title":"t","scope":["src"],"deliverable":"d","seat":"nw/glm-5.3-flash"}]`))
	if perr != nil {
		t.Fatalf("ParsePlan: %v", perr)
	}
	// repairTargets is the unit-id slot: an unknown id is quoted back.
	if _, err := repairTargets(units, []string{plant}); err == nil {
		t.Fatal("an unknown repair unit id must be refused")
	} else {
		if strings.Contains(err.Error(), plant) {
			t.Errorf("repair refusal leaks the unit id raw: %v", err)
		}
		if !strings.Contains(err.Error(), "****") {
			t.Errorf("repair refusal must be masked: %v", err)
		}
	}
	// A benign id stays readable (and resolves).
	if _, err := repairTargets(units, []string{"u1"}); err != nil {
		t.Errorf("benign repair target failed: %v", err)
	}
}
