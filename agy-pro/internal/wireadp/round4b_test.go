package wireadp

// FIX-44 — per-route auth posture. The /v1internal:* data routes can admit any
// loopback caller (passive receipt) so the real agy child on the CodeAssist
// path works: it presents only its keyring OAuth bearer, and no shim-controlled
// channel can carry an install nonce on that path. The bearer is received and
// discarded. /v1beta and /healthz keep strict nonce auth in ALL modes.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// keyringBearer is a distinctive planted credential: if it ever reaches the
// audit file or the outbound request, the pin fails.
const keyringBearer = "KEYRING-BEARER-PLANT-9f8e7d6c5b4a"

func internalStreamURL(srv *Server, model string) string {
	return srv.URL() + "/v1internal:streamGenerateContent?alt=sse"
}

// passiveConfig builds a strict-key config whose /v1internal posture is passive.
// passiveConfig builds a strict-key config whose /v1internal posture is
// passive. The caller starts the server AFTER any further Config mutation,
// because Start takes the config by value.
func passiveConfig(t *testing.T, m *mockUpstream) Config {
	t.Helper()
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuthKey = "install-nonce"
	cfg.RequireV1InternalAuth = BoolPtr(false)
	return cfg
}

// ---------------------------------------------------------------------------
// the default stays strict (no behavior change for existing callers)
// ---------------------------------------------------------------------------

func TestV1InternalAuth_DefaultIsStrict(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuthKey = "install-nonce"
	// The field is deliberately left at its zero value (nil).
	srv := startServer(t, cfg)

	// A bearer-only request — exactly what the real agy child sends — is
	// refused, because the default posture is strict.
	resp, raw := post(t, internalStreamURL(srv, "probe-gemini"),
		map[string]string{"Authorization": "Bearer " + keyringBearer}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s: the nil default must stay strict", resp.StatusCode, raw)
	}
	if env := decodeError(t, raw); env.Error.Status != statusUnauthenticated {
		t.Fatalf("envelope = %+v", env.Error)
	}
	// An explicit TRUE is the same posture.
	cfg2, _ := testUpstreamConfig(t, m)
	cfg2.AuthKey = "install-nonce"
	cfg2.RequireV1InternalAuth = BoolPtr(true)
	srv2 := startServer(t, cfg2)
	resp, raw = post(t, internalStreamURL(srv2, "probe-gemini"),
		map[string]string{"Authorization": "Bearer " + keyringBearer}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("explicit TRUE status %d body %s", resp.StatusCode, raw)
	}
}

// ---------------------------------------------------------------------------
// passive mode admits any loopback caller on /v1internal
// ---------------------------------------------------------------------------

func TestV1InternalAuth_PassiveAdmitsBearerOnlyAndBare(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"a keyring bearer only", map[string]string{"Authorization": "Bearer " + keyringBearer}},
		{"no credential at all", nil},
		{"a bogus nonce is also admitted", map[string]string{headerAPIKey: "not-the-nonce"}},
		{"a lowercase bearer", map[string]string{"Authorization": "bearer " + keyringBearer}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			srv := startServer(t, passiveConfig(t, m))

			resp, raw := post(t, internalStreamURL(srv, "probe-gemini"), tc.headers, envelopeBody("probe-gemini"))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s: passive mode must admit the caller", resp.StatusCode, raw)
			}
			if !strings.Contains(raw, "finishReason") {
				t.Fatalf("no completed stream: %s", raw)
			}
			if m.count() != 1 {
				t.Fatalf("upstream calls = %d, want 1 (the request must be served)", m.count())
			}
		})
	}
}

// TestV1InternalAuth_PassiveAdmitsTheNonGenerationRPCs covers the lifecycle RPCs
// on the same surface: loadCodeAssist must answer so agy's session can start.
func TestV1InternalAuth_PassiveAdmitsTheNonGenerationRPCs(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))
	for _, rpc := range []string{"loadCodeAssist", "fetchUserInfo", "fetchAvailableModels", "listExperiments"} {
		resp, raw := post(t, srv.URL()+"/v1internal:"+rpc,
			map[string]string{"Authorization": "Bearer " + keyringBearer}, []byte(`{}`))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d body %s", rpc, resp.StatusCode, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// the bearer never leaves the process
// ---------------------------------------------------------------------------

// TestV1InternalAuth_PassiveBearerNeverReachesUpstream is the passive-mode twin
// of TestGenerate_OutboundRequestStripsInboundHeaders: the planted bearer must
// not become the outbound credential, and no agy header may be forwarded.
func TestV1InternalAuth_PassiveBearerNeverReachesUpstream(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))

	resp, raw := post(t, internalStreamURL(srv, "probe-gemini"),
		map[string]string{
			"Authorization":               "Bearer " + keyringBearer,
			"User-Agent":                  "antigravity/cli/1.3.5",
			"X-Cloudaicompanion-Trace-Id": "trace-abc",
		}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	call := m.last()
	if strings.Contains(call.AuthzValue, keyringBearer) {
		t.Fatalf("the inbound bearer became the outbound credential: %q", call.AuthzValue)
	}
	if len(call.LeakedAgy) != 0 {
		t.Fatalf("inbound agy headers reached the upstream: %v", call.LeakedAgy)
	}
	if strings.Contains(call.AuthzValue, "install-nonce") {
		t.Fatalf("the install nonce became the outbound credential: %q", call.AuthzValue)
	}
}

// TestV1InternalAuth_PassiveAuditNeverCarriesTheBearer greps the audit file for
// the planted bearer and asserts only header NAMES are recorded.
func TestV1InternalAuth_PassiveAuditNeverCarriesTheBearer(t *testing.T) {
	m := newMockUpstream(t)
	cfg := passiveConfig(t, m)
	srv := startServer(t, cfg)

	resp, _ := post(t, internalStreamURL(srv, "probe-gemini"),
		map[string]string{"Authorization": "Bearer " + keyringBearer}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if lines[0].Route != routeInternalGen {
		t.Fatalf("audit route = %q", lines[0].Route)
	}
	joined := strings.Join(lines[0].HeaderNames, ",")
	if !strings.Contains(joined, "Authorization") {
		t.Fatalf("the audit line must carry the header NAME: %v", lines[0].HeaderNames)
	}
	raw, err := readFileForTest(cfg.AuditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(string(raw), keyringBearer) {
		t.Fatalf("the audit file leaked the bearer: %s", raw)
	}
	if strings.Contains(string(raw), "install-nonce") {
		t.Fatalf("the audit file leaked the install nonce: %s", raw)
	}
	// The discard is noted by name only, so the decision is auditable.
	if !strings.Contains(lines[0].Note, "credential discarded") {
		t.Fatalf("audit note = %q, want the discard record", lines[0].Note)
	}
}

// ---------------------------------------------------------------------------
// the other surfaces keep strict auth in every mode
// ---------------------------------------------------------------------------

func TestV1InternalAuth_PassiveKeepsOtherSurfacesStrict(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))

	// /v1beta without the nonce: 401.
	resp, raw := post(t, streamURL(srv, "probe-gemini"),
		map[string]string{"Authorization": "Bearer " + keyringBearer}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/v1beta status %d body %s", resp.StatusCode, raw)
	}
	// /v1beta with the nonce: served.
	resp, _ = post(t, streamURL(srv, "probe-gemini"),
		map[string]string{headerAPIKey: "install-nonce"}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1beta with the nonce status %d", resp.StatusCode)
	}

	// /healthz without the nonce: 401, even though it is exempt from the data
	// auth path (the handler does its own constant-time check).
	resp, raw = get(t, srv.URL()+"/healthz", map[string]string{"Authorization": "Bearer " + keyringBearer})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/healthz status %d body %s", resp.StatusCode, raw)
	}
	// /healthz with the nonce: 200.
	resp, _ = get(t, srv.URL()+"/healthz", map[string]string{headerInstallNonce: "install-nonce"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz with the nonce status %d", resp.StatusCode)
	}

	// The listing route is /v1beta surface too.
	resp, raw = get(t, srv.URL()+"/v1beta/models", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("listing status %d body %s", resp.StatusCode, raw)
	}

	// A non-loopback peer is refused regardless of posture (defense in depth).
	req := mustRequest(t, http.MethodPost, internalStreamURL(srv, "probe-gemini"), envelopeBody("probe-gemini"))
	req.RemoteAddr = "203.0.113.7:4444"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback peer status %d, want 403", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// the rate limit still applies in passive mode
// ---------------------------------------------------------------------------

func TestV1InternalAuth_PassiveStillRateLimited(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))
	url := internalStreamURL(srv, "probe-gemini")
	headers := map[string]string{"Authorization": "Bearer " + keyringBearer}

	var passed, limited int
	for i := 0; i < rateLimitPerMinute+10; i++ {
		resp, body := post(t, url, headers, envelopeBody("probe-gemini"))
		switch resp.StatusCode {
		case http.StatusOK:
			passed++
		case http.StatusTooManyRequests:
			limited++
			if env := decodeError(t, body); env.Error.Status != statusResourceExhausted {
				t.Fatalf("429 envelope = %+v", env.Error)
			}
		default:
			t.Fatalf("attempt %d: status %d body %s", i, resp.StatusCode, body)
		}
	}
	if limited == 0 {
		t.Fatal("passive mode escaped the per-install rate limit")
	}
	if passed > rateLimitPerMinute+5 {
		t.Fatalf("%d requests passed, want at most %d", passed, rateLimitPerMinute+5)
	}
}

// ---------------------------------------------------------------------------
// posture classification
// ---------------------------------------------------------------------------

func TestPostureFor(t *testing.T) {
	srv := &Server{cfg: Config{AuthKey: "nonce"}}
	strict := []string{
		"/v1beta/models/probe-gemini:streamGenerateContent",
		"/v1beta/models",
		"/healthz",
		"/v1internal", // the bare path hits the catch-all
		"/",
	}
	for _, p := range strict {
		if got := srv.postureFor(p); got != authStrict {
			t.Fatalf("postureFor(%q) = %v, want strict by default", p, got)
		}
	}
	passive := []string{
		"/v1internal:generateContent",
		"/v1internal:streamGenerateContent",
		"/v1internal:loadCodeAssist",
		"/v1internal:unknownFutureRpc",
	}
	srv.cfg.RequireV1InternalAuth = BoolPtr(false)
	for _, p := range passive {
		if got := srv.postureFor(p); got != authPassive {
			t.Fatalf("postureFor(%q) = %v, want passive", p, got)
		}
	}
	// The other surfaces stay strict even in passive mode.
	for _, p := range []string{"/v1beta/models/probe-gemini:generateContent", "/healthz", "/"} {
		if got := srv.postureFor(p); got != authStrict {
			t.Fatalf("postureFor(%q) = %v, want strict in passive mode", p, got)
		}
	}
	// An explicit TRUE restores strict everywhere.
	srv.cfg.RequireV1InternalAuth = BoolPtr(true)
	if got := srv.postureFor("/v1internal:loadCodeAssist"); got != authStrict {
		t.Fatalf("explicit TRUE posture = %v, want strict", got)
	}
}

func TestRequireV1InternalAuth_Default(t *testing.T) {
	srv := &Server{}
	if !srv.requireV1InternalAuth() {
		t.Fatal("the nil default must require the nonce on /v1internal")
	}
	srv.cfg.RequireV1InternalAuth = BoolPtr(false)
	if srv.requireV1InternalAuth() {
		t.Fatal("an explicit false must relax the posture")
	}
	srv.cfg.RequireV1InternalAuth = BoolPtr(true)
	if !srv.requireV1InternalAuth() {
		t.Fatal("an explicit true must require the nonce")
	}
}

// TestV1InternalAuth_PassiveWithoutAnyKeyIsPlainPermissive guards the
// interaction: with no AuthKey at all the whole adapter is permissive and the
// posture field is irrelevant.
func TestV1InternalAuth_PassiveWithoutAnyKeyIsPlainPermissive(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuthKey = ""
	cfg.RequireV1InternalAuth = BoolPtr(false)
	srv := startServer(t, cfg)

	resp, _ := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1beta status %d", resp.StatusCode)
	}
	resp, _ = post(t, internalStreamURL(srv, "probe-gemini"), nil, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1internal status %d", resp.StatusCode)
	}
	// healthz stays open in permissive mode.
	resp, _ = get(t, srv.URL()+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status %d", resp.StatusCode)
	}
}

// TestV1InternalAuth_PassiveWatchdogConfigStillApplies makes sure the passive
// posture does not disturb the §6.4 knobs: a shrunk idle budget still fires.
func TestV1InternalAuth_PassiveWatchdogConfigStillApplies(t *testing.T) {
	m := newMockUpstream(t)
	cfg := passiveConfig(t, m)
	cfg.UpstreamFirstByteTimeout = 5 * time.Second
	cfg.UpstreamIdleTimeout = 150 * time.Millisecond
	srv := startServer(t, cfg)
	release := make(chan struct{})
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEFrame(w, contentChunk("시작"))
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	})
	defer close(release)

	resp, raw := post(t, internalStreamURL(srv, "probe-gemini"),
		map[string]string{"Authorization": "Bearer " + keyringBearer}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(raw, "DEADLINE_EXCEEDED") {
		t.Fatalf("the idle watchdog must still fire on the passive route: %s", raw)
	}
}
