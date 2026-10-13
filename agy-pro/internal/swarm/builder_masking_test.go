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

// TestBuilderMasksPlantedTTYTokens (E7/F5): the interactive builder echoes raw
// TTY lines in its invalid-template / invalid-selection errors; a misdirected
// key paste must come back masked, benign tokens readable. F5 adds the
// decorated class that whole-string classification cannot see ("native:<aws>",
// "1-<aws>", "1-<asia>", a non-eyJ JWT): those mask via the seeded sentence
// pass (providers.MaskSentence) and render as [REDACTED].
func TestBuilderMasksPlantedTTYTokens(t *testing.T) {
	const (
		awsPlant   = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
		nativeP    = "native:" + awsPlant
		rangeP     = "1-" + awsPlant
		asiaP      = "1-ASIAIOSFODNN7EXAMPLE"
		jwtPlant   = "aaaaab.cccccc.dddddd" // non-eyJ, all-lowercase: escapes the squash-uppercase rule
		redactedMk = "[REDACTED]"
	)
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})

	t.Run("invalid template token", func(t *testing.T) {
		plants := []string{skPlant, akiaPlant, nativeP, rangeP, asiaP, jwtPlant}
		for _, plant := range plants {
			var out, errOut bytes.Buffer
			b := &Builder{In: strings.NewReader(plant + "\n"), Out: &out, ErrW: &errOut, Inv: inv}
			if code := b.Run(); code != 2 {
				t.Fatalf("builder exit = %d; want 2 (input failure)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
			}
			if strings.Contains(errOut.String(), plant) {
				t.Errorf("template error echoes the planted key %q raw:\n%s", plant, errOut.String())
			}
			if !strings.Contains(errOut.String(), `invalid template "`+redactedMk+`"`) {
				t.Errorf("template error missing the seeded mask for %q:\n%s", plant, errOut.String())
			}
		}
	})

	t.Run("invalid seat selection token", func(t *testing.T) {
		// Decorated plants are masked by the builder's seeded sentence pass
		// ([REDACTED]); sk-/AKIA shapes are already masked at the inventory
		// error source (first-5 + ****) and the builder pass keeps that form.
		cases := []struct{ plant, mask string }{
			// I3: the decorated class renders through the canonical first-5
			// mask (the U8 rule) at the inventory error source; the seeded
			// builder pass keeps that form.
			{nativeP, `invalid selection token "nativ****"`},
			{rangeP, `invalid selection token "1-wJa****"`},
			{asiaP, `invalid selection token "1-ASI****"`},
			{jwtPlant, `invalid selection token "aaaaa****"`},
			{skPlant, `invalid selection token "` + skMask + `"`},
			{akiaPlant, `invalid selection token "` + akiaMask + `"`},
		}
		for _, tc := range cases {
			var out, errOut bytes.Buffer
			b := &Builder{In: strings.NewReader("1\n" + tc.plant + "\n"), Out: &out, ErrW: &errOut, Inv: inv}
			if code := b.Run(); code != 2 {
				t.Fatalf("builder exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
			}
			if strings.Contains(errOut.String(), tc.plant) {
				t.Errorf("seat selection error echoes the planted key %q raw:\n%s", tc.plant, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.mask) {
				t.Errorf("seat selection error missing the mask %q:\n%s", tc.mask, errOut.String())
			}
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

// TestResolveSeatsAndSeatLineMaskRegistryDerivedPlants (F7): the
// nil-inventory branch echoes the raw selection line, the k3 warning and
// rejection sentences embed the registry-derived spec, and SeatLine renders
// the spec column; key-shaped values must render masked, benign ones readable.
func TestResolveSeatsAndSeatLineMaskRegistryDerivedPlants(t *testing.T) {
	const (
		awsPlant   = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
		nativeP    = "native:" + awsPlant
		redactedMk = "[REDACTED]"
	)

	t.Run("nil inventory error masks the selection line", func(t *testing.T) {
		_, err := ResolveSeats(nativeP, nil, nil)
		if err == nil {
			t.Fatal("nil inventory must error")
		}
		if strings.Contains(err.Error(), awsPlant) || strings.Contains(err.Error(), nativeP) {
			t.Errorf("nil-inventory error leaks the plant raw: %v", err)
		}
		if !strings.Contains(err.Error(), redactedMk) {
			t.Errorf("nil-inventory error missing the mask: %v", err)
		}
		_, err = ResolveSeats("zz", nil, nil)
		if err == nil || !strings.Contains(err.Error(), `invalid selection token "zz"`) {
			t.Errorf("benign selection line must stay readable: %v", err)
		}
	})

	// Native rows carrying the k3 token: entry 1 is key-shaped (a misdirected
	// paste in the registry model id), entry 2 is benign.
	k3Inv := func(t *testing.T) *inventory.Inventory {
		return buildInventory(t,
			[]inventory.NativeModel{
				{ID: "flash-k3-native-" + awsPlant, DisplayName: "Planted"},
				{ID: "glm-k3-pro-benign", DisplayName: "Benign"},
			},
			nil)
	}

	t.Run("explicit k3 pick rejects with masked spec", func(t *testing.T) {
		_, err := ResolveSeats("1", k3Inv(t), nil)
		if err == nil {
			t.Fatal("explicit k3 pick must be rejected")
		}
		if strings.Contains(err.Error(), awsPlant) {
			t.Errorf("k3 rejection leaks the planted model ID raw: %v", err)
		}
		if !strings.Contains(err.Error(), redactedMk) {
			t.Errorf("k3 rejection missing the mask: %v", err)
		}
		_, err = ResolveSeats("2", k3Inv(t), nil)
		if err == nil || !strings.Contains(err.Error(), "glm-k3-pro-benign") {
			t.Errorf("benign k3 spec must stay readable in the rejection: %v", err)
		}
	})

	t.Run("preset k3 drop warns with masked spec", func(t *testing.T) {
		var buf bytes.Buffer
		kept, err := ResolveSeats("p3", k3Inv(t), &buf)
		if err != nil {
			t.Fatalf("p3 resolve: %v", err)
		}
		if len(kept) != 0 {
			t.Errorf("k3 rows must be dropped from the preset, kept = %d", len(kept))
		}
		out := buf.String()
		if strings.Contains(out, awsPlant) {
			t.Errorf("k3 drop warning leaks the planted model ID raw:\n%s", out)
		}
		if !strings.Contains(out, redactedMk) || !strings.Contains(out, "skipped seat") {
			t.Errorf("k3 drop warning missing the masked row:\n%s", out)
		}
		if !strings.Contains(out, "glm-k3-pro-benign") {
			t.Errorf("benign k3 spec must stay readable in the warning:\n%s", out)
		}
	})

	t.Run("SeatLine masks key-shaped specs", func(t *testing.T) {
		line := SeatLine(1, inventory.Entry{Number: 1, Kind: "provider", Spec: "nw/" + awsPlant, ModelID: awsPlant, DisplayName: "Neural"})
		if strings.Contains(line, awsPlant) {
			t.Errorf("SeatLine leaks the planted model ID raw: %s", line)
		}
		if !strings.Contains(line, redactedMk) {
			t.Errorf("SeatLine missing the mask: %s", line)
		}
		benign := SeatLine(2, inventory.Entry{Number: 2, Kind: "native", Spec: "native:gemini-3.8-flash-high", ModelID: "gemini-3.8-flash-high", DisplayName: "Flash"})
		if !strings.Contains(benign, "native:gemini-3.8-flash-high") || !strings.Contains(benign, "[native]") {
			t.Errorf("benign seat spec must stay readable: %s", benign)
		}
	})
}
