package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// Bridge-side tool execution loop (plan R1).
//
// agy's gateway client never sends `tools`/`tool_choice` and discards response
// `tool_calls`, so external providers only gain Gemini-parity tool actions when
// the bridge attaches tools itself, executes the returned tool calls with
// agenttools, re-feeds the results, and finally synthesizes a text-only
// response back to agy. The loop is stateless: the working message list is a
// per-request copy.

const (
	// toolLoopMaxIterations caps upstream turns per request (plan 8b R1 budgets).
	toolLoopMaxIterations = 8
	// toolLoopWallBudget is the hard wall clock for one whole loop.
	toolLoopWallBudget = 240 * time.Second
	// toolLoopTurnBudget is the per-upstream-turn timeout; each turn gets
	// min(toolLoopTurnBudget, remaining wall budget).
	toolLoopTurnBudget = 60 * time.Second
	// toolLoopToolBudget bounds each tool execution (F-04). run_command also
	// clamps its own timeout_seconds argument at the tool layer (R1-5); this
	// deadline covers every tool, including the ctx-free read/search ones.
	toolLoopToolBudget = 30 * time.Second
	// toolLoopMaxToolCallsPerTurn caps model-requested tool executions in one
	// upstream turn (security M-1): a hostile upstream flooding tool_calls can
	// no longer fan out unbounded local work. Calls beyond the cap get a
	// failure tool result instead of execution.
	toolLoopMaxToolCallsPerTurn = 16

	// maxOutboundBodyBytes caps the request body the bridge builds for the
	// provider (M4 item 4). It is the outbound counterpart to the inbound
	// openaiup.MaxBodyBytes cap the bridge applies before reading the request
	// (bridge.go), and it matches that 32 MB value so a clean, well-formed
	// request is never affected. A tool loop grows the message history on every
	// turn, so a pathological or prompt-injected conversation could otherwise
	// forward an ever-larger body and blast the provider (or exhaust memory).
	// A body past the cap fails loudly with a clear error; it is never silently
	// truncated. Enforced on both outbound paths: the reverse proxy
	// (handleChatCompletions) and every upstream turn (upstreamTurn).
	maxOutboundBodyBytes = openaiup.MaxBodyBytes
)

// toolLoopApology is the fixed fallback answer used when the loop ends without
// any non-empty text (panic, timeout, iteration exhaustion). A textless
// response makes the agy gateway client retry hundreds of times per minute, so
// every exit path must end with non-empty text + finish_reason "stop" + [DONE].
const toolLoopApology = "I'm sorry, but I could not finish the tool-assisted turn within the time or step limits. Please retry the request."

// envBridgeWorkspace is the bridge's own-process workspace root override. It is
// read here (never from the child agy env) and wins over the shim cwd (R1-3).
const envBridgeWorkspace = "AGY_PRO_WORKSPACE"

// bridgeToolTokenSet is the closed bridge-tools capability vocabulary
// (identical to fanout --tools tokens). Parsing is fail-closed.
var bridgeToolTokenSet = []string{"read", "search", "write", "exec"}

// toolNameTokens maps every agenttools tool to its bridge-tools token.
var toolNameTokens = map[string]string{
	"read_file":       "read",
	"list_dir":        "read",
	"grep":            "search",
	"glob":            "search",
	"write_file":      "write",
	"replace_in_file": "write",
	"run_command":     "exec",
}

// toolLoopHTTPClient has no client-level timeout: every turn is bounded by its
// own context deadline (min(60s, remaining wall budget)).
var toolLoopHTTPClient = &http.Client{}

// bridgeToolSet is the resolved loop allow-list: the OpenAI tool definitions
// attached to every upstream turn plus the matching ToolRunner flags.
type bridgeToolSet struct {
	allowWrite bool
	allowExec  bool
	names      []string
	defs       []agenttools.ToolDef
}

// resolveBridgeTools maps bridge-tools tokens to agenttools tool definitions
// fail-closed: unknown tokens are an error, "none"/blank tokens are skipped, an
// empty result disables the loop entirely (R1-6: allow-list is bridge-tools
// config only; model Capabilities.AllowedTools is fanout's domain).
func resolveBridgeTools(tokens []string) (*bridgeToolSet, error) {
	set := map[string]bool{}
	for _, raw := range tokens {
		tok := strings.ToLower(strings.TrimSpace(raw))
		if tok == "" || tok == "none" {
			continue
		}
		known := false
		for _, allowed := range bridgeToolTokenSet {
			if tok == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown bridge tool token %q (want read,search,write,exec)", tok)
		}
		set[tok] = true
	}
	if len(set) == 0 {
		return nil, nil
	}

	ts := filterToolDefs(agenttools.BuildOpenAITools(set["write"], set["exec"]), set)
	if len(ts.defs) == 0 {
		return nil, nil
	}
	return ts, nil
}

// filterToolDefs keeps only tools whose name maps to a granted bridge-tools
// token. Unmapped names are dropped (fail closed, F-06) on every allow-list, so
// a future BuildOpenAITools tool cannot ship until toolNameTokens maps it.
func filterToolDefs(defs []agenttools.ToolDef, set map[string]bool) *bridgeToolSet {
	ts := &bridgeToolSet{allowWrite: set["write"], allowExec: set["exec"]}
	for _, def := range defs {
		tok, known := toolNameTokens[def.Function.Name]
		if !known || !set[tok] {
			continue
		}
		ts.defs = append(ts.defs, def)
		ts.names = append(ts.names, def.Function.Name)
	}
	return ts
}

// maybeToolLoopTools returns the resolved allow-list when the request should
// take the bridge tool loop instead of the verbatim reverse proxy. All four
// conditions must hold (plan 8b R1-6/R1-7): loop config active, request has no
// `tools` key, model can use tools, allow-list non-empty.
func maybeToolLoopTools(cfg BridgeToolsConfig, rawReq map[string]any, target providers.Target) *bridgeToolSet {
	if !cfg.Enabled {
		return nil
	}
	if _, hasTools := rawReq["tools"]; hasTools {
		return nil
	}
	if !target.Model.Capabilities.CanUseTools() {
		return nil
	}
	tools, err := resolveBridgeTools(cfg.Tools)
	if err != nil || tools == nil {
		return nil
	}
	return tools
}

// resolveBridgeWorkspaceRoot returns AGY_PRO_WORKSPACE when set, else the shim
// cwd captured at StartBridge time (R1-3: env-over-cwd, own process only).
func resolveBridgeWorkspaceRoot() string {
	if v := strings.TrimSpace(os.Getenv(envBridgeWorkspace)); v != "" {
		return v
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// toolProtocolInstructions renders the <tool_instructions> block appended to
// the system prompt so the model knows the bridge tool protocol (DDC-5).
func toolProtocolInstructions(defs []agenttools.ToolDef) string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	var b strings.Builder
	b.WriteString("<tool_instructions>\n")
	b.WriteString("You can call workspace tools via tool_calls to inspect or change the workspace before answering.\n")
	b.WriteString("Available tools: ")
	b.WriteString(strings.Join(names, ", "))
	b.WriteString(".\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Tool paths are relative to the workspace root.\n")
	b.WriteString("- Each tool call is answered by a role \"tool\" message; never invent tool results.\n")
	b.WriteString("- Adjust arguments or paths when a tool returns an error string instead of repeating the same call.\n")
	b.WriteString("- When you have enough information, reply with the final answer as plain text and no further tool calls.\n")
	b.WriteString("</tool_instructions>")
	return b.String()
}

// injectToolInstructions appends the tool protocol block to messages[0] when it
// is a system/developer message, otherwise inserts a new system message at
// index 0 (DDC-5). Input messages are never mutated.
func injectToolInstructions(messages []any, block string) []any {
	out := make([]any, 0, len(messages)+1)
	if len(messages) > 0 {
		if m, ok := messages[0].(map[string]any); ok {
			role, _ := m["role"].(string)
			if role == "system" || role == "developer" {
				if content, isStr := m["content"].(string); isStr {
					clone := make(map[string]any, len(m))
					for k, v := range m {
						clone[k] = v
					}
					if strings.TrimSpace(content) == "" {
						clone["content"] = block
					} else {
						clone["content"] = content + "\n\n" + block
					}
					out = append(out, clone)
					out = append(out, messages[1:]...)
					return out
				}
			}
		}
	}
	out = append(out, map[string]any{"role": "system", "content": block})
	out = append(out, messages...)
	return out
}

func cloneMessages(raw any) []any {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]any, len(list))
	copy(out, list)
	return out
}

// toolLoop runs the aggregate-then-synthesize loop for one request. All state is
// request-local; nothing is shared between requests (plan 8b R1-3).
type toolLoop struct {
	target        providers.Target
	reqBody       map[string]any // request fields preserved verbatim (stream/stream_options/max_completion_tokens/...)
	messages      []any
	tools         *bridgeToolSet
	workspaceRoot string
	redact        func(string) string
	maxIter       int
	wallBudget    time.Duration
	turnBudget    time.Duration
	client        *http.Client

	runnerOnce sync.Once
	runner     *agenttools.ToolRunner
	loggerOnce sync.Once
	logger     *agenttools.Logger

	// toolBudget bounds a single tool execution (defaults to
	// toolLoopToolBudget; tests shrink it).
	toolBudget time.Duration

	// turnHook is a test seam invoked at the start of each iteration; the
	// production path leaves it nil.
	turnHook func(turn int)
	// execOverride is a test seam replacing tool dispatch; nil in production.
	execOverride func(name, argsJSON string) string

	// observe, when non-nil, receives every upstream turn as it completes so a
	// streaming caller can flush SSE to the client incrementally instead of
	// buffering the whole loop (M4 item 1). reasoning is this turn's own
	// reasoning trace (earlier turns were already observed); content and usage
	// are meaningful only when terminal is true, i.e. the final, tool-call-free
	// answer, whose usage is the cross-turn cumulative sum (M4 item 2). The
	// production non-streaming path leaves it nil, so run's return value stays
	// the single source of truth there.
	observe func(reasoning, content string, usage map[string]any, terminal bool)
}

// run executes the loop and ALWAYS returns a turn with non-empty content
// (R1-8): final text when upstream produced one, else the last non-empty text
// seen, else the fixed apology. Panic recovery lives here too.
func (lp *toolLoop) run(ctx context.Context) (out *openaiup.Turn) {
	var lastText string
	var reasoningLog strings.Builder
	// usageTurns collects every turn's usage block so the surfaced usage is the
	// cross-turn cumulative sum (main turn + tool turns), not just the final
	// turn's (M4 item 2).
	var usageTurns []map[string]any
	defer func() {
		if rec := recover(); rec != nil {
			out = lp.fallbackTurn(lastText, reasoningLog.String())
			out.Usage = sumUsage(usageTurns)
		}
	}()

	deadline := time.Now().Add(lp.wallBudget)
	messages := lp.messages

	for turn := 0; turn < lp.maxIter; turn++ {
		if lp.turnHook != nil {
			lp.turnHook(turn)
		}
		if ctx.Err() != nil {
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		turnBudget := lp.turnBudget
		if remaining < turnBudget {
			turnBudget = remaining
		}
		turnCtx, cancel := context.WithTimeout(ctx, turnBudget)
		agg, err := lp.upstreamTurn(turnCtx, messages)
		cancel()
		if err != nil {
			// Upstream failure mid-loop: fall back to text. Never relay a raw
			// error body into the synthesized answer (R1-8), but do record the
			// failure so the turn is diagnosable (F-03).
			lp.logUpstreamError(turn, err)
			break
		}
		if agg.Content() != "" {
			lastText = agg.Content()
		}
		reasoningLog.WriteString(agg.Reasoning())
		usageTurns = append(usageTurns, agg.Usage)

		// A turn with no tool calls and non-empty content is the final answer.
		// text+tool_calls in one turn is impossible here because tool_calls
		// always win (a turn carrying tool_calls is never terminal).
		if len(agg.OrderedCalls()) == 0 && strings.TrimSpace(agg.Content()) != "" {
			agg.Usage = sumUsage(usageTurns)
			if lp.observe != nil {
				// Stream this turn's own reasoning (earlier turns already
				// streamed theirs) then the answer, and close the SSE framing.
				lp.observe(agg.Reasoning(), agg.Content(), agg.Usage, true)
			}
			// Preserve the full reasoning trace across tool turns (R1-2).
			agg.SetReasoning(reasoningLog.String())
			return agg
		}

		// Not the final answer: stream this turn's reasoning so a streaming
		// client sees progress during the tool loop instead of blocking until
		// the whole loop finishes (M4 item 1). Any content in a tool turn is
		// discarded from the client stream (tool_calls win, R1-2) and is only
		// re-injected as the assistant message below.
		if lp.observe != nil {
			lp.observe(agg.Reasoning(), "", nil, false)
		}
		if len(agg.OrderedCalls()) == 0 {
			// A tool-call-free turn with empty content is not an answer: fall
			// through to the fallback below (R1-8).
			break
		}

		// Tool calls: execute, re-feed, continue (tool_calls outrank any text
		// produced in the same turn - R1-2).
		assistant := map[string]any{
			"role":       "assistant",
			"content":    contentOrNil(agg.Content()),
			"tool_calls": toolCallsPayload(agg),
		}
		messages = append(messages, assistant)
		// Tool execution inherits the remaining wall budget (F-04): the
		// 240s ceiling must hold during tool runs, not only between turns.
		toolCtx, cancelTools := context.WithTimeout(ctx, time.Until(deadline))
		messages = append(messages, lp.executeTools(toolCtx, agg)...)
		cancelTools()
	}

	out = lp.fallbackTurn(lastText, reasoningLog.String())
	out.Usage = sumUsage(usageTurns)
	return out
}

func (lp *toolLoop) fallbackTurn(lastText, lastReasoning string) *openaiup.Turn {
	out := openaiup.NewTurn()
	out.SetContent(lastText)
	out.SetReasoning(lastReasoning)
	if strings.TrimSpace(out.Content()) == "" {
		out.SetContent(toolLoopApology)
	}
	return out
}

// sumUsage aggregates token usage across every turn of a bridged request (the
// main turn plus every tool turn) so the usage surfaced to agy reflects the
// whole tool-augmented generation, not just the final turn (M4 item 2).
// prompt_tokens, completion_tokens, total_tokens and the per-turn cache-token
// counts are all summed across turns, so a cache ratio derived from the block
// is measured against the same summed prompt size instead of mixing
// granularities. The cache counts are written back in whichever spelling the
// turns carried: the nested prompt_tokens_details.cached_tokens (OpenAI) and/or
// the top-level prompt_cache_hit_tokens and prompt_cache_miss_tokens
// (DeepSeek-native); each spelling is summed only over the turns that reported
// it. The emitted hit and the emitted miss are therefore each a partial sum, so
// hit+miss equals the summed prompt only when every turn reported both, and a
// turn that omits miss contributes 0 to the miss sum. The last turn's remaining
// keys are kept as the base so the per-turn usage shape is preserved rather than
// broken, and a turn that reported no cache field contributes nothing (no zero
// is fabricated). Returns nil when no turn reported a usage block, matching the
// prior behavior for a provider that sends none.
func sumUsage(turns []map[string]any) map[string]any {
	var base map[string]any
	var prompt, completion, total float64
	var sawPrompt, sawCompletion, sawTotal bool
	var cached, cacheHit, cacheMiss float64
	var sawCached, sawCacheHit, sawCacheMiss bool
	for _, m := range turns {
		if m == nil {
			continue
		}
		base = m
		if v, ok := usageNum(m, "prompt_tokens"); ok {
			prompt += v
			sawPrompt = true
		}
		if v, ok := usageNum(m, "completion_tokens"); ok {
			completion += v
			sawCompletion = true
		}
		if v, ok := usageNum(m, "total_tokens"); ok {
			total += v
			sawTotal = true
		}
		// OpenAI nested shape: usage.prompt_tokens_details.cached_tokens.
		if details, ok := m["prompt_tokens_details"].(map[string]any); ok {
			if v, ok := usageNum(details, "cached_tokens"); ok {
				cached += v
				sawCached = true
			}
		}
		// DeepSeek-native shape: usage.prompt_cache_hit_tokens and its miss
		// counterpart usage.prompt_cache_miss_tokens, both top-level.
		if v, ok := usageNum(m, "prompt_cache_hit_tokens"); ok {
			cacheHit += v
			sawCacheHit = true
		}
		if v, ok := usageNum(m, "prompt_cache_miss_tokens"); ok {
			cacheMiss += v
			sawCacheMiss = true
		}
	}
	if base == nil {
		return nil
	}
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	if sawPrompt {
		out["prompt_tokens"] = prompt
	}
	if sawCompletion {
		out["completion_tokens"] = completion
	}
	if sawTotal {
		out["total_tokens"] = total
	}
	// The details block is copied before the summed value lands in it: out
	// starts as a shallow copy of the last turn's block, so writing through
	// out["prompt_tokens_details"] would otherwise rewrite the provider's own
	// turn map that is still referenced by the caller.
	if sawCached {
		details, _ := base["prompt_tokens_details"].(map[string]any)
		merged := make(map[string]any, len(details)+1)
		for k, v := range details {
			merged[k] = v
		}
		merged["cached_tokens"] = cached
		out["prompt_tokens_details"] = merged
	}
	if sawCacheHit {
		out["prompt_cache_hit_tokens"] = cacheHit
	}
	// prompt_cache_miss_tokens is summed the same way, in the same top-level
	// spelling the turns carried, so it is the sum over only the turns that
	// reported a miss; hit+miss equals the summed prompt only when every turn
	// reports both. float64 addition saturates to +Inf rather than wrapping, and
	// usageNum drops a negative term, so a huge or bogus count cannot wrap the
	// sum negative.
	if sawCacheMiss {
		out["prompt_cache_miss_tokens"] = cacheMiss
	}
	return out
}

// usageNum reads a token-count field from a usage block. A JSON-decoded usage
// block carries float64; json.Number, int and int64 are accepted too so a
// caller that builds a usage block directly (a test, a future non-JSON path)
// still sums correctly. A NEGATIVE count is treated as absent (ok=false), the
// same rule geminiwire's usageInt applies, so a bogus negative is never
// accumulated into a summed total.
func usageNum(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case float32:
		f = float64(n)
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f < 0 {
		return 0, false
	}
	return f, true
}

// upstreamTurn performs one raw HTTP call to the provider. The request keeps
// every original body field; only model/messages/tools are set per turn. The
// transport itself lives in openaiup.Call so the bridge and the geminiwire
// adapter share one implementation.
func (lp *toolLoop) upstreamTurn(ctx context.Context, messages []any) (*openaiup.Turn, error) {
	body := make(map[string]any, len(lp.reqBody)+3)
	for k, v := range lp.reqBody {
		body[k] = v
	}
	body["model"] = lp.target.Model.ID
	body["messages"] = messages
	body["tools"] = lp.tools.defs

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize upstream request: %w", err)
	}
	// M4 item 4: the loop grows the message history on every turn, so cap the
	// outbound body before it reaches the provider. An over-cap body fails
	// loudly (this error is logged by logUpstreamError and the loop falls back
	// to text); it is never silently truncated.
	if len(payload) > maxOutboundBodyBytes {
		return nil, fmt.Errorf("outbound request body is %d bytes, over the %d byte cap", len(payload), maxOutboundBodyBytes)
	}
	return openaiup.Call(ctx, lp.client, lp.target, payload, lp.redact)
}

// auditLogger lazily builds the redacting JSONL audit logger shared by tool
// executions and upstream-turn failures (R1-4a, F-03). The log file is created
// on first use so tool-less requests do not litter the log directory.
func (lp *toolLoop) auditLogger() *agenttools.Logger {
	lp.loggerOnce.Do(func() {
		var logger *agenttools.Logger
		if home, err := providers.Home(); err == nil {
			ts := time.Now().UTC().Format("20060102-150405.000")
			logPath := filepath.Join(home, "logs", fmt.Sprintf("bridge-tools-%s.jsonl", ts))
			logger, _ = agenttools.NewLogger(logPath, lp.redact)
		}
		lp.logger = logger
	})
	return lp.logger
}

// logUpstreamError records a failed upstream turn in the audit log and on
// stderr (F-03). The error string already carries only a redacted snippet of
// the upstream body; the logger redacts again, so the raw body never lands in
// either sink. The provider and model names go through MaskOperand at both
// sinks: a key-shaped value misdirected into a registry slot must not render
// raw in the durable JSONL or on stderr.
func (lp *toolLoop) logUpstreamError(turn int, err error) {
	if err == nil {
		return
	}
	msg := lp.redact(err.Error())
	provName := providers.MaskOperand(lp.target.Provider.Name)
	modelID := providers.MaskOperand(lp.target.Model.ID)
	log.Printf("[bridge] tool loop upstream turn %d failed for %s/%s: %s",
		turn, provName, modelID, msg)
	if lg := lp.auditLogger(); lg != nil {
		_ = lg.Log(agenttools.LogEntry{
			SeatID:    0,
			Model:     modelID,
			Provider:  provName,
			Tool:      "upstream_turn",
			Arguments: fmt.Sprintf(`{"turn":%d}`, turn),
			Error:     msg,
		})
	}
}

// toolRunner lazily builds a ToolRunner bound to the audit logger (R1-4a).
func (lp *toolLoop) toolRunner() *agenttools.ToolRunner {
	lp.runnerOnce.Do(func() {
		r := agenttools.NewRunner(lp.workspaceRoot, lp.tools.allowWrite, lp.tools.allowExec, lp.auditLogger())
		r.AllowedModelTools = lp.tools.names
		lp.runner = r
	})
	return lp.runner
}

// executeTools runs each aggregated call and builds role:"tool" messages. Every
// output is passed through the registry Redactor before re-injection (R1-4b);
// the runner's audit log redacts the same strings (R1-4a). Each dispatch is
// bounded by the per-tool deadline and by the wall-budget context (F-04), and
// calls beyond the per-turn cap fail without executing (security M-1). Every
// aggregated call still gets a tool message so the assistant/tool pairing in
// the re-fed history stays protocol-complete.
func (lp *toolLoop) executeTools(ctx context.Context, agg *openaiup.Turn) []any {
	calls := agg.OrderedCalls()
	out := make([]any, 0, len(calls))
	for i, call := range calls {
		name := call.Name
		if name == "" {
			name = "unknown"
		}
		var content string
		switch {
		case i >= toolLoopMaxToolCallsPerTurn:
			content = fmt.Sprintf("Error: tool call dropped: per-turn limit of %d tool calls exceeded", toolLoopMaxToolCallsPerTurn)
		case ctx.Err() != nil:
			content = "Error: tool loop cancelled before execution"
		default:
			content = lp.executeOne(ctx, name, call.Args)
		}
		out = append(out, map[string]any{
			"role":         "tool",
			"tool_call_id": agg.CallID(i),
			"name":         name,
			"content":      lp.redact(content),
		})
	}
	return out
}

// executeOne dispatches a single tool under the per-tool deadline and the
// loop's wall-budget context. agenttools.ToolRunner.Execute takes no context,
// so the call runs in a goroutine and the result is collected or abandoned at
// the deadline (F-04). An abandoned call keeps running until the tool layer's
// own caps stop it; its late result is discarded.
func (lp *toolLoop) executeOne(ctx context.Context, name, argsJSON string) string {
	dispatch := func() string {
		if lp.execOverride != nil {
			return lp.execOverride(name, argsJSON)
		}
		out, _ := lp.toolRunner().Execute(0, lp.target.Model.ID, lp.target.Provider.Name, name, argsJSON)
		return out
	}
	budget := lp.toolBudget
	if budget <= 0 {
		budget = toolLoopToolBudget
	}
	type result struct{ out string }
	ch := make(chan result, 1)
	go func() {
		ch <- result{dispatch()}
	}()
	select {
	case r := <-ch:
		return r.out
	case <-ctx.Done():
		return "Error: tool loop cancelled during execution"
	case <-time.After(budget):
		return fmt.Sprintf("Error: tool execution exceeded the %s deadline", budget)
	}
}

func toolCallsPayload(agg *openaiup.Turn) []map[string]any {
	calls := agg.OrderedCalls()
	list := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		typ := call.Type
		if typ == "" {
			typ = "function"
		}
		list = append(list, map[string]any{
			"id":   agg.CallID(i),
			"type": typ,
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Args,
			},
		})
	}
	return list
}

func contentOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// loopEnv carries everything serveToolLoop needs from handleChatCompletions.
type loopEnv struct {
	rawReq   map[string]any
	target   providers.Target
	modelReq string
	tools    *bridgeToolSet
	root     string
	redact   func(string) string
	// turnHook is a test seam; nil in production.
	turnHook func(turn int)
}

// serveToolLoop runs the tool loop and synthesizes the response agy consumes:
// a chat.completion.chunk SSE stream for stream:true, a single chat.completion
// JSON body otherwise (R1-1). Every exit path ends with non-empty text +
// finish_reason "stop" + [DONE] (R1-8).
//
// The gateway path is XOR and tool-less on agy 1.3.5 (design V8): agy's gateway
// client never sends `tools` and discards response `tool_calls`, so this loop
// is the escape hatch that re-attaches tools, runs them locally, and
// synthesizes a text-only reply. It is NOT the native-coexistence path (that is
// the mesh); it hardens the existing escape hatch, it does not widen it.
func serveToolLoop(w http.ResponseWriter, r *http.Request, env loopEnv) {
	stream := false
	if s, ok := env.rawReq["stream"].(bool); ok {
		stream = s
	}

	redact := env.redact
	if redact == nil {
		// Fail closed: tool outputs and upstream error text are the two places
		// uncontrolled text reaches a sink, so a missing redactor still masks
		// the generic credential shapes.
		redact = providers.NewRedactor()
	}

	reqBody := make(map[string]any, len(env.rawReq))
	for k, v := range env.rawReq {
		reqBody[k] = v
	}
	delete(reqBody, "messages")
	delete(reqBody, "tools")

	lp := &toolLoop{
		target:        env.target,
		reqBody:       reqBody,
		messages:      injectToolInstructions(cloneMessages(env.rawReq["messages"]), toolProtocolInstructions(env.tools.defs)),
		tools:         env.tools,
		workspaceRoot: env.root,
		redact:        redact,
		maxIter:       toolLoopMaxIterations,
		wallBudget:    toolLoopWallBudget,
		turnBudget:    toolLoopTurnBudget,
		client:        toolLoopHTTPClient,
		turnHook:      env.turnHook,
	}

	id := openaiup.NewCompletionID()
	created := time.Now().Unix()

	if !stream {
		// Non-streaming: aggregate the whole loop, then emit one JSON body.
		// Built before the writer is touched so a panic during synthesis still
		// leaves room for the R1-8 fallback body.
		var jsonBody []byte
		responded := false
		defer func() {
			if rec := recover(); rec != nil && !responded {
				writeFallbackJSON(w, env.modelReq, id, created)
			}
		}()
		out := lp.run(r.Context())
		jsonBody, err := json.Marshal(buildCompletion(env.modelReq, id, created, out))
		if err != nil {
			jsonBody = nil
		}
		responded = true
		if jsonBody == nil {
			out = lp.fallbackTurn(out.Content(), out.Reasoning())
			jsonBody, _ = json.Marshal(buildCompletion(env.modelReq, id, created, out))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jsonBody)
		return
	}

	// Streaming (M4 item 1): open the SSE stream immediately, then flush each
	// turn's reasoning as the loop completes it and the final answer when the
	// loop reaches it, so a long tool-augmented generation streams incrementally
	// instead of blocking until the whole loop finishes (the non-tool reverse
	// proxy streams the same way, via FlushInterval: -1). Every path still ends
	// with non-empty text + finish_reason "stop" + [DONE] (R1-8).
	flusher, _ := w.(http.Flusher)
	sw := &sseStream{w: w, flusher: flusher, id: id, created: created, model: env.modelReq}
	sw.open()

	finished := false
	lp.observe = func(reasoning, content string, usage map[string]any, terminal bool) {
		for _, piece := range openaiup.SplitPieces(reasoning, 2048) {
			sw.delta(map[string]any{"reasoning_content": piece}, nil, nil)
		}
		if !terminal {
			return
		}
		for _, piece := range openaiup.SplitPieces(content, 2048) {
			sw.delta(map[string]any{"content": piece}, nil, nil)
		}
		sw.delta(map[string]any{}, "stop", usage)
		sw.done()
		finished = true
	}

	out := lp.run(r.Context())
	if !finished {
		// The loop ended without a final-answer turn (iteration exhaustion,
		// timeout, upstream error, or a panic the loop recovered from): the
		// per-turn reasoning already streamed, so emit only the fallback text to
		// close the R1-8 framing. The cross-turn cumulative usage rides on the
		// last chunk.
		for _, piece := range openaiup.SplitPieces(out.Content(), 2048) {
			sw.delta(map[string]any{"content": piece}, nil, nil)
		}
		sw.delta(map[string]any{}, "stop", out.Usage)
		sw.done()
	}
}

// sseStream writes one synthesized chat.completion.chunk SSE response to agy,
// flushing after every chunk so a long tool-augmented generation streams
// incrementally (M4 item 1). It carries the R1-8 framing: a role delta first,
// then reasoning/content deltas, then finish_reason "stop" with the cumulative
// usage, then the [DONE] sentinel.
type sseStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
	id      string
	created int64
	model   string
}

// open writes the SSE headers, the 200 status, and the opening role delta, so
// the client sees the stream is live before the first upstream turn completes.
func (s *sseStream) open() {
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	s.w.WriteHeader(http.StatusOK)
	s.delta(map[string]any{"role": "assistant"}, nil, nil)
}

// delta writes one chunk carrying delta, the finish_reason (nil on non-final
// chunks), and the usage block (only on the final chunk), then flushes.
func (s *sseStream) delta(delta map[string]any, finish any, usage map[string]any) {
	env := map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]any{
			{"index": 0, "delta": delta, "finish_reason": finish},
		},
	}
	if usage != nil {
		env["usage"] = usage
	}
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	_, _ = io.WriteString(s.w, "data: "+string(b)+"\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// done writes the terminal [DONE] sentinel and flushes.
func (s *sseStream) done() {
	_, _ = io.WriteString(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// writeFallbackJSON is the non-streaming panic path: still a complete,
// non-empty chat.completion body (R1-8). The streaming path has no equivalent
// because it writes incrementally and closes its own framing (see
// serveToolLoop).
func writeFallbackJSON(w http.ResponseWriter, modelReq, id string, created int64) {
	out := openaiup.NewTurn()
	out.SetContent(toolLoopApology)
	body, _ := json.Marshal(buildCompletion(modelReq, id, created, out))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// buildCompletion renders the single chat.completion body for non-streaming
// requests (R1-1).
func buildCompletion(model, id string, created int64, out *openaiup.Turn) map[string]any {
	msg := map[string]any{"role": "assistant", "content": out.Content()}
	if out.Reasoning() != "" {
		msg["reasoning_content"] = out.Reasoning()
	}
	env := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{"index": 0, "message": msg, "finish_reason": "stop"},
		},
	}
	if out.Usage != nil {
		env["usage"] = out.Usage
	}
	return env
}

// isGeminiWirePath reports whether an inbound path is the Gemini native wire
// (v1beta streamGenerateContent). The bridge serves the OpenAI wire only, so
// these are rejected with an explicit logged 404 instead of a silent catch-all
// (R1-10).
func isGeminiWirePath(path string) bool {
	if strings.Contains(path, "streamGenerateContent") || strings.Contains(path, ":generateContent") {
		return true
	}
	return strings.HasPrefix(path, "/v1beta/")
}
