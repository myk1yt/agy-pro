package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// D3 planted-key slots: the registry-driven display lines (status/models
// inventory, doctor key audits, --fix-keys targets and the repair report) echo
// user-editable JSON fields. A misdirected paste can land a key-shaped value
// in a provider name, baseURL, env name or model id, so every such slot must
// route through providers.MaskOperand: credential-shaped values are masked,
// benign ones stay readable.

func TestD3StatusModelsSlotsMasked(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      sweepAWSKey,
		BaseURL:   "https://acme.example/v1",
		APIKeyEnv: maskSweepKey,
		Models: []providers.Model{{
			ID:           maskSweepKey,
			Capabilities: providers.Capabilities{SupportsTools: true},
		}},
	}}}
	out := captureStdout(t, func() { printConfiguredProvidersAndModels(reg) })
	for _, leak := range []string{maskSweepKey, sweepAWSKey} {
		if strings.Contains(out, leak) {
			t.Errorf("status/models output leaks the planted key %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{maskSweepMasked, sweepAWSMask, "https://acme.example/v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status/models output missing %q:\n%s", want, out)
		}
	}

	// Benign registry values stay byte-identical.
	benign := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		BaseURL:   "https://api.neuralwatt.com/v1",
		APIKeyEnv: "NW_API_KEY",
		Models:    []providers.Model{{ID: "glm-5.3-flash-flex", Family: "glm"}},
	}}}
	out = captureStdout(t, func() { printConfiguredProvidersAndModels(benign) })
	for _, want := range []string{"[1] nw (https://api.neuralwatt.com/v1)", "glm-5.3-flash-flex", "glm", "NW_API_KEY"} {
		if !strings.Contains(out, want) {
			t.Errorf("benign status/models output lost %q:\n%s", want, out)
		}
	}
}

// C2: the banner family slot masks the CASE-PRESERVED explicit registry
// family. Masking the folded FamilyName() instead destroyed the uppercase
// letter the decorated class requires, so a key-shaped family pasted into the
// registry family slot rode raw; MaskFamilyName keeps the case and masks. The
// plant is separator-decorated (hyphens), so its folded form is neither a
// contiguous base64 run nor identifier-class: only the preserved case can
// fire the decorated class.
func TestBannerFamilySlotMasksKeyShapedFamily(t *testing.T) {
	const familyPlant = "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	const familyPlantMask = "wJalr****"
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		BaseURL:   "https://api.example/v1",
		APIKeyEnv: "NW_API_KEY",
		Models:    []providers.Model{{ID: "glm-5.3-flash-flex", Family: familyPlant}},
	}}}
	out := captureStdout(t, func() { printConfiguredProvidersAndModels(reg) })
	if strings.Contains(out, familyPlant) {
		t.Errorf("banner family slot leaks the key-shaped family raw:\n%s", out)
	}
	if !strings.Contains(out, "Family: "+familyPlantMask) {
		t.Errorf("banner family slot must render the plant as %q:\n%s", familyPlantMask, out)
	}

	// Benign families stay readable, and an explicit mixed-case family keeps
	// its case (the folded rendering would have printed "gemini").
	benign := &providers.Registry{Providers: []providers.Provider{{
		Name:    "google",
		BaseURL: "https://api.example/v1",
		Models: []providers.Model{
			{ID: "gemini-2.5-flash", Family: "Gemini"},
			{ID: "glm-5.3-flash-flex"},
		},
	}}}
	out = captureStdout(t, func() { printConfiguredProvidersAndModels(benign) })
	for _, want := range []string{"Family: Gemini", "Family: glm"} {
		if !strings.Contains(out, want) {
			t.Errorf("benign banner family output lost %q:\n%s", want, out)
		}
	}
}

// D1: the composite-error sites seed the redactor with the raw operand, so a
// planted key is masked wherever it appears in the sentence while the operand
// slot keeps its readable mask.
func TestD1CompositeErrorMasksPlantedKeys(t *testing.T) {
	t.Run("swarm prompt-file AWS plant", func(t *testing.T) {
		defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
		var out, errw strings.Builder
		code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", sweepAWSKey},
			strings.NewReader(""), &out, &errw)
		if code != 1 {
			t.Fatalf("exit = %d; want 1\nstderr:\n%s", code, errw.String())
		}
		if strings.Contains(errw.String(), sweepAWSKey) {
			t.Errorf("prompt-file error leaks the planted key:\n%s", errw.String())
		}
		for _, want := range []string{sweepAWSMask, "[REDACTED]", "Error reading prompt file"} {
			if !strings.Contains(errw.String(), want) {
				t.Errorf("prompt-file error missing %q:\n%s", want, errw.String())
			}
		}
	})

	t.Run("swarm prompt-file benign path stays readable", func(t *testing.T) {
		defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
		var out, errw strings.Builder
		runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", "./prompts/missing.md"},
			strings.NewReader(""), &out, &errw)
		if !strings.Contains(errw.String(), "./prompts/missing.md") {
			t.Errorf("benign path must stay readable in the error:\n%s", errw.String())
		}
		if strings.Contains(errw.String(), "[REDACTED]") {
			t.Errorf("benign operand must not trigger any redaction:\n%s", errw.String())
		}
	})

	// E2: a hash-named relative path is not location-shaped, so the old
	// fragment extractor seeded its 16-rune hash and the composite sentence
	// printed "[REDACTED].md". Whole-operand seeding keeps it readable.
	t.Run("swarm prompt-file hash-named path stays readable", func(t *testing.T) {
		defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
		var out, errw strings.Builder
		runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", "prompts/3f2a8b9c1d2e3f4a.md"},
			strings.NewReader(""), &out, &errw)
		if !strings.Contains(errw.String(), "prompts/3f2a8b9c1d2e3f4a.md") {
			t.Errorf("hash-named relative path must stay readable in the error:\n%s", errw.String())
		}
		if strings.Contains(errw.String(), "[REDACTED]") {
			t.Errorf("benign operand must not trigger any redaction:\n%s", errw.String())
		}
	})

	t.Run("settings add-json AWS plant", func(t *testing.T) {
		installSettingsCore(t)
		_, _, errw := runSettings(t, []string{"providers", "add-json", sweepAWSKey}, "")
		if strings.Contains(errw, sweepAWSKey) {
			t.Errorf("add-json error leaks the planted key:\n%s", errw)
		}
		if !strings.Contains(errw, "[REDACTED]") {
			t.Errorf("add-json error should carry the redaction marker:\n%s", errw)
		}
	})
}

// D2: the mask runs on the ORIGINAL-CASE operand; masking the case-folded copy
// never fires the uppercase-anchored AKIA/ASIA credential class.
func TestD2OriginalCaseMasking(t *testing.T) {
	msg := invalidModeArgMessage(sweepAKIAKey)
	if strings.Contains(msg, sweepAKIAKey) {
		t.Errorf("mode error leaks the planted AKIA key:\n%s", msg)
	}
	if !strings.Contains(msg, sweepAKIAMask) {
		t.Errorf("mode error should carry the readable mask %q:\n%s", sweepAKIAMask, msg)
	}
	// Benign operands stay readable so the error stays actionable.
	if msg := invalidModeArgMessage("bogus"); !strings.Contains(msg, `"bogus"`) {
		t.Errorf("benign mode operand must stay readable:\n%s", msg)
	}
}

// D8: stdlib flag-set output routes through the echo-line redactor, so a
// planted key can never surface through a parse error (`invalid value "..."
// for flag -timeout`), undefined flag names cannot echo a key-shaped operand,
// and benign values stay readable.
func TestD8FlagOutputMasked(t *testing.T) {
	t.Run("swarm --timeout sk- plant", func(t *testing.T) {
		var out, errw strings.Builder
		code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--timeout", maskSweepKey},
			strings.NewReader(""), &out, &errw)
		if code != 2 {
			t.Fatalf("exit = %d; want 2\nstderr:\n%s", code, errw.String())
		}
		if strings.Contains(errw.String(), maskSweepKey) {
			t.Errorf("flag error leaks the planted key:\n%s", errw.String())
		}
		for _, want := range []string{maskSweepMasked, "invalid value", "for flag -timeout"} {
			if !strings.Contains(errw.String(), want) {
				t.Errorf("flag error missing %q:\n%s", want, errw.String())
			}
		}
	})

	t.Run("swarm --timeout AWS plant", func(t *testing.T) {
		var out, errw strings.Builder
		runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--timeout", sweepAWSKey},
			strings.NewReader(""), &out, &errw)
		if strings.Contains(errw.String(), sweepAWSKey) {
			t.Errorf("flag error leaks the planted key:\n%s", errw.String())
		}
		if !strings.Contains(errw.String(), sweepAWSMask) {
			t.Errorf("flag error should carry the readable mask:\n%s", errw.String())
		}
	})

	t.Run("swarm --timeout benign value stays readable", func(t *testing.T) {
		var out, errw strings.Builder
		runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--timeout", "abc"},
			strings.NewReader(""), &out, &errw)
		if !strings.Contains(errw.String(), `invalid value "abc"`) {
			t.Errorf("benign flag value must stay readable:\n%s", errw.String())
		}
	})

	t.Run("models undefined flag sk- plant", func(t *testing.T) {
		var out, errw strings.Builder
		code := runModelsCmdTo([]string{"--" + maskSweepKey}, &out, &errw)
		if code != 2 {
			t.Fatalf("exit = %d; want 2\nstderr:\n%s", code, errw.String())
		}
		if strings.Contains(errw.String(), maskSweepKey) {
			t.Errorf("undefined-flag error leaks the planted key:\n%s", errw.String())
		}
		if !strings.Contains(errw.String(), "[REDACTED]") {
			t.Errorf("undefined-flag error should carry the redaction marker:\n%s", errw.String())
		}
	})

	t.Run("models undefined flag AKIA plant", func(t *testing.T) {
		var out, errw strings.Builder
		runModelsCmdTo([]string{"--" + sweepAKIAKey}, &out, &errw)
		if strings.Contains(errw.String(), sweepAKIAKey) {
			t.Errorf("undefined-flag error leaks the planted key:\n%s", errw.String())
		}
		if !strings.Contains(errw.String(), "[REDACTED]") {
			t.Errorf("undefined-flag error should carry the redaction marker:\n%s", errw.String())
		}
	})
}

func TestD3DoctorAuditSlotsMasked(t *testing.T) {
	audits := []providers.KeyAudit{
		{
			Provider: sweepAWSKey, Env: maskSweepKey, Resolved: true,
			MaskedHint: "sk-…01",
			Probe:      providers.KeyProbeResult{Status: providers.ProbeValid},
		},
		{
			Provider: "nw", Env: "NW_API_KEY", Resolved: true,
			MaskedHint: "sk-…02",
			Probe:      providers.KeyProbeResult{Status: providers.ProbeValid},
		},
	}
	out := captureStdout(t, func() { printKeyAudits(audits) })
	for _, leak := range []string{maskSweepKey, sweepAWSKey} {
		if strings.Contains(out, leak) {
			t.Errorf("key audit output leaks the planted key %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{maskSweepMasked, sweepAWSMask, "key nw (NW_API_KEY): valid"} {
		if !strings.Contains(out, want) {
			t.Errorf("key audit output missing %q:\n%s", want, out)
		}
	}
}

func TestD3FixKeysTargetsAndReportMasked(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)

	oldResolve, oldRepair := resolveRepairTargets, repairedWindowsKeyNames
	t.Cleanup(func() { resolveRepairTargets, repairedWindowsKeyNames = oldResolve, oldRepair })

	resolveRepairTargets = func(*providers.Registry, string) ([]string, error) {
		return []string{maskSweepKey, "W6_FIX_KEY"}, nil
	}
	repairedWindowsKeyNames = func(*providers.Registry, providers.RepairOptions) (*providers.RepairReport, error) {
		return &providers.RepairReport{
			Repaired: 1, Skipped: 1,
			Entries: []providers.RepairEntry{
				{
					Provider: sweepAWSKey, Env: maskSweepKey,
					Action:      providers.RepairActionRewrite,
					VariantName: "\ufeff" + maskSweepKey, MaskedValue: "sk-…0001",
				},
				{
					Provider: "acme", Env: "ACME_KEY",
					Action: "", // default branch renders the [SKIP] line
					Note:   "clean name already set",
				},
			},
		}, nil
	}

	out := captureStdout(t, func() { runDoctorFixKeys("all") })
	for _, leak := range []string{maskSweepKey, sweepAWSKey} {
		if strings.Contains(out, leak) {
			t.Errorf("fix-keys output leaks the planted key %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{
		"Targets: " + maskSweepMasked + ", W6_FIX_KEY",
		"[FIXED] " + sweepAWSMask + " (" + maskSweepMasked + ")",
		"[SKIP]  acme (ACME_KEY): clean name already set",
		"sk-…0001",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fix-keys output missing %q:\n%s", want, out)
		}
	}
}

// D3: the unknown doctor option is an operand echo slot; the operand must be
// masked in its original case (sk-, AKIA and the AWS-secret shape).
func TestD3DoctorUnknownOptionMasked(t *testing.T) {
	for _, tc := range []struct {
		plant, masked string
	}{
		{maskSweepKey, maskSweepMasked},
		{sweepAKIAKey, sweepAKIAMask},
		{sweepAWSKey, sweepAWSMask},
		// The classifier's first-5 form for the ASIA twin: the generic
		// redactor pattern (F3) renders the full marker inside sentences, but a
		// bare operand echo still shows first-5 + **** (U8).
		{sweepASIAKey, sweepASIAMask},
	} {
		_, err := parseDoctorArgs([]string{tc.plant})
		if err == nil {
			t.Fatalf("parseDoctorArgs(%q) expected unknown-option error", tc.plant)
		}
		if strings.Contains(err.Error(), tc.plant) {
			t.Errorf("unknown-option error leaks the planted key:\n%v", err)
		}
		if !strings.Contains(err.Error(), tc.masked) {
			t.Errorf("unknown-option error should carry the readable mask %q:\n%v", tc.masked, err)
		}
	}

	// A benign unknown option stays readable so the error stays actionable.
	if _, err := parseDoctorArgs([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("benign unknown option must stay readable: %v", err)
	}
}

// E1: the stdlib prints undefined flag names and bad-syntax arguments
// UNQUOTED, so the quoted-segment redactor cannot see a planted secret there
// (live PoC: `agy pro swarm --wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`
// echoed all 40 runes of the AWS secret). Both flag writers now scan unquoted
// runs: 16+ rune base64-shaped runs (AWS secrets, ASIA key ids) render through
// the canonical classifier and JWT-shaped 3-segment tokens render as
// MaskSecret, while benign echo lines and usage text stay byte-identical.
func TestE1UnquotedFlagEchoMasked(t *testing.T) {
	plants := []struct{ name, key, masked string }{
		{"aws", sweepAWSKey, sweepAWSMask},
		{"jwt", sweepJWTKey, sweepJWTMask},
		// F3: the ASIA twin in the generic redactor pattern now masks the id in
		// the whole line BEFORE the unquoted scan sees it (the AKIA class has
		// always rendered this way), so the flag echo carries the full marker
		// rather than the first-5 mask. The classifier's first-5 form stays
		// pinned by TestD3DoctorUnknownOptionMasked below.
		{"asia", sweepASIAKey, "[REDACTED]"},
	}
	for _, p := range plants {
		for _, kind := range []struct{ name, arg string }{
			{"undefined flag", "--" + p.key},
			{"bad flag syntax", "----" + p.key},
		} {
			t.Run("models "+kind.name+" "+p.name+" plant", func(t *testing.T) {
				var out, errw strings.Builder
				code := runModelsCmdTo([]string{kind.arg}, &out, &errw)
				if code != 2 {
					t.Fatalf("exit = %d; want 2\nstderr:\n%s", code, errw.String())
				}
				if strings.Contains(errw.String(), p.key) {
					t.Errorf("%s echo leaks the planted key:\n%s", kind.name, errw.String())
				}
				if !strings.Contains(errw.String(), p.masked) {
					t.Errorf("%s echo should carry the readable mask %q:\n%s", kind.name, p.masked, errw.String())
				}
				if !strings.Contains(errw.String(), "Usage: agy pro models [--all | --json]") {
					t.Errorf("usage text must stay readable:\n%s", errw.String())
				}
			})
			t.Run("swarm "+kind.name+" "+p.name+" plant", func(t *testing.T) {
				var out, errw strings.Builder
				code := runSwarmCmd([]string{kind.arg}, strings.NewReader(""), &out, &errw)
				if code != 2 {
					t.Fatalf("exit = %d; want 2\nstderr:\n%s", code, errw.String())
				}
				if strings.Contains(errw.String(), p.key) {
					t.Errorf("%s echo leaks the planted key:\n%s", kind.name, errw.String())
				}
				if !strings.Contains(errw.String(), p.masked) {
					t.Errorf("%s echo should carry the readable mask %q:\n%s", kind.name, p.masked, errw.String())
				}
				if !strings.Contains(errw.String(), "Usage: agy pro swarm [options]") {
					t.Errorf("usage text must stay readable:\n%s", errw.String())
				}
			})
		}
	}

	// Benign stdlib echo lines stay byte-identical through the scan.
	t.Run("benign echoes stay readable", func(t *testing.T) {
		var out, errw strings.Builder
		runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--timeout", "abc"},
			strings.NewReader(""), &out, &errw)
		if !strings.Contains(errw.String(), `invalid value "abc" for flag -timeout`) {
			t.Errorf("benign flag value must stay readable:\n%s", errw.String())
		}
		var out2, errw2 strings.Builder
		runModelsCmdTo([]string{"--bogus"}, &out2, &errw2)
		if !strings.Contains(errw2.String(), "flag provided but not defined: -bogus") {
			t.Errorf("benign undefined flag name must stay readable:\n%s", errw2.String())
		}
	})
}

// E2: composite-error seeding follows the ONE shared rule
// (providers.SeedEligible / providers.MaskSentence, F-alpha); this twin
// delegates to it. Only WHOLE operands are ever seeded, never a fragment: the
// old run extractor seeded every separator-free 16+ rune fragment, which
// mangled benign tokens inside composite sentences
// ("native:anthropic/claude-sonnet-4-5/x" printed
// "native:[REDACTED]-sonnet-4-5/x", "prompts/3f2a8b9c1d2e3f4a.md" printed
// "[REDACTED].md"). Seeds now require the WHOLE trimmed operand to be
// secret-shaped; the trimmed form of every seed stays armed for parsers that
// echo the operand without surrounding whitespace.
func TestE2SeedEligibilityWholeOperandOnly(t *testing.T) {
	// AWS whole-operand secrets stay masked (classifier verdict seeds the
	// redactor, which renders the seed as [REDACTED]).
	msg := "open " + sweepAWSKey + ": The system cannot find the file specified."
	if got := maskOperandSentence(msg, sweepAWSKey); strings.Contains(got, sweepAWSKey) || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("AWS whole-operand must stay masked:\n%s", got)
	}

	// Benign operands stay readable, fragment seeds gone.
	for _, op := range []string{
		"native:anthropic/claude-sonnet-4-5/x",
		"prompts/3f2a8b9c1d2e3f4a.md",
		"./prompts/3f2a8b9c1d2e3f4a.md",
		"/var/lib/agy/providers.json",
		"https://acme.example/v1",
		// F-alpha benign pins: dotted names and filenames the JWT/squash shapes
		// must not claim (r5 N1 over-mask class, MyReport filename class).
		"config.dev.override",
		"claude-3.5.sonnet",
		"MyReport2024FINAL.pdf",
	} {
		msg := fmt.Sprintf("seat 1: %q: native model id must not contain %q", op, "/")
		got := maskOperandSentence(msg, op)
		if !strings.Contains(got, op) {
			t.Errorf("benign operand %q must stay readable:\n%s", op, got)
		}
		if strings.Contains(got, "[REDACTED]") {
			t.Errorf("benign operand %q must not trigger any redaction:\n%s", op, got)
		}
	}

	// JWT-shaped operands are masked: the eyJ header classifies, the shape
	// rule covers headers the classifier cannot see.
	for _, jwt := range []string{sweepJWTKey, "abcdefghijklmnop.qrstuvwxyz0123.456789"} {
		msg := fmt.Sprintf("invalid selection token %q", jwt)
		got := maskOperandSentence(msg, jwt)
		if strings.Contains(got, jwt) {
			t.Errorf("JWT operand must be masked:\n%s", got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("JWT operand should carry the redaction marker:\n%s", got)
		}
	}

	// Decorated secrets keep the D1/D3 coverage through the shared squash
	// rule: "1-<aws-secret>" and "native:<aws-secret>/x" stay masked in
	// composite sentences without any fragment seed.
	for _, op := range []string{"1-" + sweepAWSKey, "native:" + sweepAWSKey + "/x"} {
		msg := fmt.Sprintf("invalid selection token %q", op)
		got := maskOperandSentence(msg, op)
		if strings.Contains(got, sweepAWSKey) {
			t.Errorf("decorated AWS operand %q must stay masked:\n%s", op, got)
		}
	}

	// F-alpha regression pins (r5 N2): decorated operands whose inner run is
	// NOT mixed-case-plus-digit used to print raw. The uppercase-only class
	// (ASIA id, bare upper run) and the whitespace-decorated AWS secret seed
	// through the shared rule now.
	for _, op := range []string{
		"1-" + sweepASIAKey,
		"1-ABCDEFGHIJKLMNOPQRST",
		"native: " + sweepAWSKey,
	} {
		msg := fmt.Sprintf("invalid selection token %q", op)
		got := maskOperandSentence(msg, op)
		if strings.Contains(got, sweepASIAKey) || strings.Contains(got, "ABCDEFGHIJKLMNOPQRST") || strings.Contains(got, sweepAWSKey) {
			t.Errorf("decorated operand %q must stay masked:\n%s", op, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("decorated operand %q should carry the redaction marker:\n%s", op, got)
		}
	}
}
