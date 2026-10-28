package providers

import (
	"strings"
	"testing"
)

// C2: FindTarget's unresolved-spec error is an operand echo (selection.go).
// A misdirected key paste (`--seats sk-...` lands here via the fanout seat
// path) must come back masked, never raw.
func TestFindTargetErrorMasksKeyShapedSpec(t *testing.T) {
	reg := &Registry{Providers: []Provider{{Name: "nw", Models: []Model{{ID: "glm-5.3-flash"}}}}}

	const planted = "sk-planted-selection-echo-key-0123456789abcdef"
	_, err := reg.FindTarget(planted)
	if err == nil {
		t.Fatal("FindTarget must reject an unknown spec")
	}
	if strings.Contains(err.Error(), planted) {
		t.Errorf("FindTarget error echoes the planted key raw: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pl****") {
		t.Errorf("FindTarget error should carry the readable mask: %v", err)
	}

	// Benign specs keep their readable form (actionable errors).
	_, err = reg.FindTarget("nope-model")
	if err == nil || !strings.Contains(err.Error(), `"nope-model"`) {
		t.Errorf("benign spec must stay readable in the error, got %v", err)
	}
}
