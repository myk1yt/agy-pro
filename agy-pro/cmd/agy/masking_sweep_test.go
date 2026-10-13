package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// C2 planted-key sweep (B2/U8 class): every operand echo on the cmd/agy
// wrapper's error and success paths must route through providers.MaskOperand,
// so a misdirected key paste (`agy pro settings providers sk-...`,
// `rename acme sk-...`) never reaches stderr/stdout raw. The masked form of
// the planted key is its first five runes + "****"; the sweep asserts the RAW
// key never appears, and the dedicated tests assert the readable mask.
const (
	maskSweepKey    = "sk-planted-c2-sweep-key-0123456789abcdef"
	maskSweepMasked = "sk-pl****"

	// D1/D2/D3 delta fixtures: an AWS-secret-shaped key (the live PoC shape,
	// contiguous base64 with "/") and an AWS access key id (AKIA, the
	// uppercase class that original-case masking restores).
	sweepAWSKey   = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	sweepAWSMask  = "wJalr****"
	sweepAKIAKey  = "AKIAIOSFODNN7EXAMPLE"
	sweepAKIAMask = "AKIAI****"

	// E1 delta fixtures: a JWT (3-segment dotted token; the "eyJ" header
	// classifies as a credential, the shape rule covers other headers) and an
	// ASIA-prefixed AWS access key id (the credentialShapeRe sibling of AKIA
	// that the generic AKIA-only redactor pattern misses, so only the
	// unquoted-run scan catches it).
	sweepJWTKey   = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	sweepJWTMask  = "eyJhb****"
	sweepASIAKey  = "ASIAIOSFODNN7EXAMPLE"
	sweepASIAMask = "ASIAI****"
)

// TestWrapperPlantedKeySweep drives every in-process wrapper echo site with a
// key-shaped operand and asserts neither stdout nor stderr carries it raw.
func TestWrapperPlantedKeySweep(t *testing.T) {
	type site struct {
		name string
		run  func() (int, string, string)
	}
	sites := []site{
		// --- cmd/agy/pro_settings.go error paths --------------------------------
		{"settings unknown show option", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"show", maskSweepKey}, "")
		}},
		{"settings unknown subcommand", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{maskSweepKey}, "")
		}},
		{"settings unknown providers subcommand", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"providers", maskSweepKey}, "")
		}},
		{"settings unknown mode subcommand", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"mode", maskSweepKey}, "")
		}},
		{"settings unknown doctor subcommand", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"doctor", maskSweepKey}, "")
		}},
		{"settings add-json unreadable operand path", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"providers", "add-json", maskSweepKey}, "")
		}},
		{"settings add-json unreadable AWS-shaped operand path", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"providers", "add-json", sweepAWSKey}, "")
		}},
		{"settings set rejects key-shaped value", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"set", "default-profile", maskSweepKey}, "")
		}},
		{"settings providers test unknown provider", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"providers", "test", maskSweepKey}, "")
		}},
		{"settings providers rename unknown provider", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"providers", "rename", maskSweepKey, "newname"}, "")
		}},
		{"settings providers delete unknown provider", func() (int, string, string) {
			installSettingsCore(t)
			return runSettings(t, []string{"providers", "delete", maskSweepKey}, "")
		}},
		// --- cmd/agy/main.go error paths ----------------------------------------
		{"mode invalid argument message", func() (int, string, string) {
			return 0, invalidModeArgMessage(maskSweepKey), ""
		}},
		{"mode invalid argument AKIA plant (D2 original case)", func() (int, string, string) {
			return 0, "", invalidModeArgMessage(sweepAKIAKey)
		}},
		{"doctor unknown option sk- plant", func() (int, string, string) {
			_, err := parseDoctorArgs([]string{"--" + maskSweepKey})
			if err == nil {
				t.Fatal("expected unknown-option error")
			}
			return 2, "", err.Error()
		}},
		{"doctor unknown option AKIA plant", func() (int, string, string) {
			_, err := parseDoctorArgs([]string{sweepAKIAKey})
			if err == nil {
				t.Fatal("expected unknown-option error")
			}
			return 2, "", err.Error()
		}},
		{"doctor unknown option AWS-secret plant", func() (int, string, string) {
			_, err := parseDoctorArgs([]string{sweepAWSKey})
			if err == nil {
				t.Fatal("expected unknown-option error")
			}
			return 2, "", err.Error()
		}},
		// --- cmd/agy/pro_swarm.go error paths -----------------------------------
		{"swarm unknown template", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", maskSweepKey, "--seats", "p1"}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm prompt-file unreadable operand path", func() (int, string, string) {
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", maskSweepKey},
				strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm prompt-file unreadable AWS-shaped operand path", func() (int, string, string) {
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "p1", "--prompt-file", sweepAWSKey},
				strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm seats invalid selection token", func() (int, string, string) {
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "1-" + maskSweepKey},
				strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm seats selection token AWS plant", func() (int, string, string) {
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "1-" + sweepAWSKey},
				strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm seats native id with slash", func() (int, string, string) {
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "native:" + maskSweepKey + "/x"},
				strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm seats native id AWS plant with slash", func() (int, string, string) {
			defer withStubSwarmInventory(t, swarmFixtureInventory(t), nil)()
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--template", "code-review", "--seats", "native:" + sweepAWSKey + "/x"},
				strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		// --- E1: stdlib unquoted flag echoes (undefined flag / bad syntax) -----
		{"models undefined flag AWS plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runModelsCmdTo([]string{"--" + sweepAWSKey}, &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"models bad flag syntax AWS plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runModelsCmdTo([]string{"----" + sweepAWSKey}, &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"models undefined flag JWT plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runModelsCmdTo([]string{"--" + sweepJWTKey}, &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"models bad flag syntax JWT plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runModelsCmdTo([]string{"----" + sweepJWTKey}, &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"models undefined flag ASIA plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runModelsCmdTo([]string{"--" + sweepASIAKey}, &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"models bad flag syntax ASIA plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runModelsCmdTo([]string{"----" + sweepASIAKey}, &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm undefined flag AWS plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--" + sweepAWSKey}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm bad flag syntax AWS plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"----" + sweepAWSKey}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm undefined flag JWT plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--" + sweepJWTKey}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm bad flag syntax JWT plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"----" + sweepJWTKey}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm undefined flag ASIA plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"--" + sweepASIAKey}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
		{"swarm bad flag syntax ASIA plant", func() (int, string, string) {
			var out, errw strings.Builder
			code := runSwarmCmd([]string{"----" + sweepASIAKey}, strings.NewReader(""), &out, &errw)
			return code, out.String(), errw.String()
		}},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			code, out, errw := s.run()
			for _, src := range []struct{ name, body string }{{"stdout", out}, {"stderr", errw}} {
				if strings.Contains(src.body, maskSweepKey) {
					t.Errorf("exit=%d %s prints the planted key raw:\n%s", code, src.name, src.body)
				}
				if strings.Contains(src.body, sweepAWSKey) {
					t.Errorf("exit=%d %s prints the planted AWS-shaped key raw:\n%s", code, src.name, src.body)
				}
				if strings.Contains(src.body, sweepAKIAKey) {
					t.Errorf("exit=%d %s prints the planted AKIA key raw:\n%s", code, src.name, src.body)
				}
				if strings.Contains(src.body, sweepJWTKey) {
					t.Errorf("exit=%d %s prints the planted JWT raw:\n%s", code, src.name, src.body)
				}
				if strings.Contains(src.body, sweepASIAKey) {
					t.Errorf("exit=%d %s prints the planted ASIA key raw:\n%s", code, src.name, src.body)
				}
			}
		})
	}
}

// TestWrapperPlantedKeySweepSuccessPaths covers the SUCCESS confirmations:
// a key-shaped operand in a slot the operation accepts must still be masked
// on the way out (the `rename acme sk-...` class from C2).
func TestWrapperPlantedKeySweepSuccessPaths(t *testing.T) {
	t.Run("rename success echoes masked new name", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: "acme", BaseURL: "https://acme.example/v1"}}}
		code, out, errw := runSettings(t, []string{"providers", "rename", "acme", maskSweepKey}, "")
		if code != 0 {
			t.Fatalf("rename exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, maskSweepKey) {
			t.Errorf("rename success line prints the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, maskSweepMasked) {
			t.Errorf("rename success line should carry the readable mask %q:\n%s", maskSweepMasked, out)
		}
	})

	t.Run("delete success echoes masked provider name", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: maskSweepKey}}}
		code, out, errw := runSettings(t, []string{"providers", "delete", maskSweepKey}, "")
		if code != 0 {
			t.Fatalf("delete exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, maskSweepKey) {
			t.Errorf("delete success line prints the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, maskSweepMasked) {
			t.Errorf("delete success line should carry the readable mask:\n%s", out)
		}
	})

	t.Run("test success echoes masked provider name", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: maskSweepKey, APIKeyEnv: "SWEEP_KEY"}}}
		tc.audits = []providers.KeyAudit{{
			Provider: maskSweepKey, Env: "SWEEP_KEY", Resolved: true,
			Probe: providers.KeyProbeResult{Status: providers.ProbeValid},
		}}
		code, out, errw := runSettings(t, []string{"providers", "test", maskSweepKey}, "")
		if code != 0 {
			t.Fatalf("test exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, maskSweepKey) {
			t.Errorf("test success line prints the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, maskSweepMasked) {
			t.Errorf("test success line should carry the readable mask:\n%s", out)
		}
	})

	t.Run("add-json success echoes masked provider name", func(t *testing.T) {
		installSettingsCore(t)
		specPath := filepath.Join(t.TempDir(), "spec.json")
		spec := `{"name":"` + maskSweepKey + `","baseURL":"https://acme.example/v1","apiKeyEnv":"ACME_KEY","models":[{"id":"m1"}]}`
		if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errw := runSettings(t, []string{"providers", "add-json", specPath}, "")
		if code != 0 {
			t.Fatalf("add-json exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, maskSweepKey) {
			t.Errorf("add-json success line prints the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, maskSweepMasked) {
			t.Errorf("add-json success line should carry the readable mask:\n%s", out)
		}
		if !strings.Contains(out, "ACME_KEY") {
			t.Errorf("benign env-var NAME must stay readable:\n%s", out)
		}
	})
}

// TestWrapperPlantedKeySweepSubprocess covers the echo sites that terminate
// through os.Exit (runConfig's invalid default profile, the `pro` dispatch
// default case): they run in a helper subprocess and the parent asserts the
// captured stderr is masked.
func TestWrapperPlantedKeySweepSubprocess(t *testing.T) {
	cases := []struct {
		name       string
		dispatch   string
		wantCode   int
		maskedWant string
	}{
		{"config invalid default profile", "config-bad-profile", 1, maskSweepMasked},
		{"pro unknown subcommand", "pro-unknown-subcommand", 1, maskSweepMasked},
		// D2: the mask must fire on the ORIGINAL-CASE operand; the
		// case-folded copy never matches the uppercase-anchored AKIA class.
		{"pro unknown subcommand AKIA plant", "pro-unknown-subcommand-akia", 1, sweepAKIAMask},
		{"config unknown subcommand AKIA plant", "config-unknown-subcommand-akia", 1, sweepAKIAMask},
		{"mode invalid argument AKIA plant", "mode-bad-arg-akia", 1, sweepAKIAMask},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runDispatchHelperCaptureStderr(t, tc.dispatch)
			if code != tc.wantCode {
				t.Fatalf("exit = %d; want %d\nstderr:\n%s", code, tc.wantCode, stderr)
			}
			for _, plant := range []string{maskSweepKey, sweepAKIAKey} {
				if strings.Contains(stderr, plant) {
					t.Errorf("stderr prints the planted key %q raw:\n%s", plant, stderr)
				}
			}
			if !strings.Contains(stderr, tc.maskedWant) {
				t.Errorf("stderr should carry the readable mask %q:\n%s", tc.maskedWant, stderr)
			}
		})
	}
}

// runDispatchHelperCaptureStderr is runDispatchHelper with stderr captured
// (the C2 echo sites write to stderr before os.Exit).
func runDispatchHelperCaptureStderr(t *testing.T, dispatchCase string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProDispatchHelperProcess")
	cmd.Env = append(os.Environ(),
		dispatchHelperEnv+"="+dispatchCase,
		paths.EnvAgyProHome+"="+filepath.Join(t.TempDir(), "home"),
		providers.EnvProviders+"="+filepath.Join(t.TempDir(), "missing-providers.json"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("dispatch helper %q failed to run: %v\nstderr:\n%s", dispatchCase, err, stderr.String())
		}
		code = exitErr.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}
