package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
	"agypro/internal/tui"
)

func TestFormatTokenCount(t *testing.T) {
	cases := []struct {
		input    int
		expected string
	}{
		{-1, "max"},
		{0, "max"},
		{1048576, "1M"},
		{2097152, "2M"},
		{131072, "128k"},
		{128000, "128k"},
		{200000, "200k"},
		{4096, "4k"},
		{8192, "8k"},
		{500, "500"},
	}

	for _, c := range cases {
		got := formatTokenCount(c.input)
		if got != c.expected {
			t.Errorf("formatTokenCount(%d) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestPrintConfiguredProvidersAndModels(t *testing.T) {
	t.Setenv("MOCK_NW_KEY", "dummy-nw-token")

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   "https://api.neuralwatt.com/v1",
				APIKeyEnv: "MOCK_NW_KEY",
				Models: []providers.Model{
					{
						ID:              "deepseek-v4.1-flash-flex",
						Family:          "deepseek",
						ContextLength:   1048576,
						MaxOutputTokens: -1,
						Capabilities: providers.Capabilities{
							SupportsTools:    true,
							SupportsVision:   true,
							SupportsThinking: true,
							ReasoningEffort:  "high",
						},
					},
					{
						ID:              "glm-5.3-flash-flex",
						Family:          "glm",
						ContextLength:   1048576,
						MaxOutputTokens: -1,
						Capabilities: providers.Capabilities{
							SupportsTools:    true,
							SupportsThinking: true,
							ReasoningEffort:  "high",
						},
					},
				},
			},
		},
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printConfiguredProvidersAndModels(reg)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	expectedProviderLine := "[1] NW (https://api.neuralwatt.com/v1) [API Key: Configured]"
	expectedModel1 := "- deepseek-v4.1-flash-flex (NW)  [Family: deepseek | Context: 1M | MaxOutput: max | Capabilities: tools, vision, thinking(high)]"
	expectedModel2 := "- glm-5.3-flash-flex (NW)        [Family: glm      | Context: 1M | MaxOutput: max | Capabilities: tools, thinking(high)]"

	if !strings.Contains(output, expectedProviderLine) {
		t.Errorf("missing expected provider line in output:\nGot:\n%s\nWant line:\n%s", output, expectedProviderLine)
	}
	if !strings.Contains(output, expectedModel1) {
		t.Errorf("missing expected model 1 line in output:\nGot:\n%s\nWant line:\n%s", output, expectedModel1)
	}
	if !strings.Contains(output, expectedModel2) {
		t.Errorf("missing expected model 2 line in output:\nGot:\n%s\nWant line:\n%s", output, expectedModel2)
	}
}

// captureStdout runs fn and returns everything it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

func TestRunConfigDefaultProfileRoundTrip(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv(paths.EnvAgyProHome, home)

	out := captureStdout(t, func() {
		runConfig([]string{"default-profile", "gateway"})
	})
	if !strings.Contains(out, "[SUCCESS] Default launch profile set to: gateway") {
		t.Errorf("set output missing confirmation:\n%s", out)
	}
	if !strings.Contains(out, "local bridge") {
		t.Errorf("set output should explain the gateway profile:\n%s", out)
	}

	// Round trip through state.json: the key persists and `show` reads it back.
	out = captureStdout(t, func() {
		runConfig([]string{"show"})
	})
	if !strings.Contains(out, "Default Launch Profile : gateway") {
		t.Errorf("show output missing persisted profile:\n%s", out)
	}

	// Native and prompt values round trip too.
	for _, profile := range []string{"native", "prompt"} {
		captureStdout(t, func() {
			runConfig([]string{"default-profile", profile})
		})
		got := captureStdout(t, func() {
			runConfig([]string{"show"})
		})
		want := "Default Launch Profile : " + profile
		if !strings.Contains(got, want) {
			t.Errorf("after setting %q, show output missing %q:\n%s", profile, want, got)
		}
	}
}

func TestRunConfigShowFreshHome(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "fresh"))
	out := captureStdout(t, func() {
		runConfig([]string{"show"})
	})
	if !strings.Contains(out, "Default Launch Profile : prompt") {
		t.Errorf("fresh home should default to prompt:\n%s", out)
	}
	if !strings.Contains(out, "Bridge Tools           : read,search") {
		t.Errorf("fresh home should default bridge tools to read,search:\n%s", out)
	}
}

// R1-6: `agy pro config bridge-tools` persists the allow-list in state.json and
// `config show` reads it back; "none" disables the loop.
func TestRunConfigBridgeToolsRoundTrip(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv(paths.EnvAgyProHome, home)

	out := captureStdout(t, func() {
		runConfig([]string{"bridge-tools", "read,search,write"})
	})
	if !strings.Contains(out, "[SUCCESS] Bridge tools set to: read,search,write") {
		t.Errorf("set output missing confirmation:\n%s", out)
	}
	if !strings.Contains(out, "write_file, replace_in_file") {
		t.Errorf("set output should name the enabled tools:\n%s", out)
	}

	out = captureStdout(t, func() {
		runConfig([]string{"show"})
	})
	if !strings.Contains(out, "Bridge Tools           : read,search,write") {
		t.Errorf("show output missing persisted bridge tools:\n%s", out)
	}

	// Canonical ordering: the stored value is normalized, not echoed raw.
	captureStdout(t, func() {
		runConfig([]string{"bridge-tools", "exec,read"})
	})
	out = captureStdout(t, func() {
		runConfig([]string{"show"})
	})
	if !strings.Contains(out, "Bridge Tools           : read,exec") {
		t.Errorf("show output should hold the canonical token order:\n%s", out)
	}

	// "none" disables the loop and survives a reload.
	captureStdout(t, func() {
		runConfig([]string{"bridge-tools", "none"})
	})
	out = captureStdout(t, func() {
		runConfig([]string{"show"})
	})
	if !strings.Contains(out, "Bridge Tools           : none") {
		t.Errorf("show output missing disabled loop:\n%s", out)
	}
}

func TestParseBridgeToolsSpec(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want string
	}{
		{"read,search", "read,search"},
		{"search,read", "read,search"},
		{"read,search,write,exec", "read,search,write,exec"},
		{"READ, Search", "read,search"},
		{"none", "none"},
		{"NONE", "none"},
		{" none ", "none"},
	} {
		got, err := parseBridgeToolsSpec(tc.spec)
		if err != nil {
			t.Errorf("parseBridgeToolsSpec(%q) unexpected error: %v", tc.spec, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseBridgeToolsSpec(%q) = %q; want %q", tc.spec, got, tc.want)
		}
	}

	// Fail closed: unknown tokens and empty values must never be accepted.
	for _, spec := range []string{"", "  ", "bogus", "read,bogus", "nowrite", "exec-disabled", "all"} {
		if got, err := parseBridgeToolsSpec(spec); err == nil {
			t.Errorf("parseBridgeToolsSpec(%q) = %q; want error", spec, got)
		}
	}
}

func TestRunModeStatusWired(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	out := captureStdout(t, func() {
		runMode([]string{"status"})
	})
	if !strings.Contains(out, "agy-pro Profile Mode Status") {
		t.Errorf("agy pro mode status output missing:\n%s", out)
	}
}

// F-19: the usage and error strings must reflect the status/config
// subcommands (and every accepted `mode` argument).
func TestPrintProHelpReflectsStatusAndConfig(t *testing.T) {
	out := captureStdout(t, printProHelp)
	for _, want := range []string{
		"status          Display current mode, providers, and real agy status",
		"config          Show or set persistent defaults (default-profile, bridge-tools)",
		"doctor          Diagnose toolchain, paths, keys (--strict, --fix-keys, --rollback-keys)",
		"models          Configured providers & models; --all adds the numbered combined inventory",
		"swarm           Build and run a multi-model swarm with numbered seat selection",
		"settings        Show or change settings (TUI, show/set, providers, mode, doctor)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

// P12: the help emphasis block advertises the setup TUI directly under the
// Usage line and names the --wizard escape for the classic line wizard.
func TestPrintProHelpAdvertisesSetupTUI(t *testing.T) {
	out := captureStdout(t, printProHelp)
	for _, want := range []string{
		">>> agy pro setup  opens the full-screen setup TUI <<<",
		"agy pro setup --wizard",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing setup TUI emphasis %q:\n%s", want, out)
		}
	}
}

func TestInvalidModeArgMessage(t *testing.T) {
	msg := invalidModeArgMessage("bogus")
	for _, want := range []string{"Invalid mode argument", "\"bogus\"", "status", "list", "restore"} {
		if !strings.Contains(msg, want) {
			t.Errorf("mode error hint missing %q: %s", want, msg)
		}
	}
}

// F-1: doctor flag parsing.
func TestParseDoctorArgs(t *testing.T) {
	cases := []struct {
		args    []string
		action  doctorAction
		target  string
		errWant bool
	}{
		{args: nil, action: doctorDiagnose},
		{args: []string{}, action: doctorDiagnose},
		{args: []string{"--fix-keys"}, action: doctorFixKeys, target: ""},
		{args: []string{"--fix-keys=all"}, action: doctorFixKeys, target: "all"},
		{args: []string{"--fix-keys=nw,charm"}, action: doctorFixKeys, target: "nw,charm"},
		{args: []string{"--fix-keys", "nw"}, action: doctorFixKeys, target: "nw"},
		{args: []string{"--rollback-keys", "C:\\backup\\run1"}, action: doctorRollbackKeys, target: "C:\\backup\\run1"},
		{args: []string{"--rollback-keys=C:\\backup\\run1"}, action: doctorRollbackKeys, target: "C:\\backup\\run1"},
		{args: []string{"--rollback-keys"}, errWant: true},
		{args: []string{"--rollback-keys="}, errWant: true},
		{args: []string{"--fix-keys", "--rollback-keys", "x"}, errWant: true},
		{args: []string{"--nope"}, errWant: true},
	}
	for _, tc := range cases {
		got, err := parseDoctorArgs(tc.args)
		if tc.errWant {
			if err == nil {
				t.Errorf("parseDoctorArgs(%v) expected error, got %+v", tc.args, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDoctorArgs(%v) unexpected error: %v", tc.args, err)
			continue
		}
		if got.action != tc.action || got.target != tc.target {
			t.Errorf("parseDoctorArgs(%v) = (%v, %q); want (%v, %q)", tc.args, got.action, got.target, tc.action, tc.target)
		}
	}
}

// F-1: key status lines carry the probe verdict, the source (env/HKCU/BOM
// variant), and a masked hint - never a clear value.
func TestPrintKeyAudits(t *testing.T) {
	audits := []providers.KeyAudit{
		{
			Provider: "nw", Env: "NW_API_KEY", Resolved: true,
			Origin: providers.KeyOriginUserBOM, MaskedHint: "sk-real…0001",
			Probe: providers.KeyProbeResult{Status: providers.ProbeValid, HTTPStatus: 200, Detail: "credential accepted by upstream"},
		},
		{
			Provider: "openai", Env: "OPENAI_API_KEY", Resolved: true,
			Origin: providers.KeyOriginProcessEnv, MaskedHint: "sk-ab…xy",
			Probe: providers.KeyProbeResult{Status: providers.ProbeInvalid, HTTPStatus: 401, Detail: "invalid_api_key"},
		},
		{
			Provider: "anth", Env: "ANTH_API_KEY", Resolved: true,
			Origin: providers.KeyOriginUserExact, MaskedHint: "***",
			Probe: providers.KeyProbeResult{Status: providers.ProbeIndeterminate, HTTPStatus: 404},
		},
		{
			Provider: "charm", Env: "CHARM_API_KEY", Resolved: false,
			Probe: providers.KeyProbeResult{Status: providers.ProbeSkipped, Detail: "no usable key found for CHARM_API_KEY"},
		},
	}
	out := captureStdout(t, func() {
		if printKeyAudits(audits) {
			t.Error("printKeyAudits must report failure when a key probes invalid")
		}
	})
	for _, want := range []string{
		"valid | source: hkcu-bom-variant | mask: sk-real…0001",
		"invalid (HTTP 401) | source: process-env | mask: sk-ab…xy",
		"indeterminate (HTTP 404) | source: hkcu-exact | mask: ***",
		"SKIP | source: none | mask: -",
		"[FAIL] key openai (OPENAI_API_KEY)",
		"[SKIP] key charm (CHARM_API_KEY)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("key audit output missing %q:\n%s", want, out)
		}
	}

	allOK := []providers.KeyAudit{{
		Provider: "nw", Env: "NW_API_KEY", Resolved: true,
		Origin: providers.KeyOriginProcessEnv, MaskedHint: "sk-…001",
		Probe: providers.KeyProbeResult{Status: providers.ProbeValid},
	}}
	out = captureStdout(t, func() {
		if !printKeyAudits(allOK) {
			t.Error("printKeyAudits must pass when every key is valid")
		}
	})
	if !strings.Contains(out, "[OK]   key nw (NW_API_KEY): valid") {
		t.Errorf("valid key line missing:\n%s", out)
	}
}

// F-1: --fix-keys calls ResolveRepairTargets + RepairWindowsKeyNames and
// prints the backup path that --rollback-keys consumes.
func TestRunDoctorFixKeysPrintsBackupRollbackTarget(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)

	oldResolve, oldRepair := resolveRepairTargets, repairedWindowsKeyNames
	t.Cleanup(func() { resolveRepairTargets, repairedWindowsKeyNames = oldResolve, oldRepair })

	var gotTargets string
	resolveRepairTargets = func(r *providers.Registry, arg string) ([]string, error) {
		gotTargets = arg
		return []string{"W6_FIX_KEY"}, nil
	}
	repairedWindowsKeyNames = func(r *providers.Registry, opts providers.RepairOptions) (*providers.RepairReport, error) {
		if len(opts.EnvNames) != 1 || opts.EnvNames[0] != "W6_FIX_KEY" {
			t.Errorf("repair got env names %v", opts.EnvNames)
		}
		return &providers.RepairReport{
			BackupPath: filepath.Join(t.TempDir(), "20260102_030405", "registry-keys.v2.json"),
			Repaired:   1,
			Entries: []providers.RepairEntry{{
				Provider: "fix", Env: "W6_FIX_KEY", Action: providers.RepairActionRewrite,
				VariantName: "\ufeffW6_FIX_KEY", MaskedValue: "sk-…0001",
			}},
		}, nil
	}

	out := captureStdout(t, func() { runDoctorFixKeys("all") })
	if gotTargets != "all" {
		t.Errorf("ResolveRepairTargets got %q; want %q", gotTargets, "all")
	}
	for _, want := range []string{
		"Targets: W6_FIX_KEY",
		"[FIXED] fix (W6_FIX_KEY)",
		"Repaired: 1",
		"Backup written:",
		"Rollback target (backup dir):",
		"Undo with: agy pro doctor --rollback-keys",
		"registry-keys.v2.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fix-keys output missing %q:\n%s", want, out)
		}
	}
}

// F-1 / S-5: --rollback-keys accepts the backup directory (resolving the
// versioned backup file first) and a direct file path.
func TestRunDoctorRollbackAcceptsDirOrFile(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)

	oldRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = oldRollback })

	var gotPath string
	rollbackKeyNames = func(backupPath string, opts providers.RepairOptions) error {
		gotPath = backupPath
		return nil
	}

	backupDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(backupDir, "registry-keys.v2.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { runDoctorRollback(backupDir) })
	wantFile := filepath.Join(backupDir, "registry-keys.v2.json")
	if gotPath != wantFile {
		t.Errorf("rollback got %q; want %q", gotPath, wantFile)
	}
	if !strings.Contains(out, "[SUCCESS]") {
		t.Errorf("rollback output missing success:\n%s", out)
	}

	// A direct file path is passed through unchanged.
	direct := filepath.Join(t.TempDir(), "custom-backup.json")
	runDoctorRollback(direct)
	if gotPath != direct {
		t.Errorf("rollback got %q; want %q", gotPath, direct)
	}
}

// F-1: the doctor's provider check is probe-based (AuditProviderKeys), not
// presence-only.
func TestRunDoctorWiresProbeBasedKeyAudit(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)

	oldAudit := auditProviderKeys
	t.Cleanup(func() { auditProviderKeys = oldAudit })

	called := false
	auditProviderKeys = func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
		called = true
		return []providers.KeyAudit{{
			Provider: "fix", Env: "W6_FIX_KEY", Resolved: true,
			Origin: providers.KeyOriginUserBOM, MaskedHint: "sk-…01",
			Probe: providers.KeyProbeResult{Status: providers.ProbeValid, Detail: "credential accepted by upstream"},
		}}
	}

	out := captureStdout(t, func() { runDoctor(nil) })
	if !called {
		t.Fatal("runDoctor did not call AuditProviderKeys")
	}
	if !strings.Contains(out, "key fix (W6_FIX_KEY): valid | source: hkcu-bom-variant | mask: sk-…01") {
		t.Errorf("probe-based key line missing:\n%s", out)
	}
	if strings.Contains(out, "environment variables not set") {
		t.Errorf("presence-only key check must be gone:\n%s", out)
	}
}

// I2: --strict parses on its own and combines with the action flags.
func TestParseDoctorArgsStrict(t *testing.T) {
	cases := []struct {
		args   []string
		strict bool
		action doctorAction
	}{
		{args: []string{"--strict"}, strict: true, action: doctorDiagnose},
		{args: []string{"--strict", "--fix-keys=nw"}, strict: true, action: doctorFixKeys},
		{args: []string{"--fix-keys", "nw", "--strict"}, strict: true, action: doctorFixKeys},
	}
	for _, tc := range cases {
		got, err := parseDoctorArgs(tc.args)
		if err != nil {
			t.Errorf("parseDoctorArgs(%v) unexpected error: %v", tc.args, err)
			continue
		}
		if got.strict != tc.strict || got.action != tc.action {
			t.Errorf("parseDoctorArgs(%v) = (strict=%v, action=%v); want (strict=%v, action=%v)",
				tc.args, got.strict, got.action, tc.strict, tc.action)
		}
	}
	// A misspelled strict-like token is still rejected with the usage line.
	if _, err := parseDoctorArgs([]string{"--strictx"}); err == nil {
		t.Error("parseDoctorArgs(--strictx) expected unknown-option error")
	}
}

// doctorHarness redirects every environment-touching seam runDoctor uses and
// restores it on cleanup. It returns a pointer capturing the --strict exit
// code (stays -1 when doctor never exits).
func doctorHarness(t *testing.T, realAgyPath string, realAgyErr error) *int {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv(paths.EnvAgyProHome, home)

	oldFind, oldSelf, oldExit := findRealAgy, selfExecutable, doctorExit
	t.Cleanup(func() { findRealAgy, selfExecutable, doctorExit = oldFind, oldSelf, oldExit })

	findRealAgy = func() (string, error) { return realAgyPath, realAgyErr }
	selfExecutable = func() (string, error) { return filepath.Join(home, "bin", "agy.exe"), nil }

	exitCode := -1
	doctorExit = func(code int) { exitCode = code }
	return &exitCode
}

// I2: strict exits 1 when the structural checks fail (real agy missing).
func TestRunDoctorStrictExitsOnStructuralFailure(t *testing.T) {
	exitCode := doctorHarness(t, "", errors.New("real agy binary not found"))

	out := captureStdout(t, func() { runDoctor([]string{"--strict"}) })
	if *exitCode != 1 {
		t.Errorf("strict doctor exit code = %d; want 1\noutput:\n%s", *exitCode, out)
	}
	if !strings.Contains(out, "[FAIL] Real agy binary not detected.") {
		t.Errorf("structural failure line missing:\n%s", out)
	}
	if !strings.Contains(out, "Strict diagnostics: structural checks failed") {
		t.Errorf("strict gate summary missing:\n%s", out)
	}
}

// I2 (N-3 semantics): provider-key probe verdicts (valid/invalid/SKIP) must
// not influence the exit code, even under --strict. Structural checks pass in
// this scenario, so the strict gate must not fire despite the invalid key.
func TestRunDoctorStrictIgnoresKeyProbeVerdicts(t *testing.T) {
	exitCode := doctorHarness(t, t.TempDir(), nil)
	writeTestRegistry(t)

	oldAudit := auditProviderKeys
	t.Cleanup(func() { auditProviderKeys = oldAudit })
	auditProviderKeys = func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
		return []providers.KeyAudit{{
			Provider: "fix", Env: "W6_FIX_KEY", Resolved: true,
			Origin: providers.KeyOriginProcessEnv, MaskedHint: "sk-…01",
			Probe: providers.KeyProbeResult{Status: providers.ProbeInvalid, HTTPStatus: 401},
		}}
	}

	out := captureStdout(t, func() { runDoctor([]string{"--strict"}) })
	if *exitCode != -1 {
		t.Errorf("strict doctor exited with %d on an invalid key; key probes must not affect exit code (N-3)\noutput:\n%s", *exitCode, out)
	}
	if !strings.Contains(out, "[FAIL] key fix (W6_FIX_KEY): invalid (HTTP 401)") {
		t.Errorf("invalid-key verdict line missing:\n%s", out)
	}
	if !strings.Contains(out, "[WARN] Some items need attention.") {
		t.Errorf("non-strict summary missing:\n%s", out)
	}
}

// I2: without --strict a structural failure never terminates the process
// (today's interactive behavior), and the shim-location mismatch is a
// structural failure under strict (installed-location check).
func TestRunDoctorStrictShimLocationAndNonStrict(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv(paths.EnvAgyProHome, home)

	oldFind, oldSelf, oldExit := findRealAgy, selfExecutable, doctorExit
	t.Cleanup(func() { findRealAgy, selfExecutable, doctorExit = oldFind, oldSelf, oldExit })
	findRealAgy = func() (string, error) { return t.TempDir(), nil }

	exitCode := -1
	doctorExit = func(code int) { exitCode = code }

	// Shim running from an unrelated directory (test binary path).
	out := captureStdout(t, func() { runDoctor([]string{"--strict"}) })
	if exitCode != 1 {
		t.Errorf("strict doctor exit code = %d; want 1 for a shim outside %s\noutput:\n%s", exitCode, filepath.Join(home, "bin"), out)
	}
	if !strings.Contains(out, "not first on PATH") {
		t.Errorf("shim location warning missing:\n%s", out)
	}

	// Same environment without --strict: no exit, WARN only.
	out = captureStdout(t, func() { runDoctor(nil) })
	if exitCode != 1 { // unchanged from the strict run above
		t.Errorf("non-strict doctor must not exit; exit code changed to %d\noutput:\n%s", exitCode, out)
	}
	if !strings.Contains(out, "[WARN] Running shim executable:") {
		t.Errorf("non-strict shim warning missing:\n%s", out)
	}
	if strings.Contains(out, "Strict diagnostics: structural checks failed") {
		t.Errorf("strict gate summary must be absent without --strict:\n%s", out)
	}
}

// I6: the real-agy failure guidance points at the official Antigravity site
// and the AGY_PRO_REAL_AGY override, without inventing a download URL.
func TestRunDoctorRealAgyFixMentionsOfficialSite(t *testing.T) {
	exitCode := doctorHarness(t, "", errors.New("real agy binary not found"))

	out := captureStdout(t, func() { runDoctor([]string{"--strict"}) })
	for _, want := range []string{"https://antigravity.google", "AGY_PRO_REAL_AGY"} {
		if !strings.Contains(out, want) {
			t.Errorf("real-agy fix guidance missing %q:\n%s", want, out)
		}
	}
	if *exitCode != 1 {
		t.Errorf("strict exit code = %d; want 1", *exitCode)
	}
}

// writeTestRegistry points providers.Load at a minimal registry so doctor
// tests never read the real ~/.agy-pro/providers.json.
func writeTestRegistry(t *testing.T) *providers.Registry {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "providers.json")
	data := `{"providers":[{"name":"fix","baseURL":"https://fix.example/v1","apiKeyEnv":"W6_FIX_KEY","models":[{"id":"m1"}]}]}`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, p)
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("loading test registry: %v", err)
	}
	return reg
}

// ---------------------------------------------------------------------------
// U-wiring dispatch tests (architect-report docs/261012_0002 §2.6 + §4)
// ---------------------------------------------------------------------------

// The pro dispatch lines terminate the process via os.Exit with the command
// function's return value. os.Exit cannot be exercised in-process, so each
// dispatch case runs end to end in a helper subprocess of this test binary
// (standard Go helper-process pattern): the child synthesizes argv, calls
// main(), and the parent asserts the real exit code and captured stdout.
const dispatchHelperEnv = "AGY_PRO_DISPATCH_HELPER"

// TestProDispatchHelperProcess is the child side of TestProDispatchWiring.
// The parent picks the case through dispatchHelperEnv; without that env var
// the test is a no-op so ordinary `go test` runs skip it.
func TestProDispatchHelperProcess(t *testing.T) {
	switch os.Getenv(dispatchHelperEnv) {
	case "models-all":
		// Stub the inventory source: the child must never spawn `agy models`.
		loadModelsInventory = func(context.Context) (*inventory.Inventory, error) {
			return &inventory.Inventory{Entries: []inventory.Entry{{
				Number:      1,
				Kind:        "native",
				Group:       "native",
				ModelID:     "gemini-3.8-flash-high",
				DisplayName: "Gemini 3.8 Flash (High)",
				Spec:        "native:gemini-3.8-flash-high",
			}}}, nil
		}
		os.Args = []string{"agy-test", "pro", "models", "--all"}
		main()
	case "models-legacy":
		os.Args = []string{"agy-test", "pro", "models"}
		main()
	case "models-bad-flag":
		os.Args = []string{"agy-test", "pro", "models", "--bogus"}
		main()
	case "swarm-dry-run":
		os.Args = []string{"agy-test", "pro", "swarm", "--template", "code-review", "--seats", "native:gemini-3.8-flash-high", "--dry-run"}
		main()
	case "swarm-missing-required":
		os.Args = []string{"agy-test", "pro", "swarm", "--root", "."}
		main()
	case "settings-version":
		os.Args = []string{"agy-test", "pro", "settings", "version-info"}
		main()
	case "settings-unknown":
		os.Args = []string{"agy-test", "pro", "settings", "bogus-verb"}
		main()
	case "unknown-subcommand":
		os.Args = []string{"agy-test", "pro", "bogus-subcommand"}
		main()
	case "config-bad-profile":
		// C2 sweep: the invalid-profile operand is a planted key; the child
		// must exit 1 with the operand masked on stderr.
		os.Args = []string{"agy-test", "pro", "config", "default-profile", maskSweepKey}
		main()
	case "pro-unknown-subcommand":
		// C2 sweep: a planted key pasted into the `pro` subcommand slot.
		os.Args = []string{"agy-test", "pro", maskSweepKey}
		main()
	case "pro-unknown-subcommand-akia":
		// D2 sweep: an AKIA key pasted into the `pro` subcommand slot; the
		// ORIGINAL-CASE argument must be masked (AKIA/ASIA class).
		os.Args = []string{"agy-test", "pro", sweepAKIAKey}
		main()
	case "config-unknown-subcommand-akia":
		// D2 sweep: an AKIA key pasted into the `config` subcommand slot.
		os.Args = []string{"agy-test", "pro", "config", sweepAKIAKey}
		main()
	case "mode-bad-arg-akia":
		// D2 sweep: an AKIA key pasted into the `mode` argument slot.
		os.Args = []string{"agy-test", "pro", "mode", sweepAKIAKey}
		main()
	}
	// Dispatch paths that return instead of exiting (help) fall through and
	// the child finishes with the test framework's exit code 0.
}

// runDispatchHelper spawns this test binary as a helper subprocess running
// one dispatch case. The child gets a hermetic environment (temp agy-pro
// home, a providers path that does not exist) so no real configuration is
// read or written. Returns the real process exit code and the child stdout.
func runDispatchHelper(t *testing.T, dispatchCase string) (int, string) {
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
	return code, stdout.String()
}

// TestProDispatchWiring drives the `agy pro` switch end to end: models
// forwards subArgs into runModelsCmd (legacy no-arg output preserved, --all
// rendering the combined inventory), swarm and settings are reachable, and
// every dispatch line passes the command's exit code through os.Exit. The
// unknown-subcommand default case keeps its exit 1.
func TestProDispatchWiring(t *testing.T) {
	cases := []struct {
		name       string
		dispatch   string
		wantCode   int
		wantStdout []string
	}{
		{
			name:       "models --all passes args through and renders the inventory",
			dispatch:   "models-all",
			wantCode:   0,
			wantStdout: []string{"gemini-3.8-flash-high", "[native]"},
		},
		{
			name:       "models no-args keeps the legacy output verbatim",
			dispatch:   "models-legacy",
			wantCode:   0,
			wantStdout: []string{"agy-pro Configured Models"},
		},
		{
			name:     "models --bogus exits 2 through the dispatch line",
			dispatch: "models-bad-flag",
			wantCode: 2,
		},
		{
			name:       "swarm --dry-run prints the fanout command and exits 0",
			dispatch:   "swarm-dry-run",
			wantCode:   0,
			wantStdout: []string{"agy pro fanout"},
		},
		{
			name:     "swarm missing required options exits 2 without prompting",
			dispatch: "swarm-missing-required",
			wantCode: 2,
		},
		{
			name:       "settings version-info is reachable and exits 0",
			dispatch:   "settings-version",
			wantCode:   0,
			wantStdout: []string{"agy-pro version"},
		},
		{
			name:     "settings unknown verb exits 2",
			dispatch: "settings-unknown",
			wantCode: 2,
		},
		{
			name:     "unknown subcommand still exits 1",
			dispatch: "unknown-subcommand",
			wantCode: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runDispatchHelper(t, tc.dispatch)
			if code != tc.wantCode {
				t.Fatalf("dispatch case %q exit code = %d; want %d\nstdout:\n%s", tc.dispatch, code, tc.wantCode, out)
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(out, want) {
					t.Errorf("dispatch case %q stdout missing %q:\n%s", tc.dispatch, want, out)
				}
			}
		})
	}
}

// U7 machine-output guard (contract §2.10): the community banner lives only
// in the interactive launcher, so `agy pro` help and status output must
// never mention it.
func TestProHelpAndStatusHaveNoCommunityBanner(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	t.Setenv(providers.EnvProviders, filepath.Join(t.TempDir(), "missing-providers.json"))
	outputs := map[string]string{
		"help":   captureStdout(t, printProHelp),
		"status": captureStdout(t, runStatus),
	}
	for name, out := range outputs {
		if strings.Contains(out, "reddit.com") {
			t.Errorf("agy pro %s output leaks the community banner:\n%s", name, out)
		}
	}
}

// captureStderr runs fn and returns everything it wrote to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// stubDoctorExit captures the doctorExit seam (os.Exit) for tests that drive
// the doctor failure paths.
func stubDoctorExit(t *testing.T) *int {
	t.Helper()
	old := doctorExit
	t.Cleanup(func() { doctorExit = old })
	code := 0
	doctorExit = func(c int) { code = c }
	return &code
}

// TestPrintConfiguredProvidersMasksKeyShapedMissingEnv (E8): the
// `Missing (%s)` composite embeds the registry env name; a key-shaped env
// (misdirected paste) must render masked INSIDE the composite, because
// whole-string classification on the composite never fires. Benign env names
// stay readable.
func TestPrintConfiguredProvidersMasksKeyShapedMissingEnv(t *testing.T) {
	const benignEnv = "E8_BENIGN_MISSING_ENV"
	cases := []struct{ plant, mask string }{
		{testfixtures.AnthropicPlanted, "sk-an****"},
		{testfixtures.AKIAExample, "AKIAI****"},
		{testfixtures.AWSSecretNoSlash, "wJalr****"},
	}
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "benign", BaseURL: "https://benign.example/v1", APIKeyEnv: benignEnv},
	}}
	for _, tc := range cases {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name: "p-" + tc.mask, BaseURL: "https://p.example/v1", APIKeyEnv: tc.plant,
		})
	}
	out := captureStdout(t, func() { printConfiguredProvidersAndModels(reg) })
	if !strings.Contains(out, "Missing ("+benignEnv+")") {
		t.Errorf("benign missing env must stay readable:\n%s", out)
	}
	for _, tc := range cases {
		if strings.Contains(out, tc.plant) {
			t.Errorf("status output leaks the planted env %q raw:\n%s", tc.plant, out)
		}
		if !strings.Contains(out, "Missing ("+tc.mask+")") {
			t.Errorf("status output missing masked composite Missing (%s):\n%s", tc.mask, out)
		}
	}
}

// K1 banner sink pin: the status / legacy-models banner renders the
// capability summary, which embeds the registry-controlled reasoningEffort.
// Summary masks a key-shaped effort at the composition (plainly key-shaped
// and location-shaped alike, K1), and the banner slot masks the composed
// summary again through MaskOperand as defense in depth (same as the models
// --json and RenderNumbered sinks): the composed "tools, thinking(wJalr****)"
// is itself secret-shaped, so the line renders "tools****". Without the
// banner slot change the line renders "tools, thinking(wJalr****)" and this
// test fails.
func TestPrintConfiguredProvidersAndModelsMasksKeyShapedEffort(t *testing.T) {
	const plainPlant = "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	const locationPlant = "./" + plainPlant
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "NW",
		BaseURL: "https://api.neuralwatt.com/v1",
		Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true, ReasoningEffort: locationPlant,
			}},
			{ID: "deepseek-v4.1-flash-flex", Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true, ReasoningEffort: plainPlant,
			}},
			{ID: "qwen3.8-flash", Capabilities: providers.Capabilities{
				SupportsTools: true, SupportsThinking: true, ReasoningEffort: "high",
			}},
		},
	}}}
	out := captureStdout(t, func() { printConfiguredProvidersAndModels(reg) })
	if strings.Contains(out, plainPlant) {
		t.Errorf("banner leaks the key-shaped reasoningEffort raw:\n%s", out)
	}
	// The sink renders the composed summary through MaskOperand (first-5+****
	// of the whole summary); the composition mask inside Summary already
	// renders the effort as "thinking(wJalr****)", and the sink mask is the
	// defense-in-depth layer that pins the line to "tools****".
	if !strings.Contains(out, "Capabilities: tools****") {
		t.Errorf("banner must render the key-shaped effort masked:\n%s", out)
	}
	if !strings.Contains(out, "Capabilities: tools, thinking(high)") {
		t.Errorf("banner must keep the benign effort readable:\n%s", out)
	}
}

// TestRunDoctorFixKeysMasksFailureTailAndNote (E6): the --fix-keys failure
// tail and the entry note line echo registry-derived env/variant names; a
// key-shaped env must reach both masked, benign shapes readable.
func TestRunDoctorFixKeysMasksFailureTailAndNote(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)
	const plantEnv = testfixtures.AnthropicPlanted

	oldResolve, oldRepair := resolveRepairTargets, repairedWindowsKeyNames
	t.Cleanup(func() { resolveRepairTargets, repairedWindowsKeyNames = oldResolve, oldRepair })
	resolveRepairTargets = func(*providers.Registry, string) ([]string, error) {
		return []string{plantEnv}, nil
	}
	repairedWindowsKeyNames = func(*providers.Registry, providers.RepairOptions) (*providers.RepairReport, error) {
		return &providers.RepairReport{
			Failed: 1,
			Entries: []providers.RepairEntry{{
				Provider: "fix", Env: plantEnv,
				VariantName: "\ufeff" + plantEnv,
				Action:      providers.RepairActionRewrite,
				Note:        "clean name rewritten, but removing the BOM variant failed: remove \ufeff" + plantEnv + ": access denied",
			}},
		}, fmt.Errorf("%s: access denied (aggregate)", plantEnv)
	}

	exit := stubDoctorExit(t)
	var out, errOut string
	// runDoctorFixKeys writes to both streams in one call; nest the captures.
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() { runDoctorFixKeys("all") })
	})
	if *exit != 1 {
		t.Fatalf("doctor exit = %d; want 1", *exit)
	}
	combined := out + errOut
	if strings.Contains(combined, plantEnv) || strings.Contains(combined, "\ufeff"+plantEnv) {
		t.Errorf("fix-keys output leaks the planted env raw:\nstdout=%s\nstderr=%s", out, errOut)
	}
	if !strings.Contains(combined, "Key repair finished with errors: [REDACTED]: access denied (aggregate)") {
		t.Errorf("failure tail must redact the seeded env:\nstderr=%s", errOut)
	}
	if !strings.Contains(combined, "[FIXED] fix (sk-an****)") {
		t.Errorf("entry line must carry the masked env:\nstdout=%s", out)
	}
	if !strings.Contains(combined, "note: clean name rewritten, but removing the BOM variant failed: remove [REDACTED]: access denied") {
		t.Errorf("note line must redact the seeded variant name:\nstdout=%s", out)
	}
}

// TestRunDoctorRollbackMasksPlantedBackupPath (E5): the --rollback-keys
// operand and the forwarded ReadFile error re-embed the raw backup path; a
// key-shaped operand must render masked on the "Backup file:" line and be
// redacted out of the failure tail, while benign paths stay readable.
func TestRunDoctorRollbackMasksPlantedBackupPath(t *testing.T) {
	oldRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = oldRollback })
	rollbackKeyNames = func(backupPath string, opts providers.RepairOptions) error {
		return fmt.Errorf("open %s: The system cannot find the file specified.", backupPath)
	}

	t.Run("bare base64 plant (live repro shape)", func(t *testing.T) {
		const plant = testfixtures.AWSSecretNoSlash
		exit := stubDoctorExit(t)
		var out, errOut string
		errOut = captureStderr(t, func() {
			out = captureStdout(t, func() { runDoctorRollback(plant) })
		})
		if *exit != 1 {
			t.Fatalf("exit = %d; want 1", *exit)
		}
		if !strings.Contains(out, "Backup file: wJalr****") {
			t.Errorf("backup-file line must render the mask:\nstdout=%s", out)
		}
		if strings.Contains(out, plant) || strings.Contains(errOut, plant) {
			t.Errorf("rollback output leaks the planted operand raw:\nstdout=%s\nstderr=%s", out, errOut)
		}
		if !strings.Contains(errOut, "Rollback failed: open [REDACTED]:") {
			t.Errorf("failure tail must redact the seeded path:\nstderr=%s", errOut)
		}
	})

	t.Run("directory target with sk- plant", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), testfixtures.AnthropicPlanted)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		exit := stubDoctorExit(t)
		var out, errOut string
		errOut = captureStderr(t, func() {
			out = captureStdout(t, func() { runDoctorRollback(dir) })
		})
		if *exit != 1 {
			t.Fatalf("exit = %d; want 1", *exit)
		}
		if strings.Contains(out, testfixtures.AnthropicPlanted) || strings.Contains(errOut, testfixtures.AnthropicPlanted) {
			t.Errorf("rollback output leaks the planted dir raw:\nstdout=%s\nstderr=%s", out, errOut)
		}
		if !strings.Contains(out, "Backup file:") || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("backup-file line must mask the planted dir:\nstdout=%s", out)
		}
	})

	t.Run("directory target with AKIA plant", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), testfixtures.AKIAExample)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		exit := stubDoctorExit(t)
		var out, errOut string
		errOut = captureStderr(t, func() {
			out = captureStdout(t, func() { runDoctorRollback(dir) })
		})
		if *exit != 1 {
			t.Fatalf("exit = %d; want 1", *exit)
		}
		if strings.Contains(out, testfixtures.AKIAExample) || strings.Contains(errOut, testfixtures.AKIAExample) {
			t.Errorf("rollback output leaks the planted dir raw:\nstdout=%s\nstderr=%s", out, errOut)
		}
		if !strings.Contains(out, "Backup file:") || !strings.Contains(out, "[REDACTED]") {
			t.Errorf("backup-file line must mask the planted dir:\nstdout=%s", out)
		}
	})

	t.Run("benign path stays readable", func(t *testing.T) {
		dir := t.TempDir()
		exit := stubDoctorExit(t)
		var out, errOut string
		errOut = captureStderr(t, func() {
			out = captureStdout(t, func() { runDoctorRollback(dir) })
		})
		if *exit != 1 {
			t.Fatalf("exit = %d; want 1", *exit)
		}
		wantFile := filepath.Join(dir, "registry-keys.v2.json")
		if !strings.Contains(out, wantFile) {
			t.Errorf("benign backup path must stay readable:\nstdout=%s", out)
		}
		if !strings.Contains(errOut, wantFile) {
			t.Errorf("benign failure tail must stay readable:\nstderr=%s", errOut)
		}
		if strings.Contains(out, "[REDACTED]") || strings.Contains(errOut, "[REDACTED]") {
			t.Errorf("benign rollback output must not be redacted:\nstdout=%s\nstderr=%s", out, errOut)
		}
	})
}

// F-beta F7: the nil-report "Key repair failed" tail echoes the raw repair
// error (a composite embedding registry-derived env names); a key-shaped name
// must render masked, and the branch must RETURN instead of falling through to
// printRepairReport(nil) under the doctorExit test seam.
func TestRunDoctorFixKeysNilReportMasksAndReturns(t *testing.T) {
	const plant = testfixtures.AnthropicPlanted
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)

	oldResolve, oldRepair := resolveRepairTargets, repairedWindowsKeyNames
	t.Cleanup(func() { resolveRepairTargets, repairedWindowsKeyNames = oldResolve, oldRepair })
	resolveRepairTargets = func(*providers.Registry, string) ([]string, error) {
		return []string{plant}, nil
	}
	repairedWindowsKeyNames = func(*providers.Registry, providers.RepairOptions) (*providers.RepairReport, error) {
		return nil, fmt.Errorf("repair crashed for %s", plant)
	}

	exit := stubDoctorExit(t)
	var out, errOut string
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() { runDoctorFixKeys("all") })
	})
	if *exit != 1 {
		t.Fatalf("exit = %d; want 1", *exit)
	}
	if strings.Contains(errOut, plant) {
		t.Errorf("nil-report failure tail echoes the planted env raw:\n%s", errOut)
	}
	if !strings.Contains(errOut, "[REDACTED]") {
		t.Errorf("nil-report failure tail missing the mask:\n%s", errOut)
	}
	if strings.Contains(out, "Repaired:") {
		t.Errorf("nil-report branch fell through to printRepairReport:\n%s", out)
	}
}

func TestRunDoctorFixKeysMasksResolveErrorTokens(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "home"))
	writeTestRegistry(t)

	oldResolve := resolveRepairTargets
	t.Cleanup(func() { resolveRepairTargets = oldResolve })
	oldExit := doctorExit
	t.Cleanup(func() { doctorExit = oldExit })
	doctorExit = func(int) {}
	plant := testfixtures.AWSSecretExample
	resolveRepairTargets = func(r *providers.Registry, arg string) ([]string, error) {
		return nil, fmt.Errorf("cannot resolve %q", "native:"+plant)
	}

	errOut := captureStderr(t, func() { runDoctorFixKeys("1,native:" + plant) })
	if strings.Contains(errOut, plant) {
		t.Errorf("doctor fix-keys resolve error leaked the decorated plant: %q", errOut)
	}
	if !strings.Contains(errOut, "wJalr****") && !strings.Contains(errOut, "[REDACTED]") {
		t.Errorf("doctor fix-keys resolve error must render the plant masked: %q", errOut)
	}
}

func TestRunDoctorRollbackMasksDecoratedPlant(t *testing.T) {
	oldRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = oldRollback })
	rollbackKeyNames = func(backupPath string, opts providers.RepairOptions) error {
		return fmt.Errorf("open %s: The system cannot find the file specified.", backupPath)
	}
	plant := "native:" + testfixtures.AWSSecretExample
	exit := stubDoctorExit(t)
	var out, errOut string
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() { runDoctorRollback(plant) })
	})
	if *exit != 1 {
		t.Fatalf("exit = %d; want 1", *exit)
	}
	if strings.Contains(out, plant) || strings.Contains(errOut, plant) {
		t.Errorf("rollback output leaks the decorated plant raw:\nstdout=%s\nstderr=%s", out, errOut)
	}
	// C3: the rendering is deterministic (U8 first-5 mask of the whole
	// operand), so the assertion pins it exactly instead of a bare "****"
	// that any short benign token would also satisfy.
	if !strings.Contains(out, "Backup file: nativ****") {
		t.Errorf("backup-file line must mask the decorated plant as nativ****:\nstdout=%s", out)
	}
}

// Batch K slot-hardening pin for the pre-existing main.go twin slot: the
// --rollback-keys slot re-embeds the untrusted target in the resolved file.
// A compound target (pattern-shaped AKIA + bare decorated secret) was only
// PARTIALLY masked by MaskOperand, so the old equality-only fallback was
// skipped and the bare secret echoed raw on the "Backup file:" line. The
// hardened two-pass slot runs the seeded sentence pass for every
// whitespace-carrying operand, seeded from the original target. Without the
// hardening the raw plant reaches stdout and this test fails.
func TestRunDoctorRollbackSlotHardenedCompoundTarget(t *testing.T) {
	oldRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = oldRollback })
	rollbackKeyNames = func(backupPath string, opts providers.RepairOptions) error {
		return fmt.Errorf("open %s: The system cannot find the file specified.", backupPath)
	}
	const bare = testfixtures.AWSSecretExample
	const akia = testfixtures.AKIAExample
	target := "prefix " + akia + " " + bare
	exit := stubDoctorExit(t)
	var out, errOut string
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() { runDoctorRollback(target) })
	})
	if *exit != 1 {
		t.Fatalf("exit = %d; want 1", *exit)
	}
	if strings.Contains(out, bare) || strings.Contains(out, akia) {
		t.Errorf("hardened rollback slot leaked the compound operand raw:\nstdout=%s", out)
	}
	if strings.Contains(errOut, bare) || strings.Contains(errOut, akia) {
		t.Errorf("rollback failure tail leaked the compound operand raw:\nstderr=%s", errOut)
	}
	if !strings.Contains(out, "Backup file: prefix [REDACTED] [REDACTED]") {
		t.Errorf("backup-file line must render the compound operand fully masked:\nstdout=%s", out)
	}
}

// Batch K byte-identity pins for the rollback twin slot: a one-space operand
// and a location-shaped path must render exactly as the plain
// MaskOperand-first slot rendered before the hardening (the single-token
// bare/decorated plants are already pinned by
// TestRunDoctorRollbackMasksPlantedBackupPath / ...MasksDecoratedPlant).
func TestRunDoctorRollbackSlotRenderingPins(t *testing.T) {
	oldRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = oldRollback })
	rollbackKeyNames = func(backupPath string, opts providers.RepairOptions) error {
		return errors.New("stop")
	}
	for _, tc := range []struct{ name, target, wantLine string }{
		{"one-space operand", "a b", "Backup file: a b"},
		{"location-shaped path", "./registry-keys.v2.json", "Backup file: ./registry-keys.v2.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exit := stubDoctorExit(t)
			var out, errOut string
			errOut = captureStderr(t, func() {
				out = captureStdout(t, func() { runDoctorRollback(tc.target) })
			})
			if *exit != 1 {
				t.Fatalf("exit = %d; want 1", *exit)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Errorf("rollback slot rendering drifted:\ngot:  %q\nwant: %q", out, tc.wantLine)
			}
			_ = errOut
		})
	}
}

// Batch L slot-gate pin for the main.go rollback twin: the batch-K gate ran
// the seeded pass only for whitespace-carrying operands, so the
// comma/semicolon-decorated class the OLD equality fallback used to seed
// (21827f6) rode raw again — a masked->raw regression vs 21827f6. A
// whitespace-free comma-decorated target ("./backups,<secret>") kept its
// bare secret raw on the "Backup file:" line. The widened gate
// (ContainsAny(op, " \t\n\r,;")) re-runs the seeded pass for the comma
// class, so both plants render masked on the line and in the failure tail
// (the tail seeds the per-token forms, so it masks fully). Without the
// widening the raw plants reach stdout and this test fails.
func TestRunDoctorRollbackSlotCommaDecoratedTargets(t *testing.T) {
	oldRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = oldRollback })
	rollbackKeyNames = func(backupPath string, opts providers.RepairOptions) error {
		return fmt.Errorf("open %s: The system cannot find the file specified.", backupPath)
	}
	for _, tc := range []struct{ name, target, wantLine string }{
		{"comma-decorated compound target", "./backups," + testfixtures.AWSSecretExample, "Backup file: ./backups,[REDACTED]"},
		{"comma-decorated bare secret", "note,abcdefghijklmnopqrs", "Backup file: note,[REDACTED]"},
		{"benign comma list", "read,search", "Backup file: read,search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exit := stubDoctorExit(t)
			var out, errOut string
			errOut = captureStderr(t, func() {
				out = captureStdout(t, func() { runDoctorRollback(tc.target) })
			})
			if *exit != 1 {
				t.Fatalf("exit = %d; want 1", *exit)
			}
			if !strings.Contains(out, tc.wantLine) {
				t.Errorf("rollback slot rendering drifted:\ngot:  %q\nwant: %q", out, tc.wantLine)
			}
			// A masked expectation must not carry the raw target anywhere;
			// the benign expectation carries it verbatim by design.
			if !strings.Contains(tc.wantLine, tc.target) {
				if strings.Contains(out, tc.target) {
					t.Errorf("rollback slot leaked the comma-decorated target raw:\nstdout=%s", out)
				}
				if strings.Contains(errOut, tc.target) {
					t.Errorf("rollback failure tail leaked the comma-decorated target raw:\nstderr=%s", errOut)
				}
			}
		})
	}
}

// TestPrintConfiguredProvidersMasksLookAlikeModelID pins the status banner's
// id and family columns, the two sinks this round moved onto the shared
// registry-slot policy. A look-alike-decorated model id (U+2025 TWO DOT LEADER +
// "/" + a lowercase 20-rune core) and the same plant in the family slot must
// render masked: the id column composes "<id> (<provider>)", so the credential
// sits behind an annotation that the whole-value classifier cannot see, and the
// family column is a registry slot. Before the policy round both reached
// MaskOperand, whose location exemption and pre-policy classifier left the core
// readable in the banner.
func TestPrintConfiguredProvidersMasksLookAlikeModelID(t *testing.T) {
	const core = "ab3def7gh9jk1mnpqr5t"
	const plant = "\u2025/" + core
	const masked = "ab3de****"

	// The provider name is lowercase on purpose: it makes the composed label
	// ("<id> (<provider>)") carry no uppercase letter, so the pre-policy
	// authority classified it as benign and rendered the core raw. That is what
	// makes this pin discriminate.
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.neuralwatt.com/v1",
		Models: []providers.Model{
			{ID: plant, Family: plant, ContextLength: 1048576, MaxOutputTokens: -1},
			{ID: "glm-5.3-flash-flex", Family: "glm", ContextLength: 1048576, MaxOutputTokens: -1},
		},
	}}}
	out := captureStdout(t, func() { printConfiguredProvidersAndModels(reg) })
	if strings.Contains(out, core) || strings.Contains(out, plant) {
		t.Errorf("status banner leaks the look-alike plant raw:\n%s", out)
	}
	// Both columns mask (the id column pads to the widest display name, so the
	// assertion is padding-insensitive).
	if !strings.Contains(out, "- "+masked) || !strings.Contains(out, "[Family: "+masked) {
		t.Errorf("status banner must mask the id and family columns:\n%s", out)
	}
	// The benign row stays byte-identical.
	if !strings.Contains(out, "- glm-5.3-flash-flex") || !strings.Contains(out, "[Family: glm ") {
		t.Errorf("benign banner row moved:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// P4-5: the setup entry gate requires a stdin console as well as a stdout
// console. The TUI key source reads the Windows console input buffer, so a
// console stdout paired with a PIPED stdin (`producer | agy pro setup`) would
// open a full-screen surface that can never accept typed text. The gate opens
// the TUI only when BOTH standard handles are consoles; every other shape keeps
// the byte-stable line wizard. swapSetupSeams (pro_setup_test.go) drives both
// probe seams, so no pin here needs a real console.
// ---------------------------------------------------------------------------

// TestSetupGateRequiresBothConsoleHandles pins the handle shapes: the TUI opens
// only for console stdout + console stdin, and the piped-stdin shape degrades to
// the wizard without ever reaching the TUI.
func TestSetupGateRequiresBothConsoleHandles(t *testing.T) {
	cases := []struct {
		name         string
		stdinConsole bool
		wantTUI      bool
	}{
		{"console stdout + console stdin opens the TUI", true, true},
		{"console stdout + piped stdin degrades to the wizard", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := swapSetupSeams(t)
			rec.stdinConsole = c.stdinConsole
			rec.tuiCode = 3
			rec.exitCode = -1

			runSetup(nil)

			if rec.tuiCalled != c.wantTUI {
				t.Fatalf("TUI called = %v; want %v", rec.tuiCalled, c.wantTUI)
			}
			if rec.wizardCalled == c.wantTUI {
				t.Fatalf("wizard called = %v; want %v", rec.wizardCalled, !c.wantTUI)
			}
			if !c.wantTUI {
				// Degrade path: the wizard gets stdin/stdout and no exit code is
				// requested, so the process exit stays the caller's.
				if rec.wizardIn != os.Stdin || rec.wizardOut != os.Stdout {
					t.Error("degrade path: wizard must receive stdin/stdout")
				}
				if rec.exitCode != -1 {
					t.Errorf("degrade path: exit code requested = %d; want none", rec.exitCode)
				}
				return
			}
			if rec.tuiIn != os.Stdin || rec.tuiOut != os.Stdout || rec.tuiErrw != os.Stderr {
				t.Error("TUI must receive the process stdio handles")
			}
			if rec.exitCode != 3 {
				t.Errorf("exit code = %d; want 3 (TUI result propagated)", rec.exitCode)
			}
		})
	}
}

// TestSetupGateNonConsoleStdoutDegradesOnAnyStdin pins today's behavior: a
// non-console stdout always takes the wizard, whatever stdin looks like, and
// the stdin probe is not even consulted (the stdout signal short-circuits), so a
// scripted caller with a closed stdin never has that handle probed.
func TestSetupGateNonConsoleStdoutDegradesOnAnyStdin(t *testing.T) {
	for _, stdinConsole := range []bool{true, false} {
		rec := swapSetupSeams(t)
		rec.cap = tui.Cap{SuppressBanner: true}
		rec.stdinConsole = stdinConsole
		rec.exitCode = -1

		runSetup(nil)

		if !rec.wizardCalled {
			t.Fatalf("stdinConsole=%v: non-console stdout must run the wizard", stdinConsole)
		}
		if rec.tuiCalled {
			t.Fatalf("stdinConsole=%v: non-console stdout must not run the TUI", stdinConsole)
		}
		if rec.stdinProbes != 0 {
			t.Errorf("stdinConsole=%v: stdin probe queried %d times; the stdout signal must short-circuit", stdinConsole, rec.stdinProbes)
		}
		if rec.exitCode != -1 {
			t.Errorf("stdinConsole=%v: exit code requested = %d; want none", stdinConsole, rec.exitCode)
		}
	}
}

// TestSetupGateWizardFlagForcesWizardOnAnyShape: --wizard keeps forcing the
// line wizard on every handle shape, console or not, and never probes stdin.
func TestSetupGateWizardFlagForcesWizardOnAnyShape(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		for _, stdinConsole := range []bool{true, false} {
			rec := swapSetupSeams(t)
			rec.cap = tui.Cap{SuppressBanner: suppress}
			rec.stdinConsole = stdinConsole
			rec.exitCode = -1

			runSetup([]string{"--wizard"})

			if !rec.wizardCalled {
				t.Fatalf("suppress=%v stdinConsole=%v: --wizard must run the wizard", suppress, stdinConsole)
			}
			if rec.tuiCalled {
				t.Fatalf("suppress=%v stdinConsole=%v: --wizard must not run the TUI", suppress, stdinConsole)
			}
			if rec.wizardIn != os.Stdin || rec.wizardOut != os.Stdout {
				t.Errorf("suppress=%v stdinConsole=%v: wizard must receive stdin/stdout", suppress, stdinConsole)
			}
			if rec.stdinProbes != 0 {
				t.Errorf("suppress=%v stdinConsole=%v: --wizard must not probe stdin", suppress, stdinConsole)
			}
			if rec.exitCode != -1 {
				t.Errorf("suppress=%v stdinConsole=%v: exit code requested = %d; want none", suppress, stdinConsole, rec.exitCode)
			}
		}
	}
}

// TestSetupGateUnknownArgStillFailsClosed: an unknown argument keeps its
// fail-closed contract on both stdin shapes (masked operand, help text, exit 2)
// and never reaches either runner.
func TestSetupGateUnknownArgStillFailsClosed(t *testing.T) {
	for _, stdinConsole := range []bool{true, false} {
		rec := swapSetupSeams(t)
		rec.stdinConsole = stdinConsole
		rec.exitCode = -1

		var stderr string
		stdout := captureStdout(t, func() {
			stderr = captureStderr(t, func() { runSetup([]string{"--bogus"}) })
		})

		if rec.exitCode != 2 {
			t.Errorf("stdinConsole=%v: exit code = %d; want 2", stdinConsole, rec.exitCode)
		}
		if !strings.Contains(stderr, `Unknown setup argument: "--bogus"`) {
			t.Errorf("stdinConsole=%v: stderr missing the masked operand line:\n%s", stdinConsole, stderr)
		}
		if !strings.Contains(stdout, "Usage: agy pro <command>") {
			t.Errorf("stdinConsole=%v: help text was not printed:\n%s", stdinConsole, stdout)
		}
		if rec.tuiCalled || rec.wizardCalled {
			t.Errorf("stdinConsole=%v: unknown argument must not run the TUI or the wizard", stdinConsole)
		}
	}
}

// ---------------------------------------------------------------------------
// FIX-C: the setup gate's degrade branch restores the console output mode its
// capability probe enabled. Detect turns VT processing on for the console
// output handle, and the TUI path hands that mode back through tui.RunSetup's
// own defer cap.Restore() (internal/tui/app.go:50). The degrade branch used to
// route into the line wizard with the mode still on, so the process outlived
// itself with a modified console. The pins below observe the restore through
// the setupCapRestore seam (Cap's own hook is unexported, so a test in this
// package can only observe the call site) and pin the branch table.
// ---------------------------------------------------------------------------

// setupRestoreRec records the entry gate's capability-restore calls.
type setupRestoreRec struct {
	calls int
	caps  []tui.Cap
}

// captureSetupRestore swaps the restore seam for the duration of one test. The
// shared seam set (swapSetupSeams, pro_setup_test.go) leaves setupCapRestore at
// its production value, whose Restore on a zero Cap is a no-op, so the older
// pins keep running the real wiring.
func captureSetupRestore(t *testing.T) *setupRestoreRec {
	t.Helper()
	rec := &setupRestoreRec{}
	old := setupCapRestore
	setupCapRestore = func(cap tui.Cap) {
		rec.calls++
		rec.caps = append(rec.caps, cap)
	}
	t.Cleanup(func() { setupCapRestore = old })
	return rec
}

// TestSetupGateDegradeRestoresProbedCapExactlyOnce pins FIX-C on every shape
// that probes the capability ladder and then degrades: the exact probed Cap is
// restored exactly once, the restore lands BEFORE the wizard runs (the wizard
// exits through setupExit on failure and an os.Exit skips defers), and both the
// wizard transcript and the exit code are unchanged, because the restore emits
// no bytes and requests no code.
func TestSetupGateDegradeRestoresProbedCapExactlyOnce(t *testing.T) {
	const marker = "wizard transcript line\n"
	cases := []struct {
		name  string
		cap   tui.Cap
		stdin bool
	}{
		{"non-console stdout, console stdin", tui.Cap{Tier: tui.TierPlain, SuppressBanner: true}, true},
		{"non-console stdout, piped stdin", tui.Cap{Tier: tui.TierPlain, SuppressBanner: true}, false},
		{"console stdout, piped stdin", tui.Cap{Tier: tui.Tier256, Width: 120, Height: 40}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := swapSetupSeams(t)
			rec.cap = c.cap
			rec.stdinConsole = c.stdin
			rec.exitCode = -1
			restores := captureSetupRestore(t)

			restoresBeforeWizard := -1
			setupWizardRunner = func(in io.Reader, out io.Writer) error {
				rec.wizardCalled = true
				rec.wizardIn, rec.wizardOut = in, out
				restoresBeforeWizard = restores.calls
				fmt.Fprint(out, marker)
				return nil
			}

			var stderr string
			stdout := captureStdout(t, func() {
				stderr = captureStderr(t, func() { runSetup(nil) })
			})

			if !rec.wizardCalled || rec.tuiCalled {
				t.Fatalf("wizard called = %v, TUI called = %v; want the wizard only",
					rec.wizardCalled, rec.tuiCalled)
			}
			if restores.calls != 1 {
				t.Fatalf("restore calls = %d; want exactly 1", restores.calls)
			}
			if got := restores.caps[0]; got.Tier != c.cap.Tier || got.Width != c.cap.Width ||
				got.Height != c.cap.Height || got.SuppressBanner != c.cap.SuppressBanner {
				t.Errorf("restored cap = %+v; want the probed cap %+v", got, c.cap)
			}
			if restoresBeforeWizard != 1 {
				t.Errorf("restore calls at wizard entry = %d; want 1 (the restore must precede the wizard)",
					restoresBeforeWizard)
			}
			if stdout != marker {
				t.Errorf("degrade transcript = %q; want %q (the restore must emit nothing)", stdout, marker)
			}
			if stderr != "" {
				t.Errorf("degrade path wrote to stderr: %q", stderr)
			}
			if rec.exitCode != -1 {
				t.Errorf("exit code requested = %d; want none", rec.exitCode)
			}
		})
	}
}

// TestSetupGateDoesNotRestoreOnPathsWithoutTheDegrade pins the other half of
// FIX-C: the restore belongs to the degrade branch alone. The TUI branch hands
// the mode back through tui.RunSetup's defer, and the --wizard and unknown
// argument branches never probe the ladder, so nothing is restored there.
func TestSetupGateDoesNotRestoreOnPathsWithoutTheDegrade(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantProbe  int
		wantTUI    bool
		wantWizard bool
	}{
		{"console + console opens the TUI", nil, 1, true, false},
		{"--wizard never probes the ladder", []string{"--wizard"}, 0, false, true},
		{"unknown argument never probes the ladder", []string{"--bogus"}, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := swapSetupSeams(t)
			rec.cap = tui.Cap{Tier: tui.Tier256, Width: 120, Height: 40}
			rec.stdinConsole = true
			rec.exitCode = -1
			restores := captureSetupRestore(t)

			capProbes := 0
			innerCap := setupConsoleCap
			setupConsoleCap = func() tui.Cap {
				capProbes++
				return innerCap()
			}

			captureStdout(t, func() {
				captureStderr(t, func() { runSetup(c.args) })
			})

			if capProbes != c.wantProbe {
				t.Errorf("capability probes = %d; want %d", capProbes, c.wantProbe)
			}
			if restores.calls != 0 {
				t.Errorf("restore calls = %d; want 0 (the TUI owns its defer, the other branches never probed)",
					restores.calls)
			}
			if rec.tuiCalled != c.wantTUI {
				t.Errorf("TUI called = %v; want %v", rec.tuiCalled, c.wantTUI)
			}
			if rec.wizardCalled != c.wantWizard {
				t.Errorf("wizard called = %v; want %v", rec.wizardCalled, c.wantWizard)
			}
			switch {
			case c.wantTUI:
				if rec.exitCode != 0 {
					t.Errorf("exit code = %d; want 0 (the TUI result becomes the exit code)", rec.exitCode)
				}
			case c.wantWizard:
				if rec.exitCode != -1 {
					t.Errorf("exit code requested = %d; want none", rec.exitCode)
				}
			default:
				if rec.exitCode != 2 {
					t.Errorf("exit code = %d; want 2 (the unknown argument keeps its fail-closed code)", rec.exitCode)
				}
			}
		})
	}
}

// TestSetupCapRestoreSeamIsNoOpSafe pins that the production restore seam can
// be handed any cap the gate can produce without doing anything unexpected:
// Cap.Restore is a no-op when the ladder never enabled VT and writes the
// console mode once on repeat calls (both arms are pinned in
// internal/tui/termcap_test.go). This pin covers the seam wire in front of it.
func TestSetupCapRestoreSeamIsNoOpSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("restore seam panicked on a Cap without a hook: %v", r)
		}
	}()
	restoreSetupCap(tui.Cap{})
	restoreSetupCap(tui.Cap{Tier: tui.TierPlain, Width: 80, Height: 24, SuppressBanner: true})
	restoreSetupCap(tui.Cap{Tier: tui.TierTruecolor, Width: 120, Height: 40})
}
