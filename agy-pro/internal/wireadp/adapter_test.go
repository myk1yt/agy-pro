package wireadp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// testRegistry is an in-memory registry: no providers.json, no real network.
// probe-gemini and probe-blade mirror the GF-30 fixture's token limits.
func testRegistry() *providers.Registry {
	return &providers.Registry{
		Providers: []providers.Provider{{
			Name:    "mock-provider",
			BaseURL: "http://127.0.0.1:9/v1",
			Models: []providers.Model{
				{ID: "probe-gemini", ContextLength: 1048576, MaxOutputTokens: 65536},
				{ID: "probe-blade", ContextLength: 200000, MaxOutputTokens: 8192},
				{ID: "probe-evergreen"},
			},
		}},
	}
}

// testConfig isolates every test in its own agy-pro home so neither the
// discovery file nor the audit log touches the real ~/.agy-pro.
func testConfig(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	return Config{
		Registry:  testRegistry(),
		AuditPath: filepath.Join(home, "audit.jsonl"),
	}
}

func startServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	srv, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv
}

// resetSingletonForTest drops the in-process singleton so a test can simulate
// a fresh process adopting the discovery file.
func resetSingletonForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		singletonMu.Lock()
		singleton = nil
		singletonMu.Unlock()
	})
	singletonMu.Lock()
	singleton = nil
	singletonMu.Unlock()
}

func do(t *testing.T, method, url string, headers map[string]string, body []byte) (*http.Response, string) {
	t.Helper()
	resp, raw, err := tryDo(method, url, headers, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp, raw
}

// tryDo performs a request and returns the transport error instead of failing,
// for the cases where a dead server is the expected outcome. A request with a
// body carries Content-Type: application/json unless the caller sets its own,
// which is the shape every real client (agy's genai SDK and its CodeAssist
// client) sends.
func tryDo(method, url string, headers map[string]string, body []byte) (*http.Response, string, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return resp, string(raw), nil
}

func get(t *testing.T, url string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	return do(t, http.MethodGet, url, headers, nil)
}

func post(t *testing.T, url string, headers map[string]string, body []byte) (*http.Response, string) {
	t.Helper()
	return do(t, http.MethodPost, url, headers, body)
}

// mustRequest builds a request for direct handler invocation.
func mustRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("NewRequest(%s %s): %v", method, url, err)
	}
	req.RemoteAddr = "127.0.0.1:5555"
	return req
}

// chatBody is a minimal valid GenerateContentRequest.
func chatBody() []byte {
	return []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
}

// envelopeBody is a minimal valid /v1internal envelope.
func envelopeBody(model string) []byte {
	return []byte(`{"project":"p","model":"` + model + `","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
}

// errorEnvelope is the decoded §9.1 body shape.
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func decodeError(t *testing.T, body string) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("response is not a §9.1 envelope: %v (%s)", err, body)
	}
	return env
}

func goldenPath(name string) string {
	return filepath.Join("..", "geminiwire", "golden", name)
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return data
}

// recordingGenerate is a spy GenerateHandler: it records the contexts it was
// handed and answers with a caller-chosen outcome.
type recordingGenerate struct {
	mu     sync.Mutex
	calls  []*GenerateContext
	err    error
	body   string
	status int
}

func (g *recordingGenerate) Generate(w http.ResponseWriter, r *http.Request, gc *GenerateContext) error {
	g.mu.Lock()
	g.calls = append(g.calls, gc)
	g.mu.Unlock()
	if g.body != "" {
		status := g.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, g.body)
	}
	return g.err
}

func (g *recordingGenerate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *recordingGenerate) last() *GenerateContext {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.calls) == 0 {
		return nil
	}
	return g.calls[len(g.calls)-1]
}

// ---------------------------------------------------------------------------
// lifecycle
// ---------------------------------------------------------------------------

func TestStart_BindsLoopbackEphemeralPort(t *testing.T) {
	srv := startServer(t, testConfig(t))
	if !strings.HasPrefix(srv.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL %q is not loopback", srv.URL())
	}
	if srv.Port() <= 0 || srv.Port() > 65535 {
		t.Fatalf("port %d out of range", srv.Port())
	}
	addr, ok := srv.listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr %v is not TCP", srv.listener.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("listener bound to non-loopback %v", addr.IP)
	}
	resp, body := get(t, srv.URL()+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status %d body %s", resp.StatusCode, body)
	}
}

func TestStart_ServerTimeoutShape(t *testing.T) {
	srv := startServer(t, testConfig(t))
	if srv.http.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.http.ReadHeaderTimeout)
	}
	if srv.http.ReadTimeout != 60*time.Second {
		t.Errorf("ReadTimeout = %v, want 60s", srv.http.ReadTimeout)
	}
	if srv.http.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.http.IdleTimeout)
	}
	// WriteTimeout must stay unset so an SSE stream can outlive any deadline.
	if srv.http.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want unset", srv.http.WriteTimeout)
	}
}

func TestStart_HealthzBodyAndMethods(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := get(t, srv.URL()+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz status %d", resp.StatusCode)
	}
	if got := strings.TrimSpace(body); got != `{"status":"ok","service":"agy-pro-wireadp"}` {
		t.Fatalf("healthz body = %s", got)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("healthz content-type = %q", ct)
	}
	resp, body = post(t, srv.URL()+"/healthz", nil, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusInvalidArgument {
		t.Fatalf("405 status string = %q", env.Error.Status)
	}
}

func TestServerHealthz_ProbesOwnRoute(t *testing.T) {
	srv := startServer(t, testConfig(t))
	if err := srv.Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz: %v", err)
	}
	_ = srv.Stop()
	if err := srv.Healthz(context.Background()); err == nil {
		t.Fatal("Healthz must fail after Stop")
	}
}

func TestStop_IsIdempotent(t *testing.T) {
	srv := startServer(t, testConfig(t))
	if err := srv.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if _, _, err := tryDo(http.MethodGet, srv.URL()+"/healthz", nil, nil); err == nil {
		t.Fatal("a stopped server must not answer")
	}
}

func TestSingleton_AdoptsWhenNonceMatches(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = "install-nonce-alpha"
	srv1 := startServer(t, cfg)
	srv2, err := Start(cfg)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if srv2 != srv1 {
		t.Fatal("second Start must adopt the healthy singleton")
	}
	if err := srv2.Healthz(context.Background()); err != nil {
		t.Fatalf("adopted server healthz: %v", err)
	}
}

func TestSingleton_RefusesWhenNonceMismatchesAndNeverKillsTheFirst(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = "nonce-one"
	srv1 := startServer(t, cfg)

	cfg2 := testConfig(t)
	cfg2.Registry = cfg.Registry
	cfg2.AuthKey = "nonce-two"
	if _, err := Start(cfg2); err == nil {
		t.Fatal("a nonce mismatch must refuse to adopt")
	}

	// The first instance is untouched and still serving under its own nonce.
	if err := srv1.Healthz(context.Background()); err != nil {
		t.Fatalf("first server must survive a refused adopt: %v", err)
	}
	url := srv1.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"
	resp, body := post(t, url, map[string]string{headerAPIKey: "nonce-one"}, chatBody())
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("first server lost its auth: %s", body)
	}
	// Auth passed through to the generation path (which then fails upstream:
	// the test registry points at a closed port).
	if env := decodeError(t, body); env.Error.Status != statusUnavailable {
		t.Fatalf("expected the upstream transport envelope, got %s", body)
	}
}

func TestSingleton_NeverSilentlyChangesAuthMode(t *testing.T) {
	// A strict caller must not adopt a permissive singleton (silent auth
	// downgrade), and a permissive caller must not adopt a strict one.
	permissive := testConfig(t)
	srv1 := startServer(t, permissive)

	strict := testConfig(t)
	strict.Registry = permissive.Registry
	strict.AuthKey = "nonce-one"
	if _, err := Start(strict); err == nil {
		t.Fatal("a strict Start must refuse to adopt a permissive singleton")
	}
	if err := srv1.Healthz(context.Background()); err != nil {
		t.Fatalf("permissive server must survive: %v", err)
	}
	resp, body := post(t, srv1.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("permissive server changed behavior: %d %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusUnavailable {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func TestSingleton_ReplacesAStoppedInstance(t *testing.T) {
	cfg := testConfig(t)
	srv1, err := Start(cfg)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := srv1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	srv2 := startServer(t, cfg)
	if srv2 == srv1 {
		t.Fatal("a stopped singleton must be replaced, not adopted")
	}
	if resp, _ := get(t, srv2.URL()+"/healthz", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("replacement server is not serving")
	}
}

func TestSingleton_AdoptsRemoteInstanceFromDiscoveryFile(t *testing.T) {
	// One home for the whole test: the discovery file is the only channel a
	// later process has, so both "processes" must share the agy-pro home.
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	reg := testRegistry()
	cfg := Config{Registry: reg, AuthKey: "shared-nonce", AuditPath: filepath.Join(home, "audit.jsonl")}
	srv1 := startServer(t, cfg)

	// A later process has no in-process singleton: it must adopt the recorded
	// instance through the healthz nonce, and must never stop it.
	resetSingletonForTest(t)
	cfg2 := Config{Registry: reg, AuthKey: "shared-nonce", AuditPath: filepath.Join(home, "audit2.jsonl")}
	srv2, err := Start(cfg2)
	if err != nil {
		t.Fatalf("cross-process Start: %v", err)
	}
	if !srv2.adopted {
		t.Fatal("expected an adopted remote handle")
	}
	if srv2.URL() != srv1.URL() {
		t.Fatalf("adopted URL %s != owner URL %s", srv2.URL(), srv1.URL())
	}
	if srv2.Port() != srv1.Port() {
		t.Fatalf("adopted port %d != owner port %d", srv2.Port(), srv1.Port())
	}
	// Stopping the adopted handle must leave the owner running.
	if err := srv2.Stop(); err != nil {
		t.Fatalf("adopted Stop: %v", err)
	}
	if err := srv1.Healthz(context.Background()); err != nil {
		t.Fatalf("owner must survive an adopted handle's Stop: %v", err)
	}
	// Serving through an adopted handle reports the situation instead of
	// pretending to own the listener.
	rec := httptest.NewRecorder()
	srv2.ServeHTTP(rec, mustRequest(t, http.MethodGet, srv2.URL()+"/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("adopted ServeHTTP status = %d, want 503", rec.Code)
	}

	// A different nonce refuses the adopt instead of starting a second
	// listener, and still never kills the first.
	resetSingletonForTest(t)
	cfg3 := Config{Registry: reg, AuthKey: "other-nonce", AuditPath: filepath.Join(home, "audit3.jsonl")}
	if _, err := Start(cfg3); err == nil {
		t.Fatal("a remote nonce mismatch must refuse to adopt")
	}
	if err := srv1.Healthz(context.Background()); err != nil {
		t.Fatalf("owner must survive a refused remote adopt: %v", err)
	}

	// A permissive candidate refuses to adopt the strict owner: one adapter
	// per install nonce, never a silent auth-mode change, never a kill.
	resetSingletonForTest(t)
	cfg4 := Config{Registry: reg, AuditPath: filepath.Join(home, "audit4.jsonl")}
	if _, err := Start(cfg4); err == nil {
		t.Fatal("a permissive candidate must refuse to adopt a strict singleton")
	}
	if err := srv1.Healthz(context.Background()); err != nil {
		t.Fatalf("owner must survive: %v", err)
	}
}

func TestIdleFor_TracksActivity(t *testing.T) {
	srv := startServer(t, testConfig(t))
	time.Sleep(5 * time.Millisecond)
	if srv.IdleFor() <= 0 {
		t.Fatal("IdleFor must be positive after a pause")
	}
	if resp, _ := get(t, srv.URL()+"/healthz", nil); resp.StatusCode != http.StatusOK {
		t.Fatal("healthz failed")
	}
	if d := srv.IdleFor(); d > 50*time.Millisecond {
		t.Fatalf("IdleFor = %v after a request, want small", d)
	}
}
