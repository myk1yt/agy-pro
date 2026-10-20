package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"agypro/internal/agenttools"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// toolsTestRegistry builds a single-provider registry with one tools-capable
// model. The API key value doubles as a redaction canary in the masking tests.
func toolsTestRegistry(t *testing.T, upstreamURL string) *providers.Registry {
	t.Helper()
	t.Setenv("TEST_BRIDGE_TOOLS_KEY", "secret-upstream-key-abcdef")
	return &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "tool-prov",
				BaseURL:   upstreamURL,
				APIKeyEnv: "TEST_BRIDGE_TOOLS_KEY",
				Models: []providers.Model{
					{ID: "tool-model", Capabilities: providers.Capabilities{SupportsTools: true}},
				},
			},
		},
	}
}

type turnFunc func(w http.ResponseWriter, body map[string]any)

// scriptedUpstream serves canned per-turn responses and records every request
// body. When fewer scripts than calls exist, the last script repeats.
type scriptedUpstream struct {
	mu    sync.Mutex
	calls []map[string]any
	turns []turnFunc
}

func (u *scriptedUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(bodyBytes, &body)

		u.mu.Lock()
		u.calls = append(u.calls, body)
		idx := len(u.calls) - 1
		var turn turnFunc
		if idx < len(u.turns) {
			turn = u.turns[idx]
		} else if len(u.turns) > 0 {
			turn = u.turns[len(u.turns)-1]
		}
		u.mu.Unlock()

		if turn == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		turn(w, body)
	})
}

func (u *scriptedUpstream) call(i int) map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	if i < len(u.calls) {
		return u.calls[i]
	}
	return nil
}

func (u *scriptedUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func sseTurn(events ...string) turnFunc {
	return func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}
}

// sseTurnUnterminated writes the given SSE events and then closes the stream
// WITHOUT a terminal signal: no finish_reason chunk and no [DONE] sentinel. It
// is the complete-but-unterminated shape the G10F-1 gate in openaiup.Call refuses
// (ErrStreamTruncated), used to pin how the bridge tool loop answers it.
func sseTurnUnterminated(events ...string) turnFunc {
	return func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

// G10F-1 on the bridge path: the truncation gate lives in the shared
// openaiup.Call, so the bridge tool loop now also receives ErrStreamTruncated for
// a stream that carried the whole answer and then closed with neither a
// finish_reason nor any [DONE] spelling. The loop must NOT present that partial
// text as a complete answer (a silent fake success): it discards the turn, records
// the failure, and answers the fixed apology. Before the gate this shape came back
// as a normal answer, and no bridge test pinned the behavior either way.
func TestToolLoop_UnterminatedUpstreamStreamAnswersApology(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	// A full-looking answer delta, then a clean close with no terminal frame.
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "half an answer"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	up := &scriptedUpstream{turns: []turnFunc{sseTurnUnterminated(string(payload))}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 with the synthesized fallback", status)
	}
	content := sseContents(t, raw)
	if content != toolLoopApology {
		t.Errorf("content = %q; want the fixed apology (the truncated turn must not be presented as complete)", content)
	}
	if strings.Contains(content, "half an answer") {
		t.Errorf("the partial text of an unterminated stream leaked into the answer: %q", content)
	}
	// The loop breaks on the failure: exactly one upstream call, no retry.
	if got := up.count(); got != 1 {
		t.Errorf("upstream calls = %d; want 1 (the loop must not retry a truncated stream)", got)
	}

	// The failure is diagnosable (F-03): the audit log names the truncated turn.
	logDir := filepath.Join(home, "logs")
	entries, readErr := os.ReadDir(logDir)
	if readErr != nil {
		t.Fatalf("audit log dir missing (F-03 requires an upstream-error record): %v", readErr)
	}
	var logText strings.Builder
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bridge-tools-") && strings.HasSuffix(e.Name(), ".jsonl") {
			data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			logText.Write(data)
		}
	}
	text := logText.String()
	if !strings.Contains(text, "upstream_turn") {
		t.Errorf("audit log missing the upstream_turn record: %s", text)
	}
	if !strings.Contains(text, "terminal frame") {
		t.Errorf("audit log must name the truncation cause: %s", text)
	}
}

func jsonTurn(payload string) turnFunc {
	return func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, payload)
	}
}

func startToolsBridge(t *testing.T, reg *providers.Registry, cfg BridgeToolsConfig) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewHandler(reg, testSessionKey, cfg))
	t.Cleanup(server.Close)
	return server
}

func postChat(t *testing.T, serverURL, body string) (int, http.Header, []byte) {
	t.Helper()
	resp := authDo(t, http.MethodPost, serverURL+"/v1/chat/completions", "application/json", body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read chat response: %v", err)
	}
	return resp.StatusCode, resp.Header, raw
}

func parseSSEData(t *testing.T, raw []byte) []string {
	t.Helper()
	var events []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			events = append(events, strings.TrimPrefix(line, "data: "))
		}
	}
	return events
}

// sseContents joins every delta.content of a synthesized SSE stream.
func sseContents(t *testing.T, raw []byte) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range parseSSEData(t, raw) {
		if ev == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(ev), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		delta, _ := ch["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok {
			b.WriteString(c)
		}
	}
	return b.String()
}

// sseLastMeta returns finish_reason and usage of the last data event.
func sseLastMeta(t *testing.T, raw []byte) (string, map[string]any) {
	t.Helper()
	events := parseSSEData(t, raw)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(events[i]), &chunk); err != nil {
			continue
		}
		usage, _ := chunk["usage"].(map[string]any)
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			return "", usage
		}
		ch, _ := choices[0].(map[string]any)
		fr, _ := ch["finish_reason"].(string)
		return fr, usage
	}
	return "", nil
}

func sseHasReasoning(t *testing.T, raw []byte, want string) bool {
	t.Helper()
	for _, ev := range parseSSEData(t, raw) {
		if ev == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(ev), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		delta, _ := ch["delta"].(map[string]any)
		if r, ok := delta["reasoning_content"].(string); ok && strings.Contains(r, want) {
			return true
		}
	}
	return false
}

func msgAt(t *testing.T, body map[string]any, i int) map[string]any {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	if i < 0 || i >= len(msgs) {
		t.Fatalf("message index %d out of range (have %d): %+v", i, len(msgs), body["messages"])
	}
	m, ok := msgs[i].(map[string]any)
	if !ok {
		t.Fatalf("message %d is not an object: %T", i, msgs[i])
	}
	return m
}

// contentOf returns the first role:"tool" message content of a captured request.
func contentOf(call map[string]any) string {
	for _, rawMsg := range call["messages"].([]any) {
		m, ok := rawMsg.(map[string]any)
		if ok && m["role"] == "tool" {
			s, _ := m["content"].(string)
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// R1-2: index-aware tool_calls delta merge (arguments split across chunks)
// ---------------------------------------------------------------------------

func TestToolLoop_ChunkSplitToolCallArguments(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("FILEBODY-42"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(
			`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking-here"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"Checking."}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hel"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"lo.txt\"}"}}]}}]}`,
		),
		sseTurn(
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"Final: 42"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"total_tokens":11}}`,
		),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read", "search"}})

	reqBody := `{"model":"tool-model","stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":1234,"messages":[{"role":"user","content":"read hello.txt"}]}`
	status, header, raw := postChat(t, server.URL, reqBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body %s)", status, raw)
	}
	if !strings.Contains(header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type = %q; want text/event-stream", header.Get("Content-Type"))
	}
	if got := sseContents(t, raw); got != "Final: 42" {
		t.Errorf("final content = %q; want %q", got, "Final: 42")
	}
	if !sseHasReasoning(t, raw, "thinking-here") {
		t.Error("synthesized stream must preserve delta.reasoning_content")
	}
	fr, usage := sseLastMeta(t, raw)
	if fr != "stop" {
		t.Errorf("finish_reason = %q; want stop", fr)
	}
	if usage == nil {
		t.Error("usage must ride on the last chunk when upstream provided it")
	}
	events := parseSSEData(t, raw)
	if events[len(events)-1] != "[DONE]" {
		t.Errorf("stream must end with [DONE], got %q", events[len(events)-1])
	}

	// Upstream turn 1: original fields preserved, tools attached, system
	// instruction inserted above the user message (DDC-5 branch 2).
	first := up.call(0)
	if first == nil {
		t.Fatal("upstream received no request")
	}
	if _, hasTools := first["tools"]; !hasTools {
		t.Error("loop must attach tools to the upstream request")
	}
	if first["stream"] != true {
		t.Errorf("stream field = %v; want true preserved", first["stream"])
	}
	if first["max_completion_tokens"] != float64(1234) {
		t.Errorf("max_completion_tokens = %v; want 1234 preserved", first["max_completion_tokens"])
	}
	sysMsg := msgAt(t, first, 0)
	if sysMsg["role"] != "system" {
		t.Errorf("messages[0].role = %v; want system insertion", sysMsg["role"])
	}
	if content, _ := sysMsg["content"].(string); !strings.Contains(content, "<tool_instructions>") {
		t.Errorf("inserted system message missing tool_instructions: %q", content)
	}
	if m := msgAt(t, first, 1); m["role"] != "user" {
		t.Errorf("messages[1].role = %v; want user", m["role"])
	}

	// Upstream turn 2: the split arguments merged into one JSON object and the
	// tool result re-fed as role:"tool".
	second := up.call(1)
	if second == nil {
		t.Fatal("upstream received no second request")
	}
	var sawAssistant, sawTool bool
	msgs, _ := second["messages"].([]any)
	for _, rawMsg := range msgs {
		m := rawMsg.(map[string]any)
		switch m["role"] {
		case "assistant":
			calls, _ := m["tool_calls"].([]any)
			if len(calls) != 1 {
				continue
			}
			call := calls[0].(map[string]any)
			fn := call["function"].(map[string]any)
			if fn["arguments"] != `{"path":"hello.txt"}` {
				t.Errorf("merged arguments = %q; want %q", fn["arguments"], `{"path":"hello.txt"}`)
			}
			sawAssistant = true
		case "tool":
			content, _ := m["content"].(string)
			if !strings.Contains(content, "FILEBODY-42") {
				t.Errorf("tool message content = %q; want file body", content)
			}
			if m["tool_call_id"] != "call_1" {
				t.Errorf("tool_call_id = %v; want call_1", m["tool_call_id"])
			}
			sawTool = true
		}
	}
	if !sawAssistant || !sawTool {
		t.Errorf("re-injected messages incomplete (assistant=%v tool=%v)", sawAssistant, sawTool)
	}
}

// ---------------------------------------------------------------------------
// R1-1: stream:false / absent stream => single chat.completion JSON
// ---------------------------------------------------------------------------

func TestToolLoop_StreamFalseSingleJSON(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		jsonTurn(`{"id":"u1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_9","type":"function","function":{"name":"list_dir","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
		jsonTurn(`{"id":"u2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"listed"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	for _, body := range []string{
		`{"model":"tool-model","stream":false,"messages":[{"role":"user","content":"list"}]}`,
		`{"model":"tool-model","messages":[{"role":"user","content":"list"}]}`,
	} {
		status, header, raw := postChat(t, server.URL, body)
		if status != http.StatusOK {
			t.Fatalf("status = %d; want 200 (body %s)", status, raw)
		}
		if !strings.Contains(header.Get("Content-Type"), "application/json") {
			t.Fatalf("Content-Type = %q; want application/json", header.Get("Content-Type"))
		}
		if strings.Contains(string(raw), "data: ") {
			t.Errorf("non-stream request must not get SSE synthesis: %s", raw)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if parsed["object"] != "chat.completion" {
			t.Errorf("object = %v; want chat.completion", parsed["object"])
		}
		choices, _ := parsed["choices"].([]any)
		if len(choices) != 1 {
			t.Fatalf("choices = %v", parsed["choices"])
		}
		ch := choices[0].(map[string]any)
		if ch["finish_reason"] != "stop" {
			t.Errorf("finish_reason = %v; want stop", ch["finish_reason"])
		}
		msg := ch["message"].(map[string]any)
		if msg["content"] != "listed" {
			t.Errorf("content = %v; want listed", msg["content"])
		}
		if parsed["usage"] == nil {
			t.Error("usage must be present on the JSON body when upstream sent it")
		}
	}
}

// ---------------------------------------------------------------------------
// R1-7: loop-off is a byte-identical passthrough for a tools-capable model
// ---------------------------------------------------------------------------

const verbatimBody = "{\n  \"object\": \"weird.custom\",\n  \"choices\": [ { \"message\": { \"content\": \"verbatim bytes\" } } ]\n}"

func TestLoopOff_VerbatimPassthrough(t *testing.T) {
	cases := []struct {
		name string
		cfg  BridgeToolsConfig
	}{
		{"disabled zero value", BridgeToolsConfig{}},
		{"tokens none", BridgeToolsConfig{Enabled: true, Tools: []string{"none"}}},
		{"empty allow-list", BridgeToolsConfig{Enabled: true, Tools: nil}},
		{"disabled with tokens", BridgeToolsConfig{Enabled: false, Tools: []string{"read", "search"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := &scriptedUpstream{turns: []turnFunc{jsonTurn(verbatimBody)}}
			upstream := httptest.NewServer(up.handler())
			defer upstream.Close()

			reg := toolsTestRegistry(t, upstream.URL)
			server := startToolsBridge(t, reg, tc.cfg)

			status, _, raw := postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"user","content":"hi"}]}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d; want 200", status)
			}
			if string(raw) != verbatimBody {
				t.Errorf("loop-off response must be byte-identical:\n got %q\nwant %q", raw, verbatimBody)
			}
			captured := up.call(0)
			if captured == nil {
				t.Fatal("upstream received no request")
			}
			if _, hasTools := captured["tools"]; hasTools {
				t.Error("loop-off request must not carry a tools key")
			}
			if up.count() != 1 {
				t.Errorf("upstream calls = %d; want 1", up.count())
			}
		})
	}
}

// Requests that already declare their own tools always keep the verbatim
// proxy path, even when the loop is enabled (plan 8b R1-7 condition 2).
func TestLoopOff_RequestWithToolsPassthrough(t *testing.T) {
	up := &scriptedUpstream{turns: []turnFunc{jsonTurn(verbatimBody)}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read", "search", "write", "exec"}})

	reqBody := `{"model":"tool-model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"client_tool","parameters":{"type":"object"}}}]}`
	status, _, raw := postChat(t, server.URL, reqBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if string(raw) != verbatimBody {
		t.Errorf("tools-bearing request must stay verbatim, got %q", raw)
	}
	captured := up.call(0)
	tools, _ := captured["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("upstream tools = %v; want the client's own list untouched", captured["tools"])
	}
}

// FIX-6: the passthrough copies the inbound request, so every header agy sent
// would otherwise ride along to the provider. Only Content-Type and the
// Authorization rule may survive.
func TestLoopOff_StripsInboundHeaders(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, verbatimBody)
	}))
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{})

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"tool-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testSessionKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", "agy-gemini-key-must-not-leak")
	req.Header.Set("X-Custom-Thing", "must-not-be-forwarded")
	req.Header.Set("User-Agent", "google-genai-sdk/1.74.0")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if string(raw) != verbatimBody {
		t.Errorf("response must stay byte-identical, got %q", raw)
	}
	if got == nil {
		t.Fatal("upstream received no request")
	}

	dump := fmt.Sprintf("%v", got)
	for _, forbidden := range []string{
		"X-Goog-Api-Key", "agy-gemini-key-must-not-leak",
		"X-Custom-Thing", "must-not-be-forwarded",
		"google-genai-sdk/1.74.0",
	} {
		if strings.Contains(dump, forbidden) {
			t.Errorf("inbound header %q reached the provider: %s", forbidden, dump)
		}
	}
	if got.Get("Authorization") != "Bearer secret-upstream-key-abcdef" {
		t.Errorf("Authorization = %q; want the provider key, not the bridge session key", got.Get("Authorization"))
	}
	if got.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got.Get("Content-Type"))
	}
}

// ---------------------------------------------------------------------------
// R1-2: text + tool_calls in one turn => tool_calls win, loop continues
// ---------------------------------------------------------------------------

func TestToolLoop_TextAndToolCallsSameTurn(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("FILEBODY-42"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(
			`{"choices":[{"index":0,"delta":{"content":"Let me check."}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]}}]}`,
		),
		sseTurn(`{"choices":[{"index":0,"delta":{"content":"Final answer."}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"read"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if got := sseContents(t, raw); got != "Final answer." {
		t.Errorf("final content = %q; want the post-tool text, not the mixed turn", got)
	}

	second := up.call(1)
	if second == nil {
		t.Fatal("loop did not continue after the mixed turn")
	}
	found := false
	for _, rawMsg := range second["messages"].([]any) {
		m := rawMsg.(map[string]any)
		if m["role"] != "assistant" {
			continue
		}
		if m["content"] == "Let me check." {
			if calls, _ := m["tool_calls"].([]any); len(calls) == 1 {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("mixed-turn assistant message (text+tool_calls) missing from re-injected history: %+v", second["messages"])
	}
}

// ---------------------------------------------------------------------------
// R1-8: iteration exhaustion and panic both end with non-empty text
// ---------------------------------------------------------------------------

func TestToolLoop_MaxIterationsFallback(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"list_dir","arguments":"{}"}}]}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"loop forever"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if got := sseContents(t, raw); got != toolLoopApology {
		t.Errorf("exhausted loop content = %q; want fixed apology", got)
	}
	fr, _ := sseLastMeta(t, raw)
	if fr != "stop" {
		t.Errorf("finish_reason = %q; want stop", fr)
	}
	events := parseSSEData(t, raw)
	if events[len(events)-1] != "[DONE]" {
		t.Errorf("stream must end with [DONE], got %q", events[len(events)-1])
	}
	if up.count() != toolLoopMaxIterations {
		t.Errorf("upstream turns = %d; want max %d", up.count(), toolLoopMaxIterations)
	}
}

func TestToolLoop_PanicRecoveryEmitsFallbackText(t *testing.T) {
	lp := &toolLoop{
		target:     providers.Target{},
		tools:      &bridgeToolSet{},
		redact:     func(s string) string { return s },
		maxIter:    4,
		wallBudget: time.Second,
		turnBudget: time.Second,
		client:     toolLoopHTTPClient,
		turnHook:   func(turn int) { panic("synthetic loop panic") },
	}
	out := lp.run(context.Background())
	if out == nil || strings.TrimSpace(out.Content()) == "" {
		t.Fatalf("panic must still produce non-empty text, got %+v", out)
	}
	if out.Content() != toolLoopApology {
		t.Errorf("panic content = %q; want fixed apology", out.Content())
	}
}

func TestToolLoop_PanicKeepsLastNonEmptyText(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("FILEBODY-42"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(
			`{"choices":[{"index":0,"delta":{"content":"partial text"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_p","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]}}]}`,
		),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	target, err := findBestTarget(reg, "tool-model")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := resolveBridgeTools([]string{"read"})
	if err != nil || tools == nil {
		t.Fatalf("resolveBridgeTools: %v %v", tools, err)
	}

	lp := &toolLoop{
		target:        target,
		tools:         tools,
		workspaceRoot: ws,
		redact:        reg.Redactor(),
		maxIter:       4,
		wallBudget:    5 * time.Second,
		turnBudget:    5 * time.Second,
		client:        toolLoopHTTPClient,
		turnHook: func(turn int) {
			if turn == 1 {
				panic("boom after first turn")
			}
		},
	}
	out := lp.run(context.Background())
	if out.Content() != "partial text" {
		t.Errorf("panic fallback content = %q; want last non-empty text %q", out.Content(), "partial text")
	}
}

// ---------------------------------------------------------------------------
// R1-4: Redactor masks tool outputs re-injected upstream and the audit log
// ---------------------------------------------------------------------------

func TestToolLoop_RedactorMasksToolOutputAndAuditLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)

	ws := t.TempDir()
	secret := "sk-abcdefghijklmno123456"
	leak := "leak " + secret + " and secret-upstream-key-abcdef"
	if err := os.WriteFile(filepath.Join(ws, "secret.txt"), []byte(leak), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_s","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"secret.txt\"}"}}]}}]}`),
		sseTurn(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"read secret"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body %s)", status, raw)
	}

	// (b) tool output re-injected as role:"tool" must be masked.
	second := up.call(1)
	if second == nil {
		t.Fatal("no second upstream request")
	}
	var toolContent string
	for _, rawMsg := range second["messages"].([]any) {
		m := rawMsg.(map[string]any)
		if m["role"] == "tool" {
			toolContent, _ = m["content"].(string)
		}
	}
	if toolContent == "" {
		t.Fatal("no role:tool message re-injected")
	}
	if strings.Contains(toolContent, secret) || strings.Contains(toolContent, "secret-upstream-key-abcdef") {
		t.Errorf("tool output re-injected unmasked: %q", toolContent)
	}
	if !strings.Contains(toolContent, "[REDACTED]") {
		t.Errorf("tool output should show redaction marker: %q", toolContent)
	}

	// (a) the loop audit log must be masked too (R1-4a).
	logDir := filepath.Join(home, "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("audit log dir missing: %v", err)
	}
	var logText strings.Builder
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bridge-tools-") && strings.HasSuffix(e.Name(), ".jsonl") {
			data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			logText.Write(data)
		}
	}
	if logText.Len() == 0 {
		t.Fatal("bridge-tools audit log was not written")
	}
	if strings.Contains(logText.String(), secret) || strings.Contains(logText.String(), "secret-upstream-key-abcdef") {
		t.Errorf("audit log leaked secrets: %s", logText.String())
	}
}

// ---------------------------------------------------------------------------
// R1-3: workspace root env-over-cwd, per-bridge isolation
// ---------------------------------------------------------------------------

func TestResolveBridgeWorkspaceRoot(t *testing.T) {
	t.Setenv(envBridgeWorkspace, "/tmp/custom-root")
	if got := resolveBridgeWorkspaceRoot(); got != "/tmp/custom-root" {
		t.Errorf("root = %q; want env override", got)
	}
	t.Setenv(envBridgeWorkspace, "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveBridgeWorkspaceRoot(); got != wd {
		t.Errorf("root = %q; want cwd %q", got, wd)
	}
}

func TestToolLoop_WorkspaceRootIsolation(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "only_a.txt"), []byte("AAA-ONLY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "only_b.txt"), []byte("BBB-ONLY"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Each bridge session gets its own mock provider; both ask the model to
	// read the same relative path "only_a.txt".
	turns := []turnFunc{
		sseTurn(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"only_a.txt\"}"}}]}}]}`),
		sseTurn(`{"choices":[{"index":0,"delta":{"content":"done"}}]}`),
	}
	cfg := BridgeToolsConfig{Enabled: true, Tools: []string{"read"}}

	upA := &scriptedUpstream{turns: turns}
	upstreamA := httptest.NewServer(upA.handler())
	defer upstreamA.Close()
	t.Setenv(envBridgeWorkspace, dirA)
	serverA := startToolsBridge(t, toolsTestRegistry(t, upstreamA.URL), cfg)

	upB := &scriptedUpstream{turns: turns}
	upstreamB := httptest.NewServer(upB.handler())
	defer upstreamB.Close()
	t.Setenv(envBridgeWorkspace, dirB)
	serverB := startToolsBridge(t, toolsTestRegistry(t, upstreamB.URL), cfg)

	postChat(t, serverA.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"read"}]}`)
	gotA := contentOf(upA.call(1))
	postChat(t, serverB.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"read"}]}`)
	gotB := contentOf(upB.call(1))

	if !strings.Contains(gotA, "AAA-ONLY") {
		t.Errorf("bridge A tool output = %q; want AAA-ONLY", gotA)
	}
	if strings.Contains(gotB, "AAA-ONLY") {
		t.Errorf("bridge B leaked bridge A workspace: %q", gotB)
	}
	if !strings.Contains(gotB, "Error") {
		t.Errorf("bridge B tool output = %q; want a not-found error", gotB)
	}
}

// ---------------------------------------------------------------------------
// R1-10: gemini wire => explicit logged 404
// ---------------------------------------------------------------------------

func TestGeminiWire_Explicit404(t *testing.T) {
	reg := toolsTestRegistry(t, "http://127.0.0.1:1")
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	for _, path := range []string{
		"/v1beta/models/gemini-3.8-flash:streamGenerateContent",
		"/v1beta/models/gemini-3.8-flash:generateContent",
	} {
		resp := authDo(t, http.MethodPost, server.URL+path, "application/json", `{"contents":[]}`)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d; want 404", path, resp.StatusCode)
		}
		if !strings.Contains(strings.ToLower(string(raw)), "gemini") && !strings.Contains(string(raw), "streamGenerateContent") {
			t.Errorf("%s body must name the unsupported wire, got %s", path, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// DDC-5: system prompt injection both branches + upstream error fallback
// ---------------------------------------------------------------------------

func TestToolLoop_InstructionInjectionBranches(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)
	up := &scriptedUpstream{turns: []turnFunc{
		jsonTurn(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	// Branch 1: messages[0] is system -> append to its content.
	postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"system","content":"You are agy."},{"role":"user","content":"hi"}]}`)
	m0 := msgAt(t, up.call(0), 0)
	content, _ := m0["content"].(string)
	if m0["role"] != "system" || !strings.Contains(content, "You are agy.") || !strings.Contains(content, "<tool_instructions>") {
		t.Errorf("system append branch failed: role=%v content=%q", m0["role"], content)
	}

	// Branch 2: messages[0] is user -> insert system at index 0.
	postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"user","content":"hi"}]}`)
	m0 = msgAt(t, up.call(1), 0)
	content, _ = m0["content"].(string)
	if m0["role"] != "system" || !strings.Contains(content, "<tool_instructions>") {
		t.Errorf("system insert branch failed: role=%v content=%q", m0["role"], content)
	}
	if m := msgAt(t, up.call(1), 1); m["role"] != "user" {
		t.Errorf("messages[1].role = %v; want user", m["role"])
	}
}

func TestToolLoop_UpstreamErrorFallsBackWithoutRawErrorBody(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)
	up := &scriptedUpstream{turns: []turnFunc{
		func(w http.ResponseWriter, body map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"boom internal sk-abcdefghijklmno123456"}`)
		},
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 with synthesized fallback", status)
	}
	content := sseContents(t, raw)
	if content != toolLoopApology {
		t.Errorf("error fallback content = %q; want fixed apology", content)
	}
	if strings.Contains(string(raw), "boom internal") {
		t.Errorf("raw upstream error body leaked into the response: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// F-01: SSE synthesis must cut chunks on rune boundaries, never mid-UTF-8
// (the chunking itself lives in internal/openaiup and is pinned there)
// ---------------------------------------------------------------------------

func TestToolLoop_KoreanLongStreamNoCorruption(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	korean := strings.Repeat("가나다라마바사아자차카타파하한글테스트", 120) // 6480 bytes
	reasoning := strings.Repeat("생각의흐름이계속이어집니다", 200)    // 3900 bytes
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
			"reasoning_content": reasoning,
			"content":           korean,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	up := &scriptedUpstream{turns: []turnFunc{sseTurn(string(payload))}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"긴 한글 답변"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}

	if got := sseContents(t, raw); got != korean {
		t.Errorf("content reconstruction corrupted: len=%d want=%d equal=%v", len(got), len(korean), got == korean)
	}

	var gotReasoning strings.Builder
	contentPieces := 0
	for _, ev := range parseSSEData(t, raw) {
		if ev == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(ev), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		ch, _ := choices[0].(map[string]any)
		delta, _ := ch["delta"].(map[string]any)
		if r, ok := delta["reasoning_content"].(string); ok {
			gotReasoning.WriteString(r)
		}
		if _, ok := delta["content"].(string); ok {
			contentPieces++
		}
	}
	if gotReasoning.String() != reasoning {
		t.Errorf("reasoning reconstruction corrupted: len=%d want=%d", gotReasoning.Len(), len(reasoning))
	}
	if contentPieces < 2 {
		t.Errorf("content pieces = %d; want the >2048-byte text to be split (proves the rune-boundary path ran)", contentPieces)
	}
	if strings.Contains(string(raw), "\uFFFD") {
		t.Error("synthesized stream contains U+FFFD: multi-byte runes were split mid-character")
	}
	if !utf8.Valid(raw) {
		t.Error("synthesized stream is not valid UTF-8")
	}
}

// ---------------------------------------------------------------------------
// F-03: upstream turn failures are recorded (redacted) in the audit log
// ---------------------------------------------------------------------------

func TestToolLoop_UpstreamErrorAudited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		func(w http.ResponseWriter, body map[string]any) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"boom internal sk-abcdefghijklmno123456"}`)
		},
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 with synthesized fallback", status)
	}
	if got := sseContents(t, raw); got != toolLoopApology {
		t.Errorf("fallback content = %q; want apology", got)
	}

	logDir := filepath.Join(home, "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("audit log dir missing (F-03 requires an upstream-error record): %v", err)
	}
	var logText strings.Builder
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bridge-tools-") && strings.HasSuffix(e.Name(), ".jsonl") {
			data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			logText.Write(data)
		}
	}
	text := logText.String()
	if !strings.Contains(text, "upstream_turn") {
		t.Errorf("audit log missing upstream_turn record: %s", text)
	}
	if !strings.Contains(text, "status 500") {
		t.Errorf("audit log missing the failure status: %s", text)
	}
	if strings.Contains(text, "sk-abcdefghijklmno123456") {
		t.Errorf("raw upstream error body leaked into the audit log: %s", text)
	}
}

// ---------------------------------------------------------------------------
// FIX-28: the audit sinks must mask key-shaped registry names. A value
// misdirected into a provider-name or model-id slot used to render raw in the
// durable 0600 JSONL and on stderr, because only the error snippet was
// redacted there.
// ---------------------------------------------------------------------------

func TestToolLoop_UpstreamErrorAuditMasksKeyShapedNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	const (
		provPlant  = "sk-planted-provider-name-0123456789abcdef"
		modelPlant = "sk-planted-model-id-0123456789abcdef"
	)
	t.Setenv("TEST_BRIDGE_TOOLS_KEY", "secret-upstream-key-abcdef")

	up := &scriptedUpstream{turns: []turnFunc{
		func(w http.ResponseWriter, body map[string]any) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"boom internal"}`)
		},
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      provPlant,
		BaseURL:   upstream.URL,
		APIKeyEnv: "TEST_BRIDGE_TOOLS_KEY",
		Models: []providers.Model{
			{ID: modelPlant, Capabilities: providers.Capabilities{SupportsTools: true}},
		},
	}}}

	// Capture the log.Printf stderr sink alongside the JSONL sink.
	var stderr bytes.Buffer
	log.SetOutput(&stderr)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})
	status, _, raw := postChat(t, server.URL, `{"model":"`+modelPlant+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 with synthesized fallback (body %s)", status, raw)
	}
	if got := sseContents(t, raw); got != toolLoopApology {
		t.Errorf("fallback content = %q; want apology", got)
	}

	logDir := filepath.Join(home, "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("audit log dir missing: %v", err)
	}
	var logText strings.Builder
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bridge-tools-") && strings.HasSuffix(e.Name(), ".jsonl") {
			data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			logText.Write(data)
		}
	}
	jsonl := logText.String()
	if !strings.Contains(jsonl, "upstream_turn") {
		t.Fatalf("audit log missing the upstream_turn record: %s", jsonl)
	}
	for _, plant := range []string{provPlant, modelPlant} {
		if strings.Contains(jsonl, plant) {
			t.Errorf("audit JSONL leaked %q raw:\n%s", plant, jsonl)
		}
		if strings.Contains(stderr.String(), plant) {
			t.Errorf("stderr audit line leaked %q raw:\n%s", plant, stderr.String())
		}
	}
	if !strings.Contains(jsonl, "sk-pl****") {
		t.Errorf("audit JSONL carries no masked registry name:\n%s", jsonl)
	}
	if !strings.Contains(stderr.String(), "sk-pl****") {
		t.Errorf("stderr audit line carries no masked registry name:\n%s", stderr.String())
	}
	// The masked names still identify the failing target.
	if !strings.Contains(stderr.String(), "tool loop upstream turn 0 failed for") {
		t.Errorf("stderr audit line lost its shape:\n%s", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// F-04 / security M-1: per-turn tool call cap, per-tool deadline, wall budget
// ---------------------------------------------------------------------------

func TestToolLoop_ToolCallsPerTurnCapped(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	total := toolLoopMaxToolCallsPerTurn + 4
	events := make([]string, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, fmt.Sprintf(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"id":"call_%d","type":"function","function":{"name":"list_dir","arguments":"{}"}}]}}]}`, i, i))
	}
	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(events...),
		sseTurn(`{"choices":[{"index":0,"delta":{"content":"done"}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"flood"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body %s)", status, raw)
	}
	if got := sseContents(t, raw); got != "done" {
		t.Errorf("final content = %q; want done", got)
	}

	second := up.call(1)
	if second == nil {
		t.Fatal("loop did not re-feed tool results")
	}
	var toolMsgs []map[string]any
	assistantCalls := 0
	for _, rawMsg := range second["messages"].([]any) {
		m := rawMsg.(map[string]any)
		switch m["role"] {
		case "tool":
			toolMsgs = append(toolMsgs, m)
		case "assistant":
			if calls, _ := m["tool_calls"].([]any); len(calls) > 0 {
				assistantCalls = len(calls)
			}
		}
	}
	if assistantCalls != total {
		t.Errorf("assistant tool_calls = %d; want %d (protocol pairing kept)", assistantCalls, total)
	}
	if len(toolMsgs) != total {
		t.Fatalf("tool messages = %d; want one per call (%d)", len(toolMsgs), total)
	}
	for i, m := range toolMsgs {
		content, _ := m["content"].(string)
		dropped := strings.Contains(content, "dropped")
		if i < toolLoopMaxToolCallsPerTurn && dropped {
			t.Errorf("call %d is within cap but reported dropped: %q", i, content)
		}
		if i >= toolLoopMaxToolCallsPerTurn && !dropped {
			t.Errorf("call %d exceeds cap but was executed: %q", i, content)
		}
	}
}

func TestToolLoop_ToolDeadlineAbortsHungTool(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_h","type":"function","function":{"name":"read_file","arguments":"{}"}}]}}]}`),
		sseTurn(`{"choices":[{"index":0,"delta":{"content":"done"}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	target, err := findBestTarget(reg, "tool-model")
	if err != nil {
		t.Fatal(err)
	}
	tools, err := resolveBridgeTools([]string{"read"})
	if err != nil || tools == nil {
		t.Fatalf("resolveBridgeTools: %v %v", tools, err)
	}

	lp := &toolLoop{
		target:        target,
		tools:         tools,
		workspaceRoot: ws,
		redact:        reg.Redactor(),
		messages:      []any{map[string]any{"role": "user", "content": "hi"}},
		maxIter:       4,
		wallBudget:    5 * time.Second,
		turnBudget:    5 * time.Second,
		toolBudget:    20 * time.Millisecond,
		client:        toolLoopHTTPClient,
		execOverride: func(name, argsJSON string) string {
			time.Sleep(300 * time.Millisecond)
			return "SLOW-RESULT"
		},
	}
	out := lp.run(context.Background())
	if out.Content() != "done" {
		t.Fatalf("loop content = %q; want done (loop must survive a hung tool)", out.Content())
	}
	toolContent := contentOf(up.call(1))
	if !strings.Contains(toolContent, "deadline") {
		t.Errorf("hung tool result = %q; want a deadline error", toolContent)
	}
	if strings.Contains(toolContent, "SLOW-RESULT") {
		t.Errorf("late tool result must be discarded, got %q", toolContent)
	}
}

func TestToolLoop_ToolExecCancelledDuringExecution(t *testing.T) {
	agg := openaiup.NewTurn()
	idx := 0
	agg.MergeToolCall(&openaiup.ToolCall{
		Index:    &idx,
		ID:       "call_1",
		Type:     "function",
		Function: openaiup.ToolCallFunction{Name: "read_file", Arguments: "{}"},
	})

	lp := &toolLoop{
		tools:      &bridgeToolSet{},
		redact:     func(s string) string { return s },
		toolBudget: time.Second,
		execOverride: func(name, argsJSON string) string {
			time.Sleep(300 * time.Millisecond)
			return "late"
		},
	}

	// Wall budget expires while the tool is running.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	msgs := lp.executeTools(ctx, agg)
	cancel()
	if len(msgs) != 1 {
		t.Fatalf("tool messages = %d; want 1", len(msgs))
	}
	content, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, "cancelled during execution") {
		t.Errorf("content = %q; want cancellation during execution", content)
	}

	// Already-cancelled context: the tool must not be dispatched at all.
	executed := false
	lp2 := &toolLoop{
		tools:      &bridgeToolSet{},
		redact:     func(s string) string { return s },
		toolBudget: time.Second,
		execOverride: func(name, argsJSON string) string {
			executed = true
			return "ran"
		},
	}
	doneCtx, doneCancel := context.WithCancel(context.Background())
	doneCancel()
	msgs2 := lp2.executeTools(doneCtx, agg)
	if executed {
		t.Error("cancelled context must not dispatch the tool")
	}
	content2, _ := msgs2[0].(map[string]any)["content"].(string)
	if !strings.Contains(content2, "cancelled before execution") {
		t.Errorf("content = %q; want pre-execution cancellation", content2)
	}
}

// ---------------------------------------------------------------------------
// F-06: unknown tool names fail closed on every allow-list
// ---------------------------------------------------------------------------

func TestFilterToolDefs_FailClosedUnknownTool(t *testing.T) {
	defs := []agenttools.ToolDef{
		{Type: "function", Function: agenttools.FunctionDef{Name: "read_file"}},
		{Type: "function", Function: agenttools.FunctionDef{Name: "mystery_tool"}},
		{Type: "function", Function: agenttools.FunctionDef{Name: "run_command"}},
	}
	// Even a write+exec allow-list must not expose an unmapped tool name.
	ts := filterToolDefs(defs, map[string]bool{"read": true, "write": true, "exec": true})
	names := strings.Join(ts.names, ",")
	if strings.Contains(names, "mystery_tool") {
		t.Errorf("unknown tool must be dropped fail-closed, got %q", names)
	}
	if names != "read_file,run_command" {
		t.Errorf("names = %q; want read_file,run_command", names)
	}

	// read-only allow-list keeps only the read token tools.
	tsRead := filterToolDefs(defs, map[string]bool{"read": true})
	if strings.Join(tsRead.names, ",") != "read_file" {
		t.Errorf("read-only names = %q; want read_file", strings.Join(tsRead.names, ","))
	}
}

// FIX-13: serveToolLoop's nil-redact fallback must mask, not pass through. The
// re-injected tool message is the sink, and a loop invoked without a redactor
// used to write the raw tool output upstream.
func TestServeToolLoop_NilRedactMasksToolOutput(t *testing.T) {
	ws := t.TempDir()
	const planted = "AIzaSyDELmE1f2g3h4i5j6k7l8m9n0o1p2q3r4s"
	if err := os.WriteFile(filepath.Join(ws, "secret.txt"), []byte("leak "+planted), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_s","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"secret.txt\"}"}}]}}]}`),
		sseTurn(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	tools, err := resolveBridgeTools([]string{"read"})
	if err != nil || tools == nil {
		t.Fatalf("resolveBridgeTools: %v %v", tools, err)
	}
	target, err := findBestTarget(reg, "tool-model")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"read"}]}`))
	serveToolLoop(rec, req, loopEnv{
		rawReq:   map[string]any{"stream": true, "model": "tool-model", "messages": []any{map[string]any{"role": "user", "content": "read"}}},
		target:   target,
		modelReq: "tool-model",
		tools:    tools,
		root:     ws,
		redact:   nil, // the FIX-13 path
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body %s)", rec.Code, rec.Body.String())
	}
	toolContent := contentOf(up.call(1))
	if toolContent == "" {
		t.Fatal("no role:tool message re-injected")
	}
	if strings.Contains(toolContent, planted) {
		t.Errorf("nil redactor re-injected the credential raw: %q", toolContent)
	}
	if !strings.Contains(toolContent, "[REDACTED]") {
		t.Errorf("tool output should show the redaction marker: %q", toolContent)
	}
}

// ---------------------------------------------------------------------------
// openaiup.Call seam: the loop still authenticates upstream exactly as before
// the transport moved into internal/openaiup
// ---------------------------------------------------------------------------

// jsonOKTurn answers one non-streaming turn so the loop finishes immediately.
func jsonOKTurn(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
}

func TestToolLoop_SendsBearerKeyUpstream(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		jsonOKTurn(w)
	}))
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, _ := postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if gotAuth != "Bearer secret-upstream-key-abcdef" {
		t.Errorf("upstream Authorization = %q; want the provider key as a bearer token", gotAuth)
	}
}

// A provider that declares no key variable must not send an Authorization
// header at all (never a bare "Bearer ").
func TestToolLoop_OmitsAuthorizationWhenKeyEmpty(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	var gotAuth string
	sawHeader := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["Authorization"]
		gotAuth = r.Header.Get("Authorization")
		jsonOKTurn(w)
	}))
	defer upstream.Close()

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:    "tool-prov",
				BaseURL: upstream.URL,
				Models: []providers.Model{
					{ID: "tool-model", Capabilities: providers.Capabilities{SupportsTools: true}},
				},
			},
		},
	}
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, _ := postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if sawHeader {
		t.Errorf("upstream Authorization = %q; want the header absent when the provider declares no key", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// sumUsage: cross-turn cache-token accounting (audit finding G5, F-04)
// ---------------------------------------------------------------------------

// G5/F-04: sumUsage summed prompt/completion/total across the tool loop's
// turns but kept only the LAST turn's cache-token count, so a bridged request
// whose early turns hit the cache and whose final turn missed reported the
// final turn's hit count against the summed prompt size. Turn 1 and turn 2
// below report DIFFERENT cached_tokens, so the aggregated block must carry
// their sum, not the last turn's value.
func TestSumUsage_SumsNestedCachedTokensAcrossTurns(t *testing.T) {
	nested := func(prompt, cached int) map[string]any {
		return map[string]any{
			"prompt_tokens":     float64(prompt),
			"completion_tokens": float64(1),
			"total_tokens":      float64(prompt + 1),
			"prompt_tokens_details": map[string]any{
				"cached_tokens": float64(cached),
				"audio_tokens":  0,
			},
		}
	}
	turn1, turn2 := nested(100, 64), nested(200, 16)

	out := sumUsage([]map[string]any{turn1, turn2})
	if out == nil {
		t.Fatal("sumUsage returned nil for turns that reported a usage block")
	}
	// The plain counts were already cross-turn sums; the cache count joins them.
	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"prompt_tokens", 300},
		{"completion_tokens", 2},
		{"total_tokens", 302},
	} {
		if got, ok := usageNum(out, tc.key); !ok || got != tc.want {
			t.Errorf("%s = %v; want %v summed across both turns", tc.key, out[tc.key], tc.want)
		}
	}
	details, ok := out["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_tokens_details missing from the aggregated block: %v", out)
	}
	if got, ok := usageNum(details, "cached_tokens"); !ok || got != 80 {
		t.Errorf("prompt_tokens_details.cached_tokens = %v; want 80 (64+16 summed across turns, not the last turn's 16)", details["cached_tokens"])
	}
	// The rest of the last turn's details block survives untouched.
	if v, ok := details["audio_tokens"]; !ok || v != 0 {
		t.Errorf("prompt_tokens_details.audio_tokens = %v; want the last turn's 0 preserved", v)
	}
	// Aggregation must not rewrite the provider's own turn blocks.
	for i, turn := range []map[string]any{turn1, turn2} {
		d, ok := turn["prompt_tokens_details"].(map[string]any)
		if !ok {
			t.Fatalf("turn %d lost its details block", i+1)
		}
		want := float64(64)
		if i == 1 {
			want = 16
		}
		if got, _ := usageNum(d, "cached_tokens"); got != want {
			t.Errorf("turn %d's own cached_tokens = %v; want %v (sumUsage must not mutate its input)", i+1, got, want)
		}
	}
}

// The DeepSeek-native spelling (top-level prompt_cache_hit_tokens) must be
// summed in place, in that same spelling, and never folded into the nested
// OpenAI shape.
func TestSumUsage_SumsNativeCacheHitTokensAcrossTurns(t *testing.T) {
	native := func(prompt, hit int) map[string]any {
		return map[string]any{
			"prompt_tokens":            float64(prompt),
			"completion_tokens":        float64(1),
			"total_tokens":             float64(prompt + 1),
			"prompt_cache_hit_tokens":  float64(hit),
			"prompt_cache_miss_tokens": float64(prompt - hit),
		}
	}
	out := sumUsage([]map[string]any{native(100, 90), native(200, 20)})
	if out == nil {
		t.Fatal("sumUsage returned nil for turns that reported a usage block")
	}
	if got, ok := usageNum(out, "prompt_cache_hit_tokens"); !ok || got != 110 {
		t.Errorf("prompt_cache_hit_tokens = %v; want 110 (90+20 summed across turns, not the last turn's 20)", out["prompt_cache_hit_tokens"])
	}
	if got, _ := usageNum(out, "prompt_tokens"); got != 300 {
		t.Errorf("prompt_tokens = %v; want 300 summed across both turns", out["prompt_tokens"])
	}
	// The miss count is summed the same way (10+180=190), so hit+miss==prompt
	// holds on the aggregated block.
	if got, ok := usageNum(out, "prompt_cache_miss_tokens"); !ok || got != 190 {
		t.Errorf("prompt_cache_miss_tokens = %v; want 190 (10+180 summed across turns, not the last turn's 180)", out["prompt_cache_miss_tokens"])
	}
	hit, _ := usageNum(out, "prompt_cache_hit_tokens")
	miss, _ := usageNum(out, "prompt_cache_miss_tokens")
	if prompt, _ := usageNum(out, "prompt_tokens"); hit+miss != prompt {
		t.Errorf("hit+miss = %v; want == summed prompt_tokens %v on the aggregated block", hit+miss, prompt)
	}
	// No nested block is fabricated for a provider that uses the native shape.
	if _, exists := out["prompt_tokens_details"]; exists {
		t.Errorf("prompt_tokens_details = %v; want no nested block fabricated for the native shape", out["prompt_tokens_details"])
	}
}

// A provider that mixes spellings across turns gets each summed within its own
// spelling and written back in that spelling, so a consumer reading either
// field sees the whole total for that field.
func TestSumUsage_MixedCacheSpellingsEachSummedInPlace(t *testing.T) {
	out := sumUsage([]map[string]any{
		{"prompt_tokens": float64(100), "prompt_tokens_details": map[string]any{"cached_tokens": float64(64)}},
		{"prompt_tokens": float64(100), "prompt_cache_hit_tokens": float64(50)},
	})
	if out == nil {
		t.Fatal("sumUsage returned nil for turns that reported a usage block")
	}
	details, ok := out["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_tokens_details missing from the aggregated block: %v", out)
	}
	if got, _ := usageNum(details, "cached_tokens"); got != 64 {
		t.Errorf("nested cached_tokens = %v; want 64 (only turn 1 used the nested spelling)", details["cached_tokens"])
	}
	if got, _ := usageNum(out, "prompt_cache_hit_tokens"); got != 50 {
		t.Errorf("native prompt_cache_hit_tokens = %v; want 50 (only turn 2 used the native spelling)", out["prompt_cache_hit_tokens"])
	}
}

// A provider that reports no cache field at all keeps the prior behavior: the
// summed plain counts plus the last turn's other keys, and no cache key
// fabricated. A zero would read as "0 tokens cached" instead of "not reported".
func TestSumUsage_NoCacheFieldFabricatesNothing(t *testing.T) {
	out := sumUsage([]map[string]any{
		{"prompt_tokens": float64(10), "completion_tokens": float64(2), "total_tokens": float64(12)},
		{
			"prompt_tokens": float64(30), "completion_tokens": float64(3), "total_tokens": float64(33),
			"prompt_tokens_details": map[string]any{"audio_tokens": 5},
		},
	})
	if out == nil {
		t.Fatal("sumUsage returned nil for turns that reported a usage block")
	}
	if got, _ := usageNum(out, "prompt_tokens"); got != 40 {
		t.Errorf("prompt_tokens = %v; want 40 summed across both turns", out["prompt_tokens"])
	}
	details, ok := out["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("the last turn's prompt_tokens_details must survive: %v", out)
	}
	if _, exists := details["cached_tokens"]; exists {
		t.Errorf("cached_tokens = %v; want no fabricated zero when no turn reported a cache count", details["cached_tokens"])
	}
	if v, ok := details["audio_tokens"]; !ok || v != 5 {
		t.Errorf("prompt_tokens_details.audio_tokens = %v; want the last turn's 5 preserved", v)
	}
	if _, exists := out["prompt_cache_hit_tokens"]; exists {
		t.Errorf("prompt_cache_hit_tokens = %v; want no fabricated zero when no turn reported a cache count", out["prompt_cache_hit_tokens"])
	}
}

// The nil contract is unchanged: no usage block anywhere means no block out.
func TestSumUsage_NilWhenNoTurnReportedUsage(t *testing.T) {
	if out := sumUsage(nil); out != nil {
		t.Errorf("sumUsage(nil) = %v; want nil", out)
	}
	if out := sumUsage([]map[string]any{}); out != nil {
		t.Errorf("sumUsage of no turns = %v; want nil", out)
	}
	if out := sumUsage([]map[string]any{nil, nil}); out != nil {
		t.Errorf("sumUsage of nil turns = %v; want nil", out)
	}
}

// FIX 5: a negative count is treated as absent (never accumulated) and a summed
// count saturates to +Inf instead of wrapping negative. usageNum is the single
// gate; sumUsage inherits both properties from it.
func TestUsageNumAndSumRejectNegativeAndSaturate(t *testing.T) {
	// usageNum rejects a negative directly.
	if _, ok := usageNum(map[string]any{"x": float64(-5)}, "x"); ok {
		t.Error("usageNum accepted a negative count; want ok=false")
	}
	// A negative nested cache count does not drag a valid turn's sum down.
	out := sumUsage([]map[string]any{
		{"prompt_tokens": float64(100), "prompt_tokens_details": map[string]any{"cached_tokens": float64(64)}},
		{"prompt_tokens": float64(100), "prompt_tokens_details": map[string]any{"cached_tokens": float64(-40)}},
	})
	if out == nil {
		t.Fatal("sumUsage returned nil for turns that reported a usage block")
	}
	details, _ := out["prompt_tokens_details"].(map[string]any)
	if got, _ := usageNum(details, "cached_tokens"); got != 64 {
		t.Errorf("nested cached_tokens = %v; want 64 (the negative turn contributed nothing, not 64-40)", got)
	}
	// A negative native hit is ignored the same way.
	native := sumUsage([]map[string]any{
		{"prompt_tokens": float64(100), "prompt_cache_hit_tokens": float64(90)},
		{"prompt_tokens": float64(100), "prompt_cache_hit_tokens": float64(-50)},
	})
	if got, _ := usageNum(native, "prompt_cache_hit_tokens"); got != 90 {
		t.Errorf("prompt_cache_hit_tokens = %v; want 90 (the negative turn contributed nothing)", got)
	}
	// Overflow saturates to +Inf (positive), never wraps negative.
	big := sumUsage([]map[string]any{
		{"prompt_tokens": math.MaxFloat64},
		{"prompt_tokens": math.MaxFloat64},
	})
	got, ok := usageNum(big, "prompt_tokens")
	if !ok {
		t.Fatal("prompt_tokens missing from the overflow block")
	}
	if math.Signbit(got) {
		t.Errorf("prompt_tokens = %v; want a non-negative saturated value (no wrap to negative)", got)
	}
	if !math.IsInf(got, 1) {
		t.Errorf("prompt_tokens = %v; want saturation to +Inf", got)
	}
}

// End-to-end pin for G5/F-04 on the path agy actually consumes: a tool turn
// that hit the cache followed by a final turn that missed is exactly the shape
// the audit flagged, and the synthesized chat.completion body must carry the
// summed cache count.
func TestToolLoop_JSONBodyCarriesSummedCachedTokens(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		jsonTurn(`{"id":"u1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_c","type":"function","function":{"name":"list_dir","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":64}}}`),
		jsonTurn(`{"id":"u2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"listed"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":7,"total_tokens":207,"prompt_tokens_details":{"cached_tokens":16}}}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"user","content":"list the dir"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body %s)", status, raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("synthesized body is not JSON: %v", err)
	}
	usage, _ := parsed["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("usage missing from the synthesized body: %s", raw)
	}
	if got, _ := usageNum(usage, "prompt_tokens"); got != 300 {
		t.Errorf("prompt_tokens = %v; want 300 summed across both turns", usage["prompt_tokens"])
	}
	details, ok := usage["prompt_tokens_details"].(map[string]any)
	if !ok {
		t.Fatalf("prompt_tokens_details missing from the surfaced usage: %v", usage)
	}
	if got, _ := usageNum(details, "cached_tokens"); got != 80 {
		t.Errorf("cached_tokens = %v; want 80 (64+16 summed across the tool turn and the final turn)", details["cached_tokens"])
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): the audit sinks in bridge_tools.go quote
// lp.target.Provider.Name and lp.target.Model.ID, both registry slots. They
// route through providers.MaskSlot (the registry-slot authority) instead of
// the operand masker, whose underscore-head arm is the identity, so the
// "sk_live_"/"tok_" class no longer lands raw in the 0600 JSONL or on stderr.
// Benign names keep their exact bytes at both sinks.
// ---------------------------------------------------------------------------

func TestToolLoop_UpstreamErrorAuditMasksUnderscoreHeadNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)
	t.Setenv("TEST_BRIDGE_TOOLS_KEY", "secret-upstream-key-abcdef")

	const (
		provPlant     = "sk_live_0123456789abcdef"
		provPlantMask = "sk_li****"
		modelPlant    = "tok_a1b2c3d4e5f6g7h8i9j0"
		modelMask     = "tok_a****"
	)

	run := func(t *testing.T, provName, modelID string) (string, string) {
		t.Helper()
		up := &scriptedUpstream{turns: []turnFunc{
			func(w http.ResponseWriter, body map[string]any) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"boom internal"}`)
			},
		}}
		upstream := httptest.NewServer(up.handler())
		t.Cleanup(upstream.Close)

		reg := &providers.Registry{Providers: []providers.Provider{{
			Name:      provName,
			BaseURL:   upstream.URL,
			APIKeyEnv: "TEST_BRIDGE_TOOLS_KEY",
			Models: []providers.Model{
				{ID: modelID, Capabilities: providers.Capabilities{SupportsTools: true}},
			},
		}}}

		var stderr bytes.Buffer
		log.SetOutput(&stderr)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })

		server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})
		status, _, raw := postChat(t, server.URL, `{"model":"`+modelID+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d; want 200 with synthesized fallback (body %s)", status, raw)
		}

		logDir := filepath.Join(home, "logs")
		entries, err := os.ReadDir(logDir)
		if err != nil {
			t.Fatalf("audit log dir missing: %v", err)
		}
		var logText strings.Builder
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "bridge-tools-") && strings.HasSuffix(e.Name(), ".jsonl") {
				data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				logText.Write(data)
			}
		}
		// Each run gets a fresh home, so only this run's records are read.
		if err := os.RemoveAll(logDir); err != nil {
			t.Fatalf("reset audit log dir: %v", err)
		}
		return logText.String(), stderr.String()
	}

	jsonl, stderr := run(t, provPlant, modelPlant)
	if !strings.Contains(jsonl, "upstream_turn") {
		t.Fatalf("audit log missing the upstream_turn record: %s", jsonl)
	}
	for _, plant := range []string{provPlant, modelPlant} {
		if strings.Contains(jsonl, plant) {
			t.Errorf("audit JSONL leaked %q raw:\n%s", plant, jsonl)
		}
		if strings.Contains(stderr, plant) {
			t.Errorf("stderr audit line leaked %q raw:\n%s", plant, stderr)
		}
	}
	for _, masked := range []string{provPlantMask, modelMask} {
		if !strings.Contains(jsonl, masked) {
			t.Errorf("audit JSONL is missing the masked form %q:\n%s", masked, jsonl)
		}
		if !strings.Contains(stderr, masked) {
			t.Errorf("stderr audit line is missing the masked form %q:\n%s", masked, stderr)
		}
	}
	if !strings.Contains(stderr, "tool loop upstream turn 0 failed for") {
		t.Errorf("stderr audit line lost its shape:\n%s", stderr)
	}

	// Benign corpus: both sinks carry the names byte-identically.
	jsonl, stderr = run(t, "nw", "glm-5.3-flash-flex")
	if !strings.Contains(jsonl, `"provider":"nw"`) {
		t.Errorf("benign provider name was rewritten in the JSONL:\n%s", jsonl)
	}
	if !strings.Contains(jsonl, `"model":"glm-5.3-flash-flex"`) {
		t.Errorf("benign model id was rewritten in the JSONL:\n%s", jsonl)
	}
	if !strings.Contains(stderr, "failed for nw/glm-5.3-flash-flex") {
		t.Errorf("benign names were rewritten on stderr:\n%s", stderr)
	}
	if strings.Contains(jsonl, "****") || strings.Contains(stderr, "****") {
		t.Errorf("a benign registry name was masked: jsonl=%s stderr=%s", jsonl, stderr)
	}
}

// ---------------------------------------------------------------------------
// FIX-8 / census O4: the four JSON error sentences on the request path quote a
// request operand or a registry slot. Each interpolates through the shared
// authority (MaskOperand for the request model spec and the key-env name,
// MaskSlot for the provider name), so a credential-shaped value cannot render
// raw in the error body a local client receives, and every benign operand keeps
// its exact bytes. The plants discriminate the authorities: the provider-name
// plant is the underscore-head class that MaskOperand renders raw and only the
// slot authority masks.
// ---------------------------------------------------------------------------

// decodeBridgeErrorBody returns the "error" object of a bridge error response.
func decodeBridgeErrorBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("error body is not JSON: %v (body %s)", err, raw)
	}
	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("body carries no 'error' object: %s", raw)
	}
	return errObj
}

func TestBridgeJSONErrorMasksCredentialOperands(t *testing.T) {
	const (
		modelPlant = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		modelMask  = "wJalr****"
		provPlant  = "sk_live_0123456789abcdef"
		provMask   = "sk_li****"
		envPlant   = "ghp_0123456789abcdefghij"
		envMask    = "ghp_0****"
	)
	// Premises: each operand classifies as credential material on the arm the
	// converted site uses, and the provider plant is raw under the operand
	// masker. A reverted interpolation therefore turns the payload assertions
	// red instead of passing on an already-benign value.
	if got := providers.MaskOperand(modelPlant); got != modelMask {
		t.Fatalf("premise broken: MaskOperand(%q) = %q; want %q", modelPlant, got, modelMask)
	}
	if got := providers.MaskOperand(envPlant); got != envMask {
		t.Fatalf("premise broken: MaskOperand(%q) = %q; want %q", envPlant, got, envMask)
	}
	if got := providers.MaskSlot(provPlant); got != provMask {
		t.Fatalf("premise broken: MaskSlot(%q) = %q; want %q", provPlant, got, provMask)
	}
	if got := providers.MaskOperand(provPlant); got != provPlant {
		t.Fatalf("premise broken: MaskOperand is not identity on the provider plant (%q)", got)
	}

	t.Run("model not found masks the request operand", func(t *testing.T) {
		reg := &providers.Registry{Providers: []providers.Provider{{
			Name:    "tool-prov",
			BaseURL: "http://127.0.0.1:1",
			Models:  []providers.Model{{ID: "tool-model"}},
		}}}
		server := startToolsBridge(t, reg, BridgeToolsConfig{})
		status, _, raw := postChat(t, server.URL, `{"model":"`+modelPlant+`"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d; want 404 (body %s)", status, raw)
		}
		errObj := decodeBridgeErrorBody(t, raw)
		want := `model "` + modelMask + `" not found in configured providers`
		if errObj["message"] != want {
			t.Errorf("error.message = %q; want %q", errObj["message"], want)
		}
		if strings.Contains(string(raw), modelPlant) {
			t.Errorf("error body leaks the raw request operand: %s", raw)
		}
	})

	t.Run("unconfigured key masks the provider name and the env name", func(t *testing.T) {
		// The env name resolves to nothing, so the handler takes the 401 arm.
		reg := &providers.Registry{Providers: []providers.Provider{{
			Name:      provPlant,
			BaseURL:   "http://127.0.0.1:1",
			APIKeyEnv: envPlant,
			Models:    []providers.Model{{ID: "tool-model"}},
		}}}
		server := startToolsBridge(t, reg, BridgeToolsConfig{})
		status, _, raw := postChat(t, server.URL, `{"model":"tool-model"}`)
		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d; want 401 (body %s)", status, raw)
		}
		errObj := decodeBridgeErrorBody(t, raw)
		want := "API key for provider " + provMask + " (" + envMask + ") is not configured or empty in environment"
		if errObj["message"] != want {
			t.Errorf("error.message = %q; want %q", errObj["message"], want)
		}
		for _, plant := range []string{provPlant, envPlant} {
			if strings.Contains(string(raw), plant) {
				t.Errorf("error body leaks %q raw: %s", plant, raw)
			}
		}
	})

	t.Run("bad gateway masks the provider name", func(t *testing.T) {
		t.Setenv("TEST_BRIDGE_BAD_GATEWAY_KEY", "secret-upstream-key-abcdef")
		reg := &providers.Registry{Providers: []providers.Provider{{
			Name:      provPlant,
			BaseURL:   "http://127.0.0.1:1", // closed port: the proxy transport fails
			APIKeyEnv: "TEST_BRIDGE_BAD_GATEWAY_KEY",
			Models:    []providers.Model{{ID: "tool-model"}},
		}}}
		server := startToolsBridge(t, reg, BridgeToolsConfig{})
		status, _, raw := postChat(t, server.URL, `{"model":"tool-model","messages":[{"role":"user","content":"hi"}]}`)
		if status != http.StatusBadGateway {
			t.Fatalf("status = %d; want 502 (body %s)", status, raw)
		}
		errObj := decodeBridgeErrorBody(t, raw)
		msg, _ := errObj["message"].(string)
		wantPrefix := `bridge error contacting provider "` + provMask + `": `
		if !strings.HasPrefix(msg, wantPrefix) {
			t.Errorf("error.message = %q; want prefix %q", msg, wantPrefix)
		}
		if strings.Contains(string(raw), provPlant) {
			t.Errorf("error body leaks the raw provider name: %s", raw)
		}
	})

	t.Run("registry lookup error masks the request operand", func(t *testing.T) {
		reg := &providers.Registry{Providers: []providers.Provider{{
			Name:    "tool-prov",
			BaseURL: "http://127.0.0.1:1",
			Models:  []providers.Model{{ID: "tool-model"}},
		}}}
		_, err := findBestTarget(reg, modelPlant)
		if err == nil {
			t.Fatal("findBestTarget must fail on an unknown spec")
		}
		want := `model "` + modelMask + `" not found in configured providers`
		if err.Error() != want {
			t.Errorf("lookup error = %q; want %q", err.Error(), want)
		}
		if strings.Contains(err.Error(), modelPlant) {
			t.Errorf("lookup error leaks the raw request operand: %v", err)
		}
	})
}

// TestBridgeJSONErrorBenignOperandsByteIdentical holds the benign side of the
// same four sites: every byte of the payload a local client receives is the
// byte it received before the conversion, so the error surface does not drift.
func TestBridgeJSONErrorBenignOperandsByteIdentical(t *testing.T) {
	benignReg := func(name, apiKeyEnv, model string) *providers.Registry {
		return &providers.Registry{Providers: []providers.Provider{{
			Name:      name,
			BaseURL:   "http://127.0.0.1:1",
			APIKeyEnv: apiKeyEnv,
			Models:    []providers.Model{{ID: model}},
		}}}
	}

	t.Run("model not found", func(t *testing.T) {
		server := startToolsBridge(t, benignReg("nw", "", "glm-5.3-flash-flex"), BridgeToolsConfig{})
		status, _, raw := postChat(t, server.URL, `{"model":"missing-model"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d; want 404 (body %s)", status, raw)
		}
		want := "{\"error\":{\"code\":404,\"message\":\"model \\\"missing-model\\\" not found in configured providers\",\"type\":\"model_not_found\"}}\n"
		if string(raw) != want {
			t.Errorf("error body drifted\n got: %q\nwant: %q", raw, want)
		}
	})

	t.Run("unconfigured key", func(t *testing.T) {
		t.Setenv("UNSET_NW_KEY_FOR_TEST", "")
		server := startToolsBridge(t, benignReg("NW", "UNSET_NW_KEY_FOR_TEST", "glm-5.3-flash-flex"), BridgeToolsConfig{})
		status, _, raw := postChat(t, server.URL, `{"model":"glm-5.3-flash-flex"}`)
		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d; want 401 (body %s)", status, raw)
		}
		want := "{\"error\":{\"code\":401,\"message\":\"API key for provider NW (UNSET_NW_KEY_FOR_TEST) is not configured or empty in environment\",\"type\":\"authentication_error\"}}\n"
		if string(raw) != want {
			t.Errorf("error body drifted\n got: %q\nwant: %q", raw, want)
		}
	})

	t.Run("bad gateway", func(t *testing.T) {
		t.Setenv("TEST_BRIDGE_BAD_GATEWAY_KEY", "secret-upstream-key-abcdef")
		server := startToolsBridge(t, benignReg("broken-prov", "TEST_BRIDGE_BAD_GATEWAY_KEY", "offline-model"), BridgeToolsConfig{})
		status, _, raw := postChat(t, server.URL, `{"model":"offline-model","messages":[{"role":"user","content":"hi"}]}`)
		if status != http.StatusBadGateway {
			t.Fatalf("status = %d; want 502 (body %s)", status, raw)
		}
		errObj := decodeBridgeErrorBody(t, raw)
		msg, _ := errObj["message"].(string)
		// The transport error text is supplied by the client stack, so the
		// byte-identity assertion covers the interpolated prefix.
		wantPrefix := `bridge error contacting provider "broken-prov": `
		if !strings.HasPrefix(msg, wantPrefix) {
			t.Errorf("error.message = %q; want prefix %q", msg, wantPrefix)
		}
	})

	t.Run("registry lookup error", func(t *testing.T) {
		_, err := findBestTarget(benignReg("nw", "", "glm-5.3-flash-flex"), "missing-model")
		if err == nil {
			t.Fatal("findBestTarget must fail on an unknown spec")
		}
		if want := `model "missing-model" not found in configured providers`; err.Error() != want {
			t.Errorf("lookup error drifted\n got: %q\nwant: %q", err.Error(), want)
		}
	})
}
