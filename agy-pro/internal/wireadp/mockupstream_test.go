package wireadp

// Mock upstream provider: a loopback httptest server that speaks the OpenAI
// chat.completions wire, records every call, and lets each test script the
// response. No real provider, no real registry, no real network beyond
// loopback.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/providers"
)

// upstreamCall is one recorded outbound request.
type upstreamCall struct {
	Method      string
	Path        string
	RawQuery    string
	HeaderNames []string
	HasAuthz    bool
	AuthzScheme string
	// AuthzValue is the raw outbound Authorization value. It is captured only
	// so a pin can prove an inbound credential never becomes the outbound one;
	// it is a planted test value, never a real key.
	AuthzValue string
	LeakedAgy  []string // inbound agy headers that must never be forwarded
	Body       map[string]any
	RawBody    []byte
}

// mockUpstream is the scripted OpenAI-compatible upstream.
type mockUpstream struct {
	server *httptest.Server

	mu    sync.Mutex
	calls []upstreamCall
	// script decides the response for the n-th call (0-based). Nil means the
	// default two-delta SSE completion.
	script func(n int, w http.ResponseWriter)
}

func newMockUpstream(t *testing.T) *mockUpstream {
	t.Helper()
	m := &mockUpstream{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		names := make([]string, 0, len(r.Header))
		for name := range r.Header {
			names = append(names, name)
		}
		authz := r.Header.Get("Authorization")
		call := upstreamCall{
			Method:      r.Method,
			Path:        r.URL.Path,
			RawQuery:    r.URL.RawQuery,
			HeaderNames: names,
			Body:        body,
			RawBody:     raw,
		}
		if authz != "" {
			call.HasAuthz = true
			call.AuthzScheme, _, _ = strings.Cut(authz, " ")
			call.AuthzValue = authz
		}
		for _, leaked := range []string{"X-Goog-Api-Key", "X-Agy-Pro-Install", "X-Cloudaicompanion-Trace-Id"} {
			if r.Header.Get(leaked) != "" {
				call.LeakedAgy = append(call.LeakedAgy, leaked)
			}
		}
		if ua := r.Header.Get("User-Agent"); strings.Contains(ua, "google-genai-sdk") || strings.Contains(ua, "antigravity") {
			call.LeakedAgy = append(call.LeakedAgy, "User-Agent")
		}
		m.mu.Lock()
		n := len(m.calls)
		m.calls = append(m.calls, call)
		script := m.script
		m.mu.Unlock()

		if script != nil {
			script(n, w)
			return
		}
		writeSSECompletion(w, defaultDeltas, defaultUsage)
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockUpstream) close() { m.server.Close() }

// url is the registry BaseURL: Provider.Endpoint() appends /chat/completions.
func (m *mockUpstream) url() string { return m.server.URL }

func (m *mockUpstream) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockUpstream) call(i int) upstreamCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i < 0 || i >= len(m.calls) {
		return upstreamCall{}
	}
	return m.calls[i]
}

func (m *mockUpstream) last() upstreamCall { return m.call(m.count() - 1) }

// setScript installs a responder for subsequent calls.
func (m *mockUpstream) setScript(fn func(n int, w http.ResponseWriter)) {
	m.mu.Lock()
	m.script = fn
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// response writers
// ---------------------------------------------------------------------------

var (
	defaultDeltas = []string{"안녕하세요", " 세계 🚀"}
	defaultUsage  = map[string]any{"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17}
)

// writeSSEFrame writes one already-marshaled chunk as an SSE data frame
// without touching the response headers.
func writeSSEFrame(w http.ResponseWriter, chunk map[string]any) {
	payload, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// contentChunk builds one content delta chunk.
func contentChunk(delta string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
	}
}

// writeSSECompletion streams one OpenAI completion as SSE.
func writeSSECompletion(w http.ResponseWriter, deltas []string, usage map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
	for _, d := range deltas {
		writeSSEFrame(w, contentChunk(d))
	}
	final := map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}
	if usage != nil {
		final["usage"] = usage
	}
	writeSSEFrame(w, final)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeJSONCompletion writes one non-streaming completion.
func writeJSONCompletion(w http.ResponseWriter, content string, usage map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"id":     "chatcmpl-mock",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	}
	if usage != nil {
		body["usage"] = usage
	}
	payload, _ := json.Marshal(body)
	_, _ = w.Write(payload)
}

// writeStatus answers with a plain upstream error status and body.
func writeStatus(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// abortMidStream writes n SSE deltas and then drops the connection without a
// clean close, the §9.3 / GF-28 scenario. Each delta is flushed first so the
// client really did receive it before the abort.
func abortMidStream(w http.ResponseWriter, deltas ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, d := range deltas {
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": d}, "finish_reason": nil}},
		})
		_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	panic(http.ErrAbortHandler)
}

// truncateMidStream writes n SSE deltas and then ENDS THE RESPONSE CLEANLY: no
// finish_reason chunk, no [DONE] sentinel, and no connection error. This is the
// G10F-1 scenario, and it is the one a read-error check cannot see: the HTTP
// body terminated normally, so only the absence of a terminal signal reveals
// that the turn was cut short. A provider that drops mid-body, or a proxy that
// ends the response, produces exactly this shape.
func truncateMidStream(w http.ResponseWriter, deltas ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, d := range deltas {
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": d}, "finish_reason": nil}},
		})
		_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// cleanSSEStream writes the canonical clean upstream turn: one content delta, a
// chunk carrying finish_reason "stop", and the [DONE] sentinel. It is the
// G10F-1 negative control: a stream that reached its terminal signal must
// complete normally, never an error.
func cleanSSEStream(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
	writeSSEFrame(w, contentChunk(content))
	writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   defaultUsage})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// replayGolden writes a golden provider SSE fixture and then drops the
// connection, so the adapter sees exactly the fixture's bytes followed by a
// mid-stream disconnect.
func replayGoldenAbort(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, line := range strings.Split(string(readGolden(t, name)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return
		}
	}
	if flusher != nil {
		flusher.Flush()
	}
	// Let the client read the flushed bytes before the abort lands, which keeps
	// the folded turn non-empty.
	time.Sleep(150 * time.Millisecond)
	panic(http.ErrAbortHandler)
}

// ---------------------------------------------------------------------------
// test config
// ---------------------------------------------------------------------------

const testKeyEnv = "AGY_PRO_WIREADP_TEST_KEY"

// testUpstreamConfig builds a config whose single registry model points at the
// mock upstream, plus the registry itself for direct inspection.
func testUpstreamConfig(t *testing.T, m *mockUpstream, modelIDs ...string) (Config, *providers.Registry) {
	t.Helper()
	if len(modelIDs) == 0 {
		modelIDs = []string{"probe-gemini"}
	}
	models := make([]providers.Model, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, providers.Model{ID: id, ContextLength: 1048576, MaxOutputTokens: 65536})
	}
	reg := &providers.Registry{
		Providers: []providers.Provider{{
			Name:    "mock-provider",
			BaseURL: m.url(),
			Models:  models,
		}},
	}
	cfg := testConfig(t)
	cfg.Registry = reg
	return cfg, reg
}

// thinkingConfig is the request's generationConfig for the tests that gate
// thought parts: {"thinkingConfig":{"includeThoughts":…,"thinkingBudget":…}}.
func thinkingConfig(includeThoughts bool, budget int) map[string]any {
	return map[string]any{
		"thinkingConfig": map[string]any{"includeThoughts": includeThoughts, "thinkingBudget": budget},
	}
}

// chatRequestBody builds a minimal Gemini request body with an optional
// generationConfig.
func chatRequestBody(generationConfig map[string]any) []byte {
	req := map[string]any{
		"contents": []any{map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "안녕하세요"}},
		}},
	}
	if generationConfig != nil {
		req["generationConfig"] = generationConfig
	}
	body, _ := json.Marshal(req)
	return body
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// readFileForTest reads a file for the audit assertions.
func readFileForTest(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// errString is a trivial error type for the goroutine results.
type errString string

func (e errString) Error() string { return string(e) }
