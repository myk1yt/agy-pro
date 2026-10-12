package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if out == nil || strings.TrimSpace(out.content) == "" {
		t.Fatalf("panic must still produce non-empty text, got %+v", out)
	}
	if out.content != toolLoopApology {
		t.Errorf("panic content = %q; want fixed apology", out.content)
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
	if out.content != "partial text" {
		t.Errorf("panic fallback content = %q; want last non-empty text %q", out.content, "partial text")
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
// ---------------------------------------------------------------------------

func TestSplitPieces_RuneBoundaries(t *testing.T) {
	cases := []struct {
		name string
		s    string
		size int
	}{
		{"hangul small budget", strings.Repeat("한", 10), 4},
		{"mixed ascii hangul", strings.Repeat("a가", 1500), 2048},
		{"emoji 4-byte runes", strings.Repeat("😀", 5), 4},
		{"empty", "", 4},
		{"shorter than budget", "abc", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pieces := splitPieces(tc.s, tc.size)
			if strings.Join(pieces, "") != tc.s {
				t.Errorf("reconstruction lost data (input %d bytes)", len(tc.s))
			}
			for _, p := range pieces {
				if !utf8.ValidString(p) {
					t.Errorf("piece %q is not valid UTF-8", p)
				}
				if tc.size > 0 && len(p) > tc.size {
					t.Errorf("piece length %d exceeds size %d", len(p), tc.size)
				}
			}
		})
	}

	// The exact F-01 shape: 3-byte runes against a 2048-byte budget.
	s := strings.Repeat("한글", 700) // 4200 bytes
	pieces := splitPieces(s, 2048)
	if len(pieces) < 2 {
		t.Fatalf("expected multiple pieces, got %d", len(pieces))
	}
	if strings.Join(pieces, "") != s {
		t.Error("2048-budget reconstruction lost data")
	}
	for _, p := range pieces {
		if !utf8.ValidString(p) {
			t.Errorf("2048-budget piece corrupted: %q", p)
		}
	}
}

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
	if out.content != "done" {
		t.Fatalf("loop content = %q; want done (loop must survive a hung tool)", out.content)
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
	agg := newTurnAgg()
	call := &toolCallAgg{id: "call_1", name: "read_file"}
	call.args.WriteString("{}")
	agg.calls[0] = call
	agg.order = []int{0}

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
		t.Errorf("read-only names = %q; want read_file", tsRead.names)
	}
}
