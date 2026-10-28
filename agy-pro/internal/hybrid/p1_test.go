package hybrid

import (
	"bytes"
	"context"
	"encoding/json"
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

	"agypro/internal/mesh"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/wirecheck"
)

// ---------------------------------------------------------------------------
// shared sandbox + fixtures
// ---------------------------------------------------------------------------

// twoExtRegistryJSON is a deterministic two-provider registry. Each provider
// carries one enabled external referenced by an env-var NAME only, so the child
// env scrub can be asserted (NW_API_KEY / CHARM_API_KEY must never reach the
// child). The model ids are benign (not on the deny-list).
const twoExtRegistryJSON = `{"providers":[
  {"name":"provnw","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"ext-nw","contextLength":300000}]},
  {"name":"provcharm","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"CHARM_API_KEY","models":[{"id":"ext-charm","contextLength":300000}]}
]}`

// emptyRegistryJSON has no providers: zero enabled externals.
const emptyRegistryJSON = `{"providers":[]}`

// sandboxP1 points AGY_PRO_HOME, AGY_PRO_PROVIDERS and the user home at fresh
// temp dirs and clears every override that could redirect resolution or trip the
// recursion guard, so no test can touch the real ~/.agy-pro or the real user
// profile. It returns the agy-pro home and the fake user home.
func sandboxP1(t *testing.T) (agyHome, userHome string) {
	t.Helper()
	agyHome = t.TempDir()
	t.Setenv(paths.EnvAgyProHome, agyHome)
	userHome = t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", userHome)
	} else {
		t.Setenv("HOME", userHome)
	}
	t.Setenv(EnvHybridProfile, "")
	t.Setenv(paths.EnvAgyProRealAgy, "")
	t.Setenv(paths.EnvAgyProActive, "")
	t.Setenv("CLOUD_CODE_URL", "")
	clearEnv(t, "CLOUD_CODE_URL")
	clearEnv(t, "GEMINI_API_KEY")
	clearEnv(t, "GOOGLE_API_KEY")
	clearEnv(t, "GO_P1_HELPER_MODE")
	clearEnv(t, "P1_STUB_ENVFILE")
	clearEnv(t, "P1_STUB_MODE")
	return agyHome, userHome
}

// clearEnv unsets key for the duration of the test and restores it afterwards.
func clearEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		}
	})
}

// writeRegistry writes a providers.json to a temp file and points
// AGY_PRO_PROVIDERS at it, so providers.Load reads the sandbox registry.
func writeRegistry(t *testing.T, json string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(p, []byte(json), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	t.Setenv(providers.EnvProviders, p)
}

// readEnvFile parses a KEY=VALUE dump (one per line) into a map.
func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env dump %s: %v", path, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if i := strings.IndexByte(line, '='); i >= 0 {
			out[line[:i]] = line[i+1:]
		}
	}
	return out
}

// treeContains returns the first file under root whose bytes contain needle, or
// "" when none does.
func treeContains(t *testing.T, root, needle string) string {
	t.Helper()
	hit := ""
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(data), needle) {
			hit = path
		}
		return nil
	})
	return hit
}

// findFile returns the first file named name under root, or "" when none.
func findFile(t *testing.T, root, name string) string {
	t.Helper()
	hit := ""
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == name {
			hit = path
		}
		return nil
	})
	return hit
}

// ---------------------------------------------------------------------------
// seam swappers
// ---------------------------------------------------------------------------

func swapWirecheck(t *testing.T, r wirecheck.Report) {
	t.Helper()
	prev := wirecheckCheck
	wirecheckCheck = func(string) wirecheck.Report { return r }
	t.Cleanup(func() { wirecheckCheck = prev })
}

func swapRealagy(t *testing.T, path string, err error) {
	t.Helper()
	prev := realagyResolve
	realagyResolve = func() (string, error) { return path, err }
	t.Cleanup(func() { realagyResolve = prev })
}

func swapMeshStart(t *testing.T, fn func(mesh.Config) (*mesh.Mesh, error)) {
	t.Helper()
	prev := meshStart
	meshStart = fn
	t.Cleanup(func() { meshStart = prev })
}

func swapMeshRunChild(t *testing.T, fn func(context.Context, *mesh.Mesh, *exec.Cmd) error) {
	t.Helper()
	prev := meshRunChild
	meshRunChild = fn
	t.Cleanup(func() { meshRunChild = prev })
}

// goReport is a synthetic wirecheck report whose P1 canaries are all Found, so
// Verdict() is GO and ProfileOK("P1") is true.
func goReport() wirecheck.Report {
	return wirecheck.Report{
		BinaryPath: filepath.Join("fake", "agy.exe"),
		Version:    wirecheck.PinnedVersion,
		Canaries: []wirecheck.Canary{
			{Profile: wirecheck.ProfileP1, Name: "cloud_code_url_env", Literal: "CLOUD_CODE_URL", Found: true},
			{Profile: wirecheck.ProfileP1, Name: "cloud_code_url_override_log", Literal: "Overriding CloudCodeServerURL via CLOUD_CODE_URL environment variable", Found: true},
			{Profile: wirecheck.ProfileP1, Name: "route_fetch_available_models", Literal: "/v1internal:fetchAvailableModels", Found: true},
			{Profile: wirecheck.ProfileP1, Name: "route_stream_generate_content", Literal: "/v1internal:streamGenerateContent", Found: true},
			{Profile: wirecheck.ProfileP1, Name: "route_load_code_assist", Literal: "/v1internal:loadCodeAssist", Found: true},
			{Profile: wirecheck.ProfileP1, Name: "plan_model_gate", Literal: "plan model not specified", Found: true},
		},
	}
}

// nogoReport is a synthetic wirecheck report with one P1 canary missing, so
// Verdict() is NOGO.
func nogoReport() wirecheck.Report {
	return wirecheck.Report{
		BinaryPath: filepath.Join("fake", "agy.exe"),
		Version:    wirecheck.PinnedVersion,
		Canaries: []wirecheck.Canary{
			{Profile: wirecheck.ProfileP1, Name: "cloud_code_url_env", Literal: "CLOUD_CODE_URL", Found: false},
		},
	}
}

// ---------------------------------------------------------------------------
// buildP1Config / nonce / catalog options
// ---------------------------------------------------------------------------

func TestBuildP1Config_StrictNonceAndSeams(t *testing.T) {
	agyHome, _ := sandboxP1(t)
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	// Pre-build the catalog once (FIX-48): buildP1Config serves these bytes.
	want, err := BuildFetchAvailableModels(reg, DefaultCatalogOptions())
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	cfg, err := buildP1Config(reg, agyHome, want)
	if err != nil {
		t.Fatalf("buildP1Config: %v", err)
	}

	// Strict mode is unconditional: AuthKey is a non-empty 64-char hex nonce
	// (32 random bytes). The permissive mode must never be reachable here.
	if cfg.AuthKey == "" {
		t.Fatal("AuthKey must never be empty on the P1 path (strict mode is mandatory)")
	}
	if len(cfg.AuthKey) != 64 {
		t.Fatalf("AuthKey length = %d; want 64 hex chars (32 bytes)", len(cfg.AuthKey))
	}
	for _, c := range cfg.AuthKey {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("AuthKey is not lowercase hex: %q", cfg.AuthKey)
		}
	}

	// The catalog seam serves the PRE-BUILT bytes (no per-request rebuild).
	if cfg.CatalogFunc == nil {
		t.Fatal("CatalogFunc must be set")
	}
	got, err := cfg.CatalogFunc()
	if err != nil {
		t.Fatalf("CatalogFunc: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("catalog seam bytes differ:\n got: %s\nwant: %s", got, want)
	}

	// The resolver seam resolves an enabled external and misses an unknown one.
	if cfg.ResolveModel == nil {
		t.Fatal("ResolveModel must be set")
	}
	if target, ok, err := cfg.ResolveModel("ext-nw"); err != nil || !ok || target.Model.ID != "ext-nw" {
		t.Fatalf("ResolveModel(ext-nw) = (%q, %v, %v); want (ext-nw, true, nil)", target.Model.ID, ok, err)
	}
	if _, ok, _ := cfg.ResolveModel("no-such-model"); ok {
		t.Fatal("ResolveModel must miss an unknown model")
	}

	// Redact and AuditPath are wired; Generate is nil (wireadp installs its
	// default generation handler).
	if cfg.Redact == nil {
		t.Fatal("Redact must be set")
	}
	if cfg.Generate != nil {
		t.Fatal("Generate must be nil so wireadp installs its default handler")
	}
	if !strings.HasPrefix(cfg.AuditPath, filepath.Join(agyHome, "logs")) {
		t.Fatalf("AuditPath = %q; want it under %s", cfg.AuditPath, filepath.Join(agyHome, "logs"))
	}
	if _, err := os.Stat(filepath.Join(agyHome, "logs")); err != nil {
		t.Fatalf("the logs directory must be created: %v", err)
	}
}

func TestNewInstallNonce_UniqueHex(t *testing.T) {
	a, err := newInstallNonce()
	if err != nil {
		t.Fatalf("newInstallNonce: %v", err)
	}
	b, err := newInstallNonce()
	if err != nil {
		t.Fatalf("newInstallNonce: %v", err)
	}
	if len(a) != 64 || len(b) != 64 {
		t.Fatalf("nonce lengths = %d, %d; want 64, 64", len(a), len(b))
	}
	if a == b {
		t.Fatal("two nonces must differ (per-spawn freshness)")
	}
}

func TestDefaultCatalogOptions_SkipsReservedEnums(t *testing.T) {
	opts := DefaultCatalogOptions()
	if opts.ReservedEnums == nil || !opts.ReservedEnums[326] || !opts.ReservedEnums[330] {
		t.Fatal("DefaultCatalogOptions must carry the reserved-enum set (326, 330, ...)")
	}
	// The other knobs keep their catalog.go defaults.
	if opts.EnumBase != 0 || opts.GroupLabel != "" {
		t.Fatalf("DefaultCatalogOptions must leave EnumBase/GroupLabel at their defaults; got %d / %q", opts.EnumBase, opts.GroupLabel)
	}
}

// ---------------------------------------------------------------------------
// catalog through the mesh + strict auth
// ---------------------------------------------------------------------------

// TestCatalogThroughMesh_BytesMatchAndStrictAuth starts a real mesh over a temp
// registry (loopback httptest upstream), then POSTs
// /v1internal:fetchAvailableModels: without the install nonce the mesh answers
// 401 (strict mode); with it the bytes equal hybrid.BuildFetchAvailableModels.
func TestCatalogThroughMesh_BytesMatchAndStrictAuth(t *testing.T) {
	agyHome, _ := sandboxP1(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"prov","baseURL":"%s/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"ext-alpha","contextLength":300000}]}]}`, upstream.URL))
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	want, err := BuildFetchAvailableModels(reg, DefaultCatalogOptions())
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	cfg, err := buildP1Config(reg, agyHome, want)
	if err != nil {
		t.Fatalf("buildP1Config: %v", err)
	}
	m, err := mesh.Start(mesh.Config{Config: cfg})
	if err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	// FIX-45: /v1internal runs the passive-receipt posture (RequireV1InternalAuth
	// = false), so a bearer-only request with NO install nonce is accepted (the
	// real-agy CodeAssist client presents only its keyring bearer). 200 + the
	// catalog bytes.
	noNonce, err := http.Post(m.URL()+"/v1internal:fetchAvailableModels", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST (no nonce): %v", err)
	}
	body, _ := io.ReadAll(noNonce.Body)
	noNonce.Body.Close()
	if noNonce.StatusCode != http.StatusOK {
		t.Fatalf("bearer-only fetchAvailableModels status = %d; want 200 (passive receipt, FIX-45)\n%s", noNonce.StatusCode, body)
	}
	if string(body) != string(want) {
		t.Errorf("catalog bytes through the mesh differ:\n got: %s\nwant: %s", body, want)
	}

	// The strict install nonce still guards /healthz: no nonce -> 401, with the
	// nonce -> 200 (the shim's own probes carry it).
	hz, err := http.Get(m.URL() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz (no nonce): %v", err)
	}
	hz.Body.Close()
	if hz.StatusCode != http.StatusUnauthorized {
		t.Errorf("healthz without nonce = %d; want 401 (strict nonce stays on /healthz)", hz.StatusCode)
	}
	req, err := http.NewRequest(http.MethodGet, m.URL()+"/healthz", nil)
	if err != nil {
		t.Fatalf("new healthz request: %v", err)
	}
	req.Header.Set("X-Agy-Pro-Install", cfg.AuthKey)
	hz2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz (nonce): %v", err)
	}
	hz2.Body.Close()
	if hz2.StatusCode != http.StatusOK {
		t.Errorf("healthz with nonce = %d; want 200", hz2.StatusCode)
	}
}

// TestBearerOnlyChildDrivesMeshEndToEnd is the FIX-45 integration pin whose
// absence let the strict-auth defect ship past the unit tests into G4. A fake
// child that presents ONLY a keyring bearer (Authorization: Bearer …, NO install
// nonce) drives a REAL mesh end to end: fetchAvailableModels returns the catalog
// bytes AND a generation round-trip reaches the loopback upstream and returns a
// Gemini-shaped response. This is exactly the real-agy CodeAssist credential
// path (the G4 report's loadCodeAssist-401 blocker).
func TestBearerOnlyChildDrivesMeshEndToEnd(t *testing.T) {
	agyHome, _ := sandboxP1(t)

	// A loopback OpenAI-compatible upstream (the provider endpoint) that records
	// each call and streams one SSE completion.
	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		writeOpenAISSE(w)
	}))
	defer upstream.Close()

	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"mockprov","baseURL":"%s/v1","apiKeyEnv":"MOCK_API_KEY","models":[{"id":"ext-alpha","contextLength":300000}]}]}`, upstream.URL))
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	// The mesh resolves the provider key from its env-var name at request time.
	t.Setenv("MOCK_API_KEY", "planted-mock-key")

	want, err := BuildFetchAvailableModels(reg, DefaultCatalogOptions())
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	cfg, err := buildP1Config(reg, agyHome, want)
	if err != nil {
		t.Fatalf("buildP1Config: %v", err)
	}
	m, err := mesh.Start(mesh.Config{Config: cfg})
	if err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	// The fake child presents ONLY its keyring bearer, never the install nonce.
	// Content-Type: application/json is set because the adapter's /v1internal RPC
	// routes require it (the real CodeAssist client sends it too).
	bearer := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer planted-keyring-bearer")
		req.Header.Set("Content-Type", "application/json")
	}

	// 1. fetchAvailableModels returns the catalog bytes (passive receipt).
	req, err := http.NewRequest(http.MethodPost, m.URL()+"/v1internal:fetchAvailableModels", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new fetchAvailableModels request: %v", err)
	}
	bearer(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetchAvailableModels: %v", err)
	}
	catBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer-only fetchAvailableModels status = %d; want 200 (FIX-45)\n%s", resp.StatusCode, catBody)
	}
	if string(catBody) != string(want) {
		t.Errorf("catalog bytes differ:\n got: %s\nwant: %s", catBody, want)
	}

	// 2. A generation round-trip succeeds through the mesh (upstream reached).
	envelope := map[string]any{
		"project": "p", "model": "ext-alpha",
		"request": map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}},
	}
	env, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	req2, err := http.NewRequest(http.MethodPost, m.URL()+"/v1internal:streamGenerateContent?alt=sse", bytes.NewReader(env))
	if err != nil {
		t.Fatalf("new generate request: %v", err)
	}
	bearer(req2)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("streamGenerateContent: %v", err)
	}
	gen, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("bearer-only generation status = %d; want 200 (FIX-45)\n%s", resp2.StatusCode, gen)
	}
	if atomic.LoadInt32(&upstreamCalls) == 0 {
		t.Fatal("the generation round-trip never reached the upstream")
	}
	if !strings.Contains(string(gen), "data:") {
		t.Errorf("the generation response must be SSE:\n%s", gen)
	}
	if !strings.Contains(string(gen), "PROBE_OK") {
		t.Errorf("the generation response must carry the upstream content:\n%s", gen)
	}
}

// writeOpenAISSE streams one OpenAI chat.completion SSE completion (the shape the
// mesh translates to the Gemini /v1internal envelope).
func writeOpenAISSE(w http.ResponseWriter) {
	frame := func(chunk map[string]any) {
		p, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", p)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	frame(map[string]any{"id": "c", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
	frame(map[string]any{"id": "c", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "PROBE_OK"}, "finish_reason": nil}}})
	frame(map[string]any{"id": "c", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// mustWriteRegistry writes a registry file and returns its path.
func mustWriteRegistry(t *testing.T, json string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(p, []byte(json), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	return p
}

// TestPaddedIDListsAndRoutesToSameTarget is the FIX-49 pin: the catalog keys an
// entry by TrimSpace(m.ID), so a whitespace-padded registry id is listed under
// its trimmed key AND the resolver must compare the same trimmed form or the
// padded id would 404 on the wire (listed but unroutable).
func TestPaddedIDListsAndRoutesToSameTarget(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "prov",
		Models: []providers.Model{{ID: "  ext-padded  ", ContextLength: 300000}},
	}}}
	data, err := BuildFetchAvailableModels(reg, DefaultCatalogOptions())
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	if !strings.Contains(string(data), `"ext-padded":`) {
		t.Fatalf("the padded id must be listed under its trimmed key:\n%s", data)
	}
	// The wire form (the trimmed catalog key) must resolve to the same target.
	target, ok, err := RegistryResolver(reg)("ext-padded")
	if err != nil || !ok {
		t.Fatalf("resolve(ext-padded) = (%q, %v, %v); want the target, true, nil", target.Model.ID, ok, err)
	}
	if strings.TrimSpace(target.Model.ID) != "ext-padded" {
		t.Fatalf("resolved id = %q; want the padded-id target", target.Model.ID)
	}
}

// TestCatalogStripsControlCharsFromLabels is the FIX-51 pin: registry strings
// flow into agy's picker as display_name and group labels, so control characters
// (\r\n here) must be stripped, leaving neither the raw bytes nor their escaped
// JSON forms.
func TestCatalogStripsControlCharsFromLabels(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "prov\r\ninjected",
		Models: []providers.Model{{ID: "ext-x", ContextLength: 300000}},
	}}}
	data, err := BuildFetchAvailableModels(reg, DefaultCatalogOptions())
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	if strings.ContainsAny(string(data), "\r\n") {
		t.Fatalf("raw control characters reached the catalog JSON:\n%q", data)
	}
	if strings.Contains(string(data), "\\r") || strings.Contains(string(data), "\\n") {
		t.Fatalf("escaped control characters reached the catalog JSON (labels not stripped):\n%s", data)
	}
	if !strings.Contains(string(data), "provinjected") {
		t.Fatalf("the control-stripped label must be present:\n%s", data)
	}
}

// TestControlCharIDListsStrippedAndRoutes is the FIX-64 pin: a registry model id
// carrying control characters is normalized once (trim + strip) so it is LISTED
// under the stripped key (catalog map key + agent_model_sorts.model_ids), the
// catalog JSON carries no control characters (raw or escaped), and the resolver
// routes the stripped wire form to the SAME target.
func TestControlCharIDListsStrippedAndRoutes(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "prov",
		Models: []providers.Model{{ID: "ext-\r\nalpha", ContextLength: 300000}},
	}}}
	data, err := BuildFetchAvailableModels(reg, DefaultCatalogOptions())
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	if strings.ContainsAny(string(data), "\r\n") {
		t.Fatalf("raw control characters reached the catalog JSON:\n%q", data)
	}
	if strings.Contains(string(data), "\\r") || strings.Contains(string(data), "\\n") {
		t.Fatalf("escaped control characters reached the catalog JSON (id not stripped):\n%s", data)
	}
	if !strings.Contains(string(data), `"ext-alpha":`) {
		t.Fatalf("the id must be listed under its control-stripped key:\n%s", data)
	}
	// The stripped wire form routes to the same (control-char-id) target.
	target, ok, err := RegistryResolver(reg)("ext-alpha")
	if err != nil || !ok {
		t.Fatalf("resolve(ext-alpha) = (%q, %v, %v); want the target, true, nil", target.Model.ID, ok, err)
	}
	if stripControlChars(strings.TrimSpace(target.Model.ID)) != "ext-alpha" {
		t.Fatalf("resolved id = %q; want the control-char-id target", target.Model.ID)
	}
}

// TestP1VersionTokenMirrorsWirecheck is the FIX-62 pin: p1VersionToken must
// equal wirecheck's version-pattern literal (wirecheck/version.go) so a drift in
// THIS copy is caught. (wirecheck's rule is unexported; exporting it is the
// follow-up that would pin the other direction.) The parse behavior is pinned too.
func TestP1VersionTokenMirrorsWirecheck(t *testing.T) {
	const wirecheckVersionPattern = `\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.-]+)?`
	if p1VersionToken.String() != wirecheckVersionPattern {
		t.Fatalf("p1VersionToken = %q; want it to mirror wirecheck's %q", p1VersionToken.String(), wirecheckVersionPattern)
	}
	if got := p1VersionToken.FindString("agy 1.3.5 (build 7)"); got != "1.3.5" {
		t.Fatalf("parse of a decorated banner = %q; want 1.3.5", got)
	}
}

// TestP1VersionProbeOutputCap is the FIX-62 pin: the probe's capture is bounded
// at wirecheck's 4 KiB cap, an over-cap write sets overflow (and the probe then
// fails), and Write never blocks the child (it reports the full length consumed).
func TestP1VersionProbeOutputCap(t *testing.T) {
	if versionProbeOutputCap != 4<<10 {
		t.Fatalf("versionProbeOutputCap = %d; want 4096 (mirror wirecheck's 4 KiB)", versionProbeOutputCap)
	}
	over := &cappedOutput{}
	big := strings.Repeat("x", versionProbeOutputCap+16)
	n, err := over.Write([]byte(big))
	if err != nil || n != len(big) {
		t.Fatalf("over-cap Write = (%d, %v); want (%d, nil) so the child never blocks", n, err, len(big))
	}
	if !over.overflow {
		t.Error("an over-cap write must set overflow (the probe then fails)")
	}
	under := &cappedOutput{}
	if _, err := under.Write([]byte(strings.Repeat("y", 32))); err != nil {
		t.Fatalf("under-cap Write: %v", err)
	}
	if under.overflow {
		t.Error("an under-cap write must not overflow")
	}
}
