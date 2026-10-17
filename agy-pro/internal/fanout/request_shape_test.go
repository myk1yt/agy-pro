package fanout

// Request-shape pins for the KV-cache conformance axes (DeepSeek context-caching
// benchmark).
//
// Part 1 (cache accounting): DeepSeek-native providers report
// prompt_cache_hit_tokens / prompt_cache_miss_tokens at the TOP LEVEL of the
// usage object and carry no prompt_tokens_details sub-object, so the
// OpenAI-only reader counted every DeepSeek-native call as a full cache miss.
// These pins hold the native parse, the two-shape normalization into one
// internal count, and the end-to-end accumulation into summary.json.
//
// Part 2 (max-turns synthesis): the synthesis call after max turns used to
// pass a nil tool block while every loop turn passed effectiveTools, which
// made the seat's largest request a guaranteed full cache miss.
//
// Part 3 (request construction): the four invariants the cache hit stands on -
// system message first, byte-stable tool order across turns, a volatile-free
// prefix, and a byte-identical preamble shared across the seats of one run.
//
// Every pin in this file is mutation-verified: breaking the invariant in
// client.go / worker.go makes the matching test fail (see the coder report).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// reqShapeTask / reqShapePrompt are the shared run config every pin in this
// file builds its seats from: the "review" preset and a prompt that carries no
// digit and no path, so no volatile-content pattern can match it by accident.
const (
	reqShapeTask   = "review"
	reqShapePrompt = "Audit the fanout request construction for cache stability"
	// reqShapeFindings keeps every stub seat non-thin, so a seat succeeds on
	// its first turn and never fails over into a second provider (which would
	// multiply the captured requests and blur per-seat attribution).
	reqShapeFindings = "## Findings\n- **[HIGH]** Request shape drift (Location: worker.go:1)\n"
	// reqShapeTargetFile is the file the stub tool calls read.
	reqShapeTargetFile = "target.go"
)

// capturedChatRequest is one chat request exactly as the worker put it on the
// wire: the decoded messages plus the RAW bytes of the tools and tool_choice
// fields, so a "byte-identical" comparison sees the provider's view of the
// request rather than a re-marshalled copy.
type capturedChatRequest struct {
	Model      string
	Messages   []ChatMessage
	SystemMsg  string
	FirstUser  string
	ToolsJSON  string
	ToolChoice string
}

// chatRecorder is a stub chat-completions server that records every request it
// receives (raw field bytes included) and answers from a scripted responder.
// Seats run concurrently, so both the record and the read hold the mutex.
type chatRecorder struct {
	respond func(call int, req capturedChatRequest) ChatResponse

	mu       sync.Mutex
	requests []capturedChatRequest
}

func (r *chatRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var wire struct {
		Model      string          `json:"model"`
		Messages   []ChatMessage   `json:"messages"`
		Tools      json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}

	captured := capturedChatRequest{
		Model:      wire.Model,
		Messages:   wire.Messages,
		ToolsJSON:  string(wire.Tools),
		ToolChoice: string(wire.ToolChoice),
	}
	// Role-based, not positional: the volatile-free pin must inspect the REAL
	// system message and the REAL first user message whatever their index.
	for _, m := range wire.Messages {
		switch m.Role {
		case "system":
			if captured.SystemMsg == "" {
				captured.SystemMsg = m.Content
			}
		case "user":
			if captured.FirstUser == "" {
				captured.FirstUser = m.Content
			}
		}
	}

	r.mu.Lock()
	r.requests = append(r.requests, captured)
	call := len(r.requests)
	respond := r.respond
	r.mu.Unlock()

	if respond == nil {
		respond = requestShapeAnswer
	}
	resp := respond(call, captured)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// snapshot returns a copy of the requests captured so far.
func (r *chatRecorder) snapshot() []capturedChatRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedChatRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// requestShapeAnswer is the default single-turn responder: a findings body, so
// the seat succeeds without a tool call and without a failover.
func requestShapeAnswer(call int, req capturedChatRequest) ChatResponse {
	return ChatResponse{Choices: []ChatChoice{{
		Index:        0,
		FinishReason: "stop",
		Message:      ChatMessage{Role: "assistant", Content: reqShapeFindings},
	}}}
}

// requestShapeToolCall answers with one read_file tool call, the multi-turn
// script every turn-counted pin needs.
func requestShapeToolCall(call int) ChatResponse {
	return ChatResponse{Choices: []ChatChoice{{
		Index:        0,
		FinishReason: "tool_calls",
		Message: ChatMessage{
			Role: "assistant",
			ToolCalls: []ChatToolCall{{ID: fmt.Sprintf("call-%d", call), Type: "function",
				Function: ChatFunctionCall{Name: "read_file", Arguments: `{"path":"` + reqShapeTargetFile + `"}`}}},
		},
	}}}
}

// requestShapeRunResult is one shared-config run: the recorded requests, the
// reconciled summary and the paths the pins need to name volatile values.
type requestShapeRunResult struct {
	rec     *chatRecorder
	summary *SummaryReport
	outDir  string
	root    string
}

// requestShapeRun stands up TWO tool-capable provider seats on one stub server
// (two providers, two model ids, same task / prompt / root / tool set) and runs
// the real engine, so the captured requests are the ones production sends.
func requestShapeRun(t *testing.T, respond func(call int, req capturedChatRequest) ChatResponse) *requestShapeRunResult {
	t.Helper()
	rec := &chatRecorder{respond: respond}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, reqShapeTargetFile), []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "REQ_SHAPE_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "REQ_SHAPE_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("REQ_SHAPE_KEY_A", "key-a")
	t.Setenv("REQ_SHAPE_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:   reqShapeTask,
		Prompt: reqShapePrompt,
		Root:   dir,
		OutDir: outDir,
		Seats: []SeatSpec{
			{Kind: SeatKindProvider, Raw: "prov-a/model-a", ModelID: "model-a"},
			{Kind: SeatKindProvider, Raw: "prov-b/model-b", ModelID: "model-b"},
		},
		RegistryPath: regPath,
		MaxTurns:     5,
		TimeoutSec:   30,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	return &requestShapeRunResult{rec: rec, summary: summary, outDir: outDir, root: dir}
}

// requestShapeWorker builds one tool-capable provider seat whose chat endpoint
// is serverURL, carrying the shared preset, prompt and tool set. It drives
// SeatWorker directly (no engine) so the turn count is exact.
func requestShapeWorker(serverURL, root string, maxTurns int) *SeatWorker {
	return &SeatWorker{
		SeatID: 1,
		InitialTarget: providers.Target{
			Provider: providers.Provider{Name: "prov-a", BaseURL: serverURL},
			Model:    providers.Model{ID: "model-a", Family: "family-a", Capabilities: providers.Capabilities{SupportsTools: true}},
		},
		Registry:             &providers.Registry{},
		Preset:               GetTaskPreset(reqShapeTask),
		UserPrompt:           BuildUserPrompt(reqShapeTask, reqShapePrompt),
		Runner:               agenttools.NewRunner(root, false, false, nil),
		Client:               NewClient(),
		MaxTurns:             maxTurns,
		Tools:                agenttools.BuildOpenAITools(false, false),
		AllowToolsForSession: true,
	}
}

// writeRequestShapeRoot creates the workspace root one seat's tool calls read.
func writeRequestShapeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, reqShapeTargetFile), []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	return root
}

// --- Part 1: cache accounting ------------------------------------------------

// The DeepSeek-native wire shape: TOP-LEVEL prompt_cache_hit_tokens /
// prompt_cache_miss_tokens and NO prompt_tokens_details sub-object. The parsed
// cache-token count must equal the native hit value, and the miss counter must
// land in its own field instead of leaking into the hit count.
func TestChatUsageParsesDeepSeekNativeCacheFields(t *testing.T) {
	const nativeBody = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,` +
		`"prompt_cache_hit_tokens":960,"prompt_cache_miss_tokens":40}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, nativeBody)
	}))
	t.Cleanup(server.Close)

	target := providers.Target{
		Provider: providers.Provider{Name: "deepseek-prov", BaseURL: server.URL},
		Model:    providers.Model{ID: "deepseek-chat"},
	}
	resp, err := NewClient().Complete(context.Background(), target, []ChatMessage{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage = nil; want the native usage block")
	}
	if resp.Usage.PromptTokensDetails != nil {
		t.Errorf("prompt_tokens_details = %+v; want nil (the native shape has no nested sub-object)", resp.Usage.PromptTokensDetails)
	}
	if resp.Usage.PromptCacheHitTokens == nil || *resp.Usage.PromptCacheHitTokens != 960 {
		t.Errorf("prompt_cache_hit_tokens = %v; want 960", resp.Usage.PromptCacheHitTokens)
	}
	if resp.Usage.PromptCacheMissTokens == nil || *resp.Usage.PromptCacheMissTokens != 40 {
		t.Errorf("prompt_cache_miss_tokens = %v; want 40", resp.Usage.PromptCacheMissTokens)
	}
	if c := resp.Usage.cachedTokens(); c == nil || *c != 960 {
		t.Errorf("cachedTokens() = %v; want the native hit count 960", c)
	}
}

// A native hit of ZERO is a reported zero, not a missing number: the count must
// come back as a pointer to 0, because a zero and an absent value mean
// different things once they become a ratio.
func TestChatUsageNativeZeroHitIsNotAMissingNumber(t *testing.T) {
	const zeroHitBody = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, zeroHitBody)
	}))
	t.Cleanup(server.Close)

	target := providers.Target{
		Provider: providers.Provider{Name: "deepseek-prov", BaseURL: server.URL},
		Model:    providers.Model{ID: "deepseek-chat"},
	}
	resp, err := NewClient().Complete(context.Background(), target, []ChatMessage{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	c := resp.Usage.cachedTokens()
	if c == nil {
		t.Fatal("cachedTokens() = nil; want a pointer to 0 (the provider reported a real zero hit)")
	}
	if *c != 0 {
		t.Errorf("cachedTokens() = %d; want 0", *c)
	}
}

// cachedTokens() normalizes BOTH cache shapes into ONE internal count, with the
// native hit taking priority when a provider reports both, and nil when a
// provider reports neither.
func TestChatUsageNormalizesBothCacheShapesToOneCount(t *testing.T) {
	nativeHit, nestedHit := int64(960), int64(512)
	zero := int64(0)
	cases := []struct {
		name  string
		usage string
		want  *int64
	}{
		{
			name:  "native only",
			usage: `{"prompt_cache_hit_tokens":960,"prompt_cache_miss_tokens":40}`,
			want:  &nativeHit,
		},
		{
			name:  "openai nested only",
			usage: `{"prompt_tokens_details":{"cached_tokens":512}}`,
			want:  &nestedHit,
		},
		{
			name:  "both shapes, native wins",
			usage: `{"prompt_cache_hit_tokens":960,"prompt_tokens_details":{"cached_tokens":512}}`,
			want:  &nativeHit,
		},
		{
			name:  "native zero beats a nested count",
			usage: `{"prompt_cache_hit_tokens":0,"prompt_tokens_details":{"cached_tokens":512}}`,
			want:  &zero,
		},
		{
			name:  "neither shape",
			usage: `{"prompt_tokens":10,"completion_tokens":2}`,
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp ChatResponse
			body := `{"id":"x","choices":[],"usage":` + tc.usage + `}`
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("unmarshal %s: %v", body, err)
			}
			got := resp.Usage.cachedTokens()
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("cachedTokens() = %d; want nil (no cache shape reported)", *got)
			case tc.want != nil && got == nil:
				t.Errorf("cachedTokens() = nil; want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("cachedTokens() = %d; want %d", *got, *tc.want)
			}
		})
	}

	var nilUsage *ChatUsage
	if got := nilUsage.cachedTokens(); got != nil {
		t.Errorf("nil usage cachedTokens() = %v; want nil", got)
	}
}

// Dual-shape precedence pin over the real parse path: a provider that reports
// BOTH the DeepSeek-native top-level prompt_cache_hit_tokens and the OpenAI
// nested prompt_tokens_details.cached_tokens, with DIFFERENT values (960 vs
// 512), must resolve cachedTokens() to the NATIVE hit. Flipping cachedTokens()
// to nested-first fails here, and it is the fanout half of the one precedence
// rule both cache readers share.
func TestChatUsageDualShapeNativeHitWinsOverNested(t *testing.T) {
	const dualBody = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,` +
		`"prompt_cache_hit_tokens":960,"prompt_cache_miss_tokens":40,` +
		`"prompt_tokens_details":{"cached_tokens":512}}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, dualBody)
	}))
	t.Cleanup(server.Close)

	target := providers.Target{
		Provider: providers.Provider{Name: "dual-prov", BaseURL: server.URL},
		Model:    providers.Model{ID: "dual-chat"},
	}
	resp, err := NewClient().Complete(context.Background(), target, []ChatMessage{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	c := resp.Usage.cachedTokens()
	if c == nil || *c != 960 {
		t.Errorf("cachedTokens() = %v; want the native hit 960 to win over the nested 512", c)
	}
}

// End to end: a provider that reports ONLY the native shape must still surface
// its cache tokens in the seat totals, the run totals and summary.json, and the
// summary output shape must not gain the native wire field names.
func TestProviderSeatUsageAccumulatesDeepSeekNativeCacheCount(t *testing.T) {
	summary, outDir := providerUsageRun(t, func(call int) ChatResponse {
		usage := &ChatUsage{
			PromptTokens:          int64Ptr(1000),
			CompletionTokens:      int64Ptr(120),
			PromptCacheHitTokens:  int64Ptr(900),
			PromptCacheMissTokens: int64Ptr(100),
		}
		if call == 1 {
			return ChatResponse{
				Choices: []ChatChoice{{Index: 0, FinishReason: "tool_calls", Message: ChatMessage{
					Role: "assistant",
					ToolCalls: []ChatToolCall{{ID: "c1", Type: "function",
						Function: ChatFunctionCall{Name: "read_file", Arguments: `{"path":"target.go"}`}}},
				}}},
				Usage: usage,
			}
		}
		return ChatResponse{
			Choices: []ChatChoice{{Index: 0, FinishReason: "stop",
				Message: ChatMessage{Role: "assistant", Content: realBody}}},
			Usage: usage,
		}
	})

	if len(summary.Seats) != 1 {
		t.Fatalf("seats = %d; want 1", len(summary.Seats))
	}
	seat := summary.Seats[0]
	if seat.Usage == nil || seat.Usage.CachedTokens == nil {
		t.Fatalf("seat usage = %+v; want the accumulated native cache count", seat.Usage)
	}
	if *seat.Usage.CachedTokens != 1800 {
		t.Errorf("seat cached_tokens = %d; want 1800 (two turns x 900 native hits)", *seat.Usage.CachedTokens)
	}
	if summary.Usage == nil || summary.Usage.CacheHitRatio == nil {
		t.Fatalf("run usage = %+v; want a cache_hit_ratio", summary.Usage)
	}
	if *summary.Usage.CacheHitRatio != 0.9 {
		t.Errorf("cache_hit_ratio = %v; want 0.9 (1800/2000)", *summary.Usage.CacheHitRatio)
	}

	sumRaw := readOutFile(t, outDir, "summary.json")
	if !strings.Contains(sumRaw, `"cached_tokens": 1800`) {
		t.Errorf("summary.json missing the accumulated native cache count:\n%s", sumRaw)
	}
	for _, unwanted := range []string{"prompt_cache_hit_tokens", "prompt_cache_miss_tokens"} {
		if strings.Contains(sumRaw, unwanted) {
			t.Errorf("summary.json must keep its output shape and not carry %s:\n%s", unwanted, sumRaw)
		}
	}
}

// FIX 5: a negative token/cache count is treated as absent (never accumulated)
// and a summed count saturates at math.MaxInt64 instead of wrapping negative on
// int64 overflow. The missing-vs-zero semantics are preserved: a nil/absent or
// negative count leaves the total nil, never a fabricated zero.
func TestUsageAddRejectsNegativeAndSaturatesOnOverflow(t *testing.T) {
	// A negative native hit with no nested count is ignored: cached stays nil.
	negNative := &usageTotals{}
	negNative.add(&ChatResponse{Usage: &ChatUsage{PromptCacheHitTokens: int64Ptr(-5)}})
	if negNative.cached != nil {
		t.Errorf("cached = %d; want nil (a negative native hit is not accumulated)", *negNative.cached)
	}
	// A negative nested count with no native hit is ignored too.
	negNested := &usageTotals{}
	negNested.add(&ChatResponse{Usage: &ChatUsage{
		PromptTokensDetails: &ChatPromptTokensDetails{CachedTokens: int64Ptr(-7)},
	}})
	if negNested.cached != nil {
		t.Errorf("cached = %d; want nil (a negative nested count is not accumulated)", *negNested.cached)
	}
	// A negative prompt count is not accumulated either.
	negPrompt := &usageTotals{}
	negPrompt.add(&ChatResponse{Usage: &ChatUsage{PromptTokens: int64Ptr(-1)}})
	if negPrompt.prompt != nil {
		t.Errorf("prompt = %d; want nil (a negative prompt count is not accumulated)", *negPrompt.prompt)
	}
	// A negative add does not drag a prior valid total down.
	mixed := &usageTotals{}
	mixed.add(&ChatResponse{Usage: &ChatUsage{PromptTokens: int64Ptr(100)}})
	mixed.add(&ChatResponse{Usage: &ChatUsage{PromptTokens: int64Ptr(-40)}})
	if mixed.prompt == nil || *mixed.prompt != 100 {
		t.Errorf("prompt = %v; want 100 (the negative turn contributed nothing)", mixed.prompt)
	}
	// Overflow saturates at math.MaxInt64 instead of wrapping negative.
	over := &usageTotals{}
	big := int64Ptr(math.MaxInt64)
	over.add(&ChatResponse{Usage: &ChatUsage{PromptTokens: big}})
	over.add(&ChatResponse{Usage: &ChatUsage{PromptTokens: big}})
	if over.prompt == nil {
		t.Fatal("prompt = nil; want a saturated total")
	}
	if *over.prompt < 0 {
		t.Errorf("prompt = %d; want a non-negative saturated value (no int64 wrap)", *over.prompt)
	}
	if *over.prompt != math.MaxInt64 {
		t.Errorf("prompt = %d; want saturation at math.MaxInt64", *over.prompt)
	}
}

// --- Part 2: max-turns synthesis ---------------------------------------------

// The synthesis call after max turns is the seat's largest request. It must
// carry the SAME tools block (and the same tool_choice wiring) as the loop turn
// that precedes it, byte for byte.
func TestMaxTurnsSynthesisCarriesTheToolBlock(t *testing.T) {
	root := writeRequestShapeRoot(t)
	rec := &chatRecorder{respond: func(call int, req capturedChatRequest) ChatResponse {
		if call == 1 {
			return requestShapeToolCall(call)
		}
		return requestShapeAnswer(call, req)
	}}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	// MaxTurns=1: one loop turn (tool call), then the synthesis request.
	w := requestShapeWorker(server.URL, root, 1)
	res := w.Run(context.Background())
	if res.Status != "success" {
		t.Fatalf("seat status = %q; want success (err %q)", res.Status, res.Error)
	}

	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("captured %d requests; want 2 (one loop turn + the synthesis)", len(reqs))
	}
	loopTurn, synthesis := reqs[0], reqs[1]
	if loopTurn.ToolsJSON == "" {
		t.Fatal("the loop turn carried no tools array; the pin would be vacuous")
	}
	if synthesis.ToolsJSON == "" {
		t.Fatalf("the synthesis request carried NO tools array: a nil tool block makes the seat's largest request a guaranteed cache miss")
	}
	if synthesis.ToolsJSON != loopTurn.ToolsJSON {
		t.Errorf("synthesis tools = %s;\nwant byte-identical to the preceding turn = %s", synthesis.ToolsJSON, loopTurn.ToolsJSON)
	}
	if synthesis.ToolChoice != loopTurn.ToolChoice {
		t.Errorf("synthesis tool_choice = %s; want %s (the loop turn's wiring)", synthesis.ToolChoice, loopTurn.ToolChoice)
	}
	if synthesis.ToolChoice != `"auto"` {
		t.Errorf("synthesis tool_choice = %s; want \"auto\"", synthesis.ToolChoice)
	}

	// The captured second request really is the synthesis: its last message is
	// the synthesis instruction and the full tool history rode along.
	last := synthesis.Messages[len(synthesis.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Maximum turns reached") {
		t.Fatalf("last synthesis message = %+v; want the max-turns instruction", last)
	}
	if len(synthesis.Messages) != 5 {
		t.Errorf("synthesis carried %d messages; want 5 (system, user, assistant tool call, tool result, instruction)", len(synthesis.Messages))
	}
}

// A2 lets the synthesis answer with a tool call (empty content). The worker must
// retry the synthesis EXACTLY ONCE with tools=nil to force a textual answer and
// return that retried body, never passing the empty tool-call answer off as a
// thin success. The FIRST synthesis still carries the tools block (A2 invariant
// preserved); only the retry drops it.
func TestMaxTurnsSynthesisRetriesTextuallyOnToolCallAnswer(t *testing.T) {
	root := writeRequestShapeRoot(t)
	rec := &chatRecorder{respond: func(call int, req capturedChatRequest) ChatResponse {
		switch call {
		case 1:
			return requestShapeToolCall(call) // loop turn: one tool call
		case 2:
			// First synthesis: the model answers with a tool call, empty content.
			return ChatResponse{Choices: []ChatChoice{{Index: 0, FinishReason: "tool_calls",
				Message: ChatMessage{Role: "assistant",
					ToolCalls: []ChatToolCall{{ID: "syn-1", Type: "function",
						Function: ChatFunctionCall{Name: "read_file", Arguments: `{"path":"` + reqShapeTargetFile + `"}`}}}}}}}
		default:
			// The tools-nil retry: a real findings body.
			return requestShapeAnswer(call, req)
		}
	}}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	// MaxTurns=1: one loop turn (tool call), then the synthesis, then the retry.
	w := requestShapeWorker(server.URL, root, 1)
	res := w.Run(context.Background())
	if res.Status != "success" {
		t.Fatalf("seat status = %q; want success (err %q)", res.Status, res.Error)
	}
	if res.Content != reqShapeFindings {
		t.Errorf("content = %q; want the retried findings body %q", res.Content, reqShapeFindings)
	}

	reqs := rec.snapshot()
	if len(reqs) != 3 {
		t.Fatalf("captured %d requests; want 3 (loop turn + tool-call synthesis + textual retry)", len(reqs))
	}
	// The first synthesis still carries the tools block (A2 invariant preserved).
	if reqs[1].ToolsJSON == "" {
		t.Error("the first synthesis carried no tools block; the A2 invariant must hold")
	}
	// The retry dropped the tools block and the tool_choice, exactly once.
	retry := reqs[2]
	if retry.ToolsJSON != "" {
		t.Errorf("the retry carried a tools block %s; want empty (tools=nil forces a textual answer)", retry.ToolsJSON)
	}
	if retry.ToolChoice != "" {
		t.Errorf("the retry carried tool_choice %s; want empty", retry.ToolChoice)
	}
}

// The synthesis retry path must accumulate the usage of BOTH synthesis turns:
// the first synthesis (the tool-call answer, empty content) via usage.add at
// worker.go:388 and the tools-nil retry (the textual answer) via usage.add at
// worker.go:404. The loop turn that precedes them carries no usage here, so the
// seat's accumulated prompt/completion totals are exactly the first synthesis's
// plus the retry's. Deleting the retry's usage.add call makes the seat
// under-report and fails this pin.
func TestMaxTurnsSynthesisRetryAccumulatesBothTurnsUsage(t *testing.T) {
	root := writeRequestShapeRoot(t)
	firstUsage := &ChatUsage{
		PromptTokens:     int64Ptr(300),
		CompletionTokens: int64Ptr(30),
	}
	retryUsage := &ChatUsage{
		PromptTokens:     int64Ptr(700),
		CompletionTokens: int64Ptr(70),
	}
	rec := &chatRecorder{respond: func(call int, req capturedChatRequest) ChatResponse {
		switch call {
		case 1:
			return requestShapeToolCall(call) // loop turn: one tool call, no usage
		case 2:
			// First synthesis: answers with a tool call, empty content, and
			// carries its own usage block.
			return ChatResponse{Choices: []ChatChoice{{Index: 0, FinishReason: "tool_calls",
				Message: ChatMessage{Role: "assistant",
					ToolCalls: []ChatToolCall{{ID: "syn-1", Type: "function",
						Function: ChatFunctionCall{Name: "read_file", Arguments: `{"path":"` + reqShapeTargetFile + `"}`}}}}}},
				Usage: firstUsage}
		default:
			// The tools-nil retry: a real findings body, carrying its own usage.
			resp := requestShapeAnswer(call, req)
			resp.Usage = retryUsage
			return resp
		}
	}}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	// MaxTurns=1: one loop turn (tool call), then the synthesis, then the retry.
	w := requestShapeWorker(server.URL, root, 1)
	res := w.Run(context.Background())
	if res.Status != "success" {
		t.Fatalf("seat status = %q; want success (err %q)", res.Status, res.Error)
	}
	if got := len(rec.snapshot()); got != 3 {
		t.Fatalf("captured %d requests; want 3 (loop turn + tool-call synthesis + textual retry)", got)
	}

	if res.Usage == nil {
		t.Fatal("seat usage = nil; want the accumulated synthesis + retry usage")
	}
	if res.Usage.PromptTokens == nil {
		t.Fatalf("seat prompt_tokens = nil; want 1000 (first synthesis 300 + retry 700)")
	}
	if got := *res.Usage.PromptTokens; got != 1000 {
		t.Errorf("seat prompt_tokens = %d; want 1000 (first synthesis 300 + retry 700)", got)
	}
	if res.Usage.CompletionTokens == nil {
		t.Fatalf("seat completion_tokens = nil; want 100 (first synthesis 30 + retry 70)")
	}
	if got := *res.Usage.CompletionTokens; got != 100 {
		t.Errorf("seat completion_tokens = %d; want 100 (first synthesis 30 + retry 70)", got)
	}
}

// When BOTH synthesis attempts (the effectiveTools call and the tools-nil
// retry) return empty content, the seat must fail visibly with a descriptive
// error instead of passing the empty body off as a thin success.
func TestMaxTurnsSynthesisFailsWhenBothAttemptsEmpty(t *testing.T) {
	root := writeRequestShapeRoot(t)
	rec := &chatRecorder{respond: func(call int, req capturedChatRequest) ChatResponse {
		if call == 1 {
			return requestShapeToolCall(call) // loop turn: one tool call
		}
		// Synthesis and retry: empty content, no tool calls.
		return ChatResponse{Choices: []ChatChoice{{Index: 0, FinishReason: "stop",
			Message: ChatMessage{Role: "assistant", Content: ""}}}}
	}}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	w := requestShapeWorker(server.URL, root, 1)
	res := w.Run(context.Background())
	if res.Status != "failed" {
		t.Fatalf("seat status = %q; want failed (an empty synthesis must not pass as success)", res.Status)
	}
	if !strings.Contains(res.Error, "synthesis produced empty content") {
		t.Errorf("seat error = %q; want it to name the empty synthesis", res.Error)
	}
	// Both attempts were issued: loop turn + synthesis + retry, no more (no loop).
	if got := len(rec.snapshot()); got != 3 {
		t.Errorf("captured %d requests; want 3 (loop turn + two synthesis attempts)", got)
	}
}

// --- Part 3: request construction --------------------------------------------

// (a) system-first: the system message is messages[0] in every provider-seat
// request, and it is the task preset's system prompt.
func TestProviderSeatRequestPutsSystemMessageFirst(t *testing.T) {
	run := requestShapeRun(t, requestShapeAnswer)
	reqs := run.rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("captured %d requests; want exactly one per seat", len(reqs))
	}
	wantSystem := GetTaskPreset(reqShapeTask).SystemPrompt
	for i, r := range reqs {
		if len(r.Messages) < 2 {
			t.Fatalf("request %d (%s) carried %d messages; want at least system + user", i+1, r.Model, len(r.Messages))
		}
		if r.Messages[0].Role != "system" {
			t.Errorf("request %d (%s): messages[0].role = %q; want \"system\" (the cache prefix must open with the system message)", i+1, r.Model, r.Messages[0].Role)
		}
		if r.Messages[0].Content != wantSystem {
			t.Errorf("request %d (%s): messages[0] is not the %q preset system prompt:\n%s", i+1, r.Model, reqShapeTask, r.Messages[0].Content)
		}
		if r.Messages[1].Role != "user" {
			t.Errorf("request %d (%s): messages[1].role = %q; want \"user\"", i+1, r.Model, r.Messages[1].Role)
		}
	}
}

// (b) tool-order stability: the tool/function definitions are byte-stable in
// order across turns, and the order is the shared tool set's order.
func TestToolOrderIsByteStableAcrossTurns(t *testing.T) {
	root := writeRequestShapeRoot(t)
	rec := &chatRecorder{respond: func(call int, req capturedChatRequest) ChatResponse {
		if call <= 2 {
			return requestShapeToolCall(call)
		}
		return requestShapeAnswer(call, req)
	}}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	w := requestShapeWorker(server.URL, root, 3)
	res := w.Run(context.Background())
	if res.Status != "success" {
		t.Fatalf("seat status = %q; want success (err %q)", res.Status, res.Error)
	}

	reqs := rec.snapshot()
	if len(reqs) != 3 {
		t.Fatalf("captured %d requests; want 3 (two tool turns + the final answer)", len(reqs))
	}
	for i, r := range reqs {
		if r.ToolsJSON == "" {
			t.Fatalf("request %d carried no tools array; the pin would be vacuous", i+1)
		}
		if r.ToolsJSON != reqs[0].ToolsJSON {
			t.Errorf("request %d tools = %s;\nwant byte-identical to request 1 = %s", i+1, r.ToolsJSON, reqs[0].ToolsJSON)
		}
		if r.ToolChoice != `"auto"` {
			t.Errorf("request %d tool_choice = %s; want \"auto\"", i+1, r.ToolChoice)
		}
	}

	var defs []agenttools.ToolDef
	if err := json.Unmarshal([]byte(reqs[0].ToolsJSON), &defs); err != nil {
		t.Fatalf("decode tools array: %v", err)
	}
	var gotOrder []string
	for _, d := range defs {
		gotOrder = append(gotOrder, d.Function.Name)
	}
	wantOrder := []string{"read_file", "list_dir", "grep", "glob"}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("tool order = %v; want %v (the shared tool set's order)", gotOrder, wantOrder)
	}
}

// volatilePatterns are the shapes that must never reach a seat's system message
// or first user message: each one differs per run, per seat or per machine, so
// any of them in the prefix would change the cache key on every request. The
// matchers are deliberately broad so the detector has no blind spots: an
// uppercase-hex or base64 nonce, an epoch-seconds or RFC3339 timestamp, an
// "N seats" / "seat-N" phrasing and a short hostname must all be caught, not
// only the narrow lowercase-hex / whitespace spellings.
var volatilePatterns = []struct {
	name      string
	re        *regexp.Regexp
	needDigit bool
}{
	{name: "fanout run timestamp", re: regexp.MustCompile(`20\d{6}-\d{6}`)},
	{name: "RFC3339 timestamp", re: regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)},
	{name: "epoch-seconds timestamp", re: regexp.MustCompile(`\b1\d{9}\b`)},
	{name: "seat index", re: regexp.MustCompile(`(?i)seat[-\s]*\d+`)},
	{name: "seat count", re: regexp.MustCompile(`(?i)\b\d+\s+seats?\b`)},
	{name: "seat fraction", re: regexp.MustCompile(`\b\d+\s*/\s*\d+\b`)},
	{name: "uuid", re: regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)},
	// A nonce is any long (16+ char) hex/base64-ish token; needDigit keeps an
	// ordinary English word (no digit) from matching it.
	{name: "hex/base64 nonce", re: regexp.MustCompile(`[A-Za-z0-9+/]{16,}`), needDigit: true},
}

// volatileHits returns a human-readable description of every volatile value or
// shape found in text: first the concrete literals (run id, nonce, seat
// index/count, cwd, hostname, model id, timestamps), then the broadened regex
// shapes. An empty result means the text carries no volatile the engine can
// generate. The volatile-free pin asserts the real prefix is clean; the
// detector self-test injects one volatile of each shape and asserts this
// function fires, so the matchers can never go vacuous.
func volatileHits(text string, literals []string) []string {
	var hits []string
	for _, lit := range literals {
		if lit != "" && strings.Contains(text, lit) {
			hits = append(hits, fmt.Sprintf("volatile literal %q", lit))
		}
	}
	for _, vp := range volatilePatterns {
		for _, m := range vp.re.FindAllString(text, -1) {
			if vp.needDigit && !strings.ContainsAny(m, "0123456789") {
				continue
			}
			hits = append(hits, fmt.Sprintf("%s %q", vp.name, m))
			break // one hit per shape is enough to fail the pin
		}
	}
	return hits
}

// (c) volatile-free prefix: none of {timestamp, run ID, seat index, seat count,
// model name, cwd, hostname, nonce} appears in the system message or the first
// user message of any seat request. Both the concrete literals the engine can
// reach for and the broadened regex shapes are checked.
func TestSeatPreambleCarriesNoVolatileContent(t *testing.T) {
	run := requestShapeRun(t, requestShapeAnswer)
	reqs := run.rec.snapshot()
	if len(reqs) == 0 {
		t.Fatal("no requests captured")
	}

	// (i) Concrete volatile literals the engine can reach for: the run's model
	// ids and root, the live cwd and hostname (length-independent), plus a
	// representative value of each remaining shape (run id, hex/base64 nonce,
	// seat index/count string, epoch-seconds and RFC3339 timestamps). None may
	// be a substring of the cacheable prefix.
	literals := []string{
		"model-a", "model-b",
		run.root,
		"20261016-120000",                    // run id
		"a1b2c3d4e5f6a7b8c9d0e1f2",           // hex nonce
		"QWxhZGRpbjpvcGVuIHNlc2FtZQ",         // base64 nonce
		"seat 3", "seat-3", "3 seats", "3/5", // seat index/count
		"1729075200",           // epoch-seconds timestamp
		"2026-10-16T12:00:00Z", // RFC3339 timestamp
		"antigravity-build-01", // hostname
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		literals = append(literals, cwd)
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		literals = append(literals, host)
	}

	for i, r := range reqs {
		for field, text := range map[string]string{"system message": r.SystemMsg, "first user message": r.FirstUser} {
			if hits := volatileHits(text, literals); len(hits) > 0 {
				t.Errorf("request %d (%s) %s carries volatile content: %s\n%s", i+1, r.Model, field, strings.Join(hits, "; "), text)
			}
		}
	}
}

// The volatile detector must fire on every shape it claims to cover, or the
// volatile-free pin above goes vacuous. Each representative below is injected
// into a synthetic prefix and must be flagged; this is the mutation-verification
// for the broadened matchers (inject a volatile of each newly-covered shape,
// confirm the detector fires). The hostname literal is length-independent, so a
// 2-character hostname is caught too.
func TestVolatileDetectorCatchesEveryShape(t *testing.T) {
	cases := []struct {
		shape string
		msg   string
	}{
		{"run id", "prefix 20261016-120000 suffix"},
		{"hex nonce", "prefix a1b2c3d4e5f6a7b8c9d0e1f2 suffix"},
		{"uppercase-hex nonce", "prefix DEADBEEFCAFE1234567890AB suffix"},
		{"base64 nonce", "prefix QWxhZGRpbjpvcGVuIHNlc2FtZQ suffix"},
		{"seat index", "prefix seat 3 suffix"},
		{"seat dash index", "prefix seat-3 suffix"},
		{"seat count", "prefix 3 seats suffix"},
		{"seat fraction", "prefix 3/5 suffix"},
		{"epoch timestamp", "prefix 1729075200 suffix"},
		{"rfc3339 timestamp", "prefix 2026-10-16T12:00:00Z suffix"},
		{"uuid", "prefix 3f2504e0-4f89-11d3-9a0c-0305e82c3301 suffix"},
	}
	for _, tc := range cases {
		if hits := volatileHits(tc.msg, nil); len(hits) == 0 {
			t.Errorf("%s: volatileHits did not flag the injected value in %q", tc.shape, tc.msg)
		}
	}
	// The hostname literal is length-independent: even a 2-char hostname is
	// caught as a substring.
	if hits := volatileHits("prefix ab suffix", []string{"ab"}); len(hits) == 0 {
		t.Error("a short (2-char) hostname literal was not flagged")
	}
}

// (d) cross-seat byte-identity: two seats built from the same run config
// produce byte-identical preambles (system message, first user message and the
// shared tool block). Only the per-unit/per-seat body may vary.
func TestTwoSeatsFromOneRunConfigShareByteIdenticalPreamble(t *testing.T) {
	run := requestShapeRun(t, requestShapeAnswer)

	byModel := map[string][]capturedChatRequest{}
	for _, r := range run.rec.snapshot() {
		byModel[r.Model] = append(byModel[r.Model], r)
	}
	seatA, okA := byModel["model-a"]
	seatB, okB := byModel["model-b"]
	if !okA || len(seatA) == 0 || !okB || len(seatB) == 0 {
		t.Fatalf("want one request per seat model, got %v", func() []string {
			var keys []string
			for k, v := range byModel {
				keys = append(keys, fmt.Sprintf("%s=%d", k, len(v)))
			}
			return keys
		}())
	}

	first, second := seatA[0], seatB[0]
	if first.SystemMsg != second.SystemMsg {
		t.Errorf("system messages differ across seats:\nseat model-a: %q\nseat model-b: %q", first.SystemMsg, second.SystemMsg)
	}
	if first.FirstUser != second.FirstUser {
		t.Errorf("first user messages differ across seats:\nseat model-a: %q\nseat model-b: %q", first.FirstUser, second.FirstUser)
	}
	if first.ToolsJSON != second.ToolsJSON {
		t.Errorf("tool blocks differ across seats:\nseat model-a: %s\nseat model-b: %s", first.ToolsJSON, second.ToolsJSON)
	}
	// Non-empty guard: two seats that BOTH dropped the tools block would be
	// byte-identical (both "") and slip past the identity check above. Assert
	// the per-seat tools JSON is actually present, so an engine-glue mutation
	// that drops tools from both seats fails this pin.
	if first.ToolsJSON == "" || second.ToolsJSON == "" {
		t.Errorf("per-seat tools block is empty (seat model-a: %q, seat model-b: %q); the byte-identity pin is vacuous without it", first.ToolsJSON, second.ToolsJSON)
	}

	// The shared preamble is the preset + the user prompt, not a coincidence of
	// two empty strings.
	wantSystem := GetTaskPreset(reqShapeTask).SystemPrompt
	if first.SystemMsg != wantSystem {
		t.Errorf("system message = %q; want the %q preset system prompt", first.SystemMsg, reqShapeTask)
	}
	if first.FirstUser != reqShapePrompt {
		t.Errorf("first user message = %q; want %q", first.FirstUser, reqShapePrompt)
	}
}
