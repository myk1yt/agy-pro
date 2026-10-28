package wireadp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"agypro/internal/geminiwire"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// ---------------------------------------------------------------------------
// /v1beta generate routes
// ---------------------------------------------------------------------------

func TestGenerateRoute_ColonTrapModelNames(t *testing.T) {
	spy := &recordingGenerate{}
	cfg := testConfig(t)
	cfg.Generate = spy
	srv := startServer(t, cfg)

	cases := []struct {
		name       string
		rawPath    string
		wantModel  string
		wantTarget string
	}{
		{
			name:       "raw colons, agy's own shape",
			rawPath:    "/v1beta/models/evergreen:127.0.0.1:8791/models/probe-evergreen:streamGenerateContent",
			wantModel:  "evergreen:127.0.0.1:8791/models/probe-evergreen",
			wantTarget: "probe-evergreen",
		},
		{
			name:       "percent-encoded colons keep the distinction",
			rawPath:    "/v1beta/models/evergreen%3A127.0.0.1%3A8791/models/probe-evergreen:streamGenerateContent",
			wantModel:  "evergreen%3A127.0.0.1%3A8791/models/probe-evergreen",
			wantTarget: "probe-evergreen",
		},
		{
			name:       "plain slug",
			rawPath:    "/v1beta/models/probe-gemini:streamGenerateContent",
			wantModel:  "probe-gemini",
			wantTarget: "probe-gemini",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := post(t, srv.URL()+tc.rawPath+"?alt=sse", nil, chatBody())
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s", resp.StatusCode, body)
			}
			call := spy.last()
			if call == nil {
				t.Fatal("the seam was never reached")
			}
			if call.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", call.Model, tc.wantModel)
			}
			if call.Verb != "streamGenerateContent" {
				t.Fatalf("verb = %q", call.Verb)
			}
			if !call.AltSSE {
				t.Fatal("AltSSE must be true for the stream verb")
			}
			if call.Target.Model.ID != tc.wantTarget {
				t.Fatalf("resolved target = %q, want %q (terminal-segment rule)", call.Target.Model.ID, tc.wantTarget)
			}
			if call.Target.Provider.Name != "mock-provider" {
				t.Fatalf("resolved provider = %q", call.Target.Provider.Name)
			}
			if call.Envelope != nil {
				t.Fatal("the public surface must not carry an envelope")
			}
			if call.Request == nil || len(call.Request.Contents) != 1 {
				t.Fatalf("decoded request = %+v", call.Request)
			}
		})
	}
}

func TestGenerateRoute_NonStreamVerbToleratesMissingAltSSE(t *testing.T) {
	spy := &recordingGenerate{}
	cfg := testConfig(t)
	cfg.Generate = spy
	srv := startServer(t, cfg)

	// No alt=sse: still accepted (§5.1 defensive row).
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if call := spy.last(); call == nil || !call.AltSSE {
		t.Fatal("the stream verb must imply SSE framing")
	}

	// A stray alt=sse on the non-stream verb is tolerated, not rejected.
	resp, body = post(t, srv.URL()+"/v1beta/models/probe-gemini:generateContent?alt=sse", nil, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	call := spy.last()
	if call.Verb != "generateContent" {
		t.Fatalf("verb = %q", call.Verb)
	}
	if !call.AltSSE {
		t.Fatal("a stray alt=sse must be honored")
	}
}

func TestGenerateRoute_WrongMethodIs405(t *testing.T) {
	srv := startServer(t, testConfig(t))
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		resp, body := do(t, method, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status %d body %s", method, resp.StatusCode, body)
		}
		env := decodeError(t, body)
		if env.Error.Code != http.StatusMethodNotAllowed || env.Error.Status != statusInvalidArgument {
			t.Fatalf("%s envelope = %+v", method, env.Error)
		}
	}
}

func TestGenerateRoute_BodyCapIs400(t *testing.T) {
	srv := startServer(t, testConfig(t))
	oversized := bytes.Repeat([]byte("a"), openaiup.MaxBodyBytes+1)
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, oversized)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	env := decodeError(t, body)
	if env.Error.Status != statusInvalidArgument || env.Error.Message != "request body exceeds limit" {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func TestGenerateRoute_MalformedBodyIs400(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	env := decodeError(t, body)
	if env.Error.Status != statusInvalidArgument || env.Error.Message != "invalid GenerateContentRequest body" {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func TestGenerateRoute_UnknownModelIs404WithAudit(t *testing.T) {
	cfg := testConfig(t)
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-unknown-model:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	env := decodeError(t, body)
	if env.Error.Code != http.StatusNotFound || env.Error.Status != statusNotFound {
		t.Fatalf("envelope = %+v", env.Error)
	}
	if env.Error.Message != "model probe-unknown-model is not served by this adapter" {
		t.Fatalf("message = %q", env.Error.Message)
	}
	// GF-32 pins the audit line alongside the 404 (allow-list = scan guard).
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if lines[0].Route != routeGenerate || lines[0].Model != "probe-unknown-model" {
		t.Fatalf("audit line = %+v", lines[0])
	}
	if lines[0].Status != http.StatusNotFound {
		t.Fatalf("audit status = %d", lines[0].Status)
	}
}

// TestGenerateRoute_DefaultHandlerIsTheRealGenerationPath pins that a nil
// Config.Generate installs the generation handler (not the 501 placeholder):
// a resolved model now completes a real upstream turn. The placeholder stays
// reachable only as an explicit override (TestGenerate_ExplicitUnimplementedOverride).
func TestGenerateRoute_DefaultHandlerIsTheRealGenerationPath(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "finishReason") {
		t.Fatalf("no completed stream: %s", body)
	}
	if m.count() != 1 {
		t.Fatalf("upstream calls = %d, want 1", m.count())
	}
}

func TestGenerateRoute_SeamErrorMapping(t *testing.T) {
	spy := &recordingGenerate{err: &GenerateError{Code: http.StatusBadGateway, Status: statusUnavailable, Message: "provider mock-provider: transport failure"}}
	cfg := testConfig(t)
	cfg.Generate = spy
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Message != "provider mock-provider: transport failure" {
		t.Fatalf("message = %q", env.Error.Message)
	}

	// An unmapped error becomes a fixed 500 with no internal detail on the wire.
	spy.err = errBoom
	resp, body = post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Message != "internal adapter error" || strings.Contains(body, "boom") {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom: secret-ish detail" }

func TestGenerateRoute_HousekeepingIsAnsweredLocally(t *testing.T) {
	spy := &recordingGenerate{}
	cfg := testConfig(t)
	cfg.Generate = spy
	srv := startServer(t, cfg)

	request := readGolden(t, "gf-29-request.json")
	resp, body := post(t, srv.URL()+"/v1beta/models/gemini-3.1-flash-lite-preview:streamGenerateContent?alt=sse", nil, request)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if want := string(readGolden(t, "gf-29-expected.sse")); body != want {
		t.Fatalf("housekeeping stream mismatch:\n got %q\nwant %q", body, want)
	}
	if spy.count() != 0 {
		t.Fatal("housekeeping must never reach the generation seam")
	}

	// The non-stream verb answers one JSON body with the same title.
	resp, body = post(t, srv.URL()+"/v1beta/models/gemini-3.1-flash-lite-preview:generateContent", nil, request)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	var parsed geminiwire.GenerateContentResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("non-stream housekeeping body: %v (%s)", err, body)
	}
	if len(parsed.Candidates) != 1 || parsed.Candidates[0].FinishReason != "STOP" {
		t.Fatalf("non-stream housekeeping response = %s", body)
	}
	if parsed.UsageMetadata == nil || parsed.UsageMetadata.CandidatesTokenCount != 60 {
		t.Fatalf("usage = %+v", parsed.UsageMetadata)
	}
	if spy.count() != 0 {
		t.Fatal("housekeeping must never reach the generation seam")
	}
}

func TestGenerateRoute_UnknownGeminiShapedVerbIs404(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:countTokens", nil, chatBody())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusNotFound {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

// ---------------------------------------------------------------------------
// GET /v1beta/models listing
// ---------------------------------------------------------------------------

func TestListModels_RegistryShape(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := get(t, srv.URL()+"/v1beta/models?pageSize=1000", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	var out geminiwire.ListModelsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("listing body: %v (%s)", err, body)
	}
	if out.NextPageToken != "" {
		t.Fatalf("nextPageToken = %q", out.NextPageToken)
	}
	if len(out.Models) != 3 {
		t.Fatalf("models = %d, want 3 registered slugs", len(out.Models))
	}
	first := out.Models[0]
	if first.Name != "models/probe-gemini" {
		t.Fatalf("name = %q", first.Name)
	}
	if first.DisplayName != "probe-gemini (mock-provider)" {
		t.Fatalf("displayName = %q", first.DisplayName)
	}
	if first.Description != "agy-pro adapter model" {
		t.Fatalf("description = %q", first.Description)
	}
	if first.InputTokenLimit != 1048576 || first.OutputTokenLimit != 65536 {
		t.Fatalf("token limits = %d/%d", first.InputTokenLimit, first.OutputTokenLimit)
	}
	if len(first.SupportedGenerationMethods) != 2 ||
		first.SupportedGenerationMethods[0] != "generateContent" ||
		first.SupportedGenerationMethods[1] != "streamGenerateContent" {
		t.Fatalf("supportedGenerationMethods = %v", first.SupportedGenerationMethods)
	}
}

func TestListModels_WrongMethodIs405(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := post(t, srv.URL()+"/v1beta/models", nil, chatBody())
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusInvalidArgument {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func TestListModels_NilRegistryIsEmptyPage(t *testing.T) {
	cfg := testConfig(t)
	cfg.Registry = nil
	srv := startServer(t, cfg)
	_, body := get(t, srv.URL()+"/v1beta/models", nil)
	if strings.TrimSpace(body) != `{"models":[],"nextPageToken":""}` {
		t.Fatalf("empty listing = %s", body)
	}
}

// ---------------------------------------------------------------------------
// /v1internal envelope branch
// ---------------------------------------------------------------------------

func TestInternalGenerate_DispatchesWithEnvelopeContext(t *testing.T) {
	spy := &recordingGenerate{}
	cfg := testConfig(t)
	cfg.Generate = spy
	srv := startServer(t, cfg)

	resp, body := post(t, srv.URL()+"/v1internal:streamGenerateContent?alt=sse", nil, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	call := spy.last()
	if call == nil {
		t.Fatal("the seam was never reached")
	}
	if call.Surface != surfaceV1Internal {
		t.Fatalf("surface = %q", call.Surface)
	}
	if call.Envelope == nil || call.Envelope.Model != "probe-gemini" {
		t.Fatalf("envelope = %+v", call.Envelope)
	}
	if call.Request == nil || len(call.Request.Contents) != 1 {
		t.Fatalf("inner request = %+v", call.Request)
	}
	if call.Target.Model.ID != "probe-gemini" {
		t.Fatalf("target = %q", call.Target.Model.ID)
	}
}

func TestInternalGenerate_MalformedEnvelopeIs400(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := post(t, srv.URL()+"/v1internal:generateContent", nil, []byte("{not json"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusInvalidArgument {
		t.Fatalf("envelope = %+v", env.Error)
	}

	resp, body = post(t, srv.URL()+"/v1internal:generateContent", nil, []byte(`{"project":"p"}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing request payload: status %d body %s", resp.StatusCode, body)
	}
}

func TestInternalGenerate_MissingModelIs400(t *testing.T) {
	srv := startServer(t, testConfig(t))
	body := []byte(`{"project":"p","request":{"contents":[]}}`)
	resp, raw := post(t, srv.URL()+"/v1internal:generateContent", nil, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
}

func TestInternalGenerate_WrongMethodIs405(t *testing.T) {
	srv := startServer(t, testConfig(t))
	resp, body := do(t, http.MethodGet, srv.URL()+"/v1internal:generateContent", nil, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// /v1internal non-generation RPCs
// ---------------------------------------------------------------------------

// TestInternalRPC_MinimalDefaults pins the exact default bytes. The loadCodeAssist
// and fetchUserInfo rows are byte-aligned with the known-good probe payloads the
// design session served to the real agy binary (FIX-57); see
// TestInternalRPC_DefaultBodiesMatchProbe for the full table with sources.
func TestInternalRPC_MinimalDefaults(t *testing.T) {
	srv := startServer(t, testConfig(t))
	cases := []struct {
		rpc  string
		want string
	}{
		{"loadCodeAssist", `{"cloudaicompanion_project":"mock-project-1234","current_tier":{"id":"free-tier","name":"Free"},"paid_tier":{"id":"free-tier","name":"Free"}}`},
		{"fetchUserInfo", `{"user_settings":{},"user_tags":[],"region_code":"US"}`},
		{"fetchAdminControls", `{}`},
		{"onboardUser", `{}`},
		{"fetchAvailableModels", `{"models":{},"agent_model_sorts":[]}`},
		{"retrieveUserQuotaSummary", `{"userStatus":{"groups":[]}}`},
		{"listExperiments", `{"experiment_ids":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.rpc, func(t *testing.T) {
			resp, body := post(t, srv.URL()+"/v1internal:"+tc.rpc, nil, []byte(`{}`))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s", resp.StatusCode, body)
			}
			if got := strings.TrimSpace(body); got != tc.want {
				t.Fatalf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInternalRPC_HookOverride(t *testing.T) {
	cfg := testConfig(t)
	catalog := []byte(`{"models":{"ext-1":{"model":326,"display_name":"Ext"}},"agent_model_sorts":[{"groups":[{"model_ids":["ext-1"]}]}]}`)
	cfg.CatalogFunc = func() ([]byte, error) { return catalog, nil }
	cfg.RPCHooks = map[string]func(body []byte) ([]byte, error){
		"fetchUserInfo": func(body []byte) ([]byte, error) {
			if string(body) != `{"trace":"x"}` {
				t.Errorf("hook received body %s", body)
			}
			return []byte(`{"user_settings":{"k":"v"},"user_tags":{},"region_code":"KR"}`), nil
		},
	}
	srv := startServer(t, cfg)

	resp, body := post(t, srv.URL()+"/v1internal:fetchAvailableModels", nil, []byte(`{"project":"p"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if body != string(catalog) {
		t.Fatalf("catalog body = %s, want verbatim %s", body, catalog)
	}

	resp, body = post(t, srv.URL()+"/v1internal:fetchUserInfo", nil, []byte(`{"trace":"x"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"user_settings":{"k":"v"},"user_tags":{},"region_code":"KR"}` {
		t.Fatalf("hook body = %s", body)
	}

	// An unhooked RPC still answers its default.
	_, body = post(t, srv.URL()+"/v1internal:listExperiments", nil, []byte(`{}`))
	if strings.TrimSpace(body) != `{"experiment_ids":[]}` {
		t.Fatalf("default body = %s", body)
	}
}

func TestInternalRPC_HookFailures(t *testing.T) {
	cfg := testConfig(t)
	cfg.RPCHooks = map[string]func(body []byte) ([]byte, error){
		"loadCodeAssist": func(body []byte) ([]byte, error) { return nil, errBoom },
		"fetchUserInfo":  func(body []byte) ([]byte, error) { return []byte("not json"), nil },
	}
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1internal:loadCodeAssist", nil, []byte(`{}`))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusInternal {
		t.Fatalf("envelope = %+v", env.Error)
	}
	resp, body = post(t, srv.URL()+"/v1internal:fetchUserInfo", nil, []byte(`{}`))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

// TestInternalRPC_UnknownRPCIs404WithCapture pins the writeTrajectoryAcls
// posture (G10, blind-qa-g4-rerun2-report.md finding 2): the RPC stays UNSERVED
// and answers the §5.1 explicit 404.
//
// Decision: PIN, not IMPLEMENT. agy POSTs /v1internal:writeTrajectoryAcls once
// per print run (9 requests across the G4 sandbox) and provably recovers from
// the 404: exit 0, PROBE_OK, no retry storm. Serving a 200 here would be a fake
// success (the mesh owns no trajectory, so there are no ACLs to persist), and
// this unit cannot verify against the real binary that agy accepts a synthesized
// envelope. The exact agy contract IS known from the binary's own proto
// descriptor (google.internal.cloud.code.v1internal.WriteTrajectoryACLsRequest:
// trajectory_id, attributed_owner; WriteTrajectoryACLsResponse: no fields, so a
// protobuf-JSON `{}` would parse), so a live gate can land the one-line upgrade
// (add "writeTrajectoryAcls" to internalRPCNames and answer `{}`) once it probes
// it. Until then the 404 is the honest answer and costs nothing: no provider
// call, no key, no state.
func TestInternalRPC_UnknownRPCIs404WithCapture(t *testing.T) {
	cfg := testConfig(t)
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1internal:writeTrajectoryAcls", nil, []byte(`{}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	env := decodeError(t, body)
	if env.Error.Code != http.StatusNotFound || env.Error.Status != statusNotFound {
		t.Fatalf("envelope = %+v", env.Error)
	}
	// The catch-all wording, not the serveInternalRPC default-branch wording: the
	// route is not registered in the mux at all, so it reaches serveNotFound.
	if want := "route /v1internal:writeTrajectoryAcls is not served by this adapter"; env.Error.Message != want {
		t.Fatalf("message = %q, want %q", env.Error.Message, want)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if lines[0].Route != routeUnknown {
		t.Fatalf("audit route = %q", lines[0].Route)
	}
	if lines[0].Method != http.MethodPost || lines[0].Path != "/v1internal:writeTrajectoryAcls" {
		t.Fatalf("audit capture = %+v", lines[0])
	}
	if lines[0].Status != http.StatusNotFound {
		t.Fatalf("audit status = %d", lines[0].Status)
	}
}

func TestCatchAll404_LogsMethodAndPathOnly(t *testing.T) {
	cfg := testConfig(t)
	srv := startServer(t, cfg)
	resp, body := get(t, srv.URL()+"/v1/models/probe-gemini:embedContent", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusNotFound {
		t.Fatalf("envelope = %+v", env.Error)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if lines[0].Route != routeUnknown || lines[0].Path != "/v1/models/probe-gemini:embedContent" {
		t.Fatalf("audit capture = %+v", lines[0])
	}

	// A gemini-shaped verb under the models subtree is a 404 too, even with a
	// non-POST method (§5.1: unknown-but-gemini-shaped, not a 405).
	resp, body = do(t, http.MethodGet, srv.URL()+"/v1beta/models/probe-gemini:countTokens", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET :countTokens status %d body %s", resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// resolution order
// ---------------------------------------------------------------------------

func TestResolveModel_CustomSeamOverridesDefault(t *testing.T) {
	spy := &recordingGenerate{}
	cfg := testConfig(t)
	cfg.Generate = spy
	target := providers.Target{Provider: providers.Provider{Name: "hybrid"}, Model: providers.Model{ID: "hybrid-pick"}}
	cfg.ResolveModel = func(name string) (providers.Target, bool, error) {
		if name == "tier-fallback-id" {
			return target, true, nil
		}
		return providers.Target{}, false, nil
	}
	srv := startServer(t, cfg)

	// The seam resolves a name the registry does not carry.
	resp, body := post(t, srv.URL()+"/v1beta/models/tier-fallback-id:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if call := spy.last(); call.Target.Model.ID != "hybrid-pick" {
		t.Fatalf("target = %+v", call.Target)
	}

	// The seam's miss is still the allow-list 404.
	resp, body = post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

// contextSpy records the request context's rendering so the test can prove no
// credential rides in a context value.
type contextSpy struct {
	mu       sync.Mutex
	rendered []string
}

func (s *contextSpy) Generate(w http.ResponseWriter, r *http.Request, g *GenerateContext) error {
	s.mu.Lock()
	s.rendered = append(s.rendered, fmt.Sprintf("%v", r.Context()))
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	return nil
}

func TestSeam_ContextCarriesNoCredential(t *testing.T) {
	const key = "install-nonce-secret-value"
	spy := &contextSpy{}
	cfg := testConfig(t)
	cfg.AuthKey = key
	cfg.Generate = spy
	srv := startServer(t, cfg)

	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent",
		map[string]string{headerAPIKey: key}, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.rendered) != 1 {
		t.Fatalf("seam calls = %d", len(spy.rendered))
	}
	// The only context value the pipeline installs is the audit record; it
	// must render without the credential anywhere in it.
	if strings.Contains(spy.rendered[0], key) {
		t.Fatalf("the request context carries the credential: %s", spy.rendered[0])
	}
	if !strings.Contains(spy.rendered[0], "auditRecord") {
		t.Fatalf("expected the audit record on the context: %s", spy.rendered[0])
	}
}

func TestConcurrentRequests_ShareThePipelineSafely(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, body := post(t, url, nil, chatBody())
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusTooManyRequests {
				errs <- fmt.Errorf("status %d body %s", resp.StatusCode, body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Every request left exactly one audit line, and none of them is corrupt.
	lines := readAuditLines(t, cfg.AuditPath, workers)
	if len(lines) != workers {
		t.Fatalf("audit lines = %d, want %d", len(lines), workers)
	}
}

func TestResolveRegistrySlug_Rules(t *testing.T) {
	reg := testRegistry()
	cases := []struct {
		name    string
		wantID  string
		wantHit bool
	}{
		{"probe-gemini", "probe-gemini", true},
		{"127.0.0.1:8791/models/probe-gemini", "probe-gemini", true},
		{"evergreen:127.0.0.1:8791/models/probe-evergreen", "probe-evergreen", true},
		{"blade:127.0.0.1:8791/models/probe-blade", "probe-blade", true},
		{"probe-unknown", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		target, ok := resolveRegistrySlug(reg, tc.name)
		if ok != tc.wantHit {
			t.Fatalf("%q: hit = %v, want %v", tc.name, ok, tc.wantHit)
		}
		if ok && target.Model.ID != tc.wantID {
			t.Fatalf("%q: resolved %q, want %q", tc.name, target.Model.ID, tc.wantID)
		}
	}
	if _, ok := resolveRegistrySlug(nil, "probe-gemini"); ok {
		t.Fatal("a nil registry must resolve nothing")
	}
}
