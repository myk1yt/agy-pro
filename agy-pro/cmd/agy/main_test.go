package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
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
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
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
