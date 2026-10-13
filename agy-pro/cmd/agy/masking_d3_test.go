package main

import (
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
					Action: providers.RepairActionRewrite,
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
