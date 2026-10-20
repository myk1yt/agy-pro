package wireadp

// The generation path through the real server, the real codec and a mock
// upstream: translate -> upstream -> SSE/JSON emit -> §9 error mapping.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/geminiwire"
	"agypro/internal/openaiup"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

func streamURL(srv *Server, model string) string {
	return srv.URL() + "/v1beta/models/" + model + ":streamGenerateContent?alt=sse"
}

func generateURL(srv *Server, model string) string {
	return srv.URL() + "/v1beta/models/" + model + ":generateContent"
}

// sseFrames splits an SSE body into its data payloads, dropping comments.
func sseFrames(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return out
}

func decodeFrames(t *testing.T, body string) []*geminiwire.GenerateContentResponse {
	t.Helper()
	frames := sseFrames(t, body)
	out := make([]*geminiwire.GenerateContentResponse, 0, len(frames))
	for _, f := range frames {
		var resp geminiwire.GenerateContentResponse
		if err := json.Unmarshal([]byte(f), &resp); err != nil {
			t.Fatalf("frame is not a GenerateContentResponse: %v (%s)", err, f)
		}
		out = append(out, &resp)
	}
	return out
}

// decodeEnvelopeFrames parses the /v1internal SSE stream: every data line is a
// {"response": <chunk>} envelope wrapping one GenerateContentResponse.
func decodeEnvelopeFrames(t *testing.T, body string) []*geminiwire.GenerateContentResponse {
	t.Helper()
	var out []*geminiwire.GenerateContentResponse
	for _, frame := range sseFrames(t, body) {
		var wrapped geminiwire.V1InternalResponse
		if err := json.Unmarshal([]byte(frame), &wrapped); err != nil {
			t.Fatalf("frame is not an envelope: %v (%s)", err, frame)
		}
		var inner geminiwire.GenerateContentResponse
		if err := json.Unmarshal(wrapped.Response, &inner); err != nil {
			t.Fatalf("envelope payload is not a response: %v (%s)", err, wrapped.Response)
		}
		out = append(out, &inner)
	}
	return out
}

// ---------------------------------------------------------------------------
// happy paths
// ---------------------------------------------------------------------------

// TestGenerate_TextStreamMatchesGF22 replays the GF-22 provider stream through
// the whole stack and pins the adapter's bytes against the golden expectation.
func TestGenerate_TextStreamMatchesGF22(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{SupportsThinking: true}
	srv := startServer(t, cfg)

	m.setScript(func(n int, w http.ResponseWriter) {
		// The GF-22 provider stream, verbatim.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readGolden(t, "gf-22-request.sse"))
	})

	body := chatRequestBody(thinkingConfig(true, -1))
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if want := string(readGolden(t, "gf-22-expected.sse")); raw != want {
		t.Fatalf("stream bytes mismatch:\n got %q\nwant %q", raw, want)
	}
	// The golden stream carries no [DONE] sentinel on the Gemini wire.
	if strings.Contains(raw, "[DONE]") {
		t.Fatal("the Gemini wire must never carry [DONE]")
	}
	if m.count() != 1 {
		t.Fatalf("upstream calls = %d, want 1", m.count())
	}
}

func TestGenerate_DefaultDeltasStreamAndUsageOnFinalChunk(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	// The adapter aggregates the turn, so the two provider deltas arrive as
	// one text part followed by the final chunk (§8.4-§8.5).
	frames := decodeFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want text + final: %s", len(frames), raw)
	}
	first := frames[0].Candidates[0]
	if first.FinishReason != "" {
		t.Fatalf("frame 0 carries a finishReason early: %s", raw)
	}
	if len(first.Content.Parts) != 1 || first.Content.Parts[0].Thought {
		t.Fatalf("frame 0 parts = %+v", first.Content.Parts)
	}
	if got := first.Content.Parts[0].Text; got != "안녕하세요 세계 🚀" {
		t.Fatalf("aggregated text = %q (mid-rune Korean and the emoji must survive)", got)
	}
	final := frames[1].Candidates[0]
	if final.FinishReason != "STOP" {
		t.Fatalf("final finishReason = %q", final.FinishReason)
	}
	if len(final.Content.Parts) != 0 {
		t.Fatalf("final chunk parts = %+v, want empty", final.Content.Parts)
	}
	usage := frames[1].UsageMetadata
	if usage == nil || usage.PromptTokenCount != 12 || usage.CandidatesTokenCount != 5 || usage.TotalTokenCount != 17 {
		t.Fatalf("usage = %+v", usage)
	}
	if frames[0].UsageMetadata != nil {
		t.Fatal("usage must ride the final chunk only")
	}
}

func TestGenerate_ThoughtGating(t *testing.T) {
	cases := []struct {
		name          string
		caps          providers.Capabilities
		genConfig     map[string]any
		wantThought   bool
		wantEffortKey bool
	}{
		{
			name:        "includeThoughts on a thinking model surfaces the thought part",
			caps:        providers.Capabilities{SupportsThinking: true},
			genConfig:   thinkingConfig(true, -1),
			wantThought: true,
		},
		{
			name:        "includeThoughts off drops the reasoning deltas",
			caps:        providers.Capabilities{SupportsThinking: true},
			genConfig:   thinkingConfig(false, -1),
			wantThought: false,
		},
		{
			name:        "a non-thinking model drops them even when asked",
			caps:        providers.Capabilities{},
			genConfig:   thinkingConfig(true, 8192),
			wantThought: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			cfg, reg := testUpstreamConfig(t, m)
			reg.Providers[0].Models[0].Capabilities = tc.caps
			srv := startServer(t, cfg)
			m.setScript(func(n int, w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(readGolden(t, "gf-22-request.sse"))
			})

			resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(tc.genConfig))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s", resp.StatusCode, raw)
			}
			sawThought := false
			for _, frame := range decodeFrames(t, raw) {
				for _, p := range frame.Candidates[0].Content.Parts {
					if p.Thought {
						sawThought = true
					}
				}
			}
			if sawThought != tc.wantThought {
				t.Fatalf("thought parts = %v, want %v: %s", sawThought, tc.wantThought, raw)
			}
			// The thought text must never leak into a plain part.
			for _, frame := range decodeFrames(t, raw) {
				for _, p := range frame.Candidates[0].Content.Parts {
					if !p.Thought && strings.Contains(p.Text, "계획을 세웁니다") {
						t.Fatalf("thought text leaked into a user-visible part: %s", raw)
					}
				}
			}
		})
	}
}

// TestGenerate_ReasoningEffortNoneIsSentAsIs pins the documented divergence
// from openaiup.ApplyEffort on this path (§7.6 decision 1 of the U3 report).
func TestGenerate_ReasoningEffortNoneIsSentAsIs(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{SupportsThinking: true}
	srv := startServer(t, cfg)

	resp, _ := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(thinkingConfig(true, 0)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body := m.last().Body
	if body["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %v, want \"none\"", body["reasoning_effort"])
	}
}

func TestGenerate_NonStreamBranch(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		writeJSONCompletion(w, "한 번에 답합니다", defaultUsage)
	})

	resp, raw := post(t, generateURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var out geminiwire.GenerateContentResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("body: %v (%s)", err, raw)
	}
	if len(out.Candidates) != 1 || out.Candidates[0].FinishReason != "STOP" {
		t.Fatalf("candidates = %+v", out.Candidates)
	}
	if got := out.Candidates[0].Content.Parts[0].Text; got != "한 번에 답합니다" {
		t.Fatalf("text = %q", got)
	}
	if out.UsageMetadata == nil || out.UsageMetadata.TotalTokenCount != 17 {
		t.Fatalf("usage = %+v", out.UsageMetadata)
	}
	if m.last().Body["stream"] != nil {
		t.Fatalf("non-stream branch must not set stream: %v", m.last().Body)
	}
}

// ---------------------------------------------------------------------------
// tool round trip
// ---------------------------------------------------------------------------

// TestGenerate_ToolCallRoundTrip drives the full §7.3 cycle: the provider
// streams tool-call argument FRAGMENTS, the adapter emits one complete
// functionCall part, and the resent history's functionResponse pairs onto the
// id the adapter emitted.
func TestGenerate_ToolCallRoundTrip(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)

	// Turn 1: fragmented tool-call arguments around multi-byte runes.
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readGolden(t, "gf-22-request.sse"))
	})
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn 1 status %d body %s", resp.StatusCode, raw)
	}
	frames := decodeFrames(t, raw)
	var call *geminiwire.FunctionCall
	for _, frame := range frames {
		for _, p := range frame.Candidates[0].Content.Parts {
			if p.FunctionCall != nil {
				call = p.FunctionCall
			}
		}
	}
	if call == nil {
		t.Fatalf("no functionCall part emitted: %s", raw)
	}
	if call.Name != "view_file" {
		t.Fatalf("call name = %q", call.Name)
	}
	if call.ID != "call_gf22" {
		t.Fatalf("call id = %q", call.ID)
	}
	args, err := json.Marshal(call.Args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if string(args) != `{"path":"한국어.md"}` {
		t.Fatalf("args = %s, want the reassembled object", args)
	}

	// Turn 2: agy resends the full history with the functionResponse carrying
	// the id it echoed back. The outbound tool message must pair onto the call
	// id, and the outbound assistant message must carry the same id.
	m.setScript(func(n int, w http.ResponseWriter) {
		writeSSECompletion(w, []string{"도구 실행 완료"}, defaultUsage)
	})
	history := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "안녕하세요"}}},
			map[string]any{"role": "model", "parts": []any{
				map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Name, "args": call.Args}},
			}},
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"functionResponse": map[string]any{
					"id": call.ID, "name": call.Name,
					"response": map[string]any{"output": "파일 내용"},
				}},
			}},
		},
	}
	body, _ := json.Marshal(history)
	resp, raw = post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn 2 status %d body %s", resp.StatusCode, raw)
	}
	if m.count() != 2 {
		t.Fatalf("upstream calls = %d, want 2", m.count())
	}
	messages, ok := m.last().Body["messages"].([]any)
	if !ok {
		t.Fatalf("outbound messages = %T", m.last().Body["messages"])
	}
	var toolID, assistantCallID string
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		switch msg["role"] {
		case "tool":
			toolID, _ = msg["tool_call_id"].(string)
			if msg["name"] != "view_file" {
				t.Fatalf("tool message name = %v", msg["name"])
			}
			if content, _ := msg["content"].(string); content != "파일 내용" {
				t.Fatalf("tool message content = %q", content)
			}
		case "assistant":
			calls, _ := msg["tool_calls"].([]any)
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				assistantCallID, _ = cm["id"].(string)
			}
		}
	}
	if toolID == "" {
		t.Fatal("no outbound tool message was produced from the resent history")
	}
	if toolID != call.ID || assistantCallID != call.ID {
		t.Fatalf("pairing broken: tool_call_id=%q assistant id=%q want %q", toolID, assistantCallID, call.ID)
	}
}

func TestGenerate_SystemInstructionAndToolsForwarded(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{SupportsTools: true}
	srv := startServer(t, cfg)

	req := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "파일 읽어줘"}}}},
		"systemInstruction": map[string]any{"role": "user", "parts": []any{
			map[string]any{"text": "<system one>"},
			map[string]any{"text": "<system two>"},
		}},
		"tools": []any{map[string]any{"functionDeclarations": []any{
			map[string]any{"name": "view_file", "description": "read a file",
				"parametersJsonSchema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		}}},
		"generationConfig": map[string]any{"maxOutputTokens": 65536, "stopSequences": []any{"a", "b", "c", "d", "e", "f"}},
	}
	body, _ := json.Marshal(req)
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	out := m.last().Body
	messages, _ := out["messages"].([]any)
	if len(messages) < 2 {
		t.Fatalf("messages = %d, want system + user", len(messages))
	}
	system, _ := messages[0].(map[string]any)
	if system["role"] != "system" {
		t.Fatalf("messages[0] role = %v", system["role"])
	}
	if content, _ := system["content"].(string); content != "<system one>\n<system two>" {
		t.Fatalf("system content = %q", content)
	}
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", out["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type = %v", tool["type"])
	}
	// maxOutputTokens clamps to the registry cap (65536 -> 65536 here) and the
	// stop list truncates to 4.
	if out["max_tokens"] != float64(65536) {
		t.Fatalf("max_tokens = %v", out["max_tokens"])
	}
	stop, _ := out["stop"].([]any)
	if len(stop) != 4 {
		t.Fatalf("stop = %v, want 4", out["stop"])
	}
	if out["stream"] != true {
		t.Fatalf("stream = %v", out["stream"])
	}
	if opts, ok := out["stream_options"].(map[string]any); !ok || opts["include_usage"] != true {
		t.Fatalf("stream_options = %v", out["stream_options"])
	}
}

func TestGenerate_ToolsDroppedWithoutCaps(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)

	req := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
		"tools": []any{map[string]any{"functionDeclarations": []any{
			map[string]any{"name": "view_file"},
		}}},
	}
	body, _ := json.Marshal(req)
	resp, _ := post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, exists := m.last().Body["tools"]; exists {
		t.Fatalf("tools must be dropped without SupportsTools: %v", m.last().Body["tools"])
	}
	if _, exists := m.last().Body["tool_choice"]; exists {
		t.Fatalf("tool_choice must be dropped with tools: %v", m.last().Body["tool_choice"])
	}
}

// ---------------------------------------------------------------------------
// §6.3 family quirks
// ---------------------------------------------------------------------------

// quirkRequest asks for the sampling knobs and a candidate count so the quirk
// table has something to act on.
func quirkRequest() []byte {
	req := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
		"generationConfig": map[string]any{
			"maxOutputTokens": 4096,
			"temperature":     0.7,
			"topP":            0.9,
			"candidateCount":  3,
		},
	}
	body, _ := json.Marshal(req)
	return body
}

func TestGenerate_FamilyQuirks(t *testing.T) {
	cases := []struct {
		name           string
		modelID        string
		family         string
		wantTokenField string
		wantSampling   bool
		wantStreamOpts bool
	}{
		{"openai o-series uses max_completion_tokens and drops sampling", "o3-mini", "", "max_completion_tokens", false, true},
		{"openai gpt-5 uses max_completion_tokens", "gpt-5-turbo", "", "max_completion_tokens", false, true},
		{"vendor-prefixed o-series still matches", "openai/o4-mini", "", "max_completion_tokens", false, true},
		{"a plain model keeps max_tokens and sampling", "probe-gemini", "", "max_tokens", true, true},
		{"an explicit family is honored for stream_options", "probe-blade", "claude", "max_tokens", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			cfg, reg := testUpstreamConfig(t, m, tc.modelID)
			reg.Providers[0].Models[0].Family = tc.family
			srv := startServer(t, cfg)

			resp, raw := post(t, streamURL(srv, tc.modelID), nil, quirkRequest())
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s", resp.StatusCode, raw)
			}
			body := m.last().Body
			if _, ok := body[tc.wantTokenField]; !ok {
				t.Fatalf("token field %q missing from %v", tc.wantTokenField, body)
			}
			if _, ok := body["max_tokens"]; ok && tc.wantTokenField != "max_tokens" {
				t.Fatalf("max_tokens must be renamed for this family: %v", body)
			}
			if _, hasTemp := body["temperature"]; hasTemp != tc.wantSampling {
				t.Fatalf("temperature present = %v, want %v", hasTemp, tc.wantSampling)
			}
			if _, hasTopP := body["top_p"]; hasTopP != tc.wantSampling {
				t.Fatalf("top_p present = %v, want %v", hasTopP, tc.wantSampling)
			}
			_, hasStreamOpts := body["stream_options"]
			if hasStreamOpts != tc.wantStreamOpts {
				t.Fatalf("stream_options present = %v, want %v", hasStreamOpts, tc.wantStreamOpts)
			}
		})
	}
}

func TestGenerate_FamilyQuirks_RegistryMaxTokensFieldOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	regPath := filepath.Join(home, "providers.json")
	// The optional per-model maxTokensField key, parsed tolerantly.
	doc := `{"providers":[{"name":"mock-provider","baseURL":"http://127.0.0.1:9/v1",
	        "models":[{"id":"probe-gemini","maxTokensField":"max_completion_tokens"}]}]}`
	if err := os.WriteFile(regPath, []byte(doc), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m, "probe-gemini")
	reg.Path = regPath
	srv := startServer(t, cfg)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, quirkRequest())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	body := m.last().Body
	if _, ok := body["max_completion_tokens"]; !ok {
		t.Fatalf("the registry override must select max_completion_tokens: %v", body)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("max_tokens must be renamed: %v", body)
	}
	// An unreadable or absent file leaves the family default in place.
	reg.Path = filepath.Join(home, "missing.json")
	if got := loadMaxTokensFields(reg); len(got) != 0 {
		t.Fatalf("a missing registry file must yield no overrides: %v", got)
	}
}

func TestGenerate_FamilyQuirks_NClampWithTools(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	reg.Providers[0].Models[0].Capabilities = providers.Capabilities{SupportsTools: true}
	srv := startServer(t, cfg)

	req := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
		"tools": []any{map[string]any{"functionDeclarations": []any{
			map[string]any{"name": "view_file"},
		}}},
		"generationConfig": map[string]any{"candidateCount": 3},
	}
	body, _ := json.Marshal(req)
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if got := m.last().Body["n"]; got != float64(1) {
		t.Fatalf("n = %v, want 1 (clamped with tools attached)", got)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if !strings.Contains(lines[0].Note, "clamped to 1") {
		t.Fatalf("audit note = %q, want the clamp record", lines[0].Note)
	}
}

func TestGenerate_FamilyQuirks_NKeptWithoutTools(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)

	req := map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
		"generationConfig": map[string]any{"candidateCount": 3},
	}
	body, _ := json.Marshal(req)
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if got := m.last().Body["n"]; got != float64(3) {
		t.Fatalf("n = %v, want 3 (no tools attached)", got)
	}
}

// ---------------------------------------------------------------------------
// §6.1 outbound hygiene
// ---------------------------------------------------------------------------

func TestGenerate_OutboundRequestStripsInboundHeaders(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	reg.Providers[0].APIKeyEnv = testKeyEnv
	t.Setenv(testKeyEnv, "upstream-secret-value")
	srv := startServer(t, cfg)

	headers := map[string]string{
		headerAPIKey:                  "install-nonce-value",
		"User-Agent":                  "google-genai-sdk/1.74.0",
		"X-Cloudaicompanion-Trace-Id": "trace-123",
	}
	resp, _ := post(t, streamURL(srv, "probe-gemini")+"&alt=sse", headers, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	call := m.last()
	if len(call.LeakedAgy) != 0 {
		t.Fatalf("inbound agy headers reached the upstream: %v", call.LeakedAgy)
	}
	if !call.HasAuthz || call.AuthzScheme != "Bearer" {
		t.Fatalf("outbound authorization = %q/%v", call.AuthzScheme, call.HasAuthz)
	}
	if call.Path != "/chat/completions" {
		t.Fatalf("outbound path = %q", call.Path)
	}
	if call.RawQuery != "" {
		t.Fatalf("outbound query = %q, want none (no ?key=, no alt=sse)", call.RawQuery)
	}
	// The inbound install nonce must not be the outbound credential.
	if strings.Contains(strings.Join(call.HeaderNames, ","), "X-Agy-Pro-Install") {
		t.Fatalf("install nonce header reached the upstream: %v", call.HeaderNames)
	}
}

func TestGenerate_NoAuthzWhenProviderHasNoKey(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	resp, _ := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if m.last().HasAuthz {
		t.Fatal("a provider without apiKeyEnv must not receive an Authorization header")
	}
}

// ---------------------------------------------------------------------------
// §9.1 error classes
// ---------------------------------------------------------------------------

func TestGenerate_ErrorClasses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantCode   int
		wantStatus string
		wantMsg    string
	}{
		{"provider 400", 400, `{"error":"unsupported parameter: response_format"}`, 400, statusInvalidArgument, "provider mock-provider: rejected request (unsupported parameter: response_format)"},
		{"provider 401", 401, `{"error":"bad key"}`, 401, statusUnauthenticated, "provider mock-provider: authentication failed"},
		{"provider 403", 403, `{"error":"forbidden"}`, 403, statusPermissionDenied, "provider mock-provider: permission denied"},
		{"provider 404", 404, `{"error":"missing"}`, 404, statusNotFound, "provider mock-provider: model probe-gemini not found"},
		{"provider 408", 408, `{"error":"timeout"}`, 504, statusDeadlineExceeded, "provider mock-provider: timed out"},
		{"provider 413", 413, `{"error":"too large"}`, 400, statusInvalidArgument, "request too large for provider mock-provider"},
		{"provider 429", 429, `{"error":"slow down"}`, 429, statusResourceExhausted, "provider mock-provider: rate limited (retry later)"},
		{"provider 409", 409, `{"error":"conflict"}`, 409, statusAborted, "provider mock-provider: request conflict"},
		{"provider 500", 500, `{"error":"boom"}`, 502, statusUnavailable, "provider mock-provider: upstream error (HTTP 500)"},
		{"provider 503", 503, `{"error":"down"}`, 502, statusUnavailable, "provider mock-provider: upstream error (HTTP 503)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			cfg, _ := testUpstreamConfig(t, m)
			srv := startServer(t, cfg)
			m.setScript(func(n int, w http.ResponseWriter) { writeStatus(w, tc.status, tc.body) })

			resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status %d body %s", resp.StatusCode, raw)
			}
			env := decodeError(t, raw)
			if env.Error.Status != tc.wantStatus || env.Error.Message != tc.wantMsg {
				t.Fatalf("envelope = %+v, want %s/%s", env.Error, tc.wantStatus, tc.wantMsg)
			}
			// The upstream body is never echoed into the message.
			if strings.Contains(raw, "bad key") || strings.Contains(raw, "slow down") {
				t.Fatalf("upstream body leaked: %s", raw)
			}
		})
	}
}

func TestGenerate_MalformedUpstreamBody(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>not json</html>"))
	})
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	// A 200 whose body cannot be folded is an upstream failure, not a fake
	// success: the mapped status answers before anything is streamed.
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusUnavailable || env.Error.Message != "provider mock-provider: unparseable response" {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

// TestGenerate_MidStreamDisconnectMatchesGF28 replays the GF-28 provider
// stream (two deltas, then a dropped connection) and pins the §9.3 bytes.
func TestGenerate_MidStreamDisconnectMatchesGF28(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		// The GF-28 provider stream, verbatim, then a dropped connection.
		replayGoldenAbort(t, w, "gf-28-request.sse")
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if want := string(readGolden(t, "gf-28-expected.sse")); raw != want {
		t.Fatalf("mid-stream bytes mismatch:\n got %q\nwant %q", raw, want)
	}
	// No fake success terminator after the error frame.
	if strings.Count(raw, "finishReason") != 0 {
		t.Fatalf("a mid-stream failure must not emit a success terminator: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// G10F-1: a stream that ends without a terminal signal is not a complete turn
// ---------------------------------------------------------------------------

// TestGenerate_TruncatedStreamSurfacesError is the G10 finding-1 regression. The
// upstream answers 200 SSE, sends one content delta and then ends the response
// CLEANLY: no finish_reason chunk and no [DONE] sentinel, and no connection
// error for a read-error check to see. The HTTP body terminated normally, so the
// only signal that the turn was cut short is the absence of a terminal frame.
//
// Before G10F-1 the mesh folded the partial delta, synthesized a terminal
// finishReason STOP and answered 200, so agy printed the truncated text and
// exited 0 with no way to tell it from a complete answer. Now the §9.1
// stream-truncated row answers, and the partial text rides the §9.3 error frame
// exactly as a transport failure does (GF-28's shape).
func TestGenerate_TruncatedStreamSurfacesError(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		truncateMidStream(w, "G10_TRUNCATED_PARTIAL_")
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	frames := sseFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want the partial text + the error frame: %s", len(frames), raw)
	}
	// The text that did arrive is still emitted (§9.3), so agy can render it.
	var first geminiwire.GenerateContentResponse
	if err := json.Unmarshal([]byte(frames[0]), &first); err != nil {
		t.Fatalf("first frame: %v (%s)", err, frames[0])
	}
	if got := first.Candidates[0].Content.Parts[0].Text; got != "G10_TRUNCATED_PARTIAL_" {
		t.Fatalf("partial text = %q", got)
	}
	// Then the error frame, and NO synthesized success terminator anywhere.
	var env errorEnvelope
	if err := json.Unmarshal([]byte(frames[1]), &env); err != nil {
		t.Fatalf("second frame is not a §9.1 envelope: %v (%s)", err, frames[1])
	}
	if env.Error.Code != http.StatusServiceUnavailable || env.Error.Status != statusUnavailable {
		t.Fatalf("envelope = %+v, want 503/UNAVAILABLE", env.Error)
	}
	if want := "provider mock-provider: upstream stream ended before a terminal frame"; env.Error.Message != want {
		t.Fatalf("message = %q, want %q", env.Error.Message, want)
	}
	if strings.Contains(raw, "finishReason") {
		t.Fatalf("a truncated stream must not carry a synthesized terminator: %s", raw)
	}
	if m.count() != 1 {
		t.Fatalf("upstream calls = %d, want 1", m.count())
	}
}

// TestGenerate_TruncatedStreamWithNoContentAnswersTheMappedStatus is the
// zero-byte variant: nothing was committed, so the mapped §9.1 status answers
// instead of a stream. Before G10F-1 this produced a fabricated apology plus a
// synthesized STOP at 200.
func TestGenerate_TruncatedStreamWithNoContentAnswersTheMappedStatus(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		truncateMidStream(w)
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusUnavailable {
		t.Fatalf("envelope = %+v", env.Error)
	}
	if want := "provider mock-provider: upstream stream ended before a terminal frame"; env.Error.Message != want {
		t.Fatalf("message = %q, want %q", env.Error.Message, want)
	}
}

// TestGenerate_TruncatedEmptyTurnOnACommittedStreamCarriesTheApology is the
// G10F2-3 pin. A role-only delta arms the §8.9 pinger, so a stall past the
// keep-alive interval commits the 200; the provider then closes cleanly with no
// terminal frame and the folded turn is EMPTY. Before G10F2-3 that shape emitted
// only the §9.3 error frame, which is §9.2's textless definition on a committed
// 200 and whose client reaction has never been measured. The fixed apology now
// rides before the frame, so a committed 200 always carries renderable text.
func TestGenerate_TruncatedEmptyTurnOnACommittedStreamCarriesTheApology(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	// A short keep-alive interval commits the stream before the upstream ends.
	cfg.StreamKeepAliveInterval = 20 * time.Millisecond
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A role-only delta: no content, so the folded turn is empty. It is
		// still a read, which is what arms the pinger.
		writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Long enough for the pinger to fire and commit the stream, short enough
		// to keep the test fast.
		time.Sleep(120 * time.Millisecond)
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	frames := sseFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want the apology + the error frame: %s", len(frames), raw)
	}
	// The apology, not an error-only stream.
	var first geminiwire.GenerateContentResponse
	if err := json.Unmarshal([]byte(frames[0]), &first); err != nil {
		t.Fatalf("first frame: %v (%s)", err, frames[0])
	}
	if len(first.Candidates) != 1 || first.Candidates[0].Content == nil {
		t.Fatalf("first frame carries no candidate: %s", frames[0])
	}
	text := first.Candidates[0].Content.Parts[0].Text
	if !strings.HasPrefix(text, geminiwire.DefaultApology) {
		t.Fatalf("first frame text = %q, want the §9.2 apology", text)
	}
	if !strings.Contains(text, "provider mock-provider") {
		t.Fatalf("the apology lost the provider name: %q", text)
	}
	// Then the error frame, and still no synthesized success terminator.
	var env errorEnvelope
	if err := json.Unmarshal([]byte(frames[1]), &env); err != nil {
		t.Fatalf("second frame is not a §9.1 envelope: %v (%s)", err, frames[1])
	}
	if env.Error.Code != http.StatusServiceUnavailable || env.Error.Status != statusUnavailable {
		t.Fatalf("envelope = %+v, want 503/UNAVAILABLE", env.Error)
	}
	if strings.Contains(raw, "finishReason") {
		t.Fatalf("no synthesized terminator allowed: %s", raw)
	}
	if m.count() != 1 {
		t.Fatalf("upstream calls = %d, want 1", m.count())
	}
}

// TestGenerate_CleanStreamStillCompletes is the G10F-1 negative control on the
// same route: an upstream that sends content, a finish_reason chunk and [DONE]
// must complete exactly as before. A terminal signal of EITHER kind is enough,
// so both spellings are pinned.
func TestGenerate_CleanStreamStillCompletes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(w http.ResponseWriter)
	}{
		{"finish_reason and [DONE]", func(w http.ResponseWriter) { cleanSSEStream(w, "PROBE_OK") }},
		{"[DONE] only", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSEFrame(w, contentChunk("PROBE_OK"))
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}},
		{"finish_reason only", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSEFrame(w, contentChunk("PROBE_OK"))
			writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
				"usage":   defaultUsage})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockUpstream(t)
			cfg, _ := testUpstreamConfig(t, m)
			srv := startServer(t, cfg)
			m.setScript(func(n int, w http.ResponseWriter) { tc.write(w) })

			resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s", resp.StatusCode, raw)
			}
			frames := decodeFrames(t, raw)
			if len(frames) != 2 {
				t.Fatalf("frames = %d, want text + final: %s", len(frames), raw)
			}
			if got := frames[0].Candidates[0].Content.Parts[0].Text; got != "PROBE_OK" {
				t.Fatalf("text = %q", got)
			}
			if frames[1].Candidates[0].FinishReason != "STOP" {
				t.Fatalf("finishReason = %q, want the real terminal STOP", frames[1].Candidates[0].FinishReason)
			}
			if m.count() != 1 {
				t.Fatalf("upstream calls = %d, want 1", m.count())
			}
		})
	}
}

// TestGenerate_TruncatedStreamOnTheEnvelopeSurface pins G10F-1 on the P1
// /v1internal wire: the error frame rides the Cloud Code envelope, and the audit
// note names the failure class.
func TestGenerate_TruncatedStreamOnTheEnvelopeSurface(t *testing.T) {
	m := newMockUpstream(t)
	cfg := passiveConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		truncateMidStream(w, "G10_TRUNCATED_PARTIAL_")
	})

	resp, raw := post(t, internalStreamURL(srv, "probe-gemini"),
		map[string]string{"Authorization": "Bearer " + keyringBearer}, envelopeBody("probe-gemini"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	frames := sseFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want the partial text + the error frame: %s", len(frames), raw)
	}
	// The text that did arrive rides the Cloud Code envelope (§9.3).
	var wrapped geminiwire.V1InternalResponse
	if err := json.Unmarshal([]byte(frames[0]), &wrapped); err != nil {
		t.Fatalf("first frame is not an envelope: %v (%s)", err, frames[0])
	}
	var inner geminiwire.GenerateContentResponse
	if err := json.Unmarshal(wrapped.Response, &inner); err != nil {
		t.Fatalf("envelope payload: %v (%s)", err, wrapped.Response)
	}
	if got := inner.Candidates[0].Content.Parts[0].Text; got != "G10_TRUNCATED_PARTIAL_" {
		t.Fatalf("partial text = %q", got)
	}
	// The §9.3 error frame is emitted bare, exactly as GF-28 pins it.
	var env errorEnvelope
	if err := json.Unmarshal([]byte(frames[1]), &env); err != nil {
		t.Fatalf("second frame is not a §9.1 envelope: %v (%s)", err, frames[1])
	}
	if env.Error.Code != http.StatusServiceUnavailable || env.Error.Status != statusUnavailable {
		t.Fatalf("envelope = %+v, want 503/UNAVAILABLE", env.Error)
	}
	if want := "provider mock-provider: upstream stream ended before a terminal frame"; env.Error.Message != want {
		t.Fatalf("message = %q, want %q", env.Error.Message, want)
	}
	if strings.Contains(raw, "finishReason") {
		t.Fatalf("no synthesized terminator allowed on the envelope surface: %s", raw)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if !strings.Contains(lines[0].Note, "upstream failed: UNAVAILABLE") {
		t.Fatalf("audit note = %q", lines[0].Note)
	}
}

func TestGenerate_TransportFailure(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	// Point the registry at a closed port: dial fails before any response.
	cfg.Registry.Providers[0].BaseURL = "http://127.0.0.1:9"
	srv := startServer(t, cfg)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusUnavailable || env.Error.Message != "provider mock-provider: transport failure" {
		t.Fatalf("envelope = %+v", env.Error)
	}
	if m.count() != 0 {
		t.Fatalf("no upstream request should have been served: %d", m.count())
	}
}

// ---------------------------------------------------------------------------
// §9.2 never textless
// ---------------------------------------------------------------------------

func TestGenerate_EmptyTurnYieldsApology(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		// A 200 with a role chunk and a finish, and no content at all.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readGolden(t, "gf-27-request.sse"))
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	frames := decodeFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want apology + final: %s", len(frames), raw)
	}
	text := frames[0].Candidates[0].Content.Parts[0].Text
	// The §9.2 apology with the provider name and the cause, synthesized by the
	// codec's choke point.
	wantPrefix := strings.TrimSuffix(geminiwire.DefaultApology, ".") + ": provider mock-provider: "
	if !strings.HasPrefix(text, wantPrefix) {
		t.Fatalf("apology = %q, want the prefix %q", text, wantPrefix)
	}
	if frames[1].Candidates[0].FinishReason != "STOP" {
		t.Fatalf("final finishReason = %q", frames[1].Candidates[0].FinishReason)
	}
	// The apology is rune-safe: no U+FFFD replacement characters.
	if strings.ContainsRune(raw, '\uFFFD') {
		t.Fatalf("apology contains replacement characters: %s", raw)
	}
}

func TestGenerate_RefusalOnlyTurnIsNotClobbered(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	// A refusal and no content: the refusal is the turn's only text, so the
	// §9.2 apology must not replace it.
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "refusal": "정책상 거부됩니다"},
			"finish_reason": "stop",
		}}}
		payload, _ := json.Marshal(body)
		_, _ = w.Write(payload)
	})

	resp, raw := post(t, generateURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	// A refusal is content: the apology must not replace it.
	if strings.Contains(raw, geminiwire.DefaultApology) {
		t.Fatalf("a refusal-only turn must keep its own text: %s", raw)
	}
	if !strings.Contains(raw, "정책상 거부됩니다") {
		t.Fatalf("refusal text missing: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// §7.13 repair retry
// ---------------------------------------------------------------------------

func TestGenerate_RepairRetry(t *testing.T) {
	t.Run("400 naming response_format retries once without it", func(t *testing.T) {
		m := newMockUpstream(t)
		cfg, _ := testUpstreamConfig(t, m)
		srv := startServer(t, cfg)
		req := map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "json 줘"}}}},
			"generationConfig": map[string]any{
				"responseMimeType":   "application/json",
				"responseJsonSchema": map[string]any{"type": "object"},
			},
		}
		body, _ := json.Marshal(req)

		var firstHadFormat bool
		m.setScript(func(n int, w http.ResponseWriter) {
			if n == 0 {
				firstHadFormat = m.call(0).Body["response_format"] != nil
				writeStatus(w, 400, `{"error":"unsupported parameter: response_format"}`)
				return
			}
			writeSSECompletion(w, []string{"재시도 성공"}, defaultUsage)
		})

		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d body %s", resp.StatusCode, raw)
		}
		if !firstHadFormat {
			t.Fatal("the first attempt should have carried response_format")
		}
		if m.count() != 2 {
			t.Fatalf("upstream calls = %d, want exactly 2 (one retry)", m.count())
		}
		if _, exists := m.call(1).Body["response_format"]; exists {
			t.Fatalf("the retry must drop response_format: %v", m.call(1).Body)
		}
		if !strings.Contains(raw, "재시도 성공") {
			t.Fatalf("retry response missing: %s", raw)
		}
		lines := readAuditLines(t, cfg.AuditPath, 1)
		if !strings.Contains(lines[0].Note, "repair retry") {
			t.Fatalf("audit note = %q, want the repair-retry record", lines[0].Note)
		}
	})

	t.Run("400 naming nothing is not retried", func(t *testing.T) {
		m := newMockUpstream(t)
		cfg, _ := testUpstreamConfig(t, m)
		srv := startServer(t, cfg)
		m.setScript(func(n int, w http.ResponseWriter) {
			writeStatus(w, 400, `{"error":"context length exceeded"}`)
		})
		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d body %s", resp.StatusCode, raw)
		}
		if m.count() != 1 {
			t.Fatalf("upstream calls = %d, want 1 (no retry)", m.count())
		}
	})

	t.Run("a failing retry is not retried again", func(t *testing.T) {
		m := newMockUpstream(t)
		cfg, _ := testUpstreamConfig(t, m)
		srv := startServer(t, cfg)
		m.setScript(func(n int, w http.ResponseWriter) {
			writeStatus(w, 400, `{"error":"unsupported parameter: reasoning_effort"}`)
		})
		req := map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
			"generationConfig": map[string]any{
				"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": 8192},
			},
		}
		body, _ := json.Marshal(req)
		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d body %s", resp.StatusCode, raw)
		}
		if m.count() != 2 {
			t.Fatalf("upstream calls = %d, want exactly 2 (never a third)", m.count())
		}
		if _, exists := m.call(1).Body["reasoning_effort"]; exists {
			t.Fatalf("the retry must drop reasoning_effort: %v", m.call(1).Body)
		}
	})
}

// ---------------------------------------------------------------------------
// §6.4 watchdogs
// ---------------------------------------------------------------------------

func TestGenerate_FirstByteWatchdog(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.UpstreamFirstByteTimeout = 120 * time.Millisecond
	cfg.UpstreamIdleTimeout = 30 * time.Second
	srv := startServer(t, cfg)
	release := make(chan struct{})
	m.setScript(func(n int, w http.ResponseWriter) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		writeSSECompletion(w, defaultDeltas, defaultUsage)
	})
	defer close(release)

	start := time.Now()
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the watchdog took %v to fire", elapsed)
	}
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusDeadlineExceeded || env.Error.Message != "provider mock-provider: timed out" {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func TestGenerate_IdleWatchdog(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.UpstreamFirstByteTimeout = 5 * time.Second
	cfg.UpstreamIdleTimeout = 150 * time.Millisecond
	srv := startServer(t, cfg)
	release := make(chan struct{})
	m.setScript(func(n int, w http.ResponseWriter) {
		// Headers and nothing else: the first-byte watchdog is satisfied, the
		// idle one must fire.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	})
	defer close(release)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusDeadlineExceeded {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

func TestGenerate_IdleWatchdogAfterFirstByteEmitsErrorFrame(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.UpstreamIdleTimeout = 150 * time.Millisecond
	srv := startServer(t, cfg)
	release := make(chan struct{})
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "첫 번째"}, "finish_reason": nil}},
		})
		_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	})
	defer close(release)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	// The stream was committed by the first byte, so the watchdog rides the
	// §9.3 error frame instead of a mapped status.
	if !strings.Contains(raw, `"status":"DEADLINE_EXCEEDED"`) {
		t.Fatalf("missing the mid-stream timeout frame: %s", raw)
	}
	if strings.Contains(raw, "finishReason") {
		t.Fatalf("no fake success terminator allowed: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// §8.9 keep-alive
// ---------------------------------------------------------------------------

func TestGenerate_KeepAlivePings(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.StreamKeepAliveInterval = 60 * time.Millisecond
	cfg.UpstreamIdleTimeout = 5 * time.Second
	srv := startServer(t, cfg)
	release := make(chan struct{})
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEFrame(w, contentChunk("시작"))
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		writeSSEFrame(w, contentChunk("끝"))
		writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   defaultUsage})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	defer close(release)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, ": ping\n\n") {
		t.Fatalf("no keep-alive comment during the upstream silence: %q", raw)
	}
	// The pings are comments: the parsed frames stay a clean chunk sequence.
	// The adapter aggregates the turn, so the two deltas arrive as one text
	// part followed by the final chunk.
	frames := decodeFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want text + final: %s", len(frames), raw)
	}
	if got := frames[0].Candidates[0].Content.Parts[0].Text; got != "시작끝" {
		t.Fatalf("aggregated text = %q", got)
	}
	if frames[1].Candidates[0].FinishReason != "STOP" {
		t.Fatalf("final finishReason = %q", frames[1].Candidates[0].FinishReason)
	}
}

// ---------------------------------------------------------------------------
// housekeeping, envelope branch, guards
// ---------------------------------------------------------------------------

func TestGenerate_HousekeepingCostsZeroUpstreamCalls(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	// A hostile default: any upstream call would fail the test.
	m.setScript(func(n int, w http.ResponseWriter) {
		t.Error("housekeeping must never reach the provider")
		writeStatus(w, 500, `{"error":"must not happen"}`)
	})

	request := readGolden(t, "gf-29-request.json")
	resp, raw := post(t, streamURL(srv, "gemini-3.1-flash-lite-preview"), nil, request)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if want := string(readGolden(t, "gf-29-expected.sse")); raw != want {
		t.Fatalf("housekeeping bytes mismatch:\n got %q\nwant %q", raw, want)
	}
	if m.count() != 0 {
		t.Fatalf("upstream calls = %d, want 0", m.count())
	}
}

// TestGenerate_EnvelopeBranchRoundTrip drives the /v1internal branch end to end
// and pins the re-wrapped chunk shape (Appendix A, GF-31).
func TestGenerate_EnvelopeBranchRoundTrip(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		writeSSECompletion(w, []string{"메시 응답"}, defaultUsage)
	})

	envelope := map[string]any{
		"project":     "mock-project-1234",
		"model":       "probe-gemini",
		"userAgent":   "antigravity",
		"requestType": "streamGenerateContent",
		"requestId":   "checkpoint/abc",
		"request": map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "안녕"}}}},
		},
	}
	body, _ := json.Marshal(envelope)
	resp, raw := post(t, srv.URL()+"/v1internal:streamGenerateContent?alt=sse", nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	// Every chunk rides in {"response": <chunk>}.
	for _, frame := range sseFrames(t, raw) {
		var wrapped struct {
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(frame), &wrapped); err != nil {
			t.Fatalf("frame is not an envelope: %v (%s)", err, frame)
		}
		if len(wrapped.Response) == 0 {
			t.Fatalf("chunk is not wrapped: %s", frame)
		}
	}
	frames := decodeEnvelopeFrames(t, raw)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want delta + final: %s", len(frames), raw)
	}
	if got := frames[0].Candidates[0].Content.Parts[0].Text; got != "메시 응답" {
		t.Fatalf("text = %q", got)
	}
	if frames[1].Candidates[0].FinishReason != "STOP" {
		t.Fatalf("final finishReason = %q", frames[1].Candidates[0].FinishReason)
	}
	// The outbound model is the envelope's .model.
	if m.last().Body["model"] != "probe-gemini" {
		t.Fatalf("outbound model = %v", m.last().Body["model"])
	}
}

func TestGenerate_EnvelopeNonStreamReWraps(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		writeJSONCompletion(w, "한 번에", defaultUsage)
	})
	envelope := map[string]any{
		"project": "p", "model": "probe-gemini",
		"request": map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}},
	}
	body, _ := json.Marshal(envelope)
	resp, raw := post(t, srv.URL()+"/v1internal:generateContent", nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	var wrapped geminiwire.V1InternalResponse
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		t.Fatalf("body: %v (%s)", err, raw)
	}
	var inner geminiwire.GenerateContentResponse
	if err := json.Unmarshal(wrapped.Response, &inner); err != nil {
		t.Fatalf("inner: %v (%s)", err, wrapped.Response)
	}
	if len(inner.Candidates) != 1 || inner.Candidates[0].FinishReason != "STOP" {
		t.Fatalf("inner candidates = %+v", inner.Candidates)
	}
}

func TestGenerate_RateLimitAndAuthStillEnforced(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuthKey = "install-nonce"
	srv := startServer(t, cfg)

	// Strict mode guards the generation routes.
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	resp, _ = post(t, streamURL(srv, "probe-gemini"), map[string]string{headerAPIKey: "install-nonce"}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status %d", resp.StatusCode)
	}
	// Release the strict instance before the permissive one: one singleton per
	// install nonce.
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Permissive mode still rate limits the generation routes.
	cfg2, _ := testUpstreamConfig(t, m)
	srv2 := startServer(t, cfg2)
	limited := false
	for i := 0; i < rateLimitPerMinute+10 && !limited; i++ {
		resp, _ = post(t, streamURL(srv2, "probe-gemini"), nil, chatRequestBody(nil))
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("the generation route escaped the per-install rate limit")
	}
}

func TestGenerate_AuditCarriesNoKeyValues(t *testing.T) {
	const secret = "SEKRET-UPSTREAM-KEY-1234567890"
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.Redact = providers.NewRedactor(secret)
	cfg.AuthKey = "install-nonce"
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		writeStatus(w, http.StatusTooManyRequests, `{"error":"key `+secret+` rejected"}`)
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), map[string]string{headerAPIKey: "install-nonce"}, chatRequestBody(nil))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("the planted key leaked into the error body: %s", raw)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if strings.Contains(lines[0].Note, secret) {
		t.Fatalf("the planted key leaked into the audit note: %q", lines[0].Note)
	}
	auditBytes, err := readFileForTest(cfg.AuditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(string(auditBytes), secret) {
		t.Fatalf("the planted key leaked into the audit file: %s", auditBytes)
	}
}

func TestGenerate_ConcurrentBurst(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)

	const workers = 12
	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
			if resp.StatusCode != http.StatusOK {
				done <- errString(fmt.Sprintf("status %d body %s", resp.StatusCode, raw))
				return
			}
			if !strings.Contains(raw, "finishReason") {
				done <- errString(fmt.Sprintf("stream never terminated: %s", raw))
				return
			}
			done <- nil
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if m.count() != workers {
		t.Fatalf("upstream calls = %d, want %d", m.count(), workers)
	}
	lines := readAuditLines(t, cfg.AuditPath, workers)
	if len(lines) != workers {
		t.Fatalf("audit lines = %d, want %d", len(lines), workers)
	}
}

func TestGenerate_ExplicitUnimplementedOverride(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.Generate = UnimplementedGenerate{}
	srv := startServer(t, cfg)
	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusUnimplemented {
		t.Fatalf("envelope = %+v", env.Error)
	}
	if m.count() != 0 {
		t.Fatalf("the placeholder must not call the upstream: %d", m.count())
	}
}

// ---------------------------------------------------------------------------
// M4-item-4 parity: the OUTBOUND request-body cap
// ---------------------------------------------------------------------------

// newTestStreamSession builds the minimal session callUpstream needs for the
// non-stream branch (no emitter, the production watchdog defaults).
func newTestStreamSession(t *testing.T, h *generateHandler, body string) *streamSession {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/probe-gemini:generateContent", strings.NewReader(body))
	return &streamSession{
		h:         h,
		r:         req,
		firstByte: h.srv.firstByteTimeout(),
		idle:      h.srv.idleTimeout(),
		keepAlive: h.srv.keepAliveInterval(),
	}
}

// The mesh caps the inbound body at 32 MB and the upstream response at 32 MB, but
// the re-marshaled OUTBOUND body was sent uncapped, and json.Marshal HTML-escapes
// <, >, & so a body at the inbound cap can inflate several-fold before it reaches
// the provider. The bridge closed exactly this shape in M4 item 4; this pins the
// mesh-side mirror. An over-cap body fails loudly and the provider is NEVER
// contacted (never silently truncated).
func TestCallUpstream_OutboundBodyOverCapIsRefusedBeforeContact(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	h := &generateHandler{srv: srv}
	target := providers.Target{Provider: reg.Providers[0], Model: reg.Providers[0].Models[0]}

	over := make([]byte, openaiup.MaxBodyBytes+1)
	for i := range over {
		over[i] = 'a'
	}
	sess := newTestStreamSession(t, h, string(over))

	turn, fail := h.callUpstream(context.Background(), sess, target, over)
	if fail == nil {
		t.Fatal("an over-cap outbound body must fail loudly")
	}
	if fail.kind != failureOutboundTooLarge {
		t.Fatalf("failure kind = %v; want failureOutboundTooLarge", fail.kind)
	}
	if turn != nil {
		t.Errorf("turn = %+v; want nil (nothing was sent, so nothing folded)", turn)
	}
	if got := m.count(); got != 0 {
		t.Errorf("the mock upstream was contacted %d times; want 0 (the provider is never dialed over cap)", got)
	}
	if !strings.Contains(fail.err.Error(), "byte cap") {
		t.Errorf("the failure must name the cap breach: %v", fail.err)
	}
	// The mapped §9.1 triple: the existing 413 row, provider-specific.
	code, status, message := fail.envelope("mock-provider", "probe-gemini")
	if code != http.StatusBadRequest || status != statusInvalidArgument {
		t.Errorf("envelope = (%d, %s); want (400, %s)", code, status, statusInvalidArgument)
	}
	if !strings.Contains(message, "mock-provider") || !strings.Contains(message, "too large") {
		t.Errorf("envelope message = %q; want the provider-named too-large row", message)
	}
}

// The cap must not reject a legitimate request: a normal body still reaches the
// provider and the gate is invisible on that path.
func TestCallUpstream_OutboundBodyUnderCapStillRuns(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	h := &generateHandler{srv: srv}
	target := providers.Target{Provider: reg.Providers[0], Model: reg.Providers[0].Models[0]}
	sess := newTestStreamSession(t, h, "{}")

	body := []byte(`{"model":"probe-gemini","messages":[{"role":"user","content":"hi"}]}`)
	turn, fail := h.callUpstream(context.Background(), sess, target, body)
	if fail != nil {
		t.Fatalf("a normal body must not trip the outbound cap: %v", fail.err)
	}
	if turn == nil {
		t.Fatal("a normal body must still produce a turn")
	}
	if got := m.count(); got != 1 {
		t.Errorf("mock upstream calls = %d; want 1", got)
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): both "empty upstream response" causes in
// generate.go append the registry provider NAME to the §9.2 apology. The name
// is a registry slot, so it renders through providers.MaskSlot (the
// registry-slot authority), which closes the underscore-head credential class
// that the operand masker is the identity on. Benign names keep their bytes.
// ---------------------------------------------------------------------------

func TestGenerate_EmptyUpstreamCauseMasksRegistryProviderName(t *testing.T) {
	const (
		plant     = "sk_live_0123456789abcdef"
		plantMask = "sk_li****"
	)

	// apologyText returns the §9.2 apology sentence of the stream, so the pin
	// fails on the exact sentence rather than on a substring of the raw body.
	apologyText := func(t *testing.T, raw string) string {
		t.Helper()
		found := ""
		for _, f := range sseFrames(t, raw) {
			var resp geminiwire.GenerateContentResponse
			if err := json.Unmarshal([]byte(f), &resp); err != nil {
				continue
			}
			for _, c := range resp.Candidates {
				if c.Content == nil {
					continue
				}
				for _, part := range c.Content.Parts {
					if strings.Contains(part.Text, "could not finish this turn") {
						found = part.Text
					}
				}
			}
		}
		if found == "" {
			t.Fatalf("the stream carries no §9.2 apology: %s", raw)
		}
		return found
	}

	// assertCause pins the apology sentence AND the whole response: the §9.1
	// error frame interpolates the same name through errorwriter.go's row
	// templates, and generate.go masks both operands at that call site, so the
	// raw value must not survive anywhere in the stream.
	assertCause := func(t *testing.T, raw string) {
		t.Helper()
		text := apologyText(t, raw)
		if !strings.Contains(text, "provider "+plantMask+": empty upstream response") {
			t.Errorf("the apology cause must render the name as %q: %s", plantMask, text)
		}
		if strings.Contains(raw, plant) {
			t.Errorf("the response leaks the registry provider name raw: %s", raw)
		}
	}

	// Sink 1: the clean-empty success path (a 200 with no renderable turn and
	// no upstream failure).
	t.Run("clean empty turn", func(t *testing.T) {
		m := newMockUpstream(t)
		cfg, reg := testUpstreamConfig(t, m)
		reg.Providers[0].Name = plant
		resetSingletonForTest(t)
		srv := startServer(t, cfg)
		m.setScript(func(n int, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		})

		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d body %s", resp.StatusCode, raw)
		}
		assertCause(t, raw)
	})

	// Sink 2: the keep-alive-committed branch, where the apology precedes the
	// error frame.
	t.Run("committed stream", func(t *testing.T) {
		m := newMockUpstream(t)
		cfg, reg := testUpstreamConfig(t, m)
		reg.Providers[0].Name = plant
		cfg.StreamKeepAliveInterval = 20 * time.Millisecond
		resetSingletonForTest(t)
		srv := startServer(t, cfg)
		m.setScript(func(n int, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(120 * time.Millisecond)
		})

		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d body %s", resp.StatusCode, raw)
		}
		assertCause(t, raw)
	})

	// Benign corpus: the cause is byte-identical to the pre-fix rendering.
	for _, name := range []string{"nw", "openai", "gemini", "deepseek", "charm", "acme-corp"} {
		m := newMockUpstream(t)
		cfg, reg := testUpstreamConfig(t, m)
		reg.Providers[0].Name = name
		resetSingletonForTest(t)
		srv := startServer(t, cfg)
		m.setScript(func(n int, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		})

		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", name, resp.StatusCode, raw)
		}
		if !strings.Contains(raw, "provider "+name+": empty upstream response") {
			t.Errorf("%s: the cause lost its exact bytes: %s", name, raw)
		}
	}
}
