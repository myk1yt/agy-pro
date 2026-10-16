package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/wireadp"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// testRegistry is an in-memory registry: no providers.json, no real network.
// The provider's key is referenced by env-var NAME only; the value is never
// part of the mesh layer's business (wireadp resolves it at request time).
func testRegistry() *providers.Registry {
	return &providers.Registry{
		Providers: []providers.Provider{{
			Name:      "mock-provider",
			BaseURL:   "http://127.0.0.1:9/v1",
			APIKeyEnv: "GO_MESH_TEST_PROVIDER_KEY_NAME",
			Models: []providers.Model{
				{ID: "probe-gemini", ContextLength: 1048576, MaxOutputTokens: 65536},
			},
		}},
	}
}

// sandboxHome points AGY_PRO_HOME and the user home at fresh temp dirs so no
// test can touch the real ~/.agy-pro or the real user profile. It returns the
// agy-pro home and the fake user home.
func sandboxHome(t *testing.T) (agyHome, userHome string) {
	t.Helper()
	agyHome = t.TempDir()
	t.Setenv(paths.EnvAgyProHome, agyHome)
	userHome = t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", userHome)
	} else {
		t.Setenv("HOME", userHome)
	}
	return agyHome, userHome
}

func meshConfig(t *testing.T, home string) Config {
	t.Helper()
	return Config{
		Config: wireadp.Config{
			Registry:  testRegistry(),
			AuditPath: filepath.Join(home, "audit.jsonl"),
		},
	}
}

func startMesh(t *testing.T, cfg Config) *Mesh {
	t.Helper()
	m, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	return m
}

// swapStartServer replaces the wireadp.Start seam for one test and returns a
// function that puts the real one back.
func swapStartServer(t *testing.T, fn func(wireadp.Config) (*wireadp.Server, error)) func() {
	t.Helper()
	prev := startServer
	startServer = fn
	restore := func() { startServer = prev }
	t.Cleanup(restore)
	return restore
}

// treeContains returns the first file under root whose bytes contain needle,
// or "" when none does.
func treeContains(t *testing.T, root, needle string) string {
	t.Helper()
	hit := ""
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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

func portFromURL(t *testing.T, rawURL string) string {
	t.Helper()
	trimmed := strings.TrimPrefix(rawURL, "http://")
	idx := strings.LastIndex(trimmed, ":")
	if idx < 0 {
		t.Fatalf("cannot parse a port out of %q", rawURL)
	}
	return trimmed[idx+1:]
}

// scopeDirEntries lists the mesh scope directories directly under the
// agy-pro home (FIX-54: there must never be more than one).
func scopeDirEntries(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", home, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "mesh-") {
			names = append(names, e.Name())
		}
	}
	return names
}

// readScopeRecord returns the deterministic scope directory's discovery
// record. The mesh itself never reads it; the tests assert where it lands.
func readScopeRecord(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, meshScopeDirName, wireadpRecordFile))
	if err != nil {
		t.Fatalf("the scope record must exist at %s: %v", filepath.Join(home, meshScopeDirName, wireadpRecordFile), err)
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// Start / lifetime
// ---------------------------------------------------------------------------

func TestStart_EphemeralLoopbackPortAndHealthyMesh(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))

	if !strings.HasPrefix(m.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL %q is not loopback", m.URL())
	}
	if m.Port() <= 0 || m.Port() > 65535 {
		t.Fatalf("port %d out of range", m.Port())
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", m.Port()), 2*time.Second)
	if err != nil {
		t.Fatalf("the mesh port is not reachable on loopback: %v", err)
	}
	_ = conn.Close()

	if err := m.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("EnsureHealthy on a fresh mesh: %v", err)
	}
	if err := m.Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz on a fresh mesh: %v", err)
	}

	// The singleton record for THIS mesh stays inside the sandbox
	// AGY_PRO_HOME, in the one deterministic scope directory.
	records, err := filepath.Glob(filepath.Join(home, meshScopeDirName, wireadpRecordFile))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	found := false
	for _, rec := range records {
		if data, readErr := os.ReadFile(rec); readErr == nil && strings.Contains(string(data), fmt.Sprintf("%d", m.Port())) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no discovery record for port %d in the scope directory (got %v)", m.Port(), records)
	}
}

func TestStart_TwoStartsGiveDifferentPorts(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)

	m1 := startMesh(t, cfg)
	first := m1.Port()
	if err := m1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m1.Healthz(context.Background()); err == nil {
		t.Fatal("a stopped mesh must not answer healthz")
	}

	m2 := startMesh(t, cfg)
	if m2.Port() == first {
		t.Fatalf("the second spawn reused port %d; per-spawn means a fresh ephemeral port", first)
	}
	if err := m2.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("EnsureHealthy on the second spawn: %v", err)
	}
}

func TestStart_HealthGateFailureRefusesToLaunch(t *testing.T) {
	home, _ := sandboxHome(t)

	// A crippled instance: a real bind that is dead before the health gate
	// runs. The seam's wireadp.Start executes inside the supervisor's
	// per-spawn scope pin, so the instance looks owned (its discovery record
	// lands in the pinned directory) and only the gate can reject it.
	var crippled *wireadp.Server
	restore := swapStartServer(t, func(wireadp.Config) (*wireadp.Server, error) {
		srv, err := wireadp.Start(wireadp.Config{
			Registry:  testRegistry(),
			AuditPath: filepath.Join(home, "crippled-audit.jsonl"),
		})
		if err != nil {
			return nil, err
		}
		crippled = srv
		_ = srv.Stop()
		return srv, nil
	})

	cfg := meshConfig(t, home)
	cfg.HealthTimeout = 200 * time.Millisecond
	m, err := Start(cfg)
	if err == nil {
		t.Fatal("Start must refuse to launch when the health gate fails")
	}
	if m != nil {
		t.Fatal("a refused Start must not return a mesh")
	}
	if !strings.Contains(err.Error(), "refusing to launch") {
		t.Errorf("the error must read as a refuse-to-launch: %v", err)
	}
	if !strings.Contains(err.Error(), "health gate failed") {
		t.Errorf("the error must name the failed gate: %v", err)
	}
	if crippled == nil {
		t.Fatal("the seam did not run")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("port %d", crippled.Port())) {
		t.Errorf("the error must name the mesh port %d: %v", crippled.Port(), err)
	}

	// The failed spawn must not hold the one-live-mesh slot: a real spawn
	// still works afterwards.
	restore()
	real := startMesh(t, meshConfig(t, home))
	if err := real.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("a later spawn must still work: %v", err)
	}
}

func TestStart_PropagatesWireadpStartError(t *testing.T) {
	sandboxHome(t)
	swapStartServer(t, func(wireadp.Config) (*wireadp.Server, error) {
		return nil, errors.New("bind exploded")
	})
	m, err := Start(Config{})
	if err == nil {
		t.Fatal("Start must fail when wireadp fails")
	}
	if m != nil {
		t.Fatal("no mesh may be returned on a wireadp failure")
	}
	if !strings.Contains(err.Error(), "bind exploded") {
		t.Errorf("the wireadp error must survive the wrap: %v", err)
	}
}

func TestEnsureHealthy_StoppedMeshErrors(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	err := m.EnsureHealthy(context.Background())
	if err == nil {
		t.Fatal("EnsureHealthy on a stopped mesh must error")
	}
	if !strings.Contains(err.Error(), "refusing to launch") {
		t.Errorf("the error must read as a refuse-to-launch: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("port %d", m.Port())) {
		t.Errorf("the error must name the port %d: %v", m.Port(), err)
	}
	if !strings.Contains(err.Error(), m.URL()) {
		t.Errorf("the error must name the URL %s: %v", m.URL(), err)
	}
}

func TestStop_IsIdempotent(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))
	if err := m.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if !m.isStopped() {
		t.Fatal("the mesh must be marked stopped")
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", m.Port()), time.Second); err == nil {
		t.Fatal("the port must be closed after Stop")
	}
}

// ---------------------------------------------------------------------------
// singleton posture: per-spawn, never a foreign instance
// ---------------------------------------------------------------------------

func TestStart_RefusesWhileAnotherMeshIsLive(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)
	m1 := startMesh(t, cfg)

	_, err := Start(cfg)
	if err == nil {
		t.Fatal("a second live mesh in one process must be refused")
	}
	if !strings.Contains(err.Error(), "already serving") {
		t.Errorf("the error must explain the one-live-mesh rule: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("port %d", m1.Port())) {
		t.Errorf("the error must name the running mesh's port: %v", err)
	}
	if err := m1.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("the running mesh must be untouched: %v", err)
	}

	// Once the first mesh is stopped, a new spawn is allowed again.
	if err := m1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	m2 := startMesh(t, cfg)
	if err := m2.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("the replacement spawn must be healthy: %v", err)
	}
}

func TestStart_NeverAdoptsAForeignInProcessInstance(t *testing.T) {
	home, _ := sandboxHome(t)

	// A foreign wireadp singleton started directly (not by this supervisor),
	// the exact shape wireadp's adopt path would return.
	foreign, err := wireadp.Start(wireadp.Config{
		Registry:  testRegistry(),
		AuthKey:   "shared-nonce",
		AuditPath: filepath.Join(home, "foreign-audit.jsonl"),
	})
	if err != nil {
		t.Fatalf("foreign Start: %v", err)
	}
	t.Cleanup(func() { _ = foreign.Stop() })

	// The ownership probe runs under a per-attempt random nonce, so wireadp
	// refuses the adopt instead of returning the live singleton, and the
	// supervisor refuses to launch.
	cfg := meshConfig(t, home)
	cfg.AuthKey = "shared-nonce"
	if _, err := Start(cfg); err == nil {
		t.Fatal("the supervisor must refuse an instance it did not bind itself")
	} else if !strings.Contains(err.Error(), foreign.URL()) {
		t.Errorf("the error must name the foreign instance's URL: %v", err)
	}

	// The refused spawn opened no listener of its own: the scope record is
	// still the reservation (no URL), so nothing adoptable was left behind.
	if data, readErr := os.ReadFile(filepath.Join(home, meshScopeDirName, wireadpRecordFile)); readErr == nil {
		var rec struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(data, &rec) == nil && rec.URL != "" {
			t.Fatalf("a refused spawn left an adoptable record: %s", data)
		}
	}

	// The foreign instance is untouched and still serving under its nonce.
	if err := foreign.Healthz(context.Background()); err != nil {
		t.Fatalf("the foreign instance must survive the refusal: %v", err)
	}
}

// TestStart_UnusableScopeRecordNeverBlocksASpawn pins the round-3b FIX-42a
// scenario under the deterministic scope directory: the record wireadp leaves
// there is unusable (here: corrupt, standing in for the silent write
// failure). The supervisor must still start (it never reads the record), must
// close the ownership probe's own listener, and must not wedge: later spawns
// keep working.
func TestStart_UnusableScopeRecordNeverBlocksASpawn(t *testing.T) {
	home, _ := sandboxHome(t)

	var mu sync.Mutex
	var bound []*wireadp.Server
	swapStartServer(t, func(cfg wireadp.Config) (*wireadp.Server, error) {
		srv, err := wireadp.Start(cfg)
		if err != nil {
			return nil, err
		}
		// Corrupt the record the bind just wrote, standing in for a record
		// that never landed usable. Written, never deleted.
		_ = os.WriteFile(filepath.Join(os.Getenv(paths.EnvAgyProHome), wireadpRecordFile), []byte("{not json"), 0o600)
		mu.Lock()
		bound = append(bound, srv)
		mu.Unlock()
		return srv, nil
	})

	m1 := startMesh(t, meshConfig(t, home))
	if err := m1.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("an unusable scope record must not block a spawn: %v", err)
	}

	mu.Lock()
	servers := append([]*wireadp.Server(nil), bound...)
	mu.Unlock()
	if len(servers) != 2 {
		t.Fatalf("want the ownership probe bind plus the real bind, got %d", len(servers))
	}
	// The probe's listener is closed: no owned-but-unreferenced listener.
	if err := servers[0].Healthz(context.Background()); err == nil {
		t.Fatal("the ownership probe's listener must be closed")
	}
	// The real bind serves the mesh.
	if err := servers[1].Healthz(context.Background()); err != nil {
		t.Fatalf("the mesh's own listener must serve: %v", err)
	}

	// No wedge: stopping and starting again binds a fresh instance. The old
	// code leaked the first listener as wireadp's in-process singleton, so
	// every later Start adopted it and refused forever.
	if err := m1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	m2 := startMesh(t, meshConfig(t, home))
	if m2.Port() == m1.Port() {
		t.Fatalf("the second spawn reused port %d", m1.Port())
	}
	if err := m2.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("the second spawn must be healthy: %v", err)
	}
}

// TestStart_TwoSpawnsLeaveExactlyOneScopeDirectory pins FIX-54: the scope
// directory is deterministic and reused, so successive spawns in one
// AGY_PRO_HOME leave exactly one entry, with the record overwritten in place.
// The second spawn also proves the stale-record path: it binds behind a dead
// record left by the first spawn (and by the probe) without refusing.
func TestStart_TwoSpawnsLeaveExactlyOneScopeDirectory(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)

	before := scopeDirEntries(t, home)

	m1 := startMesh(t, cfg)
	if err := m1.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	if rec := readScopeRecord(t, home); !strings.Contains(rec, fmt.Sprintf("%d", m1.Port())) {
		t.Fatalf("the scope record must carry the first spawn's port %d: %s", m1.Port(), rec)
	}
	if err := m1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	m2 := startMesh(t, cfg)
	if m2.Port() == m1.Port() {
		t.Fatalf("the second spawn reused port %d; per-spawn means a fresh listener", m1.Port())
	}
	if err := m2.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("the second spawn must start behind the first spawn's dead record: %v", err)
	}
	if rec := readScopeRecord(t, home); !strings.Contains(rec, fmt.Sprintf("%d", m2.Port())) {
		t.Fatalf("the scope record must be overwritten in place with port %d: %s", m2.Port(), rec)
	}

	after := scopeDirEntries(t, home)
	if len(after) != len(before)+1 || after[0] != meshScopeDirName {
		t.Fatalf("want exactly one scope directory %q after two spawns, got %v (before %v)", meshScopeDirName, after, before)
	}
}

// TestStart_LiveSiblingRecordInTheScopeDirectoryIsIgnored pins the
// cross-process isolation the deterministic name must preserve (FIX-54): a
// live sibling spawn's record in the shared scope directory must neither be
// adopted nor refuse this spawn, because per-spawn means every spawn binds
// its own listener.
func TestStart_LiveSiblingRecordInTheScopeDirectoryIsIgnored(t *testing.T) {
	home, _ := sandboxHome(t)
	scopeDir := filepath.Join(home, meshScopeDirName)
	if err := os.MkdirAll(scopeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// A live adapter owned by "another process", recorded inside the shared
	// scope directory.
	siblingCmd, urlFile := newForeignAdapterChild(t, scopeDir)
	if err := siblingCmd.Start(); err != nil {
		t.Fatalf("sibling helper start: %v", err)
	}
	siblingURL := waitForFile(t, urlFile, 15*time.Second)

	m := startMesh(t, meshConfig(t, home))
	if m.URL() == siblingURL {
		t.Fatal("the sibling's instance was adopted")
	}
	if err := m.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("a live sibling record must not block this spawn: %v", err)
	}

	// The sibling is untouched and still serving.
	resp, err := http.Get(siblingURL + "/healthz")
	if err != nil {
		t.Fatalf("the sibling instance must survive: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the sibling instance's healthz = %d", resp.StatusCode)
	}
}

// TestNoDeletionCallsInThePackage greps the package's own sources: the mesh
// layer never deletes a file or directory (FIX-54 fixes accumulation by not
// creating it, not by cleaning up). The needles are assembled at runtime so
// this file cannot match itself.
func TestNoDeletionCallsInThePackage(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	needles := []string{"os." + "Remove", "os." + "RemoveAll", "Remove" + "-Item", "rm " + "-rf"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		src := string(data)
		for _, needle := range needles {
			if strings.Contains(src, needle) {
				t.Errorf("%s contains %q; the mesh layer never deletes", e.Name(), needle)
			}
		}
	}
}

// TestBuildProbeConfig_ForcesStrictV1InternalAuth pins FIX-66: the ownership
// probe's throwaway listener never runs the passive /v1internal posture, even
// when the session opted into it, and every other field is the session's.
func TestBuildProbeConfig_ForcesStrictV1InternalAuth(t *testing.T) {
	const sentinel = "PROBE-SENTINEL"
	parent := wireadp.Config{
		Registry:    testRegistry(),
		Redact:      func(string) string { return sentinel },
		AuthKey:     "session-nonce",
		CatalogFunc: func() ([]byte, error) { return []byte(sentinel), nil },
		RPCHooks: map[string]func(body []byte) ([]byte, error){
			"loadCodeAssist": func([]byte) ([]byte, error) { return []byte(sentinel), nil },
		},
		AuditPath:                filepath.Join("session", "audit.jsonl"),
		ResolveModel:             func(string) (providers.Target, bool, error) { return providers.Target{}, true, nil },
		Generate:                 wireadp.UnimplementedGenerate{},
		UpstreamFirstByteTimeout: 3 * time.Second,
		UpstreamIdleTimeout:      4 * time.Second,
		StreamKeepAliveInterval:  5 * time.Second,
		// The session's P1 passive opt-in: the probe must not inherit it.
		RequireV1InternalAuth: wireadp.BoolPtr(false),
	}

	probe := buildProbeConfig(Config{Config: parent}, "probe-nonce", filepath.Join("probe", "audit.jsonl"))

	if probe.RequireV1InternalAuth == nil || !*probe.RequireV1InternalAuth {
		t.Fatalf("the probe must force RequireV1InternalAuth = true, got %v", probe.RequireV1InternalAuth)
	}
	if probe.AuthKey != "probe-nonce" {
		t.Errorf("probe AuthKey = %q, want the per-attempt nonce", probe.AuthKey)
	}
	if probe.AuditPath != filepath.Join("probe", "audit.jsonl") {
		t.Errorf("probe AuditPath = %q, want the probe's own file", probe.AuditPath)
	}

	// Everything else is the session's config, carried over verbatim.
	if probe.Registry != parent.Registry {
		t.Error("the probe lost the session's registry")
	}
	if got := probe.Redact("anything"); got != sentinel {
		t.Errorf("probe Redact = %q, want the session's redactor", got)
	}
	if data, err := probe.CatalogFunc(); err != nil || string(data) != sentinel {
		t.Errorf("probe CatalogFunc = %q (%v), want the session's", data, err)
	}
	if data, err := probe.RPCHooks["loadCodeAssist"](nil); err != nil || string(data) != sentinel {
		t.Errorf("probe RPCHooks = %q (%v), want the session's", data, err)
	}
	if _, ok, err := probe.ResolveModel("probe-gemini"); err != nil || !ok {
		t.Errorf("probe ResolveModel = (%v, %v), want the session's resolver", ok, err)
	}
	if probe.Generate == nil {
		t.Error("the probe lost the session's generate seam")
	}
	if probe.UpstreamFirstByteTimeout != parent.UpstreamFirstByteTimeout ||
		probe.UpstreamIdleTimeout != parent.UpstreamIdleTimeout ||
		probe.StreamKeepAliveInterval != parent.StreamKeepAliveInterval {
		t.Error("the probe lost the session's stream timeouts")
	}

	// A session that never set the field (nil is already strict) still gets
	// the explicit pointer: the intent is unambiguous in the code.
	unset := buildProbeConfig(Config{Config: wireadp.Config{}}, "probe-nonce", "audit.jsonl")
	if unset.RequireV1InternalAuth == nil || !*unset.RequireV1InternalAuth {
		t.Fatalf("the probe must set the posture explicitly, got %v", unset.RequireV1InternalAuth)
	}
}

// TestStart_ProbeListenerNeverRunsPassivePosture pins FIX-66 end to end: the
// session opts into the passive /v1internal posture, yet the probe's
// throwaway listener (captured through the startServer seam) answers a
// no-credential /v1internal request with 401, and the session's own bind
// keeps the posture it was given.
func TestStart_ProbeListenerNeverRunsPassivePosture(t *testing.T) {
	home, _ := sandboxHome(t)

	type seen struct {
		cfg          wireadp.Config
		noCredential int
	}
	var mu sync.Mutex
	var binds []seen
	swapStartServer(t, func(cfg wireadp.Config) (*wireadp.Server, error) {
		srv, err := wireadp.Start(cfg)
		if err != nil {
			return nil, err
		}
		status := 0
		if len(binds) == 0 { // the ownership probe's throwaway listener
			resp, postErr := http.Post(srv.URL()+"/v1internal:generateContent", "application/json",
				strings.NewReader(`{"project":"p","model":"probe-gemini","request":{}}`))
			if postErr != nil {
				status = -1
			} else {
				status = resp.StatusCode
				_ = resp.Body.Close()
			}
		}
		mu.Lock()
		binds = append(binds, seen{cfg: cfg, noCredential: status})
		mu.Unlock()
		return srv, nil
	})

	cfg := meshConfig(t, home)
	cfg.Generate = wireadp.UnimplementedGenerate{}     // keep the check hermetic: no upstream call
	cfg.RequireV1InternalAuth = wireadp.BoolPtr(false) // the session's passive opt-in
	m := startMesh(t, cfg)
	if err := m.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("EnsureHealthy: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(binds) != 2 {
		t.Fatalf("want the ownership probe bind plus the real bind, got %d", len(binds))
	}
	if binds[0].cfg.RequireV1InternalAuth == nil || !*binds[0].cfg.RequireV1InternalAuth {
		t.Fatalf("the probe listener must run the strict posture, got %v", binds[0].cfg.RequireV1InternalAuth)
	}
	// Any non-401 here means the passive route admitted the request (with the
	// hermetic seam it would be 501); 401 is the proof it did not.
	if binds[0].noCredential != http.StatusUnauthorized {
		t.Fatalf("a no-credential /v1internal request on the probe listener = %d, want 401", binds[0].noCredential)
	}
	if binds[1].cfg.RequireV1InternalAuth == nil || *binds[1].cfg.RequireV1InternalAuth {
		t.Fatalf("the session's own bind must keep the passive posture, got %v", binds[1].cfg.RequireV1InternalAuth)
	}
}

func TestStart_ForeignCrossProcessRecordIsNeverAdopted(t *testing.T) {
	home, _ := sandboxHome(t)

	// A live adapter owned by another process, recorded in the sandbox home
	// exactly where wireadp's cross-process adoption looks for it.
	foreignCmd, urlFile := newForeignAdapterChild(t, home)
	if err := foreignCmd.Start(); err != nil {
		t.Fatalf("foreign helper start: %v", err)
	}
	foreignURL := waitForFile(t, urlFile, 15*time.Second)
	foreignPort := portFromURL(t, foreignURL)

	// The mesh must start its OWN instance: never adopt the foreign record,
	// never clobber it.
	m := startMesh(t, meshConfig(t, home))
	if m.URL() == foreignURL {
		t.Fatal("the mesh adopted the foreign instance's URL")
	}
	if m.Port() <= 0 {
		t.Fatalf("the mesh did not bind its own port: %d", m.Port())
	}
	if err := m.EnsureHealthy(context.Background()); err != nil {
		t.Fatalf("the mesh's own instance must be healthy: %v", err)
	}

	// The foreign instance is still alive and serving.
	resp, err := http.Get(foreignURL + "/healthz")
	if err != nil {
		t.Fatalf("the foreign instance must survive: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the foreign instance's healthz = %d", resp.StatusCode)
	}

	// The foreign discovery record was not clobbered by this spawn.
	record, err := os.ReadFile(filepath.Join(home, "wireadp.json"))
	if err != nil {
		t.Fatalf("the foreign record must still exist: %v", err)
	}
	if !strings.Contains(string(record), foreignPort) {
		t.Fatalf("the mesh clobbered the foreign discovery record: %s", record)
	}
}

// ---------------------------------------------------------------------------
// sandbox isolation
// ---------------------------------------------------------------------------

func TestSandbox_RealUserHomeUntouched(t *testing.T) {
	home, userHome := sandboxHome(t)

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

	// A full session: spawn, supervise a child that runs to completion.
	m := startMesh(t, meshConfig(t, home))
	child := newHelperChild(t, "touch", 0)
	if err := m.RunChild(context.Background(), child.cmd); err != nil {
		t.Fatalf("RunChild: %v", err)
	}
	if _, err := os.Stat(child.file); err != nil {
		t.Fatalf("the child did not run: %v", err)
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
	data, err := os.ReadFile(canary)
	if err != nil || string(data) != "untouched" {
		t.Fatalf("the canary was modified: %q (%v)", data, err)
	}

	// And the mesh's own state stayed inside the sandbox AGY_PRO_HOME.
	if hit := treeContains(t, userHome, "wireadp.json"); hit != "" {
		t.Fatalf("mesh state leaked into the user home: %s", hit)
	}
	if rec := readScopeRecord(t, home); !strings.Contains(rec, fmt.Sprintf("%d", m.Port())) {
		t.Fatalf("the record for port %d must live in the sandbox scope directory: %s", m.Port(), rec)
	}
}

// ---------------------------------------------------------------------------
// credential hygiene: the mesh layer holds no key values
// ---------------------------------------------------------------------------

func TestNoKeyValuesAnywhereInTheMeshLayer(t *testing.T) {
	home, _ := sandboxHome(t)

	// The registry references the key by env-var NAME; the value lives in the
	// environment and is resolved inside wireadp at request time.
	const keyName = "GO_MESH_TEST_PROVIDER_KEY_NAME"
	const keyValue = "sk-synthetic-MESH-CANARY-9f13"
	t.Setenv(keyName, keyValue)
	if got := testRegistry().Providers[0].APIKeyEnv; got != keyName {
		t.Fatalf("the registry must carry the env-var NAME, got %q", got)
	}

	var visible bytes.Buffer
	cfg := meshConfig(t, home)
	cfg.ErrWriter = &visible
	cfg.ChildPollInterval = 50 * time.Millisecond
	m := startMesh(t, cfg)

	// Exercise the whole surface, including the visible fail-fast path.
	child := newHelperChild(t, "sleep", 0)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = m.srv.Stop()
	}()
	err := m.RunChild(context.Background(), child.cmd)
	if !errors.Is(err, ErrMeshDied) {
		t.Fatalf("want ErrMeshDied, got %v", err)
	}

	if strings.Contains(visible.String(), keyValue) {
		t.Fatal("a key value reached the visible error writer")
	}
	if hit := treeContains(t, home, keyValue); hit != "" {
		t.Fatalf("a key value reached a file under the sandbox home: %s", hit)
	}
	if hit := treeContains(t, userHomeOf(t), keyValue); hit != "" {
		t.Fatalf("a key value reached the user home: %s", hit)
	}
}

func userHomeOf(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return os.Getenv("USERPROFILE")
	}
	return os.Getenv("HOME")
}
