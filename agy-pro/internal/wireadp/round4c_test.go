package wireadp

// Round-4c fix pins:
//   FIX-57 — /v1internal RPC default payloads byte-aligned with the known-good
//            probe payloads that reached generation end-to-end against the real
//            agy binary (the fetchUserInfo user_tags shape blocked everything).
//   FIX-58 — the passive posture gains a JSON content-type gate and a
//            Sec-Fetch-* rejection, closing the CORS simple-request path.
//   FIX-59 — singleton adoption compares the /v1internal posture too, so a
//            strict caller can never silently adopt a passive singleton.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postNoContentType sends a POST with NO Content-Type header at all: the CORS
// simple-request shape. The shared post helper always sets application/json
// when a body is present, so this case needs its own path.
func postNoContentType(t *testing.T, url string, headers map[string]string, body []byte) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Del("Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("POST %s: read body: %v", url, err)
	}
	return resp, string(raw)
}

// ---------------------------------------------------------------------------
// FIX-57 — byte-pinned payload table with the known-good sources
// ---------------------------------------------------------------------------

// probeBodies is the byte contract for the /v1internal RPC defaults. Every row
// is the exact content of the known-good probe payload the design session
// served to agy 1.3.5 and reached print-mode generation end-to-end
// (refounded-topology.md:13; the mock's payload files are
// %TEMP%\agy-gate\payloads\<name>.json). onboardUser has no probe file (the
// probe answered {} and agy tolerated it); fetchAvailableModels is served by
// the mesh's CatalogFunc and only falls back to the empty catalog here.
var probeBodies = []struct {
	rpc  string
	want string
}{
	{"loadCodeAssist", `{"cloudaicompanion_project":"mock-project-1234","current_tier":{"id":"free-tier","name":"Free"},"paid_tier":{"id":"free-tier","name":"Free"}}`},
	{"fetchUserInfo", `{"user_settings":{},"user_tags":[],"region_code":"US"}`},
	{"fetchAdminControls", `{}`},
	{"listExperiments", `{"experiment_ids":[]}`},
	{"retrieveUserQuotaSummary", `{"userStatus":{"groups":[]}}`},
}

func TestInternalRPC_DefaultBodiesMatchProbe(t *testing.T) {
	srv := startServer(t, testConfig(t))
	for _, tc := range probeBodies {
		t.Run(tc.rpc, func(t *testing.T) {
			resp, body := post(t, srv.URL()+"/v1internal:"+tc.rpc, nil, []byte(`{}`))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s", resp.StatusCode, body)
			}
			if got := strings.TrimSpace(body); got != tc.want {
				t.Fatalf("body = %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestInternalRPC_ProbeBodiesAreValidJSON is the belt to the byte pin: each
// default must be parseable JSON with the field types agy's protobuf-JSON
// parser expects (user_tags an array, not an object).
func TestInternalRPC_ProbeBodiesAreValidJSON(t *testing.T) {
	srv := startServer(t, testConfig(t))
	for _, tc := range probeBodies {
		t.Run(tc.rpc, func(t *testing.T) {
			_, body := post(t, srv.URL()+"/v1internal:"+tc.rpc, nil, []byte(`{}`))
			var doc map[string]any
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatalf("body is not a JSON object: %v (%s)", err, body)
			}
			if tc.rpc == "fetchUserInfo" {
				tags, ok := doc["user_tags"]
				if !ok {
					t.Fatal("user_tags is missing")
				}
				if _, isArray := tags.([]any); !isArray {
					t.Fatalf("user_tags = %T, want an array (repeated field)", tags)
				}
				if doc["region_code"] != "US" {
					t.Fatalf("region_code = %v, want US", doc["region_code"])
				}
			}
			if tc.rpc == "loadCodeAssist" {
				for _, key := range []string{"cloudaicompanion_project", "current_tier", "paid_tier"} {
					if _, ok := doc[key]; !ok {
						t.Fatalf("%s is missing", key)
					}
				}
				tier, _ := doc["current_tier"].(map[string]any)
				if tier["id"] != "free-tier" || tier["name"] != "Free" {
					t.Fatalf("current_tier = %v", doc["current_tier"])
				}
			}
		})
	}
}

// TestInternalRPC_HookStillOverridesTheProbeDefault proves the alignment did
// not remove the per-RPC override seam the mesh uses.
func TestInternalRPC_HookStillOverridesTheProbeDefault(t *testing.T) {
	cfg := testConfig(t)
	custom := []byte(`{"user_settings":{"k":"v"},"user_tags":[],"region_code":"KR"}`)
	cfg.RPCHooks = map[string]func(body []byte) ([]byte, error){
		"fetchUserInfo": func(body []byte) ([]byte, error) { return custom, nil },
	}
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1internal:fetchUserInfo", nil, []byte(`{}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != string(custom) {
		t.Fatalf("body = %s, want the hook's bytes", body)
	}
}

// ---------------------------------------------------------------------------
// FIX-58 — the passive-posture browser gate
// ---------------------------------------------------------------------------

// TestBrowserGate_PassiveRejectsSimpleRequests pins the CORS simple-request
// path closed: a text/plain (or absent) Content-Type needs no preflight, so the
// adapter refuses it before the body is decoded and before any upstream call.
func TestBrowserGate_PassiveRejectsSimpleRequests(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
	}{
		{"text/plain", "text/plain"},
		{"absent", ""},
		{"form encoded", "application/x-www-form-urlencoded"},
		{"text/json is not application/json", "text/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			srv := startServer(t, passiveConfig(t, m))
			headers := map[string]string{"Authorization": "Bearer " + keyringBearer}
			if tc.contentType != "" {
				headers["Content-Type"] = tc.contentType
			}
			var resp *http.Response
			var raw string
			if tc.contentType == "" {
				resp, raw = postNoContentType(t, internalStreamURL(srv, "probe-gemini"), headers, envelopeBody("probe-gemini"))
			} else {
				resp, raw = post(t, internalStreamURL(srv, "probe-gemini"), headers, envelopeBody("probe-gemini"))
			}
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("status %d body %s, want 415", resp.StatusCode, raw)
			}
			if env := decodeError(t, raw); env.Error.Status != statusInvalidArgument {
				t.Fatalf("envelope = %+v", env.Error)
			}
			if m.count() != 0 {
				t.Fatalf("a refused request reached the upstream %d times", m.count())
			}
		})
	}
}

// TestBrowserGate_PassiveRejectsFetchMetadata covers the Sec-Fetch layer:
// browsers set these on every request and scripts cannot forge or suppress
// them, and agy (a Go binary) never sends them.
func TestBrowserGate_PassiveRejectsFetchMetadata(t *testing.T) {
	cases := []struct {
		name string
		site string
	}{
		{"cross-site", "cross-site"},
		{"same-site", "same-site"},
		{"uppercase value", "CROSS-SITE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			srv := startServer(t, passiveConfig(t, m))
			resp, raw := post(t, internalStreamURL(srv, "probe-gemini"), map[string]string{
				"Content-Type":   "application/json",
				"Sec-Fetch-Site": tc.site,
				"Sec-Fetch-Mode": "cors",
			}, envelopeBody("probe-gemini"))
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d body %s, want 403", resp.StatusCode, raw)
			}
			if env := decodeError(t, raw); env.Error.Status != statusPermissionDenied {
				t.Fatalf("envelope = %+v", env.Error)
			}
			if m.count() != 0 {
				t.Fatalf("a refused request reached the upstream %d times", m.count())
			}
		})
	}
}

// TestBrowserGate_PassiveAdmitsAgysShape is the no-regression pin: agy's real
// shape (application/json, no Sec-Fetch headers, keyring bearer) is served.
func TestBrowserGate_PassiveAdmitsAgysShape(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))
	resp, raw := post(t, internalStreamURL(srv, "probe-gemini"), map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + keyringBearer,
		"User-Agent":    "antigravity/cli/1.3.5",
	}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: agy's shape must be served", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "finishReason") {
		t.Fatalf("no completed stream: %s", raw)
	}
	// A charset parameter is still JSON.
	resp, _ = post(t, internalStreamURL(srv, "probe-gemini"), map[string]string{
		"Content-Type": "application/json; charset=utf-8",
	}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("charset parameter status %d", resp.StatusCode)
	}
}

// TestBrowserGate_AppliesToTheNonGenerationRPCs pins that the whole passive
// surface is gated, not only the generate routes.
func TestBrowserGate_AppliesToTheNonGenerationRPCs(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))
	for _, rpc := range []string{"loadCodeAssist", "fetchUserInfo", "fetchAvailableModels"} {
		resp, raw := post(t, srv.URL()+"/v1internal:"+rpc,
			map[string]string{"Content-Type": "text/plain"}, []byte(`{}`))
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("%s status %d body %s, want 415", rpc, resp.StatusCode, raw)
		}
	}
}

// TestBrowserGate_StrictModeUnchanged is the required no-regression pin: in
// strict mode the nonce governs and the content-type gate is NOT added, so a
// nonce-bearing request with a non-JSON content type behaves exactly as before.
func TestBrowserGate_StrictModeUnchanged(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuthKey = "install-nonce"
	// The posture is left at its nil default (strict).
	srv := startServer(t, cfg)

	// A nonce-bearing request with a non-JSON content type is admitted (the
	// gate does not apply in strict mode) and served end to end.
	resp, raw := post(t, streamURL(srv, "probe-gemini"), map[string]string{
		headerAPIKey:   "install-nonce",
		"Content-Type": "text/plain",
	}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: strict mode must not gain a content-type gate", resp.StatusCode, raw)
	}
	// A browser-shaped request without the nonce is still 401 (the nonce, not
	// the content type, is what blocks it).
	resp, raw = post(t, streamURL(srv, "probe-gemini"), map[string]string{
		"Content-Type":   "text/plain",
		"Sec-Fetch-Site": "cross-site",
	}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s, want 401", resp.StatusCode, raw)
	}
	// The same on /v1internal: no nonce → 401 regardless of the content type.
	resp, raw = post(t, internalStreamURL(srv, "probe-gemini"), map[string]string{
		"Content-Type": "text/plain",
	}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("strict /v1internal status %d body %s, want 401", resp.StatusCode, raw)
	}
	// And a nonce-bearing /v1internal request with a non-JSON content type is
	// served, proving no gate was added in strict mode.
	resp, raw = post(t, internalStreamURL(srv, "probe-gemini"), map[string]string{
		headerAPIKey:   "install-nonce",
		"Content-Type": "text/plain",
	}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("strict /v1internal with nonce status %d body %s", resp.StatusCode, raw)
	}
}

// TestBrowserGate_PermissiveModeUnchanged covers the third posture: with no
// AuthKey at all the adapter is fully permissive and its pre-existing §10.2
// residual is unchanged (no new gate).
func TestBrowserGate_PermissiveModeUnchanged(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	resp, raw := post(t, streamURL(srv, "probe-gemini"), map[string]string{
		"Content-Type": "text/plain",
	}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: fully permissive mode is unchanged", resp.StatusCode, raw)
	}
}

// TestBrowserGate_RefusalDoesNotSpendTheRateLimit proves the gate runs before
// the limiter, so an attacker's refused simple requests cannot starve the
// victim's budget.
func TestBrowserGate_RefusalDoesNotSpendTheRateLimit(t *testing.T) {
	m := newMockUpstream(t)
	srv := startServer(t, passiveConfig(t, m))
	headers := map[string]string{"Content-Type": "text/plain"}
	for i := 0; i < rateLimitPerMinute+20; i++ {
		resp, _ := post(t, internalStreamURL(srv, "probe-gemini"), headers, envelopeBody("probe-gemini"))
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("attempt %d: status %d, want 415", i, resp.StatusCode)
		}
	}
	// The real client's shape is still served: the bucket was never consumed.
	resp, raw := post(t, internalStreamURL(srv, "probe-gemini"), map[string]string{
		"Content-Type": "application/json",
	}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: the refused requests must not spend the budget", resp.StatusCode, raw)
	}
}

func TestIsJSONContentType(t *testing.T) {
	jsonTypes := []string{
		"application/json",
		"application/json; charset=utf-8",
		"Application/JSON",
		"  application/json  ",
	}
	for _, ct := range jsonTypes {
		if !isJSONContentType(ct) {
			t.Fatalf("%q must be accepted as JSON", ct)
		}
	}
	nonJSON := []string{"", "text/plain", "text/json", "application/xml", "multipart/form-data; boundary=x"}
	for _, ct := range nonJSON {
		if isJSONContentType(ct) {
			t.Fatalf("%q must be refused", ct)
		}
	}
}

// ---------------------------------------------------------------------------
// FIX-59 — adoption compares the /v1internal posture
// ---------------------------------------------------------------------------

// TestSingleton_AdoptionComparesThePosture proves a matching nonce is not
// enough: a caller whose /v1internal posture differs from the running
// singleton's is refused rather than silently adopting an auth-mode downgrade.
func TestSingleton_AdoptionComparesThePosture(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = "shared-nonce"
	srv1 := startServer(t, cfg)

	// Same nonce, opposite posture: refuse, and leave the first running.
	cfg2 := testConfig(t)
	cfg2.Registry = cfg.Registry
	cfg2.AuthKey = "shared-nonce"
	cfg2.RequireV1InternalAuth = BoolPtr(false)
	if _, err := Start(cfg2); err == nil {
		t.Fatal("a posture mismatch must refuse adoption")
	}
	if err := srv1.Healthz(context.Background()); err != nil {
		t.Fatalf("the first server must survive a refused adopt: %v", err)
	}

	// Same nonce, same posture (both strict): adopt.
	cfg3 := testConfig(t)
	cfg3.Registry = cfg.Registry
	cfg3.AuthKey = "shared-nonce"
	cfg3.RequireV1InternalAuth = BoolPtr(true)
	srv3, err := Start(cfg3)
	if err != nil {
		t.Fatalf("a matching posture must adopt: %v", err)
	}
	if srv3 != srv1 {
		t.Fatal("the matching-posture Start must return the same server")
	}

	// A passive singleton is likewise not adopted by a strict-default caller.
	// Release the first singleton first: one nonce per singleton.
	if err := srv1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	passive := testConfig(t)
	passive.AuthKey = "passive-nonce"
	passive.RequireV1InternalAuth = BoolPtr(false)
	srvP := startServer(t, passive)
	strict := testConfig(t)
	strict.Registry = passive.Registry
	strict.AuthKey = "passive-nonce"
	if _, err := Start(strict); err == nil {
		t.Fatal("a strict-default caller must refuse to adopt a passive singleton")
	}
	if err := srvP.Healthz(context.Background()); err != nil {
		t.Fatalf("the passive server must survive: %v", err)
	}
}

func TestSameSingletonConfig(t *testing.T) {
	strict := Config{AuthKey: "n"}
	strictExplicit := Config{AuthKey: "n", RequireV1InternalAuth: BoolPtr(true)}
	passive := Config{AuthKey: "n", RequireV1InternalAuth: BoolPtr(false)}
	otherNonce := Config{AuthKey: "m"}

	cases := []struct {
		name string
		a, b Config
		want bool
	}{
		{"nil and explicit-true are the same strict posture", strict, strictExplicit, true},
		{"nil and explicit-false differ", strict, passive, false},
		{"explicit-true and explicit-false differ", strictExplicit, passive, false},
		{"a differing nonce always differs", passive, otherNonce, false},
		{"both permissive with the same posture", Config{}, Config{}, true},
		{"both passive with the same nonce", passive, passive, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameSingletonConfig(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameSingletonConfig = %v, want %v", got, tc.want)
			}
			// The comparison must be symmetric.
			if got := sameSingletonConfig(tc.b, tc.a); got != tc.want {
				t.Fatalf("sameSingletonConfig is asymmetric: %v, want %v", got, tc.want)
			}
		})
	}
}
