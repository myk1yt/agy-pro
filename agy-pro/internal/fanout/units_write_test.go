package fanout

// B4-1 pins: the per-unit write opt-in in the plan format.
//
// "write" is an optional boolean on a plan unit (default false). ParsePlan
// honors it verbatim, it never weakens any other plan rule (required fields,
// duplicate ids, scope overlap, the unit cap, root-wide rejection), and the
// E2 plan-error redaction keeps masking key-shaped values in errors raised by
// plans that carry the new field.

import (
	"strings"
	"testing"
)

// The write field defaults to false and "write": true is honored verbatim;
// unknown-to-write validation stays exactly as strict (a write unit missing a
// required field is still rejected).
func TestParsePlanWriteField(t *testing.T) {
	data := []byte(`[{"id":"u1","title":"t","scope":["src/a"],"deliverable":"d"},` +
		`{"id":"u2","title":"t","scope":["src/b"],"deliverable":"d","write":true}]`)
	units, err := ParsePlan(data)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("units = %d; want 2", len(units))
	}
	if units[0].Write {
		t.Errorf("unit without \"write\" got Write=true; want the default false")
	}
	if !units[1].Write {
		t.Errorf("unit with \"write\":true got Write=false; want true honored")
	}

	// A write unit missing a required field is still rejected.
	bad := []byte(`[{"id":"u1","title":"t","scope":["src/a"],"write":true}]`)
	if _, err := ParsePlan(bad); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Errorf("write unit missing deliverable: err = %v; want the required-field rejection", err)
	}
}

// E2 with the new field present: a plan whose unit ids are key-shaped and
// that carries "write": true must still ride every validation error through
// maskPlanError; the raw key-shaped id must never reach the error string.
func TestParsePlanWriteFieldKeepsMaskingKeyShapedIDs(t *testing.T) {
	// Overlapping scopes force an error that quotes both unit labels.
	overlap := []byte(`[{"id":"` + keyShapedUnitID + `","title":"a","scope":["src/a/*.go"],"deliverable":"da","write":true},` +
		`{"id":"u2","title":"b","scope":["src/a/b.go"],"deliverable":"db"}]`)
	_, err := ParsePlan(overlap)
	if err == nil {
		t.Fatal("ParsePlan accepted overlapping scopes; want the overlap rejection")
	}
	if strings.Contains(err.Error(), keyShapedUnitID) {
		t.Errorf("plan error leaks the raw key-shaped id:\n%s", err.Error())
	}

	// The duplicate-id path, with write on the second unit.
	dup := []byte(`[{"id":"u1","title":"a","scope":["src/a"],"deliverable":"d"},` +
		`{"id":"` + keyShapedUnitID + `","title":"b","scope":["src/b"],"deliverable":"d","write":true},{"id":"u1","title":"c","scope":["src/c"],"deliverable":"d"}]`)
	_, err = ParsePlan(dup)
	if err == nil {
		t.Fatal("ParsePlan accepted duplicate ids; want the duplicate rejection")
	}
	if strings.Contains(err.Error(), keyShapedUnitID) {
		t.Errorf("duplicate-id error leaks the raw key-shaped id:\n%s", err.Error())
	}
}
