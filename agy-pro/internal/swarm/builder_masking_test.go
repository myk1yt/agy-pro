package swarm

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// Plants shared by the E7/E8 masking tests: a Claude-style key, an AWS access
// key ID, an AWS secret access key, and the first-5 masks MaskSecret renders
// for them.
const (
	skPlant   = testfixtures.AnthropicPlanted
	skMask    = "sk-an****"
	akiaPlant = testfixtures.AKIAExample
	akiaMask  = "AKIAI****"
	awsPlant  = testfixtures.AWSSecretNoSlash
	awsMask   = "wJalr****"
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
// "1-<aws>", "1-<asia>", a non-eyJ JWT). The two echo sites mask through
// different passes: the template error seeds providers.MaskSentence with the
// raw line and renders [REDACTED], while the seat-selection error is already
// masked at the inventory source to the canonical first-5 form (the U8 rule)
// and the builder's seeded pass keeps that form.
func TestBuilderMasksPlantedTTYTokens(t *testing.T) {
	const (
		awsPlant   = testfixtures.AWSSecretNoSlash
		nativeP    = "native:" + awsPlant
		rangeP     = "1-" + awsPlant
		asiaP      = "1-" + testfixtures.ASIADocExample
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
		// Every plant renders through the canonical first-5 mask (the U8 rule)
		// at the inventory error source; the builder's seeded sentence pass
		// keeps that form (I3), so the expectations below pin first-5 masks
		// rather than [REDACTED].
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
		awsPlant   = testfixtures.AWSSecretNoSlash
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

	t.Run("explicit k3 pick is honored with a masked spec", func(t *testing.T) {
		// D2 = b: an explicit numbered pick of an excluded row is HONORED with
		// the shared warning - never rejected - and the warning still masks a
		// key-shaped registry-derived spec.
		var buf bytes.Buffer
		seats, err := ResolveSeats("1", k3Inv(t), &buf)
		if err != nil {
			t.Fatalf("explicit pick of an excluded row must be honored: %v", err)
		}
		if len(seats) != 1 || seats[0].Number != 1 {
			t.Fatalf("explicit pick seated %+v, want the named row", seats)
		}
		out := buf.String()
		if strings.Contains(out, awsPlant) {
			t.Errorf("explicit-pick warning leaks the planted model ID raw:\n%s", out)
		}
		if !strings.Contains(out, redactedMk) || !strings.Contains(out, "explicit pick honored") {
			t.Errorf("explicit-pick warning missing the mask or the shared sentence:\n%s", out)
		}
		buf.Reset()
		seats, err = ResolveSeats("2", k3Inv(t), &buf)
		if err != nil {
			t.Fatalf("explicit pick of the benign excluded row must be honored: %v", err)
		}
		if len(seats) != 1 || !strings.Contains(buf.String(), "glm-k3-pro-benign") {
			t.Errorf("benign k3 spec must stay readable in the warning: %v\n%s", seats, buf.String())
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

// --- C1/S3/C4: the step-4 prompt slot (builder.go printBudgetAndPreview) ---

// builderInv is the standard 2-entry fixture (native + provider) the prompt
// tests drive the builder with.
func builderInv(t *testing.T) *inventory.Inventory {
	t.Helper()
	return buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})
}

// lastPromptSlotLine returns the last rendered "  4. prompt   ..." line (the
// slot is re-rendered after every edit, so the last one carries the edit).
func lastPromptSlotLine(t *testing.T, out string) string {
	t.Helper()
	last := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  4. prompt   ") {
			last = line
		}
	}
	if last == "" {
		t.Fatalf("no step-4 prompt slot rendered:\n%s", out)
	}
	return last
}

// runBuilderToPrompt drives the builder to the budget loop, pastes prompt at
// edit choice "4", then cancels; it returns stdout.
func runBuilderToPrompt(t *testing.T, inv *inventory.Inventory, prompt string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader("1\n1\n4\n" + prompt + "\nq\n"), Out: &out, ErrW: &errOut, Inv: inv}
	if code := b.Run(); code != 0 {
		t.Fatalf("builder exit = %d; want 0 (cancel)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	return out.String()
}

// TestBuilderPromptSlotMasksShortBareKey (C1): the mask used to sit inside the
// `len(prompt) > 60` branch, so a <=60-byte key-shaped prompt pasted at edit
// choice "4" printed raw at slot 4. The mask is now unconditional.
func TestBuilderPromptSlotMasksShortBareKey(t *testing.T) {
	out := runBuilderToPrompt(t, builderInv(t), skPlant)
	if strings.Contains(out, skPlant) {
		t.Errorf("builder echoes the planted key raw somewhere in stdout:\n%s", out)
	}
	slot := lastPromptSlotLine(t, out)
	if !strings.Contains(slot, "[REDACTED]") {
		t.Errorf("step-4 slot must render the seeded mask, got %q", slot)
	}
}

// TestBuilderPromptSlotMasksLongMultiWordPrompt (C1/S3/C4): a >60-byte
// multi-word prompt with an embedded bare key renders masked at slot 4 and on
// the command preview line. The seed set is the whole prompt plus each
// whitespace-separated token (providers.FlagSeedOperands); the per-token seed
// is what masks when the whole prompt is not itself seed-eligible (a
// location-shaped prompt never seeds as a whole).
func TestBuilderPromptSlotMasksLongMultiWordPrompt(t *testing.T) {
	t.Run("bare key in a sentence", func(t *testing.T) {
		prompt := "please review the attached diff and check " + testfixtures.AnthropicPlanted + " before merging upstream"
		if len(prompt) <= 60 {
			t.Fatalf("test prompt must exceed 60 bytes, got %d", len(prompt))
		}
		out := runBuilderToPrompt(t, builderInv(t), prompt)
		slot := lastPromptSlotLine(t, out)
		if strings.Contains(slot, skPlant) {
			t.Errorf("step-4 slot echoes the planted key raw: %q", slot)
		}
		if !strings.Contains(slot, "[REDACTED]") {
			t.Errorf("step-4 slot missing the mask: %q", slot)
		}
		// The command preview line masks per argument (FormatCommand ->
		// MaskOperand -> generic redactor), so the key must not survive there
		// either.
		if strings.Contains(out, skPlant) {
			t.Errorf("preview line echoes the planted key raw:\n%s", out)
		}
	})

	t.Run("per-token seed: bare key in a location-shaped sentence", func(t *testing.T) {
		// The "://" makes the whole prompt location-shaped, so it can never
		// seed as a whole; only the FlagSeedOperands token seeds, which is
		// exactly the multi-token gap those seeds exist for.
		prompt := "compare https://api.example.com/v1/spec with " + awsPlant + " and report differences"
		if len(prompt) <= 60 {
			t.Fatalf("test prompt must exceed 60 bytes, got %d", len(prompt))
		}
		out := runBuilderToPrompt(t, builderInv(t), prompt)
		slot := lastPromptSlotLine(t, out)
		if strings.Contains(slot, awsPlant) {
			t.Errorf("step-4 slot echoes the planted key raw: %q", slot)
		}
		if !strings.Contains(slot, "[REDACTED]") {
			t.Errorf("step-4 slot missing the per-token mask: %q", slot)
		}
		if !strings.Contains(slot, "compare https://api.example.com/v1/spec with") {
			t.Errorf("benign prompt wording must stay readable: %q", slot)
		}
		// The command preview line masks per argument via MaskOperand
		// (FormatCommand H5); a sentence-shaped prompt carrying an
		// unrecognized bare secret there is the gate-acknowledged residual
		// (code-reviewer-final-report.md non-goals), outside this batch.
	})
}

// TestBuilderPromptSlotSeedListIsFlagSeedOperands (batch K, swarm package):
// printBudgetAndPreview seeds MaskSentence with exactly
// providers.FlagSeedOperands(prompt). That helper already returns the whole
// prompt first and then each comma/space/semicolon-separated token, so the old
// append([]string{prompt}, providers.FlagSeedOperands(prompt)...) prepended a
// duplicate that MaskSentence deduped away: dropping it leaves every rendering
// byte-identical. These pins hold the renderings the WHOLE-VALUE seed is
// responsible for, so a later "simplification" that seeds tokens only (and so
// loses the whole value) fails here.
func TestBuilderPromptSlotSeedListIsFlagSeedOperands(t *testing.T) {
	t.Run("whole-value seed masks a squash-shaped multi-word prompt", func(t *testing.T) {
		// The squashed whole value is one 38-rune opaque run (>= the 21-rune
		// decorated floor), so it seeds as a whole. Token 2
		// ("K7MDENGbPxRfiCYEXAMPLEKEY", 25 runes) clears the 16-rune
		// contiguous-run floor and seeds on its own, but token 1
		// ("wJalrXUtnFEMI", 13 runes) is below every floor and reachable ONLY
		// through the whole-value seed: token-only seeding would render
		// "wJalrXUtnFEMI [REDACTED]" and leak the first half raw, so this pin
		// fails without the whole-value seed.
		prompt := "wJalrXUtnFEMI K7MDENGbPxRfiCYEXAMPLEKEY"
		if providers.SeedEligible(prompt) != true {
			t.Fatalf("fixture must stay whole-value seed eligible: %q", prompt)
		}
		out := runBuilderToPrompt(t, builderInv(t), prompt)
		slot := lastPromptSlotLine(t, out)
		if slot != "  4. prompt   [REDACTED]" {
			t.Errorf("whole-value seed must mask the prompt wholly, got %q", slot)
		}
		if strings.Contains(out, "wJalrXUtnFEMI") || strings.Contains(out, "K7MDENGbPxRfiCYEXAMPLEKEY") {
			t.Errorf("builder echoes a half of the plant raw:\n%s", out)
		}
	})

	t.Run("whole-value seed masks a benign sentence around a bare key", func(t *testing.T) {
		// Token-only seeding would render "[REDACTED] extra words around the
		// key" (the redactor masks a seeded token as [REDACTED], never as the
		// first-5 form); the whole-value seed renders the value masked as one
		// unit.
		prompt := akiaPlant + " extra words around the key"
		out := runBuilderToPrompt(t, builderInv(t), prompt)
		slot := lastPromptSlotLine(t, out)
		if slot != "  4. prompt   [REDACTED]" {
			t.Errorf("whole-value seed must mask the prompt wholly, got %q", slot)
		}
		if strings.Contains(slot, "extra words") {
			t.Errorf("benign sentence wording must be masked with the whole value, not left readable: %q", slot)
		}
	})

	t.Run("rendering equals the FlagSeedOperands composition", func(t *testing.T) {
		prompts := []string{
			"hello",
			"fix the login bug and add tests",
			"native:gemini-3.8-flash-high",
			skPlant,
			akiaPlant,
			awsPlant,
			"please review the attached diff and check " + testfixtures.AnthropicPlanted + " before merging upstream",
			"compare https://api.example.com/v1/spec with " + awsPlant + " and report differences",
			"Bearer " + awsPlant,
			"wJalrXUtnFEMI K7MDENGbPxRfiCYEXAMPLEKEY",
			"请 review " + strings.Repeat("这个仓库的安全问题并报告发现", 5),
			strings.Repeat("abcdefghij", 12),
		}
		for _, p := range prompts {
			out := runBuilderToPrompt(t, builderInv(t), p)
			slot := lastPromptSlotLine(t, out)
			want := providers.MaskSentence(p, providers.FlagSeedOperands(p)...)
			if utf8.RuneCountInString(want) > 60 {
				want = string([]rune(want)[:57]) + "..."
			}
			if want := "  4. prompt   " + want; slot != want {
				t.Errorf("prompt %q (runes=%d):\n got %q\nwant %q", p, utf8.RuneCountInString(p), slot, want)
			}
		}
	})
}

// TestBuilderPromptSlotTruncatesOnRuneBoundary (S3): the old prompt[:57]
// byte-slice split a multi-byte rune and rendered U+FFFD; truncation now cuts
// on the rune boundary.
func TestBuilderPromptSlotTruncatesOnRuneBoundary(t *testing.T) {
	prompt := "请 review " + strings.Repeat("这个仓库的安全问题并报告发现", 5)
	if len(prompt) <= 60 || utf8.RuneCountInString(prompt) <= 60 {
		t.Fatalf("prompt must exceed 60 bytes and 60 runes: bytes=%d runes=%d", len(prompt), utf8.RuneCountInString(prompt))
	}
	if utf8.ValidString(prompt[:57]) {
		t.Fatalf("test prompt must carry a rune straddling byte 57 (the old byte-slice cut cleanly)")
	}
	out := runBuilderToPrompt(t, builderInv(t), prompt)
	slot := lastPromptSlotLine(t, out)
	if strings.Contains(slot, "\uFFFD") {
		t.Errorf("step-4 slot renders a replacement char (rune split): %q", slot)
	}
	if !utf8.ValidString(slot) {
		t.Errorf("step-4 slot is not valid UTF-8: %q", slot)
	}
	if !strings.HasSuffix(slot, "...") {
		t.Errorf("step-4 slot must be truncated with an ellipsis: %q", slot)
	}
	if !strings.Contains(slot, "这个仓库") {
		t.Errorf("truncated prompt lost its multi-byte content: %q", slot)
	}
}

// TestRenderSeatListMasksKeyShapedGroupAndDisplayName (S5): the seat list
// echoes the registry-derived group (provider name) and the native display
// name; key-shaped ones render masked while benign IDs stay readable.
func TestRenderSeatListMasksKeyShapedGroupAndDisplayName(t *testing.T) {
	const (
		keyGroup   = testfixtures.AKIAExample
		keyDisplay = testfixtures.AWSSecretExample
	)
	inv := buildInventory(t,
		[]inventory.NativeModel{
			{ID: "gemini-3.8-flash-high", DisplayName: keyDisplay},
			{ID: "glm-5.3-flash", DisplayName: "Flash"},
		},
		[]providers.Provider{{Name: keyGroup, Models: []providers.Model{{ID: "gemini-2.5-flash"}}}})
	var buf bytes.Buffer
	RenderSeatList(&buf, inv)
	out := buf.String()
	if strings.Contains(out, keyGroup) || strings.Contains(out, keyDisplay) {
		t.Errorf("seat list leaks a key-shaped Group/DisplayName:\n%s", out)
	}
	if !strings.Contains(out, "[AKIAI****]") {
		t.Errorf("key-shaped provider name (Group) not masked:\n%s", out)
	}
	if !strings.Contains(out, "wJalr****") {
		t.Errorf("key-shaped native DisplayName not masked:\n%s", out)
	}
	if !strings.Contains(out, "gemini-3.8-flash-high") || !strings.Contains(out, "Flash") {
		t.Errorf("benign model IDs and display names must stay readable:\n%s", out)
	}
}

// TestRenderSeatListMasksLookAlikeRegistrySlot pins the seat-list conversion:
// the unnamed seat listing renders the same inventory entries as
// inventory.RenderNumbered, so its id and mid columns route through
// providers.MaskSlot too. The plant discriminates the pre-policy masker:
// providers.MaskOperand is identity on it, so the pre-conversion listing
// printed the credential core raw while the models --json twin masked it.
func TestRenderSeatListMasksLookAlikeRegistrySlot(t *testing.T) {
	const core = "ab3def7gh9jk1mnpqr5t"
	const masked = "ab3de****"
	plant := "\u2025/" + core
	if got := providers.MaskOperand(plant); got != plant {
		t.Fatalf("premise broken: MaskOperand is not identity on the plant (%q)", got)
	}

	inv := buildInventory(t,
		[]inventory.NativeModel{
			{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
			{ID: "gpt-oss-120b-medium", DisplayName: plant},
		},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: plant}, {ID: "glm-5.3-flash-flex"}}}})
	var buf bytes.Buffer
	RenderSeatList(&buf, inv)
	out := buf.String()
	if strings.Contains(out, core) || strings.Contains(out, plant) {
		t.Errorf("seat list leaks the look-alike plant raw:\n%s", out)
	}
	if strings.Count(out, masked) != 2 {
		t.Errorf("seat list must mask the id column and the mid column to %q:\n%s", masked, out)
	}
	if !strings.Contains(out, "glm-5.3-flash-flex") || !strings.Contains(out, "Gemini 3.8 Flash (High)") {
		t.Errorf("benign rows must stay readable:\n%s", out)
	}
}

// TestPickTemplateMasksMultiWordLineWithEmbeddedSecret (C5/S2): the invalid
// -template echo is a whole-line seed site (builder.go:194). With the wide
// seed class a multi-word misdirected paste embedding a bare secret is
// SeedEligible, so the quoted line renders [REDACTED] while the surrounding
// wording stays readable.
func TestPickTemplateMasksMultiWordLineWithEmbeddedSecret(t *testing.T) {
	const line = "here is the key " + awsPlant
	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader(line + "\n"), Out: &out, ErrW: &errOut, Inv: builderInv(t)}
	if code := b.Run(); code != 2 {
		t.Fatalf("builder exit = %d; want 2 (input failure)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
	}
	if strings.Contains(errOut.String(), awsPlant) {
		t.Errorf("template error echoes the planted key raw:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), `invalid template "[REDACTED]"`) {
		t.Errorf("template error missing the whole-line seed mask:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "enter 1-") {
		t.Errorf("template error lost its actionable wording:\n%s", errOut.String())
	}
}

// ---------------------------------------------------------------------------
// FIX-8: RenderSummary's model column was the last exempt operand-masker user
// on a registry slot (the residual recorded since the phase-2 masking mission).
// It now renders through providers.MaskSlot, the same authority the seat list
// uses, so the underscore-head credential class masks here too. MaskOperand is
// that authority's first stage, so every benign table line stays byte-identical.
// ---------------------------------------------------------------------------

func TestRenderSummaryMasksUnderscoreHeadModelID(t *testing.T) {
	const (
		plant     = testfixtures.SKLive
		plantMask = "sk_li****"
	)
	var buf bytes.Buffer
	RenderSummary(&buf, []fanout.SeatResult{{
		SeatID:      1,
		Kind:        "provider",
		FinalTarget: providers.Target{Model: providers.Model{ID: plant}},
		Status:      "success",
	}}, false)
	if strings.Contains(buf.String(), plant) {
		t.Errorf("summary leaks the credential-shaped model id:\n%s", buf.String())
	}
	if want := "seat | kind | model | verdict\n1 | provider | " + plantMask + " | success\n"; buf.String() != want {
		t.Errorf("summary drifted\n got: %q\nwant: %q", buf.String(), want)
	}

	// Benign model ids: byte-identical table lines (the whole rendering, header
	// included), for the corpus the sibling sweep pins plus the values here.
	for _, id := range []string{"openai", "gemini", "deepseek", "nw", "charm", "acme-corp", "glm-5.3-flash", "gpt-oss-120b"} {
		buf.Reset()
		RenderSummary(&buf, []fanout.SeatResult{{
			SeatID:      1,
			Kind:        "provider",
			FinalTarget: providers.Target{Model: providers.Model{ID: id}},
			Status:      "success",
		}}, false)
		if want := "seat | kind | model | verdict\n1 | provider | " + id + " | success\n"; buf.String() != want {
			t.Errorf("benign model id %q drifted\n got: %q\nwant: %q", id, buf.String(), want)
		}
	}
}

// ---------------------------------------------------------------------------
// The swarm twin of the preset surface: the interactive seat picker's saved
// preset listing (builder.go pickSeats) composed the stored name and numbers
// operands raw, the same O6-class sink the three preset surfaces already render
// through providers.MaskOperand. Both operands route through that masker now,
// and a benign pair keeps the exact bytes the raw composition wrote.
// ---------------------------------------------------------------------------

// presetStoreStub is the in-memory swarm.Store the picker pin reads.
type presetStoreStub struct{ presets []SavedPreset }

func (s presetStoreStub) LoadPresets() ([]SavedPreset, error) { return s.presets, nil }
func (s presetStoreStub) SavePresets([]SavedPreset) error     { return nil }

// savedPresetBlock returns the rendered "Saved presets:" section, from its
// header line up to the following section header.
func savedPresetBlock(t *testing.T, out string) string {
	t.Helper()
	const header = "Saved presets:\n"
	start := strings.Index(out, header)
	if start < 0 {
		t.Fatalf("seat picker did not render the saved-preset section:\n%s", out)
	}
	rest := out[start:]
	end := strings.Index(rest, "Numbered inventory:\n")
	if end < 0 {
		t.Fatalf("saved-preset section has no following inventory header:\n%s", out)
	}
	return rest[:end]
}

// TestPickSeatsMasksSavedPresetOperands pins the seat picker's saved-preset
// listing: a credential-shaped name and a credential-shaped numbers field
// render first-5+**** (neither the plant nor its post-5 tail survives), while a
// benign name and numbers list render the exact line the raw composition wrote.
func TestPickSeatsMasksSavedPresetOperands(t *testing.T) {
	const (
		presetName     = skPlant // sk-an****
		presetNameMask = skMask
		presetNums     = awsPlant // wJalr****
		presetNumsMask = awsMask
		presetNameTail = "t-api03-PLANT0123456789abcdef"
		presetNumsTail = "XUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
	)
	if providers.MaskOperand(presetName) == presetName || providers.MaskOperand(presetNums) == presetNums {
		t.Fatalf("premise broken: providers.MaskOperand is the identity on a plant")
	}

	var out, errOut bytes.Buffer
	b := &Builder{Out: &out, ErrW: &errOut, Inv: builderInv(t), Store: presetStoreStub{presets: []SavedPreset{
		{Name: presetName, Numbers: "1,3"},
		{Name: "quick", Numbers: presetNums},
	}}}
	if _, ok := b.pickSeats(bufio.NewReader(strings.NewReader("p1\n")), &out); !ok {
		t.Fatalf("seat picker failed on a valid selection\nstdout:\n%s\nstderr:\n%s", out.String(), errOut.String())
	}
	block := savedPresetBlock(t, out.String())
	for _, want := range []string{
		"  " + presetNameMask + " = 1,3\n",
		"  quick = " + presetNumsMask + "\n",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("saved-preset listing missing %q\n%s", want, block)
		}
	}
	for _, raw := range []string{presetName, presetNums, presetNameTail, presetNumsTail} {
		if strings.Contains(block, raw) {
			t.Errorf("saved-preset listing echoes %q raw\n%s", raw, block)
		}
	}

	// The identity half: a benign pair renders byte-identically to the raw
	// composition, so the conversion cannot move a pinned benign line.
	benign := []SavedPreset{
		{Name: "p9", Numbers: "1,3,19,23"},
		{Name: "deep-review", Numbers: "2,5"},
		{Name: "swift", Numbers: "1"},
	}
	for _, p := range benign {
		if providers.MaskOperand(p.Name) != p.Name || providers.MaskOperand(p.Numbers) != p.Numbers {
			t.Fatalf("premise broken: providers.MaskOperand is not the identity on the benign preset %+v", p)
		}
	}
	var benignOut, benignErr bytes.Buffer
	bb := &Builder{Out: &benignOut, ErrW: &benignErr, Inv: builderInv(t), Store: presetStoreStub{presets: benign}}
	if _, ok := bb.pickSeats(bufio.NewReader(strings.NewReader("p1\n")), &benignOut); !ok {
		t.Fatalf("seat picker failed on a valid selection\nstdout:\n%s\nstderr:\n%s", benignOut.String(), benignErr.String())
	}
	const wantBlock = "Saved presets:\n  p9 = 1,3,19,23\n  deep-review = 2,5\n  swift = 1\n"
	if got := savedPresetBlock(t, benignOut.String()); got != wantBlock {
		t.Errorf("benign saved-preset section drifted from the raw composition\n got: %q\nwant: %q", got, wantBlock)
	}
	if strings.Contains(benignOut.String(), "****") {
		t.Errorf("a benign saved preset was masked:\n%s", benignOut.String())
	}
}
