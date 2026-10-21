package hybrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"agypro/internal/agysettings"
	"agypro/internal/mesh"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
	"agypro/internal/wirecheck"
)

func init() {
	// The LaunchP2 tests re-invoke THIS test binary as the stand-in "real agy"
	// (see p2SelfHelperArgs). Real agy defines --app_data_dir and --model, and
	// FIX-2 puts them FIRST in the child argv, so the test binary's own flag
	// parser must accept them instead of exiting 2 on an unknown flag. They are
	// no-ops here: the helper child reads the raw os.Args dump, not these vars.
	flag.String("app_data_dir", "", "stand-in for the real agy --app_data_dir flag (test helper child only)")
	flag.String("model", "", "stand-in for the real agy --model flag (test helper child only)")
}

// ---------------------------------------------------------------------------
// shared sandbox + fixtures
// ---------------------------------------------------------------------------

// p2RegistryJSON is a deterministic three-provider registry: two enabled
// externals referenced by env-var NAMES only (so the child env scrub can be
// asserted), one disabled model, and one reserved static-id model (served, if
// at all, through the static-id policy rather than customModels).
const p2RegistryJSON = `{"providers":[
  {"name":"provnw","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_API_KEY","models":[
    {"id":"ext-nw","contextLength":300000},
    {"id":"ext-off","enabled":false,"contextLength":300000}
  ]},
  {"name":"provcharm","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"CHARM_API_KEY","models":[{"id":"ext-charm","contextLength":300000}]},
  {"name":"provstatic","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"gemini-3.8-flash-high","contextLength":300000}]}
]}`

// visionRegistryJSON carries one external with vision capabilities, so the
// modelFeatures mapping can be asserted.
const visionRegistryJSON = `{"providers":[{"name":"provvision","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"VISION_API_KEY","models":[{"id":"ext-vision","contextLength":128000,"capabilities":{"supportsVision":true}}]}]}`

// staticOnlyRegistryJSON has exactly one enabled model and its id is one of the
// 11 static Gemini defaults: the customModels document must NOT carry it, while
// the resolver serves it through the static-id policy.
const staticOnlyRegistryJSON = `{"providers":[{"name":"provstatic","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"gemini-3.8-flash-high","contextLength":300000}]}]}`

// sandboxP2 is sandboxP1 plus the P2 helper-child markers, so no test can touch
// the real ~/.agy-pro or the real user profile and no helper mode leaks between
// tests. It returns the agy-pro home and the fake user home.
func sandboxP2(t *testing.T) (agyHome, userHome string) {
	t.Helper()
	agyHome, userHome = sandboxP1(t)
	clearEnv(t, "GO_P2_HELPER_MODE")
	clearEnv(t, "GO_P2_HELPER_ENVFILE")
	clearEnv(t, "GO_P2_HELPER_ARGSFILE")
	clearEnv(t, "P2_STUB_ENVFILE")
	return agyHome, userHome
}

// goP2Report is a synthetic wirecheck report whose P2 canaries are all Found,
// so Verdict() is GO and ProfileOK("P2") is true. The table mirrors
// wirecheck/canaries.go's P2 rows exactly.
func goP2Report() wirecheck.Report {
	return wirecheck.Report{
		BinaryPath: filepath.Join("fake", "agy.exe"),
		Version:    wirecheck.PinnedVersion,
		Canaries: []wirecheck.Canary{
			{Profile: wirecheck.ProfileP2, Name: "scheme_gemini_api", Literal: "gemini-api:", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "scheme_blade", Literal: "blade:", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "scheme_evergreen", Literal: "evergreen:", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "scheme_beyond_api", Literal: "beyond-api:", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "genai_sdk_user_agent", Literal: "google-genai-sdk/", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "env_google_gemini_base_url", Literal: "GOOGLE_GEMINI_BASE_URL", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "env_gemini_api_key", Literal: "GEMINI_API_KEY", Found: true},
			{Profile: wirecheck.ProfileP2, Name: "settings_model_provider", Literal: "modelProvider", Found: true},
		},
	}
}

// p2NogoReport is a synthetic wirecheck report with one P2 canary missing, so
// Verdict() is NOGO.
func p2NogoReport() wirecheck.Report {
	return wirecheck.Report{
		BinaryPath: filepath.Join("fake", "agy.exe"),
		Version:    wirecheck.PinnedVersion,
		Canaries: []wirecheck.Canary{
			{Profile: wirecheck.ProfileP2, Name: "scheme_gemini_api", Literal: "gemini-api:", Found: false},
		},
	}
}

// readArgsFile parses the JSON argv dump the helper child writes.
func readArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args dump %s: %v", path, err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatalf("parse args dump %s: %v\n%s", path, err, data)
	}
	return args
}

// argValue returns the value following flag in args, or "" when absent.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// hasArg reports whether args contains the bare flag.
func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// settingsDocShape mirrors the written settings document for assertions.
type settingsDocShape struct {
	ModelProvider string `json:"modelProvider"`
	CustomModels  map[string]struct {
		ModelName     string `json:"modelName"`
		DisplayName   string `json:"displayName"`
		ContextWindow int    `json:"context_window"`
		ModelFeatures *struct {
			Images bool `json:"images"`
			Video  bool `json:"video"`
		} `json:"modelFeatures"`
	} `json:"customModels"`
}

// readSettingsDoc reads and parses the settings document at path.
func readSettingsDoc(t *testing.T, path string) settingsDocShape {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings %s: %v", path, err)
	}
	var doc settingsDocShape
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse settings %s: %v\n%s", path, err, data)
	}
	return doc
}

// plantGlobalCanary writes a known document at the protected global settings
// path inside the sandbox user home and returns its path. A P2 launch must
// leave it byte-identical.
func plantGlobalCanary(t *testing.T, userHome string) string {
	t.Helper()
	global, err := agysettings.GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const canary = `{"theme":"p2-global-canary"}`
	if err := os.WriteFile(global, []byte(canary), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return global
}

// countFilesNamed returns every file named name under root.
func countFilesNamed(t *testing.T, root, name string) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == name {
			hits = append(hits, path)
		}
		return nil
	})
	return hits
}

// geminiErrorShape mirrors wireadp's §9.1 error envelope.
type geminiErrorShape struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// recordingUpstream is a loopback OpenAI-compatible upstream that records the
// model of every call and streams one SSE completion (writeOpenAISSE).
type recordingUpstream struct {
	server *httptest.Server
	calls  int32
	models []string
}

func newRecordingUpstream(t *testing.T) *recordingUpstream {
	t.Helper()
	up := &recordingUpstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&up.calls, 1)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		up.models = append(up.models, body.Model)
		writeOpenAISSE(w)
	}))
	t.Cleanup(up.server.Close)
	return up
}

// ---------------------------------------------------------------------------
// helper child process (bounded, headless, never interactive)
// ---------------------------------------------------------------------------

// TestP2HelperChild is the bounded helper the LaunchP2 tests spawn as the "real
// agy". It is a no-op unless GO_P2_HELPER_MODE selects a behavior, so the
// parent's own test run never enters it. Every mode is bounded (a hard
// -test.timeout) and holds no terminal.
func TestP2HelperChild(t *testing.T) {
	switch os.Getenv("GO_P2_HELPER_MODE") {
	case "":
		return
	case "dumpenv":
		if p := os.Getenv("GO_P2_HELPER_ENVFILE"); p != "" {
			var b strings.Builder
			for _, e := range os.Environ() {
				b.WriteString(e)
				b.WriteByte('\n')
			}
			_ = os.WriteFile(p, []byte(b.String()), 0o600)
		}
		os.Exit(0)
	case "dumpenvargs":
		if p := os.Getenv("GO_P2_HELPER_ENVFILE"); p != "" {
			var b strings.Builder
			for _, e := range os.Environ() {
				b.WriteString(e)
				b.WriteByte('\n')
			}
			_ = os.WriteFile(p, []byte(b.String()), 0o600)
		}
		if p := os.Getenv("GO_P2_HELPER_ARGSFILE"); p != "" {
			if data, err := json.Marshal(os.Args); err == nil {
				_ = os.WriteFile(p, data, 0o600)
			}
		}
		os.Exit(0)
	case "exit":
		n := 0
		if v := os.Getenv("GO_P2_HELPER_EXIT"); v != "" {
			fmt.Sscanf(v, "%d", &n)
		}
		os.Exit(n)
	default:
		fmt.Fprintf(os.Stderr, "p2 helper: unknown mode %q\n", os.Getenv("GO_P2_HELPER_MODE"))
		os.Exit(2)
	}
}

// p2SelfHelperArgs returns the args that re-invoke this test binary as the
// helper child, running only TestP2HelperChild under a hard timeout. The
// trailing "--" terminates the test binary's flag parsing, so the P2 argv
// LaunchP2 appends (--app_data_dir, --model) reaches the helper in os.Args
// instead of being rejected as unknown test flags.
func p2SelfHelperArgs() []string {
	return []string{"-test.run=^TestP2HelperChild$", "-test.timeout=120s", "--"}
}

// p2SelfHelperArgsModel is p2SelfHelperArgs with a --model pin inserted BEFORE
// the trailing "--" terminator, so the pin is a real flag agy parses (and the
// front door rewrites). A token after "--" is a positional, not a selector
// (R5AF3-1), so appending the pin after p2SelfHelperArgs would leave it
// un-extracted.
func p2SelfHelperArgsModel(model string) []string {
	base := p2SelfHelperArgs()
	out := make([]string, 0, len(base)+2)
	out = append(out, base[:len(base)-1]...) // drop the trailing "--"
	return append(out, "--model", model, "--")
}

// p2HelperChildEnv returns the ChildEnv entries that make the helper dump its
// environment and argv into fresh temp files.
func p2HelperChildEnv(t *testing.T) (childEnv []string, envFile, argsFile string) {
	t.Helper()
	envFile = filepath.Join(t.TempDir(), "child.env")
	argsFile = filepath.Join(t.TempDir(), "child.args")
	return []string{
		"GO_P2_HELPER_MODE=dumpenvargs",
		"GO_P2_HELPER_ENVFILE=" + envFile,
		"GO_P2_HELPER_ARGSFILE=" + argsFile,
	}, envFile, argsFile
}

// ---------------------------------------------------------------------------
// buildP2Config
// ---------------------------------------------------------------------------

func TestBuildP2Config_StrictNonceAndSeams(t *testing.T) {
	agyHome, _ := sandboxP2(t)
	reg, err := providers.LoadFrom(mustWriteRegistry(t, p2RegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cfg, err := buildP2Config(reg, agyHome)
	if err != nil {
		t.Fatalf("buildP2Config: %v", err)
	}

	// Strict mode is unconditional: AuthKey is a non-empty 64-char hex nonce
	// (32 random bytes). The permissive mode must never be reachable here.
	if cfg.AuthKey == "" {
		t.Fatal("AuthKey must never be empty on the P2 path (strict mode is mandatory)")
	}
	if len(cfg.AuthKey) != 64 {
		t.Fatalf("AuthKey length = %d; want 64 hex chars (32 bytes)", len(cfg.AuthKey))
	}
	for _, c := range cfg.AuthKey {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("AuthKey is not lowercase hex: %q", cfg.AuthKey)
		}
	}

	// P2 does not use /v1internal: the field stays nil, which wireadp reads as
	// the STRICT default. The passive-receipt opt-in is not taken.
	if cfg.RequireV1InternalAuth != nil {
		t.Errorf("RequireV1InternalAuth = %v; want nil (strict default; P2 never speaks /v1internal)", *cfg.RequireV1InternalAuth)
	}
	// The genai surface lists nothing (W12): no catalog, no RPC hooks.
	if cfg.CatalogFunc != nil {
		t.Error("CatalogFunc must be nil on the P2 path (the genai surface lists nothing)")
	}
	if cfg.RPCHooks != nil {
		t.Error("RPCHooks must be nil on the P2 path")
	}

	// The resolver seam resolves an enabled external, misses an unknown one,
	// and applies the static-id policy.
	if cfg.ResolveModel == nil {
		t.Fatal("ResolveModel must be set")
	}
	if target, ok, err := cfg.ResolveModel("ext-nw"); err != nil || !ok || target.Model.ID != "ext-nw" {
		t.Fatalf("ResolveModel(ext-nw) = (%q, %v, %v); want (ext-nw, true, nil)", target.Model.ID, ok, err)
	}
	if _, ok, _ := cfg.ResolveModel("no-such-model"); ok {
		t.Error("ResolveModel must miss an unknown model")
	}
	// The registry carries gemini-3.8-flash-high, so the static id resolves.
	if target, ok, err := cfg.ResolveModel("gemini-3.8-flash-high"); err != nil || !ok || target.Model.ID != "gemini-3.8-flash-high" {
		t.Fatalf("ResolveModel(gemini-3.8-flash-high) = (%q, %v, %v); want the static-id target, true, nil", target.Model.ID, ok, err)
	}

	if cfg.Redact == nil {
		t.Error("Redact must be set")
	}
	if cfg.Generate != nil {
		t.Error("Generate must be nil so wireadp installs its default handler")
	}
	if !strings.HasPrefix(cfg.AuditPath, filepath.Join(agyHome, "logs")) {
		t.Fatalf("AuditPath = %q; want it under %s", cfg.AuditPath, filepath.Join(agyHome, "logs"))
	}
	if _, err := os.Stat(filepath.Join(agyHome, "logs")); err != nil {
		t.Fatalf("the logs directory must be created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// static-id policy
// ---------------------------------------------------------------------------

func TestStaticIDTarget_BothBranches(t *testing.T) {
	servable := &providers.Registry{Providers: []providers.Provider{{
		Name:   "provstatic",
		Models: []providers.Model{{ID: "gemini-3.8-flash-high", ContextLength: 300000}},
	}}}
	// A registry whose static-id model is DISABLED is not servable.
	disabled := &providers.Registry{Providers: []providers.Provider{{
		Name:   "provstatic",
		Models: []providers.Model{{ID: "gemini-3.8-flash-high", Enabled: boolPtr(false)}},
	}}}
	// A case variant of the static id is still an exact policy hit.
	variant := &providers.Registry{Providers: []providers.Provider{{
		Name:   "provstatic",
		Models: []providers.Model{{ID: "Gemini-3.8-Flash-High", ContextLength: 300000}},
	}}}

	// Served: the registry carries the exact id.
	target, ok, note := StaticIDTarget(servable, "gemini-3.8-flash-high")
	if !ok || target.Model.ID != "gemini-3.8-flash-high" {
		t.Fatalf("StaticIDTarget(servable) = (%q, %v, %q); want the target, true, a note", target.Model.ID, ok, note)
	}
	if !strings.Contains(note, "AI-Studio-billed") {
		t.Errorf("the served note must carry the AI-Studio-billed label: %q", note)
	}

	// Refused: the registry has no such model.
	_, ok, reason := StaticIDTarget(&providers.Registry{}, "gemini-3.8-flash-high")
	if ok || reason == "" {
		t.Fatalf("StaticIDTarget(unservable) = (_, %v, %q); want false plus a reason", ok, reason)
	}
	if !strings.Contains(reason, "gemini-3.8-flash-high") {
		t.Errorf("the reason must name the model: %q", reason)
	}

	// A disabled static-id model is not servable.
	if _, ok, _ := StaticIDTarget(disabled, "gemini-3.8-flash-high"); ok {
		t.Error("a disabled static-id model must not be served")
	}

	// A case variant matches (the deny-list is case-insensitive too).
	if target, ok, _ := StaticIDTarget(variant, "gemini-3.8-flash-high"); !ok || target.Model.ID != "Gemini-3.8-Flash-High" {
		t.Errorf("StaticIDTarget(case variant) = (%q, %v); want the target, true", target.Model.ID, ok)
	}

	// Not a static id: not a static-id question (empty reason).
	if _, ok, reason := StaticIDTarget(servable, "ext-nw"); ok || reason != "" {
		t.Errorf("StaticIDTarget(non-static) = (_, %v, %q); want false plus an empty reason", ok, reason)
	}

	// A nil registry refuses every static id.
	if _, ok, _ := StaticIDTarget(nil, "gemini-3.8-flash-high"); ok {
		t.Error("a nil registry must not serve a static id")
	}
}

func TestP2Resolver_StaticIDPolicy(t *testing.T) {
	servable := &providers.Registry{Providers: []providers.Provider{{
		Name:   "provstatic",
		Models: []providers.Model{{ID: "gemini-3.8-flash-high", ContextLength: 300000}},
	}}}

	// Served branch: the static id resolves to the registry target.
	res := p2Resolver(servable)
	if target, ok, err := res("gemini-3.8-flash-high"); err != nil || !ok || target.Model.ID != "gemini-3.8-flash-high" {
		t.Fatalf("resolver(gemini-3.8-flash-high) = (%q, %v, %v); want the target, true, nil", target.Model.ID, ok, err)
	}

	// Refused branch: the miss is reported as (zero, false, nil), so wireadp
	// answers its Gemini-shaped 404 naming the model (a resolver ERROR would
	// map to a 500 instead).
	unservable := p2Resolver(&providers.Registry{Providers: []providers.Provider{{
		Name:   "provnw",
		Models: []providers.Model{{ID: "ext-nw", ContextLength: 300000}},
	}}})
	target, ok, err := unservable("gemini-3.8-flash-high")
	if ok || err != nil || target.Model.ID != "" {
		t.Fatalf("resolver(unservable static id) = (%q, %v, %v); want (zero, false, nil) so wireadp 404s", target.Model.ID, ok, err)
	}

	// A non-static id keeps the ordinary allow-list behavior.
	if target, ok, err := unservable("ext-nw"); err != nil || !ok || target.Model.ID != "ext-nw" {
		t.Fatalf("resolver(ext-nw) = (%q, %v, %v); want the target, true, nil", target.Model.ID, ok, err)
	}
}

// ---------------------------------------------------------------------------
// the mesh: strict /v1beta auth, the blade: derivation, the static-id policy
// ---------------------------------------------------------------------------

// TestP2ThroughMesh_StrictAuthAndBladeDerivation starts a real mesh over a temp
// registry (loopback httptest upstream) and drives the public Gemini wire:
// without the install nonce /v1beta answers 401; with it, a request to
// /v1beta/models/<slug>:streamGenerateContent (the exact path agy derives from
// a blade: key, W10) routes to the right registry target and streams back.
func TestP2ThroughMesh_StrictAuthAndBladeDerivation(t *testing.T) {
	agyHome, _ := sandboxP2(t)
	up := newRecordingUpstream(t)

	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"provnw","baseURL":"%s/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"ext-nw","contextLength":300000}]}]}`, up.server.URL))
	// The mesh resolves the provider key from its env-var name at request time;
	// the value stays in this process and never reaches the child.
	t.Setenv("NW_API_KEY", "planted-nw-key-value")
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cfg, err := buildP2Config(reg, agyHome)
	if err != nil {
		t.Fatalf("buildP2Config: %v", err)
	}
	m, err := mesh.Start(mesh.Config{Config: cfg})
	if err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	const body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	url := m.URL() + "/v1beta/models/ext-nw:streamGenerateContent?alt=sse"

	// No nonce: strict mode answers 401 (the genai path is where agy DOES send
	// GEMINI_API_KEY as X-Goog-Api-Key, so strict is the right posture).
	noNonce, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST (no nonce): %v", err)
	}
	noNonce.Body.Close()
	if noNonce.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-nonce status = %d; want 401 (strict /v1beta)", noNonce.StatusCode)
	}

	// With the nonce: the blade: derivation routes the bare slug to the right
	// registry target and the upstream is reached.
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", cfg.AuthKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST (nonce): %v", err)
	}
	gen := readAllAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nonce status = %d; want 200\n%s", resp.StatusCode, gen)
	}
	if !strings.Contains(string(gen), "data:") {
		t.Errorf("the response must be SSE:\n%s", gen)
	}
	if !strings.Contains(string(gen), "PROBE_OK") {
		t.Errorf("the response must carry the upstream content:\n%s", gen)
	}
	if atomic.LoadInt32(&up.calls) == 0 {
		t.Fatal("the generation round-trip never reached the upstream")
	}
	if len(up.models) != 1 || up.models[0] != "ext-nw" {
		t.Fatalf("upstream models = %v; want [ext-nw] (the blade: derivation routes by the bare slug)", up.models)
	}
}

// TestStaticIDThroughMesh_ServedBranch proves the static-id policy's served
// branch through the live mesh: a registry carrying the exact static id is
// served when agy requests that static id.
func TestStaticIDThroughMesh_ServedBranch(t *testing.T) {
	agyHome, _ := sandboxP2(t)
	up := newRecordingUpstream(t)

	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"provstatic","baseURL":"%s/v1","apiKeyEnv":"STATIC_API_KEY","models":[{"id":"gemini-3.8-flash-high","contextLength":300000}]}]}`, up.server.URL))
	t.Setenv("STATIC_API_KEY", "planted-static-key-value")
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cfg, err := buildP2Config(reg, agyHome)
	if err != nil {
		t.Fatalf("buildP2Config: %v", err)
	}
	m, err := mesh.Start(mesh.Config{Config: cfg})
	if err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	req, err := http.NewRequest(http.MethodPost,
		m.URL()+"/v1beta/models/gemini-3.8-flash-high:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", cfg.AuthKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	gen := readAllAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static-id status = %d; want 200 (served branch)\n%s", resp.StatusCode, gen)
	}
	if len(up.models) != 1 || up.models[0] != "gemini-3.8-flash-high" {
		t.Fatalf("upstream models = %v; want [gemini-3.8-flash-high]", up.models)
	}
}

// TestStaticIDThroughMesh_Unservable404 proves the refused branch: a static id
// the registry does not carry becomes wireadp's Gemini-shaped 404 that names
// the model, and the upstream is never called (never a silent substitution).
func TestStaticIDThroughMesh_Unservable404(t *testing.T) {
	agyHome, _ := sandboxP2(t)
	up := newRecordingUpstream(t)

	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"provnw","baseURL":"%s/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"ext-nw","contextLength":300000}]}]}`, up.server.URL))
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cfg, err := buildP2Config(reg, agyHome)
	if err != nil {
		t.Fatalf("buildP2Config: %v", err)
	}
	m, err := mesh.Start(mesh.Config{Config: cfg})
	if err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	req, err := http.NewRequest(http.MethodPost,
		m.URL()+"/v1beta/models/gemini-3.8-flash-high:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", cfg.AuthKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := readAllAndClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unservable static id status = %d; want 404\n%s", resp.StatusCode, body)
	}
	var gerr geminiErrorShape
	if err := json.Unmarshal(body, &gerr); err != nil {
		t.Fatalf("the 404 must be a Gemini-shaped error envelope: %v\n%s", err, body)
	}
	if gerr.Error.Status != "NOT_FOUND" {
		t.Errorf("error status = %q; want NOT_FOUND", gerr.Error.Status)
	}
	if !strings.Contains(gerr.Error.Message, "gemini-3.8-flash-high") {
		t.Errorf("the error must name the model: %q", gerr.Error.Message)
	}
	if atomic.LoadInt32(&up.calls) != 0 {
		t.Error("an unservable static id must never reach the upstream (no silent substitution)")
	}
}

// readAllAndClose reads a response body and closes it.
func readAllAndClose(body io.ReadCloser) []byte {
	if body == nil {
		return nil
	}
	data, _ := io.ReadAll(body)
	_ = body.Close()
	return data
}

// ---------------------------------------------------------------------------
// externals composition + app-data dir name
// ---------------------------------------------------------------------------

func TestP2Externals_BladeKeysAndFields(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, p2RegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	var drops []string
	entries := p2Externals(reg, "http://127.0.0.1:55555", func(format string, args ...any) {
		drops = append(drops, fmt.Sprintf(format, args...))
	})

	// Two enabled, non-reserved externals: ext-nw and ext-charm. ext-off is
	// disabled, gemini-3.8-flash-high is reserved (static-id policy territory).
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d; want 2\n%+v", len(entries), entries)
	}
	byKey := map[string]agysettings.ExternalEntry{}
	for _, e := range entries {
		byKey[e.URL] = e
	}

	nw, ok := byKey["blade:127.0.0.1:55555/models/ext-nw"]
	if !ok {
		t.Fatalf("the blade: key for ext-nw is missing: %v", entryKeys(entries))
	}
	if nw.ModelName != nw.URL {
		t.Errorf("ModelName = %q; want the same key %q", nw.ModelName, nw.URL)
	}
	if nw.DisplayName != "ext-nw (provnw)" {
		t.Errorf("DisplayName = %q; want %q", nw.DisplayName, "ext-nw (provnw)")
	}
	if nw.ContextWindow != 300000 {
		t.Errorf("ContextWindow = %d; want 300000 (the registry value verbatim)", nw.ContextWindow)
	}
	if nw.ModelFeatures != nil {
		t.Errorf("ModelFeatures = %+v; want nil (no media capability declared)", nw.ModelFeatures)
	}

	charm, ok := byKey["blade:127.0.0.1:55555/models/ext-charm"]
	if !ok {
		t.Fatalf("the blade: key for ext-charm is missing: %v", entryKeys(entries))
	}
	if charm.ContextWindow != 300000 {
		t.Errorf("ContextWindow = %d; want 300000 (the registry value verbatim)", charm.ContextWindow)
	}

	// A registry model without contextLength emits 0 verbatim (agy's own zero),
	// never an invented default.
	zeroReg, err := providers.LoadFrom(mustWriteRegistry(t, `{"providers":[{"name":"provzero","baseURL":"http://127.0.0.1:9/v1","models":[{"id":"ext-zero"}]}]}`))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	zeroEntries := p2Externals(zeroReg, "http://127.0.0.1:55555", nil)
	if len(zeroEntries) != 1 || zeroEntries[0].ContextWindow != 0 {
		t.Fatalf("zero-context entries = %+v; want one entry with ContextWindow 0", zeroEntries)
	}

	// The reserved drop is observable, and the document is still composable.
	joined := strings.Join(drops, "\n")
	if !strings.Contains(joined, "gemini-3.8-flash-high") || !strings.Contains(joined, "reserved") {
		t.Errorf("the reserved-id drop must be logged: %q", joined)
	}
	if _, err := agysettings.BuildP2Settings(entries); err != nil {
		t.Fatalf("BuildP2Settings(entries) = %v; want the composed document to be writable", err)
	}
}

// TestP2Externals_VisionCapabilitiesMapToModelFeatures pins the capabilities
// mapping: images from supportsVision, video from supportsVideo, nil when
// neither is declared.
func TestP2Externals_VisionCapabilitiesMapToModelFeatures(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, visionRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	entries := p2Externals(reg, "http://127.0.0.1:55555", nil)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d; want 1", len(entries))
	}
	if entries[0].ModelFeatures == nil || !entries[0].ModelFeatures.Images {
		t.Fatalf("ModelFeatures = %+v; want images=true (supportsVision)", entries[0].ModelFeatures)
	}
	if entries[0].ModelFeatures.Video {
		t.Error("Video must be false when supportsVideo is not declared")
	}
}

// TestP2Externals_DropsCrossProviderDuplicate pins the duplicate drop: the first
// provider wins, exactly like the P1 catalog.
func TestP2Externals_DropsCrossProviderDuplicate(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, `{"providers":[
		{"name":"first","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"A_API_KEY","models":[{"id":"shared","contextLength":1000}]},
		{"name":"second","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"B_API_KEY","models":[{"id":"shared","contextLength":2000}]}
	]}`))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	var drops []string
	entries := p2Externals(reg, "http://127.0.0.1:55555", func(format string, args ...any) {
		drops = append(drops, fmt.Sprintf(format, args...))
	})
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d; want 1 (the duplicate is dropped)", len(entries))
	}
	if entries[0].ContextWindow != 1000 {
		t.Errorf("ContextWindow = %d; want 1000 (the first provider wins)", entries[0].ContextWindow)
	}
	if !strings.Contains(strings.Join(drops, "\n"), "already registered") {
		t.Errorf("the duplicate drop must be logged: %q", drops)
	}
}

func TestNewAppDataDirName_RelativeAndUnique(t *testing.T) {
	a, err := newAppDataDirName()
	if err != nil {
		t.Fatalf("newAppDataDirName: %v", err)
	}
	b, err := newAppDataDirName()
	if err != nil {
		t.Fatalf("newAppDataDirName: %v", err)
	}
	for _, name := range []string{a, b} {
		if !strings.HasPrefix(name, appDataDirPrefix) {
			t.Errorf("name = %q; want the %q prefix", name, appDataDirPrefix)
		}
		// Relative, single segment: no volume, no separator, no traversal, and
		// resolvable through agysettings' validator (which rejects absolute and
		// traversing values).
		if filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
			t.Errorf("name = %q; want a relative value", name)
		}
		if strings.ContainsAny(name, `:\`) || strings.Contains(name, "..") {
			t.Errorf("name = %q; want no volume separator, backslash or traversal", name)
		}
		if _, err := agysettings.AppDataDirSettingsPath(name); err != nil {
			t.Errorf("AppDataDirSettingsPath(%q) = %v; want the name to validate", name, err)
		}
	}
	if a == b {
		t.Fatalf("two spawns produced the same app-data dir name %q (timestamp + random suffix must differ)", a)
	}
}

// ---------------------------------------------------------------------------
// child argv construction (the --model pin)
// ---------------------------------------------------------------------------

func TestP2ChildArgs_ModelForms(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	const wantKey = "blade:127.0.0.1:4444/models/ext-nw"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	tests := []struct {
		name     string
		args     []string
		wantFlag bool
		wantVal  string
	}{
		{"no args", nil, false, ""},
		{"plain prompt arg", []string{"-p", "hi"}, false, ""},
		{"space form", []string{"--model", "ext-nw"}, true, wantKey},
		{"equals form", []string{"--model=ext-nw"}, true, wantKey},
		{"short space form", []string{"-m", "ext-nw"}, true, wantKey},
		{"short equals form", []string{"-m=ext-nw"}, true, wantKey},
		{"picker label form", []string{"--model", "ext-nw (provnw)"}, true, wantKey},
		{"provider slash form", []string{"--model", "provnw/ext-nw"}, true, wantKey},
		{"scheme key passes through", []string{"--model", "blade:127.0.0.1:1/models/other"}, true, "blade:127.0.0.1:1/models/other"},
		{"gemini-api key passes through", []string{"--model", "gemini-api://127.0.0.1:1/models/other"}, true, "gemini-api://127.0.0.1:1/models/other"},
		{"static id passes through verbatim", []string{"--model", "gemini-3.8-flash-high"}, true, "gemini-3.8-flash-high"},
		{"last selector wins", []string{"--model", "ext-nw", "-p", "hi", "--model", "ext-charm"}, true, "blade:127.0.0.1:4444/models/ext-charm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p2ChildArgs(tt.args, "agy-hybrid-test", meshURL, reg)
			// The app-data dir is always passed, and the caller's args survive
			// in order (minus the extracted selector).
			if v := argValue(got, "--app_data_dir"); v != "agy-hybrid-test" {
				t.Fatalf("--app_data_dir = %q; want agy-hybrid-test\nargs: %v", v, got)
			}
			if !tt.wantFlag {
				if hasArg(got, "--model") {
					t.Fatalf("no model was pinned, so --model must be absent: %v", got)
				}
				return
			}
			if v := argValue(got, "--model"); v != tt.wantVal {
				t.Fatalf("--model = %q; want %q\nargs: %v", v, tt.wantVal, got)
			}
			// Exactly one --model reaches the child.
			n := 0
			for _, a := range got {
				if a == "--model" {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("the child argv carries %d --model flags; want exactly 1: %v", n, got)
			}
		})
	}
}

// entryKeys returns the entry keys in order.
func entryKeys(entries []agysettings.ExternalEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.URL)
	}
	return out
}

// samePathString compares two paths for identity under this platform's rules
// (the agysettings samePath discipline, restated here so the launch test can
// assert the child's --app_data_dir names the file that was written).
func samePathString(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// ---------------------------------------------------------------------------
// LaunchP2: settings document, app-data dir, child env
// ---------------------------------------------------------------------------

// TestLaunchP2_SettingsWrittenOnlyToAppDataDir runs a full P2 launch and proves
// the settings regime: the document lands ONLY in the per-session app-data dir,
// the protected global file stays byte-identical, and the child receives the
// relative --app_data_dir (and no --model when nothing was pinned).
func TestLaunchP2_SettingsWrittenOnlyToAppDataDir(t *testing.T) {
	agyHome, userHome := sandboxP2(t)
	writeRegistry(t, p2RegistryJSON)
	swapWirecheck(t, goP2Report())
	globalCanary := plantGlobalCanary(t, userHome)
	globalBefore, err := os.ReadFile(globalCanary)
	if err != nil {
		t.Fatalf("read canary: %v", err)
	}

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
	childEnv, envFile, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}

	// Exactly one settings file outside the protected global path, and it is
	// the per-session one (the planted global canary is excluded by name).
	var settings []string
	for _, p := range countFilesNamed(t, userHome, "settings.json") {
		if samePathString(p, globalCanary) {
			continue
		}
		settings = append(settings, p)
	}
	if len(settings) != 1 {
		t.Fatalf("settings.json files under the sandbox user home = %v; want exactly 1 (the app-data dir)", settings)
	}
	settingsPath := settings[0]
	if !strings.Contains(filepath.ToSlash(settingsPath), "/.gemini/agy-hybrid-") {
		t.Fatalf("settings path = %q; want it under <home>/.gemini/agy-hybrid-<id>/", settingsPath)
	}

	// The child received the SAME relative app-data dir name.
	args := readArgsFile(t, argsFile)
	appDataDir := argValue(args, "--app_data_dir")
	if appDataDir == "" {
		t.Fatalf("the child argv has no --app_data_dir: %v", args)
	}
	if want := filepath.Join(userHome, ".gemini", appDataDir, "settings.json"); !samePathString(settingsPath, want) {
		t.Fatalf("settings path = %q; want %q (the child's --app_data_dir)", settingsPath, want)
	}
	// No model was pinned, so no --model rides along (agy lands on its static
	// default; the picker unit adds the interactive selection later).
	if hasArg(args, "--model") {
		t.Errorf("no model was pinned, so --model must be absent: %v", args)
	}

	// The document: modelProvider plus a blade: customModels entry for every
	// ENABLED, non-reserved external (ext-nw, ext-charm; not the disabled one).
	doc := readSettingsDoc(t, settingsPath)
	if doc.ModelProvider != "gemini" {
		t.Errorf("modelProvider = %q; want %q", doc.ModelProvider, "gemini")
	}
	port := startedMesh.Port()
	wantKeys := []string{
		fmt.Sprintf("blade:127.0.0.1:%d/models/ext-nw", port),
		fmt.Sprintf("blade:127.0.0.1:%d/models/ext-charm", port),
	}
	if len(doc.CustomModels) != len(wantKeys) {
		t.Fatalf("customModels = %v; want exactly %v", keysOfDoc(doc), wantKeys)
	}
	for _, key := range wantKeys {
		entry, ok := doc.CustomModels[key]
		if !ok {
			t.Fatalf("customModels is missing %q: %v", key, keysOfDoc(doc))
		}
		if entry.ModelName != key {
			t.Errorf("%s: modelName = %q; want the same key", key, entry.ModelName)
		}
		if entry.ContextWindow != 300000 {
			t.Errorf("%s: context_window = %d; want 300000", key, entry.ContextWindow)
		}
		if !strings.HasPrefix(entry.DisplayName, "ext-") {
			t.Errorf("%s: displayName = %q; want the <id> (<provider>) label", key, entry.DisplayName)
		}
	}
	if _, ok := doc.CustomModels[fmt.Sprintf("blade:127.0.0.1:%d/models/ext-off", port)]; ok {
		t.Error("a disabled model must not get a customModels entry")
	}
	if _, ok := doc.CustomModels[fmt.Sprintf("blade:127.0.0.1:%d/models/gemini-3.8-flash-high", port)]; ok {
		t.Error("a reserved static id must not get a customModels entry (the static-id policy serves it)")
	}

	// The protected global settings file is untouched (no write, no lock).
	globalAfter, err := os.ReadFile(globalCanary)
	if err != nil {
		t.Fatalf("read canary after: %v", err)
	}
	if !bytes.Equal(globalBefore, globalAfter) {
		t.Fatalf("the global settings file changed:\nbefore: %s\nafter:  %s", globalBefore, globalAfter)
	}
	// The write cycle really ran through agysettings: the per-session dir holds
	// the released lockfile (truncated, never unlinked), while the protected
	// global path gained none.
	if _, err := os.Stat(settingsPath + ".lock"); err != nil {
		t.Errorf("the per-session settings dir must carry the released lockfile: %v", err)
	}
	if lock := countFilesNamed(t, filepath.Dir(globalCanary), "settings.json.lock"); len(lock) != 0 {
		t.Errorf("the global settings path gained a lock sibling: %v", lock)
	}

	// Nothing was written under the agy-pro home either.
	if hit := findFile(t, agyHome, "settings.json"); hit != "" {
		t.Errorf("a settings file leaked into the agy-pro home: %s", hit)
	}

	// The child env still went through the scrub (the marker proves the helper ran).
	if _, ok := readEnvFile(t, envFile)["AGY_PRO_ACTIVE"]; !ok {
		t.Error("the child env dump is missing AGY_PRO_ACTIVE")
	}
}

// TestLaunchP2_ChildEnvConstruction pins the P2 credential boundary: the scrub
// removes the user's real GEMINI_API_KEY/GOOGLE_API_KEY and every provider key
// name, THEN GOOGLE_GEMINI_BASE_URL and GEMINI_API_KEY=<install nonce> are
// appended after it. No provider key value reaches the child or any file.
func TestLaunchP2_ChildEnvConstruction(t *testing.T) {
	agyHome, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

	var startedMesh *mesh.Mesh
	var capturedAuthKey string
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		capturedAuthKey = cfg.AuthKey
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})

	// Plant provider key VALUES in the parent env; they must NOT reach the child.
	t.Setenv("NW_API_KEY", "sk-planted-nw-secret-value")
	t.Setenv("CHARM_API_KEY", "sk-planted-charm-secret-value")
	// Planted AFTER the sandbox clears them, so the "absent from the child"
	// assertions prove CleanChildEnv SCRUBS them rather than never seeing them.
	t.Setenv("GEMINI_API_KEY", testfixtures.AIzaPlantedGemini)
	t.Setenv("GOOGLE_API_KEY", testfixtures.AIzaPlantedGoogle)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, envFile, _ := p2HelperChildEnv(t)
	var visible bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &visible,
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}

	childEnvMap := readEnvFile(t, envFile)
	if got := childEnvMap["GOOGLE_GEMINI_BASE_URL"]; got != startedMesh.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q; want %q", got, startedMesh.URL())
	}
	// The nonce is appended AFTER the scrub: the child presents exactly the
	// mesh's install nonce, not the planted ambient key.
	if got := childEnvMap["GEMINI_API_KEY"]; got != capturedAuthKey {
		t.Errorf("GEMINI_API_KEY = %q; want the install nonce %q", got, capturedAuthKey)
	}
	if len(capturedAuthKey) != 64 {
		t.Errorf("install nonce length = %d; want 64 hex chars", len(capturedAuthKey))
	}
	if childEnvMap["AGY_PRO_ACTIVE"] != "1" {
		t.Errorf("AGY_PRO_ACTIVE = %q; want 1", childEnvMap["AGY_PRO_ACTIVE"])
	}
	// No provider key NAMES (hence no VALUES), and no ambient Gemini key: the
	// scrub runs first, the nonce append second.
	for _, k := range []string{"NW_API_KEY", "CHARM_API_KEY", "GOOGLE_API_KEY"} {
		if v, ok := childEnvMap[k]; ok {
			t.Errorf("%s must not reach the child (got %q)", k, v)
		}
	}
	if childEnvMap["GEMINI_API_KEY"] == testfixtures.AIzaPlantedGemini {
		t.Error("the planted ambient GEMINI_API_KEY must be scrubbed, not inherited")
	}
	// FIX-8d: no planted secret value reaches ANY file, sweeping the two REAL P2
	// sinks (the mesh audit log under <agyHome>/logs/wireadp-*.jsonl and the
	// per-session <userHome>/.gemini/agy-hybrid-*/settings.json) plus the child
	// dump. The live install nonce is a SEPARATE needle: it MUST reach the child
	// (agy authenticates to the mesh with it), so it is only forbidden from the
	// persistent sinks and stderr, never the child dump.
	planted := []string{
		"sk-planted-nw-secret-value",
		"sk-planted-charm-secret-value",
		"AIzaPLANTEDambientGEMINI",
		"AIzaPLANTEDambientGOOGLE",
	}
	sinkNeedles := append(append([]string(nil), planted...), capturedAuthKey)
	for _, root := range []string{agyHome, userHome} {
		for _, secret := range sinkNeedles {
			if hit := treeContains(t, root, secret); hit != "" {
				t.Errorf("a secret leaked into a persistent sink (%s): %s", secret, hit)
			}
		}
	}
	for _, secret := range planted {
		if hit := treeContains(t, filepath.Dir(envFile), secret); hit != "" {
			t.Errorf("a planted secret leaked into the child dump: %s", hit)
		}
	}
	if s := visible.String(); s != "" {
		for _, secret := range sinkNeedles {
			if strings.Contains(s, secret) {
				t.Errorf("a secret leaked into stderr: %q", secret)
			}
		}
	}
}

// TestLaunchP2_ModelPinReceivesFullMapKey proves the launcher's pinned model
// reaches the child as the full blade: map key (a bare slug is rejected by agy
// per W11).
func TestLaunchP2_ModelPinReceivesFullMapKey(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

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
	childEnv, _, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel("ext-nw"),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}
	want := fmt.Sprintf("blade:127.0.0.1:%d/models/ext-nw", startedMesh.Port())
	if got := argValue(readArgsFile(t, argsFile), "--model"); got != want {
		t.Fatalf("--model = %q; want the full map key %q", got, want)
	}
}

// TestLaunchP2_StaticIDPinPassesThroughVerbatim proves a pinned static Gemini
// id reaches the child verbatim, so agy resolves it from its own static catalog
// and the mesh's static-id policy decides whether it is servable (rewriting it
// into a blade: key would point at a customModels entry that must not exist).
func TestLaunchP2_StaticIDPinPassesThroughVerbatim(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, p2RegistryJSON)
	swapWirecheck(t, goP2Report())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel("gemini-3.8-flash-high"),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if got := argValue(readArgsFile(t, argsFile), "--model"); got != "gemini-3.8-flash-high" {
		t.Fatalf("--model = %q; want the static id verbatim", got)
	}
}

// TestLaunchP2_AppDataDirUniquePerSpawn proves the per-session app-data dir is
// relative and unique per spawn: two launches produce two different names.
func TestLaunchP2_AppDataDirUniquePerSpawn(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		childEnv, _, argsFile := p2HelperChildEnv(t)
		code, err := LaunchP2(LaunchOptions{
			RealAgyPath: self,
			Args:        p2SelfHelperArgs(),
			ChildEnv:    childEnv,
		})
		if err != nil || code != 0 {
			t.Fatalf("launch %d: LaunchP2 = (%d, %v); want (0, nil)", i, code, err)
		}
		name := argValue(readArgsFile(t, argsFile), "--app_data_dir")
		if name == "" {
			t.Fatalf("launch %d: the child argv has no --app_data_dir", i)
		}
		names = append(names, name)
	}
	if names[0] == names[1] {
		t.Fatalf("two spawns reused the app-data dir name %q; the timestamp + random suffix must differ", names[0])
	}
}

// ---------------------------------------------------------------------------
// LaunchP2: exit-code map
// ---------------------------------------------------------------------------

func TestLaunchP2_ExitCodePassthrough(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    []string{"GO_P2_HELPER_MODE=exit", "GO_P2_HELPER_EXIT=42"},
	})
	if err != nil {
		t.Fatalf("LaunchP2 error: %v", err)
	}
	if code != 42 {
		t.Fatalf("exit code = %d; want 42 (child exit passthrough)", code)
	}
}

// TestLaunchP2_MeshDeathMapsToExit75 shares P1's ExitMeshDied constant: a mesh
// that dies mid-session yields 75 with a nil error, the mesh's single visible
// line, and a stopped mesh.
func TestLaunchP2_MeshDeathMapsToExit75(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

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
		// Reproduce the mesh's mid-session-death contract exactly as
		// mesh.RunChild writes it (the real child-kill + visible line live in
		// internal/mesh/child_test.go).
		fmt.Fprintf(capturedCfg.ErrWriter, "mesh: %s (mesh %s, port %d; health probe failed); the supervised child was killed\n",
			mesh.ErrMeshDied.Error(), m.URL(), m.Port())
		return fmt.Errorf("mesh: %w: the health probe on %s (port %d) failed", mesh.ErrMeshDied, m.URL(), m.Port())
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &visible,
		ChildEnv:    []string{"GO_P2_HELPER_MODE=exit", "GO_P2_HELPER_EXIT=0"},
	})
	if err != nil {
		t.Fatalf("LaunchP2 returned an error for a mesh death (the visible line already covers it): %v", err)
	}
	if code != ExitMeshDied {
		t.Fatalf("exit code = %d; want %d (mesh died mid-session)", code, ExitMeshDied)
	}
	line := visible.String()
	if !strings.Contains(line, "mesh died mid-session") {
		t.Errorf("the visible line must state the mid-session death: %q", line)
	}
	if startedMesh != nil {
		if !strings.Contains(line, fmt.Sprintf("port %d", startedMesh.Port())) {
			t.Errorf("the visible line must name the mesh port %d: %q", startedMesh.Port(), line)
		}
		if err := startedMesh.Healthz(context.Background()); err == nil {
			t.Error("the mesh must be stopped after a mid-session death")
		}
	}
	if capturedCfg.ErrWriter != io.Writer(&visible) {
		t.Error("the mesh ErrWriter must be the injected stderr")
	}
}

// TestLaunchP2_SettingsWriteFailureExit1NoChild proves the never-half-configured
// rule: a settings write failure returns exit 1 with a clear error, the child is
// never started, and the mesh is stopped.
func TestLaunchP2_SettingsWriteFailureExit1NoChild(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

	var startedMesh *mesh.Mesh
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})
	prevWrite := settingsWrite
	settingsWrite = func(string, map[string]any) error { return errors.New("planted write failure") }
	t.Cleanup(func() { settingsWrite = prevWrite })

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, envFile, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    childEnv,
	})
	if code != 1 {
		t.Fatalf("exit code = %d; want 1 on a settings write failure", code)
	}
	if err == nil {
		t.Fatal("a settings write failure must be surfaced, not swallowed")
	}
	if !strings.Contains(err.Error(), "settings") {
		t.Errorf("the error must name the settings write: %v", err)
	}
	// No child started: the helper would have written both dumps.
	if _, statErr := os.Stat(envFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the child must not have run (env dump exists: %v)", statErr)
	}
	if _, statErr := os.Stat(argsFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the child must not have run (args dump exists: %v)", statErr)
	}
	// No settings document was written anywhere under the sandbox USER HOME
	// (where the per-session app-data-dir settings.json would land), and the
	// mesh was stopped. Scanning the user home (not the child's own temp dir)
	// makes this assertion able to fail.
	if hits := countFilesNamed(t, userHome, "settings.json"); len(hits) != 0 {
		t.Errorf("no settings file may be written on this path: %v", hits)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}
	if err := startedMesh.Healthz(context.Background()); err == nil {
		t.Error("the mesh must be stopped after a settings write failure")
	}
}

// TestLaunchP2_MeshStartFailureExit1 covers the refuse-to-launch class of the
// exit-code map: a mesh that cannot be bound is exit 1 with the error, and no
// child, no settings write.
func TestLaunchP2_MeshStartFailureExit1(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	swapMeshStart(t, func(mesh.Config) (*mesh.Mesh, error) {
		return nil, errors.New("planted bind failure")
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, envFile, _ := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    childEnv,
	})
	if code != 1 {
		t.Fatalf("exit code = %d; want 1 on a mesh start failure", code)
	}
	if err == nil || !strings.Contains(err.Error(), "refusing to launch") {
		t.Fatalf("the error must state the refuse-to-launch: %v", err)
	}
	if _, statErr := os.Stat(envFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the child must not have run: %v", statErr)
	}
	if hit := findFile(t, userHome, "settings.json"); hit != "" {
		t.Errorf("a refused launch must not write a settings file: %s", hit)
	}
}

// ---------------------------------------------------------------------------
// LaunchP2: the gates that fall back to a TRUE native session
// ---------------------------------------------------------------------------

// TestLaunchP2_WirecheckNOGOFallsBackToNative proves the never-half-configured
// rule at the first gate: a NOGO verdict prints a clear notice and runs a TRUE
// native session (scrubbed env, no mesh, no settings write).
func TestLaunchP2_WirecheckNOGOFallsBackToNative(t *testing.T) {
	agyHome, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, p2NogoReport())

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P2_HELPER_MODE=dumpenv", "GO_P2_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "NOGO") || !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("the notice must state NOGO + native fallback: %q", notice.String())
	}
	childEnvMap := readEnvFile(t, envFile)
	if _, ok := childEnvMap["GOOGLE_GEMINI_BASE_URL"]; ok {
		t.Error("the native fallback must not set GOOGLE_GEMINI_BASE_URL")
	}
	if _, ok := childEnvMap["GEMINI_API_KEY"]; ok {
		t.Error("the native fallback must not inject GEMINI_API_KEY")
	}
	if childEnvMap["AGY_PRO_ACTIVE"] != "1" {
		t.Errorf("AGY_PRO_ACTIVE = %q; want 1 even on the fallback path", childEnvMap["AGY_PRO_ACTIVE"])
	}
	for _, root := range []string{agyHome, userHome} {
		if hit := findFile(t, root, "settings.json"); hit != "" {
			t.Errorf("the NOGO fallback must not write a settings file: %s", hit)
		}
	}
}

// TestLaunchP2_ProfileP2NotOKFallsBackToNative pins that the gate is
// PROFILE-SPECIFIC: a report that is GO for P1 but incomplete for P2 must still
// fall back, because the P2 canaries are the ones this session depends on.
func TestLaunchP2_ProfileP2NotOKFallsBackToNative(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	// goReport() carries only P1 canaries: Verdict() is GO, ProfileOK("P2") is false.
	if !goReportOKForP1Only() {
		t.Fatal("the fixture must be GO for P1 and not OK for P2")
	}
	swapWirecheck(t, goReport())

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P2_HELPER_MODE=dumpenv", "GO_P2_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("a P2-incomplete report must fall back to native: %q", notice.String())
	}
	// FIX-7: a GO-but-not-ProfileOK report must NOT say "NOGO" (the verdict is
	// GO); it renders the distinct missing-P2-canaries notice instead.
	if strings.Contains(notice.String(), "NOGO") {
		t.Errorf("a GO-but-not-ProfileOK report must not claim NOGO: %q", notice.String())
	}
	if !strings.Contains(notice.String(), "no P2 canaries") {
		t.Errorf("a GO-but-not-ProfileOK report must state the missing-P2-canaries reason: %q", notice.String())
	}
	if _, ok := readEnvFile(t, envFile)["GOOGLE_GEMINI_BASE_URL"]; ok {
		t.Error("the fallback must not set GOOGLE_GEMINI_BASE_URL")
	}
	if hit := findFile(t, userHome, "settings.json"); hit != "" {
		t.Errorf("the fallback must not write a settings file: %s", hit)
	}
}

// goReportOKForP1Only reports whether the shared P1 fixture is GO overall but
// not ProfileOK for P2 (the shape this test needs).
func goReportOKForP1Only() bool {
	r := goReport()
	return r.Verdict() == wirecheck.VerdictGO && !r.ProfileOK(ProfileP2.WirecheckName())
}

func TestLaunchP2_ZeroExternalsFallsBackToNative(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, emptyRegistryJSON)
	swapWirecheck(t, goP2Report())

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P2_HELPER_MODE=dumpenv", "GO_P2_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "nothing to serve") || !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("the notice must state no externals + native fallback: %q", notice.String())
	}
	if _, ok := readEnvFile(t, envFile)["GOOGLE_GEMINI_BASE_URL"]; ok {
		t.Error("the zero-externals fallback must not set GOOGLE_GEMINI_BASE_URL")
	}
	if hit := findFile(t, userHome, "settings.json"); hit != "" {
		t.Errorf("the zero-externals fallback must not write a settings file: %s", hit)
	}
}

func TestLaunchP2_VersionDriftProceedsWithNotice(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	r := goP2Report()
	r.Version = "9.9.9"
	r.VersionDrift = true
	swapWirecheck(t, r)

	var startedMesh *mesh.Mesh
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P2_HELPER_MODE=dumpenv", "GO_P2_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) - drift with GO canaries must proceed", code, err)
	}
	if !strings.Contains(notice.String(), "version drift") {
		t.Errorf("the drift must be surfaced in the notice: %q", notice.String())
	}
	if strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("drift with GO canaries must NOT fall back to native: %q", notice.String())
	}
	// The mesh path really ran: the child carries the mesh URL.
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}
	if got := readEnvFile(t, envFile)["GOOGLE_GEMINI_BASE_URL"]; got != startedMesh.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q; want %q (the drift path still runs the mesh)", got, startedMesh.URL())
	}
}

// TestLaunchP2_ResolveFailureIsSurfaced proves a real-agy resolution failure
// returns (1, err), never a silent native run.
func TestLaunchP2_ResolveFailureIsSurfaced(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	swapRealagy(t, "", errors.New("real agy binary not found"))

	code, err := LaunchP2(LaunchOptions{Args: nil})
	if err == nil {
		t.Fatal("a resolution failure must be surfaced, not swallowed")
	}
	if code != 1 {
		t.Errorf("exit code = %d; want 1 on a resolution failure", code)
	}
}

// ---------------------------------------------------------------------------
// LaunchP2: the REAL wirecheck gate against go-built stubs
// ---------------------------------------------------------------------------

// TestLaunchP2_RealStubMissingCanariesGatesToNative plants a real,
// canary-missing stub "agy" via AGY_PRO_REAL_AGY and lets LaunchP2 resolve it
// and run the REAL wirecheck (no seam): the stub is missing every P2 canary, so
// the verdict is NOGO and the session falls back to native.
func TestLaunchP2_RealStubMissingCanariesGatesToNative(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	stub := buildP1Stub(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)

	// Prove the stub really is NOGO for P2 before relying on it.
	if wirecheck.Check().ProfileOK(ProfileP2.WirecheckName()) {
		t.Fatal("the stub must be missing the P2 canaries for this test to be meaningful")
	}

	var notice bytes.Buffer
	envFile := filepath.Join(t.TempDir(), "child.env")
	// RealAgyPath empty: LaunchP2 resolves the real agy via realagy.Resolve()
	// (AGY_PRO_REAL_AGY), the real wirecheck scans it (NOGO), and the session
	// falls back to native.
	code, err := LaunchP2(LaunchOptions{
		Args:     nil,
		Stderr:   &notice,
		ChildEnv: []string{"P1_STUB_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	if !strings.Contains(notice.String(), "NOGO") {
		t.Errorf("the notice must state the NOGO verdict from the real wirecheck: %q", notice.String())
	}
	if !strings.Contains(notice.String(), "falling back to native") {
		t.Errorf("the notice must state the native fallback: %q", notice.String())
	}
	childEnvMap := readEnvFile(t, envFile)
	if _, ok := childEnvMap["GOOGLE_GEMINI_BASE_URL"]; ok {
		t.Error("the native fallback must not set GOOGLE_GEMINI_BASE_URL")
	}
	if hit := findFile(t, userHome, "settings.json"); hit != "" {
		t.Errorf("the NOGO fallback must not write a settings file: %s", hit)
	}
}

// TestLaunchP2_RealStubWithP2CanariesProceeds is the GO direction of the same
// real gate: a stub carrying all 8 P2 canary literals and a pinned --version
// passes the REAL wirecheck (no seam), so LaunchP2 runs the mesh path: the
// settings document is written and the child carries the gemini-mode env.
func TestLaunchP2_RealStubWithP2CanariesProceeds(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	stub := buildP2Stub(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)

	// Prove the stub passes the real P2 gate before relying on it.
	real := wirecheck.Check()
	if real.Verdict() != wirecheck.VerdictGO {
		t.Fatalf("the stub must be GO for this test to be meaningful: %v", real.Reasons())
	}
	if !real.ProfileOK(ProfileP2.WirecheckName()) {
		t.Fatal("the stub must satisfy the P2 canaries for this test to be meaningful")
	}

	envFile := filepath.Join(t.TempDir(), "child.env")
	code, err := LaunchP2(LaunchOptions{
		Args:     nil,
		ChildEnv: []string{"P2_STUB_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) on the mesh path", code, err)
	}
	childEnvMap := readEnvFile(t, envFile)
	if got := childEnvMap["GOOGLE_GEMINI_BASE_URL"]; !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q; want the mesh URL", got)
	}
	nonce := childEnvMap["GEMINI_API_KEY"]
	if len(nonce) != 64 {
		t.Errorf("GEMINI_API_KEY = %q; want a 64-char install nonce", nonce)
	}
	settings := countFilesNamed(t, userHome, "settings.json")
	if len(settings) != 1 {
		t.Fatalf("settings.json files = %v; want exactly 1 (the per-session app-data dir)", settings)
	}
	if doc := readSettingsDoc(t, settings[0]); doc.ModelProvider != "gemini" {
		t.Errorf("modelProvider = %q; want %q", doc.ModelProvider, "gemini")
	}
}

// buildP2Stub compiles a tiny helper binary that carries all 8 P2 canary
// literals and answers `--version` with the pinned version, so the REAL
// wirecheck gate evaluates it as GO for profile P2. It dumps its environment to
// P2_STUB_ENVFILE when set.
func buildP2Stub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const stubSrc = `package main

import (
	"fmt"
	"os"
	"strings"
)

// canaryBlob carries every wirecheck P2 canary literal (wirecheck/canaries.go)
// as a substring, so the real scan finds them all. It is referenced by a
// reachable branch: a blank assignment is dead-code-eliminated and the literals
// would not survive into the binary.
const canaryBlob = "gemini-api:blade:evergreen:beyond-api:google-genai-sdk/GOOGLE_GEMINI_BASE_URLGEMINI_API_KEYmodelProvider"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Fprintln(os.Stdout, "agy 1.3.5")
			return
		}
	}
	if os.Getenv("P2_STUB_PRINT_CANARIES") != "" {
		fmt.Fprintln(os.Stdout, canaryBlob)
		return
	}
	if p := os.Getenv("P2_STUB_ENVFILE"); p != "" {
		var b strings.Builder
		for _, e := range os.Environ() {
			b.WriteString(e)
			b.WriteByte('\n')
		}
		_ = os.WriteFile(p, []byte(b.String()), 0o600)
	}
	fmt.Fprintln(os.Stderr, "stub agy (p2 canaries)")
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
// LaunchP2: sandbox isolation (the real user home is never touched)
// ---------------------------------------------------------------------------

func TestLaunchP2_SandboxCanaryUntouched(t *testing.T) {
	_, userHome := sandboxP2(t)
	// A canary in the fake real home: if anything resolves paths through the
	// user profile instead of the sandbox, the canary directory gains files.
	canaryDir := filepath.Join(userHome, ".agy-pro")
	if err := os.MkdirAll(canaryDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	canary := filepath.Join(canaryDir, "CANARY")
	if err := os.WriteFile(canary, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    []string{"GO_P2_HELPER_MODE=exit", "GO_P2_HELPER_EXIT=0"},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
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
	// The mesh state stays inside the sandboxed agy-pro home.
	if hit := treeContains(t, userHome, "wireadp.json"); hit != "" {
		t.Fatalf("mesh state leaked into the user home: %s", hit)
	}
}

// TestP2ChildArgs_ValuelessModelFlagGoesLast pins the pathological case: a
// valueless --model is preserved at the END of the argv, AFTER the confinement
// flag, so agy's flag parser reports "flag needs an argument" instead of
// swallowing the --app_data_dir value as the model.
func TestP2ChildArgs_ValuelessModelFlagGoesLast(t *testing.T) {
	got := p2ChildArgs([]string{"-p", "hi", "--model"}, "agy-hybrid-test", "http://127.0.0.1:4444", nil)
	want := []string{"--app_data_dir", "agy-hybrid-test", "-p", "hi", "--model"}
	if len(got) != len(want) {
		t.Fatalf("args = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v; want %v", got, want)
		}
	}
}

// keysOfDoc returns the customModels keys of a parsed document.
func keysOfDoc(doc settingsDocShape) []string {
	out := make([]string, 0, len(doc.CustomModels))
	for k := range doc.CustomModels {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// round5a FIX pins
// ---------------------------------------------------------------------------

// p2SlashRegistryJSON carries a slash-bearing model id ("openai/gpt-4o") plus a
// normal one, so the FIX-1 exact-slug pin (a raw id ParseModelSpec would
// otherwise strip to "gpt-4o") can be asserted end-to-end.
const p2SlashRegistryJSON = `{"providers":[
  {"name":"provnw","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"ext-nw","contextLength":300000}]},
  {"name":"provslash","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"SLASH_API_KEY","models":[{"id":"openai/gpt-4o","contextLength":300000}]}
]}`

// FIX-1: p2ModelKey must produce a blade: key whose slug is byte-equal to the id
// p2Externals keyed into the settings doc, for a slash-bearing raw id, a picker
// label, a provider-slash spelling, and a bare id; a custom-scheme value passes
// through; an unknown id is a best-effort blade: key.
func TestP2ModelKey_RegistryExactSlug(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, p2SlashRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	const meshURL = "http://127.0.0.1:55555"
	// The exact keys p2Externals emits into the settings doc.
	docKeys := map[string]bool{}
	for _, e := range p2Externals(reg, meshURL, nil) {
		docKeys[e.URL] = true
	}
	cases := []struct {
		name  string
		input string
		want  string
		inDoc bool
	}{
		{"slash-bearing raw id", "openai/gpt-4o", "blade:127.0.0.1:55555/models/openai/gpt-4o", true},
		{"slash-bearing trimmed", "  openai/gpt-4o  ", "blade:127.0.0.1:55555/models/openai/gpt-4o", true},
		{"slash-in-label picker spelling", "openai/gpt-4o (provslash)", "blade:127.0.0.1:55555/models/openai/gpt-4o", true},
		{"picker label form", "ext-nw (provnw)", "blade:127.0.0.1:55555/models/ext-nw", true},
		{"provider slash form", "provnw/ext-nw", "blade:127.0.0.1:55555/models/ext-nw", true},
		{"bare id", "ext-nw", "blade:127.0.0.1:55555/models/ext-nw", true},
		{"custom scheme passthrough", "blade:127.0.0.1:1/models/other", "blade:127.0.0.1:1/models/other", false},
		{"unknown id best-effort", "no-such-model", "blade:127.0.0.1:55555/models/no-such-model", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p2ModelKey(reg, tc.input, meshURL)
			if got != tc.want {
				t.Fatalf("p2ModelKey(%q) = %q; want %q", tc.input, got, tc.want)
			}
			if tc.inDoc && !docKeys[got] {
				t.Errorf("p2ModelKey(%q) = %q is not a customModels key p2Externals emitted: %v", tc.input, got, docKeys)
			}
		})
	}
}

// FIX-1: a provider-qualified static id (label or slash spelling) must resolve
// to the BARE static id, never a blade: key (a static id hidden in a qualified
// spelling is still a static-id question served by StaticIDTarget).
func TestP2ModelKey_StaticIDReturnsBareID(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, p2RegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	const meshURL = "http://127.0.0.1:55555"
	for _, input := range []string{
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-high (provstatic)",
		"provstatic/gemini-3.8-flash-high",
	} {
		t.Run(input, func(t *testing.T) {
			got := p2ModelKey(reg, input, meshURL)
			if got != "gemini-3.8-flash-high" {
				t.Fatalf("p2ModelKey(%q) = %q; want the bare static id %q (NOT a blade key)", input, got, "gemini-3.8-flash-high")
			}
		})
	}
}

// FIX-2: the injected front-door flags come FIRST, so a real flag.FlagSet (agy
// parses with Go's flag package) always registers --app_data_dir even with a
// leading positional, a -- terminator, or a trailing valueless value-flag. A
// valueless value-flag must produce a clean parse error, never silently drop the
// confinement flag.
func TestP2ChildArgs_FlagsParseBeforePositionals(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	const dir = "agy-hybrid-test"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"leading positional subcommand", []string{"models"}, false},
		{"leading positional with arg", []string{"agents", "x"}, false},
		{"double-dash terminator", []string{"--", "-p", "hi"}, false},
		{"trailing valueless value-flag", []string{"-p"}, true},
		{"model plus prompt", []string{"--model", "openai/gpt-4o", "-p", "hi"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := p2ChildArgs(tc.args, dir, meshURL, reg)
			fs := flag.NewFlagSet("agy", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			appDataDir := fs.String("app_data_dir", "", "")
			fs.String("model", "", "")
			fs.String("p", "", "") // agy's value-taking -p
			err := fs.Parse(out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("a valueless -p must produce a clean parse error, never silently drop --app_data_dir: %v", out)
				}
			} else if err != nil {
				t.Fatalf("fs.Parse(%v) = %v; want nil", out, err)
			}
			if *appDataDir != dir {
				t.Errorf("app_data_dir = %q; want %q (the front-door flag must be consumed first)\nargs: %v", *appDataDir, dir, out)
			}
		})
	}
}

// FIX-3: an empty --model value ("--model=", "-m=", or an empty quoted value) is
// forwarded valueless as the last token so agy rejects it cleanly, never a
// silent static-default session; a last-wins empty clears a real pin.
func TestP2ChildArgs_EmptyModelValueForwardsValueless(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"equals empty", []string{"--model="}},
		{"short equals empty", []string{"-m="}},
		{"space empty", []string{"--model", ""}},
		{"last-wins empty clears a real pin", []string{"--model=ext-nw", "--model="}},
		{"last-wins empty quoted clears a real pin", []string{"--model", "ext-nw", "--model", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p2ChildArgs(tc.args, "agy-hybrid-test", meshURL, reg)
			if v := argValue(got, "--app_data_dir"); v != "agy-hybrid-test" {
				t.Fatalf("--app_data_dir = %q; want agy-hybrid-test\nargs: %v", v, got)
			}
			n := 0
			for _, a := range got {
				if a == "--model" || a == "-m" {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("want exactly one valueless model selector; got %d: %v", n, got)
			}
			if last := got[len(got)-1]; last != "--model" && last != "-m" {
				t.Fatalf("the empty model selector must be the last token (valueless); got %q\nargs: %v", last, got)
			}
		})
	}
}

// FIX-4: a READABLE registry that does not carry a provider must still scrub
// that provider's known-preset key NAME, so an exported preset key VALUE never
// rides into the child (the S-3 class).
func TestLaunchP2_ReadableRegistryStillScrubsPresetKeys(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON) // readable; no openai provider
	swapWirecheck(t, goP2Report())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-planted-openai-secret-value")
	childEnv, envFile, _ := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if v, ok := readEnvFile(t, envFile)["OPENAI_API_KEY"]; ok {
		t.Errorf("a readable registry must still scrub the known-preset key OPENAI_API_KEY (FIX-4); got %q", v)
	}
	if hit := treeContains(t, filepath.Dir(envFile), "sk-planted-openai-secret-value"); hit != "" {
		t.Errorf("the planted preset key leaked into the sandbox: %s", hit)
	}
}

// FIX-5: opts.ChildEnv is a caller-controlled channel appended BEFORE the two
// injected gemini-mode vars, so it can never override the mesh base URL or the
// install nonce under Go 1.22 last-wins exec dedup.
func TestLaunchP2_ChildEnvCannotOverrideInjectedVars(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())

	var startedMesh *mesh.Mesh
	var capturedAuthKey string
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		capturedAuthKey = cfg.AuthKey
		m, err := mesh.Start(cfg)
		startedMesh = m
		return m, err
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, envFile, _ := p2HelperChildEnv(t)
	childEnv = append(childEnv,
		"GOOGLE_GEMINI_BASE_URL=http://attacker.example/",
		"GEMINI_API_KEY=attacker-supplied-key-value",
	)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was not started")
	}
	childEnvMap := readEnvFile(t, envFile)
	if got := childEnvMap["GOOGLE_GEMINI_BASE_URL"]; got != startedMesh.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q; want the mesh URL %q (ChildEnv must not override it, FIX-5)", got, startedMesh.URL())
	}
	if got := childEnvMap["GEMINI_API_KEY"]; got != capturedAuthKey {
		t.Errorf("GEMINI_API_KEY = %q; want the install nonce %q (ChildEnv must not override it, FIX-5)", got, capturedAuthKey)
	}
}

// FIX-6: the native-fallback notice is masked + control-stripped and its
// registry source is class-labeled, so a registry path under the fake user home
// never reaches stderr (no C:\Users\ substring, no control bytes).
func TestLaunchP2_NativeFallbackNoticeMasksPaths(t *testing.T) {
	_, userHome := sandboxP2(t)
	// A registry path under the fake user home that fails to read, so the
	// registry-load error (which embeds the absolute path) feeds the notice.
	t.Setenv(providers.EnvProviders, filepath.Join(userHome, "missing-providers.json"))
	swapWirecheck(t, goP2Report())

	var notice bytes.Buffer
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "child.env")
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		Stderr:      &notice,
		ChildEnv:    []string{"GO_P2_HELPER_MODE=dumpenv", "GO_P2_HELPER_ENVFILE=" + envFile},
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) after native fallback", code, err)
	}
	out := notice.String()
	// The intended path was exercised: the registry-read fallback (not NOGO or
	// zero-externals), whose notice names the registry class.
	if !strings.Contains(out, "provider registry could not be read") {
		t.Fatalf("the test must exercise the registry-read fallback notice: %q", out)
	}
	if strings.Contains(out, userHome) {
		t.Errorf("the fallback notice must not echo the user home path: %q", out)
	}
	if strings.Contains(out, `C:\Users\`) {
		t.Errorf("the fallback notice must not echo a C:\\Users\\ path: %q", out)
	}
	for _, r := range out {
		if r != '\n' && r != '\t' && r < 0x20 {
			t.Errorf("the fallback notice must not carry control bytes: %q", out)
			break
		}
	}
}

// FIX-8a (FIX-55 parity): LaunchP2 must resolve the real agy ONCE and use the
// SAME path for the wirecheck gate and the exec (no TOCTOU), mirroring the P1
// proof. The gate path is captured (P2's shared swapWirecheck discards it, so
// the seam is swapped inline here).
func TestLaunchP2_ResolvesOnceGateAndExecAgree(t *testing.T) {
	sandboxP2(t)
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
		return goP2Report()
	}
	t.Cleanup(func() { wirecheckCheck = prevCheck })

	childEnv, envFile, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		// RealAgyPath empty forces the resolve-once path.
		Args:     p2SelfHelperArgs(),
		ChildEnv: childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if calls != 1 {
		t.Errorf("realagyResolve called %d times; want 1 (resolve ONCE, FIX-55)", calls)
	}
	if gatedPath != self {
		t.Errorf("the gate scanned %q; want the resolved path %q (same binary as the exec, FIX-55)", gatedPath, self)
	}
	// The exec ran the resolved binary: the child argv carries the confinement
	// flag and its env carries the injected mesh URL (the mesh path ran).
	if v := argValue(readArgsFile(t, argsFile), "--app_data_dir"); v == "" {
		t.Errorf("the exec should have run the resolved binary: %v", readArgsFile(t, argsFile))
	}
	if _, ok := readEnvFile(t, envFile)["GOOGLE_GEMINI_BASE_URL"]; !ok {
		t.Error("the exec should have run the resolved binary (GOOGLE_GEMINI_BASE_URL present)")
	}
}

// FIX-8b: a mesh that starts then is immediately stopped makes m.EnsureHealthy
// fail, which is a refuse-to-launch: exit 1, no child exec'd, no settings write.
func TestLaunchP2_EnsureHealthyFailureRefusesToLaunch(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, goP2Report())
	var startedMesh *mesh.Mesh
	swapMeshStart(t, func(cfg mesh.Config) (*mesh.Mesh, error) {
		m, err := mesh.Start(cfg)
		if err != nil {
			return nil, err
		}
		_ = m.Stop() // up then immediately down: the front-door re-check must fail
		startedMesh = m
		return m, nil
	})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, envFile, _ := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgs(),
		ChildEnv:    childEnv,
	})
	if code != 1 {
		t.Fatalf("exit code = %d; want 1 when EnsureHealthy fails (refuse to launch)", code)
	}
	if err == nil {
		t.Fatal("an EnsureHealthy failure must be surfaced, not swallowed")
	}
	if _, statErr := os.Stat(envFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the child must not have run (env dump exists: %v)", statErr)
	}
	if startedMesh == nil {
		t.Fatal("the mesh was started")
	}
	if hit := findFile(t, userHome, "settings.json"); hit != "" {
		t.Errorf("a refused launch must not write a settings file: %s", hit)
	}
}

// FIX-8e: a static-only registry (every id is a reserved static Gemini id) still
// runs the mesh path (an enabled model exists), the settings doc carries NO
// customModels entry, and a pinned static id reaches the child verbatim (served
// via the static-id policy).
func TestLaunchP2_StaticOnlyRegistryServesViaPolicy(t *testing.T) {
	_, userHome := sandboxP2(t)
	writeRegistry(t, staticOnlyRegistryJSON)
	swapWirecheck(t, goP2Report())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel("gemini-3.8-flash-high"),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) on the mesh path", code, err)
	}
	if got := argValue(readArgsFile(t, argsFile), "--model"); got != "gemini-3.8-flash-high" {
		t.Fatalf("--model = %q; want the static id verbatim (served via the policy)", got)
	}
	settings := countFilesNamed(t, userHome, "settings.json")
	if len(settings) != 1 {
		t.Fatalf("settings.json files = %v; want exactly 1", settings)
	}
	doc := readSettingsDoc(t, settings[0])
	if doc.ModelProvider != "gemini" {
		t.Errorf("modelProvider = %q; want gemini", doc.ModelProvider)
	}
	if len(doc.CustomModels) != 0 {
		t.Errorf("customModels = %v; want none (the only registry id is a reserved static id)", keysOfDoc(doc))
	}
}

// FIX-9: customModelSchemes must stay in lock-step with the scheme literals
// agysettings' customModels key validator accepts (its customModelKeyRegex is
// unexported). Each scheme must build a key agysettings validates as a custom
// model URL, so a scheme added or removed on either side fails here instead of
// silently rejecting a real --model pin.
func TestCustomModelSchemesMatchAgysettings(t *testing.T) {
	for _, scheme := range customModelSchemes {
		if !strings.HasSuffix(scheme, ":") {
			t.Errorf("scheme %q must end in ':'", scheme)
		}
		key := scheme + "127.0.0.1:1/models/probe"
		entries := []agysettings.ExternalEntry{{URL: key, ModelName: key, DisplayName: "probe (prov)"}}
		if _, err := agysettings.BuildP2Settings(entries); err != nil {
			t.Errorf("scheme %q does not build a valid agysettings customModels key: %v", scheme, err)
		}
		if !hasCustomModelScheme(key) {
			t.Errorf("hasCustomModelScheme must recognize %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// round5a-fix wave 2 pins (R5AF2-1..6)
// ---------------------------------------------------------------------------

// R5AF2-1: the front door OWNS --app_data_dir (the per-session V6 confinement).
// A caller copy in any spelling must be stripped so it cannot override the
// injected per-session dir under Go-flag last-wins (which would defeat the
// settings isolation while gemini-mode env is still injected). Asserted with a
// real flag.FlagSet parse (the last-wins VALUE), which the first-match argValue
// helper cannot see.
func TestP2ChildArgs_CallerAppDataDirStripped(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	const dir = "agy-hybrid-test"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"space form", []string{"--app_data_dir", "callerX", "-p", "hi"}},
		{"equals form", []string{"--app_data_dir=callerX"}},
		{"single-dash space form", []string{"-app_data_dir", "callerX"}},
		{"single-dash equals form", []string{"-app_data_dir=callerX"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := p2ChildArgs(tc.args, dir, meshURL, reg)
			n := 0
			for _, a := range out {
				if a == "--app_data_dir" || a == "-app_data_dir" ||
					strings.HasPrefix(a, "--app_data_dir=") || strings.HasPrefix(a, "-app_data_dir=") {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("want exactly one --app_data_dir (the injected one); got %d: %v", n, out)
			}
			fs := flag.NewFlagSet("agy", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			appDataDir := fs.String("app_data_dir", "", "")
			fs.String("model", "", "")
			fs.String("p", "", "")
			if err := fs.Parse(out); err != nil {
				t.Fatalf("fs.Parse(%v) = %v; want nil", out, err)
			}
			if *appDataDir != dir {
				t.Errorf("app_data_dir = %q; want the injected per-session dir %q (a caller copy must not win, R5AF2-1)", *appDataDir, dir)
			}
		})
	}
}

// R5AF2-1: every dash-variant model spelling Go's flag package treats as
// --model / -m (-model, --m, and their = forms) must be stripped and rebuilt
// into the single canonical --model pin; no stray dash-model token may survive.
func TestP2ChildArgs_DashVariantModelSpellings(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	const wantKey = "blade:127.0.0.1:4444/models/ext-nw"
	for _, spelling := range []string{"--model", "-model", "--m", "-m"} {
		for _, form := range []string{"space", "equals"} {
			t.Run(spelling+" "+form, func(t *testing.T) {
				var args []string
				if form == "space" {
					args = []string{spelling, "ext-nw", "-p", "hi"}
				} else {
					args = []string{spelling + "=ext-nw", "-p", "hi"}
				}
				out := p2ChildArgs(args, "agy-hybrid-test", meshURL, reg)
				for _, a := range out {
					if a == "-model" || a == "--m" || a == "-m" ||
						strings.HasPrefix(a, "-model=") || strings.HasPrefix(a, "--m=") || strings.HasPrefix(a, "-m=") {
						t.Fatalf("a stray dash-variant model flag survived: %q in %v", a, out)
					}
				}
				fs := flag.NewFlagSet("agy", flag.ContinueOnError)
				fs.SetOutput(io.Discard)
				fs.String("app_data_dir", "", "")
				model := fs.String("model", "", "")
				fs.String("p", "", "")
				if err := fs.Parse(out); err != nil {
					t.Fatalf("fs.Parse(%v) = %v", out, err)
				}
				if *model != wantKey {
					t.Errorf("model = %q; want the canonical pin %q (a dash-variant spelling must rebuild it)", *model, wantKey)
				}
			})
		}
	}
}

// R5AF2-3: only the LAST model selector decides (Go last-wins). A valued last
// selector pins that value and forwards NO valueless marker; a valueless last
// selector forwards the valueless marker so agy rejects it cleanly. This closes
// the empty-then-value edge where a stale valueless marker neutralized a later
// valid pin.
func TestP2ChildArgs_LastSelectorWinsValueless(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	const nwKey = "blade:127.0.0.1:4444/models/ext-nw"
	cases := []struct {
		name        string
		args        []string
		wantPin     string
		wantValLess bool
	}{
		{"empty then value (space)", []string{"--model=", "--model", "ext-nw"}, nwKey, false},
		{"empty then value (equals)", []string{"--model=", "--model=ext-nw"}, nwKey, false},
		{"empty alone", []string{"--model="}, "", true},
		{"value then empty", []string{"--model", "ext-nw", "--model="}, "", true},
		{"value with prompt", []string{"-p", "hi", "--model", "ext-nw"}, nwKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := p2ChildArgs(tc.args, "agy-hybrid-test", meshURL, reg)
			if v := argValue(out, "--model"); v != tc.wantPin {
				t.Fatalf("--model = %q; want %q\nargs: %v", v, tc.wantPin, out)
			}
			last := out[len(out)-1]
			isValLess := last == "--model" || last == "-m" || last == "-model" || last == "--m"
			if isValLess != tc.wantValLess {
				t.Fatalf("valueless-marker-last = %v; want %v\nargs: %v", isValLess, tc.wantValLess, out)
			}
		})
	}
}

// R5AF2-2: an exact raw-id / label match must beat a ParseModelSpec-stripped
// candidate match regardless of registry order. Pinning the slash-bearing
// "openai/gpt-4o" must resolve to the openai entry (slug "openai/gpt-4o"), never
// an earlier or later stripped "gpt-4o" the slash-strip would have matched.
func TestP2ModelKey_ExactRawIDBeatsStrippedCandidate(t *testing.T) {
	const meshURL = "http://127.0.0.1:55555"
	const want = "blade:127.0.0.1:55555/models/openai/gpt-4o"
	registries := map[string]string{
		"exact first": `{"providers":[
			{"name":"provopenai","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"OPENAI_API_KEY","models":[{"id":"openai/gpt-4o","contextLength":1000}]},
			{"name":"provother","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"OTHER_API_KEY","models":[{"id":"gpt-4o","contextLength":2000}]}
		]}`,
		"exact last": `{"providers":[
			{"name":"provother","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"OTHER_API_KEY","models":[{"id":"gpt-4o","contextLength":2000}]},
			{"name":"provopenai","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"OPENAI_API_KEY","models":[{"id":"openai/gpt-4o","contextLength":1000}]}
		]}`,
	}
	for name, regJSON := range registries {
		t.Run(name, func(t *testing.T) {
			reg, err := providers.LoadFrom(mustWriteRegistry(t, regJSON))
			if err != nil {
				t.Fatalf("load registry: %v", err)
			}
			if got := p2ModelKey(reg, "openai/gpt-4o", meshURL); got != want {
				t.Fatalf("p2ModelKey(openai/gpt-4o) = %q; want %q (the exact entry, not the stripped gpt-4o)", got, want)
			}
		})
	}
}

// R5AF2-5: a wirecheck NOGO whose reasons carry a ScanErr that embeds the
// real-agy absolute path must NOT echo that path (providers.MaskOperand exempts
// locations). The path-bearing reason is withheld; non-path reasons survive.
func TestP2NogoNotice_ScanErrPathWithheld(t *testing.T) {
	report := wirecheck.Report{
		BinaryPath: filepath.Join("fake", "agy.exe"),
		ScanErr:    `read C:\Users\someuser\.agy-pro\bin\agy.exe: Access is denied`,
		VersionErr: "probe timed out", // forces VerdictNOGO so the reasons render
	}
	if report.Verdict() != wirecheck.VerdictNOGO {
		t.Fatal("the fixture must be NOGO for the reasons to render")
	}
	notice := p2NogoNotice(report)
	if strings.Contains(notice, `C:\Users\`) {
		t.Errorf("the NOGO notice must not echo the real-agy path from a ScanErr: %q", notice)
	}
	if !strings.Contains(notice, "withheld") {
		t.Errorf("the notice must state a path-bearing reason was withheld: %q", notice)
	}
	if !strings.Contains(notice, "probe timed out") {
		t.Errorf("the non-path reason must survive verbatim: %q", notice)
	}
}

// R5AF2-6: drive the audit-log sink from a test that issues a real mesh request
// (so <agyHome>/logs/wireadp-*.jsonl is actually written), then grep it for the
// planted provider-key VALUE and the live install nonce. The round-5a sweep ran
// in a request-less test, so the audit file never existed and this half could
// not fail.
func TestP2AuditLog_HidesKeyValueAndNonce(t *testing.T) {
	agyHome, _ := sandboxP2(t)
	up := newRecordingUpstream(t)
	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"provnw","baseURL":"%s/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"ext-nw","contextLength":300000}]}]}`, up.server.URL))
	const plantedKey = "sk-planted-nw-secret-value"
	t.Setenv("NW_API_KEY", plantedKey)

	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cfg, err := buildP2Config(reg, agyHome)
	if err != nil {
		t.Fatalf("buildP2Config: %v", err)
	}
	m, err := mesh.Start(mesh.Config{Config: cfg})
	if err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	// One real request (with the nonce) so the audit log is written.
	req, err := http.NewRequest(http.MethodPost,
		m.URL()+"/v1beta/models/ext-nw:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", cfg.AuthKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = readAllAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	logsDir := filepath.Join(agyHome, "logs")
	auditFiles := 0
	if entries, rerr := os.ReadDir(logsDir); rerr == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "wireadp-") && strings.HasSuffix(e.Name(), ".jsonl") {
				auditFiles++
			}
		}
	}
	if auditFiles == 0 {
		t.Fatal("no wireadp-*.jsonl audit file was written after a mesh request")
	}
	if hit := treeContains(t, logsDir, plantedKey); hit != "" {
		t.Errorf("the planted key VALUE leaked into the audit log: %s", hit)
	}
	if hit := treeContains(t, logsDir, cfg.AuthKey); hit != "" {
		t.Errorf("the install nonce leaked into the audit log: %s", hit)
	}
}

// ---------------------------------------------------------------------------
// round5a-fix wave-3 pins (R5AF3-1..5)
// ---------------------------------------------------------------------------

// R5AF3-1: extractChildSelectors models Go's "--" terminator. Once a bare "--"
// is seen it and every subsequent token are copied verbatim into rest, so a
// caller's post-"--" literal data is never deleted and a post-"--"
// --app_data_dir / --model cannot become a parsed flag. The injected
// --app_data_dir is emitted first and is the only parsed one.
func TestP2ChildArgs_DoubleDashTerminatorPreserved(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	const dir = "agy-hybrid-test"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	cases := []struct {
		name     string
		args     []string
		wantTail []string
	}{
		{
			name:     "post-dash app_data_dir and model are positionals",
			args:     []string{"--app_data_dir", "x", "--", "-m", "--app_data_dir", "evil"},
			wantTail: []string{"--", "-m", "--app_data_dir", "evil"},
		},
		{
			name:     "post-dash model literal is not the pin",
			args:     []string{"-p", "hi", "--", "--model", "literal"},
			wantTail: []string{"-p", "hi", "--", "--model", "literal"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := p2ChildArgs(tc.args, dir, meshURL, reg)
			want := append([]string{"--app_data_dir", dir}, tc.wantTail...)
			if len(out) != len(want) {
				t.Fatalf("args = %v; want %v", out, want)
			}
			for i := range want {
				if out[i] != want[i] {
					t.Fatalf("args = %v; want %v", out, want)
				}
			}
			// A real flag.FlagSet parse confirms the injected app_data_dir wins
			// and the post-"--" tokens are positionals (Go last-wins + terminator).
			fs := flag.NewFlagSet("agy", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			appDataDir := fs.String("app_data_dir", "", "")
			fs.String("model", "", "")
			fs.String("p", "", "")
			if err := fs.Parse(out); err != nil {
				t.Fatalf("fs.Parse(%v) = %v; want nil", out, err)
			}
			if *appDataDir != dir {
				t.Errorf("app_data_dir = %q; want the injected dir %q (a post-\"--\" app_data_dir is a positional)", *appDataDir, dir)
			}
		})
	}
}

// R5AF3-3: a whitespace-only --model value routes to the valueless branch (a
// valueless marker is forwarded, agy rejects cleanly) rather than emitting an
// empty --model value that agy would silently accept and fall to a default.
func TestP2ChildArgs_WhitespaceModelValueIsValueless(t *testing.T) {
	const meshURL = "http://127.0.0.1:4444"
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	for _, args := range [][]string{
		{"--model", "   "},
		{"-m", " "},
		{"--model=   "},
	} {
		t.Run(strings.Join(args, "|"), func(t *testing.T) {
			out := p2ChildArgs(args, "agy-hybrid-test", meshURL, reg)
			if v := argValue(out, "--model"); v != "" {
				t.Fatalf("a whitespace-only value must not pin --model; got %q\nargs: %v", v, out)
			}
			last := out[len(out)-1]
			if last != "--model" && last != "-m" && last != "-model" && last != "--m" {
				t.Fatalf("a valueless model marker must be forwarded as the last token; got %q\nargs: %v", last, out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): the P2 customModels drop reasons quote the
// registry provider NAME and model ID. Both are registry slots, so each
// renders through providers.MaskSlot (the registry-slot authority) at the
// composition site; the p2DropSink keeps its whole-sentence MaskOperand as
// defense in depth. Benign values keep their exact bytes.
// ---------------------------------------------------------------------------

func TestP2DropReasonsMaskRegistrySlots(t *testing.T) {
	const (
		plant     = testfixtures.SKLive
		plantMask = "sk_li****"
	)
	collect := func() (*strings.Builder, func(string, ...any)) {
		var log strings.Builder
		return &log, func(format string, args ...any) {
			log.WriteString(fmt.Sprintf(format, args...))
			log.WriteString("\n")
		}
	}

	log, logf := collect()
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: plant, BaseURL: "https://plant.example/v1", Models: []providers.Model{
			{ID: plant, ContextLength: 32768},
			{ID: "", ContextLength: 32768},
		}},
		{Name: "acme", BaseURL: "https://acme.example/v1", Models: []providers.Model{
			{ID: plant, ContextLength: 32768},
		}},
	}}
	p2Externals(reg, "http://127.0.0.1:55555", logf)
	lines := log.String()
	if strings.Contains(lines, plant) {
		t.Errorf("P2 drop reason leaks a registry slot raw:\n%s", lines)
	}
	if want := `hybrid P2: dropped a model from provider "` + plantMask + `": blank id`; !strings.Contains(lines, want) {
		t.Errorf("blank-id drop reason must mask the provider name as %q:\n%s", plantMask, lines)
	}
	if want := `dropped model "` + plantMask + `" from provider "acme": id already registered by an earlier provider`; !strings.Contains(lines, want) {
		t.Errorf("duplicate-id drop reason must mask the model id as %q:\n%s", plantMask, lines)
	}

	// Benign corpus: both operands keep their exact bytes, and the generated
	// entries are unaffected.
	blog, blogf := collect()
	benign := &providers.Registry{Providers: []providers.Provider{
		{Name: "nw", BaseURL: "https://api.neuralwatt.example/v1", Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", ContextLength: 32768},
		}},
		{Name: "acme-corp", BaseURL: "https://api.acme-corp.example/v1", Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", ContextLength: 32768},
			{ID: "", ContextLength: 32768},
		}},
	}}
	entries := p2Externals(benign, "http://127.0.0.1:55555", blogf)
	for _, want := range []string{
		`hybrid P2: dropped a model from provider "acme-corp": blank id`,
		`dropped model "glm-5.3-flash-flex" from provider "acme-corp": id already registered by an earlier provider`,
	} {
		if !strings.Contains(blog.String(), want) {
			t.Errorf("benign drop reason lost its exact bytes (%q):\n%s", want, blog.String())
		}
	}
	if strings.Contains(blog.String(), "****") {
		t.Errorf("a benign drop reason was masked:\n%s", blog.String())
	}
	if len(entries) == 0 {
		t.Fatal("the benign registry produced no customModels entry")
	}
}
