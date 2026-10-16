package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/hybrid"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/wirecheck"
)

// fakeHybridReport is the injected wirecheck report for the command tests:
// P1 is GO (its canary is found), P2 is NOGO (its canary is missing). The
// production seam scans the real agy binary, which may not resolve in a sandbox.
func fakeHybridReport() wirecheck.Report {
	return wirecheck.Report{Canaries: []wirecheck.Canary{
		{Profile: wirecheck.ProfileP1, Name: "c1", Literal: "L1", Found: true},
		{Profile: wirecheck.ProfileP2, Name: "c2", Literal: "L2", Found: false},
	}}
}

// sandboxHybrid points every state-touching path at a temp dir and clears the
// env override, then installs the fake wirecheck seam. The real ~/.agy-pro and
// ~/.gemini are never read or written.
func sandboxHybrid(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tmp)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tmp, "cfg"))
	t.Setenv("USERPROFILE", tmp)
	t.Setenv(hybrid.EnvHybridProfile, "")

	old := wirecheckCheck
	wirecheckCheck = fakeHybridReport
	t.Cleanup(func() { wirecheckCheck = old })
}

func TestRunHybridCmd_ArgTable(t *testing.T) {
	sandboxHybrid(t)

	// A single shared state file drives the sequence: on p1 -> status/check read
	// p1 (GO) -> on p2 -> check reads p2 (NOGO) -> off -> check reads none.
	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"no args", nil, 2},
		{"on without profile", []string{"on"}, 2},
		{"on garbage", []string{"on", "garbage"}, 2},
		{"on off rejected", []string{"on", "off"}, 2},
		{"on extra args", []string{"on", "p1", "extra"}, 2},
		{"bogus subcommand", []string{"bogus"}, 2},
		{"on p1", []string{"on", "p1"}, 0},
		{"status p1", []string{"status"}, 0},
		{"check p1 GO", []string{"check"}, 0},
		{"on p2", []string{"on", "p2"}, 0},
		{"check p2 NOGO", []string{"check"}, 1},
		{"off", []string{"off"}, 0},
		{"check none", []string{"check"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errw bytes.Buffer
			code := runHybridCmdTo(tc.args, &out, &errw)
			if code != tc.wantCode {
				t.Fatalf("args %v: exit = %d; want %d\nstdout:\n%s\nstderr:\n%s", tc.args, code, tc.wantCode, out.String(), errw.String())
			}
		})
	}
}

func TestRunHybridCmd_PersistsAndClears(t *testing.T) {
	sandboxHybrid(t)

	// on p1 persists "p1".
	if code := runHybridCmdTo([]string{"on", "p1"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("on p1 exit = %d; want 0", code)
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.HybridProfile != "p1" {
		t.Fatalf("persisted HybridProfile = %q; want p1", st.HybridProfile)
	}
	if st.EffectiveHybridProfile() != "p1" {
		t.Fatalf("EffectiveHybridProfile = %q; want p1", st.EffectiveHybridProfile())
	}

	// off clears it.
	if code := runHybridCmdTo([]string{"off"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("off exit = %d; want 0", code)
	}
	st, err = mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.HybridProfile != "" {
		t.Fatalf("HybridProfile after off = %q; want empty", st.HybridProfile)
	}
}

func TestRunHybridCmd_OnPrintsCostLine(t *testing.T) {
	sandboxHybrid(t)

	var out bytes.Buffer
	if code := runHybridCmdTo([]string{"on", "p1"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("on p1 exit = %d; want 0", code)
	}
	got := out.String()
	if !strings.Contains(got, "[SUCCESS]") {
		t.Fatalf("on p1 output missing success banner:\n%s", got)
	}
	if !strings.Contains(got, hybrid.ProfileP1.Description()) {
		t.Fatalf("on p1 output missing the honest cost line:\n%s", got)
	}
	if !strings.Contains(got, "CLOUD_CODE_URL") {
		t.Fatalf("on p1 launch instruction must name CLOUD_CODE_URL:\n%s", got)
	}
}

func TestRunHybridCmd_CheckEnvOverrideDrivesExitCode(t *testing.T) {
	sandboxHybrid(t)

	// Persist p1 (GO under the fake report).
	if code := runHybridCmdTo([]string{"on", "p1"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("on p1 exit = %d; want 0", code)
	}
	// The env override to p2 (NOGO) must drive the check exit code to 1 even
	// though the persisted profile is the GO profile p1.
	t.Setenv(hybrid.EnvHybridProfile, "p2")
	if code := runHybridCmdTo([]string{"check"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatalf("check with env override p2 (NOGO): exit = %d; want 1", code)
	}
	// status reflects the env override as the effective profile.
	var out bytes.Buffer
	if code := runHybridCmdTo([]string{"status"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("status exit = %d; want 0", code)
	}
	if !strings.Contains(out.String(), hybrid.EnvHybridProfile+"=p2") {
		t.Fatalf("status must surface the env override:\n%s", out.String())
	}
}

func TestRunHybridCmd_BadArgDoesNotLeakPaste(t *testing.T) {
	sandboxHybrid(t)

	// A misdirected key paste in the profile slot must not print raw.
	const pasted = "sk-pasted-into-the-profile-slot-42"
	var errw bytes.Buffer
	if code := runHybridCmdTo([]string{"on", pasted}, &bytes.Buffer{}, &errw); code != 2 {
		t.Fatalf("on <pasted> exit = %d; want 2", code)
	}
	if strings.Contains(errw.String(), pasted) {
		t.Fatalf("profile-slot error echoed the raw pasted value:\n%s", errw.String())
	}
}

// FIX-52: `check` must not fail open when the state read fails. With a corrupt
// state file AND AGY_PRO_HYBRID=p2 (NOGO under the fake report), check must exit
// 1 (fail closed), honoring the env override rather than treating the unreadable
// state as "no profile".
func TestRunHybridCmd_CheckStateErrorHonorsEnvOverride(t *testing.T) {
	sandboxHybrid(t)

	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatalf("AgyProStateFile: %v", err)
	}
	if err := os.WriteFile(stateFile, []byte("{ not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	t.Setenv(hybrid.EnvHybridProfile, "p2")

	var errw bytes.Buffer
	if code := runHybridCmdTo([]string{"check"}, &bytes.Buffer{}, &errw); code != 1 {
		t.Fatalf("check with corrupt state + env override p2 (NOGO): exit = %d; want 1 (fail closed)\nstderr:\n%s", code, errw.String())
	}
}

// FIX-52: off/status/check take no operands; a junk operand is a usage error
// (exit 2, the same convention `on` uses).
func TestRunHybridCmd_OffStatusCheckRejectExtraArgs(t *testing.T) {
	sandboxHybrid(t)
	for _, sub := range []string{"off", "status", "check"} {
		var errw bytes.Buffer
		code := runHybridCmdTo([]string{sub, "junk"}, &bytes.Buffer{}, &errw)
		if code != 2 {
			t.Errorf("%s junk: exit = %d; want 2 (usage)", sub, code)
		}
		if !strings.Contains(errw.String(), "takes no arguments") {
			t.Errorf("%s junk: stderr must state the usage error:\n%s", sub, errw.String())
		}
	}
}

// FIX-63: `agy pro hybrid on` pre-validates the catalog. A registry whose id
// collides with a static Gemini id (a deny-list collision) makes `on p1` refuse
// (exit non-zero) and the profile must NOT be persisted, so the user fixes
// providers.json instead of hitting a launch-time native fallback.
func TestRunHybridCmd_OnPrevalidatesCatalog(t *testing.T) {
	sandboxHybrid(t)
	home, err := paths.AgyProHome()
	if err != nil {
		t.Fatalf("AgyProHome: %v", err)
	}
	colliding := `{"providers":[{"name":"prov","baseURL":"http://127.0.0.1:9/v1","models":[{"id":"gemini-3.8-flash-high","contextLength":300000}]}]}`
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(colliding), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}

	var errw bytes.Buffer
	if code := runHybridCmdTo([]string{"on", "p1"}, &bytes.Buffer{}, &errw); code == 0 {
		t.Fatalf("on p1 with a deny-list-colliding registry must refuse (exit non-zero)\nstderr:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "catalog could not be built") {
		t.Errorf("the refusal must name the catalog build failure:\n%s", errw.String())
	}
	// The profile must NOT be persisted on a collision.
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.HybridProfile != "" {
		t.Errorf("the profile must NOT be persisted on a collision; got %q", st.HybridProfile)
	}
}

// FIX-63 profile-aware (false-refusal direction): the opt-in gate must validate
// the artifact the CHOSEN profile uses. P1's deny-list carries every
// known-preset provider name and alias, which is strictly wider than P2's
// reserved set, so a registry whose id is "openai" made `on p2` refuse with the
// P1 catalog's collision message even though LaunchP2 serves that id happily as
// a blade: customModels entry. `on p2` must now pass (and persist), while `on p1`
// still refuses on the same registry.
func TestRunHybridCmd_OnP2IgnoresTheP1DenyList(t *testing.T) {
	sandboxHybrid(t)
	home, err := paths.AgyProHome()
	if err != nil {
		t.Fatalf("AgyProHome: %v", err)
	}
	// "openai" is a known-preset provider NAME: denied by the P1 catalog's
	// deny-list, and NOT in P2's reserved set (static Gemini ids + AI-Studio
	// preset names only).
	registry := `{"providers":[{"name":"provopenai","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"OPENAI_API_KEY","models":[{"id":"openai","contextLength":300000}]}]}`
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(registry), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}

	// The P1 twin still refuses (unchanged behavior).
	var errw bytes.Buffer
	if code := runHybridCmdTo([]string{"on", "p1"}, &bytes.Buffer{}, &errw); code == 0 {
		t.Fatalf("on p1 with a deny-list-colliding registry must refuse\nstderr:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "catalog could not be built") {
		t.Errorf("the p1 refusal must still name the catalog build failure:\n%s", errw.String())
	}

	// on p2 must PASS and persist: the id is servable as a customModels entry.
	var out, errw2 bytes.Buffer
	if code := runHybridCmdTo([]string{"on", "p2"}, &out, &errw2); code != 0 {
		t.Fatalf("on p2 with a P2-servable registry must pass; exit = %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errw2.String())
	}
	if !strings.Contains(out.String(), "[SUCCESS]") {
		t.Errorf("on p2 output missing the success banner:\n%s", out.String())
	}
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.HybridProfile != "p2" {
		t.Errorf("persisted HybridProfile = %q; want p2", st.HybridProfile)
	}
}

// FIX-63 profile-aware (missed-refusal direction): `on p2` must validate the
// artifact P2 really writes. A whitespace-bearing model id passes registry
// validation and p2Externals' drop policy, produces a blade: key
// agysettings refuses, and used to sail through `on p2` only to exit 1 on every
// invocation afterwards (no native fallback on that path). The check must refuse
// with the composer's own reason and must NOT persist the profile.
func TestRunHybridCmd_OnP2RefusesUnkeyableRegistry(t *testing.T) {
	sandboxHybrid(t)
	home, err := paths.AgyProHome()
	if err != nil {
		t.Fatalf("AgyProHome: %v", err)
	}
	registry := `{"providers":[{"name":"provspace","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"SPACE_KEY","models":[{"id":"gpt 4o","contextLength":300000}]}]}`
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(registry), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}

	var errw bytes.Buffer
	if code := runHybridCmdTo([]string{"on", "p2"}, &bytes.Buffer{}, &errw); code == 0 {
		t.Fatalf("on p2 with a whitespace-bearing id must refuse (exit non-zero)\nstderr:\n%s", errw.String())
	}
	msg := errw.String()
	if !strings.Contains(msg, "P2 session settings could not be composed") {
		t.Errorf("the refusal must name the P2 artifact it could not compose:\n%s", msg)
	}
	if !strings.Contains(msg, "whitespace") {
		t.Errorf("the refusal must carry the composer's reason:\n%s", msg)
	}
	// Fail closed on persistence: the profile is not saved.
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.HybridProfile != "" {
		t.Errorf("the profile must NOT be persisted on a refused composition; got %q", st.HybridProfile)
	}
}

// FIX-63 profile-aware: an unreadable/missing registry is not a refusal for
// either profile (the launch-time gates own that case), so `on` still persists.
func TestRunHybridCmd_OnMissingRegistryStillPersists(t *testing.T) {
	sandboxHybrid(t)
	for _, profile := range []string{"p1", "p2"} {
		var out, errw bytes.Buffer
		if code := runHybridCmdTo([]string{"on", profile}, &out, &errw); code != 0 {
			t.Fatalf("on %s with no providers.json must still persist; exit = %d\nstderr:\n%s", profile, code, errw.String())
		}
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		if st.HybridProfile != profile {
			t.Errorf("persisted HybridProfile = %q; want %q", st.HybridProfile, profile)
		}
		if code := runHybridCmdTo([]string{"off"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
			t.Fatalf("off: exit = %d", code)
		}
	}
}
