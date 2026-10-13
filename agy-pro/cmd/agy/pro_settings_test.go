package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/swarm"
)

// settingsTestCore wires runSettingsCmd against an isolated fake Core (no
// live ~/.agy-pro, no network).
type settingsTestCore struct {
	state    *mode.State
	reg      *providers.Registry
	regPath  string
	swarmSt  *testSwarmStore
	audits   []providers.KeyAudit
	fixErr   error
	doctorN  int
	switched []string
}

type testSwarmStore struct{ presets []swarm.SavedPreset }

func (s *testSwarmStore) LoadPresets() ([]swarm.SavedPreset, error) { return s.presets, nil }
func (s *testSwarmStore) SavePresets(p []swarm.SavedPreset) error   { s.presets = p; return nil }

func installSettingsCore(t *testing.T) *settingsTestCore {
	t.Helper()
	tmp := t.TempDir()
	tc := &settingsTestCore{
		state:   &mode.State{CurrentMode: mode.ModeClean, ManagedItems: mode.DefaultManagedItems},
		reg:     &providers.Registry{},
		regPath: filepath.Join(tmp, "providers.json"),
		swarmSt: &testSwarmStore{},
	}
	prev := newSettingsCore
	newSettingsCore = func() *settings.Core {
		return settings.NewCore(settings.Deps{
			LoadState: func() (*mode.State, error) { return tc.state, nil },
			SaveState: func(st *mode.State) error { tc.state = st; return nil },
			LoadReg:   func() (*providers.Registry, error) { return tc.reg, nil },
			SaveReg: func(reg *providers.Registry) error {
				tc.reg = reg
				return nil
			},
			AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
				return tc.audits
			},
			FixKeys:      func(target string) error { return tc.fixErr },
			RollbackKeys: func(dir string) error { return nil },
			RunWizard:    func(in io.Reader, out io.Writer) error { return nil },
			Doctor: func(out io.Writer, strict bool) error {
				tc.doctorN++
				return nil
			},
			Inventory: func(ctx context.Context) (*inventory.Inventory, error) {
				return &inventory.Inventory{}, nil
			},
			SwarmStore:  tc.swarmSt,
			VersionFunc: func() string { return "0.0-test" },
			SwitchMode: func(target string, in io.Reader, out io.Writer) error {
				tc.switched = append(tc.switched, target)
				return nil
			},
			RestoreMode: func(out io.Writer) error { return nil },
		})
	}
	t.Cleanup(func() { newSettingsCore = prev })
	return tc
}

func runSettings(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	out, errw := &bytes.Buffer{}, &bytes.Buffer{}
	code := runSettingsCmd(args, strings.NewReader(stdin), out, errw)
	return code, out.String(), errw.String()
}

func TestRunSettingsCmd_Show(t *testing.T) {
	installSettingsCore(t)
	code, out, errw := runSettings(t, []string{"show"}, "")
	if code != 0 {
		t.Fatalf("show exit = %d, stderr=%s", code, errw)
	}
	if !strings.Contains(out, "Default Launch Profile") {
		t.Errorf("show output missing profile row:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("show must be ANSI-free (C3)")
	}

	code, out, _ = runSettings(t, []string{"show", "--json"}, "")
	if code != 0 {
		t.Fatalf("show --json exit = %d", code)
	}
	if !strings.Contains(out, `"sections"`) {
		t.Errorf("json output missing sections:\n%s", out)
	}

	code, _, _ = runSettings(t, []string{"show", "--bogus"}, "")
	if code != 2 {
		t.Errorf("bad show flag exit = %d; want 2", code)
	}
}

func TestRunSettingsCmd_Set(t *testing.T) {
	tc := installSettingsCore(t)
	code, out, errw := runSettings(t, []string{"set", "default-profile", "native"}, "")
	if code != 0 {
		t.Fatalf("set exit = %d stderr=%s", code, errw)
	}
	if tc.state.DefaultProfile != mode.ProfileNative {
		t.Errorf("state = %+v", tc.state)
	}
	if !strings.Contains(out, "[SUCCESS]") {
		t.Errorf("set output = %q", out)
	}

	code, _, _ = runSettings(t, []string{"set", "default-profile", "bogus"}, "")
	if code != 1 {
		t.Errorf("invalid value exit = %d; want 1", code)
	}
	code, _, _ = runSettings(t, []string{"set", "default-profile"}, "")
	if code != 2 {
		t.Errorf("missing value exit = %d; want 2", code)
	}
	code, _, _ = runSettings(t, []string{"set", "native_seat_concurrency", "4"}, "")
	if code != 0 || tc.state.NativeSeatConcurrency != 4 {
		t.Errorf("concurrency set failed: code=%d state=%+v", code, tc.state)
	}
}

func TestRunSettingsCmd_Providers(t *testing.T) {
	tc := installSettingsCore(t)

	// add-json from a temp file (env-var NAME accepted).
	specPath := filepath.Join(t.TempDir(), "spec.json")
	spec := `{"name":"acme","baseURL":"https://acme.example/v1","apiKeyEnv":"ACME_KEY","models":[{"id":"m1"}]}`
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errw := runSettings(t, []string{"providers", "add-json", specPath}, "")
	if code != 0 {
		t.Fatalf("add-json exit = %d stderr=%s", code, errw)
	}
	if len(tc.reg.Providers) != 1 || tc.reg.Providers[0].Name != "acme" {
		t.Fatalf("registry = %+v", tc.reg.Providers)
	}

	// add-json from stdin ("-").
	spec2 := `{"name":"beta","baseURL":"https://beta.example/v1","apiKeyEnv":"BETA_KEY","models":[]}`
	code, _, errw = runSettings(t, []string{"providers", "add-json", "-"}, spec2)
	if code != 0 {
		t.Fatalf("add-json - exit = %d stderr=%s", code, errw)
	}

	// A key VALUE in apiKeyEnv is rejected and never echoed (U8/F-PROV).
	secret := "sk-cmd-plaintext-key-abcdef"
	code, _, errw = runSettings(t, []string{"providers", "add-json", "-"},
		`{"name":"leak","baseURL":"https://leak.example/v1","apiKeyEnv":"`+secret+`","models":[]}`)
	if code == 0 {
		t.Fatal("key VALUE accepted by add-json")
	}
	if strings.Contains(errw, secret) {
		t.Error("stderr echoes the key value")
	}

	// rename + list + delete.
	code, _, errw = runSettings(t, []string{"providers", "rename", "acme", "acme2"}, "")
	if code != 0 {
		t.Fatalf("rename exit = %d stderr=%s", code, errw)
	}
	code, out, _ := runSettings(t, []string{"providers", "list"}, "")
	if code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	if !strings.Contains(out, "acme2") {
		t.Errorf("list missing provider:\n%s", out)
	}
	code, _, errw = runSettings(t, []string{"providers", "delete", "acme2"}, "")
	if code != 0 {
		t.Fatalf("delete exit = %d stderr=%s", code, errw)
	}

	// test + usage errors.
	tc.audits = []providers.KeyAudit{{Provider: "beta", Env: "BETA_KEY", Resolved: true, Probe: providers.KeyProbeResult{Status: providers.ProbeValid}}}
	code, _, errw = runSettings(t, []string{"providers", "test", "beta"}, "")
	if code != 0 {
		t.Fatalf("test exit = %d stderr=%s", code, errw)
	}
	code, _, _ = runSettings(t, []string{"providers", "delete"}, "")
	if code != 2 {
		t.Errorf("delete without name exit = %d; want 2", code)
	}
	code, _, _ = runSettings(t, []string{"providers", "nope"}, "")
	if code != 2 {
		t.Errorf("unknown providers sub exit = %d; want 2", code)
	}
}

func TestRunSettingsCmd_ModeAndInfo(t *testing.T) {
	tc := installSettingsCore(t)
	code, _, errw := runSettings(t, []string{"mode", "switch", "full"}, "")
	if code != 0 {
		t.Fatalf("mode switch exit = %d stderr=%s", code, errw)
	}
	if len(tc.switched) != 1 || tc.switched[0] != "full" {
		t.Errorf("switch calls = %v", tc.switched)
	}
	code, _, _ = runSettings(t, []string{"mode", "status"}, "")
	if code != 0 {
		t.Errorf("mode status exit = %d", code)
	}
	code, _, _ = runSettings(t, []string{"mode", "list"}, "")
	if code != 0 {
		t.Errorf("mode list exit = %d", code)
	}
	code, _, _ = runSettings(t, []string{"mode", "switch"}, "")
	if code != 2 {
		t.Errorf("mode switch w/o target exit = %d; want 2", code)
	}

	for _, sub := range []string{"update-info", "uninstall-info", "version-info"} {
		code, out, _ := runSettings(t, []string{sub}, "")
		if code != 0 || strings.TrimSpace(out) == "" {
			t.Errorf("%s exit = %d out = %q", sub, code, out)
		}
	}
}

func TestRunSettingsCmd_Doctor(t *testing.T) {
	tc := installSettingsCore(t)
	code, _, _ := runSettings(t, []string{"doctor", "diagnose"}, "")
	if code != 0 || tc.doctorN != 1 {
		t.Errorf("doctor diagnose exit = %d calls = %d", code, tc.doctorN)
	}
	code, _, _ = runSettings(t, []string{"doctor", "rollback-keys"}, "")
	if code != 2 {
		t.Errorf("rollback without dir exit = %d; want 2", code)
	}
}

// TestRunSettingsCmd_NonTTYDegrade (D3 honest degrade): without a console the
// TUI entry prints the snapshot and falls back to line-mode keys over the same
// Core verbs; ANSI stays stripped (C3).
func TestRunSettingsCmd_NonTTYDegrade(t *testing.T) {
	installSettingsCore(t)
	code, out, errw := runSettings(t, nil, "")
	if code != 0 {
		t.Fatalf("no-args non-TTY exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{"Line-mode fallback", "agy pro settings show", "Default Launch Profile"} {
		if !strings.Contains(out, want) {
			t.Errorf("degrade output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("non-TTY path must not emit ANSI (C3)")
	}
}

func TestRunSettingsCmd_UnknownSubcommand(t *testing.T) {
	installSettingsCore(t)
	code, _, errw := runSettings(t, []string{"bogus"}, "")
	if code != 2 {
		t.Errorf("unknown subcommand exit = %d; want 2", code)
	}
	if !strings.Contains(errw, "Usage:") {
		t.Errorf("usage missing from stderr:\n%s", errw)
	}
}

// TestRunSettingsCmd_KeyEchoMasked (C2): a key paste in an operand slot the
// settings command ACCEPTS (rename target, provider name, add-json name) must
// come back masked on the success line, never raw.
func TestRunSettingsCmd_KeyEchoMasked(t *testing.T) {
	const planted = "sk-planted-settings-echo-key-0123456789abcdef"
	const masked = "sk-pl****"

	t.Run("rename success", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: "acme", BaseURL: "https://acme.example/v1"}}}
		code, out, errw := runSettings(t, []string{"providers", "rename", "acme", planted}, "")
		if code != 0 {
			t.Fatalf("rename exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, planted) || strings.Contains(errw, planted) {
			t.Errorf("rename echoes the planted key raw:\nstdout=%s stderr=%s", out, errw)
		}
		if !strings.Contains(out, masked) {
			t.Errorf("rename success line missing the readable mask:\n%s", out)
		}
	})

	t.Run("delete success", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: planted}}}
		code, out, errw := runSettings(t, []string{"providers", "delete", planted}, "")
		if code != 0 {
			t.Fatalf("delete exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, planted) {
			t.Errorf("delete echoes the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, masked) {
			t.Errorf("delete success line missing the readable mask:\n%s", out)
		}
	})

	t.Run("test success", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: planted, APIKeyEnv: "PLANT_KEY"}}}
		tc.audits = []providers.KeyAudit{{
			Provider: planted, Env: "PLANT_KEY", Resolved: true,
			Probe: providers.KeyProbeResult{Status: providers.ProbeValid},
		}}
		code, out, errw := runSettings(t, []string{"providers", "test", planted}, "")
		if code != 0 {
			t.Fatalf("test exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, planted) {
			t.Errorf("test echoes the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, masked) {
			t.Errorf("test success line missing the readable mask:\n%s", out)
		}
	})

	t.Run("add-json success", func(t *testing.T) {
		installSettingsCore(t)
		specPath := filepath.Join(t.TempDir(), "spec.json")
		spec := `{"name":"` + planted + `","baseURL":"https://acme.example/v1","apiKeyEnv":"ACME_KEY","models":[{"id":"m1"}]}`
		if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errw := runSettings(t, []string{"providers", "add-json", specPath}, "")
		if code != 0 {
			t.Fatalf("add-json exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, planted) {
			t.Errorf("add-json echoes the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, masked) || !strings.Contains(out, "ACME_KEY") {
			t.Errorf("add-json success line must mask the name and keep the env NAME readable:\n%s", out)
		}
	})

	t.Run("unreadable add-json path", func(t *testing.T) {
		installSettingsCore(t)
		code, _, errw := runSettings(t, []string{"providers", "add-json", planted}, "")
		if code != 1 {
			t.Fatalf("add-json exit = %d; want 1", code)
		}
		if strings.Contains(errw, planted) {
			t.Errorf("read failure echoes the planted key raw (os.ReadFile embeds the path):\n%s", errw)
		}
	})

	t.Run("unknown subcommands", func(t *testing.T) {
		installSettingsCore(t)
		for _, args := range [][]string{
			{planted},
			{"show", planted},
			{"providers", planted},
			{"mode", planted},
			{"doctor", planted},
		} {
			code, _, errw := runSettings(t, args, "")
			if code != 2 {
				t.Errorf("args %v exit = %d; want 2", args, code)
			}
			if strings.Contains(errw, planted) {
				t.Errorf("args %v echoes the planted key raw:\n%s", args, errw)
			}
		}
	})
}

// TestRunSettingsCmd_RollbackKeysErrorMasked (E5): the settings doctor
// rollback path forwards the raw ReadFile error, which re-embeds the backup
// dir / resolved file; a key-shaped --rollback-keys operand must be redacted
// from the printed error, benign paths stay readable.
func TestRunSettingsCmd_RollbackKeysErrorMasked(t *testing.T) {
	failRollback := func() {
		t.Helper()
		prev := newSettingsCore
		newSettingsCore = func() *settings.Core {
			return settings.NewCore(settings.Deps{
				RollbackKeys: func(dir string) error {
					return fmt.Errorf("open %s: The system cannot find the file specified.", providers.ResolveBackupFile(dir))
				},
			})
		}
		t.Cleanup(func() { newSettingsCore = prev })
	}

	t.Run("bare base64 plant (live repro shape)", func(t *testing.T) {
		failRollback()
		const plant = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
		code, _, errw := runSettings(t, []string{"doctor", "rollback-keys", plant}, "")
		if code != 1 {
			t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if strings.Contains(errw, plant) {
			t.Errorf("settings rollback error leaks the planted operand raw:\n%s", errw)
		}
		if !strings.Contains(errw, "[REDACTED]") {
			t.Errorf("settings rollback error missing the redaction:\n%s", errw)
		}
	})

	t.Run("sk- plant directory", func(t *testing.T) {
		failRollback()
		dir := filepath.Join(t.TempDir(), "sk-ant-api03-PLANT0123456789abcdef")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, errw := runSettings(t, []string{"doctor", "rollback-keys", dir}, "")
		if code != 1 {
			t.Fatalf("exit = %d; want 1", code)
		}
		if strings.Contains(errw, "sk-ant-api03-PLANT0123456789abcdef") {
			t.Errorf("settings rollback error leaks the planted dir raw:\n%s", errw)
		}
		if !strings.Contains(errw, "[REDACTED]") {
			t.Errorf("settings rollback error missing the redaction:\n%s", errw)
		}
	})

	t.Run("AKIA plant directory", func(t *testing.T) {
		failRollback()
		dir := filepath.Join(t.TempDir(), "AKIAIOSFODNN7EXAMPLE")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, errw := runSettings(t, []string{"doctor", "rollback-keys", dir}, "")
		if code != 1 {
			t.Fatalf("exit = %d; want 1", code)
		}
		if strings.Contains(errw, "AKIAIOSFODNN7EXAMPLE") {
			t.Errorf("settings rollback error leaks the planted dir raw:\n%s", errw)
		}
		if !strings.Contains(errw, "[REDACTED]") {
			t.Errorf("settings rollback error missing the redaction:\n%s", errw)
		}
	})

	t.Run("benign path stays readable", func(t *testing.T) {
		failRollback()
		dir := t.TempDir()
		code, _, errw := runSettings(t, []string{"doctor", "rollback-keys", dir}, "")
		if code != 1 {
			t.Fatalf("exit = %d; want 1", code)
		}
		if !strings.Contains(errw, filepath.Join(dir, "registry-keys.v2.json")) {
			t.Errorf("benign rollback path must stay readable:\n%s", errw)
		}
		if strings.Contains(errw, "[REDACTED]") {
			t.Errorf("benign rollback error must not be redacted:\n%s", errw)
		}
	})
}

// F-beta F7: the `settings doctor fix-keys` tail echoes the aggregate error
// unmasked before this batch; a decorated key-shaped target (whole-string
// classification cannot see it) must render masked in the tail, benign
// targets readable.
func TestRunSettingsCmd_FixKeysErrorMasked(t *testing.T) {
	const (
		awsPlant = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
		// I3: the decorated class now renders through the canonical first-5
		// mask (the U8 rule) instead of the seeded whole-string [REDACTED].
		redact   = "nativ****"
	)

	t.Run("decorated target masked in the tail", func(t *testing.T) {
		installSettingsCore(t) // empty registry: the target resolves to an error echoing the raw token
		code, _, errw := runSettings(t, []string{"doctor", "fix-keys", "native:" + awsPlant}, "")
		if code != 1 {
			t.Fatalf("fix-keys exit = %d; want 1\nstderr=%s", code, errw)
		}
		if strings.Contains(errw, awsPlant) {
			t.Errorf("fix-keys tail echoes the planted key raw:\n%s", errw)
		}
		if !strings.Contains(errw, redact) {
			t.Errorf("fix-keys tail missing the mask:\n%s", errw)
		}
	})

	t.Run("benign target stays readable", func(t *testing.T) {
		installSettingsCore(t)
		code, _, errw := runSettings(t, []string{"doctor", "fix-keys", "nope"}, "")
		if code != 1 {
			t.Fatalf("fix-keys exit = %d; want 1\nstderr=%s", code, errw)
		}
		if !strings.Contains(errw, `"nope" matches no provider name or known API key variable`) {
			t.Errorf("benign fix-keys tail must stay readable:\n%s", errw)
		}
	})
}
