package swarm

import (
	"bytes"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// Plants shared by the E7/E8 masking tests: a Claude-style key, an AWS access
// key ID, and the first-5 masks MaskSecret renders for them.
const (
	skPlant   = "sk-ant-api03-PLANT0123456789abcdef"
	skMask    = "sk-an****"
	akiaPlant = "AKIAIOSFODNN7EXAMPLE"
	akiaMask  = "AKIAI****"
)

// TestRenderSummaryAndSeatListMaskKeyShapedModelIDs (E8): the summary table
// and the seat list echo registry-derived model IDs; key-shaped IDs must
// render as masks for display consistency with `status`/`models`, while
// benign IDs stay readable.
func TestRenderSummaryAndSeatListMaskKeyShapedModelIDs(t *testing.T) {
	seats := []fanout.SeatResult{
		{SeatID: 1, Kind: "provider", FinalTarget: providers.Target{Model: providers.Model{ID: skPlant}}, Status: "success"},
		{SeatID: 2, Kind: "provider", FinalTarget: providers.Target{Model: providers.Model{ID: akiaPlant}}, Status: "failed"},
		{SeatID: 3, Kind: "native", FinalTarget: providers.Target{Model: providers.Model{ID: "glm-5.3-flash"}}, Status: "success"},
	}
	var buf bytes.Buffer
	RenderSummary(&buf, seats, false)
	out := buf.String()
	if !strings.Contains(out, skMask) || !strings.Contains(out, akiaMask) {
		t.Errorf("summary must render masked model IDs, got:\n%s", out)
	}
	if strings.Contains(out, skPlant) || strings.Contains(out, akiaPlant) {
		t.Errorf("summary leaks a key-shaped model ID raw:\n%s", out)
	}
	if !strings.Contains(out, "glm-5.3-flash") {
		t.Errorf("benign model IDs must stay readable:\n%s", out)
	}

	inv := buildInventory(t, nil, []providers.Provider{{Name: "nw", Models: []providers.Model{
		{ID: skPlant},
		{ID: akiaPlant},
		{ID: "glm-5.3-flash"},
	}}})
	buf.Reset()
	RenderSeatList(&buf, inv)
	out = buf.String()
	if !strings.Contains(out, skMask) || !strings.Contains(out, akiaMask) {
		t.Errorf("seat list must render masked model IDs, got:\n%s", out)
	}
	if strings.Contains(out, skPlant) || strings.Contains(out, akiaPlant) {
		t.Errorf("seat list leaks a key-shaped model ID raw:\n%s", out)
	}
	if !strings.Contains(out, "glm-5.3-flash") {
		t.Errorf("benign model IDs must stay readable:\n%s", out)
	}
}

// TestBuilderMasksPlantedTTYTokens (E7): the interactive builder echoes raw
// TTY lines in its invalid-template / invalid-selection errors; a misdirected
// key paste must come back masked, benign tokens readable.
func TestBuilderMasksPlantedTTYTokens(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})

	t.Run("invalid template token", func(t *testing.T) {
		cases := []struct{ plant, mask string }{{skPlant, skMask}, {akiaPlant, akiaMask}}
		for _, tc := range cases {
			var out, errOut bytes.Buffer
			b := &Builder{In: strings.NewReader(tc.plant + "\n"), Out: &out, ErrW: &errOut, Inv: inv}
			if code := b.Run(); code != 2 {
				t.Fatalf("builder exit = %d; want 2 (input failure)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
			}
			if strings.Contains(errOut.String(), tc.plant) {
				t.Errorf("template error echoes the planted key raw:\n%s", errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.mask) {
				t.Errorf("template error missing the mask %q:\n%s", tc.mask, errOut.String())
			}
		}
	})

	t.Run("invalid seat selection token", func(t *testing.T) {
		var out, errOut bytes.Buffer
		b := &Builder{In: strings.NewReader("1\n" + akiaPlant + "\n"), Out: &out, ErrW: &errOut, Inv: inv}
		if code := b.Run(); code != 2 {
			t.Fatalf("builder exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
		}
		if strings.Contains(errOut.String(), akiaPlant) {
			t.Errorf("seat selection error echoes the planted key raw:\n%s", errOut.String())
		}
		if !strings.Contains(errOut.String(), akiaMask) {
			t.Errorf("seat selection error missing the mask %q:\n%s", akiaMask, errOut.String())
		}
	})

	t.Run("benign tokens stay readable", func(t *testing.T) {
		var out, errOut bytes.Buffer
		b := &Builder{In: strings.NewReader("not-a-template\n"), Out: &out, ErrW: &errOut, Inv: inv}
		b.Run()
		if !strings.Contains(errOut.String(), `invalid template "not-a-template"`) {
			t.Errorf("benign template token must stay readable:\n%s", errOut.String())
		}

		out, errOut = bytes.Buffer{}, bytes.Buffer{}
		b = &Builder{In: strings.NewReader("1\np9\n"), Out: &out, ErrW: &errOut, Inv: inv}
		b.Run()
		if !strings.Contains(errOut.String(), `invalid selection token "p9"`) {
			t.Errorf("benign seat token must stay readable:\n%s", errOut.String())
		}
	})
}
