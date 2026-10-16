package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// M4 item 4: outbound request body cap
// ---------------------------------------------------------------------------

// A tool loop grows the message history every turn, so a pathological or
// prompt-injected conversation could otherwise forward an ever-larger body to
// the provider. The outbound body is capped at maxOutboundBodyBytes (the same
// 32 MB as the inbound cap): an over-cap body fails loudly with a clear error
// and the provider is NEVER contacted, so nothing is silently truncated.
func TestUpstreamTurn_RejectsOverCapOutboundBody(t *testing.T) {
	contacted := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted = true
		jsonOKTurn(w)
	}))
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
		target:   target,
		tools:    tools,
		redact:   reg.Redactor(),
		reqBody:  map[string]any{"model": "tool-model"},
		messages: []any{map[string]any{"role": "user", "content": strings.Repeat("a", maxOutboundBodyBytes)}},
		client:   toolLoopHTTPClient,
	}

	_, err = lp.upstreamTurn(context.Background(), lp.messages)
	if err == nil {
		t.Fatal("an over-cap outbound body must fail loudly")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("err = %v; want the cap breach named", err)
	}
	if contacted {
		t.Error("the provider must not be contacted for an over-cap outbound body (never silently truncated)")
	}
}

// The negative control: a clean, well-formed request is well under the cap and
// reaches the provider unchanged.
func TestUpstreamTurn_UnderCapBodyReachesProvider(t *testing.T) {
	contacted := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted = true
		jsonOKTurn(w)
	}))
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
		target:   target,
		tools:    tools,
		redact:   reg.Redactor(),
		reqBody:  map[string]any{"model": "tool-model"},
		messages: []any{map[string]any{"role": "user", "content": "a normal, small prompt"}},
		client:   toolLoopHTTPClient,
	}

	out, err := lp.upstreamTurn(context.Background(), lp.messages)
	if err != nil {
		t.Fatalf("a clean request must succeed: %v", err)
	}
	if out == nil || out.Content() != "ok" {
		t.Errorf("turn = %+v; want the folded 'ok'", out)
	}
	if !contacted {
		t.Error("the provider must be contacted for a clean, under-cap request")
	}
}

// ---------------------------------------------------------------------------
// M4 item 2: cross-turn usage sum
// ---------------------------------------------------------------------------

// Both turns of a tool-augmented generation report usage; the surfaced usage
// must be the cross-turn cumulative sum (prompt/completion/total), not just the
// final turn's. Verified on both the streaming and the non-streaming surface.
func TestToolLoop_CrossTurnUsageSummed(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "stream"
		if !stream {
			name = "non-stream"
		}
		t.Run(name, func(t *testing.T) {
			usage := crossTurnUsage(t, stream)
			if usage == nil {
				t.Fatalf("%s: usage must be present", name)
			}
			want := map[string]float64{
				"prompt_tokens":     30, // 10 + 20
				"completion_tokens": 13, // 5 + 8
				"total_tokens":      43, // 15 + 28
			}
			for k, w := range want {
				got, ok := usage[k].(float64)
				if !ok {
					t.Errorf("%s: usage[%q] missing or not a number: %v", name, k, usage[k])
					continue
				}
				if got != w {
					t.Errorf("%s: usage[%q] = %v; want the cross-turn sum %v", name, k, got, w)
				}
			}
		})
	}
}

// crossTurnUsage drives a two-turn tool loop (a tool turn reporting usage, then
// a final turn reporting usage) through a fresh bridge and returns the usage
// block agy receives, on the streaming or non-streaming surface.
func crossTurnUsage(t *testing.T, stream bool) map[string]any {
	t.Helper()
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_u","type":"function","function":{"name":"list_dir","arguments":"{}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		),
		sseTurn(
			`{"choices":[{"index":0,"delta":{"content":"done"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}`,
		),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	body := `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"list"}]}`
	if !stream {
		body = `{"model":"tool-model","stream":false,"messages":[{"role":"user","content":"list"}]}`
	}
	_, _, raw := postChat(t, server.URL, body)

	if stream {
		_, usage := sseLastMeta(t, raw)
		return usage
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("non-stream response is not JSON: %v", err)
	}
	usage, _ := parsed["usage"].(map[string]any)
	return usage
}

// A single-turn request keeps the provider's own usage block verbatim (no
// phantom zero fields are injected): summing one turn is that turn.
func TestToolLoop_SingleTurnUsageShapePreserved(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)
	up := &scriptedUpstream{turns: []turnFunc{
		jsonTurn(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"total_tokens":9}}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	_, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	usage, _ := parsed["usage"].(map[string]any)
	if usage == nil {
		t.Fatal("usage must be present")
	}
	if usage["total_tokens"] != float64(9) {
		t.Errorf("total_tokens = %v; want 9", usage["total_tokens"])
	}
	// The per-turn shape is preserved: only the field the provider sent is present.
	if _, ok := usage["prompt_tokens"]; ok {
		t.Errorf("prompt_tokens must not be invented when the provider sent none: %v", usage)
	}
}

// ---------------------------------------------------------------------------
// M4 item 1: incremental SSE streaming during tool turns
// ---------------------------------------------------------------------------

// The tool loop must stream a tool turn's output to the client as the loop
// progresses, not buffer the whole loop and emit only at the end. The upstream
// answers turn 0 (a tool call) immediately, then BLOCKS turn 1 until the test
// confirms the tool turn's reasoning already reached the client. The old
// buffered loop would block inside run() waiting for turn 1 and never emit the
// reasoning, so this test times out on the pre-M4 behavior.
func TestToolLoop_StreamsReasoningDuringToolTurns(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)

	proceed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(bodyBytes, &body)
		hasToolResult := false
		if msgs, ok := body["messages"].([]any); ok {
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok && mm["role"] == "tool" {
					hasToolResult = true
					break
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if !hasToolResult {
			// Turn 0: reasoning plus a tool call.
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"TOOLTURN-REASONING\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_s\",\"type\":\"function\",\"function\":{\"name\":\"list_dir\",\"arguments\":\"{}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			fl.Flush()
			return
		}
		// Turn 1: block until the test proves the tool-turn reasoning streamed.
		<-proceed
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"final-answer\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"list"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testSessionKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var (
		mu           sync.Mutex
		lines        []string
		reasoningHit = make(chan struct{})
		readerDone   = make(chan struct{})
	)
	go func() {
		defer close(readerDone)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
			if strings.Contains(line, "TOOLTURN-REASONING") {
				select {
				case <-reasoningHit:
				default:
					close(reasoningHit)
				}
			}
		}
	}()

	select {
	case <-reasoningHit:
		// The tool turn's reasoning reached the client while turn 1 was still
		// blocked: the loop streams incrementally.
	case <-time.After(5 * time.Second):
		close(proceed) // let turn 1 finish so the server can wind down
		t.Fatal("tool-turn reasoning was not streamed to the client before the loop finished")
	}
	close(proceed)
	<-readerDone

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "final-answer") {
		t.Errorf("final answer missing from the stream:\n%s", joined)
	}
	if !strings.HasSuffix(strings.TrimSpace(joined), "data: [DONE]") {
		t.Errorf("stream must end with [DONE]:\n%s", joined)
	}
}

// A streaming tool loop that never reaches a final-answer turn (every turn is a
// tool call, then iteration exhaustion) still ends with the apology text,
// finish_reason "stop" and [DONE] (R1-8), and never leaks the upstream's raw
// reasoning-less tool turns as content.
func TestToolLoop_StreamFallbackStillClosesFraming(t *testing.T) {
	ws := t.TempDir()
	t.Setenv(envBridgeWorkspace, ws)
	up := &scriptedUpstream{turns: []turnFunc{
		sseTurn(`{"choices":[{"index":0,"delta":{"reasoning_content":"r"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"list_dir","arguments":"{}"}}]}}]}`),
	}}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()

	reg := toolsTestRegistry(t, upstream.URL)
	server := startToolsBridge(t, reg, BridgeToolsConfig{Enabled: true, Tools: []string{"read"}})

	status, _, raw := postChat(t, server.URL, `{"model":"tool-model","stream":true,"messages":[{"role":"user","content":"loop"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if got := sseContents(t, raw); got != toolLoopApology {
		t.Errorf("exhausted streaming loop content = %q; want the fixed apology", got)
	}
	fr, _ := sseLastMeta(t, raw)
	if fr != "stop" {
		t.Errorf("finish_reason = %q; want stop", fr)
	}
	events := parseSSEData(t, raw)
	if events[len(events)-1] != "[DONE]" {
		t.Errorf("stream must end with [DONE], got %q", events[len(events)-1])
	}
}
