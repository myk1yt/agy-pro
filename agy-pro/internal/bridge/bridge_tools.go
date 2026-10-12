package bridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"unicode/utf8"

	"agypro/internal/agenttools"
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

// wireToolCall is one OpenAI tool_call as it appears in stream deltas and in
// non-streaming messages; `index` is optional and drives the delta merge.
type wireToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// wireMessage covers both stream `delta` and non-stream `message` shapes.
type wireMessage struct {
	Role             string         `json:"role"`
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content"`
	ToolCalls        []wireToolCall `json:"tool_calls"`
}

type wireChoice struct {
	Index        int         `json:"index"`
	Delta        wireMessage `json:"delta"`
	Message      wireMessage `json:"message"`
	FinishReason *string     `json:"finish_reason"`
}

type wireChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []wireChoice   `json:"choices"`
	Usage   map[string]any `json:"usage"`
}

// toolCallAgg accumulates one tool call across index-aware deltas (R1-2:
// arguments routinely arrive split over several chunks).
type toolCallAgg struct {
	id   string
	typ  string
	name string
	args strings.Builder
}

// turnAgg is the aggregated result of one upstream turn.
type turnAgg struct {
	content   string
	reasoning string
	calls     map[int]*toolCallAgg
	order     []int
	usage     map[string]any
}

func newTurnAgg() *turnAgg {
	return &turnAgg{calls: map[int]*toolCallAgg{}}
}

func (a *turnAgg) mergeChunk(c *wireChunk) {
	if c.Usage != nil {
		a.usage = c.Usage
	}
	for i := range c.Choices {
		a.mergeMessage(&c.Choices[i].Delta)
		a.mergeMessage(&c.Choices[i].Message)
	}
}

func (a *turnAgg) mergeMessage(m *wireMessage) {
	if m == nil {
		return
	}
	if m.Content != nil {
		a.content += *m.Content
	}
	if m.ReasoningContent != nil {
		a.reasoning += *m.ReasoningContent
	}
	for i := range m.ToolCalls {
		a.mergeToolCall(&m.ToolCalls[i])
	}
}

func (a *turnAgg) mergeToolCall(tc *wireToolCall) {
	var slot *toolCallAgg
	if tc.Index != nil {
		slot = a.calls[*tc.Index]
	} else if tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments != "" && len(a.order) > 0 {
		// Continuation chunk that omitted index: append to the newest call.
		slot = a.calls[a.order[len(a.order)-1]]
	}
	if slot == nil {
		idx := len(a.order)
		if tc.Index != nil {
			idx = *tc.Index
		}
		for {
			if _, exists := a.calls[idx]; !exists {
				break
			}
			idx++
		}
		slot = &toolCallAgg{}
		a.calls[idx] = slot
		a.order = append(a.order, idx)
	}
	if tc.ID != "" {
		slot.id = tc.ID
	}
	if tc.Type != "" {
		slot.typ = tc.Type
	}
	if tc.Function.Name != "" {
		slot.name = tc.Function.Name
	}
	if tc.Function.Arguments != "" {
		slot.args.WriteString(tc.Function.Arguments)
	}
}

// callID returns the wire id of the i-th aggregated call, synthesizing a stable
// fallback when the upstream omitted it (assistant and tool messages must agree).
func (a *turnAgg) callID(i int) string {
	if i >= 0 && i < len(a.order) {
		if call := a.calls[a.order[i]]; call != nil && call.id != "" {
			return call.id
		}
	}
	return fmt.Sprintf("call_%d", i)
}

func (a *turnAgg) mergeSSE(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk wireChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		a.mergeChunk(&chunk)
	}
	return sc.Err()
}

func (a *turnAgg) mergeJSON(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, maxChatBodyBytes))
	if err != nil {
		return fmt.Errorf("failed to read upstream response: %w", err)
	}
	var chunk wireChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return fmt.Errorf("failed to parse upstream response: %w", err)
	}
	a.mergeChunk(&chunk)
	return nil
}

// toolLoop runs the aggregate-then-synthesize loop for one request. All state is
// request-local; nothing is shared between requests (plan 8b R1-3).
type toolLoop struct {
	target        providers.Target
	apiKey        string
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
}

// run executes the loop and ALWAYS returns a turn with non-empty content
// (R1-8): final text when upstream produced one, else the last non-empty text
// seen, else the fixed apology. Panic recovery lives here too.
func (lp *toolLoop) run(ctx context.Context) (out *turnAgg) {
	var lastText string
	var reasoningLog strings.Builder
	defer func() {
		if rec := recover(); rec != nil {
			out = lp.fallbackTurn(lastText, reasoningLog.String())
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
		if agg.content != "" {
			lastText = agg.content
		}
		reasoningLog.WriteString(agg.reasoning)

		if len(agg.order) == 0 {
			// Final answer turn. text+tool_calls in one turn is impossible here
			// because tool_calls always win (checked above).
			if strings.TrimSpace(agg.content) == "" {
				return lp.fallbackTurn(lastText, reasoningLog.String())
			}
			// Preserve the full reasoning trace across tool turns (R1-2).
			agg.reasoning = reasoningLog.String()
			return agg
		}

		// Tool calls: execute, re-feed, continue (tool_calls outrank any text
		// produced in the same turn - R1-2).
		assistant := map[string]any{
			"role":       "assistant",
			"content":    contentOrNil(agg.content),
			"tool_calls": toolCallsPayload(agg),
		}
		messages = append(messages, assistant)
		// Tool execution inherits the remaining wall budget (F-04): the
		// 240s ceiling must hold during tool runs, not only between turns.
		toolCtx, cancelTools := context.WithTimeout(ctx, time.Until(deadline))
		messages = append(messages, lp.executeTools(toolCtx, agg)...)
		cancelTools()
	}

	return lp.fallbackTurn(lastText, reasoningLog.String())
}

func (lp *toolLoop) fallbackTurn(lastText, lastReasoning string) *turnAgg {
	out := &turnAgg{content: lastText, reasoning: lastReasoning}
	if strings.TrimSpace(out.content) == "" {
		out.content = toolLoopApology
	}
	return out
}

// upstreamTurn performs one raw HTTP call to the provider. The request keeps
// every original body field; only model/messages/tools are set per turn.
func (lp *toolLoop) upstreamTurn(ctx context.Context, messages []any) (*turnAgg, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, lp.target.Provider.Endpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if lp.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+lp.apiKey)
	}

	resp, err := lp.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, lp.redact(string(snippet)))
	}

	agg := newTurnAgg()
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		if err := agg.mergeSSE(resp.Body); err != nil {
			return nil, err
		}
	} else {
		if err := agg.mergeJSON(resp.Body); err != nil {
			return nil, err
		}
	}
	return agg, nil
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
// either sink.
func (lp *toolLoop) logUpstreamError(turn int, err error) {
	if err == nil {
		return
	}
	msg := lp.redact(err.Error())
	log.Printf("[bridge] tool loop upstream turn %d failed for %s/%s: %s",
		turn, lp.target.Provider.Name, lp.target.Model.ID, msg)
	if lg := lp.auditLogger(); lg != nil {
		_ = lg.Log(agenttools.LogEntry{
			SeatID:    0,
			Model:     lp.target.Model.ID,
			Provider:  lp.target.Provider.Name,
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
func (lp *toolLoop) executeTools(ctx context.Context, agg *turnAgg) []any {
	out := make([]any, 0, len(agg.order))
	for i, idx := range agg.order {
		call := agg.calls[idx]
		if call == nil {
			continue
		}
		name := call.name
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
			content = lp.executeOne(ctx, name, call.args.String())
		}
		out = append(out, map[string]any{
			"role":         "tool",
			"tool_call_id": agg.callID(i),
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

func toolCallsPayload(agg *turnAgg) []map[string]any {
	list := make([]map[string]any, 0, len(agg.order))
	for i, idx := range agg.order {
		call := agg.calls[idx]
		if call == nil {
			continue
		}
		typ := call.typ
		if typ == "" {
			typ = "function"
		}
		list = append(list, map[string]any{
			"id":   agg.callID(i),
			"type": typ,
			"function": map[string]any{
				"name":      call.name,
				"arguments": call.args.String(),
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

func newCompletionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(buf)
}

// splitPieces splits s into pieces of at most size bytes, cutting only at UTF-8
// rune boundaries (F-01): byte-level slicing would split multi-byte runes
// (Korean/CJK/emoji) and json.Marshal would then coerce each half to U+FFFD in
// the synthesized SSE deltas.
func splitPieces(s string, size int) []string {
	if s == "" {
		return nil
	}
	if size <= 0 || len(s) <= size {
		return []string{s}
	}
	var out []string
	for len(s) > size {
		cut := size
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// Defensive: a rune larger than the piece size still gets a
			// whole-rune piece instead of a corrupted fragment.
			_, n := utf8.DecodeRuneInString(s)
			if n <= 0 {
				n = 1
			}
			out = append(out, s[:n])
			s = s[n:]
			continue
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// loopEnv carries everything serveToolLoop needs from handleChatCompletions.
type loopEnv struct {
	rawReq   map[string]any
	target   providers.Target
	apiKey   string
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
func serveToolLoop(w http.ResponseWriter, r *http.Request, env loopEnv) {
	stream := false
	if s, ok := env.rawReq["stream"].(bool); ok {
		stream = s
	}

	redact := env.redact
	if redact == nil {
		redact = func(s string) string { return s }
	}

	reqBody := make(map[string]any, len(env.rawReq))
	for k, v := range env.rawReq {
		reqBody[k] = v
	}
	delete(reqBody, "messages")
	delete(reqBody, "tools")

	lp := &toolLoop{
		target:        env.target,
		apiKey:        env.apiKey,
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

	id := newCompletionID()
	created := time.Now().Unix()

	// Build every response byte before touching the writer, so a panic during
	// synthesis still leaves room for the R1-8 fallback response.
	var sseEvents []string
	var jsonBody []byte
	responded := false

	defer func() {
		if rec := recover(); rec != nil && !responded {
			writeFallbackResponse(w, stream, env.modelReq, id, created)
		}
	}()

	out := lp.run(r.Context())
	if stream {
		sseEvents = buildSSEEvents(env.modelReq, id, created, out)
	} else {
		var err error
		jsonBody, err = json.Marshal(buildCompletion(env.modelReq, id, created, out))
		if err != nil {
			jsonBody = nil
		}
	}

	responded = true
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ev := range sseEvents {
			_, _ = io.WriteString(w, ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}

	if jsonBody == nil {
		out = lp.fallbackTurn(out.content, out.reasoning)
		jsonBody, _ = json.Marshal(buildCompletion(env.modelReq, id, created, out))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(jsonBody)
}

// writeFallbackResponse is the panic path: still a complete non-empty answer.
func writeFallbackResponse(w http.ResponseWriter, stream bool, modelReq, id string, created int64) {
	out := &turnAgg{content: toolLoopApology}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ev := range buildSSEEvents(modelReq, id, created, out) {
			_, _ = io.WriteString(w, ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}
	body, _ := json.Marshal(buildCompletion(modelReq, id, created, out))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// buildSSEEvents renders the synthesized chat.completion.chunk stream:
// reasoning deltas preserved, content deltas, finish_reason "stop", usage on
// the last chunk, then [DONE] (R1-2).
func buildSSEEvents(model, id string, created int64, out *turnAgg) []string {
	events := make([]string, 0, 8)
	emit := func(delta map[string]any, finish any, usage map[string]any) {
		env := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
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
		events = append(events, "data: "+string(b)+"\n\n")
	}

	emit(map[string]any{"role": "assistant"}, nil, nil)
	for _, piece := range splitPieces(out.reasoning, 2048) {
		emit(map[string]any{"reasoning_content": piece}, nil, nil)
	}
	for _, piece := range splitPieces(out.content, 2048) {
		emit(map[string]any{"content": piece}, nil, nil)
	}
	emit(map[string]any{}, "stop", out.usage)
	events = append(events, "data: [DONE]\n\n")
	return events
}

// buildCompletion renders the single chat.completion body for non-streaming
// requests (R1-1).
func buildCompletion(model, id string, created int64, out *turnAgg) map[string]any {
	msg := map[string]any{"role": "assistant", "content": out.content}
	if out.reasoning != "" {
		msg["reasoning_content"] = out.reasoning
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
	if out.usage != nil {
		env["usage"] = out.usage
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
