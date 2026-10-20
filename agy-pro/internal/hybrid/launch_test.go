package hybrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/mesh"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/wirecheck"
)

// ---------------------------------------------------------------------------
// helper child process (bounded, headless, never interactive)
// ---------------------------------------------------------------------------

// TestP1HelperChild is the bounded helper the LaunchP1 tests spawn as the "real
// agy". It is a no-op unless GO_P1_HELPER_MODE selects a behavior, so the
// parent's own test run never enters it. Every mode is bounded (a hard
// -test.timeout) and holds no terminal.
func TestP1HelperChild(t *testing.T) {
	switch os.Getenv("GO_P1_HELPER_MODE") {
	case "":
		return
	case "dumpenv":
		if p := os.Getenv("GO_P1_HELPER_ENVFILE"); p != "" {
			var b strings.Builder
			for _, e := range os.Environ() {
				b.WriteString(e)
				b.WriteByte('\n')
			}
			_ = os.WriteFile(p, []byte(b.String()), 0o600)
		}
		os.Exit(0)
	case "exit":
		n, err := strconv.Atoi(os.Getenv("GO_P1_HELPER_EXIT"))
		if err != nil {
			n = 0
		}
		os.Exit(n)
	case "dumpargs":
		// The forwarded argv as JSON, the same dump shape the P2 helper child
		// writes, so both profiles read it through the shared readArgsFile.
		if p := os.Getenv("GO_P1_HELPER_ARGSFILE"); p != "" {
			if data, err := json.Marshal(os.Args); err == nil {
				_ = os.WriteFile(p, data, 0o600)
			}
		}
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "p1 helper: unknown mode %q\n", os.Getenv("GO_P1_HELPER_MODE"))
		os.Exit(2)
	}
}

// selfHelperArgs returns the args that re-invoke this test binary as the helper
// child, running only TestP1HelperChild under a hard timeout.
func selfHelperArgs() []string {
	return []string{"-test.run=^TestP1HelperChild$", "-test.timeout=120s"}
}

// ---------------------------------------------------------------------------
// p1ExitCode mapping
// ---------------------------------------------------------------------------

func TestP1ExitCode(t *testing.T) {
	// nil (child exited 0) -> 0.
	if code, err := p1ExitCode(nil); code != 0 || err != nil {
		t.Errorf("p1ExitCode(nil) = (%d, %v); want (0, nil)", code, err)
	}
	// A real child exit produces an *exec.ExitError whose code passes through.
	cmd := exec.Command(os.Args[0], selfHelperArgs()...)
	cmd.Env = append(os.Environ(), "GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=42")
	if runErr := cmd.Run(); runErr != nil {
		if code, err := p1ExitCode(runErr); code != 42 || err != nil {
			t.Errorf("p1ExitCode(exit 42) = (%d, %v); want (42, nil)", code, err)
		}
	} else {
		t.Fatal("the helper child did not exit non-zero")
	}
	// A mesh that died mid-session maps to ExitMeshDied with a nil error (the
	// mesh already wrote the one visible fail-fast line).
	meshDied := fmt.Errorf("mesh: %w: health probe failed", mesh.ErrMeshDied)
	if code, err := p1ExitCode(meshDied); code != ExitMeshDied || err != nil {
		t.Errorf("p1ExitCode(ErrMeshDied) = (%d, %v); want (%d, nil)", code, err, ExitMeshDied)
	}
	// Any other error maps to exit 1 with the error propagated.
	if code, err := p1ExitCode(errors.New("refuse to launch")); code != 1 || err == nil {
		t.Errorf("p1ExitCode(other) = (%d, %v); want (1, non-nil)", code, err)
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: child env construction
// ---------------------------------------------------------------------------

func TestLaunchP1_ChildEnvConstruction(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goReport())

	var startedMesh *mesh.Mesh
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})

	// Plant provider key VALUES in the parent env; they must NOT reach the child.
	t.Setenv("NW_API_KEY", "sk-planted-nw-secret-value")
	t.Setenv("CHARM_API_KEY", "sk-planted-charm-secret-value")
	// FIX-61: plant the ambient native-credential names AFTER sandboxP1 (which
	// clears them) so the "absent from the child" assertion is non-vacuous: it
	// now proves CleanChildEnv SCRUBS an inherited GEMINI_API_KEY/GOOGLE_API_KEY,
	// not merely that the var was never present.
	t.Setenv("GEMINI_API_KEY", "AIzaPLANTEDambientGEMINI0000000000000000")
	t.Setenv("GOOGLE_API_KEY", "AIzaPLANTEDambientGOOGLE000000000000000")

	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil)", code, err)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}

	childEnv := readEnvFile(t, envFile)
	// CLOUD_CODE_URL points at the mesh's loopback URL.
	if got := childEnv["CLOUD_CODE_URL"]; got != startedMesh.URL() {
		t.Errorf("CLOUD_CODE_URL = %q; want %q", got, startedMesh.URL())
	}
	if !strings.HasPrefix(childEnv["CLOUD_CODE_URL"], "http://127.0.0.1:") {
		t.Errorf("CLOUD_CODE_URL must be a loopback URL: %q", childEnv["CLOUD_CODE_URL"])
	}
	// The recursion guard rides along via CleanChildEnv.
	if childEnv["AGY_PRO_ACTIVE"] != "1" {
		t.Errorf("AGY_PRO_ACTIVE = %q; want 1", childEnv["AGY_PRO_ACTIVE"])
	}
	// No provider key NAMES (hence no VALUES) in the child.
	for _, k := range []string{"NW_API_KEY", "CHARM_API_KEY"} {
		if v, ok := childEnv[k]; ok {
			t.Errorf("provider key %s must not reach the child (got %q)", k, v)
		}
	}
	// FIX-61: the ambient GEMINI_API_KEY / GOOGLE_API_KEY planted in the parent
	// (above) must be SCRUBBED from the child (the keyring bearer is the
	// CodeAssist credential, and neither is injected).
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if v, ok := childEnv[k]; ok {
			t.Errorf("ambient %s must be scrubbed from the child (got %q)", k, v)
		}
	}
	// The planted secret values never transit to any file.
	if hit := treeContains(t, filepath.Dir(envFile), "sk-planted-nw-secret-value"); hit != "" {
		t.Errorf("a provider key value leaked into the sandbox: %s", hit)
	}
	if hit := treeContains(t, filepath.Dir(envFile), "AIzaPLANTEDambientGEMINI"); hit != "" {
		t.Errorf("the ambient GEMINI_API_KEY value leaked into the sandbox: %s", hit)
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: exit-code passthrough
// ---------------------------------------------------------------------------

func TestLaunchP1_ExitCodePassthrough(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=42"},
	})
	if err != nil {
		t.Fatalf("LaunchP1 error: %v", err)
	}
	if code != 42 {
		t.Fatalf("exit code = %d; want 42 (child exit passthrough)", code)
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: mesh death mid-session
// ---------------------------------------------------------------------------

func TestLaunchP1_MeshDeathMapsToExit75(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goReport())

	var capturedCfg mesh.Config
	var startedMesh *mesh.Mesh
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		capturedCfg = cfg
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})
	var visible bytes.Buffer
	swapMeshRunChild(t, func(_ context.Context, m *mesh.Mesh, _ *exec.Cmd) error {
		// Reproduce the mesh's mid-session-death contract: one visible line to
		// the configured ErrWriter, then the ErrMeshDied-wrapped error. The real
		// child-kill + visible line live in mesh.RunChild (proven in
		// mesh_test.go TestRunChild_MeshDeathKillsTheChildWithAVisibleError).
		fmt.Fprintf(capturedCfg.ErrWriter, "mesh: %s (mesh %s, port %d; health probe failed); the supervised child was killed\n",
			mesh.ErrMeshDied.Error(), m.URL(), m.Port())
		return fmt.Errorf("mesh: %w: the health probe on %s (port %d) failed", mesh.ErrMeshDied, m.URL(), m.Port())
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &visible,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=0"},
	})
	if err != nil {
		t.Fatalf("LaunchP1 returned an error for a mesh death (the visible line already covers it): %v", err)
	}
	if code != ExitMeshDied {
		t.Fatalf("exit code = %d; want %d (mesh died mid-session)", code, ExitMeshDied)
	}
	line := visible.String()
	if !strings.Contains(line, "mesh died mid-session") {
		t.Errorf("the visible line must state the mid-session death: %q", line)
	}
	if startedMesh != nil && !strings.Contains(line, fmt.Sprintf("port %d", startedMesh.Port())) {
		t.Errorf("the visible line must name the mesh port %d: %q", startedMesh.Port(), line)
	}
	// The injected stderr is the mesh's ErrWriter.
	if capturedCfg.ErrWriter != io.Writer(&visible) {
		t.Error("the mesh ErrWriter must be the injected stderr")
	}
	// The mesh was stopped (LaunchP1's cleanup).
	if startedMesh != nil {
		if err := startedMesh.Healthz(context.Background()); err == nil {
			t.Error("the mesh must be stopped after a mid-session death")
		}
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: wirecheck NOGO -> native fallback (no CLOUD_CODE_URL)
// ---------------------------------------------------------------------------

func TestLaunchP1_WirecheckNOGOFallsBackToNative(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, nogoReport())

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "NOGO") || !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("the notice must state NOGO + native fallback: %q", notice.String())
	}
	// The native-fallback child has NO CLOUD_CODE_URL (no mesh was started).
	childEnv := readEnvFile(t, envFile)
	if _, ok := childEnv["CLOUD_CODE_URL"]; ok {
		t.Errorf("the native fallback must not set CLOUD_CODE_URL: %q", childEnv["CLOUD_CODE_URL"])
	}
	if childEnv["AGY_PRO_ACTIVE"] != "1" {
		t.Errorf("AGY_PRO_ACTIVE = %q; want 1 even on the fallback path", childEnv["AGY_PRO_ACTIVE"])
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: zero enabled externals -> native fallback
// ---------------------------------------------------------------------------

func TestLaunchP1_ZeroExternalsFallsBackToNative(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, emptyRegistryJSON)
	swapWirecheck(t, goReport())

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "nothing to serve") || !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("the notice must state no externals + native fallback: %q", notice.String())
	}
	if _, ok := readEnvFile(t, envFile)["CLOUD_CODE_URL"]; ok {
		t.Error("the zero-externals fallback must not set CLOUD_CODE_URL")
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: version drift with GO canaries proceeds (drift surfaced)
// ---------------------------------------------------------------------------

func TestLaunchP1_VersionDriftProceedsWithNotice(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	r := goReport()
	r.Version = "9.9.9"
	r.VersionDrift = true
	swapWirecheck(t, r)

	var notice bytes.Buffer
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=0"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) - drift with GO canaries must proceed", code, err)
	}
	if !strings.Contains(notice.String(), "version drift") {
		t.Errorf("the drift must be surfaced in the notice: %q", notice.String())
	}
	if strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("drift with GO canaries must NOT fall back to native: %q", notice.String())
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: no settings file is written anywhere
// ---------------------------------------------------------------------------

func TestLaunchP1_NoSettingsFileWritten(t *testing.T) {
	agyHome, userHome := sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=0"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil)", code, err)
	}
	for _, root := range []string{agyHome, userHome} {
		if hit := findFile(t, root, "settings.json"); hit != "" {
			t.Errorf("the P1 launch path must not write a settings file: %s", hit)
		}
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: sandbox isolation (the real user home is never touched)
// ---------------------------------------------------------------------------

func TestLaunchP1_SandboxCanaryUntouched(t *testing.T) {
	_, userHome := sandboxP1(t)
	// A canary in the fake real home: if anything resolves paths through the
	// user profile instead of AGY_PRO_HOME, the canary directory gains files.
	canaryDir := filepath.Join(userHome, ".agy-pro")
	if err := os.MkdirAll(canaryDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	canary := filepath.Join(canaryDir, "CANARY")
	if err := os.WriteFile(canary, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=0"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil)", code, err)
	}

	entries, err := os.ReadDir(canaryDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "CANARY" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the fake real home was touched: %v", names)
	}
	if data, _ := os.ReadFile(canary); string(data) != "untouched" {
		t.Fatalf("the canary was modified: %q", data)
	}
	if hit := treeContains(t, userHome, "wireadp.json"); hit != "" {
		t.Fatalf("mesh state leaked into the user home: %s", hit)
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: real agy resolution + real wirecheck against a canary-missing stub
// ---------------------------------------------------------------------------

// TestLaunchP1_RealStubMissingCanariesGatesToNative plants a real, canary-missing
// stub "agy" via AGY_PRO_REAL_AGY and lets LaunchP1 resolve it and run the REAL
// wirecheck (no seam): the stub is missing every P1 canary, so the verdict is
// NOGO and the session falls back to native (the stub is exec'd with no
// CLOUD_CODE_URL).
func TestLaunchP1_RealStubMissingCanariesGatesToNative(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	stub := buildP1Stub(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)

	// Prove the stub really is NOGO for P1 before relying on it.
	if wirecheck.Check().ProfileOK(ProfileP1.WirecheckName()) {
		t.Fatal("the stub must be missing the P1 canaries for this test to be meaningful")
	}

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	// RealAgyPath empty: LaunchP1 resolves the real agy via realagy.Resolve()
	// (AGY_PRO_REAL_AGY), the real wirecheck scans it (NOGO), and the session
	// falls back to native.
	code, err := LaunchP1(LaunchOptions{
		Args:     nil,
		Stderr:   &notice,
		ChildEnv: []string{"P1_STUB_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "NOGO") {
		t.Errorf("the notice must state the NOGO verdict from the real wirecheck: %q", notice.String())
	}
	if !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("the notice must state the native fallback: %q", notice.String())
	}
	if _, ok := readEnvFile(t, envFile)["CLOUD_CODE_URL"]; ok {
		t.Error("the native fallback must not set CLOUD_CODE_URL")
	}
}

// buildP1Stub compiles a tiny canary-missing helper binary (a distinct file from
// the test binary, so realagy.Resolve accepts it via AGY_PRO_REAL_AGY without a
// recursion refusal) and returns its path. It dumps its environment to
// P1_STUB_ENVFILE when set, so the fallback child's env can be asserted.
func buildP1Stub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const stubSrc = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if p := os.Getenv("P1_STUB_ENVFILE"); p != "" {
		var b strings.Builder
		for _, e := range os.Environ() {
			b.WriteString(e)
			b.WriteByte('\n')
		}
		_ = os.WriteFile(p, []byte(b.String()), 0o600)
	}
	fmt.Fprintln(os.Stderr, "stub agy")
}
`
	if err := os.WriteFile(src, []byte(stubSrc), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	out := filepath.Join(dir, "agy")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	build := exec.Command("go", "build", "-o", out, src)
	build.Env = append(os.Environ(), "GO111MODULE=off")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, b)
	}
	return out
}

// ---------------------------------------------------------------------------
// LaunchP1: real-agy resolution failure is surfaced (never silent)
// ---------------------------------------------------------------------------

func TestLaunchP1_ResolveFailureIsSurfaced(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goReport())
	swapRealagy(t, "", errors.New("real agy binary not found"))

	// RealAgyPath empty forces the realagy.Resolve() path, which the seam fails.
	code, err := LaunchP1(LaunchOptions{Args: nil})
	if err == nil {
		t.Fatal("a resolution failure must be surfaced, not swallowed")
	}
	if code != 1 {
		t.Errorf("exit code = %d; want 1 on a resolution failure", code)
	}
}

// FIX-47: on a registry-read failure LaunchP1 falls back with reg=nil. The
// fallback must still scrub the compiled-in known-preset env names (independent
// of providers.json), so a planted known-preset key VALUE never reaches the
// native child.
func TestLaunchP1_RegistryErrorFallbackScrubsKnownPresetKeys(t *testing.T) {
	sandboxP1(t)
	// A providers path that does not exist -> providers.Load fails -> reg=nil.
	t.Setenv(providers.EnvProviders, filepath.Join(t.TempDir(), "missing.json"))
	swapWirecheck(t, goReport())
	t.Setenv("OPENAI_API_KEY", "sk-planted-openai-secret")

	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	childEnv := readEnvFile(t, envFile)
	if _, ok := childEnv["OPENAI_API_KEY"]; ok {
		t.Errorf("the reg=nil fallback must scrub the known-preset key (FIX-47); got %q", childEnv["OPENAI_API_KEY"])
	}
	if hit := treeContains(t, filepath.Dir(envFile), "sk-planted-openai-secret"); hit != "" {
		t.Errorf("the planted known-preset key leaked into the sandbox: %s", hit)
	}
}

// FIX-48: a registry whose enabled id collides with a static Gemini id fails the
// catalog build. LaunchP1 must catch that at launch (a clear notice + native
// fallback), never start a live mesh whose catalog would 500 mid-session.
func TestLaunchP1_DenyListCollisionFallsBackNative(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, `{"providers":[{"name":"prov","baseURL":"http://127.0.0.1:9/v1","models":[{"id":"gemini-3.8-flash-high","contextLength":300000}]}]}`)
	swapWirecheck(t, goReport())

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "catalog could not be built") {
		t.Errorf("the deny-list collision must print a catalog-build notice: %q", notice.String())
	}
	if _, ok := readEnvFile(t, envFile)["CLOUD_CODE_URL"]; ok {
		t.Error("the deny-list collision must NOT start a mesh (no CLOUD_CODE_URL)")
	}
}

// FIX-50: the cross-provider duplicate-id drop must be OBSERVABLE in production.
// The production catalog options wire a masked, control-stripped stderr sink, so
// a duplicate-id registry surfaces the drop reason on the injected stderr.
func TestLaunchP1_DuplicateIDDropReasonReachesSink(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, `{"providers":[
		{"name":"first","baseURL":"http://127.0.0.1:9/v1","models":[{"id":"shared","contextLength":300000}]},
		{"name":"second","baseURL":"http://127.0.0.1:9/v1","models":[{"id":"shared","contextLength":300000}]}
	]}`)
	swapWirecheck(t, goReport())

	var sink bytes.Buffer
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &sink,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=0"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil)", code, err)
	}
	if !strings.Contains(sink.String(), "catalog:") || !strings.Contains(sink.String(), "dropped") {
		t.Errorf("the cross-provider duplicate drop must reach the production sink (FIX-50): %q", sink.String())
	}
}

// FIX-55: LaunchP1 must resolve the real agy ONCE and use the SAME path for the
// wirecheck gate and the exec (no TOCTOU). A resolve seam that returns DIFFERENT
// paths on successive calls must not cause the gate to scan one binary and the
// exec to run another.
func TestLaunchP1_ResolvesOnceGateAndExecAgree(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	prevResolve := realagyResolve
	realagyResolve = func() (string, error) {
		calls++
		if calls == 1 {
			return self, nil
		}
		return filepath.Join(t.TempDir(), "other-agy"), nil
	}
	t.Cleanup(func() { realagyResolve = prevResolve })

	var gatedPath string
	prevCheck := wirecheckCheck
	wirecheckCheck = func(pinned string) wirecheck.Report {
		gatedPath = pinned
		return goReport()
	}
	t.Cleanup(func() { wirecheckCheck = prevCheck })

	envFile := filepath.Join(t.TempDir(), "child.env")
	code, err := LaunchP1(LaunchOptions{
		// RealAgyPath empty forces the resolve-once path.
		Args:     selfHelperArgs(),
		ChildEnv: []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil)", code, err)
	}
	if calls != 1 {
		t.Errorf("realagyResolve called %d times; want 1 (resolve ONCE, FIX-55)", calls)
	}
	if gatedPath != self {
		t.Errorf("the gate scanned %q; want the resolved path %q (same binary as the exec, FIX-55)", gatedPath, self)
	}
	if _, ok := readEnvFile(t, envFile)["CLOUD_CODE_URL"]; !ok {
		t.Error("the exec should have run the resolved binary (CLOUD_CODE_URL present in its env)")
	}
}

// R5AF2-4: LaunchP1's MESH-path child env must use ChildKeyScrubNames (the
// registry names UNION the known presets), not the raw ApiKeyEnvNames, so an
// exported preset key for a provider absent from the registry cannot ride into
// the P1 mesh child (S-3 class; FIX-4 parity with P2's mesh path).
func TestLaunchP1_ReadableRegistryStillScrubsPresetKeys(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON) // readable; no openai provider
	swapWirecheck(t, goReport())

	var startedMesh *mesh.Mesh
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-planted-openai-secret-value")
	envFile := filepath.Join(t.TempDir(), "child.env")
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil)", code, err)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}
	if v, ok := readEnvFile(t, envFile)["OPENAI_API_KEY"]; ok {
		t.Errorf("the P1 mesh child must scrub the known-preset key OPENAI_API_KEY (R5AF2-4); got %q", v)
	}
	if hit := treeContains(t, filepath.Dir(envFile), "sk-planted-openai-secret-value"); hit != "" {
		t.Errorf("the planted preset key leaked into the sandbox: %s", hit)
	}
}

// R5AF2-5 (P1 twin): nogoNotice renders reasons through the shared
// renderWirecheckReasons, so a ScanErr that embeds the real-agy absolute path is
// withheld, not interpolated.
func TestNogoNotice_ScanErrPathWithheld(t *testing.T) {
	report := wirecheck.Report{
		BinaryPath: filepath.Join("fake", "agy.exe"),
		ScanErr:    `read C:\Users\someuser\.agy-pro\bin\agy.exe: Access is denied`,
		VersionErr: "probe timed out",
	}
	if report.Verdict() != wirecheck.VerdictNOGO {
		t.Fatal("the fixture must be NOGO for the reasons to render")
	}
	notice := nogoNotice(report)
	if strings.Contains(notice, `C:\Users\`) {
		t.Errorf("the P1 NOGO notice must not echo the real-agy path from a ScanErr: %q", notice)
	}
	if !strings.Contains(notice, "withheld") {
		t.Errorf("the P1 NOGO notice must state a path-bearing reason was withheld: %q", notice)
	}
}

// R5AF3-2: noticePathShapeRe must also catch a backslash UNC path
// ("\\server\share\...", "\\?\UNC\..."), which carries no drive letter and no
// forward slash, so such a ScanErr reason is withheld too.
func TestRenderWirecheckReasons_WithholdsUNCPath(t *testing.T) {
	for _, unc := range []string{
		`read \\fileserver\tools\agy.exe: Access is denied`,
		`open \\?\UNC\fileserver\tools\agy.exe: The network path was not found`,
	} {
		t.Run(unc, func(t *testing.T) {
			out := renderWirecheckReasons([]string{unc})
			if strings.Contains(out, `\\fileserver\`) {
				t.Errorf("a UNC path reason must be withheld, not echoed: %q", out)
			}
			if !strings.Contains(out, "withheld") {
				t.Errorf("the notice must state a path-bearing reason was withheld: %q", out)
			}
		})
	}
}

// R5AF3-4: pin the false-positive (kept) direction of renderWirecheckReasons:
// legitimate reasons (blade: keys, gemini-api: URLs, single-segment route
// literals, mesh URLs, canary/version/drift strings) must survive verbatim, so a
// future regex tightening cannot silently swallow a real diagnostic.
func TestRenderWirecheckReasons_KeepsNonPathReasons(t *testing.T) {
	kept := []string{
		`blade:127.0.0.1:4444/models/ext-nw`,
		`gemini-api://127.0.0.1:4444/v1beta`,
		`/v1internal:fetchUserInfo`,
		`CLOUD_CODE_URL=http://127.0.0.1:8080/v1beta/models/gpt-4o:streamGenerateContent`,
		`profile P2 incomplete: missing scheme_blade ("blade:")`,
		`version drift: binary reports "1.3.4", pinned "1.3.5"`,
	}
	out := renderWirecheckReasons(kept)
	if strings.Contains(out, "withheld") {
		t.Fatalf("no legitimate reason should be withheld (none is path-shaped): %q", out)
	}
	for _, r := range kept {
		if !strings.Contains(out, r) {
			t.Errorf("legitimate reason must survive verbatim: %q\nrendered: %q", r, out)
		}
	}
}

// R5AF3-5: P1's launchNativeFallback notice is masked + control-stripped +
// path-withheld through the shared renderFallbackNotice (parity with P2's
// p2NativeFallback). A P1 fallback triggered with a path-bearing registry-read
// error must emit no C:\Users\ substring.
func TestLaunchP1_NativeFallbackNoticeWithholdsPath(t *testing.T) {
	_, userHome := sandboxP1(t)
	// A registry path under the fake user home that fails to read, so the
	// registry-load error (which embeds the absolute path) feeds the P1 fallback
	// notice.
	t.Setenv(providers.EnvProviders, filepath.Join(userHome, "missing-providers.json"))
	swapWirecheck(t, goReport())

	var notice bytes.Buffer
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "child.env")
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        selfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpenv", "GO_P1_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	out := notice.String()
	if strings.Contains(out, userHome) {
		t.Errorf("the P1 fallback notice must not echo the user home path: %q", out)
	}
	if strings.Contains(out, `C:\Users\`) {
		t.Errorf("the P1 fallback notice must not echo a C:\\Users\\ path: %q", out)
	}
	for _, r := range out {
		if r != '\n' && r != '\t' && r < 0x20 {
			t.Errorf("the P1 fallback notice must not carry control bytes: %q", out)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// LaunchP1: the native fallback strips a mesh-specific --model pin (R5B-FIX-2
// parity with P2's p2NativeFallback)
// ---------------------------------------------------------------------------

// p1SelfHelperArgsModel is selfHelperArgs with a --model pin inserted BEFORE a
// trailing "--" terminator, mirroring p2SelfHelperArgsModel: the test binary
// forwards the pin to the helper's os.Args instead of rejecting it as an unknown
// test flag, and the pin sits before the terminator because a post-terminator
// token is not a selector (R5AF3-1).
func p1SelfHelperArgsModel(model string) []string {
	return []string{"-test.run=^TestP1HelperChild$", "-test.timeout=120s", "--model", model, "--"}
}

// P1's fallback used to forward opts.Args verbatim, so on a wirecheck-NOGO
// fallback a pinned mesh-catalog id (--model ext-nw) reached native agy, which
// has no mesh and no catalog to answer it, and the session died at agy's own
// --model gate with exit 1. The P2 twin already stripped the pin and disclosed
// the drop; P1 now shares the same seam (nativeFallbackArgs /
// nativeFallbackDroppedPin), so the two twins cannot drift.
func TestLaunchP1_WirecheckNOGOFallbackStripsMeshPin(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  string
		// want is the bare --model value the native child must receive; "" means
		// the pin is stripped and no model selector reaches the child at all.
		want string
	}{
		{"pinned external id is stripped", "ext-nw", ""},
		{"a blade scheme key is stripped", "blade:127.0.0.1:4444/models/ext-nw", ""},
		{"a provider-qualified spelling is stripped", "ext-nw (provnw)", ""},
		// R5BF2-2 parity: a static id native agy CAN resolve is kept, in the bare
		// spelling agy's --model gate accepts, and re-pinned FIRST.
		{"a static id is kept in its bare spelling", "nw/gemini-3.8-flash-high", "gemini-3.8-flash-high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxP1(t)
			writeRegistry(t, twoExtRegistryJSON)
			swapWirecheck(t, nogoReport())

			var notice bytes.Buffer
			argsFile := filepath.Join(t.TempDir(), "child.args")
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			code, err := LaunchP1(LaunchOptions{
				RealAgyPath: self,
				Args:        p1SelfHelperArgsModel(tc.pin),
				Stderr:      &notice,
				ChildEnv:    []string{"GO_P1_HELPER_MODE=dumpargs", "GO_P1_HELPER_ARGSFILE=" + argsFile},
			})
			if err != nil || code != 0 {
				t.Fatalf("LaunchP1 = (%d, %v); want (0, nil) after the native fallback", code, err)
			}
			got := readArgsFile(t, argsFile)
			line := notice.String()
			if tc.want == "" {
				if hasArg(got, "--model") || hasArg(got, "-m") {
					t.Errorf("the native fallback child still carries a model pin: %v", got)
				}
				for _, a := range got {
					if a == tc.pin {
						t.Errorf("the mesh-catalog pin value reached the native child: %v", got)
					}
				}
				// The caller's other tokens (the helper flags and the terminator)
				// survive the strip: this is "the caller's args minus the pin".
				joined := strings.Join(got, " ")
				if !strings.Contains(joined, "-test.timeout=120s") || got[len(got)-1] != "--" {
					t.Errorf("the caller's other args must survive the strip: %v", got)
				}
				// The drop is disclosed, never silent, and it names the pin.
				if !strings.Contains(line, "not resolvable on this native fallback") {
					t.Errorf("the stripped pin must be disclosed: %q", line)
				}
				if !strings.Contains(line, tc.pin) {
					t.Errorf("the disclosure must name the stripped pin %q: %q", tc.pin, line)
				}
				return
			}
			if v := argValue(got, "--model"); v != tc.want {
				t.Errorf("--model = %q; want the bare static id %q", v, tc.want)
			}
			if len(got) < 2 || got[1] != "--model" {
				t.Errorf("the kept pin must be re-pinned FIRST (before every caller token): %v", got)
			}
			if got[len(got)-1] != "--" {
				t.Errorf("the caller's terminator must survive: %v", got)
			}
			if strings.Contains(line, "not resolvable on this native fallback") {
				t.Errorf("a KEPT static pin must not be disclosed as dropped: %q", line)
			}
		})
	}
}

// The fallback's exit code is still the CHILD's: stripping the pin must not
// change the exit-code contract (the child's own code passes through).
func TestLaunchP1_NativeFallbackExitCodeIsTheChilds(t *testing.T) {
	sandboxP1(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, nogoReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP1(LaunchOptions{
		RealAgyPath: self,
		Args:        []string{"--model", "ext-nw"},
		Stderr:      &bytes.Buffer{},
		ChildEnv:    []string{"GO_P1_HELPER_MODE=exit", "GO_P1_HELPER_EXIT=7"},
	})
	if err != nil {
		t.Fatalf("LaunchP1 error: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d; want 7 (the child's own exit, unchanged by the pin strip)", code)
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep, hybrid surface): the dropped-pin disclosure
// names a registry-borne pin (a registry model id, a scheme key, a blade slug),
// so it renders through providers.MaskSlot, the registry-slot authority.
// MaskOperand is that authority's first stage, so every benign pin keeps its
// exact bytes; the underscore-head credential class that MaskOperand renders
// raw masks here.
// ---------------------------------------------------------------------------

func TestPrintDroppedPinNoticeMasksRegistrySlot(t *testing.T) {
	const (
		plant     = "sk_live_0123456789abcdef"
		plantMask = "sk_li****"
	)
	line := func(v string) string {
		return "[agy-pro] the pinned model " + v + " is not resolvable on this native fallback (only agy's static Gemini ids are); the session runs agy's default native model instead\n"
	}

	// The plant: masked, never raw.
	var buf bytes.Buffer
	printDroppedPinNotice(&buf, plant)
	if strings.Contains(buf.String(), plant) {
		t.Errorf("dropped-pin notice leaks the credential-shaped pin:\n%s", buf.String())
	}
	if want := line(plantMask); buf.String() != want {
		t.Errorf("dropped-pin notice drifted\n got: %q\nwant: %q", buf.String(), want)
	}

	// Benign pins (registry ids, a scheme key, a blade slug) stay byte-identical.
	for _, benign := range []string{
		"claude-opus-5-5-high", "gpt-oss-120b", "gemini-3.8-flash-high",
		"nw:glm-5.3-flash", "blade:review", "openai/gpt-5.1",
	} {
		buf.Reset()
		printDroppedPinNotice(&buf, benign)
		if want := line(benign); buf.String() != want {
			t.Errorf("benign pin %q drifted\n got: %q\nwant: %q", benign, buf.String(), want)
		}
	}
}
