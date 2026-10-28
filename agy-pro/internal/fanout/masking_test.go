package fanout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// C2: the unresolved-model errors on the seat/spec resolution paths
// (selector.go explicit picks, engine.go explicit seats) are operand echoes.
// A misdirected key paste in a model slot must come back masked, never raw.
const maskSweepKey = "sk-planted-fanout-echo-key-0123456789abcdef"

func TestSelectTargetsMasksUnresolvedExplicitModel(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}},
	}}}

	_, err := SelectTargets(reg, nil, 1, []string{maskSweepKey}, nil)
	if err == nil {
		t.Fatal("SelectTargets must reject an unresolvable explicit model")
	}
	if strings.Contains(err.Error(), maskSweepKey) {
		t.Errorf("SelectTargets error echoes the planted key raw: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pl****") {
		t.Errorf("SelectTargets error should carry the readable mask: %v", err)
	}

	// Benign specs keep their readable form.
	_, err = SelectTargets(reg, nil, 1, []string{"nope-model"}, nil)
	if err == nil || !strings.Contains(err.Error(), `"nope-model"`) {
		t.Errorf("benign spec must stay readable in the error, got %v", err)
	}
}

func TestExecuteMasksUnresolvedSeatModel(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "providers.json")
	regJSON := `{"providers":[{"name":"nw","baseURL":"http://127.0.0.1:1","apiKeyEnv":"NW_KEY","models":[{"id":"glm-5.3-flash"}]}]}`
	if err := os.WriteFile(regPath, []byte(regJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Execute(context.Background(), RunConfig{
		Seats:        []SeatSpec{{Kind: SeatKindProvider, Raw: maskSweepKey, ModelID: maskSweepKey}},
		RegistryPath: regPath,
		TimeoutSec:   1,
	})
	if err == nil {
		t.Fatal("Execute must fail on an unresolvable provider seat")
	}
	if strings.Contains(err.Error(), maskSweepKey) {
		t.Errorf("Execute error echoes the planted key raw: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pl****") {
		t.Errorf("Execute error should carry the readable mask: %v", err)
	}
}
